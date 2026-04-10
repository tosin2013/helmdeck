// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package api

// mcp_sse.go (T302a) — Server-Sent Events MCP transport.
//
// Background: T302 shipped helmdeck-as-MCP-server over WebSocket
// at /api/v1/mcp/ws. WS works for any MCP client willing to speak
// it but most containerized agent runtimes (OpenClaw, in particular)
// connect to remote MCP servers via the URL-based SSE transport
// from the MCP spec, not WebSocket. Without an HTTP-side MCP
// transport, the only way to put helmdeck next to an existing
// containerized client is to bake the helmdeck-mcp stdio bridge
// into the client's image — fragile and per-client.
//
// T302a closes that gap. PackServer.Serve already takes
// (ctx, io.Reader, io.Writer) — it's transport-agnostic by design,
// and internal/mcp/server.go:20-38 explicitly calls out HTTP/SSE
// as a future second transport. This file is the wrapper that
// adapts the SSE pair (GET stream + POST messages) to that
// io.Reader/io.Writer surface.
//
// Wire shape per MCP SSE spec:
//   GET  /api/v1/mcp/sse                       — opens the SSE
//        stream. The first frame is
//          event: endpoint
//          data: /api/v1/mcp/sse/message?sessionId=<uuid>
//        so the client knows where to POST messages.
//   POST /api/v1/mcp/sse/message?sessionId=…   — one JSON-RPC
//        request per call; the server routes the body into the
//        paired session's PackServer reader and the response
//        comes back over the SSE stream as
//          event: message
//          data: <json>
//
// JWT enforcement is shared with every other /api/v1/* route via
// IsProtectedPath in router.go.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/tosin2013/helmdeck/internal/mcp"
)

// sseSession is one live SSE pairing. PackServer reads JSON-RPC
// requests off `in` and writes responses to `out`; the GET handler
// drains `out` onto the SSE stream and the POST handler writes
// inbound bodies into `inW`. cancel() tears down PackServer when
// the SSE stream closes.
type sseSession struct {
	id     string
	in     *io.PipeReader
	inW    *io.PipeWriter
	out    chan []byte
	cancel context.CancelFunc
}

// sseSessionRegistry holds the live sessions keyed by id. Both the
// GET handler (creator) and the POST handler (consumer) coordinate
// through it. Plain map + mutex — sync.Map is overkill for the
// lifecycle pattern here.
type sseSessionRegistry struct {
	mu       sync.Mutex
	sessions map[string]*sseSession
}

func newSSERegistry() *sseSessionRegistry {
	return &sseSessionRegistry{sessions: make(map[string]*sseSession)}
}

func (r *sseSessionRegistry) put(s *sseSession) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions[s.id] = s
}

func (r *sseSessionRegistry) get(id string) (*sseSession, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[id]
	return s, ok
}

func (r *sseSessionRegistry) drop(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, id)
}

// registerMCPSSERoutes mounts /api/v1/mcp/sse and
// /api/v1/mcp/sse/message. Returns 503 stubs when packs aren't
// configured (matches the WS handler pattern).
func registerMCPSSERoutes(mux *http.ServeMux, deps Deps) {
	if deps.PackRegistry == nil || deps.PackEngine == nil {
		stub := func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusServiceUnavailable, "mcp_server_unavailable", "pack registry not configured")
		}
		mux.HandleFunc("/api/v1/mcp/sse", stub)
		mux.HandleFunc("/api/v1/mcp/sse/message", stub)
		return
	}
	var mcpOpts []mcp.PackServerOption
	if deps.ArtifactStore != nil {
		mcpOpts = append(mcpOpts, mcp.WithArtifacts(deps.ArtifactStore))
	}
	server := mcp.NewPackServer(deps.PackRegistry, deps.PackEngine, mcpOpts...)
	registry := newSSERegistry()

	mux.HandleFunc("GET /api/v1/mcp/sse", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeError(w, http.StatusInternalServerError, "no_flusher", "response writer does not support streaming")
			return
		}
		// SSE headers per the spec. Disable proxy buffering with
		// X-Accel-Buffering for nginx-style fronts; Cache-Control
		// no-store for any aggressive intermediary.
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		sess := newSSESession()
		sess.cancel = cancel
		registry.put(sess)
		defer registry.drop(sess.id)
		defer sess.inW.Close()

		// Per MCP SSE spec: first frame names the message endpoint
		// the client should POST to. The sessionId is opaque to the
		// client; we just need it to round-trip.
		fmt.Fprintf(w, "event: endpoint\ndata: /api/v1/mcp/sse/message?sessionId=%s\n\n", sess.id)
		flusher.Flush()

		go func() {
			defer close(sess.out)
			_ = server.Serve(ctx, sess.in, &chanWriter{out: sess.out})
		}()

		// Drain pump — PackServer writes responses to sess.out;
		// each message becomes one SSE event. Loop exits when
		// the channel closes (Serve returned) or the request
		// context is cancelled (client went away).
		for {
			select {
			case msg, ok := <-sess.out:
				if !ok {
					return
				}
				if _, err := fmt.Fprintf(w, "event: message\ndata: %s\n\n", msg); err != nil {
					return
				}
				flusher.Flush()
			case <-ctx.Done():
				return
			}
		}
	})

	mux.HandleFunc("POST /api/v1/mcp/sse/message", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("sessionId")
		if id == "" {
			writeError(w, http.StatusBadRequest, "missing_session", "sessionId query parameter required")
			return
		}
		sess, ok := registry.get(id)
		if !ok {
			writeError(w, http.StatusNotFound, "unknown_session", "session not found or already closed")
			return
		}
		// Body is one JSON-RPC frame. Append a newline so
		// PackServer's bufio.Scanner sees a complete line even when
		// the client omits the trailing \n.
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeError(w, http.StatusBadRequest, "read_body", err.Error())
			return
		}
		if len(body) == 0 || body[len(body)-1] != '\n' {
			body = append(body, '\n')
		}
		if _, err := sess.inW.Write(body); err != nil {
			writeError(w, http.StatusInternalServerError, "session_write", err.Error())
			return
		}
		// 202 Accepted — the response comes back asynchronously on
		// the SSE stream, not on this POST.
		w.WriteHeader(http.StatusAccepted)
	})
}

// newSSESession constructs an unwired session. cancel is set later
// by the GET handler once the request context is split off.
func newSSESession() *sseSession {
	pr, pw := io.Pipe()
	return &sseSession{
		id:  newSessionID(),
		in:  pr,
		inW: pw,
		out: make(chan []byte, 16),
	}
}

func newSessionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// chanWriter adapts a chan []byte to io.Writer so PackServer can
// hand it as the output sink. Each Write call corresponds to one
// JSON-RPC frame followed by '\n' (PackServer's framing
// convention); we strip the trailing newline since SSE has its own
// data: framing.
type chanWriter struct {
	out chan []byte
}

func (c *chanWriter) Write(p []byte) (int, error) {
	// Copy because PackServer may reuse the buffer.
	msg := make([]byte, len(p))
	copy(msg, p)
	// Trim trailing newline — SSE wraps in event:/data: lines.
	if n := len(msg); n > 0 && msg[n-1] == '\n' {
		msg = msg[:n-1]
	}
	if len(msg) == 0 {
		return len(p), nil
	}
	c.out <- msg
	return len(p), nil
}

// compile-time check: chanWriter satisfies io.Writer.
var _ io.Writer = (*chanWriter)(nil)
