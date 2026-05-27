// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package pipelines

import (
	"errors"
	"fmt"
	"net/url"

	"github.com/tosin2013/helmdeck/internal/packs"
)

// Failure classes attach a "whose fault / what now" to a failed step, so
// a run reads like a CI/CD job: an operator (or agent) can tell at a
// glance whether to fix their input, re-run, or file a bug.
const (
	// FailureCallerFixable — the inputs/model given to the step were
	// invalid. The caller (often the agent) can fix them and re-run.
	FailureCallerFixable = "caller_fixable"
	// FailurePackBug — a code error inside helmdeck (a pack handler
	// failed in an uncategorized way, violated its own output contract,
	// or hit an engine invariant). Not the caller's fault → file an issue.
	FailurePackBug = "pack_bug"
	// FailureTransient — an environment/infra blip (timeout, session
	// acquisition, artifact store). Re-running may simply succeed.
	FailureTransient = "transient"
	// FailureStateChanged — the external state the step acted on changed
	// under it (e.g. a non-fast-forward push). Refresh and re-run.
	FailureStateChanged = "state_changed"
)

// helmdeckIssueRepo is where pack-bug reports are filed.
const helmdeckIssueRepo = "tosin2013/helmdeck"

// classify turns a step failure into (typed code, failure class, a
// one-line human reason + recommended action). pack is the failing
// step's pack name, used in the pack-bug issue link.
//
// The typed code is pulled from the *packs.PackError the engine returns.
// A non-PackError (e.g. an unresolved ${{ }} reference or an unknown
// pack — both pipeline-definition problems) is caller-fixable.
func classify(err error, pack string) (code packs.ErrorCode, class, reason string) {
	var pe *packs.PackError
	if !errors.As(err, &pe) {
		// Runner-level failure: bad template reference or missing pack —
		// the pipeline definition is wrong, which the author can fix.
		return "", FailureCallerFixable,
			"The pipeline definition or step wiring is invalid (e.g. an unresolved ${{ … }} reference or an unregistered pack). Fix the pipeline and re-run."
	}
	code = pe.Code
	switch code {
	case packs.CodeInvalidInput:
		class = FailureCallerFixable
		reason = "The inputs given to this step were invalid. Fix them and re-run — the step's error message says what was wrong."
	case packs.CodeSchemaMismatch:
		class = FailureStateChanged
		reason = "The target changed under this step (e.g. the branch moved). Refresh and re-run."
	case packs.CodeTimeout, packs.CodeSessionUnavailable, packs.CodeArtifactFailed:
		class = FailureTransient
		reason = "A transient/environment error (timeout, session, or artifact store). Re-running may succeed."
	default: // CodeHandlerFailed, CodeInvalidOutput, CodeInternal, unknown
		class = FailurePackBug
		reason = fmt.Sprintf(
			"This looks like a bug in the %q pack inside helmdeck, not your input. Please open an issue: %s",
			pack, issueURL(pack, code, pe.Message))
	}
	return code, class, reason
}

// isRetryable reports whether a failure class is safe to auto-retry
// (ADR 044 slice 2). Lives here now as the seam the retry policy keys off.
func isRetryable(code packs.ErrorCode) bool {
	switch code {
	case packs.CodeTimeout, packs.CodeSessionUnavailable, packs.CodeArtifactFailed:
		return true
	default:
		return false
	}
}

// issueURL builds a prefilled GitHub "new issue" link for a pack bug so
// the operator files a useful report in one click.
func issueURL(pack string, code packs.ErrorCode, msg string) string {
	const maxMsg = 300
	if len(msg) > maxMsg {
		msg = msg[:maxMsg] + "…"
	}
	title := fmt.Sprintf("pack %s failed (%s)", pack, code)
	body := fmt.Sprintf(
		"A pipeline step failed with a code-level error.\n\n- pack: `%s`\n- error code: `%s`\n- message: %s\n\n(What were you running? Any inputs we can use to reproduce?)",
		pack, code, msg)
	q := url.Values{}
	q.Set("title", title)
	q.Set("body", body)
	q.Set("labels", "bug")
	return "https://github.com/" + helmdeckIssueRepo + "/issues/new?" + q.Encode()
}
