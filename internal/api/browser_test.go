package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/cdp"
	cdpfake "github.com/tosin2013/helmdeck/internal/cdp/fake"
	"github.com/tosin2013/helmdeck/internal/session"
	"github.com/tosin2013/helmdeck/internal/session/fake"
)

type stubCDPFactory struct {
	c       cdp.Client
	evicted []string
}

func (s *stubCDPFactory) Get(_ context.Context, _ string) (cdp.Client, error) {
	return s.c, nil
}
func (s *stubCDPFactory) Evict(id string) { s.evicted = append(s.evicted, id) }

func newBrowserRouter(t *testing.T) (http.Handler, *cdpfake.Client) {
	t.Helper()
	fc := &cdpfake.Client{ExtractText: "Hello World", ExecuteResult: 42}
	h := NewRouter(Deps{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:    "test",
		CDPFactory: &stubCDPFactory{c: fc},
	})
	return h, fc
}

func TestBrowserNavigate(t *testing.T) {
	h, fc := newBrowserRouter(t)
	body := bytes.NewBufferString(`{"session_id":"s1","url":"https://example.com"}`)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/browser/navigate", body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if fc.NavigateURL != "https://example.com" {
		t.Fatalf("Navigate URL = %q", fc.NavigateURL)
	}
}

func TestBrowserExtract(t *testing.T) {
	h, fc := newBrowserRouter(t)
	body := bytes.NewBufferString(`{"session_id":"s1","selector":".price","format":"text"}`)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/browser/extract", body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["content"] != "Hello World" {
		t.Fatalf("content = %v", resp["content"])
	}
	if len(fc.ExtractCalls) != 1 || fc.ExtractCalls[0].Selector != ".price" {
		t.Fatalf("ExtractCalls = %+v", fc.ExtractCalls)
	}
}

func TestBrowserScreenshot(t *testing.T) {
	h, _ := newBrowserRouter(t)
	body := bytes.NewBufferString(`{"session_id":"s1","full_page":true}`)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/browser/screenshot", body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if rr.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("content-type = %q", rr.Header().Get("Content-Type"))
	}
	if !bytes.HasPrefix(rr.Body.Bytes(), []byte("\x89PNG")) {
		t.Fatalf("body does not start with PNG magic: %q", rr.Body.String())
	}
}

func TestBrowserExecute(t *testing.T) {
	h, _ := newBrowserRouter(t)
	body := bytes.NewBufferString(`{"session_id":"s1","script":"document.title"}`)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/browser/execute", body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"result":42`) {
		t.Fatalf("body = %s", rr.Body.String())
	}
}

func TestBrowserInteractClick(t *testing.T) {
	h, fc := newBrowserRouter(t)
	body := bytes.NewBufferString(`{"session_id":"s1","action":"click","selector":"#submit"}`)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/browser/interact", body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if len(fc.InteractCalls) != 1 || fc.InteractCalls[0].Action != cdp.ActionClick {
		t.Fatalf("InteractCalls = %+v", fc.InteractCalls)
	}
}

func TestBrowserMissingURLBadRequest(t *testing.T) {
	h, _ := newBrowserRouter(t)
	rr := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"session_id":"s1"}`)
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/browser/navigate", body))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestBrowserNoFactoryReturns503(t *testing.T) {
	h := NewRouter(Deps{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
	})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/browser/navigate", bytes.NewBufferString(`{}`)))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
}

// T503 — verify the navigate handler invokes the injector and the
// vault credential lands on the cdp client before the navigation.
func TestBrowserNavigateWithVaultCookieInjection(t *testing.T) {
	db := newTestDB(t)
	v := newTestVault(t, db)
	rec, _ := v.Create(context.Background(), vaultCreateInput("gh", "cookie", "github.com",
		[]byte(`[{"name":"session","value":"abc123"}]`)))
	_ = v.Grant(context.Background(), rec.ID, vaultGrant("*", ""))

	fc := &cdpfake.Client{}
	h := NewRouter(Deps{
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:    "test",
		CDPFactory: &stubCDPFactory{c: fc},
		Vault:      v,
		Injector:   newTestInjector(v),
	})
	body := bytes.NewBufferString(`{"session_id":"s1","url":"https://github.com/foo"}`)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/browser/navigate", body))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if len(fc.CookiesSet) != 1 || fc.CookiesSet[0][0].Value != "abc123" {
		t.Errorf("cookie not installed before navigate: %+v", fc.CookiesSet)
	}
	if fc.NavigateURL != "https://github.com/foo" {
		t.Errorf("navigate not called: %q", fc.NavigateURL)
	}
	// Response should report the vault outcome.
	if !strings.Contains(rr.Body.String(), `"action":"cookies_installed"`) {
		t.Errorf("response missing vault outcome: %s", rr.Body.String())
	}
}

// TestDefaultCDPClientFactory_GetErrorPaths exercises the two error
// branches in defaultCDPFactory.Get that don't need a real Chrome
// process to reach: (1) session.Runtime.Get returns ErrSessionNotFound,
// and (2) the session exists but has no CDPEndpoint. The happy path
// dials chromedp + WaitReady, which needs a real Chromium — covered
// by the integration suite, not unit tests.
func TestDefaultCDPClientFactory_GetErrorPaths(t *testing.T) {
	rt := fake.New()
	factory := DefaultCDPClientFactory(rt)
	if factory == nil {
		t.Fatal("DefaultCDPClientFactory returned nil")
	}

	// (1) Unknown session id → ErrSessionNotFound propagates verbatim
	// so the browser handler can map it to 404.
	if _, err := factory.Get(context.Background(), "no-such-session"); err == nil {
		t.Error("Get on unknown session must error")
	} else if !errors.Is(err, session.ErrSessionNotFound) {
		t.Errorf("err = %v; want session.ErrSessionNotFound", err)
	}

	// (2) Session exists but has no CDPEndpoint — the factory must
	// refuse rather than hand back a half-built client.
	s, err := rt.Create(context.Background(), session.Spec{Image: "browser:1"})
	if err != nil {
		t.Fatal(err)
	}
	// fake.Create doesn't populate CDPEndpoint — that mirrors the real
	// behavior of a pending container that hasn't bound CDP yet.
	if _, err := factory.Get(context.Background(), s.ID); err == nil {
		t.Error("Get on session with no CDPEndpoint must error")
	} else if !strings.Contains(err.Error(), "CDP endpoint") {
		t.Errorf("err = %v; want one mentioning CDP endpoint", err)
	}
}

// TestDefaultCDPClientFactory_Evict covers both Evict branches:
// the cached-hit path (Close called, cache entry removed) and the
// no-op path (unknown id). We populate the cache directly with a
// fake cdp.Client since spinning up a real one needs Chromium.
func TestDefaultCDPClientFactory_Evict(t *testing.T) {
	fc := &cdpfake.Client{}
	f := &defaultCDPFactory{
		rt:    fake.New(),
		cache: map[string]cdp.Client{"s1": fc},
	}
	// No-op path: Evict on an unknown id should not panic and should
	// leave the cache untouched.
	f.Evict("missing")
	if len(f.cache) != 1 {
		t.Errorf("unknown-id Evict mutated cache: %+v", f.cache)
	}

	// Hit path: Evict on s1 removes the entry and Closes the client.
	f.Evict("s1")
	if _, ok := f.cache["s1"]; ok {
		t.Error("Evict left s1 in cache")
	}
	if fc.CloseCallCount != 1 {
		t.Errorf("Evict CloseCallCount = %d; want 1", fc.CloseCallCount)
	}

	// Idempotency: Evict again on s1 (already gone) is safe.
	f.Evict("s1")
}

func TestBrowserNavigateWithoutInjectorIsUnchanged(t *testing.T) {
	h, fc := newBrowserRouter(t)
	body := bytes.NewBufferString(`{"session_id":"s1","url":"https://example.com"}`)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/browser/navigate", body))
	if rr.Code != http.StatusOK || fc.NavigateURL != "https://example.com" {
		t.Fatalf("baseline navigate broke: code=%d url=%q", rr.Code, fc.NavigateURL)
	}
	if len(fc.CookiesSet) != 0 {
		t.Errorf("no injector should mean no cookies: %+v", fc.CookiesSet)
	}
}
