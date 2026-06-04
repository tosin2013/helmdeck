// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// helpers_test.go (PR G of the v0.25.0 reliability arc) covers the
// small infrastructure helpers in server.go and jsonrpc.go that
// weren't worth a dedicated file. Each is small but load-bearing —
// `isInlineableImage` gates whether the MCP image content block
// renders inline (a regression here would silently fall back to
// text-only URLs for every screenshot pack); `rpcError.Error()` is
// the string format any logging path uses to surface MCP failures.

// TestIsInlineableImage_KnownMIMETypes pins the closed set of MIME
// types the MCP image content block supports. PNG / JPEG / GIF /
// WebP are inlineable; everything else (SVG, AVIF, BMP, anything
// outside the set) must fall back to text-URL.
func TestIsInlineableImage_KnownMIMETypes(t *testing.T) {
	cases := map[string]bool{
		"image/png":       true,
		"image/jpeg":      true,
		"image/gif":       true,
		"image/webp":      true,
		"image/svg+xml":   false,
		"image/avif":      false,
		"image/bmp":       false,
		"application/pdf": false,
		"text/plain":      false,
		"":                false,
	}
	for mime, want := range cases {
		if got := isInlineableImage(mime); got != want {
			t.Errorf("isInlineableImage(%q) = %v; want %v", mime, got, want)
		}
	}
}

// TestBase64Encode_RoundTrip — the helper is a one-line wrapper but
// pinning it ensures call sites don't drift if the wrapper renames
// (the package keeps one named entry point for grepping image-encode
// call sites).
func TestBase64Encode_RoundTrip(t *testing.T) {
	encoded := base64Encode([]byte("\x89PNG\r\n\x1a\n"))
	if encoded != "iVBORw0KGgo=" {
		t.Errorf("base64Encode = %q; want iVBORw0KGgo=", encoded)
	}
	// Empty input is well-defined.
	if got := base64Encode(nil); got != "" {
		t.Errorf("base64Encode(nil) = %q; want empty", got)
	}
}

// TestRPCError_StringFormat — the rpcError.Error() format is what
// every log line for MCP failures uses; pin the shape so log-parsing
// scripts (and humans grepping the field) don't get blindsided.
func TestRPCError_StringFormat(t *testing.T) {
	e := &rpcError{Code: -32601, Message: "method not found"}
	got := e.Error()
	if !strings.Contains(got, "-32601") {
		t.Errorf("rpcError.Error() should include code: %q", got)
	}
	if !strings.Contains(got, "method not found") {
		t.Errorf("rpcError.Error() should include message: %q", got)
	}
}

// TestExtractWebhookFields_NoFields — input without webhook_url is
// returned unchanged. The webhook surface is opt-in; a pack input
// that doesn't carry webhook metadata MUST pass through verbatim.
func TestExtractWebhookFields_NoFields(t *testing.T) {
	in := json.RawMessage(`{"msg":"hi"}`)
	url, secret, cleaned := extractWebhookFields(in)
	if url != "" || secret != "" {
		t.Errorf("expected no webhook fields; got url=%q secret=%q", url, secret)
	}
	if string(cleaned) != string(in) {
		t.Errorf("cleaned = %q; want passthrough", cleaned)
	}
}

// TestExtractWebhookFields_ExtractAndStrip — the two fields the
// MCP webhook surface advertises are pulled out of the pack input
// BEFORE the input reaches the pack handler. Pin: the handler must
// NEVER see webhook_url or webhook_secret (security boundary —
// these are MCP-server-level metadata, not pack inputs, and a
// regression that leaked them through would let any pack with
// JSON-input access read the webhook secret).
func TestExtractWebhookFields_ExtractAndStrip(t *testing.T) {
	in := json.RawMessage(`{"msg":"hi","webhook_url":"https://hook.example/notify","webhook_secret":"s3cr3t"}`)
	url, secret, cleaned := extractWebhookFields(in)
	if url != "https://hook.example/notify" {
		t.Errorf("url = %q", url)
	}
	if secret != "s3cr3t" {
		t.Errorf("secret = %q", secret)
	}
	// Cleaned input must NOT carry the webhook fields.
	var got map[string]any
	_ = json.Unmarshal(cleaned, &got)
	if _, leaked := got["webhook_url"]; leaked {
		t.Errorf("webhook_url leaked into cleaned input: %s", cleaned)
	}
	if _, leaked := got["webhook_secret"]; leaked {
		t.Errorf("webhook_secret leaked into cleaned input: %s", cleaned)
	}
	if got["msg"] != "hi" {
		t.Errorf("non-webhook fields lost: %s", cleaned)
	}
}

// TestExtractWebhookFields_SecretWithoutURL — when webhook_secret
// is present but webhook_url is not, the function bails out and
// returns the ORIGINAL input unchanged with empty url AND empty
// secret. Webhook is opt-in on the url; without it the entire
// extraction is a no-op so the field round-trips to the handler
// rather than getting silently swallowed by the MCP layer.
func TestExtractWebhookFields_SecretWithoutURL(t *testing.T) {
	in := json.RawMessage(`{"msg":"hi","webhook_secret":"s3cr3t"}`)
	url, secret, cleaned := extractWebhookFields(in)
	if url != "" {
		t.Errorf("url should be empty: %q", url)
	}
	// No url ⇒ extraction bails; both returns are empty and the input
	// passes through unchanged so the secret field reaches the
	// handler rather than getting stripped by MCP-side metadata.
	if secret != "" {
		t.Errorf("secret = %q; want empty (no url ⇒ no extraction)", secret)
	}
	if string(cleaned) != string(in) {
		t.Errorf("cleaned should be the original input when no webhook_url: %q", cleaned)
	}
}

// TestExtractWebhookFields_InvalidJSON — malformed input passes
// through unchanged. The webhook extractor MUST NOT corrupt a pack
// input it can't parse — the handler's own validation reports the
// invalid JSON.
func TestExtractWebhookFields_InvalidJSON(t *testing.T) {
	in := json.RawMessage(`{not-json`)
	url, secret, cleaned := extractWebhookFields(in)
	if url != "" || secret != "" {
		t.Errorf("no fields should extract from invalid JSON; got url=%q secret=%q",
			url, secret)
	}
	if string(cleaned) != string(in) {
		t.Errorf("cleaned should equal original on invalid JSON: %q", cleaned)
	}
}
