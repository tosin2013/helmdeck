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

	// hyperframesComposeSystemPromptTierC is the Tier C (free / weak open
	// model) system prompt. Constraint-heavy, compact, leads with the
	// hard rules verbatim because Tier C models reliably honor explicit
	// rules and unreliably honor "remember to" guidance. Same template
	// shape as the original prompt — the only real change vs. pre-#502
	// is the added "TIMELINE COVERAGE" requirement that closes the
	// blank-screen failure mode the 2026-06-13 session surfaced.
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

Hard rules (the render fails if you break them):
- Every visible, timed element MUST have class="clip" and the attributes data-start, data-duration, data-track-index (integers; data-start+data-duration must stay within 0..%g). Give each element a unique id you reference from the timeline.
- TIMELINE COVERAGE: the union of every class="clip" element's [data-start, data-start+data-duration) interval MUST cover the ENTIRE [0, %g) range without gaps. The first element starts at data-start="0". If you don't have a foreground element to fill a span, add a permanent background element (e.g. a solid-color full-canvas div with data-start="0" data-duration="%g") so the rendered video is never blank. The body's reset CSS sets background:#000; any gap shows up as a visible black run in the final MP4 (and av.validate flags it).
- Position elements with absolute CSS inside the %d×%d canvas; use large, legible type (this is video, not a web page).
- Keep STYLES brief — a handful of rules, plain colors and simple gradients; do NOT write long or elaborate CSS (it can truncate the response).
- DETERMINISTIC ONLY: no Date.now(), no Math.random(), no network/fetch, no external images or fonts. Use solid colors, simple CSS gradients, shapes, and text.
- Do NOT emit <html>, <head>, <body>, <style> tags, the root div, the GSAP <script>, or the window.__timelines line — those are added for you. Only the three marked sections above.`

	// hyperframesComposeSystemPromptTierAB is the Tier A/B (frontier /
	// mid-tier hosted model) system prompt. Leaner: the model is trusted
	// to honor concise instructions and to fetch the linked best-practices
	// guide when designing more sophisticated compositions. The hard
	// contract rules are the same as Tier C (any model that breaks them
	// makes the render fail caller_fixable regardless of capability).
	// %d=width %d=height %g=duration %s=audio-note %s=style-note %g=duration %d=width %d=height %s=best-practices-url.
	hyperframesComposeSystemPromptTierAB = `You design short HTML video compositions for the HyperFrames renderer (Chromium + GSAP). You will be given a DESCRIPTION of the video to make.

The canvas is %d×%d pixels and the video is %g seconds long. Animate with GSAP into a single paused timeline named ` + "`tl`" + ` (declared for you — just add tweens).%s%s

Respond with three marker-delimited sections in order: ===BODY===, ===TIMELINE===, ===STYLES=== (no JSON, no fences, no escaping). Finish BODY and TIMELINE before STYLES.

Hard contract (the render fails if you break it):
- Every timed element has class="clip" and integer data-start, data-duration, data-track-index attributes; data-start+data-duration must stay within 0..%g.
- Timeline coverage: the union of every clip's [data-start, data-start+data-duration) MUST cover [0, %g) without gaps. Use a permanent background element at data-start="0" data-duration="%g" if your foreground has gaps — otherwise the rendered MP4 has visible black runs.
- Absolute positioning inside the %d×%d canvas. No external resources (fonts, images, fetch, Date.now, Math.random).
- Do NOT emit <html>/<head>/<body>/<style>, the root div, the GSAP <script>, or the window.__timelines line — those are scaffolding the pack adds.

For richer guidance on visual hierarchy, pacing, type-on-screen rules, color choices, and the GSAP transition patterns that play well with HyperFrames, see the best-practices guide at %s.`

	composeSecStyles   = "===STYLES==="
	composeSecBody     = "===BODY==="
	composeSecTimeline = "===TIMELINE==="
)

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
			IntentKeywords: []string{"compose video", "design animated visual", "describe a video", "make explainer animation"},
			TypicalUse:     "Generator pack for video compositions. Chain hyperframes.render after for an MP4. Pair with podcast.generate's audio_url for a narrated video (the prompt-narrated-video pipeline).",
			Limitations:    []string{"does not render — outputs HTML/CSS/JS only; chain hyperframes.render", "visual design is LLM-driven; no fine-grained timeline control via inputs", "audio sync depends on duration_seconds being correct"},
		},
		InputSchema: packs.BasicSchema{
			Required: []string{"description", "model"},
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
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"composition_html", "model", "width", "height"},
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
			},
		},
		Handler: hyperframesComposeHandler(d),
		Async:   true,
	}
}

func hyperframesComposeHandler(d vision.Dispatcher) packs.HandlerFunc {
	return func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
		if d == nil {
			return nil, &packs.PackError{Code: packs.CodeInternal,
				Message: "hyperframes.compose registered without a gateway dispatcher"}
		}
		var in hyperframesComposeInput
		if err := json.Unmarshal(ec.Input, &in); err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
		}
		if strings.TrimSpace(in.Description) == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "description is required"}
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
		duration := in.DurationSeconds
		if duration <= 0 {
			duration = hyperframesComposeDefaultDuration
		}
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
