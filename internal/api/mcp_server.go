package api

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"net/http"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"

	"github.com/tosin2013/helmdeck/internal/mcp"
)

// registerMCPServerRoute mounts the helmdeck-as-MCP WebSocket
// endpoint at /api/v1/mcp/ws (T302). The bridge binary in T303 is
// the canonical client; agents speak to helmdeck via the bridge,
// not directly to this WebSocket, but exposing it on the public
// REST surface keeps the same JWT enforcement and audit log around
// every tools/call.
func registerMCPServerRoute(mux *http.ServeMux, deps Deps) {
	if deps.PackRegistry == nil || deps.PackEngine == nil {
		mux.HandleFunc("/api/v1/mcp/ws", func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusServiceUnavailable, "mcp_server_unavailable", "pack registry not configured")
		})
		return
	}
	var mcpOpts []mcp.PackServerOption
	if deps.ArtifactStore != nil {
		mcpOpts = append(mcpOpts, mcp.WithArtifacts(deps.ArtifactStore))
	}
	server := mcp.NewPackServer(deps.PackRegistry, deps.PackEngine, mcpOpts...)
	mux.HandleFunc("/api/v1/mcp/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, _, _, err := ws.UpgradeHTTP(r, w)
		if err != nil {
			// Upgrade may have already written a response — bail
			// without trying to writeError on top.
			return
		}
		defer conn.Close()

		// Adapt the WebSocket conn to the line-delimited io.Reader/
		// io.Writer the PackServer expects. Each MCP request /
		// response is one JSON-RPC frame; we read text frames into
		// a pipe and write outgoing frames whenever the server
		// flushes a line. The bidirectional bridge runs both
		// directions concurrently and tears down on first error.
		runWSSession(r.Context(), conn, server)
	})
}

// runWSSession bridges a single WebSocket connection to a
// mcp.PackServer.Serve call. Inbound text frames become bytes on
// the server's reader; outbound bytes become text frames on the
// WebSocket. The split keeps PackServer transport-agnostic.
func runWSSession(ctx context.Context, conn net.Conn, server *mcp.PackServer) {
	// Frames-in -> server.Serve reader.
	pr, pw := io.Pipe()
	defer pr.Close()
	defer pw.Close()

	// server.Serve writer -> wsutil.WriteServerText. We buffer at
	// the line boundary because PackServer writes one full
	// JSON-RPC frame followed by '\n' per response, and a
	// WebSocket message must contain exactly one frame.
	out := &lineFlusher{conn: conn}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Reader pump: pull WS frames, write into the pipe. Stops on
	// any error (clean close, network drop, context cancel).
	go func() {
		defer pw.Close()
		for {
			if ctx.Err() != nil {
				return
			}
			msg, op, err := wsutil.ReadClientData(conn)
			if err != nil {
				return
			}
			// Only text and binary carry MCP payloads. Control
			// frames (ping/pong/close) are handled inside
			// wsutil.ReadClientData.
			if op != ws.OpText && op != ws.OpBinary {
				continue
			}
			// Ensure each message ends with a newline so the
			// PackServer's bufio.Scanner sees a complete line even
			// if the client omits the trailing \n.
			if !bytes.HasSuffix(msg, []byte("\n")) {
				msg = append(msg, '\n')
			}
			if _, err := pw.Write(msg); err != nil {
				return
			}
		}
	}()

	// Run the server inline so the goroutine returns when Serve
	// does — Serve owns the request lifetime, the reader pump is
	// only there to feed it.
	_ = server.Serve(ctx, pr, out)
	cancel()
}

// lineFlusher buffers PackServer's stream-style writes and emits
// one WebSocket text frame per complete line. PackServer always
// terminates a frame with '\n', so the line boundary is the
// natural cut point.
type lineFlusher struct {
	conn net.Conn
	buf  bytes.Buffer
}

func (l *lineFlusher) Write(p []byte) (int, error) {
	l.buf.Write(p)
	for {
		line, err := l.buf.ReadBytes('\n')
		if err != nil {
			// No newline yet; put the partial back so the next
			// Write can extend it.
			l.buf.Reset()
			l.buf.Write(line)
			break
		}
		// Strip the trailing newline before sending — WebSocket
		// frames carry their own boundaries.
		payload := line
		if len(payload) > 0 && payload[len(payload)-1] == '\n' {
			payload = payload[:len(payload)-1]
		}
		if err := wsutil.WriteServerText(l.conn, payload); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// compile-time assertion: bufio.Reader still satisfies io.Reader.
var _ io.Reader = (*bufio.Reader)(nil)
