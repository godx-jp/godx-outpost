#!/bin/sh
# Install the latest `outpost` release for this machine's OS/arch.
#
#   curl -fsSL https://raw.githubusercontent.com/famgia/remote-host/main/scripts/install.sh | sh
#
# Installs to /usr/local/bin (via sudo if needed) or ~/.local/bin. Override with
# OUTPOST_BIN_DIR=/path. Pure-Go static binary — no runtime dependencies (dtach
# is optional, for restart-persistent sessions).
set -eu

REPO="famgia/remote-host"
BIN="outpost"

os=$(uname -s)
arch=$(uname -m)
case "$os" in
  Linux)  goos=linux ;;
  Darwin) goos=darwin ;;
  *) echo "unsupported OS: $os (use the prebuilt binaries or 'go install')" >&2; exit 1 ;;
esac
case "$arch" in
  x86_64|amd64) goarch=amd64 ;;
  arm64|aarch64) goarch=arm64 ;;
  *) echo "unsupported arch: $arch" >&2; exit 1 ;;
esac

# Resolve the latest release tag.
tag=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
  | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)
if [ -z "$tag" ]; then
  echo "could not find a published release for $REPO" >&2
  exit 1
fi
ver=${tag#v}
asset="outpost_${ver}_${goos}_${goarch}.tar.gz"
url="https://github.com/$REPO/releases/download/$tag/$asset"

echo "Downloading $asset ($tag)…"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fsSL "$url" -o "$tmp/$asset" || { echo "download failed: $url" >&2; exit 1; }
tar -xzf "$tmp/$asset" -C "$tmp"

# Pick an install dir.
if [ -n "${OUTPOST_BIN_DIR:-}" ]; then
  dir="$OUTPOST_BIN_DIR"
elif [ -w /usr/local/bin ] 2>/dev/null; then
  dir=/usr/local/bin
elif command -v sudo >/dev/null 2>&1 && [ "$goos" = linux ]; then
  dir=/usr/local/bin
  SUDO=sudo
else
  dir="$HOME/.local/bin"
fi
mkdir -p "$dir" 2>/dev/null || ${SUDO:-} mkdir -p "$dir"
${SUDO:-} install -m 0755 "$tmp/$BIN" "$dir/$BIN"

echo "Installed $BIN → $dir/$BIN"
"$dir/$BIN" version || true
case ":$PATH:" in *":$dir:"*) ;; *) echo "Note: add $dir to your PATH." ;; esac
