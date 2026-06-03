package api

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/store"
	"github.com/tosin2013/helmdeck/internal/vault"
)

func newVaultRouter(t *testing.T) (http.Handler, *vault.Store) {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	key := make([]byte, 32)
	v, err := vault.New(db, key)
	if err != nil {
		t.Fatal(err)
	}
	h := NewRouter(Deps{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
		Vault:   v,
	})
	return h, v
}

func doVault(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestVault_CreateGetListRoundTrip(t *testing.T) {
	h, _ := newVaultRouter(t)
	pt := base64.StdEncoding.EncodeToString([]byte("ghp_secret"))
	body := `{"name":"github","type":"api_key","host_pattern":"api.github.com","plaintext_b64":"` + pt + `"}`
	rr := doVault(t, h, http.MethodPost, "/api/v1/vault/credentials", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", rr.Code, rr.Body.String())
	}
	var rec vault.Record
	_ = json.Unmarshal(rr.Body.Bytes(), &rec)
	if rec.ID == "" {
		t.Fatal("id missing")
	}

	rr = doVault(t, h, http.MethodGet, "/api/v1/vault/credentials/"+rec.ID, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("get status=%d", rr.Code)
	}
	rr = doVault(t, h, http.MethodGet, "/api/v1/vault/credentials", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("list status=%d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"count":1`) {
		t.Errorf("list count wrong: %s", rr.Body.String())
	}
}

func TestVault_CreateValidationErrors(t *testing.T) {
	h, _ := newVaultRouter(t)
	cases := []struct {
		name string
		body string
		code int
	}{
		{"missing_name", `{"type":"api_key","host_pattern":"h","plaintext_b64":"YQ=="}`, http.StatusBadRequest},
		{"unknown_type", `{"name":"x","type":"weird","host_pattern":"h","plaintext_b64":"YQ=="}`, http.StatusBadRequest},
		{"missing_host", `{"name":"x","type":"api_key","plaintext_b64":"YQ=="}`, http.StatusBadRequest},
		{"missing_plaintext", `{"name":"x","type":"api_key","host_pattern":"h","plaintext_b64":""}`, http.StatusBadRequest},
		{"bad_b64", `{"name":"x","type":"api_key","host_pattern":"h","plaintext_b64":"!!!"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := doVault(t, h, http.MethodPost, "/api/v1/vault/credentials", tc.body)
			if rr.Code != tc.code {
				t.Errorf("got %d body=%s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestVault_DuplicateNameConflict(t *testing.T) {
	h, _ := newVaultRouter(t)
	body := `{"name":"x","type":"api_key","host_pattern":"h","plaintext_b64":"YQ=="}`
	doVault(t, h, http.MethodPost, "/api/v1/vault/credentials", body)
	rr := doVault(t, h, http.MethodPost, "/api/v1/vault/credentials", body)
	if rr.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", rr.Code)
	}
}

func TestVault_RotateChangesFingerprint(t *testing.T) {
	h, _ := newVaultRouter(t)
	body := `{"name":"x","type":"api_key","host_pattern":"h","plaintext_b64":"YQ=="}`
	rr := doVault(t, h, http.MethodPost, "/api/v1/vault/credentials", body)
	var rec vault.Record
	_ = json.Unmarshal(rr.Body.Bytes(), &rec)
	oldFP := rec.Fingerprint

	newPT := base64.StdEncoding.EncodeToString([]byte("rotated"))
	rr = doVault(t, h, http.MethodPut, "/api/v1/vault/credentials/"+rec.ID,
		`{"plaintext_b64":"`+newPT+`"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("rotate status=%d body=%s", rr.Code, rr.Body.String())
	}
	var rotated vault.Record
	_ = json.Unmarshal(rr.Body.Bytes(), &rotated)
	if rotated.Fingerprint == oldFP {
		t.Errorf("fingerprint should change on rotate")
	}
}

func TestVault_GrantsAndRevoke(t *testing.T) {
	h, _ := newVaultRouter(t)
	body := `{"name":"x","type":"api_key","host_pattern":"h","plaintext_b64":"YQ=="}`
	rr := doVault(t, h, http.MethodPost, "/api/v1/vault/credentials", body)
	var rec vault.Record
	_ = json.Unmarshal(rr.Body.Bytes(), &rec)

	// Grant.
	rr = doVault(t, h, http.MethodPost, "/api/v1/vault/credentials/"+rec.ID+"/grants",
		`{"actor_subject":"alice","actor_client":"claude-code"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("grant status=%d", rr.Code)
	}

	// List.
	rr = doVault(t, h, http.MethodGet, "/api/v1/vault/credentials/"+rec.ID+"/grants", "")
	if !strings.Contains(rr.Body.String(), "alice") {
		t.Errorf("grants list missing alice: %s", rr.Body.String())
	}

	// Revoke.
	rr = doVault(t, h, http.MethodDelete,
		"/api/v1/vault/credentials/"+rec.ID+"/grants/alice?client=claude-code", "")
	if rr.Code != http.StatusNoContent {
		t.Fatalf("revoke status=%d", rr.Code)
	}
	rr = doVault(t, h, http.MethodGet, "/api/v1/vault/credentials/"+rec.ID+"/grants", "")
	if strings.Contains(rr.Body.String(), "alice") {
		t.Errorf("revoked grant still present: %s", rr.Body.String())
	}
}

func TestVault_GrantMissingSubject(t *testing.T) {
	h, _ := newVaultRouter(t)
	body := `{"name":"x","type":"api_key","host_pattern":"h","plaintext_b64":"YQ=="}`
	rr := doVault(t, h, http.MethodPost, "/api/v1/vault/credentials", body)
	var rec vault.Record
	_ = json.Unmarshal(rr.Body.Bytes(), &rec)
	rr = doVault(t, h, http.MethodPost, "/api/v1/vault/credentials/"+rec.ID+"/grants",
		`{"actor_client":"x"}`)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

func TestVault_DeleteRemovesCredential(t *testing.T) {
	h, _ := newVaultRouter(t)
	body := `{"name":"x","type":"api_key","host_pattern":"h","plaintext_b64":"YQ=="}`
	rr := doVault(t, h, http.MethodPost, "/api/v1/vault/credentials", body)
	var rec vault.Record
	_ = json.Unmarshal(rr.Body.Bytes(), &rec)
	rr = doVault(t, h, http.MethodDelete, "/api/v1/vault/credentials/"+rec.ID, "")
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d", rr.Code)
	}
	rr = doVault(t, h, http.MethodGet, "/api/v1/vault/credentials/"+rec.ID, "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404 after delete, got %d", rr.Code)
	}
}

// TestVault_GetUnknownIDIs404 — Get on a non-existent id returns 404
// not_found via writeVaultError's ErrNotFound branch.
func TestVault_GetUnknownIDIs404(t *testing.T) {
	h, _ := newVaultRouter(t)
	rr := doVault(t, h, http.MethodGet, "/api/v1/vault/credentials/no-such-id", "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// TestVault_DeleteUnknownIDIs404 — Delete on a missing id surfaces
// 404 (not 204) so the operator notices typos.
func TestVault_DeleteUnknownIDIs404(t *testing.T) {
	h, _ := newVaultRouter(t)
	rr := doVault(t, h, http.MethodDelete, "/api/v1/vault/credentials/no-such-id", "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// TestVault_RotateUnknownIDIs404 — same for rotate.
func TestVault_RotateUnknownIDIs404(t *testing.T) {
	h, _ := newVaultRouter(t)
	pt := base64.StdEncoding.EncodeToString([]byte("rotated"))
	rr := doVault(t, h, http.MethodPut, "/api/v1/vault/credentials/no-such-id",
		`{"plaintext_b64":"`+pt+`"}`)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// TestVault_RotateBadJSON returns 400.
func TestVault_RotateBadJSON(t *testing.T) {
	h, _ := newVaultRouter(t)
	body := `{"name":"x","type":"api_key","host_pattern":"h","plaintext_b64":"YQ=="}`
	rr := doVault(t, h, http.MethodPost, "/api/v1/vault/credentials", body)
	var rec vault.Record
	_ = json.Unmarshal(rr.Body.Bytes(), &rec)

	rr2 := doVault(t, h, http.MethodPut, "/api/v1/vault/credentials/"+rec.ID, `{nope`)
	if rr2.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr2.Code)
	}
}

// TestVault_RotateBadB64 — non-base64 plaintext is rejected.
func TestVault_RotateBadB64(t *testing.T) {
	h, _ := newVaultRouter(t)
	body := `{"name":"x","type":"api_key","host_pattern":"h","plaintext_b64":"YQ=="}`
	rr := doVault(t, h, http.MethodPost, "/api/v1/vault/credentials", body)
	var rec vault.Record
	_ = json.Unmarshal(rr.Body.Bytes(), &rec)

	rr2 := doVault(t, h, http.MethodPut, "/api/v1/vault/credentials/"+rec.ID,
		`{"plaintext_b64":"!!!not-b64!!!"}`)
	if rr2.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr2.Code)
	}
}

// TestVault_GrantBadJSON returns 400.
func TestVault_GrantBadJSON(t *testing.T) {
	h, _ := newVaultRouter(t)
	body := `{"name":"x","type":"api_key","host_pattern":"h","plaintext_b64":"YQ=="}`
	rr := doVault(t, h, http.MethodPost, "/api/v1/vault/credentials", body)
	var rec vault.Record
	_ = json.Unmarshal(rr.Body.Bytes(), &rec)

	rr2 := doVault(t, h, http.MethodPost, "/api/v1/vault/credentials/"+rec.ID+"/grants", `{nope`)
	if rr2.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr2.Code)
	}
}

// TestVault_UsageEndpoint — GET /usage returns an empty list for a
// fresh credential and wraps it under {"usage":[...],"count":0}.
func TestVault_UsageEndpoint(t *testing.T) {
	h, _ := newVaultRouter(t)
	body := `{"name":"x","type":"api_key","host_pattern":"h","plaintext_b64":"YQ=="}`
	rr := doVault(t, h, http.MethodPost, "/api/v1/vault/credentials", body)
	var rec vault.Record
	_ = json.Unmarshal(rr.Body.Bytes(), &rec)

	rr2 := doVault(t, h, http.MethodGet, "/api/v1/vault/credentials/"+rec.ID+"/usage", "")
	if rr2.Code != http.StatusOK {
		t.Fatalf("usage status = %d (%s)", rr2.Code, rr2.Body.String())
	}
	if !strings.Contains(rr2.Body.String(), `"count":0`) {
		t.Errorf("usage body missing count:0: %s", rr2.Body.String())
	}
}

// TestVault_ListWithTypeFilter — ?type=login returns only login
// credentials (api_key-typed entries are excluded).
func TestVault_ListWithTypeFilter(t *testing.T) {
	h, _ := newVaultRouter(t)
	body := `{"name":"k","type":"api_key","host_pattern":"h","plaintext_b64":"YQ=="}`
	_ = doVault(t, h, http.MethodPost, "/api/v1/vault/credentials", body)
	// Filter out the api_key entry by asking for login.
	rr := doVault(t, h, http.MethodGet, "/api/v1/vault/credentials?type=login", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"count":0`) {
		t.Errorf("login-typed list should be empty, got %s", rr.Body.String())
	}
}

func TestVault_NoVaultConfigured503(t *testing.T) {
	h := NewRouter(Deps{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
	})
	rr := doVault(t, h, http.MethodGet, "/api/v1/vault/credentials", "")
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rr.Code)
	}
}
