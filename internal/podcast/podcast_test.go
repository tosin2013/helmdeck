// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package podcast

import (
	"errors"
	"testing"
)

func TestPickEngine(t *testing.T) {
	t.Run("default empty name returns elevenlabs", func(t *testing.T) {
		eng, err := PickEngine("", "key")
		if err != nil {
			t.Fatal(err)
		}
		if eng.Name() != "elevenlabs" {
			t.Errorf("name = %q, want elevenlabs", eng.Name())
		}
	})

	t.Run("explicit elevenlabs", func(t *testing.T) {
		eng, err := PickEngine("elevenlabs", "key")
		if err != nil {
			t.Fatal(err)
		}
		if eng.Name() != "elevenlabs" {
			t.Errorf("name = %q", eng.Name())
		}
	})

	t.Run("unknown engine errors", func(t *testing.T) {
		_, err := PickEngine("playht", "key")
		if err == nil {
			t.Fatal("expected error for unknown engine")
		}
		if !errors.Is(err, ErrEngineNotFound) {
			t.Errorf("expected ErrEngineNotFound, got %v", err)
		}
	})
}

func TestValidateScript(t *testing.T) {
	t.Run("happy path returns sorted unique speakers", func(t *testing.T) {
		script := []Turn{
			{Speaker: "Alex", Text: "Welcome"},
			{Speaker: "Jordan", Text: "Thanks"},
			{Speaker: "Alex", Text: "Today we're talking about"},
		}
		speakers := map[string]string{"Alex": "v1", "Jordan": "v2"}
		seen, err := ValidateScript(script, speakers)
		if err != nil {
			t.Fatal(err)
		}
		if len(seen) != 2 || seen[0] != "Alex" || seen[1] != "Jordan" {
			t.Errorf("seen = %v, want [Alex Jordan]", seen)
		}
	})

	t.Run("empty script errors", func(t *testing.T) {
		_, err := ValidateScript(nil, map[string]string{"x": "y"})
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("speaker not in map errors", func(t *testing.T) {
		_, err := ValidateScript(
			[]Turn{{Speaker: "Carol", Text: "hi"}},
			map[string]string{"Alex": "v1"},
		)
		if err == nil || err.Error() == "" {
			t.Fatal("expected error")
		}
		// The error message should help the agent — list the configured speakers.
		if !contains(err.Error(), "Alex") {
			t.Errorf("error should list configured speakers: %v", err)
		}
	})

	t.Run("empty text errors", func(t *testing.T) {
		_, err := ValidateScript(
			[]Turn{{Speaker: "Alex", Text: "  "}},
			map[string]string{"Alex": "v1"},
		)
		if err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestValidTheme(t *testing.T) {
	for _, valid := range []string{"interview", "debate", "news-roundup", "deep-dive", "solo-essay"} {
		if !ValidTheme(valid) {
			t.Errorf("expected %q to be valid", valid)
		}
	}
	for _, invalid := range []string{"chat", "rant", "", "interview-style"} {
		if ValidTheme(invalid) {
			t.Errorf("expected %q to be invalid", invalid)
		}
	}
}

func TestThemeFragmentNonEmpty(t *testing.T) {
	for _, theme := range []Theme{ThemeInterview, ThemeDebate, ThemeNewsRoundup, ThemeDeepDive, ThemeSoloEssay} {
		frag := ThemeFragment(theme)
		if len(frag) < 100 {
			t.Errorf("theme %q fragment too short: %q", theme, frag)
		}
	}
}

func TestSystemPromptIncludesSpeakers(t *testing.T) {
	got := SystemPromptForScript(ThemeInterview, []string{"Alex", "Jordan"}, 8)
	if !contains(got, `"Alex"`) || !contains(got, `"Jordan"`) {
		t.Errorf("system prompt missing speaker names: %q", got)
	}
	// 8 min × 150 wpm = 1200 word target
	if !contains(got, "1200") {
		t.Errorf("system prompt missing word target: %q", got)
	}
}

func TestParseScriptJSON(t *testing.T) {
	t.Run("raw JSON", func(t *testing.T) {
		turns, err := parseScriptJSON(`[{"speaker":"A","text":"hi"},{"speaker":"B","text":"hello"}]`)
		if err != nil {
			t.Fatal(err)
		}
		if len(turns) != 2 || turns[0].Speaker != "A" || turns[1].Text != "hello" {
			t.Errorf("parsed = %+v", turns)
		}
	})

	t.Run("fenced JSON", func(t *testing.T) {
		raw := "```json\n" + `[{"speaker":"A","text":"hi"}]` + "\n```"
		turns, err := parseScriptJSON(raw)
		if err != nil {
			t.Fatal(err)
		}
		if len(turns) != 1 {
			t.Errorf("parsed = %+v", turns)
		}
	})

	t.Run("preamble + JSON fallback", func(t *testing.T) {
		raw := "Sure, here's the script:\n[{\"speaker\":\"A\",\"text\":\"yo\"}]"
		turns, err := parseScriptJSON(raw)
		if err != nil {
			t.Fatal(err)
		}
		if len(turns) != 1 || turns[0].Speaker != "A" {
			t.Errorf("parsed = %+v", turns)
		}
	})

	t.Run("malformed errors", func(t *testing.T) {
		_, err := parseScriptJSON("not json at all")
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("bare single object → one-turn script (Tier C fallback)", func(t *testing.T) {
		// Empirically found 2026-06-14: gpt-oss-120b:free sometimes
		// emits ONE bare object instead of [{...}] when the brief
		// maps to a single-narrator concept. Semantically a one-turn
		// script IS valid; the missing array wrapping was a parser
		// gap, not a content gap.
		raw := `{"speaker":"Host","text":"Kernel rootkits. They're the boogeyman of cybersecurity..."}`
		turns, err := parseScriptJSON(raw)
		if err != nil {
			t.Fatalf("expected single-object fallback to succeed, got %v", err)
		}
		if len(turns) != 1 {
			t.Fatalf("expected 1 turn, got %d", len(turns))
		}
		if turns[0].Speaker != "Host" {
			t.Errorf("speaker = %q, want Host", turns[0].Speaker)
		}
		if !contains(turns[0].Text, "Kernel rootkits") {
			t.Errorf("text didn't survive parse: %q", turns[0].Text)
		}
	})

	t.Run("bare single object with preamble (Tier C fallback)", func(t *testing.T) {
		// Tier C variation: prose preamble then a bare object.
		raw := "Here's the script: " + `{"speaker":"Narrator","text":"Today we explore eBPF observability."}`
		turns, err := parseScriptJSON(raw)
		if err != nil {
			t.Fatalf("expected preamble + bare-object fallback to succeed, got %v", err)
		}
		if len(turns) != 1 || turns[0].Speaker != "Narrator" {
			t.Errorf("parsed = %+v", turns)
		}
	})

	t.Run("fenced single object (Tier C fallback)", func(t *testing.T) {
		raw := "```json\n" + `{"speaker":"Host","text":"hi"}` + "\n```"
		turns, err := parseScriptJSON(raw)
		if err != nil {
			t.Fatalf("expected fenced bare-object fallback, got %v", err)
		}
		if len(turns) != 1 || turns[0].Text != "hi" {
			t.Errorf("parsed = %+v", turns)
		}
	})
}

func TestCoverPromptForScript(t *testing.T) {
	turns := []Turn{
		{Speaker: "Alex", Text: "Today we're diving into WebAssembly performance benchmarks."},
	}
	prompt := CoverPromptForScript(ThemeDeepDive, []string{"Alex", "Jordan"}, turns)
	if !contains(prompt, "Alex") || !contains(prompt, "Jordan") {
		t.Errorf("expected speakers in prompt: %q", prompt)
	}
	if !contains(prompt, "WebAssembly") {
		t.Errorf("expected hook in prompt: %q", prompt)
	}
	if !contains(prompt, "1024x1024") {
		t.Errorf("expected square-format hint: %q", prompt)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
