// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// content_ground.go (T623, ADR 035) — link-grounding for markdown.
//
// An agent hands in a path to a blog post in a session-local clone.
// The pack (1) asks the gateway LLM to extract the top N factual
// claims that would benefit from a citation, (2) runs each claim's
// generated search query through Firecrawl /v1/search, (3) picks
// the first result with a non-empty URL, and (4) rewrites the
// markdown file in place by appending ` [source](url)` after each
// grounded claim. The result is a report of which claims got
// citations, which were skipped, and the new sha256 of the file.
//
// Why one pack and not `research.deep` + `fs.patch` chained by the
// agent?
//   - Claim extraction is a strict JSON round trip — delegating it
//     to the agent means every caller has to re-implement the same
//     prompt and parser, and drift across callers produces
//     inconsistent groundings.
//   - The substring the LLM picks as "the claim" has to match the
//     file content exactly. When the agent orchestrates from
//     outside, the claim text and the file content live in two
//     different context windows and drift is common. One pack that
//     owns the whole pipeline avoids that whole class of bug.
//   - The fs write needs to happen once per run, not once per
//     claim, so we can cap the number of session-shell round trips
//     regardless of how many claims were grounded.
//
// Milestone note (T623): the original milestone text mentions
// `github.search` + `http.fetch` + `web.scrape` + `fs.patch` as the
// canonical chain. We collapse the first three into a single
// Firecrawl `/v1/search` call with inline scrape — Google (which
// Firecrawl's self-hosted default uses) indexes GitHub repos,
// docs, and issues alongside the rest of the web, so one API path
// covers "search GitHub and the wider web" without the plumbing of
// a separate github.search integration. Operators who need
// GitHub-only results add a `site:github.com` token to the
// generated query (the claim-extractor prompt honours a `topic`
// hint for exactly this reason).
//
// Deployment:
//   - Gated on HELMDECK_FIRECRAWL_ENABLED=true (same toggle as
//     web.scrape and research.deep).
//   - NeedsSession=true because the markdown file lives in a
//     session-local clone — fs reads/writes go through the session
//     executor the same way fs.patch does.
//   - No egress guard on claim queries (they are search strings,
//     not URLs). Firecrawl's own egress policy enforces SSRF
//     defence on the crawler side.
//
// Input shape:
//
//	{
//	  "clone_path": "/tmp/helmdeck-abc/posts",
//	  "path":       "2026-quantum.md",
//	  "model":      "openai/gpt-4o-mini",
//	  "max_claims": 5,                                  // optional, default 5, cap 8
//	  "topic":      "quantum computing"                 // optional hint to bias the claim extractor
//	}
//
// Output shape:
//
//	{
//	  "path":              "2026-quantum.md",
//	  "claims_considered": 5,
//	  "claims_grounded":   3,
//	  "grounding":         [ { "claim": "...", "url": "...", "title": "..." }, ... ],
//	  "skipped":           [ "claim with no source found" ],
//	  "sha256":            "hex-of-patched-file",
//	  "file_changed":      true
//	}

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/tosin2013/helmdeck/internal/gateway"
	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/vision"
)

const (
	defaultContentGroundClaims = 5
	maxContentGroundClaims     = 8

	// JIT length-sizing constants (issue #532 / convention #525).
	// content.ground is **cost-cap shaped** like research.deep —
	// each claim costs a Firecrawl /v1/search + a per-source LLM
	// verify call. Intent maps to the existing max_claims input:
	//   summary    → 3 claims
	//   thorough   → 5 claims (matches the legacy default)
	//   exhaustive → 8 claims (matches the legacy ceiling)
	//
	// Despite the issue's original "back-compat break" framing,
	// the current code is already capped at 8 with a default of 5
	// — exhaustive=8 is just a label for today's hard cap, not a
	// new behavior. Existing callers passing `max_claims` see ZERO
	// change.
	contentGroundIntentSummary    = "summary"
	contentGroundIntentThorough   = "thorough"
	contentGroundIntentExhaustive = "exhaustive"
	contentGroundIntentDefault    = contentGroundIntentThorough
	// defaultContentGroundTokens is the completion-token cap for the
	// claim-extractor call. 1024 was too tight: the system prompt +
	// topic + 5-8 claim JSON entries can land near ~750 tokens, leaving
	// minimal headroom; weak models or large posts would truncate the
	// JSON mid-response, surfacing as "unparseable JSON" with an empty
	// snippet (#179). 2048 gives ~1200 tokens of output budget while
	// staying cheap on the common case.
	defaultContentGroundTokens = 2048
	maxContentGroundTokens     = 8192
	// contentGroundPrompt is the frozen system prompt for the claim
	// extractor. The strict JSON schema is critical — we parse the
	// response with json.Unmarshal and bail on invalid_input if it
	// doesn't match, so small models need a very concrete example.
	contentGroundPrompt = `You are a fact-checker for technical blog posts. You will receive the full markdown of a post and a maximum number of claims to extract. Your job is to pick the most impactful factual claims that would benefit from an authoritative citation.

For each claim:
  - "text" MUST be a verbatim substring of the original markdown — copy it exactly, including punctuation, so the caller can locate it with a literal substring match. Do NOT rephrase.
  - "query" is the search query you would use to find a source — specific enough to reach authoritative material, not a generic topic word.

Respond with ONE JSON object and nothing else. No markdown fences, no commentary. Schema:

{
  "claims": [
    {"text": "<exact substring from the post>", "query": "<search query>"},
    ...
  ]
}

Rules:
  - Return AT MOST the requested number of claims. Prefer fewer high-quality claims over many weak ones.
  - Skip claims that are trivially obvious, subjective ("I think", "arguably"), or already contain a link.
  - Skip headings, code blocks, and list bullets — ground only prose sentences.
  - If no claim meets the bar, return {"claims": []}.`

	// contentGroundConcurrency caps how many claims we Firecrawl-
	// search + LLM-verify in parallel. 4 is a balance:
	//   - Below 4, the wall-clock win shrinks (a 12-claim post still
	//     takes ~3 sequential batches of the verify LLM call).
	//   - Above 4, free-tier Firecrawl rate limits kick in (~10
	//     concurrent /v1/search calls) and the LLM gateway starts
	//     queueing.
	// Operators can revisit if they're running self-hosted Firecrawl
	// with a higher limit; the constant is the lever.
	contentGroundConcurrency = 4
)

// contentGroundIntentTable maps each intent name to the max_claims
// value it should produce. Mirrors researchDeepIntentTable's shape.
var contentGroundIntentTable = map[string]int{
	contentGroundIntentSummary:    3,
	contentGroundIntentThorough:   5,
	contentGroundIntentExhaustive: 8,
}

// contentGroundSize is the resolved max_claims + label for one call.
// Mirrors the size struct in other JIT-adopted packs.
type contentGroundSize struct {
	maxClaims int
	applied   string // intent:summary / explicit / default
}

// resolveContentGroundSize encodes the precedence:
//  1. MaxClaims > 0 (explicit) → honor verbatim, clamped to ceiling
//  2. LengthIntent set → table
//  3. Default → defaultContentGroundClaims (5, matches legacy default)
//
// Note: thorough's row (5) equals the legacy default and exhaustive's
// row (8) equals the legacy ceiling — so existing callers see ZERO
// numerical behavior change. Labeling distinguishes which path the
// resolver took.
func resolveContentGroundSize(in *contentGroundInput) contentGroundSize {
	if in.MaxClaims > 0 {
		clamped := in.MaxClaims
		if clamped > maxContentGroundClaims {
			clamped = maxContentGroundClaims
		}
		return contentGroundSize{maxClaims: clamped, applied: "explicit"}
	}
	key := strings.ToLower(strings.TrimSpace(in.LengthIntent))
	if key != "" {
		mc, ok := contentGroundIntentTable[key]
		if !ok {
			mc = contentGroundIntentTable[contentGroundIntentDefault]
			key = contentGroundIntentDefault
		}
		return contentGroundSize{maxClaims: mc, applied: "intent:" + key}
	}
	return contentGroundSize{maxClaims: defaultContentGroundClaims, applied: "default"}
}

// ContentGround constructs the pack.
func ContentGround(d vision.Dispatcher) *packs.Pack {
	return &packs.Pack{
		Name:    "content.ground",
		Version: "v1",
		Description: "Extract factual claims from markdown, find authoritative sources via Firecrawl, " +
			"verify each source supports the claim, and optionally rewrite weak claims into stronger " +
			"prose backed by what the sources actually say. Accepts text directly or a file in a session clone. " +
			"Produces a grounded markdown artifact for download.",
		Metadata: packs.PackMetadata{
			Accepts:        []string{"markdown", "text", "clone_path+path"},
			Produces:       []string{"grounded_markdown"},
			IntentKeywords: []string{"add citations", "fact check", "cite sources", "ground claims", "strengthen claims", "quick grounding pass", "thorough grounding", "exhaustive grounding"},
			TypicalUse:     "Annotator pack — inserts [source](url) links for the claims it can find sources for. Use as the citation pass AFTER a generator pack (e.g. blog.rewrite_for_audience) for the rewrite-blog pipeline family. Use length_intent (summary / thorough / exhaustive) to scale per-call cost — summary=3 claims, thorough=5, exhaustive=8.",
			Limitations:    []string{"is an ANNOTATOR, not a GENERATOR — output is roughly the same length and shape as input", "requires HELMDECK_FIRECRAWL_ENABLED + the Firecrawl overlay", "does not generate prose from a brief — pasted briefs come back as the brief plus a few links", "truncated:true signals the claim extractor LLM hit max_tokens (or the rewrite path truncated) — fewer claims surfaced than the post may actually contain"},
		},
		// NeedsSession is false because the text-mode path doesn't
		// require a session. When clone_path + path are provided the
		// handler checks ec.Exec at runtime and returns a clear error
		// if the session isn't available.
		NeedsSession: false,
		InputSchema: packs.BasicSchema{
			// `model` is no longer Required: when omitted, the handler
			// resolves a default via defaultPackModel() — see
			// model_defaults.go for the precedence chain
			// (input → HELMDECK_DEFAULT_PACK_MODEL → first of
			// HELMDECK_OPENROUTER_MODELS → openrouter/auto). This
			// closes the Tier C silent-skip gap where small models
			// reading skill prose forget to pass the model argument
			// and the call rejects with "model: must have required
			// properties model" before any actual work begins.
			Properties: map[string]string{
				"clone_path":    "string",
				"path":          "string",
				"text":          "string",
				"model":         "string",
				"max_claims":    "number",
				"topic":         "string",
				"rewrite":       "boolean",
				"persona":       "string",
				"length_intent": "string",
				"inspect":       "boolean",
			},
		},
		OutputSchema: packs.BasicSchema{
			// Required deliberately narrows in inspect mode — the
			// engine's BasicSchema.Validate rejects missing required
			// fields. Inspect doesn't produce claims_considered /
			// _grounded / sha256 because no extraction happens. We
			// drop the inspect-irrelevant fields from Required and
			// let the runtime path keep populating them.
			Required: []string{},
			Properties: map[string]string{
				"path":              "string",
				"claims_considered": "number",
				"claims_grounded":   "number",
				"grounding":         "array",
				"skipped":           "array",
				"sha256":            "string",
				"file_changed":      "boolean",
				"grounded_text":     "string",
				"artifact_key":      "string",
				"persona_used":      "string",
				// JIT length-sizing telemetry (issue #531). Reported
				// on every generate response so callers see what
				// scale the pack ran at.
				"max_claims_applied":    "number",
				"length_intent_applied": "string",
				"truncated":             "boolean",
				// Inspect mode only.
				"inspect":              "boolean",
				"suggested_max_claims": "number",
				"reason":               "string",
			},
		},
		Handler: contentGroundHandler(d),
		// Heavy: extract → per-claim search → per-claim verify (LLM)
		// → optional whole-document rewrite (LLM). With rewrite=true
		// and a handful of claims, 60-120s is typical. Async=true
		// keeps the JSON-RPC request short via the SEP-1686 task
		// envelope; clients that need synchronous behavior can still
		// use the legacy pack.start/status/result trio explicitly.
		Async: true,
		// Memory cache (ADR 047): content.ground is expensive
		// (Firecrawl search + per-claim LLM verify + optional
		// whole-document rewrite — typical 60-120s). A 24-hour TTL
		// matches the cadence at which source authority changes
		// (slow, by web-content standards) and is well above
		// github.go's 5-minute pattern, which targets fast-moving
		// API content. The engine-level cache key is the hash of
		// (caller, input bytes) — see memory_seam_test.go:102. This
		// means the cache serves idempotent re-runs (same input
		// markdown + same options → cached result) but is a MISS
		// when the input changes by even one byte (typo fix,
		// whitespace edit, etc.). For per-claim caching across
		// edits, a handler-internal cache keyed on (claim_text,
		// search_query) would be the next layer — out of scope for
		// this change but tracked in the audit follow-up.
		Memory: &packs.MemoryConfig{Cache: true, TTL: 24 * time.Hour, Category: "cache"},
	}
}

type contentGroundInput struct {
	ClonePath string `json:"clone_path"`
	Path      string `json:"path"`
	Text      string `json:"text"` // direct text mode — no session needed
	Model     string `json:"model"`
	MaxClaims int    `json:"max_claims"`
	Topic     string `json:"topic"`
	Rewrite   bool   `json:"rewrite"` // when true, rewrite weak claims using source content
	// MaxCompletionTokens optionally raises the claim-extractor's
	// completion cap above the 2048 default. Useful for posts with
	// long claim summaries or when running against a weak model that
	// produces verbose JSON. Hard upper bound: 8192.
	MaxCompletionTokens int `json:"max_completion_tokens,omitempty"`
	// Persona tunes the rewrite step's tone/register when Rewrite=true.
	// Closed set: general / technical / marketing / executive /
	// educational / academic; anything else is a freeform tone hint.
	// Ignored when Rewrite=false (citation-only mode doesn't change
	// voice). See contentGroundPersonas.
	Persona string `json:"persona,omitempty"`

	// JIT length-sizing inputs (issue #531 / convention #525).
	// LengthIntent declares "summary" / "thorough" / "exhaustive";
	// the pack maps that to a max_claims value (3 / 5 / 8) when
	// explicit MaxClaims isn't set. Explicit MaxClaims wins
	// (back-compat preserved).
	LengthIntent string `json:"length_intent,omitempty"`
	// Inspect: return the resolved max_claims + intent without
	// firing Firecrawl or the LLMs. Skips the Firecrawl-enabled
	// gate too, so agents can plan in environments where the
	// overlay isn't wired.
	Inspect bool `json:"inspect,omitempty"`
}

// claimPlan is the parsed shape the extractor LLM returns.
type claimPlan struct {
	Claims []struct {
		Text  string `json:"text"`
		Query string `json:"query"`
	} `json:"claims"`
}

type grounding struct {
	Claim   string `json:"claim"`
	URL     string `json:"url"`
	Title   string `json:"title,omitempty"`
	Snippet string `json:"snippet,omitempty"` // excerpt from source that supports the claim
}

func contentGroundHandler(d vision.Dispatcher) packs.HandlerFunc {
	return func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
		var in contentGroundInput
		if err := json.Unmarshal(ec.Input, &in); err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
		}

		// JIT inspect short-circuit (issue #531). Runs before the
		// dispatcher / Firecrawl-enabled checks so agents can plan
		// a grounding pass in environments where Firecrawl isn't
		// up. No HTTP calls, no LLM calls.
		if in.Inspect {
			size := resolveContentGroundSize(&in)
			reason := fmt.Sprintf(
				"applying %s for max_claims=%d (per-claim Firecrawl + verify LLM cost). No grounding ran.",
				size.applied, size.maxClaims)
			return json.Marshal(map[string]any{
				"inspect":               true,
				"suggested_max_claims":  size.maxClaims,
				"length_intent_applied": size.applied,
				"reason":                reason,
			})
		}

		if d == nil {
			return nil, &packs.PackError{
				Code:    packs.CodeInternal,
				Message: "content.ground registered without a gateway dispatcher",
			}
		}
		if os.Getenv("HELMDECK_FIRECRAWL_ENABLED") != "true" {
			return nil, &packs.PackError{
				Code: packs.CodeInvalidInput,
				Message: "content.ground is disabled; set HELMDECK_FIRECRAWL_ENABLED=true " +
					"and bring up the Firecrawl overlay (deploy/compose/compose.firecrawl.yml)",
			}
		}
		base := strings.TrimRight(os.Getenv("HELMDECK_FIRECRAWL_URL"), "/")
		if base == "" {
			base = defaultFirecrawlURL
		}

		// Resolve a default when the caller omitted model. Tier C
		// models calling this pack via MCP routinely skip the model
		// arg; defaultPackModel guarantees a non-empty value.
		in.Model = defaultPackModel(in.Model)
		// Resolve persona once, up front, so every successful return
		// path can echo persona_used — including the "no claims found"
		// early return. directive is only consumed when Rewrite=true.
		personaDirective, personaUsed := resolveContentGroundPersona(in.Persona)
		// JIT length-sizing precedence (issue #531): explicit
		// max_claims > length_intent > legacy default 5. Existing
		// callers passing max_claims see ZERO behavior change —
		// the resolver's clamping matches the existing inline
		// check that used to live here.
		size := resolveContentGroundSize(&in)
		maxClaims := size.maxClaims
		maxTokens := defaultContentGroundTokens
		if in.MaxCompletionTokens > 0 {
			if in.MaxCompletionTokens > maxContentGroundTokens {
				return nil, &packs.PackError{
					Code: packs.CodeInvalidInput,
					Message: fmt.Sprintf("max_completion_tokens %d exceeds cap of %d",
						in.MaxCompletionTokens, maxContentGroundTokens),
				}
			}
			maxTokens = in.MaxCompletionTokens
		}

		// Two modes:
		//   (A) text mode — markdown provided directly, no session needed
		//   (B) file mode — read from clone_path + path in a session
		var original string
		var full string
		textMode := strings.TrimSpace(in.Text) != ""

		if textMode {
			original = in.Text
		} else {
			// File mode — requires session + clone_path + path
			if strings.TrimSpace(in.ClonePath) == "" {
				return nil, &packs.PackError{Code: packs.CodeInvalidInput,
					Message: "either 'text' (direct markdown) or 'clone_path' + 'path' (file in session) is required"}
			}
			if strings.TrimSpace(in.Path) == "" {
				return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "path is required when using clone_path"}
			}
			if ec.Exec == nil {
				return nil, &packs.PackError{Code: packs.CodeSessionUnavailable,
					Message: "content.ground file mode requires a session executor; use 'text' for direct markdown input"}
			}
			var perr *packs.PackError
			full, perr = safeJoin(in.ClonePath, in.Path, ec)
			if perr != nil {
				return nil, perr
			}
			statRes, err := runShell(ctx, ec, "wc -c < "+shellQuote(full), nil)
			if err != nil || statRes.ExitCode != 0 {
				return nil, &packs.PackError{
					Code:    packs.CodeInvalidInput,
					Message: fmt.Sprintf("file not readable: %s", strings.TrimSpace(string(statRes.Stderr))),
				}
			}
			size, _ := strconv.ParseInt(strings.TrimSpace(string(statRes.Stdout)), 10, 64)
			if size > maxFsReadBytes {
				return nil, &packs.PackError{
					Code:    packs.CodeInvalidInput,
					Message: fmt.Sprintf("file is %d bytes, exceeds %d byte cap", size, maxFsReadBytes),
				}
			}
			readRes, err := runShell(ctx, ec, "cat "+shellQuote(full), nil)
			if err != nil || readRes.ExitCode != 0 {
				return nil, &packs.PackError{
					Code:    packs.CodeHandlerFailed,
					Message: "failed to read markdown file",
				}
			}
			original = string(readRes.Stdout)
		}
		if strings.TrimSpace(original) == "" {
			return nil, &packs.PackError{
				Code:    packs.CodeInvalidInput,
				Message: "markdown content is empty",
			}
		}

		// 3. Claim extraction via LLM. The topic hint (when set)
		// gets a dedicated line in the user message — the prompt
		// knows what to do with it because it reads like part of
		// the goal framing rather than a schema field.
		ec.Report(10, fmt.Sprintf("extracting claims (max %d, %s)", maxClaims, size.applied))
		claims, rawModel, extractFinishReason, perr := extractClaims(ctx, d, in.Model, original, in.Topic, maxClaims, maxTokens)
		if perr != nil {
			return nil, perr
		}
		// Track truncation: extractor finish_reason=length means the
		// claim list may be incomplete. The rewrite step's truncation
		// is handled separately (errRewriteTruncated falls back to
		// the citation-only version) and folded into this flag below.
		extractorTruncated := strings.EqualFold(extractFinishReason, "length")
		if len(claims) == 0 {
			// No groundable claims — return a clean report, don't
			// touch the file. Not an error: a well-grounded post
			// is the ideal outcome.
			sum := sha256.Sum256([]byte(original))
			return json.Marshal(map[string]any{
				"path":              in.Path,
				"claims_considered": 0,
				"claims_grounded":   0,
				"grounding":         []grounding{},
				"skipped":           []string{},
				"sha256":            hex.EncodeToString(sum[:]),
				"file_changed":      false,
				// grounded_text is a total contract: it is always
				// present so downstream pipeline steps (e.g.
				// slides.render in builtin.grounded-deck) can wire
				// ${{ steps.ground.output.grounded_text }} without the
				// reference failing when a post had no groundable
				// claims. Here the text is unchanged from the input.
				"grounded_text": original,
				"persona_used":  personaUsed,
				// JIT length-sizing telemetry (issue #531).
				"max_claims_applied":    maxClaims,
				"length_intent_applied": size.applied,
				"truncated":             extractorTruncated,
			})
		}
		_ = rawModel // retained for future audit logging; not surfaced today

		// 4. For each claim, search Firecrawl and take the first
		// result with a non-empty URL. Skip claims whose text
		// doesn't appear in the markdown (hallucinated substrings
		// are a failure mode for smaller models) and claims whose
		// query returns no usable source.
		//
		// Architecturally split in three phases (was one sequential
		// loop in v0.27.x and earlier — the audit found 60-120s
		// pipeline runs because per-claim REST + LLM calls couldn't
		// run in parallel):
		//
		//   Phase 1 (synchronous, fast): locate each claim's byte
		//     span in the markdown. Hallucinated claims fall out
		//     here without spending a Firecrawl call. Uses fuzzy
		//     matching (exact substring first; whitespace-tolerant
		//     scan on miss) so a slight whitespace/punctuation
		//     normalization from the LLM extractor doesn't drop a
		//     real claim.
		//
		//   Phase 2 (concurrent): for each findable claim, call
		//     Firecrawl + verify in parallel. Bounded errgroup
		//     keeps Firecrawl-side concurrency reasonable while
		//     collapsing wall-clock from N×(search+verify) down to
		//     ceil(N/workers)×(search+verify). Errors are collected
		//     per-claim, not propagated (one bad claim doesn't
		//     short-circuit the others).
		//
		//   Phase 3 (sequential): apply each result to `patched` in
		//     original claim order. Patching must be sequential
		//     because each substitution can move byte offsets of
		//     subsequent claims; re-finding the claim per-iteration
		//     handles the case where an earlier patch's `[source]`
		//     suffix has shifted the document.
		groundings := make([]grounding, 0, len(claims))
		skipped := make([]string, 0)
		patched := original
		considered := len(claims)
		// firecrawlCalls counts claims that survived the substring
		// check and reached callFirecrawlSearch. firecrawlErrors
		// counts how many of those returned a transport error.
		// When every reached call failed → Firecrawl is unreachable
		// and we must fail loud (#182) rather than return an empty-
		// success "no sources found" output. "Search returned zero
		// results" or "verify rejected the result" do NOT increment
		// firecrawlErrors — those are legitimate empty outcomes that
		// preserve partial-success behavior when Firecrawl is healthy.
		firecrawlCalls := 0
		firecrawlErrors := 0

		// Phase 1 — fuzzy-locate findable claims, drop the rest.
		type pendingClaim struct {
			idx   int // index into claims (preserves order for Phase 3)
			text  string
			query string
		}
		pending := make([]pendingClaim, 0, len(claims))
		for i, c := range claims {
			if _, _, ok := findClaimSpan(patched, c.Text); !ok {
				skipped = append(skipped, c.Text)
				continue
			}
			pending = append(pending, pendingClaim{idx: i, text: c.Text, query: c.Query})
		}

		// Phase 2 — concurrent Firecrawl + verify.
		type claimResult struct {
			idx       int
			text      string
			pick      *firecrawlSearchItem
			snippet   string
			searchErr error
		}
		results := make([]claimResult, len(pending))
		if len(pending) > 0 {
			ec.Report(20, fmt.Sprintf("grounding %d claims concurrently (limit=%d)",
				len(pending), contentGroundConcurrency))
			grp, gctx := errgroup.WithContext(ctx)
			grp.SetLimit(contentGroundConcurrency)
			for i, p := range pending {
				i, p := i, p // capture
				grp.Go(func() error {
					r := claimResult{idx: p.idx, text: p.text}
					fc, searchErr := callFirecrawlSearch(gctx, base, firecrawlSearchRequest{
						Query: p.query,
						Limit: 3,
						ScrapeOptions: &firecrawlSearchScrapeOpt{
							Formats: []string{"markdown"},
						},
					})
					if searchErr != nil {
						r.searchErr = searchErr
						results[i] = r
						return nil // never short-circuit; siblings finish
					}
					r.pick, r.snippet = verifyBestSource(gctx, d, in.Model, p.text, fc.Data)
					results[i] = r
					return nil
				})
			}
			_ = grp.Wait() // never errors — all goroutines return nil
		}

		// Phase 3 — apply results to `patched` in original claim order.
		// `pending` was built by iterating `claims` in order, so
		// `results` is already in pending-order; that's the same as
		// original-claim-order modulo the already-skipped entries.
		ec.Report(85, "patching grounded claims")
		for _, r := range results {
			firecrawlCalls++
			if r.searchErr != nil {
				firecrawlErrors++
				if ec.Logger != nil {
					ec.Logger.Warn("firecrawl search failed", "claim", r.text, "err", r.searchErr)
				}
				skipped = append(skipped, r.text)
				continue
			}
			if r.pick == nil {
				skipped = append(skipped, r.text)
				continue
			}
			// Re-locate: an earlier patch in this loop may have
			// shifted byte offsets. The fuzzy matcher also tolerates
			// the case where the LLM emitted a slightly-normalized
			// claim text vs the actual markdown bytes.
			start, end, ok := findClaimSpan(patched, r.text)
			if !ok {
				// Lost during patching (overlapping claims, etc.).
				// Skip rather than corrupt the file.
				skipped = append(skipped, r.text)
				continue
			}
			// Preserve the ORIGINAL bytes from the document; the
			// LLM may have given us a normalized variant. We splice
			// `[source](url)` after the doc's literal text, not the
			// LLM's normalized text.
			docText := patched[start:end]
			insertion := fmt.Sprintf("%s [source](%s)", docText, r.pick.URL)
			patched = patched[:start] + insertion + patched[end:]
			title := r.pick.Title
			if title == "" {
				title = r.pick.Metadata.Title
			}
			groundings = append(groundings, grounding{
				Claim:   r.text,
				URL:     r.pick.URL,
				Title:   title,
				Snippet: r.snippet,
			})
		}

		// Fail loud if every Firecrawl call we attempted errored at
		// the transport layer — that's a service issue, not a result
		// issue, and silently producing an empty-success output would
		// mislead the caller into thinking content.ground "tried but
		// found nothing" rather than "couldn't reach Firecrawl" (#182).
		if firecrawlCalls > 0 && firecrawlErrors == firecrawlCalls {
			return nil, &packs.PackError{
				Code:    packs.CodeHandlerFailed,
				Message: fmt.Sprintf("content.ground: every Firecrawl search call failed; verify the firecrawl service is reachable at %s", base),
			}
		}

		// 5. Rewrite (optional). When rewrite=true, ask the LLM to
		// improve each grounded claim using the source content so
		// the prose is stronger, more specific, and properly cited
		// inline — not just a bare [source](url) appended. Persona
		// (when set) tunes the register of the rewrite without
		// changing structure — without it, the model defaulted to
		// formal-academic for every input. Citation-only mode
		// (rewrite=false) doesn't touch the prose, so persona is
		// ignored there by design.
		rewriteTruncated := false
		if in.Rewrite && len(groundings) > 0 {
			ec.Report(85, fmt.Sprintf("rewriting prose with sources (persona: %s)", personaUsed))
			rewritten, err := rewriteWithSources(ctx, d, in.Model, patched, groundings, personaDirective)
			if err != nil {
				// errRewriteTruncated is a recoverable signal — the
				// pack ships the citation-only version but flags
				// truncated:true so the operator knows the rewrite
				// path hit max_tokens.
				if errors.Is(err, errRewriteTruncated) {
					rewriteTruncated = true
				}
				ec.Logger.Warn("rewrite failed, keeping citation-only version", "err", err)
			} else if strings.TrimSpace(rewritten) != "" {
				patched = rewritten
			}
		}

		// 6. Write back + artifact.
		fileChanged := patched != original

		// File mode: write the patched file back to the session.
		if !textMode && fileChanged && ec.Exec != nil {
			writeRes, err := runShell(ctx, ec, "cat > "+shellQuote(full), []byte(patched))
			if err != nil || writeRes.ExitCode != 0 {
				return nil, &packs.PackError{
					Code:    packs.CodeHandlerFailed,
					Message: fmt.Sprintf("write back failed: %s", strings.TrimSpace(string(writeRes.Stderr))),
				}
			}
		}

		// Always upload the grounded markdown as a downloadable
		// artifact so the operator can copy-paste it to a blog.
		var artifactKey string
		if ec.Artifacts != nil && fileChanged {
			art, err := ec.Artifacts.Put(ctx, "content.ground", "grounded.md", []byte(patched), "text/markdown")
			if err != nil {
				ec.Logger.Warn("artifact upload failed", "err", err)
			} else {
				artifactKey = art.Key
			}
		}

		sum := sha256.Sum256([]byte(patched))
		out := map[string]any{
			"path":              in.Path,
			"claims_considered": considered,
			"claims_grounded":   len(groundings),
			"grounding":         groundings,
			"skipped":           skipped,
			"sha256":            hex.EncodeToString(sum[:]),
			"file_changed":      fileChanged,
			"grounded_text":     patched,
			"persona_used":      personaUsed,
			// JIT length-sizing telemetry (issue #531). truncated
			// fires if EITHER the claim extractor hit max_tokens
			// (incomplete claim list) OR the rewrite step truncated
			// (citation-only fallback shipped instead). Operators
			// reading truncated:true re-run with smaller intent or
			// larger max_completion_tokens.
			"max_claims_applied":    maxClaims,
			"length_intent_applied": size.applied,
			"truncated":             extractorTruncated || rewriteTruncated,
		}
		if artifactKey != "" {
			out["artifact_key"] = artifactKey
		}
		return json.Marshal(out)
	}
}

// extractClaims asks the LLM to pick up to maxClaims grounding
// candidates. Returns the parsed claim list plus the raw model
// response (useful for future audit logging).
func extractClaims(ctx context.Context, d vision.Dispatcher, model, markdown, topic string, maxClaims, maxTokens int) ([]struct {
	Text  string `json:"text"`
	Query string `json:"query"`
}, string, string, *packs.PackError) {
	var userMsg strings.Builder
	fmt.Fprintf(&userMsg, "MAX_CLAIMS: %d\n", maxClaims)
	if strings.TrimSpace(topic) != "" {
		fmt.Fprintf(&userMsg, "TOPIC HINT: %s\n", topic)
	}
	userMsg.WriteString("\nPOST:\n")
	userMsg.WriteString(markdown)
	req := gateway.ChatRequest{
		Model:     model,
		MaxTokens: &maxTokens,
		Messages: []gateway.Message{
			{Role: "system", Content: gateway.TextContent(contentGroundPrompt)},
			{Role: "user", Content: gateway.TextContent(userMsg.String())},
		},
	}
	resp, err := d.Dispatch(ctx, req)
	if err != nil {
		return nil, "", "", dispatchError("claim extractor dispatch", err)
	}
	if len(resp.Choices) == 0 {
		return nil, "", "", &packs.PackError{
			Code:    packs.CodeHandlerFailed,
			Message: "claim extractor returned no choices",
			Cause:   errors.New("empty choices"),
		}
	}
	finishReason := resp.Choices[0].FinishReason
	raw := strings.TrimSpace(resp.Choices[0].Message.Content.Text())
	plan, perr := parseClaimPlan(raw)
	if perr != nil {
		return nil, raw, finishReason, perr
	}
	// Cap to maxClaims in case the model ignored the instruction.
	if len(plan.Claims) > maxClaims {
		plan.Claims = plan.Claims[:maxClaims]
	}
	return plan.Claims, raw, finishReason, nil
}

// parseClaimPlan delegates to the shared DecodeStructuredResponse
// helper (ADR 051 PR #1) so the same defensive pipeline — reasoning-
// token stripping, code-fence unwrap, decoder-tolerant parse,
// balanced-brace substring fallback — applies uniformly across
// every LLM-backed pack. On failure, the helper's error already
// has the right CodeHandlerFailed code; we wrap to keep the
// content.ground-flavored message so operators can tell which
// pack the model output came from.
func parseClaimPlan(raw string) (claimPlan, *packs.PackError) {
	var p claimPlan
	if perr := DecodeStructuredResponse(raw, "claim extractor", &p); perr != nil {
		// Preserve the caller-visible snippet preview so trace
		// diagnostics keep the same shape they had before
		// consolidation.
		snippet := raw
		if len(snippet) > 256 {
			snippet = snippet[:256] + "…"
		}
		return claimPlan{}, &packs.PackError{
			Code:    packs.CodeHandlerFailed,
			Message: fmt.Sprintf("claim extractor returned unparseable JSON: %s", snippet),
			Cause:   perr.Cause,
		}
	}
	return p, nil
}

// firstUsableSource returns the first search result with a
// non-empty URL, or nil if none. We deliberately ignore empty-
// description and missing-markdown results here — for grounding
// the URL alone is enough, and restricting further would skip
// otherwise-fine sources for no reason.
func firstUsableSource(items []firecrawlSearchItem) *firecrawlSearchItem {
	for i := range items {
		if items[i].URL != "" {
			return &items[i]
		}
	}
	return nil
}

// findClaimSpan locates `claim` inside `doc` with whitespace-tolerant
// matching and returns the byte span [start, end) on hit, plus a
// boolean indicating whether a hit was found.
//
// Two-stage match:
//
//  1. Exact substring (`strings.Index`). Preserves identical behavior
//     for the 95% case where the LLM emits text byte-identical to the
//     source. No fuzziness; no risk of changing established patterns.
//  2. Whitespace-tolerant scan. Walks `doc` looking for a position
//     where `claim` matches with any run of `\s+` in either side
//     treated as equivalent to any run of `\s+` in the other. Closes
//     the "the LLM normalized double-space to single-space" failure
//     mode the audit identified — a class of valid claims that the
//     strict matcher silently dropped as "hallucinations."
//
// Intentionally NOT done in v1 of this helper: smart-quote / em-dash /
// ellipsis folding, lowercase matching, Levenshtein distance.
// Whitespace is by far the most common normalization the extractor
// LLM applies; adding more lenient matching widens the false-positive
// surface (matching against the wrong span in the document) without
// closing meaningfully more dropped-claim cases. Revisit if empirical
// evidence shows other normalization classes recur.
func findClaimSpan(doc, claim string) (int, int, bool) {
	if i := strings.Index(doc, claim); i >= 0 {
		return i, i + len(claim), true
	}
	if len(claim) == 0 || len(doc) == 0 {
		return 0, 0, false
	}
	docB := []byte(doc)
	claimB := []byte(claim)
	for start := 0; start <= len(docB); start++ {
		if matchEnd, ok := matchWhitespaceTolerant(docB, start, claimB); ok {
			return start, matchEnd, true
		}
	}
	return 0, 0, false
}

// matchWhitespaceTolerant returns the doc byte-offset just past the
// end of a successful match starting at `docStart`, or (0, false) on
// miss. Treats any whitespace byte in either side as compatible with
// any whitespace byte in the other.
func matchWhitespaceTolerant(doc []byte, docStart int, claim []byte) (int, bool) {
	i, j := docStart, 0
	for j < len(claim) {
		if i >= len(doc) {
			return 0, false
		}
		if isASCIIWhitespace(claim[j]) {
			if !isASCIIWhitespace(doc[i]) {
				return 0, false
			}
			// Skip ALL contiguous whitespace on both sides.
			for j < len(claim) && isASCIIWhitespace(claim[j]) {
				j++
			}
			for i < len(doc) && isASCIIWhitespace(doc[i]) {
				i++
			}
			continue
		}
		if doc[i] != claim[j] {
			return 0, false
		}
		i++
		j++
	}
	return i, true
}

func isASCIIWhitespace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

// verifyBestSource asks the LLM to pick the best source that
// actually supports a claim, returning the chosen source and a
// short supporting snippet. This prevents inserting citations
// where the search title looks relevant but the page content
// doesn't actually back the claim.
//
// Returns nil if no source is verified as supporting the claim.
func verifyBestSource(ctx context.Context, d vision.Dispatcher, model, claim string, items []firecrawlSearchItem) (*firecrawlSearchItem, string) {
	// Build a compact source list for the LLM. Truncate each
	// source's markdown to 500 chars — enough for the LLM to
	// judge relevance without blowing up token usage.
	var sourcesMsg strings.Builder
	validCount := 0
	for i, item := range items {
		if item.URL == "" {
			continue
		}
		validCount++
		content := item.Markdown
		if content == "" {
			content = item.Description
		}
		if len(content) > 500 {
			content = content[:500] + "..."
		}
		title := item.Title
		if title == "" {
			title = item.Metadata.Title
		}
		fmt.Fprintf(&sourcesMsg, "SOURCE %d: %s\nTitle: %s\nContent: %s\n\n", i, item.URL, title, content)
	}
	if validCount == 0 {
		return nil, ""
	}

	maxTokens := 256
	req := gateway.ChatRequest{
		Model:     model,
		MaxTokens: &maxTokens,
		Messages: []gateway.Message{
			{Role: "system", Content: gateway.TextContent(
				`You verify whether web sources support a factual claim. Given a CLAIM and numbered SOURCES, respond with ONE JSON object:

{"pick": 0, "snippet": "exact quote or close paraphrase from the source that supports the claim"}

Rules:
- "pick" is the SOURCE number (0-indexed) that BEST supports the claim. Set to -1 if NO source supports it.
- "snippet" is a 1-2 sentence excerpt from the chosen source proving it backs the claim. Empty if pick is -1.
- Do not wrap in markdown. One JSON object only.`)},
			{Role: "user", Content: gateway.TextContent(
				fmt.Sprintf("CLAIM: %s\n\n%s\nWhich source best supports this claim?", claim, sourcesMsg.String()))},
		},
	}
	resp, err := d.Dispatch(ctx, req)
	if err != nil || len(resp.Choices) == 0 {
		// Verification failed — fall back to first usable source
		// rather than skipping entirely. Better to cite without
		// verification than to drop a valid grounding because the
		// LLM had a transient error.
		pick := firstUsableSource(items)
		return pick, ""
	}

	raw := strings.TrimSpace(resp.Choices[0].Message.Content.Text())
	var result struct {
		Pick    int    `json:"pick"`
		Snippet string `json:"snippet"`
	}
	result.Pick = -1
	// ADR 051 migration: this verifier was the last content.ground
	// caller still using the legacy extractFirstJSONObject fallback;
	// the extractor (parseClaimPlan) moved to DecodeStructuredResponse
	// earlier. Behavior preserved: if the parse fails entirely (no
	// JSON object in the response, all markdown-fence/preamble
	// fallbacks exhausted), result.Pick stays -1 and the bounds check
	// below returns (nil, "") — which the handler treats as "no
	// source" and skips the claim rather than citing a guess.
	_ = DecodeStructuredResponse(raw, "content.ground verifier", &result)

	if result.Pick < 0 || result.Pick >= len(items) || items[result.Pick].URL == "" {
		return nil, ""
	}
	return &items[result.Pick], result.Snippet
}

// contentGroundDefaultPersona is the fallback when no persona is set on
// input; empty/unknown personas resolve via resolveContentGroundPersona.
const contentGroundDefaultPersona = "general"

// contentGroundPersonas maps a closed-set persona key to a tone/register
// directive injected into the rewrite system prompt. Same vocabulary as
// blog.rewrite_for_audience and slides.outline so operators only have to
// learn the picker once. Directives are content.ground-specific: they
// tune register WITHOUT licensing structural change (the rewrite must
// still preserve every slide separator and the overall flow).
var contentGroundPersonas = map[string]string{
	"general":     "Tone: conversational, accessible. Light editorial polish only — preserve the source's structure and pacing.",
	"technical":   "Tone: precise, hands-on. Keep specific terms, file paths, function signatures, and config snippets from the source. Don't soften jargon when it's load-bearing.",
	"marketing":   "Tone: benefits-led, scannable. Shorter sentences. Lead each grounded claim with the outcome it produces, not the mechanism.",
	"executive":   "Tone: brief, impact-led. Use numbers where the sources support them. Strip implementation detail; keep what affects a decision.",
	"educational": "Tone: step-by-step, beginner-friendly. Define each term inline the first time it appears. Build context before claims.",
	"academic":    "Tone: formal register, hedged language (\"appears to\", \"suggests\"). Third person mostly. Preserve qualifications and counter-examples from the sources.",
}

// resolveContentGroundPersona returns the directive to inject into the
// rewrite system prompt and the canonical key to echo on output. Empty →
// the default; a known key (case-insensitive) → its directive; an unknown
// non-empty string → a freeform tone hint.
func resolveContentGroundPersona(p string) (directive, used string) {
	key := strings.ToLower(strings.TrimSpace(p))
	if key == "" {
		key = contentGroundDefaultPersona
	}
	if d, ok := contentGroundPersonas[key]; ok {
		return d, key
	}
	trimmed := strings.TrimSpace(p)
	return "Tone: tailor register and emphasis for this audience: " + trimmed, trimmed
}

// errRewriteTruncated signals that the rewrite response hit the
// completion-token ceiling and is missing the tail of the document.
// The caller treats it like any rewrite failure: keep the
// citation-only version, which preserves all content (#deck-truncation).
var errRewriteTruncated = errors.New("rewrite truncated at token ceiling; keeping citation-only version")

// estimatedTokens is a coarse token estimate (~4 chars/token) used only
// to size completion budgets. It is deliberately rough — over-budgeting
// a little is cheap, under-budgeting truncates output.
func estimatedTokens(s string) int { return len(s) / 4 }

// rewriteWithSources asks the LLM to improve the grounded text by
// rewriting weak claims into stronger prose backed by what the
// sources actually say. The LLM sees the full text (with [source]
// links already inserted) plus the grounding report (claim →
// snippet pairs) and produces a polished version. personaDirective
// tunes register/tone (e.g. "Tone: hands-on, precise…") so the
// rewrite isn't always formal-academic.
func rewriteWithSources(ctx context.Context, d vision.Dispatcher, model, text string, gs []grounding, personaDirective string) (string, error) {
	// Budget the rewrite's output to fit the document. The rewrite
	// returns the WHOLE text (rewritten), so the old fixed 2048-token
	// cap silently truncated long inputs — a 20-25 slide deck blew
	// past it and every slide after the cap was dropped. Scale to the
	// input size (~4 chars/token) with 25% headroom for the citation
	// links the rewrite weaves in, clamped to
	// [defaultContentGroundTokens, maxContentGroundTokens]. Inputs that
	// would need more than the ceiling fall back to the citation-only
	// version via the FinishReason=="length" guard below.
	maxTokens := estimatedTokens(text) * 5 / 4
	if maxTokens < defaultContentGroundTokens {
		maxTokens = defaultContentGroundTokens
	}
	if maxTokens > maxContentGroundTokens {
		maxTokens = maxContentGroundTokens
	}

	var sourcesMsg strings.Builder
	sourcesMsg.WriteString("GROUNDING REPORT:\n\n")
	for i, g := range gs {
		fmt.Fprintf(&sourcesMsg, "Claim %d: %q\n", i+1, g.Claim)
		fmt.Fprintf(&sourcesMsg, "Source: %s\n", g.URL)
		if g.Snippet != "" {
			fmt.Fprintf(&sourcesMsg, "Source excerpt: %s\n", g.Snippet)
		}
		sourcesMsg.WriteString("\n")
	}

	systemPrompt := `You are an expert editor who improves factual writing by making claims more specific and authoritative using source material.

You will receive:
1. ORIGINAL TEXT — markdown with [source](url) citation links already inserted
2. GROUNDING REPORT — for each cited claim, the source URL and an excerpt from that source

Your job: rewrite the text so that:
- Weak or vague claims become specific and authoritative, drawing on the source excerpts
- Every rewritten claim naturally integrates its citation as an inline [source](url) link
- Claims that were NOT grounded (no [source] link) are left unchanged
- The overall structure and flow of the original are preserved
- Do NOT add new claims or information not supported by the sources
- Do NOT remove any content — only improve what has a source backing it
- Keep the same markdown format
- If the text is a slide deck (Marp/markdown using "---" as slide separators), preserve EVERY "---" separator and keep the exact same number of slides — never merge, split, drop, or reorder slides, and keep each slide's heading

` + personaDirective + `

Return ONLY the rewritten markdown text. No commentary, no explanation, no code fences.`
	req := gateway.ChatRequest{
		Model:     model,
		MaxTokens: &maxTokens,
		Messages: []gateway.Message{
			{Role: "system", Content: gateway.TextContent(systemPrompt)},
			{Role: "user", Content: gateway.TextContent(
				fmt.Sprintf("ORIGINAL TEXT:\n%s\n\n%s\nRewrite the text, improving grounded claims using the source excerpts.", text, sourcesMsg.String()))},
		},
	}

	resp, err := d.Dispatch(ctx, req)
	if err != nil {
		return "", fmt.Errorf("rewrite dispatch: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("rewrite: no choices returned")
	}
	// If the model hit the token ceiling, the tail of the document is
	// missing — shipping it would silently drop content (e.g. the last
	// slides of a deck). Discard the truncated rewrite; the caller
	// keeps the structure-preserving citation-only version.
	if resp.Choices[0].FinishReason == "length" {
		return "", errRewriteTruncated
	}
	result := strings.TrimSpace(resp.Choices[0].Message.Content.Text())

	// Strip markdown code fences if the model wrapped it
	if strings.HasPrefix(result, "```") {
		lines := strings.Split(result, "\n")
		if len(lines) > 2 {
			// Remove first and last lines (the fences)
			start := 1
			end := len(lines) - 1
			if strings.TrimSpace(lines[end]) == "```" {
				result = strings.Join(lines[start:end], "\n")
			}
		}
	}
	return result, nil
}
