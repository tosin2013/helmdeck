# Personalize an OpenClaw agent (layered SOUL / IDENTITY / USER / AGENTS pattern)

A walkthrough for operators who want to use the helmdeck shipped skills (and skills under `~/.openclaw/skills/`) with **their own** persona, platforms, and goals — not the defaults the skill author baked in.

The five-layer mental model maps how things SHOULD split:

| File | Layer | Owner | Reusable across models? |
|---|---|---|---|
| `SOUL.md` | Voice, tone, banned phrases, editorial principles | You | ✅ Yes |
| `IDENTITY.md` | Agent's display name, emoji, theme | You | ✅ Yes |
| `USER.md` | Who YOU are (domain, platforms, projects) | You | ✅ Yes |
| `AGENTS.md` | Operating rules, workflow shape, tool whitelist | You (often forked from a recipe) | ❌ No — tunes per model |
| `~/.openclaw/skills/<name>/SKILL.md` | Mechanism: pack vocabulary, schemas, contracts | Skill author | ❌ Skill-specific |

The persona files (SOUL / IDENTITY / USER) stay constant when you spin up multiple agents on different models. Only AGENTS.md changes per model. This is what makes one helmdeck install support many agents with the same voice across different model variants.

## Why this matters

Two failure modes when the layering breaks down:

1. **Skill-prose forks** — operator copies a skill, edits it to add their domain / platforms / projects, and now has a private fork that drifts from the upstream. Every helmdeck update requires re-merging.
2. **Identity sprawl in AGENTS.md** — operator dumps voice + persona + operating rules into one giant AGENTS.md. The file blows past the 12,000-char bootstrap cap. The agent loads truncated content and behaves unpredictably.

The fix is the layering: persona stays in the workspace (`~/.openclaw/workspace-<agent-id>/`), mechanism stays in the skill (`~/.openclaw/skills/`). Editing the workspace files never touches the skill; updating the skill never overwrites your persona.

Empirical context: today's [PR #481](https://github.com/tosin2013/helmdeck/pull/481) → [#484](https://github.com/tosin2013/helmdeck/pull/484) Nemotron baseline-vs-hardened A/B (24 calls / 0 deposit → 7 calls / deposit + verify with `all_present: true`) is one demonstration that per-use-case AGENTS.md hardening is more impactful than persona dumping. See also [`docs/integrations/openclaw.md` §5d](../integrations/openclaw.md) for the canonical file roles reference.

## What goes where

Quote from [OpenClaw's own SOUL.md guide](https://openclaw.ai/concepts/soul) sets the principle:

> Keep `AGENTS.md` for operating rules. Keep `SOUL.md` for voice, stance, and editorial posture.

Concrete examples per layer:

### SOUL.md — voice and editorial posture

- Pronouns and person ("first-person, terse, practitioner-voiced")
- Editorial discipline ("sentence case for headings", "≤2-year sources")
- Banned filler ("game-changer", "let's dive in", "in conclusion")
- Voice stance ("architect-voiced", "skeptic-toward-marketing", "casual but precise")

NOT in SOUL.md: model-specific prompting shape, tool-call rules, workflow turn structure.

### IDENTITY.md — display name and surface presence

- Agent display name (`"Maya"`, `"Press-Gemma"`, `"TraceTest"`)
- Emoji (renders in OpenClaw's agent picker UI)
- One-line theme/tagline summarizing what the agent does

NOT in IDENTITY.md: detailed persona attributes, platforms, domain expertise.

### USER.md — who YOU are

- Your name, role, geographic context
- Your domain expertise (security research, distributed systems, content marketing)
- Your audience and platforms (where you publish, who reads you)
- Your projects and current focus (recent talks, ongoing themes)
- Your editorial preferences AS A PERSON (informed by but distinct from SOUL.md's voice rules)

This is the most-customized file. Operators who skip USER.md and dump everything into IDENTITY.md create overloaded files that hit the 12K cap fast.

### AGENTS.md — operating rules

- Workflow shape (three-turn iterative? single-response? something else?)
- Tool whitelist ("you MAY call ONLY these packs")
- Handoff lines (literal, non-skippable)
- Success criteria (machine-checkable)
- Model-specific prompting hints (Gemma 4 `<|think|>` toggle, Llama 3.3 header tokens, etc.)

This is the most model-dependent file. See [`docs/howto/per-model-agents/`](per-model-agents/) for recipe examples (gemma-4 iterative workflow is the worked example shipped today).

### SKILL.md — mechanism only

Lives in `~/.openclaw/skills/<name>/SKILL.md`. Stays mechanism-only:
- Helmdeck pack vocabulary (what packs do what)
- Schemas (input/output shapes per pack)
- Error-handling contracts
- Session-chaining rules

NOT in SKILL.md: persona, operator-specific defaults, workflow shape. Those belong in the workspace files.

## Walkthrough — populating USER.md

USER.md is the file you'll customize the most. Starter template:

```markdown
# USER.md

## Who I am
- Name: <your name>
- Role: <your professional role>
- Geographic context: <city / region — informs timezone, regulatory context>

## My domain
- Primary expertise: <one or two sentences>
- Secondary areas I write about: <list>
- What I'm NOT: <areas where you're a beginner; signals where to ask vs. assume>

## My audience
- Where I publish: <list of platforms with audience size if known>
- Who reads me: <reader persona — practitioners, executives, students, peers>
- Editorial pet peeves I want the agent to enforce: <e.g., "no acronyms without first-use spell-out">

## Current focus
- Recent talks / posts: <list, with dates>
- Ongoing themes: <2-4 ideas you keep returning to>
- Active projects I publish about: <list, with one-line descriptions>

## Editorial preferences
- Tone: <e.g., "practitioner-first, skeptical of vendor marketing">
- Length defaults: <e.g., "800-1300 words for technical-deep-dive">
- Things I avoid: <e.g., "no 'in conclusion'", "no listicle clickbait">
```

Cap is 12,000 characters (bootstrap injection limit). If you're going long, prune ruthlessly — the agent reads everything at every session start.

## Walkthrough — tuning IDENTITY.md

Most operators don't need to tune IDENTITY.md much beyond the basics. Override defaults when:

- Your platforms list differs from the shipped example
- You want a specific display emoji or name pattern for multi-agent setups
- The default theme doesn't capture what you actually do

Template:

```markdown
# IDENTITY.md

- name: <agent display name>
- emoji: <single emoji>
- theme: <one-line summary — what this agent does, in your voice>
```

## Walkthrough — tuning SOUL.md

Generally **don't customize SOUL.md**. The defaults shipped by skill authors capture broadly-applicable editorial principles (sentence case, banned marketing jargon, voice stance). Override only when:

- Your voice is sharply different from "architect-voiced practitioner" (e.g., you're a comedian writing about engineering)
- Your editorial discipline differs in concrete ways (e.g., you allow listicle format your skill's default forbids)
- You have explicit banned-phrase additions (e.g., domain-specific weasel words)

When you do override, mirror the structure OpenClaw's [SOUL.md guide](https://openclaw.ai/concepts/soul) prescribes — voice rules, banned phrases, editorial discipline. Don't introduce operating rules; those belong in AGENTS.md.

## Tradeoffs — when to fork the skill vs customize via identity files

| Situation | Better path |
|---|---|
| Operator has different platforms, domain, audience | **Customize via USER.md** — keep upstream skill intact |
| Operator wants different workflow shape (e.g., two turns instead of three) | **Customize via AGENTS.md** — keep upstream skill intact |
| Operator wants different tool whitelist (e.g., no filesystem packs) | **Customize via AGENTS.md** — see [PR #484's nemotron hardening](https://github.com/tosin2013/helmdeck/pull/484) for the empirical case |
| Operator wants different sampling defaults per model | **Per-agent model config in `openclaw.json`** — not a workspace file change |
| Operator wants fundamentally different packs (not helmdeck packs at all) | **Fork the skill** — different mechanism, different SKILL.md |
| Operator wants to add a new pack to an existing skill | **Wait for upstream support OR contribute back** — fork only as last resort |

The "fork the skill" path is rare and should be a deliberate choice, not an accident from overloading workspace files.

## Worked example — Maya, a security researcher

Maya is the sanitized persona used in [`docs/howto/per-model-agents/gemma-4-iterative-workflow.md`](per-model-agents/gemma-4-iterative-workflow.md) and [`docs/integrations/openclaw.md` §5d](../integrations/openclaw.md). She publishes on Substack, Phrack, and Black Hat / DEF CON archives.

### Maya's `SOUL.md`

```markdown
# SOUL.md

## Voice
- First-person, terse, practitioner-voiced
- Skeptical of vendor marketing
- Casual but precise — favor "I tested this on..." over "best practices suggest..."

## Editorial discipline
- Sentence case for all headings
- Subheading every 2-4 paragraphs
- Sources cited ≤2 years old; explain why older sources are still authoritative
- Acronyms spelled out on first use ("Berkeley Packet Filter (BPF)" then "BPF")
- One sentence, one idea

## Banned filler
- "game-changer", "10x", "transformative", "leverage as verb", "synergy"
- "Great question," "let's dive in," "in conclusion"

## Voice stance
- Practitioner who instruments her own VMs before trusting vendor claims
- Open about uncertainty ("I haven't tested this against IPv6 yet" beats false confidence)
```

### Maya's `IDENTITY.md`

```markdown
# IDENTITY.md

- name: Maya
- emoji: 🔍
- theme: Security researcher who instruments before she writes. Casual + technical.
```

### Maya's `USER.md`

```markdown
# USER.md

## Who I am
- Name: Maya (use first name only in drafts)
- Role: Independent security researcher
- Geographic context: US-Eastern timezone

## My domain
- Primary expertise: Linux kernel security, eBPF observability, rootkit detection
- Secondary areas I write about: Container security, Kubernetes admission control
- What I'm NOT: A web app security person (different threat model; ask before assuming)

## My audience
- Where I publish:
  - Personal Substack (4,500 subscribers — defenders, blue team, kernel ops folks)
  - Phrack archive submissions (peer-reviewed, deep-technical)
  - DEF CON / Black Hat conference talks → blog posts
- Who reads me: Practitioners maintaining production Linux fleets
- Editorial pet peeves: No buzzwords, no vendor hype, always include reproducible commands

## Current focus
- Recent: Phrack 71-12 ftrace bypass technique (Q1 2026)
- Ongoing: eBPF tracepoint observability for kernel module rootkits
- Active project: Open-source rootkit-detector based on tracepoint signatures

## Editorial preferences
- Tone: Practitioner-first, skeptical of vendor marketing
- Length defaults: 1,000-1,500 words for technical-deep-dive
- Things I avoid: Marketing recap intros; "executives need to understand" framing
- Things I always include: Reproducible commands, test VM setup steps, false-positive caveats
```

### Maya's `AGENTS.md`

For Maya's Gemma 4 variant, the AGENTS.md follows the [gemma-4 iterative workflow recipe](per-model-agents/gemma-4-iterative-workflow.md) — three-turn workflow (outline → draft + ground → deposit + verify), Gemma 4 role-turn-conversational format, `<|think|>` toggle, multimodal ordering rules.

For Maya's Llama 3.3 variant, the AGENTS.md follows the same shape but adapted to Llama's `role_header_chatml` format (no thinking-mode knob, scaffold reasoning via numbered steps).

The persona files (SOUL / IDENTITY / USER) are **identical across both variants** — only AGENTS.md changes per model. That's the layering payoff: one persona, many models.

### Multi-variant Maya in `openclaw.json`

```json
{
  "agents": {
    "list": [
      {
        "id": "maya-gemma-4",
        "workspace": "/home/node/.openclaw/workspace-maya-gemma-4",
        "model": "openrouter/google/gemma-4-26b-a4b-it:free",
        "identity": { "name": "Maya", "emoji": "🔍", "theme": "..." }
      },
      {
        "id": "maya-llama",
        "workspace": "/home/node/.openclaw/workspace-maya-llama",
        "model": "openrouter/meta-llama/llama-3.3-70b-instruct:free",
        "identity": { "name": "Maya", "emoji": "🔍", "theme": "..." }
      }
    ]
  }
}
```

Each workspace directory holds Maya's SOUL/IDENTITY/USER (the same three files copied across) plus a model-specific AGENTS.md.

## Verify your setup

After bootstrapping a personalized agent:

1. **Trigger a session** in the OpenClaw UI — confirm the agent picker shows your name + emoji + theme.
2. **Send a representative prompt** (the `BLOG DRAFT` trigger from the per-model recipes is a good test).
3. **Walk the three-turn workflow** to confirm AGENTS.md fired correctly.
4. **Run [`helmdeck-trace`](../../scripts/helmdeck-trace/README.md)** against the resulting session jsonl to confirm the audit-callback pattern fired:
   ```bash
   ./scripts/helmdeck-trace/helmdeck-trace summary \
     --session ~/.openclaw/agents/<your-agent-id>/sessions/<session-id>.jsonl
   ```
   Look for `verify_manifest_called: True`, `all_present: True`, and a tool tally that matches the workflow your AGENTS.md prescribed.

If metrics deviate from what AGENTS.md prescribed, the workspace layering may have leaked something into the wrong file — most commonly persona content into AGENTS.md inflating it past the 12K cap.

## Bootstrap helper

The [`configure-openclaw.sh`](../../scripts/configure-openclaw.sh) script handles the initial OpenClaw setup (gateway, JWT, MCP wiring, skill installation). It doesn't currently seed canonical workspace files — that's tracked in [issue #454](https://github.com/tosin2013/helmdeck/issues/454). Until that lands, copy the template scaffolds from this guide into your new agent's workspace directory.

For ongoing maintenance, the workspace files live at `~/.openclaw/workspace-<agent-id>/` (read by OpenClaw at every session start). Edit them directly with your text editor — no rebuild step required.

## Related

- [`docs/integrations/openclaw.md` §5d Canonical file roles](../integrations/openclaw.md) — the conceptual map this howto builds on
- [`docs/howto/per-model-agents/gemma-4-iterative-workflow.md`](per-model-agents/gemma-4-iterative-workflow.md) — worked example of an AGENTS.md tuned to one specific model
- [`scripts/helmdeck-trace/README.md`](../../scripts/helmdeck-trace/README.md) — CLI for validating your personalized agent's behavior empirically
- [PR #481](https://github.com/tosin2013/helmdeck/pull/481) + [#484](https://github.com/tosin2013/helmdeck/pull/484) — empirical proof per-use-case AGENTS.md hardening matters more than profile guidance alone
- [Issue #482](https://github.com/tosin2013/helmdeck/issues/482) — HuggingFace community models track (alternate routing layer)
- OpenClaw [SOUL.md guide](https://openclaw.ai/concepts/soul) and [agent-workspace docs](https://openclaw.ai/concepts/agent-workspace) — canonical upstream reference
