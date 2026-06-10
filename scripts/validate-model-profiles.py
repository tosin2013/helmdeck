#!/usr/bin/env python3
# SPDX-License-Identifier: Apache-2.0
"""
Validate per-model profile YAMLs under models/*.yaml against the schema
documented in docs/reference/model-profiles-schema.md.

Checks:
- Required top-level fields present (provider, model, family, parameters,
  tier, context_window)
- provider: is one of the accepted union values (openrouter, huggingface,
  together, groq, cerebras, sambanova, custom)
- tier: is one of A, B, C
- File size under a soft cap (20 KB) — sanity check
- Empirical sections present even if empty arrays (validated_against,
  community_traces, comparison_traces)
- When provider: huggingface, optional hf_routing_policy / hf_partner fields
  follow expected shape
- When provider: custom, endpoint_base_url is present

Usage:
  python3 scripts/validate-model-profiles.py             # validate all models/*.yaml
  python3 scripts/validate-model-profiles.py path/to.yaml [more.yaml ...]  # validate specific files

Exit codes:
  0 — all files pass
  1 — at least one file failed validation
  2 — script error (no files found, can't read files, etc.)

Stdlib-only — requires PyYAML (installed by default on most CI runners; if
not, the script falls back to a structural check using `yaml.safe_load` import
guard).
"""
import sys
from pathlib import Path

try:
    import yaml
except ImportError:
    sys.stderr.write(
        "error: PyYAML required. Install via `pip install pyyaml` "
        "or your CI runner's apt-equivalent.\n"
    )
    sys.exit(2)


REPO_ROOT = Path(__file__).resolve().parent.parent
MODELS_DIR = REPO_ROOT / "models"

ACCEPTED_PROVIDERS = {
    "openrouter",
    "huggingface",
    "together",
    "groq",
    "cerebras",
    "sambanova",
    "custom",
}

ACCEPTED_TIERS = {"A", "B", "C"}

REQUIRED_TOP_LEVEL = [
    "provider",
    "model",
    "family",
    "parameters",
    "tier",
    "context_window",
]

REQUIRED_EMPIRICAL_SECTIONS = [
    "validated_against",
    "community_traces",
    "comparison_traces",
]

SOFT_SIZE_CAP_BYTES = 30 * 1024  # 30 KB — sanity check, not a tight limit.
                                  # Per-model profiles are reference data, not
                                  # bootstrap-injected (unlike AGENTS.md's 12K cap).
                                  # Rich empirical content (multiple community_traces +
                                  # detailed validated_against findings) can legitimately
                                  # exceed 20 KB — bumped after nemotron landed at 22.5 KB
                                  # post-PR #487. If a profile exceeds 30 KB, review what's
                                  # in it; consider whether the empirical work belongs in
                                  # a companion blog post.


def validate_one(path: Path) -> list[str]:
    """Return a list of error strings for this file. Empty list = pass."""
    errors: list[str] = []

    # File size check
    size = path.stat().st_size
    if size > SOFT_SIZE_CAP_BYTES:
        errors.append(
            f"file size {size} bytes exceeds soft cap of {SOFT_SIZE_CAP_BYTES} bytes"
        )

    # Parse
    try:
        with path.open() as f:
            data = yaml.safe_load(f)
    except yaml.YAMLError as e:
        errors.append(f"YAML parse error: {e}")
        return errors

    if not isinstance(data, dict):
        errors.append(f"top-level value must be a mapping, got {type(data).__name__}")
        return errors

    # Required top-level keys
    for key in REQUIRED_TOP_LEVEL:
        if key not in data:
            errors.append(f"missing required top-level key: {key}")

    # provider: in accepted union
    provider = data.get("provider")
    if provider is not None and provider not in ACCEPTED_PROVIDERS:
        errors.append(
            f"provider: '{provider}' not in accepted union "
            f"{sorted(ACCEPTED_PROVIDERS)}; see docs/reference/model-profiles-schema.md"
        )

    # tier: in A/B/C
    tier = data.get("tier")
    if tier is not None and tier not in ACCEPTED_TIERS:
        errors.append(
            f"tier: '{tier}' not in {sorted(ACCEPTED_TIERS)}"
        )

    # parameters and context_window are ints
    for numeric_field in ("parameters", "context_window"):
        v = data.get(numeric_field)
        if v is not None and not isinstance(v, int):
            errors.append(
                f"{numeric_field} must be an integer, got {type(v).__name__}: {v!r}"
            )

    # Empirical sections present (even if empty)
    for section in REQUIRED_EMPIRICAL_SECTIONS:
        if section not in data:
            errors.append(
                f"missing required empirical section: {section} "
                f"(use `[]` if empty)"
            )
        elif not isinstance(data[section], list):
            errors.append(
                f"{section} must be a list (got {type(data[section]).__name__})"
            )

    # Provider-specific checks
    if provider == "custom":
        if "endpoint_base_url" not in data:
            errors.append(
                "provider: custom requires endpoint_base_url field "
                "(see docs/reference/model-profiles-schema.md)"
            )

    if provider == "huggingface":
        # hf_routing_policy is optional; if present, validate the value
        policy = data.get("hf_routing_policy")
        if policy is not None and not isinstance(policy, str):
            errors.append(
                f"hf_routing_policy must be a string when set, got "
                f"{type(policy).__name__}: {policy!r}"
            )

    return errors


def main(argv: list[str]) -> int:
    if argv:
        paths = [Path(p) for p in argv]
        missing = [p for p in paths if not p.exists()]
        if missing:
            for p in missing:
                sys.stderr.write(f"error: file not found: {p}\n")
            return 2
    else:
        if not MODELS_DIR.exists():
            sys.stderr.write(f"error: models directory not found: {MODELS_DIR}\n")
            return 2
        paths = sorted(MODELS_DIR.glob("*.yaml"))
        if not paths:
            sys.stderr.write(f"error: no models/*.yaml files found in {MODELS_DIR}\n")
            return 2

    print(f"Validating {len(paths)} profile(s)...")
    print()

    any_failed = False
    for path in paths:
        rel = path.relative_to(REPO_ROOT) if str(path).startswith(str(REPO_ROOT)) else path
        errors = validate_one(path)
        if errors:
            any_failed = True
            print(f"  ✗ {rel}")
            for e in errors:
                print(f"      - {e}")
        else:
            print(f"  ✓ {rel}")

    print()
    if any_failed:
        print(
            "Validation FAILED. Fix the errors above, or update "
            "docs/reference/model-profiles-schema.md if the schema itself "
            "needs to evolve."
        )
        return 1
    print(f"All {len(paths)} profile(s) pass validation.")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
