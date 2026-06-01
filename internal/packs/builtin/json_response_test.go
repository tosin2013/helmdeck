package builtin

import (
	"errors"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
)

// testTarget is the decode target shared across the tests below.
// Mirrors the shape plan.go's response_test uses so the helper's
// behavior on real call-site shapes is exercised.
type testTarget struct {
	Steps      []map[string]interface{} `json:"steps"`
	Complexity string                   `json:"complexity"`
	Reasoning  string                   `json:"reasoning"`
}

// TestDecodeStructuredResponse_HappyPath — clean JSON parses
// successfully. The defensive pipeline must not corrupt input that
// didn't need defense.
func TestDecodeStructuredResponse_HappyPath(t *testing.T) {
	body := `{"steps":[{"order":1,"tool":"x"}],"complexity":"single-action","reasoning":"one tool"}`
	var out testTarget
	if err := DecodeStructuredResponse(body, "plan", &out); err != nil {
		t.Fatalf("happy path should not error; got %v", err)
	}
	if out.Complexity != "single-action" {
		t.Errorf("complexity not preserved; got %q", out.Complexity)
	}
	if len(out.Steps) != 1 {
		t.Errorf("steps not preserved; got %d", len(out.Steps))
	}
}

// TestDecodeStructuredResponse_StripsThinkBlock — the hybrid-model
// failure mode: <think>...</think> precedes the JSON. The helper
// strips it before the decoder fires.
func TestDecodeStructuredResponse_StripsThinkBlock(t *testing.T) {
	body := `<think>
Let me figure out the right plan. The user wants one tool call.
</think>
{"steps":[],"complexity":"single-action"}`
	var out testTarget
	if err := DecodeStructuredResponse(body, "plan", &out); err != nil {
		t.Fatalf("think-block prefix should parse cleanly; got %v", err)
	}
	if out.Complexity != "single-action" {
		t.Errorf("complexity not preserved after strip; got %q", out.Complexity)
	}
}

// TestDecodeStructuredResponse_StripsReasoningBlock — the
// <reasoning>...</reasoning> variant used by OpenAI o-series models.
func TestDecodeStructuredResponse_StripsReasoningBlock(t *testing.T) {
	body := `<reasoning>thinking step by step</reasoning>{"complexity":"pack-chain"}`
	var out testTarget
	if err := DecodeStructuredResponse(body, "plan", &out); err != nil {
		t.Fatalf("reasoning-block prefix should parse cleanly; got %v", err)
	}
	if out.Complexity != "pack-chain" {
		t.Errorf("complexity not preserved; got %q", out.Complexity)
	}
}

// TestDecodeStructuredResponse_UnwrapsCodeFences — even with strict
// system prompts forbidding code fences, weak models often emit
// ```json…```. Helper strips before parse.
func TestDecodeStructuredResponse_UnwrapsCodeFences(t *testing.T) {
	body := "```json\n{\"complexity\":\"pack-chain\"}\n```"
	var out testTarget
	if err := DecodeStructuredResponse(body, "plan", &out); err != nil {
		t.Fatalf("code-fenced JSON should parse; got %v", err)
	}
	if out.Complexity != "pack-chain" {
		t.Errorf("complexity not preserved; got %q", out.Complexity)
	}
}

// TestDecodeStructuredResponse_TolerateTrailingContent — model
// emits valid JSON then trailing prose / HTML / a second object.
// json.Decoder reads one value and stops; this was the critical fix
// from ADR 050 PR #4 that unblocked the MiniMax-paste prompt on
// openrouter/openrouter/free.
func TestDecodeStructuredResponse_TolerateTrailingContent(t *testing.T) {
	body := `{"complexity":"single-action"} this trailing garbage <html> tags`
	var out testTarget
	if err := DecodeStructuredResponse(body, "plan", &out); err != nil {
		t.Fatalf("trailing content should be tolerated; got %v", err)
	}
	if out.Complexity != "single-action" {
		t.Errorf("complexity not preserved; got %q", out.Complexity)
	}
}

// TestDecodeStructuredResponse_LeadingProse — model emitted prose
// before the JSON object. extractFirstJSONObject fallback finds
// the balanced {…} substring and parses it.
func TestDecodeStructuredResponse_LeadingProse(t *testing.T) {
	body := `Sure, here's the plan you requested: {"complexity":"pack-chain","reasoning":"x"} let me know!`
	var out testTarget
	if err := DecodeStructuredResponse(body, "plan", &out); err != nil {
		t.Fatalf("leading prose should be tolerated via substring extraction; got %v", err)
	}
	if out.Complexity != "pack-chain" {
		t.Errorf("complexity not preserved; got %q", out.Complexity)
	}
}

// TestDecodeStructuredResponse_BraceInsideString — the
// balanced-brace extractor must NOT prematurely match a `}` that
// lives inside a JSON string literal. Regression guard for the
// naive LastIndex approach plan.go used to use.
func TestDecodeStructuredResponse_BraceInsideString(t *testing.T) {
	body := `{"complexity":"pack-chain","reasoning":"text containing } brace"}`
	var out testTarget
	if err := DecodeStructuredResponse(body, "plan", &out); err != nil {
		t.Fatalf("brace inside string should not break parse; got %v", err)
	}
	if !strings.Contains(out.Reasoning, "brace") {
		t.Errorf("reasoning containing literal } got truncated; got %q", out.Reasoning)
	}
}

// TestDecodeStructuredResponse_EmptyBody — empty input yields the
// distinct "gateway returned an empty <packName> response" error.
// The packName field is used so operators see "empty plan response"
// vs "empty route response" vs "empty rewrite response" depending
// on which handler hit the failure.
func TestDecodeStructuredResponse_EmptyBody(t *testing.T) {
	var out testTarget
	err := DecodeStructuredResponse("", "plan", &out)
	if err == nil {
		t.Fatal("empty body should error")
	}
	if err.Code != packs.CodeHandlerFailed {
		t.Errorf("want CodeHandlerFailed; got %s", err.Code)
	}
	if !strings.Contains(err.Message, "empty plan response") {
		t.Errorf("error should name the pack; got %q", err.Message)
	}
}

// TestDecodeStructuredResponse_OnlyReasoningBlock — model emitted
// thinking but never made it to the JSON (the Kimi-K2.6 timeout
// pattern). After stripping, body is empty → same error path as
// truly empty input.
func TestDecodeStructuredResponse_OnlyReasoningBlock(t *testing.T) {
	body := `<think>I was still thinking when the timeout hit</think>`
	var out testTarget
	err := DecodeStructuredResponse(body, "plan", &out)
	if err == nil {
		t.Fatal("reasoning-only input should error after strip")
	}
	if !strings.Contains(err.Message, "empty plan response") {
		t.Errorf("post-strip empty should look like empty-body case; got %q", err.Message)
	}
}

// TestDecodeStructuredResponse_UnrecoverableGarbage — input with
// no recoverable JSON object yields a parse error mentioning what
// the decoder choked on. Helps operators diagnose model output
// drift.
func TestDecodeStructuredResponse_UnrecoverableGarbage(t *testing.T) {
	body := `this is just prose with no JSON anywhere at all`
	var out testTarget
	err := DecodeStructuredResponse(body, "plan", &out)
	if err == nil {
		t.Fatal("unparseable input should error")
	}
	if !strings.Contains(err.Message, "model output is not valid JSON") {
		t.Errorf("error should describe parse failure; got %q", err.Message)
	}
	if err.Cause == nil {
		t.Errorf("error should chain a Cause from json.Decoder")
	}
}

// TestDecodeStructuredResponse_PackNameThreaded — error messages
// thread the packName so operators see "rewrite" / "routing" /
// "plan" depending on which caller hit the failure.
func TestDecodeStructuredResponse_PackNameThreaded(t *testing.T) {
	var out testTarget
	if err := DecodeStructuredResponse("", "rewrite", &out); err == nil ||
		!strings.Contains(err.Message, "empty rewrite response") {
		t.Errorf("packName should appear in empty-response error; got %v", err)
	}
}

// TestDecodeStructuredResponse_ThinkBlockPlusCodeFence — combined
// failure mode: model emits a reasoning block AND wraps its JSON
// in code fences. Both layers should be peeled.
func TestDecodeStructuredResponse_ThinkBlockPlusCodeFence(t *testing.T) {
	body := "<think>let me wrap this in markdown to be safe</think>\n```json\n{\"complexity\":\"pack-chain\"}\n```"
	var out testTarget
	if err := DecodeStructuredResponse(body, "plan", &out); err != nil {
		t.Fatalf("combined think + fence should parse; got %v", err)
	}
	if out.Complexity != "pack-chain" {
		t.Errorf("complexity not preserved; got %q", out.Complexity)
	}
}

// --- ADR 051 PR #2: cause-typed error tests -----------------------

// TestDecodeStructuredResponseWithCause_SafetyFiltered — empty body
// + finish_reason="content_filter" classifies as ErrSafetyFiltered.
// The Message includes an actionable hint about the prompt.
func TestDecodeStructuredResponseWithCause_SafetyFiltered(t *testing.T) {
	var out testTarget
	err := DecodeStructuredResponseWithCause("", "content_filter", "plan", &out)
	if err == nil {
		t.Fatal("empty body with content_filter should error")
	}
	if !errors.Is(err.Cause, ErrSafetyFiltered) {
		t.Errorf("Cause should be ErrSafetyFiltered; got %v", err.Cause)
	}
	if !strings.Contains(err.Message, "safety filter") {
		t.Errorf("Message should mention safety filter; got %q", err.Message)
	}
}

// TestDecodeStructuredResponseWithCause_LengthTruncated — empty body
// + finish_reason="length" classifies as ErrLengthTruncated. Rare
// shape but the rule is consistent.
func TestDecodeStructuredResponseWithCause_LengthTruncated(t *testing.T) {
	var out testTarget
	err := DecodeStructuredResponseWithCause("", "length", "plan", &out)
	if err == nil {
		t.Fatal("empty body with length should error")
	}
	if !errors.Is(err.Cause, ErrLengthTruncated) {
		t.Errorf("Cause should be ErrLengthTruncated; got %v", err.Cause)
	}
	if !strings.Contains(err.Message, "max_tokens") {
		t.Errorf("Message should mention max_tokens; got %q", err.Message)
	}
}

// TestDecodeStructuredResponseWithCause_LikelyTimeout_EmptyFinishReason
// — empty body + no finish_reason classifies as ErrLikelyTimeout
// (streaming disconnect pattern).
func TestDecodeStructuredResponseWithCause_LikelyTimeout_EmptyFinishReason(t *testing.T) {
	var out testTarget
	err := DecodeStructuredResponseWithCause("", "", "plan", &out)
	if err == nil {
		t.Fatal("empty body should error")
	}
	if !errors.Is(err.Cause, ErrLikelyTimeout) {
		t.Errorf("Cause should be ErrLikelyTimeout; got %v", err.Cause)
	}
	if !strings.Contains(err.Message, "timeout") {
		t.Errorf("Message should mention timeout; got %q", err.Message)
	}
}

// TestDecodeStructuredResponseWithCause_LikelyTimeout_UnknownFinishReason
// — an unrecognized finish_reason (e.g. an aggregator's custom marker)
// on an empty body conservatively classifies as ErrLikelyTimeout
// rather than guessing.
func TestDecodeStructuredResponseWithCause_LikelyTimeout_UnknownFinishReason(t *testing.T) {
	var out testTarget
	err := DecodeStructuredResponseWithCause("", "weird_aggregator_marker", "plan", &out)
	if err == nil {
		t.Fatal("empty body should error")
	}
	if !errors.Is(err.Cause, ErrLikelyTimeout) {
		t.Errorf("unknown finish_reason on empty body should default to ErrLikelyTimeout; got %v", err.Cause)
	}
}

// TestDecodeStructuredResponseWithCause_LengthTruncatedOnParseFail —
// non-empty but unparseable body + finish_reason="length" classifies
// as ErrLengthTruncated (model emitted partial JSON that got cut off
// mid-emission).
func TestDecodeStructuredResponseWithCause_LengthTruncatedOnParseFail(t *testing.T) {
	body := `{"steps":[{"order":1,"tool":"x","args":{`
	var out testTarget
	err := DecodeStructuredResponseWithCause(body, "length", "plan", &out)
	if err == nil {
		t.Fatal("partial JSON should error")
	}
	if !errors.Is(err.Cause, ErrLengthTruncated) {
		t.Errorf("Cause should be ErrLengthTruncated; got %v (msg=%q)", err.Cause, err.Message)
	}
	// Original parse error stays in the Message for diagnostics.
	if !strings.Contains(err.Message, "model output is not valid JSON") {
		t.Errorf("Message should preserve parse-error prefix; got %q", err.Message)
	}
}

// TestDecodeStructuredResponseWithCause_ConstrainedDeadlock — non-
// empty but unparseable body + no specific finish_reason classifies
// as ErrConstrainedDeadlock. This is the constrained-decoding fail
// mode the research synthesis describes for quantized open-weights
// models.
func TestDecodeStructuredResponseWithCause_ConstrainedDeadlock(t *testing.T) {
	body := `{"this is not valid json at all` // intentional garbage
	var out testTarget
	err := DecodeStructuredResponseWithCause(body, "stop", "plan", &out)
	if err == nil {
		t.Fatal("garbage should error")
	}
	if !errors.Is(err.Cause, ErrConstrainedDeadlock) {
		t.Errorf("Cause should be ErrConstrainedDeadlock; got %v", err.Cause)
	}
}

// TestDecodeStructuredResponseWithCause_SafetyFilteredOnParseFail —
// content present + finish_reason="content_filter" classifies as
// ErrSafetyFiltered (the visible body may be the lead-in before the
// safety system cut it off).
func TestDecodeStructuredResponseWithCause_SafetyFilteredOnParseFail(t *testing.T) {
	body := `Sorry, I can't help with that.`
	var out testTarget
	err := DecodeStructuredResponseWithCause(body, "content_filter", "plan", &out)
	if err == nil {
		t.Fatal("non-JSON body should error")
	}
	if !errors.Is(err.Cause, ErrSafetyFiltered) {
		t.Errorf("Cause should be ErrSafetyFiltered; got %v", err.Cause)
	}
}

// TestDecodeStructuredResponseWithCause_BackwardCompatNilCause — when
// finish_reason is empty AND the body fails to parse, the Cause stays
// the original json.Decoder error (NOT a typed sentinel). This
// preserves the pre-PR-#2 contract — callers that asserted on a
// specific Cause type keep working.
func TestDecodeStructuredResponseWithCause_BackwardCompatNilCause(t *testing.T) {
	body := `garbage`
	var out testTarget
	err := DecodeStructuredResponseWithCause(body, "", "plan", &out)
	if err == nil {
		t.Fatal("garbage should error")
	}
	// When finishReason is empty AND we have a parse failure, we
	// classify as ErrConstrainedDeadlock since that's the most
	// likely cause for "non-empty body that won't parse".
	if !errors.Is(err.Cause, ErrConstrainedDeadlock) {
		t.Errorf("empty finish_reason + parse fail should still classify; got %v", err.Cause)
	}
}

// TestDecodeStructuredResponse_BackwardCompatWrapper — the existing
// no-finish_reason entry point still returns the same Message shape
// it always has. The wrapper just calls the cause-typed variant with
// an empty finish_reason, which classifies empty bodies as
// ErrLikelyTimeout.
func TestDecodeStructuredResponse_BackwardCompatWrapper(t *testing.T) {
	var out testTarget
	err := DecodeStructuredResponse("", "plan", &out)
	if err == nil {
		t.Fatal("empty body should error")
	}
	if !errors.Is(err.Cause, ErrLikelyTimeout) {
		t.Errorf("Cause should be ErrLikelyTimeout via backward-compat wrapper; got %v", err.Cause)
	}
	// Message still starts with the historical text.
	if !strings.HasPrefix(err.Message, "gateway returned an empty plan response") {
		t.Errorf("Message should preserve historical prefix; got %q", err.Message)
	}
}
