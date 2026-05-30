// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package mcp

// routing_guide.go — helmdeck://routing-guide structured catalog (ADR 047).
//
// The chat agent should query this resource FIRST for any multi-step
// request. It returns:
//   - policy: text the agent reads as system-prompt context — how to use
//     the catalog, prefer-pipelines-over-packs, treat Limitations as gap
//     analysis seeds.
//   - packs[]: every registered pack's name, description, and PackMetadata.
//     Empty metadata is fine — it just means the pack hasn't been
//     populated yet and won't show up in routing decisions.
//   - pipelines[]: every pipeline's id, name, description, and
//     PipelineMetadata. Same empty-is-fine rule.
//
// SKILL.md remains a fallback for offline reads, but this resource is
// canonical going forward.

import (
	"context"
	"encoding/json"
	"time"
)

// RoutingGuide is the JSON shape we serialize at helmdeck://routing-guide.
// Top-level Policy is system-prompt text; Packs / Pipelines are the
// catalog the routing pack (ADR 047 PR #3) consumes.
type RoutingGuide struct {
	Policy    string                  `json:"policy"`
	FetchedAt string                  `json:"fetched_at"`
	Packs     []routingGuidePackEntry `json:"packs"`
	Pipelines []routingGuidePipeEntry `json:"pipelines"`
}

// routingGuidePackEntry is the per-pack catalog row. Mirrors the subset of
// packs.Pack the agent needs for routing — full input schema lives at
// helmdeck://packs (kept separate so a routing scan stays light).
type routingGuidePackEntry struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
}

// routingGuidePipeEntry is the per-pipeline catalog row. Steps live at
// helmdeck://pipelines (pipeline-list MCP tool) — kept separate so the
// routing scan is fast.
type routingGuidePipeEntry struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
}

// routingGuidePolicy is the system-prompt text emitted at the top of every
// routing-guide payload. It tells the agent how to consume the catalog.
// Versioned alongside ADR 047 — when the routing pack ships (PR #3) this
// text gains a "call helmdeck.route first" instruction.
const routingGuidePolicy = `helmdeck routing guide (ADR 047).

When a user asks for a multi-step workflow, do this:

1. Tokenize the user's intent (e.g. "make a blog post from a PDF" → {action:"blog", source:"pdf"}).
2. Scan pipelines[] first. Match by entry.metadata.accepts (does any include the user's source kind?) AND entry.metadata.produces (does any include the target format?) AND entry.metadata.intent_keywords. A pipeline that matches both axes is the right answer.
3. If a pipeline's metadata.supersedes lists packs the user mentioned by name, USE the pipeline — do NOT chain those packs by hand. The pipeline is the maintained one-call surface.
4. Only fall back to chaining packs[] if no pipeline matches. Use pack-level accepts/produces for the same scoring.
5. When NOTHING matches the user's intent, scan nearby entries' limitations[] to seed a gap proposal: "we don't have X — a new pack/pipeline that took {accepts} and produced {produces} would unblock this." A future helmdeck.route pack (ADR 047 PR #3) will return this proposal structurally; today it's a free-form observation you make to the user.
6. typical_use is a one-sentence hint the maintainer wrote — surface it to the user when explaining why you picked an entry.

Empty metadata on a catalog entry means the maintainer hasn't populated it yet; treat that entry as routable by name + description fallback only. Metadata coverage grows incrementally as packs/pipelines are touched in PRs.`

// buildRoutingGuide assembles the catalog payload. Fast path — no I/O
// beyond the pipeline list (which is in-memory in production via the
// pipeline service adapter).
func (s *PackServer) buildRoutingGuide(ctx context.Context) (RoutingGuide, *rpcError) {
	out := RoutingGuide{
		Policy:    routingGuidePolicy,
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
		Packs:     []routingGuidePackEntry{},
		Pipelines: []routingGuidePipeEntry{},
	}

	// Packs side: walk the registry, surface name/description plus the
	// PackMetadata field. Marshaling the metadata as json.RawMessage
	// keeps empty/zero values as `null` (json.Marshal of a zero
	// PackMetadata with omitempty tags produces `{}`); we then collapse
	// `{}` to nil so the wire shape stays clean.
	for _, info := range s.registry.List() {
		pack, err := s.registry.Get(info.Name, "")
		if err != nil {
			continue
		}
		meta := marshalRoutingMeta(pack.Metadata)
		out.Packs = append(out.Packs, routingGuidePackEntry{
			Name:        pack.Name,
			Description: pack.Description,
			Metadata:    meta,
		})
	}

	// Pipelines side: pull the full list from the pipeline service (the
	// adapter typed in internal/api/mcp_pipelines.go). Each entry's JSON
	// already carries metadata via the new json tag.
	if s.pipelines != nil {
		raw, err := s.pipelines.List(ctx)
		if err != nil {
			return out, &rpcError{Code: -32603, Message: "routing-guide: list pipelines: " + err.Error()}
		}
		// PipelineService.List returns the canonical pipelines JSON
		// array. Re-decode into the routing-guide pipeline-entry shape
		// (a subset).
		var fullPipes []struct {
			ID          string          `json:"id"`
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Metadata    json.RawMessage `json:"metadata,omitempty"`
		}
		if err := json.Unmarshal(raw, &fullPipes); err != nil {
			return out, &rpcError{Code: -32603, Message: "routing-guide: decode pipelines: " + err.Error()}
		}
		for _, p := range fullPipes {
			out.Pipelines = append(out.Pipelines, routingGuidePipeEntry{
				ID:          p.ID,
				Name:        p.Name,
				Description: p.Description,
				Metadata:    collapseEmptyJSON(p.Metadata),
			})
		}
	}
	return out, nil
}

// marshalRoutingMeta serializes a packs.PackMetadata-shaped value. Caller
// passes the interface-typed value (PackMetadata is concrete here but
// we use any to avoid the cyclic import internal/mcp → internal/packs
// in test fakes that fake the registry). Empty zero values come out as
// nil so the wire shape is clean.
func marshalRoutingMeta(meta any) json.RawMessage {
	b, err := json.Marshal(meta)
	if err != nil {
		return nil
	}
	return collapseEmptyJSON(b)
}

// collapseEmptyJSON returns nil for the empty-object JSON literal so
// downstream consumers don't see `{}` for unpopulated metadata. Mirrors
// `omitempty` semantics for embedded structs (which Go's encoder doesn't
// do by default).
func collapseEmptyJSON(b json.RawMessage) json.RawMessage {
	s := string(b)
	if s == "" || s == "{}" || s == "null" {
		return nil
	}
	return b
}
