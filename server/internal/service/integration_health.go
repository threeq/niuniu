package service

// Continuous integration-source health (Epic #726 / #744).
//
// The verify probe of #732 only runs once, at publish time. This service runs
// it on a schedule instead: it pings every Provider (via the contract's
// verify_operation, through the real proxy so auth + scope are exercised) and
// every DataSource (the existing connection Verify), records the verdict on the
// source row, watches the upstream OpenAPI document for drift, and switches a
// source off after N consecutive failures -- notifying the user so a dead
// upstream or a revoked credential surfaces before an agent hits the wall.
//
// Scheduling reuses the existing scheduler: server.go registers
// Scheduler.RegisterHealthSweep, which adds one cron entry to the scheduler's
// own cron engine (no second scheduler is created). Probe history goes to
// integration_probe_runs, the health analog of schedule_runs (that table is
// workspace-bound, and integration sources are not).

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// Health statuses stored on the source rows.
const (
	HealthUnknown   = "unknown"
	HealthHealthy   = "healthy"
	HealthUnhealthy = "unhealthy"
	HealthDisabled  = "disabled"
)

// Probe run statuses (integration_probe_runs.status).
const (
	ProbeStatusOK      = "ok"
	ProbeStatusFailed  = "failed"
	ProbeStatusDrift   = "drift"
	ProbeStatusSkipped = "skipped"
)

// Probe subjects.
const (
	ProbeSubjectProvider   = "provider"
	ProbeSubjectDataSource = "data_source"
)

// Alert kinds for IntegrationHealthAlert.
const (
	AlertAutoDisabled = "auto_disabled"
	AlertDrift        = "drift"
	AlertRecovered    = "recovered"
)

// IntegrationHealthAlert is what a notifier receives when a source needs the
// user's attention. The notifier decides how to deliver it (station-in hub,
// IM Bot, org audit); the service carries only facts, never a rendering.
type IntegrationHealthAlert struct {
	SubjectKind string
	SubjectID   int64
	OwnerType   string
	OwnerID     int64
	Name        string
	Kind        string // AlertAutoDisabled | AlertDrift
	Message     string
}

// IntegrationHealthNotifier delivers health alerts. Wired in server.go so this
// package stays free of the notify hub and the IM Bot service. A nil notifier
// disables alerting (probes still run and record).
type IntegrationHealthNotifier interface {
	NotifyIntegrationHealth(ctx context.Context, alert IntegrationHealthAlert)
}

// IntegrationHealthConfig bounds the sweep. Zero values fall back to
// DefaultIntegrationHealthConfig.
type IntegrationHealthConfig struct {
	Enabled          bool
	DefaultInterval  time.Duration
	FailureThreshold int64
}

// DefaultIntegrationHealthConfig is the shipped default: probe daily, disable
// after 3 consecutive failures.
func DefaultIntegrationHealthConfig() IntegrationHealthConfig {
	return IntegrationHealthConfig{
		Enabled:          true,
		DefaultInterval:  24 * time.Hour,
		FailureThreshold: 3,
	}
}

// normalized fills zero values with defaults so callers can pass a partial
// config (e.g. only FailureThreshold from the config file).
func (c IntegrationHealthConfig) normalized() IntegrationHealthConfig {
	d := DefaultIntegrationHealthConfig()
	if c.DefaultInterval <= 0 {
		c.DefaultInterval = d.DefaultInterval
	}
	if c.FailureThreshold <= 0 {
		c.FailureThreshold = d.FailureThreshold
	}
	return c
}

// ProbeOutcome is the result of a single probe, returned to callers (the
// manual-probe handler) and recorded in integration_probe_runs.
type ProbeOutcome struct {
	SubjectKind         string `json:"subject_kind"`
	SubjectID           int64  `json:"subject_id"`
	Status              string `json:"status"`
	Message             string `json:"message"`
	LatencyMs           int64  `json:"latency_ms"`
	Drift               bool   `json:"drift"`
	ConsecutiveFailures int64  `json:"consecutive_failures"`
	AutoDisabled        bool   `json:"auto_disabled"`
	ContractDigest      string `json:"contract_digest,omitempty"`
	UpstreamDigest      string `json:"upstream_digest,omitempty"`
}

// IntegrationHealthService runs and records integration-source health probes.
type IntegrationHealthService struct {
	q             *store.Queries
	db            *store.DB
	providerSvc   *ExternalProviderService
	proxySvc      *ExternalProxyService
	dataSourceSvc *DataSourceService
	client        *http.Client
	notifier      IntegrationHealthNotifier
	cfg           IntegrationHealthConfig

	// sweepMu serializes sweeps so a manual probe-now cannot race the cron
	// sweep into double-counting a failure streak.
	sweepMu sync.Mutex
}

// NewIntegrationHealthService wires the service. rawDB is wrapped into a
// *store.DB (placeholder rewriting for Postgres).
func NewIntegrationHealthService(
	q *store.Queries,
	rawDB *sql.DB,
	providerSvc *ExternalProviderService,
	proxySvc *ExternalProxyService,
	dataSourceSvc *DataSourceService,
	cfg IntegrationHealthConfig,
) *IntegrationHealthService {
	return &IntegrationHealthService{
		q:             q,
		db:            store.Wrap(rawDB),
		providerSvc:   providerSvc,
		proxySvc:      proxySvc,
		dataSourceSvc: dataSourceSvc,
		// SSRF-guarded: openapi_url is user-supplied, so the drift fetch must
		// not be able to reach loopback / link-local / cloud metadata.
		client: NewSSRFGuardedClient(20*time.Second, 20*time.Second),
		cfg:    cfg.normalized(),
	}
}

// SetNotifier installs the alert delivery hook. Safe to call after New.
func (s *IntegrationHealthService) SetNotifier(n IntegrationHealthNotifier) { s.notifier = n }

// RunSweep probes every due source. Registered on the scheduler's cron engine
//; errors are logged, never fatal, so one bad source cannot stop the sweep.
func (s *IntegrationHealthService) RunSweep(ctx context.Context) {
	if !s.cfg.Enabled {
		return
	}
	s.sweepMu.Lock()
	defer s.sweepMu.Unlock()

	s.sweepProviders(ctx)
	s.sweepDataSources(ctx)
}

func (s *IntegrationHealthService) sweepProviders(ctx context.Context) {
	rows, err := s.q.ListProvidersForProbe(ctx)
	if err != nil {
		slog.Warn("integration-health: list providers for probe failed", "error", err)
		return
	}
	for _, p := range rows {
		if !probeDue(p.LastProbeAt, p.ProbeIntervalSeconds, s.cfg.DefaultInterval) {
			continue
		}
		if _, err := s.ProbeProvider(ctx, p.ID); err != nil {
			slog.Warn("integration-health: provider probe error", "provider_id", p.ID, "error", err)
		}
	}
}

func (s *IntegrationHealthService) sweepDataSources(ctx context.Context) {
	rows, err := s.q.ListDataSourcesForProbe(ctx)
	if err != nil {
		slog.Warn("integration-health: list data sources for probe failed", "error", err)
		return
	}
	for _, d := range rows {
		if !probeDue(d.LastProbeAt, d.ProbeIntervalSeconds, s.cfg.DefaultInterval) {
			continue
		}
		if _, err := s.ProbeDataSource(ctx, d.ID); err != nil {
			slog.Warn("integration-health: data source probe error", "source_id", d.ID, "error", err)
		}
	}
}

// probeDue reports whether a row is due for its next probe.
func probeDue(lastProbe sql.NullTime, intervalSeconds int64, fallback time.Duration) bool {
	if !lastProbe.Valid {
		return true
	}
	interval := fallback
	if intervalSeconds > 0 {
		interval = time.Duration(intervalSeconds) * time.Second
	}
	return time.Since(lastProbe.Time) >= interval
}

// ProbeProvider runs the contract's verify_operation for one provider through
// the proxy, records the verdict, applies the failure policy, and returns the
// outcome. Authz/whitelist blocks are recorded as "skipped" (never a health
// failure) -- a probe that the permission gate refuses says nothing about the
// upstream's health.
func (s *IntegrationHealthService) ProbeProvider(ctx context.Context, providerID int64) (*ProbeOutcome, error) {
	p, err := s.q.GetProviderByID(ctx, providerID)
	if err != nil {
		return nil, fmt.Errorf("get provider %d: %w", providerID, err)
	}

	def := rowToProviderDef(p)
	contract := def.Contract()
	op := selectProbeOperation(contract)
	if op == nil {
		return s.recordProviderProbe(ctx, p, "", "", ProbeStatusSkipped,
			"contract declares no read-only verify_operation to probe", 0)
	}

	method, path, query, body, ok := probeRequestFor(op)
	if !ok {
		return s.recordProviderProbe(ctx, p, contract.Digest, "", ProbeStatusSkipped,
			"verify_operation "+op.Name+" needs path parameters and cannot be probed automatically", 0)
	}

	owners, err := s.q.ListProbeCredentialOwnersForProvider(ctx, p.Name)
	if err != nil {
		return nil, fmt.Errorf("list credential owners for %s: %w", p.Name, err)
	}
	var userID int64
	found := false
	for _, o := range owners {
		if o.OwnerType == "user" {
			userID, found = o.OwnerID, true
			break
		}
	}
	if !found {
		return s.recordProviderProbe(ctx, p, contract.Digest, "", ProbeStatusSkipped,
			"no user credential configured for this provider; nothing to authenticate the probe with", 0)
	}

	start := time.Now()
	out, callErr := s.proxySvc.Call(ctx, ProxyInput{
		UserID:      userID,
		Provider:    p.Name,
		Method:      method,
		Path:        path,
		Query:       query,
		Body:        body,
		HealthProbe: true,
	})
	latency := time.Since(start).Milliseconds()

	status, msg := classifyProbeCall(out, callErr)
	if status == ProbeStatusSkipped {
		// Permission gate refused the probe (e.g. the whitelist does not cover
		// the verify operation). Not a health signal -- leave the streak alone.
		return s.recordProviderProbe(ctx, p, contract.Digest, "", ProbeStatusSkipped, msg, latency)
	}

	upstreamDigest, drift, driftMsg := s.checkContractDrift(ctx, p)

	outcome, err := s.recordProviderProbe(ctx, p, contract.Digest, upstreamDigest, status, msg, latency)
	if err != nil {
		return outcome, err
	}
	if drift {
		s.applyDrift(ctx, p, upstreamDigest, driftMsg)
		outcome.Drift = true
		if outcome.Status == ProbeStatusOK {
			outcome.Status = ProbeStatusDrift
		}
	}
	return outcome, nil
}

// checkContractDrift fetches the provider's upstream OpenAPI document (when
// openapi_url is set) and compares its digest with the stored baseline. The
// first successful fetch establishes the baseline (no drift); a later change
// is drift. A fetch failure is reported but never treated as a health failure
// -- the digest is a courtesy check, not a liveness probe.
func (s *IntegrationHealthService) checkContractDrift(ctx context.Context, p store.ExternalProvider) (digest string, drift bool, message string) {
	if strings.TrimSpace(p.OpenapiUrl) == "" {
		return "", false, ""
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.OpenapiUrl, nil)
	if err != nil {
		return "", false, ""
	}
	resp, err := s.client.Do(req)
	if err != nil {
		slog.Warn("integration-health: openapi fetch failed", "provider", p.Name, "error", err)
		return "", false, ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		slog.Warn("integration-health: openapi fetch non-2xx", "provider", p.Name, "status", resp.StatusCode)
		return "", false, ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return "", false, ""
	}
	sum := sha256.Sum256(body)
	d := "sha256:" + hex.EncodeToString(sum[:])

	if p.UpstreamSpecDigest == "" {
		// First observation: record the baseline, do not cry drift.
		s.setBaseline(ctx, p.ID, d)
		return d, false, ""
	}
	if d == p.UpstreamSpecDigest {
		return d, false, ""
	}
	return d, true, "上游已变更，建议重新审阅契约（upstream OpenAPI document changed since it was last reviewed）"
}

// setBaseline pins the upstream digest without raising drift (first
// observation, or after the user acknowledges).
func (s *IntegrationHealthService) setBaseline(ctx context.Context, id int64, digest string) {
	if err := s.q.UpdateProviderDriftState(ctx, store.UpdateProviderDriftStateParams{
		ContractDrift:      0,
		UpstreamSpecDigest: digest,
		ID:                 id,
	}); err != nil {
		slog.Warn("integration-health: set drift baseline failed", "provider_id", id, "error", err)
	}
}

// SeedProviderDriftBaseline fetches the provider's upstream OpenAPI document
// once and pins its digest as the drift baseline. The draft pipeline calls it at
// PUBLISH time, so the baseline is the document as it stood when the contract
// went live: an upstream change that happens between publication and the first
// scheduled sweep is then detected as drift, instead of being absorbed silently
// when the first sweep would otherwise have established the baseline itself.
//
// A no-op when the provider has no openapi_url, or when a baseline is already
// set (idempotent, so it is safe to call on every publish). A fetch failure is
// never fatal: the first sweep falls back to establishing the baseline.
func (s *IntegrationHealthService) SeedProviderDriftBaseline(ctx context.Context, providerID int64) {
	p, err := s.q.GetProviderByID(ctx, providerID)
	if err != nil {
		return
	}
	if p.UpstreamSpecDigest != "" {
		return
	}
	// With an empty baseline, checkContractDrift records the baseline from the
	// fetched document (or returns early on an empty url / fetch failure).
	s.checkContractDrift(ctx, p)
}

// applyDrift raises the drift flag + notifies once. A provider already flagged
// stays flagged (only the observed digest is refreshed) and is not re-notified
// on every sweep; the flag clears when the user acknowledges.
func (s *IntegrationHealthService) applyDrift(ctx context.Context, p store.ExternalProvider, digest, message string) {
	if p.ContractDrift != 0 {
		// Already flagged: keep the flag (do NOT call setBaseline, which would
		// clear it) and refresh only the observed digest.
		if err := s.q.UpdateProviderDriftState(ctx, store.UpdateProviderDriftStateParams{
			ContractDrift:      1,
			UpstreamSpecDigest: digest,
			ID:                 p.ID,
		}); err != nil {
			slog.Warn("integration-health: refresh drift digest failed", "provider_id", p.ID, "error", err)
		}
		return
	}
	if err := s.q.UpdateProviderDriftState(ctx, store.UpdateProviderDriftStateParams{
		ContractDrift:      1,
		UpstreamSpecDigest: digest,
		LastDriftAt:        sql.NullTime{Time: time.Now(), Valid: true},
		ID:                 p.ID,
	}); err != nil {
		slog.Warn("integration-health: set drift state failed", "provider_id", p.ID, "error", err)
		return
	}
	s.alert(ctx, IntegrationHealthAlert{
		SubjectKind: ProbeSubjectProvider,
		SubjectID:   p.ID,
		Name:        providerDisplayName(p),
		Kind:        AlertDrift,
		Message:     message,
	})
}

// classifyProbeCall maps a proxy call to a probe status. A permission-gate
// refusal is "skipped"; any transport error or non-2xx upstream response is a
// failure; a 2xx is ok.
func classifyProbeCall(out *ProxyOutput, err error) (status, message string) {
	if err != nil {
		var perr *ProxyError
		if errors.As(err, &perr) {
			return ProbeStatusSkipped, "probe blocked by permission gate: " + perr.Message
		}
		return ProbeStatusFailed, "probe request failed: " + err.Error()
	}
	if out == nil {
		return ProbeStatusFailed, "probe returned no response"
	}
	if out.Status >= 200 && out.Status < 300 {
		return ProbeStatusOK, fmt.Sprintf("verify_operation returned %d", out.Status)
	}
	return ProbeStatusFailed, fmt.Sprintf("upstream returned HTTP %d", out.Status)
}

// recordProviderProbe writes the run row, updates the source's health state,
// applies the auto-disable policy, and returns the outcome.
func (s *IntegrationHealthService) recordProviderProbe(ctx context.Context, p store.ExternalProvider, contractDigest, upstreamDigest, status, message string, latencyMs int64) (*ProbeOutcome, error) {
	health, streak, enabled, autoDisabledAt, autoDisabled, autoRecovered := s.applyPolicy(p.Enabled, p.ConsecutiveFailures, p.AutoDisabledAt, status)

	if err := s.q.UpdateProviderProbeResult(ctx, store.UpdateProviderProbeResultParams{
		HealthStatus:        health,
		ConsecutiveFailures: streak,
		LastProbeError:      probeErrorText(status, message),
		Enabled:             enabled,
		AutoDisabledAt:      autoDisabledAt,
		ID:                  p.ID,
	}); err != nil {
		return nil, fmt.Errorf("update provider %d health: %w", p.ID, err)
	}

	outcome := &ProbeOutcome{
		SubjectKind:         ProbeSubjectProvider,
		SubjectID:           p.ID,
		Status:              status,
		Message:             message,
		LatencyMs:           latencyMs,
		ConsecutiveFailures: streak,
		AutoDisabled:        autoDisabled,
		ContractDigest:      contractDigest,
		UpstreamDigest:      upstreamDigest,
	}
	s.recordRun(ctx, outcome, "user", 0, autoDisabled)

	if autoDisabled {
		s.alert(ctx, IntegrationHealthAlert{
			SubjectKind: ProbeSubjectProvider,
			SubjectID:   p.ID,
			Name:        providerDisplayName(p),
			Kind:        AlertAutoDisabled,
			Message:     fmt.Sprintf("已连续 %d 次探针失败，源已自动停用：%s", streak, message),
		})
	} else if autoRecovered {
		s.alert(ctx, IntegrationHealthAlert{
			SubjectKind: ProbeSubjectProvider,
			SubjectID:   p.ID,
			Name:        providerDisplayName(p),
			Kind:        AlertRecovered,
			Message:     "探针已恢复，源已自动重新启用",
		})
	}
	return outcome, nil
}

// ProbeDataSource pings one data source (the #732 "验证连接" path), records the
// verdict, and applies the failure policy.
func (s *IntegrationHealthService) ProbeDataSource(ctx context.Context, id int64) (*ProbeOutcome, error) {
	row, err := s.q.GetDataSource(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get data source %d: %w", id, err)
	}

	start := time.Now()
	verifyErr := s.dataSourceSvc.VerifyRow(ctx, row)
	latency := time.Since(start).Milliseconds()

	status, message := ProbeStatusOK, "connection verified"
	if verifyErr != nil {
		status, message = ProbeStatusFailed, "connection verify failed: "+verifyErr.Error()
	}

	health, streak, enabled, autoDisabledAt, autoDisabled, autoRecovered := s.applyPolicy(row.Enabled, row.ConsecutiveFailures, row.AutoDisabledAt, status)

	if err := s.q.UpdateDataSourceProbeResult(ctx, store.UpdateDataSourceProbeResultParams{
		HealthStatus:        health,
		ConsecutiveFailures: streak,
		LastProbeError:      probeErrorText(status, message),
		Enabled:             enabled,
		AutoDisabledAt:      autoDisabledAt,
		ID:                  row.ID,
	}); err != nil {
		return nil, fmt.Errorf("update data source %d health: %w", row.ID, err)
	}

	outcome := &ProbeOutcome{
		SubjectKind:         ProbeSubjectDataSource,
		SubjectID:           row.ID,
		Status:              status,
		Message:             message,
		LatencyMs:           latency,
		ConsecutiveFailures: streak,
		AutoDisabled:        autoDisabled,
	}
	s.recordRun(ctx, outcome, row.OwnerType, row.OwnerID, autoDisabled)

	if autoDisabled {
		s.alert(ctx, IntegrationHealthAlert{
			SubjectKind: ProbeSubjectDataSource,
			SubjectID:   row.ID,
			OwnerType:   row.OwnerType,
			OwnerID:     row.OwnerID,
			Name:        row.Name,
			Kind:        AlertAutoDisabled,
			Message:     fmt.Sprintf("已连续 %d 次探针失败，数据源已自动停用：%s", streak, message),
		})
	} else if autoRecovered {
		s.alert(ctx, IntegrationHealthAlert{
			SubjectKind: ProbeSubjectDataSource,
			SubjectID:   row.ID,
			OwnerType:   row.OwnerType,
			OwnerID:     row.OwnerID,
			Name:        row.Name,
			Kind:        AlertRecovered,
			Message:     "探针已恢复，数据源已自动重新启用",
		})
	}
	return outcome, nil
}

// applyPolicy computes the next health state from the current one and the probe
// status. Only a "failed" probe advances the streak; "ok" resets it; "skipped"
// and "drift" leave both streak and enabled untouched. Reaching the threshold
// disables the source (if not already) and stamps auto_disabled_at. A source
// that was AUTO-disabled (auto_disabled_at set) and probes clean again is
// switched back on automatically; a source the user turned off stays off.
func (s *IntegrationHealthService) applyPolicy(curEnabled, curStreak int64, curAutoDisabledAt sql.NullTime, status string) (health string, streak, enabled int64, autoDisabledAt sql.NullTime, autoDisabled, autoRecovered bool) {
	enabled = curEnabled
	streak = curStreak
	autoDisabledAt = curAutoDisabledAt
	wasAutoDisabled := curAutoDisabledAt.Valid && curEnabled == 0

	switch status {
	case ProbeStatusOK:
		streak = 0
		switch {
		case wasAutoDisabled:
			enabled = 1
			autoDisabledAt = sql.NullTime{}
			autoRecovered = true
			health = HealthHealthy
		case curEnabled == 1:
			health = HealthHealthy
		default:
			health = HealthDisabled
		}
	case ProbeStatusFailed:
		streak = curStreak + 1
		health = HealthUnhealthy
		if streak >= s.cfg.FailureThreshold && curEnabled == 1 {
			enabled = 0
			autoDisabled = true
			autoDisabledAt = sql.NullTime{Time: time.Now(), Valid: true}
			health = HealthDisabled
		}
	case ProbeStatusSkipped:
		if curEnabled == 1 {
			health = HealthUnknown
		} else {
			health = HealthDisabled
		}
	default: // drift
		if curEnabled == 1 {
			health = HealthHealthy
		} else {
			health = HealthDisabled
		}
	}
	return health, streak, enabled, autoDisabledAt, autoDisabled, autoRecovered
}

// recordRun appends the probe to integration_probe_runs (the audit trail).
func (s *IntegrationHealthService) recordRun(ctx context.Context, o *ProbeOutcome, ownerType string, ownerID int64, autoDisabled bool) {
	if ownerType == "" {
		ownerType = "user"
	}
	var drift, disabled int64
	if o.Drift {
		drift = 1
	}
	if autoDisabled {
		disabled = 1
	}
	if err := s.q.CreateIntegrationProbeRun(ctx, store.CreateIntegrationProbeRunParams{
		SubjectKind:         o.SubjectKind,
		SubjectID:           o.SubjectID,
		OwnerType:           ownerType,
		OwnerID:             ownerID,
		Status:              o.Status,
		Message:             o.Message,
		LatencyMs:           o.LatencyMs,
		ContractDigest:      o.ContractDigest,
		UpstreamDigest:      o.UpstreamDigest,
		Drift:               drift,
		ConsecutiveFailures: o.ConsecutiveFailures,
		AutoDisabled:        disabled,
	}); err != nil {
		slog.Warn("integration-health: record probe run failed", "subject", o.SubjectKind, "id", o.SubjectID, "error", err)
	}
}

func (s *IntegrationHealthService) alert(ctx context.Context, a IntegrationHealthAlert) {
	// Team edition (#740 seam): a health anomaly on an ORG-owned source is
	// recorded in the org audit log, so an admin sees auto-disables / drift in
	// the organization's trail, not just in the owner's notifications. Personal
	// ('user') sources have no audit trail, so this is a no-op for them.
	s.appendOrgAudit(ctx, a)
	if s.notifier == nil {
		return
	}
	s.notifier.NotifyIntegrationHealth(ctx, a)
}

// appendOrgAudit writes the alert to org_audit_log when the subject is
// org-owned. Actor is the system (0); best-effort like every audit write.
func (s *IntegrationHealthService) appendOrgAudit(ctx context.Context, a IntegrationHealthAlert) {
	if a.OwnerType != "org" {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"subject_kind": a.SubjectKind,
		"name":         a.Name,
		"kind":         a.Kind,
		"message":      a.Message,
	})
	if err != nil {
		payload = []byte("{}")
	}
	appendResourceAudit(ctx, s.db, nil, a.OwnerType, a.OwnerID, 0,
		"integration_health."+a.Kind, a.SubjectKind, a.SubjectID, string(payload))
}

// Reenable clears an auto-disable (or manual off) and the failure streak so
// the next probe starts clean. Used by the recovery endpoint.
func (s *IntegrationHealthService) Reenable(ctx context.Context, subjectKind string, id int64) error {
	switch subjectKind {
	case ProbeSubjectProvider:
		return s.q.ReenableProvider(ctx, id)
	case ProbeSubjectDataSource:
		return s.q.ReenableDataSource(ctx, id)
	default:
		return fmt.Errorf("unknown subject kind %q", subjectKind)
	}
}

// SetEnabled turns a source on/off manually.
func (s *IntegrationHealthService) SetEnabled(ctx context.Context, subjectKind string, id int64, enabled bool) error {
	v := int64(0)
	if enabled {
		v = 1
	}
	switch subjectKind {
	case ProbeSubjectProvider:
		return s.q.SetExternalProviderEnabled(ctx, store.SetExternalProviderEnabledParams{Enabled: v, ID: id})
	case ProbeSubjectDataSource:
		return s.q.SetDataSourceEnabled(ctx, store.SetDataSourceEnabledParams{Enabled: v, ID: id})
	default:
		return fmt.Errorf("unknown subject kind %q", subjectKind)
	}
}

// AcknowledgeDrift re-baselines a provider's upstream digest to whatever was
// last observed, clearing the drift flag. This is the "建议重新审阅契约" action:
// the user has looked, so stop flagging until the upstream changes again.
func (s *IntegrationHealthService) AcknowledgeDrift(ctx context.Context, providerID int64) error {
	p, err := s.q.GetProviderByID(ctx, providerID)
	if err != nil {
		return err
	}
	// Refetch so acknowledging immediately after a probe pins the newest digest.
	digest, _, _ := s.checkContractDrift(ctx, p)
	if digest == "" {
		digest = p.UpstreamSpecDigest
	}
	return s.q.UpdateProviderDriftState(ctx, store.UpdateProviderDriftStateParams{
		ContractDrift:      0,
		UpstreamSpecDigest: digest,
		ID:                 providerID,
	})
}

// ListProbeRuns returns recent probe history (subjectID 0 = any of that kind),
// scoped to the caller's accessible owner set. Data-source runs can quote a raw
// driver error, so they are only returned for the caller's own owner or an org
// they belong to (membership derived in SQL from org_members, so the caller
// cannot widen the set); provider runs are global (providers have no owner) and
// stay visible to any authenticated caller, like the provider list.
func (s *IntegrationHealthService) ListProbeRuns(ctx context.Context, subjectKind string, subjectID, userID, limit int64) ([]store.IntegrationProbeRun, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return s.q.ListIntegrationProbeRuns(ctx, store.ListIntegrationProbeRunsParams{
		SubjectKind: subjectKind,
		SubjectID:   subjectID,
		UserID:      userID,
		Lim:         limit,
	})
}

// --- probe request construction ------------------------------------------

// selectProbeOperation picks the operation to probe: the contract's declared
// verify_operation when present, else the first read-only operation as a
// best-effort fallback. Returns nil when nothing safe can be probed.
func selectProbeOperation(c *ProviderContract) *ContractOperation {
	if c == nil || len(c.Operations) == 0 {
		return nil
	}
	if c.VerifyOperation != "" {
		for i := range c.Operations {
			if c.Operations[i].Name == c.VerifyOperation {
				return &c.Operations[i]
			}
		}
	}
	for i := range c.Operations {
		if c.Operations[i].ReadOnly {
			return &c.Operations[i]
		}
	}
	return nil
}

// probeRequestFor turns a contract operation into a concrete call. Path
// placeholders are filled from request_example; leftover request_example keys
// become query params (GET/HEAD) or a JSON body (POST/PUT/PATCH/DELETE). ok is
// false when the path still has an unresolved placeholder.
func probeRequestFor(op *ContractOperation) (method, path string, query, body map[string]any, ok bool) {
	method, path = op.Method, op.Path
	example := map[string]any{}
	if len(op.RequestExample) > 0 {
		_ = json.Unmarshal(op.RequestExample, &example)
	}

	for k, v := range example {
		if strings.Contains(path, "{"+k+"}") {
			path = strings.ReplaceAll(path, "{"+k+"}", fmt.Sprintf("%v", v))
			delete(example, k)
		}
	}
	if strings.Contains(path, "{") {
		return "", "", nil, nil, false
	}

	switch method {
	case "GET", "HEAD", "DELETE":
		if len(example) > 0 {
			query = example
		}
	case "POST", "PUT", "PATCH":
		if len(example) > 0 {
			body = example
		}
	}
	return method, path, query, body, true
}

// --- helpers --------------------------------------------------------------

func probeErrorText(status, message string) string {
	if status == ProbeStatusFailed {
		return message
	}
	return ""
}

func providerDisplayName(p store.ExternalProvider) string {
	if p.Label != "" {
		return p.Label
	}
	return p.Name
}
