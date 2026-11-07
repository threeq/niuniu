package opsbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// Unified metric keys the weekly report reads from the metrics namespace.
const (
	MetricViews        = "views"
	MetricImpressions  = "impressions"
	MetricInteractions = "interactions"
	MetricFollowers    = "followers"
	MetricConversions  = "conversions"
)

const (
	defaultHitTopN         = 3
	homogeneousThreshold   = 0.8
	hitRefPrefix           = "爆款要素："
	hitRefLinePrefix       = "引用："
	hitMemoryType          = "pattern"
	hitMemorySource        = "generate"
	defaultProducerAgent   = "opsbot"
	reusableTopicsLabel    = "可复用选题类型"
	reusableStructureLabel = "可复用结构"
	reusableCoverLabel     = "可复用封面"
	reusableSlotLabel      = "黄金发布时间"
	reusableHookLabel      = "可复用标题钩子"
)

var (
	// ErrFeedbackNotConfigured is returned when the data source a report needs
	// is not wired.
	ErrFeedbackNotConfigured = errors.New("opsbot: feedback not configured")
)

// PublishedContent is one published piece plus the outcomes and attributes the
// weekly review decomposes. Per-content metrics come from the channel adapter
// (official API / browser backend), whose collection is wired by the caller.
type PublishedContent struct {
	BotID        int64     `json:"bot_id"`
	IssueID      int64     `json:"issue_id"`
	Title        string    `json:"title"`
	Cover        string    `json:"cover,omitempty"`
	Structure    string    `json:"structure,omitempty"`
	TopicType    string    `json:"topic_type,omitempty"`
	PublishedAt  time.Time `json:"published_at"`
	Views        float64   `json:"views"`
	Interactions float64   `json:"interactions"`
	Followers    float64   `json:"followers"`
	Conversions  float64   `json:"conversions"`
}

// SpendBreakdown is the budget position of the review window.
type SpendBreakdown struct {
	Total    float64     `json:"total"`
	Currency string      `json:"currency,omitempty"`
	Items    []UsageItem `json:"items,omitempty"`
}

// ApprovalOutcome is one approval card in the review window.
type ApprovalOutcome struct {
	Type        ApprovalType `json:"type"`
	Status      string       `json:"status"`
	Action      string       `json:"action,omitempty"`
	RequestedAt time.Time    `json:"requested_at"`
	DecidedAt   time.Time    `json:"decided_at"`
}

// RiskEvent is one risk signal raised in the window.
type RiskEvent struct {
	BotID  int64     `json:"bot_id"`
	Kind   string    `json:"kind"`
	Detail string    `json:"detail,omitempty"`
	At     time.Time `json:"at"`
}

// HitKnowledgeEntry is one unit written back into the knowledge/memory store.
// Refs are the strings a later OpsContentBrief cites in SourceRefs.
type HitKnowledgeEntry struct {
	Key      string         `json:"key"`
	Title    string         `json:"title"`
	Content  string         `json:"content"`
	Refs     []string       `json:"refs"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// HitKnowledge persists hit elements as knowledge/memory entries and answers
// topic lookups for the next brief. SQLHitKnowledge is the production
// implementation; tests inject an in-memory fake.
type HitKnowledge interface {
	Store(ctx context.Context, botID int64, entries []HitKnowledgeEntry) error
	Search(ctx context.Context, botID int64, topic string) ([]string, error)
}

// SQLHitKnowledge writes hit elements into the owner's memories (the durable
// knowledge store) and mirrors them onto the bot's control workspace
// blackboard. Search is owner-scoped, which is what makes a director's hit
// library shared across its accounts.
type SQLHitKnowledge struct {
	Q *store.Queries
	// ProducerAgent labels blackboard entries. Defaults to "opsbot".
	ProducerAgent string
}

var _ HitKnowledge = (*SQLHitKnowledge)(nil)

// Store upserts one memory per entry, keyed by title, so a repeated weekly
// review refreshes the same entry instead of duplicating it.
func (s *SQLHitKnowledge) Store(ctx context.Context, botID int64, entries []HitKnowledgeEntry) error {
	if s == nil || s.Q == nil {
		return ErrFeedbackNotConfigured
	}
	if botID <= 0 {
		return fmt.Errorf("%w: bot id must be > 0", ErrInvalidInput)
	}
	bot, err := s.Q.GetOpsBot(ctx, botID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %d", ErrNotFound, botID)
	}
	if err != nil {
		return fmt.Errorf("opsbot: load bot for knowledge write-back: %w", err)
	}

	existing, err := s.Q.ListMemoriesForOwner(ctx, store.ListMemoriesForOwnerParams{
		OwnerType: bot.OwnerType, OwnerID: bot.OwnerID,
	})
	if err != nil {
		return fmt.Errorf("opsbot: list memories: %w", err)
	}
	byTitle := make(map[string]store.Memory, len(existing))
	for _, m := range existing {
		byTitle[m.Title] = m
	}

	producer := strings.TrimSpace(s.ProducerAgent)
	if producer == "" {
		producer = defaultProducerAgent
	}
	workspace := sql.NullInt64{}
	if bot.ControlWorkspaceID.Valid {
		workspace = bot.ControlWorkspaceID
	}

	for _, entry := range entries {
		content := memoryContent(entry)
		if prev, ok := byTitle[entry.Title]; ok {
			_, err = s.Q.UpdateMemoryContent(ctx, store.UpdateMemoryContentParams{
				MemType: hitMemoryType, Title: entry.Title, Content: content,
				Source: hitMemorySource, SourcePath: entry.Key,
				Version: prev.Version + 1, ID: prev.ID,
			})
		} else {
			var created store.Memory
			created, err = s.Q.CreateMemory(ctx, store.CreateMemoryParams{
				OwnerType: bot.OwnerType, OwnerID: bot.OwnerID,
				WorkspaceID: workspace, MemType: hitMemoryType,
				Title: entry.Title, Content: content,
				Source: hitMemorySource, SourcePath: entry.Key,
			})
			if err == nil {
				byTitle[entry.Title] = created
			}
		}
		if err != nil {
			return fmt.Errorf("opsbot: write hit memory %q: %w", entry.Title, err)
		}

		if !workspace.Valid {
			continue
		}
		metadata, err := json.Marshal(entry.Metadata)
		if err != nil {
			return fmt.Errorf("opsbot: encode hit metadata %q: %w", entry.Key, err)
		}
		if _, err := s.Q.UpsertBlackboardEntry(ctx, store.UpsertBlackboardEntryParams{
			WorkspaceID: workspace.Int64, ProducerAgent: producer,
			EntryType: "result", EntryKey: entry.Key,
			Content: content, Metadata: string(metadata),
		}); err != nil {
			return fmt.Errorf("opsbot: upsert hit blackboard entry %q: %w", entry.Key, err)
		}
	}
	return nil
}

// Search returns the refs stored with memories whose title or content matches
// topic. An empty topic returns every stored hit ref.
func (s *SQLHitKnowledge) Search(ctx context.Context, botID int64, topic string) ([]string, error) {
	if s == nil || s.Q == nil {
		return nil, ErrFeedbackNotConfigured
	}
	if botID <= 0 {
		return nil, fmt.Errorf("%w: bot id must be > 0", ErrInvalidInput)
	}
	bot, err := s.Q.GetOpsBot(ctx, botID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %d", ErrNotFound, botID)
	}
	if err != nil {
		return nil, fmt.Errorf("opsbot: load bot for knowledge search: %w", err)
	}
	memories, err := s.Q.ListMemoriesForOwner(ctx, store.ListMemoriesForOwnerParams{
		OwnerType: bot.OwnerType, OwnerID: bot.OwnerID,
	})
	if err != nil {
		return nil, fmt.Errorf("opsbot: list memories: %w", err)
	}

	topic = strings.ToLower(strings.TrimSpace(topic))
	var refs []string
	for _, m := range memories {
		if topic != "" && !strings.Contains(strings.ToLower(m.Title+m.Content), topic) {
			continue
		}
		for _, line := range strings.Split(m.Content, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, hitRefLinePrefix) {
				refs = append(refs, strings.TrimSpace(strings.TrimPrefix(line, hitRefLinePrefix)))
			}
		}
	}
	return refs, nil
}

func memoryContent(entry HitKnowledgeEntry) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(entry.Content))
	for _, ref := range entry.Refs {
		if ref = strings.TrimSpace(ref); ref == "" {
			continue
		}
		b.WriteString("\n")
		b.WriteString(hitRefLinePrefix)
		b.WriteString(ref)
	}
	return b.String()
}

// Feedback compiles weekly reports, decomposes hit content and feeds reusable
// elements back into brand knowledge (design §6.1, §7 C13).
//
// Dependencies are hooks, matching Budget and Metrics: production wires the
// platform/source collectors in server.New, tests inject in-memory fakes.
type Feedback struct {
	// Bots supplies goals_json, role and display name. Optional.
	Bots BotGetter
	// Metrics supplies the unified daily metric totals. Optional: without it
	// totals are derived from Published.
	Metrics *Metrics

	// Published returns every piece published in [from, to], inclusive.
	Published func(ctx context.Context, botID int64, from, to string) ([]PublishedContent, error)
	// Spend returns the budget consumed in [from, to]. Optional.
	Spend func(ctx context.Context, botID int64, from, to string) (SpendBreakdown, error)
	// Approvals returns approval cards requested in [from, to]. Optional.
	Approvals func(ctx context.Context, botID int64, from, to string) ([]ApprovalOutcome, error)
	// Risks returns risk events in [from, to]. Optional.
	Risks func(ctx context.Context, botID int64, from, to string) ([]RiskEvent, error)
	// Accounts returns the account bot ids under a director. Optional; a
	// director report without it covers the director bot alone.
	Accounts func(ctx context.Context, directorBotID int64) ([]int64, error)

	// Knowledge is the write-back sink; required by WriteBack/KnowledgeRefs.
	Knowledge HitKnowledge

	// HitTopN caps the hit list per account. Defaults to 3.
	HitTopN int
	// HitMinViews excludes pieces below this view count from the hit list.
	HitMinViews float64
	// Now overrides the clock in tests. Optional.
	Now func() time.Time
}

// ApprovalSummary aggregates approval efficiency over the review window.
type ApprovalSummary struct {
	Total              int     `json:"total"`
	Answered           int     `json:"answered"`
	Approved           int     `json:"approved"`
	Rejected           int     `json:"rejected"`
	Modified           int     `json:"modified"`
	Regenerated        int     `json:"regenerated"`
	TimedOut           int     `json:"timed_out"`
	Cancelled          int     `json:"cancelled"`
	PassRate           float64 `json:"pass_rate"`
	AvgDecisionSeconds float64 `json:"avg_decision_seconds"`

	decidedSeconds float64
}

// DirectorRollup is the cross-account view a director report adds.
type DirectorRollup struct {
	Accounts         int             `json:"accounts"`
	Published        int             `json:"published"`
	Views            float64         `json:"views"`
	Hits             int             `json:"hits"`
	HitRate          float64         `json:"hit_rate"`
	TopicReuseRate   float64         `json:"topic_reuse_rate"`
	HomogeneousRatio float64         `json:"homogeneous_ratio"`
	Detail           []AccountRollup `json:"detail"`
}

// AccountRollup is one account's line in a director report.
type AccountRollup struct {
	BotID       int64   `json:"bot_id"`
	DisplayName string  `json:"display_name,omitempty"`
	Published   int     `json:"published"`
	Views       float64 `json:"views"`
	Hits        int     `json:"hits"`
}

// WeeklyReport is the per-account (or director roll-up) review of one window.
type WeeklyReport struct {
	BotID              int64              `json:"bot_id"`
	Role               string             `json:"role"`
	DisplayName        string             `json:"display_name,omitempty"`
	From               string             `json:"from"`
	To                 string             `json:"to"`
	Days               int                `json:"days"`
	Published          int                `json:"published"`
	Views              float64            `json:"views"`
	AvgViews           float64            `json:"avg_views"`
	Impressions        float64            `json:"impressions"`
	Interactions       float64            `json:"interactions"`
	InteractionRate    float64            `json:"interaction_rate"`
	Followers          float64            `json:"followers"`
	Conversions        float64            `json:"conversions"`
	ConversionRate     float64            `json:"conversion_rate"`
	Budget             SpendBreakdown     `json:"budget"`
	Approvals          ApprovalSummary    `json:"approvals"`
	Risks              []RiskEvent        `json:"risks,omitempty"`
	RiskCount          int                `json:"risk_count"`
	Goals              map[string]float64 `json:"goals,omitempty"`
	Achievement        map[string]float64 `json:"achievement,omitempty"`
	OverallAchievement float64            `json:"overall_achievement"`
	BrandGoal          string             `json:"brand_goal,omitempty"`
	Director           *DirectorRollup    `json:"director,omitempty"`
	GeneratedAt        time.Time          `json:"generated_at"`
}

// Hit is one decomposed piece of hit content.
type Hit struct {
	BotID              int64    `json:"bot_id"`
	IssueID            int64    `json:"issue_id"`
	Title              string   `json:"title"`
	Cover              string   `json:"cover,omitempty"`
	Structure          string   `json:"structure,omitempty"`
	TopicType          string   `json:"topic_type,omitempty"`
	PublishedAt        string   `json:"published_at,omitempty"`
	PublishSlot        string   `json:"publish_slot,omitempty"`
	Views              float64  `json:"views"`
	Interactions       float64  `json:"interactions"`
	Followers          float64  `json:"followers"`
	Conversions        float64  `json:"conversions"`
	Score              float64  `json:"score"`
	Rank               int      `json:"rank"`
	RankByViews        int      `json:"rank_by_views"`
	RankByInteractions int      `json:"rank_by_interactions"`
	RankByFollowers    int      `json:"rank_by_followers"`
	Elements           []string `json:"elements"`
}

// Hits is an ordered hit list.
type Hits []Hit

// Reusable returns the aggregated reusable elements across the hit list: the
// most common topic type, structure, cover, publish slot and title hook.
func (h Hits) Reusable() []string {
	if len(h) == 0 {
		return nil
	}
	type category struct {
		label string
		value func(Hit) string
	}
	categories := []category{
		{reusableTopicsLabel, func(x Hit) string { return x.TopicType }},
		{reusableStructureLabel, func(x Hit) string { return x.Structure }},
		{reusableCoverLabel, func(x Hit) string { return x.Cover }},
		{reusableSlotLabel, func(x Hit) string { return x.PublishSlot }},
		{reusableHookLabel, func(x Hit) string { return titleHook(x.Title) }},
	}
	out := make([]string, 0, len(categories))
	for _, c := range categories {
		counts := map[string]int{}
		bestRank := map[string]int{}
		for _, hit := range h {
			v := c.value(hit)
			if v == "" {
				continue
			}
			counts[v]++
			if r, ok := bestRank[v]; !ok || hit.Rank < r {
				bestRank[v] = hit.Rank
			}
		}
		if len(counts) == 0 {
			continue
		}
		values := make([]string, 0, len(counts))
		for v := range counts {
			values = append(values, v)
		}
		// Ties go to the value from the higher-ranked hit, so a 1-of-3 tie is
		// resolved by the actual best-performing piece, not byte order.
		sort.Slice(values, func(i, j int) bool {
			if counts[values[i]] != counts[values[j]] {
				return counts[values[i]] > counts[values[j]]
			}
			if bestRank[values[i]] != bestRank[values[j]] {
				return bestRank[values[i]] < bestRank[values[j]]
			}
			return values[i] < values[j]
		})
		top := values[0]
		out = append(out, fmt.Sprintf("%s：%s（%d/%d 爆款）", c.label, top, counts[top], len(h)))
	}
	return out
}

// WeeklyReport aggregates one bot's published volume, metrics, budget,
// approval efficiency, risks and goal achievement for [from, to]. A director
// bot instead rolls up its accounts and adds the cross-account view.
func (f *Feedback) WeeklyReport(ctx context.Context, bot BotRef, from, to string) (WeeklyReport, error) {
	if bot.ID <= 0 {
		return WeeklyReport{}, fmt.Errorf("%w: bot id must be > 0", ErrInvalidInput)
	}
	dates, err := metricsDates(from, to)
	if err != nil {
		return WeeklyReport{}, err
	}
	if f.Published == nil {
		return WeeklyReport{}, ErrFeedbackNotConfigured
	}
	from, to = dates[0], dates[len(dates)-1]

	row, err := f.botRow(ctx, bot.ID)
	if err != nil {
		return WeeklyReport{}, err
	}
	role := strings.TrimSpace(bot.Role)
	if role == "" {
		role = strings.TrimSpace(row.Role)
	}
	if role == "" {
		role = RoleAccount
	}

	if role == RoleDirector && f.Accounts != nil {
		return f.directorReport(ctx, bot.ID, row, from, to, len(dates))
	}
	rep, _, _, err := f.accountReport(ctx, bot.ID, row, role, from, to, len(dates))
	return rep, err
}

// Breakdown ranks the window's published pieces by views, interactions and
// follower growth, decomposes each into reusable elements, and returns the top
// hits.
func (f *Feedback) Breakdown(ctx context.Context, bot BotRef, from, to string) (Hits, error) {
	if bot.ID <= 0 {
		return nil, fmt.Errorf("%w: bot id must be > 0", ErrInvalidInput)
	}
	dates, err := metricsDates(from, to)
	if err != nil {
		return nil, err
	}
	if f.Published == nil {
		return nil, ErrFeedbackNotConfigured
	}
	published, err := f.Published(ctx, bot.ID, dates[0], dates[len(dates)-1])
	if err != nil {
		return nil, fmt.Errorf("opsbot: load published content: %w", err)
	}
	for i := range published {
		if published[i].BotID == 0 {
			published[i].BotID = bot.ID
		}
	}
	return f.rank(published), nil
}

// WriteBack persists each hit's decomposed elements plus one aggregate
// playbook entry. The entries' Refs are what the next brief cites.
func (f *Feedback) WriteBack(ctx context.Context, bot BotRef, hits Hits) error {
	if f.Knowledge == nil {
		return ErrFeedbackNotConfigured
	}
	if bot.ID <= 0 {
		return fmt.Errorf("%w: bot id must be > 0", ErrInvalidInput)
	}
	if len(hits) == 0 {
		return nil
	}

	entries := make([]HitKnowledgeEntry, 0, len(hits)+1)
	for _, h := range hits {
		refs := make([]string, 0, len(h.Elements))
		for _, el := range h.Elements {
			refs = append(refs, hitRefPrefix+el)
		}
		entries = append(entries, HitKnowledgeEntry{
			Key:     fmt.Sprintf("ops-hit/%d/%d", bot.ID, h.IssueID),
			Title:   "爆款拆解：" + h.Title,
			Content: hitKnowledgeText(h),
			Refs:    refs,
			Metadata: map[string]any{
				"issue_id": h.IssueID, "title": h.Title, "topic_type": h.TopicType,
				"views": h.Views, "interactions": h.Interactions,
				"followers": h.Followers, "conversions": h.Conversions,
				"score": h.Score, "rank": h.Rank,
			},
		})
	}

	reusable := hits.Reusable()
	playbookRefs := make([]string, 0, len(reusable))
	for _, el := range reusable {
		playbookRefs = append(playbookRefs, hitRefPrefix+el)
	}
	entries = append(entries, HitKnowledgeEntry{
		Key:     fmt.Sprintf("ops-hit-playbook/%d", bot.ID),
		Title:   "爆款可复用要素",
		Content: strings.Join(reusable, "\n"),
		Refs:    playbookRefs,
		Metadata: map[string]any{
			"hits": len(hits),
		},
	})

	if err := f.Knowledge.Store(ctx, bot.ID, entries); err != nil {
		return fmt.Errorf("opsbot: write hit knowledge: %w", err)
	}
	return nil
}

// KnowledgeRefs returns the stored hit elements relevant to topic, ready to be
// wired as BriefBuilder.Knowledge so the next round's brief cites them.
func (f *Feedback) KnowledgeRefs(ctx context.Context, botID int64, topic string) ([]string, error) {
	if f.Knowledge == nil {
		return nil, ErrFeedbackNotConfigured
	}
	if botID <= 0 {
		return nil, fmt.Errorf("%w: bot id must be > 0", ErrInvalidInput)
	}
	refs, err := f.Knowledge.Search(ctx, botID, strings.TrimSpace(topic))
	if err != nil {
		return nil, fmt.Errorf("opsbot: search hit knowledge: %w", err)
	}
	seen := make(map[string]struct{}, len(refs))
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		out = append(out, ref)
	}
	return out, nil
}

func (f *Feedback) accountReport(
	ctx context.Context, botID int64, row store.OpsBot, role, from, to string, days int,
) (WeeklyReport, Hits, []PublishedContent, error) {
	rep := WeeklyReport{
		BotID:       botID,
		Role:        role,
		DisplayName: row.DisplayName,
		From:        from,
		To:          to,
		Days:        days,
		GeneratedAt: f.now(),
	}

	published, err := f.Published(ctx, botID, from, to)
	if err != nil {
		return WeeklyReport{}, nil, nil, fmt.Errorf("opsbot: load published content: %w", err)
	}
	for i := range published {
		if published[i].BotID == 0 {
			published[i].BotID = botID
		}
	}
	rep.Published = len(published)

	if f.Spend != nil {
		rep.Budget, err = f.Spend(ctx, botID, from, to)
		if err != nil {
			return WeeklyReport{}, nil, nil, fmt.Errorf("opsbot: load spend: %w", err)
		}
	}

	if f.Approvals != nil {
		outcomes, err := f.Approvals(ctx, botID, from, to)
		if err != nil {
			return WeeklyReport{}, nil, nil, fmt.Errorf("opsbot: load approvals: %w", err)
		}
		for _, o := range outcomes {
			rep.Approvals.add(o)
		}
		rep.Approvals.finalize()
	}

	if f.Risks != nil {
		rep.Risks, err = f.Risks(ctx, botID, from, to)
		if err != nil {
			return WeeklyReport{}, nil, nil, fmt.Errorf("opsbot: load risks: %w", err)
		}
	}
	rep.RiskCount = len(rep.Risks)

	totals := map[string]float64{}
	if f.Metrics != nil {
		sum, err := f.Metrics.Summary(ctx, botID, from, to)
		if err != nil {
			return WeeklyReport{}, nil, nil, err
		}
		for k, v := range sum.Totals {
			totals[k] = v
		}
		rep.Goals = sum.Goals
		rep.Achievement = sum.Achievement
	} else {
		for _, p := range published {
			totals[MetricViews] += p.Views
			totals[MetricInteractions] += p.Interactions
			totals[MetricFollowers] += p.Followers
			totals[MetricConversions] += p.Conversions
		}
	}

	rep.Views = totals[MetricViews]
	rep.Impressions = totals[MetricImpressions]
	rep.Interactions = interactionTotal(totals)
	rep.Followers = followerTotal(totals)
	rep.Conversions = totals[MetricConversions]
	if rep.Published > 0 {
		rep.AvgViews = rep.Views / float64(rep.Published)
	}
	if rep.Views > 0 {
		rep.InteractionRate = rep.Interactions / rep.Views
		rep.ConversionRate = rep.Conversions / rep.Views
	}

	goals, brand, err := parseGoalSet(row.GoalsJson)
	if err != nil {
		return WeeklyReport{}, nil, nil, err
	}
	if len(rep.Goals) == 0 {
		rep.Goals = goals
	}
	rep.BrandGoal = brand
	if len(rep.Achievement) == 0 {
		rep.Achievement = achievementOf(rep.Goals, totals)
	}
	rep.OverallAchievement = averageAchievement(rep.Achievement)

	return rep, f.rank(published), published, nil
}

func (f *Feedback) directorReport(
	ctx context.Context, directorID int64, row store.OpsBot, from, to string, days int,
) (WeeklyReport, error) {
	accounts, err := f.Accounts(ctx, directorID)
	if err != nil {
		return WeeklyReport{}, fmt.Errorf("opsbot: list director accounts: %w", err)
	}

	rep := WeeklyReport{
		BotID:       directorID,
		Role:        RoleDirector,
		DisplayName: row.DisplayName,
		From:        from,
		To:          to,
		Days:        days,
		Goals:       map[string]float64{},
		Achievement: map[string]float64{},
		GeneratedAt: f.now(),
	}
	roll := &DirectorRollup{Detail: make([]AccountRollup, 0, len(accounts))}
	hitsByBot := make(map[int64]Hits, len(accounts))
	var contents []PublishedContent
	var brands []string
	totals := map[string]float64{}

	for _, accountID := range accounts {
		accountRow, err := f.botRow(ctx, accountID)
		if err != nil {
			return WeeklyReport{}, err
		}
		arep, hits, published, err := f.accountReport(ctx, accountID, accountRow, RoleAccount, from, to, days)
		if err != nil {
			return WeeklyReport{}, err
		}

		roll.Accounts++
		roll.Published += arep.Published
		roll.Views += arep.Views
		roll.Hits += len(hits)
		roll.Detail = append(roll.Detail, AccountRollup{
			BotID: accountID, DisplayName: arep.DisplayName,
			Published: arep.Published, Views: arep.Views, Hits: len(hits),
		})

		rep.Published += arep.Published
		rep.Views += arep.Views
		rep.Impressions += arep.Impressions
		rep.Interactions += arep.Interactions
		rep.Followers += arep.Followers
		rep.Conversions += arep.Conversions
		rep.Budget.Total += arep.Budget.Total
		if rep.Budget.Currency == "" {
			rep.Budget.Currency = arep.Budget.Currency
		}
		rep.Budget.Items = append(rep.Budget.Items, arep.Budget.Items...)
		rep.Approvals.merge(arep.Approvals)
		rep.Risks = append(rep.Risks, arep.Risks...)

		for k, v := range arep.Goals {
			rep.Goals[k] += v
		}
		if arep.BrandGoal != "" {
			brands = append(brands, arep.BrandGoal)
		}

		hitsByBot[accountID] = hits
		contents = append(contents, published...)
	}

	rep.RiskCount = len(rep.Risks)
	rep.Approvals.finalize()
	rep.BrandGoal = strings.Join(brands, "；")
	if rep.Published > 0 {
		rep.AvgViews = rep.Views / float64(rep.Published)
	}
	if rep.Views > 0 {
		rep.InteractionRate = rep.Interactions / rep.Views
		rep.ConversionRate = rep.Conversions / rep.Views
	}
	if roll.Published > 0 {
		roll.HitRate = float64(roll.Hits) / float64(roll.Published)
	}
	roll.TopicReuseRate = topicReuseRate(hitsByBot)
	roll.HomogeneousRatio = homogeneousRatio(contents)
	rep.Director = roll

	totals[MetricViews] = rep.Views
	totals[MetricInteractions] = rep.Interactions
	totals[MetricFollowers] = rep.Followers
	totals[MetricConversions] = rep.Conversions
	rep.Achievement = achievementOf(rep.Goals, totals)
	rep.OverallAchievement = averageAchievement(rep.Achievement)
	return rep, nil
}

func (f *Feedback) rank(published []PublishedContent) Hits {
	hits := make([]Hit, 0, len(published))
	for _, p := range published {
		if f.HitMinViews > 0 && p.Views < f.HitMinViews {
			continue
		}
		hits = append(hits, Hit{
			BotID:       p.BotID,
			IssueID:     p.IssueID,
			Title:       p.Title,
			Cover:       p.Cover,
			Structure:   p.Structure,
			TopicType:   p.TopicType,
			PublishedAt: p.PublishedAt.Format(time.RFC3339),
			PublishSlot: publishSlot(p.PublishedAt),
			Views:       p.Views, Interactions: p.Interactions,
			Followers: p.Followers, Conversions: p.Conversions,
			Elements: hitElements(p),
		})
	}
	if len(hits) == 0 {
		return Hits{}
	}

	viewsRank := rankOf(hits, func(h Hit) float64 { return h.Views })
	interactionRank := rankOf(hits, func(h Hit) float64 { return h.Interactions })
	followerRank := rankOf(hits, func(h Hit) float64 { return h.Followers })
	maxViews := maxOf(hits, func(h Hit) float64 { return h.Views })
	maxInteractions := maxOf(hits, func(h Hit) float64 { return h.Interactions })
	maxFollowers := maxOf(hits, func(h Hit) float64 { return h.Followers })

	for i := range hits {
		h := &hits[i]
		h.RankByViews = viewsRank[h.IssueID]
		h.RankByInteractions = interactionRank[h.IssueID]
		h.RankByFollowers = followerRank[h.IssueID]
		score, parts := 0.0, 0
		if maxViews > 0 {
			score += h.Views / maxViews
			parts++
		}
		if maxInteractions > 0 {
			score += h.Interactions / maxInteractions
			parts++
		}
		if maxFollowers > 0 {
			score += h.Followers / maxFollowers
			parts++
		}
		if parts > 0 {
			h.Score = score / float64(parts)
		}
	}

	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].IssueID < hits[j].IssueID
	})
	topN := f.HitTopN
	if topN <= 0 {
		topN = defaultHitTopN
	}
	if len(hits) > topN {
		hits = hits[:topN]
	}
	for i := range hits {
		hits[i].Rank = i + 1
	}
	return Hits(hits)
}

func (f *Feedback) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now().UTC()
}

func (f *Feedback) botRow(ctx context.Context, botID int64) (store.OpsBot, error) {
	if f.Bots == nil {
		return store.OpsBot{ID: botID}, nil
	}
	row, err := f.Bots.GetOpsBot(ctx, botID)
	if errors.Is(err, sql.ErrNoRows) {
		return store.OpsBot{}, fmt.Errorf("%w: %d", ErrNotFound, botID)
	}
	if err != nil {
		return store.OpsBot{}, fmt.Errorf("opsbot: load bot: %w", err)
	}
	return row, nil
}

func hitElements(p PublishedContent) []string {
	elements := make([]string, 0, 5)
	elements = append(elements, titleHook(p.Title))
	if p.Cover != "" {
		elements = append(elements, "封面："+p.Cover)
	}
	if p.Structure != "" {
		elements = append(elements, "结构："+p.Structure)
	}
	if p.TopicType != "" {
		elements = append(elements, "选题类型："+p.TopicType)
	}
	if slot := publishSlot(p.PublishedAt); slot != "" {
		elements = append(elements, "发布时间："+slot)
	}
	return elements
}

func hitKnowledgeText(h Hit) string {
	var b strings.Builder
	fmt.Fprintf(&b, "标题：%s\n", h.Title)
	if h.TopicType != "" {
		fmt.Fprintf(&b, "选题类型：%s\n", h.TopicType)
	}
	if h.Cover != "" {
		fmt.Fprintf(&b, "封面：%s\n", h.Cover)
	}
	if h.Structure != "" {
		fmt.Fprintf(&b, "结构：%s\n", h.Structure)
	}
	if h.PublishedAt != "" {
		fmt.Fprintf(&b, "发布时间：%s（%s）\n", h.PublishedAt, h.PublishSlot)
	}
	fmt.Fprintf(&b, "数据：阅读 %.0f / 互动 %.0f / 涨粉 %.0f / 转化 %.0f\n",
		h.Views, h.Interactions, h.Followers, h.Conversions)
	fmt.Fprintf(&b, "可复用要素：%s", strings.Join(h.Elements, "；"))
	return b.String()
}

// titleHook classifies a title's hook so the element can be reused next round.
func titleHook(title string) string {
	switch {
	case hasDigit(title):
		return "标题含数字"
	case strings.ContainsAny(title, "?？"):
		return "标题提问"
	case strings.Contains(title, "vs") || strings.Contains(title, "VS") ||
		strings.Contains(title, "对比") || strings.Contains(title, "还是"):
		return "标题对比"
	case strings.ContainsAny(title, "!！"):
		return "标题强调"
	}
	return "标题陈述"
}

func hasDigit(s string) bool {
	for _, r := range s {
		if unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// publishSlot buckets a publish time into a two-hour window, the granularity
// a content calendar can actually reuse.
func publishSlot(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	start := t.Hour()
	end := (start + 2) % 24
	return fmt.Sprintf("%02d:00-%02d:00", start, end)
}

func interactionTotal(m map[string]float64) float64 {
	if v, ok := m[MetricInteractions]; ok {
		return v
	}
	var sum float64
	for _, k := range []string{"likes", "comments", "shares", "saves", "favorites", "collects"} {
		sum += m[k]
	}
	return sum
}

func followerTotal(m map[string]float64) float64 {
	if v, ok := m[MetricFollowers]; ok {
		return v
	}
	if v, ok := m["follower_gain"]; ok {
		return v
	}
	return m["new_followers"]
}

func (s *ApprovalSummary) add(o ApprovalOutcome) {
	s.Total++
	switch o.Status {
	case ApprovalStatusAnswered:
		s.Answered++
		switch o.Action {
		case ActionApproved:
			s.Approved++
		case ActionRejected:
			s.Rejected++
		case ActionModified:
			s.Modified++
		case ActionRegenerate:
			s.Regenerated++
		}
		if !o.DecidedAt.IsZero() && !o.RequestedAt.IsZero() && o.DecidedAt.After(o.RequestedAt) {
			s.decidedSeconds += o.DecidedAt.Sub(o.RequestedAt).Seconds()
		}
	case ApprovalStatusTimeout:
		s.TimedOut++
	case ApprovalStatusCancelled:
		s.Cancelled++
	}
}

func (s *ApprovalSummary) merge(other ApprovalSummary) {
	s.Total += other.Total
	s.Answered += other.Answered
	s.Approved += other.Approved
	s.Rejected += other.Rejected
	s.Modified += other.Modified
	s.Regenerated += other.Regenerated
	s.TimedOut += other.TimedOut
	s.Cancelled += other.Cancelled
	s.decidedSeconds += other.decidedSeconds
}

func (s *ApprovalSummary) finalize() {
	if s.Answered == 0 {
		return
	}
	s.PassRate = float64(s.Approved+s.Modified) / float64(s.Answered)
	s.AvgDecisionSeconds = s.decidedSeconds / float64(s.Answered)
}

func parseGoalSet(raw string) (map[string]float64, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]float64{}, "", nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return nil, "", fmt.Errorf("%w: goals_json must be a JSON object: %v", ErrInvalidConfig, err)
	}
	goals := make(map[string]float64, len(fields))
	brand := ""
	for k, v := range fields {
		var n float64
		if err := json.Unmarshal(v, &n); err == nil {
			goals[k] = n
			continue
		}
		var text string
		if err := json.Unmarshal(v, &text); err == nil && k == "brand" {
			brand = text
		}
	}
	return goals, brand, nil
}

func achievementOf(goals, totals map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(goals))
	for k, goal := range goals {
		if goal <= 0 {
			continue
		}
		actual, ok := goalActual(k, totals)
		if !ok {
			continue
		}
		out[k] = actual / goal
	}
	return out
}

func goalActual(key string, totals map[string]float64) (float64, bool) {
	switch key {
	case MetricFollowers, "follower", "涨粉":
		return followerTotal(totals), true
	case MetricConversions, "conversion", "转化":
		return totals[MetricConversions], true
	case MetricViews, "read", "阅读":
		return totals[MetricViews], true
	case MetricInteractions, "interaction", "互动":
		return interactionTotal(totals), true
	}
	v, ok := totals[key]
	return v, ok
}

func averageAchievement(achievement map[string]float64) float64 {
	if len(achievement) == 0 {
		return 0
	}
	var sum float64
	for _, v := range achievement {
		sum += v
	}
	return sum / float64(len(achievement))
}

func rankOf(hits []Hit, value func(Hit) float64) map[int64]int {
	sorted := append([]Hit(nil), hits...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if value(sorted[i]) != value(sorted[j]) {
			return value(sorted[i]) > value(sorted[j])
		}
		return sorted[i].IssueID < sorted[j].IssueID
	})
	ranks := make(map[int64]int, len(sorted))
	for i, h := range sorted {
		ranks[h.IssueID] = i + 1
	}
	return ranks
}

func maxOf(hits []Hit, value func(Hit) float64) float64 {
	var max float64
	for _, h := range hits {
		if v := value(h); v > max {
			max = v
		}
	}
	return max
}

// topicReuseRate is the share of hits whose topic type was also used by a
// different account in the same window.
func topicReuseRate(byBot map[int64]Hits) float64 {
	topicBots := map[string]map[int64]struct{}{}
	total := 0
	for botID, hits := range byBot {
		for _, h := range hits {
			if h.TopicType == "" {
				continue
			}
			total++
			if topicBots[h.TopicType] == nil {
				topicBots[h.TopicType] = map[int64]struct{}{}
			}
			topicBots[h.TopicType][botID] = struct{}{}
		}
	}
	if total == 0 {
		return 0
	}
	reused := 0
	for _, hits := range byBot {
		for _, h := range hits {
			if len(topicBots[h.TopicType]) > 1 {
				reused++
			}
		}
	}
	return float64(reused) / float64(total)
}

// homogeneousRatio is the share of published pieces whose title is a near
// duplicate of a piece on a different account.
func homogeneousRatio(contents []PublishedContent) float64 {
	if len(contents) == 0 {
		return 0
	}
	duplicated := 0
	for i, c := range contents {
		for j, other := range contents {
			if i == j || c.BotID == other.BotID {
				continue
			}
			if titleSimilarity(c.Title, other.Title) >= homogeneousThreshold {
				duplicated++
				break
			}
		}
	}
	return float64(duplicated) / float64(len(contents))
}

// titleSimilarity is the rune-bigram Jaccard similarity of two normalized
// titles. Digit/CJK-numeral runs collapse to "#" so "3步" and "三步" variants
// of the same title compare as duplicates.
func titleSimilarity(a, b string) float64 {
	ra, rb := titleBigrams(a), titleBigrams(b)
	if len(ra) == 0 || len(rb) == 0 {
		return 0
	}
	inter := 0
	for gram := range ra {
		if _, ok := rb[gram]; ok {
			inter++
		}
	}
	union := len(ra) + len(rb) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func isCJNumeral(r rune) bool {
	switch r {
	case '零', '一', '二', '三', '四', '五', '六', '七', '八', '九', '十', '百', '千', '万', '两':
		return true
	}
	return false
}

func titleBigrams(s string) map[string]struct{} {
	var norm []rune
	inDigit := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if unicode.IsDigit(r) || isCJNumeral(r) {
			if !inDigit {
				norm = append(norm, '#')
			}
			inDigit = true
			continue
		}
		inDigit = false
		if unicode.IsSpace(r) {
			continue
		}
		norm = append(norm, r)
	}
	grams := make(map[string]struct{}, len(norm))
	for i := 0; i+1 < len(norm); i++ {
		grams[string(norm[i:i+2])] = struct{}{}
	}
	return grams
}
