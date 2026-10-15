# SSE/WS 长连接 token 过期自愈设计

日期：2026-10-15
状态：已批准（方案 A）
来源事故：self 生产环境（self.niu6ai.com），用户 IP `218.77.105.129`（workspace 254）自 01:08 起持续 6+ 小时 401 死循环重连，页面表现为"卡住"。

## 1. 问题

三层客户端通道的 401 处理不一致：

| 通道 | 401 处理 | 现状 |
|---|---|---|
| REST（`lib/api.ts` `apiFetch`） | 主动预刷新（`isAccessTokenNearExpiry`）+ 401→`refreshAccessToken`→重试→仍 401 则 logout+跳 /login | 完善 |
| Agent SSE（`stores/agent-sse-store.ts`） | `es.onerror` → 2 秒后带旧 token 重连 | 无限 401 循环：不 refresh、不 logout |
| Notify WS（`stores/notification-ws-store.ts`） | `ws.onclose` → 指数退避（1s→30s）重连 | 同样的循环 |

约束：`EventSource` / `WebSocket` 的错误回调不暴露 HTTP 状态码，前端无法直接区分「401 token 过期」与「网络中断」。但 auth-store 已具备判断与恢复的全部工具：

- `isAccessTokenNearExpiry(marginSeconds)` — 解析 JWT `exp`（不验签）
- `refreshAccessToken()` — 用 refresh token 换新，带 inflight 并发去重

## 2. 方案（已批准：A）

重连前检测 token 已过期或连续失败 ≥3 次 → 先 `refreshAccessToken()`：成功则拿新 token 静默重连（用户无感知）；refresh 失败（refreshToken 也失效）→ `logoutAndRedirect()`，终止循环，用户重新登录一次即恢复。与 REST 层语义完全一致。

落选方案：B（仅静默自愈，refresh 失败只停止重连，等用户 REST 401 自然跳转——用户可能对着僵死页面更久）；C（改服务端协议，三端联动，改动大）。

## 3. 变更点

### 3.1 `stores/auth-store.ts` — 新增共享 helper

```ts
export function logoutAndRedirect(): void {
  useAuthStore.getState().logout();
  window.location.href = '/login';
}
```

`lib/api.ts` 的 `apiFetch` 中内联的 `logout(); window.location.href = '/login'` 两行替换为调用它。三处共用同一语义：「会话不可恢复，登出并回登录页」。无循环依赖（agent-sse-store 已 import auth-store；auth-store 不依赖任何 store 消费方）。

### 3.2 `stores/agent-sse-store.ts` — 核心修复

模块级新增 `let consecutiveErrors = 0`（store 为单例，模块级即可）：

- `es.onopen` → `consecutiveErrors = 0`
- `es.onerror` → `consecutiveErrors++`；既有的 2 秒延迟重连 setTimeout 内，重连前判断：

```
if (isAccessTokenNearExpiry(0) || consecutiveErrors >= 3) {
  const newToken = await refreshAccessToken();
  if (!newToken) { logoutAndRedirect(); return; }
}
get().connect(ws);
```

- `isAccessTokenNearExpiry(0)`：margin=0，仅 token 真过期才触发
- `consecutiveErrors >= 3`：token 未过期但服务端持续拒绝（如服务端轮换 JWT 密钥）的兜底
- `refreshAccessToken()` 自带 inflight 去重，SSE 与 WS 同时触发只发一次网络请求
- `doRefresh` 失败路径内部已 `logout()`；`logoutAndRedirect` 重复调用幂等无害
- 既有 `intentionalClose` 闩锁检查保持不变（重连 setTimeout 内已存在）

### 3.3 `stores/notification-ws-store.ts` — 同样处理

模块级 `consecutiveErrors`：

- `ws.onopen` → 清零（保留既有 backoffMs 重置）
- `ws.onclose` → `consecutiveErrors++`；在 `scheduleReconnect()` 之前判断（条件与 3.2 相同）：需要刷新时先 `await refreshAccessToken()`，失败 → `logoutAndRedirect()` 并 return（不排重连定时器）；成功或无需刷新 → 走既有 `scheduleReconnect()`（指数退避节奏不变）

## 4. 明确不做

- 不改 EventSource/WebSocket 协议、不改服务端（`/ws/sse`、`/ws/notify` 行为不变）
- 不动 terminal WS（用户主动打开，断开有显式 UI，无死循环）
- 不处理跨 tab refresh token 轮换竞争（REST 层既有行为，超范围）

## 5. 测试

- `agent-sse-store.test.ts`（已有）新增用例：
  1. token 过期 + onerror → `refreshAccessToken` 被调用 → 新 token 重连成功
  2. refresh 失败 → `logoutAndRedirect` 被调用、不再重连
  3. token 未过期但连续 3 次 onerror → 第 3 次触发 refresh
  4. onopen 清零连续失败计数（后续 onerror 直接重连不触发 refresh）
- `notification-ws-store` 新增最小测试（mock WebSocket 类）：onclose 后 token 过期 → refresh 失败 → logoutAndRedirect、无重连定时器
- `lib/api.ts` 的 helper 替换靠现有 api 测试回归（`api.headers.test.ts` 等）

## 6. 验收口径

生产复现场景：登录后把页面挂到 access token 过期之后 → SSE/WS 自动换新 token 恢复，全程无感知；把 refresh token 也搞失效（或等服务端拒绝）→ 页面自动回到 /login，不再出现无限 401 重连。
