# 6. MCP Server Registry with stdio / SSE / WebSocket Transports

**Status**: Accepted
**Date**: 2026-04-07
**Domain**: api-design, distributed-systems

## Context
Agents need access to a curated catalog of external tools (GitHub, filesystems, internal databases) via the Model Context Protocol. MCP servers ship in three transport flavors and arbitrary trust levels; the platform must register, test, and govern them centrally (PRD §6.5, §8.5).

## Decision
Implement an MCP Registry as a first-class subsystem with CRUD APIs and a UI panel. Support all three transports: `stdio` (local subprocess), `SSE` (remote HTTP), and `WebSocket` (remote persistent). Registration is a multi-step flow that fetches and displays the tool manifest before persisting. Secrets (`env` vars, auth tokens) are encrypted at rest. The platform itself ships a built-in MCP server that exposes every installed Capability Pack as a typed MCP tool, giving agent frameworks zero-code access (see ADR 003).

## Consequences
**Positive:** new tools are added by operators in <60 s; all MCP traffic is auditable; agent frameworks integrate via a single MCP-server registration.
**Negative:** stdio servers must be supply-chain reviewed manually; the registry becomes a high-value target for misconfiguration.

## Related PRD Sections
§6.5 MCP Server Management, §8.5 MCP Registry Panel, §11.3 MCPServer data model, §13 Agent Consumer Ecosystem
