// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tosin2013/helmdeck/internal/gateway"
	"github.com/tosin2013/helmdeck/internal/session"
	"github.com/tosin2013/helmdeck/internal/session/fake"
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

// TestSessionListerAdapter_List covers the helmdeck://sessions adapter:
// it must shape session.Session into mcp.SessionView (id/status/image/
// created_at) and propagate runtime errors. An empty runtime returns []
// so the resource never renders as `null` on the MCP wire.
func TestSessionListerAdapter_List(t *testing.T) {
	rt := fake.New()
	a := sessionListerAdapter{rt: rt}

	out, err := a.List(context.Background())
	if err != nil {
		t.Fatalf("empty List: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("empty runtime List = %+v; want []", out)
	}

	s, err := rt.Create(context.Background(), session.Spec{Image: "browser:1"})
	if err != nil {
		t.Fatal(err)
	}
	out, err = a.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("want 1 view, got %d (%+v)", len(out), out)
	}
	v := out[0]
	if v.ID != s.ID || v.Image != "browser:1" || v.Status != string(session.StatusRunning) {
		t.Errorf("view = %+v; want id=%s image=browser:1 status=running", v, s.ID)
	}
	if v.CreatedAt == "" {
		t.Error("CreatedAt must be RFC3339-formatted, got empty")
	}
	if _, err := time.Parse(time.RFC3339, v.CreatedAt); err != nil {
		t.Errorf("CreatedAt %q is not RFC3339: %v", v.CreatedAt, err)
	}
}

// listerErr is a session.Runtime whose List returns a fixed error so we
// can exercise the error-propagation branch of sessionListerAdapter.
// The other Runtime methods are unused in this test; embedding fake's
// behavior would force us to seed a session we don't want.
type listerErrRuntime struct {
	session.Runtime
	err error
}

func (r listerErrRuntime) List(_ context.Context) ([]*session.Session, error) {
	return nil, r.err
}

func TestSessionListerAdapter_List_PropagatesRuntimeError(t *testing.T) {
	want := errors.New("docker daemon unreachable")
	a := sessionListerAdapter{rt: listerErrRuntime{err: want}}
	_, err := a.List(context.Background())
	if !errors.Is(err, want) {
		t.Errorf("List error = %v; want %v", err, want)
	}
}

// TestImageModelListerAdapter_List covers the helmdeck://image-models
// adapter — the constructor (which today wires the in-tree static
// catalog) plus the per-Model → ImageModelView reshape.
func TestImageModelListerAdapter_List(t *testing.T) {
	a := newImageModelListerAdapter()
	out, err := a.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("static catalog must surface at least one model")
	}
	// The catalog is curated cheapest-first; assert the first entry is
	// a fal-ai/flux/* family entry so the adapter is preserving order
	// rather than reshuffling under us.
	if !strings.HasPrefix(out[0].ID, "fal-ai/") {
		t.Errorf("first model id = %q; want fal-ai/* (catalog is ordered cheapest-first)", out[0].ID)
	}
	for _, m := range out {
		if m.ID == "" || m.Engine == "" || m.Provider == "" {
			t.Errorf("incomplete view: %+v", m)
		}
	}
}

// stubGWProvider is the minimal gateway.Provider needed to exercise
// modelListerAdapter — only Models() is called by AllModels.
type stubGWProvider struct {
	name   string
	models []string
	err    error
}

func (p stubGWProvider) Name() string { return p.name }
func (p stubGWProvider) Models(_ context.Context) ([]string, error) {
	if p.err != nil {
		return nil, p.err
	}
	return p.models, nil
}
func (p stubGWProvider) ChatCompletion(_ context.Context, _ gateway.ChatRequest) (gateway.ChatResponse, error) {
	return gateway.ChatResponse{}, errors.New("not used in this test")
}

// TestModelListerAdapter_List covers helmdeck://models — the gateway
// formats IDs as "<provider>/<model>"; the adapter parses the first
// segment back into ModelView.Provider so an agent can pin a model
// AND know which gateway provider will resolve it.
func TestModelListerAdapter_List(t *testing.T) {
	reg := gateway.NewRegistry()
	reg.Register(stubGWProvider{name: "openai", models: []string{"gpt-4o", "gpt-4o-mini"}})
	reg.Register(stubGWProvider{name: "anthropic", models: []string{"claude-sonnet-4-6"}})

	a := newModelListerAdapter(reg)
	out, err := a.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("want 3 views (2 openai + 1 anthropic), got %d (%+v)", len(out), out)
	}
	byID := map[string]string{}
	for _, v := range out {
		byID[v.ID] = v.Provider
	}
	cases := map[string]string{
		"openai/gpt-4o":               "openai",
		"openai/gpt-4o-mini":          "openai",
		"anthropic/claude-sonnet-4-6": "anthropic",
	}
	for id, wantProvider := range cases {
		if got, ok := byID[id]; !ok {
			t.Errorf("missing model id %q in output", id)
		} else if got != wantProvider {
			t.Errorf("model %q provider = %q; want %q", id, got, wantProvider)
		}
	}
}

func TestModelListerAdapter_List_PropagatesRegistryError(t *testing.T) {
	reg := gateway.NewRegistry()
	want := errors.New("upstream models endpoint timed out")
	reg.Register(stubGWProvider{name: "openai", err: want})

	a := newModelListerAdapter(reg)
	_, err := a.List(context.Background())
	if err == nil {
		t.Fatal("want error from provider Models() to propagate")
	}
	// AllModels wraps with "provider <name>: <err>" — confirm both ends.
	if !strings.Contains(err.Error(), "openai") || !errors.Is(err, want) {
		t.Errorf("err = %v; want wrapped error containing provider name and original err", err)
	}
}
