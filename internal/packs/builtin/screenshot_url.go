// Package builtin houses the reference Capability Packs that ship in
// the helmdeck binary. They are kept in a sub-package so that
// internal/packs has no dependency on the CDP wrappers — packs depend
// on packs+cdp, but the engine itself stays browser-agnostic.
package builtin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tosin2013/helmdeck/internal/packs"
)

// ScreenshotURL is the reference pack referenced by ADR 021. It
// validates that every layer of the pack execution substrate works
// end-to-end: input schema → session acquire → CDP navigate +
// screenshot → artifact upload → typed result.
//
// Input shape:
//
//	{
//	  "url":      "https://example.com",   // required, string
//	  "fullPage": true                       // optional, boolean
//	}
//
// Output shape:
//
//	{
//	  "url":          "https://example.com",
//	  "artifact_key": "browser.screenshot_url/<rand>-screenshot.png",
//	  "size":         12345
//	}
//
// The PNG bytes are uploaded via the artifact store rather than
// embedded in the response so weak models calling this pack don't
// have to handle multi-megabyte base64 payloads.
func ScreenshotURL() *packs.Pack {
	return &packs.Pack{
		Name:        "browser.screenshot_url",
		Version:     "v1",
		Description: "Navigate to a URL and capture a PNG screenshot.",
		NeedsSession: true,
		InputSchema: packs.BasicSchema{
			Required: []string{"url"},
			Properties: map[string]string{
				"url":      "string",
				"fullPage": "boolean",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"url", "artifact_key", "size"},
			Properties: map[string]string{
				"url":          "string",
				"artifact_key": "string",
				"size":         "number",
			},
		},
		Handler: screenshotHandler,
	}
}

type screenshotInput struct {
	URL      string `json:"url"`
	FullPage bool   `json:"fullPage"`
}

func screenshotHandler(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
	var in screenshotInput
	if err := json.Unmarshal(ec.Input, &in); err != nil {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
	}
	if ec.CDP == nil {
		// The engine guarantees CDP is non-nil only when a CDPFactory
		// was wired. Surfacing this as session_unavailable is the
		// honest answer — the runtime is up but the bridge isn't.
		return nil, &packs.PackError{Code: packs.CodeSessionUnavailable, Message: "engine has no CDP factory"}
	}
	if err := ec.CDP.Navigate(ctx, in.URL); err != nil {
		return nil, fmt.Errorf("navigate %s: %w", in.URL, err)
	}
	png, err := ec.CDP.Screenshot(ctx, in.FullPage)
	if err != nil {
		return nil, fmt.Errorf("screenshot: %w", err)
	}
	art, err := ec.Artifacts.Put(ctx, ec.Pack.Name, "screenshot.png", png, "image/png")
	if err != nil {
		return nil, &packs.PackError{Code: packs.CodeArtifactFailed, Message: err.Error(), Cause: err}
	}
	return json.Marshal(map[string]any{
		"url":          in.URL,
		"artifact_key": art.Key,
		"size":         art.Size,
	})
}
