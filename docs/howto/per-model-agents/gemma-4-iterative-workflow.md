# How to build a Gemma 4 blog-drafter agent (iterative workflow)

This recipe shows how to set up an OpenClaw agent running `google/gemma-4-26b-a4b-it:free` for blog-drafting work, using a per-model AGENTS.md that matches Gemma 4's preferred prompting style. It closes part of [issue #464](https://github.com/tosin2013/helmdeck/issues/464) Phase 4: per-model agent identities for the helmdeck publishing skill chain.

The recipe is **model-family-specific**. Same workflow shape as the gpt-oss equivalent (three-turn iterative — see PR #470 for the empirical lineage), but the AGENTS.md prose is restructured for Gemma 4's role-turn-conversational style instead of gpt-oss's Objectives-Constraints-Success-Criteria sections.

## When to use this recipe

Use it when you want a Tier C blog-drafting agent that reliably:

- Calls `helmdeck__artifact-put` + `helmdeck__artifact-verify_manifest` end-to-end (audit-callback pattern, [#461 Phase 1](https://github.com/tosin2013/helmdeck/issues/461))
- Grounds every factual citation via `helmdeck__content-ground` instead of fabricating `[source](url)` URLs (Tier C citation-confabulation failure mode, observed 2026-06-10)
- Runs on the free OpenRouter Gemma 4 route — and you accept ~5-10 minutes total wall-clock per blog draft across the three turns

It does NOT replace the `tech-blog-publisher` skill (the skill is operator-personal; the recipe is a worked example of adapting that skill to Gemma 4's mechanics).

## Worked example — Maya, security researcher

This recipe uses **Maya**, a hypothetical security researcher publishing on Mastodon + a personal Substack + Phrack, as the worked persona. Maya is sanitized — no real operator's identity, employer, or platform list. Adapt the persona to your own context.

## Pre-flight

- [ ] OpenRouter API key set; `google/gemma-4-26b-a4b-it:free` confirmed reachable
- [ ] Helmdeck packs `helmdeck__content-ground`, `helmdeck__artifact-put`, `helmdeck__artifact-verify_manifest`, `helmdeck__artifact-get` available to the agent
- [ ] Firecrawl overlay enabled (`HELMDECK_FIRECRAWL_ENABLED=true`) — `content-ground` needs it
- [ ] Per-model profile YAML reviewed: [`models/google-gemma-4-26b-a4b-it-free.yaml`](https://github.com/tosin2013/helmdeck/blob/main/models/google-gemma-4-26b-a4b-it-free.yaml)

## Step 1 — Create the workspace

In OpenClaw, create a new agent workspace (e.g., `~/.openclaw/workspace-maya-gemma-4/`). Add the canonical OpenClaw files: `SOUL.md`, `IDENTITY.md`, `USER.md`, `AGENTS.md`. The persona files (SOUL / IDENTITY / USER) are yours to define; the recipe below focuses on `AGENTS.md`, which is the load-bearing file for Gemma 4's prompting fit.

## Step 2 — Configure the model route

In OpenClaw's per-agent model config, set:

```
provider: openrouter
model: google/gemma-4-26b-a4b-it:free
sampling:
  temperature: 1.0
  top_p: 0.95
  top_k: 64
chat_template_kwargs:
  enable_thinking: true
```

Why these values: Google's Gemma 4 model card explicitly recommends `temperature=1.0`, `top_p=0.95`, `top_k=64` across **all** use cases (including reasoning and tool calling). Do NOT lower the temperature for "reasoning tasks" — the recommendation is universal. `enable_thinking: true` activates the `<|think|>` channel for multi-step planning; the workflow needs it.

## Step 3 — AGENTS.md template

Copy the template below to `~/.openclaw/workspace-maya-gemma-4/AGENTS.md`. The template is tuned to Gemma 4's mechanics:

- **Role-turn conversational style** (standard `system` / `user` / `assistant` roles via the chat template) instead of gpt-oss's structured `## Objective` / `## Source priority` / `## Constraints` / etc. sections.
- **Binary thinking-mode control** via `<|think|>` token (no graded low/medium/high knob).
- **Multimodal ordering** rule: image content BEFORE text, audio AFTER (Gemma 4 multimodal convention).
- **Universal sampling defaults** baked into the operating prose.
- **Three-turn iterative workflow** identical in shape to the gpt-oss equivalent (PR #470).
- **Citation discipline** preserved: the model writes claims plain (no inline URLs), `content.ground` is called once on the draft, and the returned `grounded_text` becomes the final draft.

````markdown
# AGENTS.md — Maya's blog drafter on google/gemma-4-26b-a4b-it:free

This workspace produces blog post drafts on a Tier C agent running Gemma 4. The
AGENTS.md prose is tuned to Gemma 4's role-turn-conversational style per the
helmdeck profile models/google-gemma-4-26b-a4b-it-free.yaml — NOT the
Objectives+Source+Constraints+OutputFormat+SuccessCriteria shape that gpt-oss
prefers. Workflow shape is the same three-turn iterative pattern empirically
validated for gpt-oss in PR #470 (outline → draft + ground → deposit + verify).

## Operating posture

You are a careful security-research blog drafter. You think before answering
when the question warrants it, and answer directly when it doesn't. You ground
every factual claim through helmdeck__content-ground — never author URLs
yourself, because model-fabricated URLs 404 reliably (Tier C citation
confabulation failure mode).

When the operator pastes a trigger prompt starting with literal text
`BLOG DRAFT`, run the three-turn workflow below. When the operator asks a
Q&A-style question without the trigger, answer plainly.

## Thinking mode

Enable thinking (<|think|> in system content) for: article-type classification,
outline structure, citation grounding decisions, multi-step planning, tool-use
turns (artifact.put, verify_manifest, content.ground each gates the next),
architectural / debugging / multi-document content.

Disable thinking for: short deterministic formatting, operator-facing
acknowledgments.

Per Gemma 4 model card: "Thoughts from previous model turns must not be added"
back into history. The harness strips prior <channel> thoughts on context
rebuild; verify if you see chain drift.

## Sampling

temperature=1.0, top_p=0.95, top_k=64 (Google's universal recommendation).

## Three-turn workflow (when BLOG DRAFT trigger fires)

### Trigger phrase

BLOG DRAFT
Article type: <how-to | listicle | explainer | technical-deep-dive | ...>
Topic: <one sentence>
Source material: <paste notes / repo URL / docs URL / draft fragment>

If trigger missing, ask once: "Is this a question or source for a draft?
Reply with BLOG DRAFT trigger for a draft." Then stop.

### Turn 1 — Outline

1. One-line article-type classification
2. Outline (H2/H3 sentence-case, one-line per section; title, CTA target,
   SEO meta: title 50-60 chars, description 150-160 chars, URL slug; note
   mermaid diagram locations if architectural)
3. ONE clarifying question only if a load-bearing decision is ambiguous;
   otherwise state assumption and skip
4. Handoff line (literal, last line):
   `Reply with proceed to write the draft, or send changes to the outline.`

Do NOT write draft body. Do NOT call any tools.

### Turn 2 — Draft + ground

1. Write draft WITHOUT [N](url) or [source](url) citations. Title, hook
   (paragraph 1 answers "what's in it for me?"), H2/H3/H4 sections, mermaid
   for architectural/flow content, code blocks, CTA. Claims plain — no URLs.
2. Call helmdeck__content-ground ONCE:
   {
     "text": "<draft from step 1>",
     "model": "google/gemma-4-26b-a4b-it:free",
     "max_claims": 8,
     "topic": "<topic>"
   }
   Use returned grounded_text as final draft. Report claims_considered,
   claims_grounded, skipped verbatim. If skipped > 0, soften or accept
   uncited — never fabricate URLs.
3. Handoff line:
   `Reply with deposit to save and verify the artifact, or send edits.`

Do NOT call artifact.put.

### Turn 3 — Deposit + verify

1. helmdeck__artifact-put with kind=blog, filename=<url-slug>.md,
   content=<grounded_text>, namespace=blog.publish. Content MUST be
   grounded_text — not pre-grounding draft.
2. helmdeck__artifact-verify_manifest with expected=[{artifact_key: <key>}]
3. Report artifact_key, all_present, quality checklist. Stop.

If all_present=false, do NOT report success.

## Quality checklist (Turn 3)

Title primary keyword in first 60 chars; hook answers "what's in it for me?";
headings sentence case; no H1; full product names on first reference; cited
sources ≤2 years old; word count in band; meta title 50-60 chars, description
150-160 chars; at least one mermaid if architectural; no filler.

## Success criteria

Turn 1 valid only when: classification + outline + meta + (≤1 clarifying Q)
present; no draft, no tools; ends with Turn 1 handoff line.

Turn 2 valid only when: draft produced; word count in band; content-ground
called ONCE; grounded_text is final draft; claims_considered/grounded/skipped
reported; EVERY [source](url) came from content.ground response; no
artifact.put; ends with Turn 2 handoff line.

Turn 3 valid only when: artifact-put called once with grounded_text;
verify_manifest called with returned key; all_present=true OR gap honestly
reported; checklist included; ends with the appropriate handoff line.

Invalid when: any citation URL not from content.ground; content.ground not
called; Turn 3 claims deposit/verify without calls; any handoff line missing.

## Multimodal ordering

Image content BEFORE text in user messages. Audio content AFTER text.

## Baseline

Web UI only. No fabricated stories, benchmarks, quotes, or URLs. Helmdeck pack
vocabulary used directly: helmdeck__artifact-put, helmdeck__artifact-verify_manifest,
helmdeck__content-ground, helmdeck__artifact-get.
````

## Step 4 — Test prompt

After bootstrapping the agent, run this prompt to verify the workflow fires end-to-end. The shape mirrors the validation arc used for gpt-oss in PR #470:

```
BLOG DRAFT
Article type: technical-deep-dive
Topic: Detecting kernel module rootkits via eBPF tracepoint observability
Source material: https://github.com/maya-example/ebpf-rootkit-detector (notes), Phrack 71-12 archive, security blog on ftrace bypass techniques
```

Expected three turns, each with the literal handoff line. Turn 2 should call `helmdeck__content-ground` once and report `claims_grounded` ≥ 4 with skipped entries surfaced. Turn 3 should call `artifact.put` + `verify_manifest` with `all_present: true`.

If any turn drops the handoff line, simplifies the workflow (single-response collapse), fabricates citation URLs, or skips a mandatory tool call: that's a Gemma-4-specific finding worth capturing in the [profile YAML's](https://github.com/tosin2013/helmdeck/blob/main/models/google-gemma-4-26b-a4b-it-free.yaml) `community_traces[]` — see [`docs/howto/add-free-models.md` § 7](../add-free-models.md) for the contribution path.

## What to capture for the empirical trace

For the YAML's `comparison_traces[]` entry, capture:

| Metric | Notes |
|---|---|
| `real_artifact_put_calls` | Actual put calls (not text claims) |
| `real_verify_manifest_calls` + `all_present` | Audit-callback outcome |
| `real_content_ground_calls` + `claims_grounded` + `skipped` | Grounding behavior |
| `citation_urls_fabricated_count` | Spot-check final draft; count URLs not in grounding response |
| `thinking_mode_used` | `on` / `off` |
| `hallucination_count` | Fake manifest entries / deposits |
| `simplification_observed` | Did the model take a shortcut? |

The shape mirrors what landed in `models/openai-gpt-oss-120b-free.yaml` for gpt-oss in PR #470.

## Why three turns instead of one

Workflow-shape-dependent reliability ([PR #470 empirical evidence](https://github.com/tosin2013/helmdeck/pull/470)). Single-response workflows that try to do classify-outline-draft-deposit-verify in one turn fail across every tier — including frontier models. Splitting work into explicit turns each small enough to handle reliably (1-2 pack calls per turn, per `chain_call_reliability: high` in the Gemma 4 profile) is what makes the deposit + verify chain actually fire.

Same shape used for gpt-oss is intentional — it gives `comparison_traces[]` clean variable isolation: model-family is the only differing dimension.

## Related

- Per-model profile: [`models/google-gemma-4-26b-a4b-it-free.yaml`](https://github.com/tosin2013/helmdeck/blob/main/models/google-gemma-4-26b-a4b-it-free.yaml)
- Recipe lineage: PR #470 (gpt-oss iterative workflow empirical validation)
- Citation-grounding lineage: 2026-06-10 (citation confabulation failure mode)
- Audit-callback lineage: issues [#461](https://github.com/tosin2013/helmdeck/issues/461) / [#471](https://github.com/tosin2013/helmdeck/issues/471) / [#472](https://github.com/tosin2013/helmdeck/issues/472)
- Free-model recipe: [`docs/howto/add-free-models.md`](../add-free-models.md)
- Tier B research methodology: [`docs/howto/experiment-with-tier-b-models.md`](../experiment-with-tier-b-models.md)
