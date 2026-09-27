#!/usr/bin/env bash
#
# fetch-ffmpeg.sh — download the static ffmpeg + ffprobe binaries that get
# embedded into niuniu-video-mcp and stage them under
# server/internal/ffmpegbin/dist/<goos>-<goarch>/, where the `ffmpeg_bundled`
# build tag picks them up (see server/internal/ffmpegbin/embed_bundled.go).
# `make ffmpeg-stage` is the intended entry point.
#
# Usage:
#   scripts/fetch-ffmpeg.sh <goos> <goarch> [--dry-run] [--force]
#
#   <goos>      windows | linux | darwin
#   <goarch>    amd64 | arm64
#   --dry-run   print the URLs and target paths only; download nothing
#   --force     re-download even when both binaries are already staged
#
# Proxies
# -------
# Downloads go through curl, which honours HTTPS_PROXY/HTTP_PROXY (lowercase
# forms too). On networks where github.com itself is unreachable but a local
# proxy is running, point curl at it — e.g.:
#   HTTPS_PROXY=http://127.0.0.1:7890 make ffmpeg-stage
# (Verified 2026-09-27: direct connection timed out, the same command through
# a local proxy staged windows/amd64 in ~15s.)
#
# Output: server/internal/ffmpegbin/dist/<goos>-<goarch>/{ffmpeg,ffprobe}[.exe]
# dist/ is gitignored. Everything under the target directory is embedded
# verbatim, so stage only the platform you are building — remove stale
# platform dirs if you cross-build several targets from one checkout.
#
# Sources (and their risks)
# -------------------------
#   windows/amd64, linux/amd64, linux/arm64
#     BtbN/FFmpeg-Builds — the de-facto community distribution of FFmpeg's own
#     git master (gpl builds). The `latest` release tag is a rolling tag whose
#     asset names are stable, so these URLs do not rot; the *content* drifts
#     with upstream master. That drift is why the extraction directory is keyed
#     by the payload's content fingerprint rather than a version string.
#
#   darwin/amd64
#     evermeet.cx "getrelease" zips. RISK: community, unofficial, and x86_64
#     only — on Apple Silicon this binary runs under Rosetta 2. The download
#     redirects to a mirror on a non-standard port (e.deolaha.ca:4242 when this
#     comment was written), which some corporate networks block.
#
#   darwin/arm64
#     osxexperts.net zips. RISK: community, unofficial, and the filenames embed
#     the FFmpeg release version (ffmpeg71arm.zip == FFmpeg 7.1), so they ROT:
#     when upstream publishes a new build these pinned URLs start returning
#     404 until this script is updated (see OSXEXPERTS_* below — both can be
#     overridden by environment). Version drift against the evermeet source is
#     therefore expected, not a bug.
#
#     macOS Gatekeeper: these zips are unsigned/notarised by nobody, so the
#     binaries may need an explicit user approval (or `xattr -d
#     com.apple.quarantine`) before macOS will execute them — verify on a real
#     macOS box before a release.
#
# Requirements: curl, plus unzip or a zip-capable tar (bsdtar; Git Bash, macOS
# and most BSDs qualify). All downloads land in a temp dir first; dist/ is only
# touched after every archive has been fetched and unpacked, so a failed run
# leaves the previous staging intact.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
DIST_ROOT="$ROOT/server/internal/ffmpegbin/dist"

# BtbN/FFmpeg-Builds: the rolling `latest` release tag. Overridable so a
# mirror (or a test fixture) can stand in for github.com.
BTBN_BASE="${BTBN_BASE:-https://github.com/BtbN/FFmpeg-Builds/releases/download/latest}"

# evermeet.cx: one zip per tool; /getrelease/ redirects to the current build.
EVERMEET_FFMPEG_URL="${EVERMEET_FFMPEG_URL:-https://evermeet.cx/ffmpeg/getrelease/zip}"
EVERMEET_FFPROBE_URL="${EVERMEET_FFPROBE_URL:-https://evermeet.cx/ffprobe/getrelease/zip}"

# osxexperts.net: filenames pin the FFmpeg release (71 == 7.1) and change when
# upstream bumps it — update these (or export the env vars) when they 404.
OSXEXPERTS_FFMPEG_URL="${OSXEXPERTS_FFMPEG_URL:-https://www.osxexperts.net/ffmpeg71arm.zip}"
OSXEXPERTS_FFPROBE_URL="${OSXEXPERTS_FFPROBE_URL:-https://www.osxexperts.net/ffprobe71arm.zip}"

# Static builds are tens of MB; anything smaller than this is an error page or
# a truncated download, not an executable.
MIN_SIZE_BYTES=1000000

usage() {
    cat <<'EOF'
fetch-ffmpeg.sh — stage static ffmpeg + ffprobe for embedding.

Usage:
  scripts/fetch-ffmpeg.sh <goos> <goarch> [--dry-run] [--force]

  <goos>      windows | linux | darwin
  <goarch>    amd64 | arm64
  --dry-run   print the URLs and target paths only; download nothing
  --force     re-download even when both binaries are already staged

Output: server/internal/ffmpegbin/dist/<goos>-<goarch>/{ffmpeg,ffprobe}[.exe]
EOF
}

die() { echo "fetch-ffmpeg: $*" >&2; exit 1; }

GOOS=""
GOARCH=""
DRY_RUN=0
FORCE=0

while [ $# -gt 0 ]; do
    case "$1" in
        --dry-run) DRY_RUN=1; shift ;;
        --force)   FORCE=1; shift ;;
        -h|--help) usage; exit 0 ;;
        -*)        die "unknown option '$1' (try --help)" ;;
        *)
            if [ -z "$GOOS" ]; then
                GOOS="$1"
            elif [ -z "$GOARCH" ]; then
                GOARCH="$1"
            else
                die "unexpected argument '$1' (try --help)"
            fi
            shift ;;
    esac
done

[ -n "$GOOS" ] && [ -n "$GOARCH" ] || { usage >&2; exit 2; }

# Per-platform download plan. LAYOUT=btbn: one archive holding bin/ffmpeg +
# bin/ffprobe; LAYOUT=flat: one archive per tool holding the binary at the
# archive root, URLS[0] -> ffmpeg, URLS[1] -> ffprobe. ARCHIVE_TYPE is passed
# to extract_archive explicitly because the evermeet URLs end in "/zip" with
# no usable extension.
LAYOUT=""
ARCHIVE_TYPE=""
URLS=()
case "$GOOS-$GOARCH" in
    windows-amd64)
        LAYOUT=btbn
        ARCHIVE_TYPE=zip
        URLS=("$BTBN_BASE/ffmpeg-master-latest-win64-gpl.zip") ;;
    linux-amd64)
        LAYOUT=btbn
        ARCHIVE_TYPE=tarxz
        URLS=("$BTBN_BASE/ffmpeg-master-latest-linux64-gpl.tar.xz") ;;
    linux-arm64)
        LAYOUT=btbn
        ARCHIVE_TYPE=tarxz
        URLS=("$BTBN_BASE/ffmpeg-master-latest-linuxarm64-gpl.tar.xz") ;;
    darwin-amd64)
        LAYOUT=flat
        ARCHIVE_TYPE=zip
        URLS=("$EVERMEET_FFMPEG_URL" "$EVERMEET_FFPROBE_URL") ;;
    darwin-arm64)
        LAYOUT=flat
        ARCHIVE_TYPE=zip
        URLS=("$OSXEXPERTS_FFMPEG_URL" "$OSXEXPERTS_FFPROBE_URL") ;;
    *)
        die "unsupported platform '$GOOS-$GOARCH' (supported: windows-amd64, linux-amd64, linux-arm64, darwin-amd64, darwin-arm64)" ;;
esac

EXE=""
[ "$GOOS" = windows ] && EXE=".exe"
DEST="$DIST_ROOT/$GOOS-$GOARCH"
FFMPEG_OUT="$DEST/ffmpeg$EXE"
FFPROBE_OUT="$DEST/ffprobe$EXE"

already_staged=0
if [ -s "$FFMPEG_OUT" ] && [ -s "$FFPROBE_OUT" ]; then
    already_staged=1
fi

if [ "$DRY_RUN" = 1 ]; then
    echo "fetch-ffmpeg: dry run — $GOOS/$GOARCH"
    echo "  target dir: $DEST"
    echo "  target:     $FFMPEG_OUT"
    echo "  target:     $FFPROBE_OUT"
    for url in "${URLS[@]}"; do
        echo "  download:   $url"
    done
    if [ "$already_staged" = 1 ] && [ "$FORCE" != 1 ]; then
        echo "  note: both binaries already staged — a real run would skip (pass --force to re-download)"
    fi
    exit 0
fi

if [ "$already_staged" = 1 ] && [ "$FORCE" != 1 ]; then
    echo "fetch-ffmpeg: already staged, skipping (pass --force to re-download): $DEST"
    exit 0
fi

TMP="$(mktemp -d "${TMPDIR:-/tmp}/fetch-ffmpeg.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT

download() { # <url> <target-file>
    echo "fetch-ffmpeg: downloading $1"
    curl -fL --connect-timeout 20 --retry 3 --retry-delay 2 -o "$2" "$1"
}

extract_archive() { # <archive> <dest-dir> <zip|tarxz>
    mkdir -p "$2"
    case "$3" in
        zip)
            if command -v unzip >/dev/null 2>&1; then
                unzip -q -o "$1" -d "$2"
            elif tar -tf "$1" >/dev/null 2>&1; then
                # bsdtar (macOS, Windows) reads zip; GNU tar does not.
                tar -xf "$1" -C "$2"
            else
                die "cannot unpack $1: install unzip (or use a bsdtar-based tar)"
            fi ;;
        tarxz)
            tar -xf "$1" -C "$2" ;;
        *)
            die "unknown archive type '$3' for $1" ;;
    esac
}

locate_btbn() { # <extract-dir> <binary-name>
    find "$1" -type f -path "*/bin/$2" -print -quit
}

locate_flat() { # <extract-dir> <binary-name>
    find "$1" -type f \( -name "$2" -o -name "$2.exe" \) -print -quit
}

install_bin() { # <staged-source> <final-path>
    local size
    size=$(wc -c <"$1" | tr -d ' ')
    [ "$size" -ge "$MIN_SIZE_BYTES" ] ||
        die "refusing to stage $2: only $size bytes (expected >= $MIN_SIZE_BYTES; stale URL or error page?)"
    mkdir -p "$(dirname "$2")"
    cp "$1" "$2.tmp"
    chmod 0755 "$2.tmp" 2>/dev/null || true
    mv -f "$2.tmp" "$2"
}

# Every URL is a separate zip in the flat layout (one tool each); the btbn
# layout has a single archive with both tools.
extract_dir=()
i=0
for url in "${URLS[@]}"; do
    archive="$TMP/dl-$i.archive"
    d="$TMP/x-$i"
    download "$url" "$archive"
    extract_archive "$archive" "$d" "$ARCHIVE_TYPE"
    extract_dir[$i]="$d"
    i=$((i + 1))
done

staged_ffmpeg=""
staged_ffprobe=""
if [ "$LAYOUT" = btbn ]; then
    staged_ffmpeg="$(locate_btbn "${extract_dir[0]}" "ffmpeg$EXE")"
    staged_ffprobe="$(locate_btbn "${extract_dir[0]}" "ffprobe$EXE")"
else
    staged_ffmpeg="$(locate_flat "${extract_dir[0]}" "ffmpeg")"
    staged_ffprobe="$(locate_flat "${extract_dir[1]}" "ffprobe")"
fi
[ -n "$staged_ffmpeg" ] || die "no ffmpeg binary found in the downloaded archive(s)"
[ -n "$staged_ffprobe" ] || die "no ffprobe binary found in the downloaded archive(s)"

install_bin "$staged_ffmpeg" "$FFMPEG_OUT"
install_bin "$staged_ffprobe" "$FFPROBE_OUT"

echo "fetch-ffmpeg: staged $GOOS/$GOARCH"
echo "  $FFMPEG_OUT ($(du -h "$FFMPEG_OUT" | cut -f1))"
echo "  $FFPROBE_OUT ($(du -h "$FFPROBE_OUT" | cut -f1))"
