---
name: h1-csv-col-sum
description: CSV 列求和
---

## Task
data.csv 每行形如 name,value。把第二列所有数值求和，结果写入 sum.txt（只要数字）。

## Fixtures
- data.csv: a,10
b,20
c,12

## Checks
- contains: sum.txt, 42
