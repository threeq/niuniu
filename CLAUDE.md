# CLAUDE.md

Guidance for AI coding agents (Claude Code) working in this repository.

> This is the **open-source personal edition** of Niuniu (single-user, MIT). An
> **enterprise** edition (multi-tenant teams, cloud relay, seat licensing) lives
> in a separate private repo. Items marked **[enterprise]** below are NOT in this
> codebase — keep contributions scoped to the personal edition.

## Project at a glance

Niuniu is a local-first workstation for driving many parallel AI agent
(Claude Code / Codex / Qwen Code) sessions across projects and repositories. A
user creates **projects** (kanban boards of issues), attaches **repositories**,
then spins up **workspaces** — isolated dirs each holding one `git worktree` per
attached repo — and runs an agent inside each workspace. Workspaces also become
office studios, data cockpits, design benches, etc. via **scenes**. Everything
persists to a local SQLite database (or PostgreSQL) and streams to clients over
WebSocket/SSE. No data leaves your machine unless you connect an external source.

## Repository layout (multi-module Go workspace)

| Path | Stack | Purpose |
|------|-------|---------|
| `server/` | Go 1.25 + React 19 SPA at `server/web/` | HTTP/WS/SSE backend, sqlc store, MCP server, embedded SPA |
| `desktop/` | Go + Wails v3 (own `go.mod`) | Native desktop — `cmd/personal` bundles a local server + remote picker |
| `mobile/` | React Native + Expo Router | iOS/Android client |
| `go-shared/` | Go (own `go.mod`) | Cross-binary shared libs (`lanproto/`, `relayproto/`, `pairingcrypto/`, `tokenbind/`, `version/`) |

The server module is `github.com/niuniu-dev/niuniu` and lives in `server/`. **All
`go` commands run from `server/`.** The frontend is at `server/web/`, embedded via
`server/web/embed.go` (`//go:embed all:dist`). The root `Makefile` is the build
entry point; each `make build*` appends a `yyyyMMddHHmmss` timestamp.

Server internals (`server/internal/`) — one file per domain: `api/` (Gin
handlers), `service/` (business logic), `store/` (sqlc-generated), plus
`agentproxy/`, `terminal/`, `harness/`, `scheduler/`, `notify/`, `event/`,
`auth/`, `migration/`, `integration/`, `claudehome/`, `credstore/`, `imageopt/`,
`paths/`, `git/`, etc. The **license contract** is the *public* package
`server/license/` (the `Gate` interface + a no-op default; see Personal edition).

## Build & run commands

| Command | Purpose |
|---------|---------|
| `make dev` | Run backend + frontend concurrently |
| `make dev-backend` | `cd server && go run ./cmd/niuniu` (port 3000) |
| `make dev-frontend` | `cd server/web && pnpm dev` (port 5173, proxies `/api`+`/ws`) |
| `make build` / `make build-win` | Build server + MCP into `bin/` |
| `make test` / `make test-services` / `make test-coverage` | Go tests `-race -cover` |
| `make sqlc` | Regenerate `internal/store/*.sql.go` from `queries/*.sql` |
| `make schema-diff` | Verify SQLite/PG schema parity (run after any DDL change) |
| `make docs` | Regenerate Swagger |
| `make build-personal[-current\|-windows\|-darwin\|-linux]` | Bundled desktop build |
| `make package-personal-darwin` | macOS `.app` + `.dmg` (needs macOS host) |

Backend one-offs (from `server/`): `go run ./cmd/niuniu`, `go run ./cmd/niuniu-mcp`,
`go test -run Name ./internal/...`. Frontend (from `server/web/`): `pnpm dev`,
`pnpm build` (`tsc -b && vite build`), `pnpm lint`, `pnpm test:run`. **Package
manager is pnpm.** Default ports: backend `:3000`, Vite `:5173`.

> Don't `go build ./...` from a bare checkout without building the frontend first —
> `//go:embed all:dist` fails when `server/web/dist/` is missing.

## Backend architecture (`server/internal/`)

Layered: **Handler** (`api/`) → **Service** (`service/`) → **Store** (`store/`,
sqlc) → SQLite/PostgreSQL. Manual DI assembled in `internal/server/server.go`;
routes in `internal/server/router.go`. One file per domain in `service/` and
`api/` — discover by name.

| Package | Role |
|---------|------|
| `api/` | Gin handlers; `response.go` standardizes JSON envelopes |
| `service/` | Business logic |
| `service/owner_ref.go` | `OwnerRef.WorkspacePath/RepositoryPath` — only legal way to build per-owner paths |
| `store/` | sqlc CRUD; raw SQL in `queries/*.sql`; `schema.sql` runs on startup |
| `store/open.go` | Per-table column-add migrations (driver-aware) |
| `store/migrate.go` | Higher-level feature migrations called from `server.New` |
| `migration/` | On-disk migrations |
| `git/` | Git plumbing via `os/exec` |
| `terminal/` | `creack/pty` PTY manager + websocket bridge |
| `agentproxy/` | Non-PTY chat with the CLI: messages, sessions, costs, SSE |
| `harness/` | Pipeline runner (typed specs, checkers, `ai_judge` LLM gate) |
| `scheduler/` | Cron triggers → `schedule_runs` |
| `notify/` | Per-window WS hub at `/ws/notify` |
| `event/` | In-process pub/sub bus |
| `auth/` | JWT, identity, MFA, rate limit (auth is **off** by default in personal edition) |
| `integration/` | External-provider abstraction + AES-GCM crypto |

## Frontend architecture (`server/web/src/`)

React 19 + TypeScript + Vite, **TanStack Router/Query**, **Zustand**,
**shadcn/ui**, **Tailwind 4**, **xterm.js**, **react-resizable-panels**,
**@dnd-kit**. Routes in `router.tsx`; pages in `pages/`; components in
`components/` (`ui/`, `dialogs/`, `issue/`, `shared/`, `layout/`). Zustand stores
in `stores/` (identity/config, workspace-IDE, agent/streams). Lib: `api.ts`,
`ws.ts`. Types in `types/*.ts`.

## Frontend design system — MANDATORY

**Every UI change must follow `docs/design-system.md`.** Hard gate — PRs that
violate it get bounced. Single source of truth for color tokens, typography
(weights 400/500/600 only), spacing (Tailwind 4px grid, no arbitrary values),
shadcn components, `lucide-react` icons, `t()` i18n, and dark-mode parity.

⚠️ **Auto-rejection red lines:**
- No hex codes in components except external user input (`label.color`).
- No Tailwind arbitrary color/spacing (`bg-[#xxx]`, `p-[7px]`).
- No hardcoded user-visible strings in JSX — always `t('...')`.
- Priority **0=low must use the neutral-gray token**, never green.
- One primary CTA per page max.
- Banned: gradient text, dark glow, glassmorphism, `shadow-xl+`, `transition-all`.
- Hand-rolling `<button>` with raw Tailwind instead of shadcn `<Button>`.

Adding a token is deliberate (update doc + `index.css` + `tailwind.config.js` in
the same change). **Read `docs/design-system.md` before any UI change.**

## Database

Dual-driver (SQLite default / PostgreSQL) behind a query-rewriting wrapper.

⚠️ **Red lines (each has caused production bugs):**
- **Dual-schema parity**: every DDL goes into BOTH `schema.sql` and
  `schema_postgres.sql` (`INTEGER PK AUTOINCREMENT`→`BIGSERIAL`, etc.). Run
  `make schema-diff` after — `OK: no table-level drift detected` is the pass.
- **Never `CREATE INDEX` in schema files for a migration-added column** — fails on
  existing DBs. The index goes in `migrate.go` after `addColumnIfNotExists`.
- **Services hold `*store.DB`, not `*sql.DB`** (the wrapper rewrites `?`→`$N` and
  `INSERT OR IGNORE` for PG). Forbidden: `*sql.DB` service fields, bare `?` on raw
  `*sql.DB`.
- **Every `-- comment` in `store/queries/*.sql` must be pure ASCII** (a stray
  non-ASCII char silently truncates a later query). `make sqlc-lint` guards it.
- **Every `?` must anchor to a typed column** (`owner_id = ?`) or sit in an
  explicit cast — bare `?`/`? IS NULL` crashes pgx at parse time.

## Key cross-cutting patterns

- **Workspace ↔ issue ↔ project / workspace ↔ schedule are 1:1.** Reverse-lookup
  via `workspace.issue_id → issue.column_id → column.project_id`. Multi-issue and
  global-scheduler are intentionally not supported.
- **Workspace isolation**: each workspace = its own dir with one git worktree per
  attached repo.
- **Two agent integration paths**: PTY-based (`terminal/` + `service/agent.go`)
  and proxy-based (`agentproxy/`, structured chat + costs + SSE).
- **Auth** is feature-flagged via `cfg.Auth.Enabled` (off in personal edition).
- **Scene-based MCP/plugin projection**: each workspace resolves a "scene"
  (curated MCP servers + plugins), projected by writing `.mcp.json` + a `~/.claude`
  overlay. Plugin install is explicit user-click only.
- **Autohost**: unattended watchdog that keeps an agent working until a goal
  condition is met; completion signaled by the `[AUTOHOST_DONE]` sentinel.
- **Multi-account Claude / Codex**: per-owner CLI creds; PTY-driven login switches.

## Personal edition specifics

- **Auth is off by default** (single-user). The server forces loopback bind when
  unauthenticated — never expose an unauthenticated server on the network.
- **License = no-op.** `server/license/` defines the `Gate` contract; the personal
  build uses `NopGate` (always active, no seat enforcement). `license.Factory` is
  nil here — the enterprise build injects the real gate.
- **No cloud relay** in this repo. **[enterprise]** multi-tenant `user`/`org`
  ownership, relay, and licensing are in the separate private repo.
- **Config & data**: `~/.niuniu/config.yaml`; SQLite at `~/.niuniu/niuniu.db`;
  per-owner dirs at `~/.niuniu/{users,orgs}/<id>/...`.

## Terminology (UI vs data layer) — never "fix" these to match

The UI label was renamed; data/code layers were intentionally NOT renamed
(migration cost; API consumed by mobile + desktop).

| UI string | DB / Go / REST / TS |
|-----------|---------------------|
| 工作流 / Workflow | `project_templates`, `template_id`, `ProjectTemplate`, `/api/project-templates/*` |
| 项目模板 / Project template | `project_blueprints`, `ProjectBlueprint`, `/api/project-blueprints` |

## Contributing

See `CONTRIBUTING.md`. Scope changes to the personal edition; open an issue first
for larger features. By participating you agree to the Code of Conduct.
