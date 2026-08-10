---
name: geo-citation-audit
description: >-
  Use when the user wants to measure GEO (Generative Engine Optimization) — how
  often an LLM cites their brand / URL when answering domain questions. Runs a
  batch of queries across the available model backends (Claude / Qwen / Codex),
  detects brand/URL citations, and produces a citation-rate report + gap list.
  Trigger on: "引用率" "被引用" "GEO" "生成引擎优化" "品牌被 AI 提到" "被大模型引用"
  "citation rate" "does the AI cite" "brand mention in LLM" "share of voice"
  "AI 会不会推荐我" or any request to test whether models mention a brand/site.
---

# GEO Citation Audit — 引用率实测

**牛牛的差异化能力**：用现有多 agent 后端跑一批 query，统计品牌/URL 被 LLM
引用的次数——这是 GEO 最核心、其他工具最缺的指标。**零新依赖**，只用机器上已有的
`claude` / `qwen` / `codex` CLI 与本 skill 自带的纯标准库 Python 引擎。

## 分工（关键）

- **你（agent）负责模糊部分**：确认品牌信息、设计/生成 query 集、决定用哪些模型、
  发起提问、收集回答。
- **引擎 `scripts/geo_audit.py` 负责确定性部分**：引用检测（大小写/全半角无关的子串
  匹配）、引用率计算、缺口清单、报告落盘、产物登记。**绝不要**手工数引用次数——一律
  交给引擎，保证可复现。

## 输入

一次审计需要（缺则向用户确认，不要编造）：

1. **品牌名** `brand` + 可选**别名** `aliases`（中英文写法、handle）。
2. 可选 **URL/域名** `urls`（引用检测会同时匹配完整 URL 和裸域名）。
3. 一组 **query**：领域内用户真实会问的问题。用户没给就基于「品牌/主题」生成
   8–15 条覆盖不同意图（选型、痛点求解、品类盘点、对比）的问题，并让用户确认。
   若工作空间挂了 KB，可用 `kb_search` 从知识库抽取真实高频问法。
4. 可选**竞品** `competitors`：用于「声量占比 Share of Voice」对比。

## 工作流

### 1. 生成配置

```bash
python scripts/geo_audit.py init --out config.json
```

编辑 `config.json`：填入 `brand` / `aliases` / `urls` / `competitors` / `queries`，
设置 `models`（默认 `["claude","qwen","codex"]`）与 `rounds`（每个 query×模型重复
几轮，默认 2，多轮平滑随机性）。

### 2. 探测可用模型

```bash
python scripts/geo_audit.py probe --config config.json
```

只保留 `available: true` 的模型到 `config.json.models`。**只有一个模型可用也能跑**——
就用它多跑几轮；报告会如实标注模型数。不要因为凑不齐三个模型就放弃。

### 3. 采集回答 answers.jsonl

**每个 query × 每个模型 × 每一轮**问一次「中立的领域问题」（**不要**在问题里出现品牌名，
否则等于喂答案，引用率失真），把回答存成 `answers.jsonl`，每行一条：

```json
{"query": "该领域最好的工具是什么？", "model": "claude", "round": 1, "answer": "<模型完整回答>", "error": null}
```

两条采集路径，任选：

- **A · 引擎自动驱动**（模型 CLI 就绪时最省事）：
  ```bash
  python scripts/geo_audit.py collect config.json --out answers.jsonl
  ```
  引擎按 `config.json.commands` 的模板逐一调用 CLI，单次超时/报错记为 error 不中断。
  各 CLI 的一次性调用方式不同（默认 `claude -p {query}` / `qwen -p {query}` /
  `codex exec {query}`），若某个 CLI 需要别的参数，改 `config.json.commands` 里的模板。
- **B · 你分发 sub-agent 采集**：当自动驱动某模型失败、或你想并行时，用你自己的
  多 agent 能力：对每个 query 发一个 sub-agent（或直接 Bash 调对应 CLI），把返回文本
  逐条 append 进 `answers.jsonl`（格式同上）。**成本提示**：这会真实调用多个模型多轮，
  query×模型×轮数 = 调用次数，跑前按体量给用户一个次数预估。

### 4. 打分出报告

```bash
python scripts/geo_audit.py score answers.jsonl config.json --out-dir . --ws-root <工作空间根目录>
```

产出：
- `geo-report.md` — **可预览产物**（右侧产物面板可看），含总体引用率、分模型、
  分 query（引用率升序，缺口在最上）、缺口 query 清单、声量占比。
- `geo-report.json` — 原始数据，供后续周期对比 / cron。
- 自动登记到 `<ws>/.niuniu/artifacts.json`（产物预览面板据此显示）。

### 5. 汇报

面向用户用业务话术给出：**总体引用率**、哪些 query 是**零引用缺口**、
分模型差异、（有竞品时）**声量占比**。缺口 query 就是 GEO 优化的落点——
提示用户「针对这些问题补权威内容 / 让模型学到你的品牌」。

## 引用怎么判定

引擎把回答与「品牌名 + 别名 + URL + 裸域名」做 NFKC 归一化后的大小写无关子串匹配，
命中任一即记一次引用。这是确定、可复现、零依赖的。它不做语义级判断（如「模型描述了
你的产品但没点名」），需要更严的判定时再叠加 LLM 判官，但默认的子串法已足够支撑
「被点名引用率」这一核心指标。

## 纪律

- 采集问题保持中立，**绝不在 query 里塞品牌名**。
- 数字一律来自引擎，不要口算或估计。
- 信息以用户提供为准，缺失就标注并索取。
- 后续可接 cron：把 `score` 步骤挂到周期任务，用 `geo-report.json` 做时间序列对比，
  监测引用率涨跌——本 skill 的产物已为此设计（稳定 JSON schema）。

## 自检

改动引擎后跑：

```bash
python scripts/geo_audit.py selftest
```

用合成回答校验打分数学（引用率、缺口、声量占比、域名解析），无网络、无模型调用。
