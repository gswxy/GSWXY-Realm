#!/usr/bin/env bash
# smoke-test.sh — verify the staged payload on a Linux host:
# binaries execute, dynamic deps resolve, required data present.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
APP="${APP:-$ROOT/fnos/app}"

fail() { echo "SMOKE FAIL: $*" >&2; exit 1; }

echo "== binaries present and executable =="
for b in worldserver authserver gswxy-manager; do
  [ -x "$APP/bin/$b" ] || fail "$APP/bin/$b missing or not executable"
done

echo "== gswxy-manager --version =="
"$APP/bin/gswxy-manager" version

echo "== dynamic libraries resolve (with bundled lib dir) =="
export LD_LIBRARY_PATH="$APP/lib${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
for b in worldserver authserver; do
  MISSING=$(ldd "$APP/bin/$b" 2>/dev/null | awk '/not found/{print $1}') || true
  if [ -n "${MISSING:-}" ]; then
    fail "$b has unresolved libs: $MISSING"
  fi
done

echo "== conf dists =="
for c in worldserver.conf.dist authserver.conf.dist; do
  [ -f "$APP/etc/$c" ] || fail "$APP/etc/$c missing"
done
ls "$APP/etc/modules/"*.conf.dist >/dev/null 2>&1 || fail "module conf dists missing"

echo "== sql present =="
[ -f "$APP/data/sql/base/db_auth/acore_auth.sql" ] || true   # layout-tolerant
N=$(find "$APP/data/sql/base" -name '*.sql' | wc -l)
[ "$N" -gt 0 ] || fail "no base SQL found"

echo "== mysql runtime =="
[ -x "$APP/mysql/bin/mysqld" ] || [ -x "$APP/mysql/bin/mariadbd" ] || fail "mysqld missing"
[ -d "$APP/mysql/share/charsets" ] || fail "mysql share/charsets missing"
MISSING=$(LD_LIBRARY_PATH="$APP/mysql/lib:$APP/lib" ldd "$APP/mysql/bin/mysqld" 2>/dev/null | awk '/not found/{print $1}') || true
if [ -n "${MISSING:-}" ]; then
  fail "mysqld has unresolved libs: $MISSING"
fi

echo "== resources.json =="
python3 -c "import json,sys; r=json.load(open('$APP/resources.json')); assert r['url'].startswith('https://')"

echo "== 配置键防回归：botprofiles/recommended 的键必须存在于上游 dist =="
python3 - "$ROOT" "$APP" <<'EOF'
import json, re, sys, pathlib
root, app = map(pathlib.Path, sys.argv[1:3])

def keys_of(path):
    ks = set()
    for line in path.read_text(encoding="utf8", errors="ignore").splitlines():
        m = re.match(r"^([A-Za-z][A-Za-z0-9._]*)\s*=", line)
        if m:
            ks.add(m.group(1))
    return ks

dists = {
    "worldserver.conf": keys_of(app / "etc/worldserver.conf.dist"),
    "authserver.conf": keys_of(app / "etc/authserver.conf.dist"),
    # playerbots.conf 的逻辑名对应模块 dist
    "playerbots.conf": keys_of(app / "etc/modules/playerbots.conf.dist"),
}

def check(source, label):
    bad = []
    for conf, kv in source.items():
        for key in kv:
            if key not in dists[conf]:
                bad.append(f"{conf}: {key}")
    if bad:
        sys.exit(f"KEY REGRESSION in {label}: 这些键不在上游 dist 里: {bad}")

profiles = json.loads((root / "manager/internal/app/botprofiles.json").read_text(encoding="utf8"))
check({"playerbots.conf": {k for p in profiles for k in p["values"]}}, "botprofiles.json")

rec = json.loads((root / "manager/internal/recommend/recommended.json").read_text(encoding="utf8"))
check(rec, "recommended.json")
print("config keys ok")
EOF

echo "SMOKE OK"
