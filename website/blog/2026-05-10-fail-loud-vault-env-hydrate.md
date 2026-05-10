---
slug: fail-loud-vault-env-hydrate
title: "Fail loud: how a silent ElevenLabs fallback hid a credential bug — and the platform fix that closed the class"
authors: [tosin]
tags: [friction, agent-architecture, security]
description: A vault-backed pack returned HTTP 200 with a silent MP3 when the credential lookup missed. The per-pack fix surfaced the error; the platform fix made the class of bug impossible.
date: 2026-05-10
draft: false
---

## Hook

For a week, every `podcast.generate` call returned **HTTP 200 with `has_narration: false` and an MP3 made entirely of silence**. No log line, no error, just a quietly broken artifact you only noticed by listening to it. The fix landed in v0.11.0 as two PRs that close the bug at two layers: one **fails loud** at the pack contract, the other **closes the class** at the platform.

<!-- truncate -->

## Context

helmdeck v0.10.2 shipped two new content packs — `podcast.generate` and `slides.narrate` — both backed by ElevenLabs TTS. Both read the API key from helmdeck's vault under the canonical name `elevenlabs-key`. The README told operators to set `HELMDECK_ELEVENLABS_API_KEY` in `deploy/compose/.env.local`. Reasonable assumption: the env var would somehow find its way into the vault.

It didn't. The packs read vault-only; the env var went unused. When the lookup failed, the silent-fallback path emitted a 5-second `anullsrc` segment per turn and called it a success. The "podcast" came back as silence. We discovered this by `ffprobe`-ing the artifact and finding `Stream #0: Audio: mp3, anullsrc`.

The full bug was filed as [#138](https://github.com/tosin2013/helmdeck/issues/138) (per-pack contract) and [#142](https://github.com/tosin2013/helmdeck/issues/142) (platform fix).

## Finding

There were two ways to fix this. Most teams pick one. We picked both, because they answer different questions.

### Layer 1 — fail loud at the pack contract (#138)

The `podcast.generate` handler used to be:

```go
// Pre-this-change: silent fallback was the default.
res, err := vs.ResolveByName(ctx, vault.Actor{Subject: "*"}, "elevenlabs-key")
if err == nil {
    apiKey = string(res.Plaintext)
}
// Errors silently — fallback to anullsrc-padded silent MP3.
hasNarration := apiKey != ""
```

That comment is the smoking gun: the design intent was "silent fallback by design." But "by design" doesn't help an operator who did everything the README said and still got broken artifacts.

The fix inverts the default. Resolution now walks a four-step ladder:

1. Explicit `credential` input
2. Vault entry `elevenlabs-key` (the canonical name)
3. Vault entry `elevenlabs-api-key` (back-compat alias for the README-natural naming)
4. `os.Getenv("HELMDECK_ELEVENLABS_API_KEY")` (last-resort)

If none yield a key, the pack returns:

```json
{
  "code": "missing_credential",
  "message": "ElevenLabs key not found. Set HELMDECK_ELEVENLABS_API_KEY in deploy/compose/.env.local — it auto-imports into the vault as 'elevenlabs-key' on startup (#142). Or POST a credential named 'elevenlabs-key' to /api/v1/vault/credentials. To produce a silence-padded MP3 instead (CI smoke / placeholder use), pass `\"allow_silent_output\": true`."
}
```

Three things that error message gets right:

- **It tells you what to do**, not just what failed.
- **It distinguishes the operator-error case from the deliberate-CI-smoke case** with an explicit opt-in (`allow_silent_output: true`).
- **It points at the platform fix that should have made it unnecessary**.

That last point is the connective tissue to layer 2.

### Layer 2 — close the bug class at the platform (#142)

Per-pack fixes patch instances. They don't prevent the next pack from making the same mistake. We're going to ship `image.generate` (Replicate / Flux / SDXL), `voice.clone` (ElevenLabs / PlayHT), `web.search` (Tavily / Serper) — every one needs a service key, and every one would tempt a developer to write the same `os.Getenv` shortcut, or worse, the same silent-fallback.

The vault now hydrates well-known credentials from env vars at startup:

```go
var WellKnownEnvCredentials = []EnvCredential{
    {
        EnvVar:      "HELMDECK_ELEVENLABS_API_KEY",
        EnvVarFile:  "HELMDECK_ELEVENLABS_API_KEY_FILE", // docker-secret path
        Name:        "elevenlabs-key",
        Type:        TypeAPIKey,
        HostPattern: "api.elevenlabs.io",
    },
    // future packs add their service here — one source of truth.
}

func (s *Store) HydrateFromEnv(ctx context.Context, logger *slog.Logger, lookup EnvLookup) (created, updated, skipped int) {
    for _, c := range WellKnownEnvCredentials {
        // ... resolve env, skip if user-managed, upsert otherwise,
        //     auto-grant wildcard ACL on first create.
    }
}
```

`docker logs helmdeck-control-plane | grep "vault env hydrate"` now reveals exactly which credentials loaded:

```
vault env hydrate ok name=elevenlabs-key host=api.elevenlabs.io action=create
vault env hydrate done created=1 updated=0 skipped=0
```

The skip case is load-bearing: if an operator deliberately created `elevenlabs-key` via the UI (no `source: env-hydrate` metadata), subsequent restarts must not clobber that. The hydration is metadata-aware — it only re-upserts entries it itself created.

## Why-it-matters-to-the-reader

The temptation in helmdeck's position — pre-1.0, fast iteration, not-yet-paying customers — is to fix bugs at the cheapest layer. The silent-MP3 bug had a one-line fix: `if apiKey == "" { return PackError }`. We could have shipped that and moved on.

That fix would have been correct. It also would have left the **next** pack to repeat the same plumbing. Per-pack `os.Getenv` shortcuts. Per-pack "did the credential resolve?" comments. A growing collection of paper-cuts that look identical from the operator's seat but live in N different files.

Closing the bug **class** — putting the env-var → vault bridge into the platform — costs more upfront. It also means the next pack just registers an entry in `WellKnownEnvCredentials` and inherits the audit log, the user-managed-skip safety, the ACL grant, and the operator-visible startup log line for free. The cost amortizes over every future TTS / image / search-API pack.

The honest version of the lesson:

| Layer | Cost | Closes |
|---|---|---|
| Per-pack fail-loud | 1 PR, ~50 lines | The instance in this pack |
| Platform env-hydrate | 1 PR, ~150 lines + 1 new vault method | The class across all current and future packs |

Skip layer 1 and the second pack is broken until you write its fix. Skip layer 2 and you'll write that fix five times.

## CTA

If you hit a `podcast.generate` or `slides.narrate` failure in v0.10.x, upgrade to v0.11.0 — both fixes ship together. The implementation is in [PR #150](https://github.com/tosin2013/helmdeck/pull/150) (vault env-hydrate) and [PR #151](https://github.com/tosin2013/helmdeck/pull/151) (pack contract change). The `WellKnownEnvCredentials` registry is the right place to land the next service-key mapping when you build a new pack — find it at `internal/vault/hydrate.go`.

If your team is shipping its own MCP servers and hitting "the env var I documented isn't being read," consider this pattern. The cheapest fix is rarely the smallest one.
