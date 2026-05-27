---
title: blog.publish
description: Publish a post to a Ghost blog (live Admin API) or write rendered markdown/HTML to the helmdeck artifact store. Two body modes (agent supplies body OR prompt+model the pack expands), two destinations, two formats.
keywords: [helmdeck, blog, publish, ghost, markdown, html, MCP]
---

# `blog.publish`

The "publish a blog post" pack. Two destinations, two body modes, two formats — picked at call time. Closes [#68](https://github.com/tosin2013/helmdeck/issues/68).

| Axis | Options |
|---|---|
| **Destination** | `ghost` (live publish via Ghost Admin API) · `artifact` (render to a helmdeck artifact, no external network) |
| **Body mode** | `body` (the agent already wrote the post) · `prompt + model` (the pack expands the prompt into a body via the gateway LLM) |
| **Format** | `markdown` (rendered to HTML via goldmark when Ghost destination needs it) · `html` (pre-rendered, passes through) |

The two body modes let an agent treat publishing as either a *primitive* (it composed the body upstream and just hands it off) or as a *macro* (it knows what it wants but lets the pack do the writing). The two destinations let the same agent publish a draft to a real Ghost blog OR generate a stand-alone artifact a downstream system can pick up.

## Setup prerequisite

For the `ghost` destination, add the Ghost Admin API key to the *Vault* panel:

| Field | Value |
|---|---|
| **Name** | `ghost-admin-key` (exact string — pack default; override with `credential` input) |
| **Type** | `api_key` |
| **Host pattern** | Your Ghost installation's hostname (e.g. `blog.example.com`) |
| **Value** | The full Admin API key in `<id>:<secret>` form (Ghost ships them this way; secret is hex-encoded) |

Get the key from your Ghost admin: **Settings → Advanced → Integrations → Add custom integration → Admin API Key**. The key looks like `650f...:a1b2c3...` — paste the whole thing, including the colon.

For the `artifact` destination, **no vault credential is needed** — the pack writes locally to the helmdeck artifact store.

## Inputs

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `destination` | `string` | no | `"artifact"` | `"ghost"` (publish via Ghost Admin API + save artifact safety net) or `"artifact"` (save only). Omitting the field defaults to `"artifact"` — the safety-net path that never loses the body. |
| `also_save_artifact` | `boolean` | no | `true` | When `destination="ghost"`, controls whether the post body is also written as an artifact alongside the Ghost publish. Default `true` means a Ghost failure no longer loses the agent's work — see [Partial success](#partial-success). Set `false` to opt out (today's pre-#203 hard-fail-on-Ghost-error behaviour). |
| `format` | `string` | yes | — | `"markdown"` or `"html"`. |
| `title` | `string` | yes | — | Post title. Slugified for the artifact filename. |
| `body` | `string` | one-of | — | The post body. **Either** this **or** `prompt`+`model`. |
| `prompt` | `string` | one-of | — | Generation prompt for prompt mode. **Either** this **or** `body`. |
| `model` | `string` | with `prompt` | — | Provider/model for prompt mode (e.g. `openrouter/openai/gpt-4o-mini`). |
| `max_tokens` | `number` | no | `1024` | Cap on the prompt-mode body length. Ignored in body mode. |
| `tags` | `array` | no | `[]` | Tag names. For Ghost, converted to `{name: ...}` objects. |
| `status` | `string` | no | `"draft"` | `"draft"` (default), `"published"`, or `"scheduled"`. |
| `published_at` | `string` | with `status="scheduled"` | — | RFC3339 timestamp in the future. |
| `host` | `string` | with `destination="ghost"` | — | Ghost installation hostname. Accepts `host`, `https://host`, or `http://host:port` (the last for self-hosted Ghost on a non-HTTPS port). |
| `credential` | `string` | no | `"ghost-admin-key"` | Vault credential name. Override only if you store the key under a non-default name. |
| `feature_image_artifact_key` | `string` | no | — | Operator-supplied feature image (v0.12.0 #146). Pass an artifact key from a prior pack call — typically `image.generate`, or a chained pack's `cover_image_artifact_key`. For Ghost, the pack uploads the bytes via `/ghost/api/admin/images/upload/` then stamps the returned URL into the post's `feature_image` field. For artifact-mode, the cover lands as a sidecar `<slug>-cover.png` artifact alongside the post body. **Mutually exclusive with `hero_image:true`.** |
| `hero_image` | `boolean` | no | `false` | Auto-generate the feature image via `image.generate` (v0.12.0 #146). Uses `hero_image_prompt` if set, falling back to the post title. **Mutually exclusive with `feature_image_artifact_key`.** |
| `hero_image_prompt` | `string` | no | — | Prompt for the auto-generated hero image when `hero_image:true`. Defaults to the post title if omitted. |
| `hero_image_model` | `string` | no | `"fal-ai/flux/schnell"` | fal.ai model used when `hero_image:true`. Browse choices via the `helmdeck://image-models` MCP resource. |
| `mermaid` | `boolean` | no | `true` | Pre-render ```` ```mermaid ```` fenced blocks to inline SVG (server-side via mmdc) so diagrams show reliably everywhere. Set `false` to leave fences for client-side rendering. See [Mermaid diagrams](#mermaid-diagrams-in-technical-posts). |

**Validation:**
- Exactly one of `body` or (`prompt`+`model`) — providing both or neither errors.
- Providing both `feature_image_artifact_key` AND `hero_image:true` errors — pick one source for the cover.
- `status="scheduled"` requires `published_at` in the future.
- `destination="ghost"` requires `host` and a vault credential.
- `destination` must be `"ghost"`, `"artifact"`, or omitted. Omitted defaults to `"artifact"`.

## Outputs

Common fields (always present):

| Field | Type | Notes |
|---|---|---|
| `destination` | `string` | Echo of the input destination (or `"artifact"` when omitted). |
| `format` | `string` | Echo. |
| `body_source` | `string` | `"input"` (body mode) or `"model"` (prompt mode). |
| `status` | `string` | Outcome discriminator — `"artifact_saved"` (artifact-only path), `"draft"` / `"published"` / `"scheduled"` (Ghost confirmed), or `"artifact_saved_ghost_failed"` (Ghost attempted but failed; the body is in the artifact store — see [Partial success](#partial-success)). |
| `model_used` | `string` | Only in prompt mode — the model that generated the body. |

Artifact safety-net fields (present unless `also_save_artifact:false` was set):

| Field | Type | Notes |
|---|---|---|
| `artifact_key` | `string` | `blog.publish/<slug>.{md\|html}`. Resolve via `/api/v1/artifacts/<key>`. |
| `artifact_url` | `string` | Presigned URL for direct fetch (S3 backend) or a `memory://` URL (in-memory test backend). |
| `size` | `number` | Bytes. |
| `feature_image_artifact_key` | `string` | Sidecar cover artifact (`blog.publish/<slug>-cover.png`) when a feature image was supplied or auto-generated. **In `destination=ghost` mode, Ghost's reference to the original input artifact key takes precedence** — see "Feature-image fields" below. |

Ghost-specific (present when `destination="ghost"` AND Ghost publish succeeded):

| Field | Type | Notes |
|---|---|---|
| `post_id` | `string` | Ghost post id. |
| `url` | `string` | Public URL. |
| `html_url` | `string` | Same as `url`, for parity with `github.*` packs. |
| `published_at` | `string` | Ghost-assigned RFC3339. |
| `feature_image_url` | `string` | Ghost-hosted CDN URL of the uploaded cover (present when `feature_image_artifact_key` or `hero_image:true` was set). |

Partial-success-specific (present when Ghost failed but the artifact was saved):

| Field | Type | Notes |
|---|---|---|
| `ghost_error` | `string` | The error message from the failed Ghost step (vault credential missing, JWT key format, API non-2xx, egress denied, etc.). The agent retries by re-invoking `blog.publish` with the body fetched from `artifact_url`. |

Feature-image fields (both destinations):

| Field | Type | Notes |
|---|---|---|
| `hero_image_model_used` | `string` | Only when `hero_image:true`. Echoes the model that actually generated the cover. |

### How to read the response

The `status` field is the canonical discriminator. **Always check `status` rather than assuming HTTP 200 means full success.** The pattern:

| `status` value | Meaning | Agent next step |
|---|---|---|
| `artifact_saved` | No Ghost publish was requested (or `destination=artifact`/omitted). The body is in the artifact store. | Fetch via `artifact_url` if needed; otherwise nothing more to do. |
| `draft` / `published` / `scheduled` | Ghost confirmed the publish at the named state. The artifact is also saved (unless `also_save_artifact:false`). | Done — the post is live. |
| `artifact_saved_ghost_failed` | Ghost was requested but failed. The body is in the artifact store. | Inspect `ghost_error`, fix the underlying issue (credentials, host, network), and re-invoke `blog.publish` with the body fetched from `artifact_url`. |

## Vault credentials needed

`ghost-admin-key` for ghost destination only. **Optional for artifact destination.**

## Use it from your agent (OpenClaw chat-UI worked example)

<!-- TODO(maintainer): paste an OpenClaw chat-UI transcript here.
     Prompt to use: "Use helmdeck__blog-publish in artifact mode with destination=artifact, format=markdown, title=\"Demo PR-D2 post\", body=\"# Hello\\n\\nThis is a test.\". Tell me the artifact_key and size." -->

> *OpenClaw chat capture pending.*

## Developer reference (`curl`)

### Artifact mode (no Ghost required)

```bash
ADMIN_PW=$(grep HELMDECK_ADMIN_PASSWORD /root/helmdeck/deploy/compose/.env.local | cut -d= -f2)
JWT=$(curl -fsS -X POST http://localhost:3000/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"${ADMIN_PW}\"}" \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])')

curl -fsS -X POST http://localhost:3000/api/v1/packs/blog.publish \
  -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
  -d '{
    "destination": "artifact",
    "format":      "markdown",
    "title":       "Demo PR-D2 post",
    "body":        "# Hello\n\nThis is a test."
  }'
```

Response:

```json
{
  "pack": "blog.publish",
  "version": "v1",
  "output": {
    "destination":  "artifact",
    "format":       "markdown",
    "body_source":  "input",
    "status":       "artifact_saved",
    "artifact_key": "blog.publish/demo-pr-d2-post.md",
    "artifact_url": "https://s3.example/blog.publish/...?X-Amz-Signature=...",
    "size":         101
  }
}
```

> **Tip:** Omitting `destination` entirely is equivalent to `destination=artifact`. The artifact-only path is the safety-net default — no agent ever loses a body to a misconfigured Ghost setup.

### Ghost mode (live API)

```bash
curl -fsS -X POST http://localhost:3000/api/v1/packs/blog.publish \
  -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
  -d '{
    "destination": "ghost",
    "format":      "markdown",
    "title":       "Hello from helmdeck",
    "body":        "# Welcome\n\nThis post was filed via blog.publish.",
    "host":        "blog.example.com",
    "tags":        ["demo","helmdeck"],
    "status":      "draft"
  }'
```

Response (Ghost succeeded — note the merged shape with **both** `post_id` and `artifact_key` from the #203 safety net):

```json
{
  "pack": "blog.publish",
  "version": "v1",
  "output": {
    "destination":  "ghost",
    "format":       "markdown",
    "body_source":  "input",
    "post_id":      "650f1234567890",
    "url":          "https://blog.example.com/p/hello-from-helmdeck/",
    "html_url":     "https://blog.example.com/p/hello-from-helmdeck/",
    "status":       "draft",
    "published_at": null,
    "artifact_key": "blog.publish/hello-from-helmdeck.md",
    "artifact_url": "https://s3.example/blog.publish/...?X-Amz-Signature=...",
    "size":         57
  }
}
```

To skip the artifact safety net (today's pre-#203 ghost-only behaviour), add `"also_save_artifact": false` to the request body. The response then omits `artifact_key`/`artifact_url`/`size` and Ghost failures revert to hard errors.

### Prompt mode + Ghost

```bash
curl -fsS -X POST http://localhost:3000/api/v1/packs/blog.publish \
  -H "Authorization: Bearer $JWT" -H 'Content-Type: application/json' \
  -d '{
    "destination": "ghost",
    "format":      "markdown",
    "title":       "Why packs beat naive function-calling",
    "prompt":      "Write a 400-word post arguing that typed packs (helmdeck) yield 10x lower per-task LLM cost than naive function-calling on Sonnet. Use a concrete example.",
    "model":       "openrouter/openai/gpt-4o-mini",
    "max_tokens":  600,
    "host":        "blog.example.com",
    "status":      "draft",
    "tags":        ["agent-architecture","cost"]
  }'
```

The pack calls the gateway LLM with a frozen system prompt that instructs it to emit ONLY the post body in the requested format (no preamble, no surrounding code fences, no repeated title).

## Partial success {#partial-success}

When `destination="ghost"` AND `also_save_artifact:true` (the default) AND Ghost fails after the artifact write, the pack returns a **successful HTTP response** carrying a partial-success shape. The body is safe in the artifact store; only the side-effect (Ghost publish) failed. The agent retries the Ghost step against the artifact without paying for prompt expansion again.

Example response when Ghost returns 401 (bad credentials):

```json
{
  "pack": "blog.publish",
  "version": "v1",
  "output": {
    "destination":  "ghost",
    "format":       "markdown",
    "body_source":  "model",
    "model_used":   "openai/gpt-4o-mini",
    "status":       "artifact_saved_ghost_failed",
    "ghost_error":  "ghost API POST https://blog.example/ghost/api/admin/posts/?source=html: 401 Authorization failed",
    "artifact_key": "blog.publish/why-packs-beat-naive-function-calling.md",
    "artifact_url": "https://s3.example/blog.publish/...?X-Amz-Signature=...",
    "size":         2174
  }
}
```

**Failure modes that go through partial success** (when the safety net is on):

- Vault credential missing or wrong shape (`<id>:<secret>` parse failure).
- Ghost API non-2xx response (auth, validation, rate-limit, 5xx).
- Egress guard denied the Ghost host.
- Markdown→HTML conversion failure (rare — affects malformed input).

**Failure modes that still hard-error** (artifact write itself broke):

- S3/MinIO artifact upload failed — there's nothing to fall back to. The pack returns `artifact_failed`.

**Opting out of the safety net:** Pass `"also_save_artifact": false` in the request. Today's pre-#203 contract is preserved verbatim — Ghost-only behaviour, and Ghost failures surface as hard errors with no artifact saved.

## Error codes

These are **hard errors** — the handler returns a `PackError` and no response shape. Many Ghost-side failures that previously hard-errored now surface in [Partial success](#partial-success) responses instead; only the cases where the safety net itself is bypassed or broken remain as hard errors below.

| Code | Triggers | Captured response |
|---|---|---|
| `invalid_input` | `destination` outside `"ghost"`/`"artifact"`/empty | `destination must be "ghost", "artifact", or empty (defaults to artifact)` |
| `invalid_input` | `format` outside `"markdown"`/`"html"` | `format must be "markdown" or "html"` |
| `invalid_input` | `title` empty | `title is required` |
| `invalid_input` | Both `body` AND `prompt` supplied | `must provide either body OR prompt+model, not both` |
| `invalid_input` | Neither `body` nor `prompt` supplied | `must provide either body OR prompt+model` |
| `invalid_input` | `prompt` set but `model` missing | `prompt mode requires model (provider/model)` |
| `invalid_input` | `status` outside the closed set | `status must be "draft", "published", or "scheduled"` |
| `invalid_input` | `status="scheduled"` without `published_at` | `published_at (RFC3339) is required when status=scheduled` |
| `invalid_input` | `published_at` not in the future | `published_at must be in the future for status=scheduled` |
| `invalid_input` | `destination="ghost"` without `host` | `host is required when destination=ghost` |
| `internal` | Prompt mode but pack registered without a gateway dispatcher | `blog.publish prompt mode registered without a gateway dispatcher` |
| `handler_failed` | Prompt expansion model returned no choices | `blog.publish prompt expansion: model returned no choices` |
| `artifact_failed` | Artifact store write failed (the safety net itself broke) | `artifact upload failed: …` |

**Hard errors only when `also_save_artifact:false`** (caller opted out of the safety net, so Ghost failures bubble up as in the pre-#203 contract):

| Code | Triggers | Captured response |
|---|---|---|
| `invalid_input` | `ghost-admin-key` not in vault | `vault credential "ghost-admin-key" not found …` |
| `invalid_input` | Vault key not in `<id>:<secret>` form | `ghost-admin-key vault value must be \`<id>:<secret>\` …` |
| `invalid_input` | Ghost host resolves to a blocked range | `egress denied: …` |
| `handler_failed` | Ghost API non-2xx | `ghost API POST …: 401 Authorization failed` |
| `handler_failed` | Markdown→HTML conversion failed | `markdown→html for Ghost: …` |

With `also_save_artifact:true` (the default), all five of those failures appear in the response's `ghost_error` field under `status="artifact_saved_ghost_failed"` — see [Partial success](#partial-success).

## Mermaid diagrams in technical posts

By default (`mermaid: true`), `blog.publish` **pre-renders** ```` ```mermaid ```` fenced blocks in a markdown body to **inline SVG** server-side — it shells to `mmdc` in the sidecar (the same renderer `slides.render` uses) and replaces each fence with an `<img src="data:image/svg+xml;base64,…" class="mermaid-svg" />`. The diagram is then **baked into the post**, so it renders identically on Ghost (regardless of theme), in email, in RSS, and in any plain-markdown reader — **no client-side MermaidJS required.**

| `mermaid` | Behaviour |
|---|---|
| `true` (default) | Fences → inline-SVG `<img>` before publishing. Works for every destination/format. Needs a session (the pack runs with one — see below). |
| `false` | Fences are left as ```` ```mermaid ```` and rendered **client-side**: in `html`/Ghost output they become `<pre class="mermaid">` + an injected MermaidJS `<script>` (via [goldmark-mermaid](https://pkg.go.dev/go.abhg.dev/goldmark/mermaid)); in `markdown` output the fence passes through verbatim (for renderers that know mermaid — Docusaurus, GitHub, MkDocs). Use this if you'd rather ship the source fence than a baked SVG, or your reader already loads MermaidJS. |

**Prompt-mode nudging.** In prompt mode the pack's system prompt instructs the model to emit ```` ```mermaid ```` fences when content is genuinely visual (architecture, request flow, sequence interactions, state transitions, decision trees) and to prefer prose otherwise. So "write a post about X with a diagram of its architecture" produces a diagram with no extra flag — and with the default `mermaid: true` it ships as SVG.

**Supported diagram kinds.** Anything mermaid supports: `flowchart`/`graph`, `sequenceDiagram`, `stateDiagram-v2`, `classDiagram`, `erDiagram`, `gantt`, `pie`, `mindmap`, etc. Invalid mermaid syntax fails the publish with `handler_failed` (the `mmdc` error), rather than silently shipping a broken diagram.

## Session chaining

**Runs with a session** (`NeedsSession: true`) so it can reach `mmdc` for server-side mermaid rendering — i.e. each publish acquires a short-lived sidecar even for diagram-free posts (set `mermaid: false` to skip the render work). Composes naturally:

- **`research.deep` → `content.ground` → `blog.publish`** — the canonical "evidence-grounded blog post" chain. Research surfaces sources; content.ground appends citations into a draft body; blog.publish ships it.
- **`web.scrape` → `blog.publish` (artifact mode)** — re-publish a scraped page as a draft artifact for later editing.
- **`repo.fetch` + `fs.read` + `blog.publish` (prompt mode)** — generate a release-notes blog post from a repo's recent changelog without the agent ever materializing the body itself.

## Async behavior

Synchronous. Wall-clock = (prompt-mode LLM call, ~3–10s if used) + (markdown→html via goldmark, ~1ms) + (Ghost API round-trip, ~200–800ms) for ghost mode; or just the goldmark step + artifact upload for artifact mode (~10–50ms).

## See also

- Catalog row: [`PACKS.md`](/PACKS) — `blog.publish`.
- Source: [`internal/packs/builtin/blog_publish.go`](https://github.com/tosin2013/helmdeck/blob/main/internal/packs/builtin/blog_publish.go).
- Issue: [#68](https://github.com/tosin2013/helmdeck/issues/68).
- Companion packs: [`research.deep`](../research/deep.md) (source discovery), [`content.ground`](../content/ground.md) (citation injection), [`http.fetch`](../http/fetch.md) (read-only blog API access if/when needed).
- Vault setup: see "Setup prerequisite" above.
- Ghost Admin API docs: <https://ghost.org/docs/admin-api/>
