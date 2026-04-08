package mcp

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestStdioAdapterAgainstFakeServer spawns a tiny Go program that
// speaks the MCP stdio handshake and returns one tool from
// tools/list. This exercises the real lineRPC framing without
// pulling in a real MCP server as a test dependency.
func TestStdioAdapterAgainstFakeServer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-server uses /bin/sh framing")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "fake.go")
	bin := filepath.Join(dir, "fake")
	if err := os.WriteFile(src, []byte(fakeMCPSource), 0o644); err != nil {
		t.Fatal(err)
	}
	// Build the helper using the same Go toolchain that runs the test.
	if out, err := runCmd("go", "build", "-o", bin, src); err != nil {
		t.Fatalf("build helper: %v\n%s", err, out)
	}

	a := NewStdioAdapter(StdioConfig{Command: bin})
	m, err := a.FetchManifest(context.Background())
	if err != nil {
		t.Fatalf("FetchManifest: %v", err)
	}
	if len(m.Tools) != 1 || m.Tools[0].Name != "ping" {
		t.Errorf("manifest = %+v", m)
	}
}

func runCmd(name string, args ...string) ([]byte, error) {
	c := osExec(name, args...)
	return c.CombinedOutput()
}

// fakeMCPSource is a self-contained MCP-stdio responder. It reads
// JSON-RPC requests one per line from stdin, replies to initialize
// with an empty result, ignores notifications/initialized, and
// answers tools/list with a single tool. Anything else closes.
const fakeMCPSource = `package main

import (
	"bufio"
	"encoding/json"
	"os"
)

type req struct {
	JSONRPC string          ` + "`json:\"jsonrpc\"`" + `
	ID      json.RawMessage ` + "`json:\"id\"`" + `
	Method  string          ` + "`json:\"method\"`" + `
}

type resp struct {
	JSONRPC string          ` + "`json:\"jsonrpc\"`" + `
	ID      json.RawMessage ` + "`json:\"id\"`" + `
	Result  any             ` + "`json:\"result\"`" + `
}

func main() {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	enc := json.NewEncoder(os.Stdout)
	for sc.Scan() {
		var r req
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			continue
		}
		switch r.Method {
		case "initialize":
			_ = enc.Encode(resp{JSONRPC: "2.0", ID: r.ID, Result: map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]any{"name": "fake", "version": "0.0.1"},
			}})
		case "notifications/initialized":
			// no response
		case "tools/list":
			_ = enc.Encode(resp{JSONRPC: "2.0", ID: r.ID, Result: map[string]any{
				"tools": []map[string]any{{"name": "ping", "description": "ping pong"}},
			}})
		default:
			return
		}
	}
}
`
