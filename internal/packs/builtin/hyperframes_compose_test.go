// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
)

func runCompose(t *testing.T, disp *scriptedDispatcherWT, input string) (json.RawMessage, error) {
	t.Helper()
	pack := HyperframesCompose(disp)
	ec := &packs.ExecutionContext{Pack: pack, Input: json.RawMessage(input)}
	return pack.Handler(context.Background(), ec)
}

type composeOut struct {
	CompositionHTML string  `json:"composition_html"`
	Model           string  `json:"model"`
	AspectRatio     string  `json:"aspect_ratio"`
	Width           int     `json:"width"`
	Height          int     `json:"height"`
	DurationSeconds float64 `json:"duration_seconds"`
	HasAudio        bool    `json:"has_audio"`
	DurationSource  string  `json:"duration_source"`
}

func decodeCompose(t *testing.T, raw json.RawMessage) composeOut {
	t.Helper()
	var o composeOut
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return o
}

// goodSpec is a well-formed creative payload the model is expected to return —
// marker-delimited raw CSS/HTML/JS (note the unescaped quotes that would break a
// JSON payload).
const goodSpec = "===STYLES===\n" +
	".t{color:#fff;font-size:80px;position:absolute;top:40px;left:40px}\n" +
	"===BODY===\n" +
	`<div id="t" class="clip" data-start="0" data-duration="5" data-track-index="1">Hello</div>` + "\n" +
	"===TIMELINE===\n" +
	"tl.from('#t',{opacity:0,duration:1},0);"

// TestCompose_AssemblesContractScaffolding — the pack wraps the model's creative
// pieces in the guaranteed HyperFrames contract: sized canvas, root data-*, and a
// paused window.__timelines registration. The model can't omit those.
func TestCompose_AssemblesContractScaffolding(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{goodSpec}}
	raw, err := runCompose(t, disp, `{"description":"a hello title card","model":"openrouter/auto","aspect_ratio":"16:9"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	out := decodeCompose(t, raw)
	html := out.CompositionHTML
	for _, must := range []string{
		"<!doctype html>",
		`data-composition-id="main"`,
		`data-width="1920"`,
		`data-height="1080"`,
		"width: 1920px; height: 1080px",
		`window.__timelines["main"] = tl`,
		"gsap.timeline({ paused: true })",
		`class="clip"`, // the model's body content survived
		"tl.from('#t'", // the model's timeline survived
	} {
		if !strings.Contains(html, must) {
			t.Errorf("composition missing required %q\n---\n%s", must, html)
		}
	}
	if out.Width != 1920 || out.Height != 1080 {
		t.Errorf("dims = %dx%d, want 1920x1080", out.Width, out.Height)
	}
	if out.HasAudio || out.DurationSource != "timeline" {
		t.Errorf("silent compose should have has_audio=false, duration_source=timeline; got %v/%q", out.HasAudio, out.DurationSource)
	}
	// System prompt must carry the exact canvas dimensions so the model targets them.
	if sys := disp.captured[0].Messages[0].Content.Text(); !strings.Contains(sys, "1920×1080") {
		t.Errorf("system prompt should state the 1920×1080 canvas, got: %q", sys)
	}
	if verr := HyperframesCompose(disp).OutputSchema.Validate(raw); verr != nil {
		t.Errorf("output violates declared OutputSchema: %v", verr)
	}
}

// TestCompose_VerticalDimensions — aspect_ratio drives the canvas size.
func TestCompose_VerticalDimensions(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{goodSpec}}
	raw, err := runCompose(t, disp, `{"description":"x","model":"openrouter/auto","aspect_ratio":"9:16"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	out := decodeCompose(t, raw)
	if out.Width != 1080 || out.Height != 1920 {
		t.Errorf("9:16 dims = %dx%d, want 1080x1920", out.Width, out.Height)
	}
	if !strings.Contains(out.CompositionHTML, `data-width="1080"`) || !strings.Contains(out.CompositionHTML, `data-height="1920"`) {
		t.Errorf("composition should be sized 1080x1920: %s", out.CompositionHTML)
	}
}

// TestCompose_AudioEmbedded — when audio_url is given, the pack adds the <audio>
// element and reports duration_source=audio.
func TestCompose_AudioEmbedded(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{goodSpec}}
	raw, err := runCompose(t, disp, `{"description":"x","model":"openrouter/auto","audio_url":"https://store/a.mp3","duration_seconds":12}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	out := decodeCompose(t, raw)
	if !out.HasAudio || out.DurationSource != "audio" {
		t.Errorf("want has_audio=true, duration_source=audio; got %v/%q", out.HasAudio, out.DurationSource)
	}
	if !strings.Contains(out.CompositionHTML, `<audio id="a-roll-audio" src="https://store/a.mp3"`) {
		t.Errorf("composition should embed the audio element: %s", out.CompositionHTML)
	}
	if out.DurationSeconds != 12 {
		t.Errorf("duration_seconds = %v, want 12", out.DurationSeconds)
	}
}

// TestCompose_EmptyAudioURLIsSilent — an empty audio_url (the narrated pipeline on
// a keyless store) must NOT embed an <audio> tag; it degrades to a silent video.
func TestCompose_EmptyAudioURLIsSilent(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{goodSpec}}
	raw, err := runCompose(t, disp, `{"description":"x","model":"openrouter/auto","audio_url":""}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	out := decodeCompose(t, raw)
	if out.HasAudio || strings.Contains(out.CompositionHTML, "<audio") {
		t.Errorf("empty audio_url should produce a silent composition, got has_audio=%v", out.HasAudio)
	}
}

// TestCompose_UnwrapsCodeFence — models often wrap the whole reply in a fence.
func TestCompose_UnwrapsCodeFence(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{"```\n" + goodSpec + "\n```"}}
	raw, err := runCompose(t, disp, `{"description":"x","model":"openrouter/auto"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !strings.Contains(decodeCompose(t, raw).CompositionHTML, `class="clip"`) {
		t.Errorf("fenced spec should be parsed and assembled")
	}
}

// TestCompose_NoMarkers — a reply with no ===BODY=== section is caller_fixable,
// not a crash.
func TestCompose_NoMarkers(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{"I cannot do that."}}
	_, err := runCompose(t, disp, `{"description":"x","model":"openrouter/auto"}`)
	pe := &packs.PackError{}
	if !errors.As(err, &pe) || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("want invalid_input when the section markers are absent, got %v", err)
	}
}

// TestCompose_EmptyBody — a BODY section with no visible elements is caller_fixable.
func TestCompose_EmptyBody(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{"===STYLES===\n\n===BODY===\n   \n===TIMELINE===\n"}}
	_, err := runCompose(t, disp, `{"description":"x","model":"openrouter/auto"}`)
	pe := &packs.PackError{}
	if !errors.As(err, &pe) || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("want invalid_input on empty body, got %v", err)
	}
}

func TestCompose_MissingFields(t *testing.T) {
	for _, tc := range []struct{ name, input string }{
		{"no description", `{"model":"openrouter/auto"}`},
		{"no model", `{"description":"x"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runCompose(t, &scriptedDispatcherWT{}, tc.input)
			pe := &packs.PackError{}
			if !errors.As(err, &pe) || pe.Code != packs.CodeInvalidInput {
				t.Errorf("want invalid_input, got %v", err)
			}
		})
	}
}

// TestCompose_BadAspectRatio — an unsupported aspect_ratio rejects (reusing the
// renderer's preset matrix) rather than producing a mismatched composition.
func TestCompose_BadAspectRatio(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{goodSpec}}
	_, err := runCompose(t, disp, `{"description":"x","model":"openrouter/auto","aspect_ratio":"21:9"}`)
	pe := &packs.PackError{}
	if !errors.As(err, &pe) || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("want invalid_input on unsupported aspect_ratio, got %v", err)
	}
}
