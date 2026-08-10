package main

import (
	"bufio"
	"context"
	"crypto/sha1"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/config"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	"golang.org/x/term"
)

func handleAdminCommand() {
	if len(os.Args) < 3 {
		fmt.Println("Usage: niuniu admin <command>")
		fmt.Println("Commands:")
		fmt.Println("  create-user          Create a new user")
		fmt.Println("  create-org           Create a new organization")
		fmt.Println("  add-member           Add a user to an organization")
		fmt.Println("  migrate-recover      Inspect half-applied owner-model migrations")
		fmt.Println("  delete-user          Delete a user (with referential checks; --purge to cascade)")
		fmt.Println("  list-user-resources  Show a user's personal resources and org memberships")
		fmt.Println("  transfer-resource    Transfer resource ownership to another user or org")
		fmt.Println("  schema-diff          Compare SQLite and PostgreSQL schema DDL for drift")
		os.Exit(1)
	}

	switch os.Args[2] {
	case "create-user":
		createUserCommand()
	case "create-org":
		handleCreateOrg()
	case "add-member":
		handleAddMember()
	case "migrate-recover":
		handleMigrateRecover()
	case "delete-user":
		handleDeleteUser()
	case "list-user-resources":
		handleListUserResources()
	case "transfer-resource":
		handleTransferResource()
	case "schema-diff":
		handleSchemaDiff()
	default:
		fmt.Printf("Unknown admin command: %s\n", os.Args[2])
		os.Exit(1)
	}
}

func handleSchemaDiff() {
	sqliteOnly, pgOnly, differing := store.SchemaDiff(store.Schema, store.SchemaPostgres)
	hasIssues := false
	for _, t := range sqliteOnly {
		fmt.Printf("SQLITE_ONLY  %s\n", t)
		hasIssues = true
	}
	for _, t := range pgOnly {
		fmt.Printf("PG_ONLY      %s\n", t)
		hasIssues = true
	}
	for name, defs := range differing {
		fmt.Printf("DIFFER       %s\n", name)
		fmt.Printf("  SQLite: %s\n", strings.TrimSpace(defs[0]))
		fmt.Printf("  PG:     %s\n", strings.TrimSpace(defs[1]))
		hasIssues = true
	}
	if !hasIssues {
		fmt.Println("OK: no table-level drift detected between SQLite and PostgreSQL schemas")
		return
	}
	os.Exit(1)
}

func createUserCommand() {
	fs := flag.NewFlagSet("create-user", flag.ExitOnError)
	username := fs.String("username", "", "Username (required)")
	role := fs.String("role", "member", "Role: admin, member, viewer")
	displayName := fs.String("display-name", "", "Display name")
	fs.Parse(os.Args[3:])

	if *username == "" {
		fmt.Println("Error: --username is required")
		os.Exit(1)
	}

	fmt.Print("Password: ")
	passwordBytes, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil || len(passwordBytes) == 0 {
		fmt.Println("Error: password is required")
		os.Exit(1)
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Printf("Error loading config: %v\n", err)
		os.Exit(1)
	}

	db, err := store.Open(cfg)
	if err != nil {
		fmt.Printf("Error opening database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	q := store.NewQueries(db)
	secret := config.GetAuthSecret()
	authSvc := service.NewAuthService(q, db, secret, cfg.Auth.TokenExpiry, cfg.Auth.RefreshExpiry)

	dn := *displayName
	if dn == "" {
		dn = *username
	}

	user, err := authSvc.CreateUser(context.Background(), *username, string(passwordBytes), dn, *role)
	if err != nil {
		fmt.Printf("Error creating user: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("User created: %s (role: %s, id: %d)\n", user.Username, user.Role, user.ID)
}

func handleCreateOrg() {
	fs := flag.NewFlagSet("create-org", flag.ExitOnError)
	name := fs.String("name", "", "Display name")
	slug := fs.String("slug", "", "Slug (optional; derived from name)")
	_ = fs.Parse(os.Args[3:])
	if *name == "" {
		fmt.Fprintln(os.Stderr, "--name is required")
		os.Exit(2)
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}
	db, err := store.Open(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "db: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()
	s := *slug
	if s == "" {
		s = slugifyCLI(*name)
	}
	var createdBy int64
	if err := db.QueryRow(`SELECT id FROM users WHERE role='admin' ORDER BY id LIMIT 1`).Scan(&createdBy); err != nil {
		fmt.Fprintf(os.Stderr, "need at least one admin user first: %v\n", err)
		os.Exit(1)
	}
	res, err := db.Exec(`INSERT INTO organizations (slug, name, created_by) VALUES (?, ?, ?)`, s, *name, createdBy)
	if err != nil {
		fmt.Fprintf(os.Stderr, "insert org: %v\n", err)
		os.Exit(1)
	}
	id, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO org_members (org_id, user_id, role) VALUES (?, ?, 'owner')`, id, createdBy); err != nil {
		fmt.Fprintf(os.Stderr, "seed owner: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("created org %s (id=%d, slug=%s)\n", *name, id, s)
}

func handleAddMember() {
	fs := flag.NewFlagSet("add-member", flag.ExitOnError)
	orgSlug := fs.String("org", "", "Org slug")
	username := fs.String("user", "", "Username")
	role := fs.String("role", "member", "Role (owner|admin|member)")
	_ = fs.Parse(os.Args[3:])
	if *orgSlug == "" || *username == "" {
		fmt.Fprintln(os.Stderr, "--org and --user are required")
		os.Exit(2)
	}
	switch *role {
	case "owner", "admin", "member":
	default:
		fmt.Fprintf(os.Stderr, "invalid role %q\n", *role)
		os.Exit(2)
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}
	db, err := store.Open(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "db: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	ctx := context.Background()
	q := store.NewQueries(db)
	authz := service.NewAuthz(q, db)
	orgSvc := service.NewOrgService(q, db, authz)

	// Lookup org by slug
	var orgID int64
	if err := db.QueryRow(`SELECT id FROM organizations WHERE slug = ?`, *orgSlug).Scan(&orgID); err != nil {
		fmt.Fprintf(os.Stderr, "org not found: %v\n", err)
		os.Exit(1)
	}

	// Lookup user by username using store.Queries
	u, err := q.GetUserByUsername(ctx, *username)
	if err == sql.ErrNoRows {
		fmt.Fprintf(os.Stderr, "user %q not found\n", *username)
		os.Exit(1)
	} else if err != nil {
		fmt.Fprintf(os.Stderr, "lookup user: %v\n", err)
		os.Exit(1)
	}

	// Find an admin user to use as caller
	var callerID int64
	if err := db.QueryRow(`SELECT id FROM users WHERE role='admin' ORDER BY id LIMIT 1`).Scan(&callerID); err != nil {
		fmt.Fprintf(os.Stderr, "need at least one admin user to add members: %v\n", err)
		os.Exit(1)
	}
	callerLabel := fmt.Sprintf("admin:%d", callerID)

	// Call orgSvc.AddMember with user ID
	if err := orgSvc.AddMember(ctx, callerID, callerLabel, orgID, u.ID, *role); err != nil {
		fmt.Fprintf(os.Stderr, "add-member: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("added %s to %s as %s\n", *username, *orgSlug, *role)
}

// slugifyCLI converts name to a URL-safe ASCII slug. For names that consist
// entirely of non-ASCII characters (e.g. CJK), the ASCII portion would be
// empty; in that case a stable 8-char hex hash of the original name is used
// to produce a unique slug instead of always falling back to "default".
func slugifyCLI(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.ReplaceAll(s, " ", "-")
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
			b = append(b, c)
		}
	}
	if len(b) == 0 {
		sum := sha1.Sum([]byte(name))
		return fmt.Sprintf("org-%x", sum[:4])
	}
	return string(b)
}

func handleMigrateRecover() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}
	shadow := filepath.Join(cfg.DataDir, ".migrate-shadow")
	quar := filepath.Join(cfg.DataDir, ".migrate-quarantine")

	reported := 0
	report := func(dir string) {
		entries, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			return
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", dir, err)
			return
		}
		for _, e := range entries {
			fmt.Printf("  %s\n", filepath.Join(dir, e.Name()))
			reported++
		}
	}

	fmt.Println("shadow dir contents:")
	report(shadow)
	fmt.Println("quarantine dir contents:")
	report(quar)

	if reported == 0 {
		fmt.Println("nothing to recover — migration completed cleanly.")
		return
	}
	fmt.Println("\nTo finish a half-applied migration manually:")
	fmt.Println("  1. Inspect the directories above.")
	fmt.Println("  2. Either move shadow content into its final owner path,")
	fmt.Println("     or move quarantine content back to the legacy location.")
	fmt.Println("  3. Update workspaces.path / repositories.path in the DB to match.")
}

// handleDeleteUser implements: niuniu admin delete-user --username=<name>
// Spec §3.8: checks membership, sole-ownership, and personal resource ownership
// before prompting to delete.
// buildAdminUserService assembles an AdminUserService for CLI use. The
// notify/SSE/agent hooks are left nil (nil-safe): a CLI purge has no live
// clients or PTY sessions to disconnect.
func buildAdminUserService(cfg *config.Config, db *sql.DB) *service.AdminUserService {
	q := store.NewQueries(db)
	authz := service.NewAuthz(q, db)
	projectSvc := service.NewProjectService(q, db, nil, authz)
	repoSvc := service.NewRepositoryService(q, db, authz, cfg.DataDir)
	workspaceSvc := service.NewWorkspaceService(q, db, &cfg.Workspace, cfg.DataDir, nil, authz)
	orgSvc := service.NewOrgService(q, db, authz)
	return service.NewAdminUserService(q, db, cfg.DataDir, projectSvc, workspaceSvc, repoSvc, orgSvc, authz)
}

func handleListUserResources() {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: niuniu admin list-user-resources <username>")
		os.Exit(2)
	}
	username := os.Args[3]

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}
	db, err := store.Open(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "db: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	ctx := context.Background()
	var userID int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM users WHERE username = ?`, username).Scan(&userID); err != nil {
		fmt.Fprintf(os.Stderr, "user %q not found: %v\n", username, err)
		os.Exit(1)
	}

	adminSvc := buildAdminUserService(cfg, db)
	summary, err := adminSvc.ListUserResources(ctx, userID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "list-user-resources: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("User: %s (id=%d, role=%s)\n", summary.User.Username, summary.User.ID, summary.User.Role)
	fmt.Printf("Orgs (%d):\n", len(summary.Orgs))
	for _, o := range summary.Orgs {
		last := ""
		if o.IsLastOwner {
			last = " [LAST OWNER]"
		}
		fmt.Printf("  - %s (slug=%s, role=%s)%s\n", o.Name, o.Slug, o.Role, last)
	}
	fmt.Printf("Projects (%d):\n", len(summary.Projects))
	for _, p := range summary.Projects {
		fmt.Printf("  - #%d %s\n", p.ID, p.Name)
	}
	fmt.Printf("Workspaces (%d):\n", len(summary.Workspaces))
	for _, w := range summary.Workspaces {
		fmt.Printf("  - #%d %s (status=%s)\n", w.ID, w.Name, w.Status)
	}
	fmt.Printf("Repositories (%d):\n", len(summary.Repositories))
	for _, r := range summary.Repositories {
		fmt.Printf("  - #%d %s (%s)\n", r.ID, r.Name, r.Path)
	}
	c := summary.Counts
	fmt.Printf("Counts: env_presets=%d quick_actions=%d agents=%d scenes=%d harness_specs=%d\n",
		c.EnvPresets, c.QuickActions, c.Agents, c.Scenes, c.HarnessSpecs)
}

func handleDeleteUser() {
	fs := flag.NewFlagSet("delete-user", flag.ExitOnError)
	username := fs.String("username", "", "Username to delete (required)")
	purge := fs.Bool("purge", false, "Cascade-delete the user's personal resources and account")
	_ = fs.Parse(os.Args[3:])
	if *username == "" {
		fmt.Fprintln(os.Stderr, "--username is required")
		os.Exit(2)
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}
	db, err := store.Open(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "db: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	ctx := context.Background()

	// 1. Load user by username.
	var userID int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM users WHERE username = ?`, *username).Scan(&userID); err != nil {
		fmt.Fprintf(os.Stderr, "user %q not found: %v\n", *username, err)
		os.Exit(1)
	}

	// --purge: cascade-delete resources + account via AdminUserService.
	if *purge {
		fmt.Printf("PURGE user %q (id=%d) and ALL their personal resources? [y/N] ", *username, userID)
		scanner := bufio.NewScanner(os.Stdin)
		scanner.Scan()
		answer := strings.TrimSpace(strings.ToLower(scanner.Text()))
		if answer != "y" && answer != "yes" {
			fmt.Println("aborted.")
			os.Exit(0)
		}
		adminSvc := buildAdminUserService(cfg, db)
		// actorID=0 (system): never equals a real userID, so the self-purge guard
		// never trips on the CLI path.
		summary, err := adminSvc.PurgeUser(ctx, 0, userID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "purge: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("purged user %q (id=%d): projects=%d workspaces=%d repositories=%d "+
			"env_presets=%d quick_actions=%d agents=%d scenes=%d orgs_left=%d\n",
			*username, userID, summary.Projects, summary.Workspaces, summary.Repositories,
			summary.EnvPresets, summary.QuickActions, summary.Agents, summary.Scenes, summary.OrgsLeft)
		// Note: the on-disk ~/.niuniu/users/<id> tree is removed by a background
		// goroutine inside PurgeUser; on the short-lived CLI process it may not
		// finish before exit. The DB purge (the authoritative state) is committed
		// synchronously above; a leftover directory can be removed manually.
		return
	}

	// 2. Refuse if user is a member of any org.
	var memberCount int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM org_members WHERE user_id = ?`, userID).Scan(&memberCount); err != nil {
		fmt.Fprintf(os.Stderr, "membership check: %v\n", err)
		os.Exit(1)
	}
	if memberCount > 0 {
		rows, err := db.QueryContext(ctx,
			`SELECT o.slug FROM organizations o JOIN org_members m ON o.id = m.org_id WHERE m.user_id = ?`, userID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "list orgs: %v\n", err)
			os.Exit(1)
		}
		var slugs []string
		for rows.Next() {
			var slug string
			if err := rows.Scan(&slug); err == nil {
				slugs = append(slugs, slug)
			}
		}
		rows.Close()
		fmt.Fprintf(os.Stderr, "error: user %q is a member of org(s): %s\n", *username, strings.Join(slugs, ", "))
		fmt.Fprintln(os.Stderr, "remove the user from all orgs first (admin remove-member).")
		os.Exit(1)
	}

	// 3. Refuse if user is the sole owner of any org.
	// We already know memberCount == 0 so this check is satisfied, but check via CountOrgOwners
	// on any org where they were previously listed as owner (belt-and-suspenders):
	// memberCount == 0 means no org memberships at all, so no sole-ownership possible.
	// (Kept as comment for clarity; skip redundant query.)

	// 4. Refuse if user is owner_id of any personal-scoped top-level resource.
	ownedTables := []string{
		"projects", "repositories", "workspaces",
		"env_presets", "quick_actions",
		"harnesses", "harness_specs", "teams", "agents",
	}
	for _, tbl := range ownedTables {
		var dummy int64
		err := db.QueryRowContext(ctx,
			fmt.Sprintf(`SELECT 1 FROM %s WHERE owner_type='user' AND owner_id=? LIMIT 1`, tbl),
			userID).Scan(&dummy)
		if err == nil {
			fmt.Fprintf(os.Stderr, "error: user %q owns personal resources in table %q\n", *username, tbl)
			fmt.Fprintln(os.Stderr, "transfer or delete those resources first.")
			os.Exit(1)
		}
	}

	// 5. Prompt confirmation.
	fmt.Printf("delete user %q (id=%d)? [y/N] ", *username, userID)
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Scan()
	answer := strings.TrimSpace(strings.ToLower(scanner.Text()))
	if answer != "y" && answer != "yes" {
		fmt.Println("aborted.")
		os.Exit(0)
	}

	// 6. Delete.
	if _, err := db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, userID); err != nil {
		fmt.Fprintf(os.Stderr, "delete user: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("user %q (id=%d) deleted.\n", *username, userID)
}

// handleTransferResource implements:
// niuniu admin transfer-resource --type=<type> --id=<id> --to=<user:alice|org:acme>
func handleTransferResource() {
	fs := flag.NewFlagSet("transfer-resource", flag.ExitOnError)
	resType := fs.String("type", "", "Resource type: workspace|project|repository|harness|env_preset|quick_action|team|agent")
	resID := fs.Int64("id", 0, "Resource ID")
	toRef := fs.String("to", "", "Target owner: user:<username> or org:<slug>")
	_ = fs.Parse(os.Args[3:])

	if *resType == "" || *resID == 0 || *toRef == "" {
		fmt.Fprintln(os.Stderr, "--type, --id, and --to are required")
		os.Exit(2)
	}

	// Validate resource type.
	validTypes := map[string]string{
		"workspace":   "workspaces",
		"project":     "projects",
		"repository":  "repositories",
		"harness":     "harnesses",
		"env_preset":  "env_presets",
		"quick_action": "quick_actions",
		"team":        "teams",
		"agent":       "agents",
	}
	tableName, ok := validTypes[*resType]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown resource type %q; valid: workspace|project|repository|harness|env_preset|quick_action|team|agent\n", *resType)
		os.Exit(2)
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}
	db, err := store.Open(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "db: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	ctx := context.Background()

	// 1. Parse --to into target OwnerRef.
	parts := strings.SplitN(*toRef, ":", 2)
	if len(parts) != 2 || (parts[0] != "user" && parts[0] != "org") {
		fmt.Fprintln(os.Stderr, "--to must be user:<username> or org:<slug>")
		os.Exit(2)
	}
	toOwnerType := parts[0]
	toOwnerName := parts[1]

	var toOwnerID int64
	switch toOwnerType {
	case "user":
		if err := db.QueryRowContext(ctx, `SELECT id FROM users WHERE username = ?`, toOwnerName).Scan(&toOwnerID); err != nil {
			fmt.Fprintf(os.Stderr, "target user %q not found: %v\n", toOwnerName, err)
			os.Exit(1)
		}
	case "org":
		if err := db.QueryRowContext(ctx, `SELECT id FROM organizations WHERE slug = ?`, toOwnerName).Scan(&toOwnerID); err != nil {
			fmt.Fprintf(os.Stderr, "target org %q not found: %v\n", toOwnerName, err)
			os.Exit(1)
		}
	}
	toRef2 := service.OwnerRef{Type: toOwnerType, ID: toOwnerID}

	// 2. Load resource's current owner.
	var fromOwnerType string
	var fromOwnerID int64
	err = db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT owner_type, owner_id FROM %s WHERE id = ?`, tableName),
		*resID).Scan(&fromOwnerType, &fromOwnerID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s id=%d not found: %v\n", *resType, *resID, err)
		os.Exit(1)
	}

	fromLabel := fromOwnerType + ":" + fmt.Sprintf("%d", fromOwnerID)
	toLabel := *toRef

	// 3. Prompt.
	fmt.Printf("Transfer %s id=%d from %s to %s? [y/N] ", *resType, *resID, fromLabel, toLabel)
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Scan()
	answer := strings.TrimSpace(strings.ToLower(scanner.Text()))
	if answer != "y" && answer != "yes" {
		fmt.Println("aborted.")
		os.Exit(0)
	}

	// 4. Perform transfer.
	needsFilesystemMove := *resType == "workspace" || *resType == "repository"

	if needsFilesystemMove {
		// DB-only transfer with warning about disk content.
		// Full filesystem move (BuildShadow + AtomicSwap) is complex to wire here
		// without a live DB+session context; scope-cut to DB-only with clear warning.
		var oldPath string
		pathCol := "path"
		err = db.QueryRowContext(ctx,
			fmt.Sprintf(`SELECT %s FROM %s WHERE id = ?`, pathCol, tableName),
			*resID).Scan(&oldPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load path: %v\n", err)
			os.Exit(1)
		}

		fromRef := service.OwnerRef{Type: fromOwnerType, ID: fromOwnerID}
		var newPath string
		switch *resType {
		case "workspace":
			newPath = toRef2.WorkspacePath(cfg.DataDir, *resID)
		case "repository":
			newPath = toRef2.RepositoryPath(cfg.DataDir, *resID)
		}

		// Update DB only.
		_, err = db.ExecContext(ctx,
			fmt.Sprintf(`UPDATE %s SET owner_type = ?, owner_id = ?, path = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, tableName),
			toOwnerType, toOwnerID, newPath, *resID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "update db: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("DB updated: %s id=%d owner set to %s.\n", *resType, *resID, toLabel)
		fmt.Printf("WARN: disk content will need manual move:\n")
		fmt.Printf("  from: %s\n", oldPath)
		fmt.Printf("  to:   %s\n", newPath)
		fmt.Printf("  (fromRef=%s/%d path unchanged on disk)\n", fromRef.Type, fromRef.ID)
	} else {
		// Non-filesystem resources: just UPDATE owner columns.
		_, err = db.ExecContext(ctx,
			fmt.Sprintf(`UPDATE %s SET owner_type = ?, owner_id = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, tableName),
			toOwnerType, toOwnerID, *resID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "update db: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("transferred %s id=%d from %s to %s.\n", *resType, *resID, fromLabel, toLabel)
	}

	// After owner UPDATE succeeded, cleanup project_repositories that no
	// longer satisfy owner consistency. Best-effort; log failures but don't
	// fail the transfer (the defensive list-filter still hides residue).
	if *resType == "project" || *resType == "repository" {
		q := store.NewQueries(db)
		svc := service.NewProjectService(q, db, nil, nil)
		var removed int64
		var cerr error
		switch *resType {
		case "project":
			removed, cerr = svc.CleanupCrossOwnerRepositoriesByProject(ctx, *resID, toOwnerType, toOwnerID)
		case "repository":
			removed, cerr = svc.CleanupCrossOwnerRepositoriesByRepo(ctx, *resID, toOwnerType, toOwnerID)
		}
		if cerr != nil {
			fmt.Fprintf(os.Stderr, "warning: cross-owner cleanup failed: %v\n", cerr)
		} else if removed > 0 {
			fmt.Printf("cleaned up %d cross-owner project_repositories rows\n", removed)
		}
	}
}
