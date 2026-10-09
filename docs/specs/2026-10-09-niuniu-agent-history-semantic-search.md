# 设计：HistorySearch 历史归档的向量语义检索

> 状态：**待评审**（2026-10-09）
> 范围：niuniu-agent（`agent/` 模块）；零第三方依赖约束不变
> 关联：竞品调研结论（`niuniu-enterprise-main/竞品分析/05`）、Epic #720（P4 记忆升级，本文不依赖其产出，但共享"可选 provider"模式）

---

## 1. 背景与动机

### 1.1 现状

compact 把被逐出的消息逐条归档为 JSON chunk（`tools/history_search.go`）：

- 归档：`SaveHistoryArchive(dir, msgs)` — 每条消息一个文件 `<unix-ts>-<seq>.json`，内容 `HistoryChunk{At, Role, Text}`，单条 4KB 截断
- 检索：`SearchHistoryArchive(dir, query, topN)` — 关键词小写计数打分，扫描上限 4000 chunks，返回 top-N（400 字节摘录）
- 装配：`sessionRegistry`（headless 与 ACP 共用，`cmd/niuniu-agent/main.go:103`）注册 `tools.HistorySearch{Dir: …}`

### 1.2 痛点

1. **同义改写全部 miss**：归档含 "retry with exponential backoff"，查询 "重试策略怎么做的" → 零命中
2. **CJK 检索退化为精确子串**：`searchTerms` 把连续非 ASCII 段落当一个 term，中文查询 ≈ 全文精确匹配
3. **扫描上限下的排序噪声**：关键词计数无法区分"提到该词"与"讨论该主题"

竞品结论（2026-10 调研）：embedding 检索是品类标配（栖语成本仅"几块到十几块"），但 niuniu-agent 的记忆库已走结构化分域路线（Epic #720）**不需要** embedding；归档是唯一"量大 + 转述多 + 无结构"的语料，是语义检索的第一个正确落点。

### 1.3 现状勘误（本设计顺带修复）

**Bug**：`sessionRegistry` 注册 `HistorySearch{Dir: <cwd>/.niuniu-agent/history}`（main.go:106），而 compact 归档写入 `tools.HistoryDir(cwd)` = `~/.niuniu-agent/projects/<escaped-cwd>/history`（main.go:345 / acp/server.go:333，statepath.go:57）。常规环境（HOME 可用）下两者不一致，**工具检索的是空目录**（仅当 HOME 不可用时 StateDir 回退才重合）。`loop.go:96` 的注释（"Usually <cwd>/.niuniu-agent/history"）同为旧地址。
**修复**：装配点统一改为 `tools.HistoryDir(cwd)`，并更新注释。归档是私有状态（statepath.go 注释的分层原则），应放 home 侧。
**状态更新（2026-10-09）**：本 bug 已在设计评审前单独修复并合入回归测试（`cmd/niuniu-agent/main_test.go` TestHistorySearchWiredToArchiveDir）；D8 关闭，不再进入 P1 范围。

## 2. 目标 / 非目标

**目标**

- G1 语义命中：改写、中英混合、口语化查询能召回归档内容
- G2 默认零变化：未配置 embedding 时行为与现状逐字节一致（关键词检索、零网络、零额外文件）
- G3 零依赖单二进制：不引入第三方库、不内嵌模型、不加 CGO
- G4 local-first：向量全部落本地；文本外发仅发生在用户显式配置 provider 之后
- G5 归档即索引：compact 落盘时同步建索引，无后台进程、无独立服务

**非目标**

- 不做本地 ONNX/模型推理（体积与跨平台成本）
- 不做 ANN 索引（语料 ≤4000 chunks，暴力余弦 <10ms）
- 不覆盖 memory 条目检索与 niuniu 服务端 kb_search（`internal/embed` 包为它们预留复用，但本期不接）
- 不承诺离线语义检索（离线 = 自动回退关键词，属 G2）

## 3. 总体方案

```
                    ┌──────────────────────────────┐
 compact ──► SaveHistoryArchive ──► *.json（原文，不变）      ──► 无 .vec 的 chunk 只参与关键词
                    │                                             ▲
                    └─► embed.Client（可选，nil=跳过）             │
                         └─► 批量 POST /v1/embeddings             │
                              └─► *.json.vec（sidecar，本地） ─────┘

 HistorySearch.Execute
   ──► query ──► embed.Client ──► qvec        （失败/未配置 → 纯关键词）
   ──► 扫描目录：关键词分（现有逻辑）+ 余弦分（.vec 有效时）
   ──► RRF 融合两份排名 ──► top-N，结果标注 via semantic/keyword/both
```

核心原则：**embedding 是增量增强，不是依赖**。任何一层失败都退化为现状。

## 4. 详细设计

### 4.1 embed provider 抽象（新包 `internal/embed`）

```go
// Client 将文本批量转为向量。实现必须可并发安全。
type Client interface {
    Embed(ctx context.Context, texts []string) ([][]float32, error)
    Model() string // 模型标识，写入 .vec header 用于失效判定
}

// FromEnv 按 env 装配；未配置返回 (nil, nil)——这是正常路径不是错误。
// 配置了但不完整（缺 key）时 stderr 打一次性指引并返回 (nil, nil)，
// 对齐 WebSearch "未配置报指引" 的既有模式。
func FromEnv() Client
```

**唯一实现：OpenAI 兼容 `/v1/embeddings`**。该线格式被 OpenAI / 智谱 / SiliconFlow / Jina / Ollama(`/v1/embeddings`) / LM Studio 等广泛兼容，一个实现覆盖绝大多数 provider。Anthropic 无 embeddings API，不做。

- `POST {BASE_URL}/embeddings`，body `{"model": M, "input": [t1, t2, ...]}`，批量上限 64 条/请求，超出分批
- 维度不预配置：从首个响应的向量长度自动学习（`len(embedding)`），与 `.vec` header 记录的 dim 严格校验
- 超时 30s；429/5xx 退避重试 1 次（1s），仍失败按失败处理
- 零依赖：`net/http` + `encoding/json`

**env 配置**（对齐 `NIUNIU_AGENT_*` 命名）：

| 变量 | 语义 |
|---|---|
| `NIUNIU_AGENT_EMBEDDING` | 未设或 `off` = 关闭（默认）；`<model-id>` = 启用；`fake` = 内置确定性测试 provider（见 §7.3） |
| `NIUNIU_AGENT_EMBEDDING_BASE_URL` | 默认 `https://api.openai.com/v1` |
| `NIUNIU_AGENT_EMBEDDING_API_KEY` | Bearer 凭证；启用但未设 → 打指引后按关闭处理 |

### 4.2 向量存储：sidecar 文件（推荐方案）

每个归档 chunk 旁放一个 `.vec` 文件：`<unix-ts>-<seq>.json` → `<unix-ts>-<seq>.json.vec`。

```
offset  size  字段
0       4     magic "NVA1"
4       4     dim        (uint32 LE)
8       8     modelHash  (uint64 LE, FNV-1a(Client.Model()))
16      8     textHash   (uint64 LE, SHA-256(chunk.Text) 前 8 字节)
24      4*dim data       (float32 LE)
```

**选 sidecar 而非集中式索引文件的理由**：与现有"一 chunk 一文件"模型一致；增量写、按条失效、损坏容错都是文件级操作，无对账逻辑；删除归档文件后残留 .vec 在扫描时因找不到同名 .json 而自然跳过。

**有效性判定**（查询时逐文件校验）：magic 对 + dim 与本次 qvec 相等 + modelHash 与当前 Client 一致 + textHash 与 .json 内容一致。任一不符 → 该文件忽略（视为"无向量"，仍参与关键词）。**换 embedding 模型 = 旧向量自动全部失效**，无需迁移。

**容量**：1024 维 float32 ≈ 4KB/chunk；1 万 chunks ≈ 40MB。可接受；int8 量化（→10MB）列为 P3 可选。

### 4.3 索引时机

- **增量（主路径）**：`SaveHistoryArchive` 增加 embedder 参数（nil = 现状）。写完所有 .json 后，若 embedder 非 nil：收集本批文本 → 批量 embed（64/批）→ 逐条写 .vec。**同步执行**（compact 本身已是 LLM 调用，量级不变）；失败仅 `slog.Warn`，.json 已落地、该条无 .vec——与现有 best-effort 哲学一致
- **存量回填**：新 CLI 子命令 `niuniu-agent index-history`：遍历 `tools.HistoryDir(cwd)`，为缺失/失效的 .vec 补建，打印统计（total/embedded/skipped/failed）。查询路径**不做**惰性回填（保持查询快速且离线可用）

### 4.4 查询路径：混合检索 + RRF 融合

`HistorySearch.Execute` / `SearchHistoryArchive` 改造：

1. qvec：`Embed([query])`；client 为 nil 或调用失败 → qvec = nil（纯关键词，结果尾部附一行 `note: semantic search unavailable` 仅在"曾配置但本次失败"时出现）
2. 单遍扫描目录（维持 4000 chunks 上限与现状一致），对每个 chunk 计算：
   - `kwScore`：现有关键词计数逻辑，不动
   - `cosScore`：qvec 与有效 .vec 的余弦；无有效 .vec → 不入向量榜
3. 两个排名表：关键词榜（kwScore>0 按 kwScore 降序）、语义榜（按 cosScore 降序），各取前 50
4. **RRF 融合**：`score(c) = Σ_lists 1/(60 + rank_in_list)`，取 top-N
5. 输出格式向后兼容，仅追加来源标注：`[file] (role) excerpt  ·via semantic|keyword|both`

RRF 而非加权归一的理由：无需跨模型调分数尺度，实现 ~15 行，检索文献标准做法。

### 4.5 装配点（单点注入）

- `sessionRegistry`（main.go）与 loop Options 是仅有的两个接触面：
  - `sessionRegistry`：`emb := embed.FromEnv()`；`reg.Register(tools.HistorySearch{Dir: tools.HistoryDir(cwd), Embed: emb})`（**同时修复 §1.3 的目录 bug**）
  - `loop.Options` 新增 `Embedder embed.Client` 字段；main.go:345 与 acp/server.go:333 的 Options 构造处传入同一 client
  - `loop.Options.HistoryDir` 与工具 Dir 从此同源，杜绝再次分叉
- 子 agent（`newChild`）不注册 HistorySearch（维持现状——子 agent 上下文隔离，无需回注父历史）

### 4.6 隐私

- 默认关闭，零网络零新文件（G2/G4）
- 启用 = 用户显式设置 env = 明示同意将**归档消息文本**发送至 `EMBEDDING_BASE_URL`；README 明示该数据流
- niuniu server 侧经 workspace env（Settings → Environment Variables / Providers 绑定）注入这三个变量，不落 `.mcp.json` 等项目内文件
- `.vec` 仅含向量与哈希，不含原文——泄露面不扩大

## 5. 失败与降级矩阵

| 场景 | 行为 |
|---|---|
| 未配置 `NIUNIU_AGENT_EMBEDDING` | 现状逐字节一致（纯关键词、无网络） |
| 配置了但缺 API key | stderr 一次性指引 → 按关闭处理 |
| 查询时 embed 网络/超时/429 | 纯关键词回退 + `note: semantic search unavailable` |
| 索引时 embed 失败 | .json 照常落地，该 chunk 无 .vec（仅关键词）；slog.Warn |
| 换 embedding 模型/维度 | 旧 .vec 因 modelHash/dim 不符自动忽略；`index-history` 重建 |
| .vec 损坏/半写（崩溃） | header 校验失败 → 忽略；下次回填覆盖 |
| 归档目录被删 | 干净 miss（现状语义），索引随目录一起消失 |

## 6. 性能与容量预算

| 项 | 预算 |
|---|---|
| compact 索引延迟 | 1 个批量 RTT（≤64 条/批），典型 <1s；失败不阻塞 |
| 查询延迟 | 1 次 embed RTT（100–500ms，主导项）+ 本地扫描 4000×1024 维余弦 <10ms |
| 磁盘 | +4KB/chunk（1024 维）；1 万 chunks ≈ 40MB |
| 内存 | 查询期流式逐文件处理，无常驻向量表（进程内缓存列为 P3 可选） |

## 7. 测试与验收

### 7.1 单测（`internal/embed`、`internal/tools`）

- HTTP client：`httptest` 假 /v1/embeddings（成功/429 重试/超时/维度变化/畸形响应）
- sidecar：round-trip、magic/dim/modelHash/textHash 各字段失效判定、半写文件容错
- 混合检索：**可控向量 fake client**（按关键词映射正交维度构造向量）验证——纯语义命中（零关键词重叠）、RRF 融合序、both 标注、client=nil 回退、embed 失败回退
- 目录 bug 回归：`HistorySearch.Dir` 与 `tools.HistoryDir(cwd)` 一致性断言

### 7.2 eval（`agent/eval/tasks`）

新增 3 个用例（runner 判定原语不变）：
- `history-semantic-plumbing`：`NIUNIU_AGENT_EMBEDDING=fake` 下，无关键词重叠的归档内容可被语义路径召回（contains 判定）
- `history-fallback-off`：embedding 关闭时行为与 baseline 一致
- `history-fallback-error`：BASE_URL 指向不可达端口 → 回退关键词且不出错

> 说明：eval 验证**管线正确性与降级路径**；真实语义质量依赖具体模型，不可确定性断言，靠 §7.3 人工清单 + 真实模型抽查。

### 7.3 内置 `fake` provider 与人工验收清单

`NIUNIU_AGENT_EMBEDDING=fake`：确定性 hash 向量（64 维，sha256 派生，归一化），零网络，stderr 打一次性 "test provider" 告警。供 eval / 冒烟 / 无 key 环境验证管线。

真实模型人工验收（评审通过后执行）：
1. off 模式全量回归（现有测试 + eval baseline 不动）
2. 中文改写查询命中：归档 "exponential backoff retry"，查 "重试是怎么做的" → top3 命中，标注 semantic
3. 断网查询 → 关键词结果 + unavailable 附注
4. 切换模型 → 旧 .vec 全部失效；`index-history` 后恢复语义命中
5. `index-history` 对 1000+ 存量 chunk 的耗时与失败率记录

## 8. 文件级改动清单

| 文件 | 改动 |
|---|---|
| `agent/internal/embed/embed.go`（新） | Client 接口、openai 兼容实现、FromEnv、fake provider、FNV/SHA 工具 |
| `agent/internal/embed/embed_test.go`（新） | httptest + fake 全套 |
| `agent/internal/tools/history_search.go` | SaveHistoryArchive 加 embedder 参数；.vec 读写与校验；SearchHistoryArchive 混合检索 + RRF；HistorySearch 结构体加 Embed 字段；工具描述更新 |
| `agent/internal/tools/history_search_test.go` | 上述单测 |
| `agent/internal/loop/loop.go` | Options.Embedder 字段；compact 调用点传参；§1.3 注释修正 |
| `agent/internal/loop/compact.go` | compact → SaveHistoryArchive 传 embedder |
| `agent/cmd/niuniu-agent/main.go` | sessionRegistry：FromEnv + Dir 改 `tools.HistoryDir(cwd)`（bug 修复）；Options 传 Embedder；`index-history` 子命令 |
| `agent/internal/acp/server.go` | Options 传 Embedder |
| `agent/eval/tasks/`（新 3 个） | §7.2 用例 |
| `agent/README.md` | env 表、数据流隐私说明、index-history 用法、P6 段落更新 |

## 9. 分期

- **P1（核心）**：embed 包 + sidecar + 混合检索/RRF + 装配与 bug 修复 + 单测 —— 可独立合入（默认关闭，零行为变化）
- **P2**：`index-history` 子命令 + 3 个 eval 用例 + README
- **P3（可选，另立单）**：int8 量化、进程内向量缓存、`internal/embed` 复用到服务端 kb_search / memory 检索

## 10. 开放决策点（评审重点）

| # | 决策 | 推荐 | 备选 |
|---|---|---|---|
| D1 | 配置形态 | 独立 env 三件套（EMBEDDING/BASE_URL/API_KEY） | 挂 profile（embedding 服务商常与 chat 不同，混在一起反而绕） |
| D2 | 存储 | .json.vec sidecar | 集中式 vectors.bin + entries.jsonl（省 inode，但对账逻辑重） |
| D3 | 索引时机 | compact 同步批量 | 后台 goroutine（快但引入生命周期复杂度） |
| D4 | 回填 | 显式 `index-history` 子命令 | 查询时惰性回填（查询路径变慢且需网络） |
| D5 | 融合 | RRF(k=60)，各榜 top-50 | 加权归一（要调参、跨模型不稳） |
| D6 | 默认 BASE_URL | 预填 `https://api.openai.com/v1` | 必须显式配置（更保守，但多一步） |
| D7 | fake provider | 内置 `fake` 值供 eval/冒烟 | 不内置（eval 需自起 httptest，复杂） |
| D8 | §1.3 目录 bug | 并入本设计 P1 修复 | 单独立单（建议并入，改动同一处代码） |
