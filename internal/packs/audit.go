package packs

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/tosin2013/helmdeck/internal/memory"
)

// ADR 047 PR #2 — per-caller audit hooks on Engine.Execute (and
// Runner.RunSync at the pipeline layer). Every successful or
// caller-fixable pack run writes one tiny audit row to the memory
// store under the caller's bare namespace (NOT the session-scoped
// namespace ec.Memory uses for caches), so per-caller defaults can
// be projected across sessions by helmdeck://my-defaults.

// AuditTTL bounds how long audit rows live. Long enough to learn
// monthly usage patterns, short enough that SQLite stays bounded
// on heavy callers. helmdeck.memory_forget exposes manual reset
// before the TTL expires.
const AuditTTL = 30 * 24 * time.Hour

// AuditCategoryPack, AuditCategoryPipeline, and AuditCategoryPlan
// are the memory categories used by audit-write seams. The forget
// pack and the my-defaults / my-plans projections filter by category
// so they never touch cache rows (which live under "cache" or pack-
// defined categories). AuditCategoryPlan was added in ADR 049 PR #1
// for the helmdeck.plan pack's decomposition history.
const (
	AuditCategoryPack     = "pack_history"
	AuditCategoryPipeline = "pipeline_history"
	AuditCategoryPlan     = "plan_history"
)

// AuditKeyPrefix* are the key prefixes that List(ns, prefix) uses to
// scope a query to one audit category. ADR 049 added the plan_history
// prefix for helmdeck.plan's decomposition rows.
const (
	AuditKeyPrefixPack     = "pack_history/"
	AuditKeyPrefixPipeline = "pipeline_history/"
	AuditKeyPrefixPlan     = "plan_history/"
)

// learnableInputFields names the input JSON fields that get mined
// for the per-caller default projection. Everything else is dropped
// at audit-write time — markdown bodies, URLs, raw queries never
// land in audit memory.
var learnableInputFields = map[string]struct{}{
	"persona":      {},
	"audience":     {},
	"angle":        {},
	"model":        {},
	"theme":        {},
	"voice":        {},
	"persona_used": {},
	"kind":         {},
	"format":       {},
	"title":        {},
	"author":       {},
}

// PackAudit is one pack-execution audit row. Wire-format under
// memory.Entry.Value.
type PackAudit struct {
	Pack        string            `json:"pack"`
	Version     string            `json:"version,omitempty"`
	Outcome     string            `json:"outcome"`
	AtUnix      int64             `json:"at_unix"`
	DurationMs  int64             `json:"duration_ms,omitempty"`
	LearnInputs map[string]string `json:"learn_inputs,omitempty"`
}

// PipelineAudit is one pipeline-run audit row.
type PipelineAudit struct {
	Pipeline    string            `json:"pipeline"`
	RunID       string            `json:"run_id"`
	Outcome     string            `json:"outcome"`
	AtUnix      int64             `json:"at_unix"`
	DurationMs  int64             `json:"duration_ms,omitempty"`
	LearnInputs map[string]string `json:"learn_inputs,omitempty"`
}

// PlanAuditStep is one step's compact summary inside a PlanAudit row.
// We persist only the tool name + a hash of the args so the audit
// stays small but PR #2's projection can group plans by shape.
type PlanAuditStep struct {
	Order   int    `json:"order"`
	Tool    string `json:"tool"`
	ArgsSHA string `json:"args_sha,omitempty"`
}

// PlanAudit is one helmdeck.plan-execution audit row. Captures the
// intent hash (so PR #2 can group similar prompts), the complexity
// classification, and the step sequence — not the full rewritten
// prompt or rationales, which can be large and contain user data.
type PlanAudit struct {
	IntentSHA  string          `json:"intent_sha"`
	Complexity string          `json:"complexity"`
	Steps      []PlanAuditStep `json:"steps,omitempty"`
	Outcome    string          `json:"outcome"`
	AtUnix     int64           `json:"at_unix"`
	DurationMs int64           `json:"duration_ms,omitempty"`
	Model      string          `json:"model,omitempty"`
}

// WritePlanAudit records one plan-execution audit row under the
// caller's bare namespace. Mirrors WritePipelineAudit's contract:
// nil-store-safe, fail-soft, exported because the pack handler lives
// in internal/packs/builtin and calls in through the engine handle.
// Each call gets a nanosecond-suffix key for chronological List().
func (e *Engine) WritePlanAudit(ctx context.Context, audit PlanAudit) {
	if e.memory == nil {
		return
	}
	caller := callerFromContext(ctx)
	now := e.now().UTC()
	if audit.AtUnix == 0 {
		audit.AtUnix = now.Unix()
	}
	body, err := json.Marshal(audit)
	if err != nil {
		e.logger.Warn("plan audit marshal failed", "err", err)
		return
	}
	key := fmt.Sprintf("%s%s/%020d", AuditKeyPrefixPlan, audit.IntentSHA, now.UnixNano())
	if _, err := e.memory.Put(ctx, caller, key, body,
		memory.WithTTL(AuditTTL),
		memory.WithCategory(AuditCategoryPlan),
	); err != nil {
		e.logger.Warn("plan audit write failed", "err", err)
	}
}

// extractLearnableInputs scans the input JSON for low-cardinality
// string fields named in learnableInputFields. Non-string values and
// fields not in the closed set are dropped — keeps audit rows tiny
// and avoids persisting opaque blobs.
func extractLearnableInputs(input json.RawMessage) map[string]string {
	if len(input) == 0 {
		return nil
	}
	var obj map[string]any
	if err := json.Unmarshal(input, &obj); err != nil {
		return nil
	}
	out := map[string]string{}
	for k, v := range obj {
		if _, want := learnableInputFields[k]; !want {
			continue
		}
		if s, ok := v.(string); ok && s != "" {
			out[k] = s
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// writePackAudit records one pack-execution audit row under the
// caller's bare namespace. Failure is logged-and-ignored — never
// fails the pack call. Gated by the caller (Engine.Execute) checking
// e.memory != nil before invoking.
func (e *Engine) writePackAudit(ctx context.Context, pack *Pack, input json.RawMessage, outcome string, duration time.Duration) {
	if e.memory == nil || pack == nil || pack.NoAudit {
		return
	}
	caller := callerFromContext(ctx)
	now := e.now().UTC()
	audit := PackAudit{
		Pack:        pack.Name,
		Version:     pack.Version,
		Outcome:     outcome,
		AtUnix:      now.Unix(),
		DurationMs:  duration.Milliseconds(),
		LearnInputs: extractLearnableInputs(input),
	}
	body, err := json.Marshal(audit)
	if err != nil {
		e.logger.Warn("audit marshal failed", "pack", pack.Name, "err", err)
		return
	}
	// Nanosecond-suffix key keeps List(prefix) chronologically sortable
	// and collision-safe at human invocation rates.
	key := fmt.Sprintf("%s%s/%020d", AuditKeyPrefixPack, pack.Name, now.UnixNano())
	if _, err := e.memory.Put(ctx, caller, key, body,
		memory.WithTTL(AuditTTL),
		memory.WithCategory(AuditCategoryPack),
	); err != nil {
		e.logger.Warn("audit write failed", "pack", pack.Name, "err", err)
	}
}

// WritePipelineAudit records one pipeline-run audit row. Exported
// because Runner.RunSync lives in the pipelines package and calls
// this through the same engine handle. Same nil-store + nil-pipeline
// guards as the pack hook.
func (e *Engine) WritePipelineAudit(ctx context.Context, pipelineID, runID string, inputs json.RawMessage, outcome string, duration time.Duration) {
	if e.memory == nil || pipelineID == "" {
		return
	}
	caller := callerFromContext(ctx)
	now := e.now().UTC()
	audit := PipelineAudit{
		Pipeline:    pipelineID,
		RunID:       runID,
		Outcome:     outcome,
		AtUnix:      now.Unix(),
		DurationMs:  duration.Milliseconds(),
		LearnInputs: extractLearnableInputs(inputs),
	}
	body, err := json.Marshal(audit)
	if err != nil {
		e.logger.Warn("pipeline audit marshal failed", "pipeline", pipelineID, "err", err)
		return
	}
	key := fmt.Sprintf("%s%s/%020d", AuditKeyPrefixPipeline, pipelineID, now.UnixNano())
	if _, err := e.memory.Put(ctx, caller, key, body,
		memory.WithTTL(AuditTTL),
		memory.WithCategory(AuditCategoryPipeline),
	); err != nil {
		e.logger.Warn("pipeline audit write failed", "pipeline", pipelineID, "err", err)
	}
}

// MemoryStore exposes the engine's wired memory store to callers in
// the pipelines package (Runner.RunSync calls WritePipelineAudit on
// the engine). Returns nil when no store is configured, matching the
// gating contract every audit hook uses.
func (e *Engine) MemoryStore() memory.MemoryStore { return e.memory }
