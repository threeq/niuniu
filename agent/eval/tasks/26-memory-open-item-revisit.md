---
name: 26-memory-open-item-revisit
description: open_item 主动回访——未过期 open_item 以 [open, due …] 状态标注注入召回，agent 能主动提出跟进该事项
---

## Task
（用户）我记性不好——你开场记忆里带着的待办提醒里，有什么值得这阵子跟进的事？不要去读项目文件，凭注入的记忆作答，把要跟进的事写进 followup.md。

## Fixtures
- .niuniu-agent/memory/readme-translation.md: ---
title: readme-translation
type: user
domain: open_item
lifecycle: open
expires_at: 2099-12-31T00:00:00Z
created: 2026-09-20T09:00:00Z
updated: 2026-09-20T09:00:00Z
---
答应帮朋友把他 CLI 工具的 README 翻译成英文，还没动工。

## Checks
- file-exists: followup.md
- contains: followup.md, README
