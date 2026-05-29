package packs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/tosin2013/helmdeck/internal/session"
)

func newTestEngine() *Engine {
	return New(WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
}

// fakeRuntime is a minimal session.Runtime implementation for the
// engine tests. It records create/terminate counts so tests can
// assert lifecycle behavior without spinning up Docker.
type fakeRuntime struct {
	createCalls    int
	terminateCalls int
	createErr      error
	// lastSpec captures the Spec the last Create was called with so
	// label-on-create tests can assert what the engine passed in.
	lastSpec session.Spec
}

func (f *fakeRuntime) Create(ctx context.Context, spec session.Spec) (*session.Session, error) {
	f.createCalls++
	f.lastSpec = spec
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &session.Session{ID: "sess-1", Status: session.StatusRunning}, nil
}
func (f *fakeRuntime) Get(ctx context.Context, id string) (*session.Session, error) {
	if id == "" {
		return nil, nil
	}
	return &session.Session{ID: id, Status: session.StatusRunning}, nil
}
func (f *fakeRuntime) List(ctx context.Context) ([]*session.Session, error)       { return nil, nil }
func (f *fakeRuntime) Logs(ctx context.Context, id string) (io.ReadCloser, error) { return nil, nil }
func (f *fakeRuntime) Terminate(ctx context.Context, id string) error             { f.terminateCalls++; return nil }
func (f *fakeRuntime) Close() error                                               { return nil }

func TestEngineHappyPath(t *testing.T) {
	pack := &Pack{
		Name:    "echo",
		Version: "v1",
		InputSchema: BasicSchema{
			Required:   []string{"msg"},
			Properties: map[string]string{"msg": "string"},
		},
		OutputSchema: BasicSchema{
			Required:   []string{"echo"},
			Properties: map[string]string{"echo": "string"},
		},
		Handler: func(ctx context.Context, ec *ExecutionContext) (json.RawMessage, error) {
			var in struct {
				Msg string `json:"msg"`
			}
			_ = json.Unmarshal(ec.Input, &in)
			return json.Marshal(map[string]string{"echo": in.Msg})
		},
	}
	res, err := newTestEngine().Execute(context.Background(), pack, json.RawMessage(`{"msg":"hi"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Pack != "echo" || res.Version != "v1" {
		t.Errorf("res = %+v", res)
	}
	if string(res.Output) != `{"echo":"hi"}` {
		t.Errorf("output = %s", res.Output)
	}
	if res.Duration <= 0 {
		t.Errorf("duration = %v", res.Duration)
	}
}

func TestEngineRejectsBadInput(t *testing.T) {
	pack := &Pack{
		Name: "echo", Version: "v1",
		InputSchema: BasicSchema{Required: []string{"msg"}, Properties: map[string]string{"msg": "string"}},
		Handler: func(ctx context.Context, ec *ExecutionContext) (json.RawMessage, error) {
			t.Error("handler should not run on invalid input")
			return nil, nil
		},
	}
	_, err := newTestEngine().Execute(context.Background(), pack, json.RawMessage(`{}`))
	var perr *PackError
	if !errors.As(err, &perr) || perr.Code != CodeInvalidInput {
		t.Errorf("err = %v, want CodeInvalidInput", err)
	}
}

func TestEngineRejectsBadOutput(t *testing.T) {
	pack := &Pack{
		Name: "echo", Version: "v1",
		OutputSchema: BasicSchema{Required: []string{"echo"}},
		Handler: func(ctx context.Context, ec *ExecutionContext) (json.RawMessage, error) {
			return json.RawMessage(`{"wrong":"shape"}`), nil
		},
	}
	_, err := newTestEngine().Execute(context.Background(), pack, json.RawMessage(`{}`))
	var perr *PackError
	if !errors.As(err, &perr) || perr.Code != CodeInvalidOutput {
		t.Errorf("err = %v, want CodeInvalidOutput", err)
	}
}

func TestEngineSessionLifecycle(t *testing.T) {
	rt := &fakeRuntime{}
	pack := &Pack{
		Name: "screenshot", Version: "v1",
		NeedsSession: true,
		Handler: func(ctx context.Context, ec *ExecutionContext) (json.RawMessage, error) {
			if ec.Session == nil || ec.Session.ID != "sess-1" {
				return nil, errors.New("session not injected")
			}
			return json.RawMessage(`{}`), nil
		},
	}
	eng := New(WithRuntime(rt), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if _, err := eng.Execute(context.Background(), pack, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if rt.createCalls != 1 || rt.terminateCalls != 1 {
		t.Errorf("create=%d terminate=%d (want 1/1)", rt.createCalls, rt.terminateCalls)
	}
}

func TestEngineSessionTerminatedEvenOnHandlerError(t *testing.T) {
	rt := &fakeRuntime{}
	pack := &Pack{
		Name: "fail", Version: "v1", NeedsSession: true,
		Handler: func(ctx context.Context, ec *ExecutionContext) (json.RawMessage, error) {
			return nil, errors.New("kaboom")
		},
	}
	eng := New(WithRuntime(rt), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	_, err := eng.Execute(context.Background(), pack, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if rt.terminateCalls != 1 {
		t.Errorf("terminate not called on handler error: %d", rt.terminateCalls)
	}
}

func TestEngineSessionUnavailableWithoutRuntime(t *testing.T) {
	pack := &Pack{Name: "x", Version: "v1", NeedsSession: true,
		Handler: func(ctx context.Context, ec *ExecutionContext) (json.RawMessage, error) {
			return nil, nil
		}}
	_, err := newTestEngine().Execute(context.Background(), pack, nil)
	var perr *PackError
	if !errors.As(err, &perr) || perr.Code != CodeSessionUnavailable {
		t.Errorf("err = %v, want CodeSessionUnavailable", err)
	}
}

func TestEngineSessionCreateError(t *testing.T) {
	rt := &fakeRuntime{createErr: errors.New("docker dead")}
	pack := &Pack{Name: "x", Version: "v1", NeedsSession: true,
		Handler: func(ctx context.Context, ec *ExecutionContext) (json.RawMessage, error) { return nil, nil }}
	eng := New(WithRuntime(rt), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	_, err := eng.Execute(context.Background(), pack, nil)
	var perr *PackError
	if !errors.As(err, &perr) || perr.Code != CodeSessionUnavailable {
		t.Errorf("err = %v, want CodeSessionUnavailable", err)
	}
}

func TestEngineHandlerPanicRecovered(t *testing.T) {
	pack := &Pack{Name: "boom", Version: "v1",
		Handler: func(ctx context.Context, ec *ExecutionContext) (json.RawMessage, error) {
			panic("oh no")
		}}
	_, err := newTestEngine().Execute(context.Background(), pack, nil)
	var perr *PackError
	if !errors.As(err, &perr) || perr.Code != CodeHandlerFailed {
		t.Errorf("err = %v, want CodeHandlerFailed", err)
	}
}

func TestEngineTimeoutClassification(t *testing.T) {
	pack := &Pack{Name: "slow", Version: "v1",
		Handler: func(ctx context.Context, ec *ExecutionContext) (json.RawMessage, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := newTestEngine().Execute(ctx, pack, nil)
	var perr *PackError
	if !errors.As(err, &perr) || perr.Code != CodeTimeout {
		t.Errorf("err = %v, want CodeTimeout", err)
	}
}

func TestEnginePreservesPackErrorFromHandler(t *testing.T) {
	pack := &Pack{Name: "x", Version: "v1",
		Handler: func(ctx context.Context, ec *ExecutionContext) (json.RawMessage, error) {
			return nil, &PackError{Code: CodeArtifactFailed, Message: "s3 down"}
		}}
	_, err := newTestEngine().Execute(context.Background(), pack, nil)
	var perr *PackError
	if !errors.As(err, &perr) || perr.Code != CodeArtifactFailed {
		t.Errorf("err = %v, want CodeArtifactFailed preserved", err)
	}
}

func TestEngineCollectsArtifacts(t *testing.T) {
	pack := &Pack{Name: "snap", Version: "v1",
		Handler: func(ctx context.Context, ec *ExecutionContext) (json.RawMessage, error) {
			if _, err := ec.Artifacts.Put(ctx, ec.Pack.Name, "a.png", []byte("aaaa"), "image/png"); err != nil {
				return nil, err
			}
			if _, err := ec.Artifacts.Put(ctx, ec.Pack.Name, "b.png", []byte("bbbbbbb"), "image/png"); err != nil {
				return nil, err
			}
			return json.RawMessage(`{}`), nil
		}}
	res, err := newTestEngine().Execute(context.Background(), pack, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Artifacts) != 2 {
		t.Errorf("artifacts = %d", len(res.Artifacts))
	}
	for _, a := range res.Artifacts {
		if a.Pack != "snap" || a.ContentType != "image/png" || a.Size == 0 {
			t.Errorf("artifact = %+v", a)
		}
	}
}

func TestBasicSchema(t *testing.T) {
	s := BasicSchema{
		Required: []string{"url"},
		Properties: map[string]string{
			"url":      "string",
			"fullPage": "boolean",
			"timeout":  "number",
			"headers":  "object",
		},
	}
	good := []string{
		`{"url":"https://x"}`,
		`{"url":"x","fullPage":true,"timeout":30,"headers":{"a":"b"}}`,
	}
	for _, g := range good {
		if err := s.Validate(json.RawMessage(g)); err != nil {
			t.Errorf("good %s: %v", g, err)
		}
	}
	bad := map[string]string{
		"missing required":  `{}`,
		"wrong url type":    `{"url":123}`,
		"wrong bool type":   `{"url":"x","fullPage":"yes"}`,
		"not an object":     `["url"]`,
	}
	for name, b := range bad {
		if err := s.Validate(json.RawMessage(b)); err == nil {
			t.Errorf("bad %s expected error", name)
		}
	}
}

func TestMemoryArtifactStoreRoundTrip(t *testing.T) {
	s := NewMemoryArtifactStore()
	ctx := context.Background()
	a, err := s.Put(ctx, "snap", "x.png", []byte("data"), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	got, meta, err := s.Get(ctx, a.Key)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "data" || meta.Size != 4 {
		t.Errorf("get = %q meta=%+v", got, meta)
	}
	list, _ := s.ListForPack(ctx, "snap")
	if len(list) != 1 {
		t.Errorf("list = %d", len(list))
	}
	other, _ := s.ListForPack(ctx, "nope")
	if len(other) != 0 {
		t.Errorf("list other = %d", len(other))
	}
}

// T510 — verify the engine emits a helmdeck.pack.* span on a
// successful execution and on a handler-error path. Uses the
// in-memory tracetest exporter so no real OTel collector is needed.
func TestEngineExecute_EmitsPackSpan_Success(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(noop.NewTracerProvider()) })

	pack := &Pack{
		Name:    "test.echo",
		Version: "v1",
		Handler: func(ctx context.Context, ec *ExecutionContext) (json.RawMessage, error) {
			return json.RawMessage(`{"ok":true}`), nil
		},
	}
	eng := New()
	if _, err := eng.Execute(context.Background(), pack, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	span := spans[0]
	if span.Name != "pack.test.echo" {
		t.Errorf("span name = %s", span.Name)
	}
	got := map[string]any{}
	for _, a := range span.Attributes {
		got[string(a.Key)] = a.Value.AsInterface()
	}
	if got["helmdeck.pack.name"] != "test.echo" {
		t.Errorf("pack name attr wrong: %v", got["helmdeck.pack.name"])
	}
	if got["helmdeck.pack.result"] != "ok" {
		t.Errorf("result attr should be ok on success, got %v", got["helmdeck.pack.result"])
	}
}

func TestEngineExecute_EmitsPackSpan_Error(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(noop.NewTracerProvider()) })

	pack := &Pack{
		Name:    "test.fail",
		Version: "v1",
		Handler: func(ctx context.Context, ec *ExecutionContext) (json.RawMessage, error) {
			return nil, &PackError{Code: CodeHandlerFailed, Message: "boom"}
		},
	}
	eng := New()
	if _, err := eng.Execute(context.Background(), pack, json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected handler error")
	}
	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	got := map[string]any{}
	for _, a := range spans[0].Attributes {
		got[string(a.Key)] = a.Value.AsInterface()
	}
	if got["helmdeck.pack.result"] != "handler_failed" {
		t.Errorf("result attr should be the typed code, got %v", got["helmdeck.pack.result"])
	}
}

// TestEngine_WithRunID_LabelsCreatedSession — WithRunID on the ctx ⇒ Execute
// stamps the helmdeck.run_id label on the session spec it passes to
// Runtime.Create. TerminateByRunID later finds the container by this label.
func TestEngine_WithRunID_LabelsCreatedSession(t *testing.T) {
	rt := &fakeRuntime{}
	pack := &Pack{
		Name: "render", Version: "v1", NeedsSession: true,
		Handler: func(ctx context.Context, ec *ExecutionContext) (json.RawMessage, error) {
			return json.RawMessage(`{}`), nil
		},
	}
	eng := New(WithRuntime(rt), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	ctx := WithRunID(context.Background(), "run_abc")
	if _, err := eng.Execute(ctx, pack, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := rt.lastSpec.Labels[session.LabelRunID]; got != "run_abc" {
		t.Errorf("Create spec.Labels[%s] = %q, want run_abc", session.LabelRunID, got)
	}
}

// TestEngine_WithRunID_PinnedSessionNotRelabeled — a reused (_session_id)
// session belongs to whoever created it. Execute reuses it via Get and must
// NOT call Create or stamp the run label on the pack's shared SessionSpec.
func TestEngine_WithRunID_PinnedSessionNotRelabeled(t *testing.T) {
	rt := &fakeRuntime{}
	pack := &Pack{
		Name: "render", Version: "v1", NeedsSession: true,
		SessionSpec: session.Spec{Image: "shared"},
		Handler: func(ctx context.Context, ec *ExecutionContext) (json.RawMessage, error) {
			return json.RawMessage(`{}`), nil
		},
	}
	eng := New(WithRuntime(rt), WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	ctx := WithRunID(context.Background(), "run_xyz")
	if _, err := eng.Execute(ctx, pack, json.RawMessage(`{"_session_id":"pinned"}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if rt.createCalls != 0 {
		t.Errorf("pinned session should reuse, not create; createCalls=%d", rt.createCalls)
	}
	// The pack's shared SessionSpec must be untouched (no per-run label
	// stamped on its Labels map across calls).
	if v, ok := pack.SessionSpec.Labels[session.LabelRunID]; ok {
		t.Errorf("pack.SessionSpec should not be mutated by Execute, got Labels[%s]=%q", session.LabelRunID, v)
	}
}
