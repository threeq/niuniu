package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"log/slog"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

const (
	// MCPSessionTTL is the lifetime a session token is (re)issued for. Agent
	// starts create tokens with this TTL; Validate slides the expiry forward by
	// the same amount while the token is actively used.
	MCPSessionTTL = 24 * time.Hour
	// mcpSessionRenewWindow: when a valid token has less than this remaining, a
	// successful Validate pushes its expiry out to now+MCPSessionTTL. This keeps
	// long-running (autohost) sessions from ever tripping over the 24h TTL, while
	// still expiring idle/leaked tokens. Bounded to ~one DB write per window.
	mcpSessionRenewWindow = MCPSessionTTL / 2
)

type MCPSessionService struct {
	q *store.Queries
}

func NewMCPSessionService(q *store.Queries) *MCPSessionService {
	return &MCPSessionService{q: q}
}

// Create issues a new token for a workspace session. Returns the RAW token
// string (base64, 32 bytes). The caller writes this into the workspace's
// .mcp.json; the server only stores its sha256 hash.
func (s *MCPSessionService) Create(ctx context.Context, workspaceID int64, ttl time.Duration) (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	rawStr := base64.RawURLEncoding.EncodeToString(raw[:])
	hash := sha256.Sum256([]byte(rawStr))
	hashStr := base64.RawURLEncoding.EncodeToString(hash[:])
	if err := s.q.CreateMCPSessionToken(ctx, store.CreateMCPSessionTokenParams{
		WorkspaceID: workspaceID,
		TokenHash:   hashStr,
		ExpiresAt:   time.Now().Add(ttl),
	}); err != nil {
		return "", err
	}
	return rawStr, nil
}

// Validate returns the workspace_id bound to the token, or an error if the
// token is unknown, expired, or invalid.
func (s *MCPSessionService) Validate(ctx context.Context, raw string) (int64, error) {
	hash := sha256.Sum256([]byte(raw))
	hashStr := base64.RawURLEncoding.EncodeToString(hash[:])
	row, err := s.q.GetMCPSessionByHash(ctx, hashStr)
	if err != nil {
		return 0, errors.New("mcp session: invalid token")
	}
	now := time.Now()
	if !now.Before(row.ExpiresAt) {
		return 0, errors.New("mcp session: token expired")
	}
	// Sliding-window renewal: an actively-used token must not lapse mid-session.
	// A long-running agent (autohost) can outlive the fixed 24h TTL; once the
	// token expired, the hourly cleanup deleted the row and every /mcp call
	// 401'd with "invalid token". Pushing the expiry forward on use keeps such
	// sessions alive without rewriting .mcp.json (the raw token is unchanged),
	// while idle/leaked tokens still expire as the safety net. A best-effort
	// write: on failure the token is still valid for this request and we retry
	// on the next call.
	if row.ExpiresAt.Sub(now) < mcpSessionRenewWindow {
		if err := s.q.RenewMCPSessionToken(ctx, store.RenewMCPSessionTokenParams{
			ExpiresAt: now.Add(MCPSessionTTL),
			TokenHash: hashStr,
		}); err != nil {
			slog.Warn("mcp session: renew expiry failed", "workspaceID", row.WorkspaceID, "err", err)
		}
	}
	return row.WorkspaceID, nil
}

// RenewForWorkspace pushes the expiry of every token bound to a workspace out
// to now+MCPSessionTTL. Called by the server's periodic heartbeat for every
// workspace with a live agent process/session, so a long-lived workspace (weeks,
// with idle 停留 gaps between activity) keeps a valid token for as long as its
// agent is alive — independent of MCP-call activity, which the sliding-on-use
// renewal in Validate depends on. A no-op (0 rows) when the workspace has no
// token. Clean session end still Revokes the token, so a stopped workspace stops
// being renewed and its token lapses via the normal cleanup path.
func (s *MCPSessionService) RenewForWorkspace(ctx context.Context, workspaceID int64) error {
	return s.q.RenewMCPSessionsForWorkspace(ctx, store.RenewMCPSessionsForWorkspaceParams{
		ExpiresAt:   time.Now().Add(MCPSessionTTL),
		WorkspaceID: workspaceID,
	})
}

// Revoke drops all tokens for a workspace (session end).
func (s *MCPSessionService) Revoke(ctx context.Context, workspaceID int64) error {
	return s.q.DeleteMCPSessionsForWorkspace(ctx, workspaceID)
}

// CleanupExpired removes all expired MCP session tokens. Intended to be called
// periodically (e.g., hourly) to prevent accumulation of stale rows.
func (s *MCPSessionService) CleanupExpired(ctx context.Context) error {
	return s.q.DeleteExpiredMCPSessions(ctx)
}
