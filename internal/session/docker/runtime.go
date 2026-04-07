// Package docker is the Docker-Engine-backed [session.Runtime] used in the
// helmdeck Compose tier. The Kubernetes backend lives in a sibling package
// and is wired in T701 (ADR 009).
package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/docker/go-units"
	"github.com/google/uuid"

	"github.com/tosin2013/helmdeck/internal/session"
)

const (
	// LabelManaged tags every container the runtime creates so List can
	// re-discover sessions after a control-plane restart and so the watchdog
	// can prune orphans.
	LabelManaged = "helmdeck.managed"
	// LabelSessionID stores the runtime-assigned session UUID.
	LabelSessionID = "helmdeck.session_id"
	// LabelLabel stores the operator-supplied human label.
	LabelLabel = "helmdeck.label"

	defaultImage       = "ghcr.io/tosin2013/helmdeck-sidecar:latest"
	defaultMemoryLimit = "1g"
	defaultSHMSize     = "2g"
	defaultTimeout     = 5 * time.Minute
	cdpPort            = "9222"
)

// Runtime is the Docker SDK implementation of [session.Runtime]. It is safe
// for concurrent use; all access to the in-memory session table is guarded
// by a mutex.
type Runtime struct {
	cli     *client.Client
	network string

	mu       sync.RWMutex
	sessions map[string]*session.Session // id → session view
}

// Option configures a Runtime at construction time.
type Option func(*Runtime)

// WithNetwork attaches every spawned session container to the named Docker
// network. Use this to keep sessions on the internal baas-net bridge so
// the CDP port is not reachable from the host (ADR 010 security model).
func WithNetwork(name string) Option {
	return func(r *Runtime) { r.network = name }
}

// New constructs a Runtime backed by the local Docker daemon (or whatever
// DOCKER_HOST points at). The caller owns the returned Runtime and must
// call Close when finished.
func New(opts ...Option) (*Runtime, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker session runtime: %w", err)
	}
	r := &Runtime{
		cli:      cli,
		sessions: make(map[string]*session.Session),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r, nil
}

// Create spawns a new browser sidecar container.
func (r *Runtime) Create(ctx context.Context, spec session.Spec) (*session.Session, error) {
	resolved := withDefaults(spec)

	memBytes, err := units.RAMInBytes(resolved.MemoryLimit)
	if err != nil {
		return nil, fmt.Errorf("memory_limit %q: %w", resolved.MemoryLimit, err)
	}
	shmBytes, err := units.RAMInBytes(resolved.SHMSize)
	if err != nil {
		return nil, fmt.Errorf("shm_size %q: %w", resolved.SHMSize, err)
	}

	// Pull the image if it's not already present locally. Best-effort: if
	// the registry is unreachable but the image exists locally, Create will
	// proceed; if neither, ContainerCreate will surface the error.
	if err := r.ensureImage(ctx, resolved.Image); err != nil {
		// fall through; ContainerCreate will fail visibly if the image is missing
		_ = err
	}

	id := uuid.NewString()
	hostCfg := &container.HostConfig{
		Resources: container.Resources{
			Memory:   memBytes,
			NanoCPUs: int64(resolved.CPULimit * 1e9),
		},
		ShmSize:        shmBytes,
		ReadonlyRootfs: false,
		AutoRemove:     false, // we control teardown so we can fetch logs after exit
		SecurityOpt:    []string{"no-new-privileges:true"},
	}
	if r.network != "" {
		hostCfg.NetworkMode = container.NetworkMode(r.network)
	}

	cfg := &container.Config{
		Image: resolved.Image,
		Env:   envSlice(resolved.Env),
		Labels: map[string]string{
			LabelManaged:   "true",
			LabelSessionID: id,
			LabelLabel:     resolved.Label,
		},
		ExposedPorts: nat.PortSet{nat.Port(cdpPort + "/tcp"): {}},
	}

	created, err := r.cli.ContainerCreate(ctx, cfg, hostCfg, nil, nil, "helmdeck-session-"+id)
	if err != nil {
		return nil, fmt.Errorf("container create: %w", err)
	}
	if err := r.cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		// Best-effort cleanup on failed start so we don't leak the container.
		_ = r.cli.ContainerRemove(context.Background(), created.ID, container.RemoveOptions{Force: true})
		return nil, fmt.Errorf("container start: %w", err)
	}

	s := &session.Session{
		ID:          id,
		ContainerID: created.ID,
		Status:      session.StatusRunning,
		CreatedAt:   time.Now().UTC(),
		Spec:        resolved,
		// CDPEndpoint is populated in T106 by the cdp package after probing
		// /json/version on port 9222. T103 stops at "container is up".
		CDPEndpoint: "",
	}

	r.mu.Lock()
	r.sessions[id] = s
	r.mu.Unlock()

	return s, nil
}

// Get returns the current snapshot of a session.
func (r *Runtime) Get(ctx context.Context, id string) (*session.Session, error) {
	r.mu.RLock()
	s, ok := r.sessions[id]
	r.mu.RUnlock()
	if !ok {
		return nil, session.ErrSessionNotFound
	}
	// Refresh status from the daemon so callers see real lifecycle state.
	insp, err := r.cli.ContainerInspect(ctx, s.ContainerID)
	if err != nil {
		if client.IsErrNotFound(err) {
			return nil, session.ErrSessionNotFound
		}
		return nil, fmt.Errorf("inspect: %w", err)
	}
	out := *s
	switch {
	case insp.State.Running:
		out.Status = session.StatusRunning
	case insp.State.ExitCode != 0:
		out.Status = session.StatusError
	default:
		out.Status = session.StatusTerminated
	}
	return &out, nil
}

// List returns every session this runtime currently tracks.
func (r *Runtime) List(ctx context.Context) ([]*session.Session, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*session.Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		cp := *s
		out = append(out, &cp)
	}
	return out, nil
}

// Logs streams the container's combined stdout+stderr.
func (r *Runtime) Logs(ctx context.Context, id string) (io.ReadCloser, error) {
	r.mu.RLock()
	s, ok := r.sessions[id]
	r.mu.RUnlock()
	if !ok {
		return nil, session.ErrSessionNotFound
	}
	return r.cli.ContainerLogs(ctx, s.ContainerID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Tail:       "all",
	})
}

// Terminate stops and removes the session container. Idempotent.
func (r *Runtime) Terminate(ctx context.Context, id string) error {
	r.mu.Lock()
	s, ok := r.sessions[id]
	if ok {
		delete(r.sessions, id)
	}
	r.mu.Unlock()
	if !ok {
		return nil
	}
	timeout := 5
	stopErr := r.cli.ContainerStop(ctx, s.ContainerID, container.StopOptions{Timeout: &timeout})
	if stopErr != nil && !client.IsErrNotFound(stopErr) {
		return fmt.Errorf("stop: %w", stopErr)
	}
	rmErr := r.cli.ContainerRemove(ctx, s.ContainerID, container.RemoveOptions{Force: true, RemoveVolumes: true})
	if rmErr != nil && !client.IsErrNotFound(rmErr) {
		return fmt.Errorf("remove: %w", rmErr)
	}
	return nil
}

// Close releases the underlying Docker client.
func (r *Runtime) Close() error { return r.cli.Close() }

// PruneOrphans removes any helmdeck-managed containers the daemon still
// holds that are no longer in our session table. Called on startup so a
// crashed control plane does not leak browser containers across restarts.
func (r *Runtime) PruneOrphans(ctx context.Context) error {
	args := filters.NewArgs(filters.Arg("label", LabelManaged+"=true"))
	containers, err := r.cli.ContainerList(ctx, container.ListOptions{All: true, Filters: args})
	if err != nil {
		return fmt.Errorf("list orphans: %w", err)
	}
	r.mu.RLock()
	known := make(map[string]struct{}, len(r.sessions))
	for _, s := range r.sessions {
		known[s.ContainerID] = struct{}{}
	}
	r.mu.RUnlock()
	var firstErr error
	for _, c := range containers {
		if _, ok := known[c.ID]; ok {
			continue
		}
		if err := r.cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true, RemoveVolumes: true}); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (r *Runtime) ensureImage(ctx context.Context, ref string) error {
	if _, _, err := r.cli.ImageInspectWithRaw(ctx, ref); err == nil {
		return nil
	}
	rc, err := r.cli.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return err
	}
	defer rc.Close()
	_, err = io.Copy(io.Discard, rc) // drain pull progress
	return err
}

func withDefaults(in session.Spec) session.Spec {
	out := in
	if out.Image == "" {
		out.Image = defaultImage
	}
	if out.MemoryLimit == "" {
		out.MemoryLimit = defaultMemoryLimit
	}
	if out.SHMSize == "" {
		out.SHMSize = defaultSHMSize
	}
	if out.CPULimit == 0 {
		out.CPULimit = 1.0
	}
	if out.Timeout == 0 {
		out.Timeout = defaultTimeout
	}
	return out
}

func envSlice(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

// compile-time interface check
var _ session.Runtime = (*Runtime)(nil)

// guard against an unused import warning if the strconv import is dropped later
var _ = strconv.Itoa

// errImageMissing is reserved for richer image-resolution errors in T104.
var errImageMissing = errors.New("docker session runtime: image missing")

var _ = errImageMissing
