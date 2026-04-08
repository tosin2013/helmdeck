package gateway

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Trigger names a class of upstream failure that should cause the chain
// to advance to the next fallback. ADR 005 enumerates exactly these
// three because they cover the failure modes operators can act on
// without per-provider knowledge: hitting a quota, an outage, or a
// stuck request.
type Trigger string

const (
	// TriggerRateLimit fires on HTTP 429 from a provider.
	TriggerRateLimit Trigger = "rate_limit"
	// TriggerError fires on any non-timeout, non-rate-limit error
	// (5xx, network failure, decode error, unknown provider).
	TriggerError Trigger = "error"
	// TriggerTimeout fires when the request context's deadline is hit
	// or the upstream returns a gateway-timeout style status.
	TriggerTimeout Trigger = "timeout"
)

// Rule is one entry in a Chain's routing table. The map key in
// Chain.rules is Primary, so a single primary model has exactly one
// rule — operators define a separate rule per upstream they want to
// protect. Fallbacks are tried in order; an empty Triggers slice means
// "any failure" (equivalent to all three triggers).
type Rule struct {
	Primary   string    `json:"primary"`
	Fallbacks []string  `json:"fallbacks"`
	Triggers  []Trigger `json:"triggers,omitempty"`
}

// Chain wraps a Registry with declarative fallback rules. It satisfies
// Dispatcher, so swapping a bare Registry for a Chain in the HTTP
// handler is a one-line change at wire-up time.
//
// When a request arrives for `provider/model`, Chain looks for a rule
// keyed by that exact string and, on a triggering failure from the
// primary, walks each fallback in order until one succeeds or the list
// is exhausted. Models without a rule pass through unchanged — the
// chain is opt-in per primary, not a global wrapper.
type Chain struct {
	reg   *Registry
	mu    sync.RWMutex
	rules map[string]Rule
}

// NewChain returns an empty chain backed by reg. Add rules with
// SetRule before serving traffic; rules can be replaced at runtime
// without restarting the control plane (the lock is per-write only).
func NewChain(reg *Registry) *Chain {
	return &Chain{reg: reg, rules: make(map[string]Rule)}
}

// SetRule installs or replaces the rule for r.Primary. The rule is
// validated only loosely — referenced fallback models do not need to
// exist at install time because providers may register lazily.
func (c *Chain) SetRule(r Rule) error {
	if r.Primary == "" {
		return errors.New("chain: primary required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rules[r.Primary] = r
	return nil
}

// DeleteRule removes the rule for primary if present.
func (c *Chain) DeleteRule(primary string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.rules, primary)
}

// Rules returns a copy of the current rule set in unspecified order.
func (c *Chain) Rules() []Rule {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Rule, 0, len(c.rules))
	for _, r := range c.rules {
		out = append(out, r)
	}
	return out
}

// Dispatch routes req through the chain. If req.Model has no rule it
// is dispatched directly to the registry. Otherwise the primary is
// tried first; on a triggering failure each fallback is attempted in
// order. The returned response always reflects the model that actually
// served the request, so clients can tell when failover occurred.
func (c *Chain) Dispatch(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	c.mu.RLock()
	rule, ok := c.rules[req.Model]
	c.mu.RUnlock()
	if !ok {
		return c.reg.Dispatch(ctx, req)
	}

	candidates := append([]string{rule.Primary}, rule.Fallbacks...)
	var lastErr error
	for i, model := range candidates {
		// Stop walking if the caller's context is already dead — no
		// point trying further upstreams once the deadline is gone.
		if err := ctx.Err(); err != nil {
			return ChatResponse{}, err
		}

		attempt := req
		attempt.Model = model
		resp, err := c.reg.Dispatch(ctx, attempt)
		if err == nil {
			return resp, nil
		}
		lastErr = err

		// On the last candidate there is nowhere to fall through to;
		// surface the error verbatim.
		if i == len(candidates)-1 {
			break
		}

		// Only advance if this error matches a configured trigger.
		// An empty Triggers slice means "advance on anything" — that
		// is the most useful default for an operator who just wants
		// any failover at all.
		if !triggerMatches(err, rule.Triggers) {
			break
		}
	}
	return ChatResponse{}, fmt.Errorf("chain exhausted for %s: %w", rule.Primary, lastErr)
}

// AllModels delegates to the underlying registry. Fallback rules don't
// alter what's listed in /v1/models — clients still address primaries
// directly, and the chain decides at request time whether to fail over.
func (c *Chain) AllModels(ctx context.Context) ([]Model, error) {
	return c.reg.AllModels(ctx)
}

// classifyError reports which trigger an error matches. Order matters:
// timeout is checked before generic error so a context.DeadlineExceeded
// is not bucketed as a plain error.
func classifyError(err error) Trigger {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return TriggerTimeout
	}
	var perr *providerError
	if errors.As(err, &perr) {
		switch {
		case perr.StatusCode == 429:
			return TriggerRateLimit
		case perr.StatusCode == 504 || perr.StatusCode == 408:
			return TriggerTimeout
		}
	}
	return TriggerError
}

func triggerMatches(err error, triggers []Trigger) bool {
	if len(triggers) == 0 {
		return true
	}
	got := classifyError(err)
	for _, t := range triggers {
		if t == got {
			return true
		}
	}
	return false
}
