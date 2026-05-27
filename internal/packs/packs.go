// Package packs implements the Capability Pack execution engine
// described in T205 / ADR 003 / ADR 008.
//
// A Pack is a self-contained, schema-validated unit of work the
// control plane can run on behalf of an agent: "render slides",
// "screenshot a URL", "scrape an SPA". The Engine drives a fixed
// pipeline around the pack's handler so every pack ships with the
// same guarantees regardless of what it does internally:
//
//  1. validate input  (typed, refused before any side effects)
//  2. acquire session (only when the pack declares NeedsSession)
//  3. invoke handler  (with a strongly-typed ExecutionContext)
//  4. validate output (refuses leaks of un-schemaed payloads)
//  5. surface artifacts uploaded during the run
//  6. return a typed Result OR a typed error
//
// The pack registry (T207), built-in packs (T208–T210), and the
// artifact upload backend (T211) all build on this engine. T206
// enforces that handler errors get bucketed into the closed-set
// error codes defined here.
package packs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/tosin2013/helmdeck/internal/cdp"
	"github.com/tosin2013/helmdeck/internal/memory"
	"github.com/tosin2013/helmdeck/internal/session"
	"github.com/tosin2013/helmdeck/internal/telemetry"
)

// ProgressFunc is the callback signature handlers use to report
// incremental status. pct is a 0-100 percentage (handlers should
// emit 0 at the start and 100 just before returning); message is a
// short human-readable summary of the current step. The MCP server
// translates these into `notifications/progress` JSON-RPC frames
// (T302a follow-up, see internal/mcp/server.go).
type ProgressFunc func(pct float64, message string)

// progressCtxKey is the context value key used to thread a
// ProgressFunc from the MCP server (or the async job runner) into
// Engine.Execute without bloating its signature. Unexported so the
// only way to attach a callback is via WithProgress below.
type progressCtxKey struct{}

// WithProgress returns a child context that carries the supplied
// progress callback. Engine.Execute pulls it off the context and
// installs it on ExecutionContext.Progress so handlers can call it
// directly. Pass nil to clear an inherited callback.
func WithProgress(ctx context.Context, fn ProgressFunc) context.Context {
	return context.WithValue(ctx, progressCtxKey{}, fn)
}

// progressFromContext returns the callback attached by WithProgress,
// or a no-op when none is present. Always non-nil so callers don't
// need to check.
func progressFromContext(ctx context.Context) ProgressFunc {
	if v, ok := ctx.Value(progressCtxKey{}).(ProgressFunc); ok && v != nil {
		return v
	}
	return func(float64, string) {}
}

// callerCtxKey carries the authenticated caller subject (JWT "sub")
// into Engine.Execute so the memory layer can derive a stable
// namespace. Mirrors progressCtxKey. Unexported so the only way to
// attach a caller is via WithCaller.
type callerCtxKey struct{}

// WithCaller returns a child context carrying the caller's subject
// (e.g. the JWT "sub" claim). The REST and MCP layers call this before
// Engine.Execute so memory namespaces are scoped per caller. An empty
// subject is treated as "unknown" by callerFromContext.
func WithCaller(ctx context.Context, subject string) context.Context {
	return context.WithValue(ctx, callerCtxKey{}, subject)
}

// callerFromContext returns the subject attached by WithCaller, or
// "unknown" when none is present (or it is empty). Always non-empty so
// the memory namespace is always well-defined.
func callerFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(callerCtxKey{}).(string); ok && v != "" {
		return v
	}
	return "unknown"
}

// MemoryConfig is the per-pack opt-in for the memory engine seam. The
// zero value (nil *MemoryConfig on a Pack) means "no memory hooks" —
// the pack runs exactly as it did before the memory layer existed.
type MemoryConfig struct {
	// Cache, when true, turns on the read-through response cache: the
	// engine keys on sha256(input) under the pack name and, if a fresh
	// entry exists in the caller's namespace, returns it WITHOUT
	// invoking the handler. After a successful miss it stores the
	// output under that key with TTL.
	Cache bool
	// TTL bounds cached entries. Zero with Cache=true means "never
	// expire" — almost never what you want for a cache; set a real TTL.
	TTL time.Duration
	// Category tags stored cache entries (defaults to "cache" when
	// empty) for Context() grouping.
	Category string
}

// EntrySummary is a redacted, value-light view of a memory.Entry used
// by Context() so packs/agents can decide what to recall without
// pulling every payload. The Fingerprint identifies the value; callers
// Recall by Key to get the bytes.
type EntrySummary struct {
	Key         string    `json:"key"`
	Category    string    `json:"category,omitempty"`
	Tags        []string  `json:"tags,omitempty"`
	Fingerprint string    `json:"fingerprint"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// SessionContext is the structured recall surface returned by
// MemoryInterface.Context (#260): the N most-recent non-expired
// entries in the namespace, grouped by category.
type SessionContext struct {
	Namespace string                    `json:"namespace"`
	Entries   map[string][]EntrySummary `json:"entries"`
}

// contextEntryCap bounds how many entries Context() aggregates so a
// long-lived namespace doesn't return an unbounded blob.
const contextEntryCap = 50

// MemoryInterface is the handler-facing, namespace-scoped memory
// handle exposed on ExecutionContext.Memory. It hides the namespace so
// handlers never address another caller's memory by mistake. It is
// non-nil only when the engine has a MemoryStore wired (WithMemoryStore)
// — handlers MUST nil-check ec.Memory.
type MemoryInterface interface {
	Store(key string, value []byte, opts ...memory.PutOption) error
	Recall(key string) (*memory.Entry, error)
	List(prefix string) ([]memory.Entry, error)
	Delete(key string) error
	Namespace() string
	Context() (*SessionContext, error)
}

// memoryAdapter binds a memory.MemoryStore to a fixed namespace and a
// context, satisfying MemoryInterface. One is built per Execute call.
type memoryAdapter struct {
	store memory.MemoryStore
	ns    string
	ctx   context.Context
}

func (m *memoryAdapter) Namespace() string { return m.ns }

func (m *memoryAdapter) Store(key string, value []byte, opts ...memory.PutOption) error {
	_, err := m.store.Put(m.ctx, m.ns, key, value, opts...)
	return err
}

func (m *memoryAdapter) Recall(key string) (*memory.Entry, error) {
	e, err := m.store.Get(m.ctx, m.ns, key)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func (m *memoryAdapter) List(prefix string) ([]memory.Entry, error) {
	return m.store.List(m.ctx, m.ns, prefix)
}

func (m *memoryAdapter) Delete(key string) error {
	return m.store.Delete(m.ctx, m.ns, key)
}

func (m *memoryAdapter) Context() (*SessionContext, error) {
	entries, err := m.store.List(m.ctx, m.ns, "")
	if err != nil {
		return nil, err
	}
	// List returns newest-first; cap to the most-recent N.
	if len(entries) > contextEntryCap {
		entries = entries[:contextEntryCap]
	}
	sc := &SessionContext{Namespace: m.ns, Entries: map[string][]EntrySummary{}}
	for _, e := range entries {
		cat := e.Category
		if cat == "" {
			cat = "uncategorized"
		}
		sc.Entries[cat] = append(sc.Entries[cat], EntrySummary{
			Key:         e.Key,
			Category:    e.Category,
			Tags:        e.Tags,
			Fingerprint: e.Fingerprint,
			CreatedAt:   e.CreatedAt,
			UpdatedAt:   e.UpdatedAt,
		})
	}
	return sc, nil
}

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
	Name            string
	Version         string
	Description     string
	InputSchema     Schema       // validated before Handler runs
	OutputSchema    Schema       // validated before Handler's output is returned
	NeedsSession    bool         // when true, Engine acquires a session and exposes it via ExecutionContext
	PreserveSession bool         // when true AND NeedsSession, the engine does NOT terminate the session on return — the session persists for follow-on packs to reuse via _session_id. Watchdog cleans up after timeout.
	SessionSpec     session.Spec // optional override; zero value means "runtime defaults"
	Handler         HandlerFunc

	// ArtifactTTL is the per-pack retention override consulted by the
	// janitor (T211b, ADR 031). Zero means "use the platform default
	// from HELMDECK_ARTIFACT_TTL". Set on packs whose outputs are
	// either especially short-lived (e.g. one-off screenshots) or
	// especially valuable (e.g. rendered slide decks the user shares).
	ArtifactTTL time.Duration

	// Async, when true, tells the MCP layer to route tools/call for
	// this pack through the async job registry instead of executing
	// inline. The initial JSON-RPC response returns a SEP-1686 task
	// envelope (taskId in _meta.modelcontextprotocol.io/related-task)
	// rather than the full result, so the call completes in
	// milliseconds and never trips the client's per-request JSON-RPC
	// timeout. Clients that speak SEP-1686 then poll tasks/get under
	// the hood; older clients can poll the equivalent
	// pack.start/pack.status/pack.result trio.
	//
	// Set this on packs whose handler routinely runs longer than ~30s
	// (slides.narrate, research.deep, content.ground rewrite).
	// Short packs MUST NOT set Async — the round-trip cost outweighs
	// the benefit and the LLM has to interpret a task reference
	// instead of an immediate result.
	Async bool

	// Memory is the optional opt-in for the Universal Memory engine
	// seam (ADR 039). Nil — the default for every pack — means no
	// memory hooks run and the pack behaves exactly as it did before
	// the memory layer landed. Set it (e.g. {Cache: true, TTL: ...})
	// to enable the declarative read-through response cache.
	Memory *MemoryConfig
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

	// Progress is the raw progress callback wired by the engine when
	// a listener attached one via WithProgress. May be nil — handlers
	// should NOT call it directly; use ec.Report instead, which is
	// nil-safe and survives test fixtures that build an EC manually.
	Progress ProgressFunc

	// Memory is the namespace-scoped memory handle. NIL unless the
	// engine has a MemoryStore wired (WithMemoryStore). Handlers MUST
	// nil-check before use — the default deployment (no memory key)
	// leaves this nil so behavior is unchanged.
	Memory MemoryInterface

	// PersistentReposPath is the in-container mount point of the
	// persistent repos volume (ADR 040), or "" when no repos volume is
	// configured. Non-empty tells repo.* handlers they may persist a
	// clone across sessions under PersistentReposPath/<Caller>/<hash>;
	// empty means clones go to an ephemeral /tmp dir as before. Mirrors
	// session.Session.ReposPath, surfaced here so handlers don't reach
	// into the runtime.
	PersistentReposPath string

	// Caller is the bare authenticated subject (JWT subject, or
	// "unknown" when unauthenticated) — WITHOUT the per-session suffix
	// that ec.Memory.Namespace() carries. repo.* uses it to namespace
	// persistent clones by caller so a clone is shared across that
	// caller's sessions (the whole point of cross-session reuse), unlike
	// session-scoped memory which is deliberately not shared.
	Caller string
}

// Report sends an incremental status update to whoever is listening
// (MCP `notifications/progress`, the async job tracker, or both).
// Safe to call even when no listener is attached — the underlying
// callback is nil-checked here so handlers can sprinkle progress
// reports without ceremony. Use percentages 0-100; pct should be
// monotonically increasing but is not enforced. Heavy packs
// (slides.narrate, research.deep, content.ground) should call this
// every few seconds so MCP clients with low JSON-RPC timeouts
// (default 60s on the TS SDK) keep their per-request timer reset.
func (ec *ExecutionContext) Report(pct float64, message string) {
	if ec == nil || ec.Progress == nil {
		return
	}
	ec.Progress(pct, message)
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
	runtime    session.Runtime    // optional; nil disallows packs with NeedsSession
	cdpFactory CDPFactory         // optional; when nil, ExecutionContext.CDP is nil
	executor   session.Executor   // optional; when nil, ExecutionContext.Exec is nil
	artifacts  ArtifactStore      // optional; defaults to an in-memory store
	memory     memory.MemoryStore // optional; when nil, ExecutionContext.Memory is nil (memory disabled)
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

// WithMemoryStore wires the Universal Memory backend (ADR 039). Without
// one, ExecutionContext.Memory is nil on every run and the memory
// engine seam is inert — the default-off safety contract. Pass an
// InMemoryStore in tests or a SQLiteStore in production.
func WithMemoryStore(s memory.MemoryStore) Option { return func(e *Engine) { e.memory = s } }

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
		Progress:  progressFromContext(ctx),
		Caller:    callerFromContext(ctx),
	}
	// Persistent repos path (ADR 040): surfaced to repo.* handlers when
	// the runtime mounted a repos volume into this session. Empty when
	// no volume is configured ⇒ ephemeral /tmp clone, as before.
	if sess != nil {
		ec.PersistentReposPath = sess.ReposPath
	}
	// Memory handle: non-nil only when a MemoryStore is wired. The
	// namespace is the authenticated caller (JWT subject, "unknown"
	// when unauthenticated); when the pack acquired a session we
	// further scope it to that session so per-session memory doesn't
	// bleed across a caller's concurrent runs.
	if e.memory != nil {
		ns := callerFromContext(ctx)
		if sess != nil {
			ns += ":" + sess.ID
		}
		ec.Memory = &memoryAdapter{store: e.memory, ns: ns, ctx: ctx}
	}
	if pack.NeedsSession && e.executor != nil && sess != nil {
		sessID := sess.ID
		ec.Exec = func(ctx context.Context, req session.ExecRequest) (session.ExecResult, error) {
			return e.executor.Exec(ctx, sessID, req)
		}
	}

	// Memory cache seam (PRE). Gated by ec.Memory != nil (a store is
	// wired) AND pack.Memory.Cache (the pack opted in). Both nil/false
	// ⇒ this block is skipped entirely ⇒ zero behavior change. When a
	// fresh entry exists for sha256(input) under the pack name, return
	// it as the output and skip the handler.
	cacheEnabled := ec.Memory != nil && pack.Memory != nil && pack.Memory.Cache
	var cacheKey string
	if cacheEnabled {
		cacheKey = pack.Name + "/" + sha256hex(input)
		if ent, rerr := ec.Memory.Recall(cacheKey); rerr == nil && ent != nil {
			logger.Debug("memory cache hit", "pack", pack.Name, "key", cacheKey, "fingerprint", ent.Fingerprint)
			var sessionID string
			if sess != nil {
				sessionID = sess.ID
			}
			return &Result{
				Pack:      pack.Name,
				Version:   pack.Version,
				Output:    json.RawMessage(ent.Value),
				SessionID: sessionID,
				Duration:  e.now().Sub(start),
			}, nil
		} else if rerr != nil && !errors.Is(rerr, memory.ErrNotFound) {
			// A real backend error (not just a miss) shouldn't fail the
			// pack — log and fall through to the handler.
			logger.Warn("memory cache recall failed; running handler", "pack", pack.Name, "err", rerr)
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

	// Memory cache seam (POST). Store the successful output under the
	// cache key with the pack's TTL/category so the next identical call
	// within TTL hits the cache. Deliberately runs AFTER output-schema
	// validation so a schema-invalid output is never cached (otherwise a
	// rejected payload would be served as success on the next call,
	// bypassing validation). A store failure must not fail the pack — it
	// only forfeits the cache for this call.
	if cacheEnabled {
		cat := pack.Memory.Category
		if cat == "" {
			cat = "cache"
		}
		if serr := ec.Memory.Store(cacheKey, []byte(output),
			memory.WithTTL(pack.Memory.TTL), memory.WithCategory(cat)); serr != nil {
			logger.Warn("memory cache store failed", "pack", pack.Name, "err", serr)
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
// sha256hex returns the hex-encoded sha256 of b, used to key cache
// entries on the exact input bytes.
func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func safeInvoke(ctx context.Context, ec *ExecutionContext, h HandlerFunc) (out json.RawMessage, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("pack handler panicked: %v", r)
		}
	}()
	return h(ctx, ec)
}
