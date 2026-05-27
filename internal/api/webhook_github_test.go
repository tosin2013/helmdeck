// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
)

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func mkIssuePayload(label string) webhookPayload {
	var p webhookPayload
	p.Action = "labeled"
	p.Repo.FullName = "o/r"
	p.Repo.CloneURL = "https://github.com/o/r.git"
	p.Issue.Number = 7
	p.Issue.Title = "Crash on empty input"
	p.Issue.Body = "Steps to reproduce..."
	if label != "" {
		p.Issue.Labels = []struct {
			Name string `json:"name"`
		}{{Name: label}}
	}
	return p
}

func TestRuleMatches_IssueLabelFilter(t *testing.T) {
	rule := WebhookRule{Event: "issues", Action: "labeled", Label: "swe-solve", Pack: "swe.solve"}
	if !ruleMatches(rule, "issues", mkIssuePayload("swe-solve")) {
		t.Error("should match an issues event carrying the trigger label")
	}
	if ruleMatches(rule, "issues", mkIssuePayload("bug")) {
		t.Error("must NOT match when the trigger label is absent")
	}
	if ruleMatches(rule, "push", mkIssuePayload("swe-solve")) {
		t.Error("must NOT match a different event type")
	}
	// Action filter.
	p := mkIssuePayload("swe-solve")
	p.Action = "closed"
	if ruleMatches(rule, "issues", p) {
		t.Error("must NOT match when the action filter differs")
	}
}

func TestBuildWebhookInput_IssueToSweSolve(t *testing.T) {
	rule := WebhookRule{
		Event: "issues", Label: "swe-solve", Pack: "swe.solve",
		Args: json.RawMessage(`{"credential":"gh-pat","model":"gpt-4o","mode":"branch"}`),
	}
	raw, ok := buildWebhookInput(rule, "issues", mkIssuePayload("swe-solve"), "del-1")
	if !ok {
		t.Fatal("expected input to build")
	}
	var in map[string]any
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	if in["repo_url"] != "https://github.com/o/r.git" {
		t.Errorf("repo_url = %v", in["repo_url"])
	}
	task, _ := in["task"].(string)
	if !strings.Contains(task, "Crash on empty input") || !strings.Contains(task, "Steps to reproduce") {
		t.Errorf("task should combine title + body, got %q", task)
	}
	// Operator args win over the default mode.
	if in["mode"] != "branch" {
		t.Errorf("rule.Args mode should override default, got %v", in["mode"])
	}
	if in["credential"] != "gh-pat" || in["model"] != "gpt-4o" {
		t.Errorf("rule.Args fields missing: %v", in)
	}
}

func TestBuildWebhookInput_CommentBodyWins(t *testing.T) {
	p := mkIssuePayload("swe-solve")
	p.Comment.Body = "/swe-solve please fix the null deref in parser.go"
	raw, ok := buildWebhookInput(WebhookRule{Event: "issue_comment", Pack: "swe.solve"}, "issue_comment", p, "d")
	if !ok {
		t.Fatal("expected input")
	}
	var in map[string]any
	_ = json.Unmarshal(raw, &in)
	if in["task"] != p.Comment.Body {
		t.Errorf("issue_comment task should be the comment body, got %v", in["task"])
	}
}

func TestBuildWebhookInput_PushUnchanged(t *testing.T) {
	var p webhookPayload
	p.Ref = "refs/heads/main"
	p.Repo.CloneURL = "https://github.com/o/r.git"
	raw, ok := buildWebhookInput(WebhookRule{Event: "push", Pack: "cmd.run"}, "push", p, "d1")
	if !ok {
		t.Fatal("expected input")
	}
	var in map[string]any
	_ = json.Unmarshal(raw, &in)
	if in["url"] != "https://github.com/o/r.git" || in["ref"] != "refs/heads/main" {
		t.Errorf("push input shape changed: %v", in)
	}
	if in["_webhook_event"] != "push" {
		t.Errorf("missing _webhook_event: %v", in)
	}
}

func TestWebhookResultBody(t *testing.T) {
	// Failure path.
	if got := webhookResultBody(nil, errors.New("boom")); !strings.Contains(got, "boom") || !strings.Contains(got, "No changes were pushed") {
		t.Errorf("failure body missing error/no-push note: %q", got)
	}
	// Success with PR.
	res := &packs.Result{Output: json.RawMessage(`{"success":true,"summary":"Fixed the off-by-one","pr_url":"https://github.com/o/r/pull/9","trajectory_artifact_key":"swe.solve/t.json"}`)}
	got := webhookResultBody(res, nil)
	for _, want := range []string{"finished", "Fixed the off-by-one", "pull/9", "swe.solve/t.json"} {
		if !strings.Contains(got, want) {
			t.Errorf("success body missing %q\n%s", want, got)
		}
	}
}

func TestWebhookRuleCredential(t *testing.T) {
	if got := webhookRuleCredential(WebhookRule{Args: json.RawMessage(`{"credential":"gh-pat"}`)}); got != "gh-pat" {
		t.Errorf("got %q", got)
	}
	if got := webhookRuleCredential(WebhookRule{}); got != "" {
		t.Errorf("no args should yield empty credential, got %q", got)
	}
}

// TestWebhookHandler_SignatureAndMatch drives the HTTP endpoint: a signed
// "issues" event with the trigger label is accepted and matches one rule;
// a bad signature is rejected; the same event without the label matches
// nothing. The dispatch goroutine is a no-op here (nil engine), so this
// asserts only the synchronous request-path decision.
func TestWebhookHandler_SignatureAndMatch(t *testing.T) {
	secret := "shhh"
	t.Setenv("HELMDECK_GITHUB_WEBHOOK_SECRET", secret)
	t.Setenv("HELMDECK_GITHUB_WEBHOOK_RULES",
		`[{"event":"issues","action":"labeled","label":"swe-solve","pack":"swe.solve","args":{"credential":"gh-pat"}}]`)

	mux := http.NewServeMux()
	registerGitHubWebhookRoute(mux, Deps{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})

	post := func(event string, p webhookPayload, withSig bool) (int, map[string]any) {
		body, _ := json.Marshal(p)
		req := httptest.NewRequest("POST", "/api/v1/webhooks/github", strings.NewReader(string(body)))
		req.Header.Set("X-GitHub-Event", event)
		req.Header.Set("X-GitHub-Delivery", "test-delivery")
		if withSig {
			req.Header.Set("X-Hub-Signature-256", sign(secret, body))
		} else {
			req.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	// Bad signature → 401.
	if code, _ := post("issues", mkIssuePayload("swe-solve"), false); code != http.StatusUnauthorized {
		t.Errorf("bad signature: code = %d, want 401", code)
	}
	// Labeled issue → 200, one rule matched.
	code, out := post("issues", mkIssuePayload("swe-solve"), true)
	if code != http.StatusOK {
		t.Fatalf("code = %d, want 200", code)
	}
	if m, _ := out["rules_matched"].(float64); m != 1 {
		t.Errorf("rules_matched = %v, want 1", out["rules_matched"])
	}
	// Unlabeled issue → 200, no match.
	_, out = post("issues", mkIssuePayload("bug"), true)
	if m, _ := out["rules_matched"].(float64); m != 0 {
		t.Errorf("unlabeled rules_matched = %v, want 0", out["rules_matched"])
	}
}
