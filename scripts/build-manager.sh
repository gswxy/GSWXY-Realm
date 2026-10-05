#!/usr/bin/env bash
# build-manager.sh — compile the Go Manager (linux/amd64 by default).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
GOOS_BIN="${GOOS_BIN:-linux}"
GOARCH_BIN="${GOARCH_BIN:-amd64}"

cd "$ROOT/manager"
export CGO_ENABLED=0
export GOOS="$GOOS_BIN"
export GOARCH="$GOARCH_BIN"
mkdir -p bin
go build -trimpath -ldflags="-s -w" -o bin/gswxy-manager ./cmd/gswxy-manager

# Windows dev build (optional, skipped when cross-compiling for CI).
if [ "$GOOS_BIN" = "windows" ]; then
  go build -trimpath -ldflags="-s -w" -o bin/gswxy-manager.exe ./cmd/gswxy-manager
fi

ls -la bin/
