package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// WorkspaceOverviewItem is one row in the cross-workspace dashboard. Built
// from workspaces table + workspace_costs aggregations + agent_messages
// aggregations + a "stuck" heuristic.
type WorkspaceOverviewItem struct {
	WorkspaceID    int64      `json:"workspace_id"`
	Name           string     `json:"name"`
	OwnerType      string     `json:"owner_type"`
	OwnerID        int64      `json:"owner_id"`
	Status         string     `json:"status"`
	SessionStatus  string     `json:"session_status"`
	UpdatedAt      time.Time  `json:"updated_at"`
	LastActivityAt *time.Time `json:"last_activity_at,omitempty"`
	MessageCount   int64      `json:"message_count"`
	// Token usage by type (lifetime, from workspace_stats). Cost in $ is no
	// longer surfaced (we record tokens, not money).
	UserMessageCount    int64 `json:"user_message_count"`
	AiMessageCount      int64 `json:"ai_message_count"`
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
	IsStuck             bool  `json:"is_stuck"` // running but no activity for >2h
	IsArchived     bool       `json:"is_archived"`
	CreatedBy      *int64     `json:"created_by,omitempty"` // creator user ID, nil if NULL
	ProjectName    string     `json:"project_name,omitempty"`
	ChangesCount   int        `json:"changes_count"` // aggregate uncommitted changes across worktrees
	AheadCount     int        `json:"ahead_count"`   // aggregate commits ahead of base across worktrees
}

// WorkspaceOverviewSummary is the top-card aggregate.
type WorkspaceOverviewSummary struct {
	TotalCount  int `json:"total_count"`
	ActiveCount int `json:"active_count"` // status running/needs_review/attention
	StuckCount  int `json:"stuck_count"`
	// Aggregate token usage + message counts across the (filtered) set.
	UserMessageCount    int64 `json:"user_message_count"`
	AiMessageCount      int64 `json:"ai_message_count"`
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
}

// WorkspaceOverview is the response payload.
type WorkspaceOverview struct {
	Summary    WorkspaceOverviewSummary `json:"summary"`
	Workspaces []WorkspaceOverviewItem  `json:"workspaces"`
}

const stuckThreshold = 2 * time.Hour

// BuildOverview assembles the cross-workspace overview from a pre-filtered
// sidebar-meta slice. The metric summary aggregates over exactly this slice --
// callers that pass a creator-filtered list will see metrics scoped to that
// creator only. Authz / owner / creator narrowing is the caller's job; this
// function trusts the input. ProjectName/ChangesCount/AheadCount on each item
// come from the meta's sidebar enrichment (project lookup + per-worktree git
// ops); cost / message / stuck calculations come from store aggregations.
func (s *WorkspaceService) BuildOverview(ctx context.Context, metas []WorkspaceSidebarMeta) (WorkspaceOverview, error) {
	now := time.Now()

	// Lifetime per-workspace token + message stats, scoped to the workspaces
	// in this (already filtered) meta slice. Replaces the old $-cost rollup.
	workspaceIDs := make([]int64, 0, len(metas))
	for _, m := range metas {
		workspaceIDs = append(workspaceIDs, m.Workspace.ID)
	}
	statsMap := make(map[int64]store.WorkspaceStat, len(workspaceIDs))
	if len(workspaceIDs) > 0 {
		statsRows, err := s.q.ListWorkspaceStatsForWorkspaces(ctx, workspaceIDs)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return WorkspaceOverview{}, err
		}
		for _, r := range statsRows {
			statsMap[r.WorkspaceID] = r
		}
	}

	msgRows, err := s.q.AggregateWorkspaceMessages(ctx)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return WorkspaceOverview{}, err
	}
	msgMap := make(map[int64]struct {
		count int64
		lastAt *time.Time
	}, len(msgRows))
	for _, r := range msgRows {
		msgMap[r.WorkspaceID] = struct {
			count  int64
			lastAt *time.Time
		}{
			count:  r.MessageCount,
			lastAt: asTimePtr(r.LastMessageAt),
		}
	}

	out := WorkspaceOverview{Workspaces: make([]WorkspaceOverviewItem, 0, len(metas))}
	for _, m := range metas {
		ws := m.Workspace
		st := statsMap[ws.ID]
		msgs := msgMap[ws.ID]
		var statsLastAt *time.Time
		if st.LastActivityAt.Valid {
			t := st.LastActivityAt.Time
			statsLastAt = &t
		}
		lastActivity := mostRecent(statsLastAt, msgs.lastAt, &ws.UpdatedAt)
		isArchived := ws.IsArchived == 1
		isStuck := false
		// Stuck := agent_status="running" AND no activity for stuckThreshold.
		// session_status alone is unreliable (idle-but-process-alive is also "running"
		// in agent_status); use updated_at as the last server-side touch fallback.
		if ws.Status == "running" && !isArchived {
			latest := lastActivity
			if latest != nil && now.Sub(*latest) >= stuckThreshold {
				isStuck = true
			}
		}
		item := WorkspaceOverviewItem{
			WorkspaceID:    ws.ID,
			Name:           ws.Name,
			OwnerType:      ws.OwnerType,
			OwnerID:        ws.OwnerID,
			Status:         ws.Status,
			UpdatedAt:      ws.UpdatedAt,
			LastActivityAt: lastActivity,
			MessageCount:        msgs.count,
			UserMessageCount:    st.UserMessageCount,
			AiMessageCount:      st.AiMessageCount,
			InputTokens:         st.InputTokens,
			OutputTokens:        st.OutputTokens,
			CacheCreationTokens: st.CacheCreationTokens,
			CacheReadTokens:     st.CacheReadTokens,
			IsStuck:             isStuck,
			IsArchived:          isArchived,
			ProjectName:         m.ProjectName,
			ChangesCount:        m.ChangesCount,
			AheadCount:          m.AheadCount,
		}
		if ws.SessionStatus.Valid {
			item.SessionStatus = ws.SessionStatus.String
		}
		if ws.CreatedBy.Valid {
			id := ws.CreatedBy.Int64
			item.CreatedBy = &id
		}
		out.Workspaces = append(out.Workspaces, item)

		out.Summary.TotalCount++
		out.Summary.UserMessageCount += st.UserMessageCount
		out.Summary.AiMessageCount += st.AiMessageCount
		out.Summary.InputTokens += st.InputTokens
		out.Summary.OutputTokens += st.OutputTokens
		out.Summary.CacheCreationTokens += st.CacheCreationTokens
		out.Summary.CacheReadTokens += st.CacheReadTokens
		switch ws.Status {
		case "running", "needs_review", "attention":
			if !isArchived {
				out.Summary.ActiveCount++
			}
		}
		if isStuck {
			out.Summary.StuckCount++
		}
	}

	// Sort: stuck first, then active, then by last activity desc, then name.
	sort.SliceStable(out.Workspaces, func(i, j int) bool {
		a, b := out.Workspaces[i], out.Workspaces[j]
		if a.IsStuck != b.IsStuck {
			return a.IsStuck
		}
		ai, bi := isActiveStatus(a.Status), isActiveStatus(b.Status)
		if ai != bi {
			return ai
		}
		at := timeOrZero(a.LastActivityAt)
		bt := timeOrZero(b.LastActivityAt)
		if !at.Equal(bt) {
			return at.After(bt)
		}
		return a.Name < b.Name
	})

	return out, nil
}

func isActiveStatus(s string) bool {
	switch s {
	case "running", "needs_review", "attention":
		return true
	}
	return false
}

func mostRecent(times ...*time.Time) *time.Time {
	var latest *time.Time
	for _, t := range times {
		if t == nil {
			continue
		}
		if latest == nil || t.After(*latest) {
			latest = t
		}
	}
	return latest
}

func timeOrZero(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// asFloat coerces sqlc's interface{} (from COALESCE+SUM, where the driver
// can't disambiguate the column type) into a float64. Handles the int64,
// float64, and []byte cases produced by SQLite/Postgres respectively.
func asFloat(v interface{}) float64 {
	if v == nil {
		return 0
	}
	switch x := v.(type) {
	case float64:
		return x
	case float32:
		return float64(x)
	case int64:
		return float64(x)
	case int:
		return float64(x)
	case []byte:
		var f float64
		if _, err := fmt.Sscanf(string(x), "%f", &f); err == nil {
			return f
		}
	case string:
		var f float64
		if _, err := fmt.Sscanf(x, "%f", &f); err == nil {
			return f
		}
	}
	return 0
}

// timeFormats are the layouts SQLite and Postgres can stamp into TIMESTAMP
// columns when scanning into interface{}. Tried in order.
var timeFormats = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05-07:00",
	"2006-01-02 15:04:05",
}

// asTimePtr coerces sqlc's interface{} (from MAX(created_at)) into *time.Time.
// Returns nil when the source is NULL or unparseable.
func asTimePtr(v interface{}) *time.Time {
	if v == nil {
		return nil
	}
	switch x := v.(type) {
	case time.Time:
		if x.IsZero() {
			return nil
		}
		return &x
	case *time.Time:
		return x
	case []byte:
		return parseTimeFormats(string(x))
	case string:
		return parseTimeFormats(x)
	}
	return nil
}

func parseTimeFormats(s string) *time.Time {
	for _, layout := range timeFormats {
		if t, err := time.Parse(layout, s); err == nil {
			return &t
		}
	}
	return nil
}

// CreatorBrief is a minimal user descriptor returned by
// ListOverviewCreators for the overview-page creator picker.
type CreatorBrief struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
}

// ErrForbiddenOwnerScope is returned by ListOverviewCreators when the
// caller requests an ownerFilter (org or user) outside their accessible
// set. Handler maps this to HTTP 403.
var ErrForbiddenOwnerScope = errors.New("owner scope not accessible")

// OwnerScope is the service-level view of the parsed ?owner= filter.
// Mirrors api.OwnerFilter; defined here to avoid an api -> service import
// cycle. The api handler converts api.OwnerFilter -> service.OwnerScope.
type OwnerScope struct {
	Type string // "" | "user" | "org"
	ID   int64
}

// ListOverviewCreators returns the distinct set of users who created at
// least one workspace inside the requested ownerFilter scope. Empty
// ownerFilter means the caller's full Authz set.
//
// Security: when ownerFilter narrows scope (e.g. org:acme), this method
// MUST validate that the caller is a member before issuing the SQL --
// otherwise picker candidates leak across orgs. See spec sec 5.3.
//
// NULL created_by rows are excluded -- picker has no "unknown" entry.
func (s *WorkspaceService) ListOverviewCreators(ctx context.Context, userID int64, ownerFilter OwnerScope) ([]CreatorBrief, error) {
	owners, err := s.authz.Accessible(ctx, userID)
	if err != nil {
		return nil, err
	}

	// Resolve final (ownerUserID, orgIDs) tuple after applying ownerFilter.
	ownerUserID := owners.UserID
	orgIDs := owners.OrgIDs

	if ownerFilter.Type != "" {
		switch ownerFilter.Type {
		case "user":
			if ownerFilter.ID != owners.UserID {
				return nil, ErrForbiddenOwnerScope
			}
			orgIDs = nil // personal-only
		case "org":
			isMember := false
			for _, id := range owners.OrgIDs {
				if id == ownerFilter.ID {
					isMember = true
					break
				}
			}
			if !isMember {
				return nil, ErrForbiddenOwnerScope
			}
			ownerUserID = -1 // sentinel: no personal scope
			orgIDs = []int64{ownerFilter.ID}
		}
	}

	if len(orgIDs) == 0 {
		orgIDs = []int64{-1} // SQL needs non-empty IN clause
	}

	rows, err := s.q.ListWorkspaceCreatorsForOwners(ctx, store.ListWorkspaceCreatorsForOwnersParams{
		OwnerID: ownerUserID,
		OrgIds:  orgIDs,
	})
	if err != nil {
		return nil, err
	}
	out := make([]CreatorBrief, len(rows))
	for i, r := range rows {
		out[i] = CreatorBrief{
			ID:          r.ID,
			Username:    r.Username,
			DisplayName: r.DisplayName,
		}
	}
	return out, nil
}
