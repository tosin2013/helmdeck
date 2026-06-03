// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// slides_narrate.go (T406 revived, ADR 035) — narrated video from
// Marp slide decks with ElevenLabs TTS and YouTube metadata.
//
// The pack composes three pipelines in one call:
//   1. Marp → per-slide PNGs (via --images in the sidecar)
//   2. ElevenLabs TTS → per-slide MP3 narration (from speaker notes)
//   3. ffmpeg → timed video (each slide plays for its audio duration)
//   4. Gateway LLM → YouTube metadata (title, description+timestamps,
//      tags, category)
//
// ElevenLabs API key is resolved from the credential vault as
// "elevenlabs-key" at handler time. When the key is missing, the
// pack degrades gracefully: slides get silence audio and the video
// is still produced with has_narration=false. When voice_id is empty,
// the handler calls GET /v1/voices and randomly picks from the top 5.
//
// The YouTube metadata is optional — only generated when metadata_model
// is set in the input. Uses the gateway dispatcher (same pattern as
// research.deep and content.ground).

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/tosin2013/helmdeck/internal/gateway"
	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/podcast"
	"github.com/tosin2013/helmdeck/internal/security"
	"github.com/tosin2013/helmdeck/internal/session"
	"github.com/tosin2013/helmdeck/internal/vault"
	"github.com/tosin2013/helmdeck/internal/vision"
	"github.com/tosin2013/helmdeck/internal/voices"
)

const (
	elevenLabsBaseURL        = "https://api.elevenlabs.io"
	elevenLabsDefaultModelID = "eleven_multilingual_v2"
	elevenLabsDefaultFormat  = "mp3_44100_128"

	defaultSlideDuration = 5.0       // seconds for slides without narration
	maxVideoSize         = 256 << 20 // 256 MiB cap on final video

	// slidesNarrateDefaultFfmpegThreads bounds libx264's per-encoder
	// thread count on the per-segment ffmpeg command. Without an
	// explicit -threads flag, libx264 grabs every host core (12 on a
	// typical workstation), and each thread holds ~50-80 MB of frame
	// buffers at 1080p — that's ~800 MB of encoder state alone before
	// reference frames, lookahead, and Chromium's resident set. Most
	// observed OOMs trace back to this. Capping to 4 cuts peak by
	// ~3× at the cost of ~20% wall-clock per segment, which is
	// negligible against the wins. Operators with abundant RAM bump
	// via HELMDECK_SLIDES_NARRATE_FFMPEG_THREADS. ADR 045 stays in
	// place — CPUProfile=ProfileCompute still scales the container's
	// CPU quota with host cores; this cap is narrowly about the
	// encoder thread *count*, not CPU allocation.
	slidesNarrateDefaultFfmpegThreads = "4"
	slidesNarrateFfmpegThreadsEnv     = "HELMDECK_SLIDES_NARRATE_FFMPEG_THREADS"

	// minRenderedSlidePngBytes is the smallest PNG size we accept from
	// marp before handing a slide to ffmpeg. Marp can silently emit a
	// near-empty PNG when a slide contains content its headless
	// Chromium can't render — most commonly an embedded Mermaid block
	// (`flowchart`, `sequenceDiagram`, etc.), custom HTML with broken
	// CSS, or a fenced YAML that confuses the renderer. In that case
	// marp's process still exits 0; the broken file flows into the
	// per-segment ffmpeg encode and produces a misleading
	// "ffmpeg segment N failed" error that classify.go routes to
	// FailurePackBug — pointing operators at a non-existent helmdeck
	// bug instead of the slide they need to edit. 1 KB is well below
	// any real rendered slide (the smallest sensible solid-color
	// 1920x1080 PNG is several KB after deflate overhead) and well
	// above the few hundred bytes a marp-blank-output produces, so
	// the threshold is safe in both directions.
	minRenderedSlidePngBytes = 1024

	// minEncodedSegmentBytes is the smallest per-segment .mp4 (and the
	// final concatenated .mp4) we accept from ffmpeg before treating it
	// as a successful encode. ffmpeg can exit 0 yet produce a 0-byte
	// or truncated output when the input image is malformed in a way
	// libavformat can't surface as a non-zero exit — that broken file
	// then flows into concat and surfaces as a misleading
	// "ffmpeg concat failed" error. A valid h264 .mp4 with even a
	// single frame is at minimum a few KB (the mdat box + a sane
	// moov header). 1 KB is comfortably below that floor and well
	// above zero, so empty/truncated output trips reliably without
	// false-positiving on legitimately short segments.
	minEncodedSegmentBytes = 1024

	// pngMagicHex is the 8-byte PNG file signature in lowercase hex.
	// validateMarpPngs uses `head -c 8 | od -An -tx1` to read these
	// bytes off marp's output; a mismatch (or a shorter read) means
	// the file is not a valid PNG and the downstream ffmpeg encode
	// will silently produce garbage. The four leading bytes
	// (89 50 4e 47) are the "real" diagnostic — 89 is the high-bit
	// guard, "PNG" is the type. The 5-8 bytes (0d 0a 1a 0a) are
	// CRLF / EOF guards but a mismatch on any of them still indicates
	// a corrupt or truncated file. Source:
	// https://www.w3.org/TR/png-3/#5DataRep
	pngMagicHex = "89504e470d0a1a0a"

	// minSilenceMp3Bytes is the smallest silence MP3 file we accept
	// from generateSilence. libmp3lame's overhead per file (ID3v2 + a
	// single MPEG frame) is at least a few hundred bytes even for very
	// short durations. 256 is well above zero and well below any real
	// silence track for typical slide durations.
	minSilenceMp3Bytes = 256

	// minTTSResponseBytes is the smallest body we accept from a
	// successful ElevenLabs TTS response. A 1-second of MP3 audio is
	// at minimum ~2 KB; a body smaller than minTTSResponseBytes is
	// almost certainly an error wrapped in HTTP 200 (which ElevenLabs
	// does occasionally — JSON error envelope with `{"error":"..."}`
	// returned as 200, not 4xx). Floor is well below any real audio
	// payload.
	minTTSResponseBytes = 512

	narrateYouTubePrompt = `You are a YouTube metadata writer. Given the content and durations of a slide presentation, produce ONE JSON object with exactly these fields:

{
  "title": "catchy YouTube title, max 100 characters",
  "description": "2-3 paragraph description followed by timestamps formatted as:\n\nTimestamps:\n0:00 First slide title\n0:32 Second slide title\n...",
  "tags": ["tag1", "tag2", ...],
  "category": "Science & Technology",
  "language": "en"
}

Rules:
- Timestamps must use cumulative durations provided
- Format timestamps as M:SS (e.g. 0:00, 1:32, 10:05)
- Description should summarize the presentation content
- Tags should cover the main topics for discoverability (10-15 tags)
- Do not wrap in markdown`
)

// SlidesNarrate constructs the pack. The dispatcher is used for
// YouTube metadata generation (optional). The vault resolves the
// ElevenLabs API key. Both degrade gracefully.
func SlidesNarrate(d vision.Dispatcher, vs *vault.Store, eg *security.EgressGuard) *packs.Pack {
	return &packs.Pack{
		Name:         "slides.narrate",
		Version:      "v1",
		Description:  "Convert a Marp slide deck to a narrated MP4 video with ElevenLabs TTS and YouTube metadata. Requires HELMDECK_ELEVENLABS_API_KEY in .env.local (auto-hydrated to vault as 'elevenlabs-key'); pass allow_silent_output:true to render slides over silence when no key is configured (CI smoke / demo placeholder). Optional hero_image_prompt generates a hero artwork via fal.ai and inlines it into slide 1.",
		NeedsSession: true,
		InputSchema: packs.BasicSchema{
			Required: []string{"markdown"},
			Properties: map[string]string{
				"markdown":               "string",
				"voice_id":               "string",
				"model_id":               "string",
				"resolution":             "string",
				"fade_ms":                "number",
				"default_slide_duration": "number",
				"metadata_model":         "string",
				"credential":             "string",
				"allow_silent_output":    "boolean",
				"min_turn_duration_s":    "number",
				"dry_run":                "boolean",
				"plan":                   "string",
				"hero_image_prompt":      "string",
				"hero_image_model":       "string",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"video_artifact_key", "video_size", "slide_count", "total_duration_s", "has_narration"},
			Properties: map[string]string{
				"video_artifact_key":    "string",
				"video_size":            "number",
				"slide_count":           "number",
				"total_duration_s":      "number",
				"has_narration":         "boolean",
				"tts_failure_count":     "number",
				"voice_used":            "string",
				"metadata_artifact_key": "string",
				"metadata":              "object",
				"hero_image_model_used": "string",
				// Cost transparency — emitted by the handler; declared
				// here so agents/pipeline authors see them in the catalog.
				// tts_chars is a per-slide breakdown map with a "_total"
				// key (see computeSlideTTSChars) — an object, not a number.
				// Declaring it "number" shipped a real invalid_output
				// failure on every pipeline run in v0.17.1.
				"tts_chars":                "object",
				"estimated_cost_usd":       "number",
				"estimated_cost_breakdown": "object",
			},
		},
		Handler: slidesNarrateHandler(d, vs, eg),
		// Heavy: 60-180s wall-clock typical (Marp render + per-slide
		// TTS + ffmpeg encode + concat). Async=true routes the
		// MCP tools/call through the SEP-1686 task envelope path so
		// no JSON-RPC request blocks long enough to trip the client's
		// per-request timeout. See internal/mcp/jobs.go for the wire
		// shape and docs/integrations/webhooks.md for push delivery.
		Async: true,
		// Memory: encoding is serial (one ffmpeg per segment, then
		// stream-copy concat), so peak RAM is bounded by a single
		// ffmpeg + the Chromium baseline — not by slide count.
		//
		// Measured footprints on libx264/stillimage + AAC 192k + a
		// live Chromium/Playwright sidecar:
		//   720p  steady-state ≈ 1.2 GB  (500 MB ffmpeg + 670 MB Chromium)
		//   1080p steady-state ≈ 1.38 GB (700 MB ffmpeg + 670 MB Chromium)
		//
		// 3 GiB gives a comfortable ~55% headroom for transient
		// encoder spikes on complex frames. 4K would still need an
		// override — operators rendering larger resolutions bump
		// this at registration time.
		//
		// Timeout: the runtime default is 5 minutes, which fit
		// screenshots and short scrapes but not video encoding —
		// a 20-slide 1080p deck with ~50s narration per slide takes
		// 15-20 minutes wall-clock (TTS + per-segment ffmpeg + a
		// final stream-copy concat). Watchdog at 5m kills the
		// container mid-encode and ffmpeg exits 137, indistinguishable
		// from an OOM. Bump to 30 minutes so any realistic deck has
		// room to finish. Operators with larger decks or slower
		// sidecars can override via SessionSpec.
		SessionSpec: session.Spec{
			MemoryLimit: "3g",
			Timeout:     30 * time.Minute,
			// CPU-bound — per-slide TTS upload is I/O, but the Marp
			// render and per-segment ffmpeg encode + concat are
			// compute-heavy and dominate wall-clock. ProfileCompute
			// scales the cap with host cores (vs the legacy 1-core
			// default that pegged encode at 100%). ADR 045.
			CPUProfile: session.ProfileCompute,
		},
	}
}

type slidesNarrateInput struct {
	Markdown             string  `json:"markdown"`
	VoiceID              string  `json:"voice_id"`
	ModelID              string  `json:"model_id"`
	Resolution           string  `json:"resolution"`
	FadeMS               int     `json:"fade_ms"`
	DefaultSlideDuration float64 `json:"default_slide_duration"`
	MetadataModel        string  `json:"metadata_model"`
	Credential           string  `json:"credential"`
	AllowSilentOutput    bool    `json:"allow_silent_output"`
	MinTurnDurationS     float64 `json:"min_turn_duration_s"`
	DryRun               bool    `json:"dry_run"`
	Plan                 string  `json:"plan"`
	// HeroImagePrompt (#146c) triggers RunImageGen and inlines the
	// resulting PNG into slide 1. Inserted WITHOUT a `---` separator
	// so it becomes part of slide 1's existing narration — adding a
	// blank intro slide would break the audio pipeline.
	HeroImagePrompt string `json:"hero_image_prompt"`
	HeroImageModel  string `json:"hero_image_model"`
}

func slidesNarrateHandler(d vision.Dispatcher, vs *vault.Store, eg *security.EgressGuard) packs.HandlerFunc {
	return func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
		var in slidesNarrateInput
		if err := json.Unmarshal(ec.Input, &in); err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
		}
		if strings.TrimSpace(in.Markdown) == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "markdown is required"}
		}
		if ec.Exec == nil {
			return nil, &packs.PackError{Code: packs.CodeSessionUnavailable, Message: "slides.narrate requires a session executor"}
		}

		// Hero image — when prompt set, generate PNG via RunImageGen
		// and inline-inject into slide 1's content (no `---` separator
		// — adding a blank intro slide would break the per-slide TTS
		// pipeline). Skipped during dry_run for the same reason as
		// podcast.generate's cover_image (no real money on a preview).
		heroImageModelUsed := ""
		markdown := in.Markdown
		if strings.TrimSpace(in.HeroImagePrompt) != "" && !in.DryRun {
			heroModel := in.HeroImageModel
			if heroModel == "" {
				heroModel = imageGenDefaultModel
			}
			heroRes, perr := RunImageGen(ctx, ec, vs, eg, ImageGenRequest{
				Prompt: in.HeroImagePrompt,
				Model:  heroModel,
			})
			if perr != nil {
				return nil, perr
			}
			heroImageModelUsed = heroRes.ModelUsed
			imgBytes, _, gerr := ec.Artifacts.Get(ctx, heroRes.ArtifactKeys[0])
			if gerr != nil {
				return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
					Message: fmt.Sprintf("read hero image artifact: %v", gerr), Cause: gerr}
			}
			dataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(imgBytes)
			heroBlock := fmt.Sprintf("<img src=\"%s\" alt=\"hero\" class=\"hero-image\" />\n\n", dataURI)
			if idx := frontmatterEndIndex(markdown); idx > 0 {
				markdown = markdown[:idx] + "\n" + heroBlock + markdown[idx:]
			} else {
				markdown = heroBlock + markdown
			}
		}

		// Defaults.
		// Resolution accepts BOTH named presets ("720p", "1080p",
		// "2160p"/"4k") AND pre-formatted "WIDTHxHEIGHT" strings.
		// hyperframes.render takes named presets too, so an operator
		// can use the same "1080p" value across both packs — but
		// slides.narrate passes its value verbatim to ffmpeg's
		// `scale=` filter, which rejects "1080p" with "Invalid size".
		// Normalize before ffmpeg ever sees it.
		resolution := normalizeSlidesNarrateResolution(in.Resolution)
		if resolution == "" {
			resolution = "1920x1080"
		}
		modelID := in.ModelID
		if modelID == "" {
			modelID = elevenLabsDefaultModelID
		}
		slideDur := in.DefaultSlideDuration
		if slideDur <= 0 {
			slideDur = defaultSlideDuration
		}

		// 1. Parse slides + notes.
		ec.Report(0, "parsing slides")
		slides := parseSlidesAndNotes(markdown)
		if len(slides) == 0 {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "no slides found in markdown"}
		}
		ec.Report(5, fmt.Sprintf("parsed %d slides", len(slides)))

		// 1b. Cost accounting (#145). Per-slide char counts feed both
		// the dry_run short-circuit AND the regular response — same
		// shape as podcast.generate.
		slideTTSChars, slideTTSCharsTotal := computeSlideTTSChars(slides)
		slideEstimateUSD, slideEstimateBreakdown := podcast.EstimateElevenLabs(slideTTSCharsTotal, in.Plan)

		if in.DryRun {
			// dry_run runs BEFORE credential resolve so cost preview
			// works on Free-tier accounts without a paid key.
			out := map[string]any{
				"dry_run":                  true,
				"slide_count":              len(slides),
				"tts_chars":                slideTTSChars,
				"estimated_cost_usd":       slideEstimateUSD,
				"estimated_cost_breakdown": slideEstimateBreakdown,
			}
			raw, mErr := json.Marshal(out)
			if mErr != nil {
				return nil, &packs.PackError{Code: packs.CodeInternal, Message: mErr.Error(), Cause: mErr}
			}
			return raw, nil
		}

		// 2. Resolve ElevenLabs API key through the shared #138 ladder
		// (explicit → vault:elevenlabs-key → vault:elevenlabs-api-key
		// → env:HELMDECK_ELEVENLABS_API_KEY). Per #138 we now hard-fail
		// rather than silently rendering a video over silence, unless
		// the caller explicitly opted in via allow_silent_output.
		apiKey, keySrc := resolveElevenLabsKey(ctx, vs, in.Credential)
		if apiKey == "" {
			if !in.AllowSilentOutput {
				return nil, &packs.PackError{
					Code:    packs.CodeInvalidInput,
					Message: elevenLabsMissingCredentialMessage,
				}
			}
			ec.Logger.Warn("slides.narrate: no ElevenLabs key resolved; rendering with silent audio (allow_silent_output=true)",
				"explicit_credential", in.Credential)
		} else {
			ec.Logger.Info("slides.narrate: resolved ElevenLabs key", "source", keySrc)
		}
		// narrationRequested captures intent: does the caller (or its
		// pipeline) plan to narrate this deck? It's true when a key
		// was resolved AND the caller didn't pass allow_silent_output
		// — which would mean they explicitly want silence and there's
		// no need to validate or call ElevenLabs at all. The final
		// "has_narration" output is computed at return time from the
		// per-slide TTS success counter (see narratedCount /
		// ttsSuccessCount below), so a deck where the precheck
		// passed but every TTS call later silently fell back to
		// silence will correctly emit has_narration=false. Honest
		// output > convenient lie.
		narrationRequested := apiKey != ""

		// ADR: paid-API credential precheck. Hit a cheap "who am I"
		// endpoint BEFORE doing expensive work (voice listing, Marp
		// render, gateway LLM call for YouTube metadata). A 401/403/
		// quota-exhausted key surfaces as CodeCredentialInvalid here
		// instead of as a string of "TTS failed, falling back to
		// silence" warnings 30 seconds into the run. Maps to
		// FailureCallerFixable with a "update the vault" reason via
		// classify.go.
		//
		// Skipped when the caller explicitly opted into silent output
		// (no narration → no need to check the credential) or when no
		// key was resolved (the missing-key branch above already
		// fired or the allow_silent_output path took over).
		if narrationRequested {
			validateClient := &http.Client{Timeout: 10 * time.Second}
			if err := vault.ValidateElevenLabs(ctx, validateClient, apiKey); err != nil {
				if perr, ok := err.(*packs.PackError); ok && perr.Code == packs.CodeCredentialInvalid {
					return nil, perr
				}
				// Transient (network blip, 5xx, rate-limited on
				// the precheck endpoint). Don't block — the
				// per-slide TTS calls each carry their own
				// fallback-to-silence safety net. Log so an
				// operator reading the run log can correlate.
				ec.Logger.Warn("slides.narrate: ElevenLabs precheck transient error; proceeding",
					"err", err)
			}
		}

		// hasNarration tracks intent through the rest of the handler;
		// the final output's has_narration field reflects measured
		// outcome (see below). They start equal — if the precheck
		// just passed, we expect to narrate every slide that has
		// notes — and diverge only when per-slide TTS calls fail
		// silently to fallback.
		hasNarration := narrationRequested

		// 3. Pick voice (random from top 5 if not specified).
		voiceID := in.VoiceID
		if hasNarration && voiceID == "" {
			picked, err := pickRandomVoice(ctx, apiKey)
			if err != nil {
				ec.Logger.Warn("failed to list voices, using default", "err", err)
				voiceID = "21m00Tcm4TlvDq8ikWAM" // Rachel fallback
			} else {
				voiceID = picked
			}
		}

		// 4. Write markdown to sidecar + export PNGs. Inject the auto-fit
		// <style> (#280) so oversized diagrams/tables don't clip in the
		// per-slide PNGs either. Done after parseSlidesAndNotes so the
		// slide count is unaffected — Marp hoists <style> to global CSS
		// and renders no slide for it.
		markdown = injectFitStyle(markdown)
		if _, err := execWithStdin(ctx, ec, "/tmp/helmdeck-deck.md", []byte(markdown)); err != nil {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("write markdown to sidecar: %v", err)}
		}
		marpCmd := fmt.Sprintf(
			"mkdir -p /tmp/slides && marp --images png --allow-local-files /tmp/helmdeck-deck.md -o /tmp/slides/deck.png",
		)
		marpRes, err := ec.Exec(ctx, session.ExecRequest{Cmd: []string{"sh", "-c", marpCmd}})
		if err != nil || marpRes.ExitCode != 0 {
			stderr := ""
			if marpRes.ExitCode != 0 {
				stderr = strings.TrimSpace(string(marpRes.Stderr))
			}
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("marp --images failed (exit %d): %s", marpRes.ExitCode, stderr)}
		}

		// 4a. Validate marp's per-slide PNGs BEFORE handing them to
		// ffmpeg. marp returns exit 0 even when its headless Chromium
		// silently fails to render embedded content (Mermaid blocks,
		// custom HTML, fenced YAML), producing either no PNG for that
		// slide or a tiny near-empty one. Without this check the
		// broken file flows into the per-segment ffmpeg encode and
		// surfaces as "ffmpeg segment N failed (exit 0)" — which
		// classify.go routes to FailurePackBug with a
		// "file a helmdeck issue" URL, pointing operators away from
		// the slide they actually need to edit. Returning
		// CodeInvalidInput here routes to FailureCallerFixable with
		// the exact slide index + a concrete recovery hint.
		if perr := validateMarpPngs(ctx, ec, len(slides)); perr != nil {
			return nil, perr
		}

		// 5. Generate audio per slide (TTS or silence). Progress
		// from 10→50% across the slides; this is the slowest stage
		// when ElevenLabs is involved (a few seconds per slide), so
		// reporting per-slide is what keeps low-timeout MCP clients
		// (OpenClaw 60s default) from giving up.
		ec.Report(10, "generating narration audio")
		// #141: per-slide duration floor. When unset, default to the
		// slides.narrate house style (5s — same as defaultSlideDuration
		// for note-less slides). Pass min_turn_duration_s:0 explicitly
		// to opt out and use raw TTS pacing.
		minTurnSec := in.MinTurnDurationS
		if minTurnSec == 0 && !zeroFloorOptedIn(ec.Input) {
			minTurnSec = defaultMinTurnDurationS
		}
		durations := make([]float64, len(slides))
		// narratableSlideCount = slides with non-empty notes that we
		// EXPECTED to narrate. ttsSuccessCount = of those, how many
		// actually got real TTS audio (didn't fall back to silence).
		// Final has_narration is (narrationRequested && success ==
		// expected). A deck where the precheck passed but every TTS
		// call fell back to silence (e.g. an intermittent ElevenLabs
		// outage during the run) will correctly emit
		// has_narration=false and tts_failure_count=N — the output
		// matches the bytes.
		var narratableSlideCount, ttsSuccessCount int
		for i, s := range slides {
			ec.Report(10+float64(i)*40/float64(len(slides)),
				fmt.Sprintf("audio %d/%d", i+1, len(slides)))
			if hasNarration && s.Notes != "" {
				narratableSlideCount++
				audio, err := elevenLabsTTS(ctx, apiKey, voiceID, modelID, s.Notes)
				if err != nil {
					ec.Logger.Warn("TTS failed, falling back to silence",
						"slide", i, "err", err)
					if err := generateSilence(ctx, ec, i, slideDur); err != nil {
						return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
							Message: fmt.Sprintf("silence gen for slide %d: %v", i, err)}
					}
					durations[i] = slideDur
					continue
				}
				ttsSuccessCount++
				// Transfer audio into sidecar.
				if _, err := execWithStdin(ctx, ec, fmt.Sprintf("/tmp/audio-%03d.mp3", i), audio); err != nil {
					return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
						Message: fmt.Sprintf("transfer audio slide %d: %v", i, err)}
				}
				// Probe duration.
				dur, err := probeAudioDuration(ctx, ec, i)
				if err != nil {
					ec.Logger.Warn("ffprobe failed, using default duration", "slide", i, "err", err)
					dur = slideDur
				}
				// #141: enforce per-segment floor. If TTS came back
				// shorter than the floor, append silence to the audio
				// file so the encoded video segment plays for at least
				// minTurnSec — keeps downstream pipelines (YouTube
				// cuts, slide-sync) from feeling rushed.
				if minTurnSec > 0 && dur < minTurnSec {
					if perr := padSlideAudioToMin(ctx, ec, i, dur, minTurnSec); perr != nil {
						ec.Logger.Warn("pad audio failed, using raw duration", "slide", i, "err", perr)
					} else {
						dur = minTurnSec
					}
				}
				durations[i] = dur
			} else {
				if err := generateSilence(ctx, ec, i, slideDur); err != nil {
					return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
						Message: fmt.Sprintf("silence gen for slide %d: %v", i, err)}
				}
				durations[i] = slideDur
			}
		}

		// 6. Compose per-slide video segments. Progress 50→90%.
		ec.Report(50, "encoding video segments")
		for i := range slides {
			ec.Report(50+float64(i)*40/float64(len(slides)),
				fmt.Sprintf("encoding segment %d/%d", i+1, len(slides)))
			slideFile := fmt.Sprintf("/tmp/slides/deck.%03d.png", i+1) // marp uses 1-based
			audioFile := fmt.Sprintf("/tmp/audio-%03d.mp3", i)
			segFile := fmt.Sprintf("/tmp/seg-%03d.mp4", i)
			// ffmpeg filter uses colon-separated dimensions, not "x"
			resDim := strings.Replace(resolution, "x", ":", 1)
			vf := fmt.Sprintf(
				"scale=%s:force_original_aspect_ratio=decrease,pad=%s:(ow-iw)/2:(oh-ih)/2",
				resDim, resDim,
			)
			if in.FadeMS > 0 {
				fadeSec := float64(in.FadeMS) / 1000.0
				dur := durations[i]
				if dur > fadeSec*2 {
					vf += fmt.Sprintf(",fade=t=in:st=0:d=%.3f,fade=t=out:st=%.3f:d=%.3f",
						fadeSec, dur-fadeSec, fadeSec)
				}
			}
			// Primary attempt: capped threads (default 4) at libx264's
			// "medium" preset. Operators can bump the thread cap via
			// HELMDECK_SLIDES_NARRATE_FFMPEG_THREADS on hosts with
			// abundant RAM.
			primaryOpts := ffmpegEncodeOpts{
				Threads: slidesNarrateFfmpegThreads(),
			}
			res, err := encodeSegment(ctx, ec, slideFile, audioFile, segFile, vf, primaryOpts)
			// Adaptive retry: if the OS killed ffmpeg with the OOM
			// signature (exit 137 → CodeResourceExhausted via
			// classifyShellExitCode), retry THIS segment ONCE with a
			// single thread and the veryfast preset — that combination
			// cuts the encoder's working set by ~3-4× at the cost of a
			// minor quality and bitrate efficiency hit. The retry is
			// bounded to one attempt per segment so a structurally
			// undersized memory cap doesn't burn loops; the second
			// failure escalates to the operator with the original
			// CodeResourceExhausted reason so they know to bump
			// SessionSpec.MemoryLimit. Logged loud so post-mortem
			// reviews see when degraded encoding fired.
			if res.ExitCode != 0 {
				if rc, ok := classifyShellExitCode(res.ExitCode); ok && rc == packs.CodeResourceExhausted {
					ec.Logger.Warn("slides.narrate: segment OOM-killed; retrying ONCE with degraded encoder settings",
						"segment", i,
						"primary_threads", primaryOpts.Threads,
						"retry_threads", "1",
						"retry_preset", "veryfast")
					retryOpts := ffmpegEncodeOpts{Threads: "1", Preset: "veryfast"}
					res, err = encodeSegment(ctx, ec, slideFile, audioFile, segFile, vf, retryOpts)
				}
			}
			if err != nil || res.ExitCode != 0 {
				stderr := strings.TrimSpace(string(res.Stderr))
				// Reconstruct the cmd string for the stderr artifact
				// header — useful for post-mortems but not load-bearing.
				cmdForArtifact := fmt.Sprintf("ffmpeg -y -loop 1 ... -threads %s (encode segment %d)", primaryOpts.Threads, i)
				artKey := persistFfmpegStderr(ctx, ec, fmt.Sprintf("ffmpeg-stderr-segment-%03d.txt", i),
					cmdForArtifact, res.Stderr)
				// Honest error message: when the failure is a docker-exec
				// transport error (ec.Exec returned err != nil), res.ExitCode
				// is the zero value (0) and printing it as "exit 0" misleads
				// operators into chasing imaginary ffmpeg bugs. Surface the
				// real transport error instead. classifyShellExitCode never
				// matches exit 0, so the OOM-retry path is unreachable here.
				if err != nil {
					return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
						Message: fmt.Sprintf("ffmpeg segment %d: docker-exec transport error (ffmpeg did NOT return a real exit code): %v. stderr (if any): %s%s",
							i, err, truncStr(stderr, 4096), artifactSuffix(artKey)),
						Cause: err}
				}
				// Lift OS-side kills (typically OOM at 1080p with
				// large segment counts) into CodeResourceExhausted so
				// classify.go routes them to FailureTransient instead
				// of FailurePackBug. The classify path adds an
				// actionable "bump MemoryLimit / split the deck"
				// reason; we replace the message here too so an
				// operator reading the raw error sees the same hint.
				code := packs.CodeHandlerFailed
				msg := fmt.Sprintf("ffmpeg segment %d failed (exit %d): %s%s",
					i, res.ExitCode, truncStr(stderr, 4096), artifactSuffix(artKey))
				if rc, ok := classifyShellExitCode(res.ExitCode); ok {
					code = rc
					msg = fmt.Sprintf("ffmpeg segment %d killed by the OS on exit %d after primary encode AND degraded-retry both OOM'd (likely OOM at 1080p — bump SessionSpec.MemoryLimit, reduce slide count, or lower the encode resolution). stderr: %s%s",
						i, res.ExitCode, truncStr(stderr, 4096), artifactSuffix(artKey))
				}
				return nil, &packs.PackError{Code: code, Message: msg}
			}
			// Post-encode existence check: ffmpeg can exit 0 yet produce
			// a 0-byte / truncated .mp4 (typically when the input PNG was
			// malformed in a way that survived validateMarpPngs's size
			// floor but tripped libavformat). Without this check the
			// broken segment flows into concat and surfaces there as a
			// misleading "ffmpeg concat failed" error, again pointing
			// operators at the wrong step. Stat the produced file via
			// the same wc-c pattern fs.read uses; sub-1KB output is
			// genuinely never a valid h264 segment.
			if perr := requireNonEmptyOutput(ctx, ec, segFile, minEncodedSegmentBytes,
				fmt.Sprintf("ffmpeg segment %d", i)); perr != nil {
				return nil, perr
			}
		}

		// 7. Concatenate all segments.
		ec.Report(90, "concatenating final video")
		var concatList strings.Builder
		for i := range slides {
			fmt.Fprintf(&concatList, "file '/tmp/seg-%03d.mp4'\n", i)
		}
		if _, err := execWithStdin(ctx, ec, "/tmp/concat.txt", []byte(concatList.String())); err != nil {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("write concat list: %v", err)}
		}
		concatRes, err := ec.Exec(ctx, session.ExecRequest{
			Cmd: []string{"sh", "-c", "ffmpeg -y -f concat -safe 0 -i /tmp/concat.txt -c copy /tmp/final.mp4"},
		})
		if err != nil || concatRes.ExitCode != 0 {
			stderr := strings.TrimSpace(string(concatRes.Stderr))
			artKey := persistFfmpegStderr(ctx, ec, "ffmpeg-stderr-concat.txt",
				"ffmpeg -y -f concat -safe 0 -i /tmp/concat.txt -c copy /tmp/final.mp4", concatRes.Stderr)
			// Same honest-message fix as the segment path: a transport
			// error (err != nil) zero-initializes concatRes.ExitCode, so
			// the old message printed a misleading "exit 0".
			if err != nil {
				return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
					Message: fmt.Sprintf("ffmpeg concat: docker-exec transport error (ffmpeg did NOT return a real exit code): %v. stderr (if any): %s%s",
						err, truncStr(stderr, 4096), artifactSuffix(artKey)),
					Cause: err}
			}
			code := packs.CodeHandlerFailed
			msg := fmt.Sprintf("ffmpeg concat failed (exit %d): %s%s",
				concatRes.ExitCode, truncStr(stderr, 4096), artifactSuffix(artKey))
			if rc, ok := classifyShellExitCode(concatRes.ExitCode); ok {
				code = rc
				msg = fmt.Sprintf("ffmpeg concat killed by the OS on exit %d (likely OOM — concat is usually cheap, but tight memory limits + large per-segment files can still trip the OOM killer). stderr: %s%s",
					concatRes.ExitCode, truncStr(stderr, 4096), artifactSuffix(artKey))
			}
			return nil, &packs.PackError{Code: code, Message: msg}
		}
		// Post-concat existence check: ffmpeg concat can exit 0 yet
		// produce a 0-byte /tmp/final.mp4 when individual segment files
		// are present but malformed in a way -c copy cannot replay. The
		// downstream "cat /tmp/final.mp4" then returns empty Stdout and
		// the existing size check at line 635 only catches >maxVideoSize,
		// not 0 bytes. Validate up front so the failure surfaces as the
		// concat step (the actual cause), not a generic "no video".
		if perr := requireNonEmptyOutput(ctx, ec, "/tmp/final.mp4", minEncodedSegmentBytes,
			"ffmpeg concat"); perr != nil {
			return nil, perr
		}

		// 8. Read back the final video.
		catRes, err := ec.Exec(ctx, session.ExecRequest{
			Cmd: []string{"sh", "-c", "cat /tmp/final.mp4"},
		})
		if err != nil || catRes.ExitCode != 0 {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: "failed to read final video"}
		}
		videoBytes := catRes.Stdout
		if len(videoBytes) > maxVideoSize {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("video exceeds %d MiB cap (%d bytes)", maxVideoSize>>20, len(videoBytes))}
		}

		// 9. Upload video artifact.
		ec.Report(95, "uploading video artifact")
		videoArt, err := ec.Artifacts.Put(ctx, "slides.narrate", "video.mp4", videoBytes, "video/mp4")
		if err != nil {
			return nil, &packs.PackError{Code: packs.CodeArtifactFailed,
				Message: fmt.Sprintf("upload video: %v", err), Cause: err}
		}

		// 10. YouTube metadata (optional).
		var totalDuration float64
		for _, d := range durations {
			totalDuration += d
		}

		var metadataKey string
		var metadata map[string]any
		if d != nil && strings.TrimSpace(in.MetadataModel) != "" {
			ec.Report(98, "generating YouTube metadata")
			md, err := generateYouTubeMetadata(ctx, d, in.MetadataModel, slides, durations)
			if err != nil {
				ec.Logger.Warn("YouTube metadata generation failed", "err", err)
			} else {
				metadata = md
				mdBytes, _ := json.MarshalIndent(md, "", "  ")
				art, err := ec.Artifacts.Put(ctx, "slides.narrate", "metadata.json", mdBytes, "application/json")
				if err != nil {
					ec.Logger.Warn("metadata artifact upload failed", "err", err)
				} else {
					metadataKey = art.Key
				}
			}
		}

		// 11. Return.
		// has_narration reflects MEASURED OUTCOME, not just intent:
		// true iff we wanted to narrate AND every slide that had
		// notes ended up with real TTS audio (no silence
		// fallback). Lying here — emitting has_narration=true on a
		// deck that silently fell back across the board — was the
		// concrete operator complaint that motivated this fix.
		ttsFailureCount := narratableSlideCount - ttsSuccessCount
		honestHasNarration := hasNarration && voiceID != "" &&
			narratableSlideCount > 0 && ttsFailureCount == 0
		out := map[string]any{
			"video_artifact_key":    videoArt.Key,
			"video_size":            len(videoBytes),
			"slide_count":           len(slides),
			"total_duration_s":      totalDuration,
			"has_narration":         honestHasNarration,
			"tts_failure_count":     ttsFailureCount,
			"voice_used":            voiceID,
			"metadata_artifact_key": metadataKey,
			// #145: cost transparency on real runs too. Mirrors the
			// dry_run shape so callers can rely on the same fields
			// regardless of mode.
			"tts_chars":                slideTTSChars,
			"estimated_cost_usd":       slideEstimateUSD,
			"estimated_cost_breakdown": slideEstimateBreakdown,
		}
		if metadata != nil {
			out["metadata"] = metadata
		}
		if heroImageModelUsed != "" {
			out["hero_image_model_used"] = heroImageModelUsed
		}
		return json.Marshal(out)
	}
}

// computeSlideTTSChars sums the speaker-notes length per slide and
// returns the per-slide map (keyed by slide-NNN, with "_total") plus
// the total. Mirrors computeTTSChars in podcast_generate.go but
// keyed by slide index instead of speaker.
func computeSlideTTSChars(slides []slideContent) (map[string]int, int) {
	per := map[string]int{}
	total := 0
	for _, s := range slides {
		n := len(s.Notes)
		per[fmt.Sprintf("slide-%03d", s.Index+1)] = n
		total += n
	}
	per["_total"] = total
	return per, total
}

// --- ElevenLabs helpers --------------------------------------------------

// elevenLabsTTS calls the ElevenLabs text-to-speech endpoint and
// returns the raw audio bytes (MP3).
func elevenLabsTTS(ctx context.Context, apiKey, voiceID, modelID, text string) ([]byte, error) {
	reqBody, _ := json.Marshal(map[string]any{
		"text":     text,
		"model_id": modelID,
		"voice_settings": map[string]any{
			"stability":        0.5,
			"similarity_boost": 0.75,
		},
	})
	url := fmt.Sprintf("%s/v1/text-to-speech/%s?output_format=%s",
		elevenLabsBaseURL, voiceID, elevenLabsDefaultFormat)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "audio/mpeg")
	httpReq.Header.Set("xi-api-key", apiKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("elevenlabs request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20)) // 32 MiB cap per slide
	if err != nil {
		return nil, fmt.Errorf("read elevenlabs response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("elevenlabs %d: %s", resp.StatusCode, truncStr(string(body), 256))
	}
	// Post-200 body validation. ElevenLabs occasionally wraps an
	// error as `{"error":"..."}` in an HTTP 200 — the caller would
	// otherwise hand that JSON to ffmpeg as "audio bytes", which then
	// silently produces no audio. Delegate the actual checks to
	// validateElevenLabsBody (testable in isolation).
	if err := validateElevenLabsBody(body); err != nil {
		return nil, err
	}
	return body, nil
}

// validateElevenLabsBody rejects HTTP-200 bodies that are too small or
// don't start with an MP3 sync word / ID3v2 tag. Extracted so the
// validation logic is unit-testable without an HTTP stub. Returns nil
// for a body that looks like real MP3 audio.
func validateElevenLabsBody(body []byte) error {
	if len(body) < minTTSResponseBytes || !looksLikeMP3(body) {
		return fmt.Errorf("elevenlabs returned HTTP 200 but body is not valid MP3 audio (%d bytes, prefix=%q) — likely an error envelope wrapped as 200",
			len(body), truncStr(string(body), 64))
	}
	return nil
}

// looksLikeMP3 reports whether b starts with a valid MP3 file signature
// — either an MPEG frame sync (first 11 bits all set, encoded as
// 0xFF 0xE0..0xFF) or an ID3v2 tag header. The MPEG-1 Layer 3 sync
// uses 0xFB / 0xFA; MPEG-2 Layer 3 uses 0xF3 / 0xF2. ElevenLabs
// emits MPEG-1 Layer 3 at the format we request (mp3_44100_128) so
// 0xFB is the common case, but we accept the full Layer 3 family
// to stay forward-compatible with format-string changes.
func looksLikeMP3(b []byte) bool {
	if len(b) < 3 {
		return false
	}
	if string(b[:3]) == "ID3" {
		return true
	}
	if b[0] != 0xFF {
		return false
	}
	switch b[1] {
	case 0xFB, 0xFA, 0xF3, 0xF2:
		return true
	}
	return false
}

// pickRandomVoice fetches the operator's voice catalog via
// internal/voices and returns one VoiceID picked at random from the
// first 5. The full ListVoices result is not cached here — slides.narrate
// is the only caller and runs once per pack invocation; the
// helmdeck://voices MCP resource (#143) does its own caching.
func pickRandomVoice(ctx context.Context, apiKey string) (string, error) {
	list, err := voices.ListVoices(ctx, apiKey)
	if err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "", fmt.Errorf("no voices available")
	}
	n := len(list)
	if n > 5 {
		n = 5
	}
	return list[rand.Intn(n)].VoiceID, nil
}

// --- ffmpeg helpers ------------------------------------------------------

// generateSilence creates a silent MP3 of the given duration in the
// sidecar. Post-success check: stat the produced file and require it
// be at least minSilenceMp3Bytes. ffmpeg can exit 0 yet leave a 0-byte
// MP3 on disk (mid-write SIGPIPE, temp-disk full, …); without this
// check the empty file would flow into the segment encode and ffmpeg
// would itself exit 0 again with a broken audio stream, defeating
// the post-encode size check we'd otherwise expect to catch it.
func generateSilence(ctx context.Context, ec *packs.ExecutionContext, slideIdx int, seconds float64) error {
	out := fmt.Sprintf("/tmp/audio-%03d.mp3", slideIdx)
	cmd := fmt.Sprintf(
		"ffmpeg -y -f lavfi -i anullsrc=r=44100:cl=mono -t %.3f -acodec libmp3lame %s",
		seconds, out,
	)
	res, err := ec.Exec(ctx, session.ExecRequest{Cmd: []string{"sh", "-c", cmd}})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("exit %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	if perr := requireNonEmptyOutput(ctx, ec, out, minSilenceMp3Bytes,
		fmt.Sprintf("silence-gen slide %d", slideIdx)); perr != nil {
		return perr
	}
	return nil
}

// probeAudioDuration uses ffprobe to measure an audio file's duration.
// ParseFloat accepts "NaN", "Inf", and "-Inf" as valid floats — and on
// locale-affected ffprobe builds, garbage stdout can also yield 0. None
// of those are valid audio durations; we reject them explicitly so the
// caller's "if dur < slideDur fallback" path fires instead of silently
// using a poisoned value for downstream pad/fade math.
func probeAudioDuration(ctx context.Context, ec *packs.ExecutionContext, slideIdx int) (float64, error) {
	cmd := fmt.Sprintf(
		"ffprobe -v error -show_entries format=duration -of csv=p=0 /tmp/audio-%03d.mp3",
		slideIdx,
	)
	res, err := ec.Exec(ctx, session.ExecRequest{Cmd: []string{"sh", "-c", cmd}})
	if err != nil {
		return 0, err
	}
	if res.ExitCode != 0 {
		return 0, fmt.Errorf("ffprobe exit %d", res.ExitCode)
	}
	raw := strings.TrimSpace(string(res.Stdout))
	dur, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("parse duration %q: %w", raw, err)
	}
	if math.IsNaN(dur) || math.IsInf(dur, 0) || dur <= 0 {
		return 0, fmt.Errorf("ffprobe returned non-positive/NaN/Inf duration %q (parsed=%v) — treating as probe failure so the caller falls back to the default slide duration", raw, dur)
	}
	return dur, nil
}

// padSlideAudioToMin (#141) appends silence to /tmp/audio-NNN.mp3 so its
// total duration is at least minSec. Same padding strategy as the
// podcast.generate concat path: generate a silence segment of the
// deficit, concat-demuxer the original + silence, replace the
// original. Re-encoding (libmp3lame) handles frame-size differences
// between ElevenLabs MP3s and our anullsrc-generated ones.
func padSlideAudioToMin(ctx context.Context, ec *packs.ExecutionContext, slideIdx int, currentDur, minSec float64) error {
	deficit := minSec - currentDur
	if deficit <= 0.001 {
		return nil
	}
	turnPath := fmt.Sprintf("/tmp/audio-%03d.mp3", slideIdx)
	padPath := fmt.Sprintf("/tmp/audio-%03d-pad.mp3", slideIdx)
	mergedPath := fmt.Sprintf("/tmp/audio-%03d-padded.mp3", slideIdx)
	listPath := fmt.Sprintf("/tmp/audio-%03d-pad.txt", slideIdx)
	cmds := []string{
		fmt.Sprintf("ffmpeg -y -f lavfi -i anullsrc=r=44100:cl=mono -t %.3f -acodec libmp3lame %s",
			deficit, padPath),
		fmt.Sprintf("printf \"file '%s'\\nfile '%s'\\n\" > %s", turnPath, padPath, listPath),
		fmt.Sprintf("ffmpeg -y -f concat -safe 0 -i %s -acodec libmp3lame -b:a 128k %s",
			listPath, mergedPath),
		fmt.Sprintf("mv %s %s", mergedPath, turnPath),
	}
	for _, cmd := range cmds {
		res, err := ec.Exec(ctx, session.ExecRequest{Cmd: []string{"sh", "-c", cmd}})
		if err != nil {
			return err
		}
		if res.ExitCode != 0 {
			return fmt.Errorf("pad exit %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
		}
	}
	return nil
}

// execWithStdin writes content to a file in the sidecar via stdin.
func execWithStdin(ctx context.Context, ec *packs.ExecutionContext, path string, content []byte) (session.ExecResult, error) {
	return ec.Exec(ctx, session.ExecRequest{
		Cmd:   []string{"sh", "-c", "cat > " + shellQuote(path)},
		Stdin: content,
	})
}

// --- YouTube metadata helper ---------------------------------------------

func generateYouTubeMetadata(ctx context.Context, d vision.Dispatcher, model string, slides []slideContent, durations []float64) (map[string]any, error) {
	maxTokens := 1024
	var userMsg strings.Builder
	userMsg.WriteString("SLIDE DECK:\n\n")

	cumulative := 0.0
	for i, s := range slides {
		ts := formatTimestamp(cumulative)
		content := s.Content
		if content == "" {
			content = "(empty slide)"
		}
		fmt.Fprintf(&userMsg, "Slide %d [%s, %.1fs]:\n%s\n\n", i+1, ts, durations[i], content)
		cumulative += durations[i]
	}
	fmt.Fprintf(&userMsg, "Total duration: %s (%.0f seconds)\n", formatTimestamp(cumulative), cumulative)
	userMsg.WriteString("\nGenerate YouTube metadata for this presentation.")

	req := gateway.ChatRequest{
		Model:     model,
		MaxTokens: &maxTokens,
		Messages: []gateway.Message{
			{Role: "system", Content: gateway.TextContent(narrateYouTubePrompt)},
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

	// Tolerant JSON parse (same pattern as webtest/content_ground).
	var md map[string]any
	if err := json.Unmarshal([]byte(raw), &md); err != nil {
		if obj := extractFirstJSONObject(raw); obj != "" {
			if err2 := json.Unmarshal([]byte(obj), &md); err2 != nil {
				return nil, fmt.Errorf("parse metadata JSON: %w", err2)
			}
		} else {
			return nil, fmt.Errorf("no parseable JSON in metadata response")
		}
	}
	return md, nil
}

// formatTimestamp converts seconds to M:SS format for YouTube timestamps.
func formatTimestamp(seconds float64) string {
	totalSec := int(seconds)
	m := totalSec / 60
	s := totalSec % 60
	return fmt.Sprintf("%d:%02d", m, s)
}

// truncStr truncates a string to n characters, appending "..." if truncated.
func truncStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// persistFfmpegStderr writes the full ffmpeg stderr (and the command
// line that produced it) to the artifact store so operators can grab
// the unredacted output even when the inline error message gets
// truncated. Returns the artifact key, or "" if the artifact store is
// unavailable or the write fails — never errors the caller.
func persistFfmpegStderr(ctx context.Context, ec *packs.ExecutionContext, name, cmd string, stderr []byte) string {
	if ec == nil || ec.Artifacts == nil {
		return ""
	}
	body := []byte("# command:\n" + cmd + "\n\n# stderr:\n")
	body = append(body, stderr...)
	art, err := ec.Artifacts.Put(ctx, "slides.narrate", name, body, "text/plain")
	if err != nil {
		return ""
	}
	return art.Key
}

// artifactSuffix renders the " (full stderr: <key>)" tail used in
// ffmpeg failure messages. Empty string when no artifact was stored,
// so the message stays clean in unit tests that don't wire artifacts.
func artifactSuffix(key string) string {
	if key == "" {
		return ""
	}
	return " (full stderr: " + key + ")"
}

// normalizeSlidesNarrateResolution maps the named-resolution presets
// hyperframes.render and other packs accept ("720p", "1080p",
// "2160p", "4k") onto the ffmpeg-shaped "WIDTHxHEIGHT" syntax the
// per-segment ffmpeg `scale=` filter requires. Pre-formatted
// "WIDTHxHEIGHT" strings pass through; empty input returns empty (the
// caller applies its own default downstream); anything else also
// passes through and lets ffmpeg surface its own "Invalid size"
// error so an operator who passed garbage still sees a useful
// message rather than a silent rewrite.
//
// The motivating case was a real run that failed `handler_failed:
// ffmpeg: Invalid size '1080p'` when the caller passed
// `resolution: "1080p"` — the value was structurally fine for
// hyperframes.render but garbage for ffmpeg. Normalizing here lets
// operators use the same vocabulary across both packs.
func normalizeSlidesNarrateResolution(r string) string {
	switch strings.ToLower(strings.TrimSpace(r)) {
	case "":
		return ""
	case "720p":
		return "1280x720"
	case "1080p":
		return "1920x1080"
	case "1440p":
		return "2560x1440"
	case "2160p", "4k":
		return "3840x2160"
	}
	return r
}

// validateMarpPngs confirms marp produced a non-trivial PNG for every
// expected slide BEFORE the per-segment ffmpeg encode runs. Each PNG
// is statted via `wc -c < /tmp/slides/deck.NNN.png`; a missing file or
// one under minRenderedSlidePngBytes returns a CodeInvalidInput error
// naming the 1-based slide index and pointing at the most likely
// culprit (embedded Mermaid blocks, custom HTML, broken fenced YAML).
// numSlides is the count parseSlidesAndNotes returned — the function
// assumes marp wrote files at the 1-based deck.001.png … deck.NNN.png
// path convention the rest of the handler already relies on. The wc-c
// shell pattern matches the same pattern fs.read uses (see
// fs_packs.go:140-151) so the validation reuses the existing exec path.
//
// After the size check passes, also verifies the first 8 bytes of the
// file match the PNG file signature (pngMagicHex). A Mermaid block can
// fail in a way that produces a >=1024-byte file (e.g. a placeholder
// image with marp's headers) yet the actual image payload is corrupt,
// so the byte-floor alone is insufficient. The magic-byte check uses
// `head -c 8 | od -An -tx1 | tr -d ' \n'` to render the bytes as a
// 16-char lowercase hex string the Go side compares against pngMagicHex.
// Mismatched magic → CodeInvalidInput with the same Mermaid hint as
// the size case, because the operator action is identical (edit the
// slide that caused marp's render to silently fail).
func validateMarpPngs(ctx context.Context, ec *packs.ExecutionContext, numSlides int) error {
	for i := 0; i < numSlides; i++ {
		slideFile := fmt.Sprintf("/tmp/slides/deck.%03d.png", i+1) // marp uses 1-based
		sizeRes, sizeErr := runShell(ctx, ec, "wc -c < "+shellQuote(slideFile), nil)
		if sizeErr != nil {
			return &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("validate slide %d PNG: %v", i+1, sizeErr)}
		}
		if sizeRes.ExitCode != 0 {
			return &packs.PackError{Code: packs.CodeInvalidInput,
				Message: fmt.Sprintf("slide %d produced no rendered PNG (marp exited 0 but the expected output file is missing). Most common cause: an embedded block marp's headless Chromium can't render — a Mermaid diagram (`flowchart`, `sequenceDiagram`), custom HTML with broken CSS, or a fenced YAML that confuses the parser. Edit slide %d's markdown to remove or simplify the offending block, then re-run.",
					i+1, i+1)}
		}
		size, _ := strconv.ParseInt(strings.TrimSpace(string(sizeRes.Stdout)), 10, 64)
		if size < minRenderedSlidePngBytes {
			return &packs.PackError{Code: packs.CodeInvalidInput,
				Message: fmt.Sprintf("slide %d's rendered PNG is only %d bytes (below the %d-byte floor), which is the signature of a silent marp render failure — marp produced an empty/near-empty image because an embedded block (Mermaid `flowchart`, custom HTML, fenced YAML) failed inside its headless Chromium. Edit slide %d's markdown to remove or simplify the offending block, then re-run.",
					i+1, size, minRenderedSlidePngBytes, i+1)}
		}
		// Magic-byte check. A PNG whose size passes the floor but
		// whose first 8 bytes don't match the PNG signature is corrupt
		// — most commonly the residue of a Mermaid render that wrote
		// HTML or partial PNG headers without the deflate payload.
		// ffmpeg would fail downstream on this; surface the real cause.
		magicRes, magicErr := runShell(ctx, ec,
			"head -c 8 "+shellQuote(slideFile)+" | od -An -tx1 | tr -d ' \\n'", nil)
		if magicErr != nil {
			return &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("read slide %d PNG header: %v", i+1, magicErr)}
		}
		if magicRes.ExitCode != 0 {
			// Should not happen given the wc-c stat succeeded, but
			// guard so a partial-IO state surfaces honestly.
			return &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("read slide %d PNG header (exit %d): %s",
					i+1, magicRes.ExitCode, strings.TrimSpace(string(magicRes.Stderr)))}
		}
		gotMagic := strings.TrimSpace(string(magicRes.Stdout))
		if !strings.EqualFold(gotMagic, pngMagicHex) {
			return &packs.PackError{Code: packs.CodeInvalidInput,
				Message: fmt.Sprintf("slide %d's rendered file passed the %d-byte size check but its first 8 bytes (%q) do not match the PNG signature (%q) — marp produced a corrupt/non-PNG output, typically because an embedded block (Mermaid `flowchart`, custom HTML, fenced YAML) failed mid-render and left placeholder content. Edit slide %d's markdown to remove or simplify the offending block, then re-run.",
					i+1, minRenderedSlidePngBytes, gotMagic, pngMagicHex, i+1)}
		}
	}
	return nil
}

// requireNonEmptyOutput stats a file via `wc -c < FILE` and returns a
// CodeHandlerFailed error if the file is missing, the stat call fails
// at the transport layer, or the file is below minBytes. Used by the
// post-encode checks for the per-segment and concat ffmpeg steps to
// catch the "exit 0 but no output" silent-failure mode: ffmpeg returns
// success on certain malformed inputs without producing a real video
// file. label is a human-readable identifier ("ffmpeg segment 4",
// "ffmpeg concat") so the error names the actual step. The wc-c
// pattern matches validateMarpPngs above — no new exec shape.
func requireNonEmptyOutput(ctx context.Context, ec *packs.ExecutionContext, path string, minBytes int64, label string) error {
	sizeRes, sizeErr := runShell(ctx, ec, "wc -c < "+shellQuote(path), nil)
	if sizeErr != nil {
		return &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("%s: stat output %s (transport error): %v", label, path, sizeErr),
			Cause:   sizeErr}
	}
	if sizeRes.ExitCode != 0 {
		return &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("%s: produced no output (expected %s; wc-c stat exited %d: %s). ffmpeg returned exit 0 but the output file is missing — typically the upstream PNG was malformed in a way libavformat could not surface as a non-zero exit. Check the slide PNG that fed this step.",
				label, path, sizeRes.ExitCode, strings.TrimSpace(string(sizeRes.Stderr)))}
	}
	size, _ := strconv.ParseInt(strings.TrimSpace(string(sizeRes.Stdout)), 10, 64)
	if size < minBytes {
		return &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("%s: produced only %d bytes (below the %d-byte floor) at %s. ffmpeg returned exit 0 but the output is too small to be a valid encoded segment — typically a silent libavformat failure on a malformed input PNG.",
				label, size, minBytes, path)}
	}
	return nil
}

// slidesNarrateFfmpegThreads picks the per-segment libx264 thread cap
// from the env var, falling back to the default. Whitespace-only
// values and non-numeric values fall through to the default —
// refusing to boot over a typo would be worse than running with a
// known-safe baseline.
func slidesNarrateFfmpegThreads() string {
	v := strings.TrimSpace(os.Getenv(slidesNarrateFfmpegThreadsEnv))
	if v == "" {
		return slidesNarrateDefaultFfmpegThreads
	}
	if _, err := strconv.Atoi(v); err != nil {
		return slidesNarrateDefaultFfmpegThreads
	}
	return v
}

// ffmpegEncodeOpts collects the knobs the per-segment encoder uses.
// Threads bounds libx264's frame-thread count (typically 4 by default,
// 1 on the OOM-retry path). Preset tunes the speed/quality/memory
// tradeoff — empty string uses libx264's "medium" default; the
// adaptive-retry path uses "veryfast" which cuts encoder memory
// roughly in half at the cost of a measurable but acceptable quality
// hit (CRF 23 still looks fine; the difference is mainly bitrate
// efficiency, not visual artifacts).
type ffmpegEncodeOpts struct {
	Threads string
	Preset  string // "" leaves libx264 default ("medium")
}

// encodeSegment builds and runs the per-segment ffmpeg command with
// the supplied encoder opts. Extracted so the adaptive-retry path can
// re-run with degraded settings without duplicating the command-build
// logic. Returns the raw session.ExecResult so the caller decides
// whether to retry, error, or proceed.
func encodeSegment(ctx context.Context, ec *packs.ExecutionContext, slideFile, audioFile, segFile, vf string, opts ffmpegEncodeOpts) (session.ExecResult, error) {
	presetFlag := ""
	if opts.Preset != "" {
		presetFlag = "-preset " + opts.Preset + " "
	}
	cmd := fmt.Sprintf(
		"ffmpeg -y -loop 1 -i %s -i %s -c:v libx264 -threads %s %s-tune stillimage "+
			"-c:a aac -b:a 192k -vf '%s' -pix_fmt yuv420p -shortest %s",
		shellQuote(slideFile), shellQuote(audioFile), opts.Threads, presetFlag, vf, shellQuote(segFile),
	)
	return ec.Exec(ctx, session.ExecRequest{Cmd: []string{"sh", "-c", cmd}})
}
