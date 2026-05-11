// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/store"
	"github.com/tosin2013/helmdeck/internal/vault"
)

// vaultWithGhostKey constructs an in-memory vault store with one Ghost
// admin-key credential. The key is `id:secret` where secret is hex.
func vaultWithGhostKey(t *testing.T, name, hostPattern, idAndHexSecret string) *vault.Store {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	key := make([]byte, 32)
	v, err := vault.New(db, key)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := v.Create(context.Background(), vault.CreateInput{
		Name:        name,
		Type:        vault.TypeAPIKey,
		HostPattern: hostPattern,
		Plaintext:   []byte(idAndHexSecret),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Grant(context.Background(), rec.ID, vault.Grant{ActorSubject: "*"}); err != nil {
		t.Fatal(err)
	}
	return v
}

// runBlogPublish invokes the handler directly with a hand-built
// ExecutionContext. Pass nil for disp to test body-mode-only paths
// (the helper avoids the typed-nil-interface gotcha by constructing
// the pack with an untyped nil dispatcher when disp == nil).
func runBlogPublish(t *testing.T, v *vault.Store, disp *scriptedDispatcherWT, input string) (json.RawMessage, error) {
	t.Helper()
	var pack *packs.Pack
	if disp == nil {
		pack = BlogPublish(v, nil, nil)
	} else {
		pack = BlogPublish(v, nil, disp)
	}
	ec := &packs.ExecutionContext{
		Pack:      pack,
		Input:     json.RawMessage(input),
		Artifacts: packs.NewMemoryArtifactStore(),
	}
	return pack.Handler(context.Background(), ec)
}

// stubGhost simulates the Ghost Admin API for one POST. Returns the
// httptest server + a captured-request slice the test can assert on.
func stubGhost(t *testing.T, status int, respBody string) (*httptest.Server, *[]ghostCapture) {
	t.Helper()
	captured := []ghostCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured = append(captured, ghostCapture{
			Method: r.Method,
			Path:   r.URL.Path,
			Query:  r.URL.RawQuery,
			Auth:   r.Header.Get("Authorization"),
			Body:   string(body),
		})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respBody))
	}))
	t.Cleanup(srv.Close)
	return srv, &captured
}

type ghostCapture struct {
	Method string
	Path   string
	Query  string
	Auth   string
	Body   string
}

// validGhostKey produces an `<id>:<hexsecret>` test key whose JWT can
// be re-verified inside the test (so we can assert the kid header and
// the claims set match).
func validGhostKey() (id, hexSecret, full string) {
	id = "650000000000000000000001"
	hexSecret = hex.EncodeToString([]byte("super-secret-test-key-bytes-32!!"))
	return id, hexSecret, id + ":" + hexSecret
}

// --- tests ----------------------------------------------------------------

func TestBlogPublish_Artifact_MarkdownBody(t *testing.T) {
	raw, err := runBlogPublish(t, nil, nil, `{
		"destination": "artifact",
		"format":      "markdown",
		"title":       "Test Post",
		"body":        "# Hello\n\nThis is markdown."
	}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		Destination string `json:"destination"`
		Format      string `json:"format"`
		BodySource  string `json:"body_source"`
		ArtifactKey string `json:"artifact_key"`
		Size        int    `json:"size"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Destination != "artifact" || out.Format != "markdown" {
		t.Errorf("unexpected destination/format: %+v", out)
	}
	if out.BodySource != "input" {
		t.Errorf("body_source = %q, want input", out.BodySource)
	}
	if !strings.HasSuffix(out.ArtifactKey, ".md") {
		t.Errorf("artifact_key %q should end in .md", out.ArtifactKey)
	}
	if out.Size <= 0 {
		t.Errorf("size = %d, want > 0", out.Size)
	}
	if !strings.Contains(out.ArtifactKey, "test-post") {
		t.Errorf("artifact_key %q should include the slugified title", out.ArtifactKey)
	}
}

func TestBlogPublish_Artifact_HTMLBody(t *testing.T) {
	raw, err := runBlogPublish(t, nil, nil, `{
		"destination": "artifact",
		"format":      "html",
		"title":       "Pre-rendered HTML",
		"body":        "<h1>Hello</h1><p>This is HTML.</p>"
	}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		ArtifactKey string `json:"artifact_key"`
		Format      string `json:"format"`
	}
	_ = json.Unmarshal(raw, &out)
	if !strings.HasSuffix(out.ArtifactKey, ".html") {
		t.Errorf("artifact_key %q should end in .html", out.ArtifactKey)
	}
	if out.Format != "html" {
		t.Errorf("format = %q, want html", out.Format)
	}
}

func TestBlogPublish_Artifact_MarkdownBodyToHTMLArtifact(t *testing.T) {
	// Cross-mode: agent passed markdown body but asked for an HTML
	// artifact. Pack should goldmark-convert before storing.
	raw, err := runBlogPublish(t, nil, nil, `{
		"destination": "artifact",
		"format":      "html",
		"title":       "Cross mode",
		"body":        "# Hello\n\nMarkdown body, html artifact."
	}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		ArtifactKey string `json:"artifact_key"`
	}
	_ = json.Unmarshal(raw, &out)
	if !strings.HasSuffix(out.ArtifactKey, ".html") {
		t.Errorf("artifact_key %q should end in .html", out.ArtifactKey)
	}
}

func TestBlogPublish_Artifact_MermaidFenceRendersInHTML(t *testing.T) {
	// Markdown body with a ```mermaid block, html artifact output. The
	// goldmark mermaid extender should convert the fence into a
	// <pre class="mermaid">…</pre> block (NOT a plain code block) and
	// inject a single MermaidJS <script> tag at end of document.
	pack := BlogPublish(nil, nil, nil)
	ec := &packs.ExecutionContext{
		Pack: pack,
		Input: json.RawMessage(`{
			"destination": "artifact",
			"format":      "html",
			"title":       "Architecture overview",
			"body":        "# Diagram\n\n` + "```mermaid" + `\ngraph TD; A-->B; B-->C;\n` + "```" + `\n\nSome prose after."
		}`),
		Artifacts: packs.NewMemoryArtifactStore(),
	}
	raw, err := pack.Handler(context.Background(), ec)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		ArtifactKey string `json:"artifact_key"`
	}
	_ = json.Unmarshal(raw, &out)
	bytes, _, err := ec.Artifacts.Get(context.Background(), out.ArtifactKey)
	if err != nil {
		t.Fatalf("artifact get: %v", err)
	}
	html := string(bytes)
	if !strings.Contains(html, `<pre class="mermaid">`) {
		t.Errorf("expected <pre class=\"mermaid\"> in rendered html; got:\n%s", html)
	}
	if strings.Contains(html, `<code class="language-mermaid">`) {
		t.Errorf("mermaid fence should NOT render as <code class=\"language-mermaid\">; got:\n%s", html)
	}
	if !strings.Contains(html, "graph TD; A--&gt;B") && !strings.Contains(html, "graph TD; A-->B") {
		t.Errorf("mermaid source content missing from rendered html:\n%s", html)
	}
	if !strings.Contains(html, "mermaid") || !strings.Contains(html, "<script") {
		t.Errorf("expected a <script> tag (MermaidJS loader) when mermaid blocks present; got:\n%s", html)
	}
}

func TestBlogPublish_Artifact_MarkdownFormatPreservesMermaidFence(t *testing.T) {
	// Markdown body with a ```mermaid block, markdown artifact output.
	// Fence MUST pass through verbatim so downstream Markdown renderers
	// (Docusaurus, GitHub) can render it themselves.
	pack := BlogPublish(nil, nil, nil)
	ec := &packs.ExecutionContext{
		Pack: pack,
		Input: json.RawMessage(`{
			"destination": "artifact",
			"format":      "markdown",
			"title":       "Architecture overview",
			"body":        "# Diagram\n\n` + "```mermaid" + `\ngraph TD; A-->B;\n` + "```" + `"
		}`),
		Artifacts: packs.NewMemoryArtifactStore(),
	}
	raw, err := pack.Handler(context.Background(), ec)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		ArtifactKey string `json:"artifact_key"`
	}
	_ = json.Unmarshal(raw, &out)
	bytes, _, err := ec.Artifacts.Get(context.Background(), out.ArtifactKey)
	if err != nil {
		t.Fatalf("artifact get: %v", err)
	}
	md := string(bytes)
	if !strings.Contains(md, "```mermaid") {
		t.Errorf("mermaid fence should pass through verbatim in markdown artifact; got:\n%s", md)
	}
	if !strings.Contains(md, "graph TD; A-->B;") {
		t.Errorf("mermaid source should pass through verbatim; got:\n%s", md)
	}
}

func TestBlogPublish_Ghost_HappyPath_MarkdownBody(t *testing.T) {
	srv, captured := stubGhost(t, 201, `{
		"posts": [{
			"id":           "post-id-123",
			"url":          "https://blog.example/p/test-post/",
			"status":       "draft",
			"published_at": null
		}]
	}`)
	_, _, key := validGhostKey()
	v := vaultWithGhostKey(t, "ghost-admin-key", "blog.example", key)

	host := strings.TrimPrefix(srv.URL, "http://") // pack accepts http://host
	raw, err := runBlogPublish(t, v, nil, `{
		"destination": "ghost",
		"format":      "markdown",
		"title":       "Test Post",
		"body":        "# Heading\n\nBody **paragraph**.",
		"host":        "http://`+host+`",
		"tags":        ["demo","blog-publish"],
		"status":      "draft"
	}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}

	var out struct {
		Destination string `json:"destination"`
		Format      string `json:"format"`
		BodySource  string `json:"body_source"`
		PostID      string `json:"post_id"`
		HTMLURL     string `json:"html_url"`
		Status      string `json:"status"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.PostID != "post-id-123" || out.HTMLURL != "https://blog.example/p/test-post/" || out.Status != "draft" {
		t.Errorf("unexpected ghost output: %+v", out)
	}
	if out.BodySource != "input" {
		t.Errorf("body_source = %q, want input", out.BodySource)
	}

	// Inspect what the pack actually sent to Ghost.
	if len(*captured) != 1 {
		t.Fatalf("expected 1 ghost call, got %d", len(*captured))
	}
	c := (*captured)[0]
	if c.Method != "POST" {
		t.Errorf("method = %q", c.Method)
	}
	if c.Path != ghostAdminPostsPath {
		t.Errorf("path = %q, want %q", c.Path, ghostAdminPostsPath)
	}
	if c.Query != "source=html" {
		t.Errorf("query = %q, want source=html", c.Query)
	}
	if !strings.HasPrefix(c.Auth, "Ghost ") {
		t.Errorf("auth header = %q, expected Ghost prefix", c.Auth)
	}
	// Ghost wants HTML, so the markdown body must have been rendered.
	var sent struct {
		Posts []struct {
			Title  string                  `json:"title"`
			HTML   string                  `json:"html"`
			Status string                  `json:"status"`
			Tags   []map[string]string     `json:"tags"`
		} `json:"posts"`
	}
	if err := json.Unmarshal([]byte(c.Body), &sent); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sent.Posts[0].HTML, "<h1>Heading</h1>") {
		t.Errorf("expected rendered html, got: %q", sent.Posts[0].HTML)
	}
	if sent.Posts[0].Status != "draft" {
		t.Errorf("status sent = %q", sent.Posts[0].Status)
	}
	if len(sent.Posts[0].Tags) != 2 {
		t.Errorf("tags sent = %+v", sent.Posts[0].Tags)
	}

	// JWT validity check — re-verify with the same secret.
	tokString := strings.TrimPrefix(c.Auth, "Ghost ")
	_, hexSecret, _ := validGhostKey()
	secretBytes, _ := hex.DecodeString(hexSecret)
	parsed, err := jwt.Parse(tokString, func(t *jwt.Token) (interface{}, error) {
		return secretBytes, nil
	})
	if err != nil {
		t.Fatalf("jwt parse: %v", err)
	}
	if parsed.Header["kid"] != "650000000000000000000001" {
		t.Errorf("kid header = %v, want test id", parsed.Header["kid"])
	}
	claims, _ := parsed.Claims.(jwt.MapClaims)
	if claims["aud"] != "/admin/" {
		t.Errorf("aud claim = %v, want /admin/", claims["aud"])
	}
}

func TestBlogPublish_Ghost_PromptMode(t *testing.T) {
	srv, _ := stubGhost(t, 201, `{"posts":[{"id":"p2","url":"https://b.example/p/x","status":"published","published_at":"2026-05-09T00:00:00.000Z"}]}`)
	_, _, key := validGhostKey()
	v := vaultWithGhostKey(t, "ghost-admin-key", "b.example", key)
	disp := &scriptedDispatcherWT{replies: []string{
		"# Generated post\n\nThis body came from the LLM, not the agent.",
	}}
	host := strings.TrimPrefix(srv.URL, "http://")
	raw, err := runBlogPublish(t, v, disp, `{
		"destination": "ghost",
		"format":      "markdown",
		"title":       "LLM-Generated",
		"prompt":      "Write a short post about Go testing.",
		"model":       "openai/gpt-4o-mini",
		"host":        "http://`+host+`",
		"status":      "published"
	}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		BodySource string `json:"body_source"`
		ModelUsed  string `json:"model_used"`
		PostID     string `json:"post_id"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.BodySource != "model" {
		t.Errorf("body_source = %q, want model", out.BodySource)
	}
	if out.ModelUsed != "openai/gpt-4o-mini" {
		t.Errorf("model_used = %q", out.ModelUsed)
	}
	if out.PostID != "p2" {
		t.Errorf("post_id = %q", out.PostID)
	}
	if disp.calls != 1 {
		t.Errorf("dispatcher calls = %d, want 1", disp.calls)
	}
}

func TestBlogPublish_Validation_BothBodyAndPrompt(t *testing.T) {
	_, err := runBlogPublish(t, nil, nil, `{
		"destination": "artifact",
		"format":      "markdown",
		"title":       "x",
		"body":        "...",
		"prompt":      "..."
	}`)
	if err == nil || !strings.Contains(err.Error(), "either body OR prompt") {
		t.Fatalf("expected dual-mode error, got %v", err)
	}
	if pe, ok := err.(*packs.PackError); !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got %T %v", err, err)
	}
}

func TestBlogPublish_Validation_NeitherBodyNorPrompt(t *testing.T) {
	_, err := runBlogPublish(t, nil, nil, `{
		"destination": "artifact",
		"format":      "markdown",
		"title":       "x"
	}`)
	if err == nil || !strings.Contains(err.Error(), "must provide either body OR prompt") {
		t.Fatalf("expected missing-body error, got %v", err)
	}
}

func TestBlogPublish_Validation_ScheduledNeedsPublishedAt(t *testing.T) {
	_, err := runBlogPublish(t, nil, nil, `{
		"destination": "artifact",
		"format":      "markdown",
		"title":       "x",
		"body":        "...",
		"status":      "scheduled"
	}`)
	if err == nil || !strings.Contains(err.Error(), "published_at") {
		t.Fatalf("expected scheduled-needs-published_at error, got %v", err)
	}
}

func TestBlogPublish_Validation_BadDestination(t *testing.T) {
	_, err := runBlogPublish(t, nil, nil, `{"destination":"medium","format":"markdown","title":"x","body":"y"}`)
	if err == nil || !strings.Contains(err.Error(), "destination must be") {
		t.Fatalf("expected destination error, got %v", err)
	}
}

func TestBlogPublish_PromptMode_NoDispatcher(t *testing.T) {
	// nil dispatcher, prompt mode → CodeInternal at handler time.
	_, err := runBlogPublish(t, nil, nil, `{
		"destination": "artifact",
		"format":      "markdown",
		"title":       "x",
		"prompt":      "write something",
		"model":       "openai/gpt-4o-mini"
	}`)
	if err == nil {
		t.Fatal("expected error for prompt mode without dispatcher")
	}
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInternal {
		t.Fatalf("expected CodeInternal, got %T %v", err, err)
	}
}

func TestBlogPublish_Ghost_NoCredentialInVault(t *testing.T) {
	_, _, _ = validGhostKey()
	// Vault exists but doesn't have ghost-admin-key.
	v := vaultWithGhostKey(t, "other-key", "b.example", "id:abcd1234")
	_, err := runBlogPublish(t, v, nil, `{
		"destination": "ghost",
		"format":      "markdown",
		"title":       "x",
		"body":        "y",
		"host":        "blog.example"
	}`)
	if err == nil || !strings.Contains(err.Error(), "ghost-admin-key") {
		t.Fatalf("expected vault credential error, got %v", err)
	}
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got %T %v", err, err)
	}
}

func TestBlogPublish_Ghost_API401(t *testing.T) {
	srv, _ := stubGhost(t, 401, `{"errors":[{"message":"Authorization failed","type":"NoPermissionError"}]}`)
	_, _, key := validGhostKey()
	v := vaultWithGhostKey(t, "ghost-admin-key", "b.example", key)
	host := strings.TrimPrefix(srv.URL, "http://")
	_, err := runBlogPublish(t, v, nil, `{
		"destination": "ghost",
		"format":      "markdown",
		"title":       "x",
		"body":        "y",
		"host":        "http://`+host+`"
	}`)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected 401 surfaced, got %v", err)
	}
	if !strings.Contains(err.Error(), "Authorization failed") {
		t.Errorf("expected ghost message in error, got %v", err)
	}
}

func TestBlogPublish_Ghost_BadJWTKeyFormat(t *testing.T) {
	v := vaultWithGhostKey(t, "ghost-admin-key", "b.example", "no-colon-here")
	_, err := runBlogPublish(t, v, nil, `{
		"destination": "ghost",
		"format":      "markdown",
		"title":       "x",
		"body":        "y",
		"host":        "blog.example"
	}`)
	if err == nil || !strings.Contains(err.Error(), "id>:<secret") {
		t.Fatalf("expected bad-key-format error, got %v", err)
	}
}

// Bench-y check: the JWT we mint actually verifies with the secret.
// Catches sign-with-wrong-bytes regressions (e.g. forgetting to hex-decode).
func TestBlogPublish_JWTRoundtrip(t *testing.T) {
	id, hexSecret, full := validGhostKey()
	tok, perr := mintGhostJWT(full)
	if perr != nil {
		t.Fatalf("mint: %v", perr)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("expected 3-part JWT, got %d", len(parts))
	}
	secretBytes, _ := hex.DecodeString(hexSecret)
	signingInput := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, secretBytes)
	mac.Write([]byte(signingInput))
	expectedSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if parts[2] != expectedSig {
		t.Errorf("signature mismatch: got %q want %q", parts[2], expectedSig)
	}
	parsed, err := jwt.Parse(tok, func(t *jwt.Token) (interface{}, error) {
		return secretBytes, nil
	})
	if err != nil {
		t.Fatalf("jwt parse: %v", err)
	}
	if parsed.Header["kid"] != id {
		t.Errorf("kid = %v, want %v", parsed.Header["kid"], id)
	}
}
