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
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/tosin2013/helmdeck/internal/bridge"
)

var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg := bridge.Config{
		URL:     os.Getenv("HELMDECK_URL"),
		Token:   os.Getenv("HELMDECK_TOKEN"),
		Version: version,
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
	}
	fmt.Fprintf(os.Stderr, "helmdeck-mcp %s (%s)\n", version, commit)

	if err := bridge.Run(ctx, cfg); err != nil {
		if errors.Is(err, bridge.ErrMissingConfig) {
			fmt.Fprintln(os.Stderr, "helmdeck-mcp: HELMDECK_URL and HELMDECK_TOKEN must be set")
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "helmdeck-mcp: %v\n", err)
		os.Exit(1)
	}
}
