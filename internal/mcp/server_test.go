package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
)

// startPackServerScanner wires a PackServer to in-memory pipes
// and runs it in a goroutine. The returned write function sends
// one JSON-RPC frame; read returns the next response. Tests must
// call stop to drain the goroutine.
func startPackServerScanner(t *testing.T, reg *packs.Registry, eng *packs.Engine) (write func(string), read func() string, stop func()) {
	t.Helper()
	srv := NewPackServer(reg, eng)
	clientToServer, fromClient := io.Pipe()
	fromServer, serverToClient := io.Pipe()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ctx, clientToServer, serverToClient)
	}()

	sc := bufio.NewScanner(fromServer)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	write = func(line string) {
		if !strings.HasSuffix(line, "\n") {
			line += "\n"
		}
		if _, err := fromClient.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	read = func() string {
		if !sc.Scan() {
			t.Fatalf("read: %v", sc.Err())
		}
		return sc.Text()
	}
	stop = func() {
		cancel()
		_ = fromClient.Close()
		_ = serverToClient.Close()
		<-done
	}
	return
}

func newServerFixture(t *testing.T) (*packs.Registry, *packs.Engine) {
	t.Helper()
	reg := packs.NewPackRegistry()
	_ = reg.Register(&packs.Pack{
		Name: "echo", Version: "v1", Description: "echoes input.msg",
		InputSchema: packs.BasicSchema{
			Required:   []string{"msg"},
			Properties: map[string]string{"msg": "string"},
		},
		Handler: func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
			var in struct {
				Msg string `json:"msg"`
			}
			_ = json.Unmarshal(ec.Input, &in)
			return json.Marshal(map[string]string{"echo": in.Msg})
		},
	})
	_ = reg.Register(&packs.Pack{
		Name: "boom", Version: "v1",
		Handler: func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed, Message: "kaboom"}
		},
	})
	eng := packs.New()
	return reg, eng
}

func TestPackServerInitialize(t *testing.T) {
	reg, eng := newServerFixture(t)
	write, read, stop := startPackServerScanner(t, reg, eng)
	defer stop()

	write(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	resp := read()
	if !strings.Contains(resp, `"protocolVersion":"2024-11-05"`) {
		t.Errorf("initialize resp = %s", resp)
	}
	if !strings.Contains(resp, `"name":"helmdeck"`) {
		t.Errorf("server info missing: %s", resp)
	}
}

func TestPackServerToolsList(t *testing.T) {
	reg, eng := newServerFixture(t)
	write, read, stop := startPackServerScanner(t, reg, eng)
	defer stop()

	write(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	resp := read()

	var env struct {
		Result struct {
			Tools []Tool `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(resp), &env); err != nil {
		t.Fatal(err)
	}
	if len(env.Result.Tools) != 2 {
		t.Errorf("tools = %d", len(env.Result.Tools))
	}
	seen := map[string]bool{}
	for _, tool := range env.Result.Tools {
		seen[tool.Name] = true
		// Echo's input schema must round-trip through schemaToJSON
		// as object with msg required.
		if tool.Name == "echo" {
			var schema map[string]any
			_ = json.Unmarshal(tool.InputSchema, &schema)
			if schema["type"] != "object" {
				t.Errorf("echo schema type = %v", schema["type"])
			}
			req, ok := schema["required"].([]any)
			if !ok || len(req) != 1 || req[0] != "msg" {
				t.Errorf("echo required = %v", schema["required"])
			}
		}
	}
	if !seen["echo"] || !seen["boom"] {
		t.Errorf("missing tools: %+v", seen)
	}
}

func TestPackServerToolsCallSuccess(t *testing.T) {
	reg, eng := newServerFixture(t)
	write, read, stop := startPackServerScanner(t, reg, eng)
	defer stop()

	write(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"msg":"hello"}}}`)
	resp := read()
	if !strings.Contains(resp, `"isError":false`) {
		t.Errorf("expected isError:false: %s", resp)
	}
	if !strings.Contains(resp, `\"echo\":\"hello\"`) {
		t.Errorf("expected echo output in text content: %s", resp)
	}
}

func TestPackServerToolsCallFailureMapsToErrorContent(t *testing.T) {
	reg, eng := newServerFixture(t)
	write, read, stop := startPackServerScanner(t, reg, eng)
	defer stop()

	write(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"boom","arguments":{}}}`)
	resp := read()
	if !strings.Contains(resp, `"isError":true`) {
		t.Errorf("expected isError:true: %s", resp)
	}
	if !strings.Contains(resp, `handler_failed`) {
		t.Errorf("expected closed-set code in body: %s", resp)
	}
}

func TestPackServerUnknownTool(t *testing.T) {
	reg, eng := newServerFixture(t)
	write, read, stop := startPackServerScanner(t, reg, eng)
	defer stop()

	write(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"nope","arguments":{}}}`)
	resp := read()
	if !strings.Contains(resp, `"code":-32601`) {
		t.Errorf("expected -32601 method not found mapping: %s", resp)
	}
}

func TestPackServerUnknownMethod(t *testing.T) {
	reg, eng := newServerFixture(t)
	write, read, stop := startPackServerScanner(t, reg, eng)
	defer stop()

	write(`{"jsonrpc":"2.0","id":6,"method":"resources/list"}`)
	resp := read()
	if !strings.Contains(resp, `"code":-32601`) {
		t.Errorf("expected -32601: %s", resp)
	}
}

func TestPackServerParseError(t *testing.T) {
	reg, eng := newServerFixture(t)
	write, read, stop := startPackServerScanner(t, reg, eng)
	defer stop()

	write(`not json at all`)
	resp := read()
	if !strings.Contains(resp, `"code":-32700`) {
		t.Errorf("expected -32700 parse error: %s", resp)
	}
}

func TestPackServerHotReload(t *testing.T) {
	// tools/list re-reads the registry on every call so packs
	// registered mid-session show up immediately.
	reg, eng := newServerFixture(t)
	write, read, stop := startPackServerScanner(t, reg, eng)
	defer stop()

	write(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	first := read()
	beforeCount := strings.Count(first, `"name":`)

	_ = reg.Register(&packs.Pack{
		Name: "fresh", Version: "v1",
		Handler: func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
			return nil, nil
		},
	})

	write(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	second := read()
	if strings.Count(second, `"name":`) != beforeCount+1 {
		t.Errorf("hot-loaded pack not visible: before=%d after=%s", beforeCount, second)
	}
}
