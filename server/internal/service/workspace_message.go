package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// Cross-workspace agent messaging: one workspace's agent addresses another
// workspace's agent directly. Scoped to the SAME PROJECT by default — a
// workspace can only reach peers that belong to the same project, which keeps
// the feature inside one unit of work (and one tenant) without extra
// per-workspace ACLs.
//
// The transport is the EXISTING delivery path (AgentProxy.Deliver): an inbound
// message queues behind a busy target, starts an idle one, is persisted as a
// chat message and shows up over SSE. There is deliberately no second message
// pipeline — everything the SPA already does (queueing, autohost, history)
// applies unchanged.
//
// Epic integration: an orchestration workspace addresses a child by
// TargetIssueID, and a child can reach its parent epic the same way; see the
// prompt hints added in epic_execution.go.

// WorkspaceMessageDeliverer is the delivery side; *agentproxy.AgentProxy (via
// the service shim) satisfies it.
type WorkspaceMessageDeliverer interface {
	Deliver(ctx context.Context, workspaceID int64, workDir, content, attachments string) (queued bool, queueID int64, err error)
}

// WorkspaceMessageTarget is one addressable peer workspace.
type WorkspaceMessageTarget struct {
	WorkspaceID   int64  `json:"workspace_id"`
	Name          string `json:"name"`
	Status        string `json:"status"`
	AgentStatus   string `json:"agent_status"`
	IssueID       int64  `json:"issue_id"`
	IssueTitle    string `json:"issue_title"`
	IssueType     string `json:"issue_type"`
	ParentIssueID int64  `json:"parent_issue_id"`
}

// WorkspaceMessageRequest addresses a peer either directly (workspace id) or
// through its issue — the handle an Epic orchestrator naturally has.
type WorkspaceMessageRequest struct {
	TargetWorkspaceID int64
	TargetIssueID     int64
	Text              string
	ReplyTo           string
}

// WorkspaceMessageResult is what the sender gets back.
type WorkspaceMessageResult struct {
	MessageID   string `json:"message_id"`
	WorkspaceID int64  `json:"workspace_id"`
	Queued      bool   `json:"queued"`
}

const (
	workspaceMessageMaxChars   = 8000
	workspaceMessageRateLimit  = 10
	workspaceMessageRateWindow = time.Minute
)

// WorkspaceMessageService resolves targets, enforces the project scope and
// rate limit, and delivers through the agent proxy.
type WorkspaceMessageService struct {
	q         *store.Queries
	deliverer WorkspaceMessageDeliverer

	mu   sync.Mutex
	hits map[string][]time.Time
	now  func() time.Time
}

func NewWorkspaceMessageService(q *store.Queries, d WorkspaceMessageDeliverer) *WorkspaceMessageService {
	return &WorkspaceMessageService{q: q, deliverer: d, hits: map[string][]time.Time{}, now: time.Now}
}

// List returns the sender's peers: live workspaces of the same project.
func (s *WorkspaceMessageService) List(ctx context.Context, senderWorkspaceID int64) ([]WorkspaceMessageTarget, error) {
	projectID, err := s.q.GetProjectIDByWorkspaceID(ctx, senderWorkspaceID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("this workspace is not bound to a project — cross-workspace messaging requires a project workspace")
		}
		return nil, fmt.Errorf("resolve sender project: %w", err)
	}
	rows, err := s.q.ListProjectWorkspacesForMessaging(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]WorkspaceMessageTarget, 0, len(rows))
	for _, r := range rows {
		if r.WorkspaceID == senderWorkspaceID {
			continue
		}
		status := ""
		if r.AgentStatus.Valid {
			status = r.AgentStatus.String
		}
		out = append(out, WorkspaceMessageTarget{
			WorkspaceID:   r.WorkspaceID,
			Name:          r.WorkspaceName,
			Status:        r.Status,
			AgentStatus:   status,
			IssueID:       r.IssueID,
			IssueTitle:    r.IssueTitle,
			IssueType:     r.IssueType,
			ParentIssueID: r.ParentIssueID,
		})
	}
	return out, nil
}

// Send delivers one message to a peer workspace's agent.
func (s *WorkspaceMessageService) Send(ctx context.Context, senderWorkspaceID int64, req WorkspaceMessageRequest) (WorkspaceMessageResult, error) {
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return WorkspaceMessageResult{}, errors.New("message text is required")
	}
	if len([]rune(text)) > workspaceMessageMaxChars {
		return WorkspaceMessageResult{}, fmt.Errorf("message too long (max %d characters)", workspaceMessageMaxChars)
	}
	if s.deliverer == nil {
		return WorkspaceMessageResult{}, errors.New("workspace messaging is not wired on this server")
	}

	target, err := s.resolveTarget(ctx, req)
	if err != nil {
		return WorkspaceMessageResult{}, err
	}
	if target.WorkspaceID == senderWorkspaceID {
		return WorkspaceMessageResult{}, errors.New("cannot send a message to the workspace you are running in")
	}

	sender, err := s.q.GetWorkspace(ctx, senderWorkspaceID)
	if err != nil {
		return WorkspaceMessageResult{}, fmt.Errorf("load sender workspace: %w", err)
	}
	targetWs, err := s.q.GetWorkspace(ctx, target.WorkspaceID)
	if err != nil {
		return WorkspaceMessageResult{}, fmt.Errorf("load target workspace: %w", err)
	}

	senderProject, err := s.q.GetProjectIDByWorkspaceID(ctx, senderWorkspaceID)
	if err != nil {
		return WorkspaceMessageResult{}, fmt.Errorf("resolve sender project: %w", err)
	}
	targetProject, err := s.q.GetProjectIDByWorkspaceID(ctx, target.WorkspaceID)
	if err != nil {
		return WorkspaceMessageResult{}, fmt.Errorf("resolve target project: %w", err)
	}
	if senderProject != targetProject {
		return WorkspaceMessageResult{}, errors.New("cross-project messaging is not allowed: the target must belong to the same project")
	}

	if err := s.checkRate(senderWorkspaceID, target.WorkspaceID); err != nil {
		return WorkspaceMessageResult{}, err
	}

	messageID := uuid.NewString()
	body := composeInboundMessage(messageID, sender, target, text, req.ReplyTo)
	queued, _, err := s.deliverer.Deliver(ctx, target.WorkspaceID, targetWs.Path, body, "")
	if err != nil {
		return WorkspaceMessageResult{}, fmt.Errorf("deliver to workspace %d: %w", target.WorkspaceID, err)
	}
	return WorkspaceMessageResult{MessageID: messageID, WorkspaceID: target.WorkspaceID, Queued: queued}, nil
}

// resolveTarget turns the request's handle into a live peer workspace.
func (s *WorkspaceMessageService) resolveTarget(ctx context.Context, req WorkspaceMessageRequest) (WorkspaceMessageTarget, error) {
	switch {
	case req.TargetWorkspaceID > 0:
		ws, err := s.q.GetWorkspace(ctx, req.TargetWorkspaceID)
		if err != nil {
			return WorkspaceMessageTarget{}, fmt.Errorf("target workspace %d not found: %w", req.TargetWorkspaceID, err)
		}
		return targetFromWorkspace(ctx, s.q, ws)
	case req.TargetIssueID > 0:
		wss, err := s.q.GetWorkspacesByIssue(ctx, sql.NullInt64{Int64: req.TargetIssueID, Valid: true})
		if err != nil {
			return WorkspaceMessageTarget{}, fmt.Errorf("load workspaces of issue %d: %w", req.TargetIssueID, err)
		}
		for _, ws := range wss {
			if ws.IsArchived != 0 || ws.Status == "completed" {
				continue
			}
			return targetFromWorkspace(ctx, s.q, ws)
		}
		return WorkspaceMessageTarget{}, fmt.Errorf("issue %d has no live workspace yet — create one first (start_workspace)", req.TargetIssueID)
	default:
		return WorkspaceMessageTarget{}, errors.New("target_workspace_id or target_issue_id is required")
	}
}

// targetFromWorkspace validates the peer is live and builds its descriptor.
func targetFromWorkspace(ctx context.Context, q *store.Queries, ws store.Workspace) (WorkspaceMessageTarget, error) {
	if ws.IsArchived != 0 {
		return WorkspaceMessageTarget{}, errors.New("target workspace is archived")
	}
	if ws.Status == "completed" {
		return WorkspaceMessageTarget{}, errors.New("target workspace is already completed")
	}
	out := WorkspaceMessageTarget{
		WorkspaceID: ws.ID,
		Name:        ws.Name,
		Status:      ws.Status,
	}
	if ws.AgentStatus.Valid {
		out.AgentStatus = ws.AgentStatus.String
	}
	if ws.IssueID.Valid {
		out.IssueID = ws.IssueID.Int64
		if issue, err := q.GetIssue(ctx, ws.IssueID.Int64); err == nil {
			out.IssueTitle = issue.Title
			out.IssueType = issue.IssueType
			if issue.ParentIssueID.Valid {
				out.ParentIssueID = issue.ParentIssueID.Int64
			}
		}
	}
	return out, nil
}

// checkRate caps sender→target traffic so two agents cannot ping-pong forever.
func (s *WorkspaceMessageService) checkRate(senderID, targetID int64) error {
	key := fmt.Sprintf("%d>%d", senderID, targetID)
	now := s.now()
	cutoff := now.Add(-workspaceMessageRateWindow)

	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.hits[key][:0]
	for _, t := range s.hits[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= workspaceMessageRateLimit {
		s.hits[key] = kept
		return fmt.Errorf("rate limit: at most %d messages per minute to the same workspace", workspaceMessageRateLimit)
	}
	s.hits[key] = append(kept, now)
	return nil
}

// composeInboundMessage renders the message the TARGET agent receives. The
// provenance line is load-bearing: the receiving agent must not mistake peer
// traffic for a user instruction.
func composeInboundMessage(messageID string, sender store.Workspace, target WorkspaceMessageTarget, text, replyTo string) string {
	var b strings.Builder
	b.WriteString("【跨工作空间消息】message_id=")
	b.WriteString(messageID)
	b.WriteString("\n来自：ws#")
	b.WriteString(fmt.Sprintf("%d", sender.ID))
	b.WriteString("「")
	b.WriteString(sender.Name)
	b.WriteString("」")
	if target.IssueID > 0 {
		b.WriteString(fmt.Sprintf("（你的 issue #%d「%s」）", target.IssueID, target.IssueTitle))
	}
	b.WriteString("\n")
	if replyTo != "" {
		b.WriteString("对方消息 ID（回复时作为 reply_to 传回）：")
		b.WriteString(replyTo)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(text)
	b.WriteString("\n\n")
	b.WriteString("（这是同一项目内另一个工作空间 agent 发来的消息，不是用户本人发送的。无需回复可忽略；需要回复时调用 send_workspace_message 并带上 reply_to。）")
	return b.String()
}
