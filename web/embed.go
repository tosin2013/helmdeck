// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

// Package web exposes the React/Vite build output as an embedded
// io/fs.FS so the control plane can serve the Management UI from
// inside the single-binary distribution. The build output lives in
// web/dist/ which is gitignored except for a tiny stub
// index.html that lets the Go build succeed before the Vite bundle
// has been produced.
//
// Build sources:
//   - Production docker images: web/dist is generated INSIDE the
//     docker build by the web-build Node stage (see
//     deploy/docker/control-plane.Dockerfile). The host's web/dist
//     is .dockerignore'd out of the build context, so the embedded
//     HTML and embedded assets are always produced from the same
//     source tree in the same stage — byte-for-byte consistent.
//   - Host-side `go build` (IDE, `go test`, etc.): the committed
//     stub web/dist/index.html keeps go:embed satisfied. Running
//     `npm run build` in web/ replaces the stub with a real bundle
//     for local UI dev. The stub itself is byte-stable, so accidentally
//     committing it back from a host that had not built does not
//     introduce drift.
//
// Mixing Go and JavaScript source in the same directory is
// intentional: it lets //go:embed reach the dist files without a
// copy step at build time. Vite ignores .go files (they don't
// match its glob patterns), and `go build` ignores everything
// outside .go files.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// DistFS returns the embedded UI filesystem rooted at web/dist.
// Caller is expected to wrap it with fs.Sub("dist") to get a
// filesystem rooted at the actual dist contents — the package-level
// helper UIFS does that work.
func DistFS() embed.FS { return distFS }

// UIFS is the embedded filesystem rooted at the dist directory.
// Returns nil and an error if the dist subdirectory doesn't exist
// at compile time, which happens only when the source tree is
// missing the placeholder web/dist/index.html.
var UIFS, UIFSErr = fs.Sub(distFS, "dist")
