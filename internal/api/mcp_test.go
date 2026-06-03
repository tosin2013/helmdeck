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

// TestMCPCreate_BadJSON — malformed body returns 400.
func TestMCPCreate_BadJSON(t *testing.T) {
	h, _ := newMCPRouter(t)
	rr := doMCP(t, h, http.MethodPost, "/api/v1/mcp/servers", `{not-json`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// TestMCPCreate_ValidationFailure — registry rejects invalid input
// (missing name) with create_failed.
func TestMCPCreate_ValidationFailure(t *testing.T) {
	h, _ := newMCPRouter(t)
	rr := doMCP(t, h, http.MethodPost, "/api/v1/mcp/servers",
		`{"transport":"stdio","config":{}}`) // missing required `name`
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (%s)", rr.Code, rr.Body.String())
	}
}

// TestMCPGet_UnknownID returns 404.
func TestMCPGet_UnknownID(t *testing.T) {
	h, _ := newMCPRouter(t)
	rr := doMCP(t, h, http.MethodGet, "/api/v1/mcp/servers/no-such", "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// TestMCPUpdate_UnknownID returns 404.
func TestMCPUpdate_UnknownID(t *testing.T) {
	h, _ := newMCPRouter(t)
	rr := doMCP(t, h, http.MethodPut, "/api/v1/mcp/servers/no-such",
		`{"name":"e","transport":"stdio","config":{"command":"echo"}}`)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// TestMCPUpdate_BadJSON returns 400 invalid_json.
func TestMCPUpdate_BadJSON(t *testing.T) {
	h, _ := newMCPRouter(t)
	body := `{"name":"e","transport":"stdio","config":{"command":"echo"},"enabled":true}`
	rr := doMCP(t, h, http.MethodPost, "/api/v1/mcp/servers", body)
	var s mcp.Server
	_ = json.Unmarshal(rr.Body.Bytes(), &s)

	rr2 := doMCP(t, h, http.MethodPut, "/api/v1/mcp/servers/"+s.ID, `{nope`)
	if rr2.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr2.Code)
	}
}

// TestMCPDelete_UnknownID returns 404 not_found.
func TestMCPDelete_UnknownID(t *testing.T) {
	h, _ := newMCPRouter(t)
	rr := doMCP(t, h, http.MethodDelete, "/api/v1/mcp/servers/no-such", "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// TestMCP_MethodNotAllowed — PATCH on /{id} returns 405.
func TestMCP_MethodNotAllowed(t *testing.T) {
	h, _ := newMCPRouter(t)
	body := `{"name":"x","transport":"stdio","config":{"command":"echo"},"enabled":true}`
	rr := doMCP(t, h, http.MethodPost, "/api/v1/mcp/servers", body)
	var s mcp.Server
	_ = json.Unmarshal(rr.Body.Bytes(), &s)

	rr2 := doMCP(t, h, http.MethodPatch, "/api/v1/mcp/servers/"+s.ID, "")
	if rr2.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rr2.Code)
	}
}

// TestMCP_MissingIDIs404 — trailing /api/v1/mcp/servers/ with no id
// returns 404.
func TestMCP_MissingIDIs404(t *testing.T) {
	h, _ := newMCPRouter(t)
	rr := doMCP(t, h, http.MethodGet, "/api/v1/mcp/servers/", "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// TestMCP_UnknownSubroute — GET /{id}/bogus returns 404 unknown route.
func TestMCP_UnknownSubroute(t *testing.T) {
	h, _ := newMCPRouter(t)
	body := `{"name":"sub","transport":"stdio","config":{"command":"echo"},"enabled":true}`
	rr := doMCP(t, h, http.MethodPost, "/api/v1/mcp/servers", body)
	var s mcp.Server
	_ = json.Unmarshal(rr.Body.Bytes(), &s)

	rr2 := doMCP(t, h, http.MethodGet, "/api/v1/mcp/servers/"+s.ID+"/bogus", "")
	if rr2.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr2.Code)
	}
}

// TestMCP_ManifestUnknownID — GET /{id}/manifest on a missing id
// returns 404 from the underlying Manifest call.
func TestMCP_ManifestUnknownID(t *testing.T) {
	h, _ := newMCPRouter(t)
	rr := doMCP(t, h, http.MethodGet, "/api/v1/mcp/servers/missing/manifest", "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// TestMCP_ManifestMethodNotAllowed — DELETE on /{id}/manifest is 405.
func TestMCP_ManifestMethodNotAllowed(t *testing.T) {
	h, _ := newMCPRouter(t)
	body := `{"name":"m","transport":"stdio","config":{"command":"echo"},"enabled":true}`
	rr := doMCP(t, h, http.MethodPost, "/api/v1/mcp/servers", body)
	var s mcp.Server
	_ = json.Unmarshal(rr.Body.Bytes(), &s)

	rr2 := doMCP(t, h, http.MethodDelete, "/api/v1/mcp/servers/"+s.ID+"/manifest", "")
	if rr2.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rr2.Code)
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
