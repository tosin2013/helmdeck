package packs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// Artifact is one file produced during a pack run. T211 will replace
// the in-memory store with an S3-compatible backend that fills URL
// with a signed link; for T205 the URL is a local-only handle the
// REST layer can resolve into a download stream.
type Artifact struct {
	Key         string    `json:"key"`
	URL         string    `json:"url"`
	Size        int64     `json:"size"`
	ContentType string    `json:"content_type"`
	CreatedAt   time.Time `json:"created_at"`
	Pack        string    `json:"pack"`
}

// ArtifactStore is the contract every backend implements. The engine
// only ever calls Put (from inside handlers via ExecutionContext)
// and ListForPack (after the handler returns) — Get/Delete live in
// the REST artifact endpoint that T211 will land alongside S3.
type ArtifactStore interface {
	Put(ctx context.Context, pack, name string, content []byte, contentType string) (Artifact, error)
	ListForPack(ctx context.Context, pack string) ([]Artifact, error)
	Get(ctx context.Context, key string) ([]byte, Artifact, error)
}

// MemoryArtifactStore is the dev/test backend. It keeps content in a
// map keyed by Artifact.Key — fine for tests and the Compose dev
// stack, but lost on restart and obviously not suitable for
// multi-replica deployments.
type MemoryArtifactStore struct {
	mu       sync.Mutex
	contents map[string][]byte
	meta     map[string]Artifact
	now      func() time.Time
}

// NewMemoryArtifactStore returns an empty in-memory store.
func NewMemoryArtifactStore() *MemoryArtifactStore {
	return &MemoryArtifactStore{
		contents: make(map[string][]byte),
		meta:     make(map[string]Artifact),
		now:      func() time.Time { return time.Now().UTC() },
	}
}

// Put stores content under a generated key namespaced by pack name.
// The key format is `<pack>/<rand>-<name>` so ListForPack can find
// every artifact for a given pack with a single prefix scan.
func (s *MemoryArtifactStore) Put(ctx context.Context, pack, name string, content []byte, contentType string) (Artifact, error) {
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return Artifact{}, err
	}
	key := pack + "/" + hex.EncodeToString(rnd[:]) + "-" + name
	a := Artifact{
		Key:         key,
		URL:         "memory://" + key,
		Size:        int64(len(content)),
		ContentType: contentType,
		CreatedAt:   s.now(),
		Pack:        pack,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Defensive copy: handlers may reuse the byte slice they passed in.
	cp := make([]byte, len(content))
	copy(cp, content)
	s.contents[key] = cp
	s.meta[key] = a
	return a, nil
}

// ListForPack returns every artifact whose key starts with `pack/`.
// Order is unspecified.
func (s *MemoryArtifactStore) ListForPack(ctx context.Context, pack string) ([]Artifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Artifact
	prefix := pack + "/"
	for k, m := range s.meta {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			out = append(out, m)
		}
	}
	return out, nil
}

// Get returns the bytes for a key.
func (s *MemoryArtifactStore) Get(ctx context.Context, key string) ([]byte, Artifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.contents[key]
	if !ok {
		return nil, Artifact{}, &PackError{Code: CodeArtifactFailed, Message: "artifact not found"}
	}
	cp := make([]byte, len(c))
	copy(cp, c)
	return cp, s.meta[key], nil
}
