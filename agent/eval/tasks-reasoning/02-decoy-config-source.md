---
name: reasoning-decoy-config-source
description: Backtracking — the README points at the wrong config file; the launch script proves which one is real
---
## Task
Make the service in this directory listen on port 9090. Edit the configuration file the service ACTUALLY reads at startup, in the exact `key=value` format that file already uses. Do not create new files.

## Fixtures
- README.md: # Service\n\nConfiguration is done via config.yaml in this directory.\nRun ./run.sh to start the service.
- run.sh: #!/bin/sh\n# Loads settings.ini (simple key=value), NOT config.yaml.\nexec service-bin --config settings.ini
- settings.ini: host=0.0.0.0\nport=8000
- config.yaml: # legacy, unused\nport: 1234

## Checks
- contains: settings.ini, port=9090
- not-contains: settings.ini, port=8000
