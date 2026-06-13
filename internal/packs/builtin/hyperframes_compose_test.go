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

// TestCompose_BodyFirstAndTruncatedStyles — the prompt now emits BODY/TIMELINE
// before STYLES, and the parser is order-agnostic. If a chatty model truncates
// inside the (now-last) STYLES section, the required BODY/TIMELINE still survive
// and the composition assembles — the opposite of the real failure, where a
// verbose leading STYLES section truncated before BODY ever appeared.
func TestCompose_BodyFirstAndTruncatedStyles(t *testing.T) {
	reply := "===BODY===\n" +
		`<div id="t" class="clip" data-start="0" data-duration="5" data-track-index="1">Hi</div>` + "\n" +
		"===TIMELINE===\n" +
		"tl.from('#t',{opacity:0,duration:1},0);\n" +
		"===STYLES===\n" // truncated: marker present, content cut off
	disp := &scriptedDispatcherWT{replies: []string{reply}}
	raw, err := runCompose(t, disp, `{"description":"x","model":"openrouter/auto"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	out := decodeCompose(t, raw)
	if !strings.Contains(out.CompositionHTML, `class="clip"`) || !strings.Contains(out.CompositionHTML, "tl.from('#t'") {
		t.Errorf("BODY/TIMELINE before a truncated STYLES must survive: %q", out.CompositionHTML)
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

// TestCompose_AudioURLRequiresDuration — audio_url with duration_seconds omitted
// (or <=0) is a load-bearing foot-gun: the timeline would default to 8s and
// silently truncate longer narration tracks. The pack must reject this at the
// input boundary with a CodeInvalidInput error so callers see the constraint
// immediately. Issue #498.
func TestCompose_AudioURLRequiresDuration(t *testing.T) {
	for _, tc := range []struct{ name, input string }{
		{"audio_url + no duration", `{"description":"x","model":"openrouter/auto","audio_url":"https://store/a.mp3"}`},
		{"audio_url + duration=0", `{"description":"x","model":"openrouter/auto","audio_url":"https://store/a.mp3","duration_seconds":0}`},
		{"audio_url + duration<0", `{"description":"x","model":"openrouter/auto","audio_url":"https://store/a.mp3","duration_seconds":-3}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runCompose(t, &scriptedDispatcherWT{}, tc.input)
			pe := &packs.PackError{}
			if !errors.As(err, &pe) || pe.Code != packs.CodeInvalidInput {
				t.Fatalf("want CodeInvalidInput; got %v", err)
			}
			if !strings.Contains(pe.Message, "duration_seconds is required when audio_url is provided") {
				t.Errorf("error message should reference the duration_seconds + audio_url contract; got %q", pe.Message)
			}
		})
	}
}

// TestCompose_NoAudioStillDefaults — when audio_url is empty, the silent
// micro-animation default (8s) is still appropriate. Backwards-compatible for
// the silent-clip case.
func TestCompose_NoAudioStillDefaults(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{goodSpec}}
	raw, err := runCompose(t, disp, `{"description":"x","model":"openrouter/auto"}`)
	if err != nil {
		t.Fatalf("silent clip without duration should still work: %v", err)
	}
	out := decodeCompose(t, raw)
	if out.DurationSeconds != 8 {
		t.Errorf("silent-clip default duration = %v, want 8", out.DurationSeconds)
	}
}

// --- Engagement metadata (duration-band-aware) ---------------------------

// Minimal valid engagement payloads per band — exact-field detail is enforced
// by prompt rules; for tests we just need the JSON to parse.
const (
	engagementShortJSON = `{"title":"How eBPF tracepoint catches rootkits","hook":"Most rootkits hide from /proc — eBPF tracepoints don't care.","hashtags":["ebpf","linux","kernel","security"],"caption":"How tracepoint observability catches kernel rootkits without writing a kernel module.","thumbnail_prompt":"A stylized kernel diagram with a green checkmark over a tracepoint hook."}`
	engagementMidJSON   = `{"title":"eBPF tracepoint detection in 90 seconds","hook":"Rootkits hide. But the kernel can't unsee a tracepoint.","hashtags":["ebpf","linux","kernel","security","observability"],"caption":"How eBPF tracepoint observability catches kernel rootkits — explained in 90 seconds.","social_blurb":"You don't need to write a kernel module to spot a rootkit. This short shows the trace flow from syscall entry to userspace alert using only eBPF tracepoints — the same primitives bcc and bpftrace already give you, applied to a real detection pipeline.","thumbnail_prompt":"A kernel-diagram with the syscall path highlighted in cyan and a tracepoint glyph."}`
	engagementLongJSON  = `{"title":"eBPF tracepoint observability for kernel rootkit detection","description":"Most rootkits hide their state from procfs and the conventional ps/lsof toolchain. They don't hide from kernel tracepoints. This explainer walks through the trace flow from syscall entry to userspace alert using only eBPF — no kernel modules required.","chapters":[{"timestamp":"0:00","title":"The detection problem","seconds":0},{"timestamp":"1:30","title":"Tracepoints vs. kprobes","seconds":90},{"timestamp":"3:15","title":"From event to alert","seconds":195}],"hashtags":["ebpf","linux","kernel","security"],"tags":["ebpf tracepoint","kernel observability","rootkit detection","syscall tracing","linux security","kernel security","bpf programs","kernel modules","bcc","bpftrace"],"hook_30s":"Most rootkits hide from procfs. They cannot hide from a kernel tracepoint. In the next ten minutes I show you the exact trace flow from a syscall entry to a userspace alert — no kernel modules, no boot-time kludges, just tracepoints and a BPF program. Here's why every defender should know this pipeline by 2026.","category":"Science & Technology","language":"en","thumbnail_prompt":"A clean kernel-architecture diagram with the syscall→tracepoint→BPF→userspace path highlighted in cyan against a black background."}`
)

// TestCompose_EngagementShortBand — duration <60s picks the short_form prompt
// and emits the corresponding engagement object on the output.
func TestCompose_EngagementShortBand(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{goodSpec, engagementShortJSON}}
	raw, err := runCompose(t, disp,
		`{"description":"x","model":"openrouter/auto","metadata_model":"openrouter/test","duration_seconds":30}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out map[string]any
	if uerr := json.Unmarshal(raw, &out); uerr != nil {
		t.Fatalf("decode: %v", uerr)
	}
	eng, ok := out["engagement"].(map[string]any)
	if !ok {
		t.Fatalf("engagement field missing or wrong type: %T", out["engagement"])
	}
	if eng["format"] != "short_form" {
		t.Errorf("format = %v, want short_form", eng["format"])
	}
	// Engagement-call prompt should be the short-form template.
	if len(disp.captured) < 2 {
		t.Fatalf("expected 2 dispatch calls (composition + engagement), got %d", len(disp.captured))
	}
	sys := disp.captured[1].Messages[0].Content.Text()
	if !strings.Contains(sys, "short-form video engagement-metadata writer") {
		t.Errorf("second dispatch system prompt is not the short-form template: %q", sys)
	}
}

// TestCompose_EngagementMidBand — duration 60–179s picks the mid_form prompt.
func TestCompose_EngagementMidBand(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{goodSpec, engagementMidJSON}}
	raw, err := runCompose(t, disp,
		`{"description":"x","model":"openrouter/auto","metadata_model":"openrouter/test","audio_url":"https://store/a.mp3","duration_seconds":120}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	eng, ok := out["engagement"].(map[string]any)
	if !ok || eng["format"] != "mid_form" {
		t.Fatalf("expected mid_form engagement, got %v", out["engagement"])
	}
	if _, hasSocial := eng["social_blurb"]; !hasSocial {
		t.Errorf("mid_form engagement should include social_blurb; got %v", eng)
	}
	sys := disp.captured[1].Messages[0].Content.Text()
	if !strings.Contains(sys, "mid-form video engagement-metadata writer") {
		t.Errorf("second dispatch system prompt is not the mid-form template: %q", sys)
	}
}

// TestCompose_EngagementLongBand — duration ≥180s picks the long_form prompt
// and the engagement object contains chapters + description.
func TestCompose_EngagementLongBand(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{goodSpec, engagementLongJSON}}
	raw, err := runCompose(t, disp,
		`{"description":"x","model":"openrouter/auto","metadata_model":"openrouter/test","audio_url":"https://store/a.mp3","duration_seconds":300}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	eng, ok := out["engagement"].(map[string]any)
	if !ok || eng["format"] != "long_form" {
		t.Fatalf("expected long_form engagement, got %v", out["engagement"])
	}
	if _, hasChapters := eng["chapters"]; !hasChapters {
		t.Errorf("long_form engagement should include chapters")
	}
	if _, hasDesc := eng["description"]; !hasDesc {
		t.Errorf("long_form engagement should include description")
	}
	sys := disp.captured[1].Messages[0].Content.Text()
	if !strings.Contains(sys, "long-form video engagement-metadata writer") {
		t.Errorf("second dispatch system prompt is not the long-form template: %q", sys)
	}
}

// TestCompose_EngagementOptOut — metadata_model:"" disables engagement gen
// entirely. The pack runs ONE dispatch (composition) and the engagement
// fields are absent on the output.
func TestCompose_EngagementOptOut(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{goodSpec}}
	raw, err := runCompose(t, disp,
		`{"description":"x","model":"openrouter/auto","metadata_model":""}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if disp.calls != 1 {
		t.Errorf("opt-out should make exactly 1 dispatch call, got %d", disp.calls)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, has := out["engagement"]; has {
		t.Errorf("opt-out should produce no engagement field, got %v", out["engagement"])
	}
	if _, has := out["engagement_artifact_key"]; has {
		t.Errorf("opt-out should produce no engagement_artifact_key, got %v", out["engagement_artifact_key"])
	}
}

// TestCompose_EngagementFailureGracefulDegrade — if engagement generation
// fails (unparseable JSON), the composition still succeeds and the
// engagement field is just absent. composition_html is the load-bearing
// output; engagement is best-effort.
func TestCompose_EngagementFailureGracefulDegrade(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{goodSpec, "this is not JSON"}}
	raw, err := runCompose(t, disp,
		`{"description":"x","model":"openrouter/auto","metadata_model":"openrouter/test","duration_seconds":30}`)
	if err != nil {
		t.Fatalf("composition should not fail when engagement parsing fails: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, hasEng := out["engagement"]; hasEng {
		t.Errorf("engagement field should be absent on parse failure, got %v", out["engagement"])
	}
	if _, hasHTML := out["composition_html"]; !hasHTML {
		t.Errorf("composition_html should still be produced on engagement failure")
	}
}

// TestCompose_EngagementBandSelector — direct unit test for the band
// boundaries so refactors that change the constants surface immediately.
func TestCompose_EngagementBandSelector(t *testing.T) {
	cases := []struct {
		duration float64
		want     string
	}{
		{0, "short_form"},
		{8, "short_form"},
		{59.9, "short_form"},
		{60, "mid_form"},
		{120, "mid_form"},
		{179.9, "mid_form"},
		{180, "long_form"},
		{600, "long_form"},
		{720, "long_form"},
	}
	for _, tc := range cases {
		if got := composeEngagementBand(tc.duration); got != tc.want {
			t.Errorf("composeEngagementBand(%v) = %q, want %q", tc.duration, got, tc.want)
		}
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
