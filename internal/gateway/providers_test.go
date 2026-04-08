package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOpenAIProviderHappyPath(t *testing.T) {
	var gotPath, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-x","object":"chat.completion","created":1700000000,"model":"gpt-4o",
			"choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}
		}`))
	}))
	defer srv.Close()

	p := NewOpenAIProvider(OpenAIConfig{APIKey: "sk-test", BaseURL: srv.URL, Models: []string{"gpt-4o"}})
	resp, err := p.ChatCompletion(context.Background(), ChatRequest{
		Model:    "gpt-4o",
		Messages: []Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("auth = %q", gotAuth)
	}
	if !strings.Contains(gotBody, `"model":"gpt-4o"`) {
		t.Errorf("body missing model: %s", gotBody)
	}
	if resp.Choices[0].Message.Content != "hi" || resp.Usage.TotalTokens != 7 {
		t.Errorf("unexpected response: %+v", resp)
	}

	models, _ := p.Models(context.Background())
	if len(models) != 1 || models[0] != "gpt-4o" {
		t.Errorf("Models() = %v", models)
	}
}

func TestDeepseekDefaultsName(t *testing.T) {
	p := NewDeepseekProvider(OpenAIConfig{APIKey: "k"})
	if p.Name() != "deepseek" {
		t.Errorf("name = %q", p.Name())
	}
}

func TestAnthropicProviderTranslation(t *testing.T) {
	var gotBody, gotKey, gotVer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		gotVer = r.Header.Get("anthropic-version")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{
			"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-6",
			"content":[{"type":"text","text":"hello "},{"type":"text","text":"world"}],
			"stop_reason":"end_turn",
			"usage":{"input_tokens":10,"output_tokens":4}
		}`))
	}))
	defer srv.Close()

	p := NewAnthropicProvider(AnthropicConfig{APIKey: "ak", BaseURL: srv.URL, Models: []string{"claude-sonnet-4-6"}})
	resp, err := p.ChatCompletion(context.Background(), ChatRequest{
		Model: "claude-sonnet-4-6",
		Messages: []Message{
			{Role: "system", Content: "be terse"},
			{Role: "system", Content: "be kind"},
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if gotKey != "ak" || gotVer != "2023-06-01" {
		t.Errorf("headers wrong: key=%q ver=%q", gotKey, gotVer)
	}
	// system messages must move to the top-level system field, not the
	// messages array
	if !strings.Contains(gotBody, `"system":"be terse\n\nbe kind"`) {
		t.Errorf("system not concatenated: %s", gotBody)
	}
	if strings.Contains(gotBody, `"role":"system"`) {
		t.Errorf("system leaked into messages array: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"max_tokens":4096`) {
		t.Errorf("default max_tokens missing: %s", gotBody)
	}
	if resp.Choices[0].Message.Content != "hello world" {
		t.Errorf("text blocks not joined: %q", resp.Choices[0].Message.Content)
	}
	if resp.Usage.PromptTokens != 10 || resp.Usage.CompletionTokens != 4 || resp.Usage.TotalTokens != 14 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestGeminiProviderURLAndShape(t *testing.T) {
	var gotPath, gotQuery, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{
			"candidates":[{"content":{"role":"model","parts":[{"text":"yo"}]},"finishReason":"STOP","index":0}],
			"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1,"totalTokenCount":4}
		}`))
	}))
	defer srv.Close()

	p := NewGeminiProvider(GeminiConfig{APIKey: "gk", BaseURL: srv.URL, Models: []string{"gemini-2.0-flash"}})
	resp, err := p.ChatCompletion(context.Background(), ChatRequest{
		Model: "gemini-2.0-flash",
		Messages: []Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "earlier"},
		},
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if gotPath != "/v1beta/models/gemini-2.0-flash:generateContent" {
		t.Errorf("path = %q", gotPath)
	}
	if gotQuery != "key=gk" {
		t.Errorf("query = %q", gotQuery)
	}
	if !strings.Contains(gotBody, `"systemInstruction"`) {
		t.Errorf("system not lifted: %s", gotBody)
	}
	// assistant role must be remapped to "model"
	if !strings.Contains(gotBody, `"role":"model"`) {
		t.Errorf("assistant not remapped: %s", gotBody)
	}
	if resp.Choices[0].Message.Content != "yo" || resp.Usage.TotalTokens != 4 {
		t.Errorf("response = %+v", resp)
	}
}

func TestOllamaProviderHappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("path = %q", r.URL.Path)
		}
		var got ollamaRequest
		_ = json.NewDecoder(r.Body).Decode(&got)
		if got.Stream {
			t.Error("stream must be false")
		}
		_, _ = w.Write([]byte(`{
			"model":"llama3","created_at":"2026-01-01T00:00:00Z",
			"message":{"role":"assistant","content":"hey"},"done":true,
			"prompt_eval_count":4,"eval_count":2
		}`))
	}))
	defer srv.Close()

	p := NewOllamaProvider(OllamaConfig{BaseURL: srv.URL, Models: []string{"llama3"}})
	resp, err := p.ChatCompletion(context.Background(), ChatRequest{
		Model:    "llama3",
		Messages: []Message{{Role: "user", Content: "yo"}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if resp.Choices[0].Message.Content != "hey" || resp.Choices[0].FinishReason != "stop" {
		t.Errorf("resp = %+v", resp)
	}
	if resp.Usage.TotalTokens != 6 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestRetryOn5xxThenSuccess(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("upstream busy"))
			return
		}
		_, _ = w.Write([]byte(`{
			"id":"x","object":"chat.completion","created":1,"model":"m",
			"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}
		}`))
	}))
	defer srv.Close()

	p := NewOpenAIProvider(OpenAIConfig{
		APIKey: "k", BaseURL: srv.URL,
		Retry: RetryPolicy{MaxAttempts: 3, BaseDelay: 1 * time.Millisecond, MaxDelay: 5 * time.Millisecond},
	})
	resp, err := p.ChatCompletion(context.Background(), ChatRequest{
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if atomic.LoadInt32(&calls) != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
	if resp.Choices[0].Message.Content != "ok" {
		t.Errorf("resp = %+v", resp)
	}
}

func TestNoRetryOn4xx(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad key"}`))
	}))
	defer srv.Close()

	p := NewOpenAIProvider(OpenAIConfig{
		APIKey: "k", BaseURL: srv.URL,
		Retry: RetryPolicy{MaxAttempts: 5, BaseDelay: 1 * time.Millisecond, MaxDelay: 5 * time.Millisecond},
	})
	_, err := p.ChatCompletion(context.Background(), ChatRequest{
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var perr *providerError
	if !errors.As(err, &perr) || perr.StatusCode != 401 {
		t.Errorf("err = %v", err)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("expected 1 call (no retry on 4xx), got %d", calls)
	}
}

func TestRetryExhaustion(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	p := NewOpenAIProvider(OpenAIConfig{
		APIKey: "k", BaseURL: srv.URL,
		Retry: RetryPolicy{MaxAttempts: 3, BaseDelay: 1 * time.Millisecond, MaxDelay: 2 * time.Millisecond},
	})
	_, err := p.ChatCompletion(context.Background(), ChatRequest{
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if atomic.LoadInt32(&calls) != 3 {
		t.Errorf("expected 3 attempts, got %d", calls)
	}
}

func TestContextCancelDuringBackoff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	p := NewOpenAIProvider(OpenAIConfig{
		APIKey: "k", BaseURL: srv.URL,
		Retry: RetryPolicy{MaxAttempts: 5, BaseDelay: 200 * time.Millisecond, MaxDelay: 1 * time.Second},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := p.ChatCompletion(ctx, ChatRequest{
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected context error")
	}
}
