// Package memory — OWASP Agent Memory Guard middleware implementation.
package memory

import (
	"context"
	"fmt"
)

// OWASPMemoryMiddleware validates memory entries using the OWASP Agent
// Memory Guard (https://owasp.org/www-project-agent-memory-guard/).
//
// It wraps an AMGClient and conforms to the MemoryMiddleware interface
// described in issue #268. When a scan returns action="block", the
// middleware rejects the write with Allowed=false.
type OWASPMemoryMiddleware struct {
	client *AMGClient
}

// NewOWASPMemoryMiddleware creates a middleware that validates writes
// against an OWASP Agent Memory Guard server at the given URL.
//
// Example:
//   mw := NewOWASPMemoryMiddleware("http://localhost:8000")
//   store := NewInMemoryStore()
//   wrapped := NewValidatingStore(store, mw)
func NewOWASPMemoryMiddleware(amgURL string) *OWASPMemoryMiddleware {
	return &OWASPMemoryMiddleware{
		client: NewAMGClient(amgURL, nil),
	}
}

// Validate calls AMG's /scan endpoint and translates the response to a
// ValidationVerdict. When action="block" or safe=false, Allowed is false.
func (m *OWASPMemoryMiddleware) Validate(ctx context.Context, key string, value []byte, source ValidationSource) ValidationVerdict {
	// Map helmdeck's source class to AMG's expected values
	sourceClass := source.SourceClass
	if sourceClass == "" {
		sourceClass = "agent_authored"
	}

	resp, err := m.client.Scan(ctx, key, value, source.Category, sourceClass)
	if err != nil {
		// Network or parse error — fail closed: reject the write.
		return ValidationVerdict{
			Allowed:    false,
			Action:     "block",
			Reason:     fmt.Sprintf("AMG scan failed: %v", err),
			ThreatType: "scan_error",
			Confidence: 0.0,
		}
	}

	// When AMG says "block" or safe=false, reject the write.
	allowed := resp.Action == "allow" && resp.Safe
	action := resp.Action
	if !allowed && action == "" {
		action = "block"
	}

	// Extract first threat type from events if present
	var threatType string
	var confidence float64
	if len(resp.Events) > 0 {
		if det, ok := resp.Events[0]["detector"].(string); ok {
			threatType = det
		}
		if conf, ok := resp.Events[0]["confidence"].(float64); ok {
			confidence = conf
		}
	}

	reason := ""
	if !allowed && len(resp.Events) > 0 {
		if msg, ok := resp.Events[0]["message"].(string); ok {
			reason = msg
		}
	}

	return ValidationVerdict{
		Allowed:    allowed,
		Action:     action,
		Reason:     reason,
		ThreatType: threatType,
		Confidence: confidence,
	}
}
