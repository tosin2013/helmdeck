// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// blog_publish.go (#68) — publish a post to a Ghost blog OR write the
// rendered markdown/HTML to the artifact store.
//
// Two destinations:
//   - "ghost"    — POST to <host>/ghost/api/admin/posts/ with a 5-min
//                  HS256 JWT minted from the vault-stored Admin API key.
//                  Returns post id + html_url + status + published_at.
//   - "artifact" — write the body (rendered to format) as an artifact;
//                  return artifact_key + size.
//
// Two body modes:
//   - body mode   — agent already wrote the body and passes it as `body`
//   - prompt mode — agent passes `prompt` + `model`; pack expands via the
//                   gateway LLM. Requires the gateway dispatcher to be
//                   wired at registration; otherwise prompt mode returns
//                   CodeInternal at handler time. Body mode always works.
//
// Two formats:
//   - "markdown" — body stays as markdown (artifact destination), or
//                  is rendered to HTML before POSTing to Ghost (Ghost's
//                  modern API accepts html/lexical/mobiledoc, not raw md)
//   - "html"     — body is HTML; passes through unchanged to either
//                  destination
//
// Vault credential: "ghost-admin-key" (default), type api_key. Value is
// the Ghost Admin API key in its native colon-separated form: "<id>:<secret>"
// where <secret> is hex-encoded. The pack splits and uses both halves
// (id → JWT kid header; secret → HMAC key, hex-decoded) and never
// surfaces the secret in errors.

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/tosin2013/helmdeck/internal/gateway"
	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/security"
	"github.com/tosin2013/helmdeck/internal/vault"
	"github.com/tosin2013/helmdeck/internal/vision"
	"github.com/yuin/goldmark"
	"go.abhg.dev/goldmark/mermaid"
)

const (
	defaultGhostCred         = "ghost-admin-key"
	defaultBlogPromptMaxToks = 1024
	maxBlogResponse          = 1 << 20 // 1 MiB
	ghostJWTLifetime         = 5 * time.Minute
	ghostAdminPostsPath      = "/ghost/api/admin/posts/"
)

// BlogPublish constructs the blog.publish pack. Pass nil for d if the
// gateway is not wired — body mode still works; prompt mode returns
// CodeInternal at handler time.
func BlogPublish(v *vault.Store, eg *security.EgressGuard, d vision.Dispatcher) *packs.Pack {
	return &packs.Pack{
		Name:        "blog.publish",
		Version:     "v1",
		Description: "Publish a post to a Ghost blog or render markdown/HTML to the artifact store. Body or prompt+model. Optional feature image (auto-generated via fal.ai or operator-supplied artifact). Markdown bodies may include ```mermaid``` fenced blocks — diagrams render client-side in HTML/Ghost outputs (Ghost theme must include MermaidJS).",
		InputSchema: packs.BasicSchema{
			Required: []string{"format", "title"},
			Properties: map[string]string{
				"destination":                "string",  // "ghost" | "artifact" | "" (defaults to artifact)
				"also_save_artifact":         "boolean", // default true; when destination=ghost, also writes the artifact safety net
				"format":                     "string",  // "markdown" | "html"
				"title":                      "string",
				"body":                       "string",
				"prompt":                     "string",
				"model":                      "string",
				"max_tokens":                 "number",
				"tags":                       "array",
				"status":                     "string",
				"published_at":               "string",
				"host":                       "string",
				"credential":                 "string",
				"feature_image_artifact_key": "string",
				"hero_image":                 "boolean",
				"hero_image_model":           "string",
				"hero_image_prompt":          "string",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"destination", "format", "body_source"},
			Properties: map[string]string{
				"destination":                "string",
				"format":                     "string",
				"body_source":                "string",
				"model_used":                 "string",
				"post_id":                    "string",
				"url":                        "string",
				"html_url":                   "string",
				"status":                     "string", // "artifact_saved" | "draft" | "published" | "scheduled" | "artifact_saved_ghost_failed"
				"published_at":               "string",
				"artifact_key":               "string",
				"artifact_url":               "string",
				"size":                       "number",
				"ghost_error":                "string", // populated only when status="artifact_saved_ghost_failed"
				"feature_image_artifact_key": "string",
				"feature_image_url":          "string",
				"hero_image_model_used":      "string",
			},
		},
		Handler: blogPublishHandler(v, eg, d),
	}
}

type blogPublishInput struct {
	Destination string `json:"destination"`
	// AlsoSaveArtifact controls the artifact safety net. When destination
	// is "ghost", a true value (the default when unset) causes the pack
	// to also write the post body as an artifact so Ghost failures don't
	// lose the agent's work product. A nil pointer means "field absent in
	// JSON" → treated as true; an explicit false opts out of the safety
	// net (today's hard-fail-on-ghost-error behaviour). Pointer not bool
	// so we can distinguish absent vs explicitly-false.
	AlsoSaveArtifact *bool    `json:"also_save_artifact,omitempty"`
	Format           string   `json:"format"`
	Title       string   `json:"title"`
	Body        string   `json:"body"`
	Prompt      string   `json:"prompt"`
	Model       string   `json:"model"`
	MaxTokens   int      `json:"max_tokens"`
	Tags        []string `json:"tags"`
	Status      string   `json:"status"`
	PublishedAt string   `json:"published_at"`
	Host        string   `json:"host"`
	Credential  string   `json:"credential"`
	// Feature image inputs (#146d). Exactly one of these triggers
	// feature-image processing:
	//   feature_image_artifact_key — operator passes an artifact key
	//     from a prior pack call (typically image.generate or another
	//     content pack's cover_image_artifact_key); pack fetches the
	//     bytes from ec.Artifacts.Get and uploads to Ghost / surfaces
	//     in the artifact-mode response.
	//   hero_image — auto-generate via RunImageGen using title (or
	//     hero_image_prompt if set) as the prompt.
	FeatureImageArtifactKey string `json:"feature_image_artifact_key"`
	HeroImage               bool   `json:"hero_image"`
	HeroImageModel          string `json:"hero_image_model"`
	HeroImagePrompt         string `json:"hero_image_prompt"`
}

func blogPublishHandler(v *vault.Store, eg *security.EgressGuard, d vision.Dispatcher) packs.HandlerFunc {
	return func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
		var in blogPublishInput
		if err := json.Unmarshal(ec.Input, &in); err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
		}

		// Schema validation — closed sets and exactly-one-mode rules.
		// destination is now optional: empty/unset defaults to artifact-only
		// (the safety-net path). Explicit "ghost" or "artifact" still work.
		if in.Destination != "" && in.Destination != "ghost" && in.Destination != "artifact" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: `destination must be "ghost", "artifact", or empty (defaults to artifact)`}
		}
		if in.Format != "markdown" && in.Format != "html" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: `format must be "markdown" or "html"`}
		}
		if strings.TrimSpace(in.Title) == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "title is required"}
		}
		hasBody := strings.TrimSpace(in.Body) != ""
		hasPrompt := strings.TrimSpace(in.Prompt) != ""
		if hasBody == hasPrompt {
			if hasBody {
				return nil, &packs.PackError{Code: packs.CodeInvalidInput,
					Message: "must provide either body OR prompt+model, not both"}
			}
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: "must provide either body OR prompt+model"}
		}
		if hasPrompt && strings.TrimSpace(in.Model) == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: "prompt mode requires model (provider/model)"}
		}
		status := in.Status
		if status == "" {
			status = "draft"
		}
		switch status {
		case "draft", "published", "scheduled":
		default:
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: `status must be "draft", "published", or "scheduled"`}
		}
		if status == "scheduled" {
			if in.PublishedAt == "" {
				return nil, &packs.PackError{Code: packs.CodeInvalidInput,
					Message: "published_at (RFC3339) is required when status=scheduled"}
			}
			t, err := time.Parse(time.RFC3339, in.PublishedAt)
			if err != nil {
				return nil, &packs.PackError{Code: packs.CodeInvalidInput,
					Message: fmt.Sprintf("published_at must be RFC3339: %v", err)}
			}
			if !t.After(time.Now()) {
				return nil, &packs.PackError{Code: packs.CodeInvalidInput,
					Message: "published_at must be in the future for status=scheduled"}
			}
		}
		if in.Destination == "ghost" && strings.TrimSpace(in.Host) == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: "host is required when destination=ghost (the Ghost installation hostname)"}
		}

		// Resolve body — either echo input or expand prompt via gateway.
		body := in.Body
		bodySource := "input"
		modelUsed := ""
		if hasPrompt {
			if d == nil {
				return nil, &packs.PackError{Code: packs.CodeInternal,
					Message: "blog.publish prompt mode registered without a gateway dispatcher"}
			}
			expanded, err := blogExpandPrompt(ctx, d, in.Title, in.Prompt, in.Model, in.MaxTokens, in.Format)
			if err != nil {
				return nil, err
			}
			body = expanded
			bodySource = "model"
			modelUsed = in.Model
		}

		// Resolve feature image (#146d). Either operator-supplied
		// artifact key OR auto-generated via RunImageGen. Validation:
		// providing both is an input error — caller picks one source.
		fi, perr := resolveFeatureImage(ctx, ec, v, eg, in)
		if perr != nil {
			return nil, perr
		}

		// Dispatch — #203 artifact-first refactor.
		//
		//   - Always save an artifact (the safety net) UNLESS the caller
		//     explicitly opts out with also_save_artifact:false.
		//   - When destination is empty or "artifact": return the
		//     artifact-only response. This is the new default — agents
		//     that don't specify a destination get a saved post they can
		//     fetch later.
		//   - When destination is "ghost": attempt the Ghost publish on
		//     top of the saved artifact. If Ghost fails AND we saved the
		//     artifact, return a partial-success response so the agent
		//     can retry against the artifact without losing the body.
		//     If Ghost fails AND the caller opted out of the artifact
		//     safety net (also_save_artifact:false), fall through to the
		//     pre-refactor hard-fail behaviour.
		shouldSaveArtifact := in.AlsoSaveArtifact == nil || *in.AlsoSaveArtifact
		isGhost := in.Destination == "ghost"

		var artifactJSON json.RawMessage
		if shouldSaveArtifact || !isGhost {
			aj, err := blogPublishArtifact(ctx, ec, in.Title, body, in.Format, bodySource, modelUsed, fi)
			if err != nil {
				// Artifact-write failure is hard fail — the safety net
				// itself broke; nothing useful to return.
				return nil, err
			}
			artifactJSON = aj
		}

		if !isGhost {
			return artifactJSON, nil
		}

		ghostJSON, gerr := blogPublishGhost(ctx, ec, eg, v, in, body, bodySource, modelUsed, fi)
		if gerr != nil {
			if artifactJSON != nil {
				// Partial success: post body is safe in the artifact
				// store, Ghost publish failed. Surface both so the agent
				// can retry the Ghost step without re-running prompt
				// expansion.
				return mergeBlogResults(artifactJSON, partialFailureResult(gerr)), nil
			}
			// Caller opted out of the safety net AND Ghost failed —
			// honour the original hard-fail contract.
			return nil, gerr
		}

		if artifactJSON == nil {
			return ghostJSON, nil
		}
		return mergeBlogResults(artifactJSON, ghostJSON), nil
	}
}

// mergeBlogResults combines the artifact and Ghost response shapes into
// a single JSON object. Ghost fields take precedence on overlapping
// keys (destination, format, body_source, status) — they describe the
// authoritative outcome of the user-requested side effect. The artifact
// fields (artifact_key, artifact_url, size) survive the merge because
// Ghost doesn't emit them.
//
// Errors during unmarshal/marshal fall back to returning the artifact
// half alone — better partial information than no response at all.
func mergeBlogResults(artifactJSON, ghostJSON json.RawMessage) json.RawMessage {
	var am, gm map[string]any
	if err := json.Unmarshal(artifactJSON, &am); err != nil {
		return artifactJSON
	}
	if err := json.Unmarshal(ghostJSON, &gm); err != nil {
		return artifactJSON
	}
	for k, v := range gm {
		am[k] = v
	}
	out, err := json.Marshal(am)
	if err != nil {
		return artifactJSON
	}
	return out
}

// partialFailureResult builds the artifact-saved-ghost-failed response
// fragment. Merged on top of the artifact response, this gives the
// agent enough information to retry Ghost against the saved body.
func partialFailureResult(err error) json.RawMessage {
	out := map[string]any{
		"status":      "artifact_saved_ghost_failed",
		"destination": "ghost",
		"ghost_error": err.Error(),
	}
	raw, _ := json.Marshal(out)
	return raw
}

// featureImage carries the resolved feature-image state through the
// destination handlers. Empty Bytes means "no feature image" — both
// destinations short-circuit gracefully on the zero value.
type featureImage struct {
	Bytes       []byte
	ArtifactKey string // helmdeck artifact key the bytes came from
	ContentType string
	ModelUsed   string // empty when caller supplied the artifact directly
}

// resolveFeatureImage picks between (a) operator-supplied artifact and
// (b) auto-generated hero. Returns zero value when neither is requested.
// Hard-fails if both are set (mutually exclusive).
func resolveFeatureImage(ctx context.Context, ec *packs.ExecutionContext, v *vault.Store, eg *security.EgressGuard, in blogPublishInput) (featureImage, *packs.PackError) {
	hasArt := strings.TrimSpace(in.FeatureImageArtifactKey) != ""
	if hasArt && in.HeroImage {
		return featureImage{}, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: "provide either feature_image_artifact_key OR hero_image:true, not both"}
	}
	if hasArt {
		if ec.Artifacts == nil {
			return featureImage{}, &packs.PackError{Code: packs.CodeInternal,
				Message: "blog.publish feature_image_artifact_key requires an artifact store"}
		}
		bytes, art, err := ec.Artifacts.Get(ctx, in.FeatureImageArtifactKey)
		if err != nil {
			return featureImage{}, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: fmt.Sprintf("feature_image_artifact_key %q not found: %v", in.FeatureImageArtifactKey, err)}
		}
		ct := art.ContentType
		if ct == "" {
			ct = "image/png"
		}
		return featureImage{
			Bytes:       bytes,
			ArtifactKey: in.FeatureImageArtifactKey,
			ContentType: ct,
		}, nil
	}
	if !in.HeroImage {
		return featureImage{}, nil
	}
	// Auto-generate. Prompt: explicit hero_image_prompt > title.
	prompt := strings.TrimSpace(in.HeroImagePrompt)
	if prompt == "" {
		prompt = in.Title
	}
	model := in.HeroImageModel
	if model == "" {
		model = imageGenDefaultModel
	}
	res, perr := RunImageGen(ctx, ec, v, eg, ImageGenRequest{Prompt: prompt, Model: model})
	if perr != nil {
		return featureImage{}, perr
	}
	bytes, art, err := ec.Artifacts.Get(ctx, res.ArtifactKeys[0])
	if err != nil {
		return featureImage{}, &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("read generated hero image: %v", err), Cause: err}
	}
	ct := art.ContentType
	if ct == "" {
		ct = "image/png"
	}
	return featureImage{
		Bytes:       bytes,
		ArtifactKey: res.ArtifactKeys[0],
		ContentType: ct,
		ModelUsed:   res.ModelUsed,
	}, nil
}

// blogExpandPrompt asks the gateway LLM for a body the agent didn't
// write itself. The system prompt is frozen — it tells the model to
// emit ONLY the body in the requested format, no preamble/explanation.
func blogExpandPrompt(ctx context.Context, d vision.Dispatcher, title, prompt, model string, maxToks int, format string) (string, *packs.PackError) {
	if maxToks <= 0 {
		maxToks = defaultBlogPromptMaxToks
	}
	sys := fmt.Sprintf(
		"You are a blog post author. Emit ONLY the post body in %s format — no preamble, no explanation, no surrounding code fences. The post title is %q; do NOT repeat it as a heading at the top. "+
			"When a concept is genuinely visual — system architecture, request/data flow, sequence interactions, state transitions, decision trees — author it as a fenced ```mermaid``` block (graph/flowchart, sequenceDiagram, stateDiagram, classDiagram, erDiagram, gantt). Prefer prose for everything else. Do not invent diagrams to fill space; one diagram that earns its place beats three that don't.",
		format, title)
	resp, err := d.Dispatch(ctx, gateway.ChatRequest{
		Model:     model,
		MaxTokens: &maxToks,
		Messages: []gateway.Message{
			{Role: "system", Content: gateway.TextContent(sys)},
			{Role: "user", Content: gateway.TextContent(prompt)},
		},
	})
	if err != nil {
		return "", &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("blog.publish prompt expansion: %v", err), Cause: err}
	}
	if len(resp.Choices) == 0 {
		return "", &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: "blog.publish prompt expansion: model returned no choices",
			Cause:   errors.New("empty choices")}
	}
	out := strings.TrimSpace(resp.Choices[0].Message.Content.Text())
	if out == "" {
		return "", &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: "blog.publish prompt expansion: model returned empty text"}
	}
	return out, nil
}

// blogPublishArtifact renders the body in the requested format and
// stores it as a helmdeck artifact. No external network calls; no
// vault. The agent fetches the artifact later via /api/v1/artifacts/<key>.
// When fi.Bytes is non-empty, the feature image is also written as a
// sidecar artifact (and surfaced in the output) so the agent can
// publish or display it separately from the post body.
func blogPublishArtifact(ctx context.Context, ec *packs.ExecutionContext, title, body, format, bodySource, modelUsed string, fi featureImage) (json.RawMessage, error) {
	var (
		ext         string
		contentType string
		payload     []byte
	)
	slug := blogSlugify(title)
	switch format {
	case "markdown":
		ext = "md"
		contentType = "text/markdown; charset=utf-8"
		// Wrap with a tiny frontmatter so downstream tools (Hugo,
		// Docusaurus, Jekyll) recognize the artifact as a post, not
		// a free-form .md.
		var fm strings.Builder
		fm.WriteString("---\n")
		fm.WriteString("title: ")
		fm.WriteString(jsonEscape(title))
		fm.WriteString("\n")
		fm.WriteString("date: ")
		fm.WriteString(time.Now().UTC().Format(time.RFC3339))
		fm.WriteString("\n")
		fm.WriteString("---\n\n")
		fm.WriteString(body)
		payload = []byte(fm.String())
	case "html":
		ext = "html"
		contentType = "text/html; charset=utf-8"
		// Body is already HTML in agent-supplied html mode. In
		// prompt-mode + format=html, the model was told to emit HTML.
		// In agent-supplied markdown body but format=html, render via
		// goldmark — the cross-mode case (agent gave us markdown, asked
		// for an HTML artifact). We detect this by sniffing the body
		// for a leading `<` tag; otherwise treat as markdown.
		if strings.HasPrefix(strings.TrimSpace(body), "<") {
			payload = []byte(body)
		} else {
			html, err := mdToHTML(body)
			if err != nil {
				return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
					Message: fmt.Sprintf("markdown→html conversion: %v", err), Cause: err}
			}
			payload = []byte(html)
		}
	}

	if ec.Artifacts == nil {
		return nil, &packs.PackError{Code: packs.CodeInternal,
			Message: "blog.publish artifact mode requires an artifact store"}
	}
	art, err := ec.Artifacts.Put(ctx, ec.Pack.Name, slug+"."+ext, payload, contentType)
	if err != nil {
		return nil, &packs.PackError{Code: packs.CodeArtifactFailed, Message: err.Error(), Cause: err}
	}
	out := map[string]any{
		"destination":  "artifact",
		"format":       format,
		"body_source":  bodySource,
		"status":       "artifact_saved",
		"artifact_key": art.Key,
		"artifact_url": art.URL,
		"size":         art.Size,
	}
	if modelUsed != "" {
		out["model_used"] = modelUsed
	}
	if len(fi.Bytes) > 0 {
		// Sidecar artifact: write the cover next to the post so the
		// agent can fetch both via /api/v1/artifacts/. Reuse the same
		// pack-name namespace + slug-based filename so the pair is
		// discoverable by prefix.
		coverArt, err := ec.Artifacts.Put(ctx, ec.Pack.Name,
			slug+"-cover."+contentTypeToExt(fi.ContentType),
			fi.Bytes, fi.ContentType)
		if err == nil {
			out["feature_image_artifact_key"] = coverArt.Key
		}
		// If err: not fatal — the post artifact landed; surface the
		// already-resolved key from fi instead.
		if err != nil {
			out["feature_image_artifact_key"] = fi.ArtifactKey
		}
		if fi.ModelUsed != "" {
			out["hero_image_model_used"] = fi.ModelUsed
		}
	}
	return json.Marshal(out)
}

// blogPublishGhost mints a 5-min HS256 JWT from the vault-stored Admin
// API key, optionally renders markdown→html (Ghost takes html/lexical/
// mobiledoc, not markdown), and POSTs to /ghost/api/admin/posts/.
// When fi.Bytes is non-empty, the image is uploaded to Ghost's
// /ghost/api/admin/images/upload/ first and the returned URL goes into
// the post body's feature_image field.
func blogPublishGhost(ctx context.Context, ec *packs.ExecutionContext, eg *security.EgressGuard, v *vault.Store, in blogPublishInput, body, bodySource, modelUsed string, fi featureImage) (json.RawMessage, error) {
	credName := in.Credential
	if credName == "" {
		credName = defaultGhostCred
	}
	if v == nil {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: "blog.publish ghost mode requires a credential vault"}
	}
	res, err := v.ResolveByName(ctx, vault.Actor{Subject: "*"}, credName)
	if err != nil {
		if errors.Is(err, vault.ErrNotFound) || errors.Is(err, vault.ErrNoMatch) {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: fmt.Sprintf("vault credential %q not found (Ghost destination requires the Admin API key under this name)", credName)}
		}
		return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("vault resolve %q: %v", credName, err), Cause: err}
	}
	// Egress guard: strip scheme + port for the host check (CheckHost
	// resolves DNS).
	hostForCheck := in.Host
	hostForCheck = strings.TrimPrefix(hostForCheck, "http://")
	hostForCheck = strings.TrimPrefix(hostForCheck, "https://")
	if i := strings.IndexAny(hostForCheck, "/:"); i >= 0 {
		hostForCheck = hostForCheck[:i]
	}
	if eg != nil {
		if err := eg.CheckHost(ctx, hostForCheck); err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: fmt.Sprintf("egress denied: %v", err), Cause: err}
		}
	}

	tok, perr := mintGhostJWT(string(res.Plaintext))
	if perr != nil {
		return nil, perr
	}

	// Ghost wants `html` (modern path) when we're not using lexical/
	// mobiledoc. If body is markdown, render it first.
	htmlBody := body
	if in.Format == "markdown" {
		out, err := mdToHTML(body)
		if err != nil {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("markdown→html for Ghost: %v", err), Cause: err}
		}
		htmlBody = out
	}

	// Upload feature image to Ghost FIRST so its returned URL can be
	// stamped into the post's feature_image field. Ghost's
	// /images/upload/ accepts multipart form-data with the same JWT.
	featureImageURL := ""
	if len(fi.Bytes) > 0 {
		scheme, hostNoSchema := splitSchemeHost(in.Host)
		imgURL := fmt.Sprintf("%s://%s/ghost/api/admin/images/upload/", scheme, hostNoSchema)
		uploaded, uperr := ghostUploadImage(ctx, imgURL, tok, fi)
		if uperr != nil {
			return nil, uperr
		}
		featureImageURL = uploaded
	}

	postBody := map[string]any{
		"posts": []map[string]any{
			{
				"title":  in.Title,
				"html":   htmlBody,
				"status": coalesce(in.Status, "draft"),
				"tags":   ghostTags(in.Tags),
			},
		},
	}
	if in.PublishedAt != "" {
		postBody["posts"].([]map[string]any)[0]["published_at"] = in.PublishedAt
	}
	if featureImageURL != "" {
		postBody["posts"].([]map[string]any)[0]["feature_image"] = featureImageURL
	}

	// Use ?source=html so Ghost knows to ingest the html field as the
	// authoritative body (vs lexical/mobiledoc). Host can carry an
	// explicit scheme (handy for self-hosted Ghost on a non-HTTPS
	// port, and for tests against httptest servers); default https.
	scheme, host := splitSchemeHost(in.Host)
	url := fmt.Sprintf("%s://%s%s?source=html", scheme, host, ghostAdminPostsPath)
	respRaw, perr := callGhostAPI(ctx, "POST", url, tok, postBody)
	if perr != nil {
		return nil, perr
	}

	// Ghost responds with {posts: [{id, url, status, published_at, ...}]}
	var ghostResp struct {
		Posts []struct {
			ID          string `json:"id"`
			URL         string `json:"url"`
			Status      string `json:"status"`
			PublishedAt string `json:"published_at"`
		} `json:"posts"`
	}
	if err := json.Unmarshal(respRaw, &ghostResp); err != nil || len(ghostResp.Posts) == 0 {
		return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("ghost API: unexpected response shape (raw: %s)", truncateString(string(respRaw), 256))}
	}
	p := ghostResp.Posts[0]
	out := map[string]any{
		"destination":  "ghost",
		"format":       in.Format,
		"body_source":  bodySource,
		"post_id":      p.ID,
		"url":          p.URL,
		"html_url":     p.URL,
		"status":       p.Status,
		"published_at": p.PublishedAt,
	}
	if modelUsed != "" {
		out["model_used"] = modelUsed
	}
	if featureImageURL != "" {
		out["feature_image_url"] = featureImageURL
		out["feature_image_artifact_key"] = fi.ArtifactKey
	}
	if fi.ModelUsed != "" {
		out["hero_image_model_used"] = fi.ModelUsed
	}
	return json.Marshal(out)
}

// splitSchemeHost extracts the scheme + bare hostname from a Ghost
// host input that may carry an explicit scheme. Defaults to https.
// Pulled out so both the post POST and the image upload share the
// same parser.
func splitSchemeHost(host string) (scheme, bare string) {
	scheme = "https"
	bare = host
	if strings.HasPrefix(bare, "http://") {
		scheme = "http"
		bare = strings.TrimPrefix(bare, "http://")
	} else if strings.HasPrefix(bare, "https://") {
		bare = strings.TrimPrefix(bare, "https://")
	}
	return scheme, bare
}

// ghostUploadImage POSTs an image to Ghost's /ghost/api/admin/images/upload/
// endpoint as multipart form-data and returns the hosted URL. Same JWT
// auth as the post POST. The response shape is:
//
//	{"images":[{"url":"https://.../content/images/...","ref":null}]}
//
// On non-2xx the function surfaces the body so operators can debug
// content-type / size limits (Ghost caps at 10MB by default).
func ghostUploadImage(ctx context.Context, url, tok string, fi featureImage) (string, *packs.PackError) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	// Filename matters for content-type sniffing on Ghost's side.
	filename := "feature-image." + contentTypeToExt(fi.ContentType)
	partHdr := textproto.MIMEHeader{}
	partHdr.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, filename))
	partHdr.Set("Content-Type", fi.ContentType)
	part, err := w.CreatePart(partHdr)
	if err != nil {
		return "", &packs.PackError{Code: packs.CodeInternal,
			Message: fmt.Sprintf("compose multipart: %v", err), Cause: err}
	}
	if _, err := part.Write(fi.Bytes); err != nil {
		return "", &packs.PackError{Code: packs.CodeInternal,
			Message: fmt.Sprintf("write image part: %v", err), Cause: err}
	}
	if err := w.Close(); err != nil {
		return "", &packs.PackError{Code: packs.CodeInternal,
			Message: fmt.Sprintf("close multipart: %v", err), Cause: err}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return "", &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("ghost image upload: %v", err), Cause: err}
	}
	req.Header.Set("Authorization", "Ghost "+tok)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("ghost image upload request: %v", err), Cause: err}
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("ghost image upload %d: %s", resp.StatusCode, truncateString(string(respBody), 512))}
	}
	var parsed struct {
		Images []struct {
			URL string `json:"url"`
		} `json:"images"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil || len(parsed.Images) == 0 || parsed.Images[0].URL == "" {
		return "", &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("ghost image upload: unexpected response (raw: %s)", truncateString(string(respBody), 256))}
	}
	return parsed.Images[0].URL, nil
}

// mintGhostJWT splits the Admin API key into <id>:<secret>, hex-decodes
// the secret, and signs a 5-minute HS256 JWT with the id as the kid
// header. Audience is fixed to /admin/ per Ghost's Admin API contract.
func mintGhostJWT(adminKey string) (string, *packs.PackError) {
	parts := strings.SplitN(strings.TrimSpace(adminKey), ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", &packs.PackError{Code: packs.CodeInvalidInput,
			Message: "ghost-admin-key vault value must be `<id>:<secret>` (Ghost Admin API key format)"}
	}
	id, secretHex := parts[0], parts[1]
	secret, err := hex.DecodeString(secretHex)
	if err != nil {
		return "", &packs.PackError{Code: packs.CodeInvalidInput,
			Message: "ghost-admin-key secret must be hex-encoded (Ghost ships them this way)"}
	}
	now := time.Now()
	claims := jwt.MapClaims{
		"iat": now.Unix(),
		"exp": now.Add(ghostJWTLifetime).Unix(),
		"aud": "/admin/",
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	t.Header["kid"] = id
	signed, err := t.SignedString(secret)
	if err != nil {
		return "", &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("ghost JWT mint: %v", err), Cause: err}
	}
	return signed, nil
}

// callGhostAPI is the tight HTTP shape Ghost expects for the Admin API.
// JWT in `Authorization: Ghost <jwt>` (NOT Bearer — Ghost's quirk).
func callGhostAPI(ctx context.Context, method, url, token string, body any) (json.RawMessage, *packs.PackError) {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, &packs.PackError{Code: packs.CodeInternal, Message: err.Error(), Cause: err}
		}
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, &packs.PackError{Code: packs.CodeInternal, Message: err.Error(), Cause: err}
	}
	req.Header.Set("Authorization", "Ghost "+token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", "Helmdeck/0.10.0 (+https://github.com/tosin2013/helmdeck)")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("ghost API %s %s: %v", method, url, err), Cause: err}
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxBlogResponse))
	if err != nil {
		return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("read ghost response: %v", err), Cause: err}
	}
	if resp.StatusCode >= 400 {
		// Ghost error shape: {errors: [{message, type, ...}]}
		var ghErr struct {
			Errors []struct {
				Message string `json:"message"`
				Type    string `json:"type"`
			} `json:"errors"`
		}
		_ = json.Unmarshal(respBody, &ghErr)
		msg := ""
		if len(ghErr.Errors) > 0 {
			msg = ghErr.Errors[0].Message
		}
		if msg == "" {
			msg = truncateString(string(respBody), 256)
		}
		return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("ghost API %s %s: %d %s", method, url, resp.StatusCode, msg)}
	}
	return json.RawMessage(respBody), nil
}

// mdToHTML renders markdown to HTML via goldmark.
// Used by Ghost destination (always — Ghost wants html) and by artifact
// destination when the agent gave us markdown but asked for an html artifact.
//
// Registers the goldmark-mermaid extender in client-render mode so
// ```mermaid fences become <pre class="mermaid">…</pre> blocks and a
// single MermaidJS <script> is injected per document when any mermaid
// block is present. Standalone .html artifacts render in the browser
// directly; Ghost users whose theme sanitises <script> tags need to add
// the MermaidJS loader to their default.hbs (documented in the pack
// reference).
func mdToHTML(md string) (string, error) {
	engine := goldmark.New(
		goldmark.WithExtensions(&mermaid.Extender{
			RenderMode: mermaid.RenderModeClient,
		}),
	)
	var buf bytes.Buffer
	if err := engine.Convert([]byte(md), &buf); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// blogSlugify produces a filesystem-safe slug from a title. Lowercase,
// alnum + hyphens, max 60 chars. Used for the artifact filename so the
// stored artifact's key is human-recognizable.
func blogSlugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevHyphen := true
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevHyphen = false
		case r == ' ' || r == '-' || r == '_':
			if !prevHyphen {
				b.WriteByte('-')
				prevHyphen = true
			}
		}
		if b.Len() >= 60 {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "post"
	}
	return out
}

// ghostTags converts ["a","b"] into Ghost's tag-object format.
func ghostTags(tags []string) []map[string]string {
	if len(tags) == 0 {
		return nil
	}
	out := make([]map[string]string, 0, len(tags))
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		out = append(out, map[string]string{"name": t})
	}
	return out
}

// jsonEscape escapes a string for safe inclusion in YAML/JSON values.
// Cheap replacement — frontmatter values are short, no need for full
// YAML serialization.
func jsonEscape(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func coalesce(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
