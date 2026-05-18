package vault

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
)

// stubLookup returns an EnvLookup backed by a static map keyed by the
// env-var name. The file-key path is ignored (no test exercises it
// because envOrFile dual-mode is the caller's concern).
func stubLookup(values map[string]string) EnvLookup {
	return func(envKey, _ string) string {
		return values[envKey]
	}
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestUpsertByName_CreatePath is the "no existing row" branch: insert
// fresh, return created=true with a usable record.
func TestUpsertByName_CreatePath(t *testing.T) {
	v := newTestStore(t)
	ctx := context.Background()
	rec, created, err := v.UpsertByName(ctx, CreateInput{
		Name:        "elevenlabs-key",
		Type:        TypeAPIKey,
		HostPattern: "api.elevenlabs.io",
		Plaintext:   []byte("sk_test_one"),
		Metadata:    map[string]any{"source": "env-hydrate"},
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if !created {
		t.Error("created should be true on first upsert")
	}
	if rec.Name != "elevenlabs-key" || rec.HostPattern != "api.elevenlabs.io" {
		t.Errorf("record fields wrong: %+v", rec)
	}
}

// TestUpsertByName_UpdatePath is the "row exists" branch: rotate
// ciphertext + refresh patterns/metadata, return created=false, keep
// the original ID and CreatedAt.
func TestUpsertByName_UpdatePath(t *testing.T) {
	v := newTestStore(t)
	ctx := context.Background()
	first, _, err := v.UpsertByName(ctx, CreateInput{
		Name:        "elevenlabs-key",
		Type:        TypeAPIKey,
		HostPattern: "api.elevenlabs.io",
		Plaintext:   []byte("sk_test_one"),
	})
	if err != nil {
		t.Fatalf("upsert 1: %v", err)
	}
	second, created, err := v.UpsertByName(ctx, CreateInput{
		Name:        "elevenlabs-key",
		Type:        TypeAPIKey,
		HostPattern: "api.elevenlabs.io",
		Plaintext:   []byte("sk_test_TWO"),
		Metadata:    map[string]any{"source": "env-hydrate"},
	})
	if err != nil {
		t.Fatalf("upsert 2: %v", err)
	}
	if created {
		t.Error("created should be false on update")
	}
	if second.ID != first.ID {
		t.Errorf("ID changed across upsert: %s -> %s", first.ID, second.ID)
	}
	if !second.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("CreatedAt mutated: %s -> %s", first.CreatedAt, second.CreatedAt)
	}
	if second.Fingerprint == first.Fingerprint {
		t.Error("fingerprint should change after rotating plaintext")
	}
	// Confirm the new plaintext is what comes back via Resolve. We
	// need a wildcard grant first because Create/Upsert doesn't add
	// one — that's HydrateFromEnv's responsibility.
	if err := v.Grant(ctx, second.ID, Grant{ActorSubject: "*"}); err != nil {
		t.Fatalf("grant: %v", err)
	}
	res, err := v.ResolveByName(ctx, Actor{Subject: "anyone"}, "elevenlabs-key")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if string(res.Plaintext) != "sk_test_TWO" {
		t.Errorf("plaintext after upsert = %q, want sk_test_TWO", string(res.Plaintext))
	}
}

// TestHydrateFromEnv_CreatesAndGrants asserts that the first hydrate
// run for a present env var creates the credential, stamps it with
// metadata.source=env-hydrate, and adds a wildcard ACL so packs can
// resolve it without further setup.
func TestHydrateFromEnv_CreatesAndGrants(t *testing.T) {
	v := newTestStore(t)
	ctx := context.Background()
	c, u, s := v.HydrateFromEnv(ctx, quietLogger(), stubLookup(map[string]string{
		"HELMDECK_ELEVENLABS_API_KEY": "sk_from_env",
	}))
	if c != 1 || u != 0 || s != 0 {
		t.Errorf("hydrate counters = (created=%d updated=%d skipped=%d), want (1,0,0)", c, u, s)
	}
	rec, err := v.GetByName(ctx, "elevenlabs-key")
	if err != nil {
		t.Fatalf("get by name: %v", err)
	}
	if got, _ := rec.Metadata["source"].(string); got != "env-hydrate" {
		t.Errorf("metadata.source = %q, want env-hydrate", got)
	}
	// Wildcard grant means any caller can resolve.
	res, err := v.ResolveByName(ctx, Actor{Subject: "any-caller"}, "elevenlabs-key")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if string(res.Plaintext) != "sk_from_env" {
		t.Errorf("plaintext = %q, want sk_from_env", string(res.Plaintext))
	}
}

// TestHydrateFromEnv_SkipsUserManaged is the safety check: if an
// operator already created elevenlabs-key by hand (no source metadata,
// or metadata.source != "env-hydrate"), a subsequent restart with the
// env var set must NOT clobber it.
func TestHydrateFromEnv_SkipsUserManaged(t *testing.T) {
	v := newTestStore(t)
	ctx := context.Background()
	// Operator-created entry (no source stamp).
	rec, err := v.Create(ctx, CreateInput{
		Name:        "elevenlabs-key",
		Type:        TypeAPIKey,
		HostPattern: "api.elevenlabs.io",
		Plaintext:   []byte("sk_user_managed"),
		Metadata:    map[string]any{"created_via": "ui"},
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	_ = v.Grant(ctx, rec.ID, Grant{ActorSubject: "*"})

	c, u, s := v.HydrateFromEnv(ctx, quietLogger(), stubLookup(map[string]string{
		"HELMDECK_ELEVENLABS_API_KEY": "sk_from_env_DIFFERENT",
	}))
	if c != 0 || u != 0 || s != 1 {
		t.Errorf("hydrate counters = (created=%d updated=%d skipped=%d), want (0,0,1)", c, u, s)
	}
	// Plaintext must still be the operator-managed value.
	res, err := v.ResolveByName(ctx, Actor{Subject: "anyone"}, "elevenlabs-key")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if string(res.Plaintext) != "sk_user_managed" {
		t.Errorf("user-managed plaintext was clobbered: got %q, want sk_user_managed",
			string(res.Plaintext))
	}
}

// TestHydrateFromEnv_UpdatesPriorEnvHydrate is the natural follow-on:
// when an entry was created by env-hydrate and the env var changed
// across restarts, the stored credential should track the new value.
func TestHydrateFromEnv_UpdatesPriorEnvHydrate(t *testing.T) {
	v := newTestStore(t)
	ctx := context.Background()
	v.HydrateFromEnv(ctx, quietLogger(), stubLookup(map[string]string{
		"HELMDECK_ELEVENLABS_API_KEY": "sk_v1",
	}))
	c, u, s := v.HydrateFromEnv(ctx, quietLogger(), stubLookup(map[string]string{
		"HELMDECK_ELEVENLABS_API_KEY": "sk_v2",
	}))
	if c != 0 || u != 1 || s != 0 {
		t.Errorf("second hydrate counters = (created=%d updated=%d skipped=%d), want (0,1,0)", c, u, s)
	}
	res, err := v.ResolveByName(ctx, Actor{Subject: "anyone"}, "elevenlabs-key")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if string(res.Plaintext) != "sk_v2" {
		t.Errorf("plaintext after re-hydrate = %q, want sk_v2", string(res.Plaintext))
	}
}

// TestHydrateFromEnv_NoEnvVarIsNoOp covers the common case where the
// env var simply isn't set — nothing should be inserted, no error.
func TestHydrateFromEnv_NoEnvVarIsNoOp(t *testing.T) {
	v := newTestStore(t)
	ctx := context.Background()
	c, u, s := v.HydrateFromEnv(ctx, quietLogger(), stubLookup(nil))
	if c != 0 || u != 0 || s != 0 {
		t.Errorf("counters = (%d,%d,%d), want all zero", c, u, s)
	}
	if _, err := v.GetByName(ctx, "elevenlabs-key"); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound for elevenlabs-key, got %v", err)
	}
}

// TestWellKnownEnvCredentials_PexelsRegistered guards against the
// v0.13.0 cycle's #230 regression: stock.search's CHANGELOG advertised
// HELMDECK_PEXELS_API_KEY auto-hydration, but the entry was missed in
// internal/vault/hydrate.go. This test reads the registry directly so
// even if HydrateFromEnv's iteration logic changes, the contract that
// pexels-key is a recognized credential stays pinned.
func TestWellKnownEnvCredentials_PexelsRegistered(t *testing.T) {
	var got *EnvCredential
	for i := range WellKnownEnvCredentials {
		if WellKnownEnvCredentials[i].EnvVar == "HELMDECK_PEXELS_API_KEY" {
			got = &WellKnownEnvCredentials[i]
			break
		}
	}
	if got == nil {
		t.Fatal("HELMDECK_PEXELS_API_KEY not registered in WellKnownEnvCredentials (#230 regression)")
	}
	if got.Name != "pexels-key" {
		t.Errorf("Name = %q, want %q", got.Name, "pexels-key")
	}
	if got.HostPattern != "api.pexels.com" {
		t.Errorf("HostPattern = %q, want %q", got.HostPattern, "api.pexels.com")
	}
	if got.Type != TypeAPIKey {
		t.Errorf("Type = %q, want TypeAPIKey", got.Type)
	}
	if got.EnvVarFile != "HELMDECK_PEXELS_API_KEY_FILE" {
		t.Errorf("EnvVarFile = %q, want HELMDECK_PEXELS_API_KEY_FILE", got.EnvVarFile)
	}
}

// TestHydrateFromEnv_PexelsKey is the end-to-end version of the
// registry test above: with HELMDECK_PEXELS_API_KEY set, the env-hydrate
// pass creates a pexels-key credential under the documented host
// pattern with env-hydrate provenance.
func TestHydrateFromEnv_PexelsKey(t *testing.T) {
	v := newTestStore(t)
	ctx := context.Background()
	c, _, _ := v.HydrateFromEnv(ctx, quietLogger(), stubLookup(map[string]string{
		"HELMDECK_PEXELS_API_KEY": "pexels-fake-token",
	}))
	if c == 0 {
		t.Fatalf("created counter = %d, want ≥1 (pexels-key should be a fresh insert)", c)
	}
	rec, err := v.GetByName(ctx, "pexels-key")
	if err != nil {
		t.Fatalf("get pexels-key: %v", err)
	}
	if rec.HostPattern != "api.pexels.com" {
		t.Errorf("host_pattern = %q, want api.pexels.com", rec.HostPattern)
	}
	if got, _ := rec.Metadata["source"].(string); got != "env-hydrate" {
		t.Errorf("metadata.source = %q, want env-hydrate", got)
	}
	// Wildcard grant: any caller can resolve, matching the elevenlabs/fal pattern.
	res, err := v.ResolveByName(ctx, Actor{Subject: "any-caller"}, "pexels-key")
	if err != nil {
		t.Fatalf("resolve pexels-key: %v", err)
	}
	if string(res.Plaintext) != "pexels-fake-token" {
		t.Errorf("plaintext = %q, want pexels-fake-token", string(res.Plaintext))
	}
}
