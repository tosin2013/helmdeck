// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

// Command helmdeck is the operator-facing CLI for managing a running
// helmdeck control plane: list capability packs, install + uninstall
// marketplace packs, browse the marketplace catalog.
//
// Wraps the same REST endpoints the Management UI uses (T810/T812
// marketplace surface, the existing /api/v1/packs catalog). Pairs with
// helmdeck-mcp (the MCP bridge binary) — same env-var conventions:
//
//	HELMDECK_URL    base URL of the helmdeck control plane
//	                (default: http://localhost:3000)
//	HELMDECK_TOKEN  bearer JWT issued from the Management UI's
//	                API Tokens panel
//
// Subcommands (T812 / #30):
//
//	helmdeck pack list                      built-in + installed marketplace
//	helmdeck pack marketplace [--refresh]   browse the marketplace catalog
//	helmdeck pack install <name>            POST /api/v1/marketplace/install
//	helmdeck pack uninstall <name>          POST /api/v1/marketplace/uninstall
//	helmdeck pack installed                 just the marketplace-installed list
//
// Every subcommand supports --json to emit raw JSON instead of the
// human-readable table — useful for shell pipelines.
//
// Distribution: shipped via goreleaser alongside the control-plane
// binary on every release tag. Operators install by downloading the
// platform-specific archive from the GH release page, OR by going
// through the existing install.sh / install --image-mode flow which
// drops helmdeck into PATH.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

// version is set at build time via -ldflags; falls back to "dev"
// during `go run` / `go build` without flags.
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "helmdeck: "+err.Error())
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		printUsage(os.Stdout)
		return nil
	}
	if args[0] == "--version" || args[0] == "-v" {
		fmt.Println("helmdeck", version)
		return nil
	}
	switch args[0] {
	case "pack":
		return runPack(args[1:])
	default:
		return fmt.Errorf("unknown subcommand %q (try `helmdeck help`)", args[0])
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintf(w, `helmdeck — operator CLI for a running helmdeck control plane

USAGE
  helmdeck <command> [args] [flags]

COMMANDS
  pack list                       list every pack the control plane has registered
  pack marketplace [--refresh]    browse the marketplace catalog
  pack install <name>             install a marketplace pack (hot-load, no restart)
  pack uninstall <name>           uninstall a marketplace pack
  pack installed                  list just the marketplace-installed packs

ENVIRONMENT
  HELMDECK_URL     control-plane base URL (default: http://localhost:3000)
  HELMDECK_TOKEN   bearer JWT (required; create one in Management UI → API Tokens)

GLOBAL FLAGS
  --json           emit raw JSON instead of the human-readable table
  --version, -v    print version
  --help, -h       show this help

EXAMPLES
  export HELMDECK_URL=http://localhost:3000
  export HELMDECK_TOKEN=eyJhbGc...
  helmdeck pack list
  helmdeck pack marketplace
  helmdeck pack install cmd.upper
  helmdeck pack installed --json | jq '.installed[] | .name'
`)
}

// --- pack subcommands ---------------------------------------------------

func runPack(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("missing subcommand for `pack` (try list, marketplace, install, uninstall, installed)")
	}
	switch args[0] {
	case "list":
		return runPackList(args[1:])
	case "marketplace", "catalog":
		return runPackCatalog(args[1:])
	case "install":
		return runPackInstall(args[1:])
	case "uninstall":
		return runPackUninstall(args[1:])
	case "installed":
		return runPackInstalled(args[1:])
	default:
		return fmt.Errorf("unknown pack subcommand %q", args[0])
	}
}

func runPackList(args []string) error {
	fs := flag.NewFlagSet("pack list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit raw JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	body, err := c.get("/api/v1/packs")
	if err != nil {
		return err
	}
	if *asJSON {
		fmt.Println(string(body))
		return nil
	}
	var packs []struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Versions    []string `json:"versions,omitempty"`
		Latest      string   `json:"latest,omitempty"`
	}
	if err := json.Unmarshal(body, &packs); err != nil {
		return fmt.Errorf("parse packs response: %w", err)
	}
	sort.Slice(packs, func(i, j int) bool { return packs[i].Name < packs[j].Name })
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tVERSION\tDESCRIPTION")
	for _, p := range packs {
		v := p.Latest
		if v == "" && len(p.Versions) > 0 {
			v = p.Versions[0]
		}
		desc := p.Description
		if len(desc) > 80 {
			desc = desc[:77] + "..."
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", p.Name, v, desc)
	}
	tw.Flush()
	fmt.Fprintf(os.Stderr, "\n%d pack(s) registered\n", len(packs))
	return nil
}

func runPackCatalog(args []string) error {
	fs := flag.NewFlagSet("pack marketplace", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit raw JSON")
	refresh := fs.Bool("refresh", false, "force a fresh fetch from the marketplace upstream")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	var body []byte
	if *refresh {
		body, err = c.post("/api/v1/marketplace/refresh", nil)
	} else {
		body, err = c.get("/api/v1/marketplace/catalog")
	}
	if err != nil {
		return err
	}
	if *asJSON {
		fmt.Println(string(body))
		return nil
	}
	var resp struct {
		Index struct {
			CatalogVersion string `json:"catalog_version"`
			Packs          []struct {
				Name        string   `json:"name"`
				Version     string   `json:"version"`
				Description string   `json:"description"`
				Author      string   `json:"author"`
				Category    string   `json:"category,omitempty"`
				Tags        []string `json:"tags,omitempty"`
			} `json:"packs"`
		} `json:"index"`
		Meta struct {
			Source      string `json:"source"`
			ResolvedURL string `json:"resolved_url,omitempty"`
			FetchedAt   string `json:"fetched_at,omitempty"`
			LastError   string `json:"last_error,omitempty"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("parse catalog response: %w", err)
	}
	fmt.Fprintf(os.Stderr, "source: %s\n", resp.Meta.Source)
	if resp.Meta.FetchedAt != "" {
		fmt.Fprintf(os.Stderr, "fetched_at: %s\n\n", resp.Meta.FetchedAt)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tVERSION\tCATEGORY\tAUTHOR\tDESCRIPTION")
	for _, p := range resp.Index.Packs {
		desc := p.Description
		if len(desc) > 60 {
			desc = desc[:57] + "..."
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", p.Name, p.Version, p.Category, p.Author, desc)
	}
	tw.Flush()
	fmt.Fprintf(os.Stderr, "\n%d pack(s) in catalog\n", len(resp.Index.Packs))
	if resp.Meta.LastError != "" {
		fmt.Fprintf(os.Stderr, "warning: last refresh failed: %s\n", resp.Meta.LastError)
	}
	return nil
}

func runPackInstall(args []string) error {
	fs := flag.NewFlagSet("pack install", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit raw JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: helmdeck pack install <name>")
	}
	name := fs.Arg(0)
	c, err := newClient()
	if err != nil {
		return err
	}
	body, err := c.post("/api/v1/marketplace/install", map[string]string{"name": name})
	if err != nil {
		return err
	}
	if *asJSON {
		fmt.Println(string(body))
		return nil
	}
	var resp struct {
		Pack struct {
			Name          string `json:"name"`
			Version       string `json:"version"`
			InstallDir    string `json:"install_dir"`
			TrustVerified bool   `json:"trust_verified"`
			TrustNote     string `json:"trust_note,omitempty"`
		} `json:"pack"`
	}
	_ = json.Unmarshal(body, &resp)
	fmt.Printf("installed %s@%s\n", resp.Pack.Name, resp.Pack.Version)
	fmt.Printf("  install_dir:    %s\n", resp.Pack.InstallDir)
	if resp.Pack.TrustVerified {
		fmt.Printf("  trust_verified: yes\n")
	} else {
		fmt.Printf("  trust_verified: no\n")
	}
	if resp.Pack.TrustNote != "" {
		fmt.Printf("  trust_note:     %s\n", resp.Pack.TrustNote)
	}
	return nil
}

func runPackUninstall(args []string) error {
	fs := flag.NewFlagSet("pack uninstall", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit raw JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: helmdeck pack uninstall <name>")
	}
	name := fs.Arg(0)
	c, err := newClient()
	if err != nil {
		return err
	}
	body, err := c.post("/api/v1/marketplace/uninstall", map[string]string{"name": name})
	if err != nil {
		return err
	}
	if *asJSON {
		fmt.Println(string(body))
		return nil
	}
	fmt.Printf("uninstalled %s\n", name)
	return nil
}

func runPackInstalled(args []string) error {
	fs := flag.NewFlagSet("pack installed", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "emit raw JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	body, err := c.get("/api/v1/marketplace/installed")
	if err != nil {
		return err
	}
	if *asJSON {
		fmt.Println(string(body))
		return nil
	}
	var resp struct {
		Installed []struct {
			Name          string `json:"name"`
			Version       string `json:"version"`
			InstalledAt   string `json:"installed_at"`
			TrustVerified bool   `json:"trust_verified"`
		} `json:"installed"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("parse installed response: %w", err)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tVERSION\tINSTALLED_AT\tTRUST")
	for _, p := range resp.Installed {
		trust := "unsigned"
		if p.TrustVerified {
			trust = "verified"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", p.Name, p.Version, p.InstalledAt, trust)
	}
	tw.Flush()
	fmt.Fprintf(os.Stderr, "\n%d marketplace pack(s) installed\n", len(resp.Installed))
	return nil
}

// --- HTTP client --------------------------------------------------------

// client wraps the basic auth + retry semantics. Constructed once per
// CLI invocation from env vars; the HTTP transport is left at default.
type client struct {
	baseURL string
	token   string
	http    *http.Client
}

func newClient() (*client, error) {
	base := strings.TrimRight(os.Getenv("HELMDECK_URL"), "/")
	if base == "" {
		base = "http://localhost:3000"
	}
	token := os.Getenv("HELMDECK_TOKEN")
	if token == "" {
		return nil, errors.New("HELMDECK_TOKEN is not set. Create an API token in Management UI → API Tokens.")
	}
	return &client{
		baseURL: base,
		token:   token,
		http:    &http.Client{Timeout: 60 * time.Second},
	}, nil
}

func (c *client) get(path string) ([]byte, error) {
	return c.request(http.MethodGet, path, nil)
}

func (c *client) post(path string, body any) ([]byte, error) {
	return c.request(http.MethodPost, path, body)
}

func (c *client) request(method, path string, body any) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.baseURL+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s %s response: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Surface the structured error envelope verbatim so the
		// operator sees the typed code (pack_not_in_catalog,
		// marketplace_install_disabled, etc.).
		return nil, fmt.Errorf("%s %s: HTTP %d: %s",
			method, path, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return respBody, nil
}
