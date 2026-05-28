#!/usr/bin/env bash
# scripts/configure-claude.sh — install helmdeck's agent skills into a
# Claude Code project so the model can invoke them as `/`-skills.
#
# Claude Code loads skills from <project>/.claude/skills/<name>/SKILL.md.
# This copies every skills/<name>/SKILL.md from the helmdeck checkout into
# that location (helmdeck + helmdeck-debug + any future skill), re-stamping
# the helmdeckVersion frontmatter with the current git HEAD short-hash so the
# installed copy matches what's checked out. Idempotent — re-runs are safe.
#
# This is the structured `.claude/skills/` install path. It complements the
# system-prompt path documented in docs/integrations/claude-code.md (curl
# SKILLS.md into CLAUDE.md); use whichever your workflow prefers, or both.
#
# Usage:
#   ./scripts/configure-claude.sh                          # install all skills into ./.claude/skills
#   ./scripts/configure-claude.sh --project /path/to/proj  # target another project dir
#   ./scripts/configure-claude.sh --skill helmdeck-debug   # install just one skill (default: all)
#
# Exits 0 on success.

set -euo pipefail

# --- defaults --------------------------------------------------------------

HELMDECK_ROOT="${HELMDECK_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
SKILLS_ROOT="${SKILLS_ROOT:-${HELMDECK_ROOT}/skills}"
PROJECT_DIR="${PROJECT_DIR:-$PWD}"
SKILL_ONLY=""

# --- arg parse -------------------------------------------------------------

usage() {
	sed -n '2,20p' "$0" | sed 's/^# //;s/^#$//'
	exit 0
}

while [[ $# -gt 0 ]]; do
	case "$1" in
		--project) PROJECT_DIR="$2"; shift 2 ;;
		--skill)   SKILL_ONLY="$2"; shift 2 ;;
		-h|--help) usage ;;
		*) echo "unknown flag: $1" >&2; exit 2 ;;
	esac
done

# --- helpers ---------------------------------------------------------------

log()  { printf '\033[1;34m[configure-claude]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[configure-claude]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m[configure-claude]\033[0m %s\n' "$*" >&2; exit 1; }

# install_claude_skill re-stamps the helmdeckVersion frontmatter with the
# current git HEAD short-hash and writes the skill to
# <project>/.claude/skills/<name>/SKILL.md. Host filesystem only — no docker.
install_claude_skill() {
	local skill_name="$1" skill_file="$2" dest_dir
	dest_dir="${PROJECT_DIR}/.claude/skills/${skill_name}"
	log "installing ${skill_name} → ${dest_dir}/SKILL.md ($(wc -c < "$skill_file") bytes)"
	mkdir -p "$dest_dir"
	if git_hash="$(cd "$HELMDECK_ROOT" && git rev-parse --short HEAD 2>/dev/null)"; then
		sed -E 's/(helmdeckVersion: *")[^"]*(")/\1'"$git_hash"'\2/' "$skill_file" > "${dest_dir}/SKILL.md"
	else
		cp "$skill_file" "${dest_dir}/SKILL.md"
	fi
	local stamp
	stamp="$(grep -oE 'helmdeckVersion: *"[^"]+"' "${dest_dir}/SKILL.md" | head -1 | sed 's/.*"\([^"]*\)".*/\1/' || true)"
	[[ -n "$stamp" ]] && log "${skill_name} stamped helmdeck version ${stamp}"
}

# --- install ---------------------------------------------------------------

[[ -d "$SKILLS_ROOT" ]] || die "skills root not found at $SKILLS_ROOT — run from a helmdeck checkout"
[[ -d "$PROJECT_DIR" ]] || die "project dir not found: $PROJECT_DIR"

installed_any="false"
for skill_dir in "$SKILLS_ROOT"/*/; do
	[[ -d "$skill_dir" ]] || continue
	skill_name="$(basename "$skill_dir")"
	skill_file="${skill_dir}SKILL.md"
	[[ -f "$skill_file" ]] || { warn "no SKILL.md in ${skill_dir}, skipping"; continue; }
	if [[ -n "$SKILL_ONLY" && "$SKILL_ONLY" != "$skill_name" ]]; then
		continue
	fi
	install_claude_skill "$skill_name" "$skill_file"
	installed_any="true"
done
[[ "$installed_any" == "true" ]] || die "no skills installed (looked under ${SKILLS_ROOT}/*/SKILL.md; --skill='${SKILL_ONLY}')"

log "done — restart Claude Code (or reload the project) so it picks up .claude/skills/"
