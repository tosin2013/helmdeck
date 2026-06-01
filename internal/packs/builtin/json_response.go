// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// json_response.go — defensive JSON parsing for LLM-backed packs
// (ADR 051 PR #1).
//
// Today every LLM-backed pack (plan, route, content_ground, webtest,
// blog.rewrite_for_audience, slides.outline, …) has slightly
// different code for "the model returned text, parse it as JSON,
// recover from common prose-wrap / markdown-fence / trailing-garbage
// quirks." The patterns are equivalent in intent; the implementations
// drift. ADR 050 PR #4's json.Decoder + substring-extraction fallback
// lives only in plan.go; route.go is still on strict json.Unmarshal;
// content_ground.go has its own substring fallback.
//
// DecodeStructuredResponse is the consolidated path. It applies, in
// order:
//
//   1. StripReasoningTokens   — drop <think>/<reasoning>/[REASONING] blocks
//                                that hybrid models (Claude 3.7 Sonnet
//                                thinking, o3-mini, DeepSeek V4 Pro,
//                                Kimi K2 series) emit BEFORE their JSON
//   2. TrimSpace               — leading/trailing whitespace
//   3. unwrapCodeFence         — strip ```json…``` wrappers (existing helper)
//   4. json.Decoder.Decode     — reads one complete JSON value and stops,
//                                tolerates trailing prose/HTML/markdown that
//                                weak models sometimes emit after the
//                                structured payload
//   5. extractFirstJSONObject  — balanced-brace substring extraction
//                                (handles `}` inside JSON string literals;
//                                better than naive LastIndex). Falls back
//                                to json.Unmarshal on the extracted slice
//
// Returns a typed *packs.PackError so callers plug the error directly
// into their handler return. Empty input is treated as a distinct
// failure with the message string callers were using before
// consolidation, so existing string-equal error matching (in tests
// and downstream tooling) keeps working.

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/tosin2013/helmdeck/internal/llmcontext"
	"github.com/tosin2013/helmdeck/internal/packs"
)

// Cause sentinels — set as the Cause of the returned *packs.PackError so
// callers can `errors.Is(perr.Cause, ErrSafetyFiltered)` to bucket
// telemetry, choose retry strategy, or surface user-facing messages
// per cause. ADR 051 PR #2 introduces these.
//
// The taxonomy follows the research synthesis cited in ADR 051: empty
// HTTP-200 completions are NOT a single failure mode, they have four
// distinct root causes with four different correct responses (retry
// with shorter prompt, fall back to a different model, escalate to the
// operator, or shorten the user input). Calling code that's content
// to surface a generic message stays generic; code that wants to act
// on the cause uses errors.Is.
var (
	// ErrSafetyFiltered — provider safety system redacted the
	// output. HTTP 200, zero content, finish_reason="content_filter".
	// Right response: surface a user-facing "your prompt was
	// flagged" message rather than silently retrying.
	ErrSafetyFiltered = errors.New("response redacted by provider safety filter")

	// ErrLengthTruncated — output cut off mid-generation by the
	// max_tokens limit. HTTP 200, content present but unparseable,
	// finish_reason="length". Right response: re-run with a higher
	// output budget OR a tighter prompt.
	ErrLengthTruncated = errors.New("response truncated by length limit")

	// ErrConstrainedDeadlock — model's structural output collapsed
	// (often visible as JSON-shaped but unparseable garbage). The
	// report describes this as the failure mode of quantized open-
	// weight models running under strict-JSON decoding. Right
	// response: retry without strict-JSON mode if it was on, or fall
	// back to a different model class.
	ErrConstrainedDeadlock = errors.New("constrained JSON decoding deadlocked")

	// ErrLikelyTimeout — HTTP 200, zero content, no finish_reason at
	// all. The streaming disconnect case: model was emitting hidden
	// reasoning tokens, the upstream connection got severed by the
	// provider's serverless timeout, and the aggregator returned an
	// empty payload rather than a useful gateway-timeout error.
	// Right response: drop to a non-reasoning model or shorten the
	// prompt.
	ErrLikelyTimeout = errors.New("likely connection timeout (empty response with no finish_reason)")
)

// DecodeStructuredResponse parses a model's textual response into v
// applying the defensive pipeline described in the file header.
// `packName` is used in the error Message ("gateway returned an empty
// <packName> response") for parity with the strings handlers emitted
// before consolidation.
//
// Backward-compat wrapper around DecodeStructuredResponseWithCause —
// callers that don't have access to finish_reason (or don't care
// about the typed cause) keep using this signature unchanged. New
// callers that want cause-typed errors pass through the new variant.
//
// Returns nil on success, a PackError on failure. The Code is always
// CodeHandlerFailed because parse failure is a server-observable model
// quirk; downstream handlers can wrap the error to add semantic
// context.
func DecodeStructuredResponse(rawBody, packName string, v interface{}) *packs.PackError {
	return DecodeStructuredResponseWithCause(rawBody, "", packName, v)
}

// DecodeStructuredResponseWithCause is the cause-typed variant. When
// finishReason is non-empty (the value the gateway captured from the
// provider — "stop", "length", "content_filter", "tool_calls"), the
// returned PackError's Cause is set to one of the sentinel errors
// above so callers can branch with errors.Is. When finishReason is
// the empty string (caller didn't have it, or the gateway never
// captured one — typical of streaming disconnects), we infer
// ErrLikelyTimeout on empty bodies and leave Cause unset on parse
// failures.
//
// New code should prefer this signature. ADR 051 PR #2.
func DecodeStructuredResponseWithCause(rawBody, finishReason, packName string, v interface{}) *packs.PackError {
	body := llmcontext.StripReasoningTokens(rawBody)
	body = unwrapCodeFence(strings.TrimSpace(body))
	if body == "" {
		cause := classifyEmptyCompletion(finishReason)
		return &packs.PackError{
			Code:    packs.CodeHandlerFailed,
			Message: emptyResponseMessage(packName, cause),
			Cause:   cause,
		}
	}

	// Primary: streaming decoder reads one value and stops. Tolerates
	// trailing prose/HTML/markup that weak models often emit.
	dec := json.NewDecoder(strings.NewReader(body))
	if derr := dec.Decode(v); derr == nil {
		return nil
	} else {
		// Secondary: balanced-brace extraction handles cases where
		// the model wrapped JSON in prose ("Sure, here's your answer:
		// {…} let me know if you need anything else"). Reuses the
		// brace-counting + string-aware helper from webtest.go.
		if obj := extractFirstJSONObject(body); obj != "" {
			if jerr := json.Unmarshal([]byte(obj), v); jerr == nil {
				return nil
			}
		}
		// Both paths exhausted. Classify the parse failure: length-
		// truncation looks like "model emitted partial JSON";
		// constrained-decoding deadlock looks like "model emitted
		// JSON-shaped output that won't parse." finish_reason
		// disambiguates when present. The Cause is the typed
		// sentinel so callers can `errors.Is`; the underlying
		// json.Decoder error is preserved in the Message for
		// operator-side diagnostics.
		cause := classifyParseFailure(finishReason)
		if cause == nil {
			// No typed classification — preserve the original
			// json.Decoder error as the Cause so existing tests
			// asserting on Cause type keep working.
			cause = derr
		}
		return &packs.PackError{
			Code:    packs.CodeHandlerFailed,
			Message: "model output is not valid JSON: " + derr.Error(),
			Cause:   cause,
		}
	}
}

// classifyEmptyCompletion picks the typed cause sentinel for an empty
// HTTP-200 response based on finish_reason. The four cases from the
// ADR 051 research synthesis:
//
//   - finish_reason="content_filter" → ErrSafetyFiltered (provider
//     redacted the output, won't surface the underlying violation)
//   - finish_reason="length" → ErrLengthTruncated (rare for empty
//     bodies but theoretically possible if max_tokens was 0)
//   - finish_reason="" → ErrLikelyTimeout (no reason captured at all
//     usually means a streaming disconnect mid-generation)
//   - any other value → ErrLikelyTimeout (conservative fallback —
//     more often a connection issue than a deliberate provider
//     decision when the body is empty)
func classifyEmptyCompletion(finishReason string) error {
	switch finishReason {
	case "content_filter":
		return ErrSafetyFiltered
	case "length":
		return ErrLengthTruncated
	default:
		return ErrLikelyTimeout
	}
}

// classifyParseFailure picks the typed cause for a non-empty but
// unparseable response. finish_reason="length" is the strong signal
// that the JSON was cut off mid-emission (ErrLengthTruncated); a
// JSON-shaped but unparseable body with no length signal is more
// likely the constrained-decoding deadlock the report describes
// (ErrConstrainedDeadlock). When finish_reason is empty we don't
// guess — return nil so the caller preserves the underlying
// json.Decoder error as the Cause.
func classifyParseFailure(finishReason string) error {
	switch finishReason {
	case "length":
		return ErrLengthTruncated
	case "content_filter":
		// Body had content but finish_reason flags safety; treat
		// as safety-filtered (the visible body may be the leading
		// portion before the redaction kicked in).
		return ErrSafetyFiltered
	default:
		// Without a length / safety signal, the most common shape
		// of "JSON-ish output that won't parse" is the constrained-
		// decoding deadlock — provider's logit masker got into a
		// state where no valid token had probability >0 and the
		// generation aborted. Less precise than the length /
		// safety cases but better than presenting it as a generic
		// parse error to telemetry.
		return ErrConstrainedDeadlock
	}
}

// emptyResponseMessage formats the user-facing Message for an empty
// completion. The shape stays parallel to the existing
// "gateway returned an empty <packName> response" text so existing
// log-scraping doesn't break; we append a cause hint when known so
// operators reading a single error get an actionable next step.
func emptyResponseMessage(packName string, cause error) string {
	base := "gateway returned an empty " + packName + " response"
	switch {
	case errors.Is(cause, ErrSafetyFiltered):
		return base + " (provider safety filter redacted the output — check the prompt for triggering content)"
	case errors.Is(cause, ErrLengthTruncated):
		return base + " (max_tokens reached before any content emitted — increase max_tokens or shorten the prompt)"
	case errors.Is(cause, ErrLikelyTimeout):
		return base + " (likely connection timeout — model may be reasoning-heavy or upstream provider disconnected)"
	default:
		return base
	}
}
