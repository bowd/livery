#!/bin/sh
# Livery (`liv`) installer — downloads a static, SHA-256 verified binary for macOS or Linux.
#
# Usage:
#   curl -fsSL https://liv.bowd.io/install.sh | sh
#   curl -fsSL https://liv.bowd.io/install.sh | LIVERY_VERSION=v0.1.0 sh
#
# Environment:
#   LIVERY_VERSION      release tag to install (default: latest stable release)
#   LIVERY_INSTALL_DIR  where to put `liv` (default: ~/.local/bin)
#   LIVERY_REPO         GitHub repository to download from (default: bowd/livery)
#   LIVERY_BASE_URL     download assets from here instead of the GitHub release
#                       (any URL curl understands, e.g. file:///path/to/dist; needs LIVERY_VERSION)
set -eu

REPO="${LIVERY_REPO:-bowd/livery}"
INSTALL_DIR="${LIVERY_INSTALL_DIR:-$HOME/.local/bin}"
VERSION="${LIVERY_VERSION:-latest}"

die() {
  echo "install.sh: $*" >&2
  exit 1
}

for tool in curl tar; do
  command -v "$tool" >/dev/null 2>&1 || die "'$tool' is required but was not found"
done

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
  darwin|linux) ;;
  *) die "unsupported OS: $OS" ;;
esac

MACH=$(uname -m | tr '[:upper:]' '[:lower:]')
case "$MACH" in
  x86_64|amd64) ARCH=amd64 ;;
  arm64|aarch64) ARCH=arm64 ;;
  *) die "unsupported architecture: $MACH" ;;
esac

if [ "$VERSION" = "latest" ]; then
  [ -z "${LIVERY_BASE_URL:-}" ] || die "LIVERY_BASE_URL needs an explicit LIVERY_VERSION"
  # /releases/latest redirects to /releases/tag/<tag>; reading the redirect avoids the rate-limited API.
  VERSION=$(curl -fsSI "https://github.com/${REPO}/releases/latest" \
    | grep -i '^location:' \
    | sed 's/.*tag\/\([^[:space:]]*\).*/\1/' \
    | tr -d '\r')
  if [ -z "$VERSION" ]; then
    echo "If building from a local checkout, run: make install" >&2
    die "could not resolve the latest release tag for ${REPO}"
  fi
fi

VER_NUM=${VERSION#v}
ASSET="livery_${VER_NUM}_${OS}_${ARCH}.tar.gz"
BASE_URL="${LIVERY_BASE_URL:-https://github.com/${REPO}/releases/download/${VERSION}}"
BASE_URL=${BASE_URL%/}

TMP=$(mktemp -d)
STAGED=""
cleanup() {
  rm -rf "$TMP"
  [ -z "$STAGED" ] || rm -f "$STAGED"
}
trap cleanup EXIT

echo "Downloading livery ${VERSION} (${OS}/${ARCH})..."
curl -fsSL "${BASE_URL}/${ASSET}" -o "${TMP}/${ASSET}" \
  || die "could not download ${BASE_URL}/${ASSET}"
curl -fsSL "${BASE_URL}/checksums.txt" -o "${TMP}/checksums.txt" \
  || die "could not download ${BASE_URL}/checksums.txt, refusing to install an unverified binary"

EXPECTED=$(awk -v asset="$ASSET" '$2 == asset || $2 == "*" asset { print $1; exit }' "${TMP}/checksums.txt")
[ -n "$EXPECTED" ] || die "checksums.txt has no entry for ${ASSET}"

if command -v sha256sum >/dev/null 2>&1; then
  ACTUAL=$(sha256sum "${TMP}/${ASSET}" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  ACTUAL=$(shasum -a 256 "${TMP}/${ASSET}" | awk '{print $1}')
else
  die "need sha256sum or shasum to verify the download"
fi
[ "$EXPECTED" = "$ACTUAL" ] || die "SHA-256 mismatch for ${ASSET} (expected ${EXPECTED}, got ${ACTUAL})"
echo "Verified SHA-256 checksum."

tar -xzf "${TMP}/${ASSET}" -C "${TMP}" liv
mkdir -p "${INSTALL_DIR}"
# Stage next to the destination so the final rename is atomic, even while `liv` is running.
STAGED="${INSTALL_DIR}/.liv.tmp.$$"
cp "${TMP}/liv" "$STAGED"
chmod 755 "$STAGED"
mv -f "$STAGED" "${INSTALL_DIR}/liv"
STAGED=""

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
