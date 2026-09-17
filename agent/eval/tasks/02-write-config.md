---
name: 02-write-config
description: 创建 JSON 配置文件
---

## Task
创建文件 config.json，内容为 JSON：键 name 值 demo，键 version 值 1.0。用 Write 工具直接写出。

## Fixtures
（无）

## Checks
- contains: config.json, "demo"
- file-exists: config.json
