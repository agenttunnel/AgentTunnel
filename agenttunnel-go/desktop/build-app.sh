#!/usr/bin/env bash
#
# Builds AgentTunnel.app — the proper macOS bundle — from the binary build.sh
# produces, and optionally packages a drag-to-Applications DMG.
#
# Usage: ./build-app.sh [--install] [--dmg] [--no-build] [--binary PATH]
#                       [--version VERSION] [--arch ARCH]
#
#   --install   copy the app to /Applications (or ~/Applications when that is
#               not writable) and link `agenttunnel` into ~/.local/bin.
#   --dmg       also build dist/AgentTunnel-darwin-<arch>.dmg.
#   --no-build  reuse an existing binary instead of running build.sh.
#   --binary    binary to bundle (default: bin/agenttunnel).
#
# The app archive the installer downloads is always written to
# dist/AgentTunnel-darwin-<arch>.app.tar.gz.
set -euo pipefail

cd "$(dirname "$0")"

INSTALL=0
DMG=0
BUILD=1
BINARY="bin/agenttunnel"
VERSION=""
ARCH=""

need_value() {
  if [ $# -lt 2 ] || [ -z "$2" ]; then
    echo "build-app.sh: $1 needs a value" >&2
    exit 2
  fi
}

while [ $# -gt 0 ]; do
  case "$1" in
    --install) INSTALL=1 ;;
    --dmg) DMG=1 ;;
    --no-build) BUILD=0 ;;
    --binary)
      need_value "$@"
      BINARY="$2"
      shift
      ;;
    --version)
      need_value "$@"
      VERSION="$2"
      shift
      ;;
    --arch)
      need_value "$@"
      ARCH="$2"
      shift
      ;;
    -h | --help)
      sed -n '2,16p' "$0"
      exit 0
      ;;
    *)
      echo "build-app.sh: unknown argument: $1" >&2
      exit 2
      ;;
  esac
  shift
done

if [ "$(uname -s)" != "Darwin" ]; then
  echo "build-app.sh only assembles a macOS bundle; run it on a Mac." >&2
  exit 1
fi

if [ -z "$VERSION" ]; then
  VERSION="$(git describe --tags --always 2>/dev/null || echo dev)"
fi
# CFBundleVersion must be dot-separated integers. Release tags are already
# valid; local git-describe suffixes (v0.10.1-3-gabc) are not.
CF_VERSION="${VERSION#v}"
CF_VERSION="${CF_VERSION%%-*}"
case "$CF_VERSION" in
  "" | *[!0-9.]*) CF_VERSION="0.0.0" ;;
esac
if [ -z "$ARCH" ]; then
  case "$(uname -m)" in
    arm64 | aarch64) ARCH=arm64 ;;
    x86_64 | amd64) ARCH=amd64 ;;
    *) ARCH="$(uname -m)" ;;
  esac
fi

if [ "$BUILD" = 1 ]; then
  echo "› building binary"
  ./build.sh
fi

if [ ! -f "$BINARY" ]; then
  echo "build-app.sh: binary not found: $BINARY (build it first or pass --binary)" >&2
  exit 1
fi

mkdir -p dist
APP="dist/AgentTunnel.app"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"

echo "› assembling $APP ($VERSION, darwin/$ARCH)"
cp "$BINARY" "$APP/Contents/MacOS/agenttunnel"
cp build/darwin/icons.icns "$APP/Contents/Resources/icons.icns"

sed -e "s/<string>0\.1\.0<\/string>/<string>$CF_VERSION<\/string>/g" \
    build/darwin/Info.plist > "$APP/Contents/Info.plist"

# Ad-hoc signature: no identity, no Gatekeeper prompt for a locally built app.
codesign --force --sign - "$APP" >/dev/null 2>&1 || true

APP_TAR="dist/AgentTunnel-darwin-${ARCH}.app.tar.gz"
rm -f "$APP_TAR"
tar -czf "$APP_TAR" -C dist AgentTunnel.app
echo "› app archive: $APP_TAR"

if [ "$DMG" = 1 ]; then
  if ! command -v hdiutil >/dev/null 2>&1; then
    echo "build-app.sh: hdiutil is required to build a DMG" >&2
    exit 1
  fi
  DMG_PATH="dist/AgentTunnel-darwin-${ARCH}.dmg"
  STAGE="$(mktemp -d "${TMPDIR:-/tmp}/agenttunnel-dmg.XXXXXX")"
  trap 'rm -rf "$STAGE"' EXIT
  cp -R "$APP" "$STAGE/AgentTunnel.app"
  ln -s /Applications "$STAGE/Applications"
  echo "› packaging $DMG_PATH"
  hdiutil create -volname "AgentTunnel $VERSION" -srcfolder "$STAGE" \
    -ov -format UDZO "$DMG_PATH" >/dev/null
  rm -rf "$STAGE"
  trap - EXIT
  echo "› DMG: $DMG_PATH"
fi

if [ "$INSTALL" = 1 ]; then
  APP_DIR="${AGENTTUNNEL_APP_DIR:-}"
  if [ -z "$APP_DIR" ]; then
    APP_DIR="/Applications"
    if [ ! -d "$APP_DIR" ] || [ ! -w "$APP_DIR" ]; then
      APP_DIR="$HOME/Applications"
    fi
  fi
  mkdir -p "$APP_DIR"
  echo "› installing to $APP_DIR"
  rm -rf "$APP_DIR/AgentTunnel.app"
  cp -R "$APP" "$APP_DIR/AgentTunnel.app"
  xattr -dr com.apple.quarantine "$APP_DIR/AgentTunnel.app" 2>/dev/null || true

  BIN_DIR="${AGENTTUNNEL_INSTALL_DIR:-$HOME/.local/bin}"
  mkdir -p "$BIN_DIR"
  ln -sf "$APP_DIR/AgentTunnel.app/Contents/MacOS/agenttunnel" "$BIN_DIR/agenttunnel"
  case ":$PATH:" in
    *":$BIN_DIR:"*) ;;
    *) echo "  note: add $BIN_DIR to PATH to run \`agenttunnel\` from the terminal" ;;
  esac
fi

echo "› done: $(cd dist && pwd)/AgentTunnel.app"
