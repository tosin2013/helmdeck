---
title: Inspect audit logs
description: Query helmdeck's three audit tables (audit_log, provider_calls, credential_usage_log) for compliance reports, debugging, and failure-pattern analysis. SQL examples + REST endpoints.
keywords: [helmdeck, audit log, provider_calls, credential_usage_log, compliance, observability, T607, SQLite]
---

# Inspect audit logs

Helmdeck writes three categories of forensic data, in three separate tables. This page covers what's where, how to query each, and the patterns operators reach for most.

| Table | What it captures | Volume |
|---|---|---|
| `audit_log` | Every API call hitting the control plane (path, method, status, actor, redacted payload) | High — one row per HTTP request |
| `provider_calls` | Every chat-completion attempt the gateway dispatches to an upstream LLM (provider, model, status, latency, tokens, fallback flag) | High — 1–3 rows per chat completion (one per attempt) |
| `credential_usage_log` | Every vault `Resolve` call (which credential, which subject, allowed/denied/no-match) | Medium — one row per pack-call that touches the vault |

All three live in `helmdeck.db` (SQLite, `/var/lib/helmdeck/helmdeck.db` inside the control-plane container).

## Where the data is

```bash
# From the host, drop into a sqlite3 shell inside the control-plane container:
docker compose -f deploy/compose/compose.yaml exec control-plane \
  sqlite3 /var/lib/helmdeck/helmdeck.db
```

Or copy the DB out for offline analysis:

```bash
# Snapshot for offline querying — safe to run on a hot DB; SQLite WAL handles it
docker compose -f deploy/compose/compose.yaml exec control-plane \
  sqlite3 /var/lib/helmdeck/helmdeck.db ".backup /tmp/helmdeck-snapshot.db"
docker compose -f deploy/compose/compose.yaml cp \
  control-plane:/tmp/helmdeck-snapshot.db ./helmdeck-snapshot.db
sqlite3 ./helmdeck-snapshot.db
```

Most operators don't need raw SQL — the **Audit Logs** UI panel covers the common filters (time range, session id, actor, event type). Drop to SQL when you need *aggregations* (counts, rates, percentile latencies) or *exports* the UI doesn't surface.

## Table 1 — `audit_log`

Every HTTP request hitting `/api/v1/*`, plus every internal forensic event. Columns:

| Column | Type | Notes |
|---|---|---|
| `id` | INTEGER PK | autoincrement |
| `ts` | TEXT (RFC3339) | UTC |
| `severity` | TEXT | `info` / `warn` / `error` |
| `event_type` | TEXT | `http_request` / `pack_executed` / `vault_resolved` / `session_created` / etc. |
| `actor_subject` | TEXT | JWT `sub` claim |
| `actor_client` | TEXT | JWT custom client claim |
| `session_id` | TEXT | Helmdeck session id (NULL for non-session events) |
| `method` | TEXT | HTTP verb |
| `path` | TEXT | request URL path |
| `status_code` | INTEGER | HTTP response status |
| `payload_json` | TEXT | redacted request/response excerpt; never includes secrets |

### Common queries

```sql
-- All non-2xx responses in the last hour, by actor
SELECT actor_subject, status_code, path, COUNT(*) AS n
FROM audit_log
WHERE ts >= datetime('now', '-1 hour')
  AND status_code >= 400
GROUP BY actor_subject, status_code, path
ORDER BY n DESC;

-- Pack invocation rate per actor over the last 24h
SELECT actor_subject,
       SUBSTR(path, LENGTH('/api/v1/packs/') + 1) AS pack,
       COUNT(*) AS calls
FROM audit_log
WHERE ts >= datetime('now', '-1 day')
  AND path LIKE '/api/v1/packs/%'
  AND method = 'POST'
GROUP BY actor_subject, pack
ORDER BY calls DESC;

-- Reconstruct one session's timeline
SELECT ts, event_type, method, path, status_code
FROM audit_log
WHERE session_id = '<session-id>'
ORDER BY ts ASC;

-- Compliance export — every action by a specific subject in a date range
SELECT ts, event_type, method, path, status_code, payload_json
FROM audit_log
WHERE actor_subject = 'alice@example.com'
  AND ts BETWEEN '2026-04-01' AND '2026-05-01'
ORDER BY ts ASC;
```

Indexes are tuned for `ts`, `session_id`, `event_type`, `actor_subject` lookups. Other column predicates (e.g. `path LIKE '%scrape%'`) will scan.

## Table 2 — `provider_calls`

Every dispatch the LLM gateway makes to an upstream — primary attempts, fallback attempts, successes, failures. Columns:

| Column | Type | Notes |
|---|---|---|
| `id` | INTEGER PK | autoincrement |
| `ts` | TEXT (RFC3339) | UTC |
| `provider` | TEXT | `anthropic` / `openai` / `openrouter` / etc. |
| `model` | TEXT | upstream model id |
| `status` | TEXT | `success` / `error` |
| `latency_ms` | INTEGER | wall-clock from dispatch to last byte |
| `error_code` | TEXT | non-empty only when `status='error'`. Closed set: `network` / `timeout` / `http_4xx` / `http_5xx` / `decode` / `unknown_provider` |
| `fallback_used` | INTEGER | 1 if this row is a fallback attempt (not the primary), else 0 |
| `prompt_tokens`, `completion_tokens`, `total_tokens` | INTEGER | usage rollups (0 if upstream didn't report) |

This table backs the **AI Providers → Model Success Rates** panel (T607). Compound index on `(provider, model, ts)`.

### Common queries

```sql
-- Per-(provider, model) success rate over the last 24h
SELECT provider, model,
       COUNT(*) AS attempts,
       SUM(CASE WHEN status='success' THEN 1 ELSE 0 END) AS ok,
       ROUND(100.0 * SUM(CASE WHEN status='success' THEN 1 ELSE 0 END) / COUNT(*), 1) AS pct,
       AVG(latency_ms) AS avg_ms,
       MAX(latency_ms) AS max_ms
FROM provider_calls
WHERE ts >= datetime('now', '-1 day')
GROUP BY provider, model
ORDER BY attempts DESC;

-- How often is the primary failing badly enough to trip the fallback chain?
SELECT model,
       SUM(CASE WHEN fallback_used = 0 THEN 1 ELSE 0 END) AS primary_attempts,
       SUM(CASE WHEN fallback_used = 1 THEN 1 ELSE 0 END) AS fallback_attempts,
       ROUND(100.0 * SUM(fallback_used) / COUNT(*), 1) AS fallback_pct
FROM provider_calls
WHERE ts >= datetime('now', '-1 day')
GROUP BY model
HAVING fallback_attempts > 0
ORDER BY fallback_pct DESC;

-- Error breakdown — what kinds of failures dominate?
SELECT error_code, COUNT(*) AS n
FROM provider_calls
WHERE status = 'error'
  AND ts >= datetime('now', '-1 day')
GROUP BY error_code
ORDER BY n DESC;

-- Cost approximation — total tokens by (provider, model) for the month
SELECT provider, model,
       SUM(prompt_tokens) AS prompt,
       SUM(completion_tokens) AS completion,
       SUM(total_tokens) AS total
FROM provider_calls
WHERE ts >= datetime('now', 'start of month')
GROUP BY provider, model
ORDER BY total DESC;
```

The same data is available over REST (`GET /api/v1/providers/stats?since=24h`) — see [Configure LLM providers](./configure-llm-providers.md).

## Table 3 — `credential_usage_log`

Every `vault.Resolve()` call. Append-only — survives credential deletion so the forensic trail outlives the credential. Columns:

| Column | Type | Notes |
|---|---|---|
| `id` | INTEGER PK | autoincrement |
| `credential_id` | TEXT | NOT a foreign key (survives credential deletion) |
| `actor_subject` | TEXT | the JWT `sub` claim of the caller |
| `actor_client` | TEXT | JWT client claim |
| `host_matched` | TEXT | which host the resolve was for |
| `path_matched` | TEXT | which path |
| `result` | TEXT | `allowed` / `denied` / `no_match` / `expired` |
| `ts` | TEXT | UTC |

### Common queries

```sql
-- Every denied resolve in the last 24h — usually a missing ACL grant
SELECT credential_id, actor_subject, host_matched, COUNT(*) AS n
FROM credential_usage_log
WHERE result = 'denied'
  AND ts >= datetime('now', '-1 day')
GROUP BY credential_id, actor_subject, host_matched
ORDER BY n DESC;

-- All resolves of a specific credential, time-ordered
SELECT ts, actor_subject, host_matched, result
FROM credential_usage_log
WHERE credential_id = '<credential-id>'
ORDER BY ts DESC
LIMIT 100;

-- Subjects that touched ANY credential in the last week (compliance question:
-- "who used vault credentials between dates X and Y?")
SELECT DISTINCT actor_subject, COUNT(*) AS resolves
FROM credential_usage_log
WHERE ts BETWEEN '2026-04-01' AND '2026-04-30'
GROUP BY actor_subject
ORDER BY resolves DESC;

-- Which credentials are no longer being used? (Candidates for cleanup)
SELECT credential_id,
       MAX(ts) AS last_used,
       COUNT(*) AS lifetime_resolves
FROM credential_usage_log
GROUP BY credential_id
ORDER BY last_used ASC;
```

The per-credential REST surface (`GET /api/v1/vault/credentials/{id}/usage`) covers the single-credential view — drop to SQL when you need cross-credential aggregates.

## Exporting for compliance

For audit responses or compliance reports, dump the relevant rows as JSON:

```bash
docker compose -f deploy/compose/compose.yaml exec control-plane \
  sqlite3 /var/lib/helmdeck/helmdeck.db \
  -json \
  "SELECT * FROM audit_log
   WHERE actor_subject = 'alice@example.com'
     AND ts BETWEEN '2026-04-01' AND '2026-05-01'" \
  > alice-april.json
```

Or as CSV:

```bash
docker compose -f deploy/compose/compose.yaml exec control-plane \
  sqlite3 /var/lib/helmdeck/helmdeck.db \
  -csv -header \
  "SELECT ts, provider, model, status, latency_ms, total_tokens
   FROM provider_calls
   WHERE ts >= datetime('now', '-30 days')" \
  > provider-calls-30d.csv
```

Both flags work with the version of SQLite shipped in the control-plane container.

## Retention

There's **no automatic retention/pruning** today. The three tables grow unbounded. At realistic volumes (10k pack calls/day → ~90k audit rows + ~30k provider_calls + ~40k vault_usage rows per day), SQLite is comfortable for years. Periodic `VACUUM` reclaims space after deletes if you implement your own retention policy:

```sql
-- Example: keep 90 days of audit_log
DELETE FROM audit_log WHERE ts < datetime('now', '-90 days');
VACUUM;
```

Run this from a cron job or systemd timer. No code change required. Automated retention with configurable windows is on the v1.x roadmap.

## Performance notes

- All three tables have indexes tuned for time-window queries. Predicates on indexed columns (`ts`, `session_id`, `event_type`, `actor_subject` for `audit_log`; `(provider, model, ts)` for `provider_calls`; `credential_id`, `ts` for `credential_usage_log`) use the index.
- Predicates on JSON inside `audit_log.payload_json` will scan. For frequent payload-querying patterns, denormalize the field into its own column and add an index — open a feature request at [github.com/tosin2013/helmdeck/issues](https://github.com/tosin2013/helmdeck/issues) if there's one you'd reach for.
- A single SQLite reader doesn't block writers (WAL mode is on). Concurrent SELECT during a write-heavy workload is safe.

## Known limitations

- **No automatic retention** — see above. Manual cron-based cleanup until v1.x.
- **No row-level access control** — anyone with read access to `helmdeck.db` (file-system or via a JWT with a sufficiently broad scope) can see all audit data. The vault encryption is for *credential plaintexts*, not for the audit metadata itself.
- **No external SIEM export** — getting audit data into Splunk / Datadog / Elasticsearch requires either polling the REST endpoints or scraping the SQLite file. A dedicated OpenTelemetry-shaped export pipeline is on the roadmap (ADR 013 covers GenAI tracing; full audit export is a separate track).
- **Schema migration on upgrade is automatic but additive-only** — `internal/store/migrations/` only adds columns/tables, never drops or renames. This means downgrades work (a v0.10 binary can read a v0.11 DB), but it also means historical data shape can drift slightly across versions. Always read the migration files for the version you're querying against.

## Related

- [Manage credentials in the vault](./manage-vault-credentials.md) — for the vault REST surface that produces these `credential_usage_log` rows
- [Configure LLM providers](./configure-llm-providers.md) — for the gateway that produces `provider_calls`
- [Architecture overview §2 Request flows](../reference/architecture.md#2-request-flows) — where audit writes happen in the request flow (always after the side effect, always unconditional)
- [ADR 013 — OpenTelemetry with GenAI conventions](../adrs/013-opentelemetry-with-genai-conventions.md) — the longer-term observability story
