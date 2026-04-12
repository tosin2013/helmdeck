// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tosin2013/helmdeck/internal/gateway"
	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/pwmcp"
	"github.com/tosin2013/helmdeck/internal/security"
	"github.com/tosin2013/helmdeck/internal/session"
)

// --- test doubles ---------------------------------------------------------

// scriptedPWMCP implements PlaywrightMCPClient for tests. It serves a
// queue of canned ToolResults keyed per tool call count so each test
// can walk a specific path through the plan loop.
type scriptedPWMCP struct {
	mu            sync.Mutex
	initCalls     int
	initErr       error
	calls         []scriptedPWMCPCall
	replies       []*pwmcp.ToolResult // popped in FIFO order
	replyErrs     []error             // same index as replies; non-nil pushes an error
	defaultText   string              // returned when replies is exhausted
	defaultIsErr  bool
}

type scriptedPWMCPCall struct {
	Tool string
	Args map[string]any
}

func (s *scriptedPWMCP) Initialize(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initCalls++
	return s.initErr
}

func (s *scriptedPWMCP) ToolsCall(ctx context.Context, tool string, arguments map[string]any) (*pwmcp.ToolResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, scriptedPWMCPCall{Tool: tool, Args: arguments})
	idx := len(s.calls) - 1
	if idx < len(s.replyErrs) && s.replyErrs[idx] != nil {
		return nil, s.replyErrs[idx]
	}
	if idx < len(s.replies) && s.replies[idx] != nil {
		return s.replies[idx], nil
	}
	return &pwmcp.ToolResult{Text: s.defaultText, IsError: s.defaultIsErr}, nil
}

// scriptedDispatcherWT is the web.test flavour of vision_packs_test.go's
// scriptedDispatcher — plain text replies, FIFO queue. Declared under a
// distinct name so it coexists with the vision-test version in the
// same package.
type scriptedDispatcherWT struct {
	mu       sync.Mutex
	replies  []string
	replyErr []error
	captured []gateway.ChatRequest
	calls    int
}

func (s *scriptedDispatcherWT) Dispatch(_ context.Context, req gateway.ChatRequest) (gateway.ChatResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.captured = append(s.captured, req)
	idx := s.calls
	s.calls++
	if idx < len(s.replyErr) && s.replyErr[idx] != nil {
		return gateway.ChatResponse{}, s.replyErr[idx]
	}
	reply := ""
	if idx < len(s.replies) {
		reply = s.replies[idx]
	}
	return gateway.ChatResponse{
		Choices: []gateway.Choice{{
			Index:   0,
			Message: gateway.Message{Role: "assistant", Content: gateway.TextContent(reply)},
		}},
	}, nil
}

// webTestClientFactory returns a factory closure that hands out `mock`
// on every call — so the test can inspect the single client used.
func webTestClientFactory(mock PlaywrightMCPClient) func(endpoint string) PlaywrightMCPClient {
	return func(string) PlaywrightMCPClient { return mock }
}

// stubResolver returns canned IPs so the egress guard unit tests don't
// touch real DNS. Unknown hosts resolve to a public IP (8.8.8.8), which
// passes the default block list — the tests that assert blocking pass
// literal metadata IPs in the URL so they hit the no-DNS fast path.
type stubResolver struct{ m map[string][]net.IPAddr }

func (s stubResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	if v, ok := s.m[host]; ok {
		return v, nil
	}
	return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
}

// permissiveEgressWT builds a guard with the default block list but
// every hostname in these tests mapped to a public IP through the
// stub resolver — so "https://example.com" and friends pass.
func permissiveEgressWT(t *testing.T) *security.EgressGuard {
	t.Helper()
	return security.New(security.WithResolver(stubResolver{}))
}

// runWebTestHandler calls the pack handler directly with a
// hand-built ExecutionContext so the tests don't need the full
// session runtime / docker path. Keeps the test surface focused on
// the plan-loop logic.
func runWebTestHandler(t *testing.T, d gateway.Provider, disp *scriptedDispatcherWT, eg *security.EgressGuard, client PlaywrightMCPClient, sess *session.Session, input string) (json.RawMessage, error) {
	t.Helper()
	pack := WebTestWithClientFactory(disp, eg, webTestClientFactory(client))
	ec := &packs.ExecutionContext{
		Pack:    pack,
		Input:   json.RawMessage(input),
		Session: sess,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return pack.Handler(context.Background(), ec)
}

func sessionWithPWMCP(endpoint string) *session.Session {
	return &session.Session{
		ID:                    "sess-webtest-1",
		Status:                session.StatusRunning,
		PlaywrightMCPEndpoint: endpoint,
	}
}

// --- tests ----------------------------------------------------------------

func TestWebTest_HappyPathDoneWithAssertions(t *testing.T) {
	// Seed nav + snapshot happen automatically. The model plans two
	// steps: click the login link, then emit done. Final snapshot
	// must contain both assertion strings.
	final := "- document [ref=e1]: Welcome, alice\n  - heading \"Dashboard\" [ref=e2]"
	client := &scriptedPWMCP{
		replies: []*pwmcp.ToolResult{
			{Text: "navigated to https://app.example.com"},      // browser_navigate (seed)
			{Text: "- document [ref=e1]: Sign in\n  - link \"Log in\" [ref=e3]"}, // initial snapshot
			{Text: "clicked e3"},                                  // browser_click from plan step 1
			{Text: final},                                         // snapshot after click
		},
	}
	disp := &scriptedDispatcherWT{replies: []string{
		`{"tool":"browser_click","arguments":{"element":"Log in link","ref":"e3"},"reasoning":"navigate to dashboard"}`,
		`{"tool":"done","reasoning":"dashboard visible"}`,
	}}
	raw, err := runWebTestHandler(t, nil, disp, permissiveEgressWT(t), client, sessionWithPWMCP("http://stub/mcp"),
		`{"url":"https://app.example.com","instruction":"log in as alice and reach dashboard","model":"openai/gpt-4o","assertions":["Welcome, alice","Dashboard"]}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		Completed        bool   `json:"completed"`
		StepsUsed        int    `json:"steps_used"`
		FinalSnapshot    string `json:"final_snapshot"`
		AssertionsPassed bool   `json:"assertions_passed"`
		Reason           string `json:"reason"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if !out.Completed {
		t.Errorf("completed = false, want true")
	}
	if !out.AssertionsPassed {
		t.Errorf("assertions_passed = false, want true")
	}
	if !strings.Contains(out.Reason, "done") {
		t.Errorf("reason = %q, want contains 'done'", out.Reason)
	}
	if out.FinalSnapshot != final {
		t.Errorf("final_snapshot did not propagate")
	}
	// Seed nav + seed snapshot + plan click + post-click snapshot + done.
	if len(client.calls) != 4 {
		t.Errorf("mcp call count = %d, want 4 (nav, snap, click, snap)", len(client.calls))
	}
	if client.initCalls != 1 {
		t.Errorf("initialize called %d times, want 1", client.initCalls)
	}
}

func TestWebTest_MissingSession(t *testing.T) {
	_, err := runWebTestHandler(t, nil, &scriptedDispatcherWT{}, permissiveEgressWT(t), &scriptedPWMCP{}, nil,
		`{"url":"https://example.com","instruction":"noop","model":"openai/gpt-4o"}`)
	pe := &packs.PackError{}
	if !errors.As(err, &pe) || pe.Code != packs.CodeSessionUnavailable {
		t.Errorf("want session_unavailable, got %v", err)
	}
}

func TestWebTest_SessionWithoutPWMCPEndpoint(t *testing.T) {
	_, err := runWebTestHandler(t, nil, &scriptedDispatcherWT{}, permissiveEgressWT(t), &scriptedPWMCP{},
		&session.Session{ID: "s", Status: session.StatusRunning}, // endpoint empty
		`{"url":"https://example.com","instruction":"noop","model":"openai/gpt-4o"}`)
	pe := &packs.PackError{}
	if !errors.As(err, &pe) || pe.Code != packs.CodeSessionUnavailable {
		t.Errorf("want session_unavailable, got %v", err)
	}
	if !strings.Contains(pe.Message, "T807a") {
		t.Errorf("error message should point operators at T807a, got: %s", pe.Message)
	}
}

func TestWebTest_MissingRequiredFields(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"no url", `{"instruction":"x","model":"openai/gpt-4o"}`, "url is required"},
		{"no instruction", `{"url":"https://example.com","model":"openai/gpt-4o"}`, "instruction is required"},
		{"no model", `{"url":"https://example.com","instruction":"x"}`, "model is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runWebTestHandler(t, nil, &scriptedDispatcherWT{}, permissiveEgressWT(t), &scriptedPWMCP{},
				sessionWithPWMCP("http://stub/mcp"), tc.input)
			pe := &packs.PackError{}
			if !errors.As(err, &pe) || pe.Code != packs.CodeInvalidInput {
				t.Fatalf("want invalid_input, got %v", err)
			}
			if !strings.Contains(pe.Message, tc.want) {
				t.Errorf("message = %q, want contains %q", pe.Message, tc.want)
			}
		})
	}
}

func TestWebTest_EgressGuardBlocksTargetURL(t *testing.T) {
	// Literal metadata IP — EgressGuard's no-DNS fast path catches
	// it even under the permissive resolver stub.
	_, err := runWebTestHandler(t, nil, &scriptedDispatcherWT{}, permissiveEgressWT(t), &scriptedPWMCP{},
		sessionWithPWMCP("http://stub/mcp"),
		`{"url":"http://169.254.169.254/latest","instruction":"leak metadata","model":"openai/gpt-4o"}`)
	pe := &packs.PackError{}
	if !errors.As(err, &pe) || pe.Code != packs.CodeInvalidInput {
		t.Errorf("want invalid_input, got %v", err)
	}
	if !strings.Contains(pe.Message, "egress") {
		t.Errorf("message = %q, want contains 'egress'", pe.Message)
	}
}

func TestWebTest_EgressGuardBlocksMidTestNavigation(t *testing.T) {
	// Initial URL is example.com (stub resolves to 8.8.8.8, default
	// block list allows it). Mid-run the model emits a navigate to
	// 169.254.169.254 — metadata IP, default block list catches it
	// via the no-DNS fast path. Pack must short-circuit and NOT
	// forward the blocked call to MCP.
	guard := security.New(security.WithResolver(stubResolver{}))
	client := &scriptedPWMCP{
		replies: []*pwmcp.ToolResult{
			{Text: "navigated"},        // seed nav
			{Text: "seed snapshot"},     // seed snapshot
		},
	}
	disp := &scriptedDispatcherWT{replies: []string{
		`{"tool":"browser_navigate","arguments":{"url":"http://169.254.169.254/"},"reasoning":"pivot"}`,
	}}
	raw, err := runWebTestHandler(t, nil, disp, guard, client, sessionWithPWMCP("http://stub/mcp"),
		`{"url":"https://example.com","instruction":"x","model":"openai/gpt-4o"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		Completed bool   `json:"completed"`
		Reason    string `json:"reason"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.Completed {
		t.Errorf("completed = true, expected blocked")
	}
	if !strings.Contains(out.Reason, "egress") {
		t.Errorf("reason = %q, want contains 'egress'", out.Reason)
	}
	// Only the seed nav + seed snapshot should have hit the client.
	// The blocked navigate MUST NOT be forwarded.
	if len(client.calls) != 2 {
		t.Errorf("client calls = %d, want 2 (seed nav + seed snap)", len(client.calls))
	}
}

func TestWebTest_MCPInitializeFailure(t *testing.T) {
	client := &scriptedPWMCP{initErr: errors.New("connection refused")}
	// Use a short-deadline context so the retry loop in the handler
	// breaks early instead of sleeping through all 10 attempts (~27s).
	pack := WebTestWithClientFactory(&scriptedDispatcherWT{}, permissiveEgressWT(t), webTestClientFactory(client))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ec := &packs.ExecutionContext{
		Pack:    pack,
		Input:   json.RawMessage(`{"url":"https://example.com","instruction":"x","model":"openai/gpt-4o"}`),
		Session: sessionWithPWMCP("http://stub/mcp"),
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	_, err := pack.Handler(ctx, ec)
	pe := &packs.PackError{}
	if !errors.As(err, &pe) || pe.Code != packs.CodeHandlerFailed {
		t.Errorf("want handler_failed, got %v", err)
	}
	if !strings.Contains(pe.Message, "initialize") {
		t.Errorf("message = %q, want contains 'initialize'", pe.Message)
	}
}

func TestWebTest_ModelEmitsFail(t *testing.T) {
	client := &scriptedPWMCP{
		replies: []*pwmcp.ToolResult{
			{Text: "navigated"},
			{Text: "snapshot-empty"},
		},
	}
	disp := &scriptedDispatcherWT{replies: []string{
		`{"tool":"fail","arguments":{"reason":"login form not present"},"reasoning":"page is a 404"}`,
	}}
	raw, err := runWebTestHandler(t, nil, disp, permissiveEgressWT(t), client, sessionWithPWMCP("http://stub/mcp"),
		`{"url":"https://example.com","instruction":"log in","model":"openai/gpt-4o"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		Completed bool   `json:"completed"`
		Reason    string `json:"reason"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.Completed {
		t.Errorf("completed = true, want false")
	}
	if !strings.Contains(out.Reason, "login form not present") {
		t.Errorf("reason = %q, want to surface model's fail reason", out.Reason)
	}
}

func TestWebTest_MaxStepsExhausted(t *testing.T) {
	// Dispatcher keeps emitting snapshot tool calls forever; loop
	// must terminate after max_steps and report incomplete.
	client := &scriptedPWMCP{
		// Every MCP call returns the same snapshot; the factory
		// default handles calls past the seed pair.
		replies: []*pwmcp.ToolResult{
			{Text: "navigated"},
			{Text: "page"},
		},
		defaultText: "page",
	}
	disp := &scriptedDispatcherWT{replies: []string{
		`{"tool":"browser_snapshot","arguments":{},"reasoning":"spin"}`,
		`{"tool":"browser_snapshot","arguments":{},"reasoning":"spin"}`,
		`{"tool":"browser_snapshot","arguments":{},"reasoning":"spin"}`,
	}}
	raw, err := runWebTestHandler(t, nil, disp, permissiveEgressWT(t), client, sessionWithPWMCP("http://stub/mcp"),
		`{"url":"https://example.com","instruction":"never done","model":"openai/gpt-4o","max_steps":2}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		Completed bool   `json:"completed"`
		Reason    string `json:"reason"`
		StepsUsed int    `json:"steps_used"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.Completed {
		t.Errorf("completed = true, want false (max_steps)")
	}
	if !strings.Contains(out.Reason, "max_steps") {
		t.Errorf("reason = %q, want contains 'max_steps'", out.Reason)
	}
}

func TestWebTest_AssertionsFailFinalReport(t *testing.T) {
	// Model emits done but the final snapshot doesn't contain the
	// required substring — the final completed bit must be false.
	client := &scriptedPWMCP{
		replies: []*pwmcp.ToolResult{
			{Text: "navigated"},
			{Text: "no-match-here"},
		},
	}
	disp := &scriptedDispatcherWT{replies: []string{
		`{"tool":"done","reasoning":"trivially"}`,
	}}
	raw, err := runWebTestHandler(t, nil, disp, permissiveEgressWT(t), client, sessionWithPWMCP("http://stub/mcp"),
		`{"url":"https://example.com","instruction":"x","model":"openai/gpt-4o","assertions":["Dashboard"]}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		Completed        bool `json:"completed"`
		AssertionsPassed bool `json:"assertions_passed"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.AssertionsPassed {
		t.Errorf("assertions_passed = true, want false")
	}
	if out.Completed {
		t.Errorf("completed = true — must be false when assertions fail")
	}
}

func TestWebTest_ModelReturnsUnparseableJSON(t *testing.T) {
	client := &scriptedPWMCP{
		replies: []*pwmcp.ToolResult{
			{Text: "navigated"},
			{Text: "snap"},
		},
	}
	disp := &scriptedDispatcherWT{replies: []string{
		`I don't know what to do. Maybe click something?`,
	}}
	_, err := runWebTestHandler(t, nil, disp, permissiveEgressWT(t), client, sessionWithPWMCP("http://stub/mcp"),
		`{"url":"https://example.com","instruction":"x","model":"openai/gpt-4o"}`)
	pe := &packs.PackError{}
	if !errors.As(err, &pe) || pe.Code != packs.CodeHandlerFailed {
		t.Errorf("want handler_failed, got %v", err)
	}
}

func TestWebTest_ToleratesJSONWrappedInProse(t *testing.T) {
	// Small models often emit a sentence of preamble before the
	// JSON. parsePlan must extract it via the balanced-brace scanner.
	client := &scriptedPWMCP{
		replies: []*pwmcp.ToolResult{
			{Text: "navigated"},
			{Text: "snap"},
		},
	}
	disp := &scriptedDispatcherWT{replies: []string{
		"Sure! Here's what I'd do:\n\n```json\n{\"tool\":\"done\",\"reasoning\":\"it's already loaded\"}\n```\n",
	}}
	raw, err := runWebTestHandler(t, nil, disp, permissiveEgressWT(t), client, sessionWithPWMCP("http://stub/mcp"),
		`{"url":"https://example.com","instruction":"x","model":"openai/gpt-4o"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		Completed bool `json:"completed"`
	}
	_ = json.Unmarshal(raw, &out)
	if !out.Completed {
		t.Errorf("prose-wrapped JSON should still parse, completed = false")
	}
}

func TestWebTest_UnknownToolName(t *testing.T) {
	client := &scriptedPWMCP{
		replies: []*pwmcp.ToolResult{
			{Text: "navigated"},
			{Text: "snap"},
		},
	}
	disp := &scriptedDispatcherWT{replies: []string{
		`{"tool":"browser_hack","arguments":{},"reasoning":"make it up"}`,
	}}
	_, err := runWebTestHandler(t, nil, disp, permissiveEgressWT(t), client, sessionWithPWMCP("http://stub/mcp"),
		`{"url":"https://example.com","instruction":"x","model":"openai/gpt-4o"}`)
	pe := &packs.PackError{}
	if !errors.As(err, &pe) || pe.Code != packs.CodeHandlerFailed {
		t.Errorf("want handler_failed, got %v", err)
	}
	if !strings.Contains(pe.Message, "browser_hack") {
		t.Errorf("message should name the bad tool, got %q", pe.Message)
	}
}
