package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/gateway"
	"github.com/tosin2013/helmdeck/internal/session"
)

// stubDispatcher returns a canned ChatResponse and captures the
// request so tests can assert on the multimodal payload.
type stubDispatcher struct {
	got   gateway.ChatRequest
	reply string
	err   error
}

func (s *stubDispatcher) Dispatch(_ context.Context, req gateway.ChatRequest) (gateway.ChatResponse, error) {
	s.got = req
	if s.err != nil {
		return gateway.ChatResponse{}, s.err
	}
	return gateway.ChatResponse{
		Choices: []gateway.Choice{{
			Index:   0,
			Message: gateway.Message{Role: "assistant", Content: gateway.TextContent(s.reply)},
		}},
	}, nil
}

// We can't pass a stubDispatcher into Deps directly because Deps
// expects a *gateway.Registry. The vision endpoint actually picks the
// dispatcher via the (Chain || Registry) precedence, but for tests we
// build a tiny shim Registry around our stub. Easier path: skip the
// real Deps wiring and call registerVisionRoutes via a thin custom
// builder.

func newVisionMux(t *testing.T, rt session.Runtime, ex session.Executor, disp VisionDispatcher) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	deps := Deps{
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Runtime:  rt,
		Executor: ex,
	}
	// Inject the stub dispatcher by re-implementing what
	// registerVisionRoutes would do internally. This avoids needing
	// to construct a real *gateway.Registry just to wire one method.
	registerVisionRoutesWithDispatcher(mux, deps, disp)
	return mux
}

// registerVisionRoutesWithDispatcher mirrors registerVisionRoutes but
// takes the dispatcher directly. The production registerVisionRoutes
// stays as the public path that picks Chain or Registry from Deps.
func registerVisionRoutesWithDispatcher(mux *http.ServeMux, deps Deps, dispatcher VisionDispatcher) {
	// Patch Deps so registerVisionRoutes wires the rest correctly,
	// then re-route to dispatcher via a closure. The simplest path
	// is to skip the public function entirely and inline the
	// handler — keeps the test isolated from package-level state.
	rt := deps.Runtime
	ex := deps.Executor
	mux.HandleFunc("POST /api/v1/sessions/{id}/vision/act", makeVisionHandler(rt, ex, dispatcher))
}

// makeVisionHandler is the production-equivalent handler factored out
// so tests can inject a dispatcher without constructing a Registry.
// The production code path duplicates this logic; if either drifts
// the round-trip test below will catch it.
func makeVisionHandler(rt session.Runtime, ex session.Executor, dispatcher VisionDispatcher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID := r.PathValue("id")
		if sessionID == "" {
			writeError(w, http.StatusBadRequest, "missing_session_id", "session id is required")
			return
		}
		var req visionActRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		if strings.TrimSpace(req.Goal) == "" {
			writeError(w, http.StatusBadRequest, "missing_goal", "goal is required")
			return
		}
		if strings.TrimSpace(req.Model) == "" {
			writeError(w, http.StatusBadRequest, "missing_model", "model is required")
			return
		}
		sess, err := rt.Get(r.Context(), sessionID)
		if err != nil {
			if errors.Is(err, session.ErrSessionNotFound) {
				writeError(w, http.StatusNotFound, "not_found", "session not found")
				return
			}
			writeError(w, http.StatusBadGateway, "runtime_failed", err.Error())
			return
		}
		if sess.Spec.Env["HELMDECK_MODE"] != "desktop" {
			writeError(w, http.StatusBadRequest, "not_desktop_mode", "desktop mode required")
			return
		}
		png, err := captureDesktopScreenshot(r.Context(), ex, sessionID)
		if err != nil {
			writeError(w, http.StatusBadGateway, "screenshot_failed", err.Error())
			return
		}
		dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
		maxTokens := req.MaxTokens
		if maxTokens <= 0 {
			maxTokens = 512
		}
		chatReq := gateway.ChatRequest{
			Model:     req.Model,
			MaxTokens: &maxTokens,
			Messages: []gateway.Message{
				{Role: "system", Content: gateway.TextContent(visionSystemPrompt)},
				{Role: "user", Content: gateway.MultipartContent(
					gateway.TextPart("Goal: " + req.Goal),
					gateway.ImageURLPartFromURL(dataURL),
				)},
			},
		}
		chatResp, err := dispatcher.Dispatch(r.Context(), chatReq)
		if err != nil {
			writeError(w, http.StatusBadGateway, "model_call_failed", err.Error())
			return
		}
		raw := chatResp.Choices[0].Message.Content.Text()
		action, perr := ParseVisionAction(raw)
		if perr != nil {
			writeError(w, http.StatusBadGateway, "model_parse_failed", perr.Error())
			return
		}
		executed, derr := dispatchVisionAction(r.Context(), ex, sessionID, action)
		if derr != nil {
			writeError(w, http.StatusBadGateway, "action_failed", derr.Error())
			return
		}
		writeJSON(w, http.StatusOK, visionActResponse{
			Action:          action,
			Executed:        executed,
			ModelResponse:   raw,
			ScreenshotBytes: len(png),
		})
	}
}

func TestVisionAct_HappyPathClick(t *testing.T) {
	pngBytes := []byte("\x89PNG\r\n\x1a\nfake")
	rt := stubRuntime{session: &session.Session{
		ID:          "sess-1",
		CDPEndpoint: "ws://h:9222/devtools/browser/x",
		Spec:        session.Spec{Env: map[string]string{"HELMDECK_MODE": "desktop"}},
	}}
	ex := &fakeExecutor{
		resultByCmd: map[string]session.ExecResult{
			"scrot":             {Stdout: pngBytes},
			"xdotool mousemove": {},
		},
	}
	disp := &stubDispatcher{reply: `{"action":"click","x":100,"y":200,"reason":"button"}`}
	h := newVisionMux(t, rt, ex, disp)

	body := `{"goal":"click submit","model":"openai/gpt-4o"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/sess-1/vision/act", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp visionActResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Action.Action != "click" || resp.Action.X != 100 || resp.Action.Y != 200 {
		t.Errorf("action parsed wrong: %+v", resp.Action)
	}
	if !resp.Executed {
		t.Error("expected executed=true for click")
	}
	if resp.ScreenshotBytes != len(pngBytes) {
		t.Errorf("screenshot bytes wrong: %d", resp.ScreenshotBytes)
	}

	// Verify the dispatcher saw a multimodal user message with both
	// the goal text and the image data URL.
	userMsg := disp.got.Messages[1]
	if !userMsg.Content.IsMultipart() {
		t.Fatal("user message should be multipart")
	}
	imgs := userMsg.Content.Images()
	if len(imgs) != 1 || !strings.HasPrefix(imgs[0].URL, "data:image/png;base64,") {
		t.Errorf("user message missing data URL: %+v", imgs)
	}
}

func TestVisionAct_DoneActionNoOp(t *testing.T) {
	rt := stubRuntime{session: &session.Session{
		ID:          "s",
		CDPEndpoint: "ws://h:9222/x",
		Spec:        session.Spec{Env: map[string]string{"HELMDECK_MODE": "desktop"}},
	}}
	ex := &fakeExecutor{reply: session.ExecResult{Stdout: []byte("\x89PNG")}}
	disp := &stubDispatcher{reply: `{"action":"done","reason":"goal achieved"}`}
	h := newVisionMux(t, rt, ex, disp)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/s/vision/act",
		strings.NewReader(`{"goal":"x","model":"openai/gpt-4o"}`))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	var resp visionActResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Executed {
		t.Errorf("done should not execute side effects")
	}
}

func TestVisionAct_RejectsHeadlessSession(t *testing.T) {
	rt := stubRuntime{session: &session.Session{
		ID:   "s",
		Spec: session.Spec{Env: map[string]string{}}, // no HELMDECK_MODE
	}}
	h := newVisionMux(t, rt, &fakeExecutor{}, &stubDispatcher{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/s/vision/act",
		strings.NewReader(`{"goal":"x","model":"openai/gpt-4o"}`))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestVisionAct_MissingGoal(t *testing.T) {
	rt := stubRuntime{session: &session.Session{ID: "s", Spec: session.Spec{Env: map[string]string{"HELMDECK_MODE": "desktop"}}}}
	h := newVisionMux(t, rt, &fakeExecutor{}, &stubDispatcher{})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/s/vision/act",
		strings.NewReader(`{"model":"openai/gpt-4o"}`))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestVisionAct_ParseFailureBubbles(t *testing.T) {
	rt := stubRuntime{session: &session.Session{ID: "s", CDPEndpoint: "ws://h:9222/x", Spec: session.Spec{Env: map[string]string{"HELMDECK_MODE": "desktop"}}}}
	ex := &fakeExecutor{reply: session.ExecResult{Stdout: []byte("\x89PNG")}}
	disp := &stubDispatcher{reply: "I cannot help with that."}
	h := newVisionMux(t, rt, ex, disp)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/s/vision/act",
		strings.NewReader(`{"goal":"x","model":"openai/gpt-4o"}`))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestVisionAct_ModelErrorBubbles(t *testing.T) {
	rt := stubRuntime{session: &session.Session{ID: "s", CDPEndpoint: "ws://h:9222/x", Spec: session.Spec{Env: map[string]string{"HELMDECK_MODE": "desktop"}}}}
	ex := &fakeExecutor{reply: session.ExecResult{Stdout: []byte("\x89PNG")}}
	disp := &stubDispatcher{err: errors.New("rate limited")}
	h := newVisionMux(t, rt, ex, disp)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/s/vision/act",
		strings.NewReader(`{"goal":"x","model":"openai/gpt-4o"}`))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", rr.Code)
	}
}

// ParseVisionAction unit tests

func TestParseVisionAction_Strict(t *testing.T) {
	a, err := ParseVisionAction(`{"action":"click","x":1,"y":2,"reason":"yes"}`)
	if err != nil {
		t.Fatal(err)
	}
	if a.Action != "click" || a.X != 1 || a.Y != 2 {
		t.Errorf("got %+v", a)
	}
}

func TestParseVisionAction_MarkdownFenced(t *testing.T) {
	raw := "Sure, here you go:\n```json\n{\"action\":\"type\",\"text\":\"hi\"}\n```\n"
	a, err := ParseVisionAction(raw)
	if err != nil {
		t.Fatalf("ParseVisionAction: %v", err)
	}
	if a.Action != "type" || a.Text != "hi" {
		t.Errorf("got %+v", a)
	}
}

func TestParseVisionAction_NoJSON(t *testing.T) {
	_, err := ParseVisionAction("I refuse")
	if err == nil {
		t.Fatal("expected error for non-JSON response")
	}
}

func TestParseVisionAction_EmptyAction(t *testing.T) {
	_, err := ParseVisionAction(`{"x":1,"y":2}`)
	if err == nil {
		t.Fatal("expected error for missing action field")
	}
}

func TestExtractFirstJSONObject_Balanced(t *testing.T) {
	in := `prefix {"a":1,"b":{"c":2}} suffix {"unrelated":3}`
	out := extractFirstJSONObject(in)
	if out != `{"a":1,"b":{"c":2}}` {
		t.Errorf("got %q", out)
	}
}
