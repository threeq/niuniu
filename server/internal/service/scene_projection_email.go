// Package service: office-mail / ai-zerolab mcp-email-server integration.
//
// mcp-email-server supports MULTIPLE accounts (different domains/providers) via
// a TOML config file's [[emails]] array, and lets the config path be overridden
// with MCP_EMAIL_SERVER_CONFIG_PATH. So niuniu, at projection time, materializes
// a PER-WORKSPACE config.toml containing one [[emails]] entry per imap
// credential the (owner,user) has bound, and points the server at it. This both
// delivers the decrypted credentials and isolates accounts per workspace — no
// global config, no encryption to reproduce, no HOME hack.
//
// Write-permission gating is two-layered (global per user, keyed by the imap
// provider write-pref):
//   - SEND/DRAFT: an account with no [emails.outgoing] is read-only (the server
//     hides send_email/save_to_mailbox when no account can send). So niuniu
//     omits every account's outgoing block unless write-permission is granted.
//   - DELETE/MOVE/MARK: the server does NOT gate these, so niuniu denies them
//     via Claude settings.json permissions.deny when write-permission is off.
//   - add_email_account is always denied: niuniu owns the account config.
package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/integration"
)

// emailMCPServerName is the MCP server name the office-mail scene declares; it
// becomes the `mcp__<name>__<tool>` prefix used in permission deny patterns.
const emailMCPServerName = "email"

// imapProviderName is the external-provider name the office-mail credential
// (mailbox) binds to. Single source so the seed (external_provider.go), the
// projection-time checks here, and the credential lookups all agree — a mismatch
// would silently skip email integration.
const imapProviderName = "imap"

// emailConfigSubpath is the per-workspace location of the generated TOML config
// pointed at by MCP_EMAIL_SERVER_CONFIG_PATH.
const emailConfigSubdir = ".email-config"

// emailAlwaysDenyTools are denied regardless of write-permission: niuniu owns
// the account config, so the agent must not add accounts.
var emailAlwaysDenyTools = []string{"add_email_account"}

// emailWriteTools mutate the mailbox; denied unless write-permission is granted.
// send_email/save_to_mailbox are also auto-hidden when no account has outgoing,
// but we deny-list them too as belt-and-suspenders.
var emailWriteTools = []string{
	"delete_emails", "move_emails", "mark_emails_as_read",
	"send_email", "save_to_mailbox",
}

// emailDeniedToolPatterns returns the Claude permissions.deny patterns for the
// email MCP server given the write-permission decision.
func emailDeniedToolPatterns(serverName string, writeEnabled bool) []string {
	tools := append([]string{}, emailAlwaysDenyTools...)
	if !writeEnabled {
		tools = append(tools, emailWriteTools...)
	}
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, fmt.Sprintf("mcp__%s__%s", serverName, t))
	}
	return out
}

// emailAccount is one materialized mailbox (a [[emails]] TOML entry).
type emailAccount struct {
	Name     string // account_name + email_address; agent selects by this
	User     string
	Password string
	Host     string
	Port     int
	UseSSL   bool
	StartSSL bool
	// Outgoing (SMTP) — populated only when write-permission is granted.
	SMTPHost     string
	SMTPPort     int
	SMTPUseSSL   bool
	SMTPStartSSL bool
	HasSMTP      bool
}

// applyEmailIntegration wires the office-mail scene's mcp-email-server: it
// materializes a per-workspace TOML config with one account per imap credential
// the (owner,user) has bound (multi-account, different domains supported),
// points the server at it via MCP_EMAIL_SERVER_CONFIG_PATH, and writes a
// permissions.deny list gating write/management tools by write-permission. It
// mutates resolvedConfigs/dropped in place and returns the aliases that could
// not be resolved (for MissingCredentials). No-op when the scene has no email
// server / imap credential requirement.
func (p *SceneProjector) applyEmailIntegration(
	ctx context.Context, owner OwnerRef, userID, wsID int64, wsDir string,
	proj *Projection, resolvedConfigs map[string]map[string]any, dropped map[string]bool,
) map[string]bool {
	missing := map[string]bool{}
	srv, ok := resolvedConfigs[emailMCPServerName]
	if !ok {
		return missing
	}
	alias := ""
	for _, rc := range proj.RequiredCredentials {
		if rc.Provider == imapProviderName {
			alias = rc.Alias
			break
		}
	}
	if alias == "" {
		return missing
	}

	// cfgPath holds plaintext passwords; whenever we drop the email server
	// (no credential, decrypt failure, write error) remove any stale file so a
	// deleted/changed credential's password doesn't linger on disk. Best-effort.
	cfgPath := filepath.Join(wsDir, emailConfigSubdir, "config.toml")
	drop := func() {
		delete(resolvedConfigs, emailMCPServerName)
		dropped[emailMCPServerName] = true
		missing[alias] = true // label for the "bind credential" card
		if err := os.Remove(cfgPath); err != nil && !os.IsNotExist(err) {
			slog.Warn("scene projector: stale email config.toml cleanup failed", "err", err)
		}
	}
	if p.extCred == nil {
		drop()
		return missing
	}

	// Autohost (bypassPermissions) runs UNATTENDED with no confirmation prompt,
	// and it is the DEFAULT mode for new workspaces. Sending/deleting mail is
	// irreversible and outward-facing, so office-mail is forced READ-ONLY in
	// autohost regardless of the write-permission toggle — no outgoing(SMTP) and
	// every write tool denied. To actually send, the user switches the workspace
	// to an interactive mode, where each write hits the permission prompt.
	writeEnabled := p.imapWriteEnabled(ctx, userID) && !p.workspaceAutohost(ctx, wsID)
	accounts := p.collectEmailAccounts(ctx, wsID, userID, writeEnabled)
	if len(accounts) == 0 {
		drop() // no imap mailbox the creator bound on the workspace's project
		return missing
	}

	if err := writeEmailConfigTOML(cfgPath, accounts); err != nil {
		slog.Warn("scene projector: email config.toml write failed", "err", err)
		drop()
		return missing
	}
	// Point the server at the per-workspace config (isolation; no global file).
	setServerEnv(srv, "MCP_EMAIL_SERVER_CONFIG_PATH", cfgPath)
	// Enable attachment download (off by default in the server) so the agent can
	// fetch attachments; files land under the workspace so they stay accessible.
	setServerEnv(srv, "MCP_EMAIL_SERVER_ENABLE_ATTACHMENT_DOWNLOAD", "true")
	// Stamp the write state into the (hashed) server env so toggling write
	// permission changes the projection Digest → restart_required fires and the
	// banner prompts a restart. Otherwise the write toggle only rewrites the
	// side-files (config.toml outgoing + permissions.deny), which the digest
	// doesn't track, so the agent would silently keep the old (read-only)
	// behaviour until an unrelated change. The server itself ignores this var.
	setServerEnv(srv, "NIUNIU_OFFICE_MAIL_WRITE", strconv.FormatBool(writeEnabled))

	// Audit injection of an org-shared mailbox (no-op for personal owners):
	// actor is the workspace creator whose bound credentials were projected.
	appendResourceAudit(ctx, p.db, nil, owner.Type, owner.ID, userID,
		"office_mail.projected", "workspace", wsID,
		fmt.Sprintf(`{"accounts":%d}`, len(accounts)))

	if p.mcpGen != nil {
		deny := emailDeniedToolPatterns(emailMCPServerName, writeEnabled)
		if err := p.mcpGen.SetWorkspaceManagedDeny(wsDir, emailMCPServerName, deny); err != nil {
			slog.Warn("scene projector: email deny-list write failed", "err", err)
		}
	}
	return missing
}

// collectEmailAccounts decrypts the imap credentials BOUND TO THE WORKSPACE'S
// PROJECT by its creator (project_external_sources, provider=imap, bound by
// userID) and maps each to an emailAccount (account_name = credential alias).
// Binding which mailboxes a context uses is a PROJECT-level concern (Settings →
// 外部源管理), so a workspace projects exactly the mailboxes its project bound —
// multiple, possibly across different domains. Scoping to the creator's own
// bindings keeps one org-project member's plaintext mailbox password out of
// another member's materialized config.toml (no-op for personal projects).
// Incomplete credentials are skipped. SMTP (outgoing) is included per account
// only when writeEnabled. Order is stable (by alias).
func (p *SceneProjector) collectEmailAccounts(ctx context.Context, wsID, userID int64, writeEnabled bool) []emailAccount {
	projectID, ok := p.projectIDForWorkspace(ctx, wsID)
	if !ok {
		return nil // workspace has no owning project → no project-bound mailboxes
	}
	creds, err := p.extCred.ListDecryptedForProject(ctx, projectID, userID, integration.ProviderName(imapProviderName))
	if err != nil {
		slog.Warn("scene projector: list project imap sources failed", "err", err)
		return nil
	}
	out := make([]emailAccount, 0, len(creds))
	for _, c := range creds { // already ordered by alias for deterministic output
		if acct, ok := buildEmailAccount(c.Alias, c.Config, writeEnabled); ok {
			out = append(out, acct)
		}
	}
	return out
}

// projectIDForWorkspace resolves a workspace to its owning project via the
// workspace → issue → column → project chain (1:1). Returns false when the
// workspace has no issue/column/project (e.g. studio or schedule workspaces).
func (p *SceneProjector) projectIDForWorkspace(ctx context.Context, wsID int64) (int64, bool) {
	var pid int64
	err := p.db.QueryRowContext(ctx, `
		SELECT c.project_id
		FROM workspaces w
		JOIN issues i ON i.id = w.issue_id
		JOIN columns c ON c.id = i.column_id
		WHERE w.id = ?`, wsID).Scan(&pid)
	if err != nil {
		return 0, false
	}
	return pid, true
}

// buildEmailAccount maps a decrypted imap credential config to an emailAccount.
// Field names mirror AddCredentialDialog's imap form (imap_host / imap_port /
// username / password / security; optional smtp_host / smtp_port).
func buildEmailAccount(name string, cfg map[string]any, writeEnabled bool) (emailAccount, bool) {
	host := credString(cfg["imap_host"])
	user := credString(cfg["username"])
	pass := credString(cfg["password"])
	if host == "" || user == "" || pass == "" {
		return emailAccount{}, false
	}
	useSSL, startSSL, defPort := mailSecurity(credString(cfg["security"]), 993, 143)
	port := credInt(cfg["imap_port"])
	if port == 0 {
		port = defPort
	}
	acct := emailAccount{
		Name: name, User: user, Password: pass, Host: host, Port: port,
		UseSSL: useSSL, StartSSL: startSSL,
	}
	if writeEnabled {
		// SMTP presence is what lets the server send. Use the credential's
		// optional smtp_* fields if provided, else derive sensible defaults.
		smtpHost := credString(cfg["smtp_host"])
		if smtpHost == "" {
			smtpHost = deriveSMTPHost(host) // imap.host -> smtp.host convention
		}
		// smtp_security: ssl(465) | starttls(587) | none; falls back to the imap
		// security when unset.
		smtpSec := credString(cfg["smtp_security"])
		if strings.TrimSpace(smtpSec) == "" {
			smtpSec = credString(cfg["security"])
		}
		smtpUseSSL, smtpStartSSL, smtpDefPort := mailSecurity(smtpSec, 465, 587)
		smtpPort := credInt(cfg["smtp_port"])
		if smtpPort == 0 {
			smtpPort = smtpDefPort
		}
		acct.SMTPHost, acct.SMTPPort = smtpHost, smtpPort
		acct.SMTPUseSSL, acct.SMTPStartSSL, acct.HasSMTP = smtpUseSSL, smtpStartSSL, true
	}
	return acct, true
}

// mailSecurity maps a security mode to its (use_ssl, start_ssl) flags and the
// default port for that mode. sslPort applies to implicit TLS (the default /
// "ssl" / "tls"); plainPort applies to STARTTLS and plaintext. Shared by the
// IMAP (993/143) and SMTP (465/587) sides so the mapping lives in one place.
func mailSecurity(security string, sslPort, plainPort int) (useSSL, startSSL bool, port int) {
	switch strings.ToLower(strings.TrimSpace(security)) {
	case "starttls":
		return false, true, plainPort
	case "none":
		return false, false, plainPort
	default: // "", "ssl", "tls", "implicit"
		return true, false, sslPort
	}
}

// writeEmailConfigTOML renders the accounts into mcp-email-server's [[emails]]
// TOML and writes it 0600 (it holds plaintext passwords) under a per-workspace
// dir. Uses inline tables for incoming/outgoing so multiple [[emails]] entries
// stay unambiguous.
func writeEmailConfigTOML(path string, accounts []emailAccount) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir email config dir: %w", err)
	}
	var b strings.Builder
	b.WriteString("# Auto-generated by niuniu. Do not edit; regenerated on projection.\n")
	for _, a := range accounts {
		b.WriteString("\n[[emails]]\n")
		fmt.Fprintf(&b, "account_name = %s\n", tomlString(a.Name))
		fmt.Fprintf(&b, "full_name = %s\n", tomlString(a.Name))
		fmt.Fprintf(&b, "email_address = %s\n", tomlString(a.User))
		fmt.Fprintf(&b, "incoming = %s\n", tomlServerInline(a.User, a.Password, a.Host, a.Port, a.UseSSL, a.StartSSL))
		if a.HasSMTP {
			fmt.Fprintf(&b, "outgoing = %s\n", tomlServerInline(a.User, a.Password, a.SMTPHost, a.SMTPPort, a.SMTPUseSSL, a.SMTPStartSSL))
		}
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

func tomlServerInline(user, pass, host string, port int, useSSL, startSSL bool) string {
	return fmt.Sprintf(
		"{ user_name = %s, password = %s, host = %s, port = %d, use_ssl = %t, start_ssl = %t, verify_ssl = true }",
		tomlString(user), tomlString(pass), tomlString(host), port, useSSL, startSSL,
	)
}

// tomlString renders a TOML basic string, escaping backslash and double-quote
// (and control chars) so credentials with special characters stay valid.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// deriveSMTPHost turns an "imap.example.com" host into "smtp.example.com"; a
// host without the imap. prefix is returned unchanged (many self-hosted servers
// share one host for IMAP and SMTP).
func deriveSMTPHost(imapHost string) string {
	if strings.HasPrefix(imapHost, "imap.") {
		return "smtp." + strings.TrimPrefix(imapHost, "imap.")
	}
	return imapHost
}

// ReprojectImapWorkspaces re-applies projection for every non-archived workspace
// the (owner,user) created whose cached projection references an imap credential
// (office-mail). Called after an imap credential is changed/deleted so the
// per-workspace config.toml is refreshed (or the server dropped) instead of
// keeping a stale password snapshot that silently fails login. Best-effort:
// errors are logged, not surfaced. Safe to wire as ExternalCredentialService's
// change hook.
func (p *SceneProjector) ReprojectImapWorkspaces(ctx context.Context, owner OwnerRef, userID int64) {
	// Pre-filtered to the (owner,user)'s non-archived workspaces, then matched on
	// the PARSED projection structure rather than a substring LIKE on the JSON
	// blob — so it stays correct if the marshaler's spacing/field order changes.
	rows, err := p.db.QueryContext(ctx, `
		SELECT w.id, sp.projected_definition FROM workspaces w
		JOIN workspace_scene_projection sp ON sp.workspace_id = w.id
		WHERE w.owner_type = ? AND w.owner_id = ? AND w.created_by = ? AND w.is_archived = 0`,
		owner.Type, owner.ID, userID)
	if err != nil {
		slog.Warn("reproject imap workspaces: query failed", "err", err)
		return
	}
	p.reprojectImapRows(ctx, rows)
}

// ReprojectImapWorkspacesForUser re-projects every non-archived workspace the
// user CREATED that references an imap credential, regardless of owner. Used
// when a USER-GLOBAL setting changes — the imap write-permission toggle
// (external_api_write_prefs is keyed by user, not owner) — so every office-mail
// workspace the user drives (personal + org) regenerates its config.toml
// (outgoing/SMTP) and permissions.deny to match the new write state. Without
// this the toggle never reaches the materialized files and sending stays
// disabled. Best-effort: errors logged, not surfaced.
func (p *SceneProjector) ReprojectImapWorkspacesForUser(ctx context.Context, userID int64) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT w.id, sp.projected_definition FROM workspaces w
		JOIN workspace_scene_projection sp ON sp.workspace_id = w.id
		WHERE w.created_by = ? AND w.is_archived = 0`, userID)
	if err != nil {
		slog.Warn("reproject imap workspaces (user): query failed", "err", err)
		return
	}
	p.reprojectImapRows(ctx, rows)
}

// reprojectImapRows drains (id, projected_definition) rows and re-applies every
// workspace whose cached projection references an imap required-credential.
// Applies AFTER draining the cursor (Apply runs its own queries; nesting on the
// open rows deadlocks the single-conn SQLite path).
func (p *SceneProjector) reprojectImapRows(ctx context.Context, rows *sql.Rows) {
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		var def string
		if err := rows.Scan(&id, &def); err != nil {
			continue
		}
		if projectionReferencesIMAP(def) {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		if _, err := p.Apply(ctx, id); err != nil {
			slog.Warn("reproject imap workspaces: apply failed", "workspace_id", id, "err", err)
		}
	}
}

// projectionReferencesIMAP reports whether a cached projection JSON declares an
// imap required-credential. Parses just the required_credentials providers, so
// it's immune to JSON formatting and won't false-match "imap" appearing in some
// other field.
func projectionReferencesIMAP(def string) bool {
	var pj struct {
		RequiredCredentials []struct {
			Provider string `json:"provider"`
		} `json:"required_credentials"`
	}
	if err := json.Unmarshal([]byte(def), &pj); err != nil {
		return false
	}
	for _, rc := range pj.RequiredCredentials {
		if rc.Provider == imapProviderName {
			return true
		}
	}
	return false
}

// imapWriteEnabled reports whether the user granted write-permission for their
// mailboxes. It reads the SAME store the existing write-permission UI writes —
// external_api_write_prefs keyed by the imap provider's id (POST via
// ExternalProviderService.SetWritePref) — so the Settings toggle actually
// governs send/delete. Default false → read-only: no SMTP outgoing and
// delete/move/mark denied. Global per user (per-account is a future step).
// workspaceAutohost reports whether the workspace runs in autohost permission
// mode (NIUNIU_PERMISSION_MODE=autohost) — niuniu's bypassPermissions superset,
// where the CLI raises NO confirmation prompt and a watchdog auto-continues.
// Fails CLOSED: an unknown/unreadable mode is treated as autohost so office-mail
// never auto-enables an irreversible send when we can't confirm a human is in
// the loop (Create seeds 'autohost' anyway, so a missing row is already the
// unattended default).
func (p *SceneProjector) workspaceAutohost(ctx context.Context, wsID int64) bool {
	var mode string
	err := p.db.QueryRowContext(ctx,
		`SELECT value FROM workspace_env WHERE workspace_id = ? AND key = 'NIUNIU_PERMISSION_MODE'`, wsID).Scan(&mode)
	if err != nil {
		return true
	}
	return strings.TrimSpace(mode) == "autohost"
}

func (p *SceneProjector) imapWriteEnabled(ctx context.Context, userID int64) bool {
	var enabled int
	err := p.db.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(w.enabled), 0)
		FROM external_providers p
		LEFT JOIN external_api_write_prefs w
		  ON w.provider_id = p.id AND w.user_id = ?
		WHERE p.name = ?`, userID, imapProviderName).Scan(&enabled)
	if err != nil {
		return false
	}
	return enabled == 1
}

// setServerEnv sets env[key]=val on a deep-copied MCP server config map,
// creating the env sub-map if absent.
func setServerEnv(srv map[string]any, key, val string) {
	env, _ := srv["env"].(map[string]any)
	if env == nil {
		env = map[string]any{}
		srv["env"] = env
	}
	env[key] = val
}

func credString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return stringifyCredValue(t)
	default:
		return ""
	}
}

func credInt(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(t))
		if err != nil {
			return 0
		}
		return n
	default:
		return 0
	}
}
