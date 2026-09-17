---
name: 01-fix-typo
description: 修复文本中的拼写错误
---

## Task
文件 notes.txt 中有拼写错误：把 'recieve' 全部改为 'receive'，直接改文件。

## Fixtures
- notes.txt: please recieve my apologies
we will recieve the package soon

## Checks
- contains: notes.txt, receive
- not-contains: notes.txt, recieve
