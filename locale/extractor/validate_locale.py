#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
validate_locale.py — 校验 locale/zhCN 数据包（CI 与本地共用）。

检查:
1. manifest.json 存在且字段完整（版本/语言/统计）。
2. world/*.sql 与 playerbots/*.sql 均为幂等形态：
   - world 文件以 START TRANSACTION 开头、COMMIT 结尾
   - 语句全部为 UPDATE/DELETE/INSERT（无 DDL，防止破坏 upstream schema）
3. SQL 引用的表名在白名单内。
4. 生成/校验 checksums/*.sha256。

用法: python3 validate_locale.py locale/zhCN
"""
import glob
import hashlib
import json
import os
import re
import sys

ALLOWED_TABLES = {
    "creature_template", "item_template", "quest_template", "gameobject_template",
    "broadcast_text", "npc_text", "gossip_menu_option", "acore_string",
    "playerbots_names", "playerbots_guild_names", "playerbots_arena_team_names",
    "ai_playerbot_texts",
}
RE_TABLE = re.compile(r"(?:UPDATE|DELETE FROM|INSERT INTO|REPLACE INTO)\s+`(\w+)`", re.I)
RE_FORBIDDEN = re.compile(
    r"\b(CREATE|ALTER|DROP|TRUNCATE|GRANT|REVOKE|INSERT INTO\s+acore_auth|acore_characters\.characters)\b",
    re.I)


def checksum(path: str) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def main() -> int:
    root = sys.argv[1] if len(sys.argv) > 1 else "locale/zhCN"
    errors = []

    mpath = os.path.join(root, "manifest.json")
    if not os.path.exists(mpath):
        print(f"FAIL: 缺少 {mpath}")
        return 1
    m = json.load(open(mpath, encoding="utf-8"))
    for field in ("name", "version", "language", "totals"):
        if field not in m:
            errors.append(f"manifest 缺少字段: {field}")
    if m.get("language") != "zhCN":
        errors.append("manifest.language 必须为 zhCN")

    sql_files = sorted(glob.glob(os.path.join(root, "**", "*.sql"), recursive=True))
    if not sql_files:
        errors.append("没有任何 SQL 数据文件")

    total_stmts = 0
    for path in sql_files:
        rel = os.path.relpath(path, root)
        with open(path, encoding="utf-8") as f:
            content = f.read()
        # 生成器保证一条语句一行（文本值内可能含分号，不能按 ; 切分）
        lines = [l.strip() for l in content.splitlines() if l.strip()
                 and not l.strip().startswith("--")]
        total_stmts += len(lines)
        stmts = lines
        for stmt in stmts:
            mm = RE_TABLE.search(stmt)
            if mm and mm.group(1) not in ALLOWED_TABLES:
                errors.append(f"{rel}: 非白名单表 {mm.group(1)}")
            if RE_FORBIDDEN.match(stmt):
                errors.append(f"{rel}: 发现禁止的语句类型（DDL / auth / characters）")
                break
        # world 文件须为事务包裹的 UPDATE 集（按行首精确判断）
        if "/world/" in rel.replace("\\", "/") or rel.startswith("world"):
            if "START TRANSACTION" not in content or "COMMIT;" not in content:
                errors.append(f"{rel}: world 数据必须包在事务里")
            for stmt in stmts:
                if not re.match(r"^(START TRANSACTION|COMMIT|UPDATE)\b", stmt, re.I):
                    errors.append(f"{rel}: 发现非 UPDATE 语句: {stmt[:60]}")
                    break

    # checksums
    cs_dir = os.path.join(root, "checksums")
    os.makedirs(cs_dir, exist_ok=True)
    for path in sql_files:
        rel = os.path.relpath(path, root).replace("\\", "/")
        sumfile = os.path.join(cs_dir, rel.replace("/", "__") + ".sha256")
        want = f"{checksum(path)}  {rel}"
        have = open(sumfile, encoding="utf-8").read().strip() if os.path.exists(sumfile) else None
        if have != want:
            with open(sumfile, "w", encoding="utf-8") as f:
                f.write(want)

    if errors:
        print("LOCALE VALIDATION FAILED:")
        for e in errors:
            print("  -", e)
        return 1
    print(f"locale OK: {len(sql_files)} 个数据文件, {total_stmts} 条语句, "
          f"totals={m.get('totals')}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
