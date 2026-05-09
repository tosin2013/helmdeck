---
slug: /howto/
title: How-to guides
description: Problem-solving recipes for common helmdeck tasks.
---

# How-to guides

Problem-solving recipes. Each guide assumes you already have helmdeck installed and answers a single, focused question: *how do I do X?*

## Operations

- **[Troubleshoot the install](./troubleshoot-install.md)** — symptom-first table for the known sharp edges in the install path: 502 on first session, GHCR pull failures, lost admin password, sidecar build hangs, blank UI panels, accidental `--reset`.
- **[Upgrade helmdeck](./upgrade-helmdeck.md)** — pre-flight checklist, in-place Compose-stack upgrade (`git pull && make install`), schema-migration handling, post-upgrade validation, and rollback. Previews the Helm/Kubernetes path coming with v1.0.
- **[Register helmdeck with your MCP client](./register-with-mcp-clients.md)** — one-line install via the official MCP Registry (`io.github.tosin2013/helmdeck`), stdio config snippet for any client, smoke test, and per-client lookup table covering Claude Code, Claude Desktop, OpenClaw, Gemini CLI, Hermes Agent, and Cursor.
- **[Watch the agent live via noVNC](./watch-agent-via-vnc.md)** — open a browser tab into a running sidecar and watch what the agent sees in real time. Quick-start desktop-mode session creation, three reachability paths (baas-net / port-forward / reverse-proxy with `HELMDECK_VNC_PUBLIC_BASE`), agent-status overlay, and security caveats. Useful for debugging vision packs and verifying desktop packs.
- **[Manage credentials in the vault](./manage-vault-credentials.md)** — create / grant / use / audit credentials of all 5 supported types (`login`, `cookie`, `api_key`, `oauth`, `ssh`). Worked examples for GitHub PAT, SSH deploy keys, Ghost Admin keys, ElevenLabs, site logins, and OAuth bundles.
- **[Configure LLM providers](./configure-llm-providers.md)** — register provider keys (Anthropic, OpenAI, Gemini, OpenRouter, Ollama, Groq, Mistral, Deepseek), list/rotate/test/delete via REST, configure fallback chains with the three closed-set triggers (`rate_limit`, `timeout`, `error`), and read the T607 success-rate panel.
- **[Inspect audit logs](./inspect-audit-logs.md)** — query patterns for the three audit tables (`audit_log`, `provider_calls`, `credential_usage_log`) with SQL examples for compliance exports, failure-pattern analysis, fallback-rate monitoring, and cost approximation.

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
