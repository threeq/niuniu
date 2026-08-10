// API types — aligned with backend Go types

export interface Project {
  id: number;
  name: string;
  description?: string;
  issue_stats?: ColumnIssueStat[];
  ws_stats?: WsStatusStat[];
  created_at: string;
  updated_at: string;
}

export interface ColumnIssueStat {
  column_name: string;
  count: number;
}

// Workspace count grouped by workspaces.status, e.g. { status: "running", count: 3 }.
// Mirrors server/internal/api/response.go WsStatusStat. Used by ProjectCard
// to surface attention / needs_review badges next to a project name.
export interface WsStatusStat {
  status: string;
  count: number;
}

export interface Column {
  id: number;
  project_id: number;
  name: string;
  position: number;
  lifecycle_mapping: string;
}

export interface Issue {
  id: number;
  column_id: number;
  title: string;
  description?: string;
  priority: number;
  labels?: string;
  position: number;
  lifecycle_status: string;
  assignee?: string;
  start_date?: string;
  due_date?: string;
  estimate_type?: string;
  estimate?: number;
  actual_time?: number;
  created_at: string;
  updated_at: string;
  checklist_done?: number;
  checklist_total?: number;
}

export interface IssueChecklist {
  id: number;
  issue_id: number;
  title: string;
  is_completed: boolean;
  position: number;
}

export interface IssueComment {
  id: number;
  issue_id: number;
  author: string;
  content: string;
  created_at: string;
  updated_at: string;
}

// Mirrors server/web/src/types/api.ts WorkspaceStatus — the canonical workflow
// state in the workspaces.status column. Distinct from agent_status which
// describes the underlying Claude CLI process (running/idle/completed/error).
// Server transitions: agent done → needs_review; agent error or
// needs_review timeout → attention; manual → completed.
export type WorkspaceStatus = 'created' | 'running' | 'needs_review' | 'attention' | 'completed';

export interface TaskStatsDTO {
  total: number;
  completed: number;
  current_task?: string;
}

export interface BgTaskHighlight {
  kind: 'bash' | 'subagent' | 'wakeup';
  title: string;
  started_at?: string;
  scheduled_for?: string;
}

export interface BgTaskAggregateDTO {
  agent_busy: boolean;
  bash_count: number;
  wakeup_count: number;
  subagent_count: number;
  cron_count: number;
  highlight?: BgTaskHighlight;
}

export interface WorktreeSidebarDTO {
  name: string;
  repo_name: string;
  branch: string;
  base_branch: string;
  changes_count: number;
  ahead_count: number;
}

export interface Workspace {
  id: string;  // Backend sends string, not number
  issue_id?: number;
  name: string;
  path: string;
  status: string;
  agent_status?: string;
  session_status?: string;
  loop_id?: number;
  created_at: string;
  updated_at: string;
  // Extended fields from list API
  issue_title?: string;
  project_name?: string;
  total_cost?: number;
  total_turns?: number;
  changes_count?: number;
  ahead_count?: number;
  schedule_count?: number;
  task_stats?: TaskStatsDTO;
  worktrees?: WorktreeSidebarDTO[];
  bg_tasks?: BgTaskAggregateDTO;
}

export interface AgentMessage {
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
  attachments?: string;
  createdAt: number;
}

export interface AgentMessagesResponse {
  messages: AgentMessage[];
  hasMore: boolean;
}

export interface ChatAttachment {
  path: string;       // server-relative, e.g. ".attachments/foo.png"
  name: string;
  type: 'upload';
  mimeType: string;
  size: number;
  localUri?: string;  // mobile-only: local file URI for thumbnail preview
}

export interface AttachmentUploadResponse {
  name: string;
  path: string;
  size: number;
  mimeType: string;
}

export interface WorkspaceTask {
  id: number;
  workspace_id: number;
  subject: string;
  description: string;
  status: string;
  phase: string;
  created_at: string;
}

export interface QueueItem {
  id: number;
  workspace_id: number;
  content: string;
  position: number;
  source: string;
  created_at: string;
}

export interface TokenPair {
  access_token: string;
  refresh_token: string;
  expires_in: number;
}

export interface HealthResponse {
  status: string;
  version: string;
  uptime_seconds: number;
  server_id: string;
  auth_enabled: boolean;
  api_version: number;
}

export interface ServerConfig {
  id: string;
  name: string;
  host: string;
  port: number;
  scheme: 'http' | 'https';
  isDefault: boolean;
  lastConnected?: string;
  authEnabled?: boolean;
}

export interface IssueActivity {
  id: number;
  issue_id: number;
  action: string;
  field: string;
  old_value: string;
  new_value: string;
  author: string;
  created_at: string;
}

export interface TimelineEntry {
  type: 'activity' | 'comment';
  activity?: IssueActivity;
  comment?: IssueComment;
  created_at: string;
}

export interface ExecutionPlan {
  id: number;
  project_id: number;
  name: string;
  status: string;
  created_at: string;
  updated_at: string;
  groups?: ExecutionPlanGroup[];
}

export interface ExecutionPlanGroup {
  id: number;
  plan_id: number;
  name: string;
  position: number;
  issues?: ExecutionPlanIssue[];
}

export interface ExecutionPlanIssue {
  plan_id: number;
  issue_id: number;
  group_id: number;
  display_order: number;
  issue?: Issue;
}

export interface Notification {
  id: string;
  type: string;
  title: string;
  body: string;
  workspace_id?: number;
  issue_id?: number;
  read: boolean;
  created_at: string;
}

// ─── Repository types ─────────────────────────────────────────────

export interface Repository {
  id: number;
  name: string;
  path: string;
  git_remote?: string;
  default_branch?: string;
  created_at: string;
  updated_at: string;
}

export interface RepositoryStats {
  total_commits: number;
  total_branches: number;
  total_contributors: number;
  last_commit_date: string;
}

export interface BranchInfo {
  branches: string[];
  current_branch: string;
}

// Mirrors server/internal/api/response.go ProjectRepositoryBinding — the row
// shape of GET /api/projects/:id/repositories.
export interface ProjectRepositoryBinding {
  repository_id: number;
  name: string;
  path: string;
  repo_default_branch: string;
  project_default_branch: string;
}

export interface CommitEntry {
  hash: string;
  author: string;
  date: string;
  message: string;
}

export interface WorktreeEntry {
  id?: number;
  path: string;
  branch: string;
  is_current: boolean;
  has_changes: boolean;
  workspace_id?: string;
  workspace_path?: string;
}

export interface TreeNode {
  name: string;
  path: string;
  is_dir: boolean;
  children?: TreeNode[];
}

export interface FileEntry {
  name: string;
  path: string;
  type: 'file' | 'dir';
  size: number;
  mode: string;
}

export interface GitStatusResponse {
  modified: string[];
  added: string[];
  deleted: string[];
  untracked: string[];
}

export type ChangeStatus = 'A' | 'M' | 'D' | '?';

export interface ChangeEntry {
  path: string;
  status: ChangeStatus;
}

export interface RepositoryGitInfo {
  current_branch: string;
  branches: string[];
  has_changes: boolean;
}

export interface RepositoryDetail {
  id: number;
  name: string;
  path: string;
  default_branch: string;
  git: RepositoryGitInfo;
  files: { tree: TreeNode };
}

export interface FileContent {
  path: string;
  content: string;
  size: number;
}
