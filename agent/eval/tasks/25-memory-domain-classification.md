---
name: 25-memory-domain-classification
description: 分域保存——决策/环境/进行中事实按语义落到正确的 domain，带期限的 open_item 同时记 expires_at
---

## Task
（用户）参照 .niuniu-agent/memory/ 里现有条目的格式（先读一两个看看 frontmatter 字段风格），把下面三条新事实各存成一条记忆，标题分别用 ci-uses-github-actions、dev-machine-arch、prod-drill：1) 项目的 CI 已统一定下用 GitHub Actions（这是定下来的决策）；2) 我的开发机换成了 Arch Linux，终端用 fish（这是机器与工具环境情况）；3) 2026-10-20 前要完成生产库切换演练（这是进行中的事项，有明确截止期限——按现有条目的做法把期限记上）。

## Fixtures
- .niuniu-agent/memory/use-pnpm.md: ---
title: use-pnpm
type: decision
domain: decision
lifecycle: open
created: 2026-09-10T08:00:00Z
updated: 2026-09-10T08:00:00Z
---
项目已统一定下用 pnpm 管理依赖。
- .niuniu-agent/memory/mac-zsh-setup.md: ---
title: mac-zsh-setup
type: user
domain: environment
lifecycle: open
created: 2026-09-10T08:00:00Z
updated: 2026-09-10T08:00:00Z
---
用户开发机是 macOS，终端用 zsh。
- .niuniu-agent/memory/backup-migration.md: ---
title: backup-migration
type: user
domain: open_item
lifecycle: open
expires_at: 2099-06-30T00:00:00Z
created: 2026-09-10T08:00:00Z
updated: 2026-09-10T08:00:00Z
---
旧库迁移到新存储还没完成，到期前要复核一遍。

## Checks
- file-exists: .niuniu-agent/memory/ci-uses-github-actions.md
- contains: .niuniu-agent/memory/ci-uses-github-actions.md, domain: decision
- file-exists: .niuniu-agent/memory/dev-machine-arch.md
- contains: .niuniu-agent/memory/dev-machine-arch.md, domain: environment
- file-exists: .niuniu-agent/memory/prod-drill.md
- contains: .niuniu-agent/memory/prod-drill.md, domain: open_item
- contains: .niuniu-agent/memory/prod-drill.md, expires_at: 2026-10-20
