// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package api

// mcp_qmd_sse.go — Server-Sent Events MCP transport for the QMD
// memory-corpus endpoint (ADR 048 PR #3).
//
// Sibling to mcp_sse.go which serves the main PackServer at
// /api/v1/mcp/sse. This file mounts a NARROW MCP endpoint at
// /api/v1/mcp/qmd/sse that exposes only the `query` tool defined in
// internal/mcp/qmd_server.go. OpenClaw's MCPorter daemon dials this
// URL when configured with `memory.qmd.mcporter.enabled=true` and
// `serverName=helmdeck`; the corpus chunks returned merge into
// OpenClaw's `memory_search` alongside the user's conversational
// memory.
//
// Why a separate route + server instead of multiplexing on the
// existing /api/v1/mcp/sse:
//   - MCPorter expects the tool name to be exactly `query`. Adding a
//     bare `query` to the main PackServer would pollute the pack
//     namespace and collide with future dotted pack names.
//   - The QMD endpoint should be query-only by design; the narrow
//     surface keeps the security review tractable.
//
// Behavior matches the main SSE handler 1:1 (session lifecycle, GET
// stream + POST message pairing, 15s keepalives) so any quirks
// learned operating mcp_sse.go transfer here.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/tosin2013/helmdeck/internal/mcp"
)

// registerMCPQMDSSERoutes mounts /api/v1/mcp/qmd/sse and
// /api/v1/mcp/qmd/sse/message. When no memory store is wired the
// route returns 503 — the corpus has nothing to surface.
func registerMCPQMDSSERoutes(mux *http.ServeMux, deps Deps) {
	if deps.PackEngine == nil || deps.PackEngine.MemoryStore() == nil {
		stub := func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusServiceUnavailable, "qmd_unavailable",
				"helmdeck memory store not configured; QMD corpus bridge disabled")
		}
		mux.HandleFunc("/api/v1/mcp/qmd/sse", stub)
		mux.HandleFunc("/api/v1/mcp/qmd/sse/message", stub)
		return
	}
	server := mcp.NewQMDServer(deps.PackEngine.MemoryStore())
	if server == nil {
		// Defensive belt-and-braces — NewQMDServer returns nil only on
		// nil store, which we already gated above.
		return
	}
	registry := newSSERegistry()

	mux.HandleFunc("GET /api/v1/mcp/qmd/sse", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeError(w, http.StatusInternalServerError, "no_flusher", "response writer does not support streaming")
			return
		}
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

		fmt.Fprintf(w, "event: endpoint\ndata: /api/v1/mcp/qmd/sse/message?sessionId=%s\n\n", sess.id)
		flusher.Flush()

		go func() {
			defer close(sess.out)
			_ = server.Serve(ctx, sess.in, &chanWriter{out: sess.out})
		}()

		keepalive := time.NewTicker(15 * time.Second)
		defer keepalive.Stop()
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
			case <-keepalive.C:
				if _, err := fmt.Fprintf(w, ": keepalive\n\n"); err != nil {
					return
				}
				flusher.Flush()
			case <-ctx.Done():
				return
			}
		}
	})

	mux.HandleFunc("POST /api/v1/mcp/qmd/sse/message", func(w http.ResponseWriter, r *http.Request) {
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
		w.WriteHeader(http.StatusAccepted)
	})
}
