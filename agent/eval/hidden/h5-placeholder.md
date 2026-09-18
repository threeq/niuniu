---
name: h5-placeholder
description: 替换模板占位符
---

## Task
letter.txt 中所有 {{USER}} 替换为 admin，其余原样保留。

## Fixtures
- letter.txt: Dear {{USER}}, your account {{USER}}-primary is ready.

## Checks
- contains: letter.txt, Dear admin
- not-contains: letter.txt, {{USER}}
