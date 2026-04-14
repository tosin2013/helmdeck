package mcp

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	// 2 fixture packs + 3 async wrapper tools (pack.start/status/result).
	if len(env.Result.Tools) != 5 {
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
		t.Errorf("missing fixture tools: %+v", seen)
	}
	if !seen["pack.start"] || !seen["pack.status"] || !seen["pack.result"] {
		t.Errorf("missing async wrapper tools: %+v", seen)
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

// TestPackServerProgressNotification asserts that when a tools/call
// arrives with _meta.progressToken, a handler that calls ec.Report
// produces a JSON-RPC notifications/progress frame echoing the token.
// This is the spec-compliant path for clients that opt in to
// progress (Python SDK by default; TS SDK with resetTimeoutOnProgress
// enabled). The frame must precede the eventual tool-call response.
func TestPackServerProgressNotification(t *testing.T) {
	reg := packs.NewPackRegistry()
	_ = reg.Register(&packs.Pack{
		Name: "slow", Version: "v1",
		Handler: func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
			ec.Report(50, "halfway")
			return json.RawMessage(`{"ok":true}`), nil
		},
	})
	eng := packs.New()
	write, read, stop := startPackServerScanner(t, reg, eng)
	defer stop()

	write(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"slow","arguments":{},"_meta":{"progressToken":"tok-1"}}}`)
	first := read()
	if !strings.Contains(first, `"method":"notifications/progress"`) {
		t.Fatalf("expected progress notification first, got: %s", first)
	}
	if !strings.Contains(first, `"progressToken":"tok-1"`) {
		t.Errorf("progress frame missing token echo: %s", first)
	}
	if !strings.Contains(first, `"progress":50`) {
		t.Errorf("progress frame missing pct: %s", first)
	}
	second := read()
	if !strings.Contains(second, `"id":7`) {
		t.Errorf("expected response id=7 after progress, got: %s", second)
	}
}

// TestPackServerAsyncToolLifecycle covers the pack.start → poll →
// pack.result happy path. The whole point of this trio is to keep
// each individual JSON-RPC call short (well under any client's
// per-request timeout) by detaching the actual work onto a
// background goroutine — see jobs.go for the full rationale.
func TestPackServerAsyncToolLifecycle(t *testing.T) {
	reg, eng := newServerFixture(t)
	write, read, stop := startPackServerScanner(t, reg, eng)
	defer stop()

	// 1. pack.start returns a job_id immediately.
	write(`{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"pack.start","arguments":{"pack":"echo","input":{"msg":"hi"}}}}`)
	startResp := read()
	if !strings.Contains(startResp, `\"job_id\":`) {
		t.Fatalf("pack.start missing job_id: %s", startResp)
	}
	// Pull job_id out of the text content for the follow-up calls.
	idx := strings.Index(startResp, `\"job_id\":\"`)
	if idx < 0 {
		t.Fatalf("could not locate escaped job_id in: %s", startResp)
	}
	tail := startResp[idx+len(`\"job_id\":\"`):]
	jobID := tail[:strings.Index(tail, `\"`)]

	// 2. Poll pack.status until done (echo finishes synchronously
	// the moment the goroutine is scheduled, but we still go through
	// the polling path to exercise it).
	var statusResp string
	for i := 0; i < 50; i++ {
		write(`{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"name":"pack.status","arguments":{"job_id":"` + jobID + `"}}}`)
		statusResp = read()
		if strings.Contains(statusResp, `\"state\":\"completed\"`) {
			break
		}
	}
	if !strings.Contains(statusResp, `\"state\":\"completed\"`) {
		t.Fatalf("job never reached done state: %s", statusResp)
	}

	// 3. pack.result returns the wrapped pack output.
	write(`{"jsonrpc":"2.0","id":12,"method":"tools/call","params":{"name":"pack.result","arguments":{"job_id":"` + jobID + `"}}}`)
	resultResp := read()
	if !strings.Contains(resultResp, `\"echo\":\"hi\"`) {
		t.Errorf("pack.result missing wrapped output: %s", resultResp)
	}

	// 4. Subsequent pack.result on the same job_id is unknown_job
	// because pack.result drops the entry on success.
	write(`{"jsonrpc":"2.0","id":13,"method":"tools/call","params":{"name":"pack.result","arguments":{"job_id":"` + jobID + `"}}}`)
	gone := read()
	if !strings.Contains(gone, `unknown_job`) {
		t.Errorf("expected unknown_job after pack.result consumed the job, got: %s", gone)
	}
}

// asyncFixture builds a registry with an Async-marked pack so we can
// exercise the SEP-1686 routing path without dragging in a real
// heavy pack. The handler completes instantly so we don't have to
// wait in tests.
func asyncFixture(t *testing.T) (*packs.Registry, *packs.Engine) {
	t.Helper()
	reg := packs.NewPackRegistry()
	_ = reg.Register(&packs.Pack{
		Name: "slow", Version: "v1", Description: "fakes a heavy pack",
		Async: true,
		Handler: func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
			ec.Report(50, "halfway")
			return json.RawMessage(`{"ok":true}`), nil
		},
	})
	return reg, packs.New()
}

// TestPackServerAsyncEnvelopeReturnsTaskID asserts that a tools/call
// against a pack with Async=true returns immediately with a SEP-1686
// task envelope (taskId in _meta.modelcontextprotocol.io/related-task)
// instead of blocking until the pack completes. This is the whole
// point of the hybrid plan: SEP-1686-aware clients see a sync return
// and the SDK polls tasks/get under the hood — the LLM never sees
// -32001 because the JSON-RPC request finished in milliseconds.
func TestPackServerAsyncEnvelopeReturnsTaskID(t *testing.T) {
	reg, eng := asyncFixture(t)
	write, read, stop := startPackServerScanner(t, reg, eng)
	defer stop()

	write(`{"jsonrpc":"2.0","id":20,"method":"tools/call","params":{"name":"slow","arguments":{}}}`)
	// First frame: notifications/tasks/created announces the task
	// to subscribed clients. Second frame: the actual response.
	first := read()
	if !strings.Contains(first, `notifications/tasks/created`) {
		t.Fatalf("expected tasks/created notification first, got: %s", first)
	}
	if !strings.Contains(first, `"taskId":"pack_`) {
		t.Errorf("notification missing taskId: %s", first)
	}
	resp := read()
	if !strings.Contains(resp, `"id":20`) {
		t.Fatalf("expected response id=20 after notification, got: %s", resp)
	}
	if !strings.Contains(resp, `modelcontextprotocol.io/related-task`) {
		t.Errorf("response envelope missing related-task _meta: %s", resp)
	}
	if !strings.Contains(resp, `"taskId":"pack_`) {
		t.Errorf("response envelope missing taskId: %s", resp)
	}
	// The full {ok:true} pack output should NOT be in this response —
	// it's still being computed when the envelope is sent. (For the
	// test pack the goroutine completes immediately, but the envelope
	// path returns BEFORE the result is captured.)
}

// TestPackServerTasksGetLifecycle exercises the SEP-1686 polling
// path: tools/call returns a task envelope, then tasks/get returns
// the canonical SEP-1686 status shape (status, pollFrequency, and
// the inline result when completed).
func TestPackServerTasksGetLifecycle(t *testing.T) {
	reg, eng := asyncFixture(t)
	write, read, stop := startPackServerScanner(t, reg, eng)
	defer stop()

	write(`{"jsonrpc":"2.0","id":30,"method":"tools/call","params":{"name":"slow","arguments":{}}}`)
	_ = read() // notifications/tasks/created
	resp := read()
	// Pull taskId out of the unescaped _meta block (the same string
	// is also present escaped inside the text content, but _meta is
	// the canonical SEP-1686 home).
	idx := strings.Index(resp, `"taskId":"`)
	if idx < 0 {
		t.Fatalf("could not locate taskId in: %s", resp)
	}
	tail := resp[idx+len(`"taskId":"`):]
	taskID := tail[:strings.Index(tail, `"`)]

	// Poll tasks/get until the task reaches a terminal state.
	var got string
	for i := 0; i < 50; i++ {
		write(`{"jsonrpc":"2.0","id":31,"method":"tasks/get","params":{"taskId":"` + taskID + `"}}`)
		got = read()
		if strings.Contains(got, `"status":"completed"`) {
			break
		}
	}
	if !strings.Contains(got, `"status":"completed"`) {
		t.Fatalf("tasks/get never returned completed: %s", got)
	}
	// SEP-1686 canonical fields must be present.
	if !strings.Contains(got, `"pollFrequency":5000`) {
		t.Errorf("tasks/get missing pollFrequency: %s", got)
	}
	// Completed status must inline the result so no second fetch
	// is needed.
	if !strings.Contains(got, `"result"`) {
		t.Errorf("completed tasks/get missing inline result: %s", got)
	}
	if !strings.Contains(got, `\"ok\":true`) {
		t.Errorf("inline result missing pack output: %s", got)
	}
}

// TestPackServerAsyncWebhookFires verifies that when an Async pack
// is invoked through pack.start with webhook_url set, helmdeck POSTs
// the final payload to the URL with a valid HMAC-SHA256 signature.
// This is the push-to-LLM path: the receiver is a stand-in for an
// agent gateway that re-injects results into a chat session.
func TestPackServerAsyncWebhookFires(t *testing.T) {
	reg, eng := asyncFixture(t)

	// Spin up a stub receiver that captures the body + signature.
	var hits int32
	var capturedSig string
	var capturedBody []byte
	done := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		capturedSig = r.Header.Get("X-Helmdeck-Signature")
		capturedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		select {
		case done <- struct{}{}:
		default:
		}
	}))
	defer srv.Close()

	write, read, stop := startPackServerScanner(t, reg, eng)
	defer stop()

	secret := "test-secret-" + hex.EncodeToString([]byte{0x42, 0x43})
	startReq := `{"jsonrpc":"2.0","id":40,"method":"tools/call","params":{"name":"pack.start","arguments":{"pack":"slow","input":{},"webhook_url":"` + srv.URL + `","webhook_secret":"` + secret + `"}}}`
	write(startReq)
	_ = read() // pack.start response

	// Wait for the webhook to fire (the test pack completes
	// immediately, but the goroutine + HTTP round trip takes a
	// moment). 5s is generous; the actual delivery is sub-second.
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("webhook never fired")
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("expected 1 webhook hit, got %d", got)
	}
	// HMAC verification: receivers reproduce the signature from the
	// raw body and compare. The wire format is "sha256=<hex>".
	if !strings.HasPrefix(capturedSig, "sha256=") {
		t.Fatalf("signature missing sha256 prefix: %q", capturedSig)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(capturedBody)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if capturedSig != want {
		t.Errorf("signature mismatch: got %q want %q", capturedSig, want)
	}
	// Sanity: the body decodes to a webhookPayload-shaped object.
	var payload map[string]any
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("body not valid JSON: %v", err)
	}
	if payload["event_type"] != "pack.complete" {
		t.Errorf("event_type = %v, want pack.complete", payload["event_type"])
	}
	if payload["pack"] != "slow" {
		t.Errorf("pack = %v, want slow", payload["pack"])
	}
	if !strings.HasPrefix(payload["task_id"].(string), "pack_") {
		t.Errorf("task_id missing pack_ prefix: %v", payload["task_id"])
	}
}
