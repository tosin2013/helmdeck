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

	"github.com/tosin2013/helmdeck/internal/gateway"
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

	// JIT length-sizing constants (issue #528). Source word count
	// divided by this rate gives an estimated reading time in minutes;
	// the intent table multiplies that to pick a target podcast
	// duration. 150 wpm is the conventional narration speed used
	// elsewhere in the codebase (slides.narrate caption pacing).
	podcastReadingWPM        = 150.0
	podcastIntentSummary     = "summary"
	podcastIntentThorough    = "thorough"
	podcastIntentExhaustive  = "exhaustive"
	podcastIntentDefault     = podcastIntentThorough
)

// podcastIntentRow holds the per-intent sizing parameters. Multiplier
// scales the source's reading time (source_words / podcastReadingWPM)
// to a target podcast duration; floor and ceiling clamp at extremes
// so a tiny source still produces a usable episode and a huge source
// doesn't request a 30-minute podcast that ElevenLabs would charge
// dearly for.
type podcastIntentRow struct {
	multiplier float64
	floor      int
	ceiling    int
}

// podcastIntentTable mirrors blogRewriteIntentTable's shape; numbers
// per issue #528. Revisitable as empirical data lands.
var podcastIntentTable = map[string]podcastIntentRow{
	podcastIntentSummary:    {multiplier: 0.20, floor: 1, ceiling: 3},
	podcastIntentThorough:   {multiplier: 0.50, floor: 3, ceiling: 8},
	podcastIntentExhaustive: {multiplier: 0.90, floor: 6, ceiling: 12},
}

// podcastSize captures the chosen target for one call. Mirrors the
// blog pack's blogRewriteSize shape.
type podcastSize struct {
	chosen  int
	applied string // "intent:thorough" / "explicit" / "default:legacy-8min" / "n/a:script"
}

// sizeForPodcastIntent picks a target minute count from the intent
// table. Reading time floor: if sourceWords is 0 (prompt mode or
// inspect with no measurable source), the multiplier yields 0; floor
// clamping rescues it to a usable minimum.
func sizeForPodcastIntent(sourceWords int, intent string) podcastSize {
	key := strings.ToLower(strings.TrimSpace(intent))
	if key == "" {
		key = podcastIntentDefault
	}
	row, ok := podcastIntentTable[key]
	if !ok {
		row = podcastIntentTable[podcastIntentDefault]
		key = podcastIntentDefault
	}
	readingMin := float64(sourceWords) / podcastReadingWPM
	chosen := int(readingMin * row.multiplier)
	if chosen < row.floor {
		chosen = row.floor
	}
	if chosen > row.ceiling {
		chosen = row.ceiling
	}
	return podcastSize{chosen: chosen, applied: "intent:" + key}
}

// resolvePodcastSize encodes the precedence: explicit DurationTargetMin
// > LengthIntent > legacy default (8 min). The legacy fallback keeps
// existing callers' behavior identical when they pass neither a
// numeric duration nor an intent — critical for back-compat per the
// issue #528 acceptance criteria.
func resolvePodcastSize(sourceWords int, in *podcastGenerateInput) podcastSize {
	if in.DurationTargetMin > 0 {
		return podcastSize{chosen: in.DurationTargetMin, applied: "explicit"}
	}
	if strings.TrimSpace(in.LengthIntent) != "" {
		return sizeForPodcastIntent(sourceWords, in.LengthIntent)
	}
	return podcastSize{chosen: defaultPodcastDurationMin, applied: "default:legacy-8min"}
}

// countSourceWordsForPodcast measures the source content available to
// the pack for sizing purposes. Per mode:
//   - script mode: sum of word counts across all turns (script IS the
//     source; useful for inspect-mode "what duration will this run for")
//   - source_text mode: count of source_text
//   - source_url mode: 0 at handler-entry; replaced with the scraped
//     text's count after the Firecrawl scrape lands (real-path only)
//   - prompt mode: 0 (the prompt is a planning instruction, not source)
//
// Returns 0 when no measurable source is available. The size resolver
// treats 0 sensibly via the row floor.
func countSourceWordsForPodcast(in *podcastGenerateInput) int {
	if len(in.Script) > 0 {
		total := 0
		for _, t := range in.Script {
			total += countWords(t.Text)
		}
		return total
	}
	if s := strings.TrimSpace(in.SourceText); s != "" {
		return countWords(s)
	}
	return 0
}

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
		Metadata: packs.PackMetadata{
			Accepts:        []string{"source_text", "source_url", "prompt", "markdown"},
			Produces:       []string{"mp3", "podcast_script"},
			IntentKeywords: []string{"make podcast", "audio narration", "multi-speaker dialogue", "voice over", "short podcast", "thorough podcast", "exhaustive podcast"},
			TypicalUse:     "Generator pack — turns source text or a prompt into a multi-speaker podcast. ElevenLabs by default. Use length_intent (summary / thorough / exhaustive) to scale duration to the source size, or pass duration_target_min for an explicit numeric override.",
			Limitations:    []string{"requires vault credential 'elevenlabs-key' (or allow_silent_output:true)", "does not produce video — pair with hyperframes.render or slides.narrate for a/v output", "voice selection is per-speaker — discover IDs via helmdeck://voices", "truncated:true signals the script-generation model hit max_tokens — re-run with a smaller length_intent or larger max_tokens"},
		},
		NeedsSession:    true,
		PreserveSession: false,
		SessionSpec: session.Spec{
			MemoryLimit: "2g",
		},
		Async: true, // multi-turn synthesis is 30s-3min; SEP-1686 envelope keeps callers responsive
		InputSchema: packs.BasicSchema{
			Required: []string{"speakers"},
			Properties: map[string]string{
				"engine":                   "string",
				"script":                   "array",
				"prompt":                   "string",
				"model":                    "string",
				"max_tokens":               "number",
				"source_url":               "string",
				"source_text":              "string",
				"speakers":                 "object",
				"model_id":                 "string",
				"theme":                    "string",
				"duration_target_min":      "number",
				"silence_between_turns_ms": "number",
				"min_turn_duration_s":      "number",
				"generate_cover_prompt":    "boolean",
				"cover_image":              "boolean",
				"cover_image_model":        "string",
				"credential":               "string",
				"allow_silent_output":      "boolean",
				"dry_run":                  "boolean",
				"plan":                     "string",
				// Engagement metadata knobs (default-on; pass
				// metadata_model:"" to disable). cta_style is
				// {natural,direct,none}. language is an ISO 639 code.
				"metadata_model": "string",
				"cta_style":      "string",
				"language":       "string",
				// validate (default-on, pointer-bool) runs av.validate
				// as a post-concat step. Phase 3 of the validation arc.
				// Mirrors slides.narrate's validate input.
				"validate": "boolean",
				// JIT length-sizing (issue #528). Declarative intent
				// scales duration_target_min from source word count.
				"length_intent": "string",
				"inspect":       "boolean",
			},
		},
		OutputSchema: packs.BasicSchema{
			// Required deliberately stays narrow: inspect-mode short-
			// circuits before any of these are populated. The engine
			// schema validator only checks Required for the pure
			// happy-path generate response.
			Required: []string{"engine"},
			Properties: map[string]string{
				"engine":                   "string",
				"audio_artifact_key":       "string",
				"audio_url":                "string",
				"audio_size":               "number",
				"duration_s":               "number",
				"speaker_count":            "number",
				"turn_count":               "number",
				"script_source":            "string",
				"model_used":               "string",
				"voices_used":              "object",
				"has_narration":            "boolean",
				"theme":                    "string",
				"cover_image_prompt":       "string",
				"cover_image_artifact_key": "string",
				"cover_image_model_used":   "string",
				// Engagement object follows Apple Podcasts +
				// Podcasting 2.0 conventions: title, subtitle, summary,
				// show_notes_md, chapters (JSON-chapters shape, NOT ID3),
				// hook_30s, cta, language, format_ceiling_note.
				"engagement":              "object",
				"engagement_artifact_key": "string",
				// validation is the av.validate structured report
				// inlined as a post-concat step (Phase 3). Shape
				// mirrors av.validate's output. Present when validate
				// is not explicitly false.
				"validation":              "object",
				"validation_artifact_key": "string",
				// Cost transparency — emitted by the handler; declared
				// here so agents/pipeline authors see them in the catalog.
				// tts_chars is a per-speaker breakdown map with a "_total"
				// key (see computeTTSChars) — an object, not a number.
				// Declaring it "number" shipped a real invalid_output
				// failure on every pipeline run in v0.17.1.
				"tts_chars":                "object",
				"estimated_cost_usd":       "number",
				"estimated_cost_breakdown": "object",
				// JIT length-sizing (issue #528). Reported on every
				// generate response so callers can see what scale they
				// got; inspect mode emits a subset.
				"source_words":               "number",
				"target_duration_min_chosen": "number",
				"actual_duration_min":        "number",
				"length_intent_applied":      "string",
				"truncated":                  "boolean",
				// Inspect mode only.
				"inspect":               "boolean",
				"suggested_duration_min": "number",
				"reason":                "string",
			},
		},
		Handler: podcastGenerateHandler(v, eg, d),
	}
}

type podcastGenerateInput struct {
	Engine                string            `json:"engine"`
	Script                []podcast.Turn    `json:"script"`
	Prompt                string            `json:"prompt"`
	Model                 string            `json:"model"`
	MaxTokens             int               `json:"max_tokens"`
	SourceURL             string            `json:"source_url"`
	SourceText            string            `json:"source_text"`
	Speakers              map[string]string `json:"speakers"`
	ModelID               string            `json:"model_id"`
	Theme                 string            `json:"theme"`
	DurationTargetMin     int               `json:"duration_target_min"`
	SilenceBetweenTurnsMs int               `json:"silence_between_turns_ms"`
	MinTurnDurationS      float64           `json:"min_turn_duration_s"`
	GenerateCoverPrompt   bool              `json:"generate_cover_prompt"`
	CoverImage            bool              `json:"cover_image"`
	CoverImageModel       string            `json:"cover_image_model"`
	Credential            string            `json:"credential"`
	AllowSilentOutput     bool              `json:"allow_silent_output"`
	DryRun                bool              `json:"dry_run"`
	Plan                  string            `json:"plan"`
	// Engagement metadata fields. metadata_model is a string-ptr-shaped
	// boolean: nil/absent → default to "openrouter/auto" (default-on per
	// the engagement plan); empty string ("") → operator explicitly
	// disabled engagement gen; non-empty → use the named model.
	// CTAStyle: {natural,direct,none}. Empty → natural.
	MetadataModelRaw *string `json:"metadata_model"`
	CTAStyle         string  `json:"cta_style"`
	Language         string  `json:"language"`
	// Validate (default-on, pointer-bool) runs av.validate as a
	// post-concat step. nil → on, &false → off. Mirrors slides.narrate's
	// pattern. Phase 3 of the validation arc.
	Validate *bool `json:"validate,omitempty"`

	// JIT length-sizing (issue #528 / convention #525). LengthIntent
	// declares "summary" / "thorough" / "exhaustive"; the pack
	// measures the source and picks a duration_target_min from the
	// intent table. Precedence: DurationTargetMin > LengthIntent >
	// legacy 8-min default (back-compat preserved when neither set).
	LengthIntent string `json:"length_intent,omitempty"`
	// Inspect: pack measures source (when available — script text or
	// source_text), returns the suggested duration, and does NOT call
	// the script-generation LLM. Useful when an agent wants to
	// negotiate length before committing. Does not scrape source_url.
	Inspect bool `json:"inspect,omitempty"`
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
		if (hasPrompt || hasSource) && strings.TrimSpace(in.Model) == "" && !in.Inspect {
			// Inspect short-circuits before the model is called, so
			// callers can plan in modes B/C without picking a model.
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: "model is required when using prompt or source_url/source_text mode"}
		}
		if hasSource && in.SourceURL != "" && os.Getenv("HELMDECK_FIRECRAWL_ENABLED") != "true" {
			// Inspect mode short-circuits before this check (no
			// scrape happens), so source_url + inspect is allowed
			// without HELMDECK_FIRECRAWL_ENABLED.
			if !in.Inspect {
				return nil, &packs.PackError{Code: packs.CodeInvalidInput,
					Message: "source_url mode requires Firecrawl overlay (HELMDECK_FIRECRAWL_ENABLED=true)"}
			}
		}

		// JIT length-sizing: inspect short-circuit (issue #528). Runs
		// before the executor / dispatcher / vault checks so a
		// gateway-less or session-less environment can still plan a
		// podcast. Measures source content when available (script text
		// or source_text); for prompt / source_url modes the
		// suggestion is based on the intent floor.
		if in.Inspect {
			sourceWords := countSourceWordsForPodcast(&in)
			// Intent defaults to "thorough" for inspect-mode
			// reporting — caller can override.
			intent := strings.TrimSpace(in.LengthIntent)
			if intent == "" {
				intent = podcastIntentDefault
			}
			size := sizeForPodcastIntent(sourceWords, intent)
			reason := fmt.Sprintf("source is %d words; applying %s for a target of %d minutes (floor/ceiling clamped)",
				sourceWords, size.applied, size.chosen)
			if sourceWords == 0 {
				switch {
				case hasPrompt:
					reason = fmt.Sprintf("prompt mode has no measurable source; applying %s picks intent floor at %d minutes",
						size.applied, size.chosen)
				case in.SourceURL != "":
					reason = fmt.Sprintf("source_url not scraped in inspect mode; applying %s picks intent floor at %d minutes (call without inspect to measure scraped content)",
						size.applied, size.chosen)
				}
			}
			return json.Marshal(map[string]any{
				"engine":                 engineName,
				"inspect":                true,
				"source_words":           sourceWords,
				"suggested_duration_min": size.chosen,
				"length_intent_applied":  size.applied,
				"reason":                 reason,
			})
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
			finishReason string
			sourceWords  int
		)
		// Pre-resolve size for modes B/C. For modes whose source is
		// not yet in hand (source_url before scrape), sourceWords is
		// 0 and size falls back to intent-floor or legacy default.
		// Mode A (script) skips intent entirely — duration is fixed
		// by the script.
		size := resolvePodcastSize(countSourceWordsForPodcast(&in), &in)
		switch {
		case hasScript:
			script = in.Script
			scriptSource = "input"
			sourceWords = countSourceWordsForPodcast(&in)
			// Script mode has no intent decision to make; report
			// the script's intrinsic word count but flag the
			// applied path as "n/a:script" so callers can see why.
			size.applied = "n/a:script"
		case hasPrompt:
			if d == nil {
				return nil, &packs.PackError{Code: packs.CodeInternal,
					Message: "podcast.generate prompt mode registered without a gateway dispatcher"}
			}
			ec.Report(10, fmt.Sprintf("generating script via gateway LLM (target %d min, %s)", size.chosen, size.applied))
			s, fr, err := podcast.GenerateScript(ctx, d, in.Model, podcast.Theme(theme),
				speakerNames(in.Speakers), size.chosen,
				defaultIfZero(in.MaxTokens, 4096), in.Prompt)
			finishReason = fr
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
			// Re-measure source now that scrape (if any) is done,
			// and re-resolve size so source_url callers get a
			// content-sized target rather than the intent floor.
			sourceWords = countWords(sourceText)
			inWithScrape := in
			inWithScrape.SourceText = sourceText
			size = resolvePodcastSize(sourceWords, &inWithScrape)
			ec.Report(15, fmt.Sprintf("generating script from long-form content (target %d min, %s)", size.chosen, size.applied))
			userMsg := "Convert the following content into a podcast script.\n\nCONTENT:\n" + sourceText
			s, fr, err := podcast.GenerateScript(ctx, d, in.Model, podcast.Theme(theme),
				speakerNames(in.Speakers), size.chosen,
				defaultIfZero(in.MaxTokens, 4096), userMsg)
			finishReason = fr
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
				// JIT length-sizing telemetry (issue #528). Reported
				// even in dry_run so operators previewing cost also
				// see the chosen target + whether the generation
				// truncated.
				"source_words":               sourceWords,
				"target_duration_min_chosen": size.chosen,
				"length_intent_applied":      size.applied,
				"truncated":                  strings.EqualFold(finishReason, "length"),
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
		truncated := strings.EqualFold(finishReason, "length")
		actualDurationMin := durationS / 60
		out := map[string]any{
			"engine":             eng.Name(),
			"audio_artifact_key": art.Key,
			// #233/ADR 041: the presigned artifact URL so a pipeline can embed
			// it in a hyperframes.render composition's <audio src> (podcast →
			// narrated video). Always present (even "") so a downstream
			// ${{ steps.podcast.output.audio_url }} reference always resolves;
			// empty for the in-memory artifact store (dev/CI), set when an S3
			// store is configured, so the narrated-video pipeline degrades to a
			// silent video instead of failing on a missing reference.
			"audio_url":     art.URL,
			"audio_size":    art.Size,
			"duration_s":    durationS,
			"speaker_count": len(voicesUsed),
			"turn_count":    len(script),
			"script_source": scriptSource,
			"voices_used":   voicesMap,
			"has_narration": hasNarration,
			"theme":         theme,
			// #145: cost transparency on real runs too — operators
			// log this to their accounting workflow without re-doing
			// the math against ElevenLabs' billing dashboard.
			"tts_chars":                ttsChars,
			"estimated_cost_usd":       estimateUSD,
			"estimated_cost_breakdown": estimateBreakdown,
			// JIT length-sizing telemetry (issue #528). Always
			// emitted on the generate path so callers can compare
			// their chosen target against actual_duration_min and
			// detect truncation. For mode A (script), source_words
			// reflects the script's word count and applied is
			// "n/a:script".
			"source_words":               sourceWords,
			"target_duration_min_chosen": size.chosen,
			"actual_duration_min":        actualDurationMin,
			"length_intent_applied":      size.applied,
			"truncated":                  truncated,
		}
		if modelUsed != "" {
			out["model_used"] = modelUsed
		}

		// Validation (Phase 3 of the validation arc). Default-on via
		// the Validate pointer-bool. Audio-only validation: the
		// script's mp4:* and consistency:audio_video_duration checks
		// skip automatically (no --video), so only the audio:* and
		// (when supplied) srt:* checks run. Runs against the final
		// MP3 still in the sidecar tmpfs at /tmp/helmdeck-podcast/
		// final.mp3 (see internal/podcast/concat.go).
		//
		// Failure to RUN (script missing, dep error, transport failure)
		// is logged but does NOT fail the pack — the artifact still
		// ships. Validation findings (checks at any severity) similarly
		// never fail the pack; the caller reads validation.all_passed.
		// Matches the soft-surface contract from av.validate.
		validateOn := in.Validate == nil || *in.Validate
		if validateOn {
			ec.Report(97, "validating output")
			rep, key, verr := runAVValidation(ctx, ec, runAVValidationOpts{
				AudioPath:         "/tmp/helmdeck-podcast/final.mp3",
				ArtifactNamespace: "podcast.generate",
			})
			if verr != nil {
				ec.Logger.Warn("av.validate run failed; output ships without validation field",
					"err", verr)
			} else if len(rep.Checks) > 0 {
				out["validation"] = rep
				out["validation_artifact_key"] = key
			}
		}

		// Engagement metadata — default-on per the v0.26.0 plan
		// (slides.narrate stays opt-in for back-compat; podcast
		// defaults to "openrouter/auto"). Empty-string opt-out is
		// distinguished from nil-not-specified via *string in the
		// input struct.
		var engagementModel string
		switch {
		case in.MetadataModelRaw == nil:
			engagementModel = "openrouter/auto"
		case *in.MetadataModelRaw == "":
			engagementModel = "" // operator explicitly disabled
		default:
			engagementModel = *in.MetadataModelRaw
		}
		if d != nil && engagementModel != "" && len(script) > 0 {
			ec.Report(98, "generating engagement metadata")
			engagement, err := generatePodcastEngagement(ctx, d, engagementModel,
				theme, durationS, in.Speakers, script, in)
			if err != nil {
				ec.Logger.Warn("podcast engagement generation failed", "err", err)
			} else {
				mdBytes, _ := json.MarshalIndent(engagement, "", "  ")
				engArt, err := ec.Artifacts.Put(ctx, "podcast.generate", "engagement.json",
					mdBytes, "application/json")
				if err != nil {
					ec.Logger.Warn("engagement artifact upload failed", "err", err)
				} else {
					out["engagement_artifact_key"] = engArt.Key
				}
				out["engagement"] = engagement
			}
		}

		// Cover prompt is computed once (cheap, in-process) and either
		// surfaced as `cover_image_prompt` (when generate_cover_prompt
		// is set) or used internally to feed image.generate (when
		// cover_image is set), or both. Default: neither runs.
		var coverPrompt string
		if in.GenerateCoverPrompt || in.CoverImage {
			coverPrompt = podcast.CoverPromptForScript(podcast.Theme(theme), voicesUsed, script)
		}
		if in.GenerateCoverPrompt {
			out["cover_image_prompt"] = coverPrompt
		}
		if in.CoverImage {
			// Reuses the shared #146 entrypoint RunImageGen — saves a
			// registry round-trip and a second audit-log entry per
			// chained call. Artifacts land under ec.Pack.Name
			// ("podcast.generate") rather than image.generate's
			// namespace; the chained pack owns its own artifacts.
			coverModel := in.CoverImageModel
			if coverModel == "" {
				// Schnell is the fast/cheap default. Operators wanting
				// FLUX pro / SDXL / etc. override via `cover_image_model`.
				coverModel = imageGenDefaultModel
			}
			res, perr := RunImageGen(ctx, ec, v, eg, ImageGenRequest{
				Prompt: coverPrompt,
				Model:  coverModel,
			})
			if perr != nil {
				return nil, perr
			}
			out["cover_image_artifact_key"] = res.ArtifactKeys[0]
			out["cover_image_model_used"] = res.ModelUsed
		}
		return json.Marshal(out)
	}
}

// formatCeilingNotePodcast is the constant honesty string for
// podcast engagement output. Parallel structure to slides.narrate's
// constant; the message differs because audio doesn't have the
// "slide deck vs talking head" structural ceiling — the honest note
// here is about solo-vs-cohost execution dependence.
const formatCeilingNotePodcast = "Engagement metadata defaults follow Apple/Spotify spec and Buzzsprout 2025 retention data. Solo vs co-hosted retention is execution-dependent — neither format dominates; this pack supports both. CTA placement is fixed at mid-roll (research-validated); the tone (cta_style) is operator-tunable."

// generatePodcastEngagementPrompt enforces research-validated podcast
// engagement rules. Same posture as narrateEngagementPrompt — hard
// rules in the prompt, soft enrichment in the helper.
//
// Citations:
//   - Apple Podcasts chapters (≥3 per episode >10min, each ≥120s,
//     titles ≤45 chars): podcasters.apple.com/support/5482
//   - <itunes:summary> 4000-char limit / <itunes:subtitle> short:
//     help.rss.com iTunes namespace docs
//   - Mid-roll CTA placement (engaged-listener bias): industry data
//   - Title 60-80 chars, takeaway-first: Buzzsprout planning guide
const generatePodcastEngagementPrompt = `You are a podcast engagement-metadata writer. Produce ONE JSON object — no surrounding prose, no markdown fences — with exactly these fields:

{
  "title": "...",
  "subtitle": "...",
  "summary": "...",
  "show_notes_md": "...",
  "chapters": [{"startTime": 0, "title": "..."}, ...],
  "hook_30s": "...",
  "cta": {"placement": "mid-roll", "copy": "..."}
}

HARD RULES (the output is rejected by automated tests if violated):

Title:
- 60-80 characters, takeaway-first ("How to X" / "3 Patterns to Y" / "Why Z Changes Everything").
- Plain text. Use power verbs. Numeric specificity if natural.

Subtitle:
- Short (under 100 characters) — this is the <itunes:subtitle> field for column display.

Summary:
- This is <itunes:summary>, up to 4000 characters. 2-4 paragraph episode summary.
- Mention guests by name if present. List key topics. NO sexual or strong language regardless of episode rating.

Show notes (show_notes_md):
- Multi-paragraph markdown. Include:
  - 1 paragraph episode hook (parallel to summary but punchier)
  - Bulleted list of 3-7 key topics covered
  - Bulleted list of references / links if present in source material
- Plain markdown — no HTML.

Chapters:
- The FIRST chapter MUST have startTime=0.
- Provide AT LEAST 3 chapters when total duration > 10 minutes; fewer is acceptable for shorter episodes but still aim for ≥2.
- Minimum 120 seconds between consecutive chapter starts (Apple Podcasts guidance — chapters shorter than 2 minutes degrade UX).
- Chapter titles ≤ 45 characters, descriptive (not "Intro" / "Part 1").
- startTime is in SECONDS (integer), not M:SS strings.

Hook (hook_30s):
- A 2-4 sentence cold-open script the producer can adopt. Land the hook by second 15.
- No housekeeping ("welcome to the show, today we're talking about..."). Open with a specific claim, question, or named tension.

CTA:
- placement is ALWAYS "mid-roll" — non-overridable, research-validated.
- copy is a short (≤2 sentence) ask in the tone specified.`

// generatePodcastEngagement is the podcast-side parallel to
// slides_narrate's generateEngagement. Same shape (LLM call, tolerant
// JSON parse, server-side enrichment) so a future maintainer reading
// both sees the consistent pattern.
func generatePodcastEngagement(
	ctx context.Context,
	d vision.Dispatcher,
	model string,
	theme string,
	durationS float64,
	speakers map[string]string,
	script []podcast.Turn,
	in podcastGenerateInput,
) (map[string]any, error) {
	maxTokens := 1800
	var userMsg strings.Builder
	fmt.Fprintf(&userMsg, "Podcast theme: %s. Duration: %.0f seconds (~%.1f minutes).\n",
		theme, durationS, durationS/60)
	fmt.Fprintf(&userMsg, "Speakers: %d (", len(speakers))
	first := true
	for name := range speakers {
		if !first {
			userMsg.WriteString(", ")
		}
		userMsg.WriteString(name)
		first = false
	}
	userMsg.WriteString(")\n\nSCRIPT:\n\n")
	for _, t := range script {
		fmt.Fprintf(&userMsg, "%s: %s\n\n", t.Speaker, t.Text)
	}

	ctaStyle := strings.TrimSpace(in.CTAStyle)
	if ctaStyle == "" {
		ctaStyle = "natural"
	}
	language := strings.TrimSpace(in.Language)
	if language == "" {
		language = "en"
	}
	fmt.Fprintf(&userMsg, "\nCTA tone: %q. Language: %q.\nGenerate the engagement metadata JSON now.\n",
		ctaStyle, language)

	req := gateway.ChatRequest{
		Model:     model,
		MaxTokens: &maxTokens,
		Messages: []gateway.Message{
			{Role: "system", Content: gateway.TextContent(generatePodcastEngagementPrompt)},
			{Role: "user", Content: gateway.TextContent(userMsg.String())},
		},
	}
	resp, err := d.Dispatch(ctx, req)
	if err != nil {
		return nil, err
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("model returned no choices")
	}
	raw := strings.TrimSpace(resp.Choices[0].Message.Content.Text())

	var md map[string]any
	if err := json.Unmarshal([]byte(raw), &md); err != nil {
		if obj := extractFirstJSONObject(raw); obj != "" {
			if err2 := json.Unmarshal([]byte(obj), &md); err2 != nil {
				return nil, fmt.Errorf("parse engagement JSON: %w", err2)
			}
		} else {
			return nil, fmt.Errorf("no parseable JSON in engagement response")
		}
	}

	// Enrichment: language is operator-authoritative; format_ceiling_note
	// is constant; cta.placement is force-set to mid-roll regardless of
	// what the LLM emitted (the prompt says mid-roll, but a defensive
	// override means a future prompt drift can't silently change it).
	md["language"] = language
	md["format_ceiling_note"] = formatCeilingNotePodcast
	if cta, ok := md["cta"].(map[string]any); ok {
		cta["placement"] = "mid-roll"
		md["cta"] = cta
	} else {
		md["cta"] = map[string]any{"placement": "mid-roll", "copy": ""}
	}
	if title, ok := md["title"].(string); ok {
		md["title_char_count"] = len(title)
	}
	return md, nil
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
