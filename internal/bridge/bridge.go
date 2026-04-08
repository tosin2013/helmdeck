// Package bridge implements the helmdeck-mcp stdio↔WebSocket bridge
// described in T303 / ADRs 025 + 030. The bridge runs as a child
// process under an agent client (Claude Code, Claude Desktop,
// OpenClaw, Gemini CLI), reads MCP JSON-RPC requests from stdin,
// forwards them to the platform's /api/v1/mcp/ws endpoint, and
// streams responses back to stdout.
//
// Two design points worth calling out:
//
//  1. The bridge is intentionally dumb. It does not parse the
//     JSON-RPC envelope, does not maintain a request id table,
//     and does not handle MCP-level retries. The platform server
//     (T302) is the source of truth; making the bridge stateful
//     would create a second place where MCP semantics could drift.
//
//  2. The frame mapping is "one stdin line == one WebSocket text
//     frame, one WebSocket text frame == one stdout line". This
//     mirrors PackServer's framing exactly, so any client that
//     speaks stdio MCP works against the bridge unchanged.
package bridge

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// Config holds the bridge runtime configuration. URL is the
// helmdeck control plane base URL (http or https) — the bridge
// internally rewrites the scheme to ws/wss and appends the MCP
// server path. Token is a bearer JWT minted from the API Tokens
// panel (or `helmdeck-control-plane -mint-token`).
type Config struct {
	URL   string
	Token string

	// Version is this bridge's own release tag (e.g. "v0.2.0").
	// Used for the T304 skew check against the platform's
	// /api/v1/bridge/version endpoint. Empty disables the check
	// — useful for unreleased dev builds where the version is
	// "dev" and would always look older than the platform.
	Version string

	// MCPPath is the WebSocket endpoint path. Exposed for tests
	// and for future deployments that mount helmdeck under a
	// non-default prefix. Defaults to /api/v1/mcp/ws.
	MCPPath string

	// Stdin/Stdout/Stderr are the streams the bridge talks on.
	// Tests inject in-memory pipes; the binary uses os.Std*.
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// ErrMissingConfig is returned by Run when URL or Token is empty.
var ErrMissingConfig = errors.New("bridge: HELMDECK_URL and HELMDECK_TOKEN must be set")

// Run dials the WebSocket endpoint and pumps frames between
// stdin/stdout and the connection until either side closes or
// ctx is cancelled. Returns nil on a clean shutdown (EOF on
// stdin), the underlying error otherwise.
func Run(ctx context.Context, cfg Config) error {
	if cfg.URL == "" || cfg.Token == "" {
		return ErrMissingConfig
	}
	if cfg.MCPPath == "" {
		cfg.MCPPath = "/api/v1/mcp/ws"
	}
	if cfg.Stdin == nil {
		cfg.Stdin = nopReader{}
	}
	if cfg.Stdout == nil {
		cfg.Stdout = io.Discard
	}
	if cfg.Stderr == nil {
		cfg.Stderr = io.Discard
	}

	wsURL, err := toWebSocketURL(cfg.URL, cfg.MCPPath)
	if err != nil {
		return fmt.Errorf("bridge: url: %w", err)
	}

	// T304: probe the platform's bridge version endpoint before
	// dialing. Failures here are non-fatal — they just disable
	// the skew check, because the bridge must still work against
	// older platforms that haven't shipped the endpoint.
	if cfg.Version != "" && cfg.Version != "dev" {
		if info, vErr := fetchPlatformVersion(ctx, cfg.URL); vErr == nil && info.MinRecommended != "" {
			if compareVersions(cfg.Version, info.MinRecommended) < 0 {
				fmt.Fprintf(cfg.Stderr,
					"helmdeck-mcp: WARNING — bridge %s is older than the platform's minimum recommended (%s). Update via brew/scoop/npm.\n",
					cfg.Version, info.MinRecommended)
			}
		} else if vErr != nil {
			fmt.Fprintf(cfg.Stderr, "helmdeck-mcp: version probe failed (%v); skipping skew check\n", vErr)
		}
	}

	// gobwas/ws's Dialer.Header takes a func that writes raw
	// header bytes. We piggyback the bearer token here so the
	// upgrade request carries auth — the platform's IsProtectedPath
	// covers /api/v1/mcp/ws so a missing token returns 401 at
	// upgrade time, not after the first frame.
	dialer := ws.Dialer{
		Header: ws.HandshakeHeaderHTTP(http.Header{
			"Authorization": []string{"Bearer " + cfg.Token},
		}),
	}
	conn, _, _, err := dialer.Dial(ctx, wsURL)
	if err != nil {
		return fmt.Errorf("bridge: dial %s: %w", wsURL, err)
	}
	defer conn.Close()

	fmt.Fprintf(cfg.Stderr, "helmdeck-mcp: connected to %s\n", wsURL)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Two pumps run concurrently. We watch both with a sync.Once
	// so the first error wins and the other goroutine unblocks
	// when we close the connection.
	var (
		wg      sync.WaitGroup
		once    sync.Once
		runErr  error
	)
	finish := func(err error) {
		once.Do(func() {
			runErr = err
			cancel()
			_ = conn.Close()
		})
	}

	// stdin -> WebSocket
	wg.Add(1)
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(cfg.Stdin)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			if ctx.Err() != nil {
				return
			}
			line := sc.Bytes()
			if len(line) == 0 {
				continue
			}
			if err := wsutil.WriteClientText(conn, line); err != nil {
				finish(fmt.Errorf("bridge: write ws: %w", err))
				return
			}
		}
		// Clean EOF on stdin: tell the platform we're done. This
		// translates to a clean WebSocket close so the server's
		// PackServer.Serve loop exits.
		if err := sc.Err(); err != nil {
			finish(fmt.Errorf("bridge: read stdin: %w", err))
			return
		}
		finish(nil)
	}()

	// WebSocket -> stdout
	wg.Add(1)
	go func() {
		defer wg.Done()
		w := bufio.NewWriter(cfg.Stdout)
		defer w.Flush()
		for {
			if ctx.Err() != nil {
				return
			}
			msg, op, err := wsutil.ReadServerData(conn)
			if err != nil {
				// Closed connection is the expected termination,
				// not an error worth surfacing.
				if errors.Is(err, io.EOF) || isClosedErr(err) {
					finish(nil)
					return
				}
				finish(fmt.Errorf("bridge: read ws: %w", err))
				return
			}
			if op != ws.OpText && op != ws.OpBinary {
				continue
			}
			if _, err := w.Write(msg); err != nil {
				finish(fmt.Errorf("bridge: write stdout: %w", err))
				return
			}
			if !endsWithNewline(msg) {
				_ = w.WriteByte('\n')
			}
			if err := w.Flush(); err != nil {
				finish(fmt.Errorf("bridge: flush stdout: %w", err))
				return
			}
		}
	}()

	wg.Wait()
	return runErr
}

// toWebSocketURL converts an http(s):// base URL into a ws(s)://
// URL with the MCP path appended. We accept both schemes so
// operators can paste whatever HELMDECK_URL they already have for
// the REST API into the bridge config without thinking about
// transport rewriting.
func toWebSocketURL(base, path string) (string, error) {
	switch {
	case strings.HasPrefix(base, "https://"):
		return "wss://" + strings.TrimPrefix(base, "https://") + path, nil
	case strings.HasPrefix(base, "http://"):
		return "ws://" + strings.TrimPrefix(base, "http://") + path, nil
	case strings.HasPrefix(base, "wss://") || strings.HasPrefix(base, "ws://"):
		return base + path, nil
	}
	return "", fmt.Errorf("scheme must be http(s)/ws(s), got %q", base)
}

func endsWithNewline(b []byte) bool {
	return len(b) > 0 && b[len(b)-1] == '\n'
}

// isClosedErr checks for the family of errors gobwas/ws and the
// net package surface when a peer drops a connection cleanly.
// Matching on error text is gross but the alternative is exposing
// gobwas/ws internals all the way up the call stack, which would
// couple the bridge to a specific transport library version.
func isClosedErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "use of closed network connection") ||
		strings.Contains(msg, "connection reset by peer") ||
		strings.Contains(msg, "EOF")
}

type nopReader struct{}

func (nopReader) Read(p []byte) (int, error) { return 0, io.EOF }
