// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// artifact_put.go — deterministic deposit step for skill outputs.
//
// Why this pack exists: every helmdeck skill that produces content
// for an audience (blog posts, transcripts, research summaries) has
// the same load-bearing instruction in its SKILL.md — "push the final
// result to Artifacts so the operator can download it." Tier A/B
// models follow that prose. Tier C models (the free OpenRouter
// tier — gpt-oss-120b:free, llama-3.3-70b:free, etc. — see ADR 051)
// silently treat skill prose as a suggestion and return the markdown
// inline in the chat response instead. The content is then trapped
// in the conversation log, not in the artifact store where the
// publishing step can find it.
//
// The fix is the same shape we used for av.validate: turn an
// advisory prose step into a typed pack call. A skill that ends with
//
//   helmdeck__artifact-put { kind: "blog", content: "..." }
//
// gets deterministic deposit regardless of model tier. The pack does
// one thing — write bytes to the artifact store under a chosen
// namespace — and returns the artifact_key so the next pack in the
// chain (blog.publish, email.send, etc.) can reference it by handle
// instead of re-pasting the content.
//
// Deliberately no NeedsSession: writing to the artifact store is
// in-process, doesn't need a sidecar, and the round-trip overhead
// of acquiring a session would dominate the work. This makes
// artifact.put cheap enough that skills can call it on every
// publication-shaped output without thinking about cost.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/tosin2013/helmdeck/internal/packs"
)

// artifactPutDefaults maps a kind hint to (default filename, default
// content_type). Kinds chosen to cover what skills actually produce:
// blog-shaped markdown, plain-text transcripts, JSON sidecars, raw
// binary fallback. New kinds get added here when a new skill needs
// one — the alternative (hard-coding the defaults per skill) reverts
// to the prose-instruction failure mode we're trying to fix.
var artifactPutDefaults = map[string]struct {
	Filename    string
	ContentType string
}{
	"blog":       {"content.md", "text/markdown"},
	"markdown":   {"content.md", "text/markdown"},
	"transcript": {"transcript.txt", "text/plain"},
	"summary":    {"summary.md", "text/markdown"},
	"json":       {"content.json", "application/json"},
	"text":       {"content.txt", "text/plain"},
	"html":       {"content.html", "text/html"},
	"csv":        {"content.csv", "text/csv"},
	"binary":     {"content.bin", "application/octet-stream"},
}

// artifactPutDefaultNamespace is where the bytes get filed when the
// caller doesn't choose. Distinct namespace so an operator browsing
// artifacts can tell skill-deposited content apart from packs that
// produce their own artifacts as a side effect.
const artifactPutDefaultNamespace = "artifact.put"

// ArtifactPut constructs the pack. No external deps, no session.
// Reusable across every skill that produces audience-facing content.
func ArtifactPut() *packs.Pack {
	return &packs.Pack{
		Name:    "artifact.put",
		Version: "v1",
		Description: "Deposit a final skill output into the artifact store and return a stable " +
			"artifact_key. Use this as the LAST step in any skill that produces content for an " +
			"operator to download or chain into another pack (blog.publish, email.send, etc.). " +
			"Replaces the prose instruction 'remember to save to artifacts' with a deterministic " +
			"pack call — works on every model tier, including Tier C free models that silently " +
			"ignore skill-level guidance. Accepts a `kind` hint (blog/markdown/transcript/summary/" +
			"json/text/html/csv/binary) that drives default filename + content_type so callers " +
			"don't have to think about MIME types.",
		NeedsSession: false,
		Async:        false,
		InputSchema: packs.BasicSchema{
			Required: []string{"content"},
			Properties: map[string]string{
				"content":      "string",
				"kind":         "string",
				"filename":     "string",
				"content_type": "string",
				"encoding":     "string",
				"namespace":    "string",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"artifact_key", "url", "size", "content_type"},
			Properties: map[string]string{
				"artifact_key": "string",
				"url":          "string",
				"size":         "number",
				"content_type": "string",
				"filename":     "string",
				"namespace":    "string",
			},
		},
		Metadata: packs.PackMetadata{
			Accepts:  []string{"markdown", "text", "json", "html"},
			Produces: []string{"artifact_key"},
			IntentKeywords: []string{
				"save to artifacts", "deposit artifact", "publish artifact",
				"store final output", "make downloadable",
			},
			TypicalUse: "Final deposit step in a skill that produced audience-facing content. " +
				"Chain after content generation, before publish/send packs.",
			Limitations: []string{
				"does not encrypt content — assume readers of the artifact store can read it",
				"does not deduplicate — calling twice writes two artifacts",
				"max content size bounded by artifact-store backend (memory store: process RAM)",
			},
		},
		Handler: artifactPutHandler(),
	}
}

type artifactPutInput struct {
	Content     string `json:"content"`
	Kind        string `json:"kind"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Encoding    string `json:"encoding"`
	Namespace   string `json:"namespace"`
}

func artifactPutHandler() packs.HandlerFunc {
	return func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
		var in artifactPutInput
		if err := json.Unmarshal(ec.Input, &in); err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
		}
		if in.Content == "" {
			return nil, &packs.PackError{
				Code:    packs.CodeInvalidInput,
				Message: "artifact.put: content is required and must be non-empty",
			}
		}
		if ec.Artifacts == nil {
			return nil, &packs.PackError{
				Code:    packs.CodeArtifactFailed,
				Message: "artifact.put: no artifact store wired into this execution context",
			}
		}

		body, err := decodeContent(in.Content, in.Encoding)
		if err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
		}

		filename, contentType := resolveFilenameAndType(in.Kind, in.Filename, in.ContentType)
		namespace := resolveNamespace(in.Namespace)

		art, err := ec.Artifacts.Put(ctx, namespace, filename, body, contentType)
		if err != nil {
			return nil, &packs.PackError{
				Code:    packs.CodeArtifactFailed,
				Message: fmt.Sprintf("artifact.put: store.Put failed: %v", err),
				Cause:   err,
			}
		}

		out := map[string]any{
			"artifact_key": art.Key,
			"url":          art.URL,
			"size":         art.Size,
			"content_type": art.ContentType,
			"filename":     filename,
			"namespace":    namespace,
		}
		return json.Marshal(out)
	}
}

// decodeContent translates the input string to bytes per the
// encoding hint. UTF-8 (the default and overwhelmingly common case)
// is a no-op string→bytes; base64 decodes for binary deposits the
// JSON envelope can't carry literally. Unknown encodings reject
// rather than silently passing through as UTF-8 — a typo in the
// encoding field would otherwise file the literal base64 text as
// the artifact content, which is worse than failing fast.
func decodeContent(content, encoding string) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "utf-8", "utf8", "text":
		return []byte(content), nil
	case "base64":
		decoded, err := base64.StdEncoding.DecodeString(content)
		if err != nil {
			return nil, fmt.Errorf("artifact.put: base64 decode: %w", err)
		}
		return decoded, nil
	default:
		return nil, fmt.Errorf("artifact.put: unsupported encoding %q (want utf-8 or base64)", encoding)
	}
}

// resolveFilenameAndType picks the filename and content_type for the
// deposit. Precedence: explicit filename/content_type beats the kind
// defaults; kind defaults beat the generic text fallback. The kind
// table is intentionally small — adding a kind is a deliberate API
// move, not something we want callers reaching for ad-hoc.
//
// Filename safety: strip any leading `/` and resolve `..` so a caller
// can't escape the pack namespace the store imposes. The store
// itself prefixes `<pack>/<rand>-<name>` so cosmetic damage is the
// worst case, but stripping is cheap and removes a class of "wait,
// why is there a slash in my key" surprises.
func resolveFilenameAndType(kind, filename, contentType string) (string, string) {
	defaults, kindKnown := artifactPutDefaults[strings.ToLower(strings.TrimSpace(kind))]
	if !kindKnown {
		defaults = artifactPutDefaults["text"]
	}
	if strings.TrimSpace(filename) == "" {
		filename = defaults.Filename
	} else {
		filename = sanitizeFilename(filename)
	}
	if strings.TrimSpace(contentType) == "" {
		contentType = defaults.ContentType
	}
	return filename, contentType
}

func resolveNamespace(ns string) string {
	ns = strings.TrimSpace(ns)
	if ns == "" {
		return artifactPutDefaultNamespace
	}
	return ns
}

func sanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimLeft(name, "/")
	name = path.Clean(name)
	// Strip leading "../" segments so a caller can't traverse out of
	// the namespace prefix the store imposes. path.Clean preserves
	// leading parent references (it has no idea what root is), so we
	// do it ourselves.
	for strings.HasPrefix(name, "../") {
		name = strings.TrimPrefix(name, "../")
	}
	// path.Clean turns "" into "." — that's not a sensible filename;
	// fall back to the generic default rather than letting it through.
	if name == "" || name == "." || name == ".." {
		return artifactPutDefaults["text"].Filename
	}
	return name
}
