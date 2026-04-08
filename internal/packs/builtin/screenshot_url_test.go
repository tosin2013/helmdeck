package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/tosin2013/helmdeck/internal/cdp"
	cdpfake "github.com/tosin2013/helmdeck/internal/cdp/fake"
	"github.com/tosin2013/helmdeck/internal/packs"
	"github.com/tosin2013/helmdeck/internal/session"
)

// fakeRuntime is a no-op session runtime that satisfies the engine's
// NeedsSession path without spinning up Docker.
type fakeRuntime struct{}

func (fakeRuntime) Create(ctx context.Context, spec session.Spec) (*session.Session, error) {
	return &session.Session{ID: "sess-1", Status: session.StatusRunning}, nil
}
func (fakeRuntime) Get(ctx context.Context, id string) (*session.Session, error) { return nil, nil }
func (fakeRuntime) List(ctx context.Context) ([]*session.Session, error)         { return nil, nil }
func (fakeRuntime) Logs(ctx context.Context, id string) (io.ReadCloser, error)   { return nil, nil }
func (fakeRuntime) Terminate(ctx context.Context, id string) error               { return nil }
func (fakeRuntime) Close() error                                                  { return nil }

// fakeFactory hands out a single shared fake CDP client so tests can
// inspect what the pack handler called.
type fakeFactory struct {
	client *cdpfake.Client
	evicts int
}

func (f *fakeFactory) Get(ctx context.Context, id string) (cdp.Client, error) {
	return f.client, nil
}
func (f *fakeFactory) Evict(id string) { f.evicts++ }

func newEngine(t *testing.T, fc *fakeFactory) *packs.Engine {
	t.Helper()
	return packs.New(
		packs.WithRuntime(fakeRuntime{}),
		packs.WithCDPFactory(fc),
		packs.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)
}

func TestScreenshotURLHappyPath(t *testing.T) {
	fc := &fakeFactory{client: &cdpfake.Client{ScreenshotPNG: []byte("\x89PNG\r\n\x1a\nfakebytes")}}
	eng := newEngine(t, fc)

	res, err := eng.Execute(context.Background(), ScreenshotURL(), json.RawMessage(`{"url":"https://example.com","fullPage":true}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if fc.client.NavigateURL != "https://example.com" {
		t.Errorf("navigate url = %q", fc.client.NavigateURL)
	}
	if fc.evicts != 1 {
		t.Errorf("evicts = %d, want 1", fc.evicts)
	}

	var out struct {
		URL         string `json:"url"`
		ArtifactKey string `json:"artifact_key"`
		Size        int    `json:"size"`
	}
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatal(err)
	}
	if out.URL != "https://example.com" || out.Size != len(fc.client.ScreenshotPNG) {
		t.Errorf("output = %+v", out)
	}
	if len(res.Artifacts) != 1 || res.Artifacts[0].Size != int64(len(fc.client.ScreenshotPNG)) {
		t.Errorf("artifacts = %+v", res.Artifacts)
	}
}

func TestScreenshotURLRejectsMissingURL(t *testing.T) {
	fc := &fakeFactory{client: &cdpfake.Client{ScreenshotPNG: []byte("x")}}
	eng := newEngine(t, fc)
	_, err := eng.Execute(context.Background(), ScreenshotURL(), json.RawMessage(`{}`))
	var perr *packs.PackError
	if !errors.As(err, &perr) || perr.Code != packs.CodeInvalidInput {
		t.Errorf("err = %v, want CodeInvalidInput", err)
	}
}

func TestScreenshotURLNavigateError(t *testing.T) {
	fc := &fakeFactory{client: &cdpfake.Client{NavigateErr: errors.New("net unreachable"), ScreenshotPNG: []byte("x")}}
	eng := newEngine(t, fc)
	_, err := eng.Execute(context.Background(), ScreenshotURL(), json.RawMessage(`{"url":"https://x"}`))
	var perr *packs.PackError
	if !errors.As(err, &perr) {
		t.Fatalf("err type = %T", err)
	}
	if perr.Code != packs.CodeHandlerFailed {
		t.Errorf("code = %q", perr.Code)
	}
}

func TestScreenshotURLScreenshotError(t *testing.T) {
	fc := &fakeFactory{client: &cdpfake.Client{ScreenshotErr: errors.New("oom")}}
	eng := newEngine(t, fc)
	_, err := eng.Execute(context.Background(), ScreenshotURL(), json.RawMessage(`{"url":"https://x"}`))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestScreenshotURLNoCDPFactory(t *testing.T) {
	// Engine has runtime but no CDP factory: handler must surface
	// session_unavailable instead of nil-dereferencing on ec.CDP.
	eng := packs.New(
		packs.WithRuntime(fakeRuntime{}),
		packs.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)
	_, err := eng.Execute(context.Background(), ScreenshotURL(), json.RawMessage(`{"url":"https://x"}`))
	var perr *packs.PackError
	if !errors.As(err, &perr) || perr.Code != packs.CodeSessionUnavailable {
		t.Errorf("err = %v, want CodeSessionUnavailable", err)
	}
}
