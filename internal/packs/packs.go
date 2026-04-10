// Package packs implements the Capability Pack execution engine
// described in T205 / ADR 003 / ADR 008.
//
// A Pack is a self-contained, schema-validated unit of work the
// control plane can run on behalf of an agent: "render slides",
// "screenshot a URL", "scrape an SPA". The Engine drives a fixed
// pipeline around the pack's handler so every pack ships with the
// same guarantees regardless of what it does internally:
//
//	1. validate input  (typed, refused before any side effects)
//	2. acquire session (only when the pack declares NeedsSession)
//	3. invoke handler  (with a strongly-typed ExecutionContext)
//	4. validate output (refuses leaks of un-schemaed payloads)
//	5. surface artifacts uploaded during the run
//	6. return a typed Result OR a typed error
//
// The pack registry (T207), built-in packs (T208–T210), and the
// artifact upload backend (T211) all build on this engine. T206
// enforces that handler errors get bucketed into the closed-set
// error codes defined here.
package packs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/tosin2013/helmdeck/internal/cdp"
	"github.com/tosin2013/helmdeck/internal/session"
	"github.com/tosin2013/helmdeck/internal/telemetry"
)

// CDPFactory dials a chromedp client for a session id and is the
// bridge between the engine's session lifecycle and pack handlers
// that need to drive the browser. The same interface is implemented
// by api.DefaultCDPClientFactory so REST and pack code share one
// connection cache. Evict is called by the engine after Terminate.
type CDPFactory interface {
	Get(ctx context.Context, sessionID string) (cdp.Client, error)
	Evict(sessionID string)
}

// Schema validates a JSON payload. Pack authors plug in any
// implementation — a real JSON Schema library, a hand-rolled
// validator, or BasicSchema (below) for the simple "type + required"
// case. Keeping this an interface lets T205 ship without taking on
// a JSON Schema dependency, while still enforcing validation
// uniformly across every pack.
type Schema interface {
	Validate(data json.RawMessage) error
}

// Pack is the unit the engine executes. Name and Version together form
// the registry key in T207's pack registry; the engine itself is
// stateless and just runs whatever Pack value is handed in.
type Pack struct {
	Name         string
	Version      string
	Description  string
	InputSchema  Schema      // validated before Handler runs
	OutputSchema Schema      // validated before Handler's output is returned
	NeedsSession    bool         // when true, Engine acquires a session and exposes it via ExecutionContext
	PreserveSession bool         // when true AND NeedsSession, the engine does NOT terminate the session on return — the session persists for follow-on packs to reuse via _session_id. Watchdog cleans up after timeout.
	SessionSpec     session.Spec // optional override; zero value means "runtime defaults"
	Handler      HandlerFunc

	// ArtifactTTL is the per-pack retention override consulted by the
	// janitor (T211b, ADR 031). Zero means "use the platform default
	// from HELMDECK_ARTIFACT_TTL". Set on packs whose outputs are
	// either especially short-lived (e.g. one-off screenshots) or
	// especially valuable (e.g. rendered slide decks the user shares).
	ArtifactTTL time.Duration
}

// HandlerFunc is the per-pack work function. It receives an
// ExecutionContext bound by the engine — handlers MUST NOT acquire
// their own sessions or call the artifact store directly outside this
// context, because the engine relies on those touchpoints for
// lifecycle management and audit trails.
type HandlerFunc func(ctx context.Context, ec *ExecutionContext) (json.RawMessage, error)

// ExecutionContext is what handlers see. It bundles the validated
// input, an optional session handle, a logger pre-tagged with pack
// metadata, and an artifact store handle the handler can write to.
type ExecutionContext struct {
	Pack      *Pack
	Input     json.RawMessage
	Session   *session.Session // nil unless Pack.NeedsSession
	CDP       cdp.Client       // nil unless Pack.NeedsSession AND engine has a CDPFactory
	Logger    *slog.Logger
	Artifacts ArtifactStore

	// Exec runs a command inside the current session container, when
	// the engine has a session.Executor wired AND the pack acquired a
	// session. Nil otherwise — packs MUST nil-check before calling.
	// Wrapped here as a closure so handlers don't need to thread the
	// session id manually and can never call Exec against another
	// session by mistake.
	Exec func(ctx context.Context, req session.ExecRequest) (session.ExecResult, error)
}

// Result is what Engine.Execute returns on success. Output is the
// raw bytes the handler produced (already validated against the
// pack's OutputSchema), Artifacts is whatever the handler uploaded
// during the run, and Duration covers the entire pipeline so
// operators see the cost of validation + session spin-up, not just
// the handler's wall-clock.
type Result struct {
	Pack      string          `json:"pack"`
	Version   string          `json:"version"`
	Output    json.RawMessage `json:"output"`
	Artifacts []Artifact      `json:"artifacts,omitempty"`
	Duration  time.Duration   `json:"duration_ms"`
	// SessionID is set when the pack ran inside a session container.
	// Callers pass it as `_session_id` in the input of a follow-on
	// pack to reuse the same session (session pinning). Empty when
	// the pack doesn't need a session.
	SessionID string `json:"session_id,omitempty"`
}

// Engine is the pipeline runner. Construct one per process; it is
// safe for concurrent use because it holds no mutable state.
type Engine struct {
	runtime    session.Runtime  // optional; nil disallows packs with NeedsSession
	cdpFactory CDPFactory       // optional; when nil, ExecutionContext.CDP is nil
	executor   session.Executor // optional; when nil, ExecutionContext.Exec is nil
	artifacts  ArtifactStore    // optional; defaults to an in-memory store
	logger     *slog.Logger
	now        func() time.Time
}

// Option configures Engine at construction time. Functional options
// keep New extensible without churning the constructor signature
// every time a new dependency lands (T211 will add a real S3 store).
type Option func(*Engine)

// WithRuntime hands the engine a session runtime so packs that
// declare NeedsSession can be executed.
func WithRuntime(rt session.Runtime) Option { return func(e *Engine) { e.runtime = rt } }

// WithCDPFactory wires a CDP client factory so handlers receive a
// live cdp.Client through ExecutionContext.CDP. Without one, packs
// that need browser control must dial CDP themselves from the
// session's CDPEndpoint — slower and easy to get wrong.
func WithCDPFactory(f CDPFactory) Option { return func(e *Engine) { e.cdpFactory = f } }

// WithSessionExecutor wires a session.Executor so handlers can run
// commands inside the session container via ExecutionContext.Exec.
// Required for packs that shell out to in-container tools (Marp,
// ffmpeg, tesseract). Without one, ExecutionContext.Exec is nil.
func WithSessionExecutor(ex session.Executor) Option {
	return func(e *Engine) { e.executor = ex }
}

// WithArtifactStore overrides the default in-memory artifact store.
func WithArtifactStore(s ArtifactStore) Option { return func(e *Engine) { e.artifacts = s } }

// WithLogger overrides the default slog.Default() logger.
func WithLogger(l *slog.Logger) Option { return func(e *Engine) { e.logger = l } }

// New constructs an Engine.
func New(opts ...Option) *Engine {
	e := &Engine{
		artifacts: NewMemoryArtifactStore(),
		logger:    slog.Default(),
		now:       func() time.Time { return time.Now().UTC() },
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

// Execute runs pack against input. The returned Result/error is
// fully bucketed: every error is a *PackError carrying one of the
// closed-set codes in errors.go, so callers (REST handlers, MCP
// bridges, A2A streams) can map them to wire-level error envelopes
// without inspecting messages.
func (e *Engine) Execute(ctx context.Context, pack *Pack, input json.RawMessage) (result *Result, retErr error) {
	if pack == nil {
		return nil, &PackError{Code: CodeInternal, Message: "nil pack"}
	}
	if pack.Handler == nil {
		return nil, &PackError{Code: CodeInternal, Message: "pack has no handler"}
	}
	start := e.now()
	logger := e.logger.With("pack", pack.Name, "version", pack.Version)

	// T510: every pack execution gets one OTel span. Cheap when OTel
	// is disabled (helmdeck telemetry no-op tracer); free attribute
	// data when enabled. The deferred closure inspects the named
	// return values so success/error status is recorded regardless
	// of which branch the handler takes.
	tracer := otel.Tracer("helmdeck/packs")
	ctx, span := tracer.Start(ctx, "pack."+pack.Name,
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(
			telemetry.Helmdeck.PackName.String(pack.Name),
			telemetry.Helmdeck.PackVersion.String(pack.Version),
		),
	)
	defer func() {
		if retErr != nil {
			span.RecordError(retErr)
			span.SetStatus(codes.Error, retErr.Error())
			if pe, ok := retErr.(*PackError); ok {
				span.SetAttributes(telemetry.Helmdeck.PackResult.String(string(pe.Code)))
			} else {
				span.SetAttributes(telemetry.Helmdeck.PackResult.String("error"))
			}
		} else {
			span.SetAttributes(telemetry.Helmdeck.PackResult.String("ok"))
			span.SetStatus(codes.Ok, "")
		}
		span.End()
	}()

	// Step 1: input schema. Validation runs against the raw bytes so a
	// pack can choose its own JSON Schema implementation without the
	// engine ever decoding into a Go type.
	if pack.InputSchema != nil {
		if err := pack.InputSchema.Validate(input); err != nil {
			return nil, &PackError{Code: CodeInvalidInput, Message: err.Error(), Cause: err}
		}
	}

	// Step 2: session acquire. Callers can pin to an existing session
	// by passing `_session_id` in the input JSON — the engine looks it
	// up instead of creating a new one. This is the mechanism that lets
	// the Phase 5.5 code-edit loop work: repo.fetch creates a session
	// and returns its ID; follow-on packs (fs.*, cmd.run, git.commit,
	// repo.push) reuse it by passing the same _session_id.
	//
	// When pinning, the engine does NOT terminate the session on return
	// — the session persists until the watchdog's timeout (default 5m)
	// or an explicit terminate call. New sessions (no _session_id) are
	// still cleaned up on return as before.
	var sess *session.Session
	var pinnedSession bool
	if pack.NeedsSession {
		if e.runtime == nil {
			return nil, &PackError{Code: CodeSessionUnavailable, Message: "engine has no session runtime"}
		}
		// Check for _session_id in the input. If present, reuse the
		// existing session instead of creating a new one.
		var meta struct {
			SessionID string `json:"_session_id"`
		}
		_ = json.Unmarshal(input, &meta) // best-effort; missing field is fine
		if meta.SessionID != "" {
			s, err := e.runtime.Get(ctx, meta.SessionID)
			if err != nil {
				return nil, &PackError{Code: CodeSessionUnavailable,
					Message: fmt.Sprintf("session %q not found: %v", meta.SessionID, err)}
			}
			sess = s
			pinnedSession = true
			logger.Info("reusing pinned session", "session_id", meta.SessionID, "pack", pack.Name)
		} else {
			s, err := e.runtime.Create(ctx, pack.SessionSpec)
			if err != nil {
				return nil, &PackError{Code: CodeSessionUnavailable, Message: err.Error(), Cause: err}
			}
			sess = s
		}
		if !pinnedSession && !pack.PreserveSession {
			defer func() {
				if e.cdpFactory != nil {
					e.cdpFactory.Evict(sess.ID)
				}
				tctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if err := e.runtime.Terminate(tctx, sess.ID); err != nil {
					logger.Warn("session terminate failed", "session_id", sess.ID, "err", err)
				}
			}()
		}
	}

	// Dial CDP after Create succeeded so handler errors don't leave
	// a half-initialized client behind. The factory caches per
	// session id, so re-dialing is cheap if a future pack reuses
	// the same session.
	var cdpClient cdp.Client
	if pack.NeedsSession && e.cdpFactory != nil && sess != nil {
		c, err := e.cdpFactory.Get(ctx, sess.ID)
		if err != nil {
			return nil, &PackError{Code: CodeSessionUnavailable, Message: err.Error(), Cause: err}
		}
		cdpClient = c
	}

	ec := &ExecutionContext{
		Pack:      pack,
		Input:     input,
		Session:   sess,
		CDP:       cdpClient,
		Logger:    logger,
		Artifacts: e.artifacts,
	}
	if pack.NeedsSession && e.executor != nil && sess != nil {
		sessID := sess.ID
		ec.Exec = func(ctx context.Context, req session.ExecRequest) (session.ExecResult, error) {
			return e.executor.Exec(ctx, sessID, req)
		}
	}

	// Step 3: handler. Recover from panics so a buggy pack can never
	// take down the control plane process — the recovered panic is
	// reported as CodeHandlerFailed with a fixed message because the
	// stack itself is not safe to surface to a remote agent.
	output, err := safeInvoke(ctx, ec, pack.Handler)
	if err != nil {
		// T206: every handler error funnels through Classify so the
		// returned code is always one of the closed-set ADR 008 values.
		return nil, wrap(err)
	}

	// Step 4: output schema. Refuse to surface a payload the schema
	// doesn't accept — the agent contract is "you get exactly what
	// the pack declared", and a silent drift is worse than a loud
	// failure.
	if pack.OutputSchema != nil {
		if err := pack.OutputSchema.Validate(output); err != nil {
			return nil, &PackError{Code: CodeInvalidOutput, Message: err.Error(), Cause: err}
		}
	}

	// Step 5: collect artifacts the handler wrote during THIS run.
	// ListForPack returns every artifact ever produced for this pack
	// name (the index is append-only across the process lifetime).
	// Filter to only artifacts with CreatedAt >= start so the
	// response scopes to this execution and doesn't accumulate
	// entries from prior calls.
	allArts, err := e.artifacts.ListForPack(ctx, pack.Name)
	if err != nil {
		return nil, &PackError{Code: CodeArtifactFailed, Message: err.Error(), Cause: err}
	}
	var arts []Artifact
	for _, a := range allArts {
		if !a.CreatedAt.Before(start) {
			arts = append(arts, a)
		}
	}

	var sessionID string
	if sess != nil {
		sessionID = sess.ID
	}
	return &Result{
		Pack:      pack.Name,
		Version:   pack.Version,
		Output:    output,
		Artifacts: arts,
		SessionID: sessionID,
		Duration:  e.now().Sub(start),
	}, nil
}

// safeInvoke runs handler with a deferred recover so panics become
// errors. The recovered value is intentionally not formatted into the
// error message — it might contain caller-supplied data and the
// engine has no way to redact it safely. T206's middleware can add
// stack capture for the audit log if needed.
func safeInvoke(ctx context.Context, ec *ExecutionContext, h HandlerFunc) (out json.RawMessage, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("pack handler panicked: %v", r)
		}
	}()
	return h(ctx, ec)
}
