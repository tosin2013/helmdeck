// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- newClient env-var handling -----------------------------------------

func TestNewClient_DefaultURL(t *testing.T) {
	t.Setenv("HELMDECK_URL", "")
	t.Setenv("HELMDECK_TOKEN", "tok")
	c, err := newClient()
	if err != nil {
		t.Fatal(err)
	}
	if c.baseURL != "http://localhost:3000" {
		t.Errorf("baseURL = %q", c.baseURL)
	}
}

func TestNewClient_TrimsTrailingSlash(t *testing.T) {
	t.Setenv("HELMDECK_URL", "https://h.example/")
	t.Setenv("HELMDECK_TOKEN", "tok")
	c, _ := newClient()
	if c.baseURL != "https://h.example" {
		t.Errorf("baseURL not trimmed: %q", c.baseURL)
	}
}

func TestNewClient_MissingToken_Errors(t *testing.T) {
	t.Setenv("HELMDECK_URL", "https://h.example")
	t.Setenv("HELMDECK_TOKEN", "")
	if _, err := newClient(); err == nil {
		t.Fatal("expected error when HELMDECK_TOKEN is empty")
	}
}

// --- request shape --------------------------------------------------------

func stubControlPlane(t *testing.T, fn func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(fn))
	t.Cleanup(srv.Close)
	t.Setenv("HELMDECK_URL", srv.URL)
	t.Setenv("HELMDECK_TOKEN", "test-tok")
	return srv
}

func TestClientRequest_CarriesAuthHeader(t *testing.T) {
	var gotAuth string
	stubControlPlane(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte(`{}`))
	})
	c, _ := newClient()
	if _, err := c.get("/api/v1/packs"); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer test-tok" {
		t.Errorf("Authorization = %q, want Bearer test-tok", gotAuth)
	}
}

func TestClientPost_SendsJSONBody(t *testing.T) {
	var gotMethod string
	var gotContentType string
	var gotBody []byte
	stubControlPlane(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.Write([]byte(`{}`))
	})
	c, _ := newClient()
	if _, err := c.post("/api/v1/marketplace/install", map[string]string{"name": "cmd.upper"}); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" {
		t.Errorf("method = %q", gotMethod)
	}
	if !strings.HasPrefix(gotContentType, "application/json") {
		t.Errorf("content-type = %q", gotContentType)
	}
	var parsed map[string]string
	if err := json.Unmarshal(gotBody, &parsed); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if parsed["name"] != "cmd.upper" {
		t.Errorf("body name = %q", parsed["name"])
	}
}

func TestClientRequest_4xxSurfacesEnvelope(t *testing.T) {
	stubControlPlane(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"code":"pack_not_in_catalog","message":"no pack named cmd.x"}`))
	})
	c, _ := newClient()
	_, err := c.post("/api/v1/marketplace/install", map[string]string{"name": "cmd.x"})
	if err == nil {
		t.Fatal("expected error from 404")
	}
	if !strings.Contains(err.Error(), "pack_not_in_catalog") {
		t.Errorf("error should preserve the structured code: %v", err)
	}
}

// --- subcommand dispatch ------------------------------------------------

func TestRun_UnknownTopLevelCommand(t *testing.T) {
	err := run([]string{"banana"})
	if err == nil || !strings.Contains(err.Error(), "unknown subcommand") {
		t.Errorf("expected unknown-subcommand error, got %v", err)
	}
}

func TestRun_HelpFlagsPrintUsageNoError(t *testing.T) {
	for _, arg := range []string{"-h", "--help", "help"} {
		if err := run([]string{arg}); err != nil {
			t.Errorf("run(%q) returned error: %v", arg, err)
		}
	}
}

func TestRunPack_MissingSubcommand(t *testing.T) {
	err := runPack(nil)
	if err == nil || !strings.Contains(err.Error(), "missing subcommand") {
		t.Errorf("expected missing-subcommand error, got %v", err)
	}
}

func TestRunPack_UnknownSubcommand(t *testing.T) {
	err := runPack([]string{"banana"})
	if err == nil || !strings.Contains(err.Error(), "unknown pack subcommand") {
		t.Errorf("expected unknown error, got %v", err)
	}
}

func TestRunPackInstall_MissingName(t *testing.T) {
	t.Setenv("HELMDECK_TOKEN", "tok")
	err := runPackInstall([]string{})
	if err == nil || !strings.Contains(err.Error(), "usage") {
		t.Errorf("expected usage error, got %v", err)
	}
}

// --- end-to-end happy paths against the stub ----------------------------

func TestRunPackList_HappyPath(t *testing.T) {
	stubControlPlane(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/packs" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Write([]byte(`[{"name":"cmd.upper","description":"Uppercase","latest":"v1"}]`))
	})
	// --json mode keeps stdout writes machine-readable; we just
	// verify there's no error here.
	if err := runPackList([]string{"--json"}); err != nil {
		t.Errorf("pack list: %v", err)
	}
}

func TestRunPackInstalled_HappyPath(t *testing.T) {
	stubControlPlane(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/marketplace/installed" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Write([]byte(`{"installed":[{"name":"cmd.upper","version":"v1","installed_at":"2026-05-15T18:00:00Z","trust_verified":true}]}`))
	})
	if err := runPackInstalled([]string{"--json"}); err != nil {
		t.Errorf("pack installed: %v", err)
	}
}

func TestRunPackCatalog_RefreshFlag_PostsToRefreshEndpoint(t *testing.T) {
	var gotPath, gotMethod string
	stubControlPlane(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		w.Write([]byte(`{"index":{"catalog_version":"v1","packs":[]},"meta":{"source":"file://"}}`))
	})
	if err := runPackCatalog([]string{"--refresh", "--json"}); err != nil {
		t.Errorf("pack marketplace --refresh: %v", err)
	}
	if gotMethod != "POST" || gotPath != "/api/v1/marketplace/refresh" {
		t.Errorf("--refresh did not POST to /refresh: method=%q path=%q", gotMethod, gotPath)
	}
}

func TestRunPackInstall_HappyPath(t *testing.T) {
	stubControlPlane(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/marketplace/install" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Write([]byte(`{"pack":{"name":"cmd.upper","version":"v1","install_dir":"/tmp/x","trust_verified":true,"trust_note":"sha256 verified"}}`))
	})
	// Go's flag package stops at the first positional, so flags
	// must come before the positional arg.
	if err := runPackInstall([]string{"--json", "cmd.upper"}); err != nil {
		t.Errorf("pack install: %v", err)
	}
}

func TestRunPackUninstall_HappyPath(t *testing.T) {
	var gotBody []byte
	stubControlPlane(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Write([]byte(`{"status":"uninstalled","name":"cmd.upper"}`))
	})
	if err := runPackUninstall([]string{"--json", "cmd.upper"}); err != nil {
		t.Errorf("pack uninstall: %v", err)
	}
	if !strings.Contains(string(gotBody), "cmd.upper") {
		t.Errorf("expected body to name the pack, got %q", gotBody)
	}
}
