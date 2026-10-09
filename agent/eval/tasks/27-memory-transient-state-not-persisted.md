---
name: 27-memory-transient-state-not-persisted
description: 状态 vs 偏好——一次性会话状态（「这次别写测试」）不得固化为长期偏好：记忆层文件数不增（file-count 写入探测），且枚举结果仍只含原有条目
---

## Task
（用户）这轮赶时间，测试就先不写了，代码直接改完给我（就这一次，不是以后都这样）。另外顺手帮我梳理下：你长期记忆库里现在都有哪些条目？逐条查出来原样写到 memory-list.md，一条都别漏。

## Fixtures
- .niuniu-agent/memory/test-discipline.md: ---
title: test-discipline
type: user
domain: preference
lifecycle: open
created: 2026-09-15T08:00:00Z
updated: 2026-09-15T08:00:00Z
---
Durable preference: 用户要求每次改完代码都跑测试再交付。

## Checks
- contains: memory-list.md, test-discipline
- file-count: .niuniu-agent/memory, 1
