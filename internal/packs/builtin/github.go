// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// github.go (T617, ADR 034) — core GitHub pack set.
//
// Four tools that call the GitHub REST API using a vault-stored PAT:
//   - github.create_issue
//   - github.list_prs
//   - github.post_comment
//   - github.create_release
//
// All four use pure HTTP calls to api.github.com — no `gh` CLI
// dependency — so they work in any session container or even without
// a session (NeedsSession: false). The vault PAT is resolved by name
// (default "github-token") and injected as a Bearer token.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/vault"
)

const (
	githubAPIBase      = "https://api.github.com"
	defaultGitHubCred  = "github-token"
	githubAcceptHeader = "application/vnd.github+json"
	githubAPIVersion   = "2022-11-28"
	maxGitHubResponse  = 1 << 20 // 1 MiB
)

// ── github.create_issue ──────────────────────────────────────────

func GitHubCreateIssue(v *vault.Store) *packs.Pack {
	return &packs.Pack{
		Name:        "github.create_issue",
		Version:     "v1",
		Description: "Create a GitHub issue using a vault-stored PAT.",
		InputSchema: packs.BasicSchema{
			Required: []string{"repo", "title"},
			Properties: map[string]string{
				"repo":       "string",
				"title":      "string",
				"body":       "string",
				"labels":     "array",
				"credential": "string",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"number", "url"},
			Properties: map[string]string{
				"number":   "number",
				"url":      "string",
				"html_url": "string",
			},
		},
		Handler: githubHandler(v, func(token string, input json.RawMessage) (json.RawMessage, error) {
			var in struct {
				Repo   string   `json:"repo"`
				Title  string   `json:"title"`
				Body   string   `json:"body"`
				Labels []string `json:"labels"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return nil, err
			}
			if in.Repo == "" || in.Title == "" {
				return nil, fmt.Errorf("repo and title are required")
			}
			body := map[string]any{"title": in.Title}
			if in.Body != "" {
				body["body"] = in.Body
			}
			if len(in.Labels) > 0 {
				body["labels"] = in.Labels
			}
			return githubAPI(token, "POST", "/repos/"+in.Repo+"/issues", body)
		}),
	}
}

// ── github.list_prs ──────────────────────────────────────────────

func GitHubListPRs(v *vault.Store) *packs.Pack {
	return &packs.Pack{
		Name:        "github.list_prs",
		Version:     "v1",
		Description: "List pull requests on a GitHub repository.",
		InputSchema: packs.BasicSchema{
			Required: []string{"repo"},
			Properties: map[string]string{
				"repo":       "string",
				"state":      "string",
				"head":       "string",
				"base":       "string",
				"credential": "string",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"prs"},
			Properties: map[string]string{
				"prs":   "array",
				"count": "number",
			},
		},
		Handler: githubHandler(v, func(token string, input json.RawMessage) (json.RawMessage, error) {
			var in struct {
				Repo  string `json:"repo"`
				State string `json:"state"`
				Head  string `json:"head"`
				Base  string `json:"base"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return nil, err
			}
			if in.Repo == "" {
				return nil, fmt.Errorf("repo is required")
			}
			var params []string
			if in.State != "" {
				params = append(params, "state="+in.State)
			}
			if in.Head != "" {
				params = append(params, "head="+in.Head)
			}
			if in.Base != "" {
				params = append(params, "base="+in.Base)
			}
			path := "/repos/" + in.Repo + "/pulls"
			if len(params) > 0 {
				path += "?" + strings.Join(params, "&")
			}
			resp, err := githubAPI(token, "GET", path, nil)
			if err != nil {
				return nil, err
			}
			// Wrap the array in {prs: [...], count: N}
			var prs []json.RawMessage
			if err := json.Unmarshal(resp, &prs); err != nil {
				// Not an array — return as-is (GitHub error shape)
				return resp, nil
			}
			return json.Marshal(map[string]any{"prs": prs, "count": len(prs)})
		}),
	}
}

// ── github.post_comment ──────────────────────────────────────────

func GitHubPostComment(v *vault.Store) *packs.Pack {
	return &packs.Pack{
		Name:        "github.post_comment",
		Version:     "v1",
		Description: "Post a comment on a GitHub issue or pull request.",
		InputSchema: packs.BasicSchema{
			Required: []string{"repo", "issue_number", "body"},
			Properties: map[string]string{
				"repo":         "string",
				"issue_number": "number",
				"body":         "string",
				"credential":   "string",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"id", "url"},
			Properties: map[string]string{
				"id":  "number",
				"url": "string",
			},
		},
		Handler: githubHandler(v, func(token string, input json.RawMessage) (json.RawMessage, error) {
			var in struct {
				Repo        string `json:"repo"`
				IssueNumber int    `json:"issue_number"`
				Body        string `json:"body"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return nil, err
			}
			if in.Repo == "" || in.IssueNumber == 0 || in.Body == "" {
				return nil, fmt.Errorf("repo, issue_number, and body are required")
			}
			path := fmt.Sprintf("/repos/%s/issues/%d/comments", in.Repo, in.IssueNumber)
			return githubAPI(token, "POST", path, map[string]string{"body": in.Body})
		}),
	}
}

// ── github.create_release ────────────────────────────────────────

func GitHubCreateRelease(v *vault.Store) *packs.Pack {
	return &packs.Pack{
		Name:        "github.create_release",
		Version:     "v1",
		Description: "Create a GitHub release for a tag.",
		InputSchema: packs.BasicSchema{
			Required: []string{"repo", "tag"},
			Properties: map[string]string{
				"repo":       "string",
				"tag":        "string",
				"name":       "string",
				"body":       "string",
				"draft":      "boolean",
				"credential": "string",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"id", "url"},
			Properties: map[string]string{
				"id":         "number",
				"url":        "string",
				"upload_url": "string",
				"html_url":   "string",
			},
		},
		Handler: githubHandler(v, func(token string, input json.RawMessage) (json.RawMessage, error) {
			var in struct {
				Repo  string `json:"repo"`
				Tag   string `json:"tag"`
				Name  string `json:"name"`
				Body  string `json:"body"`
				Draft bool   `json:"draft"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return nil, err
			}
			if in.Repo == "" || in.Tag == "" {
				return nil, fmt.Errorf("repo and tag are required")
			}
			body := map[string]any{
				"tag_name": in.Tag,
				"draft":    in.Draft,
			}
			if in.Name != "" {
				body["name"] = in.Name
			}
			if in.Body != "" {
				body["body"] = in.Body
			}
			return githubAPI(token, "POST", "/repos/"+in.Repo+"/releases", body)
		}),
	}
}

// ── github.list_issues ───────────────────────────────────────────

func GitHubListIssues(v *vault.Store) *packs.Pack {
	return &packs.Pack{
		Name:        "github.list_issues",
		Version:     "v1",
		Description: "List issues on a GitHub repository (filter by state, labels, assignee).",
		// Read-through cache exemplar (ADR 039, #258). Identical
		// list_issues calls within 5 minutes are served from memory
		// instead of re-hitting the GitHub API (rate-limit relief). The
		// engine seam does the caching declaratively — the handler is
		// untouched. No-op when no memory store is wired.
		Memory: &packs.MemoryConfig{Cache: true, TTL: 5 * time.Minute, Category: "cache"},
		InputSchema: packs.BasicSchema{
			Required: []string{"repo"},
			Properties: map[string]string{
				"repo":       "string",
				"state":      "string",
				"labels":     "string",
				"assignee":   "string",
				"credential": "string",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"issues"},
			Properties: map[string]string{
				"issues": "array",
				"count":  "number",
			},
		},
		Handler: githubHandler(v, func(token string, input json.RawMessage) (json.RawMessage, error) {
			var in struct {
				Repo     string `json:"repo"`
				State    string `json:"state"`
				Labels   string `json:"labels"`
				Assignee string `json:"assignee"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return nil, err
			}
			if in.Repo == "" {
				return nil, fmt.Errorf("repo is required")
			}
			var params []string
			if in.State != "" {
				params = append(params, "state="+in.State)
			}
			if in.Labels != "" {
				params = append(params, "labels="+in.Labels)
			}
			if in.Assignee != "" {
				params = append(params, "assignee="+in.Assignee)
			}
			path := "/repos/" + in.Repo + "/issues"
			if len(params) > 0 {
				path += "?" + strings.Join(params, "&")
			}
			resp, err := githubAPI(token, "GET", path, nil)
			if err != nil {
				return nil, err
			}
			var issues []json.RawMessage
			if err := json.Unmarshal(resp, &issues); err != nil {
				return resp, nil
			}
			return json.Marshal(map[string]any{"issues": issues, "count": len(issues)})
		}),
	}
}

// ── github.create_pr ─────────────────────────────────────────────

// GitHubCreatePR (swe.solve Phase 3, epic #233) opens a pull request
// via the GitHub REST API using a vault-stored PAT. It is the final
// step of swe.solve's `pull_request` output mode: repo.push lands the
// agent's work on a NEW branch, then this pack opens the PR for a
// human to review. As with every github.* pack, the token is resolved
// from the vault by name and never travels through the pack input or
// the agent-visible surface.
//
// Input shape:
//
//	{
//	  "repo":  "owner/name",  // required, "owner/repo"
//	  "head":  "feature-x",   // required, source branch
//	  "base":  "main",        // required, target branch
//	  "title": "Fix the bug", // required
//	  "body":  "...",         // optional PR description
//	  "draft": false,         // optional, open as draft
//	  "credential": "github-token" // optional vault name override
//	}
func GitHubCreatePR(v *vault.Store) *packs.Pack {
	return &packs.Pack{
		Name:        "github.create_pr",
		Version:     "v1",
		Description: "Open a pull request on a GitHub repository using a vault-stored PAT.",
		InputSchema: packs.BasicSchema{
			Required: []string{"repo", "head", "base", "title"},
			Properties: map[string]string{
				"repo":       "string",
				"head":       "string",
				"base":       "string",
				"title":      "string",
				"body":       "string",
				"draft":      "boolean",
				"credential": "string",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"number", "html_url"},
			Properties: map[string]string{
				"number":   "number",
				"url":      "string",
				"html_url": "string",
			},
		},
		Handler: githubHandler(v, func(token string, input json.RawMessage) (json.RawMessage, error) {
			var in struct {
				Repo  string `json:"repo"`
				Head  string `json:"head"`
				Base  string `json:"base"`
				Title string `json:"title"`
				Body  string `json:"body"`
				Draft bool   `json:"draft"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return nil, err
			}
			if in.Repo == "" || in.Head == "" || in.Base == "" || in.Title == "" {
				return nil, fmt.Errorf("repo, head, base, and title are required")
			}
			body := map[string]any{
				"title": in.Title,
				"head":  in.Head,
				"base":  in.Base,
				"draft": in.Draft,
			}
			if in.Body != "" {
				body["body"] = in.Body
			}
			return githubAPI(token, "POST", "/repos/"+in.Repo+"/pulls", body)
		}),
	}
}

// ── github.search ────────────────────────────────────────────────

func GitHubSearch(v *vault.Store) *packs.Pack {
	return &packs.Pack{
		Name:        "github.search",
		Version:     "v1",
		Description: "Search GitHub code, issues, or pull requests.",
		InputSchema: packs.BasicSchema{
			Required: []string{"query"},
			Properties: map[string]string{
				"query":      "string",
				"type":       "string",
				"credential": "string",
			},
		},
		OutputSchema: packs.BasicSchema{
			Required: []string{"total_count", "items"},
			Properties: map[string]string{
				"total_count": "number",
				"items":       "array",
			},
		},
		Handler: githubHandler(v, func(token string, input json.RawMessage) (json.RawMessage, error) {
			var in struct {
				Query string `json:"query"`
				Type  string `json:"type"`
			}
			if err := json.Unmarshal(input, &in); err != nil {
				return nil, err
			}
			if in.Query == "" {
				return nil, fmt.Errorf("query is required")
			}
			searchType := in.Type
			if searchType == "" {
				searchType = "issues"
			}
			switch searchType {
			case "code", "issues", "repositories", "commits":
			default:
				return nil, fmt.Errorf("type must be one of: code, issues, repositories, commits")
			}
			path := "/search/" + searchType + "?q=" + strings.ReplaceAll(in.Query, " ", "+")
			return githubAPI(token, "GET", path, nil)
		}),
	}
}

// ── shared helpers ───────────────────────────────────────────────

// githubHandler wraps an inner function with vault credential
// resolution. The inner function receives the resolved PAT and the
// raw input JSON. This pattern keeps each pack's logic clean while
// sharing the vault lookup + error mapping.
func githubHandler(v *vault.Store, inner func(token string, input json.RawMessage) (json.RawMessage, error)) packs.HandlerFunc {
	return func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
		// Resolve the credential name from the input. The PAT is
		// optional — public repo reads work without auth (60 req/hr
		// rate limit vs 5000 with a token). Writes (create_issue,
		// post_comment, create_release) will fail with a clear
		// GitHub 401 if no token is provided.
		var token string
		var meta struct {
			Credential string `json:"credential"`
		}
		_ = json.Unmarshal(ec.Input, &meta)
		credName := meta.Credential
		if credName == "" {
			credName = defaultGitHubCred
		}

		if v != nil {
			actor := vault.Actor{Subject: "*"}
			res, err := v.ResolveByName(ctx, actor, credName)
			if err == nil {
				token = string(res.Plaintext)
			}
			// If credential not found, proceed without auth — public
			// repo reads still work. Writes will get a 401 from GitHub
			// with a clear error message.
		}

		out, err := inner(token, ec.Input)
		if err != nil {
			return nil, &packs.PackError{Code: packs.CodeHandlerFailed, Message: err.Error()}
		}
		return out, nil
	}
}

// githubAPI makes a single GitHub REST API call and returns the
// response body as raw JSON. Errors from GitHub (4xx/5xx) are
// surfaced with the status code and any message from the response.
func githubAPI(token, method, path string, body any) (json.RawMessage, error) {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, githubAPIBase+path, reqBody)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", githubAcceptHeader)
	req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", "Helmdeck/0.6.0 (+https://github.com/tosin2013/helmdeck)")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxGitHubResponse))
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 400 {
		var ghErr struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(respBody, &ghErr)
		msg := ghErr.Message
		if msg == "" {
			msg = string(respBody)
		}
		return nil, fmt.Errorf("github API %s %s: %d %s", method, path, resp.StatusCode, msg)
	}

	return json.RawMessage(respBody), nil
}
