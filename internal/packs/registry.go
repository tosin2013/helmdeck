package packs

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// ErrPackNotFound is returned when no pack matches the requested name
// (and optional version).
var ErrPackNotFound = errors.New("pack not found")

// Registry holds the set of packs the control plane can dispatch. It
// is safe for concurrent use; T601 will hot-reload packs from disk
// against this same surface, so the locking is per-write only and
// reads never block writes longer than a map lookup.
//
// Versioning rule: a pack's Version must look like "v<int>" (matching
// the URL convention `/api/v1/packs/{name}/v{n}`). The integer is
// monotonic per name; "latest" always resolves to the highest version
// currently registered. Packs that don't care about versioning can
// register with Version="v1".
type Registry struct {
	mu sync.RWMutex
	// packs[name][version] = pack
	packs map[string]map[string]*Pack
}

// NewPackRegistry returns an empty registry.
func NewPackRegistry() *Registry {
	return &Registry{packs: make(map[string]map[string]*Pack)}
}

// Register adds (or replaces) a pack. Replacing the same (name,
// version) is intentional — pack authors hot-reload during dev — but
// the version string must be well-formed because the dispatch routes
// rely on parseVersion below.
func (r *Registry) Register(p *Pack) error {
	if p == nil {
		return errors.New("registry: nil pack")
	}
	if p.Name == "" {
		return errors.New("registry: pack name required")
	}
	if _, err := parseVersion(p.Version); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.packs[p.Name] == nil {
		r.packs[p.Name] = make(map[string]*Pack)
	}
	r.packs[p.Name][p.Version] = p
	return nil
}

// Unregister removes a single (name, version) pair. Removing the
// last version of a name also removes the name itself so List stays
// tidy.
func (r *Registry) Unregister(name, version string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if vs, ok := r.packs[name]; ok {
		delete(vs, version)
		if len(vs) == 0 {
			delete(r.packs, name)
		}
	}
}

// Get returns the pack at name@version. An empty version resolves to
// the latest registered version for that name.
func (r *Registry) Get(name, version string) (*Pack, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	vs, ok := r.packs[name]
	if !ok || len(vs) == 0 {
		return nil, ErrPackNotFound
	}
	if version == "" || version == "latest" {
		return latestVersion(vs)
	}
	p, ok := vs[version]
	if !ok {
		return nil, ErrPackNotFound
	}
	return p, nil
}

// PackInfo is the redacted view returned by List — no handler, no
// schemas. Surfacing handlers would leak Go closures over the wire,
// and schemas can be huge; clients that need either should call
// Describe (future endpoint) or read the pack source.
type PackInfo struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Versions    []string `json:"versions"`
	Latest      string   `json:"latest"`
}

// List returns one PackInfo per registered name, sorted by name. The
// version slice is sorted oldest-first so the highest version is
// always at the end (matching the human reading order).
func (r *Registry) List() []PackInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]PackInfo, 0, len(r.packs))
	for name, vs := range r.packs {
		versions := make([]string, 0, len(vs))
		for v := range vs {
			versions = append(versions, v)
		}
		sortVersions(versions)
		latest, _ := latestVersion(vs)
		desc := ""
		if latest != nil {
			desc = latest.Description
		}
		out = append(out, PackInfo{
			Name:        name,
			Description: desc,
			Versions:    versions,
			Latest:      versions[len(versions)-1],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// parseVersion accepts "v<int>" and returns the integer. Anything
// else is rejected at registration time so dispatch never has to
// guess what "1.2.3" means.
func parseVersion(v string) (int, error) {
	if !strings.HasPrefix(v, "v") {
		return 0, errors.New(`registry: version must look like "v<int>"`)
	}
	n, err := strconv.Atoi(v[1:])
	if err != nil || n < 1 {
		return 0, errors.New(`registry: version must look like "v<int>"`)
	}
	return n, nil
}

func sortVersions(versions []string) {
	sort.Slice(versions, func(i, j int) bool {
		ai, _ := parseVersion(versions[i])
		bj, _ := parseVersion(versions[j])
		return ai < bj
	})
}

func latestVersion(vs map[string]*Pack) (*Pack, error) {
	var (
		best   *Pack
		bestN  int
	)
	for v, p := range vs {
		n, err := parseVersion(v)
		if err != nil {
			continue
		}
		if best == nil || n > bestN {
			best = p
			bestN = n
		}
	}
	if best == nil {
		return nil, ErrPackNotFound
	}
	return best, nil
}
