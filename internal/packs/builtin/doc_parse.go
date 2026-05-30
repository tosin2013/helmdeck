// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// doc_parse.go (T807c, ADR 035) — Docling-backed `doc.parse` pack.
//
// This is the layout-aware, multi-format document understanding
// counterpart to `doc.ocr`. Where `doc.ocr` is "one image in,
// Tesseract text out", `doc.parse` handles real documents: PDFs
// with tables and figures, DOCX/PPTX/XLSX exports, HTML, and
// images — all the way to clean markdown with preserved structure.
// The Docling service does the heavy lifting; this pack is a thin
// wrapper that enforces helmdeck's security model (egress guard on
// the target URL, env-var gate, bounded response size) and translates
// the Docling response into helmdeck's pack output schema.
//
// Deployment: Docling runs as an optional compose service (see
// deploy/compose/compose.docling.yml). When HELMDECK_DOCLING_ENABLED
// is not "true", the pack returns a typed CodeInvalidInput error
// pointing the operator at the exact toggle. Same pattern as
// web.scrape — the pack is registered unconditionally so agents
// discovering the catalog see it and get an actionable error instead
// of silent absence.
//
// Input shape:
//
//	{
//	  "source_url": "https://arxiv.org/pdf/2501.17887",  // either this …
//	  "source_b64": "JVBERi0xLjQK...",                    // … or this
//	  "filename":   "paper.pdf",                           // required when source_b64 is set
//	  "formats":    ["md", "text"],                        // optional; default ["md"]
//	  "do_ocr":     true,                                  // optional; default true
//	  "ocr_lang":   ["en"]                                 // optional
//	}
//
// Output shape:
//
//	{
//	  "source":          "...",                            // url or filename
//	  "markdown":        "# Paper title\n\n...",
//	  "text":            "Paper title ...",                // only if requested
//	  "html":            "<h1>...",                        // only if requested
//	  "status":          "success",                        // Docling status
//	  "processing_time": 4.2
//	}

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/security"
)

// docParseAllowedExtensions is the closed set of file extensions
// doc.parse accepts on a source_url. Anything else (notably web pages
// like https://medium.com/... and arxiv abstract URLs that don't have
// a .pdf suffix) is rejected at input validation with a caller_fixable
// error that routes the agent to web.scrape instead — so a wrong-tool
// mismatch fails fast at the pack boundary rather than as a cryptic
// docling 4xx after a wasted network round trip.
//
// Extensions cover what docling-serve can actually parse via its input
// backends: PDF, common office docs, and image OCR. HTML is
// deliberately omitted; web pages route to web.scrape (which is also
// Firecrawl-backed and handles anti-scrape defenses doc.parse cannot).
// Extensionless URLs that *do* host a document (arxiv /abs/, content-
// negotiating landing pages) must be downloaded first and passed via
// source_b64 + filename, which carries the type explicitly.
var docParseAllowedExtensions = map[string]struct{}{
	".pdf":  {},
	".docx": {},
	".pptx": {},
	".xlsx": {},
	".odt":  {},
	".ods":  {},
	".odp":  {},
	".png":  {},
	".jpg":  {},
	".jpeg": {},
	".tif":  {},
	".tiff": {},
}

// validateDocParseURL enforces docParseAllowedExtensions on a source URL
// at input-validation time. The path is parsed and its lowercased
// extension is matched against the allowlist; anything else returns a
// caller_fixable error that says what the pack accepts and where to
// route web pages instead. Empty URL is a no-op (the caller already
// validated source presence).
func validateDocParseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return &packs.PackError{Code: packs.CodeInvalidInput,
			Message: fmt.Sprintf("source_url is not a valid URL: %v", err), Cause: err}
	}
	if u.Scheme == "" || u.Host == "" {
		return &packs.PackError{Code: packs.CodeInvalidInput,
			Message: "source_url must be an absolute URL (scheme + host)"}
	}
	ext := strings.ToLower(path.Ext(u.Path))
	// path.Ext treats anything after the last "." as the extension, so
	// an arxiv-style URL "/abs/1706.03762" returns ".03762". Numeric-
	// only "extensions" are a version-number identifier, not a file
	// type — collapse them to "no extension" so the rejection message
	// reads naturally.
	if ext != "" && isAllDigits(ext[1:]) {
		ext = ""
	}
	if _, ok := docParseAllowedExtensions[ext]; ok {
		return nil
	}
	hint := "doc.parse handles document files (.pdf, .docx, .pptx, .xlsx, .odt/.ods/.odp, .png/.jpg/.tiff). " +
		"For web pages, use web.scrape (it handles anti-scrape defenses doc.parse cannot). " +
		"For URLs without a document extension (e.g. an arxiv abstract page), download the file first and pass it via source_b64 + filename."
	if ext == "" {
		return &packs.PackError{Code: packs.CodeInvalidInput,
			Message: fmt.Sprintf("source_url %q has no file extension. %s", raw, hint)}
	}
	return &packs.PackError{Code: packs.CodeInvalidInput,
		Message: fmt.Sprintf("source_url extension %q is not a supported document type. %s", ext, hint)}
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

const (
	// defaultDoclingURL matches the service name in
	// deploy/compose/compose.docling.yml. Overridable via
	// HELMDECK_DOCLING_URL for operators running Docling on a
	// different host.
	defaultDoclingURL = "http://helmdeck-docling:5001"
	// doclingTimeout is generous because real documents take time
	// — a 100-page scanned PDF with OCR can burn 60+ seconds on
	// the Docling side. Shorter than firecrawlTimeout would bite
	// operators the first time they feed doc.parse a real paper.
	doclingTimeout = 300 * time.Second
	// maxDoclingResponse caps the Docling JSON response size.
	// Extracted markdown for large PDFs can run several MiB; 16 MiB
	// covers the realistic upper end without letting a broken run
	// balloon the control plane's memory.
	maxDoclingResponse = 16 << 20
	// maxDoclingRequestBytes caps the base64-decoded payload we
	// forward for file sources. Matches the 32 MiB ceiling doc.ocr
	// uses for image uploads.
	maxDoclingRequestBytes = 32 << 20
)

// DocParse constructs the pack. The env-var gate is resolved
// per-call inside the handler so operators can flip the toggle
// without restarting the control plane once hot-reload config
// lands. The egress guard is only consulted for http sources —
// file sources are never passed to the public internet, so the
// SSRF threat model doesn't apply to them.
func DocParse(eg *security.EgressGuard) *packs.Pack {
	return &packs.Pack{
		Name:        "doc.parse",
		Version:     "v1",
		Description: "Parse a document file to clean markdown via Docling. Layout-aware, table-aware. Accepts source_url ending in .pdf, .docx, .pptx, .xlsx, .odt/.ods/.odp, or an image extension (.png, .jpg, .tiff); web-page URLs and extensionless URLs are rejected — for web pages use web.scrape. Alternatively pass source_b64 + filename for any file you already have in memory.",
		Metadata: packs.PackMetadata{
			Accepts:        []string{"pdf", "docx", "pptx", "xlsx", "odt", "ods", "odp", "image", "source_url", "source_b64"},
			Produces:       []string{"markdown"},
			IntentKeywords: []string{"parse pdf", "extract from document", "ocr document", "convert docx to markdown"},
			TypicalUse:     "Source pack for document-shaped content. Chain into blog.rewrite_for_audience or slides.outline downstream.",
			Limitations:    []string{"web-page URLs rejected at input validation (use web.scrape instead)", "extension-less URLs rejected (download first and pass source_b64 + filename)", "requires HELMDECK_DOCLING_ENABLED + the Docling overlay"},
		},
		InputSchema: packs.BasicSchema{
			Properties: map[string]string{
				"source_url": "string",
				"source_b64": "string",
				"filename":   "string",
				"formats":    "array",
				"do_ocr":     "boolean",
				"ocr_lang":   "array",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"source", "markdown", "status"},
			Properties: map[string]string{
				"source":          "string",
				"markdown":        "string",
				"text":            "string",
				"html":            "string",
				"status":          "string",
				"processing_time": "number",
			},
		},
		Handler: docParseHandler(eg),
	}
}

type docParseInput struct {
	SourceURL string   `json:"source_url"`
	SourceB64 string   `json:"source_b64"`
	Filename  string   `json:"filename"`
	Formats   []string `json:"formats"`
	// DoOCR is a pointer so we can distinguish "unset" (default true
	// — matches Docling's own default) from "explicitly false".
	// json.Unmarshal leaves it nil when the field is absent.
	DoOCR   *bool    `json:"do_ocr"`
	OCRLang []string `json:"ocr_lang"`
}

// doclingHTTPSource is the shape Docling expects for an entry in the
// `sources` array. Docling switched from separate `http_sources` /
// `file_sources` arrays to a single discriminated `sources` array
// keyed by `kind` — we set kind=http here so the right input backend
// is picked. Empty `kind` makes the request fail with HTTP 422
// "missing body.sources".
type doclingHTTPSource struct {
	Kind string `json:"kind"` // always "http"
	URL  string `json:"url"`
}

// doclingFileSource is the shape Docling expects inside the
// `sources` array for a base64 blob plus its filename so Docling
// can pick the right input backend by extension. kind="file" tags
// the entry for the discriminator.
type doclingFileSource struct {
	Kind         string `json:"kind"` // always "file"
	Base64String string `json:"base64_string"`
	Filename     string `json:"filename"`
}

// doclingOptions mirrors the subset of /v1/convert/source options
// that helmdeck exposes. The full Docling options bag has 15+
// knobs; keeping this narrow makes the pack's contract easier to
// reason about and lets us expand later without a schema break.
type doclingOptions struct {
	ToFormats       []string `json:"to_formats,omitempty"`
	DoOCR           bool     `json:"do_ocr"`
	OCRLang         []string `json:"ocr_lang,omitempty"`
	ImageExportMode string   `json:"image_export_mode,omitempty"`
	AbortOnError    bool     `json:"abort_on_error"`
}

// doclingRequest is the /v1/convert/source body. As of the upstream
// schema change, every source is an entry in a single `sources` array
// discriminated by its `kind` field (http / file / s3 / track) — there
// is no separate `http_sources` array anymore. json.RawMessage keeps
// the entry types unmixed at the Go level without needing a sealed
// interface.
type doclingRequest struct {
	Options doclingOptions    `json:"options"`
	Sources []json.RawMessage `json:"sources"`
}

// doclingResponse matches the /v1/convert/source response shape.
// Only the fields we propagate are modeled — Docling's full
// response includes timings, errors, and doctags content we
// currently ignore.
type doclingResponse struct {
	Document struct {
		MDContent   string `json:"md_content"`
		TextContent string `json:"text_content"`
		HTMLContent string `json:"html_content"`
	} `json:"document"`
	Status         string   `json:"status"`
	ProcessingTime float64  `json:"processing_time"`
	Errors         []string `json:"errors"`
}

func docParseHandler(eg *security.EgressGuard) packs.HandlerFunc {
	return func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
		// 1. Env-var gate. Operators who have not opted into the
		// Docling overlay get a typed error pointing at the exact
		// config knob — same UX pattern as web.scrape.
		if os.Getenv("HELMDECK_DOCLING_ENABLED") != "true" {
			return nil, &packs.PackError{
				Code: packs.CodeInvalidInput,
				Message: "doc.parse is disabled; set HELMDECK_DOCLING_ENABLED=true " +
					"and bring up the Docling overlay (deploy/compose/compose.docling.yml)",
			}
		}
		base := strings.TrimRight(os.Getenv("HELMDECK_DOCLING_URL"), "/")
		if base == "" {
			base = defaultDoclingURL
		}

		var in docParseInput
		if err := json.Unmarshal(ec.Input, &in); err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
		}
		// Exactly-one-source rule — either a URL or a base64 blob,
		// not both, not neither. Mirrors doc.ocr's contract so
		// agents familiar with one pack can read the other.
		if in.SourceURL == "" && in.SourceB64 == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: "either source_url or source_b64 is required"}
		}
		if in.SourceURL != "" && in.SourceB64 != "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: "set either source_url or source_b64, not both"}
		}
		if in.SourceB64 != "" && strings.TrimSpace(in.Filename) == "" {
			// Docling uses the filename extension to pick the input
			// backend (pdf_backend vs docx_backend vs image_backend).
			// Without a name it can't dispatch, so refuse up front
			// instead of surfacing a confusing upstream error.
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: "filename is required when source_b64 is set (extension picks the parser)"}
		}

		// Source-URL extension allowlist. Web-page URLs (medium.com,
		// blog posts, anything ending in .html) and extensionless
		// URLs are rejected up front with a caller_fixable error that
		// routes the agent to web.scrape — the wrong-tool mismatch
		// fails at the pack boundary instead of as a cryptic docling
		// 4xx after a wasted network round trip. Edge case
		// (arxiv /abs/, content-negotiating landing pages): download
		// the file first and pass it as source_b64 + filename, which
		// carries the type explicitly. See docParseAllowedExtensions
		// for the closed set.
		if in.SourceURL != "" {
			if err := validateDocParseURL(in.SourceURL); err != nil {
				return nil, err
			}
		}

		// Closed-set formats — Docling accepts more, but the pack
		// output schema only models md / text / html. Reject the
		// rest early so agents don't ask for doctags and silently
		// get nothing back.
		formats := in.Formats
		if len(formats) == 0 {
			formats = []string{"md"}
		}
		// Normalize natural-language aliases. Models default to
		// "markdown" / "plaintext" because those are the human-facing
		// names; accept them and map onto Docling's terse internal
		// enum so the LLM doesn't have to internalize a quirk. Issue #91.
		for i, f := range formats {
			switch f {
			case "markdown":
				formats[i] = "md"
			case "plaintext", "plain":
				formats[i] = "text"
			}
		}
		for _, f := range formats {
			switch f {
			case "md", "text", "html":
			default:
				return nil, &packs.PackError{
					Code:    packs.CodeInvalidInput,
					Message: fmt.Sprintf("unsupported format %q; use md (or markdown), text (or plaintext), or html", f),
				}
			}
		}
		// "md" must always be present in the request so the
		// output.markdown field is non-empty — the OutputSchema
		// requires it. Agents asking for only text or html still
		// get markdown (cheap, already computed internally).
		if !containsString(formats, "md") {
			formats = append([]string{"md"}, formats...)
		}

		// 2. Egress guard on the *target* URL for http sources. The
		// Docling service is on the private bridge, but the agent
		// chooses the URL Docling will fetch — without the guard,
		// Docling could be coerced into pulling 169.254.169.254 and
		// returning cloud metadata inside the parsed markdown.
		// Belt and braces — same rationale as web.scrape.
		if in.SourceURL != "" && eg != nil {
			if err := eg.CheckURL(ctx, in.SourceURL); err != nil {
				return nil, &packs.PackError{
					Code:    packs.CodeInvalidInput,
					Message: fmt.Sprintf("egress denied: %v", err),
					Cause:   err,
				}
			}
		}

		// 3. Build the Docling request. do_ocr defaults to true to
		// match Docling's own default, but we still send it
		// explicitly so the wire contract is unambiguous in audit
		// logs.
		doOCR := true
		if in.DoOCR != nil {
			doOCR = *in.DoOCR
		}
		reqBody := doclingRequest{
			Options: doclingOptions{
				ToFormats:       formats,
				DoOCR:           doOCR,
				OCRLang:         in.OCRLang,
				ImageExportMode: "placeholder",
				AbortOnError:    false,
			},
		}
		var sourceLabel string
		if in.SourceURL != "" {
			src, merr := json.Marshal(doclingHTTPSource{Kind: "http", URL: in.SourceURL})
			if merr != nil {
				return nil, &packs.PackError{Code: packs.CodeInternal, Message: merr.Error(), Cause: merr}
			}
			reqBody.Sources = []json.RawMessage{src}
			sourceLabel = in.SourceURL
		} else {
			// Validate the base64 up front so we fail with
			// invalid_input (client bug) instead of handler_failed
			// (server-side). Also enforce the size cap before the
			// bytes leave the pack — forwarding a 500 MB payload
			// only to have Docling reject it wastes the network
			// round trip.
			decoded, err := base64.StdEncoding.DecodeString(in.SourceB64)
			if err != nil {
				return nil, &packs.PackError{Code: packs.CodeInvalidInput,
					Message: "source_b64 is not valid base64", Cause: err}
			}
			if len(decoded) == 0 {
				return nil, &packs.PackError{Code: packs.CodeInvalidInput,
					Message: "source_b64 decodes to empty bytes"}
			}
			if len(decoded) > maxDoclingRequestBytes {
				return nil, &packs.PackError{Code: packs.CodeInvalidInput,
					Message: fmt.Sprintf("source_b64 %d bytes exceeds %d byte cap", len(decoded), maxDoclingRequestBytes)}
			}
			src, merr := json.Marshal(doclingFileSource{
				Kind:         "file",
				Base64String: in.SourceB64,
				Filename:     in.Filename,
			})
			if merr != nil {
				return nil, &packs.PackError{Code: packs.CodeInternal, Message: merr.Error(), Cause: merr}
			}
			reqBody.Sources = []json.RawMessage{src}
			sourceLabel = in.Filename
		}

		bodyBytes, err := json.Marshal(reqBody)
		if err != nil {
			return nil, &packs.PackError{Code: packs.CodeInternal, Message: err.Error(), Cause: err}
		}

		httpCtx, cancel := context.WithTimeout(ctx, doclingTimeout)
		defer cancel()
		client := &http.Client{Timeout: doclingTimeout}
		// Retry while Docling is still booting so a cold start doesn't fail
		// the first parse (chat-UI readiness gap). reqFn rebuilds the
		// request each attempt since the body is consumed.
		resp, err := coldStartRetry(httpCtx, client, func() (*http.Request, error) {
			r, e := http.NewRequestWithContext(httpCtx, "POST", base+"/v1/convert/source", bytes.NewReader(bodyBytes))
			if e != nil {
				return nil, e
			}
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Accept", "application/json")
			return r, nil
		})
		if err != nil {
			return nil, &packs.PackError{
				Code:    packs.CodeHandlerFailed,
				Message: fmt.Sprintf("docling request to %s: %v", base, err),
				Cause:   err,
			}
		}
		defer resp.Body.Close()

		raw, err := io.ReadAll(io.LimitReader(resp.Body, maxDoclingResponse+1))
		if err != nil {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("read docling response: %v", err), Cause: err}
		}
		if len(raw) > maxDoclingResponse {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("docling response exceeds %d bytes", maxDoclingResponse)}
		}
		if resp.StatusCode >= 400 {
			snippet := string(raw)
			if len(snippet) > 512 {
				snippet = snippet[:512] + "…"
			}
			return nil, &packs.PackError{
				Code:    packs.CodeHandlerFailed,
				Message: fmt.Sprintf("docling %d: %s", resp.StatusCode, snippet),
			}
		}

		var dc doclingResponse
		if err := json.Unmarshal(raw, &dc); err != nil {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("decode docling response: %v", err), Cause: err}
		}
		// Docling's own status semantics:
		//   success        — parse completed cleanly
		//   partial_success — some pages / sections failed but most
		//                     of the doc is usable; we keep the
		//                     markdown and let the caller decide
		//   failure / skipped — surface as handler_failed so retries
		//                       (different ocr_lang, different format)
		//                       make sense
		switch dc.Status {
		case "success", "partial_success":
			// pass through
		default:
			msg := fmt.Sprintf("docling status=%q", dc.Status)
			if len(dc.Errors) > 0 {
				msg += ": " + strings.Join(dc.Errors, "; ")
			}
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed, Message: msg}
		}
		if dc.Document.MDContent == "" {
			return nil, &packs.PackError{
				Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("docling returned empty markdown for %s (status=%s)",
					sourceLabel, dc.Status),
			}
		}

		out := map[string]any{
			"source":          sourceLabel,
			"markdown":        dc.Document.MDContent,
			"status":          dc.Status,
			"processing_time": dc.ProcessingTime,
		}
		if dc.Document.TextContent != "" {
			out["text"] = dc.Document.TextContent
		}
		if dc.Document.HTMLContent != "" {
			out["html"] = dc.Document.HTMLContent
		}
		return json.Marshal(out)
	}
}

// containsString is a tiny helper because slices.Contains requires
// Go 1.21+ and the rest of the pack package sticks to the
// lowest-common-denominator style.
func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
