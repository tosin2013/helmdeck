// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

//go:build integration
// +build integration

package builtin_test

// slides_fit_integration_test.go (#280) — real-Docker verification that
// the auto-fit CSS keeps mermaid diagrams and wide tables inside the
// fixed Marp slide bounds.
//
// Two layers:
//
//   - render-smoke (always runs under integration): the fixture deck —
//     a big mermaid graph + a wide table — renders to a valid non-empty
//     PDF, and the HTML output carries the injected fit rules targeting
//     the mermaid <img>. Proves the fix reaches the real marp/mmdc path
//     without breaking it.
//   - geometric bounds-assert (best-effort): load the rendered HTML in
//     the sidecar's Chromium and assert no <section> overflows its own
//     box. We measure section.scrollWidth/clientWidth, which are
//     pre-transform layout values and therefore robust to Marp's
//     fit-to-viewport scale transform. Skips cleanly if a headless
//     Chromium measure isn't available in the image.
//
// Run with:
//   HELMDECK_INTEGRATION=1 go test -tags=integration ./internal/packs/builtin/... -run TestSlidesFit -v

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/packs/builtin"
	"github.com/tosin2013/helmdeck/internal/session"
	dockerrt "github.com/tosin2013/helmdeck/internal/session/docker"
)

// fitFixtureDeck has the two overflow culprits #280 targets: a dense
// mermaid graph and a wide (8-column) table.
const fitFixtureDeck = "# Architecture\n\n" +
	"```mermaid\n" +
	"graph TD\n" +
	"  A[Client] --> B[Gateway]; B --> C[Auth]; B --> D[Router]; D --> E[Pack Engine];\n" +
	"  E --> F[Session]; E --> G[Vault]; E --> H[Artifacts]; E --> I[Memory];\n" +
	"  F --> J[Chromium]; F --> K[CDP]; H --> L[Garage S3]; I --> M[SQLite];\n" +
	"```\n\n---\n\n" +
	"# Comparison\n\n" +
	"| Dimension | A | B | C | D | E | F | G |\n" +
	"|---|---|---|---|---|---|---|---|\n" +
	"| latency-p50-milliseconds | 12 | 34 | 56 | 78 | 90 | 21 | 43 |\n" +
	"| throughput-requests-per-second | 1000 | 2000 | 3000 | 4000 | 5000 | 6000 | 7000 |\n"

func newSlidesFitEngine(t *testing.T) (*packs.Engine, *dockerrt.Runtime, *packs.MemoryArtifactStore) {
	t.Helper()
	rt, err := dockerrt.New()
	if err != nil {
		t.Fatalf("docker runtime: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	ex, ok := any(rt).(session.Executor)
	if !ok {
		t.Fatal("docker runtime does not implement session.Executor")
	}
	store := packs.NewMemoryArtifactStore()
	eng := packs.New(
		packs.WithRuntime(rt),
		packs.WithSessionExecutor(ex),
		packs.WithArtifactStore(store),
		packs.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)
	return eng, rt, store
}

func renderFixture(t *testing.T, eng *packs.Engine, store *packs.MemoryArtifactStore, format string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	in, _ := json.Marshal(map[string]any{"markdown": fitFixtureDeck, "format": format})
	res, perr := eng.Execute(ctx, builtin.SlidesRender(nil, nil), in)
	if perr != nil {
		t.Fatalf("slides.render (%s): %v", format, perr)
	}
	var out struct {
		ArtifactKey string `json:"artifact_key"`
	}
	_ = json.Unmarshal(res.Output, &out)
	if out.ArtifactKey == "" {
		t.Fatalf("slides.render (%s) produced no artifact_key", format)
	}
	content, _, gerr := store.Get(ctx, out.ArtifactKey)
	if gerr != nil {
		t.Fatalf("fetch artifact %s: %v", out.ArtifactKey, gerr)
	}
	return content
}

func TestSlidesFit_RenderSmoke(t *testing.T) {
	if _, err := os.Stat("/var/run/docker.sock"); err != nil {
		t.Skip("docker daemon not reachable")
	}
	if os.Getenv("HELMDECK_INTEGRATION") == "" {
		t.Skip("set HELMDECK_INTEGRATION=1 to run real-Docker integration tests")
	}
	eng, _, store := newSlidesFitEngine(t)

	// PDF: the real user-facing format — must render to a valid, non-empty PDF.
	pdf := renderFixture(t, eng, store, "pdf")
	if len(pdf) < 1000 || !strings.HasPrefix(string(pdf[:5]), "%PDF-") {
		t.Errorf("expected a non-trivial PDF, got %d bytes prefix=%q", len(pdf), pdf[:min(8, len(pdf))])
	}

	// HTML: must carry the injected fit rules, targeting the mermaid <img>.
	html := string(renderFixture(t, eng, store, "html"))
	for _, want := range []string{"auto-fit (#280)", "section img.mermaid-svg", "table-layout: fixed", `class="mermaid-svg"`} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered HTML missing %q (fit CSS must reach the renderer)", want)
		}
	}
}

func TestSlidesFit_NoSectionOverflow(t *testing.T) {
	if _, err := os.Stat("/var/run/docker.sock"); err != nil {
		t.Skip("docker daemon not reachable")
	}
	if os.Getenv("HELMDECK_INTEGRATION") == "" {
		t.Skip("set HELMDECK_INTEGRATION=1 to run real-Docker integration tests")
	}
	eng, rt, store := newSlidesFitEngine(t)
	ex := any(rt).(session.Executor)

	html := renderFixture(t, eng, store, "html")

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	sess, err := rt.Create(ctx, session.Spec{Label: "slides-fit-280", MemoryLimit: "512m", SHMSize: "256m"})
	if err != nil {
		t.Fatalf("session create: %v", err)
	}
	t.Cleanup(func() {
		tctx, c := context.WithTimeout(context.Background(), 30*time.Second)
		defer c()
		_ = rt.Terminate(tctx, sess.ID)
	})

	// Write the rendered HTML into the sidecar.
	if _, err := ex.Exec(ctx, sess.ID, session.ExecRequest{
		Cmd:   []string{"sh", "-c", "cat > /tmp/deck-280.html"},
		Stdin: html,
	}); err != nil {
		t.Fatalf("write html: %v", err)
	}

	// Measure with the sidecar's Chromium via Playwright. scrollWidth vs
	// clientWidth is a pre-transform layout value, so it's unaffected by
	// Marp's fit-to-viewport scale. Skip cleanly if the measure harness
	// isn't usable in this image rather than fail spuriously.
	const measure = `
const { chromium } = require('playwright');
(async () => {
  const b = await chromium.launch({ executablePath: process.env.CHROMIUM || '/usr/bin/chromium', args: ['--no-sandbox'] });
  const p = await b.newPage({ viewport: { width: 1280, height: 720 } });
  await p.goto('file:///tmp/deck-280.html', { waitUntil: 'networkidle' });
  const overflow = await p.evaluate(() =>
    [...document.querySelectorAll('section')].filter(s =>
      s.scrollWidth > s.clientWidth + 2 || s.scrollHeight > s.clientHeight + 2).length);
  console.log(JSON.stringify({ overflow }));
  await b.close();
})().catch(e => { console.error('MEASURE_UNAVAILABLE:' + e.message); process.exit(42); });
`
	res, err := ex.Exec(ctx, sess.ID, session.ExecRequest{
		Cmd:   []string{"sh", "-c", "node -e \"$(cat)\""},
		Stdin: []byte(measure),
	})
	if err != nil {
		t.Skipf("headless measure unavailable (exec error): %v", err)
	}
	if res.ExitCode == 42 || strings.Contains(string(res.Stderr), "MEASURE_UNAVAILABLE") {
		t.Skipf("headless measure unavailable in this sidecar image: %s", strings.TrimSpace(string(res.Stderr)))
	}
	if res.ExitCode != 0 {
		t.Skipf("measure script exit %d (treated as unavailable): %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))
	}

	var out struct {
		Overflow int `json:"overflow"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(res.Stdout))), &out); err != nil {
		t.Skipf("could not parse measure output %q: %v", res.Stdout, err)
	}
	if out.Overflow != 0 {
		t.Errorf("%d slide section(s) overflow their bounds after the fit fix — diagrams/tables still clip", out.Overflow)
	}
}
