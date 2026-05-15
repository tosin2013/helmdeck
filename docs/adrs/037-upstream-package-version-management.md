# ADR 037 — Upstream Package Version Management for Sidecar-Bundled Tools

**Status:** Accepted
**Date:** 2026-05-15
**Author:** Tosin Akinosho
**Domain:** build-hygiene, supply-chain

## Context

Helmdeck sidecar images bundle upstream CLIs and their dependencies — `marp-cli`, `@playwright/mcp`, `@mermaid-js/mermaid-cli`, `hyperframes`, and the language-specific toolchains in `sidecar-python`/`sidecar-node`. Go pack code calls these CLIs by argv: the upstream's flag names, positional arguments, output shape, and exit-code semantics form an **implicit API surface** that helmdeck's packs are coupled to.

When an upstream releases a major version that renames or removes a flag, the helmdeck Go pack tests do not catch it. The tests use a `recordingExecutor` pattern that stubs `ec.Exec` and asserts on the argv the pack constructed — it never invokes the real binary. Breakages surface only at first real-world invocation, often months after the upstream release.

The pinning strategy was also drifting toward incoherence:

| Tool (as of 2026-05-15) | Current pin | Floats on rebuild? |
| :--- | :--- | :--- |
| `marp-cli` | `4.0.4` (Dockerfile `ARG`) | No — deterministic |
| `@playwright/mcp` | `latest` | Yes |
| `@mermaid-js/mermaid-cli` | `latest` | Yes |
| `pnpm` / `yarn` (corepack) | `latest` / `stable` | Yes |
| `typescript`, `eslint`, `prettier`, `vitest`, `ts-node` | unpinned | Yes |
| `hyperframes` (PR #210 first cut) | `^0.6.7` (caret) | Yes |

Two recent incidents motivated this ADR:

1. **PR #210, first commit (#200 `hyperframes.render`).** The pack was authored against an imagined `@hyperframes/cli@1.4.0` that does not exist on npm; the real package is `hyperframes` (no scope), latest `0.6.7`, and its CLI surface is materially different from what the Go pack assumed (`--width`/`--height`/`--duration` flags do not exist — the CLI uses `--resolution <preset>`). The image build caught the bad package name; only manual investigation of the upstream source caught the flag mismatch. If the same Go pack had targeted a real but old version of the same tool, CI would have green-lit code that fails at first real invocation.

2. **PR #205 (Caleb's macOS-build fix).** `marp-cli` had been installed via a GitHub-Releases tarball that only ships `linux-amd64`, blocking arm64 development. The fix switched to `npm install -g @marp-team/marp-cli@${MARP_VERSION}` (`MARP_VERSION=4.0.4` exact-pinned). Today's `marp-cli` install is deterministic; the rest of the stack isn't.

These are not Go-side regressions a unit test could have caught — they are upstream-surface mismatches the build process couldn't see.

## Decision

Three rules govern every upstream package, OS layer, and CLI tool bundled into a helmdeck sidecar:

### 1. Exact version pins, everywhere

Every Dockerfile pins each upstream tool, library, or base image to an **exact** version (`x.y.z`) or an immutable digest (`sha256:…` for OCI bases). The following floating constructs are **forbidden** in production Dockerfiles:

- `@latest`
- Caret ranges (`^x.y.z`)
- Tilde ranges (`~x.y.z`)
- Major-only or major.minor-only tags (`@4`, `@4.0`)

Pins live in `ARG TOOL_VERSION=x.y.z` declarations at the top of each Dockerfile so the build is reproducible **and** the value is in a stable location Dependabot's existing Docker/npm regex parsers can target.

Rationale: bit-identical rebuilds for the same Dockerfile (a different machine, six months later, still gets the same image), and surface drift becomes a deliberate PR rather than a silent overnight surprise.

### 2. Image-build CLI-surface sentinel

Each sidecar Dockerfile concludes with sanity checks that **exercise the CLI surface helmdeck actually consumes** — not just `--version`. Each Go pack that calls an upstream CLI is paired with a help-grep that asserts the flags the pack uses still exist:

```dockerfile
# Layer N — install pinned upstream
RUN npm install -g --no-fund --no-audit "hyperframes@${HYPERFRAMES_VERSION}" \
 && npm cache clean --force

# Sentinel: version + every flag/subcommand the Go pack invokes by name.
# A renamed or removed flag in a future upstream release fails the image
# build here, not at the first real-world pack call. Keep in sync with
# argv construction in internal/packs/builtin/hyperframes_render.go.
RUN hyperframes --version \
 && hyperframes render --help | grep -q -- '--resolution' \
 && hyperframes render --help | grep -q -- '--fps' \
 && hyperframes render --help | grep -q -- '--quality' \
 && hyperframes render --help | grep -q -- '--output'
```

Sentinel rules:

- One `--version` check per tool (catches missing-package and gross install-time breakage).
- One `--help | grep -q -- '--flag'` line per **flag the Go pack passes by name**. The grep target is `--` so `--fps` is matched verbatim, not as a substring of `--fps-extra`.
- A short comment naming the Go pack file that motivates the sentinel, so a future contributor moving flags can find the corresponding test in seconds.

The sentinel is **build-time**, not runtime: a tool that compiles a help string at runtime (e.g. behind `RUNTIME_PLUGIN_ENABLED=true`) needs the relevant env var set during the sentinel layer too.

### 3. Dependabot for automated upgrade PRs

A `.github/dependabot.yml` opens upgrade PRs for every pinned dep:

- `package-ecosystem: docker` covering each sidecar Dockerfile (base image bumps).
- `package-ecosystem: npm` covering each Dockerfile's `ARG`-driven npm pins (the regex matches `npm install -g "<pkg>@${ARG}"` patterns).
- `package-ecosystem: gomod` for `go.mod`.
- `package-ecosystem: github-actions` for `.github/workflows/`.

Schedule weekly (Monday morning UTC); group by ecosystem so a quiet week generates one PR per sidecar rather than 30 isolated PRs. PRs are auto-assigned to a maintainer but no auto-merge — every bump runs the full CI pipeline including the CLI-surface sentinel (rule 2). A breaking flag rename fails the Dependabot PR's image build automatically, so the maintainer's review focuses on intent and changelog, not detection.

The Dependabot PR's job is to make upgrade a one-click decision, not a research expedition.

## Consequences

**Positive:**

- Image rebuilds are reproducible. The same Dockerfile produces the same image six months later.
- Upstream surface drift fails the build at PR time, not at production-call time.
- Upgrade hygiene becomes a low-friction weekly habit: review N Dependabot PRs, merge the safe ones, prioritize the breaking ones.
- New sidecars inherit a clear template: pin everything, sentinel everything the Go pack calls.

**Negative:**

- Maintainer overhead for weekly Dependabot review. Estimated 4–8 PRs/week across all sidecars at the current sidecar count; grows with each new sidecar.
- Exact-pin discipline requires a one-time migration: the existing `@latest` instances in `sidecar.Dockerfile` (`@playwright/mcp`, `@mermaid-js/mermaid-cli`) and `sidecar-node.Dockerfile` (corepack pnpm/yarn, eslint, prettier, etc.) need to be replaced with whatever the current latest resolves to.
- Sentinel maintenance: the help-grep set must stay in sync with the Go pack's argv construction. A pack that adds a new flag also adds a sentinel line, and a code-review checklist should call this out.

**What we trade away:**

- The (illusory) "free patch updates" that `@latest` and `^x.y.z` provided. In exchange we get reproducibility and explicit breakage at upgrade time. The cost of one breaking incident (the hyperframes 1.4.0/0.6.7 episode) exceeds a year of routine Dependabot PR review.

## Migration plan

Each step is its own PR per single-purpose-PR discipline; T-1 should land before any new sidecar lands.

| Step | Scope | Effort | Sequence |
| :--- | :--- | :--- | :--- |
| **T-1** | Add `.github/dependabot.yml` covering Docker, npm, gomod, github-actions. | ~30 min | First |
| **T-2** | Replace `@latest` / unpinned tools in `sidecar.Dockerfile` and `sidecar-node.Dockerfile` with exact-pin `ARG`s set to today's resolved versions. | ~1 hour | After T-1 |
| **T-3** | Add CLI-surface sentinel blocks to every sidecar Dockerfile, one help-grep per flag the corresponding Go pack uses by name. | ~2 hours | After T-2 |
| **T-4** | Update `CONTRIBUTING.md` / `docs/SIDECAR-LANGUAGES.md` §"Adding a new language" with the pin + sentinel requirement so every new sidecar inherits the policy. | ~30 min | After T-3 |

Once T-1 through T-4 land, the first Dependabot PR cycle exercises the policy end-to-end.

## What this ADR does NOT do

- **It doesn't apply to operator-supplied subprocess packs** (`cmd.*`, ADR 024). Those are user-authored binaries whose versioning is the operator's responsibility. The pinning policy applies only to upstream packages helmdeck itself bundles.
- **It doesn't change the base-OS dep strategy.** `apt-get install` continues to use Debian-stable defaults; pinning system packages individually is out of scope (helmdeck doesn't typically consume their CLI surface by argv — they're transitive runtime deps).
- **It doesn't pre-decide major-version upgrade policy.** Whether to take `hyperframes 1.x` when it ships is a per-PR judgement, not a rule encoded here. The ADR ensures the decision is *made deliberately* with a working sentinel, not absorbed silently.

## Related

- ADR 001 — sidecar pattern for browser isolation (defines the sidecar image abstraction this ADR pins).
- ADR 024 — user-authored pack extensibility (carves out the `cmd.*` namespace from this policy).
- ADR 035 — MCP server hosting and capability pack evolution (applies the pinning rules equally to hosted MCP servers bundled in sidecars).
- PR #200 / #210 — the `hyperframes.render` incident that motivated this ADR.
- PR #205 — the `marp-cli` arm64 fix; precedent for exact `ARG VERSION` pinning.

## PRD sections

None directly — this is a build-hygiene decision.
