#!/usr/bin/env bash
# Build the React UI straight into the Go embed directory.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT/server/web"
if [ -f package-lock.json ]; then
  npm ci
else
  npm install
fi
npm run build

# Vite empties the output directory, which takes the tracked placeholder with
# it. Put it back so `go:embed` still compiles on a clean checkout and the tree
# stays clean after a build.
touch "$ROOT/server/internal/webui/dist/.gitkeep"
