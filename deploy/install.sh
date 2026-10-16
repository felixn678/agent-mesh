#!/usr/bin/env bash
# Install agent-mesh for the current user. Usage: deploy/install.sh <hub|daemon|both> [binary]
# Touches only ~/.local/bin, ~/.config/agent-mesh, ~/.local/share/agent-mesh,
# ~/.config/systemd/user/agent-mesh-* (Linux) and ~/Library/LaunchAgents (macOS).
# Never touches ~/.claude*, never creates or copies secrets, never starts services.
set -euo pipefail

role="${1:-}"
case "$role" in hub | daemon | both) ;; *) echo "usage: $0 <hub|daemon|both> [binary]" >&2; exit 2 ;; esac

here="$(cd "$(dirname "$0")" && pwd)"
os="$(uname -s)"
arch="$(uname -m)"
case "$os/$arch" in
  Linux/x86_64) default_bin="linux-amd64" ;;
  Darwin/arm64) default_bin="darwin-arm64" ;;
  Darwin/x86_64) default_bin="darwin-amd64" ;;
  *) echo "unsupported platform $os/$arch" >&2; exit 1 ;;
esac
bin_src="${2:-$here/../dist/agent-mesh-$default_bin}"
[ -x "$bin_src" ] || { echo "binary not found: $bin_src (run: make cross)" >&2; exit 1; }
if [ "$role" != daemon ] && [ "$os" != Linux ]; then
  echo "the hub is supported on Linux only in v1" >&2; exit 1
fi

bin_dir="$HOME/.local/bin"
cfg_dir="$HOME/.config/agent-mesh"
data_dir="$HOME/.local/share/agent-mesh"
umask 077
mkdir -p "$bin_dir" "$cfg_dir" "$data_dir"
chmod 0700 "$cfg_dir" "$data_dir"

# Atomic replace so a running service keeps its old inode until restart.
install -m 0755 "$bin_src" "$bin_dir/agent-mesh.new"
mv -f "$bin_dir/agent-mesh.new" "$bin_dir/agent-mesh"

# Copy example configs only when absent: never overwrite user edits.
put_cfg() { # src dest
  if [ -e "$2" ]; then echo "keep existing $2"; else install -m 0600 "$1" "$2"; echo "created $2 (edit placeholders)"; fi
}
[ "$role" = daemon ] || put_cfg "$here/config/hub.example.toml" "$cfg_dir/hub.toml"
if [ "$role" != hub ]; then
  if [ "$os" = Darwin ]; then
    put_cfg "$here/config/daemon.mac.example.toml" "$cfg_dir/daemon.toml"
  else
    put_cfg "$here/config/daemon.ubuntu.example.toml" "$cfg_dir/daemon.toml"
  fi
fi

if [ "$os" = Linux ]; then
  unit_dir="$HOME/.config/systemd/user"
  mkdir -p "$unit_dir"
  [ "$role" = daemon ] || install -m 0644 "$here/systemd/agent-mesh-hub.service" "$unit_dir/"
  [ "$role" = hub ] || install -m 0644 "$here/systemd/agent-mesh-daemon.service" "$unit_dir/"
else
  la_dir="$HOME/Library/LaunchAgents"
  mkdir -p "$la_dir" "$HOME/Library/Logs/agent-mesh"
  sed "s|__HOME__|$HOME|g" "$here/launchd/com.agentmesh.daemon.plist" >"$la_dir/com.agentmesh.daemon.plist"
  chmod 0644 "$la_dir/com.agentmesh.daemon.plist"
fi

cat <<NEXT

Installed to $bin_dir/agent-mesh. Nothing was started. Next, manually:
  1. Create tokens (0600, kept out of the repo) and paste the printed hash lines into hub.toml:
       agent-mesh token new --admin --out $cfg_dir/admin.token
       agent-mesh token new --host <host-name> --out $cfg_dir/host.token
       agent-mesh token new --session <host-name> --out $cfg_dir/session.token
  2. Create $cfg_dir/gh.env (chmod 600) containing GH_TOKEN=<fine-grained PAT>.
  3. Edit $cfg_dir/*.toml: replace every <placeholder>.
NEXT
if [ "$os" = Linux ]; then
  units=""
  [ "$role" = daemon ] || units="agent-mesh-hub"
  [ "$role" = hub ] || units="$units agent-mesh-daemon"
  cat <<NEXT
  4. loginctl enable-linger $USER
  5. nmcli connection modify <connection-name> connection.autoconnect yes
  6. systemctl --user daemon-reload && systemctl --user enable --now $units
Claude Code integration is separate and opt-in: agent-mesh setup-claude --dry-run
NEXT
else
  cat <<NEXT
  4. launchctl bootstrap gui/\$(id -u) $HOME/Library/LaunchAgents/com.agentmesh.daemon.plist
Claude Code integration is separate and opt-in: agent-mesh setup-claude --dry-run
NEXT
fi
