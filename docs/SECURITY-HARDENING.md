---
description: "Helmdeck's defense-in-depth posture and operator-controlled hardening knobs. Defaults are safe; this page covers tightening for production or high-trust deployments."
---

# Security hardening

This page documents helmdeck's defense-in-depth posture and the
operator knobs that ship with it. Defaults are safe — you should
NOT need to read this page to get a secure deployment — but if
you're hardening for production or running with elevated trust
boundaries, every layer below has a setting you can tighten.

## Defense in depth at a glance

| Layer | What it protects against | Default | Override |
|---|---|---|---|
| Application egress guard (T508) | SSRF / DNS rebinding to cloud metadata + RFC 1918 | enabled | `HELMDECK_EGRESS_ALLOWLIST=10.20.0.0/16,...` |
| Container sandbox (T509) | Kernel escape, fork bomb, privilege escalation | enabled | `HELMDECK_SECCOMP_PROFILE`, `HELMDECK_PIDS_LIMIT` |
| Credential vault (T501) | Plaintext secret leakage to LLM context | enabled | `HELMDECK_VAULT_KEY` (separate from keystore key) |
| JWT auth + audit log (T107/T109) | Unauthenticated calls + missing forensic trail | enabled | `HELMDECK_JWT_SECRET` (required) |
| iptables on `baas-net` (host) | Kernel-level egress denial | OPT IN | runbook below |

The first four layers are automatic — the binary applies them at
startup. The last layer is opt-in because it requires root on the
host and isn't appropriate for every deployment topology.

---

## T508 — Application-layer egress guard

helmdeck refuses to make any pack-handler call out to a host that
resolves to:

- **Cloud metadata** — `169.254.169.254/32` (AWS/GCP/Azure IMDS) and the wider `169.254.0.0/16` link-local range
- **RFC 1918 private** — `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`
- **Carrier-grade NAT** — `100.64.0.0/10` (Tailscale, ISPs)
- **Loopback** — `127.0.0.0/8`, `::1/128`
- **IPv6 private** — `fc00::/7` (ULA), `fe80::/10` (link-local)
- **Multicast / "this network"** — `0.0.0.0/8`, `224.0.0.0/4`, `ff00::/8`

The guard runs at the application layer in the control-plane Go
binary, so it works on bare metal, Compose, K8s, and any other
deployment topology — no iptables required.

### DNS rebinding defense

The guard resolves the host via DNS and checks **every** returned
address. A single blocked address fails the check, even if other
records look benign. This defeats the classic SSRF attack where an
attacker controls DNS for `evil.example` and returns one public IP
plus one metadata IP, hoping the kernel picks the wrong one.

### Allowlisting internal hosts

If you have internal CI servers, self-hosted git, or any other
RFC 1918 destination that pack handlers legitimately need to reach:

```sh
export HELMDECK_EGRESS_ALLOWLIST=10.20.0.0/16,192.168.5.0/24
./control-plane
```

The allowlist is comma-separated CIDRs, parsed at startup. Each
entry overrides the default block list for the addresses it
covers — `10.20.0.5` would pass the guard, but `10.20.5.5` (still
RFC 1918, not in the allowlist) would still be blocked.

The allowlist applies to **every** pack handler that consults the
guard. Today that's `repo.fetch`; T504 (placeholder-token gateway),
T505 follow-on packs, and the browser navigate handler will pick
it up over time.

---

## T509 — Container sandbox baseline

Every session container helmdeck spawns runs with:

| Setting | Value | Why |
|---|---|---|
| User | UID 1000 (`helmdeck`) | Non-root inside the container; `setuid` binaries can't escalate. |
| `--cap-drop ALL` | drops every capability | Removes the kernel attack surface that needs caps. |
| `--cap-add SYS_ADMIN` | adds back the one Chromium needs | Required for Chromium's user-namespace sandbox. Nothing else gets re-added. |
| `--security-opt no-new-privileges` | disables setuid escalation | Even setuid root binaries inside the container can't gain caps. |
| `--security-opt seccomp=...` | docker's default profile | Curated upstream syscall filter; tighter custom profiles supported. |
| `--pids-limit 1024` | hard cap on processes | Neutralizes fork bombs without breaking Chromium's normal ~150-process load. |
| `--memory <spec>` | per-session memory cap | Default 1 GiB; configurable per session. |
| `--shm-size <spec>` | per-session `/dev/shm` cap | Default 2 GiB; Chromium needs ≥1 GiB for SPA workloads. |

The non-root UID, cap drop, and `no-new-privileges` flags have been
in place since T103. T509 added `seccomp` configurability and
`pids-limit`.

### Custom seccomp profiles

The default seccomp profile is **Docker's built-in** — a curated,
upstream-maintained syscall filter that's known compatible with
Chromium. Most operators should leave this alone.

If you have a tighter custom profile (say, one that blocks `ptrace`
entirely or denies network syscalls Chromium doesn't need):

```sh
export HELMDECK_SECCOMP_PROFILE=/etc/helmdeck/chrome-strict.json
./control-plane
```

The path is passed verbatim to docker as `seccomp=<path>` in the
session container's `SecurityOpt`. The file must be readable by the
control-plane process (which is also the user that issues the
`docker create` call).

A tighter profile that we've validated against the helmdeck pack
catalog will ship in `deploy/docker/seccomp-helmdeck.json` once
the operator runbook for Chrome syscall coverage is finalized — see
the related GitHub issue.

### Adjusting the PID cap

The default `pids-limit` is **1024**. Chromium spawns ~150 processes
under typical headless load, plus ~10 for xdotool/scrot/socat
helpers, plus a handful per pack invocation — so 1024 is comfortable
with about 6× headroom.

If you're running pack handlers that legitimately fork heavily
(parallel test runners, build systems doing `make -j32`, etc.):

```sh
export HELMDECK_PIDS_LIMIT=4096
./control-plane
```

Set to `0` to disable the cap entirely. Don't disable it in
production — fork bombs are the most common DoS vector for
container workloads.

### What `T509` deliberately does NOT do

- **Read-only root filesystem.** Chromium needs `/home/helmdeck`
  writable for its profile dir. A tighter setup with `/home` as
  a tmpfs is on the roadmap; tracked separately.
- **AppArmor / SELinux profiles.** Distro-specific; the operator
  runbook for both lives in `docs/SIDECAR-EXTENDING.md`.
- **User namespaces.** Docker's `userns-remap` is the right
  long-term path but requires daemon-level config; operators who
  want it should set `userns` in `/etc/docker/daemon.json`.
- **gVisor / Firecracker.** These are the K8s "enhanced" and
  "maximum" isolation tiers (T709). Compose tier is "standard".

---

## Optional: kernel-level egress denial via iptables

For deployments where the application-layer guard isn't enough
(suspicious binary in the sidecar, in-container kernel exploit
that bypasses the helmdeck Go guard), you can enforce egress at
the host iptables layer too. This catches anything that escapes
the application layer.

### One-shot script

```sh
# Find baas-net's bridge interface name (usually br-<hash>)
BRIDGE=$(docker network inspect baas-net -f '{{range .Containers}}{{.IPv4Address}}{{end}}' | head -1 | cut -d. -f1-3)

# Block cloud metadata IP (169.254.169.254)
sudo iptables -I DOCKER-USER -i br-${BRIDGE} -d 169.254.169.254 -j DROP

# Block RFC 1918 (operators with internal allowlists should
# replace these with --not -d 10.20.0.0/16 etc.)
sudo iptables -I DOCKER-USER -i br-${BRIDGE} -d 10.0.0.0/8 -j DROP
sudo iptables -I DOCKER-USER -i br-${BRIDGE} -d 172.16.0.0/12 -j DROP
sudo iptables -I DOCKER-USER -i br-${BRIDGE} -d 192.168.0.0/16 -j DROP
```

The `DOCKER-USER` chain runs **before** docker's own bridge rules
and is the supported insertion point for operator-managed filters.
Run the script once after `docker compose up`; the rules persist
until you flush them or reboot.

**Don't run this if:**

- helmdeck is your only Docker workload AND you've accepted the
  application-layer guard's coverage.
- You have internal services that pack handlers need to reach and
  haven't carved out exceptions.
- You're not comfortable debugging iptables rules when something
  legitimate gets blocked.

### Persisting across reboots

Distro-specific. Three common options:

- **Debian/Ubuntu:** `apt install iptables-persistent`, then
  `netfilter-persistent save`.
- **RHEL/Fedora:** the rules go in `/etc/sysconfig/iptables` or you
  manage them via `firewalld`.
- **NixOS:** declarative `networking.firewall` config in your
  flake.

Reboot strategy is out of scope for this doc — pick whatever your
ops team already uses for other host-level firewall rules.

---

## Auditing what the security layers actually allowed

Every pack invocation, every vault read, every credential rotation
lands in the audit log (T109). Query it via the SQLite DB
directly:

```sql
SELECT ts, event_type, actor_subject, payload_json
  FROM audit_log
 WHERE event_type IN ('pack_call', 'vault_read', 'key_rotated')
 ORDER BY ts DESC
 LIMIT 100;
```

Egress denials and seccomp violations show up in the **pack_call**
rows — the failed handler's error message includes "egress denied"
or the syscall name docker reported. Combined with the credential
usage log (`SELECT * FROM credential_usage_log WHERE result =
'denied'`), this gives you a forensic answer to "what did the agent
try to do that we blocked?"

The Phase 6 Management UI (T611) wraps both tables in a filterable
table view; until then, SQL is the answer.

---

## T504 — Placeholder-token egress gateway

Agents reference vault credentials via `${vault:NAME}` placeholder
syntax. The placeholder resolver scans outbound HTTP requests
(URLs, headers, bodies) for these patterns, looks each name up in
the credential vault (gated by ACL), and substitutes the plaintext
before the request leaves helmdeck. The agent **never** sees the
real credential.

### Calling pattern

The canonical demo is the `http.fetch` pack:

```sh
curl -X POST http://localhost:3000/api/v1/packs/http.fetch \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "url":     "https://api.github.com/repos/tosin2013/helmdeck",
    "method":  "GET",
    "headers": {"Authorization": "Bearer ${vault:github-token}"}
  }'
```

The control plane:

1. Substitutes any `${vault:NAME}` patterns in the URL string.
2. Egress-guards the resolved URL (blocks metadata IP, RFC 1918, etc.).
3. Substitutes placeholders in every header value and the body.
4. Forwards the rewritten request via the placeholder-aware HTTP client.
5. Returns the response status, headers, and body to the agent.

The credential plaintext lives in helmdeck's process memory for
the duration of one HTTP round trip and is then dropped. There's
no audit log entry containing the plaintext — only the credential
name and the resolution result (`allowed` / `denied` / `no_match`)
land in `credential_usage_log`.

### What the resolver does NOT cover

- **Arbitrary in-container HTTP traffic.** The resolver wraps an
  http.Client in the helmdeck Go process. Code running inside a
  session container that makes its own HTTP calls bypasses the
  resolver. For session-side egress, the iptables runbook below
  remains the right answer.
- **HTTPS MITM proxying.** The resolver does not terminate TLS or
  inject a custom CA cert into session containers. That's a much
  bigger ship and breaks pinned-cert clients; the per-pack
  http.Client wrapper covers helmdeck's actual use case.
- **Response substitution.** Only outbound traffic is rewritten.
  Responses pass through verbatim — agents see the real API
  response, just never the credential that authorized it.
- **Streaming bodies > 4 MiB.** The resolver buffers the request
  body to scan it; bodies larger than 4 MiB are forwarded
  unchanged with a logged warning. Pack handlers that need to
  upload large files should call `placeholder.Substitute()` on
  the headers/URL but bypass the wrapped client for the body.

### Granting credentials to packs

Same flow as `repo.fetch`/`repo.push` — create the credential, then
grant the appropriate actor access. For an `http.fetch` call from
a Claude Code agent:

```sh
# Create the credential
curl -X POST http://localhost:3000/api/v1/vault/credentials \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -d '{
    "name":          "github-token",
    "type":          "api_key",
    "host_pattern":  "api.github.com",
    "plaintext_b64": "'$(printf 'ghp_real_token' | base64)'"
  }'

# Grant the agent access (using the JWT subject + client claim)
curl -X POST http://localhost:3000/api/v1/vault/credentials/cred_xxx/grants \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -d '{"actor_subject":"alice","actor_client":"claude-code"}'
```

The agent then references the credential by name in any
`http.fetch` call, and helmdeck enforces the ACL on every
substitution attempt.

---

## Trivy CI scan gate (T511)

Every push and PR runs `trivy fs --severity CRITICAL` over the
working tree as the `trivy-scan` job in `.github/workflows/ci.yml`.
A single CRITICAL finding fails the build and blocks the merge.
HIGH/MEDIUM/LOW findings are uploaded to the GitHub Security tab
as SARIF without blocking, so the team can triage them on cadence
instead of in the middle of unrelated PRs.

### When the scan fails

Triage in this order:

1. **Read the finding** in the failed CI job's `Run Trivy
   filesystem scan (CRITICAL gate)` step. The output names the
   vulnerable package, the installed version, the fixed version,
   and the CVE id.

2. **Bump the dependency** if a fix is available:
   ```sh
   go get <module>@<fixed-version>
   go mod tidy
   ```
   If it's a transitive dep, the direct parent has to release the
   bump first — find the parent in `go mod why <module>` and either
   wait, fork, or pin via the `replace` directive.

3. **If no fix is available** and the vulnerability genuinely doesn't
   apply to helmdeck (e.g. a CVE in a code path we don't reach),
   add a `.trivyignore` file at the repo root with the CVE id and a
   one-line justification:
   ```
   # CVE-2099-12345 — affects only the X.Y entrypoint we don't import
   CVE-2099-12345
   ```
   `.trivyignore` entries are reviewed quarterly. Don't ignore
   anything you haven't actually understood.

4. **For findings in the sidecar Dockerfile** (apt packages, base
   image), update the base image tag in `deploy/docker/sidecar.Dockerfile`
   and rebuild via `make sidecar-build`. The `trivy-scan` job
   covers the *source tree*; the `release` workflow's cosign +
   image signing step is the corresponding gate for built images.

### Adding scope to the scan

The current scope is `fs` (filesystem scan over Go modules + apt
manifests + helm charts + secrets-by-pattern). Adding `image` scans
to the release workflow is the natural next layer once the
`helmdeck-control-plane` and `helmdeck-sidecar` images settle on
stable tags — see the related GitHub issue.

---

## Reporting security issues

Security-relevant bugs (auth bypass, sandbox escape, vault leak,
egress guard bypass) should NOT be filed as public GitHub issues.
Email <tosin.akinosho@gmail.com> with `[helmdeck-security]` in the
subject and we'll coordinate disclosure.

For non-security operational questions about hardening — "is X
blocked by default?" or "how do I add Y to my allowlist?" — file
a normal issue using the **bug report** template.
