package gateway

import (
	"context"
	"encoding/json"
	"net/http"
)

// OpenAIConfig configures both the OpenAI provider and Deepseek (which
// ships an OpenAI-compatible API at a different base URL). Models is a
// caller-supplied catalog because /v1/models on hosted providers
// requires a network call we want to avoid in tests and in cold-start
// paths — T203 will populate it from the encrypted key store.
type OpenAIConfig struct {
	Name    string // registry key; defaults to "openai"
	APIKey  string
	BaseURL string // defaults to https://api.openai.com
	Models  []string
	Client  *http.Client
	Retry   RetryPolicy
}

type openAIProvider struct {
	cfg OpenAIConfig
}

// NewOpenAIProvider builds a Provider against the OpenAI chat completions
// API. Pass Name="deepseek" + BaseURL="https://api.deepseek.com" to use
// the same adapter for Deepseek (their API is byte-for-byte compatible).
func NewOpenAIProvider(cfg OpenAIConfig) Provider {
	if cfg.Name == "" {
		cfg.Name = "openai"
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openai.com"
	}
	if cfg.Client == nil {
		cfg.Client = DefaultHTTPClient()
	}
	if cfg.Retry.MaxAttempts == 0 {
		cfg.Retry = DefaultRetryPolicy()
	}
	return &openAIProvider{cfg: cfg}
}

func (p *openAIProvider) Name() string { return p.cfg.Name }

func (p *openAIProvider) Models(ctx context.Context) ([]string, error) {
	out := make([]string, len(p.cfg.Models))
	copy(out, p.cfg.Models)
	return out, nil
}

// openAIChatRequest is the wire shape we send upstream. We forward the
// message array verbatim because OpenAI's chat schema is a strict
// superset of our internal Message — Role/Content/Name map 1:1.
type openAIChatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature *float64  `json:"temperature,omitempty"`
	MaxTokens   *int      `json:"max_tokens,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
}

// openAIChatResponse mirrors the upstream response. Choices/Usage map
// directly to ChatResponse so the translation step is just a re-tag.
// Assistant responses from OpenAI are always text-only at the
// content level (the model returns prose, not images), so the
// embedded message uses a plain string Content field rather than the
// MessageContent sum type.
type openAIChatResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
}

func (p *openAIProvider) ChatCompletion(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	upstream := openAIChatRequest{
		Model:       req.Model,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Stream:      false, // T201 facade rejects streaming upstream of us
	}
	body, err := json.Marshal(upstream)
	if err != nil {
		return ChatResponse{}, err
	}
	headers := map[string]string{
		"Authorization": "Bearer " + p.cfg.APIKey,
	}
	respBody, err := doJSONRequest(ctx, p.cfg.Client, p.cfg.Retry, p.cfg.Name, http.MethodPost, p.cfg.BaseURL+"/v1/chat/completions", headers, body)
	if err != nil {
		return ChatResponse{}, err
	}
	var parsed openAIChatResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return ChatResponse{}, err
	}
	out := ChatResponse{
		ID:      parsed.ID,
		Object:  parsed.Object,
		Created: parsed.Created,
		Model:   parsed.Model,
		Usage:   parsed.Usage,
	}
	for _, c := range parsed.Choices {
		out.Choices = append(out.Choices, Choice{
			Index:        c.Index,
			Message:      Message{Role: c.Message.Role, Content: TextContent(c.Message.Content)},
			FinishReason: c.FinishReason,
		})
	}
	return out, nil
}

// NewDeepseekProvider is a thin wrapper that points the OpenAI adapter
// at Deepseek's base URL. Their API is OpenAI-compatible at the wire
// level so reusing one adapter is the right tradeoff — if their schema
// ever drifts we fork this into its own file.
func NewDeepseekProvider(cfg OpenAIConfig) Provider {
	if cfg.Name == "" {
		cfg.Name = "deepseek"
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.deepseek.com"
	}
	return NewOpenAIProvider(cfg)
}
