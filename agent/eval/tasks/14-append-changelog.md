---
name: 14-append-changelog
description: 追加变更记录
---

## Task
在 CHANGELOG.md 顶部（# Changelog 标题行之后）追加一行 '- 2026-09-18: add eval harness'。

## Fixtures
- CHANGELOG.md: # Changelog

- 2026-09-01: initial release


## Checks
- contains: CHANGELOG.md, add eval harness
- contains: CHANGELOG.md, initial release
