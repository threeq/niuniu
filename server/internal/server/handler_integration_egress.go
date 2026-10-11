// handler_integration_egress.go -- egress data-governance alerts (Epic #726 /
// #747).
//
// When an outbound integration write trips the egress policy (a body over the
// size ceiling, or a sensitive-pattern hit), the proxy service hands the
// verdict to this notifier. The personal edition only ever warns, so the alert
// is a warning — the SPA invalidates the integration activity feed and shows a
// localized toast; the audit row is the durable record.
//
// The notifier implements service.EgressAlertNotifier and is wired in server.go
// so the service package stays free of the notify hub (the same seam #744 uses
// for integration health).
package server

import (
	"context"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/niuniu-dev/niuniu/internal/notify"
	"github.com/niuniu-dev/niuniu/internal/service"
)

// egressAlertNotifier broadcasts egress warnings to the station-in notification
// hub and, best-effort, to the owner's IM Bot chats.
type egressAlertNotifier struct {
	hub      *notify.NotificationHub
	imbotSvc *service.IMBotService
}

// NotifyEgressAlert broadcasts a station-in notification (the SPA invalidates
// the activity feed and toasts) and pushes a short, value-free line to the
// owner's active IM chats when the alert has an owner.
//
// The notification is value-free by construction — it carries the summary's
// categories and byte count, never a field value.
func (n *egressAlertNotifier) NotifyEgressAlert(ctx context.Context, alert service.EgressAlert) {
	if n.hub != nil {
		n.hub.Broadcast(notify.Notification{
			Topic:  notify.TopicEgressAlert,
			Action: "flagged",
			ID:     alert.ProviderID,
			Extra: gin.H{
				"provider": alert.Provider,
				"method":   alert.Method,
				"path":     alert.Path,
				"bytes":    alert.Summary.Bytes,
				"limit":    service.DefaultEgressWarnBytes,
				"oversize": alert.Summary.Bytes > service.DefaultEgressWarnBytes,
				"patterns": sensitivePatternNames(alert.Summary),
				"reasons":  alert.Verdict.Reasons,
			},
			OwnerType: alert.OwnerType,
			OwnerID:   alert.OwnerID,
		})
	}
	if n.imbotSvc != nil && alert.OwnerType != "" && alert.OwnerID != 0 {
		n.imbotSvc.NotifyOwnerText(ctx, service.OwnerRef{Type: alert.OwnerType, ID: alert.OwnerID},
			"[出口告警] "+alert.Provider+" "+alert.Method+" "+alert.Path+
				"：外发请求体 "+strconv.FormatInt(alert.Summary.Bytes, 10)+" 字节命中出口策略，详见集成活动。")
	}
}

// sensitivePatternNames lists the matched categories, for the toast.
func sensitivePatternNames(s service.EgressDataSummary) []string {
	names := make([]string, 0, len(s.SensitiveHits))
	for _, h := range s.SensitiveHits {
		names = append(names, h.Pattern)
	}
	return names
}
