---
slug: /reference/
title: Reference
description: Information-oriented lookup material — pack contracts, ADRs, project tracking.
---

# Reference

Information-oriented lookup. These pages are precise, complete, and dry — optimized for the reader who already knows what they're looking for.

## Architecture

- **[Architecture overview](./architecture.md)** — canonical reference for decision makers and architects. Five views: components, request flow, deployment topology, trust boundaries, scaling. Mermaid diagrams + source-of-truth pointers into the codebase.

## Pack catalog

- **[PACKS](../PACKS.md)** — input/output contract for every shipped capability pack.
- **[Per-pack reference](./packs/)** — one deep page per pack family (CLI invocation, error codes, session chaining).
- **[Prompt templates](./prompt-templates/index.md)** — copy-and-fill prompts for every pack and pipeline (pack-first index).
- **[Cookbook — intent → prompt](../cookbook/intent-to-prompt.md)** — common natural-language intents mapped to the OpenClaw prompt + direct invocation underneath (intent-first index over the templates).
- **[MCP resources](./mcp-resources.md)** — read-only resources for catalog discovery, routing, learned defaults, memory, and context budgets.
- **[Agent memory](./agent-memory.md)** — the per-caller memory delivery layer (ADR 039) packs opt into.
- **[SKILLS](../integrations/SKILLS.md)** — agent-facing reference. Load this into your MCP client's system prompt so the LLM knows how to use all 52 packs, retry transient errors, and chain sessions.
- **[Integrations index](../integrations/README.md)** — sidecar topology and per-client guide map.

## Architecture Decisions

The 49 numbered ADRs (001-050, no 042) in **[Architecture Decisions](/adrs)** are the source of truth for every architectural choice helmdeck has made — from the sidecar pattern (ADR 001) through the latest pipeline routing, memory, and LLM-context-manager designs (ADRs 047-050).

## Project tracking

- **[TASKS](../TASKS.md)** — ~85 tasks across 8 project phases.
- **[MILESTONES](../MILESTONES.md)** — milestone checklists with current ship state.

## What goes here

Reference is *information-oriented*. It describes *what is*, not how to use it or why it was made that way. If a page reads like prose, it probably belongs in How-to or Explanation. If it reads like a table or a contract, it belongs here.
