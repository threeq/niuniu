package server

import (
	"context"

	"github.com/niuniu-dev/niuniu/internal/notify"
	"github.com/niuniu-dev/niuniu/internal/service"
	"github.com/niuniu-dev/niuniu/internal/store"
)

// approvalNotifier fans integration-approval lifecycle events to the notify hub,
// targeted per eligible approver. It implements service.ApprovalEvents.
//
// Routing to specific users (rather than the org-wide broadcast the permission
// broker uses) is the whole point of issue #741: the confirmation card must
// land on members holding the required role, never the requester.
type approvalNotifier struct {
	hub *notify.NotificationHub
}

func newApprovalNotifier(hub *notify.NotificationHub) *approvalNotifier {
	return &approvalNotifier{hub: hub}
}

func (n *approvalNotifier) ApprovalChanged(_ context.Context, kind service.ApprovalEventKind, req store.IntegrationApprovalRequest, approverIDs []int64) {
	if n == nil || n.hub == nil {
		return
	}
	extra := map[string]any{
		"kind":       string(kind),
		"request_id": req.ID,
		"org_id":     req.OwnerID,
		"action":     req.Action,
		"status":     req.Status,
	}
	seen := map[int64]bool{}
	push := func(uid int64) {
		if uid == 0 || seen[uid] {
			return
		}
		seen[uid] = true
		n.hub.Broadcast(notify.Notification{
			Topic:     notify.TopicIntegrationApproval,
			Action:    string(kind),
			ID:        req.ID,
			Extra:     extra,
			OwnerType: "user",
			OwnerID:   uid,
		})
	}
	// Eligible approvers own the decision.
	for _, uid := range approverIDs {
		push(uid)
	}
	// On a decided/invalidated transition, also refresh the requester's view.
	if kind == service.ApprovalEventDecided || kind == service.ApprovalEventInvalidated {
		push(req.RequestedBy)
	}
}
