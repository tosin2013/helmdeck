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

// anthropicTextBlock, anthropicImageBlock, anthropicToolUseBlock, and
// anthropicToolResultBlock are the four content block shapes we emit.
// T807f adds the tool_use / tool_result pair so gateway-level tool
// routing works end-to-end — vision.* StepNative and any other pack
// using native computer-use tool schemas depends on this.
type anthropicTextBlock struct {
	Type string `json:"type"` // "text"
	Text string `json:"text"`
}

type anthropicImageBlock struct {
	Type   string                    `json:"type"` // "image"
	Source anthropicImageBlockSource `json:"source"`
}

type anthropicImageBlockSource struct {
	Type      string `json:"type"` // "base64" or "url"
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

// anthropicToolUseBlock is an assistant-role content block: the model's
// tool call. Input is the provider-native JSON object for the tool's
// arguments — Anthropic wants a decoded object, not a raw string, so
// we use json.RawMessage and let the marshaller embed it verbatim.
type anthropicToolUseBlock struct {
	Type  string          `json:"type"` // "tool_use"
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// anthropicToolResultBlock is a user-role content block: the caller
// handing a tool's output back for the next turn. Content is forwarded
// as an opaque JSON value so callers can return strings, structured
// objects, or anything else the model can parse.
type anthropicToolResultBlock struct {
	Type      string          `json:"type"` // "tool_result"
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

// anthropicTool is the top-level tool definition — the shape Anthropic
// expects in request.tools. InputSchema is forwarded verbatim from the
// gateway ToolDefinition so pack authors can hand in a JSON Schema
// document and have it reach the model unchanged.
type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// anthropicToolChoice constrains the model's tool selection. Anthropic
// accepts {type: "auto"}, {type: "any"}, {type: "tool", name: "..."}.
// We also pass {type: "none"} through when the caller explicitly sets
// Mode to "none" — Anthropic treats that as "do not call any tools,"
// which is the same semantics as the OpenAI and Gemini variants.
type anthropicToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

type anthropicRequest struct {
	Model       string               `json:"model"`
	System      string               `json:"system,omitempty"`
	Messages    []anthropicMessage   `json:"messages"`
	MaxTokens   int                  `json:"max_tokens"`
	Temperature *float64             `json:"temperature,omitempty"`
	Tools       []anthropicTool      `json:"tools,omitempty"`
	ToolChoice  *anthropicToolChoice `json:"tool_choice,omitempty"`
}

// anthropicResponse decodes Anthropic's /v1/messages response. The
// Content array is a union of text and tool_use blocks (plus image
// blocks on the request side, never on the response side). We keep
// every field we might care about in one struct and branch in the
// caller — simpler than a json.RawMessage + second decode.
type anthropicResponse struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Role    string `json:"role"`
	Model   string `json:"model"`
	Content []struct {
		Type  string          `json:"type"`
		Text  string          `json:"text,omitempty"`
		ID    string          `json:"id,omitempty"`    // tool_use id
		Name  string          `json:"name,omitempty"`  // tool_use name
		Input json.RawMessage `json:"input,omitempty"` // tool_use arguments
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
	// Tool definitions + tool choice (T807f). Empty Tools is the
	// chat-only path and leaves both fields nil so the wire shape is
	// unchanged for legacy callers. Each ToolDefinition translates
	// 1:1 to the Anthropic top-level tool schema.
	if len(req.Tools) > 0 {
		upstream.Tools = make([]anthropicTool, 0, len(req.Tools))
		for _, t := range req.Tools {
			upstream.Tools = append(upstream.Tools, anthropicTool{
				Name:        t.Name,
				Description: t.Description,
				InputSchema: t.InputSchema,
			})
		}
	}
	if req.ToolChoice != nil {
		upstream.ToolChoice = translateAnthropicToolChoice(req.ToolChoice)
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
	// Build the assistant message. Two shapes:
	//   (a) Text-only response — legacy chat path, fold every text
	//       block into a single TextContent for backward compat.
	//   (b) Any response containing a tool_use block — emit
	//       MultipartContent with a mix of TextPart and ToolUsePart
	//       so packs that drive tool-use can see the model's call
	//       alongside any accompanying text.
	var hasTool bool
	for _, b := range parsed.Content {
		if b.Type == "tool_use" {
			hasTool = true
			break
		}
	}
	var message Message
	if hasTool {
		parts := make([]ContentPart, 0, len(parsed.Content))
		for _, b := range parsed.Content {
			switch b.Type {
			case "text":
				if b.Text != "" {
					parts = append(parts, TextPart(b.Text))
				}
			case "tool_use":
				parts = append(parts, ToolUsePart(b.ID, b.Name, b.Input))
			}
		}
		message = Message{Role: "assistant", Content: MultipartContent(parts...)}
	} else {
		var content string
		for _, b := range parsed.Content {
			if b.Type == "text" {
				content += b.Text
			}
		}
		message = Message{Role: "assistant", Content: TextContent(content)}
	}
	return ChatResponse{
		ID:      parsed.ID,
		Object:  "chat.completion",
		Model:   parsed.Model,
		Choices: []Choice{{Index: 0, Message: message, FinishReason: parsed.StopReason}},
		Usage: Usage{
			PromptTokens:     parsed.Usage.InputTokens,
			CompletionTokens: parsed.Usage.OutputTokens,
			TotalTokens:      parsed.Usage.InputTokens + parsed.Usage.OutputTokens,
		},
	}, nil
}

// translateAnthropicToolChoice maps the gateway-agnostic ToolChoice
// onto Anthropic's type-tagged shape. Mode defaults to "auto" when
// blank or unknown so a caller that accidentally sends an empty
// ToolChoice still gets sensible behavior.
func translateAnthropicToolChoice(tc *ToolChoice) *anthropicToolChoice {
	switch tc.Mode {
	case "tool":
		return &anthropicToolChoice{Type: "tool", Name: tc.Name}
	case "any", "required":
		return &anthropicToolChoice{Type: "any"}
	case "none":
		return &anthropicToolChoice{Type: "none"}
	default:
		return &anthropicToolChoice{Type: "auto"}
	}
}

// anthropicContent translates a gateway MessageContent into the
// shape Anthropic accepts on the wire. Text-only content stays as a
// plain string (the legacy form, smaller payload). Multipart content
// becomes a typed block array. Image data URIs are decoded into the
// {type:"image", source:{type:"base64", ...}} form Anthropic expects;
// http(s) URLs pass through as {source:{type:"url", url:"..."}}.
// T807f adds tool_use / tool_result blocks so multi-turn tool
// conversations round-trip correctly.
func anthropicContent(mc MessageContent) any {
	if !mc.IsMultipart() {
		return mc.Text()
	}
	blocks := make([]any, 0, len(mc.Parts()))
	for _, p := range mc.Parts() {
		switch p.Type {
		case ContentPartText:
			blocks = append(blocks, anthropicTextBlock{Type: "text", Text: p.Text})
		case ContentPartImageURL:
			if p.ImageURL == nil {
				continue
			}
			src := imageURLToAnthropicSource(p.ImageURL.URL)
			blocks = append(blocks, anthropicImageBlock{Type: "image", Source: src})
		case ContentPartToolUse:
			// An assistant-role tool_use block being echoed back to
			// Anthropic (e.g. multi-turn tool loop where the caller
			// preserves history). Input is forwarded verbatim.
			blocks = append(blocks, anthropicToolUseBlock{
				Type:  "tool_use",
				ID:    p.ToolUseID,
				Name:  p.ToolName,
				Input: p.ToolInput,
			})
		case ContentPartToolResult:
			blocks = append(blocks, anthropicToolResultBlock{
				Type:      "tool_result",
				ToolUseID: p.ToolResultID,
				Content:   p.ToolResultContent,
				IsError:   p.IsError,
			})
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
