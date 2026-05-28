// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The helmdeck contributors

package builtin

// coldstart.go — retry overlay-backed HTTP calls while the target service is
// still booting. helmdeck's overlay services (Firecrawl, Docling, and any
// sidecar HTTP endpoint) come up asynchronously; the first request after the
// stack starts (or after a service restarts) can hit a not-yet-listening
// service and fail. From the OpenClaw chat UI that surfaces as a spurious
// failed pack/pipeline run for what is really a few-seconds readiness gap.
//
// coldStartRetry smooths that over: a connection-refused/reset dial error or
// a 502/503/504 is treated as "still starting" and retried with bounded
// exponential backoff. Genuine outcomes — success, 4xx, 500, a body the
// caller can read — return immediately and unchanged, so existing error
// handling (and the pipeline failure classifier) behave exactly as before
// once the service is actually up.

import (
	"context"
	"errors"
	"net/http"
	"syscall"
	"time"
)

const (
	coldStartMaxAttempts = 4
	coldStartBaseBackoff = 500 * time.Millisecond
)

// coldStartRetry issues reqFn() through client, retrying while the response
// looks like the service is still starting. reqFn MUST build a fresh request
// each call — a prior attempt may have consumed the request body. It returns
// the final (resp, err) so the caller's normal handling reports the true
// upstream state once retries are exhausted.
func coldStartRetry(ctx context.Context, client *http.Client, reqFn func() (*http.Request, error)) (*http.Response, error) {
	backoff := coldStartBaseBackoff
	for attempt := 1; ; attempt++ {
		req, err := reqFn()
		if err != nil {
			return nil, err
		}
		resp, doErr := client.Do(req)
		retryable := (doErr != nil && isColdStartErr(doErr)) ||
			(doErr == nil && isColdStartStatus(resp.StatusCode))
		if !retryable || attempt >= coldStartMaxAttempts {
			return resp, doErr
		}
		// Drain+close the cold response before retrying so the connection
		// can be reused and we don't leak it.
		if resp != nil {
			resp.Body.Close()
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
	}
}

// isColdStartStatus reports whether an HTTP status looks like the service is
// up but not ready (a front proxy returning 502/503/504), as opposed to a
// real application error (4xx/500) the caller should see.
func isColdStartStatus(code int) bool {
	switch code {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// isColdStartErr reports whether a transport error means "nothing is
// listening yet" — connection refused (service not bound) or reset (process
// came up mid-handshake). Genuine network failures and timeouts are NOT
// treated as cold-start: a connect timeout is more likely real slowness, and
// retrying it would just multiply the wait.
func isColdStartErr(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET)
}
