---
name: 13-fix-indent
description: 修复缩进
---

## Task
indent.py 用 2 空格缩进但混入了 tab，把所有 tab 缩进替换为两个空格（其余内容不变）。

## Fixtures
- indent.py: def run():
	return 1
	if True:
		return 2


## Checks
- not-contains: indent.py, 	
- contains: indent.py, def run()
