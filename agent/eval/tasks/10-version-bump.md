---
name: 10-version-bump
description: 升级版本号
---

## Task
把 version.txt 中的 1.2.3 替换为 1.3.0。

## Fixtures
- version.txt: version=1.2.3
build=42

## Checks
- contains: version.txt, 1.3.0
- not-contains: version.txt, 1.2.3
