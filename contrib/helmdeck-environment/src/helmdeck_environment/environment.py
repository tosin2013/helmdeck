# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 The helmdeck contributors
"""A mini-swe-agent ``Environment`` that runs commands via helmdeck.

The implementation targets the mini-swe-agent ``Environment`` plugin
contract (see the project's ``minisweagent/environments/local.py``). That
contract, as of mini-swe-agent's local/docker environments, is small:

    class Environment(Protocol):
        config: <a dataclass with `cwd` and `env` fields>

        def execute(self, command: str, cwd: str = "") -> dict:
            '''Run `command`; return {"output": str, "returncode": int}.'''

        def get_template_vars(self) -> dict:
            '''Vars exposed to the agent's prompt templates.'''

The agent treats a non-zero ``returncode`` and the combined ``output`` text
exactly as it would from a local shell, so as long as we return that dict
shape the agent loop is satisfied.

We could NOT pin the exact upstream signature at authoring time (the package
was not installable in the build environment — no network). The shape above
is implemented as a documented best-effort and is the contract mini-swe-agent
has shipped for its built-in environments. If a future upstream release
renames the keys, only :meth:`HelmdeckEnvironment.execute` needs to change.
"""

from __future__ import annotations

import json
import os
import shlex
import urllib.error
import urllib.request
from dataclasses import dataclass, field
from typing import Any


class HelmdeckError(RuntimeError):
    """Raised when helmdeck returns an error envelope or is unreachable."""


@dataclass
class HelmdeckEnvironmentConfig:
    """Configuration for :class:`HelmdeckEnvironment`.

    Every field has an environment-variable fallback so the environment can
    be constructed by mini-swe-agent's ``--env`` machinery (which instantiates
    environments with no required positional args).
    """

    # Base URL of the helmdeck control plane, e.g. "http://localhost:3000".
    # Read from HELMDECK_BASE_URL (preferred) or HELMDECK_URL.
    base_url: str = field(
        default_factory=lambda: (
            os.environ.get("HELMDECK_BASE_URL")
            or os.environ.get("HELMDECK_URL")
            or "http://localhost:3000"
        ).rstrip("/")
    )

    # Pre-minted helmdeck JWT. Read from HELMDECK_TOKEN (preferred) or
    # HELMDECK_JWT. If empty, the environment falls back to username/password
    # login against /api/v1/auth/login using HELMDECK_USERNAME / HELMDECK_PASSWORD.
    token: str = field(
        default_factory=lambda: os.environ.get("HELMDECK_TOKEN")
        or os.environ.get("HELMDECK_JWT")
        or ""
    )
    username: str = field(default_factory=lambda: os.environ.get("HELMDECK_USERNAME", "admin"))
    password: str = field(default_factory=lambda: os.environ.get("HELMDECK_PASSWORD", ""))

    # Working directory inside the helmdeck session. helmdeck's cmd.run pack
    # requires an absolute path under /tmp/helmdeck- or /home/helmdeck/work/.
    # When mini-swe-agent's repo.fetch step (Phase 3) creates a session it
    # returns the real clone_path; until then HELMDECK_CLONE_PATH lets a manual
    # run point at an existing checkout.
    cwd: str = field(
        default_factory=lambda: os.environ.get(
            "HELMDECK_CLONE_PATH", "/home/helmdeck/work/repo"
        )
    )

    # Pin to an existing helmdeck session (e.g. the one repo.fetch created).
    # Read from HELMDECK_SESSION_ID. When empty, the first command creates a
    # session and we reuse its id for every subsequent command.
    session_id: str = field(default_factory=lambda: os.environ.get("HELMDECK_SESSION_ID", ""))

    # Extra environment variables to export before each command. mini-swe-agent
    # expects an `env` mapping on the config for parity with LocalEnvironment.
    env: dict[str, str] = field(default_factory=dict)

    # Per-request timeout in seconds for the HTTP call to helmdeck.
    timeout: int = field(default_factory=lambda: int(os.environ.get("HELMDECK_TIMEOUT", "300")))


class HelmdeckEnvironment:
    """mini-swe-agent Environment that executes commands via helmdeck's REST API.

    Each :meth:`execute` call POSTs to ``/api/v1/packs/cmd.run`` with the
    command wrapped as ``["bash", "-lc", <command>]`` and the configured
    ``clone_path``. The helmdeck session is pinned via ``_session_id`` so the
    working directory and any filesystem mutations persist across the many
    commands the agent issues during a single task.
    """

    def __init__(self, *, config: HelmdeckEnvironmentConfig | None = None, **kwargs: Any) -> None:
        # mini-swe-agent constructs environments with keyword config overrides;
        # accept either a ready config or loose kwargs that map onto the
        # config dataclass fields.
        if config is None:
            config = HelmdeckEnvironmentConfig()
        for key, value in kwargs.items():
            if hasattr(config, key) and value is not None:
                setattr(config, key, value)
        self.config = config
        self._token: str | None = config.token or None
        self._session_id: str = config.session_id

    # -- mini-swe-agent Environment contract ------------------------------

    def execute(self, command: str, cwd: str = "") -> dict[str, Any]:
        """Run ``command`` through helmdeck and return the agent-facing dict.

        Returns ``{"output": <combined stdout+stderr>, "returncode": <int>}``,
        matching mini-swe-agent's LocalEnvironment so the agent loop can branch
        on the return code and feed ``output`` back into the conversation.
        """
        clone_path = cwd or self.config.cwd
        # Prepend any configured env exports so they apply to the command.
        prefix = ""
        if self.config.env:
            exports = " ".join(
                f"export {k}={shlex.quote(v)};" for k, v in self.config.env.items()
            )
            prefix = exports + " "
        script = prefix + command

        payload: dict[str, Any] = {
            "clone_path": clone_path,
            # cmd.run takes an argv array; we hand it a bash -lc wrapper so the
            # agent's shell syntax (pipes, &&, globs, redirects) works as it
            # would locally.
            "command": ["bash", "-lc", script],
        }
        if self._session_id:
            payload["_session_id"] = self._session_id

        result = self._post_pack("cmd.run", payload)

        # The engine surfaces the session id at the top level of the Result
        # envelope; capture it so subsequent commands reuse the same session.
        sid = result.get("session_id")
        if sid:
            self._session_id = sid

        output = result.get("output", {})
        stdout = output.get("stdout", "")
        stderr = output.get("stderr", "")
        combined = stdout
        if stderr:
            combined = (stdout + "\n" + stderr) if stdout else stderr
        return {
            "output": combined,
            "returncode": int(output.get("exit_code", 0)),
        }

    def get_template_vars(self) -> dict[str, Any]:
        """Variables exposed to mini-swe-agent's prompt templates."""
        return {
            "cwd": self.config.cwd,
            "helmdeck_base_url": self.config.base_url,
            "helmdeck_session_id": self._session_id,
            **self.config.env,
        }

    # -- helmdeck REST plumbing -------------------------------------------

    def _ensure_token(self) -> str:
        if self._token:
            return self._token
        if not self.config.password:
            raise HelmdeckError(
                "no helmdeck token: set HELMDECK_TOKEN (or HELMDECK_JWT), or "
                "HELMDECK_PASSWORD for /api/v1/auth/login"
            )
        body = json.dumps(
            {"username": self.config.username, "password": self.config.password}
        ).encode()
        resp = self._raw_request(
            "POST",
            f"{self.config.base_url}/api/v1/auth/login",
            body,
            auth=False,
        )
        token = resp.get("token")
        if not token:
            raise HelmdeckError(f"login returned no token: {resp!r}")
        self._token = token
        return token

    def _post_pack(self, name: str, payload: dict[str, Any]) -> dict[str, Any]:
        url = f"{self.config.base_url}/api/v1/packs/{name}"
        body = json.dumps(payload).encode()
        return self._raw_request("POST", url, body, auth=True)

    def _raw_request(
        self, method: str, url: str, body: bytes, *, auth: bool
    ) -> dict[str, Any]:
        headers = {"Content-Type": "application/json"}
        if auth:
            headers["Authorization"] = f"Bearer {self._ensure_token()}"
        req = urllib.request.Request(url, data=body, method=method, headers=headers)
        try:
            with urllib.request.urlopen(req, timeout=self.config.timeout) as r:
                raw = r.read()
        except urllib.error.HTTPError as e:
            detail = e.read().decode("utf-8", "replace")
            raise HelmdeckError(
                f"helmdeck {method} {url} -> HTTP {e.code}: {detail}"
            ) from e
        except urllib.error.URLError as e:
            raise HelmdeckError(f"helmdeck {method} {url} unreachable: {e}") from e
        if not raw:
            return {}
        try:
            return json.loads(raw)
        except json.JSONDecodeError as e:
            raise HelmdeckError(f"helmdeck returned non-JSON: {raw[:200]!r}") from e
