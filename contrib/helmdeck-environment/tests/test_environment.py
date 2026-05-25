# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 The helmdeck contributors
"""Tests for HelmdeckEnvironment.

The unit tests stub helmdeck's REST layer so they run offline and assert the
request shape + the mini-swe-agent return contract. The integration test is
skipped unless HELMDECK_BASE_URL points at a live helmdeck (it runs `ls` and
`cat` for real, mirroring the manual `mini --env` smoke test).
"""

from __future__ import annotations

import json
import os

import pytest

from helmdeck_environment import HelmdeckEnvironment, HelmdeckEnvironmentConfig


def test_execute_request_shape_and_return_contract():
    """execute() POSTs the right cmd.run payload and returns {output, returncode}."""
    captured = {}

    cfg = HelmdeckEnvironmentConfig(
        base_url="http://helmdeck.test:3000",
        token="test-jwt",
        cwd="/home/helmdeck/work/repo",
    )
    env = HelmdeckEnvironment(config=cfg)

    def fake_raw_request(method, url, body, *, auth):
        captured["method"] = method
        captured["url"] = url
        captured["auth"] = auth
        captured["payload"] = json.loads(body)
        return {
            "pack": "cmd.run",
            "session_id": "sess-123",
            "output": {"stdout": "hello\n", "stderr": "", "exit_code": 0},
        }

    env._raw_request = fake_raw_request  # type: ignore[assignment]

    result = env.execute("echo hello")

    assert captured["method"] == "POST"
    assert captured["url"] == "http://helmdeck.test:3000/api/v1/packs/cmd.run"
    assert captured["auth"] is True
    assert captured["payload"]["clone_path"] == "/home/helmdeck/work/repo"
    assert captured["payload"]["command"] == ["bash", "-lc", "echo hello"]
    # First call has no pinned session yet.
    assert "_session_id" not in captured["payload"]

    assert result == {"output": "hello\n", "returncode": 0}
    # Session id captured for reuse.
    assert env._session_id == "sess-123"


def test_session_pinning_reuses_session_id():
    cfg = HelmdeckEnvironmentConfig(base_url="http://h:3000", token="t", cwd="/tmp/helmdeck-x")
    env = HelmdeckEnvironment(config=cfg)
    calls = []

    def fake_raw_request(method, url, body, *, auth):
        calls.append(json.loads(body))
        return {"session_id": "S1", "output": {"stdout": "", "stderr": "", "exit_code": 0}}

    env._raw_request = fake_raw_request  # type: ignore[assignment]
    env.execute("true")
    env.execute("true")

    assert "_session_id" not in calls[0]
    assert calls[1]["_session_id"] == "S1"


def test_nonzero_exit_and_stderr_merge():
    cfg = HelmdeckEnvironmentConfig(base_url="http://h:3000", token="t", cwd="/tmp/helmdeck-x")
    env = HelmdeckEnvironment(config=cfg)

    def fake_raw_request(method, url, body, *, auth):
        return {"output": {"stdout": "out", "stderr": "boom", "exit_code": 2}}

    env._raw_request = fake_raw_request  # type: ignore[assignment]
    result = env.execute("false")
    assert result["returncode"] == 2
    assert "out" in result["output"] and "boom" in result["output"]


def test_env_var_config_defaults(monkeypatch):
    monkeypatch.setenv("HELMDECK_BASE_URL", "http://from-env:3000/")
    monkeypatch.setenv("HELMDECK_TOKEN", "envtok")
    monkeypatch.setenv("HELMDECK_CLONE_PATH", "/tmp/helmdeck-abc")
    cfg = HelmdeckEnvironmentConfig()
    assert cfg.base_url == "http://from-env:3000"  # trailing slash stripped
    assert cfg.token == "envtok"
    assert cfg.cwd == "/tmp/helmdeck-abc"


def test_get_template_vars():
    cfg = HelmdeckEnvironmentConfig(base_url="http://h:3000", token="t", cwd="/tmp/helmdeck-x")
    env = HelmdeckEnvironment(config=cfg)
    tv = env.get_template_vars()
    assert tv["cwd"] == "/tmp/helmdeck-x"
    assert tv["helmdeck_base_url"] == "http://h:3000"


@pytest.mark.skipif(
    not os.environ.get("HELMDECK_BASE_URL"),
    reason="set HELMDECK_BASE_URL (+ HELMDECK_TOKEN/HELMDECK_PASSWORD and "
    "HELMDECK_CLONE_PATH) to run the live integration smoke test",
)
def test_live_ls_and_cat():
    """Live smoke: run `ls` then `cat` against a real helmdeck session.

    Mirrors the manual `mini --env helmdeck-environment` run documented in the
    README. Requires a reachable helmdeck and a valid clone_path under
    /tmp/helmdeck- or /home/helmdeck/work/.
    """
    env = HelmdeckEnvironment()
    ls = env.execute("ls -la")
    assert ls["returncode"] == 0, ls["output"]
    assert env._session_id, "session id should be captured after first command"

    cat = env.execute("cat /etc/hostname || true")
    assert "returncode" in cat
