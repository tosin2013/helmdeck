package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/tosin2013/helmdeck/internal/cdp"
	"github.com/tosin2013/helmdeck/internal/packs"
)

// ScrapeSPA is the second reference pack referenced by ADR 017. It
// navigates to a SPA URL and runs a caller-supplied map of CSS
// selectors against the rendered DOM, returning whatever it could
// extract along with the list of fields that failed.
//
// Partial-result handling matters here because SPAs are flaky: a
// single missing selector should not blow up an otherwise-useful
// scrape. The pack succeeds whenever at least one field resolves;
// the caller decides whether the missing list is acceptable. Total
// failure (zero fields resolved) is surfaced as CodeHandlerFailed
// so retries make sense.
//
// Input shape:
//
//	{
//	  "url": "https://app.example.com",
//	  "fields": {
//	    "title": {"selector": "h1", "format": "text"},
//	    "body":  {"selector": "article", "format": "html"}
//	  },
//	  "wait_ms": 500   // optional, defaults to 0
//	}
//
// Output shape:
//
//	{
//	  "url": "...",
//	  "data": { "title": "...", "body": "..." },
//	  "missing": ["body"]   // always present (possibly empty)
//	}
func ScrapeSPA() *packs.Pack {
	return &packs.Pack{
		Name:        "web.scrape_spa",
		Version:     "v1",
		Description: "Render a SPA and extract fields by CSS selector with partial-result handling.",
		NeedsSession: true,
		// BasicSchema can only enforce top-level shape; the per-field
		// validation happens inside the handler since the fields map
		// is open-ended (any caller-chosen keys).
		InputSchema: packs.BasicSchema{
			Required: []string{"url", "fields"},
			Properties: map[string]string{
				"url":     "string",
				"fields":  "object",
				"wait_ms": "number",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"url", "data", "missing"},
			Properties: map[string]string{
				"url":     "string",
				"data":    "object",
				"missing": "array",
			},
		},
		Handler: scrapeSPAHandler,
	}
}

type scrapeFieldSpec struct {
	Selector string `json:"selector"`
	Format   string `json:"format"`
}

type scrapeInput struct {
	URL    string                     `json:"url"`
	Fields map[string]scrapeFieldSpec `json:"fields"`
	WaitMS int                        `json:"wait_ms"`
}

func scrapeSPAHandler(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
	var in scrapeInput
	if err := json.Unmarshal(ec.Input, &in); err != nil {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
	}
	if len(in.Fields) == 0 {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "fields map must not be empty"}
	}
	for name, spec := range in.Fields {
		if spec.Selector == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: fmt.Sprintf("field %q: selector required", name)}
		}
	}
	if ec.CDP == nil {
		return nil, &packs.PackError{Code: packs.CodeSessionUnavailable, Message: "engine has no CDP factory"}
	}
	if err := ec.CDP.Navigate(ctx, in.URL); err != nil {
		return nil, fmt.Errorf("navigate %s: %w", in.URL, err)
	}
	if in.WaitMS > 0 {
		// Caller-supplied settle delay. Some SPAs hydrate after the
		// initial nav event; this is the simplest knob that works
		// without baking a per-app readiness heuristic into the pack.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(in.WaitMS) * time.Millisecond):
		}
	}

	data := make(map[string]string, len(in.Fields))
	missing := make([]string, 0)
	for name, spec := range in.Fields {
		format := cdp.FormatText
		if spec.Format == "html" {
			format = cdp.FormatHTML
		}
		val, err := ec.CDP.Extract(ctx, spec.Selector, format)
		if err != nil {
			ec.Logger.Debug("scrape field missing", "field", name, "selector", spec.Selector, "err", err)
			missing = append(missing, name)
			continue
		}
		data[name] = val
	}

	if len(data) == 0 {
		// Total failure: every selector missed. Return a typed error
		// instead of an empty data object so callers can retry against
		// a different selector set without re-parsing the response.
		return nil, &packs.PackError{
			Code:    packs.CodeHandlerFailed,
			Message: fmt.Sprintf("no fields extracted from %s (all %d selectors missed)", in.URL, len(in.Fields)),
		}
	}

	// Sort the missing list for stable output — tests and audit
	// diffs depend on deterministic field ordering.
	sort.Strings(missing)

	return json.Marshal(map[string]any{
		"url":     in.URL,
		"data":    data,
		"missing": missing,
	})
}
