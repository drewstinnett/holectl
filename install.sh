#!/bin/sh
# Installs the holectl release binary for this machine.
#
#   curl -fsSL https://raw.githubusercontent.com/drewstinnett/holectl/main/install.sh | sh
#
# HOLECTL_VERSION=v0.2.0   install that release instead of the latest
# HOLECTL_INSTALL_DIR=dir  install there (default /usr/local/bin if writable, else ~/.local/bin)
set -eu

repo=drewstinnett/holectl

fail() {
	echo "holectl install: $*" >&2
	exit 1
}

case $(uname -s) in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) fail "unsupported OS $(uname -s); download a release from https://github.com/$repo/releases" ;;
esac

case $(uname -m) in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
armv7*) arch=armv7 ;;
armv6*) arch=armv6 ;;
*) fail "unsupported architecture $(uname -m); download a release from https://github.com/$repo/releases" ;;
esac

asset="holectl_${os}_${arch}.tar.gz"
if [ -n "${HOLECTL_VERSION:-}" ]; then
	case $HOLECTL_VERSION in v*) tag=$HOLECTL_VERSION ;; *) tag=v$HOLECTL_VERSION ;; esac
	base="https://github.com/$repo/releases/download/$tag"
else
	base="https://github.com/$repo/releases/latest/download"
fi

if [ -n "${HOLECTL_INSTALL_DIR:-}" ]; then
	dir=$HOLECTL_INSTALL_DIR
elif [ -w /usr/local/bin ]; then
	dir=/usr/local/bin
else
	dir="$HOME/.local/bin"
fi

if command -v curl >/dev/null 2>&1; then
	fetch() { curl -fsSL -o "$2" "$1"; }
elif command -v wget >/dev/null 2>&1; then
	fetch() { wget -qO "$2" "$1"; }
else
	fail "need curl or wget"
fi

if command -v sha256sum >/dev/null 2>&1; then
	sha256() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
	sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
	fail "need sha256sum or shasum to verify the download"
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "Downloading $base/$asset"
fetch "$base/$asset" "$tmp/$asset" || fail "download failed: $base/$asset"
fetch "$base/checksums.txt" "$tmp/checksums.txt" || fail "download failed: $base/checksums.txt"

want=$(awk -v a="$asset" '$2 == a { print $1 }' "$tmp/checksums.txt")
[ -n "$want" ] || fail "checksums.txt has no entry for $asset"
got=$(sha256 "$tmp/$asset")
[ "$got" = "$want" ] || fail "checksum mismatch for $asset (got $got, want $want); not installing"

tar -xzf "$tmp/$asset" -C "$tmp" holectl
mkdir -p "$dir"
# Write next to the target, then rename, so a running holectl is never half-overwritten.
cp "$tmp/holectl" "$dir/.holectl.new"
chmod 0755 "$dir/.holectl.new"
mv -f "$dir/.holectl.new" "$dir/holectl"

echo "Installed holectl $("$dir/holectl" version --field version) to $dir/holectl"
case ":$PATH:" in
*":$dir:"*) ;;
*) echo "note: $dir is not on your PATH; add it, e.g. export PATH=\"$dir:\$PATH\"" ;;
esac
