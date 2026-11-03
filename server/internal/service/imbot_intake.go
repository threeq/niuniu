package service

// IM source adapter for the intake pipeline (Epic #755 / design §5).
//
// HandleInbound keeps everything that is genuinely IM-specific — channel
// lookup, pairing/nudge, permission + ask-user callbacks, slash commands,
// `#<id>` conversation control, the observation layer — and hands the remaining
// generic work (dedupe, authorize, resolve, decide, record, execute) to
// intake.Service. This file supplies that pipeline's IM collaborators:
//
//	imbotSource   InboundEvent -> intake.Trigger normalization
//	imbotStore    intake_events / intake_decisions persistence (+ inbox dual-write)
//	imbotPolicy   project+source policy (allow list, bot list, rules, version)
//	imbotResolver thread > pinned > active task resolution
//	imbotExecutor RouteInProject / bind / deliverToWorkspace
//
// Behaviour notes:
//   - An admitted chat message is Explicit: chat pairing is the admission
//     decision, so no allow-list entry is needed for the ordinary IM path
//     (design §6.2 "直接 @ 或私聊"). Explicit work executes in every mode, which
//     is what keeps existing chats (intake_mode off/auto) behaving identically.
//   - Every accepted or resumed message still creates/reuses exactly one
//     workspace and delivers exactly once; the decision is now also recorded in
//     intake_decisions.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/imbot"
	"github.com/niuniu-dev/niuniu/internal/intake"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// --- Source adapter ---------------------------------------------------------

// imbotSource normalizes an admitted IM chat message into an intake.Trigger.
//
// It deliberately does not implement intake.Source: that interface's Reply
// method cannot be honoured from a ThreadRef alone (it carries neither the IM
// channel nor the chat), and replies stay with the IM adapter, which owns the
// channel credential and each platform's message shape.
type imbotSource struct{}

func (imbotSource) Kind() string { return intake.SourceKindIMBot }

func (imbotSource) Normalize(
	ev imbot.InboundEvent,
	channel store.ImBotChannel,
	chat store.ImBotChat,
	text string,
	forceNew bool,
) intake.Trigger {
	// Dedupe on the platform event id; a message id is the stable fallback when
	// the platform does not report one (it is unique per delivered message).
	eventID := ev.EventID
	if eventID == "" {
		eventID = ev.MessageExtID
	}
	atts := make([]intake.Attachment, 0, len(ev.Attachments))
	for _, a := range ev.Attachments {
		atts = append(atts, intake.Attachment{Kind: a.Kind, Name: a.Name})
	}
	// Raw keeps the platform payload for the executor, which materializes
	// attachments through the adapter's resource fetcher — bytes never travel
	// through the pipeline's generic shape.
	raw, err := json.Marshal(ev)
	if err != nil {
		raw = nil
	}
	return intake.Trigger{
		ProjectID:         chat.ProjectID.Int64,
		SourceKind:        intake.SourceKindIMBot,
		SourceRef:         strconv.FormatInt(channel.ID, 10),
		EventID:           eventID,
		EventKind:         ev.Kind,
		Thread:            intake.ThreadRef{ThreadExtID: ev.ThreadExtID},
		Actor:             intake.ActorRef{ExtID: ev.ActorExtID, Name: ev.ActorName},
		Text:              text,
		Attachments:       atts,
		Raw:               raw,
		ReceivedAt:        time.Now(),
		ConversationExtID: ev.ChatExtID,
		// Chat pairing admitted the conversation, not the actor. This still
		// grants explicit authority while no allow list is configured (the
		// pre-policy behaviour), and once one exists Policy.Evaluate requires
		// the actor to be on it.
		Explicit: true,
		// An observe-mode chat reads every message; unlisted actors there are
		// inferred candidates rather than denied.
		ReceiveAll: normalizeAgentMode(chat.AgentMode) == AgentModeObserve,
		ForceNew:   forceNew,
	}
}

// --- Store ------------------------------------------------------------------

// imbotIntakeStore persists intake events and decisions through the shared
// sqlc queries.
type imbotIntakeStore struct{ q *store.Queries }

// RecordEvent is idempotent on (source_kind, source_ref, event_ext_id): the
// UNIQUE constraint plus INSERT OR IGNORE means a redelivered event inserts no
// row, which the pipeline reads as a duplicate.
//
// Phase 1 dual-write: the legacy im_bot_inbox row is written here too, so both
// tables carry the event. HandleInbound's early admission guard also writes it
// before this runs (an unknown or unpaired chat must not re-seed on redelivery),
// which makes this insert a no-op in the common path.
func (st imbotIntakeStore) RecordEvent(ctx context.Context, t intake.Trigger) (intake.EventRecord, error) {
	eventExtID := t.EventID
	if eventExtID == "" {
		// No platform id at all: never dedupe unrelated messages.
		eventExtID = fmt.Sprintf("%s#%d", t.EventKind, time.Now().UnixNano())
	}
	payload := "{}"
	if len(t.Raw) > 0 {
		payload = string(t.Raw)
	}
	params := store.CreateIntakeEventParams{
		ProjectID:   t.ProjectID,
		SourceKind:  t.SourceKind,
		SourceRef:   t.SourceRef,
		EventExtID:  eventExtID,
		EventKind:   t.EventKind,
		ActorExtID:  t.Actor.ExtID,
		ActorName:   t.Actor.Name,
		ThreadExtID: t.Thread.ThreadExtID,
		PayloadJson: payload,
	}
	row, err := st.q.CreateIntakeEvent(ctx, params)
	if errors.Is(err, sql.ErrNoRows) {
		existing, gerr := st.q.GetIntakeEventByKey(ctx, store.GetIntakeEventByKeyParams{
			SourceKind: t.SourceKind, SourceRef: t.SourceRef, EventExtID: eventExtID,
		})
		if gerr != nil {
			return intake.EventRecord{}, gerr
		}
		return intake.EventRecord{ID: existing.ID, Duplicate: true}, nil
	}
	if err != nil {
		return intake.EventRecord{}, err
	}
	st.recordLegacyInbox(ctx, t)
	return intake.EventRecord{ID: row.ID}, nil
}

// recordLegacyInbox mirrors a newly recorded intake event into im_bot_inbox.
// Only events with a real platform id are mirrored: the legacy table is keyed on
// that id, and a synthesized one would be meaningless there. Best-effort — the
// intake event is already stored, so losing the mirror only affects the Phase-2
// cutover, not this delivery.
func (st imbotIntakeStore) recordLegacyInbox(ctx context.Context, t intake.Trigger) {
	if t.EventID == "" {
		return
	}
	channelID, err := strconv.ParseInt(t.SourceRef, 10, 64)
	if err != nil || channelID == 0 {
		return
	}
	_ = st.q.CreateIMBotInboxEvent(ctx, store.CreateIMBotInboxEventParams{
		ChannelID: channelID, EventExtID: t.EventID,
	})
}

func (st imbotIntakeStore) RecordDecision(ctx context.Context, rec intake.DecisionRecord) (int64, error) {
	matched := "[]"
	if len(rec.MatchedRuleIDs) > 0 {
		b, err := json.Marshal(rec.MatchedRuleIDs)
		if err != nil {
			return 0, err
		}
		matched = string(b)
	}
	wouldDo := "{}"
	if rec.WouldDo != nil {
		b, err := json.Marshal(rec.WouldDo)
		if err != nil {
			return 0, err
		}
		wouldDo = string(b)
	}
	row, err := st.q.CreateIntakeDecision(ctx, store.CreateIntakeDecisionParams{
		EventID:            rec.EventID,
		ProjectID:          rec.ProjectID,
		ModeAtDecision:     string(rec.Mode),
		Strength:           string(rec.Strength),
		Decision:           string(rec.Decision),
		Executed:           0,
		ReasonCode:         rec.ReasonCode,
		ReasonText:         rec.ReasonText,
		MatchedRuleIdsJson: matched,
		PolicyVersion:      rec.PolicyVersion,
		IssueID:            validID(rec.Thread.IssueID),
		WorkspaceID:        validID(rec.Thread.WorkspaceID),
		WouldDoJson:        wouldDo,
	})
	if err != nil {
		return 0, err
	}
	return row.ID, nil
}

func (st imbotIntakeStore) MarkExecuted(ctx context.Context, decisionID int64, res intake.ExecutionResult) error {
	_, err := st.q.SetIntakeDecisionExecuted(ctx, store.SetIntakeDecisionExecutedParams{
		IssueID:     validID(res.IssueID),
		WorkspaceID: validID(res.WorkspaceID),
		ID:          decisionID,
	})
	return err
}

func (st imbotIntakeStore) ReserveDailySlot(ctx context.Context, projectID int64, sourceKind, day string, cap int64) (bool, error) {
	return st.q.ReserveIntakeDailySlot(ctx, store.ReserveIntakeDailySlotParams{
		ProjectID: projectID, SourceKind: sourceKind, Day: day, Cap: cap,
	})
}

func (st imbotIntakeStore) ReleaseDailySlot(ctx context.Context, projectID int64, sourceKind, day string) error {
	return st.q.ReleaseIntakeDailySlot(ctx, projectID, sourceKind, day)
}

func validID(id int64) sql.NullInt64 {
	return sql.NullInt64{Int64: id, Valid: id > 0}
}

// --- Policy -----------------------------------------------------------------

// imbotPolicy loads the effective policy for one project+source. A project with
// no stored settings yields ModeOff with empty lists — never a permissive
// default (the pipeline's contract).
type imbotPolicy struct{ q *store.Queries }

func (p imbotPolicy) LoadPolicy(ctx context.Context, projectID int64, sourceKind string) (intake.Policy, error) {
	pol := intake.Policy{ProjectID: projectID, SourceKind: sourceKind, Mode: intake.ModeOff}

	row, err := p.q.GetIntakeSetting(ctx, store.GetIntakeSettingParams{
		ProjectID: projectID, SourceKind: sourceKind,
	})
	switch {
	case err == nil:
		pol.Mode = intakeMode(row.Mode)
		pol.DailyCap = row.DailyCap
		pol.AllowList = decodeStringList(row.AllowListJson)
		pol.BotList = decodeStringList(row.BotListJson)
	case errors.Is(err, sql.ErrNoRows):
		// No policy configured: explicit work only.
	default:
		return intake.Policy{}, err
	}

	rules, err := p.q.ListEnabledIntakeRules(ctx, store.ListEnabledIntakeRulesParams{
		ProjectID: projectID, SourceKind: sourceKind,
	})
	if err != nil {
		return intake.Policy{}, err
	}
	pol.Rules = make([]intake.Rule, 0, len(rules))
	for _, r := range rules {
		pol.Rules = append(pol.Rules, intake.Rule{
			ID:          r.ID,
			Name:        r.Name,
			Instruction: r.Instruction,
			Priority:    r.Priority,
			Enabled:     r.Enabled != 0,
			Conditions:  decodeRuleConditions(r.ConditionsJson),
		})
	}

	version, err := p.q.GetLatestIntakePolicyVersion(ctx, store.GetLatestIntakePolicyVersionParams{
		ProjectID: projectID, SourceKind: sourceKind,
	})
	switch {
	case err == nil:
		pol.Version = version.Version
	case errors.Is(err, sql.ErrNoRows):
		// No snapshot yet: version 0 means "no policy". The row is only written
		// when settings change, so an unconfigured project never has one.
	default:
		return intake.Policy{}, err
	}
	return pol, nil
}

// intakeMode maps a stored mode string onto the pipeline's enum. Unknown values
// degrade to off — the conservative choice that still lets explicit work run.
func intakeMode(mode string) intake.Mode {
	switch intake.Mode(strings.TrimSpace(mode)) {
	case intake.ModeObserve:
		return intake.ModeObserve
	case intake.ModeAuto:
		return intake.ModeAuto
	default:
		return intake.ModeOff
	}
}

// decodeRuleConditions parses a stored conditions JSON blob. A malformed rule
// keeps no conditions, which means it never matches (an empty rule is not a
// catch-all) — the safe direction.
func decodeRuleConditions(raw string) intake.RuleConditions {
	var cond intake.RuleConditions
	if strings.TrimSpace(raw) == "" {
		return cond
	}
	_ = json.Unmarshal([]byte(raw), &cond)
	return cond
}

// --- Resolver ---------------------------------------------------------------

// imbotResolver resolves which task a trigger belongs to, preserving the IM
// priority order: pinned workspace chat > bound thread > active pointer.
type imbotResolver struct{ svc *IMBotService }

func (r imbotResolver) Resolve(ctx context.Context, t intake.Trigger) (intake.Resolution, error) {
	chat, err := r.svc.intakeChat(ctx, t)
	if err != nil {
		return intake.Resolution{}, err
	}
	issueID, wsID := r.svc.resolveTask(ctx, chat, t.Thread.ThreadExtID)
	thread := t.Thread
	thread.IssueID, thread.WorkspaceID = issueID, wsID
	return intake.Resolution{
		Thread:      thread,
		IssueID:     issueID,
		WorkspaceID: wsID,
		Bound:       wsID != 0,
	}, nil
}

// intakeChat reloads the chat a trigger came from. It is the pipeline's
// (channel, chat) key — the same one HandleInbound used for admission.
func (s *IMBotService) intakeChat(ctx context.Context, t intake.Trigger) (store.ImBotChat, error) {
	channelID, err := strconv.ParseInt(t.SourceRef, 10, 64)
	if err != nil {
		return store.ImBotChat{}, fmt.Errorf("imbot intake: bad channel ref %q: %w", t.SourceRef, err)
	}
	return s.q.GetIMBotChatByExt(ctx, store.GetIMBotChatByExtParams{
		ChannelID: channelID, ChatExtID: t.ConversationExtID,
	})
}

// --- Executor ---------------------------------------------------------------

// imbotExecutor performs the IM side of an accepted (new) or resumed decision:
// route into the project, bind the conversation, announce the new task and
// deliver the message into the workspace's agent session.
type imbotExecutor struct{ svc *IMBotService }

// errRouteIncomplete reports a router that returned a zero-valued target
// without an error: continuing would bind the chat to issue 0, reply with a
// "#0" marker the user cannot reach, and deliver into workspace 0.
var errRouteIncomplete = errors.New("imbot intake: router returned an incomplete target")

func (e imbotExecutor) Execute(ctx context.Context, plan intake.ExecutionPlan) (intake.ExecutionResult, error) {
	s := e.svc
	channel, err := s.q.GetIMBotChannel(ctx, e.channelID(plan.Trigger))
	if err != nil {
		return intake.ExecutionResult{}, err
	}
	ev := decodeInboundEvent(plan.Trigger.Raw)

	if plan.Decision == intake.DecisionResume {
		if plan.WorkspaceID == 0 {
			return intake.ExecutionResult{}, errors.New("imbot intake: resume without a workspace")
		}
		s.deliverToWorkspace(ctx, channel, ev, plan.WorkspaceID, plan.Trigger.Text)
		return intake.ExecutionResult{IssueID: plan.IssueID, WorkspaceID: plan.WorkspaceID}, nil
	}

	chat, err := s.intakeChat(ctx, plan.Trigger)
	if err != nil {
		return intake.ExecutionResult{}, err
	}
	out, err := s.startInboundTask(ctx, channel, chat, ev, plan.Trigger)
	if err != nil {
		return intake.ExecutionResult{}, err
	}
	s.recordIssueProvenance(ctx, out.IssueID, plan.Trigger)
	return out, nil
}

// recordIssueProvenance attaches the intake origin to a newly created task so
// the Issue shows where it came from (§6.2). Best-effort: the task already
// exists, and a provenance miss must not fail the delivery. Issues that already
// carry an external source are left alone (a routed existing task may have been
// imported from GitHub).
func (s *IMBotService) recordIssueProvenance(ctx context.Context, issueID int64, t intake.Trigger) {
	if issueID == 0 {
		return
	}
	externalID := strings.TrimSpace(t.Thread.ThreadExtID)
	if externalID == "" {
		externalID = strings.TrimSpace(t.EventID)
	}
	if externalID == "" {
		return
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE issues SET external_source = ?, external_id = ?, external_url = ''
		 WHERE id = ? AND (external_source IS NULL OR external_source = '')`,
		t.SourceKind, externalID, issueID,
	); err != nil {
		slog.Warn("imbot: write issue provenance failed", "issue", issueID, "error", err)
	}
}

// startInboundTask is the accept path: a new conversation in the chat's bound
// project.
func (s *IMBotService) startInboundTask(
	ctx context.Context,
	channel store.ImBotChannel,
	chat store.ImBotChat,
	ev imbot.InboundEvent,
	t intake.Trigger,
) (intake.ExecutionResult, error) {
	if s.dispatch == nil {
		return intake.ExecutionResult{}, errors.New("imbot intake: no task router wired")
	}
	owner, ok := s.projectOwner(ctx, t.ProjectID)
	if !ok {
		return intake.ExecutionResult{}, errors.New("imbot intake: project owner unresolved")
	}
	hint := RouteHint{ForceNew: t.ForceNew}
	// A no-thread chat with an active pointer continues that task unless the
	// user forced a new one; the resolver already decided continue-vs-new, so
	// ActiveIssueID is only a classifier tie-break hint.
	if t.Thread.ThreadExtID == "" && chat.ActiveIssueID.Valid && !t.ForceNew {
		hint.ActiveIssueID = chat.ActiveIssueID.Int64
	}

	target, err := s.dispatch.RouteInProject(ctx, owner, t.ProjectID, t.Text, hint)
	if err != nil {
		s.pushText(ctx, channel, ev.ChatExtID, ev.ThreadExtID, "牛牛暂时无法开始这个任务，请稍后再试。")
		return intake.ExecutionResult{}, fmt.Errorf("imbot intake: route: %w", err)
	}
	if target.IssueID == 0 || target.WorkspaceID == 0 {
		// A router can return a zero-valued target WITHOUT an error (nothing
		// resolved). Continuing would bind the chat to issue 0, reply with a "#0"
		// marker the user cannot reach, and then deliver into workspace 0 — i.e.
		// silently drop the message while claiming work started.
		s.pushText(ctx, channel, ev.ChatExtID, ev.ThreadExtID, "牛牛暂时无法开始这个任务，请稍后再试。")
		return intake.ExecutionResult{}, fmt.Errorf("%w: issue=%d workspace=%d",
			errRouteIncomplete, target.IssueID, target.WorkspaceID)
	}

	// Bind the new task so follow-ups land in the same place.
	if ev.ThreadExtID != "" {
		if _, cerr := s.q.CreateIMBotThread(ctx, store.CreateIMBotThreadParams{
			ChatID: chat.ID, ThreadExtID: ev.ThreadExtID, IssueID: target.IssueID, WorkspaceID: target.WorkspaceID,
		}); cerr != nil {
			slog.Warn("imbot: bind thread failed", "chat", chat.ID, "thread", ev.ThreadExtID, "error", cerr)
		}
	} else {
		s.setActiveIssue(ctx, chat, target.IssueID)
	}

	// A NEW conversation was just created — reply once with its `#<id> <标题>`
	// so the user knows the task id (and can `#<id>` back to it later). Only
	// here, never on follow-ups, to avoid flooding the chat with replies.
	s.replyTaskMarker(ctx, channel, ev, target.IssueID)

	s.deliverToWorkspace(ctx, channel, ev, target.WorkspaceID, t.Text)
	return intake.ExecutionResult{IssueID: target.IssueID, WorkspaceID: target.WorkspaceID}, nil
}

// channelID reads the IM channel a trigger belongs to (SourceRef).
func (imbotExecutor) channelID(t intake.Trigger) int64 {
	id, _ := strconv.ParseInt(t.SourceRef, 10, 64)
	return id
}

// decodeInboundEvent recovers the platform payload captured during
// normalization. An unusable payload yields a zero event: routing/delivery then
// still work, only the attachment/reaction/reply extras are skipped.
func decodeInboundEvent(raw json.RawMessage) imbot.InboundEvent {
	var ev imbot.InboundEvent
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &ev)
	}
	return ev
}
