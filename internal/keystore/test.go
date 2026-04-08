package keystore

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// TestEndpoint pings each provider's cheapest authenticated endpoint to
// verify that a key works. We deliberately do NOT call chat completions
// here — listing models is free across every supported provider, while
// chat completions cost money even at min tokens. Returning the live
// status code keeps debugging straightforward when a key fails.
//
// The function is intentionally a free function (not a Store method) so
// the provider-test endpoint can call it with a freshly-decrypted key
// without the keystore needing to know about HTTP wire formats.
func TestProviderKey(ctx context.Context, client *http.Client, provider, apiKey string) error {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	var (
		req *http.Request
		err error
	)
	switch provider {
	case "openai":
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, "https://api.openai.com/v1/models", nil)
		if err == nil {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
	case "deepseek":
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, "https://api.deepseek.com/v1/models", nil)
		if err == nil {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
	case "anthropic":
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, "https://api.anthropic.com/v1/models", nil)
		if err == nil {
			req.Header.Set("x-api-key", apiKey)
			req.Header.Set("anthropic-version", "2023-06-01")
		}
	case "gemini":
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, "https://generativelanguage.googleapis.com/v1beta/models?key="+apiKey, nil)
	case "ollama":
		// Ollama is local — there is no hosted endpoint to validate against,
		// and no auth, so a key check is meaningless. Surface this as a
		// no-op success rather than a misleading failure.
		return nil
	default:
		return fmt.Errorf("unknown provider %q", provider)
	}
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s reachability: %w", provider, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s returned %d", provider, resp.StatusCode)
	}
	return nil
}
