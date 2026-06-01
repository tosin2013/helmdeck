// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package llmcontext

// filter.go — two-pass LLM-filter cascade helpers (ADR 050 PR #4).
//
// PR #3's lexical retrieval handles most cases — keyword overlap is
// reliable when the user's intent uses the same vocabulary the
// catalog metadata uses. But when the intent is dominated by a long
// user paste (the original MiniMax M3 launch failure mode) or uses
// semantic phrasing that doesn't lexically overlap with catalog
// fields, lexical retrieval returns ambiguous scores and the wrong
// tools survive truncation.
//
// PR #4 adds a small LLM call before the real planning call: feed
// the candidate tool list + the intent to the model and let it pick
// which ids are relevant. The filter prompt is tiny (just tool names
// + one-line descriptions, no full metadata), so even weak models
// produce a usable structured response. The planning call then sees
// a catalog of only the picked ids — small input, room for the
// model's structured output to land.
//
// Architecture choice: filter.go produces the prompt strings and
// parses the response. Pack handlers (plan.go, route.go) own the
// actual gateway.Dispatcher dispatch — this keeps llmcontext free of
// gateway deps and centralizes the LLM-call lifecycle in one place.
// The hook surface is three helpers: BuildFilterUserMessage,
// ParseFilterResponse, RestrictCatalog.

import (
	"sort"
	"strings"
)

// FilterSystemPrompt is the system message for the filter pass. We
// keep it explicit about the response format because weak models will
// otherwise narrate their picks ("I think the relevant ones are...").
// One JSON object, one key, an array of strings — easy to parse.
const FilterSystemPrompt = `You are the tool-filter assistant for helmdeck.

The user wants to accomplish something. You will receive:
  1. A list of available tool ids with one-line descriptions.
  2. The user's intent.

Your ONE job: pick the ids that are relevant to the intent. Return ONLY a JSON object with this exact shape:

{"ids": ["tool.id.one", "tool.id.two", ...]}

Rules:
- Include every id that could plausibly be part of the plan, even if you're not 100% sure.
- Exclude ids that are clearly unrelated.
- Pick between 3 and 12 ids. If the intent maps to fewer, still include adjacent helpers; if it maps to more, pick the top 12 by relevance.
- NEVER invent an id that doesn't appear in the provided list.
- Return ONLY the JSON object, no prose, no code fences.`

// BuildFilterUserMessage assembles the filter-pass user message. The
// shape is "ids + one-line descriptions, then the intent." Total
// size for the current helmdeck catalog (~70 entries) is roughly
// 2–3 KB — well within any free model's working set for a small
// structured-output task.
func BuildFilterUserMessage(rg RoutingGuide, intent string) string {
	var b strings.Builder
	b.WriteString("AVAILABLE TOOLS:\n")
	for _, p := range rg.Packs {
		b.WriteString("- ")
		b.WriteString(p.Name)
		if d := firstSentence(p.Description); d != "" {
			b.WriteString(" — ")
			b.WriteString(d)
		}
		b.WriteString("\n")
	}
	for _, p := range rg.Pipelines {
		b.WriteString("- ")
		b.WriteString(p.ID)
		if d := firstSentence(p.Description); d != "" {
			b.WriteString(" — ")
			b.WriteString(d)
		}
		b.WriteString("\n")
	}
	b.WriteString("\nINTENT:\n")
	b.WriteString(strings.TrimSpace(intent))
	b.WriteString("\n\nReturn the JSON object now.")
	return b.String()
}

// ParseFilterResponse extracts the id list from the filter model's
// completion. Tolerates code-fenced JSON, leading/trailing prose,
// and partial responses (returns whatever it could parse). Returns
// the empty slice when the response can't be decoded — callers fall
// back to the unfiltered catalog in that case.
func ParseFilterResponse(text string) []string {
	body := strings.TrimSpace(text)
	if body == "" {
		return nil
	}
	body = stripCodeFence(body)
	// The response may have prose before/after the JSON object —
	// scan for the first {...} pair.
	start := strings.Index(body, "{")
	end := strings.LastIndex(body, "}")
	if start < 0 || end <= start {
		return nil
	}
	body = body[start : end+1]
	var probe struct {
		IDs []string `json:"ids"`
	}
	if err := jsonUnmarshal([]byte(body), &probe); err != nil {
		return nil
	}
	// Deduplicate while preserving first-seen order. Same id can
	// appear twice if a weak model gets repetitive.
	seen := map[string]struct{}{}
	out := make([]string, 0, len(probe.IDs))
	for _, id := range probe.IDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// RestrictCatalog returns a RoutingGuide containing only the entries
// whose id appears in keep. Unknown ids are ignored — the filter pass
// model is allowed to hallucinate and we discard those silently. The
// returned guide preserves the original ordering of packs / pipelines
// within their respective slices.
func RestrictCatalog(rg RoutingGuide, keep []string) RoutingGuide {
	if len(keep) == 0 {
		return RoutingGuide{Packs: []Pack{}, Pipelines: []Pipeline{}}
	}
	keepSet := make(map[string]struct{}, len(keep))
	for _, id := range keep {
		keepSet[id] = struct{}{}
	}
	out := RoutingGuide{
		Packs:     make([]Pack, 0, len(rg.Packs)),
		Pipelines: make([]Pipeline, 0, len(rg.Pipelines)),
	}
	for _, p := range rg.Packs {
		if _, ok := keepSet[p.Name]; ok {
			out.Packs = append(out.Packs, p)
		}
	}
	for _, p := range rg.Pipelines {
		if _, ok := keepSet[p.ID]; ok {
			out.Pipelines = append(out.Pipelines, p)
		}
	}
	return out
}

// stripCodeFence removes ```json fenced wrappers if the model
// emitted them despite our system-prompt instruction not to. Weak
// models often disregard "no code fences" — be defensive.
func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		// Drop the first line ("```" or "```json") and the trailing fence.
		nl := strings.IndexByte(s, '\n')
		if nl > 0 {
			s = s[nl+1:]
		}
		if i := strings.LastIndex(s, "```"); i >= 0 {
			s = s[:i]
		}
	}
	return strings.TrimSpace(s)
}

// ShouldEscalateToFilter reports whether the lexical-pass result is
// ambiguous enough that callers should escalate to the LLM filter.
// Combines two signals: low HighConfidence (top scores too close)
// AND a non-trivial catalog size (no point escalating when only 2
// tools survived anyway).
//
// Threshold of 0.4 was picked by inspecting the score gaps the live
// test produced: confident picks land at >0.6 gap; the ambiguous
// "complex paste" case had gaps under 0.3. The middle band is where
// the filter pass adds the most value.
func ShouldEscalateToFilter(ranked []Scored, minEntries int) bool {
	if len(ranked) < minEntries {
		return false
	}
	return !HighConfidence(ranked, 0.4)
}

// MergeKeepOrder returns the union of two id slices preserving the
// order of first occurrence. Used by the cascade orchestrator to
// combine lexical top-N (must-keep) with LLM filter picks (relevant)
// without duplicates or order loss. Exported for plan.go / route.go.
func MergeKeepOrder(primary, secondary []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(primary)+len(secondary))
	for _, id := range primary {
		if _, dup := seen[id]; !dup {
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	for _, id := range secondary {
		if _, dup := seen[id]; !dup {
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}

// idsFromRoutingGuide extracts every dispatch identifier from a
// RoutingGuide. Used by plan/route to construct the "must-keep"
// list (e.g. the lexical top-N) when computing the filter overlay.
// Exported for the pack handlers; internal helpers in this package
// use a tighter loop.
func IDsFromRoutingGuide(rg RoutingGuide) []string {
	out := make([]string, 0, len(rg.Packs)+len(rg.Pipelines))
	for _, p := range rg.Packs {
		out = append(out, p.Name)
	}
	for _, p := range rg.Pipelines {
		out = append(out, p.ID)
	}
	// Stable order so concurrent identical inputs produce identical
	// filter-prompt strings (useful when investigating empty-
	// completion failures: the prompt should be reproducible).
	sort.Strings(out)
	return out
}
