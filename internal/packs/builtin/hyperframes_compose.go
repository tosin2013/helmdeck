// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// hyperframes_compose.go — turn a plain-language DESCRIPTION into a HyperFrames
// HTML/CSS/JS composition that hyperframes.render can turn into an MP4.
//
// Why a separate pack: hyperframes.render only RENDERS an author-supplied
// composition. Writing that composition by hand (a full sized HTML doc with the
// data-* scaffolding and a window.__timelines GSAP registration the upstream CLI
// requires) is a real authoring burden — so callers ended up pasting raw HTML.
// This is the "describe it, the LLM writes it" half, exactly like slides.outline
// turns prose into a Marp deck before slides.render.
//
// Reliability model — the pack guarantees the contract, the LLM fills the
// creative content. The composition contract (verified against the upstream CLI's
// own `blank` template + AGENTS.md) is non-negotiable: a sized <body>, a root
// `<div data-composition-id data-start data-duration data-width data-height>`, and
// a PAUSED GSAP timeline on `window.__timelines["main"]`. We do NOT trust the LLM
// to reproduce that boilerplate (a dropped data-duration or __timelines line makes
// the render fail caller_fixable). Instead the LLM returns only three creative
// pieces — extra CSS, the visible `class="clip"` elements, and the GSAP timeline
// body — and this pack ASSEMBLES the final document around the guaranteed
// scaffolding. So the structural contract holds regardless of model quality.

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/tosin2013/helmdeck/internal/gateway"
	"github.com/tosin2013/helmdeck/internal/llmcontext"
	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/vision"
)

// hyperframesBestPracticesURL is the canonical URL agents (or operators)
// can fetch on demand for richer guidance on what makes a good HyperFrames
// composition vs. a technically-valid one. Embedded in the tier-A/B
// system prompts as the "see the linked guide" anchor; expanded on at
// docs/reference/packs/hyperframes/best-practices.md.
const hyperframesBestPracticesURL = "https://helmdeck.dev/reference/packs/hyperframes/best-practices"

const (
	hyperframesComposeDefaultDuration = 8.0
	hyperframesComposeMaxDuration     = 720.0 // 12 min — matches hyperframes.render's cap
	// 6144 gives headroom so a chatty model finishes all three sections; a
	// verbose STYLES block at 4096 truncated before ===BODY===.
	hyperframesComposeDefaultTokens  = 6144
	hyperframesComposeMaxTokensFloor = 2048
	hyperframesComposeMaxTokensCeil  = 8192
	// gsapCDN is the exact GSAP build the upstream CLI's `blank` template loads.
	gsapCDN = "https://cdn.jsdelivr.net/npm/gsap@3.14.2/dist/gsap.min.js"

	// JIT length-sizing constants (issue #529 / convention #525). Unlike
	// blog and podcast where source word count drives the target, the
	// hyperframes pack has no inherent "source → output length"
	// relationship (the description is a planning instruction, not
	// source material). Intent therefore picks a fixed duration from
	// the table; description word count is reported for transparency
	// but doesn't scale the choice.
	hyperframesComposeIntentSummary    = "summary"
	hyperframesComposeIntentThorough   = "thorough"
	hyperframesComposeIntentExhaustive = "exhaustive"
	hyperframesComposeIntentDefault    = hyperframesComposeIntentThorough

	// hyperframesComposeSystemPromptTierC is the Tier C (free / weak open
	// model) system prompt. Constraint-heavy, compact, leads with the
	// hard rules verbatim because Tier C models reliably honor explicit
	// rules and unreliably honor "remember to" guidance.
	//
	// Rules are sourced from the upstream HyperFrames AGENTS.md / SKILL.md
	// research surfaced during PR #504, not synthesized. In particular:
	// the data-track-index temporal-exclusion rule, the layout-first
	// pattern (write static hero frame, then animate), the
	// gsap.from/gsap.to entrance/exit convention (so a failed timeline
	// reverts to a readable steady state), and the static-volume audio
	// constraint are all upstream-documented hard rules — not opinions.
	//
	// %d=width %d=height %g=duration %s=audio-note %s=style-note %g=duration %d=width %d=height.
	hyperframesComposeSystemPromptTierC = `You design short HTML video compositions for the HyperFrames renderer (Chromium + GSAP). You will be given a DESCRIPTION of the video to make.

The canvas is EXACTLY %d×%d pixels and the video is %g seconds long. Animate with GSAP into a single paused timeline variable named ` + "`tl`" + ` (already declared for you — just add tweens to it).%s%s

Respond with EXACTLY these three sections in THIS ORDER and NOTHING else. Put each marker on its OWN line and write raw CSS / HTML / JS between them — NO JSON, NO escaping, NO markdown fences. ALWAYS finish BODY and TIMELINE before STYLES — they are required; STYLES is the least important:

===BODY===
(the visible elements that go inside the root div)
===TIMELINE===
(GSAP statements that add tweens to the existing 'tl' timeline)
===STYLES===
(a FEW concise extra CSS rules — keep this short; the canvas sizing + reset are already provided)

Hard rules (upstream HyperFrames hard rules — the render fails if you break them):

- LAYOUT FIRST, ANIMATION SECOND: design the static "hero frame" (the moment the composition rests in its final state) using CSS flex + gap + padding INSIDE the elements. Do NOT use position:absolute / top:Npx on content containers — upstream prohibits this because content overflows uncontrollably when taller than the viewport. After the hero frame is structurally sound, add gsap.from() entrance tweens (animate TO the established CSS position FROM an off-screen offset) and gsap.to() exit tweens (animate AWAY from the steady state). This convention ensures a paused or failed timeline reverts to a readable layout, not a chaotic jumble.

- class="clip" CONTRACT: every visible, timed element MUST have class="clip" and the integer attributes data-start, data-duration, data-track-index. data-start+data-duration must stay within 0..%g. Give each element a unique id you reference from the timeline.

- TIMELINE COVERAGE: the union of every class="clip" element's [data-start, data-start+data-duration) interval MUST cover the ENTIRE [0, %g) range without gaps. The first element starts at data-start="0". If you don't have a foreground element to fill a span, add a permanent background element (e.g. a solid-color full-canvas div with data-start="0" data-duration="%g") so the rendered video is never blank.

- data-track-index IS TEMPORAL, NOT SPATIAL: track-index is a non-linear-editor row, NOT a CSS z-index. Clips on the SAME integer track MUST NOT temporally overlap — they must be sequential. To stack visuals at the same moment in time, put them on DIFFERENT tracks and use CSS z-index for spatial layering. Upstream convention: data-track-index="0" for backgrounds, "1" for primary scenes, "9+" for audio elements.

- AUDIO VOLUME IS IMMUTABLE: if you embed an <audio> element, set data-volume="<float 0.0-1.0>" once at element declaration. NEVER tween audio volume with GSAP (e.g. tl.to(audio, { volume: 0.5 })) — the engine multiplexes audio post-capture and silently ignores volume tweens. Bake fades into the audio file upstream of compose.

- Position visual elements inside the %d×%d canvas; use large, legible type (this is video, not a web page).

- Keep STYLES brief — a handful of rules, plain colors and simple gradients; do NOT write long or elaborate CSS (it can truncate the response).

- DETERMINISTIC ONLY: no Date.now(), no performance.now(), no Math.random() (use a seeded PRNG if you need stochasticity), no network/fetch, no external images or fonts. The capture engine steps time artificially; live clocks desynchronize frames.

- Do NOT emit <html>, <head>, <body>, <style> tags, the root div, the GSAP <script>, or the window.__timelines line — those are added for you. Only the three marked sections above.`

	// hyperframesComposeSystemPromptTierAB is the Tier A/B (frontier /
	// mid-tier hosted model) system prompt. Leaner: the model is trusted
	// to honor concise instructions and to fetch the linked best-practices
	// guide. The hard contract rules are the same as Tier C — any model
	// that breaks them makes the render fail caller_fixable regardless
	// of capability — but the prompt leans on the doc URL for the full
	// upstream-sourced ruleset rather than reproducing it verbatim.
	// %d=width %d=height %g=duration %s=audio-note %s=style-note %g=duration %d=width %d=height %s=best-practices-url.
	hyperframesComposeSystemPromptTierAB = `You design short HTML video compositions for the HyperFrames renderer (Chromium + GSAP). You will be given a DESCRIPTION of the video to make.

The canvas is %d×%d pixels and the video is %g seconds long. Animate with GSAP into a single paused timeline named ` + "`tl`" + ` (declared for you — just add tweens).%s%s

Respond with three marker-delimited sections in order: ===BODY===, ===TIMELINE===, ===STYLES=== (no JSON, no fences, no escaping). Finish BODY and TIMELINE before STYLES.

Upstream HyperFrames hard contract (the render fails if you break it):
- Layout first: design the static hero frame with flex/gap/padding, NOT position:absolute on content containers. Animate with gsap.from() for entrances + tl.to() for exits so the steady state is readable when paused.
- class="clip" + integer data-start, data-duration, data-track-index. data-start+data-duration must stay within 0..%g.
- Timeline coverage: the union of every clip's [data-start, data-start+data-duration) MUST cover [0, %g) — add a permanent background at data-start="0" data-duration="%g" if foreground has gaps.
- data-track-index is TEMPORAL (NLE row semantics), not Z-order. Clips on the same track MUST NOT overlap temporally; stack visuals at the same moment by putting them on DIFFERENT tracks + using CSS z-index. Convention: 0=bg, 1=primary, 9+=audio.
- Audio: data-volume is static and immutable — volume tweens are silently ignored. Bake fades upstream.
- Deterministic only: no Date.now, no Math.random (use seeded PRNG), no network/fetch, no external resources.
- Scaffolding is added by the pack: do NOT emit <html>/<head>/<body>/<style>, the root div, the GSAP <script>, or the window.__timelines line.

Canvas: %d×%d. For the full upstream-sourced ruleset (seven-step pipeline, reference template catalog, attribute vocabulary, documented failure modes, audio-reactive FFT pre-extraction pattern, React migration constraints, ARM64 deployment escape hatch), see the helmdeck-side guide at %s — it cites the upstream HyperFrames AGENTS.md / SKILL.md and is the canonical reference.`

	composeSecStyles   = "===STYLES==="
	composeSecBody     = "===BODY==="
	composeSecTimeline = "===TIMELINE==="
)

// hyperframesComposeIntentRow holds per-intent duration parameters in
// seconds. Mirrors podcastIntentRow's shape, but here the "chosen"
// value is the row's own target (not a multiplier × source word count)
// because there's no length-scaling source signal to apply.
type hyperframesComposeIntentRow struct {
	target  float64
	floor   float64
	ceiling float64
}

// hyperframesComposeIntentTable per issue #529. Numbers are defaults
// to revisit as empirical data lands. Ceilings respect the upstream
// hyperframes.render 12-minute cap.
var hyperframesComposeIntentTable = map[string]hyperframesComposeIntentRow{
	hyperframesComposeIntentSummary:    {target: 60, floor: 30, ceiling: 120},
	hyperframesComposeIntentThorough:   {target: 180, floor: 120, ceiling: 360},
	hyperframesComposeIntentExhaustive: {target: 600, floor: 360, ceiling: 720},
}

// hyperframesComposeSize captures the chosen duration + the path that
// produced it. Mirrors podcastSize / blogRewriteSize so the convention
// stays recognizable across packs.
type hyperframesComposeSize struct {
	chosen  float64
	applied string // intent:thorough / explicit / explicit:audio-locked / default:legacy-8sec
}

// sizeForHyperframesComposeIntent picks a duration from the intent
// table. Floor/ceiling are reported but don't currently clamp the
// chosen value — that's reserved for future scaling rules (e.g.
// description-richness-based adjustments).
func sizeForHyperframesComposeIntent(intent string) hyperframesComposeSize {
	key := strings.ToLower(strings.TrimSpace(intent))
	if key == "" {
		key = hyperframesComposeIntentDefault
	}
	row, ok := hyperframesComposeIntentTable[key]
	if !ok {
		row = hyperframesComposeIntentTable[hyperframesComposeIntentDefault]
		key = hyperframesComposeIntentDefault
	}
	return hyperframesComposeSize{chosen: row.target, applied: "intent:" + key}
}

// resolveHyperframesComposeSize encodes the precedence:
//  1. audio_url present → explicit DurationSeconds required (existing
//     hard-fail upstream of this call); reported as "explicit:audio-locked"
//  2. DurationSeconds > 0 → explicit numeric ("explicit")
//  3. LengthIntent set → intent table
//  4. Default → legacy 8-second default ("default:legacy-8sec")
//
// The legacy default branch preserves back-compat — existing silent
// micro-animation callers passing neither numeric nor intent see ZERO
// behavior change.
func resolveHyperframesComposeSize(in *hyperframesComposeInput) hyperframesComposeSize {
	hasAudio := strings.TrimSpace(in.AudioURL) != ""
	if hasAudio && in.DurationSeconds > 0 {
		return hyperframesComposeSize{chosen: in.DurationSeconds, applied: "explicit:audio-locked"}
	}
	if in.DurationSeconds > 0 {
		return hyperframesComposeSize{chosen: in.DurationSeconds, applied: "explicit"}
	}
	if strings.TrimSpace(in.LengthIntent) != "" {
		return sizeForHyperframesComposeIntent(in.LengthIntent)
	}
	return hyperframesComposeSize{chosen: hyperframesComposeDefaultDuration, applied: "default:legacy-8sec"}
}

type hyperframesComposeInput struct {
	Description     string  `json:"description"`
	Model           string  `json:"model"`
	AspectRatio     string  `json:"aspect_ratio"`
	Resolution      string  `json:"resolution"`
	DurationSeconds float64 `json:"duration_seconds"`
	AudioURL        string  `json:"audio_url"`
	Style           string  `json:"style"`
	MaxTokens       int     `json:"max_tokens"`
	// MetadataModelRaw is a string-ptr-shaped opt-in/opt-out for video
	// engagement metadata generation (title, hook, hashtags, etc.).
	// Pointer semantics distinguish three states:
	//   nil          → default to "openrouter/auto" (paid; safe default
	//                  matching podcast.generate's behavior)
	//   ""           → operator explicitly disabled engagement gen
	//   "<model id>" → use the named model (e.g. the agent's own free
	//                  model for end-to-end free-tier discipline)
	// Mirrors podcast.generate's MetadataModelRaw pattern.
	MetadataModelRaw *string `json:"metadata_model"`

	// JIT length-sizing inputs (issue #529 / convention #525).
	// LengthIntent declares "summary" / "thorough" / "exhaustive";
	// pack picks a duration from the intent table when no explicit
	// duration_seconds is set. audio_url with explicit duration takes
	// precedence regardless. See resolveHyperframesComposeSize for the
	// full precedence order.
	LengthIntent string `json:"length_intent,omitempty"`
	// Inspect: return the planned duration + description word count
	// without calling the dispatcher. Useful when an agent wants to
	// see what the pack would pick before committing tokens.
	Inspect bool `json:"inspect,omitempty"`
}

// composeSpec is the creative payload the model returns; the pack assembles the
// final composition document around the guaranteed scaffolding.
type composeSpec struct {
	Styles   string `json:"styles"`
	Body     string `json:"body"`
	Timeline string `json:"timeline"`
}

// HyperframesCompose constructs the pack. Dispatcher-gated like slides.outline;
// no session needed (it only calls the gateway), so it's registered in the same
// dispatcher block and chained into a pipeline before hyperframes.render.
func HyperframesCompose(d vision.Dispatcher) *packs.Pack {
	return &packs.Pack{
		Name:        "hyperframes.compose",
		Version:     "v1",
		Description: "Generate a HyperFrames HTML/CSS/JS video composition from a plain-language description, ready for hyperframes.render. The pack guarantees the render contract (sized canvas, data-* scaffolding, a paused GSAP window.__timelines registration); the model only writes the creative visuals. Pass audio_url (e.g. a podcast.generate presigned URL) for a narrated video.",
		Metadata: packs.PackMetadata{
			Accepts:        []string{"description", "audio_url"},
			Produces:       []string{"composition_html"},
			IntentKeywords: []string{"compose video", "design animated visual", "describe a video", "make explainer animation", "short video", "thorough explainer video", "exhaustive explainer video"},
			TypicalUse:     "Generator pack for video compositions. Chain hyperframes.render after for an MP4. Pair with podcast.generate's audio_url for a narrated video (the prompt-narrated-video pipeline). Use length_intent (summary / thorough / exhaustive) to pick a duration without specifying seconds; explicit duration_seconds always wins.",
			Limitations:    []string{"does not render — outputs HTML/CSS/JS only; chain hyperframes.render", "visual design is LLM-driven; no fine-grained timeline control via inputs", "audio sync depends on duration_seconds being correct", "truncated:true signals the composition-HTML LLM hit max_tokens — re-run with a richer description or smaller length_intent"},
		},
		InputSchema: packs.BasicSchema{
			// model is required at runtime by the handler for the
			// generate path, but NOT in the schema — inspect mode
			// short-circuits without calling the model and would
			// otherwise be rejected by the BasicSchema validator
			// before the handler ran. Runtime check below picks up
			// non-inspect calls that omit model.
			Required: []string{"description"},
			Properties: map[string]string{
				"description":      "string",
				"model":            "string",
				"aspect_ratio":     "string",
				"resolution":       "string",
				"duration_seconds": "number",
				"audio_url":        "string",
				"style":            "string",
				"max_tokens":       "number",
				// metadata_model: string-ptr-shaped opt-in for engagement
				// metadata (default "openrouter/auto"; "" disables).
				"metadata_model": "string",
				// JIT length-sizing (issue #529).
				"length_intent": "string",
				"inspect":       "boolean",
			},
		},
		OutputSchema: packs.BasicSchema{
			// Narrowed from {composition_html, model, width, height}
			// to just composition_html so inspect mode (which doesn't
			// emit width/height/model — no composition is built) can
			// satisfy the validator. The four-field requirement was
			// validator-friendly but excluded the inspect path; the
			// narrow form keeps generate behavior intact.
			Required: []string{"composition_html"},
			Properties: map[string]string{
				"composition_html": "string",
				"model":            "string",
				"aspect_ratio":     "string",
				"width":            "number",
				"height":           "number",
				"duration_seconds": "number",
				"has_audio":        "boolean",
				"duration_source":  "string",
				// engagement: duration-band-aware object (short_form /
				// mid_form / long_form). engagement_artifact_key: stable
				// key to the JSON sidecar with the same payload, useful
				// when chaining downstream packs.
				"engagement":              "object",
				"engagement_artifact_key": "string",
				// JIT length-sizing telemetry (issue #529).
				"description_words":           "number",
				"target_duration_sec_chosen":  "number",
				"length_intent_applied":       "string",
				"truncated":                   "boolean",
				// Inspect mode only.
				"inspect":                "boolean",
				"suggested_duration_sec": "number",
				"reason":                 "string",
			},
		},
		Handler: hyperframesComposeHandler(d),
		Async:   true,
	}
}

func hyperframesComposeHandler(d vision.Dispatcher) packs.HandlerFunc {
	return func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
		var in hyperframesComposeInput
		if err := json.Unmarshal(ec.Input, &in); err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
		}
		if strings.TrimSpace(in.Description) == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "description is required"}
		}
		// JIT inspect short-circuit (issue #529). Runs before the
		// dispatcher / model-required checks so gateway-less and
		// dispatcher-less environments can plan a composition. The
		// audio_url + duration_seconds hard rule is also skipped:
		// inspect doesn't render, so there's nothing to truncate.
		if in.Inspect {
			size := resolveHyperframesComposeSize(&in)
			descriptionWords := countWords(in.Description)
			reason := fmt.Sprintf("description is %d words; applying %s for a duration of %.0f seconds",
				descriptionWords, size.applied, size.chosen)
			return json.Marshal(map[string]any{
				// composition_html populated empty to satisfy
				// OutputSchema's Required list.
				"composition_html":       "",
				"model":                  in.Model,
				"inspect":                true,
				"description_words":      descriptionWords,
				"suggested_duration_sec": size.chosen,
				"length_intent_applied":  size.applied,
				"reason":                 reason,
			})
		}
		if d == nil {
			return nil, &packs.PackError{Code: packs.CodeInternal,
				Message: "hyperframes.compose registered without a gateway dispatcher"}
		}
		if strings.TrimSpace(in.Model) == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "model is required (provider/model id; see helmdeck://models)"}
		}

		resolution := in.Resolution
		if resolution == "" {
			resolution = "1080p"
		}
		aspectRatio := in.AspectRatio
		if aspectRatio == "" {
			aspectRatio = "16:9"
		}
		// Reuse hyperframes.render's preset matrix so the canvas dimensions match
		// exactly what the renderer expects for this resolution × aspect_ratio
		// (a mismatch is the #1 caller_fixable render failure).
		preset, err := resolvePreset(resolution, aspectRatio)
		if err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
		}

		// When audio_url is provided, duration_seconds is required.
		// The pack's default (hyperframesComposeDefaultDuration = 8s) is
		// only correct for silent micro-animations; chaining a narrated
		// audio source without an explicit duration silently truncates
		// the audio at the 8-second timeline boundary in the rendered
		// MP4 — and downstream av.validate's audio_video_duration check
		// passes trivially because both clipped at the same point.
		// Issue #498 documents the empirical repro. Fail caller_fixable
		// here instead of producing a silently-broken video.
		hasAudioInput := strings.TrimSpace(in.AudioURL) != ""
		if hasAudioInput && in.DurationSeconds <= 0 {
			return nil, &packs.PackError{
				Code: packs.CodeInvalidInput,
				Message: "duration_seconds is required when audio_url is provided. " +
					"Pass the audio's duration in seconds (e.g. podcast.generate's `duration_s` output, " +
					"rounded up). Without it, the composition timeline defaults to 8s and would silently " +
					"truncate longer narration tracks.",
			}
		}
		// JIT length-sizing (issue #529). Precedence: audio + explicit
		// duration → explicit numeric → length_intent → legacy 8s
		// default. The legacy fallback preserves back-compat — silent
		// micro-animation callers passing neither numeric nor intent
		// see ZERO behavior change.
		size := resolveHyperframesComposeSize(&in)
		duration := size.chosen
		if duration > hyperframesComposeMaxDuration {
			duration = hyperframesComposeMaxDuration
		}

		maxTokens := in.MaxTokens
		if maxTokens <= 0 {
			maxTokens = hyperframesComposeDefaultTokens
		}
		if maxTokens < hyperframesComposeMaxTokensFloor {
			maxTokens = hyperframesComposeMaxTokensFloor
		}
		if maxTokens > hyperframesComposeMaxTokensCeil {
			maxTokens = hyperframesComposeMaxTokensCeil
		}

		hasAudio := strings.TrimSpace(in.AudioURL) != ""
		audioNote := ""
		if hasAudio {
			audioNote = " A narration audio track is added for you and runs the full duration; pace your visuals to it."
		}
		styleNote := ""
		if s := strings.TrimSpace(in.Style); s != "" {
			styleNote = " Visual style: " + s + "."
		}
		system := composeSystemPromptFor(in.Model, preset.Width, preset.Height, duration, audioNote, styleNote)

		ec.Report(10, fmt.Sprintf("composing a %dx%d video", preset.Width, preset.Height))
		mt := maxTokens
		chat, err := d.Dispatch(ctx, gateway.ChatRequest{
			Model:     in.Model,
			MaxTokens: &mt,
			Messages: []gateway.Message{
				{Role: "system", Content: gateway.TextContent(system)},
				{Role: "user", Content: gateway.TextContent(in.Description)},
			},
		})
		if err != nil {
			return nil, dispatchError("hyperframes.compose gateway", err)
		}
		if len(chat.Choices) == 0 {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed, Message: "gateway returned no choices"}
		}

		finishReason := chat.Choices[0].FinishReason
		raw := unwrapCodeFence(strings.TrimSpace(chat.Choices[0].Message.Content.Text()))
		spec, perr := parseComposeSpec(raw)
		if perr != nil {
			return nil, perr
		}
		if strings.TrimSpace(spec.Body) == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: "the model returned no visible elements (empty body) — give a richer description or a different model"}
		}

		// Timeline-coverage check. A composition whose class="clip"
		// elements don't span the full [0, duration) renders as a
		// MP4 with visible black runs (the body's reset background is
		// #000). av.validate flags the result but the operator has
		// already paid for the render + audio. Catching the gap here
		// at compose-time costs only the LLM retry and keeps the chain
		// fail-loud rather than fail-late. Issue surfaced 2026-06-13;
		// reported as a 1 black run ≥2s warn against an 8s test video.
		//
		// Threshold: min(2.0s, duration*0.05). For a 60s video that's
		// 2.0s; for an 8s video it's 0.4s; for a 720s video it's 2.0s.
		// Below the threshold is "visual transition tolerance"; above
		// it's a real failure mode worth surfacing.
		allowedGap := 2.0
		if v := duration * 0.05; v < allowedGap {
			allowedGap = v
		}
		if hasGap, gapStart, gapEnd := composeCoverageGap(spec.Body, duration, allowedGap); hasGap {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: fmt.Sprintf(
					"composition has a %.1fs–%.1fs blank-screen gap (no class=\"clip\" element covers that range) in a %gs video; the rendered MP4 would show %.1fs of black there. Add a permanent background element (e.g. a solid-color full-canvas div with data-start=\"0\" data-duration=\"%g\") or extend existing element durations to cover the timeline.",
					gapStart, gapEnd, duration, gapEnd-gapStart, duration)}
		}

		// Track-index collision check (upstream HyperFrames hard rule).
		// Per the upstream AGENTS.md research surfaced in PR #504: clips
		// sharing the same integer data-track-index MUST NOT temporally
		// overlap. Track is a non-linear-editor-style row, not a
		// z-index. Visual layering is governed by CSS z-index entirely
		// independent of track-index. If the upstream auditor would
		// reject the composition with a track-collision error, we reject
		// it here so the operator doesn't pay for a downstream render
		// that the producer pipeline would fail anyway.
		if collide, trackIdx, aStart, aEnd, bStart, bEnd := composeTrackCollision(spec.Body); collide {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: fmt.Sprintf(
					"data-track-index=\"%d\" has two temporally-overlapping clips ([%.1fs, %.1fs) and [%.1fs, %.1fs)). Upstream HyperFrames forbids overlap on the same track — clips on the same integer track must be sequential. To stack visuals at the same time, put them on DIFFERENT tracks and use CSS z-index for spatial layering. Convention: data-track-index=\"0\" for backgrounds, 1 for primary scenes, 9+ for audio.",
					trackIdx, aStart, aEnd, bStart, bEnd)}
		}

		durationSource := "timeline"
		if hasAudio {
			durationSource = "audio"
		}
		composition := assembleComposition(preset.Width, preset.Height, duration, in.AudioURL, spec)

		out := map[string]any{
			"composition_html": composition,
			"model":            in.Model,
			"aspect_ratio":     aspectRatio,
			"width":            preset.Width,
			"height":           preset.Height,
			"duration_seconds": duration,
			"has_audio":        hasAudio,
			"duration_source":  durationSource,
			// JIT length-sizing telemetry (issue #529). Always
			// reported on the generate path so callers can compare
			// chosen target against actual rendered length and
			// detect truncation of the composition-HTML LLM call.
			"description_words":          countWords(in.Description),
			"target_duration_sec_chosen": size.chosen,
			"length_intent_applied":      size.applied,
			"truncated":                  strings.EqualFold(finishReason, "length"),
		}

		// Engagement metadata — duration-band-aware, opt-out via empty
		// string, default to "openrouter/auto" (matches podcast.generate).
		// Failures soft-degrade: we log + skip rather than fail the
		// composition itself, mirroring the slides.narrate / podcast.generate
		// pattern. The composition_html is the load-bearing output; engagement
		// is a value-add sidecar.
		var engagementModel string
		switch {
		case in.MetadataModelRaw == nil:
			engagementModel = "openrouter/auto"
		case *in.MetadataModelRaw == "":
			engagementModel = "" // operator explicitly disabled
		default:
			engagementModel = *in.MetadataModelRaw
		}
		if d != nil && engagementModel != "" {
			band := composeEngagementBand(duration)
			ec.Report(98, "generating "+band+" engagement metadata")
			engagement, err := generateComposeEngagement(ctx, d, engagementModel, band, in.Description, in.AudioURL, duration)
			if err != nil {
				if ec.Logger != nil {
					ec.Logger.Warn("hyperframes.compose engagement generation failed", "err", err, "band", band)
				}
			} else {
				out["engagement"] = engagement
				if ec.Artifacts != nil {
					if mdBytes, err := json.Marshal(engagement); err == nil {
						if art, err := ec.Artifacts.Put(ctx, "hyperframes.compose", "engagement.json", mdBytes, "application/json"); err == nil {
							out["engagement_artifact_key"] = art.Key
						} else if ec.Logger != nil {
							ec.Logger.Warn("hyperframes.compose engagement artifact upload failed", "err", err)
						}
					}
				}
			}
		}

		return json.Marshal(out)
	}
}

// parseComposeSpec splits the model's marker-delimited reply into the three
// creative sections. Lenient: any preamble before the first marker is ignored,
// and STYLES/TIMELINE are optional (only BODY is required). Raw CSS/HTML/JS
// between markers needs no escaping — the reason we dropped the JSON format,
// which broke whenever the body/timeline contained quotes or newlines.
func parseComposeSpec(raw string) (composeSpec, *packs.PackError) {
	iStyles := strings.Index(raw, composeSecStyles)
	iBody := strings.Index(raw, composeSecBody)
	iTimeline := strings.Index(raw, composeSecTimeline)
	if iBody < 0 {
		snippet := raw
		if len(snippet) > 256 {
			snippet = snippet[:256] + "…"
		}
		return composeSpec{}, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: fmt.Sprintf("model did not return the expected ===STYLES===/===BODY===/===TIMELINE=== sections (try a more capable model or a richer description): %s", snippet)}
	}
	// Order-agnostic: each section runs from its marker to the NEXT marker
	// (whichever comes next), so it doesn't matter what order the model emits
	// them, and a truncated tail just yields a shorter/empty optional section.
	sectionEnd := func(start int) int {
		end := len(raw)
		for _, o := range []int{iStyles, iBody, iTimeline} {
			if o > start && o < end {
				end = o
			}
		}
		return end
	}
	var s composeSpec
	if iStyles >= 0 {
		s.Styles = composeSection(raw, iStyles+len(composeSecStyles), sectionEnd(iStyles))
	}
	s.Body = composeSection(raw, iBody+len(composeSecBody), sectionEnd(iBody))
	if iTimeline >= 0 {
		s.Timeline = composeSection(raw, iTimeline+len(composeSecTimeline), sectionEnd(iTimeline))
	}
	return s, nil
}

// composeSection returns the trimmed slice s[start:end], guarding the bounds.
func composeSection(s string, start, end int) string {
	if start < 0 || end > len(s) || start > end {
		return ""
	}
	return strings.TrimSpace(s[start:end])
}

// assembleComposition builds the final HyperFrames document around the guaranteed
// contract scaffolding: sized canvas, root div with the required data-* attributes,
// an optional narration <audio> element, and the paused window.__timelines["main"]
// registration. Only spec.Styles / spec.Body / spec.Timeline come from the model.
func assembleComposition(w, h int, duration float64, audioURL string, spec composeSpec) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<!doctype html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=%d, height=%d" />
    <script src="%s"></script>
    <style>
      * { margin: 0; padding: 0; box-sizing: border-box; }
      html, body { width: %dpx; height: %dpx; overflow: hidden; background: #000; }
%s
    </style>
  </head>
  <body>
    <div id="root" data-composition-id="main" data-start="0" data-duration="%g" data-width="%d" data-height="%d">
%s
`, w, h, gsapCDN, w, h, spec.Styles, duration, w, h, spec.Body)

	if strings.TrimSpace(audioURL) != "" {
		fmt.Fprintf(&b, `      <audio id="a-roll-audio" src="%s" data-start="0" data-duration="%g" data-track-index="2" data-volume="1"></audio>
`, audioURL, duration)
	}

	fmt.Fprintf(&b, `    </div>
    <script>
      window.__timelines = window.__timelines || {};
      const tl = gsap.timeline({ paused: true });
%s
      window.__timelines["main"] = tl;
    </script>
  </body>
</html>
`, spec.Timeline)
	return b.String()
}

// composeSystemPromptFor picks the system prompt template appropriate to
// the caller-supplied model's tier (per the existing llmcontext budget
// registry). Tier C → compact, constraint-heavy verbatim rules + an
// inline TIMELINE COVERAGE warning (issue #498's blank-run failure mode
// closed for that tier the same way #498 closed silent-truncation:
// loud, explicit, rules-as-success-criteria). Tier A/B → leaner prompt
// that trusts the model to honor the contract and references the
// best-practices guide for richer composition design.
//
// The contract rules (canvas size, deterministic-only, timeline coverage)
// are identical across tiers — any model that breaks them makes the
// render fail caller_fixable. What differs is the depth of guidance.
func composeSystemPromptFor(model string, w, h int, duration float64, audioNote, styleNote string) string {
	tier := llmcontext.BudgetFor(model).Tier
	if tier == llmcontext.TierA || tier == llmcontext.TierB {
		return fmt.Sprintf(hyperframesComposeSystemPromptTierAB,
			w, h, duration, audioNote, styleNote,
			duration, duration, duration,
			w, h, hyperframesBestPracticesURL)
	}
	// TierC + unknown-tier fallback both use the constraint-heavy template.
	return fmt.Sprintf(hyperframesComposeSystemPromptTierC,
		w, h, duration, audioNote, styleNote,
		duration, duration, duration,
		w, h)
}

// composeClipAttrRE captures the data-start and data-duration values on
// every class="clip" element in the body. Matches attribute pairs
// regardless of order within the element; matches both quoted and
// unquoted attribute values, and tolerates whitespace around `=`.
var composeClipAttrRE = regexp.MustCompile(
	`class\s*=\s*"clip"[^>]*?data-start\s*=\s*"([0-9.]+)"[^>]*?data-duration\s*=\s*"([0-9.]+)"|` +
		`class\s*=\s*"clip"[^>]*?data-duration\s*=\s*"([0-9.]+)"[^>]*?data-start\s*=\s*"([0-9.]+)"`)

// composeCoverageGap inspects the body for every class="clip" element's
// [data-start, data-start+data-duration) interval, computes their union,
// and returns (true, gapStart, gapEnd) if any contiguous gap longer than
// allowedGap seconds exists within [0, duration). Returns (false, 0, 0)
// when the timeline is fully covered.
//
// The check is conservative — it operates on the raw HTML and doesn't
// account for GSAP opacity / transform tweens that could make an
// "active" element invisible. Those edge cases are covered by the
// best-practices guide; this check closes the blunt failure mode where
// the model emits a clip with data-start="0" data-duration="2" and the
// remaining timeline is empty.
//
// The empty-body case (no class="clip" elements) returns gap=duration
// so the handler can surface it via the existing empty-body check first.
func composeCoverageGap(body string, duration, allowedGap float64) (bool, float64, float64) {
	matches := composeClipAttrRE.FindAllStringSubmatch(body, -1)
	if len(matches) == 0 {
		return false, 0, 0 // empty body handled separately
	}
	type interval struct{ start, end float64 }
	intervals := make([]interval, 0, len(matches))
	for _, m := range matches {
		// The regex has two alternations; pick whichever capture group fired.
		var startStr, durStr string
		switch {
		case m[1] != "" && m[2] != "":
			startStr, durStr = m[1], m[2]
		case m[3] != "" && m[4] != "":
			startStr, durStr = m[4], m[3]
		default:
			continue
		}
		start, err1 := strconv.ParseFloat(startStr, 64)
		dur, err2 := strconv.ParseFloat(durStr, 64)
		if err1 != nil || err2 != nil || dur <= 0 {
			continue
		}
		end := start + dur
		if end > duration {
			end = duration
		}
		if start < 0 {
			start = 0
		}
		if start >= duration {
			continue
		}
		intervals = append(intervals, interval{start, end})
	}
	if len(intervals) == 0 {
		return false, 0, 0
	}
	sort.Slice(intervals, func(i, j int) bool { return intervals[i].start < intervals[j].start })
	// Walk merged intervals tracking the cursor as the "covered up to" point.
	cursor := 0.0
	for _, iv := range intervals {
		if iv.start-cursor > allowedGap {
			return true, cursor, iv.start
		}
		if iv.end > cursor {
			cursor = iv.end
		}
	}
	if duration-cursor > allowedGap {
		return true, cursor, duration
	}
	return false, 0, 0
}

// composeClipTrackRE captures the data-track-index value alongside
// data-start and data-duration on every class="clip" element. Matches
// all six attribute-order permutations of the three attributes within a
// single element open tag. Sibling to composeClipAttrRE but track-aware.
var composeClipTrackRE = regexp.MustCompile(
	`class\s*=\s*"clip"[^>]*?` +
		`(?:` +
		`data-start\s*=\s*"([0-9.]+)"[^>]*?data-duration\s*=\s*"([0-9.]+)"[^>]*?data-track-index\s*=\s*"([0-9-]+)"` + `|` +
		`data-start\s*=\s*"([0-9.]+)"[^>]*?data-track-index\s*=\s*"([0-9-]+)"[^>]*?data-duration\s*=\s*"([0-9.]+)"` + `|` +
		`data-duration\s*=\s*"([0-9.]+)"[^>]*?data-start\s*=\s*"([0-9.]+)"[^>]*?data-track-index\s*=\s*"([0-9-]+)"` + `|` +
		`data-duration\s*=\s*"([0-9.]+)"[^>]*?data-track-index\s*=\s*"([0-9-]+)"[^>]*?data-start\s*=\s*"([0-9.]+)"` + `|` +
		`data-track-index\s*=\s*"([0-9-]+)"[^>]*?data-start\s*=\s*"([0-9.]+)"[^>]*?data-duration\s*=\s*"([0-9.]+)"` + `|` +
		`data-track-index\s*=\s*"([0-9-]+)"[^>]*?data-duration\s*=\s*"([0-9.]+)"[^>]*?data-start\s*=\s*"([0-9.]+)"` +
		`)`)

// composeTrackCollision detects pairs of class="clip" elements that
// share the same integer data-track-index AND have overlapping
// [data-start, data-start+data-duration) intervals. Returns (true,
// trackIdx, aStart, aEnd, bStart, bEnd) for the first overlapping pair
// found, or (false, 0, 0, 0, 0, 0) if no collisions exist.
//
// Upstream HyperFrames hard rule (per research surfaced 2026-06-14):
// the layout auditor rejects compositions where clips on the same
// integer track overlap temporally. Track-index is non-linear-editor
// row semantics, NOT spatial Z-order. Visual stacking is CSS z-index
// independent of track-index.
//
// Clips that don't declare data-track-index are skipped (the upstream
// engine treats missing track-index as a layout warning, not a hard
// error, but we leave that to the upstream auditor).
func composeTrackCollision(body string) (bool, int, float64, float64, float64, float64) {
	type clip struct {
		track int
		start float64
		end   float64
	}
	matches := composeClipTrackRE.FindAllStringSubmatch(body, -1)
	clips := make([]clip, 0, len(matches))
	for _, m := range matches {
		// Six alternations × three groups each = 18 capture groups.
		// Match the first non-empty triple and assign by attribute name.
		var startStr, durStr, trackStr string
		switch {
		case m[1] != "" && m[2] != "" && m[3] != "":
			startStr, durStr, trackStr = m[1], m[2], m[3]
		case m[4] != "" && m[5] != "" && m[6] != "":
			startStr, trackStr, durStr = m[4], m[5], m[6]
		case m[7] != "" && m[8] != "" && m[9] != "":
			durStr, startStr, trackStr = m[7], m[8], m[9]
		case m[10] != "" && m[11] != "" && m[12] != "":
			durStr, trackStr, startStr = m[10], m[11], m[12]
		case m[13] != "" && m[14] != "" && m[15] != "":
			trackStr, startStr, durStr = m[13], m[14], m[15]
		case m[16] != "" && m[17] != "" && m[18] != "":
			trackStr, durStr, startStr = m[16], m[17], m[18]
		default:
			continue
		}
		start, err1 := strconv.ParseFloat(startStr, 64)
		dur, err2 := strconv.ParseFloat(durStr, 64)
		track, err3 := strconv.Atoi(trackStr)
		if err1 != nil || err2 != nil || err3 != nil || dur <= 0 {
			continue
		}
		clips = append(clips, clip{track: track, start: start, end: start + dur})
	}
	// Group by track index, then walk each track sorted by start to
	// detect overlaps. Sorting per-track is O(n log n); overall worst
	// case is O(n log n) when all clips share a single track.
	byTrack := map[int][]clip{}
	for _, c := range clips {
		byTrack[c.track] = append(byTrack[c.track], c)
	}
	for track, list := range byTrack {
		sort.Slice(list, func(i, j int) bool { return list[i].start < list[j].start })
		for i := 1; i < len(list); i++ {
			prev, cur := list[i-1], list[i]
			// Strict half-open intervals: [prev.start, prev.end) vs
			// [cur.start, cur.end). Touching (prev.end == cur.start)
			// is fine — the prev clip has ended by the time cur starts.
			if cur.start < prev.end {
				return true, track, prev.start, prev.end, cur.start, cur.end
			}
		}
	}
	return false, 0, 0, 0, 0, 0
}

// composeEngagementBand picks the duration-band-appropriate engagement
// shape. Boundaries chosen from distribution-target conventions:
//   - <60s         → short_form (TikTok / YouTube Shorts cap is 60s)
//   - 60–179s      → mid_form   (still social-form; Twitter / LinkedIn)
//   - ≥180s        → long_form  (YouTube proper; chapters become meaningful)
func composeEngagementBand(durationSeconds float64) string {
	switch {
	case durationSeconds < 60:
		return "short_form"
	case durationSeconds < 180:
		return "mid_form"
	default:
		return "long_form"
	}
}

// Engagement-metadata prompt templates. Each band yields a different JSON
// shape tuned to its distribution targets. Same prose conventions as
// podcast.generate / slides.narrate engagement prompts: ONE JSON object,
// no fences, hard rules enforced.

const composeEngagementShortPrompt = `You are a short-form video engagement-metadata writer for an animated explainer (≤60 seconds, TikTok / YouTube Shorts / Reels distribution). Produce ONE JSON object — no surrounding prose, no markdown fences — with exactly these fields:

{
  "format": "short_form",
  "title": "...",
  "hook": "...",
  "hashtags": ["...", "..."],
  "caption": "...",
  "thumbnail_prompt": "..."
}

HARD RULES (the output is rejected by automated tests if violated):

Title:
- 30-50 characters. Hook-first ("How X works", "The Y problem", "Why Z fails").
- Plain text. No emoji. No clickbait punctuation.

Hook:
- ONE punchy sentence the narrator says in the first 3-5 seconds. Pattern interrupt: surprising specific claim, NOT "today we're talking about".

Hashtags:
- 3-5 hashtags, each genuinely relevant. Format as plain strings WITHOUT the leading #.
- ZERO generic hashtags (#viral, #fyp, #foryou, #trending, #subscribe) — algorithmic signal is near-zero and they often get downranked.

Caption:
- ≤140 characters. The caption an operator can drop into Twitter/X / LinkedIn / Mastodon when sharing the artifact. Plain text.

Thumbnail prompt:
- 1-2 sentences describing the thumbnail / cover image. Concrete visual (subject + style + composition). The operator can feed this to image.generate or use as-is.`

const composeEngagementMidPrompt = `You are a mid-form video engagement-metadata writer for an animated explainer (60-180 seconds, social-native distribution: Twitter / LinkedIn / Reels longer / Shorts-edge). Produce ONE JSON object — no surrounding prose, no markdown fences — with exactly these fields:

{
  "format": "mid_form",
  "title": "...",
  "hook": "...",
  "hashtags": ["...", "..."],
  "caption": "...",
  "social_blurb": "...",
  "thumbnail_prompt": "..."
}

HARD RULES (the output is rejected by automated tests if violated):

Title:
- 40-60 characters. Hook-first. Front-load the primary keyword.
- Plain text. No emoji. No clickbait punctuation.

Hook:
- 1-2 sentences for the first 8-12 seconds of the narration. Pattern interrupt + payoff promise.

Hashtags:
- 5-8 hashtags, each genuinely relevant. Format as plain strings WITHOUT the leading #.
- ZERO generic hashtags (#viral, #fyp, #foryou, #trending, #subscribe).

Caption:
- ≤280 characters (Twitter cap). The share-blurb for Twitter / Mastodon. Plain text.

Social blurb:
- 300-500 characters. The longer-form intro for LinkedIn / blog embedding. Conversational, third-paragraph deep. Plain text.

Thumbnail prompt:
- 1-2 sentences describing the thumbnail / cover image. Concrete visual (subject + style + composition).`

const composeEngagementLongPrompt = `You are a long-form video engagement-metadata writer for a narrated animated video (3-12 minutes, YouTube-primary distribution). Produce ONE JSON object — no surrounding prose, no markdown fences — with exactly these fields:

{
  "format": "long_form",
  "title": "...",
  "description": "...",
  "chapters": [{"timestamp": "0:00", "title": "...", "seconds": 0}, ...],
  "hashtags": ["...", "..."],
  "tags": ["...", "..."],
  "hook_30s": "...",
  "category": "Science & Technology",
  "language": "en",
  "thumbnail_prompt": "..."
}

HARD RULES (the output is rejected by automated tests if violated):

Title:
- 45-55 characters target, NEVER exceed 60 (mobile truncates beyond).
- Front-load the primary keyword in the first 4-5 words.
- One power word max. Numeric specificity ("3 steps", "5 patterns") if natural.
- Plain text. No emoji. No clickbait punctuation.

Description:
- First 100 characters are the hook (this is what appears above-the-fold in search). Make them land.
- Total length under 1000 characters.
- Plain text. No keyword stuffing — algorithms read the transcript directly.

Chapters:
- The FIRST chapter MUST have timestamp "0:00" and seconds=0.
- Provide AT LEAST 3 chapters when the video is >7 minutes; fewer is acceptable below but still aim for ≥2.
- Minimum 10 seconds between consecutive chapter starts.
- Use timestamps from the supplied total duration; format as M:SS (e.g. "0:00", "1:32", "10:05").
- Chapter titles ≤45 characters, descriptive (not "Intro" / "Part 1" — use the actual topic).

Hashtags:
- 3-5 hashtags, each genuinely relevant. Format as plain strings WITHOUT the leading #.
- ZERO generic hashtags (#viral, #fyp, #foryou, #trending, #subscribe).

Tags:
- 10-15 backend keywords covering the main topics for discoverability.

Hook (hook_30s):
- A 2-4 sentence opening that follows the research-validated structure:
  (a) Pattern interrupt — a specific surprising claim or question. NOT "Welcome to my channel".
  (b) Payoff promise — what concrete thing the viewer gets.
  (c) Commitment hook — open a loop ("here's why" / "this changes when") that resolves later.
- This is a recommended VO script the creator can adopt; do not include in the description.

Thumbnail prompt:
- 1-2 sentences describing the thumbnail / cover image. Concrete visual (subject + style + composition).`

func composeEngagementPromptFor(band string) string {
	switch band {
	case "short_form":
		return composeEngagementShortPrompt
	case "mid_form":
		return composeEngagementMidPrompt
	default:
		return composeEngagementLongPrompt
	}
}

// generateComposeEngagement asks the gateway to produce a video engagement
// payload appropriate to the duration band. The dispatcher + model are
// the same shape podcast.generate's engagement helper uses. Errors are
// returned so the handler can decide to soft-degrade (log + skip) rather
// than fail the composition outright. The "max_tokens" budget is tight
// (1024) — the JSON shape is bounded.
func generateComposeEngagement(
	ctx context.Context,
	d vision.Dispatcher,
	model, band, description, audioURL string,
	durationSeconds float64,
) (map[string]any, error) {
	if d == nil {
		return nil, fmt.Errorf("engagement: dispatcher unavailable")
	}
	system := composeEngagementPromptFor(band)
	userParts := []string{
		fmt.Sprintf("Video duration: %.0f seconds (band: %s).", durationSeconds, band),
	}
	if strings.TrimSpace(audioURL) != "" {
		userParts = append(userParts, "The video has a narration audio track.")
	} else {
		userParts = append(userParts, "The video is silent (no narration audio).")
	}
	userParts = append(userParts, "Source description:")
	userParts = append(userParts, description)

	mt := 1024
	chat, err := d.Dispatch(ctx, gateway.ChatRequest{
		Model:     model,
		MaxTokens: &mt,
		Messages: []gateway.Message{
			{Role: "system", Content: gateway.TextContent(system)},
			{Role: "user", Content: gateway.TextContent(strings.Join(userParts, "\n"))},
		},
	})
	if err != nil {
		return nil, dispatchError("hyperframes.compose engagement", err)
	}
	if len(chat.Choices) == 0 {
		return nil, fmt.Errorf("engagement: gateway returned no choices")
	}
	raw := strings.TrimSpace(chat.Choices[0].Message.Content.Text())
	raw = unwrapCodeFence(raw)
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		snippet := raw
		if len(snippet) > 256 {
			snippet = snippet[:256] + "…"
		}
		return nil, fmt.Errorf("engagement: model did not return parseable JSON: %s", snippet)
	}
	// Stamp the band as a defense against the model emitting a different
	// shape than the prompt asked for — operators inspecting the artifact
	// know which schema to apply.
	out["format"] = band
	return out, nil
}
