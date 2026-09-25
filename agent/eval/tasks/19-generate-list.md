---
name: 19-generate-list
description: 生成编号列表
---

## Task
创建 plan.md：三行，分别为 '1. 调研'、'2. 实现'、'3. 验证'（每行一行）。

## Fixtures
（无）

## Checks
- contains: plan.md, 1. 调研
- contains: plan.md, 2. 实现
- contains: plan.md, 3. 验证
