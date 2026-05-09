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
- **[SKILLS](../integrations/SKILLS.md)** — agent-facing reference. Load this into your MCP client's system prompt so the LLM knows how to use all 38 packs, retry transient errors, and chain sessions.
- **[Integrations index](../integrations/README.md)** — sidecar topology and per-client guide map.

## Architecture Decisions

The 36 numbered ADRs in **[Architecture Decisions](/adrs)** are the source of truth for every architectural choice helmdeck has made — from the sidecar pattern (ADR 001) through the latest pack designs.

## Project tracking

- **[TASKS](../TASKS.md)** — ~85 tasks across 8 project phases.
- **[MILESTONES](../MILESTONES.md)** — milestone checklists with current ship state.

## What goes here

Reference is *information-oriented*. It describes *what is*, not how to use it or why it was made that way. If a page reads like prose, it probably belongs in How-to or Explanation. If it reads like a table or a contract, it belongs here.
