# Agentproxy subsystem

`server/internal/agentproxy/` 是 niuniu 与底层 CLI agent（Claude Code、OpenAI
Codex）打交道的核心。它负责拉起 CLI、把每一轮对话从用户消息驱动到完成、
把流式输出归一成统一的事件流（持久化 + SSE 推送），并维护后台任务、自动
托管（autohost）、权限提示、附件等横切机制。

本文是 long-lived 设计记录；具体行号会随重构变化，但段落标题指向的概念
保持稳定。CLAUDE.md 只放一句话引用，深入读这里。

---

## 1. 两种 CLI backend

| Backend | 进程模型 | 主驱动函数 | 备注 |
|---------|----------|-----------|------|
| Claude  | 长驻 process（`claude --stream-json --resume`） | `runOneShotTurn` → `Send()` 里走 long-running 分支 | JSONL 双向；`message_start` / `content_block_delta` / `content_block_stop` / `result` |
| Codex   | App-server JSON-RPC 子进程 | `runCodexAppServerTurn`（在 `Send()` 入口被 `cliAdapter.Type() == TypeCodex` 截获优先派发） | 通过 `codex_appserver_client.go` 的 stdio client；事件由 `codex_parser.go` + `adapter/codex.go` 转成 Claude 同款的 `stream_event` / `tool_use` / `tool_result` 形状 |

下游 `handleEvent` / `handleStreamEvent` 对两种 backend 通用——所有形状归一
完成于 adapter 层。

历史上还有 `codex exec-server` JSONL client（`codex_execserver_client.go`），
现已删除（M2.5 切到 app-server 后未再使用）。

### 1.1 Codex 文本块顺序

Codex 的 `item/agentMessage/delta` 在 adapter 里硬编码用 `BlockIndex=0`
发文本 delta。如果一轮内出现 `text → tool_use → text` 这种交错，
`handleStreamEvent` 默认按 BlockIndex 取 buffer/persist-id 会把工具后的
文本 UPDATE 进工具前同一行（用工具前的 created_at 时间戳），UI 按
created_at 排序后变成「所有文本挤前面、所有工具堆末尾」。

修复：text_delta 入口检测 `lastBlockWasTool == true` 时清掉对应 BlockIndex
的三个 map（`textBlockBufs` / `textBlockMessageIDs` / `textBlockPersisted`），
强制下一次 persist 走 INSERT 新行。Claude 因每块由 SDK 给唯一递增
BlockIndex 不受影响（清的对应 key 本就为空）。

---

## 2. Send loop 时序

`SendLoop` 是每条用户/外部消息的驱动器；它一直转直到「队列空 + autohost
不再续跑」才结束。单次 iteration：

```
0. SendLoop 入口：s.running=true / s.status=StatusRunning（**loop-scoped**，覆盖
   整个 loop 含轮间判官），defer 在退出时清回 idle。已 running 则直接 return
   （防并发起第二个 loop）。
0.5 每轮检查 s.stopRequested（Stop() 置位）→ true 则直接 return 终止 loop。

1. Send()                                      ← workspace.status = running
   • statusHook.OnAgentEvent("running")
   • 写 stdin 后经 waitForTurnComplete 阻塞等 turnDone（result 事件）
     —— select 三路：turnDone / ctx.Done / **不活动看门狗 ticker**
   • result 到达 → handleEvent 处理 → 信号 turnDone（**不动 s.running**）
   • 看门狗：readLoop/codex 事件每行 bump lastActivityAt；若连续
     turnInactivityTimeout（默认 15min）**零输出** → 判定进程僵死/丢失 →
     置 lastTurnError + killProcess + 返回 error（见关键不变量）
   • Send 返回
   • 返回后再查 s.stopRequested（kill 解阻塞后）→ true 则 return

2. wasError?
   ├─ yes → autohostMaybeRecover                ← 错误恢复 LLM 判断
   │        ok=true  → continue 1
   │        ok=false → finalizeSendLoopTurn(error) → status=attention → return
   └─ no  → 继续

3. dequeue（DB workspace_queue, FIFO）
   • 有 next → continue 1

4. autohostMaybeContinue                        ← 完成 LLM 判断（任务是否真的做完）
   • ok=true  → content=prompt, injected=true, continue 1
     （续跑提示经 Send 以 role="system" 持久化+echo，前端渲染成 autohost 系统块、
      不是「You」气泡；判官 verdict 行已说明续跑原因，故不再额外发
      "autohost: 队列空闲，自动续跑" ping。recover 提示同样 injected=true。）
   • ok=false → 继续

5. finalizeSendLoopTurn(clean)                  ← workspace.status = needs_review
   • 若 hasPendingBgWork() 且非 autohostTerminalStop → return（保持 running，见 §3.2）
   • statusHook.OnAgentEvent("done") → DB needs_review
   • eventBus.Publish(EventAgentDone)
   • notifyHub.Broadcast workspace.agent_done
   • Broadcast EventIdle → return
```

**关键不变量：**
- `s.running` / `s.status`（会话忙闲，gate `Enqueue` 与 scheduler）是 **loop-scoped**：
  SendLoop 入口置 running、defer 出口清，**整个 loop（含轮间 LLM 判官）全程为
  running**。Send 与各 turn 完成处理器只动 turnDone / lastTurnResult，不再 per-turn
  翻 running。否则判官期间会出现 running=false 的空窗，此时发消息会被分发器当成
  「空闲」再起一个 SendLoop，两个 loop 抢同一会话 → 损坏到只能重启。
- workspace.status（DB needs_review/attention，statusHook）只在 Send() 入口写一次
  `running`、finalize 写一次 `needs_review`/`attention`。SendLoop 内部即使跑 10 轮
  autohost 续跑也不反复刷数据库。
- autohost 完成判断（`autohostMaybeContinue`）**永远在前**，
  `finalize → needs_review` 是判断说「不再续跑」之后才执行的。
- **前端输入门 = loop-scoped 单一信号（防 Done 与可输入态错位）**：SPA 的
  `sessionState` 门控输入框（disabled + 发送/队列按钮），它必须与后端 loop-scoped
  `running`（`Enqueue` 用的同一个门）对应。陷阱：每个 turn 的 `result` 都会发
  `done`（autohost 链中每轮都发），`error` 同理；若前端从 `done`/`error` 推断
  idle，就会在链中途解锁输入，而后端 `running` 仍 true → 入队 → 两个门错位
  （现象：聊天已显示 Done，输入却只能加队列）。**规则**：`done`/`error` 只渲染
  历史，不动 `sessionState`；唯一翻 idle 的边是 loop 结束的 `EventIdle`，由
  `SendLoop` 的 **defer 在每条退出路径（clean/error/watchdog/stop/paced-wait）保证
  广播**。前端挂载/重连时用 `GetSession`（返回 loop-scoped `Status()`）双向校准，
  漏掉的 `EventIdle` 在下次加载自愈。`waiting_confirm`（harness gate）是前端独有
  态，重连时保留不被覆盖。
- **turn 不活动看门狗（防卡死）**：`turnDone` 只由 `result` 事件或进程退出触发，
  turn 的 ctx 是 `context.Background()` 永不取消。若 claude/codex 进程「活着但
  不再产出任何输出」（卡在工具/MCP/网络、CLI 死锁、或进程丢失），原先 `Send` 会
  **永久阻塞** → loop 永不退出 → `running` 永久 true → 前端永远「Agent is
  working」、队列永不消费、idle 定时器在 running 时只重排不杀 → 只能重启 server。
  现在 `waitForTurnComplete` 用 `lastActivityAt`（每行输出 bump）判定：连续
  `turnInactivityTimeout` 零输出即视为僵死，杀进程并把该 turn 记为 error（下一轮
  recover/attention，`--resume` 保上下文）。**测量的是「不活动」而非总时长**——
  持续流式输出的长 turn 不断刷新计时、永不被误杀。
- **Stop 必然生效（`s.stopRequested`）**：`Stop()` 先置 `stopRequested=true` 再
  `killProcess()`；killProcess 除杀进程外**直接信号 turnDone**（不依赖进程监控
  goroutine 的竞态），阻塞中的 `Send` 立即解除；SendLoop 每轮查 `stopRequested`，
  true 即 return。否则孤儿 SendLoop 会被 kill 触发的 turnDone 唤醒、把死亡 turn
  当成干净 turn 继续 autohost 续跑并重启进程，使 Stop 形同无效（旧 bug）。
  `stopRequested` 在 SendLoop 入口清零。进程**非零码异常退出**时监控 goroutine
  在 `running && !stopRequested` 下置 `lastTurnError`，使崩溃走有界 recover 而非
  无声续跑。
- **Stop 真实杀死整个进程树（不止主 PID）**：`claude` 会派生子进程（Bash 工具、
  niuniu-mcp、各 MCP server）。`os.Process.Kill` 只杀单个 PID → 子进程变孤儿继续
  跑。`killProcess` 改用 `killPIDTree`：Windows 走 `taskkill /F /T /PID`（沿树清），
  Unix 在 spawn 时 `SysProcAttr{Setpgid:true}` 让主进程成组长、再 `kill(-pid)` 杀
  整组（见 `proc_tree_kill_{windows,other}.go`）。Stop 完整闭环：① `killPIDTree`
  杀树 + `s.cancel()` 释放 cmdCtx；② `stopRequested` 让 SendLoop 退出（不再续跑）；
  ③ `statusHook("done")` 把 `workspace.status` 从 running 翻 `needs_review`、
  `agent_status=idle`、广播 `EventIdle`/`agent_done`；④ `alive=false` 使下次
  `ensureProcess` 重新拉起进程（`--resume` 保上下文）重进 SendLoop。

---

## 3. 后台任务（in-flight tracker）

### 3.1 数据模型

`InflightTracker` per-session 维护三类 in-flight 条目（`inflight.go`）：

| Kind | 入口 | 删除时机 |
|------|------|---------|
| `BgTaskBash` | `Bash` tool with `run_in_background:true` 的 tool_use → `recordBgTaskUse` | `KillBash[shell_id]` / GC 探针 / 1h zombie 兜底 |
| `BgTaskSubagent` | `Task` tool tool_use | `Task` 的 tool_result 到达（subagent 永远在当前 turn 内完成）|
| `BgTaskWakeup` | `ScheduleWakeup` tool_use | `ScheduledFor` 过期后被 `GCStale` 清掉 |

Bash 条目里有 `BashID`（Claude CLI 返回的 opaque shell handle，例如
`b5kcffa1v`）。这个 handle **没有 PID 映射**——`KillBash` 走 handle 反查；
我们没办法直接知道某个具体 shell 对应哪个 OS 进程。

侧边栏的 bg-tasks 指示器读 `service.fillBgTaskMeta` 汇总出来的
`BgAgentBusy` + 三个 count + `BgHighlight`。

### 3.2 后台任务 与 finalize 的耦合

`finalizeSendLoopTurn` 的 clean 路径在两种情况下 `return`、不把 workspace 写成
`needs_review`（保持 running）：

1. **`autohostScheduledWait`** —— autohost 在 budget 耗尽且仍有后台任务时没有停机，
   而是安排了一条定时 resume 稍后重判（见 §5）。到点 resume 重新 SendLoop ->
   finalize，那一轮后台任务做完了才真正流转。
2. **`hasPendingFutureWakeup()`** —— 非 autohost 的历史行为：agent 排了未到期
   wakeup，先别翻 needs_review。

**只挡这两种**。特意**不挡**「裸 `Bash[bg]`/`Subagent` 且非 autohost」——那条没有
任何 resume 机制，挡了会把非 autohost 工作空间一直卡在 running 直到 1h GC（这是
一次 code review 抓到的回归，已避免）。

`isError` 路径**不延迟**，错误必须立刻置 attention 触发用户介入。

**autohost 终止流转**：`SendLoop` 用 `autohostTerminalStop = !scheduledWait &&
mode==autohost` 算出是否「真正停机」。met / `[AUTOHOST_DONE]` -> `scheduledWait`
为假 -> `autohostTerminalStop=true` -> 跳过延迟、立即流转（残留 stale wakeup 不挡）。
budget 耗尽但有后台任务 -> 安排 resume + `scheduledWait=true` -> `autohostTerminalStop`
为假 -> 延迟、保持 running。

> 已知残留（code review 记录）：① resume 是 scheduler 的一次性 timer，`trigger()`
> 在 session 仍 `StatusRunning` 时会 skip 且不重排；用 `autohostMinResumeDelay`(15s)
> 兜住「resume 早于本轮 unwind」的自竞争，并发被别的轮次顶起的少见情形靠那一轮
> autohost 重新安排 resume 自愈。② 每个 resume cycle 会先跑满 budget 次 immediate
> continue 再重排，长 build 的累计判官调用偏多（有界，待优化）。

### 3.3 GC + 进程树探针

`gcInflightLoop` 每 10s tick（之前是 10min；缩短到 10s 是为了让 wakeup
过期能在 ~10s 内反映到侧边栏计数）。每 tick 对每个 session：

1. `GCStale(now)` —— 标准超时清理（Bash 1h / Subagent 30m / Wakeup 过期）
2. **Bash 进程树探针**（`bash_probe.go`，bashCount > 0 时才跑）：
   - 取 CLI 进程 PID（`cliProcessPid()`），用 `gopsutil/v4/process` 沿后
     代树数 shell 名称的进程（`bash` / `sh` / `zsh` / `cmd.exe` /
     `powershell.exe` / `pwsh` 等，excluded 是 node/npx/python 因为它们
     大概率是 MCP 子进程或 daemonize 出来的，不在 tracker 跟踪范畴）
   - `alive == 0` → `ClearBash()` 全清（确认僵尸）
   - `0 < alive < bashCount` → `TrimBashTo(alive)` 按 "无 BashID 优先 +
     StartedAt 升序" 砍掉差量，让显示数量匹配实际 shell 数
   - `alive >= bashCount` → 不动（多出来的可能是 subagent 自启 shell；
     我们不跟踪不能误清）
3. 若清掉了任何条目，发 `bg_task` notify（下游 200ms 去抖兜底）

探针返回 -1 表示 OS 探测失败（gopsutil 报错、PID 无效等），调用方走保守
路径完全跳过 trim/clear，避免误清活 shell。Windows 上 gopsutil 走 WMI 偏
慢；用 `CountByKind` 在零 bash 时 early-exit + 16 层递归上限控制最坏开销。

**局限**：bash daemonize 出子进程后自身退出（`bash -c "python app.py &"`），
我们看到 0 shell 后代会清掉 tracker 条目。语义上 tracker 跟踪 SHELL 本身，
子进程后续运行属于 CLI bg model 之外，可接受。

---

## 4. SSE / 持久化事件流

`event.OutputEvent`（`internal/event/types.go`）是 SSE 与 DB
（`agent_messages`）共享的统一形状。字段一览：

- `Type` / `Content` / `MessageId` / `Role` / `Ts` / `WorkspaceId`：通用骨架
- `ToolName` / `ToolInput` / `ToolUseId` / `IsError`：tool_use / tool_result
- `CostUsd` / `NumTurns` / `DurationMs` / `InputTokens` / `OutputTokens`：done 事件
- `Attachments`（**2026-05 新增**）：序列化的 `[]ChatAttachment` JSON。用户
  消息走 queue 路径时，chat-input 的乐观气泡被 queue 确认后移除；后续
  dequeue 触发的 `Send()` 广播 user echo 此前只带 content 不带 attachments，
  前端 fallback 分支按 content-only 创建新 event，导致 `[附件: …]` 标记原样
  显示。echo 里塞 attachments 后，前端 fallback 直接挂到
  `TimelineEvent.attachments` 渲染预览。直发路径（session 空闲）走乐观气泡
  reconcile 不受影响。
- `RateLimit` / `TaskData` / `HarnessConfirm` / `HarnessStatus` /
  `AgentDispatched` / `AgentCompleted` / `AgentActivity` / `InboxSent` /
  `PermissionRequest` / `PermissionDecided` / `AskUserRequest` /
  `AskUserDecided` / `RunPhase` / `GateJob` / `AgentLifecycle` /
  `GateProgress`：各种 payload sidecar

DB 行通过 `persistEvent` / `persistAndBroadcast` 写入，`MessageId` 关联同一
turn 的多事件、`agentMessages.harness_run_id` 关联当前 harness 运行。

---

## 5. 与 autohost 的接口

`autohost.go` 不再做 LLM-as-judge（判官已于 2026-06-10 下线，见
`docs/superpowers/specs/2026-06-10-autohost-remove-llm-judge-design.md`）。续跑/
恢复纯粹基于 **`[AUTOHOST_DONE]` sentinel + continue budget**：
- `autohostMaybeContinue(ctx)` —— 在 SendLoop 每轮 dequeue 空之后调一次：本轮输出
  出现 `[AUTOHOST_DONE]` → 停机流转；否则在 `autohostBudget` 未耗尽时续跑一轮。
- `autohostMaybeRecover(ctx)` —— 在 SendLoop 看到 lastTurnError 后调一次：在
  `autohostErrorBudget` 未耗尽时自动重试。

`autohostChainID` 把同一 SendLoop 内的连续续跑挂同一 chain；
`autohostBudget` / `autohostErrorBudget` 控制无限循环上限。**没有
`autohost_judge_events` 表、没有 LLM 抽样比例。** 完成条件不再由判官打分，而是把
per-issue/workspace 的判停条件（`issue.goal_condition` →
`NIUNIU_AUTOHOST_GOAL_CONDITION`）**注入到 watchdog 的续跑/恢复 prompt**，让 agent
自行判断是否达成、达成时主动输出 `[AUTOHOST_DONE]`。条件为空时就只靠 sentinel +
budget。

**后台任务 → 暂停续跑、改为定时重判（不停机、不流转）**：`autohostMaybeContinue`
在 budget 耗尽准备停机前，先查 `hasPendingBgWork()`（`Bash[bg]` / `Subagent` /
未到期 `Wakeup`）。有在跑的后台任务说明任务没真做完，budget 是用来兜「agent 空转」
的、不该把在跑的构建/子任务丢掉。

但**不能直接 immediate-continue**——agent 若每轮重排一个 wakeup，`hasPendingBgWork`
永远为真，budget 被彻底绕过 → 无限续跑、烧 token。所以改为**定时重判**：调
`scheduleBgWaitResume` → `RateLimitScheduler.OnAutohostWait` 建一条 once 定时任务
（时间取 agent 最近的未到期 wakeup，没有就 `autohostBgPollInterval=90s`），消息为
续跑提示；然后 `return false` **停掉当前 loop**，并置 `autohostScheduledWait=true`
让 §3.2 的 finalize 保持 running 不流转。到点 scheduler 重新 SendLoop → 重新评估：
后台任务清掉了就按正常 budget 闸门走，没清掉就再排一次。发 `⏩ 已安排稍后重新检查`
的非持久 ping。没有 scheduler（测试 / 旧装机）时回退到 `budget_exhausted` 停机，
保证有界。这样有后台任务时：**既不停机（定时重判直到完成）、也不把状态翻成
needs_review、更不会无限烧 token（节奏由 wakeup / 90s 决定）**。

详细 spec：`docs/superpowers/specs/2026-06-10-autohost-remove-llm-judge-design.md`
（下线判官）与 `docs/superpowers/specs/2026-05-13-auto-host-mode-design.md`。

---

## 6. 阅读入口索引

| 文件 | 内容 |
|------|------|
| `proxy.go` | WorkspaceSession 状态机、Send/SendLoop、handleEvent/handleStreamEvent、GC loop |
| `inflight.go` | InflightTracker + BgTask 模型 + ClearBash/TrimBashTo/GCStale |
| `bash_probe.go` | gopsutil 进程树探针 + shell 名称匹配 |
| `inflight_parse.go` | bash bg / subagent / wakeup 的 tool input/result 文本解析 |
| `codex_appserver*.go` / `codex_appserver_client*.go` | Codex 子进程驱动 |
| `codex_parser.go` + `adapter/codex.go` | Codex 事件 → 通用形状归一 |
| `adapter/{adapter,claude,codex,spawn}.go` | CLI 适配层 + spawn helper |
| `cli_probe.go` | claude/codex CLI 探活 |
| `autohost*.go` | sentinel + budget 续跑 / 恢复决策（无 LLM 判官） |
| `auto_compact.go` | 上下文占用越过预算阈值时自动注入真正的 `/compact`（Claude-only，suppress/re-arm 防抖）。设计见 `docs/superpowers/specs/2026-06-17-auto-context-compaction-design.md` |
| `parser.go` | Claude JSONL 行解析 shim |
