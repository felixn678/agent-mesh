#!/bin/sh
# agent-mesh installer (no sudo):
#
#   curl -fsSL https://raw.githubusercontent.com/felixn678/agent-mesh/main/scripts/install.sh | sh
#
# Downloads the release archive for this OS/arch, verifies it against the
# release's SHA256SUMS and installs the binary to ~/.local/bin.
#
# Environment:
#   AGENTMESH_VERSION        release tag to install (default: latest), e.g. v0.2.0
#   AGENTMESH_INSTALL_DIR    target directory (default: $HOME/.local/bin)
#   AGENTMESH_DOWNLOAD_BASE  directory URL holding the assets, overriding GitHub
#                            (mirrors and tests; file:// is allowed only here)
set -eu

repo=felixn678/agent-mesh
version=${AGENTMESH_VERSION:-latest}
install_dir=${AGENTMESH_INSTALL_DIR:-$HOME/.local/bin}

die() {
	printf 'agent-mesh install: %s\n' "$*" >&2
	exit 1
}

case $(uname -s) in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) die "unsupported OS $(uname -s) (supported: Linux, macOS); try: go install github.com/$repo/cmd/agent-mesh@latest" ;;
esac
case $(uname -m) in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) die "unsupported CPU $(uname -m) (supported: amd64, arm64)" ;;
esac

proto='=https'
if [ -n "${AGENTMESH_DOWNLOAD_BASE:-}" ]; then
	base=${AGENTMESH_DOWNLOAD_BASE%/}
	proto='=https,file'
elif [ "$version" = latest ]; then
	base=https://github.com/$repo/releases/latest/download
else
	case $version in
	v*) ;;
	*) version=v$version ;;
	esac
	base=https://github.com/$repo/releases/download/$version
fi

# fetch <url> <dest>: HTTPS only (no downgrade on redirect) unless overridden.
fetch() {
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL --proto "$proto" --proto-redir "$proto" -o "$2" "$1" || die "download failed: $1"
	elif command -v wget >/dev/null 2>&1; then
		case $1 in https://*) ;; *) die "wget supports only https URLs; install curl" ;; esac
		wget -q --https-only -O "$2" "$1" || die "download failed: $1"
	else
		die "need curl or wget"
	fi
}

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	elif command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | awk '{print $1}'
	else
		die "need sha256sum or shasum to verify the download"
	fi
}

asset=agent-mesh-$os-$arch.tar.gz
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

echo "downloading $asset ($version)"
fetch "$base/$asset" "$tmp/$asset"
fetch "$base/SHA256SUMS" "$tmp/SHA256SUMS"

want=$(awk -v f="$asset" '$2 == f || $2 == "*" f { print $1 }' "$tmp/SHA256SUMS")
[ -n "$want" ] || die "SHA256SUMS has no entry for $asset"
got=$(sha256 "$tmp/$asset")
[ "$got" = "$want" ] || die "checksum mismatch for $asset (expected $want, got $got); not installing"

tar -xzf "$tmp/$asset" -C "$tmp" agent-mesh || die "cannot unpack $asset"
mkdir -p "$install_dir"
# Copy then rename: atomic, and safe while an older agent-mesh is running.
cp "$tmp/agent-mesh" "$install_dir/.agent-mesh.new.$$"
chmod 0755 "$install_dir/.agent-mesh.new.$$"
mv -f "$install_dir/.agent-mesh.new.$$" "$install_dir/agent-mesh"

installed=$("$install_dir/agent-mesh" version 2>/dev/null || echo unknown)
echo "installed agent-mesh $installed to $install_dir/agent-mesh"

case ":$PATH:" in
*":$install_dir:"*) ;;
*)
	echo
	echo "warning: $install_dir is not on your PATH. Add it, e.g.:"
	echo "  echo 'export PATH=\"$install_dir:\$PATH\"' >> ~/.profile   # or ~/.zshrc on macOS"
	;;
esac

cat <<EOF

Next steps:
  hub machine:     agent-mesh up
  other machines:  agent-mesh join <hub-url> <bundle>   (run "agent-mesh invite <name>" on the hub)
  then, on each:   agent-mesh setup-claude && agent-mesh doctor
EOF
