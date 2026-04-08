package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net"
	"net/http"
	"time"
)

// DefaultHTTPClient builds the shared *http.Client every provider adapter
// uses by default. Connection pooling matters here because every chat
// request opens a TLS connection to a long-lived upstream — without an
// explicit Transport, Go's default disables keep-alives in some build
// modes and the per-request handshake dwarfs the actual inference cost.
//
// Tunables here mirror what production OpenAI/Anthropic SDKs ship: 100
// idle conns total, 10 per host, 90s idle timeout. The Timeout is
// deliberately generous because completions can stream for a minute on
// long contexts; per-request deadlines belong on the caller's
// context.Context, not on the global client.
func DefaultHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 5 * time.Minute,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			MaxConnsPerHost:       0,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			ForceAttemptHTTP2:     true,
		},
	}
}

// RetryPolicy controls how doRequest retries transient failures.
// MaxAttempts counts the initial attempt — MaxAttempts=3 means at most
// two retries. BaseDelay/MaxDelay bound the exponential backoff.
type RetryPolicy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

// DefaultRetryPolicy is what every adapter uses unless the caller
// overrides it. Three attempts with jittered exponential backoff is the
// sweet spot in practice — fewer hides flaky upstreams, more amplifies
// outages and burns the request's deadline before the user gives up.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts: 3,
		BaseDelay:   200 * time.Millisecond,
		MaxDelay:    5 * time.Second,
	}
}

// providerError is the structured error every adapter returns. Keeping
// it concrete (instead of fmt.Errorf strings) lets the gateway map
// upstream status codes to the OpenAI envelope without re-parsing
// messages later.
type providerError struct {
	Provider   string
	StatusCode int
	Message    string
}

func (e *providerError) Error() string {
	return fmt.Sprintf("%s upstream %d: %s", e.Provider, e.StatusCode, e.Message)
}

// doJSONRequest POSTs body as JSON to url with the given headers and
// retries on 429/5xx according to policy. It returns the response body
// bytes on 2xx, or a *providerError on terminal failure.
//
// The caller's context is honored for cancellation between retries —
// hitting Ctrl+C should not block on a sleeping backoff.
func doJSONRequest(
	ctx context.Context,
	client *http.Client,
	policy RetryPolicy,
	provider, method, url string,
	headers map[string]string,
	body []byte,
) ([]byte, error) {
	if client == nil {
		client = DefaultHTTPClient()
	}
	if policy.MaxAttempts <= 0 {
		policy = DefaultRetryPolicy()
	}

	var lastErr error
	for attempt := 0; attempt < policy.MaxAttempts; attempt++ {
		if attempt > 0 {
			delay := backoffDelay(policy, attempt)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := client.Do(req)
		if err != nil {
			// Network errors are always retryable — context cancellation
			// is checked at the top of the loop on the next iteration.
			lastErr = err
			continue
		}

		respBody, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return respBody, nil
		}

		// 429 and 5xx are transient; everything else is terminal so we
		// don't burn retries on a 401 or a malformed request.
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = &providerError{Provider: provider, StatusCode: resp.StatusCode, Message: string(respBody)}
			continue
		}
		return nil, &providerError{Provider: provider, StatusCode: resp.StatusCode, Message: string(respBody)}
	}
	if lastErr == nil {
		lastErr = errors.New("retry exhausted")
	}
	return nil, lastErr
}

func backoffDelay(p RetryPolicy, attempt int) time.Duration {
	// Exponential: base * 2^(attempt-1), capped at MaxDelay, plus full
	// jitter (uniform [0, delay)) — full jitter outperforms equal jitter
	// for thundering-herd avoidance, per the AWS architecture blog.
	exp := float64(p.BaseDelay) * math.Pow(2, float64(attempt-1))
	if exp > float64(p.MaxDelay) {
		exp = float64(p.MaxDelay)
	}
	jittered := rand.Int63n(int64(exp) + 1)
	return time.Duration(jittered)
}
