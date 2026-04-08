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
