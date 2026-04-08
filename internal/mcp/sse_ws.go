package mcp

import "context"

// SSE and WebSocket transports are stubs in T301. The Adapter
// interface is the same shape, so when consumer demand justifies
// the extra dependencies (eventsource client, gorilla/websocket)
// the only thing that needs to change is each FetchManifest body —
// the registry, REST surface, and storage layer all already speak
// to Adapter generically.

// SSEAdapter is a placeholder. Tests assert ErrNotImplemented so a
// future quiet regression that swaps in a broken impl shows up.
type SSEAdapter struct {
	cfg SSEConfig
}

func NewSSEAdapter(cfg SSEConfig) *SSEAdapter { return &SSEAdapter{cfg: cfg} }

func (s *SSEAdapter) FetchManifest(ctx context.Context) (*Manifest, error) {
	return nil, ErrNotImplemented
}

// WebSocketAdapter is a placeholder; see SSEAdapter for the rationale.
type WebSocketAdapter struct {
	cfg WebSocketConfig
}

func NewWebSocketAdapter(cfg WebSocketConfig) *WebSocketAdapter { return &WebSocketAdapter{cfg: cfg} }

func (w *WebSocketAdapter) FetchManifest(ctx context.Context) (*Manifest, error) {
	return nil, ErrNotImplemented
}
