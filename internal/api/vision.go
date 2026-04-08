// Package api — vision-mode action endpoint (T407, ADR 027).
//
// POST /api/v1/sessions/{id}/vision/act takes a high-level goal in
// natural language ("click the submit button"), captures a screenshot
// of the session container's desktop, sends image + goal through the
// AI gateway with multimodal content (T407 prep), parses the model's
// structured action JSON response, and dispatches the action via
// the existing session.Executor against xdotool/scrot.
//
// Each call performs ONE step of the action loop. Callers iterate
// (the reference vision packs T408 wrap this with retry logic). The
// single-step shape keeps the endpoint cheap to reason about and
// matches the existing /api/v1/desktop/* primitives in spirit.
//
// The model is prompted to respond with this exact JSON shape:
//
//	{
//	  "action": "click" | "type" | "key" | "none" | "done",
//	  "x":      <int, required for click>,
//	  "y":      <int, required for click>,
//	  "text":   <string, required for type>,
//	  "keys":   <string, required for key — xdotool key spec>,
//	  "reason": <string, free-form>
//	}
//
// Permissive parsing: the response is decoded strictly first; on
// failure we fall back to extracting the first {...} block from the
// content. Frontier models tend to wrap JSON in code fences, weak
// models tend to add prose around it; both are tolerated.

package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/tosin2013/helmdeck/internal/gateway"
	"github.com/tosin2013/helmdeck/internal/session"
)

// VisionAction is the parsed structured response from the vision
// model. Exported so tests in other packages (and the reference
// vision packs in T408) can build expected fixtures.
type VisionAction struct {
	Action string `json:"action"`
	X      int    `json:"x,omitempty"`
	Y      int    `json:"y,omitempty"`
	Text   string `json:"text,omitempty"`
	Keys   string `json:"keys,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type visionActRequest struct {
	Goal      string `json:"goal"`
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens,omitempty"`
}

type visionActResponse struct {
	Action          VisionAction `json:"action"`
	Executed        bool         `json:"executed"`
	ModelResponse   string       `json:"model_response"`
	ScreenshotBytes int          `json:"screenshot_bytes"`
}

// VisionDispatcher is the gateway surface this endpoint depends on.
// Both *gateway.Registry and *gateway.Chain satisfy it via the
// existing gateway.Dispatcher interface — we re-declare it locally
// so tests can stub a single-method fake without dragging in the
// gateway test surface.
type VisionDispatcher interface {
	Dispatch(ctx context.Context, req gateway.ChatRequest) (gateway.ChatResponse, error)
}

// visionSystemPrompt instructs the model to emit a strict JSON action
// object and nothing else. Centralised so tests can assert on it and
// future tuning has a single edit point.
const visionSystemPrompt = `You control a Linux desktop. You will see a screenshot of the current screen and a user goal. Decide the SINGLE next action to advance toward the goal.

Respond with ONE JSON object and nothing else. Do not wrap it in markdown. The schema:

{
  "action": "click" | "type" | "key" | "none" | "done",
  "x":      <integer pixel x for click, required if action is click>,
  "y":      <integer pixel y for click, required if action is click>,
  "text":   <string to type, required if action is type>,
  "keys":   <xdotool key spec like "Return" or "ctrl+a", required if action is key>,
  "reason": <one-sentence explanation>
}

Use "done" when the goal is achieved. Use "none" if no action is appropriate this turn.`

func registerVisionRoutes(mux *http.ServeMux, deps Deps) {
	if deps.Runtime == nil || deps.Executor == nil {
		mux.HandleFunc("POST /api/v1/sessions/{id}/vision/act", func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusServiceUnavailable, "vision_unavailable",
				"vision actions require both a session runtime and a session.Executor")
		})
		return
	}
	// Pick the chain when present (it includes fallback rules); fall
	// back to the bare registry. Same precedence as gateway.go.
	var dispatcher VisionDispatcher
	if deps.GatewayChain != nil {
		dispatcher = deps.GatewayChain
	} else if deps.Gateway != nil {
		dispatcher = deps.Gateway
	}
	if dispatcher == nil {
		mux.HandleFunc("POST /api/v1/sessions/{id}/vision/act", func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusServiceUnavailable, "vision_unavailable",
				"vision actions require an AI gateway provider")
		})
		return
	}
	rt := deps.Runtime
	ex := deps.Executor

	mux.HandleFunc("POST /api/v1/sessions/{id}/vision/act", func(w http.ResponseWriter, r *http.Request) {
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
			writeError(w, http.StatusBadRequest, "missing_model",
				"model is required (provider/model syntax, e.g. openai/gpt-4o)")
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
			writeError(w, http.StatusBadRequest, "not_desktop_mode",
				"vision actions require a session created with HELMDECK_MODE=desktop")
			return
		}

		// 1. Capture the screen.
		png, err := captureDesktopScreenshot(r.Context(), ex, sessionID)
		if err != nil {
			writeError(w, http.StatusBadGateway, "screenshot_failed", err.Error())
			return
		}

		// 2. Build the multimodal chat request.
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

		// 3. Dispatch to the gateway.
		chatResp, err := dispatcher.Dispatch(r.Context(), chatReq)
		if err != nil {
			writeError(w, http.StatusBadGateway, "model_call_failed", err.Error())
			return
		}
		if len(chatResp.Choices) == 0 {
			writeError(w, http.StatusBadGateway, "model_empty", "model returned no choices")
			return
		}
		raw := chatResp.Choices[0].Message.Content.Text()

		// 4. Parse the action JSON.
		action, perr := ParseVisionAction(raw)
		if perr != nil {
			writeError(w, http.StatusBadGateway, "model_parse_failed",
				fmt.Sprintf("could not parse action JSON from model response: %v (raw: %q)", perr, truncate(raw, 256)))
			return
		}

		// 5. Execute the action.
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
	})
}

// captureDesktopScreenshot runs scrot inside the session container
// and returns the PNG bytes. Same temp-file dance as the desktop
// REST endpoint so it works against scrot 1.0+.
func captureDesktopScreenshot(ctx context.Context, ex session.Executor, sessionID string) ([]byte, error) {
	tmp := "/tmp/helmdeck-vision-shot.png"
	res, err := ex.Exec(ctx, sessionID, session.ExecRequest{
		Cmd: []string{"sh", "-c", "scrot -o " + tmp + " >/dev/null && cat " + tmp + " && rm -f " + tmp},
		Env: []string{"DISPLAY=:99"},
	})
	if err != nil {
		return nil, err
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("scrot exit %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	if len(res.Stdout) == 0 {
		return nil, errors.New("scrot produced no output")
	}
	return res.Stdout, nil
}

// ParseVisionAction decodes the model's JSON response into a
// VisionAction. Strict decode is tried first; on failure we extract
// the first balanced {...} block from the response and try again.
// This tolerates frontier models that wrap JSON in markdown code
// fences and weak models that emit a sentence of prose around it.
func ParseVisionAction(raw string) (VisionAction, error) {
	raw = strings.TrimSpace(raw)
	var v VisionAction
	if err := json.Unmarshal([]byte(raw), &v); err == nil {
		if v.Action == "" {
			return v, errors.New("action field is empty")
		}
		return v, nil
	}
	// Fallback: find the first balanced JSON object.
	if obj := extractFirstJSONObject(raw); obj != "" {
		if err := json.Unmarshal([]byte(obj), &v); err == nil {
			if v.Action == "" {
				return v, errors.New("action field is empty")
			}
			return v, nil
		}
	}
	return v, errors.New("no parseable JSON object found")
}

// extractFirstJSONObject scans for the first { ... } block with
// matching brace depth. Returns "" if no balanced block is present.
// Doesn't handle quoted braces inside strings perfectly — good
// enough for the action JSON shape which has no string values that
// contain braces.
func extractFirstJSONObject(s string) string {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return ""
	}
	depth := 0
	inString := false
	escape := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if escape {
			escape = false
			continue
		}
		if c == '\\' && inString {
			escape = true
			continue
		}
		if c == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch c {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

// dispatchVisionAction maps a VisionAction onto a session.Executor
// invocation. Returns (executed, err) where executed indicates whether
// any side effect was attempted — "none" and "done" are valid no-ops.
// Reuses the same xdotool/scrot envelope as the /api/v1/desktop/*
// endpoints (DISPLAY=:99, single-quote-escaped argv where shells are
// involved).
func dispatchVisionAction(ctx context.Context, ex session.Executor, sessionID string, a VisionAction) (bool, error) {
	switch strings.ToLower(a.Action) {
	case "none", "done", "":
		return false, nil
	case "click":
		cmd := []string{"sh", "-c", fmt.Sprintf("xdotool mousemove %d %d click 1", a.X, a.Y)}
		return runVisionCmd(ctx, ex, sessionID, cmd)
	case "type":
		if a.Text == "" {
			return false, errors.New("type action missing text field")
		}
		cmd := []string{"xdotool", "type", "--", a.Text}
		return runVisionCmd(ctx, ex, sessionID, cmd)
	case "key":
		if a.Keys == "" {
			return false, errors.New("key action missing keys field")
		}
		cmd := []string{"xdotool", "key", "--", a.Keys}
		return runVisionCmd(ctx, ex, sessionID, cmd)
	default:
		return false, fmt.Errorf("unknown action %q", a.Action)
	}
}

func runVisionCmd(ctx context.Context, ex session.Executor, sessionID string, cmd []string) (bool, error) {
	res, err := ex.Exec(ctx, sessionID, session.ExecRequest{
		Cmd: cmd,
		Env: []string{"DISPLAY=:99"},
	})
	if err != nil {
		return false, err
	}
	if res.ExitCode != 0 {
		return false, fmt.Errorf("exit %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	return true, nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...(truncated)"
}
