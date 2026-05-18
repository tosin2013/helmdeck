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
//	  "clone_path":  "/tmp/helmdeck-clone-<rand>",
//	  "session_id":  "abcdef-1234-..."
//	}
//
// The `session_id` field is the same value the engine surfaces on the
// Result envelope (`Result.SessionID`). It is duplicated into the
// pack output so callers reading `clone_path` cannot miss it — the
// follow-on packs (`fs.*`, `cmd.run`, `git.commit`, `repo.push`) MUST
// pass it back as `_session_id` in their input or the engine will
// spin up a fresh session whose `/tmp` does not contain this clone.
// See issue #232 for the failure mode.
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
			Required: []string{"url", "commit", "clone_path", "session_id"},
			Properties: map[string]string{
				"url":            "string",
				"ref":            "string",
				"commit":         "string",
				"credential":     "string",
				"files":          "number",
				"clone_path":     "string",
				"session_id":     "string",
				"tree":           "array",
				"tree_total":     "number",
				"tree_truncated": "boolean",
				"readme":         "object",
				"entrypoints":    "array",
				"doc_hints":      "array",
				"signals":        "object",
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
		// Issue #94 — empty (refless) remotes get a fast typed error
		// instead of letting git stumble forward and the script error
		// late on `git rev-parse HEAD`. The shell scripts emit exit
		// 99 when `git ls-remote` returns no refs.
		if execRes.ExitCode == 99 {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: fmt.Sprintf("remote %s has no branches; push at least one commit before cloning", in.URL)}
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
		// Anything else is treated as a script bug. The python3 path
		// emits the full context envelope (tree/readme/entrypoints/
		// signals); the busybox fallback emits only the legacy three
		// fields. We decode into a permissive map so both shapes flow
		// through without losing fields.
		var envelope map[string]any
		if err := json.Unmarshal(execRes.Stdout, &envelope); err != nil {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("could not parse clone envelope: %v (raw: %q)", err, truncateString(string(execRes.Stdout), 256))}
		}

		// Build the response by merging handler-provided context
		// (url, ref, credential name — the pack knows these, the
		// script does not) on top of the script's envelope. Script
		// fields win for anything it computed; handler fields fill
		// in only what the script could not know.
		out := make(map[string]any, len(envelope)+4)
		for k, v := range envelope {
			out[k] = v
		}
		out["url"] = in.URL
		out["ref"] = ref
		out["credential"] = credentialName
		// Issue #232: surface session_id alongside clone_path. The same
		// value is on Result.SessionID (the engine envelope), but agents
		// that read only `output` miss it there. Putting it next to
		// clone_path inside `output` makes the trap impossible: anything
		// that consumes clone_path sees session_id in the same object.
		if ec.Session != nil {
			out["session_id"] = ec.Session.ID
		}
		return json.Marshal(out)
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
		// Issue #94 — fast-fail on empty (refless) remotes. ls-remote
		// returns exit 0 with no output when the remote has no branches;
		// downstream `git clone` then succeeds with an empty working
		// tree and `git rev-parse HEAD` errors late. Exit 99 is mapped
		// to invalid_input by the Go handler.
		"if [ -z \"$(git ls-remote --heads " + shellQuote(url) + " 2>/dev/null)\" ]; then exit 99; fi",
		"git clone " + depthFlag + shellQuote(url) + " \"$CLONE_DIR\" 1>&2",
	}
	if ref != "" {
		lines = append(lines, "git -C \"$CLONE_DIR\" checkout "+shellQuote(ref)+" 1>&2")
	}
	lines = append(lines, repoFetchEnvelopeScript)
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
	// Issue #94 — fast-fail on empty (refless) remotes before paying
	// the full clone round-trip. Same sentinel exit code as the SSH path.
	lines = append(lines,
		"if [ -z \"$(git ls-remote --heads "+shellQuote(gitURL)+" 2>/dev/null)\" ]; then exit 99; fi",
		"git clone "+depthFlag+shellQuote(gitURL)+" \"$CLONE_DIR\" 1>&2",
	)
	if ref != "" {
		lines = append(lines, "git -C \"$CLONE_DIR\" checkout "+shellQuote(ref)+" 1>&2")
	}
	lines = append(lines, repoFetchEnvelopeScript)
	return strings.Join(lines, "\n")
}

// repoFetchEnvelopeScript emits the stdout JSON envelope the Go handler
// parses after a successful clone. Preferred path: python3 inspects the
// clone and emits the full context envelope (tree + readme + entrypoints
// + signals) the agent needs to orient in the repo without a second
// tool call. Fallback path: if python3 is unavailable in the sidecar
// image, emit the legacy minimal envelope so existing callers keep
// working.
//
// The Python script runs from $CLONE_DIR. All paths in the envelope
// are relative to the clone root. Hard caps: tree ≤ 300 entries,
// readme content ≤ 4096 bytes.
const repoFetchEnvelopeScript = `COMMIT=$(git -C "$CLONE_DIR" rev-parse HEAD)
FILES_TOTAL=$(git -C "$CLONE_DIR" ls-files | wc -l | tr -d ' ')
if command -v python3 >/dev/null 2>&1; then
  CLONE_PATH="$CLONE_DIR" COMMIT="$COMMIT" FILES_TOTAL="$FILES_TOTAL" python3 <<'PYEOF'
import json, os, subprocess

clone_path = os.environ["CLONE_PATH"]
commit = os.environ["COMMIT"]
files_total = int(os.environ["FILES_TOTAL"])

os.chdir(clone_path)

tree_out = subprocess.run(
    ["git", "ls-files"], capture_output=True, text=True, check=True
).stdout
tree_all = sorted(l for l in tree_out.splitlines() if l)
TREE_CAP = 300
tree = tree_all[:TREE_CAP]
tree_truncated = len(tree_all) > TREE_CAP

# README auto-detect: case-insensitive match on common extensions at repo root.
readme = None
for entry in sorted(os.listdir(".")):
    if not os.path.isfile(entry):
        continue
    low = entry.lower()
    if low in ("readme.md", "readme.adoc", "readme.rst", "readme.txt", "readme") or (
        low.startswith("readme.") and low.rsplit(".", 1)[-1] in ("md", "adoc", "rst", "txt", "markdown")
    ):
        size = os.path.getsize(entry)
        with open(entry, "rb") as f:
            data = f.read(4096)
        readme = {
            "path": entry,
            "content": data.decode("utf-8", errors="replace"),
            "truncated": size > 4096,
        }
        break

KNOWN_ENTRYPOINTS = [
    ("Makefile", "build"), ("go.mod", "go"), ("package.json", "node"),
    ("pyproject.toml", "python"), ("Cargo.toml", "rust"), ("pom.xml", "java"),
    ("build.gradle", "gradle"), ("devfile.yaml", "devfile"),
    ("Dockerfile", "container"), ("docker-compose.yml", "compose"),
    ("docker-compose.yaml", "compose"),
    ("CLAUDE.md", "agent-doc"), ("AGENTS.md", "agent-doc"),
    ("CONTRIBUTING.md", "contributing"),
]
entrypoints = [{"path": p, "kind": k} for p, k in KNOWN_ENTRYPOINTS if os.path.exists(p)]

DOC_DIRS = ("docs", "doc", "content", "site", "book", "guide", "tutorials", "blog-posts", "examples")
CODE_DIRS = ("src", "cmd", "lib", "internal", "pkg", "app")
SOURCE_EXTS = (
    ".go", ".py", ".js", ".ts", ".tsx", ".jsx", ".rs", ".java",
    ".c", ".cpp", ".cc", ".h", ".hpp", ".rb", ".php", ".cs", ".kt", ".swift",
)
DOC_EXTS = (".md", ".adoc", ".rst")

has_docs_dir = any(os.path.isdir(d) for d in DOC_DIRS)
has_code_root_dir = any(os.path.isdir(d) for d in CODE_DIRS)
doc_file_count = sum(1 for f in tree_all if f.lower().endswith(DOC_EXTS))
code_file_count = sum(1 for f in tree_all if f.lower().endswith(SOURCE_EXTS))

signals = {
    "has_readme":      readme is not None,
    "has_docs_dir":    has_docs_dir,
    "has_code":        has_code_root_dir or code_file_count > 0,
    "doc_file_count":  doc_file_count,
    "code_file_count": code_file_count,
    "sparse":          (doc_file_count + code_file_count) < 3,
}

envelope = {
    "clone_path":     clone_path,
    "commit":         commit,
    "files":          files_total,
    "tree":           tree,
    "tree_total":     len(tree_all),
    "tree_truncated": tree_truncated,
    "readme":         readme,
    "entrypoints":    entrypoints,
    "doc_hints": [
        "README*",
        "docs/**/*.md", "docs/**/*.adoc", "docs/**/*.rst",
        "content/**/*.md", "content/**/*.adoc",
    ],
    "signals":        signals,
}
print(json.dumps(envelope))
PYEOF
else
  printf '{"clone_path":"%s","commit":"%s","files":%s}' "$CLONE_DIR" "$COMMIT" "$FILES_TOTAL"
fi`
