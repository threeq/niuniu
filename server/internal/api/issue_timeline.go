package api

import (
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/service"
)

type IssueTimelineHandler struct {
	commentSvc  *service.IssueCommentService
	activitySvc *service.IssueActivityService
	execSvc     *service.ExecEventService
	Authz       *service.Authz
}

func NewIssueTimelineHandler(commentSvc *service.IssueCommentService, activitySvc *service.IssueActivityService, execSvc *service.ExecEventService) *IssueTimelineHandler {
	return &IssueTimelineHandler{commentSvc: commentSvc, activitySvc: activitySvc, execSvc: execSvc}
}

// ExecTimelineEntry is one row of the per-issue execution timeline (spec §23.7).
type ExecTimelineEntry struct {
	ID         int64     `json:"id"`
	Kind       string    `json:"kind"`
	Summary    string    `json:"summary"`
	DetailJSON string    `json:"detail_json,omitempty"`
	CostUSD    float64   `json:"cost_usd"`
	CreatedAt  time.Time `json:"created_at"`
}

// ExecTimelineResponse is the wire format for GET /api/issues/:id/exec-timeline.
type ExecTimelineResponse struct {
	Entries   []ExecTimelineEntry `json:"entries"`
	TotalCost float64             `json:"total_cost"`
}

// GetExecTimeline returns the issue's first-class execution timeline + total cost
// (spec §23.7): advance moves, gate results, ask_user round-trips, terminal
// transitions, interventions, cost.
func (h *IssueTimelineHandler) GetExecTimeline(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	issueID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid issue ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, aerr := h.Authz.CanAccessIssue(c.Request.Context(), userID, issueID); aerr != nil {
			writeAuthzError(c, aerr)
			return
		}
	}
	events, err := h.execSvc.List(c.Request.Context(), issueID)
	if err != nil {
		InternalError(c, err)
		return
	}
	total, _ := h.execSvc.TotalCost(c.Request.Context(), issueID)
	resp := ExecTimelineResponse{TotalCost: total, Entries: make([]ExecTimelineEntry, 0, len(events))}
	for _, e := range events {
		resp.Entries = append(resp.Entries, ExecTimelineEntry{
			ID:         e.ID,
			Kind:       e.Kind,
			Summary:    e.Summary,
			DetailJSON: nullStringValResp(e.DetailJson),
			CostUSD:    e.CostUsd,
			CreatedAt:  e.CreatedAt,
		})
	}
	c.JSON(http.StatusOK, resp)
}

type TimelineEntry struct {
	Type      string    `json:"type"`
	ID        int64     `json:"id"`
	Action    string    `json:"action,omitempty"`
	Field     string    `json:"field,omitempty"`
	OldValue  string    `json:"old_value,omitempty"`
	NewValue  string    `json:"new_value,omitempty"`
	Author    string    `json:"author"`
	Content   string    `json:"content,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

func (h *IssueTimelineHandler) GetTimeline(c *gin.Context) {
	userID := c.GetInt64("auth_user_id")
	issueID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		BadRequest(c, "invalid issue ID")
		return
	}
	if userID > 0 && h.Authz != nil {
		if _, aerr := h.Authz.CanAccessIssue(c.Request.Context(), userID, issueID); aerr != nil {
			writeAuthzError(c, aerr)
			return
		}
	}

	comments, err := h.commentSvc.ListByIssue(c.Request.Context(), issueID)
	if err != nil {
		InternalError(c, err)
		return
	}

	activities, err := h.activitySvc.ListByIssue(c.Request.Context(), issueID)
	if err != nil {
		InternalError(c, err)
		return
	}

	entries := make([]TimelineEntry, 0, len(comments)+len(activities))

	for _, cm := range comments {
		entries = append(entries, TimelineEntry{
			Type:      "comment",
			ID:        cm.ID,
			Author:    cm.Author,
			Content:   cm.Content,
			CreatedAt: cm.CreatedAt,
			UpdatedAt: cm.UpdatedAt,
		})
	}

	for _, act := range activities {
		entries = append(entries, TimelineEntry{
			Type:      "activity",
			ID:        act.ID,
			Action:    act.Action,
			Field:     nullStringValResp(act.Field),
			OldValue:  nullStringValResp(act.OldValue),
			NewValue:  nullStringValResp(act.NewValue),
			Author:    nullStringValResp(act.Author),
			CreatedAt: act.CreatedAt,
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].CreatedAt.Before(entries[j].CreatedAt)
	})

	c.JSON(http.StatusOK, entries)
}
