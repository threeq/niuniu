package service

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/niuniu-dev/niuniu/internal/store"

	_ "modernc.org/sqlite"
)

// openAskUserTestDB mirrors openPermissionTestDB — an in-memory SQLite with
// the full schema applied so tests exercise the real CRUD path.
func openAskUserTestDB(t *testing.T) *sql.DB {
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

// awaitAskUserRequest blocks until an ask_user_request event lands on the
// bus, then returns the embedded payload. Fails the test on 1s timeout.
func awaitAskUserRequest(t *testing.T, bus *event.Bus) *event.AskUserRequestData {
	t.Helper()
	ch := bus.Subscribe()
	defer bus.Unsubscribe(ch)
	deadline := time.After(time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Type == event.EventAskUserRequest && ev.AskUserRequest != nil {
				return ev.AskUserRequest
			}
		case <-deadline:
			t.Fatalf("no ask_user_request event within 1s")
			return nil
		}
	}
}

// sampleQuestions builds a minimal valid questions array for tests.
func sampleQuestions() []event.AskUserQuestion {
	return []event.AskUserQuestion{
		{
			Question:    "Pick a color",
			Header:      "Color preference",
			MultiSelect: false,
			Options: []event.AskUserQuestionOption{
				{Label: "red"},
				{Label: "blue"},
			},
		},
	}
}

func TestAskUserService_RequestAndDecide(t *testing.T) {
	rawDB := openAskUserTestDB(t)
	bus := event.NewBus()
	svc := NewAskUserService(rawDB, bus, nil, time.Hour)
	wsID := mustWorkspace(t, rawDB)

	type res struct {
		resp AskUserResult
		err  error
	}
	out := make(chan res, 1)
	go func() {
		r, err := svc.Request(context.Background(), wsID, "user", 1, "sess", sampleQuestions())
		out <- res{r, err}
	}()

	data := awaitAskUserRequest(t, bus)
	answers := []AskUserAnswer{{Question: "Pick a color", Labels: []string{"red"}}}
	if err := svc.Decide(context.Background(), data.RequestID, AskUserDecision{
		Answers: answers,
		UserID:  7,
	}); err != nil {
		t.Fatalf("Decide: %v", err)
	}

	select {
	case r := <-out:
		if r.err != nil {
			t.Fatalf("Request err: %v", r.err)
		}
		if !r.resp.Answered {
			t.Errorf("Answered=false, want true")
		}
		if len(r.resp.Answers) != 1 || r.resp.Answers[0].Labels[0] != "red" {
			t.Errorf("unexpected answers: %+v", r.resp.Answers)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Request did not unblock")
	}

	// Persisted row reaches status='answered' and answers_json is set.
	var status, src string
	var answersJSON sql.NullString
	err := rawDB.QueryRowContext(context.Background(),
		`SELECT status, decision_source, answers_json FROM agent_ask_user_requests WHERE workspace_id=? ORDER BY id DESC LIMIT 1`,
		wsID,
	).Scan(&status, &src, &answersJSON)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if status != "answered" || src != "user" {
		t.Errorf("got status=%s source=%s; want answered/user", status, src)
	}
	if !answersJSON.Valid || answersJSON.String == "" {
		t.Errorf("answers_json should be persisted, got %v", answersJSON)
	}
}

// TestAskUserService_ProjectsWaitingInputThenRestores verifies the spec §19
// waiting-user-input projection: the linked issue is flipped to 'waiting_input'
// while the question is open and back to 'running' once it is answered.
func TestAskUserService_ProjectsWaitingInputThenRestores(t *testing.T) {
	rawDB := openAskUserTestDB(t)
	bus := event.NewBus()
	svc := NewAskUserService(rawDB, bus, nil, time.Hour)
	wsID := mustWorkspace(t, rawDB)

	var mu sync.Mutex
	var got []string
	svc.SetIssueExecProjector(func(_ context.Context, ws int64, status, _ string) {
		if ws == wsID {
			mu.Lock()
			got = append(got, status)
			mu.Unlock()
		}
	})

	out := make(chan struct{}, 1)
	go func() {
		_, _ = svc.Request(context.Background(), wsID, "user", 1, "sess", sampleQuestions())
		out <- struct{}{}
	}()

	data := awaitAskUserRequest(t, bus)
	if err := svc.Decide(context.Background(), data.RequestID, AskUserDecision{
		Answers: []AskUserAnswer{{Question: "Pick a color", Labels: []string{"red"}}}, UserID: 7,
	}); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	select {
	case <-out:
	case <-time.After(2 * time.Second):
		t.Fatal("Request did not unblock")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) < 2 || got[0] != "waiting_input" || got[len(got)-1] != "running" {
		t.Errorf("projection sequence = %v, want first=waiting_input last=running", got)
	}
}

func TestAskUserService_DecideTwice_AlreadyDecided(t *testing.T) {
	rawDB := openAskUserTestDB(t)
	bus := event.NewBus()
	svc := NewAskUserService(rawDB, bus, nil, time.Hour)
	wsID := mustWorkspace(t, rawDB)

	go svc.Request(context.Background(), wsID, "user", 1, "s", sampleQuestions())
	data := awaitAskUserRequest(t, bus)

	answers := []AskUserAnswer{{Question: "Pick a color", Labels: []string{"red"}}}
	if err := svc.Decide(context.Background(), data.RequestID, AskUserDecision{Answers: answers, UserID: 7}); err != nil {
		t.Fatalf("first Decide: %v", err)
	}
	err := svc.Decide(context.Background(), data.RequestID, AskUserDecision{Answers: answers, UserID: 8})
	if err == nil || !errors.Is(err, ErrAskUserAlreadyDecided) {
		t.Errorf("second Decide: got %v want ErrAskUserAlreadyDecided", err)
	}
}

func TestAskUserService_Timeout(t *testing.T) {
	rawDB := openAskUserTestDB(t)
	bus := event.NewBus()
	// 50ms timeout — fast enough for a unit test.
	svc := NewAskUserService(rawDB, bus, nil, 50*time.Millisecond)
	wsID := mustWorkspace(t, rawDB)

	resp, err := svc.Request(context.Background(), wsID, "user", 1, "s", sampleQuestions())
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if resp.Answered || resp.Reason != "timeout" {
		t.Errorf("got %+v, want Answered=false reason=timeout", resp)
	}

	// Persisted row should be marked timeout.
	var status string
	_ = rawDB.QueryRowContext(context.Background(),
		`SELECT status FROM agent_ask_user_requests WHERE workspace_id=? ORDER BY id DESC LIMIT 1`,
		wsID,
	).Scan(&status)
	if status != "timeout" {
		t.Errorf("got status=%s, want timeout", status)
	}
}

func TestAskUserService_CancelByWorkspace_UnblocksRequest(t *testing.T) {
	rawDB := openAskUserTestDB(t)
	bus := event.NewBus()
	svc := NewAskUserService(rawDB, bus, nil, time.Hour)
	wsID := mustWorkspace(t, rawDB)

	out := make(chan AskUserResult, 1)
	go func() {
		r, _ := svc.Request(context.Background(), wsID, "user", 1, "s", sampleQuestions())
		out <- r
	}()
	awaitAskUserRequest(t, bus)

	if err := svc.CancelByWorkspace(context.Background(), wsID, "session_end"); err != nil {
		t.Fatalf("CancelByWorkspace: %v", err)
	}

	select {
	case r := <-out:
		if r.Answered || r.Reason != "cancelled" {
			t.Errorf("got %+v, want cancelled", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Request did not unblock after CancelByWorkspace")
	}
}

func TestAskUserService_OnStartup_CancelsPending(t *testing.T) {
	rawDB := openAskUserTestDB(t)
	bus := event.NewBus()
	svc := NewAskUserService(rawDB, bus, nil, time.Hour)
	wsID := mustWorkspace(t, rawDB)

	for i := 0; i < 2; i++ {
		_, err := rawDB.ExecContext(context.Background(),
			`INSERT INTO agent_ask_user_requests
             (workspace_id, owner_type, owner_id, session_id, questions_json, status, expires_at)
             VALUES (?, 'user', 1, 's', '[]', 'pending', ?)`,
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
		`SELECT COUNT(*) FROM agent_ask_user_requests WHERE status='pending'`,
	).Scan(&n)
	if n != 0 {
		t.Errorf("got %d pending after OnStartup, want 0", n)
	}
}

func TestAskUserService_Request_EmptyQuestions(t *testing.T) {
	rawDB := openAskUserTestDB(t)
	bus := event.NewBus()
	svc := NewAskUserService(rawDB, bus, nil, time.Hour)
	wsID := mustWorkspace(t, rawDB)

	_, err := svc.Request(context.Background(), wsID, "user", 1, "s", nil)
	if err == nil {
		t.Errorf("expected error for empty questions, got nil")
	}
}
