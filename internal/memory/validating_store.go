// Package memory — MemoryStore wrapper that validates writes via middleware.
package memory

import (
	"context"
	"fmt"
)

// ValidatingStore wraps a MemoryStore and runs writes through a
// MemoryMiddleware before delegating to the backing store. Reads pass
// through unchanged.
//
// This is the integration point for issue #268: wrap a store with
// ValidatingStore + OWASPMemoryMiddleware to reject poisoned entries
// before they hit memory.
type ValidatingStore struct {
	store      MemoryStore
	middleware MemoryMiddleware
}

// NewValidatingStore wraps store with middleware. When middleware is nil,
// this degenerates to a pass-through (no validation).
func NewValidatingStore(store MemoryStore, middleware MemoryMiddleware) *ValidatingStore {
	return &ValidatingStore{
		store:      store,
		middleware: middleware,
	}
}

// Put validates the entry via middleware before delegating to the store.
// When middleware rejects it (Allowed=false), Put returns an error.
func (v *ValidatingStore) Put(ctx context.Context, ns, key string, value []byte, opts ...PutOption) (Entry, error) {
	if v.middleware != nil {
		cfg := applyPutOptions(opts)
		source := ValidationSource{
			Category:  cfg.category,
			Namespace: ns,
			// SourceClass is not captured in PutOptions today — callers
			// would need to pass it via a new WithSourceClass option.
			// For now, default to "agent_authored".
			SourceClass: "agent_authored",
		}
		verdict := v.middleware.Validate(ctx, key, value, source)
		if !verdict.Allowed {
			return Entry{}, fmt.Errorf("memory validation blocked write (action=%s, reason=%s)", verdict.Action, verdict.Reason)
		}
		// If verdict.Action == "redact", we'd modify value here.
		// For the first implementation, redact is not yet handled — it
		// requires agreement on what "redact" means (mask patterns?
		// quarantine the entry?). Log a warning and proceed.
		if verdict.Action == "redact" {
			// TODO: implement redaction once the contract is agreed.
			// For now, log and store as-is.
		}
	}
	return v.store.Put(ctx, ns, key, value, opts...)
}

// Get delegates to the store unchanged.
func (v *ValidatingStore) Get(ctx context.Context, ns, key string) (Entry, error) {
	return v.store.Get(ctx, ns, key)
}

// List delegates to the store unchanged.
func (v *ValidatingStore) List(ctx context.Context, ns, prefix string) ([]Entry, error) {
	return v.store.List(ctx, ns, prefix)
}

// Delete delegates to the store unchanged.
func (v *ValidatingStore) Delete(ctx context.Context, ns, key string) error {
	return v.store.Delete(ctx, ns, key)
}

// ListNamespaces delegates to the store unchanged.
func (v *ValidatingStore) ListNamespaces(ctx context.Context) ([]NamespaceCount, error) {
	return v.store.ListNamespaces(ctx)
}

// DeletePrefix delegates to the store unchanged.
func (v *ValidatingStore) DeletePrefix(ctx context.Context, ns, prefix string) (int, error) {
	return v.store.DeletePrefix(ctx, ns, prefix)
}

// ListAll delegates to the store unchanged.
func (v *ValidatingStore) ListAll(ctx context.Context) ([]Entry, error) {
	return v.store.ListAll(ctx)
}

// DeleteExpired delegates to the store unchanged.
func (v *ValidatingStore) DeleteExpired(ctx context.Context) (int, error) {
	return v.store.DeleteExpired(ctx)
}
