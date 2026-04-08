package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type stubProvider struct {
	name   string
	models []string
	last   ChatRequest
	err    error
}

func (s *stubProvider) Name() string { return s.name }

func (s *stubProvider) Models(ctx context.Context) ([]string, error) {
	return s.models, nil
}

func (s *stubProvider) ChatCompletion(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	s.last = req
	if s.err != nil {
		return ChatResponse{}, s.err
	}
	return ChatResponse{
		Choices: []Choice{{
			Index:        0,
			Message:      Message{Role: "assistant", Content: "hi from " + s.name + " using " + req.Model},
			FinishReason: "stop",
		}},
		Usage: Usage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3},
	}, nil
}

func TestSplitModel(t *testing.T) {
	cases := []struct {
		in        string
		wantProv  string
		wantModel string
		wantErr   bool
	}{
		{"anthropic/claude-sonnet-4-6", "anthropic", "claude-sonnet-4-6", false},
		{"ollama/library/llama3", "ollama", "library/llama3", false},
		{"", "", "", true},
		{"noprefix", "", "", true},
		{"/leading", "", "", true},
		{"trailing/", "", "", true},
	}
	for _, tc := range cases {
		p, m, err := SplitModel(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("SplitModel(%q) expected error", tc.in)
			}
			continue
		}
		if err != nil || p != tc.wantProv || m != tc.wantModel {
			t.Errorf("SplitModel(%q) = (%q,%q,%v), want (%q,%q,nil)", tc.in, p, m, err, tc.wantProv, tc.wantModel)
		}
	}
}

func TestRegistryDispatch(t *testing.T) {
	reg := NewRegistry()
	sp := &stubProvider{name: "anthropic", models: []string{"claude-sonnet-4-6"}}
	reg.Register(sp)

	resp, err := reg.Dispatch(context.Background(), ChatRequest{
		Model:    "anthropic/claude-sonnet-4-6",
		Messages: []Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if sp.last.Model != "claude-sonnet-4-6" {
		t.Errorf("provider received model %q, want bare name", sp.last.Model)
	}
	if resp.Model != "anthropic/claude-sonnet-4-6" {
		t.Errorf("response model = %q, want full provider/model", resp.Model)
	}
	if resp.Object != "chat.completion" {
		t.Errorf("response object = %q", resp.Object)
	}
	if resp.Created == 0 {
		t.Error("response created not set")
	}
}

func TestRegistryDispatchUnknownProvider(t *testing.T) {
	reg := NewRegistry()
	_, err := reg.Dispatch(context.Background(), ChatRequest{
		Model:    "ghost/x",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("err = %v, want ErrUnknownProvider", err)
	}
}

func TestHandlerModels(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&stubProvider{name: "anthropic", models: []string{"claude-sonnet-4-6", "claude-haiku-4-5"}})
	reg.Register(&stubProvider{name: "openai", models: []string{"gpt-4o"}})

	r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	w := httptest.NewRecorder()
	Handler(reg).ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var body struct {
		Object string  `json:"object"`
		Data   []Model `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Object != "list" || len(body.Data) != 3 {
		t.Fatalf("unexpected body: %+v", body)
	}
	seen := map[string]bool{}
	for _, m := range body.Data {
		seen[m.ID] = true
		if !strings.Contains(m.ID, "/") {
			t.Errorf("model ID %q missing provider prefix", m.ID)
		}
	}
	for _, want := range []string{"anthropic/claude-sonnet-4-6", "anthropic/claude-haiku-4-5", "openai/gpt-4o"} {
		if !seen[want] {
			t.Errorf("missing model %q in /v1/models output", want)
		}
	}
}

func TestHandlerChatCompletions(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&stubProvider{name: "anthropic", models: []string{"claude-sonnet-4-6"}})

	body := `{"model":"anthropic/claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}]}`
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	w := httptest.NewRecorder()
	Handler(reg).ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp ChatResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ID == "" || !strings.HasPrefix(resp.ID, "chatcmpl-") {
		t.Errorf("missing/invalid id: %q", resp.ID)
	}
	if resp.Model != "anthropic/claude-sonnet-4-6" {
		t.Errorf("model = %q", resp.Model)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Message.Role != "assistant" {
		t.Errorf("choices = %+v", resp.Choices)
	}
}

func TestHandlerChatCompletionsErrors(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&stubProvider{name: "anthropic", models: []string{"x"}})
	h := Handler(reg)

	cases := []struct {
		name string
		body string
		code int
	}{
		{"bad json", `{`, http.StatusBadRequest},
		{"empty messages", `{"model":"anthropic/x","messages":[]}`, http.StatusBadRequest},
		{"unknown provider", `{"model":"ghost/x","messages":[{"role":"user","content":"hi"}]}`, http.StatusNotFound},
		{"invalid model", `{"model":"noprefix","messages":[{"role":"user","content":"hi"}]}`, http.StatusBadRequest},
		{"streaming unsupported", `{"model":"anthropic/x","stream":true,"messages":[{"role":"user","content":"hi"}]}`, http.StatusNotImplemented},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(tc.body))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.code {
				t.Errorf("status = %d, want %d, body=%s", w.Code, tc.code, w.Body.String())
			}
			var env struct {
				Error map[string]string `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if env.Error["type"] == "" || env.Error["message"] == "" {
				t.Errorf("missing OpenAI error envelope: %s", w.Body.String())
			}
		})
	}
}
