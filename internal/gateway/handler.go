package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
)

// Dispatcher is the surface the HTTP handler depends on. *Registry
// satisfies it directly; *Chain wraps a Registry to add fallback rules
// (T204) and also satisfies it, so the handler is identical in either
// configuration.
type Dispatcher interface {
	Dispatch(ctx context.Context, req ChatRequest) (ChatResponse, error)
	AllModels(ctx context.Context) ([]Model, error)
}

// Handler returns an http.Handler that serves the OpenAI-compatible
// surface. It is mounted by the api package under the existing JWT-
// protected /v1/* prefix.
func Handler(reg Dispatcher) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		models, err := reg.AllModels(r.Context())
		if err != nil {
			writeOpenAIError(w, http.StatusBadGateway, "provider_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"object": "list",
			"data":   models,
		})
	})

	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
			return
		}
		if req.Stream {
			// Streaming SSE lands with the provider adapters in T202;
			// surfacing this as 501 keeps the contract honest instead of
			// silently returning a non-streamed body.
			writeOpenAIError(w, http.StatusNotImplemented, "stream_unsupported", "streaming responses are not yet implemented")
			return
		}
		if len(req.Messages) == 0 {
			writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "messages must not be empty")
			return
		}
		resp, err := reg.Dispatch(r.Context(), req)
		if err != nil {
			switch {
			case errors.Is(err, ErrInvalidModel):
				writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
			case errors.Is(err, ErrUnknownProvider):
				writeOpenAIError(w, http.StatusNotFound, "model_not_found", err.Error())
			default:
				writeOpenAIError(w, http.StatusBadGateway, "provider_error", err.Error())
			}
			return
		}
		if resp.ID == "" {
			resp.ID = "chatcmpl-" + randomID()
		}
		writeJSON(w, http.StatusOK, resp)
	})

	return mux
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

// writeOpenAIError emits the nested {"error": {...}} envelope OpenAI
// clients expect, not the flat shape used by /api/v1/* helmdeck
// endpoints. SDKs like the official openai-python rely on this exact
// shape for their typed exceptions.
func writeOpenAIError(w http.ResponseWriter, code int, kind, message string) {
	writeJSON(w, code, map[string]any{
		"error": map[string]string{
			"type":    kind,
			"message": message,
		},
	})
}

func randomID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
