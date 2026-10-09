---
name: 22-memory-same-turn-correction
description: 当轮纠错协议——用户纠正后同一轮完成旧记忆 deprecated 打标 + 新记忆写入，不等异步 consolidate
---

## Task
（用户）记住了：我以后都不喝拿铁了，只喝美式。请现在就按纠错流程更新记忆，不要拖到会话结束：把旧的咖啡偏好条目（.niuniu-agent/memory/coffee-preference.md）标记成 lifecycle: deprecated（保留原有内容），再新建一条记忆，标题用 americano-preference，记录纠正后的偏好。

## Fixtures
- .niuniu-agent/memory/coffee-preference.md: ---
title: coffee-preference
type: user
domain: preference
lifecycle: open
created: 2026-10-01T08:00:00Z
updated: 2026-10-01T08:00:00Z
---
Durable preference: the user's usual coffee order is a latte.

## Checks
- contains: .niuniu-agent/memory/coffee-preference.md, lifecycle: deprecated
- contains: .niuniu-agent/memory/coffee-preference.md, a latte
- file-exists: .niuniu-agent/memory/americano-preference.md
- contains: .niuniu-agent/memory/americano-preference.md, americano
