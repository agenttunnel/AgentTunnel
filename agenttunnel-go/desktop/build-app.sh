#!/usr/bin/env bash
#
# Builds AgentTunnel.app — the proper macOS bundle — from the binary build.sh
# already produces. With --install, replaces /Applications/AgentTunnel.app and
# links the terminal command into ~/.local/bin, so `agenttunnel <dir>` runs the
# same binary the bundle runs.
#
# Usage: ./build-app.sh [--install] [version]
set -euo pipefail

cd "$(dirname "$0")"

VERSION=""
INSTALL=0
for arg in "$@"; do
  case "$arg" in
    --install) INSTALL=1 ;;
    *) VERSION="$arg" ;;
  esac
done
if [ -z "$VERSION" ]; then
  VERSION="$(git describe --tags --always 2>/dev/null || echo dev)"
fi

if [ "$(uname -s)" != "Darwin" ]; then
  echo "build-app.sh only assembles a macOS bundle; run it on a Mac." >&2
  exit 1
fi

echo "› building binary"
./build.sh

APP="dist/AgentTunnel.app"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"

echo "› assembling $APP ($VERSION)"
cp bin/agenttunnel "$APP/Contents/MacOS/agenttunnel"
cp build/darwin/icons.icns "$APP/Contents/Resources/icons.icns"
[ -f build/darwin/Assets.car ] && cp build/darwin/Assets.car "$APP/Contents/Resources/Assets.car"

sed -e "s/<string>0\.1\.0<\/string>/<string>$VERSION<\/string>/g" \
    build/darwin/Info.plist > "$APP/Contents/Info.plist"

# Ad-hoc signature: no identity, no Gatekeeper prompt for a locally built app.
codesign --force --sign - "$APP" >/dev/null 2>&1 || true

if [ "$INSTALL" = 1 ]; then
  echo "› installing to /Applications"
  rm -rf "/Applications/AgentTunnel.app"
  cp -R "$APP" "/Applications/AgentTunnel.app"

  BIN_DIR="$HOME/.local/bin"
  mkdir -p "$BIN_DIR"
  ln -sf "/Applications/AgentTunnel.app/Contents/MacOS/agenttunnel" "$BIN_DIR/agenttunnel"
  case ":$PATH:" in
    *":$BIN_DIR:"*) ;;
    *) echo "  note: add $BIN_DIR to PATH to run \`agenttunnel\` from the terminal" ;;
  esac
fi

echo "› done: $(cd dist && pwd)/AgentTunnel.app"
