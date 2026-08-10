-- name: CreateAgentMessage :exec
INSERT INTO agent_messages (id, workspace_id, role, content, message_id, event_type, tool_name, tool_input, tool_use_id, is_error, cost_usd, num_turns, duration_ms, input_tokens, output_tokens, harness_run_id, attachments, workspace_agent_id, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP);

-- name: ListAgentMessages :many
SELECT * FROM agent_messages
WHERE workspace_id = ?
ORDER BY created_at ASC, id ASC
LIMIT ? OFFSET ?;

-- name: ListAgentMessagesLatest :many
SELECT * FROM (
    SELECT * FROM agent_messages
    WHERE workspace_id = ?
    ORDER BY created_at DESC, id DESC
    LIMIT ?
) ORDER BY created_at ASC, id ASC;

-- name: ListAgentMessagesBefore :many
SELECT * FROM (
    SELECT am.* FROM agent_messages am
    WHERE am.workspace_id = ?
      AND (am.created_at < (SELECT am2.created_at FROM agent_messages am2 WHERE am2.id = ?)
           OR (am.created_at = (SELECT am2.created_at FROM agent_messages am2 WHERE am2.id = ?) AND am.id < ?))
    ORDER BY am.created_at DESC, am.id DESC
    LIMIT ?
) ORDER BY created_at ASC, id ASC;

-- name: ListAgentMessagesAfter :many
SELECT am.* FROM agent_messages am
WHERE am.workspace_id = ?
  AND (am.created_at > (SELECT am2.created_at FROM agent_messages am2 WHERE am2.id = ?)
       OR (am.created_at = (SELECT am2.created_at FROM agent_messages am2 WHERE am2.id = ?) AND am.id > ?))
ORDER BY am.created_at ASC, am.id ASC
LIMIT ?;

-- name: CountAgentMessages :one
SELECT COUNT(*) FROM agent_messages
WHERE workspace_id = ?;

-- name: GetAgentMessage :one
SELECT * FROM agent_messages
WHERE id = ?;

-- name: GetLastAgentMessage :one
SELECT * FROM agent_messages
WHERE workspace_id = ?
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: ListAgentMessagesByAgent :many
SELECT * FROM agent_messages
WHERE workspace_id = ? AND workspace_agent_id = ?
ORDER BY created_at DESC, id DESC
LIMIT ?;

-- name: PruneAgentMessages :exec
-- Delete messages older than cutoff for archived workspaces (retention policy).
DELETE FROM agent_messages
WHERE workspace_id IN (SELECT id FROM workspaces WHERE is_archived = 1)
  AND agent_messages.created_at < ?;

-- name: PruneOrphanedAgentMessages :exec
-- Delete agent_messages whose workspace no longer exists.
-- Handles rows left behind by older versions where foreign_keys was disabled.
DELETE FROM agent_messages WHERE workspace_id NOT IN (SELECT id FROM workspaces);

-- name: SearchWorkspaceIDsByUserContentForOwners :many
-- Distinct workspace IDs (within the caller's accessible scope) that contain at
-- least one user-authored chat message whose text content matches the keyword.
-- role='user' selects messages the human sent (assistant/tool output excluded);
-- event_type='text' skips tool_use/thinking/system rows. No ESCAPE clause (sqlc
-- parser limitation); user wildcards %,_ match as SQL wildcards (same as user
-- search). Caller pre-narrows scope via authz to avoid cross-owner leaks.
SELECT DISTINCT w.id
FROM agent_messages m
JOIN workspaces w ON w.id = m.workspace_id
WHERE w.is_archived = 0
  AND m.role = 'user'
  AND m.event_type = 'text'
  AND LOWER(m.content) LIKE LOWER(?)
  AND ((w.owner_type = 'user' AND w.owner_id = ?)
    OR (w.owner_type = 'org'  AND w.owner_id IN (sqlc.slice('org_ids'))))
ORDER BY w.id;

-- name: LatestMessagePerAgent :many
SELECT am.*
FROM agent_messages am
WHERE am.workspace_id = ?
  AND am.workspace_agent_id IS NOT NULL
  AND am.id = (
    SELECT am2.id FROM agent_messages am2
    WHERE am2.workspace_id = am.workspace_id
      AND am2.workspace_agent_id = am.workspace_agent_id
    ORDER BY am2.created_at DESC, am2.id DESC
    LIMIT 1
  );

