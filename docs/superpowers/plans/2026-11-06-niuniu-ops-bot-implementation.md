# 牛牛运营 Bot 全阶段实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 分五个阶段落地运营 Bot：Phase 0 骨架、Phase 1 公众号内容闭环、Phase 2 小红书浏览器闭环、Phase 3 视频/团队云浏览器/多账号、Phase 4 Pack 泛化与 Facebook/知乎。

**Architecture:** 复用现有 Epic/Workspace/Task Brief/autohost/imbot/integration/data source；新增 `server/internal/opsbot` 作为账号级配置与编排层。平台差异收敛到 `ChannelAdapter` + 文件系统 Platform Pack；不新建聊天系统、执行引擎或平台专用核心适配器。

**Tech Stack:** Go 1.25、Gin、sqlc、SQLite/PostgreSQL、MCP（mark3labs/mcp-go）、React 19（对话优先，极简控制台）、Playwright/CDP（Phase 2+）。

## Global Constraints

- 设计文档：`docs/superpowers/specs/2026-11-06-niuniu-ops-bot-design.md`；实现前先读对应章节。
- **最小数据模型**：只新增 `ops_bots`、`ops_publish_records`、`ops_metrics_daily` 三张表；`im_bot_chats.ops_bot_id`、`agent_ask_user_requests.kind/metadata_json` 三个列。其余复用现有表。
- 双数据库：DDL 同时写入 `server/internal/store/schema.sql` 与 `schema_postgres.sql`；跑 `make schema-diff`。
- sqlc：修改 `store/queries/*.sql` 后跑 `make sqlc`；查询注释必须纯 ASCII。
- 迁移列用 `addColumnIfNotExists`；迁移列不加 CHECK。
- 不新建聊天系统；复用 imbot 入站/出站/卡片。
- 不新建执行引擎；内容任务复用 `issues` + `workspaces`。
- 凭据只存 `credential_refs_json`；日志、对话、prompt、Pack 文件中不得出现明文密钥。
- 外部文本只作数据，不构成指令；所有外发经 egress/授权/审计。
- 每个任务结束前跑对应测试并提交；提交信息不添加 `Co-Authored-By`。

---

## Phase Map

| Phase | 目标 | 关键交付 | 依赖 |
|---|---|---|---|
| **0 骨架** | 可创建账号 Bot、绑定多账号、配置预算/审批 | 3 表 + 3 列、ChannelAdapter、Pack 文件、OpsBot CRUD、对话接入、HTTP/MCP | 无 |
| **1 公众号** | 公众号图文闭环 | WeChat Adapter、内容流水线、合规门禁、审批、发布、指标、日历 | Phase 0 |
| **2 小红书** | 小红书图文闭环 | 本地 Browser Worker、XHS Adapter、互动建议、风控降级 | Phase 0 |
| **3 规模** | 视频、团队云浏览器、多账号 | 视频流水线、云端 Worker、总监跨账号协调 | Phase 1+2 |
| **4 泛化** | 新渠道不改核心 | Pack 生成、Recipe DSL、Facebook/知乎 Pack、热点驱动 | Phase 3 |

---

# Phase 0：骨架

## Task 1: 最小数据模型与 sqlc 查询

**Files:**
- Modify: `server/internal/store/schema.sql`
- Modify: `server/internal/store/schema_postgres.sql`
- Modify: `server/internal/store/migrate.go`
- Create: `server/internal/store/queries/opsbot.sql`
- Test: `server/internal/store/migrate_opsbot_test.go`

**Interfaces:**
- Produces: `store.Queries`：`CreateOpsBot`、`GetOpsBot`、`ListOpsBotsByOwner`、`GetOpsBotByAccount`、`UpdateOpsBotStatus`、`UpdateOpsBotConfig`、`SetOpsBotProvision`、`CreateOpsPublishRecord`、`UpsertOpsMetricsDaily`、`SumOpsAccountCost`。

- [ ] **Step 1: 写迁移测试（先失败）**

```go
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

	for _, tbl := range []string{"ops_bots", "ops_publish_records", "ops_metrics_daily"} {
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

	_, err := db.Exec(`INSERT INTO ops_bots (owner_type, owner_id, role, pack_id, account_ref, display_name)
		VALUES ('user', 1, 'account', 'wechat_mp', 'acc-a', 'A')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO ops_bots (owner_type, owner_id, role, pack_id, account_ref, display_name)
		VALUES ('user', 1, 'account', 'wechat_mp', 'acc-a', 'A2')`)
	require.Error(t, err, "duplicate account_ref must be rejected")
}
```

- [ ] **Step 2: 运行测试，确认失败**

Run: `cd server && go test ./internal/store -run TestOpsBotSchemaFreshAndRepeatable -v`  
Expected: FAIL，`no such table: ops_bots`。

- [ ] **Step 3: 双写 DDL**

把设计文档 §11.2 的 3 张表追加到 `schema.sql`，在 `schema_postgres.sql` 做等价转换（`AUTOINCREMENT` → `BIGSERIAL`，`TIMESTAMP` → `TIMESTAMPTZ`，`REAL` → `DOUBLE PRECISION`）。同时在 `schema.sql` 的 `im_bot_chats` / `agent_ask_user_requests` 表定义中加列，并在 `migrate.go` 用 `addColumnIfNotExists` 给存量 DB 补列。

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
SELECT * FROM ops_bots WHERE owner_type = ? AND owner_id = ? ORDER BY role DESC, id ASC;

-- name: GetOpsBotByAccount :one
SELECT * FROM ops_bots WHERE owner_type = ? AND owner_id = ? AND pack_id = ? AND account_ref = ?;

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

-- name: CreateOpsPublishRecord :one
INSERT INTO ops_publish_records (
    issue_id, bot_id, pack_id, account_ref, content_version, idempotency_key, status
) VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING *;

-- name: GetOpsPublishRecord :one
SELECT * FROM ops_publish_records WHERE bot_id = ? AND idempotency_key = ?;

-- name: UpsertOpsMetricsDaily :one
INSERT INTO ops_metrics_daily (bot_id, date, metrics_json, source)
VALUES (?, ?, ?, ?)
ON CONFLICT(bot_id, date) DO UPDATE SET metrics_json = excluded.metrics_json, source = excluded.source
RETURNING *;

-- name: SumOpsAccountCost :one
SELECT CAST(COALESCE(SUM(e.cost_usd), 0) AS REAL)
FROM issue_exec_events e
JOIN issues i ON i.id = e.issue_id
WHERE i.parent_issue_id = ? AND e.created_at >= ? AND e.created_at < ?;
```

- [ ] **Step 5: 生成并验证**

```bash
cd server
go run ./cmd/niuniu admin schema-diff
go test ./internal/store -run TestOpsBotSchemaFreshAndRepeatable -v
cd .. && make sqlc
```

Expected: schema-diff `OK`；测试 PASS；sqlc 无 diff。

- [ ] **Step 6: 提交**

```bash
git add server/internal/store/schema.sql server/internal/store/schema_postgres.sql \
  server/internal/store/migrate.go server/internal/store/queries/opsbot.sql \
  server/internal/store/migrate_opsbot_test.go server/internal/store/*.sql.go
git commit -m "feat(opsbot): add minimal ops bot schema and queries"
```

---

## Task 2: ChannelAdapter 与 Pack 类型/校验

**Files:**
- Create: `server/internal/opsbot/channel/adapter.go`
- Create: `server/internal/opsbot/packs/types.go`
- Test: `server/internal/opsbot/packs/types_test.go`

**Interfaces:**
- Produces: `channel.ChannelAdapter`、`channel.CapabilitySet`、`packs.Manifest`、`packs.Validate`。

- [ ] **Step 1: 写失败测试**

```go
package packs

import "testing"

func TestValidateRejectsBrowserBindingWithoutRecipe(t *testing.T) {
	m := Manifest{ID: "zhihu", Version: 1, Capabilities: []string{"publish"},
		Bindings: map[string]Binding{"publish": {Transport: "browser"}}}
	if err := Validate(m); err == nil {
		t.Fatal("expected browser binding without recipe to fail")
	}
}
```

- [ ] **Step 2: 运行测试，确认失败**

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

type Receipt struct{ ExternalID, URL, Raw string }
type Engagement struct{ ExtID, Type, Author, Text string }

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

type Binding struct {
	Transport string `json:"transport"` // openapi | mcp | browser | manual
	Provider  string `json:"provider,omitempty"`
	Recipe    string `json:"recipe,omitempty"`
}

type Manifest struct {
	ID           string             `json:"id"`
	Version      int                `json:"version"`
	Status       string             `json:"status"` // draft | testing | published | deprecated
	DisplayName  string             `json:"display_name"`
	Capabilities []string           `json:"capabilities"`
	Bindings     map[string]Binding `json:"bindings"`
}

var validTransports = map[string]bool{"openapi": true, "mcp": true, "browser": true, "manual": true}

func Validate(m Manifest) error {
	if strings.TrimSpace(m.ID) == "" { return fmt.Errorf("pack id is required") }
	if m.Version <= 0 { return fmt.Errorf("pack version must be > 0") }
	if len(m.Capabilities) == 0 { return fmt.Errorf("pack must declare at least one capability") }
	for _, c := range m.Capabilities {
		b, ok := m.Bindings[c]
		if !ok { return fmt.Errorf("capability %q has no binding", c) }
		if !validTransports[b.Transport] { return fmt.Errorf("capability %q has unknown transport %q", c, b.Transport) }
		if b.Transport == "browser" && strings.TrimSpace(b.Recipe) == "" {
			return fmt.Errorf("capability %q uses browser but has no recipe", c)
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

## Task 3: Pack 文件系统存储与内置 Pack

**Files:**
- Create: `server/internal/opsbot/packs/registry.go`
- Create: `server/internal/opsbot/packs/builtin.go`
- Create: `server/internal/opsbot/packs/builtin/wechat_mp/v1/manifest.json`
- Create: `server/internal/opsbot/packs/builtin/xiaohongshu/v1/manifest.json`
- Test: `server/internal/opsbot/packs/registry_test.go`

**Interfaces:**
- Produces: `packs.Registry`：`Root`、`Save`、`Load`、`List`、`Publish`、`InstallBuiltins`。

- [ ] **Step 1: 写失败测试**

```go
func TestRegistryRejectsPathTraversal(t *testing.T) {
	r := &Registry{DataDir: t.TempDir()}
	if _, err := r.Root("user", 1, "../evil", 1); err == nil {
		t.Fatal("expected path traversal rejection")
	}
}

func TestRegistrySaveLoadAndList(t *testing.T) {
	r := &Registry{DataDir: t.TempDir()}
	m := Manifest{ID: "demo", Version: 1, Status: "published",
		Capabilities: []string{"publish"}, Bindings: map[string]Binding{"publish": {Transport: "manual"}}}
	if err := r.Save("user", 1, m); err != nil { t.Fatal(err) }
	got, err := r.Load("user", 1, "demo", 1)
	if err != nil || got.ID != "demo" { t.Fatalf("load: %v %+v", err, got) }
	list, err := r.List("user", 1)
	if err != nil || len(list) != 1 { t.Fatalf("list: %v %+v", err, list) }
}
```

- [ ] **Step 2: 实现 registry.go**

```go
package packs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
)

type Registry struct{ DataDir string }

var safeID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

func (r *Registry) Root(ownerType string, ownerID int64, packID string, version int) (string, error) {
	if !safeID.MatchString(packID) || version <= 0 {
		return "", fmt.Errorf("invalid pack id/version")
	}
	dir := "users"
	if ownerType == "org" { dir = "orgs" }
	return filepath.Join(r.DataDir, dir, strconv.FormatInt(ownerID, 10),
		"ops", "packs", packID, strconv.Itoa(version)), nil
}

func (r *Registry) Save(ownerType string, ownerID int64, m Manifest) error {
	if err := Validate(m); err != nil { return err }
	root, err := r.Root(ownerType, ownerID, m.ID, m.Version)
	if err != nil { return err }
	if err := os.MkdirAll(root, 0o755); err != nil { return err }
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil { return err }
	tmp := filepath.Join(root, "manifest.json.tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil { return err }
	return os.Rename(tmp, filepath.Join(root, "manifest.json"))
}

func (r *Registry) Load(ownerType string, ownerID int64, packID string, version int) (Manifest, error) {
	root, err := r.Root(ownerType, ownerID, packID, version)
	if err != nil { return Manifest{}, err }
	b, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil { return Manifest{}, err }
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil { return Manifest{}, err }
	return m, Validate(m)
}

func (r *Registry) List(ownerType string, ownerID int64) ([]Manifest, error) {
	base := filepath.Join(r.Root(ownerType, ownerID, "x", 1))
	base = filepath.Dir(filepath.Dir(base)) // .../ops/packs
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) { return nil, nil }
		return nil, err
	}
	var out []Manifest
	for _, e := range entries {
		if !e.IsDir() { continue }
		versions, _ := os.ReadDir(filepath.Join(base, e.Name()))
		for _, v := range versions {
			ver, err := strconv.Atoi(v.Name())
			if err != nil { continue }
			m, err := r.Load(ownerType, ownerID, e.Name(), ver)
			if err == nil { out = append(out, m) }
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (r *Registry) Publish(ownerType string, ownerID int64, packID string, version int) error {
	m, err := r.Load(ownerType, ownerID, packID, version)
	if err != nil { return err }
	m.Status = "published"
	return r.Save(ownerType, ownerID, m)
}
```

- [ ] **Step 3: 内置 Pack 与安装**

`wechat_mp/v1/manifest.json`：

```json
{"id":"wechat_mp","version":1,"status":"published","display_name":"微信公众号",
 "capabilities":["publish","metrics","engagements","reply"],
 "bindings":{"publish":{"transport":"mcp","provider":"wechat-mp"},
 "metrics":{"transport":"mcp","provider":"wechat-mp"},
 "engagements":{"transport":"mcp","provider":"wechat-mp"},
 "reply":{"transport":"mcp","provider":"wechat-mp"}}}
```

`xiaohongshu/v1/manifest.json`（Phase 0 manual；Phase 2 切 browser）：

```json
{"id":"xiaohongshu","version":1,"status":"published","display_name":"小红书",
 "capabilities":["publish","metrics","engagements","reply"],
 "bindings":{"publish":{"transport":"manual"},"metrics":{"transport":"manual"},
 "engagements":{"transport":"manual"},"reply":{"transport":"manual"}}}
```

```go
//go:embed builtin/*/v1/manifest.json
var builtinFS embed.FS

func InstallBuiltins(r *Registry, ownerType string, ownerID int64) error {
	for _, id := range []string{"wechat_mp", "xiaohongshu"} {
		p := "builtin/" + id + "/v1/manifest.json"
		b, err := builtinFS.ReadFile(p)
		if err != nil { return err }
		var m Manifest
		if err := json.Unmarshal(b, &m); err != nil { return err }
		if err := r.Save(ownerType, ownerID, m); err != nil { return err }
	}
	return nil
}
```

- [ ] **Step 4: 运行测试**

Run: `cd server && go test ./internal/opsbot/packs -v`  
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add server/internal/opsbot/packs
git commit -m "feat(opsbot): add filesystem pack registry and builtins"
```

---

## Task 4: Ops Bot CRUD 与多账号

**Files:**
- Create: `server/internal/opsbot/service.go`
- Test: `server/internal/opsbot/service_test.go`

**Interfaces:**
- Consumes: `*store.Queries`、`packs.Registry`。
- Produces: `Service`：`CreateAccountBot`、`CreateDirector`、`Get`、`ListByOwner`、`Pause`、`Update`。

- [ ] **Step 1: 写测试文件**

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
	owner := OwnerRef{Type: "user", ID: 1}
	_, err := svc.CreateAccountBot(context.Background(), CreateAccountInput{
		Owner: owner, PackID: "wechat_mp", AccountRef: "acc-a", DisplayName: "A",
	})
	require.NoError(t, err)
	_, err = svc.CreateAccountBot(context.Background(), CreateAccountInput{
		Owner: owner, PackID: "wechat_mp", AccountRef: "acc-b", DisplayName: "B",
	})
	require.NoError(t, err)
}
```

- [ ] **Step 2: 实现 service.go**

```go
type OwnerRef struct{ Type string; ID int64 }

type CreateAccountInput struct {
	Owner OwnerRef
	PackID string
	PackVersion int64
	AccountRef, DisplayName string
	PersonaJSON, CredentialRefsJSON string
	GoalsJSON, CadenceJSON, ApprovalPolicyJSON, BudgetJSON string
	DirectorBotID, CreatedBy int64
}

type Service struct{ q *store.Queries; packs *packs.Registry }

func NewService(q *store.Queries, r *packs.Registry) *Service { return &Service{q: q, packs: r} }

func (s *Service) CreateAccountBot(ctx context.Context, in CreateAccountInput) (store.OpsBot, error) {
	if in.PackID == "" || in.AccountRef == "" { return store.OpsBot{}, fmt.Errorf("pack_id and account_ref are required") }
	version := in.PackVersion
	if version <= 0 {
		list, err := s.packs.List(in.Owner.Type, in.Owner.ID)
		if err != nil { return store.OpsBot{}, err }
		for _, m := range list {
			if m.ID == in.PackID && m.Status == "published" && int64(m.Version) > version { version = int64(m.Version) }
		}
		if version == 0 { return store.OpsBot{}, fmt.Errorf("no published pack %s", in.PackID) }
	}
	if _, err := s.packs.Load(in.Owner.Type, in.Owner.ID, in.PackID, int(version)); err != nil {
		return store.OpsBot{}, err
	}
	return s.q.CreateOpsBot(ctx, store.CreateOpsBotParams{
		OwnerType: in.Owner.Type, OwnerID: in.Owner.ID, Role: "account",
		PackID: in.PackID, PackVersion: version, AccountRef: in.AccountRef,
		DisplayName: in.DisplayName, PersonaJson: dflt(in.PersonaJSON),
		CredentialRefsJson: dflt(in.CredentialRefsJSON), KnowledgeScopeJson: "{}",
		GoalsJson: dflt(in.GoalsJSON), CadenceJson: dflt(in.CadenceJSON),
		ApprovalPolicyJson: approvalDefault(in.ApprovalPolicyJSON),
		BudgetJson: dflt(in.BudgetJSON),
		DirectorBotID: sql.NullInt64{Int64: in.DirectorBotID, Valid: in.DirectorBotID > 0},
	})
}

func (s *Service) CreateDirector(ctx context.Context, owner OwnerRef, name, budget string, by int64) (store.OpsBot, error) {
	return s.q.CreateOpsBot(ctx, store.CreateOpsBotParams{
		OwnerType: owner.Type, OwnerID: owner.ID, Role: "director",
		PackID: "", PackVersion: 0, AccountRef: "", DisplayName: name,
		PersonaJson: "{}", CredentialRefsJson: "{}", KnowledgeScopeJson: "{}",
		GoalsJson: "{}", CadenceJson: "{}", ApprovalPolicyJson: "{}",
		BudgetJson: dflt(budget), DirectorBotID: sql.NullInt64{},
	})
}

func (s *Service) Get(ctx context.Context, id int64) (store.OpsBot, error) { return s.q.GetOpsBot(ctx, id) }
func (s *Service) ListByOwner(ctx context.Context, o OwnerRef) ([]store.OpsBot, error) {
	return s.q.ListOpsBotsByOwner(ctx, store.ListOpsBotsByOwnerParams{OwnerType: o.Type, OwnerID: o.ID})
}
func (s *Service) Pause(ctx context.Context, id int64) (store.OpsBot, error) {
	return s.q.UpdateOpsBotStatus(ctx, store.UpdateOpsBotStatusParams{Status: "paused", ID: id})
}

func dflt(v string) string { if v == "" { return "{}" }; return v }
func approvalDefault(v string) string {
	if v == "" { return `{"publish":true,"reply":true,"video":true}` }
	return v
}
```

- [ ] **Step 3: 运行测试**

Run: `cd server && go test ./internal/opsbot -run TestCreateTwoAccounts -v`  
Expected: PASS。

- [ ] **Step 4: 提交**

```bash
git add server/internal/opsbot
git commit -m "feat(opsbot): add account bot service with multi-account support"
```

---

## Task 5: 账号 Epic + 控制工作空间

**Files:**
- Create: `server/internal/opsbot/provision.go`
- Test: `server/internal/opsbot/provision_test.go`

**Interfaces:**
- Produces: `Provisioner.ProvisionAccount(ctx, owner, projectID, botID, title, createdBy) (issueID, workspaceID, error)`。

- [ ] **Step 1: 写测试**

```go
type fakeIssues struct{}
func (fakeIssues) CreateEpic(context.Context, int64, string, string, int64) (int64, error) { return 901, nil }
type fakeWS struct{}
func (fakeWS) CreateControl(context.Context, int64, string, int64, int64) (int64, error) { return 1601, nil }

func TestProvisionAccount(t *testing.T) {
	p := &Provisioner{Issues: fakeIssues{}, Workspaces: fakeWS{},
		FirstColumn: func(context.Context, int64) (int64, error) { return 1, nil }}
	i, w, err := p.ProvisionAccount(context.Background(), OwnerRef{Type: "user", ID: 1}, 42, 7, "公众号 A", 1)
	if err != nil || i != 901 || w != 1601 { t.Fatalf("got %d %d %v", i, w, err) }
}
```

- [ ] **Step 2: 实现 provision.go**

```go
type IssueCreator interface {
	CreateEpic(ctx context.Context, columnID int64, title, description string, byUserID int64) (int64, error)
}
type WorkspaceCreator interface {
	CreateControl(ctx context.Context, issueID int64, ownerType string, ownerID, createdBy int64) (int64, error)
}
type Provisioner struct {
	Issues IssueCreator
	Workspaces WorkspaceCreator
	FirstColumn func(context.Context, int64) (int64, error)
}
func (p *Provisioner) ProvisionAccount(ctx context.Context, owner OwnerRef, projectID, botID int64, title string, createdBy int64) (int64, int64, error) {
	col, err := p.FirstColumn(ctx, projectID)
	if err != nil { return 0, 0, err }
	issueID, err := p.Issues.CreateEpic(ctx, col, title, "运营 Bot 控制 Epic", createdBy)
	if err != nil { return 0, 0, err }
	wsID, err := p.Workspaces.CreateControl(ctx, issueID, owner.Type, owner.ID, createdBy)
	if err != nil { return 0, 0, err }
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

## Task 6: 审批复用与预算查询

**Files:**
- Create: `server/internal/opsbot/approval.go`
- Create: `server/internal/opsbot/budget.go`
- Test: `server/internal/opsbot/approval_test.go`
- Test: `server/internal/opsbot/budget_test.go`

**Interfaces:**
- Produces: `Approval.Create/List/Decide`（底层 `agent_ask_user_requests`，`kind='ops_approval'`）；`Budget.Check(ctx, botID, cost)`（底层 `SumOpsAccountCost`）。

- [ ] **Step 1: 写测试**

```go
func TestBudgetRejectsOverLimit(t *testing.T) {
	b := &Budget{Spent: func(context.Context, int64, string) (float64, float64, error) { return 990, 1000, nil }}
	if err := b.Check(context.Background(), 1, 20); err == nil { t.Fatal("expected over-limit") }
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

- [ ] **Step 2: 实现**

Approval 不新建表，包装 `store.InsertAskUserRequest` / `MarkAskUserRequestAnswered`：
- `questions_json` = `{"type":"ops_approval","options":["通过","驳回","修改"],"preview":...}`
- `kind` = `ops_approval`
- `metadata_json` = `{"bot_id":...,"issue_id":...,"type":"publish","content_version":1}`
- `expires_at` = now + policy timeout（默认 24h）
- `Decide` 只允许 pending → answered；`answers_json` 存 decision/reason。

Phase 0 先用内存实现满足测试，生产环境替换为 ask-user store：

```go
type ApprovalRequest struct{ ID int64; Status string }
type Approval struct {
	nextID int64
	items  map[int64]*ApprovalRequest
}
func (a *Approval) New(botID, issueID int64, typ, payload, preview string) (ApprovalRequest, error) {
	a.nextID++
	if a.items == nil { a.items = map[int64]*ApprovalRequest{} }
	req := &ApprovalRequest{ID: a.nextID, Status: "pending"}
	a.items[req.ID] = req
	return *req, nil
}
func (a *Approval) Decide(ctx context.Context, id, operator int64, status, reason string) (ApprovalRequest, error) {
	req, ok := a.items[id]
	if !ok { return ApprovalRequest{}, fmt.Errorf("approval %d not found", id) }
	if req.Status != "pending" { return ApprovalRequest{}, fmt.Errorf("approval %d is already %s", id, req.Status) }
	if status != "approved" && status != "rejected" { return ApprovalRequest{}, fmt.Errorf("invalid decision %q", status) }
	req.Status = status
	return *req, nil
}
```

Budget：
```go
type Budget struct {
	Spent func(ctx context.Context, epicIssueID int64, period string) (spent, limit float64, err error)
}
func (b *Budget) Check(ctx context.Context, epicIssueID int64, cost float64) error {
	spent, limit, err := b.Spent(ctx, epicIssueID, time.Now().Format("2006-01"))
	if err != nil { return err }
	if limit > 0 && spent+cost > limit { return fmt.Errorf("budget exceeded: %.2f + %.2f > %.2f", spent, cost, limit) }
	return nil
}
```

`Spent` 用 `SumOpsAccountCost(epicIssueID, monthStart, nextMonthStart)`，limit 从 `ops_bots.budget_json.monthly_limit` 读取。

- [ ] **Step 3: 运行测试**

Run: `cd server && go test ./internal/opsbot -run 'TestBudget|TestApproval' -v`  
Expected: PASS。

- [ ] **Step 4: 提交**

```bash
git add server/internal/opsbot/approval.go server/internal/opsbot/budget.go \
  server/internal/opsbot/approval_test.go server/internal/opsbot/budget_test.go
git commit -m "feat(opsbot): reuse ask-user approvals and add budget checks"
```

---

## Task 7: 对话式接入

**Files:**
- Create: `server/internal/opsbot/onboarding.go`
- Test: `server/internal/opsbot/onboarding_test.go`
- Modify: `server/internal/service/imbot_inbound.go`

**Interfaces:**
- Produces: `Onboarding.Handle(ctx, chatID, owner, projectID, text) (reply string, done bool, err error)`。

- [ ] **Step 1: 写测试**

```go
func TestOnboardingCreatesAccountBot(t *testing.T) {
	o := newTestOnboarding(t)
	owner := OwnerRef{Type: "user", ID: 1}
	reply, done, err := o.Handle(context.Background(), 10, owner, 42, "接入公众号")
	if err != nil || done || reply == "" { t.Fatalf("first: %v %v %q", err, done, reply) }
	for _, ans := range []string{"品牌 A", "涨粉", "每周 2 篇", "需要"} {
		reply, done, err = o.Handle(context.Background(), 10, owner, 42, ans)
		if err != nil { t.Fatal(err) }
	}
	if !done { t.Fatal("expected done") }
}
```

- [ ] **Step 2: 实现状态机**

```go
type BotCreator interface {
	CreateAccountBot(context.Context, CreateAccountInput) (store.OpsBot, error)
}
type AccountProvisioner interface {
	ProvisionAccount(context.Context, OwnerRef, int64, int64, string, int64) (int64, int64, error)
}
type OnboardingSession struct {
	ChatID    int64
	Owner     OwnerRef
	ProjectID int64
	PackID    string
	Account   string
	Goal      string
	Cadence   string
	Step      string
}
type Onboarding struct {
	Service     BotCreator
	Provisioner AccountProvisioner
	Sessions    map[int64]*OnboardingSession
}

type fakeBotCreator struct{ created int }
func (f *fakeBotCreator) CreateAccountBot(context.Context, CreateAccountInput) (store.OpsBot, error) {
	f.created++
	return store.OpsBot{ID: int64(f.created)}, nil
}
type fakeProvisioner struct{}
func (fakeProvisioner) ProvisionAccount(context.Context, OwnerRef, int64, int64, string, int64) (int64, int64, error) {
	return 1, 2, nil
}
func newTestOnboarding(t *testing.T) *Onboarding {
	return &Onboarding{Service: &fakeBotCreator{}, Provisioner: fakeProvisioner{},
		Sessions: map[int64]*OnboardingSession{}}
}

func (o *Onboarding) Handle(ctx context.Context, chatID int64, owner OwnerRef, projectID int64, text string) (string, bool, error) {
	s := o.Sessions[chatID]
	if s == nil {
		pack := ""
		switch text {
		case "接入公众号": pack = "wechat_mp"
		case "接入小红书": pack = "xiaohongshu"
		}
		if pack == "" { return "", false, nil }
		o.Sessions[chatID] = &OnboardingSession{ChatID: chatID, Owner: owner, ProjectID: projectID, PackID: pack, Step: "account"}
		return "账号名称是？", false, nil
	}
	if text == "/cancel" { delete(o.Sessions, chatID); return "已取消接入。", true, nil }
	switch s.Step {
	case "account": s.Account = text; s.Step = "goal"; return "主要目标是涨粉、转化还是品牌？", false, nil
	case "goal": s.Goal = text; s.Step = "cadence"; return "每周发几篇？要热点加更吗？", false, nil
	case "cadence": s.Cadence = text; s.Step = "approval"; return "发布前需要你确认吗？", false, nil
	case "approval":
		bot, err := o.Service.CreateAccountBot(ctx, CreateAccountInput{
			Owner: s.Owner, PackID: s.PackID, AccountRef: s.Account,
			DisplayName: s.Account, CreatedBy: s.Owner.ID,
		})
		if err != nil { return "", false, err }
		if _, _, err := o.Provisioner.ProvisionAccount(ctx, s.Owner, s.ProjectID, bot.ID, s.Account, s.Owner.ID); err != nil {
			return "", false, err
		}
		delete(o.Sessions, chatID)
		return "已创建「" + s.Account + "」运营 Bot。", true, nil
	}
	return "", false, nil
}
```

- 每步一问；`/cancel` 取消。
- `done` 时调用 `Service.CreateAccountBot` + `Provisioner.ProvisionAccount`。
- 凭据步骤返回一次性链接提示，不收密钥。

- [ ] **Step 3: 接入 imbot**

在 `HandleInbound` 的路由前：

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
git commit -m "feat(opsbot): add conversation onboarding"
```

---

## Task 8: HTTP API 与 MCP 工具

**Files:**
- Create: `server/internal/api/opsbot.go`
- Modify: `server/internal/server/server.go`
- Modify: `server/internal/server/router.go`
- Create: `server/cmd/niuniu-mcp/opsbot_tools.go`
- Modify: `server/cmd/niuniu-mcp/main.go`
- Test: `server/internal/api/opsbot_test.go`

**Interfaces:**
- HTTP：`GET/POST /api/ops/bots`、`PATCH /api/ops/bots/:id`、`GET /api/ops/packs`、`GET /api/ops/approvals`、`POST /api/ops/approvals/:id/approve|reject`。
- MCP：`opsbot_create`、`opsbot_list`、`opsbot_pause`、`ops_pack_list`、`ops_approval_list`、`ops_approval_approve`、`ops_approval_reject`。

- [ ] **Step 1: 写 handler 测试**

复用 `intake_completion_contract_test.go` 的 router/DB harness，覆盖：合法 owner 创建 200；跨 owner 403；同 Pack 第二个账号 200；重复 `account_ref` 400/409。

- [ ] **Step 2: 实现 handler + 路由 + 服务接线**

`OpsBotHandler` 持有 `*opsbot.Service`、`*opsbot.Provisioner`、`*service.Authz`；写操作先 `EnsureOwnerWritable`，再创建 Bot + 账号 Epic/workspace。

- [ ] **Step 3: 实现 MCP 工具**

在 `main.go` 的 `registerIntakeTools` 旁加 `registerOpsBotTools(s, api, pid)`；工具参数按设计文档 §12。

- [ ] **Step 4: 运行测试**

Run: `cd server && go test ./internal/api ./cmd/niuniu-mcp -run 'OpsBot|Ops' -v`  
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add server/internal/api/opsbot.go server/internal/server/server.go server/internal/server/router.go \
  server/cmd/niuniu-mcp/opsbot_tools.go server/cmd/niuniu-mcp/main.go
git commit -m "feat(opsbot): add http api and mcp tools"
```

---

## Task 9: Phase 0 E2E 与文档

**Files:**
- Create: `server/internal/opsbot/phase0_e2e_test.go`
- Create: `docs/operations/ops-bot-phase0.md`

- [ ] **Step 1: E2E 测试**

覆盖：对话创建公众号 Bot → 账号 Epic/workspace 存在；同 Pack 第二账号；重复 account_ref 失败；跨 owner 403；预算超限；审批 pending → approved，二次决定失败。

- [ ] **Step 2: 运行验收**

```bash
cd server
go test ./internal/store ./internal/opsbot ./internal/service ./internal/api -run 'OpsBot|Onboarding|Provision|Budget|Approval' -v
cd ..
make schema-diff
make sqlc
```

- [ ] **Step 3: 写 Phase 0 runbook**

`docs/operations/ops-bot-phase0.md`：能力边界、对话接入、多账号、内置 Pack、预算/审批默认值、回滚。

- [ ] **Step 4: 提交**

```bash
git add server/internal/opsbot/phase0_e2e_test.go docs/operations/ops-bot-phase0.md
git commit -m "test(opsbot): add phase 0 end-to-end acceptance"
```

---

# Phase 1：微信公众号内容闭环

## Task 10: WeChat ChannelAdapter（官方 API / MCP）

**Files:**
- Create: `server/internal/opsbot/channel/wechat.go`
- Test: `server/internal/opsbot/channel/wechat_test.go`

**Interfaces:**
- Implements `ChannelAdapter`：`EnsureSession`、`Publish`（草稿/发布/定时）、`FetchMetrics`（用户分析/图文统计）、`ListEngagements`、`Reply`、`HealthCheck`。
- Consumes: 现有 `wechat-mp` MCP 客户端 / integration provider；凭据从 `credential_refs_json` 解析。

- [ ] **Step 1: 写契约测试**

用 fake MCP transport 测：发布 payload 映射、回执解析、指标归一化、MCP 错误映射。

- [ ] **Step 2: 实现 adapter**

- `Publish`：先建草稿 → 上传素材 → 发布/定时 → 返回 `Receipt{ExternalID,URL,Raw}`。
- `FetchMetrics`：阅读/分享/在看/涨掉粉/菜单点击，归一化键名。
- `ListEngagements` / `Reply`：若 MCP 不支持，返回 `ErrCapabilityUnavailable`，由 Pack capability 决定是否调用。

- [ ] **Step 3: 运行测试并提交**

```bash
cd server && go test ./internal/opsbot/channel -run WeChat -v
git add server/internal/opsbot/channel/wechat.go server/internal/opsbot/channel/wechat_test.go
git commit -m "feat(opsbot): add wechat channel adapter"
```

---

## Task 11: 公众号内容生产流水线

**Files:**
- Create: `server/internal/opsbot/content/pipeline.go`
- Create: `server/internal/opsbot/content/wechat_prompt.go`
- Test: `server/internal/opsbot/content/pipeline_test.go`

**Interfaces:**
- `Pipeline.Run(ctx, bot, brief) (ContentArtifact, error)`。
- Stages：topic → copy → cover/image → layout draft；产物写 workspace，登记 `.niuniu/artifacts.json`。

- [ ] **Step 1: 测试 brief → 文案 → 图片 → 草稿标识**
- [ ] **Step 2: 实现 stage 编排，复用 `wechat-mp` / `media-studio` 场景与现有写作能力**
- [ ] **Step 3: 失败处理：缺素材/缺凭据/平台错误 → 停在当前 stage，生成具体问题**
- [ ] **Step 4: 运行测试并提交**

---

## Task 12: 合规门禁与审批卡片

**Files:**
- Modify: `server/internal/opsbot/content/pipeline.go`
- Create: `server/internal/opsbot/content/review.go`
- Test: `server/internal/opsbot/content/review_test.go`

- [ ] **Step 1: 复用 harness `ai_judge` 审校：事实溯源、广告法、版权、平台红线**
- [ ] **Step 2: 审校失败 → 阻断，不进入审批**
- [ ] **Step 3: 审校通过 → 创建 `agent_ask_user_requests`（kind=ops_approval，metadata 含 content_version/preview）**
- [ ] **Step 4: IM 卡片：通过/驳回/修改**
- [ ] **Step 5: 测试并提交**

---

## Task 13: 发布、回执与指标

**Files:**
- Create: `server/internal/opsbot/publish.go`
- Create: `server/internal/opsbot/metrics.go`
- Test: `server/internal/opsbot/publish_test.go`

- [ ] **Step 1: 发布幂等：`ops_publish_records` + idempotency_key**
- [ ] **Step 2: 审批通过 → adapter.Publish → 回执/URL/截图 → 写记录**
- [ ] **Step 3: 失败退避重试；仍失败 → 保留上下文 + 具体问题**
- [ ] **Step 4: `FetchMetrics` → `ops_metrics_daily`；成本 → `issue_exec_events(kind='cost')`**
- [ ] **Step 5: 测试并提交**

---

## Task 14: 日历、调度与总监

**Files:**
- Create: `server/internal/opsbot/calendar.go`
- Create: `server/internal/opsbot/director.go`
- Test: `server/internal/opsbot/calendar_test.go`

- [ ] **Step 1: 内容日历用 `issues`：计划日期 + “计划”列；不建新表**
- [ ] **Step 2: 固定节奏：scheduler 触发总监，按账号目标生成内容 issues**
- [ ] **Step 3: 总监分配选题/预算，跨账号去重**
- [ ] **Step 4: 测试并提交**

---

## Task 15: 复盘与爆款回流

**Files:**
- Create: `server/internal/opsbot/feedback.go`
- Test: `server/internal/opsbot/feedback_test.go`

- [ ] **Step 1: 汇总 `ops_metrics_daily` → 账号目标达成度**
- [ ] **Step 2: 爆款拆解写回知识库/记忆；下一轮选题引用**
- [ ] **Step 3: 总监复盘报告 + Mission control 指标**
- [ ] **Step 4: 测试并提交**

---

## Task 16: 公众号端到端验收

- [ ] 对话创建公众号 Bot → 授权 → 选题 → 文案/图片 → 审校 → 审批 → 发布 → 指标。
- [ ] 审批驳回/修改后重新生成。
- [ ] 预算超限暂停。
- [ ] 同 Pack 第二账号独立运行。
- [ ] 运行 `go test ./internal/opsbot/... ./internal/service/...` 并提交。

---

# Phase 2：小红书浏览器闭环

## Task 17: 本地 Browser Worker

**Files:**
- Create: `server/internal/opsbot/browser/worker.go`
- Create: `server/internal/opsbot/browser/session.go`
- Test: `server/internal/opsbot/browser/worker_test.go`

- [ ] **Step 1: 每账号独立 profile/cookie；账号级租约**
- [ ] **Step 2: 任务队列、频率限制、随机化节奏**
- [ ] **Step 3: 登录失效 → 生成“重新登录”任务；UI 改版 → 降级人工发布包**
- [ ] **Step 4: 截图/回执证据；`ops_bots.runtime_json` 记录健康/风险**
- [ ] **Step 5: 测试并提交**

---

## Task 18: XHS ChannelAdapter

**Files:**
- Create: `server/internal/opsbot/channel/xhs.go`
- Test: `server/internal/opsbot/channel/xhs_test.go`

- [ ] **Step 1: 扫码登录 / 会话保活**
- [ ] **Step 2: 发布图文/视频笔记；读取创作者后台指标**
- [ ] **Step 3: 读取评论/私信；只生成建议回复，不自动发送**
- [ ] **Step 4: 能力缺失返回 `ErrCapabilityUnavailable`**
- [ ] **Step 5: 测试并提交**

---

## Task 19: 互动建议与审批

**Files:**
- Create: `server/internal/opsbot/engagement.go`
- Test: `server/internal/opsbot/engagement_test.go`

- [ ] **Step 1: 评论/私信入 `intake_events`（source_kind=xhs）**
- [ ] **Step 2: 分类 + 建议回复写 `intake_decisions.would_do_json`**
- [ ] **Step 3: 审批卡片 `agent_ask_user_requests`；通过后 adapter.Reply**
- [ ] **Step 4: 高风险升级总监；发送频率限制**
- [ ] **Step 5: 测试并提交**

---

## Task 20: 风控与降级

- [ ] 账号级 kill switch；登录失效/限流/封禁提示 → 暂停 + 通知。
- [ ] 不做批量关注/点赞/私信。
- [ ] 页面改版 → recipe 失效 → 人工发布包。
- [ ] 测试并提交。

---

## Task 21: 小红书端到端验收

- [ ] 对话创建 XHS Bot → 扫码 → 发布图文 → 读指标 → 评论建议 → 审批发送。
- [ ] 登录失效降级；预算超限；同渠道第二账号隔离。
- [ ] 运行全量测试并提交。

---

# Phase 3：视频、团队云浏览器、多账号

## Task 22: 视频流水线

**Files:**
- Create: `server/internal/opsbot/content/video.go`
- Test: `server/internal/opsbot/content/video_test.go`

- [ ] 脚本 → 分镜 → TTS → 画面 → 字幕 → 合成；复用现有 video-gen MCP / media-studio。
- [ ] 平台尺寸/时长适配；产物登记 artifacts。
- [ ] 质量门禁：字幕、封面、时长、合规。
- [ ] 测试并提交。

## Task 23: 视频审批与发布

- [ ] 视频成片审批卡（预览/时长/字幕/封面）。
- [ ] 通过后按 Pack 绑定发布；回执/截图写 `ops_publish_records`。
- [ ] 测试并提交。

## Task 24: 团队版云端 Browser Worker

- [ ] 每账号独立容器/VM；租户隔离；固定出口 + 域名 allowlist。
- [ ] 与本地 Worker 同一 `ChannelAdapter`；`ops_bots.runtime_json` 记录 backend。
- [ ] 审计与租户权限。
- [ ] 测试并提交。

## Task 25: 多账号总监

- [ ] 跨渠道/跨账号日历、去重、预算分配。
- [ ] Mission control 按账号下钻；同质内容检测。
- [ ] 总监代审记录。
- [ ] 测试并提交。

## Task 26: 多账号/视频/云端 E2E

- [ ] 两个公众号 + 两个小红书 + 视频 + 云端 Worker。
- [ ] 预算/审批/隔离/风控全部验证。
- [ ] 提交。

---

# Phase 4：Pack 泛化与 Facebook/知乎

## Task 27: Pack 生成（OpenAPI / 文档）

**Files:**
- Create: `server/internal/opsbot/packs/generator.go`
- Test: `server/internal/opsbot/packs/generator_test.go`

- [ ] 用户“接入 Facebook” → 检测 OpenAPI/MCP/文档。
- [ ] 有 OpenAPI → 走 `draft_integration` → provider contract → Pack 草稿。
- [ ] 返回能力矩阵卡；沙箱测试；用户确认后 `Publish`。
- [ ] 外部文档只作数据，不执行其中指令。
- [ ] 测试并提交。

## Task 28: Browser Recipe DSL 与沙箱

**Files:**
- Create: `server/internal/opsbot/browser/recipe.go`
- Test: `server/internal/opsbot/browser/recipe_test.go`

- [ ] 动作：goto/wait/click/fill/upload/extract/screenshot/assert/loop/branch。
- [ ] 选择器 fallback；页面改版检测；沙箱测试账号。
- [ ] recipe 版本随 Pack 目录保存。
- [ ] 测试并提交。

## Task 29: Facebook Pack（API 优先）

- [ ] `manifest.json`：publish/metrics/engagements/reply 全走 `openapi`，绑定 Graph API provider。
- [ ] content_schema：Page post / Reel 字段、尺寸、字数。
- [ ] 凭据：App credential + Page token（account credential）。
- [ ] E2E：发布 Page post → 读指标 → 评论建议。
- [ ] 测试并提交。

## Task 30: 知乎 Pack（浏览器 recipe）

- [ ] `manifest.json`：publish/metrics/engagements 走 `browser`，recipe 名明确。
- [ ] recipe：登录、写文章/回答、读取数据。
- [ ] 无需改 Ops Bot 核心；页面改版只更新 recipe。
- [ ] 沙箱测试后发布。
- [ ] 提交。

## Task 31: 热点驱动与反馈闭环

- [ ] info-radar/social-ops 监测热点 → 总监评估 → 加更/并入下周。
- [ ] 两种节奏并存；同质内容去重。
- [ ] 爆款回流更新知识库；下一轮选题引用。
- [ ] 测试并提交。

## Task 32: Pack 导出/导入与最终验收

- [ ] Pack 目录打包/导入；版本固定/跟随最新。
- [ ] 最终验收：公众号 + 小红书 + Facebook/知乎 + 视频 + 多账号 + 热点。
- [ ] 文档更新；全量测试；提交。

---

## Self-Review

**Spec coverage:**

- 最小数据模型：Task 1；Pack 文件系统：Task 3；ChannelAdapter：Task 2/10/18/29/30。
- 多账号与账号级隔离：Task 4/5/25。
- 预算/审批：Task 6/12/23。
- 对话式接入：Task 7。
- HTTP/MCP：Task 8。
- 公众号闭环：Task 10–16。
- 小红书闭环：Task 17–21。
- 视频/云端/多账号：Task 22–26。
- Pack 泛化/Facebook/知乎：Task 27–32。
- 测试/验收：每个 Phase 末尾。

**Placeholder scan:** 计划中没有 `TBD` / `TODO`；Phase 0 明确只做骨架，内容生产/发布/互动在 Phase 1–4。

**Type consistency:** `Manifest`、`Binding`、`Registry`、`Service`、`Provisioner`、`Budget`、`Approval`、`Onboarding`、`ChannelAdapter` 的命名在各阶段保持一致；`store.Queries` 方法名与 Task 1 查询一致。
