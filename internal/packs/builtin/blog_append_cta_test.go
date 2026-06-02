// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
)

// runBlogAppendCTA drives the pack through its handler with a scripted
// dispatcher (no real LLM). Mirrors runBlogRewrite.
func runBlogAppendCTA(t *testing.T, disp *scriptedDispatcherWT, input string) (json.RawMessage, error) {
	t.Helper()
	pack := BlogAppendCTA(disp)
	ec := &packs.ExecutionContext{Pack: pack, Input: json.RawMessage(input)}
	return pack.Handler(context.Background(), ec)
}

// TestBlogAppendCTA_NoOpWhenNoLinks — the design contract. With no
// source_url / project_url / github_url, the pack must return the
// markdown unchanged AND must not call the dispatcher. This is what
// lets the step slot into every pipeline unconditionally without
// burning a model call for the common no-CTA path.
func TestBlogAppendCTA_NoOpWhenNoLinks(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{"should NOT be called"}}
	raw, err := runBlogAppendCTA(t, disp, `{"markdown":"# Real post\n\nSome body."}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		Markdown string `json:"markdown"`
		CTAAdded bool   `json:"cta_added"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Markdown != "# Real post\n\nSome body." {
		t.Errorf("markdown should be returned verbatim; got %q", out.Markdown)
	}
	if out.CTAAdded {
		t.Errorf("cta_added should be false on no-op path")
	}
	if len(disp.captured) != 0 {
		t.Errorf("dispatcher must NOT be called on no-op path; got %d dispatches", len(disp.captured))
	}
}

// TestBlogAppendCTA_NoOpWhenWhitespaceLinks — empty-string and
// whitespace-only link inputs both count as "unset" so the no-op
// path fires. Caller-side templating that resolves an unfilled
// `${{ inputs.project_url }}` to "" must not trigger the LLM call.
func TestBlogAppendCTA_NoOpWhenWhitespaceLinks(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{"should NOT be called"}}
	raw, err := runBlogAppendCTA(t, disp, `{
		"markdown":"# Post\n\nBody.",
		"source_url":"   ",
		"project_url":"",
		"github_url":"\n"
	}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		CTAAdded bool `json:"cta_added"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.CTAAdded {
		t.Errorf("whitespace-only links should still trigger the no-op path")
	}
	if len(disp.captured) != 0 {
		t.Errorf("dispatcher must NOT be called when only whitespace links given; got %d dispatches", len(disp.captured))
	}
}

// TestBlogAppendCTA_AppendsWhenProjectURLSet — happy path. With a
// project URL set the dispatcher is called, the returned section is
// appended to the original article (which is preserved verbatim),
// and the model prompt includes the URL.
func TestBlogAppendCTA_AppendsWhenProjectURLSet(t *testing.T) {
	cta := "## Learn more\n\nVisit [the project page](https://tosin2013.github.io/openshift-agent-install/) for hands-on guides."
	disp := &scriptedDispatcherWT{replies: []string{cta}}
	const articleEsc = `# Real post\n\nThis is the original body.`
	raw, err := runBlogAppendCTA(t, disp, `{
		"markdown":"`+articleEsc+`",
		"project_url":"https://tosin2013.github.io/openshift-agent-install/",
		"model":"openrouter/auto"
	}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		Markdown  string `json:"markdown"`
		CTAAdded  bool   `json:"cta_added"`
		ModelUsed string `json:"model_used"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.CTAAdded {
		t.Errorf("cta_added should be true when a link is set")
	}
	if out.ModelUsed != "openrouter/auto" {
		t.Errorf("model_used should echo input; got %q", out.ModelUsed)
	}
	article := "# Real post\n\nThis is the original body."
	if !strings.HasPrefix(out.Markdown, article) {
		t.Errorf("original article body should be preserved verbatim at the start; got %q", out.Markdown)
	}
	if !strings.Contains(out.Markdown, "## Learn more") {
		t.Errorf("CTA section should be appended; got %q", out.Markdown)
	}
	// The system prompt must carry the URL so the model knows what
	// to promote. Failing this turns the pack into a generic
	// add-some-CTA — defeating the point.
	if len(disp.captured) != 1 {
		t.Fatalf("expected 1 dispatch; got %d", len(disp.captured))
	}
	sys := disp.captured[0].Messages[0].Content.Text()
	if !strings.Contains(sys, "https://tosin2013.github.io/openshift-agent-install/") {
		t.Errorf("system prompt missing project URL: %s", sys)
	}
	if !strings.Contains(sys, "Project page:") {
		t.Errorf("system prompt should label project URL: %s", sys)
	}
}

// TestBlogAppendCTA_AllThreeLinksLandInPrompt — when source_url +
// project_url + github_url are all set, each lands in the model
// prompt under its own label. The model can then decide how to weave
// them into the section.
func TestBlogAppendCTA_AllThreeLinksLandInPrompt(t *testing.T) {
	cta := "## Get involved\n\nSee the source at the original link, browse [the repo](https://github.com/tosin2013/openshift-agent-install), and visit [the project page](https://tosin2013.github.io/openshift-agent-install/)."
	disp := &scriptedDispatcherWT{replies: []string{cta}}
	raw, err := runBlogAppendCTA(t, disp, `{
		"markdown":"# Article body.",
		"source_url":"https://example.com/source",
		"project_url":"https://tosin2013.github.io/openshift-agent-install/",
		"github_url":"https://github.com/tosin2013/openshift-agent-install",
		"model":"openrouter/auto"
	}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		CTAAdded bool `json:"cta_added"`
	}
	_ = json.Unmarshal(raw, &out)
	if !out.CTAAdded {
		t.Errorf("cta_added should be true")
	}
	sys := disp.captured[0].Messages[0].Content.Text()
	for _, url := range []string{
		"https://example.com/source",
		"https://tosin2013.github.io/openshift-agent-install/",
		"https://github.com/tosin2013/openshift-agent-install",
	} {
		if !strings.Contains(sys, url) {
			t.Errorf("system prompt missing %s", url)
		}
	}
	for _, label := range []string{"Project page:", "GitHub repository:", "Original source:"} {
		if !strings.Contains(sys, label) {
			t.Errorf("system prompt missing label %q", label)
		}
	}
}

// TestBlogAppendCTA_RequiresModelWhenLinkSet — opening the no-op
// escape hatch only when ALL links are empty means the LLM path
// is unavoidable once any link is set; the model field is required
// there.
func TestBlogAppendCTA_RequiresModelWhenLinkSet(t *testing.T) {
	disp := &scriptedDispatcherWT{}
	_, err := runBlogAppendCTA(t, disp, `{"markdown":"x","project_url":"https://example.com"}`)
	if err == nil {
		t.Fatalf("expected error when model is missing alongside a link")
	}
	perr, ok := err.(*packs.PackError)
	if !ok || perr.Code != packs.CodeInvalidInput {
		t.Errorf("expected CodeInvalidInput; got %#v", err)
	}
	if !strings.Contains(perr.Message, "model is required") {
		t.Errorf("error message should mention model: %q", perr.Message)
	}
}

// TestBlogAppendCTA_PersonaMatchesArticleVoice — persona threads
// through to the system prompt so the CTA voice matches the article's
// voice (the pipeline can pass the same persona blog.rewrite_for_audience
// used). The "technical" persona's directive is from the closed set
// shared with blog.rewrite — locks the vocabulary parity.
func TestBlogAppendCTA_PersonaMatchesArticleVoice(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{"## Try it yourself\n\nSomething."}}
	raw, err := runBlogAppendCTA(t, disp, `{
		"markdown":"# Post.",
		"project_url":"https://example.com",
		"persona":"technical",
		"model":"openrouter/auto"
	}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		PersonaUsed string `json:"persona_used"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.PersonaUsed != "technical" {
		t.Errorf("persona_used should be the resolved key; got %q", out.PersonaUsed)
	}
	sys := disp.captured[0].Messages[0].Content.Text()
	// "precise, hands-on" is the leading phrase of the technical
	// persona directive in blog_rewrite_for_audience.go — confirms
	// the shared helper is being called.
	if !strings.Contains(sys, "precise, hands-on") {
		t.Errorf("technical persona directive missing from system prompt: %s", sys)
	}
}

// TestBlogAppendCTA_UnwrapsCodeFence — defensive: weak models
// sometimes wrap their entire output in a ```markdown fence. The
// existing unwrapCodeFence helper handles this for every LLM-backed
// pack; this test pins that blog.append_cta honors it too.
func TestBlogAppendCTA_UnwrapsCodeFence(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{"```markdown\n## Learn more\n\nSomething.\n```"}}
	raw, err := runBlogAppendCTA(t, disp, `{
		"markdown":"# Post.",
		"project_url":"https://example.com",
		"model":"openrouter/auto"
	}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		Markdown string `json:"markdown"`
	}
	_ = json.Unmarshal(raw, &out)
	if strings.Contains(out.Markdown, "```") {
		t.Errorf("code fence not stripped: %q", out.Markdown)
	}
}

// TestBlogAppendCTA_EmptyMarkdownRejected — required-input guard.
func TestBlogAppendCTA_EmptyMarkdownRejected(t *testing.T) {
	disp := &scriptedDispatcherWT{}
	_, err := runBlogAppendCTA(t, disp, `{"markdown":"   "}`)
	if err == nil {
		t.Fatalf("expected error for empty markdown")
	}
	perr, ok := err.(*packs.PackError)
	if !ok || perr.Code != packs.CodeInvalidInput {
		t.Errorf("expected CodeInvalidInput; got %#v", err)
	}
}

// TestBlogAppendCTA_EmptyModelResponseSurfacesError — when the
// dispatcher returns an empty completion the pack must surface a
// handler-failed error rather than silently appending nothing.
func TestBlogAppendCTA_EmptyModelResponseSurfacesError(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{""}}
	_, err := runBlogAppendCTA(t, disp, `{
		"markdown":"# Post.",
		"project_url":"https://example.com",
		"model":"openrouter/auto"
	}`)
	if err == nil {
		t.Fatalf("expected error on empty model response")
	}
	perr, ok := err.(*packs.PackError)
	if !ok || perr.Code != packs.CodeHandlerFailed {
		t.Errorf("expected CodeHandlerFailed; got %#v", err)
	}
}
