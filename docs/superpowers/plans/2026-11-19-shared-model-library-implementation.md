# 共享模型库 + 用量预算 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 增加一份 owner 级共享模型库，让所有 provider 与 Claude Code / Codex / Qwen Code 共用模型名；按 owner / 项目 / workspace / CLI / 模型 / provider 归因 token 用量，并提供 owner 月度 token 硬上限与项目软上限。

**Architecture:** 复用现有 `env_providers` / `sceneenv` / `agentproxy`，不新增代理或监听端口。模型库只影响 provider 表单下拉与手输；用量在现有 `recordProviderTokens` 调用点旁写入 `model_usage_hourly`；预算在 `GetOrStartSession` 与会话每个新 turn 前检查。

**Tech Stack:** Go 1.25、Gin、sqlc、SQLite/PostgreSQL、React 19、TanStack Query、shadcn/ui。

**设计文档：** `docs/superpowers/specs/2026-11-19-shared-model-library-design.md`

## Global Constraints

- 任务编号只表示依赖顺序，不表示里程碑；每个任务交付最终行为，不允许占位实现。
- 新增只有 3 张表：`models`、`model_usage_hourly`、`model_budget_settings`；不改 `env_providers`、`workspaces`。
- 不做代理、别名、请求级 fallback、虚拟 key、美元成本、团队治理。
- 模型库是 owner 级共享资源，不绑定 provider。
- 双数据库：DDL 同时写 `schema.sql` 与 `schema_postgres.sql`；跑 `make schema-diff`。
- sqlc：改 `store/queries/*.sql` 后跑 `make sqlc`；查询注释纯 ASCII。
- 迁移列用 `addColumnIfNotExists`；新表用 `CREATE TABLE IF NOT EXISTS`。
- 默认不配置模型库/预算时，provider/env/CLI 行为与现在逐字节一致。
- 统计失败 fail-open：用量/预算查询故障不能阻断用户执行；只有明确超限才拦截。
- 每任务结束前跑对应测试并提交；提交信息不添加 `Co-Authored-By`。

## 目标文件结构

```
server/internal/service/
  model_library.go      # 模型库 CRUD + 系统/owner 合并
  model_refresh.go      # provider /models 预览与导入
  model_usage.go        # 用量聚合
  model_budget.go       # owner 硬上限 / 项目软上限
server/internal/api/
  models.go             # /api/models、/api/model-usage、/api/model-budget
server/internal/store/queries/
  models.sql            # 模型库、用量、预算查询
server/internal/agentproxy/
  model_budget.go       # BudgetChecker 接口 + 会话检查辅助
server/cmd/niuniu-mcp/
  model_tools.go        # model_* MCP 工具
server/web/src/
  pages/settings/model-settings.tsx
  components/models/model-input.tsx
  components/models/model-refresh-dialog.tsx
  lib/model-api.ts
  types/model.ts
```

---

## Task 1: 数据模型与 sqlc 查询

**Files:**
- Modify: `server/internal/store/schema.sql`
- Modify: `server/internal/store/schema_postgres.sql`
- Modify: `server/internal/store/migrate.go`
- Create: `server/internal/store/queries/models.sql`
- Create: `server/internal/service/model_seed.go`
- Test: `server/internal/store/migrate_models_test.go`
- Test: `server/internal/service/model_seed_test.go`

**Interfaces:**
- Produces: `store.Queries`：`ListModelsForOwner`、`GetModel`、`GetModelByName`、`CreateModel`、`UpdateModel`、`DeleteModel`、`UpsertModelUsageHourly`、`SumModelUsageByOwner`、`SumModelUsageByProject`、`ListModelUsageByModel`、`ListModelUsageByProvider`、`ListModelUsageByProject`、`ListModelUsageByWorkspace`、`ListModelUsageByCLI`、`GetModelBudget`、`UpsertModelBudget`、`DeleteModelBudget`、`ListModelBudgetsForOwner`。
- Produces: `service.SeedBuiltinModels(ctx context.Context, q *store.Queries) error`。

- [ ] **Step 1: 写迁移测试（先失败）**

```go
package store

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestModelsSchemaFreshAndRepeatable(t *testing.T) {
	Driver = "sqlite"
	db := openTestDB(t)
	Migrate(db)
	Migrate(db)

	for _, tbl := range []string{"models", "model_usage_hourly", "model_budget_settings"} {
		var n int
		require.NoError(t, db.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&n))
		require.Equal(t, 1, n, "table %s missing", tbl)
	}
	ok, err := columnExists(db, "model_usage_hourly", "estimated")
	require.NoError(t, err)
	require.True(t, ok)
}
```

- [ ] **Step 2: 运行测试，确认失败**

Run: `cd server && go test ./internal/store -run TestModelsSchema -v`
Expected: FAIL，`no such table: models`。

- [ ] **Step 3: 双写 DDL**

```sql
CREATE TABLE IF NOT EXISTS models (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    owner_type   TEXT NOT NULL DEFAULT 'user' CHECK (owner_type IN ('user','org')),
    owner_id     INTEGER NOT NULL DEFAULT 0,
    name         TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    enabled      INTEGER NOT NULL DEFAULT 1,
    source       TEXT NOT NULL DEFAULT 'manual',
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(owner_type, owner_id, name)
);
CREATE INDEX IF NOT EXISTS idx_models_owner ON models(owner_type, owner_id, enabled);

CREATE TABLE IF NOT EXISTS model_usage_hourly (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    owner_type            TEXT NOT NULL DEFAULT 'user' CHECK (owner_type IN ('user','org')),
    owner_id              INTEGER NOT NULL DEFAULT 0,
    project_id            INTEGER NOT NULL DEFAULT 0,
    workspace_id          INTEGER NOT NULL DEFAULT 0,
    agent_cli             TEXT NOT NULL DEFAULT '',
    model_name            TEXT NOT NULL DEFAULT '',
    provider_id           INTEGER NOT NULL DEFAULT 0,
    bucket_hour           TIMESTAMP NOT NULL,
    input_tokens          INTEGER NOT NULL DEFAULT 0,
    output_tokens         INTEGER NOT NULL DEFAULT 0,
    cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens     INTEGER NOT NULL DEFAULT 0,
    interaction_count     INTEGER NOT NULL DEFAULT 0,
    estimated             INTEGER NOT NULL DEFAULT 0,
    UNIQUE(owner_type, owner_id, project_id, workspace_id, agent_cli, model_name, provider_id, bucket_hour)
);
CREATE INDEX IF NOT EXISTS idx_model_usage_owner_time
    ON model_usage_hourly(owner_type, owner_id, bucket_hour);
CREATE INDEX IF NOT EXISTS idx_model_usage_project_time
    ON model_usage_hourly(project_id, bucket_hour);

CREATE TABLE IF NOT EXISTS model_budget_settings (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    owner_type     TEXT NOT NULL DEFAULT 'user' CHECK (owner_type IN ('user','org')),
    owner_id       INTEGER NOT NULL DEFAULT 0,
    project_id     INTEGER NOT NULL DEFAULT 0,
    monthly_tokens INTEGER NOT NULL DEFAULT 0,
    enabled        INTEGER NOT NULL DEFAULT 1,
    created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(owner_type, owner_id, project_id)
);
```

PostgreSQL 转换：`AUTOINCREMENT` → `BIGSERIAL`，`TIMESTAMP` → `TIMESTAMPTZ`。`migrate.go` 加 `migrateModels(db)` 建上述索引。

- [ ] **Step 4: 写 sqlc 查询**

```sql
-- server/internal/store/queries/models.sql

-- name: ListModelsForOwner :many
SELECT * FROM models
WHERE (owner_type = 'user' AND owner_id = 0)
   OR (owner_type = ? AND owner_id = ?)
ORDER BY name ASC;

-- name: GetModel :one
SELECT * FROM models WHERE id = ?;

-- name: GetModelByName :one
SELECT * FROM models WHERE owner_type = ? AND owner_id = ? AND name = ?;

-- name: CreateModel :one
INSERT INTO models (owner_type, owner_id, name, display_name, enabled, source)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateModel :one
UPDATE models SET display_name = ?, enabled = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ? RETURNING *;

-- name: DeleteModel :execrows
DELETE FROM models WHERE id = ? AND owner_type = ? AND owner_id = ?;

-- name: UpsertModelUsageHourly :exec
INSERT INTO model_usage_hourly (
    owner_type, owner_id, project_id, workspace_id, agent_cli,
    model_name, provider_id, bucket_hour,
    input_tokens, output_tokens, cache_creation_tokens, cache_read_tokens,
    interaction_count, estimated
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(owner_type, owner_id, project_id, workspace_id, agent_cli, model_name, provider_id, bucket_hour)
DO UPDATE SET
    input_tokens = model_usage_hourly.input_tokens + excluded.input_tokens,
    output_tokens = model_usage_hourly.output_tokens + excluded.output_tokens,
    cache_creation_tokens = model_usage_hourly.cache_creation_tokens + excluded.cache_creation_tokens,
    cache_read_tokens = model_usage_hourly.cache_read_tokens + excluded.cache_read_tokens,
    interaction_count = model_usage_hourly.interaction_count + excluded.interaction_count,
    estimated = CASE WHEN excluded.estimated = 1 THEN 1 ELSE model_usage_hourly.estimated END;

-- name: SumModelUsageByOwner :one
SELECT CAST(COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) AS BIGINT)
FROM model_usage_hourly
WHERE owner_type = ? AND owner_id = ? AND bucket_hour >= ? AND bucket_hour < ?;

-- name: SumModelUsageByProject :one
SELECT CAST(COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0) AS BIGINT)
FROM model_usage_hourly
WHERE project_id = ? AND bucket_hour >= ? AND bucket_hour < ?;

-- name: ListModelUsageByModel :many
SELECT model_name,
       CAST(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens) AS BIGINT) AS total_tokens,
       CAST(SUM(interaction_count) AS INTEGER) AS requests
FROM model_usage_hourly
WHERE owner_type = ? AND owner_id = ? AND bucket_hour >= ? AND bucket_hour < ?
GROUP BY model_name ORDER BY total_tokens DESC;

-- name: ListModelUsageByProvider :many
SELECT provider_id,
       CAST(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens) AS BIGINT) AS total_tokens,
       CAST(SUM(interaction_count) AS INTEGER) AS requests
FROM model_usage_hourly
WHERE owner_type = ? AND owner_id = ? AND bucket_hour >= ? AND bucket_hour < ?
GROUP BY provider_id ORDER BY total_tokens DESC;

-- name: ListModelUsageByProject :many
SELECT project_id,
       CAST(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens) AS BIGINT) AS total_tokens,
       CAST(SUM(interaction_count) AS INTEGER) AS requests
FROM model_usage_hourly
WHERE owner_type = ? AND owner_id = ? AND bucket_hour >= ? AND bucket_hour < ?
GROUP BY project_id ORDER BY total_tokens DESC;

-- name: ListModelUsageByWorkspace :many
SELECT workspace_id,
       CAST(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens) AS BIGINT) AS total_tokens,
       CAST(SUM(interaction_count) AS INTEGER) AS requests
FROM model_usage_hourly
WHERE owner_type = ? AND owner_id = ? AND bucket_hour >= ? AND bucket_hour < ?
GROUP BY workspace_id ORDER BY total_tokens DESC;

-- name: ListModelUsageByCLI :many
SELECT agent_cli,
       CAST(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens) AS BIGINT) AS total_tokens,
       CAST(SUM(interaction_count) AS INTEGER) AS requests
FROM model_usage_hourly
WHERE owner_type = ? AND owner_id = ? AND bucket_hour >= ? AND bucket_hour < ?
GROUP BY agent_cli ORDER BY total_tokens DESC;

-- name: GetModelBudget :one
SELECT * FROM model_budget_settings
WHERE owner_type = ? AND owner_id = ? AND project_id = ?;

-- name: UpsertModelBudget :one
INSERT INTO model_budget_settings (owner_type, owner_id, project_id, monthly_tokens, enabled)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(owner_type, owner_id, project_id) DO UPDATE SET
    monthly_tokens = excluded.monthly_tokens,
    enabled = excluded.enabled,
    updated_at = CURRENT_TIMESTAMP
RETURNING *;

-- name: DeleteModelBudget :execrows
DELETE FROM model_budget_settings
WHERE owner_type = ? AND owner_id = ? AND project_id = ?;

-- name: ListModelBudgetsForOwner :many
SELECT * FROM model_budget_settings
WHERE owner_type = ? AND owner_id = ? ORDER BY project_id ASC;
```

- [ ] **Step 5: 内置模型种子**

```go
// server/internal/service/model_seed.go
package service

import (
	"context"
	"database/sql"
	"errors"

	"github.com/niuniu-dev/niuniu/internal/store"
)

var builtinModels = []string{
	"glm-5.1", "glm-5-turbo", "glm-4.5-air",
	"deepseek-v4-pro", "deepseek-v4-flash",
	"qwen3.6-plus",
	"kimi-k2.6",
	"MiniMax-M2.7",
}

func SeedBuiltinModels(ctx context.Context, q *store.Queries) error {
	for _, name := range builtinModels {
		existing, err := q.GetModelByName(ctx, store.GetModelByNameParams{
			OwnerType: "user", OwnerID: 0, Name: name,
		})
		if err == nil && existing.ID > 0 {
			continue
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := q.CreateModel(ctx, store.CreateModelParams{
			OwnerType: "user", OwnerID: 0, Name: name,
			DisplayName: name, Enabled: 1, Source: "builtin",
		}); err != nil {
			return err
		}
	}
	return nil
}
```

`server.go` 在 `EnvProviderService.SeedDefaults` 之后调用 `service.SeedBuiltinModels`。

- [ ] **Step 6: 运行测试并提交**

```bash
cd server && go test ./internal/store ./internal/service -run 'ModelsSchema|SeedBuiltinModels' -v
cd .. && make schema-diff && make sqlc
git add server/internal/store server/internal/service/model_seed.go server/internal/service/model_seed_test.go
git commit -m "feat(models): add model library usage and budget schema"
```

---

## Task 2: 模型库服务

**Files:**
- Create: `server/internal/service/model_library.go`
- Test: `server/internal/service/model_library_test.go`

**Interfaces:**
- Produces: `ModelDTO`、`ModelLibraryService.List/Upsert/Update/Delete`。
- Consumes: `*store.Queries`。

- [ ] **Step 1: 写测试**

```go
func TestModelLibraryMergesSystemAndOwner(t *testing.T) {
	svc := newTestModelLibrary(t)
	ctx := context.Background()
	owner := OwnerRef{Type: "user", ID: 1}

	_, err := svc.Upsert(ctx, owner, "glm-5.1", "GLM 5.1", true)
	require.NoError(t, err)
	_, err = svc.Upsert(ctx, owner, "my-private-model", "", true)
	require.NoError(t, err)

	list, err := svc.List(ctx, owner, false)
	require.NoError(t, err)
	require.Contains(t, names(list), "glm-5.1")
	require.Contains(t, names(list), "my-private-model")

	var privateID int64
	for _, m := range list {
		if m.Name == "my-private-model" {
			privateID = m.ID
		}
	}
	require.NotZero(t, privateID)
	_, err = svc.Update(ctx, owner, privateID, ptr("私有模型"), ptr(false))
	require.NoError(t, err)
	list, err = svc.List(ctx, owner, false)
	require.NotContains(t, names(list), "my-private-model")
}

func TestModelLibraryCannotDeleteSystemModel(t *testing.T) {
	svc := newTestModelLibrary(t)
	owner := OwnerRef{Type: "user", ID: 1}
	list, err := svc.List(context.Background(), owner, true)
	require.NoError(t, err)
	var systemID int64
	for _, m := range list {
		if m.OwnerID == 0 {
			systemID = m.ID
			break
		}
	}
	require.NotZero(t, systemID)
	require.ErrorIs(t, svc.Delete(context.Background(), owner, systemID), ErrSystemModelImmutable)
}
```

- [ ] **Step 2: 实现**

```go
type ModelDTO struct {
	ID          int64  `json:"id"`
	OwnerType   string `json:"owner_type"`
	OwnerID     int64  `json:"owner_id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Enabled     bool   `json:"enabled"`
	Source      string `json:"source"`
}

var ErrSystemModelImmutable = errors.New("system model cannot be modified")

type ModelLibraryService struct {
	q     *store.Queries
	authz *Authz
}

func (s *ModelLibraryService) List(ctx context.Context, owner OwnerRef, includeDisabled bool) ([]ModelDTO, error)
func (s *ModelLibraryService) Upsert(ctx context.Context, owner OwnerRef, name, displayName string, enabled bool) (ModelDTO, error)
func (s *ModelLibraryService) Update(ctx context.Context, owner OwnerRef, id int64, displayName *string, enabled *bool) (ModelDTO, error)
func (s *ModelLibraryService) Delete(ctx context.Context, owner OwnerRef, id int64) error
func (s *ModelLibraryService) Import(ctx context.Context, owner OwnerRef, names []string) (added, skipped int, err error)
```

行为：

- `List` 取系统行 + owner 行；同名时 owner 行覆盖 `DisplayName`/`Enabled`；`includeDisabled=false` 过滤掉禁用。
- `Upsert` 查 owner 行：存在则更新；不存在则创建 `source='manual'`。
- `Upsert` 同名 `enabled=false` 会创建/更新 owner 覆盖行，从而隐藏系统行；这是“禁用系统模型”的唯一方式。
- `Update` 只允许 owner 行；系统行返回 `ErrSystemModelImmutable`。
- `Delete` 只删 owner 行；系统行返回 `ErrSystemModelImmutable`。

- [ ] **Step 3: 运行测试并提交**

```bash
cd server && go test ./internal/service -run TestModelLibrary -v
git add server/internal/service/model_library.go server/internal/service/model_library_test.go
git commit -m "feat(models): add shared model library service"
```

---

## Task 3: Provider 模型刷新与导入

**Files:**
- Create: `server/internal/service/model_refresh.go`
- Test: `server/internal/service/model_refresh_test.go`

**Interfaces:**
- Produces: `ModelCandidate`、`ModelRefreshService.RefreshPreview`。
- 导入复用 Task 2 的 `ModelLibraryService.Import`。
- Consumes: `*store.Queries`、`*EnvProviderService`（或直接读 `env_providers` + accounts）。

- [ ] **Step 1: 写测试**

```go
func TestRefreshPreviewTriesOpenAIThenCodexThenAnthropic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/openai/models" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if r.URL.Path == "/codex/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"glm-5.1"},{"id":"glm-4.5-air"}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	svc := newTestRefresh(t, srv.URL)
	got, err := svc.RefreshPreview(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, []string{"glm-4.5-air", "glm-5.1"}, got)
}

func TestImportSkipsExisting(t *testing.T) {
	svc := newTestModelLibrary(t)
	_, err := svc.Upsert(context.Background(), OwnerRef{"user", 1}, "glm-5.1", "", true)
	require.NoError(t, err)
	added, skipped, err := svc.Import(context.Background(), OwnerRef{"user", 1}, []string{"glm-5.1", "new-model"})
	require.NoError(t, err)
	require.Equal(t, 1, added)
	require.Equal(t, 1, skipped)
}
```

- [ ] **Step 2: 实现刷新**

```go
type ModelCandidate struct {
	Name             string `json:"name"`
	AlreadyInLibrary bool   `json:"already_in_library"`
}

type ModelRefreshService struct {
	q      *store.Queries
	client *http.Client
}

func (s *ModelRefreshService) RefreshPreview(ctx context.Context, providerID int64) ([]ModelCandidate, error)
```

行为：

- 加载 provider；按 `openai → codex → anthropic` 顺序解析 base_url + key。
- `GET {base}/models`，超时 15s；Bearer（anthropic 额外 `x-api-key` + `anthropic-version: 2023-06-01`）。
- 解析 `data[].id` / `models[].id` / `data[].name`；去重排序。
- 404/405 → 下一个协议；全部失败返回 `ErrModelsEndpointUnsupported`。
- `Import` 调 `ModelLibraryService.Upsert`；已存在计 skipped。

- [ ] **Step 3: 运行测试并提交**

```bash
cd server && go test ./internal/service -run 'RefreshPreview|Import' -v
git add server/internal/service/model_refresh.go server/internal/service/model_refresh_test.go
git commit -m "feat(models): add provider model refresh and import"
```

---

## Task 4: 用量写入与聚合

**Files:**
- Modify: `server/internal/agentproxy/proxy.go`
- Modify: `server/internal/agentproxy/omp_exec.go`
- Modify: `server/internal/agentproxy/goose_exec.go`
- Create: `server/internal/agentproxy/model_usage.go`
- Test: `server/internal/agentproxy/model_usage_test.go`

**Interfaces:**
- Produces: `recordModelUsage(ctx, q, params)`；`WorkspaceSession.projectID`。
- Consumes: `store.UpsertModelUsageHourly`、`store.GetProjectIDForWorkspace`。

- [ ] **Step 1: 写测试**

```go
func TestRecordModelUsage(t *testing.T) {
	q := newTestQueries(t)
	ctx := context.Background()
	recordModelUsage(ctx, q, modelUsageParams{
		OwnerType: "user", OwnerID: 1, ProjectID: 2, WorkspaceID: 3,
		AgentCLI: "claude", ModelName: "glm-5.1", ProviderID: 4,
		InputTokens: 100, OutputTokens: 20, CacheReadTokens: 5,
		InteractionCount: 1, At: time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC),
	})
	total, err := q.SumModelUsageByOwner(ctx, store.SumModelUsageByOwnerParams{
		OwnerType: "user", OwnerID: 1,
		BucketHour:   time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		BucketHour_2: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.EqualValues(t, 125, total)
}
```

- [ ] **Step 2: 实现**

```go
type modelUsageParams struct {
	OwnerType                              string
	OwnerID, ProjectID, WorkspaceID        int64
	AgentCLI, ModelName                    string
	ProviderID                             int64
	InputTokens, OutputTokens              int
	CacheCreationTokens, CacheReadTokens   int
	InteractionCount                       int
	Estimated                              bool
	At                                     time.Time
}

func recordModelUsage(ctx context.Context, q *store.Queries, p modelUsageParams) {
	if q == nil || p.OwnerID <= 0 || p.InteractionCount <= 0 {
		return
	}
	estimated := p.Estimated
	if p.InputTokens == 0 && p.OutputTokens == 0 &&
		p.CacheCreationTokens == 0 && p.CacheReadTokens == 0 {
		estimated = true
	}
	if err := q.UpsertModelUsageHourly(ctx, store.UpsertModelUsageHourlyParams{
		OwnerType:           p.OwnerType,
		OwnerID:             p.OwnerID,
		ProjectID:           p.ProjectID,
		WorkspaceID:         p.WorkspaceID,
		AgentCli:            p.AgentCLI,
		ModelName:           p.ModelName,
		ProviderID:          p.ProviderID,
		BucketHour:          p.At.UTC().Truncate(time.Hour),
		InputTokens:         int64(p.InputTokens),
		OutputTokens:        int64(p.OutputTokens),
		CacheCreationTokens: int64(p.CacheCreationTokens),
		CacheReadTokens:     int64(p.CacheReadTokens),
		InteractionCount:    int64(p.InteractionCount),
		Estimated:           boolToInt64(estimated),
	}); err != nil {
		slog.Warn("recordModelUsage failed", "workspace_id", p.WorkspaceID, "model", p.ModelName, "error", err)
	}
}

func boolToInt64(v bool) int64 {
	if v {
		return 1
	}
	return 0
}
```

- 新增 `server/internal/service/model_usage.go`：

```go
type ModelUsageSummary struct {
	TotalTokens int64            `json:"total_tokens"`
	Requests    int64            `json:"requests"`
	ByModel     []ModelUsageRow  `json:"by_model,omitempty"`
	ByProvider  []ModelUsageRow  `json:"by_provider,omitempty"`
	ByProject   []ModelUsageRow  `json:"by_project,omitempty"`
	ByWorkspace []ModelUsageRow  `json:"by_workspace,omitempty"`
	ByCLI       []ModelUsageRow  `json:"by_cli,omitempty"`
}

type ModelUsageRow struct {
	Key         string `json:"key"`
	TotalTokens int64  `json:"total_tokens"`
	Requests    int64  `json:"requests"`
}

type ModelUsageService struct{ q *store.Queries }

func (s *ModelUsageService) Summary(ctx context.Context, owner OwnerRef, from, to time.Time, groupBy string) (ModelUsageSummary, error)
```

`Summary` 用 `SumModelUsageByOwner` + 对应维度的 `ListModelUsageBy*`；`groupBy` 为空时只返回总量。

- 在 `WorkspaceSession` 增加 `projectID int64`；`GetOrStartSession` 初始化时调用 `q.GetProjectIDForWorkspace`，失败记 0。
- 在 `proxy.go`、`omp_exec.go`、`goose_exec.go` 的 `recordProviderTokens` 调用点后调用 `recordModelUsage`，传入 `s.workspaceID`、`s.projectID`、`s.cliType`、`s.modelName`、`usageProviderID`。
- `s.modelName` 为空时，用 `usageProviderID` 查 `env_providers` 的 `model` / `codex_model`；仍为空写 `unknown`。
- 三个目标 CLI（Claude/Codex/Qwen）都走 `proxy.go` 的结果事件，已覆盖。

- [ ] **Step 3: 运行测试并提交**

```bash
cd server && go test ./internal/agentproxy -run TestRecordModelUsage -v
git add server/internal/agentproxy
git commit -m "feat(models): record per-model hourly usage"
```

---

## Task 5: 预算服务与拦截

**Files:**
- Create: `server/internal/service/model_budget.go`
- Create: `server/internal/agentproxy/model_budget.go`
- Modify: `server/internal/agentproxy/proxy.go`
- Modify: `server/internal/notify/types.go`
- Modify: `server/internal/server/server.go`
- Test: `server/internal/service/model_budget_test.go`

**Interfaces:**
- Produces: `agentproxy.ModelBudgetChecker`、`BudgetDecision`、`ErrModelBudgetExceeded`。
- Produces: `service.ModelBudgetService.Check/Get/Set/SetProject/DeleteProject`。

- [ ] **Step 1: 写测试**

```go
func TestBudgetCheckOwnerHardCap(t *testing.T) {
	svc := newTestBudget(t, 1000, 900)
	dec, err := svc.Check(context.Background(), "user", 1, 0)
	require.NoError(t, err)
	require.False(t, dec.Allowed)
	require.True(t, dec.OwnerExceeded)
	require.EqualValues(t, 900, dec.UsedTokens)
}

func TestBudgetCheckProjectSoftCap(t *testing.T) {
	svc := newTestBudget(t, 1000, 100, 50)
	dec, err := svc.Check(context.Background(), "user", 1, 2)
	require.NoError(t, err)
	require.True(t, dec.Allowed)
	require.True(t, dec.ProjectWarning)
}
```

- [ ] **Step 2: 实现**

```go
// agentproxy/model_budget.go
package agentproxy

var ErrModelBudgetExceeded = errors.New("model token budget exceeded")

type BudgetDecision struct {
	Allowed        bool
	OwnerExceeded  bool
	ProjectWarning bool
	UsedTokens     int64
	LimitTokens    int64
	Period         string
}

type ModelBudgetChecker interface {
	Check(ctx context.Context, ownerType string, ownerID, projectID int64) (BudgetDecision, error)
}
```

`service.ModelBudgetService`：

```go
type ModelBudgetService struct {
	q        *store.Queries
	hub      *notify.NotificationHub
	mu       sync.Mutex
	notified map[string]bool // ownerType:ownerID:period
}

func (s *ModelBudgetService) Check(ctx context.Context, ownerType string, ownerID, projectID int64) (agentproxy.BudgetDecision, error) {
	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	dec := agentproxy.BudgetDecision{Allowed: true, Period: start.Format("2006-01")}

	ownerRow, err := s.q.GetModelBudget(ctx, store.GetModelBudgetParams{
		OwnerType: ownerType, OwnerID: ownerID, ProjectID: 0,
	})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		slog.Warn("model budget: owner lookup failed (fail-open)", "error", err)
		return dec, nil
	}
	if err == nil && ownerRow.Enabled != 0 && ownerRow.MonthlyTokens > 0 {
		used, uerr := s.q.SumModelUsageByOwner(ctx, store.SumModelUsageByOwnerParams{
			OwnerType: ownerType, OwnerID: ownerID, BucketHour: start, BucketHour_2: end,
		})
		if uerr != nil {
			slog.Warn("model budget: owner usage failed (fail-open)", "error", uerr)
			return dec, nil
		}
		dec.UsedTokens, dec.LimitTokens = used, ownerRow.MonthlyTokens
		if used >= ownerRow.MonthlyTokens {
			dec.Allowed, dec.OwnerExceeded = false, true
			s.notifyOnce(ownerType, ownerID, dec.Period, used, ownerRow.MonthlyTokens)
		}
	}

	if projectID > 0 {
		projectRow, perr := s.q.GetModelBudget(ctx, store.GetModelBudgetParams{
			OwnerType: ownerType, OwnerID: ownerID, ProjectID: projectID,
		})
		if perr == nil && projectRow.Enabled != 0 && projectRow.MonthlyTokens > 0 {
			used, uerr := s.q.SumModelUsageByProject(ctx, store.SumModelUsageByProjectParams{
				ProjectID: projectID, BucketHour: start, BucketHour_2: end,
			})
			if uerr == nil && used >= projectRow.MonthlyTokens {
				dec.ProjectWarning = true
				s.notifyProjectOnce(ownerType, ownerID, projectID, dec.Period, used, projectRow.MonthlyTokens)
			}
		}
	}
	return dec, nil
}
```

- `Set(ctx, owner, monthlyTokens)`、`SetProject(ctx, owner, projectID, monthlyTokens)`、`DeleteProject` 调 `UpsertModelBudget` / `DeleteModelBudget`。
- `notifyOnce`：`hub.Broadcast(notify.Notification{Topic: notify.TopicModelBudget, Action: "exceeded", OwnerType: ownerType, OwnerID: ownerID, Extra: map[string]any{"period": period, "used": used, "limit": limit}})`；`notified` map 去重。
- `notifyProjectOnce`：同 topic，`Action: "project_warning"`，`Extra` 带 `project_id`。

`AgentProxy`：

- 增加 `budgetChecker ModelBudgetChecker` 字段 + `SetModelBudgetChecker`；`nil` 表示未启用预算，所有请求放行。
- `GetOrStartSession`：加载 `ws` 后、创建 session 前调用 `Check`；`!Allowed` 返回 `ErrModelBudgetExceeded`（带已用/上限）。
- `SendLoop`：在 `s.Send(...)` 之前调用 `Check`；`!Allowed` 时 `s.emitAutohostSystemInfo(ctx, "本月模型 token 已用尽…")` + `s.finalizeSendLoopTurn(ctx, true, reason, false)` + `return`。

`notify/types.go` 增加 `TopicModelBudget = "model_budget"`。

`server.go` 构造 `ModelBudgetService` 并 `agentProxy.SetModelBudgetChecker(...)`。

- [ ] **Step 3: 运行测试并提交**

```bash
cd server && go test ./internal/service ./internal/agentproxy -run 'Budget' -v
git add server/internal/service/model_budget.go server/internal/service/model_budget_test.go \
  server/internal/agentproxy/model_budget.go server/internal/agentproxy/proxy.go \
  server/internal/notify/types.go server/internal/server/server.go
git commit -m "feat(models): enforce owner token budget"
```

---

## Task 6: HTTP API

**Files:**
- Create: `server/internal/api/models.go`
- Modify: `server/internal/server/router.go`
- Modify: `server/internal/server/server.go`
- Test: `server/internal/api/models_test.go`

**Interfaces:**
- Produces: `/api/models`、`/api/models/refresh-preview`、`/api/models/import`、`/api/model-usage/summary`、`/api/model-budget`、`/api/model-budget/projects/:id`。

- [ ] **Step 1: 写 handler 测试**

覆盖：列表含系统 + owner；新增/更新/删除；系统模型删除 400；刷新预览 200；导入计数；用量汇总；预算设置与读取；跨 owner 403。

- [ ] **Step 2: 实现 handler**

```go
type ModelHandler struct {
	library *service.ModelLibraryService
	refresh *service.ModelRefreshService
	usage   *service.ModelUsageService
	budget  *service.ModelBudgetService
	authz   *service.Authz
}
```

- `ownerFromRequest(c)`：沿用 env provider 的 owner 解析方式；默认当前 user。
- 写操作校验 owner 权限。
- `GET /api/models?all=1` 返回禁用行。
- `GET /api/model-usage/summary?from=&to=&group_by=` 返回 `total_tokens/requests` + 维度分组；`group_by` 取 `model|provider|project|workspace|cli`（空 = 只返回总量）。
- 预算：`project_id=0` 为 owner 硬上限；`projects/:id` 为软上限。

- [ ] **Step 3: 路由与接线**

`router.go` 注册：

```go
api.GET("/models", s.modelHandler.List)
api.POST("/models", s.modelHandler.Upsert)
api.PATCH("/models/:id", s.modelHandler.Update)
api.DELETE("/models/:id", s.modelHandler.Delete)
api.POST("/models/refresh-preview", s.modelHandler.RefreshPreview)
api.POST("/models/import", s.modelHandler.Import)
api.GET("/model-usage/summary", s.modelHandler.UsageSummary)
api.GET("/model-budget", s.modelHandler.GetBudget)
api.PUT("/model-budget", s.modelHandler.SetBudget)
api.PUT("/model-budget/projects/:id", s.modelHandler.SetProjectBudget)
api.DELETE("/model-budget/projects/:id", s.modelHandler.DeleteProjectBudget)
```

- [ ] **Step 4: 运行测试并提交**

```bash
cd server && go test ./internal/api -run TestModel -v
git add server/internal/api/models.go server/internal/api/models_test.go \
  server/internal/server/router.go server/internal/server/server.go
git commit -m "feat(models): add model library usage and budget api"
```

---

## Task 7: MCP 工具

**Files:**
- Create: `server/cmd/niuniu-mcp/model_tools.go`
- Modify: `server/cmd/niuniu-mcp/main.go`
- Test: `server/cmd/niuniu-mcp/model_tools_test.go`

**Interfaces:**
- Produces: `model_list`、`model_create`、`model_update`、`model_delete`、`model_refresh_preview`、`model_import`、`model_usage_summary`、`model_budget_get`、`model_budget_set`、`model_budget_set_project`。

- [ ] **Step 1: 实现工具**

- `model_create`：`name` 必填，`display_name` 可选，`enabled` 默认 true。
- `model_refresh_preview`：`provider_id` 必填；返回候选与 `already_in_library`。
- `model_import`：`names` 数组；返回 added/skipped。
- `model_usage_summary`：`from`/`to`/`group_by`。
- `model_budget_set`：`monthly_tokens`；0 = 不限。
- 所有工具复用项目 API，错误码透传。

- [ ] **Step 2: 测试并提交**

```bash
cd server && go test ./cmd/niuniu-mcp -run TestModel -v
git add server/cmd/niuniu-mcp/model_tools.go server/cmd/niuniu-mcp/model_tools_test.go \
  server/cmd/niuniu-mcp/main.go
git commit -m "feat(models): add model library mcp tools"
```

---

## Task 8: 设置页：模型库 + 预算

**Files:**
- Create: `server/web/src/pages/settings/model-settings.tsx`
- Create: `server/web/src/lib/model-api.ts`
- Create: `server/web/src/types/model.ts`
- Modify: `server/web/src/pages/settings/index.tsx`
- Modify: `server/web/src/i18n/locales/zh-CN/settings.json`
- Modify: `server/web/src/i18n/locales/en/settings.json`
- Test: `server/web/src/pages/settings/model-settings.test.tsx`

**Interfaces:**
- Produces: 设置 → 模型 tab，包含模型库与预算两块。

- [ ] **Step 1: API 客户端与类型**

```ts
// types/model.ts
export interface ModelItem {
  id: number; owner_type: string; owner_id: number;
  name: string; display_name: string; enabled: boolean; source: string;
}
export interface ModelBudget {
  owner_type: string; owner_id: number; monthly_tokens: number;
  used_tokens: number; period: string;
  projects: { project_id: number; monthly_tokens: number; used_tokens: number }[];
}
```

`model-api.ts`：`listModels(includeDisabled)`、`upsertModel`、`updateModel`、`deleteModel`、`refreshPreview`、`importModels`、`getBudget`、`setBudget`、`setProjectBudget`、`deleteProjectBudget`。

- [ ] **Step 2: 页面**

- 模型库卡片：搜索、添加、列表（名称/显示名/启用/来源弱化）、编辑/禁用/删除。
- 系统行（`owner_id=0`）不显示编辑/删除；显示“隐藏”，调用 `upsertModel(name, displayName, false)` 创建 owner 覆盖行。
- 预算卡片：owner 月度 token 输入 + 进度条；项目软上限列表。
- 遵循 `docs/design-system.md`：shadcn、lucide、`t()`、warm token、Dark mode。
- 空状态、加载、错误。

- [ ] **Step 3: 注册 tab 与 i18n**

`settings/index.tsx` 增加 `'models'` 到 `SettingsTab`、tabs 数组、渲染分支；i18n 增加 `tabs.models` 和页面文案。

- [ ] **Step 4: 测试并提交**

```bash
cd server/web && pnpm lint && pnpm test:run -t "ModelSettings"
git add server/web/src/pages/settings/model-settings.tsx server/web/src/pages/settings/model-settings.test.tsx \
  server/web/src/pages/settings/index.tsx server/web/src/lib/model-api.ts server/web/src/types/model.ts \
  server/web/src/i18n/locales
git commit -m "feat(models): add model library and budget settings"
```

---

## Task 9: Provider 表单模型选择与刷新

**Files:**
- Create: `server/web/src/components/models/model-input.tsx`
- Create: `server/web/src/components/models/model-refresh-dialog.tsx`
- Modify: `server/web/src/pages/settings/env-settings.tsx`
- Modify: i18n
- Test: `server/web/src/components/models/model-input.test.tsx`

**Interfaces:**
- Produces: `ModelInput`（datalist 下拉 + 手输 + 加入模型库）、`ModelRefreshDialog`（预览勾选导入）。

- [ ] **Step 1: ModelInput**

```tsx
export function ModelInput({ value, onChange, models, onAdd }: {
  value: string
  onChange: (v: string) => void
  models: string[]
  onAdd?: (name: string) => void
}) {
  const id = useId()
  const missing = value.trim() !== '' && !models.includes(value.trim())
  return (
    <div className="flex gap-1">
      <Input list={id} value={value} onChange={(e) => onChange(e.target.value)} />
      <datalist id={id}>
        {models.map((m) => <option key={m} value={m} />)}
      </datalist>
      {missing && onAdd ? (
        <Button type="button" variant="outline" size="sm" onClick={() => onAdd(value.trim())}>
          {t('models.addToLibrary')}
        </Button>
      ) : null}
    </div>
  )
}
```

- [ ] **Step 2: 替换 provider 模型字段**

`env-settings.tsx` 的 6 个模型 `Input` 改为 `ModelInput`；从 `model-api.listModels(false)` 取列表；`onAdd` 调 `upsertModel` 并刷新。

- [ ] **Step 3: 刷新对话框**

- provider 编辑对话框内加“刷新模型”按钮。
- 打开 `ModelRefreshDialog`，调 `refreshPreview(providerId)`；checkbox 列表；确认调 `importModels`。
- 失败显示 `t('models.refreshUnsupported')` 等错误。

- [ ] **Step 4: 测试并提交**

```bash
cd server/web && pnpm lint && pnpm test:run -t "ModelInput"
git add server/web/src/components/models server/web/src/pages/settings/env-settings.tsx \
  server/web/src/i18n/locales
git commit -m "feat(models): add model picker and provider refresh"
```

---

## Task 10: 用量视图、Workspace 预算状态与通知

**Files:**
- Modify: `server/web/src/pages/settings/model-settings.tsx`（增加用量区）
- Modify: `server/web/src/pages/workspaces/panels/workspace-settings-dialog.tsx`
- Modify: `server/web/src/lib/notification-dispatcher.ts`
- Modify: i18n
- Test: `server/web/src/pages/settings/model-settings.test.tsx`
- Test: `server/web/src/lib/notification-dispatcher.test.ts`

**Interfaces:**
- Produces: 用量卡片（owner/项目/模型/CLI/provider）、workspace 预算状态、`model_budget` toast。

- [ ] **Step 1: 用量区**

- 时间范围选择（本月/上月/自定义）。
- 总量 + 按模型/CLI/provider/项目分组表。
- 使用 `model-api.usageSummary`。

- [ ] **Step 2: Workspace 状态**

`workspace-settings-dialog.tsx` 增加“模型与预算”行：当前 provider（已有数据）、当前模型（provider model）、本月 token、owner 预算进度；数据来自 `usageSummary` + `getBudget`。只读。

- [ ] **Step 3: 通知**

`notification-dispatcher.ts` 增加：

```ts
if (n.topic === 'model_budget') {
  toast.warning(t('models.budgetExceededToast', { used: ..., limit: ... }))
  return
}
```

同一事件只提示一次（按 `n.extra.period` 去重）。

- [ ] **Step 4: 测试并提交**

```bash
cd server/web && pnpm lint && pnpm test:run -t "ModelUsage|model_budget"
git add server/web/src/pages/settings/model-settings.tsx \
  server/web/src/pages/workspaces/panels/workspace-settings-dialog.tsx \
  server/web/src/lib/notification-dispatcher.ts server/web/src/i18n/locales
git commit -m "feat(models): add usage views budget status and notifications"
```

---

## Task 11: E2E 与运维文档

**Files:**
- Create: `server/internal/service/model_library_e2e_test.go`
- Create: `docs/operations/shared-model-library-runbook.md`

- [ ] **Step 1: E2E 场景**

1. 新增模型 → provider 下拉可选 → 保存 → `sceneenv.Resolve` 输出正确 env（Claude/Codex/Qwen）。
2. 手输新模型 → 加入模型库 → 下次下拉出现。
3. 刷新预览 → 勾选导入 → 下拉出现；不支持时提示手动输入。
4. Claude / Codex / Qwen 各跑一轮 → `model_usage_hourly` 出现 owner/project/workspace/CLI/model/provider 维度。
5. owner 预算超限 → 新会话被拒 + 通知；调高后恢复。
6. 项目软上限 → 允许执行 + 告警。
7. 不配置模型库时，现有 provider/env 行为与改动前一致。
8. 无新增监听端口/代理进程。

- [ ] **Step 2: 全量验证**

```bash
cd server && go test ./internal/store ./internal/service ./internal/agentproxy ./internal/api -race
cd .. && make schema-diff && make sqlc
cd server/web && pnpm lint && pnpm test:run && pnpm build
```

- [ ] **Step 3: 运维文档**

`docs/operations/shared-model-library-runbook.md`：模型库维护、刷新排错、预算设置、超限恢复、用量口径、团队版 owner 说明、回滚。

- [ ] **Step 4: 提交**

```bash
git add server/internal/service/model_library_e2e_test.go docs/operations/shared-model-library-runbook.md
git commit -m "test(models): add end-to-end coverage and runbook"
```

---

## Self-Review

**设计覆盖：**

| 设计章节 | 任务 |
|---|---|
| §5 数据模型 | 1 |
| §6 模型库行为 | 2、3 |
| §7 Provider 表单 | 9 |
| §8 运行时/用量/预算 | 4、5 |
| §9 用量聚合 | 4、6、10 |
| §10 UI | 8、9、10 |
| §11 API/MCP | 6、7 |
| §12 错误处理 | 2、3、5、8 |
| §13 安全与隐私 | 6 |
| §14 团队版边界 | 1（owner 作用域）、11（文档） |
| §15 测试与验收 | 各任务 + 11 |

**延迟实现扫描：**

- 全文无 `TBD` / `TODO` / “后续阶段” / “先用 X 后替换”。
- 11 个任务全部必须完成，不按阶段裁剪。

**类型一致性：**

- `ModelDTO`、`ModelLibraryService` 贯穿 2/3/6/7/8。
- `modelUsageParams`、`recordModelUsage` 贯穿 4/5。
- `ModelBudgetChecker`、`BudgetDecision` 贯穿 5。
- `ModelItem`、`ModelBudget`、`model-api` 贯穿 8/9/10。
