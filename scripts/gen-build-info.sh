#!/usr/bin/env bash
# gen-build-info.sh — produce build-info.json from versions/upstream.json
# plus CI-provided environment. No hand-edited version metadata anywhere.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
APP="${APP:-$ROOT/fnos/app}"

# 唯一版本来源：fnos/manifest。GSRM_VERSION 仅用于 CI 显式覆盖
# （如 nightly 追加 -nightly.N 后缀），且覆盖值必须以 manifest 版本开头。
MANIFEST_VERSION=$(python3 - <<'EOF'
import re
m = re.search(r'version="([^"]+)"', open("fnos/manifest", encoding="utf8").read())
print(m.group(1) if m else "")
EOF
)
if [ -n "${GSRM_VERSION:-}" ]; then
  VERSION="$GSRM_VERSION"
  case "$VERSION" in
    "$MANIFEST_VERSION"|"$MANIFEST_VERSION"-*) ;;
    *) echo "ERROR: GSRM_VERSION=$VERSION 与 manifest 版本 $MANIFEST_VERSION 不一致" >&2; exit 1 ;;
  esac
else
  VERSION="${MANIFEST_VERSION:-1.0.0}"
fi
CHANNEL="${GSRM_CHANNEL:-$([ "${VERSION#*-}" != "$VERSION" ] && echo nightly || echo stable)}"
RUN_ID="${GSRM_RUN_ID:-local}"
BUILT_AT="${GSRM_BUILT_AT:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"

python3 - "$ROOT" "$APP" "$VERSION" "$CHANNEL" "$RUN_ID" "$BUILT_AT" <<'EOF'
import json, sys
root, app, version, channel, run_id, built_at = sys.argv[1:7]
u = json.load(open(root + "/versions/upstream.json"))
info = {
    "product": "GSWXY Realm",
    "version": version,
    "channel": channel,
    "core": {
        "repository": u["core"]["repository"],
        "branch": u["core"]["branch"],
        "commit": u["core"]["commit"],
    },
    "playerbots": {
        "repository": u["playerbots"]["repository"],
        "branch": u["playerbots"]["branch"],
        "commit": u["playerbots"]["commit"],
    },
    "client_data": {"version": u["client_data"]["version"]},
    "database_runtime": {
        "flavor": u["database_runtime"]["flavor"],
        "version": u["database_runtime"]["version"],
    },
    "locale": {
        "language": u["gswxy_locale"]["language"],
        "version": u["gswxy_locale"]["version"],
    },
    "build": {"run_id": run_id, "built_at": built_at},
}
import os
open(app + "/build-info.json", "w").write(json.dumps(info, indent=2, ensure_ascii=False))
print(json.dumps(info, indent=2, ensure_ascii=False))
EOF
