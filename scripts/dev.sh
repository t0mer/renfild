#!/usr/bin/env bash
# Run the server and the Vite dev server side by side with hot reload.
#
#   ./scripts/dev.sh                 # uses ./server/config.example.yaml
#   RENFILD_CONFIG=... ./scripts/dev.sh
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CONFIG="${RENFILD_CONFIG:-$ROOT/server/config.example.yaml}"

cleanup() { kill 0; }
trap cleanup EXIT INT TERM

(cd "$ROOT/server" && go run . serve --config "$CONFIG" --db "$ROOT/dev.db" --log-level debug) &
(cd "$ROOT/server/web" && npm run dev) &
wait
