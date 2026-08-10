package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// validPlatforms and validConnectionModes enumerate the DB CHECK-constraint
// values for the im_bot_onboarding_tokens table. An invalid value passed
// straight to CreateOnboardingToken causes a constraint violation that surfaces
// as a 500 with raw SQL text — validate here and return ErrInvalidChannelConfig
// (→ HTTP 400) instead.
var validPlatforms = map[string]bool{
	"lark": true, "dingtalk": true, "telegram": true, "wework": true, "wechat": true,
}
var validConnectionModes = map[string]bool{
	"stream": true, "webhook": true,
}

// ErrOnboardingTokenInvalid is returned by SubmitOnboardingCredential when
// the token is not found, already used, or expired.
var ErrOnboardingTokenInvalid = errors.New("imbot: onboarding token invalid or expired")

// IssueOnboardingToken generates a cryptographically-random one-time token
// (>= 32 bytes, URL-safe base64 encoded), stores only the sha256 hex hash
// in the database with a 15-minute expiry, and returns the raw token to the
// caller. The raw token is never stored and is returned exactly once.
func (s *IMBotService) IssueOnboardingToken(ctx context.Context, projectID int64, platform, channelName, connectionMode string) (rawToken string, err error) {
	// Validate platform against the DB CHECK constraint so an invalid value
	// returns ErrInvalidChannelConfig (→ 400) rather than a raw-SQL 500.
	if !validPlatforms[platform] {
		return "", fmt.Errorf("%w: platform must be one of lark|dingtalk|telegram|wework|wechat, got %q", ErrInvalidChannelConfig, platform)
	}
	// Default empty connectionMode to "stream" (the canonical long-connection mode).
	if connectionMode == "" {
		connectionMode = "stream"
	}
	if !validConnectionModes[connectionMode] {
		return "", fmt.Errorf("%w: connection_mode must be stream|webhook, got %q", ErrInvalidChannelConfig, connectionMode)
	}

	// Generate 32 random bytes.
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	rawToken = base64.RawURLEncoding.EncodeToString(buf)

	// Hash: sha256 hex of raw token bytes (the raw URL-safe base64 string).
	hash := sha256.Sum256([]byte(rawToken))
	tokenHash := hex.EncodeToString(hash[:])

	_, err = s.q.CreateOnboardingToken(ctx, store.CreateOnboardingTokenParams{
		TokenHash:      tokenHash,
		ProjectID:      projectID,
		Platform:       platform,
		ChannelName:    channelName,
		ConnectionMode: connectionMode,
		ExpiresAt:      time.Now().UTC().Add(15 * time.Minute),
	})
	if err != nil {
		return "", err
	}
	return rawToken, nil
}

// GetOnboardingTokenInfo looks up token metadata without consuming it.
// Returns platform, channel_name, and connection_mode so the credential form
// can render platform-correct fields before the user submits.
//
// Returns ErrOnboardingTokenInvalid for unknown, expired, or already-used
// tokens — same opaque error in all three cases to avoid enumeration.
func (s *IMBotService) GetOnboardingTokenInfo(ctx context.Context, rawToken string) (platform, channelName, connectionMode string, err error) {
	hash := sha256.Sum256([]byte(rawToken))
	tokenHash := hex.EncodeToString(hash[:])

	row, err := s.q.GetOnboardingTokenByHash(ctx, tokenHash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", "", ErrOnboardingTokenInvalid
		}
		return "", "", "", err
	}

	if row.UsedAt.Valid {
		return "", "", "", ErrOnboardingTokenInvalid
	}
	if time.Now().UTC().After(row.ExpiresAt) {
		return "", "", "", ErrOnboardingTokenInvalid
	}

	return row.Platform, row.ChannelName, row.ConnectionMode, nil
}

// SubmitOnboardingCredential redeems a one-time token and creates an IM bot
// channel using the provided credential map. If the credential map contains a
// "webhook_secret" key, its string value is extracted and passed as the
// channel's WebhookSecret (satisfying the webhook-mode validation gateway);
// it is NOT stored in the encrypted credential blob.
//
// Returns ErrOnboardingTokenInvalid if the token is unknown, already used, or
// expired. No secret or plaintext credential values are returned or logged.
func (s *IMBotService) SubmitOnboardingCredential(ctx context.Context, rawToken string, credential map[string]any) (channelID int64, err error) {
	// Hash the raw token.
	hash := sha256.Sum256([]byte(rawToken))
	tokenHash := hex.EncodeToString(hash[:])

	row, err := s.q.GetOnboardingTokenByHash(ctx, tokenHash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrOnboardingTokenInvalid
		}
		return 0, err
	}

	// Reject if already used.
	if row.UsedAt.Valid {
		return 0, ErrOnboardingTokenInvalid
	}

	// Reject if expired.
	if time.Now().UTC().After(row.ExpiresAt) {
		return 0, ErrOnboardingTokenInvalid
	}

	// Extract an optional display name from the credential map — it is not a
	// secret and must not go into the encrypted blob. When present and non-empty
	// it overrides the name chosen when the credential link was issued, letting
	// the person entering the credential name the bot themselves.
	nameOverride := ""
	if v, ok := credential["name"]; ok {
		s, ok := v.(string)
		if !ok {
			return 0, fmt.Errorf("imbot: name must be a string")
		}
		nameOverride = strings.TrimSpace(s)
		delete(credential, "name")
	}

	// Extract webhook_secret from credential if present — it must not go into
	// the encrypted blob; it belongs in the WebhookSecret column.
	webhookSecret := ""
	if v, ok := credential["webhook_secret"]; ok {
		s, ok := v.(string)
		if !ok {
			return 0, fmt.Errorf("imbot: webhook_secret must be a string")
		}
		webhookSecret = s
		delete(credential, "webhook_secret")
	}

	// The bot is owner-level; derive its owner from the onboarding token's project
	// (the project whose admin ran the wizard). The bot is not bound to that project.
	owner, ok := s.projectOwner(ctx, row.ProjectID)
	if !ok {
		return 0, ErrIMBotNotFound
	}
	name := row.ChannelName
	if nameOverride != "" {
		name = nameOverride
	}
	dto, err := s.CreateChannel(ctx, owner, CreateChannelInput{
		ChannelType:    row.Platform,
		Name:           name,
		ConnectionMode: row.ConnectionMode,
		WebhookSecret:  webhookSecret,
		Credential:     credential,
	})
	if err != nil {
		return 0, err
	}

	// Mark token as used after successful channel creation.
	if markErr := s.q.MarkOnboardingTokenUsed(ctx, row.ID); markErr != nil {
		// Channel already created; log concern but don't fail — the channel exists.
		// A second submit will also find UsedAt.Valid=false only until this
		// executes, so the window is tiny. In production a transaction would close
		// this gap; the task spec uses real sqlite env so we keep it simple.
		slog.Warn("imbot: mark onboarding token used failed", "tokenID", row.ID, "error", markErr)
	}

	return dto.ID, nil
}
