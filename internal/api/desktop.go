// Package api — desktop actions REST surface (T401, ADR 027).
//
// The desktop endpoints expose xdotool/scrot driven by the existing
// session.Executor against any session container running in desktop
// mode (Xvfb on DISPLAY=:99 + XFCE4 + chromium, started by
// deploy/docker/sidecar-entrypoint.sh when SIDECAR_MODE=desktop).
//
// Every endpoint follows the same shape as the browser CDP endpoints:
// JSON request body, session_id field, executor lookup, typed JSON
// response or PNG bytes for screenshot. JWT enforcement and audit
// logging come for free from the /api/v1/* prefix.
//
// Command injection: xdotool's `type` and `key` subcommands take the
// payload as a single arg, and we always pass argv via the session
// Executor's Cmd []string (no shell expansion) so user input cannot
// escape into a shell context. The launch endpoint runs an arbitrary
// command inside the session container — no different from the
// existing /api/v1/browser/execute surface in terms of trust model;
// the sandbox boundary is the session container, not the API.

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/tosin2013/helmdeck/internal/session"
)

// desktopDisplay is the X server number the desktop-mode sidecar
// entrypoint starts Xvfb on. Every xdotool/scrot invocation needs
// DISPLAY=:99 in its env or it will fail with "Can't open display".
const desktopDisplay = ":99"

// Maximum response sizes — guards against runaway scrot output or
// xdotool windows scans on a desktop with thousands of windows.
const (
	maxScreenshotBytes = 32 << 20 // 32 MiB
	maxWindowsListed   = 1024
)

type desktopClickRequest struct {
	SessionID string `json:"session_id"`
	X         int    `json:"x"`
	Y         int    `json:"y"`
	Button    string `json:"button,omitempty"` // left|right|middle (default left)
}

type desktopTypeRequest struct {
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
	DelayMS   int    `json:"delay_ms,omitempty"` // per-keystroke delay
}

type desktopKeyRequest struct {
	SessionID string `json:"session_id"`
	Keys      string `json:"keys"` // xdotool key spec, e.g. "ctrl+a", "Return"
}

type desktopLaunchRequest struct {
	SessionID string   `json:"session_id"`
	Command   string   `json:"command"`
	Args      []string `json:"args,omitempty"`
}

type desktopFocusRequest struct {
	SessionID string `json:"session_id"`
	WindowID  string `json:"window_id"`
}

type desktopScreenshotRequest struct {
	SessionID string `json:"session_id"`
}

// T807f request types — primitives added so the desktop runtime speaks
// the full Claude computer_20251124 / Gemini computer-use-preview
// action surface. Every struct uses named fields so we can add new
// optional arguments without breaking existing callers.

type desktopDoubleClickRequest struct {
	SessionID string `json:"session_id"`
	X         int    `json:"x"`
	Y         int    `json:"y"`
	Button    string `json:"button,omitempty"` // left|right|middle (default left)
}

type desktopTripleClickRequest struct {
	SessionID string `json:"session_id"`
	X         int    `json:"x"`
	Y         int    `json:"y"`
	Button    string `json:"button,omitempty"` // default left
}

// desktopDragRequest moves the pointer to (StartX, StartY), holds
// the button, drags to (EndX, EndY), then releases. xdotool's
// `mousedown` / `mouseup` verbs chain the whole motion in one
// invocation so there is no synthetic delay mid-drag.
type desktopDragRequest struct {
	SessionID string `json:"session_id"`
	StartX    int    `json:"start_x"`
	StartY    int    `json:"start_y"`
	EndX      int    `json:"end_x"`
	EndY      int    `json:"end_y"`
	Button    string `json:"button,omitempty"` // default left
}

// desktopScrollRequest scrolls at a given point. Direction is
// "up"/"down"/"left"/"right". Amount is the number of scroll clicks
// to emit (xdotool --repeat N). Coordinates are optional — when
// omitted, the scroll lands wherever the cursor currently is.
type desktopScrollRequest struct {
	SessionID string `json:"session_id"`
	X         int    `json:"x"`
	Y         int    `json:"y"`
	HasCoord  bool   `json:"-"` // set by the decoder when X/Y present
	Direction string `json:"direction"`
	Amount    int    `json:"amount,omitempty"` // default 3
}

// desktopModifierClickRequest clicks at (X, Y) while holding one or
// more modifier keys. Modifiers are xdotool key names: "shift",
// "ctrl", "alt", "super". Used by Claude's computer_20251124 when it
// emits a click with the `text` modifier field.
type desktopModifierClickRequest struct {
	SessionID string   `json:"session_id"`
	X         int      `json:"x"`
	Y         int      `json:"y"`
	Button    string   `json:"button,omitempty"`
	Modifiers []string `json:"modifiers"`
}

// desktopMouseMoveRequest moves the pointer without clicking —
// useful for hover-driven UIs and for `mouse_move` in the native
// computer-use schemas.
type desktopMouseMoveRequest struct {
	SessionID string `json:"session_id"`
	X         int    `json:"x"`
	Y         int    `json:"y"`
}

// desktopWaitRequest blocks for Seconds on the control plane side.
// Cheap convenience for models that emit a `wait` action and expect
// time to elapse before the next snapshot. Capped at 30s so a
// hallucinated large value cannot wedge a session.
type desktopWaitRequest struct {
	SessionID string  `json:"session_id"`
	Seconds   float64 `json:"seconds"`
}

// desktopZoomRequest crops a region of the current screen and
// resizes it for the LLM to inspect at higher effective resolution —
// maps onto Claude `computer_20251124`'s `zoom` action. The region
// is expressed as [x1, y1, x2, y2] (top-left / bottom-right). Output
// is a PNG served back on the HTTP response (same shape as the
// regular screenshot endpoint).
type desktopZoomRequest struct {
	SessionID string `json:"session_id"`
	X1        int    `json:"x1"`
	Y1        int    `json:"y1"`
	X2        int    `json:"x2"`
	Y2        int    `json:"y2"`
}

// DesktopWindow is one entry in the windows listing. ID is the X11
// window id (decimal string from xdotool), Name is the window title,
// PID is the owning process id (best-effort; some windows lack one).
type DesktopWindow struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	PID  int    `json:"pid,omitempty"`
}

func registerDesktopRoutes(mux *http.ServeMux, deps Deps) {
	if deps.Executor == nil {
		mux.HandleFunc("/api/v1/desktop/", func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusServiceUnavailable, "executor_unavailable",
				"desktop actions require a session.Executor backend")
		})
		return
	}
	ex := deps.Executor

	// run is a small wrapper that injects DISPLAY and maps Executor
	// errors / non-zero exit codes onto the desktop endpoints' typed
	// error vocabulary. Returns the result so callers can inspect
	// stdout when they need to (e.g. windows listing, screenshot).
	run := func(w http.ResponseWriter, r *http.Request, sessionID string, cmd []string) (session.ExecResult, bool) {
		if sessionID == "" {
			writeError(w, http.StatusBadRequest, "missing_session_id", "session_id is required")
			return session.ExecResult{}, false
		}
		res, err := ex.Exec(r.Context(), sessionID, session.ExecRequest{
			Cmd: cmd,
			Env: []string{"DISPLAY=" + desktopDisplay},
		})
		if err != nil {
			if errors.Is(err, session.ErrSessionNotFound) {
				writeError(w, http.StatusNotFound, "not_found", "session not found")
				return session.ExecResult{}, false
			}
			writeError(w, http.StatusBadGateway, "exec_failed", err.Error())
			return session.ExecResult{}, false
		}
		if res.ExitCode != 0 {
			writeError(w, http.StatusBadGateway, "command_failed",
				fmt.Sprintf("exit %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr))))
			return session.ExecResult{}, false
		}
		return res, true
	}

	mux.HandleFunc("POST /api/v1/desktop/screenshot", func(w http.ResponseWriter, r *http.Request) {
		var req desktopScreenshotRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		// scrot writes to a temp file then we cat it back. Using
		// `scrot -` (stdout) is only supported in scrot ≥1.2 and the
		// sidecar's apt repo may ship 1.0; the temp-file dance is
		// portable. -o overwrites silently.
		tmp := "/tmp/helmdeck-shot.png"
		res, ok := run(w, r, req.SessionID, []string{
			"sh", "-c",
			"scrot -o " + tmp + " >/dev/null && cat " + tmp + " && rm -f " + tmp,
		})
		if !ok {
			return
		}
		if len(res.Stdout) == 0 {
			writeError(w, http.StatusBadGateway, "command_failed", "scrot produced no output")
			return
		}
		if len(res.Stdout) > maxScreenshotBytes {
			writeError(w, http.StatusInternalServerError, "screenshot_too_large",
				fmt.Sprintf("scrot returned %d bytes (max %d)", len(res.Stdout), maxScreenshotBytes))
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(res.Stdout)
	})

	mux.HandleFunc("POST /api/v1/desktop/click", func(w http.ResponseWriter, r *http.Request) {
		var req desktopClickRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		button := "1"
		switch strings.ToLower(req.Button) {
		case "", "left":
			button = "1"
		case "middle":
			button = "2"
		case "right":
			button = "3"
		default:
			writeError(w, http.StatusBadRequest, "invalid_button",
				"button must be left, middle, or right")
			return
		}
		// xdotool mousemove + click in one invocation: pass the two
		// commands joined with `--`-style separators isn't supported,
		// so chain via sh -c with explicit numeric args (the integers
		// can't carry shell injection).
		cmd := []string{
			"sh", "-c",
			fmt.Sprintf("xdotool mousemove %d %d click %s",
				req.X, req.Y, button),
		}
		if _, ok := run(w, r, req.SessionID, cmd); !ok {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "x": req.X, "y": req.Y, "button": button})
	})

	mux.HandleFunc("POST /api/v1/desktop/type", func(w http.ResponseWriter, r *http.Request) {
		var req desktopTypeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		if req.Text == "" {
			writeError(w, http.StatusBadRequest, "missing_text", "text is required")
			return
		}
		// xdotool type takes the literal string as a single arg —
		// passing via Cmd []string keeps it out of any shell context.
		cmd := []string{"xdotool", "type"}
		if req.DelayMS > 0 {
			cmd = append(cmd, "--delay", strconv.Itoa(req.DelayMS))
		}
		cmd = append(cmd, "--", req.Text)
		if _, ok := run(w, r, req.SessionID, cmd); !ok {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "length": len(req.Text)})
	})

	mux.HandleFunc("POST /api/v1/desktop/key", func(w http.ResponseWriter, r *http.Request) {
		var req desktopKeyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		if req.Keys == "" {
			writeError(w, http.StatusBadRequest, "missing_keys", "keys is required")
			return
		}
		if _, ok := run(w, r, req.SessionID, []string{"xdotool", "key", "--", req.Keys}); !ok {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "keys": req.Keys})
	})

	mux.HandleFunc("POST /api/v1/desktop/launch", func(w http.ResponseWriter, r *http.Request) {
		var req desktopLaunchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		if req.Command == "" {
			writeError(w, http.StatusBadRequest, "missing_command", "command is required")
			return
		}
		// nohup + setsid + & so the launched process outlives this
		// exec rpc and detaches from xdotool's session — otherwise
		// the application would die when our Exec returns.
		quoted := []string{shellQuote(req.Command)}
		for _, a := range req.Args {
			quoted = append(quoted, shellQuote(a))
		}
		cmd := []string{
			"sh", "-c",
			"nohup setsid " + strings.Join(quoted, " ") + " >/dev/null 2>&1 &",
		}
		if _, ok := run(w, r, req.SessionID, cmd); !ok {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "command": req.Command})
	})

	mux.HandleFunc("GET /api/v1/desktop/windows", func(w http.ResponseWriter, r *http.Request) {
		sessionID := r.URL.Query().Get("session_id")
		// xdotool search "" returns every window id, one per line.
		// We then resolve names + pids in a single shell loop so we
		// don't pay an Exec rpc per window.
		script := `
ids=$(xdotool search --onlyvisible "" 2>/dev/null || true)
for id in $ids; do
  name=$(xdotool getwindowname "$id" 2>/dev/null || true)
  pid=$(xdotool getwindowpid "$id" 2>/dev/null || echo 0)
  printf '%s\t%s\t%s\n' "$id" "$pid" "$name"
done
`
		res, ok := run(w, r, sessionID, []string{"sh", "-c", script})
		if !ok {
			return
		}
		var out []DesktopWindow
		for _, line := range strings.Split(strings.TrimSpace(string(res.Stdout)), "\n") {
			if line == "" {
				continue
			}
			parts := strings.SplitN(line, "\t", 3)
			if len(parts) < 3 {
				continue
			}
			pid, _ := strconv.Atoi(parts[1])
			out = append(out, DesktopWindow{ID: parts[0], PID: pid, Name: parts[2]})
			if len(out) >= maxWindowsListed {
				break
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"windows": out, "count": len(out)})
	})

	mux.HandleFunc("POST /api/v1/desktop/focus", func(w http.ResponseWriter, r *http.Request) {
		var req desktopFocusRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		if req.WindowID == "" {
			writeError(w, http.StatusBadRequest, "missing_window_id", "window_id is required")
			return
		}
		// windowactivate brings the window to the front and sets it
		// as the focused window — the next type/key call will land
		// in it. Reject non-numeric window ids before shelling out.
		if _, err := strconv.ParseUint(req.WindowID, 10, 64); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_window_id", "window_id must be a numeric X11 window id")
			return
		}
		if _, ok := run(w, r, req.SessionID, []string{"xdotool", "windowactivate", "--sync", req.WindowID}); !ok {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "window_id": req.WindowID})
	})

	// T807f primitives ----------------------------------------------
	//
	// Every endpoint below reuses the same `run` helper and follows
	// the POST /api/v1/desktop/* shape as the original click/type/key
	// trio. They're the minimum set a native computer-use tool call
	// from Claude / OpenAI / Gemini needs the desktop runtime to
	// support.

	mux.HandleFunc("POST /api/v1/desktop/double_click", func(w http.ResponseWriter, r *http.Request) {
		var req desktopDoubleClickRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		button, ok := normalizeButton(w, req.Button)
		if !ok {
			return
		}
		// xdotool click supports --repeat + --delay so a double click
		// lands in one invocation. --delay is in milliseconds between
		// clicks; 100ms matches what desktop environments expect.
		cmd := []string{
			"sh", "-c",
			fmt.Sprintf("xdotool mousemove %d %d click --repeat 2 --delay 100 %s",
				req.X, req.Y, button),
		}
		if _, runOK := run(w, r, req.SessionID, cmd); !runOK {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "x": req.X, "y": req.Y, "clicks": 2})
	})

	mux.HandleFunc("POST /api/v1/desktop/triple_click", func(w http.ResponseWriter, r *http.Request) {
		var req desktopTripleClickRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		button, ok := normalizeButton(w, req.Button)
		if !ok {
			return
		}
		cmd := []string{
			"sh", "-c",
			fmt.Sprintf("xdotool mousemove %d %d click --repeat 3 --delay 100 %s",
				req.X, req.Y, button),
		}
		if _, runOK := run(w, r, req.SessionID, cmd); !runOK {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "x": req.X, "y": req.Y, "clicks": 3})
	})

	mux.HandleFunc("POST /api/v1/desktop/drag", func(w http.ResponseWriter, r *http.Request) {
		var req desktopDragRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		button, ok := normalizeButton(w, req.Button)
		if !ok {
			return
		}
		// xdotool chains verbs within a single invocation, so the
		// drag is atomic: mousedown stays held for the entire
		// mousemove → mouseup sequence with no Exec RPC overhead
		// between sub-actions.
		cmd := []string{
			"sh", "-c",
			fmt.Sprintf("xdotool mousemove %d %d mousedown %s mousemove %d %d mouseup %s",
				req.StartX, req.StartY, button, req.EndX, req.EndY, button),
		}
		if _, runOK := run(w, r, req.SessionID, cmd); !runOK {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"start_x": req.StartX, "start_y": req.StartY,
			"end_x": req.EndX, "end_y": req.EndY,
		})
	})

	mux.HandleFunc("POST /api/v1/desktop/scroll", func(w http.ResponseWriter, r *http.Request) {
		var req desktopScrollRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		// xdotool uses button 4 = scroll up, 5 = scroll down,
		// 6 = left, 7 = right. --repeat multiplies the click count,
		// which is the canonical "amount" knob for scroll actions.
		var button string
		switch strings.ToLower(req.Direction) {
		case "up":
			button = "4"
		case "down":
			button = "5"
		case "left":
			button = "6"
		case "right":
			button = "7"
		default:
			writeError(w, http.StatusBadRequest, "invalid_direction",
				"direction must be up, down, left, or right")
			return
		}
		amount := req.Amount
		if amount <= 0 {
			amount = 3
		}
		if amount > 50 {
			// Hard cap to keep a hallucinated large value from
			// wedging the session. 50 clicks is more than a full
			// screenful on any sane wheel resolution.
			amount = 50
		}
		var script string
		if req.X != 0 || req.Y != 0 {
			script = fmt.Sprintf("xdotool mousemove %d %d click --repeat %d %s",
				req.X, req.Y, amount, button)
		} else {
			script = fmt.Sprintf("xdotool click --repeat %d %s", amount, button)
		}
		if _, ok := run(w, r, req.SessionID, []string{"sh", "-c", script}); !ok {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":        true,
			"direction": req.Direction,
			"amount":    amount,
		})
	})

	mux.HandleFunc("POST /api/v1/desktop/modifier_click", func(w http.ResponseWriter, r *http.Request) {
		var req desktopModifierClickRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		button, ok := normalizeButton(w, req.Button)
		if !ok {
			return
		}
		if len(req.Modifiers) == 0 {
			writeError(w, http.StatusBadRequest, "missing_modifiers",
				"modifiers must list at least one of shift, ctrl, alt, super")
			return
		}
		// Whitelist the modifier names so a crafted request can't
		// inject arbitrary xdotool key specs. xdotool uses lowercase
		// names for modifiers (ctrl, shift, alt, super).
		allowed := map[string]bool{"shift": true, "ctrl": true, "alt": true, "super": true}
		for _, m := range req.Modifiers {
			if !allowed[strings.ToLower(m)] {
				writeError(w, http.StatusBadRequest, "invalid_modifier",
					fmt.Sprintf("unknown modifier %q; must be shift, ctrl, alt, or super", m))
				return
			}
		}
		// Press every modifier, click, release every modifier.
		// Chain everything in one xdotool invocation so there's no
		// Exec RPC lag between keydown and click.
		var parts []string
		for _, m := range req.Modifiers {
			parts = append(parts, "keydown", strings.ToLower(m))
		}
		parts = append(parts, "mousemove", strconv.Itoa(req.X), strconv.Itoa(req.Y), "click", button)
		for _, m := range req.Modifiers {
			parts = append(parts, "keyup", strings.ToLower(m))
		}
		cmd := append([]string{"xdotool"}, parts...)
		if _, runOK := run(w, r, req.SessionID, cmd); !runOK {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "x": req.X, "y": req.Y, "modifiers": req.Modifiers,
		})
	})

	mux.HandleFunc("POST /api/v1/desktop/mouse_move", func(w http.ResponseWriter, r *http.Request) {
		var req desktopMouseMoveRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		cmd := []string{"xdotool", "mousemove", strconv.Itoa(req.X), strconv.Itoa(req.Y)}
		if _, ok := run(w, r, req.SessionID, cmd); !ok {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "x": req.X, "y": req.Y})
	})

	mux.HandleFunc("POST /api/v1/desktop/wait", func(w http.ResponseWriter, r *http.Request) {
		var req desktopWaitRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		if req.Seconds <= 0 {
			writeError(w, http.StatusBadRequest, "invalid_seconds",
				"seconds must be > 0")
			return
		}
		// Hard cap at 30s. A hallucinated long wait from a model
		// would otherwise stall the pack handler for minutes. 30s is
		// plenty of settle time for any UI transition.
		const maxWait = 30.0
		secs := req.Seconds
		if secs > maxWait {
			secs = maxWait
		}
		// Run `sleep` inside the session container so the timing is
		// observed from the sidecar's frame of reference — same
		// place xdotool commands execute. This avoids drift if the
		// control-plane clock differs from the session's.
		script := fmt.Sprintf("sleep %.3f", secs)
		if _, ok := run(w, r, req.SessionID, []string{"sh", "-c", script}); !ok {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "seconds": secs})
	})

	mux.HandleFunc("POST /api/v1/desktop/zoom", func(w http.ResponseWriter, r *http.Request) {
		var req desktopZoomRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		// Normalize the region so x1/y1 is the top-left even if the
		// caller flipped the corners. Reject a zero-area region
		// rather than running scrot on nothing and returning a
		// confusing empty PNG.
		x1, y1, x2, y2 := req.X1, req.Y1, req.X2, req.Y2
		if x1 > x2 {
			x1, x2 = x2, x1
		}
		if y1 > y2 {
			y1, y2 = y2, y1
		}
		width := x2 - x1
		height := y2 - y1
		if width <= 0 || height <= 0 {
			writeError(w, http.StatusBadRequest, "invalid_region",
				"zoom region must have non-zero width and height")
			return
		}
		// scrot -> imagemagick crop + upscale. scrot 1.0 doesn't
		// ship a --section flag, so the full-frame-then-crop dance
		// via `convert` is the portable path — imagemagick is
		// already in layer 4 of sidecar.Dockerfile.
		tmpFull := "/tmp/helmdeck-zoom-full.png"
		tmpOut := "/tmp/helmdeck-zoom-out.png"
		script := fmt.Sprintf(
			"scrot -o %s >/dev/null && "+
				"convert %s -crop %dx%d+%d+%d -resize 1024x1024 %s && "+
				"cat %s && rm -f %s %s",
			tmpFull,
			tmpFull, width, height, x1, y1, tmpOut,
			tmpOut,
			tmpFull, tmpOut,
		)
		res, ok := run(w, r, req.SessionID, []string{"sh", "-c", script})
		if !ok {
			return
		}
		if len(res.Stdout) == 0 {
			writeError(w, http.StatusBadGateway, "command_failed", "zoom produced no output")
			return
		}
		if len(res.Stdout) > maxScreenshotBytes {
			writeError(w, http.StatusInternalServerError, "screenshot_too_large",
				fmt.Sprintf("zoom returned %d bytes (max %d)", len(res.Stdout), maxScreenshotBytes))
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(res.Stdout)
	})
}

// normalizeButton validates the button field on click-shaped
// requests and returns the xdotool numeric button code. Writes a
// 400 and returns ok=false when the button name is unknown so
// callers can just `return` on failure.
func normalizeButton(w http.ResponseWriter, name string) (string, bool) {
	switch strings.ToLower(name) {
	case "", "left":
		return "1", true
	case "middle":
		return "2", true
	case "right":
		return "3", true
	default:
		writeError(w, http.StatusBadRequest, "invalid_button",
			"button must be left, middle, or right")
		return "", false
	}
}

// shellQuote wraps an arg in single quotes for safe inclusion in a
// shell command line. The only character we have to escape is the
// single quote itself; replace ' with '\'' (close, escaped, reopen).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
