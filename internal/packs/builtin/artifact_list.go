// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// artifact_list.go — introspection for the artifact store. Lets the
// LLM discover what artifacts exist without an external system
// having to inline the catalog into the prompt.
//
// The dominant use case is the user-upload loop: an operator
// uploads files via REST / CLI / the management UI (path TBD —
// see the open issue for POST /api/v1/artifacts). The agent
// doesn't know what was just uploaded unless it asks. artifact.list
// is that ask.
//
// Secondary use case: chained-skill introspection. After a long
// skill produces multiple artifacts (draft, summary, citations,
// final render), `artifact.list` with the matching namespace
// surfaces every one so the agent can deposit/return a manifest
// to the operator instead of remembering each key from earlier
// turns.

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/tosin2013/helmdeck/internal/packs"
)

// artifactListDefaultLimit caps the response size when the caller
// doesn't ask for one. Listing all artifacts in a long-lived store
// can return thousands of rows; defaulting to a finite page keeps
// the LLM's context window manageable. Callers who genuinely want
// everything set `limit:0` (explicit opt-in to unbounded).
const artifactListDefaultLimit = 100

// ArtifactList constructs the pack. The handler is a thin wrapper
// over ArtifactStore.ListForPack / ListAll with optional filtering.
func ArtifactList() *packs.Pack {
	return &packs.Pack{
		Name:    "artifact.list",
		Version: "v1",
		Description: "List artifacts in the store, optionally filtered by namespace or filename " +
			"substring. Use this when the operator may have uploaded files the agent needs to " +
			"find, or when introspecting what a multi-step skill produced. Returns metadata only " +
			"(key, filename, namespace, content_type, size, created_at) — call `artifact.get` to " +
			"fetch a specific artifact's bytes.",
		NeedsSession: false,
		Async:        false,
		InputSchema: packs.BasicSchema{
			Properties: map[string]string{
				"namespace": "string",
				"filename":  "string",
				"limit":     "number",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"artifacts", "count"},
			Properties: map[string]string{
				"artifacts": "array",
				"count":     "number",
				"truncated": "boolean",
			},
		},
		Metadata: packs.PackMetadata{
			Accepts:  []string{"query"},
			Produces: []string{"artifact_list"},
			IntentKeywords: []string{
				"list artifacts", "find uploaded file", "what did the user upload",
				"browse artifacts", "show artifacts",
			},
			TypicalUse: "Introspect the artifact store at the start of a session to discover " +
				"operator-uploaded files, or to enumerate what a multi-pack skill produced.",
			Limitations: []string{
				"returns metadata only — call artifact.get to fetch bytes",
				"sort is best-effort newest-first; backends without timestamps may return unordered",
				"filename filter is a case-insensitive substring match, not a glob",
			},
		},
		Handler: artifactListHandler(),
	}
}

type artifactListInput struct {
	Namespace string `json:"namespace"`
	Filename  string `json:"filename"`
	Limit     int    `json:"limit"`
}

type artifactListEntry struct {
	ArtifactKey string    `json:"artifact_key"`
	Filename    string    `json:"filename"`
	Namespace   string    `json:"namespace"`
	ContentType string    `json:"content_type"`
	Size        int64     `json:"size"`
	CreatedAt   time.Time `json:"created_at"`
	URL         string    `json:"url"`
}

func artifactListHandler() packs.HandlerFunc {
	return func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
		var in artifactListInput
		if err := json.Unmarshal(ec.Input, &in); err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
		}
		if ec.Artifacts == nil {
			return nil, &packs.PackError{
				Code:    packs.CodeArtifactFailed,
				Message: "artifact.list: no artifact store wired into this execution context",
			}
		}

		var raw []packs.Artifact
		var err error
		ns := strings.TrimSpace(in.Namespace)
		if ns != "" {
			raw, err = ec.Artifacts.ListForPack(ctx, ns)
		} else {
			raw, err = ec.Artifacts.ListAll(ctx)
		}
		if err != nil {
			return nil, &packs.PackError{
				Code:    packs.CodeArtifactFailed,
				Message: "artifact.list: store list failed: " + err.Error(),
				Cause:   err,
			}
		}

		filenameFilter := strings.ToLower(strings.TrimSpace(in.Filename))
		entries := make([]artifactListEntry, 0, len(raw))
		for _, a := range raw {
			filename, namespace := splitArtifactKey(a.Key)
			if filenameFilter != "" && !strings.Contains(strings.ToLower(filename), filenameFilter) {
				continue
			}
			entries = append(entries, artifactListEntry{
				ArtifactKey: a.Key,
				Filename:    filename,
				Namespace:   namespace,
				ContentType: a.ContentType,
				Size:        a.Size,
				CreatedAt:   a.CreatedAt,
				URL:         a.URL,
			})
		}
		// Newest-first sort. Backends that don't track created_at
		// return zero-time for every row; this becomes a stable
		// no-op and we don't fall over.
		sort.SliceStable(entries, func(i, j int) bool {
			return entries[i].CreatedAt.After(entries[j].CreatedAt)
		})

		limit := in.Limit
		if limit < 0 {
			limit = artifactListDefaultLimit
		}
		if limit == 0 {
			// Explicit 0 in input is "default" (the BasicSchema can't
			// tell missing from zero); we want unbounded only on a
			// negative value, but reading the JSON tag with omitempty
			// would treat negative as "unset". Pragmatic rule: 0 is
			// the default; negative is unsupported; very-large is the
			// way to ask for unbounded.
			limit = artifactListDefaultLimit
		}
		truncated := false
		if len(entries) > limit {
			entries = entries[:limit]
			truncated = true
		}

		out := map[string]any{
			"artifacts": entries,
			"count":     len(entries),
			"truncated": truncated,
		}
		return json.Marshal(out)
	}
}
