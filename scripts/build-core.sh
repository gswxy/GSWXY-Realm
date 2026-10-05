#!/usr/bin/env bash
# build-core.sh — CMake/Ninja Release build of the staged core + module,
# installed into the staging tree. Uses ccache when available.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SRC="${SRC:-$ROOT/build/src}"
BUILD="${BUILD:-$ROOT/build/core-build}"
STAGING="${STAGING:-$ROOT/build/staging}"

NPROC=$(nproc 2>/dev/null || echo 4)

if command -v ccache >/dev/null 2>&1; then
  export CCACHE_MAXSIZE="${CCACHE_MAXSIZE:-2G}"
fi

mkdir -p "$BUILD" "$STAGING"

cmake -S "$SRC" -B "$BUILD" \
  -G Ninja \
  -DCMAKE_BUILD_TYPE=Release \
  -DCMAKE_C_COMPILER_LAUNCHER=$(command -v ccache || echo) \
  -DCMAKE_CXX_COMPILER_LAUNCHER=$(command -v ccache || echo) \
  -DCMAKE_INSTALL_PREFIX="$STAGING/server" \
  -DCONF_DIR=etc \
  -DTOOLS=0 \
  -DPLAYERBOTS=ON \
  -DUSE_SCRIPTPCH=OFF \
  -DUSE_COREPCH=OFF \
  -DBUILD_TESTING=OFF \
  -DWITH_WARNINGS=1

cmake --build "$BUILD" --config Release -- -j"$NPROC"
cmake --install "$BUILD" --config Release

echo "installed to $STAGING/server"
