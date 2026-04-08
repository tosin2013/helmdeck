package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"

	"github.com/tosin2013/helmdeck/internal/packs"
)

func newMCPServerRouter(t *testing.T) http.Handler {
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
	eng := packs.New()
	return NewRouter(Deps{
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:      "test",
		PackRegistry: reg,
		PackEngine:   eng,
	})
}

func TestMCPServerOverWebSocket(t *testing.T) {
	srv := httptest.NewServer(newMCPServerRouter(t))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/mcp/ws"

	dialCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, _, err := ws.Dial(dialCtx, wsURL)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	send := func(payload string) {
		if err := wsutil.WriteClientText(conn, []byte(payload)); err != nil {
			t.Fatal(err)
		}
	}
	recv := func() string {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		b, _, err := wsutil.ReadServerData(conn)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		return string(b)
	}

	// initialize → response carries protocol version
	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if r := recv(); !strings.Contains(r, `"protocolVersion"`) {
		t.Errorf("init resp = %s", r)
	}

	// tools/list → echo present
	send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if r := recv(); !strings.Contains(r, `"echo"`) {
		t.Errorf("list resp = %s", r)
	}

	// tools/call → echo result
	send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"msg":"hi"}}}`)
	r := recv()
	if !strings.Contains(r, `"isError":false`) {
		t.Errorf("call resp missing isError:false: %s", r)
	}
	if !strings.Contains(r, `\"echo\":\"hi\"`) {
		t.Errorf("call resp missing echoed text: %s", r)
	}
}

func TestMCPServerWSUnavailableWhenNil(t *testing.T) {
	h := NewRouter(Deps{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
	})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/mcp/ws", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d", rr.Code)
	}
}
