---
name: 06-count-numbers
description: 统计并写入结果
---

## Task
统计 numbers.txt 中有多少行（每行一个数字），把行数写入 result.txt（只要数字）。

## Fixtures
- numbers.txt: 1
2
3
4
5

## Checks
- contains: result.txt, 5
