// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// json_response.go — defensive JSON parsing for LLM-backed packs
// (ADR 051 PR #1).
//
// Today every LLM-backed pack (plan, route, content_ground, webtest,
// blog.rewrite_for_audience, slides.outline, …) has slightly
// different code for "the model returned text, parse it as JSON,
// recover from common prose-wrap / markdown-fence / trailing-garbage
// quirks." The patterns are equivalent in intent; the implementations
// drift. ADR 050 PR #4's json.Decoder + substring-extraction fallback
// lives only in plan.go; route.go is still on strict json.Unmarshal;
// content_ground.go has its own substring fallback.
//
// DecodeStructuredResponse is the consolidated path. It applies, in
// order:
//
//   1. StripReasoningTokens   — drop <think>/<reasoning>/[REASONING] blocks
//                                that hybrid models (Claude 3.7 Sonnet
//                                thinking, o3-mini, DeepSeek V4 Pro,
//                                Kimi K2 series) emit BEFORE their JSON
//   2. TrimSpace               — leading/trailing whitespace
//   3. unwrapCodeFence         — strip ```json…``` wrappers (existing helper)
//   4. json.Decoder.Decode     — reads one complete JSON value and stops,
//                                tolerates trailing prose/HTML/markdown that
//                                weak models sometimes emit after the
//                                structured payload
//   5. extractFirstJSONObject  — balanced-brace substring extraction
//                                (handles `}` inside JSON string literals;
//                                better than naive LastIndex). Falls back
//                                to json.Unmarshal on the extracted slice
//
// Returns a typed *packs.PackError so callers plug the error directly
// into their handler return. Empty input is treated as a distinct
// failure with the message string callers were using before
// consolidation, so existing string-equal error matching (in tests
// and downstream tooling) keeps working.

import (
	"encoding/json"
	"strings"

	"github.com/tosin2013/helmdeck/internal/llmcontext"
	"github.com/tosin2013/helmdeck/internal/packs"
)

// DecodeStructuredResponse parses a model's textual response into v
// applying the defensive pipeline described in the file header.
// `packName` is used in the error Message ("gateway returned an empty
// <packName> response") for parity with the strings handlers emitted
// before consolidation.
//
// Returns nil on success, a PackError on failure. The Code is always
// CodeHandlerFailed because parse failure is a server-observable model
// quirk; downstream handlers can wrap the error to add semantic
// context.
func DecodeStructuredResponse(rawBody, packName string, v interface{}) *packs.PackError {
	body := llmcontext.StripReasoningTokens(rawBody)
	body = unwrapCodeFence(strings.TrimSpace(body))
	if body == "" {
		return &packs.PackError{
			Code:    packs.CodeHandlerFailed,
			Message: "gateway returned an empty " + packName + " response",
		}
	}

	// Primary: streaming decoder reads one value and stops. Tolerates
	// trailing prose/HTML/markup that weak models often emit.
	dec := json.NewDecoder(strings.NewReader(body))
	if derr := dec.Decode(v); derr == nil {
		return nil
	} else {
		// Secondary: balanced-brace extraction handles cases where
		// the model wrapped JSON in prose ("Sure, here's your answer:
		// {…} let me know if you need anything else"). Reuses the
		// brace-counting + string-aware helper from webtest.go.
		if obj := extractFirstJSONObject(body); obj != "" {
			if jerr := json.Unmarshal([]byte(obj), v); jerr == nil {
				return nil
			}
		}
		// Both paths exhausted. Surface the streaming-decoder error
		// (it's the most descriptive of "where parsing broke").
		return &packs.PackError{
			Code:    packs.CodeHandlerFailed,
			Message: "model output is not valid JSON: " + derr.Error(),
			Cause:   derr,
		}
	}
}
