// Package session defines the platform-agnostic browser-session contract
// that the helmdeck control plane uses to manage ephemeral browser
// containers. Concrete backends (Docker, Kubernetes client-go, Firecracker)
// live in subpackages and satisfy [Runtime].
//
// See ADR 001 (sidecar pattern), ADR 004 (ephemeral sessions),
// ADR 009 (dual-tier deployment), ADR 011 (isolation tiers).
package session

import (
	"context"
	"errors"
	"io"
	"time"
)

// Status is the lifecycle state of a browser session.
type Status string

const (
	StatusPending    Status = "pending"
	StatusRunning    Status = "running"
	StatusTerminated Status = "terminated"
	StatusError      Status = "error"
)

// Spec describes a session the caller wants to create.
//
// Zero-valued fields fall back to runtime defaults documented per backend;
// the control plane sets explicit values from the JWT context (ADR 011) so
// the runtime never has to guess at security policy.
type Spec struct {
	// Image is the browser sidecar image (default decided by runtime).
	Image string

	// Label is a human-friendly identifier surfaced in logs and the UI.
	Label string

	// MemoryLimit is the hard memory cap (e.g. "1g", "2g").
	MemoryLimit string

	// SHMSize is the size of /dev/shm; Chromium needs ≥1g for SPA workloads.
	SHMSize string

	// CPULimit is fractional cores (e.g. 1.0, 0.5).
	CPULimit float64

	// Timeout is the wall-clock lifetime after which the watchdog recycles.
	// Zero means "use runtime default" (5m).
	Timeout time.Duration

	// MaxTasks caps the number of CDP commands before forced recycle. Zero
	// disables the cap.
	MaxTasks int

	// Env is extra environment variables passed into the session container.
	Env map[string]string
}

// Session is the runtime-observable view of a created session.
type Session struct {
	ID          string // runtime-assigned UUID
	ContainerID string // backend-specific container/pod identifier
	Status      Status // current lifecycle status
	CDPEndpoint string // ws://host:port/devtools/browser/... reachable from the control plane

	// PlaywrightMCPEndpoint is the per-session SSE URL of the
	// @playwright/mcp server bundled in the sidecar (T807a / ADR 035).
	// Shape: http://<container-ip>:8931/sse. Empty string means either
	// the sidecar was built without Playwright MCP (old image) or the
	// operator disabled it via HELMDECK_PLAYWRIGHT_MCP_ENABLED=false.
	// Packs that drive Playwright MCP (e.g. `web.test`, T807e) read
	// this field to find the per-session endpoint; there is no entry
	// in the external mcp.Registry because that registry is for
	// operator-configured MCP servers, not auto-launched sidecar
	// children.
	PlaywrightMCPEndpoint string

	CreatedAt time.Time // session creation timestamp (UTC)
	Spec      Spec      // the spec the session was created with
}

// Runtime is the contract every backend (Docker, Kubernetes, Firecracker)
// must satisfy. It is intentionally minimal: pack execution and CDP I/O
// live in higher layers (internal/cdp, internal/packs).
type Runtime interface {
	// Create spawns a new session container per spec and returns once the
	// container reports a reachable CDP endpoint or the context expires.
	Create(ctx context.Context, spec Spec) (*Session, error)

	// Get returns the current view of a session. Returns ErrSessionNotFound
	// if the session was never created or has already been pruned.
	Get(ctx context.Context, id string) (*Session, error)

	// List returns every live session known to the runtime.
	List(ctx context.Context) ([]*Session, error)

	// Logs streams the session container's stdout+stderr. The reader is
	// closed when the context is canceled or the session terminates.
	Logs(ctx context.Context, id string) (io.ReadCloser, error)

	// Terminate stops and removes the session container. Idempotent: a
	// second call on an already-terminated session returns nil.
	Terminate(ctx context.Context, id string) error

	// Close releases any backend resources (Docker client, K8s informers).
	Close() error
}

// ErrSessionNotFound is returned by Runtime methods when the requested
// session ID is unknown to the backend.
var ErrSessionNotFound = errors.New("session: not found")
