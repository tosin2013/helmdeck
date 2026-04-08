package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
)

// StdioAdapter spawns the configured command and speaks JSON-RPC
// over its stdin/stdout — the canonical MCP stdio transport. The
// process is short-lived: each FetchManifest invocation spawns a
// fresh child, sends the initialize handshake, calls tools/list,
// and tears down. Long-lived sessions are T302's job, not the
// registry's.
type StdioAdapter struct {
	cfg StdioConfig
}

// NewStdioAdapter constructs an adapter from the parsed config.
func NewStdioAdapter(cfg StdioConfig) *StdioAdapter {
	return &StdioAdapter{cfg: cfg}
}

// FetchManifest spawns the server, performs the MCP initialize
// handshake, calls tools/list, and returns the parsed manifest.
//
// MCP servers expect: initialize (id 1) → notifications/initialized
// (no response) → tools/list (id 2). We do not currently parse the
// initialize response beyond confirming it returned without error;
// the protocolVersion negotiation surface is reserved for T302 once
// version skew matters.
func (s *StdioAdapter) FetchManifest(ctx context.Context) (*Manifest, error) {
	if s.cfg.Command == "" {
		return nil, fmt.Errorf("stdio: command required")
	}
	cmd := exec.CommandContext(ctx, s.cfg.Command, s.cfg.Args...)
	if len(s.cfg.Env) > 0 {
		// Caller-supplied env replaces, not augments — MCP servers
		// often need a clean env to avoid leaking host secrets.
		env := make([]string, 0, len(s.cfg.Env))
		for k, v := range s.cfg.Env {
			env = append(env, k+"="+v)
		}
		cmd.Env = env
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdio: stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdio: stdout: %w", err)
	}
	// Drain stderr in the background so the child doesn't block on
	// a full pipe; we don't surface its output today, but a future
	// "diagnose this MCP server" REST endpoint will want it.
	stderr, _ := cmd.StderrPipe()
	if stderr != nil {
		go io.Copy(io.Discard, stderr)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("stdio: start: %w", err)
	}
	defer func() {
		_ = stdin.Close()
		// Wait reaps the child; ignore the exit error because tools/list
		// only requires a successful response, not a clean exit.
		_ = cmd.Wait()
	}()

	rw := newLineRPC(stdin, stdout)

	// 1) initialize
	if _, err := rw.call(ctx, 1, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "helmdeck", "version": "0.2.0"},
	}); err != nil {
		return nil, fmt.Errorf("stdio: initialize: %w", err)
	}
	// 2) notifications/initialized — no response expected
	if err := rw.notify("notifications/initialized", map[string]any{}); err != nil {
		return nil, fmt.Errorf("stdio: initialized notify: %w", err)
	}
	// 3) tools/list
	raw, err := rw.call(ctx, 2, "tools/list", map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("stdio: tools/list: %w", err)
	}
	var parsed struct {
		Tools []Tool `json:"tools"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("stdio: decode tools/list: %w", err)
	}
	return &Manifest{Tools: parsed.Tools}, nil
}

// lineRPC is a tiny JSON-RPC framer over a line-delimited byte
// stream. The MCP stdio transport sends one JSON object per line —
// no Content-Length headers, no chunking — so a bufio.Scanner is
// the right primitive. Concurrent access is serialized through mu
// because we issue requests sequentially in FetchManifest.
type lineRPC struct {
	mu      sync.Mutex
	w       io.Writer
	scanner *bufio.Scanner
}

func newLineRPC(w io.Writer, r io.Reader) *lineRPC {
	sc := bufio.NewScanner(r)
	// 1 MiB max line — MCP manifests with rich tool schemas can run
	// to hundreds of KB and bufio's default 64 KiB will silently
	// truncate them.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	return &lineRPC{w: w, scanner: sc}
}

func (l *lineRPC) call(ctx context.Context, id int, method string, params any) (json.RawMessage, error) {
	req, err := newRequest(id, method, params)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.writeFrame(req); err != nil {
		return nil, err
	}
	for {
		// Honor caller cancellation between frames so a hung child
		// process doesn't pin the request indefinitely.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		if !l.scanner.Scan() {
			if err := l.scanner.Err(); err != nil {
				return nil, err
			}
			return nil, io.ErrUnexpectedEOF
		}
		var resp rpcResponse
		if err := json.Unmarshal(l.scanner.Bytes(), &resp); err != nil {
			// Skip non-JSON lines (some servers print banners). A
			// strict client would error here, but every MCP server
			// I've seen in the wild emits at least one log line
			// before the first response.
			continue
		}
		// Skip notifications and out-of-order responses; we keep
		// reading until our id matches.
		if len(resp.ID) == 0 || string(resp.ID) == "null" {
			continue
		}
		var gotID int
		if err := json.Unmarshal(resp.ID, &gotID); err != nil || gotID != id {
			continue
		}
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	}
}

func (l *lineRPC) notify(method string, params any) error {
	// Notifications have no id field. We bypass the call/scanner
	// loop entirely because there is no response to wait for.
	type note struct {
		JSONRPC string          `json:"jsonrpc"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params,omitempty"`
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return l.writeFrame(note{JSONRPC: "2.0", Method: method, Params: raw})
}

func (l *lineRPC) writeFrame(v any) error {
	buf, err := json.Marshal(v)
	if err != nil {
		return err
	}
	buf = append(buf, '\n')
	_, err = l.w.Write(buf)
	return err
}
