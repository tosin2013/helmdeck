// Package memory — HTTP client for OWASP Agent Memory Guard REST API.
package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// AMGClient is an HTTP client for the OWASP Agent Memory Guard server.
// It calls the /scan endpoint to validate memory writes.
//
// Usage:
//   client := NewAMGClient("http://localhost:8000", nil)
//   result, err := client.Scan(ctx, "mykey", []byte("content"), "cache")
type AMGClient struct {
	baseURL    string
	httpClient *http.Client
}

// NewAMGClient creates an AMG client pointed at the given base URL.
// If httpClient is nil, a default client with 10s timeout is used.
func NewAMGClient(baseURL string, httpClient *http.Client) *AMGClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &AMGClient{
		baseURL:    baseURL,
		httpClient: httpClient,
	}
}

// ScanRequest is the payload sent to AMG's POST /scan endpoint.
type ScanRequest struct {
	Text        string  `json:"text"`
	Key         string  `json:"key"`
	Source      string  `json:"source"`
	SourceClass *string `json:"source_class,omitempty"`
}

// ScanResponse is the response from AMG's POST /scan endpoint.
type ScanResponse struct {
	Action           string                   `json:"action"`
	ThreatsDetected  int                      `json:"threats_detected"`
	Events           []map[string]interface{} `json:"events"`
	Safe             bool                     `json:"safe"`
}

// Scan calls AMG's /scan endpoint to validate a memory entry.
// It returns the verdict or an error if the HTTP call failed.
func (c *AMGClient) Scan(ctx context.Context, key string, value []byte, source, sourceClass string) (*ScanResponse, error) {
	reqBody := ScanRequest{
		Text:   string(value),
		Key:    key,
		Source: source,
	}
	if sourceClass != "" {
		reqBody.SourceClass = &sourceClass
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("amg: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/scan", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("amg: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("amg: http call: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("amg: unexpected status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var scanResp ScanResponse
	if err := json.NewDecoder(resp.Body).Decode(&scanResp); err != nil {
		return nil, fmt.Errorf("amg: decode response: %w", err)
	}

	return &scanResp, nil
}
