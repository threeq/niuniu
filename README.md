<div align="center">

# Niuniu

**A local-first workstation for orchestrating AI agents across software, office, data & content work.**

Everything runs on your machine and persists to a local database. No data leaves your host unless you connect an external source yourself.

[![Build](https://github.com/threeq/niuniu/actions/workflows/ci.yml/badge.svg)](https://github.com/threeq/niuniu/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](CONTRIBUTING.md)

</div>

---

Niuniu started as a way to drive many parallel Claude Code sessions and grew into a
general **AI work platform**. You create **projects** (kanban boards of issues),
attach **repositories**, then spin up **workspaces** — isolated directories each
holding one git worktree per attached repo — and run an agent (Claude Code, Codex,
or Qwen Code) inside each workspace to get work done.

A workspace is not limited to code. Through **scenes** — one-click, declarative work
modes — the same workspace becomes an office studio (Word/Excel/PPT), a data cockpit
(query databases, pin live dashboards), a design/diagram bench, a writing/research
desk, or a support console.

> This is the **open-source personal edition** (single-user, MIT). A hosted
> **enterprise edition** with multi-tenant teams, cloud relay, and seat licensing
> exists separately — see [Enterprise](#enterprise-edition).

## Key features

- **Multi-workspace IDE** — each workspace has its own terminal, file tree, git panel, diff viewer, and AI chat
- **Project & kanban management** — issues, columns, checklists, comments, execution plans
- **Multiple agent backends** — Claude Code, Codex, Qwen Code, via PTY terminal or structured proxy chat (messages, costs, permissions, SSE)
- **Any model via scene env presets** — point the agent at any OpenAI- or Anthropic-compatible endpoint (GLM, DeepSeek, MiniMax, Kimi, self-hosted) per workspace
- **Git worktree isolation** — every workspace is a self-contained directory; parallel work never collides
- **Harness pipelines** — templated multi-phase runs (spec → plan → impl → test) with automated checkers and an LLM gate
- **Scenes** — 18 built-in work modes (office, content, data, viz, dev) that project curated MCP servers, plugins, env presets, and conventions into a workspace
- **Office & content generation** — Word/Excel/PPT/PDF/Markdown from a brief, diagrams (draw.io/Excalidraw), posters, landing pages, SEO/JSON-LD
- **Data intelligence** — connect SQL/Redis/Mongo/ES/HTTP sources, governed querying under a strict permission model, live charts & pin-able dashboards
- **Knowledge & memory** — ingest local docs into searchable knowledge bases; versioned project memory (patterns, gotchas, decisions) auto-distilled from sessions
- **Native clients** — Wails v3 desktop (Windows/macOS/Linux) and React Native mobile, plus a browser UI

## Architecture

```
niuniu/
├── server/         # Go backend + embedded React SPA
│   ├── cmd/        # API server + MCP server
│   ├── internal/   # api → service → store (SQLite or PostgreSQL)
│   └── web/        # React 19 + TypeScript + Vite
├── desktop/        # Wails v3 native shell (bundles the server)
├── mobile/         # React Native + Expo Router
├── go-shared/      # Cross-binary shared libs
└── Makefile        # Root build driver
```

**Backend**: Go 1.25 · Gin · sqlc · SQLite (`modernc.org/sqlite`, no CGO) / PostgreSQL · gorilla/websocket · creack/pty
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

**Desktop** (bundles the server into a native app):

```bash
make build-personal-current     # current platform
make build-personal-windows     # Windows .exe
# macOS/Linux need their respective SDKs — see Makefile
```

### Runtime extras (optional, feature-gated)

- **Tesseract OCR** — enables text extraction in `read_image` (otherwise falls back to model vision)
- **Claude Code / Codex / Qwen Code CLI** — the agent backends driven inside workspaces

## Configuration

Default config at `~/.niuniu/config.yaml`; SQLite at `~/.niuniu/niuniu.db`. PostgreSQL
is also supported — see `config-postgres.example.yaml`.

## Enterprise edition

This repository is the open-source **personal edition** (single-user, MIT). A separate
**enterprise edition** adds: multi-tenant teams (`user`/`org` ownership with isolated
storage/streaming/MCP), a cloud relay (account auth, device pairing, multi-node
tunneling), and seat licensing. The commercial code is not in this repo.

Looking for hosted/binary releases? See the project site.

## Contributing

Contributions are welcome! Please read [CONTRIBUTING.md](CONTRIBUTING.md) to get started,
and open an issue first to discuss larger changes. By participating you agree to abide by
the [Code of Conduct](CODE_OF_CONDUCT.md).

## Security

Found a vulnerability? Please see [SECURITY.md](SECURITY.md) for responsible disclosure.

## License

[MIT](LICENSE) © threeq
