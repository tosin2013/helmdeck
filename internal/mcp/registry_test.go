package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/tosin2013/helmdeck/internal/store"
)

func newTestRegistry(t *testing.T) *Registry {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewRegistry(db)
}

func TestCreateAndGet(t *testing.T) {
	r := newTestRegistry(t)
	ctx := context.Background()
	cfg, _ := json.Marshal(StdioConfig{Command: "echo", Args: []string{"hi"}})
	s, err := r.Create(ctx, CreateInput{
		Name:      "echo",
		Transport: TransportStdio,
		Config:    cfg,
		Enabled:   true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if s.ID == "" || s.Name != "echo" || s.Transport != TransportStdio {
		t.Errorf("server = %+v", s)
	}
	got, err := r.Get(ctx, s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "echo" || !got.Enabled {
		t.Errorf("get = %+v", got)
	}
}

func TestCreateRejectsBadInput(t *testing.T) {
	r := newTestRegistry(t)
	ctx := context.Background()
	stdioCfg, _ := json.Marshal(StdioConfig{Command: "x"})
	cases := map[string]CreateInput{
		"missing name":      {Transport: TransportStdio, Config: stdioCfg},
		"missing transport": {Name: "x", Config: stdioCfg},
		"bad transport":     {Name: "x", Transport: "carrier-pigeon", Config: stdioCfg},
		"missing config":    {Name: "x", Transport: TransportStdio},
		"stdio no command":  {Name: "x", Transport: TransportStdio, Config: json.RawMessage(`{}`)},
		"sse no url":        {Name: "x", Transport: TransportSSE, Config: json.RawMessage(`{}`)},
		"ws no url":         {Name: "x", Transport: TransportWebSocket, Config: json.RawMessage(`{}`)},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := r.Create(ctx, in); err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestCreateDuplicate(t *testing.T) {
	r := newTestRegistry(t)
	ctx := context.Background()
	cfg, _ := json.Marshal(StdioConfig{Command: "x"})
	if _, err := r.Create(ctx, CreateInput{Name: "dup", Transport: TransportStdio, Config: cfg}); err != nil {
		t.Fatal(err)
	}
	_, err := r.Create(ctx, CreateInput{Name: "dup", Transport: TransportStdio, Config: cfg})
	if !errors.Is(err, ErrDuplicateName) {
		t.Errorf("err = %v, want ErrDuplicateName", err)
	}
}

func TestList(t *testing.T) {
	r := newTestRegistry(t)
	ctx := context.Background()
	for _, name := range []string{"zeta", "alpha", "beta"} {
		cfg, _ := json.Marshal(StdioConfig{Command: name})
		_, _ = r.Create(ctx, CreateInput{Name: name, Transport: TransportStdio, Config: cfg})
	}
	list, err := r.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 || list[0].Name != "alpha" || list[2].Name != "zeta" {
		t.Errorf("list not sorted: %+v", list)
	}
}

func TestUpdate(t *testing.T) {
	r := newTestRegistry(t)
	ctx := context.Background()
	cfg, _ := json.Marshal(StdioConfig{Command: "x"})
	s, _ := r.Create(ctx, CreateInput{Name: "a", Transport: TransportStdio, Config: cfg, Enabled: true})

	newCfg, _ := json.Marshal(StdioConfig{Command: "y"})
	updated, err := r.Update(ctx, s.ID, CreateInput{
		Name:      "a-renamed",
		Transport: TransportStdio,
		Config:    newCfg,
		Enabled:   false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "a-renamed" || updated.Enabled {
		t.Errorf("updated = %+v", updated)
	}
}

func TestUpdateInvalidatesManifestCache(t *testing.T) {
	r := newTestRegistry(t)
	ctx := context.Background()
	cfg, _ := json.Marshal(StdioConfig{Command: "x"})
	s, _ := r.Create(ctx, CreateInput{Name: "a", Transport: TransportStdio, Config: cfg, Enabled: true})

	// Inject a fake adapter so Manifest() succeeds without spawning
	// a real process.
	r.WithAdapterFactory(func(*Server) (Adapter, error) {
		return fakeAdapter{tools: []Tool{{Name: "tool1"}}}, nil
	})
	if _, err := r.Manifest(ctx, s.ID, false); err != nil {
		t.Fatal(err)
	}
	// Confirm cache populated
	got, _ := r.Get(ctx, s.ID)
	if got.Manifest == nil || len(got.Manifest.Tools) != 1 {
		t.Fatalf("cache not populated: %+v", got.Manifest)
	}

	// Update -> cache should be cleared
	newCfg, _ := json.Marshal(StdioConfig{Command: "y"})
	if _, err := r.Update(ctx, s.ID, CreateInput{Name: "a", Transport: TransportStdio, Config: newCfg, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	got, _ = r.Get(ctx, s.ID)
	if got.Manifest != nil {
		t.Errorf("cache not invalidated after update: %+v", got.Manifest)
	}
}

func TestDelete(t *testing.T) {
	r := newTestRegistry(t)
	ctx := context.Background()
	cfg, _ := json.Marshal(StdioConfig{Command: "x"})
	s, _ := r.Create(ctx, CreateInput{Name: "a", Transport: TransportStdio, Config: cfg})
	if err := r.Delete(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(ctx, s.ID); !errors.Is(err, ErrServerNotFound) {
		t.Errorf("get after delete: %v", err)
	}
	if err := r.Delete(ctx, "missing"); !errors.Is(err, ErrServerNotFound) {
		t.Errorf("delete missing: %v", err)
	}
}

type fakeAdapter struct {
	tools []Tool
	calls int
	err   error
}

func (f fakeAdapter) FetchManifest(ctx context.Context) (*Manifest, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &Manifest{Tools: f.tools}, nil
}

func TestManifestFetchAndCache(t *testing.T) {
	r := newTestRegistry(t)
	ctx := context.Background()
	cfg, _ := json.Marshal(StdioConfig{Command: "x"})
	s, _ := r.Create(ctx, CreateInput{Name: "a", Transport: TransportStdio, Config: cfg, Enabled: true})

	calls := 0
	r.WithAdapterFactory(func(*Server) (Adapter, error) {
		calls++
		return fakeAdapter{tools: []Tool{{Name: "browser.screenshot_url"}}}, nil
	})

	m, err := r.Manifest(ctx, s.ID, false)
	if err != nil || len(m.Tools) != 1 {
		t.Fatalf("first fetch: %v %+v", err, m)
	}
	if calls != 1 {
		t.Errorf("calls = %d after first fetch", calls)
	}
	// Second call within TTL should hit cache, not adapter
	if _, err := r.Manifest(ctx, s.ID, false); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("calls = %d after cached read (want 1)", calls)
	}
	// Force refresh bypasses cache
	if _, err := r.Manifest(ctx, s.ID, true); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("calls = %d after force (want 2)", calls)
	}
}

func TestManifestExpired(t *testing.T) {
	r := newTestRegistry(t)
	ctx := context.Background()
	cfg, _ := json.Marshal(StdioConfig{Command: "x"})
	s, _ := r.Create(ctx, CreateInput{Name: "a", Transport: TransportStdio, Config: cfg, Enabled: true})

	calls := 0
	r.WithAdapterFactory(func(*Server) (Adapter, error) {
		calls++
		return fakeAdapter{tools: []Tool{{Name: "x"}}}, nil
	})
	r.ttl = 1 * time.Millisecond

	if _, err := r.Manifest(ctx, s.ID, false); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := r.Manifest(ctx, s.ID, false); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("expected 2 calls after expiry, got %d", calls)
	}
}

func TestManifestDisabledServer(t *testing.T) {
	r := newTestRegistry(t)
	ctx := context.Background()
	cfg, _ := json.Marshal(StdioConfig{Command: "x"})
	s, _ := r.Create(ctx, CreateInput{Name: "a", Transport: TransportStdio, Config: cfg, Enabled: false})
	_, err := r.Manifest(ctx, s.ID, false)
	if err == nil {
		t.Error("expected error for disabled server")
	}
}

func TestSSEAndWebSocketStubs(t *testing.T) {
	sse := NewSSEAdapter(SSEConfig{URL: "http://x"})
	if _, err := sse.FetchManifest(context.Background()); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("sse err = %v", err)
	}
	ws := NewWebSocketAdapter(WebSocketConfig{URL: "ws://x"})
	if _, err := ws.FetchManifest(context.Background()); !errors.Is(err, ErrNotImplemented) {
		t.Errorf("ws err = %v", err)
	}
}
