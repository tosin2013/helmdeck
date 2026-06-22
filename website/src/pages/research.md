---
title: Research
description: "Helmdeck publishes empirical findings on running agentic workflows against weak open-weight models. This page is the index — every entry links to a blog post, ADR, or production change that landed the finding."
slug: /research
---

# Research

Helmdeck is, in practice, a research project that happens to ship as
production software. The novelty lives in **empirical findings about running
agentic workflows on weak open-weight models** (`gpt-oss-120b:free`, Gemma,
Mistral) — the tier where capable closed models hide most of the hard
problems. Every finding below was produced by hitting the problem in real
work, then writing it up.

The page is the index. Each entry has a one-line summary and a link to the
blog post, Architecture Decision Record, or production change where the
finding landed. The pattern: hard implementation work surfaces a finding;
the finding becomes a write-up; the write-up lands here.

License: Apache 2.0. Everything cited from this page is openly published.

---

## Empirical findings: weak open-weight models

How the open-weight tier behaves under load, and what architectural pieces
have to exist for it to be reliable.

- **The agent that learns from its own lint errors** (forthcoming, 2026-06).
  Abstract authoring rules in the system prompt don't change a Tier C
  model's behavior; concrete personal violation counts do. Findings-memory
  is the architecture that closes that loop at the prompt layer instead of
  fine-tune time. The write-up is in draft pending a second empirical
  cycle.

- **[Tier A is structurally better. The deposit-step failure is universal.](/blog/tier-a-empirical-baseline)** (2026-06-09).
  The same prompt against Claude Sonnet 4.6 and `gpt-oss-120b:free` shows
  the model class boundary clearly — but both tiers skip the mandatory
  artifact deposit step. The most damaging failures are tier-invariant.

- **[Plausibility-shaped output: when Tier C models manifest deposits they never made](/blog/plausibility-shaped-output)** (2026-06-09).
  A free model produced a confidently-formatted six-entry manifest with byte
  sizes and a policy citation, for artifacts that never existed. The
  architectural fix is verify-against-ground-truth — the audit-callback
  pattern, treated as anti-hallucination middleware.

- **[The audit-callback pattern](/blog/the-audit-callback-pattern)** (2026-06-09).
  For any pack call an LLM might transform in its text response, ship a
  paired audit pack that reads ground truth. Architectural shape that
  generalizes to any "trust the LLM's narration" failure mode.

- **[Empirical validation: the profile only gets you partway](/blog/empirical-validation-per-model-profile)** (2026-06-09).
  A profile-aware Tier C agent ran end-to-end on `gpt-oss-120b:free` with
  real artifacts and `all_present:true`. It also simplified a 9-platform
  table down to 2. The model profile library is a starting point, not a
  finished product.

- **[Free models empty-completed our 35KB tool catalog](/blog/empirical-tier-context-management)** (2026-06-01).
  Some free LLMs return empty completions when the tool catalog exceeds
  their effective working set. We classify models by observed structured-
  output reliability, not advertised context window.

---

## Architectural patterns

Patterns that survived empirical testing and got promoted into the design
vocabulary of the project.

- **[We shipped a 4-phase reliability arc. The first bug it caught was itself.](/blog/validation-arc-caught-its-own-first-bug)** (2026-06-05).
  A reliability arc that detected its own Dockerfile/runtime image mismatch
  the first time it ran production-shaped. Plus what a 120B free-tier
  model did to our planner.

- **[Render ≠ preview: what we learned shipping a hyperframes integration](/blog/child-composition-slot-lifetime)** (2026-06-17).
  A v0.29.2 pipeline produced 15 seconds of animation followed by 83
  seconds of blank canvas. The fix was upstream; the lesson was bigger.
  Upstream's own lint was telling us the whole time — so we wrapped it as
  a helmdeck pack so the next agent catches it before burning the render
  budget.

- **[Tool layer vs. sandbox layer](/blog/tool-layer-vs-sandbox-layer)** (2026-05-13).
  Most "secure agent platform" stories conflate tool execution with
  sandbox enforcement. They're different layers with different failure
  modes. Helmdeck owns the tool layer; NVIDIA OpenShell owns the sandbox
  layer; the composition is the production posture.

---

## Cost economics

- **[Why a $0.10 model can do work that needs a $3 model](/blog/cheap-models-do-frontier-work)** (2026-05-08).
  Helmdeck moves intelligence from the LLM to the pack handler. The result
  is a ~10× per-task cost reduction against naïve frontier-model function
  calling. With the prompts and recipe to reproduce.

---

## Load-bearing Architecture Decision Records

These ADRs encode the contracts the empirical findings rely on. Each is a
written-down design decision, with context and consequences, that the
codebase implements.

- **[ADR 047 — Universal Memory + per-caller scoping](/adrs/pipeline-routing-and-memory)** —
  the memory substrate the findings-memory architecture writes to.
  Per-caller JWT-scoped isolation; AES-256-GCM at rest.
- **[ADR 052 — Pipeline AV output validation as a post-step](/adrs/av-output-validation-post-step)** —
  the architectural shape findings-memory generalizes to other artifact
  classes.
- **[ADR 051 — Failure-mode-aware dispatch](/adrs/failure-mode-aware-dispatch)** —
  the routing layer that picks a model based on observed failure modes,
  not vendor specs.
- **[ADR 008 — Typed error codes for weak-model reliability](/adrs/typed-error-codes-for-weak-model-reliability)** —
  the contract that lets weak models recover from errors deterministically.

The full ADR series lives at **[/adrs](/adrs)**.

---

## Citing this work

If you cite a finding, prefer the blog post URL (durable) or the ADR URL
(durable) over PR or commit links (these rot). Suggested citation:

> Akinosho, T. *Helmdeck Research: \<finding title\>*. 2026.
> Available at: https://helmdeck.dev/research

---

## How this page is maintained

The discipline: when development on helmdeck surfaces a finding worth
publishing — a quantified cost result, an empirical pattern, a friction
story with a clean fix, an unexpected interaction between subsystems —
we draft a blog post alongside the implementation (see
[`CONTRIBUTING.md`](https://github.com/tosin2013/helmdeck/blob/main/CONTRIBUTING.md)
§"Other contribution types"). If the post is research-flavored — that is,
the finding generalizes beyond the immediate fix — we add an entry to this
page in the same change.

The pattern is load-bearing: hard implementation work in helmdeck is what
produces the research. Treating that work as research output instead of
just shipped code is how the page stays current.
