package agentproxy

import (
	"context"
	"errors"
)

// ErrModelBudgetExceeded is returned when an owner has used up their monthly
// token allowance. This is a hard stop: no new session or turn may start until
// the budget is raised or the next UTC month begins.
var ErrModelBudgetExceeded = errors.New("model token budget exceeded")

// BudgetDecision is the outcome of a budget check. Allowed=false is a hard
// stop; ProjectWarning is advisory only and the request still runs.
type BudgetDecision struct {
	Allowed        bool
	OwnerExceeded  bool
	ProjectWarning bool
	UsedTokens     int64
	LimitTokens    int64
	Period         string
}

// ModelBudgetChecker is the enforcement hook injected into AgentProxy. A nil
// checker means budgets are not configured and every request is allowed.
type ModelBudgetChecker interface {
	Check(ctx context.Context, ownerType string, ownerID, projectID int64) (BudgetDecision, error)
}
