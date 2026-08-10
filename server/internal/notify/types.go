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
)
