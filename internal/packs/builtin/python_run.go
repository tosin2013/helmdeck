package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/session"
)

// PythonRun (per-pack image override demo, ADR 001) executes Python
// code or commands inside a Python-equipped session container. The
// pack acquires its own session via SessionSpec.Image, asking the
// runtime for the python sidecar instead of the default browser one.
//
// This is the canonical example of "Option B" per-pack image
// override: the pack catalog stays language-agnostic at the API
// layer, but each pack pins exactly the toolchain it needs at
// session-acquire time. Other languages (node.run, future
// rust.run, go.run, ...) follow the same pattern.
//
// Input shape (exactly one of code or command must be set):
//
//	{
//	  "code":    "print(2 + 2)",          // run inline via python3 -c
//	  "command": ["pytest", "-v"],         // OR run a command in cwd
//	  "cwd":     "/tmp/helmdeck-clone-X1", // optional working dir
//	  "stdin":   "input bytes"             // optional stdin
//	}
//
// Output shape:
//
//	{
//	  "stdout":    "...",
//	  "stderr":    "...",
//	  "exit_code": 0,
//	  "runtime":   "python"
//	}
func PythonRun() *packs.Pack {
	return &packs.Pack{
		Name:        "python.run",
		Version:     "v1",
		Description: "Run Python code or commands inside a Python-equipped session container.",
		NeedsSession: true,
		SessionSpec: session.Spec{
			Image: pythonSidecarImage(),
		},
		InputSchema: packs.BasicSchema{
			Properties: map[string]string{
				"code":    "string",
				"command": "array",
				"cwd":     "string",
				"stdin":   "string",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"stdout", "stderr", "exit_code", "runtime"},
			Properties: map[string]string{
				"stdout":    "string",
				"stderr":    "string",
				"exit_code": "number",
				"runtime":   "string",
			},
		},
		Handler: pythonRunHandler,
	}
}

type langRunInput struct {
	Code    string   `json:"code"`
	Command []string `json:"command"`
	Cwd     string   `json:"cwd"`
	Stdin   string   `json:"stdin"`
}

func pythonRunHandler(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
	var in langRunInput
	if err := json.Unmarshal(ec.Input, &in); err != nil {
		return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
	}
	if err := validateLangRunInput(in); err != nil {
		return nil, err
	}
	if ec.Exec == nil {
		return nil, &packs.PackError{Code: packs.CodeSessionUnavailable, Message: "engine has no session executor"}
	}

	var cmd []string
	if strings.TrimSpace(in.Code) != "" {
		cmd = []string{"python3", "-c", in.Code}
	} else {
		cmd = append([]string{}, in.Command...)
	}

	res, err := runWithCwd(ctx, ec, cmd, in.Cwd, []byte(in.Stdin))
	if err != nil {
		return nil, &packs.PackError{Code: packs.CodeHandlerFailed, Message: err.Error(), Cause: err}
	}
	return marshalLangRunResult(res, "python")
}

// validateLangRunInput enforces "exactly one of code or command".
// Both packs share this rule so the validator lives at package
// scope rather than being duplicated per language.
func validateLangRunInput(in langRunInput) *packs.PackError {
	hasCode := strings.TrimSpace(in.Code) != ""
	hasCmd := len(in.Command) > 0
	if hasCode == hasCmd {
		return &packs.PackError{Code: packs.CodeInvalidInput,
			Message: "exactly one of `code` or `command` must be set"}
	}
	return nil
}

// runWithCwd dispatches a command via ec.Exec, optionally wrapping
// it in a `sh -c` so the working directory takes effect. We avoid
// sh when no cwd is set so the captured argv stays inspectable in
// tests and there's one less layer of quoting to worry about.
//
// When the cwd lives on the persistent repos volume (ADR 040), the
// command also gets the per-language dependency-cache env vars pointed
// at the clone's .hdcache, and the script first ensures that directory
// exists. This is the cross-session win: `go mod download` / `npm
// install` populate a cache that survives into the next session instead
// of re-downloading every run. Off the volume, env is nil and behavior
// is unchanged.
func runWithCwd(ctx context.Context, ec *packs.ExecutionContext, cmd []string, cwd string, stdin []byte) (session.ExecResult, error) {
	if cwd == "" {
		return ec.Exec(ctx, session.ExecRequest{Cmd: cmd, Stdin: stdin})
	}
	base := hdcacheBase(ec.PersistentReposPath, cwd)
	quoted := make([]string, 0, len(cmd))
	for _, a := range cmd {
		quoted = append(quoted, shellQuote(a))
	}
	script := "cd " + shellQuote(cwd) + " && exec " + strings.Join(quoted, " ")
	if base != "" {
		// Tools create their own per-cache subdirs; just guarantee the root.
		script = "mkdir -p " + shellQuote(base) + " && " + script
	}
	return ec.Exec(ctx, session.ExecRequest{
		Cmd:   []string{"sh", "-c", script},
		Stdin: stdin,
		Env:   hdcacheEnvForBase(base),
	})
}

// hdcacheBase returns the clone's .hdcache directory when cwd is on the
// persistent repos volume, or "" otherwise. It anchors at the clone ROOT
// (<reposPath>/<caller>/<hash>) regardless of how deep cwd sits, so the
// cache always lands where `git clean -fdx -e .hdcache` preserves it.
func hdcacheBase(reposPath, cwd string) string {
	if reposPath == "" || cwd == "" {
		return ""
	}
	root := strings.TrimRight(reposPath, "/") + "/"
	if !strings.HasPrefix(cwd, root) {
		return ""
	}
	parts := strings.SplitN(strings.TrimPrefix(cwd, root), "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return filepath.Join(reposPath, parts[0], parts[1], ".hdcache")
}

// hdcacheEnvForBase maps a .hdcache base to the per-language cache env
// vars (Go, npm/yarn/pnpm, pip, cargo). Returns nil for an empty base so
// the ephemeral path injects nothing. Harmless when a tool's language
// isn't the one running — an unused env var costs nothing.
func hdcacheEnvForBase(base string) []string {
	if base == "" {
		return nil
	}
	return []string{
		"GOMODCACHE=" + filepath.Join(base, "go-mod"),
		"GOCACHE=" + filepath.Join(base, "go-build"),
		"npm_config_cache=" + filepath.Join(base, "npm"),
		"YARN_CACHE_FOLDER=" + filepath.Join(base, "yarn"),
		"PIP_CACHE_DIR=" + filepath.Join(base, "pip"),
		"CARGO_HOME=" + filepath.Join(base, "cargo"),
	}
}

// marshalLangRunResult is the shared output encoder for language
// run packs. Keeps the field names and types in lockstep across
// python.run, node.run, and any future siblings.
func marshalLangRunResult(res session.ExecResult, runtime string) (json.RawMessage, error) {
	return json.Marshal(map[string]any{
		"stdout":    string(res.Stdout),
		"stderr":    string(res.Stderr),
		"exit_code": res.ExitCode,
		"runtime":   runtime,
	})
}

// pythonSidecarImage returns the image tag the pack pins via
// SessionSpec. Defaults to the canonical helmdeck Python sidecar;
// operators who roll their own per docs/SIDECAR-LANGUAGES.md can
// override by setting HELMDECK_SIDECAR_PYTHON in the control-plane
// environment before the binary starts.
func pythonSidecarImage() string {
	if v := os.Getenv("HELMDECK_SIDECAR_PYTHON"); v != "" {
		return v
	}
	return "ghcr.io/tosin2013/helmdeck-sidecar-python:latest"
}
