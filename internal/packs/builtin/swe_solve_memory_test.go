package builtin

import (
	"context"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/memory"
	"github.com/tosin2013/helmdeck/internal/packs"
)

// fakeMemory is a tiny MemoryInterface backed by an InMemoryStore for a
// fixed namespace, so the swe.solve recall/store helpers can be tested
// without the full engine.
type fakeMemory struct {
	store *memory.InMemoryStore
	ns    string
}

func newFakeMemory() *fakeMemory {
	return &fakeMemory{store: memory.NewInMemoryStore(), ns: "test"}
}

func (f *fakeMemory) Namespace() string { return f.ns }
func (f *fakeMemory) Store(key string, value []byte, opts ...memory.PutOption) error {
	_, err := f.store.Put(context.Background(), f.ns, key, value, opts...)
	return err
}
func (f *fakeMemory) Recall(key string) (*memory.Entry, error) {
	e, err := f.store.Get(context.Background(), f.ns, key)
	if err != nil {
		return nil, err
	}
	return &e, nil
}
func (f *fakeMemory) List(prefix string) ([]memory.Entry, error) {
	return f.store.List(context.Background(), f.ns, prefix)
}
func (f *fakeMemory) Delete(key string) error {
	return f.store.Delete(context.Background(), f.ns, key)
}
func (f *fakeMemory) Context() (*packs.SessionContext, error) {
	return &packs.SessionContext{Namespace: f.ns}, nil
}

// TestPriorContextNilSafe proves swe.solve's recall hook returns the
// task unchanged when no memory is wired (ec.Memory == nil) — the
// pre-memory behavior.
func TestPriorContextNilSafe(t *testing.T) {
	in := sweSolveInput{RepoURL: "https://github.com/o/r.git", Task: "fix the bug"}
	ec := &packs.ExecutionContext{} // Memory is nil
	if got := priorContext(context.Background(), ec, in); got != in.Task {
		t.Fatalf("nil-memory priorContext should return task unchanged, got %q", got)
	}
}

// TestStoreAndRecallSolveNote proves a stored solve note is prepended
// to the task on the next solve against the same repo.
func TestStoreAndRecallSolveNote(t *testing.T) {
	fm := newFakeMemory()
	ec := &packs.ExecutionContext{Memory: fm}
	repo := "https://github.com/o/r.git"

	// First solve stores a note.
	storeSolveNote(ec, repo, "fixed null deref in parser", "swe.solve/abc-trajectory.json", "helmdeck/swe-solve-abc123")

	// Next solve recalls it and prepends to the task.
	in := sweSolveInput{RepoURL: repo, Task: "add validation"}
	got := priorContext(context.Background(), ec, in)
	if !strings.Contains(got, "fixed null deref in parser") {
		t.Fatalf("prior note not prepended: %q", got)
	}
	if !strings.Contains(got, "add validation") {
		t.Fatalf("task missing from prior-context output: %q", got)
	}
	if !strings.HasSuffix(got, "add validation") {
		t.Fatalf("task should come last (after prior context): %q", got)
	}
}

// TestSolveNoteNamespacedByRepo proves a note stored for one repo is
// not recalled for a different repo.
func TestSolveNoteNamespacedByRepo(t *testing.T) {
	fm := newFakeMemory()
	ec := &packs.ExecutionContext{Memory: fm}
	storeSolveNote(ec, "https://github.com/o/repo-a.git", "note-a", "k", "b")

	in := sweSolveInput{RepoURL: "https://github.com/o/repo-b.git", Task: "do thing"}
	got := priorContext(context.Background(), ec, in)
	if got != in.Task {
		t.Fatalf("note from repo-a leaked into repo-b solve: %q", got)
	}
}
