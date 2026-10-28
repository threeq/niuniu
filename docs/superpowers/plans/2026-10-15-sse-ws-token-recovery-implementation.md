# SSE/WS Token 自愈修复 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 修复 agent SSE 与 notify WS 重连循环在 token 过期后无限 401 的 bug —— 重连前自愈（refresh），不可恢复时 logout + 跳登录页。

**Architecture:** auth-store 新增共享 `logoutAndRedirect()` helper；SSE/WS 两个 store 的重连路径在「token 已过期（JWT exp 判定）或连续失败 ≥3 次」时先调 `refreshAccessToken()`（自带 inflight 去重），失败则跳登录。REST 层（api.ts）语义不变，仅复用新 helper。

**Tech Stack:** React 19 + TypeScript + Zustand + Vitest（fake timers + 手写 EventSource/WebSocket mock）。

**Spec:** `docs/superpowers/specs/2026-10-15-sse-ws-token-recovery-design.md`

## Global Constraints

- 工作目录：`server/web/`；包管理器 pnpm；测试命令 `pnpm vitest run <file>`；类型检查 `pnpm tsc -b`
- 不改服务端、不改 EventSource/WebSocket 协议、不动 terminal WS
- `refreshAccessToken()` 已有 inflight 去重，禁止再包一层去重
- `EventSource`/`WebSocket` 回调拿不到 HTTP 状态码——只能靠 `isAccessTokenNearExpiry(0)`（本地 exp）与 `consecutiveErrors >= 3` 两个信号
- git commit：Conventional Commits 中文描述，不加任何 Co-Authored-By 签名
- 所有新增导出的注释用英文（与文件内既有注释风格一致）

---

### Task 1: auth-store `logoutAndRedirect()` + api.ts 收敛

**Files:**
- Modify: `server/web/src/stores/auth-store.ts`（文件末尾追加）
- Modify: `server/web/src/lib/api.ts:1-8`（import 块）与 `server/web/src/lib/api.ts:207-213`（401 收口）

**Interfaces:**
- Consumes: 无（新函数）
- Produces: `logoutAndRedirect(): void` — Task 2、Task 3 依赖此签名

- [ ] **Step 1: 在 auth-store.ts 末尾（`doRefresh` 函数之后）追加 helper**

```ts
/**
 * Unrecoverable-session teardown shared by every auth consumer: clear the
 * persisted credentials and bounce to /login. Callers: apiFetch (REST 401
 * after a failed refresh) and the SSE/WS reconnect loops (their refresh
 * attempt failed). Idempotent — logout() is a no-op when already logged out.
 */
export function logoutAndRedirect(): void {
  useAuthStore.getState().logout();
  window.location.href = '/login';
}
```

- [ ] **Step 2: api.ts 改用 helper（两处）**

import 块（第 3-8 行）改为——注意移除 `useAuthStore`（替换后唯一使用点消失，留着会让 `tsc -b` 报 unused）：

```ts
import {
  getAccessToken,
  logoutAndRedirect,
  refreshAccessToken,
  isAccessTokenNearExpiry,
} from '@/stores/auth-store'
```

401 收口（现第 207-213 行）：

```ts
    if (response.status === 401) {
      if (token) {
        logoutAndRedirect();
      }
      throw new ApiError(401, 'Session expired', {});
    }
```

- [ ] **Step 3: 类型检查 + 相关测试回归**

Run: `cd server/web && pnpm tsc -b && pnpm vitest run src/lib/api.headers.test.ts src/stores/agent-sse-store.test.ts`
Expected: tsc 0 错误；vitest 全部 PASS（既有 3 个 SSE 用例不受影响）

- [ ] **Step 4: Commit**

```bash
git add server/web/src/stores/auth-store.ts server/web/src/lib/api.ts
git commit -m "refactor(auth): 抽出共享 logoutAndRedirect helper，apiFetch 401 收口复用"
```

---

### Task 2: agent-sse-store 重连自愈 + 测试

**Files:**
- Modify: `server/web/src/stores/agent-sse-store.ts`（import 块、`connect` 内 onopen/onerror）
- Create: `server/web/src/stores/agent-sse-store.auth-recovery.test.ts`

**Interfaces:**
- Consumes: `logoutAndRedirect(): void`（Task 1）；`refreshAccessToken(): Promise<string | null>`、`isAccessTokenNearExpiry(marginSeconds?: number): boolean`（auth-store 既有导出）
- Produces: 无新导出（行为修复）

- [ ] **Step 1: 写失败测试（新文件 `agent-sse-store.auth-recovery.test.ts`）**

```ts
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useAgentSSEStore } from '@/stores/agent-sse-store';
import {
  getAccessToken,
  isAccessTokenNearExpiry,
  logoutAndRedirect,
  refreshAccessToken,
} from '@/stores/auth-store';

// File-scoped auth-store mock: the recovery logic under test is exactly the
// calls into these four functions, so the mock records them while keeping the
// store's token plumbing (getAccessToken) scriptable per test.
vi.mock('@/stores/auth-store', () => ({
  getAccessToken: vi.fn(() => mockedAuth.token),
  isAccessTokenNearExpiry: vi.fn(() => mockedAuth.expired),
  refreshAccessToken: vi.fn(() => mockedAuth.refreshResult),
  logoutAndRedirect: vi.fn(),
}));

const mockedAuth = vi.mocked({
  token: null as string | null,
  expired: false,
  refreshResult: null as Promise<string | null> | null,
});

// Same tracking mock as agent-sse-store.test.ts: records every constructed
// instance so tests can count streams and drive lifecycle events.
class MockEventSource {
  static instances: MockEventSource[] = [];
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;
  readyState = MockEventSource.CONNECTING;
  url: string;
  onopen: ((ev: Event) => unknown) | null = null;
  onmessage: ((ev: MessageEvent) => unknown) | null = null;
  onerror: ((ev: Event) => unknown) | null = null;
  constructor(url: string) {
    this.url = url;
    MockEventSource.instances.push(this);
  }
  close() {
    this.readyState = MockEventSource.CLOSED;
  }
  emitError() {
    this.onerror?.(new Event('error'));
  }
  emitOpen() {
    this.readyState = MockEventSource.OPEN;
    this.onopen?.(new Event('open'));
  }
}

function resetStore() {
  useAgentSSEStore.setState({
    es: null,
    subscribedWorkspaces: new Set(),
    handlers: new Map(),
    globalHandlers: new Set(),
    lastEventTs: 0,
    reconnectTimer: null,
    intentionalClose: false,
    onReconnectCallbacks: new Set(),
  });
}

describe('agent-sse-store token recovery', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    MockEventSource.instances = [];
    vi.stubGlobal('EventSource', MockEventSource);
    mockedAuth.token = 'tok';
    mockedAuth.expired = false;
    mockedAuth.refreshResult = null;
    vi.mocked(refreshAccessToken).mockClear();
    vi.mocked(logoutAndRedirect).mockClear();
    resetStore();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  it('refreshes and reconnects with the new token when the JWT is expired', async () => {
    mockedAuth.expired = true;
    mockedAuth.refreshResult = Promise.resolve('tok-2');
    mockedAuth.token = 'tok-2';

    useAgentSSEStore.getState().addWorkspace('254');
    await vi.advanceTimersByTimeAsync(300);
    expect(MockEventSource.instances).toHaveLength(1);

    MockEventSource.instances[0].emitError();
    await vi.advanceTimersByTimeAsync(2000);

    expect(refreshAccessToken).toHaveBeenCalledTimes(1);
    expect(logoutAndRedirect).not.toHaveBeenCalled();
    expect(MockEventSource.instances).toHaveLength(2);
    expect(MockEventSource.instances[1].url).toContain('token=tok-2');
  });

  it('logs out and stops reconnecting when the refresh fails', async () => {
    mockedAuth.expired = true;
    mockedAuth.refreshResult = Promise.resolve(null);

    useAgentSSEStore.getState().addWorkspace('254');
    await vi.advanceTimersByTimeAsync(300);
    MockEventSource.instances[0].emitError();
    await vi.advanceTimersByTimeAsync(2000);

    expect(logoutAndRedirect).toHaveBeenCalledTimes(1);
    expect(MockEventSource.instances).toHaveLength(1);
  });

  it('forces a refresh after 3 consecutive errors even with a valid JWT', async () => {
    mockedAuth.refreshResult = Promise.resolve('tok-2');
    mockedAuth.token = 'tok-2';

    useAgentSSEStore.getState().addWorkspace('254');
    await vi.advanceTimersByTimeAsync(300);

    // Errors 1 and 2: server hiccup territory — plain reconnect, no refresh.
    MockEventSource.instances[0].emitError();
    await vi.advanceTimersByTimeAsync(2000);
    MockEventSource.instances[1].emitError();
    await vi.advanceTimersByTimeAsync(2000);
    expect(refreshAccessToken).not.toHaveBeenCalled();

    // Error 3: same token keeps failing — try the refresh.
    MockEventSource.instances[2].emitError();
    await vi.advanceTimersByTimeAsync(2000);
    expect(refreshAccessToken).toHaveBeenCalledTimes(1);
    expect(MockEventSource.instances).toHaveLength(4);
  });

  it('clears the consecutive-error count on a successful open', async () => {
    mockedAuth.refreshResult = Promise.resolve('tok-2');
    mockedAuth.token = 'tok-2';

    useAgentSSEStore.getState().addWorkspace('254');
    await vi.advanceTimersByTimeAsync(300);

    // Two failures, then a successful open — the counter must reset.
    MockEventSource.instances[0].emitError();
    await vi.advanceTimersByTimeAsync(2000);
    MockEventSource.instances[1].emitOpen();
    MockEventSource.instances[1].emitError();
    await vi.advanceTimersByTimeAsync(2000);
    MockEventSource.instances[2].emitError();
    await vi.advanceTimersByTimeAsync(2000);
    expect(refreshAccessToken).not.toHaveBeenCalled();
  });
});
```

- [ ] **Step 2: 运行确认失败**

Run: `cd server/web && pnpm vitest run src/stores/agent-sse-store.auth-recovery.test.ts`
Expected: FAIL —— 用例 1/2 中 `refreshAccessToken` 从未被调用（现实现直接重连），断言 `toHaveBeenCalledTimes(1)` 失败

- [ ] **Step 3: 实现 agent-sse-store.ts 修复**

import 块（现第 2 行）改为：

```ts
import {
  getAccessToken,
  isAccessTokenNearExpiry,
  logoutAndRedirect,
  refreshAccessToken,
} from '@/stores/auth-store';
```

`generateId` 函数之后（现第 20 行附近）加模块级计数器：

```ts
// Consecutive onerror count without an intervening onopen. EventSource hides
// the HTTP status code, so "the same token keeps failing" is the only
// server-rejection signal available — at 3 the reconnect path tries a token
// refresh even when the local exp claim still looks valid (e.g. the server
// rotated its JWT signing key).
let consecutiveErrors = 0;
```

`connect` 内 `es.onopen`（现第 110-116 行）加清零：

```ts
    es.onopen = () => {
      consecutiveErrors = 0;
      console.info('[SSE] CONNECTED', { windowId, workspaces: workspaceIds, since: lastTs || 'initial' });
      if (lastTs > 0) {
        // This is a reconnect, not initial connect
        get().onReconnectCallbacks.forEach(fn => fn());
      }
    };
```

`es.onerror`（现第 118-131 行）整体替换为：

```ts
    es.onerror = () => {
      const currentEs = get().es;
      if (currentEs !== es || get().intentionalClose) return;

      set({ es: null });
      consecutiveErrors++;
      // Auto-reconnect with all current workspaces
      setTimeout(() => {
        const ws = Array.from(get().subscribedWorkspaces);
        if (ws.length === 0 || get().intentionalClose) return;
        void (async () => {
          // Token recovery: the JWT is past exp, or the same token has kept
          // failing (EventSource hides the status code). Refresh first so an
          // expired-session loop cannot spin forever; a failed refresh means
          // the session is unrecoverable — bounce to login like apiFetch does.
          if (isAccessTokenNearExpiry(0) || consecutiveErrors >= 3) {
            const newToken = await refreshAccessToken();
            if (!newToken) {
              logoutAndRedirect();
              return;
            }
          }
          console.info('[SSE] reconnecting...', { workspaces: ws });
          get().connect(ws);
        })();
      }, 2000);
    };
```

- [ ] **Step 4: 运行新测试 + 既有回归**

Run: `cd server/web && pnpm vitest run src/stores/agent-sse-store.auth-recovery.test.ts src/stores/agent-sse-store.test.ts && pnpm tsc -b`
Expected: 新文件 4 用例 PASS；既有 3 用例 PASS；tsc 0 错误

- [ ] **Step 5: Commit**

```bash
git add server/web/src/stores/agent-sse-store.ts server/web/src/stores/agent-sse-store.auth-recovery.test.ts
git commit -m "fix(web): agent SSE 重连自愈——token 过期/连续失败先 refresh，不可恢复跳登录（修 401 死循环卡死）"
```

---

### Task 3: notification-ws-store 重连自愈 + 测试

**Files:**
- Modify: `server/web/src/stores/notification-ws-store.ts`（import 块、模块级计数器、`ws.onopen`/`ws.onclose`）
- Create: `server/web/src/stores/notification-ws-store.test.ts`

**Interfaces:**
- Consumes: `logoutAndRedirect(): void`（Task 1）；`refreshAccessToken` / `isAccessTokenNearExpiry`（auth-store 既有）
- Produces: 无新导出

- [ ] **Step 1: 写失败测试（新文件 `notification-ws-store.test.ts`）**

```ts
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useNotificationWSStore } from '@/stores/notification-ws-store';
import {
  isAccessTokenNearExpiry,
  logoutAndRedirect,
  refreshAccessToken,
} from '@/stores/auth-store';

vi.mock('@/stores/auth-store', () => ({
  getAccessToken: vi.fn(() => mockedAuth.token),
  isAccessTokenNearExpiry: vi.fn(() => mockedAuth.expired),
  refreshAccessToken: vi.fn(() => mockedAuth.refreshResult),
  logoutAndRedirect: vi.fn(),
}));

const mockedAuth = vi.mocked({
  token: null as string | null,
  expired: false,
  refreshResult: null as Promise<string | null> | null,
});

// Tracking WebSocket mock: close() synchronously fires onclose (the real one
// is async) — enough for the reconnect-gate logic under test.
class MockWebSocket {
  static instances: MockWebSocket[] = [];
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 3;
  readyState = MockWebSocket.CONNECTING;
  url: string;
  onopen: ((ev: Event) => unknown) | null = null;
  onmessage: ((ev: MessageEvent) => unknown) | null = null;
  onclose: ((ev: CloseEvent) => unknown) | null = null;
  onerror: ((ev: Event) => unknown) | null = null;
  constructor(url: string) {
    this.url = url;
    MockWebSocket.instances.push(this);
  }
  close() {
    if (this.readyState === MockWebSocket.CLOSED) return;
    this.readyState = MockWebSocket.CLOSED;
    this.onclose?.(new CloseEvent('close'));
  }
  send() {}
  emitOpen() {
    this.readyState = MockWebSocket.OPEN;
    this.onopen?.(new Event('open'));
  }
  emitClose() {
    this.close();
  }
}

describe('notification-ws-store token recovery', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    MockWebSocket.instances = [];
    vi.stubGlobal('WebSocket', MockWebSocket);
    mockedAuth.token = 'tok';
    mockedAuth.expired = false;
    mockedAuth.refreshResult = null;
    vi.mocked(refreshAccessToken).mockClear();
    vi.mocked(logoutAndRedirect).mockClear();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  it('refreshes and reconnects when the JWT is expired', async () => {
    mockedAuth.expired = true;
    mockedAuth.refreshResult = Promise.resolve('tok-2');
    mockedAuth.token = 'tok-2';

    useNotificationWSStore.getState().connect();
    expect(MockWebSocket.instances).toHaveLength(1);

    MockWebSocket.instances[0].emitClose();
    await vi.advanceTimersByTimeAsync(1000);

    expect(refreshAccessToken).toHaveBeenCalledTimes(1);
    expect(logoutAndRedirect).not.toHaveBeenCalled();
    expect(MockWebSocket.instances).toHaveLength(2);
    expect(MockWebSocket.instances[1].url).toContain('token=tok-2');
  });

  it('logs out and never schedules a reconnect when the refresh fails', async () => {
    mockedAuth.expired = true;
    mockedAuth.refreshResult = Promise.resolve(null);

    useNotificationWSStore.getState().connect();
    MockWebSocket.instances[0].emitClose();
    await vi.advanceTimersByTimeAsync(1000);

    expect(logoutAndRedirect).toHaveBeenCalledTimes(1);
    // The 30s backoff ceiling must not produce a new socket either.
    await vi.advanceTimersByTimeAsync(60_000);
    expect(MockWebSocket.instances).toHaveLength(1);
  });

  it('forces a refresh after 3 consecutive closes even with a valid JWT', async () => {
    mockedAuth.refreshResult = Promise.resolve('tok-2');
    mockedAuth.token = 'tok-2';

    useNotificationWSStore.getState().connect();
    // Backoff: 1s, 2s, 4s — advance generously between closes.
    MockWebSocket.instances[0].emitClose();
    await vi.advanceTimersByTimeAsync(2000);
    MockWebSocket.instances[1].emitClose();
    await vi.advanceTimersByTimeAsync(4000);
    expect(refreshAccessToken).not.toHaveBeenCalled();

    MockWebSocket.instances[2].emitClose();
    await vi.advanceTimersByTimeAsync(8000);
    expect(refreshAccessToken).toHaveBeenCalledTimes(1);
  });
});
```

- [ ] **Step 2: 运行确认失败**

Run: `cd server/web && pnpm vitest run src/stores/notification-ws-store.test.ts`
Expected: FAIL —— 用例 1/2 中 `refreshAccessToken` 从未被调用

- [ ] **Step 3: 实现 notification-ws-store.ts 修复**

import 块（现第 1-4 行）改为：

```ts
import { create } from 'zustand';
import {
  getAccessToken,
  isAccessTokenNearExpiry,
  logoutAndRedirect,
  refreshAccessToken,
} from '@/stores/auth-store';
import { useOrgStore } from '@/stores/org-store';
import type { Notification } from '@/lib/notification-dispatcher';
```

`MAX_BACKOFF` 常量之后（现第 7 行后）加：

```ts
// Consecutive closes without an intervening open. WebSocket callbacks hide
// the HTTP status code — at 3 the reconnect path tries a token refresh even
// when the local exp claim still looks valid.
let consecutiveErrors = 0;
```

`connect` 内 `ws.onopen`（现第 86-90 行）加清零：

```ts
      ws.onopen = () => {
        consecutiveErrors = 0;
        set({ connected: true });
        resetHeartbeat();
        backoffResetTimer = setTimeout(() => { backoffMs = 1000; }, 15_000);
      };
```

`ws.onclose`（现第 148-155 行）整体替换为：

```ts
      ws.onclose = () => {
        clearTimers();
        set({ ws: null, connected: false });
        if (!intentionalClose) {
          set((s) => ({ reconnectCount: s.reconnectCount + 1 }));
          consecutiveErrors++;
          // Token recovery — same gate as the agent SSE store: refresh when
          // the JWT is expired or the same token keeps failing; a failed
          // refresh is an unrecoverable session, so stop the backoff loop and
          // bounce to login instead of retrying forever.
          if (isAccessTokenNearExpiry(0) || consecutiveErrors >= 3) {
            void (async () => {
              const newToken = await refreshAccessToken();
              if (!newToken) {
                logoutAndRedirect();
                return;
              }
              scheduleReconnect();
            })();
            return;
          }
          scheduleReconnect();
        }
      };
```

注意：`scheduleReconnect` 定义在 `connect` 之前（现第 58-64 行），`onclose` 闭包引用它没有顺序问题。

- [ ] **Step 4: 运行新测试 + 全 store 回归**

Run: `cd server/web && pnpm vitest run src/stores/notification-ws-store.test.ts src/stores/ && pnpm tsc -b`
Expected: 新文件 3 用例 PASS；stores 目录全部 PASS；tsc 0 错误

- [ ] **Step 5: Commit**

```bash
git add server/web/src/stores/notification-ws-store.ts server/web/src/stores/notification-ws-store.test.ts
git commit -m "fix(web): notify WS 重连自愈——与 agent SSE 相同的 token 恢复闸门（过期/连续失败先 refresh，失败跳登录）"
```
