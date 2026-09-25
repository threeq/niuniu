---
name: 03-sort-lines
description: 对文件行排序
---

## Task
把 items.txt 的行按字母排序后写回 items.txt（覆盖原文件）。

## Fixtures
- items.txt: cherry
apple
banana

## Checks
- contains: items.txt, apple
banana
cherry
