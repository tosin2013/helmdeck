// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func mkReqFn(url string) func() (*http.Request, error) {
	return func() (*http.Request, error) {
		return http.NewRequest("POST", url, bytes.NewReader([]byte(`{}`)))
	}
}

// TestColdStartRetry_RetriesThen200 — a service that 503s twice then returns
// 200 (the cold-start-then-ready shape) must succeed via retry.
func TestColdStartRetry_RetriesThen200(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	}))
	defer srv.Close()

	resp, err := coldStartRetry(context.Background(), srv.Client(), mkReqFn(srv.URL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 (should have retried past the 503s)", resp.StatusCode)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("calls = %d, want 3 (two 503s + the 200)", got)
	}
}

// TestColdStartRetry_NoRetryOn4xx5xx — a genuine application error (4xx/500)
// must return immediately, NOT be retried (those aren't cold-start signals).
func TestColdStartRetry_NoRetryOn4xx5xx(t *testing.T) {
	for _, code := range []int{http.StatusBadRequest, http.StatusInternalServerError} {
		t.Run(fmt.Sprintf("%d", code), func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(code)
			}))
			defer srv.Close()
			resp, err := coldStartRetry(context.Background(), srv.Client(), mkReqFn(srv.URL))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			defer resp.Body.Close()
			if got := calls.Load(); got != 1 {
				t.Errorf("calls = %d, want 1 (no retry on %d)", got, code)
			}
		})
	}
}

// TestColdStartRetry_RefusedExhausts — connection refused (nothing listening)
// is the canonical cold-start error: it must be retried (so a still-booting
// service gets a few chances) and the loop must terminate with the real error
// once attempts are exhausted, never hang. Point at a closed port and assert
// it returns a refused error in bounded time.
func TestColdStartRetry_RefusedExhausts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // port now refuses connections, permanently for this test

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	start := time.Now()
	resp, err := coldStartRetry(ctx, &http.Client{Timeout: time.Second}, mkReqFn(url))
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatalf("expected a connection error against a closed port, got nil")
	}
	if !isColdStartErr(err) {
		t.Errorf("error against a closed port should classify as cold-start (refused): %v", err)
	}
	// 4 attempts with 0.5+1+2s backoff ⇒ retried (≥ ~3s), and bounded.
	if elapsed := time.Since(start); elapsed < 3*time.Second {
		t.Errorf("returned in %v — looks like it did not retry the refused connection", elapsed)
	}
}

func TestIsColdStartStatus(t *testing.T) {
	for _, c := range []int{502, 503, 504} {
		if !isColdStartStatus(c) {
			t.Errorf("%d should be cold-start status", c)
		}
	}
	for _, c := range []int{200, 400, 401, 404, 429, 500} {
		if isColdStartStatus(c) {
			t.Errorf("%d should NOT be cold-start status", c)
		}
	}
}
