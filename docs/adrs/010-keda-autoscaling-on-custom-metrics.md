# 10. KEDA Autoscaling on Custom Session Metrics

**Status**: Accepted
**Date**: 2026-04-07
**Domain**: distributed-systems

## Context
Standard CPU/memory HPA does not correlate with browser session demand: a session can be idle (low CPU) while holding a critical authenticated state, or thrashing (high CPU) on a JavaScript-heavy page. Scaling on hardware metrics produces both starvation and waste (PRD §20.6).

## Decision
Use KEDA with a Prometheus scaler reading two custom metrics exported by the Go control plane at `/metrics`:
- `baas_queued_session_requests` — scale up when any request queues.
- `baas_active_sessions / baas_pool_capacity` — scale up when pool exceeds 80% utilization.

A separate `browser-pool-warmup` Deployment maintains a pool of pre-initialized session containers that the control plane claims on demand, eliminating the 2–4 s Chromium cold start from the first pack call's critical path.

## Consequences
**Positive:** scaling tracks real session demand; warm pool removes cold-start latency; metrics are also visible to operators on the dashboard.
**Negative:** KEDA is a required cluster add-on for the K8s tier; warm pool consumes baseline resources even when idle.

## Related PRD Sections
§20.6 Autoscaling with KEDA, §8.2 Dashboard, §18 Success Metrics
