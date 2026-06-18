---
title: hyperframes.validate
description: Runtime-error pre-render diagnostic. Wraps `hyperframes validate --json` — loads the composition in headless Chrome and reports DevTools console errors (CORS, missing assets, JS exceptions) plus WCAG AA contrast across timeline samples.
keywords: [helmdeck, hyperframes, validate, console, CORS, WCAG, contrast, MCP]
---

# `hyperframes.validate`

Wrap upstream's [`hyperframes validate`](https://github.com/heygen-com/hyperframes) as a structured pre-render diagnostic. Loads the composition in headless Chrome, watches the DevTools console for errors during the load, and (by default) audits WCAG AA contrast across sampled timestamps. Final third of the pre-render validation suite: [`lint`](./lint.md) (static source) + [`inspect`](./inspect.md) (runtime layout) + this pack (runtime errors).

## Why this pack exists

Lint can't catch a composition script that throws on load — it reads source, not runtime state. Inspect can't catch a CORS-blocked video URL — it samples the DOM, not the network log. Validate fills that gap: it boots the composition in real Chrome and reports anything the browser itself flagged as an error during the load. The common findings are exactly the ones that produce silent blank media + blank canvases in the final MP4:

- **CORS-blocked external assets** — a video or font URL that responds 200 to a curl but is blocked by browser CORS policy in the headless sandbox, producing a blank media element in the render
- **`net::ERR_FAILED`** — any external resource the sandbox can't reach (missing local file, blocked Google Fonts CDN, dead S3 URL)
- **JS runtime errors** — the composition's `<script>` threw and `window.__timelines["x"]` was never registered, leading to a blank canvas during the render seek
- **WCAG AA contrast failures** — text whose foreground/background contrast ratio is below the AA threshold (4.5 for normal text, 3.0 for large)

## Input schema

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `project_artifact_key` | string | one-of | — | Project tarball. |
| `composition_html` | string | one-of | — | Single-file index.html. **Pass EXACTLY ONE.** |
| `contrast` | boolean | no | `true` | WCAG contrast audit. Set `false` to skip when the composition is intentionally low-contrast (cinema titles, motion-graphics intro frames). |
| `timeout` | integer | no | `5000` (CLI) | Ms to wait for the runtime to initialize. |
| `strict` | boolean | no | `false` | Any console-level **error** → `CodeArtifactFailed`. Contrast failures do NOT trigger strict — they're a separate audit dimension. |

## Output schema

```json
{
  "validate": {
    "ok": false,
    "error_count": 2,
    "warning_count": 1,
    "contrast_sample_count": 47,
    "contrast_failure_count": 3,
    "errors": [
      {
        "level": "error",
        "text": "Access to video at 'https://...' has been blocked by CORS policy",
        "url": "http://127.0.0.1:34169/"
      }
    ],
    "warnings": [...],
    "contrast": [
      {
        "time": 1.5,
        "selector": "div.figma-label",
        "text": "animations",
        "ratio": 19.75,
        "wcagAA": true,
        "large": true,
        "fg": "rgb(255,255,255)",
        "bg": "rgb(10,10,15)"
      }
    ]
  },
  "validate_artifact_key": "hyperframes.validate/abc123-validate.json"
}
```

`contrast_failure_count` is a helmdeck-derived field — count of `contrast[]` rows where `wcagAA: false`. Operators can read it for a one-glance summary; the full `contrast[]` array stays in the output for detailed audits.

## Strict-mode shape

Strict mode is **about console errors only.** A CORS failure or a JS throw aborts; contrast failures don't. The rationale: console errors usually mean the rendered video is broken (blank media, blank canvas). Contrast failures mean the video has accessibility regressions, which is a publish concern but not a render-correctness concern.

```
hyperframes.validate: 2 console error(s) in strict mode.
First: [error] Access to video at 'https://...' has been blocked by CORS policy
```

## Example — pre-publish gate

For pipelines that produce published video assets, validate is the last gate before render. Combine with lint + inspect:

```yaml
- pack: hyperframes.lint
  inputs:
    project_artifact_key: "${steps.attach_audio.project_key}"
    strict: true
- pack: hyperframes.inspect
  inputs:
    project_artifact_key: "${steps.attach_audio.project_key}"
    at_transitions: true
    strict: true
- pack: hyperframes.validate
  inputs:
    project_artifact_key: "${steps.attach_audio.project_key}"
    strict: true
- pack: hyperframes.render
  inputs:
    project_artifact_key: "${steps.attach_audio.project_key}"
```

## Related

- [`hyperframes.lint`](./lint.md) — static-source validation
- [`hyperframes.inspect`](./inspect.md) — runtime-layout validation
- [`hyperframes.render`](./render.md) — the rendering step validate gates
- [`av.validate`](../av/validate.md) — post-render audio/video parity audit
- Field report: [Render ≠ preview](/blog/child-composition-slot-lifetime)
