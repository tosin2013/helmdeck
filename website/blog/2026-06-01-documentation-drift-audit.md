---
slug: documentation-drift-audit
title: "The docs said 38 packs. The binary registered 52. Here's what 10 releases of silent drift cost us."
authors: [tosin]
tags: [field-report, friction]
description: A full documentation audit after v0.22.0 found 14 stale pack counts, 4 phantom pipelines, 7 undocumented packs, a sitemap on the wrong domain, and ADRs still marked "Proposed" for shipped work. The fix was mechanical; the lesson is about cadence.
image: /img/social-card.png
date: 2026-06-01
draft: false
---

## Hook

The README said **41 capability packs**. `PACKS.md` said **38**. `SKILLS.md` said **43 tools**. The control-plane binary actually registered **52**. None of those four numbers agreed, and the gap had been widening for roughly ten releases.

## Context

After v0.22.0 shipped the routing/memory/context subsystems (ADRs 047-050), we ran a full documentation audit against the source of truth — `cmd/control-plane/main.go` for pack registration, `internal/pipelines/seed.go` for pipelines, `internal/mcp/server.go` for resources. The drift wasn't in one place; it was everywhere a number had been typed by hand and never re-derived.

## Finding

The pack count alone was wrong in 14 files, each frozen at whatever the catalog size happened to be when that page was last touched. But the count was the *cheap* error. The expensive ones were structural:

| Drift class | What we found |
|---|---|
| Stale counts | Pack count wrong in 14 files (38/41/43/35/36/39); README ADR count said 36, actual 49 |
| Phantom catalog entries | A `slides.notes` pack that doesn't exist; 4 pipelines (`*-ground-blog`) replaced by `*-rewrite-blog` but still documented |
| Missing docs | 7 shipped packs (the 4 orchestration meta-packs, `github.get_issue`/`create_pr`, `blog.rewrite_for_audience`) had no reference page; 10 pipelines undocumented |
| Wrong wiring | Pipeline step chains still showed `content.ground → slides.render`, omitting the `slides.outline` step added in v0.18 |
| Status lies | ADR 050 still marked "Proposed" though all four of its PRs had shipped |
| SEO rot | `sitemap.xml` pointed at the old `helmdeck.vercel.app` domain (canonical is `helmdeck.dev`) with months-old `lastmod` dates |

The mechanical fixes are verifiable by grep — a single sweep confirms zero residual stale counts. The structural fixes are not: each new claim (a pipeline's step chain, a pack's input schema) had to be cross-checked against the registration code before it was written down, because the docs themselves were no longer trustworthy as a source.

## Why this matters to you

Documentation drift is a *compounding* liability, not a constant one. Each release that adds a pack without touching the count makes every hardcoded count one more unit wrong, and the cost of reconciliation grows superlinearly because you eventually can't trust any single page to cross-check another — you have to go back to the code. The fix is cadence, not heroics: re-derive counts from one canonical place (we use `skills/helmdeck/SKILL.md`), keep ADR status headers honest at merge time, and treat a phantom catalog entry as a bug, not a typo. A pack you document but never shipped is worse than a pack you shipped but never documented — the first actively lies to the agent reading your `SKILLS.md`.

## See also

- Pack catalog: [/PACKS](/PACKS) — now the single quick-reference table for all 52 packs
- MCP resources: [/reference/mcp-resources](/reference/mcp-resources)
- Routing & memory: [/howto/routing-and-gap-analysis](/howto/routing-and-gap-analysis), [/howto/free-models-and-context](/howto/free-models-and-context)
