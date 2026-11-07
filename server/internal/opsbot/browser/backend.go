package browser

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/niuniu-dev/niuniu/internal/opsbot/packs"
)

// BrowserBackend is the execution backend contract for browser recipes
// (design §8). LocalBackend runs a browser on this machine with a per-account
// profile; the team edition injects a CloudBackend that runs one container per
// account. Callers never need to know which one they hold.
type BrowserBackend interface {
	// Start opens an isolated browser session for one bot account.
	Start(ctx context.Context, spec SessionSpec) (SessionHandle, error)
	// Stop closes the session. Login state under the profile directory is kept.
	Stop(ctx context.Context, handle SessionHandle) error
	// Exec runs one recipe against the session. Recipes come from a Platform
	// Pack, so their steps are data, not code.
	Exec(ctx context.Context, handle SessionHandle, recipe packs.Recipe, in ExecInput) (ExecOutput, error)
	// Health reports whether the session can still reach its platform.
	Health(ctx context.Context, handle SessionHandle) (Health, error)
}

// SessionSpec describes the account-scoped browser session to open.
type SessionSpec struct {
	BotID int64
	// Backend is "local" or "cloud"; empty defaults to "local".
	Backend string
	// ProfileDir overrides the per-account profile directory. When empty the
	// backend derives it from the data dir (local) or the account container
	// (cloud). Profiles are never shared between accounts.
	ProfileDir string
	// AllowedHosts is the egress allowlist for navigation. An empty list denies
	// every navigation; recipes can never widen it themselves.
	AllowedHosts []string
	// TenantID labels the owning tenant for cloud isolation and audit. Unused
	// by the local backend.
	TenantID string
}

// SessionHandle identifies an open session. It is safe to pass across process
// boundaries for cloud backends; local handles are only valid in the process
// that created them.
type SessionHandle struct {
	ID           string
	BotID        int64
	Backend      string
	ProfileDir   string
	AllowedHosts []string
	TenantID     string
}

// ExecInput carries the caller's data into a recipe. Templates in recipe
// values and selectors use {{key}} for Values and {{files.key}} for Files.
type ExecInput struct {
	Values map[string]string
	// Files maps logical names to absolute local paths. Recipes may upload only
	// paths listed here (or under LocalConfig.FileRoot).
	Files map[string]string
}

// ExecOutput is the recipe's result.
type ExecOutput struct {
	// Values holds the caller's inputs plus every extracted value.
	Values map[string]string
	// Receipt is the platform acknowledgement (post id, message id, ...).
	Receipt string
	// Screenshot is the last screenshot taken by the recipe, if any.
	Screenshot []byte
	// ScreenshotPath is set when the backend persisted the screenshot.
	ScreenshotPath string
}

// Health is a point-in-time session report.
type Health struct {
	State       string // healthy | degraded | down
	LoginStatus string // unknown | valid | expired
	URL         string
	Detail      string
	CheckedAt   time.Time
}

// CloudBackend is the team-edition contract. Implementations live outside this
// repository (enterprise) and are registered with RegisterBackendFactory, so
// the runner keeps using the same BrowserBackend interface.
//
// Required properties, verified by the operations doc and enterprise tests:
//   - one container per bot account, never shared between accounts or tenants;
//   - a tenant label on every container and every receipt;
//   - fixed egress plus the same AllowedHosts allowlist enforced here;
//   - screenshots/receipts written to tenant-scoped object storage;
//   - encrypted login state with tenant-level isolation and tenant-level audit.
type CloudBackend interface {
	BrowserBackend
	// Container reports the isolation facts for an open session.
	Container(ctx context.Context, handle SessionHandle) (ContainerInfo, error)
}

// ContainerInfo describes where a cloud session runs.
type ContainerInfo struct {
	ContainerID string
	TenantID    string
	EgressHosts []string
	// ReceiptPrefix is the object-storage prefix for this account's evidence.
	ReceiptPrefix string
}

// Backend errors. ErrBackendNotConfigured and ErrBackendUnsupported are the
// "loud failure" contract: an unconfigured cloud backend must never silently
// fall back to local execution.
var (
	ErrBackendNotConfigured = errors.New("opsbot/browser: browser backend is not configured")
	ErrBackendUnsupported   = errors.New("opsbot/browser: browser backend is not available in this edition")
	ErrUnknownHandle        = errors.New("opsbot/browser: unknown session handle")
	ErrSessionClosed        = errors.New("opsbot/browser: session is closed")
	ErrHostNotAllowed       = errors.New("opsbot/browser: navigation host is not in the allowlist")
	ErrProfileShared        = errors.New("opsbot/browser: profile directory is shared by multiple bots")
	ErrSelectorNotFound     = errors.New("opsbot/browser: selector not found")
	ErrFileNotAllowed       = errors.New("opsbot/browser: upload file is not allowed")
)

var (
	errInvalidRecipe = errors.New("opsbot/browser: invalid recipe")
	errAssertFailed  = errors.New("opsbot/browser: recipe assertion failed")
)

// ConfigError explains why a backend could not be built. It deliberately
// carries a hint so an operator can fix the configuration instead of guessing.
type ConfigError struct {
	Kind   string
	Reason string
	Hint   string
}

func (e *ConfigError) Error() string {
	msg := fmt.Sprintf("%s (%s): %s", ErrBackendNotConfigured, e.Kind, e.Reason)
	if e.Hint != "" {
		msg += "; hint: " + e.Hint
	}
	return msg
}

// Unwrap lets errors.Is(err, ErrBackendNotConfigured) match configuration
// failures without losing the specific kind/reason.
func (e *ConfigError) Unwrap() error { return ErrBackendNotConfigured }

// BackendConfig is the factory input. Only the section named by Kind is used.
type BackendConfig struct {
	Kind  string
	Local LocalConfig
	Cloud CloudConfig
}

// LocalConfig configures LocalBackend.
type LocalConfig struct {
	// DataDir is the server data root (normally ~/.niuniu). Profiles live under
	// ops/browser/<botID>/local/.
	DataDir string
	// EvidenceDir persists screenshots. Empty disables persistence (the bytes
	// are still returned in ExecOutput).
	EvidenceDir string
	// ChromePath overrides browser discovery (also NIUNIU_CHROME_PATH).
	ChromePath string
	// Headless runs the browser without a window. Login-by-QR flows need a
	// headed browser, so this defaults to false.
	Headless bool
	// DisableLease turns off taking the ops_browser_sessions lease inside Exec.
	// Set it when the caller (Worker) already holds the lease.
	DisableLease bool
	// FileRoot optionally allows upload steps to reference files under this
	// directory. When empty only ExecInput.Files entries are uploadable.
	FileRoot string
	// ActionTimeout bounds one recipe step. Defaults to 15s.
	ActionTimeout time.Duration
	// NavigateTimeout bounds one navigation. Defaults to 30s.
	NavigateTimeout time.Duration
	// LeaseTTL is the operation-lease lifetime. Defaults to 5m.
	LeaseTTL time.Duration
	// Sessions enables shared lease/risk state with Worker. Optional for tests.
	Sessions *SessionManager
	// DriverFactory replaces the CDP driver (tests, or an alternative engine).
	DriverFactory DriverFactory
}

// CloudConfig is the team-edition factory input. It is validated by the
// enterprise implementation, not here.
type CloudConfig struct {
	Endpoint      string
	TenantID      string
	EgressProxy   string
	ReceiptBucket string
}

// BackendFactory builds a backend for one kind. Enterprise registers the
// "cloud" factory at startup.
type BackendFactory func(cfg BackendConfig) (BrowserBackend, error)

var (
	backendFactoriesMu sync.RWMutex
	backendFactories   = map[string]BackendFactory{}
)

// RegisterBackendFactory installs (or replaces) the factory for a backend
// kind. It is the injection point for the enterprise CloudBackend: register
// "cloud" before calling NewBackend.
func RegisterBackendFactory(kind string, f BackendFactory) error {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "" {
		return fmt.Errorf("opsbot/browser: backend kind is empty")
	}
	if f == nil {
		return fmt.Errorf("opsbot/browser: backend factory for %q is nil", kind)
	}
	backendFactoriesMu.Lock()
	backendFactories[kind] = f
	backendFactoriesMu.Unlock()
	return nil
}

func unregisterBackendFactory(kind string) {
	backendFactoriesMu.Lock()
	delete(backendFactories, kind)
	backendFactoriesMu.Unlock()
}

func lookupBackendFactory(kind string) BackendFactory {
	backendFactoriesMu.RLock()
	defer backendFactoriesMu.RUnlock()
	return backendFactories[kind]
}

// NewBackend builds the backend selected by cfg.Kind. A registered factory
// wins over the built-in defaults, which is how enterprise injects cloud
// execution.
//
// A cloud backend that is not configured returns a typed UnsupportedBackend
// *and* a *ConfigError. Ignoring the error cannot silently degrade to local:
// every method on the returned backend fails with ErrBackendNotConfigured.
func NewBackend(cfg BackendConfig) (BrowserBackend, error) {
	kind := strings.ToLower(strings.TrimSpace(cfg.Kind))
	if kind == "" {
		return nil, &ConfigError{
			Kind:   kind,
			Reason: "backend kind is empty",
			Hint:   `set Kind to "local" (personal edition) or register a cloud factory for the team edition`,
		}
	}
	if f := lookupBackendFactory(kind); f != nil {
		return f(cfg)
	}
	switch kind {
	case BackendLocal:
		return NewLocalBackend(cfg.Local)
	case BackendCloud:
		reason := "no cloud backend factory is registered"
		if cfg.Cloud.Endpoint == "" {
			reason = "cloud endpoint is empty and no cloud backend factory is registered"
		}
		ub := &UnsupportedBackend{
			Kind:   BackendCloud,
			Reason: reason,
			Hint:   `the team edition must call RegisterBackendFactory("cloud", ...) with a CloudBackend implementation`,
		}
		return ub, &ConfigError{Kind: BackendCloud, Reason: reason, Hint: ub.Hint}
	default:
		return nil, &ConfigError{
			Kind:   kind,
			Reason: "unknown backend kind",
			Hint:   `use "local" or "cloud"`,
		}
	}
}

// UnsupportedBackend is returned for a backend kind that this edition cannot
// run. Every operation fails loudly; there is no implicit fallback.
type UnsupportedBackend struct {
	Kind   string
	Reason string
	Hint   string
}

func (u *UnsupportedBackend) Err() error {
	e := &ConfigError{Kind: u.Kind, Reason: u.Reason, Hint: u.Hint}
	return errors.Join(ErrBackendUnsupported, e)
}

func (u *UnsupportedBackend) Start(context.Context, SessionSpec) (SessionHandle, error) {
	return SessionHandle{}, u.Err()
}

func (u *UnsupportedBackend) Stop(context.Context, SessionHandle) error { return u.Err() }

func (u *UnsupportedBackend) Exec(context.Context, SessionHandle, packs.Recipe, ExecInput) (ExecOutput, error) {
	return ExecOutput{}, u.Err()
}

func (u *UnsupportedBackend) Health(context.Context, SessionHandle) (Health, error) {
	return Health{}, u.Err()
}

// HostAllowed reports whether host is permitted by allowed. Rules are
// case-insensitive and ignore ports: exact hosts, "*.<domain>" wildcards
// (which also match the apex), and "*" (deny-by-default opt-out).
func HostAllowed(host string, allowed []string) bool {
	host = normalizeHost(host)
	if host == "" {
		return false
	}
	for _, rule := range allowed {
		rule = normalizeHost(rule)
		if rule == "" {
			continue
		}
		if rule == "*" || rule == host {
			return true
		}
		if strings.HasPrefix(rule, "*.") {
			base := strings.TrimPrefix(rule, "*.")
			if host == base || strings.HasSuffix(host, "."+base) {
				return true
			}
		}
	}
	return false
}

// validateAllowedHosts rejects malformed rules up front so a config typo is
// diagnosable instead of silently denying navigation.
func validateAllowedHosts(hosts []string) error {
	for _, rule := range hosts {
		r := strings.ToLower(strings.TrimSpace(rule))
		if r == "" || r == "*" {
			continue
		}
		if strings.HasPrefix(r, "*.") {
			r = strings.TrimPrefix(r, "*.")
		}
		if strings.ContainsAny(r, "/@") || strings.Contains(r, "://") {
			return fmt.Errorf("opsbot/browser: invalid allowed host %q", rule)
		}
	}
	return nil
}

func normalizeHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	host = strings.TrimSuffix(host, ".")
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.Trim(host, "[]")
}

// checkNavigation validates a goto target against the allowlist. Only http(s)
// URLs without embedded credentials are permitted.
func checkNavigation(raw string, allowed []string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errInvalidRecipe, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("%w: scheme %q is not allowed", errInvalidRecipe, u.Scheme)
	}
	if u.User != nil {
		return nil, fmt.Errorf("%w: URLs with credentials are not allowed", errInvalidRecipe)
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("%w: URL has no host", errInvalidRecipe)
	}
	if !HostAllowed(u.Hostname(), allowed) {
		return nil, fmt.Errorf("%w: %s", ErrHostNotAllowed, u.Hostname())
	}
	return u, nil
}
