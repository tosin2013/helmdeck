---
description: "ADR-027: Dual-Mode Action API: Structured (CDP) and Vision — Proposed. Architectural decision record for the helmdeck control-plane."
---

# 27. Dual-Mode Action API: Structured (CDP) and Vision

**Status**: Proposed
**Date**: 2026-04-07
**Domain**: api-design

## Context
The dominant browser-automation paradigm is shifting from DOM/accessibility-tree control to vision-based screenshot control, driven by frontier multimodal models (Claude computer use, OpenAI Operator, AI2's open visual web agent). Vision works on any UI — including legacy desktop apps, DOM-obfuscated SPAs, and Canvas/WebGL surfaces — but costs more tokens and is less precise on dense UIs. Structured CDP control is fast, cheap, and precise but breaks on anti-bot DOM obfuscation. Neither approach dominates the other; the right choice is workload-dependent (PRD §19.4).

## Decision
Expose two parallel action surfaces and let pack authors (and substrate users) choose per-call.

**Structured mode (default):** existing CDP endpoints under `/api/v1/browser/*` — `navigate`, `extract`, `click`, `interact`, `execute`. Fast, deterministic, low token cost. This is what most packs will use.

**Vision mode:** new endpoint `POST /api/v1/sessions/{id}/vision/act` accepting `{ instruction: string, model?: string, max_steps?: integer }`. The handler captures a screenshot, sends `{screenshot, instruction}` to a configurable vision model via the existing AI gateway (ADR 005), parses the model's action plan (click coords, type text, scroll, key), executes via xdotool / CDP, and loops until the model reports completion or `max_steps` is exhausted. Returns a structured trace of every action plus the final screenshot.

The Session Explorer panel (§8.3) shows the active mode per session and a toggle. Packs declare a default mode in their authoring metadata; per-call override is allowed.

A small set of "vision-native" packs ship out of the box: `vision.click_anywhere`, `vision.extract_visible_text`, `vision.fill_form_by_label` — each demonstrating the pattern and providing baseline reliability for legacy/canvas UIs.

## Consequences
**Positive:** packs can target the right tool for the job (e.g. `web.scrape_spa` stays structured for speed, but a new `desktop.legacy_app_action` pack uses vision); operators absorb future vision-model improvements automatically via the AI gateway routing layer.
**Negative:** vision mode is expensive — needs cost guardrails (per-token budgets per agent); vision models drift behavior across versions, so vision packs need pinned model versions or success-rate monitoring (Model Success Rates tab, §8.6).

## Related PRD Sections
§19.4 Vision-Based Computer Use, §6.2 Custom Golang Functions, §13 Desktop Actions, §6.6 Capability Packs

---

## §2026-04-19 revision — operator-visibility as a third axis

### Context

On 2026-04-18 the x11vnc fix (`4a8bcf5`) made the session sidecar's noVNC actually work; the operator could now watch the XFCE4 desktop via an SSH tunnel. Driving an OpenClaw-connected agent against that setup surfaced a gap the original ADR didn't anticipate: when the operator asked "go to a website and search for something," the agent consistently reached for `browser.interact` (headless CDP), so the operator saw **nothing** on the noVNC viewer. The agent was technically correct — `browser.interact` does what its description says — but the operator's actual product requirement ("I want to watch the agent work") was not expressible in the 2026-04-07 decision framing.

### Research findings (2026-04-19)

External survey of 7 systems (Anthropic `computer_20251124`, OpenAI `computer-use-preview`, Gemini computer-use, browser-use, Skyvern, Agent-S, OpenAdapt) surfaced three paradigms for the "agent on a desktop" case:

- **(a) Empty desktop + sibling shell tool** — Anthropic, Agent-S, OpenAdapt. Primitives are `screenshot/click/type/key`; apps launched via a parallel `bash` tool. Pure-pixel world.
- **(b) First-class launch primitive** — Gemini only. `open_web_browser`, `navigate`, `search` are tool-action verbs.
- **(c) Pre-launched world** — OpenAI, browser-use, Skyvern. A browser is a precondition; the agent never has to open one.

Helmdeck sits in **(c)** at the runtime level (Chromium auto-launches in desktop-mode sessions) while the MCP tool catalog advertises paradigm **(a)** primitives. The agent reads the catalog, infers paradigm (a) with a `launch` primitive that no leading system has, and rationally picks the one obviously browser-named tool (`browser.interact`) when asked to browse. It never sees the Chromium that's already running.

**Single biggest research insight**: OpenAI passes `environment: browser|ubuntu|mac|windows` as schema metadata in their computer-use tool spec; Gemini carries similar hints. **Environment label at the schema layer — not the system prompt — conditions model behavior.** This is the cheapest lever we weren't using.

### Decision

Keep the dual-mode architecture from the 2026-04-07 decision. Add a **third axis** to the decision surface: **operator-visibility**. A pack now declares (implicitly, via SessionSpec + its `Description` field) three dimensions:

1. **Action style** — structured (CDP / selectors) vs. vision (pixels). The original ADR 027 axis.
2. **Execution substrate** — our own chromedp vs. a sidecar like Playwright MCP or Firecrawl. The ADR 035 axis.
3. **Operator-visibility** — headless (invisible to noVNC) vs. visible (runs on the Xvfb display). **New.**

`browser.interact`, `browser.screenshot_url`, `web.scrape*`, `web.test` are all **headless** — optimal for automated workloads. `vision.*` packs and the 16 `desktop.*` REST primitives are all **visible** — optimal for watchable workloads, accessibility-limited UIs, and tasks where the operator's role is supervisory.

Pack descriptions exposed over MCP now explicitly state which surface they run on ("HEADLESS Chromium", "VISIBLE XFCE4 desktop", "operator watches via noVNC"). SKILL.md adds a "Driving the visible desktop" section with a decision table keyed on the user's framing ("the user wants to watch" → visible; "scrape data" → headless). This is the Tier 1 fix — no code-logic change, just honest schema metadata.

### What this ADR commits helmdeck to

- **Paradigm (a) + launch hybrid**, not (b) or (c). The agent picks mouse/keyboard primitives by default but can spawn extra apps via `desktop.run_app_and_screenshot` and `/api/v1/desktop/launch`. Chromium is pre-launched as a convenience (operators watching an empty XFCE4 desktop for 10 seconds while Chromium cold-starts is bad UX); the agent is told it's already there.
- **Pixel-space coordinates**, not Gemini's normalized 0-1000. Helmdeck's Xvfb display is fixed 1920×1080; the resolution-independence that normalized coords buy doesn't pay off in a fixed environment.
- **One-action-per-call MCP streaming**, not Gemini-style parallel batching. Action batching is 3-10× faster for the agent but hides intermediate state from an operator watching via noVNC — which is exactly the product we're opting into with visible mode. Operators seeing cursor paths + intermediate screens is the feature.

### Deferred to future ADRs (if data demands it)

- **Grounding-inside-click primitive** — promote `vision.click_anywhere` from a pack to a first-class primitive (Gemini `click_at(label)`). The three leading OS agents all fuse grounding with the click action. Worth considering if we observe the current pack-level shape costing measurable round trips.
- **Structured desktop-tree observation** — window titles + focus + panel state as JSON alongside screenshots. No surveyed system does this for the desktop (only for the browser DOM). Genuine greenfield; revisit if there's evidence of agent drift that purer vision can't fix.
- **Dropping `launch` from the catalog** — no surveyed system has it, and it may be actively confusing the agent's paradigm inference. Gated on measurement.

### Consequences

**Positive:** agents select the right surface based on what they read in the tool catalog, not on pattern-matching pack names. Operator-watchability becomes a first-class product dimension instead of an accident. When someone asks "go search for X so I can see it," the agent reaches for `vision.*` + `desktop.*` by design, not against the grain of its schema metadata.

**Negative:** pack descriptions are longer — the agent spends more tokens reading the catalog on tool discovery. Trade-off accepted because the cost is once per session and the alternative (wrong pack choice) costs per-action.

### Verification

Tier 1 success criteria: on the failing prompt from 2026-04-18 ("go to a website and search so I can watch"), the agent calls `vision.click_anywhere` or the `desktop.*` REST primitives and the operator sees cursor + keyboard activity in noVNC. Failure criteria: agent still picks `browser.interact` — escalate to Tier 2 (rename primitives so the names themselves signal visibility) or Tier 3 (drop `launch`, commit to paradigm (a) pure).

### Related artifacts

- Research summary: `/root/.claude/plans/silly-kindling-hedgehog.md` "Key findings from the research" section
- Related ADRs: **ADR 035** §2026 T807f (native computer-use tool routing — separate axis, this revision is about which surface to route to)
- Pack-catalog visibility: `skills/helmdeck/SKILL.md` + `docs/integrations/SKILLS.md` new "Driving the visible desktop" section
- 16 REST primitives: `internal/api/desktop.go`
