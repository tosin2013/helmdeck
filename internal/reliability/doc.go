// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

// Package reliability houses opt-in reliability tests that prove
// helmdeck's architectural bets at the live-model level — currently
// the PR H model-recovery loop against a free OpenRouter model
// (v0.25.0 reliability arc).
//
// The real source files are all build-tagged `recovery`, so a
// default `go test ./...` compiles only this doc file and the
// package is a no-op for ordinary CI. To run the recovery tests:
//
//	go test -tags=recovery ./internal/reliability/...
//
// AND set HELMDECK_RECOVERY_TESTS=1 + OPENROUTER_API_KEY in the
// environment. See .github/workflows/model-recovery.yml for the
// nightly invocation, and the package-level docstring in
// scenarios.go for what each scenario tests.
package reliability
