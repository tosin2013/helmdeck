---
title: slides.render
description: Convert Marp markdown to PDF, PPTX, or HTML via the Marp CLI bundled in the sidecar. Returns an artifact key for the rendered file.
keywords: [helmdeck, slides, render, marp, pdf, pptx, MCP]
---

# `slides.render`

The "Marp markdown → PDF/PPTX/HTML" pack. Caller hands in a Marp deck (frontmatter + `---`-delimited slides) and a target `format`; the pack invokes the Marp CLI inside the sidecar (`marp --stdin --allow-local-files`), streams the binary output to the artifact store, and returns the artifact key + size. The deck never touches disk inside the sidecar — input via stdin, output streamed.

For narrated video output (MP4 with TTS over each slide), see [`slides.narrate`](./narrate.md). For just the static deck, this is the right pack.

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `markdown` | `string` | yes | — | Marp deck. Must start with `---\nmarp: true\n---` frontmatter for the directives to apply. Slides separated by `---`. May contain ```` ```mermaid ```` fenced blocks — see [Mermaid diagrams](#mermaid-diagrams) below. |
| `format` | `string` | no | `"pdf"` | Closed-set: `pdf`, `pptx`, `html`. Picks the Marp output codec. |
| `mermaid` | `boolean` | no | `true` | Pre-render ```` ```mermaid ```` fenced blocks to inline-SVG `<img>` data-URIs via `mmdc` before Marp sees the deck. Set `false` to skip the pre-pass (saves a few hundred ms of mmdc startup if the deck has no diagrams or you've embedded SVGs by hand). |
| `hero_image_prompt` | `string` | no | — | When non-empty (v0.12.0 #146), the pack calls `image.generate` with this prompt, fetches the resulting PNG, and base64-inlines it as `<img src="data:image/png;base64,…">` immediately after the deck's frontmatter so PDF/PPTX/HTML outputs all carry the hero. Fails loud on missing `fal-key` credential — no silent omission. |
| `hero_image_model` | `string` | no | `"fal-ai/flux/schnell"` | fal.ai model used when `hero_image_prompt` is set. Browse choices via the `helmdeck://image-models` MCP resource. |

## Outputs

| Field | Type | Notes |
|---|---|---|
| `format` | `string` | Echo of the chosen format. |
| `artifact_key` | `string` | `slides.render/<rand>-deck.{pdf|pptx|html}` — pass to `/api/v1/artifacts/<key>` to retrieve. |
| `hero_image_model_used` | `string` | Only when `hero_image_prompt` was set. Echoes the model that actually generated the hero. |
| `size` | `number` | Bytes. |

## Vault credentials needed

**None.** Marp runs entirely in-sidecar.

## Use it from your agent (OpenClaw chat-UI worked example)

**Prompt** (sent in OpenClaw chat UI / `openclaw-cli agent`):

> Use helmdeck__slides-render to convert this Marp markdown to PDF: "---\nmarp: true\n---\n# Hello helmdeck\n\n---\n\n# Thanks for listening". Tell me the artifact_key and the size in bytes.

**Tool call** (1 call, no failures):

```json
{
  "name": "helmdeck__slides-render",
  "arguments": {
    "format": "pdf",
    "markdown": "---\nmarp: true\n---\n# Hello helmdeck\n\n---\n\n# Thanks for listening"
  }
}
```

**Pack response** (truncated):

```json
{"pack":"slides.render","version":"v1","output":{"artifact_key":"slides.render/7d549cd238a7bc34-deck.pdf","format":"pdf","size":7902},"artifacts":[{"key":"slides.render/7d549cd238a7bc34-deck.pdf","url":"http://garage:3900/helmdeck-artifacts/slides.render/7d549cd238a7bc34-deck.pdf?X-Amz-Algorithm=AWS4-HMAC-SHA256\u0026X-Amz-Credential=GKaef177ebe9fd814dc103b84b%2F20260508%2Fgarage%2Fs3%2Faws4_request\u0026X-Amz-Date=20260508T152130Z\u0026X-Amz-Expires=900\u0026X-Amz-SignedHeaders=host\u0026X-Amz-Signature=b2f7c01cfc3a39c4365d0bd5f79dfff7d49e972497f4eff7b535b8eaccf3c544","size":7902,"content_type":"application/pdf","created_at":"2026-05-08T15:21:30.754346118Z","pack":"slides.render"}],"duration_ms":18808030860,"session_id":"586c8ee9-3529-4128-9cbf-541e0cc20de0"}
```

**Agent's final reply**:

> The PDF was created successfully.
> 
> - **artifact_key:** `slides.render/7d549cd238a7bc34-deck.pdf`  
> - **size:** `7902` bytes

*Verified via OpenClaw 2026.5.6 + helmdeck v0.9.0-dev + `openrouter/openai/gpt-oss-120b` on 2026-05-07 (cost: $0.0013).*

## Developer reference (`curl`)

```bash
curl -fsS -X POST http://localhost:3000/api/v1/packs/slides.render \
  -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
  -d '{
    "markdown": "---\nmarp: true\n---\n# Hello helmdeck\n\n---\n\n# Thanks for listening",
    "format":   "pdf"
  }'
```

Response shape:

```json
{
  "pack": "slides.render",
  "version": "v1",
  "output": {
    "format":       "pdf",
    "artifact_key": "slides.render/abc123-deck.pdf",
    "size":         98304
  }
}
```

Retrieve the artifact:

```bash
curl -fsS -H "Authorization: Bearer $JWT" \
  "http://localhost:3000/api/v1/artifacts/slides.render/abc123-deck.pdf" \
  -o deck.pdf
```

## Error codes

| Code | Triggers | Captured response |
|---|---|---|
| `invalid_input` | `markdown` empty | `markdown is required` |
| `invalid_input` | `format` outside the closed set | `unsupported format "docx"; use pdf, pptx, or html` |
| `handler_failed` | Marp CLI exit non-zero (malformed deck, missing fonts) | `marp exit N: <stderr>` (truncated to 1024 chars) |
| `artifact_failed` | Object store write failed | `artifact upload failed: …` |

## Session chaining

**Required (creates if absent).** Stateless logically — the deck is in the input, the output is an artifact — but the Marp render runs inside a session sidecar. Typically not chained; one-shot.

## Async behavior

Synchronous. PDF rendering of a 10-slide deck is ~3–6s. PPTX ~5–10s. HTML ~1–2s.

## Mermaid diagrams

Markdown bodies may include ```` ```mermaid ```` fenced blocks anywhere a fenced code block is legal. The pack pre-processes the deck before piping it to Marp: each fence is sent through `mmdc` (the [Mermaid CLI](https://github.com/mermaid-js/mermaid-cli)) running in the sidecar, the SVG is base64-encoded and inlined as an `<img src="data:image/svg+xml;base64,…">` tag, and Marp then renders the deck with the diagrams in place. The same path produces correct output for **PDF, PPTX, and HTML** — no client-side script is involved.

~~~markdown
---
marp: true
theme: gaia
---

# Architecture overview

```mermaid
flowchart LR
  agent[Agent] --> mcp[MCP bridge]
  mcp --> cp[Control plane]
  cp --> sidecar[Browser sidecar]
```

---

# Request flow

```mermaid
sequenceDiagram
  participant Agent
  participant CP as Control plane
  participant SC as Sidecar
  Agent->>CP: POST /api/v1/packs/slides.render
  CP->>SC: exec marp
  SC-->>CP: PDF bytes
  CP-->>Agent: artifact_key
```
~~~

**Supported diagram kinds.** Anything Mermaid understands: `flowchart`/`graph`, `sequenceDiagram`, `stateDiagram-v2`, `classDiagram`, `erDiagram`, `gantt`, `pie`, `mindmap`, `timeline`, etc. The pack does no syntax validation — `mmdc` is the source of truth, and a malformed diagram surfaces as `handler_failed` with the diagram source included in the message so authors can debug without re-running locally.

**Cost.** Each diagram is a separate `mmdc` invocation, each ~300–700ms (Chromium warm-up dominates). Three diagrams ≈ +2s on top of the Marp render. For decks with no diagrams, the pre-pass is a no-op (no `mmdc` exec).

**Opt out.** Pass `"mermaid": false` to skip the pre-pass entirely. Use this when you've embedded SVG manually or know your deck contains no Mermaid.

**Failure behaviour.** A bad Mermaid syntax error surfaces as:

```
handler_failed: mmdc exit 1 on diagram 0: <truncated stderr>
--- diagram source ---
graphh TD; A-->B;
```

The diagram source is included (truncated to 256 chars) so the author can spot the typo without re-running `mmdc` themselves.

## Custom design (themes + CSS)

The pack passes the deck verbatim to `marp --stdin`, so **all Marp frontmatter directives apply** — there's no schema knob for themes because the theme lives in the markdown itself.

### Built-in themes

Marp ships three themes out of the box. Pick one in the frontmatter:

```markdown
---
marp: true
theme: gaia      # or: default | uncover
---

# Slide 1
```

| Theme | Looks like |
|---|---|
| `default` | Clean white background, dark text. Marp's reference look. |
| `gaia` | Warm cream background, brown accents. Good for narrative decks. |
| `uncover` | High-contrast minimalist (black/white). Good for keynote-style decks. |

### Helmdeck curated themes (#202)

In addition to Marp's built-ins, the pack ships two hand-tuned themes the agent can pick by name. **These are the safest option for "make my deck look like X" prompts** — they declare colors for every element type explicitly, so the contrast problems custom CSS frequently introduces can't occur.

| Theme | Palette | Use case |
|---|---|---|
| `helmdeck-dark` | Slate-900 background, slate-100 body text, sky-400 accent, dark tables/code/blockquote | Modern technical deck, conference talk, dark-mode preference |
| `helmdeck-corporate` | White background, gray-800 body text, blue-800 accent, conservative tables/code | Internal exec deck, customer report, business pitch |

Pick a curated theme the same way you'd pick a Marp built-in:

```markdown
---
marp: true
theme: helmdeck-dark
---

# A deck with a known-good palette

Every nested element (h1-h6, p, a, table, th, td, code, pre, blockquote)
inherits a WCAG-AA-compliant color out of the box. You don't need a `style:`
block at all.
```

Source: [`internal/packs/builtin/themes/helmdeck-dark.css`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/themes/helmdeck-dark.css), [`helmdeck-corporate.css`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/themes/helmdeck-corporate.css). The CSS is embedded into the control-plane binary at build time and uploaded to the sidecar on each call that references one of these themes.

### Color contrast best practices (#202)

When the agent generates a Marp deck with custom CSS, the most common quality bug is **changing `section { background }` without restyling the nested element types** — `table`, `td`, `th`, `code`, `blockquote`, `pre`. The default Marp theme styles those for a light background; against a new dark `section` background, the previous theme's table colors glare through and make the slide unreadable.

Rules of thumb, in priority order:

1. **Prefer a curated theme** (`helmdeck-dark` / `helmdeck-corporate`) over hand-authored CSS. The curated themes have already done the work.
2. **WCAG AA minimum**: 4.5:1 contrast for body text, 3:1 for large text and UI components. Online checker: <https://webaim.org/resources/contrastchecker/>.
3. **When overriding `section { background }`, override every nested element type's foreground AND background**. The reproducer for #202 was:
   ```css
   /* BAD: leaves default light-themed tables glaring through */
   section { background: #1e3a8a; color: #fff; }

   /* GOOD: every nested element type gets explicit colors */
   section { background: #1e3a8a; color: #fff; }
   table, th, td { background-color: #1e293b; color: #f1f5f9; }
   code { background-color: #0b1220; color: #f0a500; }
   blockquote { background-color: #1e293b; color: #cbd5e1; border-left: 4px solid #38bdf8; }
   pre { background-color: #0b1220; color: #f1f5f9; }
   ```
4. **Bad combinations to avoid**: light-grey text on white, white text on light-yellow, dark-blue text on dark-purple, any pair where you'd squint to read it on a phone. The pack's lint (see below) catches a subset automatically.

### Custom CSS (per-deck overrides)

Two ways to inject custom CSS — both work because Marp processes them inline:

**Frontmatter `style` block** (preferred — keeps everything in one place):

```markdown
---
marp: true
theme: gaia
style: |
  section {
    background: linear-gradient(135deg, #1a1a2e, #16213e);
    color: #f5f5f5;
  }
  h1 { color: #00d4ff; }
  code { background: #222; color: #f0a500; padding: 2px 6px; border-radius: 3px; }
---

# Custom-styled deck
```

**Embedded `<style>` tag** (works inside the markdown body, useful for per-slide variations):

```markdown
---
marp: true
---

<style scoped>
  section { background: #fff; color: #111; }
</style>

# This slide uses scoped CSS
```

`<style scoped>` applies only to the slide it appears on; a plain `<style>` block applies deck-wide.

### When the agent needs custom design

For an agent driving `slides.render` (or `slides.narrate`) on the user's behalf, the pattern is: **write the design into the markdown frontmatter the agent generates**. The pack itself doesn't accept theme arguments — design lives in the deck. Agents handling "make this deck look like X" prompts should emit the corresponding frontmatter directives, not pass them as separate pack inputs.

Reference: <https://marpit.marp.app/theme-css> for the full Marp directive list.

## Response warnings (contrast lint, #202)

The pack runs a static CSS lint against the markdown's frontmatter `style:` block and embedded `<style>` tags **before** marp renders. When it spots a palette anti-pattern, it adds an entry to the response's `warnings` array. **Lint warnings are informational, not errors** — the deck still renders and the artifact still uploads. Agents should check `warnings` and decide whether to re-render with corrections.

Two warning rules ship today:

| `rule` | Fires when | Recommendation in the response |
|---|---|---|
| `section-background-without-nested-overrides` | The CSS overrides `section { background }` but does not set explicit colors for one or more of `table`, `td`, `th`, `code`, `blockquote`, `pre`. | "Add CSS rules for each, or switch to `helmdeck-dark` / `helmdeck-corporate`." |
| `wcag-aa-text-contrast` | A single CSS rule sets both `color` and `background-color` (or shorthand `background`) using hex values whose computed WCAG contrast is below 4.5:1. | "Darken the background or lighten the foreground until the ratio is ≥ 4.5:1." |

Example response when the deck has a contrast problem:

```json
{
  "format": "pdf",
  "artifact_key": "slides.render/abc-deck.pdf",
  "size": 12345,
  "warnings": [
    {
      "rule": "section-background-without-nested-overrides",
      "selector": "section",
      "recommendation": "section background was customized (#1e3a8a) but the following nested element types were not restyled: table, td, th, code, blockquote, pre. ..."
    }
  ]
}
```

Decks that use a curated theme (`theme: helmdeck-dark` or `theme: helmdeck-corporate`) almost always produce zero warnings because those themes declare every element's colors explicitly.

The response also carries a `curated_theme_used` field when the frontmatter picked one of helmdeck's curated themes — so the agent can tell whether the theme actually applied or whether Marp fell back to a default. Example:

```json
{
  "format": "pdf",
  "artifact_key": "slides.render/abc-deck.pdf",
  "size": 12345,
  "curated_theme_used": "helmdeck-dark"
}
```

Limitations of the static lint (deliberate cutoffs, not bugs):

- **Hex colors only.** `rgb(...)`, named colors, `linear-gradient(...)` etc. skip the WCAG ratio check — the lint refuses to make up a contrast value it can't compute from the source. False negatives are preferable to false alarms.
- **No full CSS cascade.** The lint sees rules in isolation; it can miss subtle inheritance bugs that only surface after full cascade resolution. Adding browser-based scanning is tracked as a follow-up.
- **Static analysis only.** Anti-patterns embedded in dynamically-generated `<style>` (none today, but possible if Marp ever allows it) won't be caught.

## See also

- Catalog row: [`PACKS.md`](/PACKS) — `slides.render`.
- Source: [`internal/packs/builtin/slides_render.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/slides_render.go).
- Companion pack: [`slides.narrate`](./narrate.md) (MP4 + TTS narration).
- Marp documentation: <https://marp.app/>.
