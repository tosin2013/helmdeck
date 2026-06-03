// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

import (
	"context"
	"testing"

	"github.com/tosin2013/helmdeck/internal/vault"
)

// elevenlabs_creds_test.go pins the four-step credential-resolution
// ladder defined in #138. Both LLM packs (podcast.generate,
// slides.narrate) call resolveElevenLabsKey at handler entry, so a
// regression here means BOTH packs lose their credential — silent
// fallback or hard failure depending on the caller's allow_silent_output
// flag. The ladder is small but the precedence matters; this table
// pins it so a future maintainer reordering the lookups gets a
// loud failure pointing at the source step.

// elevenlabsTestEnvUnset clears the env var for the test's duration so
// the env-fallback branch only fires when explicitly seeded. Without
// this, an operator's local env (or a previous test) could leak into
// the resolve and confuse precedence assertions.
func elevenlabsTestEnvUnset(t *testing.T) {
	t.Helper()
	t.Setenv("HELMDECK_ELEVENLABS_API_KEY", "")
}

// TestResolveElevenLabsKey_NoSourcesYieldsEmpty — empty vault + no env
// var + no explicit credential → returns ("", keySrcNone). The
// downstream handlers use the empty result + keySrcNone to format the
// canonical "set the env var or POST a credential" message.
func TestResolveElevenLabsKey_NoSourcesYieldsEmpty(t *testing.T) {
	elevenlabsTestEnvUnset(t)
	v := vaultWithElevenKey(t, "") // empty vault, no creds seeded
	key, src := resolveElevenLabsKey(context.Background(), v, "")
	if key != "" {
		t.Errorf("key = %q; want empty", key)
	}
	if src != keySrcNone {
		t.Errorf("src = %q; want keySrcNone", src)
	}
}

// TestResolveElevenLabsKey_CanonicalVault — the canonical
// `elevenlabs-key` name (env-hydrate's target, #142) is the first
// vault entry tried when no explicit credential is named.
func TestResolveElevenLabsKey_CanonicalVault(t *testing.T) {
	elevenlabsTestEnvUnset(t)
	v := vaultWithElevenKey(t, "sk_canonical")
	key, src := resolveElevenLabsKey(context.Background(), v, "")
	if key != "sk_canonical" {
		t.Errorf("key = %q; want sk_canonical", key)
	}
	if src != keySrcCanonical {
		t.Errorf("src = %q; want keySrcCanonical", src)
	}
}

// TestResolveElevenLabsKey_AliasVault — back-compat alias
// `elevenlabs-api-key` resolves when the canonical name is absent.
// The alias is the third step in the ladder, so this exercises the
// "tried canonical, fell through, hit alias" branch.
func TestResolveElevenLabsKey_AliasVault(t *testing.T) {
	elevenlabsTestEnvUnset(t)
	v := vaultWithElevenAliasKey(t, "sk_alias")
	key, src := resolveElevenLabsKey(context.Background(), v, "")
	if key != "sk_alias" {
		t.Errorf("key = %q; want sk_alias", key)
	}
	if src != keySrcAlias {
		t.Errorf("src = %q; want keySrcAlias", src)
	}
}

// TestResolveElevenLabsKey_ExplicitWins — when the caller passes an
// explicit credential name AND it resolves in vault, that beats the
// canonical/alias/env paths. Important for multi-tenant deployments
// where different callers want different keys.
func TestResolveElevenLabsKey_ExplicitWins(t *testing.T) {
	elevenlabsTestEnvUnset(t)
	v := vaultWithElevenKey(t, "sk_canonical") // canonical seeded
	// Also seed an "explicit" credential under a custom name.
	rec, err := v.Create(context.Background(), vault.CreateInput{
		Name:        "elevenlabs-custom",
		Type:        vault.TypeAPIKey,
		HostPattern: "api.elevenlabs.io",
		Plaintext:   []byte("sk_explicit"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Grant(context.Background(), rec.ID, vault.Grant{ActorSubject: "*"}); err != nil {
		t.Fatal(err)
	}
	key, src := resolveElevenLabsKey(context.Background(), v, "elevenlabs-custom")
	if key != "sk_explicit" {
		t.Errorf("key = %q; want sk_explicit", key)
	}
	if src != keySrcExplicit {
		t.Errorf("src = %q; want keySrcExplicit", src)
	}
}

// TestResolveElevenLabsKey_ExplicitMissingFallsThrough — if the
// caller names a credential that doesn't exist, the ladder continues
// down to canonical/env rather than failing. The rationale (in the
// resolveElevenLabsKey doc): operators sometimes pass `credential` as
// a hint and rely on env-hydrate for the real value.
func TestResolveElevenLabsKey_ExplicitMissingFallsThrough(t *testing.T) {
	elevenlabsTestEnvUnset(t)
	v := vaultWithElevenKey(t, "sk_canonical")
	key, src := resolveElevenLabsKey(context.Background(), v, "no-such-credential")
	if key != "sk_canonical" {
		t.Errorf("key = %q; want sk_canonical (fall-through)", key)
	}
	if src != keySrcCanonical {
		t.Errorf("src = %q; want keySrcCanonical", src)
	}
}

// TestResolveElevenLabsKey_EnvFallback — vault is empty AND no
// explicit cred → env var is the last-resort source. Critical for
// no-vault deployments (early-eval dev, or operators who explicitly
// disable env-hydrate).
func TestResolveElevenLabsKey_EnvFallback(t *testing.T) {
	t.Setenv("HELMDECK_ELEVENLABS_API_KEY", "sk_env")
	v := vaultWithElevenKey(t, "") // empty vault
	key, src := resolveElevenLabsKey(context.Background(), v, "")
	if key != "sk_env" {
		t.Errorf("key = %q; want sk_env", key)
	}
	if src != keySrcEnv {
		t.Errorf("src = %q; want keySrcEnv", src)
	}
}

// TestResolveElevenLabsKey_NilVault — when vs is nil (rare —
// compose deployments without a vault store), the ladder skips the
// three vault steps and falls through directly to env (or empty).
// Without the nil-vault guard, the function would nil-deref on
// ResolveByName and crash the handler.
func TestResolveElevenLabsKey_NilVault(t *testing.T) {
	t.Setenv("HELMDECK_ELEVENLABS_API_KEY", "sk_env_only")
	key, src := resolveElevenLabsKey(context.Background(), nil, "")
	if key != "sk_env_only" {
		t.Errorf("key = %q; want sk_env_only", key)
	}
	if src != keySrcEnv {
		t.Errorf("src = %q; want keySrcEnv", src)
	}

	// Also: nil vault + no env → empty.
	elevenlabsTestEnvUnset(t)
	key, src = resolveElevenLabsKey(context.Background(), nil, "")
	if key != "" {
		t.Errorf("key = %q; want empty", key)
	}
	if src != keySrcNone {
		t.Errorf("src = %q; want keySrcNone", src)
	}
}
