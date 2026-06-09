// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// model_defaults.go — shared default-model resolver for packs whose
// `model` input is a load-bearing string but whose callers (Tier C
// LLMs, skill prose, ad-hoc invocations) routinely omit it.
//
// Why this exists: every pack that takes a model argument has had
// the same Tier C failure mode — the skill says "call content.ground
// with the source markdown," the pack contract says "you must pass
// `model: \"provider/model\"`," the small open-weight model on the
// receiving end omits the parameter and the validation layer rejects
// the call. Operator pastes the trace, helmdeck audits the skill
// prose, the skill gets a sentence reminding the agent to pass the
// model, three weeks later a different skill makes the same mistake.
//
// Same architectural shape as `artifact.put` (PR #450) and the
// av-validation arc (ADR 052): turn an advisory step ("remember to
// pass the model") into a deterministic default applied at the pack
// layer. The operator's `--model` flag still wins; the agent's
// explicit input still wins; only when both are absent does the
// default fire.
//
// Resolution precedence (first non-empty wins):
//
//   1. explicit `model` field on the pack input (caller's intent)
//   2. HELMDECK_DEFAULT_PACK_MODEL env (operator-side global override)
//   3. first entry of HELMDECK_OPENROUTER_MODELS env, prefixed with
//      "openrouter/" if not already (reuses the existing gateway-side
//      model registry pin from internal/gateway/hydrate_openrouter.go)
//   4. hard fallback: "openrouter/auto"
//
// The hard fallback is `openrouter/auto` rather than a specific free
// model because (a) it routes through OpenRouter's per-call provider
// selection which is generally available on every deployment that
// has HELMDECK_OPENROUTER_API_KEY set, and (b) it preserves the
// existing project posture that the gateway prefers `auto` for
// orchestration work (helmdeck.plan ADR 053 PromptVariantFullSteps
// runs on auto by default).
//
// Why a hard fallback at all rather than returning CodeInvalidInput
// when the env vars are unset: the typical zero-config dev experience
// is a fresh helmdeck stack with HELMDECK_OPENROUTER_API_KEY set
// (because that's the only way the gateway works) and no model
// override. The Tier C silent-skip mode means an agent calling a
// pack on that stack would hit "model is required" with no hint of
// what value to pass. Defaulting to `openrouter/auto` makes the
// pack succeed at the cost of using more tokens than a hand-tuned
// model choice would. Operators who want a different default set
// HELMDECK_DEFAULT_PACK_MODEL once at the stack level.

import (
	"os"
	"strings"
)

// defaultPackModel returns the model a pack handler should use when
// the caller did not supply one. See file-level docstring for
// resolution precedence. The caller's explicit value is passed
// through verbatim — this function is only meant for the empty-input
// case.
//
// callerInput is the model field from the pack's own input struct
// (typically `in.Model`). When non-empty after trimming, it is
// returned unchanged so the function is safe to wire into any
// handler's input-resolution prologue without changing existing
// behavior:
//
//   in.Model = defaultPackModel(in.Model)
//
// Returns a string guaranteed to be non-empty (the openrouter/auto
// hard fallback ensures this).
func defaultPackModel(callerInput string) string {
	if v := strings.TrimSpace(callerInput); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("HELMDECK_DEFAULT_PACK_MODEL")); v != "" {
		return v
	}
	if v := firstOpenrouterModel(os.Getenv("HELMDECK_OPENROUTER_MODELS")); v != "" {
		return v
	}
	return "openrouter/auto"
}

// firstOpenrouterModel pulls the first entry off the comma-separated
// HELMDECK_OPENROUTER_MODELS env var and prefixes it with
// "openrouter/" when not already. Mirrors the parsing in
// internal/gateway/hydrate_openrouter.go's parseModels so a stack
// running `HELMDECK_OPENROUTER_MODELS=auto,foo,bar` gets `auto`
// resolved consistently on both the gateway-registration path AND
// the pack-default path.
//
// Returns empty when the env var is unset OR contains only
// whitespace/commas.
func firstOpenrouterModel(env string) string {
	for _, raw := range strings.Split(env, ",") {
		m := strings.TrimSpace(raw)
		if m == "" {
			continue
		}
		if strings.HasPrefix(m, "openrouter/") {
			return m
		}
		return "openrouter/" + m
	}
	return ""
}
