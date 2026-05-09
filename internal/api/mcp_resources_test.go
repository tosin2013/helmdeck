// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tosin2013/helmdeck/internal/store"
	"github.com/tosin2013/helmdeck/internal/vault"
	"github.com/tosin2013/helmdeck/internal/voices"
)

// stubVoicesAPI returns an httptest server emulating ElevenLabs
// /v1/voices, plus a *uint64 counter the test asserts against to
// verify caching collapses repeated reads into a single upstream call.
func stubVoicesAPI(t *testing.T, body string) (*httptest.Server, *uint64) {
	t.Helper()
	var hits uint64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddUint64(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	prev := voices.ElevenLabsBaseURL
	voices.ElevenLabsBaseURL = srv.URL
	t.Cleanup(func() { voices.ElevenLabsBaseURL = prev })
	return srv, &hits
}

// vaultWithKey seeds a fresh in-memory vault store with the
// elevenlabs-key credential + wildcard ACL so ResolveByName succeeds.
func vaultWithKey(t *testing.T, plaintext string) *vault.Store {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	master := make([]byte, 32)
	v, err := vault.New(db, master)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := v.Create(context.Background(), vault.CreateInput{
		Name:        "elevenlabs-key",
		Type:        vault.TypeAPIKey,
		HostPattern: "api.elevenlabs.io",
		Plaintext:   []byte(plaintext),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Grant(context.Background(), rec.ID, vault.Grant{ActorSubject: "*"}); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestVoiceListerCachingAdapter_CachesUntilTTL(t *testing.T) {
	_, hits := stubVoicesAPI(t, `{"voices":[{"voice_id":"v1","name":"Rachel","labels":{"accent":"american"}}]}`)
	v := vaultWithKey(t, "sk_test")
	a := newVoiceListerCachingAdapter(v, 1*time.Hour)

	for i := 0; i < 5; i++ {
		out, err := a.List(context.Background())
		if err != nil {
			t.Fatalf("List #%d: %v", i, err)
		}
		if len(out) != 1 || out[0].VoiceID != "v1" || out[0].Source != "elevenlabs" {
			t.Errorf("List #%d returned wrong data: %+v", i, out)
		}
	}
	if got := atomic.LoadUint64(hits); got != 1 {
		t.Errorf("upstream hit count = %d, want 1 (cache should have absorbed 4 reads)", got)
	}
}

func TestVoiceListerCachingAdapter_RefetchesAfterTTL(t *testing.T) {
	_, hits := stubVoicesAPI(t, `{"voices":[{"voice_id":"v1","name":"Rachel"}]}`)
	v := vaultWithKey(t, "sk_test")

	// 1ms TTL, plus a fake clock so the test isn't time-dependent.
	a := newVoiceListerCachingAdapter(v, 1*time.Millisecond)
	current := time.Now()
	a.now = func() time.Time { return current }

	if _, err := a.List(context.Background()); err != nil {
		t.Fatalf("first List: %v", err)
	}
	current = current.Add(10 * time.Millisecond) // advance past TTL
	if _, err := a.List(context.Background()); err != nil {
		t.Fatalf("second List: %v", err)
	}
	if got := atomic.LoadUint64(hits); got != 2 {
		t.Errorf("upstream hits after TTL expiry = %d, want 2", got)
	}
}

func TestVoiceListerCachingAdapter_InvalidatesOnKeyRotation(t *testing.T) {
	_, hits := stubVoicesAPI(t, `{"voices":[{"voice_id":"v1","name":"Rachel"}]}`)
	v := vaultWithKey(t, "sk_test_first")
	a := newVoiceListerCachingAdapter(v, 1*time.Hour)

	if _, err := a.List(context.Background()); err != nil {
		t.Fatalf("first List: %v", err)
	}

	// Rotate the credential to a new value (different fingerprint).
	rec, err := v.GetByName(context.Background(), "elevenlabs-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Rotate(context.Background(), rec.ID, []byte("sk_test_SECOND")); err != nil {
		t.Fatal(err)
	}

	if _, err := a.List(context.Background()); err != nil {
		t.Fatalf("post-rotation List: %v", err)
	}
	if got := atomic.LoadUint64(hits); got != 2 {
		t.Errorf("upstream hits after key rotation = %d, want 2 (rotation should invalidate cache)", got)
	}
}

func TestVoiceListerCachingAdapter_PropagatesVaultErrors(t *testing.T) {
	stubVoicesAPI(t, `{"voices":[]}`) // never reached
	v := vaultWithKey(t, "sk_test")
	a := newVoiceListerCachingAdapter(v, time.Hour)

	// Delete the credential so ResolveByName errors.
	rec, _ := v.GetByName(context.Background(), "elevenlabs-key")
	_ = v.Delete(context.Background(), rec.ID)

	_, err := a.List(context.Background())
	if err == nil {
		t.Fatal("want error when credential is missing, got nil")
	}
	if !strings.Contains(err.Error(), "credential not found") {
		t.Errorf("unexpected error: %v", err)
	}
}
