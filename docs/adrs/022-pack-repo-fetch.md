---
description: "ADR-022: Pack: `repo.fetch` — Accepted. Architectural decision record for the helmdeck control-plane."
---

# 22. Pack: `repo.fetch`

**Status**: Accepted
**Date**: 2026-04-07
**Domain**: api-design, security

## Context
The 2026-04-06 git SSH failure is the canonical motivation for Capability Packs: an agent attempted `git clone git@github.com:...`, hit SSH host-key verification with no `known_hosts` and no key material, and could not reason through the multi-line stderr blob. Weak models cannot reliably recover from this surface (PRD §3.1 Failure 3, §6.6).

## Decision
Ship `repo.fetch` as a built-in pack.

**Input:** `{ url: string (https or git@ form), ref?: string (default "HEAD"), subpath?: string }`
**Output:** `{ local_path: string, commit_sha: string, files_count: integer }`
**Errors:** `auth_failed`, `not_found`, `timeout`, `network_error`

The handler normalizes the URL (HTTPS↔SSH), resolves credentials from the Credential Vault by host pattern, sets `GIT_SSH_COMMAND` with `StrictHostKeyChecking=accept-new` if only an SSH key is available, retries on transient failures, and returns a typed result. The agent never sees SSH stderr, never chooses between HTTPS and SSH, and never touches `known_hosts`.

## Consequences
**Positive:** the failure mode that motivated this whole architecture is closed in one pack.
**Negative:** must keep up with git server quirks (LFS, partial clones, mono-repo subpath fetches).

## Related PRD Sections
§3.1 Failure Mode 3, §6.6 Capability Packs (`repo.fetch` example), §14 Credential Vault

---

## §2026-04-15 revision — context envelope

### Context

The original `repo.fetch` output schema returned `{clone_path, commit, files: <int>}` — a file *count* with no content. On 2026-04-14 an OpenClaw agent cloned `github.com/tosin2013/low-latency-performance-workshop` (1688 KB, README.adoc, `content/`, `docs/`, `blog-posts/`, 100+ tracked files) and reported **"the repository appears to be empty"**, then gave up. Root-causing from the agent trace:

1. Agent defaulted to reading `README.md`. This repo uses AsciiDoc (`README.adoc`). `fs.read README.md` returned not-found; agent generalized "no README = empty repo."
2. `fs.list` without `recursive: true` hit only top-level files and missed where the actual material lives (`content/`, `docs/`, `blog-posts/`).
3. `fs.list` with `recursive: true` on a large repo errors hard at `maxFsListFiles`, so agents don't get a truncated view they can reason about — they get a failure that looks terminal.

This is an **orientation** failure, not a clone-pipeline failure. The clone worked; the agent didn't know how to look at what it had.

### Research anchor

Surveyed six agent-platforms' "first-touch" repo context strategies (Aider, Cursor, Continue.dev, Claude Code/Codex, OpenHands, Sourcegraph Cody). Strongest empirical data point: Aider's structural repo-map got them to **SOTA 26.3% on SWE-bench Lite** ([aider.chat/2024/05/22/swe-bench-lite.html](https://aider.chat/2024/05/22/swe-bench-lite.html)) at ~1k tokens, beating embeddings/RAG at the same budget. The hard first step for an agent is **"which files matter"** — and a compact structural hint outperforms prose summaries or vector retrieval.

Claude Code and Codex CLI take a different shape: auto-load `CLAUDE.md` / `AGENTS.md` verbatim into the system prompt. This is the same principle (give the agent orientation on the first turn) with a different surface.

### Decision

Extend `repo.fetch`'s output schema with a **context envelope** — always populated when python3 is available in the sidecar, omitted otherwise for backward compatibility. New fields:

- `tree` — flat array of `git ls-files` output, sorted, capped at 300 entries.
- `tree_total` / `tree_truncated` — real count + truncation flag so the agent knows to narrow with `fs.list` + glob if needed.
- `readme` — `{path, content, truncated}` for the first file matching `README.{md,adoc,rst,txt}` case-insensitively at repo root, content capped at 4 KB.
- `entrypoints` — whitelist-matched orientation files (`Makefile`, `go.mod`, `package.json`, `pyproject.toml`, `Cargo.toml`, `pom.xml`, `build.gradle`, `devfile.yaml`, `Dockerfile`, `docker-compose.yml`, `CLAUDE.md`, `AGENTS.md`, `CONTRIBUTING.md`) with a `kind` classifier.
- `doc_hints` — static glob list (`README*`, `docs/**/*.{md,adoc,rst}`, `content/**/*.{md,adoc}`) the agent can pass to `fs.list`.
- `signals` — coarse classifier `{has_readme, has_docs_dir, has_code, doc_file_count, code_file_count, sparse}` so the agent can one-check-branch instead of inferring from paths.

Hard envelope cap: ~8 KB / ~2 k tokens (matches Aider's empirically-tuned budget plus headroom for README content). Enforced by clipping `readme.content` first, then truncating `tree` to 300 entries.

### Alternatives considered

- **LLM-generated prose summary.** Adds an LLM call (latency + cost + new failure mode). Beaten per-token by structural maps per Aider's SWE-bench data.
- **Vector embeddings / RAG index on clone.** What Cursor/Continue.dev do. Requires embedding gateway, vector store, index invalidation. Aider's data shows structural maps win at ~1k-token budgets. Revisit only if agents exhibit semantic-search needs we can't meet with `fs.list` + `repo.map`.
- **Tree-sitter symbol map inline in `repo.fetch`.** Highest signal-per-token, but needs parser shared libraries in the sidecar image (~30–150 MB) and costs CPU per clone even for docs-only repos. Shipped instead as a separate opt-in pack — see ADR 036.
- **Nested tree objects** (`{name, type, children}`). Costs more tokens than a flat path array for no additional signal; the LLM reads hierarchy from paths.

### Consequences

**Positive:**
- The "empty repo" false positive is structurally impossible when a README is present — `signals.has_readme` + `readme.content` both surface it. SKILLS.md explicitly forbids the agent from concluding "empty" when `has_readme: true`.
- Agents orient in one tool call instead of three (fetch → list → read-README).
- Response size grows from ~200 B to ~8 KB worst case. Envelope is cheap to produce (single Python pass over the clone) and cheap to consume (plain JSON, LLMs parse it trivially).
- Backward compatible — old callers reading `{clone_path, commit, files}` keep working; envelope fields are additions.

**Negative:**
- Sidecar image now requires `python3` (already present for other packs) and ships with `universal-ctags` for the follow-on `repo.map`. Operators patching this into a minimal custom image need both installed.
- README auto-detection is glob-based: if a repo uses a non-standard name (e.g. `about.md`, `project-overview.adoc`), it won't surface and the agent must fall back to `tree` + `doc_hints`.

### Verification

- `TestRepoFetchEnvelopeScript_Integration` runs the real shell pipeline against a fixture mirroring the workshop-repo failure mode (`README.adoc`, `content/`, `docs/`, `Makefile`). Asserts envelope shape: README detected, entrypoints include Makefile, signals flag `has_readme: true` / `has_docs_dir: true` / `sparse: false`.
- `TestRepoFetchEnvelopeScript_SparseRepo` asserts `sparse: true` on a bare LICENSE-only repo so agents have a deterministic flag for the "ask the user" branch.
- `TestRepoFetch_LegacyEnvelopeStillWorks` confirms the handler accepts the old 3-field shape without synthesising fields or erroring.

### Related ADRs
- **ADR 036** — `repo.map` (follow-on opt-in pack; symbol-level structural map)
- **ADR 003** — Capability Packs as the primary product surface

