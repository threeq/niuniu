# Contributing to Niuniu

Thanks for your interest in contributing! This guide covers the basics.

## Getting set up

1. **Fork & clone** the repo.
2. Install prerequisites: **Go 1.25+**, **Node.js 18+ with pnpm**, **Git**.
3. Verify it builds:
   ```bash
   make dev          # runs backend (:3000) + frontend (:5173)
   ```
4. Create a feature branch from `main`.

## Before you open a pull request

- **Build & vet** the backend:
  ```bash
  cd server && go build ./... && go vet ./...
  ```
- **Frontend** (if you touched `server/web/`):
  ```bash
  cd server/web && pnpm install && pnpm lint && pnpm build
  ```
- **Tests**: add or update tests for behavior changes; run
  `cd server && go test ./internal/...` (and `pnpm test:run` for the web).
- **One concern per PR** — keep changes focused; split unrelated work.
- **Conventional Commits** style (`feat:`, `fix:`, `docs:`, …) is appreciated.

## What to contribute

- 🐛 **Bugs** — open an issue with reproduction steps, then a PR.
- ✨ **Features** — please open an issue to discuss scope first.
- 📚 **Docs & scenes** — improvements to docs or new built-in scenes.
- 🎨 **UI** — follow the design system (see `docs/design-system.md`); no hardcoded
  colors/strings, use tokens and `t()` i18n.

## Pull request checklist

- [ ] Builds cleanly (`go build ./...`, `pnpm build`)
- [ ] Tests added/updated and passing
- [ ] No secrets, credentials, or internal infra references committed
- [ ] Commit messages follow Conventional Commits

## Code of conduct

Participation in this project is governed by the [Code of Conduct](CODE_OF_CONDUCT.md).
Please be excellent to each other.

## A note on editions

This is the open-source **personal edition**. Commercial/enterprise features
(multi-tenant teams, cloud relay, licensing) live in a separate private repo and are
not part of this codebase. Please scope contributions to the personal edition.
