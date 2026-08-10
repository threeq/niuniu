// Package claudehome probes the user's $HOME/.claude/ directory and
// $HOME/.claude.json for OAuth account information. Used by the default-
// account row seed migration and the Refresh handler.
//
// This package is intentionally dependency-free (only stdlib) so it can be
// imported by both internal/store/ (for migrations) and internal/service/
// (for service-layer logic) without creating a circular dependency.
package claudehome

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// ReadEmailFromConfigDir extracts oauthAccount.emailAddress from
// <dir>/.claude.json. Returns "" on any failure (missing file, malformed
// JSON, missing field) — callers decide what to do with empty.
func ReadEmailFromConfigDir(dir string) string {
	return readEmail(filepath.Join(dir, ".claude.json"))
}

// ReadEmailFromHomeJSON reads $HOME/.claude.json (note: not inside ~/.claude/).
// This is where the Claude CLI writes account info when CLAUDE_CONFIG_DIR is
// unset (the default account path).
func ReadEmailFromHomeJSON() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return readEmail(filepath.Join(home, ".claude.json"))
}

func readEmail(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var v struct {
		OAuthAccount struct {
			EmailAddress string `json:"emailAddress"`
		} `json:"oauthAccount"`
	}
	if json.Unmarshal(b, &v) != nil {
		return ""
	}
	return v.OAuthAccount.EmailAddress
}

// ReadTierFromConfigDir extracts (subscriptionType, rateLimitTier) from
// <dir>/.claude.json's oauthAccount block. organizationType has its
// "claude_" prefix stripped so values align with the .credentials.json
// shorthand (e.g. "claude_max" -> "max"). Returns ("", "") on any failure
// (missing file, malformed JSON, missing fields).
//
// This is the authoritative source for tier display: the Claude CLI's
// profile sync writes it consistently, whereas .credentials.json may
// carry null values for these fields depending on the OAuth response.
func ReadTierFromConfigDir(dir string) (subscriptionType, rateLimitTier string) {
	return readTier(filepath.Join(dir, ".claude.json"))
}

// ReadTierFromHomeJSON reads $HOME/.claude.json (same fields as
// ReadTierFromConfigDir). This is where the default account's tier info
// lives when CLAUDE_CONFIG_DIR is unset.
func ReadTierFromHomeJSON() (subscriptionType, rateLimitTier string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", ""
	}
	return readTier(filepath.Join(home, ".claude.json"))
}

func readTier(path string) (string, string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	var v struct {
		OAuthAccount struct {
			OrganizationType          string `json:"organizationType"`
			OrganizationRateLimitTier string `json:"organizationRateLimitTier"`
		} `json:"oauthAccount"`
	}
	if json.Unmarshal(b, &v) != nil {
		return "", ""
	}
	return strings.TrimPrefix(v.OAuthAccount.OrganizationType, "claude_"), v.OAuthAccount.OrganizationRateLimitTier
}

// CredsExistInConfigDir is a best-effort check for "this dir already has
// Claude CLI credentials". Linux/Windows: .credentials.json file size > 0.
// macOS uses Keychain — file may not exist even if logged in. Caller should
// also consider ReadEmailFromConfigDir != "" as a positive signal there.
func CredsExistInConfigDir(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, ".credentials.json"))
	return err == nil && st.Size() > 0
}

// CredsExistInHome checks $HOME/.claude/.credentials.json.
func CredsExistInHome() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	return CredsExistInConfigDir(filepath.Join(home, ".claude"))
}
