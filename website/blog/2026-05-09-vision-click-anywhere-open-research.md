---
slug: vision-click-anywhere-open-research
title: "vision.click_anywhere works mechanically. The model still doesn't. Five projects waiting for someone to build them."
authors: [tosin]
tags: [vision, agent-architecture, mcp, open-research, computer-use]
description: The screenshot loop in helmdeck's vision packs converges. The vision model rarely emits "done". Here are five concrete open-source projects — verifiers, MCP servers, observability standards, prompt harnesses, fine-tuning datasets — that any one of which would solve the gap. None of them need to live in helmdeck. All of them are waiting for the right contributor.
image: /img/social-card.png
date: 2026-05-09
draft: true
---

> [Issue #112](https://github.com/tosin2013/helmdeck/issues/112) is the canonical research thread. This post pulls the same thinking into a project-shaped frame: five separable projects, any one of which would close the gap. If you're an OSS maintainer or researcher looking for a 3–6 month project that lots of people would benefit from — pick one.

In v0.10.0 we shipped a [mechanical fix](https://github.com/tosin2013/helmdeck/pull/105) for `vision.click_anywhere`'s "loops forever clicking the same coords" bug. The fix worked: per-step screenshots now show genuine visual progression instead of identical bytes. Xvfb repaints, scrot captures the new frame, the next iteration sees a different image.

And the model still doesn't emit `done`.

We tested with three plausible goals — "click the URL bar to focus it", "click the New Tab button at the top of the Chromium window", "click anywhere in the center of the visible window". Per-step coordinates were sensible, screenshots changed turn-to-turn, but `claude-haiku-4.5` (and similar small/cheap vision models) hit `max_steps: 10` with `completed: false` every time. The model could see the screenshot. It couldn't *decide* whether the click had succeeded.

<!-- truncate -->

## The actual problem

Computer-use agents have **two distinct sub-problems**:

1. **Where to click** — a generative task. Look at a screenshot, propose a coordinate. Vision models do this passably.
2. **Did it work?** — a discriminative task. Compare pre-action and post-action state, decide whether the goal is achieved. Vision models do this *badly*, especially at the cheap-and-fast end of the model market.

Today's agent loops conflate the two. The same model that proposed the click is asked, on the next turn, "given the new screenshot, are you done?" The model is conservative: small visual changes (a focus highlight, a cursor blink one frame past the screenshot capture) are easy to miss, and "I'm not sure" reads to the model as "keep trying", not "emit done".

Anthropic's own [computer-use docs](https://docs.anthropic.com/en/docs/build-with-claude/computer-use) acknowledge this: even the strongest vision models struggle with did-my-click-work.

## Why this isn't a helmdeck problem to solve

We could keep iterating in helmdeck core — better prompt nudges, two-shot click confirmation, an extra screenshot pass per turn. We've enumerated those approaches in #112's "Avenues to research" section and we'll probably try them in some order.

But the underlying problem is bigger than any one agent platform. **It's a missing layer in the open agent ecosystem.** Every framework that ships a `click_anywhere`-shaped tool — LangChain, AutoGen, OpenAI Operator, Anthropic Computer Use, Gemini CLI's experimental computer-use mode, OpenInterpreter, Browser-use — is fighting the same bug.

The right fix is **separation of concerns**. The action layer generates intent. A *separate, deterministic verification layer* decides whether the intent succeeded. The verification layer needs to be cheap, fast, and not a frontier-model call — otherwise we just shifted the cost.

That's not one project. That's at least five.

## Five projects waiting for someone to build them

Each is filed as a helmdeck issue, but **none of them need to live in helmdeck**. They're standalone open-source projects that helmdeck (and every other agent framework) would happily integrate with via MCP or a well-shaped CLI.

### 1. OpenCVD — Open Computer-Vision Done-detector

**The bet:** A standalone, lightweight visual verification engine. Takes a pre-action screenshot, the intended action ("click URL bar"), and a post-action screenshot. Returns a binary SUCCESS / FAILURE with a confidence score — using fine-tuned small-parameter vision models (OmniParser variants, fine-tuned Florence-2) or traditional CV heuristics (structural similarity, focus-ring detection, multi-frame cursor-blink sampling).

**Why it's a separate project, not a helmdeck PR:** Verification is useful to every agent framework, not just helmdeck. The natural shape is a small server that exposes one endpoint (or one MCP tool) — `verify(pre_screenshot, action, post_screenshot) → {success, confidence}` — and lets every agent platform plug it in.

**Who would build it:** Computer-vision researchers, QA-automation engineers, agentic-framework maintainers.

**Tracked at:** [helmdeck#…](#) (issue link below).

### 2. helmdeck `verify` MCP server

**The bet:** A dedicated MCP server with explicit assertion tools — `assert_element_focused`, `assert_text_visible`, `await_visual_change`, `compare_screenshots`. The agent's prompt structure shifts from "do this and tell me when you're done" to "do this, then *call the verify tool* to check".

This formalizes "two-shot click confirmation" from #112's avenue (E) into a typed tool call instead of a system-prompt instruction.

**Why it's separate:** It's an MCP server. It composes with helmdeck (and any other MCP-aware agent), and the toolset evolves on a different cadence than helmdeck's pack schemas.

**Who would build it:** TypeScript / Go developers familiar with the MCP SDK; accessibility-tree experts; UI-automation veterans.

### 3. UI-Trace — agentic telemetry standard

**The bet:** Today's loop asks the model to make decisions from a single static PNG. UI-Trace is a low-overhead local recording daemon that captures a rolling buffer (5 sec pre/post action), the accessibility tree, and OS-level events (cursor blinks, focus changes). Agents can query *"what changed visually in the last 2 seconds at coordinates (x, y)?"* — solving the missed-cursor-blink problem at the source.

This is OpenTelemetry for computer-use agents. The schema needs to be open, the recorder needs to be tiny, and the API needs to be the same on Xvfb, Wayland, and macOS.

**Why it's separate:** Telemetry standards live or die by adoption across many frameworks. A standard owned by helmdeck would fail; a standard owned by a small spec body — like OpenTelemetry's working-group model — could become real.

**Who would build it:** Systems programmers (Rust/C++), observability experts, OS-level hackers.

### 4. Prompt-Harness — dynamic prompt injection for stuck agents

**The bet:** Instead of throwing CV at the problem, throw prompt engineering at it. Prompt-Harness intercepts the loop between the environment and the LLM. If the agent repeats the same action 3 times, it dynamically injects a nudge into the *next* turn's system prompt: *"You have clicked here 3 times. Look closely at the focus indicator. If it is active, emit done."*

This operationalizes #112's avenue (C) as a state-machine library. It's free at runtime — no extra inference, no extra tools, just smarter prompting.

**Why it's separate:** Pure prompt engineering. Doesn't need helmdeck's runtime. Wraps any agent loop.

**Who would build it:** Prompt engineers, LLM researchers, framework maintainers who want a low-effort win.

### 5. OpenNativeComputerUse — open-weight native computer-use schemas

**The bet:** Anthropic and OpenAI ship *native* computer-use schemas — their models are trained to emit `computer.click(x, y)` directly. Open-weight models fall back to JSON-shaped tool calls and aren't trained on the success patterns that come with the native shape.

Two parts: (1) a translation bridge that maps Anthropic's `computer_use` schema to standard OS actions; (2) an open dataset and fine-tuning pipeline that trains LLaMA-3, Qwen, Mistral, etc. to natively output the schema *and* emit `done` when the screenshot evidence supports it.

This is the deepest project — it attacks the conservatism at the model-training level instead of the application level.

**Why it's separate:** Fine-tuning and dataset curation lives in the hands of organizations like NousResearch, Open-Orca, and the LocalLLaMA community. Helmdeck is a downstream consumer.

**Who would build it:** Open-source AI researchers, fine-tuning experts (Axolotl / Unsloth), dataset curators.

## How they relate

Each project attacks a different level of the stack:

| Layer | Project | Approach |
|---|---|---|
| Model training | **OpenNativeComputerUse** | Train the conservatism out of open-weight models |
| Tool surface | **Verify MCP server** | Make verification an explicit typed call |
| Verification engine | **OpenCVD** | Replace LLM-as-verifier with cheap CV |
| Telemetry | **UI-Trace** | Stop pretending a static PNG is ground truth |
| Loop control | **Prompt-Harness** | Detect repetition, nudge the model out of loops |

Any **one** of these would close most of the gap that #112 is about. **Two of them** in combination — say OpenCVD + Verify MCP, or UI-Trace + Prompt-Harness — would give you a meaningfully more reliable computer-use agent than anything currently shipping.

None of them require permission from us. We'd integrate with all of them happily, but the work is OSS-shaped and the value is broader than helmdeck.

## Why I'm posting this instead of starting them

We have a roadmap. v0.11.0 is pack-authoring + test-runner. v1.0 is Kubernetes. The vision-verification gap is real but not on our critical path — and frankly, the people best positioned to solve it aren't us.

I might build one of these eventually. Probably not soon. If you build one before I do, I will:

- Integrate it into helmdeck within a week of you cutting a tag.
- Send you the helmdeck users who hit this problem.
- Co-write a follow-up post comparing your approach to the others.

If you're considering this and want to talk shape before writing code, the issues below are the place to start the conversation.

## The issues

| # | Project | Issue |
|---|---|---|
| 1 | OpenCVD — open done-detector | [helmdeck#115](https://github.com/tosin2013/helmdeck/issues/115) |
| 2 | helmdeck `verify` MCP server | [helmdeck#116](https://github.com/tosin2013/helmdeck/issues/116) |
| 3 | UI-Trace — agentic telemetry | [helmdeck#117](https://github.com/tosin2013/helmdeck/issues/117) |
| 4 | Prompt-Harness — dynamic injection | [helmdeck#118](https://github.com/tosin2013/helmdeck/issues/118) |
| 5 | OpenNativeComputerUse — open-weight native CU | [helmdeck#119](https://github.com/tosin2013/helmdeck/issues/119) |

Each issue has the full proposal — target contributor community, philosophy, governance, sustainability. Pick one that fits your interests and your time.

## What we're keeping in helmdeck core

To be clear on the boundary: helmdeck core *will* keep iterating on the avenues from #112's "Avenues to research" — better system prompts, optional two-shot confirmation, vision-stronger model defaults. Those are reasonable internal experiments and we owe users *some* convergence improvement in the meantime.

But those experiments are deliberately constrained — they don't try to be the standard verification layer for the open agent ecosystem. The five projects above can.

## Pre-empting questions

**Are vision packs deprecated?** No. v0.10.0 flags `vision.click_anywhere` and `vision.fill_form_by_label` as **experimental for production**, with a recommendation to prefer `web.test` (Playwright MCP, deterministic) where possible. They still ship, the loop still runs; you just shouldn't bet a critical workflow on them yet.

**Is this just an open-source land-grab?** No. The five projects above each have distinct contributor communities, licenses, and governance models. They're not helmdeck's to claim — they're shapes that any of several existing communities (LocalLLaMA, OpenTelemetry-style spec bodies, MCP server maintainers) could naturally pick up.

**Could these all become one big project?** Probably not productively. The skill sets, communities, and decision-making cadences are different — fine-tuning datasets vs. CV models vs. observability standards vs. MCP servers vs. prompt libraries are five separate disciplines. Better five focused projects than one mega-monorepo nobody finishes.

**What if I want to fork one of these and ship it commercially?** Apache 2.0 / MIT recommendations for each. Take it.

## See also

- [Issue #112](https://github.com/tosin2013/helmdeck/issues/112) — the canonical research thread; this post is a project-shaped expansion of it
- [PR #105](https://github.com/tosin2013/helmdeck/pull/105) — the mechanical fix that closes the *loop* bug but leaves the *model* problem standing
- [vision.click_anywhere reference](/reference/packs/vision/click-anywhere) — pack docs with the experimental caveat
- [Anthropic computer-use docs](https://docs.anthropic.com/en/docs/build-with-claude/computer-use) — the upstream acknowledgment that vision-model verification is hard
- [`web.test` reference](/reference/packs/web/test) — the Playwright-MCP-backed deterministic alternative for browser goals
