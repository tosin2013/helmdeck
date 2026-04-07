# 26. A2A Agent Card Endpoint

**Status**: Proposed
**Date**: 2026-04-07
**Domain**: api-design, distributed-systems

## Context
Google's Agent2Agent (A2A) protocol — merged with IBM's ACP under the Linux Foundation in August 2025 and now backed by 150+ organizations — is the dominant open standard for agent-to-agent delegation. Where MCP defines how an agent calls a tool, A2A defines how one agent hands a task to another. Helmdeck is functionally a specialist agent (the "browser/desktop expert") that orchestration agents should be able to discover and delegate to without custom SDK code (PRD §19.1).

## Decision
Expose an A2A-compatible **Agent Card** at `GET /.well-known/agent.json` and an A2A task endpoint at `POST /a2a/v1/tasks`. The Agent Card advertises:
- Identity (`name: "helmdeck"`, version, owner contact).
- Skills derived **automatically from the installed pack catalog** — each Capability Pack becomes one A2A skill with its input/output JSON Schema mapped from the pack schemas (ADR 024).
- Supported modalities (`text`, `image`, `application/pdf`) and streaming via SSE.
- Authentication scheme (Bearer JWT, same tokens as the REST API and MCP bridge).

The task endpoint maps `tasks/send` and `tasks/sendSubscribe` onto pack invocation, returning A2A `Artifacts` for pack outputs. Long-running packs (`slides.video`) stream progress events over SSE.

The Go control plane implements this using the `pion`-style `net/http` + `io.Pipe` SSE pattern that already underlies the OpenAI gateway (ADR 005).

## Consequences
**Positive:** orchestration agents (CrewAI, AutoGen, LangGraph supervisors, OpenClaw Pi) discover helmdeck via standard `.well-known` lookup and delegate tasks without bespoke integration; the pack catalog becomes self-describing across both MCP and A2A surfaces simultaneously; long-running packs get a native streaming idiom.
**Negative:** A2A spec is still evolving — must track LF AI & Data releases; auto-generated skill cards from pack schemas need schema-quality enforcement, since malformed pack schemas now leak into a public discovery surface.

## Related PRD Sections
§19.1 A2A and ACP Protocols, §13 Agent Consumer Ecosystem, §6.6 Capability Packs
