-- IM Bot remote channels (Epic #555). All comments here are pure ASCII.
-- Channels are owner-level (owner_type, owner_id); a project sees its bots by
-- reverse lookup through the chats routed to it (project -> chat -> channel).

-- name: CreateIMBotChannel :one
INSERT INTO im_bot_channels (owner_type, owner_id, credential_fingerprint, channel_type, name, connection_mode, credential_enc, webhook_secret, status)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetIMBotChannel :one
SELECT * FROM im_bot_channels WHERE id = ?;

-- name: ListIMBotChannelsByProject :many
-- Bots (channels) serving this project: purely by reverse lookup through the
-- chats routed here (project -> chat -> channel). A shared bot is owner-level and
-- not bound to any project, so a project only ever sees a bot through the chats
-- routed to it.
SELECT DISTINCT ch.* FROM im_bot_channels ch
WHERE ch.id IN (SELECT c.channel_id FROM im_bot_chats c WHERE c.project_id = sqlc.arg(project_id))
ORDER BY ch.created_at, ch.id;

-- name: ListIMBotChannelsByOwner :many
SELECT * FROM im_bot_channels WHERE owner_type = ? AND owner_id = ? ORDER BY created_at, id;

-- name: ListAllIMBotChannels :many
-- Global-admin scope: every bot regardless of owner (settings page aggregation).
SELECT * FROM im_bot_channels ORDER BY created_at, id;

-- name: GetIMBotChannelByFingerprint :one
SELECT * FROM im_bot_channels
WHERE owner_type = ? AND owner_id = ? AND channel_type = ? AND credential_fingerprint = ?;

-- name: ListIMBotChannelsMissingFingerprint :many
-- Channels created before the credential fingerprint existed. Startup backfills
-- their fingerprint so the one-bot-per-app UNIQUE constraint becomes enforceable.
SELECT * FROM im_bot_channels WHERE credential_fingerprint = '' AND credential_enc != '';

-- name: ListActiveStreamChannels :many
SELECT * FROM im_bot_channels WHERE status = 'active' AND connection_mode = 'stream' ORDER BY id;

-- name: UpdateIMBotChannel :one
UPDATE im_bot_channels
SET name = ?, connection_mode = ?, webhook_secret = ?, status = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING *;

-- name: UpdateIMBotChannelCredential :exec
UPDATE im_bot_channels
SET credential_enc = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: SetIMBotChannelFingerprint :exec
UPDATE im_bot_channels
SET credential_fingerprint = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?;

-- name: DeleteIMBotChannel :exec
DELETE FROM im_bot_channels WHERE id = ?;

-- name: CreateIMBotChat :one
INSERT INTO im_bot_chats (channel_id, chat_ext_id, chat_name, status)
VALUES (?, ?, ?, ?)
RETURNING *;

-- name: GetIMBotChat :one
SELECT * FROM im_bot_chats WHERE id = ?;

-- name: GetIMBotChatByExt :one
SELECT * FROM im_bot_chats WHERE channel_id = ? AND chat_ext_id = ?;

-- name: ListIMBotChatsByChannel :many
SELECT * FROM im_bot_chats WHERE channel_id = ? ORDER BY created_at, id;

-- name: ListActiveIMBotChatsByChannel :many
SELECT * FROM im_bot_chats WHERE channel_id = ? AND status = 'active' ORDER BY id;

-- name: ListIMBotChatsByProject :many
-- A shared bot fans different chats to different projects, so a chat belongs to
-- the project it is routed to (c.project_id). Channels have no home project, so
-- unassigned (pending) chats are surfaced owner-level via ListPendingIMBotChatsByOwner,
-- not here.
SELECT c.* FROM im_bot_chats c
WHERE c.project_id = sqlc.arg(project_id)
ORDER BY c.created_at, c.id;

-- name: ListPendingIMBotChatsByOwner :many
SELECT c.* FROM im_bot_chats c
JOIN im_bot_channels ch ON ch.id = c.channel_id
WHERE ch.owner_type = ? AND ch.owner_id = ? AND c.status = 'pending'
ORDER BY c.created_at, c.id;

-- name: ListActiveIMBotChatsByOwner :many
-- The owner's active chat->project bindings (which group routes to which project),
-- for owner-level management + unbinding.
SELECT c.* FROM im_bot_chats c
JOIN im_bot_channels ch ON ch.id = c.channel_id
WHERE ch.owner_type = ? AND ch.owner_id = ? AND c.status = 'active' AND c.project_id IS NOT NULL
ORDER BY c.created_at, c.id;

-- name: ListAllPendingIMBotChats :many
-- Global-admin scope: pending chats across every owner.
SELECT c.* FROM im_bot_chats c
WHERE c.status = 'pending'
ORDER BY c.created_at, c.id;

-- name: ListAllActiveIMBotChats :many
-- Global-admin scope: active chat->project bindings across every owner.
SELECT c.* FROM im_bot_chats c
WHERE c.status = 'active' AND c.project_id IS NOT NULL
ORDER BY c.created_at, c.id;

-- name: ListActiveIMBotChatsByProject :many
SELECT * FROM im_bot_chats WHERE project_id = ? AND status = 'active' ORDER BY id;

-- name: ApproveIMBotChat :one
UPDATE im_bot_chats
SET status = 'active', paired_by = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING *;

-- name: ApproveIMBotChatToProject :one
UPDATE im_bot_chats
SET status = 'active', project_id = ?, paired_by = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING *;

-- name: ReassignIMBotChat :one
UPDATE im_bot_chats
SET project_id = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING *;

-- name: UpdateIMBotChat :one
UPDATE im_bot_chats
SET bind_mode = ?, pinned_issue_id = ?, active_issue_id = ?, status = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING *;

-- name: UpdateIMBotChatAgentMode :one
-- Agent employee mode (issue #664): 'command' acts only when addressed,
-- 'observe' also logs and periodically analyzes all chatter. Validated in the
-- service layer (the column carries no CHECK -- it is migration-added).
UPDATE im_bot_chats
SET agent_mode = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ?
RETURNING *;

-- name: ListObserveIMBotChats :many
-- Every active chat opted into observation mode, for the periodic proactive
-- analyzer sweep. Only chats already routed to a project can produce work.
SELECT * FROM im_bot_chats
WHERE status = 'active' AND agent_mode = 'observe' AND project_id IS NOT NULL
ORDER BY id;

-- name: DeleteIMBotChat :exec
DELETE FROM im_bot_chats WHERE id = ?;

-- name: CreateIMBotThread :one
INSERT INTO im_bot_threads (chat_id, thread_ext_id, issue_id, workspace_id)
VALUES (?, ?, ?, ?)
RETURNING *;

-- name: GetIMBotThreadByExt :one
SELECT * FROM im_bot_threads WHERE chat_id = ? AND thread_ext_id = ?;

-- name: ListIMBotThreadsByIssue :many
SELECT * FROM im_bot_threads WHERE issue_id = ?;

-- name: DeleteIMBotThreadsByIssue :exec
-- Remove every thread<->issue binding for a deleted task so a later in-thread
-- message no longer resolves to the gone workspace (which would silently drop it).
DELETE FROM im_bot_threads WHERE issue_id = ?;

-- name: CreateIMBotInboxEvent :exec
INSERT OR IGNORE INTO im_bot_inbox (channel_id, event_ext_id) VALUES (?, ?);

-- name: GetIMBotInboxEvent :one
SELECT * FROM im_bot_inbox WHERE channel_id = ? AND event_ext_id = ?;

-- name: CreateIMBotChatMessage :one
-- Append one observed message to a chat's rolling transcript (issue #664).
-- attachments carries JSON metadata (kind + name) for files shared in the message
-- (issue #679), never the bytes.
INSERT INTO im_bot_chat_messages (chat_id, actor_ext_id, actor_name, text, addressed, attachments)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: CountPendingIMBotChatMessages :one
-- Whether ANY observe-mode chat has unanalyzed messages. Lets the fallback
-- heartbeat no-op on an idle server instead of scanning every observe chat --
-- with a message-driven trigger, "nothing new" is the common state.
SELECT COUNT(*) FROM im_bot_chat_messages m
JOIN im_bot_chats c ON m.chat_id = c.id
WHERE m.analyzed_at IS NULL
  AND c.status = 'active'
  AND c.agent_mode = 'observe'
  AND c.project_id IS NOT NULL;

-- name: ListUnanalyzedIMBotChatMessages :many
-- The chatter a chat has accumulated since the last proactive analysis, oldest
-- first so the analyzer reads the conversation in order. Bounded by the caller.
SELECT * FROM im_bot_chat_messages
WHERE chat_id = ? AND analyzed_at IS NULL
ORDER BY id
LIMIT ?;

-- name: MarkIMBotChatMessagesAnalyzed :exec
-- Stamp a just-analyzed batch so the same chatter never drives a second
-- suggestion. Bounded by max id rather than a timestamp so messages arriving
-- mid-analysis stay unanalyzed and are picked up by the next sweep.
UPDATE im_bot_chat_messages
SET analyzed_at = CURRENT_TIMESTAMP
WHERE chat_id = ? AND analyzed_at IS NULL AND id <= ?;

-- name: TrimIMBotChatMessages :exec
-- Keep a chat's transcript to the newest N rows. The log is a rolling analysis
-- window, not an archive, so it must not grow without bound in a busy group.
-- Both references are aliased: an unqualified chat_id would be ambiguous between
-- the outer DELETE target and the inner SELECT.
DELETE FROM im_bot_chat_messages
WHERE id IN (
    SELECT m.id FROM im_bot_chat_messages m
    WHERE m.chat_id = sqlc.arg(chat_id)
      AND m.id NOT IN (
        SELECT k.id FROM im_bot_chat_messages k
        WHERE k.chat_id = sqlc.arg(chat_id)
        ORDER BY k.id DESC
        LIMIT sqlc.arg(keep)
      )
);

-- name: GetProjectContextByWorkspace :one
SELECT w.id AS workspace_id, w.issue_id AS issue_id, i.title AS issue_title, c.project_id AS project_id
FROM workspaces w
JOIN issues i ON i.id = w.issue_id
JOIN columns c ON c.id = i.column_id
WHERE w.id = ?;

-- name: CreateOnboardingToken :one
INSERT INTO im_bot_onboarding_tokens (token_hash, project_id, platform, channel_name, connection_mode, expires_at)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetOnboardingTokenByHash :one
SELECT * FROM im_bot_onboarding_tokens WHERE token_hash = ?;

-- name: MarkOnboardingTokenUsed :exec
UPDATE im_bot_onboarding_tokens SET used_at = CURRENT_TIMESTAMP WHERE id = ?;

-- name: DeleteExpiredOnboardingTokens :exec
DELETE FROM im_bot_onboarding_tokens WHERE expires_at < CURRENT_TIMESTAMP;
