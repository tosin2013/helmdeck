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

	"github.com/tosin2013/helmdeck/internal/mcp"
	"github.com/tosin2013/helmdeck/internal/store"
)

func newMCPRouter(t *testing.T) (http.Handler, *mcp.Registry) {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	reg := mcp.NewRegistry(db)
	// Stub the adapter so manifest endpoints work without spawning anything.
	reg.WithAdapterFactory(func(*mcp.Server) (mcp.Adapter, error) {
		return stubAdapter{}, nil
	})
	h := NewRouter(Deps{
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:     "test",
		MCPRegistry: reg,
	})
	return h, reg
}

type stubAdapter struct{}

func (stubAdapter) FetchManifest(ctx context.Context) (*mcp.Manifest, error) {
	return &mcp.Manifest{Tools: []mcp.Tool{{Name: "ping"}}}, nil
}

func doMCP(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestMCPCRUDFlow(t *testing.T) {
	h, _ := newMCPRouter(t)

	// create
	body := `{"name":"echo","transport":"stdio","config":{"command":"echo","args":["hi"]},"enabled":true}`
	rr := doMCP(t, h, http.MethodPost, "/api/v1/mcp/servers", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", rr.Code, rr.Body.String())
	}
	var s mcp.Server
	_ = json.Unmarshal(rr.Body.Bytes(), &s)
	if s.ID == "" || s.Name != "echo" {
		t.Errorf("server = %+v", s)
	}

	// list
	rr = doMCP(t, h, http.MethodGet, "/api/v1/mcp/servers", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d", rr.Code)
	}

	// get
	rr = doMCP(t, h, http.MethodGet, "/api/v1/mcp/servers/"+s.ID, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("get status = %d", rr.Code)
	}

	// duplicate -> 409
	rr = doMCP(t, h, http.MethodPost, "/api/v1/mcp/servers", body)
	if rr.Code != http.StatusConflict {
		t.Errorf("duplicate status = %d", rr.Code)
	}

	// update
	updated := `{"name":"echo","transport":"stdio","config":{"command":"echo","args":["bye"]},"enabled":false}`
	rr = doMCP(t, h, http.MethodPut, "/api/v1/mcp/servers/"+s.ID, updated)
	if rr.Code != http.StatusOK {
		t.Fatalf("update status = %d body=%s", rr.Code, rr.Body.String())
	}

	// manifest GET (stub returns ping tool)
	// First enable it again or manifest will refuse
	enabled := `{"name":"echo","transport":"stdio","config":{"command":"echo","args":["bye"]},"enabled":true}`
	_ = doMCP(t, h, http.MethodPut, "/api/v1/mcp/servers/"+s.ID, enabled)
	rr = doMCP(t, h, http.MethodGet, "/api/v1/mcp/servers/"+s.ID+"/manifest", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("manifest status = %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"ping"`) {
		t.Errorf("manifest body = %s", rr.Body.String())
	}

	// manifest POST forces refresh
	rr = doMCP(t, h, http.MethodPost, "/api/v1/mcp/servers/"+s.ID+"/manifest", "")
	if rr.Code != http.StatusOK {
		t.Errorf("manifest force status = %d", rr.Code)
	}

	// delete
	rr = doMCP(t, h, http.MethodDelete, "/api/v1/mcp/servers/"+s.ID, "")
	if rr.Code != http.StatusNoContent {
		t.Errorf("delete status = %d", rr.Code)
	}
	rr = doMCP(t, h, http.MethodGet, "/api/v1/mcp/servers/"+s.ID, "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("get-after-delete status = %d", rr.Code)
	}
}

func TestMCPUnavailableWhenNil(t *testing.T) {
	h := NewRouter(Deps{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
	})
	rr := doMCP(t, h, http.MethodGet, "/api/v1/mcp/servers", "")
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d", rr.Code)
	}
}
