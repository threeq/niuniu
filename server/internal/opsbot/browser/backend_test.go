package browser

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/niuniu-dev/niuniu/internal/opsbot/packs"
)

// fakeDriver is an in-memory Driver. Tests declare which selectors exist and
// what they contain; every invocation is recorded for assertions.
type fakeDriver struct {
	mu         sync.Mutex
	url        string
	urlErr     error
	navErr     error
	counts     map[string]int
	texts      map[string]string
	attrs      map[string]map[string]string
	failOp     map[string]error
	clicks     []string
	fills      map[string]string
	uploads    map[string][]string
	gotos      []string
	shot       []byte
	closed     bool
	profileDir string
	redirect   string
}

func newFakeDriver() *fakeDriver {
	return &fakeDriver{
		counts:  map[string]int{},
		texts:   map[string]string{},
		attrs:   map[string]map[string]string{},
		failOp:  map[string]error{},
		fills:   map[string]string{},
		uploads: map[string][]string{},
	}
}

func (f *fakeDriver) fail(op, selector string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.failOp[op+":"+selector]
}

func (f *fakeDriver) Goto(_ context.Context, url string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.navErr != nil {
		return f.navErr
	}
	f.gotos = append(f.gotos, url)
	f.url = url
	if f.redirect != "" {
		f.url = f.redirect
	}
	return nil
}

func (f *fakeDriver) CurrentURL(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.url, f.urlErr
}

func (f *fakeDriver) Count(_ context.Context, selector string) (int, error) {
	if err := f.fail("count", selector); err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counts[selector], nil
}

func (f *fakeDriver) Wait(ctx context.Context, target Target, _ time.Duration) error {
	if err := f.fail("wait", target.Selector); err != nil {
		return err
	}
	n, err := f.Count(ctx, target.Selector)
	if err != nil {
		return err
	}
	if n <= target.Index {
		return fmt.Errorf("%w: %s[%d]", ErrSelectorNotFound, target.Selector, target.Index)
	}
	return nil
}

func (f *fakeDriver) Click(ctx context.Context, target Target, timeout time.Duration) error {
	if err := f.Wait(ctx, target, timeout); err != nil {
		return err
	}
	if err := f.fail("click", target.Selector); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clicks = append(f.clicks, fmt.Sprintf("%s#%d", target.Selector, target.Index))
	return nil
}

func (f *fakeDriver) Fill(ctx context.Context, target Target, value string, timeout time.Duration) error {
	if err := f.Wait(ctx, target, timeout); err != nil {
		return err
	}
	if err := f.fail("fill", target.Selector); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fills[target.Selector] = value
	return nil
}

func (f *fakeDriver) Upload(ctx context.Context, target Target, files []string, timeout time.Duration) error {
	if err := f.Wait(ctx, target, timeout); err != nil {
		return err
	}
	if err := f.fail("upload", target.Selector); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uploads[target.Selector] = append([]string(nil), files...)
	return nil
}

func (f *fakeDriver) Extract(_ context.Context, target Target, attr string) (string, error) {
	if err := f.fail("extract", target.Selector); err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if n, ok := f.counts[target.Selector]; ok && n <= target.Index {
		return "", fmt.Errorf("%w: %s[%d]", ErrSelectorNotFound, target.Selector, target.Index)
	}
	if attr != "" {
		if m := f.attrs[target.Selector]; m != nil {
			return m[attr], nil
		}
		return "", nil
	}
	if text, ok := f.texts[target.Selector]; ok {
		return text, nil
	}
	return "text:" + target.Selector, nil
}

func (f *fakeDriver) Screenshot(context.Context) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.shot) == 0 {
		return []byte("png-bytes"), nil
	}
	return append([]byte(nil), f.shot...), nil
}

func (f *fakeDriver) Close(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeDriver) clicked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.clicks...)
}

func newFakeBackend(t *testing.T, tune func(*LocalConfig)) (*LocalBackend, *fakeDriver) {
	t.Helper()
	drv := newFakeDriver()
	cfg := LocalConfig{
		DataDir:         t.TempDir(),
		EvidenceDir:     t.TempDir(),
		ActionTimeout:   time.Second,
		NavigateTimeout: time.Second,
		DriverFactory: func(_ context.Context, profileDir string, _ LocalConfig) (Driver, error) {
			drv.mu.Lock()
			drv.profileDir = profileDir
			drv.mu.Unlock()
			return drv, nil
		},
	}
	if tune != nil {
		tune(&cfg)
	}
	b, err := NewLocalBackend(cfg)
	require.NoError(t, err)
	return b, drv
}

func localSpec(t *testing.T, b *LocalBackend, botID int64) SessionHandle {
	t.Helper()
	h, err := b.Start(context.Background(), SessionSpec{
		BotID:        botID,
		Backend:      BackendLocal,
		AllowedHosts: []string{"creator.example.com"},
	})
	require.NoError(t, err)
	return h
}

func publishRecipe() packs.Recipe {
	return packs.Recipe{Name: "publish", Steps: []packs.RecipeStep{
		{Action: "goto", Value: "https://creator.example.com/publish"},
		{Action: "fill", Selector: "#missing", Fallback: []string{"textarea[placeholder]"}, Value: "{{title}}"},
		{Action: "click", Selector: "button.submit"},
		{Action: "assert", Selector: ".success", Args: map[string]string{"contains": "发布成功"}},
		{Action: "assert", Selector: ".error", Args: map[string]string{"when": "absent"}},
		{Action: "extract", Selector: ".note-url", Args: map[string]string{"as": "url"}},
		{Action: "loop", Selector: ".item", Args: map[string]string{"max": "2"}},
		{Action: "click"},
		{Action: "screenshot"},
	}}
}

func TestBackendLocalExecutesFullRecipe(t *testing.T) {
	b, drv := newFakeBackend(t, nil)
	drv.counts = map[string]int{
		"button.submit": 1, ".success": 1, ".error": 0, ".note-url": 1, ".item": 2,
		"textarea[placeholder]": 1,
	}
	drv.texts = map[string]string{
		".success":  "发布成功",
		".note-url": "https://creator.example.com/note/1",
	}
	h := localSpec(t, b, 1)

	out, err := b.Exec(context.Background(), h, publishRecipe(), ExecInput{
		Values: map[string]string{"title": "标题"},
	})
	require.NoError(t, err)

	require.Equal(t, "https://creator.example.com/note/1", out.Values["url"])
	require.Equal(t, "标题", out.Values["title"])
	require.Equal(t, "标题", drv.fills["textarea[placeholder]"])
	require.Equal(t, []string{"button.submit#0", ".item#0", ".item#1"}, drv.clicked())
	require.Equal(t, []string{"https://creator.example.com/publish"}, drv.gotos)
	require.NotEmpty(t, out.Screenshot)
	require.FileExists(t, out.ScreenshotPath)
	require.True(t, strings.HasSuffix(out.ScreenshotPath, ".png"))
}

func TestBackendLocalBranchSkipsSteps(t *testing.T) {
	b, drv := newFakeBackend(t, nil)
	drv.counts = map[string]int{"#a": 1, "#b": 1, ".ads": 0}
	h := localSpec(t, b, 1)

	_, err := b.Exec(context.Background(), h, packs.Recipe{Name: "branch", Steps: []packs.RecipeStep{
		{Action: "branch", Selector: ".ads", Args: map[string]string{"when": "present", "skip": "1"}},
		{Action: "click", Selector: "#a"},
		{Action: "click", Selector: "#b"},
	}}, ExecInput{})
	require.NoError(t, err)
	require.Equal(t, []string{"#b#0"}, drv.clicked())
}

func TestBackendLocalUploadUsesDeclaredFilesOnly(t *testing.T) {
	b, drv := newFakeBackend(t, nil)
	drv.counts = map[string]int{"input[type=file]": 1}
	h := localSpec(t, b, 1)

	dir := t.TempDir()
	cover := filepath.Join(dir, "cover.png")
	require.NoError(t, os.WriteFile(cover, []byte("img"), 0o600))
	stranger := filepath.Join(dir, "stranger.png")
	require.NoError(t, os.WriteFile(stranger, []byte("img"), 0o600))

	recipe := packs.Recipe{Name: "upload", Steps: []packs.RecipeStep{
		{Action: "upload", Selector: "input[type=file]", Value: "{{files.cover}}"},
	}}
	_, err := b.Exec(context.Background(), h, recipe, ExecInput{
		Files: map[string]string{"cover": cover},
	})
	require.NoError(t, err)
	require.Equal(t, []string{cover}, drv.uploads["input[type=file]"])

	_, err = b.Exec(context.Background(), h, packs.Recipe{Name: "upload2", Steps: []packs.RecipeStep{
		{Action: "upload", Selector: "input[type=file]", Value: stranger},
	}}, ExecInput{Files: map[string]string{"cover": cover}})
	require.ErrorIs(t, err, ErrFileNotAllowed)
}

func TestBackendLocalRejectsForeignHostAndScheme(t *testing.T) {
	b, drv := newFakeBackend(t, nil)
	h := localSpec(t, b, 1)

	_, err := b.Exec(context.Background(), h, packs.Recipe{Name: "evil", Steps: []packs.RecipeStep{
		{Action: "goto", Value: "https://evil.example/steal"},
	}}, ExecInput{})
	require.ErrorIs(t, err, ErrHostNotAllowed)
	require.Empty(t, drv.gotos)

	_, err = b.Exec(context.Background(), h, packs.Recipe{Name: "js", Steps: []packs.RecipeStep{
		{Action: "goto", Value: "javascript:alert(1)"},
	}}, ExecInput{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "scheme")

	_, err = b.Exec(context.Background(), h, packs.Recipe{Name: "creds", Steps: []packs.RecipeStep{
		{Action: "goto", Value: "https://user:pass@creator.example.com/x"},
	}}, ExecInput{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "credentials")

	empty, _ := newFakeBackend(t, nil)
	h2, err := empty.Start(context.Background(), SessionSpec{BotID: 2, Backend: BackendLocal})
	require.NoError(t, err)
	_, err = empty.Exec(context.Background(), h2, packs.Recipe{Name: "deny", Steps: []packs.RecipeStep{
		{Action: "goto", Value: "https://creator.example.com/x"},
	}}, ExecInput{})
	require.ErrorIs(t, err, ErrHostNotAllowed)

	// A redirect after the allowlist pre-check must be caught too.
	redirecting, rdrv := newFakeBackend(t, nil)
	rdrv.redirect = "https://evil.example/landing"
	h3 := localSpec(t, redirecting, 3)
	_, err = redirecting.Exec(context.Background(), h3, packs.Recipe{Name: "redirect", Steps: []packs.RecipeStep{
		{Action: "goto", Value: "https://creator.example.com/publish"},
	}}, ExecInput{})
	require.ErrorIs(t, err, ErrHostNotAllowed)
	require.Contains(t, err.Error(), "redirected")
}

func TestBackendLocalRejectsUnknownActionsAndBadRecipes(t *testing.T) {
	b, _ := newFakeBackend(t, nil)
	h := localSpec(t, b, 1)

	_, err := b.Exec(context.Background(), h, packs.Recipe{Name: "empty"}, ExecInput{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "no steps")

	_, err = b.Exec(context.Background(), h, packs.Recipe{Name: "eval", Steps: []packs.RecipeStep{
		{Action: "eval", Value: "alert(1)"},
	}}, ExecInput{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown action")

	_, err = b.Exec(context.Background(), h, packs.Recipe{Name: "tmpl", Steps: []packs.RecipeStep{
		{Action: "fill", Selector: "#title", Value: "{{missing}}"},
	}}, ExecInput{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown template key")
}

func TestBackendLocalClassifiesLoginAndRisk(t *testing.T) {
	t.Run("login redirect", func(t *testing.T) {
		b, drv := newFakeBackend(t, nil)
		drv.url = "https://passport.example.com/login"
		drv.counts = map[string]int{"#submit": 1}
		drv.failOp["click:#submit"] = ErrSelectorNotFound
		h := localSpec(t, b, 1)
		_, err := b.Exec(context.Background(), h, packs.Recipe{Name: "publish", Steps: []packs.RecipeStep{
			{Action: "click", Selector: "#submit"},
		}}, ExecInput{})
		require.ErrorIs(t, err, ErrLoginExpired)
	})

	t.Run("throttled banner", func(t *testing.T) {
		b, drv := newFakeBackend(t, nil)
		drv.url = "https://creator.example.com/publish"
		drv.counts = map[string]int{"#submit": 1}
		drv.texts["body"] = "操作过于频繁，请稍后再试"
		drv.failOp["click:#submit"] = ErrSelectorNotFound
		h := localSpec(t, b, 1)
		_, err := b.Exec(context.Background(), h, packs.Recipe{Name: "publish", Steps: []packs.RecipeStep{
			{Action: "click", Selector: "#submit"},
		}}, ExecInput{})
		require.ErrorIs(t, err, ErrRateLimited)
	})

	t.Run("banned banner", func(t *testing.T) {
		b, drv := newFakeBackend(t, nil)
		drv.url = "https://creator.example.com/publish"
		drv.counts = map[string]int{"#submit": 1}
		drv.texts["body"] = "账号已被封禁"
		drv.failOp["click:#submit"] = ErrSelectorNotFound
		h := localSpec(t, b, 1)
		_, err := b.Exec(context.Background(), h, packs.Recipe{Name: "publish", Steps: []packs.RecipeStep{
			{Action: "click", Selector: "#submit"},
		}}, ExecInput{})
		require.ErrorIs(t, err, ErrBlocked)
	})

	t.Run("selector breakage", func(t *testing.T) {
		b, drv := newFakeBackend(t, nil)
		drv.url = "https://creator.example.com/publish"
		drv.counts = map[string]int{"#submit": 0}
		h := localSpec(t, b, 1)
		_, err := b.Exec(context.Background(), h, packs.Recipe{Name: "publish", Steps: []packs.RecipeStep{
			{Action: "click", Selector: "#submit"},
		}}, ExecInput{})
		require.ErrorIs(t, err, ErrRecipeBroken)
	})
}

func TestBackendLocalSharesLeaseAndRiskState(t *testing.T) {
	w, _ := newTestWorker(t)
	drv := newFakeDriver()
	b, err := NewLocalBackend(LocalConfig{
		DataDir:       t.TempDir(),
		Sessions:      w.Sessions,
		ActionTimeout: time.Second,
		DriverFactory: func(_ context.Context, profileDir string, _ LocalConfig) (Driver, error) {
			drv.profileDir = profileDir
			return drv, nil
		},
	})
	require.NoError(t, err)
	ctx := context.Background()
	h, err := b.Start(ctx, SessionSpec{BotID: 1, Backend: BackendLocal, AllowedHosts: []string{"creator.example.com"}})
	require.NoError(t, err)

	sess, err := w.Sessions.State(ctx, 1, BackendLocal)
	require.NoError(t, err)
	require.DirExists(t, sess.ProfileDir)
	require.Equal(t, sess.ProfileDir, h.ProfileDir)

	// A Worker-owned lease blocks the backend (same account, no concurrency).
	require.NoError(t, w.Sessions.Acquire(ctx, 1, BackendLocal, "worker-a", time.Minute))
	_, err = b.Exec(ctx, h, packs.Recipe{Name: "x", Steps: []packs.RecipeStep{
		{Action: "goto", Value: "https://creator.example.com/"},
	}}, ExecInput{})
	require.ErrorIs(t, err, ErrLeaseBusy)
	require.NoError(t, w.Sessions.Release(ctx, 1, BackendLocal, "worker-a"))

	// Risk state blocks automatic work before any recipe runs.
	blocked := RiskBlocked
	_, err = w.Sessions.Update(ctx, 1, BackendLocal, StatePatch{RiskState: &blocked})
	require.NoError(t, err)
	_, err = b.Exec(ctx, h, packs.Recipe{Name: "x", Steps: []packs.RecipeStep{
		{Action: "goto", Value: "https://creator.example.com/"},
	}}, ExecInput{})
	require.ErrorIs(t, err, ErrBlocked)

	normal := RiskNormal
	_, err = w.Sessions.Update(ctx, 1, BackendLocal, StatePatch{RiskState: &normal})
	require.NoError(t, err)
	_, err = b.Exec(ctx, h, packs.Recipe{Name: "x", Steps: []packs.RecipeStep{
		{Action: "goto", Value: "https://creator.example.com/"},
	}}, ExecInput{})
	require.NoError(t, err)

	// A successful Exec releases its lease, so another owner can take it.
	require.NoError(t, w.Sessions.Acquire(ctx, 1, BackendLocal, "worker-b", time.Minute))
	require.NoError(t, w.Sessions.Release(ctx, 1, BackendLocal, "worker-b"))
}

func TestBackendLocalProfileIsolation(t *testing.T) {
	b, _ := newFakeBackend(t, nil)
	ctx := context.Background()
	shared := filepath.Join(t.TempDir(), "profile")

	h1, err := b.Start(ctx, SessionSpec{BotID: 1, Backend: BackendLocal, ProfileDir: shared})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(h1.ProfileDir, filepath.Clean(shared)))

	_, err = b.Start(ctx, SessionSpec{BotID: 1, Backend: BackendLocal, ProfileDir: shared})
	require.ErrorIs(t, err, ErrProfileShared, "one account cannot open two sessions on one profile")

	_, err = b.Start(ctx, SessionSpec{BotID: 2, Backend: BackendLocal, ProfileDir: shared})
	require.ErrorIs(t, err, ErrProfileShared)

	h2, err := b.Start(ctx, SessionSpec{BotID: 2, Backend: BackendLocal})
	require.NoError(t, err)
	require.NotEqual(t, h1.ProfileDir, h2.ProfileDir)
	require.DirExists(t, h1.ProfileDir)
	require.DirExists(t, h2.ProfileDir)

	require.NoError(t, b.Stop(ctx, h1))
	_, err = b.Start(ctx, SessionSpec{BotID: 1, Backend: BackendLocal, ProfileDir: shared})
	require.NoError(t, err, "stopping a session releases its profile reservation")
}

func TestBackendLocalStopAndHealth(t *testing.T) {
	b, drv := newFakeBackend(t, nil)
	ctx := context.Background()
	h := localSpec(t, b, 1)

	drv.url = "https://creator.example.com/home"
	health, err := b.Health(ctx, h)
	require.NoError(t, err)
	require.Equal(t, HealthHealthy, health.State)
	require.Equal(t, "https://creator.example.com/home", health.URL)

	drv.url = "https://evil.example/"
	health, err = b.Health(ctx, h)
	require.NoError(t, err)
	require.Equal(t, HealthDegraded, health.State)
	require.Contains(t, health.Detail, "allowlist")

	drv.urlErr = errors.New("page gone")
	health, err = b.Health(ctx, h)
	require.NoError(t, err)
	require.Equal(t, HealthDown, health.State)
	drv.urlErr = nil

	require.NoError(t, b.Stop(ctx, h))
	require.True(t, drv.closed)
	require.NoError(t, b.Stop(ctx, h), "stop is idempotent")

	_, err = b.Exec(ctx, h, publishRecipe(), ExecInput{})
	require.ErrorIs(t, err, ErrSessionClosed)
	_, err = b.Health(ctx, h)
	require.ErrorIs(t, err, ErrSessionClosed)

	err = b.Stop(ctx, SessionHandle{ID: "nope"})
	require.ErrorIs(t, err, ErrUnknownHandle)
}

func TestBackendLocalExtractsReceipt(t *testing.T) {
	b, drv := newFakeBackend(t, nil)
	drv.counts = map[string]int{".post-id": 1}
	drv.texts = map[string]string{".post-id": "post-42"}
	h := localSpec(t, b, 1)

	out, err := b.Exec(context.Background(), h, packs.Recipe{Name: "publish", Steps: []packs.RecipeStep{
		{Action: "extract", Selector: ".post-id", Args: map[string]string{"as": "receipt"}},
	}}, ExecInput{})
	require.NoError(t, err)
	require.Equal(t, "post-42", out.Receipt)
	require.Equal(t, "post-42", out.Values["receipt"])
}

type fakeCloudBackend struct{}

func (f *fakeCloudBackend) Start(context.Context, SessionSpec) (SessionHandle, error) {
	return SessionHandle{ID: "cloud-1", Backend: BackendCloud, TenantID: "tenant-1"}, nil
}

func (f *fakeCloudBackend) Stop(context.Context, SessionHandle) error { return nil }

func (f *fakeCloudBackend) Exec(context.Context, SessionHandle, packs.Recipe, ExecInput) (ExecOutput, error) {
	return ExecOutput{Values: map[string]string{}}, nil
}

func (f *fakeCloudBackend) Health(context.Context, SessionHandle) (Health, error) {
	return Health{State: HealthHealthy}, nil
}

func (f *fakeCloudBackend) Container(context.Context, SessionHandle) (ContainerInfo, error) {
	return ContainerInfo{
		ContainerID:   "container-1",
		TenantID:      "tenant-1",
		EgressHosts:   []string{"creator.example.com"},
		ReceiptPrefix: "s3://receipts/tenant-1/1/",
	}, nil
}

func TestBackendFactoryLocal(t *testing.T) {
	drv := newFakeDriver()
	b, err := NewBackend(BackendConfig{Kind: BackendLocal, Local: LocalConfig{
		DataDir: t.TempDir(),
		DriverFactory: func(context.Context, string, LocalConfig) (Driver, error) {
			return drv, nil
		},
	}})
	require.NoError(t, err)
	require.IsType(t, &LocalBackend{}, b)
}

func TestBackendFactoryRejectsBadConfig(t *testing.T) {
	var cfgErr *ConfigError

	_, err := NewBackend(BackendConfig{})
	require.ErrorAs(t, err, &cfgErr)
	require.Contains(t, cfgErr.Hint, "local")

	_, err = NewBackend(BackendConfig{Kind: BackendLocal})
	require.ErrorAs(t, err, &cfgErr)
	require.Contains(t, err.Error(), "DataDir")

	_, err = NewBackend(BackendConfig{Kind: "weird"})
	require.ErrorAs(t, err, &cfgErr)
	require.Contains(t, err.Error(), "unknown backend kind")

	require.Error(t, RegisterBackendFactory("", func(BackendConfig) (BrowserBackend, error) { return nil, nil }))
	require.Error(t, RegisterBackendFactory("x", nil))
}

func TestBackendFactoryCloudNotConfigured(t *testing.T) {
	b, err := NewBackend(BackendConfig{Kind: BackendCloud, Cloud: CloudConfig{Endpoint: "https://cloud.example"}})
	require.Error(t, err)
	var cfgErr *ConfigError
	require.ErrorAs(t, err, &cfgErr)
	require.Contains(t, err.Error(), `RegisterBackendFactory("cloud"`)

	ub, ok := b.(*UnsupportedBackend)
	require.True(t, ok, "factory must return a typed unsupported backend, never a silent local fallback")
	require.Equal(t, BackendCloud, ub.Kind)

	ctx := context.Background()
	_, err = b.Start(ctx, SessionSpec{BotID: 1, Backend: BackendCloud})
	require.ErrorIs(t, err, ErrBackendNotConfigured)
	require.ErrorIs(t, err, ErrBackendUnsupported)

	_, err = b.Exec(ctx, SessionHandle{ID: "x"}, packs.Recipe{}, ExecInput{})
	require.ErrorIs(t, err, ErrBackendNotConfigured)

	_, err = b.Health(ctx, SessionHandle{ID: "x"})
	require.ErrorIs(t, err, ErrBackendNotConfigured)
}

func TestBackendCloudFactoryInjection(t *testing.T) {
	fake := &fakeCloudBackend{}
	require.NoError(t, RegisterBackendFactory(BackendCloud, func(BackendConfig) (BrowserBackend, error) {
		return fake, nil
	}))
	t.Cleanup(func() { unregisterBackendFactory(BackendCloud) })

	b, err := NewBackend(BackendConfig{Kind: BackendCloud, Cloud: CloudConfig{Endpoint: "https://cloud.example"}})
	require.NoError(t, err)
	require.Same(t, fake, b)

	cb, ok := b.(CloudBackend)
	require.True(t, ok, "the injected backend must satisfy the CloudBackend contract")
	info, err := cb.Container(context.Background(), SessionHandle{ID: "cloud-1"})
	require.NoError(t, err)
	require.Equal(t, "tenant-1", info.TenantID)
	require.Equal(t, []string{"creator.example.com"}, info.EgressHosts)
	require.NotEmpty(t, info.ReceiptPrefix)
}

func TestBackendSharedHostAllowlist(t *testing.T) {
	rules := []string{"creator.example.com", "*.trusted.test", "127.0.0.1", "localhost"}
	require.True(t, HostAllowed("creator.example.com", rules))
	require.True(t, HostAllowed("CREATOR.EXAMPLE.COM:443", rules))
	require.True(t, HostAllowed("cdn.trusted.test", rules))
	require.True(t, HostAllowed("trusted.test", rules))
	require.True(t, HostAllowed("127.0.0.1", rules))
	require.True(t, HostAllowed("localhost:3000", rules))
	require.False(t, HostAllowed("evil.example", rules))
	require.False(t, HostAllowed("trusted.test.evil.example", rules))
	require.False(t, HostAllowed("creator.example.com", nil))
	require.True(t, HostAllowed("anything.test", []string{"*"}))
}

func TestBackendLocalCDPChrome(t *testing.T) {
	if os.Getenv("NIUNIU_OPSBOT_CHROME_TEST") == "" {
		t.Skip("set NIUNIU_OPSBOT_CHROME_TEST=1 to exercise the real CDP driver")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><html><body>
			<input id="title">
			<button id="submit" onclick="
				document.getElementById('out').textContent =
					'saved:' + document.getElementById('title').value;
				document.getElementById('title').value = '';">go</button>
			<div id="out">idle</div>
		</body></html>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	parsed, err := url.Parse(srv.URL)
	require.NoError(t, err)

	b, err := NewLocalBackend(LocalConfig{
		DataDir:         t.TempDir(),
		EvidenceDir:     t.TempDir(),
		Headless:        true,
		ActionTimeout:   5 * time.Second,
		NavigateTimeout: 20 * time.Second,
	})
	require.NoError(t, err)
	ctx := context.Background()
	h, err := b.Start(ctx, SessionSpec{BotID: 1, Backend: BackendLocal, AllowedHosts: []string{parsed.Hostname()}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Stop(context.Background(), h) })

	out, err := b.Exec(ctx, h, packs.Recipe{Name: "publish", Steps: []packs.RecipeStep{
		{Action: "goto", Value: srv.URL},
		{Action: "wait", Selector: "#title"},
		{Action: "fill", Selector: "#title", Value: "标题"},
		{Action: "click", Selector: "#submit"},
		{Action: "assert", Selector: "#out", Args: map[string]string{"contains": "saved:标题"}},
		{Action: "extract", Selector: "#out", Args: map[string]string{"as": "result"}},
		{Action: "screenshot"},
	}}, ExecInput{})
	require.NoError(t, err)
	require.Equal(t, "saved:标题", out.Values["result"])
	require.NotEmpty(t, out.Screenshot)
	require.FileExists(t, out.ScreenshotPath)
}
