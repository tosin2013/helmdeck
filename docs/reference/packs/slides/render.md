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

## See also

- Catalog row: [`PACKS.md`](/PACKS) — `slides.render`.
- Source: [`internal/packs/builtin/slides_render.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/slides_render.go).
- Companion pack: [`slides.narrate`](./narrate.md) (MP4 + TTS narration).
- Marp documentation: <https://marp.app/>.
