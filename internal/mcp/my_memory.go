// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package mcp

// my_memory.go — helmdeck://my-memory MCP resource (ADR 048 PR #2).
//
// Surfaces the caller's user-written fact categories so the chat
// agent can discover what's already stored BEFORE re-asking the user
// or duplicating a fact. Returns counts and recent keys per category;
// the actual fact values aren't echoed back here — that's the
// helmdeck.memory_recall pack's job (out of scope for PR #2; lives in
// PR #3 alongside the corpus bridge).
//
// Wire shape:
//   {
//     "scope":       "caller=<id>",
//     "fetched_at":  "...",
//     "categories":  [
//        { "name": "user_facts", "count": 3, "recent_keys": ["preferences/frontend-framework", ...] },
//        { "name": "project_conventions", "count": 1, "recent_keys": [...] }
//     ],
//     "note":        "..."  // when empty or memory disabled
//   }
//
// The agent reads this resource at the top of a session (or before
// asking the user for inputs that look durable) to honor existing
// facts. helmdeck.memory_forget covers the cleanup half of the
// surface; helmdeck.memory_store (this PR) covers the write half.

import (
	"context"
	"sort"
	"time"

	"github.com/tosin2013/helmdeck/internal/memory"
	"github.com/tosin2013/helmdeck/internal/packs"
)

// myMemoryRecentCap bounds how many recent keys per category the
// projection surfaces. Keeps the resource compact; agents needing
// the full set can list via the (future) memory_recall pack.
const myMemoryRecentCap = 5

// MyMemory is the wire shape of the projection.
type MyMemory struct {
	Scope      string             `json:"scope"`
	FetchedAt  string             `json:"fetched_at"`
	Categories []MyMemoryCategory `json:"categories"`
	Note       string             `json:"note,omitempty"`
}

// MyMemoryCategory groups the caller's facts by category name with a
// count and a recent-keys peek.
type MyMemoryCategory struct {
	Name       string   `json:"name"`
	Count      int      `json:"count"`
	RecentKeys []string `json:"recent_keys,omitempty"`
}

func (s *PackServer) buildMyMemory(ctx context.Context, caller string) (MyMemory, *rpcError) {
	out := MyMemory{
		Scope:      "caller=" + caller,
		FetchedAt:  time.Now().UTC().Format(time.RFC3339),
		Categories: []MyMemoryCategory{},
	}
	var store memory.MemoryStore
	if s.engine != nil {
		store = s.engine.MemoryStore()
	}
	if store == nil {
		out.Note = "memory store not configured; no user facts available"
		return out, nil
	}

	// Read ALL keys under the caller's bare namespace (empty prefix)
	// then group by Category. Audit categories (pack_history /
	// pipeline_history) are filtered out — the agent already gets
	// those via helmdeck://my-defaults; this resource is about
	// agent-written facts only.
	entries, err := store.List(ctx, caller, "")
	if err != nil {
		return out, &rpcError{Code: -32603, Message: "my-memory: list: " + err.Error()}
	}
	byCategory := map[string][]memory.Entry{}
	for _, e := range entries {
		if e.Category == "" || packs.IsReservedFactCategory(e.Category) {
			continue
		}
		byCategory[e.Category] = append(byCategory[e.Category], e)
	}
	if len(byCategory) == 0 {
		out.Note = "no user facts stored yet; agents can persist durable preferences via helmdeck.memory_store"
		return out, nil
	}
	for name, group := range byCategory {
		// Sort by UpdatedAt desc so RecentKeys reads "most-recent first".
		sort.SliceStable(group, func(i, j int) bool {
			return group[i].UpdatedAt.After(group[j].UpdatedAt)
		})
		recent := make([]string, 0, myMemoryRecentCap)
		for i, e := range group {
			if i >= myMemoryRecentCap {
				break
			}
			recent = append(recent, e.Key)
		}
		out.Categories = append(out.Categories, MyMemoryCategory{
			Name:       name,
			Count:      len(group),
			RecentKeys: recent,
		})
	}
	// Stable category order so successive reads don't churn the wire.
	sort.SliceStable(out.Categories, func(i, j int) bool {
		return out.Categories[i].Name < out.Categories[j].Name
	})
	return out, nil
}
