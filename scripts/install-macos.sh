#!/usr/bin/env bash
#
# Installs an AgentTunnel macOS build from GitHub Releases and clears the
# Gatekeeper quarantine flag. Unsigned builds are rejected by macOS until that
# flag is removed, so a plain drag-and-drop install reports the app as damaged.
#
# Usage:
#   # Private repositories, with the GitHub CLI authenticated:
#   gh api repos/WaelRabah/AgentTunnel/contents/scripts/install-macos.sh \
#     -H "Accept: application/vnd.github.raw" | bash
#   # Or run it from a checkout:
#   ./scripts/install-macos.sh --version 0.3.0
#
# Options:
#   --version <version>  Install a specific version instead of the latest.
#   --dir <path>         Install into this directory (default: /Applications).
#   --repo <owner/repo>  GitHub repository to download from.
#   --dmg <path>         Install from a local disk image instead of downloading.
#   -h, --help           Show this help.
set -euo pipefail

show_help() {
  cat <<'EOF'
Installs an AgentTunnel macOS build from GitHub Releases and clears the
Gatekeeper quarantine flag. Unsigned builds are rejected by macOS until that
flag is removed, so a plain drag-and-drop install reports the app as damaged.

Usage:
  # Private repositories, with the GitHub CLI authenticated:
  gh api repos/WaelRabah/AgentTunnel/contents/scripts/install-macos.sh \
    -H "Accept: application/vnd.github.raw" | bash
  # Or run it from a checkout:
  ./scripts/install-macos.sh --version 0.3.0

Options:
  --version <version>  Install a specific version instead of the latest.
  --dir <path>         Install into this directory (default: /Applications).
  --repo <owner/repo>  GitHub repository to download from.
  --dmg <path>         Install from a local disk image instead of downloading.
  -h, --help           Show this help.
EOF
}

repo="WaelRabah/AgentTunnel"
version=""
install_dir="/Applications"
local_dmg=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version)
      version="${2:?--version requires a value}"
      version="${version#v}"
      shift 2
      ;;
    --dir)
      install_dir="${2:?--dir requires a path}"
      shift 2
      ;;
    --repo)
      repo="${2:?--repo requires an owner/repo value}"
      shift 2
      ;;
    --dmg)
      local_dmg="${2:?--dmg requires a path}"
      shift 2
      ;;
    -h | --help)
      show_help
      exit 0
      ;;
    *)
      echo "Unknown option: $1" >&2
      show_help >&2
      exit 2
      ;;
  esac
done

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "This installer is macOS-only." >&2
  exit 1
fi

case "$(uname -m)" in
  arm64) asset_arch="arm64" ;;
  x86_64) asset_arch="x64" ;;
  *)
    echo "Unsupported architecture: $(uname -m)" >&2
    exit 1
    ;;
esac

work_dir="$(mktemp -d)"
mount_point=""

cleanup() {
  if [[ -n "$mount_point" ]]; then
    hdiutil detach "$mount_point" -quiet >/dev/null 2>&1 || true
  fi
  rm -rf "$work_dir"
}
trap cleanup EXIT

# Private repositories have no anonymous release URLs, so prefer the GitHub CLI
# when it is installed and authenticated and fall back to plain HTTPS for
# public repositories.
gh_available() {
  command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1
}

resolve_latest_version() {
  if gh_available; then
    gh release view --repo "$repo" --json tagName --jq '.tagName' | sed 's/^v//'
    return
  fi
  curl -fsSL -H "User-Agent: agenttunnel-installer" \
    "https://api.github.com/repos/$repo/releases/latest" |
    sed -n 's/.*"tag_name":[[:space:]]*"v\{0,1\}\([^"]*\)".*/\1/p' | head -n 1
}

if [[ -n "$local_dmg" ]]; then
  dmg_path="$local_dmg"
  if [[ ! -f "$dmg_path" ]]; then
    echo "Disk image not found: $dmg_path" >&2
    exit 1
  fi
else
  if [[ -z "$version" ]]; then
    version="$(resolve_latest_version)"
    if [[ -z "$version" ]]; then
      echo "Could not determine the latest $repo release." >&2
      echo "If the repository is private, install and authenticate the GitHub CLI (gh auth login)." >&2
      exit 1
    fi
  fi

  asset="AgentTunnel-${version}-${asset_arch}.dmg"
  dmg_path="$work_dir/$asset"
  echo "Downloading $asset..."
  if gh_available; then
    gh release download "v${version}" --repo "$repo" --pattern "$asset" --dir "$work_dir" --clobber
  else
    curl -fL --progress-bar -H "User-Agent: agenttunnel-installer" -o "$dmg_path" \
      "https://github.com/$repo/releases/download/v${version}/${asset}" || {
      echo "Download failed. If $repo is private, authenticate the GitHub CLI (gh auth login)." >&2
      exit 1
    }
  fi
fi

mount_point="$work_dir/mount"
mkdir -p "$mount_point"
echo "Mounting $(basename "$dmg_path")..."
hdiutil attach "$dmg_path" -mountpoint "$mount_point" -nobrowse -quiet

app_path="$(find "$mount_point" -maxdepth 1 -name '*.app' -print -quit)"
if [[ -z "$app_path" ]]; then
  echo "No application bundle found in $(basename "$dmg_path")." >&2
  exit 1
fi

mkdir -p "$install_dir"
target="$install_dir/$(basename "$app_path")"
rm -rf "$target"
echo "Installing to $target..."
cp -R "$app_path" "$install_dir/"

hdiutil detach "$mount_point" -quiet
mount_point=""

echo "Clearing the quarantine flag..."
xattr -dr com.apple.quarantine "$target" || true

echo "Installed $(basename "$app_path"). Open it from $install_dir."
