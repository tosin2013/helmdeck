---
title: Use the helmdeck CLI
description: Operator reference for the `helmdeck` command — list, install, and manage marketplace packs from a terminal.
---

# Use the helmdeck CLI

`helmdeck` is the operator-facing CLI for a running helmdeck control plane. It wraps the same REST endpoints the Management UI uses ([T810 catalog](../reference/marketplace/catalog.md), T812 install/uninstall) so you can manage marketplace packs without leaving the terminal.

Same auth conventions as the `helmdeck-mcp` bridge:

| Env var | Required | Default | Notes |
|---|---|---|---|
| `HELMDECK_URL` | no | `http://localhost:3000` | Base URL of the control plane. |
| `HELMDECK_TOKEN` | **yes** | — | Bearer JWT issued from Management UI → API Tokens. |

## Install

`helmdeck` ships as a release artifact on every helmdeck release tag. Download from <https://github.com/tosin2013/helmdeck/releases>:

```sh
# Pick the right tarball for your OS/arch
curl -L -o helmdeck.tar.gz \
  https://github.com/tosin2013/helmdeck/releases/download/v0.13.0/helmdeck_0.13.0_linux_amd64.tar.gz
tar -xzf helmdeck.tar.gz
sudo mv helmdeck /usr/local/bin/
helmdeck --version
```

`scripts/install.sh` also drops the CLI into `$PATH` as part of the standard install path — if you went through that, `helmdeck` is already there.

## Commands

### `helmdeck pack list`

Lists every pack the control plane has registered — built-in core packs plus anything installed from the marketplace.

```sh
$ helmdeck pack list
NAME                  VERSION  DESCRIPTION
blog.publish          v1       Publish a post to a Ghost blog OR write rendered markdown/HT...
browser.screenshot…   v1       Take a screenshot of any URL. Returns a PNG artifact.
cmd.upper             v1       Uppercase a string. Smallest possible example pack.
hyperframes.render    v1       Render an HTML/CSS/JS composition into a deterministic MP4 v...
…

42 pack(s) registered
```

Equivalent to `GET /api/v1/packs`. Add `--json` for raw output.

### `helmdeck pack marketplace`

Browse the marketplace catalog. Default behavior reads the cached catalog from the control plane.

```sh
$ helmdeck pack marketplace
source: https://github.com/tosin2013/helmdeck-marketplace
fetched_at: 2026-05-15T18:42:11Z

NAME           VERSION  CATEGORY          AUTHOR     DESCRIPTION
cmd.lower      v1       developer-tools   tosin2013  Lowercase a string. Sibling of cmd.upper.
cmd.upper      v1       developer-tools   tosin2013  Uppercase a string. Smallest possible example…
cmd.wordcount  v1       developer-tools   tosin2013  Count lines, words, and characters in a string…

3 pack(s) in catalog
```

`--refresh` forces a fresh fetch from the upstream marketplace before listing (calls `POST /api/v1/marketplace/refresh`).

`--json` emits the raw catalog response.

### `helmdeck pack install <name>`

Install a marketplace pack into the running control plane. The pack appears in `tools/list` immediately — no restart.

```sh
$ helmdeck pack install cmd.upper
installed cmd.upper@v1
  install_dir:    /home/helmdeck/.helmdeck/packs/cmd.upper
  trust_verified: yes
  trust_note:     sha256 verified (bf2219701e87…); manifest declares signed_by=tosin2013 (full cosign identity verification deferred to stage B)
```

Trust outcomes:

- **`trust_verified: yes`** — the pack's `helmdeck-pack.yaml` declared a `trust.sha256`, and the installed bytes match it. See [§Trust model](../reference/marketplace/catalog.md#trust-model-v0130-ga-status).
- **`trust_verified: no`** — manifest didn't declare a hash, or declared one we couldn't verify against. The install still proceeded (unsigned packs are allowed); the note explains why.
- **Install rejected** — manifest declared a hash that **didn't match** what we just downloaded. Common cause: the marketplace upstream got compromised or you're pointed at the wrong fork. The CLI exits non-zero and surfaces the structured error.

### `helmdeck pack uninstall <name>`

Remove a marketplace pack from the registry and disk. Core packs cannot be uninstalled this way.

```sh
$ helmdeck pack uninstall cmd.upper
uninstalled cmd.upper
```

### `helmdeck pack installed`

List just the marketplace-installed packs (NOT core packs).

```sh
$ helmdeck pack installed
NAME       VERSION  INSTALLED_AT          TRUST
cmd.upper  v1       2026-05-15T18:42:11Z  verified

1 marketplace pack(s) installed
```

## Output flags

Every subcommand accepts `--json` for machine-readable output, useful for shell pipelines:

```sh
helmdeck pack installed --json | jq '.installed[] | .name'
helmdeck pack marketplace --json | jq '.index.packs[] | select(.category == "developer-tools") | .name'
helmdeck pack install cmd.upper --json | jq .pack.install_dir
```

**Flag order note**: Go's `flag` package stops parsing at the first positional argument. Put flags **before** the positional:

```sh
helmdeck pack install --json cmd.upper          # works
helmdeck pack install cmd.upper --json          # --json ignored
```

## Exit codes

| Code | When |
|---|---|
| `0` | Success. |
| `1` | Any error — argument validation, HTTP failure, install rejection, missing token. The error message names the actionable bit. |

## Troubleshooting

| Symptom | Cause |
|---|---|
| `HELMDECK_TOKEN is not set` | Create an API token at Management UI → API Tokens, then `export HELMDECK_TOKEN=...`. |
| `HTTP 401` on any command | Token expired or invalid. Re-create in the UI. |
| `marketplace_install_disabled` 503 | Control plane runs without `HELMDECK_PACKS_DIR` configured. See [marketplace install config](../reference/marketplace/catalog.md#install-configuration). |
| `marketplace_not_ready` 503 | First catalog fetch hasn't completed. Wait a few seconds, or `helmdeck pack marketplace --refresh`. |
| `install_failed` with `trust verification failed: sha256 mismatch...` | Stage A trust gate fired. The pack files don't match the hash the manifest declared. The marketplace upstream may be compromised or you're pointed at the wrong fork — investigate before installing. |
| `pack_not_in_catalog` 404 | The name isn't in the current cached catalog. Try `helmdeck pack marketplace --refresh` and confirm the spelling with `helmdeck pack marketplace`. |

## Related

- [Marketplace catalog reference](../reference/marketplace/catalog.md) — the REST endpoints this CLI wraps
- [ADR 034](../adrs/034-pack-marketplace.md) — pack marketplace design
- The Management UI's `/marketplace` panel for a graphical equivalent
- [`helmdeck-mcp`](https://github.com/tosin2013/helmdeck/blob/main/cmd/helmdeck-mcp/main.go) — the sibling MCP bridge binary, same env-var conventions
