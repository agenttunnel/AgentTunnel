#!/bin/sh
# Installs AgentTunnel from a GitHub release.
#
# On macOS this installs AgentTunnel.app into ~/Applications (so it appears in
# Spotlight/Launchpad with its icon) and links agenttunnel-desktop into
# ~/.local/bin. Every platform gets the atd daemon and the agenttunnel CLI.
#
# Usage:
#   curl -fsSL https://github.com/agenttunnel/AgentTunnel/releases/latest/download/install-agenttunnel-go.sh | sh
#
# Install also registers atd as an always-on service (launchd, systemd --user,
# or Task Scheduler) through `agenttunnel daemon install`. Re-running the install
# updates in place and restarts that service so it runs the new atd. To uninstall,
# pass --uninstall. It stops and removes the service, then removes the binaries and
# the macOS app, but keeps ~/.agenttunnel (threads and settings):
#   curl -fsSL https://github.com/agenttunnel/AgentTunnel/releases/latest/download/install-agenttunnel-go.sh | sh -s -- --uninstall
#
# Environment:
#   AGENTTUNNEL_VERSION      Release tag to install (default: latest).
#   AGENTTUNNEL_INSTALL_DIR  CLI directory (default: ~/.local/bin).
#   AGENTTUNNEL_APP_DIR      macOS app directory (default: ~/Applications).
#   AGENTTUNNEL_REPO         GitHub repository (default: agenttunnel/AgentTunnel).
#   AGENTTUNNEL_BASE_URL     Override the release download base URL.
#   AGENTTUNNEL_NO_SERVICE   Set to 1 to skip registering the always-on service.
set -eu

repo="${AGENTTUNNEL_REPO:-agenttunnel/AgentTunnel}"
version="${AGENTTUNNEL_VERSION:-latest}"
install_dir="${AGENTTUNNEL_INSTALL_DIR:-${HOME:-/tmp}/.local/bin}"
app_dir="${AGENTTUNNEL_APP_DIR:-${HOME:-/tmp}/Applications}"

mode=install
for arg in "$@"; do
  case "$arg" in
    --uninstall) mode=uninstall ;;
    -h | --help)
      echo "usage: install.sh [--uninstall]"
      exit 0
      ;;
    *)
      echo "install: unknown option: $arg" >&2
      exit 2
      ;;
  esac
done

case "$(uname -s)" in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  MINGW* | MSYS* | CYGWIN*) os=windows ;;
  *)
    echo "install: unsupported operating system: $(uname -s)" >&2
    exit 1
    ;;
esac

# uninstall removes the files install puts on disk. It leaves the state
# directory (~/.agenttunnel) alone: threads and settings live there.
uninstall() {
  echo "uninstall: quit AgentTunnel and any running atd first; deleting files does not stop them."
  failed=0

  # Remove the always-on service while its binaries still exist, so launchd,
  # systemd, or Task Scheduler never relaunches a missing atd. If that fails,
  # keep the binaries: the service still points at them and uninstall can rerun.
  cli="$install_dir/agenttunnel"
  [ -f "$cli" ] || cli="$install_dir/agenttunnel.exe"
  if [ -x "$cli" ]; then
    if ! "$cli" daemon uninstall; then
      echo "uninstall: cannot remove the always-on service; binaries were kept. Fix the error above and run uninstall again." >&2
      return 1
    fi
  fi

  if [ "$os" = "darwin" ]; then
    for dir in "$app_dir" /Applications; do
      # Skip the fallback when the user pointed the installer elsewhere.
      if [ "$dir" = "/Applications" ] && [ -n "${AGENTTUNNEL_APP_DIR:-}" ]; then
        continue
      fi
      bundle="$dir/AgentTunnel.app"
      # Only remove a bundle that holds our desktop binary, never any app with a similar name.
      if [ -x "$bundle/Contents/MacOS/agenttunnel-desktop" ]; then
        if rm -rf "$bundle" 2>/dev/null; then
          echo "uninstall: removed $bundle"
        else
          echo "uninstall: cannot remove $bundle; try: sudo rm -rf \"$bundle\"" >&2
          failed=1
        fi
      fi
    done
  fi

  for name in atd agenttunnel agenttunnel-desktop; do
    for candidate in "$name" "$name.exe"; do
      target="$install_dir/$candidate"
      # -L catches the agenttunnel-desktop symlink into the app bundle; rm -f removes the link only.
      if [ -e "$target" ] || [ -L "$target" ]; then
        if rm -f "$target"; then
          echo "uninstall: removed $target"
        else
          echo "uninstall: cannot remove $target" >&2
          failed=1
        fi
      fi
    done
  done

  if [ "$failed" -ne 0 ]; then
    return 1
  fi
  echo "uninstall: AgentTunnel is removed. Threads and settings were kept in ${HOME:-/tmp}/.agenttunnel."
  echo "uninstall: to delete them too (this cannot be undone): rm -rf \"${HOME:-/tmp}/.agenttunnel\""
}

if [ "$mode" = uninstall ]; then
  uninstall
  exit $?
fi

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
      app_binary="$app_dir/AgentTunnel.app/Contents/MacOS/agenttunnel-desktop"
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
for name in atd agenttunnel agenttunnel-desktop; do
  for candidate in "$name" "$name.exe"; do
    if [ -f "$tmpdir/$candidate" ]; then
      if [ "$name" = "agenttunnel-desktop" ] && [ -n "$app_binary" ] && [ -x "$app_binary" ]; then
        # Same rule as cp below: an old link or file must not be written through.
        rm -f "$install_dir/agenttunnel-desktop"
        ln -sf "$app_binary" "$install_dir/agenttunnel-desktop"
      else
        # Drop any old link first: cp would write through a symlink into the app bundle.
        rm -f "$install_dir/$candidate"
        cp "$tmpdir/$candidate" "$install_dir/$candidate"
        chmod 0755 "$install_dir/$candidate"
      fi
      installed=1
    fi
  done
done

if [ "$installed" -eq 0 ]; then
  echo "install: archive did not contain atd, agenttunnel, or agenttunnel-desktop" >&2
  exit 1
fi

echo "install: installed into ${install_dir}"

# Register atd as an always-on service after the binaries are in place, so the
# service starts the files it will run. daemon install is idempotent: it rewrites
# the unit and restarts the service, so an upgrade runs the new atd.
cli="$install_dir/agenttunnel"
[ -f "$cli" ] || cli="$install_dir/agenttunnel.exe"
if [ "${AGENTTUNNEL_NO_SERVICE:-}" = 1 ]; then
  echo "install: always-on service skipped (AGENTTUNNEL_NO_SERVICE=1)"
elif [ -n "${AT_AGENT_ID:-}" ]; then
  # The CLI refuses service changes inside an agent session; registering the service is the user's call.
  echo "install: always-on service skipped: this is an agent session (AT_AGENT_ID is set)."
  echo "install: to register it, run from your own shell: $cli daemon install"
elif [ ! -x "$cli" ]; then
  echo "install: this release has no agenttunnel CLI, so the always-on service was not registered" >&2
else
  # Registration is optional: headless Linux, containers and WSL have no systemd --user session.
  # The binaries are already installed, so a failure warns and the install still succeeds.
  if ! "$cli" daemon install; then
    echo "install: warning: cannot register the always-on service; the binaries are installed and atd still runs on demand." >&2
    echo "install: fix the error above and run: $cli daemon install" >&2
    echo "install: or rerun with AGENTTUNNEL_NO_SERVICE=1 to skip the service." >&2
  fi
fi

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
