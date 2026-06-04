// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

//go:build recovery

package reliability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// client.go is a deliberately tiny OpenRouter chat-completions
// caller used by the recovery test (PR H, v0.25.0). The production
// gateway in internal/gateway/ has providers, fallbacks, retries,
// OTel — none of which the test wants. The test is asserting the
// LLM's response to a typed-error envelope; we hit the API directly
// so the harness can't be confused with what's being measured.

const openRouterChatURL = "https://openrouter.ai/api/v1/chat/completions"

// openRouterClient is a thin chat-completions wrapper. Single-
// goroutine — the recovery test runs scenarios sequentially to stay
// under OpenRouter's ~20-req/min free-tier limit.
type openRouterClient struct {
	apiKey     string
	model      string
	httpClient *http.Client
}

func newOpenRouterClient(apiKey, model string) *openRouterClient {
	return &openRouterClient{
		apiKey: apiKey,
		model:  model,
		httpClient: &http.Client{
			// Free-tier responses occasionally take 20–30s; 60s
			// leaves slack without hanging CI forever.
			Timeout: 60 * time.Second,
		},
	}
}

// chatRequest mirrors the OpenAI-compatible body OpenRouter accepts.
// Only the fields the test sets are populated; everything else uses
// OpenRouter defaults.
type chatRequest struct {
	Model          string         `json:"model"`
	Messages       []chatMessage  `json:"messages"`
	ResponseFormat responseFormat `json:"response_format,omitempty"`
	Temperature    float64        `json:"temperature,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Code    int    `json:"code,omitempty"`
	} `json:"error,omitempty"`
}

// recoveryDecision is the structured action the model must return.
// JSON tag names match the schema described in systemPrompt.
type recoveryDecision struct {
	Action         string          `json:"action"`
	Reason         string          `json:"reason"`
	CorrectedInput json.RawMessage `json:"corrected_input,omitempty"`
}

// askForRecovery sends one (system, user) prompt to OpenRouter and
// parses the response into a recoveryDecision. Internally retries on
// 429 with exponential backoff because the free tier has TWO rate
// limits stacked: OpenRouter account-level (16 req/min observed) AND
// upstream provider-level (Kimi/Moonshot can throttle independently).
// Up to 3 retries with 4s/8s/16s backoff covers both — a 429 burst
// usually drains in one window, but the upstream provider sometimes
// needs longer. After retries, the caller counts the attempt as a
// recovery failure (which is itself reliability signal — if the model
// can't be reached, the typed-error contract isn't being tested).
//
// Malformed-output errors (the model returned non-JSON or a non-
// closed-set action) are NOT retried — they're scenario-level signal.
func (c *openRouterClient) askForRecovery(ctx context.Context, system, user string) (*recoveryDecision, error) {
	// Backoff schedule: 15s → 30s → 60s → 120s. The upstream
	// provider error "moonshotai/kimi-k2.6:free is temporarily
	// rate-limited upstream" does not carry a Retry-After header,
	// so we can't be precise — we just give the provider plenty of
	// time to drain. The 4-retry ceiling means total wait per failed
	// call can be 15+30+60+120 = 225s; even a fully-throttled run
	// fits in the 40m workflow timeout.
	const maxRetries = 4
	backoff := 15 * time.Second
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		decision, err := c.attemptOnce(ctx, system, user)
		if err == nil {
			return decision, nil
		}
		lastErr = err
		// Retry only on 429 / transient. Decode errors (malformed
		// JSON) and 4xx other than 429 are fail-fast.
		if !shouldRetry(err) || attempt == maxRetries {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
	}
	return nil, lastErr
}

// shouldRetry returns true for errors worth retrying — currently 429
// rate-limit errors and 5xx server errors. The check is substring-
// based because the error formatting wraps the response body; a
// stronger typed approach would be a custom error type, but for the
// 4-line check this keeps the file small.
func shouldRetry(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "openrouter 429") ||
		strings.Contains(msg, "openrouter 502") ||
		strings.Contains(msg, "openrouter 503") ||
		strings.Contains(msg, "openrouter 504")
}

// attemptOnce does the single-request work. askForRecovery wraps it
// with the retry loop; this keeps the request-shape code uncluttered.
func (c *openRouterClient) attemptOnce(ctx context.Context, system, user string) (*recoveryDecision, error) {
	req := chatRequest{
		Model: c.model,
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		// Force JSON output — OpenRouter forwards this to providers
		// that support it (Kimi does). Models that don't will still
		// honor the "JSON only" instruction in the system prompt;
		// the malformed-output path catches them.
		ResponseFormat: responseFormat{Type: "json_object"},
		// Low temperature: the test measures the model's ability to
		// apply the typed-error rules consistently, not creativity.
		// Higher temperature would inflate variance and make the
		// thresholds harder to calibrate.
		Temperature: 0.2,
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, openRouterChatURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	// OpenRouter surfaces app identity via these two headers; helps
	// them rate-limit per-app when one consumer is noisy.
	httpReq.Header.Set("HTTP-Referer", "https://github.com/tosin2013/helmdeck")
	httpReq.Header.Set("X-Title", "helmdeck recovery test")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openrouter: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("openrouter %d: %s", resp.StatusCode, truncate(string(respBody), 500))
	}

	var chat chatResponse
	if err := json.Unmarshal(respBody, &chat); err != nil {
		return nil, fmt.Errorf("decode chat response: %w (body %q)", err, truncate(string(respBody), 200))
	}
	if chat.Error != nil {
		return nil, fmt.Errorf("openrouter error: %s", chat.Error.Message)
	}
	if len(chat.Choices) == 0 {
		return nil, errors.New("openrouter returned no choices")
	}

	raw := strings.TrimSpace(chat.Choices[0].Message.Content)
	// Some free providers wrap JSON in ```json fences even when
	// response_format=json_object is requested. Unwrap before parse.
	raw = stripCodeFence(raw)
	var decision recoveryDecision
	if err := json.Unmarshal([]byte(raw), &decision); err != nil {
		return nil, fmt.Errorf("model returned non-JSON output (treat as recovery failure): %q",
			truncate(raw, 200))
	}
	return &decision, nil
}

func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		// Drop opening ``` and optional language tag.
		if nl := strings.IndexByte(s, '\n'); nl > 0 {
			s = s[nl+1:]
		}
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSpace(s)
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
