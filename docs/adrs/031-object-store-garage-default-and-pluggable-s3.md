# 31. Object Store: Garage as Bundled Default, Pluggable External S3

**Status**: Accepted
**Date**: 2026-04-08
**Domain**: distributed-systems
**Supersedes**: open question §1 in `docs/TASKS.md` ("bundled MinIO vs external S3")
**Touches**: ADR 014 (`slides.render`), ADR 021 (`browser.screenshot_url`) — both delegate to "the configured object store"; this ADR fills in what that means.

## Context

T211 (Phase 2) shipped a working object-store integration that speaks the S3 API via `github.com/minio/minio-go/v7`, configured through `HELMDECK_S3_*` environment variables and falling back to an in-memory store when those are unset. ADRs 014 and 021 reference "the configured object store" abstractly without naming a backend. The original Phase 1 kickoff question — "bundle MinIO or require external S3?" — was deferred because Phase 2 prioritized the pack engine over the storage substrate.

That deferred decision now has a forcing function: **MinIO upstream is dead.** The chronology:

| Date | Event |
| :--- | :--- |
| 2025-05 | MinIO Inc. strips the admin Console UI from the AGPL `minio/minio` repo. User mgmt, IAM, bucket policies, lifecycle config — gone from the GUI. CLI still works. |
| 2025-06 | Community backlash. Pricing for the paid AIStor replacement: USD 96k/year minimum. |
| 2025-12-03 | Upstream `minio/minio` repo flipped to "maintenance mode" on GitHub. |
| 2026-02-12 | Status changed to "no longer maintained"; repo archived. |
| 2026 | A community fork ("MinIO Resurrected" / Vonng's revival) restores the console and ships its own CI binaries — single-maintainer effort, no governance. |

Bundling an archived upstream — even one with a community revival — is untenable for a project aiming at GA. Operators who already run MinIO in production are not stranded (the existing binaries still work, the fork is functional), but new helmdeck deployments must default to a backend with a credible long-term maintenance story.

The artifacts helmdeck stores are pack outputs: PNG screenshots, scraped JSON, rendered PDFs/PPTX, OCR results, MP4 demos. Sizes range from KB to ~50 MB. Workloads are write-once / read-few via signed URL distribution. Single-node Compose is the v0.x deployment target; Helm/Kubernetes is the v1.0 target.

## Decision

**Bundle [Garage](https://garagehq.deuxfleurs.fr/) as the default object store. Treat the storage layer as a pluggable S3-compatible client so any external backend (AWS S3, Cloudflare R2, Backblaze B2, SeaweedFS, Garage cluster, Ceph RGW, the MinIO fork, …) is a first-class deployment option. Never bundle MinIO.**

### Backend choice: Garage (default)

| Dimension | Garage | Notes |
| :--- | :--- | :--- |
| License | AGPLv3, stable | Community-governed (Deuxfleurs collective), no for-profit vendor that can flip the table. |
| Release cadence | v2.0.0 (2025-06), v2.1.0 + v2.2.1 (Q1 2026) | Active. |
| Footprint | Single static Rust binary, ~30 MB | Fits the helmdeck "boring infrastructure" profile. |
| Target use case | Self-hosted, small-to-medium, geo-distributed | Designed for exactly the scale helmdeck targets at GA. |
| S3 API surface | Bucket CRUD, object CRUD, multipart, presigned PUT/GET, ACLs (basic) | Sufficient for every helmdeck pack output today. |
| Ops surface | Single config file, one persistent volume, one bootstrap command | Trivial to ship in Compose; trivial to wrap as a Helm sub-chart. |

Rejected alternatives:

- **MinIO upstream** — archived, no longer maintained.
- **MinIO community fork** — functional but single-maintainer; not appropriate as a default.
- **SeaweedFS** — actively developed, Apache 2.0, but designed for billions-of-files workloads. Heavier than helmdeck needs and the S3 gateway is a translation layer over a native filer API, leaving more surface for edge-case incompatibilities.
- **Ceph RGW** — production-grade but operationally heavy; an order of magnitude more moving parts than the helmdeck single-node Compose target tolerates.
- **External-only (no bundled default)** — raises the helmdeck "first run" cost from `docker compose up` to "go provision an S3 bucket somewhere first." Hostile to the evaluator path.

### Client library: keep `minio-go/v7`

The MinIO *server* being archived does not affect the MinIO *client library*. `minio-go/v7` is Apache 2.0 licensed, mature, actively maintained as a standalone S3 client, and handles two AWS SDK for Go v2 footguns automatically:

- Path-style addressing is the default (the AWS SDK v2 default of virtual-host style breaks against most non-AWS backends).
- It does not emit `x-amz-checksum-*` headers on presigned PUT (the AWS SDK v2 default trips checksum-strict non-AWS backends).

Migrating to `aws-sdk-go-v2` would be non-trivial work for zero functional gain. We keep `minio-go/v7` and treat it as the canonical S3 client. The dependency name is unfortunate optics; the technical reality is that it is the better tool for the non-AWS S3 ecosystem.

### Configuration shape

Existing env vars stay (no break for current deployments):

```
HELMDECK_S3_ENDPOINT          # required to enable persistent storage
HELMDECK_S3_BUCKET            # required
HELMDECK_S3_ACCESS_KEY        # required
HELMDECK_S3_SECRET_KEY        # required
HELMDECK_S3_REGION            # optional, default us-east-1
HELMDECK_S3_USE_SSL           # optional, default false
HELMDECK_S3_PUBLIC_ENDPOINT   # optional, rewrite host for signed URLs reachable outside docker net
```

Compose defaults wire these to the bundled Garage service so `docker compose up` produces a working artifact pipeline with no operator action.

### TTL and lifecycle

Bucket lifecycle policies are intentionally **not used**. Garage's lifecycle support is partial; SeaweedFS's is partial; even AWS S3 lifecycle has surprising semantics around versioned buckets. A backend-portable design must own this in helmdeck itself.

Implementation: a janitor goroutine in the control plane scans the audit table for pack output references older than `HELMDECK_ARTIFACT_TTL` (default 7 days) and deletes the corresponding objects. This also gives operators per-pack retention policies, which a bucket-level rule cannot express, and keeps the "what's still in the bucket" question answerable from helmdeck's own database.

### Backend-specific features that must NOT be used

To preserve drop-in portability across Garage / SeaweedFS / R2 / B2 / MinIO-fork / AWS S3:

- No bucket lifecycle (handled in app, see above).
- No backend-managed server-side encryption (use client-side encryption for sensitive payloads in a future ADR if needed).
- No ETag comparison across backends (ETag formats differ; treat as opaque versioning hints only).
- No conditional requests with `If-Match`/`If-None-Match` headers (semantics drift between backends).
- No object tagging (Garage and SeaweedFS support is partial).
- No bucket policies for access control (every backend speaks a different dialect; rely on signed URLs with short TTL for access enforcement).

### Helm / Kubernetes story (Phase 7)

The Helm chart will expose `objectStore.mode: bundled | external`:

- `bundled` deploys Garage as a StatefulSet with a single PVC (any RWO storage class), bootstrapped by an init container.
- `external` takes endpoint + credentials from a Secret.

Same shape as the Postgres sub-chart pattern (T703).

## Consequences

**Positive:**
- helmdeck never depends on a vendor that can flip the table on the OSS contract again.
- `docker compose up` produces a working artifact pipeline with no external setup.
- The same config surface works for hobbyist single-node deployments and production AWS deployments.
- The TTL janitor approach gives helmdeck per-pack retention semantics that no bucket-level rule can express.
- Future migrations between backends are tar+restore exercises, not code changes.

**Negative:**
- Adds a new service to the Compose stack (Garage container + persistent volume).
- The "minio-go imported but no MinIO bundled" optics need explanation in the README.
- Operators who hoped for bucket-lifecycle-driven cleanup get an in-app janitor instead — slightly less observable from the bucket side.
- We forgo a few S3 features (object tagging, backend SSE) that some workloads might want.

**Migration:**
- No code changes required for existing deployments — same env vars, same client library.
- Smoke harness (`make smoke`) gets a Garage service so the persistent-storage path is exercised in CI.
- A new task tracks bundling the Garage service + bootstrap in `deploy/compose/compose.yaml`.
- A new task tracks the artifact TTL janitor.
- A new task tracks updating ADRs 014 and 021 to cross-reference this ADR (one-line additions).
