---
name: 23-memory-correction-behavior-sync
description: 当轮纠错行为同步——同一轮内完成纠错协议（旧条目 deprecated + 新条目）且产物不再沿用旧偏好（config.json 改新值且无旧值残留）
---

## Task
（用户）改主意了，记住了：我以后工具一律用浅色（light）主题，不再用深色（dark）。请现在就按纠错流程更新记忆，不要拖到会话结束：把旧的偏好条目 .niuniu-agent/memory/theme-preference.md 标记成 lifecycle: deprecated（保留原有内容），再新建一条记忆，标题用 theme-light-preference，记录纠正后的偏好；同时把 config.json 的 theme 值改成 "light"。

## Fixtures
- .niuniu-agent/memory/theme-preference.md: ---
title: theme-preference
type: user
domain: preference
lifecycle: open
created: 2026-10-01T08:00:00Z
updated: 2026-10-01T08:00:00Z
---
Durable preference: the user prefers the dark theme for all his tools.
- config.json: {"theme": "dark", "lang": "zh"}

## Checks
- contains: .niuniu-agent/memory/theme-preference.md, lifecycle: deprecated
- contains: .niuniu-agent/memory/theme-preference.md, dark theme
- file-exists: .niuniu-agent/memory/theme-light-preference.md
- contains: .niuniu-agent/memory/theme-light-preference.md, light theme
- contains: config.json, "light"
- not-contains: config.json, dark
