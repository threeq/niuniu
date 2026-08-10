#!/usr/bin/env bash
# Package a Wails Mach-O binary into a directly-runnable macOS .app bundle
# wrapped in a .dmg disk image.
#
# Output:
#   <output-dir>/<display-name>.app/                   (intermediate, lives inside the dmg too)
#   <output-dir>/<artifact-base>-darwin-<arch>.dmg     (final distribution artifact)
#
# Requires macOS host (uses hdiutil + sips). Unsigned by default — Gatekeeper
# will quarantine on first launch; users bypass via right-click → Open or
# `xattr -dr com.apple.quarantine <path>`.

set -euo pipefail

usage() {
    cat <<EOF
Usage: $0 \\
    --binary <path>            \\  # the Mach-O produced by go build
    --icon <path>              \\  # icon.icns
    --display-name <"Niuniu Personal">  \\  # used inside .app and as DMG volume name
    --identifier <com.niuniu.personal>  \\
    --version <vX.Y.Z>         \\
    --arch <arm64|amd64>       \\
    --artifact-base <niuniu-desktop-vX.Y.Z>  \\  # filename stem; full = <stem>-darwin-<arch>.dmg
    --output-dir <bin/>        \\
    [--ui-element]                # set LSUIElement=true (menu-bar-only apps like cmd/connect)
EOF
    exit 1
}

BINARY="" ICON="" DISPLAY_NAME="" IDENT="" VERSION="" ARCH="" ARTIFACT_BASE="" OUTDIR=""
UI_ELEMENT=0

while [[ $# -gt 0 ]]; do
    case $1 in
        --binary) BINARY=$2; shift 2;;
        --icon) ICON=$2; shift 2;;
        --display-name) DISPLAY_NAME=$2; shift 2;;
        --identifier) IDENT=$2; shift 2;;
        --version) VERSION=$2; shift 2;;
        --arch) ARCH=$2; shift 2;;
        --artifact-base) ARTIFACT_BASE=$2; shift 2;;
        --output-dir) OUTDIR=$2; shift 2;;
        --ui-element) UI_ELEMENT=1; shift;;
        -h|--help) usage;;
        *) echo "Unknown arg: $1" >&2; usage;;
    esac
done

for v in BINARY ICON DISPLAY_NAME IDENT VERSION ARCH ARTIFACT_BASE OUTDIR; do
    if [[ -z "${!v}" ]]; then
        echo "Missing required arg: --${v,,}" >&2
        usage
    fi
done

if [[ "$(uname -s)" != "Darwin" ]]; then
    echo "ERROR: macOS .app/.dmg packaging requires a macOS host (uname -s = $(uname -s))" >&2
    exit 1
fi

[[ -f "$BINARY" ]] || { echo "Binary not found: $BINARY" >&2; exit 1; }
[[ -f "$ICON"   ]] || { echo "Icon not found: $ICON"     >&2; exit 1; }

# ── Optional code-signing + notarization (Developer ID) ──────────────────────
# Everything below is gated on environment variables, so an unsigned local build
# (`make package-personal-darwin` on a dev Mac with no certs) behaves exactly as
# before. CI exports these from repo secrets — see .github/workflows/release-sync.yml.
#
#   MACOS_SIGN_IDENTITY  "Developer ID Application: Name (TEAMID)". If unset we
#                        auto-detect the first Developer ID Application identity
#                        in the keychain search list. Empty => unsigned build.
#   APPLE_API_KEY_PATH   path to the App Store Connect API key (.p8)
#   APPLE_API_KEY_ID     that key's Key ID
#   APPLE_API_ISSUER_ID  that key's Issuer ID
#
# Signing runs whenever an identity is resolved; notarization runs only when all
# three APPLE_API_* vars are present (and requires a signed bundle).
ENTITLEMENTS="$(cd "$(dirname "$0")" && pwd)/entitlements.plist"

# Resolve the signing identity from the keychain. We do NOT trust
# MACOS_SIGN_IDENTITY blindly: a hand-typed identity string that doesn't match
# the certificate's Common Name byte-for-byte makes codesign fail with "no
# identity found". So MACOS_SIGN_IDENTITY is honored only when it actually
# exists in the keychain; otherwise (wrong value, or unset) we auto-detect the
# first Developer ID Application identity that was imported. Empty => unsigned.
autodetect_identity() {
    security find-identity -v -p codesigning 2>/dev/null \
        | grep 'Developer ID Application' | head -1 \
        | sed -E 's/^[[:space:]]*[0-9]+\)[[:space:]]+[0-9A-F]+[[:space:]]+"(.*)"$/\1/'
}
SIGN_IDENTITY="${MACOS_SIGN_IDENTITY:-}"
if [[ -n "$SIGN_IDENTITY" ]] && ! security find-identity -v -p codesigning 2>/dev/null | grep -qF "$SIGN_IDENTITY"; then
    echo "==> MACOS_SIGN_IDENTITY not present in keychain; auto-detecting the imported Developer ID instead"
    SIGN_IDENTITY=""
fi
if [[ -z "$SIGN_IDENTITY" ]]; then
    SIGN_IDENTITY="$(autodetect_identity)" || true
fi
# Surface what the keychain actually holds — makes "no identity found" failures
# self-diagnosing in the CI log.
echo "==> codesigning identities available:"
security find-identity -v -p codesigning 2>&1 | sed 's/^/    /' || true

mkdir -p "$OUTDIR"

APP_DIR="$OUTDIR/${DISPLAY_NAME}.app"
DMG_PATH="$OUTDIR/${ARTIFACT_BASE}-darwin-${ARCH}.dmg"

rm -rf "$APP_DIR" "$DMG_PATH"
mkdir -p "$APP_DIR/Contents/MacOS" "$APP_DIR/Contents/Resources"

cp "$BINARY" "$APP_DIR/Contents/MacOS/${DISPLAY_NAME}"
chmod +x "$APP_DIR/Contents/MacOS/${DISPLAY_NAME}"
cp "$ICON" "$APP_DIR/Contents/Resources/icon.icns"

# Info.plist. CFBundleVersion must be a numeric build identifier (Apple
# Gatekeeper rejects strings like "v1.2.3-3-gabc1234-dirty" with a console
# warning), so we strip the leading "v" and any -gXXX/-dirty suffix for that
# field while keeping the full git-describe in CFBundleShortVersionString.
SEMVER_NUMERIC="$(echo "$VERSION" | sed -E 's/^v//; s/-[0-9]+-g[0-9a-f]+(-dirty)?$//; s/-dirty$//')"
[[ -z "$SEMVER_NUMERIC" ]] && SEMVER_NUMERIC="0.0.0"

{
    cat <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleDevelopmentRegion</key>
    <string>en</string>
    <key>CFBundleExecutable</key>
    <string>${DISPLAY_NAME}</string>
    <key>CFBundleIconFile</key>
    <string>icon.icns</string>
    <key>CFBundleIdentifier</key>
    <string>${IDENT}</string>
    <key>CFBundleInfoDictionaryVersion</key>
    <string>6.0</string>
    <key>CFBundleName</key>
    <string>${DISPLAY_NAME}</string>
    <key>CFBundleDisplayName</key>
    <string>${DISPLAY_NAME}</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>CFBundleShortVersionString</key>
    <string>${VERSION}</string>
    <key>CFBundleVersion</key>
    <string>${SEMVER_NUMERIC}</string>
    <key>LSMinimumSystemVersion</key>
    <string>10.15</string>
    <key>NSHighResolutionCapable</key>
    <true/>
    <key>NSSupportsAutomaticGraphicsSwitching</key>
    <true/>
PLIST
    if [[ "$UI_ELEMENT" == "1" ]]; then
        cat <<PLIST
    <key>LSUIElement</key>
    <true/>
PLIST
    fi
    cat <<PLIST
</dict>
</plist>
PLIST
} > "$APP_DIR/Contents/Info.plist"

# 8-byte legacy PkgInfo — Finder uses this to recognize the bundle as an app
# even before reading Info.plist. Modern macOS treats it as optional, but
# omitting it triggers a launch-services warning on older releases.
printf 'APPL????' > "$APP_DIR/Contents/PkgInfo"

# Strip the quarantine xattr that copying through some toolchains may apply,
# so a fresh DMG doesn't already carry it before the user even downloads.
xattr -cr "$APP_DIR" 2>/dev/null || true

# Code-sign the bundle (inside-out: executable first, then the .app wrapper) with
# the Hardened Runtime enabled, which notarization requires. The single embedded
# server/mcp Mach-O is shipped *inside* the main binary via go:embed, so the
# bundle has exactly one executable to sign here.
if [[ -n "$SIGN_IDENTITY" ]]; then
    echo "==> Code-signing with: $SIGN_IDENTITY"
    codesign --force --options runtime --timestamp \
        --entitlements "$ENTITLEMENTS" \
        --sign "$SIGN_IDENTITY" \
        "$APP_DIR/Contents/MacOS/${DISPLAY_NAME}"
    codesign --force --options runtime --timestamp \
        --entitlements "$ENTITLEMENTS" \
        --sign "$SIGN_IDENTITY" \
        "$APP_DIR"
    codesign --verify --strict --verbose=2 "$APP_DIR"
else
    echo "==> No Developer ID identity found; building UNSIGNED .app"
fi

# Stage for DMG: the .app + a symlink to /Applications so the user can drag.
STAGE_DIR="$(mktemp -d)"
cleanup() { rm -rf "$STAGE_DIR"; }
trap cleanup EXIT

cp -R "$APP_DIR" "$STAGE_DIR/"
ln -s /Applications "$STAGE_DIR/Applications"

VOLNAME="${DISPLAY_NAME} ${VERSION}"

hdiutil create \
    -volname "$VOLNAME" \
    -srcfolder "$STAGE_DIR" \
    -ov \
    -format UDZO \
    -fs HFS+ \
    "$DMG_PATH" >/dev/null

# Sign the DMG itself (the container the user downloads), then notarize + staple.
# We notarize the .dmg rather than the .app: submitting the dmg registers the
# contained app's cdhash with Apple too, and stapling the dmg means Gatekeeper
# clears it the moment it's mounted. (Note: the .app extracted from the dmg is
# signed but not individually stapled, so its very first launch does an online
# notarization check — fine for an internet-connected desktop tool.)
if [[ -n "$SIGN_IDENTITY" ]]; then
    codesign --force --timestamp --sign "$SIGN_IDENTITY" "$DMG_PATH"
fi

if [[ -n "${APPLE_API_KEY_PATH:-}" && -n "${APPLE_API_KEY_ID:-}" && -n "${APPLE_API_ISSUER_ID:-}" ]]; then
    if [[ -z "$SIGN_IDENTITY" ]]; then
        echo "ERROR: notarization requested but the bundle is unsigned (no Developer ID identity)" >&2
        exit 1
    fi
    echo "==> Notarizing $DMG_PATH (waits for Apple; usually 1-5 min)..."
    xcrun notarytool submit "$DMG_PATH" \
        --key "$APPLE_API_KEY_PATH" \
        --key-id "$APPLE_API_KEY_ID" \
        --issuer "$APPLE_API_ISSUER_ID" \
        --wait
    xcrun stapler staple "$DMG_PATH"
    xcrun stapler validate "$DMG_PATH"
    echo "==> Notarized + stapled."
else
    echo "==> Notarization env (APPLE_API_*) not set; skipping notarization"
fi

echo "==> Built:"
echo "    .app  $APP_DIR"
echo "    .dmg  $DMG_PATH"
