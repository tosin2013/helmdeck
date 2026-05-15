// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// stock_search.go (#217) — stock photography search.
//
// Pack name: `stock.search`
//
// Searches Pexels (day 1) for photos matching a query, downloads up
// to 4 results into the helmdeck artifact store, and returns artifact
// keys + attribution metadata. The downloaded artifacts chain into
// every content pack (slides.render/slides.narrate, podcast.generate,
// blog.publish, hyperframes.render) via the existing *_artifact_key
// chained inputs — `stock.search` and `image.generate` produce the
// same contract; downstream packs don't care which source the agent
// picked.
//
// Engine-pluggable from day 1: the input has an `engine` field that
// only accepts "pexels" today; Unsplash/Pixabay land later as new
// engines without renaming the pack or breaking caller schemas
// (same pattern as image.generate's `engine: "fal"`).
//
// Credential: `pexels-key` (vault) or `HELMDECK_PEXELS_API_KEY`
// (env-var fallback). Same #138 resolution ladder as the other
// API-keyed packs.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/security"
	"github.com/tosin2013/helmdeck/internal/vault"
)

const (
	stockSearchDefaultEngine   = "pexels"
	stockSearchPexelsCredName  = "pexels-key"
	stockSearchPexelsEnvVar    = "HELMDECK_PEXELS_API_KEY"
	stockSearchHTTPTimeout     = 30 * time.Second
	stockSearchMaxResponseSize = 4 << 20  // 4 MiB cap on Pexels JSON response
	stockSearchMaxImageBytes   = 32 << 20 // 32 MiB per downloaded image
)

// PexelsBaseURL is the API host. Exported as a var (not const) so
// tests can redirect to an httptest stub without threading a client
// through every call site — same pattern as ImageGenFalBaseURL.
var PexelsBaseURL = "https://api.pexels.com/v1"

// StockSearch constructs the pack.
func StockSearch(v *vault.Store, eg *security.EgressGuard) *packs.Pack {
	return &packs.Pack{
		Name:        "stock.search",
		Version:     "v1",
		Description: "Search Pexels for stock photos matching a query and download the top results into the artifact store. Returns artifact keys that chain into slides.render / slides.narrate / blog.publish / podcast.generate / hyperframes.render the same way image.generate's output does. Engine-pluggable: day 1 ships 'pexels'; Unsplash/Pixabay follow.",
		InputSchema: packs.BasicSchema{
			Required: []string{"query"},
			Properties: map[string]string{
				"query":       "string",
				"engine":      "string", // "pexels" (default); future: "unsplash", "pixabay"
				"count":       "number", // default 1, capped at 4 (mirrors image.generate)
				"orientation": "string", // "landscape" | "portrait" | "square"
				"size":        "string", // "large" | "medium" | "small" (minimum-size filter)
				"color":       "string", // hex like "#ff0000" or named ("red")
				"media_type":  "string", // "photo" (default); "video" is a follow-up PR
				"credential":  "string", // optional explicit vault name override
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"engine", "artifact_keys", "results", "query_used"},
			Properties: map[string]string{
				"engine":        "string",
				"artifact_keys": "array",
				"results":       "array",
				"query_used":    "string",
			},
		},
		Handler: stockSearchHandler(v, eg),
	}
}

type stockSearchInput struct {
	Query       string `json:"query"`
	Engine      string `json:"engine"`
	Count       int    `json:"count"`
	Orientation string `json:"orientation"`
	Size        string `json:"size"`
	Color       string `json:"color"`
	MediaType   string `json:"media_type"`
	Credential  string `json:"credential"`
}

// StockSearchResult is one item in the response's `results` array.
// Exported so future chained callers (a follow-up "auto-credit
// slides" pack, say) can typed-walk the attribution metadata.
type StockSearchResult struct {
	ID              int64  `json:"id"`
	Photographer    string `json:"photographer"`
	PhotographerURL string `json:"photographer_url"`
	SourceURL       string `json:"source_url"`
	Width           int    `json:"width"`
	Height          int    `json:"height"`
	AltText         string `json:"alt_text,omitempty"`
	ArtifactKey     string `json:"artifact_key"`
}

func stockSearchHandler(v *vault.Store, eg *security.EgressGuard) packs.HandlerFunc {
	return func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
		var in stockSearchInput
		if err := json.Unmarshal(ec.Input, &in); err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
		}
		if strings.TrimSpace(in.Query) == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "query is required"}
		}
		engine := in.Engine
		if engine == "" {
			engine = stockSearchDefaultEngine
		}
		if engine != "pexels" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: fmt.Sprintf(`engine must be "pexels" (got %q); other engines (unsplash, pixabay) ship in future PRs`, engine)}
		}
		count := in.Count
		if count == 0 {
			count = 1
		}
		if count < 1 || count > 4 {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: "count must be between 1 and 4"}
		}
		// Validate filter values — reject typos at the pack boundary
		// rather than letting Pexels silently ignore them.
		if in.Orientation != "" && !isValidOrientation(in.Orientation) {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: fmt.Sprintf(`orientation must be "landscape" | "portrait" | "square" (got %q)`, in.Orientation)}
		}
		if in.Size != "" && !isValidSize(in.Size) {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: fmt.Sprintf(`size must be "large" | "medium" | "small" (got %q)`, in.Size)}
		}
		mediaType := in.MediaType
		if mediaType == "" {
			mediaType = "photo"
		}
		if mediaType != "photo" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: fmt.Sprintf(`media_type must be "photo" (got %q); "video" support is a follow-up PR`, mediaType)}
		}
		if ec.Artifacts == nil {
			return nil, &packs.PackError{Code: packs.CodeInternal,
				Message: "stock.search requires an artifact store"}
		}

		apiKey := resolvePexelsKey(ctx, v, in.Credential)
		if apiKey == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: "pexels key not found. Set HELMDECK_PEXELS_API_KEY in deploy/compose/.env.local, or POST a credential named 'pexels-key' to /api/v1/vault/credentials. Get a free key at https://www.pexels.com/api/."}
		}

		ec.Report(20, fmt.Sprintf("searching pexels for %q", in.Query))
		photos, err := pexelsSearch(ctx, eg, apiKey, in.Query, count, in.Orientation, in.Size, in.Color)
		if err != nil {
			return nil, err
		}
		if len(photos) == 0 {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("pexels returned no results for query %q. Try a broader query or remove filters.", in.Query)}
		}

		ec.Report(50, fmt.Sprintf("downloading %d photo(s)", len(photos)))
		results := make([]StockSearchResult, 0, len(photos))
		artKeys := make([]string, 0, len(photos))
		for i, p := range photos {
			ec.Report(50+float64(i)*40/float64(len(photos)),
				fmt.Sprintf("downloading photo %d/%d", i+1, len(photos)))
			downloadURL := p.bestDownloadURL()
			imgBytes, ct, derr := pexelsDownload(ctx, eg, downloadURL)
			if derr != nil {
				return nil, derr
			}
			if len(imgBytes) > stockSearchMaxImageBytes {
				return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
					Message: fmt.Sprintf("photo %d exceeds %d MiB cap (%d bytes); pick a smaller size or request fewer results",
						i, stockSearchMaxImageBytes>>20, len(imgBytes))}
			}
			ext := contentTypeToExt(ct)
			art, perr := ec.Artifacts.Put(ctx, ec.Pack.Name,
				fmt.Sprintf("photo-%03d.%s", i, ext), imgBytes, ct)
			if perr != nil {
				return nil, &packs.PackError{Code: packs.CodeArtifactFailed,
					Message: perr.Error(), Cause: perr}
			}
			artKeys = append(artKeys, art.Key)
			results = append(results, StockSearchResult{
				ID:              p.ID,
				Photographer:    p.Photographer,
				PhotographerURL: p.PhotographerURL,
				SourceURL:       p.URL,
				Width:           p.Width,
				Height:          p.Height,
				AltText:         p.Alt,
				ArtifactKey:     art.Key,
			})
		}

		ec.Report(95, "stock.search complete")
		out := map[string]any{
			"engine":        engine,
			"artifact_keys": artKeys,
			"results":       results,
			"query_used":    in.Query,
		}
		return json.Marshal(out)
	}
}

// --- Pexels HTTP client ------------------------------------------------

// pexelsPhoto is the subset of Pexels's photo response we consume.
type pexelsPhoto struct {
	ID              int64           `json:"id"`
	Width           int             `json:"width"`
	Height          int             `json:"height"`
	URL             string          `json:"url"`
	Photographer    string          `json:"photographer"`
	PhotographerURL string          `json:"photographer_url"`
	Src             pexelsPhotoSrc  `json:"src"`
	Alt             string          `json:"alt"`
}

// pexelsPhotoSrc is Pexels's per-size CDN URL set. We prefer
// `Large2x` for the artifact download — high enough for hero
// images and slide backgrounds without paying for the full
// `Original` (which can be 10+ MiB).
type pexelsPhotoSrc struct {
	Original  string `json:"original"`
	Large2x   string `json:"large2x"`
	Large     string `json:"large"`
	Medium    string `json:"medium"`
	Small     string `json:"small"`
	Portrait  string `json:"portrait"`
	Landscape string `json:"landscape"`
	Tiny      string `json:"tiny"`
}

// bestDownloadURL picks the size we want to artifact-store. Large2x
// is ~1.5-3 MiB typically, balancing quality vs storage. Falls back
// down the size ladder if the API ever omits a size variant.
func (p *pexelsPhoto) bestDownloadURL() string {
	for _, u := range []string{p.Src.Large2x, p.Src.Large, p.Src.Original, p.Src.Medium, p.Src.Small} {
		if u != "" {
			return u
		}
	}
	return ""
}

// pexelsSearchResponse mirrors the Pexels search-endpoint envelope.
type pexelsSearchResponse struct {
	Photos     []pexelsPhoto `json:"photos"`
	TotalCount int           `json:"total_results"`
}

// pexelsSearch hits the search endpoint and returns the photos slice
// or a PackError ready to bubble up. The caller is responsible for
// the count cap — Pexels accepts up to 80 per_page but we already
// validated count ≤ 4 in the handler.
func pexelsSearch(ctx context.Context, eg *security.EgressGuard, apiKey, query string, count int, orientation, size, color string) ([]pexelsPhoto, *packs.PackError) {
	endpoint := strings.TrimRight(PexelsBaseURL, "/") + "/search"
	q := url.Values{}
	q.Set("query", query)
	q.Set("per_page", fmt.Sprintf("%d", count))
	if orientation != "" {
		q.Set("orientation", orientation)
	}
	if size != "" {
		q.Set("size", size)
	}
	if color != "" {
		q.Set("color", color)
	}
	fullURL := endpoint + "?" + q.Encode()

	if eg != nil {
		if err := eg.CheckURL(ctx, fullURL); err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: fmt.Sprintf("egress denied: %v", err), Cause: err}
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, &packs.PackError{Code: packs.CodeHandlerFailed, Message: err.Error(), Cause: err}
	}
	req.Header.Set("Authorization", apiKey)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: stockSearchHTTPTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("pexels request: %v", err), Cause: err}
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, stockSearchMaxResponseSize))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: fmt.Sprintf("pexels rejected the API key (HTTP %d). Verify the credential is correct and that the Pexels account has not been suspended.", resp.StatusCode)}
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: "pexels rate limit exceeded (HTTP 429). Default tier is 200 req/hour; raise via https://www.pexels.com/api/ or wait for the window to roll over."}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("pexels %d: %s", resp.StatusCode, truncStr(string(body), 512))}
	}

	var parsed pexelsSearchResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("parse pexels response: %v", err), Cause: err}
	}
	return parsed.Photos, nil
}

// pexelsDownload fetches a single photo's CDN URL and returns the
// bytes + content type. CDN downloads don't require the API key
// (they're served from images.pexels.com) but we still pipe through
// the egress guard.
func pexelsDownload(ctx context.Context, eg *security.EgressGuard, photoURL string) ([]byte, string, *packs.PackError) {
	if eg != nil {
		if err := eg.CheckURL(ctx, photoURL); err != nil {
			return nil, "", &packs.PackError{Code: packs.CodeInvalidInput,
				Message: fmt.Sprintf("egress denied: %v", err), Cause: err}
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, photoURL, nil)
	if err != nil {
		return nil, "", &packs.PackError{Code: packs.CodeHandlerFailed, Message: err.Error(), Cause: err}
	}
	client := &http.Client{Timeout: stockSearchHTTPTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("photo download: %v", err), Cause: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("photo download HTTP %d", resp.StatusCode)}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, stockSearchMaxImageBytes+1))
	if err != nil {
		return nil, "", &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("read photo body: %v", err), Cause: err}
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "image/jpeg"
	}
	return body, ct, nil
}

// --- credential resolution + helpers ----------------------------------

// resolvePexelsKey walks the credential ladder: explicit override →
// vault `pexels-key` → env `HELMDECK_PEXELS_API_KEY`. Returns "" when
// none resolve so the caller can surface the actionable error.
func resolvePexelsKey(ctx context.Context, v *vault.Store, explicit string) string {
	if v != nil && explicit != "" {
		if res, err := v.ResolveByName(ctx, vault.Actor{Subject: "*"}, explicit); err == nil {
			return string(res.Plaintext)
		}
	}
	if v != nil {
		if res, err := v.ResolveByName(ctx, vault.Actor{Subject: "*"}, stockSearchPexelsCredName); err == nil {
			return string(res.Plaintext)
		}
	}
	if k := os.Getenv(stockSearchPexelsEnvVar); k != "" {
		return k
	}
	return ""
}

func isValidOrientation(o string) bool {
	switch o {
	case "landscape", "portrait", "square":
		return true
	}
	return false
}

func isValidSize(s string) bool {
	switch s {
	case "large", "medium", "small":
		return true
	}
	return false
}

// truncStr/contentTypeToExt are shared with other packs — already
// defined in image_generate.go / slides_render.go. Reusing those.
var _ = bytes.NewReader // keep import in case helper functions move
