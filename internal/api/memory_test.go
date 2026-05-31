// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tosin2013/helmdeck/internal/memory"
	"github.com/tosin2013/helmdeck/internal/packs"
)

// newMemoryRouter builds a router with a real *packs.Engine wired to
// an in-memory store. Auth is disabled so the test request's caller
// resolves to "unknown" (matching the production callerSubject
// fallback).
func newMemoryRouter(t *testing.T) (http.Handler, memory.MemoryStore) {
	t.Helper()
	store := memory.NewInMemoryStore()
	eng := packs.New(packs.WithMemoryStore(store), packs.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	h := NewRouter(Deps{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:    "test",
		PackEngine: eng,
	})
	return h, store
}

func seedPackAudit(t *testing.T, s memory.MemoryStore, ns, pack string, atUnix int64, learn map[string]string) {
	t.Helper()
	body, _ := json.Marshal(packs.PackAudit{
		Pack: pack, Outcome: "ok", AtUnix: atUnix, LearnInputs: learn,
	})
	key := packs.AuditKeyPrefixPack + pack + "/" + jsonInt(atUnix)
	if _, err := s.Put(context.Background(), ns, key, body, memory.WithCategory(packs.AuditCategoryPack)); err != nil {
		t.Fatal(err)
	}
}

func jsonInt(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// TestMemoryDefaults_EmptyWithoutStore — engine has no memory store
// wired → 200 with empty arrays + an explanatory note.
func TestMemoryDefaults_EmptyWithoutStore(t *testing.T) {
	h := NewRouter(Deps{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
	})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/memory/defaults", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var got memoryDefaultsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Packs) != 0 || len(got.Pipelines) != 0 {
		t.Errorf("want empty projection; got packs=%d pipelines=%d", len(got.Packs), len(got.Pipelines))
	}
	if got.Note == "" {
		t.Errorf("expected explanatory note when store missing")
	}
}

// TestMemoryDefaults_PicksMostCommon — seeded audit history projects
// into per-pack defaults and surfaces a recent-activity list.
func TestMemoryDefaults_PicksMostCommon(t *testing.T) {
	h, store := newMemoryRouter(t)
	caller := "unknown" // auth disabled → callerSubject returns "unknown"
	seedPackAudit(t, store, caller, "blog.rewrite_for_audience", 100, map[string]string{"persona": "technical", "audience": "engineers"})
	seedPackAudit(t, store, caller, "blog.rewrite_for_audience", 200, map[string]string{"persona": "technical", "audience": "engineers"})
	seedPackAudit(t, store, caller, "blog.rewrite_for_audience", 300, map[string]string{"persona": "marketing"})

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/memory/defaults", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var got memoryDefaultsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Packs) != 1 {
		t.Fatalf("want 1 pack; got %d", len(got.Packs))
	}
	if got.Packs[0].CommonInputs["persona"] != "technical" {
		t.Errorf("want persona=technical (2 of 3 successes); got %q", got.Packs[0].CommonInputs["persona"])
	}
	if len(got.Recent) != 3 {
		t.Fatalf("want 3 recent rows; got %d", len(got.Recent))
	}
	// Newest first ordering.
	if got.Recent[0].AtUnix != 300 || got.Recent[2].AtUnix != 100 {
		t.Errorf("recent should be newest-first; got %d ... %d", got.Recent[0].AtUnix, got.Recent[2].AtUnix)
	}
}

// TestMemoryForget_All — seed two rows, POST /forget scope=all, both gone.
func TestMemoryForget_All(t *testing.T) {
	h, store := newMemoryRouter(t)
	caller := "unknown"
	seedPackAudit(t, store, caller, "blog.publish", 100, nil)
	seedPackAudit(t, store, caller, "doc.parse", 200, nil)

	body := bytes.NewBufferString(`{"scope":"all"}`)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/memory/forget", body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var got memoryForgetResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if got.Deleted != 2 {
		t.Errorf("want 2 deleted; got %d", got.Deleted)
	}
	left, _ := store.List(context.Background(), caller, packs.AuditKeyPrefixPack)
	if len(left) != 0 {
		t.Errorf("audit rows should be empty post-forget; got %d", len(left))
	}
}

// TestMemoryForget_BadScope — unknown scope → 400.
func TestMemoryForget_BadScope(t *testing.T) {
	h, _ := newMemoryRouter(t)
	body := bytes.NewBufferString(`{"scope":"nope"}`)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/memory/forget", body))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400; got %d body=%s", rr.Code, rr.Body.String())
	}
}

// TestMemoryForget_ScopedByPackID — seed mixed, forget one pack only.
func TestMemoryForget_ScopedByPackID(t *testing.T) {
	h, store := newMemoryRouter(t)
	caller := "unknown"
	seedPackAudit(t, store, caller, "blog.publish", 100, nil)
	seedPackAudit(t, store, caller, "blog.publish", 200, nil)
	seedPackAudit(t, store, caller, "doc.parse", 300, nil)

	body := bytes.NewBufferString(`{"scope":"pack:blog.publish"}`)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/memory/forget", body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	var got memoryForgetResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if got.Deleted != 2 {
		t.Errorf("want 2 deleted (blog.publish only); got %d", got.Deleted)
	}
	left, _ := store.List(context.Background(), caller, packs.AuditKeyPrefixPack)
	if len(left) != 1 {
		t.Errorf("want 1 row left (doc.parse); got %d", len(left))
	}
}

// TestMemoryForget_KeyScope — per-row forget via "key:<exact>" deletes
// only that key. Backs the UI's "forget this run" buttons.
func TestMemoryForget_KeyScope(t *testing.T) {
	h, store := newMemoryRouter(t)
	caller := "unknown"
	seedPackAudit(t, store, caller, "blog.publish", 100, nil)
	seedPackAudit(t, store, caller, "blog.publish", 200, nil)

	// Discover the actual key prefix from the store so we delete a real entry.
	entries, _ := store.List(context.Background(), caller, packs.AuditKeyPrefixPack)
	if len(entries) != 2 {
		t.Fatalf("seed setup off; got %d", len(entries))
	}
	targetKey := entries[0].Key
	body := bytes.NewBufferString(`{"scope":"key:` + targetKey + `"}`)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/memory/forget", body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	var got memoryForgetResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if got.Deleted != 1 {
		t.Errorf("want 1 deleted; got %d", got.Deleted)
	}
	left, _ := store.List(context.Background(), caller, packs.AuditKeyPrefixPack)
	if len(left) != 1 {
		t.Errorf("want 1 row left; got %d", len(left))
	}
}
