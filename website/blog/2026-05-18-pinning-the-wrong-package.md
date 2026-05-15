---
slug: pinning-the-wrong-package
title: "We almost pinned a package that doesn't exist — and the discipline that came out of it"
authors: [tosin]
tags: [friction, agent-architecture]
description: The first cut of helmdeck's hyperframes.render sidecar Dockerfile pinned @hyperframes/cli@1.4.0 — a package that has never existed on npm. The real package is hyperframes (no scope), 0.6.7, requires Node ≥22. The build failed loud and we caught it. If we hadn't, every operator pulling the sidecar would have seen the same 404. ADR 037 is the discipline that came out of it.
image: /img/social-card.png
date: 2026-05-18
draft: false
---

## Hook

The first cut of helmdeck's `helmdeck-sidecar-hyperframes` Dockerfile pinned `@hyperframes/cli@1.4.0`. That package has never existed on npm. The actual upstream is `hyperframes` (no scope), version `0.6.7`, requiring Node ≥22. We caught it because Docker failed loud:

```
npm ERR! 404 Not Found - GET https://registry.npmjs.org/@hyperframes%2Fcli
npm ERR! 404 '@hyperframes/cli@1.4.0' is not in the npm registry.
```

If we hadn't caught it in CI, every operator who pulled `helmdeck-sidecar-hyperframes:0.13.0` would have seen the same 404. That would have been the loudest possible failure — but the friction story underneath is "we wrote a Dockerfile against a package name we never verified," and the discipline that came out of it ([ADR 037](https://github.com/tosin2013/helmdeck/blob/main/docs/adrs/037-upstream-package-version-management.md)) is now project-wide.

## Context

The work was [#200](https://github.com/tosin2013/helmdeck/issues/200), `hyperframes.render`: a new media-output pack that takes an HTML/CSS/JS composition and renders it to MP4. The implementation depends on the upstream [`hyperframes`](https://github.com/heygen-com/hyperframes) CLI, which orchestrates headless Chromium's BeginFrame API plus ffmpeg for deterministic frame-accurate output. The expected workflow was: build a sidecar image with the CLI installed via `npm`, wire the pack handler to shell out to it, ship a `helmdeck-sidecar-hyperframes` image in CI.

The first cut of the Dockerfile started this way:

```dockerfile
RUN npm install -g @hyperframes/cli@1.4.0
```

The `@hyperframes/cli` package name was an assumption. So was `1.4.0`. The npm registry disagreed with both.

## Finding

Going to the actual upstream, here's what was true:

- The real npm package is named `hyperframes` (no scope, no `/cli` suffix).
- The latest version at the time was `0.6.7`. There was no `1.4.0`.
- It requires Node ≥22.

The rewrite that made the build pass:

```dockerfile
FROM ghcr.io/tosin2013/helmdeck-sidecar:0.13.0 AS base

# Node ≥22 required by hyperframes 0.6.x
RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
 && apt-get install -y --no-install-recommends nodejs \
 && rm -rf /var/lib/apt/lists/*

# Pin exact upstream version; surface it in the build for visibility
RUN npm install -g hyperframes@0.6.7
RUN hyperframes --version  # ← prints 0.6.7; build fails loud if it doesn't
```

The fix is two lines. The lesson is the third one: `RUN hyperframes --version`. That's the **CLI-surface sentinel**. If npm ever serves us a wrong artifact for `hyperframes@0.6.7` (typosquat, registry compromise, package rename, anything), the sentinel breaks the build. Without the sentinel, the install could "succeed" by pulling a malicious lookalike and the failure would only surface at runtime, inside a sidecar, when a pack invocation tries to render. That's late.

The Pack-handler code paths cared about exactly two things the CLI surface exposes: `--resolution` (one of `landscape`/`portrait`/`square` ± `-4k`) and the positional project-directory argument. Neither of those flags is in the imagined `@hyperframes/cli@1.4.0` API. They're the real upstream's API. If the wrong package somehow slipped through, the very first integration test against `hyperframes --resolution landscape ./project` would fail with `unknown flag --resolution`.

So the discipline that came out of this — written up as [ADR 037](https://github.com/tosin2013/helmdeck/blob/main/docs/adrs/037-upstream-package-version-management.md) — has three rules:

1. **Exact pins, no `^`/`~`.** `npm install -g foo@0.6.7`, not `^0.6.7`. A package author bumping `1.0.0` between when we wrote the Dockerfile and when CI rebuilt the image is a real failure mode. The constraint is "we tested against 0.6.7"; let Dependabot bump it deliberately.
2. **CLI-surface sentinel.** Every upstream binary the sidecar shells out to gets a `RUN <binary> --version` (or `--help`) call after install. The build fails loud if the wrong artifact landed.
3. **Dependabot watches what we actually use.** `.github/dependabot.yml` registers the real package name (`hyperframes`, not `@hyperframes/cli`) so version bumps appear in CI as PRs, with the sentinel still in the Dockerfile to catch any post-bump surprise.

## Why this matters to you

If you're integrating any upstream tool through a container — npm CLI, Python package, OS package, Go module fetched at build time — the trap is assuming the package name matches the binary name. It usually does. When it doesn't, the failure mode depends on how late you find out:

| Find out at | Cost |
|---|---|
| `docker build` (CLI sentinel catches it) | 30 seconds |
| `docker pull` by an operator | the operator's afternoon |
| Pack invocation at runtime | a production incident |
| Through typosquat to a malicious package | a breach |

The first row is free. The discipline is two extra lines of Dockerfile (`RUN <binary> --version`) and pinning the version exactly. The benefit is the whole table to the right of that row never happens to you.

The broader pattern: **integrate against the surface, not the name**. Names are assumptions. Behaviors are verifiable. The CLI sentinel is just one shape of "before you trust this thing, run it once and check it behaves." If you can also pin its hash (sigstore-attested artifacts, OCI digest pins, npm `@types/...` provenance), do that too. But the cheapest first step is the version sentinel.

## See also

- [ADR 037 — Upstream package version management](https://github.com/tosin2013/helmdeck/blob/main/docs/adrs/037-upstream-package-version-management.md)
- [The `hyperframes.render` pack reference](/docs/reference/packs/hyperframes/render)
- [v0.13.0 release announcement](/blog/v0-13-0-marketplace-beta)
- [Upstream `hyperframes`](https://github.com/heygen-com/hyperframes)
