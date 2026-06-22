// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// hyperframes_compose_findings_test.go — #570 slice 4 tests.
// Verifies the findings-prefix injection on hyperframes.compose's
// system prompt: empty memory → no prefix; memory with audits → prefix
// listing the top-N common findings appended to the existing prompt.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/tosin2013/helmdeck/internal/memory"
	"github.com/tosin2013/helmdeck/internal/packs"
)

// findingsTestMemory is a MemoryInterface that returns pre-seeded
// PackAudit entries on List(AuditKeyPrefixPack), simulating prior
// pack-history rows. Other methods are no-ops since the prefix
// builder only reads via List.
type findingsTestMemory struct {
	audits []packs.PackAudit
}

func (m *findingsTestMemory) Store(string, []byte, ...memory.PutOption) error { return nil }
func (m *findingsTestMemory) Recall(string) (*memory.Entry, error)            { return nil, nil }
func (m *findingsTestMemory) Delete(string) error                             { return nil }
func (m *findingsTestMemory) Namespace() string                               { return "test-caller" }
func (m *findingsTestMemory) Context() (*packs.SessionContext, error)         { return nil, nil }

func (m *findingsTestMemory) List(prefix string) ([]memory.Entry, error) {
	if prefix != packs.AuditKeyPrefixPack {
		return nil, nil
	}
	out := make([]memory.Entry, 0, len(m.audits))
	now := time.Now()
	for i, a := range m.audits {
		body, _ := json.Marshal(a)
		out = append(out, memory.Entry{
			Key:       prefix + a.Pack + "/" + jsonIntFindings(int64(i)),
			Value:     body,
			Category:  packs.AuditCategoryPack,
			CreatedAt: now,
		})
	}
	return out, nil
}

func jsonIntFindings(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// runComposeWithMemory is runCompose's findings-test twin — wires
// a custom MemoryInterface that returns pre-seeded audit rows.
func runComposeWithMemory(t *testing.T, disp *scriptedDispatcherWT, mem packs.MemoryInterface, input string) (json.RawMessage, error) {
	t.Helper()
	pack := HyperframesCompose(disp)
	ec := &packs.ExecutionContext{
		Pack:   pack,
		Input:  json.RawMessage(input),
		Memory: mem,
	}
	return pack.Handler(context.Background(), ec)
}

// systemPromptFromCaptured pulls the system message's text out of a
// scripted dispatcher's first captured request. Returns "" if no
// captured calls or no system message.
func systemPromptFromCaptured(d *scriptedDispatcherWT) string {
	if len(d.captured) == 0 {
		return ""
	}
	for _, m := range d.captured[0].Messages {
		if m.Role == "system" {
			return m.Content.Text()
		}
	}
	return ""
}

// --- empty memory → no prefix ---------------------------------------------

func TestComposeFindingsPrefix_EmptyMemory_NoPrefix(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{goodSpec}}
	mem := &findingsTestMemory{audits: nil}
	if _, err := runComposeWithMemory(t, disp, mem, `{"description":"x","model":"openrouter/auto"}`); err != nil {
		t.Fatalf("handler: %v", err)
	}
	sys := systemPromptFromCaptured(disp)
	if strings.Contains(sys, "FINDINGS FROM YOUR PRIOR RUNS") {
		t.Errorf("empty memory should produce no findings prefix; system prompt contains it:\n%s", sys)
	}
}

func TestComposeFindingsPrefix_NilMemory_NoPrefix(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{goodSpec}}
	// No Memory wired at all — most common production setup when
	// HELMDECK_MEMORY_KEY is unset.
	if _, err := runCompose(t, disp, `{"description":"x","model":"openrouter/auto"}`); err != nil {
		t.Fatalf("handler: %v", err)
	}
	sys := systemPromptFromCaptured(disp)
	if strings.Contains(sys, "FINDINGS FROM YOUR PRIOR RUNS") {
		t.Errorf("nil memory should produce no findings prefix; system prompt contains it:\n%s", sys)
	}
}

// --- audits without findings → no prefix ----------------------------------

func TestComposeFindingsPrefix_AuditsWithoutFindings_NoPrefix(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{goodSpec}}
	mem := &findingsTestMemory{audits: []packs.PackAudit{
		{Pack: "hyperframes.compose", AtUnix: 1, Outcome: "ok"},
		{Pack: "hyperframes.render", AtUnix: 2, Outcome: "ok"},
	}}
	if _, err := runComposeWithMemory(t, disp, mem, `{"description":"x","model":"openrouter/auto"}`); err != nil {
		t.Fatalf("handler: %v", err)
	}
	sys := systemPromptFromCaptured(disp)
	if strings.Contains(sys, "FINDINGS FROM YOUR PRIOR RUNS") {
		t.Errorf("audits without findings should produce no prefix; got:\n%s", sys)
	}
}

// --- audits with findings → prefix appears with codes ---------------------

func TestComposeFindingsPrefix_WithFindings_AppendsPrefix(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{goodSpec}}
	// Simulate the empirical findings from the 2026-06-22 BYO test.
	mem := &findingsTestMemory{audits: []packs.PackAudit{
		{Pack: "hyperframes.lint", AtUnix: 1, Outcome: "handler_failed", Findings: []packs.AuditFinding{
			{Code: "missing_local_asset", Severity: "error"},
			{Code: "gsap_studio_edit_blocked", Severity: "warning"},
		}},
		{Pack: "hyperframes.lint", AtUnix: 2, Outcome: "handler_failed", Findings: []packs.AuditFinding{
			{Code: "missing_local_asset", Severity: "error"}, // repeat → count goes up
		}},
	}}
	if _, err := runComposeWithMemory(t, disp, mem, `{"description":"x","model":"openrouter/auto"}`); err != nil {
		t.Fatalf("handler: %v", err)
	}
	sys := systemPromptFromCaptured(disp)
	if !strings.Contains(sys, "FINDINGS FROM YOUR PRIOR RUNS") {
		t.Errorf("expected findings header in system prompt; got:\n%s", sys)
	}
	if !strings.Contains(sys, "missing_local_asset") {
		t.Errorf("expected most-frequent finding 'missing_local_asset' in prompt; got:\n%s", sys)
	}
	if !strings.Contains(sys, "seen 2 time") {
		t.Errorf("expected occurrence count 'seen 2 time' for missing_local_asset; got:\n%s", sys)
	}
	if !strings.Contains(sys, "gsap_studio_edit_blocked") {
		t.Errorf("expected secondary finding 'gsap_studio_edit_blocked' in prompt; got:\n%s", sys)
	}
}

// --- prefix shape: rules-style closing constraint -------------------------

func TestComposeFindingsPrefix_HasHardConstraintClose(t *testing.T) {
	disp := &scriptedDispatcherWT{replies: []string{goodSpec}}
	mem := &findingsTestMemory{audits: []packs.PackAudit{
		{Pack: "hyperframes.lint", AtUnix: 1, Findings: []packs.AuditFinding{
			{Code: "media_missing_id", Severity: "error"},
		}},
	}}
	if _, err := runComposeWithMemory(t, disp, mem, `{"description":"x","model":"openrouter/auto"}`); err != nil {
		t.Fatalf("handler: %v", err)
	}
	sys := systemPromptFromCaptured(disp)
	// The closing line tells the LLM these are hard constraints, not
	// just warnings — load-bearing for the prompt's interpretability.
	if !strings.Contains(sys, "Do not produce HTML, CSS, or JS that would trigger any of the codes above") {
		t.Errorf("expected hard-constraint closing line; got:\n%s", sys)
	}
}

// --- bounded prefix size --------------------------------------------------

func TestComposeFindingsPrefix_CapsAtTopN(t *testing.T) {
	// Seed many more than composeFindingsTopN distinct codes; only
	// the top N should appear in the prompt.
	findings := make([]packs.AuditFinding, 0, composeFindingsTopN+5)
	for i := 0; i < composeFindingsTopN+5; i++ {
		findings = append(findings, packs.AuditFinding{
			Code:     "code_" + string(rune('a'+i)),
			Severity: "warning",
		})
	}
	disp := &scriptedDispatcherWT{replies: []string{goodSpec}}
	mem := &findingsTestMemory{audits: []packs.PackAudit{
		{Pack: "hyperframes.lint", AtUnix: 1, Findings: findings},
	}}
	if _, err := runComposeWithMemory(t, disp, mem, `{"description":"x","model":"openrouter/auto"}`); err != nil {
		t.Fatalf("handler: %v", err)
	}
	sys := systemPromptFromCaptured(disp)
	// Count occurrences of "code_" — each finding appears once.
	count := strings.Count(sys, "code_")
	if count != composeFindingsTopN {
		t.Errorf("expected exactly %d code_* mentions (cap), got %d in:\n%s",
			composeFindingsTopN, count, sys)
	}
}
