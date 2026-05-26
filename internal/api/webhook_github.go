// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package api

// webhook_github.go (ADR 033) — GitHub webhook receiver.
//
// POST /api/v1/webhooks/github receives events from GitHub, validates the
// HMAC-SHA256 signature against HELMDECK_GITHUB_WEBHOOK_SECRET, and
// dispatches a configured pack. The endpoint returns 200 immediately
// (async dispatch) so GitHub doesn't hit its ~10s delivery timeout.
//
// Events:
//   - push / pull_request → dispatch a pack with {url, ref} (the original
//     ADR 033 Phase 1 behavior).
//   - issues / issue_comment → swe.solve auto-trigger (#233 Phase 6):
//     when an issue carries the configured trigger label, dispatch
//     swe.solve with the issue as the task, then post the resulting PR /
//     summary back as an issue comment. "Label an issue, get a PR."
//
// Rules come from HELMDECK_GITHUB_WEBHOOK_RULES (a JSON array). Dispatch
// runs on a detached context with its own timeout — the request context
// is already cancelled by the time the goroutine runs, so a long pack
// (swe.solve takes minutes) must not borrow it.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/tosin2013/helmdeck/internal/packs"
)

// WebhookRule maps a GitHub event to a pack dispatch.
type WebhookRule struct {
	Event  string          `json:"event"`            // "push", "pull_request", "issues", "issue_comment"
	Ref    string          `json:"ref,omitempty"`    // optional ref filter, e.g. "refs/heads/main"
	Action string          `json:"action,omitempty"` // optional action filter, e.g. "opened", "labeled", "created"
	Label  string          `json:"label,omitempty"`  // optional: for issues events, the issue must carry this label
	Pack   string          `json:"pack"`             // pack to dispatch, e.g. "swe.solve"
	Args   json.RawMessage `json:"args,omitempty"`   // pack input; merged over the event-derived input (operator fields win)
}

const maxWebhookPayload = 5 << 20 // 5 MiB

// webhookDispatchTimeout bounds an async dispatch. swe.solve runs an
// agent loop that can take many minutes; the request context can't be
// used (it's cancelled when the 200 returns), so dispatch gets its own.
const webhookDispatchTimeout = 30 * time.Minute

// webhookPayload is the subset of the GitHub event payload the receiver
// reads across all supported event types.
type webhookPayload struct {
	Ref    string `json:"ref"`
	Action string `json:"action"`
	Repo   struct {
		FullName string `json:"full_name"`
		CloneURL string `json:"clone_url"`
	} `json:"repository"`
	Issue struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	} `json:"issue"`
	Comment struct {
		Body string `json:"body"`
	} `json:"comment"`
}

func registerGitHubWebhookRoute(mux *http.ServeMux, deps Deps) {
	secret := os.Getenv("HELMDECK_GITHUB_WEBHOOK_SECRET")
	if secret == "" {
		// Read from _FILE variant
		if path := os.Getenv("HELMDECK_GITHUB_WEBHOOK_SECRET_FILE"); path != "" {
			b, err := os.ReadFile(path)
			if err == nil {
				secret = strings.TrimSpace(string(b))
			}
		}
	}

	mux.HandleFunc("POST /api/v1/webhooks/github", func(w http.ResponseWriter, r *http.Request) {
		if secret == "" {
			writeError(w, http.StatusServiceUnavailable, "webhook_not_configured",
				"HELMDECK_GITHUB_WEBHOOK_SECRET not set")
			return
		}

		// Read + validate payload
		body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookPayload+1))
		if err != nil {
			writeError(w, http.StatusBadRequest, "read_body", err.Error())
			return
		}
		if len(body) > maxWebhookPayload {
			writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large",
				fmt.Sprintf("payload exceeds %d bytes", maxWebhookPayload))
			return
		}

		// HMAC-SHA256 signature validation
		sig := r.Header.Get("X-Hub-Signature-256")
		if sig == "" {
			writeError(w, http.StatusUnauthorized, "missing_signature",
				"X-Hub-Signature-256 header required")
			return
		}
		if !verifyGitHubSignature(secret, sig, body) {
			writeError(w, http.StatusUnauthorized, "invalid_signature",
				"HMAC signature verification failed")
			return
		}

		eventType := r.Header.Get("X-GitHub-Event")
		deliveryID := r.Header.Get("X-GitHub-Delivery")

		// Parse rules from env
		rules := parseWebhookRules()
		if len(rules) == 0 {
			deps.Logger.Info("github webhook received but no rules configured",
				"event", eventType, "delivery", deliveryID)
			writeJSON(w, http.StatusOK, map[string]string{
				"status":   "accepted",
				"message":  "no rules configured — event logged but not dispatched",
				"delivery": deliveryID,
			})
			return
		}

		var payload webhookPayload
		_ = json.Unmarshal(body, &payload)

		// Match rules and dispatch
		matched := 0
		for _, rule := range rules {
			if !ruleMatches(rule, eventType, payload) {
				continue
			}
			input, ok := buildWebhookInput(rule, eventType, payload, deliveryID)
			if !ok {
				deps.Logger.Warn("github webhook: could not build pack input",
					"event", eventType, "pack", rule.Pack, "delivery", deliveryID)
				continue
			}

			matched++
			deps.Logger.Info("github webhook dispatching pack",
				"event", eventType,
				"action", payload.Action,
				"repo", payload.Repo.FullName,
				"pack", rule.Pack,
				"delivery", deliveryID,
			)

			// Async dispatch on a DETACHED context — r.Context() is
			// cancelled the moment we return 200 below, so a long pack
			// (swe.solve) would be killed instantly if it borrowed it.
			go dispatchWebhookPack(deps, rule, eventType, input, payload, deliveryID)
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"status":        "accepted",
			"event":         eventType,
			"delivery":      deliveryID,
			"rules_matched": matched,
		})
	})
}

// ruleMatches reports whether a rule applies to this event: event type,
// optional ref/action filters, and (for issue events) an optional label
// the issue must carry.
func ruleMatches(rule WebhookRule, eventType string, payload webhookPayload) bool {
	if rule.Event != eventType {
		return false
	}
	if rule.Ref != "" && rule.Ref != payload.Ref {
		return false
	}
	if rule.Action != "" && rule.Action != payload.Action {
		return false
	}
	if rule.Label != "" && !issueHasLabel(payload, rule.Label) {
		return false
	}
	return true
}

func issueHasLabel(payload webhookPayload, label string) bool {
	for _, l := range payload.Issue.Labels {
		if l.Name == label {
			return true
		}
	}
	return false
}

// buildWebhookInput derives the pack input for an event, then merges the
// rule's static Args over it so operator-supplied fields (mode,
// credential, model) win. Returns ok=false when the event lacks the data
// the pack needs (e.g. no clone URL).
func buildWebhookInput(rule WebhookRule, eventType string, payload webhookPayload, deliveryID string) (json.RawMessage, bool) {
	base := map[string]any{}
	switch eventType {
	case "issues", "issue_comment":
		if payload.Repo.CloneURL == "" {
			return nil, false
		}
		// For issue_comment the actionable text is the comment; for an
		// issue it's the title + body. swe.solve consumes repo_url + task.
		task := strings.TrimSpace(payload.Issue.Title + "\n\n" + payload.Issue.Body)
		if eventType == "issue_comment" && strings.TrimSpace(payload.Comment.Body) != "" {
			task = strings.TrimSpace(payload.Comment.Body)
		}
		if task == "" {
			return nil, false
		}
		base["repo_url"] = payload.Repo.CloneURL
		base["task"] = task
		// Default to opening a PR — the headline "issue → PR" flow.
		base["mode"] = "pull_request"
	default: // push, pull_request, and any future ref-shaped event
		if payload.Repo.CloneURL == "" && len(rule.Args) == 0 {
			return nil, false
		}
		base["url"] = payload.Repo.CloneURL
		base["ref"] = payload.Ref
		base["_webhook_event"] = eventType
		base["_webhook_delivery"] = deliveryID
	}

	// Merge rule.Args over the base (operator fields win).
	if len(rule.Args) > 0 {
		var overrides map[string]any
		if err := json.Unmarshal(rule.Args, &overrides); err != nil {
			return nil, false
		}
		for k, v := range overrides {
			base[k] = v
		}
	}
	out, err := json.Marshal(base)
	if err != nil {
		return nil, false
	}
	return out, true
}

// dispatchWebhookPack runs the matched pack on a detached, timed context
// and, for issue events, posts the outcome back as an issue comment.
func dispatchWebhookPack(deps Deps, rule WebhookRule, eventType string, input json.RawMessage, payload webhookPayload, deliveryID string) {
	if deps.PackRegistry == nil || deps.PackEngine == nil {
		deps.Logger.Warn("webhook dispatch skipped: pack engine not configured")
		return
	}
	pack, err := deps.PackRegistry.Get(rule.Pack, "")
	if err != nil {
		deps.Logger.Warn("webhook dispatch: pack not found", "pack", rule.Pack, "err", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), webhookDispatchTimeout)
	defer cancel()

	res, err := deps.PackEngine.Execute(ctx, pack, input)
	if err != nil {
		deps.Logger.Warn("webhook dispatch: pack failed",
			"pack", rule.Pack, "delivery", deliveryID, "err", err)
	} else {
		deps.Logger.Info("webhook dispatch: pack succeeded",
			"pack", rule.Pack, "delivery", deliveryID)
	}

	// For issue events, report the outcome back on the issue.
	if eventType == "issues" || eventType == "issue_comment" {
		postIssueResultComment(ctx, deps, rule, payload, res, err)
	}
}

// postIssueResultComment posts a comment on the triggering issue with the
// pack result — the PR link + summary on success, or the error on
// failure. Best-effort: it needs a github credential (from the rule's
// args) and the github.post_comment pack; missing either just logs.
func postIssueResultComment(ctx context.Context, deps Deps, rule WebhookRule, payload webhookPayload, res *packs.Result, dispatchErr error) {
	if payload.Repo.FullName == "" || payload.Issue.Number == 0 {
		return
	}
	cred := webhookRuleCredential(rule)
	if cred == "" {
		deps.Logger.Info("webhook: no credential in rule args; skipping result comment",
			"repo", payload.Repo.FullName, "issue", payload.Issue.Number)
		return
	}
	cpack, err := deps.PackRegistry.Get("github.post_comment", "")
	if err != nil {
		deps.Logger.Warn("webhook: github.post_comment unavailable", "err", err)
		return
	}

	commentInput, err := json.Marshal(map[string]any{
		"repo":         payload.Repo.FullName,
		"issue_number": payload.Issue.Number,
		"body":         webhookResultBody(res, dispatchErr),
		"credential":   cred,
	})
	if err != nil {
		return
	}
	if _, err := deps.PackEngine.Execute(ctx, cpack, commentInput); err != nil {
		deps.Logger.Warn("webhook: posting result comment failed",
			"repo", payload.Repo.FullName, "issue", payload.Issue.Number, "err", err)
	}
}

// webhookRuleCredential pulls the "credential" field out of a rule's
// Args so the result comment posts as the same identity swe.solve used.
func webhookRuleCredential(rule WebhookRule) string {
	if len(rule.Args) == 0 {
		return ""
	}
	var a struct {
		Credential string `json:"credential"`
	}
	_ = json.Unmarshal(rule.Args, &a)
	return a.Credential
}

// webhookResultBody renders the issue comment for a swe.solve dispatch.
func webhookResultBody(res *packs.Result, dispatchErr error) string {
	if dispatchErr != nil {
		return "🛠️ **helmdeck swe.solve** could not complete this task:\n\n```\n" +
			dispatchErr.Error() + "\n```\n\n_No changes were pushed._"
	}
	var out struct {
		Success    bool   `json:"success"`
		Summary    string `json:"summary"`
		PRURL      string `json:"pr_url"`
		Branch     string `json:"branch"`
		Trajectory string `json:"trajectory_artifact_key"`
	}
	if res != nil {
		_ = json.Unmarshal(res.Output, &out)
	}
	var b strings.Builder
	if out.Success {
		b.WriteString("🛠️ **helmdeck swe.solve** finished.\n\n")
	} else {
		b.WriteString("🛠️ **helmdeck swe.solve** ran but did not converge on a fix.\n\n")
	}
	if out.Summary != "" {
		b.WriteString(out.Summary + "\n\n")
	}
	if out.PRURL != "" {
		b.WriteString("**Pull request:** " + out.PRURL + "\n\n")
	} else if out.Branch != "" {
		b.WriteString("**Branch:** `" + out.Branch + "`\n\n")
	}
	if out.Trajectory != "" {
		b.WriteString("_Trajectory artifact:_ `" + out.Trajectory + "`\n")
	}
	return b.String()
}

func verifyGitHubSignature(secret, signature string, payload []byte) bool {
	// GitHub sends "sha256=<hex>"
	if !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	expected := strings.TrimPrefix(signature, "sha256=")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	actual := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(actual))
}

func parseWebhookRules() []WebhookRule {
	raw := os.Getenv("HELMDECK_GITHUB_WEBHOOK_RULES")
	if raw == "" {
		return nil
	}
	var rules []WebhookRule
	if err := json.Unmarshal([]byte(raw), &rules); err != nil {
		return nil
	}
	return rules
}
