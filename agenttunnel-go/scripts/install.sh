#!/bin/sh
# Installs AgentTunnel from a GitHub release.
#
# On macOS this installs AgentTunnel.app into ~/Applications (so it appears in
# Spotlight/Launchpad with its icon) and links the terminal command into
# ~/.local/bin. Linux and Windows get the daemon and desktop binaries.
#
# Usage:
#   curl -fsSL https://github.com/agenttunnel/AgentTunnel/releases/latest/download/install-agenttunnel-go.sh | sh
#
# Environment:
#   AGENTTUNNEL_VERSION      Release tag to install (default: latest).
#   AGENTTUNNEL_INSTALL_DIR  CLI directory (default: ~/.local/bin).
#   AGENTTUNNEL_APP_DIR      macOS app directory (default: ~/Applications).
#   AGENTTUNNEL_REPO         GitHub repository (default: agenttunnel/AgentTunnel).
#   AGENTTUNNEL_BASE_URL     Override the release download base URL.
set -eu

repo="${AGENTTUNNEL_REPO:-agenttunnel/AgentTunnel}"
version="${AGENTTUNNEL_VERSION:-latest}"
install_dir="${AGENTTUNNEL_INSTALL_DIR:-${HOME:-/tmp}/.local/bin}"
app_dir="${AGENTTUNNEL_APP_DIR:-${HOME:-/tmp}/Applications}"

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  MINGW* | MSYS* | CYGWIN*) os=windows ;;
  *)
    echo "install: unsupported operating system: $(uname -s)" >&2
    exit 1
    ;;
esac

case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *)
    echo "install: unsupported architecture: $(uname -m)" >&2
    exit 1
    ;;
esac

if [ -n "${AGENTTUNNEL_BASE_URL:-}" ]; then
  base="${AGENTTUNNEL_BASE_URL%/}"
elif [ "$version" = "latest" ]; then
  base="https://github.com/${repo}/releases/latest/download"
else
  base="https://github.com/${repo}/releases/download/${version}"
fi

tmpdir="$(mktemp -d 2>/dev/null || mktemp -d -t agenttunnel)"
trap 'rm -rf "$tmpdir"' EXIT INT TERM

# Create the destination before any cp. A dangling ~/.local/bin symlink is the
# usual cause of "cp: ... No such file or directory" here.
if ! mkdir -p "$install_dir"; then
  echo "install: cannot create ${install_dir}" >&2
  echo "install: set AGENTTUNNEL_INSTALL_DIR to a writable directory" >&2
  exit 1
fi

# The desktop app bundle is what gives macOS its Dock/Launchpad icon. Older
# releases only shipped bare binaries, so treat it as best effort and fall back.
app_binary=""
if [ "$os" = "darwin" ]; then
  app_asset="AgentTunnel-darwin-${arch}.app.tar.gz"
  echo "install: downloading ${base}/${app_asset}"
  if curl -fsSL "${base}/${app_asset}" -o "$tmpdir/$app_asset"; then
    mkdir -p "$tmpdir/app"
    if tar -xzf "$tmpdir/$app_asset" -C "$tmpdir/app" && [ -d "$tmpdir/app/AgentTunnel.app" ]; then
      if ! mkdir -p "$app_dir"; then
        echo "install: cannot create ${app_dir}" >&2
        echo "install: set AGENTTUNNEL_APP_DIR to a writable directory" >&2
        exit 1
      fi
      rm -rf "$app_dir/AgentTunnel.app.new"
      cp -R "$tmpdir/app/AgentTunnel.app" "$app_dir/AgentTunnel.app.new"
      rm -rf "$app_dir/AgentTunnel.app"
      mv "$app_dir/AgentTunnel.app.new" "$app_dir/AgentTunnel.app"
      xattr -dr com.apple.quarantine "$app_dir/AgentTunnel.app" 2>/dev/null || true
      app_binary="$app_dir/AgentTunnel.app/Contents/MacOS/agenttunnel"
      echo "install: installed app into ${app_dir}/AgentTunnel.app"
    else
      echo "install: app archive was not a valid bundle; installing the CLI only" >&2
    fi
  else
    echo "install: this release has no macOS app bundle; installing the CLI only" >&2
  fi
fi

asset="agenttunnel-go-${os}-${arch}.tar.gz"
url="${base}/${asset}"
echo "install: downloading ${url}"
if ! curl -fsSL "$url" -o "$tmpdir/$asset"; then
  echo "install: no Go build for ${os}/${arch} at ${url}" >&2
  exit 1
fi

tar -xzf "$tmpdir/$asset" -C "$tmpdir"

installed=0
for name in atd agenttunnel at; do
  for candidate in "$name" "$name.exe"; do
    if [ -f "$tmpdir/$candidate" ]; then
      if [ "$name" = "agenttunnel" ] && [ -n "$app_binary" ]; then
        ln -sf "$app_binary" "$install_dir/agenttunnel"
      else
        cp "$tmpdir/$candidate" "$install_dir/$candidate"
        chmod 0755 "$install_dir/$candidate"
      fi
      installed=1
    fi
  done
done

if [ "$installed" -eq 0 ]; then
  echo "install: archive did not contain atd, agenttunnel, or at" >&2
  exit 1
fi

echo "install: installed into ${install_dir}"
if [ "$os" = "darwin" ] && [ -n "$app_binary" ]; then
  echo "install: launch AgentTunnel from ${app_dir}/AgentTunnel.app"
fi
case ":${PATH:-}:" in
  *":${install_dir}:"*) ;;
  *)
    echo "install: add ${install_dir} to PATH, for example:"
    echo "  export PATH=\"${install_dir}:\$PATH\""
    ;;
esac
