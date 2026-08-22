.PHONY: dev dev-backend dev-frontend dev-desktop \
	build build-win build-linux build-mcp \
	build-personal build-personal-current build-personal-all \
	build-personal-windows build-personal-darwin build-personal-linux \
	package-personal-darwin \
	_personal-prepare _personal-prepare-current \
	gen-windows-resources ensure-goversioninfo \
	clean test test-coverage test-services test-handlers test-desktop test-pg test-pg-smoke docs sqlc sqlc-lint \
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

# Desktop ldflags: inject version into the updater (release-poll target) AND
# into cmd/personal.personalVersion (used by probe.Decide for personal↔server
# version-compat check). On Windows we also pass -H windowsgui to hide the
# console window the linker would otherwise attach.
DESKTOP_LDFLAGS_COMMON = $(STRIP_LDFLAGS) \
	-X github.com/niuniu-dev/niuniu-desktop/internal/updater.Version=$(VERSION) \
	-X main.personalVersion=$(VERSION)
ifeq ($(OS),Windows_NT)
DESKTOP_LDFLAGS = -ldflags "$(DESKTOP_LDFLAGS_COMMON) -H windowsgui"
else
DESKTOP_LDFLAGS = -ldflags "$(DESKTOP_LDFLAGS_COMMON)"
endif

# Wails desktop needs CGO (AppKit bindings). On a macOS host, building the
# non-host arch is a cross-compile and Go implicitly turns CGO off; Xcode clang
# also needs `-arch <target>` to emit code for the right slice. Pin both arches
# symmetrically so a build off either Apple Silicon or Intel works.
DARWIN_ARM64_ENV = CGO_ENABLED=1 CGO_CFLAGS="-arch arm64" CGO_LDFLAGS="-arch arm64" GOOS=darwin GOARCH=arm64
DARWIN_AMD64_ENV = CGO_ENABLED=1 CGO_CFLAGS="-arch x86_64" CGO_LDFLAGS="-arch x86_64" GOOS=darwin GOARCH=amd64

# Server/niuniu-mcp now need CGO for libwebp (github.com/chai2010/webp, which
# vendors the libwebp C source — no target libwebp install required). Cross-
# compiling Linux therefore needs a C cross-compiler; we use `zig cc` (same as
# deploy/self/deploy.sh), producing a static musl binary. `make build-linux`
# thus REQUIRES zig on PATH. Override CC=... to use a different cross toolchain.
LINUX_AMD64_ENV = CGO_ENABLED=1 CC="zig cc -target x86_64-linux-musl" GOOS=linux GOARCH=amd64
LINUX_ARM64_ENV = CGO_ENABLED=1 CC="zig cc -target aarch64-linux-musl" GOOS=linux GOARCH=arm64

# cgo env for the bundled niuniu-server/niuniu-mcp built into the personal
# desktop app (_personal-prepare), selected by $(GOOS)_$(GOARCH). Same WebP/cgo
# requirement as above. Linux is cross-compiled with zig cc (static musl);
# Windows uses zig cc natively (no mingw needed); macOS builds natively on its
# own runner with clang, pinning -arch for the non-host slice. These set only
# the toolchain — the recipe still passes GOOS/GOARCH explicitly.
BUNDLE_CGO_linux_amd64   = CGO_ENABLED=1 CC="zig cc -target x86_64-linux-musl"
BUNDLE_CGO_linux_arm64   = CGO_ENABLED=1 CC="zig cc -target aarch64-linux-musl"
BUNDLE_CGO_windows_amd64 = CGO_ENABLED=1 CC="zig cc"
BUNDLE_CGO_darwin_arm64  = CGO_ENABLED=1 CGO_CFLAGS="-arch arm64" CGO_LDFLAGS="-arch arm64"
BUNDLE_CGO_darwin_amd64  = CGO_ENABLED=1 CGO_CFLAGS="-arch x86_64" CGO_LDFLAGS="-arch x86_64"
BUNDLE_CGO = $(if $(BUNDLE_CGO_$(GOOS)_$(GOARCH)),$(BUNDLE_CGO_$(GOOS)_$(GOARCH)),CGO_ENABLED=1)

# goversioninfo (Windows resource compiler for Go).
# Generates resource_windows_amd64.syso from versioninfo.json + icon.ico so
# `go build` auto-embeds the icon into the .exe (file-explorer / start-menu
# icon). The runtime window/taskbar icon is set separately via the embedded
# appicon.png passed to application.Options.Icon.
GO_BIN := $(shell go env GOPATH 2>/dev/null)/bin
ifeq ($(OS),Windows_NT)
GOVERSIONINFO_BIN := $(GO_BIN)/goversioninfo.exe
else
GOVERSIONINFO_BIN := $(GO_BIN)/goversioninfo
endif

ensure-goversioninfo:
	@if [ ! -x "$(GOVERSIONINFO_BIN)" ]; then \
		echo "Installing goversioninfo to $(GOVERSIONINFO_BIN) ..."; \
		go install github.com/josephspurrier/goversioninfo/cmd/goversioninfo@latest; \
	fi

# Parse $(VERSION) (e.g. "v0.2.0-27-g99dee791-dirty", "v1.0.7", "dev") into
# the four-part numeric tuple that Windows FixedFileInfo requires. Falls
# back to 0.0.0.0 when VERSION doesn't match semver — but we still pass the
# full VERSION string into StringFileInfo's FileVersion/ProductVersion so
# users see "v0.2.0-27-g99dee791" in the file Properties dialog. Filling
# these out matters for AV reputation: empty/zero version blocks contribute
# to Heur.Generic heuristic scores, especially on 360 / 火绒 / Defender.
VER_MAJOR := $(shell echo "$(VERSION)" | sed -n 's/^v\?\([0-9][0-9]*\)\..*/\1/p')
VER_MINOR := $(shell echo "$(VERSION)" | sed -n 's/^v\?[0-9][0-9]*\.\([0-9][0-9]*\)\..*/\1/p')
VER_PATCH := $(shell echo "$(VERSION)" | sed -n 's/^v\?[0-9][0-9]*\.[0-9][0-9]*\.\([0-9][0-9]*\).*/\1/p')
VER_BUILD := $(shell echo "$(VERSION)" | sed -n 's/^v\?[0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*-\([0-9][0-9]*\)-.*/\1/p')
VER_MAJOR := $(if $(VER_MAJOR),$(VER_MAJOR),0)
VER_MINOR := $(if $(VER_MINOR),$(VER_MINOR),0)
VER_PATCH := $(if $(VER_PATCH),$(VER_PATCH),0)
VER_BUILD := $(if $(VER_BUILD),$(VER_BUILD),0)

GOVERSIONINFO_VER_FLAGS = \
	-ver-major=$(VER_MAJOR) -ver-minor=$(VER_MINOR) \
	-ver-patch=$(VER_PATCH) -ver-build=$(VER_BUILD) \
	-product-ver-major=$(VER_MAJOR) -product-ver-minor=$(VER_MINOR) \
	-product-ver-patch=$(VER_PATCH) -product-ver-build=$(VER_BUILD) \
	-file-version=$(VERSION) -product-version=$(VERSION)

# Generate the Windows resource (.syso) file for the desktop binary. The
# `-platform-specific=true` flag names the output `resource_windows_amd64.syso`
# so it is auto-included only on windows-amd64 builds and ignored on other GOOS.
# Version flags override the (intentionally zero) defaults in versioninfo.json
# at build time so the artifact carries its real version in the PE header.
gen-windows-resources: ensure-goversioninfo
	cd desktop/cmd/personal && "$(GOVERSIONINFO_BIN)" -platform-specific=true -icon build/icon.ico $(GOVERSIONINFO_VER_FLAGS) versioninfo.json

# UPX compression — OFF by default (install: choco/brew/apt install upx).
#
# WARNING: UPX-packed Windows binaries are a top trigger for Chinese AV
# heuristics (360, 火绒, 腾讯) and Microsoft Defender — the UPX unpack stub
# itself matches generic-trojan signatures (e.g. Heur.Generic.H8oAgTEA).
# Wails/Go binaries are especially prone because they already have an
# unusual section layout. So local packaging AND the shipped release
# artifacts (build-personal-*) all ship UNCOMPRESSED; the durable fix for
# the residual false positives is an Authenticode code-signing cert, not
# compression.
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

dev-desktop:
	cd desktop && wails3 dev

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
	@echo "NOTE: Desktop is now the bundled cmd/personal app — build it explicitly with: make build-personal-current (or build-personal-{windows,darwin,linux})"

build-win:
	cd server/web && pnpm install && pnpm build
	cd server && go build $(SERVER_LDFLAGS) -o ../bin/niuniu-server-$(VERSION).exe ./cmd/niuniu
	cd server && go build $(SERVER_LDFLAGS) -o ../bin/niuniu-mcp-$(VERSION).exe ./cmd/niuniu-mcp
	$(call compress,bin/niuniu-server-$(VERSION).exe)
	$(call compress,bin/niuniu-mcp-$(VERSION).exe)
	@echo "NOTE: Desktop is now the bundled cmd/personal app — build it explicitly with: make build-personal-windows"

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
	@echo "NOTE: Desktop (Wails) requires CGO and Linux SDK — build on Linux with: make build-personal-linux"

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
	@echo "NOTE: Desktop (Wails) requires CGO and macOS SDK — build on macOS with: make build-personal-darwin"

build-mcp:
	cd server && go build $(SERVER_LDFLAGS) -o ../bin/niuniu-mcp-$(VERSION) ./cmd/niuniu-mcp
	$(call compress,bin/niuniu-mcp-$(VERSION))

# Desktop build = the bundled cmd/personal app. cmd/connect (the old remote-only
# picker) has been merged into cmd/personal and retired, so build-personal-* are
# the only desktop build targets — see further below.

# Clean build artifacts
clean:
	rm -rf bin/ server/web/dist/ desktop/build/bin/
	find desktop/internal/bundle/server-bin -type f ! -name '.gitkeep' -delete 2>/dev/null || true
	rm -f desktop/cmd/personal/resource_*.syso

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

test-desktop:
	cd desktop && go test -v -race ./internal/...

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
# Opt-in bundle: embeds server into a Wails shell. Does NOT run in `make build`.

ifeq ($(OS),Windows_NT)
EXE_SUFFIX := .exe
else
EXE_SUFFIX :=
endif

build-personal: build-personal-current

# On Windows hosts, generate the .syso resource so `make build-personal-current`
# also gets the .exe icon embedded. Non-Windows hosts skip the prereq because
# the .syso wouldn't be linked into a darwin/linux binary anyway and we don't
# want to install goversioninfo on machines that won't use it.
ifeq ($(OS),Windows_NT)
build-personal-current: gen-windows-resources _personal-prepare-current
else
build-personal-current: _personal-prepare-current
endif
	cd desktop && go build $(DESKTOP_LDFLAGS) \
		-o ../bin/niuniu-desktop-$(VERSION)$(EXE_SUFFIX) ./cmd/personal
	$(call compress,bin/niuniu-desktop-$(VERSION)$(EXE_SUFFIX))

build-personal-all: build-personal-windows build-personal-darwin build-personal-linux

build-personal-windows: gen-windows-resources
	$(MAKE) _personal-prepare GOOS=windows GOARCH=amd64 EXT=.exe
	cd desktop && GOOS=windows GOARCH=amd64 go build \
		-ldflags "$(DESKTOP_LDFLAGS_COMMON) -H windowsgui" \
		-o ../bin/niuniu-desktop-$(VERSION)-windows-amd64.exe ./cmd/personal
	# NO UPX here on purpose. This .exe IS the artifact shipped to end users
	# (CI uploads it straight to the public release), and UPX packing is the
	# top trigger for Chinese AV (360 / 火绒 / 腾讯) + Microsoft Defender
	# generic-trojan heuristics (see the UPX warning block above). Matches
	# build-personal-darwin / build-personal-linux, which also skip
	# compression. The durable fix for the residual false positives is an
	# Authenticode code-signing cert, not compression.

build-personal-darwin:
	$(MAKE) _personal-prepare GOOS=darwin GOARCH=arm64 EXT=
	cd desktop && $(DARWIN_ARM64_ENV) go build $(DESKTOP_LDFLAGS) \
		-o ../bin/niuniu-desktop-$(VERSION)-darwin-arm64 ./cmd/personal
	$(MAKE) _personal-prepare GOOS=darwin GOARCH=amd64 EXT=
	cd desktop && $(DARWIN_AMD64_ENV) go build $(DESKTOP_LDFLAGS) \
		-o ../bin/niuniu-desktop-$(VERSION)-darwin-amd64 ./cmd/personal

build-personal-linux:
	$(MAKE) _personal-prepare GOOS=linux GOARCH=amd64 EXT=
	cd desktop && GOOS=linux GOARCH=amd64 go build $(DESKTOP_LDFLAGS) \
		-o ../bin/niuniu-desktop-$(VERSION)-linux-amd64 ./cmd/personal

# Linux AppImage packaging. Wraps the bare ELF binary produced by build-* into a
# single portable .AppImage (chmod +x and run, launcher integration included) —
# the Linux equivalent of the macOS .dmg. Requires a Linux host; appimagetool is
# auto-downloaded by the script if not installed.
package-personal-linux: build-personal-linux
	bash desktop/build/linux/package.sh \
		--binary bin/niuniu-desktop-$(VERSION)-linux-amd64 \
		--icon desktop/cmd/personal/appicon.png \
		--display-name "Niuniu Desktop" \
		--version $(VERSION) \
		--arch amd64 \
		--artifact-base niuniu-desktop-$(VERSION) \
		--output-dir bin

# macOS .app/.dmg packaging. Wraps the bare Mach-O produced by build-* into a
# directly-runnable .app bundle, then a .dmg disk image with a drag-to-
# /Applications shortcut. Requires a macOS host (CGO + macOS SDK already
# required by the Wails build itself; hdiutil + xattr are macOS-only too).
# Unsigned: first launch requires `xattr -dr com.apple.quarantine` or
# right-click→Open. Code-signing/notarization is a follow-up.
#
# cmd/personal is a regular dock app (the retired cmd/connect was the menu-bar-
# only LSUIElement variant; it has been merged in and removed).
package-personal-darwin: build-personal-darwin
	bash desktop/build/macos/package.sh \
		--binary bin/niuniu-desktop-$(VERSION)-darwin-arm64 \
		--icon desktop/cmd/personal/build/icon.icns \
		--display-name "Niuniu Desktop" \
		--identifier com.niuniu.personal \
		--version $(VERSION) \
		--arch arm64 \
		--artifact-base niuniu-desktop-$(VERSION) \
		--output-dir bin
	bash desktop/build/macos/package.sh \
		--binary bin/niuniu-desktop-$(VERSION)-darwin-amd64 \
		--icon desktop/cmd/personal/build/icon.icns \
		--display-name "Niuniu Desktop" \
		--identifier com.niuniu.personal \
		--version $(VERSION) \
		--arch amd64 \
		--artifact-base niuniu-desktop-$(VERSION) \
		--output-dir bin

# Internal: build server for target platform, copy to desktop/internal/bundle/server-bin/<os>-<arch>/
# Remove any existing output first — `go build -o <path>` refuses to overwrite
# a non-object file (e.g. the dev stub seeded by Task C4).
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
	mkdir -p desktop/internal/bundle/server-bin/$(GOOS)-$(GOARCH)
	rm -f desktop/internal/bundle/server-bin/$(GOOS)-$(GOARCH)/niuniu-server$(EXT)
	rm -f desktop/internal/bundle/server-bin/$(GOOS)-$(GOARCH)/niuniu-mcp$(EXT)
	cd server && $(BUNDLE_CGO) GOOS=$(GOOS) GOARCH=$(GOARCH) go build $(SERVER_LDFLAGS) \
		-o ../desktop/internal/bundle/server-bin/$(GOOS)-$(GOARCH)/niuniu-server$(EXT) ./cmd/niuniu
	cd server && $(BUNDLE_CGO) GOOS=$(GOOS) GOARCH=$(GOARCH) go build $(SERVER_LDFLAGS) \
		-o ../desktop/internal/bundle/server-bin/$(GOOS)-$(GOARCH)/niuniu-mcp$(EXT) ./cmd/niuniu-mcp

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
	cd desktop && go test ./...

relay-docker:
	docker build -f relay/Dockerfile -t niuniu-relay:latest .

relay-compose-up:
	cd deploy/self && docker compose up -d

relay-compose-down:
	cd deploy/self && docker compose down

relay-compose-logs:
	cd deploy/self && docker compose logs -f niuniu-relay

