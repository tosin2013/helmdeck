-- T301: MCP server registry.
--
-- Each row describes one *external* MCP server that helmdeck is
-- configured to talk to as a client (the inverse of T302, where
-- helmdeck IS an MCP server). The transport column is one of
-- 'stdio', 'sse', or 'websocket'. Connection details are transport-
-- specific and stored as JSON so the schema doesn't churn whenever
-- a new transport knob lands upstream.
--
-- The cached manifest (tools/list response) is also JSON; it has a
-- TTL stored alongside in cache_expires_at so the registry can
-- decide when to refetch without keeping a separate cache table.

CREATE TABLE IF NOT EXISTS mcp_servers (
    id                 TEXT PRIMARY KEY,
    name               TEXT NOT NULL UNIQUE,
    description        TEXT,
    transport          TEXT NOT NULL CHECK (transport IN ('stdio','sse','websocket')),
    config_json        TEXT NOT NULL,
    enabled            INTEGER NOT NULL DEFAULT 1,
    manifest_json      TEXT,
    cache_expires_at   TEXT,
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_mcp_servers_transport ON mcp_servers(transport);
