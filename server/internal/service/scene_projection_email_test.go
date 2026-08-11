package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/integration"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

func TestBuildEmailAccount_SMTPSecurity(t *testing.T) {
	// Explicit smtp_security=starttls → start_ssl + port 587.
	a, ok := buildEmailAccount("w", map[string]any{
		"imap_host": "imap.x.com", "username": "u", "password": "p",
		"smtp_host": "mail.x.com", "smtp_security": "starttls",
	}, true)
	require.True(t, ok)
	assert.True(t, a.HasSMTP)
	assert.Equal(t, "mail.x.com", a.SMTPHost)
	assert.False(t, a.SMTPUseSSL)
	assert.True(t, a.SMTPStartSSL)
	assert.Equal(t, 587, a.SMTPPort)

	// Explicit smtp_port overrides default.
	a2, _ := buildEmailAccount("w", map[string]any{
		"imap_host": "imap.x.com", "username": "u", "password": "p", "smtp_port": float64(2525),
	}, true)
	assert.Equal(t, 2525, a2.SMTPPort)
}

func TestBuildEmailAccount_SecurityAndSMTPGate(t *testing.T) {
	cfg := map[string]any{
		"imap_host": "imap.example.com", "imap_port": float64(993),
		"username": "a@x.com", "password": "s3cret", "security": "ssl",
	}
	ro, ok := buildEmailAccount("work", cfg, false)
	require.True(t, ok)
	assert.Equal(t, "imap.example.com", ro.Host)
	assert.Equal(t, 993, ro.Port)
	assert.True(t, ro.UseSSL)
	assert.False(t, ro.StartSSL)
	assert.False(t, ro.HasSMTP, "write off → no outgoing → read-only")

	rw, ok := buildEmailAccount("work", cfg, true)
	require.True(t, ok)
	assert.True(t, rw.HasSMTP)
	assert.Equal(t, "smtp.example.com", rw.SMTPHost, "imap. -> smtp. derivation")
	assert.Equal(t, 465, rw.SMTPPort)

	st, _ := buildEmailAccount("w", map[string]any{"imap_host": "mail.x.com", "username": "u", "password": "p", "security": "starttls"}, false)
	assert.False(t, st.UseSSL)
	assert.True(t, st.StartSSL)
	assert.Equal(t, 143, st.Port)

	_, ok = buildEmailAccount("w", map[string]any{"imap_host": "h"}, false)
	assert.False(t, ok, "missing user/pass")
}

func TestWriteEmailConfigTOML_MultiAccountInlineTables(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	accts := []emailAccount{
		{Name: "work", User: "a@work.com", Password: `p"\x`, Host: "imap.work.com", Port: 993, UseSSL: true, HasSMTP: true, SMTPHost: "smtp.work.com", SMTPPort: 465},
		{Name: "personal", User: "b@163.com", Password: "q", Host: "imap.163.com", Port: 993, UseSSL: true}, // read-only (no SMTP)
	}
	require.NoError(t, writeEmailConfigTOML(path, accts))
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	s := string(b)

	assert.Equal(t, 2, strings.Count(s, "[[emails]]"), "two accounts")
	assert.Contains(t, s, `account_name = "work"`)
	assert.Contains(t, s, `account_name = "personal"`)
	assert.Contains(t, s, `host = "imap.work.com"`)
	assert.Contains(t, s, `host = "imap.163.com"`, "different domain second account")
	assert.Contains(t, s, `outgoing = { user_name = "a@work.com"`, "work has SMTP")
	assert.NotContains(t, s, `host = "smtp.163.com"`, "personal is read-only, no outgoing")
	assert.Contains(t, s, `password = "p\"\\x"`, "special chars escaped in TOML")
}

func TestEmailDeniedToolPatterns_WriteGate(t *testing.T) {
	off := emailDeniedToolPatterns("email", false)
	assert.Contains(t, off, "mcp__email__delete_emails")
	assert.Contains(t, off, "mcp__email__send_email")
	assert.Contains(t, off, "mcp__email__add_email_account")

	on := emailDeniedToolPatterns("email", true)
	assert.NotContains(t, on, "mcp__email__delete_emails")
	assert.NotContains(t, on, "mcp__email__send_email")
	assert.Contains(t, on, "mcp__email__add_email_account", "account mgmt always denied")
}

func TestSetWorkspaceManagedDeny_MergePreserves(t *testing.T) {
	ws := t.TempDir()
	claudeDir := filepath.Join(ws, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o755))
	seed := `{"permissions":{"deny":["Bash(rm -rf:*)"]},"hooks":{"x":1}}`
	require.NoError(t, os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(seed), 0o644))

	g := &MCPConfigGenerator{}
	require.NoError(t, g.SetWorkspaceManagedDeny(ws, "email",
		[]string{"mcp__email__send_email", "mcp__email__delete_emails"}))
	var root map[string]any
	b, _ := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	require.NoError(t, json.Unmarshal(b, &root))
	deny := root["permissions"].(map[string]any)["deny"].([]any)
	assert.Contains(t, deny, "Bash(rm -rf:*)")
	assert.Contains(t, deny, "mcp__email__send_email")
	assert.NotNil(t, root["hooks"])

	require.NoError(t, g.SetWorkspaceManagedDeny(ws, "email", []string{"mcp__email__send_email"}))
	b, _ = os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	json.Unmarshal(b, &root)
	deny = root["permissions"].(map[string]any)["deny"].([]any)
	assert.NotContains(t, deny, "mcp__email__delete_emails", "stale managed entry removed")
	assert.Contains(t, deny, "Bash(rm -rf:*)")
}

// grantImapWrite enables imap write-permission for a user through the real UI
// path: seed the imap system provider, then SetWritePref (external_api_write_prefs).
func grantImapWrite(t *testing.T, db *sql.DB, userID int64) {
	t.Helper()
	prov := NewExternalProviderService(store.New(db), db)
	_, err := prov.SeedSystem(context.Background())
	require.NoError(t, err)
	imap, err := prov.GetByName(context.Background(), "imap")
	require.NoError(t, err)
	require.NoError(t, prov.SetWritePref(context.Background(), userID, imap.ID, true))
}

// bindImapCred creates an imap credential for user 1 and returns its id.
func bindImapCred(t *testing.T, cred *ExternalCredentialService, alias, host string) int64 {
	t.Helper()
	c, err := cred.Create(context.Background(), ExternalCredentialUpsertInput{
		OwnerType: "user", OwnerID: 1, UserID: 1,
		Provider: integration.ProviderName("imap"), Alias: alias,
		RawConfig: map[string]any{"imap_host": host, "imap_port": 993, "username": "u@" + alias + ".com", "password": "p", "security": "ssl"},
	})
	require.NoError(t, err)
	return c.ID
}

// createWorkspaceInProject builds the project → column → issue → workspace chain
// (1:1) so the office-mail projector can resolve the workspace's owning project
// and read its bound imap external sources. Returns the workspace and project id.
func createWorkspaceInProject(t *testing.T, db *sql.DB, dataDir string) (store.Workspace, int64) {
	t.Helper()
	pr, err := db.Exec(`INSERT INTO projects (name, status, owner_type, owner_id) VALUES ('p', 'active', 'user', 1)`)
	require.NoError(t, err)
	projectID, _ := pr.LastInsertId()
	cr, err := db.Exec(`INSERT INTO columns (project_id, name, position) VALUES (?, 'todo', 0)`, projectID)
	require.NoError(t, err)
	colID, _ := cr.LastInsertId()
	ir, err := db.Exec(`INSERT INTO issues (column_id, title, position) VALUES (?, 't', 0)`, colID)
	require.NoError(t, err)
	issueID, _ := ir.LastInsertId()

	q := store.New(db)
	ws, err := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name: "ws-test", Path: filepath.Join(dataDir, "ws-tmp"), Status: "created",
		OwnerType: "user", OwnerID: 1, CreatedBy: sql.NullInt64{Int64: 1, Valid: true},
		IssueID: sql.NullInt64{Int64: issueID, Valid: true},
	})
	require.NoError(t, err)
	wsDir := OwnerRef{Type: "user", ID: 1}.WorkspacePath(dataDir, ws.ID)
	require.NoError(t, os.MkdirAll(wsDir, 0o755))
	updated, err := q.UpdateWorkspacePath(context.Background(), store.UpdateWorkspacePathParams{ID: ws.ID, Path: wsDir})
	require.NoError(t, err)
	// Default to an interactive permission mode so write-permission tests can
	// exercise the send path; autohost tests override this to 'autohost'.
	_, err = db.Exec(`INSERT INTO workspace_env (workspace_id, key, value) VALUES (?, 'NIUNIU_PERMISSION_MODE', 'default')`, ws.ID)
	require.NoError(t, err)
	return updated, projectID
}

// bindProjectImap binds an imap credential to a project as an external source
// (the project-level "外部源管理" binding the office-mail projector reads).
func bindProjectImap(t *testing.T, db *sql.DB, projectID int64, sourceKey string, credID int64) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO project_external_sources (project_id, provider, source_key, credential_id, config) VALUES (?, 'imap', ?, ?, '{}')`,
		projectID, sourceKey, credID)
	require.NoError(t, err)
}

func emailScene() *Projection {
	proj := NewProjection()
	proj.MergeFrom(&SceneDefinition{
		MCP:                 []MCPDecl{{Name: emailMCPServerName, Config: map[string]any{"command": "uvx"}}},
		RequiredCredentials: []RequiredCredential{{Alias: "mailbox", Provider: "imap"}},
	}, LayerOrigin(1))
	return proj
}

func TestApplyEmailIntegration_MultiAccountDifferentDomains(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	dataDir := t.TempDir()
	ws, projectID := createWorkspaceInProject(t, db, dataDir)
	owner := OwnerRef{Type: "user", ID: 1}
	wsDir := owner.WorkspacePath(dataDir, ws.ID)

	cred := newImapCredSvc(t, db)
	bindProjectImap(t, db, projectID, "work", bindImapCred(t, cred, "work", "imap.work.com"))
	bindProjectImap(t, db, projectID, "personal", bindImapCred(t, cred, "personal", "imap.163.com"))

	projector := NewSceneProjector(db, dataDir, &MCPConfigGenerator{}, nil, nil, cred)
	proj := emailScene()
	resolved, dropped, _ := projector.resolveProjectionCredentials(ctx, owner, 1, proj)
	missing := projector.applyEmailIntegration(ctx, owner, 1, ws.ID, wsDir, proj, resolved, dropped)
	require.Empty(t, missing)

	// CONFIG_PATH injected, pointing at the per-workspace TOML.
	cfgPath := resolved[emailMCPServerName]["env"].(map[string]any)["MCP_EMAIL_SERVER_CONFIG_PATH"].(string)
	assert.Equal(t, filepath.Join(wsDir, emailConfigSubdir, "config.toml"), cfgPath)

	b, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	s := string(b)
	assert.Equal(t, 2, strings.Count(s, "[[emails]]"), "both mailboxes materialized")
	assert.Contains(t, s, `host = "imap.work.com"`)
	assert.Contains(t, s, `host = "imap.163.com"`, "different domain account present")
	// Default read-only → no outgoing, and send/delete denied.
	assert.NotContains(t, s, "outgoing =")
	var root map[string]any
	d, _ := os.ReadFile(filepath.Join(wsDir, ".claude", "settings.json"))
	require.NoError(t, json.Unmarshal(d, &root))
	deny := root["permissions"].(map[string]any)["deny"].([]any)
	assert.Contains(t, deny, "mcp__email__send_email")
}

func TestApplyEmailIntegration_WriteEnabledAddsOutgoing(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	dataDir := t.TempDir()
	ws, projectID := createWorkspaceInProject(t, db, dataDir)
	owner := OwnerRef{Type: "user", ID: 1}
	wsDir := owner.WorkspacePath(dataDir, ws.ID)

	cred := newImapCredSvc(t, db)
	bindProjectImap(t, db, projectID, "work", bindImapCred(t, cred, "work", "imap.work.com"))
	grantImapWrite(t, db, 1) // user 1 enables write via the real UI path

	projector := NewSceneProjector(db, dataDir, &MCPConfigGenerator{}, nil, nil, cred)
	proj := emailScene()
	resolved, dropped, _ := projector.resolveProjectionCredentials(ctx, owner, 1, proj)
	projector.applyEmailIntegration(ctx, owner, 1, ws.ID, wsDir, proj, resolved, dropped)

	cfgPath := resolved[emailMCPServerName]["env"].(map[string]any)["MCP_EMAIL_SERVER_CONFIG_PATH"].(string)
	s, _ := os.ReadFile(cfgPath)
	assert.Contains(t, string(s), `outgoing = { user_name`, "write on → outgoing present")
	assert.Contains(t, string(s), `host = "smtp.work.com"`)

	d, _ := os.ReadFile(filepath.Join(wsDir, ".claude", "settings.json"))
	var root map[string]any
	require.NoError(t, json.Unmarshal(d, &root))
	deny := root["permissions"].(map[string]any)["deny"].([]any)
	assert.NotContains(t, deny, "mcp__email__send_email")
}

func TestReprojectImapWorkspaces_RefreshesConfig(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	dataDir := t.TempDir()
	ws, projectID := createWorkspaceInProject(t, db, dataDir)
	owner := OwnerRef{Type: "user", ID: 1}
	wsDir := owner.WorkspacePath(dataDir, ws.ID)

	cred := newImapCredSvc(t, db)
	bindProjectImap(t, db, projectID, "work", bindImapCred(t, cred, "work", "imap.work.com"))

	projector := NewSceneProjector(db, dataDir, &MCPConfigGenerator{}, nil, nil, cred)
	svc := NewSceneLayerService(db, projector)
	sceneSvc := NewSceneService(db)
	scene, err := sceneSvc.Create(ctx, owner, "office-mail-t", "Mail", "", nil, &SceneDefinition{
		MCP:                 []MCPDecl{{Name: emailMCPServerName, Config: map[string]any{"command": "uvx"}}},
		RequiredCredentials: []RequiredCredential{{Alias: "mailbox", Provider: "imap"}},
	})
	require.NoError(t, err)
	_, err = svc.Attach(ctx, ws.ID, scene.ID, nil)
	require.NoError(t, err)

	cfgPath := filepath.Join(wsDir, emailConfigSubdir, "config.toml")
	b, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	assert.Contains(t, string(b), `host = "imap.work.com"`)

	// Change the mailbox host, then re-project (what the credential change hook does).
	creds, err := cred.List(ctx, "user", 1, 1)
	require.NoError(t, err)
	require.NotEmpty(t, creds)
	_, err = cred.UpdateConfig(ctx, creds[0].ID, "user", 1, 1, map[string]any{
		"imap_host": "imap.NEW.com", "imap_port": 993, "username": "u@work.com", "password": "p", "security": "ssl",
	})
	require.NoError(t, err)

	projector.ReprojectImapWorkspaces(ctx, owner, 1)
	b2, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	assert.Contains(t, string(b2), `host = "imap.NEW.com"`, "config.toml refreshed after credential change")
	assert.NotContains(t, string(b2), `host = "imap.work.com"`)
}

// TestApplyEmailIntegration_OnlyProjectBoundMailboxes verifies office-mail
// projects exactly the imap creds BOUND TO THE PROJECT — a user-owned imap
// credential that is NOT bound as a project external source is excluded.
func TestApplyEmailIntegration_OnlyProjectBoundMailboxes(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	dataDir := t.TempDir()
	ws, projectID := createWorkspaceInProject(t, db, dataDir)
	owner := OwnerRef{Type: "user", ID: 1}
	wsDir := owner.WorkspacePath(dataDir, ws.ID)

	cred := newImapCredSvc(t, db)
	// "work" is bound to the project; "personal" exists for the user but is NOT.
	bindProjectImap(t, db, projectID, "work", bindImapCred(t, cred, "work", "imap.work.com"))
	bindImapCred(t, cred, "personal", "imap.163.com")

	projector := NewSceneProjector(db, dataDir, &MCPConfigGenerator{}, nil, nil, cred)
	proj := emailScene()
	resolved, dropped, _ := projector.resolveProjectionCredentials(ctx, owner, 1, proj)
	projector.applyEmailIntegration(ctx, owner, 1, ws.ID, wsDir, proj, resolved, dropped)
	cfgPath := resolved[emailMCPServerName]["env"].(map[string]any)["MCP_EMAIL_SERVER_CONFIG_PATH"].(string)
	s, _ := os.ReadFile(cfgPath)
	assert.Contains(t, string(s), `host = "imap.work.com"`, "project-bound mailbox present")
	assert.NotContains(t, string(s), `host = "imap.163.com"`, "unbound user mailbox excluded")
}

// TestReprojectImapWorkspacesForUser_AppliesWriteToggle verifies that enabling
// imap write-permission and reprojecting (what the SetWritePref hook does)
// refreshes the materialized config.toml (adds outgoing/SMTP) and drops the
// send_email deny — i.e. the toggle actually reaches the agent's files.
func TestReprojectImapWorkspacesForUser_AppliesWriteToggle(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	dataDir := t.TempDir()
	ws, projectID := createWorkspaceInProject(t, db, dataDir)
	owner := OwnerRef{Type: "user", ID: 1}
	wsDir := owner.WorkspacePath(dataDir, ws.ID)

	cred := newImapCredSvc(t, db)
	bindProjectImap(t, db, projectID, "work", bindImapCred(t, cred, "work", "imap.work.com"))

	projector := NewSceneProjector(db, dataDir, &MCPConfigGenerator{}, nil, nil, cred)
	svc := NewSceneLayerService(db, projector)
	sceneSvc := NewSceneService(db)
	scene, err := sceneSvc.Create(ctx, owner, "office-mail-w", "Mail", "", nil, &SceneDefinition{
		MCP:                 []MCPDecl{{Name: emailMCPServerName, Config: map[string]any{"command": "uvx"}}},
		RequiredCredentials: []RequiredCredential{{Alias: "mailbox", Provider: "imap"}},
	})
	require.NoError(t, err)
	_, err = svc.Attach(ctx, ws.ID, scene.ID, nil)
	require.NoError(t, err)

	cfgPath := filepath.Join(wsDir, emailConfigSubdir, "config.toml")
	b, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	assert.NotContains(t, string(b), "outgoing =", "read-only before write granted")

	// Grant write via the real Settings path, then reproject for the user (what
	// the imap write-pref change hook triggers).
	grantImapWrite(t, db, 1)
	projector.ReprojectImapWorkspacesForUser(ctx, 1)

	b2, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	assert.Contains(t, string(b2), `outgoing = { user_name`, "outgoing added after write granted + reproject")

	d, _ := os.ReadFile(filepath.Join(wsDir, ".claude", "settings.json"))
	var root map[string]any
	require.NoError(t, json.Unmarshal(d, &root))
	deny := root["permissions"].(map[string]any)["deny"].([]any)
	assert.NotContains(t, deny, "mcp__email__send_email", "send_email no longer denied after write granted")
}

// TestCollectEmailAccounts_ScopedToCreatorBindings verifies that on a shared
// (org-style) project, a workspace only materializes the imap mailboxes ITS
// CREATOR bound — another member's project binding is excluded, so one member's
// plaintext mailbox password never lands in another's config.toml.
func TestCollectEmailAccounts_ScopedToCreatorBindings(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	dataDir := t.TempDir()
	ws, projectID := createWorkspaceInProject(t, db, dataDir) // created_by = user 1
	owner := OwnerRef{Type: "user", ID: 1}
	wsDir := owner.WorkspacePath(dataDir, ws.ID)

	cred := newImapCredSvc(t, db)
	// User 1 binds "mine"; user 2 binds "other" to the SAME project.
	bindProjectImap(t, db, projectID, "mine", bindImapCred(t, cred, "mine", "imap.mine.com"))
	other, err := cred.Create(ctx, ExternalCredentialUpsertInput{
		OwnerType: "user", OwnerID: 2, UserID: 2,
		Provider: integration.ProviderName("imap"), Alias: "other",
		RawConfig: map[string]any{"imap_host": "imap.other.com", "imap_port": 993, "username": "u@other.com", "password": "p", "security": "ssl"},
	})
	require.NoError(t, err)
	bindProjectImap(t, db, projectID, "other", other.ID)

	projector := NewSceneProjector(db, dataDir, &MCPConfigGenerator{}, nil, nil, cred)
	proj := emailScene()
	resolved, dropped, _ := projector.resolveProjectionCredentials(ctx, owner, 1, proj)
	projector.applyEmailIntegration(ctx, owner, 1, ws.ID, wsDir, proj, resolved, dropped)
	cfgPath := resolved[emailMCPServerName]["env"].(map[string]any)["MCP_EMAIL_SERVER_CONFIG_PATH"].(string)
	s, _ := os.ReadFile(cfgPath)
	assert.Contains(t, string(s), `host = "imap.mine.com"`, "creator's own bound mailbox present")
	assert.NotContains(t, string(s), `host = "imap.other.com"`, "another member's bound mailbox excluded")
}

// TestApplyEmailIntegration_AutohostForcesReadOnly verifies that even with
// write-permission granted, a workspace in autohost (unattended) mode projects
// office-mail READ-ONLY: no outgoing/SMTP in config.toml and send_email denied —
// so an unattended agent can never fire off an irreversible email without a
// human confirming.
func TestApplyEmailIntegration_AutohostForcesReadOnly(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	dataDir := t.TempDir()
	ws, projectID := createWorkspaceInProject(t, db, dataDir)
	owner := OwnerRef{Type: "user", ID: 1}
	wsDir := owner.WorkspacePath(dataDir, ws.ID)

	cred := newImapCredSvc(t, db)
	bindProjectImap(t, db, projectID, "work", bindImapCred(t, cred, "work", "imap.work.com"))
	grantImapWrite(t, db, 1) // write-permission ON ...
	// ... but the workspace is unattended (autohost).
	_, err := db.Exec(`UPDATE workspace_env SET value = 'autohost' WHERE workspace_id = ? AND key = 'NIUNIU_PERMISSION_MODE'`, ws.ID)
	require.NoError(t, err)

	projector := NewSceneProjector(db, dataDir, &MCPConfigGenerator{}, nil, nil, cred)
	proj := emailScene()
	resolved, dropped, _ := projector.resolveProjectionCredentials(ctx, owner, 1, proj)
	projector.applyEmailIntegration(ctx, owner, 1, ws.ID, wsDir, proj, resolved, dropped)

	cfgPath := resolved[emailMCPServerName]["env"].(map[string]any)["MCP_EMAIL_SERVER_CONFIG_PATH"].(string)
	s, _ := os.ReadFile(cfgPath)
	assert.NotContains(t, string(s), "outgoing =", "autohost forces read-only despite write granted")

	d, _ := os.ReadFile(filepath.Join(wsDir, ".claude", "settings.json"))
	var root map[string]any
	require.NoError(t, json.Unmarshal(d, &root))
	deny := root["permissions"].(map[string]any)["deny"].([]any)
	assert.Contains(t, deny, "mcp__email__send_email", "send_email denied in autohost")
}

func TestApplyEmailIntegration_NoCredDropsServer(t *testing.T) {
	ctx := context.Background()
	db := setupSceneTestDB(t)
	dataDir := t.TempDir()
	ws := createTestWorkspace(t, db, dataDir)
	owner := OwnerRef{Type: "user", ID: 1}
	wsDir := owner.WorkspacePath(dataDir, ws.ID)

	cred := newImapCredSvc(t, db) // none bound
	projector := NewSceneProjector(db, dataDir, &MCPConfigGenerator{}, nil, nil, cred)
	proj := emailScene()
	resolved, dropped, _ := projector.resolveProjectionCredentials(ctx, owner, 1, proj)
	missing := projector.applyEmailIntegration(ctx, owner, 1, ws.ID, wsDir, proj, resolved, dropped)

	assert.True(t, missing["mailbox"])
	assert.True(t, dropped[emailMCPServerName])
	assert.NotContains(t, resolved, emailMCPServerName)
}
