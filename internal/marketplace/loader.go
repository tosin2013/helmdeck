// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package marketplace

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	yaml "go.yaml.in/yaml/v3"
)

// DefaultMarketplaceURL is the URL the control plane fetches from when
// HELMDECK_MARKETPLACE_URL is unset. Matches ADR 034.
const DefaultMarketplaceURL = "https://github.com/tosin2013/helmdeck-marketplace"

// maxIndexBytes caps how much we'll read from a marketplace index
// response. 4 MiB is generous — a fully-populated catalog of a few
// hundred packs lands well under 100 KiB.
const maxIndexBytes = 4 << 20

// loaderHTTPTimeout is the per-fetch timeout. Marketplace fetches
// happen at startup + on /refresh — operator-initiated, so 30 seconds
// is fine even on a slow link.
const loaderHTTPTimeout = 30 * time.Second

// LoadIndex fetches and parses the marketplace index from `source`.
//
// Three URL shapes are supported:
//   1. https://github.com/<owner>/<repo>             → translated to
//      raw.githubusercontent.com/<owner>/<repo>/main/index.yaml
//   2. https://raw.githubusercontent.com/...index.yaml → fetched verbatim
//   3. file:///path/to/index.yaml                     → read from disk
//      (used by tests + air-gapped operators with a local mirror)
//
// Returns the parsed Index plus a "resolved URL" string suitable for
// logging/debugging (what we actually fetched after translation).
func LoadIndex(ctx context.Context, source string) (*Index, string, error) {
	resolved, err := ResolveIndexURL(source)
	if err != nil {
		return nil, "", err
	}
	body, err := fetchBytes(ctx, resolved)
	if err != nil {
		return nil, resolved, err
	}
	var idx Index
	if err := yaml.Unmarshal(body, &idx); err != nil {
		return nil, resolved, fmt.Errorf("parse index.yaml from %s: %w", resolved, err)
	}
	return &idx, resolved, nil
}

// LoadManifest fetches and parses one pack's helmdeck-pack.yaml from
// the marketplace. The `source` is the marketplace base URL (same as
// LoadIndex's input); `path` is the entry's `path` field from the
// index (e.g. "packs/cmd.upper").
//
// For raw.githubusercontent.com / github.com bases, the manifest lives
// at `<base>/<path>/helmdeck-pack.yaml` rooted at the default branch.
// Exposed as a public function for T813's pack-detail endpoint that
// will land in a follow-up PR; T810 wires the surface but the
// catalog endpoint itself doesn't call this per-pack on every request.
func LoadManifest(ctx context.Context, source, path string) (*Manifest, error) {
	resolved, err := resolveManifestURL(source, path)
	if err != nil {
		return nil, err
	}
	body, err := fetchBytes(ctx, resolved)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := yaml.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", resolved, err)
	}
	return &m, nil
}

// ResolveIndexURL turns a marketplace base URL into the concrete URL
// that contains index.yaml. Exported for tests + observability.
func ResolveIndexURL(source string) (string, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return "", fmt.Errorf("marketplace url is empty")
	}
	if strings.HasPrefix(source, "file://") {
		// Convention: file:// URLs point at the marketplace ROOT
		// DIRECTORY. We append /index.yaml automatically so install-
		// time logic (materializeFromGit) can use the same root URL
		// to compute pack paths.
		if strings.HasSuffix(source, "/index.yaml") {
			return source, nil
		}
		return strings.TrimSuffix(source, "/") + "/index.yaml", nil
	}
	u, err := url.Parse(source)
	if err != nil {
		return "", fmt.Errorf("parse marketplace url %q: %w", source, err)
	}
	// Already a raw-content URL? Pass through.
	if u.Host == "raw.githubusercontent.com" {
		return source, nil
	}
	// github.com/<owner>/<repo>[/]  → raw.githubusercontent.com/<owner>/<repo>/main/index.yaml
	if u.Host == "github.com" {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) < 2 {
			return "", fmt.Errorf("github url %q missing owner/repo", source)
		}
		owner, repo := parts[0], parts[1]
		return fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/main/index.yaml", owner, repo), nil
	}
	// Otherwise treat as a direct URL to index.yaml. Append /index.yaml
	// if it doesn't already end there — operators pointing at a custom
	// mirror sometimes give the catalog root.
	if !strings.HasSuffix(u.Path, "/index.yaml") {
		if !strings.HasSuffix(u.Path, "/") {
			u.Path += "/"
		}
		u.Path += "index.yaml"
		return u.String(), nil
	}
	return source, nil
}

// resolveManifestURL turns a marketplace base + pack path into the URL
// for the per-pack manifest.
func resolveManifestURL(source, path string) (string, error) {
	source = strings.TrimSpace(source)
	path = strings.Trim(path, "/")
	if path == "" {
		return "", fmt.Errorf("manifest path is empty")
	}
	if strings.HasPrefix(source, "file://") {
		// Local file source. The convention here is the source is
		// a directory URL (file:///path/to/marketplace) and we join
		// `path/helmdeck-pack.yaml`.
		trimmed := strings.TrimSuffix(source, "/")
		return trimmed + "/" + path + "/helmdeck-pack.yaml", nil
	}
	u, err := url.Parse(source)
	if err != nil {
		return "", err
	}
	if u.Host == "github.com" {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) < 2 {
			return "", fmt.Errorf("github url %q missing owner/repo", source)
		}
		return fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/main/%s/helmdeck-pack.yaml",
			parts[0], parts[1], path), nil
	}
	if u.Host == "raw.githubusercontent.com" {
		// The base URL already points at raw content — strip any
		// trailing /index.yaml so we can join the manifest path.
		base := strings.TrimSuffix(u.String(), "/index.yaml")
		base = strings.TrimSuffix(base, "/")
		return base + "/" + path + "/helmdeck-pack.yaml", nil
	}
	// Generic mirror — assume source is the catalog root.
	base := strings.TrimSuffix(source, "/index.yaml")
	base = strings.TrimSuffix(base, "/")
	return base + "/" + path + "/helmdeck-pack.yaml", nil
}

// fetchBytes pulls the body of a URL, capped at maxIndexBytes. Handles
// both http(s):// and file:// schemes uniformly so tests can point at
// local fixtures.
func fetchBytes(ctx context.Context, raw string) ([]byte, error) {
	if strings.HasPrefix(raw, "file://") {
		path := strings.TrimPrefix(raw, "file://")
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", raw, err)
		}
		defer f.Close()
		return io.ReadAll(io.LimitReader(f, maxIndexBytes))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, fmt.Errorf("build request for %s: %w", raw, err)
	}
	req.Header.Set("Accept", "text/yaml,application/x-yaml,text/plain;q=0.8")
	client := &http.Client{Timeout: loaderHTTPTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", raw, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch %s: HTTP %d", raw, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxIndexBytes))
}
