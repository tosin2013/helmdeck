// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package llmcontext

// rank.go — lexical relevance scoring for catalog entries (ADR 050
// PR #3 stage 2 of the Select cascade).
//
// When CompactCatalog's metadata-trim ladder can't bring the catalog
// under the model's budget — typically because the irreducible floor
// of names + ids + supersedes is already too large for the worst Tier
// C models — Select escalates to lexical retrieval: score every entry
// against the user's intent, keep the top-N. This is the no-extra-
// LLM-call path; PR #4 may add a second-pass LLM filter on top when
// lexical confidence is low.
//
// Scoring is deliberately simple — TF-style keyword overlap weighted
// by which catalog field the match came from. Stop words filtered,
// case insensitive. Deterministic order on ties (by entry name) so a
// re-run on identical inputs produces the identical selection.
//
// Why lexical, not dense embeddings: helmdeck's catalog already
// carries the right anchor signals — `intent_keywords[]`, `accepts[]`,
// `produces[]` are domain-shaped tokens the user is likely to use
// verbatim. Embeddings would buy semantic generalization at the cost
// of an inference dependency. Reconsider when measured lexical recall
// drops below useful thresholds; track via the `helmdeck://my-plans`
// projection (PR #3 also ships this).

import (
	"encoding/json"
	"sort"
	"strings"
)

// jsonUnmarshal / jsonMarshal are thin aliases so internal helpers
// read naturally without dragging encoding/json into every call
// site. Same semantics as the standard library entry points.
var (
	jsonUnmarshal = json.Unmarshal
	jsonMarshal   = json.Marshal
)

// Scored is one catalog entry annotated with its relevance score.
// Either Pack OR Pipeline is set (mutually exclusive); the kind field
// makes that explicit so callers don't have to nil-check both. ID is
// the entry's dispatch identifier (pack name for Pack, pipeline id
// for Pipeline) — convenient handle for tie-breaking and logging.
type Scored struct {
	Kind     string // "pack" | "pipeline"
	ID       string
	Score    float64
	Pack     *Pack     // nil when Kind == "pipeline"
	Pipeline *Pipeline // nil when Kind == "pack"
}

// stopWords drops tokens with no discriminating signal in helmdeck's
// catalog. Keep this set small — every entry adds opportunities to
// drop a meaningful term. Calibrated for the catalog's tone, which
// leans on imperative verbs and domain nouns.
var stopWords = map[string]struct{}{
	"a": {}, "an": {}, "the": {}, "this": {}, "that": {}, "these": {}, "those": {},
	"of": {}, "in": {}, "to": {}, "for": {}, "and": {}, "with": {}, "on": {},
	"is": {}, "are": {}, "was": {}, "be": {}, "by": {}, "from": {}, "as": {},
	"it": {}, "its": {}, "or": {}, "if": {}, "i": {}, "me": {}, "my": {},
	"do": {}, "does": {}, "want": {}, "need": {}, "can": {}, "should": {}, "would": {},
}

// Field weights — bigger means a match in that field contributes more
// to the score. Tuned for helmdeck's catalog shape: intent_keywords
// is the strongest signal (curator-supplied, intentionally selected
// for routing), accepts/produces is medium (domain match), name is
// medium (the dispatch identifier the user often types verbatim),
// description is weak (longer, noisier). Pipeline supersedes gets a
// boost so when the user mentions a pack by name, the superseding
// pipeline outranks the bare pack.
const (
	weightIntentKeyword   = 3.0
	weightAcceptsProduces = 2.0
	weightName            = 2.0
	weightDescription     = 1.0
	weightSupersedes      = 2.5 // pipeline only
)

// LexicalRank scores every catalog entry against the intent and
// returns them sorted by Score descending, then by ID ascending for
// deterministic ties. Score-zero entries are kept in the result
// (callers like Select decide what to drop) so the contract is "rank,
// don't filter" — composable with separate TopK truncation.
func LexicalRank(rg RoutingGuide, intent string) []Scored {
	intentTokens := tokenize(intent)
	out := make([]Scored, 0, len(rg.Packs)+len(rg.Pipelines))

	for i := range rg.Packs {
		p := &rg.Packs[i]
		score := scorePack(p, intentTokens)
		out = append(out, Scored{Kind: "pack", ID: p.Name, Score: score, Pack: p})
	}
	for i := range rg.Pipelines {
		p := &rg.Pipelines[i]
		score := scorePipeline(p, intentTokens)
		out = append(out, Scored{Kind: "pipeline", ID: p.ID, Score: score, Pipeline: p})
	}

	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Score != out[b].Score {
			return out[a].Score > out[b].Score
		}
		return out[a].ID < out[b].ID
	})
	return out
}

// TopK truncates a ranked slice to k entries. k <= 0 keeps the full
// list (callers may want to inspect scores without truncating). k >=
// len(ranked) is a no-op.
func TopK(ranked []Scored, k int) []Scored {
	if k <= 0 || k >= len(ranked) {
		return ranked
	}
	return ranked[:k]
}

// HighConfidence reports whether the top-ranked entry is meaningfully
// ahead of its closest rival. Used by Select to decide whether to
// short-circuit further escalation (e.g., skip the eventual PR #4
// LLM-filter pass) when lexical retrieval is unambiguous. Threshold
// is the score-gap ratio between rank 0 and rank 1 — 0.5 means the
// top score must be at least 1.5x the second.
func HighConfidence(ranked []Scored, threshold float64) bool {
	if len(ranked) < 2 {
		// One entry total: confidently use it. Zero entries: caller
		// has a bigger problem than confidence detection.
		return true
	}
	top := ranked[0].Score
	if top == 0 {
		return false
	}
	second := ranked[1].Score
	if second == 0 {
		return true
	}
	return (top-second)/top >= threshold
}

// scorePack computes a pack's relevance score against the tokenized
// intent. Returns 0 when no field matches; the score is the weighted
// sum across all matched fields.
func scorePack(p *Pack, intentTokens map[string]int) float64 {
	score := 0.0
	if len(intentTokens) == 0 {
		return 0
	}
	// intent_keywords: each phrase that matches a window in the
	// intent gets weightIntentKeyword. The phrase signal beats
	// single-token overlap because curators write keywords as
	// disambiguating phrases ("rewrite for audience" vs "rewrite").
	for _, kw := range p.Metadata.IntentKeywords {
		if phraseOverlap(intentTokens, kw) {
			score += weightIntentKeyword
		}
	}
	// accepts + produces: each matching token contributes once.
	for _, a := range p.Metadata.Accepts {
		score += weightAcceptsProduces * tokenSetOverlap(intentTokens, a)
	}
	for _, prod := range p.Metadata.Produces {
		score += weightAcceptsProduces * tokenSetOverlap(intentTokens, prod)
	}
	// Name: the dispatched identifier. Users often type pack names
	// verbatim ("use blog.rewrite_for_audience"), so a name token
	// match is high signal.
	score += weightName * tokenSetOverlap(intentTokens, p.Name)
	// Description: weakest signal. The first sentence carries most
	// of the meaning; rest is often boilerplate. We score the whole
	// description but with the lowest weight, accepting some noise.
	score += weightDescription * tokenSetOverlap(intentTokens, p.Description)
	return score
}

// scorePipeline mirrors scorePack but on pipeline metadata, which is
// json.RawMessage. We parse just enough to extract the same anchor
// fields plus `supersedes` — when the user mentions a pack by name
// and a pipeline supersedes that pack, the pipeline should outrank
// the pack (mirrors helmdeck.route's rule R2 and helmdeck.plan's rule
// P2).
func scorePipeline(p *Pipeline, intentTokens map[string]int) float64 {
	score := 0.0
	if len(intentTokens) == 0 {
		return 0
	}
	// Pipeline metadata is opaque JSON; we shallow-parse to grab the
	// fields the scorer needs without dragging the full pipelines
	// type into this package.
	meta := parsePipelineMetadata(p.Metadata)
	for _, kw := range meta.IntentKeywords {
		if phraseOverlap(intentTokens, kw) {
			score += weightIntentKeyword
		}
	}
	for _, a := range meta.Accepts {
		score += weightAcceptsProduces * tokenSetOverlap(intentTokens, a)
	}
	for _, prod := range meta.Produces {
		score += weightAcceptsProduces * tokenSetOverlap(intentTokens, prod)
	}
	// Supersedes: when a token from a superseded pack's name appears
	// in the intent, boost this pipeline. Implements the supersedes-
	// honoring policy at the ranking layer (rule P2 / R2).
	for _, sup := range meta.Supersedes {
		score += weightSupersedes * tokenSetOverlap(intentTokens, sup)
	}
	score += weightName * tokenSetOverlap(intentTokens, p.Name)
	score += weightName * tokenSetOverlap(intentTokens, p.ID)
	score += weightDescription * tokenSetOverlap(intentTokens, p.Description)
	return score
}

// pipelineMetaProbe is the shallow shape we need from pipeline
// metadata for scoring. Untyped JSON fields are tolerated — missing
// keys return zero values.
type pipelineMetaProbe struct {
	Accepts        []string `json:"accepts"`
	Produces       []string `json:"produces"`
	IntentKeywords []string `json:"intent_keywords"`
	Supersedes     []string `json:"supersedes"`
}

func parsePipelineMetadata(raw []byte) pipelineMetaProbe {
	var out pipelineMetaProbe
	if len(raw) == 0 {
		return out
	}
	// Use the standard library decoder directly; importing
	// encoding/json at module scope keeps this file self-contained.
	_ = jsonUnmarshal(raw, &out)
	return out
}

// tokenize lowercases the input and splits on word boundaries,
// dropping stop words. Returns a multiset (map[token]count) so
// downstream scoring can be O(intent_tokens) instead of O(intent_text
// × catalog_entry_text).
func tokenize(s string) map[string]int {
	out := map[string]int{}
	if s == "" {
		return out
	}
	// Split on any non-letter / non-digit; emit lowercase tokens.
	var b strings.Builder
	emit := func() {
		if b.Len() == 0 {
			return
		}
		t := b.String()
		b.Reset()
		if _, drop := stopWords[t]; drop {
			return
		}
		if len(t) < 2 {
			// Single-character tokens are too noisy to score.
			return
		}
		out[t]++
	}
	for _, r := range s {
		if isWordRune(r) {
			b.WriteRune(toLower(r))
		} else {
			emit()
		}
	}
	emit()
	return out
}

// tokenSetOverlap counts how many distinct tokens in field appear in
// the intent's token multiset. We use distinct count (not weighted by
// intent frequency) so a long intent doesn't artificially boost a
// catalog entry just because the user repeated a token. Returns 0.0
// when no overlap; ≥1.0 when ≥1 token matches.
func tokenSetOverlap(intentTokens map[string]int, field string) float64 {
	if field == "" || len(intentTokens) == 0 {
		return 0
	}
	fieldTokens := tokenize(field)
	if len(fieldTokens) == 0 {
		return 0
	}
	hits := 0.0
	for t := range fieldTokens {
		if _, ok := intentTokens[t]; ok {
			hits++
		}
	}
	return hits
}

// phraseOverlap returns true when every token of phrase appears in
// the intent's token set. Used for intent_keywords — multi-word
// keywords like "rewrite for audience" must match all tokens to
// count, not just one. Matches don't have to be contiguous (the
// intent's wording will vary), but the keyword's whole token set
// must be present.
func phraseOverlap(intentTokens map[string]int, phrase string) bool {
	phraseTokens := tokenize(phrase)
	if len(phraseTokens) == 0 {
		return false
	}
	for t := range phraseTokens {
		if _, ok := intentTokens[t]; !ok {
			return false
		}
	}
	return true
}

func isWordRune(r rune) bool {
	return (r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9')
}

func toLower(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r + 32
	}
	return r
}
