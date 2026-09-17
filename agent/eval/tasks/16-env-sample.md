---
name: 16-env-sample
description: 生成环境变量样例
---

## Task
根据 config.json 的键生成 .env.example：每行 KEY_NAME=<VALUE>（键转大写）。config.json 有键 db_host 值 localhost。

## Fixtures
- config.json: {
  "db_host": "localhost",
  "retries": 3
}


## Checks
- contains: .env.example, DB_HOST=localhost
- contains: .env.example, RETRIES=3
