package gateway

import (
	"context"
	"encoding/json"
	"net/http"
)

// OllamaConfig configures the Ollama adapter. Ollama is a local-first
// runtime so APIKey is usually empty and BaseURL points at a sidecar.
type OllamaConfig struct {
	BaseURL string // defaults to http://localhost:11434
	Models  []string
	Client  *http.Client
	Retry   RetryPolicy
}

type ollamaProvider struct {
	cfg OllamaConfig
}

// NewOllamaProvider builds a Provider against an Ollama server's
// /api/chat endpoint. Ollama returns one JSON object per token in
// streaming mode and a single object when stream=false; we always send
// stream=false because the T201 facade doesn't surface streaming yet.
func NewOllamaProvider(cfg OllamaConfig) Provider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "http://localhost:11434"
	}
	if cfg.Client == nil {
		cfg.Client = DefaultHTTPClient()
	}
	if cfg.Retry.MaxAttempts == 0 {
		cfg.Retry = DefaultRetryPolicy()
	}
	return &ollamaProvider{cfg: cfg}
}

func (p *ollamaProvider) Name() string { return "ollama" }

func (p *ollamaProvider) Models(ctx context.Context) ([]string, error) {
	out := make([]string, len(p.cfg.Models))
	copy(out, p.cfg.Models)
	return out, nil
}

type ollamaOptions struct {
	Temperature *float64 `json:"temperature,omitempty"`
	NumPredict  *int     `json:"num_predict,omitempty"`
}

// ollamaWireMessage is the per-message shape Ollama wants on the wire.
// Unlike OpenAI/Anthropic, Ollama does not embed images in the
// content array — it has a separate top-level Images field per
// message containing base64-encoded image bytes. We translate at
// adapter time so callers can use the same MessageContent abstraction
// for every backend.
type ollamaWireMessage struct {
	Role    string   `json:"role"`
	Content string   `json:"content"`
	Images  []string `json:"images,omitempty"`
}

type ollamaRequest struct {
	Model    string             `json:"model"`
	Messages []ollamaWireMessage `json:"messages"`
	Stream   bool               `json:"stream"`
	Options  *ollamaOptions     `json:"options,omitempty"`
}

type ollamaResponse struct {
	Model     string `json:"model"`
	CreatedAt string `json:"created_at"`
	// Ollama always returns text-only assistant messages — the images
	// field is request-only. Decode into a flat shape and lift into a
	// gateway Message via TextContent below.
	Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"message"`
	Done bool `json:"done"`
	// Token counts when available; Ollama only fills these on the final
	// response, which is what we get with stream=false.
	PromptEvalCount int `json:"prompt_eval_count"`
	EvalCount       int `json:"eval_count"`
}

func (p *ollamaProvider) ChatCompletion(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	wire := make([]ollamaWireMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		wm := ollamaWireMessage{Role: m.Role, Content: m.Content.Text()}
		// Lift any image_url parts out of the content stream into the
		// per-message Images field. data:image/...;base64,<x> URLs
		// become the raw base64 (Ollama wants the payload only); other
		// URL forms (https://...) are skipped because Ollama can't
		// fetch them server-side.
		for _, img := range m.Content.Images() {
			if _, b64, ok := decodeDataURL(img.URL); ok {
				wm.Images = append(wm.Images, b64)
			}
		}
		wire = append(wire, wm)
	}
	upstream := ollamaRequest{
		Model:    req.Model,
		Messages: wire,
		Stream:   false,
	}
	if req.Temperature != nil || req.MaxTokens != nil {
		upstream.Options = &ollamaOptions{
			Temperature: req.Temperature,
			NumPredict:  req.MaxTokens,
		}
	}
	body, err := json.Marshal(upstream)
	if err != nil {
		return ChatResponse{}, err
	}
	respBody, err := doJSONRequest(ctx, p.cfg.Client, p.cfg.Retry, "ollama", http.MethodPost, p.cfg.BaseURL+"/api/chat", nil, body)
	if err != nil {
		return ChatResponse{}, err
	}
	var parsed ollamaResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return ChatResponse{}, err
	}
	finish := ""
	if parsed.Done {
		finish = "stop"
	}
	return ChatResponse{
		Object: "chat.completion",
		Model:  parsed.Model,
		Choices: []Choice{{
			Index:        0,
			Message:      Message{Role: parsed.Message.Role, Content: TextContent(parsed.Message.Content)},
			FinishReason: finish,
		}},
		Usage: Usage{
			PromptTokens:     parsed.PromptEvalCount,
			CompletionTokens: parsed.EvalCount,
			TotalTokens:      parsed.PromptEvalCount + parsed.EvalCount,
		},
	}, nil
}
