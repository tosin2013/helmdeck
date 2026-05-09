// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// podcast_generate.go — produce a multi-speaker podcast MP3 from
// either a script (mode A), a prompt (mode B), or long-form content
// (mode C — source_url or source_text). Day 1 ships ElevenLabs as
// the only TTS engine; the pack delegates to internal/podcast/ for
// engine routing, script generation, and ffmpeg concat — so future
// PRs can add PlayHT/Hume/Resemble engines without touching this
// handler.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/podcast"
	"github.com/tosin2013/helmdeck/internal/security"
	"github.com/tosin2013/helmdeck/internal/session"
	"github.com/tosin2013/helmdeck/internal/vault"
	"github.com/tosin2013/helmdeck/internal/vision"
)

const (
	defaultPodcastTheme       = "deep-dive"
	defaultPodcastDurationMin = 8
	defaultPodcastSilenceMs   = 600
	// defaultMinTurnDurationS (#141) is the per-turn duration floor
	// applied when min_turn_duration_s isn't specified. Matches the
	// 5s slides.narrate house style. Pass min_turn_duration_s:0
	// explicitly to opt out and preserve raw TTS pacing.
	defaultMinTurnDurationS = 5.0
)

// zeroFloorOptedIn checks whether the caller passed
// "min_turn_duration_s": 0 explicitly (in which case they want the
// no-floor behavior) versus omitting the field (in which case JSON
// gives us 0 too but they meant "default please"). Distinguishing
// the two is the difference between a back-compat preserving default
// and a confusing breaking change.
func zeroFloorOptedIn(raw json.RawMessage) bool {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	_, present := probe["min_turn_duration_s"]
	return present
}

// PodcastGenerate constructs the podcast.generate pack. v supplies
// the ElevenLabs API key; eg is the egress guard used to validate
// source_url before scraping; d is the gateway dispatcher needed for
// modes B and C (script generation via LLM). Pass nil for d when
// the gateway isn't wired — body mode (A) still works; modes B/C
// return CodeInternal at handler time.
func PodcastGenerate(v *vault.Store, eg *security.EgressGuard, d vision.Dispatcher) *packs.Pack {
	return &packs.Pack{
		Name:        "podcast.generate",
		Version:     "v1",
		Description: "Multi-speaker podcast (1..N) → MP3 artifact via pluggable TTS engine. Day 1: ElevenLabs. Requires HELMDECK_ELEVENLABS_API_KEY in .env.local (auto-hydrated to vault as 'elevenlabs-key'); pass allow_silent_output:true to produce a silence-padded MP3 when no key is configured (CI smoke / demo placeholder).",
		NeedsSession:    true,
		PreserveSession: false,
		SessionSpec: session.Spec{
			MemoryLimit: "2g",
		},
		Async: true, // multi-turn synthesis is 30s-3min; SEP-1686 envelope keeps callers responsive
		InputSchema: packs.BasicSchema{
			Required: []string{"speakers"},
			Properties: map[string]string{
				"engine":                    "string",
				"script":                    "array",
				"prompt":                    "string",
				"model":                     "string",
				"max_tokens":                "number",
				"source_url":                "string",
				"source_text":               "string",
				"speakers":                  "object",
				"model_id":                  "string",
				"theme":                     "string",
				"duration_target_min":       "number",
				"silence_between_turns_ms":  "number",
				"min_turn_duration_s":       "number",
				"generate_cover_prompt":     "boolean",
				"credential":                "string",
				"allow_silent_output":       "boolean",
				"dry_run":                   "boolean",
				"plan":                      "string",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"engine", "audio_artifact_key", "audio_size", "speaker_count", "turn_count", "script_source", "has_narration"},
			Properties: map[string]string{
				"engine":             "string",
				"audio_artifact_key": "string",
				"audio_size":         "number",
				"duration_s":         "number",
				"speaker_count":      "number",
				"turn_count":         "number",
				"script_source":      "string",
				"model_used":         "string",
				"voices_used":        "object",
				"has_narration":      "boolean",
				"theme":              "string",
				"cover_image_prompt": "string",
			},
		},
		Handler: podcastGenerateHandler(v, eg, d),
	}
}

type podcastGenerateInput struct {
	Engine                 string            `json:"engine"`
	Script                 []podcast.Turn    `json:"script"`
	Prompt                 string            `json:"prompt"`
	Model                  string            `json:"model"`
	MaxTokens              int               `json:"max_tokens"`
	SourceURL              string            `json:"source_url"`
	SourceText             string            `json:"source_text"`
	Speakers               map[string]string `json:"speakers"`
	ModelID                string            `json:"model_id"`
	Theme                  string            `json:"theme"`
	DurationTargetMin      int               `json:"duration_target_min"`
	SilenceBetweenTurnsMs  int               `json:"silence_between_turns_ms"`
	MinTurnDurationS       float64           `json:"min_turn_duration_s"`
	GenerateCoverPrompt    bool              `json:"generate_cover_prompt"`
	Credential             string            `json:"credential"`
	AllowSilentOutput      bool              `json:"allow_silent_output"`
	DryRun                 bool              `json:"dry_run"`
	Plan                   string            `json:"plan"`
}

func podcastGenerateHandler(v *vault.Store, eg *security.EgressGuard, d vision.Dispatcher) packs.HandlerFunc {
	return func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
		var in podcastGenerateInput
		if err := json.Unmarshal(ec.Input, &in); err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
		}

		// 1. Schema validation.
		if len(in.Speakers) == 0 {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: "speakers map is required (at least one speaker → voice_id entry)"}
		}
		theme := in.Theme
		if theme == "" {
			theme = defaultPodcastTheme
		}
		if !podcast.ValidTheme(theme) {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: fmt.Sprintf(`theme must be one of: interview, debate, news-roundup, deep-dive, solo-essay (got %q)`, theme)}
		}
		// Engine: closed set day 1.
		engineName := in.Engine
		if engineName == "" {
			engineName = "elevenlabs"
		}
		if engineName != "elevenlabs" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: fmt.Sprintf(`engine must be "elevenlabs" (got %q); other engines ship in future PRs`, engineName)}
		}

		// 2. Mode validation (XOR over the three script-source modes).
		hasScript := len(in.Script) > 0
		hasPrompt := strings.TrimSpace(in.Prompt) != ""
		hasSource := strings.TrimSpace(in.SourceURL) != "" || strings.TrimSpace(in.SourceText) != ""
		modes := 0
		for _, b := range []bool{hasScript, hasPrompt, hasSource} {
			if b {
				modes++
			}
		}
		if modes == 0 {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: "must provide one of: script | prompt+model | source_url/source_text+model"}
		}
		if modes > 1 {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: "must provide exactly one of: script | prompt+model | source_url/source_text+model — got multiple"}
		}
		if (hasPrompt || hasSource) && strings.TrimSpace(in.Model) == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: "model is required when using prompt or source_url/source_text mode"}
		}
		if hasSource && in.SourceURL != "" && os.Getenv("HELMDECK_FIRECRAWL_ENABLED") != "true" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: "source_url mode requires Firecrawl overlay (HELMDECK_FIRECRAWL_ENABLED=true)"}
		}

		if ec.Exec == nil {
			return nil, &packs.PackError{Code: packs.CodeSessionUnavailable,
				Message: "engine has no session executor (podcast.generate runs ffmpeg in a sidecar)"}
		}

		// 3. Resolve script.
		var (
			script       []podcast.Turn
			scriptSource string
			modelUsed    string
		)
		switch {
		case hasScript:
			script = in.Script
			scriptSource = "input"
		case hasPrompt:
			if d == nil {
				return nil, &packs.PackError{Code: packs.CodeInternal,
					Message: "podcast.generate prompt mode registered without a gateway dispatcher"}
			}
			ec.Report(10, "generating script via gateway LLM")
			s, err := podcast.GenerateScript(ctx, d, in.Model, podcast.Theme(theme),
				speakerNames(in.Speakers), defaultIfZero(in.DurationTargetMin, defaultPodcastDurationMin),
				defaultIfZero(in.MaxTokens, 4096), in.Prompt)
			if err != nil {
				return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
					Message: err.Error(), Cause: err}
			}
			script = s
			scriptSource = "model"
			modelUsed = in.Model
		case hasSource:
			if d == nil {
				return nil, &packs.PackError{Code: packs.CodeInternal,
					Message: "podcast.generate source_* mode registered without a gateway dispatcher"}
			}
			sourceText := in.SourceText
			if in.SourceURL != "" {
				ec.Report(5, "scraping source_url via Firecrawl")
				if eg != nil {
					if err := eg.CheckURL(ctx, in.SourceURL); err != nil {
						return nil, &packs.PackError{Code: packs.CodeInvalidInput,
							Message: fmt.Sprintf("egress denied: %v", err), Cause: err}
					}
				}
				txt, err := scrapeSourceURL(ctx, in.SourceURL)
				if err != nil {
					return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
						Message: fmt.Sprintf("scrape source_url: %v", err), Cause: err}
				}
				sourceText = txt
			}
			ec.Report(15, "generating script from long-form content")
			userMsg := "Convert the following content into a podcast script.\n\nCONTENT:\n" + sourceText
			s, err := podcast.GenerateScript(ctx, d, in.Model, podcast.Theme(theme),
				speakerNames(in.Speakers), defaultIfZero(in.DurationTargetMin, defaultPodcastDurationMin),
				defaultIfZero(in.MaxTokens, 4096), userMsg)
			if err != nil {
				return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
					Message: err.Error(), Cause: err}
			}
			script = s
			if in.SourceURL != "" {
				scriptSource = "source_url"
			} else {
				scriptSource = "source_text"
			}
			modelUsed = in.Model
		}

		voicesUsed, verr := podcast.ValidateScript(script, in.Speakers)
		if verr != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: verr.Error(), Cause: verr}
		}

		// 3b. Cost accounting (#145). Char counts feed both the
		// dry_run short-circuit AND the regular response — operators
		// always see what their last call cost without having to
		// re-do the math against ElevenLabs' billing dashboard.
		ttsChars, ttsCharsTotal := computeTTSChars(script)
		estimateUSD, estimateBreakdown := podcast.EstimateElevenLabs(ttsCharsTotal, in.Plan)

		if in.DryRun {
			// dry_run intentionally runs BEFORE credential resolve so
			// operators on a Free tier can preview cost even without
			// a paid ElevenLabs key configured.
			out := map[string]any{
				"engine":                   engineName,
				"dry_run":                  true,
				"script":                   script,
				"speaker_count":            len(in.Speakers),
				"turn_count":               len(script),
				"script_source":            scriptSource,
				"model_used":               modelUsed,
				"voices_used":              voicesUsedMap(voicesUsed, in.Speakers),
				"theme":                    theme,
				"tts_chars":                ttsChars,
				"estimated_cost_usd":       estimateUSD,
				"estimated_cost_breakdown": estimateBreakdown,
			}
			raw, mErr := json.Marshal(out)
			if mErr != nil {
				return nil, &packs.PackError{Code: packs.CodeInternal, Message: mErr.Error(), Cause: mErr}
			}
			return raw, nil
		}

		// 4. Resolve credential through the shared #138 ladder:
		// explicit input → vault:elevenlabs-key → vault:elevenlabs-api-key
		// → env:HELMDECK_ELEVENLABS_API_KEY. Per #138 we now hard-fail
		// rather than silently produce silence, unless the caller
		// explicitly opted in to the silent path via allow_silent_output.
		apiKey, keySrc := resolveElevenLabsKey(ctx, v, in.Credential)
		if apiKey == "" {
			if !in.AllowSilentOutput {
				return nil, &packs.PackError{
					Code:    packs.CodeInvalidInput,
					Message: elevenLabsMissingCredentialMessage,
				}
			}
			ec.Logger.Warn("podcast.generate: no ElevenLabs key resolved; producing silence (allow_silent_output=true)",
				"explicit_credential", in.Credential)
		} else {
			ec.Logger.Info("podcast.generate: resolved ElevenLabs key", "source", keySrc)
		}
		hasNarration := apiKey != ""

		// 5. Pick engine.
		eng, err := podcast.PickEngine(engineName, apiKey)
		if err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: err.Error(), Cause: err}
		}

		// 6. Synthesize each turn (or generate silence per turn when key missing).
		ec.Report(30, fmt.Sprintf("synthesizing %d turns via %s", len(script), eng.Name()))
		ex := podcastExecutorAdapter{ec: ec}

		mp3Turns := make([][]byte, 0, len(script))
		for i, turn := range script {
			voiceID := in.Speakers[turn.Speaker]
			modelID := in.ModelID
			var mp3 []byte
			if hasNarration {
				m, err := eng.Synthesize(ctx, turn, podcast.SynthesizeOptions{
					VoiceID: voiceID,
					ModelID: modelID,
				})
				if errors.Is(err, podcast.ErrNoAPIKey) {
					hasNarration = false // mid-loop API key disappearance — degrade
				} else if err != nil {
					return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
						Message: fmt.Sprintf("synthesize turn %d: %v", i, err), Cause: err}
				} else {
					mp3 = m
				}
			}
			if !hasNarration {
				// Silent fallback: 5 seconds of silence per turn so
				// the podcast structure is preserved even without TTS.
				silent, err := podcast.SilenceTurn(ctx, ex, ec.Session.ID, 5.0)
				if err != nil {
					return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
						Message: fmt.Sprintf("silent fallback turn %d: %v", i, err), Cause: err}
				}
				mp3 = silent
			}
			mp3Turns = append(mp3Turns, mp3)
			ec.Report(30+60*float64(i+1)/float64(len(script)),
				fmt.Sprintf("synthesized %d/%d turns", i+1, len(script)))
		}

		// 7. Concat into final MP3.
		ec.Report(92, "ffmpeg concat")
		silenceMs := in.SilenceBetweenTurnsMs
		if silenceMs <= 0 {
			silenceMs = defaultPodcastSilenceMs
		}
		// #141: per-turn floor. nil/zero means "use the default 5s
		// floor"; setting to <0 (encoded as in.MinTurnDurationS == 0
		// after json default) preserves today's no-floor behavior.
		// Operators who want raw TTS pacing pass min_turn_duration_s:
		// 0 explicitly via the dedicated opt-out — see schema doc.
		minTurnSec := in.MinTurnDurationS
		if minTurnSec == 0 && !zeroFloorOptedIn(ec.Input) {
			minTurnSec = defaultMinTurnDurationS
		}
		finalMP3, durationS, err := podcast.Concat(ctx, ex, ec.Session.ID, mp3Turns, podcast.ConcatOptions{
			SilenceBetweenTurnsMs: silenceMs,
			MinTurnDurationS:      minTurnSec,
		})
		if err != nil {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("concat: %v", err), Cause: err}
		}

		// 8. Upload artifact.
		if ec.Artifacts == nil {
			return nil, &packs.PackError{Code: packs.CodeInternal,
				Message: "podcast.generate requires an artifact store"}
		}
		art, err := ec.Artifacts.Put(ctx, ec.Pack.Name, "podcast.mp3", finalMP3, "audio/mpeg")
		if err != nil {
			return nil, &packs.PackError{Code: packs.CodeArtifactFailed,
				Message: err.Error(), Cause: err}
		}

		// 9. Build output.
		voicesMap := voicesUsedMap(voicesUsed, in.Speakers)
		out := map[string]any{
			"engine":             eng.Name(),
			"audio_artifact_key": art.Key,
			"audio_size":         art.Size,
			"duration_s":         durationS,
			"speaker_count":      len(voicesUsed),
			"turn_count":         len(script),
			"script_source":      scriptSource,
			"voices_used":        voicesMap,
			"has_narration":      hasNarration,
			"theme":              theme,
			// #145: cost transparency on real runs too — operators
			// log this to their accounting workflow without re-doing
			// the math against ElevenLabs' billing dashboard.
			"tts_chars":                ttsChars,
			"estimated_cost_usd":       estimateUSD,
			"estimated_cost_breakdown": estimateBreakdown,
		}
		if modelUsed != "" {
			out["model_used"] = modelUsed
		}
		if in.GenerateCoverPrompt {
			out["cover_image_prompt"] = podcast.CoverPromptForScript(podcast.Theme(theme), voicesUsed, script)
		}
		return json.Marshal(out)
	}
}

// computeTTSChars sums the per-turn text length per speaker and
// returns the per-speaker map (with a "_total" key) plus the total.
// Used by both the dry_run short-circuit and the regular response.
func computeTTSChars(script []podcast.Turn) (map[string]int, int) {
	per := map[string]int{}
	total := 0
	for _, t := range script {
		n := len(t.Text)
		per[t.Speaker] += n
		total += n
	}
	per["_total"] = total
	return per, total
}

// voicesUsedMap renders the script's distinct-speakers list as a
// {speaker: voice_id} map. Pulled into a helper so the dry_run path
// and the regular response build it identically.
func voicesUsedMap(voicesUsed []string, speakers map[string]string) map[string]string {
	out := make(map[string]string, len(voicesUsed))
	for _, name := range voicesUsed {
		out[name] = speakers[name]
	}
	return out
}

// podcastExecutorAdapter wraps ExecutionContext.Exec into a
// session.Executor so internal/podcast/concat.go can call it. Same
// shape as the executorAdapter in vision_packs.go.
type podcastExecutorAdapter struct {
	ec *packs.ExecutionContext
}

func (a podcastExecutorAdapter) Exec(ctx context.Context, _ string, req session.ExecRequest) (session.ExecResult, error) {
	return a.ec.Exec(ctx, req)
}

func speakerNames(m map[string]string) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

func defaultIfZero(v, fallback int) int {
	if v <= 0 {
		return fallback
	}
	return v
}

// scrapeSourceURL fetches a URL via Firecrawl /v1/scrape and returns
// the scraped markdown. Mirrors web_scrape.go's pattern, kept inline
// to avoid an import cycle (web_scrape is a builtin pack, podcast is
// also a builtin, but Go package-internal helpers don't cross files
// neatly without a separate helper file).
//
// Trimmed-down version: we only need markdown; no formats array, no
// wait_ms, no error-shape niceties — the caller already handles
// errors.
func scrapeSourceURL(ctx context.Context, url string) (string, error) {
	base := strings.TrimRight(os.Getenv("HELMDECK_FIRECRAWL_URL"), "/")
	if base == "" {
		base = "http://firecrawl-api:3002"
	}
	body, _ := json.Marshal(map[string]any{
		"url":     url,
		"formats": []string{"markdown"},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/scrape", strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("firecrawl %d: %s", resp.StatusCode, truncateString(string(respBody), 256))
	}
	var parsed struct {
		Data struct {
			Markdown string `json:"markdown"`
		} `json:"data"`
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("parse firecrawl response: %w", err)
	}
	if !parsed.Success {
		return "", fmt.Errorf("firecrawl: %s", parsed.Error)
	}
	if parsed.Data.Markdown == "" {
		return "", fmt.Errorf("firecrawl returned empty markdown for %s", url)
	}
	return parsed.Data.Markdown, nil
}
