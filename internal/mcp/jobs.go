// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package mcp

// jobs.go — async job registry for long-running pack calls.
//
// Why this exists: the MCP TypeScript SDK (which OpenClaw and most
// JS-based MCP clients are built on) defaults to a 60-second
// per-request JSON-RPC timeout AND defaults `resetTimeoutOnProgress`
// to false (issue #245, PR #849 rejected Sep 2025). That means even
// a perfectly spec-compliant `notifications/progress` stream from
// our server will not save heavy packs (slides.narrate,
// research.deep, content.ground rewrite, future book-writing
// workflows) from MCP error -32001 "Request timed out" on those
// clients.
//
// The fix is the pattern OpenClaw uses for its own long-running
// tools: split the call in two. `pack.start` returns a job_id
// immediately (well within any reasonable timeout), the work runs
// in a background goroutine, and the client polls `pack.status` —
// each poll is a tiny new request with its own fresh timeout — until
// state == "done", then `pack.result` retrieves the final payload.
//
// SKILLS.md teaches the agent: prefer the async path for known-heavy
// packs. The sync path stays available for clients that handle long
// calls fine (Claude Desktop, MCP Inspector with --reset-on-progress,
// Python-SDK clients).

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/tosin2013/helmdeck/internal/packs"
)

// jobTTL is how long a finished job's result stays in the registry
// before the sweeper evicts it. 1 hour leaves comfortable headroom
// for an agent that fires `pack.start` and then walks away to do
// other thinking before polling `pack.result`.
const jobTTL = 1 * time.Hour

// jobSweepInterval is how often the background goroutine scans the
// registry for expired jobs. Doesn't need to be precise.
const jobSweepInterval = 5 * time.Minute

// asyncJob is one in-flight or completed pack execution. The mutex
// guards the mutable fields (state, progress, message, result, err,
// endedAt) — startedAt and the immutable identity fields are safe
// to read without it.
type asyncJob struct {
	ID        string
	Pack      string
	StartedAt time.Time

	// Webhook fields, set at job creation time when the caller wants
	// helmdeck to POST the final result to a URL on completion. See
	// docs/integrations/webhooks.md for the wire contract. Empty URL
	// disables webhook delivery — the job still tracks state through
	// pack.status / tasks/get either way.
	WebhookURL    string
	WebhookSecret string

	mu       sync.Mutex
	state    string // "working" | "completed" | "failed" | "cancelled"
	progress float64
	message  string
	result   *packs.Result
	err      error
	endedAt  time.Time
	cancel   context.CancelFunc
}

// jobSnapshot is the legacy `pack.status` response shape (kept for
// backward compatibility with the pack.start/status/result trio
// that shipped in commit 4e77494). State strings here ALSO use the
// SEP-1686 vocabulary ("working"/"completed"/"failed"/"cancelled")
// so the two paths return identical state values — clients can use
// either entrypoint without translation.
type jobSnapshot struct {
	JobID     string  `json:"job_id"`
	Pack      string  `json:"pack"`
	State     string  `json:"state"`
	Progress  float64 `json:"progress"`
	Message   string  `json:"message,omitempty"`
	StartedAt string  `json:"started_at"`
	EndedAt   string  `json:"ended_at,omitempty"`
	Error     string  `json:"error,omitempty"`
}

func (j *asyncJob) snapshot() jobSnapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := jobSnapshot{
		JobID:     j.ID,
		Pack:      j.Pack,
		State:     j.state,
		Progress:  j.progress,
		Message:   j.message,
		StartedAt: j.StartedAt.UTC().Format(time.RFC3339),
	}
	if !j.endedAt.IsZero() {
		out.EndedAt = j.endedAt.UTC().Format(time.RFC3339)
	}
	if j.err != nil {
		out.Error = j.err.Error()
	}
	return out
}

// taskID returns the SEP-1686 task identifier. We prefix our internal
// job IDs with "pack_" so the value reads as a task reference in
// MCP-Inspector and other generic tooling rather than an opaque hex
// blob. Backward-compatible: pack.status/result also accept the bare
// hex form for jobs created before this prefix was introduced.
func (j *asyncJob) taskID() string {
	return "pack_" + j.ID
}

// jobRegistry holds active and recently-completed async jobs. The
// sweeper goroutine evicts entries older than jobTTL after they
// reach a terminal state.
type jobRegistry struct {
	mu   sync.Mutex
	jobs map[string]*asyncJob
}

func newJobRegistry() *jobRegistry {
	r := &jobRegistry{jobs: make(map[string]*asyncJob)}
	go r.sweepLoop(context.Background())
	return r
}

func (r *jobRegistry) put(j *asyncJob) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.jobs[j.ID] = j
}

func (r *jobRegistry) get(id string) (*asyncJob, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	return j, ok
}

func (r *jobRegistry) drop(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.jobs, id)
}

// sweepLoop runs forever, scanning every jobSweepInterval and
// dropping terminal jobs whose endedAt is older than jobTTL.
// Process-scoped context — the registry lives for the life of the
// PackServer, which is process-wide. No shutdown path needed.
func (r *jobRegistry) sweepLoop(ctx context.Context) {
	t := time.NewTicker(jobSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.sweep(time.Now())
		}
	}
}

func (r *jobRegistry) sweep(now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, j := range r.jobs {
		j.mu.Lock()
		expired := !j.endedAt.IsZero() && now.Sub(j.endedAt) > jobTTL
		j.mu.Unlock()
		if expired {
			delete(r.jobs, id)
		}
	}
}

// asyncOptions configures startAsync. Zero value is "no webhook,
// no extras" — the most common case from the SEP-1686 task envelope
// path. The pack.start tool fills WebhookURL/WebhookSecret when the
// caller asked for push delivery.
type asyncOptions struct {
	WebhookURL    string
	WebhookSecret string
}

// startAsync spawns a goroutine that runs engine.Execute for pack
// with input, capturing progress into the job. The returned job is
// already registered and in state "working"; callers should not
// mutate it directly — webhook fields and any other knobs must be
// passed via opts so the goroutine sees a fully-initialised job
// from the moment it spawns (no data race).
//
// The goroutine uses a detached context (context.Background) because
// the SSE/WS request that triggered the start may close before the
// pack finishes — that's the whole point of the async pattern. We
// keep the cancel handle on the job for a future `pack.cancel` tool.
func (s *PackServer) startAsync(pack *packs.Pack, input json.RawMessage, opts asyncOptions) *asyncJob {
	jobCtx, cancel := context.WithCancel(context.Background())
	j := &asyncJob{
		ID:            newJobID(),
		Pack:          pack.Name,
		StartedAt:     time.Now().UTC(),
		state:         "working",
		cancel:        cancel,
		WebhookURL:    opts.WebhookURL,
		WebhookSecret: opts.WebhookSecret,
	}
	s.jobs.put(j)

	progress := func(pct float64, message string) {
		j.mu.Lock()
		j.progress = pct
		if message != "" {
			j.message = message
		}
		j.mu.Unlock()
	}
	jobCtx = packs.WithProgress(jobCtx, progress)

	go func() {
		defer cancel()
		res, err := s.engine.Execute(jobCtx, pack, input)
		j.mu.Lock()
		j.endedAt = time.Now().UTC()
		if err != nil {
			j.state = "failed"
			j.err = err
		} else {
			j.state = "completed"
			j.result = res
			j.progress = 100
		}
		j.mu.Unlock()
		// Webhook fan-out runs after the lock is released so a slow
		// receiver can't stall pack.status / tasks/get readers waiting
		// on the same job. fireWebhook is a no-op when WebhookURL is
		// empty.
		s.fireWebhook(j)
	}()

	return j
}

// newJobID returns a short random identifier for a job. 128 bits is
// plenty — collision probability is negligible at any realistic
// concurrent-job count.
func newJobID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// taskEnvelope returns the SEP-1686-shaped CallToolResult that MCP
// servers send instead of a full result when starting an async pack.
// Wire shape:
//
//	{
//	  "_meta": { "modelcontextprotocol.io/related-task": { "taskId": "pack_<hex>" } },
//	  "content": [{"type":"text","text":"<job snapshot json>"}],
//	  "isError": false
//	}
//
// The text content carries the same JSON snapshot pack.status would
// return — agents that don't yet speak SEP-1686 can still parse the
// taskId / state out of it without learning new semantics.
func (j *asyncJob) taskEnvelope() map[string]any {
	body, _ := json.Marshal(j.snapshot())
	return map[string]any{
		"_meta": map[string]any{
			"modelcontextprotocol.io/related-task": map[string]any{
				"taskId": j.taskID(),
			},
		},
		"content": []map[string]any{
			{"type": "text", "text": string(body)},
		},
		"isError": false,
	}
}

// taskGetResult builds the SEP-1686 tasks/get response. When the job
// is still working, only state + progress + pollFrequency are sent;
// once completed, the full CallToolResult is inlined under "result"
// so the client never has to make a follow-up call to retrieve the
// payload. A pollFrequency of 5000ms is a deliberate compromise: low
// enough that a 30s pack feels responsive, high enough that a 3-min
// pack doesn't generate 36 status pings.
func (s *PackServer) taskGetResult(ctx context.Context, j *asyncJob) map[string]any {
	j.mu.Lock()
	state := j.state
	progress := j.progress
	message := j.message
	res := j.result
	jobErr := j.err
	endedAt := j.endedAt
	j.mu.Unlock()

	out := map[string]any{
		"taskId":        j.taskID(),
		"status":        state,
		"progress":      progress,
		"pollFrequency": 5000,
	}
	if message != "" {
		out["message"] = message
	}
	if !endedAt.IsZero() {
		out["endedAt"] = endedAt.UTC().Format(time.RFC3339)
	}
	switch state {
	case "completed":
		out["result"] = s.packResultAsToolResult(ctx, res)
	case "failed":
		out["result"] = packErrorAsToolResult(jobErr)
	}
	return out
}

// asyncPackTools are the three MCP tools exposed by the async layer.
// They show up in tools/list alongside every regular pack so the
// LLM can discover them. Schemas are intentionally permissive on the
// `input` field — the async layer is a thin wrapper, the wrapped
// pack's own schema is what ultimately validates the payload.
func asyncPackTools() []Tool {
	startSchema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pack":           map[string]any{"type": "string", "description": "Pack name to run asynchronously, e.g. \"slides.narrate\""},
			"input":          map[string]any{"type": "object", "description": "Arguments object that would normally be passed directly to the pack."},
			"webhook_url":    map[string]any{"type": "string", "description": "Optional. When set, helmdeck POSTs the final result to this URL on completion (HMAC-SHA256 signed via X-Helmdeck-Signature). Receivers can re-inject into the agent's chat as a fresh message — see docs/integrations/webhooks.md."},
			"webhook_secret": map[string]any{"type": "string", "description": "Optional. Shared secret used to sign the webhook payload. Strongly recommended whenever webhook_url is set; receivers MUST verify the signature."},
		},
		"required": []string{"pack"},
	})
	idSchema := mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"job_id": map[string]any{"type": "string"},
		},
		"required": []string{"job_id"},
	})
	return []Tool{
		{
			Name:        "pack.start",
			Description: "Start a pack call asynchronously and return a job_id immediately. Use for heavy packs (slides.narrate, research.deep, content.ground with rewrite=true) when your MCP client has a low per-request timeout. Then poll pack.status, then call pack.result.",
			InputSchema: startSchema,
		},
		{
			Name:        "pack.status",
			Description: "Check the state of an async pack call. Returns {state: running|done|failed, progress: 0-100, message}. Poll every 2-5 seconds. When state is done, call pack.result.",
			InputSchema: idSchema,
		},
		{
			Name:        "pack.result",
			Description: "Retrieve the final result of a completed async pack call. Errors if the job is still running. Job results are kept for 1 hour after completion.",
			InputSchema: idSchema,
		},
	}
}

// dispatchAsyncTool handles pack.start / pack.status / pack.result
// without going through the engine. Returns (toolResult, true) when
// the call was an async-tool call and was handled; (nil, false) when
// the caller should fall through to the regular pack path.
func (s *PackServer) dispatchAsyncTool(name string, arguments json.RawMessage) (map[string]any, bool) {
	switch name {
	case "pack.start":
		var args struct {
			Pack          string          `json:"pack"`
			Input         json.RawMessage `json:"input"`
			WebhookURL    string          `json:"webhook_url,omitempty"`
			WebhookSecret string          `json:"webhook_secret,omitempty"`
		}
		if err := json.Unmarshal(arguments, &args); err != nil {
			return errorToolResult("invalid_input", "pack.start: "+err.Error()), true
		}
		if args.Pack == "" {
			return errorToolResult("invalid_input", "pack.start: pack is required"), true
		}
		pack, err := s.registry.Get(args.Pack, "")
		if err != nil {
			return errorToolResult("unknown_pack", "pack.start: unknown pack "+args.Pack), true
		}
		input := args.Input
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}
		j := s.startAsync(pack, input, asyncOptions{
			WebhookURL:    args.WebhookURL,
			WebhookSecret: args.WebhookSecret,
		})
		body, _ := json.Marshal(j.snapshot())
		return map[string]any{
			"content": []map[string]any{{"type": "text", "text": string(body)}},
			"isError": false,
		}, true

	case "pack.status":
		j, ok := s.lookupJob(arguments)
		if !ok {
			return errorToolResult("unknown_job", "pack.status: job_id not found"), true
		}
		body, _ := json.Marshal(j.snapshot())
		return map[string]any{
			"content": []map[string]any{{"type": "text", "text": string(body)}},
			"isError": false,
		}, true

	case "pack.result":
		j, ok := s.lookupJob(arguments)
		if !ok {
			return errorToolResult("unknown_job", "pack.result: job_id not found"), true
		}
		j.mu.Lock()
		state := j.state
		res := j.result
		jobErr := j.err
		j.mu.Unlock()
		switch state {
		case "working":
			return errorToolResult("not_ready", fmt.Sprintf("pack.result: job %s still working — keep polling pack.status", j.ID)), true
		case "failed":
			return packErrorAsToolResult(jobErr), true
		case "completed":
			s.jobs.drop(j.ID)
			return s.packResultAsToolResult(context.Background(), res), true
		default:
			return errorToolResult("internal", "pack.result: unexpected job state "+state), true
		}
	}
	return nil, false
}

// lookupJob extracts the job_id (or task_id) from a tool-call
// arguments blob and returns the matching job. Both pack.status and
// pack.result use the same shape; the SEP-1686 tasks/get path also
// reuses this via lookupJobByID.
func (s *PackServer) lookupJob(arguments json.RawMessage) (*asyncJob, bool) {
	var args struct {
		JobID  string `json:"job_id"`
		TaskID string `json:"task_id"` // tolerated alias
	}
	if err := json.Unmarshal(arguments, &args); err != nil {
		return nil, false
	}
	id := args.JobID
	if id == "" {
		id = args.TaskID
	}
	if id == "" {
		return nil, false
	}
	return s.lookupJobByID(id)
}

// lookupJobByID finds a job by either its raw hex ID or its
// SEP-1686-prefixed taskID ("pack_<hex>"). Centralized so the
// SEP-1686 method handler in server.go and the legacy pack.* tools
// share one resolution policy.
func (s *PackServer) lookupJobByID(id string) (*asyncJob, bool) {
	// Strip the SEP-1686 "pack_" prefix if present so the registry
	// (keyed by raw hex) can find the entry.
	if len(id) > 5 && id[:5] == "pack_" {
		id = id[5:]
	}
	return s.jobs.get(id)
}

// errorToolResult formats a typed error as an MCP tool-result so the
// LLM sees a structured error in the content block rather than a
// transport-level JSON-RPC error. Mirrors packErrorAsToolResult's
// shape for consistency with the sync path.
func errorToolResult(code, message string) map[string]any {
	body, _ := json.Marshal(map[string]string{"error": code, "message": message})
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(body)}},
		"isError": true,
	}
}

// mustJSON marshals a Go map to json.RawMessage and panics on error.
// Only used for the static async-tool schemas at startup, so a panic
// here would surface immediately on the first MCP connection.
func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic("mcp: build async tool schema: " + err.Error())
	}
	return b
}
