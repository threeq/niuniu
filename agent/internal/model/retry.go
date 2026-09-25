package model

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"time"
)

// API resilience: 429/5xx/network errors are retried with exponential
// backoff before the FIRST byte of the response body is consumed — a
// request that already streamed partial content is never replayed (the
// caller would see duplicated output). 4xx (except 429) fails fast:
// semantic errors don't heal on retry.

// RetryPolicy bounds one model client's retry behavior. Base is the first
// backoff step; each retry doubles it up to MaxWait. Zero Max → retries
// disabled (tests, offline tools).
type RetryPolicy struct {
	Max     int
	Base    time.Duration
	MaxWait time.Duration
}

// DefaultRetryPolicy: 3 attempts beyond the initial call, 1s→2s→4s (capped
// at 30s), honoring Retry-After when the server sends one.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{Max: 3, Base: 1 * time.Second, MaxWait: 30 * time.Second}
}

// retryable reports whether an HTTP status should be retried.
func retryable(status int) bool {
	return status == http.StatusTooManyRequests ||
		status == http.StatusInternalServerError ||
		status == http.StatusBadGateway ||
		status == http.StatusServiceUnavailable ||
		status == http.StatusGatewayTimeout
}

// backoffFor computes the wait before the next attempt: Retry-After (when
// parseable) caps the exponential step from below and above — the server's
// hint wins over our guess, bounded by MaxWait.
func (p RetryPolicy) backoffFor(attempt int, retryAfter string, now time.Time) time.Duration {
	d := p.Base << attempt // 1x, 2x, 4x …
	if d > p.MaxWait {
		d = p.MaxWait
	}
	if retryAfter != "" {
		if secs, err := strconv.Atoi(retryAfter); err == nil && secs >= 0 {
			hint := time.Duration(secs) * time.Second
			if hint > p.MaxWait {
				hint = p.MaxWait
			}
			if hint > d {
				d = hint
			}
		}
	}
	return d
}

// doWithRetry performs the HTTP round trip with retry/backoff. buildReq is
// re-invoked per attempt so the request body reader is fresh each time. The
// response body is fully buffered before returning, so callers keep their
// existing parse path; retries happen only before any body byte is consumed
// by the caller.
func doWithRetry(ctx context.Context, hc *http.Client, policy RetryPolicy, endpoint string, buildReq func() (*http.Request, error)) (*http.Response, []byte, error) {
	for attempt := 0; ; attempt++ {
		req, err := buildReq()
		if err != nil {
			return nil, nil, err
		}
		resp, err := hc.Do(req)
		if err != nil {
			if attempt < policy.Max && ctx.Err() == nil {
				time.Sleep(policy.backoffFor(attempt, "", time.Now()))
				continue
			}
			return nil, nil, err
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
		resp.Body.Close()
		if err != nil {
			return nil, nil, err
		}
		if retryable(resp.StatusCode) && attempt < policy.Max && ctx.Err() == nil {
			time.Sleep(policy.backoffFor(attempt, resp.Header.Get("Retry-After"), time.Now()))
			continue
		}
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return resp, body, nil
	}
}
