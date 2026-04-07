# 13. OpenTelemetry with GenAI Semantic Conventions

**Status**: Accepted
**Date**: 2026-04-07
**Domain**: distributed-systems

## Context
Multi-agent multi-provider workflows produce traces that span LLM calls, MCP tool invocations, browser sessions, and credential injections. Flat log lines cannot reconstruct end-to-end agent behavior. The CNCF OpenTelemetry GenAI semantic conventions (stable 2025) standardize how this data should be emitted (PRD §19.8).

## Decision
The Go control plane emits OpenTelemetry traces for every significant operation: session creation, CDP command execution, MCP tool call, LLM inference request, credential injection, and pack execution. Spans carry `gen_ai.system`, `gen_ai.request.model`, `gen_ai.usage.input_tokens`, and `gen_ai.usage.output_tokens` attributes per the GenAI semantic conventions. Traces are exported via OTLP to a configurable collector; the bundled deployment ships an OTel Collector DaemonSet (K8s) or sidecar (Compose). The Audit Logs panel is backed by a Langfuse-compatible OTLP endpoint, giving operators full distributed traces rather than flat lines.

## Consequences
**Positive:** end-to-end visibility across the full agent → gateway → browser → MCP path; native compatibility with Langfuse, Arize Phoenix, Grafana Tempo, and any OTLP backend; cost attribution from token counts.
**Negative:** trace volume can be substantial; sampling policy needed; vendor backends still maturing on GenAI conventions.

## Related PRD Sections
§19.8 OpenTelemetry GenAI, §8.8 Audit Logs Panel, §19.11 Innovation Roadmap
