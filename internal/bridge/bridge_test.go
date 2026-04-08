package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tosin2013/helmdeck/internal/api"
	"github.com/tosin2013/helmdeck/internal/auth"
	"github.com/tosin2013/helmdeck/internal/packs"
)

// startServer brings up a real helmdeck control plane (just the
// pack registry + MCP server route) so the bridge has something to
// dial. JWT auth is enabled because the bridge's job is to set
// the bearer header — testing without auth would let a regression
// in the header path slip through.
func startServer(t *testing.T) (url string, token string) {
	t.Helper()
	reg := packs.NewPackRegistry()
	_ = reg.Register(&packs.Pack{
		Name: "echo", Version: "v1", Description: "echo",
		Handler: func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
			var in struct {
				Msg string `json:"msg"`
			}
			_ = json.Unmarshal(ec.Input, &in)
			return json.Marshal(map[string]string{"echo": in.Msg})
		},
	})

	secret, err := auth.GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := auth.NewIssuer([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := issuer.Issue("test", "test", "test", []auth.Scope{"admin"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	h := api.NewRouter(api.Deps{
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:      "test",
		Issuer:       issuer,
		PackRegistry: reg,
		PackEngine:   packs.New(),
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL, tok
}

func TestBridgeRoundTrip(t *testing.T) {
	srvURL, token := startServer(t)

	// Drive the bridge with an in-memory stdin pipe so we can
	// write a tools/list request, then close stdin so the bridge
	// terminates cleanly. We need to leave the stdout pipe open
	// long enough to read the response before closing it.
	stdinR, stdinW := io.Pipe()
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Config{
			URL:    srvURL,
			Token:  token,
			Stdin:  stdinR,
			Stdout: &stdout,
			Stderr: &stderr,
		})
	}()

	// Send tools/list. The bridge writes one ws frame; the server
	// responds with one frame; the bridge writes the response to
	// stdout. After we see the response we close stdin to make Run
	// return.
	if _, err := stdinW.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n")); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(stdout.String(), `"echo"`) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(stdout.String(), `"echo"`) {
		t.Fatalf("did not see tools/list response on stdout. stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	_ = stdinW.Close()

	select {
	case err := <-done:
		if err != nil && !strings.Contains(err.Error(), "EOF") {
			t.Errorf("Run returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after stdin closed")
	}

	if !strings.Contains(stderr.String(), "connected to") {
		t.Errorf("expected 'connected to' on stderr, got %q", stderr.String())
	}
}

func TestBridgeMissingConfig(t *testing.T) {
	err := Run(context.Background(), Config{})
	if err != ErrMissingConfig {
		t.Errorf("err = %v, want ErrMissingConfig", err)
	}
}

func TestBridgeRejectedAuth(t *testing.T) {
	srvURL, _ := startServer(t)
	err := Run(context.Background(), Config{
		URL:   srvURL,
		Token: "not-a-real-token",
	})
	if err == nil {
		t.Fatal("expected dial error for bad token")
	}
}

func TestToWebSocketURL(t *testing.T) {
	cases := map[string]string{
		"http://x:3000":  "ws://x:3000/api/v1/mcp/ws",
		"https://x":     "wss://x/api/v1/mcp/ws",
		"ws://x:3000":   "ws://x:3000/api/v1/mcp/ws",
		"wss://x":       "wss://x/api/v1/mcp/ws",
	}
	for in, want := range cases {
		got, err := toWebSocketURL(in, "/api/v1/mcp/ws")
		if err != nil || got != want {
			t.Errorf("toWebSocketURL(%q) = %q,%v want %q", in, got, err, want)
		}
	}
	if _, err := toWebSocketURL("ftp://x", "/x"); err == nil {
		t.Error("expected error for unsupported scheme")
	}
}
