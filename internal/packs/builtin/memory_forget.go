// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// memory_forget.go — helmdeck.memory_forget pack (ADR 047 PR #2).
//
// The audit hooks in internal/packs/audit.go write one memory entry per
// pack execution (and one per pipeline run) under the caller's bare
// namespace. Entries expire automatically via TTL (packs.AuditTTL,
// today 30 days), but the caller may want to forget sooner — fresh
// session for a new project, privacy reset, exiting a multi-tenant
// engagement.
//
// This pack exposes that reset as a first-class capability so the chat
// agent can honor "forget what you know about me" without a sidecar
// API call. It is the cleanup half of the write/clean contract:
// memory features ship with both surfaces in the same release.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tosin2013/helmdeck/internal/packs"
)

// MemoryForget returns the helmdeck.memory_forget pack. No external
// dependencies — operates purely against the engine-wired memory store
// via ec.Memory. NeedsSession: false so the memoryAdapter namespace is
// the bare caller (cross-session learning is also cross-session forget).
func MemoryForget() *packs.Pack {
	return &packs.Pack{
		Name:        "helmdeck.memory_forget",
		Version:     "v1",
		Description: "Erase the caller's pack/pipeline audit history (ADR 047 memory layer). Use when the user asks to 'forget' learned defaults, start a new project context, or clear history before a tenant handoff. Targets only audit rows (categories pack_history / pipeline_history); never touches pack caches or vault credentials.",
		NoAudit:     true,
		Metadata: packs.PackMetadata{
			Accepts:        []string{"none"},
			Produces:       []string{"deletion_summary"},
			IntentKeywords: []string{"forget my history", "clear learned defaults", "reset routing memory", "delete audit log", "start fresh"},
			TypicalUse:     "Call when the user says 'forget what you know about me' or 'reset my defaults'. Optional scope lets the user clear just one pack/pipeline's history instead of everything.",
			Limitations:    []string{"only targets audit rows under categories pack_history / pipeline_history — does NOT clear pack caches (content.ground Firecrawl cache, github.* REST cache, etc.) or vault credentials", "scoped to the calling subject's namespace; cannot forget another caller's history"},
		},
		InputSchema: packs.BasicSchema{
			Properties: map[string]string{
				"scope": "string",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"scope", "deleted"},
			Properties: map[string]string{
				"scope":   "string",
				"deleted": "number",
			},
		},
		Handler: memoryForgetHandler,
	}
}

func memoryForgetHandler(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
	if ec.Memory == nil {
		// Without a memory store wired, "forget" is a no-op success —
		// there's nothing to delete and refusing would surprise the
		// caller. Surface scope so the agent's response is accurate.
		return json.Marshal(map[string]any{
			"scope":   "all",
			"deleted": 0,
			"note":    "no memory store configured; nothing to forget",
		})
	}
	var in struct {
		Scope string `json:"scope"`
	}
	if len(ec.Input) > 0 {
		if err := json.Unmarshal(ec.Input, &in); err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "scope: " + err.Error()}
		}
	}
	scope := strings.TrimSpace(in.Scope)
	if scope == "" {
		scope = "all"
	}

	prefixes, err := prefixesForScope(scope)
	if err != nil {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error()}
	}

	deleted := 0
	for _, p := range prefixes {
		entries, lerr := ec.Memory.List(p)
		if lerr != nil {
			return nil, &packs.PackError{Code: packs.CodeInternal, Message: "list " + p + ": " + lerr.Error()}
		}
		for _, e := range entries {
			if derr := ec.Memory.Delete(e.Key); derr != nil {
				return nil, &packs.PackError{Code: packs.CodeInternal, Message: "delete " + e.Key + ": " + derr.Error()}
			}
			deleted++
		}
	}

	return json.Marshal(map[string]any{
		"scope":   scope,
		"deleted": deleted,
	})
}

// prefixesForScope returns the memory key prefixes to clear given a
// user-supplied scope string. Recognized scopes:
//
//	"all"                     → pack_history/ AND pipeline_history/
//	"packs"                   → pack_history/
//	"pipelines"               → pipeline_history/
//	"pack:<pack-name>"        → pack_history/<pack-name>/
//	"pipeline:<pipeline-id>"  → pipeline_history/<pipeline-id>/
//
// Anything else is rejected as invalid input so a typo doesn't silently
// no-op (and let the user think their history was cleared when it wasn't).
func prefixesForScope(scope string) ([]string, error) {
	switch scope {
	case "all":
		return []string{packs.AuditKeyPrefixPack, packs.AuditKeyPrefixPipeline}, nil
	case "packs":
		return []string{packs.AuditKeyPrefixPack}, nil
	case "pipelines":
		return []string{packs.AuditKeyPrefixPipeline}, nil
	}
	if strings.HasPrefix(scope, "pack:") {
		id := strings.TrimPrefix(scope, "pack:")
		if id == "" {
			return nil, fmt.Errorf("scope %q: missing pack id", scope)
		}
		return []string{packs.AuditKeyPrefixPack + id + "/"}, nil
	}
	if strings.HasPrefix(scope, "pipeline:") {
		id := strings.TrimPrefix(scope, "pipeline:")
		if id == "" {
			return nil, fmt.Errorf("scope %q: missing pipeline id", scope)
		}
		return []string{packs.AuditKeyPrefixPipeline + id + "/"}, nil
	}
	return nil, fmt.Errorf("unknown scope %q (valid: all, packs, pipelines, pack:<id>, pipeline:<id>)", scope)
}
