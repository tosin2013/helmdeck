// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// browser_interact.go (T621) — deterministic multi-step browser
// automation. The user supplies an ordered array of actions and the
// pack executes them sequentially against a session's browser via
// chromedp. No LLM needed — the user (or an LLM calling this pack)
// specifies exactly what to do.
//
// This is the building block for the AI-powered `web.test` pack
// (T807e, Phase 7) which will decompose natural-language test
// instructions into browser.interact action sequences via Playwright
// MCP's accessibility tree.
//
// Input shape:
//
//	{
//	  "url": "https://example.com",
//	  "actions": [
//	    {"action": "click", "selector": "#login-btn"},
//	    {"action": "type", "selector": "#username", "value": "admin"},
//	    {"action": "type", "selector": "#password", "value": "secret"},
//	    {"action": "click", "selector": "#submit"},
//	    {"action": "wait", "ms": 2000},
//	    {"action": "screenshot"},
//	    {"action": "assert_text", "text": "Dashboard"},
//	    {"action": "extract", "selector": "h1", "format": "text"}
//	  ]
//	}
//
// Output shape:
//
//	{
//	  "url": "https://example.com",
//	  "steps_completed": 8,
//	  "screenshots": ["<base64>", ...],
//	  "extractions": {"h1": "Dashboard"},
//	  "assertions_passed": true
//	}

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tosin2013/helmdeck/internal/cdp"
	"github.com/tosin2013/helmdeck/internal/packs"
)

func BrowserInteract() *packs.Pack {
	return &packs.Pack{
		Name:         "browser.interact",
		Version:      "v1",
		Description: "Execute a deterministic sequence of browser actions (navigate/click/type/screenshot/extract/assert/wait) against a HEADLESS Chromium via CDP. " +
			"Not visible on the desktop — operators watching a session via noVNC will see nothing when this pack runs. " +
			"Use this when speed + determinism matter and nobody is watching. " +
			"When the user is watching, or the task is 'drive a browser so I can see it', use the desktop.* REST primitives instead (screenshot/click/type/key/scroll) against a desktop-mode session where Chromium is already pre-launched on the XFCE4 display.",
		NeedsSession: true,
		InputSchema: packs.BasicSchema{
			Required: []string{"url", "actions"},
			Properties: map[string]string{
				"url":     "string",
				"actions": "array",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"steps_completed"},
			Properties: map[string]string{
				"url":               "string",
				"steps_completed":   "number",
				"screenshots":       "array",
				"extractions":       "object",
				"assertions_passed": "boolean",
			},
		},
		Handler: browserInteractHandler,
	}
}

type browserAction struct {
	Action   string `json:"action"`
	Selector string `json:"selector,omitempty"`
	Value    string `json:"value,omitempty"`
	Text     string `json:"text,omitempty"`
	Format   string `json:"format,omitempty"`
	MS       int    `json:"ms,omitempty"`
}

type browserInteractInput struct {
	URL     string          `json:"url"`
	Actions []browserAction `json:"actions"`
}

func browserInteractHandler(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
	var in browserInteractInput
	if err := json.Unmarshal(ec.Input, &in); err != nil {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error()}
	}
	if strings.TrimSpace(in.URL) == "" {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "url is required"}
	}
	if len(in.Actions) == 0 {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "actions array must not be empty"}
	}
	if ec.CDP == nil {
		return nil, &packs.PackError{Code: packs.CodeSessionUnavailable, Message: "no CDP client available"}
	}

	// Navigate to the URL first.
	if err := ec.CDP.Navigate(ctx, in.URL); err != nil {
		return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("navigate to %s: %v", in.URL, err)}
	}

	// Initialize as empty slices/maps so JSON marshals them as `[]` /
	// `{}` rather than `null` when no screenshot/extract/execute
	// action ran — the OutputSchema declares them as array/object,
	// and `null` violates the array type check in Engine.Execute.
	screenshots := []string{}
	extractions := make(map[string]string)
	assertionsPassed := true
	stepsCompleted := 0

	for i, act := range in.Actions {
		switch act.Action {
		case "click":
			if act.Selector == "" {
				return nil, &packs.PackError{Code: packs.CodeInvalidInput,
					Message: fmt.Sprintf("action[%d] click: selector required", i)}
			}
			if err := ec.CDP.Interact(ctx, cdp.ActionClick, act.Selector, ""); err != nil {
				return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
					Message: fmt.Sprintf("action[%d] click %q: %v", i, act.Selector, err)}
			}

		case "type":
			if act.Selector == "" || act.Value == "" {
				return nil, &packs.PackError{Code: packs.CodeInvalidInput,
					Message: fmt.Sprintf("action[%d] type: selector and value required", i)}
			}
			if err := ec.CDP.Interact(ctx, cdp.ActionType, act.Selector, act.Value); err != nil {
				return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
					Message: fmt.Sprintf("action[%d] type %q: %v", i, act.Selector, err)}
			}

		case "focus":
			if act.Selector == "" {
				return nil, &packs.PackError{Code: packs.CodeInvalidInput,
					Message: fmt.Sprintf("action[%d] focus: selector required", i)}
			}
			if err := ec.CDP.Interact(ctx, cdp.ActionFocus, act.Selector, ""); err != nil {
				return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
					Message: fmt.Sprintf("action[%d] focus %q: %v", i, act.Selector, err)}
			}

		case "screenshot":
			png, err := ec.CDP.Screenshot(ctx, true)
			if err != nil {
				return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
					Message: fmt.Sprintf("action[%d] screenshot: %v", i, err)}
			}
			screenshots = append(screenshots, base64.StdEncoding.EncodeToString(png))

		case "extract":
			if act.Selector == "" {
				return nil, &packs.PackError{Code: packs.CodeInvalidInput,
					Message: fmt.Sprintf("action[%d] extract: selector required", i)}
			}
			format := cdp.FormatText
			if act.Format == "html" {
				format = cdp.FormatHTML
			}
			text, err := ec.CDP.Extract(ctx, act.Selector, format)
			if err != nil {
				return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
					Message: fmt.Sprintf("action[%d] extract %q: %v", i, act.Selector, err)}
			}
			extractions[act.Selector] = text

		case "assert_text":
			if act.Text == "" {
				return nil, &packs.PackError{Code: packs.CodeInvalidInput,
					Message: fmt.Sprintf("action[%d] assert_text: text required", i)}
			}
			// Extract all visible text from body and check for substring.
			bodyText, err := ec.CDP.Extract(ctx, "body", cdp.FormatText)
			if err != nil {
				return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
					Message: fmt.Sprintf("action[%d] assert_text extract: %v", i, err)}
			}
			if !strings.Contains(bodyText, act.Text) {
				assertionsPassed = false
				return nil, &packs.PackError{Code: packs.CodeSchemaMismatch,
					Message: fmt.Sprintf("action[%d] assert_text failed: %q not found in page", i, act.Text)}
			}

		case "wait":
			ms := act.MS
			if ms <= 0 {
				ms = 1000
			}
			if ms > 30000 {
				ms = 30000
			}
			time.Sleep(time.Duration(ms) * time.Millisecond)

		case "execute":
			// Run arbitrary JavaScript in the page context.
			if act.Value == "" {
				return nil, &packs.PackError{Code: packs.CodeInvalidInput,
					Message: fmt.Sprintf("action[%d] execute: value (JavaScript) required", i)}
			}
			result, err := ec.CDP.Execute(ctx, act.Value)
			if err != nil {
				return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
					Message: fmt.Sprintf("action[%d] execute: %v", i, err)}
			}
			if result != nil {
				extractions[fmt.Sprintf("execute[%d]", i)] = fmt.Sprintf("%v", result)
			}

		default:
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: fmt.Sprintf("action[%d]: unknown action %q (supported: click, type, focus, screenshot, extract, assert_text, wait, execute)", i, act.Action)}
		}

		stepsCompleted++
	}

	return json.Marshal(map[string]any{
		"url":               in.URL,
		"steps_completed":   stepsCompleted,
		"screenshots":       screenshots,
		"extractions":       extractions,
		"assertions_passed": assertionsPassed,
	})
}
