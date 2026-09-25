---
name: 17-split-ini
description: INI 转 JSON
---

## Task
把 app.ini（[server] 段 port=8080）转换为 config.json，保留 port 的值为数字 8080。

## Fixtures
- app.ini: [server]
port = 8080
host = 0.0.0.0


## Checks
- contains: config.json, 8080
- file-exists: config.json
