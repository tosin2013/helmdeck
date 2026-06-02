// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package vault

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
)

// elevenLabsMockServer points the validator at httptest's loopback
// origin by overriding the package's baseURL constant via a tiny
// wrapper. We avoid mutating the const by using a direct
// http.NewRequest in the tested function — but the const-bound
// happy-path test below has to monkeypatch. For now run the
// validator against a real-shaped response by binding the test
// server's URL into the request through a small helper.
//
// Since elevenLabsBaseURL is a const we instead test through the
// http.Client's transport: the validator calls
// elevenLabsBaseURL+"/v1/user" which always resolves to
// api.elevenlabs.io. We override that via a custom RoundTripper that
// short-circuits the request to the test server's handler — same
// pattern used elsewhere in the codebase for upstream-mock tests.

type fixedRoundTripper struct {
	handler http.HandlerFunc
}

func (rt fixedRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	rt.handler.ServeHTTP(w, r)
	return w.Result(), nil
}

func clientWithHandler(h http.HandlerFunc) *http.Client {
	return &http.Client{Transport: fixedRoundTripper{handler: h}}
}

// TestValidateElevenLabs_OK — the happy path: provider returns 200,
// validator returns nil. Caller proceeds to the expensive work.
func TestValidateElevenLabs_OK(t *testing.T) {
	hc := clientWithHandler(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("xi-api-key"); got != "test-key" {
			t.Errorf("xi-api-key header missing or wrong: %q", got)
		}
		if r.URL.Path != "/v1/user" {
			t.Errorf("validator should hit /v1/user; got %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"xi_api_key":"test-key"}`))
	})
	if err := ValidateElevenLabs(context.Background(), hc, "test-key"); err != nil {
		t.Errorf("happy path should return nil; got %v", err)
	}
}

// TestValidateElevenLabs_401 — the motivating case: stored key is
// dead (expired, revoked, never valid). Must return
// CodeCredentialInvalid so classify.go routes to FailureCallerFixable
// with an actionable "update the vault" reason — NOT FailurePackBug.
func TestValidateElevenLabs_401(t *testing.T) {
	hc := clientWithHandler(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":"Invalid API key"}`))
	})
	err := ValidateElevenLabs(context.Background(), hc, "rejected-key")
	if err == nil {
		t.Fatal("401 should produce an error")
	}
	pe, ok := err.(*packs.PackError)
	if !ok {
		t.Fatalf("401 should produce *packs.PackError; got %T", err)
	}
	if pe.Code != packs.CodeCredentialInvalid {
		t.Errorf("401 should map to CodeCredentialInvalid; got %s", pe.Code)
	}
	// Operator-facing message must include the provider's error
	// detail so the cause is clear without spelunking logs.
	if !contains(pe.Message, "Invalid API key") {
		t.Errorf("message should include provider error detail; got %q", pe.Message)
	}
}

// TestValidateElevenLabs_403 — 403 = key valid but lacks the
// permission this pack needs. Same caller-fixable outcome as 401.
func TestValidateElevenLabs_403(t *testing.T) {
	hc := clientWithHandler(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"detail":"Tier doesn't include TTS"}`))
	})
	err := ValidateElevenLabs(context.Background(), hc, "limited-key")
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeCredentialInvalid {
		t.Errorf("403 should map to CodeCredentialInvalid; got %v", err)
	}
}

// TestValidateElevenLabs_402_QuotaExhausted — 402 Payment Required
// is how providers signal "your account is out of credits / over
// the monthly cap." Same caller fix (top up / wait for reset) — no
// reason to drag through expensive work.
func TestValidateElevenLabs_402_QuotaExhausted(t *testing.T) {
	hc := clientWithHandler(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"detail":"Quota exhausted"}`))
	})
	err := ValidateElevenLabs(context.Background(), hc, "depleted-key")
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeCredentialInvalid {
		t.Errorf("402 should map to CodeCredentialInvalid; got %v", err)
	}
}

// TestValidateElevenLabs_429_RateLimited — rate limit on the
// PRECHECK endpoint specifically. The real TTS calls have their own
// quota path; we don't want a rate-limited /v1/user lookup to
// falsely block a deployment that's just being throttled on user
// info. Treated as transient — caller proceeds.
func TestValidateElevenLabs_429_RateLimited(t *testing.T) {
	hc := clientWithHandler(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`Rate limit exceeded`))
	})
	err := ValidateElevenLabs(context.Background(), hc, "fine-key")
	if err == nil {
		t.Fatal("429 should still produce an error so the caller can log it")
	}
	if _, isPackError := err.(*packs.PackError); isPackError {
		t.Errorf("429 must NOT map to CodeCredentialInvalid (transient on precheck endpoint only); got *PackError")
	}
}

// TestValidateElevenLabs_5xx_Transient — provider 5xx is upstream
// flake, not a credential problem. Don't block; the per-slide TTS
// loop's own retry/fallback handles real outages.
func TestValidateElevenLabs_5xx_Transient(t *testing.T) {
	hc := clientWithHandler(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	err := ValidateElevenLabs(context.Background(), hc, "fine-key")
	if err == nil {
		t.Fatal("5xx should produce a non-PackError so caller can decide to proceed")
	}
	if _, isPackError := err.(*packs.PackError); isPackError {
		t.Errorf("5xx must NOT map to CodeCredentialInvalid; got *PackError")
	}
}

// TestValidateElevenLabs_EmptyKey — empty key short-circuits without
// hitting the network. CodeCredentialInvalid because a missing key
// is functionally the same outcome for the caller as a rejected one.
func TestValidateElevenLabs_EmptyKey(t *testing.T) {
	called := false
	hc := clientWithHandler(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})
	err := ValidateElevenLabs(context.Background(), hc, "")
	if called {
		t.Error("empty key should short-circuit without hitting the network")
	}
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeCredentialInvalid {
		t.Errorf("empty key should map to CodeCredentialInvalid; got %v", err)
	}
}

// TestValidateElevenLabs_WhitespaceKey — same short-circuit for
// whitespace-only keys. Env-var hydration can produce these from
// stray newlines in heredoc-style .env files.
func TestValidateElevenLabs_WhitespaceKey(t *testing.T) {
	hc := clientWithHandler(func(w http.ResponseWriter, r *http.Request) {})
	err := ValidateElevenLabs(context.Background(), hc, "   \n  ")
	pe, ok := err.(*packs.PackError)
	if !ok || pe.Code != packs.CodeCredentialInvalid {
		t.Errorf("whitespace-only key should map to CodeCredentialInvalid; got %v", err)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
