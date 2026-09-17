---
name: 13-fix-indent
description: 统一代码缩进风格
---

## Task
indent.py 当前用 4 空格缩进，项目规范要求 2 空格。把文件里所有缩进改为 2 空格（每级 4 空格改 2 空格），逻辑与换行保持不变。

## Fixtures
- indent.py: def run():\n    if True:\n        return deep(1)\n    return 0\n

## Checks
- not-contains: indent.py,     if True:
- contains: indent.py,   if True:
- contains: indent.py,     return deep(1)
- contains: indent.py,   return 0
