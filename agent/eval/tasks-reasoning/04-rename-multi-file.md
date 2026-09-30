---
name: reasoning-rename-multi-file
description: Multi-file consistency — rename a function across definition, call site and test without leaving stragglers
---
## Task
Rename the function `calc_total` to `compute_sum` EVERYWHERE in this project (definition, call sites, tests). Keep behavior identical. No reference to `calc_total` may remain in any file.

## Fixtures
- helpers.py: def calc_total(items):\n    return sum(items)
- main.py: from helpers import calc_total\n\nprint(calc_total([1, 2, 3]))
- tests/test_calc.py: from helpers import calc_total\n\ndef test_basic():\n    assert calc_total([1, 2]) == 3

## Checks
- contains: helpers.py, def compute_sum(items)
- not-contains: helpers.py, calc_total
- not-contains: main.py, calc_total
- not-contains: tests/test_calc.py, calc_total
- contains: main.py, compute_sum([1, 2, 3])
