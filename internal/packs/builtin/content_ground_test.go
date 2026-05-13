// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/session"
)

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
		Exec: exec.fn,
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
	callCount := 0
	fc := stubFirecrawlFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		switch callCount {
		case 1:
			writeSearchResult(w, firecrawlSearchItem{URL: "https://nature.com/qubits", Title: "Qubits 101", Markdown: "Quantum computers use qubits which are very fast."})
		case 2:
			writeSearchResult(w, firecrawlSearchItem{URL: "https://ibm.com/decoherence", Title: "Decoherence", Markdown: "Decoherence is a major challenge in quantum computing."})
		default:
			http.Error(w, "too many calls", 500)
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
		ClaimsGrounded int  `json:"claims_grounded"`
		FileChanged    bool `json:"file_changed"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.ClaimsGrounded != 0 || out.FileChanged {
		t.Errorf("expected 0 grounded & no file change, got %+v", out)
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
		{"no model", `{"clone_path":"/tmp/helmdeck-blog","path":"q.md"}`, "model is required"},
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
	var searchCalls int
	fc := stubFirecrawlFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		searchCalls++
		body, _ := io.ReadAll(r.Body)
		var req firecrawlSearchRequest
		_ = json.Unmarshal(body, &req)
		writeSearchResult(w, firecrawlSearchItem{URL: "https://ex.com/" + req.Query})
	})
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
	if searchCalls != 5 {
		t.Errorf("firecrawl search calls = %d, want 5", searchCalls)
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
	callCount := 0
	fc := stubFirecrawlFromHandler(t, func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			// First call: legitimate empty result (not a transport error).
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
			return
		}
		// Second call: usable URL.
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
