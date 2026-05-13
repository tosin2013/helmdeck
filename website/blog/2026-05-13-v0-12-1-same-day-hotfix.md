---
slug: v0-12-1-same-day-hotfix
title: "v0.12.1 hot-patch: when CI silence is louder than CI noise"
authors: [tosin]
tags: [field-report, release-engineering, post-mortem]
description: v0.12.0 shipped a broken Management UI on every fresh docker pull. The release workflow never ran `npm run build` before bundling the image — and nothing in CI caught it because the assets are gitignored and PR CI didn't exercise the image. v0.12.1 fixes the workflow and adds a verify step that fails the release loud rather than waiting for users to notice.
date: 2026-05-13
---

## The signal we missed

v0.12.0 shipped on 2026-05-12. Six hours later, the first bug report:

> Fresh `docker pull ghcr.io/tosin2013/helmdeck:0.12.0`, ran `docker compose up`, hit `localhost:3000` — blank page. Browser console: 404 on `/assets/index-Bo2mLgzR.js`.

The image was published. Cosign signed it. The release workflow ran clean. The MCP Registry picked up v0.12.0 as `isLatest: true`. Every signal said the release was healthy.

<!-- truncate -->

The image was, in fact, broken. And the embarrassing part: it was broken in exactly the way that would never show up in PR CI, because PR CI doesn't pull and run the published image.

## What actually happened

The control-plane binary embeds `web/dist/` via `//go:embed all:dist` (at `web/embed.go:23`) and serves it from `internal/api/web.go:54-80`. Standard pattern for a Go service that ships a React UI as one binary.

`web/dist/assets/` is gitignored. Only the placeholder `web/dist/index.html` is committed. That's deliberate — Vite emits hashed asset names like `index-Bg5l7Aco.js` on every build, and committing minified output produces merge conflicts on every PR that touches `web/src/`.

The expected flow:

1. CI checks out the repo (committed `web/dist/index.html` referencing some old hash, plus empty `web/dist/assets/`)
2. CI runs `npm ci && npm run build` in `web/`
3. Vite rewrites `web/dist/index.html` with the new hash references
4. Vite emits the matching `web/dist/assets/index-<hash>.js` and `.css`
5. `docker/build-push-action` builds the image from a context where `web/dist/index.html` and `web/dist/assets/` are consistent
6. `//go:embed all:dist` bakes them into the binary
7. Browser fetches the hashed asset → present in the FS → renders

The release workflow skipped step 2.

When I built `web/dist/` on my laptop while working on v0.12.0's content-pack image chaining, my local `web/dist/index.html` got the right hashes. Git saw `index.html` as modified, I (or a previous PR) committed the updated reference, and `web/dist/assets/` stayed gitignored.

On the CI runner cutting v0.12.0, the only `index.html` available was whatever was last committed — which pointed at the hashes from the last build done locally, with a `web/dist/assets/` directory that didn't exist in the CI checkout because it's gitignored. The image got built. The reference in `index.html` was stale. The asset files weren't there at all.

Five separate signals could have caught this. None did.

## Why nothing caught it

- **PR CI doesn't pull the published image.** It runs `make smoke` against locally built containers, where the web bundle exists because `make smoke` runs `make web-build` first. Tests pass.
- **The release workflow doesn't render the UI.** It builds, signs, pushes, exits. There's no `curl localhost:3000/assets/...` step that would 404.
- **`web/embed.go`'s `//go:embed all:dist` doesn't validate hash consistency.** It happily embeds an `index.html` that points at files not in the embedded FS — the Go compiler has no opinion about whether asset references in HTML actually resolve.
- **The Dockerfile is `COPY web ./web`.** No `npm run build` stage. Whatever's in `web/dist/` at image-build time goes into the layer verbatim.
- **The Makefile target exists.** `make web-build` does the right thing (`cd web && npm run build`). The workflow just never called it.

The thing that makes this kind of bug nasty is that it's invisible until someone *fresh* runs the image. The maintainer's laptop always has the right `web/dist/` because the maintainer just built it. The CI runner always has a stale `index.html` because the build step is missing. The smoke test always passes because it triggers `make web-build` as a side effect. The only failing observer is a brand-new user — and the first one filed a bug six hours into the release.

## The fix is two workflow steps

```yaml
# .github/workflows/release.yml, in the control-plane-image job
- uses: actions/setup-node@v4
  with:
    node-version: '20'
    cache: 'npm'
    cache-dependency-path: web/package-lock.json
- name: Build web bundle
  run: |
    cd web
    npm ci
    npm run build
- name: Verify embedded web hashes resolve
  run: |
    set -euo pipefail
    cd web/dist
    missing=0
    for asset in $(grep -oE 'assets/(index|[A-Za-z0-9_-]+)-[A-Za-z0-9_-]+\.(js|css)' index.html); do
      if [ ! -f "$asset" ]; then
        echo "MISSING: $asset (referenced from index.html but not in web/dist/)"
        missing=1
      fi
    done
    if [ "$missing" = "1" ]; then
      echo "::error::web build produced index.html referencing assets not on disk; aborting release"
      exit 1
    fi
    echo "All embedded asset hashes resolve."
```

The first step does the obvious thing — build the bundle before the image build.

The second step is the one that matters. It compares the asset hashes referenced from the freshly-built `index.html` against what's actually on disk. If they diverge for any reason — a future Vite version emits hashes differently, someone introduces a sub-bundle, a build step gets reordered — the release fails loud before the image ships.

If this check had been in v0.12.0's workflow, the broken image wouldn't have left CI. The check costs ~100ms to run. The cost of skipping it was a six-hour broken-on-arrival window for every fresh installer.

## Why not commit `web/dist/`?

The obvious "fix" is to remove `web/dist/assets/` from `.gitignore` and commit the built bundle. Every PR that touches `web/src/` would also commit a regenerated dist. CI would never need to rebuild — it'd embed whatever's in git.

Two problems:

1. **Merge churn.** Minified JS files have one-line content; any change to `web/src/` rebuilds the entire bundle into a single new file at a new hashed path. Two PRs touching `web/src/` simultaneously produce diff conflicts in every assets/* file. Resolving those means rebuilding and recommitting. The pre-commit hook becomes "rebuild and commit the bundle," which is fast in theory and slow in practice.
2. **Review noise.** A 1-line CSS change in `web/src/` produces a ~200KB diff in the committed bundle. Reviewers learn to ignore the dist diff, which means they also ignore the times someone accidentally committed a different bundle than the one their source change produces.

The Vercel-style answer ("build in CI, ship the artifact") is the right one for any project where the production bundle's content matters and where reviewers shouldn't be looking at minified output. Helmdeck fits that. The workflow-step fix is the architecturally correct approach; the bug was missing CI plumbing, not missing source-of-truth.

## What else shipped in v0.12.1

While the release-blocker was the dominant fix, three smaller reliability bugs landed in the same patch:

- **`firecrawl-rabbitmq` cold-boot race (#181).** Healthcheck `start_period: 15s` was too short for RabbitMQ's Erlang VM + mnesia init on alpine. Bumped to 60s, aligning with the `firecrawl-searxng` precedent in the same file. First-boot of the firecrawl overlay now takes ~60-90s instead of failing and requiring a manual `compose up` retry.
- **`content.ground` truncated JSON with weak models (#179).** The hard-coded 1024-token completion cap left ~270 tokens of headroom for the structured claim-plan JSON — easily blown by verbose models or large posts, surfacing as `CodeHandlerFailed: claim extractor returned unparseable JSON` with an empty snippet. Default bumped to 2048; new optional `max_completion_tokens` input accepts up to 8192.
- **`content.ground` silent degradation when Firecrawl is unreachable (#182).** The per-claim grounding loop swallowed `callFirecrawlSearch` transport errors silently, producing empty-success "no sources found" output. Now tracks transport-error count vs total attempted calls; when 100% fail at the transport layer, returns `CodeHandlerFailed` with a Firecrawl-reachability message. Mirrors the v0.11 narration contract's fail-loud-on-missing-dependency pattern.

Each landed as a separate small PR (#186/#187/#188/#189) instead of one bundled commit, so any individual fix is independently revertible if a regression surfaces.

## The lesson worth keeping

When CI is silent on a release-blocker bug, the problem is almost never "the bug is too subtle to catch." It's "nothing in CI is even looking." The fix isn't a smarter test; it's making the obvious thing fail loud.

Three lines of bash in a workflow caught a class of bug that would otherwise need a real user to notice. That's the cheapest defense-in-depth in the budget.
