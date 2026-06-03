package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tosin2013/helmdeck/internal/session"
)

// stubRuntime is a minimal session.Runtime that only implements Get;
// the desktop VNC endpoint never calls the other methods so the rest
// can panic to catch accidental misuse.
type stubRuntime struct {
	session *session.Session
	err     error
}

func (s stubRuntime) Get(_ context.Context, id string) (*session.Session, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.session == nil || s.session.ID != id {
		return nil, session.ErrSessionNotFound
	}
	cp := *s.session
	return &cp, nil
}
func (stubRuntime) Create(context.Context, session.Spec) (*session.Session, error) {
	panic("not implemented")
}
func (stubRuntime) List(context.Context) ([]*session.Session, error) { panic("not implemented") }
func (stubRuntime) Logs(context.Context, string) (io.ReadCloser, error) {
	panic("not implemented")
}
func (stubRuntime) Terminate(context.Context, string) error { panic("not implemented") }
func (stubRuntime) ExtendTimeout(context.Context, string, time.Duration) error {
	return nil
}
func (stubRuntime) Close() error { return nil }

func newVNCRouter(t *testing.T, rt session.Runtime) http.Handler {
	t.Helper()
	return NewRouter(Deps{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
		Runtime: rt,
	})
}

func TestVNCURLDesktopMode(t *testing.T) {
	rt := stubRuntime{session: &session.Session{
		ID:          "sess-1",
		Status:      session.StatusRunning,
		CDPEndpoint: "ws://helmdeck-session-abc.baas-net:9222/devtools/browser/x",
		Spec:        session.Spec{Env: map[string]string{"HELMDECK_MODE": "desktop"}},
	}}
	h := newVNCRouter(t, rt)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/desktop/vnc-url?session_id=sess-1", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var info VNCInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.SessionID != "sess-1" {
		t.Errorf("session id = %s", info.SessionID)
	}
	if info.Host != "helmdeck-session-abc.baas-net" {
		t.Errorf("host parsed wrong: %s", info.Host)
	}
	if info.Port != "6080" {
		t.Errorf("port = %s", info.Port)
	}
	if info.URL == "" || info.URL[:7] != "http://" {
		t.Errorf("url malformed: %s", info.URL)
	}
	if info.ExpiresAt.IsZero() {
		t.Errorf("expires_at not set")
	}
}

func TestVNCURLPublicBaseOverride(t *testing.T) {
	t.Setenv("HELMDECK_VNC_PUBLIC_BASE", "http://localhost:8080")
	rt := stubRuntime{session: &session.Session{
		ID:          "sess-2",
		CDPEndpoint: "ws://helmdeck-session-xyz:9222/devtools/browser/y",
		Spec:        session.Spec{Env: map[string]string{"HELMDECK_MODE": "desktop"}},
	}}
	h := newVNCRouter(t, rt)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/desktop/vnc-url?session_id=sess-2", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	var info VNCInfo
	_ = json.Unmarshal(rr.Body.Bytes(), &info)
	if info.URL[:21] != "http://localhost:8080" {
		t.Errorf("url did not honor HELMDECK_VNC_PUBLIC_BASE: %s", info.URL)
	}
	if info.Host != "localhost" {
		t.Errorf("host should be rewritten to public base: %s", info.Host)
	}
}

func TestVNCURLRejectsHeadlessSession(t *testing.T) {
	rt := stubRuntime{session: &session.Session{
		ID:          "sess-3",
		CDPEndpoint: "ws://h:9222/devtools/browser/x",
		Spec:        session.Spec{Env: map[string]string{}}, // no HELMDECK_MODE
	}}
	h := newVNCRouter(t, rt)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/desktop/vnc-url?session_id=sess-3", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-desktop session, got %d", rr.Code)
	}
	if !contains([]byte(rr.Body.String()), "not_desktop_mode") {
		t.Errorf("expected not_desktop_mode error: %s", rr.Body.String())
	}
}

func TestVNCURLSessionNotFound(t *testing.T) {
	h := newVNCRouter(t, stubRuntime{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/desktop/vnc-url?session_id=nope", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

func TestVNCURLMissingSessionID(t *testing.T) {
	h := newVNCRouter(t, stubRuntime{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/desktop/vnc-url", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestVNCURLNoRuntimeReturns503(t *testing.T) {
	h := NewRouter(Deps{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
	})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/desktop/vnc-url?session_id=s", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rr.Code)
	}
}
