// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package api

// memory.go — REST endpoints backing the Management UI's Memory page
// (ADR 047 PR #4):
//
//   GET  /api/v1/memory/defaults   → per-caller projection (the same
//                                    shape helmdeck://my-defaults serves
//                                    over MCP).
//   POST /api/v1/memory/forget     → clear audit history. Body:
//                                    {"scope":"all" | "packs" |
//                                     "pipelines" | "pack:<id>" |
//                                     "pipeline:<id>"}
//
// Both endpoints derive the caller from the verified JWT subject. The
// memory store is read straight off deps.PackEngine (matching the MCP
// resource's wiring) so empty / nil-store cases degrade gracefully.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/tosin2013/helmdeck/internal/auth"
	"github.com/tosin2013/helmdeck/internal/memory"
	"github.com/tosin2013/helmdeck/internal/packs"
)

type memoryDefaultsResponse struct {
	Scope          string                      `json:"scope"`
	FetchedAt      string                      `json:"fetched_at"`
	Packs          []packs.ProjectedPack       `json:"packs"`
	Pipelines      []packs.ProjectedPipeline   `json:"pipelines"`
	CommonFindings []packs.CommonFinding       `json:"common_findings,omitempty"`
	Recent         []memoryRecentAuditResponse `json:"recent"`
	Note           string                      `json:"note,omitempty"`
}

// memoryRecentAuditResponse is one row in the "recent activity" list
// the UI shows below the defaults projection. Same shape as
// packs.PackAudit / PipelineAudit, plus a "kind" discriminator and the
// memory key (so per-row forget can target it precisely).
type memoryRecentAuditResponse struct {
	Kind        string            `json:"kind"` // "pack" or "pipeline"
	Key         string            `json:"key"`
	ID          string            `json:"id"`
	Outcome     string            `json:"outcome"`
	AtUnix      int64             `json:"at_unix"`
	DurationMs  int64             `json:"duration_ms,omitempty"`
	LearnInputs map[string]string `json:"learn_inputs,omitempty"`
}

type memoryForgetRequest struct {
	Scope string `json:"scope"`
}

type memoryForgetResponse struct {
	Scope   string `json:"scope"`
	Deleted int    `json:"deleted"`
}

// memoryStoreRequest is the body shape POST /api/v1/memory/store accepts.
// All fields except Key + Value are optional. See storeMemoryFact for the
// guard rules (reserved categories, TTL clamping, namespace scoping).
type memoryStoreRequest struct {
	Key        string   `json:"key"`
	Value      string   `json:"value"`
	Category   string   `json:"category,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	TTLSeconds int64    `json:"ttl_seconds,omitempty"`
}

type memoryStoreResponse struct {
	Key       string `json:"key"`
	Category  string `json:"category"`
	ExpiresAt string `json:"expires_at"`
}

func registerMemoryRoutes(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("GET /api/v1/memory/defaults", func(w http.ResponseWriter, r *http.Request) {
		caller := callerSubject(r)
		// Admin override: ?caller=<name> lets a holder of the admin
		// scope inspect another caller's Routing Memory. Non-admin
		// callers see their own scope regardless of the query param —
		// defense in depth for the per-caller isolation contract
		// (ADR 047) so a Tier-C operator can't browse the admin's
		// learned defaults by guessing the query string.
		if override := strings.TrimSpace(r.URL.Query().Get("caller")); override != "" && override != caller {
			if c := auth.FromContext(r.Context()); c != nil && c.Has(auth.ScopeAdmin) {
				caller = override
			}
		}
		store := memoryStoreFromDeps(deps)
		resp := memoryDefaultsResponse{
			Scope:     "caller=" + caller,
			FetchedAt: time.Now().UTC().Format(time.RFC3339),
			Packs:     []packs.ProjectedPack{},
			Pipelines: []packs.ProjectedPipeline{},
			Recent:    []memoryRecentAuditResponse{},
		}
		if store == nil {
			resp.Note = "memory store not configured; no learned defaults available"
			writeJSON(w, http.StatusOK, resp)
			return
		}
		def, err := packs.BuildDefaults(r.Context(), store, caller)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "build defaults: "+err.Error())
			return
		}
		resp.Packs = def.Packs
		resp.Pipelines = def.Pipelines
		resp.CommonFindings = def.CommonFindings
		recent, err := recentAudits(r.Context(), store, caller)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "list recent: "+err.Error())
			return
		}
		resp.Recent = recent
		if len(resp.Packs) == 0 && len(resp.Pipelines) == 0 && len(resp.Recent) == 0 {
			resp.Note = "no audit history yet; defaults will fill in as packs/pipelines run under this caller"
		}
		writeJSON(w, http.StatusOK, resp)
	})

	mux.HandleFunc("POST /api/v1/memory/forget", func(w http.ResponseWriter, r *http.Request) {
		var req memoryForgetRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			// Empty body is allowed → defaults to scope=all. A malformed
			// non-empty body is a real 400.
			writeError(w, http.StatusBadRequest, "invalid_input", "body must be JSON: "+err.Error())
			return
		}
		scope := strings.TrimSpace(req.Scope)
		if scope == "" {
			scope = "all"
		}
		// memory_forget pack owns the scope vocabulary; reuse its
		// prefix resolution so REST and pack-call paths can never drift.
		prefixes, err := forgetPrefixesFor(scope)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_input", err.Error())
			return
		}
		caller := callerSubject(r)
		store := memoryStoreFromDeps(deps)
		if store == nil {
			writeJSON(w, http.StatusOK, memoryForgetResponse{Scope: scope, Deleted: 0})
			return
		}
		// Use DeletePrefix instead of list-then-delete. Critical when
		// the memory key has rotated (ephemeral master across
		// restarts) — listing decrypts every row, so a single
		// undecryptable orphan blocks forget from clearing anything.
		// DeletePrefix never reads ciphertext; it just DELETEs by
		// prefix at the SQL layer.
		deleted := 0
		for _, p := range prefixes {
			n, derr := store.DeletePrefix(r.Context(), caller, p)
			if derr != nil {
				writeError(w, http.StatusInternalServerError, "internal", "delete prefix: "+derr.Error())
				return
			}
			deleted += n
		}
		writeJSON(w, http.StatusOK, memoryForgetResponse{Scope: scope, Deleted: deleted})
	})

	// GET /api/v1/memory/callers — list distinct callers (namespaces)
	// + row count per caller. Powers the Routing Memory page's
	// caller-selector dropdown (issue #569). Admin-gated: non-admin
	// callers get back a single-entry response with just their own
	// caller, so a Tier-C operator can't enumerate which other
	// callers exist on the deployment.
	mux.HandleFunc("GET /api/v1/memory/callers", func(w http.ResponseWriter, r *http.Request) {
		store := memoryStoreFromDeps(deps)
		if store == nil {
			writeJSON(w, http.StatusOK, map[string]any{"callers": []any{}})
			return
		}
		all, err := store.ListNamespaces(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "list callers: "+err.Error())
			return
		}
		// Filter to admin-only if the caller isn't admin. Non-admins
		// still see themselves with their own count (so the UI can
		// still render the "you have N entries" badge).
		self := callerSubject(r)
		isAdmin := false
		if c := auth.FromContext(r.Context()); c != nil && c.Has(auth.ScopeAdmin) {
			isAdmin = true
		}
		if !isAdmin {
			filtered := all[:0]
			for _, nc := range all {
				if nc.Namespace == self {
					filtered = append(filtered, nc)
					break
				}
			}
			all = filtered
		}
		writeJSON(w, http.StatusOK, map[string]any{"callers": all})
	})

	mux.HandleFunc("POST /api/v1/memory/store", func(w http.ResponseWriter, r *http.Request) {
		var req memoryStoreRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_input", "body must be JSON: "+err.Error())
			return
		}
		entry, ferr := packs.StoreFact(r.Context(), memoryStoreFromDeps(deps), callerSubject(r), packs.StoreFactRequest{
			Key:      req.Key,
			Value:    req.Value,
			Category: req.Category,
			Tags:     req.Tags,
			TTL:      time.Duration(req.TTLSeconds) * time.Second,
		})
		if ferr != nil {
			status := http.StatusBadRequest
			if ferr.Code == packs.FactErrBackend {
				status = http.StatusInternalServerError
			}
			writeError(w, status, "invalid_input", ferr.Message)
			return
		}
		writeJSON(w, http.StatusOK, memoryStoreResponse{
			Key:       entry.Key,
			Category:  entry.Category,
			ExpiresAt: entry.ExpiresAt.UTC().Format(time.RFC3339),
		})
	})
}

// callerSubject extracts the JWT subject from the verified claims the
// auth middleware attached. Returns "unknown" when running with auth
// disabled — same convention packs.callerFromContext uses, so audit
// rows written under "unknown" remain queryable here.
func callerSubject(r *http.Request) string {
	if c := auth.FromContext(r.Context()); c != nil && c.Subject != "" {
		return c.Subject
	}
	return "unknown"
}

func memoryStoreFromDeps(deps Deps) memory.MemoryStore {
	if deps.PackEngine == nil {
		return nil
	}
	return deps.PackEngine.MemoryStore()
}

func recentAudits(ctx context.Context, store memory.MemoryStore, caller string) ([]memoryRecentAuditResponse, error) {
	out := []memoryRecentAuditResponse{}
	packEntries, err := store.List(ctx, caller, packs.AuditKeyPrefixPack)
	if err != nil {
		return nil, err
	}
	for _, e := range packEntries {
		var a packs.PackAudit
		if uerr := json.Unmarshal(e.Value, &a); uerr != nil {
			continue
		}
		out = append(out, memoryRecentAuditResponse{
			Kind: "pack", Key: e.Key, ID: a.Pack, Outcome: a.Outcome,
			AtUnix: a.AtUnix, DurationMs: a.DurationMs, LearnInputs: a.LearnInputs,
		})
	}
	pipeEntries, err := store.List(ctx, caller, packs.AuditKeyPrefixPipeline)
	if err != nil {
		return nil, err
	}
	for _, e := range pipeEntries {
		var a packs.PipelineAudit
		if uerr := json.Unmarshal(e.Value, &a); uerr != nil {
			continue
		}
		out = append(out, memoryRecentAuditResponse{
			Kind: "pipeline", Key: e.Key, ID: a.Pipeline, Outcome: a.Outcome,
			AtUnix: a.AtUnix, DurationMs: a.DurationMs, LearnInputs: a.LearnInputs,
		})
	}
	// newest first; cap to a sane page so the UI doesn't choke if
	// someone has thousands of rows in flight.
	sort.SliceStable(out, func(i, j int) bool { return out[i].AtUnix > out[j].AtUnix })
	if len(out) > 200 {
		out = out[:200]
	}
	return out, nil
}

// forgetPrefixesFor mirrors the scope vocabulary of the
// helmdeck.memory_forget pack so the REST and pack paths return the
// same set of prefixes for the same scope. Sourced from a literal
// switch (not the pack's unexported helper) to avoid an import cycle
// between internal/api and internal/packs/builtin.
func forgetPrefixesFor(scope string) ([]string, error) {
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
	// Also accept a literal memory key — the UI's per-row "forget this
	// run" button passes the exact key it received in /defaults.
	if strings.HasPrefix(scope, "key:") {
		k := strings.TrimPrefix(scope, "key:")
		if k == "" {
			return nil, fmt.Errorf("scope %q: missing key", scope)
		}
		return []string{k}, nil
	}
	return nil, fmt.Errorf("unknown scope %q (valid: all, packs, pipelines, pack:<id>, pipeline:<id>, key:<exact-key>)", scope)
}
