---
name: reasoning-messy-log-pipeline
description: Dirty-data pipeline — filter, dedupe and order-preserve under noise
---
## Task
raw.log contains log lines, junk lines and duplicates. Extract the `msg=` payloads of all lines whose level is ERROR, deduplicate them (keep the FIRST occurrence's order), and write them one per line to errors.txt. Nothing else may appear in errors.txt — no level prefixes, no INFO/DEBUG messages.

## Fixtures
- raw.log: level=INFO msg=started\nlevel=ERROR msg=disk full\njunk line without fields\nlevel=DEBUG msg=cache warm\nlevel=ERROR msg=disk full\nlevel=ERROR msg=timeout upstream\nnoise\nlevel=INFO msg=retrying\nlevel=ERROR msg=timeout upstream\nlevel=WARN msg=slow

## Checks
- file-exists: errors.txt
- contains: errors.txt, disk full
- contains: errors.txt, timeout upstream
- not-contains: errors.txt, level=
- not-contains: errors.txt, cache warm
