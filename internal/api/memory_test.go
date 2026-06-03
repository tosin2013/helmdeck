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
	"time"

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

// TestMemoryForget_BadJSON — non-empty malformed body returns 400.
// (Empty body falls through to scope=all by design.)
func TestMemoryForget_BadJSON(t *testing.T) {
	h, _ := newMemoryRouter(t)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/memory/forget",
		bytes.NewBufferString(`{not-json`)))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

// TestMemoryStore_BadJSON — malformed body on /memory/store → 400.
func TestMemoryStore_BadJSON(t *testing.T) {
	h, _ := newMemoryRouter(t)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/memory/store",
		bytes.NewBufferString(`{nope`)))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

// TestMemoryForget_PackEmptyID — "pack:" without a value → 400.
func TestMemoryForget_PackEmptyID(t *testing.T) {
	h, _ := newMemoryRouter(t)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/memory/forget",
		bytes.NewBufferString(`{"scope":"pack:"}`)))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (%s)", rr.Code, rr.Body.String())
	}
}

// TestMemoryForget_PipelineEmptyID — "pipeline:" without a value → 400.
func TestMemoryForget_PipelineEmptyID(t *testing.T) {
	h, _ := newMemoryRouter(t)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/memory/forget",
		bytes.NewBufferString(`{"scope":"pipeline:"}`)))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (%s)", rr.Code, rr.Body.String())
	}
}

// TestMemoryForget_KeyEmpty — "key:" without a value → 400.
func TestMemoryForget_KeyEmpty(t *testing.T) {
	h, _ := newMemoryRouter(t)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/memory/forget",
		bytes.NewBufferString(`{"scope":"key:"}`)))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (%s)", rr.Code, rr.Body.String())
	}
}

// TestMemoryForget_ScopedByPipelineID — "pipeline:<id>" deletes only
// audits for the named pipeline.
func TestMemoryForget_ScopedByPipelineID(t *testing.T) {
	h, store := newMemoryRouter(t)
	caller := "unknown"
	// Use the pack-audit seeding helper repurposed: we need pipeline
	// audits. Write them directly so we don't need a new helper.
	for _, pid := range []string{"flow.a", "flow.a", "flow.b"} {
		audit := packs.PipelineAudit{Pipeline: pid, Outcome: "ok", AtUnix: 1, DurationMs: 1}
		blob, _ := json.Marshal(audit)
		key := packs.AuditKeyPrefixPipeline + pid + "/run_" + pid + "_" + time.Now().Format("150405.000000000")
		_, _ = store.Put(context.Background(), caller, key, blob, memory.WithTTL(time.Hour))
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/memory/forget",
		bytes.NewBufferString(`{"scope":"pipeline:flow.a"}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rr.Code, rr.Body.String())
	}
	var got memoryForgetResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if got.Deleted < 1 {
		t.Errorf("want at least 1 deleted, got %d", got.Deleted)
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

// ── ADR 048 PR #2: POST /api/v1/memory/store ──────────────────────────

// TestMemoryStore_HappyPath round-trips a fact through the REST endpoint
// and confirms the row lands in the store under the bare caller
// namespace with the default category + applied TTL.
func TestMemoryStore_HappyPath(t *testing.T) {
	h, store := newMemoryRouter(t)
	body := bytes.NewBufferString(`{"key":"preferences/frontend","value":"React over Vue"}`)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/memory/store", body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var got memoryStoreResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Key != "preferences/frontend" {
		t.Errorf("want key preferences/frontend, got %q", got.Key)
	}
	if got.Category != packs.DefaultFactCategory {
		t.Errorf("want default category %q, got %q", packs.DefaultFactCategory, got.Category)
	}
	if got.ExpiresAt == "" {
		t.Errorf("expires_at should be populated")
	}
	// The row landed under the bare caller (auth disabled → "unknown").
	entries, _ := store.List(context.Background(), "unknown", "preferences/")
	if len(entries) != 1 {
		t.Fatalf("want 1 stored row, got %d", len(entries))
	}
	if entries[0].Category != packs.DefaultFactCategory {
		t.Errorf("want category %q, got %q", packs.DefaultFactCategory, entries[0].Category)
	}
	if string(entries[0].Value) != "React over Vue" {
		t.Errorf("value not persisted: %q", entries[0].Value)
	}
}

// TestMemoryStore_ReservedCategoryRejected proves the guard against
// agents writing into engine audit categories.
func TestMemoryStore_ReservedCategoryRejected(t *testing.T) {
	h, _ := newMemoryRouter(t)
	for _, cat := range []string{packs.AuditCategoryPack, packs.AuditCategoryPipeline} {
		body := bytes.NewBufferString(`{"key":"x","value":"y","category":"` + cat + `"}`)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/memory/store", body))
		if rr.Code != http.StatusBadRequest {
			t.Errorf("category %q should be rejected with 400; got %d body=%s", cat, rr.Code, rr.Body.String())
		}
	}
}

// TestMemoryStore_TTLGuards covers both the too-short and too-long
// bounds so a typo can't pin a fact for nanoseconds or eternity.
func TestMemoryStore_TTLGuards(t *testing.T) {
	h, _ := newMemoryRouter(t)
	cases := []struct {
		name string
		ttl  int64
	}{
		{"too-short", 30},                // 30 s < 1 h
		{"too-long", 400 * 24 * 60 * 60}, // 400 d > 365 d
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := bytes.NewBufferString(`{"key":"x","value":"y","ttl_seconds":` + intToStr(tc.ttl) + `}`)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/memory/store", body))
			if rr.Code != http.StatusBadRequest {
				t.Errorf("%s ttl should be rejected with 400; got %d body=%s", tc.name, rr.Code, rr.Body.String())
			}
		})
	}
}

// TestMemoryStore_MissingFields proves both required fields are checked.
func TestMemoryStore_MissingFields(t *testing.T) {
	h, _ := newMemoryRouter(t)
	cases := []string{
		`{}`,                        // both missing
		`{"key":"x"}`,               // no value
		`{"value":"y"}`,             // no key
		`{"key":"  ","value":"y"}`,  // whitespace-only key
		`{"key":"x","value":"   "}`, // whitespace-only value
	}
	for _, body := range cases {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/memory/store", bytes.NewBufferString(body)))
		if rr.Code != http.StatusBadRequest {
			t.Errorf("body %s should 400; got %d", body, rr.Code)
		}
	}
}

// TestMemoryStore_CustomCategoryAndTags proves the caller can override
// category and pass tags, and both land on the stored entry.
func TestMemoryStore_CustomCategoryAndTags(t *testing.T) {
	h, store := newMemoryRouter(t)
	body := bytes.NewBufferString(`{"key":"konflux/deploy","value":"only via Konflux","category":"project_conventions","tags":["deploy","konflux"]}`)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/memory/store", body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	entries, _ := store.List(context.Background(), "unknown", "konflux/")
	if len(entries) != 1 {
		t.Fatalf("want 1 row, got %d", len(entries))
	}
	if entries[0].Category != "project_conventions" {
		t.Errorf("want project_conventions, got %q", entries[0].Category)
	}
	if len(entries[0].Tags) != 2 {
		t.Errorf("want 2 tags, got %d", len(entries[0].Tags))
	}
}

// TestMemoryStore_EmptyStoreNoOp proves the endpoint soft-succeeds
// when no memory store is wired (memory-disabled deploy).
func TestMemoryStore_EmptyStoreNoOp(t *testing.T) {
	h := NewRouter(Deps{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
	})
	body := bytes.NewBufferString(`{"key":"x","value":"y"}`)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/memory/store", body))
	if rr.Code != http.StatusOK {
		t.Errorf("nil-store should soft-succeed (synthetic entry); got %d body=%s", rr.Code, rr.Body.String())
	}
}

func intToStr(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	buf := []byte{}
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	if neg {
		buf = append([]byte{'-'}, buf...)
	}
	return string(buf)
}
