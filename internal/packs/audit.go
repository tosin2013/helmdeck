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
	// Findings captures structured rule-violation entries the pack
	// surfaced (e.g. hyperframes.lint's media_missing_id finding,
	// av.validate's loudness_lufs check). Kept terse (code + severity
	// + file) so the audit row stays bounded; full message + fix_hint
	// live in the pack's sidecar artifact for operators who want the
	// detail. Capped at maxAuditFindings to defend the row size.
	// Findings-memory architecture per issue #570.
	Findings []AuditFinding `json:"findings,omitempty"`
}

// AuditFinding is the terse per-row finding shape carried in
// PackAudit. The plain-language `message` + `fix_hint` are NOT here
// — those live in the pack's full output / sidecar artifact. Memory
// rows are size-bounded; we store just enough to aggregate.
type AuditFinding struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	File     string `json:"file,omitempty"`
}

// maxAuditFindings caps how many findings a single audit row carries.
// Empirically a lint run produces ~3-20 findings; a full inspect/
// validate over a 720s composition could in theory produce hundreds.
// We cap at 50 to keep the encrypted memory row small (~10 KiB upper
// bound) and prevent a single pack run from monopolizing the audit
// budget.
const maxAuditFindings = 50

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

// extractFindings looks for structured rule-violation entries in a
// pack's output JSON. Recognized shapes (both seen empirically in the
// shipped validation suite):
//
//  1. Top-level array: `{"findings": [{"code": "...", "severity": "...", ...}]}`
//  2. Nested object: `{"lint": {"findings": [...]}}`, `{"inspect": {"issues": [...]}}`,
//     `{"validate": {"errors": [...], "warnings": [...]}}`
//
// Extraction is best-effort and silent on malformed input — the audit
// hook never fails the pack call. Findings are capped at
// maxAuditFindings; the rest are dropped. Verbose fields (message,
// fixHint, snippet, selector, rect, etc.) are NOT included — the
// full record lives in the pack's sidecar artifact for operators
// who want detail. The audit row carries the code + severity + file
// only, which is what aggregation downstream needs.
//
// Findings-memory architecture per issue #570.
func extractFindings(output json.RawMessage) []AuditFinding {
	if len(output) == 0 {
		return nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(output, &obj); err != nil {
		return nil
	}
	var out []AuditFinding
	// Shape 1: top-level "findings" array
	if raw, ok := obj["findings"]; ok {
		out = appendFindingsFromRaw(out, raw)
	}
	// Shape 2: nested {wrapper: {findings/issues/errors/warnings: [...]}}
	// Matches the lint/inspect/validate output shapes.
	for _, wrapper := range []string{"lint", "inspect", "validate"} {
		raw, ok := obj[wrapper]
		if !ok {
			continue
		}
		var inner map[string]json.RawMessage
		if err := json.Unmarshal(raw, &inner); err != nil {
			continue
		}
		// inspect uses `issues`, validate uses `errors`/`warnings`,
		// lint uses `findings` — try all four.
		for _, field := range []string{"findings", "issues", "errors", "warnings"} {
			if arrRaw, ok := inner[field]; ok {
				out = appendFindingsFromRaw(out, arrRaw)
			}
		}
	}
	if len(out) > maxAuditFindings {
		out = out[:maxAuditFindings]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func appendFindingsFromRaw(out []AuditFinding, raw json.RawMessage) []AuditFinding {
	var arr []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return out
	}
	for _, item := range arr {
		f := AuditFinding{}
		if c, ok := item["code"]; ok {
			_ = json.Unmarshal(c, &f.Code)
		}
		if s, ok := item["severity"]; ok {
			_ = json.Unmarshal(s, &f.Severity)
		}
		// validate's errors/warnings entries use `level` instead of `severity`.
		if f.Severity == "" {
			if l, ok := item["level"]; ok {
				_ = json.Unmarshal(l, &f.Severity)
			}
		}
		if file, ok := item["file"]; ok {
			_ = json.Unmarshal(file, &f.File)
		}
		// Skip entries with no useful code — keeps the audit row tight.
		if f.Code == "" {
			continue
		}
		out = append(out, f)
		if len(out) >= maxAuditFindings {
			return out
		}
	}
	return out
}

// writePackAudit records one pack-execution audit row under the
// caller's bare namespace. Failure is logged-and-ignored — never
// fails the pack call. Gated by the caller (Engine.Execute) checking
// e.memory != nil before invoking.
func (e *Engine) writePackAudit(ctx context.Context, pack *Pack, input, output json.RawMessage, outcome string, duration time.Duration) {
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
		Findings:    extractFindings(output),
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
