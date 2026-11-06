// Project-level management API for the intake ci/email/webhook bindings
// (design §10.1). This is the surface the settings UI and the MCP tools drive;
// the inbound delivery paths live in integration_webhook.go.
//
// The generated secret is NEVER returned inline. Create and Rotate write the
// plaintext into a short-lived single-use store and return a link to the page
// that shows it once, so the secret cannot leak into a conversation, a log or a
// mobile client's history.
package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/intake"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// intakeSecretTTL bounds how long a one-time secret link stays redeemable.
const intakeSecretTTL = 15 * time.Minute

// intakeSecretPath is the page a secret link points at. The page fetches the
// sibling API path below, which is what actually consumes the token.
const (
	intakeSecretPagePath = "/intake/sources/secret/"
	intakeSecretAPIPath  = "/api/intake/sources/secret/"
)

type intakeSecretEntry struct {
	Secret      string
	ProjectID   int64
	SourceID    int64
	SourceKind  string
	Name        string
	InboundPath string
	ExpiresAt   time.Time
}

// intakeSecretStore holds plaintext secrets until their link is opened. It is
// in-memory on purpose: a restart invalidates outstanding links, which also
// invalidates the transient plaintext copy.
type intakeSecretStore struct {
	mu      sync.Mutex
	entries map[string]intakeSecretEntry
}

func newIntakeSecretStore() *intakeSecretStore {
	return &intakeSecretStore{entries: map[string]intakeSecretEntry{}}
}

// Put stores the secret under a fresh token and returns the token. Expired
// entries are swept on every write so an unopened link cannot accumulate.
func (s *intakeSecretStore) Put(e intakeSecretEntry, now time.Time) (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	e.ExpiresAt = now.Add(intakeSecretTTL)
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.entries {
		if !v.ExpiresAt.After(now) {
			delete(s.entries, k)
		}
	}
	s.entries[token] = e
	return token, nil
}

// Take redeems a token exactly once. The entry is deleted whatever the outcome,
// so a refresh, a second tab or a replayed link all fail.
func (s *intakeSecretStore) Take(token string, now time.Time) (intakeSecretEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[token]
	if !ok {
		return intakeSecretEntry{}, false
	}
	delete(s.entries, token)
	if !e.ExpiresAt.After(now) {
		return intakeSecretEntry{}, false
	}
	return e, true
}

func (s *intakeSecretStore) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

func (h *IntakeHandler) intakeSourcesWired(c *gin.Context) bool {
	if h.Sources == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "intake sources are not wired"})
		return false
	}
	return true
}

// ListSources serves GET /api/projects/:id/intake/sources.
func (h *IntakeHandler) ListSources(c *gin.Context) {
	pid, ok := h.intakeProjectAccess(c, false)
	if !ok {
		return
	}
	if !h.intakeSourcesWired(c) {
		return
	}
	list, err := h.Sources.List(c.Request.Context(), c.GetInt64("auth_user_id"), pid)
	if err != nil {
		h.mapSourceErr(c, err)
		return
	}
	// source_kind is advisory: an unknown filter matches nothing rather than
	// silently returning every kind.
	if filter := strings.ToLower(strings.TrimSpace(c.Query("source_kind"))); filter != "" {
		filtered := make([]service.IntakeSourceDTO, 0, len(list))
		for _, s := range list {
			if s.SourceKind == filter {
				filtered = append(filtered, s)
			}
		}
		list = filtered
	}
	if list == nil {
		list = []service.IntakeSourceDTO{}
	}
	c.JSON(http.StatusOK, gin.H{"sources": list})
}

type createIntakeSourceBody struct {
	SourceKind   string         `json:"source_kind" binding:"required"`
	Name         string         `json:"name" binding:"required"`
	Mode         string         `json:"mode"`
	Provider     string         `json:"provider"`
	Config       map[string]any `json:"config"`
	CredentialID int64          `json:"credential_id"`
}

// CreateSource serves POST /api/projects/:id/intake/sources. The response is
// {source, secret_link?} — never the plaintext secret. IMAP bindings reference
// an existing credential, so they have no link.
func (h *IntakeHandler) CreateSource(c *gin.Context) {
	pid, ok := h.intakeProjectAccess(c, true)
	if !ok {
		return
	}
	if !h.intakeSourcesWired(c) {
		return
	}
	var body createIntakeSourceBody
	if err := c.ShouldBindJSON(&body); err != nil {
		BadRequest(c, "source_kind and name are required")
		return
	}
	dto, secret, err := h.Sources.Create(c.Request.Context(), service.IntakeSourceInput{
		ProjectID:    pid,
		SourceKind:   body.SourceKind,
		Name:         body.Name,
		Mode:         body.Mode,
		Provider:     body.Provider,
		Config:       body.Config,
		CredentialID: body.CredentialID,
		CallerUserID: c.GetInt64("auth_user_id"),
	})
	if err != nil {
		h.mapSourceErr(c, err)
		return
	}
	resp := gin.H{"source": dto}
	if secret != "" {
		link, err := h.issueSecretLink(*dto, secret)
		if err != nil {
			h.mapSourceErr(c, err)
			return
		}
		resp["secret_link"] = link
	}
	c.JSON(http.StatusCreated, resp)
}

// RotateSource serves POST /api/projects/:id/intake/sources/:sid/rotate.
func (h *IntakeHandler) RotateSource(c *gin.Context) {
	pid, ok := h.intakeProjectAccess(c, true)
	if !ok {
		return
	}
	if !h.intakeSourcesWired(c) {
		return
	}
	sid, ok := intakeSourceID(c)
	if !ok {
		return
	}
	secret, err := h.Sources.RotateSecret(c.Request.Context(), c.GetInt64("auth_user_id"), pid, sid)
	if err != nil {
		h.mapSourceErr(c, err)
		return
	}
	dto, err := h.sourceByID(c, pid, sid)
	if err != nil {
		h.mapSourceErr(c, err)
		return
	}
	link, err := h.issueSecretLink(*dto, secret)
	if err != nil {
		h.mapSourceErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"source": dto, "secret_link": link})
}

// TestSource serves POST /api/projects/:id/intake/sources/:sid/test. It is not
// a write: the synthetic event is normalized and evaluated but never recorded,
// so project members (not just writers) may run it.
func (h *IntakeHandler) TestSource(c *gin.Context) {
	pid, ok := h.intakeProjectAccess(c, false)
	if !ok {
		return
	}
	if !h.intakeSourcesWired(c) {
		return
	}
	sid, ok := intakeSourceID(c)
	if !ok {
		return
	}
	var body struct {
		Sample any `json:"sample"`
	}
	// An empty body is a valid request (the service synthesizes a sample), so a
	// decode failure only matters when bytes were actually sent.
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&body); err != nil {
			BadRequest(c, "invalid sample")
			return
		}
	}
	var sample []byte
	switch v := body.Sample.(type) {
	case nil:
	case string:
		// Free text from the UI's plain sample field is passed through verbatim;
		// the service treats a non-JSON sample as text.
		sample = []byte(strings.TrimSpace(v))
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			BadRequest(c, "invalid sample")
			return
		}
		sample = raw
	}
	res, err := h.Sources.TestDelivery(c.Request.Context(), c.GetInt64("auth_user_id"), pid, sid, sample)
	if err != nil {
		h.mapSourceErr(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

// DeleteSource serves DELETE /api/projects/:id/intake/sources/:sid.
func (h *IntakeHandler) DeleteSource(c *gin.Context) {
	pid, ok := h.intakeProjectAccess(c, true)
	if !ok {
		return
	}
	if !h.intakeSourcesWired(c) {
		return
	}
	sid, ok := intakeSourceID(c)
	if !ok {
		return
	}
	if err := h.Sources.Delete(c.Request.Context(), c.GetInt64("auth_user_id"), pid, sid); err != nil {
		h.mapSourceErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// PollSource serves POST /api/projects/:id/intake/sources/:sid/poll.
func (h *IntakeHandler) PollSource(c *gin.Context) {
	pid, ok := h.intakeProjectAccess(c, true)
	if !ok {
		return
	}
	if !h.intakeSourcesWired(c) {
		return
	}
	sid, ok := intakeSourceID(c)
	if !ok {
		return
	}
	if err := h.Sources.PollNow(c.Request.Context(), c.GetInt64("auth_user_id"), pid, sid); err != nil {
		h.mapSourceErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "polled": true})
}

// ResolveIntakeSecret serves GET /api/intake/sources/secret/:token on the
// PUBLIC surface (it is mounted outside the authenticated group). The token is
// the sole authorization and is consumed on read; the hardened no-store /
// no-referrer headers match the credential-onboarding page.
func (h *IntakeHandler) ResolveIntakeSecret(c *gin.Context) {
	ApplyCredentialPageHeaders(c)
	token := strings.TrimSpace(c.Param("token"))
	if token == "" {
		c.JSON(http.StatusGone, gin.H{"error": "this secret link is invalid, expired or already used"})
		return
	}
	entry, ok := h.secrets.Take(token, time.Now())
	if !ok {
		c.JSON(http.StatusGone, gin.H{"error": "this secret link is invalid, expired or already used"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"secret":       entry.Secret,
		"source_id":    entry.SourceID,
		"source_kind":  entry.SourceKind,
		"name":         entry.Name,
		"inbound_path": entry.InboundPath,
		"expires_at":   entry.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

// issueSecretLink parks the plaintext secret behind a single-use token.
func (h *IntakeHandler) issueSecretLink(dto service.IntakeSourceDTO, secret string) (string, error) {
	token, err := h.secrets.Put(intakeSecretEntry{
		Secret:      secret,
		ProjectID:   dto.ProjectID,
		SourceID:    dto.ID,
		SourceKind:  dto.SourceKind,
		Name:        dto.Name,
		InboundPath: dto.InboundPath,
	}, time.Now())
	if err != nil {
		return "", err
	}
	return intakeSecretPagePath + token, nil
}

// sourceByID reads the binding's display fields for the link page.
func (h *IntakeHandler) sourceByID(c *gin.Context, projectID, sourceID int64) (*service.IntakeSourceDTO, error) {
	list, err := h.Sources.List(c.Request.Context(), c.GetInt64("auth_user_id"), projectID)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].ID == sourceID {
			return &list[i], nil
		}
	}
	return nil, service.ErrIntakeSourceNotFound
}

func intakeSourceID(c *gin.Context) (int64, bool) {
	sid, err := strconv.ParseInt(strings.TrimSpace(c.Param("sid")), 10, 64)
	if err != nil || sid <= 0 {
		BadRequest(c, "invalid source ID")
		return 0, false
	}
	return sid, true
}

// mapSourceErr applies the binding-management status contract: a conflicts on
// duplicate name is 409, a binding outside the project (or a foreign project)
// is 404/403, and bad input is 400.
func (h *IntakeHandler) mapSourceErr(c *gin.Context, err error) {
	var authErr *service.ErrIMAPAuth
	switch {
	case errors.Is(err, service.ErrForbidden):
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
	case errors.Is(err, service.ErrNotFound), errors.Is(err, service.ErrIntakeSourceNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "intake source not found"})
	case errors.Is(err, service.ErrIntakeSourceExists):
		c.JSON(http.StatusConflict, gin.H{"error": "an intake source with this name already exists"})
	case errors.Is(err, service.ErrIntakeSourceNoSecret):
		c.JSON(http.StatusBadRequest, gin.H{"error": "this source has no rotatable secret"})
	case errors.Is(err, service.ErrIntakeSourceEmailPollerOff):
		c.JSON(http.StatusNotImplemented, gin.H{"error": "the email poller is not wired"})
	case errors.Is(err, service.ErrIntakeSourceInvalid),
		errors.Is(err, service.ErrCredentialNotBindable),
		errors.Is(err, service.ErrCredentialNotGranted),
		errors.Is(err, service.ErrCredentialProviderMismatch):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.As(err, &authErr):
		// A failed IMAP LOGIN is a user-correctable credential problem.
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	}
}

// isLegacyWebhookKind is the set the legacy /intake/webhook alias exposes.
func isLegacyWebhookKind(kind string) bool {
	return kind == intake.SourceKindEmail || kind == intake.SourceKindWebhook
}
