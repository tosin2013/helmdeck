---
title: hyperframes.lint
description: Pre-render validation for hyperframes scaffold projects. Wraps upstream `hyperframes lint --json` to catch render-killing issues (silent audio, conflicting timeline registration, sandboxed-render-fails-google-fonts) before burning the render budget.
keywords: [helmdeck, hyperframes, lint, validation, MCP]
---

# `hyperframes.lint`

Wrap upstream's [`hyperframes lint`](https://github.com/heygen-com/hyperframes) as a structured pre-render diagnostic. Same input shape as [`hyperframes.render`](./render.md) — pass `project_artifact_key` (a tarball from any upstream pack in the chain) or `composition_html` (a single-file index.html). Returns structured findings keyed by upstream's stable rule codes. Soft-surface by default (findings ARE the output); pass `strict:true` to gate downstream packs on a clean lint result.

```
hyperframes.scaffold     → project tarball
       ↓
hyperframes.interpolate  → topic content
       ↓
hyperframes.attach_audio → audio embedded
       ↓
hyperframes.lint         → ← THIS PACK: catch render-killers before render
       ↓
hyperframes.render       → MP4
```

## Why this pack exists

During the v0.29.2→v0.29.3 investigation, a rendered MP4 showed only 2 distinct frames over 90 seconds despite the composition having rich content. The deepest diagnostic was already shipped — upstream's own `hyperframes lint` flagged the audio element as silent-in-renders (`media_missing_id`), the manual `__timelines["x"] = tl` registration as conflicting with the runtime's auto-discovery (`gsap_studio_edit_blocked`), and the Google Fonts `<link>` as fails-in-sandboxed-renders (`google_fonts_import`). All three contributed to the symptom.

Catching these BEFORE the render step saves wall-clock (renders take minutes; lint takes <1s) and surfaces fixable issues in a structured shape the agent or pipeline can act on. Upstream CLI takes precedence over custom Go: we wrap `hyperframes lint --json` rather than reimplementing its rule engine.

## Input schema

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `project_artifact_key` | string | one-of | — | Project tarball from any upstream pack in the chain. Typical pipeline use. |
| `composition_html` | string | one-of | — | Single-file index.html. Useful for one-shot audits of an agent-authored composition. **Pass EXACTLY ONE of `project_artifact_key` OR `composition_html`.** |
| `verbose` | boolean | no | `false` | Surface info-level findings (hidden by the CLI default). Useful for thorough audits; most pipelines stay at default false. |
| `strict` | boolean | no | `false` | When `true`, any error-severity finding surfaces as `CodeArtifactFailed`. Use for CI / publish gates that should refuse to render if upstream's linter is unhappy. |

## Output schema

```json
{
  "lint": {
    "ok": false,
    "error_count": 2,
    "warning_count": 19,
    "info_count": 0,
    "files_scanned": 2,
    "findings": [
      {
        "code": "media_missing_id",
        "severity": "error",
        "message": "<audio> has data-start but no id attribute. The renderer requires id to discover media elements — this audio will be SILENT in renders.",
        "fixHint": "Add a unique id attribute: <audio id=\"my-audio\" ...>",
        "snippet": "<audio src=\"assets/aroll-audio.mp3\" data-start=\"0\"...",
        "file": "/tmp/project/index.html"
      }
    ]
  },
  "lint_artifact_key": "hyperframes.lint/abc123-lint.json"
}
```

Finding fields are passed through from upstream verbatim (camelCase preserved) so the rule codes / fix hints are stable across upstream version bumps. Top-level summary fields are snake_case to match the helmdeck pack-output convention.

## Common findings + what to do

| Code | Severity | Operator fix |
|---|---|---|
| `media_missing_id` | error | Add `id="x"` to `<audio>`/`<video>` elements. Without it the renderer cannot discover them and they're silent in the MP4. helmdeck's `hyperframes.attach_audio` adds `id="aroll-audio-<hash>"` automatically since v0.29.4. |
| `google_fonts_import` | error | Use `@font-face` with locally-bundled `.woff2` files. External font fetches fail in sandboxed renders (the bundled headless Chromium has restricted network access). |
| `missing_gsap_script` | error | Add `<script src=".../gsap.min.js">` to the composition. The lint pack can detect this before the render fails. |
| `gsap_studio_edit_blocked` | warning | Don't manually register `window.__timelines["x"] = tl` — the runtime does this automatically by reading `data-composition-id`. Manual registration conflicts with auto-discovery and triggers Studio edit-blocking. |
| `composition_self_attribute_selector` | warning | In sub-composition CSS, use `#decision-tree { ... }` instead of `[data-composition-id="decision-tree"] { ... }`. The attribute selector leaks across sibling instances when a composition is embedded twice. |
| `studio_missing_editable_id` | warning | Add `id="x"` to any timeline-animated element you want Studio (the visual editor) to allow drag/resize on. Render-only pipelines can ignore this. |

## Examples

### Pipeline-typical usage

After `hyperframes.attach_audio` returns, pass its `project_artifact_key` to lint, gate on clean result:

```yaml
- pack: hyperframes.attach_audio
  outputs:
    - name: project_key
      from: project_artifact_key
- pack: hyperframes.lint
  inputs:
    project_artifact_key: "${steps.attach_audio.project_key}"
    strict: true   # any error-severity finding aborts the pipeline
```

### One-shot composition audit

When an agent authors a composition, lint it BEFORE uploading to the project tarball:

```json
{
  "pack": "hyperframes.lint",
  "input": {
    "composition_html": "<!doctype html><html>...</html>",
    "verbose": true
  }
}
```

### Soft-surface (default — diagnostic only)

When you want findings as feedback without aborting downstream packs (most agent-debugging workflows):

```json
{
  "pack": "hyperframes.lint",
  "input": {
    "project_artifact_key": "scaffold-output-xyz"
  }
}
```

The findings come back in the output; the pack returns success even with errors.

## Strict mode error shape

When `strict:true` and any error-severity finding is present, the pack returns a typed `CodeArtifactFailed` error with the first finding's code + message inlined:

```
hyperframes.lint: 2 error-severity finding(s) in strict mode (warnings=19, info=0).
First: [media_missing_id] <audio> has data-start but no id attribute...
```

The partial output (with the full findings array) is still attached to the error so a calling agent can read both `error.message` AND `output.lint.findings` for full diagnostic context.

## Related

- [`hyperframes.render`](./render.md) — the rendering step lint gates
- [`hyperframes.attach_audio`](./attach_audio.md) — adds `id="aroll-audio-..."` since v0.29.4 to close `media_missing_id` automatically
- [`hyperframes.scaffold`](./scaffold.md) / [`interpolate`](./interpolate.md) — upstream of lint in the chain
- [`av.validate`](../av/validate.md) — the post-render twin (audio/video parity, codec, loudness)
- Field report: ["Render ≠ preview: what we learned shipping a hyperframes integration"](/blog/child-composition-slot-lifetime)
- Upstream: [`heygen-com/hyperframes#1437`](https://github.com/heygen-com/hyperframes/issues/1437) — the "render ≠ preview" bug class upstream tracks
