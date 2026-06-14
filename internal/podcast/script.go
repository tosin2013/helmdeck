// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package podcast

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/tosin2013/helmdeck/internal/gateway"
	"github.com/tosin2013/helmdeck/internal/vision"
)

// ValidateScript checks that every speaker referenced in the script
// exists in the speakers map. Returns the unique speakers seen (in
// stable order) for the response's voices_used field.
//
// Returns CodeInvalidInput-flavored errors that the pack handler
// surfaces directly.
func ValidateScript(script []Turn, speakers map[string]string) ([]string, error) {
	if len(script) == 0 {
		return nil, fmt.Errorf("script is empty")
	}
	if len(speakers) == 0 {
		return nil, fmt.Errorf("speakers map is empty")
	}
	seen := map[string]bool{}
	for i, t := range script {
		if strings.TrimSpace(t.Speaker) == "" {
			return nil, fmt.Errorf("script[%d]: speaker is empty", i)
		}
		if strings.TrimSpace(t.Text) == "" {
			return nil, fmt.Errorf("script[%d]: text is empty", i)
		}
		if _, ok := speakers[t.Speaker]; !ok {
			return nil, fmt.Errorf("script[%d]: speaker %q not in speakers map (configured: %s)",
				i, t.Speaker, speakerKeysSorted(speakers))
		}
		seen[t.Speaker] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

func speakerKeysSorted(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

// GenerateScript calls the gateway LLM with a theme-aware system
// prompt and parses the response into a structured script. Used by
// modes B (prompt) and C (source_url/source_text).
//
// The LLM is instructed to emit ONLY a JSON array. We accept both
// raw JSON and JSON wrapped in a ```json fence (some models add
// fences despite the instruction). The retry-on-malformed loop is
// kept simple: parse once, fall back to extracting the first
// balanced [...] block, give up on second failure.
func GenerateScript(
	ctx context.Context,
	d vision.Dispatcher,
	model string,
	theme Theme,
	speakerNames []string,
	durationMin int,
	maxTokens int,
	userPrompt string,
) ([]Turn, error) {
	if d == nil {
		return nil, errors.New("no gateway dispatcher (script generation requires LLM)")
	}
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	system := SystemPromptForScript(theme, speakerNames, durationMin)

	resp, err := d.Dispatch(ctx, gateway.ChatRequest{
		Model:     model,
		MaxTokens: &maxTokens,
		Messages: []gateway.Message{
			{Role: "system", Content: gateway.TextContent(system)},
			{Role: "user", Content: gateway.TextContent(userPrompt)},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("script generation dispatch: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, errors.New("script generation: model returned no choices")
	}
	raw := strings.TrimSpace(resp.Choices[0].Message.Content.Text())
	if raw == "" {
		return nil, errors.New("script generation: model returned empty text")
	}

	turns, err := parseScriptJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("script generation: %w (raw: %s)", err, truncateForErr(raw, 256))
	}
	if len(turns) == 0 {
		return nil, errors.New("script generation: model returned empty script")
	}
	return turns, nil
}

// parseScriptJSON decodes the model's response into []Turn. Tolerates:
//   - a leading "```json\n" / trailing "\n```" fence
//   - a leading/trailing prose preamble that's followed by a JSON array
//   - a bare single object (one-turn script wrapped to []Turn{turn})
//     — Tier C models (gpt-oss-120b:free, gemma) sometimes emit ONE
//     object instead of an array when the description maps to a short
//     single-narrator brief. Semantically a one-turn script IS valid;
//     only the array wrapping is missing.
func parseScriptJSON(raw string) ([]Turn, error) {
	clean := strings.TrimSpace(raw)
	clean = strings.TrimPrefix(clean, "```json")
	clean = strings.TrimPrefix(clean, "```")
	clean = strings.TrimSuffix(clean, "```")
	clean = strings.TrimSpace(clean)

	var turns []Turn
	if err := json.Unmarshal([]byte(clean), &turns); err == nil {
		return turns, nil
	}
	// Fallback A: find the first balanced [ ... ] block.
	if start := strings.Index(clean, "["); start >= 0 {
		if end := strings.LastIndex(clean, "]"); end > start {
			if err := json.Unmarshal([]byte(clean[start:end+1]), &turns); err == nil {
				return turns, nil
			}
		}
	}
	// Fallback B: bare single object → one-turn script. Closes a Tier
	// C failure mode where the model emits {"speaker":"...","text":"..."}
	// instead of [{"speaker":"...","text":"..."}]. Found empirically
	// 2026-06-14 running gpt-oss-120b:free against a short-form
	// concept-animator prompt — the single-object response is
	// semantically a valid one-turn script, just missing the array
	// brackets.
	if start := strings.Index(clean, "{"); start >= 0 {
		if end := strings.LastIndex(clean, "}"); end > start {
			var single Turn
			if err := json.Unmarshal([]byte(clean[start:end+1]), &single); err == nil {
				return []Turn{single}, nil
			}
		}
	}
	return nil, errors.New("no JSON array (or single object) found in response")
}

func truncateForErr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// CoverPromptForScript synthesizes a one-paragraph image-generation
// prompt summarizing the podcast's content and theme. The pack
// handler returns this in the cover_image_prompt output when the
// agent set generate_cover_prompt:true. The agent then hands it to
// a future image.generate pack.
//
// We don't make a separate LLM call for this — we synthesize it
// from the theme + first turn's text + speaker names. Cheap and
// deterministic.
func CoverPromptForScript(theme Theme, speakers []string, script []Turn) string {
	hook := ""
	if len(script) > 0 {
		hook = script[0].Text
		if len(hook) > 200 {
			hook = hook[:197] + "..."
		}
	}
	visualHint := ""
	switch theme {
	case ThemeInterview:
		visualHint = "two figures in conversation across a small table, warm desk lamp, soft podcast-studio aesthetic"
	case ThemeDebate:
		visualHint = "two figures at facing podiums, contrasting lighting, neutral background, editorial photography style"
	case ThemeNewsRoundup:
		visualHint = "fast-paced editorial collage with bold typography placeholder, news-desk aesthetic"
	case ThemeDeepDive:
		visualHint = "single figure or duo with contemplative posture, library or office setting, minimal cool tones"
	case ThemeSoloEssay:
		visualHint = "single figure in close-up, soft window light, intimate documentary-portrait style"
	default:
		visualHint = "podcast cover art, editorial photography style"
	}
	speakerList := strings.Join(speakers, ", ")
	if len(speakerList) > 80 {
		speakerList = speakerList[:77] + "..."
	}
	return fmt.Sprintf(
		"Podcast cover art. Style: %s. Hosts: %s. Topic hook: %q. 1024x1024 square format. No on-image text — leave space for typography overlay.",
		visualHint, speakerList, hook)
}
