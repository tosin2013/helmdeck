---
title: hyperframes.inspect
description: Runtime-layout pre-render diagnostic for hyperframes scaffold projects. Wraps `hyperframes inspect --json` to catch text/container overflow and transition-seam overlaps that lint can't see — by loading the project in headless Chrome and sampling the DOM at N timestamps.
keywords: [helmdeck, hyperframes, inspect, validation, layout, overflow, MCP]
---

# `hyperframes.inspect`

Wrap upstream's [`hyperframes inspect`](https://github.com/heygen-com/hyperframes) as a structured pre-render diagnostic. Where [`hyperframes.lint`](./lint.md) catches STATIC issues from source files, this pack catches RUNTIME layout issues by loading the project in headless Chrome and sampling the DOM at specified (or auto-derived) timestamps. Same input shape as [`hyperframes.render`](./render.md) — pass `project_artifact_key` OR `composition_html`. Soft-surface by default; pass `strict:true` to gate downstream packs.

## Why this pack exists

Lint catches "the audio is missing an id, that's silent in renders." Inspect catches "the caption fits at t=0 and t=5 but overflows its container at t=12.5 when an animation expands its parent." Both are pre-render — both run before burning the render budget. Inspect is the missing middle: source-level static checks (lint) + runtime layout sampling (inspect) + console-error audit (validate) form the three-pack pre-render validation suite.

## Input schema

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `project_artifact_key` | string | one-of | — | Project tarball. |
| `composition_html` | string | one-of | — | Single-file index.html. **Pass EXACTLY ONE.** |
| `samples` | integer | no | `9` (CLI) | Number of midpoint samples across the duration. |
| `at` | string | no | — | Comma-separated explicit timestamps in seconds (e.g. `"1.5,4,7.25"`). |
| `tolerance` | integer | no | `2` (CLI) | Allowed pixel overflow before reporting. |
| `timeout` | integer | no | `5000` (CLI) | Ms to wait for runtime initialization. |
| `max_issues` | integer | no | `80` (CLI) | Cap on returned issues after static-collapse. |
| `at_transitions` | boolean | no | `false` | Also sample every tween start/end boundary (catches transition-seam overlaps). |
| `strict` | boolean | no | `false` | Any error-severity issue → `CodeArtifactFailed`. |

## Output schema

```json
{
  "inspect": {
    "ok": false,
    "duration": 15,
    "error_count": 1,
    "warning_count": 1,
    "info_count": 0,
    "issue_count": 2,
    "total_issue_count": 2,
    "truncated": false,
    "sample_count": 5,
    "issues": [
      {
        "code": "text_box_overflow",
        "severity": "error",
        "time": 12.5,
        "selector": "div.wes-text-bottom",
        "containerSelector": "div.wes-container",
        "text": "and render.",
        "message": "Text extends outside its nearest visual/container box.",
        "fixHint": "Text is 259px x 55px..."
      }
    ]
  },
  "inspect_artifact_key": "hyperframes.inspect/abc123-inspect.json"
}
```

Issue fields preserve upstream camelCase verbatim (`fixHint`, `containerSelector`) so the codes stay stable across CLI version bumps.

## Common findings

| Code | Severity | What it means |
|---|---|---|
| `text_box_overflow` | error | Text extends outside its visual/container box at this timestamp. Fix: shrink text or expand container. |
| `transition_overlap` | warning | Sibling clips overlap at a transition seam. Fix: adjust `data-start` offsets or shorten exit tweens. |
| `static_collapse` | info | An element collapses (width or height → 0) across N samples. Fix: explicit dimensions or `min-` constraints. |

## Example — at-transitions sampling

For agent-authored compositions, the highest-leverage flag is `at_transitions:true`. It samples each tween's start AND end in addition to the default midpoints — catching the "fits at t=5 and t=10 but overlaps for 200ms at the t=7.5 transition seam" failures that midpoint sampling misses.

```json
{
  "pack": "hyperframes.inspect",
  "input": {
    "project_artifact_key": "${steps.attach_audio.project_key}",
    "at_transitions": true,
    "strict": true
  }
}
```

## Related

- [`hyperframes.lint`](./lint.md) — static-source validation (runs first)
- [`hyperframes.validate`](./validate.md) — console-error + WCAG audit (runs alongside or after)
- [`hyperframes.render`](./render.md) — the rendering step inspect gates
- Field report: [Render ≠ preview](/blog/child-composition-slot-lifetime)
