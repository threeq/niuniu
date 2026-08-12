import type {
  RunPhaseEventPayload, GateJobStartedPayload,
  GateProgressPayload, GateDonePayload, AgentLifecyclePayload,
} from './harness'
import type { SelectedUser } from './org'

// Base types
export interface BaseEntity {
  id: number;
  created_at: string;
  updated_at: string;
}

// Project types
export interface Project extends BaseEntity {
  name: string;
  description: string | null;
  status: 'active' | 'hidden';
  color?: string | null;            // palette key like 'emerald'，null/缺失为未设
  default_cli_type?: 'claude' | 'codex' | 'qwen' | 'omp' | 'goose';  // 项目默认 agent，新建工作区时预选
  issue_stats?: { column_name: string; count: number }[];
  ws_stats?: { status: string; count: number }[];
  owner?: import('./org').OwnerRef;
  project_default_branch?: string;
  repositories?: ProjectRepositoryBinding[];
}

export interface ProjectRepositoryBinding {
  repository_id: number;
  name: string;
  path: string;
  repo_default_branch: string;
  project_default_branch: string;
}

export interface CreateProjectData {
  name: string;
  description?: string;
}

// Kanban types
export interface Column extends BaseEntity {
  project_id: number;
  name: string;
  position: number;
  lifecycle_mapping: string;
  // Phase 1 extension fields (already in DB; exposed from Phase 3 frontend)
  reviewer_agent?: string;
  phase_prompt?: string;
  auto_advance?: boolean;
}

export type Label = {
  id: number;
  project_id: number;
  name: string;
  color: string;
  description: string;
  created_at: string;
  created_by: number;
  usage_count?: number;
};

export type AssignableUser = {
  id: number;
  username: string;
  display_name: string;
  role?: 'owner' | 'admin' | 'member';
};

export type IssueAssignee = {
  id: number;
  username: string;
  display_name: string;
};

export interface Issue extends BaseEntity {
  column_id: number
  project_id: number
  title: string
  description: string | null
  position: number
  assignees: IssueAssignee[]
  priority?: number
  labels: Label[]
  lifecycle_status?: string
  start_date?: string
  due_date?: string
  estimate_type?: string
  estimate?: number
  actual_time?: number
  checklist_stats?: ChecklistStats
  /** External issue tracker fields — populated when this issue was imported from
   * an external source (e.g. GitHub). `external_source` is the provider name
   * (empty string if not external); `external_id`/`external_url` carry the
   * remote identifier and link. */
  external_source?: string
  external_id?: string
  external_url?: string
  external_number?: number
  /** ISO timestamp of the last successful pull/push from the external tracker.
   *  Empty string when never synced (sql.NullString → string|null serialized as
   *  string). */
  external_snapshot_at?: string | null
  /** v1.1: read-only snapshot of upstream comments fetched on the most recent
   *  manual refresh. Never persisted into niuniu's issue_comments table; not
   *  editable, not part of the writeback path. Optional + array — undefined
   *  when the issue is not externally linked, [] when externally linked but
   *  never refreshed. */
  external_comments_snapshot?: import('./integration').ExternalComment[]
  external_comments_snapshot_at?: string | null
  /** Autohost LLM judge — per-issue completion criterion. Empty string when
   *  unset; falls back to workspace-level NIUNIU_AUTOHOST_GOAL_CONDITION when
   *  the workspace runs autohost. */
  goal_condition?: string
  /** Executable-Epic hierarchy & execution fields (E1/E2). `parent_issue_id`
   *  links a child task to its parent Epic (null = top-level / not a child).
   *  `issue_type` distinguishes a regular task from an Epic that orchestrates
   *  children. `exec_wave` groups an Epic's children into ordered execution
   *  waves (0-based). `exec_status` is the Epic's wave-execution state. */
  parent_issue_id?: number | null
  issue_type?: IssueType
  exec_wave?: number
  exec_status?: ExecStatus
  /** Human-readable reason behind a terminal exec_status (blocked-needs-human /
   *  abandoned-with-reason, spec section 19). Null/absent when not terminal. */
  exec_status_reason?: string | null
}

export type IssueType = 'task' | 'epic'
// Execution lifecycle, incl. the stage-7 floor-gate + terminal states (spec
// section 19): gate_checking/gate_blocked (blocked-needs-human), waiting_input
// (waiting-user-input), abandoned (abandoned-with-reason).
export type ExecStatus =
  | 'idle' | 'running' | 'done' | 'failed' | 'paused' | 'reviewing'
  | 'gate_checking' | 'gate_blocked' | 'waiting_input' | 'abandoned'

/** Returned by GET /issues/:id/epic-progress (also embedded in exec-status responses). */
export interface EpicProgress {
  done: number
  total: number
  exec_status: ExecStatus
}

/** One row of the per-issue execution timeline (spec section 23.7). */
export interface ExecTimelineEntry {
  id: number
  kind: 'advance' | 'gate' | 'ask_user' | 'terminal' | 'intervention' | 'cost'
  summary: string
  detail_json?: string
  cost_usd: number
  created_at: string
}

/** Returned by GET /issues/:id/exec-timeline. */
export interface ExecTimelineResponse {
  entries: ExecTimelineEntry[]
  total_cost: number
}

/** One repo's snapshot within a checkpoint step (autohost 安全网 hidden-ref). */
export interface CheckpointRepo {
  id: number
  repository_id: number
  repo_name: string
  worktree_path: string
  git_ref: string
  commit_hash: string
  parent_hash: string
}

/** One point on an issue's checkpoint timeline (a step across one or more repos). */
export interface CheckpointStep {
  step: number
  kind: 'advance' | 'gate_pass' | 'autohost_final' | 'manual'
  gate_status: string
  label: string
  created_at: string
  repos: CheckpointRepo[]
}

/** Returned by GET /issues/:id/checkpoints. */
export interface CheckpointListResponse {
  issue_id: number
  checkpoints: CheckpointStep[]
}

/** Per-repo outcome of a checkpoint revert. */
export interface CheckpointRevertRepo {
  repository_id: number
  repo_name: string
  worktree_path: string
  commit_hash: string
  ok: boolean
  error?: string
}

/** Returned by POST /issues/:id/checkpoints/revert. */
export interface CheckpointRevertResponse {
  issue_id: number
  step: number
  repos: CheckpointRevertRepo[]
}

// Server-side setting (key/value pair) — admin-managed via /api/admin/settings.
export interface ServerSetting {
  key: string;
  value: string;
}

export interface AvailableIssue {
  id: number;
  title: string;
  project_id: number;
  project_name: string;
  lifecycle_status: string;
}

// Login history — returned by GET /api/auth/login-history
export type LoginAttemptReason =
  | 'ok'
  | 'bad_password'
  | 'account_locked'
  | 'ip_locked'
  | 'mfa_failed'
  | string;

export interface LoginAttempt {
  id: number;
  ip: string;
  user_agent: string;
  success: boolean;
  reason: LoginAttemptReason;
  created_at: string;
}

export interface CreateColumnData {
  name: string;
  lifecycle_mapping?: string;
}

export interface CreateIssueData {
  title: string;
  description?: string;
  priority?: number;
  labels?: string;
  assignee?: string;
  start_date?: string;
  due_date?: string;
  estimate_type?: string;
  estimate?: number;
}

// Repository types
export interface Repository {
  id: number;
  name: string;
  path: string;
  git_remote: string | null;
  default_branch: string | null;
  total_branches: number;
  created_at: string;
  updated_at: string;
  owner?: import('./org').OwnerRef;
}

export interface CreateRepositoryData {
  name: string;
  path: string;
  default_branch?: string;
}

export interface IssueDefaultRepo {
  repository: Repository;
  branches: string[];
  preferred_branch: string;
}

// Worktree sidebar info
export interface WorktreeSidebarInfo {
  name: string;
  repo_name: string;
  branch: string;
  base_branch: string;
  changes_count: number;
  ahead_count: number;
}

// Lazily-loaded git badges for the sidebar. The workspace list (GET /workspaces)
// returns instantly without git; the SPA fetches these from
// GET /workspaces/sidebar-git and merges them into the list by workspace_id.
export interface WorktreeGitStatus {
  name: string;
  changes_count: number;
  ahead_count: number;
}

export interface WorkspaceGitStatus {
  workspace_id: string;
  changes_count: number;
  ahead_count: number;
  worktrees?: WorktreeGitStatus[];
}

// Background task types
export interface BgTaskHighlight {
  kind: 'bash' | 'subagent' | 'wakeup';
  title: string;
  started_at?: string;     // bash/subagent only
  scheduled_for?: string;  // wakeup only
}

export interface BgTaskAggregateDTO {
  agent_busy: boolean;
  bash_count: number;
  wakeup_count: number;
  subagent_count: number;
  cron_count: number;
  highlight?: BgTaskHighlight;
}

// Workspace types
export interface Workspace {
  id: string;
  issue_id: string | null;
  name: string;
  path: string;
  status: string;
  agent_pid: number | null;
  agent_status: string;
  changes_count: number;
  ahead_count: number;
  message_count?: number;
  last_message_at?: string | null;
  project_name?: string;
  lifecycle_status?: string;
  /** Linked issue's type. Only present (and only 'epic') when the workspace's
   *  issue is an Epic; omitted for regular tasks. Drives the sidebar Epic
   *  marker. See IssueType. */
  issue_type?: IssueType;
  /** Linked issue's parent Epic id. Present only when the linked issue is a
   *  sub-issue (has a parent); omitted for top-level issues. Drives the
   *  sidebar sub-issue marker. */
  parent_issue_id?: number | null;
  owner?: import('./org').OwnerRef;
  project_owner_type?: 'user' | 'org';
  project_owner_id?: number;
  project_owner_name?: string;
  task_stats?: {
    total: number;
    completed: number;
    current_task?: string;
  };
  worktrees?: WorktreeSidebarInfo[];
  schedule_count?: number;
  bg_tasks?: BgTaskAggregateDTO;
  harness_id?: number | null;
  created_by?: number | null;
  creator_owner?: import('./org').OwnerRef | null;
  claude_account_id?: number | null;
  /**
   * Which agent CLI runs in this workspace. Set at create time and immutable
   * afterwards. Defaults to 'claude' for legacy rows. Codex workspaces use a
   * different on-disk config (.codex/config.toml) and skip the Claude-specific
   * cost / account UI elements.
   */
  cli_type: 'claude' | 'codex' | 'qwen' | 'omp' | 'goose';
  /** Directly-bound subscription-platform provider (issue #653). null = none. */
  env_provider_id?: number | null;
  /** Codex managed account binding (M2.5). null = use global ~/.codex/. */
  codex_account_id?: number | null;
  /** Codex sandbox mode (M2.5). Defaults to 'danger-full-access'. */
  codex_sandbox_mode?: CodexSandboxMode;
  /** Codex approval policy (M2.5). Defaults to 'never'. */
  codex_approval_policy?: CodexApprovalPolicy;
  created_at: string;
  updated_at: string;
  is_archived?: number;
  /**
   * 1 when this workspace was created via the studio "from local directory"
   * flow (#232). The IDE shows the delivery hint (#238) when set.
   */
  is_studio?: number;
  archived_at?: string | null;
}

export type CodexSandboxMode = 'read-only' | 'workspace-write' | 'danger-full-access'
export type CodexApprovalPolicy = 'untrusted' | 'on-failure' | 'on-request' | 'never'

export interface ArchivedWorkspace {
  id: string;
  name: string;
  issue_id: string | null;
  issue_title: string | null;
  project_name: string | null;
  status: string;
  worktrees: { repo_name: string; branch: string; base_branch: string }[];
  archived_at: string | null;
  created_at: string;
}

export type WorkspaceStatus = 'created' | 'running' | 'needs_review' | 'attention' | 'completed' | 'deleting';

// Cross-workspace overview (GET /api/workspaces/overview).
// Backend computes cost roll-ups, message counts, and a "stuck" heuristic
// across every workspace the caller can access. Owner filter (?owner=) narrows
// further. See server/internal/service/workspace_overview.go.
export interface WorkspaceOverviewSummary {
  total_count: number;
  active_count: number;
  stuck_count: number;
  // Token usage by type (we record tokens, not money).
  user_message_count: number;
  ai_message_count: number;
  input_tokens: number;
  output_tokens: number;
  cache_creation_tokens: number;
  cache_read_tokens: number;
}

export interface TokenBucket {
  hour: string;
  input_tokens: number;
  output_tokens: number;
  cache_creation_tokens: number;
  cache_read_tokens: number;
  interaction_count: number;
}

export interface TokenUsageSeries {
  buckets: TokenBucket[];
}

export interface WorkspaceOverviewItem {
  workspace_id: number;
  name: string;
  owner_type: 'user' | 'org';
  owner_id: number;
  owner: import('./org').OwnerRef;
  status: WorkspaceStatus;
  session_status: string;
  updated_at: string;
  last_activity_at?: string | null;
  message_count: number;
  user_message_count: number;
  ai_message_count: number;
  input_tokens: number;
  output_tokens: number;
  cache_creation_tokens: number;
  cache_read_tokens: number;
  is_stuck: boolean;
  is_archived: boolean;
  created_by?: number | null;
  creator_owner?: import('./org').OwnerRef | null;
  project_name?: string;
  changes_count: number;
  ahead_count: number;
}

export interface WorkspaceOverview {
  summary: WorkspaceOverviewSummary;
  workspaces: WorkspaceOverviewItem[];
}

/** Reason a workspace was skipped by POST /workspaces/batch-delete. */
export type BatchDeleteSkipReason =
  | 'forbidden'
  | 'not_found'
  | 'has_changes'
  | 'already_deleting'
  | 'error';

export interface BatchDeleteSkippedItem {
  id: number;
  reason: BatchDeleteSkipReason;
}

/** Response of POST /workspaces/batch-delete. `accepted` ids are marked
 *  'deleting' and cleaned up asynchronously; `skipped` carries per-id reasons. */
export interface BatchDeleteWorkspacesResult {
  accepted: number[];
  skipped: BatchDeleteSkippedItem[];
}

export interface CommitRequest {
  message: string;
}

export interface MergeRequest {
  target_branch: string;
}

export interface WorkspaceRepository extends BaseEntity {
  workspace_id: string;
  repository_id: string;
  worktree_path: string;
  branch: string;
}

export interface RepoBranch {
  repo_id: number;
  branch: string;
}

export interface CreateWorkspaceRequest {
  issue_id?: number | null;
  name: string;
  repos: RepoBranch[];
  owner?: import('./org').OwnerRef;
  claude_account_id?: number | null;
  /** M2.5 codex multi-account binding. Only honored when cli_type='codex'. */
  codex_account_id?: number | null;
  mcp_servers?: string[];
  /** Scene IDs to attach on creation (after the base layer). */
  initial_scenes?: number[];
  /**
   * Optional CLI selector. Omit / empty string defaults to 'claude' in the
   * SQL layer. Pass 'codex' to create a Codex workspace.
   */
  cli_type?: 'claude' | 'codex' | 'qwen' | 'omp' | 'goose';
  /**
   * Create a plain owner-isolated directory with no git worktrees (office /
   * non-code tasks). When true, `repos` must be empty; when false, at least
   * one repo is required. See docs/architecture/workspace-model.md.
   */
  no_repo?: boolean;
}

// Per-workspace MCP configuration types.
// Backend handlers in server/internal/api/workspace_mcp.go return these shapes
// case-sensitively; do not rename fields without updating the Go bindings.
export interface KnownMCP {
  name: string;
  command: string;
  args: string[];
  env?: Record<string, string>;
  source: 'global' | 'plugin';
  plugin_name?: string;
}

export interface PluginConflictInfo {
  mcp_name: string;
  plugin_name: string;
  message_key: string;
}

export interface MCPDetectResult {
  recommended: string[];
  all: KnownMCP[];
  plugin_conflicts: PluginConflictInfo[];
}

export interface WorkspaceMCPState {
  servers: string[];
  unavailable: string[];
  available: KnownMCP[];
  plugin_conflicts: PluginConflictInfo[];
  strict: boolean;
}

export interface CreateWorkspaceResponse {
  id: string;
  issue_id: string | null;
  name: string;
  path: string;
  status: string;
  repositories: {
    id: string;
    repository_id: string;
    worktree_path: string;
    branch: string;
  }[];
  errors: {
    repository_id: string;
    error: string;
  }[];
  /** Non-fatal notices (e.g. "WARN_GIT_LFS_MISSING"). Present on from-directory. */
  warnings?: string[];
  /** Auto-created/bound repo id (studio from-directory flow). */
  repository_id?: string;
  /** True for studio workspaces created from a local directory (#232). */
  is_studio?: boolean;
}

export interface CreateWorkspaceFromDirectoryRequest {
  /** Local directory on the server host. Auto-init'd if not a git repo. */
  dir: string;
  owner?: import('./org').OwnerRef;
  name?: string;
  cli_type?: 'claude' | 'codex' | 'qwen' | 'omp' | 'goose';
  /** Optional workflow (project_templates) pre-selection. */
  workflow_template_id?: number | null;
}

// Config types
export interface AppConfig {
  server: {
    port: number;
    host: string;
  };
  storage: {
    driver: string;
    sqlite_path: string;
  };
  workspace: {
    base_dir: string;
  };
  agent: {
    command: string;
    args: string[];
  };
  /** Anonymous usage-telemetry opt-out flag (default true). Exposed by
   *  GET /api/config; toggle via PUT /api/config { telemetry_enabled }. */
  telemetry_enabled?: boolean;
}

// File tree types
export interface TreeNode {
  name: string;
  path: string;
  is_dir: boolean;
  children?: TreeNode[];
}

// Workspace repository types
export interface WorkspaceRepoDetail {
  id: string;
  workspace_id: string;
  repository_id: string;
  worktree_path: string;
  branch: string;
  created_at: string;
  repository?: Repository;
}

export interface AddWorkspaceRepositoryData {
  repository_id: string;
  branch?: string;
}

export interface AddRepositoryRequest {
  repository_id: string;
  branch: string;
}

// Git types
export interface GitStatus {
  modified: string[];
  added: string[];
  deleted: string[];
  untracked: string[];
  // Set when the worktree directory exists but `git status` failed (e.g. the
  // parent repo's gitlink target was removed). Backend returns 200 with
  // empty change lists; the UI should render a "broken" hint instead of
  // displaying it as a clean worktree.
  broken?: boolean;
  reason?: string;
}

export interface GitLogEntry {
  hash: string;
  message: string;
  author: string;
  date: string;
}

export interface FileChange {
  path: string;
  status: string;
}

export interface CommitDetail {
  hash: string;
  short_hash: string;
  author: string;
  author_email: string;
  date: string;
  message: string;
  files_changed: FileChange[];
}

// Worktree types
export interface WorktreeGroup {
  name: string;
  path: string;
}

// Directory types
export interface DirectoryEntry {
  name: string;
  path: string;
}

export interface DirectoryListResponse {
  path: string;
  parent: string | null;
  directories: DirectoryEntry[];
}

// Repository types (updated)
export interface CreateRepositoryData {
  name: string;
  path: string;
  default_branch?: string;
  auto_init?: boolean;
}

// Repository Detail types
export interface FileEntry {
  name: string;
  path: string;
  type: 'file' | 'dir';
  size: number;
  mode: string;
}

export interface RepositoryStats {
  total_commits: number;
  total_branches: number;
  total_contributors: number;
  last_commit_date: string;
}

export interface CommitEntry {
  hash: string;
  author: string;
  date: string;
  message: string;
}

export interface GraphCommit {
  hash: string;
  short_hash: string;
  author: string;
  date: string;
  message: string;
  parents: string[];
  refs?: string[];
  is_current?: boolean;
}

export interface BranchTree {
  current_branch: string;
  local_branches: string[];
  remote_branches: string[];
}

export interface CommitDetail {
  hash: string;
  short_hash: string;
  author: string;
  author_email: string;
  date: string;
  message: string;
  files_changed: { path: string; status: string }[];
}

export interface WorktreeWithWorkspace {
  id?: number;
  path: string;
  branch: string;
  is_current: boolean;
  has_changes: boolean;
  workspace_id?: string;
  workspace_path?: string;
}

export interface BranchInfo {
  name: string;
  is_current: boolean;
}

// Worktree types
export interface Worktree {
  id: number;
  workspace_id: number;
  repository_id: number;
  worktree_path: string;
  branch: string;
}

export interface CreateWorktreeInput {
  workspace_id: number;
  repository_id: number;
  branch: string;
}

// Repository detail
export interface RepositoryDetail {
  id: number;
  name: string;
  path: string;
  default_branch: string;
  git: {
    current_branch: string;
    branches: string[];
    has_changes: boolean;
  };
  files: {
    tree: TreeNode;
  };
}

// Tree types
export interface TreeItem {
  name: string;
  path: string;
  type: 'dir' | 'file';
  size: number;
}

export interface WorkspaceTreeResponse {
  data: {
    path: string;
    items: TreeItem[];
  };
}

// Agent SSE event types (mirrors backend OutputEvent)
export type AgentEventType =
  | 'text'        // streaming text content
  | 'tool_use'    // tool invocation start
  | 'tool_result' // tool execution output
  | 'thinking'    // model thinking
  | 'system_info' // system messages (retry, rate limit)
  | 'done'        // execution complete
  | 'error'       // error
  | 'idle'         // session idle, waiting for input
  | 'ping'         // SSE keepalive
  | 'task_update'   // workspace task update
  | 'queue_update'      // workspace queue changed
  | 'claude_status_changed' // rate-limit/usage status changed — refetch /claude-status
  | 'schedule_trigger'  // scheduled task triggered
  | 'harness_confirm'   // harness run waiting for user confirmation
  | 'harness_status'   // harness run status update
  | 'team:workers_updated'   // team workers updated
  | 'permission_request'     // permission-prompt: a tool call needs user decision
  | 'permission_decided'     // permission-prompt: a request was allowed/denied/timed-out
  | 'ask_user_request'       // ask-user-question: a multiple-choice question awaits the user
  | 'ask_user_decided'       // ask-user-question: a request was answered/timed-out/cancelled
  | 'workspace_auth_error'  // per-workspace 403: drop this workspace, keep stream alive
  // Phase 2 harness run events (Task 14 / Phase 4 Task 4)
  | 'run_phase_started'    // harness run entered a new column
  | 'run_phase_skipped'    // column skipped in multi-step forward move
  | 'run_phase_aborted'    // column left in rollback
  | 'gate_started'         // async gate job enqueued
  | 'gate_progress'        // one gate spec finished
  | 'gate_done'            // gate job completed
  | 'agent_lifecycle';     // agent start/stop for a column

export interface AgentMessage {
  id?: string;
  workspaceId: string;
  type: AgentEventType;
  role: 'user' | 'assistant' | 'system';
  content: string;
  messageId: string;
  ts: number;
  toolName?: string;
  toolInput?: string;
  toolUseId?: string;
  isError?: boolean;
  costUsd?: number;
  numTurns?: number;
  durationMs?: number;
  inputTokens?: number;   // context input tokens (message_start)
  outputTokens?: number;  // output tokens (message_delta)
  rateLimit?: RateLimitData;
  cliType?: string;   // CLI identity e.g. "claude", "codex"
  attachments?: string; // JSON-serialized []ChatAttachment on user echo events (queue-dequeued / scheduler / harness sends)
  taskData?: import('./workspace-task').TaskUpdatePayload;
  // Phase 2 harness envelope fields (OutputEvent field names per backend camelCase spec)
  runPhase?: RunPhaseEventPayload;
  gateJob?: GateJobStartedPayload;
  gateProgress?: GateProgressPayload;
  gateDone?: GateDonePayload;
  agentLifecycle?: AgentLifecyclePayload;
}

// Rate limit data from Claude CLI rate_limit_event
export interface RateLimitData {
  status: string;          // "allowed", "allowed_warning", "rejected"
  rateLimitType: string;   // "five_hour"
  resetsAt: number;        // unix timestamp (seconds)
  overageStatus: string;
}

// Attachment metadata for chat messages
export interface ChatAttachment {
  path: string;         // relative to workspace root, e.g. ".worktrees/niuniu-main/src/app.tsx"
  type: 'upload' | 'ref';
  name: string;
  repo?: string;        // worktree/repo name for @ references
  mimeType?: string;
  size?: number;
  originalSize?: number;  // bytes before server-side optimization (Task 9 / spec §6.1)
  optimized?: boolean;    // true when backend image-optimization actually shrunk the file
}

// Persisted agent message from REST API (event timeline)
export interface AgentMessageRecord {
  id: string;
  workspaceId: number;
  role: string;
  content: string;
  messageId: string;
  eventType: string;
  toolName?: string;
  toolInput?: string;
  toolUseId?: string;
  isError?: boolean;
  costUsd?: number;
  numTurns?: number;
  durationMs?: number;
  inputTokens?: number;
  outputTokens?: number;
  createdAt: number;
  attachments?: string;
}

// Issue lifecycle (per-issue staged progress via /issues/:id/lifecycle)
export type LifecycleStatus = 'created' | 'spec' | 'spec-review' | 'plan' | 'plan-review' | 'implement' | 'implement-review' | 'test' | 'completed';

export interface UpdateLifecycleData {
  lifecycleStatus: LifecycleStatus;
}

// Issue checklist
export interface IssueChecklist extends BaseEntity {
  issue_id: number
  title: string
  is_completed: number
  position: number
}

export interface ChecklistStats {
  total: number
  completed: number
}

// Issue comment
export interface IssueComment extends BaseEntity {
  issue_id: number
  author: string
  content: string
}

// Workspace review comment (line-level diff comment).
// Anchored to repo + file_path + line_number; sent_to_agent distinguishes
// "queued" (pending) from "sent".
export interface WorkspaceComment {
  id: number
  workspace_id: number
  repo: string
  file_path: string
  line_number?: number | null
  content: string
  sent_to_agent?: boolean | null
  created_at: string
}

export interface CreateWorkspaceCommentInput {
  repo: string
  file_path: string
  line_number?: number | null
  content: string
}

// Issue timeline
export interface TimelineEntry {
  type: 'comment' | 'activity'
  id: number
  action?: string
  field?: string
  old_value?: string
  new_value?: string
  author: string
  content?: string
  created_at: string
  updated_at?: string
}

export interface QueueItem {
  id: number
  workspace_id: number
  content: string
  position: number
  source: 'user' | 'retry'
  created_at: string
  updated_at: string
}

export interface WorkspaceSchedule {
  id: number
  workspace_id: number
  name: string
  default_message: string
  schedule_type: 'cron' | 'once'
  action_kind: 'agent_message' | 'autonomous_discovery'
  cron_expr: string
  run_at: string | null
  enabled: boolean
  fired_at: string | null
  last_run_at: string | null
  created_at: string
  updated_at: string
}

export interface GlobalSchedule extends WorkspaceSchedule {
  next_run_at: string | null
  workspace_name: string
  workspace_status: string
}

export interface ScheduleRun {
  id: number
  schedule_id: number
  workspace_id: number
  status: 'triggered' | 'skipped' | 'failed'
  message: string
  created_at: string
}

// Env presets
export interface EnvPreset {
  id: number
  name: string
  description: string
  env: Record<string, string>
  owner?: { type: string; id: number; name?: string; slug?: string }
  created_at: string
  updated_at: string
}

export interface CreateEnvPresetData {
  name: string
  description?: string
  env?: Record<string, string>
  owner?: { type: string; id: number }
}

export interface EnvAccount {
  id: number
  name: string
  platform: string
  description: string
  api_key: string
  owner?: { type: string; id: number; name?: string; slug?: string }
  created_at: string
  updated_at: string
}

export interface CreateEnvAccountData {
  name: string
  platform?: string
  description?: string
  api_key?: string
  owner?: { type: string; id: number }
}

export interface EnvProvider {
  id: number
  name: string
  platform: string
  description: string
  base_url: string
  protocol: string
  api_key: string
  model: string
  haiku_model: string
  sonnet_model: string
  opus_model: string
  subagent_model: string
  extra_env: Record<string, string>
  owner?: { type: string; id: number; name?: string; slug?: string }
  created_at: string
  updated_at: string
}

export interface CreateEnvProviderData {
  name: string
  platform?: string
  description?: string
  base_url?: string
  protocol?: string
  api_key?: string
  model?: string
  haiku_model?: string
  sonnet_model?: string
  opus_model?: string
  subagent_model?: string
  extra_env?: Record<string, string>
  owner?: { type: string; id: number }
}

// Workspace delete change check
export interface WorktreeChangeStatus {
  worktree_path: string;
  repo_name: string;
  branch: string;
  base_branch: string;
  unstaged_files: GitStatusFile[];
  staged_files: GitStatusFile[];
  ahead_count: number;
  ahead_commits: GitLogEntry[];
  ahead_of_base_count: number;
  ahead_of_base_commits: GitLogEntry[];
  has_merge_conflict: boolean;
  unmerged_files: GitStatusFile[];
}

interface GitStatusFile {
  path: string;
  status: string;
}

// Task Analysis
export interface StartAnalysisResponse {
  workspace_id: number
}

export interface BatchCreateIssueInput {
  title: string
  description: string
  priority: number
  labels: string[]
  estimate_type: string
  estimate: number
  checklist: string[]
}

export interface BatchCreateIssuesRequest {
  issues: BatchCreateIssueInput[]
}

export interface BatchCreateResult {
  created: Array<{ issue_id: number; title: string }>
}

// Project learnings
export interface Learning {
  id: number
  project_id: number
  category: 'pattern' | 'gotcha' | 'decision' | 'error_fix'
  title: string
  content: string
  source: 'manual' | 'mcp' | 'extract'
  workspace_id: number | null
  workspace_name: string | null
  created_at: string
  updated_at: string
}

export interface ExtractLearningsResult {
  extracted: number
  learnings: Learning[]
}

// In-memory progress of async workspace session extraction (polled by the
// toolbar button to show a spinner while running).
export interface ExtractStatus {
  running: boolean
  extracted: number
  error?: string
}

export type MemoryType = 'pattern' | 'gotcha' | 'decision' | 'error_fix' | 'note' | 'reference'
export type MemorySource = 'manual' | 'mcp' | 'extract' | 'generate' | 'consolidate'

export interface Memory {
  id: number
  owner_type: 'user' | 'org'
  owner_id: number
  project_id: number | null
  workspace_id: number | null
  mem_type: MemoryType
  title: string
  content: string
  source: MemorySource
  source_path: string
  version: number
  pending_review: boolean
  stale_reason: string
  deleted_at: string | null
  created_at: string
  updated_at: string
}

// MemorySweepRun is one execution record of the automatic staleness sweep
// (auto-detect + clear memories that no longer match the project's latest code).
export type MemorySweepTrigger = 'schedule' | 'session' | 'manual'

export interface MemorySweepRun {
  id: number
  project_id: number
  trigger: MemorySweepTrigger
  scanned: number
  auto_deleted: number
  queued: number
  detail: string
  created_at: string
}

// CleanupStatus is a workspace-cleanup target category: an issue that reached a
// terminal done state ('completed') or that was never started ('not_started').
export type CleanupStatus = 'completed' | 'not_started'

// CleanupPolicy is a project's workspace auto-cleanup configuration. OFF by
// default (enabled = false). inactive_days is the N in "no activity for N days".
export interface CleanupPolicy {
  enabled: boolean
  inactive_days: number
  statuses: CleanupStatus[]
}

// CleanupResult summarizes one project cleanup sweep.
export interface CleanupResult {
  project_id: number
  scanned: number
  deleted: number[]
  skipped_changes: number
  errors: number
}

export interface MemoryVersion {
  id: number
  memory_id: number
  version: number
  mem_type: MemoryType
  title: string
  content: string
  source: MemorySource
  source_path: string
  created_at: string
}

export interface MemoryDetail extends Memory {
  versions: MemoryVersion[]
}

export interface GitIdentity {
  name: string
  email: string
  configured: boolean
}

export interface ToolExtras {
  git_identity?: GitIdentity
}

export interface ToolStatus {
  name: 'node' | 'python3' | 'git' | 'claude' | 'codex';
  found: boolean;
  version: string;
  path: string;
  /** Per-tool installability. claude is installable when npm is on PATH (it
   *  ships with node), independent of the OS package manager; node/python3/git
   *  require winget/brew/apt-get. Always false in team edition. Use this to
   *  gate per-tool install buttons; can_install is the page-level signal for
   *  the top-of-page message. */
  installable: boolean;
  extras?: ToolExtras;
}

export interface SystemDepsInfo {
  platform: 'windows' | 'darwin' | 'linux' | string;
  package_manager: 'winget' | 'brew' | 'apt-get' | '';
  can_install: boolean;
  /** True when niuniu-server runs in personal/embedded mode. Gates host-shell
   *  ops (claude-login terminal launch). False on team-edition deployments. */
  personal_mode: boolean;
  tools: ToolStatus[];
}

export interface InstallStartResponse {
  job_id: string;
  fallback_url?: string;
}

export interface AuthUser {
  id: number
  username: string
  display_name: string
  role: 'admin' | 'member'
  created_at: string
}

export interface CreateUserRequest {
  username: string
  password: string
  display_name: string
  role: 'admin' | 'member'
}

export interface UpdateUserRequest {
  display_name?: string
  role?: 'admin' | 'member'
}

// Admin-only view of a user's personal resources, backing the
// "manage resources" dialog (GET /api/auth/users/:id/resources).
export type UserResourceType = 'project' | 'workspace' | 'repository'

export interface UserResourceOrg {
  id: number
  slug: string
  name: string
  role: string
  is_last_owner: boolean
}

export interface UserResourceProject {
  id: number
  name: string
  created_at: string
}

export interface UserResourceWorkspace {
  id: number
  name: string
  status: string
  // null when the workspace has no linked issue/project (nullable DTO contract).
  project_id: number | null
}

export interface UserResourceRepository {
  id: number
  name: string
  path: string
}

export interface UserResourceCounts {
  env_presets: number
  quick_actions: number
  agents: number
  scenes: number
  data_sources: number
  saved_queries: number
  dashboards: number
  knowledge_bases: number
  harness_specs: number
}

export interface UserResourcesResponse {
  user: AuthUser
  orgs: UserResourceOrg[]
  projects: UserResourceProject[]
  workspaces: UserResourceWorkspace[]
  repositories: UserResourceRepository[]
  counts: UserResourceCounts
}

export interface PurgeUserResponse {
  deleted: {
    projects: number
    workspaces: number
    repositories: number
    env_presets: number
    quick_actions: number
    agents: number
    scenes: number
    data_sources: number
    saved_queries: number
    dashboards: number
    knowledge_bases: number
    harness_specs: number
    orgs_left: number
  }
}

export interface SearchUsersResponse {
  users: SelectedUser[];
  total: number;
}

export interface AddMemberRequest {
  user_id: number;
  role: string;
}

// ---------------------------------------------------------------------------
// Scene-based MCP/plugin management (M1).
// Spec: docs/superpowers/specs/2026-05-17-scene-based-mcp-plugin-management-design.md
// Plan: docs/superpowers/plans/2026-05-18-scene-based-mcp-plugin-management-m1.md
// ---------------------------------------------------------------------------

export type SceneSource = 'builtin' | 'user' | 'registry'

export interface SceneMCPDecl {
  name: string;
  /** Inline ".mcp.json" server entry (e.g. { command, args, env } or { type, url }).
   *  When set, it is written verbatim into the workspace .mcp.json on mount — no
   *  registry lookup. When omitted, `name` references a locally-installed MCP. */
  config?: Record<string, unknown>;
}

export interface ScenePluginDecl {
  source: string;
  ref?: string;
  optional?: boolean;
}

export interface ScenePrompt {
  id: string;
  title: string;
  body: string;
}

export interface SceneRequiredCredential {
  alias: string;
  provider: string;
  purpose?: string;
  optional?: boolean;
}

/** A niuniu data source (数据源) the scene needs, referenced by its per-owner
 *  name — same model as a project's external source, for the dataconn
 *  connectors reached via run_data_query. `kind` is a display/validation hint. */
export interface SceneDataSourceRef {
  name: string;
  kind?: string;
  purpose?: string;
  optional?: boolean;
}

export interface SceneMatchRule {
  signal: string;
  args?: Record<string, unknown>;
  weight: number;
}

export interface SceneMatch {
  base_weight?: number;
  rules?: SceneMatchRule[];
}

export interface SceneEnvPresetAsset {
  slug: string;
  name?: string;
  env: Record<string, string>;
}

export interface SceneQuickActionAsset {
  slug: string;
  label: string;
  prompt: string;
}

export interface SceneSlugPayloadAsset {
  slug: string;
  name?: string;
  payload: Record<string, unknown>;
}

/** A reference to an existing system agent (managed on the Agents page), by name. */
export interface SceneAgentRefAsset {
  name: string;
}

/** A reference to a subscription-platform provider (env_providers), by name. The
 *  provider expands to the workspace agent's env vars per its cli_type at spawn. */
export interface SceneProviderAsset {
  name: string;
}

export interface SceneAssets {
  env_presets?: SceneEnvPresetAsset[];
  providers?: SceneProviderAsset[];
  project_templates?: SceneSlugPayloadAsset[];
  quick_actions?: SceneQuickActionAsset[];
  harness_specs?: SceneSlugPayloadAsset[];
  agents?: SceneAgentRefAsset[];
}

export interface SceneSkillDecl {
  name: string;
  optional?: boolean;
}

export interface SceneDefinition {
  mcp: SceneMCPDecl[];
  plugins: ScenePluginDecl[];
  /** Vendored Claude skills the scene materializes into the workspace's
   *  .claude/skills/ (local file copy, no install CLI). */
  skills?: SceneSkillDecl[];
  assets: SceneAssets;
  prompts: ScenePrompt[];
  required_credentials: SceneRequiredCredential[];
  required_data_sources?: SceneDataSourceRef[];
  /** Built-in niuniu MCP tool groups to hide from the agent in this scene
   *  (e.g. 'multi-agent', 'harness'). Maps to niuniu-mcp --disable-tool-groups. */
  disable_tool_groups?: string[];
  match: SceneMatch;
}

export interface Scene {
  id: number;
  owner: import('./org').OwnerRef;
  slug: string;
  display_name: string;
  description: string;
  tags: string[];
  source: SceneSource;
  source_slug: string;
  definition: SceneDefinition;
  content_hash: string;
  enabled: boolean;
  created_at: string;
  updated_at: string;
}

export interface SceneLayer {
  id: number;
  workspace_id: number;
  scene_id: number | null;
  position: number;
  is_base: boolean;
}

export interface Projection {
  mcp: string[];
  plugins: ScenePluginDecl[];
  assets: SceneAssets;
  prompts: ScenePrompt[];
  required_credentials: SceneRequiredCredential[];
  /** field path → list of layer ids that contributed (UI provenance) */
  provenance: Record<string, number[]>;
}

export interface MissingCredential {
  alias: string;
  provider: string;
  purpose?: string;
  optional: boolean;
}

/**
 * Per-plugin install plan / outcome row. The column is historically named
 * install_failures but, after the 2026-05-20 "no auto-install" change, it
 * also carries pending and skipped entries:
 *
 *  - `pending`     declared by a scene layer, not installed yet — the user
 *                  must click Install to opt in (no implicit network/CLI).
 *  - `installed`   `claude plugin install` succeeded.
 *  - `skipped`     already present on disk.
 *  - `failed`      install attempted, exit code non-zero (stderr populated).
 *  - `unsupported` codex workspace + scene declares claude plugins; codex
 *                  CLI 0.x has no plugin subcommand. SPA shows an
 *                  explanatory banner without install CTAs (M2.5.1 D).
 *
 * Renamed type alias kept as InstallFailure for compatibility with
 * existing callers; consider `PluginInstallResult` going forward.
 */
export interface InstallFailure {
  source: string;
  ref?: string;
  status: 'installed' | 'skipped' | 'pending' | 'failed' | 'unsupported';
  stderr?: string;
}
export type PluginInstallResult = InstallFailure;

export interface ApplyResult {
  projection: Projection;
  missing_credentials: MissingCredential[];
  install_failures: InstallFailure[];
  restart_required: boolean;
  digest: string;
  /**
   * Plugin sources the user has dismissed for this workspace. Their pending/
   * failed rows are filtered out of install_failures; the banner surfaces them
   * in a collapsible "restore" affordance. May be absent on older payloads.
   */
  dismissed_plugins?: string[];
  /**
   * Launcher commands the projection's inline MCP servers need but which are
   * not on PATH (e.g. ["uvx"] for office-mail). The banner prompts the user to
   * install the runtime before the agent hits "uvx: not found". Absent/empty on
   * older payloads or when everything is installed.
   */
  missing_runtimes?: string[];
}

export interface RankedScene {
  scene: Scene;
  score: number;
  hits: { signal: string; weight: number }[];
}

export interface ProjectDefaultScene {
  scene_id: number;
  slug: string;
  display_name: string;
  source: SceneSource;
  position: number;
}

export interface CreateSceneRequest {
  slug: string;
  display_name: string;
  description?: string;
  tags?: string[];
  definition: SceneDefinition;
  owner?: import('./org').OwnerRef;
}

export interface UpdateSceneRequest {
  display_name?: string;
  description?: string;
  tags?: string[];
  definition?: SceneDefinition;
  enabled?: boolean;
  owner?: import('./org').OwnerRef;
}

// Batch issue mutation response shape — returned by /api/issues/batch/* and
// /mcp/issues/batch/*. `skipped[].reason` is one of: "forbidden" (authz
// denied), "not_found" (issue missing or deleted), "cross_project" (move
// only — issue belongs to a project other than the target column's).
export interface BatchSkippedItem {
  id: number;
  reason: 'forbidden' | 'not_found' | 'cross_project';
}
export interface BatchResult {
  succeeded: number[];
  skipped: BatchSkippedItem[];
}

export type LicenseState = 'active' | 'expiring' | 'expired' | 'clock_tampered' | 'unlicensed'

export interface LicenseStatus {
  state: LicenseState
  is_trial: boolean
  customer: string
  seats_used: number
  max_seats: number
  expires_at: number
  days_remaining: number
  read_only: boolean
  /** 当前许可证启用的门控功能（功能分级）。含 'org' 表示多租户组织可用。 */
  features_enabled?: string[]
}

/** Per-user privacy & disclaimer consent state (drives the consent gate). */
export interface ConsentStatus {
  current_version: string
  agreed_version: string
  needs_consent: boolean
}
