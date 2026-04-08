package packs

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestJanitor_DeletesExpired(t *testing.T) {
	store := NewMemoryArtifactStore()
	now := time.Date(2026, 4, 8, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now.Add(-30 * 24 * time.Hour) }
	mustPut(t, store, "browser.screenshot_url", "old.png", []byte("oldcontent"))
	store.now = func() time.Time { return now.Add(-1 * time.Hour) }
	mustPut(t, store, "browser.screenshot_url", "fresh.png", []byte("freshcontent"))

	j := NewJanitor(JanitorConfig{
		Store:      store,
		Interval:   time.Hour,
		DefaultTTL: 7 * 24 * time.Hour,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	j.Now = func() time.Time { return now }

	j.sweep(context.Background())

	all, _ := store.ListAll(context.Background())
	if len(all) != 1 {
		t.Fatalf("expected 1 artifact remaining, got %d", len(all))
	}
	if !contains(all[0].Key, "fresh.png") {
		t.Errorf("wrong artifact survived: %s", all[0].Key)
	}
}

func TestJanitor_PerPackTTLOverride(t *testing.T) {
	store := NewMemoryArtifactStore()
	now := time.Date(2026, 4, 8, 12, 0, 0, 0, time.UTC)
	// One artifact under a pack with a 1-hour TTL, aged 2 hours.
	store.now = func() time.Time { return now.Add(-2 * time.Hour) }
	mustPut(t, store, "ephemeral.pack", "tmp.json", []byte("x"))
	// One artifact under the default-TTL pack, aged the same 2 hours
	// — should survive because the default is much longer.
	mustPut(t, store, "browser.screenshot_url", "shot.png", []byte("y"))

	j := NewJanitor(JanitorConfig{
		Store:      store,
		DefaultTTL: 7 * 24 * time.Hour,
		PackTTL: func(name string) (time.Duration, bool) {
			if name == "ephemeral.pack" {
				return time.Hour, true
			}
			return 0, false
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	j.Now = func() time.Time { return now }
	j.sweep(context.Background())

	all, _ := store.ListAll(context.Background())
	if len(all) != 1 {
		t.Fatalf("expected exactly the screenshot to survive, got %d", len(all))
	}
	if all[0].Pack != "browser.screenshot_url" {
		t.Errorf("wrong artifact survived: pack=%s", all[0].Pack)
	}
}

func TestJanitor_NilStoreNoOp(t *testing.T) {
	if NewJanitor(JanitorConfig{Store: nil}) != nil {
		t.Fatal("expected nil janitor for nil store")
	}
	// Calling Run on a nil receiver must not panic.
	var j *Janitor
	j.Run(context.Background())
}

func TestJanitor_RunStopsOnContextCancel(t *testing.T) {
	store := NewMemoryArtifactStore()
	j := NewJanitor(JanitorConfig{
		Store:      store,
		Interval:   10 * time.Millisecond,
		DefaultTTL: time.Hour,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		j.Run(ctx)
		close(done)
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("janitor did not stop after context cancel")
	}
}

// helpers

func mustPut(t *testing.T, store *MemoryArtifactStore, pack, name string, content []byte) {
	t.Helper()
	if _, err := store.Put(context.Background(), pack, name, content, "application/octet-stream"); err != nil {
		t.Fatal(err)
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
