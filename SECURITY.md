# Security policy

Helmdeck is a self-hosted platform that runs untrusted code (LLM-driven browser
sessions, sidecar code execution, credential injection on behalf of agents). We
take security reports seriously.

## Supported versions

We accept reports against the latest released minor (`vX.Y.*`) and the
in-development `main` branch.

| Version  | Supported          |
| -------- | ------------------ |
| 0.8.x    | :white_check_mark: |
| < 0.8    | :x:                |

## Reporting a vulnerability

**Please do not file a public GitHub issue for security reports.** Instead:

1. Email the maintainer **Tosin Akinosho** at **tosin.akinosho@gmail.com** with
   a description of the issue, reproduction steps, and the affected component
   (control plane, MCP bridge, sidecar image, deployment manifest, etc.). PGP
   key on request. Use the subject prefix `[helmdeck-security]` so the report
   routes correctly.
2. We will acknowledge within **3 business days** and aim for a triage status
   (accepted / rejected / needs-more-info) within **7 business days**.
3. We follow a **90-day coordinated disclosure** window. If we cannot ship a
   fix within 90 days we will negotiate with you in writing.
4. We credit reporters in the release notes unless you prefer otherwise.

## Hardening guidance

If you operate helmdeck in production, read
[`docs/SECURITY-HARDENING.md`](docs/SECURITY-HARDENING.md) — it covers the
sandbox baseline, NetworkPolicy egress allowlist, vault key rotation, and the
gVisor / Firecracker isolation tiers introduced in v1.0.

## Scope

In scope:

- Control plane (`cmd/control-plane`): authn/authz, vault, REST API, MCP bridge.
- Sidecars: container escape, sandbox bypass, secret leakage from env or fs.
- Distribution: tampering with `helmdeck-mcp` releases (Homebrew, Scoop, npm,
  OCI, GitHub Releases) — all of which are signed.

Out of scope:

- Vulnerabilities in upstream Chromium / Tesseract / Marp / xdotool — please
  report those upstream.
- Self-inflicted misconfiguration of the deployment (e.g. running with
  `isolation.level: standard` for hostile workloads).
