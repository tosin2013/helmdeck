// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package vault

// validate.go — provider-specific credential precheck helpers.
//
// Motivation: paid-API packs (slides.narrate ElevenLabs, image_generate
// fal.ai, research.deep Firecrawl, heygen_video, runway_video, …) all
// share the same failure mode when their vault-stored API key is dead
// (expired, revoked, quota exhausted): the pack burns through expensive
// upstream work — LLM gateway calls for prompt/metadata, Marp render,
// document parsing — and then surfaces a generic "TTS failed, falling
// back to silence" warning at the bottom of the pipeline. The operator
// sees a video without audio (or an artifact with placeholder content)
// and no obvious signal that the credential is what's wrong.
//
// The fix shape: every paid-API pack handler makes ONE cheap GET to the
// provider's "who am I" endpoint at the top of its work, BEFORE any
// expensive call. If the credential is rejected (401/403/quota), the
// handler returns CodeCredentialInvalid immediately. The gateway
// classifier (classify.go) maps that to FailureCallerFixable with a
// "update the vault" reason, and the operator fixes the key without
// burning a deck-worth of LLM tokens first.
//
// This file ships ValidateElevenLabs as the first concrete adopter
// (slides.narrate). The signature template is reusable for sibling
// providers — each gets its own small function that knows the right
// "ping" endpoint and how to read that provider's error envelope.
// Centralizing the lore in one file keeps each pack handler's call
// site tight: `if err := vault.ValidateElevenLabs(ctx, hc, key); err
// != nil { return nil, err }`.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/tosin2013/helmdeck/internal/packs"
)

// elevenLabsBaseURL is duplicated from the slides.narrate package so
// the validator stays self-contained; the two values must match.
const elevenLabsBaseURL = "https://api.elevenlabs.io"

// ValidateElevenLabs makes a single GET /v1/user against the
// ElevenLabs API to confirm the provided key is accepted before the
// caller burns more expensive work (LLM metadata calls, Marp render,
// per-slide TTS round-trips). The call is cheap by design — it
// returns a small JSON describing the account and never bills
// against the TTS character quota.
//
// Return contract:
//
//   - nil error → the key is valid. Caller proceeds.
//   - *packs.PackError{Code: CodeCredentialInvalid} → upstream
//     returned 401 / 403 / 402-quota. Caller MUST stop and surface
//     this; classify.go maps it to FailureCallerFixable with a
//     "update the vault" reason.
//   - other non-nil error → transient network blip, DNS failure,
//     upstream 5xx, etc. The handler decides: log + proceed (the
//     per-slide TTS calls themselves retry on transient errors via
//     normal HTTP retry semantics), or treat as a hard error if the
//     workload can't degrade. Today slides.narrate proceeds — if
//     ElevenLabs is intermittently flaky the per-slide loop's
//     existing fallback-to-silence path catches it.
//
// hc is required. Callers pass the same *http.Client they'll use
// for the real TTS calls (gateway egress guard, timeouts,
// transport pool) so precheck behavior matches production behavior
// — a key that validates here will work in the per-slide loop too.
func ValidateElevenLabs(ctx context.Context, hc *http.Client, apiKey string) error {
	if hc == nil {
		hc = http.DefaultClient
	}
	if strings.TrimSpace(apiKey) == "" {
		return &packs.PackError{Code: packs.CodeCredentialInvalid,
			Message: "ElevenLabs API key is empty — vault hydration may have failed or HELMDECK_ELEVENLABS_API_KEY is unset"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, elevenLabsBaseURL+"/v1/user", nil)
	if err != nil {
		return fmt.Errorf("build elevenlabs validate request: %w", err)
	}
	req.Header.Set("xi-api-key", apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		// Network blip / DNS failure / context cancellation. NOT
		// a credential problem; surface as a generic transient
		// error and let the caller decide whether to proceed.
		return fmt.Errorf("elevenlabs validate transport: %w", err)
	}
	defer resp.Body.Close()
	// Drain the body up to a small cap so the connection can be
	// reused from the transport pool. The response is small (~1
	// KB) so we don't need to be defensive about huge bodies.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))

	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusPaymentRequired:
		// 401 = key rejected; 403 = key valid but lacks permission
		// (e.g. tier doesn't include TTS); 402 = payment required
		// / quota exhausted. All three are the same outcome from
		// the caller's perspective: the credential will not produce
		// TTS audio in this deployment, so the pack must stop
		// before doing more work.
		return &packs.PackError{Code: packs.CodeCredentialInvalid,
			Message: fmt.Sprintf("ElevenLabs rejected the stored API key (%d): %s. Update the credential via /api/v1/vault/credentials/{id} or rotate HELMDECK_ELEVENLABS_API_KEY in .env.local and restart the control-plane.",
				resp.StatusCode, truncateForMessage(string(body), 200))}
	case http.StatusTooManyRequests:
		// Rate limit on the precheck endpoint specifically. The
		// real TTS calls have their own quota path; we don't want
		// the precheck to falsely block a deployment that's just
		// rate-limited on the user lookup. Treat as transient.
		return fmt.Errorf("elevenlabs validate rate-limited (429): %s", truncateForMessage(string(body), 200))
	default:
		// 5xx, unexpected status. Don't block — log up the call
		// chain and let the real TTS calls succeed or fall through
		// their existing retry path.
		return fmt.Errorf("elevenlabs validate unexpected status %d: %s",
			resp.StatusCode, truncateForMessage(string(body), 200))
	}
}

// truncateForMessage clips a response body for inclusion in an
// operator-facing error message. Keeps error envelopes intact for
// short bodies (most provider 401s are <100 bytes of JSON) but bounds
// the message length so a misbehaving provider can't blow up the
// audit log with a multi-MB HTML error page.
func truncateForMessage(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
