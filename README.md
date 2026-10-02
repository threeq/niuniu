<div align="center">

<img src="docs/images/logo.png" width="96" alt="Niuniu logo" />

# Niuniu · 牛牛 AI

**A local-first AI workstation that runs a fleet of coding agents in parallel — across projects, repos, and non-code work.**

Everything runs on your machine and persists to a local database. No data leaves your host unless you connect an external source yourself.

[![Build](https://github.com/threeq/niuniu/actions/workflows/ci.yml/badge.svg)](https://github.com/threeq/niuniu/actions/workflows/ci.yml)
[![License: Source-Available](https://img.shields.io/badge/License-Source--Available-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](CONTRIBUTING.md)

**English** · [简体中文](README.zh-CN.md)

</div>

> **License notice** — this repository is **source-available**, not MIT/OSI open source.
> Free for personal / non-commercial use; **commercial or team/organizational use
> requires a paid license**. See [LICENSE](LICENSE).

---

Niuniu started as a way to drive many parallel Claude Code sessions and grew into a
general **AI work platform**. You create **projects** (kanban boards of issues),
attach **repositories**, then spin up **workspaces** — isolated directories each
holding one git worktree per attached repo — and run an agent inside each workspace
to get work done. Seven agent engines are supported, including **niuniu-agent**, our
own zero-dependency Go agent bundled with the desktop app.

A workspace is not limited to code. Through **scenes** — one-click, declarative work
modes — the same workspace becomes an office studio (Word/Excel/PPT), a data cockpit
(query databases, pin live dashboards), a design/diagram bench, a writing/research
desk, or a support console.

> This is the **source-available personal edition** (single-user, free for
> personal/non-commercial use). A hosted **enterprise edition** with multi-tenant
> teams, cloud relay, and seat licensing exists separately — commercial or
> team/organizational use requires a license, see [Enterprise](#enterprise-edition).

## Screenshots

| Project kanban | Workspace & agent |
|:---:|:---:|
| ![Kanban board](docs/images/board.png) | ![Workspace with agent chat](docs/images/workspace.png) |
| **Kanban per project** — issues flow through columns (todo → implement → review → human review → done); each issue can spawn its own agent workspace. | **Workspace** — an isolated worktree plus a chat with the agent: tool timeline, plans, checklists, token usage, and change review. |

| Workspace overview | Scene catalog |
|:---:|:---:|
| ![Workspace overview](docs/images/overview.png) | ![Scene catalog](docs/images/scenes.png) |
| **Mission control** — activity, stuck detection, and token / cost analytics across all workspaces. | **Scene catalog** — 27 built-in work modes for dev, office, data, content, marketing, knowledge and ops. |

## Key features

- **Parallel workspaces with git worktree isolation** — every workspace is a self-contained directory with one worktree per attached repo; the IDE view ships terminal, file tree, git panel, diff viewer, artifacts and AI chat. Parallel work never collides.
- **Project & kanban management** — issues, columns, checklists, comments, labels; **epics** with a unified epic branch and merge-to-main flow; execution plans and **autohost**, an unattended watchdog that keeps an agent working until the issue's goal condition is met.
- **Seven agent engines** — built-in **niuniu-agent** (zero-dependency Go, ACP protocol, streaming thinking, MCP client, skills, subagents, native memory, auto-compact, self-evolution experiments), plus **Claude Code**, **Codex**, **Qwen Code**, **Cursor**, **Goose** and **omp**.
- **Any model, per workspace** — point an engine at any Anthropic- or OpenAI-compatible endpoint (GLM, DeepSeek, Kimi, MiniMax, Ollama, self-hosted gateways) through workspace-scoped environment providers; model and thinking level are selectable per workspace.
- **Scenes** — 27 built-in work modes that project curated MCP servers, plugins, skills, env presets and conventions into a workspace: dev (Go / TS+React / generic), office (docs, mail, writing, posters, diagrams, media studio), data analysis, knowledge bases (legal / medical / e-commerce / custom), marketing & GEO, bastion ops, customer support, file batching.
- **Office & content generation** — Word / Excel / PPT / PDF / Markdown from a brief, diagrams (draw.io / Excalidraw / architecture), posters and landing pages, plus a short-video pipeline (material grading, quotes, quality gates) backed by an optional video-gen MCP (TTS / image / video / compose).
- **Data intelligence** — connect SQL (MySQL/PostgreSQL/ClickHouse/…), Redis, MongoDB, Elasticsearch and HTTP sources under a strict scope + read/write permission model; run agent-authored queries, render charts, and pin live re-runnable dashboards.
- **Knowledge & memory** — ingest local docs (PDF/Office/text) into searchable knowledge bases; versioned project memory (patterns, gotchas, decisions) distilled from sessions; the built-in agent keeps its own layered memory per project.
- **Scheduled tasks & proactive workflows** — cron-scheduled managed workspaces, IM bot channels (Lark / DingTalk / WeCom / WeChat / Telegram), an attention inbox, and an info-radar scene for filtered digests.
- **Cost & token analytics** — per-workspace usage accounting (input / output / cache read / cache write), a token consumption chart, and stuck-workspace detection.
- **Native clients** — Tauri v2 desktop app (Windows / macOS / Linux, bundling the server and the niuniu-agent sidecar), React Native mobile app (Expo Router), and a browser UI.

## Architecture

```
niuniu/
├── server/         # Go backend + embedded React SPA
│   ├── cmd/        # API server + MCP server
│   ├── internal/   # api → service → store (SQLite or PostgreSQL)
│   └── web/        # React 19 + TypeScript + Vite
├── agent/          # niuniu-agent — built-in zero-dependency Go agent (own module)
├── desktop-v2/     # Tauri v2 native shell (bundles the server + agent sidecars)
├── mobile/         # React Native + Expo Router
├── go-shared/      # Cross-binary shared libs (protocols, pairing crypto, version)
├── docs/           # design system, specs, scene docs, screenshots
└── Makefile        # Root build driver
```

**Backend**: Go 1.25 · Gin · sqlc · SQLite (`modernc.org/sqlite`, no CGO) / PostgreSQL · gorilla/websocket · creack/pty
**Agent**: Go, zero third-party dependencies · Anthropic `/v1/messages` + OpenAI `/chat/completions` protocols · ACP (Agent Client Protocol)
**Frontend**: React 19 · TypeScript · Vite · TanStack Router/Query · Zustand · shadcn/ui · Tailwind CSS 4 · xterm.js

## Getting started

### Prerequisites

- Go 1.25+
- Node.js 18+ with pnpm
- Git

### Build & run

```bash
# Backend + frontend concurrently (dev mode)
make dev

# Or separately:
make dev-backend     # Go server on :3000
make dev-frontend    # Vite dev server on :5173 (proxies /api + /ws to :3000)

# Production build
make build           # builds server + MCP binaries into bin/
```

**Desktop** (bundles the server into a native app — desktop-v2, Tauri):

```bash
make build-personal-v2-current  # current platform
make build-personal-v2-windows  # Windows .exe
# macOS/Linux need their respective SDKs — see Makefile
```

Prebuilt installers (Windows `.exe`, macOS `.dmg`, Linux `.AppImage`) are published on
the GitHub Releases page; hosted editions and docs live at [niu6ai.com](https://niu6ai.com).

### Runtime extras (optional, feature-gated)

- **Tesseract OCR** — enables text extraction in `read_image` (otherwise falls back to model vision)
- **Claude Code / Codex / Qwen Code / Cursor / Goose / omp CLI** — external agent engines driven inside workspaces (the built-in niuniu engine needs none of them)

## Configuration

Default config at `~/.niuniu/config.yaml`; SQLite at `~/.niuniu/niuniu.db`. PostgreSQL
is also supported — see `config-postgres.example.yaml`.

## Enterprise edition

This repository is the source-available **personal edition** (single-user, free
for personal/non-commercial use). A separate **enterprise edition** adds:
multi-tenant teams (`user`/`org` ownership with isolated storage/streaming/MCP),
a cloud relay (account auth, device pairing, multi-node tunneling), and seat
licensing. The commercial code is not in this repo. **Commercial or
team/organizational use of this software requires a paid license** — see
[LICENSE](LICENSE).

## Contributing

Contributions are welcome! Please read [CONTRIBUTING.md](CONTRIBUTING.md) to get started,
and open an issue first to discuss larger changes. By participating you agree to abide by
the [Code of Conduct](CODE_OF_CONDUCT.md).

## Security

Found a vulnerability? Please see [SECURITY.md](SECURITY.md) for responsible disclosure.

## License

[Source-Available (NSL)](LICENSE) © 2026 threeq — personal/non-commercial use free;
commercial or team/organizational use requires a paid license.
