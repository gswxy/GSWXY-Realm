#!/usr/bin/env bash
# fetch-upstream.sh — clone the pinned AzerothCore/Playerbots sources.
# Reads versions/upstream.json. Uses a local cache; verbatim SHAs only.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SRC="${1:-$ROOT/build/src}"
CACHE="${GSRM_CACHE:-$ROOT/.cache}"

COMMIT=$(python3 - "$ROOT" <<'EOF'
import json, sys
u = json.load(open(sys.argv[1] + "/versions/upstream.json"))
print(u["core"]["commit"])
EOF
)
CORE_REPO=$(python3 - "$ROOT" <<'EOF'
import json, sys
print(json.load(open(sys.argv[1] + "/versions/upstream.json"))["core"]["repository"])
EOF
)
PB_COMMIT=$(python3 - "$ROOT" <<'EOF'
import json, sys
u = json.load(open(sys.argv[1] + "/versions/upstream.json"))
print(u["playerbots"]["commit"])
EOF
)
PB_REPO=$(python3 - "$ROOT" <<'EOF'
import json, sys
print(json.load(open(sys.argv[1] + "/versions/upstream.json"))["playerbots"]["repository"])
EOF
)

mkdir -p "$SRC" "$CACHE"

if [ ! -d "$CACHE/core" ]; then
  git clone --filter=blob:none "$CORE_REPO" "$CACHE/core"
fi
git -C "$CACHE/core" fetch origin "$COMMIT" 2>/dev/null || true
git -C "$CACHE/core" checkout --detach "$COMMIT"

mkdir -p "$SRC/modules"
rm -rf "$SRC/.git"  # staged tree is a plain copy
cp -a "$CACHE/core/." "$SRC/"

if [ ! -d "$CACHE/playerbots" ]; then
  git clone --filter=blob:none "$PB_REPO" "$CACHE/playerbots"
fi
git -C "$CACHE/playerbots" fetch origin "$PB_COMMIT" 2>/dev/null || true
git -C "$CACHE/playerbots" checkout --detach "$PB_COMMIT"

rm -rf "$SRC/modules/mod-playerbots"
cp -a "$CACHE/playerbots" "$SRC/modules/mod-playerbots"
rm -rf "$SRC/modules/mod-playerbots/.git"

echo "core: $COMMIT"
echo "playerbots: $PB_COMMIT"
echo "staged at $SRC"
