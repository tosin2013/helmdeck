// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// web_scrape.go (T807b, ADR 035) — Firecrawl-backed `web.scrape` pack.
//
// This is the zero-selectors counterpart to the Phase 2 `web.scrape_spa`
// pack. Callers point it at a URL and get clean markdown back; the
// Firecrawl service decides how to render, clean, and extract. No CSS
// selector knowledge required from the agent — the whole point of
// ADR 035 is that an LLM can say "scrape this page" and have it work
// without first figuring out the DOM.
//
// Deployment: Firecrawl runs as an optional compose service (see
// deploy/compose/compose.firecrawl.yml). When `HELMDECK_FIRECRAWL_URL`
// is unset, the pack returns a typed CodeInvalidInput error whose
// message points the operator at the env var. This is preferable to
// refusing to register the pack entirely — agents discovering the
// catalog see `web.scrape` and get an actionable error instead of
// silent absence.
//
// Input shape:
//
//	{
//	  "url":     "https://example.com/article",
//	  "formats": ["markdown", "html"],    // optional; default ["markdown"]
//	  "wait_ms": 2000                     // optional; default 0
//	}
//
// Output shape:
//
//	{
//	  "url":      "...",
//	  "markdown": "# Title\n\n...",
//	  "html":     "<html>...",             // only if requested
//	  "title":    "Page title",            // from Firecrawl metadata
//	  "links":    ["https://..."],         // only if requested
//	  "status":   200                      // upstream fetch status
//	}
//
// Security:
//   - HELMDECK_FIRECRAWL_URL is expected to be a private-network
//     address (helmdeck-firecrawl on baas-net). The egress guard is
//     NOT consulted on this call — that's a deliberate carve-out,
//     same as the gateway talking to garage or postgres on the
//     private bridge. Operators who point FIRECRAWL_URL at a public
//     endpoint accept the trust model.
//   - The target URL the agent passes IS run through the egress
//     guard first, so Firecrawl cannot be used as an SSRF pivot to
//     reach cloud metadata or RFC 1918 from the public internet.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/security"
)

const (
	// defaultFirecrawlURL matches the service name in
	// deploy/compose/compose.firecrawl.yml. Overridable via
	// HELMDECK_FIRECRAWL_URL so operators running Firecrawl on a
	// separate host can point there.
	defaultFirecrawlURL = "http://helmdeck-firecrawl:3002"
	// firecrawlTimeout caps the round-trip. Firecrawl itself has
	// per-request timeouts, so this is a defense against the
	// service hanging (Playwright stall, Redis lock-up).
	firecrawlTimeout = 90 * time.Second
	// maxFirecrawlResponse caps how much we read from Firecrawl.
	// The response contains the page markdown which for most
	// articles sits under 200 KB; 8 MiB covers dense pages with
	// embedded HTML without letting a pathological crawl OOM the
	// control plane.
	maxFirecrawlResponse = 8 << 20
)

// WebScrape constructs the pack. The env-var gate is resolved
// per-call inside the handler (not at construction time) so
// operators can flip the toggle without restarting the control
// plane once hot-reload config lands.
func WebScrape(eg *security.EgressGuard) *packs.Pack {
	return &packs.Pack{
		Name:        "web.scrape",
		Version:     "v1",
		Description: "Scrape a URL to clean markdown via Firecrawl. No selectors required.",
		InputSchema: packs.BasicSchema{
			Required: []string{"url"},
			Properties: map[string]string{
				"url":     "string",
				"formats": "array",
				"wait_ms": "number",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"url", "markdown"},
			Properties: map[string]string{
				"url":      "string",
				"markdown": "string",
				"html":     "string",
				"title":    "string",
				"links":    "array",
				"status":   "number",
			},
		},
		Handler: webScrapeHandler(eg),
	}
}

type webScrapeInput struct {
	URL     string   `json:"url"`
	Formats []string `json:"formats"`
	WaitMS  int      `json:"wait_ms"`
}

// firecrawlScrapeRequest matches the v1 /v1/scrape body shape.
// See https://docs.firecrawl.dev/v1/api-reference/endpoint/scrape.
type firecrawlScrapeRequest struct {
	URL     string   `json:"url"`
	Formats []string `json:"formats,omitempty"`
	WaitFor int      `json:"waitFor,omitempty"`
}

type firecrawlScrapeResponse struct {
	Success bool `json:"success"`
	Error   string `json:"error,omitempty"`
	Data    struct {
		Markdown string   `json:"markdown"`
		HTML     string   `json:"html"`
		RawHTML  string   `json:"rawHtml"`
		Links    []string `json:"links"`
		Metadata struct {
			Title       string `json:"title"`
			Description string `json:"description"`
			SourceURL   string `json:"sourceURL"`
			StatusCode  int    `json:"statusCode"`
		} `json:"metadata"`
	} `json:"data"`
}

func webScrapeHandler(eg *security.EgressGuard) packs.HandlerFunc {
	return func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
		// 1. Gate on env var. Operators who have not opted into the
		// Firecrawl overlay get a typed error that points at the
		// exact config knob they need to flip — not a generic
		// connection-refused wrapped five layers deep.
		if os.Getenv("HELMDECK_FIRECRAWL_ENABLED") != "true" {
			return nil, &packs.PackError{
				Code: packs.CodeInvalidInput,
				Message: "web.scrape is disabled; set HELMDECK_FIRECRAWL_ENABLED=true " +
					"and bring up the Firecrawl overlay (deploy/compose/compose.firecrawl.yml)",
			}
		}
		base := strings.TrimRight(os.Getenv("HELMDECK_FIRECRAWL_URL"), "/")
		if base == "" {
			base = defaultFirecrawlURL
		}

		var in webScrapeInput
		if err := json.Unmarshal(ec.Input, &in); err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
		}
		if strings.TrimSpace(in.URL) == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "url is required"}
		}
		// Closed-set formats — the pack's output schema only models
		// markdown/html/links, so reject anything exotic early and
		// tell the caller what's supported.
		formats := in.Formats
		if len(formats) == 0 {
			formats = []string{"markdown"}
		}
		for _, f := range formats {
			switch f {
			case "markdown", "html", "rawHtml", "links":
			default:
				return nil, &packs.PackError{
					Code:    packs.CodeInvalidInput,
					Message: fmt.Sprintf("unsupported format %q; use markdown, html, rawHtml, or links", f),
				}
			}
		}

		// 2. Egress guard on the *target* URL. Firecrawl itself is
		// on the private bridge, but if we didn't guard the target
		// the agent could ask Firecrawl to fetch 169.254.169.254
		// and leak cloud metadata via the scraped markdown. Belt
		// and braces — Firecrawl should not be trusted to enforce
		// SSRF policy on helmdeck's behalf.
		if eg != nil {
			if err := eg.CheckURL(ctx, in.URL); err != nil {
				return nil, &packs.PackError{
					Code:    packs.CodeInvalidInput,
					Message: fmt.Sprintf("egress denied: %v", err),
					Cause:   err,
				}
			}
		}

		// 3. Build the Firecrawl request. waitFor is the
		// settle-delay Firecrawl applies after nav before capturing
		// the DOM — the same knob as wait_ms on web.scrape_spa.
		reqBody := firecrawlScrapeRequest{
			URL:     in.URL,
			Formats: formats,
			WaitFor: in.WaitMS,
		}
		bodyBytes, err := json.Marshal(reqBody)
		if err != nil {
			return nil, &packs.PackError{Code: packs.CodeInternal, Message: err.Error(), Cause: err}
		}

		httpCtx, cancel := context.WithTimeout(ctx, firecrawlTimeout)
		defer cancel()
		req, err := http.NewRequestWithContext(httpCtx, "POST", base+"/v1/scrape", bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, &packs.PackError{Code: packs.CodeInternal, Message: err.Error(), Cause: err}
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")

		client := &http.Client{Timeout: firecrawlTimeout}
		resp, err := client.Do(req)
		if err != nil {
			return nil, &packs.PackError{
				Code:    packs.CodeHandlerFailed,
				Message: fmt.Sprintf("firecrawl request to %s: %v", base, err),
				Cause:   err,
			}
		}
		defer resp.Body.Close()

		raw, err := io.ReadAll(io.LimitReader(resp.Body, maxFirecrawlResponse+1))
		if err != nil {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("read firecrawl response: %v", err), Cause: err}
		}
		if len(raw) > maxFirecrawlResponse {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("firecrawl response exceeds %d bytes", maxFirecrawlResponse)}
		}
		if resp.StatusCode >= 400 {
			// Surface a snippet of the upstream body so operators
			// debugging a misconfigured Firecrawl (wrong env, broken
			// Playwright service) see something actionable. Clip to
			// 512 B so a giant HTML error page doesn't flood the
			// audit log.
			snippet := string(raw)
			if len(snippet) > 512 {
				snippet = snippet[:512] + "…"
			}
			return nil, &packs.PackError{
				Code:    packs.CodeHandlerFailed,
				Message: fmt.Sprintf("firecrawl %d: %s", resp.StatusCode, snippet),
			}
		}

		var fc firecrawlScrapeResponse
		if err := json.Unmarshal(raw, &fc); err != nil {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("decode firecrawl response: %v", err), Cause: err}
		}
		if !fc.Success {
			msg := fc.Error
			if msg == "" {
				msg = "firecrawl returned success=false without an error message"
			}
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed, Message: msg}
		}
		if fc.Data.Markdown == "" {
			// Empty markdown almost always means the target
			// returned a bot-challenge or blank body. Surface as
			// handler_failed so retries against a different
			// strategy (e.g. screenshot_url for visual inspection)
			// make sense.
			return nil, &packs.PackError{
				Code:    packs.CodeHandlerFailed,
				Message: fmt.Sprintf("firecrawl returned empty markdown for %s (status=%d)", in.URL, fc.Data.Metadata.StatusCode),
			}
		}

		out := map[string]any{
			"url":      in.URL,
			"markdown": fc.Data.Markdown,
			"status":   fc.Data.Metadata.StatusCode,
		}
		if fc.Data.HTML != "" {
			out["html"] = fc.Data.HTML
		}
		if fc.Data.Metadata.Title != "" {
			out["title"] = fc.Data.Metadata.Title
		}
		if len(fc.Data.Links) > 0 {
			out["links"] = fc.Data.Links
		}
		return json.Marshal(out)
	}
}
