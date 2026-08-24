.PHONY: dev dev-backend dev-frontend \
	build build-win build-linux build-mcp \
	build-personal build-personal-current build-personal-all \
	build-personal-windows build-personal-darwin build-personal-linux \
	package-personal-darwin package-personal-linux \
	dev-desktop-v2 \
	_personal-prepare _personal-prepare-current _personal-prepare-v2 \
	clean test test-coverage test-services test-handlers test-pg test-pg-smoke docs sqlc sqlc-lint \
	builtin-scenes-sync builtin-skills-sync \
	dev-relay dev-relay-web build-relay test-relay test-all \
	relay-docker relay-compose-up relay-compose-down relay-compose-logs

# VERSION is the build identity baked into binary names and ldflags. CI sets it
# explicitly via `VERSION="$TAG" make ...`; locally it falls back to git describe
# so dev builds get unique informative names (e.g. v1.0.7-3-gabc1234-dirty).
# Output binaries follow the industry pattern <app>-<version>-<os>-<arch><ext>
# — version-first puts the most-load-bearing piece up front (kubectl / GitHub
# CLI / Hugo / Bun all do this), and reproduces a deterministic name for the
# same tag so download URLs and CDN caches stay stable across rebuilds.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")

# Strip flags: remove symbol table and debug info to reduce binary size
STRIP_LDFLAGS = -s -w

# Server ldflags: inject VERSION into:
#   * github.com/niuniu-dev/niuniu/internal/api.Version — surfaced by GET
#     /api/health and the SPA "About" panel. Note: the file is health.go but
#     the package is `api`, not `health`; the previous path
#     internal/api/health.Version targeted a non-existent package and was
#     silently no-op'd by the Go linker, leaving Version="dev" forever.
#   * main.Version — read by cmd/niuniu-mcp to advertise its version to
#     MCP clients (replaces the hardcoded "1.0.0" literal). Harmless on the
#     niuniu-server build because cmd/niuniu's main package has no Version
#     variable so the linker silently no-ops it.
SERVER_LDFLAGS_COMMON = $(STRIP_LDFLAGS) \
	-X github.com/niuniu-dev/niuniu/internal/api.Version=$(VERSION) \
	-X main.Version=$(VERSION)
SERVER_LDFLAGS = -ldflags "$(SERVER_LDFLAGS_COMMON)"

# macOS cgo env for the niuniu-server/mcp sidecars bundled into the desktop-v2
# app (_personal-prepare): build the non-host arch as a cross-compile; Xcode
# clang needs `-arch <target>` to emit code for the right slice.
DARWIN_ARM64_ENV = CGO_ENABLED=1 CGO_CFLAGS="-arch arm64" CGO_LDFLAGS="-arch arm64" GOOS=darwin GOARCH=arm64
DARWIN_AMD64_ENV = CGO_ENABLED=1 CGO_CFLAGS="-arch x86_64" CGO_LDFLAGS="-arch x86_64" GOOS=darwin GOARCH=amd64

# Server/niuniu-mcp now need CGO for libwebp (github.com/chai2010/webp, which
# vendors the libwebp C source — no target libwebp install required). Cross-
# compiling Linux therefore needs a C cross-compiler; we use `zig cc` (same as
# deploy/self/deploy.sh), producing a static musl binary. `make build-linux`
# thus REQUIRES zig on PATH. Override CC=... to use a different cross toolchain.
LINUX_AMD64_ENV = CGO_ENABLED=1 CC="zig cc -target x86_64-linux-musl" GOOS=linux GOARCH=amd64
LINUX_ARM64_ENV = CGO_ENABLED=1 CC="zig cc -target aarch64-linux-musl" GOOS=linux GOARCH=arm64

# cgo env for the bundled niuniu-server/niuniu-mcp sidecars built into the
# desktop-v2 app (_personal-prepare), selected by $(GOOS)_$(GOARCH). Same
# WebP/cgo requirement as above. Linux is cross-compiled with zig cc (static
# musl); Windows uses zig cc natively (no mingw needed); macOS builds natively
# on its own runner with clang, pinning -arch for the non-host slice. These set
# only the toolchain — the recipe still passes GOOS/GOARCH explicitly.
BUNDLE_CGO_linux_amd64   = CGO_ENABLED=1 CC="zig cc -target x86_64-linux-musl"
BUNDLE_CGO_linux_arm64   = CGO_ENABLED=1 CC="zig cc -target aarch64-linux-musl"
# Windows MUST pin -mcpu=baseline: zig cc without an explicit -target compiles
# for the BUILD HOST's native CPU. On CI (AVX-512-capable Xeon) that let zig
# emit AVX-512 instructions into the vendored libwebp, which crashed with
# STATUS_ILLEGAL_INSTRUCTION (0xC000001D) on end-user CPUs without AVX-512
# (e.g. i9-13900HX) - killing the embedded server on every >100KB image
# upload that entered the WebP optimization pipeline (v0.8.6). Baseline ==
# x86-64 SSE2 floor, matching Go's default GOAMD64=v1. Explicit -target
# (Linux below) already defaults to baseline, so only the native Windows
# build needed the pin.
BUNDLE_CGO_windows_amd64 = CGO_ENABLED=1 CC="zig cc -mcpu=baseline"
BUNDLE_CGO_darwin_arm64  = CGO_ENABLED=1 CGO_CFLAGS="-arch arm64" CGO_LDFLAGS="-arch arm64"
BUNDLE_CGO_darwin_amd64  = CGO_ENABLED=1 CGO_CFLAGS="-arch x86_64" CGO_LDFLAGS="-arch x86_64"
BUNDLE_CGO = $(if $(BUNDLE_CGO_$(GOOS)_$(GOARCH)),$(BUNDLE_CGO_$(GOOS)_$(GOARCH)),CGO_ENABLED=1)

# UPX compression — OFF by default (install: choco/brew/apt install upx).
#
# WARNING: UPX-packed Windows binaries are a top trigger for Chinese AV
# heuristics (360, 火绒, 腾讯) and Microsoft Defender — the UPX unpack stub
# itself matches generic-trojan signatures (e.g. Heur.Generic.H8oAgTEA).
# Go binaries are especially prone because they already have an unusual
# section layout. So local packaging AND the shipped release artifacts all
# ship UNCOMPRESSED; the durable fix for the residual false positives is an
# Authenticode code-signing cert, not compression.
#
# Opt in explicitly with `WITH_UPX=1 make build-...` only if you specifically
# need the smaller size and accept the AV-reputation hit.
ifeq ($(WITH_UPX),1)
UPX := $(shell command -v upx 2>/dev/null)
else
UPX :=
endif
define compress
	@if [ -n "$(UPX)" ]; then \
		echo "Compressing $1 ..."; \
		$(UPX) -q --best "$1"; \
	else \
		echo "SKIP compression (UPX off by default; pass WITH_UPX=1 to enable)"; \
	fi
endef

# Development: run backend and frontend concurrently
dev: dev-backend dev-frontend

dev-backend:
	cd server && go run ./cmd/niuniu

dev-frontend:
	cd server/web && pnpm dev

dev-mobile: mobile-install
	cd mobile && REACT_NATIVE_PACKAGER_HOSTNAME=192.168.3.28 npx expo start

# Build: compile frontend then build Go binary with embedded assets.
# NOTE: server/niuniu-mcp use cgo libwebp — a native build picks up WebP only
# when a C compiler is present (cgo defaults on); without one, cgo turns off and
# WebP degrades to PNG8/JPEG (still functional). For deterministic WebP use
# `make build-linux` (zig cc) or build on a host with a C toolchain.
build:
	cd server/web && pnpm install && pnpm build
	cd server && go build $(SERVER_LDFLAGS) -o ../bin/niuniu-server-$(VERSION) ./cmd/niuniu
	cd server && go build $(SERVER_LDFLAGS) -o ../bin/niuniu-mcp-$(VERSION) ./cmd/niuniu-mcp
	$(call compress,bin/niuniu-server-$(VERSION))
	$(call compress,bin/niuniu-mcp-$(VERSION))
	@echo "NOTE: Desktop (desktop-v2, Tauri) is built separately — make build-personal-v2-current (or build-personal-v2-{windows,darwin,linux})"

build-win:
	cd server/web && pnpm install && pnpm build
	cd server && go build $(SERVER_LDFLAGS) -o ../bin/niuniu-server-$(VERSION).exe ./cmd/niuniu
	cd server && go build $(SERVER_LDFLAGS) -o ../bin/niuniu-mcp-$(VERSION).exe ./cmd/niuniu-mcp
	$(call compress,bin/niuniu-server-$(VERSION).exe)
	$(call compress,bin/niuniu-mcp-$(VERSION).exe)
	@echo "NOTE: Desktop (desktop-v2, Tauri) is built separately — make build-personal-v2-windows"

build-linux:
	cd server/web && pnpm install && pnpm build
	cd server && $(LINUX_AMD64_ENV) go build $(SERVER_LDFLAGS) -o ../bin/niuniu-server-$(VERSION)-linux-amd64 ./cmd/niuniu
	cd server && $(LINUX_ARM64_ENV) go build $(SERVER_LDFLAGS) -o ../bin/niuniu-server-$(VERSION)-linux-arm64 ./cmd/niuniu
	cd server && $(LINUX_AMD64_ENV) go build $(SERVER_LDFLAGS) -o ../bin/niuniu-mcp-$(VERSION)-linux-amd64 ./cmd/niuniu-mcp
	cd server && $(LINUX_ARM64_ENV) go build $(SERVER_LDFLAGS) -o ../bin/niuniu-mcp-$(VERSION)-linux-arm64 ./cmd/niuniu-mcp
	$(call compress,bin/niuniu-server-$(VERSION)-linux-amd64)
	$(call compress,bin/niuniu-mcp-$(VERSION)-linux-amd64)
	$(call compress,bin/niuniu-server-$(VERSION)-linux-arm64)
	$(call compress,bin/niuniu-mcp-$(VERSION)-linux-arm64)
	@echo "NOTE: Desktop (desktop-v2, Tauri) needs the Linux GTK/WebKit dev packages — build on Linux with: make build-personal-v2-linux"

build-mac:
	cd server/web && pnpm install && pnpm build
	cd server && GOOS=darwin GOARCH=arm64 go build $(SERVER_LDFLAGS) -o ../bin/niuniu-server-$(VERSION)-darwin-arm64 ./cmd/niuniu
	cd server && GOOS=darwin GOARCH=amd64 go build $(SERVER_LDFLAGS) -o ../bin/niuniu-server-$(VERSION)-darwin-amd64 ./cmd/niuniu
	cd server && GOOS=darwin GOARCH=arm64 go build $(SERVER_LDFLAGS) -o ../bin/niuniu-mcp-$(VERSION)-darwin-arm64 ./cmd/niuniu-mcp
	cd server && GOOS=darwin GOARCH=amd64 go build $(SERVER_LDFLAGS) -o ../bin/niuniu-mcp-$(VERSION)-darwin-amd64 ./cmd/niuniu-mcp
	$(call compress,bin/niuniu-server-$(VERSION)-darwin-arm64)
	$(call compress,bin/niuniu-mcp-$(VERSION)-darwin-arm64)
	$(call compress,bin/niuniu-server-$(VERSION)-darwin-amd64)
	$(call compress,bin/niuniu-mcp-$(VERSION)-darwin-amd64)
	@echo "NOTE: Desktop (desktop-v2, Tauri) requires macOS SDK — build on macOS with: make build-personal-v2-darwin"

build-mcp:
	cd server && go build $(SERVER_LDFLAGS) -o ../bin/niuniu-mcp-$(VERSION) ./cmd/niuniu-mcp
	$(call compress,bin/niuniu-mcp-$(VERSION))

# Clean build artifacts
clean:
	rm -rf bin/ server/web/dist/ desktop-v2/binaries/niuniu-server* desktop-v2/binaries/niuniu-mcp* desktop-v2/binaries/staging/

# Testing targets
test:
	cd server && go test -v -race -cover ./...

test-coverage:
	cd server && go test -v -race -coverprofile=coverage.out ./...
	cd server && go tool cover -html=coverage.out -o coverage.html

test-services:
	cd server && go test -v ./internal/service/...

test-handlers:
	cd server && go test -v ./internal/api/...

# Run the full server test suite with the PG harness enabled. SQLite paths
# still run (test files are dual-driver where applicable); PG-only smoke
# tests (TestPGSmoke* in internal/store and internal/testing) additionally
# exercise the real Postgres backend via testcontainers, catching PARSE-time
# errors like SQLSTATE 42P18 that SQLite's loose typing hides.
#
# Requires either:
#   - A running Docker daemon (testcontainers spawns postgres:16-alpine), or
#   - NIUNIU_TEST_PG_DSN set to point at an external PG instance.
# Without either, the PG subtests / smoke tests skip silently.
#
# CI usage: GH Actions injects NIUNIU_TEST_PG_DSN via services.postgres, so
# this target is the same single command used by both local Docker dev and
# CI service-container setups.
test-pg:
	cd server && go test ./internal/... -count=1

# Faster opt-in target: run only the PG-named tests (TestPGSmoke* and PG
# subtests via t.Run("postgres", ...)). Skips the bulk of the SQLite-only
# suite. Useful for fast iteration when the goal is just to validate that
# a new sqlc query passes PG PARSE.
test-pg-smoke:
	cd server && go test ./internal/store/ ./internal/testing/ ./internal/service/ \
		-run 'TestPGSmoke|/postgres' -count=1 -v

# Regenerate sqlc store code from internal/store/queries/*.sql.
# sqlc-lint runs first so a PG-incompatible placeholder pattern in source
# fails the build immediately rather than slipping through to demo deploy.
#
# sqlc-postcheck verifies the v1.30.0 known regressions: silent query-string
# truncation of claude_accounts.sql.go (every query ends with `?,` instead of
# `?, ?)`, breaking every claude-account endpoint with HTTP 500), and the
# workspaces_owner_filter.sql long-header-comment parse failure.
schema-diff:
	cd server && go run ./cmd/niuniu admin schema-diff

sqlc: sqlc-lint
	cd server && sqlc generate
	$(MAKE) sqlc-postcheck

sqlc-postcheck:
	@# (a) claude_accounts.sql.go truncation: VALUES (?, ?, ?, with trailing
	@# comma but no closing arg+paren, or WHERE x = at line end. If detected,
	@# abort with restore instructions — silent file overwrite kills demo.
	@if grep -nE 'VALUES \([^)]*,[[:space:]]*$$|WHERE [a-z_]+ =[[:space:]]*$$' server/internal/store/claude_accounts.sql.go > /dev/null; then \
		echo ""; \
		echo "==> ERROR: sqlc v1.30.0 truncated claude_accounts.sql.go (known regression)."; \
		echo "    Restore the file:"; \
		echo "      git checkout 74ac37db -- server/internal/store/claude_accounts.sql.go"; \
		echo "    Then stage your other sqlc changes (NOT including claude_accounts.sql.go) and commit."; \
		echo "    See M0 senior review I1 + Makefile sqlc-postcheck comment for context."; \
		exit 1; \
	fi
	@echo "sqlc-postcheck: claude_accounts.sql.go intact (no v1.30 truncation)"
	@# (b) workspaces_owner_filter.sql long-header-comment parse failure:
	@# sqlc v1.30 refuses to parse this file when its leading `-- ` block
	@# exceeds 2 lines (minimal repro: 3+ leading `--` lines triggers
	@# "mismatched input 'SELECid'"). Keep the header to `-- name:` + at
	@# most one description line; move longer context to git log.
	@if [ "$$(awk '/^[^-]/{exit} {print}' server/internal/store/queries/workspaces_owner_filter.sql | grep -c '^--')" -gt 2 ]; then \
		echo ""; \
		echo "==> WARNING: workspaces_owner_filter.sql leading -- comment block exceeds 2 lines."; \
		echo "    sqlc v1.30 has been observed to refuse parsing this file when the leading"; \
		echo "    comment block gets long. Keep header terse or move detail comments inline."; \
	fi

# Static check for PG-only SQL pitfalls in sqlc source queries. Catches two
# shapes that trip SQLSTATE 42P18 on Postgres at PARSE time (SQLite's loose
# typing makes both invisible in the local test suite):
#   1. Bare `? OP ?` -- both operands are placeholders, PG can't pick an
#      operator overload (`=`, `<>`, arithmetic, `||`, ...).
#   2. Bare `? IS NULL` / `? IS NOT NULL` -- IS NULL is polymorphic at
#      eval time, but PG must still assign a parse-time type and the SQL
#      gives it no anchor. `CAST(? AS BIGINT) IS NULL` is allowed because
#      the `?` sits inside a typed expression -- the regex requires the
#      `?` to be immediately followed by `[space]IS` (the cast form has
#      `?` followed by `[space]AS`, which doesn't match).
# See CLAUDE.md > Known PG-on-server pitfalls. Strips `-- ...` line
# comments before matching so doc text describing the banned shapes
# doesn't false-positive.
sqlc-lint:
	@echo "Linting server/internal/store/queries/*.sql for PG-incompatible placeholder patterns…"
	@awk ' \
		BEGIN { hit = 0 } \
		{ \
			code = $$0; \
			sub(/--.*$$/, "", code); \
			if (code ~ /\?[[:space:]]*(=|<>|!=|<=|>=|<|>|\+|-|\*|\/|\|\|)[[:space:]]*\?/) { \
				printf "%s:%d:[? OP ?] %s\n", FILENAME, FNR, $$0; \
				hit = 1; \
			} \
			if (code ~ /\?[[:space:]]+IS[[:space:]]+(NOT[[:space:]]+)?NULL/) { \
				printf "%s:%d:[? IS NULL] %s\n", FILENAME, FNR, $$0; \
				hit = 1; \
			} \
		} \
		END { exit hit }' server/internal/store/queries/*.sql \
		|| (echo "" && \
			echo "ERROR: PG-incompatible placeholder pattern detected above." && \
			echo "       Postgres cannot infer parameter types at PARSE time -> SQLSTATE 42P18." && \
			echo "       Anchor each '?' against a typed column (e.g. owner_id = ?) or wrap" && \
			echo "       in an explicit cast like CAST(? AS BIGINT) IS NULL." && \
			echo "       See CLAUDE.md > 'Known PG-on-server pitfalls' for the full rationale." && \
			exit 1)
	@echo "  OK — no untyped placeholder patterns."
	@# Lint #2: multi-byte chars in `-- comments` cause sqlc v1.30.0 to silently
	@# truncate the NEXT query string at those bytes. This bug has bit the repo
	@# three times — once breaking GET /api/claude-accounts with HTTP 500
	@# (ORDER BY ... ASC -> ASC clipped to "A"), once gutting
	@# UpdateIssueGoalCondition to leftover "d = ?;", and once before that.
	@# The truncation is non-local: a stray em-dash in comment block A can
	@# corrupt a query several lines below. SQL string literals are safe —
	@# only `-- comment` lines trigger it.
	@echo "Linting server/internal/store/queries/*.sql for multi-byte chars in -- comments…"
	@if LC_ALL=C grep -PHn '^[[:space:]]*--.*[^\x00-\x7F]' server/internal/store/queries/*.sql; then \
		echo ""; \
		echo "ERROR: non-ASCII char detected in a SQL -- comment above."; \
		echo "       sqlc v1.30.0 silently truncates the NEXT query string when"; \
		echo "       these bytes appear in comments. Replace em-dash with --,"; \
		echo "       arrow with -> or :, Chinese with ASCII paraphrase."; \
		echo "       SQL string literals (e.g. WHERE name = '前端') are fine."; \
		echo "       See CLAUDE.md > 'Known sqlc pitfalls' for the full rationale."; \
		exit 1; \
	fi
	@echo "  OK — comments are ASCII-only."

# API documentation generation
docs:
	@echo "Generating API documentation..."
	@cd server && D:/go/bin/swag init --output ../docs --parseDependency --parseInternal --parseDepth 1

# Sync docs/scenes/builtin/*.yaml → server/internal/service/builtin_scenes/.
# The server binary embeds the latter via //go:embed; the source-of-truth
# lives under docs/ for human review and editing. Run after any builtin YAML
# edit. Cross-platform: works on Linux/macOS (cp) and Windows via Git Bash
# (which ships find+cp). Pure-PowerShell run is handled separately by devs
# on rare Windows-only CI hosts.
builtin-scenes-sync:
	@echo "Syncing builtin scene YAMLs → server/internal/service/builtin_scenes/"
	@mkdir -p server/internal/service/builtin_scenes
	@find docs/scenes/builtin -name '*.yaml' -exec cp {} server/internal/service/builtin_scenes/ \;
	@echo "  OK — $$(ls server/internal/service/builtin_scenes/*.yaml | wc -l) scene YAMLs synced"

# Sync docs/scenes/skills/<skill>/ → server/internal/service/builtin_skills/.
# The server binary embeds the latter via //go:embed (scene_skills.go) and the
# scene projector copies a declared skill into <wsDir>/.claude/skills/<name>/.
# *.png samples are excluded — they bloat the binary and are not needed for the
# skills to generate output. Run after re-vendoring any skill under
# docs/scenes/skills/. Git Bash on Windows ships find+cp (--parents).
builtin-skills-sync:
	@echo "Syncing vendored skills → server/internal/service/builtin_skills/ (excluding *.png)"
	@rm -rf server/internal/service/builtin_skills
	@mkdir -p server/internal/service/builtin_skills
	@cd docs/scenes/skills && find fireworks-tech-graph drawio-skill excalidraw-skill geo-citation-audit site-audit imbot-onboarding -type f ! -name '*.png' \
		-exec cp --parents {} ../../../server/internal/service/builtin_skills/ \;
	@echo "  OK — $$(find server/internal/service/builtin_skills -type f | wc -l) skill files synced"

# ─── Personal edition ────────────────────────────────────────────────
# Opt-in bundle: embeds server into the desktop-v2 (Tauri) shell as sidecars.
# Does NOT run in `make build`.

ifeq ($(OS),Windows_NT)
EXE_SUFFIX := .exe
else
EXE_SUFFIX :=
endif

build-personal: build-personal-v2-current
build-personal-current: build-personal-v2-current
build-personal-all: build-personal-v2-all
build-personal-windows: build-personal-v2-windows
build-personal-darwin: build-personal-v2-darwin
build-personal-linux: build-personal-v2-linux

# ─── desktop-v2（Tauri v2 桌面壳层，唯一桌面版）──────────────────────────
# _personal-prepare 构建 Go server/mcp 侧车，按 Rust target triple 拷进
# desktop-v2/binaries/，再 cargo build。原 Wails 版 desktop/ 已移除（issue
# #674 收尾）。
V2_TRIPLE_windows_amd64 = x86_64-pc-windows-msvc
V2_TRIPLE_darwin_arm64  = aarch64-apple-darwin
V2_TRIPLE_darwin_amd64  = x86_64-apple-darwin
V2_TRIPLE_linux_amd64   = x86_64-unknown-linux-gnu
V2_TRIPLE_linux_arm64   = aarch64-unknown-linux-gnu
V2_TRIPLE = $(V2_TRIPLE_$(GOOS)_$(GOARCH))

# cargo 解析：Windows 上 chocolatey make 用 /usr/bin/sh 跑 recipe，其 PATH 常缺
# rustup 默认安装位 ~/.cargo/bin（go 在而 cargo 不在），先 command -v 再回退。
CARGO := $(shell command -v cargo 2>/dev/null || echo "$$HOME/.cargo/bin/cargo")
RUSTUP := $(shell command -v rustup 2>/dev/null || echo "$$HOME/.cargo/bin/rustup")
# 跨平台 target 自动补装（幂等）；rustup 不存在或离线时静默跳过，
# 缺 target 的报错由后续 cargo build 给出（E0463 自带 rustup target add 提示）。
RUSTUP_TARGET_ADD = $(RUSTUP) target add

.PHONY: build-personal-v2-current build-personal-v2-all \
	build-personal-v2-windows build-personal-v2-darwin build-personal-v2-linux \
	package-personal-v2-darwin package-personal-v2-linux \
	dev-desktop-v2 _personal-prepare-v2

# 当前主机构建（Windows 产出 .exe）。sidecar 由 _personal-prepare-v2 staging 到
# binaries/，cargo build 时 build.rs 探测后用 include_bytes! 内嵌进 exe——单文件产物，
# 对齐 v1（go:embed）。不再需要 exe 旁放 sidecar。
# 显式 --target triple（Windows=x86_64-pc-windows-msvc）：webview2-com-sys 仅在
# msvc target_env 下静态链 WebView2LoaderStatic.lib；宿主若默认 GNU 工具链
# （如本机 stable-x86_64-pc-windows-gnu），裸 cargo build 会链 WebView2Loader.dll，
# 单拷 exe 缺 DLL 直接 0xC0000135。显式 triple 不看宿主默认工具链，任何机器都
# 产出无需 DLL 的单文件 exe。
build-personal-v2-current:
	$(MAKE) _personal-prepare GOOS=$(shell go env GOOS) GOARCH=$(shell go env GOARCH) EXT=$(EXE_SUFFIX)
	$(MAKE) _personal-prepare-v2 GOOS=$(shell go env GOOS) GOARCH=$(shell go env GOARCH) EXT=$(EXE_SUFFIX)
	@TRIPLE="$(V2_TRIPLE_$(shell go env GOOS)_$(shell go env GOARCH))"; \
	$(RUSTUP_TARGET_ADD) $$TRIPLE >/dev/null 2>&1 || true; \
	cd desktop-v2 && $(CARGO) build --release --target $$TRIPLE && \
	mkdir -p ../bin && cp target/$$TRIPLE/release/niuniu-desktop-v2$(EXE_SUFFIX) ../bin/niuniu-desktop-v2-$(VERSION)$(EXE_SUFFIX)

build-personal-v2-all: build-personal-v2-windows build-personal-v2-darwin build-personal-v2-linux

# 跨平台构建：构建前自动 `rustup target add <triple>` 补装缺的 Rust target。
# sidecar 内嵌进 exe（单文件产物）。
build-personal-v2-windows:
	$(MAKE) _personal-prepare GOOS=windows GOARCH=amd64 EXT=.exe
	$(MAKE) _personal-prepare-v2 GOOS=windows GOARCH=amd64 EXT=.exe
	-@$(RUSTUP_TARGET_ADD) x86_64-pc-windows-msvc 2>/dev/null || true
	cd desktop-v2 && $(CARGO) build --release --target x86_64-pc-windows-msvc
	mkdir -p bin
	cp desktop-v2/target/x86_64-pc-windows-msvc/release/niuniu-desktop-v2.exe bin/niuniu-desktop-v2-$(VERSION)-windows-amd64.exe

build-personal-v2-darwin:
	$(MAKE) _personal-prepare GOOS=darwin GOARCH=arm64 EXT=
	$(MAKE) _personal-prepare-v2 GOOS=darwin GOARCH=arm64 EXT=
	-@$(RUSTUP_TARGET_ADD) aarch64-apple-darwin 2>/dev/null || true
	cd desktop-v2 && $(CARGO) build --release --target aarch64-apple-darwin
	mkdir -p bin
	cp desktop-v2/target/aarch64-apple-darwin/release/niuniu-desktop-v2 bin/niuniu-desktop-v2-$(VERSION)-darwin-arm64
	$(MAKE) _personal-prepare GOOS=darwin GOARCH=amd64 EXT=
	$(MAKE) _personal-prepare-v2 GOOS=darwin GOARCH=amd64 EXT=
	-@$(RUSTUP_TARGET_ADD) x86_64-apple-darwin 2>/dev/null || true
	cd desktop-v2 && $(CARGO) build --release --target x86_64-apple-darwin
	cp desktop-v2/target/x86_64-apple-darwin/release/niuniu-desktop-v2 bin/niuniu-desktop-v2-$(VERSION)-darwin-amd64

build-personal-v2-linux:
	$(MAKE) _personal-prepare GOOS=linux GOARCH=amd64 EXT=
	$(MAKE) _personal-prepare-v2 GOOS=linux GOARCH=amd64 EXT=
	-@$(RUSTUP_TARGET_ADD) x86_64-unknown-linux-gnu 2>/dev/null || true
	cd desktop-v2 && $(CARGO) build --release --target x86_64-unknown-linux-gnu
	mkdir -p bin
	cp desktop-v2/target/x86_64-unknown-linux-gnu/release/niuniu-desktop-v2 bin/niuniu-desktop-v2-$(VERSION)-linux-amd64

# macOS .app/.dmg 打包（desktop-v2/build/macos/package.sh，自 Wails 版移入）：
# 把 cargo 产出的 Mach-O 包成 .app + 拖拽安装 .dmg；签名/公证由脚本按
# MACOS_SIGN_IDENTITY / APPLE_API_* 环境变量自动启用（CI 从 secrets 注入）。
package-personal-v2-darwin: build-personal-v2-darwin
	bash desktop-v2/build/macos/package.sh \
		--binary bin/niuniu-desktop-v2-$(VERSION)-darwin-arm64 \
		--icon desktop-v2/icons/icon.icns \
		--display-name "Niuniu Desktop" \
		--identifier com.niuniu.personal \
		--version $(VERSION) \
		--arch arm64 \
		--artifact-base niuniu-desktop-v2-$(VERSION) \
		--output-dir bin
	bash desktop-v2/build/macos/package.sh \
		--binary bin/niuniu-desktop-v2-$(VERSION)-darwin-amd64 \
		--icon desktop-v2/icons/icon.icns \
		--display-name "Niuniu Desktop" \
		--identifier com.niuniu.personal \
		--version $(VERSION) \
		--arch amd64 \
		--artifact-base niuniu-desktop-v2-$(VERSION) \
		--output-dir bin

# Linux AppImage 打包（desktop-v2/build/linux/package.sh）：把 cargo 产出的
# ELF 包成免安装 .AppImage；appimagetool 未装时脚本自动下载。
package-personal-v2-linux: build-personal-v2-linux
	bash desktop-v2/build/linux/package.sh \
		--binary bin/niuniu-desktop-v2-$(VERSION)-linux-amd64 \
		--icon desktop-v2/icons/icon.png \
		--display-name "Niuniu Desktop" \
		--version $(VERSION) \
		--arch amd64 \
		--artifact-base niuniu-desktop-v2-$(VERSION) \
		--output-dir bin

# 兼容别名：老名字指向 v2 打包目标。
package-personal-darwin: package-personal-v2-darwin
package-personal-linux: package-personal-v2-linux

# dev 同样显式 triple（见 build-personal-v2-current 注释）：保证 dev 与发布构建
# 同工具链行为（Windows 上 msvc，无 WebView2Loader.dll 依赖）。
dev-desktop-v2:
	$(MAKE) _personal-prepare-current
	$(MAKE) _personal-prepare-v2 GOOS=$(shell go env GOOS) GOARCH=$(shell go env GOARCH) EXT=$(EXE_SUFFIX)
	@TRIPLE="$(V2_TRIPLE_$(shell go env GOOS)_$(shell go env GOARCH))"; \
	$(RUSTUP_TARGET_ADD) $$TRIPLE >/dev/null 2>&1 || true; \
	cd desktop-v2 && $(CARGO) run --target $$TRIPLE

# 把 _personal-prepare 产出的 server/mcp 二进制拷为 Tauri 侧车（以去 triple 名
# 为主 —— server_binary_path 先按 exe 旁/plain 名解析，toolchain 差异不影响；
# 若有三方 triple 映射则额外多拷一份 triple 名以备将来 externalBin 打包用）。
_personal-prepare-v2:
	@mkdir -p desktop-v2/binaries; \
	cp desktop-v2/binaries/staging/$(GOOS)-$(GOARCH)/niuniu-server$(EXT) desktop-v2/binaries/niuniu-server$(EXT); \
	cp desktop-v2/binaries/staging/$(GOOS)-$(GOARCH)/niuniu-mcp$(EXT) desktop-v2/binaries/niuniu-mcp$(EXT); \
	echo "staged sidecars: desktop-v2/binaries/niuniu-server$(EXT) (+niuniu-mcp)"; \
	TRIPLE="$(V2_TRIPLE)"; \
	if [ -n "$$TRIPLE" ]; then \
		cp desktop-v2/binaries/staging/$(GOOS)-$(GOARCH)/niuniu-server$(EXT) desktop-v2/binaries/niuniu-server-$$TRIPLE$(EXT); \
		cp desktop-v2/binaries/staging/$(GOOS)-$(GOARCH)/niuniu-mcp$(EXT) desktop-v2/binaries/niuniu-mcp-$$TRIPLE$(EXT); \
		echo "  + triple name niuniu-server-$$TRIPLE$(EXT)"; \
	fi

# Internal: build server for target platform, copy to
# desktop-v2/binaries/staging/<os>-<arch>/（Wails 版 desktop/ 移除后，desktop-v2
# 自己持有侧车产物目录）. Remove any existing output first — `go build -o <path>`
# refuses to overwrite a non-object file.
#
# pnpm build is skipped when server/web/dist/index.html is newer than every
# tracked SPA source — that's the difference between a 5-second Go-only
# rebuild and a 30-60s full SPA rebuild for unchanged frontend. The skip
# checks src/, index.html, package.json and pnpm-lock.yaml: the four inputs
# that legitimately invalidate the dist. vite.config / tsconfig changes are
# rare; on the off chance they happen, run `make clean` to force.
#
# `pnpm install` still runs every invocation; it is a no-op when
# node_modules matches the lockfile and reconciles automatically when
# package.json drifts. The cost we wanted to skip is `pnpm build`
# (i18n-check + tsc -b + vite build), not the install.
_personal-prepare:
	cd server/web && pnpm install
	@if [ ! -f server/web/dist/index.html ] || \
		[ -n "$$(find server/web/src server/web/index.html server/web/package.json server/web/pnpm-lock.yaml -type f -newer server/web/dist/index.html 2>/dev/null | head -n 1)" ]; then \
			echo "_personal-prepare: rebuilding SPA (source newer than dist)"; \
			cd server/web && pnpm build; \
		else \
			echo "_personal-prepare: SPA dist up to date — skipping pnpm build"; \
		fi
	mkdir -p desktop-v2/binaries/staging/$(GOOS)-$(GOARCH)
	rm -f desktop-v2/binaries/staging/$(GOOS)-$(GOARCH)/niuniu-server$(EXT)
	rm -f desktop-v2/binaries/staging/$(GOOS)-$(GOARCH)/niuniu-mcp$(EXT)
	cd server && $(BUNDLE_CGO) GOOS=$(GOOS) GOARCH=$(GOARCH) go build $(SERVER_LDFLAGS) \
		-o ../desktop-v2/binaries/staging/$(GOOS)-$(GOARCH)/niuniu-server$(EXT) ./cmd/niuniu
	cd server && $(BUNDLE_CGO) GOOS=$(GOOS) GOARCH=$(GOARCH) go build $(SERVER_LDFLAGS) \
		-o ../desktop-v2/binaries/staging/$(GOOS)-$(GOARCH)/niuniu-mcp$(EXT) ./cmd/niuniu-mcp

_personal-prepare-current:
	$(MAKE) _personal-prepare GOOS=$(shell go env GOOS) GOARCH=$(shell go env GOARCH) EXT=$(EXE_SUFFIX)

# ─── Mobile ──────────────────────────────────────────────────────────
.PHONY: mobile-install dev-mobile mobile-dev mobile-build mobile-submit mobile-sync-version

# Refresh mobile/node_modules and package-lock.json every invocation.
# npm install is a no-op when the tree is already in sync, and reconciles
# package.json ↔ package-lock.json ↔ node_modules otherwise.
mobile-install:
	cd mobile && npm install

mobile-dev: mobile-install
	cd mobile && npx expo start

mobile-build: mobile-install mobile-sync-version
	cd mobile && npx eas build --profile preview --platform all

mobile-submit: mobile-install mobile-sync-version
	cd mobile && npx eas submit --profile production --platform all

# Write the current git tag into mobile/app.json so EAS embeds it and the
# Settings → About row reflects the build identity. Standalone target so
# `make mobile-sync-version` can be run before a manual `npx eas build`.
mobile-sync-version:
	cd mobile && node scripts/sync-version.mjs

# ─── Relay ───────────────────────────────────────────────────────────

dev-relay:
	cd relay && go run ./cmd/niuniu-relay

dev-relay-web:
	cd relay/web && pnpm dev

# build-relay must build the SPA first so embed.go picks up a real dist/
# instead of a stale or stub one.  The embedded Validate() in main.go
# will refuse to start if dist/index.html is missing or implausibly small.
build-relay:
	cd relay/web && pnpm install --frozen-lockfile && pnpm build
	cd relay && go build -o ../bin/niuniu-relay-$(VERSION) ./cmd/niuniu-relay

test-relay:
	cd relay && go test ./... -race -cover

test-all:
	cd relay && go test ./...
	cd go-shared && go test ./...
	cd server && go test ./...

relay-docker:
	docker build -f relay/Dockerfile -t niuniu-relay:latest .

relay-compose-up:
	cd deploy/self && docker compose up -d

relay-compose-down:
	cd deploy/self && docker compose down

relay-compose-logs:
	cd deploy/self && docker compose logs -f niuniu-relay

