package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
)

func newA2ARouter(t *testing.T) (http.Handler, *packs.Registry) {
	t.Helper()
	reg := packs.NewPackRegistry()
	_ = reg.Register(&packs.Pack{
		Name: "echo", Version: "v1", Description: "echoes input.msg",
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
	eng := packs.New(packs.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	h := NewRouter(Deps{
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:      "v0.0.1",
		PackRegistry: reg,
		PackEngine:   eng,
	})
	return h, reg
}

func TestAgentCardListsPacks(t *testing.T) {
	h, _ := newA2ARouter(t)
	req := httptest.NewRequest(http.MethodGet, "/.well-known/agent.json", nil)
	req.Host = "helmdeck.example.com"
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var card agentCard
	if err := json.Unmarshal(rr.Body.Bytes(), &card); err != nil {
		t.Fatal(err)
	}
	if card.Name != "helmdeck" || card.Version != "v0.0.1" {
		t.Errorf("card meta = %+v", card)
	}
	if card.URL != "http://helmdeck.example.com" {
		t.Errorf("url = %q", card.URL)
	}
	if !card.Capabilities["streaming"] {
		t.Error("streaming capability missing")
	}
	if len(card.Skills) != 2 {
		t.Errorf("skills = %d", len(card.Skills))
	}
	seen := map[string]bool{}
	for _, s := range card.Skills {
		seen[s.ID] = true
	}
	if !seen["echo"] || !seen["boom"] {
		t.Errorf("skills missing: %+v", card.Skills)
	}
}

func TestAgentCardIsPublic(t *testing.T) {
	// /.well-known/agent.json must NOT be protected by IsProtectedPath
	// — remote agents fetch it before they hold a token.
	if IsProtectedPath("/.well-known/agent.json") {
		t.Error("agent card should be public")
	}
	// /a2a/v1/tasks IS protected.
	if !IsProtectedPath("/a2a/v1/tasks") {
		t.Error("a2a tasks should be protected")
	}
}

// parseSSE walks an SSE response body into ordered events. Returned
// slice has one entry per `event:` line; data is the raw JSON.
type sseEvent struct {
	Event string
	Data  string
}

func parseSSE(body string) []sseEvent {
	var out []sseEvent
	var cur sseEvent
	for _, line := range strings.Split(body, "\n") {
		switch {
		case strings.HasPrefix(line, "event: "):
			cur.Event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			cur.Data = strings.TrimPrefix(line, "data: ")
		case line == "" && cur.Event != "":
			out = append(out, cur)
			cur = sseEvent{}
		}
	}
	return out
}

func postSSE(t *testing.T, h http.Handler, path, body string) (int, string, http.Header) {
	t.Helper()
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func TestA2ATaskHappyPathSSE(t *testing.T) {
	h, _ := newA2ARouter(t)
	body := `{"skill":"echo","input":{"msg":"hi"}}`
	code, raw, headers := postSSE(t, h, "/a2a/v1/tasks", body)

	if code != http.StatusOK {
		t.Fatalf("status = %d body=%s", code, raw)
	}
	if ct := headers.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("content-type = %q", ct)
	}
	events := parseSSE(raw)
	if len(events) < 3 {
		t.Fatalf("events = %d (%v)", len(events), events)
	}
	want := []string{"submitted", "working", "completed"}
	for i, w := range want {
		if events[i].Event != w {
			t.Errorf("event[%d] = %q, want %q", i, events[i].Event, w)
		}
	}
	// completed event must carry the pack Result
	if !strings.Contains(events[2].Data, `"echo":"hi"`) {
		t.Errorf("completed payload missing output: %s", events[2].Data)
	}
}

func TestA2ATaskFailureSSE(t *testing.T) {
	h, _ := newA2ARouter(t)
	_, raw, _ := postSSE(t, h, "/a2a/v1/tasks", `{"skill":"boom"}`)
	events := parseSSE(raw)
	if len(events) < 3 {
		t.Fatalf("events = %v", events)
	}
	last := events[len(events)-1]
	if last.Event != "failed" {
		t.Errorf("last event = %q, want failed", last.Event)
	}
	if !strings.Contains(last.Data, `"handler_failed"`) {
		t.Errorf("failed payload missing closed-set code: %s", last.Data)
	}
}

func TestA2ATaskUnknownSkill(t *testing.T) {
	h, _ := newA2ARouter(t)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/a2a/v1/tasks", strings.NewReader(`{"skill":"nope"}`)))
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d", rr.Code)
	}
}

func TestA2ATaskMissingSkill(t *testing.T) {
	h, _ := newA2ARouter(t)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/a2a/v1/tasks", strings.NewReader(`{}`)))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d", rr.Code)
	}
}

func TestA2AUnavailableWhenNil(t *testing.T) {
	h := NewRouter(Deps{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
	})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/.well-known/agent.json", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d", rr.Code)
	}
}
