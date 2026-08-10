package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// epicReviewConfirmer adapts the ask-user broker to the EpicExecutionService's
// ReviewConfirmer hook (spec §22.7/§23.1): when an epic's review workspace finishes,
// it poses one lightweight confirmation question — the epic's goal_condition 对账
// summary — to the user via the same channel as niuniu_ask_user_question, and reports
// whether they explicitly confirmed completion. A decline / timeout / cancel returns
// false so the epic stays in review rather than silently finalizing.
type epicReviewConfirmer struct {
	askUser *service.AskUserService
}

// epicReviewConfirmLabel is the option that means "yes, finalize the epic". It must
// match the label posed below; any other answer (or no answer) leaves the epic open.
const epicReviewConfirmLabel = "确认完成"

// ConfirmEpicReview implements service.ReviewConfirmer.
func (c *epicReviewConfirmer) ConfirmEpicReview(ctx context.Context, ws store.Workspace, epic store.Issue, children []store.Issue) (bool, error) {
	q := event.AskUserQuestion{
		Question: buildEpicReviewQuestion(epic, children),
		Header:   "完成确认",
		Options: []event.AskUserQuestionOption{
			{Label: epicReviewConfirmLabel, Description: "产出符合目标，标记 Epic 完成", Recommended: true},
			{Label: "继续完善", Description: "尚未达标，留在评审让我处理"},
		},
	}
	res, err := c.askUser.Request(ctx, ws.ID, ws.OwnerType, ws.OwnerID, "", []event.AskUserQuestion{q})
	if err != nil {
		return false, err
	}
	if !res.Answered {
		return false, nil
	}
	for _, a := range res.Answers {
		for _, label := range a.Labels {
			if label == epicReviewConfirmLabel {
				return true, nil
			}
		}
	}
	return false, nil
}

// buildEpicReviewQuestion composes the 对账 prompt: the epic title, its inferred
// goal_condition (the thing being reconciled against), and a child-count summary.
func buildEpicReviewQuestion(epic store.Issue, children []store.Issue) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Epic「%s」评审完成。", epic.Title)
	if g := strings.TrimSpace(epic.GoalCondition); g != "" {
		fmt.Fprintf(&b, "\n\n目标(goal_condition)：%s", g)
	}
	if n := len(children); n > 0 {
		fmt.Fprintf(&b, "\n\n共 %d 个子任务已完成并合入 epic 分支。", n)
	}
	b.WriteString("\n\n请确认产出是否符合目标？")
	return b.String()
}
