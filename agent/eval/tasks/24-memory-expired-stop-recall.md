---
name: 24-memory-expired-stop-recall
description: 惰性过期停引——lifecycle=open 但 expires_at 已过的条目在召回路径自动判定为 expired 停止注入；只凭注入作答时过期安排不再出现，且判定只读不回写盘
---

## Task
（用户）早上好。你开场就带着的记忆提醒里，今天有什么需要我准备的事项吗？这是对记忆注入路径的检查：不要读取任何已有文件（包括 .niuniu-agent/memory/ 记忆库本身），只凭开场注入给你的记忆作答，把结论写进 today.md（没有要准备的就写「今天没有需要准备的安排」）。

## Fixtures
- .niuniu-agent/memory/piano-tuning.md: ---
title: piano-tuning
type: user
domain: open_item
lifecycle: open
expires_at: 2026-01-05T10:00:00Z
created: 2026-01-01T09:00:00Z
updated: 2026-01-01T09:00:00Z
---
用户约了钢琴调音师周五上午上门，记得提前一天收拾客厅。

## Checks
- file-exists: today.md
- contains: today.md, 今天没有需要准备的安排
- not-contains: today.md, 调音
- contains: .niuniu-agent/memory/piano-tuning.md, lifecycle: open
