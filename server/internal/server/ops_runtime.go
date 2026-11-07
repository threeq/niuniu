package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/niuniu-dev/niuniu/internal/integration"
	"github.com/niuniu-dev/niuniu/internal/notify"
	"github.com/niuniu-dev/niuniu/internal/opsbot"
	"github.com/niuniu-dev/niuniu/internal/opsbot/browser"
	"github.com/niuniu-dev/niuniu/internal/opsbot/channel"
	"github.com/niuniu-dev/niuniu/internal/opsbot/content"
	"github.com/niuniu-dev/niuniu/internal/opsbot/packs"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// initOpsRuntime builds the runnable Ops Bot services (adapter resolver,
// publisher, engagement, dispatcher, metrics, calendar, feedback, trend,
// director) over the configuration services from initOpsBot. Credentials and
// IM notification need services constructed later in New, so they are attached
// in wireOpsLate.
func (s *Server) initOpsRuntime(svc *opsbot.Service, approval *opsbot.Approval, budget *opsbot.Budget, registry *packs.Registry) {
	sessions := browser.NewSessionManager(s.queries, s.cfg.DataDir)
	worker := browser.NewWorker(sessions)
	worker.EvidenceDir = filepath.Join(s.cfg.DataDir, "ops", "evidence")
	worker.OnRisk = func(ctx context.Context, botID int64, signal string) {
		if s.opsRisk == nil {
			return
		}
		if signal == "recipe_broken" {
			// A broken recipe is not an account pause: degrade to the manual
			// package path and alert the operator with the concrete reason.
			_, _ = s.opsRisk.HandleAdapterError(ctx, botID, &channel.AdapterError{
				Capability: channel.CapPublish,
				Code:       channel.CodeRecipeBroken,
			})
			return
		}
		_, _ = s.opsRisk.Evaluate(ctx, botID, signal)
	}
	manual := channel.NewManualAdapter(filepath.Join(s.cfg.DataDir, "ops", "manual"))

	resolver := &opsbot.AdapterResolver{
		Bots:   s.queries,
		Packs:  registry,
		Worker: worker,
		Manual: manual,
	}

	scheduler := &opsTimerScheduler{}
	publisher := &opsbot.Publisher{
		Q:          s.queries,
		DB:         store.Wrap(s.db),
		AdapterFor: resolver.AdapterFor,
		Manual:     manual,
		Events:     s.queries,
		Problems:   opsPublishProblems{q: s.queries, kanban: s.kanbanSvc},
		Notify: func(ctx context.Context, botID int64, event string, detail map[string]any) error {
			ownerType, ownerID := "", int64(0)
			if bot, err := s.queries.GetOpsBot(ctx, botID); err == nil {
				ownerType, ownerID = bot.OwnerType, bot.OwnerID
			}
			s.notifyHub.Broadcast(notify.Notification{
				Topic:     "opsbot",
				Action:    event,
				ID:        botID,
				OwnerType: ownerType,
				OwnerID:   ownerID,
				Extra:     detail,
			})
			return nil
		},
		CheckBudget: func(ctx context.Context, botID int64) error {
			bot, err := s.queries.GetOpsBot(ctx, botID)
			if err != nil {
				return err
			}
			if !bot.EpicIssueID.Valid || bot.EpicIssueID.Int64 == 0 {
				return nil
			}
			return budget.Check(ctx, botID, bot.EpicIssueID.Int64, 0)
		},
		BotStatus: func(ctx context.Context, botID int64) (string, error) {
			bot, err := s.queries.GetOpsBot(ctx, botID)
			if err != nil {
				return "", err
			}
			return bot.Status, nil
		},
		OnAdapterFailure: func(ctx context.Context, botID int64, err error) {
			if s.opsRisk == nil {
				return
			}
			if signal, ok := opsRiskSignal(err); ok {
				_, _ = s.opsRisk.Evaluate(ctx, botID, signal)
				return
			}
			var adapterErr *channel.AdapterError
			if errors.As(err, &adapterErr) && adapterErr.Code == channel.CodeRecipeBroken {
				// A broken recipe cannot be driven automatically: alert the
				// operator with the concrete degradation reason.
				_, _ = s.opsRisk.HandleAdapterError(ctx, botID, err)
			}
		},
		Schedule: scheduler.Register,
		Now:      time.Now,
	}

	engagement := &opsbot.Engagement{
		Q:          s.queries,
		DB:         store.Wrap(s.db),
		AdapterFor: resolver.AdapterFor,
		Approvals:  approval,
	}
	dispatcher := &opsbot.EngagementDispatcher{
		Q: s.queries,
		Send: func(ctx context.Context, botID, id, approvalRequestID int64, d opsbot.Decision) error {
			return engagement.Send(ctx, opsbot.BotRef{ID: botID}, id, approvalRequestID, d)
		},
	}

	metricsSvc := &opsbot.Metrics{
		Fetch: func(ctx context.Context, botID int64, from, to string) ([]opsbot.MetricsSample, error) {
			adapter, err := resolver.AdapterFor(ctx, botID)
			if err != nil {
				return nil, err
			}
			if !adapter.Capabilities().Supports(channel.CapMetrics) {
				return nil, channel.ErrCapabilityUnavailable
			}
			values, err := adapter.FetchMetrics(ctx, from, to)
			if err != nil {
				// The metrics sync is a risk-signal producer too: an expired
				// login or a rate limit must pause the account.
				if signal, ok := opsRiskSignal(err); ok && s.opsRisk != nil {
					_, _ = s.opsRisk.Evaluate(ctx, botID, signal)
				}
				return nil, err
			}
			return []opsbot.MetricsSample{{Date: to, Fields: values, Source: "platform"}}, nil
		},
		Store: s.queries,
		Bots:  s.queries,
	}

	briefs := &content.BriefBuilder{Store: &content.SQLBriefStore{Q: s.queries}}
	calendar := &opsbot.Calendar{
		Bots:    s.queries,
		Planner: briefs,
		Store:   &opsbot.SQLCalendarStore{Q: s.queries},
	}
	feedback := &opsbot.Feedback{
		Bots:      s.queries,
		Metrics:   metricsSvc,
		Published: opsPublished{db: store.Wrap(s.db)}.Published,
		Knowledge: &opsbot.SQLHitKnowledge{Q: s.queries, ProducerAgent: "opsbot"},
	}
	director := &opsbot.Director{
		Bots:      &opsbot.SQLDirectorAccounts{Q: s.queries},
		Planner:   calendar,
		Calendar:  calendar,
		Approvals: approval,
		Feedback:  feedback,
		Budget:    budget,
		Board:     &opsDirectorBoard{q: s.queries, bus: s.eventBus, director: nil},
	}
	trend := &opsbot.Trend{
		Bots:     s.queries,
		Calendar: &opsbot.SQLTrendCalendar{Q: s.queries},
		History:  &opsbot.SQLTrendHistory{Q: s.queries},
		Budget:   budget,
	}

	if board, ok := director.Board.(*opsDirectorBoard); ok {
		board.director = director
	}

	s.opsSessions = sessions
	s.opsWorker = worker
	s.opsResolver = resolver
	s.opsScheduler = scheduler
	s.opsPublisher = publisher
	s.opsEngagement = engagement
	s.opsDispatcher = dispatcher
	s.opsMetrics = metricsSvc
	s.opsCalendar = calendar
	s.opsFeedback = feedback
	s.opsDirector = director
	s.opsTrend = trend
	s.opsBotSvc = svc
}

// wireOpsLate attaches dependencies that only exist after the credential and IM
// bot services are constructed.
func (s *Server) wireOpsLate() {
	if s.opsResolver != nil && s.extCredSvc != nil {
		s.opsResolver.Creds = opsCredentialReader{svc: s.extCredSvc}
	}
	if s.opsWorker != nil && s.imbotSvc != nil {
		s.opsWorker.Notifier = opsReloginNotifier{imbot: s.imbotSvc}
	}
	if s.opsEngagement != nil && s.imbotSvc != nil {
		s.opsEngagement.OnEscalated = func(ctx context.Context, cur store.OpsEngagement, reason string) error {
			s.imbotSvc.NotifyOpsRisk(ctx, cur.BotID, "高风险互动已升级总监："+reason)
			return nil
		}
	}
	if s.opsRisk != nil && s.opsResolver != nil {
		// A resume probes the real platform session instead of trusting the
		// stored login flag.
		s.opsRisk.VerifyLogin = func(ctx context.Context, botID int64) error {
			bot, err := s.queries.GetOpsBot(ctx, botID)
			if err != nil {
				return err
			}
			adapter, err := s.opsResolver.AdapterFor(ctx, botID)
			if err != nil {
				return err
			}
			return adapter.EnsureSession(ctx, bot.AccountRef)
		}
	}
	if s.opsDirector != nil && s.workspaceMessageSvc != nil {
		s.opsDirector.Messenger = opsDirectorMessenger{q: s.queries, svc: s.workspaceMessageSvc}
	}
	// The dispatcher sends only interactions an operator already approved; it
	// re-checks the account status and hourly cap on every send.
	if s.opsDispatcher != nil {
		_ = s.opsDispatcher.Start(context.Background())
	}
}

// --- adapters ------------------------------------------------------------

// opsCredentialReader resolves the decrypted account credential without ever
// logging its contents.
type opsCredentialReader struct {
	svc *service.ExternalCredentialService
}

func (r opsCredentialReader) Decrypted(ctx context.Context, ownerType string, ownerID int64, provider, alias string) (map[string]any, error) {
	if r.svc == nil {
		return nil, service.ErrNoCredential
	}
	rows, err := r.svc.ListForOwner(ctx, ownerType, ownerID, integration.ProviderName(provider))
	if err != nil {
		return nil, err
	}
	alias = strings.TrimSpace(alias)
	for _, row := range rows {
		if row.Alias != alias || row.OwnerType != ownerType || row.OwnerID != ownerID {
			continue
		}
		full, err := r.svc.GetBoundByID(ctx, row.ID)
		if err != nil {
			return nil, err
		}
		return full.Config, nil
	}
	return nil, service.ErrNoCredential
}

// opsPublishProblems opens a concrete, actionable issue for an unrecoverable
// publish failure inside the bot's project.
type opsPublishProblems struct {
	q      *store.Queries
	kanban *service.KanbanService
}

func (p opsPublishProblems) OpenPublishProblem(ctx context.Context, problem opsbot.PublishProblem) (int64, error) {
	if p.q == nil || p.kanban == nil {
		return 0, errors.New("ops publish problem reporter is not configured")
	}
	bot, err := p.q.GetOpsBot(ctx, problem.BotID)
	if err != nil {
		return 0, err
	}
	if !bot.EpicIssueID.Valid || bot.EpicIssueID.Int64 == 0 {
		return 0, errors.New("bot has no control epic to attach a problem issue to")
	}
	epic, err := p.q.GetIssue(ctx, bot.EpicIssueID.Int64)
	if err != nil {
		return 0, err
	}
	column, err := p.q.GetColumn(ctx, epic.ColumnID)
	if err != nil {
		return 0, err
	}
	columns, err := p.q.ListColumnsByProject(ctx, column.ProjectID)
	if err != nil {
		return 0, err
	}
	if len(columns) == 0 {
		return 0, fmt.Errorf("project %d has no columns", column.ProjectID)
	}
	title := fmt.Sprintf("发布失败：%s（%s）", problem.Platform, problem.Code)
	description := fmt.Sprintf(
		"运营 Bot 发布失败，需人工处理。\n\n- Bot：%d\n- 账号：%s\n- 平台：%s\n- 错误码：%s\n- 可重试：%v\n- 尝试次数：%d\n- 幂等键：%s\n- 内容任务：#%d\n- 发布记录：#%d\n- 错误：%s",
		problem.BotID, problem.AccountRef, problem.Platform, problem.Code,
		problem.Retryable, problem.Attempts, problem.IdempotencyKey,
		problem.IssueID, problem.RecordID, problem.Error,
	)
	priority := int64(1)
	if !problem.Retryable {
		priority = 2
	}
	issue, err := p.kanban.CreateIssue(ctx, columns[0].ID, title, description, priority, 0,
		"", "", "", 0, nil, "task", 0, nil, nil, 0)
	if err != nil {
		return 0, err
	}
	return issue.ID, nil
}

// opsPublished lists publishes in [from, to] for the weekly review. The
// existing publish record is the source of truth; the issue provides the title.
type opsPublished struct {
	db *store.DB
}

func (o opsPublished) Published(ctx context.Context, botID int64, from, to string) ([]opsbot.PublishedContent, error) {
	if o.db == nil {
		return nil, errors.New("ops published reader is not configured")
	}
	query := `SELECT r.issue_id, i.title, r.published_at
		FROM ops_publish_records r
		LEFT JOIN issues i ON i.id = r.issue_id
		WHERE r.bot_id = ? AND r.status = ?`
	args := []any{botID, opsbot.PublishStatusPublished}
	// published_at is CURRENT_TIMESTAMP (UTC text) on SQLite; PostgreSQL binds
	// time.Time natively. Binding the driver-appropriate form keeps the range
	// comparison valid on both.
	if from = strings.TrimSpace(from); from != "" {
		at, err := time.Parse("2006-01-02", from)
		if err != nil {
			return nil, fmt.Errorf("published from %q: %w", from, err)
		}
		query += ` AND r.published_at >= ?`
		args = append(args, opsTimestamp(at))
	}
	if to = strings.TrimSpace(to); to != "" {
		at, err := time.Parse("2006-01-02", to)
		if err != nil {
			return nil, fmt.Errorf("published to %q: %w", to, err)
		}
		query += ` AND r.published_at <= ?`
		args = append(args, opsTimestamp(at.Add(24*time.Hour-time.Second)))
	}
	query += ` ORDER BY r.published_at ASC`

	rows, err := o.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]opsbot.PublishedContent, 0)
	for rows.Next() {
		var (
			item        opsbot.PublishedContent
			title       sql.NullString
			publishedAt sql.NullTime
		)
		if err := rows.Scan(&item.IssueID, &title, &publishedAt); err != nil {
			return nil, err
		}
		if !publishedAt.Valid {
			continue
		}
		item.BotID = botID
		item.Title = title.String
		item.PublishedAt = publishedAt.Time
		out = append(out, item)
	}
	return out, rows.Err()
}

// opsRiskSignal maps an adapter failure to the account-risk signal it implies.
func opsRiskSignal(err error) (string, bool) {
	var adapterErr *channel.AdapterError
	if !errors.As(err, &adapterErr) {
		return "", false
	}
	return opsbot.RiskSignalForCode(adapterErr.Code)
}

// opsTimestamp binds a time the way the active driver compares it to a
// CURRENT_TIMESTAMP column: natively on PostgreSQL, as UTC text on SQLite.
func opsTimestamp(at time.Time) any {
	if store.Driver == "postgres" {
		return at
	}
	return at.UTC().Format("2006-01-02 15:04:05")
}

// opsCostBreakdown reads per-category usage from the account Epic's cost events.
// The category/unit/quantity triple lives in detail_json (written by the cost
// recorder), so the aggregation happens here rather than in SQL.
func opsCostBreakdown(ctx context.Context, db *store.DB, epicIssueID int64, from, to time.Time) ([]opsbot.UsageItem, error) {
	if db == nil || epicIssueID <= 0 {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx, `
		SELECT e.detail_json
		FROM issue_exec_events e
		JOIN issues i ON i.id = e.issue_id
		WHERE i.parent_issue_id = ? AND e.kind = ? AND e.created_at >= ? AND e.created_at < ?`,
		epicIssueID, "cost", from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type usageKey struct{ category, unit string }
	sums := map[usageKey]float64{}
	for rows.Next() {
		var raw sql.NullString
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if !raw.Valid || strings.TrimSpace(raw.String) == "" {
			continue
		}
		var item opsbot.UsageItem
		if err := json.Unmarshal([]byte(raw.String), &item); err != nil {
			continue
		}
		if item.Category == "" || item.Quantity == 0 {
			continue
		}
		sums[usageKey{item.Category, item.Unit}] += item.Quantity
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]opsbot.UsageItem, 0, len(sums))
	for key, quantity := range sums {
		out = append(out, opsbot.UsageItem{Category: key.category, Unit: key.unit, Quantity: quantity})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].Unit < out[j].Unit
	})
	return out, nil
}

type opsReloginNotifier struct {
	imbot *service.IMBotService
}

func (n opsReloginNotifier) RequestRelogin(ctx context.Context, req browser.ReloginRequest) error {
	if n.imbot == nil {
		return nil
	}
	n.imbot.NotifyOpsRisk(ctx, req.BotID, "登录态失效，请运营人重新扫码登录："+req.Reason)
	return nil
}

// opsDirectorBoard posts director state to the director bot's control
// workspace blackboard. The Director carries its identity per call (Owner/BotID
// are set by the entry layer), so the adapter resolves the workspace at write
// time from that same instance — callers must set identity on this instance,
// not on a copy, or the board write cannot resolve its workspace.
type opsDirectorBoard struct {
	q        *store.Queries
	bus      *event.Bus
	director *opsbot.Director
}

func (b *opsDirectorBoard) Write(ctx context.Context, key, content string) error {
	if b.director == nil || b.director.BotID <= 0 {
		return errors.New("ops director board: director bot is not set")
	}
	bot, err := b.q.GetOpsBot(ctx, b.director.BotID)
	if err != nil {
		return err
	}
	if !bot.ControlWorkspaceID.Valid || bot.ControlWorkspaceID.Int64 == 0 {
		return fmt.Errorf("director %d has no control workspace", bot.ID)
	}
	blackboard := service.NewBlackboard(bot.ControlWorkspaceID.Int64, b.q, b.bus)
	blackboard.Write(key, "director", "opsbot-director", content, "")
	return nil
}

type opsDirectorMessenger struct {
	q   *store.Queries
	svc *service.WorkspaceMessageService
}

func (m opsDirectorMessenger) SendToBot(ctx context.Context, botID int64, text string) error {
	target, err := m.q.GetOpsBot(ctx, botID)
	if err != nil {
		return err
	}
	if !target.ControlWorkspaceID.Valid || target.ControlWorkspaceID.Int64 == 0 {
		return fmt.Errorf("bot %d has no control workspace", botID)
	}
	if !target.DirectorBotID.Valid || target.DirectorBotID.Int64 == 0 {
		return fmt.Errorf("bot %d has no director", botID)
	}
	director, err := m.q.GetOpsBot(ctx, target.DirectorBotID.Int64)
	if err != nil {
		return err
	}
	if !director.ControlWorkspaceID.Valid || director.ControlWorkspaceID.Int64 == 0 {
		return fmt.Errorf("director %d has no control workspace", director.ID)
	}
	_, err = m.svc.Send(ctx, director.ControlWorkspaceID.Int64, service.WorkspaceMessageRequest{
		TargetWorkspaceID: target.ControlWorkspaceID.Int64,
		Text:              text,
	})
	return err
}

// opsTimerScheduler fires Publisher.Schedule callbacks at their due instant.
// It is independent of the caller's request context, so an HTTP handler
// returning does not cancel a scheduled publish.
type opsTimerScheduler struct {
	mu      sync.Mutex
	stopped bool
	pending int
	done    chan struct{}
	once    sync.Once
}

var errOpsSchedulerStopped = errors.New("ops scheduler is stopped")

func (t *opsTimerScheduler) Register(_ context.Context, at time.Time, _ string, run func(context.Context)) error {
	t.mu.Lock()
	if t.stopped {
		t.mu.Unlock()
		return errOpsSchedulerStopped
	}
	if t.done == nil {
		t.done = make(chan struct{})
	}
	done := t.done
	t.pending++
	t.mu.Unlock()

	delay := time.Until(at)
	if delay < 0 {
		delay = 0
	}
	go func() {
		defer func() {
			t.mu.Lock()
			t.pending--
			t.mu.Unlock()
		}()
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-done:
			return
		case <-timer.C:
			run(context.Background())
		}
	}()
	return nil
}

// Stop cancels every pending schedule. Safe to call more than once.
func (t *opsTimerScheduler) Stop() {
	t.mu.Lock()
	if t.stopped {
		t.mu.Unlock()
		return
	}
	t.stopped = true
	if t.done != nil {
		t.once.Do(func() { close(t.done) })
	}
	t.mu.Unlock()
}

// Pending reports scheduled callbacks that have not fired yet.
func (t *opsTimerScheduler) Pending() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.pending
}
