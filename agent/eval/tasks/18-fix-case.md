---
name: 18-fix-case
description: 统一命名风格
---

## Task
helpers.go 中函数名 GetData 参照其余文件改为 snake_case 的 get_data（两处：定义处与文件尾调用处），其余不动。

## Fixtures
- helpers.go: package helpers

func GetData() string {
	return "d"
}

var x = GetData()


## Checks
- contains: helpers.go, get_data
- not-contains: helpers.go, GetData
