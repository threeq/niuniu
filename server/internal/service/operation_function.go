package service

// Sandbox operation functions (Epic #726 / #739): the runtime half.
//
// An operation function is the escape hatch for integrations the declarative
// path cannot express — a two-step token exchange, a request that must be
// signed. The AI writes it, the user approves it through the #734 draft
// pipeline, and every call runs it in a throwaway OS-fenced process.
//
// What this service owns, in order:
//
//   - the registry (which functions exist for which provider, and their state)
//   - invocation: rate accounting, idempotency, the egress bridge, the audit row
//   - the first-run observation window
//   - behaviour rehearsal, which produces the "will send" manifest a human
//     approves BEFORE anything is published
//
// Egress is the load-bearing detail. The box never opens a socket: it emits an
// http_request frame naming an OPERATION, and this service forwards that to
// ExternalProxyService.Call. So the provider's write gate, the contract's
// operation authorization, credential injection, the SSRF guard and the shared
// audit log all apply exactly as they do for a direct call_external_api. The
// sandbox is a new way to COMPOSE calls, never a way around them.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/niuniu-dev/niuniu/internal/sandbox"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// Call-status values recorded in function_call_audit.status.
const (
	FuncStatusOK            = "ok"
	FuncStatusError         = "error"
	FuncStatusDenied        = "denied"
	FuncStatusRateLimited   = "rate_limited"
	FuncStatusReplayRefused = "replay_refused"
)

// Caller values: where a call came from. The observation window treats an
// agent call as the interesting one, but rehearsal is recorded too so the audit
// is a complete account of what ran.
const (
	CallerAgent     = "agent"
	CallerUser      = "user"
	CallerRehearsal = "rehearsal"
)

// Function status values (integration_functions.status).
const (
	FuncObserving = "observing"
	FuncActive    = "active"
	FuncDisabled  = "disabled"
)

// Rate policy. Accounting is PER FUNCTION ("出口限速按函数记账"): one function
// hammering an upstream cannot spend another function's budget, and a runaway
// function cannot turn the sandbox into a jump box for the provider.
const (
	functionCallsPerMinute  = 30
	functionEgressPerMinute = 120
)

// observationDefaultCalls is how many first-run calls are watched in full.
const observationDefaultCalls = 20

// Sentinel errors, so the API layer can map them to status codes.
var (
	ErrFunctionNotFound = errors.New("operation function not found")
	ErrFunctionDisabled = errors.New("operation function is disabled")
	ErrFenceUnavailable = errors.New("the OS sandbox for this platform is unavailable")
)

// FunctionAlertNotifier receives observation-window anomalies. Optional: a nil
// notifier simply means nothing is pushed, the audit row is still written.
type FunctionAlertNotifier interface {
	NotifyFunctionAnomaly(ctx context.Context, alert FunctionAnomaly)
}

// FunctionAnomaly describes an abnormal call during the observation window.
type FunctionAnomaly struct {
	OwnerType    string
	OwnerID      int64
	UserID       int64
	FunctionID   int64
	FunctionName string
	ProviderName string
	Status       string
	ErrorCode    string
	Detail       string
}

// OperationFunctionService owns the registry and the runtime.
type OperationFunctionService struct {
	q         *store.Queries
	db        *store.DB
	providers *ExternalProviderService
	proxy     *ExternalProxyService
	limiter   *functionRateLimiter
	notifier  FunctionAlertNotifier
}

// NewOperationFunctionService wires the runtime. The helper binary path is
// published to the sandbox package here, because every probe and every run
// needs it.
func NewOperationFunctionService(q *store.Queries, rawDB *sql.DB, providers *ExternalProviderService, proxy *ExternalProxyService) *OperationFunctionService {
	svc := &OperationFunctionService{
		q:         q,
		db:        store.Wrap(rawDB),
		providers: providers,
		proxy:     proxy,
		limiter:   newFunctionRateLimiter(),
	}
	if path := findFuncboxBinary(); path != "" {
		sandbox.SetHelperPath(path)
	}
	return svc
}

// SetAlertNotifier wires an anomaly sink for the observation window.
func (s *OperationFunctionService) SetAlertNotifier(n FunctionAlertNotifier) { s.notifier = n }

// --- views ----------------------------------------------------------------

// FunctionView is one function as the UI and the agent see it.
type FunctionView struct {
	ID               int64              `json:"id"`
	ProviderID       int64              `json:"provider_id"`
	ProviderName     string             `json:"provider_name"`
	Name             string             `json:"name"`
	ToolName         string             `json:"tool_name"`
	Description      string             `json:"description"`
	Runtime          string             `json:"runtime"`
	Code             string             `json:"code"`
	CodeHash         string             `json:"code_hash"`
	Entry            string             `json:"entry"`
	Limits           sandbox.Limits     `json:"limits"`
	Egress           []sandbox.EgressOp `json:"egress"`
	Idempotent       bool               `json:"idempotent"`
	Enabled          bool               `json:"enabled"`
	Version          int64              `json:"version"`
	Status           string             `json:"status"`
	ObservationCalls int64              `json:"observation_calls"`
	ObservationLimit int64              `json:"observation_limit"`
	Observing        bool               `json:"observing"`
	CreatedAt        time.Time          `json:"created_at"`
	UpdatedAt        time.Time          `json:"updated_at"`
}

func functionViewFromRow(row store.IntegrationFunction) FunctionView {
	v := FunctionView{
		ID:               row.ID,
		ProviderID:       row.ProviderID,
		ProviderName:     row.ProviderName,
		Name:             row.Name,
		ToolName:         sandbox.ToolName(row.ProviderName, row.Name),
		Description:      row.Description,
		Runtime:          row.Runtime,
		Code:             row.Code,
		CodeHash:         row.CodeHash,
		Entry:            row.Entry,
		Idempotent:       row.Idempotent != 0,
		Enabled:          row.Enabled != 0,
		Version:          row.Version,
		Status:           row.Status,
		ObservationCalls: row.ObservationCalls,
		ObservationLimit: row.ObservationLimit,
		Observing:        row.Status == FuncObserving,
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
	}
	_ = json.Unmarshal([]byte(row.LimitsJson), &v.Limits)
	if err := json.Unmarshal([]byte(row.EgressJson), &v.Egress); err != nil {
		v.Egress = nil
	}
	return v
}

// List returns the functions owned by an owner, newest source first.
func (s *OperationFunctionService) List(ctx context.Context, ownerType string, ownerID int64) ([]FunctionView, error) {
	rows, err := s.q.ListIntegrationFunctionsByOwner(ctx, store.ListIntegrationFunctionsByOwnerParams{
		OwnerType: ownerType, OwnerID: ownerID,
	})
	if err != nil {
		return nil, err
	}
	out := make([]FunctionView, 0, len(rows))
	for _, r := range rows {
		out = append(out, functionViewFromRow(r))
	}
	return out, nil
}

// ListForProvider returns the functions of one provider.
func (s *OperationFunctionService) ListForProvider(ctx context.Context, providerID int64) ([]FunctionView, error) {
	rows, err := s.q.ListIntegrationFunctionsByProvider(ctx, providerID)
	if err != nil {
		return nil, err
	}
	out := make([]FunctionView, 0, len(rows))
	for _, r := range rows {
		out = append(out, functionViewFromRow(r))
	}
	return out, nil
}

// Get loads one function by id.
func (s *OperationFunctionService) Get(ctx context.Context, id int64) (*FunctionView, error) {
	row, err := s.q.GetIntegrationFunction(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrFunctionNotFound
	}
	if err != nil {
		return nil, err
	}
	v := functionViewFromRow(row)
	return &v, nil
}

// GetByName resolves a function by provider + name.
func (s *OperationFunctionService) GetByName(ctx context.Context, providerName, name string) (*FunctionView, error) {
	prov, err := s.providers.GetByName(ctx, providerName)
	if err != nil {
		return nil, fmt.Errorf("provider %q not found: %w", providerName, err)
	}
	row, err := s.q.GetIntegrationFunctionByName(ctx, store.GetIntegrationFunctionByNameParams{
		ProviderID: prov.ID, Name: name,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrFunctionNotFound
	}
	if err != nil {
		return nil, err
	}
	v := functionViewFromRow(row)
	return &v, nil
}

// SetEnabled flips one function's switch without touching its provider.
func (s *OperationFunctionService) SetEnabled(ctx context.Context, ownerType string, ownerID, id int64, enabled bool) error {
	row, err := s.ownedFunction(ctx, ownerType, ownerID, id)
	if err != nil {
		return err
	}
	status := FuncDisabled
	if enabled {
		status = row.Status
		if status == FuncDisabled {
			// Re-enabling a function that had finished observing goes back to
			// active; one that was still observing resumes observing.
			status = FuncObserving
			if row.ObservationCalls >= row.ObservationLimit {
				status = FuncActive
			}
		}
	}
	on := int64(0)
	if enabled {
		on = 1
	}
	return s.q.UpdateIntegrationFunctionEnabled(ctx, store.UpdateIntegrationFunctionEnabledParams{
		Enabled: on, Status: status, ID: id,
	})
}

// Delete removes one function and its version chain.
func (s *OperationFunctionService) Delete(ctx context.Context, ownerType string, ownerID, id int64) error {
	if _, err := s.ownedFunction(ctx, ownerType, ownerID, id); err != nil {
		return err
	}
	return s.q.DeleteIntegrationFunction(ctx, id)
}

// DeleteForProvider removes every function of a provider — the "函数随源删除"
// half of the delivery requirement. Deleting a source must not leave callable
// functions pointing at operations that no longer exist.
func (s *OperationFunctionService) DeleteForProvider(ctx context.Context, providerID int64) error {
	return s.q.DeleteIntegrationFunctionsByProvider(ctx, providerID)
}

func (s *OperationFunctionService) ownedFunction(ctx context.Context, ownerType string, ownerID, id int64) (store.IntegrationFunction, error) {
	row, err := s.q.GetIntegrationFunction(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return row, ErrFunctionNotFound
	}
	if err != nil {
		return row, err
	}
	if row.OwnerType != ownerType || row.OwnerID != ownerID {
		return row, ErrFunctionNotFound
	}
	return row, nil
}

// --- versions / rollback --------------------------------------------------

// FunctionVersionView is one entry of a function's rollback chain.
type FunctionVersionView struct {
	ID          int64              `json:"id"`
	Version     int64              `json:"version"`
	CodeHash    string             `json:"code_hash"`
	Egress      []sandbox.EgressOp `json:"egress"`
	Idempotent  bool               `json:"idempotent"`
	Active      bool               `json:"active"`
	PublishedBy int64              `json:"published_by"`
	CreatedAt   time.Time          `json:"created_at"`
}

// ListVersions returns a function's published versions, newest first.
func (s *OperationFunctionService) ListVersions(ctx context.Context, functionID int64, limit int64) ([]FunctionVersionView, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.q.ListIntegrationFunctionVersions(ctx, store.ListIntegrationFunctionVersionsParams{
		FunctionID: functionID, LimitCount: limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]FunctionVersionView, 0, len(rows))
	for _, r := range rows {
		v := FunctionVersionView{
			ID: r.ID, Version: r.Version, CodeHash: r.CodeHash,
			Idempotent: r.Idempotent != 0, Active: r.Active != 0,
			PublishedBy: r.PublishedBy, CreatedAt: r.CreatedAt,
		}
		_ = json.Unmarshal([]byte(r.EgressJson), &v.Egress)
		out = append(out, v)
	}
	return out, nil
}

// FunctionRollbackInput is a rollback request.
type FunctionRollbackInput struct {
	FunctionID int64
	VersionID  int64
	OwnerType  string
	OwnerID    int64
	ByUser     int64
	FromAgent  bool
	// Confirmed acknowledges the re-confirmation a scope-changing rollback
	// demands.
	Confirmed bool
	AckKeys   []string
}

// FunctionRollbackResult reports what a rollback did (or what it needs first).
type FunctionRollbackResult struct {
	RolledBack bool  `json:"rolled_back"`
	FunctionID int64 `json:"function_id"`
	Version    int64 `json:"version,omitempty"`

	RequiresConfirmation bool                `json:"requires_confirmation,omitempty"`
	ChangedScopes        []FunctionScopeDiff `json:"changed_scopes,omitempty"`
	Detail               string              `json:"detail,omitempty"`
}

// FunctionScopeDiff names one authority change a rollback would make.
type FunctionScopeDiff struct {
	Key    string `json:"key"`
	Before string `json:"before"`
	After  string `json:"after"`
}

// Rollback restores an earlier published body. Restoring a body can RE-GRANT
// authority the user has since revoked — an older version might reach an
// operation the current one does not — so a rollback whose egress set or
// idempotency differs from the live function is an authorization action and
// must be confirmed, exactly like the provider-line rollback in #734.
func (s *OperationFunctionService) Rollback(ctx context.Context, in FunctionRollbackInput) (*FunctionRollbackResult, error) {
	if in.FromAgent {
		return nil, ErrAgentCannotAuthorize
	}
	row, err := s.ownedFunction(ctx, in.OwnerType, in.OwnerID, in.FunctionID)
	if err != nil {
		return nil, err
	}
	ver, err := s.q.GetIntegrationFunctionVersion(ctx, in.VersionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrFunctionNotFound
	}
	if err != nil {
		return nil, err
	}
	if ver.FunctionID != row.ID {
		return nil, ErrFunctionNotFound
	}

	live := functionScopeKeys(row.EgressJson, row.Idempotent != 0)
	target := functionScopeKeys(ver.EgressJson, ver.Idempotent != 0)
	diffs := diffScopeKeys(live, target)
	if len(diffs) > 0 && !in.Confirmed {
		return &FunctionRollbackResult{
			RequiresConfirmation: true,
			FunctionID:           row.ID,
			Version:              ver.Version,
			ChangedScopes:        diffs,
			Detail: "restoring this version changes what the function is allowed to reach; " +
				"confirm to re-grant it",
		}, nil
	}

	var limits sandbox.Limits
	_ = json.Unmarshal([]byte(ver.LimitsJson), &limits)
	if err := s.q.ReplaceIntegrationFunctionBody(ctx, store.ReplaceIntegrationFunctionBodyParams{
		Description:      row.Description,
		Runtime:          row.Runtime,
		Code:             ver.Code,
		CodeHash:         ver.CodeHash,
		Entry:            row.Entry,
		LimitsJson:       ver.LimitsJson,
		EgressJson:       ver.EgressJson,
		Idempotent:       ver.Idempotent,
		Version:          row.Version + 1,
		Status:           FuncObserving,
		ObservationCalls: 0,
		ObservationLimit: observationDefaultCalls,
		Enabled:          1,
		ID:               row.ID,
	}); err != nil {
		return nil, err
	}
	if err := s.q.DeactivateIntegrationFunctionVersions(ctx, row.ID); err != nil {
		return nil, err
	}
	if err := s.q.ActivateIntegrationFunctionVersion(ctx, ver.ID); err != nil {
		return nil, err
	}
	return &FunctionRollbackResult{
		RolledBack: true,
		FunctionID: row.ID,
		Version:    ver.Version,
		Detail: fmt.Sprintf("restored version %d; the function is back in its first-run observation window",
			ver.Version),
	}, nil
}

// --- invocation -----------------------------------------------------------

// FunctionInvokeInput is one call.
type FunctionInvokeInput struct {
	OwnerType    string
	OwnerID      int64
	UserID       int64
	FunctionID   int64
	FunctionName string
	ProviderName string
	Input        map[string]any
	WorkspaceID  int64
	SourceKey    string
	Caller       string
	// IdempotencyKey identifies a logical call across retries. Required for a
	// non-idempotent function to be safely retried at all: without a key the
	// platform cannot tell a retry from a new call, so it refuses nothing and
	// promises nothing.
	IdempotencyKey string
}

// FunctionInvokeResult is the outcome handed back to the agent / the UI.
type FunctionInvokeResult struct {
	OK           bool     `json:"ok"`
	Output       any      `json:"output,omitempty"`
	Error        string   `json:"error,omitempty"`
	ErrorCode    string   `json:"error_code,omitempty"`
	Logs         []string `json:"logs,omitempty"`
	DurationMS   int64    `json:"duration_ms"`
	EgressCalls  int      `json:"egress_calls"`
	EgressOps    []string `json:"egress_ops,omitempty"`
	Replayed     bool     `json:"replayed,omitempty"`
	Observation  bool     `json:"observation,omitempty"`
	FenceBackend string   `json:"fence_backend,omitempty"`
	AuditID      int64    `json:"audit_id,omitempty"`
	RetryAfterMS int64    `json:"retry_after_ms,omitempty"`
	Detail       string   `json:"detail,omitempty"`
}

// Invoke resolves the function, applies the gate sequence, runs the box and
// records the call. Every path through it writes an audit row — including the
// refusals, which are the ones an operator most wants to see.
func (s *OperationFunctionService) Invoke(ctx context.Context, in FunctionInvokeInput) (*FunctionInvokeResult, error) {
	row, err := s.resolveForInvoke(ctx, in)
	if err != nil {
		return nil, err
	}
	caller := in.Caller
	if caller == "" {
		caller = CallerAgent
	}
	spec, err := specFromRow(row)
	if err != nil {
		return nil, fmt.Errorf("operation function %d is not runnable: %w", row.ID, err)
	}

	// 1. The fence gate — before anything else. A host whose OS sandbox is
	//    unavailable refuses to run code; that refusal is recorded, not hidden.
	if avail := sandbox.SelfCheck(false); !avail.Available {
		reason := avail.Reason
		if reason == "" {
			reason = "the OS sandbox for this platform is unavailable"
		}
		s.audit(ctx, row, in, caller, FuncStatusDenied, sandbox.ErrFenceUnavailable, reason, nil, nil, false, false, spec)
		return nil, fmt.Errorf("%w: %s", ErrFenceUnavailable, reason)
	}

	// 2. Per-function rate accounting (the "no jump box" guard).
	if wait, ok := s.limiter.reserve(row.ID); !ok {
		detail := fmt.Sprintf("this function is limited to %d calls/minute (upstream protection)", functionCallsPerMinute)
		s.audit(ctx, row, in, caller, FuncStatusRateLimited, "rate_limited", detail, nil, nil, false, false, spec)
		return &FunctionInvokeResult{
			OK: false, Error: detail, ErrorCode: "rate_limited",
			RetryAfterMS: wait.Milliseconds(),
		}, nil
	}

	// 3. Idempotency. A repeated key on a non-idempotent function is REFUSED:
	//    the platform never auto-replays something that could charge twice.
	if key := strings.TrimSpace(in.IdempotencyKey); key != "" {
		prior, err := s.q.GetFunctionIdempotency(ctx, store.GetFunctionIdempotencyParams{
			FunctionID: row.ID, IdemKey: key,
		})
		switch {
		case err == nil && prior.Status == FuncStatusOK && spec.Idempotent:
			var out any
			_ = json.Unmarshal([]byte(prior.OutputJson), &out)
			s.audit(ctx, row, in, caller, FuncStatusOK, "", "replayed from the idempotency ledger", nil, nil, true, false, spec)
			return &FunctionInvokeResult{
				OK: true, Output: out, Replayed: true, AuditID: prior.CallAuditID,
				Detail: "this call was already completed; the recorded result is returned because the function is declared idempotent",
			}, nil
		case err == nil:
			detail := "a call with this idempotency key already ran, and the function is not declared idempotent — it will not be replayed"
			s.audit(ctx, row, in, caller, FuncStatusReplayRefused, "replay_refused", detail, nil, nil, false, false, spec)
			return &FunctionInvokeResult{
				OK: false, Error: detail, ErrorCode: "replay_refused", Detail: detail,
			}, nil
		case !errors.Is(err, sql.ErrNoRows):
			return nil, err
		}
	}

	// 4. Run.
	observing := row.Status == FuncObserving
	ops := &egressTrail{}
	res := sandbox.Run(ctx, sandbox.RunRequest{
		Spec:  spec,
		Input: in.Input,
		Egress: func(ectx context.Context, req *sandbox.HTTPRequestMessage) *sandbox.HTTPResponseMessage {
			return s.serveEgress(ectx, row, req, ops, in)
		},
	})

	status := FuncStatusOK
	if !res.OK {
		status = FuncStatusError
		if res.Error != nil {
			switch res.Error.Code {
			case sandbox.ErrEgressLimit, sandbox.ErrEgressDenied:
				status = FuncStatusDenied
			}
		}
	}
	code, msg := "", ""
	if res.Error != nil {
		code, msg = res.Error.Code, res.Error.Message
	}
	auditID := s.audit(ctx, row, in, caller, status, code, msg, ops.snapshot(), res, false, observing, spec)

	// 5. Observation bookkeeping + anomaly alert.
	if observing && caller != CallerRehearsal {
		s.advanceObservation(ctx, row, status)
	}

	// 6. Record the idempotency outcome so a retry can be answered.
	if key := strings.TrimSpace(in.IdempotencyKey); key != "" && res.OK {
		raw, _ := json.Marshal(res.Output)
		if _, err := s.q.CreateFunctionIdempotency(ctx, store.CreateFunctionIdempotencyParams{
			FunctionID: row.ID, IdemKey: key, CallAuditID: auditID,
			Status: FuncStatusOK, OutputJson: string(raw),
		}); err != nil {
			// A ledger write failure must not fail a call that already
			// happened; the audit row still records it.
			_ = err
		}
	}

	out := &FunctionInvokeResult{
		OK:           res.OK,
		Output:       res.Output,
		Error:        msg,
		ErrorCode:    code,
		Logs:         res.Logs,
		DurationMS:   res.DurationMS,
		EgressCalls:  res.EgressCalls,
		EgressOps:    ops.snapshot(),
		Observation:  observing,
		FenceBackend: res.FenceBackend,
		AuditID:      auditID,
	}
	if observing {
		out.Detail = fmt.Sprintf("this function is in its first-run observation window (%d/%d calls); every request it makes is being recorded",
			row.ObservationCalls+1, row.ObservationLimit)
	}
	return out, nil
}

// resolveForInvoke finds the function by id or by provider+name and checks it
// is callable.
func (s *OperationFunctionService) resolveForInvoke(ctx context.Context, in FunctionInvokeInput) (store.IntegrationFunction, error) {
	var row store.IntegrationFunction
	var err error
	if in.FunctionID > 0 {
		row, err = s.ownedFunction(ctx, in.OwnerType, in.OwnerID, in.FunctionID)
	} else if in.ProviderName != "" && in.FunctionName != "" {
		prov, perr := s.providers.GetByName(ctx, in.ProviderName)
		if perr != nil {
			return row, fmt.Errorf("provider %q not found: %w", in.ProviderName, perr)
		}
		row, err = s.q.GetIntegrationFunctionByName(ctx, store.GetIntegrationFunctionByNameParams{
			ProviderID: prov.ID, Name: in.FunctionName,
		})
		if errors.Is(err, sql.ErrNoRows) {
			return row, ErrFunctionNotFound
		}
		if err == nil && (row.OwnerType != in.OwnerType || row.OwnerID != in.OwnerID) {
			return row, ErrFunctionNotFound
		}
	} else {
		return row, fmt.Errorf("%w: provide function_id or provider_name + function_name", ErrFunctionNotFound)
	}
	if err != nil {
		return row, err
	}
	if row.Enabled == 0 || row.Status == FuncDisabled {
		return row, fmt.Errorf("%w: %s", ErrFunctionDisabled, row.Name)
	}
	return row, nil
}

// serveEgress is the bridge between the box and the ordinary proxy egress.
//
// Every boundary the proxy enforces still applies: operation authorization
// against the contract, the provider write gate, credential resolution and
// injection, the SSRF guard, egress-data policy and the shared audit log. The
// only thing the box contributes is WHICH operation and with what arguments.
func (s *OperationFunctionService) serveEgress(ctx context.Context, row store.IntegrationFunction, req *sandbox.HTTPRequestMessage, trail *egressTrail, in FunctionInvokeInput) *sandbox.HTTPResponseMessage {
	trail.add(req.Operation)
	if !s.limiter.allowEgress(row.ID) {
		return &sandbox.HTTPResponseMessage{OK: false,
			Error: fmt.Sprintf("this function has reached its outbound limit of %d calls per minute", functionEgressPerMinute)}
	}
	out, err := s.proxy.Call(ctx, ProxyInput{
		UserID:      in.UserID,
		Provider:    row.ProviderName,
		Operation:   req.Operation,
		Query:       req.Query,
		Body:        req.Body,
		WorkspaceID: in.WorkspaceID,
		SourceKey:   in.SourceKey,
	})
	if err != nil {
		// A refusal (unknown operation, write gate, SSRF, transport) is
		// reported in-band so the function can branch instead of dying.
		return &sandbox.HTTPResponseMessage{OK: false, Error: err.Error()}
	}
	// Any real HTTP response counts as a completed call, including 4xx/5xx: the
	// function reads `status` and decides. Only a failure to complete the call
	// at all sets ok=false.
	return &sandbox.HTTPResponseMessage{
		OK:      true,
		Status:  out.Status,
		Headers: out.Headers,
		Body:    out.Body,
	}
}

// advanceObservation moves a function through its first-run window.
func (s *OperationFunctionService) advanceObservation(ctx context.Context, row store.IntegrationFunction, status string) {
	next := row.ObservationCalls + 1
	newStatus := FuncObserving
	if next >= row.ObservationLimit {
		newStatus = FuncActive
	}
	if err := s.q.RecordFunctionObservationCall(ctx, store.RecordFunctionObservationCallParams{
		Status: newStatus, ID: row.ID,
	}); err != nil {
		return
	}
	if status != FuncStatusOK && s.notifier != nil {
		s.notifier.NotifyFunctionAnomaly(ctx, FunctionAnomaly{
			OwnerType: row.OwnerType, OwnerID: row.OwnerID,
			FunctionID: row.ID, FunctionName: row.Name, ProviderName: row.ProviderName,
			Status: status,
			Detail: "an abnormal call was recorded while this function is still under first-run observation",
		})
	}
}

// audit writes one call record. It returns the row id (0 when the write
// failed: an audit failure must never change a call's outcome).
func (s *OperationFunctionService) audit(ctx context.Context, row store.IntegrationFunction, in FunctionInvokeInput, caller, status, code, msg string,
	ops []string, res *sandbox.RunResult, replayed, observation bool, spec sandbox.Spec) int64 {

	inputJSON, _ := json.Marshal(in.Input)
	sum := sha256.Sum256(inputJSON)
	opsJSON, _ := json.Marshal(ops)
	if ops == nil {
		opsJSON = []byte("[]")
	}
	params := store.CreateFunctionCallAuditParams{
		OwnerType:      row.OwnerType,
		OwnerID:        row.OwnerID,
		UserID:         in.UserID,
		FunctionID:     row.ID,
		ProviderID:     row.ProviderID,
		FunctionName:   row.Name,
		ProviderName:   row.ProviderName,
		WorkspaceID:    in.WorkspaceID,
		Caller:         caller,
		Status:         status,
		ErrorCode:      code,
		ErrorMessage:   truncateMsg(msg, 1000),
		EgressOps:      string(opsJSON),
		InputSha256:    hex.EncodeToString(sum[:]),
		InputBytes:     int64(len(inputJSON)),
		IdempotencyKey: strings.TrimSpace(in.IdempotencyKey),
		CodeHash:       spec.CodeHash(),
	}
	if replayed {
		params.Replayed = 1
	}
	if observation {
		params.Observation = 1
	}
	if res != nil {
		params.DurationMs = res.DurationMS
		params.EgressCalls = int64(res.EgressCalls)
		params.PeakMemoryBytes = res.PeakMemoryBytes
		params.FenceBackend = res.FenceBackend
		if raw, err := json.Marshal(res.Output); err == nil {
			params.OutputBytes = int64(len(raw))
		}
	}
	created, err := s.q.CreateFunctionCallAudit(ctx, params)
	if err != nil {
		return 0
	}
	return created.ID
}

// --- audit read side ------------------------------------------------------

// FunctionCallRecord is one audit row as the UI reads it.
type FunctionCallRecord struct {
	ID              int64     `json:"id"`
	FunctionID      int64     `json:"function_id"`
	FunctionName    string    `json:"function_name"`
	ProviderName    string    `json:"provider_name"`
	Caller          string    `json:"caller"`
	Status          string    `json:"status"`
	ErrorCode       string    `json:"error_code"`
	ErrorMessage    string    `json:"error_message"`
	DurationMS      int64     `json:"duration_ms"`
	EgressCalls     int64     `json:"egress_calls"`
	EgressOps       []string  `json:"egress_ops"`
	InputSha256     string    `json:"input_sha256"`
	OutputBytes     int64     `json:"output_bytes"`
	PeakMemoryBytes int64     `json:"peak_memory_bytes"`
	IdempotencyKey  string    `json:"idempotency_key"`
	Replayed        bool      `json:"replayed"`
	Observation     bool      `json:"observation"`
	FenceBackend    string    `json:"fence_backend"`
	CodeHash        string    `json:"code_hash"`
	CreatedAt       time.Time `json:"created_at"`
}

func callRecordFromRow(r store.FunctionCallAudit) FunctionCallRecord {
	rec := FunctionCallRecord{
		ID: r.ID, FunctionID: r.FunctionID, FunctionName: r.FunctionName,
		ProviderName: r.ProviderName, Caller: r.Caller, Status: r.Status,
		ErrorCode: r.ErrorCode, ErrorMessage: r.ErrorMessage,
		DurationMS: r.DurationMs, EgressCalls: r.EgressCalls,
		InputSha256: r.InputSha256, OutputBytes: r.OutputBytes,
		PeakMemoryBytes: r.PeakMemoryBytes, IdempotencyKey: r.IdempotencyKey,
		Replayed: r.Replayed != 0, Observation: r.Observation != 0,
		FenceBackend: r.FenceBackend, CodeHash: r.CodeHash, CreatedAt: r.CreatedAt,
	}
	_ = json.Unmarshal([]byte(r.EgressOps), &rec.EgressOps)
	return rec
}

// Audit returns the owner's call history, newest first.
func (s *OperationFunctionService) Audit(ctx context.Context, ownerType string, ownerID int64, limit int64) ([]FunctionCallRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.q.ListFunctionCallAudit(ctx, store.ListFunctionCallAuditParams{
		OwnerType: ownerType, OwnerID: ownerID, LimitCount: limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]FunctionCallRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, callRecordFromRow(r))
	}
	return out, nil
}

// ObservationTrail returns the full request trail of a function's first-run
// window — the artefact the observation period exists to produce.
func (s *OperationFunctionService) ObservationTrail(ctx context.Context, functionID, limit int64) ([]FunctionCallRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.q.ListObservationFunctionCalls(ctx, store.ListObservationFunctionCallsParams{
		FunctionID: functionID, LimitCount: limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]FunctionCallRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, callRecordFromRow(r))
	}
	return out, nil
}

// --- helpers --------------------------------------------------------------

// specFromRow turns a stored row into an executable spec, re-validating it so a
// row that was tampered with in the database cannot skip the runtime checks.
func specFromRow(row store.IntegrationFunction) (sandbox.Spec, error) {
	spec := sandbox.Spec{
		Name:        row.Name,
		Description: row.Description,
		Runtime:     row.Runtime,
		Code:        row.Code,
		Entry:       row.Entry,
		Idempotent:  row.Idempotent != 0,
	}
	_ = json.Unmarshal([]byte(row.LimitsJson), &spec.Limits)
	if err := json.Unmarshal([]byte(row.EgressJson), &spec.Egress); err != nil {
		return spec, fmt.Errorf("egress allowlist is unreadable: %w", err)
	}
	return spec.Normalize()
}

func truncateMsg(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// egressTrail records the ordered operations a call actually attempted. The
// sandbox runs egress on the supervisor goroutine, so no lock is needed; the
// slice is only read after Run returns.
type egressTrail struct{ ops []string }

func (t *egressTrail) add(op string) { t.ops = append(t.ops, op) }

func (t *egressTrail) snapshot() []string {
	if len(t.ops) == 0 {
		return nil
	}
	out := make([]string, len(t.ops))
	copy(out, t.ops)
	return out
}

// functionScopeKeys reduces a stored scope to the comparable set a rollback
// diffs: the declared egress operations and the idempotency declaration.
func functionScopeKeys(egressJSON string, idempotent bool) map[string]bool {
	keys := map[string]bool{}
	var ops []sandbox.EgressOp
	if json.Unmarshal([]byte(egressJSON), &ops) == nil {
		for _, o := range ops {
			keys[o.Key()] = true
		}
	}
	if idempotent {
		keys["idempotent"] = true
	}
	return keys
}

// diffScopeKeys reports the authority difference between the live scope and a
// candidate. Keys present in the candidate but not live are RE-GRANTS (the
// dangerous direction) and are listed first so the confirmation card leads with
// them; revoked keys are listed too, because replacing a live policy is itself
// a decision the user should make consciously.
func diffScopeKeys(live, candidate map[string]bool) []FunctionScopeDiff {
	var granted, revoked []FunctionScopeDiff
	for k := range candidate {
		if !live[k] {
			granted = append(granted, FunctionScopeDiff{Key: k, Before: "not allowed", After: "allowed"})
		}
	}
	for k := range live {
		if !candidate[k] {
			revoked = append(revoked, FunctionScopeDiff{Key: k, Before: "allowed", After: "not allowed"})
		}
	}
	sort.Slice(granted, func(i, j int) bool { return granted[i].Key < granted[j].Key })
	sort.Slice(revoked, func(i, j int) bool { return revoked[i].Key < revoked[j].Key })
	return append(granted, revoked...)
}

// --- per-function rate limiter --------------------------------------------

// functionRateLimiter is an in-process sliding-window accountant keyed by
// function id. Personal-edition scope: one server process owns all calls, so a
// process-local window is the whole picture. The accounting is per function,
// which is what "按函数记账" asks for.
type functionRateLimiter struct {
	mu      sync.Mutex
	windows map[int64]*functionWindow
	now     func() time.Time
}

type functionWindow struct {
	start  time.Time
	calls  int
	egress int
}

func newFunctionRateLimiter() *functionRateLimiter {
	return &functionRateLimiter{windows: map[int64]*functionWindow{}, now: time.Now}
}

func (l *functionRateLimiter) windowLocked(id int64) *functionWindow {
	w := l.windows[id]
	now := l.now()
	if w == nil || now.Sub(w.start) >= time.Minute {
		w = &functionWindow{start: now}
		l.windows[id] = w
	}
	return w
}

// reserve books one invocation, reporting false (with the time until the
// window rolls) when the call budget is spent.
func (l *functionRateLimiter) reserve(id int64) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.windowLocked(id)
	if w.calls >= functionCallsPerMinute {
		return time.Minute - l.now().Sub(w.start), false
	}
	w.calls++
	return 0, true
}

// allowEgress books one outbound call.
func (l *functionRateLimiter) allowEgress(id int64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.windowLocked(id)
	if w.egress >= functionEgressPerMinute {
		return false
	}
	w.egress++
	return true
}
