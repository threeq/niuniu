package opsbot

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/niuniu-dev/niuniu/internal/opsbot/browser"
	"github.com/niuniu-dev/niuniu/internal/opsbot/channel"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// LoginState is the observable state of one bot's browser login.
type LoginState string

const (
	// LoginStateIdle means no login has been started in this process.
	LoginStateIdle LoginState = "idle"
	// LoginStateRunning means a login recipe is executing; the operator must
	// usually scan a QR code in the browser window.
	LoginStateRunning LoginState = "running"
	// LoginStateSucceeded means the last login run completed.
	LoginStateSucceeded LoginState = "succeeded"
	// LoginStateFailed means the last login run failed; LastError says why.
	LoginStateFailed LoginState = "failed"
	// LoginStateNotRequired means the Pack needs no interactive login
	// (openapi/mcp/manual transports).
	LoginStateNotRequired LoginState = "not_required"
)

// Login machine-readable next actions returned to agents and the console.
const (
	LoginActionNone    = "none"
	LoginActionLogin   = "login"
	LoginActionRelogin = "relogin"
)

// LoginStatus is the browser-session read model. It is intentionally
// machine-readable: an agent must be able to decide what to do next without
// reading source code.
type LoginStatus struct {
	State        LoginState `json:"state"`
	LoginStatus  string     `json:"login_status"`
	Backend      string     `json:"backend"`
	HasSession   bool       `json:"has_session"`
	HasLogin     bool       `json:"has_login_recipe"`
	ProfileDir   string     `json:"profile_dir,omitempty"`
	LastLoginAt  *time.Time `json:"last_login_at,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	EvidencePath string     `json:"evidence_path,omitempty"`
	NextAction   string     `json:"next_action"`
}

// LoginBotStore is the bot lookup the login manager needs. *store.Queries
// satisfies it.
type LoginBotStore interface {
	GetOpsBot(ctx context.Context, id int64) (store.OpsBot, error)
}

var _ LoginBotStore = (*store.Queries)(nil)

// loginRecipeAdapter is the optional capability a browser adapter exposes: the
// Pack declares an interactive login recipe and can run it.
type loginRecipeAdapter interface {
	HasLoginRecipe() bool
}

type loginAdapter interface {
	Login(ctx context.Context, accountRef string) (browser.Result, error)
}

type loginRun struct {
	state    LoginState
	started  time.Time
	finished time.Time
	err      error
	evidence string
}

const (
	defaultLoginTimeout  = 5 * time.Minute
	defaultVerifyTimeout = 15 * time.Second
)

// LoginManager makes browser login a first-class, asynchronous action. Starting
// a login returns immediately; the operator scans the QR code while Status is
// polled. Verify is the short, non-interactive probe used by resume and
// onboarding checks, so those never block on a QR scan.
type LoginManager struct {
	AdapterFor func(ctx context.Context, botID int64) (channel.ChannelAdapter, error)
	Bots       LoginBotStore
	Sessions   *browser.SessionManager
	// LoginTimeout bounds one interactive login run (default 5m).
	LoginTimeout time.Duration
	// VerifyTimeout bounds a resume/onboarding probe (default 15s).
	VerifyTimeout time.Duration
	// Backend defaults to local.
	Backend string

	mu   sync.Mutex
	runs map[int64]*loginRun
}

// NewLoginManager wires the manager over the adapter resolver and session store.
func NewLoginManager(adapterFor func(context.Context, int64) (channel.ChannelAdapter, error), bots LoginBotStore, sessions *browser.SessionManager) *LoginManager {
	return &LoginManager{AdapterFor: adapterFor, Bots: bots, Sessions: sessions, runs: map[int64]*loginRun{}}
}

// SupportsLogin reports whether this bot's transport needs an interactive
// browser login.
func (m *LoginManager) SupportsLogin(ctx context.Context, botID int64) (bool, error) {
	adapter, err := m.adapter(ctx, botID)
	if err != nil {
		return false, err
	}
	lr, ok := adapter.(loginRecipeAdapter)
	return ok && lr.HasLoginRecipe(), nil
}

// Start begins a login run and returns immediately with state=running. Starting
// while a run is already in flight is idempotent. A Pack with no login recipe
// returns state=not_required (not an error).
func (m *LoginManager) Start(ctx context.Context, botID int64) (LoginStatus, error) {
	if m == nil || m.Bots == nil {
		return LoginStatus{}, errors.New("opsbot: login manager is not configured")
	}
	adapter, err := m.adapter(ctx, botID)
	if err != nil {
		return LoginStatus{}, err
	}
	lr, ok := adapter.(loginRecipeAdapter)
	if !ok || !lr.HasLoginRecipe() {
		st, err := m.Status(ctx, botID)
		if err == nil {
			st.State = LoginStateNotRequired
			st.NextAction = LoginActionNone
		}
		return st, err
	}

	m.mu.Lock()
	if run := m.runs[botID]; run != nil && run.state == LoginStateRunning {
		m.mu.Unlock()
		return m.Status(ctx, botID)
	}
	run := &loginRun{state: LoginStateRunning, started: time.Now()}
	if m.runs == nil {
		m.runs = map[int64]*loginRun{}
	}
	m.runs[botID] = run
	m.mu.Unlock()

	timeout := m.LoginTimeout
	if timeout <= 0 {
		timeout = defaultLoginTimeout
	}
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	go func() {
		defer cancel()
		err := m.runLogin(runCtx, botID, adapter)
		m.mu.Lock()
		run.finished = time.Now()
		run.err = err
		if err == nil {
			run.state = LoginStateSucceeded
		} else {
			run.state = LoginStateFailed
		}
		m.mu.Unlock()
	}()

	return m.Status(ctx, botID)
}

// Verify probes the stored session with a short timeout. It never starts an
// interactive login: an unauthenticated session returns ErrReloginRequired from
// the risk controller, not a queued QR flow.
func (m *LoginManager) Verify(ctx context.Context, botID int64) error {
	if m == nil || m.Bots == nil {
		return errors.New("opsbot: login manager is not configured")
	}
	adapter, err := m.adapter(ctx, botID)
	if err != nil {
		return err
	}
	if lr, ok := adapter.(loginRecipeAdapter); ok && !lr.HasLoginRecipe() {
		return nil
	}
	bot, err := m.Bots.GetOpsBot(ctx, botID)
	if err != nil {
		return err
	}
	timeout := m.VerifyTimeout
	if timeout <= 0 {
		timeout = defaultVerifyTimeout
	}
	verifyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if v, ok := adapter.(channel.SessionVerifier); ok {
		return v.VerifySession(verifyCtx, bot.AccountRef)
	}
	return adapter.EnsureSession(verifyCtx, bot.AccountRef)
}

// Status reads the persisted session row plus the last in-process login run.
func (m *LoginManager) Status(ctx context.Context, botID int64) (LoginStatus, error) {
	hasLogin := false
	if adapter, err := m.adapter(ctx, botID); err == nil {
		if lr, ok := adapter.(loginRecipeAdapter); ok {
			hasLogin = lr.HasLoginRecipe()
		}
	}

	backend := m.backend()
	out := LoginStatus{
		State:       LoginStateIdle,
		LoginStatus: browser.LoginUnknown,
		Backend:     backend,
		HasLogin:    hasLogin,
	}
	if m.Sessions != nil {
		out.ProfileDir = m.Sessions.ProfileDir(botID, backend)
		sess, err := m.Sessions.State(ctx, botID, backend)
		switch {
		case err == nil:
			out.HasSession = true
			out.LoginStatus = sess.LoginStatus
			out.ProfileDir = sess.ProfileDir
			out.LastError = sess.LastError
			if sess.LastLoginAt.Valid {
				t := sess.LastLoginAt.Time
				out.LastLoginAt = &t
			}
		case errors.Is(err, browser.ErrSessionNotFound):
			// Not registered yet; the profile dir is still the one Ensure will use.
		default:
			return LoginStatus{}, err
		}
	}

	m.mu.Lock()
	run := m.runs[botID]
	var snapshot loginRun
	if run != nil {
		snapshot = *run
	}
	m.mu.Unlock()
	if run != nil {
		out.State = snapshot.state
		out.EvidencePath = snapshot.evidence
		started, finished := snapshot.started, snapshot.finished
		if !started.IsZero() {
			out.StartedAt = &started
		}
		if !finished.IsZero() {
			out.FinishedAt = &finished
		}
		if snapshot.err != nil {
			out.LastError = snapshot.err.Error()
		}
	}

	// A successful run is stale once the persisted session expires again; the
	// session row is authoritative for the current state.
	if out.State == LoginStateSucceeded && out.LoginStatus == browser.LoginExpired {
		out.State = LoginStateIdle
	}
	out.NextAction = nextLoginAction(out)
	return out, nil
}

func nextLoginAction(s LoginStatus) string {
	if !s.HasLogin {
		return LoginActionNone
	}
	if s.State == LoginStateRunning {
		return LoginActionLogin
	}
	switch s.LoginStatus {
	case browser.LoginValid:
		return LoginActionNone
	case browser.LoginExpired:
		return LoginActionRelogin
	default:
		return LoginActionLogin
	}
}

func (m *LoginManager) runLogin(ctx context.Context, botID int64, adapter channel.ChannelAdapter) error {
	bot, err := m.Bots.GetOpsBot(ctx, botID)
	if err != nil {
		return err
	}
	// A stored session that still works needs no QR scan. Probe only when the
	// row says valid, so an expired session goes straight to the interactive
	// login instead of emitting a spurious relogin alert.
	if m.Sessions != nil {
		if sess, serr := m.Sessions.State(ctx, botID, m.backend()); serr == nil && sess.LoginStatus == browser.LoginValid {
			if v, ok := adapter.(channel.SessionVerifier); ok {
				if verr := v.VerifySession(ctx, bot.AccountRef); verr == nil {
					return nil
				}
			}
		}
	}
	if la, ok := adapter.(loginAdapter); ok {
		res, err := la.Login(ctx, bot.AccountRef)
		m.recordEvidence(botID, res.EvidencePath)
		return err
	}
	return adapter.EnsureSession(ctx, bot.AccountRef)
}

func (m *LoginManager) recordEvidence(botID int64, path string) {
	if path == "" {
		return
	}
	m.mu.Lock()
	if run := m.runs[botID]; run != nil {
		run.evidence = path
	}
	m.mu.Unlock()
}

func (m *LoginManager) adapter(ctx context.Context, botID int64) (channel.ChannelAdapter, error) {
	if m == nil || m.AdapterFor == nil {
		return nil, errors.New("opsbot: login manager is not configured")
	}
	adapter, err := m.AdapterFor(ctx, botID)
	if err != nil {
		return nil, fmt.Errorf("opsbot: resolve adapter for login: %w", err)
	}
	return adapter, nil
}

func (m *LoginManager) backend() string {
	if m.Backend != "" {
		return m.Backend
	}
	return browser.BackendLocal
}
