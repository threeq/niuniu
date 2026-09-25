---
name: 08-rename-key
description: 重命名 JSON 键
---

## Task
把 settings.json 里的键 "colour" 重命名为 "color"（值保持 blue），其余不变。

## Fixtures
- settings.json: {
  "theme": "dark",
  "colour": "blue"
}


## Checks
- contains: settings.json, "color"
- not-contains: settings.json, colour
