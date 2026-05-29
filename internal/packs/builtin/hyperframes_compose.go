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
	"strings"

	"github.com/tosin2013/helmdeck/internal/gateway"
	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/vision"
)

const (
	hyperframesComposeDefaultDuration = 8.0
	hyperframesComposeMaxDuration     = 720.0 // 12 min — matches hyperframes.render's cap
	hyperframesComposeDefaultTokens   = 4096
	hyperframesComposeMaxTokensFloor  = 2048
	hyperframesComposeMaxTokensCeil   = 8192
	// gsapCDN is the exact GSAP build the upstream CLI's `blank` template loads.
	gsapCDN = "https://cdn.jsdelivr.net/npm/gsap@3.14.2/dist/gsap.min.js"

	// hyperframesComposeSystemPrompt asks the model for ONLY the creative pieces
	// as three marker-delimited sections (raw CSS/HTML/JS, NOT JSON) — the pack
	// assembles the contract scaffolding around them. Marker sections avoid the
	// quote/newline escaping that made models emit invalid JSON when the body /
	// timeline contained HTML and JS.
	// %d=width %d=height %g=duration %s=audio-note %s=style-note %g=duration %d=width %d=height.
	hyperframesComposeSystemPrompt = `You design short HTML video compositions for the HyperFrames renderer (Chromium + GSAP). You will be given a DESCRIPTION of the video to make.

The canvas is EXACTLY %d×%d pixels and the video is %g seconds long. Animate with GSAP into a single paused timeline variable named ` + "`tl`" + ` (already declared for you — just add tweens to it).%s%s

Respond with EXACTLY these three sections and NOTHING else. Put each marker on its OWN line and write raw CSS / HTML / JS between them — NO JSON, NO escaping, NO markdown fences:

===STYLES===
(extra CSS rules for your elements; the canvas sizing + reset are already provided)
===BODY===
(the visible elements that go inside the root div)
===TIMELINE===
(GSAP statements that add tweens to the existing 'tl' timeline)

Hard rules (the render fails if you break them):
- Every visible, timed element MUST have class="clip" and the attributes data-start, data-duration, data-track-index (integers; data-start+data-duration must stay within 0..%g). Give each element a unique id you reference from the timeline.
- Position elements with absolute CSS inside the %d×%d canvas; use large, legible type (this is video, not a web page).
- DETERMINISTIC ONLY: no Date.now(), no Math.random(), no network/fetch, no external images or fonts. Use solid colors, CSS gradients, shapes, and text.
- Do NOT emit <html>, <head>, <body>, <style> tags, the root div, the GSAP <script>, or the window.__timelines line — those are added for you. Only the three marked sections above.`

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
		system := fmt.Sprintf(hyperframesComposeSystemPrompt,
			preset.Width, preset.Height, duration, audioNote, styleNote, duration, preset.Width, preset.Height)

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
	var s composeSpec
	if iStyles >= 0 && iStyles < iBody {
		s.Styles = composeSection(raw, iStyles+len(composeSecStyles), iBody)
	}
	bodyEnd := len(raw)
	if iTimeline > iBody {
		bodyEnd = iTimeline
	}
	s.Body = composeSection(raw, iBody+len(composeSecBody), bodyEnd)
	if iTimeline > iBody {
		s.Timeline = composeSection(raw, iTimeline+len(composeSecTimeline), len(raw))
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
