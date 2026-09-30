---
name: reasoning-long-range-constraint
description: Long-range constraint — an output-format rule given up front must hold across every produced file
---
## Task
Create three files a.txt, b.txt and c.txt. Each contains the first five prime numbers, one per line: 2, 3, 5, 7, 11 (in that order). Every file MUST end with exactly one trailing newline — this applies to all three files, no exceptions. Then create index.json containing exactly: {"a.txt": 5, "b.txt": 5, "c.txt": 5}

## Fixtures
- keep.txt: placeholder

## Checks
- contains: a.txt, 2
3
5
7
11
- contains: b.txt, 2
3
5
7
11
- contains: c.txt, 2
3
5
7
11
- file-exists: index.json
- contains: index.json, "a.txt": 5
