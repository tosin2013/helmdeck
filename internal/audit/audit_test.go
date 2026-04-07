package audit_test

import (
	"context"
	"testing"
	"time"

	"github.com/tosin2013/helmdeck/internal/audit"
	"github.com/tosin2013/helmdeck/internal/store"
)

func newWriter(t *testing.T) *audit.SQLiteWriter {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return audit.NewSQLiteWriter(db)
}

func TestWriteAndQuery(t *testing.T) {
	w := newWriter(t)
	ctx := context.Background()

	for i, sub := range []string{"alice", "alice", "bob"} {
		err := w.Write(ctx, audit.Entry{
			Severity:     audit.SeverityInfo,
			EventType:    audit.EventAPIRequest,
			ActorSubject: sub,
			ActorClient:  "claude-code",
			Method:       "GET",
			Path:         "/api/v1/sessions",
			StatusCode:   200,
			Payload: map[string]any{
				"index":         i,
				"authorization": "Bearer secret-must-be-redacted",
				"nested": map[string]any{
					"api_key": "shh",
					"safe":    "ok",
				},
			},
		})
		if err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}

	all, err := w.Query(ctx, audit.Filter{})
	if err != nil {
		t.Fatalf("Query all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("query all len = %d, want 3", len(all))
	}

	// Most recent first.
	if all[0].ActorSubject != "bob" {
		t.Errorf("first row actor = %q, want bob", all[0].ActorSubject)
	}

	// Redaction
	if got := all[0].Payload["authorization"]; got != "[redacted]" {
		t.Errorf("authorization not redacted: got %v", got)
	}
	if nested, ok := all[0].Payload["nested"].(map[string]any); ok {
		if nested["api_key"] != "[redacted]" {
			t.Errorf("nested api_key not redacted: got %v", nested["api_key"])
		}
		if nested["safe"] != "ok" {
			t.Errorf("safe field clobbered: got %v", nested["safe"])
		}
	} else {
		t.Errorf("nested payload missing or wrong type")
	}

	// Filter by actor.
	alice, err := w.Query(ctx, audit.Filter{ActorSubject: "alice"})
	if err != nil {
		t.Fatalf("Query alice: %v", err)
	}
	if len(alice) != 2 {
		t.Errorf("alice len = %d, want 2", len(alice))
	}

	// Filter by event type.
	apiOnly, err := w.Query(ctx, audit.Filter{EventType: audit.EventAPIRequest})
	if err != nil {
		t.Fatalf("Query api_request: %v", err)
	}
	if len(apiOnly) != 3 {
		t.Errorf("api_request len = %d, want 3", len(apiOnly))
	}

	// Limit
	limited, err := w.Query(ctx, audit.Filter{Limit: 1})
	if err != nil {
		t.Fatalf("Query limit: %v", err)
	}
	if len(limited) != 1 {
		t.Errorf("limit len = %d, want 1", len(limited))
	}
}

func TestTimeFilter(t *testing.T) {
	w := newWriter(t)
	ctx := context.Background()

	now := time.Now().UTC()
	old := audit.Entry{Timestamp: now.Add(-time.Hour), Severity: audit.SeverityInfo, EventType: audit.EventLogin, StatusCode: 200}
	rec := audit.Entry{Timestamp: now, Severity: audit.SeverityInfo, EventType: audit.EventLogin, StatusCode: 200}
	if err := w.Write(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := w.Write(ctx, rec); err != nil {
		t.Fatal(err)
	}

	got, err := w.Query(ctx, audit.Filter{From: now.Add(-30 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 entry after 30m ago, got %d", len(got))
	}
}

func TestDiscardWriter(t *testing.T) {
	var w audit.Writer = audit.Discard{}
	if err := w.Write(context.Background(), audit.Entry{}); err != nil {
		t.Fatalf("Discard.Write: %v", err)
	}
	got, err := w.Query(context.Background(), audit.Filter{})
	if err != nil {
		t.Fatalf("Discard.Query: %v", err)
	}
	if got != nil {
		t.Fatalf("Discard.Query = %v, want nil", got)
	}
}
