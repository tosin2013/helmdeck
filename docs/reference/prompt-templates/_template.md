---
title: Prompt-template stub (for contributors)
description: Copy this entry shape when adding a prompt template for a new pack or pipeline.
---

# Prompt-template stub

When you add a pack or pipeline, add a template entry to the matching page
(`packs.md` for a pack, `pipelines.md` for a pipeline) using the shape below.
Variables are `{{UPPERCASE}}` and **must map to real inputs** — a pack's
`InputSchema` (`internal/packs/builtin/<pack>.go`) or a pipeline's
`${{ inputs.* }}` references (`internal/pipelines/seed.go`). Don't invent fields.

Copy this:

````markdown
#### `pack.name` — one-line purpose

**Template**
```
<a natural-language prompt that asks an agent to use helmdeck__<pack-name>,
with a {{VARIABLE}} in place of each value the user supplies>
```

**Variables**
- `{{VARIABLE}}` — what it is; maps to input `field` (required).
- `{{OPTIONAL_VAR}}` — what it is; maps to input `field` (optional, default `…`).

**Notes** — credentials/overlays needed, async, or gotchas (omit if none).
````

Keep it to one entry per pack/pipeline. Put pack entries under the right
family heading in `packs.md`; pipeline entries go in `pipelines.md` keyed by
`builtin.<id>`.
