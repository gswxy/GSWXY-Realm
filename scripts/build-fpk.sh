#!/usr/bin/env bash
# build-fpk.sh — assemble fnos/ (manifest + cmd + config + wizard + app)
# and pack with the official fnpack CLI.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
APP="${APP:-$ROOT/fnos/app}"
OUT="${OUT:-$ROOT/dist}"
VERSION=$(python3 - <<'EOF'
import re
m = re.search(r'version="([^"]+)"', open("fnos/manifest", encoding="utf8").read())
print(m.group(1))
EOF
)
CACHE="${GSRM_CACHE:-$ROOT/.cache}"
FNPACK_VER="1.2.3"
FNPACK="$CACHE/fnpack-${FNPACK_VER}-linux-amd64"

mkdir -p "$OUT"

if [ ! -x "$FNPACK" ]; then
  echo "downloading fnpack ${FNPACK_VER}..."
  curl -sL --retry 3 -o "$FNPACK" \
    "https://static2.fnnas.com/fnpack/fnpack-${FNPACK_VER}-linux-amd64"
  chmod +x "$FNPACK"
fi

cd "$ROOT"
"$FNPACK" build --directory "$ROOT/fnos"

# fnpack output name: usually <dirname>.fpk in the parent of the project dir
FPK="$ROOT/fnos.fpk"
if [ ! -f "$FPK" ]; then
  FPK=$(ls -t "$ROOT"/*.fpk 2>/dev/null | head -1 || true)
fi
if [ -z "${FPK:-}" ] || [ ! -f "$FPK" ]; then
  echo "ERROR: fnpack did not produce an .fpk" >&2
  exit 1
fi

FINAL="$OUT/GSWXY-Realm-${VERSION}-x86_64.fpk"
mv "$FPK" "$FINAL"
( cd "$OUT" && sha256sum "$(basename "$FINAL")" > "$(basename "$FINAL").sha256" )

echo "built: $FINAL"
ls -la "$OUT"
