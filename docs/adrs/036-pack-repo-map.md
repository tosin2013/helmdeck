---
description: "ADR-036: Pack: `repo.map` (Aider-style structural symbol map) — Proposed. Architectural decision record for the helmdeck control-plane."
---

# ADR 036 — Pack: `repo.map` (Aider-style structural symbol map)

**Status:** Accepted
**Date:** 2026-04-15
**Author:** Tosin Akinosho
**Domain:** api-design, agent-context

## Context

ADR 022's §2026-04-15 revision closed the "empty repo" orientation failure by adding a context envelope to `repo.fetch` (tree, README, entrypoints, signals). That envelope answers *"what's in this repo at the file level?"* — which is enough for docs-heavy tasks (presentations, blog posts, tutorials) and for agents that just need to pick which file to read next.

It does not answer *"where is `FunctionX` defined?"* or *"what's the API surface of this package?"* — the symbol-level questions that dominate code-understanding tasks. For those, the research anchor in the ADR 022 revision was explicit: **Aider's structural repo-map got them to SOTA 26.3% on SWE-bench Lite** at ~1k tokens, beating embeddings/RAG at the same budget ([aider.chat/2024/05/22/swe-bench-lite.html](https://aider.chat/2024/05/22/swe-bench-lite.html)). The signal-per-token of a ranked, symbol-annotated file list is the highest available.

SKILLS.md should be able to tell agents: *"if `signals.has_code: true` and the task is about code behavior, call `repo.map` instead of reading files blindly."* That requires helmdeck to actually ship a symbol-map pack.

## Decision

Ship `repo.map` as a separate **opt-in** pack the agent calls after `repo.fetch` when the task needs symbol-level context.

**Input:**
```json
{
  "_session_id":   "sess_...",
  "clone_path":    "/tmp/helmdeck-clone-...",
  "token_budget":  1500,
  "include_globs": ["*.go", "*.py"]
}
```

**Output:**
```json
{
  "map":              "path/to/file.go:\n  function Foo\n  struct Bar\n...",
  "tokens_estimated": 947,
  "files_covered":    42,
  "files_total":      142
}
```

**Errors:** `handler_failed` (ctags missing, python3 missing, ctags parse error) and `invalid_input` (unsafe glob, non-absolute `clone_path`).

### Pipeline

1. **`ctags -R --output-format=json --fields=+K+n`** — parses the entire clone with [universal-ctags](https://github.com/universal-ctags/ctags). One JSON tag record per line on stdout. Supports ~40 languages out of the box with no configuration.
2. **Python reducer** (embedded in the pack, piped after ctags) groups tags by file, ranks files, renders the map file-by-file under the token budget, emits a final JSON envelope.
3. **Ranking heuristic** — prefer files with more symbols (higher signal), shorter paths (closer to repo root), and membership in known code directories (`src/`, `cmd/`, `lib/`, `internal/`, `pkg/`, `app/`). Matches Aider's observed "important files live near the root."
4. **Budget** — token count approximated at 4 chars/token (Aider's working assumption; within ~10% across OSS tokenizers). Default budget: 1500 tokens. Caller can override.

### Security

The `include_globs` input is validated by an allow-list (alphanumerics + `*?[].,-_/`). Anything else is rejected before it reaches the shell, closing a clear injection path into `ctags --include=<glob>`.

## Alternatives considered

| Alternative | Verdict |
|---|---|
| **Call Aider CLI via `cmd.run`** (`aider --show-repo-map --map-tokens N`) | Rejected. Aider is ~500 MB installed and pulls ~20 Python deps. Shipping it inside the sidecar image is a massive footprint increase for a feature we can reimplement in 200 LOC. |
| **Use go-tree-sitter bindings directly** | Deferred. Higher fidelity than ctags (real parse trees instead of regex-ish tag patterns), but each language parser is a 5–15 MB shared library. ~150 MB total for Aider's supported language set. Worth the weight when ctags proves insufficient; not on day one. |
| **LLM-generated prose summary of the repo** | Rejected. Adds an LLM call (latency + cost + new failure mode). Aider's data shows structural maps beat prose summaries per token for symbol-retrieval tasks. |
| **Inline the symbol map into `repo.fetch`** | Rejected. Adds ~30 MB (ctags) + Python deps + CPU cost to *every* clone, even for docs-heavy tasks where symbols are irrelevant. Opt-in preserves the sub-second `repo.fetch` path and the ~8 KB envelope cap. |
| **Static regex-based symbol extraction** (grep for `func`/`def`/`class`) | Rejected. Reinvents ctags poorly — handles neither scope, kind classification, nor line numbers. ctags is the standard answer; no reason to re-solve a solved problem. |

## Consequences

**Positive:**
- Agents get Aider-grade code orientation without pulling Aider into the image.
- Clean separation between "what's in the repo" (ADR 022's envelope) and "what's defined where" (this pack). Agents can pick one without paying for the other.
- ctags is a mature, packaged dependency (`apt-get install universal-ctags`) — no version drift or parser-matrix maintenance.
- Token-budgeted output means the map always fits whatever context window the calling model has.

**Negative:**
- Sidecar image gains ~30 MB for `universal-ctags`. (Python3 was already present.) Acceptable given the image is already ~1.8 GB.
- ctags quality is language-dependent. Go, Python, JavaScript, TypeScript, Java, C/C++, Ruby are well-supported. More niche languages get fewer symbols but don't crash the pack.
- The 4-chars-per-token approximation is rough. Downstream callers using strict token accounting will need to re-tokenize. In practice agents treat the field as advisory.

## Verification

- **Unit**: `TestRepoMap_InputValidation` (required-field + glob-injection guards), `TestRepoMap_ClassifiesMissingCtags` / `TestRepoMap_ClassifiesMissingPython` (sentinel stderr → user-actionable install hint), `TestRepoMap_PassesThroughSidecarJSON` (handler does not synthesize fields).
- **Integration**: `TestRepoMap_Integration_Ranking` runs the real pipeline against a fixture with one "core" file (5 symbols) and one "helper" file (1 symbol). Asserts core outranks helper in the rendered map.
- **Integration**: `TestRepoMap_Integration_BudgetEnforced` runs against a 7-file fixture with a 10-token budget. Asserts `files_covered < files_total` and non-zero.
- **Integration**: `TestRepoMap_Integration_NoSymbols` asserts the pack returns a valid `{map: "", files_covered: 0}` envelope on a repo with no parseable code, not an error.
- **Security**: `TestIsSafeCtagsGlob` exercises the injection-guard table with shell metacharacters.

## Related ADRs
- **ADR 022** — `repo.fetch` (base pack; context envelope provides file-level orientation)
- **ADR 003** — Capability Packs as the primary product surface
- **ADR 035** — MCP server hosting & pack evolution (the "host, don't rebuild" principle — ctags is the host dependency here, not a reimplementation)

## Sources

- [Aider: Repository map](https://aider.chat/docs/repomap.html)
- [Aider: Building a better repo map with tree-sitter](https://aider.chat/2023/10/22/repomap.html)
- [Aider: SOTA on SWE-bench Lite via repo-map](https://aider.chat/2024/05/22/swe-bench-lite.html)
- [Universal Ctags](https://github.com/universal-ctags/ctags)
