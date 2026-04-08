package gateway

import (
	"context"
	"errors"
	"testing"
	"time"
)

// scriptedProvider returns a sequence of (response, error) pairs from a
// queue, advancing one entry per call. Tests use it to script provider
// failures (rate-limit, timeout, generic error) without spinning up
// httptest servers.
type scriptedProvider struct {
	name  string
	queue []scriptedReply
	calls int
}

type scriptedReply struct {
	resp ChatResponse
	err  error
}

func (s *scriptedProvider) Name() string                              { return s.name }
func (s *scriptedProvider) Models(ctx context.Context) ([]string, error) {
	return []string{"m"}, nil
}
func (s *scriptedProvider) ChatCompletion(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	idx := s.calls
	s.calls++
	if idx >= len(s.queue) {
		return ChatResponse{}, errors.New("scripted provider exhausted")
	}
	r := s.queue[idx]
	if r.err != nil {
		return ChatResponse{}, r.err
	}
	resp := r.resp
	if len(resp.Choices) == 0 {
		resp.Choices = []Choice{{Index: 0, Message: Message{Role: "assistant", Content: "ok from " + s.name}, FinishReason: "stop"}}
	}
	return resp, nil
}

func newChainWith(t *testing.T, providers ...*scriptedProvider) (*Chain, *Registry) {
	t.Helper()
	reg := NewRegistry()
	for _, p := range providers {
		reg.Register(p)
	}
	return NewChain(reg), reg
}

func TestChainPassthroughWhenNoRule(t *testing.T) {
	p := &scriptedProvider{name: "openai", queue: []scriptedReply{{}}}
	chain, _ := newChainWith(t, p)

	resp, err := chain.Dispatch(context.Background(), ChatRequest{
		Model:    "openai/gpt-4o",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if resp.Model != "openai/gpt-4o" {
		t.Errorf("model = %q", resp.Model)
	}
	if p.calls != 1 {
		t.Errorf("calls = %d", p.calls)
	}
}

func TestChainFailoverOnRateLimit(t *testing.T) {
	primary := &scriptedProvider{name: "openai", queue: []scriptedReply{{
		err: &providerError{Provider: "openai", StatusCode: 429, Message: "throttled"},
	}}}
	secondary := &scriptedProvider{name: "anthropic", queue: []scriptedReply{{}}}

	chain, _ := newChainWith(t, primary, secondary)
	if err := chain.SetRule(Rule{
		Primary:   "openai/gpt-4o",
		Fallbacks: []string{"anthropic/claude-sonnet-4-6"},
		Triggers:  []Trigger{TriggerRateLimit},
	}); err != nil {
		t.Fatal(err)
	}

	resp, err := chain.Dispatch(context.Background(), ChatRequest{
		Model:    "openai/gpt-4o",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if resp.Model != "anthropic/claude-sonnet-4-6" {
		t.Errorf("response model = %q (failover not visible to caller)", resp.Model)
	}
	if primary.calls != 1 || secondary.calls != 1 {
		t.Errorf("calls primary=%d secondary=%d", primary.calls, secondary.calls)
	}
}

func TestChainNoFailoverOnUnmatchedTrigger(t *testing.T) {
	// Rule only triggers on rate_limit, but the primary returns a 500.
	// The chain must NOT advance and must surface the error verbatim.
	primary := &scriptedProvider{name: "openai", queue: []scriptedReply{{
		err: &providerError{Provider: "openai", StatusCode: 500, Message: "boom"},
	}}}
	secondary := &scriptedProvider{name: "anthropic", queue: []scriptedReply{{}}}

	chain, _ := newChainWith(t, primary, secondary)
	_ = chain.SetRule(Rule{
		Primary:   "openai/gpt-4o",
		Fallbacks: []string{"anthropic/claude-sonnet-4-6"},
		Triggers:  []Trigger{TriggerRateLimit},
	})

	_, err := chain.Dispatch(context.Background(), ChatRequest{
		Model:    "openai/gpt-4o",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if secondary.calls != 0 {
		t.Errorf("secondary should not have been called, got %d", secondary.calls)
	}
}

func TestChainEmptyTriggersMatchesAny(t *testing.T) {
	primary := &scriptedProvider{name: "openai", queue: []scriptedReply{{
		err: &providerError{Provider: "openai", StatusCode: 500, Message: "boom"},
	}}}
	secondary := &scriptedProvider{name: "anthropic", queue: []scriptedReply{{}}}

	chain, _ := newChainWith(t, primary, secondary)
	_ = chain.SetRule(Rule{
		Primary:   "openai/gpt-4o",
		Fallbacks: []string{"anthropic/claude-sonnet-4-6"},
		// no Triggers => any failure
	})
	resp, err := chain.Dispatch(context.Background(), ChatRequest{
		Model:    "openai/gpt-4o",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if resp.Model != "anthropic/claude-sonnet-4-6" {
		t.Errorf("model = %q", resp.Model)
	}
}

func TestChainWalksMultipleFallbacks(t *testing.T) {
	primary := &scriptedProvider{name: "openai", queue: []scriptedReply{{
		err: &providerError{Provider: "openai", StatusCode: 429, Message: "throttled"},
	}}}
	secondary := &scriptedProvider{name: "anthropic", queue: []scriptedReply{{
		err: &providerError{Provider: "anthropic", StatusCode: 429, Message: "throttled"},
	}}}
	tertiary := &scriptedProvider{name: "gemini", queue: []scriptedReply{{}}}

	chain, _ := newChainWith(t, primary, secondary, tertiary)
	_ = chain.SetRule(Rule{
		Primary:   "openai/gpt-4o",
		Fallbacks: []string{"anthropic/claude-sonnet-4-6", "gemini/gemini-2.0-flash"},
		Triggers:  []Trigger{TriggerRateLimit},
	})

	resp, err := chain.Dispatch(context.Background(), ChatRequest{
		Model:    "openai/gpt-4o",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if resp.Model != "gemini/gemini-2.0-flash" {
		t.Errorf("model = %q", resp.Model)
	}
}

func TestChainExhaustionSurfacesLastError(t *testing.T) {
	primary := &scriptedProvider{name: "openai", queue: []scriptedReply{{
		err: &providerError{Provider: "openai", StatusCode: 429, Message: "p1"},
	}}}
	secondary := &scriptedProvider{name: "anthropic", queue: []scriptedReply{{
		err: &providerError{Provider: "anthropic", StatusCode: 429, Message: "p2"},
	}}}
	chain, _ := newChainWith(t, primary, secondary)
	_ = chain.SetRule(Rule{
		Primary:   "openai/gpt-4o",
		Fallbacks: []string{"anthropic/claude-sonnet-4-6"},
		Triggers:  []Trigger{TriggerRateLimit},
	})
	_, err := chain.Dispatch(context.Background(), ChatRequest{
		Model:    "openai/gpt-4o",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var perr *providerError
	if !errors.As(err, &perr) || perr.StatusCode != 429 {
		t.Errorf("err = %v, want last 429 unwrapped", err)
	}
}

func TestChainTriggerClassification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want Trigger
	}{
		{"rate limit", &providerError{StatusCode: 429}, TriggerRateLimit},
		{"504 timeout", &providerError{StatusCode: 504}, TriggerTimeout},
		{"408 timeout", &providerError{StatusCode: 408}, TriggerTimeout},
		{"500 generic", &providerError{StatusCode: 500}, TriggerError},
		{"context deadline", context.DeadlineExceeded, TriggerTimeout},
		{"plain", errors.New("nope"), TriggerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyError(tc.err); got != tc.want {
				t.Errorf("classifyError = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestChainStopsOnContextCancel(t *testing.T) {
	primary := &scriptedProvider{name: "openai", queue: []scriptedReply{{
		err: &providerError{Provider: "openai", StatusCode: 429},
	}}}
	secondary := &scriptedProvider{name: "anthropic", queue: []scriptedReply{{}}}
	chain, _ := newChainWith(t, primary, secondary)
	_ = chain.SetRule(Rule{
		Primary:   "openai/gpt-4o",
		Fallbacks: []string{"anthropic/claude-sonnet-4-6"},
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := chain.Dispatch(ctx, ChatRequest{
		Model:    "openai/gpt-4o",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if primary.calls != 0 {
		t.Errorf("primary should not be called when ctx already done")
	}
}

func TestChainSetAndDeleteRule(t *testing.T) {
	chain := NewChain(NewRegistry())
	if err := chain.SetRule(Rule{}); err == nil {
		t.Error("expected error for empty primary")
	}
	_ = chain.SetRule(Rule{Primary: "a/b", Fallbacks: []string{"c/d"}})
	if len(chain.Rules()) != 1 {
		t.Errorf("rules = %d", len(chain.Rules()))
	}
	chain.DeleteRule("a/b")
	if len(chain.Rules()) != 0 {
		t.Errorf("rules after delete = %d", len(chain.Rules()))
	}
	_ = time.Now() // keep "time" in case future tests use it
}
