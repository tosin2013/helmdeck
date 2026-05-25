# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 The helmdeck contributors
"""helmdeck-environment: a mini-swe-agent Environment backed by helmdeck.

This package provides :class:`HelmdeckEnvironment`, an implementation of
mini-swe-agent's ``Environment`` plugin contract that routes every bash
command the agent wants to run through helmdeck's ``cmd.run`` REST pack
instead of executing it on the local host.

The agent loop never touches a shell directly: every command is POSTed to
``/api/v1/packs/cmd.run`` on a helmdeck control plane, which runs it inside
helmdeck's tiered sidecar isolation (Docker / gVisor / Firecracker per
operator policy). State is preserved across commands by pinning a single
helmdeck session (``_session_id``) for the lifetime of the environment.

See ``README.md`` for configuration and a manual ``mini`` run.
"""

from .environment import HelmdeckEnvironment, HelmdeckEnvironmentConfig

__all__ = ["HelmdeckEnvironment", "HelmdeckEnvironmentConfig"]
__version__ = "0.1.0"
