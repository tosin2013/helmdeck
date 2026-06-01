package llmcontext

import (
	"strings"
	"testing"
)

// TestStripReasoningTokens_IdempotentOnCleanInput — clean JSON
// without any reasoning blocks passes through unchanged (modulo
// trim). The helper is called on EVERY response so the
// fast-path-clean case must not corrupt the input.
func TestStripReasoningTokens_IdempotentOnCleanInput(t *testing.T) {
	in := `{"steps":[{"order":1,"tool":"helmdeck.memory_store"}],"complexity":"single-action"}`
	out := StripReasoningTokens(in)
	if out != in {
		t.Errorf("clean input should pass through unchanged\nwant: %q\ngot:  %q", in, out)
	}
	// Second pass is also a no-op.
	out2 := StripReasoningTokens(out)
	if out2 != out {
		t.Errorf("second pass should be idempotent\nwant: %q\ngot:  %q", out, out2)
	}
}

// TestStripReasoningTokens_DropsThinkBlock — the most common shape:
// a single <think>...</think> block followed by the actual JSON.
// This is what Claude 3.7 Sonnet thinking-mode and Kimi-K2.6 emit.
func TestStripReasoningTokens_DropsThinkBlock(t *testing.T) {
	in := `<think>
I need to figure out what tools to call. The user wants three things:
remember a fact, write a blog, generate an image. Let me plan…
</think>
{"steps":[{"order":1,"tool":"helmdeck.memory_store"}],"complexity":"pack-chain"}`

	out := StripReasoningTokens(in)
	if strings.Contains(out, "<think>") || strings.Contains(out, "</think>") {
		t.Errorf("think tags should be stripped; got %q", out)
	}
	if !strings.HasPrefix(out, "{") {
		t.Errorf("output should start with the JSON; got prefix %q", out[:min(20, len(out))])
	}
	if !strings.Contains(out, `"steps"`) {
		t.Errorf("JSON content must survive; got %q", out)
	}
}

// TestStripReasoningTokens_DropsReasoningBlock — the <reasoning>
// variant used by OpenAI o-series models and some Anthropic
// extended-thinking outputs.
func TestStripReasoningTokens_DropsReasoningBlock(t *testing.T) {
	in := `<reasoning>Let me think step by step about this request…</reasoning>
{"result": "answer"}`
	out := StripReasoningTokens(in)
	if strings.Contains(out, "<reasoning>") {
		t.Errorf("reasoning tags should be stripped; got %q", out)
	}
	if !strings.Contains(out, `"result"`) {
		t.Errorf("payload must survive; got %q", out)
	}
}

// TestStripReasoningTokens_DropsSquareBracketReasoning — the
// [REASONING]...[/REASONING] variant seen in quantized open-weights
// inference engines.
func TestStripReasoningTokens_DropsSquareBracketReasoning(t *testing.T) {
	in := `[REASONING]Thinking about this carefully...[/REASONING]
{"x": 1}`
	out := StripReasoningTokens(in)
	if strings.Contains(out, "REASONING") {
		t.Errorf("square-bracket reasoning should be stripped; got %q", out)
	}
	if !strings.Contains(out, `"x"`) {
		t.Errorf("payload must survive; got %q", out)
	}
}

// TestStripReasoningTokens_CaseInsensitive — models emit casing
// variants (<Think>, <THINK>, <think>); the stripper handles all.
func TestStripReasoningTokens_CaseInsensitive(t *testing.T) {
	cases := []string{
		`<Think>reasoning</Think>{"a":1}`,
		`<THINK>reasoning</THINK>{"a":1}`,
		`<think>reasoning</think>{"a":1}`,
		`<TheReasoning>reasoning</TheReasoning>{"a":1}`, // not matched — different tag name
	}
	for i, in := range cases[:3] {
		out := StripReasoningTokens(in)
		if !strings.HasPrefix(out, "{") {
			t.Errorf("case %d: think tag not stripped; got %q", i, out)
		}
	}
	// The fourth case is NOT a recognized tag name, so it stays.
	out := StripReasoningTokens(cases[3])
	if !strings.Contains(out, "TheReasoning") {
		t.Errorf("non-recognized tag should NOT be stripped; got %q", out)
	}
}

// TestStripReasoningTokens_MultipleBlocks — some models emit
// multiple reasoning blocks interleaved (e.g., one before tool
// reflection, one after observation). All should be stripped.
func TestStripReasoningTokens_MultipleBlocks(t *testing.T) {
	in := `<think>First thought</think>
Partial answer fragment.
<think>Second thought</think>
{"final": true}`
	out := StripReasoningTokens(in)
	if strings.Contains(out, "<think>") {
		t.Errorf("all think blocks should be stripped; got %q", out)
	}
	if !strings.Contains(out, "Partial answer fragment.") {
		t.Errorf("content between blocks must survive; got %q", out)
	}
	if !strings.Contains(out, `"final"`) {
		t.Errorf("JSON payload must survive; got %q", out)
	}
}

// TestStripReasoningTokens_MultiLineBody — reasoning blocks span
// many lines including blank lines and embedded JSON snippets
// (models often quote example JSON inside their thinking).
func TestStripReasoningTokens_MultiLineBody(t *testing.T) {
	in := `<think>
Let me consider this.

I could call tool A or tool B.

Example output I'm considering:
{"wrong": "candidate"}

Final decision: use tool A.
</think>
{"correct": "answer"}`
	out := StripReasoningTokens(in)
	if strings.Contains(out, "Let me consider") {
		t.Errorf("multi-line block body should be fully stripped; got %q", out)
	}
	if strings.Contains(out, `"wrong"`) {
		t.Errorf("example JSON inside the block must be stripped; got %q", out)
	}
	if !strings.Contains(out, `"correct"`) {
		t.Errorf("real payload must survive; got %q", out)
	}
}

// TestStripReasoningTokens_UnclosedTagPassThrough — without a
// closing tag we can't know where the reasoning ends. Stripping
// would silently truncate the actual answer. Leave it for the
// caller to discover the parse failure rather than corrupt input.
func TestStripReasoningTokens_UnclosedTagPassThrough(t *testing.T) {
	in := `<think>open but never closed
{"data": "still here"}`
	out := StripReasoningTokens(in)
	// The open tag survives because there's no closing marker.
	if !strings.Contains(out, "<think>") {
		t.Errorf("unclosed open tag should pass through; got %q", out)
	}
	if !strings.Contains(out, `"data"`) {
		t.Errorf("trailing content must not be lost; got %q", out)
	}
}

// TestStripReasoningTokens_TagAttributesIgnored — some models emit
// attributes on the open tag, e.g. <think type="planning">. The
// attribute portion should match (the regex allows any
// non-`>` chars inside the open tag) without breaking the strip.
func TestStripReasoningTokens_TagAttributesIgnored(t *testing.T) {
	in := `<think type="planning">step-by-step</think>{"a":1}`
	out := StripReasoningTokens(in)
	if strings.Contains(out, "<think") {
		t.Errorf("tag with attributes should still be stripped; got %q", out)
	}
	if !strings.Contains(out, `"a"`) {
		t.Errorf("payload must survive; got %q", out)
	}
}

// TestStripReasoningTokens_LeadingTrailingWhitespaceTrimmed — when
// the only content was a reasoning block followed by whitespace,
// the result should be trimmed cleanly so json.Decoder sees a
// valid first token. The trim also avoids cosmetic noise in trim-
// aware log lines.
func TestStripReasoningTokens_LeadingTrailingWhitespaceTrimmed(t *testing.T) {
	in := "  <think>x</think>  \n\n{\"a\":1}  \n"
	out := StripReasoningTokens(in)
	if !strings.HasPrefix(out, "{") {
		t.Errorf("output should start with {, got %q", out)
	}
	if strings.HasSuffix(out, "\n") {
		t.Errorf("output trailing newline should be trimmed, got %q", out)
	}
}

// TestStripReasoningTokens_CollapsesBlankLines — multiple blank
// lines between a stripped block and the surviving JSON should
// collapse to at most one blank line.
func TestStripReasoningTokens_CollapsesBlankLines(t *testing.T) {
	in := "<think>x</think>\n\n\n\n\n\nbody"
	out := StripReasoningTokens(in)
	if strings.Contains(out, "\n\n\n") {
		t.Errorf("blank-line runs should collapse; got %q", out)
	}
	if !strings.Contains(out, "body") {
		t.Errorf("body must survive; got %q", out)
	}
}

// TestStripReasoningTokens_EmptyString — degenerate input returns
// empty unchanged; pure-function contract.
func TestStripReasoningTokens_EmptyString(t *testing.T) {
	if out := StripReasoningTokens(""); out != "" {
		t.Errorf("empty input should stay empty; got %q", out)
	}
}

// TestHasReasoningTokens_TrueWhenMatches — companion detector
// returns true on each pattern variant.
func TestHasReasoningTokens_TrueWhenMatches(t *testing.T) {
	cases := []string{
		`<think>x</think>`,
		`<reasoning>y</reasoning>`,
		`[REASONING]z[/REASONING]`,
		`prefix <think>x</think> suffix`,
	}
	for _, in := range cases {
		if !HasReasoningTokens(in) {
			t.Errorf("should detect reasoning tokens in %q", in)
		}
	}
}

// TestHasReasoningTokens_FalseWhenNoTags — clean input returns false.
// The check is cheap enough to gate "log how many bytes we stripped"
// without re-running the strip pass.
func TestHasReasoningTokens_FalseWhenNoTags(t *testing.T) {
	cases := []string{
		``,
		`{"a":1}`,
		`<think>open but never closed`,
		`</think>orphan close`,
		`thinking is a word`, // bare word, not a tag
	}
	for _, in := range cases {
		if HasReasoningTokens(in) {
			t.Errorf("should NOT detect reasoning tokens in %q", in)
		}
	}
}

// TestStripReasoningTokens_RegressionKimiK2_6 — sample shape we
// observed live: a long English chain-of-thought, then the actual
// JSON. Saved here as a regression guard so future tweaks don't
// regress this specific failure mode.
func TestStripReasoningTokens_RegressionKimiK2_6(t *testing.T) {
	in := `<think>
The user wants me to help with a plan. Let me think about what tools
helmdeck provides that match this intent. I see helmdeck.memory_store,
helmdeck.image_generate, blog.rewrite_for_audience... that's three
distinct actions. The plan should be a pack-chain.

Let me construct the response carefully so it's valid JSON.
</think>

{"steps":[{"order":1,"tool":"helmdeck.memory_store","args":{},"rationale":"persist"},{"order":2,"tool":"blog.rewrite_for_audience","args":{},"rationale":"write"},{"order":3,"tool":"image.generate","args":{},"rationale":"illustrate"}],"complexity":"pack-chain","reasoning":"three actions"}`

	out := StripReasoningTokens(in)
	if strings.Contains(out, "<think>") {
		t.Errorf("think block must be stripped; got prefix %q", out[:min(40, len(out))])
	}
	if !strings.HasPrefix(out, "{") {
		t.Errorf("output should start with JSON; got prefix %q", out[:min(40, len(out))])
	}
	if !strings.Contains(out, `"complexity":"pack-chain"`) {
		t.Errorf("payload must survive intact; got %q", out)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
