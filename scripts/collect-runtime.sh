#!/usr/bin/env bash
# collect-runtime.sh — assemble the fnOS payload (fnos/app) from:
#   build/staging/server  (binaries, confs, data dirs, sql)
#   manager build output  (gswxy-manager + embedded webui)
#   MariaDB runtime       (stripped bintar)
#   config-schema/locale resources
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
STAGING="${STAGING:-$ROOT/build/staging}"
APP="${APP:-$ROOT/fnos/app}"
SRC="${SRC:-$ROOT/build/src}"

MARIA_URL=$(python3 - "$ROOT" <<'EOF'
import json
print(json.load(open("versions/upstream.json"))["database_runtime"]["url"])
EOF
)
MARIA_VER=$(python3 - "$ROOT" <<'EOF'
import json
print(json.load(open("versions/upstream.json"))["database_runtime"]["version"])
EOF
)
CACHE="${GSRM_CACHE:-$ROOT/.cache}"
mkdir -p "$CACHE"

echo "== payload skeleton =="
mkdir -p "$APP/bin" "$APP/web" "$APP/etc" "$APP/data" "$APP/sql" "$APP/locale" "$APP/mysql" "$APP/runtime"

echo "== binaries =="
cp -a "$STAGING/server/bin/." "$APP/bin/"
strip --strip-unneeded "$APP/bin/worldserver" "$APP/bin/authserver" 2>/dev/null || true

echo "== conf dists =="
# worldserver/authserver dists + module dists
find "$STAGING/server/etc" -name "*.conf.dist" -exec cp -a {} "$APP/etc/" \; 2>/dev/null || true
mkdir -p "$APP/etc/modules"
# mod-playerbots conf
find "$SRC/modules/mod-playerbots/conf" -name "*.conf.dist" \
  -exec cp -a {} "$APP/etc/modules/" \; 2>/dev/null || true

echo "== data dir placeholders =="
for d in dbc maps vmaps mmaps Cameras; do mkdir -p "$APP/data/$d"; done

echo "== sql =="
mkdir -p "$APP/sql/base" "$APP/sql/updates"
cp -a "$SRC/data/sql/base/db_auth"    "$APP/sql/base/"
cp -a "$SRC/data/sql/base/db_characters" "$APP/sql/base/"
cp -a "$SRC/data/sql/base/db_world"   "$APP/sql/base/"
cp -a "$SRC/data/sql/updates/db_auth"      "$APP/sql/updates/" 2>/dev/null || true
cp -a "$SRC/data/sql/updates/db_characters" "$APP/sql/updates/" 2>/dev/null || true
cp -a "$SRC/data/sql/updates/db_world"      "$APP/sql/updates/" 2>/dev/null || true

echo "== module sql =="
mkdir -p "$APP/modules/mod-playerbots/data/sql"
cp -a "$SRC/modules/mod-playerbots/data/sql/." "$APP/modules/mod-playerbots/data/sql/" 2>/dev/null || true

echo "== zhCN locale payload =="
if [ -d "$ROOT/locale/zhCN/world" ]; then
  cp -a "$ROOT/locale/zhCN/." "$APP/locale/"
fi

echo "== manager + webui =="
if [ -f "$ROOT/manager/bin/gswxy-manager" ]; then
  cp -a "$ROOT/manager/bin/gswxy-manager" "$APP/bin/"
else
  echo "ERROR: manager binary missing (run scripts/build-manager.sh)" >&2
  exit 1
fi

echo "== config schema =="
# UI 用中文层（key→{name,desc}，未覆盖项 UI 回退 upstream 文本）
if [ -f "$ROOT/config-schema/generated/config-schema.zh.json" ]; then
  cp -a "$ROOT/config-schema/generated/config-schema.zh.json" "$APP/config-schema.json"
else
  echo "WARN: config-schema.zh.json not generated yet"
fi

echo "== resources.json =="
python3 - "$ROOT" "$APP" <<'EOF'
import json, sys
root, app = sys.argv[1], sys.argv[2]
u = json.load(open(root + "/versions/upstream.json"))
cd = u["client_data"]
mirrors = json.load(open(root + "/resources/mirrors.json"))
res = {
    "version": cd["version"],
    "label": cd.get("label", ""),
    "url": cd["url"],
    "filename": cd["asset"],
    "size_bytes": cd.get("size_bytes", 0),
    "sha256": cd.get("sha256", "") if cd.get("sha256", "").startswith(tuple("0123456789abcdef")) else "",
    "requires": cd["requires"],
    "mirrors": [m["url"].format(version=cd["version"], filename=cd["asset"]) for m in mirrors.get("mirrors", [])],
}
open(app + "/resources.json", "w").write(json.dumps(res, indent=2, ensure_ascii=False))
print("resources.json written:", res["version"])
EOF

echo "== MariaDB runtime (stripped) =="
TARBALL="$CACHE/mariadb-$MARIA_VER-linux-systemd-x86_64.tar.gz"
if [ ! -f "$TARBALL" ]; then
  echo "downloading MariaDB runtime..."
  curl -sL --retry 3 -o "$TARBALL" "$MARIA_URL"
fi
EXTRACT="$CACHE/mariadb-$MARIA_VER"
if [ ! -d "$EXTRACT" ]; then
  mkdir -p "$EXTRACT"
  tar -xzf "$TARBALL" -C "$EXTRACT" --strip-components=1
fi
# Strip to the runtime essentials: daemon, client tools, share/charsets.
mkdir -p "$APP/mysql"
cp -a "$EXTRACT/bin/mariadbd" "$APP/mysql/bin/" 2>/dev/null || cp -a "$EXTRACT/bin/mysqld" "$APP/mysql/bin/"
for t in mariadb mysql mariadb-admin mysqladmin mariadb-dump mysqldump mariadb-install-db mysql_install_db; do
  if [ -f "$EXTRACT/bin/$t" ]; then cp -a "$EXTRACT/bin/$t" "$APP/mysql/bin/"; fi
done
mkdir -p "$APP/mysql/share" "$APP/mysql/lib"
cp -a "$EXTRACT/share/." "$APP/mysql/share/" 2>/dev/null || true
# only language + charsets needed
find "$APP/mysql/share" -maxdepth 1 -mindepth 1 ! -name 'charsets' ! -name 'english' -exec rm -rf {} + 2>/dev/null || true
# shared libs mariadbd needs (libaio comes from fnOS; copy plugin dir too)
cp -a "$EXTRACT/lib/" "$APP/mysql/lib/" 2>/dev/null || true
mkdir -p "$APP/mysql/lib/plugin"
cp -a "$EXTRACT/lib/plugin/"* "$APP/mysql/lib/plugin/" 2>/dev/null || true
strip --strip-unneeded "$APP/mysql/bin/"* 2>/dev/null || true

echo "== license files =="
mkdir -p "$APP/licenses"
cp -a "$EXTRACT/COPYING" "$APP/licenses/MariaDB-COPYING" 2>/dev/null || true

echo "payload assembled at $APP"
