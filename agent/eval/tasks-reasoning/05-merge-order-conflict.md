---
name: reasoning-merge-order-conflict
description: Conflict resolution — two configs disagree; deploy docs define which one wins
---
## Task
settings.json and settings.override.json both configure the same service. According to deploy.md, the override file is merged ON TOP of the base file (override keys win, base-only keys survive). Write the EFFECTIVE merged configuration to effective.json as a flat JSON object with the final values of every key.

## Fixtures
- deploy.md: # Deploy\n\nThe loader merges settings.json first, then settings.override.json on top of it. Later keys overwrite earlier ones; keys only present in the base file are kept as-is.
- settings.json: {"retries": 3, "timeout": 30, "verbose": false}
- settings.override.json: {"retries": 5, "verbose": true}

## Checks
- file-exists: effective.json
- contains: effective.json, "retries": 5
- contains: effective.json, "timeout": 30
- contains: effective.json, "verbose": true
