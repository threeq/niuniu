---
name: h2-fix-syntax
description: 修复 Python 语法错误
---

## Task
tool.py 的 run 函数定义行缺少冒号，修复它使文件语法正确（保持其余内容不变）。

## Fixtures
- tool.py: def run()
    return 42


## Checks
- not-contains: tool.py, def run()

- contains: tool.py, def run():
