---
name: 05-fix-json
description: 修复损坏的 JSON
---

## Task
config.json 是损坏的 JSON（缺少引号），修复成合法 JSON：把 fruit 的值 apple 加上双引号。

## Fixtures
- config.json: {
  "count": 3,
  "fruit": apple
}


## Checks
- contains: config.json, "apple"
