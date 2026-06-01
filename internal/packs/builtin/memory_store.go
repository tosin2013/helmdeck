// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// memory_store.go — helmdeck.memory_store pack (ADR 048 PR #2).
//
// The write-half of the memory surface. PR #2 of ADR 048 turns
// helmdeck's per-caller memory layer into a place ANY MCP client can
// persist user-supplied facts — durable preferences, project
// conventions, decisions worth remembering — so future conversations
// honor them without re-asking the user.
//
// Lifecycle is symmetric with helmdeck.memory_forget (ADR 047 PR #2):
//   - Same caller-namespacing (bare caller, cross-session learning).
//   - Same category vocabulary; new facts default to "user_facts".
//   - Same TTL story; facts expire (default 90d, max 365d, min 1h).
//   - The existing helmdeck.memory_forget already accepts custom
//     categories (`scope: "key:<exact>"`), so cleanup composes for
//     free.
//
// Engine policy lives in internal/packs/facts.go (packs.StoreFact)
// so the REST endpoint and this pack call the same validator and
// can never drift on category guards or TTL clamping.

import (
	"context"
	"encoding/json"
	"time"

	"github.com/tosin2013/helmdeck/internal/packs"
)

// MemoryStore returns the helmdeck.memory_store pack. NoAudit: true
// because storing a user fact is meta-tooling — auditing the write
// would pollute the pack_history projection (the user would see
// `helmdeck.memory_store` ranked alongside their real work).
// NeedsSession: false so ec.Memory.Namespace() is the bare caller —
// facts learned in one session show up in the next.
func MemoryStore() *packs.Pack {
	return &packs.Pack{
		Name:        "helmdeck.memory_store",
		Version:     "v1",
		Description: "Persist a user-supplied fact to the caller's memory namespace (ADR 048). Use when the user shares a durable preference, project convention, or decision worth remembering across sessions. Default category `user_facts` (90-day TTL); pass `category` for richer taxonomy (`preferences`, `project_conventions`, etc). Categories `pack_history` and `pipeline_history` are reserved for engine audit writes and will reject.",
		NoAudit:     true,
		Metadata: packs.PackMetadata{
			Accepts:        []string{"user_fact"},
			Produces:       []string{"store_receipt"},
			IntentKeywords: []string{"remember this", "save this preference", "store my convention", "don't ask me again", "for next time"},
			TypicalUse:     "Call when the user shares a durable fact (\"I always deploy via Konflux\", \"prefer React over Vue\"). Confirm with the user first; surface the assigned category + TTL so they know what's being stored.",
			Limitations:    []string{"caller-scoped — facts written under one JWT subject are invisible to another", "categories pack_history / pipeline_history are reserved and rejected with invalid_input", "TTL is mandatory and bounded (min 1h, max 365d) — no permanent storage"},
		},
		InputSchema: packs.BasicSchema{
			Required: []string{"key", "value"},
			Properties: map[string]string{
				"key":         "string",
				"value":       "string",
				"category":    "string",
				"tags":        "array",
				"ttl_seconds": "number",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"key", "category", "expires_at"},
			Properties: map[string]string{
				"key":        "string",
				"category":   "string",
				"expires_at": "string",
			},
		},
		Handler: memoryStoreHandler,
	}
}

func memoryStoreHandler(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
	var in struct {
		Key        string   `json:"key"`
		Value      string   `json:"value"`
		Category   string   `json:"category"`
		Tags       []string `json:"tags"`
		TTLSeconds int64    `json:"ttl_seconds"`
	}
	if err := json.Unmarshal(ec.Input, &in); err != nil {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
	}

	normalized, opts, ferr := packs.ValidateFact(packs.StoreFactRequest{
		Key:      in.Key,
		Value:    in.Value,
		Category: in.Category,
		Tags:     in.Tags,
		TTL:      time.Duration(in.TTLSeconds) * time.Second,
	})
	if ferr != nil {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: ferr.Message, Cause: ferr}
	}

	out := map[string]any{
		"key":        normalized.Key,
		"category":   normalized.Category,
		"expires_at": time.Now().Add(normalized.TTL).UTC().Format(time.RFC3339),
	}
	if ec.Memory == nil {
		// Memory-disabled deployment — soft-success so agent code paths
		// don't have to special-case the nil-store case. Surface a note
		// so the agent can tell the user the fact wasn't actually
		// persisted (vs silently succeeding).
		out["note"] = "memory store not configured; fact accepted but not persisted"
		return json.Marshal(out)
	}
	// ec.Memory is namespace-scoped to the bare caller because the pack
	// has NeedsSession: false. Same namespace the my-defaults projection
	// reads from, so the fact surfaces in `helmdeck://my-memory` on the
	// next read.
	if err := ec.Memory.Store(normalized.Key, []byte(normalized.Value), opts...); err != nil {
		return nil, &packs.PackError{Code: packs.CodeInternal, Message: "memory store: " + err.Error(), Cause: err}
	}
	return json.Marshal(out)
}
