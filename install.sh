#!/bin/sh
# Installs the latest matrix-for-ai-cli for this system, checked against the release's SHA256SUMS.
# Usage: curl -fsSL https://github.com/matrix-for-ai/cli/releases/latest/download/install.sh | sh
#        BINDIR (default ~/.local/bin) to install elsewhere
set -eu
release=https://github.com/matrix-for-ai/cli/releases/latest/download
fail() { echo "matrix-for-ai-cli install: $*" >&2; exit 1; }

case $(uname -s) in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) fail "no build for $(uname -s); on Windows, download the .exe from the release" ;;
esac
case $(uname -m) in
  x86_64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) fail "no build for $(uname -m)" ;;
esac
f=matrix-for-ai-cli-$os-$arch
bindir=${BINDIR:-$HOME/.local/bin}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fsSL -o "$tmp/$f" "$release/$f"
curl -fsSL -o "$tmp/SHA256SUMS" "$release/SHA256SUMS"
grep " $f\$" "$tmp/SHA256SUMS" > "$tmp/sum" || fail "$f is not in SHA256SUMS"
if command -v sha256sum > /dev/null; then sum="sha256sum -c"; else sum="shasum -a 256 -c"; fi
(cd "$tmp" && $sum sum > /dev/null) || fail "$f does not match SHA256SUMS"

mkdir -p "$bindir"
install -m 755 "$tmp/$f" "$bindir/matrix-for-ai-cli"
echo "installed $("$bindir/matrix-for-ai-cli" version) in $bindir"
case ":$PATH:" in
  *":$bindir:"*) ;;
  *) echo "$bindir is not in your PATH: add it" ;;
esac
