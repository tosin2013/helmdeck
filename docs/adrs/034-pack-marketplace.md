---
description: "ADR-034: Pack Marketplace (App Store for Capability Packs) — Proposed. Architectural decision record for the helmdeck control-plane."
---

# ADR 034 — Pack Marketplace (App Store for Capability Packs)

**Status:** Accepted
**Date:** 2026-04-10
**Author:** Tosin Akinosho

## Context

Helmdeck ships 19 built-in capability packs today, all compiled Go code inside the control plane binary. There is no mechanism for operators to discover, install, or share additional packs without forking the repo and rebuilding. T608 (Pack Authoring UI, deferred to Phase 8) will let operators author packs in the UI, but that is a single-tenant workflow — it does not address discovery, distribution, or trust across the ecosystem.

The product needs an **app-store model** where:

- **Core packs** ship in the binary (always available, no install step)
- **Marketplace packs** are browsable in a catalog UI, one-click installable, and loadable into the running helmdeck without a rebuild
- **Community authors** can publish packs to the marketplace
- **Same pack format** — marketplace packs are identical to core packs in structure, engine execution, and MCP exposure. The only difference is distribution channel.

This is analogous to VS Code extensions, Homebrew formulae, or Helm chart repos — a discovery + distribution layer on top of an existing execution engine.

## Decision

### Pack manifest schema

Every pack (core or marketplace) is described by a `helmdeck-pack.yaml` (or `.json`):

```yaml
name: github.create_issue
version: v1
author: tosin2013
license: Apache-2.0
description: Create a GitHub issue using a vault-stored PAT.
category: developer-tools
tags: [github, issues, ci-cd]
needs_session: false

input_schema:
  required: [repo, title]
  properties:
    repo: { type: string, description: "owner/repo" }
    title: { type: string }
    body: { type: string }
    labels: { type: array, items: { type: string } }

output_schema:
  required: [number, url]
  properties:
    number: { type: number }
    url: { type: string }
    html_url: { type: string }

handler:
  type: builtin
  # type: command  → { command: ["node", "index.js"], env: {...} }
  # type: composite → { steps: [{pack: "http.fetch", args: {...}}, ...] }
  # type: wasm → { module: "handler.wasm", capabilities: ["net"] }

trust:
  signed_by: tosin2013
  sha256: abc123...
```

### Handler types

| Type | Phase | Trust level | Who uses it |
| :--- | :--- | :--- | :--- |
| `builtin` | Today | Highest (in-process Go) | Core packs (19 + `github.*`) |
| `command` | Marketplace Phase 1 | Medium (subprocess, sandboxed) | Community authors — any language, stdio JSON protocol |
| `composite` | Marketplace Phase 1 | Low (calls other packs only) | Operators wiring multi-step workflows without code |
| `wasm` | Phase 8 (T801) | High (WASI sandbox) | Performance-sensitive community packs |

The `command` handler type is the easiest community path: write a handler in any language that reads a JSON request from stdin and writes a JSON response to stdout. The control plane spawns it as a subprocess with the same egress guard, audit logging, and timeout enforcement as built-in packs.

### Registry / catalog model

A **git repo** (the "tap") serves as the marketplace:

```
helmdeck-marketplace/
├── index.yaml                    # [{name, version, source, description, category, tags, stars, installs}]
├── packs/
│   ├── gitlab.create_issue/
│   │   ├── helmdeck-pack.yaml
│   │   └── handler.js            # command-type handler
│   ├── slack.post_message/
│   │   ├── helmdeck-pack.yaml
│   │   └── handler.py
│   ├── jira.create_issue/
│   │   └── helmdeck-pack.yaml    # composite-type, calls http.fetch
│   └── ...
```

The control plane reads `index.yaml` from `HELMDECK_MARKETPLACE_URL` (default: `https://github.com/tosin2013/helmdeck-marketplace`) at startup and on `POST /api/v1/marketplace/refresh`. The Management UI's Marketplace panel renders the catalog.

### Install flow

1. Operator opens `/marketplace` in the Management UI
2. Browses by category (developer-tools, cloud, notifications, database, security, ai-tools) or searches by name/tag
3. Clicks "Install" on a pack card
4. Control plane: `git clone --depth=1` the pack source → copies `helmdeck-pack.yaml` + handler to `~/.helmdeck/packs/` → registers in the pack registry → pack appears in MCP `tools/list` and the Capability Packs panel immediately (hot-load, no restart)
5. Uninstall: removes from cache + deregisters. Core packs cannot be uninstalled.

### Core vs marketplace classification

**Core packs** (ship in the binary, T617+):

| Namespace | Packs | Why core |
| :--- | :--- | :--- |
| `browser.*` | screenshot_url | Foundational — drives the sidecar |
| `web.*` | scrape_spa | Foundational — web extraction |
| `fs.*` | read, write, list, patch | Foundational — file ops in session |
| `cmd.*` | run | Foundational — shell commands |
| `git.*` | commit | Foundational — git staging |
| `repo.*` | fetch, push | Foundational — git transport |
| `http.*` | fetch | Foundational — HTTP with vault |
| `slides.*` | render | Foundational — document generation |
| `doc.*` | ocr | Foundational — text extraction |
| `desktop.*` | run_app_and_screenshot | Foundational — desktop automation |
| `vision.*` | click, extract, fill_form | Foundational — vision loop |
| `python.*` | run | Language runtime |
| `node.*` | run | Language runtime |
| `github.*` | create_issue, list_prs, post_comment, create_release | Deep git integration already in helmdeck |

**Marketplace candidates** (community-authored):

| Category | Examples |
| :--- | :--- |
| Developer tools | `gitlab.*`, `bitbucket.*`, `jira.*`, `linear.*` |
| Cloud | `aws.s3_upload`, `gcp.cloud_run_deploy`, `azure.blob_upload` |
| Notifications | `slack.post_message`, `discord.send`, `teams.post`, `email.send` |
| Database | `postgres.query`, `redis.get_set`, `mongo.find` |
| Security | `trivy.scan`, `snyk.test`, `semgrep.run` |
| AI tools | `openai.embeddings`, `anthropic.batch`, `huggingface.inference` |
| Monitoring | `datadog.event`, `pagerduty.trigger`, `grafana.annotate` |

### Marketplace UI panel

New `/marketplace` route in the Management UI sidebar:

- **Browse tab** — card grid of available packs, filtered by category
- **Search** — full-text by name, description, tags
- **Pack detail view** — full description, author, version history, install count, input/output schema preview, "Install" button, trust badge (signed / unsigned / core)
- **Installed tab** — operator's installed marketplace packs with version badge and "Uninstall" button
- **Core badge** — built-in packs show a "Core" chip and no uninstall button

### Trust model

- **Core packs**: implicit trust (compiled into the binary)
- **Official marketplace packs** (from the `helmdeck-marketplace` repo): cosign-signed by the helmdeck maintainer. Verified at install time.
- **Community packs** (from any git repo): unsigned by default. Require `--trust-unsigned` flag or a UI confirmation dialog ("This pack is from an unverified author. Install anyway?"). Audit log entry records every install decision.
- **WASM packs** (Phase 8): run inside the WASI sandbox with explicit capability grants. The operator must approve each capability (network, filesystem, env vars) before the pack can use it.

### Future: consumer marketplace

A web frontend (separate repo, `helmdeck/marketplace-web`) where operators browse, rate, review, and publish packs. Features:

- User accounts (GitHub OAuth)
- Pack submission + review workflow
- Star / rating system
- Install analytics (download counts, active installs)
- Featured / trending packs
- Author profiles + verified badges

The control plane exposes `/api/v1/marketplace/` endpoints that the frontend calls. This is post-v1.0 territory; the ADR scopes it as a known future extension without committing to an implementation.

## PRD Sections

§6.6 Capability Packs, §6.7 Pack Authoring, §19.10 Progressive Disclosure

## Consequences

- Helmdeck becomes a **platform**, not just a tool. The pack catalog can grow without core maintainer involvement.
- The `command` handler type lowers the bar for community contribution to "write a script that reads stdin JSON and writes stdout JSON" — any language, any framework.
- The trust model adds operational complexity — operators must make install-time decisions about unsigned packs. Mitigated by the cosign-signed official channel and clear UI warnings.
- The git-based registry model means marketplace updates propagate at `git pull` speed, not at real-time speed. This is acceptable for pack distribution where the catalog changes daily, not per-second.
- Core packs continue to compile-time guarantee their availability. Marketplace packs are best-effort — if the source repo is unreachable, the local cache serves the last-installed version.
