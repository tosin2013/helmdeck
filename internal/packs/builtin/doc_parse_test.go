// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/security"
)

// stubDocling spins up an httptest server that mimics
// /v1/convert/source. The handler func lets each test craft its
// own response without duplicating the JSON boilerplate.
func stubDocling(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/convert/source", handler)
	return httptest.NewServer(mux)
}

func enableDocling(t *testing.T, url string) {
	t.Helper()
	t.Setenv("HELMDECK_DOCLING_ENABLED", "true")
	t.Setenv("HELMDECK_DOCLING_URL", url)
}

func TestDocParse_HTTPSourceHappyPath(t *testing.T) {
	srv := stubDocling(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		// Assert the new discriminated `sources` shape — a single
		// array of {kind, …} entries instead of the old http_sources
		// / file_sources arrays. Docling now rejects the old shape
		// with HTTP 422 "missing body.sources".
		var raw struct {
			Options doclingOptions    `json:"options"`
			Sources []json.RawMessage `json:"sources"`
			// Old field names — if any of these leaked back in, the
			// new Docling rejects the request. Fail loud.
			HTTPSources json.RawMessage `json:"http_sources"`
			FileSources json.RawMessage `json:"file_sources"`
		}
		if err := json.Unmarshal(body, &raw); err != nil {
			t.Fatalf("bad request: %v", err)
		}
		if len(raw.HTTPSources) > 0 || len(raw.FileSources) > 0 {
			t.Errorf("legacy http_sources/file_sources fields must not be sent: %s", body)
		}
		if len(raw.Sources) != 1 {
			t.Fatalf("expected exactly 1 entry in sources, got %d: %s", len(raw.Sources), body)
		}
		var src doclingHTTPSource
		if err := json.Unmarshal(raw.Sources[0], &src); err != nil {
			t.Fatalf("source[0] not the http shape: %v (raw=%s)", err, raw.Sources[0])
		}
		if src.Kind != "http" {
			t.Errorf("source[0].kind = %q, want \"http\"", src.Kind)
		}
		if src.URL != "https://example.com/paper.pdf" {
			t.Errorf("source[0].url not forwarded: %q", src.URL)
		}
		// Default options — md format, ocr on
		if len(raw.Options.ToFormats) != 1 || raw.Options.ToFormats[0] != "md" {
			t.Errorf("default to_formats should be [md], got %v", raw.Options.ToFormats)
		}
		if !raw.Options.DoOCR {
			t.Error("do_ocr should default to true")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"document": {"md_content": "# Paper title\n\nAbstract..."},
			"status": "success",
			"processing_time": 3.14
		}`))
	})
	defer srv.Close()
	enableDocling(t, srv.URL)

	eng := packs.New()
	pack := DocParse(permissiveEgressGuard())
	res, err := eng.Execute(context.Background(), pack,
		json.RawMessage(`{"source_url":"https://example.com/paper.pdf"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var out struct {
		Source         string  `json:"source"`
		Markdown       string  `json:"markdown"`
		Status         string  `json:"status"`
		ProcessingTime float64 `json:"processing_time"`
	}
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if out.Source != "https://example.com/paper.pdf" {
		t.Errorf("source round-trip wrong: %q", out.Source)
	}
	if !strings.Contains(out.Markdown, "Paper title") {
		t.Errorf("markdown not propagated: %q", out.Markdown)
	}
	if out.Status != "success" {
		t.Errorf("status not propagated: %q", out.Status)
	}
	if out.ProcessingTime != 3.14 {
		t.Errorf("processing_time not propagated: %v", out.ProcessingTime)
	}
}

func TestDocParse_FileSourceHappyPath(t *testing.T) {
	payload := []byte("%PDF-1.4 fake pdf bytes")
	b64 := base64.StdEncoding.EncodeToString(payload)

	srv := stubDocling(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var raw struct {
			Sources     []json.RawMessage `json:"sources"`
			HTTPSources json.RawMessage   `json:"http_sources"`
			FileSources json.RawMessage   `json:"file_sources"`
		}
		_ = json.Unmarshal(body, &raw)
		if len(raw.HTTPSources) > 0 || len(raw.FileSources) > 0 {
			t.Errorf("legacy http_sources/file_sources fields must not be sent: %s", body)
		}
		if len(raw.Sources) != 1 {
			t.Fatalf("expected exactly 1 entry in sources, got %d: %s", len(raw.Sources), body)
		}
		var fs doclingFileSource
		if err := json.Unmarshal(raw.Sources[0], &fs); err != nil {
			t.Fatalf("source[0] not the file shape: %v (raw=%s)", err, raw.Sources[0])
		}
		if fs.Kind != "file" {
			t.Errorf("source[0].kind = %q, want \"file\"", fs.Kind)
		}
		if fs.Filename != "upload.pdf" {
			t.Errorf("filename not forwarded: %q", fs.Filename)
		}
		if fs.Base64String != b64 {
			t.Errorf("base64_string not forwarded: got %q", fs.Base64String)
		}
		_, _ = w.Write([]byte(`{
			"document": {"md_content": "# Uploaded\n\nhi"},
			"status": "success",
			"processing_time": 0.5
		}`))
	})
	defer srv.Close()
	enableDocling(t, srv.URL)

	eng := packs.New()
	pack := DocParse(permissiveEgressGuard())
	reqBody, _ := json.Marshal(map[string]any{
		"source_b64": b64,
		"filename":   "upload.pdf",
	})
	res, err := eng.Execute(context.Background(), pack, reqBody)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var out struct {
		Source   string `json:"source"`
		Markdown string `json:"markdown"`
	}
	_ = json.Unmarshal(res.Output, &out)
	if out.Source != "upload.pdf" {
		t.Errorf("source should reflect filename for file sources: %q", out.Source)
	}
	if !strings.Contains(out.Markdown, "Uploaded") {
		t.Errorf("markdown not propagated: %q", out.Markdown)
	}
}

func TestDocParse_DisabledByDefault(t *testing.T) {
	t.Setenv("HELMDECK_DOCLING_ENABLED", "")
	eng := packs.New()
	pack := DocParse(permissiveEgressGuard())
	_, err := eng.Execute(context.Background(), pack,
		json.RawMessage(`{"source_url":"https://example.com/x.pdf"}`))
	if err == nil {
		t.Fatal("expected error when docling is disabled")
	}
	pe, _ := err.(*packs.PackError)
	if pe == nil || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected invalid_input, got %v", err)
	}
	if !strings.Contains(pe.Message, "HELMDECK_DOCLING_ENABLED") {
		t.Errorf("error should mention the env var: %s", pe.Message)
	}
}

func TestDocParse_RequiresExactlyOneSource(t *testing.T) {
	enableDocling(t, "http://unused")
	eng := packs.New()
	pack := DocParse(permissiveEgressGuard())

	// Neither set
	_, err := eng.Execute(context.Background(), pack, json.RawMessage(`{}`))
	if err == nil {
		t.Error("expected error when neither source is set")
	}

	// Both set
	b64 := base64.StdEncoding.EncodeToString([]byte("x"))
	both, _ := json.Marshal(map[string]any{
		"source_url": "https://example.com/x.pdf",
		"source_b64": b64,
		"filename":   "x.pdf",
	})
	_, err = eng.Execute(context.Background(), pack, both)
	if err == nil {
		t.Error("expected error when both sources are set")
	}
}

func TestDocParse_FileSourceRequiresFilename(t *testing.T) {
	enableDocling(t, "http://unused")
	eng := packs.New()
	pack := DocParse(permissiveEgressGuard())
	b64 := base64.StdEncoding.EncodeToString([]byte("%PDF-1.4"))
	reqBody, _ := json.Marshal(map[string]any{"source_b64": b64})
	_, err := eng.Execute(context.Background(), pack, reqBody)
	if err == nil {
		t.Fatal("expected error when filename is missing for file source")
	}
	if !strings.Contains(err.Error(), "filename") {
		t.Errorf("error should mention filename: %v", err)
	}
}

func TestDocParse_InvalidBase64Rejected(t *testing.T) {
	enableDocling(t, "http://unused")
	eng := packs.New()
	pack := DocParse(permissiveEgressGuard())
	reqBody, _ := json.Marshal(map[string]any{
		"source_b64": "!!!not base64!!!",
		"filename":   "x.pdf",
	})
	_, err := eng.Execute(context.Background(), pack, reqBody)
	if err == nil {
		t.Fatal("expected invalid base64 to be rejected")
	}
	pe, _ := err.(*packs.PackError)
	if pe == nil || pe.Code != packs.CodeInvalidInput {
		t.Errorf("expected invalid_input, got %v", err)
	}
}

func TestDocParse_CustomFormatsForwarded(t *testing.T) {
	var gotFormats []string
	srv := stubDocling(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var in doclingRequest
		_ = json.Unmarshal(body, &in)
		gotFormats = in.Options.ToFormats
		_, _ = w.Write([]byte(`{
			"document": {
				"md_content": "md",
				"text_content": "text",
				"html_content": "<p>html</p>"
			},
			"status": "success"
		}`))
	})
	defer srv.Close()
	enableDocling(t, srv.URL)

	eng := packs.New()
	pack := DocParse(permissiveEgressGuard())
	// Ask for text + html; the pack should still force "md" into
	// the request so the markdown field is populated (output
	// schema requires it).
	res, err := eng.Execute(context.Background(), pack, json.RawMessage(`{
		"source_url":"https://example.com/x.pdf",
		"formats":["text","html"]
	}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !containsString(gotFormats, "md") {
		t.Errorf("pack should always request md even when caller omits it: %v", gotFormats)
	}
	if !containsString(gotFormats, "text") || !containsString(gotFormats, "html") {
		t.Errorf("caller-requested formats not forwarded: %v", gotFormats)
	}
	var out map[string]any
	_ = json.Unmarshal(res.Output, &out)
	if _, ok := out["text"]; !ok {
		t.Error("text field should be in output when requested")
	}
	if _, ok := out["html"]; !ok {
		t.Error("html field should be in output when requested")
	}
}

func TestDocParse_NaturalLanguageFormatAliases(t *testing.T) {
	// Issue #91: models default to "markdown"/"plaintext" because that's
	// the natural value. Accept those as aliases for "md"/"text".
	var gotFormats []string
	srv := stubDocling(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var in doclingRequest
		_ = json.Unmarshal(body, &in)
		gotFormats = in.Options.ToFormats
		_, _ = w.Write([]byte(`{
			"document": {"md_content": "md", "text_content": "txt"},
			"status": "success"
		}`))
	})
	defer srv.Close()
	enableDocling(t, srv.URL)

	eng := packs.New()
	pack := DocParse(permissiveEgressGuard())
	_, err := eng.Execute(context.Background(), pack, json.RawMessage(`{
		"source_url":"https://example.com/x.pdf",
		"formats":["markdown","plaintext"]
	}`))
	if err != nil {
		t.Fatalf("alias formats should be accepted: %v", err)
	}
	// After normalization the request to Docling should use md+text.
	if !containsString(gotFormats, "md") {
		t.Errorf("'markdown' should normalize to 'md' on the wire: %v", gotFormats)
	}
	if !containsString(gotFormats, "text") {
		t.Errorf("'plaintext' should normalize to 'text' on the wire: %v", gotFormats)
	}
	for _, f := range gotFormats {
		if f == "markdown" || f == "plaintext" {
			t.Errorf("aliases should be normalized before forwarding to Docling: %v", gotFormats)
		}
	}
}

func TestDocParse_RejectsExoticFormat(t *testing.T) {
	enableDocling(t, "http://unused")
	eng := packs.New()
	pack := DocParse(permissiveEgressGuard())
	_, err := eng.Execute(context.Background(), pack, json.RawMessage(`{
		"source_url":"https://example.com/x.pdf",
		"formats":["doctags"]
	}`))
	if err == nil {
		t.Fatal("expected unsupported format to be rejected")
	}
	pe, _ := err.(*packs.PackError)
	if pe == nil || pe.Code != packs.CodeInvalidInput {
		t.Errorf("expected invalid_input, got %v", err)
	}
}

func TestDocParse_DoOCRExplicitFalseForwarded(t *testing.T) {
	var gotDoOCR bool
	srv := stubDocling(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var in doclingRequest
		_ = json.Unmarshal(body, &in)
		gotDoOCR = in.Options.DoOCR
		_, _ = w.Write([]byte(`{"document":{"md_content":"ok"},"status":"success"}`))
	})
	defer srv.Close()
	enableDocling(t, srv.URL)

	eng := packs.New()
	pack := DocParse(permissiveEgressGuard())
	_, err := eng.Execute(context.Background(), pack, json.RawMessage(`{
		"source_url":"https://example.com/x.pdf",
		"do_ocr": false
	}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotDoOCR != false {
		t.Errorf("do_ocr=false should be forwarded as false, got %v", gotDoOCR)
	}
}

func TestDocParse_EgressGuardBlocksMetadataIP(t *testing.T) {
	enableDocling(t, "http://unused")
	guard := security.New(
		security.WithResolver(stubFixedResolver{ip: "169.254.169.254"}),
	)
	eng := packs.New()
	pack := DocParse(guard)
	_, err := eng.Execute(context.Background(), pack,
		json.RawMessage(`{"source_url":"https://meta.example/x.pdf"}`))
	if err == nil {
		t.Fatal("expected egress guard to block metadata host")
	}
	if !strings.Contains(err.Error(), "egress denied") {
		t.Errorf("error should mention egress: %v", err)
	}
}

func TestDocParse_EgressGuardSkippedForFileSource(t *testing.T) {
	// File sources never leave helmdeck to fetch bytes — the agent
	// supplied them inline. The egress guard shouldn't block on
	// anything for this path, even with a strict (metadata-IP)
	// stub resolver.
	srv := stubDocling(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"document":{"md_content":"ok"},"status":"success"}`))
	})
	defer srv.Close()
	enableDocling(t, srv.URL)

	guard := security.New(
		security.WithResolver(stubFixedResolver{ip: "169.254.169.254"}),
	)
	eng := packs.New()
	pack := DocParse(guard)
	b64 := base64.StdEncoding.EncodeToString([]byte("%PDF-1.4"))
	reqBody, _ := json.Marshal(map[string]any{
		"source_b64": b64,
		"filename":   "x.pdf",
	})
	if _, err := eng.Execute(context.Background(), pack, reqBody); err != nil {
		t.Errorf("file source should bypass egress guard: %v", err)
	}
}

func TestDocParse_UpstreamErrorSurfaced(t *testing.T) {
	srv := stubDocling(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"detail":"model crashed"}`))
	})
	defer srv.Close()
	enableDocling(t, srv.URL)

	eng := packs.New()
	pack := DocParse(permissiveEgressGuard())
	_, err := eng.Execute(context.Background(), pack,
		json.RawMessage(`{"source_url":"https://example.com/x.pdf"}`))
	if err == nil {
		t.Fatal("expected handler_failed from upstream 500")
	}
	pe, _ := err.(*packs.PackError)
	if pe == nil || pe.Code != packs.CodeHandlerFailed {
		t.Fatalf("expected handler_failed, got %v", err)
	}
	if !strings.Contains(pe.Message, "model crashed") {
		t.Errorf("upstream error snippet should propagate: %s", pe.Message)
	}
}

func TestDocParse_FailureStatusIsError(t *testing.T) {
	srv := stubDocling(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"document": {"md_content": ""},
			"status": "failure",
			"errors": ["unsupported format", "parser blew up"]
		}`))
	})
	defer srv.Close()
	enableDocling(t, srv.URL)

	eng := packs.New()
	pack := DocParse(permissiveEgressGuard())
	_, err := eng.Execute(context.Background(), pack,
		json.RawMessage(`{"source_url":"https://example.com/x.pdf"}`))
	if err == nil {
		t.Fatal("expected status=failure to surface as handler_failed")
	}
	if !strings.Contains(err.Error(), "parser blew up") {
		t.Errorf("docling errors should propagate: %v", err)
	}
}

func TestDocParse_PartialSuccessIsAccepted(t *testing.T) {
	srv := stubDocling(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"document": {"md_content": "# Doc\n\npartial content"},
			"status": "partial_success",
			"errors": ["page 5 OCR failed"]
		}`))
	})
	defer srv.Close()
	enableDocling(t, srv.URL)

	eng := packs.New()
	pack := DocParse(permissiveEgressGuard())
	res, err := eng.Execute(context.Background(), pack,
		json.RawMessage(`{"source_url":"https://example.com/x.pdf"}`))
	if err != nil {
		t.Fatalf("partial_success should NOT be an error: %v", err)
	}
	var out struct {
		Status   string `json:"status"`
		Markdown string `json:"markdown"`
	}
	_ = json.Unmarshal(res.Output, &out)
	if out.Status != "partial_success" {
		t.Errorf("partial_success should pass through: %q", out.Status)
	}
	if !strings.Contains(out.Markdown, "partial content") {
		t.Errorf("markdown should still be returned on partial_success: %q", out.Markdown)
	}
}

func TestDocParse_EmptyMarkdownIsError(t *testing.T) {
	srv := stubDocling(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"document": {"md_content": ""},
			"status": "success"
		}`))
	})
	defer srv.Close()
	enableDocling(t, srv.URL)

	eng := packs.New()
	pack := DocParse(permissiveEgressGuard())
	_, err := eng.Execute(context.Background(), pack,
		json.RawMessage(`{"source_url":"https://example.com/x.pdf"}`))
	if err == nil {
		t.Fatal("expected empty markdown to produce a handler_failed")
	}
	pe, _ := err.(*packs.PackError)
	if pe == nil || pe.Code != packs.CodeHandlerFailed {
		t.Errorf("expected handler_failed, got %v", err)
	}
}

// TestDocParse_URLExtensionAllowlist — source_url must end in a known
// document extension. Web pages (.html, no extension, Medium-style
// blog URLs) are rejected at input validation with a caller_fixable
// error routing the agent to web.scrape. The pack never reaches
// Docling for these inputs, so no cryptic 4xx surfaces.
func TestDocParse_URLExtensionAllowlist(t *testing.T) {
	// The handler should reject without ever calling docling, so the
	// stub's t.Fatal fires if the request leaks through.
	srv := stubDocling(t, func(http.ResponseWriter, *http.Request) {
		t.Fatal("docling should not be called for a non-document URL")
	})
	defer srv.Close()
	enableDocling(t, srv.URL)
	eng := packs.New()
	pack := DocParse(permissiveEgressGuard())

	for _, tc := range []struct {
		name       string
		url        string
		wantSubstr string // a substring expected in the error message
	}{
		{"Medium webpage", "https://medium.com/@author/some-post-abc123", "no file extension"},
		{"raw .html", "https://example.com/article.html", "not a supported document type"},
		{"arxiv abstract (no extension)", "https://arxiv.org/abs/1706.03762", "no file extension"},
		{"unsupported .epub", "https://example.com/book.epub", "not a supported document type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{"source_url": tc.url})
			_, err := eng.Execute(context.Background(), pack, body)
			pe, _ := err.(*packs.PackError)
			if pe == nil || pe.Code != packs.CodeInvalidInput {
				t.Fatalf("want CodeInvalidInput, got %v", err)
			}
			if !strings.Contains(pe.Message, tc.wantSubstr) {
				t.Errorf("error msg = %q, want substring %q", pe.Message, tc.wantSubstr)
			}
			// The routing hint must always be present so the agent
			// knows what to do next — that's the whole point of
			// failing here instead of letting Docling fail later.
			if !strings.Contains(pe.Message, "web.scrape") {
				t.Errorf("error msg should route to web.scrape, got: %q", pe.Message)
			}
		})
	}
}

// TestDocParse_URLExtensionAllowlist_AcceptedShapes — extensions in
// the allowlist (case-insensitive, with query strings) make it
// through input validation and reach docling.
func TestDocParse_URLExtensionAllowlist_AcceptedShapes(t *testing.T) {
	for _, url := range []string{
		"https://arxiv.org/pdf/1706.03762.pdf",
		"https://example.com/paper.PDF",          // upper case
		"https://example.com/deck.pptx?v=2",      // query string
		"https://example.com/scan.tiff",
	} {
		t.Run(url, func(t *testing.T) {
			called := false
			srv := stubDocling(t, func(w http.ResponseWriter, _ *http.Request) {
				called = true
				_, _ = w.Write([]byte(`{"document":{"md_content":"# ok"},"status":"success","processing_time":0.1}`))
			})
			defer srv.Close()
			enableDocling(t, srv.URL)
			eng := packs.New()
			pack := DocParse(permissiveEgressGuard())
			body, _ := json.Marshal(map[string]any{"source_url": url})
			if _, err := eng.Execute(context.Background(), pack, body); err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if !called {
				t.Error("docling should have been called for an allowlisted extension")
			}
		})
	}
}

// TestDocParse_URLExtensionAllowlist_MalformedURL — a non-URL string
// short-circuits with invalid_input rather than crashing url.Parse
// downstream.
func TestDocParse_URLExtensionAllowlist_MalformedURL(t *testing.T) {
	enableDocling(t, "http://unused")
	eng := packs.New()
	pack := DocParse(permissiveEgressGuard())
	body, _ := json.Marshal(map[string]any{"source_url": "not a url"})
	_, err := eng.Execute(context.Background(), pack, body)
	pe, _ := err.(*packs.PackError)
	if pe == nil || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("want CodeInvalidInput, got %v", err)
	}
}
