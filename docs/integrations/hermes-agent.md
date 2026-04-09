# Hermes Agent

> **Status:** 🟡 Documented, not yet verified end-to-end
> Hermes is the only client where helmdeck-as-LLM-gateway drops in cleanly today via the `base_url` config field. Promote to ✅ once a maintainer has walked the Phase 5.5 loop and confirmed the T607 success-rate panel ticks from a real Hermes run.

## Topology

Hermes Agent is **Topology B** — runs on the user's host alongside a local helmdeck stack. Connection is **stdio bridge** (Hermes' MCP config docs only show stdio shape), but Hermes is unique in this matrix: its `base_url` field in the model config is an explicit OpenAI-compatible escape hatch, so **helmdeck can also serve as Hermes' LLM gateway** — every chat completion Hermes issues lands in helmdeck's `provider_calls` table and shows up in the AI Providers → Model Success Rates panel.

```
host
├── helmdeck (docker compose stack on localhost:3000)
│     ├── /v1/chat/completions  (LLM gateway — sees Hermes' chat calls)
│     └── /api/v1/mcp/ws        (MCP server — exposes packs)
│
└── hermes-agent (binary in ~/.hermes)
      ├── LLM:  base_url=http://localhost:3000  → routes through helmdeck
      └── MCP:  spawns helmdeck-mcp stdio bridge → routes through helmdeck
```

Source: <https://hermes-agent.nousresearch.com/docs/integrations/providers>

## Prerequisites

- Linux, macOS, or WSL2
- Git (the only build prerequisite per Hermes docs)
- A running helmdeck stack
- An LLM provider API key (OpenRouter recommended; works with any OpenAI-compatible upstream)
- For Phase 5.5: private GitHub repo + `ssh-git` Vault credential

## 1. Install Hermes Agent

```bash
curl -fsSL https://raw.githubusercontent.com/NousResearch/hermes-agent/main/scripts/install.sh | bash
```

Verify: `hermes --version`.

Source: <https://hermes-agent.nousresearch.com/docs/getting-started/installation>

## 2. Install the helmdeck-mcp bridge

```bash
brew install tosin2013/helmdeck/helmdeck-mcp
# or
go install github.com/tosin2013/helmdeck/cmd/helmdeck-mcp@latest
```

## 3. Configure Hermes — single YAML file

Edit `~/.hermes/config.yaml`. Two sections to add: the LLM provider (routed through helmdeck) and the MCP server (also helmdeck).

```yaml
# Route Hermes' LLM calls through helmdeck so the T607 success-rate
# panel sees every chat completion. Helmdeck then dispatches to the
# real provider (OpenRouter, OpenAI, Anthropic, …) based on which
# provider key is registered in the keystore.
model: openrouter/minimax/minimax-m2.7
base_url: http://localhost:3000/v1
api_key: <your-helmdeck-jwt>

# MCP servers — helmdeck via the stdio bridge.
mcp_servers:
  helmdeck:
    command: helmdeck-mcp
    env:
      HELMDECK_URL: http://localhost:3000
      HELMDECK_TOKEN: <your-helmdeck-jwt>
```

Note: the same JWT works for both the LLM gateway and the MCP bridge — they share helmdeck's auth surface.

## 4. Verify the LLM gateway path

Run a one-shot Hermes prompt and watch helmdeck's provider_calls fill in:

```bash
hermes "What is 2 + 2? Reply in one word."
```

Then in another shell:

```bash
sqlite3 /root/helmdeck/helmdeck.db \
  'select provider, model, status, latency_ms from provider_calls order by ts desc limit 5'
```

You should see at least one `openrouter | minimax/minimax-m2.7 | success` row. Open the helmdeck UI's **AI Providers** panel and confirm the Model Success Rates section shows the row.

## 5. Walk the Phase 5.5 code-edit loop

```bash
hermes "Use the helmdeck packs to:
  1. repo.fetch git@github.com:<me>/<fixture-repo>.git using vault credential gh-deploy-key.
  2. fs.list the clone for *.md files.
  3. fs.read the README and propose a one-line edit.
  4. fs.patch to apply the edit.
  5. cmd.run 'go test ./...' in the clone.
  6. git.commit with message 'chore: helmdeck integration smoke'.
  7. repo.push back to origin."
```

**Pass criteria:**

- Commit lands on the remote branch.
- Helmdeck **Audit Logs** panel shows one entry per pack call.
- Helmdeck **AI Providers → Model Success Rates** panel shows multiple successful rows for `openrouter/minimax/minimax-m2.7` (one per LLM round trip during the loop).
- SSH private key never appears in Hermes' chat transcript.

If all four hold, this is the *strongest* end-to-end validation in the matrix because helmdeck observes both layers (LLM dispatches AND MCP tool calls). Update the status banner ✅ and flip the matrix row.

## Troubleshooting

- **`provider_calls` empty after a Hermes prompt** — Hermes is bypassing helmdeck. Confirm `base_url` is `http://localhost:3000/v1` (note the `/v1` path) and that helmdeck has the `openrouter` provider registered (`curl -H "Authorization: Bearer $JWT" http://localhost:3000/v1/models` should list it).
- **`helmdeck-mcp: command not found`** — bridge not on `PATH`. Pin the absolute path in the `command` field.
- **OpenAI-shape response shape mismatch** — helmdeck's `/v1/chat/completions` is OpenAI-compatible; if Hermes complains, check the model id matches `provider/model` (e.g. `openrouter/minimax/minimax-m2.7`, not just `minimax-m2.7`).

## References

- [Hermes Agent provider docs](https://hermes-agent.nousresearch.com/docs/integrations/providers)
- [Hermes Agent configuration](https://hermes-agent.nousresearch.com/docs/user-guide/configuration/)
