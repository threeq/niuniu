package opsbot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/niuniu-dev/niuniu/internal/opsbot/packs"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// Onboarding conversation stages. The flow is
// intent -> pack -> account -> goal -> cadence -> budget -> approval ->
// credential -> summary (design §13.1); a channel with no Pack inserts the
// draft/test/publish branch, and 小红书 additionally asks about a third-party
// data source and the video model.
const (
	stageChannel       = "channel"
	stageAccountName   = "account_name"
	stageDataSource    = "data_source"
	stageVideoModel    = "video_model"
	stageGoal          = "goal"
	stageCadence       = "cadence"
	stageBudget        = "budget"
	stageApproval      = "approval"
	stageCredential    = "credential"
	stagePackDraft     = "pack_draft"
	stagePackQuestions = "pack_questions"
	stagePackTest      = "pack_test"
)

const defaultSessionTTL = 30 * time.Minute

// PackQuestion is one missing fact a generated draft still needs. The
// conversation asks them one at a time and never fills them in by guessing.
type PackQuestion struct {
	Key    string
	Prompt string
}

// PackDraft is a generated Pack draft for a channel that has no installed Pack.
type PackDraft struct {
	PackID       string
	Version      int64
	DisplayName  string
	Capabilities []string
	Risks        []string
	HasVideo     bool
	// Questions is non-empty while the draft still lacks confirmed facts.
	Questions []PackQuestion
}

// PackGenerator drafts, sandbox-tests and publishes a Platform Pack for a new
// channel (design §13.2). Optional: without it onboarding only accepts channels
// that already have a published Pack.
type PackGenerator interface {
	Draft(ctx context.Context, owner OwnerRef, channel string) (PackDraft, error)
	Test(ctx context.Context, owner OwnerRef, packID string, version int64) error
	Publish(ctx context.Context, owner OwnerRef, packID string, version int64) error
}

// AnswerablePackGenerator can re-draft with answers collected in conversation.
// Onboarding uses it to ask each PackQuestion in turn; a generator that does
// not implement it simply drafts from the channel name alone.
type AnswerablePackGenerator interface {
	PackGenerator
	DraftWithAnswers(ctx context.Context, owner OwnerRef, channel string, answers map[string]string) (PackDraft, error)
}

// CredentialLinker mints a one-time authorization link (or QR task) for an
// account. It must never accept or return a plaintext secret: the conversation
// only ever carries the link.
type CredentialLinker interface {
	IssueAccountLink(ctx context.Context, owner OwnerRef, botID int64, packID, accountRef string) (string, error)
}

type CadenceConfig struct {
	PostsPerWeek float64
	HotSpot      bool
	Text         string
}

type BudgetConfig struct {
	Limit     float64
	Currency  string
	Unlimited bool
	Text      string
}

type ApprovalConfig struct {
	Publish bool
	Reply   bool
	Video   bool
	Text    string
}

// Session is one chat's in-progress onboarding conversation. Stage is exported
// so callers/tests can inspect where the flow stopped.
type Session struct {
	ChatID    int64
	Owner     OwnerRef
	ProjectID int64
	Stage     string
	UpdatedAt time.Time

	PackID      string
	PackVersion int64
	Channel     string
	HasVideo    bool

	AccountName string
	DataSource  bool
	VideoModel  string
	Goal        string
	Cadence     CadenceConfig
	Budget      BudgetConfig
	Approval    ApprovalConfig

	BotID        int64
	EpicIssueID  int64
	WorkspaceID  int64
	DraftVersion int64

	// Pack generation Q&A for a channel with no installed Pack.
	ChannelInput      string
	PackQuestions     []PackQuestion
	PackQuestionTotal int
	PackAnswers       map[string]string
}

// Onboarding is the conversational setup/configuration surface used by the IM
// bot. Sessions are keyed by chat and expire after 30 minutes of inactivity.
type Onboarding struct {
	Service     *Service
	Provisioner *Provisioner // optional: creates the control Epic/workspace
	Approvals   *Approval    // optional: backs /approvals
	Risk        *Risk        // optional: gates /resume on a re-login check
	Generator   PackGenerator
	Credentials CredentialLinker
	// InstallPacks seeds an owner's built-in Packs on first use (idempotent).
	// Optional: nil means Packs must already be installed.
	InstallPacks func(ctx context.Context, ownerType string, ownerID int64) error

	// Now overrides the clock in tests. Optional.
	Now func() time.Time

	mu       sync.Mutex
	sessions map[int64]*Session
}

// NewOnboarding builds the state machine over the ops bot service.
func NewOnboarding(service *Service) *Onboarding {
	return &Onboarding{Service: service, sessions: map[int64]*Session{}}
}

// Handle advances the conversation for one chat. A reply that is returned as
// the empty string means onboarding did not claim the message, so the caller
// must keep its normal intake path. done reports that the flow completed.
func (o *Onboarding) Handle(ctx context.Context, chatID int64, owner OwnerRef, projectID int64, text string) (string, bool, error) {
	if o == nil || o.Service == nil {
		return "", false, nil
	}
	owner = owner.normalized()
	if err := owner.validate(); err != nil {
		return "", false, err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", false, nil
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	if o.sessions == nil {
		o.sessions = map[int64]*Session{}
	}

	s := o.liveSessionLocked(chatID)

	if isCancel(text) {
		if s == nil {
			return "当前没有进行中的接入流程。", false, nil
		}
		delete(o.sessions, chatID)
		return "已取消当前接入流程。", false, nil
	}

	if s == nil {
		if handled, reply, err := o.handleCommandLocked(ctx, chatID, owner, text); handled {
			return reply, false, err
		}
		channel, ok := matchOnboardingIntent(text)
		if !ok {
			return "", false, nil
		}
		s = &Session{ChatID: chatID, Owner: owner, ProjectID: projectID, UpdatedAt: o.now()}
		o.sessions[chatID] = s
		if channel == "" {
			s.Stage = stageChannel
			return "要接入哪个渠道？可选：公众号、小红书、Facebook、知乎；也可以说「接入新渠道 <名称>」。", false, nil
		}
		return o.beginChannelLocked(ctx, s, channel)
	}

	s.UpdatedAt = o.now()
	return o.advanceLocked(ctx, s, text)
}

// Matches reports whether Handle would claim this text: an onboarding command,
// an entry-point intent, or a reply inside an active session. Callers use it to
// deny an unauthorized operator without starting or advancing a flow.
func (o *Onboarding) Matches(chatID int64, text string) bool {
	if o == nil || o.Service == nil {
		return false
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if isCancel(text) {
		return true
	}
	if o.liveSessionLocked(chatID) != nil {
		return true
	}
	if strings.HasPrefix(text, "/") {
		switch cmd, _ := splitSlash(text); cmd {
		case "bots", "bot", "approvals", "pause", "resume", "budget", "calendar", "cancel":
			return true
		}
		return false
	}
	_, ok := matchOnboardingIntent(text)
	return ok
}

func (o *Onboarding) advanceLocked(ctx context.Context, s *Session, text string) (string, bool, error) {
	switch s.Stage {
	case stageChannel:
		return o.beginChannelLocked(ctx, s, text)

	case stageAccountName:
		name := strings.TrimSpace(text)
		if name == "" {
			return "账号名称是？", false, nil
		}
		s.AccountName = clipRunes(name, 60)
		return o.afterAccountLocked(s)

	case stageDataSource:
		s.DataSource = isAffirmative(text)
		return o.afterDataSourceLocked(s)

	case stageVideoModel:
		s.VideoModel = parseVideoModel(text)
		s.Stage = stageGoal
		return "主要目标是涨粉、转化还是品牌？", false, nil

	case stageGoal:
		s.Goal = strings.TrimSpace(text)
		s.Stage = stageCadence
		return "每周发几篇？要热点加更吗？", false, nil

	case stageCadence:
		s.Cadence = parseCadence(text)
		s.Stage = stageBudget
		return "每月预算多少？（回复「不限」表示不限额）", false, nil

	case stageBudget:
		s.Budget = parseBudget(text)
		s.Stage = stageApproval
		return "发布前需要你确认吗？（回复「需要」开启全部审批，或「不需要」跳过）", false, nil

	case stageApproval:
		s.Approval = parseApproval(text)
		return o.finishLocked(ctx, s)

	case stageCredential:
		return o.confirmCredentialLocked(ctx, s)

	case stagePackQuestions:
		return o.answerPackQuestionLocked(ctx, s, text)

	case stagePackDraft:
		if isCancel(text) || containsAny(text, "取消", "不继续", "放弃", "no", "nope") {
			delete(o.sessions, s.ChatID)
			return "已取消。", false, nil
		}
		s.Stage = stagePackTest
		return "是否在测试账号上跑一遍？未通过沙箱测试不能发布。回复「跑」开始测试，或「/cancel」放弃。", false, nil

	case stagePackTest:
		// A Pack that was never sandbox-tested is not publishable. The
		// generator enforces this too; the conversation must not offer a
		// shortcut around it.
		if !isAffirmative(text) || strings.Contains(text, "跳过") {
			return "未通过沙箱测试的 Pack 不能发布。回复「跑」重新测试，或「/cancel」放弃。", false, nil
		}
		if o.Generator == nil {
			return "没有配置 Pack 生成器，无法执行测试。", false, nil
		}
		version := s.DraftVersion
		if version <= 0 {
			version = 1
		}
		if err := o.Generator.Test(ctx, s.Owner, s.PackID, version); err != nil {
			return fmt.Sprintf("沙箱测试未通过：%v。修复草案后回复「跑」重试，或「/cancel」放弃。", err), false, nil
		}
		return o.publishDraftLocked(ctx, s, "测试通过。")
	}
	return "", false, nil
}

func (o *Onboarding) afterAccountLocked(s *Session) (string, bool, error) {
	if s.PackID == "xiaohongshu" {
		s.Stage = stageDataSource
		return "是否接入第三方合规数据源（竞品/行业）？回复「接入」或「不接入」（默认不接入）。", false, nil
	}
	if s.HasVideo {
		s.Stage = stageVideoModel
		return "视频生成使用默认 video-gen MCP，还是指定其他 TTS/视频模型 provider？回复「默认」或 provider 名称。", false, nil
	}
	s.Stage = stageGoal
	return "主要目标是涨粉、转化还是品牌？", false, nil
}

func (o *Onboarding) afterDataSourceLocked(s *Session) (string, bool, error) {
	if s.HasVideo {
		s.Stage = stageVideoModel
		return "视频生成使用默认 video-gen MCP，还是指定其他 TTS/视频模型 provider？回复「默认」或 provider 名称。", false, nil
	}
	s.Stage = stageGoal
	return "主要目标是涨粉、转化还是品牌？", false, nil
}

func (o *Onboarding) beginChannelLocked(ctx context.Context, s *Session, channelName string) (string, bool, error) {
	if o.InstallPacks != nil {
		_ = o.InstallPacks(ctx, s.Owner.Type, s.Owner.ID)
	}
	packID, display, known := normalizeChannel(channelName)
	if o.Service.Packs != nil {
		m, err := o.Service.Packs.LatestPublished(s.Owner.Type, s.Owner.ID, packID)
		if err == nil && m.ID == packID && m.Version > 0 {
			s.PackID = packID
			s.PackVersion = int64(m.Version)
			s.Channel = display
			s.HasVideo = packHasVideo(m)
			s.Stage = stageAccountName
			return fmt.Sprintf("已选定「%s」Pack %s v%d。账号名称是？", display, packID, m.Version), false, nil
		}
	}
	return o.startDraftLocked(ctx, s, channelName, packID, display, known)
}

func (o *Onboarding) startDraftLocked(ctx context.Context, s *Session, channelName, packID, display string, known bool) (string, bool, error) {
	if o.Generator == nil {
		delete(o.sessions, s.ChatID)
		if known {
			return fmt.Sprintf("渠道「%s」还没有安装可用的 Pack，且未配置 Pack 生成器。请先在 Pack 管理里安装或导入。", display), false, nil
		}
		return fmt.Sprintf("暂不支持渠道「%s」：还没有可用的 Pack，也未配置 Pack 生成器。", display), false, nil
	}

	draft, err := o.Generator.Draft(ctx, s.Owner, channelName)
	if err != nil {
		delete(o.sessions, s.ChatID)
		return "", false, err
	}

	// A generator may need facts the channel name alone cannot supply. Ask them
	// one at a time; nothing is filled in silently.
	if len(draft.Questions) > 0 {
		if _, ok := o.Generator.(AnswerablePackGenerator); ok {
			s.ChannelInput = channelName
			s.PackQuestions = draft.Questions
			s.PackQuestionTotal = len(draft.Questions)
			s.PackAnswers = map[string]string{}
			s.Stage = stagePackQuestions
			return o.askPackQuestionLocked(s), false, nil
		}
	}

	return o.applyPackDraftLocked(ctx, s, draft, packID, display)
}

func (o *Onboarding) askPackQuestionLocked(s *Session) string {
	if len(s.PackQuestions) == 0 {
		return "没有待确认的信息。"
	}
	done := s.PackQuestionTotal - len(s.PackQuestions)
	return fmt.Sprintf("生成 Pack 前还需要确认（%d/%d）：%s\n（回复「/cancel」放弃）",
		done+1, s.PackQuestionTotal, s.PackQuestions[0].Prompt)
}

func (o *Onboarding) answerPackQuestionLocked(ctx context.Context, s *Session, text string) (string, bool, error) {
	if len(s.PackQuestions) == 0 {
		s.Stage = stagePackDraft
		return "已收集完信息，继续生成 Pack。", false, nil
	}
	question := s.PackQuestions[0]
	s.PackQuestions = s.PackQuestions[1:]
	if s.PackAnswers == nil {
		s.PackAnswers = map[string]string{}
	}
	s.PackAnswers[question.Key] = strings.TrimSpace(text)
	if len(s.PackQuestions) > 0 {
		return o.askPackQuestionLocked(s), false, nil
	}

	answerable, ok := o.Generator.(AnswerablePackGenerator)
	if !ok {
		delete(o.sessions, s.ChatID)
		return "当前生成器不支持补充信息，无法继续。", false, nil
	}
	draft, err := answerable.DraftWithAnswers(ctx, s.Owner, s.ChannelInput, s.PackAnswers)
	if err != nil {
		delete(o.sessions, s.ChatID)
		return "生成 Pack 失败：" + err.Error(), false, nil
	}
	if len(draft.Questions) > 0 {
		s.PackQuestions = draft.Questions
		s.PackQuestionTotal = len(draft.Questions)
		return o.askPackQuestionLocked(s), false, nil
	}
	packID, display, _ := normalizeChannel(s.ChannelInput)
	s.PackAnswers = nil
	s.PackQuestions = nil
	return o.applyPackDraftLocked(ctx, s, draft, packID, display)
}

func (o *Onboarding) applyPackDraftLocked(ctx context.Context, s *Session, draft PackDraft, packID, display string) (string, bool, error) {
	if draft.PackID != "" {
		s.PackID = draft.PackID
	} else {
		s.PackID = packID
	}
	if draft.DisplayName != "" {
		display = draft.DisplayName
	}
	s.DraftVersion = draft.Version
	if s.DraftVersion <= 0 {
		s.DraftVersion = 1
	}
	s.Channel = display
	s.HasVideo = draft.HasVideo
	s.Stage = stagePackDraft

	var b strings.Builder
	fmt.Fprintf(&b, "检测到「%s」渠道，已生成 Pack 草稿 %s v%d。\n能力矩阵：\n", display, s.PackID, s.DraftVersion)
	for _, cap := range draft.Capabilities {
		fmt.Fprintf(&b, "- %s\n", cap)
	}
	if len(draft.Risks) > 0 {
		fmt.Fprintf(&b, "风险：%s\n", strings.Join(draft.Risks, "；"))
	}
	b.WriteString("是否继续？回复「继续」或「取消」。")
	return b.String(), false, nil
}

func (o *Onboarding) publishDraftLocked(ctx context.Context, s *Session, prefix string) (string, bool, error) {
	version := s.DraftVersion
	if version <= 0 {
		version = 1
	}
	if o.Generator == nil {
		return "没有配置 Pack 生成器，无法发布。", false, nil
	}
	if err := o.Generator.Publish(ctx, s.Owner, s.PackID, version); err != nil {
		return fmt.Sprintf("Pack 发布失败：%v。修复后回复「跑」重试，或「/cancel」放弃。", err), false, nil
	}
	s.PackVersion = version
	s.Stage = stageAccountName
	return fmt.Sprintf("%s已发布 Pack %s v%d。现在要绑定哪个账号？账号名称是？", prefix, s.PackID, version), false, nil
}

func (o *Onboarding) finishLocked(ctx context.Context, s *Session) (string, bool, error) {
	goalsJSON := marshalJSON(map[string]any{"type": goalType(s.Goal), "text": s.Goal})
	cadenceJSON := marshalJSON(map[string]any{
		"posts_per_week": s.Cadence.PostsPerWeek,
		"hot_spot_boost": s.Cadence.HotSpot,
		"text":           s.Cadence.Text,
	})
	budgetJSON := marshalJSON(map[string]any{
		"currency":      s.Budget.Currency,
		"monthly_limit": s.Budget.Limit,
		"unlimited":     s.Budget.Unlimited,
	})
	approvalJSON := marshalJSON(map[string]bool{
		"publish": s.Approval.Publish,
		"reply":   s.Approval.Reply,
		"video":   s.Approval.Video,
	})

	if s.ProjectID <= 0 {
		delete(o.sessions, s.ChatID)
		return "", false, fmt.Errorf("opsbot: onboarding must start from a project so the bot has a control Epic")
	}
	bot, err := o.Service.CreateAccountBot(ctx, CreateAccountInput{
		Owner:              s.Owner,
		PackID:             s.PackID,
		PackVersion:        s.PackVersion,
		AccountRef:         s.AccountName,
		DisplayName:        s.AccountName,
		KnowledgeScopeJSON: s.knowledgeScopeJSON(),
		GoalsJSON:          goalsJSON,
		CadenceJSON:        cadenceJSON,
		ApprovalPolicyJSON: approvalJSON,
		BudgetJSON:         budgetJSON,
		CreatedBy:          s.Owner.ID,
	})
	if err != nil {
		delete(o.sessions, s.ChatID)
		if errors.Is(err, ErrAccountExists) {
			return fmt.Sprintf("「%s」在 %s 下已存在，未重复创建。可用 /bots 查看。", s.AccountName, s.Channel), false, nil
		}
		return "", false, err
	}

	note := ""
	if o.Provisioner != nil {
		epicID, wsID, perr := o.Provisioner.ProvisionAccount(
			ctx, s.Owner.Type, s.Owner.ID, s.ProjectID, bot.ID, bot.DisplayName, s.Owner.ID)
		if perr != nil {
			note = fmt.Sprintf("\n注意：控制工作空间创建失败（%v），后续会自动重试。", perr)
		} else {
			s.EpicIssueID, s.WorkspaceID = epicID, wsID
		}
	}

	if o.Credentials != nil {
		link, lerr := o.Credentials.IssueAccountLink(ctx, s.Owner, bot.ID, s.PackID, s.AccountName)
		if lerr != nil {
			note += fmt.Sprintf("\n注意：一次性授权链接生成失败（%v）。", lerr)
		} else {
			s.BotID = bot.ID
			s.Stage = stageCredential
			return fmt.Sprintf("账号已登记。请点这个一次性链接完成「%s」授权：\n%s\n完成后回复「完成」。链接不会接收任何明文密钥。%s",
				s.Channel, link, note), false, nil
		}
	}

	delete(o.sessions, s.ChatID)
	return o.summary(bot, s.EpicIssueID, s.WorkspaceID) + note, true, nil
}

// confirmCredentialLocked finishes the flow after the operator completed the
// out-of-band authorization. The user's text is deliberately never stored.
func (o *Onboarding) confirmCredentialLocked(ctx context.Context, s *Session) (string, bool, error) {
	delete(o.sessions, s.ChatID)
	bot, err := o.Service.Get(ctx, s.BotID)
	if err != nil {
		return "账号已登记，可用 /bots 查看。", true, nil
	}
	return o.summary(bot, s.EpicIssueID, s.WorkspaceID), true, nil
}

func (o *Onboarding) summary(bot store.OpsBot, epicID, wsID int64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "已创建「%s」Bot。\n", bot.DisplayName)
	fmt.Fprintf(&b, "- Pack：%s v%d\n", bot.PackID, bot.PackVersion)
	fmt.Fprintf(&b, "- 目标：%s\n", goalLabel(bot.GoalsJson))
	fmt.Fprintf(&b, "- 节奏：%s\n", cadenceLabel(bot.CadenceJson))
	fmt.Fprintf(&b, "- 预算：%s\n", budgetLabel(bot.BudgetJson))
	fmt.Fprintf(&b, "- 审批：%s\n", approvalLabel(bot.ApprovalPolicyJson))
	if epicID > 0 && wsID > 0 {
		fmt.Fprintf(&b, "- 控制空间：Epic #%d / Workspace #%d\n", epicID, wsID)
	}
	b.WriteString("可随时用 /bots、/budget、/pause、/resume 查看和调整。")
	return b.String()
}

func (s *Session) knowledgeScopeJSON() string {
	scope := map[string]any{}
	if s.PackID == "xiaohongshu" {
		scope["data_source"] = map[string]any{"enabled": s.DataSource}
	}
	if s.HasVideo {
		model := s.VideoModel
		if model == "" {
			model = "default"
		}
		scope["video_model"] = map[string]any{"provider": model}
	}
	if len(scope) == 0 {
		return "{}"
	}
	return marshalJSON(scope)
}

// --- configuration commands ---------------------------------------------

func (o *Onboarding) handleCommandLocked(ctx context.Context, chatID int64, owner OwnerRef, text string) (bool, string, error) {
	if !strings.HasPrefix(text, "/") {
		return false, "", nil
	}
	cmd, rest := splitSlash(text)
	switch cmd {
	case "bots":
		reply, err := o.listBotsReply(ctx, owner)
		return true, reply, err
	case "bot":
		reply, err := o.botReply(ctx, chatID, owner, rest)
		return true, reply, err
	case "approvals":
		reply, err := o.approvalsReply(ctx, owner)
		return true, reply, err
	case "pause", "resume":
		reply, err := o.statusReply(ctx, chatID, owner, rest, cmd == "pause")
		return true, reply, err
	case "budget":
		reply, err := o.budgetReply(ctx, chatID, owner, rest)
		return true, reply, err
	case "calendar":
		return true, "内容日历将在首篇内容生成后可用；当前可用 /bots 查看各账号节奏。", nil
	}
	return false, "", nil
}

func (o *Onboarding) listBotsReply(ctx context.Context, owner OwnerRef) (string, error) {
	bots, err := o.Service.ListByOwner(ctx, owner)
	if err != nil {
		return "", err
	}
	if len(bots) == 0 {
		return "还没有运营 Bot。可以说「接入公众号」或「接入小红书」开始。", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "运营 Bot（%d）：\n", len(bots))
	for _, bot := range bots {
		if bot.Role == RoleDirector {
			fmt.Fprintf(&b, "- #%d %s（总监，%s）\n", bot.ID, bot.DisplayName, bot.Status)
			continue
		}
		fmt.Fprintf(&b, "- #%d %s（%s v%d，%s）\n", bot.ID, bot.DisplayName, bot.PackID, bot.PackVersion, bot.Status)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func (o *Onboarding) botReply(ctx context.Context, chatID int64, owner OwnerRef, arg string) (string, error) {
	bot, err := o.resolveBot(ctx, chatID, owner, arg)
	if err != nil {
		return err.Error(), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "#%d %s（%s，%s）\n", bot.ID, bot.DisplayName, bot.Role, bot.Status)
	if bot.PackID != "" {
		fmt.Fprintf(&b, "- Pack：%s v%d\n", bot.PackID, bot.PackVersion)
	}
	fmt.Fprintf(&b, "- 目标：%s\n", goalLabel(bot.GoalsJson))
	fmt.Fprintf(&b, "- 节奏：%s\n", cadenceLabel(bot.CadenceJson))
	fmt.Fprintf(&b, "- 预算：%s\n", budgetLabel(bot.BudgetJson))
	fmt.Fprintf(&b, "- 审批：%s", approvalLabel(bot.ApprovalPolicyJson))
	return b.String(), nil
}

func (o *Onboarding) approvalsReply(ctx context.Context, owner OwnerRef) (string, error) {
	if o.Approvals == nil {
		return "审批服务未启用。", nil
	}
	reqs, err := o.Approvals.ListPending(ctx, owner.Type, owner.ID)
	if err != nil {
		return "", err
	}
	if len(reqs) == 0 {
		return "当前没有待审批项。", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "待审批（%d）：\n", len(reqs))
	for _, req := range reqs {
		fmt.Fprintf(&b, "- #%d %s\n", req.ID, req.Status)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func (o *Onboarding) statusReply(ctx context.Context, chatID int64, owner OwnerRef, arg string, pause bool) (string, error) {
	bot, err := o.resolveBot(ctx, chatID, owner, arg)
	if err != nil {
		return err.Error(), nil
	}
	status := StatusActive
	label := "已恢复"
	if pause {
		status = StatusPaused
		label = "已暂停"
	}
	if pause {
		if _, err := o.Service.SetStatus(ctx, bot.ID, status); err != nil {
			return "", err
		}
	} else if o.Risk != nil {
		if _, err := o.Risk.Resume(ctx, bot.ID); err != nil {
			if errors.Is(err, ErrReloginRequired) {
				return "账号仍需重新登录：请先完成扫码/授权登录，再发送 /resume。", nil
			}
			return "", err
		}
	} else if _, err := o.Service.SetStatus(ctx, bot.ID, status); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s「%s」。", label, bot.DisplayName), nil
}

func (o *Onboarding) budgetReply(ctx context.Context, chatID int64, owner OwnerRef, arg string) (string, error) {
	bot, err := o.resolveBot(ctx, chatID, owner, arg)
	if err != nil {
		return err.Error(), nil
	}
	return fmt.Sprintf("「%s」预算：%s（状态 %s）。", bot.DisplayName, budgetLabel(bot.BudgetJson), bot.Status), nil
}

func (o *Onboarding) resolveBot(ctx context.Context, chatID int64, owner OwnerRef, arg string) (store.OpsBot, error) {
	// A chat bound to an Ops Bot addresses that bot by default, so /bot, /pause,
	// /budget and friends need no name argument there.
	if strings.TrimSpace(arg) == "" && o.Service != nil && o.Service.Q != nil {
		if chat, err := o.Service.Q.GetIMBotChat(ctx, chatID); err == nil && chat.OpsBotID.Valid {
			if bot, err := o.Service.Get(ctx, chat.OpsBotID.Int64); err == nil &&
				bot.OwnerType == owner.Type && bot.OwnerID == owner.ID {
				return bot, nil
			}
		}
	}
	bots, err := o.Service.ListByOwner(ctx, owner)
	if err != nil {
		return store.OpsBot{}, err
	}
	arg = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(arg), "#"))
	if arg != "" {
		if id, perr := strconv.ParseInt(arg, 10, 64); perr == nil {
			for _, bot := range bots {
				if bot.ID == id {
					return bot, nil
				}
			}
		}
		needle := strings.ToLower(arg)
		for _, bot := range bots {
			if strings.Contains(strings.ToLower(bot.DisplayName), needle) {
				return bot, nil
			}
		}
		return store.OpsBot{}, fmt.Errorf("没有找到名称匹配「%s」的运营 Bot。可用 /bots 查看。", arg)
	}
	if len(bots) == 1 {
		return bots[0], nil
	}
	return store.OpsBot{}, errors.New("有多个运营 Bot，请指定名称或编号，例如 /budget #3。")
}

// --- helpers -------------------------------------------------------------

func (o *Onboarding) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o *Onboarding) sessionTTL() time.Duration { return defaultSessionTTL }

func (o *Onboarding) liveSessionLocked(chatID int64) *Session {
	s := o.sessions[chatID]
	if s == nil {
		return nil
	}
	if o.now().Sub(s.UpdatedAt) > o.sessionTTL() {
		delete(o.sessions, chatID)
		return nil
	}
	return s
}

func isCancel(text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "/cancel", "/取消", "取消接入", "退出接入":
		return true
	}
	return false
}

func splitSlash(text string) (string, string) {
	body := strings.TrimPrefix(strings.TrimSpace(text), "/")
	if i := strings.IndexAny(body, " \t\r\n"); i >= 0 {
		return strings.ToLower(body[:i]), strings.TrimSpace(body[i+1:])
	}
	return strings.ToLower(body), ""
}

// matchOnboardingIntent recognizes the conversational entry points from
// design §13.1-13.3. The channel part may be empty ("接入"), meaning the next
// step asks which channel.
func matchOnboardingIntent(text string) (string, bool) {
	t := strings.TrimSpace(text)
	lower := strings.ToLower(t)
	prefixes := []string{"再接入", "接入", "新增", "添加", "绑定", "连接", "connect "}
	var rest string
	matched := false
	for _, p := range prefixes {
		if strings.HasPrefix(lower, p) {
			rest = strings.TrimSpace(t[len(p):])
			matched = true
			break
		}
	}
	if !matched {
		return "", false
	}
	for _, p := range []string{"一个新的", "新的", "一个", "个"} {
		rest = strings.TrimSpace(strings.TrimPrefix(rest, p))
	}
	for _, p := range []string{"新渠道", "渠道"} {
		rest = strings.TrimSpace(strings.TrimPrefix(rest, p))
	}
	for _, p := range []string{"渠道", "账号", "Bot", "bot", "机器人"} {
		rest = strings.TrimSpace(strings.TrimSuffix(rest, p))
	}
	return strings.TrimSpace(rest), true
}

type channelAlias struct {
	packID  string
	display string
	names   []string
}

var channelAliases = []channelAlias{
	{packID: "wechat_mp", display: "微信公众号", names: []string{"公众号", "微信公众号", "微信", "wechat", "wechatmp", "wechat_mp", "mp"}},
	{packID: "xiaohongshu", display: "小红书", names: []string{"小红书", "xiaohongshu", "xhs", "red", "redbook"}},
	{packID: "facebook", display: "Facebook", names: []string{"facebook", "fb", "脸书"}},
	{packID: "zhihu", display: "知乎", names: []string{"知乎", "zhihu"}},
}

func normalizeChannel(name string) (packID, display string, known bool) {
	key := compactName(name)
	for _, alias := range channelAliases {
		for _, n := range alias.names {
			if key == compactName(n) {
				return alias.packID, alias.display, true
			}
		}
	}
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "channel", "新渠道", false
	}
	return strings.ToLower(trimmed), trimmed, false
}

func compactName(s string) string {
	return strings.NewReplacer(" ", "", "_", "", "-", "", "\t", "").Replace(strings.ToLower(strings.TrimSpace(s)))
}

func packHasVideo(m packs.Manifest) bool {
	for _, f := range m.ContentSchema.Formats {
		if f.VideoSec > 0 {
			return true
		}
	}
	return false
}

var (
	cadenceWeeklyRe = regexp.MustCompile(`每(?:周|星期|礼拜)\s*([0-9]+|[一二两三四五六七八九十])\s*篇`)
	cadenceDailyRe  = regexp.MustCompile(`每天\s*([0-9]+|[一二两三四五六七八九十])\s*篇`)
	budgetNumberRe  = regexp.MustCompile(`[0-9]+(?:\.[0-9]+)?`)
)

var cnNumbers = map[string]float64{
	"一": 1, "二": 2, "两": 2, "三": 3, "四": 4, "五": 5,
	"六": 6, "七": 7, "八": 8, "九": 9, "十": 10,
}

func parseNumber(s string) float64 {
	if v, ok := cnNumbers[s]; ok {
		return v
	}
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

func parseCadence(text string) CadenceConfig {
	cfg := CadenceConfig{PostsPerWeek: 1, Text: strings.TrimSpace(text)}
	if m := cadenceWeeklyRe.FindStringSubmatch(text); m != nil {
		cfg.PostsPerWeek = parseNumber(m[1])
	} else if m := cadenceDailyRe.FindStringSubmatch(text); m != nil {
		cfg.PostsPerWeek = parseNumber(m[1]) * 7
	}
	cfg.HotSpot = strings.Contains(text, "热点") || strings.Contains(text, "加更")
	return cfg
}

func parseBudget(text string) BudgetConfig {
	cfg := BudgetConfig{Currency: "CNY", Text: strings.TrimSpace(text)}
	trimmed := strings.TrimSpace(text)
	if strings.Contains(trimmed, "不限") || strings.Contains(trimmed, "不限额") ||
		strings.Contains(strings.ToLower(trimmed), "unlimited") {
		cfg.Unlimited = true
		return cfg
	}
	if m := budgetNumberRe.FindString(trimmed); m != "" {
		cfg.Limit, _ = strconv.ParseFloat(m, 64)
	}
	if strings.Contains(strings.ToLower(trimmed), "usd") || strings.Contains(trimmed, "$") ||
		strings.Contains(trimmed, "美元") {
		cfg.Currency = "USD"
	}
	return cfg
}

func parseApproval(text string) ApprovalConfig {
	cfg := ApprovalConfig{Publish: true, Reply: true, Video: true, Text: strings.TrimSpace(text)}
	t := strings.ToLower(strings.TrimSpace(text))
	for _, neg := range []string{"不需要", "不用", "不要", "无需", "否", "no", "none", "skip", "跳过", "关闭"} {
		if strings.Contains(t, neg) {
			return ApprovalConfig{Text: strings.TrimSpace(text)}
		}
	}
	if strings.Contains(t, "只") || strings.Contains(t, "仅") {
		cfg.Publish = !strings.Contains(t, "回复") && !strings.Contains(t, "视频")
		cfg.Reply = strings.Contains(t, "回复")
		cfg.Video = strings.Contains(t, "视频")
		if !cfg.Publish && !cfg.Reply && !cfg.Video {
			cfg.Publish = true
		}
	}
	return cfg
}

func parseVideoModel(text string) string {
	t := strings.ToLower(strings.TrimSpace(text))
	if t == "" || strings.Contains(t, "默认") || strings.Contains(t, "default") || t == "是" || t == "yes" {
		return "default"
	}
	return strings.TrimSpace(text)
}

func goalType(text string) string {
	t := strings.ToLower(text)
	switch {
	case strings.Contains(t, "转化") || strings.Contains(t, "成交") || strings.Contains(t, "convert"):
		return "conversion"
	case strings.Contains(t, "品牌") || strings.Contains(t, "brand"):
		return "brand"
	case strings.Contains(t, "涨粉") || strings.Contains(t, "粉丝") || strings.Contains(t, "follow"):
		return "follower_growth"
	default:
		return "other"
	}
}

func goalLabel(raw string) string {
	var v struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal([]byte(raw), &v) == nil {
		if v.Text != "" {
			return v.Text
		}
		if v.Type != "" {
			return v.Type
		}
	}
	return "未设置"
}

func cadenceLabel(raw string) string {
	var v struct {
		PostsPerWeek float64 `json:"posts_per_week"`
		HotSpot      bool    `json:"hot_spot_boost"`
	}
	if json.Unmarshal([]byte(raw), &v) == nil && v.PostsPerWeek > 0 {
		label := fmt.Sprintf("每周 %g 篇", v.PostsPerWeek)
		if v.HotSpot {
			label += "（热点加更）"
		}
		return label
	}
	return "未设置"
}

func budgetLabel(raw string) string {
	var v struct {
		Limit     float64 `json:"monthly_limit"`
		Currency  string  `json:"currency"`
		Unlimited bool    `json:"unlimited"`
	}
	if json.Unmarshal([]byte(raw), &v) != nil {
		return "未设置"
	}
	if v.Unlimited || v.Limit <= 0 {
		return "不限"
	}
	switch v.Currency {
	case "CNY", "":
		return fmt.Sprintf("每月 ¥%g", v.Limit)
	case "USD":
		return fmt.Sprintf("每月 $%g", v.Limit)
	default:
		return fmt.Sprintf("每月 %g %s", v.Limit, v.Currency)
	}
}

func approvalLabel(raw string) string {
	var v map[string]bool
	if json.Unmarshal([]byte(raw), &v) != nil || len(v) == 0 {
		return "未设置"
	}
	var on []string
	for _, item := range []struct {
		key, label string
	}{{"publish", "发布"}, {"reply", "回复"}, {"video", "视频"}} {
		if v[item.key] {
			on = append(on, item.label)
		}
	}
	if len(on) == 0 {
		return "无需确认（自动执行）"
	}
	return strings.Join(on, "/") + " 需确认"
}

func isAffirmative(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	if t == "" {
		return false
	}
	for _, neg := range []string{"不接入", "不需要", "不用", "不要", "无需", "否", "no", "skip", "跳过", "关闭"} {
		if t == neg || strings.HasPrefix(t, neg) {
			return false
		}
	}
	return true
}

func containsAny(text string, needles ...string) bool {
	lower := strings.ToLower(text)
	for _, n := range needles {
		if strings.Contains(lower, strings.ToLower(n)) {
			return true
		}
	}
	return false
}

func clipRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

func marshalJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
