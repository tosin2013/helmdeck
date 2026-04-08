package gateway

import (
	"context"
	"encoding/json"
	"net/http"
)

// GeminiConfig configures the Google Gemini generateContent adapter.
type GeminiConfig struct {
	APIKey  string
	BaseURL string // defaults to https://generativelanguage.googleapis.com
	Models  []string
	Client  *http.Client
	Retry   RetryPolicy
}

type geminiProvider struct {
	cfg GeminiConfig
}

// NewGeminiProvider builds a Provider against the Google Gemini
// generateContent endpoint. Gemini's wire format is the most divergent
// of the five — content lives in `parts[].text`, roles use `model`
// instead of `assistant`, and the model goes in the URL path rather
// than the body.
func NewGeminiProvider(cfg GeminiConfig) Provider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://generativelanguage.googleapis.com"
	}
	if cfg.Client == nil {
		cfg.Client = DefaultHTTPClient()
	}
	if cfg.Retry.MaxAttempts == 0 {
		cfg.Retry = DefaultRetryPolicy()
	}
	return &geminiProvider{cfg: cfg}
}

func (p *geminiProvider) Name() string { return "gemini" }

func (p *geminiProvider) Models(ctx context.Context) ([]string, error) {
	out := make([]string, len(p.cfg.Models))
	copy(out, p.cfg.Models)
	return out, nil
}

// geminiPart is one entry in a Gemini content array. Exactly one of
// Text or InlineData should be set per part. omitempty on both keeps
// the wire format clean for text-only requests, which preserves
// backward compat with the existing test fixtures.
type geminiPart struct {
	Text       string            `json:"text,omitempty"`
	InlineData *geminiInlineData `json:"inlineData,omitempty"`
}

// geminiInlineData carries a base64-encoded image with its MIME type.
// Gemini does not accept https URLs as image inputs at the
// generateContent endpoint — operators have to inline the bytes.
type geminiInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type geminiContent struct {
	Role  string       `json:"role"`
	Parts []geminiPart `json:"parts"`
}

type geminiGenConfig struct {
	Temperature     *float64 `json:"temperature,omitempty"`
	MaxOutputTokens *int     `json:"maxOutputTokens,omitempty"`
}

type geminiRequest struct {
	Contents          []geminiContent  `json:"contents"`
	SystemInstruction *geminiContent   `json:"systemInstruction,omitempty"`
	GenerationConfig  *geminiGenConfig `json:"generationConfig,omitempty"`
}

type geminiResponse struct {
	Candidates []struct {
		Content      geminiContent `json:"content"`
		FinishReason string        `json:"finishReason"`
		Index        int           `json:"index"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		TotalTokenCount      int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
}

func (p *geminiProvider) ChatCompletion(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	upstream := geminiRequest{}
	if req.Temperature != nil || req.MaxTokens != nil {
		upstream.GenerationConfig = &geminiGenConfig{
			Temperature:     req.Temperature,
			MaxOutputTokens: req.MaxTokens,
		}
	}
	for _, m := range req.Messages {
		if m.Role == "system" {
			// Same multi-system handling as the Anthropic adapter:
			// concatenate into the typed systemInstruction field.
			// System messages are text-only at the gateway layer.
			text := m.Content.Text()
			if upstream.SystemInstruction == nil {
				upstream.SystemInstruction = &geminiContent{Parts: []geminiPart{{Text: text}}}
			} else {
				upstream.SystemInstruction.Parts[0].Text += "\n\n" + text
			}
			continue
		}
		role := m.Role
		if role == "assistant" {
			role = "model" // Gemini's role nomenclature
		}
		upstream.Contents = append(upstream.Contents, geminiContent{
			Role:  role,
			Parts: messageContentToGeminiParts(m.Content),
		})
	}

	body, err := json.Marshal(upstream)
	if err != nil {
		return ChatResponse{}, err
	}
	// Gemini puts model + key in the URL. The key as a query param is
	// what their official SDK does — header auth is not supported on
	// the public generativelanguage endpoint.
	url := p.cfg.BaseURL + "/v1beta/models/" + req.Model + ":generateContent?key=" + p.cfg.APIKey
	respBody, err := doJSONRequest(ctx, p.cfg.Client, p.cfg.Retry, "gemini", http.MethodPost, url, nil, body)
	if err != nil {
		return ChatResponse{}, err
	}
	var parsed geminiResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return ChatResponse{}, err
	}
	out := ChatResponse{
		Object: "chat.completion",
		Model:  req.Model,
		Usage: Usage{
			PromptTokens:     parsed.UsageMetadata.PromptTokenCount,
			CompletionTokens: parsed.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      parsed.UsageMetadata.TotalTokenCount,
		},
	}
	for _, c := range parsed.Candidates {
		var text string
		for _, part := range c.Content.Parts {
			text += part.Text
		}
		out.Choices = append(out.Choices, Choice{
			Index:        c.Index,
			Message:      Message{Role: "assistant", Content: TextContent(text)},
			FinishReason: c.FinishReason,
		})
	}
	return out, nil
}

// messageContentToGeminiParts maps a gateway MessageContent into the
// Gemini parts array. Text-only content becomes a single text part
// (matching the legacy fixture). Multipart content emits one part
// per text/image_url block; data: image URLs are decoded into the
// inlineData{mimeType,data} form Gemini requires. https URLs are
// dropped with a placeholder text marker since Gemini's
// generateContent endpoint does not accept remote image URLs as
// inputs (the Files API would, but routing through it is a separate
// adapter concern).
func messageContentToGeminiParts(mc MessageContent) []geminiPart {
	if !mc.IsMultipart() {
		return []geminiPart{{Text: mc.Text()}}
	}
	out := make([]geminiPart, 0, len(mc.Parts()))
	for _, p := range mc.Parts() {
		switch p.Type {
		case "text":
			out = append(out, geminiPart{Text: p.Text})
		case "image_url":
			if p.ImageURL == nil {
				continue
			}
			if mime, b64, ok := decodeDataURL(p.ImageURL.URL); ok {
				out = append(out, geminiPart{InlineData: &geminiInlineData{MimeType: mime, Data: b64}})
			} else {
				out = append(out, geminiPart{Text: "[image: " + p.ImageURL.URL + "]"})
			}
		}
	}
	return out
}

// decodeDataURL parses a data:image/<mime>;base64,<payload> URL.
// Returns ("image/png", "iVBORw...", true) on success or ("", "", false)
// for any other URL form. Centralised so adapters can share the
// parser instead of each rolling its own.
func decodeDataURL(url string) (mime, b64 string, ok bool) {
	const prefix = "data:"
	if len(url) <= len(prefix) || url[:len(prefix)] != prefix {
		return "", "", false
	}
	rest := url[len(prefix):]
	semi := -1
	comma := -1
	for i := 0; i < len(rest); i++ {
		if semi < 0 && rest[i] == ';' {
			semi = i
		}
		if rest[i] == ',' {
			comma = i
			break
		}
	}
	if semi <= 0 || comma <= semi {
		return "", "", false
	}
	return rest[:semi], rest[comma+1:], true
}
