package gateway

import (
	"context"
	"encoding/json"
	"net/http"
)

// AnthropicConfig configures the Anthropic Messages API adapter.
type AnthropicConfig struct {
	APIKey  string
	BaseURL string // defaults to https://api.anthropic.com
	Version string // anthropic-version header; defaults to 2023-06-01
	Models  []string
	Client  *http.Client
	Retry   RetryPolicy
}

type anthropicProvider struct {
	cfg AnthropicConfig
}

// NewAnthropicProvider builds a Provider against the Anthropic Messages
// API. Anthropic's wire format differs from OpenAI in three ways we
// have to translate around:
//
//  1. The system prompt is a top-level field, not a `role: system`
//     message in the array.
//  2. max_tokens is REQUIRED on every request.
//  3. The response wraps content in a typed array of blocks rather
//     than a single string.
func NewAnthropicProvider(cfg AnthropicConfig) Provider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.anthropic.com"
	}
	if cfg.Version == "" {
		cfg.Version = "2023-06-01"
	}
	if cfg.Client == nil {
		cfg.Client = DefaultHTTPClient()
	}
	if cfg.Retry.MaxAttempts == 0 {
		cfg.Retry = DefaultRetryPolicy()
	}
	return &anthropicProvider{cfg: cfg}
}

func (p *anthropicProvider) Name() string { return "anthropic" }

func (p *anthropicProvider) Models(ctx context.Context) ([]string, error) {
	out := make([]string, len(p.cfg.Models))
	copy(out, p.cfg.Models)
	return out, nil
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicRequest struct {
	Model       string             `json:"model"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature *float64           `json:"temperature,omitempty"`
}

type anthropicResponse struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Role    string `json:"role"`
	Model   string `json:"model"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func (p *anthropicProvider) ChatCompletion(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	// Anthropic requires max_tokens. If the caller didn't supply one,
	// pick a reasonable default — 4096 is what the SDK examples use and
	// is well below every current model's context cap.
	maxTokens := 4096
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		maxTokens = *req.MaxTokens
	}

	upstream := anthropicRequest{
		Model:       req.Model,
		MaxTokens:   maxTokens,
		Temperature: req.Temperature,
	}
	for _, m := range req.Messages {
		if m.Role == "system" {
			// Anthropic concatenates multiple system messages with a
			// blank line — easier to do here than at the call site.
			if upstream.System != "" {
				upstream.System += "\n\n"
			}
			upstream.System += m.Content
			continue
		}
		upstream.Messages = append(upstream.Messages, anthropicMessage{Role: m.Role, Content: m.Content})
	}

	body, err := json.Marshal(upstream)
	if err != nil {
		return ChatResponse{}, err
	}
	headers := map[string]string{
		"x-api-key":         p.cfg.APIKey,
		"anthropic-version": p.cfg.Version,
	}
	respBody, err := doJSONRequest(ctx, p.cfg.Client, p.cfg.Retry, "anthropic", http.MethodPost, p.cfg.BaseURL+"/v1/messages", headers, body)
	if err != nil {
		return ChatResponse{}, err
	}
	var parsed anthropicResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return ChatResponse{}, err
	}
	// Concatenate text blocks. Tool-use blocks are dropped at this layer
	// — the Pack Execution Engine (T205) handles tool calls; the raw
	// gateway is just for chat passthrough.
	var content string
	for _, b := range parsed.Content {
		if b.Type == "text" {
			content += b.Text
		}
	}
	return ChatResponse{
		ID:      parsed.ID,
		Object:  "chat.completion",
		Model:   parsed.Model,
		Choices: []Choice{{Index: 0, Message: Message{Role: "assistant", Content: content}, FinishReason: parsed.StopReason}},
		Usage: Usage{
			PromptTokens:     parsed.Usage.InputTokens,
			CompletionTokens: parsed.Usage.OutputTokens,
			TotalTokens:      parsed.Usage.InputTokens + parsed.Usage.OutputTokens,
		},
	}, nil
}
