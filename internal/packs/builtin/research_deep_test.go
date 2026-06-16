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
)

// stubFirecrawlSearch is the v1/search twin of stubFirecrawl. Tests
// hand in the response to return; the test flips the env vars
// HELMDECK_FIRECRAWL_ENABLED and HELMDECK_FIRECRAWL_URL to point at
// the returned test server.
func stubFirecrawlSearch(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/search" {
			http.Error(w, "bad path: "+r.URL.Path, 404)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "bad method: "+r.Method, 405)
			return
		}
		// Decode and stash the request so tests can assert on what
		// the pack actually sent.
		raw, _ := io.ReadAll(r.Body)
		var req firecrawlSearchRequest
		_ = json.Unmarshal(raw, &req)
		t.Logf("firecrawl search request: %+v", req)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func enableFirecrawlSearch(t *testing.T, url string) {
	t.Helper()
	t.Setenv("HELMDECK_FIRECRAWL_ENABLED", "true")
	t.Setenv("HELMDECK_FIRECRAWL_URL", url)
}

// runResearchDeep calls the handler directly with a hand-built
// ExecutionContext, same pattern as webtest_test.go.
func runResearchDeep(t *testing.T, disp *scriptedDispatcherWT, input string) (json.RawMessage, error) {
	t.Helper()
	pack := ResearchDeep(disp)
	ec := &packs.ExecutionContext{
		Pack:  pack,
		Input: json.RawMessage(input),
	}
	return pack.Handler(context.Background(), ec)
}

// --- tests ----------------------------------------------------------------

func TestResearchDeep_HappyPath(t *testing.T) {
	srv := stubFirecrawlSearch(t, 200, `{
		"success": true,
		"data": [
			{"url":"https://a.example","title":"A","description":"first","markdown":"source A body","metadata":{"title":"A","statusCode":200}},
			{"url":"https://b.example","title":"B","description":"second","markdown":"source B body","metadata":{"title":"B","statusCode":200}},
			{"url":"https://empty.example","title":"empty","markdown":"","metadata":{"statusCode":200}}
		]
	}`)
	enableFirecrawlSearch(t, srv.URL)
	disp := &scriptedDispatcherWT{replies: []string{
		"WebRTC uses Google congestion control (https://a.example). BBRv2 is an alternative (https://b.example).",
	}}
	raw, err := runResearchDeep(t, disp, `{"query":"webrtc congestion","model":"openai/gpt-4o-mini","limit":3}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		Query     string           `json:"query"`
		Sources   []researchSource `json:"sources"`
		Synthesis string           `json:"synthesis"`
		Model     string           `json:"model"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Query != "webrtc congestion" {
		t.Errorf("query = %q", out.Query)
	}
	// Empty-markdown source must be dropped.
	if len(out.Sources) != 2 {
		t.Errorf("sources len = %d, want 2 (empty-markdown dropped)", len(out.Sources))
	}
	if !strings.Contains(out.Synthesis, "WebRTC") {
		t.Errorf("synthesis = %q", out.Synthesis)
	}
	if out.Model != "openai/gpt-4o-mini" {
		t.Errorf("model = %q", out.Model)
	}
	// Check the dispatcher saw both sources in the user message.
	if len(disp.captured) != 1 {
		t.Fatalf("dispatcher calls = %d, want 1", len(disp.captured))
	}
	user := disp.captured[0].Messages[1].Content.Text()
	if !strings.Contains(user, "source A body") || !strings.Contains(user, "source B body") {
		t.Errorf("user message missing source bodies: %q", user)
	}
	if !strings.Contains(user, "https://a.example") {
		t.Errorf("user message missing source URL header")
	}
}

func TestResearchDeep_DisabledByDefault(t *testing.T) {
	t.Setenv("HELMDECK_FIRECRAWL_ENABLED", "") // explicitly empty
	_, err := runResearchDeep(t, &scriptedDispatcherWT{}, `{"query":"x","model":"openai/gpt-4o"}`)
	pe := &packs.PackError{}
	if !errors.As(err, &pe) || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("want invalid_input, got %v", err)
	}
	if !strings.Contains(pe.Message, "HELMDECK_FIRECRAWL_ENABLED") {
		t.Errorf("error should name the env var, got: %s", pe.Message)
	}
}

func TestResearchDeep_MissingQuery(t *testing.T) {
	srv := stubFirecrawlSearch(t, 200, `{"success":true,"data":[]}`)
	enableFirecrawlSearch(t, srv.URL)
	_, err := runResearchDeep(t, &scriptedDispatcherWT{}, `{"model":"openai/gpt-4o"}`)
	pe := &packs.PackError{}
	if !errors.As(err, &pe) || pe.Code != packs.CodeInvalidInput {
		t.Errorf("want invalid_input, got %v", err)
	}
	if !strings.Contains(pe.Message, "query") {
		t.Errorf("message = %q, want contains 'query'", pe.Message)
	}
}

func TestResearchDeep_MissingModel(t *testing.T) {
	srv := stubFirecrawlSearch(t, 200, `{"success":true,"data":[]}`)
	enableFirecrawlSearch(t, srv.URL)
	_, err := runResearchDeep(t, &scriptedDispatcherWT{}, `{"query":"x"}`)
	pe := &packs.PackError{}
	if !errors.As(err, &pe) || pe.Code != packs.CodeInvalidInput {
		t.Errorf("want invalid_input, got %v", err)
	}
	if !strings.Contains(pe.Message, "model") {
		t.Errorf("message = %q, want contains 'model'", pe.Message)
	}
}

func TestResearchDeep_LimitCap(t *testing.T) {
	// Limit 999 must be clamped to maxResearchLimit in the wire
	// request. The stub records the body it saw and the test
	// asserts on it via the server-side log — easier than
	// plumbing a shared captured state.
	var captured firecrawlSearchRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":[{"url":"https://x.example","markdown":"x"}]}`))
	}))
	defer srv.Close()
	enableFirecrawlSearch(t, srv.URL)
	disp := &scriptedDispatcherWT{replies: []string{"ok"}}
	_, err := runResearchDeep(t, disp, `{"query":"x","model":"openai/gpt-4o","limit":999}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if captured.Limit != maxResearchLimit {
		t.Errorf("wire limit = %d, want %d (capped)", captured.Limit, maxResearchLimit)
	}
}

func TestResearchDeep_FirecrawlUpstream500(t *testing.T) {
	srv := stubFirecrawlSearch(t, 500, `internal error`)
	enableFirecrawlSearch(t, srv.URL)
	_, err := runResearchDeep(t, &scriptedDispatcherWT{}, `{"query":"x","model":"openai/gpt-4o"}`)
	pe := &packs.PackError{}
	if !errors.As(err, &pe) || pe.Code != packs.CodeHandlerFailed {
		t.Errorf("want handler_failed, got %v", err)
	}
	if !strings.Contains(pe.Message, "500") {
		t.Errorf("message = %q, want contains '500'", pe.Message)
	}
}

func TestResearchDeep_FirecrawlSuccessFalse(t *testing.T) {
	srv := stubFirecrawlSearch(t, 200, `{"success":false,"error":"no results"}`)
	enableFirecrawlSearch(t, srv.URL)
	_, err := runResearchDeep(t, &scriptedDispatcherWT{}, `{"query":"x","model":"openai/gpt-4o"}`)
	pe := &packs.PackError{}
	if !errors.As(err, &pe) || pe.Code != packs.CodeHandlerFailed {
		t.Errorf("want handler_failed, got %v", err)
	}
	if !strings.Contains(pe.Message, "no results") {
		t.Errorf("message = %q, want contains 'no results'", pe.Message)
	}
}

func TestResearchDeep_AllSourcesEmptyMarkdown(t *testing.T) {
	// Every source has empty markdown — after filtering we have zero
	// usable sources. That's caller-fixable (refine the query), NOT a
	// pack bug: helmdeck searched fine, the query just yielded nothing
	// usable. invalid_input so the pipeline classifies it caller_fixable
	// instead of pack_bug ("file a GitHub issue" for a refine-your-query
	// situation).
	srv := stubFirecrawlSearch(t, 200, `{
		"success": true,
		"data": [
			{"url":"https://a.example","markdown":""},
			{"url":"https://b.example","markdown":""}
		]
	}`)
	enableFirecrawlSearch(t, srv.URL)
	_, err := runResearchDeep(t, &scriptedDispatcherWT{}, `{"query":"x","model":"openai/gpt-4o"}`)
	pe := &packs.PackError{}
	if !errors.As(err, &pe) || pe.Code != packs.CodeInvalidInput {
		t.Errorf("want invalid_input (caller_fixable), got %v", err)
	}
	if !strings.Contains(pe.Message, "no usable sources") {
		t.Errorf("message = %q, want contains 'no usable sources'", pe.Message)
	}
}

func TestResearchDeep_SynthesisDispatchFails(t *testing.T) {
	srv := stubFirecrawlSearch(t, 200, `{
		"success": true,
		"data": [{"url":"https://a.example","markdown":"body"}]
	}`)
	enableFirecrawlSearch(t, srv.URL)
	disp := &scriptedDispatcherWT{replyErr: []error{errors.New("provider quota exceeded")}}
	_, err := runResearchDeep(t, disp, `{"query":"x","model":"openai/gpt-4o"}`)
	pe := &packs.PackError{}
	if !errors.As(err, &pe) || pe.Code != packs.CodeHandlerFailed {
		t.Errorf("want handler_failed, got %v", err)
	}
	if !strings.Contains(pe.Message, "quota") {
		t.Errorf("message should propagate upstream error, got %q", pe.Message)
	}
}

func TestResearchDeep_SynthesisEmpty(t *testing.T) {
	srv := stubFirecrawlSearch(t, 200, `{
		"success": true,
		"data": [{"url":"https://a.example","markdown":"body"}]
	}`)
	enableFirecrawlSearch(t, srv.URL)
	disp := &scriptedDispatcherWT{replies: []string{"   "}} // whitespace only
	_, err := runResearchDeep(t, disp, `{"query":"x","model":"openai/gpt-4o"}`)
	pe := &packs.PackError{}
	if !errors.As(err, &pe) || pe.Code != packs.CodeHandlerFailed {
		t.Errorf("want handler_failed, got %v", err)
	}
	if !strings.Contains(pe.Message, "empty") {
		t.Errorf("message = %q, want contains 'empty'", pe.Message)
	}
}

// --- JIT length-sizing (issue #532 / convention #525) ---------------------

// TestResearchDeep_Inspect_NoFirecrawlNoDispatcher — inspect mode runs
// without Firecrawl enabled, without a dispatcher, and without a model.
// Pure cost-planning helper.
func TestResearchDeep_Inspect_NoFirecrawlNoDispatcher(t *testing.T) {
	// Explicitly DON'T enable Firecrawl. Inspect must skip the gate.
	pack := ResearchDeep(nil)
	ec := &packs.ExecutionContext{
		Pack:  pack,
		Input: json.RawMessage(`{"query":"webrtc congestion control","inspect":true,"length_intent":"exhaustive"}`),
	}
	raw, err := pack.Handler(context.Background(), ec)
	if err != nil {
		t.Fatalf("inspect with nil dispatcher + Firecrawl disabled: %v", err)
	}
	var out struct {
		Inspect             bool   `json:"inspect"`
		Query               string `json:"query"`
		SuggestedLimit      int    `json:"suggested_limit"`
		LengthIntentApplied string `json:"length_intent_applied"`
		Reason              string `json:"reason"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.Inspect {
		t.Errorf("inspect not echoed")
	}
	if out.Query != "webrtc congestion control" {
		t.Errorf("query echo = %q", out.Query)
	}
	if out.SuggestedLimit != 10 {
		t.Errorf("suggested_limit = %d, want 10 (exhaustive)", out.SuggestedLimit)
	}
	if out.LengthIntentApplied != "intent:exhaustive" {
		t.Errorf("applied = %q, want intent:exhaustive", out.LengthIntentApplied)
	}
	if !strings.Contains(out.Reason, "10") || !strings.Contains(out.Reason, "exhaustive") {
		t.Errorf("reason should mention limit + intent: %q", out.Reason)
	}
}

// TestResearchDeep_LengthIntent_ScalesByIntent — each intent maps to
// the right `limit` value (verified by inspecting the Firecrawl request).
func TestResearchDeep_LengthIntent_ScalesByIntent(t *testing.T) {
	cases := []struct {
		intent    string
		wantLimit int
	}{
		{"summary", 3},
		{"thorough", 5},
		{"exhaustive", 10},
	}
	for _, tc := range cases {
		t.Run(tc.intent, func(t *testing.T) {
			capturedLimit := -1
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				var req firecrawlSearchRequest
				_ = json.Unmarshal(raw, &req)
				capturedLimit = req.Limit
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"success":true,"data":[{"url":"https://a","markdown":"x"}]}`))
			}))
			defer srv.Close()
			enableFirecrawlSearch(t, srv.URL)
			disp := &scriptedDispatcherWT{replies: []string{"answer"}}
			raw, err := runResearchDeep(t, disp, `{
				"query":"x","model":"openai/gpt-4o","length_intent":"`+tc.intent+`"
			}`)
			if err != nil {
				t.Fatalf("handler: %v", err)
			}
			if capturedLimit != tc.wantLimit {
				t.Errorf("intent=%s: firecrawl limit = %d, want %d", tc.intent, capturedLimit, tc.wantLimit)
			}
			var out struct {
				LimitApplied        int    `json:"limit_applied"`
				LengthIntentApplied string `json:"length_intent_applied"`
			}
			_ = json.Unmarshal(raw, &out)
			if out.LimitApplied != tc.wantLimit {
				t.Errorf("output limit_applied = %d, want %d", out.LimitApplied, tc.wantLimit)
			}
			if want := "intent:" + tc.intent; out.LengthIntentApplied != want {
				t.Errorf("applied = %q, want %q", out.LengthIntentApplied, want)
			}
		})
	}
}

// TestResearchDeep_BackCompat_ExplicitLimitWins — when `limit` is set,
// it wins over `length_intent`. Existing callers see ZERO change.
func TestResearchDeep_BackCompat_ExplicitLimitWins(t *testing.T) {
	capturedLimit := -1
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req firecrawlSearchRequest
		_ = json.Unmarshal(raw, &req)
		capturedLimit = req.Limit
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":[{"url":"https://a","markdown":"x"}]}`))
	}))
	defer srv.Close()
	enableFirecrawlSearch(t, srv.URL)
	disp := &scriptedDispatcherWT{replies: []string{"answer"}}
	raw, err := runResearchDeep(t, disp, `{
		"query":"x","model":"openai/gpt-4o","limit":7,"length_intent":"summary"
	}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if capturedLimit != 7 {
		t.Errorf("explicit limit ignored: got %d, want 7", capturedLimit)
	}
	var out struct {
		LengthIntentApplied string `json:"length_intent_applied"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.LengthIntentApplied != "explicit" {
		t.Errorf("applied = %q, want explicit", out.LengthIntentApplied)
	}
}

// TestResearchDeep_BackCompat_DefaultWhenNoIntentNoLimit — no inputs →
// legacy default 5 with applied:default.
func TestResearchDeep_BackCompat_DefaultWhenNoIntentNoLimit(t *testing.T) {
	srv := stubFirecrawlSearch(t, 200, `{"success":true,"data":[{"url":"https://a","markdown":"x"}]}`)
	enableFirecrawlSearch(t, srv.URL)
	disp := &scriptedDispatcherWT{replies: []string{"answer"}}
	raw, err := runResearchDeep(t, disp, `{"query":"x","model":"openai/gpt-4o"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		LimitApplied        int    `json:"limit_applied"`
		LengthIntentApplied string `json:"length_intent_applied"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.LimitApplied != defaultResearchLimit {
		t.Errorf("limit_applied = %d, want %d (legacy default)", out.LimitApplied, defaultResearchLimit)
	}
	if out.LengthIntentApplied != "default" {
		t.Errorf("applied = %q, want default", out.LengthIntentApplied)
	}
}

// TestResearchDeep_Truncated_FinishReasonLength — synthesis LLM hitting
// finish_reason=length surfaces as truncated:true so the agent can
// retry with a smaller intent or larger max_tokens.
func TestResearchDeep_Truncated_FinishReasonLength(t *testing.T) {
	srv := stubFirecrawlSearch(t, 200, `{"success":true,"data":[{"url":"https://a","markdown":"x"}]}`)
	enableFirecrawlSearch(t, srv.URL)
	disp := &scriptedDispatcherWT{
		replies:       []string{"answer continues but"},
		finishReasons: []string{"length"},
	}
	raw, err := runResearchDeep(t, disp, `{"query":"x","model":"openai/gpt-4o"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		Truncated bool `json:"truncated"`
	}
	_ = json.Unmarshal(raw, &out)
	if !out.Truncated {
		t.Error("finish_reason=length should set truncated:true")
	}
}

// TestResearchDeep_OutputMetricsAlwaysPresent — JIT fields land on
// every generate response so callers can rely on them.
func TestResearchDeep_OutputMetricsAlwaysPresent(t *testing.T) {
	srv := stubFirecrawlSearch(t, 200, `{"success":true,"data":[{"url":"https://a","markdown":"x"}]}`)
	enableFirecrawlSearch(t, srv.URL)
	disp := &scriptedDispatcherWT{replies: []string{"answer."}}
	raw, err := runResearchDeep(t, disp, `{"query":"x","model":"openai/gpt-4o"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	for _, k := range []string{"limit_applied", "sources_used", "length_intent_applied", "truncated"} {
		if _, ok := out[k]; !ok {
			t.Errorf("missing JIT metric %q in output: %v", k, out)
		}
	}
}

// TestResearchDeep_ResolveSize_Precedence — unit-level resolver
// precedence pinned in code so the rule doesn't drift from docs.
func TestResearchDeep_ResolveSize_Precedence(t *testing.T) {
	// (1) explicit wins.
	in := researchDeepInput{Limit: 7, LengthIntent: "summary"}
	if size := resolveResearchDeepSize(&in); size.limit != 7 || size.applied != "explicit" {
		t.Errorf("explicit: limit=%d applied=%q", size.limit, size.applied)
	}
	// (2) explicit clamped to ceiling.
	in = researchDeepInput{Limit: 50}
	if size := resolveResearchDeepSize(&in); size.limit != maxResearchLimit {
		t.Errorf("explicit clamp: limit=%d, want %d", size.limit, maxResearchLimit)
	}
	// (3) intent.
	in = researchDeepInput{LengthIntent: "summary"}
	if size := resolveResearchDeepSize(&in); size.limit != 3 || size.applied != "intent:summary" {
		t.Errorf("intent: limit=%d applied=%q", size.limit, size.applied)
	}
	// (4) default.
	in = researchDeepInput{}
	size := resolveResearchDeepSize(&in)
	if size.limit != defaultResearchLimit || size.applied != "default" {
		t.Errorf("default: limit=%d applied=%q", size.limit, size.applied)
	}
}

// TestResearchDeep_UnknownIntentFallsBack — misspelled intent falls
// back to thorough rather than erroring.
func TestResearchDeep_UnknownIntentFallsBack(t *testing.T) {
	in := researchDeepInput{LengthIntent: "deeper-than-deep-dive"}
	size := resolveResearchDeepSize(&in)
	if size.applied != "intent:thorough" {
		t.Errorf("applied = %q, want intent:thorough", size.applied)
	}
}
