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

// TestAuditFindings_ErrorPathStillCapturesFindings — regression for
// the empirical 2026-06-22 BYO bug: hyperframes.lint in strict mode
// returns BOTH the findings JSON AND a CodeArtifactFailed PackError.
// The engine's pre-fix code read result.Output (nil on the error
// short-circuit) so the findings never landed in the audit row.
// Post-fix: handlerOutput captured RIGHT AFTER safeInvoke survives
// the error, audit closure passes it to extractFindings, audit row
// carries Findings.
//
// Load-bearing for #570 — without this, the findings-memory loop
// never closes on Tier C runs because lint-strict failures DON'T
// produce audit-row findings.
func TestAuditFindings_ErrorPathStillCapturesFindings(t *testing.T) {
	store := memory.NewInMemoryStore()
	eng := quietEngine(WithMemoryStore(store))
	pack := &Pack{
		Name: "lint.like", Version: "v1",
		Handler: func(ctx context.Context, ec *ExecutionContext) (json.RawMessage, error) {
			// Simulate the hyperframes.lint strict-mode pattern:
			// return findings JSON in the output AND a PackError.
			output := json.RawMessage(`{"lint":{"ok":false,"error_count":1,"findings":[
				{"code":"missing_local_asset","severity":"error","file":"/tmp/x/index.html"},
				{"code":"gsap_studio_edit_blocked","severity":"warning"}
			]}}`)
			return output, &PackError{Code: CodeArtifactFailed,
				Message: "1 error-severity finding(s) in strict mode"}
		},
	}
	ctx := WithCaller(context.Background(), "alice")
	_, err := eng.Execute(ctx, pack, json.RawMessage(`{}`))
	if err == nil {
		t.Fatalf("expected handler error to surface; got nil")
	}

	// Read the audit row alice wrote. Must contain the findings even
	// though the handler errored.
	entries, _ := store.List(context.Background(), "alice", AuditKeyPrefixPack)
	if len(entries) != 1 {
		t.Fatalf("expected 1 audit row, got %d", len(entries))
	}
	var audit PackAudit
	if err := json.Unmarshal(entries[0].Value, &audit); err != nil {
		t.Fatalf("audit decode: %v", err)
	}
	if audit.Outcome != string(CodeArtifactFailed) {
		t.Errorf("outcome = %q, want %q (the typed PackError code)",
			audit.Outcome, CodeArtifactFailed)
	}
	if len(audit.Findings) != 2 {
		t.Fatalf("expected 2 findings extracted on error path; got %d (PRE-FIX BEHAVIOR: this was 0): %+v",
			len(audit.Findings), audit.Findings)
	}
	if audit.Findings[0].Code != "missing_local_asset" {
		t.Errorf("first finding code = %q, want missing_local_asset",
			audit.Findings[0].Code)
	}
	if audit.Findings[1].Code != "gsap_studio_edit_blocked" {
		t.Errorf("second finding code = %q, want gsap_studio_edit_blocked",
			audit.Findings[1].Code)
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

// TestExtractFindings covers the findings extractor (#570 slice 1).
// Recognized shapes: top-level findings array + nested lint/inspect/
// validate wrappers.
func TestExtractFindings(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		wantCodes []string
	}{
		{"empty", ``, nil},
		{"no-findings", `{"composition_html":"<html/>"}`, nil},
		{"top-level-array",
			`{"findings":[{"code":"media_missing_id","severity":"error"}]}`,
			[]string{"media_missing_id"}},
		{"lint-wrapper",
			`{"lint":{"ok":false,"findings":[
				{"code":"media_missing_id","severity":"error","file":"/x/index.html"},
				{"code":"google_fonts_import","severity":"error"}
			]}}`,
			[]string{"media_missing_id", "google_fonts_import"}},
		{"inspect-wrapper-uses-issues-key",
			`{"inspect":{"issues":[
				{"code":"text_box_overflow","severity":"error","time":12.5}
			]}}`,
			[]string{"text_box_overflow"}},
		{"validate-wrapper-uses-errors-warnings",
			`{"validate":{"errors":[
				{"level":"error","text":"CORS blocked","code":"console_error"}
			],"warnings":[
				{"level":"warning","text":"deprecated","code":"console_warning"}
			]}}`,
			[]string{"console_error", "console_warning"}},
		{"skip-entries-without-code",
			`{"findings":[
				{"code":"real_code","severity":"error"},
				{"severity":"error"}
			]}`,
			[]string{"real_code"}},
		{"bad-json-returns-nil", `{not json}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractFindings(json.RawMessage(tc.in))
			if len(got) != len(tc.wantCodes) {
				t.Fatalf("len = %d, want %d; got=%+v", len(got), len(tc.wantCodes), got)
			}
			for i, code := range tc.wantCodes {
				if got[i].Code != code {
					t.Errorf("entry %d code = %q, want %q", i, got[i].Code, code)
				}
			}
		})
	}
}

// TestExtractFindings_CapsAtMax — load-bearing for the row-size
// bound. An inspect-style output with 500 issues should produce
// exactly maxAuditFindings entries.
func TestExtractFindings_CapsAtMax(t *testing.T) {
	var entries []string
	for i := 0; i < 500; i++ {
		entries = append(entries, `{"code":"text_box_overflow","severity":"warning"}`)
	}
	raw := `{"inspect":{"issues":[` + joinStr(entries, ",") + `]}}`
	got := extractFindings(json.RawMessage(raw))
	if len(got) != maxAuditFindings {
		t.Errorf("expected cap at %d, got %d", maxAuditFindings, len(got))
	}
}

func joinStr(s []string, sep string) string {
	if len(s) == 0 {
		return ""
	}
	out := s[0]
	for _, x := range s[1:] {
		out += sep + x
	}
	return out
}
