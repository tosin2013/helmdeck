// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
)

// runBlogRewrite drives the pack through its handler with a scripted dispatcher
// (no real LLM). Mirrors runSlidesOutline.
func runBlogRewrite(t *testing.T, disp *scriptedDispatcherWT, input string) (json.RawMessage, error) {
	t.Helper()
	pack := BlogRewriteForAudience(disp)
	ec := &packs.ExecutionContext{Pack: pack, Input: json.RawMessage(input)}
	return pack.Handler(context.Background(), ec)
}

func TestBlogRewrite_HappyPath(t *testing.T) {
	reply := `Source: Vaswani et al., 2017.

Why this matters to you: most of the tools you reach for…

## The model rewrite goes here…

## Author's note
Building agents today, the lesson…`
	disp := &scriptedDispatcherWT{replies: []string{reply}}
	raw, err := runBlogRewrite(t, disp, `{
		"source_content":"# Attention Is All You Need\n\nAbstract: …",
		"audience":"developers building AI agents",
		"angle":"connect to practical tool-calling patterns",
		"model":"openrouter/auto"
	}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct{ Markdown, Model string }
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(out.Markdown, "Author's note") {
		t.Errorf("rewrite output not propagated: %q", out.Markdown)
	}
	if out.Model != "openrouter/auto" {
		t.Errorf("model not echoed: %q", out.Model)
	}
	// System prompt must carry the audience + angle (so the model sees
	// what it's writing for). Failing this turns the pack into a generic
	// rewrite — defeating the point.
	sys := disp.captured[0].Messages[0].Content.Text()
	for _, must := range []string{"developers building AI agents", "tool-calling patterns", "DE-JARGON", "Author's note", "STAY GROUNDED"} {
		if !strings.Contains(sys, must) {
			t.Errorf("system prompt missing %q: %s", must, sys)
		}
	}
}

func TestBlogRewrite_UnwrapsCodeFence(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{"```markdown\n# Real post\n\nbody…\n```"}}
	raw, err := runBlogRewrite(t, disp, `{"source_content":"x","audience":"devs","model":"openrouter/auto"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct{ Markdown string }
	_ = json.Unmarshal(raw, &out)
	if strings.Contains(out.Markdown, "```") {
		t.Errorf("code fence not stripped: %q", out.Markdown)
	}
}

func TestBlogRewrite_DefaultAngleWhenOmitted(t *testing.T) {
	// Don't pass angle; pack should fill a neutral default so the
	// system prompt's Author's-note rule has something to write about.
	disp := &scriptedDispatcherWT{replies: []string{"# Post\n\nbody"}}
	_, err := runBlogRewrite(t, disp, `{"source_content":"x","audience":"devs","model":"openrouter/auto"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	sys := disp.captured[0].Messages[0].Content.Text()
	if !strings.Contains(sys, "your honest personal perspective") {
		t.Errorf("missing default angle in system prompt: %s", sys)
	}
}

func TestBlogRewrite_RequiredFields(t *testing.T) {
	for _, tc := range []struct{ name, input string }{
		{"no source_content", `{"audience":"x","model":"m"}`},
		{"no audience", `{"source_content":"x","model":"m"}`},
		{"empty source_content", `{"source_content":"   ","audience":"a","model":"m"}`},
		// `no model` removed: omitted model now falls back to
		// defaultPackModel() (model_defaults.go) rather than
		// rejecting as invalid_input. See
		// TestBlogRewrite_DefaultsModelWhenOmitted below.
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runBlogRewrite(t, &scriptedDispatcherWT{}, tc.input)
			pe := &packs.PackError{}
			if !errors.As(err, &pe) || pe.Code != packs.CodeInvalidInput {
				t.Errorf("want invalid_input, got %v", err)
			}
		})
	}
}

func TestBlogRewrite_RegisteredWithoutDispatcher(t *testing.T) {
	// Match the existing convention: registering without a dispatcher
	// yields an internal error at call time, not at boot. This is what
	// gateway-less deployments see if they call the pack.
	pack := BlogRewriteForAudience(nil)
	_, err := pack.Handler(context.Background(), &packs.ExecutionContext{
		Pack:  pack,
		Input: json.RawMessage(`{"source_content":"x","audience":"a","model":"m"}`),
	})
	pe := &packs.PackError{}
	if !errors.As(err, &pe) || pe.Code != packs.CodeInternal {
		t.Errorf("want CodeInternal when dispatcher is nil, got %v", err)
	}
}

// TestBlogRewrite_DefaultsModelWhenOmitted — Tier C models calling
// this pack via MCP routinely omit the `model` argument. With the
// model_defaults.go helper, omitted `model` falls back to a sensible
// default (HELMDECK_DEFAULT_PACK_MODEL → first OPENROUTER_MODELS
// entry → openrouter/auto hard fallback) instead of rejecting the
// call with invalid_input. The output's model field echoes the
// resolved value so the caller can see what fired.
func TestBlogRewrite_DefaultsModelWhenOmitted(t *testing.T) {
	t.Setenv("HELMDECK_DEFAULT_PACK_MODEL", "")
	t.Setenv("HELMDECK_OPENROUTER_MODELS", "")
	disp := &scriptedDispatcherWT{replies: []string{"# Post\n\nbody"}}
	raw, err := runBlogRewrite(t, disp, `{"source_content":"x","audience":"devs"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct{ Model string }
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Model != "openrouter/auto" {
		t.Errorf("default model not applied: got %q, want openrouter/auto", out.Model)
	}
}

func TestBlogRewrite_DefaultsModelHonorsOperatorOverride(t *testing.T) {
	t.Setenv("HELMDECK_DEFAULT_PACK_MODEL", "openrouter/openai/gpt-oss-120b:free")
	disp := &scriptedDispatcherWT{replies: []string{"# Post\n\nbody"}}
	raw, err := runBlogRewrite(t, disp, `{"source_content":"x","audience":"devs"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct{ Model string }
	_ = json.Unmarshal(raw, &out)
	if out.Model != "openrouter/openai/gpt-oss-120b:free" {
		t.Errorf("operator override not honored: got %q", out.Model)
	}
}

func TestBlogRewrite_EmptyResponse(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{"   "}}
	_, err := runBlogRewrite(t, disp, `{"source_content":"x","audience":"a","model":"m"}`)
	pe := &packs.PackError{}
	if !errors.As(err, &pe) || pe.Code != packs.CodeHandlerFailed {
		t.Errorf("want handler_failed on empty response, got %v", err)
	}
}

// TestBlogRewrite_PersonaDirectiveInPrompt — each closed-set persona
// resolves to a distinct directive that lands in the system prompt; the
// output echoes the canonical key as persona_used. Without this, every
// post defaulted to a formal-academic register no matter the audience.
func TestBlogRewrite_PersonaDirectiveInPrompt(t *testing.T) {
	for _, tc := range []struct {
		persona  string // input
		used     string // expected persona_used
		mustHave string // distinctive phrase the resolver injects
	}{
		{"general", "general", "conversational"},
		{"technical", "technical", "hands-on"},
		{"marketing", "marketing", "call-to-action"},
		{"executive", "executive", "bottom line"},
		{"educational", "educational", "Practice"},
		{"academic", "academic", "Third person"},
		{"TECHNICAL", "technical", "hands-on"}, // case-insensitive
	} {
		t.Run(tc.persona, func(t *testing.T) {
			disp := &scriptedDispatcherWT{replies: []string{"# Post\n\nbody"}}
			raw, err := runBlogRewrite(t, disp, fmt.Sprintf(`{
				"source_content":"x","audience":"devs","model":"openrouter/auto","persona":%q
			}`, tc.persona))
			if err != nil {
				t.Fatalf("handler: %v", err)
			}
			sys := disp.captured[0].Messages[0].Content.Text()
			if !strings.Contains(sys, tc.mustHave) {
				t.Errorf("system prompt should contain %q for persona %q, got:\n%s", tc.mustHave, tc.persona, sys)
			}
			var out struct {
				PersonaUsed string `json:"persona_used"`
			}
			_ = json.Unmarshal(raw, &out)
			if out.PersonaUsed != tc.used {
				t.Errorf("persona_used = %q, want %q", out.PersonaUsed, tc.used)
			}
		})
	}
}

// TestBlogRewrite_PersonaVisualAffordances — each persona that calls
// for code blocks, mermaid diagrams, or tables surfaces that hint in
// the system prompt so the model has an explicit invitation to use
// the affordance when the source supports it. Mirrors the slides
// persona enrichment so the two surfaces stay in lockstep.
func TestBlogRewrite_PersonaVisualAffordances(t *testing.T) {
	for _, tc := range []struct {
		persona  string
		mustHave string
	}{
		{"technical", "mermaid"}, // flowchart/sequenceDiagram invitation
		{"technical", "Code blocks"},
		{"executive", "markdown table"},
		{"educational", "mermaid diagram"},
		{"educational", "minimal code block"},
		{"academic", "numbered figure"},
	} {
		t.Run(tc.persona+"/"+tc.mustHave, func(t *testing.T) {
			disp := &scriptedDispatcherWT{replies: []string{"# Post\n\nbody"}}
			_, err := runBlogRewrite(t, disp, fmt.Sprintf(`{
				"source_content":"x","audience":"devs","model":"openrouter/auto","persona":%q
			}`, tc.persona))
			if err != nil {
				t.Fatalf("handler: %v", err)
			}
			sys := disp.captured[0].Messages[0].Content.Text()
			if !strings.Contains(sys, tc.mustHave) {
				t.Errorf("system prompt for persona %q should mention %q; got:\n%s", tc.persona, tc.mustHave, sys)
			}
		})
	}
}

// TestBlogRewrite_FreeformPersonaPassThrough — an unknown persona key is
// passed through as a freeform style hint (callers aren't limited to the
// closed set), and persona_used echoes the original string.
func TestBlogRewrite_FreeformPersonaPassThrough(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{"# Post\n\nbody"}}
	raw, err := runBlogRewrite(t, disp, `{
		"source_content":"x","audience":"devs","model":"openrouter/auto",
		"persona":"deadpan irreverent"
	}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	sys := disp.captured[0].Messages[0].Content.Text()
	for _, must := range []string{"deadpan irreverent", "tailor tone"} {
		if !strings.Contains(sys, must) {
			t.Errorf("system prompt should pass through freeform persona; missing %q in:\n%s", must, sys)
		}
	}
	var out struct {
		PersonaUsed string `json:"persona_used"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.PersonaUsed != "deadpan irreverent" {
		t.Errorf("persona_used = %q, want freeform passthrough", out.PersonaUsed)
	}
}

// TestBlogRewrite_DefaultPersonaWhenOmitted — empty persona resolves to
// "general", so the pack always has a style directive in the prompt.
func TestBlogRewrite_DefaultPersonaWhenOmitted(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{"# Post\n\nbody"}}
	raw, err := runBlogRewrite(t, disp, `{"source_content":"x","audience":"devs","model":"openrouter/auto"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		PersonaUsed string `json:"persona_used"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.PersonaUsed != "general" {
		t.Errorf("default persona = %q, want general", out.PersonaUsed)
	}
}

// makeSourceWithWordCount returns a markdown blob roughly `target` words
// long. Used by the length-sizing tests so they're insulated from the
// pack's exact counting strategy as long as it's whitespace-delimited.
func makeSourceWithWordCount(target int) string {
	parts := make([]string, target)
	for i := range parts {
		parts[i] = "word"
	}
	return strings.Join(parts, " ")
}

// TestBlogRewrite_InspectMode_NoModelCall — inspect:true must short-circuit
// before the dispatcher is touched. Returns measurements + suggestion.
// Cheap path the agent uses to negotiate before committing to a generate.
func TestBlogRewrite_InspectMode_NoModelCall(t *testing.T) {
	disp := &scriptedDispatcherWT{} // no replies queued; failure if dispatched
	src := makeSourceWithWordCount(5000)
	input := fmt.Sprintf(`{"source_content":%q,"audience":"devs","inspect":true,"length_intent":"thorough"}`, src)
	raw, err := runBlogRewrite(t, disp, input)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if disp.calls != 0 {
		t.Errorf("inspect:true must NOT call the dispatcher; calls=%d", disp.calls)
	}
	var out struct {
		Inspect             bool   `json:"inspect"`
		Markdown            string `json:"markdown"`
		Model               string `json:"model"`
		SourceWords         int    `json:"source_words"`
		SuggestedTarget     int    `json:"suggested_target"`
		SuggestedTargetMin  int    `json:"suggested_target_min"`
		SuggestedTargetMax  int    `json:"suggested_target_max"`
		LengthIntentApplied string `json:"length_intent_applied"`
		Reason              string `json:"reason"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.Inspect {
		t.Errorf("inspect flag not echoed: %+v", out)
	}
	if out.Markdown != "" {
		t.Errorf("inspect should return empty markdown, got %q", out.Markdown)
	}
	if out.Model == "" {
		t.Error("inspect should populate model (resolved default) so engine schema validator passes")
	}
	if out.SourceWords != 5000 {
		t.Errorf("source_words = %d, want 5000", out.SourceWords)
	}
	// 5000 * 0.30 = 1500 for thorough; within ceiling of 2500.
	if out.SuggestedTarget != 1500 {
		t.Errorf("suggested_target = %d, want 1500 (5000 * 0.30 for thorough)", out.SuggestedTarget)
	}
	if out.LengthIntentApplied != "intent:thorough" {
		t.Errorf("length_intent_applied = %q, want intent:thorough", out.LengthIntentApplied)
	}
	if out.SuggestedTargetMin >= out.SuggestedTarget || out.SuggestedTargetMax <= out.SuggestedTarget {
		t.Errorf("min/max should bracket chosen: min=%d chosen=%d max=%d",
			out.SuggestedTargetMin, out.SuggestedTarget, out.SuggestedTargetMax)
	}
	if !strings.Contains(out.Reason, "5000") || !strings.Contains(out.Reason, "thorough") {
		t.Errorf("reason should mention source size + applied intent: %q", out.Reason)
	}
}

// TestBlogRewrite_LengthIntent_ScalesWithSource — each intent picks a
// target proportional to source size. Catches regression in the table or
// the resolver path. Spot-checks one chosen target per intent.
func TestBlogRewrite_LengthIntent_ScalesWithSource(t *testing.T) {
	cases := []struct {
		intent       string
		sourceWords  int
		wantChosen   int
		wantInRange  bool // true means assert wantChosen exactly
		minOK, maxOK int  // when wantInRange is false, just check bounds
	}{
		{"summary", 5000, 500, true, 0, 0},     // 5000 * 0.10
		{"thorough", 5000, 1500, true, 0, 0},   // 5000 * 0.30
		{"exhaustive", 7000, 3850, true, 0, 0}, // 7000 * 0.55
	}
	for _, tc := range cases {
		t.Run(tc.intent, func(t *testing.T) {
			disp := &scriptedDispatcherWT{replies: []string{"# Post\n\nbody."}}
			src := makeSourceWithWordCount(tc.sourceWords)
			input := fmt.Sprintf(`{"source_content":%q,"audience":"devs","model":"openrouter/auto","length_intent":%q}`,
				src, tc.intent)
			raw, err := runBlogRewrite(t, disp, input)
			if err != nil {
				t.Fatalf("handler: %v", err)
			}
			var out struct {
				TargetWordsChosen   int    `json:"target_words_chosen"`
				LengthIntentApplied string `json:"length_intent_applied"`
				SourceWords         int    `json:"source_words"`
			}
			_ = json.Unmarshal(raw, &out)
			if out.SourceWords != tc.sourceWords {
				t.Errorf("source_words = %d, want %d", out.SourceWords, tc.sourceWords)
			}
			if out.TargetWordsChosen != tc.wantChosen {
				t.Errorf("intent=%s: target_words_chosen = %d, want %d",
					tc.intent, out.TargetWordsChosen, tc.wantChosen)
			}
			if want := "intent:" + tc.intent; out.LengthIntentApplied != want {
				t.Errorf("length_intent_applied = %q, want %q", out.LengthIntentApplied, want)
			}
		})
	}
}

// TestBlogRewrite_LengthIntent_ClampsFloor — a tiny source with intent
// exhaustive still produces a usable target by clamping to the row floor.
// Without the floor, 100 * 0.55 = 55 words — too short for any technical
// post and below the model's reasonable minimum.
func TestBlogRewrite_LengthIntent_ClampsFloor(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{"# Post\n\nbody."}}
	src := makeSourceWithWordCount(100)
	input := fmt.Sprintf(`{"source_content":%q,"audience":"devs","model":"openrouter/auto","length_intent":"exhaustive"}`, src)
	raw, err := runBlogRewrite(t, disp, input)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		TargetWordsChosen int `json:"target_words_chosen"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.TargetWordsChosen != 1500 {
		t.Errorf("clamped target = %d, want 1500 (exhaustive floor)", out.TargetWordsChosen)
	}
}

// TestBlogRewrite_LengthIntent_ClampsCeiling — a huge source with intent
// summary stays under the row ceiling. Without the ceiling, 20000 * 0.10
// = 2000 words — long enough to push the model's max_tokens past what a
// summary should ever cost.
func TestBlogRewrite_LengthIntent_ClampsCeiling(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{"# Post\n\nbody."}}
	src := makeSourceWithWordCount(20000)
	input := fmt.Sprintf(`{"source_content":%q,"audience":"devs","model":"openrouter/auto","length_intent":"summary"}`, src)
	raw, err := runBlogRewrite(t, disp, input)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		TargetWordsChosen int `json:"target_words_chosen"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.TargetWordsChosen != 1200 {
		t.Errorf("clamped target = %d, want 1200 (summary ceiling)", out.TargetWordsChosen)
	}
}

// TestBlogRewrite_ExplicitNumericOverridesIntent — when target_words_min
// AND target_words_max are both set, they win over length_intent. Lets
// power callers bypass the intent table.
func TestBlogRewrite_ExplicitNumericOverridesIntent(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{"# Post\n\nbody."}}
	src := makeSourceWithWordCount(5000)
	input := fmt.Sprintf(`{
		"source_content":%q,"audience":"devs","model":"openrouter/auto",
		"length_intent":"summary",
		"target_words_min":2000,"target_words_max":2400
	}`, src)
	raw, err := runBlogRewrite(t, disp, input)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		TargetWordsMin      int    `json:"target_words_min"`
		TargetWordsMax      int    `json:"target_words_max"`
		TargetWordsChosen   int    `json:"target_words_chosen"`
		LengthIntentApplied string `json:"length_intent_applied"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.TargetWordsMin != 2000 || out.TargetWordsMax != 2400 {
		t.Errorf("explicit numeric not honored: min=%d max=%d", out.TargetWordsMin, out.TargetWordsMax)
	}
	if out.TargetWordsChosen != 2200 {
		t.Errorf("chosen should be midpoint of explicit range: got %d, want 2200", out.TargetWordsChosen)
	}
	if out.LengthIntentApplied != "explicit" {
		t.Errorf("length_intent_applied = %q, want explicit (numeric overrode intent)", out.LengthIntentApplied)
	}
}

// TestBlogRewrite_PartialNumericFallsThroughToIntent — only one of
// target_words_min/max set is partial; pack must fall through to intent
// rather than guessing the missing bound.
func TestBlogRewrite_PartialNumericFallsThroughToIntent(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{"# Post\n\nbody."}}
	src := makeSourceWithWordCount(5000)
	input := fmt.Sprintf(`{
		"source_content":%q,"audience":"devs","model":"openrouter/auto",
		"length_intent":"thorough",
		"target_words_min":2000
	}`, src)
	raw, err := runBlogRewrite(t, disp, input)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		LengthIntentApplied string `json:"length_intent_applied"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.LengthIntentApplied != "intent:thorough" {
		t.Errorf("partial numeric should fall through; got applied=%q", out.LengthIntentApplied)
	}
}

// TestBlogRewrite_PromptCarriesTargetRange — the chosen target lands in
// the system prompt as an explicit override of the persona's word
// range. Without this the persona's "800-1200" silently out-votes a
// chosen "exhaustive" target of 3300-4400 and the JIT sizing has no
// visible effect.
func TestBlogRewrite_PromptCarriesTargetRange(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{"# Post\n\nbody."}}
	src := makeSourceWithWordCount(7000)
	input := fmt.Sprintf(`{"source_content":%q,"audience":"devs","model":"openrouter/auto","length_intent":"exhaustive"}`, src)
	_, err := runBlogRewrite(t, disp, input)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	sys := disp.captured[0].Messages[0].Content.Text()
	for _, must := range []string{
		"Target length for this post:",
		"overrides any word-count range",
	} {
		if !strings.Contains(sys, must) {
			t.Errorf("system prompt missing JIT-target directive %q:\n%s", must, sys)
		}
	}
}

// TestBlogRewrite_Truncated_FinishReasonLength — strong signal. When the
// gateway reports finish_reason=length, truncated:true must propagate.
func TestBlogRewrite_Truncated_FinishReasonLength(t *testing.T) {
	disp := &scriptedDispatcherWT{
		replies:       []string{"# Post\n\nThis ends mid-sentence and"},
		finishReasons: []string{"length"},
	}
	raw, err := runBlogRewrite(t, disp,
		`{"source_content":"x","audience":"devs","model":"openrouter/auto","length_intent":"summary"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		Truncated bool `json:"truncated"`
	}
	_ = json.Unmarshal(raw, &out)
	if !out.Truncated {
		t.Error("finish_reason=length should set truncated:true")
	}
}

// TestBlogRewrite_NotTruncated_FinishReasonStop — well-terminated output
// with finish_reason=stop should NOT be flagged truncated.
func TestBlogRewrite_NotTruncated_FinishReasonStop(t *testing.T) {
	disp := &scriptedDispatcherWT{
		replies:       []string{"# Post\n\nBody ends with a period."},
		finishReasons: []string{"stop"},
	}
	raw, err := runBlogRewrite(t, disp,
		`{"source_content":"x","audience":"devs","model":"openrouter/auto"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		Truncated bool `json:"truncated"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.Truncated {
		t.Error("finish_reason=stop + terminal punctuation should NOT set truncated")
	}
}

// TestBlogRewrite_Truncated_MidSentenceHeuristic — fallback for providers
// (e.g. Ollama) that don't always populate finish_reason. Output near
// the upper target bound AND ending mid-sentence is treated as
// truncated. Imperfect but better than silent.
func TestBlogRewrite_Truncated_MidSentenceHeuristic(t *testing.T) {
	// Summary on a 2800-word source: chosen = 280 → clamped up to
	// floor=300; max bracket = 300 * 1.15 = 345; heuristic threshold
	// = 95% of 345 ≈ 328. Reply needs ≥ 328 whitespace-delimited
	// words AND end without sentence-terminating punctuation.
	src := makeSourceWithWordCount(2800)
	reply := strings.Repeat("word ", 350) + "and then suddenly"
	disp := &scriptedDispatcherWT{
		replies:       []string{reply},
		finishReasons: []string{""}, // provider didn't expose finish_reason
	}
	input := fmt.Sprintf(`{"source_content":%q,"audience":"devs","model":"openrouter/auto","length_intent":"summary"}`, src)
	raw, err := runBlogRewrite(t, disp, input)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		Truncated   bool `json:"truncated"`
		OutputWords int  `json:"output_words"`
	}
	_ = json.Unmarshal(raw, &out)
	if !out.Truncated {
		t.Errorf("mid-sentence + ≥95%% of upper bound should set truncated; output_words=%d", out.OutputWords)
	}
}

// TestBlogRewrite_OutputMetricsAlwaysPresent — existing back-compat path
// (no length inputs) must still populate source_words/target/output and
// truncated, so callers can rely on the new fields uniformly.
func TestBlogRewrite_OutputMetricsAlwaysPresent(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{"# Post\n\nBody ends here."}}
	raw, err := runBlogRewrite(t, disp,
		`{"source_content":"one two three four five","audience":"devs","model":"openrouter/auto"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	for _, k := range []string{"source_words", "target_words_chosen", "output_words", "compression_ratio", "length_intent_applied", "truncated"} {
		if _, ok := out[k]; !ok {
			t.Errorf("missing metric %q in output: %v", k, out)
		}
	}
	// Default intent applies (thorough).
	if applied, _ := out["length_intent_applied"].(string); applied != "intent:thorough" {
		t.Errorf("default applied = %q, want intent:thorough", applied)
	}
}

// TestBlogRewrite_InspectWithoutDispatcher — inspect:true is the cheap
// pack-internal path; gateway-less deployments must be able to use it
// without a dispatcher installed. Validates the design decision (pack
// is the authority on sizing, no model needed).
func TestBlogRewrite_InspectWithoutDispatcher(t *testing.T) {
	pack := BlogRewriteForAudience(nil)
	ec := &packs.ExecutionContext{
		Pack: pack,
		Input: json.RawMessage(fmt.Sprintf(
			`{"source_content":%q,"audience":"devs","inspect":true,"length_intent":"summary"}`,
			makeSourceWithWordCount(3000))),
	}
	raw, err := pack.Handler(context.Background(), ec)
	if err != nil {
		t.Fatalf("inspect with nil dispatcher should not error: %v", err)
	}
	var out struct {
		Inspect          bool `json:"inspect"`
		SourceWords      int  `json:"source_words"`
		SuggestedTarget  int  `json:"suggested_target"`
	}
	_ = json.Unmarshal(raw, &out)
	if !out.Inspect || out.SourceWords != 3000 || out.SuggestedTarget == 0 {
		t.Errorf("inspect-without-dispatcher output incomplete: %+v", out)
	}
}
