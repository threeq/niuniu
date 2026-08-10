# syntax=docker/dockerfile:1.6
#
# Multi-stage image for the niuniu server.
# Runtime image is debian-slim (not distroless) because the server spawns
# `claude`, `git`, `node`, and `python3` as child processes for agent workflows.

# --- 1. Frontend build ---------------------------------------------------------
FROM node:20-bookworm-slim AS web-build
WORKDIR /src/web
RUN --mount=type=cache,target=/root/.npm \
    npm install -g pnpm@9
COPY server/web/package.json server/web/pnpm-lock.yaml* ./
RUN pnpm install --frozen-lockfile
COPY server/web ./
RUN pnpm build

# --- 2. Go build ---------------------------------------------------------------
FROM golang:1.25 AS go-build
WORKDIR /src
COPY go.work go.work.sum ./
COPY go-shared go-shared
COPY server server
# relay/ and desktop/ are workspace members but niuniu-server doesn't import them.
# Stub them so `go.work` resolves without shipping their source.
RUN mkdir -p relay desktop && \
    echo "module github.com/niuniu-dev/niuniu/relay" > relay/go.mod && \
    echo "go 1.25" >> relay/go.mod && \
    echo "module github.com/niuniu-dev/niuniu-desktop" > desktop/go.mod && \
    echo "go 1.25" >> desktop/go.mod
# Replace the embedded SPA with the one built in stage 1
RUN rm -rf server/web/dist
COPY --from=web-build /src/web/dist server/web/dist

WORKDIR /src/server
# cgo libwebp (chai2010/webp vendors the C source) needs a C compiler at build
# time — no libwebp-dev required, only gcc + libc headers. golang:1.25 is
# debian-based, so install via apt.
RUN apt-get update && apt-get install -y gcc libc6-dev && rm -rf /var/lib/apt/lists/*
RUN CGO_ENABLED=1 GOOS=linux go build -trimpath -ldflags="-s -w" \
        -o /out/niuniu-server ./cmd/niuniu
RUN CGO_ENABLED=1 GOOS=linux go build -trimpath -ldflags="-s -w" \
        -o /out/niuniu-mcp ./cmd/niuniu-mcp

# --- 3. Runtime ---------------------------------------------------------------
FROM debian:bookworm-slim AS runtime

ENV DEBIAN_FRONTEND=noninteractive

# npm's global prefix lives in a writable dir (chowned to niuniu below) so the
# unprivileged runtime user can upgrade the Claude/Codex CLIs from the System
# Deps page — `npm install -g` against the default /usr/lib prefix is root-only
# and fails with "权限不足" in the team docker container.
ENV NPM_CONFIG_PREFIX=/usr/local/npm-global
ENV PATH=/usr/local/npm-global/bin:$PATH

# Tools the agent code / skills shell out to: git (worktrees), node+npm (Claude
# CLI runtime), python3 (slash commands / MCP helpers), ca-certs, tini (PID 1).
#
# Team edition disables one-click system-deps install (NIUNIU_EDITION=team), so
# anything an agent might need at runtime MUST be baked in here — it cannot be
# apt-installed from inside the running container. Added 2026-06 (issue #490,
# "team 服务器图片慢 / LibreOffice 未安装"):
#   - tesseract-ocr + eng/chi_sim/chi_tra packs: the read_image MCP tool OCRs
#     images to text when tesseract is present; without it read_image degrades
#     to (slower, token-heavy) model vision. This is the team-side half of the
#     read_image speedup — the in-process downsample-before-OCR is the other.
#   - libreoffice-impress/writer/calc + fonts-noto-cjk: the pptx/docx/xlsx agent
#     skills render Office docs via `soffice --headless`; without LibreOffice
#     they error ("未安装导致无法渲染"), and without CJK fonts Chinese slides
#     render as tofu boxes. Installed with --no-install-recommends to skip the
#     GUI/Java stack and keep the layer as lean as a full office suite allows.
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
        git \
        curl \
        tini \
        python3 \
        python3-pip \
        tesseract-ocr \
        tesseract-ocr-eng \
        tesseract-ocr-chi-sim \
        tesseract-ocr-chi-tra \
        libreoffice-impress \
        libreoffice-writer \
        libreoffice-calc \
        fonts-noto-cjk \
    && curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && npm install -g @anthropic-ai/claude-code \
    && npm install -g @openai/codex \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

# Extra language toolchains agents need to build cloned repos. Team edition
# can't apt-install at runtime (NIUNIU_EDITION=team), so bake them in:
#   - Java 21 (Eclipse Temurin) — Debian bookworm main only ships JDK 17, so
#     pull 21 from the Adoptium apt repo.
#   - Maven + Go — installed from their official tarballs (not Debian's `maven`
#     package, which would drag in a second, older JDK as a dependency).
# A build-time `java/mvn/go` version check fails the build early if any download
# or package name is wrong, so a broken image is never shipped.
# Kept byte-for-byte in sync with deploy/self/server-img/Dockerfile.
ENV JAVA_HOME=/usr/lib/jvm/temurin-21-jdk-amd64
ENV MAVEN_VERSION=3.9.16
ENV MAVEN_HOME=/opt/maven
ENV GO_VERSION=1.26.4
ENV PATH=/usr/local/go/bin:/opt/maven/bin:$JAVA_HOME/bin:$PATH
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends gnupg; \
    install -d -m 0755 /etc/apt/keyrings; \
    curl -fsSL https://packages.adoptium.net/artifactory/api/gpg/key/public \
      | gpg --dearmor -o /etc/apt/keyrings/adoptium.gpg; \
    echo "deb [signed-by=/etc/apt/keyrings/adoptium.gpg] https://packages.adoptium.net/artifactory/deb bookworm main" \
      > /etc/apt/sources.list.d/adoptium.list; \
    apt-get update; \
    apt-get install -y --no-install-recommends temurin-21-jdk; \
    { curl -fsSL --retry 3 --retry-delay 2 "https://dlcdn.apache.org/maven/maven-3/${MAVEN_VERSION}/binaries/apache-maven-${MAVEN_VERSION}-bin.tar.gz" -o /tmp/maven.tar.gz \
      || curl -fsSL --retry 3 --retry-delay 2 "https://archive.apache.org/dist/maven/maven-3/${MAVEN_VERSION}/binaries/apache-maven-${MAVEN_VERSION}-bin.tar.gz" -o /tmp/maven.tar.gz; }; \
    tar -xzf /tmp/maven.tar.gz -C /opt; \
    ln -sfn "/opt/apache-maven-${MAVEN_VERSION}" /opt/maven; \
    rm -f /tmp/maven.tar.gz; \
    curl -fsSL --retry 3 --retry-delay 2 "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" -o /tmp/go.tar.gz; \
    tar -C /usr/local -xzf /tmp/go.tar.gz; \
    rm -f /tmp/go.tar.gz; \
    apt-get clean; \
    rm -rf /var/lib/apt/lists/*; \
    java -version; mvn -version; go version

# Non-root user; HOME=/data so server's `os.UserHomeDir()` resolves into the
# mounted volume (config.yaml, workspaces/, auth_secret all land there).
# Pre-create /data/.niuniu so the compose-level bind mount for config.yaml
# lands inside a dir owned by niuniu — otherwise Docker auto-creates the
# parent as root and auth_secret / config.yaml writes fail.
RUN useradd --system --create-home --home-dir /data --uid 10001 niuniu \
 && mkdir -p /data/.niuniu/workspaces \
 && chown -R niuniu:niuniu /data /usr/local/npm-global
ENV HOME=/data
ENV NIUNIU_EDITION=team

COPY --from=go-build /out/niuniu-server /usr/local/bin/niuniu-server
COPY --from=go-build /out/niuniu-mcp /usr/local/bin/niuniu-mcp

USER niuniu
WORKDIR /data
VOLUME ["/data/.niuniu"]
EXPOSE 3000

ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/niuniu-server"]
