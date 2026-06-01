// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// plan.go — helmdeck.plan meta-pack (ADR 049 PR #1).
//
// Where helmdeck.route picks the single best tool for an intent,
// helmdeck.plan decomposes a multi-intent prompt into an ordered
// sequence of tool/pipeline calls. Returns:
//
//   - steps[]:           ordered {order, tool, args, rationale}
//   - rewritten_prompt:  the same plan as a natural-language step list
//                        an agent can execute line-by-line
//   - complexity:        single-action | pipeline-direct | pack-chain
//   - reasoning:         why this decomposition
//
// Pipeline-aware: the catalog projection from helmdeck.route is reused
// so the model sees both packs AND pipelines. The system prompt teaches
// three rules — pipeline wins when one fits, honor supersedes, decompose
// only when no pipeline matches. Re-implementing a pipeline's chain as
// pack-by-pack steps would regress the curated-sequence guarantee
// pipelines exist to provide.
//
// Self-learning seam: every successful plan writes a compact PlanAudit
// row under category plan_history (intent SHA + complexity + step
// tools, NOT the full prompt or rationales). ADR 049 PR #2 will mine
// these rows into a helmdeck://my-plans projection.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tosin2013/helmdeck/internal/gateway"
	"github.com/tosin2013/helmdeck/internal/llmcontext"
	"github.com/tosin2013/helmdeck/internal/memory"
	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/vision"
)

// planDefaultMaxTokens caps the model's plan output. Wide enough for
// 5-7 steps with rationales + a rewritten_prompt + reasoning; tight
// enough to keep cost predictable across free models.
const planDefaultMaxTokens = 3000

// planPackID is the canonical id callers use to invoke the pack. We
// reject recursive plan-of-plan calls by name (the hallucination guard
// can't catch this because the id IS valid in the catalog).
const planPackID = "helmdeck.plan"

// planSystemPrompt teaches the model the pipeline-aware decomposition
// rules and the exact output shape. Versioned alongside the ADR — PR #2
// will append the my-plans prior block when it lands.
const planSystemPrompt = `You are the planning agent for helmdeck, a library of capability "packs" (atomic tool actions) and "pipelines" (curated chains of packs exposed as one call).

Your job: given a user's natural-language intent — possibly spanning multiple actions — return an ORDERED sequence of tool/pipeline calls that realizes the intent, plus a rewritten natural-language prompt explicit enough that a small model can execute each step without further reasoning.

You will receive two blocks of context as part of the user message:
  1. CATALOG (routing-guide JSON): every pack and pipeline plus its metadata (accepts/produces/intent_keywords/typical_use/limitations, plus supersedes on pipelines).
  2. CALLER DEFAULTS (my-defaults JSON): the caller's most-used packs/pipelines and their most-common input values.

Pipeline-aware rules:
  P1. PIPELINE WINS when one fits. If a pipeline's metadata.accepts matches the source kind AND metadata.produces matches the target format, emit ONE step calling "helmdeck__pipeline-run" with args {"id": "<pipeline id>", "inputs": {...}}. DO NOT re-decompose what the pipeline already does internally.
  P2. HONOR supersedes. A pipeline whose metadata.supersedes lists packs the user mentioned by name wins automatically — the pipeline is the maintained one-call surface for that chain.
  P3. DECOMPOSE only when no pipeline fits. Fall back to a pack-by-pack sequence when no pipeline matches by accepts/produces/intent_keywords.

Step rules:
  S1. NEVER hallucinate a tool id. Every step.tool MUST be either a pack name verbatim from CATALOG.packs[].name OR the literal string "helmdeck__pipeline-run" (with args.id matching a CATALOG.pipelines[].id verbatim).
  S2. NEVER emit "helmdeck.plan" or "helmdeck__plan" as a step.tool — the planner cannot call itself.
  S3. Pre-fill step.args from CALLER DEFAULTS common_inputs when a relevant value exists for the chosen tool; leave args empty when nothing is learned.
  S4. Each step.rationale is one short sentence explaining WHY this step at THIS position. The rationales become the rewritten_prompt body.
  S5. Steps are 1-indexed and strictly ordered.

Complexity classification (set complexity to exactly ONE of):
  - "single-action" — the intent is one tool call. Emit one step. (Often you should delegate to helmdeck.route instead, but a single step is acceptable.)
  - "pipeline-direct" — one step calling a pipeline that covers the whole intent.
  - "pack-chain" — two or more steps. May include a pipeline-run as one of the steps; the chain is what makes it pack-chain.

Output FORMAT — return exactly ONE JSON object, no prose around it, no code fences:

{
  "steps": [
    {"order": 1, "tool": "<exact id>", "args": { ... }, "rationale": "<one sentence>"}
  ],
  "complexity": "single-action" | "pipeline-direct" | "pack-chain",
  "reasoning": "<1-3 sentences explaining the decomposition, citing pipeline supersedes when used>"
}

The handler will derive rewritten_prompt from your steps post-hoc — do NOT emit a rewritten_prompt field yourself. Keep rationales concrete (tool name + what it produces + how step N+1 consumes it).`

// planInput is the schema agents call helmdeck.plan with.
type planInput struct {
	UserIntent string          `json:"user_intent"`
	Context    json.RawMessage `json:"context,omitempty"`
	Model      string          `json:"model"`
	MaxTokens  int             `json:"max_tokens,omitempty"`
}

// planStep is one step in the returned plan. Args is preserved raw so
// the agent can forward it to the recommended tool without round-trip
// re-marshaling.
type planStep struct {
	Order     int             `json:"order"`
	Tool      string          `json:"tool"`
	Args      json.RawMessage `json:"args,omitempty"`
	Rationale string          `json:"rationale,omitempty"`
}

// planOutput is the wire shape helmdeck.plan returns. RewrittenPrompt
// is derived from Steps in the handler — the model does not emit it
// directly, which keeps the two outputs from drifting.
//
// Compaction is the optional Trim record from llmcontext.CompactCatalog
// (ADR 050 PR #2): set when the model's budget required trimming the
// catalog projection, omitted when the catalog passed through (Tier A
// or small catalogs that fit Tier C's budget without work). Agents
// inspecting `compaction` can detect when a plan was made under a slim
// catalog and decide whether to escalate to a stronger model.
type planOutput struct {
	Steps           []planStep      `json:"steps"`
	RewrittenPrompt string          `json:"rewritten_prompt"`
	Complexity      string          `json:"complexity"`
	Reasoning       string          `json:"reasoning,omitempty"`
	Model           string          `json:"model"`
	Compaction      *planCompaction `json:"compaction,omitempty"`
}

// planCompaction is the wire shape of llmcontext.Trim when surfaced
// to the agent. Mirrors the Trim fields so agents reading the output
// see the same numbers operators see in the INFO log line.
type planCompaction struct {
	BeforeBytes int      `json:"before_bytes"`
	AfterBytes  int      `json:"after_bytes"`
	Dropped     []string `json:"dropped,omitempty"`
}

// Plan returns the helmdeck.plan pack. Constructor signature mirrors
// helmdeck.route so main.go wires the two side-by-side. The audit
// seam writes plan_history rows via the engine's WritePlanAudit; the
// engine handle is reached through ec.Memory's underlying store at
// handler time — but we go through the engine, not the adapter, to
// keep the audit write on the bare-caller namespace (audit rows are
// cross-session signals).
func Plan(d vision.Dispatcher, reg *packs.Registry, pipes PipelinesLister) *packs.Pack {
	return &packs.Pack{
		Name:        planPackID,
		Version:     "v1",
		Description: "Decompose a multi-intent user prompt into an ordered sequence of helmdeck tool/pipeline calls. Returns a `steps` array (each {tool, args, rationale}), a `rewritten_prompt` string the calling agent can execute line-by-line, and a `complexity` classifier (single-action / pipeline-direct / pack-chain). Pipeline-aware: prefers a curated pipeline over re-decomposing its constituent packs. Call this FIRST when the user's request spans multiple actions.",
		Metadata: packs.PackMetadata{
			Accepts:        []string{"user_intent"},
			Produces:       []string{"plan_steps", "rewritten_prompt"},
			IntentKeywords: []string{"plan this", "break this down", "decompose", "what steps", "how do i do all of this", "multi-step"},
			TypicalUse:     "Pre-execution call for multi-intent prompts where one pack/pipeline isn't enough. Agents can either iterate steps[] structurally or feed rewritten_prompt back into a small model as a clearer instruction.",
			Limitations:    []string{"LLM-backed: needs a wired dispatcher and a model id in the input", "RETURNS a plan; does not execute it — the agent runs the steps", "cannot call itself (recursive helmdeck.plan steps are rejected)", "plan quality is bounded by the model: free models may benefit from rewritten_prompt while frontier models can consume steps[] directly"},
		},
		InputSchema: packs.BasicSchema{
			Required: []string{"user_intent", "model"},
			Properties: map[string]string{
				"user_intent": "string",
				"context":     "object",
				"model":       "string",
				"max_tokens":  "number",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"steps", "rewritten_prompt", "complexity", "model"},
			Properties: map[string]string{
				"steps":            "array",
				"rewritten_prompt": "string",
				"complexity":       "string",
				"reasoning":        "string",
				"model":            "string",
				"compaction":       "object",
			},
		},
		Handler: planHandler(d, reg, pipes),
	}
}

func planHandler(d vision.Dispatcher, reg *packs.Registry, pipes PipelinesLister) packs.HandlerFunc {
	return func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
		if d == nil {
			return nil, &packs.PackError{Code: packs.CodeInternal,
				Message: "helmdeck.plan registered without a gateway dispatcher"}
		}
		if reg == nil {
			return nil, &packs.PackError{Code: packs.CodeInternal,
				Message: "helmdeck.plan registered without a pack registry"}
		}
		var in planInput
		if err := json.Unmarshal(ec.Input, &in); err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
		}
		intent := strings.TrimSpace(in.UserIntent)
		if intent == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "user_intent is required"}
		}
		if strings.TrimSpace(in.Model) == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "model is required (provider/model id; see helmdeck://models)"}
		}
		maxTokens := in.MaxTokens
		if maxTokens <= 0 {
			maxTokens = planDefaultMaxTokens
		}

		// Reuse the catalog + defaults projections route.go already
		// shapes. validIDs covers BOTH pack names and pipeline ids —
		// the hallucination guard below also accepts the literal
		// "helmdeck__pipeline-run" as a virtual tool (args.id is what
		// must resolve to a catalog id).
		catalog, validIDs := buildCatalog(ctx, reg, pipes)
		defaults := packs.Defaults{Packs: []packs.ProjectedPack{}, Pipelines: []packs.ProjectedPipeline{}}
		if ec.Memory != nil {
			caller := packs.CallerFromContext(ctx)
			if def, derr := defaultsFromAdapter(ctx, ec, caller); derr == nil {
				defaults = def
			} else {
				ec.Logger.Warn("helmdeck.plan: my-defaults projection failed; planning without learned defaults", "err", derr)
			}
		}

		// ADR 050: compact the catalog projection to fit the model's
		// budget before assembling the prompt. Tier A frontier models
		// pass through unchanged (MaxCatalogBytes=0). Tier B/C trim
		// metadata in a deterministic priority order until the
		// marshaled catalog fits — supersedes is preserved so rule
		// P2 stays anchored even on aggressive compaction.
		budget := llmcontext.BudgetFor(in.Model)
		compactedRG, trim := llmcontext.CompactCatalog(routingGuideFromCatalog(catalog), budget)
		catalog = catalogFromRoutingGuide(compactedRG)
		if len(trim.Dropped) > 0 {
			ec.Logger.Info("helmdeck.plan: catalog compacted to fit model budget",
				"model", in.Model,
				"tier", string(budget.Tier),
				"before_bytes", trim.BeforeBytes,
				"after_bytes", trim.AfterBytes,
				"dropped", trim.Dropped,
			)
		}

		ec.Report(20, "calling model for plan decomposition")

		started := time.Now()
		user := buildPlanUserMessage(intent, in.Context, catalog, defaults)
		mt := maxTokens
		chat, err := d.Dispatch(ctx, gateway.ChatRequest{
			Model:     in.Model,
			MaxTokens: &mt,
			Messages: []gateway.Message{
				{Role: "system", Content: gateway.TextContent(planSystemPrompt)},
				{Role: "user", Content: gateway.TextContent(user)},
			},
		})
		if err != nil {
			return nil, dispatchError("helmdeck.plan gateway", err)
		}
		if len(chat.Choices) == 0 {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed, Message: "gateway returned no choices"}
		}
		body := unwrapCodeFence(strings.TrimSpace(chat.Choices[0].Message.Content.Text()))
		if body == "" {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed, Message: "gateway returned an empty plan response"}
		}

		// Decode the LLM's structural plan, then enforce the guards
		// before we trust the steps for the audit + rewritten_prompt.
		var raw struct {
			Steps      []planStep `json:"steps"`
			Complexity string     `json:"complexity"`
			Reasoning  string     `json:"reasoning"`
		}
		if err := json.Unmarshal([]byte(body), &raw); err != nil {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: "model output is not valid JSON: " + err.Error(), Cause: err}
		}

		steps := normalizePlanSteps(raw.Steps, validIDs)
		complexity := normalizeComplexity(raw.Complexity, steps)

		out := planOutput{
			Steps:           steps,
			RewrittenPrompt: renderRewrittenPrompt(intent, steps),
			Complexity:      complexity,
			Reasoning:       strings.TrimSpace(raw.Reasoning),
			Model:           in.Model,
		}
		// Surface the Trim record to the agent ONLY when compaction
		// actually fired (trim.Dropped non-empty). Tier A models pass
		// through with empty Dropped — omitting the field there keeps
		// the wire shape clean for the unaffected path. ADR 050 PR #2.
		if len(trim.Dropped) > 0 {
			out.Compaction = &planCompaction{
				BeforeBytes: trim.BeforeBytes,
				AfterBytes:  trim.AfterBytes,
				Dropped:     trim.Dropped,
			}
		}

		// Audit-write seam. We write through ec.Memory because
		// helmdeck.plan has NeedsSession=false (default), so the
		// adapter's namespace IS the bare caller — exactly where
		// PR #2's my-plans projection will read from. Failure is
		// logged-and-ignored: an audit miss must never fail a
		// successful plan call.
		if ec.Memory != nil {
			writePlanAuditViaMemory(ec, in.Model, intent, complexity, steps, time.Since(started))
		}

		ec.Report(100, "plan ready")
		return json.Marshal(out)
	}
}

// buildPlanUserMessage assembles the catalog + defaults + intent prompt
// body. Same shape route.go uses — keeping the two prompts visually
// parallel makes it easier for operators reading agent traces to
// understand which pack produced which signal.
func buildPlanUserMessage(intent string, contextJSON json.RawMessage, catalog catalogProjection, defaults packs.Defaults) string {
	catBytes, _ := json.MarshalIndent(catalog, "", "  ")
	defBytes, _ := json.MarshalIndent(defaults, "", "  ")
	var b strings.Builder
	b.WriteString("CATALOG (helmdeck routing-guide):\n")
	b.Write(catBytes)
	b.WriteString("\n\nCALLER DEFAULTS (helmdeck://my-defaults projection):\n")
	b.Write(defBytes)
	b.WriteString("\n\nUSER REQUEST:\n")
	b.WriteString(intent)
	if len(contextJSON) > 0 && string(contextJSON) != "null" {
		b.WriteString("\n\nOPTIONAL CONTEXT:\n")
		b.Write(contextJSON)
	}
	b.WriteString("\n\nReturn the JSON object now.")
	return b.String()
}

// normalizePlanSteps enforces ordering + the hallucination + recursion
// guards. Unknown tool ids are demoted to {"tool": "unknown"} with a
// populated rationale explaining the gap — same shape route.go's
// demoteToGap uses for unknown ids, but per-step (a plan may have
// some known steps and some gaps).
func normalizePlanSteps(in []planStep, validIDs map[string]bool) []planStep {
	out := make([]planStep, 0, len(in))
	for i, s := range in {
		tool := strings.TrimSpace(s.Tool)
		rationale := strings.TrimSpace(s.Rationale)
		switch {
		case tool == "":
			// Empty tool — demote to gap.
			out = append(out, planStep{
				Order:     i + 1,
				Tool:      "unknown",
				Args:      s.Args,
				Rationale: combineGapRationale(rationale, "step had no tool id"),
			})
		case tool == planPackID || tool == "helmdeck__plan":
			// Recursion guard. Plans MUST NOT call helmdeck.plan.
			out = append(out, planStep{
				Order:     i + 1,
				Tool:      "unknown",
				Args:      s.Args,
				Rationale: combineGapRationale(rationale, "helmdeck.plan cannot call itself; rejected"),
			})
		case tool == "helmdeck__pipeline-run":
			// Virtual tool — args.id MUST resolve to a real pipeline.
			if !pipelineRunArgsResolve(s.Args, validIDs) {
				out = append(out, planStep{
					Order:     i + 1,
					Tool:      "unknown",
					Args:      s.Args,
					Rationale: combineGapRationale(rationale, "helmdeck__pipeline-run args.id missing or not in catalog"),
				})
			} else {
				out = append(out, planStep{
					Order:     i + 1,
					Tool:      tool,
					Args:      s.Args,
					Rationale: rationale,
				})
			}
		case validIDs[tool]:
			out = append(out, planStep{
				Order:     i + 1,
				Tool:      tool,
				Args:      s.Args,
				Rationale: rationale,
			})
		default:
			out = append(out, planStep{
				Order:     i + 1,
				Tool:      "unknown",
				Args:      s.Args,
				Rationale: combineGapRationale(rationale, fmt.Sprintf("tool %q not in catalog", tool)),
			})
		}
	}
	return out
}

// pipelineRunArgsResolve checks that a helmdeck__pipeline-run step's
// args carry an id field that names a real pipeline. We don't require
// inputs to be present — pipelines may have all-optional inputs.
func pipelineRunArgsResolve(args json.RawMessage, validIDs map[string]bool) bool {
	if len(args) == 0 {
		return false
	}
	var probe struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &probe); err != nil {
		return false
	}
	if probe.ID == "" {
		return false
	}
	return validIDs[probe.ID]
}

// combineGapRationale prepends the gap reason to the model's original
// rationale (if any) so the agent sees BOTH why the step was demoted
// AND what the model thought it was doing.
func combineGapRationale(modelRationale, gap string) string {
	if modelRationale == "" {
		return "gap: " + gap
	}
	return "gap: " + gap + " — model said: " + modelRationale
}

// normalizeComplexity defaults the classifier from the step shape when
// the model omitted or invented a value. The handler is the source of
// truth — the LLM's complexity field is treated as advisory.
func normalizeComplexity(modelSays string, steps []planStep) string {
	switch strings.TrimSpace(strings.ToLower(modelSays)) {
	case "single-action", "pipeline-direct", "pack-chain":
		// Trust the model only when the shape supports it.
	default:
		// Fall through to derivation.
	}
	if len(steps) == 0 {
		return "single-action"
	}
	if len(steps) == 1 {
		if steps[0].Tool == "helmdeck__pipeline-run" {
			return "pipeline-direct"
		}
		return "single-action"
	}
	return "pack-chain"
}

// renderRewrittenPrompt derives the natural-language step list from
// the validated steps. Deriving it here (not asking the LLM to produce
// it independently) keeps rewritten_prompt and steps from drifting
// — the two outputs encode the same plan in two surfaces.
func renderRewrittenPrompt(intent string, steps []planStep) string {
	if len(steps) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Plan for: ")
	b.WriteString(strings.TrimSpace(intent))
	b.WriteString("\n")
	for _, s := range steps {
		fmt.Fprintf(&b, "Step %d: call %s", s.Order, s.Tool)
		if len(s.Args) > 0 && string(s.Args) != "null" && string(s.Args) != "{}" {
			fmt.Fprintf(&b, " with args %s", string(s.Args))
		}
		if s.Rationale != "" {
			fmt.Fprintf(&b, " — %s", s.Rationale)
		}
		b.WriteString("\n")
	}
	b.WriteString("Execute the steps in order. Stop and surface any tool error to the user before proceeding to the next step.")
	return b.String()
}

// summarizePlanSteps reduces a step list to the compact audit shape:
// order + tool + hash of args. We deliberately drop the rationale and
// raw args — audit rows are signals for PR #2's projection, not a
// replay log of the LLM's reasoning. Keeping rows small also keeps the
// SQLite memory store bounded on heavy planners.
func summarizePlanSteps(steps []planStep) []packs.PlanAuditStep {
	if len(steps) == 0 {
		return nil
	}
	out := make([]packs.PlanAuditStep, 0, len(steps))
	for _, s := range steps {
		summary := packs.PlanAuditStep{Order: s.Order, Tool: s.Tool}
		if len(s.Args) > 0 {
			sum := sha256.Sum256(s.Args)
			summary.ArgsSHA = hex.EncodeToString(sum[:8])
		}
		out = append(out, summary)
	}
	return out
}

// writePlanAuditViaMemory persists one plan_history row through the
// namespace-scoped memory adapter. Mirrors Engine.WritePlanAudit's
// shape so PR #2's projection sees the same body whether the row came
// from this handler or from a hypothetical engine-side caller.
func writePlanAuditViaMemory(ec *packs.ExecutionContext, model, intent, complexity string, steps []planStep, dur time.Duration) {
	audit := packs.PlanAudit{
		IntentSHA:  intentSHA(intent),
		Complexity: complexity,
		Steps:      summarizePlanSteps(steps),
		Outcome:    "ok",
		AtUnix:     time.Now().UTC().Unix(),
		DurationMs: dur.Milliseconds(),
		Model:      model,
	}
	body, err := json.Marshal(audit)
	if err != nil {
		ec.Logger.Warn("plan audit marshal failed", "err", err)
		return
	}
	key := fmt.Sprintf("%s%s/%020d", packs.AuditKeyPrefixPlan, audit.IntentSHA, time.Now().UTC().UnixNano())
	if err := ec.Memory.Store(key, body,
		memory.WithTTL(packs.AuditTTL),
		memory.WithCategory(packs.AuditCategoryPlan),
	); err != nil {
		ec.Logger.Warn("plan audit write failed", "err", err)
	}
}

// intentSHA produces a stable hash of the (trimmed, lowercased) intent
// string. PR #2's projection groups plans by intent_sha to surface
// most-used decompositions per intent class. We use the first 16 hex
// chars of sha256 — short enough for memory keys, long enough that
// per-caller collisions are vanishingly unlikely.
func intentSHA(intent string) string {
	norm := strings.ToLower(strings.TrimSpace(intent))
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:8])
}

// routingGuideFromCatalog converts route.go's catalogProjection into
// the shared llmcontext.RoutingGuide type so CompactCatalog can
// operate on it. The JSON wire shapes are identical — we only need
// the conversion because Go disallows direct cross-package struct
// type conversion on slice element types even when the underlying
// shapes match. Cheap O(N+M); we pay it once per plan call.
func routingGuideFromCatalog(c catalogProjection) llmcontext.RoutingGuide {
	rg := llmcontext.RoutingGuide{
		Packs:     make([]llmcontext.Pack, len(c.Packs)),
		Pipelines: make([]llmcontext.Pipeline, len(c.Pipelines)),
	}
	for i, p := range c.Packs {
		rg.Packs[i] = llmcontext.Pack{Name: p.Name, Description: p.Description, Metadata: p.Metadata}
	}
	for i, p := range c.Pipelines {
		rg.Pipelines[i] = llmcontext.Pipeline{ID: p.ID, Name: p.Name, Description: p.Description, Metadata: p.Metadata}
	}
	return rg
}

// catalogFromRoutingGuide is the inverse converter used after
// CompactCatalog returns the trimmed projection. The Metadata fields
// on each side are value types (PackMetadata is a struct; pipeline
// Metadata is json.RawMessage) so the shallow copy here is enough —
// CompactCatalog has already deep-copied internally.
func catalogFromRoutingGuide(rg llmcontext.RoutingGuide) catalogProjection {
	c := catalogProjection{
		Packs:     make([]routeCatalogPack, len(rg.Packs)),
		Pipelines: make([]routeCatalogPipe, len(rg.Pipelines)),
	}
	for i, p := range rg.Packs {
		c.Packs[i] = routeCatalogPack{Name: p.Name, Description: p.Description, Metadata: p.Metadata}
	}
	for i, p := range rg.Pipelines {
		c.Pipelines[i] = routeCatalogPipe{ID: p.ID, Name: p.Name, Description: p.Description, Metadata: p.Metadata}
	}
	return c
}
