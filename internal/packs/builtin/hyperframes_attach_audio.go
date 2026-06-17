// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// hyperframes_attach_audio.go (#521) — narration-audio splice for the
// scaffold-based video chain. Closes the silent-video failure mode
// captured in issue #521: upstream's `hyperframes init --audio=<path>`
// is silently ignored by at least the `decision-tree` example (and
// possibly others), so the rendered MP4 has no audio stream regardless
// of whether podcast.generate successfully produced one and the
// pipeline threaded the URL through.
//
// This pack is the audio twin of hyperframes.attach_asset: same
// content-addressed assets/ pattern, same project-tarball transform,
// same handler shape. The only difference is the splice target — the
// root composition div instead of a named child slot.
//
// Composition with podcast.generate:
//
//   podcast.generate ─→ audio_artifact_key + duration_s
//                              ↓
//   scaffold ─→ interpolate ─→ attach_audio ─→ render → narrated MP4
//
// Architectural notes mirror attach_asset:
//   - Pure-Go in-process tarball manipulation
//   - No SessionSpec, no ec.Exec, no dispatcher — just ec.Artifacts
//   - URL fetch intentionally not supported in v1 (audio always comes
//     from the artifact store via podcast.generate's
//     audio_artifact_key output)
//
// Why a separate pack instead of folding this into hyperframes.render:
// render's job is HTML → MP4 capture, the audio attach is a project-
// tarball transform that belongs with the other transforms (scaffold,
// interpolate, attach_asset). Symmetry across the four-pack family
// keeps the pipeline composable.

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/tosin2013/helmdeck/internal/packs"
)

const (
	// 50 MiB cap matches attach_asset's. A 12-minute podcast.generate
	// MP3 at 128 kbps is ~11 MiB; at 192 kbps (default ElevenLabs
	// Creator-tier) it's ~17 MiB. 50 MiB leaves comfortable headroom
	// for higher-quality formats while preventing runaway projects.
	hyperframesAttachAudioMaxAssetSize = 50 << 20

	// Default audio splice attributes when the input doesn't override.
	// data-track-index="9" is upstream's documented convention for
	// audio tracks (per hyperframes_compose.go's system prompt
	// quoting upstream AGENTS.md).
	hyperframesAttachAudioDefaultVolume     = 1.0
	hyperframesAttachAudioDefaultTrackIndex = 9
)

// hyperframesAttachAudioContentTypeMap maps audio MIME types to their
// file extensions. ElevenLabs serves mp3_44100_192 by default (the
// elevenLabsDefaultFormat in slides_narrate.go), so audio/mpeg covers
// the common case. The others handle alternate gateways (mp4 from
// AAC-LC, wav from raw PCM) without rejecting bytes a downstream might
// reasonably hand us.
var hyperframesAttachAudioContentTypeMap = map[string]string{
	"audio/mpeg": ".mp3",
	"audio/mp3":  ".mp3",
	"audio/mp4":  ".m4a",
	"audio/aac":  ".aac",
	"audio/wav":  ".wav",
	"audio/x-wav": ".wav",
}

type hyperframesAttachAudioInput struct {
	ProjectArtifactKey string  `json:"project_artifact_key"`
	AudioArtifactKey   string  `json:"audio_artifact_key"`
	DurationSeconds    float64 `json:"duration_seconds"`
	// Volume defaults to 1.0 when omitted or zero. Upstream's audio
	// pipeline multiplexes post-capture, so this is set once at
	// scaffold time and cannot be GSAP-tweened later (per the
	// AUDIO VOLUME IS IMMUTABLE rule from hyperframes_compose.go).
	Volume float64 `json:"volume,omitempty"`
	// TrackIndex defaults to 9 when omitted or zero. Per upstream's
	// non-linear-editor convention, tracks 0-1 are visual layers
	// (background / primary), 2-8 are reserved, 9+ are audio. Set
	// explicitly to override.
	TrackIndex int `json:"track_index,omitempty"`
	// UpdateRootDuration rewrites the root composition div's
	// data-duration attribute to DurationSeconds. Default true —
	// without it, the rendered MP4 stays at the example's intrinsic
	// duration (e.g. 15s for decision-tree) and any audio beyond
	// that is silently cut. Set false ONLY when the caller has
	// already set the root duration via hyperframes.interpolate or
	// has a deliberate reason to leave it alone.
	UpdateRootDuration *bool `json:"update_root_duration,omitempty"`
}

// HyperframesAttachAudio constructs the pack. Same wiring shape as
// HyperframesAttachAsset: no dispatcher, just the artifact store.
func HyperframesAttachAudio() *packs.Pack {
	return &packs.Pack{
		Name:    "hyperframes.attach_audio",
		Version: "v1",
		Description: "Splice a narration audio track into a hyperframes scaffold project. Takes a `project_artifact_key` (from `hyperframes.scaffold` / `interpolate` / `attach_asset`) plus an `audio_artifact_key` (typically from `podcast.generate`'s output) plus the audio's `duration_seconds`. Embeds the audio bytes under `assets/aroll-audio-<hash>.<ext>` and injects an `<audio>` element as a child of the root composition div with `data-start=0`, `data-duration=<duration_seconds>`, `data-volume=1`, `data-track-index=9`. By default also rewrites the root div's `data-duration` to match the audio length so the rendered video plays the full narration (instead of truncating to the example's intrinsic 15s). Closes the silent-video failure mode where `hyperframes init --audio=<path>` is silently ignored by some upstream examples (issue #521). Used as the fourth optional step in the scaffold-video chain: scaffold → interpolate → attach_audio → render. URL fetch is not supported in v1 — chain `podcast.generate` upstream and pass its `audio_artifact_key`.",
		Metadata: packs.PackMetadata{
			Accepts:        []string{"project_artifact_key", "audio_artifact_key", "duration_seconds"},
			Produces:       []string{"project_artifact_key"},
			IntentKeywords: []string{"attach audio", "attach narration", "splice audio track", "add narration to video", "embed audio in scaffold"},
			TypicalUse:     "Fourth (deterministic) step in the scaffold-based narrated-video chain. Chain `hyperframes.scaffold` + `hyperframes.interpolate` (and optionally `attach_asset` for an A-roll image) first; pre-generate narration with `podcast.generate`; pass `audio_artifact_key` + `duration_seconds` here; then chain `hyperframes.render`. Pipeline `builtin.scaffolded-narrated-video` automates this. Use instead of relying on `hyperframes.scaffold`'s `audio_url` pass-through to upstream `--audio` (which is silently ignored by some examples — see issue #521).",
			Limitations: []string{
				"audio must be in the artifact store; URL fetching not supported in v1 (chain podcast.generate upstream)",
				"audio cap 50 MiB",
				"root composition div must have `data-composition-id=\"main\"` (the canonical hyperframes scaffold convention) for the splice to find it",
				"supported content types: audio/{mpeg,mp3,mp4,aac,wav,x-wav}",
				"duration_seconds is required and authoritative — caller is responsible for measuring the audio (e.g. via podcast.generate's `duration_s` output); attach_audio does NOT probe the audio bytes",
			},
		},
		InputSchema: packs.BasicSchema{
			Required: []string{"project_artifact_key", "audio_artifact_key", "duration_seconds"},
			Properties: map[string]string{
				"project_artifact_key":  "string",
				"audio_artifact_key":    "string",
				"duration_seconds":      "number",
				"volume":                "number",
				"track_index":           "number",
				"update_root_duration":  "boolean",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"project_artifact_key", "audio_filename", "duration_seconds_used"},
			Properties: map[string]string{
				"project_artifact_key":          "string",
				"original_project_artifact_key": "string",
				"audio_filename":                "string",
				"audio_size":                    "number",
				"duration_seconds_used":         "number",
				"root_duration_updated":         "boolean",
				"track_index_used":              "number",
				"volume_used":                   "number",
			},
		},
		Handler: hyperframesAttachAudioHandler,
	}
}

func hyperframesAttachAudioHandler(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
	var in hyperframesAttachAudioInput
	if err := json.Unmarshal(ec.Input, &in); err != nil {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
	}
	if strings.TrimSpace(in.ProjectArtifactKey) == "" {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: "project_artifact_key is required (e.g. from hyperframes.scaffold or hyperframes.interpolate)"}
	}
	if strings.TrimSpace(in.AudioArtifactKey) == "" {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: "audio_artifact_key is required (chain podcast.generate first; URL fetch is a planned follow-up)"}
	}
	if in.DurationSeconds <= 0 {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: "duration_seconds is required and must be > 0 (use podcast.generate's `duration_s` output)"}
	}
	if ec.Artifacts == nil {
		return nil, &packs.PackError{Code: packs.CodeInternal,
			Message: "hyperframes.attach_audio requires an artifact store, but none is wired into the ExecutionContext"}
	}

	// Resolve defaults for optional inputs.
	volume := in.Volume
	if volume <= 0 {
		volume = hyperframesAttachAudioDefaultVolume
	}
	trackIndex := in.TrackIndex
	if trackIndex <= 0 {
		trackIndex = hyperframesAttachAudioDefaultTrackIndex
	}
	// UpdateRootDuration defaults TRUE (rewrite the root div's
	// data-duration to match audio). Operators who set it false take
	// the audio-truncated-to-example-duration outcome; the only
	// reason to do that is when interpolate already set it.
	updateRoot := true
	if in.UpdateRootDuration != nil {
		updateRoot = *in.UpdateRootDuration
	}

	// 1. Download the audio bytes + classify.
	ec.Report(10, "downloading audio")
	audioBytes, audioArt, err := ec.Artifacts.Get(ctx, in.AudioArtifactKey)
	if err != nil {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: fmt.Sprintf("audio_artifact_key %q not found in artifact store: %v",
				in.AudioArtifactKey, err), Cause: err}
	}
	if len(audioBytes) == 0 {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: fmt.Sprintf("audio_artifact_key %q resolved to empty bytes",
				in.AudioArtifactKey)}
	}
	if len(audioBytes) > hyperframesAttachAudioMaxAssetSize {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: fmt.Sprintf("audio exceeds %d MiB cap (got %d bytes); shorten/recompress before attaching",
				hyperframesAttachAudioMaxAssetSize>>20, len(audioBytes))}
	}
	contentType := strings.ToLower(strings.TrimSpace(audioArt.ContentType))
	ext, ok := hyperframesAttachAudioContentTypeMap[contentType]
	if !ok {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: fmt.Sprintf("unsupported audio content_type %q; supported: audio/{mpeg,mp3,mp4,aac,wav,x-wav}",
				audioArt.ContentType)}
	}

	// Content-addressed filename — same bytes → same name → dedup
	// if the operator chains the same narration twice.
	h := sha256.Sum256(audioBytes)
	audioFilename := "aroll-audio-" + hex.EncodeToString(h[:6]) + ext
	audioPath := "assets/" + audioFilename

	// 2. Download the project tarball.
	ec.Report(30, "downloading project tarball")
	projectBytes, _, err := ec.Artifacts.Get(ctx, in.ProjectArtifactKey)
	if err != nil {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: fmt.Sprintf("project_artifact_key %q not found in artifact store: %v",
				in.ProjectArtifactKey, err), Cause: err}
	}
	if len(projectBytes) == 0 {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: fmt.Sprintf("project_artifact_key %q resolved to empty bytes",
				in.ProjectArtifactKey)}
	}

	// 3. Extract, find index.html, splice in the <audio> element
	// (and optionally rewrite the root div's data-duration).
	ec.Report(50, "splicing audio into index.html")
	files, err := extractTarball(projectBytes)
	if err != nil {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: fmt.Sprintf("decompress/extract project tarball: %v (is the key really a hyperframes project tarball?)", err), Cause: err}
	}
	indexIdx := -1
	for i, f := range files {
		path := strings.TrimPrefix(f.Header.Name, "./")
		if path == "index.html" {
			indexIdx = i
			break
		}
	}
	if indexIdx < 0 {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: "project tarball is missing index.html at the root — not a valid hyperframes scaffold"}
	}
	indexContent := string(files[indexIdx].Data)
	audioElement := fmt.Sprintf(
		`<audio src="%s" data-start="0" data-duration="%g" data-volume="%g" data-track-index="%d"></audio>`,
		audioPath, in.DurationSeconds, volume, trackIndex)
	newIndex, spliced := spliceAudioIntoRoot(indexContent, audioElement)
	if !spliced {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: "root composition div not found in index.html — expected <div ...data-composition-id=\"main\"...>. Is this really a hyperframes scaffold?"}
	}
	rootDurationUpdated := false
	if updateRoot {
		newIndex, rootDurationUpdated = updateRootDataDuration(newIndex, in.DurationSeconds)
	}
	files[indexIdx].Data = []byte(newIndex)
	files[indexIdx].Header.Size = int64(len(newIndex))

	// 4. Append the audio file to the tarball.
	files = append(files, tarFile{
		Header: &tar.Header{
			Name:     audioPath,
			Mode:     0644,
			Size:     int64(len(audioBytes)),
			Typeflag: tar.TypeReg,
		},
		Data: audioBytes,
	})

	// 5. Repackage + upload.
	ec.Report(80, "repackaging project")
	newTarball, err := writeTarball(files)
	if err != nil {
		return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("repackage tarball: %v", err), Cause: err}
	}
	ec.Report(95, "uploading project")
	art, putErr := ec.Artifacts.Put(ctx, "hyperframes.attach_audio", "with-audio.tar.gz", newTarball, "application/gzip")
	if putErr != nil {
		return nil, &packs.PackError{Code: packs.CodeArtifactFailed,
			Message: fmt.Sprintf("upload project tarball: %v", putErr), Cause: putErr}
	}

	out := map[string]any{
		"project_artifact_key":          art.Key,
		"original_project_artifact_key": in.ProjectArtifactKey,
		"audio_filename":                audioFilename,
		"audio_size":                    len(audioBytes),
		"duration_seconds_used":         in.DurationSeconds,
		"root_duration_updated":         rootDurationUpdated,
		"track_index_used":              trackIndex,
		"volume_used":                   volume,
	}
	return json.Marshal(out)
}

// hyperframesAttachAudioRootDivRE matches the root composition div by
// looking for `data-composition-id="main"` — the canonical hyperframes
// scaffold convention. The pattern captures (1) the full opening tag
// so we can splice the audio element right after it. Tolerates
// arbitrary attribute order and whitespace around `=`.
var hyperframesAttachAudioRootDivRE = regexp.MustCompile(
	`(<div\s[^>]*?data-composition-id\s*=\s*"main"[^>]*>)`,
)

// spliceAudioIntoRoot inserts the audioElement string as the first
// child of the root composition div (right after the opening tag).
// Returns (newHTML, true) on success, or (indexHTML, false) if the
// root div isn't found.
//
// Inserting AT THE START of children rather than at the end keeps the
// audio element predictably placed regardless of how complex the
// scaffold's body is. Upstream's audio mux happens post-capture so
// position has no rendering impact; the consistency is for our own
// downstream tooling (and for tests).
func spliceAudioIntoRoot(indexHTML, audioElement string) (string, bool) {
	match := hyperframesAttachAudioRootDivRE.FindStringSubmatchIndex(indexHTML)
	if match == nil {
		return indexHTML, false
	}
	// match[1] is the end of the full match (which is the closing >).
	insertAt := match[1]
	// Insert a newline before the audio element to keep upstream's
	// pretty-printed indent style intact when humans read the
	// modified tarball.
	return indexHTML[:insertAt] + "\n      " + audioElement + indexHTML[insertAt:], true
}

// hyperframesAttachAudioRootDataDurationRE matches the data-duration
// attribute on the root composition div. Captures (1) prefix up to and
// including `data-duration="`, (2) the existing value, (3) the closing
// quote — so the rewrite preserves surrounding attributes.
//
// Anchored to data-composition-id="main" via a lookahead so we only
// rewrite the ROOT div's duration, not any descendant clip's
// data-duration. (?s) so . matches newlines in case the attributes are
// split across lines.
var hyperframesAttachAudioRootDataDurationRE = regexp.MustCompile(
	`(?s)(<div\s[^>]*?data-duration\s*=\s*")([0-9.]+)(")([^>]*?data-composition-id\s*=\s*"main"[^>]*>)|(?s)(<div\s[^>]*?data-composition-id\s*=\s*"main"[^>]*?data-duration\s*=\s*")([0-9.]+)(")([^>]*>)`,
)

// updateRootDataDuration rewrites the root composition div's
// data-duration attribute AND any child composition div whose
// data-duration matches the root's ORIGINAL value (issue #521 follow-
// up surfaced in v0.29.2's empirical retest: extending root from 15s
// to 97.9s left the child `<div data-composition-id="decision-tree"
// data-composition-src="..." data-duration="15">` at 15s, and the
// renderer played 15s of animation followed by 83s of blank canvas).
//
// Conservative heuristic — only stretch children whose duration
// MATCHED the root's. Operator-deliberate divergences (e.g. a 5-second
// intro composition under a 30-second root) are preserved. Anchored
// on data-composition-id so class="clip" data-durations are
// untouched.
//
// Returns (newHTML, true) when at least one data-duration was
// rewritten, or (indexHTML, false) when the root div didn't have a
// data-duration attribute.
func updateRootDataDuration(indexHTML string, durationSeconds float64) (string, bool) {
	// Step 1: discover the root's current data-duration. We need
	// this value to decide which CHILD compositions to stretch
	// (those whose duration equals the root's original).
	rootMatch := hyperframesAttachAudioRootDataDurationRE.FindStringSubmatch(indexHTML)
	if rootMatch == nil {
		return indexHTML, false
	}
	var originalDur string
	switch {
	case rootMatch[2] != "":
		originalDur = rootMatch[2] // alt 1: data-duration before composition-id
	case rootMatch[6] != "":
		originalDur = rootMatch[6] // alt 2: data-duration after composition-id
	default:
		return indexHTML, false
	}

	// Step 2: build a regex that rewrites data-duration on ANY div
	// with a data-composition-id (the root OR a child) whose value
	// equals originalDur. The data-composition-id anchor is what
	// distinguishes "composition divs" from class="clip" descendants
	// (which never have data-composition-id and whose data-duration
	// is the clip's individual timing, not the composition span).
	durStr := strconv.FormatFloat(durationSeconds, 'g', -1, 64)
	quoted := regexp.QuoteMeta(originalDur)
	compositionRE := regexp.MustCompile(
		`(?s)(<div\s[^>]*?data-duration\s*=\s*")` + quoted + `("[^>]*?data-composition-id\s*=\s*"[^"]+"[^>]*>)` +
			`|` +
			`(?s)(<div\s[^>]*?data-composition-id\s*=\s*"[^"]+"[^>]*?data-duration\s*=\s*")` + quoted + `("[^>]*>)`,
	)
	updated := false
	newHTML := compositionRE.ReplaceAllStringFunc(indexHTML, func(match string) string {
		sub := compositionRE.FindStringSubmatch(match)
		updated = true
		// Alternation 1: groups 1-2 (data-duration BEFORE composition-id)
		// Alternation 2: groups 3-4 (data-duration AFTER composition-id)
		if sub[1] != "" {
			return sub[1] + durStr + sub[2]
		}
		return sub[3] + durStr + sub[4]
	})
	return newHTML, updated
}
