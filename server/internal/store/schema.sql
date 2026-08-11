-- ============================================================
-- Projects table
-- ============================================================
CREATE TABLE IF NOT EXISTS projects (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT NOT NULL,
    description TEXT DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'active',
    owner_type  TEXT NOT NULL DEFAULT 'user' CHECK (owner_type IN ('user','org')),
    owner_id    INTEGER NOT NULL DEFAULT 0,
    color       TEXT DEFAULT NULL,
    -- Per-project schedule (5-field cron, server local time) for the automatic
    -- automatic memory-maintenance schedule. Empty means OFF (the default); a
    -- non-empty cron opts the project in. When enabled the UI suggests 33 3 3 * *.
    memory_sweep_cron TEXT NOT NULL DEFAULT '',
    -- Default agent CLI for workspaces created under this project. Pre-selected
    -- in the create UI and used verbatim when a workspace is auto-created from
    -- an issue. Mirrors workspaces.cli_type's closed set.
    default_cli_type TEXT NOT NULL DEFAULT 'claude' CHECK (default_cli_type IN ('claude','codex','qwen','omp','goose')),
    -- Per-project workspace auto-cleanup policy. cleanup_enabled=0 (default) is
    -- OFF; when 1, an hourly sweeper deletes each workspace (and its issue) whose
    -- linked issue falls in one of cleanup_statuses (comma-separated subset of
    -- 'completed','not_started') and that has had no activity for at least
    -- cleanup_inactive_days days. 0 days means the policy is inert.
    cleanup_enabled       INTEGER NOT NULL DEFAULT 0,
    cleanup_inactive_days INTEGER NOT NULL DEFAULT 0,
    cleanup_statuses      TEXT NOT NULL DEFAULT 'completed,not_started',
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- ============================================================
-- Repositories table
-- ============================================================
CREATE TABLE IF NOT EXISTS repositories (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    name           TEXT NOT NULL,
    path           TEXT NOT NULL,
    git_remote     TEXT DEFAULT '',
    default_branch TEXT DEFAULT 'main',
    owner_type     TEXT NOT NULL DEFAULT 'user' CHECK (owner_type IN ('user','org')),
    owner_id       INTEGER NOT NULL DEFAULT 0,
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- ============================================================
-- Columns table
-- ============================================================
CREATE TABLE IF NOT EXISTS columns (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id        INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name              TEXT NOT NULL,
    position          INTEGER NOT NULL DEFAULT 0,
    lifecycle_mapping TEXT NOT NULL DEFAULT '',
    reviewer_agent    TEXT,
    phase_prompt      TEXT,
    auto_advance      INTEGER NOT NULL DEFAULT 0
);

-- ============================================================
-- Issues table
-- ============================================================
CREATE TABLE IF NOT EXISTS issues (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    column_id           INTEGER NOT NULL REFERENCES columns(id) ON DELETE CASCADE,
    title               TEXT NOT NULL,
    description         TEXT DEFAULT '',
    priority            INTEGER DEFAULT 0,
    position            INTEGER NOT NULL DEFAULT 0,
    lifecycle_status    TEXT NOT NULL DEFAULT 'created',
    start_date          TEXT DEFAULT '',
    due_date            TEXT DEFAULT '',
    estimate_type       TEXT DEFAULT '',
    estimate            REAL DEFAULT 0,
    actual_time         REAL DEFAULT 0,
    external_source     TEXT DEFAULT '',
    external_id         TEXT DEFAULT '',
    external_url        TEXT DEFAULT '',
    external_snapshot_at TIMESTAMP DEFAULT NULL,
    external_comments_snapshot    TEXT DEFAULT '',
    external_comments_snapshot_at TIMESTAMP DEFAULT NULL,
    goal_condition      TEXT NOT NULL DEFAULT '',
    parent_issue_id     INTEGER REFERENCES issues(id) ON DELETE SET NULL,
    issue_type          TEXT NOT NULL DEFAULT 'task' CHECK (issue_type IN ('task','epic')),
    exec_wave           INTEGER NOT NULL DEFAULT 0,
    exec_status         TEXT NOT NULL DEFAULT 'idle' CHECK (exec_status IN ('idle','running','done','failed','paused','reviewing','gate_checking','gate_blocked','waiting_input','abandoned')),
    exec_status_reason  TEXT,
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by          INTEGER DEFAULT NULL REFERENCES users(id) ON DELETE SET NULL
);

-- ============================================================
-- Issue execution timeline (AI-native board stage 7, spec section 23.7):
-- first-class append-only log of an issue's execution trajectory.
-- ============================================================
CREATE TABLE IF NOT EXISTS issue_exec_events (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id     INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    workspace_id INTEGER,
    kind         TEXT NOT NULL CHECK (kind IN ('advance','gate','ask_user','terminal','cost','intervention')),
    summary      TEXT NOT NULL,
    detail_json  TEXT,
    cost_usd     REAL NOT NULL DEFAULT 0,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_issue_exec_events_issue ON issue_exec_events(issue_id, created_at);

-- ============================================================
-- Issue checklists table
-- ============================================================
CREATE TABLE IF NOT EXISTS issue_checklists (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id     INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    title        TEXT NOT NULL,
    is_completed INTEGER NOT NULL DEFAULT 0,
    position     INTEGER NOT NULL DEFAULT 0,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- ============================================================
-- Issue comments table
-- ============================================================
CREATE TABLE IF NOT EXISTS issue_comments (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id   INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    author     TEXT NOT NULL DEFAULT '',
    content    TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- ============================================================
-- Issue activities table
-- ============================================================
CREATE TABLE IF NOT EXISTS issue_activities (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id   INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    action     TEXT NOT NULL,
    field      TEXT DEFAULT '',
    old_value  TEXT DEFAULT '',
    new_value  TEXT DEFAULT '',
    author     TEXT DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- ============================================================
-- Harness specs table (engineering standard definitions)
-- ============================================================
-- harness_specs is a single GLOBAL engineering-standards library. It is NOT
-- owner-scoped and NOT project-scoped: the per-kanban relationship lives in
-- column_gate_specs (column_id <-> spec_id). The former scope/project_id/
-- owner_type/owner_id columns were dropped (migration MigrateDropHarnessSpecOwner).
CREATE TABLE IF NOT EXISTS harness_specs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    category    TEXT NOT NULL CHECK (category IN ('commit', 'quality', 'workflow', 'agent')),
    name        TEXT NOT NULL,
    enabled     INTEGER NOT NULL DEFAULT 1,
    severity    TEXT NOT NULL DEFAULT 'warning' CHECK (severity IN ('error', 'warning', 'info')),
    config      TEXT NOT NULL DEFAULT '{}',
    -- kind values are validated at the service layer (validateSpecInput +
    -- harness.IsValidKind). No DB CHECK to keep parity with the migration
    -- path (addColumnIfNotExists cannot retrofit a CHECK on existing DBs).
    kind                TEXT    NOT NULL DEFAULT 'regex_match',
    target              TEXT    NOT NULL DEFAULT '',
    pattern             TEXT    NOT NULL DEFAULT '',
    pattern_flags       TEXT    NOT NULL DEFAULT '',
    command             TEXT    NOT NULL DEFAULT '',
    timeout_sec         INTEGER NOT NULL DEFAULT 120,
    expected_exit_code  INTEGER NOT NULL DEFAULT 0,
    extract_regex       TEXT    NOT NULL DEFAULT '',
    threshold_value     REAL    NOT NULL DEFAULT 0,
    threshold_op        TEXT    NOT NULL DEFAULT '',
    file_paths          TEXT    NOT NULL DEFAULT '[]',
    trigger_on          TEXT    NOT NULL DEFAULT 'phase_exit',
    judge_prompt        TEXT    NOT NULL DEFAULT '',
    judge_model         TEXT    NOT NULL DEFAULT 'claude-haiku-4-5-20251001',
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(category, name)
);

CREATE INDEX IF NOT EXISTS idx_harness_specs_category ON harness_specs(category);
-- idx_harness_specs_kind is created by migrateHarnessSpecsTypedColumns in
-- store/migrate.go AFTER the kind column is ensured by addColumnIfNotExists.
-- CLAUDE.md rule: never CREATE INDEX in schema files for a column added by
-- a migration — schema runs first on existing DBs and would fail on the
-- unknown column (SQLSTATE 42703 on PG, "no such column" on SQLite).

-- ============================================================
-- Column gate specs (column-based Harness model: column 出口 gate spec 关联)
-- ============================================================
CREATE TABLE IF NOT EXISTS column_gate_specs (
    column_id     INTEGER NOT NULL REFERENCES columns(id) ON DELETE CASCADE,
    spec_id       INTEGER NOT NULL REFERENCES harness_specs(id) ON DELETE CASCADE,
    position      INTEGER NOT NULL DEFAULT 0,
    applicability TEXT NOT NULL DEFAULT 'if_routed' CHECK (applicability IN ('if_routed','always')),
    PRIMARY KEY (column_id, spec_id)
);
CREATE INDEX IF NOT EXISTS idx_column_gate_specs_column ON column_gate_specs(column_id);

-- Routing livelock counters (AI-native board, stage 5; spec
-- 2026-06-05-ai-native-board-execution-design.md §23.2): per (issue, column)
-- visit count. total routing steps for an issue = SUM(visit_count); a single
-- column's re-entry count = its visit_count. Written by advance_issue via raw
-- SQL (not modelled in sqlc, matching the stage-1a/4 migrate-only convention).
CREATE TABLE IF NOT EXISTS issue_route_visits (
    issue_id    INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    column_id   INTEGER NOT NULL REFERENCES columns(id) ON DELETE CASCADE,
    visit_count INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (issue_id, column_id)
);

-- ============================================================
-- Workspaces table
-- ============================================================
CREATE TABLE IF NOT EXISTS workspaces (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id       INTEGER REFERENCES issues(id) ON DELETE SET NULL,
    name           TEXT NOT NULL DEFAULT '',
    path           TEXT NOT NULL,
    status         TEXT NOT NULL DEFAULT 'created',
    agent_pid      INTEGER DEFAULT NULL,
    agent_status   TEXT DEFAULT 'idle',
    session_id     TEXT DEFAULT NULL,
    session_status TEXT DEFAULT 'idle',
    owner_type               TEXT NOT NULL DEFAULT 'user' CHECK (owner_type IN ('user','org')),
    owner_id                 INTEGER NOT NULL DEFAULT 0,
    current_session_user_id  INTEGER DEFAULT NULL REFERENCES users(id) ON DELETE SET NULL,
    created_by               INTEGER DEFAULT NULL REFERENCES users(id) ON DELETE SET NULL,
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    is_temporary   INTEGER NOT NULL DEFAULT 0,
    is_archived    INTEGER NOT NULL DEFAULT 0,
    archived_at    TIMESTAMP DEFAULT NULL,
    claude_account_id INTEGER DEFAULT NULL REFERENCES claude_accounts(id) ON DELETE SET NULL,
    mcp_servers TEXT NOT NULL DEFAULT '[]',
    cli_type TEXT NOT NULL DEFAULT 'claude' CHECK (cli_type IN ('claude','codex','qwen','omp','goose')),
    codex_account_id INTEGER DEFAULT NULL REFERENCES codex_accounts(id) ON DELETE SET NULL,
    codex_sandbox_mode TEXT NOT NULL DEFAULT 'danger-full-access'
        CHECK (codex_sandbox_mode IN ('read-only','workspace-write','danger-full-access')),
    codex_approval_policy TEXT NOT NULL DEFAULT 'never'
        CHECK (codex_approval_policy IN ('untrusted','on-failure','on-request','never')),
    -- is_studio marks workspaces created via the "from local directory" studio
    -- flow (issue #232): they get a preset git Bash allowlist and a delivery
    -- hint in the IDE. 1 = studio, 0 = regular dev workspace.
    is_studio      INTEGER NOT NULL DEFAULT 0,
    strict_mcp_config INTEGER NOT NULL DEFAULT 0,
    -- language is the creating user's UI language code (e.g. 'zh-CN'); it seeds
    -- the "User Language" directive in generated CLAUDE.md/AGENTS.md and is
    -- inherited by epic-derived child workspaces. '' = unknown (generic directive).
    language TEXT NOT NULL DEFAULT ''
);

-- ============================================================
-- Worktrees table (links repos to workspaces with git worktree paths)
-- ============================================================
CREATE TABLE IF NOT EXISTS worktrees (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id      INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    repository_id     INTEGER NOT NULL REFERENCES repositories(id),
    worktree_path     TEXT NOT NULL UNIQUE,
    branch            TEXT NOT NULL,
    base_branch       TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(workspace_id, repository_id)
);

-- ============================================================
-- Workspace local-runner binding (Epic #526 子B)
-- Per-workspace desktop local-runner config. Workspace-scoped child (owner
-- inherited via the workspace FK). One row per workspace; presence/online is
-- in-memory only (the reverse-channel connection), never persisted.
-- ============================================================
CREATE TABLE IF NOT EXISTS workspace_local_runner (
    workspace_id         INTEGER PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
    local_dir            TEXT NOT NULL DEFAULT '',
    prompt_snippet       TEXT NOT NULL DEFAULT '',
    allowed_commands     TEXT NOT NULL DEFAULT '[]',
    always_allow_persist INTEGER NOT NULL DEFAULT 0,
    created_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- ============================================================
-- Comments table
-- ============================================================
CREATE TABLE IF NOT EXISTS comments (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id  INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    repo          TEXT NOT NULL DEFAULT '',
    file_path     TEXT NOT NULL,
    line_number   INTEGER,
    content       TEXT NOT NULL,
    sent_to_agent BOOLEAN DEFAULT FALSE,
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- ============================================================
-- Workspace environment variables table
-- ============================================================
CREATE TABLE IF NOT EXISTS workspace_env (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    key          TEXT NOT NULL,
    value        TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(workspace_id, key)
);

-- ============================================================
-- Indexes
-- ============================================================
CREATE INDEX IF NOT EXISTS idx_columns_project_position ON columns(project_id, position);
CREATE INDEX IF NOT EXISTS idx_issues_column_position ON issues(column_id, position);
-- idx_issues_exec_status lives in migrate.go (exec_status is a migration-added column).
-- Workspace-by-issue lookup (1:1 reverse; used by GetWorkspaceByIssue)
CREATE INDEX IF NOT EXISTS idx_workspaces_issue ON workspaces(issue_id);
CREATE INDEX IF NOT EXISTS idx_issue_checklists_issue ON issue_checklists(issue_id, position);
CREATE INDEX IF NOT EXISTS idx_issue_comments_issue ON issue_comments(issue_id, created_at);
CREATE INDEX IF NOT EXISTS idx_issue_activities_issue ON issue_activities(issue_id, created_at);
CREATE INDEX IF NOT EXISTS idx_worktrees_workspace ON worktrees(workspace_id);
CREATE INDEX IF NOT EXISTS idx_worktrees_repository ON worktrees(repository_id);
CREATE INDEX IF NOT EXISTS idx_comments_workspace ON comments(workspace_id);
CREATE INDEX IF NOT EXISTS idx_workspace_env_workspace ON workspace_env(workspace_id);
-- Note: idx_workspaces_created_by is created by migrate.go after
-- addColumnIfNotExists adds the column. Declaring it here would fail on
-- upgraded DBs where CREATE TABLE IF NOT EXISTS no-ops and created_by
-- therefore doesn't exist at schema-execution time.

-- ============================================================
-- Agent messages history (AI CLI chat history)
-- ============================================================
CREATE TABLE IF NOT EXISTS agent_messages (
    id                 TEXT PRIMARY KEY,
    workspace_id       INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    role               TEXT NOT NULL,            -- 'user' | 'assistant' | 'system'
    content            TEXT NOT NULL DEFAULT '', -- text content
    message_id         TEXT NOT NULL,            -- groups events within one turn
    event_type         TEXT NOT NULL DEFAULT 'text', -- text|tool_use|tool_result|thinking|system_info|done|error
    tool_name          TEXT DEFAULT '',          -- tool name (tool_use/tool_result events)
    tool_input         TEXT DEFAULT '',          -- tool input summary (tool_use events)
    tool_use_id        TEXT DEFAULT '',          -- links tool_use to tool_result
    is_error           INTEGER NOT NULL DEFAULT 0, -- 1 if tool_result is an error
    cost_usd           REAL DEFAULT 0,          -- total cost (done events)
    num_turns          INTEGER DEFAULT 0,       -- total turns (done events)
    duration_ms        INTEGER DEFAULT 0,       -- total duration ms (done events)
    input_tokens       INTEGER DEFAULT 0,       -- input tokens (done events)
    output_tokens      INTEGER DEFAULT 0,       -- output tokens (done events)
    harness_run_id     INTEGER,                 -- legacy column; harness_runs table dropped, no FK
    attachments        TEXT DEFAULT NULL,            -- JSON array of attachment metadata
    workspace_agent_id INTEGER,
    created_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_agent_messages_ws
    ON agent_messages(workspace_id, created_at);

-- ============================================================
-- Workspace cost records — permanent, never deleted by /clear
-- ============================================================
CREATE TABLE IF NOT EXISTS workspace_costs (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id   INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    session_id     TEXT DEFAULT '',          -- claude session id
    message_id     TEXT DEFAULT '',          -- correlates with agent_messages
    cost_usd       REAL NOT NULL DEFAULT 0,  -- cost for this interaction
    num_turns      INTEGER NOT NULL DEFAULT 0,
    duration_ms    INTEGER NOT NULL DEFAULT 0,
    harness_run_id INTEGER,                 -- legacy column; harness_runs table dropped, no FK
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_workspace_costs_ws
    ON workspace_costs(workspace_id, created_at);

-- ============================================================
-- Per-workspace lifetime stats (token by type + message counts).
-- Survives /clear. owner_type/owner_id are denormalized from
-- workspaces for fast per-owner aggregation; NOT a top-level owned
-- table (excluded from topLevelOwnedTables, like
-- external_provider_credentials).
-- ============================================================
CREATE TABLE IF NOT EXISTS workspace_stats (
    workspace_id          INTEGER PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
    owner_type            TEXT    NOT NULL DEFAULT 'user' CHECK (owner_type IN ('user','org')),
    owner_id              INTEGER NOT NULL DEFAULT 0,
    user_message_count    INTEGER NOT NULL DEFAULT 0,
    ai_message_count      INTEGER NOT NULL DEFAULT 0,
    interaction_count     INTEGER NOT NULL DEFAULT 0,
    total_turns           INTEGER NOT NULL DEFAULT 0,
    total_duration_ms     INTEGER NOT NULL DEFAULT 0,
    input_tokens          INTEGER NOT NULL DEFAULT 0,
    output_tokens         INTEGER NOT NULL DEFAULT 0,
    cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens     INTEGER NOT NULL DEFAULT 0,
    last_activity_at      TIMESTAMP,
    updated_at            TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_workspace_stats_owner ON workspace_stats (owner_type, owner_id);

-- ============================================================
-- Hourly token history (workspace grain, one year retention).
-- owner-grain reporting derives via JOIN workspaces + SUM.
-- ============================================================
CREATE TABLE IF NOT EXISTS workspace_token_hourly (
    workspace_id          INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    bucket_hour           TIMESTAMP NOT NULL,
    input_tokens          INTEGER NOT NULL DEFAULT 0,
    output_tokens         INTEGER NOT NULL DEFAULT 0,
    cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens     INTEGER NOT NULL DEFAULT 0,
    interaction_count     INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (workspace_id, bucket_hour)
);
CREATE INDEX IF NOT EXISTS idx_workspace_token_hourly_time ON workspace_token_hourly (bucket_hour);

-- ============================================================
-- Session state snapshots — captures workspace file/git state when a
-- Claude session goes idle, so the next --resume can detect external
-- changes (manual edits, git pull, sibling-process commits) and inject
-- a system note into the conversation. One row per (workspace, session).
-- ============================================================
CREATE TABLE IF NOT EXISTS session_state (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id  INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    session_id    TEXT NOT NULL,
    repo_states   TEXT NOT NULL DEFAULT '{}',  -- JSON: {worktree_path: {commit_sha, dirty_hash, dirty_files}}
    last_user_msg TEXT NOT NULL DEFAULT '',    -- summary for resume banner
    snapshot_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(workspace_id, session_id)
);

CREATE INDEX IF NOT EXISTS idx_session_state_ws
    ON session_state(workspace_id);

-- ============================================================
-- Agents table (file-based agent metadata)
-- ============================================================
CREATE TABLE IF NOT EXISTS agents (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT NOT NULL UNIQUE,
    description  TEXT NOT NULL DEFAULT '',
    dir_path     TEXT NOT NULL,
    file_hash    TEXT NOT NULL DEFAULT '',
    source_url   TEXT,
    driver       TEXT NOT NULL DEFAULT 'claude-cli',
    capabilities TEXT NOT NULL DEFAULT '',
    owner_type   TEXT NOT NULL DEFAULT 'user' CHECK (owner_type IN ('user','org')),
    owner_id     INTEGER NOT NULL DEFAULT 0,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- ============================================================
-- Community agents cache (for registry)
-- ============================================================
CREATE TABLE IF NOT EXISTS community_agents (
    id            TEXT PRIMARY KEY,
    registry_name TEXT NOT NULL,
    name          TEXT NOT NULL,
    description   TEXT NOT NULL DEFAULT '',
    content       TEXT NOT NULL DEFAULT '',
    author        TEXT NOT NULL DEFAULT '',
    tags          TEXT NOT NULL DEFAULT '[]',
    source_url    TEXT NOT NULL DEFAULT '',
    cached_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(registry_name, name)
);

-- ============================================================
-- Workspace tasks table (agent task tracking)
-- ============================================================
CREATE TABLE IF NOT EXISTS workspace_tasks (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id   INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    agent_task_id  TEXT    NOT NULL,
    subject        TEXT    NOT NULL,
    description    TEXT    NOT NULL DEFAULT '',
    active_form    TEXT    NOT NULL DEFAULT '',
    status         TEXT    NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'in_progress', 'completed', 'deleted', 'interrupted')),
    phase          TEXT    NOT NULL DEFAULT 'tasks',
    message_id     TEXT    NOT NULL DEFAULT '',
    batch_id       TEXT    NOT NULL DEFAULT '',
    harness_run_id INTEGER,                 -- legacy column; harness_runs table dropped, no FK
    started_at     DATETIME,
    completed_at   DATETIME,
    created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(workspace_id, agent_task_id)
);

CREATE INDEX IF NOT EXISTS idx_workspace_tasks_ws_batch
    ON workspace_tasks(workspace_id, batch_id);

-- ============================================================
-- Pinned chat messages (workspace-scoped; owner inherited via workspace)
-- Bookmarks for individual chat-flow messages so the user can jump back to
-- them from the pin panel. message_id is the stable server messageId
-- (DOM id = msg-<message_id>); preview is captured at pin time so the panel
-- can render even when the message is not currently loaded (pagination).
-- ============================================================
CREATE TABLE IF NOT EXISTS pinned_messages (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    message_id   TEXT    NOT NULL,
    role         TEXT    NOT NULL DEFAULT '',
    preview      TEXT    NOT NULL DEFAULT '',
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(workspace_id, message_id)
);

CREATE INDEX IF NOT EXISTS idx_pinned_messages_ws
    ON pinned_messages(workspace_id, created_at);

-- ============================================================
-- Quick actions table (global, shared across all workspaces)
-- ============================================================
CREATE TABLE IF NOT EXISTS quick_actions (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    label      TEXT NOT NULL,
    content    TEXT NOT NULL,
    auto_send  INTEGER NOT NULL DEFAULT 0,
    position   INTEGER NOT NULL DEFAULT 0,
    owner_type TEXT NOT NULL DEFAULT 'user' CHECK (owner_type IN ('user','org')),
    owner_id   INTEGER NOT NULL DEFAULT 0,
    slug       TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_quick_actions_position ON quick_actions(position);
-- idx_quick_actions_owner_slug is created in store/migrate.go after the
-- slug column is added by addAssetSlugColumns, since pre-scene-feature DBs
-- don't have the column yet (CREATE INDEX would fail with SQLSTATE 42703).

-- ============================================================
-- Workspace queue (persistent message queue for agent conversations)
-- ============================================================
CREATE TABLE IF NOT EXISTS workspace_queue (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    content      TEXT NOT NULL,
    position     REAL NOT NULL DEFAULT 0,
    source       TEXT NOT NULL DEFAULT 'user',
    attachments  TEXT DEFAULT NULL,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_workspace_queue_ws_pos
    ON workspace_queue(workspace_id, position);

-- ============================================================
-- Workspace schedules (cron and one-time scheduled tasks)
-- ============================================================
CREATE TABLE IF NOT EXISTS workspace_schedules (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id  INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name          TEXT NOT NULL DEFAULT '',
    default_message TEXT NOT NULL DEFAULT '',
    schedule_type TEXT NOT NULL CHECK (schedule_type IN ('cron', 'once')),
    cron_expr     TEXT NOT NULL DEFAULT '',
    run_at        TIMESTAMP,
    enabled       INTEGER NOT NULL DEFAULT 1,
    fired_at      TIMESTAMP,
    last_run_at   TIMESTAMP,
    action_kind   TEXT NOT NULL DEFAULT 'agent_message',
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_workspace_schedules_ws
    ON workspace_schedules(workspace_id);

-- ============================================================
-- Schedule runs table (execution log for schedules)
-- ============================================================
CREATE TABLE IF NOT EXISTS schedule_runs (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    schedule_id  INTEGER NOT NULL REFERENCES workspace_schedules(id) ON DELETE CASCADE,
    workspace_id INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    status       TEXT NOT NULL CHECK (status IN ('triggered', 'skipped', 'failed')),
    message      TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_schedule_runs_schedule_time
    ON schedule_runs(schedule_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_schedule_runs_workspace
    ON schedule_runs(workspace_id, created_at DESC);

-- ============================================================
-- Env Presets table (environment variable configuration groups)
-- ============================================================
CREATE TABLE IF NOT EXISTS env_presets (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    env         TEXT NOT NULL DEFAULT '{}',  -- JSON: Record<string, string>
    owner_type  TEXT NOT NULL DEFAULT 'user' CHECK (owner_type IN ('user','org')),
    owner_id    INTEGER NOT NULL DEFAULT 0,
    slug        TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
-- idx_env_presets_owner_slug is created in store/migrate.go after the
-- slug column is added by addAssetSlugColumns (existing DBs predate it).

-- ============================================================
-- Env accounts table (subscription-platform credential references)
-- An env account holds a real API key/token for a subscription platform
-- (e.g. DeepSeek, 智谱, MiniMax). Env presets reference accounts via a
-- ${ACCOUNT:<name>} placeholder in an env value instead of embedding the
-- secret inline; sceneenv.Resolve substitutes the account's api_key at
-- spawn time. One account can be referenced by many presets, so changing
-- the key once updates every agent that uses it.
-- ============================================================
CREATE TABLE IF NOT EXISTS env_accounts (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT NOT NULL UNIQUE,
    platform    TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    api_key     TEXT NOT NULL DEFAULT '',
    owner_type  TEXT NOT NULL DEFAULT 'user' CHECK (owner_type IN ('user','org')),
    owner_id    INTEGER NOT NULL DEFAULT 0,
    slug        TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_env_accounts_owner_slug ON env_accounts(owner_type, owner_id, slug);

-- ============================================================
-- Env providers table (unified subscription-platform configs)
-- A provider holds natural model-endpoint fields (base_url, model, per-tier
-- models) plus an API key that is either a literal secret or a
-- "${ACCOUNT:<name>}" reference to an env_accounts row. When a provider is
-- applied to an agent, EnvProviderService.Expand turns its fields into the
-- correct environment key/value pair for that agent's CLI type (e.g.
-- ANTHROPIC_BASE_URL/ANTHROPIC_MODEL for Claude Code, OPENAI_* for
-- Codex/Qwen/omp/goose), so the user configures model/endpoint once instead of
-- hand-writing raw env var names.
-- ============================================================
CREATE TABLE IF NOT EXISTS env_providers (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    name          TEXT NOT NULL UNIQUE,
    platform      TEXT NOT NULL DEFAULT '',
    description   TEXT NOT NULL DEFAULT '',
    base_url      TEXT NOT NULL DEFAULT '',
    api_key       TEXT NOT NULL DEFAULT '',
    model         TEXT NOT NULL DEFAULT '',
    haiku_model   TEXT NOT NULL DEFAULT '',
    sonnet_model  TEXT NOT NULL DEFAULT '',
    opus_model    TEXT NOT NULL DEFAULT '',
    subagent_model TEXT NOT NULL DEFAULT '',
    extra_env     TEXT NOT NULL DEFAULT '{}',  -- JSON: Record<string, string> passthrough
    owner_type    TEXT NOT NULL DEFAULT 'user' CHECK (owner_type IN ('user','org')),
    owner_id      INTEGER NOT NULL DEFAULT 0,
    slug          TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_env_providers_owner_slug ON env_providers(owner_type, owner_id, slug);

-- Note: agent_messages.harness_run_id is a retained-but-dead legacy column
-- (workflow subsystem decommissioned). It has no index — the old
-- idx_agent_messages_harness_run was dropped by the drop_workflow_tables_v1
-- migration and is intentionally not recreated.

-- ============================================================
-- Harness check results table (gate check execution log)
-- ============================================================
CREATE TABLE IF NOT EXISTS harness_checks (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id  INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    run_id        INTEGER,  -- legacy column; harness_runs table dropped, no FK
    spec_id       INTEGER NOT NULL REFERENCES harness_specs(id) ON DELETE CASCADE,
    phase_name    TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL CHECK (status IN ('pass', 'fail', 'skip', 'error')),
    message       TEXT NOT NULL DEFAULT '',
    details       TEXT NOT NULL DEFAULT '',
    duration_ms   INTEGER NOT NULL DEFAULT 0,
    cost_usd      REAL    NOT NULL DEFAULT 0,
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_harness_checks_workspace ON harness_checks(workspace_id, created_at DESC);
-- harness_checks.run_id is a dead legacy column (harness_runs dropped); no index.

-- ============================================================
-- Users table (auth)
-- ============================================================
CREATE TABLE IF NOT EXISTS users (
    id                       INTEGER PRIMARY KEY AUTOINCREMENT,
    username                 TEXT NOT NULL UNIQUE,
    password_hash            TEXT NOT NULL,
    display_name             TEXT NOT NULL DEFAULT '',
    email                    TEXT NOT NULL DEFAULT '',
    role                     TEXT NOT NULL DEFAULT 'member'
        CHECK (role IN ('admin', 'member', 'viewer')),
    locked_until             TIMESTAMP,
    lockout_count            INTEGER NOT NULL DEFAULT 0,
    require_password_change  INTEGER NOT NULL DEFAULT 0,
    password_changed_at      TIMESTAMP,
    mfa_enabled              INTEGER NOT NULL DEFAULT 0,
    created_at               TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at               TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- ============================================================
-- User consent table (privacy & disclaimer agreement gate)
-- One row per user; records the last agreement version they accepted.
-- consent.CurrentVersion is bumped when the agreement text changes,
-- forcing re-consent. Accessed via raw *store.DB in internal/consent.
-- ============================================================
CREATE TABLE IF NOT EXISTS user_consents (
    user_id   INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    version   TEXT NOT NULL,
    agreed_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- ============================================================
-- Refresh tokens table (auth)
-- ============================================================
CREATE TABLE IF NOT EXISTS refresh_tokens (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    expires_at TIMESTAMP NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_refresh_tokens_user ON refresh_tokens(user_id);
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_hash ON refresh_tokens(token_hash);

-- ============================================================
-- Login attempts audit (auth security Phase A)
-- Every /login and /refresh records here (success+failure).
-- Drives both login-history UI and lockout state machine.
-- ============================================================
CREATE TABLE IF NOT EXISTS login_attempts (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER,                       -- NULL when username unknown
    username   TEXT NOT NULL,
    ip         TEXT NOT NULL DEFAULT '',
    user_agent TEXT NOT NULL DEFAULT '',
    success    INTEGER NOT NULL,              -- 1 = success, 0 = failure
    reason     TEXT NOT NULL,                 -- ok | bad_password | account_locked | ip_locked | unknown_user | ...
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_login_attempts_user    ON login_attempts(user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_login_attempts_ip      ON login_attempts(ip, created_at);
CREATE INDEX IF NOT EXISTS idx_login_attempts_created ON login_attempts(created_at);

-- ============================================================
-- IP-level lockouts (auth security Phase A)
-- Independent from per-account lockout (users.locked_until).
-- ============================================================
CREATE TABLE IF NOT EXISTS ip_lockouts (
    ip           TEXT PRIMARY KEY,
    fail_count   INTEGER NOT NULL DEFAULT 0,
    locked_until TIMESTAMP,
    updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- ============================================================
-- MFA tables (auth security Phase B)
-- ============================================================
CREATE TABLE IF NOT EXISTS user_mfa (
    user_id            INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    method             TEXT NOT NULL DEFAULT 'totp',
    secret_ciphertext  BLOB NOT NULL,
    enabled_at         TIMESTAMP,
    confirmed_at       TIMESTAMP,
    created_at         TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS user_mfa_backup_codes (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code_hash  TEXT NOT NULL,
    used_at    TIMESTAMP,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_backup_codes_user ON user_mfa_backup_codes(user_id);

CREATE TABLE IF NOT EXISTS mfa_trusted_devices (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash   TEXT NOT NULL UNIQUE,
    user_agent   TEXT NOT NULL DEFAULT '',
    last_seen_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at   TIMESTAMP NOT NULL,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_mfa_trust_user ON mfa_trusted_devices(user_id);

-- ============================================================
-- Alter existing tables
-- Note: lifecycle_status column migration is handled in store/open.go
-- via addLifecycleStatusColumn() to avoid duplicate column errors.
-- ============================================================

-- Task Analysis: is_temporary column migration is handled in store/open.go
-- via addIsTemporaryColumn() to avoid duplicate column errors.

-- ============================================================
-- Blackboard entries table (shared workspace knowledge store)
-- ============================================================
CREATE TABLE IF NOT EXISTS blackboard_entries (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id    INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    producer_agent  TEXT NOT NULL,
    entry_type      TEXT NOT NULL,
    entry_key       TEXT NOT NULL,
    content         TEXT NOT NULL DEFAULT '',
    metadata        TEXT NOT NULL DEFAULT '{}',
    ref_path        TEXT,
    harness_run_id  INTEGER,  -- legacy column; harness_runs table dropped, no FK
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_blackboard_workspace ON blackboard_entries(workspace_id);

-- NOTE: project_learnings was unified into `memories` (#256) and the physical
-- table is dropped by migrate.go dropProjectLearningsTable on existing DBs.
-- New databases never create it. See the `memories` / `memory_versions` tables.
CREATE UNIQUE INDEX IF NOT EXISTS idx_blackboard_ws_key ON blackboard_entries(workspace_id, entry_key);

-- ============================================================
-- White-box memory (owner-scoped, traceable, versioned, soft-deletable)
-- Net-new tables: columns AND indexes live here (the table never existed
-- before, so CREATE TABLE IF NOT EXISTS builds it complete on every DB).
-- owner columns are inline (not enrolled via topLevelOwnedTables: no
-- pre-existing rows to migrate), same precedent as external_provider_credentials.
-- ============================================================
CREATE TABLE IF NOT EXISTS memories (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    owner_type   TEXT    NOT NULL DEFAULT 'user' CHECK (owner_type IN ('user','org')),
    owner_id     INTEGER NOT NULL DEFAULT 0,
    project_id   INTEGER REFERENCES projects(id) ON DELETE CASCADE,
    workspace_id INTEGER REFERENCES workspaces(id) ON DELETE SET NULL,
    mem_type     TEXT    NOT NULL DEFAULT 'note' CHECK (mem_type IN ('pattern','gotcha','decision','error_fix','note','reference')),
    title        TEXT    NOT NULL,
    content      TEXT    NOT NULL DEFAULT '',
    source       TEXT    NOT NULL DEFAULT 'manual' CHECK (source IN ('manual','mcp','extract','generate','consolidate')),
    source_path  TEXT    NOT NULL DEFAULT '',
    version      INTEGER NOT NULL DEFAULT 1,
    -- Auto-evolution signals: how often this memory has been reaffirmed by later
    -- sessions, when it was last reaffirmed, and (when auto-corrected) the newer
    -- memory that replaced it. Used by EvolveForProject to archive stale entries
    -- and supersede contradicted ones without user intervention.
    reinforce_count    INTEGER NOT NULL DEFAULT 0,
    last_reinforced_at TIMESTAMP,
    superseded_by      INTEGER REFERENCES memories(id) ON DELETE SET NULL,
    -- Staleness management (memory_staleness.go): when a memory is judged to no
    -- longer match the project's latest code it is soft-deleted with a reason.
    -- pending_review=1 means it was soft-deleted but awaits human confirm/restore
    -- (AI-suspected, or a hard-stale human-authored memory); pending_review=0 with
    -- a stale_reason is an auto-applied removal of a machine memory.
    pending_review     INTEGER NOT NULL DEFAULT 0,
    stale_reason       TEXT    NOT NULL DEFAULT '',
    deleted_at   TIMESTAMP,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_memories_owner ON memories(owner_type, owner_id);
CREATE INDEX IF NOT EXISTS idx_memories_project ON memories(project_id);
CREATE INDEX IF NOT EXISTS idx_memories_workspace ON memories(workspace_id);

CREATE TABLE IF NOT EXISTS memory_versions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    memory_id   INTEGER NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
    version     INTEGER NOT NULL,
    mem_type    TEXT    NOT NULL,
    title       TEXT    NOT NULL,
    content     TEXT    NOT NULL DEFAULT '',
    source      TEXT    NOT NULL DEFAULT 'manual',
    source_path TEXT    NOT NULL DEFAULT '',
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(memory_id, version)
);
CREATE INDEX IF NOT EXISTS idx_memory_versions_mem ON memory_versions(memory_id, version DESC);

-- Execution log of automatic staleness sweeps (memory_staleness.go). One row per
-- run per project so the weekly background sweep and session-start checks are
-- user-visible (what ran, when, how many memories were auto-removed vs queued).
CREATE TABLE IF NOT EXISTS memory_sweep_runs (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id   INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    trigger      TEXT    NOT NULL DEFAULT 'schedule' CHECK (trigger IN ('schedule','session','manual')),
    scanned      INTEGER NOT NULL DEFAULT 0,
    auto_deleted INTEGER NOT NULL DEFAULT 0,
    queued       INTEGER NOT NULL DEFAULT 0,
    detail       TEXT    NOT NULL DEFAULT '',
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_memory_sweep_runs_project ON memory_sweep_runs(project_id, created_at DESC);

-- ============================================================
-- Organizations (multi-tenant)
-- ============================================================
CREATE TABLE IF NOT EXISTS organizations (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    slug                TEXT    NOT NULL UNIQUE,
    name                TEXT    NOT NULL,
    description         TEXT    NOT NULL DEFAULT '',
    external_account_id TEXT    DEFAULT NULL,
    quota_json          TEXT    NOT NULL DEFAULT '{}',
    created_by          INTEGER NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_orgs_external_account
    ON organizations(external_account_id) WHERE external_account_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS org_members (
    org_id     INTEGER NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       TEXT NOT NULL DEFAULT 'member'
                 CHECK (role IN ('owner', 'admin', 'member')),
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (org_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_org_members_user ON org_members(user_id);

CREATE TABLE IF NOT EXISTS org_audit_log (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    org_id         INTEGER NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    actor_user_id  INTEGER REFERENCES users(id) ON DELETE SET NULL,
    actor_label    TEXT    NOT NULL DEFAULT '',
    action         TEXT    NOT NULL,
    target_type    TEXT    NOT NULL DEFAULT '',
    target_id      INTEGER NOT NULL DEFAULT 0,
    payload        TEXT    NOT NULL DEFAULT '{}',
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_org_audit_org_time
    ON org_audit_log(org_id, created_at DESC);

CREATE TABLE IF NOT EXISTS mcp_session_tokens (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    token_hash   TEXT    NOT NULL UNIQUE,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at   TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_mcp_session_workspace
    ON mcp_session_tokens(workspace_id);

-- ============================================================
-- Project ↔ Repository association (many-to-many).
-- Enforced at service layer: project.owner == repository.owner.
-- Cascade-delete on either side cleans this row up.
-- ============================================================
CREATE TABLE IF NOT EXISTS project_repositories (
    project_id     INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    repository_id  INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    default_branch TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (project_id, repository_id)
);
CREATE INDEX IF NOT EXISTS idx_project_repositories_repo ON project_repositories(repository_id);

-- ----- Agent permission prompts (chat-inline tool authorization) -----
-- Generated by --permission-prompt-tool MCP mechanism. See spec
-- 2026-05-01-chat-permission-prompt-design.md.
CREATE TABLE IF NOT EXISTS agent_permission_requests (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id    INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    owner_type      TEXT NOT NULL DEFAULT 'user' CHECK (owner_type IN ('user','org')),
    owner_id        INTEGER NOT NULL DEFAULT 0,
    session_id      TEXT NOT NULL,
    tool_name       TEXT NOT NULL,
    tool_input      TEXT NOT NULL,
    status          TEXT NOT NULL CHECK (status IN ('pending','allowed','denied','timeout','cancelled')),
    decision_source TEXT,
    decided_by      INTEGER REFERENCES users(id) ON DELETE SET NULL,
    deny_message    TEXT,
    matcher_used    TEXT,
    requested_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    decided_at      TIMESTAMP,
    expires_at      TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_apr_workspace_status ON agent_permission_requests(workspace_id, status);
CREATE INDEX IF NOT EXISTS idx_apr_owner ON agent_permission_requests(owner_type, owner_id);
CREATE INDEX IF NOT EXISTS idx_apr_expires ON agent_permission_requests(expires_at) WHERE status='pending';

CREATE TABLE IF NOT EXISTS agent_permission_allowlist (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id    INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    tool_name       TEXT NOT NULL,
    matcher_kind    TEXT NOT NULL CHECK (matcher_kind IN ('any','exact','prefix','glob','domain')),
    matcher_value   TEXT NOT NULL DEFAULT '',
    created_by      INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_apa_ws_tool_kind_value
    ON agent_permission_allowlist(workspace_id, tool_name, matcher_kind, matcher_value);
CREATE INDEX IF NOT EXISTS idx_apa_workspace_tool ON agent_permission_allowlist(workspace_id, tool_name);

-- Per-user workspace pins (overview page). Keyed by (user, workspace);
-- pinned_at drives the pinned-section ordering (most-recent first). Not an
-- ownable top-level resource -- scoped directly to the acting user.
CREATE TABLE IF NOT EXISTS workspace_pins (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id         INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    workspace_id    INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    pinned_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_workspace_pins_user_ws ON workspace_pins(user_id, workspace_id);

-- Agent ask-user requests (niuniu_ask_user_question MCP tool). Parallels
-- agent_permission_requests: the MCP tool blocks until a row in this table
-- moves out of 'pending'. questions_json holds the AskUserQuestion-shaped
-- input; answers_json holds the user's selections per question.
CREATE TABLE IF NOT EXISTS agent_ask_user_requests (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id    INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    owner_type      TEXT NOT NULL DEFAULT 'user' CHECK (owner_type IN ('user','org')),
    owner_id        INTEGER NOT NULL DEFAULT 0,
    session_id      TEXT NOT NULL,
    questions_json  TEXT NOT NULL,
    status          TEXT NOT NULL CHECK (status IN ('pending','answered','timeout','cancelled')),
    decision_source TEXT,
    decided_by      INTEGER REFERENCES users(id) ON DELETE SET NULL,
    answers_json    TEXT,
    requested_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    decided_at      TIMESTAMP,
    expires_at      TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_aaur_workspace_status ON agent_ask_user_requests(workspace_id, status);
CREATE INDEX IF NOT EXISTS idx_aaur_owner ON agent_ask_user_requests(owner_type, owner_id);
CREATE INDEX IF NOT EXISTS idx_aaur_expires ON agent_ask_user_requests(expires_at) WHERE status='pending';

-- ============================================================
-- Labels (project-level shared dictionary) — added 2026-05-01
-- ============================================================
CREATE TABLE IF NOT EXISTS labels (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id   INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    color        TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by   INTEGER NOT NULL REFERENCES users(id),
    UNIQUE(project_id, name)
);
CREATE INDEX IF NOT EXISTS idx_labels_project ON labels(project_id);

CREATE TABLE IF NOT EXISTS issue_labels (
    issue_id  INTEGER NOT NULL REFERENCES issues(id)  ON DELETE CASCADE,
    label_id  INTEGER NOT NULL REFERENCES labels(id)  ON DELETE CASCADE,
    PRIMARY KEY (issue_id, label_id)
);
CREATE INDEX IF NOT EXISTS idx_issue_labels_label ON issue_labels(label_id);

CREATE TABLE IF NOT EXISTS issue_assignees (
    issue_id    INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    user_id     INTEGER NOT NULL REFERENCES users(id)  ON DELETE CASCADE,
    assigned_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (issue_id, user_id)
);
CREATE INDEX IF NOT EXISTS idx_issue_assignees_user ON issue_assignees(user_id);

-- Claude account pool (deployment-global, not owner-scoped)
CREATE TABLE IF NOT EXISTS claude_accounts (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    name          TEXT NOT NULL,
    email         TEXT,
    config_dir    TEXT NOT NULL,
    visibility    TEXT NOT NULL DEFAULT 'public'
                  CHECK (visibility IN ('public','private')),
    status        TEXT NOT NULL DEFAULT 'pending'
                  CHECK (status IN ('pending','active','failed')),
    created_at    INTEGER NOT NULL,
    last_used_at  INTEGER,
    created_by    INTEGER REFERENCES users(id) ON DELETE SET NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS claude_accounts_name_unique
    ON claude_accounts (name);

CREATE UNIQUE INDEX IF NOT EXISTS claude_accounts_only_one_default
    ON claude_accounts (config_dir) WHERE config_dir = '';

CREATE TABLE IF NOT EXISTS claude_active_account (
    owner_type  TEXT NOT NULL CHECK (owner_type IN ('user','org')),
    owner_id    INTEGER NOT NULL,
    account_id  INTEGER NOT NULL REFERENCES claude_accounts(id) ON DELETE CASCADE,
    PRIMARY KEY (owner_type, owner_id)
);

-- IM-9: 必须紧跟 claude_accounts 之后；audit_log 通过 FK 依赖 claude_accounts，
-- schema 文件按出现顺序执行 DDL，不要把 audit_log 插到 claude_accounts 之前。
CREATE TABLE IF NOT EXISTS claude_account_audit_log (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id     INTEGER REFERENCES claude_accounts(id) ON DELETE SET NULL,
    actor_user_id  INTEGER REFERENCES users(id) ON DELETE SET NULL,
    action         TEXT NOT NULL,
    payload        TEXT NOT NULL DEFAULT '{}',
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_claude_audit_account_time
    ON claude_account_audit_log(account_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_claude_audit_actor_time
    ON claude_account_audit_log(actor_user_id, created_at DESC);

-- ============================================================
-- Codex account pool (deployment-global, mirrors claude_accounts)
-- ============================================================
CREATE TABLE IF NOT EXISTS codex_accounts (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    name          TEXT NOT NULL,
    email         TEXT,
    config_dir    TEXT NOT NULL,
    visibility    TEXT NOT NULL DEFAULT 'public'
                  CHECK (visibility IN ('public','private')),
    status        TEXT NOT NULL DEFAULT 'pending'
                  CHECK (status IN ('pending','active','failed')),
    created_at    INTEGER NOT NULL,
    last_used_at  INTEGER,
    created_by    INTEGER REFERENCES users(id) ON DELETE SET NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS codex_accounts_name_unique
    ON codex_accounts (name);

CREATE TABLE IF NOT EXISTS codex_account_audit_log (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id     INTEGER REFERENCES codex_accounts(id) ON DELETE SET NULL,
    actor_user_id  INTEGER REFERENCES users(id) ON DELETE SET NULL,
    action         TEXT NOT NULL,
    payload        TEXT NOT NULL DEFAULT '{}',
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_codex_audit_account_time
    ON codex_account_audit_log(account_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_codex_audit_actor_time
    ON codex_account_audit_log(actor_user_id, created_at DESC);

-- ============================================================
-- External provider credentials (per-user, AES-GCM encrypted)
-- ============================================================
CREATE TABLE IF NOT EXISTS external_provider_credentials (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    owner_type       TEXT NOT NULL CHECK (owner_type IN ('user','org')),
    owner_id         INTEGER NOT NULL,
    user_id          INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider         TEXT NOT NULL,
    alias            TEXT NOT NULL DEFAULT '',
    config           TEXT NOT NULL DEFAULT '{}',
    last_verified_at TIMESTAMP DEFAULT NULL,
    created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(owner_type, owner_id, user_id, provider, alias)
);

-- ============================================================
-- Per-user SSH credentials for git remote authentication (per-host).
-- Spec: docs/superpowers/specs/2026-05-19-per-user-git-identity-design.md
--       v5 Phase 2 sub-phase A.
-- Private key is AES-GCM encrypted with the integration keyring;
-- public_key + fingerprint are derived at create-time so the UI
-- can display them without re-decrypting.
-- ============================================================
CREATE TABLE IF NOT EXISTS git_remote_credentials (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    host         TEXT NOT NULL,
    username     TEXT NOT NULL DEFAULT 'git',
    encrypted    TEXT NOT NULL,
    public_key   TEXT NOT NULL,
    fingerprint  TEXT NOT NULL,
    last_used_at TIMESTAMP DEFAULT NULL,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(user_id, host)
);
CREATE INDEX IF NOT EXISTS idx_git_remote_credentials_user ON git_remote_credentials(user_id);

-- ============================================================
-- Per-(user, repository) Git author identity overrides.
-- Spec: docs/superpowers/specs/2026-05-19-per-user-git-identity-design.md
--       v5.1 §3.1.6.
-- When set, overrides the user's global Git signing identity for that
-- repository. Either name or email can be left empty (falls through to
-- the global default).
-- ============================================================
CREATE TABLE IF NOT EXISTS user_repository_git_identities (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    repository_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    name          TEXT NOT NULL DEFAULT '',
    email         TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(user_id, repository_id)
);
CREATE INDEX IF NOT EXISTS idx_user_repo_git_identities_user ON user_repository_git_identities(user_id);

-- ============================================================
-- Project external sources (GitHub repo / TAPD workspace bindings)
-- ============================================================
CREATE TABLE IF NOT EXISTS project_external_sources (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id    INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    provider      TEXT NOT NULL,
    source_key    TEXT NOT NULL,
    credential_id INTEGER REFERENCES external_provider_credentials(id) ON DELETE RESTRICT,
    config        TEXT NOT NULL DEFAULT '{}',
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(project_id, provider, source_key)
);

-- ============================================================
-- External user identity mapping (cross-provider user normalization)
-- See docs/superpowers/plans/2026-05-15-l4-mcp-intent-tools.md
-- ============================================================
CREATE TABLE IF NOT EXISTS external_user_identities (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider      TEXT NOT NULL,
    external_user TEXT NOT NULL,
    display_name  TEXT NOT NULL DEFAULT '',
    avatar_url    TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(user_id, provider, external_user)
);
CREATE INDEX IF NOT EXISTS idx_external_user_identities_provider_user
    ON external_user_identities(provider, external_user);

-- ============================================================
-- External write permissions (per-user, per-provider toggle)
-- Default: disabled. Each provider must be explicitly enabled.
-- ============================================================
CREATE TABLE IF NOT EXISTS external_write_prefs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider    TEXT NOT NULL,
    enabled     INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0,1)),
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(user_id, provider)
);

-- ============================================================
-- Orchestration cost guardrails (spec 2026-06-06 section 16, phase 6)
-- Overflow queue for start_workspace when an owner is at its concurrent
-- active-workspace cap. Secondary indexes live in migrate.go per repo convention.
-- ============================================================
CREATE TABLE IF NOT EXISTS workspace_start_queue (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    owner_type  TEXT NOT NULL DEFAULT 'user' CHECK (owner_type IN ('user','org')),
    owner_id    INTEGER NOT NULL DEFAULT 0,
    issue_id    INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    status      TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','started','canceled')),
    enqueued_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at  TIMESTAMP DEFAULT NULL
);

-- ============================================================
-- Server-wide K/V settings (admin-only writes)
-- ============================================================
CREATE TABLE IF NOT EXISTS server_settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_by INTEGER REFERENCES users(id) ON DELETE SET NULL
);

-- ============================================================
-- License / authorization gate (single row, id = 1)
-- ============================================================
CREATE TABLE IF NOT EXISTS app_license (
    id               INTEGER PRIMARY KEY,
    license_blob     TEXT NOT NULL DEFAULT '',
    is_trial         INTEGER NOT NULL DEFAULT 1,
    trial_started_at INTEGER NOT NULL DEFAULT 0,
    high_water_mark  INTEGER NOT NULL DEFAULT 0,
    customer         TEXT NOT NULL DEFAULT '',
    max_seats        INTEGER NOT NULL DEFAULT 0,
    issued_at        INTEGER NOT NULL DEFAULT 0,
    expires_at       INTEGER NOT NULL DEFAULT 0,
    license_id       TEXT NOT NULL DEFAULT '',
    updated_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- ============================================================
-- Scenes (M1 — scene-based MCP/plugin management)
-- ============================================================
CREATE TABLE IF NOT EXISTS scenes (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    owner_type   TEXT NOT NULL DEFAULT 'user' CHECK (owner_type IN ('user','org')),
    owner_id     INTEGER NOT NULL DEFAULT 0,
    slug         TEXT NOT NULL,
    display_name TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    tags         TEXT NOT NULL DEFAULT '[]',
    source       TEXT NOT NULL DEFAULT 'user',
    source_slug  TEXT NOT NULL DEFAULT '',
    definition   TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    enabled      INTEGER NOT NULL DEFAULT 1,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_scenes_owner_slug ON scenes(owner_type, owner_id, slug);
CREATE INDEX IF NOT EXISTS idx_scenes_owner ON scenes(owner_type, owner_id);
CREATE INDEX IF NOT EXISTS idx_scenes_source ON scenes(source);

-- workspace_scene_layers: ordered scene layers per workspace (incl. implicit base layer)
CREATE TABLE IF NOT EXISTS workspace_scene_layers (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id    INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    scene_id        INTEGER REFERENCES scenes(id) ON DELETE RESTRICT,
    position        INTEGER NOT NULL,
    is_base         INTEGER NOT NULL DEFAULT 0,
    base_definition TEXT NOT NULL DEFAULT '{}',
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK ((is_base = 1 AND scene_id IS NULL) OR (is_base = 0 AND scene_id IS NOT NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_ws_scene_layers_pos ON workspace_scene_layers(workspace_id, position);
CREATE UNIQUE INDEX IF NOT EXISTS idx_ws_scene_layers_base ON workspace_scene_layers(workspace_id) WHERE is_base = 1;
CREATE INDEX IF NOT EXISTS idx_ws_scene_layers_scene ON workspace_scene_layers(scene_id);

-- workspace_scene_projection: cached merged definition per workspace
CREATE TABLE IF NOT EXISTS workspace_scene_projection (
    workspace_id         INTEGER PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
    digest               TEXT NOT NULL DEFAULT '',
    projected_definition TEXT NOT NULL DEFAULT '{}',
    missing_credentials  TEXT NOT NULL DEFAULT '[]',
    install_failures     TEXT NOT NULL DEFAULT '[]',
    hot_applied_at       DATETIME,
    restart_required     INTEGER NOT NULL DEFAULT 0,
    recomputed_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    dismissed_plugins    TEXT NOT NULL DEFAULT '[]'
);

-- scene_asset_imports: tracks niuniu assets imported per scene per workspace
CREATE TABLE IF NOT EXISTS scene_asset_imports (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id INTEGER NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    scene_id     INTEGER NOT NULL REFERENCES scenes(id) ON DELETE CASCADE,
    asset_kind   TEXT NOT NULL CHECK (asset_kind IN ('env_preset','project_template','quick_action','harness_spec','agent')),
    asset_id     INTEGER NOT NULL,
    imported_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_scene_asset_imports_ws ON scene_asset_imports(workspace_id);
CREATE INDEX IF NOT EXISTS idx_scene_asset_imports_scene ON scene_asset_imports(scene_id);
CREATE INDEX IF NOT EXISTS idx_scene_asset_imports_asset ON scene_asset_imports(asset_kind, asset_id);

-- project_default_scenes: prefill scenes for new workspaces created from a project
CREATE TABLE IF NOT EXISTS project_default_scenes (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    scene_id   INTEGER NOT NULL REFERENCES scenes(id) ON DELETE CASCADE,
    position   INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_project_default_scenes_unique ON project_default_scenes(project_id, scene_id);
CREATE INDEX IF NOT EXISTS idx_project_default_scenes_pos ON project_default_scenes(project_id, position);

-- ============================================================
-- Project blueprints (UI: "项目模板" / project template). A reusable snapshot
-- of a project's kanban columns + default scenes, applied when creating a new
-- project. Net-new owner-scoped table: columns + indexes inline (no historical
-- rows to migrate, so it stays out of topLevelOwnedTables — same precedent as
-- `memories`). NOT the decommissioned legacy `project_templates` run engine.
-- ============================================================
CREATE TABLE IF NOT EXISTS project_blueprints (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    owner_type  TEXT NOT NULL DEFAULT 'user' CHECK (owner_type IN ('user','org')),
    owner_id    INTEGER NOT NULL DEFAULT 0,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    content     TEXT NOT NULL,
    source      TEXT NOT NULL DEFAULT 'user' CHECK (source IN ('user','builtin')),
    slug        TEXT NOT NULL DEFAULT '',
    is_default  INTEGER NOT NULL DEFAULT 0,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_project_blueprints_owner_name ON project_blueprints(owner_type, owner_id, name);
CREATE INDEX IF NOT EXISTS idx_project_blueprints_owner ON project_blueprints(owner_type, owner_id);

-- Per-owner pointer to the default blueprint pre-selected when creating a
-- project. One row per owner; absent → falls back to the builtin is_default
-- blueprint. Net-new table: columns + indexes inline.
CREATE TABLE IF NOT EXISTS default_project_blueprints (
    owner_type   TEXT NOT NULL DEFAULT 'user' CHECK (owner_type IN ('user','org')),
    owner_id     INTEGER NOT NULL DEFAULT 0,
    blueprint_id INTEGER NOT NULL REFERENCES project_blueprints(id) ON DELETE CASCADE,
    updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (owner_type, owner_id)
);

-- ============================================================
-- External API proxy: provider definitions
-- AI-adaptive external API proxy redesign. Stores per-provider
-- configuration (base URL, auth, OpenAPI URL, whitelist) so AI
-- can call any external service through a single generic proxy.
-- ============================================================
CREATE TABLE IF NOT EXISTS external_providers (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT NOT NULL UNIQUE,
    label       TEXT NOT NULL DEFAULT '',
    api_base_url TEXT NOT NULL,
    auth_type   TEXT NOT NULL DEFAULT 'bearer',
    auth_header TEXT NOT NULL DEFAULT 'Authorization',
    auth_prefix TEXT NOT NULL DEFAULT 'Bearer',
    profile     TEXT NOT NULL DEFAULT '',
    openapi_url TEXT NOT NULL DEFAULT '',
    whitelist   TEXT NOT NULL DEFAULT '',
    enabled     INTEGER NOT NULL DEFAULT 1,
    created_by  TEXT NOT NULL DEFAULT 'user',
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- External API proxy: audit log
CREATE TABLE IF NOT EXISTS external_api_audit (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     INTEGER NOT NULL,
    provider_id INTEGER NOT NULL,
    method      TEXT NOT NULL,
    path        TEXT NOT NULL,
    status_code INTEGER NOT NULL DEFAULT 0,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Per-user, per-provider write toggle for external API proxy.
-- Distinct from external_write_prefs (which governs github/jira/tapd
-- credential-based integrations).
CREATE TABLE IF NOT EXISTS external_api_write_prefs (
    user_id     INTEGER NOT NULL,
    provider_id INTEGER NOT NULL REFERENCES external_providers(id) ON DELETE CASCADE,
    enabled     INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, provider_id)
);

-- ============================================================
-- Data integration: external data sources (SQL/Redis/CK/Mongo).
-- Spec: docs/superpowers/specs/2026-06-04-data-integration-and-dashboard-design.md
-- config is AES-GCM encrypted (host/port/user/password/options).
-- owner columns added by owner_schema.addOwnerModel.
-- ============================================================
CREATE TABLE IF NOT EXISTS data_sources (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    owner_type          TEXT NOT NULL DEFAULT 'user' CHECK (owner_type IN ('user','org')),
    owner_id            INTEGER NOT NULL DEFAULT 0,
    user_id             INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name                TEXT NOT NULL,
    kind                TEXT NOT NULL CHECK (kind IN ('mysql','postgres','clickhouse','mssql','mariadb','tidb','oceanbase','starrocks','doris','cockroachdb','greenplum','redshift','opengauss','polardbpg','yugabyte','redis','mongo','trino','elasticsearch','http')),
    config              TEXT NOT NULL DEFAULT '{}',
    scope_config        TEXT NOT NULL DEFAULT '{}',
    default_access_mode TEXT NOT NULL DEFAULT 'read' CHECK (default_access_mode IN ('read','readwrite')),
    require_confirm     TEXT NOT NULL DEFAULT 'writes_only' CHECK (require_confirm IN ('always','writes_only','never')),
    last_verified_at    TIMESTAMP DEFAULT NULL,
    created_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at          TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(owner_type, owner_id, name)
);

CREATE TABLE IF NOT EXISTS data_source_audit (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id           INTEGER NOT NULL,
    source_id         INTEGER NOT NULL,
    access_mode       TEXT NOT NULL,
    database_name     TEXT NOT NULL DEFAULT '',
    objects           TEXT NOT NULL DEFAULT '[]',
    statement_summary TEXT NOT NULL DEFAULT '',
    status            TEXT NOT NULL DEFAULT '',
    rows              INTEGER NOT NULL DEFAULT 0,
    duration_ms       INTEGER NOT NULL DEFAULT 0,
    created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- data_source_bindings scopes a data source to one or more targets
-- (workspace / project / scene). A workspace's agent sees a source only when
-- the source is bound to that workspace, to its project, or to a scene the
-- workspace currently has attached. A source with NO bindings is invisible to
-- every agent (see service.DataSourceService.ListForWorkspace). Child of
-- data_sources (owner is inherited via the source row) -> no owner columns.
CREATE TABLE IF NOT EXISTS data_source_bindings (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    source_id   INTEGER NOT NULL REFERENCES data_sources(id) ON DELETE CASCADE,
    target_type TEXT NOT NULL CHECK (target_type IN ('project')),
    target_id   INTEGER NOT NULL,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(source_id, target_type, target_id)
);
CREATE INDEX IF NOT EXISTS idx_data_source_bindings_target ON data_source_bindings(target_type, target_id);

-- ============================================================
-- Data dashboards: saved (read-only) queries, dashboards, and panels.
-- Spec: docs/superpowers/specs/2026-06-04-data-integration-and-dashboard-design.md
-- saved_queries.workspace_id records the ORIGIN workspace (nullable) so a
-- dashboard panel can link back to the workspace it was pinned from.
-- owner columns on saved_queries/dashboards added by owner_schema.addOwnerModel.
-- ============================================================
CREATE TABLE IF NOT EXISTS saved_queries (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    owner_type   TEXT NOT NULL DEFAULT 'user' CHECK (owner_type IN ('user','org')),
    owner_id     INTEGER NOT NULL DEFAULT 0,
    source_id    INTEGER REFERENCES data_sources(id) ON DELETE CASCADE,
    workspace_id INTEGER REFERENCES workspaces(id) ON DELETE SET NULL,
    name         TEXT NOT NULL,
    operation    TEXT NOT NULL DEFAULT '{}',
    chart_spec   TEXT NOT NULL DEFAULT '{}',
    snapshot     TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS dashboards (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    owner_type  TEXT NOT NULL DEFAULT 'user' CHECK (owner_type IN ('user','org')),
    owner_id    INTEGER NOT NULL DEFAULT 0,
    name        TEXT NOT NULL,
    layout      TEXT NOT NULL DEFAULT '{}',
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS dashboard_panels (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    dashboard_id         INTEGER NOT NULL REFERENCES dashboards(id) ON DELETE CASCADE,
    saved_query_id       INTEGER NOT NULL REFERENCES saved_queries(id) ON DELETE CASCADE,
    title                TEXT NOT NULL DEFAULT '',
    viz_type             TEXT NOT NULL DEFAULT 'table',
    chart_spec           TEXT NOT NULL DEFAULT '{}',
    grid_x               INTEGER NOT NULL DEFAULT 0,
    grid_y               INTEGER NOT NULL DEFAULT 0,
    grid_w               INTEGER NOT NULL DEFAULT 6,
    grid_h               INTEGER NOT NULL DEFAULT 4,
    refresh_interval_sec INTEGER NOT NULL DEFAULT 0,
    created_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- knowledge_bases: first-class owner-scoped knowledge stores (KB), independent
-- of project-learning memories. Metadata + pluggable source descriptor live in
-- the main DB; the full-text index lives in a per-owner SQLite sidecar
-- (kb_index.db) that is NOT part of dual-schema parity (see internal/kbindex).
-- source_kind: 'local' (directory/upload), 'url' (network address, async
-- download in Wave2 #500), 'repo' (reserved, not implemented this wave).
CREATE TABLE IF NOT EXISTS knowledge_bases (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    owner_type    TEXT NOT NULL DEFAULT 'user' CHECK (owner_type IN ('user','org')),
    owner_id      INTEGER NOT NULL DEFAULT 0,
    name          TEXT NOT NULL,
    description   TEXT NOT NULL DEFAULT '',
    source_kind   TEXT NOT NULL DEFAULT 'local' CHECK (source_kind IN ('local','url','repo')),
    source_addr   TEXT NOT NULL DEFAULT '',
    source_config TEXT NOT NULL DEFAULT '{}',
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(owner_type, owner_id, name)
);

-- kb_documents: one row per ingested source file within a KB. Owner is
-- inherited via knowledge_bases -> no owner columns. content_hash drives
-- idempotent re-ingest; the chunk text itself lives in the kb_index sidecar.
CREATE TABLE IF NOT EXISTS kb_documents (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    kb_id        INTEGER NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    rel_path     TEXT NOT NULL,
    uri          TEXT NOT NULL DEFAULT '',
    content_hash TEXT NOT NULL DEFAULT '',
    chunk_count  INTEGER NOT NULL DEFAULT 0,
    byte_size    INTEGER NOT NULL DEFAULT 0,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(kb_id, rel_path)
);
CREATE INDEX IF NOT EXISTS idx_kb_documents_kb ON kb_documents(kb_id);

-- kb_bindings scopes a knowledge base to one or more targets (currently
-- projects). Owner is inherited via knowledge_bases -> no owner columns.
CREATE TABLE IF NOT EXISTS kb_bindings (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    kb_id       INTEGER NOT NULL REFERENCES knowledge_bases(id) ON DELETE CASCADE,
    target_type TEXT NOT NULL CHECK (target_type IN ('project')),
    target_id   INTEGER NOT NULL,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(kb_id, target_type, target_id)
);
CREATE INDEX IF NOT EXISTS idx_kb_bindings_target ON kb_bindings(target_type, target_id);

-- ============================================================
-- IM Bot remote channels (Epic #555). im_bot_channels is owner-level: a bot is
-- owned by an (owner_type, owner_id) and belongs to no project. Child tables
-- scope via project_id (im_bot_chats routing target) or via channel_id ->
-- channel -> owner. Credentials never live here: im_bot_channels stores only an
-- AES-GCM ciphertext (credential_enc) decrypted solely in the service layer;
-- DTOs never carry plaintext.
-- ============================================================

-- im_bot_channels: one channel bot instance (a Lark self-built app / a TG bot)
-- owned by (owner_type, owner_id). A shared bot serves all of the owner's
-- projects equally; it has no home / default project. connection_mode defaults
-- to 'stream' (outbound long connection, LAN-friendly, no public webhook required).
CREATE TABLE IF NOT EXISTS im_bot_channels (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    owner_type      TEXT NOT NULL DEFAULT 'user' CHECK (owner_type IN ('user','org')),
    owner_id        INTEGER NOT NULL DEFAULT 0,
    credential_fingerprint TEXT NOT NULL DEFAULT '',
    channel_type    TEXT NOT NULL CHECK (channel_type IN ('lark','dingtalk','telegram','wework','wechat')),
    name            TEXT NOT NULL,
    connection_mode TEXT NOT NULL DEFAULT 'stream' CHECK (connection_mode IN ('stream','webhook')),
    credential_enc  TEXT NOT NULL DEFAULT '',
    webhook_secret  TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(owner_type, owner_id, channel_type, name)
);
-- NOTE: idx_im_bot_channels_owner (owner_type, owner_id) is created in migrate.go
-- (migrateIMBotSharedBot), NOT here. On a DB upgraded from a version that already
-- had im_bot_channels, owner_type/owner_id are added by that migration, which runs
-- AFTER this schema on every startup; declaring the index here fails with
-- "no such column: owner_type" -> Open() errors -> the embedded server exits
-- before the ready handshake ("bundle: server exited before ready handshake").
-- See the DB red lines in CLAUDE.md (never CREATE INDEX in schema files for a
-- migration-added column).
CREATE INDEX IF NOT EXISTS idx_im_bot_channels_status_mode ON im_bot_channels(status, connection_mode);

-- im_bot_chats: a paired chat (group / DM) admission record under a channel.
-- Only status='active' chats may drive the project's agents; strangers land as
-- 'pending' awaiting an admin approval.
CREATE TABLE IF NOT EXISTS im_bot_chats (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    channel_id      INTEGER NOT NULL REFERENCES im_bot_channels(id) ON DELETE CASCADE,
    project_id      INTEGER REFERENCES projects(id) ON DELETE CASCADE,
    chat_ext_id     TEXT NOT NULL,
    chat_name       TEXT NOT NULL DEFAULT '',
    bind_mode       TEXT NOT NULL DEFAULT 'project' CHECK (bind_mode IN ('project','workspace')),
    pinned_issue_id INTEGER,
    active_issue_id INTEGER,
    status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','active','disabled')),
    paired_by       INTEGER,
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(channel_id, chat_ext_id)
);
CREATE INDEX IF NOT EXISTS idx_im_bot_chats_channel ON im_bot_chats(channel_id);

-- im_bot_threads: second-layer thread<->issue mapping (threaded channels).
CREATE TABLE IF NOT EXISTS im_bot_threads (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    chat_id       INTEGER NOT NULL REFERENCES im_bot_chats(id) ON DELETE CASCADE,
    thread_ext_id TEXT NOT NULL,
    issue_id      INTEGER NOT NULL,
    workspace_id  INTEGER NOT NULL,
    created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(chat_id, thread_ext_id)
);
CREATE INDEX IF NOT EXISTS idx_im_bot_threads_chat ON im_bot_threads(chat_id);

-- im_bot_inbox: idempotent dedupe of processed inbound event ids (platforms
-- re-deliver). Prevents building duplicate tasks on redelivery.
CREATE TABLE IF NOT EXISTS im_bot_inbox (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    channel_id   INTEGER NOT NULL REFERENCES im_bot_channels(id) ON DELETE CASCADE,
    event_ext_id TEXT NOT NULL,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(channel_id, event_ext_id)
);

-- im_bot_onboarding_tokens: one-time credential-entry token for IM Bot AI
-- onboarding. A short-lived token lets a project admin hand off bot credential
-- setup to the server without exposing long-term secrets in URLs.
CREATE TABLE IF NOT EXISTS im_bot_onboarding_tokens (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    token_hash      TEXT NOT NULL UNIQUE,
    project_id      INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    platform        TEXT NOT NULL CHECK (platform IN ('lark','dingtalk','telegram','wework','wechat')),
    channel_name    TEXT NOT NULL DEFAULT '',
    connection_mode TEXT NOT NULL DEFAULT 'stream' CHECK (connection_mode IN ('stream','webhook')),
    expires_at      TIMESTAMP NOT NULL,
    used_at         TIMESTAMP,
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
