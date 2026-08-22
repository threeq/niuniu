#!/usr/bin/env bash
# Package a Wails Linux binary into a portable AppImage — a single self-mounting
# executable users can `chmod +x` and double-click to run, with launcher
# integration baked in. This is the Linux equivalent of the macOS .dmg.
#
# Output:
#   <output-dir>/<artifact-base>-linux-<arch>.AppImage   (final distribution artifact)
#
# An AppImage is built from an AppDir staging directory:
#   AppDir/
#   ├── niuniu-desktop           (the ELF binary produced by go build)
#   ├── niuniu-desktop.desktop   (desktop entry — drives launcher integration)
#   ├── niuniu-desktop.png       (app icon, named to match the desktop Icon=)
#   └── AppRun                   (bootstrap: cd's into the mounted AppDir, exec)
# then appimagetool bundles AppDir into the single .AppImage file.
#
# Note: this bundles the app binary only — GTK/WebKit2GTK come from the user's
# system, same contract as a .deb or Wails' own `wails3 package`. The build host
# must have appimagetool (auto-downloaded to <output-dir> if not found) and, on
# FUSE-less hosts (e.g. GitHub Actions containers), it is run with
# APPIMAGE_EXTRACT_AND_RUN=1.

set -euo pipefail

usage() {
    cat <<EOF
Usage: $0 \\
    --binary <path>              \\  # the ELF binary produced by go build
    --icon <path>                \\  # PNG icon (256px recommended)
    --display-name <"Niuniu Desktop">  \\  # used in .desktop Name=
    --version <vX.Y.Z>           \\
    --arch <amd64|arm64>         \\
    --artifact-base <niuniu-desktop-vX.Y.Z>  \\  # filename stem
    --output-dir <bin/>
EOF
    exit 1
}

BINARY="" ICON="" DISPLAY_NAME="" VERSION="" ARCH="" ARTIFACT_BASE="" OUTDIR=""

while [[ $# -gt 0 ]]; do
    case $1 in
        --binary) BINARY=$2; shift 2;;
        --icon) ICON=$2; shift 2;;
        --display-name) DISPLAY_NAME=$2; shift 2;;
        --version) VERSION=$2; shift 2;;
        --arch) ARCH=$2; shift 2;;
        --artifact-base) ARTIFACT_BASE=$2; shift 2;;
        --output-dir) OUTDIR=$2; shift 2;;
        -h|--help) usage;;
        *) echo "Unknown arg: $1" >&2; usage;;
    esac
done

for v in BINARY ICON DISPLAY_NAME VERSION ARCH ARTIFACT_BASE OUTDIR; do
    if [[ -z "${!v}" ]]; then
        echo "Missing required arg: --${v,,}" >&2
        usage
    fi
done

[[ -f "$BINARY" ]] || { echo "Binary not found: $BINARY" >&2; exit 1; }
[[ -f "$ICON"   ]] || { echo "Icon not found: $ICON"     >&2; exit 1; }

mkdir -p "$OUTDIR"

APPDIR="$OUTDIR/AppDir"
APPIMAGE_PATH="$OUTDIR/${ARTIFACT_BASE}-linux-${ARCH}.AppImage"
ICON_NAME="niuniu-desktop"

# ── Locate appimagetool (prefer env override, then PATH, then download) ──────
APPIMAGETOOL="${APPIMAGETOOL:-}"
if [[ -z "$APPIMAGETOOL" ]]; then
    if command -v appimagetool >/dev/null 2>&1; then
        APPIMAGETOOL="$(command -v appimagetool)"
    else
        TOOL_PATH="$OUTDIR/.appimagetool-x86_64.AppImage"
        if [[ ! -f "$TOOL_PATH" ]]; then
            echo "==> Downloading appimagetool ..."
            curl -fL -o "$TOOL_PATH" \
                "https://github.com/AppImage/appimagetool/releases/download/1.9.1/appimagetool-x86_64.AppImage"
        fi
        chmod +x "$TOOL_PATH"
        APPIMAGETOOL="$TOOL_PATH"
    fi
fi
echo "==> Using appimagetool: $APPIMAGETOOL"

# ── Build the AppDir staging directory ───────────────────────────────────────
rm -rf "$APPDIR" "$APPIMAGE_PATH"
mkdir -p "$APPDIR"

# Binary
cp "$BINARY" "$APPDIR/niuniu-desktop"
chmod +x "$APPDIR/niuniu-desktop"

# Icon — named <Icon=value>.png so appimagetool picks it up as the app icon
cp "$ICON" "$APPDIR/${ICON_NAME}.png"

# .desktop entry — Icon= has no extension (AppImage convention); appimagetool
# rewrites Exec= to point at the bundled AppImage at package time.
cat > "$APPDIR/niuniu-desktop.desktop" <<DESKTOP
[Desktop Entry]
Version=1.0
Type=Application
Name=${DISPLAY_NAME}
Comment=AI Workstation — run parallel AI agent sessions
Exec=niuniu-desktop
Icon=${ICON_NAME}
Categories=Development;Utility;
Terminal=false
StartupWMClass=niuniu-desktop
DESKTOP

# AppRun bootstrap — the AppImage is mounted read-only at an arbitrary path, so
# cd into the AppDir before exec so the binary finds its sibling files.
cat > "$APPDIR/AppRun" <<'RUN'
#!/usr/bin/env bash
HERE="$(cd "$(dirname "$(readlink -f "$0")")" && pwd)"
cd "$HERE"
exec ./niuniu-desktop "$@"
RUN
chmod +x "$APPDIR/AppRun"

# ── Bundle with appimagetool ─────────────────────────────────────────────────
# APPIMAGE_EXTRACT_AND_RUN=1 lets the AppImage-form appimagetool run on FUSE-less
# hosts (GitHub Actions containers). Harmless if appimagetool is a native exe.
echo "==> Creating ${APPIMAGE_PATH} ..."
APPIMAGE_EXTRACT_AND_RUN=1 "$APPIMAGETOOL" "$APPDIR" "$APPIMAGE_PATH"

echo "==> Built:"
echo "    ${APPIMAGE_PATH}"
echo "    Size: $(du -h "$APPIMAGE_PATH" | cut -f1)"
echo "    Users: chmod +x and run, or use AppImageLauncher for menu integration."
