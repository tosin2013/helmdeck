---
title: Prompt templates
description: Reusable, fill-in-the-blank prompt templates for every helmdeck pack and pipeline. Copy a template, override the {{VARIABLES}}, and paste it into your MCP client.
keywords: [helmdeck, prompt templates, packs, pipelines, MCP, copy-paste, variables]
---

# Prompt templates

Copy-and-fill prompt **templates** for every helmdeck pack and pipeline. Each
template is a natural-language prompt with `{{VARIABLE}}` slots; replace the
variables with your values and paste it into your MCP client (OpenClaw, Claude
Code, Claude Desktop, Gemini CLI, …). The agent picks the right helmdeck tool.

- **[Pack templates](./packs.md)** — one template per capability pack, grouped by family.
- **[Pipeline templates](./pipelines.md)** — one template per built-in pipeline (a saved, multi-step chain).

## The `{{VARIABLE}}` convention

- Variables are **double-brace, UPPERCASE**: `{{TOPIC}}`, `{{REPO_URL}}`, `{{TITLE}}`.
- Replace **every** `{{VARIABLE}}` before sending — a leftover placeholder will confuse the agent.
- Each template lists its variables and which pack/pipeline **input** they map to, with required vs optional and defaults.
- This is distinct from a pipeline definition's `${{ inputs.* }}` syntax (that's the engine's templating, authored by pack/pipeline builders). `{{VARIABLE}}` here is just a human fill-in marker for a prompt you paste.

## How to use

1. Find the pack or pipeline you want on the [packs](./packs.md) or [pipelines](./pipelines.md) page.
2. Copy its **Template** block.
3. Replace each `{{VARIABLE}}` with your value (the **Variables** list says what each one is).
4. Paste into your MCP client's chat. Watch the result in the [Artifact Explorer](/reference/architecture) / Audit Log; long-running packs return a task you poll.

## Related

- **[Pack demo playbook](/integrations/pack-demo-playbook)** — the same idea but with *literal* values, for validating a fresh install or demoing.
- **[Per-pack reference](/reference/packs/)** — full input/output schemas, error codes, and one worked example per pack.
- **[SKILL.md](/integrations/SKILLS)** — when an agent should call a pack directly vs. run/create a pipeline.

## Keeping this current

When you add a pack or pipeline, add its template here — copy the entry shape from `_template.md` in this folder. See `CONTRIBUTING.md` and the `docs/RELEASES.md` release checklist.
