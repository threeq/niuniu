import { toast } from 'sonner'
import i18n from '@/i18n'
import {
  getAccessToken,
  refreshAccessToken,
  useAuthStore,
  isAccessTokenNearExpiry,
} from '@/stores/auth-store'
import type {
  LoginAttempt,
  Repository,
  CreateWorkspaceRequest,
  CreateWorkspaceResponse,
  CreateWorkspaceFromDirectoryRequest,
  AddRepositoryRequest,
  Worktree,
  CreateWorktreeInput,
  RepositoryDetail,
  WorkspaceTreeResponse,
  GitStatus,
  WorktreeGroup,
  Issue,
  AvailableIssue,
  UpdateLifecycleData,
  IssueChecklist,
  IssueComment,
  WorkspaceComment,
  CreateWorkspaceCommentInput,
  TimelineEntry,
  EnvPreset,
  CreateEnvPresetData,
  GitLogEntry,
  CommitDetail,
  SystemDepsInfo,
  InstallStartResponse,
  AuthUser,
  CreateUserRequest,
  UpdateUserRequest,
  UserResourceType,
  UserResourcesResponse,
  PurgeUserResponse,
  SearchUsersResponse,
  AddMemberRequest,
  IssueDefaultRepo,
  Label,
  AssignableUser,
  WorkspaceOverview,
  BatchDeleteWorkspacesResult,
  ServerSetting,
  KnownMCP,
  MCPDetectResult,
  WorkspaceMCPState,
  EpicProgress,
  ExecTimelineResponse,
  CheckpointListResponse,
  CheckpointRevertResponse,
  Scene,
  SceneLayer,
  ApplyResult,
  PluginInstallResult,
  RankedScene,
  ProjectDefaultScene,
  CreateSceneRequest,
  UpdateSceneRequest,
} from '../types/api'
import type { Org, OrgMember, OrgAuditEntry, User, OwnerRef } from '../types/org'
import type {
  PermissionRequest,
  PermissionAllowlistEntry,
  DecideBody,
} from '../types/permission'
import type {
  AskUserRequest,
  AskUserDecideBody,
} from '../types/ask-user'
import type { AccountUsage } from '@/types/claude-usage'
// Type-only (erased at runtime, so no cycle with use-file-diff -> api): reuse the
// git.FileDiff shape for the checkpoint diff response.
import type { GitFileDiff } from './hooks/use-file-diff'

const API_BASE = '/api'

export class ApiError extends Error {
  status: number;
  body: unknown;
  /** Seconds until the client may retry (present on 423 / 429 responses). */
  retry_after?: number;
  /** Backend error code (e.g. "ACCOUNT_LOCKED", "IP_LOCKED"). */
  code?: string;

  constructor(status: number, message: string, body: unknown) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.body = body;
    // Extract error metadata from the backend envelope
    const errorBody = body as { error?: { code?: string; retry_after?: number } } | null;
    if (errorBody?.error) {
      if (errorBody.error.code !== undefined) {
        this.code = errorBody.error.code;
      }
      if (typeof errorBody.error.retry_after === 'number') {
        this.retry_after = errorBody.error.retry_after;
      }
    }
  }
}

export async function apiFetch<T>(
  endpoint: string,
  options?: RequestInit & {
    params?: Record<string, string | number | boolean | undefined>;
    suppressError?: boolean;
  }
): Promise<T> {
  let url = `${API_BASE}${endpoint}`

  // Add query params if provided
  if (options?.params) {
    const searchParams = new URLSearchParams()
    for (const [key, value] of Object.entries(options.params)) {
      if (value !== undefined) {
        searchParams.append(key, String(value))
      }
    }
    const queryString = searchParams.toString()
    if (queryString) {
      url += `?${queryString}`
    }
  }

  try {
    // Proactively refresh tokens that are within 30s of `exp` so we don't
    // emit a doomed request that comes back 401, gets transparently retried
    // here, but still leaves a red error in the browser console (Chrome
    // logs every 4xx response regardless of JS-side recovery).
    if (isAccessTokenNearExpiry()) {
      await refreshAccessToken();
    }

    const token = getAccessToken();
    const authHeaders: Record<string, string> = {};
    if (token) {
      authHeaders['Authorization'] = `Bearer ${token}`;
    }

    let response = await fetch(url, {
      ...options,
      // headers must come AFTER ...options: options carries its own `headers`
      // key, so spreading it last would clobber the merged auth headers and
      // strip the Authorization bearer (any caller passing custom headers would
      // then 401). Compute the merged set last so it always wins.
      headers: {
        'Content-Type': 'application/json',
        // Forward the user's UI language so server-side flows (e.g. workspace
        // CLAUDE.md generation) can default the agent to the user's language.
        'X-Niuniu-Language': i18n.language,
        ...authHeaders,
        ...options?.headers,
      },
    })

    // On 401, try refreshing the token once
    if (response.status === 401 && token) {
      const newToken = await refreshAccessToken();
      if (newToken) {
        response = await fetch(url, {
          ...options,
          // headers last — see the note on the initial fetch above.
          headers: {
            'Content-Type': 'application/json',
            'X-Niuniu-Language': i18n.language,
            'Authorization': `Bearer ${newToken}`,
            ...options?.headers,
          },
        });
      }
    }

    // If still 401 after refresh AND we had a token to begin with, force
    // logout. When the request was sent unauthenticated (no token) we just
    // throw — the caller (e.g. a route guard) decides what to do. Without
    // this guard, an unauthenticated /api/auth/me on the login page would
    // redirect to /login, which reloads the SPA and fires the same call,
    // looping until the user manually clears localStorage.
    if (response.status === 401) {
      if (token) {
        useAuthStore.getState().logout();
        window.location.href = '/login';
      }
      throw new ApiError(401, 'Session expired', {});
    }

    if (!response.ok) {
      const errorData = await response.json().catch(() => ({
        message: `HTTP ${response.status}: ${response.statusText}`,
      }))
      const errorMessage = errorData?.error?.message || errorData?.message || `HTTP ${response.status}`

      // License gate: when the server reports the deployment license is
      // expired or seat-full, refresh the license store so the banner/UX
      // updates immediately. Lazy import avoids a circular dependency
      // (license-store imports apiFetch from this module).
      const licenseCode = (errorData as { error?: { code?: string } })?.error?.code
      if (licenseCode === 'LICENSE_EXPIRED' || licenseCode === 'LICENSE_SEAT_EXCEEDED') {
        void import('@/stores/license-store').then((m) => m.useLicenseStore.getState().fetch())
      }

      // 404 is suppressed: it typically indicates a benign race where a
      // stale subscriber (e.g. ['issues', deletedColumnId] or
      // ['issue-checklists', deletedIssueId]) refetches just after a
      // successful delete and hits a now-gone resource. The thrown
      // ApiError still propagates so callers that care can branch on
      // status; route-level "not found" UIs already handle their own
      // missing-resource state without this global toast.
      if (!options?.suppressError && response.status !== 404) {
        toast.error('API Error', {
          description: errorMessage,
        })
      }

      throw new ApiError(response.status, errorMessage, errorData)
    }

    // Handle empty responses (204 No Content, etc.)
    const text = await response.text()
    if (!text) {
      return {} as T
    }
    return JSON.parse(text) as T
  } catch (error) {
    if (error instanceof TypeError && error.message.includes('Failed to fetch')) {
      const errorMessage = i18n.t('common:errors.networkError')

      // Show toast notification for network errors
      toast.error('Network Error', {
        description: errorMessage,
      })

      throw new Error(errorMessage)
    }

    // Re-throw other errors
    throw error
  }
}

// Helper functions for common HTTP methods
export const api = {
  get: <T>(
    endpoint: string,
    options?: {
      params?: Record<string, string | number | boolean | undefined>;
      suppressError?: boolean;
    }
  ) => apiFetch<T>(endpoint, { method: 'GET', ...options }),
  post: <T>(endpoint: string, data?: unknown) =>
    apiFetch<T>(endpoint, {
      method: 'POST',
      body: data ? JSON.stringify(data) : undefined,
    }),
  put: <T>(endpoint: string, data?: unknown) =>
    apiFetch<T>(endpoint, {
      method: 'PUT',
      body: data ? JSON.stringify(data) : undefined,
    }),
  delete: <T>(endpoint: string) =>
    apiFetch<T>(endpoint, { method: 'DELETE' }),
  patch: <T>(endpoint: string, data?: unknown) =>
    apiFetch<T>(endpoint, {
      method: 'PATCH',
      body: data ? JSON.stringify(data) : undefined,
    }),

  // Auth / current user
  getMe: (): Promise<User> =>
    api.get<User>('/auth/me'),

  // Login history — requires auth; returns the calling user's login attempts
  getLoginHistory: (): Promise<{ items: LoginAttempt[] }> =>
    api.get<{ items: LoginAttempt[] }>('/auth/login-history'),

  // MFA (Multi-Factor Authentication) — TOTP-based two-step verification
  setupMFA: (): Promise<{ provisioning_uri: string; qr_data_uri: string; secret: string }> =>
    api.post<{ provisioning_uri: string; qr_data_uri: string; secret: string }>('/auth/mfa/setup'),

  enableMFA: (code: string): Promise<{ backup_codes: string[] }> =>
    api.post<{ backup_codes: string[] }>('/auth/mfa/enable', { code }),

  verifyMFA: (mfaToken: string, code: string, trustDevice?: boolean): Promise<{ access_token: string; refresh_token: string; expires_in: number }> =>
    api.post<{ access_token: string; refresh_token: string; expires_in: number }>('/auth/mfa/verify', {
      mfa_token: mfaToken,
      code,
      trust_device: trustDevice ?? false,
    }),

  getMFAStatus: (): Promise<{ enabled: boolean; backup_codes_remain: number; trusted_device_count: number }> =>
    api.get<{ enabled: boolean; backup_codes_remain: number; trusted_device_count: number }>('/auth/mfa/status'),

  disableMFA: (code: string): Promise<{ message: string }> =>
    api.post<{ message: string }>('/auth/mfa/disable', { code }),

  regenerateBackupCodes: (code: string): Promise<{ backup_codes: string[] }> =>
    api.post<{ backup_codes: string[] }>('/auth/mfa/backup-codes/regenerate', { code }),

  // Orgs — /api/me/orgs returns user's orgs with their role
  listMyOrgs: (): Promise<Org[]> =>
    api.get<Org[]>('/me/orgs'),

  // Orgs — /api/orgs returns orgs for the calling user
  listOrgs: (): Promise<Org[]> =>
    api.get<Org[]>('/orgs'),

  // Orgs — /api/orgs/all returns every org (global admin only); role is the
  // caller's own membership role, empty string when not a member.
  listAllOrgs: (): Promise<Org[]> =>
    api.get<Org[]>('/orgs/all'),

  getOrg: (id: number): Promise<Org> =>
    api.get<Org>(`/orgs/${id}`),

  createOrg: (body: { name: string; slug?: string; description?: string }): Promise<Org> =>
    api.post<Org>('/orgs', body),

  updateOrg: (id: number, body: { name?: string; description?: string; slug?: string }): Promise<Org> =>
    api.patch<Org>(`/orgs/${id}`, body),

  deleteOrg: (id: number): Promise<void> =>
    api.delete(`/orgs/${id}`),

  listMembers: (orgID: number): Promise<OrgMember[]> =>
    api.get<OrgMember[]>(`/orgs/${orgID}/members`),

  searchUsers: (orgID: number, q: string): Promise<SearchUsersResponse> =>
    api.get<SearchUsersResponse>('/users/search', {
      params: { org_id: orgID, q },
    }),

  // suppressError so 409 ALREADY_MEMBER or other conflicts surface only via
  // the page's targeted toast — apiFetch's generic "API Error" toast would
  // otherwise stack on top.
  addMember: (orgID: number, body: AddMemberRequest): Promise<{ message: string }> =>
    apiFetch<{ message: string }>(`/orgs/${orgID}/members`, {
      method: 'POST',
      body: JSON.stringify(body),
      suppressError: true,
    }),

  // suppressError so 409 or other conflicts surface only via the page's
  // targeted toast — apiFetch's generic "API Error" toast would otherwise
  // stack on top.
  updateMemberRole: (orgID: number, userID: number, role: string): Promise<OrgMember> =>
    apiFetch<OrgMember>(`/orgs/${orgID}/members/${userID}`, {
      method: 'PATCH',
      body: JSON.stringify({ role }),
      suppressError: true,
    }),

  // suppressError so 409 or other conflicts surface only via the page's
  // targeted toast — apiFetch's generic "API Error" toast would otherwise
  // stack on top.
  removeMember: (orgID: number, userID: number): Promise<void> =>
    apiFetch(`/orgs/${orgID}/members/${userID}`, {
      method: 'DELETE',
      suppressError: true,
    }),

  transferOwnership: (orgID: number, targetUserID: number): Promise<void> =>
    api.post(`/orgs/${orgID}/transfer-ownership`, { target_user_id: targetUserID }),

  listAuditLog: (orgID: number, limit?: number, offset?: number): Promise<OrgAuditEntry[]> =>
    api.get<OrgAuditEntry[]>(`/orgs/${orgID}/audit-log`, {
      params: { limit, offset },
    }),

  // Workspace
  createWorkspace: (data: CreateWorkspaceRequest) =>
    api.post<CreateWorkspaceResponse>('/workspaces', data),

  createWorkspaceFromDirectory: (data: CreateWorkspaceFromDirectoryRequest) =>
    api.post<CreateWorkspaceResponse>('/workspaces/from-directory', data),

  addWorkspaceRepository: (workspaceId: string, data: AddRepositoryRequest) =>
    api.post(`/workspaces/${workspaceId}/repositories`, data),

  listAvailableIssuesForWorkspace: () =>
    api.get<AvailableIssue[]>('/workspaces/available-issues'),

  getWorkspaceIssueDefaults: (issueId: string | number) =>
    api.get<{ repos: IssueDefaultRepo[]; project_default_cli_type?: 'claude' | 'codex' | 'qwen' | 'omp' | 'goose' }>(
      `/workspaces/issue-defaults?issue_id=${encodeURIComponent(String(issueId))}`
    ),

  // Repositories
  listRepositories: (opts?: { owner?: { type: 'user' | 'org'; id?: number; slug?: string } }): Promise<Repository[]> => {
    if (!opts?.owner) return api.get<Repository[]>('/repositories');
    const o = opts.owner;
    const ownerStr = o.type === 'org'
      ? `org:${o.slug ?? o.id ?? ''}`
      : `user:${o.id ?? ''}`;
    return api.get<Repository[]>(`/repositories?owner=${encodeURIComponent(ownerStr)}`);
  },

  // Register a local directory as a repository (auto-init'd if not yet a git
  // repo). suppressError lets callers map ApiError.code to an inline message
  // instead of the generic toast (e.g. the workspace dialog's directory source).
  createRepository: (data: {
    path: string;
    name?: string;
    auto_init?: boolean;
    owner?: { type: 'user' | 'org'; id: number };
  }): Promise<Repository> =>
    apiFetch<Repository>('/repositories', {
      method: 'POST',
      body: JSON.stringify(data),
      suppressError: true,
    }),

  getRepositoryBranches: (repoId: string): Promise<string[]> =>
    api.get<{ branches: string[] }>(`/repositories/${repoId}/branches`).then(r => r.branches),

  getRepositoryDetail: (repoId: string): Promise<RepositoryDetail> =>
    api.get<RepositoryDetail>(`/repositories/${repoId}/detail`),

  // Project ↔ repository (incremental)
  listProjectRepositories: (projectId: string | number): Promise<import('@/types/api').ProjectRepositoryBinding[]> =>
    api.get(`/projects/${projectId}/repositories`),

  addProjectRepository: (projectId: string | number, body: { repository_id: number; default_branch: string }): Promise<import('@/types/api').ProjectRepositoryBinding> =>
    api.post(`/projects/${projectId}/repositories`, body),

  removeProjectRepository: (projectId: string | number, repoId: string | number): Promise<void> =>
    api.delete(`/projects/${projectId}/repositories/${repoId}`),

  updateProjectRepositoryBranch: (projectId: string | number, repoId: string | number, defaultBranch: string): Promise<import('@/types/api').ProjectRepositoryBinding> =>
    api.patch(`/projects/${projectId}/repositories/${repoId}`, { default_branch: defaultBranch }),

  // Worktrees
  getWorktrees: (workspaceId?: number, repoId?: number): Promise<Worktree[]> =>
    api.get<Worktree[]>('/worktrees', { params: { workspace_id: workspaceId, repo_id: repoId } }),

  createWorktree: (data: CreateWorktreeInput): Promise<Worktree> =>
    api.post<Worktree>('/worktrees', data),

  deleteWorktree: (id: number): Promise<void> =>
    api.delete(`/worktrees/${id}`),

  getWorktreeTree: (id: number, path?: string): Promise<WorkspaceTreeResponse> =>
    api.get<WorkspaceTreeResponse>(`/worktrees/${id}/tree`, { params: { path } }),

  getWorktreeGitStatus: (id: number): Promise<GitStatus> =>
    api.get<GitStatus>(`/worktrees/${id}/git/status`),

  // Workspace trees
  getWorkspaceMainTree: (workspaceId: string, path?: string): Promise<WorkspaceTreeResponse> =>
    api.get<WorkspaceTreeResponse>(`/workspaces/${workspaceId}/tree/main`, { params: { path } }),

  getWorkspaceWorktreeTree: (workspaceId: string, name: string, path?: string): Promise<WorkspaceTreeResponse> =>
    api.get<WorkspaceTreeResponse>(`/workspaces/${workspaceId}/tree/worktrees/${name}`, { params: { path } }),

  getWorkspaceWorktreeGitStatus: (workspaceId: string, name: string): Promise<GitStatus> =>
    api.get<GitStatus>(`/workspaces/${workspaceId}/tree/worktrees/${name}`, { params: { type: 'git-status' } }),

  getWorkspaceTreeGroups: (workspaceId: string): Promise<WorktreeGroup[]> =>
    api.get<WorktreeGroup[]>(`/workspaces/${workspaceId}/tree/groups`),

  getWorktreeCommits: (workspaceId: string, name: string): Promise<GitLogEntry[]> =>
    api.get<GitLogEntry[]>(`/workspaces/${workspaceId}/worktrees/${name}/commits`),

  getWorkspaceRepoBranches: (workspaceId: string, repoId: string): Promise<{ branches: string[]; current_branch: string }> =>
    api.get<{ branches: string[]; current_branch: string }>(`/workspaces/${workspaceId}/repositories/${repoId}/git/branches`),

  getWorktreeCommitDetail: (workspaceId: string, name: string, hash: string): Promise<CommitDetail> =>
    api.get<CommitDetail>(`/workspaces/${workspaceId}/worktrees/${name}/commits/${hash}`),

  updateIssueLifecycle: (issueId: number, data: UpdateLifecycleData) =>
    api.put<Issue>(`/issues/${issueId}/lifecycle`, data),

  getIssue: (issueId: string) =>
    api.get<Issue>(`/issues/${issueId}`),

  updateIssue: (issueId: string, data: {
    title?: string; description?: string; priority?: number; labels?: string;
    assignee?: string; start_date?: string; due_date?: string;
    estimate_type?: string; estimate?: number; actual_time?: number;
    goal_condition?: string;
  }) =>
    api.put<Issue>(`/issues/${issueId}`, data),

  moveIssueToColumn: (issueId: string, data: { column_id: number; position: number }) =>
    api.put(`/issues/${issueId}/move`, data),

  // Agent control
  stopAgent: (workspaceId: string) =>
    api.delete(`/workspaces/${workspaceId}/session`),

  // Issue checklists
  listChecklists: (issueId: number) =>
    api.get<IssueChecklist[]>(`/issues/${issueId}/checklists`),
  createChecklist: (issueId: number, data: { title: string }) =>
    api.post<IssueChecklist>(`/issues/${issueId}/checklists`, data),
  updateChecklist: (checklistId: number, data: { title: string; is_completed: number }) =>
    api.put<IssueChecklist>(`/checklists/${checklistId}`, data),
  updateChecklistPosition: (checklistId: number, data: { position: number }) =>
    api.put(`/checklists/${checklistId}/position`, data),
  deleteChecklist: (checklistId: number) =>
    api.delete(`/checklists/${checklistId}`),

  // Issue comments
  listIssueComments: (issueId: number) =>
    api.get<IssueComment[]>(`/issues/${issueId}/comments`),
  createIssueComment: (issueId: number, data: { author: string; content: string }) =>
    api.post<IssueComment>(`/issues/${issueId}/comments`, data),
  updateIssueComment: (commentId: number, data: { content: string }) =>
    api.put<IssueComment>(`/issue-comments/${commentId}`, data),
  deleteIssueComment: (commentId: number) =>
    api.delete(`/issue-comments/${commentId}`),

  // Review 闭环 (#623): reviewer marks 需修改 → bounce back to implement lane with
  // the two-layer review context (issue comments + unresolved diff comments) injected.
  requestChanges: (issueId: number, data: { comment: string; author?: string }) =>
    api.post(`/issues/${issueId}/request-changes`, data),

  // Workspace review comments (line-level diff comments)
  listWorkspaceComments: (workspaceId: string): Promise<WorkspaceComment[]> =>
    api.get<WorkspaceComment[]>(`/workspaces/${workspaceId}/comments`),
  createWorkspaceComment: (
    workspaceId: string,
    data: CreateWorkspaceCommentInput,
  ): Promise<WorkspaceComment> =>
    api.post<WorkspaceComment>(`/workspaces/${workspaceId}/comments`, data),
  sendCommentToAgent: (commentId: number): Promise<void> =>
    api.post<void>(`/comments/${commentId}/send-to-agent`),

  // Issue timeline
  getIssueTimeline: (issueId: number) =>
    api.get<TimelineEntry[]>(`/issues/${issueId}/timeline`),

  // Env presets
  listEnvPresets: (): Promise<EnvPreset[]> =>
    api.get<EnvPreset[]>('/env-presets'),
  createEnvPreset: (data: CreateEnvPresetData): Promise<EnvPreset> =>
    api.post<EnvPreset>('/env-presets', data),
  updateEnvPreset: (id: number, data: CreateEnvPresetData): Promise<void> =>
    api.put(`/env-presets/${id}`, data),
  deleteEnvPreset: (id: number): Promise<void> =>
    api.delete(`/env-presets/${id}`),

  // Attachments
  uploadAttachment: async (workspaceId: string, file: File): Promise<{ name: string; path: string; size: number; mimeType: string; originalSize?: number; optimized?: boolean }> => {
    const formData = new FormData();
    formData.append('file', file);
    const uploadHeaders: Record<string, string> = {};
    const uploadToken = getAccessToken();
    if (uploadToken) {
      uploadHeaders['Authorization'] = `Bearer ${uploadToken}`;
    }
    const response = await fetch(`${API_BASE}/workspaces/${workspaceId}/attachments`, {
      method: 'POST',
      headers: uploadHeaders,
      body: formData,
    });
    if (!response.ok) {
      const err = await response.json().catch(() => ({ message: `HTTP ${response.status}` }));
      throw new ApiError(response.status, err?.error?.message || err?.message || 'Upload failed', err);
    }
    return response.json();
  },

  deleteAttachment: (workspaceId: string, name: string) =>
    api.delete(`/workspaces/${workspaceId}/attachments/${encodeURIComponent(name)}`),

  // Write a text file into a worktree so it lands in the git working tree and
  // shows up (diffable) in the Changes panel. Used by the embedded-canvas
  // bridge to persist editor source files (e.g. `.excalidraw`). Returns the
  // workspace-root-relative path of the written file.
  writeWorktreeFile: (
    workspaceId: string,
    worktreeName: string,
    path: string,
    content: string,
  ): Promise<{ path: string }> =>
    api.put<{ path: string }>(
      `/workspaces/${workspaceId}/worktrees/${encodeURIComponent(worktreeName)}/file`,
      { path, content },
    ),

  searchWorkspaceFiles: (workspaceId: string, query: string) =>
    api.get<{ files: Array<{ path: string; name: string; repo: string; isDir: boolean }> }>(
      `/workspaces/${workspaceId}/files`,
      { params: { q: query } }
    ),

  // System deps
  getSystemDeps: (): Promise<SystemDepsInfo> =>
    api.get<SystemDepsInfo>('/system-deps'),

  // suppressError so 409/403 responses surface only via the page's targeted
  // toast — apiFetch's generic "API Error" toast would otherwise stack on top.
  startSystemDepsInstall: (name: string): Promise<InstallStartResponse> =>
    apiFetch<InstallStartResponse>('/system-deps/install', {
      method: 'POST',
      body: JSON.stringify({ name }),
      suppressError: true,
    }),

  // suppressError so the calling component (GitIdentityPanel) can show its
  // own targeted toast based on the error code rather than the global toast.
  // On rejection, apiFetch throws an ApiError whose `.body` is the raw JSON
  // from the backend. Callers extract the error code via:
  //   catch (err) { if (err instanceof ApiError) { const code = (err.body as any)?.error?.code } }
  // For INVALID_GIT_IDENTITY the backend returns:
  //   400 { "error": { "code": "INVALID_GIT_IDENTITY", "message": "..." } }
  setGitIdentity: (name: string, email: string): Promise<void> =>
    apiFetch<void>('/system-deps/git-identity', {
      method: 'POST',
      body: JSON.stringify({ name, email }),
      suppressError: true,
    }),

  systemDepsInstallStreamUrl: (jobID: string): string => {
    const tok = getAccessToken();
    const params = new URLSearchParams({ id: jobID });
    if (tok) params.set('token', tok);
    return `/api/system-deps/install/stream?${params.toString()}`;
  },
};

export async function fetchLifecycleGroups(projectId?: number): Promise<{ label: string; statuses: string[] }[]> {
  const params = projectId ? `?project_id=${projectId}` : '';
  return api.get(`/columns/lifecycle-groups${params}`);
}

// Mutations pass suppressError so the users-settings dialogs can show the
// backend's friendly classified message (e.g. "username already exists",
// "cannot remove last admin") without the global "API Error: ..." prefix.
//
// Do NOT pass `headers` in options to apiFetch: the wrapper's `...options`
// spread at the end of its fetch() call would re-apply options.headers AFTER
// the Authorization-merged headers, dropping the Authorization header
// entirely. The result is a 401 + forced /login redirect on every mutation.
// apiFetch already sets Content-Type=application/json by default, so the
// explicit headers were redundant anyway.
export const authUsersApi = {
  list: () => apiFetch<AuthUser[]>('/auth/users'),
  create: (body: CreateUserRequest) =>
    apiFetch<AuthUser>('/auth/users', {
      method: 'POST',
      body: JSON.stringify(body),
      suppressError: true,
    }),
  update: (id: number, body: UpdateUserRequest) =>
    apiFetch<AuthUser>(`/auth/users/${id}`, {
      method: 'PATCH',
      body: JSON.stringify(body),
      suppressError: true,
    }),
  resetPassword: (id: number, password: string) =>
    apiFetch<{ message: string }>(`/auth/users/${id}/password`, {
      method: 'POST',
      body: JSON.stringify({ password }),
      suppressError: true,
    }),
  delete: (id: number) =>
    apiFetch<{ message: string }>(`/auth/users/${id}`, {
      method: 'DELETE',
      suppressError: true,
    }),
  // Admin-only: enumerate a user's personal resources for the manage-resources
  // dialog. Handler: server/internal/api/admin_user.go.
  getResources: (id: number) =>
    apiFetch<UserResourcesResponse>(`/auth/users/${id}/resources`),
  // Delete a single personal resource (project|workspace|repository).
  // suppressError so the dialog surfaces failures via its own targeted toast.
  deleteResource: (id: number, type: UserResourceType, resourceId: number) =>
    apiFetch<{ message: string }>(
      `/auth/users/${id}/resources/${type}/${resourceId}`,
      { method: 'DELETE', suppressError: true },
    ),
  // Cascade-delete the account plus every personal resource. On a guard failure
  // the server returns 409 with a machine reason (self / last_admin /
  // last_owner_of_org:<slug>); suppressError so the dialog translates it.
  purge: (id: number) =>
    apiFetch<PurgeUserResponse>(`/auth/users/${id}/purge`, {
      method: 'POST',
      suppressError: true,
    }),
}

// Permission-prompt helpers — back the in-chat allow/deny dialog and the
// allowlist settings panel. Backend handlers in
// server/internal/api/permission.go; routes wired in
// server/internal/server/router.go.
//
// REST envelopes: list endpoints return `{ requests: [...] }` and
// `{ entries: [...] }`. apiFetch returns the parsed body directly (no axios
// `r.data` wrapper), so these helpers unwrap the field for the caller.

export async function listPermissionRequests(
  workspaceId: number,
): Promise<PermissionRequest[]> {
  const r = await api.get<{ requests: PermissionRequest[] }>(
    `/workspaces/${workspaceId}/permission-requests`,
    { params: { status: 'pending' } },
  );
  return r.requests ?? [];
}

export async function decidePermission(
  requestId: number,
  body: DecideBody,
): Promise<{ ok: boolean }> {
  return api.post<{ ok: boolean }>(
    `/agent-permission-decisions/${requestId}`,
    body,
  );
}

export async function listPermissionAllowlist(
  workspaceId: number,
): Promise<PermissionAllowlistEntry[]> {
  const r = await api.get<{ entries: PermissionAllowlistEntry[] }>(
    `/workspaces/${workspaceId}/permission-allowlist`,
  );
  return r.entries ?? [];
}

export async function deletePermissionAllowlistEntry(
  entryId: number,
): Promise<void> {
  await api.delete<void>(`/permission-allowlist/${entryId}`);
}

// ============================================================
// Ask-user-question (niuniu_ask_user_question MCP tool)
// ============================================================

export async function listAskUserRequests(
  workspaceId: number,
): Promise<AskUserRequest[]> {
  const r = await api.get<{ requests: AskUserRequest[] }>(
    `/workspaces/${workspaceId}/ask-user-requests`,
  );
  return r.requests ?? [];
}

export async function decideAskUser(
  requestId: number,
  body: AskUserDecideBody,
): Promise<{ ok: boolean }> {
  return api.post<{ ok: boolean }>(
    `/agent-ask-user-decisions/${requestId}`,
    body,
  );
}

export async function getClaudeAccountUsage(
  accountId: number,
  force = false,
): Promise<AccountUsage> {
  return api.get<AccountUsage>(`/claude-accounts/${accountId}/usage`, {
    params: force ? { force: '1' } : undefined,
  })
}

export async function getWorkspacesOverview(owner?: string): Promise<WorkspaceOverview> {
  return api.get<WorkspaceOverview>('/workspaces/overview', {
    params: owner ? { owner } : undefined,
  })
}

export async function listWorkspaceOverviewCreators(
  ownerFilter?: string,
): Promise<{ data: { id: number; username: string; display_name: string }[] }> {
  const path = ownerFilter
    ? `/workspaces/overview/creators?owner=${encodeURIComponent(ownerFilter)}`
    : '/workspaces/overview/creators';
  return api.get<{ data: { id: number; username: string; display_name: string }[] }>(path);
}

// Mark a workspace as done — backend at POST /workspaces/:id/mark-done flips
// the workspace into the 'completed' lifecycle status without committing or
// merging worktrees. Suppresses the global "API Error" toast so the sidebar's
// useMarkWorkspaceDone hook can show its own targeted message (409 →
// runningError, anything else → generic error w/ message). Callers branch on
// `err instanceof ApiError && err.status === 409`.
//
// Response shape: `{ status: 'ok', warnings?: string[] }`. The optional
// `warnings` array surfaces non-fatal sync issues (e.g. the linked issue's
// lifecycle could not be flipped in lockstep) so the sidebar can show a
// partial-success toast without flipping the whole mutation to "error".
export type MarkWorkspaceDoneResponse = {
  status: string;
  warnings?: string[];
};

export async function markWorkspaceDone(
  id: number | string,
): Promise<MarkWorkspaceDoneResponse> {
  return apiFetch<MarkWorkspaceDoneResponse>(`/workspaces/${id}/mark-done`, {
    method: 'POST',
    suppressError: true,
  });
}

// unmarkWorkspaceDone reverts a workspace that was just marked-done back to
// the caller-supplied previous status. Used by the sidebar "Undo" action to
// restore the workspace within the brief post-click window. Server validates
// previous_status against an allow-list ('created'|'needs_review'|'attention'|
// 'paused') and refuses 409 if the workspace already moved off 'completed'.
export async function unmarkWorkspaceDone(
  id: number | string,
  previousStatus: string,
): Promise<{ status: string }> {
  return apiFetch<{ status: string }>(`/workspaces/${id}/unmark-done`, {
    method: 'POST',
    body: JSON.stringify({ previous_status: previousStatus }),
    suppressError: true,
  });
}

// Per-user workspace pins (sidebar 置顶 zone, server-backed for cross-device
// sync). pin/unpin are idempotent and return 204; listPinnedWorkspaces returns
// the caller's pinned workspace ids ordered most-recently-pinned first.
export async function pinWorkspace(id: number | string): Promise<void> {
  await apiFetch<void>(`/workspaces/${id}/pin`, { method: 'PUT' });
}

export async function unpinWorkspace(id: number | string): Promise<void> {
  await apiFetch<void>(`/workspaces/${id}/pin`, { method: 'DELETE' });
}

export async function listPinnedWorkspaces(): Promise<number[]> {
  const res = await apiFetch<{ workspace_ids: number[] | null }>('/workspaces/pins');
  return res.workspace_ids ?? [];
}

// batchDeleteWorkspaces asynchronously deletes multiple workspaces. The server
// marks each accepted id 'deleting' and cleans it up in the background; the
// response returns immediately. `skipped` reports per-id reasons (forbidden /
// not_found / has_changes / already_deleting / error). force=false skips
// workspaces with uncommitted/unmerged changes instead of destroying them.
export async function batchDeleteWorkspaces(
  ids: Array<number | string>,
  force: boolean,
): Promise<BatchDeleteWorkspacesResult> {
  return apiFetch<BatchDeleteWorkspacesResult>('/workspaces/batch-delete', {
    method: 'POST',
    body: JSON.stringify({
      workspace_ids: ids.map((id) => Number(id)),
      force,
    }),
  });
}

// Project labels — CRUD wrappers around /api/projects/:id/labels.
// Backend handlers return `{ data: ... }` envelopes; helpers unwrap to the
// payload for read paths, but `create` returns the raw body so callers can
// branch on the `label_name_taken` conflict shape.
export const labels = {
  list: (projectId: number, q?: string, withUsage?: boolean) =>
    apiFetch<{ data: Label[] }>(`/projects/${projectId}/labels`, {
      params: {
        q: q || undefined,
        with_usage: withUsage ? 'true' : undefined,
      },
    }).then(r => r.data),

  create: (
    projectId: number,
    body: { name: string; color?: string; description?: string },
  ) =>
    apiFetch<{ data: Label } | { code: 'label_name_taken'; existing: Label }>(
      `/projects/${projectId}/labels`,
      {
        method: 'POST',
        body: JSON.stringify(body),
        suppressError: true,
      },
    ),

  update: (
    projectId: number,
    labelId: number,
    body: Partial<{ name: string; color: string; description: string }>,
  ) =>
    apiFetch<{ data: Label }>(
      `/projects/${projectId}/labels/${labelId}`,
      {
        method: 'PATCH',
        body: JSON.stringify(body),
      },
    ).then(r => r.data),

  delete: (projectId: number, labelId: number) =>
    apiFetch<void>(`/projects/${projectId}/labels/${labelId}`, {
      method: 'DELETE',
    }),
};

// Assignable users for a project — used by the assignee picker.
export const assignableUsers = {
  list: (projectId: number) =>
    apiFetch<{ data: AssignableUser[] }>(
      `/projects/${projectId}/assignable-users`,
    ).then(r => r.data),
};

// Issue-level assignee/label set operations. Named `issuesApi` to avoid
// colliding with TanStack Query's `issues` query keys elsewhere; the
// existing `api.*` namespace covers other issue endpoints already.
export const issuesApi = {
  setAssignees: (issueId: number, userIds: number[]) =>
    apiFetch<{ data: unknown }>(`/issues/${issueId}/assignees`, {
      method: 'PUT',
      // Body field is `assignee_user_ids` to match the CreateIssue body
      // convention (`assignee_user_ids` + `label_ids`). Don't change to
      // bare `user_ids` without updating the backend struct in
      // server/internal/api/issue.go and the smoke at
      // relay/deploy/smoke/issue-assignee-labels.sh.
      body: JSON.stringify({ assignee_user_ids: userIds }),
    }),

  setLabels: (issueId: number, labelIds: number[]) =>
    apiFetch<{ data: unknown }>(`/issues/${issueId}/labels`, {
      method: 'PUT',
      body: JSON.stringify({ label_ids: labelIds }),
    }),
};

// Executable-Epic hierarchy & execution API (E2). Backend handlers set the
// hierarchy/exec fields and report derived epic progress. The mode-A wave engine
// (execute/pause/resume) was retired in stage 9; an epic is now driven by its
// orchestration agent (created by making a workspace on the epic issue), and the
// agent dispatches children via startWorkspace.
//   - setExecFields: PUT /issues/:id/exec-fields — sets parent/type/wave/status.
//     Same-project parent is enforced server-side (400 on cross-project).
//   - epicProgress: GET /issues/:id/epic-progress — done/total + exec_status.
export const epicApi = {
  setExecFields: (
    issueId: number,
    body: {
      parent_issue_id: number | null;
      issue_type: string;
      exec_wave: number;
      exec_status: string;
    },
  ) => api.put<Issue>(`/issues/${issueId}/exec-fields`, body),

  epicProgress: (issueId: number) =>
    api.get<EpicProgress>(`/issues/${issueId}/epic-progress`),

  //   - mergeToMain: after review ('done'), ask the epic's control-workspace
  //     agent to merge the epic feature branch into the repos' default branches.
  //     The backend does NOT git-merge; it sends a merge prompt to the agent.
  mergeToMain: (issueId: number) =>
    api.post<EpicProgress>(`/issues/${issueId}/merge-to-main`, {}),

  //   - startWorkspace: dispatch a workspace for an issue (mode-B child dispatch;
  //     also exposed to agents as the start_workspace MCP tool).
  startWorkspace: (issueId: number) =>
    api.post<{ workspace_id: number }>(`/issues/${issueId}/start-workspace`, {}),

  //   - abandon: mark an issue abandoned-with-reason and park it in the backlog
  //     (spec section 19). Mirrors the abandon_issue MCP tool.
  abandon: (issueId: number, reason: string) =>
    api.post<{ issue_id: number; column_id: number; reason: string }>(
      `/issues/${issueId}/abandon`, { reason }),

  //   - execTimeline: the per-issue execution timeline + cumulative cost (spec 23.7).
  execTimeline: (issueId: number) =>
    api.get<ExecTimelineResponse>(`/issues/${issueId}/exec-timeline`),

  //   - attentionIssues: the "needs my attention" view (spec section 19) across
  //     every owner the caller can access.
  attentionIssues: () => api.get<Issue[]>(`/me/attention-issues`),
};

// Autohost 安全网 hidden-ref checkpoints (refs/niuniu/<ws>/<issue>/<step>). Bound to
// the WORKSPACE because a checkpoint is a per-worktree git snapshot; the backend
// resolves the workspace's 1:1 issue internally.
export const checkpointApi = {
  timeline: (workspaceId: string) =>
    api.get<CheckpointListResponse>(`/workspaces/${workspaceId}/checkpoints`),
  diff: (workspaceId: string, checkpointId: number) =>
    api.get<{ checkpoint_id: number; files: GitFileDiff[] }>(
      `/workspaces/${workspaceId}/checkpoints/${checkpointId}/diff`),
  revert: (workspaceId: string, step: number) =>
    api.post<CheckpointRevertResponse>(`/workspaces/${workspaceId}/checkpoints/revert`, { step }),
};

// Per-workspace MCP configuration API. Spec at
// docs/superpowers/specs/2026-05-17-per-workspace-mcp-config-design.md.
// Backend handlers in server/internal/api/workspace_mcp.go.
//
// - listAvailable: enumerates MCPs reachable under a Claude account (global ~
//   plugin) — used by the create-workspace dialog before any workspace exists.
// - detect: combines available MCPs with the selected repos' detected
//   capabilities to suggest a recommended subset + flag plugin-loaded servers.
// - get: returns the workspace's current servers, the available union, the
//   unavailable subset (selected but no longer installed), and any plugin
//   conflicts.
// - put: overwrites the workspace's MCP selection. Returns whether
//   .mcp.json was rewritten and the post-write effective list.
// - redetect: re-runs detection against current repo state (used by the
//   workspace settings "重新检测" button).
export const mcpApi = {
  listAvailable: (accountId: number) =>
    api.get<{ items: KnownMCP[] }>(`/claude-accounts/${accountId}/mcp/available`),
  detect: (body: { claude_account_id: number; repo_ids: number[] }) =>
    api.post<MCPDetectResult>('/workspaces/mcp/detect', body),
  get: (wsId: number) =>
    api.get<WorkspaceMCPState>(`/workspaces/${wsId}/mcp`),
  put: (wsId: number, servers: string[]) =>
    api.put<{ written: boolean; written_servers: string[]; unavailable: string[] }>(
      `/workspaces/${wsId}/mcp`,
      { servers },
    ),
  redetect: (wsId: number) =>
    api.post<MCPDetectResult>(`/workspaces/${wsId}/mcp/redetect`, {}),
  setStrict: (workspaceId: number, strict: boolean) =>
    api.put<{ strict: boolean; restart_required: boolean }>(
      `/workspaces/${workspaceId}/mcp/strict`,
      { strict }
    ),
};

// ---------------------------------------------------------------------------
// Scene-based MCP/plugin management (M1).
//
// Backend handlers in server/internal/api/{scene,workspace_scene,project_scene}.go.
// Spec: docs/superpowers/specs/2026-05-17-scene-based-mcp-plugin-management-design.md
// Plan: docs/superpowers/plans/2026-05-18-scene-based-mcp-plugin-management-m1.md
//
// Naming follows the existing per-domain split: `sceneApi` for scene CRUD,
// `workspaceSceneApi` for the per-workspace layer stack + projection,
// `projectSceneApi` for the prefill-only default-scenes list on a project.
// ---------------------------------------------------------------------------
export const sceneApi = {
  list: (params?: { source?: string; tag?: string }) =>
    api.get<Scene[]>('/scenes', { params }),
  get: (id: number) => api.get<Scene>(`/scenes/${id}`),
  create: (body: CreateSceneRequest) => api.post<Scene>('/scenes', body),
  update: (id: number, body: UpdateSceneRequest) =>
    api.put<Scene>(`/scenes/${id}`, body),
  delete: (id: number) => api.delete<void>(`/scenes/${id}`),
  fork: (id: number, newSlug: string, owner?: OwnerRef) =>
    api.post<Scene>(`/scenes/${id}/fork`, {
      new_slug: newSlug,
      ...(owner && owner.id > 0 ? { owner } : {}),
    }),
};

export const workspaceSceneApi = {
  listLayers: (wsId: number) =>
    api.get<SceneLayer[]>(`/workspaces/${wsId}/scene-layers`),
  attach: (wsId: number, sceneId: number, position?: number) =>
    api.post<ApplyResult>(`/workspaces/${wsId}/scene-layers`, {
      scene_id: sceneId,
      ...(position !== undefined ? { position } : {}),
    }),
  move: (wsId: number, layerId: number, position: number) =>
    api.patch<ApplyResult>(`/workspaces/${wsId}/scene-layers/${layerId}`, {
      position,
    }),
  detach: (wsId: number, layerId: number) =>
    api.delete<ApplyResult>(`/workspaces/${wsId}/scene-layers/${layerId}`),
  getProjection: (wsId: number) =>
    api.get<ApplyResult>(`/workspaces/${wsId}/scene-projection`),
  recompute: (wsId: number) =>
    api.post<ApplyResult>(`/workspaces/${wsId}/scene-projection/recompute`),
  /**
   * User-initiated plugin install. `sources` empty means "install all
   * currently pending plugins for this workspace"; passing a list lets
   * the per-row Install button install one plugin at a time.
   */
  installPlugins: (wsId: number, sources?: string[]) =>
    api.post<{ results: PluginInstallResult[] }>(
      `/workspaces/${wsId}/scene-projection/plugins/install`,
      sources && sources.length > 0 ? { sources } : {},
    ),
  /**
   * Dismiss (`dismissed` omitted/true) or restore (`dismissed=false`) a
   * scene-declared plugin so the banner stops/resumes surfacing it. Returns
   * the refreshed projection so the banner can re-render immediately.
   */
  dismissPlugin: (wsId: number, source: string, dismissed = true) =>
    api.post<ApplyResult>(
      `/workspaces/${wsId}/scene-projection/plugins/dismiss`,
      { source, dismissed },
    ),
  recommendations: (wsId: number, limit = 10) =>
    api.get<RankedScene[]>(`/workspaces/${wsId}/scene-recommendations`, {
      params: { limit },
    }),
};

export const projectSceneApi = {
  listDefaults: (projectId: number) =>
    api.get<ProjectDefaultScene[]>(`/projects/${projectId}/default-scenes`),
  attach: (projectId: number, sceneId: number, position?: number) =>
    api.post<ProjectDefaultScene>(`/projects/${projectId}/default-scenes`, {
      scene_id: sceneId,
      ...(position !== undefined ? { position } : {}),
    }),
  detach: (projectId: number, sceneId: number) =>
    api.delete<void>(`/projects/${projectId}/default-scenes/${sceneId}`),
};

// Admin-managed server settings (key/value). Only admin/owner sees the writer
// UI; non-admin requests return 403 from the backend.
export const adminSettingsApi = {
  get: (key: string) =>
    api.get<ServerSetting>(`/admin/settings/${encodeURIComponent(key)}`),
  put: (key: string, value: string) =>
    api.put<ServerSetting>(`/admin/settings/${encodeURIComponent(key)}`, { value }),
};
