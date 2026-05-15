// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/store"
	"github.com/tosin2013/helmdeck/internal/vault"
)

// stubPexels runs an httptest server that emulates the Pexels search
// endpoint + the CDN URLs it returns. Tests redirect PexelsBaseURL to
// the stub via the package var (mirrors stubFalAPI in image_generate_test.go).
//
// Behavior:
//   - GET /search    → JSON envelope with `photoCount` synthetic photos
//   - GET /cdn/*     → the raw PNG bytes the photos point at
// Auth check: requires "Authorization: <wantKey>" header on the
// /search call. Returns the configured status code path (401/429/500)
// on auth or status overrides — used for the failure-mode tests.
type pexelsStub struct {
	srv         *httptest.Server
	pngBytes    []byte
	wantKey     string
	photoCount  int
	searchStatus int // 0 = default 200; set non-zero to override
	emptyResult bool // when true, return zero photos in a 200 response
}

func newPexelsStub(t *testing.T, wantKey string, photoCount int) *pexelsStub {
	t.Helper()
	// 1×1 PNG (smallest possible) — same bytes as the fal stub.
	pngBytes, _ := base64.StdEncoding.DecodeString(
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP8//8/AwAI/AL+CWnKEQAAAABJRU5ErkJggg==")
	stub := &pexelsStub{pngBytes: pngBytes, wantKey: wantKey, photoCount: photoCount}
	stub.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// CDN photo download — no auth required, just serve bytes.
		if strings.Contains(r.URL.Path, "/cdn/photo-") {
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(stub.pngBytes)
			return
		}
		// Everything else is the search endpoint.
		if r.URL.Path != "/search" {
			http.Error(w, "unexpected path: "+r.URL.Path, 404)
			return
		}
		if r.Header.Get("Authorization") != stub.wantKey {
			http.Error(w, "bad key", 401)
			return
		}
		if stub.searchStatus != 0 {
			http.Error(w, "configured failure", stub.searchStatus)
			return
		}
		photos := []map[string]any{}
		if !stub.emptyResult {
			for i := 0; i < stub.photoCount; i++ {
				photos = append(photos, map[string]any{
					"id":               1000 + i,
					"width":            1920, "height": 1080,
					"url":              "https://www.pexels.com/photo/test-" + r.URL.Query().Get("query"),
					"photographer":     "Test Photographer",
					"photographer_url": "https://www.pexels.com/@test",
					"alt":              "A test photo of " + r.URL.Query().Get("query"),
					"src": map[string]any{
						"large2x":  "http://" + r.Host + "/cdn/photo-large2x.jpg",
						"large":    "http://" + r.Host + "/cdn/photo-large.jpg",
						"original": "http://" + r.Host + "/cdn/photo-original.jpg",
						"medium":   "http://" + r.Host + "/cdn/photo-medium.jpg",
					},
				})
			}
		}
		out := map[string]any{
			"photos":        photos,
			"total_results": len(photos),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(stub.srv.Close)
	prev := PexelsBaseURL
	PexelsBaseURL = stub.srv.URL
	t.Cleanup(func() { PexelsBaseURL = prev })
	return stub
}

// vaultWithPexelsKey seeds an in-memory vault with the pexels-key
// credential + wildcard ACL. Mirrors vaultWithFalKey.
func vaultWithPexelsKey(t *testing.T, key string) *vault.Store {
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
	if key == "" {
		return v
	}
	rec, err := v.Create(context.Background(), vault.CreateInput{
		Name:        stockSearchPexelsCredName,
		Type:        vault.TypeAPIKey,
		HostPattern: "api.pexels.com",
		Plaintext:   []byte(key),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Grant(context.Background(), rec.ID, vault.Grant{ActorSubject: "*"}); err != nil {
		t.Fatal(err)
	}
	return v
}

func runStockSearch(t *testing.T, v *vault.Store, input string) (json.RawMessage, error) {
	t.Helper()
	pack := StockSearch(v, nil)
	ec := &packs.ExecutionContext{
		Pack:      pack,
		Input:     json.RawMessage(input),
		Artifacts: packs.NewMemoryArtifactStore(),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return pack.Handler(context.Background(), ec)
}

// --- happy paths --------------------------------------------------------

func TestStockSearch_HappyPath_SinglePhoto(t *testing.T) {
	newPexelsStub(t, "sk_test", 1)
	v := vaultWithPexelsKey(t, "sk_test")

	raw, err := runStockSearch(t, v, `{"query":"mountain sunrise"}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		Engine       string              `json:"engine"`
		ArtifactKeys []string            `json:"artifact_keys"`
		Results      []StockSearchResult `json:"results"`
		QueryUsed    string              `json:"query_used"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Engine != "pexels" {
		t.Errorf("engine = %q, want pexels", out.Engine)
	}
	if len(out.ArtifactKeys) != 1 {
		t.Fatalf("artifact_keys = %d, want 1", len(out.ArtifactKeys))
	}
	if out.QueryUsed != "mountain sunrise" {
		t.Errorf("query_used = %q", out.QueryUsed)
	}
	if len(out.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(out.Results))
	}
	r := out.Results[0]
	if r.Photographer == "" || r.PhotographerURL == "" || r.SourceURL == "" {
		t.Errorf("attribution metadata missing: %+v", r)
	}
	if r.ArtifactKey != out.ArtifactKeys[0] {
		t.Errorf("results[0].artifact_key (%s) != artifact_keys[0] (%s)",
			r.ArtifactKey, out.ArtifactKeys[0])
	}
	if r.Width != 1920 || r.Height != 1080 {
		t.Errorf("dimensions = %dx%d, want 1920x1080", r.Width, r.Height)
	}
}

func TestStockSearch_MultiplePhotos_MatchCountInput(t *testing.T) {
	newPexelsStub(t, "sk_test", 4)
	v := vaultWithPexelsKey(t, "sk_test")

	raw, err := runStockSearch(t, v, `{"query":"forest", "count":4}`)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	var out struct {
		ArtifactKeys []string `json:"artifact_keys"`
	}
	_ = json.Unmarshal(raw, &out)
	if len(out.ArtifactKeys) != 4 {
		t.Errorf("got %d artifacts, want 4", len(out.ArtifactKeys))
	}
}

// --- input validation --------------------------------------------------

func TestStockSearch_MissingQuery_RejectsInvalidInput(t *testing.T) {
	_, err := runStockSearch(t, vaultWithPexelsKey(t, "sk"), `{}`)
	var pe *packs.PackError
	if !errors.As(err, &pe) || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got %v", err)
	}
}

func TestStockSearch_UnknownEngine_Rejects(t *testing.T) {
	_, err := runStockSearch(t, vaultWithPexelsKey(t, "sk"),
		`{"query":"cat", "engine":"unsplash"}`)
	var pe *packs.PackError
	if !errors.As(err, &pe) || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput for unknown engine, got %v", err)
	}
	if !strings.Contains(pe.Message, "unsplash") {
		t.Errorf("error should name the bad engine: %s", pe.Message)
	}
}

func TestStockSearch_CountOutOfRange_Rejects(t *testing.T) {
	// Validation should reject before we reach the HTTP layer, so no
	// stub is needed. Vault must exist for the credential resolver to
	// short-circuit successfully.
	t.Setenv(stockSearchPexelsEnvVar, "sk_unused")
	v := vaultWithPexelsKey(t, "")
	for _, c := range []string{"5", "100"} {
		_, err := runStockSearch(t, v, `{"query":"cat", "count":`+c+`}`)
		var pe *packs.PackError
		if !errors.As(err, &pe) || pe.Code != packs.CodeInvalidInput {
			t.Errorf("count=%s expected CodeInvalidInput, got %v", c, err)
		}
	}
}

func TestStockSearch_BadOrientation_Rejects(t *testing.T) {
	_, err := runStockSearch(t, vaultWithPexelsKey(t, "sk"),
		`{"query":"cat", "orientation":"slanted"}`)
	var pe *packs.PackError
	if !errors.As(err, &pe) || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput for bad orientation, got %v", err)
	}
}

func TestStockSearch_BadSize_Rejects(t *testing.T) {
	_, err := runStockSearch(t, vaultWithPexelsKey(t, "sk"),
		`{"query":"cat", "size":"huge"}`)
	var pe *packs.PackError
	if !errors.As(err, &pe) || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput for bad size, got %v", err)
	}
}

func TestStockSearch_VideoMediaType_RejectsForNow(t *testing.T) {
	// Day-1 ships photos only — `media_type: "video"` is reserved for a
	// follow-up PR. Reject loud so callers don't get a confusing
	// upstream error.
	_, err := runStockSearch(t, vaultWithPexelsKey(t, "sk"),
		`{"query":"cat", "media_type":"video"}`)
	var pe *packs.PackError
	if !errors.As(err, &pe) || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput for media_type=video, got %v", err)
	}
}

// --- credential resolution --------------------------------------------

func TestStockSearch_NoCredential_RejectsWithActionableMessage(t *testing.T) {
	// No vault key, no env var, no explicit credential → CodeInvalidInput
	// with a message pointing at how to fix it.
	t.Setenv("HELMDECK_PEXELS_API_KEY", "")
	_, err := runStockSearch(t, vaultWithPexelsKey(t, ""), `{"query":"cat"}`)
	var pe *packs.PackError
	if !errors.As(err, &pe) || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput, got %v", err)
	}
	if !strings.Contains(pe.Message, "HELMDECK_PEXELS_API_KEY") {
		t.Errorf("error should mention env var: %s", pe.Message)
	}
	if !strings.Contains(pe.Message, "pexels-key") {
		t.Errorf("error should mention vault credential name: %s", pe.Message)
	}
}

func TestStockSearch_EnvVarFallback_Honored(t *testing.T) {
	// Vault empty, env var set, stub configured to accept env-var value.
	newPexelsStub(t, "sk_from_env", 1)
	t.Setenv(stockSearchPexelsEnvVar, "sk_from_env")
	v := vaultWithPexelsKey(t, "") // empty vault
	_, err := runStockSearch(t, v, `{"query":"cat"}`)
	if err != nil {
		t.Errorf("env-var fallback should authenticate, got: %v", err)
	}
}

func TestStockSearch_ExplicitCredentialName_HonoredOverDefault(t *testing.T) {
	// Vault has two credentials. Caller's `credential: "my-alt-key"`
	// should win over the default `pexels-key`.
	stub := newPexelsStub(t, "alt_key", 1)
	_ = stub
	v := vaultWithPexelsKey(t, "default_key") // seeds pexels-key
	// Add a second credential under a different name.
	rec, err := v.Create(context.Background(), vault.CreateInput{
		Name: "my-alt-key", Type: vault.TypeAPIKey,
		HostPattern: "api.pexels.com", Plaintext: []byte("alt_key"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Grant(context.Background(), rec.ID, vault.Grant{ActorSubject: "*"}); err != nil {
		t.Fatal(err)
	}
	_, err = runStockSearch(t, v, `{"query":"cat", "credential":"my-alt-key"}`)
	if err != nil {
		t.Errorf("explicit credential should authenticate with alt_key, got: %v", err)
	}
}

// --- upstream failure modes --------------------------------------------

func TestStockSearch_PexelsRateLimit_429_MapsToHandlerFailed(t *testing.T) {
	stub := newPexelsStub(t, "sk_test", 1)
	stub.searchStatus = http.StatusTooManyRequests
	v := vaultWithPexelsKey(t, "sk_test")
	_, err := runStockSearch(t, v, `{"query":"cat"}`)
	var pe *packs.PackError
	if !errors.As(err, &pe) || pe.Code != packs.CodeHandlerFailed {
		t.Fatalf("expected CodeHandlerFailed for 429, got %v", err)
	}
	if !strings.Contains(pe.Message, "rate limit") {
		t.Errorf("error message should mention rate limit: %s", pe.Message)
	}
}

func TestStockSearch_PexelsAuthFailure_401_MapsToInvalidInput(t *testing.T) {
	stub := newPexelsStub(t, "sk_correct", 1)
	stub.searchStatus = http.StatusUnauthorized
	v := vaultWithPexelsKey(t, "sk_correct")
	_, err := runStockSearch(t, v, `{"query":"cat"}`)
	var pe *packs.PackError
	if !errors.As(err, &pe) || pe.Code != packs.CodeInvalidInput {
		t.Fatalf("expected CodeInvalidInput for 401 (credential is the actionable bit), got %v", err)
	}
}

func TestStockSearch_Pexels5xx_MapsToHandlerFailed(t *testing.T) {
	stub := newPexelsStub(t, "sk_test", 1)
	stub.searchStatus = http.StatusInternalServerError
	v := vaultWithPexelsKey(t, "sk_test")
	_, err := runStockSearch(t, v, `{"query":"cat"}`)
	var pe *packs.PackError
	if !errors.As(err, &pe) || pe.Code != packs.CodeHandlerFailed {
		t.Fatalf("expected CodeHandlerFailed for 500, got %v", err)
	}
}

func TestStockSearch_EmptyResults_MapsToHandlerFailedWithHint(t *testing.T) {
	stub := newPexelsStub(t, "sk_test", 0)
	stub.emptyResult = true
	v := vaultWithPexelsKey(t, "sk_test")
	_, err := runStockSearch(t, v, `{"query":"asdfqwzx-no-such-photo"}`)
	var pe *packs.PackError
	if !errors.As(err, &pe) || pe.Code != packs.CodeHandlerFailed {
		t.Fatalf("expected CodeHandlerFailed for empty result, got %v", err)
	}
	if !strings.Contains(pe.Message, "no results") {
		t.Errorf("error should explain the empty result: %s", pe.Message)
	}
}

// --- helpers ----------------------------------------------------------

func TestPexelsPhoto_BestDownloadURL_FallsThroughSizeLadder(t *testing.T) {
	p := pexelsPhoto{Src: pexelsPhotoSrc{
		Large2x: "L2X", Large: "L", Original: "O", Medium: "M",
	}}
	if got := p.bestDownloadURL(); got != "L2X" {
		t.Errorf("with Large2x present, want L2X; got %q", got)
	}
	p.Src.Large2x = ""
	if got := p.bestDownloadURL(); got != "L" {
		t.Errorf("missing Large2x → want Large; got %q", got)
	}
	p.Src.Large = ""
	if got := p.bestDownloadURL(); got != "O" {
		t.Errorf("missing Large2x+Large → want Original; got %q", got)
	}
}

func TestStockSearch_PackRegistrationShape(t *testing.T) {
	p := StockSearch(nil, nil)
	if p.Name != "stock.search" {
		t.Errorf("name = %q", p.Name)
	}
	if p.Version != "v1" {
		t.Errorf("version = %q", p.Version)
	}
	// `query` is the only required input
	bs, ok := p.InputSchema.(packs.BasicSchema)
	if !ok || len(bs.Required) != 1 || bs.Required[0] != "query" {
		t.Errorf("input schema required fields = %v, want [query]", bs.Required)
	}
}

func TestIsValidOrientation_Closed(t *testing.T) {
	for _, ok := range []string{"landscape", "portrait", "square"} {
		if !isValidOrientation(ok) {
			t.Errorf("%q should validate", ok)
		}
	}
	for _, bad := range []string{"", "diagonal", "slanted", "LANDSCAPE"} {
		if isValidOrientation(bad) {
			t.Errorf("%q should NOT validate", bad)
		}
	}
}

func TestIsValidSize_Closed(t *testing.T) {
	for _, ok := range []string{"large", "medium", "small"} {
		if !isValidSize(ok) {
			t.Errorf("%q should validate", ok)
		}
	}
	for _, bad := range []string{"", "huge", "tiny", "LARGE"} {
		if isValidSize(bad) {
			t.Errorf("%q should NOT validate", bad)
		}
	}
}
