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
