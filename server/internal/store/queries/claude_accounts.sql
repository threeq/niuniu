-- WARNING: sqlc v1.30.0 has a query-string truncation bug that affects
-- this file (each query loses its trailing token, and bleed-over corrupts
-- the next query). DO NOT run `make sqlc` to regenerate `claude_accounts.sql.go`
-- without first verifying the output. The current `claude_accounts.sql.go`
-- has been manually corrected; if you must regenerate, diff the result
-- against HEAD and restore truncated tokens before committing. Track
-- sqlc upgrades to find a version where this bug is fixed.
-- See: docs/superpowers/plans/2026-05-08-claude-multi-account.md Task 5 notes.

-- name: CreateClaudeAccount :one
INSERT INTO claude_accounts (name, email, config_dir, visibility, status, created_at, created_by)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING id;

-- name: GetClaudeAccount :one
SELECT id, name, email, config_dir, visibility, status,
       created_at, last_used_at, created_by
FROM claude_accounts
WHERE id = ?;

-- name: GetDefaultClaudeAccount :one
SELECT id, name, email, config_dir, visibility, status,
       created_at, last_used_at, created_by
FROM claude_accounts
WHERE config_dir = ''
LIMIT 1;

-- name: ListClaudeAccountsForCaller :many
-- 1st ? = adminFlag (1 = caller is admin, 0 = not)
-- 2nd ? = callerID (>0 = a real user, 0 = anonymous)
-- NOTE: ascii-only comments here -- sqlc v1.30.0 truncates query strings
-- when multi-byte chars appear in -- comments inside .sql files.
SELECT id, name, email, config_dir, visibility, status,
       created_at, last_used_at, created_by
FROM claude_accounts
WHERE ? = 1
   OR visibility = 'public'
   OR created_by = ?
ORDER BY (config_dir = '') DESC, created_at ASC;

-- name: UpdateClaudeAccountName :exec
UPDATE claude_accounts SET name = ? WHERE id = ?;

-- name: UpdateClaudeAccountVisibility :exec
UPDATE claude_accounts SET visibility = ? WHERE id = ?;

-- name: UpdateClaudeAccountAfterLogin :exec
UPDATE claude_accounts
SET email = ?, status = ?, last_used_at = ?
WHERE id = ?;

-- name: UpdateClaudeAccountStatus :exec
UPDATE claude_accounts SET status = ? WHERE id = ?;

-- name: TouchClaudeAccount :exec
UPDATE claude_accounts SET last_used_at = ? WHERE id = ?;

-- name: DeleteClaudeAccount :exec
DELETE FROM claude_accounts WHERE id = ? AND config_dir != '';

-- name: GetActiveClaudeAccount :one
SELECT account_id FROM claude_active_account
WHERE owner_type = ? AND owner_id = ?;

-- name: SetActiveClaudeAccount :exec
INSERT INTO claude_active_account (owner_type, owner_id, account_id)
VALUES (?, ?, ?)
ON CONFLICT (owner_type, owner_id) DO UPDATE SET account_id = excluded.account_id;

-- name: ClearActiveClaudeAccount :exec
DELETE FROM claude_active_account WHERE owner_type = ? AND owner_id = ?;

-- name: SetWorkspaceClaudeAccount :exec
UPDATE workspaces SET claude_account_id = ? WHERE id = ?;

-- name: GetWorkspaceClaudeAccount :one
SELECT claude_account_id FROM workspaces WHERE id = ?;

-- name: GetWorkspaceForClaudeResolve :one
SELECT owner_type, owner_id, claude_account_id FROM workspaces WHERE id = ?;

-- name: GetUserRole :one
SELECT role FROM users WHERE id = ?;

-- name: AppendClaudeAccountAudit :exec
INSERT INTO claude_account_audit_log (account_id, actor_user_id, action, payload)
VALUES (?, ?, ?, ?);

-- name: ListClaudeAccountAudit :many
SELECT id, account_id, actor_user_id, action, payload, created_at
FROM claude_account_audit_log
WHERE account_id = ? AND id < ?
ORDER BY id DESC
LIMIT ?;
