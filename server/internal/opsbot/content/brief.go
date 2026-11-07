// Package content builds content task briefs and (in later stages) drives the
// article, note and video production pipelines.
package content

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// Content formats. The format decides which acceptance checklist applies.
const (
	FormatArticle = "article"
	FormatNote    = "note"
	FormatVideo   = "video"
)

// Trigger sources for a brief (design spec §6.3).
const (
	TriggerFixed  = "fixed"  // scheduled cadence
	TriggerTrend  = "trend"  // hot-topic driven
	TriggerManual = "manual" // operator asked for it
)

// ErrInvalidBriefInput is returned for a brief request that cannot be built
// (missing bot/account/goal/audience, unknown format, no topic).
var ErrInvalidBriefInput = errors.New("content: invalid brief input")

var validFormats = map[string]bool{
	FormatArticle: true,
	FormatNote:    true,
	FormatVideo:   true,
}

// OpsContentBrief is the brief handed to the content pipeline (design spec
// §7.1). IssueID / Existing are filled by BriefBuilder.Build so a deduplicated
// call can return the issue that was already created.
type OpsContentBrief struct {
	Channel    string   `json:"channel"`
	AccountRef string   `json:"account_ref"`
	Goal       string   `json:"goal"`
	Audience   string   `json:"audience"`
	Format     string   `json:"format"`
	Topic      string   `json:"topic"`
	Acceptance []string `json:"acceptance"`
	SourceRefs []string `json:"source_refs"`
	DueDate    string   `json:"due_date"`
	Week       string   `json:"week"`
	IssueID    int64    `json:"issue_id"`
	Existing   bool     `json:"existing"`
}

// BriefInput is the request that produces a brief. Topic may be empty: the
// builder then takes the first trend signal. Week defaults to the current ISO
// week and DueDate to that week's Sunday.
type BriefInput struct {
	BotID      int64
	PackID     string
	AccountRef string
	Goal       string
	Audience   string
	Format     string
	Topic      string
	Trigger    string
	Week       string
	DueDate    string
	CreatedBy  int64
}

// BriefIssueInput describes the account-Epic child issue a brief lives on.
type BriefIssueInput struct {
	BotID       int64
	Title       string
	Description string
	DueDate     string
	CreatedBy   int64
}

// BriefSave is one append-only version of a brief bound to an issue.
type BriefSave struct {
	IssueID   int64
	BotID     int64
	Brief     OpsContentBrief
	DedupeKey string
	Trigger   string
	CreatedBy int64
}

// BriefStore is the persistence surface BriefBuilder needs. The real
// implementation is SQLBriefStore; tests inject an in-memory fake.
type BriefStore interface {
	// FindBriefByDedupeKey returns the issue whose latest brief carries key
	// for this bot.
	FindBriefByDedupeKey(ctx context.Context, botID int64, key string) (int64, bool, error)
	CreateBriefIssue(ctx context.Context, in BriefIssueInput) (int64, error)
	SaveBrief(ctx context.Context, in BriefSave) (int64, error)
	ReplaceAcceptanceChecklists(ctx context.Context, issueID, briefID int64, items []string) error
	DeleteIssue(ctx context.Context, issueID int64) error
}

// BriefBuilder turns brand knowledge, hit-content feedback and trend signals
// into an OpsContentBrief, creates the child issue under the account Epic and
// materialises the brief + acceptance checklist. Same account, same ISO week,
// same topic is created only once.
type BriefBuilder struct {
	// Knowledge returns brand-knowledge hits for a topic (brand tone, hit
	// content, feedback write-backs). Optional.
	Knowledge func(ctx context.Context, topic string) ([]string, error)
	// Trends returns hot-topic signals (info-radar / data sources). Optional,
	// but required when the request carries no topic.
	Trends func(ctx context.Context, query string) ([]string, error)
	Store  BriefStore
	// Now overrides the clock in tests.
	Now func() time.Time

	mu sync.Mutex
}

// Build produces the brief and its issue. A repeated request for the same
// account/week/topic returns the existing issue instead of creating a new one.
func (b *BriefBuilder) Build(ctx context.Context, in BriefInput) (OpsContentBrief, error) {
	in = in.normalized()
	if err := in.validate(); err != nil {
		return OpsContentBrief{}, err
	}
	if b.Store == nil {
		return OpsContentBrief{}, fmt.Errorf("%w: brief store is required", ErrInvalidBriefInput)
	}

	now := time.Now
	if b.Now != nil {
		now = b.Now
	}
	nowTime := now()

	var trends []string
	if b.Trends != nil {
		hits, err := b.Trends(ctx, in.trendQuery())
		if err != nil {
			return OpsContentBrief{}, fmt.Errorf("content: load trends: %w", err)
		}
		trends = hits
	}

	topic := in.Topic
	if topic == "" {
		topic = firstSignal(trends)
	}
	if topic == "" {
		return OpsContentBrief{}, fmt.Errorf("%w: topic is required (no trend signal available)", ErrInvalidBriefInput)
	}

	var knowledge []string
	if b.Knowledge != nil {
		hits, err := b.Knowledge(ctx, topic)
		if err != nil {
			return OpsContentBrief{}, fmt.Errorf("content: load knowledge: %w", err)
		}
		knowledge = hits
	}

	week := in.Week
	if week == "" {
		week = isoWeek(nowTime)
	}
	dueDate := in.DueDate
	if dueDate == "" {
		dueDate = weekDueDate(nowTime, week)
	}

	brief := OpsContentBrief{
		Channel:    in.PackID,
		AccountRef: in.AccountRef,
		Goal:       in.Goal,
		Audience:   in.Audience,
		Format:     in.Format,
		Topic:      topic,
		Acceptance: acceptanceFor(in.PackID, in.Format),
		SourceRefs: mergeRefs(knowledge, trends),
		DueDate:    dueDate,
		Week:       week,
	}
	key := dedupeKey(in.AccountRef, week, topic)

	// Serialise the lookup+create so two concurrent builds for the same key
	// cannot both observe "absent".
	b.mu.Lock()
	defer b.mu.Unlock()

	if issueID, ok, err := b.Store.FindBriefByDedupeKey(ctx, in.BotID, key); err != nil {
		return OpsContentBrief{}, fmt.Errorf("content: dedupe lookup: %w", err)
	} else if ok {
		brief.IssueID = issueID
		brief.Existing = true
		return brief, nil
	}

	issueID, err := b.Store.CreateBriefIssue(ctx, BriefIssueInput{
		BotID:       in.BotID,
		Title:       issueTitle(in.PackID, week, topic),
		Description: briefDescription(brief),
		DueDate:     dueDate,
		CreatedBy:   in.CreatedBy,
	})
	if err != nil {
		return OpsContentBrief{}, fmt.Errorf("content: create brief issue: %w", err)
	}
	brief.IssueID = issueID

	briefID, err := b.Store.SaveBrief(ctx, BriefSave{
		IssueID:   issueID,
		BotID:     in.BotID,
		Brief:     brief,
		DedupeKey: key,
		Trigger:   in.Trigger,
		CreatedBy: in.CreatedBy,
	})
	if err != nil {
		cleanupErr := b.Store.DeleteIssue(ctx, issueID)
		return OpsContentBrief{}, errors.Join(fmt.Errorf("content: save brief: %w", err), wrapCleanup(cleanupErr))
	}

	if err := b.Store.ReplaceAcceptanceChecklists(ctx, issueID, briefID, brief.Acceptance); err != nil {
		cleanupErr := b.Store.DeleteIssue(ctx, issueID)
		return OpsContentBrief{}, errors.Join(fmt.Errorf("content: write acceptance checklist: %w", err), wrapCleanup(cleanupErr))
	}

	return brief, nil
}

func (in BriefInput) normalized() BriefInput {
	in.PackID = strings.TrimSpace(in.PackID)
	in.AccountRef = strings.TrimSpace(in.AccountRef)
	in.Goal = strings.TrimSpace(in.Goal)
	in.Audience = strings.TrimSpace(in.Audience)
	in.Format = strings.ToLower(strings.TrimSpace(in.Format))
	in.Topic = strings.TrimSpace(in.Topic)
	in.Trigger = strings.TrimSpace(in.Trigger)
	in.Week = strings.TrimSpace(in.Week)
	in.DueDate = strings.TrimSpace(in.DueDate)
	return in
}

func (in BriefInput) validate() error {
	var missing []string
	if in.BotID <= 0 {
		missing = append(missing, "bot_id")
	}
	if in.PackID == "" {
		missing = append(missing, "pack_id")
	}
	if in.AccountRef == "" {
		missing = append(missing, "account_ref")
	}
	if in.Goal == "" {
		missing = append(missing, "goal")
	}
	if in.Audience == "" {
		missing = append(missing, "audience")
	}
	if in.Format == "" {
		missing = append(missing, "format")
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: missing %s", ErrInvalidBriefInput, strings.Join(missing, ", "))
	}
	if !validFormats[in.Format] {
		return fmt.Errorf("%w: format %q must be article, note or video", ErrInvalidBriefInput, in.Format)
	}
	if in.Week != "" {
		if _, _, ok := parseISOWeek(in.Week); !ok {
			return fmt.Errorf("%w: week %q must be YYYY-Www", ErrInvalidBriefInput, in.Week)
		}
	}
	if in.DueDate != "" {
		if _, err := time.Parse("2006-01-02", in.DueDate); err != nil {
			return fmt.Errorf("%w: due_date %q must be YYYY-MM-DD", ErrInvalidBriefInput, in.DueDate)
		}
	}
	return nil
}

func (in BriefInput) trendQuery() string {
	if in.Topic != "" {
		return in.Topic
	}
	return strings.TrimSpace(in.Goal + " " + in.Audience)
}

func firstSignal(signals []string) string {
	for _, s := range signals {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}

// mergeRefs keeps the first occurrence of each reference, knowledge first.
func mergeRefs(groups ...[]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, group := range groups {
		for _, ref := range group {
			ref = strings.TrimSpace(ref)
			if ref == "" || seen[ref] {
				continue
			}
			seen[ref] = true
			out = append(out, ref)
		}
	}
	return out
}

// dedupeKey normalises the account/week/topic triple so cosmetic whitespace
// and case differences do not produce a second issue.
func dedupeKey(accountRef, week, topic string) string {
	norm := func(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }
	return norm(accountRef) + "|" + week + "|" + norm(topic)
}

func issueTitle(packID, week, topic string) string {
	return fmt.Sprintf("[%s] %s · %s", channelLabel(packID), week, topic)
}

func channelLabel(packID string) string {
	switch packID {
	case "wechat_mp":
		return "公众号"
	case "xiaohongshu":
		return "小红书"
	default:
		return packID
	}
}

func briefDescription(b OpsContentBrief) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "目标：%s\n受众：%s\n格式：%s\n选题：%s\n周次：%s\n截止：%s\n", b.Goal, b.Audience, b.Format, b.Topic, b.Week, b.DueDate)
	sb.WriteString("\n验收项：\n")
	for _, item := range b.Acceptance {
		fmt.Fprintf(&sb, "- %s\n", item)
	}
	if len(b.SourceRefs) > 0 {
		sb.WriteString("\n来源引用：\n")
		for _, ref := range b.SourceRefs {
			fmt.Fprintf(&sb, "- %s\n", ref)
		}
	}
	return sb.String()
}

// acceptanceFor returns the platform checklist. Articles, notes and videos
// differ by construction; video limits also follow the pack.
func acceptanceFor(packID, format string) []string {
	switch format {
	case FormatArticle:
		return []string{
			"标题不超过 64 个字符，含核心关键词且不堆砌",
			"正文关键结论均有来源标注（引用原文、数据出处或链接）",
			"封面图 2.35:1（900×383），主体清晰无裁切",
			"排版符合公众号规范：正文 15-16px、行距 1.75、段落留白、重点加粗",
		}
	case FormatNote:
		return []string{
			"标题不超过 20 个字符，含 1 个核心钩子",
			"图片 3-9 张，首图封面比例 3:4 且主体完整",
			"标签 5-10 个，含品牌词与 2 个以上话题词",
			"正文前 2 行给出钩子，结尾有明确 CTA",
		}
	case FormatVideo:
		minutes, ratio := "15", "9:16"
		switch packID {
		case "xiaohongshu":
			ratio = "3:4"
		case "wechat_mp":
			minutes, ratio = "30", "16:9"
		}
		return []string{
			"成片时长 ≤ " + minutes + " 分钟，节奏不拖沓",
			"画面比例为 " + ratio + "，全程无黑边",
			"全程字幕完整，无错漏与遮挡",
			"封面单独生成，尺寸与比例符合平台要求",
		}
	default:
		return nil
	}
}

func isoWeek(t time.Time) string {
	year, week := t.ISOWeek()
	return fmt.Sprintf("%04d-W%02d", year, week)
}

// parseISOWeek parses YYYY-Www into (year, week).
func parseISOWeek(value string) (int, int, bool) {
	parts := strings.SplitN(value, "-W", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	year, err := strconv.Atoi(parts[0])
	if err != nil || year < 1 {
		return 0, 0, false
	}
	week, err := strconv.Atoi(parts[1])
	if err != nil || week < 1 || week > 53 {
		return 0, 0, false
	}
	return year, week, true
}

// weekDueDate returns the Sunday ending the given ISO week. An unparseable
// week falls back to the week containing now.
func weekDueDate(now time.Time, week string) string {
	year, weekNumber, ok := parseISOWeek(week)
	if !ok {
		year, weekNumber = now.ISOWeek()
	}
	// Every ISO week 1 contains January 4th.
	jan4 := time.Date(year, time.January, 4, 0, 0, 0, 0, now.Location())
	weekday := int(jan4.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	mondayWeek1 := jan4.AddDate(0, 0, -(weekday - 1))
	monday := mondayWeek1.AddDate(0, 0, (weekNumber-1)*7)
	return monday.AddDate(0, 0, 6).Format("2006-01-02")
}

func wrapCleanup(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("content: rollback issue: %w", err)
}

// SQLBriefStore persists briefs through sqlc queries. Content issues are
// children of the bot's control Epic and live in the Epic's board column.
type SQLBriefStore struct {
	Q *store.Queries
}

var _ BriefStore = (*SQLBriefStore)(nil)

// FindBriefByDedupeKey walks the bot's Epic children and compares each latest
// brief's provenance dedupe key.
func (s *SQLBriefStore) FindBriefByDedupeKey(ctx context.Context, botID int64, key string) (int64, bool, error) {
	q, err := s.queries()
	if err != nil {
		return 0, false, err
	}
	bot, err := q.GetOpsBot(ctx, botID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if !bot.EpicIssueID.Valid {
		return 0, false, nil
	}
	children, err := q.ListIssuesByParent(ctx, bot.EpicIssueID)
	if err != nil {
		return 0, false, err
	}
	for _, issue := range children {
		brief, err := q.GetLatestTaskBrief(ctx, issue.ID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return 0, false, err
		}
		if provenanceDedupeKey(brief.ProvenanceJson) == key {
			return issue.ID, true, nil
		}
	}
	return 0, false, nil
}

func (s *SQLBriefStore) CreateBriefIssue(ctx context.Context, in BriefIssueInput) (int64, error) {
	q, err := s.queries()
	if err != nil {
		return 0, err
	}
	bot, err := q.GetOpsBot(ctx, in.BotID)
	if err != nil {
		return 0, err
	}
	if !bot.EpicIssueID.Valid {
		return 0, fmt.Errorf("content: bot %d has no control epic", in.BotID)
	}
	epic, err := q.GetIssue(ctx, bot.EpicIssueID.Int64)
	if err != nil {
		return 0, err
	}
	issue, err := q.CreateIssue(ctx, store.CreateIssueParams{
		ColumnID:      epic.ColumnID,
		Title:         in.Title,
		Description:   nullString(in.Description),
		DueDate:       nullString(in.DueDate),
		ParentIssueID: bot.EpicIssueID,
		CreatedBy:     nullInt64(in.CreatedBy),
	})
	if err != nil {
		return 0, err
	}
	return issue.ID, nil
}

func (s *SQLBriefStore) SaveBrief(ctx context.Context, in BriefSave) (int64, error) {
	q, err := s.queries()
	if err != nil {
		return 0, err
	}
	brief := in.Brief
	acceptanceJSON, err := json.Marshal(brief.Acceptance)
	if err != nil {
		return 0, err
	}
	scopeInJSON, err := json.Marshal([]string{
		"topic:" + brief.Topic,
		"format:" + brief.Format,
		"audience:" + brief.Audience,
	})
	if err != nil {
		return 0, err
	}
	deliveryJSON, err := json.Marshal(map[string]any{
		"channel":     brief.Channel,
		"account_ref": brief.AccountRef,
		"format":      brief.Format,
		"due_date":    brief.DueDate,
		"week":        brief.Week,
	})
	if err != nil {
		return 0, err
	}
	provenanceJSON, err := json.Marshal(map[string]any{
		"source_refs": brief.SourceRefs,
		"trigger":     in.Trigger,
		"dedupe_key":  in.DedupeKey,
		"bot_id":      in.BotID,
		"week":        brief.Week,
	})
	if err != nil {
		return 0, err
	}
	row, err := q.CreateTaskBrief(ctx, store.CreateTaskBriefParams{
		IssueID:        in.IssueID,
		IssueID_2:      in.IssueID,
		Goal:           brief.Goal,
		AcceptanceJson: string(acceptanceJSON),
		ScopeInJson:    string(scopeInJSON),
		ScopeOutJson:   "[]",
		DeliveryJson:   string(deliveryJSON),
		ProvenanceJson: string(provenanceJSON),
		CreatedBy:      nullInt64(in.CreatedBy),
	})
	if err != nil {
		return 0, err
	}
	return row.ID, nil
}

func (s *SQLBriefStore) ReplaceAcceptanceChecklists(ctx context.Context, issueID, briefID int64, items []string) error {
	q, err := s.queries()
	if err != nil {
		return err
	}
	if err := q.DeleteAcceptanceChecklistsForBrief(ctx, sql.NullInt64{Int64: briefID, Valid: briefID > 0}); err != nil {
		return err
	}
	for _, item := range items {
		if _, err := q.CreateChecklistWithKind(ctx, store.CreateChecklistWithKindParams{
			IssueID:   issueID,
			Title:     item,
			IssueID_2: issueID,
			Kind:      "acceptance",
			BriefID:   sql.NullInt64{Int64: briefID, Valid: briefID > 0},
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLBriefStore) DeleteIssue(ctx context.Context, issueID int64) error {
	q, err := s.queries()
	if err != nil {
		return err
	}
	return q.DeleteIssue(ctx, issueID)
}

func (s *SQLBriefStore) queries() (*store.Queries, error) {
	if s == nil || s.Q == nil {
		return nil, errors.New("content: SQLBriefStore has no queries")
	}
	return s.Q, nil
}

func provenanceDedupeKey(raw string) string {
	var provenance struct {
		DedupeKey string `json:"dedupe_key"`
	}
	_ = json.Unmarshal([]byte(raw), &provenance)
	return provenance.DedupeKey
}

func nullInt64(v int64) sql.NullInt64 {
	if v <= 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: v, Valid: true}
}

func nullString(v string) sql.NullString {
	if v == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: v, Valid: true}
}
