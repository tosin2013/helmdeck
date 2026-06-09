// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// artifact_get.go — symmetric counterpart to artifact.put. Lets the
// LLM fetch an artifact's bytes by key as a typed pack call instead
// of expecting an external system to inline them into the prompt.
//
// Two use cases this closes:
//
//   1. User-uploaded files. Once a POST /api/v1/artifacts endpoint
//      lands (separate PR), an operator can upload a PDF / markdown
//      / CSV through the management UI and the agent can pick it up
//      by listing + fetching. No chat-UI changes needed.
//
//   2. Cross-pack introspection. After slides.narrate emits a
//      validation.json sidecar, an agent can artifact.get the
//      sidecar and reason over the structured report without the
//      orchestrating skill needing to thread the value through.
//
// Encoding policy mirrors artifact.put: text-shaped content_types
// (text/*, application/json, application/yaml, application/xml,
// *+json, *+xml) return as UTF-8 strings by default; everything else
// returns base64-encoded with `encoding:"base64"` set on the output.
// The caller can force base64 with `encoding:"base64"` on input when
// they know they're going to chain into a base64-expecting consumer.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tosin2013/helmdeck/internal/packs"
)

// ArtifactGet constructs the pack. No external deps, no session.
// The handler is a thin wrapper over ArtifactStore.Get with an
// encoding-policy layer.
func ArtifactGet() *packs.Pack {
	return &packs.Pack{
		Name:    "artifact.get",
		Version: "v1",
		Description: "Fetch an artifact's bytes by key. Returns text-shaped content (markdown, " +
			"JSON, YAML, plain text) as a UTF-8 string by default; binary content (images, audio, " +
			"video) returns base64-encoded with `encoding:\"base64\"` set on the output so the LLM " +
			"can tell which decoding to apply. Use this when an operator has uploaded a file the " +
			"agent needs to read, when chaining packs that produced artifacts you want to inspect " +
			"rather than blind-pass, or when consolidating multiple intermediate artifacts in a " +
			"skill.",
		NeedsSession: false,
		Async:        false,
		InputSchema: packs.BasicSchema{
			Required: []string{"artifact_key"},
			Properties: map[string]string{
				"artifact_key": "string",
				"encoding":     "string",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"content", "encoding", "content_type", "size"},
			Properties: map[string]string{
				"content":      "string",
				"encoding":     "string",
				"content_type": "string",
				"size":         "number",
				"artifact_key": "string",
				"filename":     "string",
				"namespace":    "string",
			},
		},
		Metadata: packs.PackMetadata{
			Accepts:  []string{"artifact_key"},
			Produces: []string{"text", "markdown", "json"},
			IntentKeywords: []string{
				"read artifact", "fetch artifact", "get artifact content",
				"download artifact", "inspect artifact",
			},
			TypicalUse: "Read an artifact the operator uploaded or a prior pack produced, " +
				"and use its content in the next pack call.",
			Limitations: []string{
				"does not decompress archives — zip/tar bytes return verbatim",
				"large binary artifacts inflate ~33% when base64-encoded",
			},
		},
		Handler: artifactGetHandler(),
	}
}

type artifactGetInput struct {
	ArtifactKey string `json:"artifact_key"`
	Encoding    string `json:"encoding"`
}

func artifactGetHandler() packs.HandlerFunc {
	return func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
		var in artifactGetInput
		if err := json.Unmarshal(ec.Input, &in); err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
		}
		if strings.TrimSpace(in.ArtifactKey) == "" {
			return nil, &packs.PackError{
				Code:    packs.CodeInvalidInput,
				Message: "artifact.get: artifact_key is required and must be non-empty",
			}
		}
		if ec.Artifacts == nil {
			return nil, &packs.PackError{
				Code:    packs.CodeArtifactFailed,
				Message: "artifact.get: no artifact store wired into this execution context",
			}
		}

		body, art, err := ec.Artifacts.Get(ctx, in.ArtifactKey)
		if err != nil {
			return nil, &packs.PackError{
				Code:    packs.CodeArtifactFailed,
				Message: fmt.Sprintf("artifact.get: store.Get(%q): %v", in.ArtifactKey, err),
				Cause:   err,
			}
		}

		encoding, content := encodeContent(body, art.ContentType, in.Encoding)
		filename, namespace := splitArtifactKey(art.Key)

		out := map[string]any{
			"content":      content,
			"encoding":     encoding,
			"content_type": art.ContentType,
			"size":         art.Size,
			"artifact_key": art.Key,
			"filename":     filename,
			"namespace":    namespace,
		}
		return json.Marshal(out)
	}
}

// encodeContent picks the encoding for the returned content based on
// (forceEncoding override) > (content_type heuristic). Text-shaped
// content types default to UTF-8 so the LLM can read them directly;
// everything else returns base64 so a non-UTF-8 byte sequence (PNG,
// MP4, etc.) doesn't blow up the JSON envelope. Returns (encoding,
// content).
//
// forceEncoding values: "utf-8" / "utf8" / "text" force text output
// regardless of content_type (caller asserts they know the bytes are
// readable); "base64" forces base64 regardless. Empty → heuristic.
func encodeContent(body []byte, contentType, forceEncoding string) (string, string) {
	switch strings.ToLower(strings.TrimSpace(forceEncoding)) {
	case "base64":
		return "base64", base64.StdEncoding.EncodeToString(body)
	case "utf-8", "utf8", "text":
		return "utf-8", string(body)
	}
	if isTextContentType(contentType) {
		return "utf-8", string(body)
	}
	return "base64", base64.StdEncoding.EncodeToString(body)
}

// isTextContentType returns true for MIME types the LLM can read as
// a UTF-8 string without corruption. The list is deliberately
// conservative — unknown types default to base64 so a misclassified
// binary doesn't surface as garbled text in the model's context.
func isTextContentType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if ct == "" {
		return false
	}
	if strings.HasPrefix(ct, "text/") {
		return true
	}
	switch ct {
	case "application/json", "application/yaml", "application/x-yaml",
		"application/xml", "application/javascript", "application/sql",
		"application/toml":
		return true
	}
	// `*+json` / `*+xml` (e.g. application/ld+json) — RFC 6839 structured
	// suffix convention.
	if strings.HasSuffix(ct, "+json") || strings.HasSuffix(ct, "+xml") ||
		strings.HasSuffix(ct, "+yaml") {
		return true
	}
	return false
}

// splitArtifactKey decomposes a `<namespace>/<rand>-<filename>` key
// back into (filename, namespace). The store's Put generates the
// namespace prefix + random suffix, so this is purely cosmetic — the
// caller might want to display the filename to a user, or namespace
// the followup. Defensive: a malformed key (no slash, no dash) just
// reports the original key as the filename.
func splitArtifactKey(key string) (filename, namespace string) {
	slash := strings.Index(key, "/")
	if slash < 0 {
		return key, ""
	}
	namespace = key[:slash]
	rest := key[slash+1:]
	if dash := strings.Index(rest, "-"); dash >= 0 {
		filename = rest[dash+1:]
	} else {
		filename = rest
	}
	return filename, namespace
}
