// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// research_deep.go (T622, ADR 035) — Firecrawl-backed deep research.
//
// The pack composes Firecrawl's `/v1/search` (which internally chains
// Google → scrape → markdown) with a final gateway LLM synthesis
// step, so an agent can hand in a topic and get back both the
// sources and a written answer in one call. It's the canonical
// shape the ADR 035 "host, don't rebuild" story expects: helmdeck
// glues two upstream services together (Firecrawl for retrieval,
// the gateway LLM for synthesis) with vault/audit/egress wrapped
// around the whole thing.
//
// Why one pack instead of two handler round-trips?
//   - Synthesis is almost always wanted and the step order is fixed;
//     making the caller orchestrate search → per-URL-synth → merge
//     leaks Firecrawl's response shape into the agent prompt.
//   - The engine spends one audit-logged call instead of N.
//   - Fallback / retry (one source 500s, the rest succeed) belongs
//     inside the handler, not in the caller's planner.
//
// Deployment notes:
//   - Gated on HELMDECK_FIRECRAWL_ENABLED=true, same env var as
//     web.scrape (T807b). Operators who haven't enabled the overlay
//     get a clear CodeInvalidInput error pointing at the toggle.
//   - Self-hosted Firecrawl ships with Google-backed search by
//     default (no API key needed). Operators who prefer SearXNG
//     set SEARXNG_ENDPOINT on the Firecrawl container — helmdeck
//     doesn't care which one, the response shape is the same.
//   - No egress guard on the search query: it's a string, not a
//     URL. Source URLs come back from Firecrawl's own crawler,
//     which runs on the private baas-net bridge, so by the time
//     we see them Firecrawl has already fetched them. The trust
//     boundary for SSRF is inside Firecrawl's own config (its
//     outbound network policy), not at the helmdeck client side.
//
// Input shape:
//
//	{
//	  "query":      "recent advances in WebRTC congestion control",
//	  "limit":      5,                                  // optional, default 5, cap 10
//	  "model":      "openai/gpt-4o-mini",               // required, provider/model
//	  "max_tokens": 1024                                // optional, default 1024
//	}
//
// Output shape:
//
//	{
//	  "query":     "...",
//	  "sources":   [ { "url": "...", "title": "...", "description": "...", "markdown": "..." }, ... ],
//	  "synthesis": "One-paragraph written answer grounded in the sources.",
//	  "model":     "openai/gpt-4o-mini"
//	}

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/tosin2013/helmdeck/internal/gateway"
	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/vision"
)

const (
	defaultResearchLimit     = 5
	maxResearchLimit         = 10
	defaultResearchMaxTokens = 1024
	// researchTimeout caps the round trip. Firecrawl's /v1/search
	// with per-source scraping is inherently slow — on public
	// traffic the first page typically settles under 30s but
	// worst-case we have seen 90s before results stabilize. 180s
	// leaves headroom for the LLM synthesis too, which on small
	// models is another 5-15s.
	researchTimeout = 180 * time.Second
	// maxResearchResponse caps the Firecrawl payload we'll read.
	// N markdown documents at a few hundred KB each is the happy
	// case; 16 MiB covers very long technical articles without
	// letting a pathological source OOM the control plane.
	maxResearchResponse = 16 << 20

	// researchSynthesisPrompt is the frozen system message the
	// synthesis LLM call sends. Short on purpose — this is the
	// final squeeze after the model has already been given the
	// full source markdown in the user message. Models that get
	// chatty here waste tokens; we want a tight answer.
	researchSynthesisPrompt = `You are a research assistant. You will receive a user's query followed by markdown excerpts from several web sources. Write a concise, factual synthesis answering the query.

Rules:
  - Ground every claim in the sources provided. Do not invent facts.
  - When a claim is supported by a specific source, cite the source URL in parentheses after the claim. Use the exact URL from the source list.
  - If the sources disagree, say so and present both positions.
  - If the sources do not answer the query, say so plainly and do not pad.
  - Aim for 3-6 sentences. Do not use headings, bullets, or markdown formatting — plain prose only.`
)

// ResearchDeep constructs the pack. Dispatcher is the gateway
// surface the pack uses for synthesis; cmd/control-plane wires in
// the same dispatcher the vision packs use, so registration lives
// inside the "vision packs need a dispatcher" conditional block.
func ResearchDeep(d vision.Dispatcher) *packs.Pack {
	return &packs.Pack{
		Name:        "research.deep",
		Version:     "v1",
		Description: "Search a topic via Firecrawl, scrape each result to markdown, and return a synthesized answer.",
		InputSchema: packs.BasicSchema{
			Required: []string{"query", "model"},
			Properties: map[string]string{
				"query":      "string",
				"limit":      "number",
				"model":      "string",
				"max_tokens": "number",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"query", "sources", "synthesis", "model"},
			Properties: map[string]string{
				"query":     "string",
				"sources":   "array",
				"synthesis": "string",
				"model":     "string",
			},
		},
		Handler: researchDeepHandler(d),
		// Heavy: search + N source scrapes + synthesis call. With
		// limit=5 and an open-weight model this routinely runs 30-90s.
		// Async=true keeps the JSON-RPC request short by returning
		// a SEP-1686 task envelope.
		Async: true,
	}
}

type researchDeepInput struct {
	Query     string `json:"query"`
	Limit     int    `json:"limit"`
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens"`
}

// researchSource is one item in the output sources array. We
// deliberately do NOT surface Firecrawl's full metadata block —
// the response would balloon with fields most callers never use.
type researchSource struct {
	URL         string `json:"url"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Markdown    string `json:"markdown"`
}

// firecrawlSearchRequest matches the /v1/search body shape.
// scrapeOptions.formats=["markdown"] folds scrape-each-result into
// the same HTTP round trip, which is how Firecrawl's own Python
// SDK wires "search + scrape" from a single call.
type firecrawlSearchRequest struct {
	Query         string                    `json:"query"`
	Limit         int                       `json:"limit,omitempty"`
	ScrapeOptions *firecrawlSearchScrapeOpt `json:"scrapeOptions,omitempty"`
}

type firecrawlSearchScrapeOpt struct {
	Formats []string `json:"formats"`
}

// firecrawlSearchItem is one result in /v1/search's data array.
// Exported as a package type so content.ground can reuse it.
type firecrawlSearchItem struct {
	URL         string `json:"url"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Markdown    string `json:"markdown"`
	Metadata    struct {
		Title      string `json:"title"`
		StatusCode int    `json:"statusCode"`
	} `json:"metadata"`
}

// firecrawlSearchResponse matches the /v1/search response shape
// when scrapeOptions is set — every item carries the scraped
// markdown alongside the SERP metadata.
type firecrawlSearchResponse struct {
	Success bool                  `json:"success"`
	Error   string                `json:"error,omitempty"`
	Data    []firecrawlSearchItem `json:"data"`
}

// callFirecrawlSearch is the shared HTTP round trip used by both
// research.deep and content.ground. Factored out so timeouts,
// response caps, and error shaping live in one place — a change
// here propagates to every pack that grounds against Firecrawl.
// Returns a typed PackError so callers can just `return nil, err`.
func callFirecrawlSearch(ctx context.Context, base string, body firecrawlSearchRequest) (*firecrawlSearchResponse, *packs.PackError) {
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, &packs.PackError{Code: packs.CodeInternal, Message: err.Error(), Cause: err}
	}
	httpCtx, cancel := context.WithTimeout(ctx, researchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(httpCtx, "POST", base+"/v1/search", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, &packs.PackError{Code: packs.CodeInternal, Message: err.Error(), Cause: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: researchTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, &packs.PackError{
			Code:    packs.CodeHandlerFailed,
			Message: fmt.Sprintf("firecrawl search request to %s: %v", base, err),
			Cause:   err,
		}
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResearchResponse+1))
	if err != nil {
		return nil, &packs.PackError{
			Code:    packs.CodeHandlerFailed,
			Message: fmt.Sprintf("read firecrawl search response: %v", err),
			Cause:   err,
		}
	}
	if len(raw) > maxResearchResponse {
		return nil, &packs.PackError{
			Code:    packs.CodeHandlerFailed,
			Message: fmt.Sprintf("firecrawl search response exceeds %d bytes", maxResearchResponse),
		}
	}
	if resp.StatusCode >= 400 {
		snippet := string(raw)
		if len(snippet) > 512 {
			snippet = snippet[:512] + "…"
		}
		return nil, &packs.PackError{
			Code:    packs.CodeHandlerFailed,
			Message: fmt.Sprintf("firecrawl search %d: %s", resp.StatusCode, snippet),
		}
	}
	var fc firecrawlSearchResponse
	if err := json.Unmarshal(raw, &fc); err != nil {
		return nil, &packs.PackError{
			Code:    packs.CodeHandlerFailed,
			Message: fmt.Sprintf("decode firecrawl search response: %v", err),
			Cause:   err,
		}
	}
	if !fc.Success {
		msg := fc.Error
		if msg == "" {
			msg = "firecrawl search returned success=false without an error message"
		}
		return nil, &packs.PackError{Code: packs.CodeHandlerFailed, Message: msg}
	}
	return &fc, nil
}

func researchDeepHandler(d vision.Dispatcher) packs.HandlerFunc {
	return func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
		if d == nil {
			return nil, &packs.PackError{
				Code:    packs.CodeInternal,
				Message: "research.deep registered without a gateway dispatcher",
			}
		}
		// 1. Env gate. Same as web.scrape — we refuse with an
		// actionable error pointing at the exact toggle the
		// operator needs to flip and the overlay compose file
		// to bring up.
		if os.Getenv("HELMDECK_FIRECRAWL_ENABLED") != "true" {
			return nil, &packs.PackError{
				Code: packs.CodeInvalidInput,
				Message: "research.deep is disabled; set HELMDECK_FIRECRAWL_ENABLED=true " +
					"and bring up the Firecrawl overlay (deploy/compose/compose.firecrawl.yml)",
			}
		}
		base := strings.TrimRight(os.Getenv("HELMDECK_FIRECRAWL_URL"), "/")
		if base == "" {
			base = defaultFirecrawlURL
		}

		var in researchDeepInput
		if err := json.Unmarshal(ec.Input, &in); err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
		}
		if strings.TrimSpace(in.Query) == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "query is required"}
		}
		if strings.TrimSpace(in.Model) == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "model is required (provider/model)"}
		}
		limit := in.Limit
		if limit <= 0 {
			limit = defaultResearchLimit
		}
		if limit > maxResearchLimit {
			// Cap aggressively — Firecrawl bills per scrape and
			// the LLM synthesis prompt balloons linearly in the
			// number of sources. 10 is already pushing weak model
			// context windows.
			limit = maxResearchLimit
		}
		maxTokens := in.MaxTokens
		if maxTokens <= 0 {
			maxTokens = defaultResearchMaxTokens
		}

		// 2. Firecrawl /v1/search with inline scrape.
		ec.Report(10, fmt.Sprintf("searching: %q", in.Query))
		fc, perr := callFirecrawlSearch(ctx, base, firecrawlSearchRequest{
			Query: in.Query,
			Limit: limit,
			ScrapeOptions: &firecrawlSearchScrapeOpt{
				Formats: []string{"markdown"},
			},
		})
		if perr != nil {
			return nil, perr
		}

		// Fold the response into our own source shape and drop
		// items with empty markdown — those contribute nothing to
		// synthesis and pad the prompt. We KEEP items with empty
		// description/title because the URL + markdown alone is
		// still useful grounding.
		sources := make([]researchSource, 0, len(fc.Data))
		for _, item := range fc.Data {
			if item.Markdown == "" {
				continue
			}
			title := item.Title
			if title == "" {
				title = item.Metadata.Title
			}
			sources = append(sources, researchSource{
				URL:         item.URL,
				Title:       title,
				Description: item.Description,
				Markdown:    item.Markdown,
			})
		}
		if len(sources) == 0 {
			return nil, &packs.PackError{
				Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("firecrawl search for %q returned no usable sources; "+
					"try a broader query or raise limit", in.Query),
			}
		}

		ec.Report(60, fmt.Sprintf("synthesizing from %d sources", len(sources)))
		// 3. Synthesize via gateway LLM. Each source gets a header
		// line so the model can quote URLs back cleanly. We keep
		// the user message simple — the system prompt carries the
		// rules, the user message is just (query, sources).
		var userMsg strings.Builder
		fmt.Fprintf(&userMsg, "QUERY: %s\n\n", in.Query)
		userMsg.WriteString("SOURCES:\n")
		for i, s := range sources {
			fmt.Fprintf(&userMsg, "\n--- Source %d: %s ---\n", i+1, s.URL)
			if s.Title != "" {
				fmt.Fprintf(&userMsg, "Title: %s\n", s.Title)
			}
			userMsg.WriteString(s.Markdown)
			userMsg.WriteString("\n")
		}
		userMsg.WriteString("\nSynthesize a concise answer grounded in these sources.")

		req2 := gateway.ChatRequest{
			Model:     in.Model,
			MaxTokens: &maxTokens,
			Messages: []gateway.Message{
				{Role: "system", Content: gateway.TextContent(researchSynthesisPrompt)},
				{Role: "user", Content: gateway.TextContent(userMsg.String())},
			},
		}
		chat, err := d.Dispatch(ctx, req2)
		if err != nil {
			return nil, &packs.PackError{
				Code:    packs.CodeHandlerFailed,
				Message: fmt.Sprintf("synthesis dispatch: %v", err),
				Cause:   err,
			}
		}
		if len(chat.Choices) == 0 {
			return nil, &packs.PackError{
				Code:    packs.CodeHandlerFailed,
				Message: "synthesis model returned no choices",
				Cause:   errors.New("empty choices"),
			}
		}
		synthesis := strings.TrimSpace(chat.Choices[0].Message.Content.Text())
		if synthesis == "" {
			return nil, &packs.PackError{
				Code:    packs.CodeHandlerFailed,
				Message: "synthesis model returned empty text",
			}
		}

		out := map[string]any{
			"query":     in.Query,
			"sources":   sources,
			"synthesis": synthesis,
			"model":     in.Model,
		}
		return json.Marshal(out)
	}
}
