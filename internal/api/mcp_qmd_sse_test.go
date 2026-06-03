// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/memory"
	"github.com/tosin2013/helmdeck/internal/packs"
)

// newQMDSSERouter wires the QMD SSE route with an engine that has an
// in-memory MemoryStore — the only condition the route needs to
// switch out of its 503 stub branch.
func newQMDSSERouter(t *testing.T) http.Handler {
	t.Helper()
	store := memory.NewInMemoryStore()
	eng := packs.New(packs.WithMemoryStore(store))
	return NewRouter(Deps{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:    "test",
		PackEngine: eng,
	})
}

// TestMCPQMDSSE_UnavailableWhenNoStore — without a memory store the
// route returns 503 qmd_unavailable on both GET and POST so the
// MCPorter daemon gets a clean signal rather than a connection that
// silently never delivers data.
func TestMCPQMDSSE_UnavailableWhenNoStore(t *testing.T) {
	h := NewRouter(Deps{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
		// PackEngine deliberately nil → stub branch.
	})
	for _, path := range []string{"/api/v1/mcp/qmd/sse", "/api/v1/mcp/qmd/sse/message"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusServiceUnavailable {
			t.Errorf("%s status = %d, want 503", path, rr.Code)
		}
		if !strings.Contains(rr.Body.String(), "qmd_unavailable") {
			t.Errorf("%s body should mention qmd_unavailable: %s", path, rr.Body.String())
		}
	}
}

// TestMCPQMDSSE_HandshakeReturnsEndpoint — opening the SSE stream
// must immediately emit the `endpoint` frame with a session-scoped
// /message URL the client uses for follow-up POSTs.
func TestMCPQMDSSE_HandshakeReturnsEndpoint(t *testing.T) {
	srv := httptest.NewServer(newQMDSSERouter(t))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/mcp/qmd/sse")
	if err != nil {
		t.Fatalf("get sse: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content-type = %q", ct)
	}

	br := bufio.NewReader(resp.Body)
	event, data := readSSEFrame(t, br)
	if event != "endpoint" {
		t.Errorf("first event = %q; want endpoint", event)
	}
	matched, _ := regexp.MatchString(`^/api/v1/mcp/qmd/sse/message\?sessionId=[0-9a-f]+$`, data)
	if !matched {
		t.Errorf("endpoint data = %q; want /api/v1/mcp/qmd/sse/message?sessionId=<hex>", data)
	}
}

// TestMCPQMDSSE_PostUnknownSessionIs404 — POST to /message with a
// sessionId that was never opened (or has already closed) returns 404.
func TestMCPQMDSSE_PostUnknownSessionIs404(t *testing.T) {
	srv := httptest.NewServer(newQMDSSERouter(t))
	defer srv.Close()
	r, err := http.Post(srv.URL+"/api/v1/mcp/qmd/sse/message?sessionId=nope",
		"application/json", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", r.StatusCode)
	}
}

// TestMCPQMDSSE_PostMissingSessionIs400 — sessionId query param is
// required; omitting it is a 400 with missing_session, not a 404
// (the latter would imply "we tried to find it" when we didn't).
func TestMCPQMDSSE_PostMissingSessionIs400(t *testing.T) {
	srv := httptest.NewServer(newQMDSSERouter(t))
	defer srv.Close()
	r, err := http.Post(srv.URL+"/api/v1/mcp/qmd/sse/message", "application/json",
		strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", r.StatusCode)
	}
}

// TestMCPQMDSSE_HandshakeRoundTrip — the QMD MCP server speaks
// JSON-RPC 2.0; this drives a full initialize handshake over the
// SSE transport to exercise the GET/POST plumbing. The narrative
// matches mcp_sse_test's ListAndCallRoundTrip but talks to the
// narrow QMD endpoint instead of the full PackServer.
func TestMCPQMDSSE_HandshakeRoundTrip(t *testing.T) {
	srv := httptest.NewServer(newQMDSSERouter(t))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/mcp/qmd/sse", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get sse: %v", err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)

	_, endpoint := readSSEFrame(t, br)
	if endpoint == "" {
		t.Fatal("no endpoint frame")
	}

	// POST initialize. The handler appends \n if missing, so a
	// payload without trailing newline still parses correctly on
	// the qmd server side.
	r, err := http.Post(srv.URL+endpoint, "application/json",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	if err != nil {
		t.Fatalf("post initialize: %v", err)
	}
	if r.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(r.Body)
		t.Fatalf("post status = %d: %s", r.StatusCode, body)
	}
	_ = r.Body.Close()

	// Expect a response frame on the SSE stream carrying the init result.
	_, data := readSSEFrame(t, br)
	var rpc struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(data), &rpc); err != nil {
		t.Fatalf("unmarshal initialize response: %v (data=%s)", err, data)
	}
	if rpc.JSONRPC != "2.0" || string(rpc.ID) != "1" {
		t.Errorf("init response: jsonrpc=%q id=%s", rpc.JSONRPC, rpc.ID)
	}
	if !strings.Contains(string(rpc.Result), `"protocolVersion"`) {
		t.Errorf("init result missing protocolVersion: %s", rpc.Result)
	}
}
