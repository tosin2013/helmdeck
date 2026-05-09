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
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tosin2013/helmdeck/internal/gateway"
	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/session"
	"github.com/tosin2013/helmdeck/internal/vault"
	"github.com/tosin2013/helmdeck/internal/vision"
	"github.com/tosin2013/helmdeck/internal/voices"
)

const (
	elevenLabsBaseURL        = "https://api.elevenlabs.io"
	elevenLabsDefaultModelID = "eleven_multilingual_v2"
	elevenLabsDefaultFormat  = "mp3_44100_128"

	defaultSlideDuration = 5.0  // seconds for slides without narration
	maxVideoSize         = 256 << 20 // 256 MiB cap on final video

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
func SlidesNarrate(d vision.Dispatcher, vs *vault.Store) *packs.Pack {
	return &packs.Pack{
		Name:         "slides.narrate",
		Version:      "v1",
		Description:  "Convert a Marp slide deck to a narrated MP4 video with ElevenLabs TTS and YouTube metadata. Requires HELMDECK_ELEVENLABS_API_KEY in .env.local (auto-hydrated to vault as 'elevenlabs-key'); pass allow_silent_output:true to render slides over silence when no key is configured (CI smoke / demo placeholder).",
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
				"voice_used":            "string",
				"metadata_artifact_key": "string",
				"metadata":              "object",
			},
		},
		Handler: slidesNarrateHandler(d, vs),
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
}

func slidesNarrateHandler(d vision.Dispatcher, vs *vault.Store) packs.HandlerFunc {
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

		// Defaults.
		resolution := in.Resolution
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
		slides := parseSlidesAndNotes(in.Markdown)
		if len(slides) == 0 {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "no slides found in markdown"}
		}
		ec.Report(5, fmt.Sprintf("parsed %d slides", len(slides)))

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
		hasNarration := apiKey != ""

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

		// 4. Write markdown to sidecar + export PNGs.
		if _, err := execWithStdin(ctx, ec, "/tmp/helmdeck-deck.md", []byte(in.Markdown)); err != nil {
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
		for i, s := range slides {
			ec.Report(10+float64(i)*40/float64(len(slides)),
				fmt.Sprintf("audio %d/%d", i+1, len(slides)))
			if hasNarration && s.Notes != "" {
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
			cmd := fmt.Sprintf(
				"ffmpeg -y -loop 1 -i %s -i %s -c:v libx264 -tune stillimage "+
					"-c:a aac -b:a 192k -vf '%s' -pix_fmt yuv420p -shortest %s",
				shellQuote(slideFile), shellQuote(audioFile), vf, shellQuote(segFile),
			)
			res, err := ec.Exec(ctx, session.ExecRequest{Cmd: []string{"sh", "-c", cmd}})
			if err != nil || res.ExitCode != 0 {
				stderr := strings.TrimSpace(string(res.Stderr))
				artKey := persistFfmpegStderr(ctx, ec, fmt.Sprintf("ffmpeg-stderr-segment-%03d.txt", i),
					cmd, res.Stderr)
				return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
					Message: fmt.Sprintf("ffmpeg segment %d failed (exit %d): %s%s",
						i, res.ExitCode, truncStr(stderr, 4096), artifactSuffix(artKey))}
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
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("ffmpeg concat failed (exit %d): %s%s",
					concatRes.ExitCode, truncStr(stderr, 4096), artifactSuffix(artKey))}
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
		out := map[string]any{
			"video_artifact_key":    videoArt.Key,
			"video_size":            len(videoBytes),
			"slide_count":           len(slides),
			"total_duration_s":      totalDuration,
			"has_narration":         hasNarration && voiceID != "",
			"voice_used":            voiceID,
			"metadata_artifact_key": metadataKey,
		}
		if metadata != nil {
			out["metadata"] = metadata
		}
		return json.Marshal(out)
	}
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
	return body, nil
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

// generateSilence creates a silent MP3 of the given duration in the sidecar.
func generateSilence(ctx context.Context, ec *packs.ExecutionContext, slideIdx int, seconds float64) error {
	cmd := fmt.Sprintf(
		"ffmpeg -y -f lavfi -i anullsrc=r=44100:cl=mono -t %.3f -acodec libmp3lame /tmp/audio-%03d.mp3",
		seconds, slideIdx,
	)
	res, err := ec.Exec(ctx, session.ExecRequest{Cmd: []string{"sh", "-c", cmd}})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("exit %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}
	return nil
}

// probeAudioDuration uses ffprobe to measure an audio file's duration.
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
	dur, err := strconv.ParseFloat(strings.TrimSpace(string(res.Stdout)), 64)
	if err != nil {
		return 0, fmt.Errorf("parse duration %q: %w", string(res.Stdout), err)
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
