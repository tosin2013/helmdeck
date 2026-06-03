package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/keystore"
	"github.com/tosin2013/helmdeck/internal/store"
)

func newKeysRouter(t *testing.T, tester KeyTester) (http.Handler, *keystore.Store) {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	ks, err := keystore.New(db, key)
	if err != nil {
		t.Fatalf("keystore: %v", err)
	}
	h := NewRouter(Deps{
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:   "test",
		Keys:      ks,
		KeyTester: tester,
	})
	return h, ks
}

func doJSON(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestKeysCRUDFlow(t *testing.T) {
	h, _ := newKeysRouter(t, nil)

	// create
	rr := doJSON(t, h, http.MethodPost, "/api/v1/providers/keys", `{"provider":"openai","label":"prod","key":"sk-abcd-1234"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", rr.Code, rr.Body.String())
	}
	var rec keystore.Record
	if err := json.Unmarshal(rr.Body.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Last4 != "1234" || rec.ID == "" {
		t.Errorf("rec = %+v", rec)
	}
	// plaintext must NOT appear in the create response
	if strings.Contains(rr.Body.String(), "sk-abcd-1234") {
		t.Error("plaintext leaked in create response")
	}

	// list
	rr = doJSON(t, h, http.MethodGet, "/api/v1/providers/keys", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "sk-abcd") {
		t.Error("plaintext leaked in list response")
	}

	// get
	rr = doJSON(t, h, http.MethodGet, "/api/v1/providers/keys/"+rec.ID, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("get status = %d", rr.Code)
	}

	// duplicate -> 409
	rr = doJSON(t, h, http.MethodPost, "/api/v1/providers/keys", `{"provider":"openai","label":"prod","key":"sk-other-9999"}`)
	if rr.Code != http.StatusConflict {
		t.Errorf("duplicate status = %d", rr.Code)
	}

	// rotate
	rr = doJSON(t, h, http.MethodPost, "/api/v1/providers/keys/"+rec.ID+"/rotate", `{"key":"sk-rotated-9999"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("rotate status = %d body=%s", rr.Code, rr.Body.String())
	}
	var rotated keystore.Record
	_ = json.Unmarshal(rr.Body.Bytes(), &rotated)
	if rotated.Last4 != "9999" {
		t.Errorf("rotated last4 = %q", rotated.Last4)
	}

	// delete
	rr = doJSON(t, h, http.MethodDelete, "/api/v1/providers/keys/"+rec.ID, "")
	if rr.Code != http.StatusNoContent {
		t.Errorf("delete status = %d", rr.Code)
	}
	// get after delete -> 404
	rr = doJSON(t, h, http.MethodGet, "/api/v1/providers/keys/"+rec.ID, "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("get-after-delete status = %d", rr.Code)
	}
}

func TestKeysTestEndpoint(t *testing.T) {
	var gotProvider, gotKey string
	tester := func(ctx context.Context, _ *http.Client, provider, apiKey string) error {
		gotProvider = provider
		gotKey = apiKey
		return nil
	}
	h, _ := newKeysRouter(t, tester)

	rr := doJSON(t, h, http.MethodPost, "/api/v1/providers/keys", `{"provider":"anthropic","label":"prod","key":"sk-test-1234"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d", rr.Code)
	}
	var rec keystore.Record
	_ = json.Unmarshal(rr.Body.Bytes(), &rec)

	rr = doJSON(t, h, http.MethodPost, "/api/v1/providers/keys/"+rec.ID+"/test", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("test status = %d body=%s", rr.Code, rr.Body.String())
	}
	if gotProvider != "anthropic" || gotKey != "sk-test-1234" {
		t.Errorf("tester saw provider=%q key=%q", gotProvider, gotKey)
	}
	if !strings.Contains(rr.Body.String(), `"ok":true`) {
		t.Errorf("body = %s", rr.Body.String())
	}
}

// TestKeys_BadJSONOnCreate — malformed body should return 400
// invalid_json (not 500).
func TestKeys_BadJSONOnCreate(t *testing.T) {
	h, _ := newKeysRouter(t, nil)
	rr := doJSON(t, h, http.MethodPost, "/api/v1/providers/keys", `{not-json`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "invalid_json") {
		t.Errorf("body should mention invalid_json: %s", rr.Body.String())
	}
}

// TestKeys_RotateBadJSON — malformed rotate body returns 400.
func TestKeys_RotateBadJSON(t *testing.T) {
	h, _ := newKeysRouter(t, nil)
	rr := doJSON(t, h, http.MethodPost, "/api/v1/providers/keys", `{"provider":"openai","label":"L","key":"sk-1234"}`)
	var rec keystore.Record
	_ = json.Unmarshal(rr.Body.Bytes(), &rec)

	rr2 := doJSON(t, h, http.MethodPost, "/api/v1/providers/keys/"+rec.ID+"/rotate", `{`)
	if rr2.Code != http.StatusBadRequest {
		t.Errorf("rotate bad-json status = %d, want 400", rr2.Code)
	}
}

// TestKeys_RotateUnknownKey — 404 not_found when rotating an id that
// the store doesn't recognize.
func TestKeys_RotateUnknownKey(t *testing.T) {
	h, _ := newKeysRouter(t, nil)
	rr := doJSON(t, h, http.MethodPost, "/api/v1/providers/keys/no-such-id/rotate",
		`{"key":"sk-new-1234"}`)
	if rr.Code != http.StatusNotFound {
		t.Errorf("rotate unknown status = %d, want 404", rr.Code)
	}
}

// TestKeys_DeleteUnknownKey — 404 not_found rather than 204.
func TestKeys_DeleteUnknownKey(t *testing.T) {
	h, _ := newKeysRouter(t, nil)
	rr := doJSON(t, h, http.MethodDelete, "/api/v1/providers/keys/no-such-id", "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("delete unknown status = %d, want 404", rr.Code)
	}
}

// TestKeys_MissingIDIs404 — GET /api/v1/providers/keys/ (no id) returns
// 404 with "missing id" rather than echoing the list endpoint.
func TestKeys_MissingIDIs404(t *testing.T) {
	h, _ := newKeysRouter(t, nil)
	rr := doJSON(t, h, http.MethodGet, "/api/v1/providers/keys/", "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("missing id status = %d, want 404", rr.Code)
	}
}

// TestKeys_MethodNotAllowed — PUT or PATCH on /{id} returns 405.
func TestKeys_MethodNotAllowed(t *testing.T) {
	h, _ := newKeysRouter(t, nil)
	rr := doJSON(t, h, http.MethodPost, "/api/v1/providers/keys",
		`{"provider":"openai","label":"L","key":"sk-abc-1234"}`)
	var rec keystore.Record
	_ = json.Unmarshal(rr.Body.Bytes(), &rec)

	rr2 := doJSON(t, h, http.MethodPut, "/api/v1/providers/keys/"+rec.ID, `{}`)
	if rr2.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT status = %d, want 405", rr2.Code)
	}
}

// TestKeys_UnknownSubroute — POST to /{id}/unknown returns 404.
func TestKeys_UnknownSubroute(t *testing.T) {
	h, _ := newKeysRouter(t, nil)
	rr := doJSON(t, h, http.MethodPost, "/api/v1/providers/keys",
		`{"provider":"openai","label":"L","key":"sk-xyz-1234"}`)
	var rec keystore.Record
	_ = json.Unmarshal(rr.Body.Bytes(), &rec)

	rr2 := doJSON(t, h, http.MethodPost, "/api/v1/providers/keys/"+rec.ID+"/bogus", `{}`)
	if rr2.Code != http.StatusNotFound {
		t.Errorf("bogus subroute status = %d, want 404", rr2.Code)
	}
}

// TestKeys_TestEndpoint_UnknownKey — POST /{id}/test on a missing id
// returns 404 (the Get inside test).
func TestKeys_TestEndpoint_UnknownKey(t *testing.T) {
	h, _ := newKeysRouter(t, nil)
	rr := doJSON(t, h, http.MethodPost, "/api/v1/providers/keys/no-id/test", "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// TestKeys_TestEndpoint_TesterError — tester returns an error → 502
// with {"ok":false,"error":...}, not a 500. The operator distinguishes
// "key works" from "key was rejected by the provider".
func TestKeys_TestEndpoint_TesterError(t *testing.T) {
	tester := func(_ context.Context, _ *http.Client, _, _ string) error {
		return errAuthFailed{}
	}
	h, _ := newKeysRouter(t, tester)
	rr := doJSON(t, h, http.MethodPost, "/api/v1/providers/keys",
		`{"provider":"anthropic","label":"L","key":"sk-test-9999"}`)
	var rec keystore.Record
	_ = json.Unmarshal(rr.Body.Bytes(), &rec)

	rr2 := doJSON(t, h, http.MethodPost, "/api/v1/providers/keys/"+rec.ID+"/test", "")
	if rr2.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rr2.Code)
	}
	if !strings.Contains(rr2.Body.String(), `"ok":false`) {
		t.Errorf("body should be ok:false: %s", rr2.Body.String())
	}
}

type errAuthFailed struct{}

func (errAuthFailed) Error() string { return "401 unauthorized" }

func TestKeysUnavailableWhenNil(t *testing.T) {
	h := NewRouter(Deps{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
	})
	rr := doJSON(t, h, http.MethodGet, "/api/v1/providers/keys", "")
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d", rr.Code)
	}
}
