// Package gateway implements the OpenAI-compatible AI facade described in
// ADR 005. T201 ships the routing surface — `/v1/models` and
// `/v1/chat/completions` — plus a Provider interface that real adapters
// (T202) plug into. Until T202 lands, callers register stub providers.
//
// Routing rule: the request's `model` field MUST use `provider/model`
// syntax (e.g. `anthropic/claude-sonnet-4-6`). The gateway splits on the
// first `/`, looks up the provider in the registry, and forwards the
// request with the bare model name. This keeps a single OpenAI-compatible
// endpoint in front of every backend without per-provider URLs.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/tosin2013/helmdeck/internal/telemetry"
)

// ErrUnknownProvider is returned when the `provider/` prefix in a request
// model has no registered adapter. Mapped to HTTP 404 by the handler.
var ErrUnknownProvider = errors.New("unknown provider")

// ErrInvalidModel is returned when the model field is missing or does not
// contain a `provider/model` separator. Mapped to HTTP 400.
var ErrInvalidModel = errors.New("model must use provider/model syntax")

// Message mirrors the OpenAI chat message shape. Content is the
// MessageContent sum type defined in content.go — it accepts either
// a plain text string (text-only chat, the legacy path) or an
// ordered array of typed content parts (text + images, the OpenAI
// vision spec). Use TextContent / MultipartContent constructors at
// call sites; provider adapters branch on Content.IsMultipart() to
// translate to whatever shape their upstream API speaks.
type Message struct {
	Role    string         `json:"role"`
	Content MessageContent `json:"content"`
	Name    string         `json:"name,omitempty"`
}

// ChatRequest is the subset of the OpenAI /v1/chat/completions request
// body the facade understands. Unknown fields are ignored on decode (the
// HTTP layer uses encoding/json's default behavior) and forwarded
// opaquely via Extra so providers can pass through provider-specific
// knobs without the gateway needing to know about them.
//
// T807f: Tools + ToolChoice carry provider-agnostic tool-use definitions
// so callers (vision.*, web.test, any future tool-using pack) can hand
// in a single tool schema and have each provider adapter translate it to
// the provider's native wire format — Anthropic's top-level `tools: [{name,
// description, input_schema}]`, OpenAI's `tools: [{type: "function", ...}]`,
// Gemini's `tools: [{functionDeclarations: [...]}]`. Both fields are
// optional; unset means "no tool use" and every adapter short-circuits the
// translation path. Ollama and Deepseek ignore these fields (tool-agnostic
// by design; packs fall back to the JSON-prompt path for local models).
type ChatRequest struct {
	Model       string           `json:"model"`
	Messages    []Message        `json:"messages"`
	Temperature *float64         `json:"temperature,omitempty"`
	MaxTokens   *int             `json:"max_tokens,omitempty"`
	Stream      bool             `json:"stream,omitempty"`
	Tools       []ToolDefinition `json:"tools,omitempty"`
	ToolChoice  *ToolChoice      `json:"tool_choice,omitempty"`
}

// ToolDefinition is a provider-agnostic tool the LLM may call. Name
// uniquely identifies it in the current request; Description is plain
// English for the model; InputSchema is a JSON Schema document
// describing the tool's arguments, handed to the provider verbatim.
//
// Per-provider translation lives in each adapter — this struct never
// hits the wire directly, it's always re-shaped into the upstream
// format in ChatCompletion().
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// ToolChoice constrains which tool (if any) the model may emit.
// Mode is "auto" (model picks), "any" / "required" (must call a tool),
// "none" (plain-text reply), or "tool" (must call the named tool).
// When Mode is "tool", Name identifies the specific tool to require.
// Nil ToolChoice is equivalent to Mode "auto".
type ToolChoice struct {
	Mode string `json:"mode"`
	Name string `json:"name,omitempty"`
}

// Choice is one completion alternative in a ChatResponse.
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// Usage reports token accounting in the OpenAI shape. Providers that do
// not return token counts should leave the fields zero.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatResponse mirrors the OpenAI /v1/chat/completions response body.
type ChatResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

// Model describes a single model entry returned by /v1/models. The ID
// uses the same `provider/model` syntax accepted by chat completions so
// clients can copy a value from /v1/models straight into a request.
type Model struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
	Created int64  `json:"created"`
}

// Provider is the adapter contract every backend implements. T202 ships
// the real Anthropic, Gemini, OpenAI, Ollama, and Deepseek providers;
// for T201 we accept any in-process implementation (including test
// stubs) so the routing surface can ship and be exercised independently.
//
// Implementations receive the bare model name (the part after the first
// `/`), not the full provider/model string. The Name reported by Models
// MUST equal the registry key — the handler reattaches it as the prefix
// when echoing the model in /v1/models output.
type Provider interface {
	// Name returns the registry key (e.g. "anthropic"). Stable for the
	// lifetime of the process.
	Name() string

	// Models lists the bare model identifiers this provider serves. The
	// gateway prefixes each entry with `Name() + "/"` before returning
	// it to clients.
	Models(ctx context.Context) ([]string, error)

	// ChatCompletion executes a chat request. The model field on req has
	// already been stripped of the provider prefix.
	ChatCompletion(ctx context.Context, req ChatRequest) (ChatResponse, error)
}

// Registry holds the set of providers the gateway routes to. It is
// safe for concurrent use; T207's pack registry will follow the same
// pattern so hot-loading semantics are consistent across subsystems.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
	// Recorder is the optional CallRecorder that captures every
	// Dispatch outcome into provider_calls (T607). Nil means
	// "discard" — Dispatch never panics on a nil recorder.
	Recorder CallRecorder
}

// NewRegistry returns an empty Registry. Recorder defaults to
// NoopRecorder so call sites can ignore the metrics path entirely
// when they don't need it (tests, dev mode without a database).
func NewRegistry() *Registry {
	return &Registry{providers: make(map[string]Provider), Recorder: NoopRecorder{}}
}

// Register adds or replaces a provider. The key is p.Name(); registering
// the same name twice replaces the previous entry, which is what the
// hot-reload story in ADR 005 calls for.
func (r *Registry) Register(p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[p.Name()] = p
}

// Get returns the provider registered under name, or false if absent.
func (r *Registry) Get(name string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[name]
	return p, ok
}

// List returns the registered providers in unspecified order. Callers
// that need stable ordering must sort the result themselves.
func (r *Registry) List() []Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Provider, 0, len(r.providers))
	for _, p := range r.providers {
		out = append(out, p)
	}
	return out
}

// SplitModel parses a `provider/model` string. The split is on the first
// `/` only — model identifiers themselves often contain slashes
// (e.g. `ollama/library/llama3`), so a naive Split would corrupt them.
func SplitModel(full string) (provider, model string, err error) {
	full = strings.TrimSpace(full)
	if full == "" {
		return "", "", ErrInvalidModel
	}
	idx := strings.Index(full, "/")
	if idx <= 0 || idx == len(full)-1 {
		return "", "", ErrInvalidModel
	}
	return full[:idx], full[idx+1:], nil
}

// Dispatch routes a ChatRequest to the appropriate provider. It strips
// the provider prefix from req.Model before forwarding, then re-attaches
// it on the response so clients see the same identifier they sent.
//
// T510: every dispatch is wrapped in an OTel span carrying the GenAI
// semantic-convention attributes (gen_ai.system, gen_ai.request.model,
// gen_ai.usage.*). The tracer is the helmdeck telemetry no-op when
// OTel isn't configured, so the only overhead in the disabled case
// is one tracer.Start call.
func (r *Registry) Dispatch(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	providerName, bareModel, err := SplitModel(req.Model)
	if err != nil {
		return ChatResponse{}, err
	}
	p, ok := r.Get(providerName)
	if !ok {
		return ChatResponse{}, fmt.Errorf("%w: %s", ErrUnknownProvider, providerName)
	}

	tracer := otel.Tracer("helmdeck/gateway")
	ctx, span := tracer.Start(ctx, "gen_ai.chat",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			telemetry.GenAI.System.String(providerName),
			telemetry.GenAI.OperationName.String("chat"),
			telemetry.GenAI.RequestModel.String(bareModel),
		),
	)
	defer span.End()
	if req.Temperature != nil {
		span.SetAttributes(telemetry.GenAI.RequestTemp.Float64(*req.Temperature))
	}
	if req.MaxTokens != nil {
		span.SetAttributes(telemetry.GenAI.RequestMaxTok.Int(*req.MaxTokens))
	}

	forward := req
	forward.Model = bareModel
	dispatchStart := time.Now()
	resp, err := p.ChatCompletion(ctx, forward)
	latency := time.Since(dispatchStart)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		// T607: record the failure into provider_calls before
		// returning. The recorder is best-effort — a metrics-table
		// write failure must NEVER mask the real upstream error
		// the caller is about to see, so we deliberately discard
		// any error from Record here.
		if r.Recorder != nil {
			_ = r.Recorder.Record(ctx, CallRecord{
				Provider:  providerName,
				Model:     bareModel,
				Status:    "error",
				LatencyMS: latency.Milliseconds(),
				ErrorCode: classifyRecordError(err),
			})
		}
		return ChatResponse{}, err
	}
	if resp.Created == 0 {
		resp.Created = time.Now().Unix()
	}
	if resp.Object == "" {
		resp.Object = "chat.completion"
	}
	// Re-attach the provider prefix so the caller sees the same model
	// identifier they requested.
	resp.Model = providerName + "/" + bareModel

	span.SetAttributes(
		telemetry.GenAI.ResponseModel.String(resp.Model),
		telemetry.GenAI.UsageInputTok.Int(resp.Usage.PromptTokens),
		telemetry.GenAI.UsageOutputTok.Int(resp.Usage.CompletionTokens),
		telemetry.GenAI.UsageTotalTok.Int(resp.Usage.TotalTokens),
	)
	if len(resp.Choices) > 0 && resp.Choices[0].FinishReason != "" {
		span.SetAttributes(telemetry.GenAI.ResponseFinish.String(resp.Choices[0].FinishReason))
	}
	span.SetStatus(codes.Ok, "")

	// T607: record the success after the response is fully
	// shaped (provider prefix re-attached, usage tokens parsed)
	// so the metrics table reflects what the caller actually saw.
	if r.Recorder != nil {
		_ = r.Recorder.Record(ctx, CallRecord{
			Provider:         providerName,
			Model:            bareModel,
			Status:           "success",
			LatencyMS:        latency.Milliseconds(),
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
		})
	}
	return resp, nil
}

// AllModels gathers /v1/models output across every registered provider.
// Errors from individual providers are surfaced — the OpenAI client
// expectation is a single flat list, so we fail fast rather than return
// a partial catalog that could mask a misconfigured backend.
func (r *Registry) AllModels(ctx context.Context) ([]Model, error) {
	now := time.Now().Unix()
	var out []Model
	for _, p := range r.List() {
		ids, err := p.Models(ctx)
		if err != nil {
			return nil, fmt.Errorf("provider %s: %w", p.Name(), err)
		}
		for _, id := range ids {
			out = append(out, Model{
				ID:      p.Name() + "/" + id,
				Object:  "model",
				OwnedBy: p.Name(),
				Created: now,
			})
		}
	}
	return out, nil
}
