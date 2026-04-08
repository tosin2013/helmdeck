// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

// Package web exposes the React/Vite build output as an embedded
// io/fs.FS so the control plane can serve the Management UI from
// inside the single-binary distribution. The build output lives in
// web/dist/ which is gitignored except for a placeholder
// index.html that lets the Go build succeed before `make web-build`
// has ever run.
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
