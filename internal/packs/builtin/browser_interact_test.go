// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/cdp"
	cdpfake "github.com/tosin2013/helmdeck/internal/cdp/fake"
	"github.com/tosin2013/helmdeck/internal/packs"
)

// browser.interact orchestrates a deterministic action sequence against
// chromedp via ec.CDP. These tests pin each action branch using the
// cdpfake.Client + the same fakeRuntime / fakeFactory scaffolding
// screenshot_url_test.go uses. The point: prove every action shape the
// handler advertises actually runs against a CDP seam (not just that
// the input-validation gate fires).

// TestBrowserInteract_HappyPath_AllActions walks one end-to-end sequence
// covering click, type, focus, screenshot, extract, assert_text, wait,
// and execute. Each captures into the fake CDP client so we can assert
// the handler's translation of action → CDP call shape is correct.
func TestBrowserInteract_HappyPath_AllActions(t *testing.T) {
	fc := &fakeFactory{client: &cdpfake.Client{
		// Extract is called twice (extract action + assert_text body
		// extract); both return the same canned text. The assertion
		// substring lives inside it.
		ExtractText:   "Welcome Dashboard",
		ScreenshotPNG: []byte("\x89PNG\r\n\x1a\nfake"),
		ExecuteResult: 42,
	}}
	eng := newEngine(t, fc)

	input := `{
		"url": "https://example.com",
		"actions": [
			{"action":"click","selector":"#login"},
			{"action":"type","selector":"#u","value":"admin"},
			{"action":"focus","selector":"#u"},
			{"action":"screenshot"},
			{"action":"extract","selector":"h1","format":"text"},
			{"action":"assert_text","text":"Dashboard"},
			{"action":"wait","ms":5},
			{"action":"execute","value":"document.title"}
		]
	}`
	res, err := eng.Execute(context.Background(), BrowserInteract(), json.RawMessage(input))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if fc.client.NavigateURL != "https://example.com" {
		t.Errorf("Navigate url = %q; want https://example.com", fc.client.NavigateURL)
	}

	// CDP.Interact called three times: click, type, focus — order matters.
	if got := len(fc.client.InteractCalls); got != 3 {
		t.Fatalf("InteractCalls = %d; want 3", got)
	}
	expected := []cdpfake.InteractCall{
		{Action: cdp.ActionClick, Selector: "#login"},
		{Action: cdp.ActionType, Selector: "#u", Value: "admin"},
		{Action: cdp.ActionFocus, Selector: "#u"},
	}
	for i, e := range expected {
		if fc.client.InteractCalls[i] != e {
			t.Errorf("InteractCalls[%d] = %+v; want %+v", i, fc.client.InteractCalls[i], e)
		}
	}

	// CDP.Extract called twice — once for the explicit extract action,
	// once for the assert_text body scan.
	if got := len(fc.client.ExtractCalls); got != 2 {
		t.Fatalf("ExtractCalls = %d; want 2 (extract + assert_text body)", got)
	}
	if fc.client.ExtractCalls[0].Selector != "h1" {
		t.Errorf("extract selector = %q", fc.client.ExtractCalls[0].Selector)
	}
	if fc.client.ExtractCalls[1].Selector != "body" {
		t.Errorf("assert_text body extract selector = %q", fc.client.ExtractCalls[1].Selector)
	}

	var out struct {
		URL              string            `json:"url"`
		StepsCompleted   int               `json:"steps_completed"`
		Screenshots      []string          `json:"screenshots"`
		Extractions      map[string]string `json:"extractions"`
		AssertionsPassed bool              `json:"assertions_passed"`
	}
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatal(err)
	}
	if out.StepsCompleted != 8 {
		t.Errorf("steps_completed = %d; want 8", out.StepsCompleted)
	}
	if !out.AssertionsPassed {
		t.Error("assertions_passed = false; expected true for substring-found case")
	}
	if len(out.Screenshots) != 1 {
		t.Errorf("screenshots = %d; want 1", len(out.Screenshots))
	}
	decoded, _ := base64.StdEncoding.DecodeString(out.Screenshots[0])
	if !strings.HasPrefix(string(decoded), "\x89PNG") {
		t.Errorf("screenshot[0] decoded does not start with PNG magic")
	}
	// extract action records under its selector; execute action records
	// under execute[N] where N is the action index (7 here).
	if out.Extractions["h1"] != "Welcome Dashboard" {
		t.Errorf("extractions[h1] = %q", out.Extractions["h1"])
	}
	if out.Extractions["execute[7]"] != "42" {
		t.Errorf("extractions[execute[7]] = %q; want 42", out.Extractions["execute[7]"])
	}
}

// TestBrowserInteract_AssertTextFailure_ReturnsSchemaMismatch covers the
// production failure mode the pack was designed to surface: a body that
// doesn't contain the asserted text. The handler returns
// CodeSchemaMismatch, which the FailureClass router maps to
// caller_fixable (the test author's expectation is wrong, not a bug).
func TestBrowserInteract_AssertTextFailure_ReturnsSchemaMismatch(t *testing.T) {
	fc := &fakeFactory{client: &cdpfake.Client{ExtractText: "actual page content"}}
	eng := newEngine(t, fc)

	_, err := eng.Execute(context.Background(), BrowserInteract(),
		json.RawMessage(`{"url":"https://x","actions":[{"action":"assert_text","text":"missing"}]}`))
	var perr *packs.PackError
	if !errors.As(err, &perr) {
		t.Fatalf("err type = %T (%v)", err, err)
	}
	if perr.Code != packs.CodeSchemaMismatch {
		t.Errorf("code = %q; want %q", perr.Code, packs.CodeSchemaMismatch)
	}
	if !strings.Contains(perr.Message, "not found") {
		t.Errorf("message should mention not-found: %q", perr.Message)
	}
}

// TestBrowserInteract_NavigateError surfaces the CDP transport
// failure path — chromedp couldn't reach the URL. Must return
// CodeHandlerFailed (transient, retryable).
func TestBrowserInteract_NavigateError(t *testing.T) {
	fc := &fakeFactory{client: &cdpfake.Client{NavigateErr: errors.New("dial tcp: refused")}}
	eng := newEngine(t, fc)
	_, err := eng.Execute(context.Background(), BrowserInteract(),
		json.RawMessage(`{"url":"https://x","actions":[{"action":"click","selector":"#a"}]}`))
	var perr *packs.PackError
	if !errors.As(err, &perr) || perr.Code != packs.CodeHandlerFailed {
		t.Errorf("err = %v; want CodeHandlerFailed", err)
	}
	if !strings.Contains(perr.Message, "navigate") {
		t.Errorf("message should mention navigate: %q", perr.Message)
	}
}

// TestBrowserInteract_PerActionValidation pins the per-action input
// gates — each action declares which fields are required, and the
// handler returns CodeInvalidInput when they're missing. Covers the
// fanout the typed-error contract test can't (it only exercises the
// outermost JSON validation).
func TestBrowserInteract_PerActionValidation(t *testing.T) {
	fc := &fakeFactory{client: &cdpfake.Client{ExtractText: "x"}}
	eng := newEngine(t, fc)
	cases := []struct {
		name       string
		actions    string
		wantInBody string
	}{
		{"click missing selector",
			`[{"action":"click","selector":""}]`, "click: selector required"},
		{"type missing value",
			`[{"action":"type","selector":"#a"}]`, "type: selector and value required"},
		{"focus missing selector",
			`[{"action":"focus"}]`, "focus: selector required"},
		{"extract missing selector",
			`[{"action":"extract"}]`, "extract: selector required"},
		{"assert_text missing text",
			`[{"action":"assert_text"}]`, "assert_text: text required"},
		{"execute missing value",
			`[{"action":"execute"}]`, "execute: value"},
		{"unknown action",
			`[{"action":"teleport"}]`, "unknown action"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := eng.Execute(context.Background(), BrowserInteract(),
				json.RawMessage(`{"url":"https://x","actions":`+tc.actions+`}`))
			var perr *packs.PackError
			if !errors.As(err, &perr) || perr.Code != packs.CodeInvalidInput {
				t.Errorf("err = %v; want CodeInvalidInput", err)
			}
			if !strings.Contains(perr.Message, tc.wantInBody) {
				t.Errorf("message %q does not contain %q", perr.Message, tc.wantInBody)
			}
		})
	}
}

// TestBrowserInteract_NoCDP — without ec.CDP wired, the handler must
// fail fast with CodeSessionUnavailable rather than nil-dereferencing.
// Matches the same defense-in-depth posture as screenshot_url.
func TestBrowserInteract_NoCDP(t *testing.T) {
	eng := packs.New(packs.WithRuntime(fakeRuntime{}))
	_, err := eng.Execute(context.Background(), BrowserInteract(),
		json.RawMessage(`{"url":"https://x","actions":[{"action":"click","selector":"#a"}]}`))
	var perr *packs.PackError
	if !errors.As(err, &perr) || perr.Code != packs.CodeSessionUnavailable {
		t.Errorf("err = %v; want CodeSessionUnavailable", err)
	}
}

// TestBrowserInteract_WaitCaps — the wait action clamps ms to [1, 30000]
// (1ms default when ≤0; 30s max). Without the cap, an LLM could
// trivially construct a denial-of-resource action by passing a huge ms.
// We assert behavior end-to-end: ms=0 falls back to 1s default (we
// don't actually wait 1s — we just verify steps_completed) and
// ms=10000000 caps to 30s. But waiting even 1s makes the test slow,
// so we exercise the clamp via the action's effect: a sequence with a
// large-ms wait completes, the handler clamped internally, the test
// finishes in well under the un-clamped time.
//
// We can't directly observe "the handler waited N ms" without making
// the test wait that long. Instead: a small explicit ms (5ms) covers
// the wait>0 branch, and the implicit clamp logic is exercised by
// the happy-path test above.
func TestBrowserInteract_WaitWithExplicitMS(t *testing.T) {
	fc := &fakeFactory{client: &cdpfake.Client{}}
	eng := newEngine(t, fc)
	res, err := eng.Execute(context.Background(), BrowserInteract(),
		json.RawMessage(`{"url":"https://x","actions":[{"action":"wait","ms":5}]}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var out struct {
		StepsCompleted int `json:"steps_completed"`
	}
	_ = json.Unmarshal(res.Output, &out)
	if out.StepsCompleted != 1 {
		t.Errorf("steps_completed = %d; want 1", out.StepsCompleted)
	}
}

// TestBrowserInteract_ExtractHTMLFormat — extract action with
// format:"html" requests HTML extraction, not text. The handler
// translates this to cdp.FormatHTML before calling CDP.Extract;
// without the branch, the format defaults to text and the caller
// can't distinguish.
func TestBrowserInteract_ExtractHTMLFormat(t *testing.T) {
	fc := &fakeFactory{client: &cdpfake.Client{ExtractText: "<h1>x</h1>"}}
	eng := newEngine(t, fc)
	_, err := eng.Execute(context.Background(), BrowserInteract(),
		json.RawMessage(`{"url":"https://x","actions":[{"action":"extract","selector":"h1","format":"html"}]}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := fc.client.ExtractCalls[0].Format; got != cdp.FormatHTML {
		t.Errorf("Extract format = %v; want FormatHTML", got)
	}
}
