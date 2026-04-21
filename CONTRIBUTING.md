# Contributing to helmdeck

Thanks for considering a contribution. The most useful contributions
right now are **Capability Packs** — typed, schema-validated tools
that extend what an LLM can do through helmdeck. This guide walks
through how to add one, plus the broader contribution conventions.

## License and copyright

Helmdeck is licensed under the [Apache License 2.0](LICENSE). By
submitting a pull request you agree to license your contribution
under the same terms. We don't require a separate CLA — Apache 2.0
Section 5 covers the contribution grant.

New source files should carry an SPDX header:

```go
// SPDX-License-Identifier: Apache-2.0
//
// Copyright 2026 The helmdeck contributors
```

We don't retroactively add headers to existing files; the convention
applies to new files going forward.

## Pack contributions

A Capability Pack is a Go function that returns a `*packs.Pack`
with a typed input schema, a typed output schema, and a handler.
The handler runs inside helmdeck's pack engine — it gets a
session container if it asks for one, an audit-logged context, an
artifact store, optional CDP client for the browser, and optional
session executor for shelling out to in-container tools.

### What makes a good pack

1. **Typed inputs and outputs.** Every field in the schema is
   either required or optional. The LLM should never see a free-form
   "args" map.
2. **Closed-set error codes** (see `internal/packs/errors.go`). Any
   non-trivial failure maps to one of `invalid_input`,
   `handler_failed`, `session_unavailable`, `timeout`,
   `schema_mismatch`, `artifact_failed`, or `internal`. Don't invent
   new codes — if you genuinely need one, file an issue first.
3. **Idempotent where possible.** A retry of the same input with the
   same vault state should produce the same outcome.
4. **Audit-friendly.** Don't echo secrets in the output. The audit
   middleware redacts known sensitive payload keys but not
   pack-specific ones.
5. **Bounded.** Cap response sizes, cap iteration counts, validate
   URLs against the egress guard if you make outbound HTTP calls.
6. **Tested.** Every built-in pack ships with table-driven tests
   using `recordingExecutor` (for `Exec`-driven packs) or stub
   `cdp.Client` (for browser packs). See `internal/packs/builtin/`
   for the patterns.

### Pack types we'd love to see

The categories below are concrete examples of packs that would
slot directly into the existing catalog. None of these have been
written yet — they're listed to give a sense of what fits.

- **SaaS API wrappers** — official packs for the products you
  build, maintain, or use daily. The most valuable are vendor-
  maintained: `slack.post_message`, `linear.create_issue`,
  `notion.read_page`, `stripe.create_invoice`,
  `salesforce.lookup_account`. These typically use `http.fetch`
  with `${vault:NAME}` substitution under the hood.
- **Document & data extraction** — `pdf.extract_tables`,
  `excel.read_sheet`, `docx.extract_text`, `audio.transcribe`,
  `image.classify`. Most of these need a sidecar with the right
  toolchain — see `docs/SIDECAR-LANGUAGES.md` for the runbook.
- **Code intelligence** — `code.search_symbol`, `code.format`,
  `code.lint`, `dependency.audit`. These build on the Phase 5.5
  `fs.*` and `cmd.run` primitives.
- **Workflow / composite packs** — `pr.review_loop`,
  `issue.triage`, `release.notes_from_commits`. These chain
  primitives behind one typed call.
- **Communication** — `email.send`, `pagerduty.incident`,
  `discord.webhook`. SMTP/HTTP via vault credentials.
- **Data pipelines** — `db.query` (against a vault-stored
  connection string), `redis.get`, `kafka.produce`,
  `s3.copy_object`.
- **Security & compliance** — `secrets.scan` (gitleaks wrapper),
  `image.scan` (trivy wrapper), `cve.lookup`,
  `policy.evaluate` (OPA wrapper).
- **AI helper packs** — `ai.summarize`, `ai.translate`,
  `ai.embed_text`, `ai.classify_intent`. Compose AI gateway calls
  into typed primitives.
- **Utilities** — `time.now`, `uuid.generate`, `regex.test`,
  `json.path` (jq-style queries).

### Authoring a pack — the short version

1. **Pick a namespace.** `<vendor_or_domain>.<verb_or_noun>`. Look
   at the existing built-ins for examples.
2. **Add a file** under `internal/packs/builtin/<name>.go`.
3. **Write the constructor** that returns `*packs.Pack`. Copy the
   shape of `internal/packs/builtin/screenshot_url.go` for a
   browser-driven pack or `internal/packs/builtin/doc_ocr.go` for
   an executor-driven pack.
4. **Write the handler.** `func(ctx, *packs.ExecutionContext)
   (json.RawMessage, error)`. Validate input. Use `ec.CDP` /
   `ec.Exec` / `ec.Artifacts` as needed. Return typed output.
5. **Write tests.** Use `newScreenshotEngine` / `newSlidesEngine`
   / `newRepoEngine` patterns. Mock the executor or CDP client.
6. **Register it** in `cmd/control-plane/main.go` next to the
   other `packReg.Register(builtin.X())` calls. Pass the vault
   and egress guard if your pack needs them.
7. **Update `docs/MILESTONES.md`** if your pack closes a tracked
   task.

If your pack needs a different sidecar image (a language toolchain,
a niche binary), see `docs/SIDECAR-LANGUAGES.md` for the four-file
pattern: Dockerfile, Makefile target, pack constructor with
`SessionSpec.Image`, registration line.

If your pack hits the public internet, **always** route through
the egress guard (`security.EgressGuard.CheckURL`) so corp metadata
IPs and RFC 1918 ranges are blocked by default.

## Other contribution types

Pack contributions are the highest-leverage thing you can do, but
the project also welcomes:

- **Sidecar language images** — see `.github/ISSUE_TEMPLATE/sidecar-language-request.yml`
  to request one or `docs/SIDECAR-LANGUAGES.md` to build one yourself.
- **Documentation improvements** — operator runbooks for specific
  cloud providers, security audit reports, walkthrough tutorials.
- **Bug fixes** — file an issue first if it's non-trivial; small
  fixes (typos, obvious bugs) can come straight as a PR.
- **ADR drafts** for design decisions you think the project should
  formalize. ADRs live in `docs/adrs/` and follow the existing
  numbered template.

## Development workflow

```sh
# Run the full test suite
make test
go test ./... -count=1

# Or run the full CI gate locally (vet + race test + build) before pushing
make check

# Build the control plane binary
make build

# Build the base sidecar image
make sidecar-build

# Build language sidecars (Python, Node)
make sidecars

# End-to-end smoke test (requires Docker)
make smoke
```

The CI workflow runs `go vet`, `go test -race`, `make build`, and a
Trivy filesystem scan on every PR. Run `make check` locally first to
catch the first three before they fail in CI. All four must pass
before merge. The Trivy scan fails on CRITICAL findings — see
`docs/SECURITY-HARDENING.md` for the triage runbook.

To wire `make check` into `git push`, run `make install-hooks` once in
your clone. This is opt-in — it sets `core.hooksPath` to the project's
`.githooks/` directory and only affects your local copy.

## Reporting security issues

Security-relevant bugs (auth bypass, sandbox escape, vault leak,
egress guard bypass) should NOT be filed as public GitHub issues.
Email <tosin.akinosho@gmail.com> with `[helmdeck-security]` in the
subject and we'll coordinate disclosure.

## Code of conduct

Be kind. Argue from evidence. Disagree with the design, not the
designer. We don't need a 30-page CoC — if your behavior would get
you removed from a Kubernetes SIG meeting, it'll get you removed
from helmdeck too.
