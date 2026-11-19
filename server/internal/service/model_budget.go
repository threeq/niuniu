package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/niuniu-dev/niuniu/internal/agentproxy"
	"github.com/niuniu-dev/niuniu/internal/notify"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// ModelBudgetView is the owner-facing budget snapshot for a month: the owner
// hard cap, how much of it is used, and the per-project soft caps.
type ModelBudgetView struct {
	OwnerType     string               `json:"owner_type"`
	OwnerID       int64                `json:"owner_id"`
	MonthlyTokens int64                `json:"monthly_tokens"`
	UsedTokens    int64                `json:"used_tokens"`
	Period        string               `json:"period"`
	Projects      []ModelProjectBudget `json:"projects"`
}

// ModelProjectBudget is one project's soft cap and its usage this month.
type ModelProjectBudget struct {
	ProjectID     int64 `json:"project_id"`
	MonthlyTokens int64 `json:"monthly_tokens"`
	UsedTokens    int64 `json:"used_tokens"`
}

// ModelBudgetService enforces monthly model-token budgets. The owner cap is a
// hard limit (new sessions/turns are refused); project caps are advisory
// (work continues, the owner is warned once per period). All month boundaries
// are UTC. Lookup failures are fail-open: a broken usage query must never stop
// the product from working.
type ModelBudgetService struct {
	q   *store.Queries
	hub *notify.NotificationHub

	now func() time.Time

	mu       sync.Mutex
	notified map[string]bool // dedupe: one notice per owner/project per period
}

func NewModelBudgetService(q *store.Queries, hub *notify.NotificationHub) *ModelBudgetService {
	return &ModelBudgetService{
		q:        q,
		hub:      hub,
		now:      time.Now,
		notified: make(map[string]bool),
	}
}

// Check reports whether a new session or turn may start for the owner and,
// when projectID > 0, whether that project has crossed its soft cap.
func (s *ModelBudgetService) Check(ctx context.Context, ownerType string, ownerID, projectID int64) (agentproxy.BudgetDecision, error) {
	start, end := s.period()
	dec := agentproxy.BudgetDecision{Allowed: true, Period: start.Format("2006-01")}

	ownerRow, err := s.q.GetModelBudget(ctx, store.GetModelBudgetParams{
		OwnerType: ownerType, OwnerID: ownerID, ProjectID: 0,
	})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		slog.Warn("model budget: owner lookup failed (fail-open)", "error", err)
		return dec, nil
	}
	if err == nil && ownerRow.Enabled != 0 && ownerRow.MonthlyTokens > 0 {
		used, uerr := s.q.SumModelUsageByOwner(ctx, store.SumModelUsageByOwnerParams{
			OwnerType: ownerType, OwnerID: ownerID, BucketHour: start, BucketHour_2: end,
		})
		if uerr != nil {
			slog.Warn("model budget: owner usage failed (fail-open)", "error", uerr)
			return dec, nil
		}
		dec.UsedTokens, dec.LimitTokens = used, ownerRow.MonthlyTokens
		if used >= ownerRow.MonthlyTokens {
			dec.Allowed, dec.OwnerExceeded = false, true
			s.notifyOnce(ownerType, ownerID, dec.Period, used, ownerRow.MonthlyTokens)
			return dec, nil
		}
	}

	if projectID > 0 {
		projectRow, perr := s.q.GetModelBudget(ctx, store.GetModelBudgetParams{
			OwnerType: ownerType, OwnerID: ownerID, ProjectID: projectID,
		})
		if perr == nil && projectRow.Enabled != 0 && projectRow.MonthlyTokens > 0 {
			used, uerr := s.q.SumModelUsageByProject(ctx, store.SumModelUsageByProjectParams{
				ProjectID: projectID, BucketHour: start, BucketHour_2: end,
			})
			// Soft cap: usage errors are fail-open (no warning, work proceeds).
			if uerr == nil && used >= projectRow.MonthlyTokens {
				dec.ProjectWarning = true
				s.notifyProjectOnce(ownerType, ownerID, projectID, dec.Period, used, projectRow.MonthlyTokens)
			}
		}
	}
	return dec, nil
}

// Get returns the owner's budget snapshot for the current UTC month.
func (s *ModelBudgetService) Get(ctx context.Context, owner OwnerRef) (ModelBudgetView, error) {
	if err := owner.Validate(); err != nil {
		return ModelBudgetView{}, err
	}
	start, end := s.period()
	view := ModelBudgetView{
		OwnerType: owner.Type,
		OwnerID:   owner.ID,
		Period:    start.Format("2006-01"),
		Projects:  []ModelProjectBudget{},
	}

	rows, err := s.q.ListModelBudgetsForOwner(ctx, store.ListModelBudgetsForOwnerParams{
		OwnerType: owner.Type, OwnerID: owner.ID,
	})
	if err != nil {
		return ModelBudgetView{}, fmt.Errorf("list model budgets: %w", err)
	}
	used, err := s.q.SumModelUsageByOwner(ctx, store.SumModelUsageByOwnerParams{
		OwnerType: owner.Type, OwnerID: owner.ID, BucketHour: start, BucketHour_2: end,
	})
	if err != nil {
		return ModelBudgetView{}, fmt.Errorf("sum model usage: %w", err)
	}
	view.UsedTokens = used

	for _, r := range rows {
		if r.ProjectID == 0 {
			view.MonthlyTokens = r.MonthlyTokens
			continue
		}
		pUsed, perr := s.q.SumModelUsageByProject(ctx, store.SumModelUsageByProjectParams{
			ProjectID: r.ProjectID, BucketHour: start, BucketHour_2: end,
		})
		if perr != nil {
			return ModelBudgetView{}, fmt.Errorf("sum project %d usage: %w", r.ProjectID, perr)
		}
		view.Projects = append(view.Projects, ModelProjectBudget{
			ProjectID:     r.ProjectID,
			MonthlyTokens: r.MonthlyTokens,
			UsedTokens:    pUsed,
		})
	}
	return view, nil
}

// Set stores the owner's monthly hard cap. 0 means unlimited.
func (s *ModelBudgetService) Set(ctx context.Context, owner OwnerRef, monthlyTokens int64) error {
	if err := owner.Validate(); err != nil {
		return err
	}
	_, err := s.q.UpsertModelBudget(ctx, store.UpsertModelBudgetParams{
		OwnerType:     owner.Type,
		OwnerID:       owner.ID,
		ProjectID:     0,
		MonthlyTokens: monthlyTokens,
		Enabled:       enabledFlag(monthlyTokens),
	})
	return err
}

// SetProject stores a project's soft cap. 0 means unlimited.
func (s *ModelBudgetService) SetProject(ctx context.Context, owner OwnerRef, projectID, monthlyTokens int64) error {
	if err := owner.Validate(); err != nil {
		return err
	}
	if projectID <= 0 {
		return fmt.Errorf("invalid project id %d", projectID)
	}
	_, err := s.q.UpsertModelBudget(ctx, store.UpsertModelBudgetParams{
		OwnerType:     owner.Type,
		OwnerID:       owner.ID,
		ProjectID:     projectID,
		MonthlyTokens: monthlyTokens,
		Enabled:       enabledFlag(monthlyTokens),
	})
	return err
}

// DeleteProject removes a project's soft cap. Idempotent.
func (s *ModelBudgetService) DeleteProject(ctx context.Context, owner OwnerRef, projectID int64) error {
	if err := owner.Validate(); err != nil {
		return err
	}
	_, err := s.q.DeleteModelBudget(ctx, store.DeleteModelBudgetParams{
		OwnerType: owner.Type, OwnerID: owner.ID, ProjectID: projectID,
	})
	return err
}

// period returns the current UTC month as [start, nextStart).
func (s *ModelBudgetService) period() (time.Time, time.Time) {
	now := s.now().UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	return start, start.AddDate(0, 1, 0)
}

func enabledFlag(monthlyTokens int64) int64 {
	if monthlyTokens > 0 {
		return 1
	}
	return 0
}

func (s *ModelBudgetService) notifyOnce(ownerType string, ownerID int64, period string, used, limit int64) {
	if s.hub == nil {
		return
	}
	key := fmt.Sprintf("owner:%s:%d:%s", ownerType, ownerID, period)
	s.mu.Lock()
	if s.notified[key] {
		s.mu.Unlock()
		return
	}
	s.notified[key] = true
	s.mu.Unlock()
	s.hub.Broadcast(notify.Notification{
		Topic:     notify.TopicModelBudget,
		Action:    "exceeded",
		OwnerType: ownerType,
		OwnerID:   ownerID,
		Extra:     map[string]any{"period": period, "used": used, "limit": limit},
	})
}

func (s *ModelBudgetService) notifyProjectOnce(ownerType string, ownerID, projectID int64, period string, used, limit int64) {
	if s.hub == nil {
		return
	}
	key := fmt.Sprintf("project:%s:%d:%d:%s", ownerType, ownerID, projectID, period)
	s.mu.Lock()
	if s.notified[key] {
		s.mu.Unlock()
		return
	}
	s.notified[key] = true
	s.mu.Unlock()
	s.hub.Broadcast(notify.Notification{
		Topic:     notify.TopicModelBudget,
		Action:    "project_warning",
		OwnerType: ownerType,
		OwnerID:   ownerID,
		Extra:     map[string]any{"period": period, "used": used, "limit": limit, "project_id": projectID},
	})
}
