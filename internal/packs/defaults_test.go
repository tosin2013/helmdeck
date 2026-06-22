package packs

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/tosin2013/helmdeck/internal/memory"
)

// TestBuildDefaults_RoundTrip seeds a memory store with audit rows,
// runs BuildDefaults, and verifies the projection: top-N pack ranking,
// most-common-input picking, success-only filter, per-caller scope.
func TestBuildDefaults_RoundTrip(t *testing.T) {
	store := memory.NewInMemoryStore()
	ctx := context.Background()
	caller := "alice"

	putAudit(t, store, caller, AuditKeyPrefixPack+"blog.rewrite_for_audience/0001",
		PackAudit{Pack: "blog.rewrite_for_audience", Outcome: "ok", AtUnix: 100,
			LearnInputs: map[string]string{"persona": "technical", "audience": "engineers"}})
	putAudit(t, store, caller, AuditKeyPrefixPack+"blog.rewrite_for_audience/0002",
		PackAudit{Pack: "blog.rewrite_for_audience", Outcome: "ok", AtUnix: 200,
			LearnInputs: map[string]string{"persona": "technical", "audience": "engineers"}})
	putAudit(t, store, caller, AuditKeyPrefixPack+"blog.rewrite_for_audience/0003",
		PackAudit{Pack: "blog.rewrite_for_audience", Outcome: "ok", AtUnix: 300,
			LearnInputs: map[string]string{"persona": "marketing"}})
	putAudit(t, store, caller, AuditKeyPrefixPack+"blog.rewrite_for_audience/0004",
		PackAudit{Pack: "blog.rewrite_for_audience", Outcome: "invalid_input", AtUnix: 400,
			LearnInputs: map[string]string{"persona": "executive"}})
	putAudit(t, store, caller, AuditKeyPrefixPack+"slides.outline/0001",
		PackAudit{Pack: "slides.outline", Outcome: "ok", AtUnix: 500,
			LearnInputs: map[string]string{"persona": "marketing"}})
	putPipelineAudit(t, store, caller, AuditKeyPrefixPipeline+"builtin.brief-rewrite-blog/r1",
		PipelineAudit{Pipeline: "builtin.brief-rewrite-blog", Outcome: "succeeded", AtUnix: 1000,
			LearnInputs: map[string]string{"persona": "technical"}})

	def, err := BuildDefaults(ctx, store, caller)
	if err != nil {
		t.Fatalf("BuildDefaults: %v", err)
	}
	if len(def.Packs) != 2 {
		t.Fatalf("want 2 packs, got %d", len(def.Packs))
	}
	if def.Packs[0].ID != "blog.rewrite_for_audience" {
		t.Errorf("want blog.rewrite_for_audience ranked first; got %q", def.Packs[0].ID)
	}
	if def.Packs[0].Calls != 3 {
		t.Errorf("want 3 successful calls (failure excluded), got %d", def.Packs[0].Calls)
	}
	if def.Packs[0].CommonInputs["persona"] != "technical" {
		t.Errorf("want persona=technical (2 of 3), got %q", def.Packs[0].CommonInputs["persona"])
	}
	if def.Packs[0].CommonInputs["audience"] != "engineers" {
		t.Errorf("want audience=engineers, got %q", def.Packs[0].CommonInputs["audience"])
	}
	if len(def.Pipelines) != 1 {
		t.Fatalf("want 1 pipeline, got %d", len(def.Pipelines))
	}
	if def.Pipelines[0].ID != "builtin.brief-rewrite-blog" {
		t.Errorf("want brief-rewrite-blog pipeline, got %q", def.Pipelines[0].ID)
	}
}

func TestBuildDefaults_NilStore(t *testing.T) {
	def, err := BuildDefaults(context.Background(), nil, "alice")
	if err != nil {
		t.Fatalf("BuildDefaults(nil): %v", err)
	}
	if len(def.Packs) != 0 || len(def.Pipelines) != 0 {
		t.Errorf("nil store should yield empty projection; got %+v", def)
	}
}

func TestBuildDefaults_EmptyHistory(t *testing.T) {
	store := memory.NewInMemoryStore()
	def, err := BuildDefaults(context.Background(), store, "fresh-user")
	if err != nil {
		t.Fatalf("BuildDefaults: %v", err)
	}
	if len(def.Packs) != 0 || len(def.Pipelines) != 0 {
		t.Errorf("empty history should yield empty projection; got %+v", def)
	}
}

func putAudit(t *testing.T, s memory.MemoryStore, ns, key string, a PackAudit) {
	t.Helper()
	body, _ := json.Marshal(a)
	if _, err := s.Put(context.Background(), ns, key, body, memory.WithCategory(AuditCategoryPack)); err != nil {
		t.Fatal(err)
	}
}

func putPipelineAudit(t *testing.T, s memory.MemoryStore, ns, key string, a PipelineAudit) {
	t.Helper()
	body, _ := json.Marshal(a)
	if _, err := s.Put(context.Background(), ns, key, body, memory.WithCategory(AuditCategoryPipeline)); err != nil {
		t.Fatal(err)
	}
}

// TestProjectCommonFindings covers the findings aggregation (#570 slice 2).
// Verifies the count, last_seen, and severity attribution under
// realistic empirical data (multiple lint runs, each surfacing a
// mix of codes).
func TestProjectCommonFindings(t *testing.T) {
	audits := []PackAudit{
		// First lint run
		{Pack: "hyperframes.lint", Outcome: "handler_failed", AtUnix: 1000, Findings: []AuditFinding{
			{Code: "missing_local_asset", Severity: "error"},
			{Code: "gsap_studio_edit_blocked", Severity: "warning"},
			{Code: "timeline_track_too_dense", Severity: "warning"},
		}},
		// Second lint run — same agent, similar findings
		{Pack: "hyperframes.lint", Outcome: "handler_failed", AtUnix: 2000, Findings: []AuditFinding{
			{Code: "missing_local_asset", Severity: "error"},
			{Code: "gsap_studio_edit_blocked", Severity: "warning"},
		}},
		// Third lint run — narrower; only one repeat
		{Pack: "hyperframes.lint", Outcome: "ok", AtUnix: 3000, Findings: []AuditFinding{
			{Code: "missing_local_asset", Severity: "error"},
		}},
	}
	got := projectCommonFindings(audits)
	if len(got) != 3 {
		t.Fatalf("expected 3 distinct codes, got %d: %+v", len(got), got)
	}
	// Sorted by occurrence_count desc:
	//   missing_local_asset       = 3
	//   gsap_studio_edit_blocked  = 2
	//   timeline_track_too_dense  = 1
	if got[0].Code != "missing_local_asset" || got[0].OccurrenceCount != 3 {
		t.Errorf("expected missing_local_asset=3 first, got %+v", got[0])
	}
	if got[1].Code != "gsap_studio_edit_blocked" || got[1].OccurrenceCount != 2 {
		t.Errorf("expected gsap_studio_edit_blocked=2 second, got %+v", got[1])
	}
	if got[2].Code != "timeline_track_too_dense" || got[2].OccurrenceCount != 1 {
		t.Errorf("expected timeline_track_too_dense=1 third, got %+v", got[2])
	}
	// LastSeenUnix should be the most-recent occurrence (t=3000 for
	// missing_local_asset, t=2000 for the others).
	if got[0].LastSeenUnix != 3000 {
		t.Errorf("missing_local_asset last_seen = %d, want 3000", got[0].LastSeenUnix)
	}
	if got[1].LastSeenUnix != 2000 {
		t.Errorf("gsap_studio_edit_blocked last_seen = %d, want 2000", got[1].LastSeenUnix)
	}
	// Pack + severity attributed to the most-recent occurrence.
	for _, f := range got {
		if f.Pack != "hyperframes.lint" {
			t.Errorf("code %q: pack = %q, want hyperframes.lint", f.Code, f.Pack)
		}
	}
}

// TestProjectCommonFindings_AggregatesAcrossPacks — a single code can
// appear from multiple packs (rare but possible: lint + inspect both
// flag something). The most-recent occurrence wins for pack attribution.
func TestProjectCommonFindings_AggregatesAcrossPacks(t *testing.T) {
	audits := []PackAudit{
		{Pack: "hyperframes.lint", AtUnix: 1000, Findings: []AuditFinding{
			{Code: "shared_code", Severity: "warning"},
		}},
		{Pack: "hyperframes.inspect", AtUnix: 2000, Findings: []AuditFinding{
			{Code: "shared_code", Severity: "error"},
		}},
	}
	got := projectCommonFindings(audits)
	if len(got) != 1 {
		t.Fatalf("expected 1 aggregated finding, got %d", len(got))
	}
	if got[0].OccurrenceCount != 2 {
		t.Errorf("expected count=2, got %d", got[0].OccurrenceCount)
	}
	// Most-recent: inspect wins pack + severity attribution.
	if got[0].Pack != "hyperframes.inspect" {
		t.Errorf("expected pack=hyperframes.inspect (most recent), got %q", got[0].Pack)
	}
	if got[0].Severity != "error" {
		t.Errorf("expected severity=error (most recent), got %q", got[0].Severity)
	}
}

// TestProjectCommonFindings_EmptyReturnsEmpty — no findings → no rows.
func TestProjectCommonFindings_EmptyReturnsEmpty(t *testing.T) {
	got := projectCommonFindings([]PackAudit{
		{Pack: "a", AtUnix: 1, Findings: nil},
		{Pack: "b", AtUnix: 2}, // omitted Findings
	})
	if len(got) != 0 {
		t.Errorf("expected empty, got %+v", got)
	}
}

// TestProjectCommonFindings_CapsAtTopN — long tail truncated.
func TestProjectCommonFindings_CapsAtTopN(t *testing.T) {
	var findings []AuditFinding
	for i := 0; i < DefaultsFindingsTopN+5; i++ {
		findings = append(findings, AuditFinding{
			Code:     "code_" + string(rune('A'+i)),
			Severity: "warning",
		})
	}
	got := projectCommonFindings([]PackAudit{
		{Pack: "test", AtUnix: 1, Findings: findings},
	})
	if len(got) != DefaultsFindingsTopN {
		t.Errorf("expected cap at %d, got %d", DefaultsFindingsTopN, len(got))
	}
}
