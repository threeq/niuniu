# Provider Group spawn 重解析覆盖 + 每空间状态跟进 + chat 状态栏 provider pill — 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 绑定 provider/组的工作空间，每次 agent 进程启动时最新解析的 provider env 覆盖 workspace_env 同名键；每空间持久记录当前实际使用的 provider 名称并经 WS 实时推送；chat 状态栏显示 provider pill。

**Architecture:** 覆盖语义收敛在 `sceneenv.Resolve` 一处重排（所有 spawn 路径自动受益）；`workspaces.active_env_provider_name` 新列承载每空间状态（双 schema + addColumnIfNotExists）；两个 spawn 写入点（PTY `agent.Start` / chat `ensureProcess`）持久化并经 notify hub 广播 `provider_changed`；前端在 `chat-input.tsx` 状态栏渲染 pill 并订阅 notify 失效 workspace query。

**Tech Stack:** Go 1.25（sqlc + SQLite/PG 双驱动）、React 19 + TanStack Query + Zustand、notify WebSocket。

**Spec:** `docs/superpowers/specs/2026-09-21-provider-group-reresolve-design.md`

## Global Constraints

- 所有 git 操作：`git -C <repo-path> …`；只提交到本地 `ws-933/main`，**禁止 push、禁止合入 main**。
- 所有 `go`/`make` 命令在 `server/` 目录下执行（`make` 亦可在仓库根）。构建前端前 `go build ./...` 会因 `//go:embed dist` 失败——Go 侧验证用 `go vet` + `go test`，不要裸 `go build ./...`。
- 双 schema 红线：每个 DDL 同时进 `schema.sql` 与 `schema_postgres.sql`，改后必须 `make schema-diff`（期望 `OK: no table-level drift detected`）。
- 迁移加列**不在 schema 文件里建索引**（本计划新列无索引需求）。
- `store/queries/*.sql` 的 `--` 注释必须纯 ASCII（`make sqlc-lint` 守护）。
- 前端设计系统红线：无 hex 色值、无 Tailwind 任意值（`bg-[#…]`/`p-[7px]`）、用户可见字符串必须 `t('…')`、lucide 图标、`docs/design-system.md` 为准。
- 提交信息不含任何 Claude 协作签名（用户全局规则）。
- 现有行为红线：未绑定 provider 的工作空间，`Resolve` 输出必须与改动前逐字节一致（零回归安全阀）。

---

### Task 1: sceneenv — 绑定 provider 展开覆盖 workspace_env

**Files:**
- Modify: `server/internal/sceneenv/sceneenv.go`（`Resolve`，约 L135-205）
- Test: `server/internal/sceneenv/sceneenv_test.go`（重写 L260 `TestResolve_BoundProviderOverriddenByExplicitEnv`，新增两测试）

**Interfaces:**
- Consumes: 现有 `ActiveProvider` / `ExpandProvider`（provider_fallback.go / provider.go，不改签名）。
- Produces: `Resolve` 新语义——绑定 provider 展开的键在 workspace_env 之后最终铺一遍（最高优先级）；未绑定时输出不变。后续任务不依赖其内部实现。

- [ ] **Step 1: 重写旧优先级测试 + 新增覆盖测试（先失败）**

将 `sceneenv_test.go` 中 `TestResolve_BoundProviderOverriddenByExplicitEnv`（L260-276）整体替换为：

```go
func TestResolve_BoundProviderOverridesWorkspaceEnv(t *testing.T) {
	// spec 2026-09-21: the freshest provider resolution must win over
	// workspace_env for the keys the provider emits (a stale
	// ANTHROPIC_BASE_URL in workspace_env must not shadow the group's
	// current best member). Keys the provider does not emit keep their
	// workspace_env values.
	prov := store.EnvProvider{ID: 5, Name: "DeepSeek",
		BaseUrls: `{"anthropic":"https://api.deepseek.com/anthropic"}`, ApiKey: "${ACCOUNT:DeepSeek}", Model: "deepseek-v4", Enabled: 1}
	q := fakeQuerier{
		env: []store.WorkspaceEnv{
			{WorkspaceID: 7, Key: "ANTHROPIC_BASE_URL", Value: "https://explicit.override"},
			{WorkspaceID: 7, Key: "GIT_AUTHOR_NAME", Value: "keep-me"},
		},
		boundProvider: &prov,
		accounts:      []store.EnvAccount{{Name: "DeepSeek", ApiKey: "sk-real"}},
		cliType:       "claude",
	}
	rows, err := Resolve(context.Background(), q, 7)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	got := envMap(rows)
	if got["ANTHROPIC_BASE_URL"] != "https://api.deepseek.com/anthropic" {
		t.Errorf("provider resolution must override workspace_env: %v", got["ANTHROPIC_BASE_URL"])
	}
	if got["ANTHROPIC_AUTH_TOKEN"] != "sk-real" {
		t.Errorf("provider account key must survive the final overlay: %q", got["ANTHROPIC_AUTH_TOKEN"])
	}
	if got["GIT_AUTHOR_NAME"] != "keep-me" {
		t.Errorf("workspace_env keys the provider does not emit must be kept: %q", got["GIT_AUTHOR_NAME"])
	}
}

func TestResolve_NoBoundProviderUnchanged(t *testing.T) {
	// Zero-regression guard: with no provider bound the output must equal the
	// workspace_env rows exactly (byte-for-byte semantics of the old merge).
	q := fakeQuerier{
		env: []store.WorkspaceEnv{
			{WorkspaceID: 7, Key: "ANTHROPIC_BASE_URL", Value: "https://user-set"},
			{WorkspaceID: 7, Key: "NIUNIU_PERMISSION_MODE", Value: "autohost"},
		},
		cliType: "claude",
	}
	rows, err := Resolve(context.Background(), q, 7)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	got := envMap(rows)
	if len(got) != 2 || got["ANTHROPIC_BASE_URL"] != "https://user-set" || got["NIUNIU_PERMISSION_MODE"] != "autohost" {
		t.Errorf("unbound workspace output changed: %v", got)
	}
}

func TestResolve_GroupBindingOverridesWorkspaceEnv(t *testing.T) {
	// A GROUP binding resolves through GroupProvider; the resolved member's
	// keys likewise override workspace_env.
	first := store.EnvProvider{ID: 1, Name: "智谱-1", GroupName: "智谱",
		BaseUrls: `{"anthropic":"https://open.bigmodel.cn/api/anthropic"}`, Model: "glm-5.1", Enabled: 1, GroupPosition: 0}
	second := store.EnvProvider{ID: 2, Name: "智谱-2", GroupName: "智谱",
		BaseUrls: `{"anthropic":"https://open.bigmodel.cn/api/anthropic"}`, Model: "glm-5.1", Enabled: 1, GroupPosition: 1}
	q := fakeQuerier{
		env:          []store.WorkspaceEnv{{WorkspaceID: 7, Key: "ANTHROPIC_BASE_URL", Value: "https://stale.example"}},
		groupBinding: "智谱",
		providers:    []store.EnvProvider{first, second},
		cliType:      "claude",
	}
	rows, err := Resolve(context.Background(), q, 7)
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if envMap(rows)["ANTHROPIC_BASE_URL"] != "https://open.bigmodel.cn/api/anthropic" {
		t.Errorf("group's first usable member must override workspace_env: %v", envMap(rows)["ANTHROPIC_BASE_URL"])
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `cd server && go test ./internal/sceneenv/ -run 'TestResolve_(BoundProviderOverridesWorkspaceEnv|NoBoundProviderUnchanged|GroupBindingOverridesWorkspaceEnv)' -count=1`
Expected: `TestResolve_BoundProviderOverridesWorkspaceEnv` FAIL（现状 explicit.override 胜出）；`GroupBindingOverrides…` FAIL；`NoBoundProviderUnchanged` PASS。

- [ ] **Step 3: 修改 `Resolve` 实现覆盖语义**

在 `sceneenv.go` 的 `Resolve` 中（L148-159 现状）把绑定 provider 块改为：

```go
	scene := SceneVars(ctx, q, wsID)
	merged := map[string]string{}

	// Bound provider (group-resolved when applicable). Its expanded keys are
	// re-applied AFTER workspace_env below, so the freshest resolution wins
	// over workspace-configured env for the keys the provider emits — a stale
	// ANTHROPIC_BASE_URL in workspace_env must not shadow the group's current
	// best member. Keys the provider does not emit keep their workspace_env
	// values. With no provider bound (boundEnv nil) the merge below is
	// byte-for-byte the historical behavior.
	var boundEnv map[string]string
	if p, ok := ActiveProvider(ctx, q, wsID); ok {
		boundEnv = ExpandProvider(p, cliType, accounts, true)
		for k, v := range boundEnv {
			merged[k] = v
		}
	}
```

然后在 workspace_env 合并循环（`// Highest: explicit workspace_env.` 之后、`keys := …` 之前）追加最终覆盖：

```go
	// Highest: the bound provider's own keys, re-applied over workspace_env
	// (see boundEnv above). ${ACCOUNT:<name>} references are still preserved
	// here and substituted by SubstituteAccounts at return.
	for k, v := range boundEnv {
		merged[k] = v
	}
```

- [ ] **Step 4: 全包测试通过**

Run: `cd server && go test ./internal/sceneenv/ -race -count=1 && go vet ./internal/sceneenv/`
Expected: 全部 PASS（含未动的 `TestResolve_ExplicitWorkspaceEnvWinsOverScene`——那是 workspace_env 对 scene 层的断言，不受影响）。

- [ ] **Step 5: Commit**

```bash
git -C <repo> add server/internal/sceneenv/sceneenv.go server/internal/sceneenv/sceneenv_test.go
git -C <repo> commit -m "feat(sceneenv): 绑定 provider 展开覆盖 workspace_env 同名键（spawn 最新解析优先）"
```

---

### Task 2: schema 新列 + sqlc 查询 + 迁移

**Files:**
- Modify: `server/internal/store/schema.sql`（workspaces 表尾，env_provider_group 行后）
- Modify: `server/internal/store/schema_postgres.sql`（同位置）
- Modify: `server/internal/store/queries/workspaces.sql`（`SetWorkspaceEnvProviderGroup` 后追加）
- Modify: `server/internal/store/migrate.go`（env_providers 分组列迁移块后）

**Interfaces:**
- Consumes: 无。
- Produces: `store.Workspace.ActiveEnvProviderName string`（sqlc 重生成后 `GetWorkspace` 自动携带）；`q.SetWorkspaceActiveEnvProvider(ctx, store.SetWorkspaceActiveEnvProviderParams{ActiveEnvProviderName string, ID int64}) error`。Task 3 依赖这两者。

- [ ] **Step 1: 双 schema 加列**

`schema.sql` workspaces 表（现 env_provider_group 为最后一个列、无尾逗号）改为：

```sql
    env_provider_group TEXT NOT NULL DEFAULT '', -- bind to a provider GROUP instead of one provider
    -- provider NAME the workspace's agent process actually spawned with,
    -- recorded at each spawn from sceneenv.ActiveProvider ('' = none). NOT
    -- cleared on unbind: it describes the running process until the next
    -- spawn. See docs/superpowers/specs/2026-09-21-provider-group-reresolve-design.md.
    active_env_provider_name TEXT NOT NULL DEFAULT ''
);
```

`schema_postgres.sql` 两处 workspaces 定义（L264-265 等）做同样修改。

- [ ] **Step 2: sqlc 查询（ASCII 注释）**

`queries/workspaces.sql` 在 `SetWorkspaceEnvProviderGroup` 查询之后追加：

```sql
-- name: SetWorkspaceActiveEnvProvider :exec
-- Record the provider NAME this workspace's agent process actually spawned
-- with (sceneenv.ActiveProvider result at spawn time). '' = no provider.
UPDATE workspaces SET active_env_provider_name = ? WHERE id = ?;
```

- [ ] **Step 3: 迁移加列**

`migrate.go` 中 `addColumnIfNotExists(db, "env_providers", "cooldown_until", …)` 之后追加：

```go
	// Per-workspace record of the provider NAME its agent process actually
	// spawned with (spec 2026-09-21 provider group re-resolve). '' = none.
	addColumnIfNotExists(db, "workspaces", "active_env_provider_name", "TEXT NOT NULL DEFAULT ''")
```

- [ ] **Step 4: 重生成 sqlc + 校验**

Run: `cd server && make sqlc`（或仓库根 `make sqlc`），然后 `make schema-diff`、`make sqlc-lint`。
Expected: sqlc 生成 `SetWorkspaceActiveEnvProvider`；`store.Workspace` 出现 `ActiveEnvProviderName` 字段；schema-diff 输出 `OK: no table-level drift detected`；sqlc-lint 通过。

- [ ] **Step 5: Go 侧验证 + Commit**

Run: `cd server && go vet ./internal/store/... ./internal/sceneenv/... && go test ./internal/sceneenv/ -count=1`
Expected: 通过。

```bash
git -C <repo> add server/internal/store/schema.sql server/internal/store/schema_postgres.sql server/internal/store/queries/workspaces.sql server/internal/store/migrate.go server/internal/store/
git -C <repo> commit -m "feat(store): workspaces.active_env_provider_name 列 + SetWorkspaceActiveEnvProvider 查询"
```

---

### Task 3: 两个 spawn 写入点 + provider_changed 推送

**Files:**
- Modify: `server/internal/agentproxy/proxy.go`（ensureProcess 内 L2241-2259 的 ActiveProvider 块收进新方法；新增 `recordActiveProvider` / `broadcastActiveProvider`）
- Modify: `server/internal/service/agent.go`（`Start`，L203-207 Resolve 之后）
- Test: `server/internal/agentproxy/provider_active_test.go`（新建）

**Interfaces:**
- Consumes: Task 2 的 `SetWorkspaceActiveEnvProvider` 与 `Workspace.ActiveEnvProviderName`；现有 `sceneenv.ActiveProvider` / `ProviderInCooldown` / `closeProviderRateLimitEvents`；`notify.NotificationHub.Broadcast`。
- Produces: WS 通知 `{topic:"workspace", action:"provider_changed", id:<workspaceID>, extra:{providerName:"…"}}`（Task 5 前端依赖此 wire shape）；`workspaces.active_env_provider_name` 落库语义（Task 4 DTO 直接映射）。

- [ ] **Step 1: 写失败测试（recordActiveProvider 两态 + 组内切换）**

新建 `server/internal/agentproxy/provider_active_test.go`：

```go
package agentproxy

import (
	"database/sql"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
	"github.com/niuniu-dev/niuniu/internal/sceneenv"
)

// TestRecordActiveProvider_PersistsBoundName verifies the spawn-time record:
// the workspace row carries the name of the provider ActiveProvider resolves.
func TestRecordActiveProvider_PersistsBoundName(t *testing.T) {
	ctx := newTestContext()
	s := newDispatchTestSession(t)
	bound, err := s.q.CreateEnvProvider(ctx, store.CreateEnvProviderParams{
		Name: "智谱-1", Platform: "zhipu", BaseUrls: `{"anthropic":"https://open.bigmodel.cn/api/anthropic"}`,
		ApiKey: "${ACCOUNT:智谱-1}", Model: "glm-5.1", GroupName: "智谱", Enabled: 1, OwnerType: "user", OwnerID: 0,
	})
	if err != nil {
		t.Fatalf("CreateEnvProvider: %v", err)
	}
	if err := s.q.SetWorkspaceEnvProvider(ctx, store.SetWorkspaceEnvProviderParams{ID: s.workspaceID, EnvProviderID: sql.NullInt64{Int64: bound.ID, Valid: true}}); err != nil {
		t.Fatalf("SetWorkspaceEnvProvider: %v", err)
	}

	s.recordActiveProvider(ctx)

	ws, err := s.q.GetWorkspace(ctx, s.workspaceID)
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if ws.ActiveEnvProviderName != "智谱-1" {
		t.Errorf("active provider name = %q, want 智谱-1", ws.ActiveEnvProviderName)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeProviderID != bound.ID {
		t.Errorf("activeProviderID = %d, want %d", s.activeProviderID, bound.ID)
	}
}

// TestRecordActiveProvider_FollowsGroupFallback verifies the record follows
// the group's healthy member: once the bound member is rate-limited, the next
// spawn-time record points at the fallback — each workspace independently
// re-aligns at its own process starts.
func TestRecordActiveProvider_FollowsGroupFallback(t *testing.T) {
	ctx := newTestContext()
	s := newDispatchTestSession(t)
	bound, err := s.q.CreateEnvProvider(ctx, store.CreateEnvProviderParams{
		Name: "智谱-1", Platform: "zhipu", BaseUrls: `{"anthropic":"https://open.bigmodel.cn/api/anthropic"}`,
		ApiKey: "${ACCOUNT:智谱-1}", Model: "glm-5.1", GroupName: "智谱", Enabled: 1, OwnerType: "user", OwnerID: 0,
	})
	if err != nil {
		t.Fatalf("CreateEnvProvider bound: %v", err)
	}
	if _, err := s.q.CreateEnvProvider(ctx, store.CreateEnvProviderParams{
		Name: "智谱-2", Platform: "zhipu", BaseUrls: `{"anthropic":"https://open.bigmodel.cn/api/anthropic"}`,
		ApiKey: "${ACCOUNT:智谱-2}", Model: "glm-5.1", GroupName: "智谱", Enabled: 1, OwnerType: "user", OwnerID: 0,
	}); err != nil {
		t.Fatalf("CreateEnvProvider fallback: %v", err)
	}
	if err := s.q.SetWorkspaceEnvProvider(ctx, store.SetWorkspaceEnvProviderParams{ID: s.workspaceID, EnvProviderID: sql.NullInt64{Int64: bound.ID, Valid: true}}); err != nil {
		t.Fatalf("SetWorkspaceEnvProvider: %v", err)
	}
	resetAt := time.Now().Add(5 * time.Hour).Truncate(time.Second)
	if err := sceneenv.MarkProviderCooldown(ctx, s.q, bound.ID, resetAt); err != nil {
		t.Fatalf("MarkProviderCooldown: %v", err)
	}

	s.recordActiveProvider(ctx)

	ws, err := s.q.GetWorkspace(ctx, s.workspaceID)
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if ws.ActiveEnvProviderName != "智谱-2" {
		t.Errorf("after cooldown the record must follow the fallback: %q", ws.ActiveEnvProviderName)
	}
}
```

注意：`newDispatchTestSession` / 测试 ctx 的现成写法以 `provider_rate_limit_test.go` 与同包其他测试为准（若 ctx 直接用 `context.Background()` 就用它，删掉 `newTestContext`，以能编译为准）。`notifyHub` 在测试会话里为 nil，`recordActiveProvider` 必须容忍（Step 2 有 nil 守卫）。

- [ ] **Step 2: 运行确认编译失败（方法不存在）**

Run: `cd server && go test ./internal/agentproxy/ -run TestRecordActiveProvider -count=1`
Expected: 编译错误 `s.recordActiveProvider undefined`。

- [ ] **Step 3: 实现 `recordActiveProvider` + `broadcastActiveProvider`，并改写 ensureProcess**

`proxy.go`：把 ensureProcess 中现有 ActiveProvider 块（`// Remember which provider this process actually spawned with…` 起，至 `closeProviderRateLimitEvents` 调用止）整体替换为一行调用：

```go
		// Resolve the active provider, persist its name on the workspace row
		// (per-workspace state, spec 2026-09-21), and broadcast the change.
		// Runs on every spawn — including the 429 fallback restart — so the
		// record and the UI follow the member actually in use.
		s.recordActiveProvider(ctx)
```

并在同文件（provider_usage.go 亦可，按就近原则放 proxy.go）新增两个方法：

```go
// recordActiveProvider resolves the workspace's active provider at spawn time,
// keeps the in-memory 429-attribution id in sync, persists the provider NAME
// on the workspace row (per-workspace record of what is actually running),
// and notifies listeners. Safe to call when no provider resolves (records '').
func (s *AgentSession) recordActiveProvider(ctx context.Context) {
	activeName := ""
	if p, ok := sceneenv.ActiveProvider(ctx, s.q, s.workspaceID); ok {
		activeName = p.Name
		s.mu.Lock()
		s.activeProviderID = p.ID
		s.mu.Unlock()
		// Being selected is the honest "back in service" signal — see the
		// historical comment this block was extracted from.
		if !sceneenv.ProviderInCooldown(p, time.Now()) {
			closeProviderRateLimitEvents(ctx, s.q, p.ID, time.Now(), false)
		}
	}
	if err := s.q.SetWorkspaceActiveEnvProvider(ctx, store.SetWorkspaceActiveEnvProviderParams{
		ActiveEnvProviderName: activeName,
		ID:                    s.workspaceID,
	}); err != nil {
		slog.Warn("agent: persist active provider failed", "workspaceID", s.workspaceID, "error", err)
	}
	s.broadcastActiveProvider(ctx, activeName)
}

// broadcastActiveProvider pushes the provider_changed workspace notification
// so an open chat view refreshes its provider pill without a reload. Best
// effort: hub-less sessions (tests, temporary) and owner lookup failures
// degrade to a zero-owner broadcast or a log line.
func (s *AgentSession) broadcastActiveProvider(ctx context.Context, name string) {
	if s.notifyHub == nil || s.isTemporary {
		return
	}
	ownerType, ownerID := "", int64(0)
	if ws, err := s.q.GetWorkspace(ctx, s.workspaceID); err == nil {
		ownerType, ownerID = ws.OwnerType, ws.OwnerID
	} else {
		slog.Warn("provider_changed: GetWorkspace failed; broadcasting with zero owner",
			"workspace_id", s.workspaceID, "error", err)
	}
	s.notifyHub.Broadcast(notify.Notification{
		Topic:     notify.TopicWorkspace,
		Action:    "provider_changed",
		ID:        s.workspaceID,
		Extra:     map[string]string{"providerName": name},
		OwnerType: ownerType,
		OwnerID:   ownerID,
	})
}
```

- [ ] **Step 4: PTY 写入点（service/agent.go Start）**

在 `agent.go` `Start` 中 `envSlice := convertEnvVarsToSliceFromStore(envVars)`（L207）之后追加：

```go
	// Per-workspace record of the provider this PTY process actually spawned
	// with (spec 2026-09-21). Same semantics as the agentproxy chat path:
	// refreshed at every spawn, not cleared on unbind.
	activeName := ""
	if p, ok := sceneenv.ActiveProvider(ctx, m.q, workspaceID); ok {
		activeName = p.Name
	}
	if err := m.q.SetWorkspaceActiveEnvProvider(ctx, store.SetWorkspaceActiveEnvProviderParams{
		ActiveEnvProviderName: activeName,
		ID:                    workspaceID,
	}); err != nil {
		slog.Warn("agent: persist active provider failed", "workspaceID", workspaceID, "error", err)
	}
	if m.notifyHub != nil {
		m.notifyHub.Broadcast(notify.Notification{
			Topic:     notify.TopicWorkspace,
			Action:    "provider_changed",
			ID:        workspaceID,
			Extra:     map[string]string{"providerName": activeName},
			OwnerType: ws.OwnerType,
			OwnerID:   ws.OwnerID,
		})
	}
```

（`ws` 已在 L139 取得；`notify` 包 service/agent.go 已导入——`SetNotifyHub` 在同文件。）

- [ ] **Step 5: 测试通过 + 全量相关包回归**

Run: `cd server && go test ./internal/agentproxy/ -run 'TestRecordActiveProvider|TestMaybeMarkRateLimitedProvider' -race -count=1 && go vet ./internal/agentproxy/ ./internal/service/`
Expected: PASS（现有 `TestMaybeMarkRateLimitedProvider_*` 不回归——它们直接调 `maybeMarkRateLimitedProvider`，不经 ensureProcess）。

- [ ] **Step 6: Commit**

```bash
git -C <repo> add server/internal/agentproxy/proxy.go server/internal/agentproxy/provider_active_test.go server/internal/service/agent.go
git -C <repo> commit -m "feat(spawn): 两路径持久化每空间当前 provider 名并广播 provider_changed"
```

---

### Task 4: DTO + TS 类型透出

**Files:**
- Modify: `server/internal/api/response.go`（`WorkspaceResponse` 结构体 L628 附近 + `toWorkspaceResponse` L669 附近）
- Modify: `server/web/src/types/api.ts`（Workspace 接口，L379 `env_provider_group` 附近）

**Interfaces:**
- Consumes: Task 2 的 `store.Workspace.ActiveEnvProviderName`。
- Produces: REST `WorkspaceResponse.active_env_provider_name: string`；TS `Workspace.active_env_provider_name?: string`。Task 5 消费。

- [ ] **Step 1: Go DTO**

`WorkspaceResponse` 结构体在 `EnvProviderGroup` 字段后追加：

```go
	// ActiveEnvProviderName is the provider NAME this workspace's agent
	// process actually spawned with ('' = none recorded yet). Read-only:
	// maintained server-side at each spawn.
	ActiveEnvProviderName string `json:"active_env_provider_name"`
```

`toWorkspaceResponse` 返回值字面量在 `EnvProviderGroup: w.EnvProviderGroup,` 后追加：

```go
		ActiveEnvProviderName: w.ActiveEnvProviderName,
```

- [ ] **Step 2: TS 类型**

`types/api.ts` Workspace 接口 `env_provider_group?: string;`（L379）后追加：

```ts
  active_env_provider_name?: string; // 当前 agent 进程实际使用的 provider 名（spawn 时服务端记录）
```

- [ ] **Step 3: 验证 + Commit**

Run: `cd server && go vet ./internal/api/ && go build ./internal/api/`（api 包不依赖 web embed 可直接 build；若 embed 报错则改用 `go vet` + `go test ./internal/api/ -count=1`）
Expected: 通过。

```bash
git -C <repo> add server/internal/api/response.go server/web/src/types/api.ts
git -C <repo> commit -m "feat(api): WorkspaceResponse 透出 active_env_provider_name"
```

---

### Task 5: 前端 provider pill + 实时刷新 + i18n

**Files:**
- Modify: `server/web/src/pages/workspaces/panels/chat-input.tsx`
- Modify: `server/web/src/i18n/locales/zh-CN/workspaces.json`、`en/workspaces.json`、`zh-TW/workspaces.json`（`panels.chatInput` 段）

**Interfaces:**
- Consumes: Task 3 的 WS `provider_changed` 通知；Task 4 的 `workspace.active_env_provider_name`；现有 `useNotificationWSStore`（`stores/notification-ws-store.ts` 的 `lastMessage`）。
- Produces: 用户可见的 chat 状态栏 provider pill。

- [ ] **Step 1: i18n 三语言键**

三个 locale 的 `workspaces.json` 中 `chatInput` 对象内（`"noChanges"` 键旁）追加：

zh-CN：
```json
"activeProvider": "当前使用的 Provider",
```
en：
```json
"activeProvider": "Current provider",
```
zh-TW：
```json
"activeProvider": "當前使用的 Provider",
```

- [ ] **Step 2: chat-input.tsx — 订阅 notify 失效 workspace query**

导入区（L4 lucide 行加 `Zap`；新增 store 导入）：

```ts
import { Bot, Send, X, Loader2, Paperclip, ListPlus, Activity, Zap } from 'lucide-react';
import { useNotificationWSStore } from '@/stores/notification-ws-store';
```

组件体内（现有 useEffect 区附近）追加：

```tsx
  // Live provider pill: a provider_changed workspace notification (spawn or
  // 429 fallback restart) invalidates workspace queries so the name below
  // always reflects the provider actually in use.
  const queryClient = useQueryClient();
  const lastNotify = useNotificationWSStore((s) => s.lastMessage);
  useEffect(() => {
    if (!lastNotify) return;
    if (lastNotify.topic !== 'workspace' || lastNotify.action !== 'provider_changed') return;
    if (String(lastNotify.id) !== String(workspace.id)) return;
    queryClient.invalidateQueries({ queryKey: ['workspace'] });
  }, [lastNotify, queryClient, workspace.id]);
```

（`useQueryClient` 已在文件头部导入；`lastNotify.id` 用 `String()` 比较以兼容 number/string wire 类型。若 `Notification` 类型的 `id` 为可选，先判 `lastNotify.id != null`。）

- [ ] **Step 3: 渲染 pill（状态栏右侧组，usage pill 之前）**

`chat-input.tsx` L354 `<div className="flex items-center gap-2">` 内、`{(() => {` usage pill IIFE 之前插入：

```tsx
          {/* Active provider pill (spec 2026-09-21): which provider this
              workspace's agent process is actually using. Hidden when the
              workspace has no provider env in play. */}
          {workspace.active_env_provider_name ? (
            <span
              className="flex items-center gap-1 rounded-full bg-muted px-2 py-0.5 text-xs text-muted-foreground"
              title={t('panels.chatInput.activeProvider')}
            >
              <Zap className="h-3 w-3" />
              <span className="max-w-32 truncate">{workspace.active_env_provider_name}</span>
            </span>
          ) : null}
```

- [ ] **Step 4: 前端构建 + lint**

Run: `cd server/web && pnpm build && pnpm lint`
Expected: `tsc -b && vite build` 成功；lint 无新增告警。

- [ ] **Step 5: Commit**

```bash
git -C <repo> add server/web/src/pages/workspaces/panels/chat-input.tsx server/web/src/i18n/locales/zh-CN/workspaces.json server/web/src/i18n/locales/en/workspaces.json server/web/src/i18n/locales/zh-TW/workspaces.json
git -C <repo> commit -m "feat(web): chat 状态栏显示当前 provider 名并实时跟随切换"
```

---

### Task 6: 端到端验证与收尾

**Files:** 无新改动（验证 + 手动实测记录）。

- [ ] **Step 1: 后端全量相关测试**

Run: `cd server && go vet ./... 2>&1 | head -20 && go test ./internal/sceneenv/ ./internal/agentproxy/ ./internal/service/ ./internal/api/ ./internal/store/ -race -count=1`
Expected: 全绿。

- [ ] **Step 2: schema 一致性**

Run: `make schema-diff && make sqlc-lint`（仓库根或 server/）
Expected: `OK: no table-level drift detected`；lint 通过。

- [ ] **Step 3: 手动浏览器实测（dev server）**

1. `make dev`（后端 :3000 + 前端 :5173）。
2. 设置 → 环境变量：建两个同 `group_name` 的 provider（组内排序 0/1），工作空间绑定该组。
3. 在该工作空间「环境变量」里手填一条 `ANTHROPIC_BASE_URL=https://stale.example`。
4. 打开工作空间 chat，发一条消息触发 spawn → 状态栏出现 pill，显示组内第一成员名；` respect验证覆盖`：agent 实际请求走的是 provider 的 base_url（不是 stale.example）——看 provider 的 token 用量/日志确认。
5. 手动把第一成员 enabled 关掉，重发消息 → pill 变为第二成员名（重新解析生效）。
6. 恢复第一成员，触发 429 场景（或手动设 cooldown_until）→ spawn 后 pill 跟随 fallback。
7. 打开一个未绑定 provider 的工作空间 → 无 pill，行为与改动前一致。

- [ ] **Step 4: 收尾提交（如手动验证过程无代码改动则跳过）**

```bash
git -C <repo> status --short
```
Expected: 干净（全部改动已在前 5 个任务提交）。

---

## Self-Review 记录

1. **Spec 覆盖**：需求 1（spawn 重解析覆盖）→ Task 1；需求 2（稳定算法/组内排序）→ 现有 `GroupProvider` 不动，Task 1 的组绑定测试守护；需求 3（每空间独立跟进）→ Task 2+3；需求 4（状态栏 pill + 实时）→ Task 4+5；验收 1-4 → Task 1/3/5/6 对应。无缺口。
2. **占位符扫描**：无 TBD/TODO；所有代码步骤含完整代码；Task 3 测试对测试基建差异（ctx 构造）给出了以同包现有测试为准的明确指引。
3. **类型一致性**：`SetWorkspaceActiveEnvProviderParams{ActiveEnvProviderName, ID}`（Task 2 定义 → Task 3 使用）；`active_env_provider_name`（Task 3 WS extra / Task 4 Go+TS 字段 / Task 5 消费）全链路同名；WS wire shape `{topic:'workspace', action:'provider_changed', extra.providerName}`（Task 3 产 → Task 5 消费）一致。
