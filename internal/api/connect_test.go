package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newConnectRouter(t *testing.T) http.Handler {
	t.Helper()
	return NewRouter(Deps{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
		// no Issuer => /api/v1/* auth disabled (dev mode), so the
		// connect endpoint is reachable without a JWT in the test.
	})
}

func TestConnectSnippet_AllClients(t *testing.T) {
	h := newConnectRouter(t)
	clients := []string{"claude-code", "claude-desktop", "openclaw", "gemini-cli"}
	for _, c := range clients {
		t.Run(c, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/connect/"+c+"?url=https://h.example&token=abc123", nil)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			var got map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got["client"] != c {
				t.Errorf("client=%v want %s", got["client"], c)
			}
			if _, ok := got["install_path"].(string); !ok {
				t.Errorf("missing install_path")
			}
			// The url+token must reach the env block somewhere in
			// the snippet — assert via a substring search on the
			// re-marshaled JSON to stay shape-agnostic across clients.
			raw, _ := json.Marshal(got["config"])
			if !contains(raw, "https://h.example") {
				t.Errorf("url not interpolated: %s", raw)
			}
			if !contains(raw, "abc123") {
				t.Errorf("token not interpolated: %s", raw)
			}
			if !contains(raw, "HELMDECK_URL") || !contains(raw, "HELMDECK_TOKEN") {
				t.Errorf("env keys missing: %s", raw)
			}
		})
	}
}

func TestConnectSnippet_DefaultsFromRequest(t *testing.T) {
	h := newConnectRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/connect/claude-code", nil)
	req.Host = "platform.local:3000"
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	body := rr.Body.Bytes()
	if !contains(body, "http://platform.local:3000") {
		t.Errorf("default url not derived from request host: %s", body)
	}
	if !contains(body, "REPLACE_ME") {
		t.Errorf("default token placeholder missing: %s", body)
	}
}

func TestConnectSnippet_UnknownClient(t *testing.T) {
	h := newConnectRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/connect/notreal", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func contains(haystack []byte, needle string) bool {
	return len(needle) > 0 && bytesIndex(haystack, needle) >= 0
}

func bytesIndex(h []byte, n string) int {
	// tiny indexer to avoid pulling another import — equivalent to bytes.Index.
outer:
	for i := 0; i+len(n) <= len(h); i++ {
		for j := 0; j < len(n); j++ {
			if h[i+j] != n[j] {
				continue outer
			}
		}
		return i
	}
	return -1
}
