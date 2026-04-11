// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/security"
)

// stubFirecrawl spins up an httptest server that mimics the
// /v1/scrape shape. The handler func lets each test craft its own
// response payload without duplicating the boilerplate envelope.
func stubFirecrawl(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/scrape", handler)
	return httptest.NewServer(mux)
}

// enableFirecrawl wires HELMDECK_FIRECRAWL_ENABLED + URL for the
// duration of a single test. t.Setenv handles cleanup.
func enableFirecrawl(t *testing.T, url string) {
	t.Helper()
	t.Setenv("HELMDECK_FIRECRAWL_ENABLED", "true")
	t.Setenv("HELMDECK_FIRECRAWL_URL", url)
}

// permissiveEgressGuard accepts every host. web.scrape uses the
// real DNS resolver internally via security.CheckURL, so we need to
// allowlist all the public ranges the test targets live in. Easier
// to just install a stub resolver that always returns a loopback
// address and allowlist 127.0.0.0/8 (same trick as http_fetch_test).
func permissiveEgressGuard() *security.EgressGuard {
	return security.New(
		security.WithResolver(stubFixedResolver{ip: "127.0.0.1"}),
		security.WithAllowlist([]string{"127.0.0.0/8"}),
	)
}

func TestWebScrape_HappyPath(t *testing.T) {
	srv := stubFirecrawl(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		var in firecrawlScrapeRequest
		if err := json.Unmarshal(body, &in); err != nil {
			t.Fatalf("bad request body: %v", err)
		}
		if in.URL != "https://example.com/article" {
			t.Errorf("upstream saw url=%q", in.URL)
		}
		if len(in.Formats) != 1 || in.Formats[0] != "markdown" {
			t.Errorf("default format should be [markdown], got %v", in.Formats)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"success": true,
			"data": {
				"markdown": "# Hello\n\nworld",
				"metadata": {"title": "Hello page", "statusCode": 200}
			}
		}`))
	})
	defer srv.Close()
	enableFirecrawl(t, srv.URL)

	eng := packs.New()
	pack := WebScrape(permissiveEgressGuard())
	res, err := eng.Execute(context.Background(), pack,
		json.RawMessage(`{"url":"https://example.com/article"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var out struct {
		URL      string `json:"url"`
		Markdown string `json:"markdown"`
		Title    string `json:"title"`
		Status   int    `json:"status"`
	}
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if out.URL != "https://example.com/article" {
		t.Errorf("url round-trip wrong: %q", out.URL)
	}
	if !strings.Contains(out.Markdown, "# Hello") {
		t.Errorf("markdown not propagated: %q", out.Markdown)
	}
	if out.Title != "Hello page" {
		t.Errorf("title not propagated: %q", out.Title)
	}
	if out.Status != 200 {
		t.Errorf("upstream status not propagated: %d", out.Status)
	}
}

func TestWebScrape_DisabledByDefault(t *testing.T) {
	// Deliberately do NOT set HELMDECK_FIRECRAWL_ENABLED. The pack
	// must return CodeInvalidInput with a message pointing at the
	// env var so operators know exactly what to flip.
	t.Setenv("HELMDECK_FIRECRAWL_ENABLED", "")
	eng := packs.New()
	pack := WebScrape(permissiveEgressGuard())
	_, err := eng.Execute(context.Background(), pack,
		json.RawMessage(`{"url":"https://example.com"}`))
	if err == nil {
		t.Fatal("expected error when firecrawl is disabled")
	}
	pe, _ := err.(*packs.PackError)
	if pe == nil || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected invalid_input, got %v", err)
	}
	if !strings.Contains(pe.Message, "HELMDECK_FIRECRAWL_ENABLED") {
		t.Errorf("error should mention the env var: %s", pe.Message)
	}
}

func TestWebScrape_CustomFormatsForwarded(t *testing.T) {
	var gotFormats []string
	srv := stubFirecrawl(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var in firecrawlScrapeRequest
		_ = json.Unmarshal(body, &in)
		gotFormats = in.Formats
		_, _ = w.Write([]byte(`{"success":true,"data":{"markdown":"ok","html":"<p>ok</p>","metadata":{"statusCode":200}}}`))
	})
	defer srv.Close()
	enableFirecrawl(t, srv.URL)

	eng := packs.New()
	pack := WebScrape(permissiveEgressGuard())
	res, err := eng.Execute(context.Background(), pack,
		json.RawMessage(`{"url":"https://example.com","formats":["markdown","html"]}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(gotFormats) != 2 || gotFormats[0] != "markdown" || gotFormats[1] != "html" {
		t.Errorf("formats not forwarded to firecrawl: %v", gotFormats)
	}
	// html field should be present in output now.
	var out map[string]any
	_ = json.Unmarshal(res.Output, &out)
	if _, ok := out["html"]; !ok {
		t.Errorf("html should be in output when requested: %v", out)
	}
}

func TestWebScrape_RejectsExoticFormat(t *testing.T) {
	enableFirecrawl(t, "http://unused")
	eng := packs.New()
	pack := WebScrape(permissiveEgressGuard())
	_, err := eng.Execute(context.Background(), pack,
		json.RawMessage(`{"url":"https://example.com","formats":["screenshot"]}`))
	if err == nil {
		t.Fatal("expected unsupported format to be rejected")
	}
	pe, _ := err.(*packs.PackError)
	if pe == nil || pe.Code != packs.CodeInvalidInput {
		t.Errorf("expected invalid_input, got %v", err)
	}
}

func TestWebScrape_RequiresURL(t *testing.T) {
	enableFirecrawl(t, "http://unused")
	eng := packs.New()
	pack := WebScrape(permissiveEgressGuard())
	_, err := eng.Execute(context.Background(), pack, json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error for missing url")
	}
}

func TestWebScrape_EgressGuardBlocksMetadataIP(t *testing.T) {
	enableFirecrawl(t, "http://unused")
	// Guard with no allowlist + stub resolver returning metadata IP.
	// The pack must short-circuit BEFORE calling Firecrawl so we
	// don't hand the sidecar service an SSRF pivot.
	guard := security.New(
		security.WithResolver(stubFixedResolver{ip: "169.254.169.254"}),
	)
	eng := packs.New()
	pack := WebScrape(guard)
	_, err := eng.Execute(context.Background(), pack,
		json.RawMessage(`{"url":"https://meta.example/"}`))
	if err == nil {
		t.Fatal("expected egress guard to block metadata host")
	}
	if !strings.Contains(err.Error(), "egress denied") {
		t.Errorf("error should mention egress: %v", err)
	}
}

func TestWebScrape_UpstreamErrorSurfaced(t *testing.T) {
	srv := stubFirecrawl(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"success":false,"error":"playwright crashed"}`))
	})
	defer srv.Close()
	enableFirecrawl(t, srv.URL)

	eng := packs.New()
	pack := WebScrape(permissiveEgressGuard())
	_, err := eng.Execute(context.Background(), pack,
		json.RawMessage(`{"url":"https://example.com"}`))
	if err == nil {
		t.Fatal("expected handler_failed from upstream 500")
	}
	pe, _ := err.(*packs.PackError)
	if pe == nil || pe.Code != packs.CodeHandlerFailed {
		t.Fatalf("expected handler_failed, got %v", err)
	}
	if !strings.Contains(pe.Message, "playwright crashed") {
		t.Errorf("upstream error snippet should propagate: %s", pe.Message)
	}
}

func TestWebScrape_EmptyMarkdownIsError(t *testing.T) {
	srv := stubFirecrawl(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"markdown":"","metadata":{"statusCode":403}}}`))
	})
	defer srv.Close()
	enableFirecrawl(t, srv.URL)

	eng := packs.New()
	pack := WebScrape(permissiveEgressGuard())
	_, err := eng.Execute(context.Background(), pack,
		json.RawMessage(`{"url":"https://example.com"}`))
	if err == nil {
		t.Fatal("expected empty markdown to produce a handler_failed")
	}
	pe, _ := err.(*packs.PackError)
	if pe == nil || pe.Code != packs.CodeHandlerFailed {
		t.Errorf("expected handler_failed, got %v", err)
	}
}

func TestWebScrape_SuccessFalseIsError(t *testing.T) {
	srv := stubFirecrawl(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":false,"error":"blocked by robots.txt"}`))
	})
	defer srv.Close()
	enableFirecrawl(t, srv.URL)

	eng := packs.New()
	pack := WebScrape(permissiveEgressGuard())
	_, err := eng.Execute(context.Background(), pack,
		json.RawMessage(`{"url":"https://example.com"}`))
	if err == nil {
		t.Fatal("expected success=false to surface as a pack error")
	}
	if !strings.Contains(err.Error(), "robots.txt") {
		t.Errorf("upstream error message should propagate: %v", err)
	}
}
