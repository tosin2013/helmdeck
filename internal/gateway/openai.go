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

// openAIChatRequest is the wire shape we send upstream. T807f splits
// this out from the gateway Message passthrough because OpenAI's
// tool-use shape puts tool_calls on the message struct (not inside
// the content array) and serializes tool arguments as a JSON STRING
// rather than a raw JSON object. We build openAIMessage values per
// call instead of passing req.Messages through verbatim.
type openAIChatRequest struct {
	Model       string           `json:"model"`
	Messages    []openAIMessage  `json:"messages"`
	Temperature *float64         `json:"temperature,omitempty"`
	MaxTokens   *int             `json:"max_tokens,omitempty"`
	Stream      bool             `json:"stream,omitempty"`
	Tools       []openAITool     `json:"tools,omitempty"`
	ToolChoice  any              `json:"tool_choice,omitempty"`
}

// openAIMessage mirrors the openai /v1/chat/completions message
// schema. Content is any because OpenAI accepts EITHER a plain string
// (legacy text-only), a multipart array (vision), or null (when
// tool_calls is present on an assistant message). ToolCalls is the
// assistant-side tool-use shape; ToolCallID identifies the tool_result
// block a tool-role message is answering.
type openAIMessage struct {
	Role       string           `json:"role"`
	Content    any              `json:"content,omitempty"`
	Name       string           `json:"name,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

// openAIToolCall is one entry in the assistant message's tool_calls
// array. Arguments is the JSON-encoded argument object AS A STRING
// — OpenAI has historically insisted on this and every SDK still
// serializes it this way. We produce the string on the request side
// and parse it on the response side.
type openAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"` // always "function" today
	Function openAIToolCallFunc `json:"function"`
}

type openAIToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON-encoded object, as a string
}

// openAITool wraps a ToolDefinition in OpenAI's function-typed
// envelope. Parameters is forwarded verbatim from the gateway
// ToolDefinition.InputSchema so pack authors writing JSON Schema
// don't have to re-serialize.
type openAITool struct {
	Type     string             `json:"type"` // always "function" today
	Function openAIToolFunction `json:"function"`
}

type openAIToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// openAIChatResponse mirrors the upstream response. The message
// Content field is a pointer-string because OpenAI returns JSON null
// when tool_calls carries the payload; ToolCalls is the tool-use
// shape assistant messages can carry alongside (or instead of) text.
type openAIChatResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role      string           `json:"role"`
			Content   *string          `json:"content"`
			ToolCalls []openAIToolCall `json:"tool_calls,omitempty"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
}

func (p *openAIProvider) ChatCompletion(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	upstream := openAIChatRequest{
		Model:       req.Model,
		Messages:    make([]openAIMessage, 0, len(req.Messages)),
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Stream:      false, // T201 facade rejects streaming upstream of us
	}
	// Tool definitions (T807f). Each ToolDefinition wraps into a
	// `type: "function"` envelope and the InputSchema flows through
	// as `parameters`. OpenAI's tool_choice is either a string
	// ("auto"/"none"/"required") or an object naming a specific tool,
	// so we use `any` on the wire and branch here.
	if len(req.Tools) > 0 {
		upstream.Tools = make([]openAITool, 0, len(req.Tools))
		for _, t := range req.Tools {
			upstream.Tools = append(upstream.Tools, openAITool{
				Type: "function",
				Function: openAIToolFunction{
					Name:        t.Name,
					Description: t.Description,
					Parameters:  t.InputSchema,
				},
			})
		}
	}
	if req.ToolChoice != nil {
		upstream.ToolChoice = translateOpenAIToolChoice(req.ToolChoice)
	}
	// Translate each gateway Message into an openAIMessage. Most
	// messages pass through with Content untouched — the gateway
	// MessageContent already marshals to string or array shapes
	// OpenAI accepts natively. The exceptions are:
	//
	//   (1) assistant-role messages whose Content contains tool_use
	//       blocks — OpenAI puts the tool calls on a separate
	//       ToolCalls field, not in content, so we lift them out.
	//   (2) tool_result blocks — OpenAI uses role="tool" with
	//       ToolCallID and plain-string content; we emit a separate
	//       openAIMessage for each result, NOT a combined array.
	for _, m := range req.Messages {
		if !m.Content.IsMultipart() {
			upstream.Messages = append(upstream.Messages, openAIMessage{
				Role:    m.Role,
				Content: m.Content,
				Name:    m.Name,
			})
			continue
		}
		// Multipart — split tool_use / tool_result out from text/image.
		var (
			cleanParts []ContentPart
			toolCalls  []openAIToolCall
			toolResults []openAIMessage
		)
		for _, part := range m.Content.Parts() {
			switch part.Type {
			case ContentPartToolUse:
				toolCalls = append(toolCalls, openAIToolCall{
					ID:   part.ToolUseID,
					Type: "function",
					Function: openAIToolCallFunc{
						Name:      part.ToolName,
						Arguments: string(part.ToolInput),
					},
				})
			case ContentPartToolResult:
				// Each tool_result becomes its own role="tool"
				// message. Content stringifies the raw JSON — if
				// the caller handed in a bare string it's already
				// JSON-quoted, which OpenAI accepts.
				toolResults = append(toolResults, openAIMessage{
					Role:       "tool",
					ToolCallID: part.ToolResultID,
					Content:    string(part.ToolResultContent),
				})
			default:
				cleanParts = append(cleanParts, part)
			}
		}
		// If any text/image parts remain, emit the base message
		// first. Assistant messages with tool_calls may have no
		// content at all — OpenAI accepts that.
		if len(cleanParts) > 0 || len(toolCalls) > 0 {
			base := openAIMessage{
				Role:      m.Role,
				Name:      m.Name,
				ToolCalls: toolCalls,
			}
			if len(cleanParts) > 0 {
				base.Content = MultipartContent(cleanParts...)
			}
			upstream.Messages = append(upstream.Messages, base)
		}
		// Tool-result messages follow, one per result block.
		upstream.Messages = append(upstream.Messages, toolResults...)
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
		// Build the assistant message. If the model emitted tool
		// calls, we render them as ToolUsePart blocks alongside any
		// text. Otherwise the legacy text-only path stays.
		var content MessageContent
		if len(c.Message.ToolCalls) > 0 {
			parts := make([]ContentPart, 0, len(c.Message.ToolCalls)+1)
			if c.Message.Content != nil && *c.Message.Content != "" {
				parts = append(parts, TextPart(*c.Message.Content))
			}
			for _, tc := range c.Message.ToolCalls {
				// OpenAI returns arguments as a string containing
				// JSON. Feed it through json.RawMessage so downstream
				// ToolUsePart.ToolInput looks identical to what
				// Anthropic emits — callers get provider-agnostic
				// tool_input bytes.
				parts = append(parts, ToolUsePart(tc.ID, tc.Function.Name, json.RawMessage(tc.Function.Arguments)))
			}
			content = MultipartContent(parts...)
		} else {
			var s string
			if c.Message.Content != nil {
				s = *c.Message.Content
			}
			content = TextContent(s)
		}
		out.Choices = append(out.Choices, Choice{
			Index:        c.Index,
			Message:      Message{Role: c.Message.Role, Content: content},
			FinishReason: c.FinishReason,
		})
	}
	return out, nil
}

// translateOpenAIToolChoice maps the gateway-agnostic ToolChoice onto
// OpenAI's hybrid shape — a string enum for "auto"/"none"/"required"
// or a type-tagged object naming the specific tool. Empty Mode maps
// to "auto" so a blank ToolChoice is well-defined.
func translateOpenAIToolChoice(tc *ToolChoice) any {
	switch tc.Mode {
	case "tool":
		return map[string]any{
			"type":     "function",
			"function": map[string]any{"name": tc.Name},
		}
	case "any", "required":
		return "required"
	case "none":
		return "none"
	default:
		return "auto"
	}
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
