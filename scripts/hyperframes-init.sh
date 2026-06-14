#!/usr/bin/env bash
# scripts/hyperframes-init.sh — scaffold a HyperFrames composition from an
# upstream-curated `--example` and emit the scaffolded project directory
# as a gzipped tarball.
#
# Why a tarball, not stitched HTML: the empirical scaffold from `hyperframes
# init` is a multi-file project — `index.html`, `compositions/*.html`,
# `assets/`, `hyperframes.json`, `meta.json`, `package.json`. The text
# content (caption transcript timing, sub-composition styles, asset
# references) is split across files and referenced by `data-composition-src`
# paths from index.html. There is no single-file form that preserves the
# render contract, so the script packages the whole tree and lets downstream
# packs (hyperframes.compose for content interpolation, hyperframes.render
# for execution) handle the project shape natively. See issue #503 for the
# architectural decision (Path B — project artifact mode).
#
# The `hyperframes.compose` pack calls this script via `ec.Exec` into the
# helmdeck-sidecar-hyperframes container — same pattern as av-validate.sh
# / hyperframes_render.go's session-exec invocations.
#
# Why scaffolding: Tier C models (gpt-oss-120b:free, gemma, smaller open
# models) reliably wire the compose → render chain but struggle to author
# HTML/CSS/GSAP from scratch — they collapse to "text on black background"
# under the visual-creativity ask. Borrowing the visual creativity from
# upstream's 140+ example catalog moves that burden from the model to the
# framework; the LLM only needs to interpolate content into the scaffold
# (PR 4 in #503's plan), not invent design.
#
# The principle this implements: "upstream CLI takes precedence over
# custom Go" — we shell to `hyperframes init` rather than reimplement
# the framework's example scaffolder. See CONTRIBUTING.md
# §"Pack contributions" for the full discussion.
#
# Usage:
#   hyperframes-init.sh \
#       --example=<name> \
#       [--resolution=<preset>] \
#       [--audio=<path>] \
#       [--output=<path>]
#
# Flags:
#   --example     Required. Upstream example name (e.g. swiss-grid,
#                 warm-grain, decision-tree, code-snippet-dark-modern).
#                 Run with an invalid name to see the full registry.
#   --resolution  Optional. Canvas preset accepted by `hyperframes init`:
#                 landscape (1920x1080 default), portrait, square, or
#                 their -4k variants. Defaults to `landscape`.
#   --audio       Optional. Path to an audio file (MP3/WAV/M4A) inside
#                 the sidecar. When provided, init scaffolds an audio-
#                 anchored composition. compose.go is responsible for
#                 fetching the artifact to a local path before exec.
#   --output      Optional. Where to write the gzipped tarball. Defaults
#                 to stdout (which is what ec.Exec captures via res.Stdout).
#                 Pass an explicit path when the caller prefers to read the
#                 tarball from disk (cleaner for binary handoff).
#
# Output:
#   A gzip-compressed tar archive whose root contains the project files:
#       index.html
#       compositions/{intro,graphics,captions,...}.html
#       assets/...
#       hyperframes.json
#       meta.json
#       package.json
#       AGENTS.md
#       CLAUDE.md
#
# Exit codes:
#   0 — scaffolded; tarball at --output (or stdout)
#   1 — invalid --example; upstream's registry list emitted to stderr
#       (caller parses to retry with a valid name)
#   2 — usage / missing dependency
#   3 — init succeeded but scaffold was malformed (no index.html)
#   4 — init itself failed (network, telemetry consent, internal error)
#   5 — tarball creation failed (disk, permissions)

set -uo pipefail

EXAMPLE=""
RESOLUTION="landscape"
AUDIO=""
OUTPUT="/dev/stdout"

while [[ $# -gt 0 ]]; do
  case "$1" in
    -h|--help)
      sed -n '2,60p' "$0" | sed 's|^# \?||'
      exit 0
      ;;
    --example=*)    EXAMPLE="${1#*=}"; shift ;;
    --example)      EXAMPLE="${2:?--example needs a value}"; shift 2 ;;
    --resolution=*) RESOLUTION="${1#*=}"; shift ;;
    --resolution)   RESOLUTION="${2:?--resolution needs a value}"; shift 2 ;;
    --audio=*)      AUDIO="${1#*=}"; shift ;;
    --audio)        AUDIO="${2:?--audio needs a value}"; shift 2 ;;
    --output=*)     OUTPUT="${1#*=}"; shift ;;
    --output)       OUTPUT="${2:?--output needs a value}"; shift 2 ;;
    *) echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

if [[ -z "$EXAMPLE" ]]; then
  echo "missing required --example flag" >&2
  exit 2
fi

if ! command -v hyperframes >/dev/null 2>&1; then
  echo "hyperframes CLI not found in PATH" >&2
  exit 2
fi

if ! command -v tar >/dev/null 2>&1; then
  echo "tar not found in PATH (needed to package the scaffolded project)" >&2
  exit 2
fi

if [[ -n "$AUDIO" && ! -f "$AUDIO" ]]; then
  echo "--audio path not found: $AUDIO" >&2
  exit 2
fi

# Disable upstream telemetry. Idempotent per call; air-gapped operators
# get opt-out by default. Failures non-fatal — older CLI versions may
# not have the subcommand.
hyperframes telemetry disable >/dev/null 2>&1 || true

TMPDIR="$(mktemp -d -t hyperframes-init.XXXXXX)"
trap 'rm -rf "$TMPDIR"' EXIT

SCAFFOLD="$TMPDIR/scaffold"
INIT_LOG="$TMPDIR/init.log"

INIT_ARGS=(
  init "$SCAFFOLD"
  --example="$EXAMPLE"
  --resolution="$RESOLUTION"
  --non-interactive
  --skip-skills
  --skip-transcribe
)
if [[ -n "$AUDIO" ]]; then
  INIT_ARGS+=( --audio="$AUDIO" )
fi

if ! hyperframes "${INIT_ARGS[@]}" >"$INIT_LOG" 2>&1; then
  # Upstream prints registry to stdout when --example is unknown; we
  # captured both streams to the log. If the failure was registry-miss,
  # exit 1 so the caller can retry with a valid name. Otherwise exit 4.
  if grep -q 'not found in registry' "$INIT_LOG"; then
    cat "$INIT_LOG" >&2
    exit 1
  fi
  cat "$INIT_LOG" >&2
  exit 4
fi

if [[ ! -f "$SCAFFOLD/index.html" ]]; then
  echo "scaffold did not produce index.html" >&2
  echo "--- init log ---" >&2
  cat "$INIT_LOG" >&2
  exit 3
fi

# Tarball the entire scaffold directory. Using -C to set the working
# directory means archive members are relative paths (index.html,
# compositions/captions.html, …) without a leading scaffold/ component,
# which simplifies extraction on the consumer side. Gzip compression is
# ~3-5x size reduction over raw tar — the scaffold for swiss-grid is
# ~30 KB raw, ~6 KB gzipped; the bigger code-snippet examples can be
# 100s of KB raw.
if ! tar -czf "$OUTPUT" -C "$SCAFFOLD" . 2>"$TMPDIR/tar.log"; then
  echo "failed to package scaffold as tarball: see error below" >&2
  cat "$TMPDIR/tar.log" >&2
  exit 5
fi
