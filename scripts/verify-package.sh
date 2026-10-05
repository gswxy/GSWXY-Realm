#!/usr/bin/env bash
# verify-package.sh — structural verification of the built .fpk.
# Real fnpack 1.2.3 layout (verified on fnOS):
#   app.tgz (payload), cmd/, config/, wizard/, manifest, ICON*.PNG
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
FPK="${1:-}"
if [ -z "$FPK" ]; then
  # 默认取 dist 下最新的 fpk
  FPK=$(find "$ROOT/dist" -maxdepth 1 -name "*.fpk" -type f | sort | tail -1)
fi

[ -f "$FPK" ] || { echo "fpk not found: $FPK" >&2; exit 1; }
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

echo "== archive inspect =="
tar -xf "$FPK" -C "$TMP"

echo "== manifest =="
M="$TMP/manifest"
[ -f "$M" ] || M=$(find "$TMP" -maxdepth 2 -name manifest | head -1)
[ -n "$M" ] && [ -f "$M" ] || { echo "manifest missing" >&2; exit 1; }
# fnpack 规范化 manifest 值（引号/空格），用归一化方式解析
norm() { sed -E 's/[[:space:]]*=[[:space:]]*/=/' "$1" | sed -E 's/^([a-z_]+)="?([^"]*)"?"?/\1=\2/I'; }
for field in appname version display_name platform source service_port desktop_uidir desktop_applaunchname; do
  grep -qiE "^${field}[[:space:]]*=" "$M" || { echo "manifest field missing: $field" >&2; exit 1; }
done
norm "$M" | grep -qiE '^appname=com\.gswxy\.realm$' || { echo "wrong appname" >&2; exit 1; }

echo "== structure =="
for d in cmd config wizard; do
  [ -e "$TMP/$d" ] || { echo "missing dir: $d" >&2; exit 1; }
done
for f in manifest ICON.PNG ICON_256.PNG app.tgz config/privilege config/resource; do
  [ -e "$TMP/$f" ] || { echo "missing file: $f" >&2; exit 1; }
done
[ -x "$TMP/cmd/main" ] || { echo "cmd/main not executable" >&2; exit 1; }

echo "== payload (app.tgz) =="
PAYLOAD="$TMP/payload"
mkdir -p "$PAYLOAD"
tar -xzf "$TMP/app.tgz" -C "$PAYLOAD"
[ -f "$PAYLOAD/bin/gswxy-manager" ] || { echo "manager binary missing" >&2; exit 1; }
[ -f "$PAYLOAD/resources.json" ] || { echo "resources.json missing" >&2; exit 1; }
# fnpack 归一化 payload 内权限位；fnOS install_callback 会恢复执行位，
# 因此这里只警告不失败。
[ -x "$PAYLOAD/bin/gswxy-manager" ] || echo "WARN: manager exec bit not set in app.tgz (install_callback will restore)"
[ -x "$PAYLOAD/bin/worldserver" ] || echo "WARN: worldserver missing (stub payload?)"
[ -x "$PAYLOAD/bin/authserver" ] || echo "WARN: authserver missing (stub payload?)"

echo "== JSON validity =="
PY=python3
command -v python3 >/dev/null 2>&1 || PY=python
"$PY" - "$TMP" "$PAYLOAD" <<'EOF'
import json, sys
tmp, payload = sys.argv[1], sys.argv[2]
for p in [f"{tmp}/config/privilege", f"{tmp}/config/resource",
          f"{payload}/ui/config", f"{payload}/resources.json",
          f"{tmp}/wizard/install.json", f"{tmp}/wizard/uninstall.json"]:
    try:
        json.load(open(p))
    except FileNotFoundError:
        print(f"WARN: {p} not present")
print("json ok")
EOF

echo "== checksum =="
sha256sum "$FPK"

echo "VERIFY OK: $FPK"
