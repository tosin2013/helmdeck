// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// swe_solve.go (swe.solve, epic #233 Phase 3, ADR 022/023 lineage).
//
// swe.solve takes a repo + a natural-language task and runs a
// mini-swe-agent loop inside a helmdeck session sidecar to produce a
// reviewable code change. It is the Go orchestrator that drives the
// full repo.fetch → context-seed → agent-loop → diff → (commit/push/PR)
// pipeline as a single pack call.
//
// Backend: mini-swe-agent (https://github.com/SWE-agent/mini-swe-agent),
// run LOCAL-IN-SESSION. "mini" executes inside the clone's session
// container using its OWN bash (the sidecar's shell), and its LLM calls
// go to helmdeck's OpenAI-compatible AI gateway via litellm. This is
// distinct from contrib/helmdeck-environment, which is for running mini
// OUTSIDE helmdeck and routing each bash command BACK in via cmd.run —
// swe.solve does the inverse: it puts the whole agent loop in the box.
//
// Output modes (input `mode`, default "patch"):
//   - "patch":        safe default. Return the diff + trajectory, no push.
//   - "branch":       push the change to a NEW branch via vault creds.
//   - "pull_request": push a new branch AND open a PR (github.create_pr).
//
// swe.solve NEVER pushes to the default branch. The branch/pull_request
// modes always create a fresh `helmdeck/swe-solve-<short-sha>` branch
// (or a caller-supplied non-default name) and push that. A human reviews
// the PR — the agent's work is never merged automatically.
//
// Credential handling mirrors repo.fetch / repo.push exactly: the agent
// (and the trajectory, and the logs) NEVER see the resolved credential
// value. The clone uses GIT_ASKPASS / GIT_SSH_COMMAND injection, the
// push pipes the secret via stdin, and the PR token is resolved by the
// github.* vault helper and used only for the single HTTP call. The
// gateway API key is written to a 0600 file inside the session and
// exported into mini's environment via OPENAI_API_KEY — it is shredded
// on exit and is never echoed into the trajectory.
//
// Input shape:
//
//	{
//	  "repo_url":     "https://github.com/owner/repo.git", // required
//	  "task":         "Add input validation to ...",        // required
//	  "ref":          "main",                               // optional base ref to clone
//	  "base_branch":  "main",                               // optional PR base (default = cloned ref/HEAD)
//	  "credential":   "github-token",                       // optional vault name (HTTPS PAT)
//	  "model":        "gpt-4o",                             // optional litellm model id
//	  "gateway_base": "http://helmdeck-control-plane:3000/v1", // optional gateway base URL
//	  "max_steps":    30,                                   // optional agent step cap
//	  "mode":         "patch"                               // patch | branch | pull_request
//	}
//
// Output shape:
//
//	{
//	  "success":                 true,
//	  "summary":                 "...",      // last agent message / completion note
//	  "patch":                   "diff ...", // unified diff of the change
//	  "commit":                  "abc123",   // commit sha (branch/pull_request modes)
//	  "branch":                  "helmdeck/swe-solve-abc123", // branch/pull_request modes
//	  "pr_url":                  "https://github.com/...",    // pull_request mode
//	  "trajectory_artifact_key": "swe.solve/...-trajectory.json",
//	  "steps_executed":          12
//	}

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/tosin2013/helmdeck/internal/memory"
	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/security"
	"github.com/tosin2013/helmdeck/internal/session"
	"github.com/tosin2013/helmdeck/internal/vault"
)

// Output modes.
const (
	sweModePatch       = "patch"
	sweModeBranch      = "branch"
	sweModePullRequest = "pull_request"
)

// defaultSweMaxSteps bounds the agent loop when the caller doesn't
// supply one. Kept conservative — a runaway loop burns gateway tokens
// and session wall-clock against the 20-minute SessionSpec timeout.
const defaultSweMaxSteps = 30

// sweTrajectoryPath is the in-session path mini writes its trajectory
// to. We point mini at a known location so the handler can cat it back
// after the loop regardless of mini's default output dir.
const sweTrajectoryPath = "/tmp/helmdeck-swe-trajectory.json"

// miniRunFlags is the exact `mini` CLI invocation template, isolated
// here as a single easily-edited constant.
//
// NEEDS-INTEGRATION-VERIFICATION: these flags are written against the
// mini-swe-agent CLI surface as documented (non-interactive batch run
// with a task string and a step bound), but were NOT verified against
// the pinned sidecar image — there is no image or LLM credential in the
// build environment. Verify each flag against `mini --help` in
// ghcr.io/tosin2013/helmdeck-sidecar-mini-swe before relying on the
// branch/pull_request modes in production.
//
//	-t / --task        the task string (non-interactive; no TTY prompt)
//	-y / --yes         auto-confirm tool actions (don't block on a prompt)
//	--cost-limit / --step-limit  bound the loop (we map max_steps here)
//	-o / --output      trajectory output path (JSON)
//	-m / --model       litellm model id (also via env; flag is explicit)
//
// The model + gateway are ALSO exported via env (OPENAI_API_BASE /
// OPENAI_API_KEY / MSWEA_MODEL_NAME) so mini's litellm layer resolves
// them even if a flag name drifts. Belt-and-suspenders on purpose.
const miniCLITemplate = "mini -y -t %s --output %s --step-limit %d 1>&2"

// SweSolve constructs the swe.solve pack. It depends on the vault store
// (for clone/push credential resolution, same as repo.fetch/repo.push)
// and the egress guard (SSRF protection on the clone/push host), exactly
// like the neighboring repo packs registered in cmd/control-plane.
func SweSolve(v *vault.Store, eg *security.EgressGuard) *packs.Pack {
	return &packs.Pack{
		Name:        "swe.solve",
		Version:     "v1",
		Description: "Run a mini-swe-agent loop inside a session sidecar to produce a reviewable code change (patch / branch / pull_request).",
		// Long-running agent loop: route through the async job registry
		// so the initial tools/call returns a task envelope instead of
		// blocking on the full 20-minute budget.
		Async:           true,
		NeedsSession:    true,
		PreserveSession: true, // keep the clone alive for the caller to inspect / re-run
		SessionSpec: session.Spec{
			Image:       miniSweSidecarImage(),
			MemoryLimit: "2g",
			Timeout:     20 * time.Minute,
		},
		InputSchema: packs.BasicSchema{
			Required: []string{"repo_url", "task"},
			Properties: map[string]string{
				"repo_url":     "string",
				"task":         "string",
				"ref":          "string",
				"base_branch":  "string",
				"credential":   "string",
				"model":        "string",
				"gateway_base": "string",
				"max_steps":    "number",
				"mode":         "string",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"success", "summary", "patch", "trajectory_artifact_key", "steps_executed"},
			Properties: map[string]string{
				"success":                 "boolean",
				"summary":                 "string",
				"patch":                   "string",
				"commit":                  "string",
				"branch":                  "string",
				"pr_url":                  "string",
				"trajectory_artifact_key": "string",
				"steps_executed":          "number",
			},
		},
		Handler: sweSolveHandler(v, eg),
	}
}

type sweSolveInput struct {
	RepoURL     string `json:"repo_url"`
	Task        string `json:"task"`
	Ref         string `json:"ref"`
	BaseBranch  string `json:"base_branch"`
	Credential  string `json:"credential"`
	Model       string `json:"model"`
	GatewayBase string `json:"gateway_base"`
	MaxSteps    int    `json:"max_steps"`
	Mode        string `json:"mode"`
}

func sweSolveHandler(v *vault.Store, eg *security.EgressGuard) packs.HandlerFunc {
	return func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
		var in sweSolveInput
		if err := json.Unmarshal(ec.Input, &in); err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
		}
		if strings.TrimSpace(in.RepoURL) == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "repo_url is required"}
		}
		if strings.TrimSpace(in.Task) == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: "task is required"}
		}
		if ec.Exec == nil {
			return nil, &packs.PackError{Code: packs.CodeSessionUnavailable, Message: "engine has no session executor"}
		}
		mode := in.Mode
		if mode == "" {
			mode = sweModePatch
		}
		switch mode {
		case sweModePatch, sweModeBranch, sweModePullRequest:
		default:
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: fmt.Sprintf("mode must be one of patch, branch, pull_request (got %q)", mode)}
		}
		maxSteps := in.MaxSteps
		if maxSteps <= 0 {
			maxSteps = defaultSweMaxSteps
		}

		// priorContext is the memory-layer recall hook (#257 — Universal
		// Memory Delivery Layer). Today a no-op; when the memory layer
		// lands, this is where swe.solve will fetch prior solves / repo
		// knowledge and prepend them to the task before the agent loop.
		task := priorContext(ctx, ec, in)

		ec.Report(2, "resolving clone credentials")

		// ── Step 1: clone (reuse repo.fetch helpers directly) ──────────
		// swe_solve.go is package builtin, so we call the package-private
		// clone helpers in repo_fetch.go rather than reinventing the
		// vault-injection logic. The credential never reaches the agent:
		// it is piped via stdin into the GIT_ASKPASS/GIT_SSH_COMMAND
		// script and shredded on exit.
		host, scheme, err := parseGitHost(in.RepoURL)
		if err != nil {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput, Message: err.Error(), Cause: err}
		}
		if eg != nil {
			if err := eg.CheckHost(ctx, host); err != nil {
				return nil, &packs.PackError{Code: packs.CodeInvalidInput,
					Message: fmt.Sprintf("egress denied: %v", err), Cause: err}
			}
		}

		var cloneScript string
		var cloneStdin []byte
		switch scheme {
		case "ssh":
			if v == nil {
				return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
					Message: "credential vault not configured (required for SSH clones)"}
			}
			res, rerr := resolveHostCred(ctx, v, host)
			if rerr != nil {
				return nil, rerr
			}
			if res.Record.Type != vault.TypeSSH {
				return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
					Message: fmt.Sprintf("vault credential %q is type %q, expected ssh", res.Record.Name, res.Record.Type)}
			}
			cloneScript = buildRepoFetchSSHScript(in.RepoURL, in.Ref, 0)
			cloneStdin = res.Plaintext
		case "https":
			if in.Credential != "" && v != nil {
				res, rerr := resolveNamedCred(ctx, v, in.Credential)
				if rerr != nil {
					return nil, rerr
				}
				cloneScript = buildRepoFetchHTTPSScript(in.RepoURL, in.Ref, 0, true)
				cloneStdin = res.Plaintext
			} else {
				cloneScript = buildRepoFetchHTTPSScript(in.RepoURL, in.Ref, 0, false)
			}
		default:
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: fmt.Sprintf("unsupported git scheme: %q", scheme)}
		}

		ec.Report(8, "cloning repository")
		cloneRes, err := ec.Exec(ctx, session.ExecRequest{Cmd: []string{"sh", "-c", cloneScript}, Stdin: cloneStdin})
		if err != nil {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed, Message: fmt.Sprintf("git clone exec: %v", err)}
		}
		if cloneRes.ExitCode == 99 {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: fmt.Sprintf("remote %s has no branches", in.RepoURL)}
		}
		if cloneRes.ExitCode != 0 {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("git clone exit %d: %s", cloneRes.ExitCode, truncateString(string(cloneRes.Stderr), 1024))}
		}
		var cloneEnv map[string]any
		if err := json.Unmarshal(cloneRes.Stdout, &cloneEnv); err != nil {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("could not parse clone envelope: %v", err)}
		}
		clonePath, _ := cloneEnv["clone_path"].(string)
		if clonePath == "" {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed, Message: "clone envelope missing clone_path"}
		}

		// ── Step 2: context seed via repo.map ──────────────────────────
		// Reuse repo.map's script to build an Aider-style symbol map the
		// agent can use to orient. Best-effort: a failure here (no ctags
		// in the image, etc.) is non-fatal — we just skip the seed.
		ec.Report(15, "building repo-map context seed")
		var contextSeed string
		mapScript := buildRepoMapScript(clonePath, 1500, nil)
		if mapRes, merr := ec.Exec(ctx, session.ExecRequest{Cmd: []string{"sh", "-c", mapScript}}); merr == nil && mapRes.ExitCode == 0 && json.Valid(mapRes.Stdout) {
			var m struct {
				Map string `json:"map"`
			}
			_ = json.Unmarshal(mapRes.Stdout, &m)
			contextSeed = m.Map
		}

		// ── Step 3: run mini in-session ────────────────────────────────
		// mini runs inside the clone using its own bash; its LLM calls go
		// to the helmdeck gateway via litellm (OPENAI_API_BASE/KEY +
		// model). The gateway key is resolved from the vault and written
		// to a 0600 file that mini's env points at — it never touches the
		// agent-visible argv, the task text, or the trajectory.
		ec.Report(25, "running agent loop")
		gatewayBase := in.GatewayBase
		if gatewayBase == "" {
			gatewayBase = sweGatewayBase()
		}
		model := in.Model
		if model == "" {
			model = sweModel()
		}
		gatewayKey, keyErr := resolveGatewayKey(ctx, v)
		if keyErr != nil {
			return nil, keyErr
		}

		fullTask := in.Task
		if contextSeed != "" {
			// Prepend the repo-map seed as context. mini gets ONE task
			// string; the seed is fenced so the agent reads it as orienting
			// material, not as part of the instruction.
			fullTask = "Repository symbol map (for orientation):\n" + contextSeed +
				"\n\n---\nTask:\n" + task
		} else {
			fullTask = task
		}

		miniScript := buildMiniRunScript(clonePath, fullTask, model, gatewayBase, maxSteps, len(gatewayKey) > 0)
		miniRes, err := ec.Exec(ctx, session.ExecRequest{Cmd: []string{"sh", "-c", miniScript}, Stdin: gatewayKey})
		if err != nil {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed, Message: fmt.Sprintf("mini run exec: %v", err)}
		}
		if miniRes.ExitCode != 0 {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("mini-swe-agent exit %d: %s", miniRes.ExitCode, truncateString(string(miniRes.Stderr), 1024))}
		}

		// ── Step 4: collect diff + trajectory ──────────────────────────
		ec.Report(70, "collecting diff and trajectory")
		// Diff the working tree against the cloned HEAD. The agent edited
		// files in place; this captures everything that changed.
		diffScript := "git -C " + shellQuote(clonePath) + " add -A 1>&2 || true\n" +
			"git -C " + shellQuote(clonePath) + " diff --cached"
		diffRes, err := ec.Exec(ctx, session.ExecRequest{Cmd: []string{"sh", "-c", diffScript}})
		if err != nil {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed, Message: fmt.Sprintf("git diff exec: %v", err)}
		}
		patch := string(diffRes.Stdout)

		// Read mini's trajectory file back. Parse defensively — best-
		// effort JSON. The trajectory is the agent's reasoning record and
		// is stored verbatim as an artifact; it MUST NOT contain the
		// gateway key (the key lives only in a 0600 file mini reads via
		// env, never logged into the trajectory).
		trajRes, _ := ec.Exec(ctx, session.ExecRequest{Cmd: []string{"cat", sweTrajectoryPath}})
		trajectoryBytes := trajRes.Stdout
		if len(trajectoryBytes) == 0 {
			trajectoryBytes = []byte("{}")
		}
		summary, stepsExecuted := summarizeTrajectory(trajectoryBytes)

		// ── Step 5: store trajectory artifact ──────────────────────────
		ec.Report(80, "storing trajectory artifact")
		art, err := ec.Artifacts.Put(ctx, "swe.solve", "trajectory.json", trajectoryBytes, "application/json")
		if err != nil {
			return nil, &packs.PackError{Code: packs.CodeArtifactFailed, Message: err.Error(), Cause: err}
		}

		out := map[string]any{
			"success":                 true,
			"summary":                 summary,
			"patch":                   patch,
			"trajectory_artifact_key": art.Key,
			"steps_executed":          stepsExecuted,
		}

		// ── Step 6: mode branching ─────────────────────────────────────
		// patch mode stops here — safe default, no push.
		if mode == sweModePatch {
			storeSolveNote(ec, in.RepoURL, summary, art.Key, "")
			ec.Report(100, "done (patch mode)")
			return json.Marshal(out)
		}

		// branch / pull_request modes: create a NEW branch, commit, push.
		// NEVER push to the default branch. The branch name is derived
		// from a short sha so re-runs don't collide on a stable name.
		ec.Report(85, "committing change")
		shortRes, _ := ec.Exec(ctx, session.ExecRequest{
			Cmd: []string{"git", "-C", clonePath, "rev-parse", "--short", "HEAD"},
		})
		shortSHA := strings.TrimSpace(string(shortRes.Stdout))
		if shortSHA == "" {
			shortSHA = "work"
		}
		newBranch := "helmdeck/swe-solve-" + shortSHA

		// Guard: refuse to overwrite the default/base branch. We create a
		// brand-new branch with `git switch -c`, which fails if the name
		// already exists — exactly the safety we want.
		commitScript := "set -eu\n" +
			"git -C " + shellQuote(clonePath) + " switch -c " + shellQuote(newBranch) + " 1>&2\n" +
			"GIT_AUTHOR_NAME=helmdeck-agent GIT_AUTHOR_EMAIL=agent@helmdeck.local " +
			"GIT_COMMITTER_NAME=helmdeck-agent GIT_COMMITTER_EMAIL=agent@helmdeck.local " +
			"git -C " + shellQuote(clonePath) + " commit -m " + shellQuote("swe.solve: "+in.Task) + " 1>&2\n" +
			"git -C " + shellQuote(clonePath) + " rev-parse HEAD"
		commitRes, err := ec.Exec(ctx, session.ExecRequest{Cmd: []string{"sh", "-c", commitScript}})
		if err != nil {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed, Message: fmt.Sprintf("git commit exec: %v", err)}
		}
		if commitRes.ExitCode != 0 {
			stderr := string(commitRes.Stderr)
			if strings.Contains(stderr, "nothing to commit") {
				return nil, &packs.PackError{Code: packs.CodeInvalidInput,
					Message: "agent produced no change to commit (working tree clean)"}
			}
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("git commit exit %d: %s", commitRes.ExitCode, truncateString(stderr, 1024))}
		}
		commit := strings.TrimSpace(string(commitRes.Stdout))
		out["commit"] = commit
		out["branch"] = newBranch

		// Push the new branch via the same vault-credential flow repo.push
		// uses. The secret is piped via stdin into the push script and
		// never logged.
		ec.Report(90, "pushing branch")
		if perr := sweSolvePush(ctx, ec, v, eg, clonePath, newBranch, in.Credential, scheme, host); perr != nil {
			return nil, perr
		}

		if mode == sweModeBranch {
			storeSolveNote(ec, in.RepoURL, summary, art.Key, newBranch)
			ec.Report(100, "done (branch mode)")
			return json.Marshal(out)
		}

		// pull_request mode: open a PR via github.create_pr's handler.
		ec.Report(95, "opening pull request")
		base := in.BaseBranch
		if base == "" {
			base = strings.TrimSpace(deriveRef(in.Ref))
		}
		ghRepo := githubRepoSlug(in.RepoURL)
		if ghRepo == "" {
			return nil, &packs.PackError{Code: packs.CodeInvalidInput,
				Message: "could not derive owner/repo from repo_url for pull_request mode"}
		}
		prInput, _ := json.Marshal(map[string]any{
			"repo":       ghRepo,
			"head":       newBranch,
			"base":       base,
			"title":      "swe.solve: " + in.Task,
			"body":       sweSolvePRBody(in.Task, art.Key, summary),
			"credential": in.Credential,
		})
		prHandler := GitHubCreatePR(v).Handler
		prEC := &packs.ExecutionContext{Input: prInput}
		prOut, err := prHandler(ctx, prEC)
		if err != nil {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("create PR: %v", err)}
		}
		var pr struct {
			HTMLURL string `json:"html_url"`
		}
		_ = json.Unmarshal(prOut, &pr)
		out["pr_url"] = pr.HTMLURL
		storeSolveNote(ec, in.RepoURL, summary, art.Key, newBranch)
		ec.Report(100, "done (pull_request mode)")
		return json.Marshal(out)
	}
}

// sweSolvePush pushes the new branch using the same host-matched vault
// credential resolution repo.push uses. The credential is piped via
// stdin so it never appears in argv, env, or the audit trail.
func sweSolvePush(ctx context.Context, ec *packs.ExecutionContext, v *vault.Store, eg *security.EgressGuard,
	clonePath, branch, credential, scheme, host string) *packs.PackError {
	if eg != nil {
		if err := eg.CheckHost(ctx, host); err != nil {
			return &packs.PackError{Code: packs.CodeInvalidInput, Message: fmt.Sprintf("egress denied: %v", err)}
		}
	}
	var stdinPayload []byte
	switch scheme {
	case "ssh":
		if v == nil {
			return &packs.PackError{Code: packs.CodeHandlerFailed, Message: "credential vault not configured (required for SSH push)"}
		}
		res, rerr := resolveHostCred(ctx, v, host)
		if rerr != nil {
			return rerr
		}
		if res.Record.Type != vault.TypeSSH {
			return &packs.PackError{Code: packs.CodeHandlerFailed,
				Message: fmt.Sprintf("vault credential %q is type %q, expected ssh", res.Record.Name, res.Record.Type)}
		}
		stdinPayload = res.Plaintext
	case "https":
		if credential != "" && v != nil {
			res, rerr := resolveNamedCred(ctx, v, credential)
			if rerr != nil {
				return rerr
			}
			stdinPayload = res.Plaintext
		}
	}
	var script string
	if scheme == "ssh" {
		script = buildRepoPushSSHScript(clonePath, "origin", branch, false)
	} else {
		script = buildRepoPushHTTPSScript(clonePath, "origin", branch, false, len(stdinPayload) > 0)
	}
	res, err := ec.Exec(ctx, session.ExecRequest{Cmd: []string{"sh", "-c", script}, Stdin: stdinPayload})
	if err != nil {
		return &packs.PackError{Code: packs.CodeHandlerFailed, Message: fmt.Sprintf("git push exec: %v", err)}
	}
	if res.ExitCode != 0 {
		stderr := string(res.Stderr)
		if isNonFastForward(stderr) {
			return &packs.PackError{Code: packs.CodeSchemaMismatch,
				Message: fmt.Sprintf("non-fast-forward push to origin/%s rejected: %s", branch, truncateString(stderr, 512))}
		}
		return &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("git push exit %d: %s", res.ExitCode, truncateString(stderr, 1024))}
	}
	return nil
}

// resolveHostCred / resolveNamedCred wrap the vault lookups with the
// same error mapping repo.fetch / repo.push use. Pulled out so the
// clone and push paths share one resolver.
func resolveHostCred(ctx context.Context, v *vault.Store, host string) (vault.ResolveResult, *packs.PackError) {
	actor := vault.Actor{Subject: "*"}
	res, err := v.Resolve(ctx, actor, host, "")
	if err != nil {
		return vault.ResolveResult{}, &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("no vault credential matches host %q: %v", host, err)}
	}
	return res, nil
}

func resolveNamedCred(ctx context.Context, v *vault.Store, name string) (vault.ResolveResult, *packs.PackError) {
	actor := vault.Actor{Subject: "*"}
	res, err := v.ResolveByName(ctx, actor, name)
	if err != nil {
		return vault.ResolveResult{}, &packs.PackError{Code: packs.CodeHandlerFailed,
			Message: fmt.Sprintf("vault credential %q not found: %v", name, err)}
	}
	return res, nil
}

// resolveGatewayKey resolves the AI-gateway API key the agent's litellm
// layer uses. We resolve a vault credential named "helmdeck-gateway"
// (an OpenAI-compatible token scoped to the gateway). Returns nil bytes
// when no key is configured — mini may still run against an
// auth-optional gateway, and the env export is conditional on a non-
// empty key. The key NEVER reaches the agent argv or the trajectory.
func resolveGatewayKey(ctx context.Context, v *vault.Store) ([]byte, *packs.PackError) {
	if v == nil {
		return nil, nil
	}
	actor := vault.Actor{Subject: "*"}
	res, err := v.ResolveByName(ctx, actor, sweGatewayCredName())
	if err != nil {
		// No gateway key configured is not fatal — proceed without auth.
		return nil, nil
	}
	return res.Plaintext, nil
}

// buildMiniRunScript renders the in-session shell that runs mini. The
// gateway key (if any) is read from stdin into a 0600 file and exported
// into mini's environment via OPENAI_API_KEY — it is shredded on exit
// and never echoed. The task string and model are shell-quoted into the
// `mini` argv; the trajectory is written to a known path the handler
// cats back afterward.
func buildMiniRunScript(clonePath, task, model, gatewayBase string, maxSteps int, hasKey bool) string {
	lines := []string{"set -eu"}
	if hasKey {
		lines = append(lines,
			"KEY_DIR=$(mktemp -d /tmp/helmdeck-gwkey-XXXXXX)",
			"trap 'shred -u \"$KEY_DIR\"/key 2>/dev/null || rm -f \"$KEY_DIR\"/key; rmdir \"$KEY_DIR\" 2>/dev/null || true' EXIT",
			"cat > \"$KEY_DIR\"/key",
			"chmod 600 \"$KEY_DIR\"/key",
			"export OPENAI_API_KEY=\"$(cat \"$KEY_DIR\"/key)\"",
		)
	}
	lines = append(lines,
		"export OPENAI_API_BASE="+shellQuote(gatewayBase),
		// litellm/mini also read these names depending on version; set
		// all the plausible ones so a flag/env drift doesn't break the run.
		"export OPENAI_BASE_URL="+shellQuote(gatewayBase),
		"export MSWEA_MODEL_NAME="+shellQuote(model),
		"cd "+shellQuote(clonePath),
		// NEEDS-INTEGRATION-VERIFICATION: see miniCLITemplate doc comment.
		fmt.Sprintf(miniCLITemplate, shellQuote(task), shellQuote(sweTrajectoryPath), maxSteps)+
			" -m "+shellQuote(model),
	)
	return strings.Join(lines, "\n")
}

// summarizeTrajectory extracts a best-effort summary line and a step
// count from mini's trajectory JSON. Defensive: mini's exact trajectory
// schema is image-version-specific (NEEDS-INTEGRATION-VERIFICATION), so
// we probe a few plausible shapes and fall back to a generic summary.
func summarizeTrajectory(raw []byte) (summary string, steps int) {
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return "agent loop completed (trajectory unparseable)", 0
	}
	// Step count: prefer an explicit field, else len of a messages/steps array.
	if n, ok := numField(probe, "steps_executed", "n_steps", "step"); ok {
		steps = n
	} else if arr, ok := probe["messages"].([]any); ok {
		steps = len(arr)
	} else if arr, ok := probe["steps"].([]any); ok {
		steps = len(arr)
	} else if arr, ok := probe["trajectory"].([]any); ok {
		steps = len(arr)
	}
	// Summary: prefer an explicit field, else the last message content.
	for _, k := range []string{"summary", "result", "submission", "final_message"} {
		if s, ok := probe[k].(string); ok && strings.TrimSpace(s) != "" {
			return truncateString(s, 2000), steps
		}
	}
	if arr, ok := probe["messages"].([]any); ok && len(arr) > 0 {
		if last, ok := arr[len(arr)-1].(map[string]any); ok {
			if c, ok := last["content"].(string); ok && c != "" {
				return truncateString(c, 2000), steps
			}
		}
	}
	return "agent loop completed", steps
}

func numField(m map[string]any, keys ...string) (int, bool) {
	for _, k := range keys {
		if f, ok := m[k].(float64); ok {
			return int(f), true
		}
	}
	return 0, false
}

// sweSolveNotesKey is the memory key under which swe.solve stores and
// recalls its per-repo solve note. Keyed on a hash of the repo URL so
// the note survives across tasks against the same repo without leaking
// the URL into the key.
func sweSolveNotesKey(repoURL string) string {
	sum := sha256.Sum256([]byte(repoURL))
	return "swe.solve/" + hex.EncodeToString(sum[:])[:16] + "/notes"
}

// priorContext is the memory-layer recall hook (#257, ADR 039). When a
// memory store is wired (ec.Memory != nil), it recalls the prior-solve
// note for this repo and prepends it to the task so the agent benefits
// from earlier runs ("smarter than vanilla mini-swe-agent"). Nil-safe:
// when no memory is configured it returns the task unchanged, exactly
// as before the memory layer landed.
func priorContext(_ context.Context, ec *packs.ExecutionContext, in sweSolveInput) string {
	if ec == nil || ec.Memory == nil {
		return in.Task
	}
	ent, err := ec.Memory.Recall(sweSolveNotesKey(in.RepoURL))
	if err != nil || ent == nil || len(ent.Value) == 0 {
		return in.Task
	}
	return "Prior context from earlier work on this repository (use it to avoid repeating mistakes):\n" +
		string(ent.Value) + "\n\n---\nTask:\n" + in.Task
}

// storeSolveNote persists a short note about a completed solve so the
// next run on the same repo can recall it via priorContext. Best-effort
// and nil-safe — a store failure (or no memory wired) never fails the
// solve. The note is intentionally compact (summary + trajectory key +
// branch) and carries a long TTL since solve knowledge ages slowly.
func storeSolveNote(ec *packs.ExecutionContext, repoURL, summary, trajectoryKey, branch string) {
	if ec == nil || ec.Memory == nil {
		return
	}
	note := "summary: " + summary
	if trajectoryKey != "" {
		note += "\ntrajectory: " + trajectoryKey
	}
	if branch != "" {
		note += "\nbranch: " + branch
	}
	_ = ec.Memory.Store(sweSolveNotesKey(repoURL), []byte(note),
		memory.WithTTL(30*24*time.Hour), memory.WithCategory("solve"))
}

// deriveRef returns the PR base branch when none was supplied. Falls
// back to "main" if the clone ref is empty/HEAD — GitHub rejects a PR
// with an empty base, and "main" is the overwhelmingly common default.
func deriveRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" || ref == "HEAD" {
		return "main"
	}
	return ref
}

// githubRepoSlug extracts "owner/repo" from a git URL for the GitHub
// API path. Returns "" for non-github or unparseable URLs.
func githubRepoSlug(rawURL string) string {
	s := rawURL
	s = strings.TrimSuffix(s, ".git")
	// scp-like: git@github.com:owner/repo
	if i := strings.Index(s, ":"); i >= 0 && !strings.Contains(s, "://") {
		s = s[i+1:]
		return s
	}
	// URL form: scheme://host/owner/repo
	if i := strings.Index(s, "://"); i >= 0 {
		rest := s[i+3:]
		if j := strings.Index(rest, "/"); j >= 0 {
			return rest[j+1:]
		}
	}
	return ""
}

func sweSolvePRBody(task, trajectoryKey, summary string) string {
	return "Opened by helmdeck `swe.solve` (epic #233).\n\n" +
		"**Task**\n\n> " + task + "\n\n" +
		"**Agent summary**\n\n" + summary + "\n\n" +
		"**Trajectory artifact**: `" + trajectoryKey + "`\n\n" +
		"This change was produced by an automated agent loop and requires human review before merge."
}

// miniSweSidecarImage returns the image tag swe.solve pins via
// SessionSpec. Mirrors pythonSidecarImage(): operators override via
// HELMDECK_SIDECAR_MINI_SWE in the control-plane environment.
func miniSweSidecarImage() string {
	if v := os.Getenv("HELMDECK_SIDECAR_MINI_SWE"); v != "" {
		return v
	}
	return "ghcr.io/tosin2013/helmdeck-sidecar-mini-swe:latest"
}

// sweGatewayBase returns the OpenAI-compatible AI-gateway base URL the
// agent's litellm layer points at.
//
// ASSUMPTION: there is no clean internal constant for the gateway base
// URL reachable from inside a session container (the gateway is the
// control plane's /v1 surface). We therefore accept it via the
// `gateway_base` pack input and fall back to HELMDECK_GATEWAY_BASE, then
// a localhost default. Operators MUST set HELMDECK_GATEWAY_BASE to the
// in-cluster control-plane URL (e.g. http://control-plane:8080/v1) for
// the agent loop to reach the gateway from the sidecar network. The
// default matches the Compose service name (helmdeck-control-plane:3000)
// so a stock single-host deployment works without extra config.
func sweGatewayBase() string {
	if v := os.Getenv("HELMDECK_GATEWAY_BASE"); v != "" {
		return v
	}
	return "http://helmdeck-control-plane:3000/v1"
}

// sweModel returns the default litellm model id for the agent loop.
func sweModel() string {
	if v := os.Getenv("HELMDECK_SWE_MODEL"); v != "" {
		return v
	}
	return "gpt-4o"
}

// sweGatewayCredName is the vault credential name holding the gateway
// API key. Override via HELMDECK_GATEWAY_CRED.
func sweGatewayCredName() string {
	if v := os.Getenv("HELMDECK_GATEWAY_CRED"); v != "" {
		return v
	}
	return "helmdeck-gateway"
}
