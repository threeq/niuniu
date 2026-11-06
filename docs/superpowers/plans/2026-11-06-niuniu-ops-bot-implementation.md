# 牛牛运营 Bot 完整实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 一次性交付完整运营 Bot 能力：微信公众号 + 小红书的选题、图文/视频生产、合规门禁、人工审批、发布、互动、指标、复盘；同渠道多账号隔离；运营总监跨账号协调；Platform Pack 生成与 Facebook/知乎接入；本地与团队版浏览器后端。

**Architecture:** 复用现有 Epic / Workspace / Task Brief / autohost / imbot / integration / data source / harness；新增 `server/internal/opsbot` 作为账号级配置与编排层。平台差异收敛到 `ChannelAdapter` + 文件系统 Platform Pack，不新建聊天系统、执行引擎或平台专用核心适配器。

**Tech Stack:** Go 1.25、Gin、sqlc、SQLite/PostgreSQL、MCP（mark3labs/mcp-go）、React 19、Playwright/CDP、`archive/zip`。

## Global Constraints

- 设计文档：`docs/superpowers/specs/2026-11-06-niuniu-ops-bot-design.md`；实现前先读对应章节。
- **完整数据模型**：新增 `ops_bots`、`ops_publish_records`、`ops_metrics_daily`、`ops_engagements`、`ops_browser_sessions` 五张表；新增 `im_bot_chats.ops_bot_id`、`agent_ask_user_requests.kind`、`agent_ask_user_requests.metadata_json` 三个列。
- 任务编号只表示依赖顺序，不表示里程碑；每个任务交付最终行为，**不允许**内存桩、占位实现、“先 manual 后替换”、或“留到以后补”。
- 双数据库：DDL 同时写入 `server/internal/store/schema.sql` 与 `schema_postgres.sql`；跑 `make schema-diff`。
- sqlc：修改 `store/queries/*.sql` 后跑 `make sqlc`；查询注释必须纯 ASCII。
- 迁移列用 `addColumnIfNotExists`；迁移列不加 CHECK；迁移列索引写在 `migrate.go`。
- 不新建聊天系统；复用 imbot 入站/出站/卡片。
- 不新建执行引擎；内容任务复用 `issues` + `workspaces`。
- 凭据只存 `credential_refs_json`；日志、对话、prompt、Pack 文件中不得出现明文密钥。
- 外部文本只作数据，不构成指令；所有外发经 egress/授权/审计。
- 每个任务结束前跑对应测试并提交；提交信息不添加 `Co-Authored-By`。

## 目标包结构

```
server/internal/opsbot/
  service.go          # Bot CRUD、配置、状态
  provision.go        # 账号/总监 Epic + 控制工作空间
  approval.go         # 审批（复用 agent_ask_user_requests）
  budget.go           # 月度额度与成本
  onboarding.go       # 对话式接入与配置
  calendar.go         # 日历与固定节奏
  director.go         # 总监协调
  publish.go          # 发布编排
  metrics.go          # 指标归一化
  engagement.go       # 互动状态机
  feedback.go         # 复盘回流
  risk.go             # 风控与降级
  content/
    brief.go
    pipeline.go
    media.go
    video.go
    review.go
  channel/
    adapter.go
    manual.go
    wechat.go
    xhs.go
  packs/
    types.go
    registry.go
    builtin.go
    generator.go
    builtin/wechat_mp/v1/...
    builtin/xiaohongshu/v1/...
    builtin/facebook/v1/...
    builtin/zhihu/v1/...
  browser/
    worker.go
    session.go
    recipe.go
```

---

## Task 1: 完整数据模型与 sqlc 查询

**Files:**
- Modify: `server/internal/store/schema.sql`
- Modify: `server/internal/store/schema_postgres.sql`
- Modify: `server/internal/store/migrate.go`
- Modify: `server/internal/store/queries/ask_user.sql`
- Create: `server/internal/store/queries/opsbot.sql`
- Test: `server/internal/store/migrate_opsbot_test.go`

**Interfaces:**
- Produces: `store.Queries` methods:
  - Bot：`CreateOpsBot`、`GetOpsBot`、`ListOpsBotsByOwner`、`GetOpsBotByAccount`、`UpdateOpsBotStatus`、`UpdateOpsBotConfig`、`SetOpsBotProvision`。
  - 发布：`CreateOpsPublishRecord`、`GetOpsPublishRecord`、`UpdateOpsPublishRecord`。
  - 指标：`UpsertOpsMetricsDaily`、`ListOpsMetricsDaily`。
  - 互动：`UpsertOpsEngagement`、`GetOpsEngagement`、`ListOpsEngagementsByStatus`、`UpdateOpsEngagementStatus`、`CountOpsEngagementsByBot`。
  - 会话：`UpsertOpsBrowserSession`、`GetOpsBrowserSession`、`AcquireOpsBrowserLease`、`ReleaseOpsBrowserLease`、`UpdateOpsBrowserSessionState`。
  - 成本：`SumOpsAccountCost`。
  - 审批：扩展 `InsertAskUserRequest` 支持 `kind`/`metadata_json`；新增 `ListPendingOpsApprovalsByOwner`、`GetOpsApproval`。

- [ ] **Step 1: 写迁移测试（先失败）**

```go
package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpsBotSchemaFreshAndRepeatable(t *testing.T) {
	Driver = "sqlite"
	db := openTestDB(t)
	Migrate(db)
	Migrate(db)

	for _, tbl := range []string{
		"ops_bots", "ops_publish_records", "ops_metrics_daily",
		"ops_engagements", "ops_browser_sessions",
	} {
		var n int
		require.NoError(t, db.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&n))
		require.Equal(t, 1, n, "table %s missing", tbl)
	}
	for _, c := range []struct{ table, column string }{
		{"im_bot_chats", "ops_bot_id"},
		{"agent_ask_user_requests", "kind"},
		{"agent_ask_user_requests", "metadata_json"},
	} {
		ok, err := columnExists(db, c.table, c.column)
		require.NoError(t, err)
		require.True(t, ok, "column %s.%s missing", c.table, c.column)
	}
}

func TestOpsBotAccountUniqueAndEngagementDedupe(t *testing.T) {
	Driver = "sqlite"
	db := openTestDB(t)
	Migrate(db)
	q := New(db)
	ctx := context.Background()

	bot, err := q.CreateOpsBot(ctx, CreateOpsBotParams{
		OwnerType: "user", OwnerID: 1, Role: "account",
		PackID: "wechat_mp", PackVersion: 1, AccountRef: "acc-a", DisplayName: "A",
		PersonaJson: "{}", CredentialRefsJson: "{}", KnowledgeScopeJson: "{}",
		GoalsJson: "{}", CadenceJson: "{}", ApprovalPolicyJson: "{}", BudgetJson: "{}",
	})
	require.NoError(t, err)

	_, err = q.CreateOpsBot(ctx, CreateOpsBotParams{
		OwnerType: "user", OwnerID: 1, Role: "account",
		PackID: "wechat_mp", PackVersion: 1, AccountRef: "acc-a", DisplayName: "A2",
		PersonaJson: "{}", CredentialRefsJson: "{}", KnowledgeScopeJson: "{}",
		GoalsJson: "{}", CadenceJson: "{}", ApprovalPolicyJson: "{}", BudgetJson: "{}",
	})
	require.Error(t, err, "duplicate account_ref must be rejected")

	_, err = q.UpsertOpsEngagement(ctx, UpsertOpsEngagementParams{
		BotID: bot.ID, PlatformEngagementID: "c1", Kind: "comment", Text: "hi",
	})
	require.NoError(t, err)
	_, err = q.UpsertOpsEngagement(ctx, UpsertOpsEngagementParams{
		BotID: bot.ID, PlatformEngagementID: "c1", Kind: "comment", Text: "hi again",
	})
	require.NoError(t, err, "upsert must not duplicate")
}

func TestOpsBrowserLeaseCAS(t *testing.T) {
	Driver = "sqlite"
	db := openTestDB(t)
	Migrate(db)
	q := New(db)
	ctx := context.Background()
	bot, err := q.CreateOpsBot(ctx, CreateOpsBotParams{
		OwnerType: "user", OwnerID: 1, Role: "account",
		PackID: "xiaohongshu", PackVersion: 1, AccountRef: "x1", DisplayName: "X",
		PersonaJson: "{}", CredentialRefsJson: "{}", KnowledgeScopeJson: "{}",
		GoalsJson: "{}", CadenceJson: "{}", ApprovalPolicyJson: "{}", BudgetJson: "{}",
	})
	require.NoError(t, err)
	_, err = q.UpsertOpsBrowserSession(ctx, UpsertOpsBrowserSessionParams{
		BotID: bot.ID, Backend: "local", ProfileDir: "/tmp/x1",
	})
	require.NoError(t, err)

	n, err := q.AcquireOpsBrowserLease(ctx, AcquireOpsBrowserLeaseParams{
		BotID: bot.ID, Backend: "local", LeaseOwner: "w1",
		LeaseExpiresAt: sql.NullTime{Time: time.Now().Add(time.Minute), Valid: true},
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, n)

	n, err = q.AcquireOpsBrowserLease(ctx, AcquireOpsBrowserLeaseParams{
		BotID: bot.ID, Backend: "local", LeaseOwner: "w2",
		LeaseExpiresAt: sql.NullTime{Time: time.Now().Add(time.Minute), Valid: true},
	})
	require.NoError(t, err)
	require.EqualValues(t, 0, n, "second lease must be rejected")
}
```

- [ ] **Step 2: 运行测试，确认失败**

Run: `cd server && go test ./internal/store -run 'TestOpsBot|TestOpsBrowserLease' -v`
Expected: FAIL，`no such table: ops_bots`。

- [ ] **Step 3: 双写 DDL**

把设计文档 §11.2 的 5 张表追加到 `schema.sql`，在 `schema_postgres.sql` 做等价转换（`INTEGER PRIMARY KEY AUTOINCREMENT` → `BIGSERIAL PRIMARY KEY`、`TIMESTAMP` → `TIMESTAMPTZ`、`REAL` → `DOUBLE PRECISION`）。同时在两张既有表定义中加列，并在 `migrate.go` 用 `addColumnIfNotExists` 为存量库补列、补索引：

```go
// migrate.go
func migrateOpsBot(db *DB) error {
    for _, c := range []struct{ table, column, ddl string }{
        {"im_bot_chats", "ops_bot_id", "INTEGER"},
        {"agent_ask_user_requests", "kind", "TEXT NOT NULL DEFAULT 'agent_question'"},
        {"agent_ask_user_requests", "metadata_json", "TEXT NOT NULL DEFAULT '{}'"},
    } {
        if err := addColumnIfNotExists(db, c.table, c.column, c.ddl); err != nil {
            return err
        }
    }
    if err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_aaur_kind_status
        ON agent_ask_user_requests(kind, status)`); err != nil {
        return err
    }
    return nil
}
```

- [ ] **Step 4: 写查询**

```sql
-- server/internal/store/queries/opsbot.sql

-- name: CreateOpsBot :one
INSERT INTO ops_bots (
    owner_type, owner_id, role, pack_id, pack_version, account_ref, display_name,
    persona_json, credential_refs_json, knowledge_scope_json, goals_json,
    cadence_json, approval_policy_json, budget_json, director_bot_id
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetOpsBot :one
SELECT * FROM ops_bots WHERE id = ?;

-- name: ListOpsBotsByOwner :many
SELECT * FROM ops_bots WHERE owner_type = ? AND owner_id = ?
ORDER BY role DESC, id ASC;

-- name: GetOpsBotByAccount :one
SELECT * FROM ops_bots
WHERE owner_type = ? AND owner_id = ? AND pack_id = ? AND account_ref = ?;

-- name: UpdateOpsBotStatus :one
UPDATE ops_bots SET status = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ? RETURNING *;

-- name: UpdateOpsBotConfig :one
UPDATE ops_bots SET
    display_name = ?, persona_json = ?, credential_refs_json = ?,
    knowledge_scope_json = ?, goals_json = ?, cadence_json = ?,
    approval_policy_json = ?, budget_json = ?, pack_version = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ? RETURNING *;

-- name: SetOpsBotProvision :one
UPDATE ops_bots SET epic_issue_id = ?, control_workspace_id = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ? RETURNING *;

-- name: CreateOpsPublishRecord :one
INSERT INTO ops_publish_records (
    issue_id, bot_id, pack_id, account_ref, content_version, idempotency_key, status
) VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING *;

-- name: GetOpsPublishRecord :one
SELECT * FROM ops_publish_records WHERE bot_id = ? AND idempotency_key = ?;

-- name: UpdateOpsPublishRecord :one
UPDATE ops_publish_records SET
    status = ?, receipt = ?, url = ?, screenshot_path = ?,
    published_at = CURRENT_TIMESTAMP
WHERE id = ? RETURNING *;

-- name: UpsertOpsMetricsDaily :one
INSERT INTO ops_metrics_daily (bot_id, date, metrics_json, source)
VALUES (?, ?, ?, ?)
ON CONFLICT(bot_id, date) DO UPDATE SET
    metrics_json = excluded.metrics_json, source = excluded.source
RETURNING *;

-- name: ListOpsMetricsDaily :many
SELECT * FROM ops_metrics_daily
WHERE bot_id = ? AND date >= ? AND date <= ? ORDER BY date ASC;

-- name: UpsertOpsEngagement :one
INSERT INTO ops_engagements (
    bot_id, issue_id, platform_engagement_id, kind, author, text
) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(bot_id, platform_engagement_id) DO UPDATE SET
    text = excluded.text, updated_at = CURRENT_TIMESTAMP
RETURNING *;

-- name: GetOpsEngagement :one
SELECT * FROM ops_engagements WHERE id = ?;

-- name: ListOpsEngagementsByStatus :many
SELECT * FROM ops_engagements
WHERE bot_id = ? AND status = ? ORDER BY created_at ASC LIMIT ?;

-- name: UpdateOpsEngagementStatus :one
UPDATE ops_engagements SET
    status = ?, category = ?, risk_level = ?, suggested_reply = ?,
    approval_request_id = ?, reply_text = ?, receipt = ?,
    decided_by = ?, decided_at = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ? AND status = ? RETURNING *;

-- name: CountOpsEngagementsByBot :one
SELECT COUNT(*) FROM ops_engagements
WHERE bot_id = ? AND created_at >= ? AND created_at < ?;

-- name: UpsertOpsBrowserSession :one
INSERT INTO ops_browser_sessions (bot_id, backend, profile_dir)
VALUES (?, ?, ?)
ON CONFLICT(bot_id, backend) DO UPDATE SET
    profile_dir = excluded.profile_dir, updated_at = CURRENT_TIMESTAMP
RETURNING *;

-- name: GetOpsBrowserSession :one
SELECT * FROM ops_browser_sessions WHERE bot_id = ? AND backend = ?;

-- name: AcquireOpsBrowserLease :execrows
UPDATE ops_browser_sessions SET
    lease_owner = ?, lease_expires_at = ?, updated_at = CURRENT_TIMESTAMP
WHERE bot_id = ? AND backend = ?
  AND (lease_owner = '' OR lease_expires_at IS NULL OR lease_expires_at < CURRENT_TIMESTAMP);

-- name: ReleaseOpsBrowserLease :execrows
UPDATE ops_browser_sessions SET
    lease_owner = '', lease_expires_at = NULL, updated_at = CURRENT_TIMESTAMP
WHERE bot_id = ? AND backend = ? AND lease_owner = ?;

-- name: UpdateOpsBrowserSessionState :one
UPDATE ops_browser_sessions SET
    login_status = ?, last_login_at = ?, health = ?, risk_state = ?,
    last_error = ?, updated_at = CURRENT_TIMESTAMP
WHERE bot_id = ? AND backend = ? RETURNING *;

-- name: SumOpsAccountCost :one
SELECT CAST(COALESCE(SUM(e.cost_usd), 0) AS REAL)
FROM issue_exec_events e
JOIN issues i ON i.id = e.issue_id
WHERE i.parent_issue_id = ? AND e.created_at >= ? AND e.created_at < ?;
```

`ask_user.sql` 修改：

```sql
-- name: InsertAskUserRequest :one
INSERT INTO agent_ask_user_requests
    (workspace_id, owner_type, owner_id, session_id, questions_json,
     kind, metadata_json, status, requested_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', CURRENT_TIMESTAMP, ?)
RETURNING id;

-- name: ListPendingOpsApprovalsByOwner :many
SELECT * FROM agent_ask_user_requests
WHERE owner_type = ? AND owner_id = ? AND kind = 'ops_approval' AND status = 'pending'
ORDER BY requested_at ASC;

-- name: GetOpsApproval :one
SELECT * FROM agent_ask_user_requests
WHERE id = ? AND kind = 'ops_approval';
```

- [ ] **Step 5: 生成并验证**

```bash
cd server
go run ./cmd/niuniu admin schema-diff
go test ./internal/store -run 'TestOpsBot|TestOpsBrowserLease' -v
cd .. && make sqlc && make schema-diff
```

Expected: schema-diff `OK`；测试 PASS；sqlc 无 diff。

- [ ] **Step 6: 提交**

```bash
git add server/internal/store/schema.sql server/internal/store/schema_postgres.sql \
  server/internal/store/migrate.go server/internal/store/queries/opsbot.sql \
  server/internal/store/queries/ask_user.sql \
  server/internal/store/migrate_opsbot_test.go server/internal/store/*.sql.go
git commit -m "feat(opsbot): add complete ops bot schema and queries"
```

---

## Task 2: ChannelAdapter 契约、能力错误与人工适配器

**Files:**
- Create: `server/internal/opsbot/channel/adapter.go`
- Create: `server/internal/opsbot/channel/manual.go`
- Test: `server/internal/opsbot/channel/adapter_test.go`

**Interfaces:**
- Produces: `channel.ChannelAdapter`、`channel.CapabilitySet`、`channel.Receipt`、`channel.Engagement`、`channel.ErrCapabilityUnavailable`、`channel.AdapterError`、`channel.ManualAdapter`。

- [ ] **Step 1: 写失败测试**

```go
package channel

import (
	"context"
	"errors"
	"testing"
)

func TestManualAdapterReportsUnavailableCapabilities(t *testing.T) {
	var a ChannelAdapter = NewManualAdapter(t.TempDir())
	if !a.Capabilities()[CapPublish] {
		t.Fatal("manual adapter must support publish package")
	}
	if _, err := a.ListEngagements(context.Background(), ""); !errors.Is(err, ErrCapabilityUnavailable) {
		t.Fatalf("want ErrCapabilityUnavailable, got %v", err)
	}
}

func TestAdapterErrorCarriesRetryability(t *testing.T) {
	err := &AdapterError{Capability: CapPublish, Code: "rate_limited", Retryable: true}
	if !err.Retryable || !errors.Is(err, ErrCapabilityUnavailable) {
		t.Fatal("adapter error must expose retryability")
	}
}
```

- [ ] **Step 2: 实现契约**

```go
package channel

import (
	"context"
	"errors"
	"fmt"
)

type Capability string

const (
	CapPublish     Capability = "publish"
	CapMetrics     Capability = "metrics"
	CapEngagements Capability = "engagements"
	CapReply       Capability = "reply"
)

type CapabilitySet map[Capability]bool

type Receipt struct {
	ExternalID string
	URL        string
	Raw        string
}

type Engagement struct {
	ExtID  string
	Kind   string // comment | dm
	Author string
	Text   string
}

type ChannelAdapter interface {
	Capabilities() CapabilitySet
	EnsureSession(ctx context.Context, accountRef string) error
	Publish(ctx context.Context, payload any) (Receipt, error)
	FetchMetrics(ctx context.Context, from, to string) (map[string]float64, error)
	ListEngagements(ctx context.Context, cursor string) ([]Engagement, string, error)
	Reply(ctx context.Context, engagementID, text string) (Receipt, error)
	HealthCheck(ctx context.Context) error
}

var ErrCapabilityUnavailable = errors.New("opsbot: capability unavailable")

type AdapterError struct {
	Capability Capability
	Code       string
	Retryable  bool
	Err        error
}

func (e *AdapterError) Error() string {
	return fmt.Sprintf("channel %s failed (%s): %v", e.Capability, e.Code, e.Err)
}
func (e *AdapterError) Unwrap() error { return e.Err }
func (e *AdapterError) Is(target error) bool { return target == ErrCapabilityUnavailable }
```

- [ ] **Step 3: 实现人工适配器**

`ManualAdapter` 的 `Publish` 把发布包（标题、正文、图片/视频路径、平台参数）写入 `manual/<bot>/<idempotency>/`，返回 `Receipt{ExternalID: "manual:<dir>"}`；`EnsureSession`/`HealthCheck` 返回 nil；`FetchMetrics`/`ListEngagements`/`Reply` 返回 `&AdapterError{Capability: ..., Code: "manual", Err: ErrCapabilityUnavailable}`。

- [ ] **Step 4: 运行测试并提交**

```bash
cd server && go test ./internal/opsbot/channel -run 'TestManual|TestAdapterError' -v
git add server/internal/opsbot/channel
git commit -m "feat(opsbot): add channel adapter contract and manual adapter"
```

---

## Task 3: Platform Pack 模型与校验

**Files:**
- Create: `server/internal/opsbot/packs/types.go`
- Create: `server/internal/opsbot/packs/validate.go`
- Test: `server/internal/opsbot/packs/validate_test.go`

**Interfaces:**
- Produces: `packs.Manifest`、`packs.Binding`、`packs.CredentialSlot`、`packs.ContentSchema`、`packs.Compliance`、`packs.MetricsMap`、`packs.Recipe`、`packs.Validate(root string, m Manifest) error`。

- [ ] **Step 1: 写失败测试**

```go
package packs

import "testing"

func TestValidateRejectsBrowserBindingWithoutRecipeFile(t *testing.T) {
	m := Manifest{
		ID: "zhihu", Version: 1, Status: "published",
		Capabilities: []string{"publish"},
		Bindings: map[string]Binding{"publish": {Transport: "browser", Recipe: "publish.json"}},
	}
	if err := Validate(t.TempDir(), m); err == nil {
		t.Fatal("missing recipe file must fail")
	}
}

func TestValidateRejectsOpenAPIWithoutProvider(t *testing.T) {
	m := Manifest{
		ID: "facebook", Version: 1, Status: "published",
		Capabilities: []string{"publish"},
		Bindings: map[string]Binding{"publish": {Transport: "openapi"}},
	}
	if err := Validate(t.TempDir(), m); err == nil {
		t.Fatal("openapi binding without provider must fail")
	}
}
```

- [ ] **Step 2: 定义类型**

```go
package packs

type Binding struct {
	Transport string `json:"transport"` // openapi | mcp | browser | manual
	Provider  string `json:"provider,omitempty"`
	Recipe    string `json:"recipe,omitempty"`
}

type CredentialSlot struct {
	Required bool   `json:"required"`
	Scope    string `json:"scope"` // owner | account
}

type ContentFormat struct {
	Fields     []string `json:"fields"`
	MaxChars   int      `json:"max_chars"`
	ImageRatio string   `json:"image_ratio"`
	MaxImages  int      `json:"max_images"`
	VideoSec   int      `json:"video_sec"`
}

type ContentSchema struct {
	Formats map[string]ContentFormat `json:"formats"` // article | note | video | post
}

type Compliance struct {
	ForbiddenTerms []string `json:"forbidden_terms"`
	RequiredChecks []string `json:"required_checks"`
}

type MetricsMap struct {
	Fields map[string]string `json:"fields"` // platform key -> unified key
}

type RecipeStep struct {
	Action   string            `json:"action"`
	Selector string            `json:"selector,omitempty"`
	Value    string            `json:"value,omitempty"`
	Fallback []string          `json:"fallback,omitempty"`
	Timeout  int               `json:"timeout_ms,omitempty"`
	Args     map[string]string `json:"args,omitempty"`
}

type Recipe struct {
	Name     string       `json:"name"`
	Steps    []RecipeStep `json:"steps"`
	Fixtures []string     `json:"fixtures,omitempty"`
}

type Manifest struct {
	ID              string                    `json:"id"`
	Version         int                       `json:"version"`
	Status          string                    `json:"status"` // draft | testing | published | deprecated
	DisplayName     string                    `json:"display_name"`
	Capabilities    []string                  `json:"capabilities"`
	Bindings        map[string]Binding        `json:"bindings"`
	CredentialSlots map[string]CredentialSlot `json:"credential_slots"`
	ContentSchema   ContentSchema             `json:"content_schema"`
	Compliance      Compliance                `json:"compliance"`
	MetricsMap      MetricsMap                `json:"metrics_map"`
}
```

- [ ] **Step 3: 实现校验**

`Validate(root, m)` 必须检查：

1. `ID` 匹配 `^[a-z0-9][a-z0-9_-]*$`；`Version > 0`；`Status` 属于四态。
2. `Capabilities` 非空且无重复；每个能力都有 binding。
3. `Transport` 属于 `openapi|mcp|browser|manual`。
4. `openapi`/`mcp` 必须有 `Provider`；`browser` 必须有 `Recipe` 且 `root/<Recipe>` 文件存在且能解析为 `Recipe`。
5. `CredentialSlots` 的 `Scope` 只能是 `owner|account`。
6. `ContentSchema.Formats` 覆盖能力对应的格式（`publish` 至少一个格式）。
7. `MetricsMap.Fields` 非空（声明了 `metrics` 能力时）。

- [ ] **Step 4: 运行测试并提交**

```bash
cd server && go test ./internal/opsbot/packs -run TestValidate -v
git add server/internal/opsbot/packs
git commit -m "feat(opsbot): add platform pack model and validation"
```

---

## Task 4: Pack 文件系统注册表与导入导出

**Files:**
- Create: `server/internal/opsbot/packs/registry.go`
- Create: `server/internal/opsbot/packs/builtin.go`
- Test: `server/internal/opsbot/packs/registry_test.go`

**Interfaces:**
- Produces: `packs.Registry`：`Root`、`Save`、`Load`、`List`、`LatestPublished`、`Publish`、`Export`、`Import`、`InstallBuiltins`。

- [ ] **Step 1: 写失败测试**

```go
func TestRegistryRejectsPathTraversal(t *testing.T) {
	r := &Registry{DataDir: t.TempDir()}
	if _, err := r.Root("user", 1, "../evil", 1); err == nil {
		t.Fatal("expected path traversal rejection")
	}
}

func TestRegistryPublishListAndExportImport(t *testing.T) {
	r := &Registry{DataDir: t.TempDir()}
	root := writePack(t, r, "demo", 1, "draft")
	m, err := r.Load("user", 1, "demo", 1)
	if err != nil || m.Status != "draft" {
		t.Fatalf("load draft: %v %+v", err, m)
	}
	if err := r.Publish("user", 1, "demo", 1); err != nil {
		t.Fatal(err)
	}
	latest, err := r.LatestPublished("user", 1, "demo")
	if err != nil || latest.Version != 1 {
		t.Fatalf("latest: %v %+v", err, latest)
	}
	zipPath := filepath.Join(t.TempDir(), "demo.zip")
	if err := r.Export("user", 1, "demo", 1, zipPath); err != nil {
		t.Fatal(err)
	}
	other := &Registry{DataDir: t.TempDir()}
	if err := other.Import("user", 2, zipPath); err != nil {
		t.Fatal(err)
	}
	got, err := other.Load("user", 2, "demo", 1)
	if err != nil || got.ID != "demo" {
		t.Fatalf("imported: %v %+v", err, got)
	}
}
```

- [ ] **Step 2: 实现 registry**

要点：
- `Root` 用 `filepath.Clean` + 前缀校验，拒绝 `..`、绝对路径、非法字符。
- `Save`：先 `Validate`，写 `manifest.json.tmp` 再 `os.Rename`；整个 Pack 目录内容由调用方提供时先写临时目录再原子替换。
- `List` 扫描 `<data>/<users|orgs>/<id>/ops/packs/*/*/manifest.json`，忽略损坏版本并返回可用列表。
- `LatestPublished` 只在 `status=published` 中取最大版本。
- `Export` 用 `archive/zip` 打包整个版本目录；`Import` 解压到临时目录、校验 `manifest.json`、拒绝 `..` 条目、再 `Save` 到目标 owner。
- `InstallBuiltins` 从 `//go:embed builtin/*/*/manifest.json` 读取，同名同版本已存在时跳过，不覆盖用户修改。

- [ ] **Step 3: 运行测试并提交**

```bash
cd server && go test ./internal/opsbot/packs -run 'TestRegistry' -v
git add server/internal/opsbot/packs
git commit -m "feat(opsbot): add filesystem pack registry with export and import"
```

---

## Task 5: 内置 Pack（公众号、小红书）

**Files:**
- Create: `server/internal/opsbot/packs/builtin/wechat_mp/v1/{manifest,content_schema,compliance,metrics_map}.json`
- Create: `server/internal/opsbot/packs/builtin/wechat_mp/v1/prompts/*.md`
- Create: `server/internal/opsbot/packs/builtin/xiaohongshu/v1/{manifest,content_schema,compliance,metrics_map}.json`
- Create: `server/internal/opsbot/packs/builtin/xiaohongshu/v1/prompts/*.md`
- Create: `server/internal/opsbot/packs/builtin/xiaohongshu/v1/browser/recipes/{login,publish,metrics,engagements,reply}.json`
- Modify: `server/internal/opsbot/packs/builtin.go`
- Test: `server/internal/opsbot/packs/builtin_test.go`

- [ ] **Step 1: 公众号 Pack**

`manifest.json`：`publish/metrics/engagements/reply` 全部 `mcp` → `provider: wechat-mp`；`credential_slots.account = {required:true, scope:"account"}`；`content_schema.formats.article`（标题 ≤64、正文不限、封面 900×383、图片 ≤20）；`metrics_map`：`read_count→views`、`share_count→shares`、`like_count→likes`、`net_fans→follower_delta`。

`compliance.json`：广告法绝对化用语、医疗/金融资质提示、版权与来源要求、公众号原创/转载规则。

`prompts/`：选题、长文结构、排版、标题、封面文案。

- [ ] **Step 2: 小红书 Pack**

`manifest.json`：四个能力全部 `browser`，`recipe` 分别指向 `browser/recipes/*.json`；`content_schema.formats.note`（标题 ≤20、正文 ≤1000、图片 3:4、≤18 张）、`formats.video`（≤15 分钟）；`metrics_map`：`view_num→views`、`like_num→likes`、`collect_num→saves`、`comment_num→comments`、`share_num→shares`、`fans_add→follower_delta`。

`compliance.json`：小红书社区规范、禁用词、医疗/功效宣称、导流限制、版权与二创规则。

`recipes`：
- `login.json`：打开创作者中心 → 检测登录态 → 扫码等待 → 断言已登录。
- `publish.json`：进入发布页 → 上传图片/视频 → 填标题正文标签 → 选择封面 → 提交 → 提取笔记 URL。
- `metrics.json`：进入数据页 → 选择日期范围 → 提取指标。
- `engagements.json`：进入评论/私信页 → 滚动提取列表。
- `reply.json`：定位会话 → 填入回复 → 发送 → 断言成功。

每条 recipe 带 `fixtures` 指向 `fixtures/*.html` 沙箱样例。

- [ ] **Step 3: 测试内置 Pack 全部通过校验**

```go
func TestBuiltinsValidate(t *testing.T) {
	r := &Registry{DataDir: t.TempDir()}
	if err := InstallBuiltins(r, "user", 1); err != nil {
		t.Fatal(err)
	}
	list, err := r.List("user", 1)
	if err != nil || len(list) < 2 {
		t.Fatalf("builtins: %v %d", err, len(list))
	}
	for _, m := range list {
		if err := Validate(r.mustRoot("user", 1, m.ID, m.Version), m); err != nil {
			t.Fatalf("%s v%d: %v", m.ID, m.Version, err)
		}
	}
}
```

- [ ] **Step 4: 运行测试并提交**

```bash
cd server && go test ./internal/opsbot/packs -run TestBuiltins -v
git add server/internal/opsbot/packs
git commit -m "feat(opsbot): add builtin wechat and xiaohongshu packs"
```

---

## Task 6: OpsBot 服务（账号、总监、配置、状态）

**Files:**
- Create: `server/internal/opsbot/service.go`
- Test: `server/internal/opsbot/service_test.go`

**Interfaces:**
- Consumes: `*store.Queries`、`*packs.Registry`。
- Produces: `Service`：`CreateAccountBot`、`CreateDirector`、`Get`、`ListByOwner`、`UpdateConfig`、`SetStatus`、`KillSwitch`；错误 `ErrAccountExists`、`ErrPackNotFound`。

- [ ] **Step 1: 写测试**

```go
func TestCreateTwoAccountsSamePackAndRejectDuplicate(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	owner := OwnerRef{Type: "user", ID: 1}

	_, err := svc.CreateAccountBot(ctx, CreateAccountInput{
		Owner: owner, PackID: "wechat_mp", AccountRef: "acc-a", DisplayName: "A",
	})
	require.NoError(t, err)
	_, err = svc.CreateAccountBot(ctx, CreateAccountInput{
		Owner: owner, PackID: "wechat_mp", AccountRef: "acc-b", DisplayName: "B",
	})
	require.NoError(t, err)

	_, err = svc.CreateAccountBot(ctx, CreateAccountInput{
		Owner: owner, PackID: "wechat_mp", AccountRef: "acc-a", DisplayName: "A2",
	})
	require.ErrorIs(t, err, ErrAccountExists)
}

func TestUpdateConfigValidatesJSON(t *testing.T) {
	svc := newTestService(t)
	bot, err := svc.CreateAccountBot(context.Background(), CreateAccountInput{
		Owner: OwnerRef{Type: "user", ID: 1}, PackID: "wechat_mp",
		AccountRef: "acc-a", DisplayName: "A",
	})
	require.NoError(t, err)
	_, err = svc.UpdateConfig(context.Background(), bot.ID, ConfigPatch{
		BudgetJSON: ptr(`{"monthly_limit":100,"currency":"CNY"}`),
	})
	require.NoError(t, err)
	_, err = svc.UpdateConfig(context.Background(), bot.ID, ConfigPatch{BudgetJSON: ptr(`not-json`)})
	require.Error(t, err)
}
```

- [ ] **Step 2: 实现服务**

```go
type OwnerRef struct{ Type string; ID int64 }

type CreateAccountInput struct {
	Owner                    OwnerRef
	PackID                   string
	PackVersion              int64
	AccountRef, DisplayName  string
	PersonaJSON              string
	CredentialRefsJSON       string
	KnowledgeScopeJSON       string
	GoalsJSON, CadenceJSON   string
	ApprovalPolicyJSON       string
	BudgetJSON               string
	DirectorBotID, CreatedBy int64
}

type ConfigPatch struct {
	DisplayName        *string
	PersonaJSON        *string
	CredentialRefsJSON *string
	KnowledgeScopeJSON *string
	GoalsJSON          *string
	CadenceJSON        *string
	ApprovalPolicyJSON *string
	BudgetJSON         *string
	PackVersion        *int64
}

var (
	ErrAccountExists = errors.New("opsbot: account already exists")
	ErrPackNotFound  = errors.New("opsbot: pack not found")
)
```

行为：
- `CreateAccountBot`：`pack_version=0` 时取最新 `published`；加载 Pack 校验；默认值 `{}`；审批默认 `{"publish":true,"reply":true,"video":true}`；唯一约束冲突映射为 `ErrAccountExists`。
- `CreateDirector`：`role=director`、`pack_id=''`、`account_ref=''`。
- `UpdateConfig`：只更新非 nil 字段；所有 `*JSON` 必须是 JSON object；`approval_policy_json` 的键只能是 `publish|reply|video`。
- `SetStatus`：`active|paused|disabled` 三态白名单。
- `KillSwitch`：`disabled` + 取消该 Bot 所有 pending `ops_approval`（通过 `Approval.CancelByBot`，Task 8 提供）。

- [ ] **Step 3: 运行测试并提交**

```bash
cd server && go test ./internal/opsbot -run 'TestCreateTwoAccounts|TestUpdateConfig' -v
git add server/internal/opsbot
git commit -m "feat(opsbot): add ops bot service with account isolation"
```

---

## Task 7: 账号与总监 Epic / 控制工作空间

**Files:**
- Create: `server/internal/opsbot/provision.go`
- Test: `server/internal/opsbot/provision_test.go`

**Interfaces:**
- Produces: `Provisioner.ProvisionAccount` / `ProvisionDirector` / `EnsureAll`。
- Consumes（接口，由 `server.go` 适配现有服务）：`IssueCreator`、`WorkspaceCreator`、`ColumnResolver`。

- [ ] **Step 1: 写测试**

```go
type fakeIssues struct{ n int64 }
func (f *fakeIssues) CreateEpic(context.Context, int64, string, string, int64) (int64, error) {
	f.n++
	return 900 + f.n, nil
}
type fakeWS struct{ n int64 }
func (f *fakeWS) CreateControl(context.Context, int64, string, int64, int64) (int64, error) {
	f.n++
	return 1600 + f.n, nil
}

func TestProvisionAccountAndDirector(t *testing.T) {
	p := &Provisioner{
		Issues: &fakeIssues{}, Workspaces: &fakeWS{},
		FirstColumn: func(context.Context, int64) (int64, error) { return 1, nil },
	}
	i, w, err := p.ProvisionAccount(context.Background(), OwnerRef{"user", 1}, 42, 7, "公众号 A", 1)
	if err != nil || i != 901 || w != 1601 {
		t.Fatalf("account: %d %d %v", i, w, err)
	}
	i, w, err = p.ProvisionDirector(context.Background(), OwnerRef{"user", 1}, 42, 8, "运营总监", 1)
	if err != nil || i != 902 || w != 1602 {
		t.Fatalf("director: %d %d %v", i, w, err)
	}
}
```

- [ ] **Step 2: 实现**

```go
type IssueCreator interface {
	CreateEpic(ctx context.Context, columnID int64, title, description string, byUserID int64) (int64, error)
}
type WorkspaceCreator interface {
	CreateControl(ctx context.Context, issueID int64, ownerType string, ownerID, createdBy int64) (int64, error)
}
type ColumnResolver interface {
	FirstColumn(ctx context.Context, projectID int64) (int64, error)
}
type Provisioner struct {
	Issues      IssueCreator
	Workspaces  WorkspaceCreator
	Columns     ColumnResolver
}
```

行为：
- `ProvisionAccount`：创建 `issue_type=epic` 的控制 Epic（标题 `运营 Bot：<display_name>`），再创建控制 workspace，最后 `SetOpsBotProvision` 写回 `epic_issue_id` / `control_workspace_id`。
- `ProvisionDirector`：同上，标题 `运营总监：<display_name>`。
- `EnsureAll`：为缺失 provisioning 的 Bot 补齐；已存在则跳过，可重复执行。
- 失败回滚：workspace 创建失败时删除刚创建的 Epic（调用可选 `DeleteIssue` 接口；接口缺失则记录并返回错误，由调用方重试）。

- [ ] **Step 3: 运行测试并提交**

```bash
cd server && go test ./internal/opsbot -run TestProvision -v
git add server/internal/opsbot
git commit -m "feat(opsbot): provision account and director epics with control workspaces"
```

---

## Task 8: 审批服务（复用 ask_user，完整状态机）

**Files:**
- Create: `server/internal/opsbot/approval.go`
- Test: `server/internal/opsbot/approval_test.go`
- Modify: `server/internal/service/ask_user.go`（如需暴露按 kind 查询）

**Interfaces:**
- Produces: `Approval.Create`、`Approval.Decide`、`Approval.ListPending`、`Approval.Get`、`Approval.CancelByBot`、`Approval.SweepExpired`；类型 `ApprovalType`、`Decision`。
- Consumes: `*store.Queries`、`ApprovalPolicy`。

- [ ] **Step 1: 写测试**

```go
func TestApprovalLifecycle(t *testing.T) {
	a := newTestApproval(t)
	ctx := context.Background()
	req, err := a.Create(ctx, ApprovalInput{
		BotID: 1, WorkspaceID: 10, IssueID: 20, Type: ApprovalPublish,
		ContentVersion: 3, PreviewJSON: `{"title":"t","summary":"s"}`,
	})
	require.NoError(t, err)

	_, err = a.Decide(ctx, req.ID, Decision{OperatorID: 7, Action: "approved", Reason: "ok"})
	require.NoError(t, err)
	_, err = a.Decide(ctx, req.ID, Decision{OperatorID: 7, Action: "rejected", Reason: "late"})
	require.ErrorIs(t, err, ErrAlreadyDecided)
}

func TestProxyApprovalIsRecorded(t *testing.T) {
	a := newTestApproval(t)
	req, err := a.Create(context.Background(), ApprovalInput{
		BotID: 1, WorkspaceID: 10, Type: ApprovalReply, PreviewJSON: `{}`,
	})
	require.NoError(t, err)
	got, err := a.Decide(context.Background(), req.ID, Decision{
		OperatorID: 9, Action: "approved", ProxyFor: 7,
	})
	require.NoError(t, err)
	require.Contains(t, got.MetadataJSON, `"proxy_for":7`)
	require.Contains(t, got.MetadataJSON, `"approved_by":9`)
}

func TestTimeoutNeverAutoApproves(t *testing.T) {
	a := newTestApproval(t)
	req, err := a.Create(context.Background(), ApprovalInput{
		BotID: 1, WorkspaceID: 10, Type: ApprovalVideo, TTL: time.Millisecond,
	})
	require.NoError(t, err)
	time.Sleep(5 * time.Millisecond)
	require.NoError(t, a.SweepExpired(context.Background()))
	got, err := a.Get(context.Background(), req.ID)
	require.NoError(t, err)
	require.Equal(t, "timeout", got.Status)
}
```

- [ ] **Step 2: 实现**

```go
type ApprovalType string

const (
	ApprovalPublish ApprovalType = "publish"
	ApprovalReply   ApprovalType = "reply"
	ApprovalVideo   ApprovalType = "video"
)

type ApprovalInput struct {
	BotID          int64
	WorkspaceID    int64
	IssueID        int64
	Type           ApprovalType
	ContentVersion int64
	PreviewJSON    string
	TTL            time.Duration
}

type Decision struct {
	OperatorID int64
	Action     string // approved | rejected | modified
	Reason     string
	ProxyFor   int64
	Answers    map[string]string
}

type ApprovalRequest struct {
	ID           int64
	Status       string
	MetadataJSON string
	ExpiresAt    time.Time
}
```

行为：
- `Create`：`questions_json = {"type":"ops_approval","options":[...],"preview":...}`；`kind='ops_approval'`；`metadata_json` 包含 `bot_id/issue_id/type/content_version/preview`；`session_id = "ops-approval"`；`expires_at = now + TTL`（默认 24h）。
- `Decide`：调用 `MarkAskUserRequestAnswered` 的 CAS；`0 rows` → `ErrAlreadyDecided`；`answers_json` 存 `{"action","reason","answers"}`；代审时把 `proxy_for` / `approved_by` 合并进 `metadata_json`（读-改-写，保持 preview 不丢）。
- `ListPending`：`ListPendingOpsApprovalsByOwner`，按 owner 隔离。
- `CancelByBot`：按 `metadata_json.bot_id` 过滤后调用 `MarkAskUserRequestCancelled`；用于 kill switch。
- `SweepExpired`：查询过期 pending → `MarkAskUserRequestTimeout`；**不**触发发布，只通知。
- 审批通过回调：`Approval` 暴露 `OnDecided func(ctx, req, decision) error`，由发布/互动服务注册；决定事务提交后再执行回调。

- [ ] **Step 3: 运行测试并提交**

```bash
cd server && go test ./internal/opsbot -run 'TestApproval|TestProxy|TestTimeout' -v
git add server/internal/opsbot server/internal/service/ask_user.go
git commit -m "feat(opsbot): add approval service over ask-user requests"
```

---

## Task 9: 预算服务

**Files:**
- Create: `server/internal/opsbot/budget.go`
- Test: `server/internal/opsbot/budget_test.go`

**Interfaces:**
- Produces: `Budget.Check`、`Budget.Spent`、`Budget.Usage`、`Budget.SetLimit`；错误 `ErrBudgetExceeded`。
- Consumes: `store.SumOpsAccountCost`、`ops_bots.budget_json`、`*Service.SetStatus`、`Notifier`。

- [ ] **Step 1: 写测试**

```go
func TestBudgetRejectsOverLimitAndPauses(t *testing.T) {
	paused := false
	b := &Budget{
		Sum: func(context.Context, int64, time.Time, time.Time) (float64, error) { return 990, nil },
		Pause: func(context.Context, int64) error { paused = true; return nil },
		Notify: func(context.Context, int64, string) error { return nil },
	}
	err := b.Check(context.Background(), 1, 901, 20)
	require.ErrorIs(t, err, ErrBudgetExceeded)
	require.True(t, paused)
}

func TestBudgetAllowsWithinLimit(t *testing.T) {
	b := &Budget{
		Sum: func(context.Context, int64, time.Time, time.Time) (float64, error) { return 10, nil },
	}
	require.NoError(t, b.Check(context.Background(), 1, 901, 20))
}
```

- [ ] **Step 2: 实现**

```go
type Budget struct {
	Sum    func(ctx context.Context, epicIssueID int64, from, to time.Time) (float64, error)
	Pause  func(ctx context.Context, botID int64) error
	Notify func(ctx context.Context, botID int64, reason string) error
	Limit  func(ctx context.Context, botID int64) (float64, string, error)
}

var ErrBudgetExceeded = errors.New("opsbot: budget exceeded")
```

行为：
- `Check(botID, epicIssueID, cost)`：按自然月读取额度；`limit<=0` 视为不限；`spent+cost > limit` → `Pause` + `Notify` + 返回 `ErrBudgetExceeded`。
- 预算分项：`issue_exec_events(kind='cost')` 的 `detail_json` 写 `{"category":"text|image|video|api"}`。
- `Usage` 返回：月度上限、已用、剩余、分项汇总、币种。
- `SetLimit` 由总监或运营人调用，写 `budget_json` 并恢复 `paused`（若因预算暂停）。
- 检查点：生成前、视频合成前、发布前、互动发送前。

- [ ] **Step 3: 运行测试并提交**

```bash
cd server && go test ./internal/opsbot -run TestBudget -v
git add server/internal/opsbot
git commit -m "feat(opsbot): add per-account budget enforcement"
```

---

## Task 10: 对话式接入与配置

**Files:**
- Create: `server/internal/opsbot/onboarding.go`
- Test: `server/internal/opsbot/onboarding_test.go`
- Modify: `server/internal/service/imbot_inbound.go`

**Interfaces:**
- Produces: `Onboarding.Handle(ctx, chatID, owner, projectID, text) (reply string, done bool, err error)`；`Onboarding.Session`。
- Consumes: `Service`、`Provisioner`、`Approval`、`packs.Registry`、`PackGenerator`（Task 30）。

- [ ] **Step 1: 写测试**

```go
func TestOnboardingCreateAccount(t *testing.T) {
	o := newTestOnboarding(t)
	owner := OwnerRef{"user", 1}
	steps := []struct{ in, want string }{
		{"接入公众号", "账号名称"},
		{"品牌 A", "目标"},
		{"涨粉", "每周"},
		{"每周 2 篇，热点加更", "预算"},
		{"每月 200", "确认"},
		{"需要", "已创建"},
	}
	for _, s := range steps {
		reply, done, err := o.Handle(context.Background(), 10, owner, 42, s.in)
		require.NoError(t, err)
		require.Contains(t, reply, s.want)
		if s.want == "已创建" {
			require.True(t, done)
		}
	}
}

func TestOnboardingSecondAccountReusesPack(t *testing.T) {
	o := newTestOnboarding(t)
	owner := OwnerRef{"user", 1}
	o.Handle(context.Background(), 10, owner, 42, "接入公众号")
	for _, in := range []string{"A", "涨粉", "每周 2 篇", "每月 100", "需要"} {
		o.Handle(context.Background(), 10, owner, 42, in)
	}
	reply, _, err := o.Handle(context.Background(), 11, owner, 42, "再接入一个公众号")
	require.NoError(t, err)
	require.Contains(t, reply, "账号名称")
}
```

- [ ] **Step 2: 实现状态机**

阶段：`intent → pack → account → goal → cadence → budget → approval → credential → confirm`。

行为：
- 识别：`接入公众号`、`接入小红书`、`接入新渠道 <name>`、`再接入一个 <渠道>`、`/bots`、`/bot <name>`、`/approvals`、`/pause`、`/resume`、`/budget`、`/calendar`、`/cancel`。
- 每步只问一个问题；自然语言解析目标（涨粉/转化/品牌）、节奏（每周 N 篇 + 是否热点加更）、预算（`每月 N` 或 `不限`）、审批（需要/不需要，可分别指定 publish/reply/video）。
- 凭据步骤只返回一次性安全链接或扫码任务，不接收密钥。
- 完成时：`CreateAccountBot` → `ProvisionAccount` → 返回配置摘要卡（账号、Pack、目标、节奏、预算、审批、待授权项）。
- 新渠道：调用 `PackGenerator.Draft`，返回能力矩阵卡，用户确认后 `Publish`，再回到账号步骤。
- 会话状态按 chat 保存，30 分钟无交互过期；`/cancel` 立即清除。

- [ ] **Step 3: 接入 imbot**

在 `HandleInbound` 的路由前插入 onboarding 分发；命中时推送文本并结束本轮；未命中时保持原有 Intake 流程。

- [ ] **Step 4: 运行测试并提交**

```bash
cd server && go test ./internal/opsbot ./internal/service -run 'TestOnboarding|TestIMBot' -v
git add server/internal/opsbot server/internal/service/imbot_inbound.go
git commit -m "feat(opsbot): add conversational onboarding and config"
```

---

## Task 11: imbot 路由、命令、审批卡与通知

**Files:**
- Modify: `server/internal/service/imbot_inbound.go`
- Modify: `server/internal/service/imbot.go`
- Create: `server/internal/opsbot/notify.go`
- Test: `server/internal/service/imbot_ops_test.go`

**Interfaces:**
- Produces: `Notifier.PushApproval`、`Notifier.PushText`、`Notifier.PushRisk`；chat ↔ bot 绑定解析。
- Consumes: `store.UpdateImBotChat`（新增 `ops_bot_id` 参数）、`Approval`。

- [ ] **Step 1: 写测试**

覆盖：
- 已绑定 `ops_bot_id` 的 chat，入站文本路由到对应 Bot 上下文，而不是默认项目 Intake。
- 审批卡按钮回调 `approve/reject/modify` 调用 `Approval.Decide`，重复回调返回“已处理”。
- 未绑定 chat 仍走原 Intake，行为不变。
- 通知失败不阻断主流程，记录日志。

- [ ] **Step 2: 实现**

- `im_bot_chats.ops_bot_id` 更新查询与 handler。
- 回调解析：卡片 `value` 带 `approval_id` / `action` / `reason`；`modify` 进入待修改态并返回输入提示。
- 通知模板：发布审批（标题/摘要/封面/渠道/时间/合规结论）、回复审批（原评论/分类/建议/风险）、视频审批（预览/时长/字幕/封面）、风险告警（账号/原因/已暂停）。
- 审批人解析：账号 `approval_policy_json.operator_id`；缺省用 chat owner；总监代审走 `ProxyFor`。

- [ ] **Step 3: 运行测试并提交**

```bash
cd server && go test ./internal/service -run 'TestOps' -v
git add server/internal/service server/internal/opsbot/notify.go
git commit -m "feat(opsbot): route imbot chats and approval cards to ops bots"
```

---

## Task 12: HTTP API 与 MCP 工具

**Files:**
- Create: `server/internal/api/opsbot.go`
- Modify: `server/internal/server/server.go`
- Modify: `server/internal/server/router.go`
- Create: `server/cmd/niuniu-mcp/opsbot_tools.go`
- Modify: `server/cmd/niuniu-mcp/main.go`
- Test: `server/internal/api/opsbot_test.go`

**Interfaces:**
- HTTP：
  - `GET/POST /api/ops/bots`
  - `GET/PATCH /api/ops/bots/:id`
  - `POST /api/ops/bots/:id/pause|resume|disable`
  - `GET /api/ops/packs`、`POST /api/ops/packs/import`、`GET /api/ops/packs/:id/:version/export`
  - `GET /api/ops/approvals?status=pending`
  - `POST /api/ops/approvals/:id/approve|reject|modify`
  - `GET /api/ops/bots/:id/metrics`
  - `GET /api/ops/bots/:id/engagements`
  - `GET /api/ops/bots/:id/budget`
  - `PATCH /api/imbot/chats/:id` 增加 `ops_bot_id`
- MCP：`opsbot_create`、`opsbot_list`、`opsbot_get`、`opsbot_update`、`opsbot_pause`、`opsbot_resume`、`opsbot_kill`、`opsbot_set_goals`、`opsbot_set_cadence`、`opsbot_set_budget`、`opsbot_set_approval`、`opsbot_request_credential_link`、`opsbot_browser_login`、`opsbot_bind_chat`、`ops_pack_list`、`ops_pack_generate`、`ops_pack_validate`、`ops_pack_test`、`ops_pack_publish`、`ops_pack_export`、`ops_pack_import`、`ops_approval_list`、`ops_approval_approve`、`ops_approval_reject`、`ops_publish`、`ops_engagement_reply`、`ops_metrics`、`ops_calendar`。

- [ ] **Step 1: 写 handler 测试**

复用 `intake_completion_contract_test.go` 的 router/DB harness，覆盖：合法 owner 创建 200；跨 owner 403；同 Pack 第二账号 200；重复 `account_ref` 409；审批重复决定 409；预算超限 409；MCP 工具参数与 HTTP 行为一致。

- [ ] **Step 2: 实现 handler 与接线**

`OpsBotHandler` 持有 `*opsbot.Service`、`*opsbot.Provisioner`、`*opsbot.Approval`、`*opsbot.Budget`、`*service.Authz`；写操作先 `EnsureOwnerWritable`；创建 Bot 后同步 provisioning。

- [ ] **Step 3: 实现 MCP 工具**

在 `main.go` 的 `registerIntakeTools` 旁注册；参数按设计文档 §12；写工具返回统一 envelope，错误码映射 HTTP 状态。

- [ ] **Step 4: 运行测试并提交**

```bash
cd server && go test ./internal/api ./cmd/niuniu-mcp -run 'Ops' -v
git add server/internal/api/opsbot.go server/internal/server/server.go server/internal/server/router.go \
  server/cmd/niuniu-mcp/opsbot_tools.go server/cmd/niuniu-mcp/main.go
git commit -m "feat(opsbot): add http api and mcp tools"
```

---

## Task 13: 内容 Brief 生成

**Files:**
- Create: `server/internal/opsbot/content/brief.go`
- Test: `server/internal/opsbot/content/brief_test.go`

**Interfaces:**
- Produces: `BriefBuilder.Build(ctx, bot, trigger) (OpsContentBrief, error)`；写入 `task_briefs` + `issue_checklists`。

- [ ] **Step 1: 写测试**

```go
func TestBuildBriefUsesKnowledgeHitsAndTrends(t *testing.T) {
	b := &BriefBuilder{
		Knowledge: func(context.Context, string) ([]string, error) {
			return []string{"品牌语气：克制", "爆款：清单体"}, nil
		},
		Trends: func(context.Context, string) ([]string, error) {
			return []string{"秋季穿搭"}, nil
		},
		Store: &fakeBriefStore{},
	}
	brief, err := b.Build(context.Background(), BriefInput{
		BotID: 1, PackID: "xiaohongshu", AccountRef: "x1",
		Goal: "涨粉", Audience: "25-35 女性", Format: "note",
	})
	require.NoError(t, err)
	require.Contains(t, brief.SourceRefs, "品牌语气：克制")
	require.Contains(t, brief.SourceRefs, "秋季穿搭")
	require.NotEmpty(t, brief.Acceptance)
}
```

- [ ] **Step 2: 实现**

```go
type OpsContentBrief struct {
	Channel    string   `json:"channel"`
	AccountRef string   `json:"account_ref"`
	Goal       string   `json:"goal"`
	Audience   string   `json:"audience"`
	Format     string   `json:"format"`
	Topic      string   `json:"topic"`
	Acceptance []string `json:"acceptance"`
	SourceRefs []string `json:"source_refs"`
	DueDate    string   `json:"due_date"`
}
```

行为：
- 输入：账号目标、受众、格式、触发源（固定节奏/热点/人工）；品牌 KB 用 `kb_search`；爆款库用已有 memory/blackboard；热点用 data source / info-radar。
- 验收项按平台生成：公众号（标题长度、来源、封面、排版）、小红书（标题 ≤20、图片数、标签、封面比例）、视频（时长、字幕、比例）。
- 写入 `task_briefs`（`goal`/`acceptance_json`/`scope_in_json`/`delivery_json`/`provenance_json`）与 `issue_checklists`。
- 去重：同账号同周同主题 → 返回已存在 issue，不重复创建。

- [ ] **Step 3: 运行测试并提交**

```bash
cd server && go test ./internal/opsbot/content -run TestBuildBrief -v
git add server/internal/opsbot/content
git commit -m "feat(opsbot): generate content briefs from knowledge and trends"
```

---

## Task 14: 图文内容流水线

**Files:**
- Create: `server/internal/opsbot/content/pipeline.go`
- Create: `server/internal/opsbot/content/prompts.go`
- Create: `server/internal/opsbot/content/media.go`
- Test: `server/internal/opsbot/content/pipeline_test.go`

**Interfaces:**
- Produces: `Pipeline.Run(ctx, bot, brief, issueID) (Artifact, error)`；`Artifact` 含文案、图片、排版、版本号。

- [ ] **Step 1: 写测试**

```go
func TestPipelineStagesAndArtifactManifest(t *testing.T) {
	p := &Pipeline{
		Copy:   fakeCopy{},
		Images: fakeImages{},
		Layout: fakeLayout{},
		Store:  &fakeArtifactStore{dir: t.TempDir()},
	}
	art, err := p.Run(context.Background(), BotRef{ID: 1, PackID: "wechat_mp"},
		OpsContentBrief{Topic: "秋季护理", Format: "article"}, 100)
	require.NoError(t, err)
	require.NotEmpty(t, art.CopyPath)
	require.NotEmpty(t, art.Images)
	require.Equal(t, 1, art.Version)
	require.FileExists(t, filepath.Join(art.Dir, "artifacts.json"))
}

func TestPipelineStopsOnStageFailure(t *testing.T) {
	p := &Pipeline{Copy: failingCopy{}}
	_, err := p.Run(context.Background(), BotRef{ID: 1, PackID: "wechat_mp"},
		OpsContentBrief{Format: "article"}, 100)
	require.ErrorContains(t, err, "copy stage")
}
```

- [ ] **Step 2: 实现**

阶段：`topic → copy → visual → layout`。
- `topic`：把 Brief 转成选题与角度，写 `topic.md`。
- `copy`：公众号长文（标题/摘要/正文/CTA/来源）或小红书笔记（钩子/正文/标签/CTA），用 Pack `prompts/`。
- `visual`：封面 + 配图，按 `content_schema` 尺寸生成；调用 `media-studio` 或图片生成 provider。
- `layout`：公众号 HTML 草稿 / 小红书图片顺序与文案配对。
- 产物目录：`<workspace>/ops/content/<issueID>/v<version>/`，`artifacts.json` 记录每阶段文件、provider、耗时、成本。
- 每次修改生成新版本；审批与发布引用 `content_version`。
- 失败停在当前阶段，返回阶段名与具体原因，不进入下一阶段。

- [ ] **Step 3: 运行测试并提交**

```bash
cd server && go test ./internal/opsbot/content -run TestPipeline -v
git add server/internal/opsbot/content
git commit -m "feat(opsbot): add article and note content pipeline"
```

---

## Task 15: 视频流水线

**Files:**
- Create: `server/internal/opsbot/content/video.go`
- Test: `server/internal/opsbot/content/video_test.go`

**Interfaces:**
- Produces: `VideoPipeline.Run(ctx, bot, brief, issueID) (VideoArtifact, error)`。

- [ ] **Step 1: 写测试**

```go
func TestVideoPipelineProducesFinalCut(t *testing.T) {
	v := &VideoPipeline{
		Script:    fakeScript{},
		Storyboard: fakeStoryboard{},
		TTS:       fakeTTS{},
		Frames:    fakeFrames{},
		Subtitles: fakeSubtitles{},
		Compose:   fakeCompose{},
	}
	out, err := v.Run(context.Background(), BotRef{ID: 1, PackID: "xiaohongshu"},
		OpsContentBrief{Topic: "护肤三步", Format: "video"}, 200)
	require.NoError(t, err)
	require.FileExists(t, out.FinalPath)
	require.NotEmpty(t, out.Subtitles)
	require.True(t, out.DurationSec <= 15*60)
}
```

- [ ] **Step 2: 实现**

阶段：`script → storyboard → tts → frames → subtitles → compose → adapt`。
- 工具优先级：现有 video-gen MCP → `media-studio` 素材 + TTS + 合成。
- 平台适配：小红书 3:4、≤15 分钟；公众号视频 ≤30 分钟；封面单独生成。
- 质量门禁：字幕完整、封面存在、时长/比例符合 Pack、无黑帧。
- 产物写入同一 `artifacts.json`，`video` 类型带 `duration_sec`、`ratio`、`subtitle_path`、`cover_path`。
- 成本按 `video` 分类写入 `issue_exec_events`。

- [ ] **Step 3: 运行测试并提交**

```bash
cd server && go test ./internal/opsbot/content -run TestVideo -v
git add server/internal/opsbot/content
git commit -m "feat(opsbot): add video production pipeline"
```

---

## Task 16: 合规与质量门禁

**Files:**
- Create: `server/internal/opsbot/content/review.go`
- Test: `server/internal/opsbot/content/review_test.go`

**Interfaces:**
- Produces: `Review.Run(ctx, bot, artifact, brief) (ReviewResult, error)`；`ReviewResult.Blocking`。

- [ ] **Step 1: 写测试**

```go
func TestReviewBlocksBeforeApproval(t *testing.T) {
	r := &Review{
		Judge: func(context.Context, string, string) ([]Finding, error) {
			return []Finding{{Rule: "ad_law", Severity: "blocking", Message: "绝对化用语"}}, nil
		},
	}
	res, err := r.Run(context.Background(), BotRef{ID: 1}, Artifact{}, OpsContentBrief{})
	require.NoError(t, err)
	require.True(t, res.Blocking)
	require.Len(t, res.Findings, 1)
}

func TestReviewPassesCleanContent(t *testing.T) {
	r := &Review{Judge: func(context.Context, string, string) ([]Finding, error) { return nil, nil }}
	res, err := r.Run(context.Background(), BotRef{ID: 1}, Artifact{}, OpsContentBrief{})
	require.NoError(t, err)
	require.False(t, res.Blocking)
}
```

- [ ] **Step 2: 实现**

四类检查：
1. **事实/合规**：无来源关键断言、广告法绝对化用语、医疗/金融资质、平台红线、版权。
2. **品牌一致性**：语气、术语、视觉规范（读品牌 KB）。
3. **平台适配**：尺寸、时长、标题长度、标签数量、图片数量。
4. **结构完整**：必填字段、CTA、封面、字幕。

实现：
- 用 harness `ai_judge` 做语义判断，输入 `prompt + content + pack.compliance`；输出 `Finding{Rule, Severity, Message, Location}`。
- `blocking` 级 → 返回 `Blocking=true`，**不创建审批**；`warning` 级进入审批卡展示。
- 结果写 `issue_exec_events(kind='gate', summary='ops_review')`，detail 含 findings。
- 审校失败可“重新生成”，生成新内容版本后重新审校。

- [ ] **Step 3: 运行测试并提交**

```bash
cd server && go test ./internal/opsbot/content -run TestReview -v
git add server/internal/opsbot/content
git commit -m "feat(opsbot): add compliance and quality gates"
```

---

## Task 17: 微信公众号 ChannelAdapter

**Files:**
- Create: `server/internal/opsbot/channel/wechat.go`
- Test: `server/internal/opsbot/channel/wechat_test.go`

**Interfaces:**
- Implements `ChannelAdapter`；构造 `NewWeChatAdapter(client WeChatClient)`。

- [ ] **Step 1: 写契约测试**

用 fake MCP transport 覆盖：
- `Publish`：草稿 → 上传图文素材 → 发布/定时；payload 字段映射；返回 `Receipt{ExternalID,URL}`。
- `FetchMetrics`：用户分析 + 图文统计 → 统一键（`views/shares/likes/follower_delta`）。
- `ListEngagements` / `Reply`：MCP 不支持时返回 `AdapterError{Code:"unsupported"}`。
- MCP 错误映射：`40001` → 凭据失效（不可重试）；`45009` → 限流（可重试）。

- [ ] **Step 2: 实现**

```go
type WeChatClient interface {
	DraftAdd(ctx context.Context, req DraftRequest) (string, error)
	UploadMaterial(ctx context.Context, path string) (string, error)
	Publish(ctx context.Context, mediaID string) (string, error)
	PublishScheduled(ctx context.Context, mediaID string, at time.Time) (string, error)
	ArticleStats(ctx context.Context, from, to string) (map[string]float64, error)
	UserSummary(ctx context.Context, from, to string) (map[string]float64, error)
}
```

- `EnsureSession`：校验 AppID/AppSecret 或 MCP token 可用；失败返回凭据失效错误。
- `Publish`：按 `payload` 类型分支（article / image / video）；图片先上传；定时发布记录 `publish_at`。
- `FetchMetrics`：合并两个数据源；缺项填 0；`metrics_map` 在调用方归一化。
- `HealthCheck`：调用轻量接口探测。

- [ ] **Step 3: 运行测试并提交**

```bash
cd server && go test ./internal/opsbot/channel -run WeChat -v
git add server/internal/opsbot/channel
git commit -m "feat(opsbot): add wechat official account adapter"
```

---

## Task 18: 发布编排（幂等、回执、降级）

**Files:**
- Create: `server/internal/opsbot/publish.go`
- Test: `server/internal/opsbot/publish_test.go`

**Interfaces:**
- Produces: `Publisher.Publish(ctx, PublishInput) (PublishResult, error)`。

- [ ] **Step 1: 写测试**

```go
func TestPublishIsIdempotent(t *testing.T) {
	p := newTestPublisher(t)
	in := PublishInput{BotID: 1, IssueID: 2, ContentVersion: 1, IdempotencyKey: "k1"}
	first, err := p.Publish(context.Background(), in)
	require.NoError(t, err)
	second, err := p.Publish(context.Background(), in)
	require.NoError(t, err)
	require.Equal(t, first.RecordID, second.RecordID)
	require.Equal(t, 1, p.adapter.calls, "adapter must be called once")
}

func TestPublishFallsBackToManualPackage(t *testing.T) {
	p := newTestPublisherWithAdapter(t, &failingAdapter{})
	res, err := p.Publish(context.Background(), PublishInput{BotID: 1, IssueID: 2, ContentVersion: 1})
	require.NoError(t, err)
	require.Equal(t, "manual_package", res.Mode)
	require.NotEmpty(t, res.PackageDir)
}
```

- [ ] **Step 2: 实现**

流程：
1. 计算 `idempotency_key = sha256(bot_id + issue_id + content_version + platform + scheduled_at)`。
2. `GetOpsPublishRecord`：已存在且 `status in (pending,published)` → 直接返回（不重复调用）。
3. 预算检查 → 创建 `pending` 记录。
4. `adapter.EnsureSession` → `adapter.Publish`。
5. 成功：存 `receipt/url/screenshot_path/published_at`，状态 `published`，写 `issue_exec_events(kind='terminal')`。
6. 可重试错误：指数退避（3 次）；不可重试：状态 `failed`，生成问题 issue。
7. `ErrCapabilityUnavailable` 或 `recipe broken`：切 `ManualAdapter` 生成发布包，状态 `manual_pending`，通知运营人。
8. 定时发布：`publish_at` 存入记录 metadata，由 scheduler 到点触发。

- [ ] **Step 3: 运行测试并提交**

```bash
cd server && go test ./internal/opsbot -run TestPublish -v
git add server/internal/opsbot
git commit -m "feat(opsbot): add idempotent publish orchestration with manual fallback"
```

---

## Task 19: 指标采集与归一化

**Files:**
- Create: `server/internal/opsbot/metrics.go`
- Test: `server/internal/opsbot/metrics_test.go`

**Interfaces:**
- Produces: `Metrics.Sync(ctx, bot, from, to) error`、`Metrics.Summary(ctx, bot, range) (Summary, error)`。

- [ ] **Step 1: 写测试**

```go
func TestMetricsNormalizeAndUpsert(t *testing.T) {
	m := &Metrics{
		Fetch: func(context.Context, int64, string, string) (map[string]float64, error) {
			return map[string]float64{"read_num": 100, "like_num": 5}, nil
		},
		Map:   map[string]string{"read_num": "views", "like_num": "likes"},
		Store: &fakeMetricsStore{},
	}
	require.NoError(t, m.Sync(context.Background(), BotRef{ID: 1}, "2026-11-01", "2026-11-06"))
	require.Equal(t, float64(100), m.store.last["views"])
	require.Equal(t, float64(5), m.store.last["likes"])
}
```

- [ ] **Step 2: 实现**

- `Sync`：按日期区间调用 `adapter.FetchMetrics`，用 Pack `metrics_map` 归一化，未知键保留原名；`UpsertOpsMetricsDaily`。
- 去重与补零：缺失日期写空对象，便于图表连续。
- `Summary`：区间汇总 + 环比 + 目标达成度（读 `goals_json`）。
- 成本：每次采样的 API/浏览器成本写 `issue_exec_events(kind='cost', category='api')`。

- [ ] **Step 3: 运行测试并提交**

```bash
cd server && go test ./internal/opsbot -run TestMetrics -v
git add server/internal/opsbot
git commit -m "feat(opsbot): add metrics normalization and daily storage"
```

---

## Task 20: Browser Worker 运行时

**Files:**
- Create: `server/internal/opsbot/browser/worker.go`
- Create: `server/internal/opsbot/browser/session.go`
- Test: `server/internal/opsbot/browser/worker_test.go`

**Interfaces:**
- Produces: `Worker.Run(ctx, Task) (Result, error)`、`SessionManager.Ensure/Acquire/Release/State`。

- [ ] **Step 1: 写测试**

```go
func TestWorkerRejectsConcurrentLease(t *testing.T) {
	w := newTestWorker(t)
	require.NoError(t, w.Sessions.Acquire(context.Background(), 1, "local", "w1", time.Minute))
	err := w.Sessions.Acquire(context.Background(), 1, "local", "w2", time.Minute)
	require.ErrorIs(t, err, ErrLeaseBusy)
}

func TestWorkerMarksLoginExpired(t *testing.T) {
	w := newTestWorker(t)
	w.Exec = func(context.Context, Task) (Result, error) {
		return Result{}, ErrLoginExpired
	}
	_, err := w.Run(context.Background(), Task{BotID: 1, Backend: "local", Recipe: "publish"})
	require.ErrorIs(t, err, ErrLoginExpired)
	state, _ := w.Sessions.State(context.Background(), 1, "local")
	require.Equal(t, "expired", state.LoginStatus)
}
```

- [ ] **Step 2: 实现**

- `SessionManager`：`UpsertOpsBrowserSession` 创建 profile 目录 `<data>/ops/browser/<botID>/<backend>/`；`Acquire` 用 CAS 租约查询，0 行 → `ErrLeaseBusy`；`Release` 校验 owner。
- `Worker.Run`：获取租约 → 执行 recipe（Task 21）→ 截图存证 → 更新健康/风险 → 释放租约（defer）。
- 队列：每账号串行 + 全局并发上限；指数退避 + 随机化间隔（`500ms–3s`）。
- 登录失效：返回 `ErrLoginExpired`，写 `login_status=expired`，创建“重新登录”任务并通知（扫码）。
- UI 改版：recipe 断言失败 → `ErrRecipeBroken`，写 `health=degraded`，上层降级人工包。
- 风控信号：限流/验证码/封禁提示 → `risk_state=throttled|blocked`，暂停自动动作。

- [ ] **Step 3: 运行测试并提交**

```bash
cd server && go test ./internal/opsbot/browser -run 'TestWorker|TestSession' -v
git add server/internal/opsbot/browser
git commit -m "feat(opsbot): add browser worker with per-account sessions and leases"
```

---

## Task 21: Browser Recipe DSL 与沙箱

**Files:**
- Create: `server/internal/opsbot/browser/recipe.go`
- Create: `server/internal/opsbot/browser/sandbox.go`
- Test: `server/internal/opsbot/browser/recipe_test.go`
- Testdata: `server/internal/opsbot/browser/testdata/*.html`

**Interfaces:**
- Produces: `RecipeRunner.Run(ctx, Recipe, Input) (Output, error)`；`Sandbox.Run(ctx, Recipe, fixtures) error`。

- [ ] **Step 1: 写测试**

```go
func TestRecipeActionsAndSelectorFallback(t *testing.T) {
	r := Recipe{Name: "publish", Steps: []RecipeStep{
		{Action: "goto", Value: "https://creator.example.com/publish"},
		{Action: "fill", Selector: "#missing", Fallback: []string{"textarea[placeholder]"}, Value: "标题"},
		{Action: "click", Selector: "button.submit"},
		{Action: "assert", Selector: ".success"},
		{Action: "extract", Selector: ".note-url", Value: "url"},
	}}
	out, err := newTestRunner(t).Run(context.Background(), r, Input{Session: "s1"})
	require.NoError(t, err)
	require.Contains(t, out.Values["url"], "note/")
}

func TestRecipeRejectsUnknownActionAndForeignDomain(t *testing.T) {
	require.Error(t, newTestRunner(t).Run(context.Background(),
		Recipe{Steps: []RecipeStep{{Action: "eval", Value: "alert(1)"}}}, Input{}))
	require.Error(t, newTestRunner(t).Run(context.Background(),
		Recipe{Steps: []RecipeStep{{Action: "goto", Value: "https://evil.example"}}}, Input{}))
}
```

- [ ] **Step 2: 实现**

动作白名单：`goto/wait/click/fill/upload/extract/screenshot/assert/loop/branch`。
- 选择器 fallback 按顺序尝试；每步超时默认 15s。
- 域名 allowlist 来自 Pack `manifest` 的 `allowed_hosts`；不含则拒绝。
- `loop`/`branch` 用 `args` 限定次数与条件，禁止无限循环（上限 100 次）。
- `Sandbox`：用 `fixtures` HTML 起本地 HTTP server，跑完整 recipe，断言输出；不访问真实平台。
- 失败分类：`selector_not_found`（可重试/改版）、`assert_failed`（改版）、`timeout`（可重试）、`blocked`（风控）。
- recipe 版本随 Pack 目录保存，页面改版只发新 Pack 版本。

- [ ] **Step 3: 运行测试并提交**

```bash
cd server && go test ./internal/opsbot/browser -run TestRecipe -v
git add server/internal/opsbot/browser
git commit -m "feat(opsbot): add browser recipe dsl and sandbox"
```

---

## Task 22: 小红书 ChannelAdapter

**Files:**
- Create: `server/internal/opsbot/channel/xhs.go`
- Test: `server/internal/opsbot/channel/xhs_test.go`

**Interfaces:**
- Implements `ChannelAdapter`；构造 `NewXHSAdapter(worker BrowserWorker, recipes RecipeSet)`。

- [ ] **Step 1: 写测试**

用 fake worker 覆盖：
- `EnsureSession`：登录态有效 → nil；失效 → `ErrLoginExpired` 并生成扫码任务。
- `Publish`：图片笔记/视频笔记参数映射；返回笔记 URL。
- `FetchMetrics`：创作者后台指标 → 统一键。
- `ListEngagements`：评论/私信 → `[]Engagement`。
- `Reply`：发送并返回回执。
- `HealthCheck`：首页可打开即健康。

- [ ] **Step 2: 实现**

- `Publish`：校验 `content_schema`（标题 ≤20、图片 3:4、≤18 张）→ 调 `publish` recipe → 提取 URL → 截图。
- `FetchMetrics`：调 `metrics` recipe，按日期范围取数。
- `ListEngagements`：调 `engagements` recipe，分页 cursor。
- `Reply`：调 `reply` recipe，返回回执。
- 风控：`risk_state=blocked` 时所有写能力返回不可重试错误。

- [ ] **Step 3: 运行测试并提交**

```bash
cd server && go test ./internal/opsbot/channel -run XHS -v
git add server/internal/opsbot/channel
git commit -m "feat(opsbot): add xiaohongshu browser adapter"
```

---

## Task 23: 互动处理服务

**Files:**
- Create: `server/internal/opsbot/engagement.go`
- Test: `server/internal/opsbot/engagement_test.go`

**Interfaces:**
- Produces: `Engagement.Pull`、`Engagement.Classify`、`Engagement.Suggest`、`Engagement.RequestApproval`、`Engagement.Send`、`Engagement.Escalate`。

- [ ] **Step 1: 写测试**

```go
func TestEngagementFullLifecycle(t *testing.T) {
	e := newTestEngagement(t)
	ctx := context.Background()
	items, err := e.Pull(ctx, BotRef{ID: 1}, "")
	require.NoError(t, err)
	require.Len(t, items, 1)

	require.NoError(t, e.Classify(ctx, BotRef{ID: 1}, items[0].ID))
	require.NoError(t, e.Suggest(ctx, BotRef{ID: 1}, items[0].ID))
	req, err := e.RequestApproval(ctx, BotRef{ID: 1}, items[0].ID)
	require.NoError(t, err)

	require.NoError(t, e.Send(ctx, items[0].ID, req.ID, Decision{Action: "approved"}))
	got, _ := e.Get(ctx, items[0].ID)
	require.Equal(t, "sent", got.Status)
	require.NotEmpty(t, got.Receipt)
}

func TestHighRiskEscalates(t *testing.T) {
	e := newTestEngagement(t)
	item := seedEngagement(t, e, "投诉：要求退款")
	require.NoError(t, e.Classify(context.Background(), BotRef{ID: 1}, item.ID))
	got, _ := e.Get(context.Background(), item.ID)
	require.Equal(t, "escalated", got.Status)
}

func TestEngagementDedupeAndRateLimit(t *testing.T) {
	e := newTestEngagement(t)
	seedEngagement(t, e, "hi")
	items, _ := e.Pull(context.Background(), BotRef{ID: 1}, "")
	require.Len(t, items, 1, "same platform id must not duplicate")
}
```

- [ ] **Step 2: 实现**

状态机严格按设计文档 §7.3：
- `Pull`：`adapter.ListEngagements` → `UpsertOpsEngagement`（去重键 `bot_id + platform_engagement_id`）。
- `Classify`：`inquiry|praise|complaint|negative|spam|lead` + `risk_level`；高风险直接 `escalated`。
- `Suggest`：按分类 + 品牌语气 + Pack prompts 生成建议回复；写 `suggested_reply`。
- `RequestApproval`：`Approval.Create(type=reply)`；`approval_request_id` 回写。
- `Send`：仅 `approved` → `adapter.Reply` → `sent` + receipt；失败 `failed` + 重试。
- 限速：单账号每小时上限（`approval_policy_json.reply_hourly_limit`，默认 20）；超限排队。
- 高风险（投诉/负面/法律/退款）不自动发送，升级总监或运营人。

- [ ] **Step 3: 运行测试并提交**

```bash
cd server && go test ./internal/opsbot -run 'TestEngagement|TestHighRisk' -v
git add server/internal/opsbot
git commit -m "feat(opsbot): add engagement lifecycle with approval gating"
```

---

## Task 24: 风控、Kill Switch 与降级

**Files:**
- Create: `server/internal/opsbot/risk.go`
- Test: `server/internal/opsbot/risk_test.go`

**Interfaces:**
- Produces: `Risk.Kill`、`Risk.Evaluate`、`Risk.HandleAdapterError`。

- [ ] **Step 1: 写测试**

```go
func TestKillSwitchPausesAndCancelsApprovals(t *testing.T) {
	r := newTestRisk(t)
	require.NoError(t, r.Kill(context.Background(), 1, "manual"))
	bot, _ := r.bots.Get(context.Background(), 1)
	require.Equal(t, "disabled", bot.Status)
	require.Equal(t, 0, r.approvals.pendingForBot(1))
}

func TestRiskSignalsPauseAccount(t *testing.T) {
	for _, sig := range []string{"login_expired", "rate_limited", "captcha", "banned"} {
		r := newTestRisk(t)
		require.NoError(t, r.Evaluate(context.Background(), 1, sig))
		bot, _ := r.bots.Get(context.Background(), 1)
		require.Equal(t, "paused", bot.Status, sig)
	}
}
```

- [ ] **Step 2: 实现**

- `Kill`：`SetStatus(disabled)` + `Approval.CancelByBot` + 清空待发送互动 + 通知。
- `Evaluate`：登录失效/限流/验证码/封禁 → `paused` + `risk_state` + 通知；重复信号不重复通知。
- `HandleAdapterError`：`AdapterError.Retryable` 决定重试或暂停；`recipe broken` → 降级人工包并告警。
- 发送频率：全局每账号每小时上限；随机化间隔；不做批量关注/点赞/私信。
- 恢复：运营人 `/resume` 或 MCP `opsbot_resume`，恢复前要求重新登录检查。

- [ ] **Step 3: 运行测试并提交**

```bash
cd server && go test ./internal/opsbot -run TestRisk -v
git add server/internal/opsbot
git commit -m "feat(opsbot): add risk controls and account kill switch"
```

---

## Task 25: 日历与固定节奏

**Files:**
- Create: `server/internal/opsbot/calendar.go`
- Test: `server/internal/opsbot/calendar_test.go`

**Interfaces:**
- Produces: `Calendar.PlanWeek(ctx, bot, week) ([]int64, error)`、`Calendar.List(ctx, filter)`。

- [ ] **Step 1: 写测试**

```go
func TestWeeklyPlanCreatesIssuesAndDedupes(t *testing.T) {
	c := newTestCalendar(t)
	ctx := context.Background()
	ids, err := c.PlanWeek(ctx, BotRef{ID: 1, Cadence: `{"weekly":2}`}, "2026-W45")
	require.NoError(t, err)
	require.Len(t, ids, 2)
	again, err := c.PlanWeek(ctx, BotRef{ID: 1, Cadence: `{"weekly":2}`}, "2026-W45")
	require.NoError(t, err)
	require.Empty(t, again, "same week must not duplicate")
}
```

- [ ] **Step 2: 实现**

- 用 `issues.start_date` / `due_date` + 账号 Epic 的 `parent_issue_id` 表示日历，不建新表。
- `PlanWeek`：读 `cadence_json.weekly`；按账号目标生成 N 个内容 issue（含 Brief）；跳过已存在 `(bot_id, week, slot)`。
- 调度：scheduler 每周固定时间调用总监；总监对每个账号调用 `PlanWeek`。
- 手动排期/改期：更新 `start_date`/`due_date`，不重建 issue。
- 日历查询：按账号、渠道、日期区间返回 issue 列表 + 状态。

- [ ] **Step 3: 运行测试并提交**

```bash
cd server && go test ./internal/opsbot -run TestWeeklyPlan -v
git add server/internal/opsbot
git commit -m "feat(opsbot): add content calendar and weekly cadence"
```

---

## Task 26: 热点驱动

**Files:**
- Create: `server/internal/opsbot/trend.go`
- Test: `server/internal/opsbot/trend_test.go`

**Interfaces:**
- Produces: `Trend.Poll(ctx) ([]Signal, error)`、`Trend.Evaluate(ctx, director, signals) ([]Decision, error)`。

- [ ] **Step 1: 写测试**

```go
func TestTrendDecisionAddsOrDefers(t *testing.T) {
	tr := newTestTrend(t)
	sigs := []Signal{
		{Title: "秋季护肤", Heat: 90, Fit: map[int64]float64{1: 0.9}},
		{Title: "冷门话题", Heat: 10, Fit: map[int64]float64{1: 0.1}},
	}
	decisions, err := tr.Evaluate(context.Background(), BotRef{ID: 0, Role: "director"}, sigs)
	require.NoError(t, err)
	require.Equal(t, "add_now", decisions[0].Action)
	require.Equal(t, "defer", decisions[1].Action)
}
```

- [ ] **Step 2: 实现**

- 信号源：`info-radar` / `social-ops` 场景、data source、手动输入。
- `Evaluate`：相关性（品牌/受众）、账号适配度、预算、日历余量、同质内容；输出 `add_now | merge_next_week | skip`。
- `add_now`：在账号 Epic 下创建加更 issue，插到本周日历，预算预检。
- 去重：同主题 7 天内不重复加更。
- 两种节奏并存：固定节奏先占位，热点加更只插空不覆盖。

- [ ] **Step 3: 运行测试并提交**

```bash
cd server && go test ./internal/opsbot -run TestTrend -v
git add server/internal/opsbot
git commit -m "feat(opsbot): add hot-topic driven planning"
```

---

## Task 27: 运营总监协调

**Files:**
- Create: `server/internal/opsbot/director.go`
- Test: `server/internal/opsbot/director_test.go`

**Interfaces:**
- Produces: `Director.PlanAll`、`Director.AllocateBudget`、`Director.DedupeTopics`、`Director.ProxyApprove`、`Director.Report`。

- [ ] **Step 1: 写测试**

```go
func TestDirectorDedupesAcrossAccounts(t *testing.T) {
	d := newTestDirector(t)
	topics := []Topic{
		{BotID: 1, Title: "秋季护肤三步"},
		{BotID: 2, Title: "秋季护肤 3 步"},
		{BotID: 3, Title: "通勤穿搭"},
	}
	kept, dropped := d.DedupeTopics(topics, 0.8)
	require.Len(t, kept, 2)
	require.Len(t, dropped, 1)
}

func TestDirectorAllocatesBudgetAndProxiesApproval(t *testing.T) {
	d := newTestDirector(t)
	require.NoError(t, d.AllocateBudget(context.Background(), map[int64]float64{1: 100, 2: 200}))
	require.Equal(t, float64(100), d.budget.limits[1])
	require.NoError(t, d.ProxyApprove(context.Background(), 9, 1, "总监代审"))
	require.Equal(t, int64(9), d.approvals.lastProxyFor)
}
```

- [ ] **Step 2: 实现**

- `PlanAll`：为每个账号生成周计划，汇总跨账号日历。
- `DedupeTopics`：标题/正文相似度（归一化 + Jaccard/编辑距离），阈值默认 0.8；保留先到或目标更匹配的一条，另一条换选题。
- `AllocateBudget`：按账号目标/活跃度分配月度额度，写 `budget_json`。
- `ProxyApprove`：审批 `ProxyFor` 记录代审人与理由。
- `Report`：跨账号产出、预算、爆款率、目标达成。
- 通信：`send_workspace_message` + blackboard；不直接改账号内容，只下发任务与预算。

- [ ] **Step 3: 运行测试并提交**

```bash
cd server && go test ./internal/opsbot -run TestDirector -v
git add server/internal/opsbot
git commit -m "feat(opsbot): add operations director coordination"
```

---

## Task 28: 复盘与爆款回流

**Files:**
- Create: `server/internal/opsbot/feedback.go`
- Test: `server/internal/opsbot/feedback_test.go`

**Interfaces:**
- Produces: `Feedback.WeeklyReport`、`Feedback.Breakdown`、`Feedback.WriteBack`。

- [ ] **Step 1: 写测试**

```go
func TestWeeklyReportAndBreakdown(t *testing.T) {
	f := newTestFeedback(t)
	rep, err := f.WeeklyReport(context.Background(), BotRef{ID: 1}, "2026-11-01", "2026-11-07")
	require.NoError(t, err)
	require.NotZero(t, rep.Published)
	require.NotZero(t, rep.AvgViews)

	hits, err := f.Breakdown(context.Background(), BotRef{ID: 1}, "2026-11-01", "2026-11-07")
	require.NoError(t, err)
	require.NotEmpty(t, hits)
	require.NoError(t, f.WriteBack(context.Background(), BotRef{ID: 1}, hits))
	require.True(t, f.kb.written)
}
```

- [ ] **Step 2: 实现**

- 周报：发布数、阅读/曝光、互动率、涨粉、转化、预算消耗、审批通过率/时长、风险事件。
- `Breakdown`：按阅读/互动/涨粉排序，拆解标题、封面、结构、发布时间、选题类型；输出“可复用要素”。
- `WriteBack`：爆款要素写知识库/记忆 + blackboard；下一轮 `BriefBuilder` 的 `SourceRefs` 引用。
- 目标达成度：`goals_json` 的 followers/conversion/brand 指标对比。
- 总监汇总：跨账号爆款率、选题复用率、同质内容比例。

- [ ] **Step 3: 运行测试并提交**

```bash
cd server && go test ./internal/opsbot -run TestWeeklyReport -v
git add server/internal/opsbot
git commit -m "feat(opsbot): add weekly review and hit-content feedback loop"
```

---

## Task 29: Mission control 指标与前端控制台

**Files:**
- Create: `server/web/src/pages/ops-bots.tsx`
- Create: `server/web/src/components/ops/*`
- Create: `server/web/src/types/ops.ts`
- Modify: `server/web/src/router.tsx`
- Modify: `server/web/src/lib/api.ts`
- Modify: `server/internal/service/mission_control.go`（运营面板聚合）
- Test: `server/web/src/pages/__tests__/ops-bots.test.tsx`

**Interfaces:**
- 页面：账号列表、状态、Pack、运营人、预算余量、待审批数、风险状态；下钻到账号详情（日历、指标、互动、审批、浏览器会话）。

- [ ] **Step 1: 实现聚合 API**

Mission control 增加 `ops` 维度：按 owner → 渠道 → 账号返回：状态、今日/本周产出、指标、预算、待办审批、风险。单账号失败不影响整体。

- [ ] **Step 2: 实现页面**

- 遵循 `docs/design-system.md`：warm token、shadcn、lucide、`t()`、Dark mode、4px 网格。
- 列表用 shadcn Table/Card；状态用 Badge；预算用进度条；风险用 Badge + 颜色 token。
- 操作：暂停/恢复、查看审批、查看指标；创建入口引导到对话。
- 空状态：提示“在 IM 里说‘接入公众号’”。

- [ ] **Step 3: 测试并手动验证**

```bash
cd server/web && pnpm lint && pnpm test:run && pnpm build
cd server && go test ./internal/service -run TestMissionControlOps -v
```

再启动 `make dev`，在浏览器走：列表 → 下钻 → 暂停/恢复 → 审批列表；确认 Dark mode 与移动宽度。

- [ ] **Step 4: 提交**

```bash
git add server/web/src/pages/ops-bots.tsx server/web/src/components/ops server/web/src/types/ops.ts \
  server/web/src/router.tsx server/web/src/lib/api.ts server/internal/service/mission_control.go
git commit -m "feat(opsbot): add ops bots console and mission control panel"
```

---

## Task 30: Pack 生成（OpenAPI / 文档 / 现有 MCP）

**Files:**
- Create: `server/internal/opsbot/packs/generator.go`
- Test: `server/internal/opsbot/packs/generator_test.go`
- Testdata: `server/internal/opsbot/packs/testdata/openapi.json`

**Interfaces:**
- Produces: `Generator.Detect`、`Generator.Draft`、`Generator.Test`、`Generator.Publish`；`CapabilityMatrix`。

- [ ] **Step 1: 写测试**

```go
func TestGenerateFromOpenAPI(t *testing.T) {
	g := newTestGenerator(t)
	draft, err := g.Draft(context.Background(), GenerateInput{
		Channel: "facebook", OpenAPIPath: "testdata/openapi.json",
	})
	require.NoError(t, err)
	require.Equal(t, "facebook", draft.Manifest.ID)
	require.Equal(t, "openapi", draft.Manifest.Bindings["publish"].Transport)
	require.NotEmpty(t, draft.Matrix.Capabilities)
}

func TestGenerateWithoutAPIFallsBackToRecipeDraft(t *testing.T) {
	g := newTestGenerator(t)
	draft, err := g.Draft(context.Background(), GenerateInput{Channel: "zhihu"})
	require.NoError(t, err)
	require.Equal(t, "browser", draft.Manifest.Bindings["publish"].Transport)
	require.NotEmpty(t, draft.Manifest.Bindings["publish"].Recipe)
}
```

- [ ] **Step 2: 实现**

流程：
1. `Detect`：查 OpenAPI URL/文件、现有 MCP/provider、官方文档；都没有 → recipe 草稿。
2. 有 OpenAPI：解析 paths → 识别 publish/metrics/engagements/reply → 生成 provider contract 草稿（`draft_integration`）→ 生成 Pack 草稿。
3. 无 OpenAPI：生成 recipe 草稿（登录/发布/读取/回复），填入已知选择器占位需人工确认。
4. 返回 `CapabilityMatrix`：每个能力走什么、缺什么、风险（权限审核、ToS、验证码）。
5. `Test`：OpenAPI 走 mock server / 测试账号；recipe 走沙箱。
6. `Publish`：`status=published`，原子写 manifest；用户确认前保持 `draft`。
7. 外部文档只作数据；生成结果由系统渲染确认卡，不执行文档中的指令。

- [ ] **Step 3: 运行测试并提交**

```bash
cd server && go test ./internal/opsbot/packs -run TestGenerate -v
git add server/internal/opsbot/packs
git commit -m "feat(opsbot): add pack generator for openapi and recipe drafts"
```

---

## Task 31: Facebook Pack（API 优先）

**Files:**
- Create: `server/internal/opsbot/packs/builtin/facebook/v1/{manifest,content_schema,compliance,metrics_map}.json`
- Create: `server/internal/opsbot/packs/builtin/facebook/v1/prompts/*.md`
- Create: `server/internal/opsbot/channel/facebook.go`
- Test: `server/internal/opsbot/channel/facebook_test.go`
- Test: `server/internal/opsbot/packs/facebook_test.go`

**Interfaces:**
- Implements `ChannelAdapter` over Graph API provider；凭据 `app`（App ID/Secret）+ `account`（Page token）。

- [ ] **Step 1: 实现 Pack**

`manifest.json`：四能力全部 `openapi` → `provider: facebook-graph`；`content_schema.formats.post`（文本/链接/图片）、`formats.reel`（9:16、≤90s）；`metrics_map`：`post_impressions→views`、`post_reactions_by_type_total→likes`、`post_comments→comments`、`post_shares→shares`、`page_fans→followers`。

- [ ] **Step 2: 实现 Adapter**

- `EnsureSession`：校验 Page token 与权限（`pages_manage_posts`、`pages_read_engagement`）。
- `Publish`：Page post / photo / reel；定时发布用 `scheduled_publish_time`。
- `FetchMetrics`：Page Insights + post insights。
- `ListEngagements`：comments；`Reply`：comment reply / DM。
- 错误映射：`190` 凭据失效；`4`/`17` 限流（可重试）。

- [ ] **Step 3: E2E（mock Graph API）**

发布 Page post → 回执 URL → 指标入库 → 评论拉取 → 建议回复 → 审批发送。

- [ ] **Step 4: 运行测试并提交**

```bash
cd server && go test ./internal/opsbot/channel -run Facebook -v
cd server && go test ./internal/opsbot/packs -run Facebook -v
git add server/internal/opsbot
git commit -m "feat(opsbot): add facebook pack and graph api adapter"
```

---

## Task 32: 知乎 Pack（浏览器 recipe）

**Files:**
- Create: `server/internal/opsbot/packs/builtin/zhihu/v1/{manifest,content_schema,compliance,metrics_map}.json`
- Create: `server/internal/opsbot/packs/builtin/zhihu/v1/browser/recipes/{login,publish,metrics,engagements,reply}.json`
- Create: `server/internal/opsbot/packs/builtin/zhihu/v1/fixtures/*.html`
- Test: `server/internal/opsbot/packs/zhihu_test.go`
- Test: `server/internal/opsbot/browser/zhihu_recipe_test.go`

- [ ] **Step 1: 实现 Pack**

`manifest.json`：`publish/metrics/engagements/reply` 全部 `browser`；`content_schema.formats.article`（标题、正文、封面、话题标签）；`compliance.json` 含知乎社区规范、广告法、版权。

- [ ] **Step 2: 实现 recipes**

- `login`：打开知乎 → 检测登录 → 扫码。
- `publish`：写文章/回答 → 填标题正文 → 选话题 → 发布 → 提取 URL。
- `metrics`：创作者中心 → 阅读/赞同/评论/收藏。
- `engagements`：评论/私信列表。
- `reply`：定位评论 → 回复 → 发送。

- [ ] **Step 3: 沙箱与 E2E**

沙箱跑通五条 recipe；用 fake worker 跑 adapter 全流程；页面改版只更新 recipe 新版本。

- [ ] **Step 4: 运行测试并提交**

```bash
cd server && go test ./internal/opsbot/packs -run Zhihu -v
cd server && go test ./internal/opsbot/browser -run Zhihu -v
git add server/internal/opsbot
git commit -m "feat(opsbot): add zhihu pack with browser recipes"
```

---

## Task 33: 浏览器后端契约与团队版接入点

**Files:**
- Create: `server/internal/opsbot/browser/backend.go`
- Create: `server/internal/opsbot/browser/backend_local.go`
- Test: `server/internal/opsbot/browser/backend_test.go`
- Docs: `docs/operations/ops-bot-browser-backends.md`

**Interfaces:**
- Produces: `BrowserBackend`：`Start`、`Stop`、`Session`、`Exec`、`Health`；`LocalBackend` 完整实现。

- [ ] **Step 1: 定义后端契约**

```go
type BrowserBackend interface {
	Start(ctx context.Context, session SessionSpec) (SessionHandle, error)
	Stop(ctx context.Context, handle SessionHandle) error
	Exec(ctx context.Context, handle SessionHandle, recipe Recipe, in Input) (Output, error)
	Health(ctx context.Context, handle SessionHandle) (Health, error)
}

type SessionSpec struct {
	BotID      int64
	Backend    string // local | cloud
	ProfileDir string
	AllowedHosts []string
}
```

- [ ] **Step 2: 实现本地后端**

`LocalBackend` 用 Playwright/CDP 启动、per-account profile、域名 allowlist、截图目录；与 `Worker` 共用租约与风控状态。

- [ ] **Step 3: 团队版接入点**

- `CloudBackend` 契约：每账号独立容器/VM、租户标签、固定出口、对象存储回执；实现位于 enterprise 仓库，通过同一 `BrowserBackend` 注入。
- 本仓库提供 `NewBackend(cfg)` 工厂：`local` 返回 `LocalBackend`；`cloud` 返回 `UnsupportedBackend` 并给出明确配置错误（不静默降级）。
- 文档写明企业版实现需满足的隔离与审计要求。

- [ ] **Step 4: 运行测试并提交**

```bash
cd server && go test ./internal/opsbot/browser -run TestBackend -v
git add server/internal/opsbot/browser docs/operations/ops-bot-browser-backends.md
git commit -m "feat(opsbot): add browser backend contract and local backend"
```

---

## Task 34: 全链路 E2E 验收与运维文档

**Files:**
- Create: `server/internal/opsbot/e2e_test.go`
- Create: `docs/operations/ops-bot-runbook.md`
- Modify: `docs/superpowers/specs/2026-11-06-niuniu-ops-bot-design.md`（仅在实现中发现偏差时）

- [ ] **Step 1: E2E 场景**

1. 对话创建公众号 Bot → 授权 → 选题 → 文案/封面 → 合规 → 审批 → 发布 → 指标。
2. 同渠道第二账号复用 Pack，预算/审批/指标独立。
3. 对话创建小红书 Bot → 扫码 → 发布图文 → 拉指标 → 评论建议 → 审批发送。
4. 视频任务 → 成片 → 审批 → 发布。
5. 审批驳回/修改 → 重新生成 → 再次审批。
6. 预算超限 → 暂停 → 通知 → 调额恢复。
7. 登录失效 / UI 改版 → 降级人工包 → 告警。
8. 总监周计划 + 热点加更 + 跨账号去重。
9. 新增 Facebook Pack（生成或内置）→ 绑定 Page → 发布/指标/评论。
10. 知乎 Pack → 沙箱 → 绑定账号 → 发布。
11. Pack 导出 → 另一 owner 导入 → 绑定账号。
12. Kill switch → 所有自动动作停止、pending 审批取消。

- [ ] **Step 2: 全量验证**

```bash
cd server && go test ./internal/store ./internal/opsbot/... ./internal/service ./internal/api -race -v
cd .. && make schema-diff && make sqlc
cd server/web && pnpm lint && pnpm test:run && pnpm build
```

- [ ] **Step 3: 运维文档**

`docs/operations/ops-bot-runbook.md`：能力边界、对话接入、账号授权、Pack 管理、预算/审批默认值、风控与降级、回滚、常见错误（登录失效/限流/改版/凭据失效）。

- [ ] **Step 4: 提交**

```bash
git add server/internal/opsbot/e2e_test.go docs/operations/ops-bot-runbook.md
git commit -m "test(opsbot): add end-to-end acceptance and runbook"
```

---

## Self-Review

**设计覆盖（对应设计文档 §16 完整能力清单）：**

| 能力 | 任务 |
|---|---|
| C1 Bot 与账号管理 | 6、7 |
| C2 对话式接入 | 10、11 |
| C3 Platform Pack | 3、4、5 |
| C4 Pack 生成 | 30、31、32 |
| C5 图文生产 | 13、14 |
| C6 视频生产 | 15 |
| C7 合规与质量门禁 | 16 |
| C8 发布 | 17、18、22、31、32 |
| C9 指标 | 19 |
| C10 互动 | 23、24 |
| C11 浏览器运行时 | 20、21、22、33 |
| C12 总监协调 | 25、26、27 |
| C13 复盘回流 | 28 |
| C14 可观测与治理 | 8、9、12、29、34 |

**延迟实现扫描：**

- 全文无 `TBD` / `TODO` / “留到以后补” / “先用 X 后替换”。
- 计划不按阶段组织；任务编号仅表示依赖顺序。
- `ManualAdapter` 是正式的能力降级路径，不是临时桩。
- `CloudBackend` 在个人版仓库以明确 `UnsupportedBackend` 暴露，团队版按同一契约实现；这是发行版边界，不是需求延期。

**类型一致性：**

- `store.OpsBot` / `OpsPublishRecord` / `OpsMetricsDaily` / `OpsEngagement` / `OpsBrowserSession` 与方法名贯穿各任务。
- `ChannelAdapter`、`CapabilitySet`、`Receipt`、`Engagement`、`AdapterError` 在 2/17/22/31/32 一致。
- `Manifest`、`Binding`、`Registry`、`Validate` 在 3/4/5/30 一致。
- `OwnerRef`、`CreateAccountInput`、`ConfigPatch`、`Approval`、`Budget`、`Provisioner`、`Onboarding` 在 6–12 一致。
