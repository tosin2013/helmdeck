# NemoClaw

> **Status:** 🟡 Documented (inherits OpenClaw schema inside the NVIDIA sandbox)
> NemoClaw is NVIDIA's sandbox around OpenClaw and reuses OpenClaw's MCP schema and CLI verbatim inside the sandbox boundary. Promote to ✅ once a maintainer has walked the Phase 5.5 loop *inside* a NemoClaw sandbox specifically (the OpenClaw walkthrough does not transfer automatically — sandbox networking and credential isolation differ).

## Topology

NemoClaw is **Topology A with sandbox boundary** — the agent runs in NVIDIA's NeMo sandbox, which wraps OpenClaw and adds its own credential / network isolation layer. From helmdeck's perspective, the wiring is the same as the [OpenClaw sidecar pattern](openclaw.md), but the sandbox imposes two extra constraints:

1. **Network**: the sandbox may not give the OpenClaw process inside it access to the host's docker bridge by default. You may need a NemoClaw-specific network passthrough flag (consult NVIDIA's NemoClaw docs — this section will gain a concrete recipe once a maintainer has run it).
2. **Credentials**: the helmdeck JWT and any LLM provider key must be passed in via NemoClaw's secret-injection mechanism, not via plain env vars in the OpenClaw container.

NemoClaw is alpha at the time of helmdeck v0.6.0 and the configuration surface may shift. Treat this page as a pointer; the authoritative schema for the inner OpenClaw config is still [`openclaw.md`](openclaw.md).

## What inherits from OpenClaw

- `~/.openclaw/openclaw.json` schema, including the `agents.list[].mcp.servers[]` section
- `openclaw mcp` CLI commands
- The two MCP transport options: stdio `command` and URL-based `url`

## What does NOT inherit

- The `./scripts/docker/setup.sh` flow — NemoClaw has its own bootstrap
- The host networking model — the sandbox may require explicit egress rules
- The credential storage path — secrets live in NVIDIA's sandbox vault, not on the host filesystem

## Prerequisites

- An NVIDIA GPU host with the NeMo sandbox installed
- NVIDIA NemoClaw / NeMo Agent CLI access
- A running helmdeck stack on the same host (or reachable from the sandbox via configured egress)

## Walkthrough

Until a maintainer has run NemoClaw end-to-end, follow these steps as scaffolding:

1. Install helmdeck on the host: `git clone … && ./scripts/install.sh`
2. Install NemoClaw per NVIDIA's instructions (URL TBD — see <https://github.com/NVIDIA> or NVIDIA developer docs for the current path).
3. Inside the NemoClaw sandbox, create or edit the inner `openclaw.json` to add the helmdeck MCP server entry — copy the JSON shape from [`openclaw.md` §4a](openclaw.md#4a-edit-openclawopenclawjson-directly).
4. Pass the helmdeck JWT in via NemoClaw's secret-injection mechanism (NOT a plain env var).
5. From inside the sandbox, verify the helmdeck control plane is reachable: `curl http://<host-or-bridge>:3000/healthz`.
6. Walk the Phase 5.5 loop as documented in [`openclaw.md` §6](openclaw.md#6-walk-the-phase-55-code-edit-loop).

When the walkthrough lands, replace this section with the concrete NemoClaw-specific recipe and flip the status banner ✅.

## Why NemoClaw is intentionally not a separate `connect.go` target

`/api/v1/connect/openclaw` returns the OpenClaw config shape. NemoClaw consumes that exact same shape inside its sandbox — there is no NemoClaw-specific JSON to generate, only sandbox-specific network and credential plumbing that lives outside helmdeck's connect endpoint. This is a deliberate non-decision: keeping a separate target would imply a schema divergence that doesn't exist.

## References

- [OpenClaw MCP schema](openclaw.md) (canonical)
- NVIDIA NeMo / NemoClaw docs: search <https://github.com/NVIDIA> and <https://docs.nvidia.com>
