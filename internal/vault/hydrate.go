// Package vault — env-hydrate registry (#142).
//
// Operators set service keys as `HELMDECK_<SVC>_API_KEY` in
// `deploy/compose/.env.local` per the README; vault-backed packs (e.g.
// podcast.generate, slides.narrate) then read those keys from the
// vault under canonical names like `elevenlabs-key`. Pre-this-change
// the bridge between those two layers didn't exist — packs read
// vault-only, the env vars went unused, and operators got silent
// fallbacks (PR #138 covers the per-pack contract change; this file
// closes the bug class at the platform layer).
//
// HydrateFromEnv is called once at control-plane startup, after the
// vault opens. For each well-known mapping in WellKnownEnvCredentials,
// it:
//   1. Resolves the env var (or HELMDECK_*_FILE docker-secret path)
//   2. Skips if the value is empty
//   3. Skips if an existing credential exists with metadata.source !=
//      "env-hydrate" — never clobber a user-managed entry
//   4. Otherwise upserts the credential with metadata.source =
//      "env-hydrate" and grants a wildcard ACL on first create
//
// Adding a new entry is the canonical place to register a future
// service-key (image-gen, web-search, etc.). One source of truth for
// canonical names — reviewers spotting a per-pack `os.Getenv` shortcut
// in code review get a natural "use the registry" prompt.

package vault

import (
	"context"
	"errors"
	"log/slog"
)

// envHydrateSource is the metadata.source marker on credentials that
// HydrateFromEnv created. Used to decide whether subsequent restarts
// can overwrite the row (yes if env-hydrate, no if user-managed).
const envHydrateSource = "env-hydrate"

// EnvCredential maps one env var (and its docker-secret companion) to
// a canonical vault credential name + type + host pattern.
type EnvCredential struct {
	EnvVar      string         // e.g. "HELMDECK_ELEVENLABS_API_KEY"
	EnvVarFile  string         // e.g. "HELMDECK_ELEVENLABS_API_KEY_FILE" (docker-secret path)
	Name        string         // e.g. "elevenlabs-key"
	Type        CredentialType // e.g. TypeAPIKey
	HostPattern string         // e.g. "api.elevenlabs.io"
}

// WellKnownEnvCredentials is the registry. Add an entry here when a
// new pack starts depending on a canonical env var → vault name pair.
var WellKnownEnvCredentials = []EnvCredential{
	{
		EnvVar:      "HELMDECK_ELEVENLABS_API_KEY",
		EnvVarFile:  "HELMDECK_ELEVENLABS_API_KEY_FILE",
		Name:        "elevenlabs-key",
		Type:        TypeAPIKey,
		HostPattern: "api.elevenlabs.io",
	},
	{
		// fal.ai key — used by image.generate (#71) and chained by
		// podcast.generate cover_image, slides.{render,narrate}
		// hero_image, blog.publish feature_image (#146 cluster).
		// image_generate.go:74 has advertised this auto-hydration
		// since v0.11.0; this entry fulfills that promise.
		EnvVar:      "HELMDECK_FAL_KEY",
		EnvVarFile:  "HELMDECK_FAL_KEY_FILE",
		Name:        "fal-key",
		Type:        TypeAPIKey,
		HostPattern: "fal.run",
	},
}

// EnvLookup is the function shape HydrateFromEnv calls to resolve an
// env-var-or-file pair to its value. Matches `cmd/control-plane/main.go`'s
// envOrFile helper exactly so wiring is a one-liner. Tests pass an
// in-memory lookup so they don't need to fiddle with os.Setenv.
type EnvLookup func(envKey, fileKey string) string

// HydrateFromEnv loops the registry and upserts each present env-var
// into the vault under its canonical name. Logs one INFO line per
// hydration so `docker logs helmdeck-control-plane | grep "vault env
// hydrate"` reveals exactly which credentials loaded.
//
// Errors on individual entries are logged but never abort startup —
// a single bad credential should not block the platform from coming
// up. Returns the count of (created, updated, skipped) for tests +
// metrics.
func (s *Store) HydrateFromEnv(ctx context.Context, logger *slog.Logger, lookup EnvLookup) (created, updated, skipped int) {
	if logger == nil {
		logger = slog.Default()
	}
	if lookup == nil {
		// No way to read env without a lookup function — caller bug.
		// Log once and bail rather than silently doing nothing.
		logger.Warn("vault env hydrate skipped: nil EnvLookup")
		return 0, 0, 0
	}
	for _, c := range WellKnownEnvCredentials {
		key := lookup(c.EnvVar, c.EnvVarFile)
		if key == "" {
			continue
		}

		// Refuse to clobber user-managed entries. Anything not stamped
		// with source=env-hydrate is treated as user-owned even if the
		// metadata is empty — the safer assumption is "operator wrote
		// this deliberately."
		if existing, err := s.GetByName(ctx, c.Name); err == nil {
			source, _ := existing.Metadata["source"].(string)
			if source != envHydrateSource {
				logger.Info("vault env hydrate skip (user-managed)",
					"name", c.Name, "host", c.HostPattern)
				skipped++
				continue
			}
		} else if !errors.Is(err, ErrNotFound) {
			logger.Warn("vault env hydrate lookup failed",
				"name", c.Name, "err", err)
			continue
		}

		rec, isNew, err := s.UpsertByName(ctx, CreateInput{
			Name:        c.Name,
			Type:        c.Type,
			HostPattern: c.HostPattern,
			Plaintext:   []byte(key),
			Metadata: map[string]any{
				"source":  envHydrateSource,
				"env_var": c.EnvVar,
			},
		})
		if err != nil {
			logger.Warn("vault env hydrate failed",
				"name", c.Name, "err", err)
			continue
		}

		// On first create, grant a wildcard ACL so packs can resolve
		// the credential immediately. Without this every operator
		// would need to POST /grants for the new entry — exactly the
		// foot-gun #142 + #138 are closing.
		if isNew {
			if gerr := s.Grant(ctx, rec.ID, Grant{ActorSubject: "*"}); gerr != nil {
				logger.Warn("vault env hydrate grant failed",
					"name", c.Name, "err", gerr)
			}
			created++
			logger.Info("vault env hydrate ok",
				"name", c.Name, "host", c.HostPattern, "action", "create")
		} else {
			updated++
			logger.Info("vault env hydrate ok",
				"name", c.Name, "host", c.HostPattern, "action", "update")
		}
	}
	return created, updated, skipped
}
