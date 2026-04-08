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

// anthropicMessage uses an interface for Content because Anthropic
// accepts either a plain string (text-only) or a typed content array
// of {"type":"text"} / {"type":"image"} blocks. We render text-only
// gateway messages as the string form to keep the wire compact and
// match what the existing provider tests expect.
type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// anthropicTextBlock and anthropicImageBlock are the two content
// block shapes we emit. Anthropic's full schema also includes
// tool_use / tool_result blocks; those land alongside the Pack
// Execution Engine's tool routing in a future ADR.
type anthropicTextBlock struct {
	Type string `json:"type"` // "text"
	Text string `json:"text"`
}

type anthropicImageBlock struct {
	Type   string                    `json:"type"` // "image"
	Source anthropicImageBlockSource `json:"source"`
}

type anthropicImageBlockSource struct {
	Type      string `json:"type"`       // "base64" or "url"
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
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
			// System messages are text-only at the gateway layer.
			if upstream.System != "" {
				upstream.System += "\n\n"
			}
			upstream.System += m.Content.Text()
			continue
		}
		upstream.Messages = append(upstream.Messages, anthropicMessage{
			Role:    m.Role,
			Content: anthropicContent(m.Content),
		})
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
		Choices: []Choice{{Index: 0, Message: Message{Role: "assistant", Content: TextContent(content)}, FinishReason: parsed.StopReason}},
		Usage: Usage{
			PromptTokens:     parsed.Usage.InputTokens,
			CompletionTokens: parsed.Usage.OutputTokens,
			TotalTokens:      parsed.Usage.InputTokens + parsed.Usage.OutputTokens,
		},
	}, nil
}

// anthropicContent translates a gateway MessageContent into the
// shape Anthropic accepts on the wire. Text-only content stays as a
// plain string (the legacy form, smaller payload). Multipart content
// becomes a typed block array. Image data URIs are decoded into the
// {type:"image", source:{type:"base64", ...}} form Anthropic expects;
// http(s) URLs pass through as {source:{type:"url", url:"..."}}.
func anthropicContent(mc MessageContent) any {
	if !mc.IsMultipart() {
		return mc.Text()
	}
	blocks := make([]any, 0, len(mc.Parts()))
	for _, p := range mc.Parts() {
		switch p.Type {
		case "text":
			blocks = append(blocks, anthropicTextBlock{Type: "text", Text: p.Text})
		case "image_url":
			if p.ImageURL == nil {
				continue
			}
			src := imageURLToAnthropicSource(p.ImageURL.URL)
			blocks = append(blocks, anthropicImageBlock{Type: "image", Source: src})
		}
	}
	return blocks
}

// imageURLToAnthropicSource decodes a URL into the Anthropic image
// source struct. data:image/<mime>;base64,<payload> becomes a base64
// source; everything else is forwarded as a url source (Anthropic
// added URL image support in late 2024).
func imageURLToAnthropicSource(url string) anthropicImageBlockSource {
	const dataPrefix = "data:"
	if len(url) > len(dataPrefix) && url[:len(dataPrefix)] == dataPrefix {
		// data:image/png;base64,iVBORw0KGgo...
		semi := -1
		comma := -1
		for i := len(dataPrefix); i < len(url); i++ {
			if semi < 0 && url[i] == ';' {
				semi = i
			}
			if url[i] == ',' {
				comma = i
				break
			}
		}
		if semi > 0 && comma > semi {
			return anthropicImageBlockSource{
				Type:      "base64",
				MediaType: url[len(dataPrefix):semi],
				Data:      url[comma+1:],
			}
		}
	}
	return anthropicImageBlockSource{Type: "url", URL: url}
}
