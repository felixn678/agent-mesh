#!/bin/sh
# Build release archives: one agent-mesh-<os>-<arch>.tar.gz per platform plus
# SHA256SUMS. Asset names carry no version, so the installer can always use
# GitHub's releases/latest/download/<asset> URL.
#
#   scripts/build-release.sh <version> <out-dir>
#   PLATFORMS="linux/amd64" scripts/build-release.sh v0.0.0-dev dist   # subset
set -eu

version=${1:?usage: build-release.sh <version> <out-dir>}
out=${2:?usage: build-release.sh <version> <out-dir>}
platforms=${PLATFORMS:-"linux/amd64 linux/arm64 darwin/amd64 darwin/arm64"}
module=github.com/felixn678/agent-mesh

root=$(cd "$(dirname "$0")/.." && pwd)
mkdir -p "$out"
out=$(cd "$out" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# Keep build-machine user names out of the archives (GNU tar only knows these flags).
tar_owner=""
if tar --version 2>/dev/null | grep -q GNU; then
	tar_owner="--owner=0 --group=0 --numeric-owner"
fi

cd "$root"
for p in $platforms; do
	os=${p%/*}
	arch=${p#*/}
	name=agent-mesh-$os-$arch
	mkdir -p "$work/$name"
	echo "building $name ($version)"
	CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
		-ldflags "-s -w -X $module/internal/version.Version=$version" \
		-o "$work/$name/agent-mesh" ./cmd/agent-mesh
	# shellcheck disable=SC2086 # tar_owner is a deliberate word list
	tar $tar_owner -C "$work/$name" -czf "$out/$name.tar.gz" agent-mesh
done

cd "$out"
if command -v sha256sum >/dev/null 2>&1; then
	sha256sum ./*.tar.gz | sed 's# \./# #' >SHA256SUMS
else
	shasum -a 256 ./*.tar.gz | sed 's# \./# #' >SHA256SUMS
fi
echo "wrote release archives and SHA256SUMS to $out"
