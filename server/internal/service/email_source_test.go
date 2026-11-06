// External tests for the IMAP poller: the cursor lifecycle, the read-only
// transport contract, backoff, and UIDVALIDITY resets — driven by a fake
// fetcher so no real mail server is involved.
package service_test

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/niuniu-dev/niuniu/internal/integration"
	"github.com/niuniu-dev/niuniu/internal/integration/crypto"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
	niutest "github.com/niuniu-dev/niuniu/internal/testing"
)

type fakeIMAPFetcher struct {
	mu      sync.Mutex
	cursors []service.IMAPCursor
	mailbox string
	cfg     service.IMAPConfig
	result  service.IMAPFetch
	err     error
}

func (f *fakeIMAPFetcher) Fetch(_ context.Context, cfg service.IMAPConfig, mailbox string, cursor service.IMAPCursor) (service.IMAPFetch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cursors = append(f.cursors, cursor)
	f.mailbox = mailbox
	f.cfg = cfg
	return f.result, f.err
}

func (f *fakeIMAPFetcher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.cursors)
}

func (f *fakeIMAPFetcher) lastCursor() service.IMAPCursor {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.cursors) == 0 {
		return service.IMAPCursor{}
	}
	return f.cursors[len(f.cursors)-1]
}

type emailPollFixture struct {
	env   *niutest.IsolationEnv
	svc   *service.EmailSourceService
	q     *store.Queries
	proj  store.Project
	bind  store.ProjectExternalSource
	fetch *fakeIMAPFetcher
}

func newEmailPollFixture(t *testing.T) *emailPollFixture {
	t.Helper()
	env := niutest.NewIsolationEnv(t)
	kr, err := crypto.LoadOrCreate(env.TempPath(t, "integration_secret"))
	require.NoError(t, err)
	creds := service.NewExternalCredentialService(env.Queries(), env.DB, kr, integration.NewRegistry())

	ctx := context.Background()
	proj := env.NewProject(t, env.UserA, "email-poll")
	cred, err := creds.Create(ctx, service.ExternalCredentialUpsertInput{
		OwnerType: "user",
		OwnerID:   env.UserA,
		UserID:    env.UserA,
		Provider:  integration.ProviderName("imap"),
		Alias:     "poll-inbox",
		RawConfig: map[string]any{
			"imap_host": "imap.example.com",
			"imap_port": 993,
			"security":  "ssl",
			"username":  "bot@example.com",
			"password":  "secret",
		},
	})
	require.NoError(t, err)

	bind, err := env.Queries().AddProjectExternalSource(ctx, store.AddProjectExternalSourceParams{
		ProjectID:    proj.ID,
		Provider:     "email",
		SourceKey:    "inbox",
		CredentialID: sql.NullInt64{Int64: cred.ID, Valid: true},
		Config:       `{"mode":"imap","mailbox":"INBOX","ignore_auto":true}`,
	})
	require.NoError(t, err)

	svc := service.NewEmailSourceService(env.DB, env.Queries(), creds)
	fake := &fakeIMAPFetcher{}
	svc.SetFetcher(fake)
	return &emailPollFixture{env: env, svc: svc, q: env.Queries(), proj: proj, bind: bind, fetch: fake}
}

func (f *emailPollFixture) state(t *testing.T) store.EmailSourcePollState {
	t.Helper()
	st, err := f.q.GetEmailPollState(context.Background(), store.GetEmailPollStateParams{
		BindingID: f.bind.ID, Mailbox: "INBOX",
	})
	require.NoError(t, err)
	return st
}

func (f *emailPollFixture) eventCount(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, f.env.DB.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM intake_events WHERE project_id = ?`, f.proj.ID).Scan(&n))
	return n
}

func rawMail(messageID, from, subject, body string) []byte {
	var b strings.Builder
	headers := []string{
		"Message-ID: <" + messageID + ">",
		"From: " + from,
		"Subject: " + subject,
		"Content-Type: text/plain; charset=utf-8",
	}
	for _, h := range headers {
		b.WriteString(h + "\r\n")
	}
	b.WriteString("\r\n" + body)
	return []byte(b.String())
}

func TestEmailPollBindingReadsNewMailAndAdvancesCursor(t *testing.T) {
	f := newEmailPollFixture(t)
	ctx := context.Background()

	f.fetch.result = service.IMAPFetch{
		UIDValidity: 5,
		Messages: []service.FetchedMessage{
			{UID: 10, Raw: rawMail("root@example.com", "Alice <alice@example.com>", "Deploy failed", "please look")},
			{UID: 11, Raw: rawMail("second@example.com", "Bob <bob@example.com>", "Second", "hello")},
		},
	}

	require.NoError(t, f.svc.PollBinding(ctx, f.bind.ID))

	// The credential was decrypted and mapped onto the connection settings.
	require.Equal(t, "imap.example.com", f.fetch.cfg.Host)
	require.Equal(t, "bot@example.com", f.fetch.cfg.Username)
	require.Equal(t, 993, f.fetch.cfg.Port)
	require.Equal(t, "INBOX", f.fetch.mailbox)

	// First poll has no cursor, so it asks for the recent window rather than
	// the whole mailbox.
	first := f.fetch.lastCursor()
	require.Equal(t, uint32(0), first.LastUID)
	require.WithinDuration(t, time.Now().Add(-24*time.Hour), first.ResetSince, time.Minute)

	var (
		kind, ref, eventExtID, threadExtID, actor string
	)
	require.NoError(t, f.env.DB.QueryRowContext(ctx, `
		SELECT source_kind, source_ref, event_ext_id, thread_ext_id, actor_ext_id
		FROM intake_events WHERE project_id = ? ORDER BY id LIMIT 1`, f.proj.ID).
		Scan(&kind, &ref, &eventExtID, &threadExtID, &actor))
	require.Equal(t, "email", kind)
	require.Equal(t, "inbox/"+sqlID(f.bind.ID), ref)
	require.Equal(t, "root@example.com", eventExtID)
	require.Equal(t, "root@example.com", threadExtID)
	require.Equal(t, "alice@example.com", actor)
	require.Equal(t, 2, f.eventCount(t))

	// The cursor now points past both messages, so the next poll asks for
	// only what arrived since.
	st := f.state(t)
	require.EqualValues(t, 5, st.UidValidity)
	require.EqualValues(t, 11, st.LastUid)

	f.fetch.result = service.IMAPFetch{UIDValidity: 5}
	require.NoError(t, f.svc.PollBinding(ctx, f.bind.ID))
	require.EqualValues(t, 11, f.fetch.lastCursor().LastUID)
	require.EqualValues(t, 5, f.fetch.lastCursor().UIDValidity)
	require.Equal(t, 2, f.eventCount(t), "no new mail means no new events")
}

func TestEmailPollBindingIgnoresAutoMailButAdvancesCursor(t *testing.T) {
	f := newEmailPollFixture(t)
	ctx := context.Background()

	auto := []byte("Message-ID: <auto@example.com>\r\n" +
		"From: Auto <auto@example.com>\r\n" +
		"Auto-Submitted: auto-replied\r\n" +
		"Subject: Out of office\r\n" +
		"Content-Type: text/plain\r\n\r\nI am away")
	f.fetch.result = service.IMAPFetch{
		UIDValidity: 5,
		Messages:    []service.FetchedMessage{{UID: 12, Raw: auto}},
	}

	require.NoError(t, f.svc.PollBinding(ctx, f.bind.ID))
	require.Equal(t, 0, f.eventCount(t), "auto-replies must not create tasks")
	require.EqualValues(t, 12, f.state(t).LastUid, "the cursor still moves past it")
}

func TestEmailPollBindingRecordsAuthFailureAndBacksOff(t *testing.T) {
	f := newEmailPollFixture(t)
	ctx := context.Background()

	f.fetch.err = &service.ErrIMAPAuth{Detail: "LOGIN failed"}
	err := f.svc.PollBinding(ctx, f.bind.ID)
	require.Error(t, err)

	st := f.state(t)
	require.True(t, st.BackoffUntil.Valid)
	require.True(t, st.BackoffUntil.Time.After(time.Now()))
	require.Contains(t, st.LastError, "login rejected")

	// A second tick inside the backoff window must not touch the server.
	calls := f.fetch.callCount()
	f.fetch.err = nil
	require.NoError(t, f.svc.PollBinding(ctx, f.bind.ID))
	require.Equal(t, calls, f.fetch.callCount(), "backoff should skip the fetch")
}

func TestEmailPollBindingResetsCursorOnUIDValidityChange(t *testing.T) {
	f := newEmailPollFixture(t)
	ctx := context.Background()

	// Pretend the mailbox was already being polled in an older generation.
	_, err := f.q.UpsertEmailPollState(ctx, store.UpsertEmailPollStateParams{
		ProjectID: f.proj.ID, BindingID: f.bind.ID, Mailbox: "INBOX",
		UidValidity: 5, LastUid: 100,
	})
	require.NoError(t, err)

	f.fetch.result = service.IMAPFetch{
		UIDValidity: 9,
		Reset:       true,
		Messages: []service.FetchedMessage{
			{UID: 3, Raw: rawMail("new@example.com", "Alice <alice@example.com>", "After reset", "body")},
		},
	}
	require.NoError(t, f.svc.PollBinding(ctx, f.bind.ID))

	require.EqualValues(t, 5, f.fetch.lastCursor().UIDValidity)
	require.EqualValues(t, 100, f.fetch.lastCursor().LastUID)

	st := f.state(t)
	require.EqualValues(t, 9, st.UidValidity)
	require.EqualValues(t, 3, st.LastUid, "a new generation restarts the cursor")

	// The event is still delivered: the UIDVALIDITY change itself does not
	// discard recent mail.
	require.Equal(t, 1, f.eventCount(t))
}

func TestEmailPollBindingRejectsNonIMAPBinding(t *testing.T) {
	f := newEmailPollFixture(t)
	_, err := f.env.DB.Exec(`UPDATE project_external_sources SET provider = 'webhook' WHERE id = ?`, f.bind.ID)
	require.NoError(t, err)

	err = f.svc.PollBinding(context.Background(), f.bind.ID)
	require.True(t, errors.Is(err, service.ErrIntakeSourceNotFound), "got %v", err)
}

func TestEmailPollBindingSkipsOverlappingPoll(t *testing.T) {
	f := newEmailPollFixture(t)
	block := make(chan struct{})
	started := make(chan struct{})
	blocking := &blockingFetcher{started: started, block: block}
	f.svc.SetFetcher(blocking)

	done := make(chan error, 1)
	go func() { done <- f.svc.PollBinding(context.Background(), f.bind.ID) }()
	<-started

	// While the first poll is in flight, a second one is a no-op.
	require.NoError(t, f.svc.PollBinding(context.Background(), f.bind.ID))
	close(block)
	require.NoError(t, <-done)
}

type blockingFetcher struct {
	started chan struct{}
	block   chan struct{}
	once    sync.Once
}

func (b *blockingFetcher) Fetch(context.Context, service.IMAPConfig, string, service.IMAPCursor) (service.IMAPFetch, error) {
	b.once.Do(func() { close(b.started) })
	<-b.block
	return service.IMAPFetch{}, nil
}

func sqlID(id int64) string { return strconv.FormatInt(id, 10) }
