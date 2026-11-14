#!/bin/sh
# Installs AgentTunnel from a GitHub release.
#
# On macOS this installs AgentTunnel.app into ~/Applications (so it appears in
# Spotlight/Launchpad with its icon) and links agenttunnel-desktop into
# ~/.local/bin. Every platform gets the atd daemon and the agenttunnel CLI.
#
# --server (Linux only) installs a headless daemon instead: just atd and the
# agenttunnel CLI, a dedicated state directory, a systemd unit, and a sample
# Caddyfile that puts TLS in front of atd. Nothing is started; the script
# prints the commands to run next.
#
# Usage:
#   curl -fsSL https://github.com/agenttunnel/AgentTunnel/releases/latest/download/install-agenttunnel-go.sh | sh
#   curl -fsSL https://github.com/agenttunnel/AgentTunnel/releases/latest/download/install-agenttunnel-go.sh | sudo sh -s -- --server
#
# Environment:
#   AGENTTUNNEL_VERSION      Release tag to install (default: latest).
#   AGENTTUNNEL_INSTALL_DIR  CLI directory (default: ~/.local/bin; /usr/local/bin with --server).
#   AGENTTUNNEL_APP_DIR      macOS app directory (default: ~/Applications).
#   AGENTTUNNEL_REPO         GitHub repository (default: agenttunnel/AgentTunnel).
#   AGENTTUNNEL_BASE_URL     Override the release download base URL.
#
# --server only:
#   AGENTTUNNEL_STATE_DIR    State directory (default: /var/lib/agenttunnel).
#   AGENTTUNNEL_USER         Service account that runs atd (default: agenttunnel).
#                            Created when running as root and missing.
#   AGENTTUNNEL_SYSTEMD_DIR  Where atd.service is written (default: /etc/systemd/system).
#   AGENTTUNNEL_CONFIG_DIR   Where Caddyfile.sample is written (default: /etc/agenttunnel).
set -eu

server=0
for arg in "$@"; do
  case "$arg" in
    --server) server=1 ;;
    *)
      echo "install: unknown argument: $arg" >&2
      exit 2
      ;;
  esac
done

repo="${AGENTTUNNEL_REPO:-agenttunnel/AgentTunnel}"
version="${AGENTTUNNEL_VERSION:-latest}"
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

if [ "$server" -eq 1 ] && [ "$os" != "linux" ]; then
  echo "install: --server is supported on Linux only (this is ${os})" >&2
  exit 1
fi

case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *)
    echo "install: unsupported architecture: $(uname -m)" >&2
    exit 1
    ;;
esac

if [ "$server" -eq 1 ]; then
  default_install_dir=/usr/local/bin
else
  default_install_dir="${HOME:-/tmp}/.local/bin"
fi
install_dir="${AGENTTUNNEL_INSTALL_DIR:-$default_install_dir}"

if [ -n "${AGENTTUNNEL_BASE_URL:-}" ]; then
  base="${AGENTTUNNEL_BASE_URL%/}"
elif [ "$version" = "latest" ]; then
  base="https://github.com/${repo}/releases/latest/download"
else
  base="https://github.com/${repo}/releases/download/${version}"
fi

# setup_server writes the systemd unit, the state directory, and the sample
# Caddyfile for a headless atd. $1 is the directory atd was installed into.
setup_server() {
  bin_dir="$1"
  state_dir="${AGENTTUNNEL_STATE_DIR:-/var/lib/agenttunnel}"
  systemd_dir="${AGENTTUNNEL_SYSTEMD_DIR:-/etc/systemd/system}"
  config_dir="${AGENTTUNNEL_CONFIG_DIR:-/etc/agenttunnel}"
  service_user="${AGENTTUNNEL_USER:-agenttunnel}"
  unit="${systemd_dir}/atd.service"
  caddyfile="${config_dir}/Caddyfile.sample"
  root=0
  if [ "$(id -u)" -eq 0 ]; then root=1; fi

  if [ "$root" -eq 1 ] && ! id "$service_user" >/dev/null 2>&1; then
    if ! useradd --system --home-dir "$state_dir" --shell /usr/sbin/nologin "$service_user"; then
      echo "install: cannot create service user ${service_user}" >&2
      exit 1
    fi
    echo "install: created system user ${service_user}"
  fi

  # The state dir holds the token, provider logins (HOME is the state dir for
  # the service user), and thread data. Only the owner may read it.
  if ! mkdir -p "$state_dir/workspace" "$systemd_dir" "$config_dir"; then
    echo "install: cannot create ${state_dir}, ${systemd_dir}, or ${config_dir}" >&2
    echo "install: run as root, or set AGENTTUNNEL_STATE_DIR, AGENTTUNNEL_SYSTEMD_DIR, AGENTTUNNEL_CONFIG_DIR" >&2
    exit 1
  fi
  chmod 0700 "$state_dir"
  if [ "$root" -eq 1 ]; then
    chown "$service_user:$service_user" "$state_dir" "$state_dir/workspace"
  fi

  cat >"$unit" <<EOF
# AgentTunnel daemon. Installed by install-agenttunnel-go.sh --server.
# atd binds loopback only; put a TLS reverse proxy (see Caddyfile.sample) in front.
[Unit]
Description=AgentTunnel daemon (atd)
Documentation=https://github.com/agenttunnel/AgentTunnel
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${service_user}
Group=${service_user}
ExecStart=${bin_dir}/atd --host 127.0.0.1 --port 7331 --home-dir ${state_dir} --workdir ${state_dir}/workspace
Restart=on-failure
RestartSec=5
NoNewPrivileges=yes
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
EOF
  chmod 0644 "$unit"

  cat >"$caddyfile" <<'EOF'
# Sample Caddyfile: TLS reverse proxy in front of atd.
#
# Copy this into your Caddy config (usually /etc/caddy/Caddyfile), replace the
# host name, and reload Caddy. Caddy obtains the TLS certificate itself.
#
# atd listens on 127.0.0.1:7331 only. Caddy connects from loopback, which is
# what lets clients pass the token as ?token= (browser clients cannot set an
# Authorization header on a WebSocket). CLI clients should send
# "Authorization: Bearer <token>" instead.
#
# Caddy logs the request URI by default, and that includes ?token=. Redact it
# in the log block below (unverified against a Caddy binary; check it with
# `caddy validate` before relying on it).

atd.example.com {
	reverse_proxy 127.0.0.1:7331

	# log {
	# 	output file /var/log/caddy/atd-access.log
	# 	format filter {
	# 		wrap json
	# 		fields {
	# 			request>uri query {
	# 				replace token REDACTED
	# 			}
	# 		}
	# 	}
	# }
}
EOF
  chmod 0644 "$caddyfile"

  echo "install: wrote ${unit}"
  echo "install: wrote ${caddyfile}"
  echo "install: state directory ${state_dir} (the token is created on first start at ${state_dir}/token, mode 0600)"
  echo "next:"
  echo "  1. Log each provider CLI in as ${service_user} on this host (its HOME is ${state_dir})."
  echo "  2. sudo systemctl daemon-reload && sudo systemctl enable --now atd"
  echo "  3. Edit ${caddyfile}, copy it into your Caddy config, and reload Caddy."
  echo "  4. Read the token with: sudo cat ${state_dir}/token"
}

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
  # A headless server has no desktop shell to run.
  if [ "$server" -eq 1 ] && [ "$name" = "agenttunnel-desktop" ]; then
    continue
  fi
  for candidate in "$name" "$name.exe"; do
    if [ -f "$tmpdir/$candidate" ]; then
      if [ "$name" = "agenttunnel-desktop" ] && [ -n "$app_binary" ] && [ -x "$app_binary" ]; then
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

if [ "$server" -eq 1 ]; then
  setup_server "$install_dir"
  exit 0
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
