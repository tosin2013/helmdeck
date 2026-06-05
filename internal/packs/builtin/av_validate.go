// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// av_validate.go — Phase 2 of the av-validation arc (Phase 1 shipped
// the standalone scripts/av-validate.sh in PR #428). This pack wraps
// the script as a helmdeck capability so any pipeline / agent can
// validate an MP4 or MP3 artifact and read structured findings rather
// than re-deriving the diagnostic flow from scratch every time.
//
// The pack is intentionally NOT default-wired into slides.narrate or
// podcast.generate yet — that's Phase 3. Phase 2's only job is to
// expose the validation surface so operators + the avbench workflow
// can start using it.
//
// Token-savings rationale: every manual "the video has issues"
// diagnostic burns ~3,000 tokens of bash output + analysis. Once
// Phase 3 wires this in as a post-step, the agent reads the
// `validation` field in ~200 tokens. The script is the executable
// spec; this pack is the surface area.
//
// Severity policy:
//   - The script ships every check at its natural severity (`fail`
//     for matches-shipped-bug-fixes, `warn` for soft heuristics).
//   - This pack OVERRIDES the script's severity for checks listed in
//     the `knownIssueDemotions` map below. Each entry references the
//     tracking issue that the fix is gated on. When the issue closes,
//     the same PR drops the entry — keeping severity coupled to the
//     fix landing.

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/session"
)

// avValidateScriptPath is where the sidecar Dockerfile installs the
// script. Stable path so the pack handler doesn't need to negotiate
// the script's location or upload it per-invocation.
const avValidateScriptPath = "/usr/local/bin/av-validate.sh"

// knownIssueDemotions maps a check name → the open issue tracking
// the underlying bug. When the script reports the check as `fail`,
// this pack demotes it to `warn` and appends the issue reference to
// the detail string. The demotion travels with the issue; close the
// issue (via the fix PR) → remove the entry → severity bumps back
// to fail. Same-PR coupling makes the regression guard impossible
// to silently leave behind.
//
// Document each entry inline so a future maintainer can audit drift
// without re-reading the original PRs.
//
// Currently empty: the #429 demotion was removed in the same PR that
// landed the apad-swap fix in encodeSegment (`internal/packs/builtin/
// slides_narrate.go`). Fresh slides.narrate outputs now produce
// content-accurate audio stream durations; consistency:audio_video_
// duration runs at its natural `fail` severity again. New entries
// added here should follow the same lifecycle: file the tracking
// issue first, add the entry with the issue reference, remove it in
// the same PR that ships the upstream fix.
var knownIssueDemotions = map[string]string{}

// AVValidate constructs the pack. No external dependencies — the
// script is in the sidecar image; the handler just invokes it via
// session exec. NeedsSession:true so the handler can read uploaded
// files from /tmp without re-fetching.
func AVValidate() *packs.Pack {
	return &packs.Pack{
		Name:    "av.validate",
		Version: "v1",
		Description: "Structured validation for slides.narrate / podcast.generate AV artifacts. " +
			"Runs the av-validate.sh check set (faststart, codec pin, packet contiguity, " +
			"RMS sweep, loudness LUFS, audio/video duration parity, SRT format compliance, " +
			"etc.) and returns a typed `validation` object. Default severity is honest — " +
			"`fail` for checks that match shipped bug fixes, `warn` for soft heuristics. " +
			"By default the pack returns success even when checks fail (the findings ARE " +
			"the output); pass `strict:true` to surface fail-severity failures as a typed " +
			"CodeArtifactFailed error for CI / publish-gate use cases.",
		NeedsSession: true,
		Async:        false,
		InputSchema: packs.BasicSchema{
			Properties: map[string]string{
				// Artifact-store inputs: when set, the handler fetches
				// the bytes via the engine's artifact store and writes
				// to /tmp in the session before invoking the script.
				"video_artifact_key":    "string",
				"audio_artifact_key":    "string",
				"captions_artifact_key": "string",
				// Path inputs: when set, the handler passes the path
				// to the script directly. Useful for chained-pack
				// scenarios where the file is already in the session
				// /tmp (Phase 3 will use this for the slides.narrate
				// integration — no double-fetch).
				"video_path":    "string",
				"audio_path":    "string",
				"captions_path": "string",
				// Tunables.
				"ebur128_target": "number",
				"skip_checks":    "string",
				// Strict mode: when true, any fail-severity check
				// failure surfaces as CodeArtifactFailed. Default
				// false (soft surface).
				"strict": "boolean",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"validation"},
			Properties: map[string]string{
				"validation":              "object",
				"validation_artifact_key": "string",
			},
		},
		Handler: avValidateHandler(),
	}
}

type avValidateInput struct {
	VideoArtifactKey    string  `json:"video_artifact_key"`
	AudioArtifactKey    string  `json:"audio_artifact_key"`
	CaptionsArtifactKey string  `json:"captions_artifact_key"`
	VideoPath           string  `json:"video_path"`
	AudioPath           string  `json:"audio_path"`
	CaptionsPath        string  `json:"captions_path"`
	EBUR128Target       float64 `json:"ebur128_target"`
	SkipChecks          string  `json:"skip_checks"`
	Strict              bool    `json:"strict"`
}

// scriptCheck mirrors the JSON shape av-validate.sh emits per check.
// Kept as a flat struct (no pointer fields) so json.Unmarshal accepts
// the script's output verbatim with no shape negotiation.
type scriptCheck struct {
	Name     string `json:"name"`
	Severity string `json:"severity"`
	Pass     bool   `json:"pass"`
	Detail   string `json:"detail"`
}

// scriptReport mirrors the top-level JSON the script emits with
// --json. The handler unmarshals into this, applies the known-issue
// demotions, then re-marshals into the pack's output shape.
type scriptReport struct {
	VideoPath    *string       `json:"video_path"`
	AudioPath    *string       `json:"audio_path"`
	CaptionsPath *string       `json:"captions_path"`
	Checks       []scriptCheck `json:"checks"`
	Passed       int           `json:"passed"`
	Failed       int           `json:"failed"`
	Warnings     int           `json:"warnings"`
	AllPassed    bool          `json:"all_passed"`
}

func avValidateHandler() packs.HandlerFunc {
	return func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
		var in avValidateInput
		if err := json.Unmarshal(ec.Input, &in); err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
		}

		// Validate input: must have at least one of video / audio,
		// either as an artifact key or as a direct path. Captions
		// alone aren't enough — the SRT checks need an audio stream
		// to compare against for coverage.
		videoPath, audioPath, captionsPath, err := resolveAVPaths(ctx, ec, in)
		if err != nil {
			return nil, err
		}
		if videoPath == "" && audioPath == "" {
			return nil, &packs.PackError{
				Code: packs.CodeInvalidInput,
				Message: "av.validate requires at least one of video_artifact_key, video_path, " +
					"audio_artifact_key, or audio_path",
			}
		}

		// Build the script argv. Path args are passed verbatim; the
		// script handles the --video / --audio / --captions flag
		// dispatch. --json is mandatory — this pack consumes JSON.
		args := []string{avValidateScriptPath, "--json"}
		if videoPath != "" {
			args = append(args, "--video", videoPath)
		}
		if audioPath != "" {
			args = append(args, "--audio", audioPath)
		}
		if captionsPath != "" {
			args = append(args, "--captions", captionsPath)
		}
		if in.EBUR128Target != 0 {
			args = append(args, "--ebur128-target", strconv.FormatFloat(in.EBUR128Target, 'f', -1, 64))
		}
		if strings.TrimSpace(in.SkipChecks) != "" {
			args = append(args, "--skip-checks", in.SkipChecks)
		}

		// Invoke. The script exits non-zero on any fail-severity
		// failure — we capture stdout regardless because the
		// structured JSON output is the truth, not the exit code.
		res, err := ec.Exec(ctx, session.ExecRequest{Cmd: args})
		if err != nil {
			return nil, &packs.PackError{
				Code:    packs.CodeHandlerFailed,
				Message: "av-validate.sh exec failed: " + err.Error(),
				Cause:   err,
			}
		}
		// Exit code 2 is "usage error or missing dependency" — that's
		// a real runtime failure, not a check finding. Surface as a
		// typed error so the operator knows the validation didn't
		// run, not "validation ran and reported issues."
		if res.ExitCode == 2 {
			return nil, &packs.PackError{
				Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("av-validate.sh exited 2 (usage/dep error): %s",
					truncForMessage(string(res.Stderr), 512)),
			}
		}

		var report scriptReport
		if err := json.Unmarshal(res.Stdout, &report); err != nil {
			return nil, &packs.PackError{
				Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("parse av-validate.sh JSON: %v (stdout=%q)",
					err, truncForMessage(string(res.Stdout), 512)),
			}
		}

		// Apply known-issue demotions. Walk the checks; if a check's
		// name is in knownIssueDemotions AND it failed, demote
		// severity to "warn" and append the issue ref to its detail
		// so consumers can navigate to the tracking ticket.
		demoted := 0
		for i := range report.Checks {
			c := &report.Checks[i]
			if c.Pass {
				continue
			}
			ref, known := knownIssueDemotions[c.Name]
			if !known || c.Severity != "fail" {
				continue
			}
			c.Severity = "warn"
			c.Detail = fmt.Sprintf("%s (known issue, tracked in %s)", c.Detail, ref)
			demoted++
		}
		// Recompute counters after demotion. Don't trust the script's
		// counters once we've mutated severities.
		report.Passed, report.Failed, report.Warnings = 0, 0, 0
		for _, c := range report.Checks {
			switch {
			case c.Pass:
				report.Passed++
			case c.Severity == "fail":
				report.Failed++
			default:
				report.Warnings++
			}
		}
		report.AllPassed = report.Failed == 0

		// Persist validation.json sidecar (mirrors engagement.json /
		// captions.srt pattern from #424 / #425). Operators reading
		// from the artifact store get the full structured report
		// without re-running validation.
		validationBytes, _ := json.MarshalIndent(report, "", "  ")
		var validationKey string
		if ec.Artifacts != nil {
			art, aerr := ec.Artifacts.Put(ctx, "av.validate", "validation.json",
				validationBytes, "application/json")
			if aerr != nil {
				ec.Logger.Warn("validation artifact upload failed", "err", aerr)
			} else {
				validationKey = art.Key
			}
		}

		// Strict mode: if any fail-severity check survived demotion
		// AND strict was requested, surface as a typed error. Detail
		// names the failing checks so the operator knows what broke.
		if in.Strict && report.Failed > 0 {
			var failed []string
			for _, c := range report.Checks {
				if !c.Pass && c.Severity == "fail" {
					failed = append(failed, c.Name)
				}
			}
			return nil, &packs.PackError{
				Code: packs.CodeArtifactFailed,
				Message: fmt.Sprintf("av.validate strict mode: %d fail-severity check(s) failed: %s",
					report.Failed, strings.Join(failed, ", ")),
			}
		}

		out := map[string]any{
			"validation":              report,
			"validation_artifact_key": validationKey,
		}
		return json.Marshal(out)
	}
}

// resolveAVPaths fetches artifact-key inputs from the engine's
// artifact store and writes them to /tmp in the session, returning
// the path the script should read. Direct-path inputs are returned
// verbatim. The mixed case (a key AND a path for the same modality)
// resolves to the path — direct paths win, mirroring the "operator
// override" pattern other packs use.
func resolveAVPaths(ctx context.Context, ec *packs.ExecutionContext, in avValidateInput) (video, audio, captions string, err error) {
	video, err = fetchOrPath(ctx, ec, in.VideoArtifactKey, in.VideoPath, "/tmp/av-validate-video.mp4")
	if err != nil {
		return "", "", "", err
	}
	audio, err = fetchOrPath(ctx, ec, in.AudioArtifactKey, in.AudioPath, "/tmp/av-validate-audio.mp3")
	if err != nil {
		return "", "", "", err
	}
	captions, err = fetchOrPath(ctx, ec, in.CaptionsArtifactKey, in.CaptionsPath, "/tmp/av-validate-captions.srt")
	if err != nil {
		return "", "", "", err
	}
	return
}

// fetchOrPath returns the direct path when supplied; otherwise
// fetches the artifact-key from the store and writes the bytes to
// dstPath in the session. Empty key + empty path returns empty
// string with no error — the caller is allowed to skip a modality.
func fetchOrPath(ctx context.Context, ec *packs.ExecutionContext, key, path, dstPath string) (string, error) {
	if strings.TrimSpace(path) != "" {
		return path, nil
	}
	if strings.TrimSpace(key) == "" {
		return "", nil
	}
	if ec.Artifacts == nil {
		return "", &packs.PackError{
			Code:    packs.CodeArtifactFailed,
			Message: fmt.Sprintf("av.validate: artifact_key %q given but no artifact store wired", key),
		}
	}
	bytes, _, err := ec.Artifacts.Get(ctx, key)
	if err != nil {
		return "", &packs.PackError{
			Code:    packs.CodeArtifactFailed,
			Message: fmt.Sprintf("av.validate: fetch %q: %v", key, err),
			Cause:   err,
		}
	}
	if _, werr := execWithStdin(ctx, ec, dstPath, bytes); werr != nil {
		return "", &packs.PackError{
			Code:    packs.CodeHandlerFailed,
			Message: fmt.Sprintf("av.validate: write %q to %s: %v", key, dstPath, werr),
			Cause:   werr,
		}
	}
	return dstPath, nil
}

// truncForMessage caps a string for use in error messages so the
// output schema's free-form "message" field doesn't drag a megabyte
// of stderr into the response envelope. 512-char default is generous
// enough to capture an ffmpeg error line without truncating
// mid-token.
func truncForMessage(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
