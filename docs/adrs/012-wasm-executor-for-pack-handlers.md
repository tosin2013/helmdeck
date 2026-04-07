# 12. WebAssembly Executor for Custom Pack Handlers

**Status**: Proposed
**Date**: 2026-04-07
**Domain**: security, api-design

## Context
Pack handlers written as compiled Go plugins require recompiling and redeploying the control-plane binary, which contradicts the §6.7 goal of publishing a new pack in ≤10 minutes. WASM + WASI 0.2 offers microsecond cold starts, capability-based security (zero ambient authority), bounds-checked linear memory, and language agnosticism (Rust, TinyGo, Pyodide) (PRD §19.6).

## Decision
Implement a `WASMExecutor` subsystem in the Go control plane using `wasmtime-go`. Pack authors upload `.wasm` modules through the Pack Authoring UI; the platform inspects declared WASI capabilities, requires explicit operator grant for any non-default capability, and runs the module in an isolated `wasmtime` instance per invocation. Compiled Go pack handlers remain supported for built-in packs that need substrate access not exposed via the WASM ABI.

## Consequences
**Positive:** new packs ship without redeploying the control plane; capability-based isolation strictly bounds blast radius of a malicious or buggy pack; pack authors are not constrained to Go.
**Negative:** the WASM ABI must expose enough substrate (HTTP, vault, session APIs) to be useful while remaining safe; debugging WASM panics is harder than native Go; performance ceiling lower than native handlers for CPU-heavy work.

## Related PRD Sections
§6.7 Pack Authoring, §19.6 WebAssembly Sandboxing, §19.11 Innovation Roadmap
