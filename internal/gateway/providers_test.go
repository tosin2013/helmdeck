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

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace/noop"
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
		Messages: []Message{{Role: "user", Content: TextContent("hello")}},
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
	if resp.Choices[0].Message.Content.Text() != "hi" || resp.Usage.TotalTokens != 7 {
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
			{Role: "system", Content: TextContent("be terse")},
			{Role: "system", Content: TextContent("be kind")},
			{Role: "user", Content: TextContent("hi")},
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
	if resp.Choices[0].Message.Content.Text() != "hello world" {
		t.Errorf("text blocks not joined: %q", resp.Choices[0].Message.Content.Text())
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
			{Role: "system", Content: TextContent("sys")},
			{Role: "user", Content: TextContent("hi")},
			{Role: "assistant", Content: TextContent("earlier")},
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
	if resp.Choices[0].Message.Content.Text() != "yo" || resp.Usage.TotalTokens != 4 {
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
		Messages: []Message{{Role: "user", Content: TextContent("yo")}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if resp.Choices[0].Message.Content.Text() != "hey" || resp.Choices[0].FinishReason != "stop" {
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
		Messages: []Message{{Role: "user", Content: TextContent("hi")}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if atomic.LoadInt32(&calls) != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
	if resp.Choices[0].Message.Content.Text() != "ok" {
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
		Messages: []Message{{Role: "user", Content: TextContent("hi")}},
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
		Messages: []Message{{Role: "user", Content: TextContent("hi")}},
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
		Messages: []Message{{Role: "user", Content: TextContent("hi")}},
	})
	if err == nil {
		t.Fatal("expected context error")
	}
}

// Multimodal (T407 prep) tests — verify each provider correctly
// translates a multipart Message.Content into its native upstream
// shape. The image part is a tiny inline data URL so the assertions
// are deterministic across providers.

const testDataURL = "data:image/png;base64,iVBORw0KGgo="

func TestOpenAIProviderMultipartPassThrough(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"saw it"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	p := NewOpenAIProvider(OpenAIConfig{APIKey: "k", BaseURL: srv.URL, Models: []string{"gpt-4o"}})
	_, err := p.ChatCompletion(context.Background(), ChatRequest{
		Model: "gpt-4o",
		Messages: []Message{{
			Role: "user",
			Content: MultipartContent(
				TextPart("what is in this image?"),
				ImageURLPartFromURL(testDataURL),
			),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// OpenAI passes the multipart array straight through.
	if !strings.Contains(gotBody, `"image_url"`) || !strings.Contains(gotBody, testDataURL) {
		t.Errorf("openai body missing image_url part: %s", gotBody)
	}
}

func TestAnthropicProviderMultipartTranslation(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{"id":"m","type":"message","role":"assistant","model":"claude","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()

	p := NewAnthropicProvider(AnthropicConfig{APIKey: "k", BaseURL: srv.URL, Models: []string{"claude-sonnet-4-6"}})
	_, err := p.ChatCompletion(context.Background(), ChatRequest{
		Model: "claude-sonnet-4-6",
		Messages: []Message{{
			Role: "user",
			Content: MultipartContent(
				TextPart("describe"),
				ImageURLPartFromURL(testDataURL),
			),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Anthropic must emit a typed block array with a base64 image source.
	if !strings.Contains(gotBody, `"type":"image"`) || !strings.Contains(gotBody, `"media_type":"image/png"`) {
		t.Errorf("anthropic body missing image block: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"data":"iVBORw0KGgo="`) {
		t.Errorf("anthropic body missing base64 payload: %s", gotBody)
	}
}

func TestGeminiProviderMultipartTranslation(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`))
	}))
	defer srv.Close()

	p := NewGeminiProvider(GeminiConfig{APIKey: "k", BaseURL: srv.URL, Models: []string{"gemini-2.5-flash"}})
	_, err := p.ChatCompletion(context.Background(), ChatRequest{
		Model: "gemini-2.5-flash",
		Messages: []Message{{
			Role: "user",
			Content: MultipartContent(
				TextPart("ocr this"),
				ImageURLPartFromURL(testDataURL),
			),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotBody, `"inlineData"`) || !strings.Contains(gotBody, `"mimeType":"image/png"`) {
		t.Errorf("gemini body missing inlineData: %s", gotBody)
	}
}

func TestOllamaProviderMultipartImagesField(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{"model":"llava","message":{"role":"assistant","content":"ok"},"done":true,"prompt_eval_count":1,"eval_count":1}`))
	}))
	defer srv.Close()

	p := NewOllamaProvider(OllamaConfig{BaseURL: srv.URL, Models: []string{"llava"}})
	_, err := p.ChatCompletion(context.Background(), ChatRequest{
		Model: "llava",
		Messages: []Message{{
			Role: "user",
			Content: MultipartContent(
				TextPart("what is this"),
				ImageURLPartFromURL(testDataURL),
			),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Ollama wants a top-level images array per message and the
	// content as a plain text string — NOT a parts array.
	if !strings.Contains(gotBody, `"images":["iVBORw0KGgo="]`) {
		t.Errorf("ollama body missing images field: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"content":"what is this"`) {
		t.Errorf("ollama content should be the plain text: %s", gotBody)
	}
}

// T510 — verify the dispatcher emits an OTel span with the GenAI
// semantic-convention attributes for a successful provider call.
// Uses a stub provider so the test doesn't depend on a real backend.
func TestRegistryDispatch_EmitsGenAISpan(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(noop.NewTracerProvider()) })

	stub := &scriptedProvider{
		name: "openai",
		queue: []scriptedReply{{
			resp: ChatResponse{
				Model: "gpt-4o",
				Usage: Usage{PromptTokens: 12, CompletionTokens: 8, TotalTokens: 20},
				Choices: []Choice{{
					Index:        0,
					Message:      Message{Role: "assistant", Content: TextContent("hi")},
					FinishReason: "stop",
				}},
			},
		}},
	}
	reg := NewRegistry()
	reg.Register(stub)

	maxTok := 100
	_, err := reg.Dispatch(context.Background(), ChatRequest{
		Model:     "openai/gpt-4o",
		MaxTokens: &maxTok,
		Messages:  []Message{{Role: "user", Content: TextContent("hello")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	span := spans[0]
	if span.Name != "gen_ai.chat" {
		t.Errorf("span name = %s", span.Name)
	}
	got := map[string]any{}
	for _, a := range span.Attributes {
		got[string(a.Key)] = a.Value.AsInterface()
	}
	if got["gen_ai.system"] != "openai" {
		t.Errorf("gen_ai.system = %v", got["gen_ai.system"])
	}
	if got["gen_ai.request.model"] != "gpt-4o" {
		t.Errorf("gen_ai.request.model = %v", got["gen_ai.request.model"])
	}
	if got["gen_ai.usage.input_tokens"] != int64(12) {
		t.Errorf("input tokens = %v", got["gen_ai.usage.input_tokens"])
	}
	if got["gen_ai.usage.output_tokens"] != int64(8) {
		t.Errorf("output tokens = %v", got["gen_ai.usage.output_tokens"])
	}
	if got["gen_ai.request.max_tokens"] != int64(100) {
		t.Errorf("max tokens = %v", got["gen_ai.request.max_tokens"])
	}
}

// --- T807f tool-use coverage --------------------------------------------
//
// Three providers (Anthropic, OpenAI, Gemini) gained native tool-use
// request translation + response parsing. Ollama intentionally stays
// tool-agnostic; Deepseek inherits the OpenAI adapter so its coverage
// rides on the OpenAI tests. Each test below asserts:
//   (1) the outbound wire format matches the provider's native shape
//   (2) a response containing tool calls becomes a MultipartContent
//       message carrying ToolUsePart blocks on ContentPart union

// computerUseTool is a canned ToolDefinition used across the tool-use
// tests so each provider sees the same input. InputSchema is a tiny
// JSON Schema object the adapter should forward verbatim — we parse
// the outbound body and assert the same bytes make it through.
func computerUseTool() ToolDefinition {
	return ToolDefinition{
		Name:        "click_at",
		Description: "Click at the given screen coordinates",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"x":{"type":"integer"},"y":{"type":"integer"}},"required":["x","y"]}`),
	}
}

func TestAnthropicProvider_ToolUseRequestAndResponse(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{
			"id":"msg_t1","type":"message","role":"assistant","model":"claude-opus-4-6",
			"content":[
				{"type":"text","text":"I'll click the button."},
				{"type":"tool_use","id":"toolu_01AB","name":"click_at","input":{"x":100,"y":200}}
			],
			"stop_reason":"tool_use",
			"usage":{"input_tokens":20,"output_tokens":15}
		}`))
	}))
	defer srv.Close()

	p := NewAnthropicProvider(AnthropicConfig{APIKey: "ak", BaseURL: srv.URL})
	resp, err := p.ChatCompletion(context.Background(), ChatRequest{
		Model:      "claude-opus-4-6",
		Messages:   []Message{{Role: "user", Content: TextContent("click the button")}},
		Tools:      []ToolDefinition{computerUseTool()},
		ToolChoice: &ToolChoice{Mode: "auto"},
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	// Outbound wire shape: top-level tools[] with input_schema forwarded
	// byte-for-byte; tool_choice object with type=auto.
	if !strings.Contains(gotBody, `"tools":[{"name":"click_at"`) {
		t.Errorf("missing tools[] in body: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"input_schema":{"type":"object"`) {
		t.Errorf("missing input_schema forward: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"tool_choice":{"type":"auto"}`) {
		t.Errorf("missing tool_choice: %s", gotBody)
	}
	// Response: choices[0] message must be multipart with one text
	// and one tool_use part.
	parts := resp.Choices[0].Message.Content.Parts()
	if len(parts) != 2 {
		t.Fatalf("parts len = %d, want 2: %+v", len(parts), parts)
	}
	if parts[0].Type != ContentPartText || parts[0].Text != "I'll click the button." {
		t.Errorf("text part wrong: %+v", parts[0])
	}
	if parts[1].Type != ContentPartToolUse || parts[1].ToolName != "click_at" || parts[1].ToolUseID != "toolu_01AB" {
		t.Errorf("tool_use part wrong: %+v", parts[1])
	}
	if !strings.Contains(string(parts[1].ToolInput), `"x":100`) {
		t.Errorf("tool input not forwarded: %s", string(parts[1].ToolInput))
	}
}

func TestAnthropicProvider_ToolChoiceVariants(t *testing.T) {
	cases := []struct {
		name string
		in   *ToolChoice
		want string
	}{
		{"auto", &ToolChoice{Mode: "auto"}, `"tool_choice":{"type":"auto"}`},
		{"any", &ToolChoice{Mode: "any"}, `"tool_choice":{"type":"any"}`},
		{"required maps to any", &ToolChoice{Mode: "required"}, `"tool_choice":{"type":"any"}`},
		{"none", &ToolChoice{Mode: "none"}, `"tool_choice":{"type":"none"}`},
		{"tool", &ToolChoice{Mode: "tool", Name: "click_at"}, `"tool_choice":{"type":"tool","name":"click_at"}`},
		{"unknown mode falls back to auto", &ToolChoice{Mode: "whatever"}, `"tool_choice":{"type":"auto"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				gotBody = string(b)
				_, _ = w.Write([]byte(`{"id":"x","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
			}))
			defer srv.Close()
			p := NewAnthropicProvider(AnthropicConfig{APIKey: "k", BaseURL: srv.URL})
			_, err := p.ChatCompletion(context.Background(), ChatRequest{
				Model:      "claude-opus-4-6",
				Messages:   []Message{{Role: "user", Content: TextContent("hi")}},
				Tools:      []ToolDefinition{computerUseTool()},
				ToolChoice: tc.in,
			})
			if err != nil {
				t.Fatalf("ChatCompletion: %v", err)
			}
			if !strings.Contains(gotBody, tc.want) {
				t.Errorf("body = %s\nwant contains %s", gotBody, tc.want)
			}
		})
	}
}

func TestAnthropicProvider_ToolResultRoundTrip(t *testing.T) {
	// Second turn of a multi-turn conversation: we echo the
	// assistant's prior tool_use back to Anthropic and follow with
	// a user-role tool_result block. Confirms anthropicContent
	// emits the correct block shapes.
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{"id":"x","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()
	p := NewAnthropicProvider(AnthropicConfig{APIKey: "k", BaseURL: srv.URL})
	_, err := p.ChatCompletion(context.Background(), ChatRequest{
		Model: "claude-opus-4-6",
		Messages: []Message{
			{Role: "user", Content: TextContent("click the button")},
			{Role: "assistant", Content: MultipartContent(
				TextPart("sure, clicking"),
				ToolUsePart("toolu_01AB", "click_at", json.RawMessage(`{"x":100,"y":200}`)),
			)},
			{Role: "user", Content: MultipartContent(
				ToolResultPart("toolu_01AB", json.RawMessage(`"clicked at 100,200"`), false),
			)},
		},
		Tools: []ToolDefinition{computerUseTool()},
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	// Must contain the assistant's tool_use block AND the user's
	// tool_result block in the outbound messages[] array.
	if !strings.Contains(gotBody, `"type":"tool_use","id":"toolu_01AB","name":"click_at"`) {
		t.Errorf("tool_use not echoed back: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"type":"tool_result","tool_use_id":"toolu_01AB"`) {
		t.Errorf("tool_result not emitted: %s", gotBody)
	}
}

func TestOpenAIProvider_ToolUseRequestAndResponse(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-t1","object":"chat.completion","created":1700000000,"model":"gpt-4o",
			"choices":[{
				"index":0,
				"message":{
					"role":"assistant",
					"content":null,
					"tool_calls":[{
						"id":"call_abc","type":"function",
						"function":{"name":"click_at","arguments":"{\"x\":50,\"y\":60}"}
					}]
				},
				"finish_reason":"tool_calls"
			}],
			"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}
		}`))
	}))
	defer srv.Close()

	p := NewOpenAIProvider(OpenAIConfig{APIKey: "sk", BaseURL: srv.URL})
	resp, err := p.ChatCompletion(context.Background(), ChatRequest{
		Model:      "gpt-4o",
		Messages:   []Message{{Role: "user", Content: TextContent("click it")}},
		Tools:      []ToolDefinition{computerUseTool()},
		ToolChoice: &ToolChoice{Mode: "auto"},
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	// Outbound wire shape: tools[] with function envelope, tool_choice
	// as the string "auto".
	if !strings.Contains(gotBody, `"type":"function"`) || !strings.Contains(gotBody, `"name":"click_at"`) {
		t.Errorf("tools envelope missing: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"tool_choice":"auto"`) {
		t.Errorf("tool_choice wrong: %s", gotBody)
	}
	// Response: choices[0] message becomes multipart with a single
	// tool_use part (content is null so no text block).
	parts := resp.Choices[0].Message.Content.Parts()
	if len(parts) != 1 {
		t.Fatalf("parts len = %d, want 1: %+v", len(parts), parts)
	}
	if parts[0].Type != ContentPartToolUse {
		t.Errorf("part type = %q, want tool_use", parts[0].Type)
	}
	if parts[0].ToolUseID != "call_abc" || parts[0].ToolName != "click_at" {
		t.Errorf("tool_use wrong: %+v", parts[0])
	}
	// The arguments string must flow through to ToolInput verbatim
	// so downstream code can json.Unmarshal it identically across
	// providers.
	if !strings.Contains(string(parts[0].ToolInput), `"x":50`) {
		t.Errorf("tool input = %s", string(parts[0].ToolInput))
	}
}

func TestOpenAIProvider_ToolChoiceVariants(t *testing.T) {
	cases := []struct {
		name string
		in   *ToolChoice
		want string
	}{
		{"auto", &ToolChoice{Mode: "auto"}, `"tool_choice":"auto"`},
		{"required", &ToolChoice{Mode: "required"}, `"tool_choice":"required"`},
		{"any maps to required", &ToolChoice{Mode: "any"}, `"tool_choice":"required"`},
		{"none", &ToolChoice{Mode: "none"}, `"tool_choice":"none"`},
		{"tool", &ToolChoice{Mode: "tool", Name: "click_at"}, `"tool_choice":{"function":{"name":"click_at"},"type":"function"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				gotBody = string(b)
				_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
			}))
			defer srv.Close()
			p := NewOpenAIProvider(OpenAIConfig{APIKey: "k", BaseURL: srv.URL})
			_, err := p.ChatCompletion(context.Background(), ChatRequest{
				Model:      "gpt-4o",
				Messages:   []Message{{Role: "user", Content: TextContent("hi")}},
				Tools:      []ToolDefinition{computerUseTool()},
				ToolChoice: tc.in,
			})
			if err != nil {
				t.Fatalf("ChatCompletion: %v", err)
			}
			if !strings.Contains(gotBody, tc.want) {
				t.Errorf("body = %s\nwant contains %s", gotBody, tc.want)
			}
		})
	}
}

func TestOpenAIProvider_ToolResultMessageSplit(t *testing.T) {
	// Assistant tool_use + user tool_result round-trip: OpenAI uses
	// role="tool" with ToolCallID for the result block, distinct
	// from the assistant message that carried the tool_calls array.
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer srv.Close()
	p := NewOpenAIProvider(OpenAIConfig{APIKey: "k", BaseURL: srv.URL})
	_, err := p.ChatCompletion(context.Background(), ChatRequest{
		Model: "gpt-4o",
		Messages: []Message{
			{Role: "user", Content: TextContent("click it")},
			{Role: "assistant", Content: MultipartContent(
				ToolUsePart("call_abc", "click_at", json.RawMessage(`{"x":50,"y":60}`)),
			)},
			{Role: "user", Content: MultipartContent(
				ToolResultPart("call_abc", json.RawMessage(`"clicked"`), false),
			)},
		},
		Tools: []ToolDefinition{computerUseTool()},
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	// Assistant message with tool_calls.
	if !strings.Contains(gotBody, `"tool_calls":[{"id":"call_abc"`) {
		t.Errorf("assistant tool_calls missing: %s", gotBody)
	}
	// Separate role=tool message for the result.
	if !strings.Contains(gotBody, `"role":"tool","content":"\"clicked\"","tool_call_id":"call_abc"`) &&
		!strings.Contains(gotBody, `"role":"tool"`) {
		t.Errorf("tool-role result message missing: %s", gotBody)
	}
}

func TestGeminiProvider_ToolUseRequestAndResponse(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{
			"candidates":[{
				"content":{
					"role":"model",
					"parts":[
						{"text":"clicking now"},
						{"functionCall":{"name":"click_at","args":{"x":10,"y":20}}}
					]
				},
				"finishReason":"STOP",
				"index":0
			}],
			"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":4,"totalTokenCount":9}
		}`))
	}))
	defer srv.Close()

	p := NewGeminiProvider(GeminiConfig{APIKey: "gk", BaseURL: srv.URL})
	resp, err := p.ChatCompletion(context.Background(), ChatRequest{
		Model:      "gemini-3-flash-preview",
		Messages:   []Message{{Role: "user", Content: TextContent("click")}},
		Tools:      []ToolDefinition{computerUseTool()},
		ToolChoice: &ToolChoice{Mode: "auto"},
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	// Outbound wire shape: tools[0].functionDeclarations[0] with
	// name + parameters, toolConfig.functionCallingConfig.mode = AUTO.
	if !strings.Contains(gotBody, `"functionDeclarations":[{"name":"click_at"`) {
		t.Errorf("functionDeclarations missing: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"parameters":{"type":"object"`) {
		t.Errorf("parameters forward missing: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"mode":"AUTO"`) {
		t.Errorf("functionCallingConfig mode wrong: %s", gotBody)
	}
	// Response: multipart with text + tool_use. The tool_use id is
	// synthesized ("gemini-call-<name>") since Gemini doesn't return
	// a call id; downstream pack code pairs tool_use → tool_result
	// via this id.
	parts := resp.Choices[0].Message.Content.Parts()
	if len(parts) != 2 {
		t.Fatalf("parts len = %d, want 2: %+v", len(parts), parts)
	}
	if parts[0].Type != ContentPartText || parts[0].Text != "clicking now" {
		t.Errorf("text part wrong: %+v", parts[0])
	}
	if parts[1].Type != ContentPartToolUse || parts[1].ToolName != "click_at" {
		t.Errorf("tool_use part wrong: %+v", parts[1])
	}
	if parts[1].ToolUseID != "gemini-call-click_at" {
		t.Errorf("synthesized id wrong: %s", parts[1].ToolUseID)
	}
	if !strings.Contains(string(parts[1].ToolInput), `"x":10`) {
		t.Errorf("tool args = %s", string(parts[1].ToolInput))
	}
}

func TestGeminiProvider_ToolConfigVariants(t *testing.T) {
	cases := []struct {
		name string
		in   *ToolChoice
		want string
	}{
		{"auto", &ToolChoice{Mode: "auto"}, `"mode":"AUTO"`},
		{"any", &ToolChoice{Mode: "any"}, `"mode":"ANY"`},
		{"required maps to any", &ToolChoice{Mode: "required"}, `"mode":"ANY"`},
		{"none", &ToolChoice{Mode: "none"}, `"mode":"NONE"`},
		{"tool with allowed name", &ToolChoice{Mode: "tool", Name: "click_at"}, `"mode":"ANY","allowedFunctionNames":["click_at"]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				gotBody = string(b)
				_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`))
			}))
			defer srv.Close()
			p := NewGeminiProvider(GeminiConfig{APIKey: "gk", BaseURL: srv.URL})
			_, err := p.ChatCompletion(context.Background(), ChatRequest{
				Model:      "gemini-3-flash-preview",
				Messages:   []Message{{Role: "user", Content: TextContent("hi")}},
				Tools:      []ToolDefinition{computerUseTool()},
				ToolChoice: tc.in,
			})
			if err != nil {
				t.Fatalf("ChatCompletion: %v", err)
			}
			if !strings.Contains(gotBody, tc.want) {
				t.Errorf("body = %s\nwant contains %s", gotBody, tc.want)
			}
		})
	}
}

func TestGeminiProvider_FunctionResponseRoundTrip(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"done"}]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`))
	}))
	defer srv.Close()
	p := NewGeminiProvider(GeminiConfig{APIKey: "gk", BaseURL: srv.URL})
	_, err := p.ChatCompletion(context.Background(), ChatRequest{
		Model: "gemini-3-flash-preview",
		Messages: []Message{
			{Role: "user", Content: TextContent("click")},
			{Role: "assistant", Content: MultipartContent(
				ToolUsePart("gemini-call-click_at", "click_at", json.RawMessage(`{"x":10,"y":20}`)),
			)},
			{Role: "user", Content: MultipartContent(
				ToolResultPart("gemini-call-click_at", json.RawMessage(`{"status":"ok"}`), false),
			)},
		},
		Tools: []ToolDefinition{computerUseTool()},
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	// Assistant echo must emit functionCall.
	if !strings.Contains(gotBody, `"functionCall":{"name":"click_at"`) {
		t.Errorf("functionCall echo missing: %s", gotBody)
	}
	// User result must emit functionResponse, with the "gemini-call-"
	// prefix stripped so the name matches the declaration.
	if !strings.Contains(gotBody, `"functionResponse":{"name":"click_at"`) {
		t.Errorf("functionResponse missing: %s", gotBody)
	}
}

func TestToolsOmittedByDefault(t *testing.T) {
	// Backward compat: a ChatRequest with no Tools/ToolChoice must
	// produce wire bodies byte-identical to the pre-T807f shape for
	// each provider. Legacy callers keep working.
	check := func(t *testing.T, body string, forbidden ...string) {
		t.Helper()
		for _, s := range forbidden {
			if strings.Contains(body, s) {
				t.Errorf("body contains forbidden %q: %s", s, body)
			}
		}
	}

	t.Run("anthropic", func(t *testing.T) {
		var gotBody string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
			_, _ = w.Write([]byte(`{"id":"x","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
		}))
		defer srv.Close()
		p := NewAnthropicProvider(AnthropicConfig{APIKey: "k", BaseURL: srv.URL})
		_, _ = p.ChatCompletion(context.Background(), ChatRequest{
			Model:    "claude-opus-4-6",
			Messages: []Message{{Role: "user", Content: TextContent("hi")}},
		})
		check(t, gotBody, `"tools"`, `"tool_choice"`)
	})

	t.Run("openai", func(t *testing.T) {
		var gotBody string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
			_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
		}))
		defer srv.Close()
		p := NewOpenAIProvider(OpenAIConfig{APIKey: "k", BaseURL: srv.URL})
		_, _ = p.ChatCompletion(context.Background(), ChatRequest{
			Model:    "gpt-4o",
			Messages: []Message{{Role: "user", Content: TextContent("hi")}},
		})
		check(t, gotBody, `"tools"`, `"tool_choice"`)
	})

	t.Run("gemini", func(t *testing.T) {
		var gotBody string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			gotBody = string(b)
			_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`))
		}))
		defer srv.Close()
		p := NewGeminiProvider(GeminiConfig{APIKey: "k", BaseURL: srv.URL})
		_, _ = p.ChatCompletion(context.Background(), ChatRequest{
			Model:    "gemini-3-flash-preview",
			Messages: []Message{{Role: "user", Content: TextContent("hi")}},
		})
		check(t, gotBody, `"tools"`, `"toolConfig"`)
	})
}
