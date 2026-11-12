package browser

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/niuniu-dev/niuniu/internal/opsbot/packs"
)

// Target identifies an element by CSS selector and match index.
type Target struct {
	Selector string
	Index    int
}

// Driver is the page-automation primitive layer used by LocalBackend. The
// production implementation speaks CDP to a Chrome instance; tests inject a
// fake. Recipes can only reach the actions exposed here.
type Driver interface {
	Goto(ctx context.Context, url string) error
	CurrentURL(ctx context.Context) (string, error)
	Count(ctx context.Context, selector string) (int, error)
	Wait(ctx context.Context, target Target, timeout time.Duration) error
	Click(ctx context.Context, target Target, timeout time.Duration) error
	Fill(ctx context.Context, target Target, value string, timeout time.Duration) error
	Upload(ctx context.Context, target Target, files []string, timeout time.Duration) error
	Extract(ctx context.Context, target Target, attr string) (string, error)
	Screenshot(ctx context.Context) ([]byte, error)
	Close(ctx context.Context) error
}

// DriverFactory opens a driver for an account profile directory.
type DriverFactory func(ctx context.Context, profileDir string, cfg LocalConfig) (Driver, error)

const (
	defaultActionTimeout   = 15 * time.Second
	defaultNavigateTimeout = 30 * time.Second
	maxLoopIterations      = 100
	maxExecDepth           = 8
)

// LocalBackend runs recipes in a browser on this machine. Each bot account
// gets its own profile directory (cookies/localStorage/fingerprint), and every
// navigation is checked against the session's allowlist.
type LocalBackend struct {
	cfg LocalConfig

	seq atomic.Uint64

	mu            sync.Mutex
	sessions      map[string]*localSession
	profileOwners map[string]int64
}

type localSession struct {
	handle SessionHandle
	driver Driver
	mu     sync.Mutex
	closed bool
}

// NewLocalBackend validates the local configuration and returns a backend.
func NewLocalBackend(cfg LocalConfig) (*LocalBackend, error) {
	if strings.TrimSpace(cfg.DataDir) == "" {
		return nil, &ConfigError{
			Kind:   BackendLocal,
			Reason: "local data dir is empty",
			Hint:   "set LocalConfig.DataDir to the server data root (~/.niuniu)",
		}
	}
	if cfg.ActionTimeout <= 0 {
		cfg.ActionTimeout = defaultActionTimeout
	}
	if cfg.NavigateTimeout <= 0 {
		cfg.NavigateTimeout = defaultNavigateTimeout
	}
	if cfg.LeaseTTL <= 0 {
		cfg.LeaseTTL = defaultLeaseTTL
	}
	return &LocalBackend{
		cfg:           cfg,
		sessions:      map[string]*localSession{},
		profileOwners: map[string]int64{},
	}, nil
}

// Start opens an isolated browser session for spec.BotID.
func (b *LocalBackend) Start(ctx context.Context, spec SessionSpec) (SessionHandle, error) {
	if spec.BotID <= 0 {
		return SessionHandle{}, fmt.Errorf("%w: bot id %d", ErrInvalidSession, spec.BotID)
	}
	backend := spec.Backend
	if backend == "" {
		backend = BackendLocal
	}
	if backend != BackendLocal {
		return SessionHandle{}, &ConfigError{
			Kind:   backend,
			Reason: "LocalBackend only runs local sessions",
			Hint:   "build the cloud backend with NewBackend and the enterprise factory",
		}
	}
	if err := validateAllowedHosts(spec.AllowedHosts); err != nil {
		return SessionHandle{}, err
	}

	profileDir, err := b.resolveProfileDir(ctx, spec)
	if err != nil {
		return SessionHandle{}, err
	}
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		return SessionHandle{}, fmt.Errorf("opsbot/browser: create profile dir: %w", err)
	}
	// Existing directories keep their mode; make sure a fresh one is private.
	_ = os.Chmod(profileDir, 0o700)

	b.mu.Lock()
	if owner, ok := b.profileOwners[profileDir]; ok && owner != spec.BotID {
		b.mu.Unlock()
		return SessionHandle{}, fmt.Errorf("%w: %s is already used by bot %d", ErrProfileShared, profileDir, owner)
	}
	for _, other := range b.sessions {
		if !other.closed && other.handle.ProfileDir == profileDir {
			b.mu.Unlock()
			return SessionHandle{}, fmt.Errorf("%w: %s already has an open session", ErrProfileShared, profileDir)
		}
	}
	b.profileOwners[profileDir] = spec.BotID
	b.mu.Unlock()

	factory := b.cfg.DriverFactory
	if factory == nil {
		factory = newCDPDriver
	}
	driver, err := factory(ctx, profileDir, b.cfg)
	if err != nil {
		b.releaseProfile(profileDir, spec.BotID)
		return SessionHandle{}, fmt.Errorf("opsbot/browser: start driver: %w", err)
	}

	handle := SessionHandle{
		ID:           fmt.Sprintf("local-%d-%d", spec.BotID, b.seq.Add(1)),
		BotID:        spec.BotID,
		Backend:      BackendLocal,
		ProfileDir:   profileDir,
		AllowedHosts: append([]string(nil), spec.AllowedHosts...),
		TenantID:     spec.TenantID,
	}
	b.mu.Lock()
	b.sessions[handle.ID] = &localSession{handle: handle, driver: driver}
	b.mu.Unlock()
	return handle, nil
}

func (b *LocalBackend) resolveProfileDir(ctx context.Context, spec SessionSpec) (string, error) {
	dir := strings.TrimSpace(spec.ProfileDir)
	if dir == "" {
		if b.cfg.Sessions != nil {
			sess, err := b.cfg.Sessions.Ensure(ctx, spec.BotID, BackendLocal)
			if err != nil {
				return "", err
			}
			dir = sess.ProfileDir
		} else {
			dir = filepath.Join(b.cfg.DataDir, "ops", "browser", strconv.FormatInt(spec.BotID, 10), BackendLocal)
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("opsbot/browser: resolve profile dir %q: %w", dir, err)
	}
	return abs, nil
}

func (b *LocalBackend) releaseProfile(dir string, botID int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if owner, ok := b.profileOwners[dir]; ok && owner == botID {
		delete(b.profileOwners, dir)
	}
}

// Stop closes the session. The profile directory (and its login state) stays
// on disk so the operator does not have to scan a QR code again. Stopping an
// already-closed session is a no-op.
func (b *LocalBackend) Stop(ctx context.Context, handle SessionHandle) error {
	s, err := b.session(handle)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	err = s.driver.Close(ctx)
	b.releaseProfile(s.handle.ProfileDir, s.handle.BotID)
	return err
}

// Exec runs a recipe with the account lease held (unless DisableLease) and
// records success/failure into the shared ops_browser_sessions row.
func (b *LocalBackend) Exec(ctx context.Context, handle SessionHandle, recipe packs.Recipe, in ExecInput) (ExecOutput, error) {
	s, err := b.session(handle)
	if err != nil {
		return ExecOutput{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ExecOutput{}, fmt.Errorf("%w: %s", ErrSessionClosed, handle.ID)
	}

	if err := b.checkUsable(ctx, handle, in.AllowExpired); err != nil {
		return ExecOutput{}, err
	}
	release, err := b.acquireLease(ctx, handle)
	if err != nil {
		return ExecOutput{}, err
	}
	defer release()

	out, execErr := b.runRecipe(ctx, s, recipe, in)
	if len(out.Screenshot) > 0 && b.cfg.EvidenceDir != "" {
		path, evErr := b.persistScreenshot(handle.BotID, recipe.Name, out.Screenshot)
		if evErr != nil {
			execErr = errors.Join(execErr, evErr)
		} else {
			out.ScreenshotPath = path
		}
	}

	if execErr == nil {
		b.recordState(ctx, handle, successPatch())
		return out, nil
	}
	classified := b.classify(ctx, s.driver, execErr)
	b.recordState(ctx, handle, failurePatch(classified))
	return out, classified
}

func (b *LocalBackend) session(handle SessionHandle) (*localSession, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.sessions[handle.ID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownHandle, handle.ID)
	}
	if s.handle.BotID != handle.BotID {
		return nil, fmt.Errorf("%w: handle bot %d does not own session %s", ErrUnknownHandle, handle.BotID, handle.ID)
	}
	return s, nil
}

func (b *LocalBackend) checkUsable(ctx context.Context, handle SessionHandle, allowExpired bool) error {
	if b.cfg.Sessions == nil {
		return nil
	}
	sess, err := b.cfg.Sessions.State(ctx, handle.BotID, BackendLocal)
	if err != nil {
		if errors.Is(err, ErrSessionNotFound) {
			return nil
		}
		return err
	}
	if sess.LoginStatus == LoginExpired && !allowExpired {
		return fmt.Errorf("%w: bot %d", ErrLoginExpired, handle.BotID)
	}
	switch sess.RiskState {
	case RiskBlocked:
		return fmt.Errorf("%w: bot %d risk state blocked", ErrBlocked, handle.BotID)
	case RiskThrottled:
		return fmt.Errorf("%w: bot %d risk state throttled", ErrRateLimited, handle.BotID)
	}
	return nil
}

// acquireLease takes the same DB lease Worker uses, so a LocalBackend-driven
// operation and a Worker-driven one can never touch one account at the same
// time. It returns a no-op release when lease management is disabled.
func (b *LocalBackend) acquireLease(ctx context.Context, handle SessionHandle) (func(), error) {
	if b.cfg.Sessions == nil || b.cfg.DisableLease {
		return func() {}, nil
	}
	owner := fmt.Sprintf("backend-%s-%d", handle.ID, b.seq.Add(1))
	if err := b.cfg.Sessions.Acquire(ctx, handle.BotID, BackendLocal, owner, b.cfg.LeaseTTL); err != nil {
		return nil, err
	}
	return func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), defaultActionTimeout)
		defer cancel()
		_ = b.cfg.Sessions.Release(releaseCtx, handle.BotID, BackendLocal, owner)
	}, nil
}

func (b *LocalBackend) recordState(ctx context.Context, handle SessionHandle, patch StatePatch) {
	if b.cfg.Sessions == nil || patch.empty() {
		return
	}
	stateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), defaultActionTimeout)
	defer cancel()
	_, err := b.cfg.Sessions.Update(stateCtx, handle.BotID, BackendLocal, patch)
	if err != nil && !errors.Is(err, ErrSessionNotFound) {
		return
	}
}

func (p StatePatch) empty() bool {
	return p.LoginStatus == nil && p.LastLoginAt == nil && p.Health == nil && p.RiskState == nil && p.LastError == nil
}

func successPatch() StatePatch {
	valid := LoginValid
	healthy := HealthHealthy
	normal := RiskNormal
	empty := ""
	return StatePatch{LoginStatus: &valid, Health: &healthy, RiskState: &normal, LastError: &empty}
}

func failurePatch(err error) StatePatch {
	expired := LoginExpired
	degraded := HealthDegraded
	down := HealthDown
	throttled := RiskThrottled
	blocked := RiskBlocked
	msg := err.Error()

	switch {
	case errors.Is(err, ErrLoginExpired):
		return StatePatch{LoginStatus: &expired, Health: &degraded, LastError: &msg}
	case errors.Is(err, ErrRecipeBroken):
		return StatePatch{Health: &degraded, LastError: &msg}
	case errors.Is(err, ErrRateLimited):
		return StatePatch{RiskState: &throttled, LastError: &msg}
	case errors.Is(err, ErrBlocked):
		return StatePatch{RiskState: &blocked, Health: &down, LastError: &msg}
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return StatePatch{}
	default:
		return StatePatch{LastError: &msg}
	}
}

// classify maps a driver/interpreter failure to a sentinel the Worker can act
// on. Login redirects and risk banners win over generic selector failures
// because retrying those would be harmful.
func (b *LocalBackend) classify(ctx context.Context, driver Driver, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrHostNotAllowed) || errors.Is(err, ErrInvalidRecipe) ||
		errors.Is(err, ErrMissingInput) || errors.Is(err, ErrUnknownAction) ||
		errors.Is(err, ErrFileNotAllowed) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.Join(ErrTimeout, err)
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	if errors.Is(err, ErrLoginExpired) || errors.Is(err, ErrBlocked) || errors.Is(err, ErrRateLimited) {
		return err
	}

	if url, uerr := driver.CurrentURL(ctx); uerr == nil && isLoginURL(url) {
		return errors.Join(ErrLoginExpired, err)
	}
	if risk := detectRisk(ctx, driver); risk != nil {
		return errors.Join(risk, err)
	}

	var netErr *url.Error
	if errors.As(err, &netErr) {
		return errors.Join(ErrTransient, err)
	}
	if errors.Is(err, ErrSelectorNotFound) || errors.Is(err, ErrAssertFailed) {
		return errors.Join(ErrRecipeBroken, err)
	}
	return errors.Join(ErrTransient, err)
}

func isLoginURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	path := strings.ToLower(u.Path)
	if strings.HasPrefix(host, "login.") || strings.HasPrefix(host, "passport.") {
		return true
	}
	for _, marker := range []string{"/login", "/signin", "/passport", "/auth/"} {
		if strings.Contains(path, marker) {
			return true
		}
	}
	return false
}

func detectRisk(ctx context.Context, driver Driver) error {
	text, err := driver.Extract(ctx, Target{Selector: "body"}, "")
	if err != nil {
		return nil
	}
	low := strings.ToLower(text)
	for _, marker := range []string{"账号已被封", "account suspended", "account banned"} {
		if strings.Contains(low, marker) {
			return ErrBlocked
		}
	}
	for _, marker := range []string{"captcha", "验证码", "操作过于频繁", "操作频繁", "too many requests", "rate limit"} {
		if strings.Contains(low, marker) {
			return ErrRateLimited
		}
	}
	return nil
}

func (b *LocalBackend) persistScreenshot(botID int64, recipeName string, data []byte) (string, error) {
	dir := filepath.Join(b.cfg.EvidenceDir, strconv.FormatInt(botID, 10))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("opsbot/browser: create evidence dir: %w", err)
	}
	name := fmt.Sprintf("%s-%d.png", sanitizeName(recipeName), time.Now().UnixNano())
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("opsbot/browser: write screenshot: %w", err)
	}
	return path, nil
}

// Health reports whether the session's page is alive and still inside the
// allowlist. Browser failures are reported as State=down rather than an error;
// only an unknown/closed handle returns an error.
func (b *LocalBackend) Health(ctx context.Context, handle SessionHandle) (Health, error) {
	s, err := b.session(handle)
	if err != nil {
		return Health{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Health{}, fmt.Errorf("%w: %s", ErrSessionClosed, handle.ID)
	}

	h := Health{State: HealthHealthy, LoginStatus: LoginUnknown, CheckedAt: time.Now()}
	if b.cfg.Sessions != nil {
		if sess, err := b.cfg.Sessions.State(ctx, handle.BotID, BackendLocal); err == nil {
			h.LoginStatus = sess.LoginStatus
		}
	}
	currentURL, err := s.driver.CurrentURL(ctx)
	if err != nil {
		h.State = HealthDown
		h.Detail = err.Error()
		return h, nil
	}
	h.URL = currentURL
	parsed, perr := url.Parse(currentURL)
	if perr != nil {
		h.State = HealthDegraded
		h.Detail = perr.Error()
		return h, nil
	}
	if !HostAllowed(parsed.Hostname(), handle.AllowedHosts) {
		h.State = HealthDegraded
		h.Detail = fmt.Sprintf("current page host %s is outside the allowlist", parsed.Hostname())
	}
	return h, nil
}

// ---- recipe interpreter ----

type recipeExec struct {
	driver          Driver
	opts            execOptions
	stepTimeout     time.Duration
	navigateTimeout time.Duration
}

type execOptions struct {
	allowedHosts []string
	fileRoot     string
	files        map[string]string
}

type runState struct {
	out  ExecOutput
	vars map[string]string
}

func (b *LocalBackend) runRecipe(ctx context.Context, s *localSession, recipe packs.Recipe, in ExecInput) (ExecOutput, error) {
	out := ExecOutput{Values: map[string]string{}}
	for k, v := range in.Values {
		out.Values[k] = v
	}
	if len(recipe.Steps) == 0 {
		return out, fmt.Errorf("%w: recipe %q has no steps", ErrInvalidRecipe, recipe.Name)
	}

	ex := &recipeExec{
		driver:          s.driver,
		opts:            execOptions{allowedHosts: s.handle.AllowedHosts, fileRoot: b.cfg.FileRoot, files: in.Files},
		stepTimeout:     b.cfg.ActionTimeout,
		navigateTimeout: b.cfg.NavigateTimeout,
	}
	st := &runState{out: out, vars: out.Values}
	if err := ex.run(ctx, recipe.Steps, st, Target{}, 0); err != nil {
		return st.out, err
	}
	st.out.Values = st.vars
	return st.out, nil
}

func (e *recipeExec) run(ctx context.Context, steps []packs.RecipeStep, st *runState, defaultTarget Target, depth int) error {
	if depth > maxExecDepth {
		return fmt.Errorf("%w: recipe nesting is too deep", ErrInvalidRecipe)
	}
	for i := 0; i < len(steps); i++ {
		step := steps[i]
		switch strings.ToLower(strings.TrimSpace(step.Action)) {
		case "loop":
			if i+1 >= len(steps) {
				return fmt.Errorf("%w: loop has no following step", ErrInvalidRecipe)
			}
			selector, err := e.expand(firstNonEmpty(step.Args["selector"], step.Selector), st)
			if err != nil {
				return err
			}
			if selector == "" {
				return fmt.Errorf("%w: loop has no selector", ErrInvalidRecipe)
			}
			limit := intArg(step.Args, "max", 10)
			if limit > maxLoopIterations {
				limit = maxLoopIterations
			}
			count, err := e.driver.Count(ctx, selector)
			if err != nil {
				return err
			}
			if count > limit {
				count = limit
			}
			prevIndex, hadIndex := st.vars["index"]
			for idx := 0; idx < count; idx++ {
				st.vars["index"] = strconv.Itoa(idx)
				childTarget := Target{Selector: selector, Index: idx}
				if err := e.run(ctx, steps[i+1:i+2], st, childTarget, depth+1); err != nil {
					return err
				}
			}
			if hadIndex {
				st.vars["index"] = prevIndex
			} else {
				delete(st.vars, "index")
			}
			i++
		case "branch":
			selector, err := e.expand(firstNonEmpty(step.Args["selector"], step.Selector), st)
			if err != nil {
				return err
			}
			if selector == "" {
				return fmt.Errorf("%w: branch has no condition selector", ErrInvalidRecipe)
			}
			count, err := e.driver.Count(ctx, selector)
			if err != nil {
				return err
			}
			when := strings.ToLower(firstNonEmpty(step.Args["when"], "present"))
			ok := (when == "present" && count > 0) || (when == "absent" && count == 0)
			if !ok {
				skip := intArg(step.Args, "skip", 1)
				if skip < 1 {
					skip = 1
				}
				i += skip
			}
		default:
			if err := e.runOne(ctx, step, st, defaultTarget); err != nil {
				return err
			}
		}
	}
	return nil
}

func (e *recipeExec) runOne(ctx context.Context, step packs.RecipeStep, st *runState, defaultTarget Target) error {
	action := strings.ToLower(strings.TrimSpace(step.Action))
	timeout := e.stepTimeout
	if step.Timeout > 0 {
		timeout = time.Duration(step.Timeout) * time.Millisecond
	}

	switch action {
	case "goto":
		return e.gotoStep(ctx, step, st)
	case "wait":
		if ms := intArg(step.Args, "ms", 0); ms > 0 {
			return sleepCtx(ctx, time.Duration(ms)*time.Millisecond)
		}
		return e.targetStep(ctx, step, st, defaultTarget, func(t Target) error {
			return e.driver.Wait(ctx, t, timeout)
		})
	case "click":
		return e.targetStep(ctx, step, st, defaultTarget, func(t Target) error {
			return e.driver.Click(ctx, t, timeout)
		})
	case "fill":
		value, err := e.expand(step.Value, st)
		if err != nil {
			return err
		}
		return e.targetStep(ctx, step, st, defaultTarget, func(t Target) error {
			return e.driver.Fill(ctx, t, value, timeout)
		})
	case "upload":
		files, err := e.uploadFiles(ctx, step, st)
		if err != nil {
			return err
		}
		return e.targetStep(ctx, step, st, defaultTarget, func(t Target) error {
			return e.driver.Upload(ctx, t, files, timeout)
		})
	case "extract":
		key := strings.TrimSpace(step.Args["as"])
		if key == "" {
			key = "value"
		}
		attr := strings.TrimSpace(step.Args["attr"])
		var value string
		err := e.targetStep(ctx, step, st, defaultTarget, func(t Target) error {
			v, err := e.driver.Extract(ctx, t, attr)
			if err != nil {
				return err
			}
			value = v
			return nil
		})
		if err != nil {
			return err
		}
		st.vars[key] = value
		st.out.Values[key] = value
		if key == "receipt" {
			st.out.Receipt = value
		}
		return nil
	case "screenshot":
		shot, err := e.driver.Screenshot(ctx)
		if err != nil {
			return err
		}
		st.out.Screenshot = shot
		return nil
	case "assert":
		return e.assertStep(ctx, step, st, defaultTarget)
	default:
		return fmt.Errorf("%w: %q", ErrUnknownAction, step.Action)
	}
}

func (e *recipeExec) gotoStep(ctx context.Context, step packs.RecipeStep, st *runState) error {
	candidates := append([]string{step.Value}, step.Fallback...)
	var lastErr error
	for _, raw := range candidates {
		expanded, err := e.expand(raw, st)
		if err != nil {
			return err
		}
		if strings.TrimSpace(expanded) == "" {
			continue
		}
		if _, err := checkNavigation(expanded, e.opts.allowedHosts); err != nil {
			lastErr = err
			continue
		}
		if err := e.driver.Goto(ctx, expanded); err != nil {
			lastErr = err
			continue
		}
		// Redirects are the one way a page can leave the allowlist after the
		// pre-check; verify where the browser actually landed.
		if final, err := e.driver.CurrentURL(ctx); err == nil {
			parsed, perr := url.Parse(final)
			if perr != nil || !HostAllowed(parsed.Hostname(), e.opts.allowedHosts) {
				lastErr = fmt.Errorf("%w: redirected to %s", ErrHostNotAllowed, final)
				continue
			}
		}
		return nil
	}
	if lastErr == nil {
		return fmt.Errorf("%w: goto has no URL", ErrInvalidRecipe)
	}
	return lastErr
}

// targetStep resolves selector fallbacks and runs fn against the first target
// that exists. A step with no selector uses the loop-provided target.
func (e *recipeExec) targetStep(ctx context.Context, step packs.RecipeStep, st *runState, defaultTarget Target, fn func(Target) error) error {
	candidates, err := e.selectors(step, st, defaultTarget)
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		return fmt.Errorf("%w: selector is empty", ErrInvalidRecipe)
	}
	var lastErr error = ErrSelectorNotFound
	for _, target := range candidates {
		if target.Selector != "" {
			count, err := e.driver.Count(ctx, target.Selector)
			if err != nil {
				return err
			}
			if count <= target.Index {
				continue
			}
		}
		lastErr = fn(target)
		if lastErr == nil {
			return nil
		}
		if !errors.Is(lastErr, ErrSelectorNotFound) {
			return lastErr
		}
	}
	return lastErr
}

func (e *recipeExec) assertStep(ctx context.Context, step packs.RecipeStep, st *runState, defaultTarget Target) error {
	candidates, err := e.selectors(step, st, defaultTarget)
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		return fmt.Errorf("%w: assert has no selector", ErrInvalidRecipe)
	}
	when := strings.ToLower(firstNonEmpty(step.Args["when"], "present"))
	equals, hasEquals := literalOrTemplate(step.Args["equals"], st)
	contains, hasContains := literalOrTemplate(step.Args["contains"], st)

	var lastErr error = ErrAssertFailed
	for _, target := range candidates {
		count, err := e.driver.Count(ctx, target.Selector)
		if err != nil {
			return err
		}
		if when == "absent" {
			if count == 0 {
				return nil
			}
			lastErr = fmt.Errorf("%w: %s is present", ErrAssertFailed, target.Selector)
			continue
		}
		if count <= target.Index {
			lastErr = fmt.Errorf("%w: %s", ErrSelectorNotFound, target.Selector)
			continue
		}
		if !hasEquals && !hasContains {
			return nil
		}
		text, err := e.driver.Extract(ctx, target, strings.TrimSpace(step.Args["attr"]))
		if err != nil {
			return err
		}
		if hasEquals && text != equals {
			lastErr = fmt.Errorf("%w: %q != %q", ErrAssertFailed, text, equals)
			continue
		}
		if hasContains && !strings.Contains(text, contains) {
			lastErr = fmt.Errorf("%w: %q does not contain %q", ErrAssertFailed, text, contains)
			continue
		}
		return nil
	}
	return lastErr
}

func (e *recipeExec) selectors(step packs.RecipeStep, st *runState, defaultTarget Target) ([]Target, error) {
	raw := step.Selector
	if strings.TrimSpace(raw) == "" && len(step.Fallback) == 0 {
		if defaultTarget.Selector == "" {
			return nil, nil
		}
		return []Target{defaultTarget}, nil
	}
	out := make([]Target, 0, len(step.Fallback)+1)
	for _, cand := range append([]string{raw}, step.Fallback...) {
		if strings.TrimSpace(cand) == "" {
			continue
		}
		expanded, err := e.expand(cand, st)
		if err != nil {
			return nil, err
		}
		idx := 0
		if target := defaultTarget; target.Selector == expanded {
			idx = target.Index
		}
		out = append(out, Target{Selector: expanded, Index: idx})
	}
	if len(out) == 0 {
		return []Target{defaultTarget}, nil
	}
	return out, nil
}

func (e *recipeExec) uploadFiles(ctx context.Context, step packs.RecipeStep, st *runState) ([]string, error) {
	raw := firstNonEmpty(step.Args["files"], step.Value)
	expanded, err := e.expand(raw, st)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, part := range strings.Split(expanded, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !filepath.IsAbs(part) {
			return nil, fmt.Errorf("%w: upload path %q is not absolute", ErrFileNotAllowed, part)
		}
		clean := filepath.Clean(part)
		if !e.fileAllowed(clean) {
			return nil, fmt.Errorf("%w: %s", ErrFileNotAllowed, clean)
		}
		if info, err := os.Stat(clean); err != nil || info.IsDir() {
			return nil, fmt.Errorf("%w: %s is not a readable file", ErrFileNotAllowed, clean)
		}
		files = append(files, clean)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%w: upload step has no files", ErrInvalidRecipe)
	}
	return files, nil
}

func (e *recipeExec) fileAllowed(path string) bool {
	// Resolve symlinks so a link inside an allowed root cannot smuggle in a
	// file from outside it.
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		target = path
	}
	for _, allowed := range e.opts.files {
		if allowed == "" {
			continue
		}
		abs, err := filepath.Abs(allowed)
		if err != nil {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			abs = resolved
		}
		if filepath.Clean(abs) == target {
			return true
		}
	}
	if e.opts.fileRoot == "" {
		return false
	}
	root, err := filepath.Abs(e.opts.fileRoot)
	if err != nil {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

var placeholderRe = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_.\-]+)\s*\}\}`)

func (e *recipeExec) expand(s string, st *runState) (string, error) {
	if s == "" {
		return "", nil
	}
	var missing string
	out := placeholderRe.ReplaceAllStringFunc(s, func(m string) string {
		key := placeholderRe.FindStringSubmatch(m)[1]
		if v, ok := st.vars[key]; ok {
			return v
		}
		if name, ok := strings.CutPrefix(key, "files."); ok {
			if v, ok := e.opts.files[name]; ok {
				return v
			}
		}
		missing = key
		return m
	})
	if missing != "" {
		return "", fmt.Errorf("%w: template key %q", ErrMissingInput, missing)
	}
	return out, nil
}

func literalOrTemplate(raw string, st *runState) (string, bool) {
	if raw == "" {
		return "", false
	}
	// Assertion values are plain text; templates are expanded by expandStep.
	expanded := placeholderRe.ReplaceAllStringFunc(raw, func(m string) string {
		key := placeholderRe.FindStringSubmatch(m)[1]
		if v, ok := st.vars[key]; ok {
			return v
		}
		return m
	})
	return expanded, true
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func intArg(args map[string]string, key string, def int) int {
	raw, ok := args[key]
	if !ok || strings.TrimSpace(raw) == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return def
	}
	return n
}

func sleepCtx(ctx context.Context, d time.Duration) error {
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
