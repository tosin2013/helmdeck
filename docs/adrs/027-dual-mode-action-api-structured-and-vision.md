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
