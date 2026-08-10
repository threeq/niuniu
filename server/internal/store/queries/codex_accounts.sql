-- WARNING: same sqlc v1.30.0 truncation guard as claude_accounts.sql.
-- ASCII-only comments. No em-dash, no full-width chars. See
-- docs/superpowers/specs/2026-05-22-codex-full-support-design.md.

-- name: CreateCodexAccount :one
INSERT INTO codex_accounts (name, email, config_dir, visibility, status, created_at, created_by)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING id;

-- name: GetCodexAccount :one
SELECT id, name, email, config_dir, visibility, status,
       created_at, last_used_at, created_by
FROM codex_accounts
WHERE id = ?;

-- name: ListCodexAccountsForCaller :many
-- 1st ? = adminFlag (1 = caller is admin, 0 = not)
-- 2nd ? = callerID (>0 = a real user, 0 = anonymous)
SELECT id, name, email, config_dir, visibility, status,
       created_at, last_used_at, created_by
FROM codex_accounts
WHERE ? = 1
   OR visibility = 'public'
   OR created_by = ?
ORDER BY created_at ASC;

-- name: GetCodexAccountByName :one
SELECT id, name, email, config_dir, visibility, status,
       created_at, last_used_at, created_by
FROM codex_accounts
WHERE name = ?;

-- name: UpdateCodexAccountName :exec
UPDATE codex_accounts SET name = ? WHERE id = ?;

-- name: UpdateCodexAccountVisibility :exec
UPDATE codex_accounts SET visibility = ? WHERE id = ?;

-- name: UpdateCodexAccountAfterLogin :exec
UPDATE codex_accounts
SET email = ?, status = ?, last_used_at = ?
WHERE id = ?;

-- name: UpdateCodexAccountStatus :exec
UPDATE codex_accounts SET status = ? WHERE id = ?;

-- name: TouchCodexAccount :exec
UPDATE codex_accounts SET last_used_at = ? WHERE id = ?;

-- name: DeleteCodexAccount :exec
DELETE FROM codex_accounts WHERE id = ?;

-- name: SetWorkspaceCodexAccount :exec
UPDATE workspaces SET codex_account_id = ? WHERE id = ?;

-- name: GetWorkspaceCodexAccount :one
SELECT codex_account_id FROM workspaces WHERE id = ?;

-- name: GetWorkspaceForCodexResolve :one
SELECT owner_type, owner_id, codex_account_id FROM workspaces WHERE id = ?;

-- name: ClearWorkspaceCodexAccountByAccountID :exec
UPDATE workspaces SET codex_account_id = NULL WHERE codex_account_id = ?;

-- name: SetWorkspaceCodexSandbox :exec
UPDATE workspaces SET codex_sandbox_mode = ?, codex_approval_policy = ? WHERE id = ?;

-- name: GetWorkspaceCodexConfig :one
SELECT cli_type, codex_account_id, codex_sandbox_mode, codex_approval_policy
FROM workspaces
WHERE id = ?;

-- name: AppendCodexAccountAudit :exec
INSERT INTO codex_account_audit_log (account_id, actor_user_id, action, payload)
VALUES (?, ?, ?, ?);

-- name: ListCodexAccountAudit :many
SELECT id, account_id, actor_user_id, action, payload, created_at
FROM codex_account_audit_log
WHERE account_id = ? AND id < ?
ORDER BY id DESC
LIMIT ?;
