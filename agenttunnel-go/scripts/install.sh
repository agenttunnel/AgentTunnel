#!/bin/sh
# Installs the Go build of AgentTunnel from a GitHub release.
#
# The archive contains two binaries:
#   atd          the daemon (HTTP + WebSocket server)
#   agenttunnel  the Wails desktop client
#
# Usage:
#   curl -fsSL https://github.com/agenttunnel/AgentTunnel/releases/latest/download/install-agenttunnel-go.sh | sh
#
# Environment:
#   AGENTTUNNEL_VERSION      Release tag to install (default: latest).
#   AGENTTUNNEL_INSTALL_DIR  Directory to install into (default: ~/.local/bin).
#   AGENTTUNNEL_REPO         GitHub repository (default: agenttunnel/AgentTunnel).
set -eu

repo="${AGENTTUNNEL_REPO:-agenttunnel/AgentTunnel}"
version="${AGENTTUNNEL_VERSION:-latest}"
install_dir="${AGENTTUNNEL_INSTALL_DIR:-${HOME:-/tmp}/.local/bin}"

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

asset="agenttunnel-go-${os}-${arch}.tar.gz"
if [ "$version" = "latest" ]; then
  url="https://github.com/${repo}/releases/latest/download/${asset}"
else
  url="https://github.com/${repo}/releases/download/${version}/${asset}"
fi

tmpdir="$(mktemp -d 2>/dev/null || mktemp -d -t agenttunnel)"
trap 'rm -rf "$tmpdir"' EXIT INT TERM

echo "install: downloading ${url}"
if ! curl -fsSL "$url" -o "$tmpdir/$asset"; then
  echo "install: no Go build for ${os}/${arch} at ${url}" >&2
  exit 1
fi

tar -xzf "$tmpdir/$asset" -C "$tmpdir"

mkdir -p "$install_dir"
installed=0
for name in atd agenttunnel; do
  for candidate in "$name" "$name.exe"; do
    if [ -f "$tmpdir/$candidate" ]; then
      cp "$tmpdir/$candidate" "$install_dir/$candidate"
      chmod 0755 "$install_dir/$candidate"
      installed=1
    fi
  done
done

if [ "$installed" -eq 0 ]; then
  echo "install: archive did not contain atd or agenttunnel" >&2
  exit 1
fi

echo "install: installed into ${install_dir}"
case ":${PATH:-}:" in
  *":${install_dir}:"*) ;;
  *)
    echo "install: add ${install_dir} to PATH, for example:"
    echo "  export PATH=\"${install_dir}:\$PATH\""
    ;;
esac
