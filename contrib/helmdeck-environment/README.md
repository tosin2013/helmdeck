# helmdeck-environment

A [mini-swe-agent](https://github.com/SWE-agent/mini-swe-agent) `Environment`
that routes every bash command the agent runs through **helmdeck's `cmd.run`
REST pack** instead of executing it on the local host.

This is **Phase 1** of the `swe.solve` epic
([#233](https://github.com/tosin2013/helmdeck/issues/233)). It is the seam
between an SWE agent loop and helmdeck's sidecar isolation: the agent issues
shell commands as usual, but each one is POSTed to a helmdeck control plane and
executed inside helmdeck's tiered sandbox (Docker / gVisor / Firecracker per
operator policy). The agent loop never needs to know.

> The Go `swe.solve` pack that drives this end-to-end (`repo.fetch → agent loop
> → git.commit → git.push`) is **Phase 3** and is **blocked by
> [#232](https://github.com/tosin2013/helmdeck/issues/232)** — out of scope here.

## How it works

Each `execute(command, cwd)` call becomes:

```
POST {HELMDECK_BASE_URL}/api/v1/packs/cmd.run
Authorization: Bearer <jwt>
Content-Type: application/json

{
  "clone_path": "<cwd or HELMDECK_CLONE_PATH>",
  "command": ["bash", "-lc", "<command>"],
  "_session_id": "<pinned after first call>"
}
```

helmdeck replies with:

```json
{ "output": { "stdout": "...", "stderr": "...", "exit_code": 0 }, "session_id": "..." }
```

The adapter maps that to mini-swe-agent's expected return shape
`{"output": "<stdout+stderr>", "returncode": <int>}`.

The helmdeck **session is pinned** via `_session_id`: the first command's
response yields a session id and every later command reuses it, so the working
directory and any filesystem changes persist across the whole task (the same
mechanism `repo.fetch → fs.* → cmd.run → git.commit` uses).

## Configuration (environment variables)

| Env var | Purpose | Default |
|---|---|---|
| `HELMDECK_BASE_URL` | Control-plane base URL (`HELMDECK_URL` also accepted) | `http://localhost:3000` |
| `HELMDECK_TOKEN` | Pre-minted helmdeck JWT (`HELMDECK_JWT` also accepted) | — |
| `HELMDECK_USERNAME` | Used for `/api/v1/auth/login` if no token | `admin` |
| `HELMDECK_PASSWORD` | Used for `/api/v1/auth/login` if no token | — |
| `HELMDECK_CLONE_PATH` | Working dir inside the session. Must be absolute under `/tmp/helmdeck-` or `/home/helmdeck/work/` | `/home/helmdeck/work/repo` |
| `HELMDECK_SESSION_ID` | Pin to an existing session (e.g. one `repo.fetch` created) | — (created on first command) |
| `HELMDECK_TIMEOUT` | Per-request HTTP timeout, seconds | `300` |

Provide **either** `HELMDECK_TOKEN` **or** `HELMDECK_PASSWORD` (the adapter
will mint a JWT via `POST /api/v1/auth/login` if only the password is set).

Mint a token manually the same way the [OpenClaw integration
doc](../../docs/integrations/openclaw.md) does:

```bash
export HELMDECK_BASE_URL=http://localhost:3000
export HELMDECK_TOKEN=$(curl -s -X POST "$HELMDECK_BASE_URL/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<from install.sh>"}' | jq -r .token)
```

## Install

```bash
pip install -e contrib/helmdeck-environment        # the adapter
pip install mini-swe-agent                          # the agent (separate)
```

The adapter has **no runtime dependencies** — it talks to helmdeck with the
Python stdlib, so it is safe to vendor next to mini-swe-agent.

## Point mini-swe-agent at it

mini-swe-agent resolves environments by class import path. Reference the
adapter in a mini config (`environment_class`):

```yaml
environment:
  environment_class: helmdeck_environment.HelmdeckEnvironment
  cwd: /home/helmdeck/work/repo
```

…and run:

```bash
export HELMDECK_BASE_URL=http://localhost:3000
export HELMDECK_TOKEN=...           # or HELMDECK_PASSWORD
export HELMDECK_CLONE_PATH=/tmp/helmdeck-clone-xxxx   # an existing checkout

mini -c my-helmdeck-config.yaml -t "List the files and read the README."
```

> The exact `mini --env <name>` short-form depends on your mini-swe-agent
> version's environment registry. The `environment_class` config above is the
> portable form. An `minisweagent.environments` entry-point alias
> (`helmdeck-environment`) is also declared in `pyproject.toml`.

## Manual verification (ls + cat)

With a live helmdeck and a valid `HELMDECK_CLONE_PATH`, the agent loop will run
real commands. The minimal smoke is two commands — `ls` then `cat`:

```bash
export HELMDECK_BASE_URL=http://localhost:3000
export HELMDECK_TOKEN=$(curl -s -X POST "$HELMDECK_BASE_URL/api/v1/auth/login" \
  -H 'Content-Type: application/json' -d '{"username":"admin","password":"<pw>"}' | jq -r .token)
export HELMDECK_CLONE_PATH=/tmp/helmdeck-clone-xxxx

# Drive it directly (no LLM) to confirm the round-trip:
python - <<'PY'
from helmdeck_environment import HelmdeckEnvironment
env = HelmdeckEnvironment()
print(env.execute("ls -la"))
print(env.execute("cat README.md | head -5"))
PY

# …or through mini-swe-agent:
mini -c my-helmdeck-config.yaml -t "Run ls in the repo, then cat the README."
```

You should see the directory listing and README contents in `output`, a
`returncode` of `0`, and the same `session_id` reused across both calls (check
the helmdeck Audit Logs panel — one `cmd.run` entry per command, in order).

## Tests

```bash
pip install -e 'contrib/helmdeck-environment[dev]'
pytest contrib/helmdeck-environment
```

The unit tests stub the REST layer and run offline. The `test_live_ls_and_cat`
integration test is **skipped unless `HELMDECK_BASE_URL` is set**.

## mini-swe-agent Environment contract — assumption

mini-swe-agent's `Environment` is a small Protocol. This adapter implements:

```python
class Environment:
    config: HelmdeckEnvironmentConfig          # has .cwd and .env
    def execute(self, command: str, cwd: str = "") -> dict   # -> {"output": str, "returncode": int}
    def get_template_vars(self) -> dict
```

This matches mini-swe-agent's built-in `LocalEnvironment` / `DockerEnvironment`
return shape. It could not be verified against an installed copy at authoring
time (no network in the build env), so it is a **documented best-effort**. If a
future upstream release renames the keys, only `HelmdeckEnvironment.execute`
needs updating.
