// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// hyperframes_interpolate.go (#503 Option C, PR 5) — second link in the
// scaffold-based video chain. Takes a project_artifact_key (produced by
// hyperframes.scaffold) plus a user description and rewrites the visible
// text content in compositions/*.html to fit the topic. Re-tars the
// modified project and uploads as a new project_artifact_key.
//
// Two content shapes are handled (auto-detected per-file):
//   - HTML text slots: <h1>, <h2>, <h3>, <div class="stat-value">,
//     <div class="stat-label">. Common in intro.html and graphics.html.
//     Each slot's inner text is extracted, the LLM rewrites it on-topic,
//     and the new text is spliced back.
//   - JS TRANSCRIPT array: `const TRANSCRIPT = [{text, start, end}, ...];`
//     Common in captions.html. The LLM regenerates the entire array with
//     new text fields timed against the audio duration; the old array is
//     replaced wholesale.
//
// Other compositions/*.html files (and any with neither pattern) are
// passed through unchanged — better to leave upstream content than to
// damage a file we don't understand. The output's files_rewritten
// manifest names what actually got modified.
//
// Architectural note: this pack runs entirely in-process (no
// SessionSpec, no ec.Exec). archive/tar + compress/gzip handle the
// tarball manipulation; ec.Artifacts.Get/Put move bytes through the
// store; the dispatcher does the LLM work.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/tosin2013/helmdeck/internal/gateway"
	"github.com/tosin2013/helmdeck/internal/llmcontext"
	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/vision"
)

const (
	hyperframesInterpolateDefaultDuration = 8.0
	hyperframesInterpolateMaxDuration     = 720.0
	hyperframesInterpolateDefaultTokens   = 4096
	hyperframesInterpolateMaxTokensFloor  = 1024
	hyperframesInterpolateMaxTokensCeil   = 8192
	// hyperframesInterpolateMaxFiles caps how many files we'll attempt
	// to rewrite per call. Protects against pathological scaffolds.
	hyperframesInterpolateMaxFiles = 32
)

type hyperframesInterpolateInput struct {
	ProjectArtifactKey string  `json:"project_artifact_key"`
	Description        string  `json:"description"`
	Model              string  `json:"model"`
	DurationSeconds    float64 `json:"duration_seconds"`
	AudioNote          string  `json:"audio_note"`
	MaxTokens          int     `json:"max_tokens"`
}

// HyperframesInterpolate constructs the pack. Dispatcher-gated like
// slides.outline / hyperframes.compose; no session needed because all
// manipulation is in-process Go (archive/tar + compress/gzip).
func HyperframesInterpolate(d vision.Dispatcher) *packs.Pack {
	return &packs.Pack{
		Name:        "hyperframes.interpolate",
		Version:     "v1",
		Description: "Rewrite the visible text content in a hyperframes scaffold so it fits the user's topic. Takes a `project_artifact_key` from `hyperframes.scaffold`, runs LLM passes over each `compositions/*.html` file (intro titles, graphics stats and labels, caption-transcript word array), and re-uploads the modified project as a new `project_artifact_key`. Auto-detects two content shapes per file: HTML text slots (`<h1>`, `<h2>`, `<div class=\"stat-value\">`, `<div class=\"stat-label\">`) get on-topic text substituted, and the JS `TRANSCRIPT` word array gets regenerated with timing aligned to `duration_seconds`. Other files are passed through unchanged.",
		Metadata: packs.PackMetadata{
			Accepts:        []string{"project_artifact_key", "description", "model"},
			Produces:       []string{"project_artifact_key", "files_rewritten"},
			IntentKeywords: []string{"rewrite scaffold content", "fill in template text", "customize hyperframes scaffold"},
			TypicalUse:     "Second step in a scaffolded video pipeline. Chain after hyperframes.scaffold (provides the project_artifact_key) and before hyperframes.attach_asset (A-roll image) + hyperframes.render (MP4). Pair with podcast.generate's `duration_s` output as `duration_seconds` to keep the caption transcript proportional to the narration.",
			Limitations: []string{
				"only handles two content shapes (HTML text slots + JS TRANSCRIPT array) — files using neither pattern are passed through unchanged",
				"caption-transcript timing is heuristic (150 wpm cadence); not whisper-aligned to actual audio",
				"does not rewrite assets (images, SVGs) — chain hyperframes.attach_asset for A-roll content",
			},
		},
		InputSchema: packs.BasicSchema{
			Required: []string{"project_artifact_key", "description", "model"},
			Properties: map[string]string{
				"project_artifact_key": "string",
				"description":          "string",
				"model":                "string",
				"duration_seconds":     "number",
				"audio_note":           "string",
				"max_tokens":           "number",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"project_artifact_key", "files_rewritten", "model_used"},
			Properties: map[string]string{
				"project_artifact_key":          "string",
				"original_project_artifact_key": "string",
				"files_rewritten":               "array",
				"files_skipped":                 "array",
				"model_used":                    "string",
			},
		},
		Handler: hyperframesInterpolateHandler(d),
		Async:   true,
	}
}

func hyperframesInterpolateHandler(d vision.Dispatcher) packs.HandlerFunc {
	return func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
		if d == nil {
			return nil, &packs.PackError{Code: packs.CodeInternal,
				Message: "hyperframes.interpolate registered without a gateway dispatcher"}
		}
		var in hyperframesInterpolateInput
		if err := json.Unmarshal(ec.Input, &in); err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
		}
		if strings.TrimSpace(in.ProjectArtifactKey) == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: "project_artifact_key is required (e.g. from hyperframes.scaffold's output)"}
		}
		if strings.TrimSpace(in.Description) == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: "description is required (the topic the LLM should rewrite slots to fit)"}
		}
		if strings.TrimSpace(in.Model) == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: "model is required (provider/model id; see helmdeck://models)"}
		}
		if ec.Artifacts == nil {
			return nil, &packs.PackError{Code: packs.CodeInternal,
				Message: "hyperframes.interpolate requires an artifact store but none is wired into the ExecutionContext"}
		}

		duration := in.DurationSeconds
		if duration <= 0 {
			duration = hyperframesInterpolateDefaultDuration
		}
		if duration > hyperframesInterpolateMaxDuration {
			duration = hyperframesInterpolateMaxDuration
		}

		maxTokens := in.MaxTokens
		if maxTokens <= 0 {
			maxTokens = hyperframesInterpolateDefaultTokens
		}
		if maxTokens < hyperframesInterpolateMaxTokensFloor {
			maxTokens = hyperframesInterpolateMaxTokensFloor
		}
		if maxTokens > hyperframesInterpolateMaxTokensCeil {
			maxTokens = hyperframesInterpolateMaxTokensCeil
		}

		// 1. Download the original project tarball.
		ec.Report(5, "downloading scaffold")
		tarballBytes, _, err := ec.Artifacts.Get(ctx, in.ProjectArtifactKey)
		if err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: fmt.Sprintf("project_artifact_key %q not found in artifact store: %v",
					in.ProjectArtifactKey, err), Cause: err}
		}
		if len(tarballBytes) == 0 {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: fmt.Sprintf("project_artifact_key %q resolved to an empty artifact",
					in.ProjectArtifactKey)}
		}

		// 2. Decompress + extract all files into memory.
		ec.Report(15, "extracting scaffold files")
		files, err := extractTarball(tarballBytes)
		if err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: fmt.Sprintf("decompress/extract project tarball: %v (is the key really a scaffold tarball?)", err), Cause: err}
		}
		if len(files) == 0 {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: "project tarball is empty — not a valid hyperframes scaffold"}
		}

		// 3. Per-file: classify, run LLM if applicable, replace bytes.
		rewritten := []map[string]any{}
		skipped := []string{}
		processed := 0
		for i := range files {
			f := &files[i]
			path := strings.TrimPrefix(f.Header.Name, "./")
			// Only attempt rewrites on compositions/*.html. Index,
			// assets, metadata are left to other packs (or to
			// upstream).
			if !strings.HasPrefix(path, "compositions/") || !strings.HasSuffix(path, ".html") {
				continue
			}
			if processed >= hyperframesInterpolateMaxFiles {
				skipped = append(skipped, path+" (file cap)")
				continue
			}
			processed++

			content := string(f.Data)
			kind := classifyCompositionFile(content)
			progress := 25.0 + float64(processed)/float64(hyperframesInterpolateMaxFiles)*60.0
			ec.Report(progress, fmt.Sprintf("rewriting %s (%s)", path, kind))

			var newContent string
			var rewriteErr error
			switch kind {
			case compositionKindTextSlots:
				newContent, rewriteErr = rewriteTextSlots(ctx, d, in.Model, maxTokens, content, in.Description, in.AudioNote)
			case compositionKindTranscript:
				newContent, rewriteErr = rewriteTranscript(ctx, d, in.Model, maxTokens, content, in.Description, duration, in.AudioNote)
			default:
				skipped = append(skipped, path+" ("+kind+")")
				continue
			}
			if rewriteErr != nil {
				// Soft-degrade: log + skip this file, keep going.
				// One file failing shouldn't abort the whole project.
				if ec.Logger != nil {
					ec.Logger.Warn("interpolate file failed", "path", path, "kind", kind, "err", rewriteErr)
				}
				skipped = append(skipped, path+" (rewrite error: "+truncStr(rewriteErr.Error(), 200)+")")
				continue
			}
			f.Data = []byte(newContent)
			f.Header.Size = int64(len(newContent))
			rewritten = append(rewritten, map[string]any{
				"path":          path,
				"kind":          kind,
				"original_size": len(content),
				"new_size":      len(newContent),
			})
		}

		if len(rewritten) == 0 {
			// Surface this as caller_fixable — the scaffold has nothing
			// our pattern matcher recognized. Likely an example whose
			// compositions/*.html files use unusual content shapes.
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: "no files in the scaffold matched a recognized content shape — none of the compositions/*.html files have HTML text slots (h1/h2/stat-value/stat-label) or a JS TRANSCRIPT array. Try a different upstream example."}
		}

		// 4. Re-tar the modified project.
		ec.Report(90, "repackaging project")
		newTarball, err := writeTarball(files)
		if err != nil {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("repackage tarball: %v", err), Cause: err}
		}

		// 5. Upload the new tarball.
		ec.Report(95, "uploading interpolated project")
		art, putErr := ec.Artifacts.Put(ctx, "hyperframes.interpolate", "interpolated.tar.gz", newTarball, "application/gzip")
		if putErr != nil {
			return nil, &packs.PackError{Code: packs.CodeArtifactFailed,
				Message: fmt.Sprintf("upload interpolated tarball: %v", putErr), Cause: putErr}
		}

		out := map[string]any{
			"project_artifact_key":          art.Key,
			"original_project_artifact_key": in.ProjectArtifactKey,
			"files_rewritten":               rewritten,
			"files_skipped":                 skipped,
			"model_used":                    in.Model,
		}
		return json.Marshal(out)
	}
}

// --- tarball helpers ----------------------------------------------------

type tarFile struct {
	Header *tar.Header
	Data   []byte
}

// extractTarball decompresses + reads every regular file in the tarball
// into a slice (preserving order so the rewrite pass is deterministic).
// Directory / symlink / device entries are skipped — the scaffold
// shouldn't contain them.
func extractTarball(tarballBytes []byte) ([]tarFile, error) {
	gz, err := gzip.NewReader(bytes.NewReader(tarballBytes))
	if err != nil {
		return nil, fmt.Errorf("gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	var files []tarFile
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tar next: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			// Preserve dirs/links in the output but with no Data.
			// Simpler to just skip non-regular entries — tar -czf
			// captures all the directories from file paths anyway.
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", hdr.Name, err)
		}
		files = append(files, tarFile{Header: hdr, Data: data})
	}
	return files, nil
}

// writeTarball serializes a slice of tarFile entries back into a
// gzipped tar archive. Mode and metadata fields are preserved from the
// original headers, only Size is recomputed (callers should also do
// this when they mutate Data).
func writeTarball(files []tarFile) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range files {
		hdr := *f.Header // copy
		hdr.Size = int64(len(f.Data))
		if err := tw.WriteHeader(&hdr); err != nil {
			return nil, fmt.Errorf("write header %s: %w", hdr.Name, err)
		}
		if _, err := tw.Write(f.Data); err != nil {
			return nil, fmt.Errorf("write data %s: %w", hdr.Name, err)
		}
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("tar close: %w", err)
	}
	if err := gz.Close(); err != nil {
		return nil, fmt.Errorf("gzip close: %w", err)
	}
	return buf.Bytes(), nil
}

// --- composition content classification ---------------------------------

const (
	compositionKindTextSlots  = "html_text_slots"
	compositionKindTranscript = "js_transcript"
	compositionKindUnknown    = "unknown_shape"
)

// transcriptMarker is the canonical opening of the caption transcript
// array used by upstream scaffolds (captions.html). The capture lazily
// matches up to the array's closing `];` — array literal contents may
// contain `[`/`]`/`{`/`}` inside object values, so we anchor strictly
// to `const TRANSCRIPT = [` … `];`.
var transcriptRE = regexp.MustCompile(`(?s)const\s+TRANSCRIPT\s*=\s*\[(.*?)\];`)

// textSlotRE captures inner text from tags upstream scaffolds use for
// visible content. We deliberately keep the set narrow so we don't
// accidentally rewrite chrome (e.g. <title>, hidden labels) or text
// inside <style>/<script>. Inner content is allowed to contain HTML
// entities and inline tags; we splice the whole inner range.
var textSlotRE = regexp.MustCompile(`(?s)<(h[1-3])(\s[^>]*)?>(.*?)</h[1-3]>|<div(\s[^>]*\bclass\s*=\s*"(?:stat-value|stat-label)"[^>]*)>(.*?)</div>`)

// classifyCompositionFile picks the rewrite strategy by scanning for
// the canonical content markers. Order matters: a file with BOTH
// a TRANSCRIPT array and stray <h1> chrome is treated as transcript
// (captions.html sometimes has a `<title>` or template-id chrome that
// would false-positive otherwise).
func classifyCompositionFile(content string) string {
	if transcriptRE.MatchString(content) {
		return compositionKindTranscript
	}
	if textSlotRE.MatchString(content) {
		return compositionKindTextSlots
	}
	return compositionKindUnknown
}

// --- text-slot rewriting ------------------------------------------------

// extractTextSlots returns the inner text of every slot the LLM is
// allowed to rewrite, in the order they appear in the file. The
// returned slice is consumed in order by spliceTextSlots when applying
// the LLM's response.
func extractTextSlots(content string) []string {
	matches := textSlotRE.FindAllStringSubmatch(content, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		// Match shape: full match | h-tag | h-attrs | h-inner | div-attrs | div-inner
		switch {
		case m[1] != "":
			out = append(out, strings.TrimSpace(m[3]))
		case m[4] != "":
			out = append(out, strings.TrimSpace(m[5]))
		}
	}
	return out
}

// spliceTextSlots replaces each slot's inner text with the
// corresponding entry from newSlots, in order. Extra newSlots entries
// are ignored; missing entries leave the original text intact.
func spliceTextSlots(content string, newSlots []string) string {
	i := 0
	return textSlotRE.ReplaceAllStringFunc(content, func(match string) string {
		if i >= len(newSlots) {
			i++
			return match
		}
		sub := textSlotRE.FindStringSubmatch(match)
		replacement := newSlots[i]
		i++
		switch {
		case sub[1] != "":
			// <hN ...>OLD</hN> → <hN ...>NEW</hN>
			tag := sub[1]
			attrs := sub[2]
			return "<" + tag + attrs + ">" + replacement + "</" + tag + ">"
		case sub[4] != "":
			// <div class="stat-value|stat-label">OLD</div>
			attrs := sub[4]
			return "<div" + attrs + ">" + replacement + "</div>"
		}
		return match
	})
}

func rewriteTextSlots(ctx context.Context, d vision.Dispatcher, model string, maxTokens int, content, description, audioNote string) (string, error) {
	slots := extractTextSlots(content)
	if len(slots) == 0 {
		return content, nil
	}
	system := interpolateTextSlotsPromptFor(model, len(slots), description, audioNote)

	var b strings.Builder
	b.WriteString("Original slots:\n")
	for i, s := range slots {
		fmt.Fprintf(&b, "%d: %s\n", i+1, s)
	}
	chat, err := d.Dispatch(ctx, gateway.ChatRequest{
		Model:     model,
		MaxTokens: &maxTokens,
		Messages: []gateway.Message{
			{Role: "system", Content: gateway.TextContent(system)},
			{Role: "user", Content: gateway.TextContent(b.String())},
		},
	})
	if err != nil {
		return "", err
	}
	if len(chat.Choices) == 0 {
		return "", fmt.Errorf("gateway returned no choices")
	}
	raw := strings.TrimSpace(chat.Choices[0].Message.Content.Text())
	newSlots := parseNumberedSlots(raw, len(slots))
	// Fall back to upstream text for any slot the model didn't return.
	for i := range slots {
		if i >= len(newSlots) || strings.TrimSpace(newSlots[i]) == "" {
			if i < len(newSlots) {
				newSlots[i] = slots[i]
			} else {
				newSlots = append(newSlots, slots[i])
			}
		}
	}
	return spliceTextSlots(content, newSlots[:len(slots)]), nil
}

// parseNumberedSlots parses "N: text\nN: text\n..." replies, tolerating
// blank lines and inconsistent whitespace. Out-of-order numbering is
// honored — if the model writes "3: foo\n1: bar", slot 1 gets "bar"
// and slot 3 gets "foo".
func parseNumberedSlots(raw string, expected int) []string {
	out := make([]string, expected)
	lineRE := regexp.MustCompile(`(?m)^\s*(\d+)\s*[:.\-]\s*(.+?)\s*$`)
	for _, m := range lineRE.FindAllStringSubmatch(raw, -1) {
		idx := 0
		for _, c := range m[1] {
			idx = idx*10 + int(c-'0')
		}
		idx--
		if idx >= 0 && idx < expected {
			out[idx] = m[2]
		}
	}
	return out
}

// --- transcript rewriting -----------------------------------------------

type transcriptWord struct {
	Text  string  `json:"text"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// rewriteTranscript asks the LLM for a fresh TRANSCRIPT array timed to
// the audio duration, then splices it into the file in place of the
// original `const TRANSCRIPT = [ ... ];`. The original timing is
// abandoned (it was aligned to the upstream example's audio, not the
// caller's), but the new array's spans must cover [0, duration).
func rewriteTranscript(ctx context.Context, d vision.Dispatcher, model string, maxTokens int, content, description string, duration float64, audioNote string) (string, error) {
	if !transcriptRE.MatchString(content) {
		return content, nil
	}
	system := interpolateTranscriptPromptFor(model, description, duration, audioNote)
	user := fmt.Sprintf("Duration: %.1fs\nTopic: %s\n%s", duration, description, audioNote)
	chat, err := d.Dispatch(ctx, gateway.ChatRequest{
		Model:     model,
		MaxTokens: &maxTokens,
		Messages: []gateway.Message{
			{Role: "system", Content: gateway.TextContent(system)},
			{Role: "user", Content: gateway.TextContent(user)},
		},
	})
	if err != nil {
		return "", err
	}
	if len(chat.Choices) == 0 {
		return "", fmt.Errorf("gateway returned no choices")
	}
	raw := unwrapCodeFence(strings.TrimSpace(chat.Choices[0].Message.Content.Text()))
	words, err := parseTranscriptArray(raw)
	if err != nil {
		return "", fmt.Errorf("parse transcript: %w", err)
	}
	if len(words) == 0 {
		return "", fmt.Errorf("model returned an empty transcript")
	}

	// Serialize back into JS-array-literal form. We emit each entry on
	// its own line for diff-friendliness; floats use 3 decimal places
	// which matches upstream's precision.
	var b strings.Builder
	b.WriteString("[\n")
	for i, w := range words {
		fmt.Fprintf(&b, "  { text: %q, start: %.3f, end: %.3f }", w.Text, w.Start, w.End)
		if i < len(words)-1 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	b.WriteString("]")
	newArrayLiteral := b.String()

	// Splice it in: replace the FIRST `const TRANSCRIPT = [ ... ];`
	// occurrence with the new array literal. The trailing `;` is part
	// of the replacement.
	return transcriptRE.ReplaceAllString(content, "const TRANSCRIPT = "+newArrayLiteral+";"), nil
}

// parseTranscriptArray accepts either a raw JSON array or a slightly
// liberal variant — the LLM may emit JavaScript-style `text:` (no
// quotes around keys) which JSON parsers reject. We try strict JSON
// first, then fall back to a lenient regex parser before giving up.
func parseTranscriptArray(raw string) ([]transcriptWord, error) {
	// Strict JSON path.
	var strict []transcriptWord
	if err := json.Unmarshal([]byte(raw), &strict); err == nil && len(strict) > 0 {
		return strict, nil
	}
	// Lenient path: extract objects via regex.
	lenientRE := regexp.MustCompile(`\{\s*(?:"text"|text)\s*:\s*"((?:[^"\\]|\\.)*)"\s*,\s*(?:"start"|start)\s*:\s*([0-9.]+)\s*,\s*(?:"end"|end)\s*:\s*([0-9.]+)\s*\}`)
	matches := lenientRE.FindAllStringSubmatch(raw, -1)
	if len(matches) == 0 {
		return nil, fmt.Errorf("no transcript objects parsed from response")
	}
	out := make([]transcriptWord, 0, len(matches))
	for _, m := range matches {
		var start, end float64
		_, errs := fmt.Sscanf(m[2], "%f", &start)
		_, erre := fmt.Sscanf(m[3], "%f", &end)
		if errs != nil || erre != nil {
			continue
		}
		out = append(out, transcriptWord{Text: m[1], Start: start, End: end})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("transcript objects parsed but all malformed")
	}
	return out, nil
}

// --- prompts ------------------------------------------------------------

const interpolateTextSlotsPromptTierC = `You rewrite VISIBLE TEXT in a short-form video composition. You will be given numbered text slots; the user has a topic. Replace each slot's text with on-topic content.

Reply with EXACTLY this format — one line per slot, no preamble, no markdown fences, no extra commentary:
1: <new text>
2: <new text>
3: <new text>

Hard rules:
- Match the original's casing (UPPERCASE → UPPERCASE; Title Case → Title Case).
- Match the original's approximate length (short stays short; a stat like "47%%" stays short and numeric).
- No HTML tags. No quotes around the text.
- Stay strictly on-topic with the user's description.
- If a slot looks like a percentage or fraction (e.g. "47%%", "3 OF 4"), reply with a plausible-looking statistic in the same shape.
- If a slot is a short label (≤5 words, often UPPERCASE), reply with a short label of similar length.
- If you don't have a confident replacement, repeat the original verbatim — never invent placeholder text like "TODO" or "EXAMPLE".`

const interpolateTextSlotsPromptTierAB = `Rewrite the numbered visible-text slots from a hyperframes composition to fit the user's topic. Reply ONE LINE PER SLOT in the form "N: <new text>", no preamble, no markdown.

Match each original's casing and approximate length. Stats stay numeric. Labels stay terse. No HTML tags. If unsure, repeat the original — never emit placeholder text.`

const interpolateTranscriptPromptTierC = `You generate a word-level caption transcript for a HyperFrames video. The video runs %.1f seconds and is about: %s.%s

Output ONE JSON array of word objects (no preamble, no markdown fences):
[
  { "text": "First", "start": 0.000, "end": 0.250 },
  { "text": "word", "start": 0.260, "end": 0.560 },
  ...
]

Hard rules:
- Target ~150 words per minute (so roughly %.0f total words for %.1fs).
- Cover [0, %.1f) with no gap longer than 1.0s between consecutive words.
- Each word's start < end, and consecutive words' start values increase monotonically.
- One actual word per object — split contractions if natural ("don't" can be one entry or "don" + "'t"; pick what reads more naturally as a caption).
- Punctuation is OK at the END of a word's text ("hello," / "World."); never lead with it.
- Stay strictly on-topic with the description.`

const interpolateTranscriptPromptTierAB = `Generate a word-level caption transcript for a HyperFrames video. Output a JSON array of {text, start, end} objects covering [0, %.1f) at ~150 wpm. No gaps longer than 1s. On-topic with: %s.%s Reply with the array only — no preamble, no markdown fences.`

func interpolateTextSlotsPromptFor(model string, _ int, description, audioNote string) string {
	tier := llmcontext.BudgetFor(model).Tier
	prompt := interpolateTextSlotsPromptTierC
	if tier == llmcontext.TierA || tier == llmcontext.TierB {
		prompt = interpolateTextSlotsPromptTierAB
	}
	if strings.TrimSpace(description) != "" {
		prompt += "\n\nTopic: " + description
	}
	if strings.TrimSpace(audioNote) != "" {
		prompt += "\n" + audioNote
	}
	return prompt
}

func interpolateTranscriptPromptFor(model, description string, duration float64, audioNote string) string {
	tier := llmcontext.BudgetFor(model).Tier
	targetWords := duration * 2.5 // 150 wpm
	if tier == llmcontext.TierA || tier == llmcontext.TierB {
		return fmt.Sprintf(interpolateTranscriptPromptTierAB, duration, description, audioNote)
	}
	return fmt.Sprintf(interpolateTranscriptPromptTierC, duration, description, audioNote, targetWords, duration, duration)
}
