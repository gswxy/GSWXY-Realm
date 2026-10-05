#!/usr/bin/env bash
# verify-package.sh — structural verification of the built .fpk:
# tar layout, manifest fields, script executability, payload presence.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
FPK="${1:-$ROOT/dist/GSWXY-Realm-1.0.0-x86_64.fpk}"

[ -f "$FPK" ] || { echo "fpk not found: $FPK" >&2; exit 1; }
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

echo "== archive inspect =="
tar -tf "$FPK" > "$TMP/list" 2>/dev/null || tar -tf "$FPK" > "$TMP/list"
tar -xf "$FPK" -C "$TMP"

echo "== manifest =="
M=$(find "$TMP" -maxdepth 2 -name manifest | head -1)
[ -n "$M" ] || { echo "manifest missing" >&2; exit 1; }
for field in appname version display_name platform source service_port desktop_uidir; do
  grep -q "^${field}" "$M" || { echo "manifest field missing: $field" >&2; exit 1; }
done
grep -q 'appname="com.gswxy.realm"' "$M" || { echo "wrong appname" >&2; exit 1; }

echo "== structure =="
BASE=$(dirname "$M")
for d in cmd config wizard app ui; do
  [ -e "$BASE/$d" ] || { echo "missing dir: $d" >&2; exit 1; }
done
for f in config/privilege config/resource ICON.PNG ICON_256.PNG; do
  [ -e "$BASE/$f" ] || { echo "missing file: $f" >&2; exit 1; }
done
[ -x "$BASE/cmd/main" ] || { echo "cmd/main not executable" >&2; exit 1; }
[ -f "$BASE/app/bin/gswxy-manager" ] || { echo "manager binary missing" >&2; exit 1; }
[ -f "$BASE/app/resources.json" ] || { echo "resources.json missing" >&2; exit 1; }

# JSON validity
python3 -c "
import json
json.load(open('$BASE/config/privilege'))
json.load(open('$BASE/config/resource'))
json.load(open('$BASE/app/ui/config'))
json.load(open('$BASE/app/resources.json'))
for w in ['$BASE/wizard/install.json', '$BASE/wizard/uninstall.json']:
    json.load(open(w))
print('json ok')
"

echo "== checksum =="
sha256sum "$FPK"

echo "VERIFY OK: $FPK"
