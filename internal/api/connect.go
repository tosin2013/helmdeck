package api

import (
	"net/http"
	"strings"
)

// registerConnectRoutes mounts GET /api/v1/connect/{client} (T309).
//
// Each handler returns the JSON snippet an operator pastes into the
// named client's MCP configuration so it spawns the helmdeck-mcp
// stdio bridge with the right environment. These are stubs — the
// Phase 6 Management UI (T612) will consume the same generators
// behind one-click "Connect" buttons.
//
// The endpoint accepts two optional query parameters:
//
//	?url=    overrides HELMDECK_URL (defaults to the request's host)
//	?token=  overrides HELMDECK_TOKEN (defaults to the literal
//	         placeholder "REPLACE_ME" — operators paste their own
//	         token from the API Tokens panel)
//
// Supported clients: claude-code, claude-desktop, openclaw, gemini-cli.
func registerConnectRoutes(mux *http.ServeMux, deps Deps) {
	mux.HandleFunc("GET /api/v1/connect/{client}", func(w http.ResponseWriter, r *http.Request) {
		client := r.PathValue("client")
		url := r.URL.Query().Get("url")
		if url == "" {
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}
			url = scheme + "://" + r.Host
		}
		token := r.URL.Query().Get("token")
		if token == "" {
			token = "REPLACE_ME"
		}

		snippet, ok := connectSnippet(client, url, token)
		if !ok {
			writeError(w, http.StatusNotFound, "unknown_client",
				"supported clients: claude-code, claude-desktop, openclaw, gemini-cli")
			return
		}
		writeJSON(w, http.StatusOK, snippet)
	})
}

// connectSnippet returns the per-client config object. The shape
// matches each client's documented MCP server entry — operators
// merge it into their existing config file (path noted in the
// "install_path" field for the UI's benefit).
func connectSnippet(client, url, token string) (map[string]any, bool) {
	env := map[string]string{
		"HELMDECK_URL":   url,
		"HELMDECK_TOKEN": token,
	}
	switch strings.ToLower(client) {
	case "claude-code":
		// claude_desktop_config.json-style entry; Claude Code reads
		// the same schema from ~/.config/claude/mcp.json.
		return map[string]any{
			"client":       "claude-code",
			"install_path": "~/.config/claude/mcp.json",
			"config": map[string]any{
				"mcpServers": map[string]any{
					"helmdeck": map[string]any{
						"command": "helmdeck-mcp",
						"env":     env,
					},
				},
			},
		}, true
	case "claude-desktop":
		return map[string]any{
			"client":       "claude-desktop",
			"install_path": "~/Library/Application Support/Claude/claude_desktop_config.json",
			"config": map[string]any{
				"mcpServers": map[string]any{
					"helmdeck": map[string]any{
						"command": "helmdeck-mcp",
						"env":     env,
					},
				},
			},
		}, true
	case "openclaw":
		return map[string]any{
			"client":       "openclaw",
			"install_path": "~/.openclaw/mcp.toml",
			"config": map[string]any{
				"servers": []any{
					map[string]any{
						"name":    "helmdeck",
						"command": "helmdeck-mcp",
						"env":     env,
					},
				},
			},
		}, true
	case "gemini-cli":
		return map[string]any{
			"client":       "gemini-cli",
			"install_path": "~/.gemini/settings.json",
			"config": map[string]any{
				"mcpServers": map[string]any{
					"helmdeck": map[string]any{
						"command": "helmdeck-mcp",
						"env":     env,
					},
				},
			},
		}, true
	}
	return nil, false
}
