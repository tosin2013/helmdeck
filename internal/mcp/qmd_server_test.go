package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/memory"
	"github.com/tosin2013/helmdeck/internal/packs"
)

// runQMD drives a single JSON-RPC request through QMDServer.Serve
// using line-delimited framing on byte buffers — mirrors how the
// SSE handler would feed it. Returns the parsed response.
func runQMD(t *testing.T, srv *QMDServer, caller string, frame string) rpcResponse {
	t.Helper()
	ctx := packs.WithCaller(context.Background(), caller)
	in := bytes.NewBufferString(frame + "\n")
	var out bytes.Buffer
	done := make(chan struct{})
	go func() {
		_ = srv.Serve(ctx, in, &out)
		close(done)
	}()
	// Close input so Serve's scanner exits cleanly after one request.
	// We can't close a *bytes.Buffer; instead wrap in an io.Reader
	// that returns EOF after the buffer drains (default behavior).
	<-done
	var resp rpcResponse
	if err := json.Unmarshal(bytes.TrimRight(out.Bytes(), "\n"), &resp); err != nil {
		t.Fatalf("decode response: %v\nraw: %s", err, out.String())
	}
	return resp
}

// seedPackHistory writes one pack-history audit row under the given
// caller's bare namespace. Mirrors what the audit hook produces in
// production so the projection test exercises real-shaped input.
func seedPackHistory(t *testing.T, store memory.MemoryStore, caller, pack, persona, audience string, atUnix int64) {
	t.Helper()
	body, _ := json.Marshal(packs.PackAudit{
		Pack:        pack,
		Outcome:     "ok",
		AtUnix:      atUnix,
		LearnInputs: map[string]string{"persona": persona, "audience": audience},
	})
	key := packs.AuditKeyPrefixPack + pack + "/" + jsonIntStr(atUnix)
	if _, err := store.Put(context.Background(), caller, key, body, memory.WithCategory(packs.AuditCategoryPack)); err != nil {
		t.Fatal(err)
	}
}

func seedUserFact(t *testing.T, store memory.MemoryStore, caller, key, value string) {
	t.Helper()
	if _, err := store.Put(context.Background(), caller, key, []byte(value),
		memory.WithCategory(packs.DefaultFactCategory)); err != nil {
		t.Fatal(err)
	}
}

func jsonIntStr(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// TestQMD_Initialize proves the protocol handshake works.
func TestQMD_Initialize(t *testing.T) {
	srv := NewQMDServer(memory.NewInMemoryStore())
	resp := runQMD(t, srv, "alice", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if resp.Error != nil {
		t.Fatalf("initialize error: %+v", resp.Error)
	}
	var got struct {
		ProtocolVersion string         `json:"protocolVersion"`
		ServerInfo      map[string]any `json:"serverInfo"`
	}
	_ = json.Unmarshal(resp.Result, &got)
	if got.ProtocolVersion == "" {
		t.Errorf("want protocolVersion populated, got empty")
	}
	if got.ServerInfo["name"] != "helmdeck" {
		t.Errorf("want serverInfo.name=helmdeck, got %v", got.ServerInfo["name"])
	}
}

// TestQMD_ToolsList proves a single `query` tool is exposed.
func TestQMD_ToolsList(t *testing.T) {
	srv := NewQMDServer(memory.NewInMemoryStore())
	resp := runQMD(t, srv, "alice", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if resp.Error != nil {
		t.Fatalf("tools/list error: %+v", resp.Error)
	}
	var got struct {
		Tools []map[string]any `json:"tools"`
	}
	_ = json.Unmarshal(resp.Result, &got)
	if len(got.Tools) != 1 {
		t.Fatalf("want 1 tool, got %d", len(got.Tools))
	}
	if got.Tools[0]["name"] != "query" {
		t.Errorf("want tool name=query, got %v", got.Tools[0]["name"])
	}
}

// TestQMD_QueryReturnsUserFacts proves agent-written facts surface
// through the corpus with the expected QMD chunk shape (docid,
// snippet, collection, score).
func TestQMD_QueryReturnsUserFacts(t *testing.T) {
	store := memory.NewInMemoryStore()
	seedUserFact(t, store, "alice", "preferences/frontend", "React over Vue for Konflux projects")
	seedUserFact(t, store, "alice", "preferences/lang", "Go for backend")
	srv := NewQMDServer(store)

	resp := runQMD(t, srv, "alice",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"query","arguments":{"searches":[{"type":"lex","query":"React"}],"limit":10}}}`)
	if resp.Error != nil {
		t.Fatalf("query error: %+v", resp.Error)
	}
	var wrap struct {
		StructuredContent struct {
			Results []queryResult `json:"results"`
		} `json:"structuredContent"`
	}
	_ = json.Unmarshal(resp.Result, &wrap)
	if len(wrap.StructuredContent.Results) != 1 {
		t.Fatalf("want 1 result (React match), got %d", len(wrap.StructuredContent.Results))
	}
	hit := wrap.StructuredContent.Results[0]
	if !strings.Contains(hit.DocID, "preferences/frontend") {
		t.Errorf("docid should reference the matching key; got %q", hit.DocID)
	}
	if hit.Collection != "helmdeck-"+packs.DefaultFactCategory {
		t.Errorf("collection should be category-prefixed; got %q", hit.Collection)
	}
	if !strings.Contains(hit.Snippet, "React") {
		t.Errorf("snippet should contain the match; got %q", hit.Snippet)
	}
	if hit.Score <= 0 {
		t.Errorf("score should be > 0 for a substring match; got %v", hit.Score)
	}
}

// TestQMD_QueryReturnsPackAudits proves engine-written audit rows
// project into the corpus with a readable summary format.
func TestQMD_QueryReturnsPackAudits(t *testing.T) {
	store := memory.NewInMemoryStore()
	seedPackHistory(t, store, "alice", "blog.rewrite_for_audience", "technical", "platform engineers", 100)
	seedPackHistory(t, store, "alice", "slides.outline", "executive", "leadership", 200)
	srv := NewQMDServer(store)

	resp := runQMD(t, srv, "alice",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"query","arguments":{"searches":[{"type":"lex","query":"technical"}],"limit":10}}}`)
	var wrap struct {
		StructuredContent struct {
			Results []queryResult `json:"results"`
		} `json:"structuredContent"`
	}
	_ = json.Unmarshal(resp.Result, &wrap)
	if len(wrap.StructuredContent.Results) < 1 {
		t.Fatalf("want at least 1 result for 'technical' query; got %d", len(wrap.StructuredContent.Results))
	}
	hit := wrap.StructuredContent.Results[0]
	if !strings.Contains(hit.Snippet, "Pack call: blog.rewrite_for_audience") {
		t.Errorf("snippet should render pack-audit chunk; got %q", hit.Snippet)
	}
	if !strings.Contains(hit.Snippet, "persona: technical") {
		t.Errorf("snippet should include the matching input; got %q", hit.Snippet)
	}
}

// TestQMD_QueryPerCallerIsolated proves alice's corpus is invisible
// when querying as bob — same JWT-subject namespacing the rest of the
// memory surface uses.
func TestQMD_QueryPerCallerIsolated(t *testing.T) {
	store := memory.NewInMemoryStore()
	seedUserFact(t, store, "alice", "secret/thing", "alice-only fact")
	srv := NewQMDServer(store)

	resp := runQMD(t, srv, "bob",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"query","arguments":{"searches":[{"type":"lex","query":"alice"}]}}}`)
	var wrap struct {
		StructuredContent struct {
			Results []queryResult `json:"results"`
		} `json:"structuredContent"`
	}
	_ = json.Unmarshal(resp.Result, &wrap)
	if len(wrap.StructuredContent.Results) != 0 {
		t.Errorf("alice's facts leaked to bob's namespace: %+v", wrap.StructuredContent.Results)
	}
}

// TestQMD_QueryUnknownTool rejects anything except `query`.
func TestQMD_QueryUnknownTool(t *testing.T) {
	srv := NewQMDServer(memory.NewInMemoryStore())
	resp := runQMD(t, srv, "alice",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"banana","arguments":{}}}`)
	if resp.Error == nil {
		t.Fatal("expected error for unknown tool")
	}
	if !strings.Contains(resp.Error.Message, "query") {
		t.Errorf("error should mention 'query'; got %q", resp.Error.Message)
	}
}

// TestQMD_NilStoreSafe proves NewQMDServer returns nil for nil store
// (so callers can gate the SSE route on this signal cheaply).
func TestQMD_NilStoreSafe(t *testing.T) {
	if NewQMDServer(nil) != nil {
		t.Errorf("NewQMDServer(nil) should return nil so SSE route can 503")
	}
}

// TestQMD_QueryLimitClamped — limit > 100 falls back to default;
// negative limit also defaults. Prevents pathological huge replies.
func TestQMD_QueryLimitClamped(t *testing.T) {
	store := memory.NewInMemoryStore()
	for i := 0; i < 30; i++ {
		seedUserFact(t, store, "alice", "prefs/"+jsonIntStr(int64(i)), "value-"+jsonIntStr(int64(i)))
	}
	srv := NewQMDServer(store)
	// limit=0 (default 20)
	resp := runQMD(t, srv, "alice",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"query","arguments":{"searches":[{"type":"lex","query":"value"}],"limit":0}}}`)
	var wrap struct {
		StructuredContent struct {
			Results []queryResult `json:"results"`
		} `json:"structuredContent"`
	}
	_ = json.Unmarshal(resp.Result, &wrap)
	if len(wrap.StructuredContent.Results) != qmdDefaultLimit {
		t.Errorf("want %d results at default limit; got %d", qmdDefaultLimit, len(wrap.StructuredContent.Results))
	}
}

// TestQMD_ContentTextEnvelope confirms we also emit the standard MCP
// text-content shape so older MCP clients that don't read
// structuredContent still parse our results.
func TestQMD_ContentTextEnvelope(t *testing.T) {
	store := memory.NewInMemoryStore()
	seedUserFact(t, store, "alice", "k", "needle in here")
	srv := NewQMDServer(store)
	resp := runQMD(t, srv, "alice",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"query","arguments":{"searches":[{"type":"lex","query":"needle"}]}}}`)
	var wrap struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	_ = json.Unmarshal(resp.Result, &wrap)
	if len(wrap.Content) != 1 || wrap.Content[0].Type != "text" {
		t.Fatalf("want one text content frame; got %+v", wrap.Content)
	}
	if !strings.Contains(wrap.Content[0].Text, "needle") {
		t.Errorf("text content should embed the snippet; got %q", wrap.Content[0].Text)
	}
}

// noopReader is a tiny shim used when we want Serve to terminate
// after consuming the buffered input. Not strictly needed today
// since bytes.Buffer naturally returns EOF, but kept for future
// streaming-input tests.
type noopReader struct{ io.Reader }

func (noopReader) Close() error { return nil }
