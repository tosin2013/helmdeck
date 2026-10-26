# OWASP Agent Memory Guard Integration

This document describes how to integrate OWASP Agent Memory Guard (AMG) with helmdeck's memory layer to protect against memory poisoning attacks.

## Overview

[OWASP Agent Memory Guard](https://owasp.org/www-project-agent-memory-guard/) is an OWASP Foundation Incubator project that provides runtime defense against AI agent memory poisoning, tool abuse, privilege escalation, and excessive autonomy attacks.

This integration implements the `MemoryMiddleware` seam described in [issue #268](https://github.com/tosin2013/helmdeck/issues/268) and ADR 039. It validates every memory write through AMG's threat detection before the entry hits storage.

## How It Works

1. **Memory Write**: A pack calls `ec.Memory.Store(key, value, ...)`
2. **Validation**: Before storage, the `OWASPMemoryMiddleware` sends the entry to AMG's `/scan` endpoint
3. **Verdict**: AMG returns `action: "allow"` (clean) or `action: "block"` (poisoned)
4. **Action**: Blocked writes fail with an error; allowed writes proceed to storage

The integration is **opt-in and default-off** — when no middleware is configured, helmdeck behaves exactly as before.

## Setup

### 1. Start the AMG Server

Install AMG and start its REST API server:

```bash
# Install AMG (requires Python 3.9+)
pip install agent-memory-guard[server]

# Start the server (defaults to http://127.0.0.1:8000)
amg serve --policy strict
```

The server provides these policies:
- `strict`: Blocks all detected threats (recommended)
- `tiered`: Different thresholds per source class
- `permissive`: Logs threats but doesn't block

See the [AMG documentation](https://owasp.org/www-project-agent-memory-guard/) for policy details.

### 2. Configure helmdeck

Set the AMG server URL via environment variable:

```bash
export HELMDECK_AMG_URL=http://localhost:8000
```

When `HELMDECK_AMG_URL` is set, helmdeck wraps its memory store with the OWASP validation middleware. When unset, no validation runs.

### 3. Verify

Run a pack that uses memory (e.g., `swe.solve`, `github.list_issues`). Check the AMG server logs:

```bash
# In the AMG terminal, you'll see:
# INFO:     127.0.0.1:52318 - "POST /scan HTTP/1.1" 200 OK
```

## Source Class Mapping

helmdeck's memory categories map to AMG's source classes:

| helmdeck category | AMG source_class |
|-------------------|------------------|
| `cache`           | `agent_authored` |
| `solve`           | `agent_authored` |
| `repo`            | `git.commit_message` |
| *(default)*       | `agent_authored` |

AMG applies stricter validation to `external_tool` and `user_input` than to `agent_authored`. Future helmdeck PRs can add `WithSourceClass()` option to `Put()` for finer-grained policy.

## What Gets Validated

The middleware validates:
- **All `ec.Memory.Store()` calls** from pack handlers
- **Memory cache writes** (the ADR 039 cache seam)

It does NOT validate:
- **Reads** (`ec.Memory.Recall()`) — AMG's read-side validation is future work
- **Artifacts** — the artifact store is separate from memory
- **Vault secrets** — the vault layer has its own access controls

## Threat Classes Detected

AMG detects these threat types (non-exhaustive):

- **Prompt injection** (`injection` detector)
- **Data leakage** (`leakage` detector)
- **Cross-task contamination** (`cross_task` detector)
- **Memory persistence injection** (`memory_persistence_injection` detector)
- **Self-reinforcement** (`self_reinforcement` detector)
- **Excessive autonomy** (`excessive_autonomy` detector)

See [AMG's detector catalog](https://owasp.org/www-project-agent-memory-guard/detectors/) for the full list.

## Testing

The integration ships with tests that mock AMG responses:

```bash
# Run OWASP middleware tests
go test ./internal/memory -run TestOWASP -v

# Full memory package tests
go test ./internal/memory -v
```

For integration tests with a live AMG server:

```bash
# Terminal 1: start AMG
amg serve --policy strict

# Terminal 2: run helmdeck tests with AMG enabled
export HELMDECK_AMG_URL=http://localhost:8000
make test
```

## Performance

The validation adds **~10-50ms per write** (one HTTP round-trip to the AMG server). This is acceptable for:
- **Memory cache writes** (infrequent, TTL-bound)
- **`swe.solve` context recall** (once per solve task)
- **Audit writes** (once per pack execution)

For high-throughput scenarios, consider:
- **Batching** writes where possible
- **Local AMG deployment** (loopback, no network latency)
- **Async validation** (write immediately, validate in background) — not yet implemented

## Failure Modes

### AMG Server Down

When AMG is unreachable, the middleware **fails closed**: writes are rejected with `action: "block"` and `threat_type: "scan_error"`.

This is safer than failing open (allowing unvalidated writes), but it means helmdeck cannot write to memory if AMG is down. To tolerate AMG downtime, either:
- Deploy AMG with high availability, or
- Unset `HELMDECK_AMG_URL` to disable validation temporarily

### AMG Policy Too Strict

If AMG blocks legitimate content, you have these options:

1. **Switch policy**: `amg serve --policy permissive` logs threats without blocking
2. **Tune detectors**: AMG's `Policy.tiered()` accepts per-detector thresholds (see AMG docs)
3. **File a false positive**: Report it at https://github.com/OWASP/www-project-agent-memory-guard/issues

## Future Work

- **Read-side validation**: Block poisoned entries on `ec.Memory.Recall()`
- **Redaction**: When `action: "redact"`, mask the threat pattern before storage
- **Quarantine**: Move blocked entries to a quarantine store for forensics
- **Per-pack policy**: Let packs declare their own validation strictness
- **Batch validation**: One HTTP call for multiple writes

See [issue #268](https://github.com/tosin2013/helmdeck/issues/268) for the roadmap.

## References

- [OWASP Agent Memory Guard](https://owasp.org/www-project-agent-memory-guard/)
- [helmdeck issue #268](https://github.com/tosin2013/helmdeck/issues/268)
- [ADR 039: Universal Memory Delivery Layer](../adrs/039-universal-memory-delivery-layer.md)
- [AMG GitHub](https://github.com/OWASP/www-project-agent-memory-guard)
- [OWASP Top 10 for LLM Applications: ASI06 (Memory Poisoning)](https://owasp.org/www-project-top-10-for-large-language-model-applications/)
