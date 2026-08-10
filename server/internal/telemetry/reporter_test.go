package telemetry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/config"
)

// recordingServer captures requests hitting the fake relay ingest.
type recordingServer struct {
	*httptest.Server
	mu       sync.Mutex
	count    int32
	lastBody []byte
}

func newRecordingServer(status int) *recordingServer {
	rs := &recordingServer{}
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := new(bytes.Buffer)
		_, _ = body.ReadFrom(r.Body)
		rs.mu.Lock()
		rs.lastBody = body.Bytes()
		rs.mu.Unlock()
		atomic.AddInt32(&rs.count, 1)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"accepted":true}`))
	}))
	return rs
}

func (rs *recordingServer) hits() int { return int(atomic.LoadInt32(&rs.count)) }
func (rs *recordingServer) body() []byte {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.lastBody
}

// fixedTime is a deterministic clock for opened_at assertions.
var fixedTime = time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)

func newTestReporter(url string) *Reporter {
	r := &Reporter{
		client:       &http.Client{Timeout: requestTimeout},
		url:          url,
		installID:    "install-123",
		machineFP:    machineFPHash("rawhardwareid"),
		version:      "1.2.3",
		initialDelay: time.Millisecond,
		interval:     time.Hour,
		now:          func() time.Time { return fixedTime },
	}
	return r
}

func TestMaybeStart_TeamEditionDoesNotStart(t *testing.T) {
	// Auth.Enabled=true is vendor-hosted team edition: reporter must not start.
	cfg := &config.Config{}
	cfg.Auth.Enabled = true
	cfg.Telemetry.Enabled = true
	if r := MaybeStart(context.Background(), cfg, "1.0.0"); r != nil {
		t.Fatalf("expected nil reporter when Auth.Enabled=true, got %#v", r)
	}

	// nil config also starts nothing.
	if r := MaybeStart(context.Background(), nil, "1.0.0"); r != nil {
		t.Fatalf("expected nil reporter for nil config, got %#v", r)
	}
}

func TestMaybeStart_PersonalEditionStarts(t *testing.T) {
	// Use a cancellable context and cancel before returning: MaybeStart spawns the
	// real run() goroutine pointed at the hardcoded PRODUCTION relay URL. Without
	// cancellation it would (after the initial delay) fire a real telemetry POST
	// from the test process and leak the goroutine.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := &config.Config{}
	cfg.Auth.Enabled = false
	cfg.Telemetry.Enabled = true
	r := MaybeStart(ctx, cfg, "1.0.0")
	if r == nil {
		t.Fatal("expected a reporter when Auth.Enabled=false")
	}
	if !r.enabled.Load() {
		t.Fatal("expected reporter enabled mirror to follow Telemetry.Enabled=true")
	}
}

// TestRun_InitialPingThenStopsOnCancel exercises the goroutine lifecycle: the
// delayed initial open ping fires, then ctx cancellation cleanly stops the loop.
func TestRun_InitialPingThenStopsOnCancel(t *testing.T) {
	rs := newRecordingServer(http.StatusAccepted)
	defer rs.Close()

	r := newTestReporter(rs.URL) // initialDelay=1ms, interval=1h
	r.SetEnabled(true)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.run(ctx); close(done) }()

	// Wait for the initial ping (initialDelay is 1ms).
	deadline := time.After(2 * time.Second)
	for rs.hits() == 0 {
		select {
		case <-deadline:
			t.Fatal("initial open ping was not sent")
		case <-time.After(2 * time.Millisecond):
		}
	}

	// Cancellation must unwind the loop promptly.
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("run did not stop on ctx cancel")
	}
}

func TestReporter_SkipsWhenTelemetryDisabled(t *testing.T) {
	rs := newRecordingServer(http.StatusAccepted)
	defer rs.Close()

	r := newTestReporter(rs.URL)
	r.SetEnabled(false) // master opt-out off

	r.reportOnce(context.Background())

	if got := rs.hits(); got != 0 {
		t.Fatalf("expected 0 POSTs when telemetry disabled, got %d", got)
	}
}

func TestReporter_SendsWhenEnabled(t *testing.T) {
	rs := newRecordingServer(http.StatusAccepted)
	defer rs.Close()

	r := newTestReporter(rs.URL)
	r.SetEnabled(true)

	r.reportOnce(context.Background())

	if got := rs.hits(); got != 1 {
		t.Fatalf("expected exactly 1 POST, got %d", got)
	}
}

// TestPayload_ExactWhitelist asserts the emitted JSON carries exactly the six
// fields the relay #364 contract whitelists — no more, no fewer.
func TestPayload_ExactWhitelist(t *testing.T) {
	rs := newRecordingServer(http.StatusAccepted)
	defer rs.Close()

	r := newTestReporter(rs.URL)
	r.SetEnabled(true)
	r.reportOnce(context.Background())

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rs.body(), &raw); err != nil {
		t.Fatalf("payload is not valid JSON object: %v", err)
	}
	got := make([]string, 0, len(raw))
	for k := range raw {
		got = append(got, k)
	}
	sort.Strings(got)
	want := []string{"arch", "install_id", "machine_fp_hash", "opened_at", "os", "version"}
	if len(got) != len(want) {
		t.Fatalf("payload keys = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("payload keys = %v, want %v", got, want)
		}
	}
}

// TestPayload_DecodesUnderRelayStrictContract mirrors the relay handler's
// DisallowUnknownFields decode: the reporter's body must decode cleanly into the
// exact contract struct with no unknown/trailing data.
func TestPayload_DecodesUnderRelayStrictContract(t *testing.T) {
	rs := newRecordingServer(http.StatusAccepted)
	defer rs.Close()

	r := newTestReporter(rs.URL)
	r.SetEnabled(true)
	r.reportOnce(context.Background())

	// Local mirror of relay's personalOpenEvent (#364).
	type contract struct {
		InstallID     string `json:"install_id"`
		MachineFPHash string `json:"machine_fp_hash"`
		Version       string `json:"version"`
		OS            string `json:"os"`
		Arch          string `json:"arch"`
		OpenedAt      string `json:"opened_at"`
	}
	dec := json.NewDecoder(bytes.NewReader(rs.body()))
	dec.DisallowUnknownFields()
	var ev contract
	if err := dec.Decode(&ev); err != nil {
		t.Fatalf("payload rejected by strict relay contract decode: %v", err)
	}
	if dec.More() {
		t.Fatal("payload has trailing data after the JSON object")
	}

	if ev.InstallID != "install-123" {
		t.Errorf("install_id = %q, want install-123", ev.InstallID)
	}
	if ev.Version != "1.2.3" {
		t.Errorf("version = %q, want 1.2.3", ev.Version)
	}
	if ev.OS != runtime.GOOS {
		t.Errorf("os = %q, want %q", ev.OS, runtime.GOOS)
	}
	if ev.Arch != runtime.GOARCH {
		t.Errorf("arch = %q, want %q", ev.Arch, runtime.GOARCH)
	}
	// opened_at must be RFC3339 and match the fixed clock.
	parsed, err := time.Parse(time.RFC3339, ev.OpenedAt)
	if err != nil {
		t.Fatalf("opened_at %q is not RFC3339: %v", ev.OpenedAt, err)
	}
	if !parsed.Equal(fixedTime) {
		t.Errorf("opened_at = %v, want %v", parsed, fixedTime)
	}
}

func TestMachineFPHash_SaltedAndStable(t *testing.T) {
	// Pure helper: empty seed -> empty hash. Non-empty seeds are guaranteed by
	// deriveMachineFP (see TestDeriveMachineFP_FallsBackToInstallID).
	if h := machineFPHash(""); h != "" {
		t.Fatalf("machineFPHash(\"\") = %q, want empty", h)
	}

	const raw = "ABCDEF-hardware-id"
	h := machineFPHash(raw)

	// Never the raw id.
	if h == raw {
		t.Fatal("hash must not equal the raw fingerprint")
	}
	// Salted SHA-256 hex: 64 chars, deterministic.
	if len(h) != 64 {
		t.Fatalf("hash length = %d, want 64", len(h))
	}
	want := sha256.Sum256([]byte(fpSalt + raw))
	if h != hex.EncodeToString(want[:]) {
		t.Fatalf("hash = %s, want salted sha256", h)
	}
	// Stable across calls (de-dup across restarts/reinstalls).
	if machineFPHash(raw) != h {
		t.Fatal("hash must be stable for the same fingerprint")
	}
}

// TestDeriveMachineFP_FallsBackToInstallID guards the #364/#365 contract: the
// relay makes machine_fp_hash MANDATORY (empty -> 400), so the reporter must
// never emit an empty hash. When the raw hardware fingerprint is unavailable we
// seed from the install id instead, keeping the event acceptable.
func TestDeriveMachineFP_FallsBackToInstallID(t *testing.T) {
	// Hardware id available -> hash of the hardware id (unchanged behavior).
	if got, want := deriveMachineFP("hw-123", "install-abc"), machineFPHash("hw-123"); got != want {
		t.Fatalf("deriveMachineFP with hardware id = %q, want %q", got, want)
	}

	// Hardware id unavailable -> non-empty fallback seeded from install id, so
	// the relay's mandatory machine_fp_hash check passes and the event survives.
	fallback := deriveMachineFP("", "install-abc")
	if fallback == "" {
		t.Fatal("deriveMachineFP(\"\", installID) must not be empty -- relay rejects empty machine_fp_hash")
	}
	if want := machineFPHash("install-abc"); fallback != want {
		t.Fatalf("fallback hash = %q, want hash of install id %q", fallback, want)
	}

	// Both seeds empty is the only degenerate case that yields empty (install_id
	// is itself required, so such an event is unusable regardless).
	if got := deriveMachineFP("", ""); got != "" {
		t.Fatalf("deriveMachineFP(\"\",\"\") = %q, want empty", got)
	}
}

func TestReporter_FailureIsSilentAndNonBlocking(t *testing.T) {
	// Relay returns 400: post() reports failure; with no retry backoffs the call
	// still returns promptly and never panics. Swap retryBackoffs to empty so the
	// test doesn't actually sleep.
	rs := newRecordingServer(http.StatusBadRequest)
	defer rs.Close()

	saved := retryBackoffs
	retryBackoffs = nil
	defer func() { retryBackoffs = saved }()

	r := newTestReporter(rs.URL)
	r.SetEnabled(true)

	done := make(chan struct{})
	go func() {
		r.reportOnce(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reportOnce blocked on a failing relay")
	}
	if rs.hits() == 0 {
		t.Fatal("expected at least one POST attempt")
	}
}

func TestRelayURL_Hardcode(t *testing.T) {
	const want = "https://niuniu-relay.niu6ai.com/api/telemetry/personal"
	if relayTelemetryURL != want {
		t.Fatalf("relayTelemetryURL = %q, want %q", relayTelemetryURL, want)
	}
}
