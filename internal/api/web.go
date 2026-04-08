// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package api

import (
	"io/fs"
	"net/http"
	"strings"

	webembed "github.com/tosin2013/helmdeck/web"
)

// registerWebRoute mounts the embedded React/Vite Management UI at
// the root path with an SPA-fallback handler: any request that
// doesn't match a real file in web/dist AND isn't an API route
// falls through to index.html so client-side routes (e.g.
// /sessions, /vault, /audit) work without server-side route
// definitions.
//
// API routes are recognised by the standard helmdeck prefix list
// (/api/v1, /v1, /a2a/v1, /healthz, /version, /.well-known) and
// are excluded so the rest of the mux can serve them.
//
// When the embedded filesystem is empty (e.g. operator built the
// binary before running `make web-build`), a minimal inline
// fallback page explains how to build the UI. API endpoints stay
// fully functional in that mode.
func registerWebRoute(mux *http.ServeMux, _ Deps) {
	// The catch-all is registered as `/` (any method) instead of
	// `GET /` because net/http's ServeMux refuses to register a
	// method-specific catch-all alongside broader path patterns —
	// the conflict detection treats "GET /" as more restrictive on
	// method but more general on path than "/api/v1/sessions/".
	// Method enforcement happens inside the handler.
	if webembed.UIFSErr != nil {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				writeError(w, http.StatusMethodNotAllowed, "method_not_allowed",
					"only GET/HEAD are supported on the SPA root")
				return
			}
			if isAPIPath(r.URL.Path) {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(uiNotBuiltHTML))
		})
		return
	}

	uiFS := webembed.UIFS
	fileServer := http.FileServer(http.FS(uiFS))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed",
				"only GET/HEAD are supported on the SPA root")
			return
		}
		if isAPIPath(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		clean := strings.TrimPrefix(r.URL.Path, "/")
		if clean == "" {
			clean = "index.html"
		}
		// Try to serve the file as-is. If it doesn't exist, fall
		// back to index.html so react-router-dom's client-side
		// routing can take over.
		if _, err := fs.Stat(uiFS, clean); err != nil {
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/"
			fileServer.ServeHTTP(w, r2)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

// isAPIPath reports whether the request path belongs to a registered
// API surface that should NOT be served by the SPA fallback. Mirror
// of the prefix list in IsProtectedPath but inclusive of the
// public discovery endpoints (/healthz, /version, /.well-known).
func isAPIPath(p string) bool {
	switch p {
	case "/healthz", "/version":
		return true
	}
	prefixes := []string{
		"/api/",
		"/v1/",
		"/a2a/",
		"/.well-known/",
	}
	for _, pre := range prefixes {
		if strings.HasPrefix(p, pre) {
			return true
		}
	}
	return false
}

// uiNotBuiltHTML is the inline fallback served when the go:embed
// FS is empty. Mirrors the web/dist/index.html placeholder shipped
// in the source tree so both paths feel coherent.
const uiNotBuiltHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="UTF-8">
<title>Helmdeck — UI not built</title>
<style>
body { font-family: ui-sans-serif, system-ui, sans-serif; background: #0a0a0a; color: #e5e5e5; margin: 0; display: grid; place-items: center; min-height: 100vh; padding: 2rem; }
.card { max-width: 32rem; background: #171717; border: 1px solid #262626; border-radius: 0.5rem; padding: 2rem; }
h1 { margin-top: 0; font-size: 1.25rem; }
code { background: #262626; padding: 0.125rem 0.375rem; border-radius: 0.25rem; font-family: ui-monospace, monospace; font-size: 0.875rem; }
a { color: #60a5fa; }
</style>
</head>
<body>
<div class="card">
<h1>Helmdeck UI not built</h1>
<p>The control plane was compiled without the embedded JavaScript bundle. Run <code>make web-build</code> followed by <code>make build</code> to produce a binary with the Management UI baked in.</p>
<p>API endpoints under <code>/api/v1/</code>, <code>/v1/</code>, and <code>/healthz</code> are unaffected and still work normally.</p>
<p>See <a href="https://github.com/tosin2013/helmdeck/blob/main/CONTRIBUTING.md">CONTRIBUTING.md</a> for the full developer workflow.</p>
</div>
</body>
</html>`
