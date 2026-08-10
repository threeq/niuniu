package api

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/go-shared/releasecheck"
)

// AppUpdateHandler proxies the latest desktop release from the official website
// changelog (https://www.niu6ai.com/changelog) for the SPA's "check for
// updates" feature.
//
// Why a server-side proxy instead of the SPA fetching GitHub directly:
//   - api.github.com returns HTTP 403 from mainland China (shared-IP rate limit
//     / regional blocking), which is exactly where our personal-edition users
//     are, so the old direct-from-browser GitHub poll was broken for them.
//   - The browser can't fetch the marketing site cross-origin (no CORS header),
//     but the local server can fetch it server-to-server with no CORS in play.
//
// In personal mode the server runs on the user's own machine, so it reaches the
// Aliyun-hosted site fine. Update checks are suppressed entirely in
// team/standalone modes (the SPA gates on personalMode), so this endpoint only
// matters where a reachable local server exists.
type AppUpdateHandler struct {
	baseURL string
	client  *http.Client

	mu        sync.Mutex
	cached    *releasecheck.Release
	cachedErr error
	fetchedAt time.Time
	ttl       time.Duration
}

// NewAppUpdateHandler builds the handler. baseURL is the website origin
// (releasecheck.DefaultBaseURL for production).
func NewAppUpdateHandler(baseURL string) *AppUpdateHandler {
	if baseURL == "" {
		baseURL = releasecheck.DefaultBaseURL
	}
	return &AppUpdateHandler{
		baseURL: baseURL,
		client:  &http.Client{Timeout: 15 * time.Second},
		ttl:     30 * time.Minute,
	}
}

// Latest godoc
//
//	@Summary	Latest desktop release (proxied from the official changelog)
//	@Tags		system
//	@Produce	json
//	@Success	200	{object}	releasecheck.Release
//	@Router		/app-update/latest [get]
func (h *AppUpdateHandler) Latest(c *gin.Context) {
	rel, err := h.fetch(c.Request.Context())
	if err != nil {
		// 502: we reached our server fine, but the upstream changelog fetch /
		// parse failed. The SPA treats any non-2xx as "couldn't check" and
		// surfaces the friendly "open official site" fallback.
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, rel)
}

// fetch returns a cached release within the TTL, otherwise refetches. Failures
// are cached for the same TTL too, so a blocked/offline machine doesn't hammer
// the upstream on every boot poll + manual click.
func (h *AppUpdateHandler) fetch(ctx context.Context) (*releasecheck.Release, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.fetchedAt.IsZero() && time.Since(h.fetchedAt) < h.ttl {
		return h.cached, h.cachedErr
	}
	rel, err := releasecheck.FetchLatest(ctx, h.client, h.baseURL)
	h.cached, h.cachedErr, h.fetchedAt = rel, err, time.Now()
	return rel, err
}
