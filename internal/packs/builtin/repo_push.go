package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/security"
	"github.com/tosin2013/helmdeck/internal/session"
	"github.com/tosin2013/helmdeck/internal/vault"
)

// RepoPush (T506, ADR 023) is the mirror of repo.fetch: it pushes
// commits from a clone the agent has been editing back to the git
// remote, using the same vault-resolved SSH key flow. The agent
// never sees the key — the pack writes it to a temp file inside
// the session, sets GIT_SSH_COMMAND, runs git push, and shreds
// the key on exit.
//
// Input shape:
//
//	{
//	  "clone_path": "/tmp/helmdeck-clone-X1",  // required
//	  "remote":     "origin",                   // optional, default origin
//	  "branch":     "main",                     // optional, default current branch
//	  "force":      false                       // optional; default false
//	}
//
// Output shape:
//
//	{
//	  "url":        "git@github.com:tosin2013/helmdeck.git",
//	  "remote":     "origin",
//	  "branch":     "main",
//	  "commit":     "deadbeef...",
//	  "credential": "deploy-key"
//	}
//
// Non-fast-forward errors map to CodeSchemaMismatch with the git
// stderr surfaced verbatim — that's the closed-set error code that
// signals "the remote is in a state your client doesn't expect" per
// ADR 008.  Other git failures (auth denied, network unreachable,
// repo not found) map to CodeHandlerFailed with the same stderr
// surfacing pattern.
//
// Force pushes are accepted but flagged in the audit payload so
// operators reviewing the audit log can spot agent-driven force
// pushes after the fact. The intentional design choice is to NOT
// disallow force push at the pack layer — that's a per-credential
// policy that belongs in the vault ACL, not in the pack handler.
func RepoPush(v *vault.Store, eg *security.EgressGuard) *packs.Pack {
	return &packs.Pack{
		Name:        "repo.push",
		Version:     "v1",
		Description:  "Push committed changes from a session-local clone back to its git remote using vault-resolved credentials (SSH key or HTTPS token).",
		NeedsSession: true,
		InputSchema: packs.BasicSchema{
			Required: []string{"clone_path"},
			Properties: map[string]string{
				"clone_path": "string",
				"remote":     "string",
				"branch":     "string",
				"force":      "boolean",
				"credential": "string",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"url", "remote", "branch", "commit"},
			Properties: map[string]string{
				"url":        "string",
				"remote":     "string",
				"branch":     "string",
				"commit":     "string",
				"credential": "string",
			},
		},
		Handler: repoPushHandler(v, eg),
	}
}

type repoPushInput struct {
	ClonePath  string `json:"clone_path"`
	Remote     string `json:"remote"`
	Branch     string `json:"branch"`
	Force      bool   `json:"force"`
	Credential string `json:"credential"` // optional vault name for HTTPS PATs
}

func repoPushHandler(v *vault.Store, eg *security.EgressGuard) packs.HandlerFunc {
	return func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
		var in repoPushInput
		if err := json.Unmarshal(ec.Input, &in); err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
		}
		if strings.TrimSpace(in.ClonePath) == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "clone_path is required"}
		}
		if !isSafeClonePath(in.ClonePath) {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: "clone_path must be an absolute path under /tmp/ or /home/"}
		}
		if ec.Exec == nil {
			return nil, &packs.PackError{Code: packs.CodeSessionUnavailable, Message: "engine has no session executor"}
		}
		remote := in.Remote
		if remote == "" {
			remote = "origin"
		}

		// Step 1: discover the remote URL inside the session container.
		// `git remote get-url <remote>` is the canonical way; we use
		// the existing executor instead of making the agent re-supply
		// the URL because the clone is in the session and that's the
		// authoritative source.
		urlRes, err := ec.Exec(ctx, session.ExecRequest{
			Cmd: []string{"git", "-C", in.ClonePath, "remote", "get-url", remote},
		})
		if err != nil {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("git remote get-url: %v", err), Cause: err}
		}
		if urlRes.ExitCode != 0 {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("git remote get-url exit %d: %s", urlRes.ExitCode, strings.TrimSpace(string(urlRes.Stderr)))}
		}
		remoteURL := strings.TrimSpace(string(urlRes.Stdout))
		if remoteURL == "" {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("remote %q has no url configured", remote)}
		}

		// Step 2: parse the URL to learn the host and scheme.
		host, scheme, err := parseGitHost(remoteURL)
		if err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
		}

		// Step 3: T508 egress guard.
		if eg != nil {
			if err := eg.CheckHost(ctx, host); err != nil {
				return nil, &packs.PackError{Code: packs.CodeInvalidInput,
					Message: fmt.Sprintf("egress denied: %v", err), Cause: err}
			}
		}

		// Step 4: resolve credentials based on scheme.
		var stdinPayload []byte
		var credentialName string
		switch scheme {
		case "ssh":
			if v == nil {
				return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
					Message: "credential vault not configured (required for SSH push)"}
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
			stdinPayload = res.Plaintext
			credentialName = res.Record.Name
		case "https":
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
				stdinPayload = res.Plaintext
				credentialName = in.Credential
			}
			// No credential = public repo or previously-cached auth.
		default:
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: fmt.Sprintf("unsupported git scheme: %q", scheme)}
		}

		// Step 5: determine the branch we're pushing.
		branch := in.Branch
		if branch == "" {
			branchRes, err := ec.Exec(ctx, session.ExecRequest{
				Cmd: []string{"git", "-C", in.ClonePath, "rev-parse", "--abbrev-ref", "HEAD"},
			})
			if err != nil || branchRes.ExitCode != 0 {
				return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
					Message: fmt.Sprintf("could not detect current branch: %s", strings.TrimSpace(string(branchRes.Stderr)))}
			}
			branch = strings.TrimSpace(string(branchRes.Stdout))
			if branch == "" || branch == "HEAD" {
				return nil, &packs.PackError{Code: packs.CodeInvalidInput,
					Message: "clone is in detached HEAD state; supply branch explicitly"}
			}
		}

		// Step 6: build and run the push script.
		var script string
		if scheme == "ssh" {
			script = buildRepoPushSSHScript(in.ClonePath, remote, branch, in.Force)
		} else {
			script = buildRepoPushHTTPSScript(in.ClonePath, remote, branch, in.Force, len(stdinPayload) > 0)
		}
		execRes, err := ec.Exec(ctx, session.ExecRequest{
			Cmd:   []string{"sh", "-c", script},
			Stdin: stdinPayload,
		})
		if err != nil {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("git push exec: %v", err), Cause: err}
		}
		if execRes.ExitCode != 0 {
			stderr := string(execRes.Stderr)
			if len(stderr) > 1024 {
				stderr = stderr[:1024] + "...(truncated)"
			}
			// Non-fast-forward → schema_mismatch (ADR 008): the
			// remote ref isn't where the agent thought it was.
			// Detect via git's stable error string; falls through
			// to handler_failed for everything else.
			if isNonFastForward(stderr) {
				return nil, &packs.PackError{Code: packs.CodeSchemaMismatch,
					Message: fmt.Sprintf("non-fast-forward push to %s/%s rejected: %s", remote, branch, stderr)}
			}
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("git push exit %d: %s", execRes.ExitCode, stderr)}
		}

		// Capture the commit we pushed (stdout from the script).
		commit := strings.TrimSpace(string(execRes.Stdout))
		return json.Marshal(map[string]any{
			"url":        remoteURL,
			"remote":     remote,
			"branch":     branch,
			"commit":     commit,
			"credential": credentialName,
			"forced":     in.Force,
		})
	}
}

// buildRepoPushScript renders the shell pipeline that pushes a clone
// using a key passed on stdin. Same shred-on-exit pattern as
// buildRepoFetchScript so the key never persists past the script,
// even on failure paths.
func buildRepoPushSSHScript(clonePath, remote, branch string, force bool) string {
	pushFlag := ""
	if force {
		// --force-with-lease is the safer of the two force-push
		// modes — it refuses if the remote moved since our last
		// fetch, which protects against agent-driven races.
		pushFlag = "--force-with-lease "
	}
	lines := []string{
		"set -eu",
		"KEY_DIR=$(mktemp -d /tmp/helmdeck-key-XXXXXX)",
		"trap 'shred -u \"$KEY_DIR\"/id_rsa 2>/dev/null || rm -f \"$KEY_DIR\"/id_rsa; rmdir \"$KEY_DIR\" 2>/dev/null || true' EXIT",
		"cat > \"$KEY_DIR\"/id_rsa",
		"chmod 600 \"$KEY_DIR\"/id_rsa",
		"export GIT_SSH_COMMAND=\"ssh -i $KEY_DIR/id_rsa -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=$KEY_DIR/known_hosts -o IdentitiesOnly=yes\"",
		"git -C " + shellQuote(clonePath) + " push " + pushFlag + shellQuote(remote) + " " + shellQuote(branch) + " 1>&2",
		"git -C " + shellQuote(clonePath) + " rev-parse HEAD",
	}
	return strings.Join(lines, "\n")
}

// buildRepoPushHTTPSScript renders the push shell pipeline for HTTPS
// remotes. Same GIT_ASKPASS pattern as buildRepoFetchHTTPSScript.
func buildRepoPushHTTPSScript(clonePath, remote, branch string, force, hasCredential bool) string {
	pushFlag := ""
	if force {
		pushFlag = "--force-with-lease "
	}
	lines := []string{"set -eu"}
	if hasCredential {
		lines = append(lines,
			"CRED_DIR=$(mktemp -d /tmp/helmdeck-cred-XXXXXX)",
			"cat > \"$CRED_DIR\"/token",
			"chmod 600 \"$CRED_DIR\"/token",
			"trap 'rm -f \"$CRED_DIR\"/token; rmdir \"$CRED_DIR\" 2>/dev/null || true' EXIT",
			"printf \"#!/bin/sh\\ncat \\\"$CRED_DIR\\\"/token\\n\" > \"$CRED_DIR\"/askpass",
			"chmod 700 \"$CRED_DIR\"/askpass",
			"export GIT_ASKPASS=\"$CRED_DIR/askpass\"",
			"export GIT_TERMINAL_PROMPT=0",
		)
	}
	lines = append(lines,
		"git -C "+shellQuote(clonePath)+" push "+pushFlag+shellQuote(remote)+" "+shellQuote(branch)+" 1>&2",
		"git -C "+shellQuote(clonePath)+" rev-parse HEAD",
	)
	return strings.Join(lines, "\n")
}

// isSafeClonePath enforces the "agents can only reference clone paths
// the helmdeck packs created" rule. Accepts only the two prefixes
// helmdeck packs ever produce:
//
//   /tmp/helmdeck-clone-*  — created by repo.fetch via mktemp
//   /home/helmdeck/work/*  — designated workspace dir for future packs
//
// The git command path argument is shell-quoted before injection
// so this isn't a defense-in-depth against command injection —
// it's a defense against an LLM passing /etc/passwd or
// /home/helmdeck/.ssh/id_rsa as a clone_path and getting back
// confusing errors or unintended file access.
//
// Tightened in Phase 5.5 (fs.* pack set) — the previous version
// accepted any /tmp/* or /home/* path, which is too loose for
// fs.read/fs.write since those packs read arbitrary files inside
// the clone path.
func isSafeClonePath(p string) bool {
	if !strings.HasPrefix(p, "/") {
		return false
	}
	if strings.Contains(p, "..") {
		return false
	}
	return strings.HasPrefix(p, "/tmp/helmdeck-") ||
		strings.HasPrefix(p, "/home/helmdeck/work/")
}

// isNonFastForward sniffs git's stderr for the canonical "rejected
// non-fast-forward" message. Git's CLI strings are stable across
// modern releases — we match on the most distinctive substrings.
func isNonFastForward(stderr string) bool {
	low := strings.ToLower(stderr)
	return strings.Contains(low, "non-fast-forward") ||
		strings.Contains(low, "fetch first") ||
		strings.Contains(low, "updates were rejected")
}
