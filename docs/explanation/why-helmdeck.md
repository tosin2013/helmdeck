---
title: Why helmdeck
description: Helmdeck lets cheap or local LLMs do work that otherwise needs frontier models. The mechanism is moving intelligence from the LLM into deterministic pack handlers. Per-task cost comparisons against Anthropic Computer Use, OpenAI Operator, Browser-use, Cursor, Claude Code direct, and Aider — with a "test it yourself" recipe.
keywords: [helmdeck, cost, weak models, gpt-oss, gemma, anthropic computer use, openai operator, browser-use, cursor, aider, agent architecture, mcp]
priority: 0.85
changefreq: monthly
---

# Why helmdeck

> ⚠️ **These numbers are one maintainer's findings on one install with a specific set of prompts.** They are not vendor-published benchmarks. We've tried to be honest about ranges and conditions, but **your numbers will probably differ**. The "Run the comparison yourself" section at the bottom shows exactly how to reproduce — if your findings disagree, [open an issue](https://github.com/tosin2013/helmdeck/issues/new) or contribute a [community blog post](/blog) and we'll cross-link.

## The headline argument

**Helmdeck lets a $0.10/1M-token model do work that otherwise needs a $3/1M-token model.** The mechanism is straightforward: helmdeck's 38 capability packs absorb the ambiguity that would otherwise burn LLM tokens. Deterministic Go code handles the side-effects (browser navigation, git operations, document parsing, file edits, vault credential resolution). The LLM only contributes orchestration — picking which pack to call and what arguments to pass.

For workflows that are repetitive enough to have a pack — browser scraping, code edits, GitHub triage, document parsing, slide narration, desktop automation — this is a 5–60× per-task cost reduction depending on the workflow, plus the outputs are correct on first try more often.

For one-off, novel work where no pack fits, helmdeck's overhead doesn't pay off. Use a frontier model directly. Honest section at the end.

## The structural reason

Every alternative approach asks the LLM to do all the work. They differ in **what** the LLM does, not in **how much** of the work the LLM owns:

- **Anthropic Computer Use / OpenAI Operator** — the LLM drives a screenshot-reason-action loop. Each step is a full vision-laden API call. A 20-step browser task is 20 Sonnet/GPT-4o calls.
- **Browser-use / Stagehand / Browserbase** — the LLM authors selectors and decisions; framework drives the actions. Cheaper per step, but still LLM-per-step.
- **Naive function-calling on Claude/GPT** — you wire tools, the LLM figures out schemas, retries, error semantics, state management on every fresh agent session.
- **Cursor / Claude Code / Aider** — the LLM reads your code, reasons about it, edits it, runs it, tests it. Sonnet-tier intelligence is the floor for the workflow to be reliable.

Helmdeck inverts the split. **Packs are typed, security-bounded, audited primitives.** The pack handler is Go code that already knows how to talk to Firecrawl / Docling / Playwright MCP / git / xdotool / GitHub's REST API. The LLM emits a short JSON tool call (~50–200 tokens) and reads a short JSON response (~200–800 tokens). It doesn't need to figure out the API surface — that work is done once, in code, and amortized across every invocation forever.

This is the same pattern that's been load-bearing in software for decades:

| Cheap deterministic layer | Expensive judgment layer |
|---|---|
| Compiler | The programmer choosing the algorithm |
| Postgres | The application choosing the query |
| Kubernetes | The team designing the deployment |
| **Helmdeck packs** | **The LLM choosing which pack to call** |

You move recurring deterministic work *out* of the expensive token-priced layer *into* the cheap deterministic layer, and reserve the expensive layer for the irreducibly judgment-y parts.

## Per-task comparisons

Five canonical workflows. Each table compares the dominant alternative approaches. Numbers are observed on a single helmdeck install with `gpt-oss-120b` as the chat model and `claude-haiku-4.5` as the vision model where applicable, captured during PR-A and PR-B documentation runs (commits in [`docs/reference/packs/`](https://github.com/tosin2013/helmdeck/tree/main/docs/reference/packs)). **Verify by running [the recipe at the bottom](#run-the-comparison-yourself).**

### 1. Browser scrape + GitHub comment

*Goal: scrape an article from a public URL, file a GitHub issue summarizing it.*

| Stack | Approx cost | Why |
|---|---|---|
| Anthropic Computer Use API | **$0.25–$0.40** | 8–12 vision-laden Claude Sonnet calls to navigate, screenshot, parse, summarize. Each screenshot adds ~1500 image tokens. |
| OpenAI Operator | **$0.15–$0.30** | Similar shape; o1/o4-mini-tier vision pricing. |
| Browser-use + Sonnet | **$0.10–$0.20** | LLM-per-step but no vision; cheaper than Computer Use but still O(steps). |
| **Helmdeck** (`web.scrape` + `github.create_issue` on `gpt-oss-120b`) | **$0.005–$0.010** | One Firecrawl-backed call returns clean Markdown, one GitHub REST call posts the issue. Two short LLM round-trips total. |

### 2. Code-edit loop (clone, read, patch, test, commit, push)

*Goal: clone a repo, read a file, apply a one-line edit, run tests, commit, push.*

| Stack | Approx cost | Why |
|---|---|---|
| Cursor / Claude Code direct (Sonnet) | **$0.20–$0.50** | Sonnet reads entire files into context, reasons about them, edits in-place. Each iteration is a full Sonnet round; multi-step workflows cost extra. |
| Aider with Sonnet | **$0.25–$0.80** | Each turn is a Sonnet call; Aider's repo-map adds a non-trivial input-token bill. |
| **Helmdeck Phase 5.5** on `gpt-oss-120b` | **$0.05–$0.10** | `repo.fetch` returns a context envelope (tree + readme + entrypoints + signals); `fs.read` returns bytes; `fs.patch` applies a literal change; `cmd.run` runs the test; `git.commit` + `repo.push` finish. Six tool calls, six short LLM round-trips. |

### 3. Multi-step browser test

*Goal: navigate to a SPA, log in, verify a feature works, report pass/fail.*

| Stack | Approx cost | Why |
|---|---|---|
| Playwright + Claude (LLM authors selectors) | **$0.10–$0.30** | LLM has to figure out CSS selectors from a DOM dump. Frequent retries when selectors miss. |
| Browser-use NL agent | **$0.15–$0.40** | LLM-per-step screenshot + decision loop, plus Browserbase fees. |
| **Helmdeck `web.test`** on `gpt-oss-120b` | **$0.02–$0.05** | Playwright MCP returns an accessibility tree with `[ref=eN]` identifiers; the LLM addresses elements directly. No vision required. |

### 4. Document parsing — PDF → structured Markdown

*Goal: parse a 30-page PDF (academic paper, legal contract) into clean Markdown with tables preserved.*

| Stack | Approx cost | Why |
|---|---|---|
| Naive Sonnet with vision | **$0.50–$2.00** | Each page is a 1500-token image input. 30 pages = 45k vision tokens × Sonnet pricing. |
| Unstructured.io / LlamaParse SaaS | **$0.10–$0.30 / page** | Per-page metered pricing on hosted parsers. |
| **Helmdeck `doc.parse`** (Docling overlay) | **$0.001–$0.005** | Docling does the OCR/layout work as a self-hosted service. The LLM only contributes "this is a paper, please extract" — one tool call. |

### 5. Slide deck → narrated video

*Goal: take Marp markdown with speaker notes, produce a narrated 1080p MP4 with YouTube metadata.*

| Stack | Approx cost | Why |
|---|---|---|
| Manual orchestration in shell | **Engineering time + ElevenLabs + ffmpeg** | You wire Marp → ElevenLabs → ffmpeg → YouTube CLI yourself. Hours of glue. |
| Pictory / Synthesia / Heygen SaaS | **$0.20–$2.00 / minute of video** | Per-minute video pricing on hosted slide-narration services. |
| **Helmdeck `slides.narrate`** | **ElevenLabs cost (~$0.05 / minute) + ~$0.001 LLM** | `slides.narrate` does the entire pipeline: Marp render, ElevenLabs TTS, ffmpeg concat, YouTube metadata. The LLM only authors the speaker notes (which it does anyway). |

## Where the SKILLS bundle compounds the savings

Helmdeck ships a curated agent skill at [`docs/integrations/SKILLS.md`](/integrations/SKILLS) that loads into your MCP client's system prompt. It teaches the model the pack catalog, error codes, the session-chaining contract, and the freshness contract.

Without SKILLS loaded, weak models predictably fumble: they emit Anthropic-shape `fs.patch` arguments instead of helmdeck-shape (4 retries × $0.02 = $0.08 per fresh agent), drop `_session_id` between chained calls (silent empty results, wasted turns), or hallucinate pack names like `helmdeck.fs.write_file` instead of `helmdeck__fs-write` (1–2 unknown-tool retries each).

Concrete observed numbers from the PR-A capture pipeline:

| Scenario | Phase 5.5 loop cost on `gpt-oss-120b` |
|---|---|
| SKILLS not loaded | $0.15–$0.30 with retries; broken outputs the user often has to redo |
| SKILLS loaded (~9 KB system prompt, prompt-cached) | $0.05–$0.10, clean first-try outputs |

The SKILLS bundle adds ~9 KB of system-prompt overhead per turn, but with prompt caching (Anthropic, OpenAI, OpenRouter all support it) cached reads cost ~10% of writes. The marginal per-turn cost is $0.0001–$0.0005 — dwarfed by the retry savings.

## Where helmdeck doesn't win

Honest list. Don't switch to helmdeck for these:

- **One-off, ad-hoc tasks where no pack fits.** Pack overhead doesn't amortize over a single use. Just ask Sonnet directly.
- **Highly novel workflows where the LLM has to reason from first principles.** The packs absorb common shapes; truly new shapes still need the model to invent.
- **Organizations already running tuned Sonnet pipelines that work.** The savings come from switching to a cheaper model, not from helmdeck itself. If your Sonnet bill is fine and the workflow is reliable, don't fix what isn't broken.
- **Self-hosted ops cost.** A helmdeck install needs CPU + RAM for sidecars, storage for artifacts, and someone to run upgrades. For one user / one workflow / one machine, this is overhead. The economics work when you're running many tasks across many users on shared infrastructure.

If your situation hits any of those, the comparison tables above don't apply to you. Check [the install tutorial](/tutorials/install-cli) for the operational overhead before committing.

## Summary

| Workflow | Frontier-model approach | Helmdeck (gpt-oss-120b) | Reduction |
|---|---|---|---|
| Browser scrape + GitHub comment | $0.25 | $0.005 | ~50× |
| Code edit loop (6 steps) | $0.35 | $0.07 | ~5× |
| Multi-step browser test | $0.20 | $0.03 | ~7× |
| PDF → structured Markdown | $1.00 | $0.003 | ~300× |
| Slide deck → narrated video | (engineering time) | (TTS cost only) | infinite-ish |

Median is ~10× per-task cost reduction. The variance is wide because some workflows have a pack that does most of the work in deterministic code (PDF parsing, slide narration), and others split work more evenly between the LLM and the pack (code editing).

## Run the comparison yourself

These numbers are one maintainer's findings. The most useful thing you can do is reproduce them on your own hardware, with your own models, and share what you find. We've made this as easy as possible:

### What you need

- A working helmdeck install — see [the CLI install tutorial](/tutorials/install-cli)
- An OpenRouter account with at least $5 of credit (covers all 5 workflows comfortably)
- Optionally: an Anthropic API key + Computer Use access if you want to run the Computer Use comparison
- About 30 minutes

### The recipe

1. **Install helmdeck and connect a client** (OpenClaw is the validated path) — follow [the OpenClaw integration doc](/integrations/openclaw).
2. **Load the SKILLS bundle** — `configure-openclaw.sh` does this automatically.
3. **Run each comparison workflow.** The exact prompts used to derive the numbers in the tables above are in [`scripts/oc-capture/prompts/`](https://github.com/tosin2013/helmdeck/tree/main/scripts/oc-capture/prompts) — `easy-cluster.txt` and `medium-cluster.txt` together cover the input side of all five workflows.
4. **Capture the OpenRouter cost.** OpenRouter's `/credits` endpoint or its dashboard shows per-call cost; helmdeck's audit log records every pack invocation. Compare the two.
5. **Run the same workflow on your competitor stack.** For Computer Use / Operator / Browser-use, follow each vendor's quickstart with the same instruction prompts.
6. **Tabulate.** Same shape as the tables above: stack, observed cost, your model + hardware.

### Share your findings

If your numbers are within the ranges quoted, great — that's a reproduction.

If your numbers **disagree** (lower OR higher), please share. Two ways:

- **Quick path** — [open an issue](https://github.com/tosin2013/helmdeck/issues/new) titled `cost-reproduction: <workflow>` with your numbers and conditions. We'll cross-link from this page.
- **Deeper path** — [submit a community blog post](/blog) with your full methodology. See [`CONTRIBUTING.md`](https://github.com/tosin2013/helmdeck/blob/main/CONTRIBUTING.md) §"Other contribution types" for the workflow. We particularly want **independent reproductions** — they're more valuable than fresh marketing pitches.

If a community reproduction surfaces a real discrepancy, this page gets updated with the new numbers and a link to the report. The goal is that the per-task numbers stay calibrated to reality, not to whatever the maintainer measured once.

## See also

- [Get started — install helmdeck](/tutorials/install-cli)
- [OpenClaw integration (validated end-to-end)](/integrations/openclaw)
- [SKILLS.md — the agent skill bundle](/integrations/SKILLS)
- [Pack catalog](/PACKS) — the 38 capability packs the comparisons use
- [Helmdeck blog](/blog) — short-form posts including the headline cost-positioning piece
- [Architecture decisions](/adrs) — the structural rationale ADRs (especially ADR 003 on weak-model-first design and ADR 035 on host-don't-rebuild)
