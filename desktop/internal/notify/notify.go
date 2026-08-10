package notify

import (
	"log/slog"

	"github.com/wailsapp/wails/v3/pkg/services/notifications"
)

type Service struct {
	notifier *notifications.NotificationService
	enabled  bool
}

func New(notifier *notifications.NotificationService) *Service {
	s := &Service{notifier: notifier, enabled: true}
	s.registerCategories()
	return s
}

func (s *Service) SetEnabled(enabled bool) { s.enabled = enabled }

func (s *Service) registerCategories() {
	s.notifier.RegisterNotificationCategory(notifications.NotificationCategory{
		ID:      "agent_event",
		Actions: []notifications.NotificationAction{{ID: "OPEN", Title: "Open"}},
	})
	s.notifier.RegisterNotificationCategory(notifications.NotificationCategory{
		ID:      "connection_event",
		Actions: []notifications.NotificationAction{{ID: "OPEN", Title: "Open"}},
	})
}

func (s *Service) AgentDone(workspaceName string) {
	s.send("agent_done_"+workspaceName, "Agent Completed", "", workspaceName+": Agent finished task", "agent_event")
}

func (s *Service) AgentFailed(workspaceName, errMsg string) {
	s.send("agent_fail_"+workspaceName, "Agent Failed", "", workspaceName+": "+errMsg, "agent_event")
}

func (s *Service) ConnectionLost(name, key string) {
	s.send("conn_lost_"+key, "Connection Lost", "", "Connection to "+name+" lost", "connection_event")
}

func (s *Service) ConnectionRestored(name, key string) {
	s.send("conn_restored_"+key, "Reconnected", "", "Reconnected to "+name, "connection_event")
}

// ConnectionAbandoned notifies that reconnection attempts have been exhausted.
func (s *Service) ConnectionAbandoned(name, key string) {
	s.send("conn_max_"+key, "Connection Failed", "", "Stopped reconnecting to "+name, "connection_event")
}

func (s *Service) UpdateAvailable(version string) {
	s.send("update", "Update Available", "", "Niuniu Desktop "+version+" is available", "")
}

func (s *Service) send(id, title, subtitle, body, categoryID string) {
	if !s.enabled {
		return
	}
	opts := notifications.NotificationOptions{
		ID: id, Title: title, Subtitle: subtitle, Body: body,
	}
	if categoryID != "" {
		opts.CategoryID = categoryID
		s.notifier.SendNotificationWithActions(opts)
	} else {
		s.notifier.SendNotification(opts)
	}
	slog.Debug("notification sent", "id", id, "title", title)
}
