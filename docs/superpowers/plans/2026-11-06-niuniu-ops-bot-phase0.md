# 牛牛运营 Bot Phase 0 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 落地运营 Bot 的可运行骨架：ChannelAdapter / Platform Pack 契约、内置 WeChat/XHS Pack、Ops Bot CRUD、多账号、对话式接入、预算与审批配置；不执行任何内容生产或发布。

**Architecture:** 在现有 `server/internal/service`、`store`、`api`、`cmd/niuniu-mcp` 之上新增 `server/internal/opsbot`。Ops Bot 是账号级配置实体，复用 Epic/Workspace 作为控制工作空间；平台差异收敛到 `ChannelAdapter` + Platform Pack；IM 对话复用现有 imbot 入站管线。

**Tech Stack:** Go 1.25、Gin、sqlc、SQLite/PostgreSQL、MCP（mark3labs/mcp-go）、React（Phase 0 仅复用现有 IM 对话，不新增页面）。

## Global Constraints

- 设计文档：`docs/superpowers/specs/2026-11-06-niuniu-ops-bot-design.md`；实现前先读 §3、§5、§11、§13。
- 双数据库：所有 DDL 同时写入 `server/internal/store/schema.sql` 与 `schema_postgres.sql`；跑 `make schema-diff`。
- sqlc：新增/修改 `store/queries/*.sql` 后跑 `make sqlc`；查询注释必须是纯 ASCII。
- 迁移列用 `addColumnIfNotExists`；迁移列不加 CHECK。
- 不新建聊天系统：复用 `imbot` 入站/出站/卡片。
- 不新建执行引擎：内容任务复用 `issues` + `workspaces`。
- 凭据只存 `credential_refs_json`；日志、对话、prompt、Pack 文件中不得出现明文密钥。
- Phase 0 不执行内容生产、发布、互动；只做配置、绑定、预算/审批骨架。
- 所有 owner/project 访问走现有 `Authz`；写操作必须 `EnsureOwnerWritable`。
- 每个任务结束前跑对应测试并提交；提交信息不添加 `Co-Authored-By`。

---

## File Structure

| 路径 | 职责 |
|---|---|
| `server/internal/store/schema.sql`、`schema_postgres.sql` | 新增 10 张运营域表 |
| `server/internal/store/queries/opsbot.sql` | Ops Bot / Pack / Approval / Budget 查询 |
| `server/internal/store/migrate_opsbot_test.go` | 迁移与表结构测试 |
| `server/internal/opsbot/channel/adapter.go` | ChannelAdapter 契约、CapabilitySet |
| `server/internal/opsbot/packs/types.go` | Pack manifest、校验 |
| `server/internal/opsbot/packs/registry.go` | Pack 存储、版本、发布 |
| `server/internal/opsbot/packs/builtin.go` | 内置 WeChat/XHS Pack 安装 |
| `server/internal/opsbot/packs/builtin/wechat_mp/v1/*` | 公众号 Pack |
| `server/internal/opsbot/packs/builtin/xiaohongshu/v1/*` | 小红书 Pack |
| `server/internal/opsbot/service.go` | Ops Bot CRUD、多账号、配置 |
| `server/internal/opsbot/provision.go` | 账号 Epic + 控制工作空间 |
| `server/internal/opsbot/budget.go` | 月度预算 |
| `server/internal/opsbot/approval.go` | 审批状态机 |
| `server/internal/opsbot/onboarding.go` | 对话式接入状态机 |
| `server/internal/api/opsbot.go` | 极简 HTTP 控制台 |
| `server/internal/server/server.go`、`router.go` | 服务/路由接线 |
| `server/cmd/niuniu-mcp/opsbot_tools.go`、`main.go` | MCP 工具 |
| `docs/operations/ops-bot-phase0.md` | Phase 0 上线/验收说明 |

---

### Task 1: 数据模型与 sqlc 查询

**Files:**
- Modify: `server/internal/store/schema.sql`
- Modify: `server/internal/store/schema_postgres.sql`
- Create: `server/internal/store/queries/opsbot.sql`
- Test: `server/internal/store/migrate_opsbot_test.go`

**Interfaces:**
- Produces: `store.Queries` 方法：`CreateOpsBot`、`GetOpsBot`、`ListOpsBotsByOwner`、`UpdateOpsBot`、`CreateOpsBotChat`、`GetOpsBotByChat`、`CreatePlatformPack`、`GetLatestPublishedPack`、`CreateOpsApproval`、`ListPendingOpsApprovals`、`DecideOpsApproval`、`GetOpsBudgetUsage`、`UpsertOpsBudgetUsage`。

- [ ] **Step 1: 写迁移测试（先失败）**

```go
// server/internal/store/migrate_opsbot_test.go
package store

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpsBotSchemaFreshAndRepeatable(t *testing.T) {
	Driver = "sqlite"
	db := openTestDB(t)
	Migrate(db)
	Migrate(db)

	for _, tbl := range []string{
		"ops_platform_packs", "ops_bots", "ops_bot_chats", "ops_browser_workers",
		"ops_calendar_items", "ops_publish_records", "ops_approvals",
		"ops_engagement_items", "ops_metrics_daily", "ops_budget_usage",
	} {
		var n int
		require.NoError(t, db.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&n))
		require.Equal(t, 1, n, "table %s missing", tbl)
	}

	_, err := db.Exec(`INSERT INTO ops_bots (owner_type, owner_id, role, pack_id, account_ref, display_name)
		VALUES ('user', 1, 'account', 'wechat_mp', 'acc-a', 'A')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO ops_bots (owner_type, owner_id, role, pack_id, account_ref, display_name)
		VALUES ('user', 1, 'account', 'wechat_mp', 'acc-a', 'A2')`)
	require.Error(t, err, "duplicate account_ref must be rejected")
}
```

- [ ] **Step 2: 运行测试，确认表不存在**

Run: `cd server && go test ./internal/store -run TestOpsBotSchemaFreshAndRepeatable -v`  
Expected: FAIL，提示 `no such table: ops_bots`。

- [ ] **Step 3: 双写 DDL**

把设计文档 §11.2 的 10 张表按顺序追加到 `schema.sql` 末尾；在 `schema_postgres.sql` 中做等价转换：

- `INTEGER PRIMARY KEY AUTOINCREMENT` → `BIGSERIAL PRIMARY KEY`
- `TIMESTAMP` → `TIMESTAMPTZ`
- `REAL` → `DOUBLE PRECISION`
- `TEXT` 保持不变

索引随表定义一起添加：

```sql
CREATE INDEX IF NOT EXISTS idx_ops_bots_owner ON ops_bots(owner_type, owner_id, status);
CREATE INDEX IF NOT EXISTS idx_ops_bot_chats_chat ON ops_bot_chats(im_chat_id);
CREATE INDEX IF NOT EXISTS idx_ops_approvals_pending ON ops_approvals(bot_id, status);
CREATE INDEX IF NOT EXISTS idx_ops_calendar_bot ON ops_calendar_items(bot_id, planned_at);
CREATE INDEX IF NOT EXISTS idx_ops_publish_bot ON ops_publish_records(bot_id, created_at);
CREATE INDEX IF NOT EXISTS idx_ops_metrics_bot ON ops_metrics_daily(bot_id, date);
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
SELECT * FROM ops_bots
WHERE owner_type = ? AND owner_id = ?
ORDER BY role DESC, id ASC;

-- name: GetOpsBotByAccount :one
SELECT * FROM ops_bots
WHERE owner_type = ? AND owner_id = ? AND pack_id = ? AND account_ref = ?;

-- name: UpdateOpsBotStatus :one
UPDATE ops_bots SET status = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? RETURNING *;

-- name: UpdateOpsBotConfig :one
UPDATE ops_bots SET
    display_name = ?, persona_json = ?, credential_refs_json = ?,
    knowledge_scope_json = ?, goals_json = ?, cadence_json = ?,
    approval_policy_json = ?, budget_json = ?, pack_version = ?,
    updated_at = CURRENT_TIMESTAMP
WHERE id = ? RETURNING *;

-- name: SetOpsBotProvision :one
UPDATE ops_bots SET epic_issue_id = ?, control_workspace_id = ?, updated_at = CURRENT_TIMESTAMP
WHERE id = ? RETURNING *;

-- name: CreateOpsBotChat :one
INSERT INTO ops_bot_chats (bot_id, im_chat_id, thread_ext_id)
VALUES (?, ?, ?) RETURNING *;

-- name: GetOpsBotByChat :one
SELECT b.* FROM ops_bots b
JOIN ops_bot_chats c ON c.bot_id = b.id
WHERE c.im_chat_id = ? AND c.thread_ext_id = ?
LIMIT 1;

-- name: CreatePlatformPack :one
INSERT INTO ops_platform_packs (
    owner_type, owner_id, pack_id, version, status, manifest_json, path, digest, created_by
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING *;

-- name: GetLatestPublishedPack :one
SELECT * FROM ops_platform_packs
WHERE owner_type = ? AND owner_id = ? AND pack_id = ? AND status = 'published'
ORDER BY version DESC LIMIT 1;

-- name: PublishPlatformPack :one
UPDATE ops_platform_packs
SET status = 'published', published_at = CURRENT_TIMESTAMP
WHERE id = ? RETURNING *;

-- name: CreateOpsApproval :one
INSERT INTO ops_approvals (bot_id, issue_id, type, payload_json, preview_json)
VALUES (?, ?, ?, ?, ?) RETURNING *;

-- name: ListPendingOpsApprovals :many
SELECT * FROM ops_approvals WHERE status = 'pending' ORDER BY created_at ASC;

-- name: DecideOpsApproval :one
UPDATE ops_approvals
SET status = ?, operator_id = ?, decided_at = CURRENT_TIMESTAMP, reason = ?
WHERE id = ? AND status = 'pending' RETURNING *;

-- name: GetOpsBudgetUsage :one
SELECT * FROM ops_budget_usage WHERE bot_id = ? AND period = ?;

-- name: UpsertOpsBudgetUsage :one
INSERT INTO ops_budget_usage (bot_id, period, text_cost, image_cost, video_cost, total_cost, limit_cost)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(bot_id, period) DO UPDATE SET
    text_cost = excluded.text_cost,
    image_cost = excluded.image_cost,
    video_cost = excluded.video_cost,
    total_cost = excluded.total_cost,
    limit_cost = excluded.limit_cost,
    updated_at = CURRENT_TIMESTAMP
RETURNING *;
```

- [ ] **Step 5: 生成并验证**

Run:

```bash
cd server
go run ./cmd/niuniu admin schema-diff
go test ./internal/store -run TestOpsBotSchemaFreshAndRepeatable -v
cd .. && make sqlc
```

Expected: schema-diff 输出 `OK: no table-level drift detected`；测试 PASS；sqlc 无 diff。

- [ ] **Step 6: 提交**

```bash
git add server/internal/store/schema.sql server/internal/store/schema_postgres.sql \
  server/internal/store/queries/opsbot.sql server/internal/store/migrate_opsbot_test.go \
  server/internal/store/*.sql.go
git commit -m "feat(opsbot): add ops bot schema and queries"
```

---

### Task 2: ChannelAdapter 与 Pack 类型/校验

**Files:**
- Create: `server/internal/opsbot/channel/adapter.go`
- Create: `server/internal/opsbot/packs/types.go`
- Test: `server/internal/opsbot/packs/types_test.go`

**Interfaces:**
- Produces: `channel.ChannelAdapter`、`channel.CapabilitySet`、`packs.Manifest`、`packs.Validate(m)`。

- [ ] **Step 1: 写失败测试**

```go
package packs

import "testing"

func TestValidateRejectsBrowserBindingWithoutRecipe(t *testing.T) {
	m := Manifest{
		ID: "zhihu", Version: 1, Status: StatusDraft,
		Capabilities: []string{"publish"},
		Bindings: map[string]Binding{
			"publish": {Transport: "browser"},
		},
	}
	if err := Validate(m); err == nil {
		t.Fatal("expected browser binding without recipe to fail")
	}
}
```

- [ ] **Step 2: 运行测试，确认类型不存在**

Run: `cd server && go test ./internal/opsbot/packs -v`  
Expected: FAIL（包/类型未定义）。

- [ ] **Step 3: 写类型**

```go
// server/internal/opsbot/channel/adapter.go
package channel

import "context"

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
	Type   string // comment | dm
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
```

```go
// server/internal/opsbot/packs/types.go
package packs

import (
	"fmt"
	"strings"
)

type Status string

const (
	StatusDraft     Status = "draft"
	StatusTesting   Status = "testing"
	StatusPublished Status = "published"
	StatusDeprecated Status = "deprecated"
)

type Binding struct {
	Transport string `yaml:"transport" json:"transport"` // openapi | mcp | browser | manual
	Provider  string `yaml:"provider,omitempty" json:"provider,omitempty"`
	Recipe    string `yaml:"recipe,omitempty" json:"recipe,omitempty"`
}

type Manifest struct {
	ID           string             `yaml:"id" json:"id"`
	Version      int                `yaml:"version" json:"version"`
	Status       Status             `yaml:"status" json:"status"`
	DisplayName  string             `yaml:"display_name" json:"display_name"`
	Capabilities []string           `yaml:"capabilities" json:"capabilities"`
	Bindings     map[string]Binding `yaml:"bindings" json:"bindings"`
}

var validTransports = map[string]bool{
	"openapi": true, "mcp": true, "browser": true, "manual": true,
}

func Validate(m Manifest) error {
	if strings.TrimSpace(m.ID) == "" {
		return fmt.Errorf("pack id is required")
	}
	if m.Version <= 0 {
		return fmt.Errorf("pack version must be > 0")
	}
	if len(m.Capabilities) == 0 {
		return fmt.Errorf("pack must declare at least one capability")
	}
	for _, cap := range m.Capabilities {
		b, ok := m.Bindings[cap]
		if !ok {
			return fmt.Errorf("capability %q has no binding", cap)
		}
		if !validTransports[b.Transport] {
			return fmt.Errorf("capability %q has unknown transport %q", cap, b.Transport)
		}
		if b.Transport == "browser" && strings.TrimSpace(b.Recipe) == "" {
			return fmt.Errorf("capability %q uses browser but has no recipe", cap)
		}
	}
	return nil
}
```

- [ ] **Step 4: 运行测试**

Run: `cd server && go test ./internal/opsbot/packs -v`  
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add server/internal/opsbot/channel server/internal/opsbot/packs
git commit -m "feat(opsbot): add channel adapter and pack contract"
```

---

### Task 3: Pack 存储与版本发布

**Files:**
- Create: `server/internal/opsbot/packs/registry.go`
- Test: `server/internal/opsbot/packs/registry_test.go`

**Interfaces:**
- Consumes: `packs.Manifest`、`packs.Validate`。
- Produces: `packs.Registry`：`SaveDraft`、`Load`、`Publish`、`List`、`Root`。

- [ ] **Step 1: 写失败测试**

```go
package packs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRegistryRejectsPathTraversal(t *testing.T) {
	r := &Registry{DataDir: t.TempDir()}
	if _, err := r.Root("user", 1, "../evil", 1); err == nil {
		t.Fatal("expected path traversal to be rejected")
	}
}

func TestRegistrySaveLoadPublish(t *testing.T) {
	r := &Registry{DataDir: t.TempDir()}
	m := Manifest{ID: "demo", Version: 1, Status: StatusDraft,
		Capabilities: []string{"publish"},
		Bindings: map[string]Binding{"publish": {Transport: "manual"}},
	}
	if err := r.SaveDraft("user", 1, m, nil); err != nil {
		t.Fatal(err)
	}
	got, err := r.Load("user", 1, "demo", 1)
	if err != nil || got.ID != "demo" {
		t.Fatalf("load: %v %+v", err, got)
	}
	if _, err := os.Stat(filepath.Join(r.DataDir, "users", "1", "ops", "packs", "demo", "1", "manifest.json")); err != nil {
		t.Fatal("pack file missing")
	}
}
```

- [ ] **Step 2: 运行测试，确认失败**

Run: `cd server && go test ./internal/opsbot/packs -run TestRegistry -v`  
Expected: FAIL（Registry 未定义）。

- [ ] **Step 3: 实现 Registry**

```go
package packs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

type Registry struct {
	DataDir string
}

var safeID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

func (r *Registry) Root(ownerType string, ownerID int64, packID string, version int) (string, error) {
	if !safeID.MatchString(packID) {
		return "", fmt.Errorf("invalid pack id %q", packID)
	}
	if version <= 0 {
		return "", fmt.Errorf("invalid pack version %d", version)
	}
	dir := "users"
	if ownerType == "org" {
		dir = "orgs"
	}
	return filepath.Join(r.DataDir, dir, strconv.FormatInt(ownerID, 10),
		"ops", "packs", packID, strconv.Itoa(version)), nil
}

func (r *Registry) SaveDraft(ownerType string, ownerID int64, m Manifest, files map[string][]byte) error {
	if err := Validate(m); err != nil {
		return err
	}
	root, err := r.Root(ownerType, ownerID, m.ID, m.Version)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), b, 0o644); err != nil {
		return err
	}
	for name, data := range files {
		if filepath.IsAbs(name) || filepath.Base(name) != name {
			return fmt.Errorf("invalid pack file %q", name)
		}
		if err := os.WriteFile(filepath.Join(root, name), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (r *Registry) Load(ownerType string, ownerID int64, packID string, version int) (Manifest, error) {
	root, err := r.Root(ownerType, ownerID, packID, version)
	if err != nil {
		return Manifest{}, err
	}
	b, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return Manifest{}, err
	}
	return m, Validate(m)
}

func (r *Registry) Digest(ownerType string, ownerID int64, packID string, version int) (string, error) {
	root, err := r.Root(ownerType, ownerID, packID, version)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}
```

- [ ] **Step 4: 运行测试**

Run: `cd server && go test ./internal/opsbot/packs -v`  
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add server/internal/opsbot/packs
git commit -m "feat(opsbot): add pack registry and version storage"
```

---

### Task 4: 内置 WeChat / XHS Pack

**Files:**
- Create: `server/internal/opsbot/packs/builtin/wechat_mp/v1/manifest.json`
- Create: `server/internal/opsbot/packs/builtin/xiaohongshu/v1/manifest.json`
- Create: `server/internal/opsbot/packs/builtin.go`
- Test: `server/internal/opsbot/packs/builtin_test.go`

**Interfaces:**
- Produces: `packs.InstallBuiltins(reg *Registry, ownerType string, ownerID int64) error`。

- [ ] **Step 1: 写内置 Pack 文件**

`wechat_mp/v1/manifest.json`：

```json
{
  "id": "wechat_mp",
  "version": 1,
  "status": "published",
  "display_name": "微信公众号",
  "capabilities": ["publish", "metrics", "engagements", "reply"],
  "bindings": {
    "publish":     {"transport": "mcp", "provider": "wechat-mp"},
    "metrics":     {"transport": "mcp", "provider": "wechat-mp"},
    "engagements": {"transport": "mcp", "provider": "wechat-mp"},
    "reply":       {"transport": "mcp", "provider": "wechat-mp"}
  }
}
```

`xiaohongshu/v1/manifest.json`（Phase 0 只登记能力；Phase 2 切到 browser）：

```json
{
  "id": "xiaohongshu",
  "version": 1,
  "status": "published",
  "display_name": "小红书",
  "capabilities": ["publish", "metrics", "engagements", "reply"],
  "bindings": {
    "publish":     {"transport": "manual"},
    "metrics":     {"transport": "manual"},
    "engagements": {"transport": "manual"},
    "reply":       {"transport": "manual"}
  }
}
```

- [ ] **Step 2: 写失败测试**

```go
package packs

import "testing"

func TestInstallBuiltins(t *testing.T) {
	r := &Registry{DataDir: t.TempDir()}
	if err := InstallBuiltins(r, "user", 1); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"wechat_mp", "xiaohongshu"} {
		m, err := r.Load("user", 1, id, 1)
		if err != nil || m.ID != id {
			t.Fatalf("load %s: %v %+v", id, err, m)
		}
	}
}
```

- [ ] **Step 3: 实现 builtin.go**

```go
package packs

import (
	"embed"
	"encoding/json"
	"path/filepath"
)

//go:embed builtin/*/v1/manifest.json
var builtinFS embed.FS

func InstallBuiltins(r *Registry, ownerType string, ownerID int64) error {
	for _, id := range []string{"wechat_mp", "xiaohongshu"} {
		p := filepath.ToSlash(filepath.Join("builtin", id, "v1", "manifest.json"))
		b, err := builtinFS.ReadFile(p)
		if err != nil {
			return err
		}
		var m Manifest
		if err := json.Unmarshal(b, &m); err != nil {
			return err
		}
		m.Status = StatusPublished
		if err := r.SaveDraft(ownerType, ownerID, m, nil); err != nil {
			return err
		}
	}
	return nil
}
```

> 内置 Pack 使用 JSON，避免为 Phase 0 引入 YAML 依赖。Phase 2 增加 browser recipe 时再把 recipe 文件纳入同一目录。

- [ ] **Step 4: 运行测试**

Run: `cd server && go test ./internal/opsbot/packs -run TestInstallBuiltins -v`  
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add server/internal/opsbot/packs
git commit -m "feat(opsbot): add built-in wechat and xiaohongshu packs"
```

---

### Task 5: Ops Bot CRUD 与多账号

**Files:**
- Create: `server/internal/opsbot/service.go`
- Test: `server/internal/opsbot/service_test.go`

**Interfaces:**
- Consumes: `*store.Queries`、`packs.Registry`。
- Produces: `opsbot.Service`：`CreateAccountBot`、`CreateDirector`、`Get`、`ListByOwner`、`Pause`、`Update`。

- [ ] **Step 1: 写失败测试**

```go
package opsbot

import (
	"context"
	"database/sql"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/opsbot/packs"
	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/stretchr/testify/require"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_foreign_keys=ON")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	store.Driver = "sqlite"
	store.Migrate(db)
	reg := &packs.Registry{DataDir: t.TempDir()}
	require.NoError(t, packs.InstallBuiltins(reg, "user", 1))
	return NewService(store.New(db), reg)
}

func TestCreateTwoAccountsSamePack(t *testing.T) {
	svc := newTestService(t)
	_, err := svc.CreateAccountBot(context.Background(), CreateAccountInput{
		Owner: OwnerRef{Type: "user", ID: 1}, PackID: "wechat_mp",
		AccountRef: "acc-a", DisplayName: "A", CreatedBy: 1,
	})
	require.NoError(t, err)
	_, err = svc.CreateAccountBot(context.Background(), CreateAccountInput{
		Owner: OwnerRef{Type: "user", ID: 1}, PackID: "wechat_mp",
		AccountRef: "acc-b", DisplayName: "B", CreatedBy: 1,
	})
	require.NoError(t, err)
}
```

- [ ] **Step 2: 运行测试，确认失败**

Run: `cd server && go test ./internal/opsbot -run TestCreateTwoAccounts -v`  
Expected: FAIL（Service 未定义）。

- [ ] **Step 3: 实现 service.go**

```go
package opsbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/niuniu-dev/niuniu/internal/opsbot/packs"
	"github.com/niuniu-dev/niuniu/internal/store"
)

type OwnerRef struct {
	Type string
	ID   int64
}

type CreateAccountInput struct {
	Owner       OwnerRef
	PackID      string
	PackVersion int64
	AccountRef  string
	DisplayName string
	PersonaJSON string
	CredentialRefsJSON string
	GoalsJSON   string
	CadenceJSON string
	ApprovalPolicyJSON string
	BudgetJSON  string
	DirectorBotID int64
	CreatedBy   int64
}

type Service struct {
	q     *store.Queries
	packs *packs.Registry
}

func NewService(q *store.Queries, packsReg *packs.Registry) *Service {
	return &Service{q: q, packs: packsReg}
}

func (s *Service) CreateAccountBot(ctx context.Context, in CreateAccountInput) (store.OpsBot, error) {
	if in.AccountRef == "" || in.PackID == "" {
		return store.OpsBot{}, fmt.Errorf("pack_id and account_ref are required")
	}
	m, version, err := s.resolvePack(ctx, in.Owner, in.PackID, in.PackVersion)
	if err != nil {
		return store.OpsBot{}, fmt.Errorf("resolve pack: %w", err)
	}
	if m.Status != packs.StatusPublished {
		return store.OpsBot{}, fmt.Errorf("pack %s v%d is not published", in.PackID, version)
	}
	return s.q.CreateOpsBot(ctx, store.CreateOpsBotParams{
		OwnerType: in.Owner.Type, OwnerID: in.Owner.ID, Role: "account",
		PackID: in.PackID, PackVersion: version, AccountRef: in.AccountRef,
		DisplayName: in.DisplayName, PersonaJson: defaultJSON(in.PersonaJSON),
		CredentialRefsJson: defaultJSON(in.CredentialRefsJSON),
		KnowledgeScopeJson: "{}", GoalsJson: defaultJSON(in.GoalsJSON),
		CadenceJson: defaultJSON(in.CadenceJSON),
		ApprovalPolicyJson: defaultApprovalPolicy(in.ApprovalPolicyJSON),
		BudgetJson: defaultJSON(in.BudgetJSON),
		DirectorBotID: sql.NullInt64{Int64: in.DirectorBotID, Valid: in.DirectorBotID > 0},
	})
}

func (s *Service) resolvePack(ctx context.Context, owner OwnerRef, packID string, version int64) (packs.Manifest, int64, error) {
	if version <= 0 {
		row, err := s.q.GetLatestPublishedPack(ctx, store.GetLatestPublishedPackParams{
			OwnerType: owner.Type, OwnerID: owner.ID, PackID: packID,
		})
		if err != nil {
			return packs.Manifest{}, 0, err
		}
		version = row.Version
	}
	m, err := s.packs.Load(owner.Type, owner.ID, packID, int(version))
	return m, version, err
}

func (s *Service) CreateDirector(ctx context.Context, owner OwnerRef, displayName, budgetJSON string, createdBy int64) (store.OpsBot, error) {
	return s.q.CreateOpsBot(ctx, store.CreateOpsBotParams{
		OwnerType: owner.Type, OwnerID: owner.ID, Role: "director",
		PackID: "", PackVersion: 0, AccountRef: "",
		DisplayName: displayName, PersonaJson: "{}", CredentialRefsJson: "{}",
		KnowledgeScopeJson: "{}", GoalsJson: "{}", CadenceJson: "{}",
		ApprovalPolicyJson: "{}", BudgetJson: defaultJSON(budgetJSON),
		DirectorBotID: sql.NullInt64{},
	})
}

func (s *Service) Get(ctx context.Context, id int64) (store.OpsBot, error) {
	return s.q.GetOpsBot(ctx, id)
}

func (s *Service) ListByOwner(ctx context.Context, owner OwnerRef) ([]store.OpsBot, error) {
	return s.q.ListOpsBotsByOwner(ctx, store.ListOpsBotsByOwnerParams{
		OwnerType: owner.Type, OwnerID: owner.ID,
	})
}

func (s *Service) Pause(ctx context.Context, id int64) (store.OpsBot, error) {
	return s.q.UpdateOpsBotStatus(ctx, store.UpdateOpsBotStatusParams{Status: "paused", ID: id})
}

type UpdateInput struct {
	DisplayName        string
	PersonaJSON        string
	CredentialRefsJSON string
	KnowledgeScopeJSON string
	GoalsJSON          string
	CadenceJSON        string
	ApprovalPolicyJSON string
	BudgetJSON         string
	PackVersion        int64
}

func (s *Service) Update(ctx context.Context, id int64, in UpdateInput) (store.OpsBot, error) {
	return s.q.UpdateOpsBotConfig(ctx, store.UpdateOpsBotConfigParams{
		DisplayName: in.DisplayName, PersonaJson: defaultJSON(in.PersonaJSON),
		CredentialRefsJson: defaultJSON(in.CredentialRefsJSON),
		KnowledgeScopeJson: defaultJSON(in.KnowledgeScopeJSON),
		GoalsJson: defaultJSON(in.GoalsJSON), CadenceJson: defaultJSON(in.CadenceJSON),
		ApprovalPolicyJson: defaultApprovalPolicy(in.ApprovalPolicyJSON),
		BudgetJson: defaultJSON(in.BudgetJSON), PackVersion: in.PackVersion, ID: id,
	})
}

func defaultJSON(v string) string {
	if v == "" {
		return "{}"
	}
	return v
}

func defaultApprovalPolicy(v string) string {
	if v == "" {
		b, _ := json.Marshal(map[string]bool{"publish": true, "reply": true, "video": true})
		return string(b)
	}
	return v
}
```

- [ ] **Step 4: 运行测试**

Run: `cd server && go test ./internal/opsbot -run TestCreateTwoAccounts -v`  
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add server/internal/opsbot
git commit -m "feat(opsbot): add account bot service with multi-account support"
```

---

### Task 6: 账号 Epic + 控制工作空间

**Files:**
- Create: `server/internal/opsbot/provision.go`
- Test: `server/internal/opsbot/provision_test.go`

**Interfaces:**
- Consumes: narrow interfaces `IssueCreator`、`WorkspaceCreator`（由 `service.KanbanService` / `service.WorkspaceService` 适配）。
- Produces: `Provisioner.ProvisionAccount(ctx, owner, projectID, botID, title, createdBy) (issueID, workspaceID, error)`。

- [ ] **Step 1: 写失败测试**

```go
type fakeIssues struct{ created int64 }
func (f *fakeIssues) CreateEpic(ctx context.Context, columnID int64, title, desc string, by int64) (int64, error) {
	f.created++
	return 900 + f.created, nil
}
type fakeWorkspaces struct{ issueID int64 }
func (f *fakeWorkspaces) CreateControl(ctx context.Context, issueID int64, owner string, ownerID int64, by int64) (int64, error) {
	f.issueID = issueID
	return 700 + issueID, nil
}

func TestProvisionAccountCreatesEpicAndWorkspace(t *testing.T) {
	p := &Provisioner{
		Issues: &fakeIssues{}, Workspaces: &fakeWorkspaces{},
		FirstColumn: func(ctx context.Context, projectID int64) (int64, error) { return 1, nil },
	}
	issueID, wsID, err := p.ProvisionAccount(context.Background(),
		OwnerRef{Type: "user", ID: 1}, 42, 7, "公众号 A", 1)
	if err != nil || issueID == 0 || wsID == 0 {
		t.Fatalf("provision: %v %d %d", err, issueID, wsID)
	}
}
```

- [ ] **Step 2: 实现 provision.go**

```go
package opsbot

import "context"

type IssueCreator interface {
	CreateEpic(ctx context.Context, columnID int64, title, description string, byUserID int64) (int64, error)
}

type WorkspaceCreator interface {
	CreateControl(ctx context.Context, issueID int64, ownerType string, ownerID int64, createdBy int64) (int64, error)
}

type Provisioner struct {
	Issues     IssueCreator
	Workspaces WorkspaceCreator
	FirstColumn func(ctx context.Context, projectID int64) (int64, error)
}

func (p *Provisioner) ProvisionAccount(ctx context.Context, owner OwnerRef, projectID, botID int64, title string, createdBy int64) (int64, int64, error) {
	col, err := p.FirstColumn(ctx, projectID)
	if err != nil {
		return 0, 0, err
	}
	issueID, err := p.Issues.CreateEpic(ctx, col, title, "运营 Bot 控制 Epic", createdBy)
	if err != nil {
		return 0, 0, err
	}
	wsID, err := p.Workspaces.CreateControl(ctx, issueID, owner.Type, owner.ID, createdBy)
	if err != nil {
		return 0, 0, err
	}
	return issueID, wsID, nil
}
```

- [ ] **Step 3: 运行测试**

Run: `cd server && go test ./internal/opsbot -run TestProvisionAccount -v`  
Expected: PASS。

- [ ] **Step 4: 提交**

```bash
git add server/internal/opsbot/provision.go server/internal/opsbot/provision_test.go
git commit -m "feat(opsbot): provision account epic and control workspace"
```

---

### Task 7: 预算与审批骨架

**Files:**
- Create: `server/internal/opsbot/budget.go`
- Create: `server/internal/opsbot/approval.go`
- Test: `server/internal/opsbot/budget_test.go`
- Test: `server/internal/opsbot/approval_test.go`

**Interfaces:**
- Produces: `Budget.Check(ctx, botID, cost) error`、`Budget.Record(ctx, botID, period, kind, cost) error`；`Approval.Create/ListPending/Decide`。

- [ ] **Step 1: 写失败测试**

```go
func TestBudgetRejectsOverLimit(t *testing.T) {
	b := &Budget{Usage: fakeUsage{spent: 990, limit: 1000}}
	if err := b.Check(context.Background(), 1, 20); err == nil {
		t.Fatal("expected over-limit rejection")
	}
}

func TestApprovalRejectsDoubleDecision(t *testing.T) {
	a := &Approval{}
	req, _ := a.New(1, 0, "publish", "{}", "{}")
	_, _ = a.Decide(context.Background(), req.ID, 1, "approved", "")
	if _, err := a.Decide(context.Background(), req.ID, 1, "rejected", ""); err == nil {
		t.Fatal("expected second decision to fail")
	}
}
```

- [ ] **Step 2: 实现 budget.go / approval.go**

Budget 只依赖一个 `UsageStore` 接口：

```go
type UsageStore interface {
	Get(ctx context.Context, botID int64, period string) (spent, limit float64, err error)
	Add(ctx context.Context, botID int64, period, kind string, cost float64) error
}

type Budget struct{ Usage UsageStore }

func currentPeriod() string { return time.Now().Format("2006-01") }

func (b *Budget) Check(ctx context.Context, botID int64, cost float64) error {
	spent, limit, err := b.Usage.Get(ctx, botID, currentPeriod())
	if err != nil {
		return err
	}
	if limit > 0 && spent+cost > limit {
		return fmt.Errorf("budget exceeded: %.2f + %.2f > %.2f", spent, cost, limit)
	}
	return nil
}

type fakeUsage struct{ spent, limit float64 }

func (f fakeUsage) Get(context.Context, int64, string) (float64, float64, error) {
	return f.spent, f.limit, nil
}
func (f fakeUsage) Add(context.Context, int64, string, string, float64) error { return nil }
```

Approval 用内存状态机实现 Phase 0 验收；生产环境把同一接口落到 `ops_approvals`：

```go
type ApprovalRequest struct {
	ID      int64
	BotID   int64
	IssueID int64
	Type    string
	Status  string // pending | approved | rejected
}

type Approval struct {
	nextID int64
	items  map[int64]*ApprovalRequest
}

func (a *Approval) New(botID, issueID int64, typ, payload, preview string) (ApprovalRequest, error) {
	a.nextID++
	if a.items == nil {
		a.items = map[int64]*ApprovalRequest{}
	}
	req := &ApprovalRequest{ID: a.nextID, BotID: botID, IssueID: issueID, Type: typ, Status: "pending"}
	a.items[req.ID] = req
	return *req, nil
}

func (a *Approval) Decide(ctx context.Context, id, operator int64, status, reason string) (ApprovalRequest, error) {
	req, ok := a.items[id]
	if !ok {
		return ApprovalRequest{}, fmt.Errorf("approval %d not found", id)
	}
	if req.Status != "pending" {
		return ApprovalRequest{}, fmt.Errorf("approval %d is already %s", id, req.Status)
	}
	if status != "approved" && status != "rejected" {
		return ApprovalRequest{}, fmt.Errorf("invalid decision %q", status)
	}
	req.Status = status
	return *req, nil
}
```

- [ ] **Step 3: 运行测试**

Run: `cd server && go test ./internal/opsbot -run 'TestBudget|TestApproval' -v`  
Expected: PASS。

- [ ] **Step 4: 提交**

```bash
git add server/internal/opsbot/budget.go server/internal/opsbot/approval.go \
  server/internal/opsbot/budget_test.go server/internal/opsbot/approval_test.go
git commit -m "feat(opsbot): add budget and approval skeleton"
```

---

### Task 8: 对话式接入状态机

**Files:**
- Create: `server/internal/opsbot/onboarding.go`
- Test: `server/internal/opsbot/onboarding_test.go`
- Modify: `server/internal/service/imbot_inbound.go`（在正常路由前拦截 onboarding 会话）

**Interfaces:**
- Produces: `Onboarding.Handle(ctx, chatID, text) (reply string, done bool, err error)`；`Onboarding.Start(ctx, chatID, packID)`。

- [ ] **Step 1: 写对话测试**

```go
package opsbot

import (
	"context"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
)

type fakeBotCreator struct{ created []CreateAccountInput }

func (f *fakeBotCreator) CreateAccountBot(_ context.Context, in CreateAccountInput) (store.OpsBot, error) {
	f.created = append(f.created, in)
	return store.OpsBot{ID: int64(len(f.created))}, nil
}

type fakeProvisioner struct{}

func (fakeProvisioner) ProvisionAccount(context.Context, OwnerRef, int64, int64, string, int64) (int64, int64, error) {
	return 1, 2, nil
}

func newTestOnboarding(t *testing.T) *Onboarding {
	t.Helper()
	return &Onboarding{
		Service:     &fakeBotCreator{},
		Provisioner: fakeProvisioner{},
		Sessions:    map[int64]*OnboardingSession{},
	}
}

func TestOnboardingCreatesAccountBot(t *testing.T) {
	o := newTestOnboarding(t)
	owner := OwnerRef{Type: "user", ID: 1}
	reply, done, err := o.Handle(context.Background(), 10, owner, 42, "接入公众号")
	if err != nil || done {
		t.Fatal(err)
	}
	if reply == "" {
		t.Fatal("expected first question")
	}
	for _, ans := range []string{"品牌 A", "涨粉", "每周 2 篇", "需要"} {
		reply, done, err = o.Handle(context.Background(), 10, owner, 42, ans)
		if err != nil {
			t.Fatal(err)
		}
	}
	if !done {
		t.Fatal("expected onboarding to finish")
	}
}
```

- [ ] **Step 2: 实现状态机**

```go
package opsbot

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/store"
)

type BotCreator interface {
	CreateAccountBot(context.Context, CreateAccountInput) (store.OpsBot, error)
}

type AccountProvisioner interface {
	ProvisionAccount(context.Context, OwnerRef, int64, int64, string, int64) (int64, int64, error)
}

type OnboardingSession struct {
	ChatID     int64
	Owner      OwnerRef
	ProjectID  int64
	PackID     string
	Account    string
	Goal       string
	Cadence    string
	NeedApproval bool
	Step       string
}

type Onboarding struct {
	Service     BotCreator
	Provisioner AccountProvisioner
	Sessions    map[int64]*OnboardingSession
}

func (o *Onboarding) Handle(ctx context.Context, chatID int64, owner OwnerRef, projectID int64, text string) (string, bool, error) {
	s := o.Sessions[chatID]
	if s == nil {
		switch text {
		case "接入公众号":
			o.Sessions[chatID] = &OnboardingSession{
				ChatID: chatID, Owner: owner, ProjectID: projectID,
				PackID: "wechat_mp", Step: "account",
			}
			return "账号名称是？", false, nil
		case "接入小红书":
			o.Sessions[chatID] = &OnboardingSession{
				ChatID: chatID, Owner: owner, ProjectID: projectID,
				PackID: "xiaohongshu", Step: "account",
			}
			return "账号名称是？", false, nil
		}
		return "", false, nil
	}
	if text == "/cancel" {
		delete(o.Sessions, chatID)
		return "已取消接入。", true, nil
	}
	switch s.Step {
	case "account":
		s.Account = text
		s.Step = "goal"
		return "主要目标是涨粉、转化还是品牌？", false, nil
	case "goal":
		s.Goal = text
		s.Step = "cadence"
		return "每周发几篇？要热点加更吗？", false, nil
	case "cadence":
		s.Cadence = text
		s.Step = "approval"
		return "发布前需要你确认吗？", false, nil
	case "approval":
		s.NeedApproval = strings.Contains(text, "需要")
		goals, _ := json.Marshal(map[string]string{"primary": s.Goal})
		cadence, _ := json.Marshal(map[string]string{"summary": s.Cadence})
		bot, err := o.Service.CreateAccountBot(ctx, CreateAccountInput{
			Owner: s.Owner, PackID: s.PackID, AccountRef: s.Account,
			DisplayName: s.Account, GoalsJSON: string(goals),
			CadenceJSON: string(cadence),
			CreatedBy: s.Owner.ID,
		})
		if err != nil {
			return "", false, err
		}
		if _, _, err := o.Provisioner.ProvisionAccount(ctx, s.Owner, s.ProjectID, bot.ID, s.Account, s.Owner.ID); err != nil {
			return "", false, err
		}
		delete(o.Sessions, chatID)
		return "已创建「" + s.Account + "」运营 Bot，可在对话里继续调整目标、节奏、预算和审批。", true, nil
	}
	return "", false, nil
}
```

- `Handle` 根据 chatID 取内存会话；每步只问一个问题。
- `done` 时调用 `Service.CreateAccountBot` + `Provisioner.ProvisionAccount`。
- 凭据步骤返回一次性链接提示，不在对话中收密钥。
- 支持 `/cancel` 取消。
- 生产环境把 `Sessions` 落到 `ops_onboarding_sessions` 或复用 imbot 会话状态；Phase 0 先用进程内 map。

- [ ] **Step 3: 接入 imbot**

在 `imbot_inbound.go` 的 `HandleInbound` 中，正常 `resolveTask` 之前插入：

```go
if s.opsOnboarding != nil {
	if owner, ok := s.projectOwner(ctx, chat.ProjectID.Int64); ok {
		if reply, done, err := s.opsOnboarding.Handle(ctx, chat.ID, owner, chat.ProjectID.Int64, text); err == nil && (reply != "" || done) {
			s.pushText(ctx, channel, ev.ChatExtID, ev.ThreadExtID, reply)
			return
		}
	}
}
```

- [ ] **Step 4: 运行测试**

Run: `cd server && go test ./internal/opsbot ./internal/service -run 'TestOnboarding|TestIMBot' -v`  
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add server/internal/opsbot/onboarding.go server/internal/opsbot/onboarding_test.go \
  server/internal/service/imbot_inbound.go
git commit -m "feat(opsbot): add conversation onboarding flow"
```

---

### Task 9: HTTP API、路由与 MCP 工具

**Files:**
- Create: `server/internal/api/opsbot.go`
- Modify: `server/internal/server/server.go`
- Modify: `server/internal/server/router.go`
- Create: `server/cmd/niuniu-mcp/opsbot_tools.go`
- Modify: `server/cmd/niuniu-mcp/main.go`
- Test: `server/internal/api/opsbot_test.go`

**Interfaces:**
- Produces: `GET/PATCH /api/ops/bots`、`GET /api/ops/packs`、`GET /api/ops/approvals`、`POST /api/ops/approvals/:id/approve|reject`；MCP `opsbot_*`、`ops_pack_*`、`ops_approval_*`。

- [ ] **Step 1: 写 handler 测试**

在 `server/internal/api/opsbot_test.go` 中复用 `intake_completion_contract_test.go` 的 router/DB harness，增加四个用例：

- 合法 owner 创建账号 Bot → 200，返回 bot id。
- 跨 owner 创建 → 403。
- 同一 Pack 创建第二个账号 → 200。
- 重复 `account_ref` → 400/409（按现有错误映射）。

测试数据用内置 `wechat_mp` Pack；不需要真实公众号凭据。

- [ ] **Step 2: 实现 handler**

- `OpsBotHandler` 持有 `*opsbot.Service`、`*opsbot.Provisioner`、`*service.Authz`。
- `POST /api/ops/bots`：解析 owner/project、`EnsureOwnerWritable`，调用 `CreateAccountBot` + `ProvisionAccount`。
- `GET /api/ops/bots`：按 owner 列表。
- `PATCH /api/ops/bots/:id`：pause/resume/config。
- `GET /api/ops/packs`：列出 owner 的 Pack。
- `GET /api/ops/approvals`：pending 列表。
- `POST /api/ops/approvals/:id/approve|reject`：操作人校验。

- [ ] **Step 3: 路由接线**

`router.go` 增加：

```go
api.GET("/ops/bots", s.opsBotHandler.List)
api.POST("/ops/bots", s.opsBotHandler.Create)
api.PATCH("/ops/bots/:id", s.opsBotHandler.Update)
api.GET("/ops/packs", s.opsBotHandler.ListPacks)
api.GET("/ops/approvals", s.opsBotHandler.ListApprovals)
api.POST("/ops/approvals/:id/approve", s.opsBotHandler.Approve)
api.POST("/ops/approvals/:id/reject", s.opsBotHandler.Reject)
```

`server.go` 构造 `opsbot.NewService`、`packs.NewRegistry`、`opsbot.NewProvisioner`、`api.NewOpsBotHandler`。

- [ ] **Step 4: MCP 工具**

`opsbot_tools.go` 注册：

- `opsbot_create` / `opsbot_list` / `opsbot_pause`
- `ops_pack_list` / `ops_pack_generate` / `ops_pack_publish`
- `ops_approval_list` / `ops_approval_approve` / `ops_approval_reject`

在 `main.go` 的 `registerIntakeTools` 旁边加 `registerOpsBotTools(s, api, pid)`。

- [ ] **Step 5: 运行测试**

Run: `cd server && go test ./internal/api ./cmd/niuniu-mcp -run 'OpsBot|Ops' -v`  
Expected: PASS。

- [ ] **Step 6: 提交**

```bash
git add server/internal/api/opsbot.go server/internal/server/server.go server/internal/server/router.go \
  server/cmd/niuniu-mcp/opsbot_tools.go server/cmd/niuniu-mcp/main.go
git commit -m "feat(opsbot): add http api and mcp tools"
```

---

### Task 10: 端到端验收与 Phase 0 文档

**Files:**
- Create: `server/internal/opsbot/phase0_e2e_test.go`
- Create: `docs/operations/ops-bot-phase0.md`
- Modify: `README.md`（可选：加一段 Phase 0 能力说明）

- [ ] **Step 1: 写 E2E 测试**

覆盖：

1. IM 对话 `接入公众号` → 创建账号 Bot → 账号 Epic + 控制 workspace 存在。
2. 同一 Pack 再创建第二个账号 → 两个 Ops Bot，独立 `account_ref`、预算、审批人。
3. 重复 `account_ref` → 返回错误。
4. 预算超限 → `Budget.Check` 返回错误。
5. 审批：创建 publish 审批 → 运营人通过 → 第二次通过失败。
6. 未授权 owner 创建 → 403。

- [ ] **Step 2: 运行全量验收**

```bash
cd server
go test ./internal/store ./internal/opsbot ./internal/service ./internal/api -run 'OpsBot|Onboarding|Provision|Budget|Approval' -v
cd ..
make schema-diff
make sqlc
```

Expected: 全部 PASS；schema-diff `OK`。

- [ ] **Step 3: 写 Phase 0 文档**

`docs/operations/ops-bot-phase0.md` 至少包含：

- Phase 0 能力边界（只配置，不生产/发布）。
- 对话式接入流程。
- 多账号与凭据层级。
- 内置 Pack 列表。
- 预算/审批默认值。
- 回滚：暂停 Bot、删除 Pack 版本、恢复旧版本。

- [ ] **Step 4: 提交**

```bash
git add server/internal/opsbot/phase0_e2e_test.go docs/operations/ops-bot-phase0.md README.md
git commit -m "test(opsbot): add phase 0 end-to-end acceptance"
```

---

## Self-Review

**Spec coverage:**

- ChannelAdapter / Platform Pack：Task 2、3、4。
- API 优先绑定：Task 2 的 `Binding` + Task 4 的 WeChat Pack；解析器在 Phase 1 接入，Phase 0 只存绑定。
- Pack 存储/生命周期：Task 3。
- Ops Bot CRUD / 多账号：Task 5。
- 账号 Epic + 控制工作空间：Task 6。
- 预算 / 审批：Task 7。
- 对话式接入：Task 8。
- HTTP / MCP：Task 9。
- 测试/验收：Task 10。

**Placeholder scan:** 计划中没有 `TBD` / `TODO`；Phase 0 明确不实现内容生产、发布、互动和浏览器 recipe，这些属于 Phase 1–3。

**Type consistency:** `Manifest`、`Binding`、`Registry`、`Service`、`Provisioner`、`Budget`、`Approval`、`Onboarding` 的命名在各任务中保持一致；`store.Queries` 方法名与 Task 1 的查询名一致。
