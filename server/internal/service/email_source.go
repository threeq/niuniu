package service

// Email Source, IMAP transport (design §6.2).
//
// The personal edition has no public endpoint, so mail is pulled: every tick
// the poller logs into the bound mailbox, fetches what arrived since its cursor,
// and feeds the messages through the same Intake pipeline as every other
// Source. Two properties are load-bearing:
//
//   - READ-ONLY. Fetch uses BODY.PEEK[] and never STOREs flags, deletes or
//     moves anything. A binding must not change what the user sees in their
//     mail client.
//   - RESUMABLE. Progress is a (UIDVALIDITY, UID) cursor in
//     email_source_poll_state. A reply carries the original Message-ID in its
//     References/In-Reply-To, so it resolves to the task the root created and
//     resumes it.

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/niuniu-dev/niuniu/internal/intake"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/niuniu-dev/niuniu/internal/webhooksource"
)

const (
	emailProviderName  = "email"
	emailIMAPModeValue = "imap"
	emailMailboxINBOX  = "INBOX"

	emailDialTimeout = 20 * time.Second
	emailBackoffBase = time.Minute
	emailBackoffMax  = 30 * time.Minute
	emailResetWindow = 24 * time.Hour
	emailBatchLimit  = 200
)

// IMAPConfig is the resolved connection settings of one imap credential.
type IMAPConfig struct {
	Host     string
	Port     int
	Security string
	Username string
	Password string
}

// IMAPCursor is the poller's read position. UIDValidity is the mailbox's own
// generation counter: when it changes, LastUID means nothing any more and the
// fetcher must fall back to the ResetSince window.
type IMAPCursor struct {
	UIDValidity uint32
	LastUID     uint32
	ResetSince  time.Time
}

// IMAPFetch is one read-only fetch result.
type IMAPFetch struct {
	UIDValidity uint32
	// Reset reports that the server's UIDVALIDITY differed from the cursor, so
	// Messages is the recent ResetSince window instead of new mail.
	Reset    bool
	Messages []FetchedMessage
}

// FetchedMessage is one raw RFC 5322 message.
type FetchedMessage struct {
	UID uint32
	Raw []byte
}

// IMAPFetcher is the transport seam. The production implementation is
// goIMAPFetcher; tests substitute a fake.
//
// Implementations MUST fetch with BODY.PEEK[] (never BODY[]) so the mailbox's
// \Seen flags stay untouched, must not issue STORE/EXPUNGE/MOVE, and must
// return Messages sorted by UID ascending.
type IMAPFetcher interface {
	Fetch(ctx context.Context, cfg IMAPConfig, mailbox string, cursor IMAPCursor) (IMAPFetch, error)
}

// EmailSourceService polls the imap bindings and runs their messages through
// the Intake pipeline.
type EmailSourceService struct {
	db      *store.DB
	q       *store.Queries
	creds   *ExternalCredentialService
	intake  *intake.Service
	fetcher IMAPFetcher
	now     func() time.Time

	mu      sync.Mutex
	running map[int64]bool

	delivery *DeliveryHealthTracker

	dispatchMu sync.RWMutex
	dispatch   TaskRouter
	deliverer  MessageDeliverer
}

// NewEmailSourceService builds the poller and its intake pipeline. It performs
// no network I/O until RunOnce or PollBinding is called.
func NewEmailSourceService(rawDB *sql.DB, q *store.Queries, creds *ExternalCredentialService) *EmailSourceService {
	s := &EmailSourceService{
		db:      store.Wrap(rawDB),
		q:       q,
		creds:   creds,
		fetcher: goIMAPFetcher{},
		now:     time.Now,
		running: make(map[int64]bool),
	}
	s.intake = intake.NewService(intake.Deps{
		Enabled:  true,
		Store:    imbotIntakeStore{q: q},
		Policies: imbotPolicy{q: q},
		Resolver: emailResolver{db: s.db},
		Executor: emailExecutor{svc: s},
	})
	return s
}

// SetFetcher replaces the transport (tests).
func (s *EmailSourceService) SetFetcher(f IMAPFetcher) { s.fetcher = f }

// SetDeliveryHealth wires the shared delivery tracker (server DI), so an IMAP
// login rejection raises an immediate alert instead of a silent backoff.
func (s *EmailSourceService) SetDeliveryHealth(t *DeliveryHealthTracker) { s.delivery = t }

func (s *EmailSourceService) recordDelivery(binding store.ProjectExternalSource, outcome DeliveryOutcome) {
	if s.delivery != nil {
		s.delivery.Record(binding.ID, binding.ProjectID, emailProviderName, outcome)
	}
}

// SetInbound wires the task router and agent deliverer, mirroring the other
// Source services so an accepted email creates a real issue + workspace.
func (s *EmailSourceService) SetInbound(dispatch TaskRouter, deliverer MessageDeliverer) {
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	s.dispatch = dispatch
	s.deliverer = deliverer
}

func (s *EmailSourceService) router() TaskRouter {
	s.dispatchMu.RLock()
	defer s.dispatchMu.RUnlock()
	return s.dispatch
}

func (s *EmailSourceService) messageDeliverer() MessageDeliverer {
	s.dispatchMu.RLock()
	defer s.dispatchMu.RUnlock()
	return s.deliverer
}

// RunOnce polls every imap binding once. One binding's failure never stops the
// others, and overlapping ticks are skipped per binding.
func (s *EmailSourceService) RunOnce(ctx context.Context) {
	if s == nil {
		return
	}
	bindings, err := s.imapBindings(ctx)
	if err != nil {
		slog.Warn("email source: load bindings failed", "error", err)
		return
	}
	for _, b := range bindings {
		if ctx.Err() != nil {
			return
		}
		if err := s.PollBinding(ctx, b.ID); err != nil {
			slog.Warn("email source: poll failed", "binding", b.ID, "error", err)
		}
	}
}

// imapBindings lists the email bindings in imap mode. The mode lives inside
// config JSON, so it is filtered here rather than in SQL.
func (s *EmailSourceService) imapBindings(ctx context.Context) ([]store.ProjectExternalSource, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, project_id, provider, source_key, credential_id, config, created_at
		FROM project_external_sources
		WHERE LOWER(provider) = ?
		ORDER BY id`, emailProviderName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]store.ProjectExternalSource, 0)
	for rows.Next() {
		var r store.ProjectExternalSource
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.Provider, &r.SourceKey,
			&r.CredentialID, &r.Config, &r.CreatedAt); err != nil {
			return nil, err
		}
		if configString(r.Config, "mode") == emailIMAPModeValue {
			out = append(out, r)
		}
	}
	return out, rows.Err()
}

// PollBinding runs one poll for one binding. An overlapping call for the same
// binding is skipped (it is a normal no-op, not a failure); transport failures
// are recorded as a backoff and returned.
func (s *EmailSourceService) PollBinding(ctx context.Context, bindingID int64) error {
	if s == nil || s.fetcher == nil {
		return ErrIntakeSourceEmailPollerOff
	}
	if !s.acquire(bindingID) {
		return nil
	}
	defer s.release(bindingID)

	binding, err := s.loadIMAPBinding(ctx, bindingID)
	if err != nil {
		return err
	}
	mailbox := configString(binding.Config, "mailbox")
	if mailbox == "" {
		mailbox = emailMailboxINBOX
	}

	state, err := s.loadPollState(ctx, binding, mailbox)
	if err != nil {
		return err
	}
	now := s.now()
	if state.BackoffUntil.Valid && state.BackoffUntil.Time.After(now) {
		return nil // still backing off
	}

	cfg, err := s.imapConfig(ctx, binding)
	if err != nil {
		s.recordFailure(ctx, binding, mailbox, state, now, err)
		s.recordDelivery(binding, DeliveryTransportFailed)
		return err
	}

	fetch, err := s.fetcher.Fetch(ctx, cfg, mailbox, IMAPCursor{
		UIDValidity: uint32(state.UidValidity),
		LastUID:     uint32(state.LastUid),
		ResetSince:  now.Add(-emailResetWindow),
	})
	if err != nil {
		s.recordFailure(ctx, binding, mailbox, state, now, err)
		s.recordDelivery(binding, imapFailureOutcome(err))
		return fmt.Errorf("email source: fetch %s: %w", binding.SourceKey, err)
	}
	if fetch.Reset {
		slog.Info("email source: uidvalidity changed, rescanning recent window",
			"binding", binding.ID, "mailbox", mailbox,
			"was", state.UidValidity, "now", fetch.UIDValidity)
	}

	ignoreAuto := configBool(binding.Config, "ignore_auto", true)
	// A changed UIDVALIDITY (or the first poll) starts a fresh generation: the
	// old last_uid is meaningless, so the cursor restarts from zero.
	sameGeneration := state.UidValidity != 0 && int64(fetch.UIDValidity) == state.UidValidity
	cursor := state.LastUid
	if !sameGeneration {
		cursor = 0
	}
	srcRef := eventRef(binding.SourceKey, binding.ID)

	// Messages arrive sorted; the cursor advances per message so a failure
	// halfway through a batch resumes from the last successfully handled UID.
	for _, msg := range fetch.Messages {
		if ctx.Err() != nil {
			break
		}
		uid := int64(msg.UID)

		parsed, err := parseEmailMessage(msg.Raw, cfg.Username, ignoreAuto)
		if err != nil {
			slog.Warn("email source: parse failed", "binding", binding.ID, "uid", uid, "error", err)
			cursor = uid // a malformed message must not block the mailbox
			continue
		}
		if parsed.IgnoreReason != "" {
			slog.Debug("email source: ignored message",
				"binding", binding.ID, "uid", uid, "reason", parsed.IgnoreReason)
			cursor = uid
			continue
		}
		trig := parsed.trigger(binding.ProjectID, srcRef, fetch.UIDValidity, msg.UID, parsed.Date)
		res, err := s.intake.Handle(ctx, trig)
		if err != nil {
			// Persist progress up to the last good message, then back off.
			_ = s.saveCursor(ctx, binding, mailbox, fetch.UIDValidity, cursor)
			s.recordFailure(ctx, binding, mailbox, state, now, err)
			s.recordDelivery(binding, DeliveryTransportFailed)
			return fmt.Errorf("email source: intake: %w", err)
		}
		switch {
		case res.Duplicate:
			s.recordDelivery(binding, DeliveryDuplicate)
		case res.Decision == intake.DecisionIgnore:
			s.recordDelivery(binding, DeliveryRejected)
		default:
			s.recordDelivery(binding, DeliverySuccess)
		}
		cursor = uid
	}

	if err := s.saveCursor(ctx, binding, mailbox, fetch.UIDValidity, cursor); err != nil {
		return err
	}
	// A completed poll proves the channel recovered: clear the persisted cursor
	// error so the binding does not stay "error" after a transient failure.
	s.clearFailure(ctx, binding, mailbox)
	s.recordDelivery(binding, DeliveryPollOK)
	return nil
}

// imapFailureOutcome distinguishes a login rejection (which marks the binding
// error immediately) from a transient transport failure.
func imapFailureOutcome(err error) DeliveryOutcome {
	var authErr *ErrIMAPAuth
	if errors.As(err, &authErr) {
		return DeliveryIMAPAuthFailed
	}
	return DeliveryTransportFailed
}

func (s *EmailSourceService) clearFailure(ctx context.Context, binding store.ProjectExternalSource, mailbox string) {
	if _, err := s.q.SetEmailPollBackoff(ctx, store.SetEmailPollBackoffParams{
		BackoffUntil: sql.NullTime{},
		LastError:    "",
		BindingID:    binding.ID,
		Mailbox:      mailbox,
	}); err != nil {
		slog.Warn("email source: clear backoff failed", "binding", binding.ID)
	}
}

func (s *EmailSourceService) acquire(bindingID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[bindingID] {
		return false
	}
	s.running[bindingID] = true
	return true
}

func (s *EmailSourceService) release(bindingID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running, bindingID)
}

func (s *EmailSourceService) loadIMAPBinding(ctx context.Context, bindingID int64) (store.ProjectExternalSource, error) {
	row, err := s.q.GetProjectExternalSource(ctx, bindingID)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ProjectExternalSource{}, ErrIntakeSourceNotFound
	}
	if err != nil {
		return store.ProjectExternalSource{}, err
	}
	if strings.ToLower(strings.TrimSpace(row.Provider)) != emailProviderName ||
		configString(row.Config, "mode") != emailIMAPModeValue {
		return store.ProjectExternalSource{}, ErrIntakeSourceNotFound
	}
	return row, nil
}

// loadPollState returns the binding's cursor, materializing the row when it is
// missing (a binding created before the poller existed, or a manual insert).
func (s *EmailSourceService) loadPollState(ctx context.Context, binding store.ProjectExternalSource, mailbox string) (store.EmailSourcePollState, error) {
	state, err := s.q.GetEmailPollState(ctx, store.GetEmailPollStateParams{
		BindingID: binding.ID, Mailbox: mailbox,
	})
	if err == nil {
		return state, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return store.EmailSourcePollState{}, err
	}
	return s.q.UpsertEmailPollState(ctx, store.UpsertEmailPollStateParams{
		ProjectID: binding.ProjectID,
		BindingID: binding.ID,
		Mailbox:   mailbox,
	})
}

func (s *EmailSourceService) saveCursor(ctx context.Context, binding store.ProjectExternalSource, mailbox string, uidValidity uint32, lastUID int64) error {
	if _, err := s.q.UpdateEmailPollCursor(ctx, store.UpdateEmailPollCursorParams{
		UidValidity: int64(uidValidity),
		LastUid:     lastUID,
		BindingID:   binding.ID,
		Mailbox:     mailbox,
	}); err != nil {
		return fmt.Errorf("email source: save cursor: %w", err)
	}
	return nil
}

// recordFailure stores the error and an exponentially growing backoff. The
// previous window's length is derived from (backoff_until - updated_at), so no
// extra counter column is needed.
func (s *EmailSourceService) recordFailure(ctx context.Context, binding store.ProjectExternalSource, mailbox string, state store.EmailSourcePollState, now time.Time, cause error) {
	until := now.Add(emailNextBackoff(state, now))
	if _, err := s.q.SetEmailPollBackoff(ctx, store.SetEmailPollBackoffParams{
		BackoffUntil: sql.NullTime{Time: until.UTC(), Valid: true},
		LastError:    webhooksource.TruncateUTF8(cause.Error(), 500),
		BindingID:    binding.ID,
		Mailbox:      mailbox,
	}); err != nil {
		slog.Warn("email source: save backoff failed", "binding", binding.ID, "error", err)
	}
}

func emailNextBackoff(state store.EmailSourcePollState, now time.Time) time.Duration {
	d := emailBackoffBase
	if state.BackoffUntil.Valid && state.LastError != "" && !state.UpdatedAt.IsZero() {
		if prev := state.BackoffUntil.Time.Sub(state.UpdatedAt); prev > 0 {
			d = prev * 2
		}
	}
	if d < emailBackoffBase {
		d = emailBackoffBase
	}
	if d > emailBackoffMax {
		d = emailBackoffMax
	}
	return d
}

// imapConfig decrypts the bound imap credential.
func (s *EmailSourceService) imapConfig(ctx context.Context, binding store.ProjectExternalSource) (IMAPConfig, error) {
	if s.creds == nil || !binding.CredentialID.Valid || binding.CredentialID.Int64 <= 0 {
		return IMAPConfig{}, errors.New("email source: binding has no imap credential")
	}
	cred, err := s.creds.GetBoundByID(ctx, binding.CredentialID.Int64)
	if err != nil {
		return IMAPConfig{}, fmt.Errorf("email source: load credential: %w", err)
	}
	return imapConfigFromCredential(cred.Config), nil
}

func imapConfigFromCredential(cfg map[string]any) IMAPConfig {
	out := IMAPConfig{
		Host:     stringFromConfig(cfg, "imap_host"),
		Security: strings.ToLower(stringFromConfig(cfg, "security")),
		Username: stringFromConfig(cfg, "username"),
		Password: stringFromConfig(cfg, "password"),
	}
	if v, ok := cfg["imap_port"]; ok {
		out.Port = credInt(v)
	}
	if out.Port == 0 {
		if out.Security == "starttls" || out.Security == "none" {
			out.Port = 143
		} else {
			out.Port = 993
		}
	}
	return out
}

func configBool(rawConfig, key string, fallback bool) bool {
	if rawConfig == "" {
		return fallback
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(rawConfig), &cfg); err != nil {
		return fallback
	}
	switch v := cfg[key].(type) {
	case bool:
		return v
	case string:
		if b, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil {
			return b
		}
	}
	return fallback
}

// --- IMAP transport ----------------------------------------------------------

// goIMAPFetcher is the production transport. It opens one connection per poll,
// selects the mailbox, and fetches message bodies with BODY.PEEK[].
type goIMAPFetcher struct{}

func (goIMAPFetcher) Fetch(ctx context.Context, cfg IMAPConfig, mailbox string, cursor IMAPCursor) (IMAPFetch, error) {
	if cfg.Host == "" || cfg.Username == "" {
		return IMAPFetch{}, errors.New("imap: missing host or username")
	}
	// SSRF guard, same gate as the bind-time probe (VerifyImapByID): the host
	// comes from credential config, so a poll must not be usable to reach
	// loopback / RFC1918 / link-local (cloud metadata) services. The personal
	// edition explicitly opts local mail servers back in via
	// storage.kb.allow_local_sources.
	if err := validateRemoteHost(ctx, cfg.Host); err != nil {
		return IMAPFetch{}, fmt.Errorf("imap: %w", err)
	}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	opts := &imapclient.Options{
		TLSConfig: &tls.Config{ServerName: cfg.Host},
		Dialer:    &net.Dialer{Timeout: emailDialTimeout},
	}

	var (
		c   *imapclient.Client
		err error
	)
	switch cfg.Security {
	case "", "ssl", "tls", "implicit":
		c, err = imapclient.DialTLS(addr, opts)
	case "starttls":
		c, err = imapclient.DialStartTLS(addr, opts)
	case "none":
		// Explicit opt-out on the credential itself (self-hosted / legacy
		// server). Allowed, but the password crosses the wire in cleartext, so
		// leave a trace in the log.
		slog.Warn("email source: polling over plaintext IMAP", "host", cfg.Host, "port", cfg.Port)
		c, err = imapclient.DialInsecure(addr, opts)
	default:
		return IMAPFetch{}, fmt.Errorf("imap: unsupported security %q", cfg.Security)
	}
	if err != nil {
		return IMAPFetch{}, err
	}
	defer c.Close()

	if err := c.Login(cfg.Username, cfg.Password).Wait(); err != nil {
		// A rejected LOGIN is a credential problem the user can fix; surface it
		// as *ErrIMAPAuth so the binding goes to `error` and an alert fires. The
		// server's reply never contains the password.
		return IMAPFetch{}, &ErrIMAPAuth{Detail: webhooksource.TruncateUTF8(err.Error(), 200)}
	}
	defer func() { _ = c.Logout().Wait() }()

	selected, err := c.Select(mailbox, nil).Wait()
	if err != nil {
		return IMAPFetch{}, err
	}
	result := IMAPFetch{UIDValidity: selected.UIDValidity}

	set, err := c.UIDSearch(imapFetchSearchCriteria(cursor, result.UIDValidity), nil).Wait()
	if err != nil {
		return IMAPFetch{}, err
	}
	uidSet, ok := set.All.(imap.UIDSet)
	if !ok {
		return IMAPFetch{}, fmt.Errorf("imap: unexpected search result %T", set.All)
	}
	uids, _ := uidSet.Nums()
	if len(uids) == 0 {
		return result, nil
	}
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
	if len(uids) > emailBatchLimit {
		uids = uids[:emailBatchLimit]
	}
	bounded := imap.UIDSetNum(uids...)

	bufs, err := c.Fetch(bounded, imapFetchOptions()).Collect()
	if err != nil {
		return IMAPFetch{}, err
	}
	result.Reset = cursor.UIDValidity != 0 && cursor.UIDValidity != result.UIDValidity
	result.Messages = make([]FetchedMessage, 0, len(bufs))
	for _, buf := range bufs {
		raw := buf.FindBodySection(imapFetchBodySection())
		if raw == nil {
			continue
		}
		result.Messages = append(result.Messages, FetchedMessage{UID: uint32(buf.UID), Raw: raw})
	}
	sort.Slice(result.Messages, func(i, j int) bool { return result.Messages[i].UID < result.Messages[j].UID })
	return result, nil
}

// imapFetchSearchCriteria is the read position as an IMAP search: new UIDs in
// a stable mailbox, or a time window after a UIDVALIDITY change (and on the
// very first poll, where the cursor is empty).
func imapFetchSearchCriteria(cursor IMAPCursor, uidValidity uint32) *imap.SearchCriteria {
	if cursor.UIDValidity != 0 && cursor.UIDValidity == uidValidity && cursor.LastUID > 0 {
		set := imap.UIDSet{}
		set.AddRange(imap.UID(cursor.LastUID+1), 0)
		return &imap.SearchCriteria{UID: []imap.UIDSet{set}}
	}
	return &imap.SearchCriteria{Since: cursor.ResetSince}
}

// imapFetchBodySection and imapFetchOptions are separated out so the read-only
// guarantee (PEEK, never BODY[]) is directly assertable in a unit test.
func imapFetchBodySection() *imap.FetchItemBodySection {
	return &imap.FetchItemBodySection{Peek: true}
}

func imapFetchOptions() *imap.FetchOptions {
	return &imap.FetchOptions{
		UID:         true,
		BodySection: []*imap.FetchItemBodySection{imapFetchBodySection()},
	}
}

// --- Intake collaborators ----------------------------------------------------

// emailResolver maps a message's thread root to the task it belongs to. The
// mapping is the issue's provenance row (external_source=email,
// external_id="<binding>#<thread root>"), written by the executor when the task
// was created — so no extra binding table is needed.
type emailResolver struct{ db *store.DB }

func (r emailResolver) Resolve(ctx context.Context, t intake.Trigger) (intake.Resolution, error) {
	thread := t.Thread
	extID := emailExternalID(t.SourceRef, thread.ThreadExtID)
	if extID == "" {
		return intake.Resolution{Thread: thread}, nil
	}
	var issueID int64
	err := r.db.QueryRowContext(ctx, `
		SELECT i.id
		FROM issues i
		JOIN columns c ON c.id = i.column_id
		WHERE c.project_id = ? AND i.external_source = ? AND i.external_id = ?
		ORDER BY i.id DESC LIMIT 1`,
		t.ProjectID, emailProviderName, extID,
	).Scan(&issueID)
	if errors.Is(err, sql.ErrNoRows) {
		return intake.Resolution{Thread: thread}, nil
	}
	if err != nil {
		return intake.Resolution{}, err
	}
	workspaceID, err := activeWorkspaceForIssue(ctx, r.db, issueID)
	if err != nil {
		return intake.Resolution{}, err
	}
	thread.IssueID, thread.WorkspaceID = issueID, workspaceID
	return intake.Resolution{
		Thread:      thread,
		IssueID:     issueID,
		WorkspaceID: workspaceID,
		Bound:       workspaceID != 0,
	}, nil
}

func emailExternalID(bindingRef, threadRoot string) string {
	bindingRef, threadRoot = strings.TrimSpace(bindingRef), strings.TrimSpace(threadRoot)
	if bindingRef == "" || threadRoot == "" {
		return ""
	}
	return bindingRef + "#" + threadRoot
}

// emailExecutor creates a task or resumes the existing one. It mirrors the
// GitHub executor: a resume delivers into the bound workspace, a new task is
// routed in the project and its provenance records the thread root.
type emailExecutor struct{ svc *EmailSourceService }

func (e emailExecutor) Execute(ctx context.Context, plan intake.ExecutionPlan) (intake.ExecutionResult, error) {
	s := e.svc
	if plan.Decision == intake.DecisionResume {
		if plan.WorkspaceID == 0 {
			return intake.ExecutionResult{}, errors.New("email intake: resume without a workspace")
		}
		s.deliver(ctx, plan.WorkspaceID, plan.Trigger)
		return intake.ExecutionResult{IssueID: plan.IssueID, WorkspaceID: plan.WorkspaceID}, nil
	}

	dispatch := s.router()
	if dispatch == nil {
		return intake.ExecutionResult{}, errors.New("email intake: no task router wired")
	}
	owner, ok := s.projectOwner(ctx, plan.Trigger.ProjectID)
	if !ok {
		return intake.ExecutionResult{}, errors.New("email intake: project owner unresolved")
	}
	hint := RouteHint{
		ForceNew:  plan.Trigger.ForceNew || plan.IssueID == 0,
		TitleHint: strings.TrimSpace(plan.Brief.Goal),
	}
	if plan.IssueID > 0 {
		hint.ActiveIssueID = plan.IssueID
	}
	target, err := dispatch.RouteInProject(ctx, owner, plan.Trigger.ProjectID, plan.Trigger.Text, hint)
	if err != nil {
		return intake.ExecutionResult{}, fmt.Errorf("email intake: route: %w", err)
	}
	if target.IssueID == 0 || target.WorkspaceID == 0 {
		return intake.ExecutionResult{}, fmt.Errorf("%w: issue=%d workspace=%d",
			errRouteIncomplete, target.IssueID, target.WorkspaceID)
	}

	s.recordProvenance(ctx, target.IssueID, plan.Trigger)
	s.persistBrief(ctx, target.IssueID, plan)
	s.deliver(ctx, target.WorkspaceID, plan.Trigger)
	return intake.ExecutionResult{IssueID: target.IssueID, WorkspaceID: target.WorkspaceID}, nil
}

func (s *EmailSourceService) projectOwner(ctx context.Context, projectID int64) (OwnerRef, bool) {
	p, err := s.q.GetProject(ctx, projectID)
	if err != nil {
		return OwnerRef{}, false
	}
	return OwnerRef{Type: p.OwnerType, ID: p.OwnerID}, true
}

// recordProvenance writes the upstream origin onto a newly created task. An
// issue that already carries a source is left alone.
func (s *EmailSourceService) recordProvenance(ctx context.Context, issueID int64, t intake.Trigger) {
	if issueID == 0 {
		return
	}
	extID := emailExternalID(t.SourceRef, t.Thread.ThreadExtID)
	if extID == "" {
		extID = t.SourceRef + "#" + t.EventID
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE issues SET external_source = ?, external_id = ?, external_url = ?
		 WHERE id = ? AND (external_source IS NULL OR external_source = '')`,
		emailProviderName, extID, t.SourceURL, issueID,
	); err != nil {
		slog.Warn("email intake: write issue provenance failed", "issue", issueID, "error", err)
	}
}

func (s *EmailSourceService) persistBrief(ctx context.Context, issueID int64, plan intake.ExecutionPlan) {
	if s.db == nil || issueID == 0 {
		return
	}
	if _, err := s.q.GetLatestTaskBrief(ctx, issueID); err == nil {
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		slog.Warn("email intake: read current task brief failed", "issue", issueID, "error", err)
		return
	}
	brief := plan.Brief
	if brief.Provenance.DecisionID == 0 {
		brief.Provenance.DecisionID = plan.DecisionID
	}
	if _, err := NewTaskBriefWriter(s.db).Append(ctx, issueID, brief, 0); err != nil {
		slog.Warn("email intake: persist task brief failed", "issue", issueID, "error", err)
	}
}

func (s *EmailSourceService) deliver(ctx context.Context, workspaceID int64, t intake.Trigger) {
	d := s.messageDeliverer()
	if d == nil || workspaceID == 0 {
		return
	}
	ws, err := s.q.GetWorkspace(ctx, workspaceID)
	if err != nil {
		slog.Warn("email intake: load workspace failed", "workspace", workspaceID, "error", err)
		return
	}
	if _, _, err := d.Deliver(ctx, workspaceID, ws.Path, emailDeliveryText(t), ""); err != nil {
		slog.Warn("email intake: deliver failed", "workspace", workspaceID, "error", err)
	}
}

func emailDeliveryText(t intake.Trigger) string {
	text := strings.TrimSpace(t.Text)
	if t.SourceURL == "" {
		return text
	}
	if text == "" {
		return "来源: " + t.SourceURL
	}
	return text + "\n\n来源: " + t.SourceURL
}
