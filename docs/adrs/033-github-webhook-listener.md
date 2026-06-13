---
description: "ADR-033: GitHub Webhook Listener — Proposed. Architectural decision record for the helmdeck control-plane."
---

# ADR 033 — GitHub Webhook Listener

**Status:** Accepted
**Date:** 2026-04-10
**Author:** Tosin Akinosho

## Context

Operators want helmdeck to react to GitHub events (push, pull_request,
issue_comment) and auto-trigger pack runs without a human in the loop.
Today's pack invocation requires either an MCP client (OpenClaw, Claude
Code) or a direct REST call — both are pull-based. A webhook listener
makes helmdeck event-driven: "when code is pushed to main, run the
test suite via `cmd.run`" or "when a PR is opened, clone it, run lints,
and post the result back as a comment".

## Decision

Ship `POST /api/v1/webhooks/github` as a stateless webhook receiver
that validates the GitHub HMAC signature (`X-Hub-Signature-256`),
parses the event payload, and dispatches a configured pack sequence.

### Wire shape

```
GitHub → POST /api/v1/webhooks/github
         Headers: X-Hub-Signature-256, X-GitHub-Event, X-GitHub-Delivery
         Body: JSON event payload
         ← 200 OK (accepted) or 400/401/500

Control plane:
  1. Verify HMAC-SHA256 signature against HELMDECK_GITHUB_WEBHOOK_SECRET
  2. Parse event type from X-GitHub-Event header
  3. Look up matching webhook rule in HELMDECK_GITHUB_WEBHOOK_RULES
     (or a config file / database table)
  4. For each matched rule: spawn a goroutine that calls
     engine.Execute with the rule's pack + the event payload as input
  5. Return 200 immediately (async dispatch — GitHub has a 10s timeout)
```

### Webhook rules

Rules map event types to pack sequences. Stored in the database
(with a REST CRUD surface at `/api/v1/webhooks/github/rules`) or
seeded from env:

```
HELMDECK_GITHUB_WEBHOOK_RULES='[
  {"event":"push","ref":"refs/heads/main","pack":"cmd.run","args":{"command":["make","test"]}},
  {"event":"pull_request","action":"opened","pack":"repo.fetch","chain":["cmd.run","git.commit"]}
]'
```

### Security

- **HMAC validation** is mandatory — unsigned payloads are rejected
  with 401. The secret is stored in `HELMDECK_GITHUB_WEBHOOK_SECRET`
  (or `_FILE` variant).
- **The webhook endpoint is NOT behind JWT auth** — it's called by
  GitHub's servers, not by an authenticated operator. Instead, the
  HMAC signature IS the auth. Add the path to `IsProtectedPath`
  exemptions alongside `/api/v1/auth/login`.
- **Rate limiting** — one goroutine per event × max 10 concurrent
  dispatches. Excess events are queued (bounded channel, 100 deep)
  and drained in order. If the queue is full, return 503 so GitHub
  retries later.
- **Payload size** — cap at 5 MB (GitHub's max is 25 MB but most
  events are < 100 KB). Reject with 413 if exceeded.

### Phase 1 scope (this ADR)

- `push` and `pull_request` events only
- Single-pack dispatch per rule (no chaining yet)
- Rules from env var `HELMDECK_GITHUB_WEBHOOK_RULES`
- Audit log entry per dispatch (event type, repo, ref, pack, result)

### Future extensions (tracked, not built)

- Rule CRUD in the Management UI (`/webhooks` panel)
- Pack chaining per rule (sequential, with session pinning)
- Status checks — post pack results back as GitHub commit statuses
- Branch protection integration — require helmdeck checks to pass
- Other webhook sources (GitLab, Bitbucket) via adapter pattern

## PRD Sections

§6.6 Capability Packs, §14 Webhook Integrations

## Consequences

- Helmdeck becomes event-driven, not just pull-based. This is the
  foundation for CI/CD-like workflows driven by AI agents.
- The webhook secret is a new secret that `install.sh` should generate
  and print alongside the admin password.
- Async dispatch means pack failures are not reported to GitHub
  synchronously. The operator sees them in the Audit Log panel.
  Phase 2 adds commit-status posting for synchronous feedback.
