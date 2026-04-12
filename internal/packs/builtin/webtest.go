// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// web_test.go (T807e, ADR 035) — natural-language browser testing
// via Playwright MCP's accessibility tree.
//
// The operator (or, more typically, an agent) hands in a URL plus a
// plain-English instruction — "log in as alice, open Settings, make
// sure 2FA is enabled" — and the pack drives Playwright MCP through
// it, using the gateway LLM to decompose the instruction into the
// next tool call at each step. The MCP client fires `browser_*`
// tools against the sidecar-bundled @playwright/mcp child (T807a),
// which is attached to the same Chromium process the rest of the
// browser packs use.
//
// Why Playwright MCP and not chromedp?
//   - Accessibility-tree snapshots are deterministic — no vision
//     model required to locate elements, which matters for the weak
//     models ADR 003 targets.
//   - Playwright MCP returns structured refs (`e4`, `e12`) that the
//     LLM addresses directly, so the plan-step format is tiny and
//     easy for small models to produce correctly.
//   - The `browser.interact` pack (T621) remains the deterministic,
//     LLM-free option when the caller already knows the selectors.
//     This pack is the NL front door.
//
// Flow:
//
//	1. Read PlaywrightMCPEndpoint from the session (populated by
//	   T807a). Refuse fast if the sidecar was built without it.
//	2. Run the target URL through the egress guard so a crafted
//	   instruction can't pivot the sidecar to cloud metadata.
//	3. Initialize the Playwright MCP session; navigate; snapshot.
//	4. For up to max_steps: ask the gateway LLM for the next step
//	   given (instruction, current snapshot, step trace). Parse the
//	   JSON response into a Plan. Execute it via pwmcp. Re-snapshot.
//	5. Terminate on `done`, `fail`, or max_steps. Optionally verify
//	   a caller-supplied `assertions` list against the final
//	   snapshot text (substring match, case-sensitive).
//
// This pack is NeedsSession=true and NOT PreserveSession — each
// web.test run gets a fresh session so cookies from a prior test
// don't leak into the next. Callers who need session pinning should
// use browser.interact (T621), which the authoring agent can drive
// with selectors it discovered from a preceding web.test run.
//
// Input shape:
//
//	{
//	  "url":         "https://app.example.com/login",
//	  "instruction": "log in as alice with password hunter2 and confirm the dashboard loads",
//	  "model":       "openai/gpt-4o",
//	  "max_steps":   8,                               // optional, default 8
//	  "assertions":  ["Welcome, alice", "Dashboard"]  // optional
//	}
//
// Output shape:
//
//	{
//	  "url":               "...",
//	  "completed":         true,
//	  "steps":             [ { "tool": "...", "arguments": {...}, "result": "...", "reasoning": "..." }, ... ],
//	  "steps_used":        5,
//	  "final_snapshot":    "<accessibility tree dump>",
//	  "assertions_passed": true,
//	  "reason":            "model emitted done"
//	}

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tosin2013/helmdeck/internal/gateway"
	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/pwmcp"
	"github.com/tosin2013/helmdeck/internal/security"
	"github.com/tosin2013/helmdeck/internal/vision"
)

const (
	defaultWebTestMaxSteps  = 8
	defaultWebTestMaxTokens = 768

	// webTestSystemPrompt is frozen identical to the system message
	// every LLM call uses. Keeping it centralised means the parser
	// and the model see exactly the same schema — drift between the
	// two is the most common "my pack worked yesterday" failure mode.
	webTestSystemPrompt = `You are driving a browser through Playwright MCP to accomplish a user's testing goal.

Each turn you will receive:
  - GOAL: a natural-language instruction describing what the user wants to verify.
  - SNAPSHOT: an accessibility-tree dump of the CURRENT page. Elements are tagged with [ref=eN] identifiers. Use those refs to address elements in your tool calls.
  - HISTORY: a compact log of the tool calls you have already made this run.

You MUST respond with ONE JSON object and nothing else. Do not wrap it in markdown. The schema:

{
  "tool":      "browser_navigate" | "browser_click" | "browser_type" | "browser_wait_for" | "browser_snapshot" | "done" | "fail",
  "arguments": { ...tool-specific... },
  "reasoning": "<one sentence explaining the choice>"
}

Tool arguments:
  - browser_navigate: {"url": "<absolute url>"}
  - browser_click:    {"element": "<human-readable description>", "ref": "e12"}
  - browser_type:     {"element": "<description>", "ref": "e4", "text": "<literal text>", "submit": true|false}
  - browser_wait_for: {"text": "<literal text to wait for>"} or {"time": <seconds>}
  - browser_snapshot: {}
  - done:             {} — use when the goal is achieved
  - fail:             {"reason": "<why the goal cannot be achieved>"}

Rules:
  - Prefer refs from the LATEST snapshot. Refs from earlier snapshots are stale.
  - Issue ONE tool per turn. Do not batch.
  - After a navigation or click, the next turn's SNAPSHOT will reflect the new page — take it into account.
  - Emit "done" as soon as the goal is satisfied; do not keep clicking.
  - Emit "fail" with a reason if the goal is impossible (element missing, login rejected).`
)

// WebTest constructs the pack. The dispatcher and egress guard are
// passed in the same way T407/T408 wire the vision packs, so
// cmd/control-plane/main.go can register it conditionally when a
// gateway is configured and skip it otherwise.
//
// newClient is a seam for unit tests to swap in a stubbed MCP
// client. Production call sites pass nil; the handler then builds a
// real pwmcp.Client from the session endpoint.
func WebTest(d vision.Dispatcher, eg *security.EgressGuard) *packs.Pack {
	return WebTestWithClientFactory(d, eg, nil)
}

// WebTestWithClientFactory is the testing-friendly constructor.
// newClient defaults to pwmcp.New when nil.
func WebTestWithClientFactory(d vision.Dispatcher, eg *security.EgressGuard, newClient func(endpoint string) PlaywrightMCPClient) *packs.Pack {
	if newClient == nil {
		newClient = func(endpoint string) PlaywrightMCPClient {
			return pwmcp.New(endpoint, nil)
		}
	}
	return &packs.Pack{
		Name:         "web.test",
		Version:      "v1",
		Description:  "Drive a natural-language browser test against a URL via Playwright MCP accessibility tree.",
		NeedsSession: true,
		InputSchema: packs.BasicSchema{
			Required: []string{"url", "instruction", "model"},
			Properties: map[string]string{
				"url":         "string",
				"instruction": "string",
				"model":       "string",
				"max_steps":   "number",
				"assertions":  "array",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"url", "completed", "steps_used"},
			Properties: map[string]string{
				"url":               "string",
				"completed":         "boolean",
				"steps":             "array",
				"steps_used":        "number",
				"final_snapshot":    "string",
				"assertions_passed": "boolean",
				"reason":            "string",
			},
		},
		Handler: webTestHandler(d, eg, newClient),
	}
}

// PlaywrightMCPClient is the narrow surface web.test's handler
// consumes. The real type is *pwmcp.Client; tests inject a stub.
type PlaywrightMCPClient interface {
	Initialize(ctx context.Context) error
	ToolsCall(ctx context.Context, tool string, arguments map[string]any) (*pwmcp.ToolResult, error)
}

type webTestInput struct {
	URL         string   `json:"url"`
	Instruction string   `json:"instruction"`
	Model       string   `json:"model"`
	MaxSteps    int      `json:"max_steps"`
	Assertions  []string `json:"assertions"`
}

type webTestStep struct {
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments,omitempty"`
	Result    string         `json:"result,omitempty"`
	IsError   bool           `json:"is_error,omitempty"`
	Reasoning string         `json:"reasoning,omitempty"`
}

// plan is what the LLM returns each turn.
type plan struct {
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments,omitempty"`
	Reasoning string         `json:"reasoning,omitempty"`
}

func webTestHandler(d vision.Dispatcher, eg *security.EgressGuard, newClient func(string) PlaywrightMCPClient) packs.HandlerFunc {
	return func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
		if d == nil {
			return nil, &packs.PackError{
				Code:    packs.CodeInternal,
				Message: "web.test registered without a gateway dispatcher",
			}
		}

		var in webTestInput
		if err := json.Unmarshal(ec.Input, &in); err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
		}
		if strings.TrimSpace(in.URL) == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "url is required"}
		}
		if strings.TrimSpace(in.Instruction) == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "instruction is required"}
		}
		if strings.TrimSpace(in.Model) == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "model is required (provider/model)"}
		}
		maxSteps := in.MaxSteps
		if maxSteps <= 0 {
			maxSteps = defaultWebTestMaxSteps
		}

		// Egress guard on the target URL. Playwright MCP is on the
		// private sidecar interface; without this guard an instruction
		// could coerce the sidecar into hitting 169.254.169.254 and
		// leaking metadata through the snapshot text.
		if eg != nil {
			if err := eg.CheckURL(ctx, in.URL); err != nil {
				return nil, &packs.PackError{
					Code:    packs.CodeInvalidInput,
					Message: fmt.Sprintf("egress denied: %v", err),
					Cause:   err,
				}
			}
		}

		if ec.Session == nil {
			return nil, &packs.PackError{Code: packs.CodeSessionUnavailable, Message: "web.test requires a session"}
		}
		endpoint := ec.Session.PlaywrightMCPEndpoint
		if endpoint == "" {
			return nil, &packs.PackError{
				Code: packs.CodeSessionUnavailable,
				Message: "session has no Playwright MCP endpoint; " +
					"rebuild the sidecar image with T807a or set HELMDECK_PLAYWRIGHT_MCP_ENABLED=true",
			}
		}

		client := newClient(endpoint)
		// Playwright MCP starts AFTER Chromium inside the sidecar
		// entrypoint — there's a 2-5 second window where the port
		// isn't listening yet. Retry Initialize with backoff so a
		// fresh session doesn't race against the MCP startup.
		var initErr error
		for attempt := 0; attempt < 10; attempt++ {
			initErr = client.Initialize(ctx)
			if initErr == nil {
				break
			}
			if ctx.Err() != nil {
				break
			}
			time.Sleep(time.Duration(500+attempt*500) * time.Millisecond)
		}
		if initErr != nil {
			return nil, &packs.PackError{
				Code:    packs.CodeHandlerFailed,
				Message: fmt.Sprintf("playwright mcp initialize (after retries): %v", initErr),
				Cause:   initErr,
			}
		}

		// Seed the run with navigate → snapshot so the LLM's first
		// turn already sees the target page. Without this the model
		// has to spend a turn on `browser_navigate`, which wastes a
		// max_steps budget on deterministic work.
		steps := make([]webTestStep, 0, maxSteps+2)
		navRes, err := client.ToolsCall(ctx, "browser_navigate", map[string]any{"url": in.URL})
		if err != nil {
			return nil, &packs.PackError{
				Code:    packs.CodeHandlerFailed,
				Message: fmt.Sprintf("browser_navigate %s: %v", in.URL, err),
				Cause:   err,
			}
		}
		steps = append(steps, webTestStep{
			Tool:      "browser_navigate",
			Arguments: map[string]any{"url": in.URL},
			Result:    navRes.Text,
			IsError:   navRes.IsError,
			Reasoning: "seed navigation",
		})

		snapshot, err := takeSnapshot(ctx, client)
		if err != nil {
			return nil, &packs.PackError{
				Code:    packs.CodeHandlerFailed,
				Message: fmt.Sprintf("initial browser_snapshot: %v", err),
				Cause:   err,
			}
		}
		steps = append(steps, webTestStep{Tool: "browser_snapshot", Result: snapshot, Reasoning: "seed snapshot"})

		// Plan loop. Each turn asks the model for ONE tool call,
		// executes it, and re-snapshots so the next turn sees the
		// updated page. done/fail exit immediately.
		completed := false
		exitReason := fmt.Sprintf("max_steps (%d) reached without done", maxSteps)
	loop:
		for step := 0; step < maxSteps; step++ {
			p, rawModel, err := askPlan(ctx, d, in.Model, in.Instruction, snapshot, steps)
			if err != nil {
				// Model-level errors are recorded in the trace and
				// surfaced as handler_failed so the caller sees what
				// happened on the LLM side.
				steps = append(steps, webTestStep{Tool: "model_error", Result: rawModel, Reasoning: err.Error()})
				return nil, &packs.PackError{
					Code:    packs.CodeHandlerFailed,
					Message: fmt.Sprintf("plan step %d: %v", step+1, err),
					Cause:   err,
				}
			}

			switch p.Tool {
			case "done":
				steps = append(steps, webTestStep{Tool: "done", Reasoning: p.Reasoning})
				completed = true
				exitReason = "model emitted done"
				break loop
			case "fail":
				reason, _ := p.Arguments["reason"].(string)
				steps = append(steps, webTestStep{Tool: "fail", Arguments: p.Arguments, Reasoning: p.Reasoning})
				exitReason = "model emitted fail"
				if reason != "" {
					exitReason = "model emitted fail: " + reason
				}
				break loop
			case "browser_navigate", "browser_click", "browser_type", "browser_wait_for", "browser_snapshot":
				// Guard navigate targets through the egress guard so
				// the model cannot pivot mid-test to a metadata IP.
				if p.Tool == "browser_navigate" && eg != nil {
					if navURL, _ := p.Arguments["url"].(string); navURL != "" {
						if err := eg.CheckURL(ctx, navURL); err != nil {
							steps = append(steps, webTestStep{
								Tool: "browser_navigate", Arguments: p.Arguments, IsError: true,
								Result:    fmt.Sprintf("egress denied: %v", err),
								Reasoning: p.Reasoning,
							})
							exitReason = "egress guard blocked mid-test navigation"
							break loop
						}
					}
				}

				res, err := client.ToolsCall(ctx, p.Tool, p.Arguments)
				if err != nil {
					steps = append(steps, webTestStep{
						Tool: p.Tool, Arguments: p.Arguments,
						IsError: true, Result: err.Error(), Reasoning: p.Reasoning,
					})
					return nil, &packs.PackError{
						Code:    packs.CodeHandlerFailed,
						Message: fmt.Sprintf("%s (step %d): %v", p.Tool, step+1, err),
						Cause:   err,
					}
				}
				steps = append(steps, webTestStep{
					Tool: p.Tool, Arguments: p.Arguments,
					Result: res.Text, IsError: res.IsError, Reasoning: p.Reasoning,
				})
				// Refresh the snapshot after any navigation/click/type
				// so the next turn sees the post-action state.
				// browser_snapshot updates the snapshot from its own
				// result directly.
				if p.Tool == "browser_snapshot" {
					snapshot = res.Text
				} else {
					snap, err := takeSnapshot(ctx, client)
					if err != nil {
						// Non-fatal — we still log the last successful
						// action — but a snapshot failure usually means
						// the page crashed the browser, so surface it.
						return nil, &packs.PackError{
							Code:    packs.CodeHandlerFailed,
							Message: fmt.Sprintf("post-action snapshot: %v", err),
							Cause:   err,
						}
					}
					snapshot = snap
				}
			default:
				steps = append(steps, webTestStep{
					Tool: p.Tool, Arguments: p.Arguments, IsError: true,
					Result:    "unknown tool",
					Reasoning: p.Reasoning,
				})
				return nil, &packs.PackError{
					Code:    packs.CodeHandlerFailed,
					Message: fmt.Sprintf("plan step %d: unknown tool %q", step+1, p.Tool),
				}
			}
		}

		// Assertion pass (optional). Substring match against the final
		// snapshot — cheap, deterministic, no second LLM round-trip.
		assertionsPassed := true
		if len(in.Assertions) > 0 {
			for _, a := range in.Assertions {
				if a == "" {
					continue
				}
				if !strings.Contains(snapshot, a) {
					assertionsPassed = false
					break
				}
			}
		}

		out := map[string]any{
			"url":               in.URL,
			"completed":         completed && assertionsPassed,
			"steps":             steps,
			"steps_used":        len(steps),
			"final_snapshot":    snapshot,
			"assertions_passed": assertionsPassed,
			"reason":            exitReason,
		}
		return json.Marshal(out)
	}
}

// takeSnapshot is a thin convenience wrapper around ToolsCall so the
// handler reads top-to-bottom without every call site dealing with
// the map[string]any dance.
func takeSnapshot(ctx context.Context, c PlaywrightMCPClient) (string, error) {
	res, err := c.ToolsCall(ctx, "browser_snapshot", nil)
	if err != nil {
		return "", err
	}
	if res.IsError {
		return "", fmt.Errorf("browser_snapshot returned is_error: %s", res.Text)
	}
	return res.Text, nil
}

// askPlan calls the gateway LLM with the frozen system prompt plus
// (goal, snapshot, history) and parses the response into a Plan.
// Tolerant JSON extraction mirrors vision.ParseAction so weak models
// that prefix a sentence of prose still parse cleanly.
func askPlan(ctx context.Context, d vision.Dispatcher, model, instruction, snapshot string, steps []webTestStep) (plan, string, error) {
	maxTokens := defaultWebTestMaxTokens
	history := summarizeSteps(steps)
	userMsg := fmt.Sprintf("GOAL: %s\n\nSNAPSHOT:\n%s\n\nHISTORY:\n%s\n\nWhat is the next tool call?",
		instruction, snapshot, history)
	req := gateway.ChatRequest{
		Model:     model,
		MaxTokens: &maxTokens,
		Messages: []gateway.Message{
			{Role: "system", Content: gateway.TextContent(webTestSystemPrompt)},
			{Role: "user", Content: gateway.TextContent(userMsg)},
		},
	}
	resp, err := d.Dispatch(ctx, req)
	if err != nil {
		return plan{}, "", fmt.Errorf("dispatch: %w", err)
	}
	if len(resp.Choices) == 0 {
		return plan{}, "", errors.New("model returned no choices")
	}
	raw := strings.TrimSpace(resp.Choices[0].Message.Content.Text())
	p, err := parsePlan(raw)
	if err != nil {
		return plan{}, raw, err
	}
	return p, raw, nil
}

// summarizeSteps produces a compact one-line-per-step transcript for
// the HISTORY section of the prompt. Full snapshot text is redacted
// because the model gets the latest snapshot separately — including
// every historical snapshot would explode token usage for no gain.
func summarizeSteps(steps []webTestStep) string {
	if len(steps) == 0 {
		return "(none)"
	}
	var b strings.Builder
	for i, s := range steps {
		if s.Tool == "browser_snapshot" {
			fmt.Fprintf(&b, "%d. %s → (snapshot)\n", i+1, s.Tool)
			continue
		}
		args, _ := json.Marshal(s.Arguments)
		result := s.Result
		if len(result) > 160 {
			result = result[:160] + "…"
		}
		status := "ok"
		if s.IsError {
			status = "is_error"
		}
		fmt.Fprintf(&b, "%d. %s%s → %s: %s\n", i+1, s.Tool, args, status, result)
	}
	return b.String()
}

// parsePlan decodes a plan JSON object from a model response. Strict
// unmarshal first; on failure extract the first balanced {...} block
// and retry. Empty tool → error so downstream doesn't crash on a
// blank response.
func parsePlan(raw string) (plan, error) {
	var p plan
	if err := json.Unmarshal([]byte(raw), &p); err == nil && p.Tool != "" {
		return p, nil
	}
	if obj := extractFirstJSONObject(raw); obj != "" {
		if err := json.Unmarshal([]byte(obj), &p); err == nil && p.Tool != "" {
			return p, nil
		}
	}
	snippet := raw
	if len(snippet) > 256 {
		snippet = snippet[:256] + "…"
	}
	return plan{}, fmt.Errorf("no parseable plan JSON in model response: %s", snippet)
}

// extractFirstJSONObject returns the first balanced {…} substring, or
// "" if none. Handles the common case of a model wrapping JSON in
// prose or markdown code fences.
func extractFirstJSONObject(s string) string {
	depth := 0
	start := -1
	inString := false
	escape := false
	for i, r := range s {
		if inString {
			if escape {
				escape = false
				continue
			}
			if r == '\\' {
				escape = true
				continue
			}
			if r == '"' {
				inString = false
			}
			continue
		}
		switch r {
		case '"':
			inString = true
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			if depth == 0 {
				continue
			}
			depth--
			if depth == 0 && start != -1 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

