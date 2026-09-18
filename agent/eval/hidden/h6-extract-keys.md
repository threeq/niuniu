---
name: h6-extract-keys
description: 提取配置键
---

## Task
settings.ini 中每行形如 key = value。提取所有 key 写入 keys.txt（每行一个，保持顺序）。

## Fixtures
- settings.ini: host = local
port = 80

## Checks
- contains: keys.txt, host
port
