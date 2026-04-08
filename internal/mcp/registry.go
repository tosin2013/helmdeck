package mcp

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrServerNotFound is returned when an MCP server id (or name) is
// missing from the registry.
var ErrServerNotFound = errors.New("mcp: server not found")

// ErrDuplicateName is returned by Create when name is already in use.
var ErrDuplicateName = errors.New("mcp: server name already exists")

// DefaultManifestTTL is how long a fetched manifest stays "fresh"
// in the cache before the registry will refetch on access. Manual
// Refresh always re-fetches regardless of TTL.
const DefaultManifestTTL = 1 * time.Hour

// Registry is the persistent MCP server catalog. CRUD operations
// hit the database directly; manifest fetches go through Adapter.
type Registry struct {
	db  *sql.DB
	now func() time.Time
	ttl time.Duration

	// adapterFactory builds an Adapter from a Server record. It is
	// injectable so tests can supply a fake adapter without spawning
	// real subprocesses or opening real sockets.
	adapterFactory func(*Server) (Adapter, error)

	mu sync.Mutex // serialises Refresh writes
}

// NewRegistry returns a Registry backed by db. db must already be
// open and migrated (the 0003 migration creates mcp_servers).
func NewRegistry(db *sql.DB) *Registry {
	return &Registry{
		db:             db,
		now:            func() time.Time { return time.Now().UTC() },
		ttl:            DefaultManifestTTL,
		adapterFactory: defaultAdapterFactory,
	}
}

// WithAdapterFactory swaps in a custom adapter factory. Test-only.
func (r *Registry) WithAdapterFactory(f func(*Server) (Adapter, error)) *Registry {
	r.adapterFactory = f
	return r
}

// CreateInput captures the writable fields on Create/Update. Keeping
// it separate from Server means callers can't accidentally pass an
// id or timestamps and have them silently dropped.
type CreateInput struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Transport   Transport       `json:"transport"`
	Config      json.RawMessage `json:"config"`
	Enabled     bool            `json:"enabled"`
}

// Create inserts a new MCP server.
func (r *Registry) Create(ctx context.Context, in CreateInput) (*Server, error) {
	if in.Name == "" || in.Transport == "" || len(in.Config) == 0 {
		return nil, errors.New("mcp: name, transport, and config required")
	}
	if !validTransport(in.Transport) {
		return nil, fmt.Errorf("mcp: invalid transport %q", in.Transport)
	}
	if err := validateConfig(in.Transport, in.Config); err != nil {
		return nil, err
	}
	id, err := newServerID()
	if err != nil {
		return nil, err
	}
	now := r.now()
	s := &Server{
		ID:          id,
		Name:        in.Name,
		Description: in.Description,
		Transport:   in.Transport,
		Config:      in.Config,
		Enabled:     in.Enabled,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	_, err = r.db.ExecContext(ctx, `
		INSERT INTO mcp_servers (id, name, description, transport, config_json, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.Name, s.Description, string(s.Transport), string(s.Config), boolToInt(s.Enabled),
		s.CreatedAt.Format(time.RFC3339Nano), s.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrDuplicateName
		}
		return nil, fmt.Errorf("mcp: insert: %w", err)
	}
	return s, nil
}

// Get returns one server by id.
func (r *Registry) Get(ctx context.Context, id string) (*Server, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, name, description, transport, config_json, enabled,
		       manifest_json, cache_expires_at, created_at, updated_at
		  FROM mcp_servers WHERE id = ?`, id)
	return scanServer(row)
}

// List returns every server, ordered by name.
func (r *Registry) List(ctx context.Context) ([]*Server, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, description, transport, config_json, enabled,
		       manifest_json, cache_expires_at, created_at, updated_at
		  FROM mcp_servers ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("mcp: list: %w", err)
	}
	defer rows.Close()
	var out []*Server
	for rows.Next() {
		s, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Update mutates the writable fields. The id, manifest cache, and
// timestamps are managed by the registry, not the caller.
func (r *Registry) Update(ctx context.Context, id string, in CreateInput) (*Server, error) {
	existing, err := r.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if in.Name == "" {
		in.Name = existing.Name
	}
	if in.Transport == "" {
		in.Transport = existing.Transport
	}
	if !validTransport(in.Transport) {
		return nil, fmt.Errorf("mcp: invalid transport %q", in.Transport)
	}
	if len(in.Config) == 0 {
		in.Config = existing.Config
	} else if err := validateConfig(in.Transport, in.Config); err != nil {
		return nil, err
	}
	existing.Name = in.Name
	existing.Description = in.Description
	existing.Transport = in.Transport
	existing.Config = in.Config
	existing.Enabled = in.Enabled
	existing.UpdatedAt = r.now()
	// Updating config invalidates the cached manifest — what we
	// fetched before may have been against an entirely different
	// command line.
	existing.Manifest = nil
	existing.CachedAt = nil
	_, err = r.db.ExecContext(ctx, `
		UPDATE mcp_servers
		   SET name=?, description=?, transport=?, config_json=?, enabled=?,
		       manifest_json=NULL, cache_expires_at=NULL, updated_at=?
		 WHERE id=?`,
		existing.Name, existing.Description, string(existing.Transport),
		string(existing.Config), boolToInt(existing.Enabled),
		existing.UpdatedAt.Format(time.RFC3339Nano), id,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrDuplicateName
		}
		return nil, fmt.Errorf("mcp: update: %w", err)
	}
	return existing, nil
}

// Delete removes a server by id.
func (r *Registry) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM mcp_servers WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("mcp: delete: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrServerNotFound
	}
	return nil
}

// Manifest returns the cached manifest for id, fetching from the
// upstream MCP server if no cache exists or the cache has expired.
// Pass force=true to bypass the TTL check.
func (r *Registry) Manifest(ctx context.Context, id string, force bool) (*Manifest, error) {
	s, err := r.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !force && s.Manifest != nil && s.CachedAt != nil {
		// CachedAt + ttl == expiry time
		if r.now().Before(s.CachedAt.Add(r.ttl)) {
			return s.Manifest, nil
		}
	}
	if !s.Enabled {
		return nil, errors.New("mcp: server is disabled")
	}
	adapter, err := r.adapterFactory(s)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	manifest, err := adapter.FetchManifest(ctx)
	if err != nil {
		return nil, fmt.Errorf("mcp: fetch %s: %w", s.Name, err)
	}
	manifestJSON, _ := json.Marshal(manifest)
	now := r.now()
	if _, err := r.db.ExecContext(ctx, `
		UPDATE mcp_servers SET manifest_json=?, cache_expires_at=? WHERE id=?`,
		string(manifestJSON), now.Add(r.ttl).Format(time.RFC3339Nano), id,
	); err != nil {
		// Cache write failed but the manifest is already in hand —
		// return it. The next call will refetch.
		_ = err
	}
	return manifest, nil
}

// --- internal helpers --------------------------------------------------

func defaultAdapterFactory(s *Server) (Adapter, error) {
	switch s.Transport {
	case TransportStdio:
		var cfg StdioConfig
		if err := json.Unmarshal(s.Config, &cfg); err != nil {
			return nil, fmt.Errorf("mcp: stdio config: %w", err)
		}
		return NewStdioAdapter(cfg), nil
	case TransportSSE:
		var cfg SSEConfig
		if err := json.Unmarshal(s.Config, &cfg); err != nil {
			return nil, fmt.Errorf("mcp: sse config: %w", err)
		}
		return NewSSEAdapter(cfg), nil
	case TransportWebSocket:
		var cfg WebSocketConfig
		if err := json.Unmarshal(s.Config, &cfg); err != nil {
			return nil, fmt.Errorf("mcp: websocket config: %w", err)
		}
		return NewWebSocketAdapter(cfg), nil
	}
	return nil, fmt.Errorf("mcp: unknown transport %q", s.Transport)
}

func validTransport(t Transport) bool {
	switch t {
	case TransportStdio, TransportSSE, TransportWebSocket:
		return true
	}
	return false
}

func validateConfig(t Transport, raw json.RawMessage) error {
	switch t {
	case TransportStdio:
		var c StdioConfig
		if err := json.Unmarshal(raw, &c); err != nil {
			return fmt.Errorf("mcp: stdio config: %w", err)
		}
		if c.Command == "" {
			return errors.New("mcp: stdio config requires command")
		}
	case TransportSSE:
		var c SSEConfig
		if err := json.Unmarshal(raw, &c); err != nil {
			return fmt.Errorf("mcp: sse config: %w", err)
		}
		if c.URL == "" {
			return errors.New("mcp: sse config requires url")
		}
	case TransportWebSocket:
		var c WebSocketConfig
		if err := json.Unmarshal(raw, &c); err != nil {
			return fmt.Errorf("mcp: websocket config: %w", err)
		}
		if c.URL == "" {
			return errors.New("mcp: websocket config requires url")
		}
	}
	return nil
}

func newServerID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "mcp_" + hex.EncodeToString(b[:]), nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanServer(r scanner) (*Server, error) {
	var (
		s              Server
		transportStr   string
		configStr      string
		enabledInt     int
		manifestStr    sql.NullString
		cacheExpiresAt sql.NullString
		createdAt      string
		updatedAt      string
	)
	if err := r.Scan(&s.ID, &s.Name, &s.Description, &transportStr, &configStr, &enabledInt,
		&manifestStr, &cacheExpiresAt, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrServerNotFound
		}
		return nil, err
	}
	s.Transport = Transport(transportStr)
	s.Config = json.RawMessage(configStr)
	s.Enabled = enabledInt != 0
	s.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	s.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	if manifestStr.Valid && manifestStr.String != "" {
		var m Manifest
		if err := json.Unmarshal([]byte(manifestStr.String), &m); err == nil {
			s.Manifest = &m
		}
	}
	if cacheExpiresAt.Valid && cacheExpiresAt.String != "" {
		// We store expiry, not "cached at"; reconstruct cached_at as
		// expiry - ttl. Approximate but good enough for surfacing
		// "when did we last hit this server".
		t, err := time.Parse(time.RFC3339Nano, cacheExpiresAt.String)
		if err == nil {
			cachedAt := t.Add(-DefaultManifestTTL)
			s.CachedAt = &cachedAt
		}
	}
	return &s, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return contains(msg, "UNIQUE constraint failed") || contains(msg, "constraint failed: UNIQUE")
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
