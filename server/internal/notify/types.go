package notify

// Notification is a lightweight signal broadcast to all connected frontends.
// It carries no data — frontends use it to invalidate TanStack Query caches
// and trigger on-demand REST fetches.
//
// Owner filtering (spec §5.9): if OwnerType is non-empty the hub only delivers
// the notification to connections whose accessible-owner set includes the
// (OwnerType, OwnerID) pair. Leaving OwnerType empty ("") means "broadcast to
// all" which preserves pre-existing behaviour for publishers that have not yet
// been updated to set owner metadata.
type Notification struct {
	Topic     string `json:"topic"`
	Action    string `json:"action"`
	ID        int64  `json:"id,omitempty"`
	Extra     any    `json:"extra,omitempty"`
	OwnerType string `json:"-"` // not sent over the wire; used only for hub-side filtering
	OwnerID   int64  `json:"-"` // not sent over the wire; used only for hub-side filtering
}

// Topic constants
const (
	TopicWorkspace         = "workspace"
	TopicDiff              = "diff"
	TopicGitStatus         = "git_status"
	TopicProject           = "project"
	TopicKanban            = "kanban"
	TopicSchedule          = "schedule"
	TopicQueue             = "queue"
	TopicPing              = "ping"
	TopicOrgMember         = "org_member"
	TopicWorkspaceBgTask   = "workspace_bg_task"
	TopicPermissionPending = "permission_pending"
	TopicAskUserPending    = "ask_user_pending"
	TopicPinned            = "pinned"
	TopicLocalRunner       = "local_runner"
	// TopicIntegrationHealth signals a change in an integration source's health
	// (auto-disabled after repeated probe failures, or upstream contract drift).
	// Action carries the alert kind ("auto_disabled" / "drift"); ID is the
	// subject id and Extra carries {subject_kind, name, message} so the SPA can
	// invalidate the integration lists and show a localized toast (#744).
	TopicIntegrationHealth = "integration_health"
	// TopicEgressAlert signals that an outbound integration write tripped the
	// egress data policy (over-threshold body size, or a sensitive-pattern hit).
	// Action is "flagged"; ID is the provider id and Extra carries {provider,
	// method, path, bytes, limit, oversize, patterns} so the SPA can show a
	// localized warning toast (Epic #726 / #747). The personal edition only ever
	// warns — this topic never precedes a refusal.
	TopicEgressAlert = "egress_alert"
)
