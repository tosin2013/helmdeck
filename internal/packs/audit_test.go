package packs

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/tosin2013/helmdeck/internal/memory"
)

// TestAuditWritesOnSuccess proves Engine.Execute emits one pack_history
// audit row under the caller's bare namespace on a successful run,
// containing the learnable input fields and outcome="ok".
func TestAuditWritesOnSuccess(t *testing.T) {
	store := memory.NewInMemoryStore()
	eng := quietEngine(WithMemoryStore(store))

	pack := &Pack{
		Name: "audit.probe", Version: "v1",
		Handler: func(ctx context.Context, ec *ExecutionContext) (json.RawMessage, error) {
			return json.RawMessage(`{"ok":true}`), nil
		},
	}
	ctx := WithCaller(context.Background(), "alice")
	in := json.RawMessage(`{"persona":"technical","audience":"platform engineers","markdown":"# big body should be dropped"}`)
	if _, err := eng.Execute(ctx, pack, in); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	entries, err := store.List(context.Background(), "alice", AuditKeyPrefixPack)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 audit row, got %d", len(entries))
	}
	var a PackAudit
	if err := json.Unmarshal(entries[0].Value, &a); err != nil {
		t.Fatalf("unmarshal audit: %v", err)
	}
	if a.Pack != "audit.probe" {
		t.Errorf("want pack=audit.probe, got %q", a.Pack)
	}
	if a.Outcome != "ok" {
		t.Errorf("want outcome=ok, got %q", a.Outcome)
	}
	if a.LearnInputs["persona"] != "technical" {
		t.Errorf("missing persona in learn_inputs: %+v", a.LearnInputs)
	}
	if a.LearnInputs["audience"] != "platform engineers" {
		t.Errorf("missing audience in learn_inputs: %+v", a.LearnInputs)
	}
	if _, leaked := a.LearnInputs["markdown"]; leaked {
		t.Errorf("markdown body leaked into learn_inputs (should be dropped): %+v", a.LearnInputs)
	}
	if entries[0].Category != AuditCategoryPack {
		t.Errorf("want category=%s, got %q", AuditCategoryPack, entries[0].Category)
	}
}

// TestAuditWritesOnError proves a handler failure still produces an
// audit row, with outcome set to the PackError code so projection can
// distinguish success from caller_fixable history.
func TestAuditWritesOnError(t *testing.T) {
	store := memory.NewInMemoryStore()
	eng := quietEngine(WithMemoryStore(store))
	pack := &Pack{
		Name: "audit.fail", Version: "v1",
		Handler: func(ctx context.Context, ec *ExecutionContext) (json.RawMessage, error) {
			return nil, &PackError{Code: CodeInvalidInput, Message: "boom"}
		},
	}
	ctx := WithCaller(context.Background(), "bob")
	_, err := eng.Execute(ctx, pack, json.RawMessage(`{"persona":"marketing"}`))
	if err == nil {
		t.Fatal("expected error")
	}
	entries, _ := store.List(context.Background(), "bob", AuditKeyPrefixPack)
	if len(entries) != 1 {
		t.Fatalf("want 1 audit row, got %d", len(entries))
	}
	var a PackAudit
	_ = json.Unmarshal(entries[0].Value, &a)
	if a.Outcome != string(CodeInvalidInput) {
		t.Errorf("want outcome=%s, got %q", CodeInvalidInput, a.Outcome)
	}
}

// TestAuditPerCallerIsolated proves caller A's audit history never
// surfaces in caller B's namespace — the cross-session learning seam
// is also the cross-tenant isolation seam.
func TestAuditPerCallerIsolated(t *testing.T) {
	store := memory.NewInMemoryStore()
	eng := quietEngine(WithMemoryStore(store))
	pack := &Pack{
		Name: "audit.probe", Version: "v1",
		Handler: func(ctx context.Context, ec *ExecutionContext) (json.RawMessage, error) {
			return json.RawMessage(`{}`), nil
		},
	}
	for _, who := range []string{"alice", "alice", "bob"} {
		ctx := WithCaller(context.Background(), who)
		if _, err := eng.Execute(ctx, pack, json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	alice, _ := store.List(context.Background(), "alice", AuditKeyPrefixPack)
	bob, _ := store.List(context.Background(), "bob", AuditKeyPrefixPack)
	if len(alice) != 2 || len(bob) != 1 {
		t.Fatalf("isolation broken — alice=%d (want 2), bob=%d (want 1)", len(alice), len(bob))
	}
}

// TestAuditWithoutMemoryIsNoop proves Execute runs unchanged when no
// memory store is wired (PR #2 must not regress memory-disabled
// deployments).
func TestAuditWithoutMemoryIsNoop(t *testing.T) {
	eng := quietEngine()
	pack := &Pack{
		Name: "audit.probe", Version: "v1",
		Handler: func(ctx context.Context, ec *ExecutionContext) (json.RawMessage, error) {
			return json.RawMessage(`{}`), nil
		},
	}
	ctx := WithCaller(context.Background(), "alice")
	if _, err := eng.Execute(ctx, pack, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

// TestExtractLearnableInputs covers the closed-set filter directly.
func TestExtractLearnableInputs(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want map[string]string
	}{
		{"empty", `{}`, nil},
		{"only-droppable", `{"markdown":"hello","url":"x"}`, nil},
		{"mix", `{"persona":"technical","markdown":"# drop","model":"openrouter/auto","audience":""}`,
			map[string]string{"persona": "technical", "model": "openrouter/auto"}},
		{"non-string-dropped", `{"persona":"technical","max_tokens":500}`,
			map[string]string{"persona": "technical"}},
		{"bad-json", `{not json}`, nil},
		{"null", ``, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractLearnableInputs(json.RawMessage(tc.in))
			if len(got) != len(tc.want) {
				t.Fatalf("want %v, got %v", tc.want, got)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("field %q: want %q, got %q", k, v, got[k])
				}
			}
		})
	}
}
