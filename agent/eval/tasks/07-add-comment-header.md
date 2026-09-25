---
name: 07-add-comment-header
description: 给文件加文件头注释
---

## Task
在 util.go 第一行加上注释 // Package utils 提供工具函数（保留原内容）。

## Fixtures
- util.go: package utils

func Help() {}


## Checks
- contains: util.go, // Package utils 提供工具函数
- contains: util.go, func Help()
