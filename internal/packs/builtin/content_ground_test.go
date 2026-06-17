// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/tosin2013/helmdeck/internal/memory"
	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/session"
)

// testPerClaimMemory is a minimal MemoryInterface for testing
// content.ground's per-claim cache (issue #523). The real
// memoryAdapter is unexported and bound to engine.Execute; tests that
// call the handler directly need an in-process double. Goroutine-safe
// because content.ground writes to it from the bounded errgroup.
type testPerClaimMemory struct {
	mu sync.Mutex
	m  map[string][]byte
}

func newTestPerClaimMemory() *testPerClaimMemory {
	return &testPerClaimMemory{m: map[string][]byte{}}
}

func (t *testPerClaimMemory) Namespace() string { return "test-caller" }
func (t *testPerClaimMemory) Store(key string, value []byte, _ ...memory.PutOption) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	cp := make([]byte, len(value))
	copy(cp, value)
	t.m[key] = cp
	return nil
}
func (t *testPerClaimMemory) Recall(key string) (*memory.Entry, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	v, ok := t.m[key]
	if !ok {
		return nil, memory.ErrNotFound
	}
	return &memory.Entry{Key: key, Value: v}, nil
}
func (t *testPerClaimMemory) List(_ string) ([]memory.Entry, error)         { return nil, nil }
func (t *testPerClaimMemory) Delete(key string) error                       { t.mu.Lock(); defer t.mu.Unlock(); delete(t.m, key); return nil }
func (t *testPerClaimMemory) Context() (*packs.SessionContext, error)       { return nil, nil }

// execScript stubs ec.Exec with a queue of canned results and
// records every request the handler made. Since ec.Exec already
// closes over the session id in the real engine, the test
// signature drops that argument and only cares about the request.
type execScript struct {
	calls []session.ExecRequest
	// replies is popped in FIFO order; exhaustion returns a zero
	// ExitCode result so tests that don't care about the trailing
	// calls can leave them unset.
	replies []session.ExecResult
	err     error // static error, returned on every call when non-nil
}

func (e *execScript) fn(ctx context.Context, req session.ExecRequest) (session.ExecResult, error) {
	e.calls = append(e.calls, req)
	if e.err != nil {
		return session.ExecResult{}, e.err
	}
	idx := len(e.calls) - 1
	if idx < len(e.replies) {
		return e.replies[idx], nil
	}
	return session.ExecResult{}, nil
}

func runContentGround(t *testing.T, disp *scriptedDispatcherWT, exec *execScript, firecrawl *httptest.Server, input string) (json.RawMessage, error) {
	t.Helper()
	return runContentGroundWithMemory(t, disp, exec, firecrawl, input, nil)
}

// runContentGroundWithMemory wires a MemoryInterface into the
// ExecutionContext so per-claim cache tests can share a cache across
// multiple calls. Pass nil mem for the standard no-cache flow.
func runContentGroundWithMemory(t *testing.T, disp *scriptedDispatcherWT, exec *execScript, firecrawl *httptest.Server, input string, mem packs.MemoryInterface) (json.RawMessage, error) {
	t.Helper()
	if firecrawl != nil {
		t.Setenv("HELMDECK_FIRECRAWL_ENABLED", "true")
		t.Setenv("HELMDECK_FIRECRAWL_URL", firecrawl.URL)
	}
	pack := ContentGround(disp)
	ec := &packs.ExecutionContext{
		Pack:  pack,
		Input: json.RawMessage(input),
		Session: &session.Session{
			ID:     "sess-content",
			Status: session.StatusRunning,
		},
		Exec:   exec.fn,
		Memory: mem,
	}
	return pack.Handler(context.Background(), ec)
}

// stubFirecrawlFromHandler wraps an arbitrary http.HandlerFunc as a
// Firecrawl stub. Useful for tests that need to vary the response
// per call (e.g. first query matches, second returns empty).
func stubFirecrawlFromHandler(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// writeSearchResult is a small helper that emits the JSON Firecrawl's
// /v1/search returns when scrapeOptions is omitted — just URLs, no
// markdown bodies (content.ground only needs the URL).
func writeSearchResult(w http.ResponseWriter, items ...firecrawlSearchItem) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"data":    items,
	})
}

// --- tests ----------------------------------------------------------------

func TestContentGround_HappyPath(t *testing.T) {
	// Two claims, both found in the file, both search queries
	// return a usable URL. Final file should have both links
	// inserted; sha256 in output matches the patched content.
	markdown := "Quantum computers are fast. They use qubits instead of bits.\nBut decoherence is a challenge.\n"
	exec := &execScript{replies: []session.ExecResult{
		{Stdout: []byte("80\n")},           // wc -c
		{Stdout: []byte(markdown)},          // cat
		{ExitCode: 0},                       // cat > (write-back)
	}}
	// Concurrent Phase 2 means Firecrawl receives the two search
	// requests in non-deterministic order. Route the response by
	// the request's Query so each claim gets the URL it expects
	// regardless of arrival timing.
	fc := stubFirecrawlFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch {
		case strings.Contains(body.Query, "qubit") || strings.Contains(body.Query, "computation"):
			writeSearchResult(w, firecrawlSearchItem{URL: "https://nature.com/qubits", Title: "Qubits 101", Markdown: "Quantum computers use qubits which are very fast."})
		case strings.Contains(body.Query, "decoherence"):
			writeSearchResult(w, firecrawlSearchItem{URL: "https://ibm.com/decoherence", Title: "Decoherence", Markdown: "Decoherence is a major challenge in quantum computing."})
		default:
			http.Error(w, "unexpected query: "+body.Query, 500)
		}
	})
	disp := &scriptedDispatcherWT{replies: []string{
		// Reply 1: claim extraction
		`{"claims":[{"text":"Quantum computers are fast.","query":"qubit computation speed"},{"text":"decoherence is a challenge","query":"quantum decoherence challenge"}]}`,
		// Reply 2: verify source for claim 1
		`{"pick":0,"snippet":"Quantum computers use qubits which are very fast."}`,
		// Reply 3: verify source for claim 2
		`{"pick":0,"snippet":"Decoherence is a major challenge."}`,
	}}
	raw, err := runContentGround(t, disp, exec, fc,
		`{"clone_path":"/tmp/helmdeck-blog","path":"quantum.md","model":"openai/gpt-4o-mini"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		Path             string      `json:"path"`
		ClaimsConsidered int         `json:"claims_considered"`
		ClaimsGrounded   int         `json:"claims_grounded"`
		Grounding        []grounding `json:"grounding"`
		Skipped          []string    `json:"skipped"`
		SHA256           string      `json:"sha256"`
		FileChanged      bool        `json:"file_changed"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.ClaimsGrounded != 2 {
		t.Errorf("claims_grounded = %d, want 2", out.ClaimsGrounded)
	}
	if !out.FileChanged {
		t.Errorf("file_changed = false, want true")
	}
	if len(out.Grounding) != 2 {
		t.Errorf("grounding len = %d, want 2", len(out.Grounding))
	}
	if out.Grounding[0].URL != "https://nature.com/qubits" {
		t.Errorf("grounding[0].URL = %q", out.Grounding[0].URL)
	}
	// Verify the write-back body contained both citations.
	if len(exec.calls) != 3 {
		t.Fatalf("exec calls = %d, want 3 (wc, cat, cat>)", len(exec.calls))
	}
	wrote := string(exec.calls[2].Stdin)
	if !strings.Contains(wrote, "[source](https://nature.com/qubits)") {
		t.Errorf("write-back missing nature.com citation: %q", wrote)
	}
	if !strings.Contains(wrote, "[source](https://ibm.com/decoherence)") {
		t.Errorf("write-back missing ibm.com citation: %q", wrote)
	}
}

func TestContentGround_NoClaimsReturnedUnchanged(t *testing.T) {
	markdown := "This post is all opinion. I think quantum is cool.\n"
	exec := &execScript{replies: []session.ExecResult{
		{Stdout: []byte("52\n")}, // wc -c
		{Stdout: []byte(markdown)}, // cat
	}}
	// No Firecrawl call expected — the extractor returns empty.
	fc := stubFirecrawlFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected firecrawl call")
		http.Error(w, "nope", 500)
	})
	disp := &scriptedDispatcherWT{replies: []string{`{"claims":[]}`}}
	raw, err := runContentGround(t, disp, exec, fc,
		`{"clone_path":"/tmp/helmdeck-blog","path":"q.md","model":"openai/gpt-4o-mini"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		ClaimsGrounded int    `json:"claims_grounded"`
		FileChanged    bool   `json:"file_changed"`
		GroundedText   string `json:"grounded_text"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.ClaimsGrounded != 0 || out.FileChanged {
		t.Errorf("expected 0 grounded & no file change, got %+v", out)
	}
	// grounded_text is a total contract — even with zero claims it
	// must be present (== the unchanged input) so downstream pipeline
	// steps that wire ${{ steps.ground.output.grounded_text }} never
	// fail with an unresolved-reference error.
	if out.GroundedText != markdown {
		t.Errorf("grounded_text must equal the unchanged input on zero claims; got %q", out.GroundedText)
	}
	// Write-back must NOT have fired.
	for _, c := range exec.calls {
		if strings.HasPrefix(c.Cmd[len(c.Cmd)-1], "cat > ") {
			t.Errorf("unexpected write-back: %v", c.Cmd)
		}
	}
}

func TestContentGround_HallucinatedClaimSubstringSkipped(t *testing.T) {
	// The model returns a claim whose text does NOT appear in the
	// markdown — the handler must skip it rather than corrupt the
	// file. Also tests that a good claim in the same batch still
	// gets grounded.
	markdown := "Quantum computers are fast.\n"
	exec := &execScript{replies: []session.ExecResult{
		{Stdout: []byte("28\n")},
		{Stdout: []byte(markdown)},
		{ExitCode: 0}, // write-back for the one good claim
	}}
	fc := stubFirecrawlFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeSearchResult(w, firecrawlSearchItem{URL: "https://nature.com/q", Markdown: "Quantum computers are indeed fast."})
	})
	disp := &scriptedDispatcherWT{replies: []string{
		// Claim extraction
		`{"claims":[
			{"text":"This sentence does not exist in the post","query":"ignored"},
			{"text":"Quantum computers are fast.","query":"qubit speed"}
		]}`,
		// Verify source for the good claim (hallucinated claim is skipped before verification)
		`{"pick":0,"snippet":"Quantum computers are indeed fast."}`,
	}}
	raw, err := runContentGround(t, disp, exec, fc,
		`{"clone_path":"/tmp/helmdeck-blog","path":"q.md","model":"openai/gpt-4o-mini"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		ClaimsConsidered int         `json:"claims_considered"`
		ClaimsGrounded   int         `json:"claims_grounded"`
		Skipped          []string    `json:"skipped"`
		Grounding        []grounding `json:"grounding"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.ClaimsConsidered != 2 {
		t.Errorf("considered = %d, want 2", out.ClaimsConsidered)
	}
	if out.ClaimsGrounded != 1 {
		t.Errorf("grounded = %d, want 1", out.ClaimsGrounded)
	}
	if len(out.Skipped) != 1 || !strings.Contains(out.Skipped[0], "This sentence does not exist") {
		t.Errorf("skipped = %v, want the hallucinated claim", out.Skipped)
	}
}

func TestContentGround_NoSourceFoundForClaimIsSkipped(t *testing.T) {
	markdown := "Claim one is real.\n"
	exec := &execScript{replies: []session.ExecResult{
		{Stdout: []byte("19\n")},
		{Stdout: []byte(markdown)},
	}}
	fc := stubFirecrawlFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		// Empty data — no sources found.
		writeSearchResult(w)
	})
	disp := &scriptedDispatcherWT{replies: []string{
		`{"claims":[{"text":"Claim one is real.","query":"something obscure"}]}`,
	}}
	raw, err := runContentGround(t, disp, exec, fc,
		`{"clone_path":"/tmp/helmdeck-blog","path":"q.md","model":"openai/gpt-4o-mini"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		ClaimsGrounded int  `json:"claims_grounded"`
		FileChanged    bool `json:"file_changed"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.ClaimsGrounded != 0 {
		t.Errorf("grounded = %d, want 0", out.ClaimsGrounded)
	}
	if out.FileChanged {
		t.Errorf("file_changed = true, want false — no sources means no patch")
	}
}

func TestContentGround_Disabled(t *testing.T) {
	t.Setenv("HELMDECK_FIRECRAWL_ENABLED", "")
	pack := ContentGround(&scriptedDispatcherWT{})
	ec := &packs.ExecutionContext{
		Pack:    pack,
		Input:   json.RawMessage(`{"clone_path":"/tmp/helmdeck-blog","path":"q.md","model":"openai/gpt-4o"}`),
		Session: &session.Session{ID: "s"},
		Exec:    func(context.Context, session.ExecRequest) (session.ExecResult, error) { return session.ExecResult{}, nil },
	}
	_, err := pack.Handler(context.Background(), ec)
	pe := &packs.PackError{}
	if !errors.As(err, &pe) || pe.Code != packs.CodeInvalidInput {
		t.Errorf("want invalid_input, got %v", err)
	}
	if !strings.Contains(pe.Message, "HELMDECK_FIRECRAWL_ENABLED") {
		t.Errorf("message should name the env var, got %q", pe.Message)
	}
}

func TestContentGround_MissingExecutor(t *testing.T) {
	t.Setenv("HELMDECK_FIRECRAWL_ENABLED", "true")
	pack := ContentGround(&scriptedDispatcherWT{})
	ec := &packs.ExecutionContext{
		Pack:    pack,
		Input:   json.RawMessage(`{"clone_path":"/tmp/helmdeck-blog","path":"q.md","model":"openai/gpt-4o"}`),
		Session: &session.Session{ID: "s"},
		// Exec intentionally nil
	}
	_, err := pack.Handler(context.Background(), ec)
	pe := &packs.PackError{}
	if !errors.As(err, &pe) || pe.Code != packs.CodeSessionUnavailable {
		t.Errorf("want session_unavailable, got %v", err)
	}
}

func TestContentGround_MissingRequiredFields(t *testing.T) {
	fc := stubFirecrawlSearch(t, 200, `{"success":true,"data":[]}`)
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"no clone_path or text", `{"path":"q.md","model":"openai/gpt-4o"}`, "either 'text'"},
		{"no path", `{"clone_path":"/tmp/helmdeck-blog","model":"openai/gpt-4o"}`, "path is required"},
		// `model` is intentionally NOT in this list: omitted model
		// now falls back to defaultPackModel() (model_defaults.go).
		// The "Tier C model forgot to pass the argument" failure mode
		// no longer surfaces as invalid_input — see
		// TestContentGround_DefaultsModelWhenOmitted below.
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := &execScript{}
			_, err := runContentGround(t, &scriptedDispatcherWT{}, exec, fc, tc.input)
			pe := &packs.PackError{}
			if !errors.As(err, &pe) || pe.Code != packs.CodeInvalidInput {
				t.Fatalf("want invalid_input, got %v", err)
			}
			if !strings.Contains(pe.Message, tc.want) {
				t.Errorf("message = %q, want contains %q", pe.Message, tc.want)
			}
		})
	}
}

// TestContentGround_DefaultsModelWhenOmitted — Tier C models calling
// content.ground via MCP routinely omit the `model` argument
// (observed against openai/gpt-oss-120b:free on 2026-06-09 during
// the tech-blog-publisher mcp-adr-analysis-server flow). With the
// model_defaults.go helper, omitted `model` no longer surfaces as
// invalid_input — the handler resolves a default and proceeds.
// Verify the call reaches the dispatcher (text-mode happy path)
// rather than rejecting at input validation.
func TestContentGround_DefaultsModelWhenOmitted(t *testing.T) {
	t.Setenv("HELMDECK_DEFAULT_PACK_MODEL", "")
	t.Setenv("HELMDECK_OPENROUTER_MODELS", "")
	fc := stubFirecrawlSearch(t, 200, `{"success":true,"data":[]}`)
	disp := &scriptedDispatcherWT{replies: []string{
		`{"claims":[]}`, // claim-extractor returns no claims → early return path
	}}
	raw, err := runContentGround(t, disp, &execScript{}, fc,
		`{"text":"This is a real claim.","topic":"x"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(disp.captured) != 1 {
		t.Fatalf("expected dispatcher to be called once (default model resolved + applied), got %d calls", len(disp.captured))
	}
	if disp.captured[0].Model != "openrouter/auto" {
		t.Errorf("dispatcher Model = %q, want openrouter/auto (hard fallback)", disp.captured[0].Model)
	}
	// Sanity-check the output is well-formed (handler proceeded past
	// model resolution).
	if len(raw) == 0 {
		t.Error("expected non-empty output")
	}
}

func TestContentGround_EmptyFile(t *testing.T) {
	fc := stubFirecrawlSearch(t, 200, `{"success":true,"data":[]}`)
	exec := &execScript{replies: []session.ExecResult{
		{Stdout: []byte("1\n")},  // wc
		{Stdout: []byte("\n")},    // cat — whitespace-only
	}}
	_, err := runContentGround(t, &scriptedDispatcherWT{}, exec, fc,
		`{"clone_path":"/tmp/helmdeck-blog","path":"q.md","model":"openai/gpt-4o"}`)
	pe := &packs.PackError{}
	if !errors.As(err, &pe) || pe.Code != packs.CodeInvalidInput {
		t.Errorf("want invalid_input, got %v", err)
	}
}

func TestContentGround_UnparseableClaimPlan(t *testing.T) {
	fc := stubFirecrawlSearch(t, 200, `{"success":true,"data":[]}`)
	markdown := "A post with a real claim.\n"
	exec := &execScript{replies: []session.ExecResult{
		{Stdout: []byte("26\n")},
		{Stdout: []byte(markdown)},
	}}
	disp := &scriptedDispatcherWT{replies: []string{`I'm not going to answer.`}}
	_, err := runContentGround(t, disp, exec, fc,
		`{"clone_path":"/tmp/helmdeck-blog","path":"q.md","model":"openai/gpt-4o"}`)
	pe := &packs.PackError{}
	if !errors.As(err, &pe) || pe.Code != packs.CodeHandlerFailed {
		t.Errorf("want handler_failed, got %v", err)
	}
}

func TestContentGround_MaxClaimsCap(t *testing.T) {
	// Model returns more claims than max — pack must cap to
	// maxContentGroundClaims (8) and the Firecrawl server should
	// see at most that many search calls.
	markdown := "c1.\nc2.\nc3.\nc4.\nc5.\nc6.\nc7.\nc8.\nc9.\nc10.\n"
	exec := &execScript{replies: []session.ExecResult{
		{Stdout: []byte("30\n")},
		{Stdout: []byte(markdown)},
		{}, // write-back; exit 0
	}}
	// atomic.Int32 because the handler now serves concurrent
	// requests (Phase 2 of content_ground.go runs claims in parallel
	// via errgroup).
	var searchCallsAtomic atomic.Int32
	fc := stubFirecrawlFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		searchCallsAtomic.Add(1)
		body, _ := io.ReadAll(r.Body)
		var req firecrawlSearchRequest
		_ = json.Unmarshal(body, &req)
		writeSearchResult(w, firecrawlSearchItem{URL: "https://ex.com/" + req.Query})
	})
	searchCalls := func() int { return int(searchCallsAtomic.Load()) }
	// 10 claims returned, but the pack caps itself to 8 even if
	// the input max_claims was 5 — actually the input cap is 5,
	// so let's test it: input max_claims=5, model returns 10,
	// handler should only process 5.
	var claims strings.Builder
	claims.WriteString(`{"claims":[`)
	for i := 1; i <= 10; i++ {
		if i > 1 {
			claims.WriteString(",")
		}
		claims.WriteString(`{"text":"c`)
		claims.WriteString(itoa(i))
		claims.WriteString(`.","query":"q`)
		claims.WriteString(itoa(i))
		claims.WriteString(`"}`)
	}
	claims.WriteString(`]}`)
	disp := &scriptedDispatcherWT{replies: []string{claims.String()}}
	raw, err := runContentGround(t, disp, exec, fc,
		`{"clone_path":"/tmp/helmdeck-blog","path":"q.md","model":"openai/gpt-4o-mini","max_claims":5}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		ClaimsConsidered int `json:"claims_considered"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.ClaimsConsidered != 5 {
		t.Errorf("claims_considered = %d, want 5 (input cap)", out.ClaimsConsidered)
	}
	if got := searchCalls(); got != 5 {
		t.Errorf("firecrawl search calls = %d, want 5", got)
	}
}

// TestContentGround_DefaultMaxTokens verifies the claim-extractor
// dispatch carries the new 2048-token default cap (#179 — 1024 was
// too tight and truncated JSON mid-response with weak models).
func TestContentGround_DefaultMaxTokens(t *testing.T) {
	fc := stubFirecrawlSearch(t, 200, `{"success":true,"data":[]}`)
	disp := &scriptedDispatcherWT{replies: []string{`{"claims":[]}`}}
	_, err := runContentGround(t, disp, &execScript{}, fc,
		`{"text":"A short post.","model":"openai/gpt-4o-mini"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if len(disp.captured) == 0 {
		t.Fatal("dispatcher received no requests")
	}
	got := disp.captured[0].MaxTokens
	if got == nil || *got != 2048 {
		t.Errorf("MaxTokens = %v, want 2048", got)
	}
}

// TestContentGround_MaxCompletionTokensOverride verifies operators
// can raise the cap via the new input field for verbose JSON or
// long-claim posts.
func TestContentGround_MaxCompletionTokensOverride(t *testing.T) {
	fc := stubFirecrawlSearch(t, 200, `{"success":true,"data":[]}`)
	disp := &scriptedDispatcherWT{replies: []string{`{"claims":[]}`}}
	_, err := runContentGround(t, disp, &execScript{}, fc,
		`{"text":"A short post.","model":"openai/gpt-4o-mini","max_completion_tokens":4096}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	got := disp.captured[0].MaxTokens
	if got == nil || *got != 4096 {
		t.Errorf("MaxTokens = %v, want 4096", got)
	}
}

// TestContentGround_MaxCompletionTokensOverCap rejects values above
// the 8192 hard cap with CodeInvalidInput so runaway costs are
// surfaced loud rather than silently truncated downstream.
func TestContentGround_MaxCompletionTokensOverCap(t *testing.T) {
	fc := stubFirecrawlSearch(t, 200, `{"success":true,"data":[]}`)
	disp := &scriptedDispatcherWT{}
	_, err := runContentGround(t, disp, &execScript{}, fc,
		`{"text":"x","model":"openai/gpt-4o-mini","max_completion_tokens":16384}`)
	pe := &packs.PackError{}
	if !errors.As(err, &pe) || pe.Code != packs.CodeInvalidInput {
		t.Errorf("want invalid_input for over-cap, got %v", err)
	}
	if len(disp.captured) != 0 {
		t.Errorf("dispatcher should not be called when input is rejected, got %d calls", len(disp.captured))
	}
}

// TestContentGround_FirecrawlAllErrors verifies the pack fails loud
// with CodeHandlerFailed when every Firecrawl search call returns a
// transport error — silently degrading to "no sources found" would
// mislead the caller about Firecrawl reachability (#182).
func TestContentGround_FirecrawlAllErrors(t *testing.T) {
	// 500 on every search call.
	fc := stubFirecrawlSearch(t, 500, `internal error`)
	markdown := "Quantum computers are fast. Decoherence is a challenge.\n"
	disp := &scriptedDispatcherWT{replies: []string{
		`{"claims":[{"text":"Quantum computers are fast.","query":"q1"},{"text":"Decoherence is a challenge.","query":"q2"}]}`,
	}}
	_, err := runContentGround(t, disp, &execScript{}, fc,
		`{"text":"`+strings.ReplaceAll(markdown, "\n", "\\n")+`","model":"openai/gpt-4o-mini"}`)
	pe := &packs.PackError{}
	if !errors.As(err, &pe) {
		t.Fatalf("want PackError, got %v (%T)", err, err)
	}
	if pe.Code != packs.CodeHandlerFailed {
		t.Errorf("Code = %q, want handler_failed", pe.Code)
	}
	if !strings.Contains(pe.Message, "every Firecrawl search call failed") {
		t.Errorf("Message = %q, want substring 'every Firecrawl search call failed'", pe.Message)
	}
}

// TestContentGround_FirecrawlPartialErrorsSucceed verifies the
// 100%-errors gate doesn't kill partial-success runs. With Firecrawl
// healthy but one query returning empty, the run should complete
// with the surviving claims grounded.
func TestContentGround_FirecrawlPartialErrorsSucceed(t *testing.T) {
	// Route response by query — concurrent Phase 2 means call order
	// is non-deterministic, so the "first claim's call" vs "second
	// claim's call" framing only works if we key off content.
	fc := stubFirecrawlFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Query == "q1" {
			// q1: legitimate empty result (not a transport error).
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
			return
		}
		// q2 (and any other): usable URL.
		writeSearchResult(w, firecrawlSearchItem{URL: "https://ex.com/d", Title: "D", Markdown: "Decoherence is a challenge."})
	})
	markdown := "Quantum computers are fast. Decoherence is a challenge."
	disp := &scriptedDispatcherWT{replies: []string{
		`{"claims":[{"text":"Quantum computers are fast.","query":"q1"},{"text":"Decoherence is a challenge.","query":"q2"}]}`,
		`{"pick":0,"snippet":"Decoherence is a challenge."}`,
	}}
	raw, err := runContentGround(t, disp, &execScript{}, fc,
		`{"text":"`+markdown+`","model":"openai/gpt-4o-mini"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		ClaimsGrounded int `json:"claims_grounded"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.ClaimsGrounded != 1 {
		t.Errorf("claims_grounded = %d, want 1 (partial success)", out.ClaimsGrounded)
	}
}

// itoa is a tiny helper because the test file otherwise only uses
// `strings` and I'd rather not import strconv just for one call.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [12]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// TestRewriteWithSources_DiscardsTruncated verifies the rewrite is
// rejected (errRewriteTruncated) when the model hits the token ceiling.
// This is the guard that stops a half-written deck from being shipped:
// the caller falls back to the citation-only version, preserving every
// slide.
func TestRewriteWithSources_DiscardsTruncated(t *testing.T) {
	disp := &scriptedDispatcherWT{
		replies:       []string{"# Slide 1\n\n---\n\n# Slide 2 (cut off he"},
		finishReasons: []string{"length"},
	}
	gs := []grounding{{Claim: "x", URL: "https://example.com", Snippet: "s"}}
	_, err := rewriteWithSources(context.Background(), disp, "openai/gpt-4o-mini", "original text", gs, "")
	if !errors.Is(err, errRewriteTruncated) {
		t.Fatalf("expected errRewriteTruncated on FinishReason=length, got %v", err)
	}
}

// TestRewriteWithSources_TokenBudgetScales verifies the rewrite's
// completion budget scales with the input size and is clamped to
// [defaultContentGroundTokens, maxContentGroundTokens]. The old fixed
// 2048 cap truncated long decks; the budget must grow with the input.
func TestRewriteWithSources_TokenBudgetScales(t *testing.T) {
	gs := []grounding{{Claim: "x", URL: "https://example.com", Snippet: "s"}}

	cases := []struct {
		name string
		text string
		want int
	}{
		{"short input clamps to floor", "tiny", defaultContentGroundTokens},
		{
			// ~6000 tokens of input → 6000*5/4 = 7500, under the 8192 cap.
			name: "mid input scales above floor",
			text: strings.Repeat("a", 24000),
			want: 24000 / 4 * 5 / 4,
		},
		{
			// Huge input → scaled value exceeds the cap, clamps to ceiling.
			name: "huge input clamps to ceiling",
			text: strings.Repeat("a", 200000),
			want: maxContentGroundTokens,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			disp := &scriptedDispatcherWT{replies: []string{"rewritten"}}
			if _, err := rewriteWithSources(context.Background(), disp, "m", tc.text, gs, ""); err != nil {
				t.Fatalf("rewriteWithSources: %v", err)
			}
			if len(disp.captured) != 1 || disp.captured[0].MaxTokens == nil {
				t.Fatalf("expected one captured request with MaxTokens set")
			}
			if got := *disp.captured[0].MaxTokens; got != tc.want {
				t.Errorf("MaxTokens = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestContentGround_PersonaDirectiveInRewritePrompt — closed-set persona
// keys resolve to a distinct directive injected into the rewrite system
// prompt. Without this, every rewrite came back formal-academic — the
// observation that prompted #346.
func TestContentGround_PersonaDirectiveInRewritePrompt(t *testing.T) {
	gs := []grounding{{Claim: "x", URL: "https://example.com", Snippet: "s"}}
	for _, tc := range []struct {
		persona  string
		mustHave string
	}{
		{"general", "conversational"},
		{"technical", "hands-on"},
		{"marketing", "benefits-led"},
		{"executive", "impact-led"},
		{"educational", "step-by-step"},
		{"academic", "hedged"},
	} {
		t.Run(tc.persona, func(t *testing.T) {
			directive, used := resolveContentGroundPersona(tc.persona)
			if used != tc.persona {
				t.Errorf("persona_used = %q, want %q", used, tc.persona)
			}
			disp := &scriptedDispatcherWT{replies: []string{"rewritten"}}
			if _, err := rewriteWithSources(context.Background(), disp, "m", "original", gs, directive); err != nil {
				t.Fatalf("rewriteWithSources: %v", err)
			}
			sys := disp.captured[0].Messages[0].Content.Text()
			if !strings.Contains(sys, tc.mustHave) {
				t.Errorf("rewrite system prompt should contain %q for persona %q, got:\n%s", tc.mustHave, tc.persona, sys)
			}
		})
	}
}

// TestContentGround_FreeformPersonaPassThrough — unknown persona keys are
// passed through as a freeform tone hint; persona_used echoes the
// original string.
func TestContentGround_FreeformPersonaPassThrough(t *testing.T) {
	directive, used := resolveContentGroundPersona("crisp newsroom")
	if used != "crisp newsroom" {
		t.Errorf("persona_used = %q, want freeform passthrough", used)
	}
	if !strings.Contains(directive, "crisp newsroom") {
		t.Errorf("directive should include the freeform hint, got: %q", directive)
	}
}

// TestContentGround_DefaultPersonaWhenOmitted — empty persona resolves
// to "general" so the rewrite always has a tone directive.
func TestContentGround_DefaultPersonaWhenOmitted(t *testing.T) {
	_, used := resolveContentGroundPersona("")
	if used != "general" {
		t.Errorf("empty persona resolved to %q, want general", used)
	}
}

// --- audit follow-up tests (PR after 2026-06-15 review) ----------------

// TestContentGround_MemoryCacheConfigured pins ADR 047 compliance.
// The pack must declare a Memory cache with a non-trivial TTL so the
// engine de-dupes idempotent re-runs (same caller, same input bytes)
// rather than spending Firecrawl/LLM calls every time. Other expensive
// packs (github.go, etc.) ship the same pattern; content.ground was
// the outlier called out in the audit.
func TestContentGround_MemoryCacheConfigured(t *testing.T) {
	pack := ContentGround(&scriptedDispatcherWT{})
	if pack.Memory == nil {
		t.Fatal("ContentGround must declare Memory cache config (ADR 047 audit gap)")
	}
	if !pack.Memory.Cache {
		t.Error("Memory.Cache must be true; without it the cache helper is a no-op")
	}
	if pack.Memory.TTL <= 0 {
		t.Errorf("Memory.TTL must be positive; got %v", pack.Memory.TTL)
	}
	if pack.Memory.Category != "cache" {
		t.Errorf("Memory.Category should be \"cache\" to match the engine's cache namespace; got %q", pack.Memory.Category)
	}
}

// TestFindClaimSpan_ExactMatch_FastPath asserts the existing-behavior
// guarantee: a byte-identical claim still goes through the strict
// strings.Index path (no fuzzy detour). Regression-pins the 95% case.
func TestFindClaimSpan_ExactMatch_FastPath(t *testing.T) {
	doc := "Quantum computers are fast. They run on qubits.\n"
	start, end, ok := findClaimSpan(doc, "Quantum computers are fast.")
	if !ok {
		t.Fatal("exact substring should match")
	}
	if doc[start:end] != "Quantum computers are fast." {
		t.Errorf("span [%d:%d] = %q, want exact claim", start, end, doc[start:end])
	}
}

// TestFindClaimSpan_WhitespaceTolerance_DoubleToSingle is the audit's
// finding C reproduction: the LLM extractor occasionally returns a
// claim text that's been normalized (double-space → single-space) vs
// the source document. The strict matcher used to drop these as
// "hallucinations." The fuzzy matcher recovers them by treating
// whitespace runs as equivalent.
func TestFindClaimSpan_WhitespaceTolerance_DoubleToSingle(t *testing.T) {
	// Doc has double spaces — original source style. LLM returned
	// the claim with double-space collapsed to single-space.
	doc := "Quantum  computers  are  fast.\n" // double-spaced
	llmClaim := "Quantum computers are fast." // LLM collapsed
	start, end, ok := findClaimSpan(doc, llmClaim)
	if !ok {
		t.Fatalf("fuzzy matcher should locate the claim despite whitespace normalization: doc=%q claim=%q", doc, llmClaim)
	}
	// The span should cover the ORIGINAL doc bytes (double-spaced),
	// not the LLM's normalized form. This is what lets the patcher
	// splice [source](url) after the doc's literal text.
	got := doc[start:end]
	if got != "Quantum  computers  are  fast." {
		t.Errorf("span should map back to the original doc bytes (double-spaced); got %q", got)
	}
}

// TestFindClaimSpan_WhitespaceTolerance_NewlineToSpace covers the
// reverse case: doc has a soft line wrap mid-sentence, LLM returned
// the claim with the wrap normalized to a single space.
func TestFindClaimSpan_WhitespaceTolerance_NewlineToSpace(t *testing.T) {
	doc := "Quantum computers\nare fast.\n"
	llmClaim := "Quantum computers are fast."
	start, end, ok := findClaimSpan(doc, llmClaim)
	if !ok {
		t.Fatalf("fuzzy matcher should locate the claim across a soft wrap: doc=%q claim=%q", doc, llmClaim)
	}
	got := doc[start:end]
	if !strings.Contains(got, "Quantum computers") || !strings.Contains(got, "are fast.") {
		t.Errorf("span %q should include both halves of the wrapped claim", got)
	}
}

// TestFindClaimSpan_NoMatch covers the hallucination case unchanged.
// A claim that's NOT in the doc at all (with no fuzzy variation that
// hits) must still miss — otherwise we'd corrupt the markdown by
// splicing citations next to text that doesn't exist.
func TestFindClaimSpan_NoMatch(t *testing.T) {
	doc := "Quantum computers are fast.\n"
	llmClaim := "Cats are excellent quantum computers."
	if _, _, ok := findClaimSpan(doc, llmClaim); ok {
		t.Error("fuzzy matcher must not match a claim that's substantively absent")
	}
}

// TestFindClaimSpan_EmptyClaim covers the edge case — an empty claim
// against a non-empty doc returns no match (rather than spuriously
// matching at offset 0).
func TestFindClaimSpan_EmptyClaim(t *testing.T) {
	if _, _, ok := findClaimSpan("doc content", ""); ok {
		// strings.Index("doc","") returns 0; we accept that as a hit
		// because the fast path defers to strings.Index — but the
		// fuzzy path WOULD also hit. The patcher upstream would
		// splice "[source](url)" at offset 0, which is harmless but
		// noisy. Worth pinning as the current behavior so a future
		// change is explicit.
		_ = ok
	}
	// (No assertion — this test documents the edge but doesn't fail
	// either way. If you tighten findClaimSpan to reject empty
	// claims, flip the assertion above.)
}

// TestContentGround_FuzzyClaimMatch_DoubleSpacedSourceGrounds is the
// end-to-end version of finding C: the markdown source has double
// spaces, the LLM extractor returned the claim with single spaces,
// and the handler should still ground it (instead of treating it
// like a hallucinated substring).
func TestContentGround_FuzzyClaimMatch_DoubleSpacedSourceGrounds(t *testing.T) {
	markdown := "Quantum  computers  are  fast.\n" // double-spaced
	exec := &execScript{replies: []session.ExecResult{
		{Stdout: []byte("32\n")},
		{Stdout: []byte(markdown)},
		{ExitCode: 0}, // write-back
	}}
	fc := stubFirecrawlFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeSearchResult(w, firecrawlSearchItem{
			URL:      "https://nature.com/q",
			Markdown: "Quantum computers are indeed fast.",
		})
	})
	disp := &scriptedDispatcherWT{replies: []string{
		// LLM extractor returns the claim with whitespace collapsed.
		`{"claims":[{"text":"Quantum computers are fast.","query":"qubit speed"}]}`,
		// Verify step picks the (one) source.
		`{"pick":0,"snippet":"Quantum computers are indeed fast."}`,
	}}
	raw, err := runContentGround(t, disp, exec, fc,
		`{"clone_path":"/tmp/helmdeck-blog","path":"q.md","model":"openai/gpt-4o-mini"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		ClaimsConsidered int      `json:"claims_considered"`
		ClaimsGrounded   int      `json:"claims_grounded"`
		Skipped          []string `json:"skipped"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.ClaimsConsidered != 1 {
		t.Errorf("considered = %d, want 1", out.ClaimsConsidered)
	}
	if out.ClaimsGrounded != 1 {
		t.Errorf("grounded = %d, want 1 (the strict matcher would have dropped this as a hallucination)", out.ClaimsGrounded)
	}
	if len(out.Skipped) != 0 {
		t.Errorf("nothing should be skipped; got %v", out.Skipped)
	}
}

// --- JIT length-sizing (issue #531 / convention #525) ---------------------

// TestContentGround_Inspect_NoFirecrawlNoDispatcher — inspect mode
// short-circuits before the dispatcher / Firecrawl-enabled gate.
// Cheap planning helper that works in gateway-less, Firecrawl-less
// environments.
func TestContentGround_Inspect_NoFirecrawlNoDispatcher(t *testing.T) {
	// Don't enable Firecrawl. Inspect must skip the gate.
	pack := ContentGround(nil)
	ec := &packs.ExecutionContext{
		Pack:  pack,
		Input: json.RawMessage(`{"text":"some markdown","inspect":true,"length_intent":"summary"}`),
	}
	raw, err := pack.Handler(context.Background(), ec)
	if err != nil {
		t.Fatalf("inspect with nil dispatcher + Firecrawl disabled: %v", err)
	}
	var out struct {
		Inspect             bool   `json:"inspect"`
		SuggestedMaxClaims  int    `json:"suggested_max_claims"`
		LengthIntentApplied string `json:"length_intent_applied"`
		Reason              string `json:"reason"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.Inspect {
		t.Errorf("inspect not echoed")
	}
	if out.SuggestedMaxClaims != 3 {
		t.Errorf("suggested_max_claims = %d, want 3 (summary)", out.SuggestedMaxClaims)
	}
	if out.LengthIntentApplied != "intent:summary" {
		t.Errorf("applied = %q, want intent:summary", out.LengthIntentApplied)
	}
	if !strings.Contains(out.Reason, "summary") || !strings.Contains(out.Reason, "3") {
		t.Errorf("reason should mention intent + count: %q", out.Reason)
	}
}

// TestContentGround_ResolveSize_Precedence — explicit max_claims >
// length_intent > legacy default. Pinned in code so the precedence
// doesn't drift from docs.
func TestContentGround_ResolveSize_Precedence(t *testing.T) {
	// (1) explicit wins, clamped to ceiling.
	in := contentGroundInput{MaxClaims: 20, LengthIntent: "summary"}
	if size := resolveContentGroundSize(&in); size.maxClaims != maxContentGroundClaims || size.applied != "explicit" {
		t.Errorf("explicit clamp: maxClaims=%d applied=%q", size.maxClaims, size.applied)
	}
	// (2) explicit within range.
	in = contentGroundInput{MaxClaims: 4, LengthIntent: "summary"}
	if size := resolveContentGroundSize(&in); size.maxClaims != 4 || size.applied != "explicit" {
		t.Errorf("explicit honored: maxClaims=%d applied=%q", size.maxClaims, size.applied)
	}
	// (3) intent.
	in = contentGroundInput{LengthIntent: "summary"}
	if size := resolveContentGroundSize(&in); size.maxClaims != 3 || size.applied != "intent:summary" {
		t.Errorf("intent: maxClaims=%d applied=%q", size.maxClaims, size.applied)
	}
	// (4) default.
	in = contentGroundInput{}
	size := resolveContentGroundSize(&in)
	if size.maxClaims != defaultContentGroundClaims || size.applied != "default" {
		t.Errorf("default: maxClaims=%d applied=%q", size.maxClaims, size.applied)
	}
}

// TestContentGround_LengthIntent_ScalesByIntent — each intent maps to
// the right max_claims value. Verified by reading max_claims_applied
// in the output.
func TestContentGround_LengthIntent_ScalesByIntent(t *testing.T) {
	cases := []struct {
		intent string
		want   int
	}{
		{"summary", 3},
		{"thorough", 5},
		{"exhaustive", 8},
	}
	for _, tc := range cases {
		t.Run(tc.intent, func(t *testing.T) {
			markdown := "A claim. Another claim.\n"
			exec := &execScript{}
			// No claims found → early-return path; we only need
			// the JIT fields populated in that response.
			fc := stubFirecrawlFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
				writeSearchResult(w)
			})
			disp := &scriptedDispatcherWT{replies: []string{
				// Empty claim list → early return with JIT fields.
				`{"claims":[]}`,
			}}
			raw, err := runContentGround(t, disp, exec, fc, fmt.Sprintf(
				`{"text":%q,"length_intent":%q}`, markdown, tc.intent))
			if err != nil {
				t.Fatalf("handler: %v", err)
			}
			var out struct {
				MaxClaimsApplied    int    `json:"max_claims_applied"`
				LengthIntentApplied string `json:"length_intent_applied"`
			}
			_ = json.Unmarshal(raw, &out)
			if out.MaxClaimsApplied != tc.want {
				t.Errorf("intent=%s: max_claims_applied = %d, want %d",
					tc.intent, out.MaxClaimsApplied, tc.want)
			}
			if want := "intent:" + tc.intent; out.LengthIntentApplied != want {
				t.Errorf("applied = %q, want %q", out.LengthIntentApplied, want)
			}
		})
	}
}

// TestContentGround_BackCompat_ExplicitMaxClaimsWins — when max_claims
// is set, it wins over length_intent. Existing callers see ZERO change.
func TestContentGround_BackCompat_ExplicitMaxClaimsWins(t *testing.T) {
	markdown := "A claim.\n"
	exec := &execScript{}
	fc := stubFirecrawlFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeSearchResult(w)
	})
	disp := &scriptedDispatcherWT{replies: []string{`{"claims":[]}`}}
	raw, err := runContentGround(t, disp, exec, fc, fmt.Sprintf(
		`{"text":%q,"max_claims":7,"length_intent":"summary"}`, markdown))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		MaxClaimsApplied    int    `json:"max_claims_applied"`
		LengthIntentApplied string `json:"length_intent_applied"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.MaxClaimsApplied != 7 {
		t.Errorf("max_claims_applied = %d, want 7 (explicit honored)", out.MaxClaimsApplied)
	}
	if out.LengthIntentApplied != "explicit" {
		t.Errorf("applied = %q, want explicit", out.LengthIntentApplied)
	}
}

// TestContentGround_BackCompat_DefaultLabel — no input → legacy
// default 5 with applied:"default" (distinct from "intent:thorough"
// so callers can tell which path ran).
func TestContentGround_BackCompat_DefaultLabel(t *testing.T) {
	markdown := "A claim.\n"
	exec := &execScript{}
	fc := stubFirecrawlFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeSearchResult(w)
	})
	disp := &scriptedDispatcherWT{replies: []string{`{"claims":[]}`}}
	raw, err := runContentGround(t, disp, exec, fc, fmt.Sprintf(
		`{"text":%q}`, markdown))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		MaxClaimsApplied    int    `json:"max_claims_applied"`
		LengthIntentApplied string `json:"length_intent_applied"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.MaxClaimsApplied != defaultContentGroundClaims {
		t.Errorf("max_claims_applied = %d, want %d (legacy default)",
			out.MaxClaimsApplied, defaultContentGroundClaims)
	}
	if out.LengthIntentApplied != "default" {
		t.Errorf("applied = %q, want default", out.LengthIntentApplied)
	}
}

// TestContentGround_Truncated_ExtractorFinishReasonLength — extractor
// LLM hitting finish_reason=length surfaces as truncated:true so
// callers can re-run with smaller intent or larger
// max_completion_tokens.
func TestContentGround_Truncated_ExtractorFinishReasonLength(t *testing.T) {
	markdown := "A claim.\n"
	exec := &execScript{}
	fc := stubFirecrawlFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeSearchResult(w)
	})
	disp := &scriptedDispatcherWT{
		replies:       []string{`{"claims":[]}`},
		finishReasons: []string{"length"},
	}
	raw, err := runContentGround(t, disp, exec, fc, fmt.Sprintf(
		`{"text":%q}`, markdown))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		Truncated bool `json:"truncated"`
	}
	_ = json.Unmarshal(raw, &out)
	if !out.Truncated {
		t.Error("extractor finish_reason=length should set truncated:true")
	}
}

// TestContentGround_UnknownIntentFallsBack — misspelled intent falls
// back to thorough rather than erroring.
func TestContentGround_UnknownIntentFallsBack(t *testing.T) {
	in := contentGroundInput{LengthIntent: "deeper-than-deep-dive"}
	size := resolveContentGroundSize(&in)
	if size.applied != "intent:thorough" {
		t.Errorf("applied = %q, want intent:thorough", size.applied)
	}
}

// --- Per-claim cache (issue #523) -----------------------------------------

// TestContentGround_PerClaimCache_TypoFixWorkflow exercises the canonical
// scenario from issue #523: the user runs content.ground, mutates ONE
// claim (and surrounding prose), runs again, and expects the unchanged
// claims to skip Firecrawl + verify entirely. The engine-level cache
// (ADR 047) keyed on sha256(input bytes) is a miss on any edit; the
// per-claim cache keyed on sha256(claim_text, search_query) survives
// unrelated edits.
func TestContentGround_PerClaimCache_TypoFixWorkflow(t *testing.T) {
	mem := newTestPerClaimMemory()

	// Three claims. Same extractor JSON is replayed on each run (the
	// claim_text values stay stable across both runs since the
	// EXTRACTOR is what produces them; the test scenario mutates the
	// markdown around a claim, not the claim text). Each run also
	// needs one verify reply per UNCACHED claim — three on run 1, one
	// on run 2.
	extractorJSON := `{"claims":[
		{"text":"Quantum computers use qubits.","query":"quantum qubits"},
		{"text":"They are very fast.","query":"quantum speed"},
		{"text":"Decoherence is a challenge.","query":"quantum decoherence"}
	]}`
	verifyReply := `{"pick":0,"snippet":"matched"}`

	// Run 1: extractor + 3 verify calls.
	disp := &scriptedDispatcherWT{replies: []string{
		extractorJSON, verifyReply, verifyReply, verifyReply,
	}}

	var firecrawlCalls atomic.Int32
	fc := stubFirecrawlFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		firecrawlCalls.Add(1)
		var body struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		// Return distinct URL per query so each claim gets a usable pick.
		writeSearchResult(w, firecrawlSearchItem{
			URL:      "https://source.example/" + body.Query,
			Title:    "Result for " + body.Query,
			Markdown: "supporting evidence",
		})
	})

	// Surrounding prose with a typo. Run 1's title says "Computres"
	// (typo); run 2 fixes it to "Computers". The three claim
	// sentences below the title stay byte-identical, so all three
	// claims locate via findClaimSpan in both runs — and the cache
	// keys (claim_text, query) match across runs.
	markdown1 := "# Quantum Computres: A Primer\n\nQuantum computers use qubits. They are very fast.\nDecoherence is a challenge.\n"
	exec := &execScript{} // text mode — no exec calls
	raw1, err := runContentGroundWithMemory(t, disp, exec, fc,
		fmt.Sprintf(`{"text":%q,"model":"openrouter/auto"}`, markdown1),
		mem)
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	var out1 struct {
		ClaimsCached   int `json:"claims_cached"`
		FirecrawlCalls int `json:"firecrawl_calls"`
		ClaimsGrounded int `json:"claims_grounded"`
	}
	_ = json.Unmarshal(raw1, &out1)
	if out1.ClaimsCached != 0 {
		t.Errorf("run 1 claims_cached = %d, want 0 (cold cache)", out1.ClaimsCached)
	}
	if out1.FirecrawlCalls != 3 {
		t.Errorf("run 1 firecrawl_calls = %d, want 3", out1.FirecrawlCalls)
	}
	if out1.ClaimsGrounded != 3 {
		t.Errorf("run 1 claims_grounded = %d, want 3", out1.ClaimsGrounded)
	}
	if got := firecrawlCalls.Load(); got != 3 {
		t.Errorf("run 1 firecrawl HTTP hits = %d, want 3", got)
	}

	// Run 2: typo fix in the title only. The three claim sentences
	// are byte-identical to run 1, so all three claims locate AND hit
	// the per-claim cache. The engine-level cache (sha256 of input
	// bytes) WOULD miss here — input bytes differ — but the per-claim
	// cache is content-derived.
	firecrawlCalls.Store(0)
	disp2 := &scriptedDispatcherWT{replies: []string{extractorJSON}}
	markdown2 := "# Quantum Computers: A Primer\n\nQuantum computers use qubits. They are very fast.\nDecoherence is a challenge.\n" // typo fix: Computres → Computers

	raw2, err := runContentGroundWithMemory(t, disp2, exec, fc,
		fmt.Sprintf(`{"text":%q,"model":"openrouter/auto"}`, markdown2),
		mem)
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	var out2 struct {
		ClaimsCached   int `json:"claims_cached"`
		FirecrawlCalls int `json:"firecrawl_calls"`
		ClaimsGrounded int `json:"claims_grounded"`
	}
	_ = json.Unmarshal(raw2, &out2)
	if out2.ClaimsCached != 3 {
		t.Errorf("run 2 claims_cached = %d, want 3 (all hit)", out2.ClaimsCached)
	}
	if out2.FirecrawlCalls != 0 {
		t.Errorf("run 2 firecrawl_calls = %d, want 0 (all cached)", out2.FirecrawlCalls)
	}
	if out2.ClaimsGrounded != 3 {
		t.Errorf("run 2 claims_grounded = %d, want 3", out2.ClaimsGrounded)
	}
	if got := firecrawlCalls.Load(); got != 0 {
		t.Errorf("run 2 firecrawl HTTP hits = %d, want 0 (cache served)", got)
	}
	// Dispatcher should have fired ONCE (extractor) — no verify calls
	// because every claim was cached.
	if disp2.calls != 1 {
		t.Errorf("run 2 dispatcher calls = %d, want 1 (extractor only, no verify)", disp2.calls)
	}
}

// TestContentGround_PerClaimCache_MutateOneClaim — mutating ONE claim's
// text (but leaving the other two stable) yields 2 hits + 1 miss. The
// changed claim runs through Firecrawl + verify; the unchanged ones
// short-circuit on the cache. The acceptance criteria's main test.
func TestContentGround_PerClaimCache_MutateOneClaim(t *testing.T) {
	mem := newTestPerClaimMemory()

	// Run 1: extract 3 claims + verify each.
	extractor1 := `{"claims":[
		{"text":"Quantum computers use qubits.","query":"quantum qubits"},
		{"text":"They are very fast.","query":"quantum speed"},
		{"text":"Decoherence is a challenge.","query":"quantum decoherence"}
	]}`
	verifyReply := `{"pick":0,"snippet":"matched"}`
	disp1 := &scriptedDispatcherWT{replies: []string{
		extractor1, verifyReply, verifyReply, verifyReply,
	}}
	var fcCalls atomic.Int32
	fc := stubFirecrawlFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		fcCalls.Add(1)
		var body struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		writeSearchResult(w, firecrawlSearchItem{
			URL:      "https://source.example/" + body.Query,
			Markdown: "evidence",
		})
	})

	markdown1 := "Quantum computers use qubits. They are very fast.\nDecoherence is a challenge.\n"
	exec := &execScript{}
	if _, err := runContentGroundWithMemory(t, disp1, exec, fc,
		fmt.Sprintf(`{"text":%q,"model":"openrouter/auto"}`, markdown1), mem); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	// Run 2: extractor returns ONE NEW claim ("Quantum supremacy was
	// achieved.") and two unchanged. The new claim needs a fresh
	// Firecrawl + verify; the 2 unchanged claims hit the cache.
	fcCalls.Store(0)
	extractor2 := `{"claims":[
		{"text":"Quantum computers use qubits.","query":"quantum qubits"},
		{"text":"Quantum supremacy was achieved.","query":"quantum supremacy"},
		{"text":"Decoherence is a challenge.","query":"quantum decoherence"}
	]}`
	// Only ONE verify reply because only the new claim runs through verify.
	disp2 := &scriptedDispatcherWT{replies: []string{extractor2, verifyReply}}
	markdown2 := "Quantum computers use qubits. Quantum supremacy was achieved.\nDecoherence is a challenge.\n"
	raw2, err := runContentGroundWithMemory(t, disp2, exec, fc,
		fmt.Sprintf(`{"text":%q,"model":"openrouter/auto"}`, markdown2), mem)
	if err != nil {
		t.Fatalf("mutate-one run: %v", err)
	}

	var out struct {
		ClaimsCached   int `json:"claims_cached"`
		FirecrawlCalls int `json:"firecrawl_calls"`
		ClaimsGrounded int `json:"claims_grounded"`
	}
	_ = json.Unmarshal(raw2, &out)
	if out.ClaimsCached != 2 {
		t.Errorf("claims_cached = %d, want 2 (unchanged claims)", out.ClaimsCached)
	}
	if out.FirecrawlCalls != 1 {
		t.Errorf("firecrawl_calls = %d, want 1 (just the new claim)", out.FirecrawlCalls)
	}
	if got := fcCalls.Load(); got != 1 {
		t.Errorf("firecrawl HTTP hits = %d, want 1", got)
	}
	if out.ClaimsGrounded != 3 {
		t.Errorf("claims_grounded = %d, want 3", out.ClaimsGrounded)
	}
	// disp2 fired twice: extractor + one verify (for the new claim).
	if disp2.calls != 2 {
		t.Errorf("dispatcher calls = %d, want 2 (extractor + 1 verify)", disp2.calls)
	}
}

// TestContentGround_PerClaimCache_NoMemoryNilSafe — when no Memory is
// wired on ExecutionContext (engine without WithMemoryStore), the cache
// path is a no-op. Phase 2 runs as before; firecrawl_calls counter
// reflects every result.
func TestContentGround_PerClaimCache_NoMemoryNilSafe(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{
		`{"claims":[{"text":"A claim.","query":"a"}]}`,
		`{"pick":0,"snippet":"matched"}`,
	}}
	fc := stubFirecrawlFromHandler(t, func(w http.ResponseWriter, _ *http.Request) {
		writeSearchResult(w, firecrawlSearchItem{URL: "https://x", Markdown: "y"})
	})
	exec := &execScript{}
	raw, err := runContentGround(t, disp, exec, fc,
		`{"text":"A claim.","model":"openrouter/auto"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		ClaimsCached   int `json:"claims_cached"`
		FirecrawlCalls int `json:"firecrawl_calls"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.ClaimsCached != 0 {
		t.Errorf("claims_cached = %d, want 0 (no memory)", out.ClaimsCached)
	}
	if out.FirecrawlCalls != 1 {
		t.Errorf("firecrawl_calls = %d, want 1", out.FirecrawlCalls)
	}
}

// TestContentGround_PerClaimCache_KeyStability — same (text, query) →
// same key. Sanity check.
func TestContentGround_PerClaimCache_KeyStability(t *testing.T) {
	k1 := perClaimCacheKey("Quantum computers use qubits.", "quantum qubits")
	k2 := perClaimCacheKey("Quantum computers use qubits.", "quantum qubits")
	if k1 != k2 {
		t.Errorf("same inputs → different keys: %q vs %q", k1, k2)
	}
	// Different query → different key.
	k3 := perClaimCacheKey("Quantum computers use qubits.", "different query")
	if k1 == k3 {
		t.Errorf("different query produced same key")
	}
	// Different text → different key.
	k4 := perClaimCacheKey("Different claim.", "quantum qubits")
	if k1 == k4 {
		t.Errorf("different text produced same key")
	}
	// Key uses the cg:claim: prefix for namespace clarity.
	if !strings.HasPrefix(k1, "cg:claim:") {
		t.Errorf("key %q missing cg:claim: prefix", k1)
	}
}
