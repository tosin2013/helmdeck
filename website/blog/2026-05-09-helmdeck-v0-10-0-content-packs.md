---
slug: helmdeck-v0-10-0-content-packs
title: "Helmdeck v0.10.0 — content packs, 38 packs total, registry-published"
authors: [tosin]
tags: [release-notes, mcp, content-packs, registry]
description: v0.10.0 ships two new content packs (blog.publish, podcast.generate), publishes helmdeck to the official MCP Registry, documents the operator upgrade procedure for the first time, and acknowledges the experimental status of the vision packs. Pack count 36 → 38.
image: /img/social-card.png
date: 2026-05-09
draft: false
---

> Tag: [`v0.10.0`](https://github.com/tosin2013/helmdeck/releases/tag/v0.10.0). Upgrade in 5 min: `git checkout v0.10.0 && make install`.

v0.10.0 is a content-packs release. The two new packs let an agent generate something it can publish — a blog post via Ghost, a multi-speaker podcast MP3 via ElevenLabs — in one tool call, instead of needing to chain `http.fetch` + custom JWT logic + ffmpeg shellouts. Pack count climbs from 36 to 38.

It also closes a gap on the operator side: until this release helmdeck had **zero documentation** on how to upgrade a running install. New `docs/howto/upgrade-helmdeck.md` covers the in-place Compose upgrade, schema migrations, post-upgrade validation, and rollback. With Kubernetes coming in v1.0, the absence of this guide had become a real risk.

<!-- truncate -->

## What's new in v0.10.0

### `blog.publish` (#103)

Posts to a **Ghost** blog (over the Admin API with HS256 JWT auth) or to the helmdeck **artifact store** as raw markdown/HTML. Two destinations, one pack — chosen via `destination: ghost | artifact`. The Ghost path takes care of the JWT lifecycle (5-min expiry, `kid` header) so the agent doesn't have to.

```jsonc
{
  "destination": "ghost",
  "ghost_admin_url": "https://yourblog.com/ghost/api/admin/",
  "credential": "ghost-admin-key",
  "title": "How we shipped v0.10.0",
  "markdown": "# Hello…",
  "tags": ["release-notes"],
  "status": "draft"
}
```

The agent writes one tool call and gets a published-post URL back. Body mode (you give it the markdown) and prompt mode (you give it a topic + outline and the gateway-routed model writes the body) both ship.

### `podcast.generate` (#106)

Multi-speaker TTS pipeline: 4-mode dispatcher (`script` mode, `prompt` mode, `source_url` mode via Firecrawl scrape, `source_text` mode), per-turn ElevenLabs synthesis, ffmpeg-concat with anullsrc silence padding between turns. Outputs an MP3 to the artifact store.

```jsonc
{
  "speakers": {
    "Alex":   "21m00Tcm4TlvDq8ikWAM",
    "Jordan": "EXAVITQu4vr4xnSDxMaL"
  },
  "script": [
    {"speaker": "Alex",   "text": "Welcome back to the show…"},
    {"speaker": "Jordan", "text": "And today we're diving into…"}
  ],
  "theme": "deep-dive",
  "silence_between_turns_ms": 400
}
```

Live-tested on the way to the tag: 4-turn script → **17.2 s of MP3, 275 KB, ID3v2.4 header, both speakers distinct**. The TTS engine selection is pluggable via the `Engine` interface — ElevenLabs is the default; bring-your-own is straightforward to drop in. (Future post coming on the engineering details.)

### `vision.click_anywhere` — fixed mechanically, still experimental

[#105](https://github.com/tosin2013/helmdeck/pull/105) closed [#102](https://github.com/tosin2013/helmdeck/issues/102) — the per-step screenshots now genuinely change between turns instead of returning identical bytes from the same failed click. The action history threading + post-dispatch wait both work as designed.

But: the underlying limitation is **model-side, not platform-side**. Real click-anywhere goals ("focus the URL bar", "click the New Tab button") still rarely converge to `done` even with the loop fixed — the vision model emits sensible coordinates but doesn't recognize when the click succeeded. We've filed [#112](https://github.com/tosin2013/helmdeck/issues/112) as a research track for this.

For the v0.10.0 release notes we flagged both `vision.click_anywhere` and `vision.fill_form_by_label` as **experimental for production**. If you have a deterministic browser-automation goal, prefer `web.test` — it goes through Playwright MCP and is materially more reliable. We'd rather lose a vanity feature claim than ship something that doesn't work.

## Helmdeck on the official MCP Registry

v0.10.0 is also the first release to publish to [`registry.modelcontextprotocol.io`](https://registry.modelcontextprotocol.io/) — the canonical metadata source for Anthropic, GitHub, PulseMCP, and Microsoft-backed MCP discovery. Helmdeck is now searchable as `io.github.tosin2013/helmdeck`.

What this changes for you:
- **Registry-aware MCP clients** can install helmdeck with one command (`claude mcp add @helmdeck/mcp-bridge` and similar).
- **Aggregators** (mcp.so, Glama, PulseMCP) auto-pull from the official registry, so helmdeck will appear there over the next few days without us doing anything.
- **The `.mcp/server.json`** in the repo is now the single source of truth for install metadata. Both npm (`@helmdeck/mcp-bridge@0.10.0`) and OCI (`ghcr.io/tosin2013/helmdeck-mcp:0.10.0`) install paths are declared.

The cross-client install walkthrough lives at [Register helmdeck with your MCP client](/howto/register-with-mcp-clients).

## Upgrade in 5 minutes

If you're running a v0.9.0 Compose stack:

```bash
cd /path/to/helmdeck
git fetch origin
git checkout v0.10.0
make sidecars
make install
```

Schema migrations apply automatically on first connection (no manual `migrate up`). Validate post-upgrade:

```bash
curl -fsS http://localhost:3000/healthz
JWT=$(curl -fsS -X POST http://localhost:3000/api/v1/auth/login \
  -H 'Content-Type: application/json' -d '{"username":"admin","password":"…"}' | jq -r .token)
curl -fsS -H "Authorization: Bearer $JWT" http://localhost:3000/api/v1/packs | jq '. | length'  # → 38
```

Then re-run `./scripts/configure-openclaw.sh` so the SKILL.md gets re-stamped with the v0.10.0 commit SHA.

The full upgrade procedure (rollback included) is at [Upgrade helmdeck](/howto/upgrade-helmdeck).

## Phase 7 audit — Kubernetes is next

While prepping the release I audited the v1.0 / Kubernetes path and filed four follow-up issues:

- [#108](https://github.com/tosin2013/helmdeck/issues/108) — schema-migration cross-version test (P1)
- [#109](https://github.com/tosin2013/helmdeck/issues/109) — sidecar version pinning (P2)
- [#110](https://github.com/tosin2013/helmdeck/issues/110) — vault master-key rotation (P2)
- [#111](https://github.com/tosin2013/helmdeck/issues/111) — cross-version upgrade smoke in CI (P2)

None block v0.10.0. All are scheduled into Phase 7 alongside the existing T701-T715 work. v1.0 with Helm chart + Kubernetes deployment remains the headline target.

## What slipped, and what's next

The originally-planned v0.10.0 (Pack Authoring + Test Runner) didn't ship this cycle — the content packs were ready, the authoring tooling wasn't. That work moves to v0.11.0. Marketplace beta moves to v0.12.0.

If you want to track:
- Roadmap: [`docs/RELEASES.md`](https://github.com/tosin2013/helmdeck/blob/main/docs/RELEASES.md)
- Phase 7 audit + Kubernetes: [`docs/MILESTONES.md` Phase 7](https://github.com/tosin2013/helmdeck/blob/main/docs/MILESTONES.md)

## Try it

```bash
git clone https://github.com/tosin2013/helmdeck.git
cd helmdeck && git checkout v0.10.0
make install
```

Or wire it into Claude Code, Claude Desktop, OpenClaw, Gemini CLI, or Hermes Agent via the [registry-install guide](/howto/register-with-mcp-clients). Both new packs ship with reference documentation under `/reference/packs/blog/publish` and `/reference/packs/podcast/generate`.

If you ship a podcast or a Ghost post via helmdeck this week, [drop a link](https://github.com/tosin2013/helmdeck/issues/new) — we're starting to collect "made with helmdeck" examples.

## See also

- [v0.10.0 changelog](/changelog/) — full Added/Fixed/Changed list with PR links
- [Upgrade helmdeck](/howto/upgrade-helmdeck) — operator upgrade procedure
- [Register helmdeck with your MCP client](/howto/register-with-mcp-clients) — cross-client install via the official registry
- [Why a $0.10 model can do frontier work](/blog/cheap-models-do-frontier-work) — the cost-positioning post that landed last week
