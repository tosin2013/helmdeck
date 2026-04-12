package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

// Content part type constants — the wire-level `type` field on every
// ContentPart. Kept as exported constants so provider adapters can
// branch on them without stringly-typed comparisons.
const (
	ContentPartText       = "text"
	ContentPartImageURL   = "image_url"
	ContentPartToolUse    = "tool_use"    // model emitted a tool call (assistant-role message)
	ContentPartToolResult = "tool_result" // caller returned a tool result (user-role message)
)

// MessageContent is the content body of a chat Message. It can be
// either a plain text string (the legacy / text-only path) or an
// ordered array of typed content parts (text + images, matching the
// OpenAI vision spec). The two forms serialize to JSON differently:
//
//	TextContent("hi")                    -> "hi"
//	MultipartContent(TextPart("hi"),
//	                 ImageURLPart("...")) -> [{"type":"text",...},{"type":"image_url",...}]
//
// Custom Marshal/Unmarshal lets the gateway accept both shapes from
// /v1/chat/completions clients (frontier vision UIs send arrays;
// older callers send strings) and lets each provider adapter render
// upstream in whatever shape that backend speaks.
//
// Always construct via TextContent or MultipartContent rather than
// poking at the unexported fields directly — that keeps the "exactly
// one of text/parts is set" invariant local to this file.
type MessageContent struct {
	text       string
	parts      []ContentPart
	hasContent bool // distinguishes "" text from a missing content field
}

// ContentPart is one entry in a multipart message body. Type is one
// of the ContentPart* constants above. The union is open-ended: new
// part types extend the struct with optional fields rather than
// breaking the JSON shape.
//
// T807f adds tool_use (assistant-role tool calls) and tool_result
// (user-role tool-result blocks). Both are used by capability packs
// that route native provider computer-use tool schemas through the
// gateway — see internal/vision/vision.go StepNative().
type ContentPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *ImageURLPart `json:"image_url,omitempty"`

	// Tool-use block fields (Type == ContentPartToolUse). The LLM
	// emits these on assistant-role messages. ID uniquely identifies
	// the call within the current request so the caller can pair
	// each result back in a follow-up tool_result block. Name is the
	// tool name from the request's Tools[] array. Input is the
	// tool's arguments as a raw JSON object — handed to the handler
	// verbatim so the handler can unmarshal into its own input type.
	ToolUseID    string          `json:"tool_use_id,omitempty"`
	ToolName     string          `json:"tool_name,omitempty"`
	ToolInput    json.RawMessage `json:"tool_input,omitempty"`

	// Tool-result block fields (Type == ContentPartToolResult). The
	// caller emits these on user-role messages in follow-up turns.
	// ToolResultID must match the ID of the tool_use block being
	// answered. Content is the tool's output rendered for the model
	// (typically a string, but raw JSON is accepted so handlers can
	// return structured data). IsError=true flags a failed tool call
	// so the model sees "this attempt failed" and can retry or
	// change strategy.
	ToolResultID      string          `json:"tool_result_id,omitempty"`
	ToolResultContent json.RawMessage `json:"tool_result_content,omitempty"`
	IsError           bool            `json:"is_error,omitempty"`
}

// ImageURLPart is the OpenAI vision image_url block. URL accepts
// either a public https URL or an inline data URI
// (`data:image/png;base64,...`); Detail is the OpenAI hint, ignored
// by providers that don't recognize it.
type ImageURLPart struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

// TextContent constructs a text-only MessageContent. This is the
// shape every existing call site uses.
func TextContent(s string) MessageContent {
	return MessageContent{text: s, hasContent: true}
}

// MultipartContent constructs a multipart MessageContent from any
// number of parts. Use the TextPart / ImageURLPartFromDataURL /
// ImageURLPartFromHTTPS helpers to build the parts.
func MultipartContent(parts ...ContentPart) MessageContent {
	out := make([]ContentPart, len(parts))
	copy(out, parts)
	return MessageContent{parts: out, hasContent: true}
}

// TextPart wraps a text string as a ContentPart. Equivalent to
// {"type":"text","text":s} on the wire.
func TextPart(s string) ContentPart {
	return ContentPart{Type: ContentPartText, Text: s}
}

// ImageURLPartFromURL builds an image content part from a URL. The
// URL may be an https://... link or a data:image/png;base64,...
// inline blob — providers that don't accept inline data must extract
// and re-upload, which is the per-provider adapter's job.
func ImageURLPartFromURL(url string) ContentPart {
	return ContentPart{Type: ContentPartImageURL, ImageURL: &ImageURLPart{URL: url}}
}

// ToolUsePart wraps a model-emitted tool call as a ContentPart.
// Typically constructed by provider adapters when decoding an upstream
// tool-use block, not by callers directly. id is the provider's call
// identifier (e.g. Anthropic's `toolu_...`, OpenAI's `call_...`); name
// is the tool name from the original request; input is the raw JSON
// arguments the model chose for this call.
func ToolUsePart(id, name string, input json.RawMessage) ContentPart {
	return ContentPart{
		Type:      ContentPartToolUse,
		ToolUseID: id,
		ToolName:  name,
		ToolInput: input,
	}
}

// ToolResultPart wraps a caller-provided tool result as a ContentPart.
// Callers use this in follow-up turns to hand the tool's output back to
// the model so it can decide the next action. toolUseID MUST match the
// id from the original ToolUsePart being answered. content is the raw
// JSON the model sees; simple string outputs should be json.Marshal'd
// first so the provider adapter can render them in the upstream shape.
// Pass isError=true to flag a failed tool call — the model will see
// "this attempt failed" and is free to retry or change strategy.
func ToolResultPart(toolUseID string, content json.RawMessage, isError bool) ContentPart {
	return ContentPart{
		Type:              ContentPartToolResult,
		ToolResultID:      toolUseID,
		ToolResultContent: content,
		IsError:           isError,
	}
}

// IsMultipart reports whether the content is a parts array. Used by
// provider adapters that need to branch on representation.
func (mc MessageContent) IsMultipart() bool { return mc.parts != nil }

// IsEmpty reports whether the content has no text and no parts.
func (mc MessageContent) IsEmpty() bool {
	return !mc.hasContent || (mc.text == "" && len(mc.parts) == 0)
}

// Text returns the joined text portion of the content. For text-only
// content this is the literal string; for multipart content it's the
// concatenation of every text part with no separator (the order in
// which the model would see them).
func (mc MessageContent) Text() string {
	if !mc.IsMultipart() {
		return mc.text
	}
	var sb strings.Builder
	for _, p := range mc.parts {
		if p.Type == "text" {
			sb.WriteString(p.Text)
		}
	}
	return sb.String()
}

// Parts returns the content as a slice of ContentPart. For text-only
// content this returns a single text part so providers that only
// understand parts arrays don't need to special-case the legacy form.
// The returned slice is a copy — mutating it does not affect the
// underlying MessageContent.
func (mc MessageContent) Parts() []ContentPart {
	if mc.IsMultipart() {
		out := make([]ContentPart, len(mc.parts))
		copy(out, mc.parts)
		return out
	}
	if mc.text == "" {
		return nil
	}
	return []ContentPart{{Type: "text", Text: mc.text}}
}

// Images returns the image_url parts in order. Empty for text-only
// content. Used by adapters like Ollama that lift images out of the
// content stream into a separate top-level field.
func (mc MessageContent) Images() []ImageURLPart {
	if !mc.IsMultipart() {
		return nil
	}
	var out []ImageURLPart
	for _, p := range mc.parts {
		if p.Type == "image_url" && p.ImageURL != nil {
			out = append(out, *p.ImageURL)
		}
	}
	return out
}

// MarshalJSON implements the OpenAI-compatible string-or-array
// serialization. Text-only content marshals as a JSON string;
// multipart content marshals as a JSON array of part objects.
func (mc MessageContent) MarshalJSON() ([]byte, error) {
	if mc.IsMultipart() {
		return json.Marshal(mc.parts)
	}
	return json.Marshal(mc.text)
}

// UnmarshalJSON accepts either a JSON string or a JSON array of
// part objects, matching the OpenAI vision spec. Anything else is an
// error so malformed payloads surface at decode time rather than
// silently dropping content.
func (mc *MessageContent) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		mc.hasContent = false
		mc.text = ""
		mc.parts = nil
		return nil
	}
	mc.hasContent = true
	if trimmed[0] == '"' {
		// String form: {"role":"user","content":"hello"}
		return json.Unmarshal(trimmed, &mc.text)
	}
	if trimmed[0] == '[' {
		// Array form: {"role":"user","content":[{...},{...}]}
		return json.Unmarshal(trimmed, &mc.parts)
	}
	return errors.New("gateway: message content must be a string or an array of content parts")
}
