// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// route.go — helmdeck.route meta-pack (ADR 047 PR #3).
//
// The chat agent calls this BEFORE picking a pack/pipeline for a multi-step
// request. It packages three signals into one LLM call:
//
//   1. Catalog (routing-guide shape) — every pack + pipeline with their
//      ADR 047 metadata (accepts/produces/intent_keywords/typical_use/
//      limitations, plus supersedes on pipelines).
//   2. Per-caller learned defaults (my-defaults projection) — top-N most-used
//      packs/pipelines for THIS subject, with their most-common input values.
//   3. The user's natural-language intent + optional context.
//
// The model returns a structured recommendation plus alternatives plus —
// CRITICALLY — a gap_warning when nothing in the catalog fits. The gap
// warning is a proposed pack name + input/output schema + integration
// pattern that the maintainer (or a follow-up github.create_issue call)
// can turn into a real pack.
//
// Why LLM-backed: deterministic metadata matching solves the easy case
// (intent keywords + accepts/produces overlap), but gap analysis needs
// reasoning over WHY the catalog doesn't fit. That's the value the route
// pack adds over a static lookup table.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tosin2013/helmdeck/internal/gateway"
	"github.com/tosin2013/helmdeck/internal/llmcontext"
	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/vision"
)

// PipelinesLister is the narrow surface helmdeck.route needs to enumerate
// pipelines + their metadata. Returning marshaled JSON keeps this builtin
// from importing internal/pipelines (cyclic-import-safe, same pattern the
// MCP layer uses for PipelineService).
//
// The JSON shape is the canonical pipeline-list array — at least
// {id, name, description, metadata?}. Extra fields are ignored.
type PipelinesLister interface {
	List(ctx context.Context) (json.RawMessage, error)
}

// routeDefaultMaxTokens caps how much room the model has to write the
// recommendation. Wide enough for a recommendation + 2-3 alternatives +
// a gap_warning + reasoning paragraph; tight enough to keep cost down.
const routeDefaultMaxTokens = 2000

// routeSystemPrompt is the model-facing instruction set. Versioned with
// the ADR. When PR #4 ships the management UI, this prompt stays put —
// it's about routing, not about UX.
const routeSystemPrompt = `You are the routing agent for helmdeck, a library of capability "packs" (atomic LLM/tool actions) and "pipelines" (chains of packs orchestrated as one call).

Your job: given the user's intent, pick the single best pipeline OR pack to run, propose pre-filled inputs from the caller's learned defaults, and — when NOTHING in the catalog fits — return a structured gap_warning naming the missing capability.

You will receive three blocks of context as part of the user message:
  1. CATALOG (routing-guide JSON): every pack and pipeline plus its metadata.
  2. CALLER DEFAULTS (my-defaults JSON): the caller's most-used packs/pipelines and their most-common input values.
  3. USER REQUEST: intent + optional context.

Routing rules:
  R1. Prefer a PIPELINE over chaining packs when its metadata.accepts matches the user's source kind AND metadata.produces matches the target format. Pipelines bundle steps the agent would otherwise have to chain.
  R2. If a pipeline's metadata.supersedes lists pack IDs the user mentioned by name, USE the pipeline (the maintained one-call surface).
  R3. For packs: score by metadata.intent_keywords + accepts/produces overlap with the user intent.
  R4. Pre-fill suggested_inputs from CALLER DEFAULTS common_inputs for the chosen pack/pipeline ID when present. If no defaults exist for the chosen ID, leave suggested_inputs empty — the agent will ask the user.
  R5. NEVER hallucinate an id. Only use ids that appear verbatim in CATALOG.packs[].name or CATALOG.pipelines[].id. If unsure between two, prefer the more-recently-used one (LastUsedUnix from CALLER DEFAULTS).
  R6. When NOTHING in the catalog reasonably fits (no pipeline matches accepts/produces AND no pack matches intent_keywords meaningfully), set gap_warning to a structured proposal: a realistic dotted.lowercase pack name (e.g. "youtube.transcript"), an input_schema + output_schema sketched with field names, an integration_pattern hint (e.g. "http-fetch + vault credential 'youtube-api-key' + parse caption tracks"), and a one-line why_useful explaining what existing packs it would chain with. When gap_warning is set, you may still set recommendation to the best partial match — but the reasoning MUST flag it as partial.

Output FORMAT — return exactly ONE JSON object, no prose around it, no code fences. Required shape:

{
  "recommendation": {
    "kind": "pipeline" | "pack",
    "id": "<exact id from CATALOG>",
    "suggested_inputs": { ...optional pre-fills from CALLER DEFAULTS, may be empty object... },
    "why": "<one short sentence>"
  },
  "alternatives": [
    {"kind": "pipeline" | "pack", "id": "<exact id>", "why": "<one short sentence>"}
  ],
  "gap_warning": null,
  "reasoning": "<1-3 sentences explaining the pick over alternatives and which defaults were used>"
}

When proposing a gap, replace null with:
  "gap_warning": {
    "missing_capability": "<what the user wants in their words>",
    "proposed_pack": {
      "name": "<dotted.lowercase>",
      "input_schema": { "<field>": "<type>", ... },
      "output_schema": { "<field>": "<type>", ... },
      "integration_pattern": "<one line: http-fetch / sdk / cli + auth source>",
      "why_useful": "<one line — what existing packs/pipelines it would chain with>"
    }
  }

alternatives is at most 3 entries and may be []. recommendation is REQUIRED — even when gap_warning is set, give the best partial match.`

// routeInput is the schema the agent calls helmdeck.route with.
type routeInput struct {
	UserIntent string          `json:"user_intent"`
	Context    json.RawMessage `json:"context,omitempty"`
	Model      string          `json:"model"`
	MaxTokens  int             `json:"max_tokens,omitempty"`
}

// routeOutput is what helmdeck.route returns. Mirrored from
// routeSystemPrompt so OutputSchema.Validate stays in sync.
type routeOutput struct {
	Recommendation routeRecommendation `json:"recommendation"`
	Alternatives   []routeAlternative  `json:"alternatives,omitempty"`
	GapWarning     *routeGapWarning    `json:"gap_warning,omitempty"`
	Reasoning      string              `json:"reasoning"`
	Model          string              `json:"model"`
}

type routeRecommendation struct {
	Kind            string          `json:"kind"`
	ID              string          `json:"id"`
	SuggestedInputs json.RawMessage `json:"suggested_inputs,omitempty"`
	Why             string          `json:"why,omitempty"`
}

type routeAlternative struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Why  string `json:"why,omitempty"`
}

type routeGapWarning struct {
	MissingCapability string                 `json:"missing_capability"`
	ProposedPack      map[string]interface{} `json:"proposed_pack"`
}

// catalogProjection is the routing-guide-shaped payload sent to the
// model. We rebuild it in-process here rather than importing the MCP
// layer; the shape MUST track internal/mcp/routing_guide.go closely
// (tests cover the parity).
type catalogProjection struct {
	Packs     []routeCatalogPack `json:"packs"`
	Pipelines []routeCatalogPipe `json:"pipelines"`
}

type routeCatalogPack struct {
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	Metadata    packs.PackMetadata `json:"metadata,omitempty"`
}

type routeCatalogPipe struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
}

// Route returns the helmdeck.route pack. Constructor wiring:
//   - d: LLM dispatcher (vault-resolved at engine layer). Required.
//   - reg: pack registry, queried for metadata + the id-existence check.
//   - pipes: pipelines lister; nil means "no pipelines surfaced to the model" — degrades to pack-only routing.
//
// MemoryStore for defaults is read off the engine at handler time via
// ec.Memory.Namespace() + a List call against AuditKeyPrefix*.
func Route(d vision.Dispatcher, reg *packs.Registry, pipes PipelinesLister) *packs.Pack {
	return &packs.Pack{
		Name:        "helmdeck.route",
		Version:     "v1",
		Description: "Recommend the best pipeline/pack for a user's natural-language intent. Combines the structured catalog (routing-guide), the caller's learned defaults (my-defaults), and an LLM reasoning step. Returns a recommendation + up to 3 alternatives + a `gap_warning` structured proposal when nothing in the catalog fits. Call this FIRST for any multi-step request.",
		Metadata: packs.PackMetadata{
			Accepts:        []string{"user_intent"},
			Produces:       []string{"routing_recommendation"},
			IntentKeywords: []string{"what should i use", "which pipeline", "recommend a pack", "route my request", "what's the best tool for"},
			TypicalUse:     "Pre-routing call before the agent picks a pack/pipeline. Surfaces gap_warning when the catalog can't serve the user.",
			Limitations:    []string{"LLM-backed: needs a wired dispatcher and a model id in the input", "recommendations are based on declared metadata — packs without populated metadata are invisible to the router", "does NOT execute the recommendation; the agent confirms with the user, then calls the recommended pack/pipeline"},
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
			Required: []string{"recommendation", "model"},
			Properties: map[string]string{
				"recommendation": "object",
				"alternatives":   "array",
				"gap_warning":    "object",
				"reasoning":      "string",
				"model":          "string",
			},
		},
		// Audit IS recorded for helmdeck.route — knowing how often the
		// router gets called and what it routed to is exactly the kind
		// of meta-signal PR #4's UI surfaces. NoAudit=false (default).
		Handler: routeHandler(d, reg, pipes),
	}
}

func routeHandler(d vision.Dispatcher, reg *packs.Registry, pipes PipelinesLister) packs.HandlerFunc {
	return func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
		if d == nil {
			return nil, &packs.PackError{Code: packs.CodeInternal,
				Message: "helmdeck.route registered without a gateway dispatcher"}
		}
		if reg == nil {
			return nil, &packs.PackError{Code: packs.CodeInternal,
				Message: "helmdeck.route registered without a pack registry"}
		}
		var in routeInput
		if err := json.Unmarshal(ec.Input, &in); err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
		}
		if strings.TrimSpace(in.UserIntent) == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "user_intent is required"}
		}
		if strings.TrimSpace(in.Model) == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "model is required (provider/model id; see helmdeck://models)"}
		}
		maxTokens := in.MaxTokens
		if maxTokens <= 0 {
			maxTokens = routeDefaultMaxTokens
		}

		// 1. Build the catalog projection. Same shape as
		// helmdeck://routing-guide; the agent already knows how to read it.
		catalog, validIDs := buildCatalog(ctx, reg, pipes)

		// 2. Pull the caller's defaults. ec.Memory is nil when no
		// memory store is wired ⇒ pass empty Defaults (the model
		// will gracefully omit suggested_inputs).
		defaults := packs.Defaults{Packs: []packs.ProjectedPack{}, Pipelines: []packs.ProjectedPipeline{}}
		if ec.Memory != nil {
			// ec.Memory is namespaced to the bare caller because
			// helmdeck.route's Pack has NeedsSession=false (default).
			caller := packs.CallerFromContext(ctx)
			if d, derr := defaultsFromAdapter(ctx, ec, caller); derr == nil {
				defaults = d
			} else {
				ec.Logger.Warn("helmdeck.route: my-defaults projection failed; routing without learned defaults", "err", derr)
			}
		}

		// ADR 050 PR #3: Select cascades through Tier-A pass-through
		// → metadata compaction (PR #1) → lexical retrieval + top-N
		// (PR #3) until the projection fits the model's budget. Same
		// cascade helmdeck.plan calls; keeping route + plan
		// observationally parallel makes operator traces easier to
		// read across both packs.
		budget := llmcontext.BudgetFor(in.Model)
		fullRG := routingGuideFromCatalog(catalog)
		selectedRG, trim := llmcontext.Select(fullRG, in.UserIntent, budget)
		// ADR 050 PR #4: same two-pass escalation helmdeck.plan
		// uses. The filter pass keeps the routing decision usable
		// on weak free models when lexical retrieval alone couldn't
		// narrow the catalog confidently.
		if budget.AllowsLLMFilter && shouldEscalateFromTrim(trim) {
			filterModel := budget.FilterModel
			if filterModel == "" {
				filterModel = in.Model
			}
			filteredRG, fStats, ferr := runFilterPass(ctx, d, ec, fullRG, selectedRG, in.UserIntent, filterModel)
			if ferr == nil {
				selectedRG = filteredRG
				if b, mErr := json.Marshal(selectedRG); mErr == nil {
					trim.AfterBytes = len(b)
				}
				trim.Dropped = append(trim.Dropped, fStats)
			} else {
				ec.Logger.Warn("helmdeck.route: LLM filter pass failed; falling back to lexical selection",
					"err", ferr,
				)
			}
		}
		catalog = catalogFromRoutingGuide(selectedRG)
		if len(trim.Dropped) > 0 {
			ec.Logger.Info("helmdeck.route: catalog selection ran",
				"model", in.Model,
				"tier", string(budget.Tier),
				"before_bytes", trim.BeforeBytes,
				"after_bytes", trim.AfterBytes,
				"dropped", trim.Dropped,
			)
		}

		ec.Report(20, "calling model for routing decision")

		// 3. Dispatch.
		user := buildRouteUserMessage(in.UserIntent, in.Context, catalog, defaults)
		mt := maxTokens
		chat, err := d.Dispatch(ctx, gateway.ChatRequest{
			Model:     in.Model,
			MaxTokens: &mt,
			Messages: []gateway.Message{
				{Role: "system", Content: gateway.TextContent(routeSystemPrompt)},
				{Role: "user", Content: gateway.TextContent(user)},
			},
		})
		if err != nil {
			return nil, dispatchError("helmdeck.route gateway", err)
		}
		if len(chat.Choices) == 0 {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed, Message: "gateway returned no choices"}
		}
		// ADR 051 PR #1 + #2: defensive parse uniform with plan.go,
		// with PR #2's finish_reason threading so failures get
		// classified by cause (safety filter / length truncation /
		// constrained deadlock / likely timeout).
		var out routeOutput
		if perr := DecodeStructuredResponseWithCause(
			chat.Choices[0].Message.Content.Text(),
			chat.Choices[0].FinishReason,
			"routing",
			&out,
		); perr != nil {
			return nil, perr
		}
		// Defensive: id MUST exist in the catalog. If the model
		// hallucinated, demote the recommendation to a gap_warning so
		// the agent doesn't dispatch to a non-existent pack.
		if !validIDs[out.Recommendation.ID] {
			out = demoteToGap(out, in.UserIntent)
		}
		out.Model = in.Model

		ec.Report(100, "routing recommendation ready")
		return json.Marshal(out)
	}
}

// buildCatalog walks the registry + pipelines lister and returns the
// projection shape PLUS a set of valid ids for post-validation. Same
// data the helmdeck://routing-guide MCP resource serves.
func buildCatalog(ctx context.Context, reg *packs.Registry, pipes PipelinesLister) (catalogProjection, map[string]bool) {
	valid := map[string]bool{}
	cat := catalogProjection{Packs: []routeCatalogPack{}, Pipelines: []routeCatalogPipe{}}
	for _, info := range reg.List() {
		p, err := reg.Get(info.Name, "")
		if err != nil {
			continue
		}
		cat.Packs = append(cat.Packs, routeCatalogPack{
			Name:        p.Name,
			Description: p.Description,
			Metadata:    p.Metadata,
		})
		valid[p.Name] = true
	}
	if pipes != nil {
		raw, err := pipes.List(ctx)
		if err == nil {
			var fullPipes []struct {
				ID          string          `json:"id"`
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Metadata    json.RawMessage `json:"metadata,omitempty"`
			}
			if jerr := json.Unmarshal(raw, &fullPipes); jerr == nil {
				for _, p := range fullPipes {
					cat.Pipelines = append(cat.Pipelines, routeCatalogPipe{
						ID:          p.ID,
						Name:        p.Name,
						Description: p.Description,
						Metadata:    p.Metadata,
					})
					valid[p.ID] = true
				}
			}
		}
	}
	return cat, valid
}

// defaultsFromAdapter pulls audit rows via ec.Memory and reuses the
// engine-level projection. ec.Memory's namespace is the bare caller
// because the route pack has NeedsSession=false (default).
func defaultsFromAdapter(ctx context.Context, ec *packs.ExecutionContext, caller string) (packs.Defaults, error) {
	// ec.Memory.List(prefix) returns entries within the adapter's
	// namespace. BuildDefaults wants the store directly, so we adapt:
	// list both prefixes and reconstruct PackAudit/PipelineAudit
	// arrays in-place. Avoids exposing the raw store on the
	// ExecutionContext.
	out := packs.Defaults{Packs: []packs.ProjectedPack{}, Pipelines: []packs.ProjectedPipeline{}}
	packEntries, err := ec.Memory.List(packs.AuditKeyPrefixPack)
	if err != nil {
		return out, err
	}
	pipeEntries, err := ec.Memory.List(packs.AuditKeyPrefixPipeline)
	if err != nil {
		return out, err
	}
	packAudits := make([]packs.PackAudit, 0, len(packEntries))
	for _, e := range packEntries {
		var a packs.PackAudit
		if err := json.Unmarshal(e.Value, &a); err == nil {
			packAudits = append(packAudits, a)
		}
	}
	pipeAudits := make([]packs.PipelineAudit, 0, len(pipeEntries))
	for _, e := range pipeEntries {
		var a packs.PipelineAudit
		if err := json.Unmarshal(e.Value, &a); err == nil {
			pipeAudits = append(pipeAudits, a)
		}
	}
	return packs.ProjectDefaults(packAudits, pipeAudits), nil
}

// buildRouteUserMessage assembles the catalog + defaults + intent
// prompt body. Catalog and defaults are pretty-printed for the model;
// the trailing USER REQUEST block puts the intent last so it stays
// salient.
func buildRouteUserMessage(intent string, contextJSON json.RawMessage, catalog catalogProjection, defaults packs.Defaults) string {
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

// demoteToGap is the fallback when the model picks an id that doesn't
// exist in the catalog (hallucination guard). We KEEP the model's
// reasoning + alternatives — they may still be useful — but blank
// the recommendation.id and surface a gap_warning so the agent can't
// dispatch to nothing.
func demoteToGap(out routeOutput, intent string) routeOutput {
	out.Reasoning = fmt.Sprintf("Model proposed id %q which is not in the catalog — demoted to gap_warning. %s", out.Recommendation.ID, out.Reasoning)
	out.Recommendation = routeRecommendation{
		Kind: "none",
		ID:   "",
		Why:  "no catalog entry matched; see gap_warning",
	}
	if out.GapWarning == nil {
		out.GapWarning = &routeGapWarning{
			MissingCapability: intent,
			ProposedPack: map[string]interface{}{
				"name":                "TBD",
				"input_schema":        map[string]interface{}{},
				"output_schema":       map[string]interface{}{},
				"integration_pattern": "model did not propose a concrete pack — the agent should ask the user for more detail and re-route",
				"why_useful":          "current catalog has no matching pack or pipeline",
			},
		}
	}
	return out
}
