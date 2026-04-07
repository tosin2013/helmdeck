// Package fake is an in-memory [session.Runtime] used by handler tests so
// the API layer can be exercised without a real Docker daemon. It is not
// imported by production code.
package fake

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/tosin2013/helmdeck/internal/session"
)

// Runtime is a goroutine-safe in-memory implementation of session.Runtime.
type Runtime struct {
	mu       sync.Mutex
	sessions map[string]*session.Session

	// CreateErr, when non-nil, is returned by Create instead of creating
	// a new session. Useful for testing error paths.
	CreateErr error
}

// New returns an empty fake runtime.
func New() *Runtime {
	return &Runtime{sessions: make(map[string]*session.Session)}
}

// Create implements session.Runtime.
func (r *Runtime) Create(_ context.Context, spec session.Spec) (*session.Session, error) {
	if r.CreateErr != nil {
		return nil, r.CreateErr
	}
	id := uuid.NewString()
	s := &session.Session{
		ID:          id,
		ContainerID: "fake-" + id,
		Status:      session.StatusRunning,
		CreatedAt:   time.Now().UTC(),
		Spec:        spec,
	}
	r.mu.Lock()
	r.sessions[id] = s
	r.mu.Unlock()
	cp := *s
	return &cp, nil
}

// Get implements session.Runtime.
func (r *Runtime) Get(_ context.Context, id string) (*session.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[id]
	if !ok {
		return nil, session.ErrSessionNotFound
	}
	cp := *s
	return &cp, nil
}

// List implements session.Runtime.
func (r *Runtime) List(_ context.Context) ([]*session.Session, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*session.Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		cp := *s
		out = append(out, &cp)
	}
	return out, nil
}

// Logs implements session.Runtime. The fake returns a static line so
// streaming handlers can be exercised end-to-end.
func (r *Runtime) Logs(_ context.Context, id string) (io.ReadCloser, error) {
	r.mu.Lock()
	_, ok := r.sessions[id]
	r.mu.Unlock()
	if !ok {
		return nil, session.ErrSessionNotFound
	}
	return io.NopCloser(strings.NewReader(fmt.Sprintf("fake log for %s\n", id))), nil
}

// Terminate implements session.Runtime. Idempotent.
func (r *Runtime) Terminate(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, id)
	return nil
}

// Close implements session.Runtime.
func (r *Runtime) Close() error { return nil }

// Inject is a test helper that places an explicit session in the table
// (useful for watchdog tests that need a session with a precise CreatedAt).
func (r *Runtime) Inject(s *session.Session) {
	r.mu.Lock()
	r.sessions[s.ID] = s
	r.mu.Unlock()
}

// compile-time interface check
var _ session.Runtime = (*Runtime)(nil)
