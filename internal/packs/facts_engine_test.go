// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package packs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tosin2013/helmdeck/internal/memory"
)

// facts_engine_test.go (PR F of the v0.25.0 reliability arc) covers
// the exported StoreFact convenience wrapper + the FactStoreError
// Error() method. ValidateFact is already covered by facts_test.go;
// this file pins the two remaining 0%-coverage exports.

// TestFactStoreError_Error pins the FactStoreError stringification —
// the error message must round-trip through errors.As + Error() so
// callers wrapping it (REST + pack) can surface the same message.
func TestFactStoreError_Error(t *testing.T) {
	e := &FactStoreError{Code: FactErrMissingKey, Message: "key is required"}
	if e.Error() != "key is required" {
		t.Errorf("Error() = %q; want %q", e.Error(), "key is required")
	}
	// errors.As round-trip — the REST handler at internal/api/memory.go
	// uses this seam to extract the FactStoreErrCode for status mapping.
	var as *FactStoreError
	if !errors.As(e, &as) || as.Code != FactErrMissingKey {
		t.Errorf("errors.As did not preserve Code: %+v", as)
	}
}

// TestStoreFact_HappyPath — the REST handler's primary entry point.
// Pin: row lands in the namespace under the requested key, TTL +
// category propagate through the PutOption slice ValidateFact built.
func TestStoreFact_HappyPath(t *testing.T) {
	store := memory.NewInMemoryStore()
	caller := "alice"

	entry, err := StoreFact(context.Background(), store, caller, StoreFactRequest{
		Key:      "preferences/frontend",
		Value:    "React over Vue",
		Category: "preferences",
		TTL:      2 * time.Hour,
	})
	if err != nil {
		t.Fatalf("StoreFact: %v", err)
	}
	if entry.Key != "preferences/frontend" {
		t.Errorf("Key = %q", entry.Key)
	}
	if string(entry.Value) != "React over Vue" {
		t.Errorf("Value = %q", entry.Value)
	}
	if entry.Category != "preferences" {
		t.Errorf("Category = %q; want preferences", entry.Category)
	}

	// And it actually landed in the store.
	stored, gerr := store.Get(context.Background(), caller, "preferences/frontend")
	if gerr != nil {
		t.Fatalf("store.Get: %v", gerr)
	}
	if string(stored.Value) != "React over Vue" {
		t.Errorf("stored value = %q; want React over Vue", stored.Value)
	}
}

// TestStoreFact_NilStore_SynthesizesEntry — the docstring promises a
// stable response shape even when memory is disabled. Tests rely on
// the synthesized entry carrying ExpiresAt + the inputs intact so the
// REST handler's success-response body stays consistent across
// memory-on and memory-off deployments.
func TestStoreFact_NilStore_SynthesizesEntry(t *testing.T) {
	entry, err := StoreFact(context.Background(), nil, "alice", StoreFactRequest{
		Key:      "k",
		Value:    "v",
		Category: "preferences",
		TTL:      time.Hour,
	})
	if err != nil {
		t.Fatalf("StoreFact(nil store): %v", err)
	}
	if entry.Namespace != "alice" || entry.Key != "k" || string(entry.Value) != "v" {
		t.Errorf("synthesized entry shape wrong: %+v", entry)
	}
	if entry.Category != "preferences" {
		t.Errorf("synthesized category = %q", entry.Category)
	}
	// ExpiresAt should be roughly now + TTL.
	if entry.ExpiresAt.IsZero() || time.Until(entry.ExpiresAt) > time.Hour+time.Minute {
		t.Errorf("ExpiresAt = %v (now+TTL ≈ %v)", entry.ExpiresAt, time.Now().Add(time.Hour))
	}
}

// TestStoreFact_ValidationErrorPasses through — the wrapper must not
// swallow ValidateFact's typed errors. The REST handler maps each
// code to a status code; a missing-key error coerced to backend
// would surface as 500 instead of 400.
func TestStoreFact_ValidationErrorPasses(t *testing.T) {
	store := memory.NewInMemoryStore()
	_, err := StoreFact(context.Background(), store, "alice", StoreFactRequest{
		// Missing Key.
		Value: "v",
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if err.Code != FactErrMissingKey {
		t.Errorf("err.Code = %q; want %q", err.Code, FactErrMissingKey)
	}
}

// TestStoreFact_BackendErrorWraps — when the underlying memory.Put
// fails (e.g. SQLite write error), StoreFact must wrap it as
// FactErrBackend so the caller can distinguish it from a validation
// failure. The wrapping is structural — without it the REST handler
// returns 400 (invalid_input) for a 500-class failure.
func TestStoreFact_BackendErrorWraps(t *testing.T) {
	store := &alwaysFailMemoryStore{err: errors.New("disk full")}
	_, err := StoreFact(context.Background(), store, "alice", StoreFactRequest{
		Key: "k", Value: "v",
	})
	if err == nil {
		t.Fatal("expected error from failing backend")
	}
	if err.Code != FactErrBackend {
		t.Errorf("err.Code = %q; want %q", err.Code, FactErrBackend)
	}
	if !strings.Contains(err.Message, "disk full") {
		t.Errorf("err.Message %q should include the underlying error", err.Message)
	}
}

// alwaysFailMemoryStore is a tiny test stub satisfying
// memory.MemoryStore by failing every Put. Used to exercise StoreFact's
// FactErrBackend wrapping branch.
type alwaysFailMemoryStore struct {
	memory.MemoryStore // embedded to satisfy the interface; unused methods panic
	err                error
}

func (s *alwaysFailMemoryStore) Put(_ context.Context, _, _ string, _ []byte, _ ...memory.PutOption) (memory.Entry, error) {
	return memory.Entry{}, s.err
}
