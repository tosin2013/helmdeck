// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

import (
	"errors"
	"fmt"

	"github.com/tosin2013/helmdeck/internal/gateway"
	"github.com/tosin2013/helmdeck/internal/packs"
)

// dispatchError classifies a gateway chat-completion failure for an agent
// that needs to recover from it.
//
// A bad model/provider string is the *caller's* mistake — `minimax/…`
// when minimax is only reachable as `openrouter/minimax/…`, or a string
// missing the `provider/` prefix. The gateway returns ErrUnknownProvider
// or ErrInvalidModel for these. Surfacing them as the generic
// `handler_failed` (which ADR 008 reserves for buried exceptions) tells
// the agent nothing actionable, so it re-guesses and hallucinates another
// bad model. Instead we return CodeInvalidInput — which ADR 008 defines as
// "the caller can fix this and retry" — with a message that points at the
// helmdeck://models catalog so the next attempt uses a real model.
//
// Any other dispatch failure (network, provider 5xx, malformed response)
// stays handler_failed.
//
// label is a short site tag, e.g. "claim extractor dispatch". The
// underlying error is folded into Message (this is the field MCP/REST
// surface to the agent) and Cause is left nil — setting both would make
// PackError.Error() print the error twice, which is the
// "unknown provider: minimax: unknown provider: minimax" doubling the
// original wrapping produced.
func dispatchError(label string, err error) *packs.PackError {
	if errors.Is(err, gateway.ErrUnknownProvider) || errors.Is(err, gateway.ErrInvalidModel) {
		return &packs.PackError{
			Code: packs.CodeInvalidInput,
			Message: fmt.Sprintf(
				"%s: %v — pick a configured model from the helmdeck://models resource "+
					"(or GET /v1/models); use the full provider/model id, e.g. "+
					"openrouter/minimax/minimax-m2.7, not minimax/…",
				label, err),
		}
	}
	return &packs.PackError{Code: packs.CodeHandlerFailed, Message: fmt.Sprintf("%s: %v", label, err)}
}
