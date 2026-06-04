// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/tosin2013/helmdeck/internal/packs"
)

// pipelines_test.go (PR G of the v0.25.0 reliability arc) covers the
// MCP pipelines tool surface — the 7 helmdeck__pipeline-* tools that
// every connected agent (OpenClaw, Gemini CLI, Claude Code) uses to
// list / create / run pipelines conversationally. Before this PR
// pipelines.go was at 8% coverage; the production dispatch path —
// pipeline-run, pipeline-run-status, pipeline-cancel — was completely
// unexercised. A regression in the tool name → service-method
// mapping would silently break every agent's pipeline-execution
// workflow.

// stubPipelineService is a goroutine-safe in-memory PipelineService.
// Tests configure the canned responses; each dispatch records what
// the tool dispatcher called so we can assert on the contract.
type stubPipelineService struct {
	listResp   json.RawMessage
	listErr    error
	getResp    json.RawMessage
	getErr     error
	createResp json.RawMessage
	createErr  error

	startRunID     string
	startCoalesced bool
	startErr       error

	runStatusResp json.RawMessage
	runStatusErr  error

	rerunRunID     string
	rerunCoalesced bool
	rerunErr       error

	cancelErr error

	// Captured arguments for assertion.
	lastGetID       string
	lastCreateDef   json.RawMessage
	lastStartID     string
	lastStartInputs json.RawMessage
	lastRunStatusID string
	lastRerunID     string
	lastCancelID    string
}

func (s *stubPipelineService) List(_ context.Context) (json.RawMessage, error) {
	return s.listResp, s.listErr
}
func (s *stubPipelineService) Get(_ context.Context, id string) (json.RawMessage, error) {
	s.lastGetID = id
	return s.getResp, s.getErr
}
func (s *stubPipelineService) Create(_ context.Context, def json.RawMessage) (json.RawMessage, error) {
	s.lastCreateDef = def
	return s.createResp, s.createErr
}
func (s *stubPipelineService) StartRun(_ context.Context, id string, inputs json.RawMessage) (string, bool, error) {
	s.lastStartID = id
	s.lastStartInputs = inputs
	return s.startRunID, s.startCoalesced, s.startErr
}
func (s *stubPipelineService) RunStatus(_ context.Context, runID string) (json.RawMessage, error) {
	s.lastRunStatusID = runID
	return s.runStatusResp, s.runStatusErr
}
func (s *stubPipelineService) Rerun(_ context.Context, runID string) (string, bool, error) {
	s.lastRerunID = runID
	return s.rerunRunID, s.rerunCoalesced, s.rerunErr
}
func (s *stubPipelineService) Cancel(_ context.Context, runID string) error {
	s.lastCancelID = runID
	return s.cancelErr
}

func newPipelineSrv(t *testing.T, ps PipelineService) *PackServer {
	t.Helper()
	reg := packs.NewPackRegistry()
	return NewPackServer(reg, packs.New(), WithPipelines(ps))
}

// extractText pulls the text payload out of an MCP tool-result envelope.
// Tool results are `{"content":[{"type":"text","text":"..."}],"isError":bool}`.
func extractText(t *testing.T, result map[string]any) string {
	t.Helper()
	content, ok := result["content"].([]map[string]any)
	if !ok {
		t.Fatalf("result missing content slice: %+v", result)
	}
	if len(content) == 0 {
		t.Fatal("result.content empty")
	}
	text, _ := content[0]["text"].(string)
	return text
}

// TestWithPipelines_GatesToolList — when no PipelineService is wired,
// pipelineTools returns nil and dispatchPipelineTool reports
// (nil, false) so the dispatcher falls through to pack lookup. This
// is the load-bearing gate that lets compose deployments without
// pipelines still serve a working pack catalog.
func TestWithPipelines_GatesToolList(t *testing.T) {
	srv := NewPackServer(packs.NewPackRegistry(), packs.New())
	if got := srv.pipelineTools(); got != nil {
		t.Errorf("pipelineTools() with no service = %v; want nil", got)
	}
	_, handled := srv.dispatchPipelineTool(context.Background(), "pipeline-list", nil)
	if handled {
		t.Error("dispatchPipelineTool with no service should not claim to handle the call")
	}
}

// TestWithPipelines_WiredToolList — when a service is wired, all 7
// pipeline tools appear in the tool list with bare names (no
// helmdeck__ prefix). The bare-name contract is what the docstring's
// "namespacing clients double-prefix if we bake it in" comment
// guards against — if a future refactor bakes the prefix in, every
// MCP client that namespaces tools would silently break.
func TestWithPipelines_WiredToolList(t *testing.T) {
	srv := newPipelineSrv(t, &stubPipelineService{})
	tools := srv.pipelineTools()
	if len(tools) != 7 {
		t.Fatalf("pipelineTools() = %d; want 7", len(tools))
	}
	want := map[string]bool{
		"pipeline-list":       false,
		"pipeline-get":        false,
		"pipeline-create":     false,
		"pipeline-run":        false,
		"pipeline-run-status": false,
		"pipeline-rerun":      false,
		"pipeline-cancel":     false,
	}
	for _, tool := range tools {
		if _, ok := want[tool.Name]; !ok {
			t.Errorf("unexpected tool name %q (helmdeck__ prefix must NOT be baked in)", tool.Name)
		}
		want[tool.Name] = true
	}
	for name, found := range want {
		if !found {
			t.Errorf("tool %q missing from pipelineTools()", name)
		}
	}
}

// TestDispatchPipelineTool_List covers the simplest tool — no input,
// returns whatever the service emitted, marshaled into the tool-result
// envelope with isError=false.
func TestDispatchPipelineTool_List(t *testing.T) {
	ps := &stubPipelineService{listResp: json.RawMessage(`[{"id":"p1","name":"Test"}]`)}
	srv := newPipelineSrv(t, ps)

	result, handled := srv.dispatchPipelineTool(context.Background(), "pipeline-list", nil)
	if !handled {
		t.Fatal("pipeline-list not handled")
	}
	if result["isError"] != false {
		t.Errorf("isError = %v; want false", result["isError"])
	}
	if !strings.Contains(extractText(t, result), "p1") {
		t.Errorf("response body missing service payload: %s", extractText(t, result))
	}
}

// TestDispatchPipelineTool_List_ServiceError — service-layer errors
// route through jsonOrErr → errorToolResult("pipeline_error",...).
// The error code matters because the LLM's recovery key branches on
// it (transient vs caller-fixable).
func TestDispatchPipelineTool_List_ServiceError(t *testing.T) {
	ps := &stubPipelineService{listErr: errors.New("backend unreachable")}
	srv := newPipelineSrv(t, ps)
	result, _ := srv.dispatchPipelineTool(context.Background(), "pipeline-list", nil)
	if result["isError"] != true {
		t.Errorf("isError = %v; want true on service error", result["isError"])
	}
	if !strings.Contains(extractText(t, result), "backend unreachable") {
		t.Errorf("error text should pass through: %s", extractText(t, result))
	}
}

// TestDispatchPipelineTool_Get_RequiresID — missing/empty id reports
// invalid_input WITHOUT calling the service. The caller-fixable
// branch the LLM uses to correct its argument.
func TestDispatchPipelineTool_Get_RequiresID(t *testing.T) {
	ps := &stubPipelineService{}
	srv := newPipelineSrv(t, ps)

	cases := []string{`{}`, `{"id":""}`, `{not-json`}
	for _, body := range cases {
		t.Run(body, func(t *testing.T) {
			result, _ := srv.dispatchPipelineTool(context.Background(), "pipeline-get", json.RawMessage(body))
			if result["isError"] != true {
				t.Errorf("isError = %v; want true on missing id", result["isError"])
			}
			if !strings.Contains(extractText(t, result), "id is required") {
				t.Errorf("error text missing 'id is required': %s", extractText(t, result))
			}
			if ps.lastGetID != "" {
				t.Errorf("service was called with id=%q despite invalid input", ps.lastGetID)
			}
		})
	}
}

// TestDispatchPipelineTool_Get_PassesIDThrough.
func TestDispatchPipelineTool_Get_PassesIDThrough(t *testing.T) {
	ps := &stubPipelineService{getResp: json.RawMessage(`{"id":"p1","name":"Test"}`)}
	srv := newPipelineSrv(t, ps)
	result, _ := srv.dispatchPipelineTool(context.Background(), "pipeline-get",
		json.RawMessage(`{"id":"p1"}`))
	if result["isError"] != false {
		t.Errorf("isError = %v; want false", result["isError"])
	}
	if ps.lastGetID != "p1" {
		t.Errorf("service called with id=%q; want p1", ps.lastGetID)
	}
}

// TestDispatchPipelineTool_Create — pass-through of the raw definition.
// The service does the validation; the dispatcher just forwards the
// JSON. The contract test pins that the raw arguments reach Create
// unchanged so a future refactor doesn't accidentally re-marshal and
// lose fields the service expects.
func TestDispatchPipelineTool_Create(t *testing.T) {
	ps := &stubPipelineService{createResp: json.RawMessage(`{"id":"p_new","name":"X"}`)}
	srv := newPipelineSrv(t, ps)
	def := `{"name":"My Pipeline","steps":[{"id":"a","pack":"echo","input":{}}]}`
	result, _ := srv.dispatchPipelineTool(context.Background(), "pipeline-create", json.RawMessage(def))
	if result["isError"] != false {
		t.Errorf("isError = %v; want false", result["isError"])
	}
	if string(ps.lastCreateDef) != def {
		t.Errorf("Create called with %q; want %q", ps.lastCreateDef, def)
	}
}

// TestDispatchPipelineTool_Run_HappyPath — returns the run id + the
// coalesced flag the LLM's polling loop branches on. The pipeline-run
// docstring is explicit that `coalesced: true` is NOT an error; pin
// the response shape so a regression doesn't accidentally promote it.
func TestDispatchPipelineTool_Run_HappyPath(t *testing.T) {
	ps := &stubPipelineService{startRunID: "run_abc", startCoalesced: false}
	srv := newPipelineSrv(t, ps)
	result, _ := srv.dispatchPipelineTool(context.Background(), "pipeline-run",
		json.RawMessage(`{"id":"p1","inputs":{"k":"v"}}`))
	if result["isError"] != false {
		t.Errorf("isError = %v; want false", result["isError"])
	}
	text := extractText(t, result)
	for _, want := range []string{`"run_id":"run_abc"`, `"pipeline_id":"p1"`,
		`"status":"pending"`, `"coalesced":false`} {
		if !strings.Contains(text, want) {
			t.Errorf("response missing %q: %s", want, text)
		}
	}
	// Inputs reached the service unchanged.
	if !strings.Contains(string(ps.lastStartInputs), `"k":"v"`) {
		t.Errorf("inputs forwarded as %s; want {\"k\":\"v\"}", ps.lastStartInputs)
	}
}

// TestDispatchPipelineTool_Run_CoalescedFlag — single-flight: an
// identical in-flight run returns the existing run_id with
// coalesced=true. Critical: the LLM's recovery code treats this as
// success, not duplicate, per the docstring.
func TestDispatchPipelineTool_Run_CoalescedFlag(t *testing.T) {
	ps := &stubPipelineService{startRunID: "run_existing", startCoalesced: true}
	srv := newPipelineSrv(t, ps)
	result, _ := srv.dispatchPipelineTool(context.Background(), "pipeline-run",
		json.RawMessage(`{"id":"p1"}`))
	if result["isError"] != false {
		t.Errorf("isError = %v; want false (coalesced is NOT an error)", result["isError"])
	}
	text := extractText(t, result)
	if !strings.Contains(text, `"coalesced":true`) {
		t.Errorf("response missing coalesced:true: %s", text)
	}
	if !strings.Contains(text, `"run_id":"run_existing"`) {
		t.Errorf("response missing existing run id: %s", text)
	}
}

// TestDispatchPipelineTool_Run_ServiceError — StartRun error → typed
// error with pipeline_run_failed code (not pipeline_error — the
// codes route differently in the LLM's recovery logic).
func TestDispatchPipelineTool_Run_ServiceError(t *testing.T) {
	ps := &stubPipelineService{startErr: errors.New("pipeline not found")}
	srv := newPipelineSrv(t, ps)
	result, _ := srv.dispatchPipelineTool(context.Background(), "pipeline-run",
		json.RawMessage(`{"id":"p1"}`))
	if result["isError"] != true {
		t.Errorf("isError = %v; want true", result["isError"])
	}
	if !strings.Contains(extractText(t, result), "pipeline not found") {
		t.Errorf("error message should surface service error: %s", extractText(t, result))
	}
}

// TestDispatchPipelineTool_RunStatus_HappyPath — polling endpoint.
// Service-returned JSON is passed through.
func TestDispatchPipelineTool_RunStatus_HappyPath(t *testing.T) {
	ps := &stubPipelineService{runStatusResp: json.RawMessage(`{"status":"running","steps":[]}`)}
	srv := newPipelineSrv(t, ps)
	result, _ := srv.dispatchPipelineTool(context.Background(), "pipeline-run-status",
		json.RawMessage(`{"run_id":"run_x"}`))
	if result["isError"] != false {
		t.Errorf("isError = %v; want false", result["isError"])
	}
	if ps.lastRunStatusID != "run_x" {
		t.Errorf("RunStatus called with %q", ps.lastRunStatusID)
	}
	if !strings.Contains(extractText(t, result), "running") {
		t.Errorf("response missing status: %s", extractText(t, result))
	}
}

// TestDispatchPipelineTool_Rerun_HappyPath — returns new run id +
// coalesced flag; mirrors pipeline-run's shape.
func TestDispatchPipelineTool_Rerun_HappyPath(t *testing.T) {
	ps := &stubPipelineService{rerunRunID: "run_retry", rerunCoalesced: false}
	srv := newPipelineSrv(t, ps)
	result, _ := srv.dispatchPipelineTool(context.Background(), "pipeline-rerun",
		json.RawMessage(`{"run_id":"run_failed"}`))
	if result["isError"] != false {
		t.Errorf("isError = %v; want false", result["isError"])
	}
	if ps.lastRerunID != "run_failed" {
		t.Errorf("Rerun called with %q", ps.lastRerunID)
	}
	text := extractText(t, result)
	if !strings.Contains(text, `"run_id":"run_retry"`) {
		t.Errorf("response missing new run id: %s", text)
	}
	if !strings.Contains(text, `"status":"pending"`) {
		t.Errorf("response missing pending status: %s", text)
	}
}

// TestDispatchPipelineTool_Cancel_HappyPath — cancelled status echoed
// back; no service-level body needed (Cancel returns error-or-nil only).
func TestDispatchPipelineTool_Cancel_HappyPath(t *testing.T) {
	ps := &stubPipelineService{}
	srv := newPipelineSrv(t, ps)
	result, _ := srv.dispatchPipelineTool(context.Background(), "pipeline-cancel",
		json.RawMessage(`{"run_id":"run_x"}`))
	if result["isError"] != false {
		t.Errorf("isError = %v; want false", result["isError"])
	}
	if ps.lastCancelID != "run_x" {
		t.Errorf("Cancel called with %q", ps.lastCancelID)
	}
	text := extractText(t, result)
	if !strings.Contains(text, `"status":"cancelled"`) {
		t.Errorf("response missing cancelled status: %s", text)
	}
}

// TestDispatchPipelineTool_Cancel_ServiceError — error path returns
// pipeline_cancel_failed (distinct code from pipeline_run_failed).
func TestDispatchPipelineTool_Cancel_ServiceError(t *testing.T) {
	ps := &stubPipelineService{cancelErr: errors.New("already terminal")}
	srv := newPipelineSrv(t, ps)
	result, _ := srv.dispatchPipelineTool(context.Background(), "pipeline-cancel",
		json.RawMessage(`{"run_id":"run_x"}`))
	if result["isError"] != true {
		t.Errorf("isError = %v; want true", result["isError"])
	}
	if !strings.Contains(extractText(t, result), "already terminal") {
		t.Errorf("error message should surface service error: %s", extractText(t, result))
	}
}

// TestDispatchPipelineTool_RunStatus_MissingRunID — same gate as
// pipeline-get's id requirement, applied to run_id.
func TestDispatchPipelineTool_RunStatus_MissingRunID(t *testing.T) {
	ps := &stubPipelineService{}
	srv := newPipelineSrv(t, ps)
	result, _ := srv.dispatchPipelineTool(context.Background(), "pipeline-run-status",
		json.RawMessage(`{}`))
	if result["isError"] != true {
		t.Error("isError should be true for missing run_id")
	}
	if !strings.Contains(extractText(t, result), "run_id is required") {
		t.Errorf("error should mention run_id: %s", extractText(t, result))
	}
}

// TestDispatchPipelineTool_UnknownTool — unknown name returns
// (nil, false) so the caller falls through to pack-tool lookup.
// Without this fallthrough an unknown helmdeck__ tool would
// silently fail instead of producing the documented -32601 (method
// not found) from the pack layer.
func TestDispatchPipelineTool_UnknownTool(t *testing.T) {
	srv := newPipelineSrv(t, &stubPipelineService{})
	result, handled := srv.dispatchPipelineTool(context.Background(), "pipeline-bogus", nil)
	if handled {
		t.Errorf("unknown tool should NOT be handled (handled=true); fallthrough lost: result=%+v", result)
	}
}

// TestOkToolResult_ShapeIsStable pins the exact envelope shape MCP
// clients parse. Two fields, in order: content (array of
// {type:"text", text:"..."}) and isError (false). A drift here
// would break every connected MCP client (OpenClaw, Gemini CLI,
// Claude Code) all at once.
func TestOkToolResult_ShapeIsStable(t *testing.T) {
	body := json.RawMessage(`{"x":1}`)
	result := okToolResult(body)
	if result["isError"] != false {
		t.Errorf("isError = %v; want false", result["isError"])
	}
	content, ok := result["content"].([]map[string]any)
	if !ok || len(content) != 1 {
		t.Fatalf("content shape wrong: %+v", result["content"])
	}
	if content[0]["type"] != "text" {
		t.Errorf("content[0].type = %v; want text", content[0]["type"])
	}
	if content[0]["text"] != `{"x":1}` {
		t.Errorf("content[0].text = %v; want raw body", content[0]["text"])
	}
}
