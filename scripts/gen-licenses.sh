#!/usr/bin/env bash
# gen-licenses.sh — 汇总第三方许可证，生成 THIRD-PARTY-LICENSES.txt
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="${1:-$ROOT/THIRD-PARTY-LICENSES.txt}"
CACHE="${GSRM_CACHE:-$ROOT/.cache}"

MARIA_VER=$(python3 -c "import json;print(json.load(open('versions/upstream.json'))['database_runtime']['version'])")

{
  echo "GSWXY Realm — THIRD-PARTY LICENSES"
  echo "===================================="
  echo
  echo "This distribution bundles and depends on the following third-party"
  echo "software. Each component remains under its own license."
  echo
  echo "1) AzerothCore (mod-playerbots/azerothcore-wotlk, Playerbot branch)"
  echo "   License: GNU Affero General Public License v3.0"
  echo "   Source:  https://github.com/mod-playerbots/azerothcore-wotlk"
  echo
  echo "2) mod-playerbots (mod-playerbots/mod-playerbots, master branch)"
  echo "   License: GNU Affero General Public License v3.0"
  echo "   Source:  https://github.com/mod-playerbots/mod-playerbots"
  echo
  echo "3) MariaDB Server ${MARIA_VER} (bundled runtime, stripped)"
  echo "   License: GNU General Public License v2.0 (with FOSS exception)"
  echo "   Source:  https://mariadb.org"
  echo
  echo "4) Go runtime and libraries (build-time only: go-sql-driver/mysql,"
  echo "   golang.org/x/crypto)"
  echo "   License: BSD-3-Clause / MIT-style per project"
  echo
  echo "5) AzerothCore Client Data (downloaded at runtime, NOT redistributed"
  echo "   in this repository or its releases)"
  echo "   Source:  https://github.com/wowgaming/client-data"
  echo
  echo "--------------------------------------------------------------------"
  echo "MariaDB COPYING (GPL-2.0) is bundled with the runtime and reproduced"
  echo "below when available."
  echo "--------------------------------------------------------------------"
  echo
  for f in "$CACHE"/mariadb-${MARIA_VER}/COPYING \
           "$CACHE"/mariadb-${MARIA_VER}/README.md; do
    if [ -f "$f" ]; then
      echo "=== $f ==="
      cat "$f"
      echo
    fi
  done
  # 仓库内随附的许可证文件
  for f in "$ROOT"/licenses/*; do
    [ -f "$f" ] || continue
    echo "=== $(basename "$f") ==="
    cat "$f"
    echo
  done
} > "$OUT"

echo "written: $OUT"
