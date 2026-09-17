---
name: 09-todo-complete
description: 更新任务清单文件
---

## Task
todos.txt 每行形如 [ ] 任务名。把其中包含 '写报告' 的行改为 [x] 开头（已完成），其余保持。

## Fixtures
- todos.txt: [ ] 写报告
[ ] 买牛奶
[ ] 复习代码

## Checks
- contains: todos.txt, [x] 写报告
- contains: todos.txt, [ ] 买牛奶
