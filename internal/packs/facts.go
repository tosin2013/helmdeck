package packs

// facts.go — write-surface validator for agent-supplied memory facts
// (ADR 048 PR #2). Owned by internal/packs so both the REST handler
// (internal/api/memory.go) and the helmdeck.memory_store pack
// (internal/packs/builtin/memory_store.go) can call StoreFact without
// drifting on category guards or TTL clamping.
//
// Policy:
//   - Category defaults to "user_facts" when omitted.
//   - Categories reserved for engine writes (pack_history /
//     pipeline_history) are rejected — letting agents write into
//     them would poison the my-defaults projection.
//   - TTL is mandatory. Default 90 days; min 1 hour; max 365 days.
//     The minimum prevents nonsense sub-minute TTLs that almost
//     certainly indicate a typo; the maximum keeps fact rot bounded.
//   - The store is wired per caller's bare namespace (matches the
//     audit-write seam in audit.go). Cross-session learning is
//     also cross-session recall.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tosin2013/helmdeck/internal/memory"
)

// Fact-store policy. Constants exported so the REST handler and the
// pack handler can surface them in error messages with the same
// numbers the validator checks.
const (
	DefaultFactCategory = "user_facts"
	DefaultFactTTL      = 90 * 24 * time.Hour
	MinFactTTL          = time.Hour
	MaxFactTTL          = 365 * 24 * time.Hour
)

// FactStoreErrCode is the closed-set error vocabulary StoreFact returns.
// Each error code maps to a stable HTTP status + PackError code at the
// caller layer.
type FactStoreErrCode string

const (
	FactErrMissingKey       FactStoreErrCode = "missing_key"
	FactErrMissingValue     FactStoreErrCode = "missing_value"
	FactErrReservedCategory FactStoreErrCode = "reserved_category"
	FactErrTTLTooShort      FactStoreErrCode = "ttl_too_short"
	FactErrTTLTooLong       FactStoreErrCode = "ttl_too_long"
	FactErrBackend          FactStoreErrCode = "backend"
)

// FactStoreError wraps a validation/store failure with the closed-set
// code so the caller can map it to its native wire format.
type FactStoreError struct {
	Code    FactStoreErrCode
	Message string
}

func (e *FactStoreError) Error() string { return e.Message }

// IsReservedFactCategory returns true when category is owned by the
// engine and external writers must not touch it. The reserved set is
// the audit categories; ADR 049 added plan_history so agents can't
// poison the projection via the write surface.
func IsReservedFactCategory(category string) bool {
	switch category {
	case AuditCategoryPack, AuditCategoryPipeline, AuditCategoryPlan:
		return true
	}
	return false
}

// StoreFactRequest is the validated input shape. Marshalable directly
// for tests; the REST/pack layers each define their own wire-format
// structs that translate into this.
type StoreFactRequest struct {
	Key      string
	Value    string
	Category string        // "" → DefaultFactCategory
	Tags     []string      // optional
	TTL      time.Duration // 0 → DefaultFactTTL
}

// ValidateFact applies defaults and policy checks to req. On success
// returns a normalized StoreFactRequest (Key/Value trimmed, Category
// defaulted, TTL clamped) plus the memory.PutOption slice the caller
// uses to drive the actual Put. The REST handler and the
// helmdeck.memory_store pack each call this once before doing their
// own Put — the REST handler uses memory.MemoryStore.Put directly,
// the pack uses ec.Memory.Store (the namespace-scoped adapter).
// Sharing only the validator keeps both call sites honest about the
// engine policy without forcing a put-callback indirection.
func ValidateFact(req StoreFactRequest) (StoreFactRequest, []memory.PutOption, *FactStoreError) {
	key := strings.TrimSpace(req.Key)
	if key == "" {
		return req, nil, &FactStoreError{
			Code:    FactErrMissingKey,
			Message: "key is required (e.g. \"preferences/frontend-framework\")",
		}
	}
	value := strings.TrimSpace(req.Value)
	if value == "" {
		return req, nil, &FactStoreError{
			Code:    FactErrMissingValue,
			Message: "value is required (the fact text)",
		}
	}
	category := strings.TrimSpace(req.Category)
	if category == "" {
		category = DefaultFactCategory
	}
	if IsReservedFactCategory(category) {
		return req, nil, &FactStoreError{
			Code: FactErrReservedCategory,
			Message: fmt.Sprintf("category %q is reserved for engine-written audit rows; pick a different name (e.g. %s, project_conventions, preferences)",
				category, DefaultFactCategory),
		}
	}
	ttl := req.TTL
	if ttl == 0 {
		ttl = DefaultFactTTL
	}
	if ttl < MinFactTTL {
		return req, nil, &FactStoreError{
			Code:    FactErrTTLTooShort,
			Message: fmt.Sprintf("ttl too short; minimum is %s", MinFactTTL),
		}
	}
	if ttl > MaxFactTTL {
		return req, nil, &FactStoreError{
			Code:    FactErrTTLTooLong,
			Message: fmt.Sprintf("ttl too long; maximum is %s", MaxFactTTL),
		}
	}
	out := StoreFactRequest{
		Key:      key,
		Value:    value,
		Category: category,
		Tags:     req.Tags,
		TTL:      ttl,
	}
	opts := []memory.PutOption{memory.WithTTL(ttl), memory.WithCategory(category)}
	if len(req.Tags) > 0 {
		opts = append(opts, memory.WithTags(req.Tags...))
	}
	return out, opts, nil
}

// StoreFact is a convenience for callers that hold a raw memory.MemoryStore
// (the REST handler today). It validates via ValidateFact then issues the
// Put. The helmdeck.memory_store pack does not use this — it calls
// ValidateFact directly and routes through its namespace-scoped
// ec.Memory.Store adapter.
func StoreFact(ctx context.Context, store memory.MemoryStore, caller string, req StoreFactRequest) (memory.Entry, *FactStoreError) {
	normalized, opts, verr := ValidateFact(req)
	if verr != nil {
		return memory.Entry{}, verr
	}
	if store == nil {
		// Memory-disabled deployment: synthesize the would-be entry so
		// the caller's response shape stays stable. Tests rely on this.
		return memory.Entry{
			Namespace: caller,
			Key:       normalized.Key,
			Value:     []byte(normalized.Value),
			Category:  normalized.Category,
			Tags:      normalized.Tags,
			ExpiresAt: time.Now().Add(normalized.TTL).UTC(),
		}, nil
	}
	entry, err := store.Put(ctx, caller, normalized.Key, []byte(normalized.Value), opts...)
	if err != nil {
		return memory.Entry{}, &FactStoreError{
			Code:    FactErrBackend,
			Message: "store: " + err.Error(),
		}
	}
	return entry, nil
}
