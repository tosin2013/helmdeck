# 9. Dual-Tier Deployment: Docker Compose and Kubernetes via Helm

**Status**: Accepted
**Date**: 2026-04-07
**Domain**: distributed-systems

## Context
The platform must serve both single-node developers and multi-node production clusters from the same images and configuration values. Forcing Kubernetes on small teams blocks adoption; forcing Compose on enterprises caps scale (PRD §12, §20).

## Decision
Ship two deployment tiers using shared container images:
- **Docker Compose tier** for dev / single-node staging / ≤20 concurrent sessions. The control plane uses the Docker SDK via a mounted `/var/run/docker.sock` to spawn ephemeral session containers.
- **Kubernetes tier** for production. The control plane uses `client-go` against a `ServiceAccount` scoped to `pods`, `pods/log`, `pods/exec` in the dedicated `baas-sessions` namespace only — no cluster-wide RBAC. Two namespaces: `baas-system` (control plane, DB, MCP gateway) and `baas-sessions` (ephemeral browser pods).

Distribute the Kubernetes tier as a single Helm chart (`baas-platform`) with toggles in `values.yaml` for external DB, KEDA, isolation level, TLS (cert-manager), OTel, and External Secrets Operator. GitOps via Argo CD / Flux is the recommended deployment model.

## Consequences
**Positive:** uniform development and production paths; same images promoted from staging to prod; small teams aren't forced into K8s.
**Negative:** two orchestration code paths to maintain in the control plane (Docker SDK + client-go); session-spawning logic must abstract over both.

## Related PRD Sections
§12 Deployment Architecture, §20 Docker and Kubernetes Deployment Architecture
