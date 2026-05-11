package api

// Adapters that wire concrete in-process services into the MCP
// PackServer's read-only resource interfaces:
//
//   - sessionListerAdapter        → helmdeck://sessions      (issue #44)
//   - voiceListerCachingAdapter   → helmdeck://voices        (issue #143)
//   - imageModelListerAdapter     → helmdeck://image-models  (issue #158)
//
// Kept narrow on purpose: PackServer doesn't need (or want) the full
// session.Runtime API (Create / Logs / Delete) — only List. Limiting
// the surface keeps MCP from becoming a back-channel for session
// mutation that would bypass the audit log on /api/v1/sessions/*.

import (
	"context"
	"sync"
	"time"

	"github.com/tosin2013/helmdeck/internal/imagemodels"
	"github.com/tosin2013/helmdeck/internal/mcp"
	"github.com/tosin2013/helmdeck/internal/session"
	"github.com/tosin2013/helmdeck/internal/vault"
	"github.com/tosin2013/helmdeck/internal/voices"
)

type sessionListerAdapter struct {
	rt session.Runtime
}

func (a sessionListerAdapter) List(ctx context.Context) ([]mcp.SessionView, error) {
	sessions, err := a.rt.List(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]mcp.SessionView, 0, len(sessions))
	for _, s := range sessions {
		views = append(views, mcp.SessionView{
			ID:        s.ID,
			Status:    string(s.Status),
			Image:     s.Spec.Image,
			CreatedAt: s.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	return views, nil
}

// voiceListerCachingAdapter wraps voices.ListVoices with the credential-
// vault lookup (so the API key never crosses the MCP wire) plus a
// 1-hour in-memory cache. The voice catalog rarely changes; the cache
// keeps `helmdeck://voices` from hitting ElevenLabs on every
// resources/read call from the agent's UI sidebar.
//
// Cache key is the credential's plaintext fingerprint (not the
// plaintext itself) so rotating the ElevenLabs key naturally
// invalidates the cache without leaking the key into the cache key.
type voiceListerCachingAdapter struct {
	vault *vault.Store
	ttl   time.Duration
	now   func() time.Time

	mu       sync.Mutex
	cachedAt time.Time
	cached   []mcp.VoiceView
	cachedFP string // fingerprint of the API key the cache was built with
}

// newVoiceListerCachingAdapter constructs the adapter. ttl=0 uses the
// 1h default; pass a small value (e.g. 1*time.Second) in tests.
func newVoiceListerCachingAdapter(v *vault.Store, ttl time.Duration) *voiceListerCachingAdapter {
	if ttl == 0 {
		ttl = time.Hour
	}
	return &voiceListerCachingAdapter{
		vault: v,
		ttl:   ttl,
		now:   func() time.Time { return time.Now() },
	}
}

func (a *voiceListerCachingAdapter) List(ctx context.Context) ([]mcp.VoiceView, error) {
	res, err := a.vault.ResolveByName(ctx, vault.Actor{Subject: "*"}, "elevenlabs-key")
	if err != nil {
		return nil, err
	}
	apiKey := string(res.Plaintext)
	fp := res.Record.Fingerprint

	a.mu.Lock()
	cacheValid := a.cachedFP == fp && a.now().Sub(a.cachedAt) < a.ttl && a.cached != nil
	if cacheValid {
		out := a.cached
		a.mu.Unlock()
		return out, nil
	}
	a.mu.Unlock()

	list, err := voices.ListVoices(ctx, apiKey)
	if err != nil {
		return nil, err
	}
	views := make([]mcp.VoiceView, 0, len(list))
	for _, v := range list {
		views = append(views, mcp.VoiceView{
			VoiceID:    v.VoiceID,
			Name:       v.Name,
			Labels:     v.Labels,
			PreviewURL: v.PreviewURL,
			Source:     v.Source,
		})
	}

	a.mu.Lock()
	a.cached = views
	a.cachedAt = a.now()
	a.cachedFP = fp
	a.mu.Unlock()

	return views, nil
}

// imageModelListerAdapter wraps internal/imagemodels for the MCP
// resource surface. No caching: the catalog is in-tree, the work is
// a slice copy + struct re-shape. Future dynamic-fetch impls can
// either replace this adapter or add caching here.
type imageModelListerAdapter struct {
	src imagemodels.Lister
}

// newImageModelListerAdapter constructs the adapter over the
// in-tree static catalog. Tests can construct directly with their
// own Lister fake.
func newImageModelListerAdapter() *imageModelListerAdapter {
	return &imageModelListerAdapter{src: imagemodels.StaticLister{}}
}

func (a *imageModelListerAdapter) List(ctx context.Context) ([]mcp.ImageModelView, error) {
	models, err := a.src.List(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]mcp.ImageModelView, 0, len(models))
	for _, m := range models {
		views = append(views, mcp.ImageModelView{
			ID:                    m.ID,
			DisplayName:           m.DisplayName,
			Provider:              m.Provider,
			Engine:                m.Engine,
			ApproxCostPerImageUSD: m.ApproxCostPerImageUSD,
			P50LatencyS:           m.P50LatencyS,
			SupportsSeed:          m.SupportsSeed,
			SupportsImageSize:     m.SupportsImageSize,
			MaxResolution:         m.MaxResolution,
			Capabilities:          m.Capabilities,
			Notes:                 m.Notes,
		})
	}
	return views, nil
}
