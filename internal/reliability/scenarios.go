// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

//go:build recovery

// Package reliability houses the PR H "model recovery loop" tests
// (v0.25.0 reliability arc). The build tag `recovery` plus two env
// vars (HELMDECK_RECOVERY_TESTS=1 and OPENROUTER_API_KEY) keep these
// tests opt-in — a stray `go test ./...` will not call the live API.
//
// What the test proves: helmdeck's typed-error closed-set (ADR 008)
// is supposed to give the LLM enough signal to pick the right
// recovery without re-deriving intent from prose. PR H closes the
// arc by actually driving a weak free-tier model through simulated
// tool-result envelopes and asserting the model's chosen action
// matches what each typed code is designed to elicit.
package reliability

// Scenario is one synthetic tool-result envelope the model is asked
// to react to. Field meanings:
//
//   - Name: short id used in the report.
//   - Description: what the scenario is testing (one sentence; ends
//     up in the per-scenario report).
//   - ToolResult: the JSON envelope the model receives in the user
//     message. Mirrors what an MCP client would surface from
//     packResultAsToolResult / errorToolResult in production.
//   - PreviousInput: optional — if non-empty, the system prompt
//     tells the model "your previous call had input <X>" so it can
//     emit a corrected version.
//   - ExpectedActions: the closed set of acceptable `action` values
//     the model can return for this scenario. A response outside the
//     set counts as a recovery failure for that attempt. Some
//     scenarios accept two actions (e.g. message-only ambiguity
//     accepts either retry_corrected or escalate_to_user).
//   - Threshold: minimum number of correct attempts (out of N=10) to
//     PASS. Default 7; the message-only ambiguity scenario is 6
//     because multiple recoveries are inherently acceptable.
type Scenario struct {
	Name            string
	Description     string
	ToolResult      string
	PreviousInput   string
	ExpectedActions []string
	Threshold       int
}

// recoveryAction is the closed set of decisions the model can emit.
// Mirrors the JSON-schema action values; kept here so the test
// matches exact strings rather than free prose.
const (
	ActionRetryCorrected = "retry_corrected"
	ActionRetryAsIs      = "retry_as_is"
	ActionEscalateToUser = "escalate_to_user"
	ActionReportBug      = "report_bug"
)

// systemPrompt explains helmdeck's typed-error vocabulary to the
// model — the same vocabulary an MCP-connected agent would learn
// from helmdeck://routing-guide. The prompt is deliberately compact
// (a frontier model could infer the rules from message text alone;
// the test is whether a weak model can read the code field). Updates
// to this prompt invalidate the scoring baseline — bump the
// MODEL_LAST_VERIFIED date in the workflow when changing it.
const systemPrompt = `You are an agent calling tools through helmdeck.
Tool results may carry typed error codes. Pick the right next action
based on the code, NOT just the message text.

Closed-set error codes (ADR 008):
  invalid_input        — caller's input was wrong. Fix the input and retry.
  invalid_output       — pack handler emitted output that violates its
                         declared schema. Pack bug. Do not retry; report.
  schema_mismatch      — alias for invalid_output in some envelopes; same recovery.
  handler_failed       — transient or unknown handler-side failure.
                         Retry with the same inputs; if it persists,
                         report as bug. Default to retry first.
  session_unavailable  — backend session unreachable. Transient; retry.
  credential_invalid   — the credential the pack used is expired/wrong.
                         Do NOT auto-retry (could lock the account).
                         Ask the user to refresh the credential.
  timeout              — request exceeded its budget. Retry once.
  artifact_failed      — artifact store unreachable. Transient; retry.
  resource_exhausted   — pack hit OOM or quota. Reduce scope and retry.
  internal             — unclassified failure. Report as bug.

You must respond with valid JSON matching this schema:
  {
    "action": "retry_corrected" | "retry_as_is" | "escalate_to_user" | "report_bug",
    "reason": "<one-sentence justification>",
    "corrected_input": "<JSON object if action is retry_corrected, else omit>"
  }

Action meanings:
  retry_corrected  — caller's fault; emit a fixed input
  retry_as_is      — transient or backend issue; same input, retry
  escalate_to_user — user has to act (refresh credential, change inputs)
  report_bug       — pack-side bug; do not retry, surface to maintainers

Return ONLY the JSON object. No prose around it.`

// Scenarios returns the 5 recovery scenarios PR H runs. New scenarios
// append to the end so existing report keys (Name) stay stable across
// releases — the workflow report's diff against prior nights would
// otherwise re-shuffle every time.
func Scenarios() []Scenario {
	return []Scenario{
		{
			Name:        "invalid_input_named_field",
			Description: "Caller-fixable: named field has a closed set of valid values.",
			ToolResult: `{
                "isError": true,
                "error": "invalid_input",
                "message": "field 'persona': must be one of [technical, marketing, executive]"
            }`,
			PreviousInput:   `{"persona": "developer", "topic": "AI agents"}`,
			ExpectedActions: []string{ActionRetryCorrected},
			Threshold:       7,
		},
		{
			Name:        "invalid_output_pack_bug",
			Description: "Pack-side bug: handler emitted output violating its declared schema. v0.17.1-class regression.",
			ToolResult: `{
                "isError": true,
                "error": "invalid_output",
                "message": "field 'tts_chars': expected number, got object"
            }`,
			PreviousInput:   `{"script": "Hello world."}`,
			ExpectedActions: []string{ActionReportBug},
			Threshold:       7,
		},
		{
			Name:        "handler_failed_transient",
			Description: "Transient handler-side failure (network reset). Retry with same input.",
			ToolResult: `{
                "isError": true,
                "error": "handler_failed",
                "message": "connection reset by peer"
            }`,
			PreviousInput:   `{"url": "https://example.com/page"}`,
			ExpectedActions: []string{ActionRetryAsIs},
			Threshold:       7,
		},
		{
			Name:        "credential_invalid_escalate",
			Description: "Credential expired. Auto-retry could lock the user's account; must escalate.",
			ToolResult: `{
                "isError": true,
                "error": "credential_invalid",
                "message": "GitHub token expired"
            }`,
			PreviousInput:   `{"repo": "octocat/Hello-World", "title": "Test issue"}`,
			ExpectedActions: []string{ActionEscalateToUser},
			Threshold:       7,
		},
		{
			Name:        "message_only_ambiguity",
			Description: "Vague message; only the code carries actionable signal. Tests whether the model reads the code or pattern-matches the message.",
			ToolResult: `{
                "isError": true,
                "error": "invalid_input",
                "message": "we couldn't process your request"
            }`,
			PreviousInput: `{"query": "anything"}`,
			// Either is acceptable: a confident model corrects;
			// a cautious model escalates. The fail case is retry_as_is
			// (would mean the model ignored the typed code and inferred
			// "transient" from the vague message).
			ExpectedActions: []string{ActionRetryCorrected, ActionEscalateToUser},
			// Lower threshold — inherently more variance.
			Threshold: 6,
		},
	}
}
