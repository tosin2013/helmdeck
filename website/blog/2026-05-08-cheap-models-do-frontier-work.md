---
slug: cheap-models-do-frontier-work
title: Why a $0.10 model can do work that needs a $3 model
authors: [tosin]
tags: [cost, mcp, weak-models, agent-architecture]
description: Helmdeck moves intelligence from the LLM to the pack handler. A look at where the cost actually goes — and how cheap or local models can run agentic workflows that frontier-model APIs charge 10× more for. With the prompts and recipe to test it yourself.
image: /img/social-card.png
date: 2026-05-08
---

> ⚠️ **These are my findings, not a vendor benchmark.** I ran them on one helmdeck install, with a specific set of prompts, against a few specific competitor stacks. Your numbers will probably differ. The recipe to reproduce is at the bottom — if your numbers disagree, please [share](https://github.com/tosin2013/helmdeck/issues/new) and I'll update this page.

Today's helmdeck install ran a 6-step Phase 5.5 code-edit loop on `gpt-oss-120b` for **$0.07 total** — clone a repo, read a file, apply a one-line patch, run tests, commit, push. The same loop on Cursor / Claude Code direct via Sonnet would have run **$0.30+**. Same outcome; ~5× cost gap.

That's not unusual. Here's what I see across five common workflows:

<!-- truncate -->

| Workflow | Frontier-model approach | Helmdeck (gpt-oss-120b) |
|---|---|---|
| Browser scrape + GitHub comment | $0.25 (Anthropic Computer Use) | **$0.005** |
| Code edit loop (6 steps) | $0.35 (Cursor / Aider) | **$0.07** |
| Multi-step browser test | $0.20 (Browser-use NL) | **$0.03** |
| PDF → structured Markdown | $1.00 (naive Sonnet vision) | **$0.003** |

Median is roughly 10× per-task cost reduction. Why?

## The structural reason

Every alternative approach asks the LLM to do all the work. Anthropic's Computer Use API has Sonnet drive a screenshot-reason-action loop where every step is a vision-laden API call. OpenAI Operator does the same shape on GPT-4o. Browser-use has the LLM author selectors and decisions per step. Cursor and Claude Code read entire files into context to reason about a one-line edit. Naive function-calling on Sonnet has the model figure out tool schemas, retries, error semantics, and state management on every fresh agent session.

Helmdeck inverts the split. **Packs are typed, security-bounded, audited primitives.** The pack handler is Go code that already knows how to talk to Firecrawl / Docling / Playwright MCP / git / xdotool / GitHub's REST API. The LLM emits a short JSON tool call (~50–200 tokens) and reads a short JSON response (~200–800 tokens). It doesn't need to figure out the API surface — that's done once, in code, and amortized forever.

This is the pattern that's been load-bearing in software for decades. Compilers vs. interpreters. Postgres vs. let-the-LLM-compute-everything. Move recurring deterministic work *out* of the expensive token-priced layer *into* the cheap deterministic layer. Reserve the expensive layer for the irreducibly judgment-y parts.

[`SKILLS.md`](/integrations/SKILLS) — the agent skill bundle — teaches the model the catalog and contracts upfront so it picks the right pack on first try. It's ~9 KB, prompt-cached, and shaves another ~50% off per-workflow cost on weak models because they stop fumbling schemas and dropping session ids.

## One concrete example: web.scrape

Say you need to scrape an article and post a GitHub issue summarizing it.

**Anthropic Computer Use approach.** Sonnet receives a goal. It takes a screenshot. It reasons "I should navigate to the URL" and emits a `computer` tool call. It gets a screenshot back. It reasons "the page is loaded, I should select the article body" and emits a `computer` call to scroll. Another screenshot. Another reason step. Maybe 8–12 turns later, it has the content extracted, summarizes it, and emits one more call to file the issue. Each turn carries 1500+ image tokens. Total: ~$0.25.

**Helmdeck approach.** The LLM emits one tool call:

```json
{"name": "helmdeck__web-scrape", "arguments": {"url": "https://example.com/article"}}
```

The pack handler talks to Firecrawl, gets clean Markdown back, returns it as the tool result. The LLM emits one more tool call:

```json
{"name": "helmdeck__github-create_issue",
 "arguments": {"repo": "owner/repo", "title": "...", "body": "..."}}
```

Two short LLM round-trips. Total: ~$0.005.

The 50× cost gap isn't because helmdeck has a cleverer model — it's because Firecrawl already knows how to scrape SPAs, and a deterministic pack handler is doing 90% of the work that the model would otherwise spend tokens rediscovering on every run.

## Where it doesn't win

I'm not arguing helmdeck wins everywhere:

- **One-off, ad-hoc tasks where no pack fits.** Pack overhead doesn't amortize over a single use; just ask Sonnet directly.
- **Truly novel workflows** where the LLM has to reason from first principles. Packs absorb common shapes; new shapes still need the model to invent.
- **Orgs already running tuned Sonnet pipelines that work.** Don't fix what isn't broken.
- **Self-hosted ops cost.** A helmdeck install needs CPU/RAM for sidecars, storage, upgrades. The economics work when you're running many tasks across shared infra, not for one user / one machine / one workflow.

If your situation hits any of these, the comparison numbers don't apply to you. The full breakdown — including all five workflows and the model-vs-pack split per task — is on the long-form [Why helmdeck](/explanation/why-helmdeck) page.

## Test it yourself

The most useful thing you can do with this post is reproduce the numbers (or refute them) on your own hardware:

1. [Install helmdeck](/tutorials/install-cli) (~30 min).
2. Connect [OpenClaw](/integrations/openclaw) — it's the validated end-to-end client.
3. Run the prompts at [`scripts/oc-capture/prompts/easy-cluster.txt`](https://github.com/tosin2013/helmdeck/tree/main/scripts/oc-capture/prompts) against your model of choice.
4. Run the same workflows on whichever competitor stack you're evaluating against.
5. Compare costs from each provider's billing dashboard.

If your numbers come back within the ranges quoted above, that's a reproduction. If they **disagree** — lower or higher — please [open an issue](https://github.com/tosin2013/helmdeck/issues/new) titled `cost-reproduction: <workflow>`, or [submit a community blog post](/blog) with your full methodology. See [`CONTRIBUTING.md`](https://github.com/tosin2013/helmdeck/blob/main/CONTRIBUTING.md) §"Other contribution types" for how to add yourself as an author.

We particularly want **independent reproductions** — your real findings on your real hardware are more valuable than another marketing pitch from me. The [Why helmdeck](/explanation/why-helmdeck) page will get updated with your numbers (and a link to your post) if your reproduction surfaces a meaningful discrepancy.

## See also

- [Why helmdeck](/explanation/why-helmdeck) — the long-form version of this post with all five comparison tables and a full reproduction recipe
- [Get started — install helmdeck](/tutorials/install-cli)
- [SKILLS.md](/integrations/SKILLS) — the agent skill bundle that's load-bearing for the cheap-model story
- [Pack catalog](/PACKS) — the 36 capability packs the comparisons use
