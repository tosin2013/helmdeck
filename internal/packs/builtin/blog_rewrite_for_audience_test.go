// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
)

// runBlogRewrite drives the pack through its handler with a scripted dispatcher
// (no real LLM). Mirrors runSlidesOutline.
func runBlogRewrite(t *testing.T, disp *scriptedDispatcherWT, input string) (json.RawMessage, error) {
	t.Helper()
	pack := BlogRewriteForAudience(disp)
	ec := &packs.ExecutionContext{Pack: pack, Input: json.RawMessage(input)}
	return pack.Handler(context.Background(), ec)
}

func TestBlogRewrite_HappyPath(t *testing.T) {
	reply := `Source: Vaswani et al., 2017.

Why this matters to you: most of the tools you reach for…

## The model rewrite goes here…

## Author's note
Building agents today, the lesson…`
	disp := &scriptedDispatcherWT{replies: []string{reply}}
	raw, err := runBlogRewrite(t, disp, `{
		"source_content":"# Attention Is All You Need\n\nAbstract: …",
		"audience":"developers building AI agents",
		"angle":"connect to practical tool-calling patterns",
		"model":"openrouter/auto"
	}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct{ Markdown, Model string }
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(out.Markdown, "Author's note") {
		t.Errorf("rewrite output not propagated: %q", out.Markdown)
	}
	if out.Model != "openrouter/auto" {
		t.Errorf("model not echoed: %q", out.Model)
	}
	// System prompt must carry the audience + angle (so the model sees
	// what it's writing for). Failing this turns the pack into a generic
	// rewrite — defeating the point.
	sys := disp.captured[0].Messages[0].Content.Text()
	for _, must := range []string{"developers building AI agents", "tool-calling patterns", "DE-JARGON", "Author's note", "STAY GROUNDED"} {
		if !strings.Contains(sys, must) {
			t.Errorf("system prompt missing %q: %s", must, sys)
		}
	}
}

func TestBlogRewrite_UnwrapsCodeFence(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{"```markdown\n# Real post\n\nbody…\n```"}}
	raw, err := runBlogRewrite(t, disp, `{"source_content":"x","audience":"devs","model":"openrouter/auto"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct{ Markdown string }
	_ = json.Unmarshal(raw, &out)
	if strings.Contains(out.Markdown, "```") {
		t.Errorf("code fence not stripped: %q", out.Markdown)
	}
}

func TestBlogRewrite_DefaultAngleWhenOmitted(t *testing.T) {
	// Don't pass angle; pack should fill a neutral default so the
	// system prompt's Author's-note rule has something to write about.
	disp := &scriptedDispatcherWT{replies: []string{"# Post\n\nbody"}}
	_, err := runBlogRewrite(t, disp, `{"source_content":"x","audience":"devs","model":"openrouter/auto"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	sys := disp.captured[0].Messages[0].Content.Text()
	if !strings.Contains(sys, "your honest personal perspective") {
		t.Errorf("missing default angle in system prompt: %s", sys)
	}
}

func TestBlogRewrite_RequiredFields(t *testing.T) {
	for _, tc := range []struct{ name, input string }{
		{"no source_content", `{"audience":"x","model":"m"}`},
		{"no audience", `{"source_content":"x","model":"m"}`},
		{"no model", `{"source_content":"x","audience":"a"}`},
		{"empty source_content", `{"source_content":"   ","audience":"a","model":"m"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runBlogRewrite(t, &scriptedDispatcherWT{}, tc.input)
			pe := &packs.PackError{}
			if !errors.As(err, &pe) || pe.Code != packs.CodeInvalidInput {
				t.Errorf("want invalid_input, got %v", err)
			}
		})
	}
}

func TestBlogRewrite_RegisteredWithoutDispatcher(t *testing.T) {
	// Match the existing convention: registering without a dispatcher
	// yields an internal error at call time, not at boot. This is what
	// gateway-less deployments see if they call the pack.
	pack := BlogRewriteForAudience(nil)
	_, err := pack.Handler(context.Background(), &packs.ExecutionContext{
		Pack:  pack,
		Input: json.RawMessage(`{"source_content":"x","audience":"a","model":"m"}`),
	})
	pe := &packs.PackError{}
	if !errors.As(err, &pe) || pe.Code != packs.CodeInternal {
		t.Errorf("want CodeInternal when dispatcher is nil, got %v", err)
	}
}

func TestBlogRewrite_EmptyResponse(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{"   "}}
	_, err := runBlogRewrite(t, disp, `{"source_content":"x","audience":"a","model":"m"}`)
	pe := &packs.PackError{}
	if !errors.As(err, &pe) || pe.Code != packs.CodeHandlerFailed {
		t.Errorf("want handler_failed on empty response, got %v", err)
	}
}
