---
name: h3-json-array
description: 生成 JSON 数组
---

## Task
创建 tags.json，内容为 JSON 数组 ["red", "green", "blue"]。

## Fixtures
（无）

## Checks
- contains: tags.json, "red"
- contains: tags.json, "blue"
