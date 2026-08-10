// Package telemetry implements the anonymous personal-edition "app opened"
// reporter (Epic #329 / Wave 2, issue #365).
//
// On personal / self-hosted single-user instances (Auth.Enabled=false) it sends
// one anonymous open ping shortly after boot and a keep-alive heartbeat every
// 24h, so a long-running session still counts one active day per day. The
// payload is the minimal v2 whitelist defined by the relay ingest contract
// (#364) — no usage aggregates, no PII, no DB queries. Vendor-hosted team
// edition (Auth.Enabled=true) never starts the reporter.
package telemetry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/niuniu-dev/niuniu/internal/config"
	"github.com/niuniu-dev/niuniu/internal/relayclient"
)

// relayTelemetryURL is the hardcoded relay ingest endpoint. It mirrors the
// niu6ai.com hardcode style used by version-check.ts / service.defaultRelayURL,
// kept here as the single source of truth for the telemetry path.
const relayTelemetryURL = "https://niuniu-relay.niu6ai.com/api/telemetry/personal"

// fpSalt is a fixed app-wide salt mixed into the machine fingerprint before
// hashing. It makes machine_fp_hash stable across restarts/reinstalls (so the
// vendor can de-dup the same machine) while never exposing the raw hardware id.
const fpSalt = "niuniu-personal-telemetry-v1"

// Timing constants. The initial delay lets the ready handshake settle before
// the first ping; the interval is the keep-alive heartbeat period.
const (
	defaultInitialDelay = 10 * time.Second
	defaultInterval     = 24 * time.Hour

	// requestTimeout keeps each POST short so the goroutine never lingers.
	requestTimeout = 5 * time.Second
)

// retryBackoffs drives the silent retry on a failed send: a couple of short,
// increasing pauses, then give up until the next tick. Best-effort by design —
// telemetry must never block or noisily log on the main flow.
var retryBackoffs = []time.Duration{2 * time.Second, 5 * time.Second}

// openEvent is the exact v2 payload whitelist. The json tags MUST stay byte-for-
// byte aligned with the relay handler's personalOpenEvent (#364), which decodes
// with DisallowUnknownFields — any extra/renamed field is rejected with 400.
type openEvent struct {
	InstallID     string `json:"install_id"`
	MachineFPHash string `json:"machine_fp_hash"`
	Version       string `json:"version"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	OpenedAt      string `json:"opened_at"`
}

// Reporter sends the open ping + keep-alive heartbeats. Construct it via
// MaybeStart; the zero value is not usable.
type Reporter struct {
	client    *http.Client
	url       string
	installID string
	machineFP string // already salted+hashed; never empty (install_id fallback when no hardware id)
	version   string

	// enabled is the live master opt-out (config.Telemetry.Enabled). It is read
	// on every tick so #366's /api/config setter can flip reporting off without a
	// restart. atomic so the setter and the loop never race.
	enabled atomic.Bool

	initialDelay time.Duration
	interval     time.Duration
	now          func() time.Time
}

// MaybeStart launches the reporter goroutine when telemetry applies and returns
// the running *Reporter. It returns nil (and starts nothing) for vendor-hosted
// team edition (Auth.Enabled=true). Telemetry.Enabled is captured here and then
// re-checked on every tick inside the loop, so the master opt-out can flip at
// runtime; #366 wires its setter to Reporter.SetEnabled.
//
// version is the build-time app version (api.Version) passed by the caller to
// avoid an import cycle.
func MaybeStart(ctx context.Context, cfg *config.Config, version string) *Reporter {
	if cfg == nil || cfg.Auth.Enabled {
		return nil
	}
	r := newReporter(cfg, version)
	go r.run(ctx)
	return r
}

// newReporter builds a Reporter from config with production defaults. The
// fingerprint is hashed once at construction (it is stable for the process) and
// the live opt-out mirror is seeded from cfg.Telemetry.Enabled.
func newReporter(cfg *config.Config, version string) *Reporter {
	r := &Reporter{
		client:       &http.Client{Timeout: requestTimeout},
		url:          relayTelemetryURL,
		installID:    cfg.Server.ID,
		machineFP:    deriveMachineFP(relayclient.MachineFingerprint(), cfg.Server.ID),
		version:      version,
		initialDelay: defaultInitialDelay,
		interval:     defaultInterval,
		now:          time.Now,
	}
	r.enabled.Store(cfg.Telemetry.Enabled)
	return r
}

// SetEnabled flips the live master opt-out. Safe to call from any goroutine
// (e.g. the #366 /api/config setter).
func (r *Reporter) SetEnabled(v bool) { r.enabled.Store(v) }

// machineFPHash returns the salted SHA-256 (hex) of the given seed, or "" for an
// empty seed. It is a pure hashing helper; callers use deriveMachineFP to supply
// a non-empty seed.
func machineFPHash(fp string) string {
	if fp == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(fpSalt + fp))
	return hex.EncodeToString(sum[:])
}

// deriveMachineFP produces the machine_fp_hash carried in the open event. The
// relay contract (#364) makes machine_fp_hash MANDATORY and rejects an empty
// value with 400, so when the raw hardware fingerprint is unavailable (e.g.
// containers, a Linux box without /etc/machine-id, a locked-down Windows) we
// fall back to seeding the hash from the stable per-install id (cfg.Server.ID).
// That keeps the event acceptable so DAU stays accurate; it merely forgoes
// cross-reinstall folding for that machine, which is impossible without a real
// hardware id anyway. Returns "" only if both seeds are empty (a degenerate
// case where install_id is also empty and the event is unusable regardless).
func deriveMachineFP(rawFP, installID string) string {
	seed := rawFP
	if seed == "" {
		seed = installID
	}
	return machineFPHash(seed)
}

// run drives the initial delayed ping then the 24h heartbeat loop. It mirrors
// the maintenance / daily-prune goroutine pattern in server.go: a single long-
// lived goroutine that exits on ctx cancellation.
func (r *Reporter) run(ctx context.Context) {
	// Initial open ping, delayed to avoid the ready handshake window.
	select {
	case <-ctx.Done():
		return
	case <-time.After(r.initialDelay):
	}
	r.reportOnce(ctx)

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.reportOnce(ctx)
		}
	}
}

// reportOnce builds and best-effort sends one open event. It is a no-op when the
// master opt-out is off this tick. Never returns an error: telemetry failures
// are swallowed so they can never affect the main flow.
func (r *Reporter) reportOnce(ctx context.Context) {
	if !r.enabled.Load() {
		return
	}
	body, err := json.Marshal(openEvent{
		InstallID:     r.installID,
		MachineFPHash: r.machineFP,
		Version:       r.version,
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		OpenedAt:      r.now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return
	}
	r.postWithRetry(ctx, body)
}

// postWithRetry posts the body, silently retrying a few times with short
// backoffs before giving up until the next tick.
func (r *Reporter) postWithRetry(ctx context.Context, body []byte) {
	if r.post(ctx, body) {
		return
	}
	for _, backoff := range retryBackoffs {
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if r.post(ctx, body) {
			return
		}
	}
}

// post sends a single POST and reports whether the relay accepted it (202).
func (r *Reporter) post(ctx context.Context, body []byte) bool {
	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, r.url, bytes.NewReader(body))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusAccepted
}
