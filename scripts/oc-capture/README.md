---
title: OpenClaw transcript capture pipeline
description: Drive OpenClaw against a running helmdeck stack, capture chat-UI transcripts of pack invocations, and inject them into per-pack reference pages.
---

# OpenClaw transcript capture pipeline

Three small scripts that produce the live OpenClaw transcripts embedded
in every per-pack reference page under `docs/reference/packs/`. Run them
against a working helmdeck + OpenClaw install (see
[`docs/integrations/openclaw.md`](../../docs/integrations/openclaw.md)
for setup) to regenerate or extend the captures.

## Why this exists

The per-pack pages follow an **agent-first / developer-second** structure
established in PR #52 / PR #83: the primary worked example is what an
LLM sees through an MCP client (the agent's perspective), with `curl`
relegated to a developer-reference block. Capturing live transcripts is
the only way to keep those pages honest — synthesized examples drift,
schema mismatches stay invisible, and the docs over-promise.

## Files

| File | Role |
|---|---|
| `capture-oc.sh` | Driver. Runs one prompt through OpenClaw, fetches the resulting session JSONL, hands it to the extractor. |
| `capture-batch.sh` | Generic batch driver — runs every prompt in a `prompts/*.txt` file (with optional `--start`/`--end` for subsetting). Wraps `capture-oc.sh`. |
| `extract-oc-transcript.py` | Renders a session JSONL as markdown — prompt + tool calls + responses + agent's final reply + cost footer. |
| `inject-transcripts.py` | Replaces the `<!-- TODO(maintainer): … --> > *OpenClaw chat capture pending.*` placeholder in each per-pack page with its captured transcript. |
| `prompts/easy-cluster.txt` | The 16 prompts used in PR #83 (PR-A — browser, http, doc, fs, cmd, git, language). |
| `prompts/medium-cluster.txt` | The 12 prompts used in PR #95 (PR-B — web, vision, github). |
| `prompts/hard-cluster.txt` | The 8 prompts used in PR #101 (PR-C — repo, slides, desktop, research, content). |

## End-to-end run

```bash
# Pre-req: helmdeck + OpenClaw stack up + scripts/configure-openclaw.sh
# already wired (see docs/integrations/openclaw.md).

# 1. Capture every prompt in a cluster file (one transcript per line).
bash scripts/oc-capture/capture-batch.sh \
  --prompts scripts/oc-capture/prompts/medium-cluster.txt \
  --out /tmp/captures/oc-transcripts

# 2. Inject the captured transcripts into the matching per-pack pages.
python3 scripts/oc-capture/inject-transcripts.py /tmp/captures/oc-transcripts
```

### Single-prompt run (for one-off captures)

```bash
bash scripts/oc-capture/capture-oc.sh \
  "Use helmdeck__http-fetch to GET https://httpbin.org/headers and report the User-Agent." \
  > /tmp/captures/single.md
```

### Subset re-run (after fixing one bad transcript)

```bash
# Re-run just the 5th prompt in the medium cluster:
bash scripts/oc-capture/capture-batch.sh \
  --prompts scripts/oc-capture/prompts/medium-cluster.txt \
  --out /tmp/captures/oc-transcripts \
  --start 5 --end 5

# Then re-run the inject pass to update only the matching page:
python3 scripts/oc-capture/inject-transcripts.py /tmp/captures/oc-transcripts
```

## Why a fresh session per capture matters

`capture-oc.sh` mints a unique `--session-id` for every invocation
(format `capture-<nanos>-<pid>`). Without this, every prompt inherits
the prior turn's chat history and the model frequently answers from
memory instead of calling the tool — producing transcripts that say
"the agent answered without calling any helmdeck tool" even on
perfectly-good prompts.

This was discovered post-PR #95 when two of twelve transcripts
(`github.list_issues`, `github.post_comment`) shipped without tool
calls; the captures were re-run with the session-id fix and the cost
per capture dropped from \$0.14 → \$0.001 in the process (no prior
session bloat to send to the model).

## Prompt format

Each line in a `prompts/*.txt` file is `<page>::<prompt>` — `<page>`
maps to `docs/reference/packs/<page-with-slash>.md` (e.g. `fs/read`
becomes `docs/reference/packs/fs/read.md`), and `<prompt>` is the
exact chat-UI message OpenClaw should send.

```
fs/read::First clone https://github.com/tosin2013/helmdeck.git via …
http/fetch::Use the helmdeck__http-fetch tool to GET https://httpbin.org/headers …
```

The convention: name the tool explicitly in the prompt (`Use the
helmdeck__http-fetch tool to …`) so the model doesn't reach for
OpenClaw's built-in equivalents. Be specific about expected output
(`Tell me the artifact_key and the size in bytes.`) — that nudges the
model toward calling the helmdeck tool rather than guessing from
training.

## Environment variables

| Var | Default | Purpose |
|---|---|---|
| `OPENCLAW_COMPOSE` | `/root/openclaw/docker-compose.yml` | Path to the OpenClaw stack's compose file. |
| `OPENCLAW_GATEWAY` | `openclaw-openclaw-gateway-1` | Container name of the gateway. The session JSONL lives inside it; `docker exec cat` extracts it. |

## Cost ballpark per pack family

Observed during PR-A (#83), PR-B (#95), and PR-C (#101) runs against the
`gpt-oss-120b` chat model with `claude-haiku-4.5` as the vision model.
Numbers are per capture, not per batch — multiply by your batch size.

| Family | Cost per capture | Notes |
|---|---|---|
| `http.*`, `fs.*`, `cmd.*`, `git.*`, `language.*` | **~\$0.001–\$0.005** | Simple tool calls, short LLM round trip |
| `github.*`, `web.scrape`, `web.scrape_spa`, `doc.*`, `repo.*` | **~\$0.005–\$0.015** | Larger response payloads or one extra network round trip |
| `web.test`, `vision.click_anywhere`, `vision.fill_form_by_label` | **~\$0.05–\$0.15** | Per-step screenshot + vision-model call (max_steps × ~\$0.02) |
| `slides.narrate` | **~\$0.05** | TTS-bound — ElevenLabs cost dominates the LLM-side cost |
| `research.deep`, `content.ground` | **~\$0.01–\$0.03** | Firecrawl + synthesis LLM call |
| `slides.render`, `vision.extract_visible_text`, `desktop.run_app_and_screenshot` | **~\$0.001–\$0.005** | Single tool call, minimal model work |

PR-A (16 captures) ≈ \$0.05 total. PR-B (12 captures) ≈ \$0.50. PR-C (8 captures, includes slides.narrate + research.deep + content.ground) ≈ \$0.30.

**Before the fresh-session fix landed in PR #97**, PR-B ran for ~\$2.00 for the same 12 captures — the difference (~140×) came from shipping a 280-event session history to the model on every turn.

## Limits

- **OpenClaw's CLI agent** is the capture surface. The chat UI at
  `localhost:18789` is functionally equivalent but harder to script;
  prefer the CLI for batch runs.
- **One capture at a time.** The `openclaw-cli` container is single
  instance; concurrent invocations queue rather than parallelize.
- **Fresh sessions don't share artifacts.** A capture that needs
  output from a prior step (e.g. `fs.read` after `repo.fetch`) must
  drive the entire chain in a single capture's prompt — the model has
  to call `repo.fetch`, capture the `_session_id`, and thread it into
  the next call within that one prompt.
