package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/niuniu-dev/niuniu/internal/api"
	"github.com/niuniu-dev/niuniu/internal/opsbot"
	"github.com/niuniu-dev/niuniu/internal/opsbot/browser"
	"github.com/niuniu-dev/niuniu/internal/opsbot/channel"
)

type apiFakeBrowserAdapter struct {
	hasLogin  bool
	loginWait time.Duration
	loginErr  error
}

func (a *apiFakeBrowserAdapter) Capabilities() channel.CapabilitySet {
	return channel.CapabilitySet{channel.CapPublish: true}
}

func (a *apiFakeBrowserAdapter) EnsureSession(ctx context.Context, ref string) error {
	_, err := a.Login(ctx, ref)
	return err
}

func (a *apiFakeBrowserAdapter) Publish(context.Context, any) (channel.Receipt, error) {
	return channel.Receipt{}, nil
}

func (a *apiFakeBrowserAdapter) FetchMetrics(context.Context, string, string) (map[string]float64, error) {
	return nil, nil
}

func (a *apiFakeBrowserAdapter) ListEngagements(context.Context, string) ([]channel.Engagement, error) {
	return nil, nil
}

func (a *apiFakeBrowserAdapter) Reply(context.Context, string, string) (channel.Receipt, error) {
	return channel.Receipt{}, nil
}

func (a *apiFakeBrowserAdapter) HealthCheck(context.Context) error { return nil }

func (a *apiFakeBrowserAdapter) HasLoginRecipe() bool { return a.hasLogin }

func (a *apiFakeBrowserAdapter) Login(ctx context.Context, _ string) (browser.Result, error) {
	if a.loginWait > 0 {
		select {
		case <-time.After(a.loginWait):
		case <-ctx.Done():
			return browser.Result{}, ctx.Err()
		}
	}
	return browser.Result{EvidencePath: "/tmp/qr.png"}, a.loginErr
}

func (a *apiFakeBrowserAdapter) VerifySession(context.Context, string) error { return a.loginErr }

func setupOpsBrowser(t *testing.T, adapter channel.ChannelAdapter) (*gin.Engine, string, *opsbot.LoginManager) {
	t.Helper()
	var (
		dataDir string
		login   *opsbot.LoginManager
	)
	r, _ := setupOpsBotWith(t, func(h *api.OpsBotHandler) {
		dataDir = h.DataDir
		sessions := browser.NewSessionManager(h.Queries, h.DataDir)
		login = opsbot.NewLoginManager(func(context.Context, int64) (channel.ChannelAdapter, error) {
			return adapter, nil
		}, h.Queries, sessions)
		login.LoginTimeout = 3 * time.Second
		login.VerifyTimeout = 200 * time.Millisecond
		h.Login = login
		h.Sessions = sessions
	})
	return r, dataDir, login
}

type browserStatusBody struct {
	BotID          int64  `json:"bot_id"`
	State          string `json:"state"`
	LoginStatus    string `json:"login_status"`
	Health         string `json:"health"`
	RiskState      string `json:"risk_state"`
	HasLoginRecipe bool   `json:"has_login_recipe"`
	NextAction     string `json:"next_action"`
	LastError      string `json:"last_error"`
	ProfileDir     string `json:"profile_dir"`
	EvidenceURL    string `json:"evidence_url"`
}

func getBrowserStatus(t *testing.T, r *gin.Engine, id int64) browserStatusBody {
	t.Helper()
	w := doJSON(t, r, http.MethodGet, "/api/ops/bots/"+strconv.FormatInt(id, 10)+"/browser", "1", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var body browserStatusBody
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	return body
}

func TestBrowserStatusIsMachineReadable(t *testing.T) {
	adapter := &apiFakeBrowserAdapter{hasLogin: true}
	r, _, _ := setupOpsBrowser(t, adapter)
	id := createTestBot(t, r, "1", "brand")

	body := getBrowserStatus(t, r, id)
	require.Equal(t, id, body.BotID)
	require.Equal(t, "idle", body.State)
	require.Equal(t, "unknown", body.LoginStatus)
	require.Equal(t, "unknown", body.Health)
	require.Equal(t, "normal", body.RiskState)
	require.True(t, body.HasLoginRecipe)
	require.Equal(t, opsbot.LoginActionLogin, body.NextAction)
	require.NotEmpty(t, body.ProfileDir)
	require.NotEmpty(t, body.EvidenceURL)
}

func TestStartLoginReturnsAcceptedAndCompletes(t *testing.T) {
	adapter := &apiFakeBrowserAdapter{hasLogin: true, loginWait: 100 * time.Millisecond}
	r, _, _ := setupOpsBrowser(t, adapter)
	id := createTestBot(t, r, "1", "brand")

	w := doJSON(t, r, http.MethodPost, "/api/ops/bots/"+strconv.FormatInt(id, 10)+"/login", "1", "")
	require.Equal(t, http.StatusAccepted, w.Code, w.Body.String())
	var started browserStatusBody
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &started))
	require.Equal(t, "started", started.State)

	deadline := time.Now().Add(3 * time.Second)
	for {
		body := getBrowserStatus(t, r, id)
		if body.State == "succeeded" {
			break
		}
		require.NotEqual(t, "failed", body.State, "login failed unexpectedly")
		if time.Now().After(deadline) {
			t.Fatalf("login did not finish, state = %q", body.State)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStartLoginNotRequired(t *testing.T) {
	adapter := &apiFakeBrowserAdapter{hasLogin: false}
	r, _, _ := setupOpsBrowser(t, adapter)
	id := createTestBot(t, r, "1", "brand")

	w := doJSON(t, r, http.MethodPost, "/api/ops/bots/"+strconv.FormatInt(id, 10)+"/login", "1", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var body browserStatusBody
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, "not_required", body.State)
	require.Equal(t, opsbot.LoginActionNone, body.NextAction)
}

func TestBrowserLoginRejectsCredentialChannel(t *testing.T) {
	adapter := &apiFakeBrowserAdapter{hasLogin: false}
	r, _, _ := setupOpsBrowser(t, adapter)
	id := createTestBot(t, r, "1", "brand")

	w := doJSON(t, r, http.MethodPost, "/api/ops/bots/"+strconv.FormatInt(id, 10)+"/browser-login", "1",
		`{"backend":"local"}`)
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "OPS_LOGIN_NOT_REQUIRED")
}

func TestBrowserLoginReportsProfileAndNextAction(t *testing.T) {
	adapter := &apiFakeBrowserAdapter{hasLogin: true}
	r, _, _ := setupOpsBrowser(t, adapter)
	id := createTestBot(t, r, "1", "brand")

	w := doJSON(t, r, http.MethodPost, "/api/ops/bots/"+strconv.FormatInt(id, 10)+"/browser-login", "1",
		`{"backend":"local"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var body struct {
		ProfileDir     string `json:"profile_dir"`
		HasLoginRecipe bool   `json:"has_login_recipe"`
		NextAction     string `json:"next_action"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.NotEmpty(t, body.ProfileDir)
	require.True(t, body.HasLoginRecipe)
	require.Equal(t, opsbot.LoginActionLogin, body.NextAction)
	require.NotContains(t, w.Body.String(), "next_step")
}

func TestBrowserEvidenceServesNewestScreenshot(t *testing.T) {
	adapter := &apiFakeBrowserAdapter{hasLogin: true}
	r, dataDir, _ := setupOpsBrowser(t, adapter)
	id := createTestBot(t, r, "1", "brand")
	path := "/api/ops/bots/" + strconv.FormatInt(id, 10) + "/browser/evidence"

	w := doJSON(t, r, http.MethodGet, path, "1", "")
	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())

	dir := filepath.Join(dataDir, "ops", "evidence", strconv.FormatInt(id, 10))
	require.NoError(t, os.MkdirAll(dir, 0o700))
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "login-1.png"), png, 0o600))

	w = doJSON(t, r, http.MethodGet, path, "1", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, png, w.Body.Bytes())
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
}

func TestVerifyLoginReportsResult(t *testing.T) {
	adapter := &apiFakeBrowserAdapter{hasLogin: true}
	r, _, _ := setupOpsBrowser(t, adapter)
	id := createTestBot(t, r, "1", "brand")
	path := "/api/ops/bots/" + strconv.FormatInt(id, 10) + "/login/verify"

	w := doJSON(t, r, http.MethodPost, path, "1", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var body browserStatusBody
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, "succeeded", body.State)
	require.Equal(t, opsbot.LoginActionNone, body.NextAction)

	adapter.loginErr = errors.New("session expired")
	w = doJSON(t, r, http.MethodPost, path, "1", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, "failed", body.State)
	require.Equal(t, opsbot.LoginActionRelogin, body.NextAction)
	require.Contains(t, body.LastError, "session expired")
}

func TestBrowserStatusRequiresOwner(t *testing.T) {
	adapter := &apiFakeBrowserAdapter{hasLogin: true}
	r, _, _ := setupOpsBrowser(t, adapter)
	id := createTestBot(t, r, "1", "brand")

	w := doJSON(t, r, http.MethodGet, "/api/ops/bots/"+strconv.FormatInt(id, 10)+"/browser", "2", "")
	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
}
