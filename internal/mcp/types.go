// Package mcp implements the MCP (Model Context Protocol) server
// registry described in T301 / ADR 006. helmdeck consumes external
// MCP servers as a client here — the helmdeck-as-MCP-server flip
// side lives in T302 (cmd/helmdeck-mcp + an internal exporter that
// turns every Capability Pack into a typed MCP tool).
//
// Three transports are defined: stdio (spawn a child process,
// JSON-RPC over its stdin/stdout), sse (HTTP+SSE per the MCP spec
// 2024-11 transport), and websocket (a single bidirectional
// connection). Stdio is the only transport with a real
// implementation in T301; sse and websocket return ErrNotImplemented
// until the consumer demand justifies the extra dependencies. The
// adapter interface is the same shape across all three so swapping
// is a one-line change at registration time.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Transport names the wire protocol used to talk to an MCP server.
// Stored verbatim in the database CHECK constraint, so adding a new
// transport requires a migration.
type Transport string

const (
	TransportStdio     Transport = "stdio"
	TransportSSE       Transport = "sse"
	TransportWebSocket Transport = "websocket"
)

// ErrNotImplemented is returned by transport adapters that have not
// landed yet. Tests assert against this directly so a future change
// that quietly breaks SSE/WebSocket support shows up loudly.
var ErrNotImplemented = errors.New("mcp: transport not implemented")

// Server is the public record returned by the registry. It deliberately
// excludes ConfigJSON's sensitive fields when serialized to clients —
// see Server.MarshalJSON in store.go for the redaction rule.
type Server struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Transport   Transport       `json:"transport"`
	Config      json.RawMessage `json:"config"`
	Enabled     bool            `json:"enabled"`
	Manifest    *Manifest       `json:"manifest,omitempty"`
	CachedAt    *time.Time      `json:"cached_at,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// Manifest is the cached `tools/list` response from an MCP server.
// MCP returns more than just tools (resources, prompts) but T301
// only consumes tools — that's the surface T302's bridge needs.
type Manifest struct {
	Tools []Tool `json:"tools"`
}

// Tool mirrors the MCP `tool` shape: a name, a free-form description,
// and a JSON Schema document for the input parameters. We keep the
// schema as RawMessage rather than parsing it because the schemas
// are passed straight through to whichever client (Claude Code,
// Gemini CLI, etc.) is going to invoke them.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// StdioConfig is the parsed Config for TransportStdio. JSON shape:
//
//	{"command":"npx","args":["-y","@some/mcp-server"],"env":{"K":"V"}}
type StdioConfig struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// SSEConfig and WebSocketConfig are placeholders so the JSON shape
// is documented even before the transports are implemented.
type SSEConfig struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

type WebSocketConfig struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
}

// Adapter is the contract every transport implementation satisfies.
// FetchManifest is the only operation T301 needs — calling tools/list
// and returning the result. Subsequent tasks (T302's bridge tool
// execution) will extend this interface with InvokeTool, but the
// contract stays narrow until then so adding a transport is small.
type Adapter interface {
	FetchManifest(ctx context.Context) (*Manifest, error)
}
