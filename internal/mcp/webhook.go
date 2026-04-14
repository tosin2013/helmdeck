// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package mcp

// webhook.go — outbound webhook delivery for completed async jobs.
//
// Why this exists: the MCP spec forbids server-initiated
// sampling/createMessage (the only spec mechanism that could "wake
// the LLM" directly). For users who DO want true push-to-LLM
// semantics — instead of polling — we offer an opt-in webhook: the
// caller passes webhook_url + webhook_secret to pack.start (or to
// the SEP-1686 task envelope), and helmdeck POSTs the final result
// to that URL on completion. Receivers (an OpenClaw plugin, a Slack
// bot, a custom A2A bridge) can then re-inject the result into the
// agent's context as a fresh user/system message — which IS a new
// LLM turn.
//
// See docs/integrations/webhooks.md for the full wire contract;
// docs/integrations/openclaw.md and examples/webhook-openclaw/ for
// a copy-pasteable receiver.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// webhookTimeout caps each individual POST attempt. We deliberately
// keep this low (10s) — webhook receivers must respond quickly so a
// slow downstream can't backpressure helmdeck's job goroutine pool.
// Receivers that need to do heavy work should ack fast and process
// the payload asynchronously themselves.
const webhookTimeout = 10 * time.Second

// webhookRetryDelays is the backoff schedule for failed deliveries.
// 0 / 5 / 30 seconds covers transient receiver hiccups (cold start,
// brief network blip) without retrying so aggressively that a
// genuinely-down receiver gets hammered. Total worst-case delivery
// window is ~35s.
var webhookRetryDelays = []time.Duration{0, 5 * time.Second, 30 * time.Second}

// webhookPayload is the body POSTed to the user's webhook URL when
// a job reaches a terminal state. Designed to be self-describing:
// a receiver that's never seen helmdeck before should be able to
// understand event_type, jobId/pack identity, and the embedded
// CallToolResult without needing to fetch anything else.
type webhookPayload struct {
	EventType string                 `json:"event_type"` // "pack.complete" | "pack.failed"
	JobID     string                 `json:"job_id"`
	TaskID    string                 `json:"task_id"`
	Pack      string                 `json:"pack"`
	State     string                 `json:"state"`
	StartedAt string                 `json:"started_at"`
	EndedAt   string                 `json:"ended_at"`
	Result    map[string]any         `json:"result,omitempty"` // present when state == "completed"
	Error     map[string]any         `json:"error,omitempty"`  // present when state == "failed"
	Snapshot  jobSnapshot            `json:"snapshot"`         // the same shape pack.status returns
	Meta      map[string]interface{} `json:"meta,omitempty"`   // reserved for future expansion
}

// fireWebhook delivers the terminal-state payload for j to the
// configured webhook URL. No-op when WebhookURL is empty. Runs
// synchronously inside the goroutine that completed the job — so
// the caller (startAsync) MUST invoke this AFTER releasing the job
// mutex (which it does). Failures are logged but not surfaced;
// webhook delivery is best-effort. Receivers that need stronger
// guarantees should poll tasks/get or pack.status as a fallback.
func (s *PackServer) fireWebhook(j *asyncJob) {
	if j.WebhookURL == "" {
		return
	}
	payload := buildWebhookPayload(s, j)
	body, err := json.Marshal(payload)
	if err != nil {
		// Marshal failure is a programmer bug; nothing to retry.
		return
	}
	sig := signWebhook(body, j.WebhookSecret)
	client := &http.Client{Timeout: webhookTimeout}
	for attempt, delay := range webhookRetryDelays {
		if delay > 0 {
			time.Sleep(delay)
		}
		req, err := http.NewRequest(http.MethodPost, j.WebhookURL, bytes.NewReader(body))
		if err != nil {
			return // malformed URL — no point retrying
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "helmdeck-webhook/1.0")
		req.Header.Set("X-Helmdeck-Event", payload.EventType)
		req.Header.Set("X-Helmdeck-Job-Id", j.ID)
		req.Header.Set("X-Helmdeck-Task-Id", j.taskID())
		req.Header.Set("X-Helmdeck-Delivery-Attempt", fmt.Sprintf("%d", attempt+1))
		if sig != "" {
			req.Header.Set("X-Helmdeck-Signature", "sha256="+sig)
		}
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			// 2xx is success; 4xx is a permanent client error so we
			// stop retrying (the receiver said "don't bother"); 5xx
			// is transient — fall through to the next backoff slot.
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return
			}
			if resp.StatusCode >= 400 && resp.StatusCode < 500 {
				return
			}
		}
		// Last attempt? Stop. Otherwise the loop's next iteration
		// sleeps the next delay and tries again.
	}
}

// buildWebhookPayload pulls the job's terminal state out under its
// mutex (to dodge the caller-must-have-released contract) and
// assembles the wire body. We DO read the job mutex here because
// fireWebhook runs after startAsync's goroutine released it — there
// is no deadlock risk, and a fresh snapshot is more accurate than
// passing in stale values.
func buildWebhookPayload(s *PackServer, j *asyncJob) webhookPayload {
	snap := j.snapshot()
	j.mu.Lock()
	res := j.result
	jobErr := j.err
	j.mu.Unlock()

	eventType := "pack.complete"
	if snap.State == "failed" {
		eventType = "pack.failed"
	}
	out := webhookPayload{
		EventType: eventType,
		JobID:     j.ID,
		TaskID:    j.taskID(),
		Pack:      j.Pack,
		State:     snap.State,
		StartedAt: snap.StartedAt,
		EndedAt:   snap.EndedAt,
		Snapshot:  snap,
	}
	switch snap.State {
	case "completed":
		out.Result = s.packResultAsToolResult(context.Background(), res)
	case "failed":
		out.Error = packErrorAsToolResult(jobErr)
	}
	return out
}

// signWebhook returns the hex-encoded HMAC-SHA256 of body keyed by
// secret, or "" when secret is empty (caller signals "I don't need
// signature verification" by omitting the secret). Receivers MUST
// recompute the same HMAC over the raw request body and compare in
// constant time. The signature scheme matches GitHub/Stripe/Slack
// conventions so receivers can reuse existing libraries.
func signWebhook(body []byte, secret string) string {
	if secret == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
