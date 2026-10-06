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

echo "SMOKE OK"
