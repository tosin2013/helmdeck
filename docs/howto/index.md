---
slug: /howto/
title: How-to guides
description: Problem-solving recipes for common helmdeck tasks.
---

# How-to guides

Problem-solving recipes. Each guide assumes you already have helmdeck installed and answers a single, focused question: *how do I do X?*

## Client integrations

Wire helmdeck into your MCP-capable client of choice:

- **[Claude Code](../integrations/claude-code.md)** — install + sidecar wiring + Phase 5.5 code-edit loop.
- **[Claude Desktop](../integrations/claude-desktop.md)** — Mac/Windows desktop client.
- **[Gemini CLI](../integrations/gemini-cli.md)** — Google Gemini's terminal client.
- **[Hermes Agent](../integrations/hermes-agent.md)** — self-hosted agent runner.
- **[OpenClaw](../integrations/openclaw.md)** — open-source Claude-compatible agent.
- **[OpenClaw sidecar research](../integrations/openclaw-sidecar-research.md)** — running OpenClaw alongside helmdeck.
- **[OpenClaw upgrade runbook](../integrations/openclaw-upgrade-runbook.md)** — version-bump procedure.
- **[OpenClaw upstream issue tracker](../integrations/openclaw-upstream-issue.md)** — known regressions, workarounds.
- **[Nemoclaw](../integrations/nemoclaw.md)** — Nemo's agent client.
- **[Webhooks](../integrations/webhooks.md)** — pushing pack results to external systems.

## Sidecars

Extend helmdeck's sandboxed execution surface:

- **[Extending a sidecar](../SIDECAR-EXTENDING.md)** — add a new capability to an existing sidecar image.
- **[Adding sidecar languages](../SIDECAR-LANGUAGES.md)** — ship a new language runtime (Ruby, Java, …) as a four-file contribution.

## What goes here

How-to guides are *problem-oriented*. Unlike tutorials, they assume the reader has prior context and is looking for the shortest path between a real problem and its solution. They list trade-offs but don't teach foundations — that's what tutorials are for.
