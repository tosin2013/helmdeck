// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// registry_factory_test.go (PR G of the v0.25.0 reliability arc)
// covers defaultAdapterFactory — the production seam that picks the
// right adapter from a Server record's Transport field. Tests in
// registry_test.go use WithAdapterFactory to inject fakes; this file
// pins the actual production switch so a future refactor that adds
// a new transport (or renames an existing one) gets a compile-or-
// runtime failure immediately rather than silently routing to nil.

// TestDefaultAdapterFactory_StdioRoutes — stdio transport with valid
// JSON config returns a StdioAdapter; invalid JSON returns an error
// naming the transport.
func TestDefaultAdapterFactory_StdioRoutes(t *testing.T) {
	cfg, _ := json.Marshal(StdioConfig{Command: "/usr/local/bin/some-mcp-server"})
	srv := &Server{Transport: TransportStdio, Config: cfg}
	adapter, err := defaultAdapterFactory(srv)
	if err != nil {
		t.Fatalf("StdioAdapter: %v", err)
	}
	if adapter == nil {
		t.Fatal("adapter is nil")
	}

	// Malformed config → typed error.
	bad := &Server{Transport: TransportStdio, Config: json.RawMessage(`{not-json`)}
	_, berr := defaultAdapterFactory(bad)
	if berr == nil || !strings.Contains(berr.Error(), "stdio") {
		t.Errorf("malformed stdio config should error: %v", berr)
	}
}

// TestDefaultAdapterFactory_SSERoutes — same gate for SSE transport.
func TestDefaultAdapterFactory_SSERoutes(t *testing.T) {
	cfg, _ := json.Marshal(SSEConfig{URL: "https://mcp.example.com/sse"})
	srv := &Server{Transport: TransportSSE, Config: cfg}
	adapter, err := defaultAdapterFactory(srv)
	if err != nil {
		t.Fatalf("SSEAdapter: %v", err)
	}
	if adapter == nil {
		t.Fatal("adapter is nil")
	}

	bad := &Server{Transport: TransportSSE, Config: json.RawMessage(`{not-json`)}
	_, berr := defaultAdapterFactory(bad)
	if berr == nil || !strings.Contains(berr.Error(), "sse") {
		t.Errorf("malformed sse config should error: %v", berr)
	}
}

// TestDefaultAdapterFactory_WebSocketRoutes — same gate for WebSocket.
func TestDefaultAdapterFactory_WebSocketRoutes(t *testing.T) {
	cfg, _ := json.Marshal(WebSocketConfig{URL: "wss://mcp.example.com/ws"})
	srv := &Server{Transport: TransportWebSocket, Config: cfg}
	adapter, err := defaultAdapterFactory(srv)
	if err != nil {
		t.Fatalf("WebSocketAdapter: %v", err)
	}
	if adapter == nil {
		t.Fatal("adapter is nil")
	}

	bad := &Server{Transport: TransportWebSocket, Config: json.RawMessage(`{not-json`)}
	_, berr := defaultAdapterFactory(bad)
	if berr == nil || !strings.Contains(berr.Error(), "websocket") {
		t.Errorf("malformed websocket config should error: %v", berr)
	}
}

// TestDefaultAdapterFactory_UnknownTransport — anything outside the
// closed Transport set returns a typed "unknown transport" error
// naming the unrecognized value. Critical: a typo'd transport in
// the DB row (e.g. operator entered "stio" instead of "stdio")
// must surface a clear error, not silently route to nil.
func TestDefaultAdapterFactory_UnknownTransport(t *testing.T) {
	srv := &Server{Transport: Transport("bogus"), Config: json.RawMessage(`{}`)}
	_, err := defaultAdapterFactory(srv)
	if err == nil {
		t.Fatal("unknown transport should error")
	}
	if !strings.Contains(err.Error(), "unknown transport") {
		t.Errorf("error should say 'unknown transport': %v", err)
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error should name the bad value: %v", err)
	}
}
