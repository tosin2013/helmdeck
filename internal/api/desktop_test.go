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

	"github.com/tosin2013/helmdeck/internal/session"
)

// fakeExecutor records every Exec call and replays a scripted reply.
// Tests can inspect captured calls to assert on the exact xdotool /
// scrot argv the desktop endpoints emit.
type fakeExecutor struct {
	calls []capturedExec
	// reply is the canned response. If err is set, ExecResult is
	// ignored. resultByCmd lets a test return different replies for
	// different argv0 values (used by the windows listing test).
	reply       session.ExecResult
	err         error
	resultByCmd map[string]session.ExecResult
}

type capturedExec struct {
	SessionID string
	Cmd       []string
	Env       []string
}

func (f *fakeExecutor) Exec(ctx context.Context, id string, req session.ExecRequest) (session.ExecResult, error) {
	f.calls = append(f.calls, capturedExec{SessionID: id, Cmd: req.Cmd, Env: req.Env})
	if f.err != nil {
		return session.ExecResult{}, f.err
	}
	if f.resultByCmd != nil {
		// Match against the joined script body for sh -c invocations,
		// otherwise the argv0 of a non-shell call.
		key := req.Cmd[0]
		if len(req.Cmd) >= 3 && req.Cmd[0] == "sh" && req.Cmd[1] == "-c" {
			key = req.Cmd[2]
		}
		for k, v := range f.resultByCmd {
			if strings.Contains(key, k) {
				return v, nil
			}
		}
	}
	return f.reply, nil
}

func newDesktopRouter(t *testing.T, ex *fakeExecutor) http.Handler {
	t.Helper()
	return NewRouter(Deps{
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:  "test",
		Executor: ex,
	})
}

func doDesktop(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestDesktopScreenshot(t *testing.T) {
	pngHead := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00}
	ex := &fakeExecutor{reply: session.ExecResult{Stdout: pngHead}}
	h := newDesktopRouter(t, ex)
	rr := doDesktop(t, h, http.MethodPost, "/api/v1/desktop/screenshot",
		`{"session_id":"sess-1"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr.Header().Get("Content-Type") != "image/png" {
		t.Errorf("wrong content-type: %s", rr.Header().Get("Content-Type"))
	}
	if !strings.HasPrefix(rr.Body.String(), "\x89PNG") {
		t.Errorf("body is not a PNG: %q", rr.Body.String()[:8])
	}
	if len(ex.calls) != 1 {
		t.Fatalf("expected 1 exec call, got %d", len(ex.calls))
	}
	if ex.calls[0].SessionID != "sess-1" {
		t.Errorf("session id not propagated: %s", ex.calls[0].SessionID)
	}
	if !strings.Contains(ex.calls[0].Env[0], "DISPLAY=:99") {
		t.Errorf("DISPLAY env not set: %v", ex.calls[0].Env)
	}
}

func TestDesktopClick(t *testing.T) {
	cases := []struct{ button, want string }{
		{"", "1"},
		{"left", "1"},
		{"middle", "2"},
		{"right", "3"},
	}
	for _, tc := range cases {
		t.Run(tc.button, func(t *testing.T) {
			ex := &fakeExecutor{}
			h := newDesktopRouter(t, ex)
			body := `{"session_id":"sess-1","x":42,"y":99`
			if tc.button != "" {
				body += `,"button":"` + tc.button + `"`
			}
			body += `}`
			rr := doDesktop(t, h, http.MethodPost, "/api/v1/desktop/click", body)
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			joined := strings.Join(ex.calls[0].Cmd, " ")
			if !strings.Contains(joined, "mousemove 42 99 click "+tc.want) {
				t.Errorf("wrong cmd: %s", joined)
			}
		})
	}
}

func TestDesktopClickInvalidButton(t *testing.T) {
	h := newDesktopRouter(t, &fakeExecutor{})
	rr := doDesktop(t, h, http.MethodPost, "/api/v1/desktop/click",
		`{"session_id":"s","x":1,"y":2,"button":"laser"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", rr.Code)
	}
}

func TestDesktopType(t *testing.T) {
	ex := &fakeExecutor{}
	h := newDesktopRouter(t, ex)
	rr := doDesktop(t, h, http.MethodPost, "/api/v1/desktop/type",
		`{"session_id":"s","text":"hello world","delay_ms":50}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	cmd := ex.calls[0].Cmd
	// argv: xdotool type --delay 50 -- "hello world"
	if cmd[0] != "xdotool" || cmd[1] != "type" || cmd[len(cmd)-1] != "hello world" {
		t.Errorf("bad argv: %v", cmd)
	}
}

func TestDesktopTypeMissingText(t *testing.T) {
	h := newDesktopRouter(t, &fakeExecutor{})
	rr := doDesktop(t, h, http.MethodPost, "/api/v1/desktop/type", `{"session_id":"s","text":""}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestDesktopKey(t *testing.T) {
	ex := &fakeExecutor{}
	h := newDesktopRouter(t, ex)
	rr := doDesktop(t, h, http.MethodPost, "/api/v1/desktop/key",
		`{"session_id":"s","keys":"ctrl+a"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	if ex.calls[0].Cmd[len(ex.calls[0].Cmd)-1] != "ctrl+a" {
		t.Errorf("keys not propagated: %v", ex.calls[0].Cmd)
	}
}

func TestDesktopLaunch(t *testing.T) {
	ex := &fakeExecutor{}
	h := newDesktopRouter(t, ex)
	rr := doDesktop(t, h, http.MethodPost, "/api/v1/desktop/launch",
		`{"session_id":"s","command":"xterm","args":["-e","echo hi"]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	script := strings.Join(ex.calls[0].Cmd, " ")
	if !strings.Contains(script, "nohup setsid 'xterm' '-e' 'echo hi'") {
		t.Errorf("launch script wrong: %s", script)
	}
}

func TestDesktopWindowsListing(t *testing.T) {
	ex := &fakeExecutor{
		resultByCmd: map[string]session.ExecResult{
			"xdotool search": {Stdout: []byte("12345\t100\tFirefox\n67890\t200\tTerminal\n")},
		},
	}
	h := newDesktopRouter(t, ex)
	rr := doDesktop(t, h, http.MethodGet, "/api/v1/desktop/windows?session_id=s", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Windows []DesktopWindow `json:"windows"`
		Count   int             `json:"count"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Count != 2 {
		t.Fatalf("expected 2 windows, got %d: %s", resp.Count, rr.Body.String())
	}
	if resp.Windows[0].ID != "12345" || resp.Windows[0].Name != "Firefox" || resp.Windows[0].PID != 100 {
		t.Errorf("first window parsed wrong: %+v", resp.Windows[0])
	}
}

func TestDesktopFocus(t *testing.T) {
	ex := &fakeExecutor{}
	h := newDesktopRouter(t, ex)
	rr := doDesktop(t, h, http.MethodPost, "/api/v1/desktop/focus",
		`{"session_id":"s","window_id":"12345"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	cmd := ex.calls[0].Cmd
	if cmd[len(cmd)-1] != "12345" || cmd[1] != "windowactivate" {
		t.Errorf("bad argv: %v", cmd)
	}
}

func TestDesktopFocusRejectsInjection(t *testing.T) {
	h := newDesktopRouter(t, &fakeExecutor{})
	rr := doDesktop(t, h, http.MethodPost, "/api/v1/desktop/focus",
		`{"session_id":"s","window_id":"12345; rm -rf /"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-numeric window id, got %d", rr.Code)
	}
}

func TestDesktopMissingSessionID(t *testing.T) {
	h := newDesktopRouter(t, &fakeExecutor{})
	rr := doDesktop(t, h, http.MethodPost, "/api/v1/desktop/click",
		`{"x":1,"y":2}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestDesktopNoExecutorReturns503(t *testing.T) {
	h := NewRouter(Deps{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
	})
	rr := doDesktop(t, h, http.MethodPost, "/api/v1/desktop/click",
		`{"session_id":"s","x":1,"y":2}`)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rr.Code)
	}
}

func TestDesktopExecFailureMaps502(t *testing.T) {
	ex := &fakeExecutor{reply: session.ExecResult{ExitCode: 1, Stderr: []byte("Can't open display")}}
	h := newDesktopRouter(t, ex)
	rr := doDesktop(t, h, http.MethodPost, "/api/v1/desktop/click",
		`{"session_id":"s","x":1,"y":2}`)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "command_failed") {
		t.Errorf("expected command_failed code: %s", rr.Body.String())
	}
}

// T807f desktop primitive coverage ---------------------------------
//
// Each new endpoint gets a happy-path test that asserts the xdotool
// (or scrot+convert) argv the handler emitted contains the expected
// verbs in the expected order. Error cases (invalid direction,
// missing modifiers, invalid button) get one-liner subtests.

// execScriptBody returns the `sh -c` script body a desktop handler
// passed through, or argv[0] for direct xdotool invocations. Every
// assertion below checks this string for the expected xdotool verb
// sequence.
func execScriptBody(call capturedExec) string {
	if len(call.Cmd) >= 3 && call.Cmd[0] == "sh" && call.Cmd[1] == "-c" {
		return call.Cmd[2]
	}
	return strings.Join(call.Cmd, " ")
}

func TestDesktopDoubleClick(t *testing.T) {
	ex := &fakeExecutor{}
	h := newDesktopRouter(t, ex)
	rr := doDesktop(t, h, http.MethodPost, "/api/v1/desktop/double_click",
		`{"session_id":"s","x":10,"y":20}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	body := execScriptBody(ex.calls[0])
	if !strings.Contains(body, "mousemove 10 20") {
		t.Errorf("missing mousemove: %s", body)
	}
	if !strings.Contains(body, "--repeat 2") {
		t.Errorf("expected --repeat 2: %s", body)
	}
	if !strings.Contains(body, " 1") { // button 1 = left
		t.Errorf("expected button 1: %s", body)
	}
}

func TestDesktopTripleClick(t *testing.T) {
	ex := &fakeExecutor{}
	h := newDesktopRouter(t, ex)
	rr := doDesktop(t, h, http.MethodPost, "/api/v1/desktop/triple_click",
		`{"session_id":"s","x":5,"y":5}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	body := execScriptBody(ex.calls[0])
	if !strings.Contains(body, "--repeat 3") {
		t.Errorf("expected --repeat 3: %s", body)
	}
}

func TestDesktopDrag(t *testing.T) {
	ex := &fakeExecutor{}
	h := newDesktopRouter(t, ex)
	rr := doDesktop(t, h, http.MethodPost, "/api/v1/desktop/drag",
		`{"session_id":"s","start_x":10,"start_y":20,"end_x":100,"end_y":200}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	body := execScriptBody(ex.calls[0])
	// Full drag verb sequence: mousemove start → mousedown →
	// mousemove end → mouseup, all in one xdotool invocation.
	for _, want := range []string{
		"mousemove 10 20", "mousedown 1", "mousemove 100 200", "mouseup 1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in drag script: %s", want, body)
		}
	}
}

func TestDesktopScroll(t *testing.T) {
	cases := []struct {
		name, body, wantButton string
		wantRepeat             int
	}{
		{"down default", `{"session_id":"s","direction":"down"}`, " 5", 3},
		{"up with amount", `{"session_id":"s","direction":"up","amount":7}`, " 4", 7},
		{"left", `{"session_id":"s","direction":"left","amount":2}`, " 6", 2},
		{"right", `{"session_id":"s","direction":"right","amount":1}`, " 7", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ex := &fakeExecutor{}
			h := newDesktopRouter(t, ex)
			rr := doDesktop(t, h, http.MethodPost, "/api/v1/desktop/scroll", tc.body)
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			script := execScriptBody(ex.calls[0])
			if !strings.Contains(script, tc.wantButton) {
				t.Errorf("wrong button in %s: %s", tc.name, script)
			}
			if !strings.Contains(script, "--repeat") {
				t.Errorf("missing --repeat: %s", script)
			}
		})
	}
}

func TestDesktopScroll_AmountCapped(t *testing.T) {
	ex := &fakeExecutor{}
	h := newDesktopRouter(t, ex)
	rr := doDesktop(t, h, http.MethodPost, "/api/v1/desktop/scroll",
		`{"session_id":"s","direction":"down","amount":9999}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	body := execScriptBody(ex.calls[0])
	if !strings.Contains(body, "--repeat 50") {
		t.Errorf("amount should cap at 50: %s", body)
	}
}

func TestDesktopScroll_InvalidDirection(t *testing.T) {
	ex := &fakeExecutor{}
	h := newDesktopRouter(t, ex)
	rr := doDesktop(t, h, http.MethodPost, "/api/v1/desktop/scroll",
		`{"session_id":"s","direction":"sideways"}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
	if len(ex.calls) != 0 {
		t.Errorf("bad direction should short-circuit before exec")
	}
}

func TestDesktopModifierClick(t *testing.T) {
	ex := &fakeExecutor{}
	h := newDesktopRouter(t, ex)
	rr := doDesktop(t, h, http.MethodPost, "/api/v1/desktop/modifier_click",
		`{"session_id":"s","x":100,"y":200,"modifiers":["ctrl","shift"]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	// The handler passes argv directly (no sh -c), so we check the
	// argv slice.
	argv := ex.calls[0].Cmd
	joined := strings.Join(argv, " ")
	for _, want := range []string{
		"keydown ctrl", "keydown shift",
		"mousemove 100 200", "click 1",
		"keyup ctrl", "keyup shift",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in modifier click: %s", want, joined)
		}
	}
}

func TestDesktopModifierClick_UnknownModifier(t *testing.T) {
	ex := &fakeExecutor{}
	h := newDesktopRouter(t, ex)
	rr := doDesktop(t, h, http.MethodPost, "/api/v1/desktop/modifier_click",
		`{"session_id":"s","x":1,"y":1,"modifiers":["bogus"]}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestDesktopModifierClick_MissingModifiers(t *testing.T) {
	ex := &fakeExecutor{}
	h := newDesktopRouter(t, ex)
	rr := doDesktop(t, h, http.MethodPost, "/api/v1/desktop/modifier_click",
		`{"session_id":"s","x":1,"y":1,"modifiers":[]}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

func TestDesktopMouseMove(t *testing.T) {
	ex := &fakeExecutor{}
	h := newDesktopRouter(t, ex)
	rr := doDesktop(t, h, http.MethodPost, "/api/v1/desktop/mouse_move",
		`{"session_id":"s","x":50,"y":75}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	argv := strings.Join(ex.calls[0].Cmd, " ")
	if !strings.Contains(argv, "mousemove 50 75") {
		t.Errorf("wrong argv: %s", argv)
	}
	// No click verb — pure pointer move.
	if strings.Contains(argv, "click") {
		t.Errorf("mouse_move must not click: %s", argv)
	}
}

func TestDesktopWait(t *testing.T) {
	ex := &fakeExecutor{}
	h := newDesktopRouter(t, ex)
	rr := doDesktop(t, h, http.MethodPost, "/api/v1/desktop/wait",
		`{"session_id":"s","seconds":1.5}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	body := execScriptBody(ex.calls[0])
	if !strings.Contains(body, "sleep 1.500") {
		t.Errorf("expected sleep 1.500, got: %s", body)
	}
}

func TestDesktopWait_CappedAt30(t *testing.T) {
	ex := &fakeExecutor{}
	h := newDesktopRouter(t, ex)
	rr := doDesktop(t, h, http.MethodPost, "/api/v1/desktop/wait",
		`{"session_id":"s","seconds":600}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	body := execScriptBody(ex.calls[0])
	if !strings.Contains(body, "sleep 30.000") {
		t.Errorf("wait should cap at 30s: %s", body)
	}
}

func TestDesktopWait_InvalidSeconds(t *testing.T) {
	ex := &fakeExecutor{}
	h := newDesktopRouter(t, ex)
	rr := doDesktop(t, h, http.MethodPost, "/api/v1/desktop/wait",
		`{"session_id":"s","seconds":0}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

func TestDesktopZoom(t *testing.T) {
	pngBytes := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	ex := &fakeExecutor{reply: session.ExecResult{Stdout: pngBytes}}
	h := newDesktopRouter(t, ex)
	rr := doDesktop(t, h, http.MethodPost, "/api/v1/desktop/zoom",
		`{"session_id":"s","x1":100,"y1":200,"x2":300,"y2":500}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr.Header().Get("Content-Type") != "image/png" {
		t.Errorf("content-type wrong: %s", rr.Header().Get("Content-Type"))
	}
	body := execScriptBody(ex.calls[0])
	// Crop args: widthXheight+xoff+yoff. width=200, height=300,
	// offset=100,200.
	if !strings.Contains(body, "-crop 200x300+100+200") {
		t.Errorf("wrong crop: %s", body)
	}
	if !strings.Contains(body, "-resize 1024x1024") {
		t.Errorf("missing resize: %s", body)
	}
}

func TestDesktopZoom_FlipsCoordinates(t *testing.T) {
	pngBytes := []byte{0x89, 'P', 'N', 'G'}
	ex := &fakeExecutor{reply: session.ExecResult{Stdout: pngBytes}}
	h := newDesktopRouter(t, ex)
	// Caller sent bottom-right corner as (x1,y1) — handler should
	// normalize so x1<x2, y1<y2 before computing width/height.
	rr := doDesktop(t, h, http.MethodPost, "/api/v1/desktop/zoom",
		`{"session_id":"s","x1":300,"y1":500,"x2":100,"y2":200}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	body := execScriptBody(ex.calls[0])
	if !strings.Contains(body, "-crop 200x300+100+200") {
		t.Errorf("flipped region should normalize: %s", body)
	}
}

func TestDesktopZoom_ZeroArea(t *testing.T) {
	ex := &fakeExecutor{}
	h := newDesktopRouter(t, ex)
	rr := doDesktop(t, h, http.MethodPost, "/api/v1/desktop/zoom",
		`{"session_id":"s","x1":100,"y1":100,"x2":100,"y2":100}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for zero-area: %d", rr.Code)
	}
}
