// Phase 5.5 fs/git/cmd pack set — the missing primitives that turn
// repo.fetch into a working code-edit loop. Six packs sharing one
// file because each is a thin wrapper around session.Executor and
// they all use the same path-safety helper.
//
//   fs.read   { clone_path, path }                    → { content, sha256, size }
//   fs.write  { clone_path, path, content }           → { sha256, size }
//   fs.patch  { clone_path, path, search, replace }   → { applied, sha256 }
//   fs.list   { clone_path, path?, glob? }            → { files: [...] }
//   cmd.run   { clone_path, command, stdin? }         → { stdout, stderr, exit_code }
//   git.commit{ clone_path, message, all? }           → { commit, files_changed }
//
// Path safety: every pack takes a clone_path (validated by
// isSafeClonePath — under /tmp/helmdeck-* or /home/helmdeck/work/*)
// PLUS a relative path inside the clone. The relative path must not
// start with `/`, must not contain `..`, and is joined with
// clone_path before any shell command runs. This bounds the LLM's
// reach to files under directories it created via repo.fetch.
//
// Output sizes are capped — fs.read at 8 MiB, fs.list at 5000
// entries, cmd.run combined stdout+stderr at 8 MiB. Exceeding any
// cap returns a typed error so the LLM knows to narrow its query
// rather than receive a truncated response with no signal.

package builtin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/session"
)

const (
	maxFsReadBytes  = 8 << 20  // 8 MiB
	maxFsListFiles  = 5000
	maxCmdOutBytes  = 8 << 20  // 8 MiB combined stdout+stderr
)

// --- shared helpers ------------------------------------------------------

// safeJoin validates a relative path and joins it onto a clone_path
// that has already passed isSafeClonePath. Rejects absolute paths,
// any "..", and any backslash (which would let a Windows-trained
// LLM smuggle a separator past the parser).
func safeJoin(clonePath, rel string) (string, *packs.PackError) {
	if !isSafeClonePath(clonePath) {
		return "", &packs.PackError{Code: packs.CodeInvalidInput,
			Message: "clone_path must be an absolute path under /tmp/helmdeck- or /home/helmdeck/work/"}
	}
	if rel == "" {
		return clonePath, nil
	}
	if strings.HasPrefix(rel, "/") {
		return "", &packs.PackError{Code: packs.CodeInvalidInput,
			Message: "path must be relative to clone_path"}
	}
	if strings.Contains(rel, "..") || strings.Contains(rel, "\\") {
		return "", &packs.PackError{Code: packs.CodeInvalidInput,
			Message: "path must not contain .. or backslash"}
	}
	joined := path.Join(clonePath, rel)
	// Defense in depth: after Clean, the result must still start
	// with clone_path. Rejects edge cases like a symlink-shaped
	// relative path that path.Join doesn't catch.
	if !strings.HasPrefix(joined, clonePath+"/") && joined != clonePath {
		return "", &packs.PackError{Code: packs.CodeInvalidInput,
			Message: "resolved path escapes clone_path"}
	}
	return joined, nil
}

// runShell wraps a sh -c invocation with the shellQuote helper used
// by the desktop and repo packs. Provided as a one-liner so each
// fs pack handler can read like a description rather than a quote
// management exercise.
func runShell(ctx context.Context, ec *packs.ExecutionContext, script string, stdin []byte) (session.ExecResult, error) {
	return ec.Exec(ctx, session.ExecRequest{
		Cmd:   []string{"sh", "-c", script},
		Stdin: stdin,
	})
}

// --- fs.read -------------------------------------------------------------

// FSRead exposes "read this file" as a typed pack call. Returns the
// raw content plus a sha256 the LLM can use to verify the file
// hasn't changed under it before issuing a follow-up fs.write.
func FSRead() *packs.Pack {
	return &packs.Pack{
		Name:        "fs.read",
		Version:     "v1",
		Description: "Read a file from a session-local clone path.",
		NeedsSession: true,
		InputSchema: packs.BasicSchema{
			Required: []string{"clone_path", "path"},
			Properties: map[string]string{
				"clone_path": "string",
				"path":       "string",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"content", "sha256", "size"},
			Properties: map[string]string{
				"content": "string",
				"sha256":  "string",
				"size":    "number",
			},
		},
		Handler: fsReadHandler,
	}
}

type fsPathInput struct {
	ClonePath string `json:"clone_path"`
	Path      string `json:"path"`
}

func fsReadHandler(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
	var in fsPathInput
	if err := json.Unmarshal(ec.Input, &in); err != nil {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
	}
	if ec.Exec == nil {
		return nil, &packs.PackError{Code: packs.CodeSessionUnavailable, Message: "engine has no session executor"}
	}
	full, perr := safeJoin(in.ClonePath, in.Path)
	if perr != nil {
		return nil, perr
	}
	// stat first to bail out before reading a 4 GB file. wc -c
	// gives the byte count without buffering anything.
	statRes, err := runShell(ctx, ec,
		"wc -c < "+shellQuote(full), nil)
	if err != nil {
		return nil, &packs.PackError{Code: packs.CodeHandlerFailed, Message: err.Error(), Cause: err}
	}
	if statRes.ExitCode != 0 {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: fmt.Sprintf("file not readable: %s", strings.TrimSpace(string(statRes.Stderr)))}
	}
	size, _ := strconv.ParseInt(strings.TrimSpace(string(statRes.Stdout)), 10, 64)
	if size > maxFsReadBytes {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: fmt.Sprintf("file is %d bytes, exceeds %d byte cap", size, maxFsReadBytes)}
	}
	res, err := runShell(ctx, ec, "cat "+shellQuote(full), nil)
	if err != nil {
		return nil, &packs.PackError{Code: packs.CodeHandlerFailed, Message: err.Error(), Cause: err}
	}
	if res.ExitCode != 0 {
		return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("cat exit %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))}
	}
	sum := sha256.Sum256(res.Stdout)
	return json.Marshal(map[string]any{
		"content": string(res.Stdout),
		"sha256":  hex.EncodeToString(sum[:]),
		"size":    len(res.Stdout),
	})
}

// --- fs.write ------------------------------------------------------------

// FSWrite replaces a file's contents wholesale. The agent supplies
// the new content as a string; the pack pipes it via stdin so
// payloads up to the executor's stdin limit work without quoting.
func FSWrite() *packs.Pack {
	return &packs.Pack{
		Name:        "fs.write",
		Version:     "v1",
		Description: "Write a file to a session-local clone path (creates parents as needed).",
		NeedsSession: true,
		InputSchema: packs.BasicSchema{
			Required: []string{"clone_path", "path", "content"},
			Properties: map[string]string{
				"clone_path": "string",
				"path":       "string",
				"content":    "string",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"sha256", "size"},
			Properties: map[string]string{
				"sha256": "string",
				"size":   "number",
			},
		},
		Handler: fsWriteHandler,
	}
}

type fsWriteInput struct {
	ClonePath string `json:"clone_path"`
	Path      string `json:"path"`
	Content   string `json:"content"`
}

func fsWriteHandler(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
	var in fsWriteInput
	if err := json.Unmarshal(ec.Input, &in); err != nil {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
	}
	if ec.Exec == nil {
		return nil, &packs.PackError{Code: packs.CodeSessionUnavailable, Message: "engine has no session executor"}
	}
	full, perr := safeJoin(in.ClonePath, in.Path)
	if perr != nil {
		return nil, perr
	}
	if in.Path == "" {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "path is required"}
	}
	// mkdir -p the parent so writes to fresh subdirs succeed; tee
	// reads stdin and writes the file.
	script := "mkdir -p " + shellQuote(path.Dir(full)) + " && cat > " + shellQuote(full)
	res, err := runShell(ctx, ec, script, []byte(in.Content))
	if err != nil {
		return nil, &packs.PackError{Code: packs.CodeHandlerFailed, Message: err.Error(), Cause: err}
	}
	if res.ExitCode != 0 {
		return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("write exit %d: %s", res.ExitCode, strings.TrimSpace(string(res.Stderr)))}
	}
	sum := sha256.Sum256([]byte(in.Content))
	return json.Marshal(map[string]any{
		"sha256": hex.EncodeToString(sum[:]),
		"size":   len(in.Content),
	})
}

// --- fs.patch ------------------------------------------------------------

// FSPatch performs a literal search-and-replace inside one file.
// Not a regex — agents tend to write subtly wrong regexes and the
// resulting silent miss is a worse failure mode than "your search
// string didn't match".
//
// Two input shapes are accepted (issue #90). Both are equivalent on
// the wire — the handler normalizes to a list of edits applied in
// order:
//
//  1. helmdeck native, single edit:
//     {"clone_path":..., "path":..., "search":"a", "replace":"b"}
//
//  2. Anthropic CodingAgent batch shape (what gpt-oss / Claude default
//     to without explicit prompting):
//     {"clone_path":..., "path":..., "edits":[{"oldText":"a","newText":"b"}, ...]}
//
// Accepting both means a fresh agent session converges on the right
// call on its first try instead of burning 4 retries against a strict
// schema. Backward compatible with existing single-edit callers.
func FSPatch() *packs.Pack {
	return &packs.Pack{
		Name:        "fs.patch",
		Version:     "v1",
		Description: "Replace literal strings inside a file at a session-local clone path. Pass {search, replace} for a single edit, OR {edits: [{oldText, newText}, ...]} for a batch.",
		NeedsSession: true,
		InputSchema: packs.BasicSchema{
			Required: []string{"clone_path", "path"},
			Properties: map[string]string{
				"clone_path":  "string",
				"path":        "string",
				"search":      "string",
				"replace":     "string",
				"occurrences": "number",
				"edits":       "array",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"applied", "sha256"},
			Properties: map[string]string{
				"applied": "number",
				"sha256":  "string",
			},
		},
		Handler: fsPatchHandler,
	}
}

// fsPatchEdit is one search/replace pair. Accepts the helmdeck-native
// {search, replace} OR the Anthropic-native {oldText, newText}; the
// handler picks whichever is non-empty per item.
type fsPatchEdit struct {
	Search  string `json:"search"`
	Replace string `json:"replace"`
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}

type fsPatchInput struct {
	ClonePath   string        `json:"clone_path"`
	Path        string        `json:"path"`
	Search      string        `json:"search"`
	Replace     string        `json:"replace"`
	Occurrences int           `json:"occurrences"` // 0 = no cap; applies per edit
	Edits       []fsPatchEdit `json:"edits"`
}

func fsPatchHandler(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
	var in fsPatchInput
	if err := json.Unmarshal(ec.Input, &in); err != nil {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
	}
	if ec.Exec == nil {
		return nil, &packs.PackError{Code: packs.CodeSessionUnavailable, Message: "engine has no session executor"}
	}

	// Normalize input into a single ordered list of (search, replace)
	// pairs. Per issue #90 we accept both the native single-edit shape
	// and the Anthropic batch shape; pick whichever the caller used.
	type editPair struct{ search, replace string }
	var edits []editPair
	switch {
	case len(in.Edits) > 0:
		for i, e := range in.Edits {
			s, r := e.Search, e.Replace
			if s == "" && e.OldText != "" {
				s, r = e.OldText, e.NewText
			}
			if s == "" {
				return nil, &packs.PackError{Code: packs.CodeInvalidInput,
					Message: fmt.Sprintf("edits[%d]: search/oldText must not be empty", i)}
			}
			edits = append(edits, editPair{s, r})
		}
		// If both shapes are sent in the same call, prefer edits[].
		// Don't error — be forgiving — but ignore the top-level
		// search/replace so we don't apply anything twice.
	case in.Search != "":
		edits = []editPair{{in.Search, in.Replace}}
	default:
		return nil, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: "must provide either {search, replace} or {edits: [{oldText, newText}, ...]}"}
	}

	full, perr := safeJoin(in.ClonePath, in.Path)
	if perr != nil {
		return nil, perr
	}
	// Read the file in helmdeck (via the shell) and do the literal
	// substitution in Go — keeps us out of sed's regex syntax and
	// avoids the "what does sed do with newlines in the replacement"
	// trap. Same wc-then-cat dance as fs.read so we cap memory.
	statRes, err := runShell(ctx, ec, "wc -c < "+shellQuote(full), nil)
	if err != nil || statRes.ExitCode != 0 {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: fmt.Sprintf("file not readable: %s", strings.TrimSpace(string(statRes.Stderr)))}
	}
	size, _ := strconv.ParseInt(strings.TrimSpace(string(statRes.Stdout)), 10, 64)
	if size > maxFsReadBytes {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: fmt.Sprintf("file is %d bytes, exceeds %d byte cap", size, maxFsReadBytes)}
	}
	readRes, err := runShell(ctx, ec, "cat "+shellQuote(full), nil)
	if err != nil || readRes.ExitCode != 0 {
		return nil, &packs.PackError{Code: packs.CodeHandlerFailed, Message: "cat failed"}
	}
	content := string(readRes.Stdout)

	// Apply each edit in order. occurrences caps EACH edit (matches
	// single-edit semantics; for batch callers it's typically unset).
	totalApplied := 0
	for i, e := range edits {
		matches := strings.Count(content, e.search)
		if matches == 0 {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: fmt.Sprintf("edits[%d]: search string not found in file", i)}
		}
		limit := matches
		if in.Occurrences > 0 && in.Occurrences < limit {
			limit = in.Occurrences
		}
		content = strings.Replace(content, e.search, e.replace, limit)
		totalApplied += limit
	}

	// Write it back via the same mkdir+cat trick.
	writeRes, err := runShell(ctx, ec, "cat > "+shellQuote(full), []byte(content))
	if err != nil || writeRes.ExitCode != 0 {
		return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("write back failed: %s", strings.TrimSpace(string(writeRes.Stderr)))}
	}
	sum := sha256.Sum256([]byte(content))
	return json.Marshal(map[string]any{
		"applied": totalApplied,
		"sha256":  hex.EncodeToString(sum[:]),
	})
}

// --- fs.list -------------------------------------------------------------

// FSList enumerates files under a clone_path subdirectory. The LLM
// uses this to discover what's in a freshly-cloned repo before it
// starts reading individual files.
func FSList() *packs.Pack {
	return &packs.Pack{
		Name:        "fs.list",
		Version:     "v1",
		Description: "List files under a directory in a session-local clone path.",
		NeedsSession: true,
		InputSchema: packs.BasicSchema{
			Required: []string{"clone_path"},
			Properties: map[string]string{
				"clone_path": "string",
				"path":       "string",
				"recursive":  "boolean",
				"glob":       "string",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"files", "count"},
			Properties: map[string]string{
				"files": "array",
				"count": "number",
			},
		},
		Handler: fsListHandler,
	}
}

type fsListInput struct {
	ClonePath string `json:"clone_path"`
	Path      string `json:"path"`
	Recursive bool   `json:"recursive"`
	Glob      string `json:"glob"`
}

func fsListHandler(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
	var in fsListInput
	if err := json.Unmarshal(ec.Input, &in); err != nil {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
	}
	if ec.Exec == nil {
		return nil, &packs.PackError{Code: packs.CodeSessionUnavailable, Message: "engine has no session executor"}
	}
	full, perr := safeJoin(in.ClonePath, in.Path)
	if perr != nil {
		return nil, perr
	}
	// Build a `find` invocation. -maxdepth 1 unless recursive,
	// -type f to drop directories, -name <glob> when set. Limit
	// the output to maxFsListFiles+1 entries so we can detect
	// truncation.
	args := []string{"find", full, "-type", "f"}
	if !in.Recursive {
		args = []string{"find", full, "-maxdepth", "1", "-type", "f"}
	}
	if in.Glob != "" {
		args = append(args, "-name", in.Glob)
	}
	// Quote argv elements that may contain spaces (full path,
	// glob); plain `find` flags pass through unquoted.
	quoted := make([]string, len(args))
	for i, a := range args {
		switch a {
		case "find", "-type", "f", "-maxdepth", "1", "-name":
			quoted[i] = a
		default:
			quoted[i] = shellQuote(a)
		}
	}
	script := strings.Join(quoted, " ") + " | head -n " + strconv.Itoa(maxFsListFiles+1)
	res, err := runShell(ctx, ec, script, nil)
	if err != nil {
		return nil, &packs.PackError{Code: packs.CodeHandlerFailed, Message: err.Error(), Cause: err}
	}
	if res.ExitCode != 0 {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: fmt.Sprintf("find failed: %s", strings.TrimSpace(string(res.Stderr)))}
	}
	lines := strings.Split(strings.TrimSpace(string(res.Stdout)), "\n")
	files := make([]string, 0, len(lines))
	for _, l := range lines {
		if l == "" {
			continue
		}
		// Strip the clone_path prefix so the agent sees relative
		// paths it can pass back to fs.read without re-supplying
		// the clone_path.
		rel := strings.TrimPrefix(l, full+"/")
		files = append(files, rel)
	}
	if len(files) > maxFsListFiles {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: fmt.Sprintf("listing exceeded %d files; narrow with glob or path", maxFsListFiles)}
	}
	return json.Marshal(map[string]any{
		"files": files,
		"count": len(files),
	})
}

// --- cmd.run -------------------------------------------------------------

// CmdRun is the generic "run an arbitrary command in this clone"
// pack. The language-specific packs (python.run, node.run) wrap
// the same underlying executor with sidecar image overrides; this
// pack stays on the default browser sidecar so it works for git,
// make, ls, grep, etc. without forcing operators to choose an image.
func CmdRun() *packs.Pack {
	return &packs.Pack{
		Name:        "cmd.run",
		Version:     "v1",
		Description: "Run a shell command in a session-local clone path.",
		NeedsSession: true,
		InputSchema: packs.BasicSchema{
			Required: []string{"clone_path", "command"},
			Properties: map[string]string{
				"clone_path": "string",
				"command":    "array",
				"stdin":      "string",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"stdout", "stderr", "exit_code"},
			Properties: map[string]string{
				"stdout":    "string",
				"stderr":    "string",
				"exit_code": "number",
			},
		},
		Handler: cmdRunHandler,
	}
}

type cmdRunInput struct {
	ClonePath string   `json:"clone_path"`
	Command   []string `json:"command"`
	Stdin     string   `json:"stdin"`
}

func cmdRunHandler(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
	var in cmdRunInput
	if err := json.Unmarshal(ec.Input, &in); err != nil {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
	}
	if ec.Exec == nil {
		return nil, &packs.PackError{Code: packs.CodeSessionUnavailable, Message: "engine has no session executor"}
	}
	if !isSafeClonePath(in.ClonePath) {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: "clone_path must be an absolute path under /tmp/helmdeck- or /home/helmdeck/work/"}
	}
	if len(in.Command) == 0 {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "command must not be empty"}
	}
	quoted := make([]string, 0, len(in.Command))
	for _, a := range in.Command {
		quoted = append(quoted, shellQuote(a))
	}
	script := "cd " + shellQuote(in.ClonePath) + " && exec " + strings.Join(quoted, " ")
	res, err := runShell(ctx, ec, script, []byte(in.Stdin))
	if err != nil {
		return nil, &packs.PackError{Code: packs.CodeHandlerFailed, Message: err.Error(), Cause: err}
	}
	if len(res.Stdout)+len(res.Stderr) > maxCmdOutBytes {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: fmt.Sprintf("combined output %d bytes exceeds %d cap", len(res.Stdout)+len(res.Stderr), maxCmdOutBytes)}
	}
	return json.Marshal(map[string]any{
		"stdout":    string(res.Stdout),
		"stderr":    string(res.Stderr),
		"exit_code": res.ExitCode,
	})
}

// --- git.commit ----------------------------------------------------------

// GitCommit stages and commits changes inside a clone. The "all"
// flag is true by default — the typical agent flow is "I edited a
// few files via fs.patch, commit everything that's dirty". Operators
// who want a more surgical commit pass `all: false` and rely on a
// future fs.git_add pack (or use cmd.run directly).
func GitCommit() *packs.Pack {
	return &packs.Pack{
		Name:        "git.commit",
		Version:     "v1",
		Description: "Stage and commit changes in a session-local clone path.",
		NeedsSession: true,
		InputSchema: packs.BasicSchema{
			Required: []string{"clone_path", "message"},
			Properties: map[string]string{
				"clone_path": "string",
				"message":    "string",
				"all":        "boolean",
				"author":     "string",
				"email":      "string",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"commit"},
			Properties: map[string]string{
				"commit":         "string",
				"files_changed":  "number",
			},
		},
		Handler: gitCommitHandler,
	}
}

type gitCommitInput struct {
	ClonePath string `json:"clone_path"`
	Message   string `json:"message"`
	All       *bool  `json:"all"`
	Author    string `json:"author"`
	Email     string `json:"email"`
}

func gitCommitHandler(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
	var in gitCommitInput
	if err := json.Unmarshal(ec.Input, &in); err != nil {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
	}
	if ec.Exec == nil {
		return nil, &packs.PackError{Code: packs.CodeSessionUnavailable, Message: "engine has no session executor"}
	}
	if !isSafeClonePath(in.ClonePath) {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput,
			Message: "clone_path must be an absolute path under /tmp/helmdeck- or /home/helmdeck/work/"}
	}
	if strings.TrimSpace(in.Message) == "" {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "message must not be empty"}
	}
	all := true
	if in.All != nil {
		all = *in.All
	}
	author := in.Author
	if author == "" {
		author = "helmdeck-agent"
	}
	email := in.Email
	if email == "" {
		email = "agent@helmdeck.local"
	}

	addCmd := ""
	if all {
		addCmd = "git -C " + shellQuote(in.ClonePath) + " add -A && "
	}
	// Set committer + author env vars inline so the resulting
	// commit clearly attributes the change to the helmdeck agent
	// rather than whatever default git config the sidecar inherits.
	envPrefix := "GIT_AUTHOR_NAME=" + shellQuote(author) +
		" GIT_AUTHOR_EMAIL=" + shellQuote(email) +
		" GIT_COMMITTER_NAME=" + shellQuote(author) +
		" GIT_COMMITTER_EMAIL=" + shellQuote(email) + " "
	script := addCmd + envPrefix +
		"git -C " + shellQuote(in.ClonePath) + " commit -m " + shellQuote(in.Message) + " 1>&2 && " +
		"git -C " + shellQuote(in.ClonePath) + " rev-parse HEAD"
	res, err := runShell(ctx, ec, script, nil)
	if err != nil {
		return nil, &packs.PackError{Code: packs.CodeHandlerFailed, Message: err.Error(), Cause: err}
	}
	if res.ExitCode != 0 {
		stderr := string(res.Stderr)
		if len(stderr) > 1024 {
			stderr = stderr[:1024] + "...(truncated)"
		}
		// "nothing to commit" is the most common LLM mistake — surface
		// it as invalid_input so the agent retries with actual changes
		// instead of treating it as an internal failure.
		if strings.Contains(stderr, "nothing to commit") {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: "nothing to commit (working tree clean)"}
		}
		return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("git commit exit %d: %s", res.ExitCode, stderr)}
	}
	commit := strings.TrimSpace(string(res.Stdout))
	return json.Marshal(map[string]any{
		"commit": commit,
	})
}

// ── git.diff (T619) ──────────────────────────────────────────────

func GitDiff() *packs.Pack {
	return &packs.Pack{
		Name:         "git.diff",
		Version:      "v1",
		Description:  "Show diff of uncommitted changes in a session-local clone.",
		NeedsSession: true,
		InputSchema: packs.BasicSchema{
			Required:   []string{"clone_path"},
			Properties: map[string]string{"clone_path": "string", "staged": "boolean"},
		},
		OutputSchema: packs.BasicSchema{
			Required:   []string{"diff"},
			Properties: map[string]string{"diff": "string", "files_changed": "number"},
		},
		Handler: func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
			var in struct {
				ClonePath string `json:"clone_path"`
				Staged    bool   `json:"staged"`
			}
			if err := json.Unmarshal(ec.Input, &in); err != nil {
				return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error()}
			}
			if ec.Exec == nil {
				return nil, &packs.PackError{Code: packs.CodeSessionUnavailable, Message: "engine has no session executor"}
			}
			if !isSafeClonePath(in.ClonePath) {
				return nil, &packs.PackError{Code: packs.CodeInvalidInput,
					Message: "clone_path must be an absolute path under /tmp/helmdeck- or /home/helmdeck/work/"}
			}
			diffFlag := ""
			if in.Staged {
				diffFlag = "--cached "
			}
			script := "git -C " + shellQuote(in.ClonePath) + " diff " + diffFlag + "&& echo __HELMDECK_SEP__ && git -C " + shellQuote(in.ClonePath) + " diff --stat " + diffFlag + "| tail -1"
			res, err := runShell(ctx, ec, script, nil)
			if err != nil {
				return nil, &packs.PackError{Code: packs.CodeHandlerFailed, Message: err.Error()}
			}
			parts := strings.SplitN(string(res.Stdout), "__HELMDECK_SEP__\n", 2)
			diff := parts[0]
			filesChanged := 0
			if len(parts) > 1 {
				// Parse "N files changed" from git diff --stat summary
				stat := strings.TrimSpace(parts[1])
				if idx := strings.Index(stat, " file"); idx > 0 {
					fmt.Sscanf(stat[:idx], "%d", &filesChanged)
				}
			}
			return json.Marshal(map[string]any{"diff": diff, "files_changed": filesChanged})
		},
	}
}

// ── git.log (T619) ───────────────────────────────────────────────

func GitLog() *packs.Pack {
	return &packs.Pack{
		Name:         "git.log",
		Version:      "v1",
		Description:  "Show recent commit history in a session-local clone.",
		NeedsSession: true,
		InputSchema: packs.BasicSchema{
			Required:   []string{"clone_path"},
			Properties: map[string]string{"clone_path": "string", "count": "number"},
		},
		OutputSchema: packs.BasicSchema{
			Required:   []string{"log"},
			Properties: map[string]string{"log": "string", "count": "number"},
		},
		Handler: func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
			var in struct {
				ClonePath string `json:"clone_path"`
				Count     int    `json:"count"`
			}
			if err := json.Unmarshal(ec.Input, &in); err != nil {
				return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error()}
			}
			if ec.Exec == nil {
				return nil, &packs.PackError{Code: packs.CodeSessionUnavailable, Message: "engine has no session executor"}
			}
			if !isSafeClonePath(in.ClonePath) {
				return nil, &packs.PackError{Code: packs.CodeInvalidInput,
					Message: "clone_path must be an absolute path under /tmp/helmdeck- or /home/helmdeck/work/"}
			}
			count := in.Count
			if count <= 0 {
				count = 10
			}
			if count > 100 {
				count = 100
			}
			script := fmt.Sprintf("git -C %s log --oneline -n %d", shellQuote(in.ClonePath), count)
			res, err := runShell(ctx, ec, script, nil)
			if err != nil {
				return nil, &packs.PackError{Code: packs.CodeHandlerFailed, Message: err.Error()}
			}
			log := strings.TrimSpace(string(res.Stdout))
			lines := strings.Split(log, "\n")
			if log == "" {
				lines = nil
			}
			return json.Marshal(map[string]any{"log": log, "count": len(lines)})
		},
	}
}

// ── fs.delete (T620) ─────────────────────────────────────────────

func FSDelete() *packs.Pack {
	return &packs.Pack{
		Name:         "fs.delete",
		Version:      "v1",
		Description:  "Delete a file from a session-local clone path.",
		NeedsSession: true,
		InputSchema: packs.BasicSchema{
			Required:   []string{"clone_path", "path"},
			Properties: map[string]string{"clone_path": "string", "path": "string"},
		},
		OutputSchema: packs.BasicSchema{
			Required:   []string{"deleted"},
			Properties: map[string]string{"deleted": "boolean", "path": "string"},
		},
		Handler: func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
			var in struct {
				ClonePath string `json:"clone_path"`
				Path      string `json:"path"`
			}
			if err := json.Unmarshal(ec.Input, &in); err != nil {
				return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error()}
			}
			if ec.Exec == nil {
				return nil, &packs.PackError{Code: packs.CodeSessionUnavailable, Message: "engine has no session executor"}
			}
			full, pathErr := safeJoin(in.ClonePath, in.Path)
			if pathErr != nil {
				return nil, pathErr
			}
			script := "rm -f " + shellQuote(full) + " && echo ok"
			res, err := runShell(ctx, ec, script, nil)
			if err != nil {
				return nil, &packs.PackError{Code: packs.CodeHandlerFailed, Message: err.Error()}
			}
			deleted := strings.TrimSpace(string(res.Stdout)) == "ok"
			return json.Marshal(map[string]any{"deleted": deleted, "path": in.Path})
		},
	}
}
