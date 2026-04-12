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

	// defaultImage is the fallback sidecar image used when neither
	// the per-spec Image field nor the runtime-level WithImage option
	// is set. Operators set HELMDECK_SIDECAR_IMAGE in the control
	// plane env to override this without recompiling — main.go wires
	// the env var to WithImage. The hardcoded default points at the
	// published ghcr.io image so a fresh `helmdeck install` against
	// a remote stack pulls it automatically; local builds via
	// `make sidecar-build` produce `helmdeck-sidecar:dev` and require
	// the env var to be set (the install script handles this).
	defaultImage       = "ghcr.io/tosin2013/helmdeck-sidecar:latest"
	defaultMemoryLimit = "1g"
	defaultSHMSize     = "2g"
	defaultTimeout     = 5 * time.Minute
	cdpPort            = "9222"

	// playwrightMCPPort is the sidecar port Playwright MCP binds to when
	// HELMDECK_PLAYWRIGHT_MCP_ENABLED=true (T807a / ADR 035). The sidecar
	// entrypoint starts `npx @playwright/mcp --host 0.0.0.0 --port 8931`
	// after Chromium is live, attached to the same Chromium process via
	// CDP on 127.0.0.1:9222. The control plane reaches this port over
	// the internal baas-net bridge — it is not published to the host.
	playwrightMCPPort = "8931"

	// defaultPidsLimit is the T509 sandbox baseline cap on processes
	// per session container. 1024 is enough for headless Chromium
	// (~150 processes under normal load) plus xdotool/scrot/socat
	// helpers and a couple of pack worker spawns, but tight enough
	// that a fork bomb cannot exhaust the host's PID table.
	defaultPidsLimit int64 = 1024
)

// Runtime is the Docker SDK implementation of [session.Runtime]. It is safe
// for concurrent use; all access to the in-memory session table is guarded
// by a mutex.
type Runtime struct {
	cli     *client.Client
	network string

	// T509 sandbox config. Both fields default to safe values when
	// unset (PidsLimit=defaultPidsLimit, SeccompProfile=""). Operators
	// who want a custom seccomp profile path or a different fork-bomb
	// cap override via Option setters from cmd/control-plane.
	pidsLimit      int64
	seccompProfile string
	// imageOverride is the value of HELMDECK_SIDECAR_IMAGE wired
	// through WithImage. Empty means "use the per-spec Image, falling
	// back to defaultImage". Set means "use this image when the per-
	// spec Image is empty". Per-pack overrides via SessionSpec.Image
	// always win over both.
	imageOverride string

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

// WithPidsLimit caps the number of processes a session container can
// fork (T509 sandbox baseline). Defaults to 1024 — generous enough
// for Chromium + xdotool + a couple of pack worker processes, tight
// enough that a fork bomb in a pack handler can't take down the
// host. Set to 0 (or negative) to disable the cap entirely.
func WithPidsLimit(n int64) Option {
	return func(r *Runtime) { r.pidsLimit = n }
}

// WithImage sets the default sidecar image used for sessions whose
// SessionSpec.Image field is empty. Operators wire this to the
// HELMDECK_SIDECAR_IMAGE env var in cmd/control-plane so they can
// point at a locally-built image (e.g. helmdeck-sidecar:dev) without
// recompiling the control plane. Empty image string is a no-op.
func WithImage(image string) Option {
	return func(r *Runtime) {
		if image != "" {
			r.imageOverride = image
		}
	}
}

// WithSeccompProfile points the runtime at a custom seccomp profile
// JSON path. The path is passed verbatim to docker as
// `seccomp=<path>` in HostConfig.SecurityOpt. Empty string means
// "use docker's built-in default profile" — which is the right
// answer for most deployments because the default profile is
// curated upstream and known-compatible with Chromium.
//
// Override this only when you have a tighter custom profile that
// you've validated against the helmdeck pack catalog. See
// docs/SECURITY-HARDENING.md for the runbook.
func WithSeccompProfile(path string) Option {
	return func(r *Runtime) { r.seccompProfile = path }
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
		cli:       cli,
		sessions:  make(map[string]*session.Session),
		pidsLimit: defaultPidsLimit,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r, nil
}

// Create spawns a new browser sidecar container.
func (r *Runtime) Create(ctx context.Context, spec session.Spec) (*session.Session, error) {
	resolved := r.withDefaults(spec)

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
	hostCfg := r.buildHostConfig(memBytes, shmBytes, resolved.CPULimit)

	cfg := &container.Config{
		Image: resolved.Image,
		Env:   envSlice(resolved.Env),
		Labels: map[string]string{
			LabelManaged:   "true",
			LabelSessionID: id,
			LabelLabel:     resolved.Label,
		},
		ExposedPorts: nat.PortSet{
			nat.Port(cdpPort + "/tcp"):           {},
			nat.Port(playwrightMCPPort + "/tcp"): {},
		},
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

	// Inspect the running container to learn its IP on the attached network
	// so the control plane can reach CDP. With WithNetwork the IP comes from
	// NetworkSettings.Networks[<name>].IPAddress; on the default bridge it
	// comes from NetworkSettings.IPAddress.
	insp, err := r.cli.ContainerInspect(ctx, created.ID)
	if err != nil {
		_ = r.cli.ContainerRemove(context.Background(), created.ID, container.RemoveOptions{Force: true})
		return nil, fmt.Errorf("container inspect: %w", err)
	}
	cdpEndpoint := buildCDPEndpoint(insp, r.network, cdpPort)
	playwrightMCPEndpoint := buildPlaywrightMCPEndpoint(insp, r.network, playwrightMCPPort, resolved.Env)

	s := &session.Session{
		ID:                    id,
		ContainerID:           created.ID,
		Status:                session.StatusRunning,
		CreatedAt:             time.Now().UTC(),
		Spec:                  resolved,
		CDPEndpoint:           cdpEndpoint,
		PlaywrightMCPEndpoint: playwrightMCPEndpoint,
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

func (r *Runtime) withDefaults(in session.Spec) session.Spec {
	out := in
	if out.Image == "" {
		if r != nil && r.imageOverride != "" {
			out.Image = r.imageOverride
		} else {
			out.Image = defaultImage
		}
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

// buildCDPEndpoint inspects a container and returns http://<ip>:<port>,
// preferring the explicit network the runtime was configured with and
// falling back to the default bridge network's IP if none was set.
// Returns "" if no usable IP can be found (test images that don't
// expose any network at all).
func buildCDPEndpoint(insp container.InspectResponse, network, port string) string {
	var ip string
	if insp.NetworkSettings == nil {
		return ""
	}
	if network != "" {
		if n, ok := insp.NetworkSettings.Networks[network]; ok && n != nil {
			ip = n.IPAddress
		}
	}
	if ip == "" {
		ip = insp.NetworkSettings.IPAddress
	}
	if ip == "" {
		for _, n := range insp.NetworkSettings.Networks {
			if n != nil && n.IPAddress != "" {
				ip = n.IPAddress
				break
			}
		}
	}
	if ip == "" {
		return ""
	}
	return "http://" + ip + ":" + port
}

// buildPlaywrightMCPEndpoint returns the per-session Playwright MCP
// endpoint URL (T807a / ADR 035), or "" if the operator has opted out
// via HELMDECK_PLAYWRIGHT_MCP_ENABLED=false on the spec env.
//
// The shape matches `@playwright/mcp --host 0.0.0.0 --port 8931`'s
// default SSE mount point: http://<container-ip>:8931/sse. We reuse
// buildCDPEndpoint's IP-discovery logic because both endpoints live
// on the same container IP — only the port and path differ.
func buildPlaywrightMCPEndpoint(insp container.InspectResponse, network, port string, env map[string]string) string {
	if v, ok := env["HELMDECK_PLAYWRIGHT_MCP_ENABLED"]; ok && v == "false" {
		return ""
	}
	base := buildCDPEndpoint(insp, network, port)
	if base == "" {
		return ""
	}
	return base + "/sse"
}

// errImageMissing is reserved for richer image-resolution errors in T104.
var errImageMissing = errors.New("docker session runtime: image missing")

var _ = errImageMissing

// buildHostConfig assembles the docker HostConfig the runtime hands
// to ContainerCreate. Factored out so the T509 sandbox spec can be
// unit-tested in isolation without a real docker daemon — see
// runtime_internal_test.go.
//
// Sandbox baseline (T509):
//
//   - CapDrop ALL + CapAdd SYS_ADMIN — minimum cap set Chromium's
//     user-namespace sandbox needs (ADR 011 standard tier).
//   - no-new-privileges — container can never escalate via setuid.
//   - seccomp=<profile> — only when WithSeccompProfile is set; an
//     empty path falls back to docker's curated default profile,
//     which is Chromium-safe and what most operators want.
//   - PidsLimit — hard cap on fork count; defaults to 1024 (the
//     defaultPidsLimit constant), tunable via WithPidsLimit.
//   - ReadonlyRootfs is still false because Chromium needs /home
//     writable; future hardening will mount /home as a tmpfs.
func (r *Runtime) buildHostConfig(memBytes, shmBytes int64, cpuLimit float64) *container.HostConfig {
	securityOpt := []string{"no-new-privileges:true"}
	if r.seccompProfile != "" {
		securityOpt = append(securityOpt, "seccomp="+r.seccompProfile)
	}
	pidsLimit := r.pidsLimit
	hostCfg := &container.HostConfig{
		Resources: container.Resources{
			Memory:    memBytes,
			NanoCPUs:  int64(cpuLimit * 1e9),
			PidsLimit: &pidsLimit,
		},
		ShmSize:        shmBytes,
		ReadonlyRootfs: false,
		AutoRemove:     false,
		SecurityOpt:    securityOpt,
		CapDrop:        []string{"ALL"},
		CapAdd:         []string{"SYS_ADMIN"},
	}
	if r.network != "" {
		hostCfg.NetworkMode = container.NetworkMode(r.network)
	}
	return hostCfg
}
