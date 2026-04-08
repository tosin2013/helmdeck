// Package api — credential vault REST surface (T501, ADR 007).
//
// Endpoints under /api/v1/vault/credentials/* let operators manage
// the AES-256-GCM credential store. The Resolve path (used by
// pack handlers and the placeholder-token gateway in T504) is
// in-process — no REST surface — because exposing it over HTTP
// would defeat the "agents never see plaintext" guarantee.
//
//	POST   /api/v1/vault/credentials             create
//	GET    /api/v1/vault/credentials             list
//	GET    /api/v1/vault/credentials/{id}        get (redacted)
//	PUT    /api/v1/vault/credentials/{id}        rotate (body: {plaintext})
//	DELETE /api/v1/vault/credentials/{id}        delete
//	POST   /api/v1/vault/credentials/{id}/grants add ACL entry
//	GET    /api/v1/vault/credentials/{id}/grants list ACL entries
//	DELETE /api/v1/vault/credentials/{id}/grants/{subject} revoke
//	GET    /api/v1/vault/credentials/{id}/usage  recent usage log
//
// Plaintext is base64-encoded on the wire so callers can store
// arbitrary bytes (SSH private keys, cookie jars, OAuth refresh
// tokens) without JSON-string escaping.

package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/tosin2013/helmdeck/internal/vault"
)

type vaultCreateRequest struct {
	Name        string         `json:"name"`
	Type        string         `json:"type"`
	HostPattern string         `json:"host_pattern"`
	PathPattern string         `json:"path_pattern,omitempty"`
	PlaintextB64 string        `json:"plaintext_b64"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

type vaultRotateRequest struct {
	PlaintextB64 string `json:"plaintext_b64"`
}

type vaultGrantRequest struct {
	ActorSubject string `json:"actor_subject"`
	ActorClient  string `json:"actor_client,omitempty"`
}

func registerVaultRoutes(mux *http.ServeMux, deps Deps) {
	if deps.Vault == nil {
		mux.HandleFunc("/api/v1/vault/", func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusServiceUnavailable, "vault_unavailable", "credential vault not configured")
		})
		return
	}
	v := deps.Vault

	mux.HandleFunc("POST /api/v1/vault/credentials", func(w http.ResponseWriter, r *http.Request) {
		var req vaultCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		plaintext, err := base64.StdEncoding.DecodeString(req.PlaintextB64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_plaintext_b64", err.Error())
			return
		}
		rec, err := v.Create(r.Context(), vault.CreateInput{
			Name:        req.Name,
			Type:        vault.CredentialType(req.Type),
			HostPattern: req.HostPattern,
			PathPattern: req.PathPattern,
			Plaintext:   plaintext,
			Metadata:    req.Metadata,
		})
		if err != nil {
			writeVaultError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, rec)
	})

	mux.HandleFunc("GET /api/v1/vault/credentials", func(w http.ResponseWriter, r *http.Request) {
		typ := vault.CredentialType(r.URL.Query().Get("type"))
		recs, err := v.List(r.Context(), typ)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "vault_list_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"credentials": recs, "count": len(recs)})
	})

	mux.HandleFunc("GET /api/v1/vault/credentials/{id}", func(w http.ResponseWriter, r *http.Request) {
		rec, err := v.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			writeVaultError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, rec)
	})

	mux.HandleFunc("PUT /api/v1/vault/credentials/{id}", func(w http.ResponseWriter, r *http.Request) {
		var req vaultRotateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		plaintext, err := base64.StdEncoding.DecodeString(req.PlaintextB64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_plaintext_b64", err.Error())
			return
		}
		rec, err := v.Rotate(r.Context(), r.PathValue("id"), plaintext)
		if err != nil {
			writeVaultError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, rec)
	})

	mux.HandleFunc("DELETE /api/v1/vault/credentials/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := v.Delete(r.Context(), r.PathValue("id")); err != nil {
			writeVaultError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("POST /api/v1/vault/credentials/{id}/grants", func(w http.ResponseWriter, r *http.Request) {
		var req vaultGrantRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		if strings.TrimSpace(req.ActorSubject) == "" {
			writeError(w, http.StatusBadRequest, "missing_subject", "actor_subject is required")
			return
		}
		if err := v.Grant(r.Context(), r.PathValue("id"), vault.Grant{
			ActorSubject: req.ActorSubject,
			ActorClient:  req.ActorClient,
		}); err != nil {
			writeVaultError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"ok": true})
	})

	mux.HandleFunc("GET /api/v1/vault/credentials/{id}/grants", func(w http.ResponseWriter, r *http.Request) {
		grants, err := v.Grants(r.Context(), r.PathValue("id"))
		if err != nil {
			writeVaultError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"grants": grants, "count": len(grants)})
	})

	mux.HandleFunc("DELETE /api/v1/vault/credentials/{id}/grants/{subject}", func(w http.ResponseWriter, r *http.Request) {
		client := r.URL.Query().Get("client")
		if err := v.Revoke(r.Context(), r.PathValue("id"), r.PathValue("subject"), client); err != nil {
			writeVaultError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("GET /api/v1/vault/credentials/{id}/usage", func(w http.ResponseWriter, r *http.Request) {
		entries, err := v.Usage(r.Context(), r.PathValue("id"), 100)
		if err != nil {
			writeVaultError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"usage": entries, "count": len(entries)})
	})
}

// writeVaultError maps vault sentinel errors onto HTTP codes.
func writeVaultError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, vault.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, vault.ErrDuplicate):
		writeError(w, http.StatusConflict, "duplicate", err.Error())
	case errors.Is(err, vault.ErrDenied):
		writeError(w, http.StatusForbidden, "denied", err.Error())
	case errors.Is(err, vault.ErrNoMatch):
		writeError(w, http.StatusNotFound, "no_match", err.Error())
	default:
		// Validation errors from Create surface here too — return 400.
		msg := err.Error()
		if strings.HasPrefix(msg, "vault: ") &&
			(strings.Contains(msg, "required") || strings.Contains(msg, "unknown credential type")) {
			writeError(w, http.StatusBadRequest, "invalid_input", msg)
			return
		}
		writeError(w, http.StatusInternalServerError, "vault_failed", msg)
	}
}
