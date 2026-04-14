package mcp

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/telemetry"
)

// PackServer is the helmdeck-as-MCP-server implementation. It
// exposes every Capability Pack registered in packs.Registry as a
// typed MCP tool: tools/list enumerates the live registry on every
// request (so hot-loaded packs from T207 show up immediately) and
// tools/call dispatches through packs.Engine — meaning the same
// validation, session lifecycle, artifact upload, and closed-set
// error mapping that REST callers get also covers MCP clients.
//
// PackServer is intentionally transport-agnostic: Serve takes an
// io.Reader and io.Writer and speaks line-delimited JSON-RPC 2.0,
// the same framing the StdioAdapter consumes from external servers.
// The api package wraps it in a WebSocket hijacker (T302), and
// future Phase 3 work could just as easily wrap it in an
// HTTP/SSE handler or pipe it to a Unix socket.
// DefaultInlineImageThreshold is the maximum artifact size (in bytes)
// that gets inlined as base64 image content in MCP tool results.
// Artifacts over this threshold are URL-only. Base64 inflates size
// by ~33%, so 1 MB raw → ~1.33 MB in the JSON-RPC response.
const DefaultInlineImageThreshold = 1 << 20 // 1 MiB

type PackServer struct {
	registry             *packs.Registry
	engine               *packs.Engine
	artifacts            packs.ArtifactStore
	inlineImageThreshold int64
	// jobs tracks async pack runs (pack.start / pack.status /
	// pack.result). One registry per PackServer instance — see
	// jobs.go for why this exists (workaround for MCP TS-SDK clients
	// like OpenClaw whose 60s JSON-RPC timeout doesn't reset on
	// progress notifications).
	jobs *jobRegistry
}

// PackServerOption configures a PackServer at construction time.
type PackServerOption func(*PackServer)

// WithArtifacts sets the artifact store used to fetch image bytes
// for inline MCP image content (T302b, ADR 032). When nil, image
// content blocks are not emitted — only text URLs.
func WithArtifacts(store packs.ArtifactStore) PackServerOption {
	return func(s *PackServer) { s.artifacts = store }
}

// WithInlineImageThreshold overrides the default 1 MB threshold.
// Set to 0 to disable inline images entirely.
func WithInlineImageThreshold(n int64) PackServerOption {
	return func(s *PackServer) { s.inlineImageThreshold = n }
}

// NewPackServer constructs a server bound to a pack registry and
// the engine that executes them. Both are required.
func NewPackServer(reg *packs.Registry, eng *packs.Engine, opts ...PackServerOption) *PackServer {
	s := &PackServer{
		registry:             reg,
		engine:               eng,
		inlineImageThreshold: DefaultInlineImageThreshold,
		jobs:                 newJobRegistry(),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Serve drives one MCP session. It returns when the reader hits
// EOF, the context is cancelled, or a write to w fails. Errors
// inside individual JSON-RPC calls are surfaced via the response's
// Error field, NOT as Go errors — the only Go errors Serve returns
// are transport-level (broken pipe, scanner overflow).
func (s *PackServer) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	// Writes are serialized so concurrent notifications and
	// responses don't interleave on the wire. Reads are sequential
	// (one request at a time) so a sync.Mutex is enough.
	var writeMu sync.Mutex
	writeFrame := func(v any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		buf, err := json.Marshal(v)
		if err != nil {
			return err
		}
		buf = append(buf, '\n')
		_, err = w.Write(buf)
		return err
	}

	for sc.Scan() {
		// Honor context cancellation between requests so a long-lived
		// MCP session can be torn down by closing the context.
		if err := ctx.Err(); err != nil {
			return err
		}

		var req rpcRequest
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			// Malformed JSON gets the standard parse error code.
			// We can't echo the request id because we never parsed
			// it; per JSON-RPC 2.0 the id is null in this case.
			_ = writeFrame(rpcResponse{
				JSONRPC: "2.0",
				ID:      json.RawMessage("null"),
				Error:   &rpcError{Code: -32700, Message: "parse error"},
			})
			continue
		}

		// Notifications (no id) are fire-and-forget per spec; we
		// silently consume the ones we recognise.
		if len(req.ID) == 0 || string(req.ID) == "null" {
			continue
		}

		resp := s.dispatch(ctx, req, writeFrame)
		if err := writeFrame(resp); err != nil {
			return err
		}
	}
	return sc.Err()
}

// dispatch handles one JSON-RPC call. writeFrame is supplied so the
// handler can emit out-of-band notifications/progress messages
// during a long-running tools/call without competing with the main
// response on the wire (writeFrame's mutex serializes everything).
func (s *PackServer) dispatch(ctx context.Context, req rpcRequest, writeFrame func(any) error) rpcResponse {
	mk := func(result any, err *rpcError) rpcResponse {
		out := rpcResponse{JSONRPC: "2.0", ID: req.ID}
		if err != nil {
			out.Error = err
			return out
		}
		raw, mErr := json.Marshal(result)
		if mErr != nil {
			out.Error = &rpcError{Code: -32603, Message: "marshal: " + mErr.Error()}
			return out
		}
		out.Result = raw
		return out
	}

	switch req.Method {
	case "initialize":
		// We accept any protocol version the client sends and echo
		// back the one we implement. Strict version negotiation is
		// T304's job (skew warnings); for now compatibility is best-
		// effort and the bridge handles client capability gaps.
		return mk(map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
			"serverInfo": map[string]any{
				"name":    "helmdeck",
				"version": "0.2.0",
			},
		}, nil)

	case "tools/list":
		infos := s.registry.List()
		tools := make([]Tool, 0, len(infos)+3)
		for _, info := range infos {
			pack, err := s.registry.Get(info.Name, "")
			if err != nil {
				continue
			}
			schema, _ := schemaToJSON(pack.InputSchema)
			tools = append(tools, Tool{
				Name:        pack.Name,
				Description: pack.Description,
				InputSchema: schema,
			})
		}
		// Append the async wrapper tools (pack.start/status/result).
		// Surfacing them in tools/list lets MCP clients discover them
		// the same way as regular packs — SKILLS.md tells the LLM
		// when to prefer the async path.
		tools = append(tools, asyncPackTools()...)
		return mk(map[string]any{"tools": tools}, nil)

	case "tools/call":
		// _meta.progressToken is an MCP-spec opt-in: when the client
		// sends one, we emit notifications/progress frames during
		// the call so the client's per-request JSON-RPC timer (the
		// thing behind the dreaded -32001) can be reset by SDKs that
		// honor progress. Keep the field json.RawMessage because the
		// spec allows string OR integer tokens — we echo it back
		// verbatim either way.
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
			Meta      struct {
				ProgressToken json.RawMessage `json:"progressToken,omitempty"`
			} `json:"_meta,omitempty"`
		}
		if len(req.Params) == 0 {
			return mk(nil, &rpcError{Code: -32602, Message: "missing params"})
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return mk(nil, &rpcError{Code: -32602, Message: "invalid params: " + err.Error()})
		}
		if params.Name == "" {
			return mk(nil, &rpcError{Code: -32602, Message: "tool name required"})
		}
		// Async wrapper tools (pack.start/status/result) intercept
		// before the registry lookup — they aren't packs themselves.
		// See jobs.go for the rationale.
		if asyncResult, handled := s.dispatchAsyncTool(params.Name, params.Arguments); handled {
			return mk(asyncResult, nil)
		}
		pack, err := s.registry.Get(params.Name, "")
		if err != nil {
			return mk(nil, &rpcError{Code: -32601, Message: "unknown tool: " + params.Name})
		}
		input := params.Arguments
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}
		// T510: every MCP tool call gets its own span. The pack
		// engine adds its own child span inside Execute, so the
		// resulting trace shows "mcp.tools/call" → "pack.<name>"
		// hierarchy in the operator's tracing UI.
		tracer := otel.Tracer("helmdeck/mcp")
		ctx, span := tracer.Start(ctx, "mcp.tools/call",
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				telemetry.Helmdeck.MCPServer.String("helmdeck-builtin"),
				telemetry.Helmdeck.MCPTool.String(params.Name),
			),
		)
		// Wire progress when the client opted in. The callback runs
		// on whatever goroutine the pack chose for its progress emit,
		// but writeFrame holds a mutex so concurrent notifications
		// can't interleave with the eventual response frame.
		if len(params.Meta.ProgressToken) > 0 && string(params.Meta.ProgressToken) != "null" {
			tok := params.Meta.ProgressToken
			ctx = packs.WithProgress(ctx, func(pct float64, message string) {
				_ = writeFrame(progressNotification(tok, pct, message))
			})
		}
		// SEP-1686 task envelope path: packs that declare Async=true
		// route through the job registry so the JSON-RPC response
		// returns in milliseconds. The client either polls tasks/get
		// (SEP-1686-aware SDKs do this automatically) or — when the
		// caller embedded webhook_url + webhook_secret in arguments —
		// receives the result via outbound HTTP POST when the job
		// terminates. Webhook params are stripped before passing the
		// remaining input to the engine since the underlying pack
		// handler shouldn't see them.
		if pack.Async {
			webhookURL, webhookSecret, cleanInput := extractWebhookFields(input)
			j := s.startAsync(pack, cleanInput, asyncOptions{
				WebhookURL:    webhookURL,
				WebhookSecret: webhookSecret,
			})
			// Notify subscribed clients that a task was created.
			// Clients that don't speak SEP-1686 ignore the
			// notification harmlessly; clients that do can begin
			// polling tasks/get without waiting for the response.
			_ = writeFrame(taskCreatedNotification(j.taskID()))
			span.SetStatus(codes.Ok, "")
			span.End()
			return mk(j.taskEnvelope(), nil)
		}
		res, err := s.engine.Execute(ctx, pack, input)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			span.End()
			return mk(packErrorAsToolResult(err), nil)
		}
		span.SetStatus(codes.Ok, "")
		span.End()
		return mk(s.packResultAsToolResult(ctx, res), nil)

	case "tasks/get":
		// SEP-1686 (Final, 2025-10-20). Clients call this to poll a
		// task started via the Async tools/call path. We accept both
		// the SEP-1686 prefixed taskId form ("pack_<hex>") and the
		// raw hex job ID for symmetry with pack.status. Returns the
		// canonical SEP-1686 shape including pollFrequency so the
		// client knows how often to come back.
		var params struct {
			TaskID string `json:"taskId"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil || params.TaskID == "" {
			return mk(nil, &rpcError{Code: -32602, Message: "tasks/get: taskId required"})
		}
		j, ok := s.lookupJobByID(params.TaskID)
		if !ok {
			return mk(nil, &rpcError{Code: -32602, Message: "tasks/get: unknown taskId " + params.TaskID})
		}
		return mk(s.taskGetResult(ctx, j), nil)

	default:
		return mk(nil, &rpcError{Code: -32601, Message: "method not found: " + req.Method})
	}
}

// extractWebhookFields pulls webhook_url + webhook_secret out of a
// pack input blob and returns them alongside the input with those
// fields removed. The underlying pack handler must NEVER see the
// webhook secret — it's MCP-server-level metadata that bypasses
// the pack's own schema. When the input isn't valid JSON or has no
// webhook fields, the original bytes are returned unmodified.
func extractWebhookFields(input json.RawMessage) (url, secret string, cleaned json.RawMessage) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(input, &m); err != nil || len(m) == 0 {
		return "", "", input
	}
	if raw, ok := m["webhook_url"]; ok {
		_ = json.Unmarshal(raw, &url)
		delete(m, "webhook_url")
	}
	if raw, ok := m["webhook_secret"]; ok {
		_ = json.Unmarshal(raw, &secret)
		delete(m, "webhook_secret")
	}
	if url == "" {
		return "", "", input
	}
	out, err := json.Marshal(m)
	if err != nil {
		return url, secret, input
	}
	return url, secret, out
}

// taskCreatedNotification builds the SEP-1686 notifications/tasks/created
// frame. Clients that subscribe to the notification can begin
// polling tasks/get immediately; clients that don't subscribe
// ignore it harmlessly. Like progressNotification, this is a
// JSON-RPC notification (no id field) — never a request.
func taskCreatedNotification(taskID string) any {
	return map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/tasks/created",
		"params": map[string]any{
			"_meta": map[string]any{
				"modelcontextprotocol.io/related-task": map[string]any{
					"taskId": taskID,
				},
			},
		},
	}
}

// progressNotification builds an MCP `notifications/progress` JSON-RPC
// frame. Per the spec (2025-06-18), the frame has no `id` (it's a
// notification, not a request), echoes the client-supplied
// progressToken, and carries `progress` plus an optional `message`.
// We omit `total` so clients render whatever progress the pack
// reports as a percent — the spec accepts that shape.
//
// Whether this resets the client's per-request timer is up to the
// client SDK: the Python SDK does so by default; the TS SDK requires
// `resetTimeoutOnProgress: true` opt-in. Either way, emitting these
// is the spec-compliant way to keep long packs alive on the wire.
func progressNotification(token json.RawMessage, pct float64, message string) any {
	params := map[string]any{
		"progressToken": token,
		"progress":      pct,
	}
	if message != "" {
		params["message"] = message
	}
	return map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/progress",
		"params":  params,
	}
}

// packResultAsToolResult converts a packs.Result into the MCP
// `CallToolResult` shape: a typed content array plus an optional
// structured `isError` flag.
//
// T302b (ADR 032): when the pack produced image artifacts and an
// ArtifactStore is configured, artifacts under the inline threshold
// are included as `type: "image"` content blocks with base64-
// encoded bytes. Vision-capable LLMs (GPT-4o, Claude, Gemini) can
// then reason about screenshots in one round trip — no second tool
// call to download and display the image. Artifacts over the
// threshold stay URL-only in the text block.
func (s *PackServer) packResultAsToolResult(ctx context.Context, res *packs.Result) map[string]any {
	body, _ := json.Marshal(res)
	content := []map[string]any{
		{"type": "text", "text": string(body)},
	}
	// Inline image artifacts when the store is available and the
	// artifact is small enough. The threshold check uses the raw
	// size (pre-base64) since that's what the operator configured.
	if s.artifacts != nil && s.inlineImageThreshold > 0 {
		for _, art := range res.Artifacts {
			if !isInlineableImage(art.ContentType) {
				continue
			}
			if art.Size > s.inlineImageThreshold {
				continue
			}
			data, _, err := s.artifacts.Get(ctx, art.Key)
			if err != nil {
				continue // degrade gracefully — URL is still in the text block
			}
			content = append(content, map[string]any{
				"type":     "image",
				"data":     base64Encode(data),
				"mimeType": art.ContentType,
			})
		}
	}
	return map[string]any{
		"content": content,
		"isError": false,
	}
}

// isInlineableImage returns true for MIME types the MCP image
// content type supports.
func isInlineableImage(ct string) bool {
	switch ct {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	}
	return false
}

// base64Encode wraps the standard library so the import is in one
// place and the function name is self-documenting at call sites.
func base64Encode(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}

func packErrorAsToolResult(err error) map[string]any {
	var perr *packs.PackError
	code := "internal"
	msg := err.Error()
	if errors.As(err, &perr) {
		code = string(perr.Code)
		msg = perr.Message
	}
	body, _ := json.Marshal(map[string]string{"error": code, "message": msg})
	return map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": string(body)},
		},
		"isError": true,
	}
}

// schemaToJSON converts a packs.Schema implementation into a
// JSON Schema document. Today we recognise BasicSchema and emit
// the canonical `{type:"object", required, properties}` shape; any
// other Schema type is exported as `{type:"object"}` because pack
// authors that bring a real JSON Schema library can serialise it
// themselves at registration time (a future task can extend this
// switch).
func schemaToJSON(s packs.Schema) (json.RawMessage, error) {
	if s == nil {
		return json.Marshal(map[string]any{"type": "object"})
	}
	if bs, ok := s.(packs.BasicSchema); ok {
		props := map[string]any{}
		for k, kind := range bs.Properties {
			props[k] = map[string]any{"type": kind}
		}
		out := map[string]any{
			"type":       "object",
			"properties": props,
		}
		if len(bs.Required) > 0 {
			out["required"] = bs.Required
		}
		return json.Marshal(out)
	}
	return json.Marshal(map[string]any{"type": "object"})
}

// methodNotFoundError is exported for tests that want to assert on
// the wire shape of an unknown-method response without re-parsing
// the rpcError struct.
var methodNotFoundError = fmt.Errorf("method not found")
