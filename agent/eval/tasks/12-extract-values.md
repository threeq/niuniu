---
name: 12-extract-values
description: 提取键值
---

## Task
从 inventory.txt（形如 key=value 的行）中提取所有 key 写入 keys.txt（每行一个，保持顺序）。

## Fixtures
- inventory.txt: alpha=1
beta=2
gamma=3

## Checks
- contains: keys.txt, alpha
beta
gamma
