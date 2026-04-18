# OpenClaw upgrade runbook

> Use this when you upgrade the `openclaw:local` image that fronts your helmdeck MCP server. Validated on the 2026.4.10 → 2026.4.18 upgrade path on 2026-04-18; extend the known-issue table as new releases surface problems.

## One-shot: `scripts/configure-openclaw.sh`

Everything in this runbook (JWT mint, MCP config with lowercase `authorization`, SKILLS.md push, tool-capable model pin, identity seed, gateway restart) is automated by one idempotent script:

```bash
# Configure the default agent after an upgrade
./scripts/configure-openclaw.sh

# Target a specific subagent + seed identity so BOOTSTRAP.md doesn't loop
./scripts/configure-openclaw.sh --agent coder --seed-identity

# Force a fresh JWT, pick a different model
./scripts/configure-openclaw.sh --rotate-jwt --model us.anthropic.claude-opus-4-5-20251101-v1:0

./scripts/configure-openclaw.sh --help   # full flag list
```

The rest of this document walks through what the script does (and what each manual step would look like if you're debugging a specific failure). Use the script on the happy path; fall back to the manual steps when something goes wrong and you need to bisect.

## TL;DR

```bash
# 1. Pull upstream, inspect the diff against the currently-deployed version
cd /root/openclaw
CURRENT=$(docker exec openclaw-openclaw-gateway-1 openclaw --version | awk '{print $2}')
git fetch --tags
git log --oneline "v$CURRENT"..origin/main -- 'src/agents/**' 'src/gateway/mcp*' 'src/agents/pi-bundle-mcp*' | head -30
# ^ scan for anything that looks like it touches MCP, tool policy, or the SSE transport.
git pull --ff-only

# 2. Rebuild the image + recreate the gateway in place
docker build -t openclaw:local -f Dockerfile .
docker compose up -d --force-recreate openclaw-gateway

# 3. Verify the container came up and version moved
docker exec openclaw-openclaw-gateway-1 openclaw --version

# 4. Run the post-upgrade checks (next section) BEFORE declaring victory
```

Total elapsed time on a warm build cache: ~2 minutes. Cold builds can take 5-10.

## Post-upgrade checks

Run all four. Any fail means the upgrade is not clean.

### 1. Config survived the recreate

```bash
docker exec openclaw-openclaw-gateway-1 openclaw mcp show helmdeck | head -15
```

Expected: a JSON object with `url: "http://helmdeck-control-plane:3000/api/v1/mcp/sse"` and `headers.authorization: "Bearer …"`. **The key MUST be lowercase `authorization`** — see [issue #1](https://github.com/tosin2013/helmdeck/issues/1) and [`openclaw-upstream-issue.md`](./openclaw-upstream-issue.md) for the case-collision bug this workaround dodges.

If the config is missing (possible on a `down` + `up` cycle with a named-volume reset), re-apply it:

```bash
TOKEN=$(cat /tmp/helmdeck-jwt.txt)   # or mint a fresh one, see below
docker exec openclaw-openclaw-gateway-1 openclaw mcp set helmdeck \
  "{\"url\":\"http://helmdeck-control-plane:3000/api/v1/mcp/sse\",\"headers\":{\"authorization\":\"Bearer $TOKEN\"},\"timeoutMs\":300000}"
```

### 2. helmdeck skill installed and at the right version

Helmdeck ships agent instructions as a native OpenClaw Skill (see `skills/helmdeck/SKILL.md` in this repo). The configure script installs it at `~/.openclaw/skills/helmdeck/SKILL.md` inside the OpenClaw container, stamped with the helmdeck commit hash in its frontmatter.

```bash
docker exec openclaw-openclaw-gateway-1 openclaw skills list | grep helmdeck
# Expect: ✓ ready 📦 helmdeck ...

docker exec openclaw-openclaw-gateway-1 sh -c \
  'grep -oE "helmdeckVersion: *\"[^\"]+\"" /home/node/.openclaw/skills/helmdeck/SKILL.md'
# Expect: helmdeckVersion: "<your current helmdeck commit short-hash>"
```

If the helmdeckVersion in the container doesn't match your local `git rev-parse --short HEAD`, the agent is on a stale copy — re-run `./scripts/configure-openclaw.sh` to refresh. (The historical `systemPromptOverride` mechanism has been retired; the script clears any leftover override on upgrade.)

### 3. Docker networks still bridged

```bash
docker inspect openclaw-openclaw-gateway-1 \
  --format '{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}'
```

Expected: both `baas-net` and `openclaw_default`. If only `openclaw_default` appears, reconnect:

```bash
docker network connect baas-net openclaw-openclaw-gateway-1
docker exec openclaw-openclaw-gateway-1 \
  getent hosts helmdeck-control-plane
```

Expected: a `baas-net` IP resolves for `helmdeck-control-plane`.

### 4. Chat-UI tool catalog is visible

Open `http://localhost:18789` in a browser, start a chat with the `main` agent, paste:

```
List every tool available to you whose name starts with "helmdeck__".
```

Expected: the agent lists **36 tools**, all prefixed `helmdeck__` (`helmdeck__repo_fetch`, `helmdeck__repo_map`, `helmdeck__browser_interact`, etc.). If the list is shorter, something on the MCP handshake broke. Verify with:

```bash
docker logs helmdeck-control-plane --since 10m 2>&1 | grep 'mcp/sse'
```

You should see a `GET /api/v1/mcp/sse` and several `POST /api/v1/mcp/sse/message` entries from the OpenClaw container IP.

## Known issues by version

| OpenClaw version | Affected path | Symptom | Mitigation |
|---|---|---|---|
| `2026.4.10` | All paths | `Authorization` (capital A) in MCP headers → 401 | Use lowercase `authorization`. Still required on all later versions. See issue #1. |
| `≥ 2026.4.17` | `openclaw agent` CLI | 24 built-in tools load, zero `helmdeck__*` tools, no diagnostic warn | Use chat UI for now. Upstream commit `0e7a992d` (PR #68195) added a tool-policy filter that fails closed on no-group-context sessions. CLI invocations lack group context. No local workaround known — needs upstream fix or config patch to attach a group to the agent. |
| `≥ 2026.4.18` | `agents.defaults.model.primary: openrouter/auto` | Agent emits fake Python (`print(helmdeck__repo_fetch(…))`) instead of issuing the MCP `tools/call` JSON; no tool call actually lands, agent hallucinates a response | Pin a tool-capable model explicitly. E.g.: `openclaw config set agents.defaults.model.primary us.anthropic.claude-sonnet-4-5-20250929-v1:0`. Any Bedrock Sonnet/Opus in the catalog supports native tool calls. |
| Any | Fresh workspace with `BOOTSTRAP.md` | Agent loops on "BOOTSTRAP pending — please read BOOTSTRAP.md" and never reaches the real prompt | Either complete the identity seed (`IDENTITY.md`, `USER.md`, `SOUL.md`) by replying to the agent's intro questions, or delete `/home/node/.openclaw/workspace/BOOTSTRAP.md` inside the container. Not a helmdeck bug but often surfaces during upgrade validation. |

## JWT re-mint (if needed)

JWTs are signed with `HELMDECK_JWT_SECRET` from `/root/helmdeck/deploy/compose/.env.local`. Default TTL in the helmdeck codebase is 12h; for test tokens bump to 7d:

```bash
export HELMDECK_JWT_SECRET=$(grep ^HELMDECK_JWT_SECRET \
  /root/helmdeck/deploy/compose/.env.local | cut -d= -f2)
TOKEN=$(python3 -c "
import jwt, time, os
now = int(time.time())
print(jwt.encode({
  'sub': 'openclaw-test', 'name': 'openclaw-test',
  'client': 'openclaw', 'scopes': ['admin'],
  'iss': 'helmdeck',
  'iat': now, 'nbf': now - 60, 'exp': now + 7*86400,
}, os.environ['HELMDECK_JWT_SECRET'], algorithm='HS256'))
")
echo "$TOKEN" > /tmp/helmdeck-jwt.txt
```

The `iss: 'helmdeck'` claim is required — helmdeck's JWT validator rejects tokens without it as `missing_claim`.

Then re-apply with `openclaw mcp set helmdeck …` as in post-upgrade check #1.

## Rollback procedure

If a post-upgrade check fails and the upstream diff includes a commit you suspect, you can pin back to a known-good tag:

```bash
cd /root/openclaw
git stash               # if you have local changes, otherwise skip
git checkout v2026.4.16  # or whichever tag was last green
docker build -t openclaw:local .
docker compose up -d --force-recreate openclaw-gateway
```

`v2026.4.16` is the last release before `#68195` (the tool-policy filter). If you need the CLI path working specifically, this is the right pin today.

To return to tracking `main`:

```bash
cd /root/openclaw
git checkout main && git pull --ff-only
docker build -t openclaw:local .
docker compose up -d --force-recreate openclaw-gateway
```

## Adding a new row to the known-issues table

When you upgrade and find a new breakage, document it here **before** you fix it — the mitigation research is the valuable part, not the bandaid. Template:

```
| <version-or-range> | <which path: CLI / chat-UI / SSE / etc> | <what the user actually sees> | <exact mitigation, with the config command if there is one, or "no workaround known + upstream commit hash" if not> |
```

Link to the upstream commit or PR if you can find one. If the fix lives in helmdeck (e.g. a config-recipe change), bump the revision section in [ADR 025](../adrs/025-mcp-client-integrations.md) instead of only editing this runbook — ADRs are the source of truth, runbooks are their user-facing projection.

## Related

- [ADR 025 — First-class MCP client integrations](../adrs/025-mcp-client-integrations.md) (§2026-04-18 revision covers the CLI regression policy)
- [`openclaw.md`](./openclaw.md) — installation + configuration reference
- [`openclaw-upstream-issue.md`](./openclaw-upstream-issue.md) — draft for the header case-collision bug (issue #1)
- [`openclaw-sidecar-research.md`](./openclaw-sidecar-research.md) — earlier 401 investigation (paused)
