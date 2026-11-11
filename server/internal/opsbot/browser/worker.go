package browser

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/niuniu-dev/niuniu/internal/opsbot/packs"
)

// Task execution outcomes that the worker must surface distinctly so callers
// can degrade safely instead of retrying blindly.
var (
	// ErrLoginExpired means the platform session is no longer authenticated.
	// The worker never retries against an expired login; it records the state
	// and asks the operator to scan a fresh QR code.
	ErrLoginExpired = errors.New("opsbot/browser: login expired")
	// ErrRecipeBroken means a recipe assertion/selector failed, typically
	// because the platform UI changed. The caller degrades to a manual
	// publish package instead of retrying.
	ErrRecipeBroken = errors.New("opsbot/browser: recipe broken")
	// ErrRateLimited means the platform signalled throttling (429, captcha
	// gate, "too many requests").
	ErrRateLimited = errors.New("opsbot/browser: rate limited")
	// ErrBlocked means the account is blocked or the platform presented a
	// ban notice. All automatic actions pause.
	ErrBlocked = errors.New("opsbot/browser: account blocked")
	// ErrTimeout is a transient timeout; the worker may retry with backoff.
	ErrTimeout = errors.New("opsbot/browser: task timed out")
	// ErrTransient marks other retryable failures (network blips, navigation
	// race).
	ErrTransient = errors.New("opsbot/browser: transient failure")
	// ErrNoExecutor means the worker was wired without an Exec function.
	ErrNoExecutor = errors.New("opsbot/browser: no executor configured")
)

const (
	defaultMaxConcurrent = 4
	defaultMaxAttempts   = 3
	defaultRetryBase     = 500 * time.Millisecond
	defaultJitterMin     = 500 * time.Millisecond
	defaultJitterMax     = 3 * time.Second
	evidenceTimeout      = 5 * time.Second
	reloginTimeout       = 5 * time.Second
)

// Task is one browser operation against a single bot account.
type Task struct {
	BotID   int64
	Backend string
	Recipe  string
	// RecipeSpec is the parsed recipe the executor runs. The caller that
	// resolved the Pack supplies it; the worker never reads recipe files.
	RecipeSpec *packs.Recipe
	// AllowedHosts is the Pack's navigation allowlist. An empty list denies
	// every navigation.
	AllowedHosts []string
	// IsLogin marks the login recipe. It is the only task allowed to run while
	// the stored session is expired (otherwise recovery would be impossible).
	IsLogin bool
	// Input is forwarded verbatim to the executor (title, body, media paths,
	// reply text, ...).
	Input map[string]string
}

// Result is the executor's outcome for a Task.
type Result struct {
	// Values are the recipe's extracted outputs (note URL, metrics, ...).
	Values map[string]string
	// Receipt is the platform's acknowledgement (post id, message id).
	Receipt string
	// Screenshot is optional evidence captured by the executor. The worker
	// persists it under EvidenceDir and sets EvidencePath.
	Screenshot []byte
	// EvidencePath is filled by the worker after persisting Screenshot.
	EvidencePath string
}

// ExecFunc executes a recipe for a task. It should return one of the sentinel
// errors above so the worker can update session state correctly.
type ExecFunc func(ctx context.Context, task Task) (Result, error)

// ReloginRequest describes why a bot needs an operator to log in again.
type ReloginRequest struct {
	BotID   int64
	Backend string
	Reason  string
}

// ReloginNotifier creates the "needs re-login" task and notifies the operator
// (e.g. an imbot message asking them to scan a QR code).
type ReloginNotifier interface {
	RequestRelogin(ctx context.Context, req ReloginRequest) error
}

// Worker runs browser tasks with per-account serialization and evidence
// capture. It is safe for concurrent use.
type Worker struct {
	Sessions *SessionManager
	Exec     ExecFunc
	// EvidenceDir is the root for screenshots and receipts. When empty,
	// evidence is not persisted.
	EvidenceDir string
	Notifier    ReloginNotifier
	// OnRisk is notified when a task failure implies account-level risk
	// (login_expired / rate_limited / banned / recipe_broken). The values match
	// the opsbot risk signals; wire it to Risk.Evaluate / Risk.HandleAdapterError.
	OnRisk func(ctx context.Context, botID int64, signal string)

	// MaxConcurrent caps in-flight tasks across all accounts.
	MaxConcurrent int
	// LeaseTTL is how long an acquired operation lease stays valid.
	LeaseTTL time.Duration
	// JitterMin/JitterMax bound the randomized delay before each task. Both
	// zero disables the delay (used by tests).
	JitterMin, JitterMax time.Duration
	// MaxAttempts is the total number of executions for a retryable failure.
	MaxAttempts int
	// RetryBase is the first backoff step; it doubles per attempt.
	RetryBase time.Duration
	// OwnerID identifies this worker in leases. Generated when empty.
	OwnerID string

	initOnce sync.Once
	sem      chan struct{}
	locksMu  sync.Mutex
	locks    map[int64]*accountLock
}

type accountLock struct {
	slot chan struct{}
	refs int
}

// NewWorker returns a worker with production defaults.
func NewWorker(sessions *SessionManager) *Worker {
	return &Worker{
		Sessions:      sessions,
		MaxConcurrent: defaultMaxConcurrent,
		LeaseTTL:      defaultLeaseTTL,
		JitterMin:     defaultJitterMin,
		JitterMax:     defaultJitterMax,
		MaxAttempts:   defaultMaxAttempts,
		RetryBase:     defaultRetryBase,
	}
}

// Run executes one task: ensure the account profile, take the operation lease,
// run the recipe with randomized spacing, persist evidence, update health/risk,
// and release the lease.
//
// Tasks for the same account are serialized in-process and across processes by
// the database lease, so two operations can never run concurrently on one
// account.
func (w *Worker) Run(ctx context.Context, task Task) (Result, error) {
	if err := w.validate(task); err != nil {
		return Result{}, err
	}
	w.init()

	sess, err := w.Sessions.Ensure(ctx, task.BotID, task.Backend)
	if err != nil {
		return Result{}, err
	}

	// Refuse to act on a session that already needs operator attention. The
	// login recipe is the exception: it is exactly what recovers an expired
	// session, so it must still run.
	if !task.IsLogin {
		switch sess.LoginStatus {
		case LoginExpired:
			w.notifyRelogin(ctx, task, "login expired")
			return Result{}, ErrLoginExpired
		}
	}
	switch sess.RiskState {
	case RiskBlocked:
		return Result{}, fmt.Errorf("%w: bot %d risk state blocked", ErrBlocked, task.BotID)
	case RiskThrottled:
		return Result{}, fmt.Errorf("%w: bot %d risk state throttled", ErrRateLimited, task.BotID)
	}

	unlock, err := w.lockAccount(ctx, task.BotID)
	if err != nil {
		return Result{}, err
	}
	defer unlock()

	if err := w.acquireSlot(ctx); err != nil {
		return Result{}, err
	}
	defer w.releaseSlot()

	if err := w.Sessions.Acquire(ctx, task.BotID, task.Backend, w.OwnerID, w.LeaseTTL); err != nil {
		return Result{}, err
	}
	defer w.releaseLease(task)

	// Randomized spacing keeps a burst of actions from looking mechanical.
	if err := w.sleep(ctx, w.jitter()); err != nil {
		return Result{}, err
	}

	res, execErr := w.execute(ctx, task)
	if res.Screenshot != nil || res.Receipt != "" {
		path, evErr := w.persistEvidence(task, res)
		res.EvidencePath = path
		if evErr != nil {
			execErr = errors.Join(execErr, evErr)
		}
	}

	if execErr == nil {
		return res, w.recordSuccess(ctx, task, sess.LoginStatus)
	}

	stateErr := w.recordFailure(ctx, task, execErr)
	return Result{}, errors.Join(execErr, stateErr)
}

func (w *Worker) validate(task Task) error {
	if task.BotID <= 0 {
		return fmt.Errorf("%w: bot id %d", ErrInvalidSession, task.BotID)
	}
	if err := validateBackend(task.Backend); err != nil {
		return err
	}
	if w.Sessions == nil {
		return fmt.Errorf("%w: session manager is nil", ErrInvalidSession)
	}
	if w.Exec == nil {
		return ErrNoExecutor
	}
	return nil
}

func (w *Worker) init() {
	w.initOnce.Do(func() {
		if w.MaxConcurrent <= 0 {
			w.MaxConcurrent = defaultMaxConcurrent
		}
		w.sem = make(chan struct{}, w.MaxConcurrent)
		if w.locks == nil {
			w.locks = make(map[int64]*accountLock)
		}
		if w.LeaseTTL <= 0 {
			w.LeaseTTL = defaultLeaseTTL
		}
		if w.MaxAttempts <= 0 {
			w.MaxAttempts = defaultMaxAttempts
		}
		if w.RetryBase <= 0 {
			w.RetryBase = defaultRetryBase
		}
		if w.JitterMin < 0 {
			w.JitterMin = 0
		}
		if w.JitterMax < w.JitterMin {
			w.JitterMax = w.JitterMin
		}
		if w.OwnerID == "" {
			w.OwnerID = newOwnerID()
		}
	})
}

func (w *Worker) execute(ctx context.Context, task Task) (Result, error) {
	var (
		res Result
		err error
	)
	for attempt := 1; attempt <= w.MaxAttempts; attempt++ {
		if attempt > 1 {
			backoff := w.RetryBase << (attempt - 2)
			if err := w.sleep(ctx, backoff+w.jitter()/2); err != nil {
				return Result{}, err
			}
		}
		res, err = w.Exec(ctx, task)
		if err == nil {
			return res, nil
		}
		if ctx.Err() != nil || !isRetryable(err) {
			return res, err
		}
	}
	return res, err
}

func isRetryable(err error) bool {
	return errors.Is(err, ErrTimeout) || errors.Is(err, ErrTransient)
}

func (w *Worker) recordSuccess(ctx context.Context, task Task, prevLogin string) error {
	valid := LoginValid
	healthy := HealthHealthy
	normal := RiskNormal
	empty := ""
	patch := StatePatch{
		LoginStatus: &valid,
		Health:      &healthy,
		RiskState:   &normal,
		LastError:   &empty,
	}
	if prevLogin != LoginValid {
		now := time.Now()
		patch.LastLoginAt = &now
	}
	return w.applyState(ctx, task, patch)
}

func (w *Worker) recordFailure(ctx context.Context, task Task, execErr error) error {
	expired := LoginExpired
	degraded := HealthDegraded
	down := HealthDown
	throttled := RiskThrottled
	blocked := RiskBlocked
	msg := execErr.Error()

	var (
		patch      StatePatch
		riskSignal string
	)
	switch {
	case errors.Is(execErr, ErrLoginExpired):
		patch = StatePatch{LoginStatus: &expired, Health: &degraded, LastError: &msg}
		riskSignal = "login_expired"
		w.notifyRelogin(ctx, task, msg)
	case errors.Is(execErr, ErrRecipeBroken):
		patch = StatePatch{Health: &degraded, LastError: &msg}
		riskSignal = "recipe_broken"
	case errors.Is(execErr, ErrRateLimited):
		patch = StatePatch{RiskState: &throttled, LastError: &msg}
		riskSignal = "rate_limited"
	case errors.Is(execErr, ErrBlocked):
		patch = StatePatch{RiskState: &blocked, Health: &down, LastError: &msg}
		riskSignal = "banned"
	case errors.Is(execErr, context.Canceled), errors.Is(execErr, context.DeadlineExceeded):
		// Cancellation is not a session fault; leave state untouched.
		return nil
	default:
		patch = StatePatch{LastError: &msg}
	}
	if err := w.applyState(ctx, task, patch); err != nil {
		return err
	}
	w.notifyRisk(ctx, task, riskSignal)
	return nil
}

func (w *Worker) notifyRisk(ctx context.Context, task Task, signal string) {
	if w.OnRisk == nil || signal == "" {
		return
	}
	riskCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), evidenceTimeout)
	defer cancel()
	w.OnRisk(riskCtx, task.BotID, signal)
}

func (w *Worker) applyState(ctx context.Context, task Task, patch StatePatch) error {
	stateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), evidenceTimeout)
	defer cancel()
	if _, err := w.Sessions.Update(stateCtx, task.BotID, task.Backend, patch); err != nil {
		return err
	}
	return nil
}

func (w *Worker) notifyRelogin(ctx context.Context, task Task, reason string) {
	if w.Notifier == nil {
		return
	}
	notifyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reloginTimeout)
	defer cancel()
	_ = w.Notifier.RequestRelogin(notifyCtx, ReloginRequest{
		BotID:   task.BotID,
		Backend: task.Backend,
		Reason:  reason,
	})
}

func (w *Worker) releaseLease(task Task) {
	ctx, cancel := context.WithTimeout(context.Background(), evidenceTimeout)
	defer cancel()
	_ = w.Sessions.Release(ctx, task.BotID, task.Backend, w.OwnerID)
}

// persistEvidence writes the screenshot and receipt to disk. It returns the
// screenshot path (empty when no screenshot was supplied).
func (w *Worker) persistEvidence(task Task, res Result) (string, error) {
	if w.EvidenceDir == "" {
		return "", nil
	}
	dir := filepath.Join(w.EvidenceDir, fmt.Sprintf("%d", task.BotID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("opsbot/browser: create evidence dir: %w", err)
	}
	base := fmt.Sprintf("%s-%d", sanitizeName(task.Recipe), time.Now().UnixNano())

	var shotPath string
	if len(res.Screenshot) > 0 {
		shotPath = filepath.Join(dir, base+".png")
		if err := os.WriteFile(shotPath, res.Screenshot, 0o600); err != nil {
			return "", fmt.Errorf("opsbot/browser: write screenshot: %w", err)
		}
	}
	if res.Receipt != "" {
		receiptPath := filepath.Join(dir, base+".receipt.txt")
		if err := os.WriteFile(receiptPath, []byte(res.Receipt), 0o600); err != nil {
			return shotPath, fmt.Errorf("opsbot/browser: write receipt: %w", err)
		}
	}
	return shotPath, nil
}

func (w *Worker) jitter() time.Duration {
	if w.JitterMax <= 0 {
		return 0
	}
	lo := w.JitterMin
	hi := w.JitterMax
	if hi <= lo {
		return lo
	}
	return lo + time.Duration(rand.Int64N(int64(hi-lo)))
}

func (w *Worker) sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *Worker) acquireSlot(ctx context.Context) error {
	select {
	case w.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *Worker) releaseSlot() {
	<-w.sem
}

func (w *Worker) lockAccount(ctx context.Context, botID int64) (func(), error) {
	w.locksMu.Lock()
	l := w.locks[botID]
	if l == nil {
		l = &accountLock{slot: make(chan struct{}, 1)}
		w.locks[botID] = l
	}
	l.refs++
	w.locksMu.Unlock()

	select {
	case l.slot <- struct{}{}:
		return func() {
			<-l.slot
			w.releaseAccountRef(botID, l)
		}, nil
	case <-ctx.Done():
		w.releaseAccountRef(botID, l)
		return nil, ctx.Err()
	}
}

func (w *Worker) releaseAccountRef(botID int64, l *accountLock) {
	w.locksMu.Lock()
	l.refs--
	if l.refs == 0 {
		delete(w.locks, botID)
	}
	w.locksMu.Unlock()
}

func newOwnerID() string {
	var b [8]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		return fmt.Sprintf("worker-%d", time.Now().UnixNano())
	}
	return "worker-" + hex.EncodeToString(b[:])
}

func sanitizeName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "task"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "task"
	}
	return out
}
