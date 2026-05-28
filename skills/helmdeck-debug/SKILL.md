---
name: helmdeck-debug
description: Audit/debug a helmdeck deployment. When a maintainer asks to "debug helmdeck", "audit the integrations", "health-check the pipelines", or "find bugs to file", run a diagnostic sweep across EVERY pipeline and capability pack — a static check of the definitions plus a live end-to-end run sweep — classify each failure by failure_class, and draft a ready-to-file GitHub issue per real bug. Never file anything without explicit confirmation.
metadata:
  openclaw:
    skillKey: helmdeck-debug
    helmdeckVersion: "v0.19.1"
    source: https://github.com/tosin2013/helmdeck/blob/main/skills/helmdeck-debug/SKILL.md
---

<!-- Canonical helmdeck integration-debugger skill. Re-run
     scripts/configure-openclaw.sh (OpenClaw) or scripts/configure-claude.sh
     (Claude Code) after any helmdeck release to re-stamp helmdeckVersion and
     pick up new pipelines/packs. The companion `helmdeck` skill teaches an
     agent to USE the packs; this skill teaches an agent to DEBUG them. -->

## You are the helmdeck integration debugger

When invoked, you run a diagnostic sweep across **all** helmdeck pipelines and
capability packs, classify what you find, and produce a **findings report** with
one ready-to-file GitHub issue per real bug. You **draft** issues — you do not
file them until the maintainer says which to file.

Trigger this when a maintainer asks to debug / audit / health-check helmdeck, or
"find issues to file." For normal pack usage, that's the separate `helmdeck`
skill.

## The bug classes you are hunting

These are the recurring, real classes (each has bitten this project). Look for
all four:

1. **Oversold descriptions** — a pipeline/pack `Description` promises more than
   its steps actually do. Canonical example: `builtin.grounded-blog` once said
   "fact-check + rewrite + publish" but `content.ground` only *cites* claims (it
   does not rewrite voice/structure) and `blog.publish` *saves a markdown
   artifact by default* (no Ghost credential → it does not publish anywhere).
2. **Silent-bad-output inputs** — an input that flows into user-visible output
   without a guard, so a bad value produces bad output instead of an error.
   Canonical example: a literal `{{TITLE}}` once published a post titled
   `{{TITLE}}`. (The runner now rejects unfilled `{{UPPER_SNAKE}}` inputs in
   `StartRun` — verify that guard still covers every pipeline.)
3. **Schema vs handler drift** — a pack's declared `OutputSchema` doesn't match
   what its handler actually emits (key missing, or wrong type — e.g. declaring
   `number` for a field the handler emits as an object). Handler-direct unit
   tests bypass `Engine.Execute`, so `OutputSchema.Validate` never runs on them.
4. **Failure misclassification** — a pack returns a code-level error
   (`handler_failed`/`internal`/`invalid_output`) for what is really *caller*
   input. `internal/pipelines/classify.go` maps those to `pack_bug` and mints a
   prefilled issue URL — so a misclassified caller error files a bogus bug.

## Step A — detect your mode (do this first, report it)

Three capabilities gate which passes you can run. Probe and state plainly what
you will and won't do:

- **Source checkout present?** Look for `internal/pipelines/seed.go` and
  `internal/packs/builtin/`. Required for the **static pass** (classes 1–4 need
  to read the definitions — REST does NOT expose pack input/output schemas).
  No source → say "static pass skipped — no source checkout."
- **Control-plane reachable?** `GET {HELMDECK_URL}/healthz` (expect
  `{"status":"ok"}`) and `GET {HELMDECK_URL}/version`. Required for the **live
  pass**. `HELMDECK_URL` defaults to `http://localhost:3000`.
- **OpenClaw present?** Only needed for the optional deeper agent-round-trip
  sweep (see the note in Step C).

## Step B — static / behavioral pass (needs source)

Enumerate, then check the four classes.

**Enumerate:**
- Pipelines: read `internal/pipelines/seed.go` → `Builtins()`.
- Packs: list `internal/packs/builtin/*.go` (the `Name:` field of each
  `*packs.Pack`).
- If the control-plane is up, cross-check against the live catalog
  (`GET /api/v1/pipelines`, `GET /api/v1/packs`) and flag any **seed ↔ registry
  drift** (a builtin in one but not the other).

**Class 1 — oversold descriptions.** For each pipeline in `seed.go`, compare its
`Description` string to the actual `step(...)` packs and their inputs. Red flags:
"rewrite" while a `content.ground` step is `rewrite:false`; "publish" while the
terminal `blog.publish` step has no `credential`/`host` (it saves an artifact);
any verb the steps don't perform. Use the #321 ground/blog wording as the bar
for "honest." Do the same for each pack's own `Description`.

**Class 2 — silent-bad-output inputs.** For every `${{ inputs.* }}` reference in
`seed.go`, confirm the `StartRun` placeholder guard (`internal/pipelines/runs.go`,
`validateInputsFilled` / `unfilledPlaceholderRE`) still applies — it runs for all
pipelines centrally, so the check is "does any new input path bypass it?" Flag
any input that lands in a user-visible field (title/body/filename) with no
validation.

**Class 3 — schema vs handler.** For each pack `*.go`, read its `OutputSchema`
declaration and the map its handler actually returns; flag mismatches (missing
key, declared type ≠ emitted type). Then flag any `Async:true` or
handler-direct-tested pack that lacks a contract test asserting real output
matches the schema (see `internal/packs/builtin/output_schema_contract_test.go`
for the pattern). These are the cases `Engine.Execute`'s validation never sees in
unit tests.

**Class 4 — misclassification.** Read `internal/pipelines/classify.go`. For any
pack that returns `CodeHandlerFailed`/`CodeInternal`/`CodeInvalidOutput` on what
is actually caller input (wrong shape, missing field, bad URL), flag it — it
would be classified `pack_bug` and mint a bogus prefilled issue. Corroborate with
the live pass: a pipeline that fails `pack_bug` on the obviously-safe fixture
inputs below is a prime class-4 (or genuine class-3) candidate.

## Step C — live end-to-end sweep (needs control-plane)

REST-direct is the primary path (works anywhere a control-plane is up, judges by
the authoritative `failure_class`).

1. **Authenticate.** `POST {HELMDECK_URL}/api/v1/auth/login`
   `{"username":"<admin>","password":"<pass>"}` → `{token}`. Creds from
   `HELMDECK_USER`/`HELMDECK_PASS` env, or `HELMDECK_ADMIN_PASSWORD` in
   `deploy/compose/.env.local`. Send the token as `Authorization: Bearer <token>`.
2. **Enumerate live.** `GET /api/v1/pipelines` → list of ids (don't hardcode —
   new/community pipelines auto-covered).
3. **Run each.** `POST /api/v1/pipelines/{id}/run` with
   `{"inputs": <minimal-safe inputs>}` → `run_id`. Use the fixture table below;
   for a pipeline not in it, read its `${{ inputs.* }}` refs via
   `GET /api/v1/pipelines/{id}` and synthesize a minimal value of the right shape
   (default strings to the `example.com` / `Hello-World` fixtures).
4. **Poll.** `GET /api/v1/pipelines/{id}/runs/{run_id}` every ~5s until `status`
   is `succeeded` or `failed` (deadline ~900s — narrate/video pipelines take
   minutes).
5. **Judge (verbatim rule — do not invent a stricter one):**
   - `succeeded` → pass.
   - `failed` + `failure_class == pack_bug` → **real bug → draft an issue.**
   - `failed` + `failure_class ∈ {caller_fixable, transient, state_changed}` →
     "ran, not a bug" — report it but **do NOT draft an issue.** The wiring
     worked; the input / environment / outside world was the cause. (This is what
     keeps a keyless stack — no Firecrawl/ElevenLabs/Ghost/fal — from generating
     a flood of bogus drafts.)

**Minimal-safe pipeline inputs** (kept in sync with
`scripts/validate-openclaw.sh`):

| pipeline id | inputs |
|---|---|
| `builtin.grounded-deck` | `{"markdown":"# Helmdeck\n\nHelmdeck runs browser automation and capability packs inside sandboxed containers."}` |
| `builtin.grounded-blog` | `{"markdown":"# Helmdeck\n\nHelmdeck runs browser automation in sandboxed containers.","title":"Helmdeck overview"}` |
| `builtin.research-deck` | `{"query":"mnemonics memory techniques"}` |
| `builtin.research-narrate` | `{"query":"mnemonics memory techniques"}` |
| `builtin.research-podcast` | `{"query":"mnemonics memory techniques"}` |
| `builtin.scrape-ground-blog` | `{"url":"https://example.com","title":"Example"}` |
| `builtin.research-ground-deck` | `{"query":"mnemonics memory techniques"}` |
| `builtin.doc-ground-blog` | `{"source_url":"https://example.com","title":"Example"}` |
| `builtin.scrape-deck` | `{"url":"https://example.com"}` |
| `builtin.research-blog` | `{"query":"mnemonics memory techniques","title":"Mnemonics"}` |
| `builtin.repo-presentation` | `{"repo_url":"https://github.com/octocat/Hello-World.git"}` |
| `builtin.repo-readme-podcast` | `{"repo_url":"https://github.com/octocat/Hello-World.git"}` |
| `builtin.html-video` | `{"composition_html":"<html><body><h1>Hello from helmdeck</h1></body></html>"}` |

**Never run these during a sweep** (write ops that create real external
resources): `github.create_issue`, `github.post_comment`, `github.create_release`,
`repo.push`, `email.send`. Test those by hand against throwaway resources.

**Cost/time + optional deeper sweep.** A full pipeline sweep is 10–20+ minutes and
spends LLM/ElevenLabs/fal credits (the narrate/video pipelines are minutes each).
Offer a quick **pipelines-light** mode (skip `research-*`, `*-narrate`,
`repo-presentation`, `html-video`) and warn before a full sweep. When OpenClaw is
present and the maintainer wants the per-pack agent-round-trip coverage, shell out
to `bash scripts/validate-openclaw.sh --pipelines-only` (or `--pack <name>`) and
parse its pass/bug tallies — do not reimplement it.

## Step D — findings report, then draft-then-confirm

Assemble **one** markdown report:

1. **Summary table:** target (pipeline/pack), source (`static` | `live`), bug
   class (1–4), `failure_class` (live), severity, one-line finding.
2. **One "ready-to-file issue" block per real bug** — exactly what you would pass
   to `gh issue create`, in the same shape `classify.go` uses:
   - **title:** `pack <name> failed (<error_code>)` for live pack bugs, or
     `[<pipeline-or-pack>] <one-line>` for static findings.
   - **labels:** `bug` (add `area/packs` or `area/pipelines` when clear).
   - **body:** the pack, error code, message (≤300 chars), and a **repro** — the
     exact pipeline id + inputs you ran (live), or the file path + the
     definition/handler mismatch (static).
3. Then **STOP** and say plainly: *"I have NOT filed anything. Reply with which
   issues to file (e.g. 'file 1 and 3', or 'file all')."*

Only on explicit confirmation, file the chosen issues — one at a time — via the
`gh` CLI (`gh issue create --title … --body … --label bug`, repo
`tosin2013/helmdeck`) when present, else the `helmdeck__github_create_issue` pack
with `repo: tosin2013/helmdeck`. **Dedup first:** `gh issue list --search "<title>"
--state open` (or `github.list_issues`) and skip / link rather than duplicate.

### DO draft an issue when (real bugs):
- A pack returns `internal` (a helmdeck bug, not user error).
- Output doesn't match the documented schema (class 3).
- A pipeline fails `pack_bug` on the safe fixture inputs above.
- A pack silently returns empty/wrong output for valid input (class 2).
- A description materially misrepresents what the steps do (class 1).
- A caller-input error is returned as a code-level/`pack_bug` error (class 4).

### DON'T draft an issue when:
- An overlay is disabled (`HELMDECK_*_ENABLED` unset) — configuration.
- A vault credential is missing — setup.
- The model returned unparseable output — an LLM issue, not helmdeck.
- The run failed `caller_fixable`/`transient`/`state_changed` — the wiring worked.
- The error message already tells the user exactly what to do.

## Safety
- Never file a GitHub issue (or run any write-op pack) without explicit
  confirmation of *which* items to file.
- Redact any credentials/tokens from repro inputs and error messages before they
  land in an issue body.
- The sweep is read-only against the world except for the packs it runs with safe
  fixtures; the write-op skip list above is non-negotiable.
