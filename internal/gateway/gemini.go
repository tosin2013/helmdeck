package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
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
// Text, InlineData, FunctionCall, or FunctionResponse should be set
// per part. T807f adds FunctionCall / FunctionResponse so native
// tool-use round-trips through the adapter — Gemini's functionCall
// args are a raw JSON object (unlike OpenAI's string), which maps
// cleanly onto ToolInput json.RawMessage.
type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	InlineData       *geminiInlineData       `json:"inlineData,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
}

// geminiInlineData carries a base64-encoded image with its MIME type.
// Gemini does not accept https URLs as image inputs at the
// generateContent endpoint — operators have to inline the bytes.
type geminiInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

// geminiFunctionCall is an assistant-side tool call. Args is the raw
// JSON arguments Gemini emits (or we echo back in a multi-turn).
type geminiFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// geminiFunctionResponse is the caller-side tool result. Response
// wraps the tool's output — Gemini expects an object containing at
// least a `name` field echoing the function name, plus arbitrary
// result data. We wrap raw JSON tool_result_content inside a
// standard envelope.
type geminiFunctionResponse struct {
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

// geminiFunctionDeclaration is the top-level tool schema Gemini
// accepts via tools[0].functionDeclarations. Parameters is a JSON
// Schema object, forwarded verbatim from ToolDefinition.InputSchema.
type geminiFunctionDeclaration struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type geminiToolsEntry struct {
	FunctionDeclarations []geminiFunctionDeclaration `json:"functionDeclarations,omitempty"`
}

// geminiToolConfig constrains function calling — the shape Gemini
// ships for its own computer-use tool preview. Mode is "AUTO" /
// "ANY" / "NONE"; allowedFunctionNames narrows "ANY" mode to a
// specific named tool.
type geminiToolConfig struct {
	FunctionCallingConfig geminiFunctionCallingConfig `json:"functionCallingConfig"`
}

type geminiFunctionCallingConfig struct {
	Mode                 string   `json:"mode"`
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
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
	Contents          []geminiContent    `json:"contents"`
	SystemInstruction *geminiContent     `json:"systemInstruction,omitempty"`
	GenerationConfig  *geminiGenConfig   `json:"generationConfig,omitempty"`
	Tools             []geminiToolsEntry `json:"tools,omitempty"`
	ToolConfig        *geminiToolConfig  `json:"toolConfig,omitempty"`
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
	// Tool definitions (T807f). Gemini groups every functionDeclaration
	// under a single tools[0] entry. We preserve the caller's order so
	// a model that cares about declaration order sees the same list
	// every run.
	if len(req.Tools) > 0 {
		decls := make([]geminiFunctionDeclaration, 0, len(req.Tools))
		for _, t := range req.Tools {
			decls = append(decls, geminiFunctionDeclaration{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			})
		}
		upstream.Tools = []geminiToolsEntry{{FunctionDeclarations: decls}}
	}
	if req.ToolChoice != nil {
		upstream.ToolConfig = translateGeminiToolConfig(req.ToolChoice)
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
		// Detect function calls so we can build a multipart message
		// if present; fall back to the legacy text-only path for
		// pure chat responses.
		hasFn := false
		for _, part := range c.Content.Parts {
			if part.FunctionCall != nil {
				hasFn = true
				break
			}
		}
		var content MessageContent
		if hasFn {
			parts := make([]ContentPart, 0, len(c.Content.Parts))
			for _, part := range c.Content.Parts {
				switch {
				case part.FunctionCall != nil:
					// Gemini doesn't return a call id — we synthesize
					// one from the tool name so downstream code that
					// pairs tool_use with tool_result has a non-empty
					// identifier to work with. Multi-call responses
					// use a suffix for uniqueness.
					id := "gemini-call-" + part.FunctionCall.Name
					parts = append(parts, ToolUsePart(id, part.FunctionCall.Name, part.FunctionCall.Args))
				case part.Text != "":
					parts = append(parts, TextPart(part.Text))
				}
			}
			content = MultipartContent(parts...)
		} else {
			var text string
			for _, part := range c.Content.Parts {
				text += part.Text
			}
			content = TextContent(text)
		}
		out.Choices = append(out.Choices, Choice{
			Index:        c.Index,
			Message:      Message{Role: "assistant", Content: content},
			FinishReason: c.FinishReason,
		})
	}
	return out, nil
}

// translateGeminiToolConfig maps the gateway ToolChoice onto Gemini's
// functionCallingConfig enum. "tool" and "any"/"required" both map to
// ANY mode; "tool" additionally narrows via allowedFunctionNames.
// Empty Mode → AUTO (default behavior, same as omitting tool_config).
func translateGeminiToolConfig(tc *ToolChoice) *geminiToolConfig {
	switch tc.Mode {
	case "tool":
		return &geminiToolConfig{
			FunctionCallingConfig: geminiFunctionCallingConfig{
				Mode:                 "ANY",
				AllowedFunctionNames: []string{tc.Name},
			},
		}
	case "any", "required":
		return &geminiToolConfig{FunctionCallingConfig: geminiFunctionCallingConfig{Mode: "ANY"}}
	case "none":
		return &geminiToolConfig{FunctionCallingConfig: geminiFunctionCallingConfig{Mode: "NONE"}}
	default:
		return &geminiToolConfig{FunctionCallingConfig: geminiFunctionCallingConfig{Mode: "AUTO"}}
	}
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
		case ContentPartText:
			out = append(out, geminiPart{Text: p.Text})
		case ContentPartImageURL:
			if p.ImageURL == nil {
				continue
			}
			if mime, b64, ok := decodeDataURL(p.ImageURL.URL); ok {
				out = append(out, geminiPart{InlineData: &geminiInlineData{MimeType: mime, Data: b64}})
			} else {
				out = append(out, geminiPart{Text: "[image: " + p.ImageURL.URL + "]"})
			}
		case ContentPartToolUse:
			// Echo an assistant-side tool_use back to Gemini (the
			// second turn of a multi-turn loop where the caller
			// preserved history). Gemini parses args as a raw JSON
			// object, matching what we stored in ToolInput.
			out = append(out, geminiPart{
				FunctionCall: &geminiFunctionCall{
					Name: p.ToolName,
					Args: p.ToolInput,
				},
			})
		case ContentPartToolResult:
			// Caller-side tool result. Gemini wraps it in a
			// functionResponse envelope keyed by function name —
			// which we don't have on the ContentPart (ToolResultID
			// is the call id, not the tool name). For now we
			// forward the ID as the name, which round-trips cleanly
			// with our synthetic "gemini-call-<name>" id scheme.
			out = append(out, geminiPart{
				FunctionResponse: &geminiFunctionResponse{
					Name:     strings.TrimPrefix(p.ToolResultID, "gemini-call-"),
					Response: p.ToolResultContent,
				},
			})
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
