#!/usr/bin/env bash
# Package a Wails Linux binary into a distributable tar.gz archive with a
# .desktop file and icon, so users can extract and run it directly.
#
# Output:
#   <output-dir>/<artifact-base>-linux-<arch>.tar.gz   (final distribution artifact)
#
# The tar.gz contains:
#   niuniu-desktop-<version>/
#   ├── niuniu-desktop          (the binary)
#   ├── niuniu-desktop.desktop  (desktop entry, users can copy to ~/.local/share/applications/)
#   ├── icon.png                (app icon, 256px PNG)
#   └── install.sh              (quick install script)

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

PKG_DIR="$OUTDIR/${ARTIFACT_BASE}-linux-${ARCH}"
TGZ_PATH="$OUTDIR/${ARTIFACT_BASE}-linux-${ARCH}.tar.gz"

rm -rf "$PKG_DIR" "$TGZ_PATH"
mkdir -p "$PKG_DIR"

# ── Binary ──────────────────────────────────────────────────────────────
cp "$BINARY" "$PKG_DIR/niuniu-desktop"
chmod +x "$PKG_DIR/niuniu-desktop"

# ── Icon ────────────────────────────────────────────────────────────────
cp "$ICON" "$PKG_DIR/icon.png"

# ── .desktop file ───────────────────────────────────────────────────────
# Users can copy this to ~/.local/share/applications/ for system integration.
cat > "$PKG_DIR/niuniu-desktop.desktop" <<DESKTOP
[Desktop Entry]
Version=1.0
Type=Application
Name=${DISPLAY_NAME}
Comment=AI Workstation — run parallel AI agent sessions
Exec=/opt/niuniu-desktop/niuniu-desktop
Icon=/opt/niuniu-desktop/icon.png
Categories=Development;Utility;
Terminal=false
StartupWMClass=niuniu-desktop
DESKTOP

# ── Install script ──────────────────────────────────────────────────────
cat > "$PKG_DIR/install.sh" <<'INSTALL'
#!/usr/bin/env bash
# Quick install: copies the binary and icon to /opt/niuniu-desktop/ and
# registers the .desktop file so it appears in the app launcher.
set -euo pipefail

INSTALL_DIR="/opt/niuniu-desktop"
SRC_DIR="$(cd "$(dirname "$0")" && pwd)"

echo "==> Installing Niuniu Desktop to ${INSTALL_DIR}"
sudo mkdir -p "${INSTALL_DIR}"
sudo cp "${SRC_DIR}/niuniu-desktop" "${INSTALL_DIR}/"
sudo cp "${SRC_DIR}/icon.png" "${INSTALL_DIR}/"
sudo chmod +x "${INSTALL_DIR}/niuniu-desktop"

# Update the .desktop file paths to point to the installed location
sed "s|/opt/niuniu-desktop|${INSTALL_DIR}|g" "${SRC_DIR}/niuniu-desktop.desktop" > /tmp/niuniu-desktop.desktop
mkdir -p ~/.local/share/applications
cp /tmp/niuniu-desktop.desktop ~/.local/share/applications/
rm -f /tmp/niuniu-desktop.desktop

echo "==> Done. Find 'Niuniu Desktop' in your app launcher, or run:"
echo "    ${INSTALL_DIR}/niuniu-desktop"
INSTALL
chmod +x "$PKG_DIR/install.sh"

# ── Create tar.gz ───────────────────────────────────────────────────────
echo "==> Creating ${TGZ_PATH} ..."
tar -czf "$TGZ_PATH" -C "$OUTDIR" "$(basename "$PKG_DIR")"

echo "==> Built:"
echo "    ${TGZ_PATH}"
echo "    Size: $(du -h "$TGZ_PATH" | cut -f1)"