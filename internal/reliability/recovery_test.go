// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

//go:build recovery

package reliability

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// recovery_test.go drives the 5 scenarios against the configured
// model and asserts each scores ≥ threshold (default 7/10). Tests
// skip cleanly when either gate is unset — opt-in via build tag
// (`-tags=recovery`), env var (`HELMDECK_RECOVERY_TESTS=1`), and
// API key (`OPENROUTER_API_KEY`).
//
// Each scenario runs N attempts (10 by default; overridable via
// HELMDECK_RECOVERY_ATTEMPTS so the workflow_dispatch input can
// shorten ad-hoc runs to save free-tier quota). The harness emits a
// per-scenario report to /tmp/recovery-report.json so the workflow
// can upload it as an artifact.

const (
	envEnabled  = "HELMDECK_RECOVERY_TESTS"
	envAPIKey   = "OPENROUTER_API_KEY"
	envModel    = "HELMDECK_RECOVERY_MODEL"
	envAttempts = "HELMDECK_RECOVERY_ATTEMPTS"

	defaultModel    = "moonshotai/kimi-k2.6:free"
	defaultAttempts = 10

	reportPath = "/tmp/recovery-report.json"
)

// scenarioResult is one row in the per-scenario report. Stable JSON
// shape so a future trend dashboard (deferred per the plan) can
// diff successive runs without re-shaping fields.
type scenarioResult struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Threshold   int               `json:"threshold"`
	Attempts    int               `json:"attempts"`
	Successes   int               `json:"successes"`
	Failures    int               `json:"failures"`
	Errors      int               `json:"errors"`
	ActionDist  map[string]int    `json:"action_distribution"`
	SampleFails []sampledResponse `json:"sample_failures,omitempty"`
	Passed      bool              `json:"passed"`
}

// sampledResponse captures up to N "interesting" non-recovery
// responses so a maintainer can audit what the model actually
// emitted when scoring below threshold.
type sampledResponse struct {
	Action string `json:"action"`
	Reason string `json:"reason"`
}

// TestModelRecoveryScenarios is the headline test of the v0.25.0
// reliability arc. PASS ⇒ every scenario hit threshold ⇒ the typed-
// error vocabulary actually carries usable signal to a weak model.
// FAIL on any scenario ⇒ either fix the prompt's vocabulary OR
// document the limit honestly. Either is useful output.
func TestModelRecoveryScenarios(t *testing.T) {
	if os.Getenv(envEnabled) != "1" {
		t.Skipf("%s not set; recovery tests are opt-in (nightly + workflow_dispatch only)", envEnabled)
	}
	apiKey := os.Getenv(envAPIKey)
	if apiKey == "" {
		t.Skipf("%s not set; cannot reach OpenRouter", envAPIKey)
	}

	model := os.Getenv(envModel)
	if model == "" {
		model = defaultModel
	}
	attempts := defaultAttempts
	if v := os.Getenv(envAttempts); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			attempts = n
		}
	}

	client := newOpenRouterClient(apiKey, model)
	scenarios := Scenarios()

	t.Logf("running %d scenarios × %d attempts against %s",
		len(scenarios), attempts, model)

	results := make([]scenarioResult, 0, len(scenarios))
	overallFailed := false

	for _, sc := range scenarios {
		sc := sc
		t.Run(sc.Name, func(t *testing.T) {
			res := runScenario(t, client, sc, attempts)
			results = append(results, res)

			if !res.Passed {
				overallFailed = true
				t.Errorf("FAIL %s: %d/%d (threshold %d). Distribution: %v",
					sc.Name, res.Successes, res.Attempts, sc.Threshold, res.ActionDist)
				if len(res.SampleFails) > 0 {
					t.Logf("sample non-recovery responses:")
					for i, s := range res.SampleFails {
						t.Logf("  %d. action=%q reason=%q", i+1, s.Action, s.Reason)
					}
				}
			} else {
				t.Logf("PASS %s: %d/%d (threshold %d). Distribution: %v",
					sc.Name, res.Successes, res.Attempts, sc.Threshold, res.ActionDist)
			}
		})
	}

	writeReport(t, model, results, overallFailed)
}

// runScenario executes one scenario N times, tallies the response
// actions against the scenario's ExpectedActions, and returns the
// scoring result. Errors (transport, malformed JSON, etc.) count as
// recovery failures — a model that can't emit parseable output for a
// typed-error envelope is failing the reliability contract just as
// much as one that picks the wrong action.
func runScenario(t *testing.T, client *openRouterClient, sc Scenario, attempts int) scenarioResult {
	t.Helper()
	res := scenarioResult{
		Name:        sc.Name,
		Description: sc.Description,
		Threshold:   sc.Threshold,
		Attempts:    attempts,
		ActionDist:  map[string]int{},
	}
	expected := map[string]bool{}
	for _, a := range sc.ExpectedActions {
		expected[a] = true
	}

	for i := 0; i < attempts; i++ {
		// Per-attempt context — 90s ceiling per call (the client's
		// HTTP timeout is 60s; the extra 30s covers connect/DNS).
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		decision, err := client.askForRecovery(ctx, systemPrompt, formatUserPrompt(sc))
		cancel()

		if err != nil {
			res.Errors++
			res.Failures++
			// A truncated error message goes into the per-scenario
			// sample so a maintainer can see whether the failure
			// pattern is transport (retry next nightly) or model
			// (re-prompt or re-pin).
			if len(res.SampleFails) < 3 {
				res.SampleFails = append(res.SampleFails, sampledResponse{
					Action: "<error>",
					Reason: truncate(err.Error(), 200),
				})
			}
			// Inter-attempt sleep on errors. The client already
			// retries 429 internally with 15/30/60/120s backoff;
			// this extra pause covers the case where the *next*
			// attempt is about to fire right after a quota window
			// resets — 5s gives the bucket clean room.
			time.Sleep(5 * time.Second)
			continue
		}

		action := strings.TrimSpace(decision.Action)
		res.ActionDist[action]++
		if expected[action] {
			res.Successes++
		} else {
			res.Failures++
			if len(res.SampleFails) < 3 {
				res.SampleFails = append(res.SampleFails, sampledResponse{
					Action: action,
					Reason: truncate(decision.Reason, 200),
				})
			}
		}

		// Inter-attempt sleep. The bottleneck isn't OpenRouter's
		// account quota (~16 req/min) — it's the upstream provider
		// throttle at Moonshot for kimi-k2.6:free, which doesn't
		// publish a rate but empirically delivers ~1 successful
		// call per 30s. We pace at 12s base and let the client's
		// 15/30/60/120s backoff cover the residual throttles. At
		// 12s base, best case is 50×12s=10min; worst case with
		// every call retrying once is ~17min, comfortably under
		// the 40m workflow timeout.
		time.Sleep(12 * time.Second)
	}

	res.Passed = res.Successes >= sc.Threshold
	return res
}

// formatUserPrompt builds the per-scenario user message. The shape
// mirrors what a real MCP agent would see after a failed tool call —
// system explains the rules, user message carries the envelope.
func formatUserPrompt(sc Scenario) string {
	var b strings.Builder
	if sc.PreviousInput != "" {
		fmt.Fprintf(&b, "You called a tool with input:\n%s\n\n", sc.PreviousInput)
	}
	fmt.Fprintf(&b, "The tool returned this result envelope:\n%s\n\n", sc.ToolResult)
	b.WriteString("What is the right next action? Respond with the JSON object the system prompt described — no surrounding prose.")
	return b.String()
}

// writeReport persists the per-scenario report so the workflow
// uploads it as an artifact. Includes the model id + a timestamp so
// successive nightly runs can be diffed without ambiguity. Failure
// to write is logged but does not fail the test — the in-band test
// output already carries the same information.
func writeReport(t *testing.T, model string, results []scenarioResult, anyFailed bool) {
	t.Helper()
	report := map[string]any{
		"model":      model,
		"timestamp":  time.Now().UTC().Format(time.RFC3339),
		"any_failed": anyFailed,
		"scenarios":  results,
	}
	body, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Logf("marshal report: %v", err)
		return
	}
	if err := os.WriteFile(reportPath, body, 0o644); err != nil {
		t.Logf("write report to %s: %v", reportPath, err)
		return
	}
	t.Logf("recovery report written to %s", reportPath)
}
