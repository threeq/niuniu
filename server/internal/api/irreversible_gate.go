package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// IrreversibleAskBroker is the subset of *service.AskUserService the irreversible-op
// gate needs: a blocking user confirmation. *service.AskUserService satisfies it.
type IrreversibleAskBroker interface {
	Request(ctx context.Context, workspaceID int64, ownerType string, ownerID int64,
		sessionID string, questions []event.AskUserQuestion) (service.AskUserResult, error)
}

// irreversibleConfirmLabel is the option that means "yes, run the destructive op".
const irreversibleConfirmLabel = "确认执行"

// IrreversibleOpGate forces an agent's MCP call to an irreversible operation
// (delete issue, batch delete, ...) to be confirmed by the user through the ask-user
// broker BEFORE the destructive handler runs — EVEN under autohost / bypassPermissions,
// which skips the chat permission prompt entirely (spec §23.5: 把安全红线做成与底线闸
// 同源的"单点代码强制", 而非散落 prompt). It guards only /mcp/* routes (agent-initiated);
// SPA /api routes are the user's own click and are never gated.
//
// Fail-CLOSED: a missing broker, missing workspace context, broker error, decline, or
// timeout all abort the operation with 403 so the destructive handler never runs. The
// blocking confirm reuses the same broker as niuniu_ask_user_question, so it surfaces
// as an inline question in the workspace chat regardless of permission mode.
//
// q is optional (nil-safe): when set, the workspace's owner is resolved so the
// confirmation routes to the right owner's notify hub; when nil the confirm still
// fires against the workspace, just without owner metadata.
func IrreversibleOpGate(broker IrreversibleAskBroker, q *store.Queries, opLabel string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if broker == nil {
			abortIrreversible(c, opLabel, "no confirmation broker configured")
			return
		}
		wsID := c.GetInt64("mcp_workspace_id")
		if wsID == 0 {
			abortIrreversible(c, opLabel, "no workspace context to confirm against")
			return
		}
		var ownerType string
		var ownerID int64
		if q != nil {
			if ws, err := q.GetWorkspace(c.Request.Context(), wsID); err == nil {
				ownerType, ownerID = ws.OwnerType, ws.OwnerID
			}
		}
		res, err := broker.Request(c.Request.Context(), wsID, ownerType, ownerID, "",
			[]event.AskUserQuestion{irreversibleQuestion(opLabel, c.Param("id"))})
		if err != nil || !res.Answered || !irreversibleConfirmed(res) {
			abortIrreversible(c, opLabel, "operation was not confirmed by the user")
			return
		}
		c.Next()
	}
}

func abortIrreversible(c *gin.Context, opLabel, reason string) {
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
		"error": "irreversible operation requires user confirmation: " + reason,
		"op":    opLabel,
	})
}

func irreversibleQuestion(opLabel, targetID string) event.AskUserQuestion {
	q := fmt.Sprintf("AI 请求执行不可逆操作「%s」", opLabel)
	if targetID != "" {
		q += fmt.Sprintf("（目标 #%s）", targetID)
	}
	q += "。此操作不可恢复，是否允许？"
	return event.AskUserQuestion{
		Question: q,
		Header:   "不可逆操作",
		Options: []event.AskUserQuestionOption{
			{Label: irreversibleConfirmLabel, Description: "确认执行该不可逆操作"},
			{Label: "取消", Description: "不执行此操作", Recommended: true},
		},
	}
}

func irreversibleConfirmed(res service.AskUserResult) bool {
	for _, a := range res.Answers {
		for _, label := range a.Labels {
			if label == irreversibleConfirmLabel {
				return true
			}
		}
	}
	return false
}
