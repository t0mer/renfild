#!/usr/bin/env bash
# Cross-compile the Renfild server into dist/ for every supported target.
#
# The web UI must already be built into server/internal/webui/dist — run
# scripts/web.sh (or `make web`) first. CI does exactly that.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${VERSION:-$("$ROOT/scripts/next-version.sh")}"
DIST="$ROOT/dist"
PKG="github.com/t0mer/renfild/internal/version.Version"

TARGETS=(
  "linux/amd64"
  "linux/arm64"
  "linux/arm/7"
  "linux/arm/6"
  "linux/386"
  "darwin/amd64"
  "darwin/arm64"
  "windows/amd64"
  "windows/arm64"
)

rm -rf "$DIST"
mkdir -p "$DIST"
echo "building renfild ${VERSION}"

cd "$ROOT/server"
for target in "${TARGETS[@]}"; do
  IFS=/ read -r os arch arm <<< "$target"
  name="renfild-${VERSION}-${os}-${arch}${arm:+v$arm}"
  [ "$os" = "windows" ] && name="${name}.exe"

  echo "  -> $name"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" GOARM="${arm:-}" \
    go build -trimpath -ldflags "-s -w -X ${PKG}=${VERSION}" \
    -o "$DIST/$name" .
done

cd "$DIST"
sha256sum renfild-* > "checksums-${VERSION}.txt"
echo "done: $(ls -1 "$DIST" | wc -l) files in dist/"
