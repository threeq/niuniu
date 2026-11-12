package opsbot

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// NotifyTarget identifies the IM chat a card or text should be delivered to.
// ChannelID + ChatExtID are enough for the service layer to decrypt the
// channel credential and call the platform adapter; BotID is carried for
// logging/audit only.
type NotifyTarget struct {
	ChannelID   int64
	ChannelType string
	ChatID      int64
	ChatExtID   string
	ThreadExtID string
	BotID       int64
}

// NotifyButton is one interactive action rendered on an outbound card.
type NotifyButton struct {
	Label string
	Value string
}

// NotifyPayload is the normalized outbound shape the Notifier hands to Send.
type NotifyPayload struct {
	Text    string
	Buttons []NotifyButton
}

// Notifier renders the ops-bot notification templates and hands them to Send.
// Send is wired by the service layer (it owns channel credentials); a nil Send
// makes every push a no-op, so the notifier is safe to leave unwired.
type Notifier struct {
	Send func(ctx context.Context, target NotifyTarget, payload NotifyPayload) error
}

// PublishApprovalCard is the preview shape of a publish approval card
// (design §10: 标题、正文摘要、封面/视频预览、渠道、时间、合规结论).
type PublishApprovalCard struct {
	Title      string `json:"title"`
	Summary    string `json:"summary"`
	Cover      string `json:"cover"`
	Channel    string `json:"channel"`
	PublishAt  string `json:"publish_at"`
	Compliance string `json:"compliance"`
}

// ReplyApprovalCard is the preview shape of an engagement-reply approval card
// (原评论/私信、分类、建议回复、风险提示).
type ReplyApprovalCard struct {
	Comment        string `json:"comment"`
	Category       string `json:"category"`
	SuggestedReply string `json:"suggested_reply"`
	Risk           string `json:"risk"`
}

// VideoApprovalCard is the preview shape of a finished-video approval card
// (成片预览、时长、字幕、封面、平台尺寸).
type VideoApprovalCard struct {
	Preview   string `json:"preview"`
	Duration  string `json:"duration"`
	Subtitles string `json:"subtitles"`
	Cover     string `json:"cover"`
	Size      string `json:"size"`
}

// RiskAlert is the risk-warning template payload (账号、原因、是否已暂停).
type RiskAlert struct {
	Account string
	Reason  string
	Paused  bool
	// Action, when set, renders one actionable button (used by the relogin
	// alert so the operator does not have to know a slash command).
	Action *NotifyButton
}

// ApprovalCard is one approval notification: the row id plus the typed preview.
type ApprovalCard struct {
	ID      int64
	Type    ApprovalType
	Publish *PublishApprovalCard
	Reply   *ReplyApprovalCard
	Video   *VideoApprovalCard
}

// CardFromApproval decodes an approval row's metadata into a renderable card.
func CardFromApproval(req ApprovalRequest) (ApprovalCard, error) {
	var meta approvalMetadata
	if err := json.Unmarshal([]byte(req.MetadataJSON), &meta); err != nil {
		return ApprovalCard{}, fmt.Errorf("opsbot: decode approval metadata: %w", err)
	}
	card := ApprovalCard{ID: req.ID, Type: meta.Type}
	switch meta.Type {
	case ApprovalPublish:
		var p PublishApprovalCard
		decodePreview(meta.Preview, &p)
		card.Publish = &p
	case ApprovalReply:
		var r ReplyApprovalCard
		decodePreview(meta.Preview, &r)
		card.Reply = &r
	case ApprovalVideo:
		var v VideoApprovalCard
		decodePreview(meta.Preview, &v)
		card.Video = &v
	default:
		return ApprovalCard{}, ErrInvalidApprovalType
	}
	return card, nil
}

// decodePreview tolerates a preview that does not match the template: the card
// still goes out (with empty fields) instead of silently disappearing, because
// a missing notification is worse than a sparse one.
func decodePreview(raw json.RawMessage, dst any) {
	if len(raw) == 0 {
		return
	}
	_ = json.Unmarshal(raw, dst)
}

// PushApproval renders and sends one approval card. A Send error is returned so
// the caller can log it; approval creation itself must already be committed.
func (n *Notifier) PushApproval(ctx context.Context, target NotifyTarget, card ApprovalCard) error {
	if n == nil || n.Send == nil {
		return nil
	}
	payload, err := RenderApprovalCard(card)
	if err != nil {
		return err
	}
	return n.Send(ctx, target, payload)
}

// PushText sends a plain text notification.
func (n *Notifier) PushText(ctx context.Context, target NotifyTarget, text string) error {
	if n == nil || n.Send == nil || strings.TrimSpace(text) == "" {
		return nil
	}
	return n.Send(ctx, target, NotifyPayload{Text: text})
}

// PushRisk sends the risk-warning template (账号/原因/已暂停).
func (n *Notifier) PushRisk(ctx context.Context, target NotifyTarget, alert RiskAlert) error {
	if n == nil || n.Send == nil {
		return nil
	}
	payload := NotifyPayload{Text: RenderRiskAlert(alert)}
	if alert.Action != nil && strings.TrimSpace(alert.Action.Label) != "" && strings.TrimSpace(alert.Action.Value) != "" {
		payload.Buttons = []NotifyButton{*alert.Action}
	}
	return n.Send(ctx, target, payload)
}

// RenderApprovalCard renders the card-specific template and its action buttons.
func RenderApprovalCard(card ApprovalCard) (NotifyPayload, error) {
	var b strings.Builder
	switch card.Type {
	case ApprovalPublish:
		b.WriteString(fmt.Sprintf("【发布审批 #%d】", card.ID))
		p := card.Publish
		if p == nil {
			p = &PublishApprovalCard{}
		}
		writeField(&b, "标题", p.Title)
		writeField(&b, "摘要", p.Summary)
		writeField(&b, "封面", p.Cover)
		writeField(&b, "渠道", p.Channel)
		writeField(&b, "计划时间", p.PublishAt)
		writeField(&b, "合规结论", p.Compliance)
	case ApprovalReply:
		b.WriteString(fmt.Sprintf("【回复审批 #%d】", card.ID))
		r := card.Reply
		if r == nil {
			r = &ReplyApprovalCard{}
		}
		writeField(&b, "原评论", r.Comment)
		writeField(&b, "分类", r.Category)
		writeField(&b, "建议回复", r.SuggestedReply)
		writeField(&b, "风险提示", r.Risk)
	case ApprovalVideo:
		b.WriteString(fmt.Sprintf("【视频成片审批 #%d】", card.ID))
		v := card.Video
		if v == nil {
			v = &VideoApprovalCard{}
		}
		writeField(&b, "预览", v.Preview)
		writeField(&b, "时长", v.Duration)
		writeField(&b, "字幕", v.Subtitles)
		writeField(&b, "封面", v.Cover)
		writeField(&b, "平台尺寸", v.Size)
	default:
		return NotifyPayload{}, ErrInvalidApprovalType
	}
	return NotifyPayload{Text: b.String(), Buttons: approvalButtons(card)}, nil
}

// RenderRiskAlert renders the risk-warning template.
func RenderRiskAlert(alert RiskAlert) string {
	var b strings.Builder
	b.WriteString("【风险告警】")
	writeField(&b, "账号", alert.Account)
	writeField(&b, "原因", alert.Reason)
	if alert.Paused {
		b.WriteString("\n状态：已自动暂停，请处理后用 /resume 恢复。")
	} else {
		b.WriteString("\n状态：运行中，请关注。")
	}
	if alert.Action != nil && strings.TrimSpace(alert.Action.Label) != "" {
		b.WriteString("\n处理：点击「")
		b.WriteString(alert.Action.Label)
		b.WriteString("」，或让 agent 调用 opsbot_browser_login。")
	}
	return b.String()
}

// ReloginCallbackValue builds the button payload for a relogin alert. It is a
// distinct prefix from approvals so the inbound dispatcher can route it.
func ReloginCallbackValue(botID int64) string {
	return "opslogin:" + strconv.FormatInt(botID, 10)
}

// ParseReloginCallback decodes "opslogin:<botID>".
func ParseReloginCallback(data string) (int64, bool) {
	parts := strings.Split(data, ":")
	if len(parts) != 2 || parts[0] != "opslogin" {
		return 0, false
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

func writeField(b *strings.Builder, label, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	b.WriteString("\n")
	b.WriteString(label)
	b.WriteString("：")
	b.WriteString(value)
}

func approvalButtons(card ApprovalCard) []NotifyButton {
	actions, ok := approvalOptions[card.Type]
	if !ok {
		return nil
	}
	out := make([]NotifyButton, 0, len(actions))
	for _, action := range actions {
		out = append(out, NotifyButton{
			Label: approvalActionLabel(card.Type, action),
			Value: ApprovalCallbackValue(action, card.ID),
		})
	}
	return out
}

func approvalActionLabel(cardType ApprovalType, action string) string {
	switch action {
	case ActionApproved:
		return "通过"
	case ActionRejected:
		return "驳回"
	case ActionModified:
		if cardType == ApprovalReply {
			return "修改后发送"
		}
		return "修改"
	case ActionRegenerate:
		return "重新生成"
	default:
		return action
	}
}

// Callback value tokens are short and colon-free: "opsapproval:<token>:<id>".
var approvalActionTokens = map[string]string{
	"approve":    ActionApproved,
	"reject":     ActionRejected,
	"modify":     ActionModified,
	"regenerate": ActionRegenerate,
}

var approvalTokenForAction = map[string]string{
	ActionApproved:   "approve",
	ActionRejected:   "reject",
	ActionModified:   "modify",
	ActionRegenerate: "regenerate",
}

// ApprovalCallbackValue builds the button payload for one decision action.
func ApprovalCallbackValue(action string, id int64) string {
	token := approvalTokenForAction[action]
	return "opsapproval:" + token + ":" + strconv.FormatInt(id, 10)
}

// ParseApprovalCallback decodes "opsapproval:<token>:<id>" into a Decision
// action. ok is false for any other callback (permission/ask-user) or a
// malformed payload.
func ParseApprovalCallback(data string) (action string, id int64, ok bool) {
	parts := strings.Split(data, ":")
	if len(parts) != 3 || parts[0] != "opsapproval" {
		return "", 0, false
	}
	action, ok = approvalActionTokens[parts[1]]
	if !ok {
		return "", 0, false
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || id <= 0 {
		return "", 0, false
	}
	return action, id, true
}

// ParseApprovalMeta exposes the bot/issue/type fields the IM layer needs for
// authorization and routing.
type ApprovalMeta struct {
	BotID          int64
	IssueID        int64
	Type           ApprovalType
	ContentVersion int64
}

func ParseApprovalMeta(req ApprovalRequest) (ApprovalMeta, error) {
	var meta approvalMetadata
	if err := json.Unmarshal([]byte(req.MetadataJSON), &meta); err != nil {
		return ApprovalMeta{}, fmt.Errorf("opsbot: decode approval metadata: %w", err)
	}
	return ApprovalMeta{
		BotID:          meta.BotID,
		IssueID:        meta.IssueID,
		Type:           meta.Type,
		ContentVersion: meta.ContentVersion,
	}, nil
}

// ApproverFor resolves the operator allowed to decide a bot's approval cards:
// approval_policy_json.operator_id when set, otherwise the chat's owner.
func ApproverFor(bot store.OpsBot, fallback int64) int64 {
	var cfg struct {
		OperatorID int64 `json:"operator_id"`
	}
	if strings.TrimSpace(bot.ApprovalPolicyJson) != "" {
		_ = json.Unmarshal([]byte(bot.ApprovalPolicyJson), &cfg)
	}
	if cfg.OperatorID > 0 {
		return cfg.OperatorID
	}
	return fallback
}
