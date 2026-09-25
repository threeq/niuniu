---
name: 04-dedupe
description: 去除重复行
---

## Task
把 data.txt 中的重复行去掉（保留首次出现顺序），结果写回 data.txt。

## Fixtures
- data.txt: go
python
go
rust
python

## Checks
- contains: data.txt, go
python
rust
- not-contains: data.txt, go
python
go
