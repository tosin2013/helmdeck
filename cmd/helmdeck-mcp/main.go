// Command helmdeck-mcp is the stdio MCP bridge that proxies MCP JSON-RPC
// frames between an agent client (Claude Code, Claude Desktop, OpenClaw,
// Gemini CLI) and the helmdeck platform's WebSocket MCP endpoint.
//
// See ADRs 025 (client integrations) and 030 (packaging and distribution).
//
// Configuration is read from environment variables:
//
//	HELMDECK_URL    base URL of the helmdeck control plane (e.g. http://localhost:3000)
//	HELMDECK_TOKEN  bearer JWT issued from the Management UI's API Tokens panel
package main

import (
	"fmt"
	"os"
)

var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	url := os.Getenv("HELMDECK_URL")
	token := os.Getenv("HELMDECK_TOKEN")

	if url == "" || token == "" {
		fmt.Fprintln(os.Stderr, "helmdeck-mcp: HELMDECK_URL and HELMDECK_TOKEN must be set")
		os.Exit(2)
	}

	fmt.Fprintf(os.Stderr, "helmdeck-mcp %s (%s) — bridge stub, proxy not yet implemented (T303)\n", version, commit)
	fmt.Fprintf(os.Stderr, "target: %s\n", url)
	os.Exit(0)
}
