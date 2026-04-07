package docker_test

import (
	"context"
	"io"
	"os"
	"testing"
	"time"

	"github.com/tosin2013/helmdeck/internal/session"
	dockerrt "github.com/tosin2013/helmdeck/internal/session/docker"
)

// dockerAvailable returns true when a local Docker daemon is reachable.
// Tests that need a real daemon skip themselves on developer machines and
// CI runners that don't expose /var/run/docker.sock.
func dockerAvailable(t *testing.T) bool {
	t.Helper()
	if _, err := os.Stat("/var/run/docker.sock"); err != nil {
		return false
	}
	return true
}

// TestCreateAndTerminate exercises the full lifecycle against a tiny image
// (alpine:3 sleep) so we don't depend on the helmdeck-sidecar image being
// built yet (that lands in T104).
func TestCreateAndTerminate(t *testing.T) {
	if !dockerAvailable(t) {
		t.Skip("docker daemon not reachable")
	}

	rt, err := dockerrt.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer rt.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	spec := session.Spec{
		Image:       "alpine:3",
		Label:       "t103-smoke",
		MemoryLimit: "128m",
		SHMSize:     "64m",
		CPULimit:    0.25,
		Env: map[string]string{
			"HELMDECK_TEST": "1",
		},
	}

	// alpine sleeps so the container stays running long enough to inspect.
	// We piggy-back on the env passing path; the real sidecar will use the
	// image's default entrypoint.
	s, err := rt.Create(ctx, spec)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if s.ID == "" || s.ContainerID == "" {
		t.Fatalf("Create returned empty IDs: %+v", s)
	}
	if s.Status != session.StatusRunning {
		t.Fatalf("Create returned status %q, want running", s.Status)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := rt.Terminate(ctx, s.ID); err != nil {
			t.Errorf("Terminate cleanup: %v", err)
		}
	})

	got, err := rt.Get(ctx, s.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != s.ID {
		t.Fatalf("Get id mismatch: got %q want %q", got.ID, s.ID)
	}

	list, err := rt.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) == 0 {
		t.Fatal("List returned empty")
	}

	// Logs may be empty for a freshly-started alpine; just verify the call
	// succeeds and the reader is well-formed.
	rc, err := rt.Logs(ctx, s.ID)
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}
	_, _ = io.Copy(io.Discard, rc)
	rc.Close()

	if err := rt.Terminate(ctx, s.ID); err != nil {
		t.Fatalf("Terminate: %v", err)
	}

	// Second terminate is a no-op.
	if err := rt.Terminate(ctx, s.ID); err != nil {
		t.Fatalf("Terminate second call: %v", err)
	}

	// After termination, Get returns ErrSessionNotFound.
	if _, err := rt.Get(ctx, s.ID); err == nil {
		t.Fatalf("Get after Terminate: expected ErrSessionNotFound, got nil")
	}
}

func TestGetUnknownSession(t *testing.T) {
	if !dockerAvailable(t) {
		t.Skip("docker daemon not reachable")
	}
	rt, err := dockerrt.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer rt.Close()

	if _, err := rt.Get(context.Background(), "definitely-not-a-real-id"); err == nil {
		t.Fatalf("expected ErrSessionNotFound, got nil")
	}
}
