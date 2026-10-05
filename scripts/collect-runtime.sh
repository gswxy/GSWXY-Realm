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

echo "== bundled shared libs ==="
# 动态库策略：构建基线 glibc ≤ fnOS；非系统运行库随包携带 + RPATH。
# 系统白名单（Debian 12 内置，不打包）：glibc/libstdc++/libgcc/zlib/
# bz2/readline/ssl/crypto 等；其余（libmysqlclient、boost、…）收集。
mkdir -p "$APP/lib"
WHITELIST='libc.so.6|libm.so.6|libpthread|libdl.so.2|librt.so.1|libstdc++.so.6|libgcc_s.so.1|ld-linux|libz.so.1|libbz2.so|liblzma|libreadline.so|libtinfo|libncurses|libssl.so|libcrypto.so|libresolv|libnsl'
COPIED=""
for BIN in "$APP/bin/"*; do
  [ -f "$BIN" ] || continue
  while IFS= read -r line; do
    lib=$(echo "$line" | awk '{print $1}')
    path=$(echo "$line" | awk '{print $3}')
    base=$(basename "$lib")
    case "$base" in $WHITELIST) continue ;; esac
    if [ "$path" != "not" ] && [ -f "$path" ] && [[ "$COPIED" != *"|$base|"* ]]; then
      cp -aL "$path" "$APP/lib/" && COPIED="$COPIED|$base|"
    fi
  done < <(ldd "$BIN" 2>/dev/null)
done
patchelf --set-rpath '$ORIGIN/../lib' "$APP/bin/worldserver" "$APP/bin/authserver" 2>/dev/null || true
echo "bundled: $(echo "$COPIED" | tr '|' ' ')"

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
# -L dereferences symlinks (bintar ships mariadbd -> mysqld links).
mkdir -p "$APP/mysql/bin" "$APP/mysql/share" "$APP/mysql/lib/plugin"
cp -aL "$EXTRACT/bin/mariadbd" "$APP/mysql/bin/" 2>/dev/null || cp -aL "$EXTRACT/bin/mysqld" "$APP/mysql/bin/"
for t in mariadb mysql mariadb-admin mysqladmin mariadb-dump mysqldump my_print_defaults resolveip; do
  if [ -f "$EXTRACT/bin/$t" ]; then cp -aL "$EXTRACT/bin/$t" "$APP/mysql/bin/"; fi
done
# MariaDB bintar 把 install-db 放在 scripts/ 而不是 bin/（实测 11.4.8）
for s in mariadb-install-db mysql_install_db; do
  if [ ! -f "$APP/mysql/bin/$s" ] && [ -f "$EXTRACT/scripts/$s" ]; then
    cp -aL "$EXTRACT/scripts/$s" "$APP/mysql/bin/"
  elif [ -f "$EXTRACT/bin/$s" ]; then
    cp -aL "$EXTRACT/bin/$s" "$APP/mysql/bin/"
  fi
done
cp -a "$EXTRACT/share/." "$APP/mysql/share/" 2>/dev/null || true
# 保留完整 share/：mariadb-install-db 需要 fill_help_tables.sql 与
# sys_schema，errmsg 多语言文件体积可接受
# shared libs mariadbd needs (libaio comes from fnOS; copy plugin dir too)
cp -a "$EXTRACT/lib/" "$APP/mysql/lib/" 2>/dev/null || true
mkdir -p "$APP/mysql/lib/plugin"
cp -a "$EXTRACT/lib/plugin/"* "$APP/mysql/lib/plugin/" 2>/dev/null || true
strip --strip-unneeded "$APP/mysql/bin/"* 2>/dev/null || true

echo "== license files =="
mkdir -p "$APP/licenses"
cp -a "$EXTRACT/COPYING" "$APP/licenses/MariaDB-COPYING" 2>/dev/null || true

echo "payload assembled at $APP"
