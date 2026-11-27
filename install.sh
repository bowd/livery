#!/bin/sh
# Livery (`liv`) installer — downloads a static, SHA-256 verified binary for macOS or Linux.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/bowd/livery/main/install.sh | sh
#   LIVERY_VERSION=v0.1.0 LIVERY_INSTALL_DIR=~/.local/bin sh install.sh
set -eu

REPO="${LIVERY_REPO:-bowd/livery}"
INSTALL_DIR="${LIVERY_INSTALL_DIR:-$HOME/.local/bin}"
VERSION="${LIVERY_VERSION:-latest}"

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
  darwin|linux) ;;
  *) echo "Unsupported OS: $OS" >&2; exit 1 ;;
esac

MACH=$(uname -m | tr '[:upper:]' '[:lower:]')
case "$MACH" in
  x86_64|amd64) ARCH=amd64 ;;
  arm64|aarch64) ARCH=arm64 ;;
  *) echo "Unsupported architecture: $MACH" >&2; exit 1 ;;
esac

if [ "$VERSION" = "latest" ]; then
  VERSION=$(curl -fsSI "https://github.com/${REPO}/releases/latest" \
    | grep -i '^location:' \
    | sed 's/.*tag\/\([^[:space:]]*\).*/\1/' \
    | tr -d '\r')
  if [ -z "$VERSION" ]; then
    echo "Could not resolve latest release tag for ${REPO}." >&2
    echo "If building from a local checkout, run: make install" >&2
    exit 1
  fi
fi

VER_NUM=${VERSION#v}
ASSET="livery_${VER_NUM}_${OS}_${ARCH}.tar.gz"
BASE_URL="https://github.com/${REPO}/releases/download/${VERSION}"

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

echo "Downloading livery ${VERSION} (${OS}/${ARCH})..."
curl -fsSL "${BASE_URL}/${ASSET}" -o "${TMP}/${ASSET}"

if curl -fsSL "${BASE_URL}/checksums.txt" -o "${TMP}/checksums.txt" 2>/dev/null; then
  EXPECTED=$(grep "  ${ASSET}$" "${TMP}/checksums.txt" | awk '{print $1}' | head -n 1 || true)
  if [ -n "$EXPECTED" ]; then
    ACTUAL=$( (sha256sum "${TMP}/${ASSET}" 2>/dev/null || shasum -a 256 "${TMP}/${ASSET}") | awk '{print $1}' )
    if [ "$EXPECTED" != "$ACTUAL" ]; then
      echo "SHA-256 verification failed for ${ASSET}!" >&2
      exit 1
    fi
    echo "Verified SHA-256 checksum."
  fi
fi

tar -xzf "${TMP}/${ASSET}" -C "${TMP}"
mkdir -p "${INSTALL_DIR}"
chmod 755 "${TMP}/liv"
mv "${TMP}/liv" "${INSTALL_DIR}/liv"

echo "Installed liv to ${INSTALL_DIR}/liv"
"${INSTALL_DIR}/liv" version || true

case ":$PATH:" in
  *":${INSTALL_DIR}:"*) ;;
  *)
    echo ""
    echo "Add ${INSTALL_DIR} to your PATH:"
    echo "  export PATH=\"${INSTALL_DIR}:\$PATH\""
    ;;
esac
