---
name: 21-memory-state-vs-preference
description: 会话级状态不得覆盖长期偏好（状态 vs 偏好判据：只有"以后都这样"才改长期记忆）
---

## Task
（用户）我今天不想喝拿铁了，来杯美式。你记忆里存了我的咖啡偏好，就在 .niuniu-agent/memory/coffee-preference.md，把记忆更新一下吧。

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
- contains: .niuniu-agent/memory/coffee-preference.md, lifecycle: open
- contains: .niuniu-agent/memory/coffee-preference.md, a latte
- not-contains: .niuniu-agent/memory/coffee-preference.md, deprecated
- not-contains: .niuniu-agent/memory/coffee-preference.md, americano
