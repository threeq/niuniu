-- name: ListWorktrees :many
SELECT * FROM worktrees WHERE workspace_id = ?;

-- name: ListWorktreesWithRepository :many
SELECT
  wt.id, wt.workspace_id, wt.repository_id, wt.worktree_path, wt.branch, wt.base_branch, wt.created_at,
  r.id as r_id, r.name as r_name, r.path as r_path, r.git_remote as r_git_remote,
  r.default_branch as r_default_branch, r.created_at as r_created_at, r.updated_at as r_updated_at
FROM worktrees wt
JOIN repositories r ON r.id = wt.repository_id
WHERE wt.workspace_id = ?;

-- name: ListWorktreesWithRepositoryForWorkspaces :many
-- Batched form of ListWorktreesWithRepository for the workspace sidebar: one
-- query returns the worktree+repository rows for every listed workspace so the
-- sidebar avoids a per-workspace round-trip. Caller groups rows by workspace_id.
SELECT
  wt.id, wt.workspace_id, wt.repository_id, wt.worktree_path, wt.branch, wt.base_branch, wt.created_at,
  r.id as r_id, r.name as r_name, r.path as r_path, r.git_remote as r_git_remote,
  r.default_branch as r_default_branch, r.created_at as r_created_at, r.updated_at as r_updated_at
FROM worktrees wt
JOIN repositories r ON r.id = wt.repository_id
WHERE wt.workspace_id IN (sqlc.slice('workspace_ids'));

-- name: ListWorktreesByRepository :many
SELECT * FROM worktrees WHERE repository_id = ?;

-- name: ListWorktreesByWorkspaceAndRepo :many
SELECT * FROM worktrees WHERE workspace_id = ? AND repository_id = ?;

-- name: CreateWorktree :one
INSERT INTO worktrees (workspace_id, repository_id, worktree_path, branch, base_branch) VALUES (?, ?, ?, ?, ?) RETURNING *;

-- name: DeleteWorktree :exec
DELETE FROM worktrees WHERE id = ?;

-- name: GetWorktreeByWorkspaceAndRepo :one
SELECT * FROM worktrees WHERE workspace_id = ? AND repository_id = ?;

-- name: GetWorktree :one
SELECT * FROM worktrees WHERE id = ?;

-- name: GetWorkspaceByRepository :one
SELECT w.id, w.issue_id, w.path, w.status, w.agent_pid, w.agent_status, w.created_at, w.updated_at
FROM worktrees wt
JOIN workspaces w ON w.id = wt.workspace_id
WHERE wt.repository_id = ?;

-- name: ListWorktreesByRepositoryWithWorkspace :many
SELECT wt.id, wt.workspace_id, wt.repository_id, wt.worktree_path, wt.branch, wt.base_branch, wt.created_at, w.path as workspace_path
FROM worktrees wt
JOIN workspaces w ON w.id = wt.workspace_id
WHERE wt.repository_id = ?;

-- name: UpdateWorktreePath :exec
UPDATE worktrees SET worktree_path = ? WHERE id = ?;

-- name: ListAllWorktrees :many
SELECT * FROM worktrees;
