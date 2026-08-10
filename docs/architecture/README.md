# Architecture

System design documents — **how the system is put together**, as opposed to
how to operate it. Each file here describes a major subsystem or component:
its boundaries, internal structure, key invariants, and the decisions behind
the design.

| File | Subsystem |
|------|-----------|
| `agentproxy-flow.md` | Claude / Codex 两种 CLI backend、Send loop 时序、bg-task tracker + 进程树探针、SSE OutputEvent、与 autohost 的接口 |
| `personal-edition.md` | `niuniu-desktop` Wails app, `--embedded` server mode, desktop bundle, probe |
| `workspace-model.md` | Workspace ↔ issue ↔ project / workspace ↔ schedule 关系约束；harness 与 scheduler 的解析路径 |

## What belongs here

- Subsystem architecture (component diagrams, data flow, key interfaces)
- Decision records that survive a single feature (why we chose X over Y, and
  what's load-bearing about it)
- Domain models that span multiple files / packages

## What does NOT belong here

- Operational procedures → `docs/ai-context/`
- Per-feature specs (one issue, one fix) → `docs/superpowers/specs/`
- Implementation plans → `docs/superpowers/plans/`
- API contracts → `docs/openapi.yaml`

## Relation to docs/superpowers/specs/

`superpowers/specs/` holds design documents tied to specific tickets / dated
features ("2026-05-19-codex-cli-support-design.md"). `architecture/` holds
the **long-lived** design of subsystems that outlive any single feature. A
spec graduates into architecture when its content is broadly applicable and
load-bearing for the next 6+ months of work.

## Where the current subsystem designs actually live

Not every subsystem has a dedicated file under `architecture/` yet — some
load-bearing design still lives inline in `CLAUDE.md` (root). The current
split:

| Subsystem | Location |
|-----------|----------|
| Personal-edition (Wails + embedded server) | `architecture/personal-edition.md` |
| Workspace ↔ issue ↔ project / schedule relations | `architecture/workspace-model.md` |
| Agentproxy（Claude/Codex backends、Send loop、bg-tasks、SSE） | `architecture/agentproxy-flow.md` |
| Multi-tenant owner model | inline in `CLAUDE.md` → "Multi-tenant model" |
| Harness (typed Spec, pre_commit/on_schedule, ai_judge) | `CLAUDE.md` cross-cutting + `docs/superpowers/specs/2026-05-*-harness-*.md` |
| Scene-based MCP / plugin projection | `CLAUDE.md` cross-cutting + `docs/scenes/builtin/*.yaml` |
| Dual SQLite + PostgreSQL store | inline in `CLAUDE.md` → "Database" |
| Autohost (sentinel + budget 续跑 / 恢复，判停条件注入续跑 prompt) | `architecture/agentproxy-flow.md` §5 + `docs/superpowers/specs/2026-06-10-autohost-remove-llm-judge-design.md` |
| Dev / deploy / release procedures | `docs/ai-context/` |

Promote inline `CLAUDE.md` material into a standalone `architecture/*.md`
file once it crosses ~80 lines and would benefit from being skim-loadable
from a pointer, not always-loaded.
