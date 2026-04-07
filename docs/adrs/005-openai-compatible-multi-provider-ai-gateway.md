# 5. OpenAI-Compatible Multi-Provider AI Gateway

**Status**: Accepted
**Date**: 2026-04-07
**Domain**: api-design

## Context
Operators need to route across Anthropic, Google, OpenAI, Ollama, and Deepseek with cost/fallback rules, encrypted key storage, and zero rewrites of agent code. The de facto standard for LLM clients is the OpenAI Chat Completions schema (PRD §6.4, §7.2).

## Decision
Expose `/v1/chat/completions` and `/v1/models` as an OpenAI-compatible facade. Routing is keyed off the `model` field using `provider/model` syntax (e.g. `anthropic/claude-opus-4`). Provider keys are AES-256 encrypted at rest, injected into outbound calls only at request time, and never returned in full via the API after initial entry. Fallback chains and cost-based routing are configurable from the Management UI. **OpenClaw is explicitly not a provider** — it is an agent framework that consumes this gateway as a tool (see ADR 012).

## Consequences
**Positive:** any OpenAI-SDK client works unchanged; a single audit trail covers all LLM traffic; key rotation is a UI operation.
**Negative:** non-OpenAI features (Anthropic tool use, Gemini grounding) require schema mapping; gateway becomes a critical-path dependency.

## Related PRD Sections
§6.4 Multi-Provider AI Endpoint Configuration, §7.2 AI Gateway Endpoints, §8.4 AI Providers Panel, §10 Security Model
