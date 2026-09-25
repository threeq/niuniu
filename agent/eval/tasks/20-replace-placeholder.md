---
name: 20-replace-placeholder
description: 替换模板占位符
---

## Task
template.txt 中所有 {{NAME}} 占位符替换为 niuniu，其余原样保留，写回 template.txt。

## Fixtures
- template.txt: Hello {{NAME}}, welcome to {{NAME}} platform.

## Checks
- contains: template.txt, Hello niuniu, welcome to niuniu platform.
- not-contains: template.txt, {{NAME}}
