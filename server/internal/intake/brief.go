package intake

import (
	"fmt"
	"strings"
)

// DeliverySpec says where the result of a task is reported back.
type DeliverySpec struct {
	SourceKind  string
	ThreadExtID string
	ReplyMode   string // thread | chat | pr_comment
}

// Provenance records why a task was accepted: the decision, the policy version
// in force, the authorization strength, the actor and the matched rules.
type Provenance struct {
	DecisionID    int64
	PolicyVersion int64
	Strength      Strength
	ActorExtID    string
	RuleIDs       []int64
}

// TaskBrief is the stable, versioned definition of one task: what result is
// wanted, how it is verified, and where the result goes.
//
// Acceptance is the definition of done. It is materialized as checklist rows
// with kind="acceptance" and is what completion validates; manual progress
// items are a separate, unversioned list. GoalCondition renders the cached
// issues.goal_condition from this brief — the brief remains the source of
// truth.
type TaskBrief struct {
	Goal       string
	Acceptance []string
	ScopeIn    []string
	ScopeOut   []string
	Delivery   DeliverySpec
	Provenance Provenance
}

// NewBrief drafts a brief from an accepted trigger. The draft is deliberately
// minimal — a human can edit it in the calibration queue before it is executed.
func NewBrief(t Trigger, prov Provenance) TaskBrief {
	return TaskBrief{
		Goal: strings.TrimSpace(t.Text),
		Delivery: DeliverySpec{
			SourceKind:  t.SourceKind,
			ThreadExtID: t.Thread.ThreadExtID,
			ReplyMode:   "thread",
		},
		Provenance: prov,
	}
}

// GoalCondition renders the brief into the text stored in issues.goal_condition
// and handed to completion. Only the goal and acceptance take part: scope is
// context for the agent, not a completion criterion.
func (b TaskBrief) GoalCondition() string {
	goal := strings.TrimSpace(b.Goal)
	items := make([]string, 0, len(b.Acceptance))
	for _, item := range b.Acceptance {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	if len(items) == 0 {
		return goal
	}

	var sb strings.Builder
	if goal != "" {
		sb.WriteString(goal)
		sb.WriteString("\n\n")
	}
	sb.WriteString("验收标准（全部满足才算完成）：")
	for _, item := range items {
		fmt.Fprintf(&sb, "\n- %s", item)
	}
	return sb.String()
}
