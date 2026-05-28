---
title: helmdeck-debug — the integration debugger skill
description: An installable agent skill that sweeps every helmdeck pipeline + pack (static checks + a live run sweep), classifies failures, and drafts a ready-to-file GitHub issue per real bug — confirm before filing.
keywords: [helmdeck, debug, audit, pipelines, packs, failure_class, GitHub issues, skill, OpenClaw, Claude Code]
---

# helmdeck-debug

`helmdeck-debug` is a second agent skill (alongside the main `helmdeck` skill)
that turns "audit my helmdeck deployment" into a repeatable runbook. Invoke it
and the agent sweeps **every** pipeline and capability pack, classifies each
failure, and hands you a findings report with a **ready-to-file GitHub issue per
real bug**. It never files anything until you say which.

Source: [`skills/helmdeck-debug/SKILL.md`](https://github.com/tosin2013/helmdeck/blob/main/skills/helmdeck-debug/SKILL.md).

## What it checks

Two passes, gated by what's available (the skill detects its mode and tells you
which passes it ran):

- **Static / behavioral pass** (needs a source checkout) — reads the pipeline +
  pack definitions and flags four recurring bug classes:
  1. **Oversold descriptions** — a `Description` promising more than the steps do
     (e.g. "rewrite/publish" when the pack only cites + saves an artifact).
  2. **Silent-bad-output inputs** — an input that reaches user-visible output with
     no guard (the `{{TITLE}}`-published-as-a-title class).
  3. **Schema vs handler drift** — a pack's declared `OutputSchema` ≠ what its
     handler emits (the kind handler-direct unit tests miss).
  4. **Failure misclassification** — a caller-input error returned as a code-level
     `pack_bug`, which would mint a bogus issue.
- **Live end-to-end pass** (needs a running control-plane) — runs every pipeline
  via REST with curated safe inputs, polls to terminal, and judges by the
  authoritative `failure_class`: `pack_bug` → draft an issue;
  `caller_fixable`/`transient`/`state_changed` → "ran, not a bug" (reported, not
  filed). This keeps a keyless stack from generating bogus drafts.

The live sweep never runs write-op packs (`github.create_issue`, `repo.push`,
`email.send`, …). A full sweep is 10–20+ minutes and spends LLM/ElevenLabs/fal
credits (narrate/video pipelines are minutes each); the skill offers a quick
pipelines-light mode and warns first.

## Draft, then confirm

The skill produces one report — a summary table plus a ready-to-file issue block
per finding (title/body/`labels: bug`, with a repro) reusing the same format as
helmdeck's built-in failure-attribution issue links. Then it **stops** and asks
which to file. Only on explicit confirmation does it run `gh issue create` (or the
`github.create_issue` pack) against `tosin2013/helmdeck`, deduping against open
issues first.

## Install

The skill is distributed with the main `helmdeck` skill — both installers pick up
everything under `skills/*/SKILL.md`:

- **OpenClaw:** `./scripts/configure-openclaw.sh` stamps every skill into the
  agent's managed-skill root (`~/.openclaw/skills/`). Use `--skill helmdeck-debug`
  to install just this one.
- **Claude Code:** `./scripts/configure-claude.sh --project <dir>` installs every
  skill as an invocable skill under `<dir>/.claude/skills/`. Use
  `--skill helmdeck-debug` to install just this one. (See
  [claude-code.md](./claude-code) §3.)

After any helmdeck release, re-run the installer so the `helmdeckVersion` stamp
and the pipeline/pack coverage refresh.

## Invoke

Ask the agent: *"run the helmdeck integration debugger"* (or *"audit the helmdeck
pipelines and draft issues for anything broken"*). Set `HELMDECK_URL` (default
`http://localhost:3000`) and admin credentials (`HELMDECK_USER`/`HELMDECK_PASS`,
or `HELMDECK_ADMIN_PASSWORD` in `deploy/compose/.env.local`) so the live pass can
authenticate.
