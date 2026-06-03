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

	"github.com/tosin2013/helmdeck/internal/packs"
)

func newPacksRouter(t *testing.T) (http.Handler, *packs.Registry) {
	t.Helper()
	reg := packs.NewPackRegistry()

	echo := &packs.Pack{
		Name: "echo", Version: "v1", Description: "echoes input.msg",
		InputSchema:  packs.BasicSchema{Required: []string{"msg"}, Properties: map[string]string{"msg": "string"}},
		OutputSchema: packs.BasicSchema{Required: []string{"echo"}},
		Handler: func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
			var in struct {
				Msg string `json:"msg"`
			}
			_ = json.Unmarshal(ec.Input, &in)
			return json.Marshal(map[string]string{"echo": in.Msg})
		},
	}
	echoV2 := &packs.Pack{
		Name: "echo", Version: "v2", Description: "echoes uppercase",
		Handler: func(ctx context.Context, ec *packs.ExecutionContext) (json.RawMessage, error) {
			return json.RawMessage(`{"echo":"V2"}`), nil
		},
	}
	if err := reg.Register(echo); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(echoV2); err != nil {
		t.Fatal(err)
	}

	eng := packs.New(packs.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	h := NewRouter(Deps{
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version:      "test",
		PackRegistry: reg,
		PackEngine:   eng,
	})
	return h, reg
}

func doPack(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestPacksList(t *testing.T) {
	h, _ := newPacksRouter(t)
	rr := doPack(t, h, http.MethodGet, "/api/v1/packs", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var infos []packs.PackInfo
	_ = json.Unmarshal(rr.Body.Bytes(), &infos)
	if len(infos) != 1 || infos[0].Name != "echo" {
		t.Fatalf("infos = %+v", infos)
	}
	if infos[0].Latest != "v2" {
		t.Errorf("latest = %q", infos[0].Latest)
	}
}

func TestPacksDispatchLatest(t *testing.T) {
	h, _ := newPacksRouter(t)
	rr := doPack(t, h, http.MethodPost, "/api/v1/packs/echo", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var res packs.Result
	_ = json.Unmarshal(rr.Body.Bytes(), &res)
	if res.Version != "v2" {
		t.Errorf("dispatched version = %q, want v2", res.Version)
	}
	if string(res.Output) != `{"echo":"V2"}` {
		t.Errorf("output = %s", res.Output)
	}
}

func TestPacksDispatchPinnedVersion(t *testing.T) {
	h, _ := newPacksRouter(t)
	rr := doPack(t, h, http.MethodPost, "/api/v1/packs/echo/v1", `{"msg":"hello"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var res packs.Result
	_ = json.Unmarshal(rr.Body.Bytes(), &res)
	if res.Version != "v1" {
		t.Errorf("version = %q", res.Version)
	}
	if !strings.Contains(string(res.Output), `"echo":"hello"`) {
		t.Errorf("output = %s", res.Output)
	}
}

func TestPacksDispatchInvalidInputMaps400(t *testing.T) {
	h, _ := newPacksRouter(t)
	rr := doPack(t, h, http.MethodPost, "/api/v1/packs/echo/v1", `{}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var env map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &env)
	if env["error"] != "invalid_input" {
		t.Errorf("error = %v", env["error"])
	}
}

func TestPacksDispatchUnknownPack404(t *testing.T) {
	h, _ := newPacksRouter(t)
	rr := doPack(t, h, http.MethodPost, "/api/v1/packs/nope", "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d", rr.Code)
	}
}

func TestPacksUnknownVersion404(t *testing.T) {
	h, _ := newPacksRouter(t)
	rr := doPack(t, h, http.MethodPost, "/api/v1/packs/echo/v9", "")
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d", rr.Code)
	}
}

func TestPacksGetMetadata(t *testing.T) {
	h, _ := newPacksRouter(t)
	rr := doPack(t, h, http.MethodGet, "/api/v1/packs/echo/v1", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
}

// TestWritePackError_AllStatusMappings covers every typed-error code →
// HTTP-status mapping in writePackError + the non-PackError fallback.
// The closed-set in classify.go means a new code that lands here without
// a mapping would silently fall through to 500 — this test flags such
// drift loudly.
func TestWritePackError_AllStatusMappings(t *testing.T) {
	cases := []struct {
		code   packs.ErrorCode
		status int
	}{
		{packs.CodeInvalidInput, http.StatusBadRequest},
		{packs.CodeInvalidOutput, http.StatusBadGateway},
		{packs.CodeSessionUnavailable, http.StatusServiceUnavailable},
		{packs.CodeArtifactFailed, http.StatusBadGateway},
		{packs.CodeTimeout, http.StatusGatewayTimeout},
		{packs.CodeHandlerFailed, http.StatusBadGateway},
		{packs.CodeInternal, http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(string(tc.code), func(t *testing.T) {
			rr := httptest.NewRecorder()
			writePackError(rr, &packs.PackError{Code: tc.code, Message: "test"})
			if rr.Code != tc.status {
				t.Errorf("code %s mapped to %d, want %d", tc.code, rr.Code, tc.status)
			}
			var env map[string]any
			_ = json.Unmarshal(rr.Body.Bytes(), &env)
			if env["error"] != string(tc.code) {
				t.Errorf("body error code = %v, want %q", env["error"], tc.code)
			}
			if env["message"] != "test" {
				t.Errorf("body message = %v, want \"test\"", env["message"])
			}
		})
	}

	// Non-PackError → generic 500 internal.
	rr := httptest.NewRecorder()
	writePackError(rr, plainErr("plain error"))
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("non-PackError status = %d, want 500", rr.Code)
	}
}

type plainErr string

func (e plainErr) Error() string { return string(e) }

func TestPacksUnavailableWhenNil(t *testing.T) {
	h := NewRouter(Deps{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test",
	})
	rr := doPack(t, h, http.MethodGet, "/api/v1/packs", "")
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d", rr.Code)
	}
}
