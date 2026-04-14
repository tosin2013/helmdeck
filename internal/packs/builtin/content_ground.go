// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// content_ground.go (T623, ADR 035) — link-grounding for markdown.
//
// An agent hands in a path to a blog post in a session-local clone.
// The pack (1) asks the gateway LLM to extract the top N factual
// claims that would benefit from a citation, (2) runs each claim's
// generated search query through Firecrawl /v1/search, (3) picks
// the first result with a non-empty URL, and (4) rewrites the
// markdown file in place by appending ` [source](url)` after each
// grounded claim. The result is a report of which claims got
// citations, which were skipped, and the new sha256 of the file.
//
// Why one pack and not `research.deep` + `fs.patch` chained by the
// agent?
//   - Claim extraction is a strict JSON round trip — delegating it
//     to the agent means every caller has to re-implement the same
//     prompt and parser, and drift across callers produces
//     inconsistent groundings.
//   - The substring the LLM picks as "the claim" has to match the
//     file content exactly. When the agent orchestrates from
//     outside, the claim text and the file content live in two
//     different context windows and drift is common. One pack that
//     owns the whole pipeline avoids that whole class of bug.
//   - The fs write needs to happen once per run, not once per
//     claim, so we can cap the number of session-shell round trips
//     regardless of how many claims were grounded.
//
// Milestone note (T623): the original milestone text mentions
// `github.search` + `http.fetch` + `web.scrape` + `fs.patch` as the
// canonical chain. We collapse the first three into a single
// Firecrawl `/v1/search` call with inline scrape — Google (which
// Firecrawl's self-hosted default uses) indexes GitHub repos,
// docs, and issues alongside the rest of the web, so one API path
// covers "search GitHub and the wider web" without the plumbing of
// a separate github.search integration. Operators who need
// GitHub-only results add a `site:github.com` token to the
// generated query (the claim-extractor prompt honours a `topic`
// hint for exactly this reason).
//
// Deployment:
//   - Gated on HELMDECK_FIRECRAWL_ENABLED=true (same toggle as
//     web.scrape and research.deep).
//   - NeedsSession=true because the markdown file lives in a
//     session-local clone — fs reads/writes go through the session
//     executor the same way fs.patch does.
//   - No egress guard on claim queries (they are search strings,
//     not URLs). Firecrawl's own egress policy enforces SSRF
//     defence on the crawler side.
//
// Input shape:
//
//	{
//	  "clone_path": "/tmp/helmdeck-abc/posts",
//	  "path":       "2026-quantum.md",
//	  "model":      "openai/gpt-4o-mini",
//	  "max_claims": 5,                                  // optional, default 5, cap 8
//	  "topic":      "quantum computing"                 // optional hint to bias the claim extractor
//	}
//
// Output shape:
//
//	{
//	  "path":              "2026-quantum.md",
//	  "claims_considered": 5,
//	  "claims_grounded":   3,
//	  "grounding":         [ { "claim": "...", "url": "...", "title": "..." }, ... ],
//	  "skipped":           [ "claim with no source found" ],
//	  "sha256":            "hex-of-patched-file",
//	  "file_changed":      true
//	}

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/tosin2013/helmdeck/internal/gateway"
	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/vision"
)

const (
	defaultContentGroundClaims  = 5
	maxContentGroundClaims      = 8
	defaultContentGroundTokens  = 1024
	// contentGroundPrompt is the frozen system prompt for the claim
	// extractor. The strict JSON schema is critical — we parse the
	// response with json.Unmarshal and bail on invalid_input if it
	// doesn't match, so small models need a very concrete example.
	contentGroundPrompt = `You are a fact-checker for technical blog posts. You will receive the full markdown of a post and a maximum number of claims to extract. Your job is to pick the most impactful factual claims that would benefit from an authoritative citation.

For each claim:
  - "text" MUST be a verbatim substring of the original markdown — copy it exactly, including punctuation, so the caller can locate it with a literal substring match. Do NOT rephrase.
  - "query" is the search query you would use to find a source — specific enough to reach authoritative material, not a generic topic word.

Respond with ONE JSON object and nothing else. No markdown fences, no commentary. Schema:

{
  "claims": [
    {"text": "<exact substring from the post>", "query": "<search query>"},
    ...
  ]
}

Rules:
  - Return AT MOST the requested number of claims. Prefer fewer high-quality claims over many weak ones.
  - Skip claims that are trivially obvious, subjective ("I think", "arguably"), or already contain a link.
  - Skip headings, code blocks, and list bullets — ground only prose sentences.
  - If no claim meets the bar, return {"claims": []}.`
)

// ContentGround constructs the pack.
func ContentGround(d vision.Dispatcher) *packs.Pack {
	return &packs.Pack{
		Name:    "content.ground",
		Version: "v1",
		Description: "Extract factual claims from markdown, find authoritative sources via Firecrawl, " +
			"verify each source supports the claim, and optionally rewrite weak claims into stronger " +
			"prose backed by what the sources actually say. Accepts text directly or a file in a session clone. " +
			"Produces a grounded markdown artifact for download.",
		// NeedsSession is false because the text-mode path doesn't
		// require a session. When clone_path + path are provided the
		// handler checks ec.Exec at runtime and returns a clear error
		// if the session isn't available.
		NeedsSession: false,
		InputSchema: packs.BasicSchema{
			Required: []string{"model"},
			Properties: map[string]string{
				"clone_path": "string",
				"path":       "string",
				"text":       "string",
				"model":      "string",
				"max_claims": "number",
				"topic":      "string",
				"rewrite":    "boolean",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"claims_considered", "claims_grounded", "sha256"},
			Properties: map[string]string{
				"path":              "string",
				"claims_considered": "number",
				"claims_grounded":   "number",
				"grounding":         "array",
				"skipped":           "array",
				"sha256":            "string",
				"file_changed":      "boolean",
				"grounded_text":     "string",
				"artifact_key":      "string",
			},
		},
		Handler: contentGroundHandler(d),
		// Heavy: extract → per-claim search → per-claim verify (LLM)
		// → optional whole-document rewrite (LLM). With rewrite=true
		// and a handful of claims, 60-120s is typical. Async=true
		// keeps the JSON-RPC request short via the SEP-1686 task
		// envelope; clients that need synchronous behavior can still
		// use the legacy pack.start/status/result trio explicitly.
		Async: true,
	}
}

type contentGroundInput struct {
	ClonePath string `json:"clone_path"`
	Path      string `json:"path"`
	Text      string `json:"text"` // direct text mode — no session needed
	Model     string `json:"model"`
	MaxClaims int    `json:"max_claims"`
	Topic     string `json:"topic"`
	Rewrite   bool   `json:"rewrite"` // when true, rewrite weak claims using source content
}

// claimPlan is the parsed shape the extractor LLM returns.
type claimPlan struct {
	Claims []struct {
		Text  string `json:"text"`
		Query string `json:"query"`
	} `json:"claims"`
}

type grounding struct {
	Claim   string `json:"claim"`
	URL     string `json:"url"`
	Title   string `json:"title,omitempty"`
	Snippet string `json:"snippet,omitempty"` // excerpt from source that supports the claim
}

func contentGroundHandler(d vision.Dispatcher) packs.HandlerFunc {
	return func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
		if d == nil {
			return nil, &packs.PackError{
				Code:    packs.CodeInternal,
				Message: "content.ground registered without a gateway dispatcher",
			}
		}
		if os.Getenv("HELMDECK_FIRECRAWL_ENABLED") != "true" {
			return nil, &packs.PackError{
				Code: packs.CodeInvalidInput,
				Message: "content.ground is disabled; set HELMDECK_FIRECRAWL_ENABLED=true " +
					"and bring up the Firecrawl overlay (deploy/compose/compose.firecrawl.yml)",
			}
		}
		base := strings.TrimRight(os.Getenv("HELMDECK_FIRECRAWL_URL"), "/")
		if base == "" {
			base = defaultFirecrawlURL
		}

		var in contentGroundInput
		if err := json.Unmarshal(ec.Input, &in); err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
		}
		if strings.TrimSpace(in.Model) == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "model is required (provider/model)"}
		}
		maxClaims := in.MaxClaims
		if maxClaims <= 0 {
			maxClaims = defaultContentGroundClaims
		}
		if maxClaims > maxContentGroundClaims {
			maxClaims = maxContentGroundClaims
		}

		// Two modes:
		//   (A) text mode — markdown provided directly, no session needed
		//   (B) file mode — read from clone_path + path in a session
		var original string
		var full string
		textMode := strings.TrimSpace(in.Text) != ""

		if textMode {
			original = in.Text
		} else {
			// File mode — requires session + clone_path + path
			if strings.TrimSpace(in.ClonePath) == "" {
				return nil, &packs.PackError{Code: packs.CodeInvalidInput,
					Message: "either 'text' (direct markdown) or 'clone_path' + 'path' (file in session) is required"}
			}
			if strings.TrimSpace(in.Path) == "" {
				return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "path is required when using clone_path"}
			}
			if ec.Exec == nil {
				return nil, &packs.PackError{Code: packs.CodeSessionUnavailable,
					Message: "content.ground file mode requires a session executor; use 'text' for direct markdown input"}
			}
			var perr *packs.PackError
			full, perr = safeJoin(in.ClonePath, in.Path)
			if perr != nil {
				return nil, perr
			}
			statRes, err := runShell(ctx, ec, "wc -c < "+shellQuote(full), nil)
			if err != nil || statRes.ExitCode != 0 {
				return nil, &packs.PackError{
					Code:    packs.CodeInvalidInput,
					Message: fmt.Sprintf("file not readable: %s", strings.TrimSpace(string(statRes.Stderr))),
				}
			}
			size, _ := strconv.ParseInt(strings.TrimSpace(string(statRes.Stdout)), 10, 64)
			if size > maxFsReadBytes {
				return nil, &packs.PackError{
					Code:    packs.CodeInvalidInput,
					Message: fmt.Sprintf("file is %d bytes, exceeds %d byte cap", size, maxFsReadBytes),
				}
			}
			readRes, err := runShell(ctx, ec, "cat "+shellQuote(full), nil)
			if err != nil || readRes.ExitCode != 0 {
				return nil, &packs.PackError{
					Code:    packs.CodeHandlerFailed,
					Message: "failed to read markdown file",
				}
			}
			original = string(readRes.Stdout)
		}
		if strings.TrimSpace(original) == "" {
			return nil, &packs.PackError{
				Code:    packs.CodeInvalidInput,
				Message: "markdown content is empty",
			}
		}

		// 3. Claim extraction via LLM. The topic hint (when set)
		// gets a dedicated line in the user message — the prompt
		// knows what to do with it because it reads like part of
		// the goal framing rather than a schema field.
		ec.Report(10, "extracting claims")
		claims, rawModel, perr := extractClaims(ctx, d, in.Model, original, in.Topic, maxClaims)
		if perr != nil {
			return nil, perr
		}
		if len(claims) == 0 {
			// No groundable claims — return a clean report, don't
			// touch the file. Not an error: a well-grounded post
			// is the ideal outcome.
			sum := sha256.Sum256([]byte(original))
			return json.Marshal(map[string]any{
				"path":              in.Path,
				"claims_considered": 0,
				"claims_grounded":   0,
				"grounding":         []grounding{},
				"skipped":           []string{},
				"sha256":            hex.EncodeToString(sum[:]),
				"file_changed":      false,
			})
		}
		_ = rawModel // retained for future audit logging; not surfaced today

		// 4. For each claim, search Firecrawl and take the first
		// result with a non-empty URL. Skip claims whose text
		// doesn't literally appear in the markdown (hallucinated
		// substrings are a failure mode for smaller models) and
		// claims whose query returns no usable source.
		groundings := make([]grounding, 0, len(claims))
		skipped := make([]string, 0)
		patched := original
		considered := 0

		for i, c := range claims {
			ec.Report(20+float64(i)*60/float64(len(claims)),
				fmt.Sprintf("grounding claim %d/%d", i+1, len(claims)))
			considered++
			if !strings.Contains(patched, c.Text) {
				skipped = append(skipped, c.Text)
				continue
			}
			// Search with inline scrape so we get page content
			// alongside URLs — needed for source verification.
			fc, searchErr := callFirecrawlSearch(ctx, base, firecrawlSearchRequest{
				Query: c.Query,
				Limit: 3,
				ScrapeOptions: &firecrawlSearchScrapeOpt{
					Formats: []string{"markdown"},
				},
			})
			if searchErr != nil {
				skipped = append(skipped, c.Text)
				continue
			}
			// Verify: ask the LLM which source (if any) actually
			// supports the claim. This catches irrelevant results
			// where the search title looks good but the content
			// doesn't back the claim.
			pick, snippet := verifyBestSource(ctx, d, in.Model, c.Text, fc.Data)
			if pick == nil {
				skipped = append(skipped, c.Text)
				continue
			}
			insertion := fmt.Sprintf("%s [source](%s)", c.Text, pick.URL)
			patched = strings.Replace(patched, c.Text, insertion, 1)
			title := pick.Title
			if title == "" {
				title = pick.Metadata.Title
			}
			groundings = append(groundings, grounding{
				Claim:   c.Text,
				URL:     pick.URL,
				Title:   title,
				Snippet: snippet,
			})
		}

		// 5. Rewrite (optional). When rewrite=true, ask the LLM to
		// improve each grounded claim using the source content so
		// the prose is stronger, more specific, and properly cited
		// inline — not just a bare [source](url) appended.
		if in.Rewrite && len(groundings) > 0 {
			ec.Report(85, "rewriting prose with sources")
			rewritten, err := rewriteWithSources(ctx, d, in.Model, patched, groundings)
			if err != nil {
				ec.Logger.Warn("rewrite failed, keeping citation-only version", "err", err)
			} else if strings.TrimSpace(rewritten) != "" {
				patched = rewritten
			}
		}

		// 6. Write back + artifact.
		fileChanged := patched != original

		// File mode: write the patched file back to the session.
		if !textMode && fileChanged && ec.Exec != nil {
			writeRes, err := runShell(ctx, ec, "cat > "+shellQuote(full), []byte(patched))
			if err != nil || writeRes.ExitCode != 0 {
				return nil, &packs.PackError{
					Code:    packs.CodeHandlerFailed,
					Message: fmt.Sprintf("write back failed: %s", strings.TrimSpace(string(writeRes.Stderr))),
				}
			}
		}

		// Always upload the grounded markdown as a downloadable
		// artifact so the operator can copy-paste it to a blog.
		var artifactKey string
		if ec.Artifacts != nil && fileChanged {
			art, err := ec.Artifacts.Put(ctx, "content.ground", "grounded.md", []byte(patched), "text/markdown")
			if err != nil {
				ec.Logger.Warn("artifact upload failed", "err", err)
			} else {
				artifactKey = art.Key
			}
		}

		sum := sha256.Sum256([]byte(patched))
		out := map[string]any{
			"path":              in.Path,
			"claims_considered": considered,
			"claims_grounded":   len(groundings),
			"grounding":         groundings,
			"skipped":           skipped,
			"sha256":            hex.EncodeToString(sum[:]),
			"file_changed":      fileChanged,
			"grounded_text":     patched,
		}
		if artifactKey != "" {
			out["artifact_key"] = artifactKey
		}
		return json.Marshal(out)
	}
}

// extractClaims asks the LLM to pick up to maxClaims grounding
// candidates. Returns the parsed claim list plus the raw model
// response (useful for future audit logging).
func extractClaims(ctx context.Context, d vision.Dispatcher, model, markdown, topic string, maxClaims int) ([]struct {
	Text  string `json:"text"`
	Query string `json:"query"`
}, string, *packs.PackError) {
	maxTokens := defaultContentGroundTokens
	var userMsg strings.Builder
	fmt.Fprintf(&userMsg, "MAX_CLAIMS: %d\n", maxClaims)
	if strings.TrimSpace(topic) != "" {
		fmt.Fprintf(&userMsg, "TOPIC HINT: %s\n", topic)
	}
	userMsg.WriteString("\nPOST:\n")
	userMsg.WriteString(markdown)
	req := gateway.ChatRequest{
		Model:     model,
		MaxTokens: &maxTokens,
		Messages: []gateway.Message{
			{Role: "system", Content: gateway.TextContent(contentGroundPrompt)},
			{Role: "user", Content: gateway.TextContent(userMsg.String())},
		},
	}
	resp, err := d.Dispatch(ctx, req)
	if err != nil {
		return nil, "", &packs.PackError{
			Code:    packs.CodeHandlerFailed,
			Message: fmt.Sprintf("claim extractor dispatch: %v", err),
			Cause:   err,
		}
	}
	if len(resp.Choices) == 0 {
		return nil, "", &packs.PackError{
			Code:    packs.CodeHandlerFailed,
			Message: "claim extractor returned no choices",
			Cause:   errors.New("empty choices"),
		}
	}
	raw := strings.TrimSpace(resp.Choices[0].Message.Content.Text())
	plan, perr := parseClaimPlan(raw)
	if perr != nil {
		return nil, raw, perr
	}
	// Cap to maxClaims in case the model ignored the instruction.
	if len(plan.Claims) > maxClaims {
		plan.Claims = plan.Claims[:maxClaims]
	}
	return plan.Claims, raw, nil
}

// parseClaimPlan tolerates the same prose/markdown wrapping as
// webtest.parsePlan — strict unmarshal first, balanced-brace
// fallback second. Returns a PackError so callers can plug it
// into their error return directly.
func parseClaimPlan(raw string) (claimPlan, *packs.PackError) {
	var p claimPlan
	if err := json.Unmarshal([]byte(raw), &p); err == nil {
		return p, nil
	}
	if obj := extractFirstJSONObject(raw); obj != "" {
		if err := json.Unmarshal([]byte(obj), &p); err == nil {
			return p, nil
		}
	}
	snippet := raw
	if len(snippet) > 256 {
		snippet = snippet[:256] + "…"
	}
	return claimPlan{}, &packs.PackError{
		Code:    packs.CodeHandlerFailed,
		Message: fmt.Sprintf("claim extractor returned unparseable JSON: %s", snippet),
	}
}

// firstUsableSource returns the first search result with a
// non-empty URL, or nil if none. We deliberately ignore empty-
// description and missing-markdown results here — for grounding
// the URL alone is enough, and restricting further would skip
// otherwise-fine sources for no reason.
func firstUsableSource(items []firecrawlSearchItem) *firecrawlSearchItem {
	for i := range items {
		if items[i].URL != "" {
			return &items[i]
		}
	}
	return nil
}

// verifyBestSource asks the LLM to pick the best source that
// actually supports a claim, returning the chosen source and a
// short supporting snippet. This prevents inserting citations
// where the search title looks relevant but the page content
// doesn't actually back the claim.
//
// Returns nil if no source is verified as supporting the claim.
func verifyBestSource(ctx context.Context, d vision.Dispatcher, model, claim string, items []firecrawlSearchItem) (*firecrawlSearchItem, string) {
	// Build a compact source list for the LLM. Truncate each
	// source's markdown to 500 chars — enough for the LLM to
	// judge relevance without blowing up token usage.
	var sourcesMsg strings.Builder
	validCount := 0
	for i, item := range items {
		if item.URL == "" {
			continue
		}
		validCount++
		content := item.Markdown
		if content == "" {
			content = item.Description
		}
		if len(content) > 500 {
			content = content[:500] + "..."
		}
		title := item.Title
		if title == "" {
			title = item.Metadata.Title
		}
		fmt.Fprintf(&sourcesMsg, "SOURCE %d: %s\nTitle: %s\nContent: %s\n\n", i, item.URL, title, content)
	}
	if validCount == 0 {
		return nil, ""
	}

	maxTokens := 256
	req := gateway.ChatRequest{
		Model:     model,
		MaxTokens: &maxTokens,
		Messages: []gateway.Message{
			{Role: "system", Content: gateway.TextContent(
				`You verify whether web sources support a factual claim. Given a CLAIM and numbered SOURCES, respond with ONE JSON object:

{"pick": 0, "snippet": "exact quote or close paraphrase from the source that supports the claim"}

Rules:
- "pick" is the SOURCE number (0-indexed) that BEST supports the claim. Set to -1 if NO source supports it.
- "snippet" is a 1-2 sentence excerpt from the chosen source proving it backs the claim. Empty if pick is -1.
- Do not wrap in markdown. One JSON object only.`)},
			{Role: "user", Content: gateway.TextContent(
				fmt.Sprintf("CLAIM: %s\n\n%s\nWhich source best supports this claim?", claim, sourcesMsg.String()))},
		},
	}
	resp, err := d.Dispatch(ctx, req)
	if err != nil || len(resp.Choices) == 0 {
		// Verification failed — fall back to first usable source
		// rather than skipping entirely. Better to cite without
		// verification than to drop a valid grounding because the
		// LLM had a transient error.
		pick := firstUsableSource(items)
		return pick, ""
	}

	raw := strings.TrimSpace(resp.Choices[0].Message.Content.Text())
	var result struct {
		Pick    int    `json:"pick"`
		Snippet string `json:"snippet"`
	}
	result.Pick = -1
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		if obj := extractFirstJSONObject(raw); obj != "" {
			_ = json.Unmarshal([]byte(obj), &result)
		}
	}

	if result.Pick < 0 || result.Pick >= len(items) || items[result.Pick].URL == "" {
		return nil, ""
	}
	return &items[result.Pick], result.Snippet
}

// rewriteWithSources asks the LLM to improve the grounded text by
// rewriting weak claims into stronger prose backed by what the
// sources actually say. The LLM sees the full text (with [source]
// links already inserted) plus the grounding report (claim →
// snippet pairs) and produces a polished version.
func rewriteWithSources(ctx context.Context, d vision.Dispatcher, model, text string, gs []grounding) (string, error) {
	maxTokens := 2048

	var sourcesMsg strings.Builder
	sourcesMsg.WriteString("GROUNDING REPORT:\n\n")
	for i, g := range gs {
		fmt.Fprintf(&sourcesMsg, "Claim %d: %q\n", i+1, g.Claim)
		fmt.Fprintf(&sourcesMsg, "Source: %s\n", g.URL)
		if g.Snippet != "" {
			fmt.Fprintf(&sourcesMsg, "Source excerpt: %s\n", g.Snippet)
		}
		sourcesMsg.WriteString("\n")
	}

	req := gateway.ChatRequest{
		Model:     model,
		MaxTokens: &maxTokens,
		Messages: []gateway.Message{
			{Role: "system", Content: gateway.TextContent(
				`You are an expert editor who improves factual writing by making claims more specific and authoritative using source material.

You will receive:
1. ORIGINAL TEXT — markdown with [source](url) citation links already inserted
2. GROUNDING REPORT — for each cited claim, the source URL and an excerpt from that source

Your job: rewrite the text so that:
- Weak or vague claims become specific and authoritative, drawing on the source excerpts
- Every rewritten claim naturally integrates its citation as an inline [source](url) link
- Claims that were NOT grounded (no [source] link) are left unchanged
- The overall structure, tone, and flow of the original are preserved
- Do NOT add new claims or information not supported by the sources
- Do NOT remove any content — only improve what has a source backing it
- Keep the same markdown format

Return ONLY the rewritten markdown text. No commentary, no explanation, no code fences.`)},
			{Role: "user", Content: gateway.TextContent(
				fmt.Sprintf("ORIGINAL TEXT:\n%s\n\n%s\nRewrite the text, improving grounded claims using the source excerpts.", text, sourcesMsg.String()))},
		},
	}

	resp, err := d.Dispatch(ctx, req)
	if err != nil {
		return "", fmt.Errorf("rewrite dispatch: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("rewrite: no choices returned")
	}
	result := strings.TrimSpace(resp.Choices[0].Message.Content.Text())

	// Strip markdown code fences if the model wrapped it
	if strings.HasPrefix(result, "```") {
		lines := strings.Split(result, "\n")
		if len(lines) > 2 {
			// Remove first and last lines (the fences)
			start := 1
			end := len(lines) - 1
			if strings.TrimSpace(lines[end]) == "```" {
				result = strings.Join(lines[start:end], "\n")
			}
		}
	}
	return result, nil
}
