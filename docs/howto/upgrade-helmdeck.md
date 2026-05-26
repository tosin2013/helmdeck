---
title: Upgrade helmdeck
description: Operator-facing upgrade procedure — pre-flight checklist, in-place Compose-stack upgrade, schema-migration handling, post-upgrade validation, and rollback. Covers the `git pull && make install` path today and previews the Helm path coming with v1.0.
keywords: [helmdeck, upgrade, migration, rollback, helm, kubernetes, operator]
---

# Upgrade helmdeck

Operator-facing upgrade procedure. Today's reality is the **Compose-stack path** (`git pull && make install`); the **Kubernetes/Helm path** previews here and ships fully with v1.0 (Phase 7).

For client-side upgrades (OpenClaw, Claude Code, Gemini CLI, etc.) see [`integrations/openclaw-upgrade-runbook.md`](../integrations/openclaw-upgrade-runbook.md) and the per-client integration docs.

## 1. Pre-upgrade checklist

Run through this before starting any upgrade. ~5 minutes.

**Record the current version**:

```bash
# From your helmdeck checkout:
git describe --tags --always
# → v0.9.0  (or v0.9.0-39-gc76f707 if you're past a tag)

# Or via the running container:
docker inspect helmdeck-control-plane --format '{{.Config.Image}}'
# → ghcr.io/tosin2013/helmdeck:dev (build-time tag)

# Confirm the running binary's commit (logged once at startup):
docker logs helmdeck-control-plane 2>&1 | grep 'helmdeck control-plane starting' | tail -1
# → "version":"dev","commit":"unknown"   (when built locally without -ldflags)
# → "version":"v0.10.0","commit":"abcd123" (when built from a tag via the Makefile)
```

**Back up the SQLite database**. `helmdeck.db` carries vault credentials, audit logs, and the keystore — losing it is operator-facing data loss:

```bash
cp /var/lib/helmdeck/helmdeck.db /var/lib/helmdeck/helmdeck.db.bak-$(date +%Y%m%d-%H%M%S)
# OR if running via Compose with the `helmdeck-data` volume:
docker run --rm -v helmdeck-data:/data -v /tmp:/backup alpine \
  sh -c 'cp /data/helmdeck.db /backup/helmdeck.db.bak-$(date +%Y%m%d-%H%M%S)'
```

**Snapshot the vault credential names** (NOT the contents — secrets stay in vault):

```bash
JWT=$(./bin/control-plane -mint-token admin -mint-token-scopes admin)
curl -fsS http://localhost:3000/api/v1/vault/credentials \
  -H "Authorization: Bearer $JWT" \
  | python3 -c 'import sys,json; print("\n".join(c["name"] for c in json.load(sys.stdin)))' \
  > /tmp/helmdeck-creds-pre-upgrade.txt
wc -l /tmp/helmdeck-creds-pre-upgrade.txt
# Compare post-upgrade to confirm count matches.
```

**Read the new version's CHANGELOG entry**. Specifically scan for any `### Breaking` sub-sections:

```bash
git fetch --tags
git log --oneline v0.9.0..v0.10.0    # or whichever tag you're moving to
git show v0.10.0:CHANGELOG.md | head -80
```

Breaking changes usually mean: pack-input-schema changed, a vault credential name changed, or a runtime requirement bumped. For non-breaking minor releases (most), the procedure below is just `git pull && make install`.

---

## 2. In-place Compose-stack upgrade

This is the supported path on a single-host Compose deployment. Idempotent — re-running is safe. Pick the path that matches your initial install (see [`tutorials/install-cli.md`](../tutorials/install-cli.md) §"Pick your install mode").

### Path A — Image-mode upgrade (no Go/Node toolchain)

For operators who installed with `--image-mode`. Pulls the new pre-built images from ghcr.io.

```bash
cd /path/to/helmdeck
git fetch --tags
git checkout v0.12.0                                # or whatever tag you're moving to
# Pin the version so the compose env var matches the tag you just checked out.
# If HELMDECK_VERSION is already set in deploy/compose/.env.local, update it
# (or leave at "latest" to track the rolling tag — fine for non-production).
sed -i.bak 's/^HELMDECK_VERSION=.*/HELMDECK_VERSION=0.12.0/' deploy/compose/.env.local || \
  echo "HELMDECK_VERSION=0.12.0" >> deploy/compose/.env.local
./scripts/install.sh --image-mode                   # pulls new images, recreates the container
```

### Path B — Source-build upgrade (contributors / local changes)

For operators who installed from source. Rebuilds the control-plane image locally.

```bash
cd /path/to/helmdeck
git fetch --tags
git checkout v0.12.0           # or whatever tag you're moving to
make sidecars                   # rebuilds helmdeck-sidecar:dev with any new tools (ffprobe, ctags, …)
make install                    # idempotent: re-runs preflight, rebuilds control-plane image, recreates the container
```

What both paths do (post-checkout):

1. Re-run `scripts/install.sh` preflight — verifies Docker (and Go/Node only in source-build), sufficient memory, exposed ports.
2. Refresh the control-plane image — pull a tagged image (image-mode) **or** rebuild from source (source-build).
3. Recreate the `helmdeck-control-plane` container — **data volumes (`helmdeck-data`, `helmdeck-artifacts-garage`) persist**.
4. Start dependent services (Garage, Firecrawl/Docling overlays if previously enabled).

**Time**: ~1 minute on image-mode with warm Docker cache; ~3 minutes on source-build with warm cache; ~8 minutes on a cold rebuild.

**Brief downtime**: ~30 seconds while the control-plane container restarts. In-flight pack calls error out — operators with mid-flight workflows should wait for them to complete before running `make install`.

**Sidecar refresh**: The `make sidecars` step rebuilds `helmdeck-sidecar:dev`. **Sessions started before the upgrade keep their old sidecar image**; only sessions created after the upgrade pick up the new one. Either drop existing sessions (UI: *Sessions* panel → terminate) or wait for the 5-min watchdog to expire them.

After `make install` returns successfully, run §5 (Post-upgrade validation) before declaring done.

### After every release: re-stamp the OpenClaw skill

If you have an OpenClaw client wired in, re-run the configure script so the new SKILLS.md stamps into the OpenClaw container:

```bash
./scripts/configure-openclaw.sh
```

See [`docs/RELEASES.md`](../RELEASES.md) §"Agent sync checklist — every release" for the full per-release checklist (operator + agent-side both).

---

## 3. Schema migrations

helmdeck embeds its SQL migrations into the binary (`internal/store/migrations/*.sql`) and applies any not-yet-applied migrations **automatically on every startup** via `store.Open`. The procedure is:

1. Open the database file
2. Read the `schema_migrations` table to determine the highest-applied version
3. Apply any newer files in version order, each in a transaction
4. Record the new version in `schema_migrations`

**You don't run `migrate up` manually** — it happens at boot. If a migration fails, `helmdeck-control-plane` refuses to start and logs the SQL error; revert to the prior version (§6 Rollback) and file an issue.

**To verify migrations applied** post-upgrade:

```bash
docker exec helmdeck-control-plane sqlite3 /data/helmdeck.db \
  'SELECT version, applied_at FROM schema_migrations ORDER BY version'
```

You should see one row per migration in `internal/store/migrations/`. If a version is missing AND the binary started cleanly, that migration is no-op (e.g. an additive `CREATE TABLE IF NOT EXISTS`) — not a problem.

**Note**: This auto-apply behavior makes upgrades safe but rollbacks tricky — see §6.

---

## 4. Kubernetes / Helm path (preview, GA in v1.0)

> ⚠️ **Coming with v1.0 (Phase 7)**. The full Helm chart, KEDA scaling, NetworkPolicy isolation, External Secrets integration, and OpenTelemetry Collector ship with [milestone v1.0](https://github.com/tosin2013/helmdeck/milestone/7). The procedure below is the planned shape; it may change before GA. Track progress on [#5 (Helm chart)](https://github.com/tosin2013/helmdeck/issues/5) and [#7 (pod template)](https://github.com/tosin2013/helmdeck/issues/7).

The eventual K8s upgrade flow:

```bash
# Install (first time):
helm repo add helmdeck oci://ghcr.io/tosin2013/charts
helm install helmdeck helmdeck/baas-platform \
  --namespace helmdeck --create-namespace \
  --values my-values.yaml

# Upgrade in place:
helm upgrade helmdeck helmdeck/baas-platform \
  --namespace helmdeck \
  --values my-values.yaml \
  --version 1.1.0
```

The Helm chart will:

- Run schema migrations as a `Job` before the `Deployment` rolls (so a failed migration aborts the rollout instead of taking down the running pods)
- Roll the `helmdeck-control-plane` Deployment with `maxSurge=1, maxUnavailable=0` — zero-downtime upgrade
- Trigger sidecar-image refresh by bumping the `image.tag` value (sidecar pods are spun up per-session, so existing sessions drain naturally)
- Preserve the PostgreSQL StatefulSet (or external DB pointer) across the upgrade
- Use Helm's built-in `helm rollback` for one-step rollback (see §6)

Until v1.0 ships, operators in production should use the Compose path with the documented database backups in §1.

---

## 5. Post-upgrade validation

After the new control-plane container is up, run these checks. They take ~2 minutes total.

```bash
# (1) Healthz
curl -fsS http://localhost:3000/healthz
# → {"status":"ok"}

# (2) New pack count matches expectations
JWT=$(./bin/control-plane -mint-token admin -mint-token-scopes admin)
curl -fsS http://localhost:3000/api/v1/packs -H "Authorization: Bearer $JWT" \
  | python3 -c 'import sys,json; print(len(json.load(sys.stdin)), "packs")'
# v0.9.0 → 36; v0.10.0 → 38; cross-check against the new release's docs/PACKS.md

# (3) Vault credential count unchanged
curl -fsS http://localhost:3000/api/v1/vault/credentials -H "Authorization: Bearer $JWT" \
  | python3 -c 'import sys,json; print(len(json.load(sys.stdin)), "creds")'
# Compare to the count from the pre-upgrade snapshot in §1

# (4) Smoke pack call (no external dependencies)
curl -fsS -X POST http://localhost:3000/api/v1/packs/browser.screenshot_url \
  -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
  -d '{"url":"https://example.com"}' \
  | python3 -m json.tool | head -10
# Should return an artifact_key + size > 50000

# (5) Audit log has fresh startup entry
docker logs helmdeck-control-plane 2>&1 | grep 'control-plane starting' | tail -1
# → "version":"v0.10.0","commit":"<short-sha>"
```

If any check fails, gather logs (`docker logs helmdeck-control-plane`) and consider rolling back (§6).

---

## 6. Rollback

If the upgrade misbehaves and you need to revert:

### Compose path

```bash
cd /path/to/helmdeck
git checkout v0.9.0      # or the prior known-good tag
make install
```

**Database compatibility**: helmdeck's migrations are **additive** by convention — new versions add tables/columns; they don't drop or alter columns the prior version reads. So a v0.9.0 binary running against a database that v0.10.0 migrated should work, ignoring the new columns. Exception: if the new version's migrations include a destructive change (rare; flagged in the CHANGELOG `### Breaking` section), restore the database backup from §1:

```bash
docker compose -f deploy/compose/compose.yaml stop control-plane
cp /var/lib/helmdeck/helmdeck.db.bak-<timestamp> /var/lib/helmdeck/helmdeck.db
docker compose -f deploy/compose/compose.yaml start control-plane
```

### Kubernetes path (v1.0+)

```bash
helm rollback helmdeck    # one revision back
helm rollback helmdeck 5  # specific revision number
helm history helmdeck     # see all revisions
```

Helm tracks revisions; a rollback runs the previous chart version's `Job` (which is a no-op for additive migrations or reverses the migration if the chart shipped a `down.sql` — most don't).

---

## 7. Version-specific notes

`CHANGELOG.md` is the canonical source. Pre-upgrade, scan the section for the version you're moving to:

| Section | Means |
|---|---|
| `### Added` | New packs / endpoints / fields. Backward-compatible. |
| `### Changed` | Behavior shifts that may surprise existing callers but don't error. Read carefully. |
| `### Fixed` | Bug fixes. Usually safe; sometimes a fix changes observable behavior (e.g. PR #105 fixed `vision.click_anywhere` to actually use post-action screenshots — agents written against the broken behavior may need their prompts adjusted). |
| `### Breaking` | Schema or contract changes that will break existing integrations. Operator action required. RARE in helmdeck — we try to keep input/output schemas additive. |
| `### Removed` | Features dropped. Will break callers that used them. Should be preceded by a deprecation notice in a prior release. |

### Per-hop notes (most recent first)

**v0.13.x → v0.14.0**: non-breaking, but **one recommended `.env.local` addition**. This release ships the Universal Memory layer (ADR 039) and persistent repos (ADR 040). Memory works out of the box, but to make it **durable across restarts** you must pin a key — fresh `scripts/install.sh` runs now generate it, but an in-place upgrade reuses your existing `.env.local`, which predates the key. Add it once:

```bash
grep -q '^HELMDECK_MEMORY_KEY=' deploy/compose/.env.local || \
  echo "HELMDECK_MEMORY_KEY=$(openssl rand -hex 32)" >> deploy/compose/.env.local
```

Without it the control plane autogenerates an ephemeral key (logs a warning) and memory entries are wiped on every restart. The new SQLite migration `0006_memory_entries.sql` is additive (`CREATE TABLE`, auto-applied on boot). Persistent repos is enabled by default in the bundled Compose (new `helmdeck-repos` volume mounted at `/repos`); to opt out, unset `HELMDECK_PERSISTENT_REPOS`. New built-in pack `swe.solve` (`helmdeck__swe.solve`). No removed fields, no closed-set value changes.

**v0.12.x → v0.13.0** (this is the headline release as of the May 2026 cycle): non-breaking. Introduces the **community pack marketplace** as a new opt-in surface — three REST endpoints (`/api/v1/marketplace/{catalog,install,uninstall}`), a `/marketplace` UI panel, and a new `helmdeck` CLI binary that wraps the install loop from a terminal. Two new sidecar images you'll want pulled before first marketplace install: `ghcr.io/tosin2013/helmdeck-sidecar-marketplace:0.13.0` (the default execution sandbox for installed marketplace packs — bash + jq + curl + python3 + Node 20; see ADR 038 for why) and `ghcr.io/tosin2013/helmdeck-sidecar-hyperframes:0.13.0` (for the new `hyperframes.render` HTML→MP4 pack — Node 22 + ffmpeg). Catalog fetching is on by default; disable with `HELMDECK_MARKETPLACE_DISABLE=1` in `.env.local` if you don't want the control plane reaching out to GitHub on boot. Two new built-in packs (`hyperframes.render`, `stock.search`) bumping count 39 → 41. SQLite migration `0005_provider_calls_diagnostics.sql` adds three columns to `provider_calls` (`job_id`, `finish_reason`, `raw_content_len`) via `ALTER TABLE ADD COLUMN` — O(1) metadata-only, safe even on multi-million-row tables. `blog.publish`'s `destination` is now optional (defaults to `"artifact"`); previously-passing `destination="ghost"` callers gain `artifact_key`/`artifact_url` in the response and now return a partial-success envelope on Ghost failures instead of erroring out. No removed fields, no closed-set value changes.

**v0.9.0 → v0.10.0:** non-breaking. Adds `blog.publish` + `podcast.generate` packs, fixes `vision.click_anywhere` per #102 (improvement; existing callers see better behavior), bumps pack count 36 → 38. No schema-removal, no input-shape change to existing packs.

---

## See also

- [`docs/RELEASES.md`](../RELEASES.md) — release plan, exit gates, agent sync checklist
- [`CHANGELOG.md`](https://github.com/tosin2013/helmdeck/blob/main/CHANGELOG.md) — what shipped per version
- [`docs/howto/troubleshoot-install.md`](./troubleshoot-install.md) — fresh-install troubleshooting (also useful when an upgrade misbehaves)
- [`docs/integrations/openclaw-upgrade-runbook.md`](../integrations/openclaw-upgrade-runbook.md) — OpenClaw-specific client-side upgrade (separate from helmdeck-side)
- [Milestone v1.0 (Phase 7)](https://github.com/tosin2013/helmdeck/milestone/7) — the Kubernetes / Helm path
