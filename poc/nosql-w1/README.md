# NoSQL W1 driver-connectivity PoC

Epic #345 / Wave1（issue #352）的驱动连通性验证：MongoDB / Redis / Elasticsearch / Trino
四种纯 Go 驱动，各跑一次 Ping + 一条只读操作。结论纪要见
`docs/superpowers/specs/2026-06-10-nosql-w1-poc-conclusions.md`。

本目录是**独立 go module 且刻意不进 go.work**——PoC 依赖（驱动及其间接依赖）在 Wave2/3
正式落地前不进入 server 模块。所有 go 命令需 `GOWORK=off`。

```sh
docker compose up -d          # mongo / redis / elasticsearch / trino
GOWORK=off go run .           # 全部四项
GOWORK=off go run . redis es  # 只跑指定项
```

端点可用 `POC_MONGO_URI` / `POC_REDIS_ADDR` / `POC_ES_URL` / `POC_TRINO_DSN` 覆盖。
Trino 启动约需 30–60s（用内置 tpch catalog，无需建数据）。
