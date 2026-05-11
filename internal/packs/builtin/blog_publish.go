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
	"net/http"
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
		Description: "Publish a post to a Ghost blog or render markdown/HTML to the artifact store. Body or prompt+model. Markdown bodies may include ```mermaid``` fenced blocks — diagrams render client-side in HTML/Ghost outputs (Ghost theme must include MermaidJS).",
		InputSchema: packs.BasicSchema{
			Required: []string{"destination", "format", "title"},
			Properties: map[string]string{
				"destination":  "string", // "ghost" | "artifact"
				"format":       "string", // "markdown" | "html"
				"title":        "string",
				"body":         "string",
				"prompt":       "string",
				"model":        "string",
				"max_tokens":   "number",
				"tags":         "array",
				"status":       "string",
				"published_at": "string",
				"host":         "string",
				"credential":   "string",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"destination", "format", "body_source"},
			Properties: map[string]string{
				"destination":   "string",
				"format":        "string",
				"body_source":   "string",
				"model_used":    "string",
				"post_id":       "string",
				"url":           "string",
				"html_url":      "string",
				"status":        "string",
				"published_at":  "string",
				"artifact_key":  "string",
				"size":          "number",
			},
		},
		Handler: blogPublishHandler(v, eg, d),
	}
}

type blogPublishInput struct {
	Destination string   `json:"destination"`
	Format      string   `json:"format"`
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
}

func blogPublishHandler(v *vault.Store, eg *security.EgressGuard, d vision.Dispatcher) packs.HandlerFunc {
	return func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
		var in blogPublishInput
		if err := json.Unmarshal(ec.Input, &in); err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
		}

		// Schema validation — closed sets and exactly-one-mode rules.
		if in.Destination != "ghost" && in.Destination != "artifact" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: `destination must be "ghost" or "artifact"`}
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

		switch in.Destination {
		case "artifact":
			return blogPublishArtifact(ctx, ec, in.Title, body, in.Format, bodySource, modelUsed)
		case "ghost":
			return blogPublishGhost(ctx, eg, v, in, body, bodySource, modelUsed)
		}
		return nil, &packs.PackError{Code: packs.CodeInternal, Message: "unreachable destination"}
	}
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
func blogPublishArtifact(ctx context.Context, ec *packs.ExecutionContext, title, body, format, bodySource, modelUsed string) (json.RawMessage, error) {
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
		"artifact_key": art.Key,
		"size":         art.Size,
	}
	if modelUsed != "" {
		out["model_used"] = modelUsed
	}
	return json.Marshal(out)
}

// blogPublishGhost mints a 5-min HS256 JWT from the vault-stored Admin
// API key, optionally renders markdown→html (Ghost takes html/lexical/
// mobiledoc, not markdown), and POSTs to /ghost/api/admin/posts/.
func blogPublishGhost(ctx context.Context, eg *security.EgressGuard, v *vault.Store, in blogPublishInput, body, bodySource, modelUsed string) (json.RawMessage, error) {
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

	// Use ?source=html so Ghost knows to ingest the html field as the
	// authoritative body (vs lexical/mobiledoc). Host can carry an
	// explicit scheme (handy for self-hosted Ghost on a non-HTTPS
	// port, and for tests against httptest servers); default https.
	scheme := "https"
	host := in.Host
	if strings.HasPrefix(host, "http://") {
		scheme = "http"
		host = strings.TrimPrefix(host, "http://")
	} else if strings.HasPrefix(host, "https://") {
		host = strings.TrimPrefix(host, "https://")
	}
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
	return json.Marshal(out)
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
