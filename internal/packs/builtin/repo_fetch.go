package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/security"
	"github.com/tosin2013/helmdeck/internal/session"
	"github.com/tosin2013/helmdeck/internal/vault"
)

// RepoFetch (T505 + T504, ADR 022) clones a git repository into the
// session container using vault-resolved credentials. The agent never
// sees the credential — the pack writes it to a temporary file inside
// the session, runs git with the appropriate transport config, then
// deletes the credential file before returning.
//
// Input shape:
//
//	{
//	  "url":        "https://github.com/owner/repo.git",   // required
//	  "ref":        "main",                                 // optional, default HEAD
//	  "depth":      1,                                      // optional, shallow clone
//	  "credential": "github-token"                          // optional, vault name for HTTPS PATs
//	}
//
// Output shape:
//
//	{
//	  "url":         "https://github.com/owner/repo.git",
//	  "ref":         "main",
//	  "commit":      "abc1234...",
//	  "credential":  "github-token",
//	  "files":       42,
//	  "clone_path":  "/tmp/helmdeck-clone-<rand>"
//	}
//
// URL forms accepted:
//
//	git@github.com:owner/repo.git           — SSH (scp-like)
//	ssh://git@github.com/owner/repo.git     — SSH (URL form)
//	https://github.com/owner/repo.git       — HTTPS (public or with vault credential)
//
// For SSH clones, the pack resolves an SSH key from the vault by
// host match and uses GIT_SSH_COMMAND. For HTTPS clones, if a
// credential name is provided, the pack resolves it from the vault
// and injects it via GIT_ASKPASS so the token never appears in the
// URL or in the git process environment. Public HTTPS repos can be
// cloned without any credential.
func RepoFetch(v *vault.Store, eg *security.EgressGuard) *packs.Pack {
	return &packs.Pack{
		Name:        "repo.fetch",
		Version:     "v1",
		Description:     "Clone a git repository inside the session container using vault-resolved credentials (SSH key or HTTPS token).",
		NeedsSession:    true,
		PreserveSession: true, // session persists for follow-on packs (fs.*, cmd.run, git.commit, repo.push) to reuse via _session_id
		InputSchema: packs.BasicSchema{
			Required: []string{"url"},
			Properties: map[string]string{
				"url":        "string",
				"ref":        "string",
				"depth":      "number",
				"credential": "string",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"url", "commit", "clone_path"},
			Properties: map[string]string{
				"url":        "string",
				"ref":        "string",
				"commit":     "string",
				"credential": "string",
				"files":      "number",
				"clone_path": "string",
			},
		},
		Handler: repoFetchHandler(v, eg),
	}
}

type repoFetchInput struct {
	URL        string `json:"url"`
	Ref        string `json:"ref"`
	Depth      int    `json:"depth"`
	Credential string `json:"credential"` // optional vault name for HTTPS PATs
}

func repoFetchHandler(v *vault.Store, eg *security.EgressGuard) packs.HandlerFunc {
	return func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
		var in repoFetchInput
		if err := json.Unmarshal(ec.Input, &in); err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
		}
		if strings.TrimSpace(in.URL) == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "url is required"}
		}
		if ec.Exec == nil {
			return nil, &packs.PackError{Code: packs.CodeSessionUnavailable, Message: "engine has no session executor"}
		}

		host, scheme, err := parseGitHost(in.URL)
		if err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
		}

		// T508: SSRF / metadata-IP guard.
		if eg != nil {
			if err := eg.CheckHost(ctx, host); err != nil {
				return nil, &packs.PackError{Code: packs.CodeInvalidInput,
					Message: fmt.Sprintf("egress denied: %v", err), Cause: err}
			}
		}

		ref := in.Ref
		depth := in.Depth
		if depth < 0 {
			depth = 0
		}

		var script string
		var stdinPayload []byte
		var credentialName string

		switch scheme {
		case "ssh":
			// SSH path: resolve an SSH key from the vault by host match.
			// Vault is required for SSH clones — no key = can't authenticate.
			if v == nil {
				return nil, &packs.PackError{Code: packs.CodeHandlerFailed, Message: "credential vault not configured (required for SSH clones)"}
			}
			actor := vault.Actor{Subject: "*"}
			res, err := v.Resolve(ctx, actor, host, "")
			if err != nil {
				if errors.Is(err, vault.ErrNoMatch) {
					return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
						Message: fmt.Sprintf("no vault credential matches host %q", host)}
				}
				return nil, &packs.PackError{Code: packs.CodeHandlerFailed, Message: err.Error(), Cause: err}
			}
			if res.Record.Type != vault.TypeSSH {
				return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
					Message: fmt.Sprintf("vault credential %q is type %q, expected ssh", res.Record.Name, res.Record.Type)}
			}
			script = buildRepoFetchSSHScript(in.URL, ref, depth)
			stdinPayload = res.Plaintext
			credentialName = res.Record.Name

		case "https":
			// HTTPS path (T504): public repos clone with no credential.
			// Private repos use a vault-stored PAT injected via GIT_ASKPASS.
			if in.Credential != "" && v != nil {
				actor := vault.Actor{Subject: "*"}
				res, err := v.ResolveByName(ctx, actor, in.Credential)
				if err != nil {
					if errors.Is(err, vault.ErrNoMatch) {
						return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
							Message: fmt.Sprintf("vault credential %q not found", in.Credential)}
					}
					return nil, &packs.PackError{Code: packs.CodeHandlerFailed, Message: err.Error(), Cause: err}
				}
				script = buildRepoFetchHTTPSScript(in.URL, ref, depth, true)
				stdinPayload = res.Plaintext
				credentialName = in.Credential
			} else {
				// Public repo — no credential needed.
				script = buildRepoFetchHTTPSScript(in.URL, ref, depth, false)
			}

		default:
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: fmt.Sprintf("unsupported git scheme: %q", scheme)}
		}

		execRes, err := ec.Exec(ctx, session.ExecRequest{
			Cmd:   []string{"sh", "-c", script},
			Stdin: stdinPayload,
		})
		if err != nil {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("git clone exec: %v", err)}
		}
		if execRes.ExitCode != 0 {
			stderr := string(execRes.Stderr)
			if len(stderr) > 1024 {
				stderr = stderr[:1024] + "...(truncated)"
			}
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("git clone exit %d: %s", execRes.ExitCode, stderr)}
		}

		// Parse the JSON envelope the script writes to stdout.
		// Anything else is treated as a script bug.
		var envelope struct {
			ClonePath string `json:"clone_path"`
			Commit    string `json:"commit"`
			Files     int    `json:"files"`
		}
		if err := json.Unmarshal(execRes.Stdout, &envelope); err != nil {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("could not parse clone envelope: %v (raw: %q)", err, truncateString(string(execRes.Stdout), 256))}
		}

		return json.Marshal(map[string]any{
			"url":        in.URL,
			"ref":        ref,
			"commit":     envelope.Commit,
			"credential": credentialName,
			"files":      envelope.Files,
			"clone_path": envelope.ClonePath,
		})
	}
}

// parseGitHost extracts the host portion of a git URL and identifies
// the transport scheme. Supports the three forms documented on the
// pack: scp-like (git@host:owner/repo), ssh:// URL, and https:// URL.
func parseGitHost(rawURL string) (host, scheme string, err error) {
	// scp-like form: user@host:path. The colon distinguishes it from
	// a normal URL because the part after `user@` doesn't have //.
	if !strings.Contains(rawURL, "://") {
		at := strings.Index(rawURL, "@")
		colon := strings.Index(rawURL, ":")
		if at < 0 || colon < at {
			return "", "", fmt.Errorf("malformed git url: %s", rawURL)
		}
		return rawURL[at+1 : colon], "ssh", nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", "", err
	}
	if u.Hostname() == "" {
		return "", "", fmt.Errorf("missing host in url: %s", rawURL)
	}
	switch u.Scheme {
	case "ssh", "git+ssh":
		return u.Hostname(), "ssh", nil
	case "https", "http":
		return u.Hostname(), "https", nil
	default:
		return "", "", fmt.Errorf("unsupported git scheme: %s", u.Scheme)
	}
}

// buildRepoFetchSSHScript renders the shell pipeline that clones via SSH
// using a key passed on stdin.
func buildRepoFetchSSHScript(url, ref string, depth int) string {
	depthFlag := ""
	if depth > 0 {
		depthFlag = fmt.Sprintf("--depth %d ", depth)
	}
	lines := []string{
		"set -eu",
		"KEY_DIR=$(mktemp -d /tmp/helmdeck-key-XXXXXX)",
		"CLONE_DIR=$(mktemp -d /tmp/helmdeck-clone-XXXXXX)",
		"trap 'shred -u \"$KEY_DIR\"/id_rsa 2>/dev/null || rm -f \"$KEY_DIR\"/id_rsa; rmdir \"$KEY_DIR\" 2>/dev/null || true' EXIT",
		"cat > \"$KEY_DIR\"/id_rsa",
		"chmod 600 \"$KEY_DIR\"/id_rsa",
		"export GIT_SSH_COMMAND=\"ssh -i $KEY_DIR/id_rsa -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=$KEY_DIR/known_hosts -o IdentitiesOnly=yes\"",
		"git clone " + depthFlag + shellQuote(url) + " \"$CLONE_DIR\" 1>&2",
	}
	if ref != "" {
		lines = append(lines, "git -C \"$CLONE_DIR\" checkout "+shellQuote(ref)+" 1>&2")
	}
	lines = append(lines,
		"COMMIT=$(git -C \"$CLONE_DIR\" rev-parse HEAD)",
		"FILES=$(git -C \"$CLONE_DIR\" ls-files | wc -l | tr -d ' ')",
		"printf '{\"clone_path\":\"%s\",\"commit\":\"%s\",\"files\":%s}' \"$CLONE_DIR\" \"$COMMIT\" \"$FILES\"",
	)
	return strings.Join(lines, "\n")
}

// buildRepoFetchHTTPSScript renders the shell pipeline that clones via
// HTTPS. When hasCredential is true, the script reads a PAT from stdin
// and injects it via GIT_ASKPASS — a tiny helper script that echoes the
// token as the git password. The token never appears in the URL, the
// process environment, or git's trace output.
func buildRepoFetchHTTPSScript(gitURL, ref string, depth int, hasCredential bool) string {
	depthFlag := ""
	if depth > 0 {
		depthFlag = fmt.Sprintf("--depth %d ", depth)
	}
	lines := []string{
		"set -eu",
		"CLONE_DIR=$(mktemp -d /tmp/helmdeck-clone-XXXXXX)",
	}
	if hasCredential {
		// Write a GIT_ASKPASS helper that echoes the token read from
		// stdin. The helper is a one-liner shell script invoked by git
		// whenever it needs a password. The token is stored in a temp
		// file with 0600 permissions and cleaned up in a trap.
		lines = append(lines,
			"CRED_DIR=$(mktemp -d /tmp/helmdeck-cred-XXXXXX)",
			"cat > \"$CRED_DIR\"/token",
			"chmod 600 \"$CRED_DIR\"/token",
			"trap 'rm -f \"$CRED_DIR\"/token; rmdir \"$CRED_DIR\" 2>/dev/null || true' EXIT",
			// GIT_ASKPASS is a program git invokes to get the password.
			// It receives a prompt as $1 and must print the password to
			// stdout. We write a tiny shell script that cats the token.
			"printf \"#!/bin/sh\\ncat \\\"$CRED_DIR\\\"/token\\n\" > \"$CRED_DIR\"/askpass",
			"chmod 700 \"$CRED_DIR\"/askpass",
			"export GIT_ASKPASS=\"$CRED_DIR/askpass\"",
			// Prevent git from using any system credential helpers.
			"export GIT_TERMINAL_PROMPT=0",
		)
	}
	lines = append(lines,
		"git clone "+depthFlag+shellQuote(gitURL)+" \"$CLONE_DIR\" 1>&2",
	)
	if ref != "" {
		lines = append(lines, "git -C \"$CLONE_DIR\" checkout "+shellQuote(ref)+" 1>&2")
	}
	lines = append(lines,
		"COMMIT=$(git -C \"$CLONE_DIR\" rev-parse HEAD)",
		"FILES=$(git -C \"$CLONE_DIR\" ls-files | wc -l | tr -d ' ')",
		"printf '{\"clone_path\":\"%s\",\"commit\":\"%s\",\"files\":%s}' \"$CLONE_DIR\" \"$COMMIT\" \"$FILES\"",
	)
	return strings.Join(lines, "\n")
}
