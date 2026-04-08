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

type geminiPart struct {
	Text string `json:"text"`
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
			if upstream.SystemInstruction == nil {
				upstream.SystemInstruction = &geminiContent{Parts: []geminiPart{{Text: m.Content}}}
			} else {
				upstream.SystemInstruction.Parts[0].Text += "\n\n" + m.Content
			}
			continue
		}
		role := m.Role
		if role == "assistant" {
			role = "model" // Gemini's role nomenclature
		}
		upstream.Contents = append(upstream.Contents, geminiContent{
			Role:  role,
			Parts: []geminiPart{{Text: m.Content}},
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
			Message:      Message{Role: "assistant", Content: text},
			FinishReason: c.FinishReason,
		})
	}
	return out, nil
}
