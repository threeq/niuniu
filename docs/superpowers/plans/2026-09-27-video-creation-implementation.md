# 视频创作能力实现计划（波次一）

> 依据：`docs/superpowers/specs/2026-09-15-video-creation-one-stop-design.md`（v3.1 最终方案）。本计划只覆盖 **P1（层一编排）+ P2（层三能力模块）**；P3（交互 UI）出范围。
> 执行方式：多 agent 并行，**文件所有权严格划分**——每个 agent 只写自己名下的文件；需要改别人文件时停下来在报告里说明，不越界。

## 0. 波次与文件所有权

| 流 | 内容 | 名下文件（只许写这些） |
|---|---|---|
| **A** | 编排 skill「shortvideo-forge」+ 产物族 schema + 质量 rubric + media-studio 场景扩展 | `docs/scenes/skills/shortvideo-forge/**`（新建）；`docs/scenes/builtin/media-studio.yaml`；`server/internal/service/builtin_scenes/media-studio.yaml`（经 `make builtin-scenes-sync` 同步） |
| **B** | 能力配置域后端（表/服务/API/模块注册表） | `server/internal/store/queries/capability_backends.sql`（新）、`schema.sql`/`schema_postgres.sql`（加表）、`models.go` 与 `store/*.sql.go`（经 `make sqlc` 再生成）、`server/internal/service/capability_backend.go`(+`_test`)、`server/internal/service/capability_module.go`、`server/internal/api/capability_backend.go`、`server/internal/server/router.go` 与 `server.go`（只加接线行） |
| **C** | FFmpeg 打包/解压基础设施 | `server/internal/ffmpegbin/**`（新）、`scripts/fetch-ffmpeg.sh`（新）、`Makefile`（只加 `ffmpeg-stage` 目标与 .PHONY 条目）、`.gitignore`（加 `server/internal/ffmpegbin/dist/`） |
| **D** | `niuniu-video-mcp` 能力模块（五工具 + 后端适配器 + 合成核心） | `server/cmd/niuniu-video-mcp/**`（新） |
| **E** | 设置页「能力配置」区块（按模块 schema 渲染） | `server/web/src/pages/settings/capability-settings.tsx`（新）、`server/web/src/pages/settings/index.tsx`（加 tab 一行）、`server/web/src/lib/api.ts`（加函数）、`server/web/src/i18n/locales/{zh-CN,en}/settings.json`、`server/web/src/types/api.ts` |

**冲突规则**：A/B/C/D/E 互不写对方文件。`Makefile` 归 C（桌面打包接线留波次二）。任何流发现必须改公共文件（除上表标注的"只加接线行"外），停止并在报告中列出所需改动。

## 1. 冻结契约（所有流必须严格遵守）

### 1.1 REST 契约（B 实现，E 消费）

```
GET  /api/capability-modules
  → { "modules": [ { "name": "video-gen", "display_name": "视频创作",
        "config_schema": {
          "capabilities": [
            { "key": "tts", "label": "配音", "backends": [
                { "value": "openai-compat", "label": "OpenAI 兼容",
                  "fields": [ {"key":"base_url","label":"Base URL","type":"string"},
                              {"key":"api_key","label":"API Key","type":"secret"},
                              {"key":"model","label":"模型","type":"string"} ] } ] },
            { "key": "image", ... }, { "key": "video", "backends": [seedance, kling] }
          ] } } ] }

GET  /api/capability-backends?module=video-gen[&owner_type=&owner_id=]
  → { "backends": [ { "id":1, "module":"video-gen", "capability":"tts", "backend":"openai-compat",
        "name":"我的TTS", "base_url":"https://...", "has_api_key":true, "extra_config":{"model":"tts-1"},
        "enabled":true, "position":0 } ] }        # 永不返回明文 api_key

POST /api/capability-backends   body: {module, capability, backend, name, base_url, api_key, extra_config, enabled}
  → 201 + 同上单条对象（api_key 不回显）
PUT  /api/capability-backends/:id   body 同上（api_key 缺省/空 = 不改密钥）
DELETE /api/capability-backends/:id → 204
```

### 1.2 数据表（B）

```sql
capability_backends(
  id INTEGER PK, owner_type TEXT('user'|'workspace'), owner_id INTEGER,
  module TEXT, capability TEXT, backend TEXT, name TEXT,
  base_url TEXT, api_key TEXT, extra_config TEXT(JSON), enabled INTEGER, position INTEGER,
  created_at/updated_at TEXT, UNIQUE(owner_type,owner_id,module,capability,name))
```
DDL 双写 `schema.sql` + `schema_postgres.sql`（PG 用 BIGSERIAL），跑 `make schema-diff` 验证；查询文件 ASCII 注释、`?` 锚定列（红线条）。

### 1.3 Go 服务接口（B 提供给波次二接线）

```go
// service/capability_backend.go
type CapabilityBackend struct { ID int64; OwnerType string; OwnerID int64; Module, Capability, Backend, Name, BaseURL, APIKey string; ExtraConfig map[string]string; Enabled bool; Position int }
func (s *CapabilityBackendService) List(ctx, ownerType string, ownerID int64, module string) ([]CapabilityBackend, error)
func (s *CapabilityBackendService) Create(ctx, in CapabilityBackend) (*CapabilityBackend, error)
func (s *CapabilityBackendService) Update(ctx, id int64, in CapabilityBackend, apiKeySet bool) (*CapabilityBackend, error)
func (s *CapabilityBackendService) Delete(ctx, id int64) error
// ResolveEnv 解析某工作空间应注入能力模块的环境变量：按 capability 取
// workspace 行优先、否则 user 全局行（enabled，position 最小者）。
func (s *CapabilityBackendService) ResolveEnv(ctx context.Context, userID, workspaceID int64, module string) (map[string]string, error)
// service/capability_module.go
type CapabilityModule struct { Name, DisplayName string; ConfigSchema json.RawMessage; Scenes []string }
func ListCapabilityModules() []CapabilityModule   // 本期仅 "video-gen" 一条，schema 内嵌
```

**环境变量命名（冻结，D 消费）**：每 capability 产出
`NN_CAP_<CAP>_BACKEND` / `NN_CAP_<CAP>_BASE_URL` / `NN_CAP_<CAP>_API_KEY`（无则省略）+ extra_config 每键 `NN_CAP_<CAP>_<KEY大写>`；CAP ∈ `TTS|IMAGE|VIDEO`。

### 1.4 FFmpeg 包接口（C 实现，波次二接线进 D）

```go
package ffmpegbin  // server/internal/ffmpegbin
// Resolve: $NIUNIU_FFMPEG → 解压副本 <dataDir>/bin/ffmpeg/<fp>/ffmpeg[.exe] → exec.LookPath("ffmpeg")
func Resolve(dataDir string) (string, error)
func ResolveFFprobe(dataDir string) (string, error)  // 同序，$NIUNIU_FFPROBE
func Available(dataDir string) bool
func Fingerprint() string   // 内嵌负载指纹；stub 构建返回 ""
```
- `embed_bundled.go`（`//go:build ffmpeg_bundled`）`//go:embed dist/<goos>-<goarch>/ffmpeg*`；`embed_stub.go`（`//go:build !ffmpeg_bundled`）空负载。
- 解压：原子写 + chmod 0755（非 Windows）+ `.fp` 标记，指纹匹配跳过；并发安全（进程内 mutex + rename 原子性）。
- Makefile `ffmpeg-stage`：调 `scripts/fetch-ffmpeg.sh`（curl 下载 + 解包，Win/Linux 用 BtbN 静态构建，macOS best-effort evermeet/osxexperts 并在注释标注风险），产物落 `server/internal/ffmpegbin/dist/$(GOOS)-$(GOARCH)/`；已存在即跳过（`FORCE=1` 重下）。

### 1.5 能力模块（D）

- 二进制 `niuniu-video-mcp`；flags：`--workspace-dir <abs>`（必填）、`--data-dir <abs>`（可选，默认 `~/.niuniu`）。
- 工具名（冻结）：`quote_estimate` / `tts_generate` / `image_generate` / `video_generate` / `media_compose`。
- 后端适配器接口 + 注册表：
```go
type TTSBackend interface   { Synthesize(ctx, TTSRequest) (TTSResult, error) }
type ImageBackend interface { Generate(ctx, ImageRequest) ([]ImageCandidate, error) }
type VideoBackend interface { Submit(ctx, VideoRequest) (TaskHandle, error); Poll(ctx, TaskHandle) (TaskStatus, error); Fetch(ctx, TaskHandle) ([]VideoCandidate, error) }
// 注册表按 NN_CAP_<CAP>_BACKEND 选择；实现：openai-compat(TTS/Image)、seedance、kling(Video)
```
- ffmpeg 经依赖注入：`type Deps struct { ResolveFFmpeg func() (string, error) }`，默认实现 = `exec.LookPath("ffmpeg")`（波次二替换为 ffmpegbin.Resolve，一行）。
- 产物路径（冻结）：`<ws>/video-project/{storyboard.json, quotes/, shots/, qc/, output/final.mp4}`。
- `media_compose` 硬前置：`storyboard.json` 的 `review_status == "approved"`，否则拒绝并提示。
- `video_generate`/`image_generate` 付费调用前写报价留痕 `quotes/<quote_id>.json`；失败不自动重试（返回明确错误 + 任务号）。
- 测试：HTTP 后端用 `httptest`；ffmpeg 相关测试在 ffmpeg 不可解析时 `t.Skip`。

### 1.6 编排 skill（A）

- `docs/scenes/skills/shortvideo-forge/SKILL.md` + `schemas/storyboard.schema.json` + `references/`（合成配方/成本纪律/质量 rubric）。
- storyboard.json 必填字段（冻结，D 校验）：`{title, aspect_ratio, fps, resolution, review_status, shots:[{id, duration_sec, narration, subtitle, visual:{type,tier,prompt,asset}, tts:{voice,asset}, transition}]}`。
- 场景：`media-studio.yaml` 加 `skills: [shortvideo-forge]`、`mcp:` 加 `- name: video-gen`、新增 4 个 quick_actions（脚本分镜 / 一键出片 / AI 视频增强 / 解说二创）、description 更新（档位 L1/L2/L3 + 降级链）。
- 同步：`make builtin-skills-sync && make builtin-scenes-sync`；守卫测试通过。

## 2. 各流验收（自测命令）

```bash
# 通用
cd server && go build ./cmd/niuniu-mcp && go vet ./internal/<你的包>/...
# A
make builtin-skills-sync && make builtin-scenes-sync && cd server && go test ./internal/service/ -run 'TestBuiltin'
# B
cd server && make -C .. sqlc 2>/dev/null || (cd .. && make sqlc)   # 再生成
cd .. && make schema-diff && cd server && go test ./internal/service/ -run TestCapability
# C
cd server && go build ./internal/ffmpegbin/... && go test ./internal/ffmpegbin/...
bash scripts/fetch-ffmpeg.sh --dry-run   # 若网络不可用，如实报告
# D
cd server && go build ./cmd/niuniu-video-mcp && go test ./cmd/niuniu-video-mcp/...
# E
cd server/web && pnpm lint && pnpm test:run
```

## 3. 波次二（本波完成后按结果编排）

- 接线：`FindVideoMCPBinary` 搜索 + `.mcp.json` 注入（command + `NN_CAP_*` env，复用 `service/mcp.go` 既有模式）；ffmpegbin 接入 D 的 Deps。
- 桌面打包：`_personal-prepare` 增建 `niuniu-video-mcp` 并 stage（含 `ffmpeg-stage` 前置 + `-tags ffmpeg_bundled`）；`desktop-v2/build.rs`/`server.rs` 内嵌+解压第三个 sidecar。
- 端到端验证：媒体舱建舱 → `.mcp.json` 含 video-gen 与 env → 工具可见。
