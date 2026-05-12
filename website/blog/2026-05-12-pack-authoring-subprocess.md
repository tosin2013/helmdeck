---
slug: pack-authoring-subprocess
title: "Pack authoring without Go: subprocess packs in v0.12.0"
authors: [tosin]
tags: [field-report, pack-authoring, community]
description: T811 MVP turns any executable into a helmdeck pack via the stdin-JSON / stdout-JSON protocol. Drop a Python script into $HELMDECK_COMMAND_PACKS_DIR and the control plane registers it as cmd.<name>. Honest about the MVP's cuts — manifests, hot-reload, and egress sandboxing slip to v0.13.0.
date: 2026-05-12
---

## The friction

Through v0.11.0, writing a new helmdeck pack meant writing Go. Specifically:

1. Fork the repo
2. `internal/packs/builtin/your_pack.go` with a `HandlerFunc` returning `json.RawMessage`
3. `internal/packs/builtin/your_pack_test.go` with table-driven tests
4. Register in `cmd/control-plane/main.go`
5. Rebuild the control-plane binary, redeploy

For maintainers, that's fine. For a community contributor whose stack is Python/Node/Rust, the Go-toolchain dependency is a barrier — even when the pack itself is "wrap this REST API in a typed schema."

T811 closes the gap, MVP-style.

<!-- truncate -->

## The protocol

A subprocess pack is just an executable that follows one rule:

```text
stdin    = the pack input JSON (validated against InputSchema)
stdout   = the pack output JSON (validated against OutputSchema)
stderr   = surfaced verbatim on non-zero exit; ignored on success
exit 0   = success
exit ≠0  = handler_failed; engine surfaces stderr (truncated)
```

That's it. A trivial Python uppercase pack:

```python
#!/usr/bin/env python3
# Drop into $HELMDECK_COMMAND_PACKS_DIR/upper
import json, sys
body = json.loads(sys.stdin.read())
json.dump({"text": body["text"].upper()}, sys.stdout)
```

```bash
chmod +x ~/.helmdeck/command-packs/upper
docker compose restart control-plane
# Pack now registered as cmd.upper. Try it:
curl -X POST http://localhost:3000/api/v1/packs/cmd.upper \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"text":"hello world"}'
# → {"pack":"cmd.upper","output":{"text":"HELLO WORLD"},"duration_ms":...}
```

Same registry, same audit log, same MCP tool exposure as a built-in Go pack. The MCP server's `tools/list` will surface `cmd.upper` alongside `image.generate`, and the Pack Test Runner UI (also new in v0.12.0) can exercise it from the browser.

## What this MVP does NOT do

Three things deliberately deferred to v0.13.0:

### 1. Manifest format ([#173](https://github.com/tosin2013/helmdeck/issues/173))

Today's subprocess packs use **passthrough schemas** — `BasicSchema{}` accepts any JSON, returns any JSON. Schema enforcement is the subprocess's responsibility.

v0.13.0 will add a sidecar YAML manifest:

```yaml
# ~/.helmdeck/command-packs/upper.helmdeck-pack.yaml
name: cmd.upper
version: v1
description: Uppercase a string.

input_schema:
  required: [text]
  properties:
    text: string

output_schema:
  required: [text]
  properties:
    text: string

timeout_s: 30
```

Until then, an agent calling a malformed subprocess pack gets an `invalid_output` error from the engine's empty `BasicSchema` — useful but generic.

### 2. Egress sandbox ([#174](https://github.com/tosin2013/helmdeck/issues/174))

Subprocess packs run with **whatever network access the host gives them**. helmdeck's `EgressGuard` intercepts in-process HTTP calls in Go pack handlers, but it can't wrap `exec.Command` to an arbitrary binary.

For Go packs, the egress story is solved: every outbound HTTP goes through `security.EgressGuard.CheckURL` before `http.Client.Do`. For subprocess packs, you're trusting the binary.

The Go-pack `EgressGuard` remains the recommended path for any pack that needs confined HTTP. Subprocess packs are for use cases where Go isn't a fit — existing CLI tools (`pandoc`, `ffmpeg`, `imagemagick`), language-specific ecosystems (Python data tools, Node API clients), or quick prototypes.

If your subprocess pack makes HTTP calls, today the right pattern is:

- **Run helmdeck inside a network namespace** with an outbound allowlist (Linux + CAP_NET_ADMIN). Heavy but airtight.
- **Or trust the subprocess.** If the binary is yours and the manifest is committed, this is reasonable.

v0.13.0's egress sandbox will land an `HTTP_PROXY`-based middle ground — subprocess inherits a proxy env var pointing at a local helmdeck-managed proxy that enforces the same allowlist as Go packs.

### 3. Hot-reload from the packs directory

Today, dropping a new executable into `$HELMDECK_COMMAND_PACKS_DIR` requires a control-plane restart. The dir is scanned once at startup.

v0.13.0 will add a watcher (probably `fsnotify`) so adding/removing executables updates the registry without a restart. Same flag as `helmdeck pack install/uninstall` for marketplace-registered packs.

## How it landed

The MVP is ~300 lines of pack-side code:

- `internal/packs/command_pack.go` (~200 lines) — `CommandSpec` + `NewCommandPack` + the handler closure. Maps `exec.ExitError` → `CodeHandlerFailed`, ctx-deadline → `CodeHandlerFailed`-with-timeout-message, missing-path → `CodeInternal`, non-JSON stdout → `CodeInvalidOutput`. A `cappedWriter` prevents a runaway subprocess from blowing up control-plane RAM (16 MiB stdout cap, 8 KiB stderr cap).
- `internal/packs/builtin/command_pack_example.go` (~80 lines) — `LoadCommandPacks(ctx, logger, dir)` scans the dir, registers one pack per executable found.
- `cmd/control-plane/main.go` (+13 lines) — one block that checks `HELMDECK_COMMAND_PACKS_DIR` and feeds the loader.

Tests are the bulk of the change. 17 new unit tests covering happy path, transform, non-zero exit + stderr, non-JSON stdout, empty stdout, timeout, missing path, missing binary, raw-binary sniff, the engine vs handler schema-validation boundary, and the dir-scan's basename sanitization.

The tests use the **self-exec pattern** — the test binary itself acts as the subprocess when invoked with `HELMDECK_PACK_TEST_FIXTURE=<mode>`. So the test suite needs no Python, no Bash, no `jq` — it works on any CI runner that can build Go.

## What's the right time to write a subprocess pack?

Three patterns where it's the right call:

1. **You're wrapping an existing CLI.** `pandoc.convert`, `imagemagick.resize`, `ffmpeg.transcode`. The subprocess pack IS the CLI; helmdeck just adds the audit log + MCP exposure.
2. **The logic lives in a non-Go ecosystem.** Python data-science packs (`pandas`, `scikit-learn`), Node API clients with no Go equivalent. Don't rewrite the world; wrap it.
3. **You're prototyping.** Subprocess packs are faster to iterate on than rebuilding helmdeck for every change. Get the input/output JSON working first; promote to a typed manifest (v0.13.0) when stable; promote to a Go pack (in-tree contribution) when it's load-bearing.

Three patterns where it's the wrong call:

1. **Tight egress requirements.** Until v0.13.0 ships the proxy sandbox, Go packs are the right tool for "must not exfiltrate."
2. **Performance-critical.** Each subprocess pack call costs a `fork+exec`. For sub-millisecond paths, a Go pack avoids the startup cost.
3. **In-tree shipping intent.** If you're going to land it in `internal/packs/builtin`, write Go from the start.

## Try it

v0.12.0 is out today. To enable subprocess packs:

```bash
mkdir -p ~/.helmdeck/command-packs
echo 'HELMDECK_COMMAND_PACKS_DIR=/home/me/.helmdeck/command-packs' \
  >> deploy/compose/.env.local
# Restart the stack to pick up the env var:
./scripts/install.sh --image-mode    # or your usual install command
# Drop your first executable in and call it.
```

The Pack Test Runner UI at `/packs` will list your `cmd.<name>` pack alongside the built-in catalog — click it, paste a JSON input, hit Run.

Manifests + hot-reload + egress sandbox in v0.13.0. Feedback welcome on [#173](https://github.com/tosin2013/helmdeck/issues/173) (manifests) and [#174](https://github.com/tosin2013/helmdeck/issues/174) (egress).
