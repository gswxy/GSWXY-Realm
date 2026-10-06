#!/usr/bin/env bash
# collect-runtime.sh — assemble the fnOS payload (fnos/app) from:
#   build/staging/server  (binaries, confs, data dirs, sql)
#   manager build output  (gswxy-manager + embedded webui)
#   MySQL runtime         (stripped minimal tarball)
#   config-schema/locale resources
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
STAGING="${STAGING:-$ROOT/build/staging}"
APP="${APP:-$ROOT/fnos/app}"
SRC="${SRC:-$ROOT/build/src}"

# DB runtime URL/版本统一来自 versions/upstream.json（当前为 MySQL）
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

echo "== MySQL runtime (stripped) =="
TARBALL="$CACHE/mysql-$MARIA_VER-linux-glibc2.17-x86_64-minimal.tar.xz"
if [ ! -f "$TARBALL" ]; then
  echo "downloading MySQL runtime..."
  curl -sL --retry 3 -o "$TARBALL" "$MARIA_URL"
fi
EXTRACT="$CACHE/mysql-$MARIA_VER"
if [ ! -d "$EXTRACT" ]; then
  mkdir -p "$EXTRACT"
  tar -xJf "$TARBALL" -C "$EXTRACT" --strip-components=1
fi
# 只保留运行必需：mysqld + 客户端工具 + share/（错误消息与字符集）。
mkdir -p "$APP/mysql/bin" "$APP/mysql/share" "$APP/mysql/lib/plugin"
cp -a "$EXTRACT/bin/mysqld" "$APP/mysql/bin/"
for t in mysql mysqldump mysqladmin; do
  if [ -f "$EXTRACT/bin/$t" ]; then cp -aL "$EXTRACT/bin/$t" "$APP/mysql/bin/"; fi
done
# 客户端工具依赖 libmysqlclient（minimal 包自带于 lib/ 下）；
# mysqld 的私有依赖在 lib/private/，Oracle 自带 RPATH=$ORIGIN/../lib/private
# —— 必须保留原布局，不与 lib/ 合并。
mkdir -p "$APP/mysql/lib"
cp -a "$EXTRACT/lib/"*.so* "$APP/mysql/lib/" 2>/dev/null || true
cp -a "$EXTRACT/lib/private" "$APP/mysql/lib/private" 2>/dev/null || true
cp -a "$EXTRACT/share/." "$APP/mysql/share/" 2>/dev/null || true
# plugin 目录：caching_sha2 等内置插件为静态，无需额外 plugin 文件
strip --strip-unneeded "$APP/mysql/bin/"* 2>/dev/null || true

echo "== license files =="
mkdir -p "$APP/licenses"
cp -a "$EXTRACT/LICENSE" "$APP/licenses/MySQL-LICENSE" 2>/dev/null ||   cp -a "$EXTRACT/COPYING" "$APP/licenses/MySQL-COPYING" 2>/dev/null || true
cp -a "$EXTRACT/README" "$APP/licenses/MySQL-README" 2>/dev/null || true

echo "== bundled shared libs ==="
# 动态库策略：构建基线 glibc ≤ fnOS；非系统运行库随包携带 + RPATH。
# 系统白名单（Debian 12 内置，不打包）：glibc/libstdc++/libgcc/zlib/
# bz2/readline/ssl/crypto 等；其余（libmysqlclient、boost、protobuf、…）收集。
# 游戏二进制 → app/lib；mysql 工具 → mysql/lib（各自 $ORIGIN/../lib）。
mkdir -p "$APP/lib" "$APP/mysql/lib"
WHITELIST='libc.so.6|libm.so.6|libpthread|libdl.so.2|librt.so.1|libstdc++.so.6|libgcc_s.so.1|ld-linux|libz.so.1|libbz2.so|liblzma|libreadline.so|libssl.so|libcrypto.so|libresolv|libnsl'
COPIED=""
bundle_libs() {
  local srcdir="$1" dstdir="$2"
  local BIN
  for BIN in "$srcdir"/*; do
    [ -f "$BIN" ] || continue
    while IFS= read -r line; do
      local lib path base
      lib=$(echo "$line" | awk '{print $1}')
      path=$(echo "$line" | awk '{print $3}')
      base=$(basename "$lib")
      if echo "$base" | grep -qE "^($WHITELIST)"; then continue; fi
      if [ "$path" != "not" ] && [ -f "$path" ] && [[ "$COPIED" != *"|$base|"* ]]; then
        cp -aL "$path" "$dstdir/" && COPIED="$COPIED|$base|"
      fi
    done < <(ldd "$BIN" 2>/dev/null)
  done
}
bundle_libs "$APP/bin" "$APP/lib"
bundle_libs "$APP/mysql/bin" "$APP/mysql/lib"
# Oracle 的二进制自带 RPATH=$ORIGIN/../lib/private —— 把捆绑库同时
# 放进 private/，保证 mysqld / 客户端在无环境变量时也能解析。
cp -a "$APP/mysql/lib/"*.so* "$APP/mysql/lib/private/" 2>/dev/null || true
echo "bundled: $(echo "$COPIED" | tr '|' ' ')"

echo "payload assembled at $APP"
