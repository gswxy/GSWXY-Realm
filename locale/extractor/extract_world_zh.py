#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
GSWXY Locale Extractor —— 从「耳语魔兽」只读提取通用游戏 zhCN 本地化数据。

原则（与任务要求一致）：
1. 只读取游戏内容表（creature/item/quest/gameobject/broadcast_text/npc_text/
   gossip/acore_string），绝不触碰 acore_auth、玩家角色、日志、封禁等运营数据。
2. 以锁定上游的 base SQL 为「干净 ID 集」：生产库中不属于上游主键的行
   （自定义 NPC/物品/任务等）一律排除。
3. 只导出含中文的行；输出幂等的 UPDATE/INSERT SQL（可重复导入）。
4. 数据库连接串只来自命令行参数/环境变量，绝不写入仓库。

用法:
    python3 extract_world_zh.py \
        --host wow.gswxy.com --port 9116 --user root --password '...' \
        --src <上游源码根> --out ../zhCN
"""
import argparse
import hashlib
import json
import os
import re
import sys
import time

import pymysql
import pymysql.cursors

# 表 -> (主键列, 文本列, 导出条件)。列名以 2026-10 生产库/上游 schema 实测为准。
TABLES = [
    ("acore_world", "creature_template", "entry", ["name", "subname"]),
    ("acore_world", "item_template", "entry", ["name", "description"]),
    ("acore_world", "quest_template", "ID", ["LogTitle", "QuestDescription", "LogDescription"]),
    ("acore_world", "gameobject_template", "entry", ["name"]),
    ("acore_world", "broadcast_text", "ID", ["MaleText", "FemaleText"]),
    ("acore_world", "npc_text", "ID", ["text0_0", "text0_1"]),
    ("acore_world", "gossip_menu_option", "MenuID:OptionID", ["OptionText", "BoxText"]),
    ("acore_world", "acore_string", "entry", ["content_default"]),
]

RE_TUPLE_ID = re.compile(r"\(\s*(\d+)\s*[,)]")
RE_FIRST_ID = re.compile(r"\(\s*(\d+)\s*,")
RE_INSERT = re.compile(r"INSERT INTO `(\w+)`")


def cjk(s) -> bool:
    return bool(s) and any("\u4e00" <= c <= "\u9fff" for c in str(s))


def load_clean_ids(src_root: str, table: str) -> set:
    """从上游 base db_world SQL 解析干净主键集合（取 VALUES 元组首整型）。"""
    path = os.path.join(src_root, "data", "sql", "base", "db_world", f"{table}.sql")
    if not os.path.exists(path):
        raise SystemExit(f"缺少上游 base SQL: {path}")
    ids = set()
    with open(path, encoding="utf8", errors="replace") as f:
        for line in f:
            # 单行可能包含多个完整元组 (id,...),(id2,...)；取每个元组首整型
            if "INSERT INTO" in line or "(" in line:
                for m in RE_FIRST_ID.finditer(line):
                    ids.add(int(m.group(1)))
    return ids


def esc(conn, v) -> str:
    if v is None:
        return "NULL"
    return "'" + conn.escape_string(str(v)) + "'"


def extract_table(conn, clean_ids, schema_name, table, pk, cols, out_dir, stats):
    """导出一个表的中文行 → UPDATE SQL（按主键，幂等）。"""
    if ":" in pk:
        pk_cols = pk.split(":")
    else:
        pk_cols = [pk]
    select_cols = ",".join(pk_cols + cols)
    cur = conn.cursor(pymysql.cursors.SSCursor)  # 流式，避免大表占内存
    cur.execute(f"SELECT {select_cols} FROM {schema_name}.{table}")
    kept = skipped_custom = skipped_no_zh = 0
    out_name = f"{table}.sql"
    outf = open(os.path.join(out_dir, out_name), "w", encoding="utf8", newline="\n")
    outf.write(f"-- GSWXY Locale zhCN 1.0.0 —— {table}\n")
    outf.write(f"-- 来源: 耳语魔兽（只读提取，仅上游干净 ID 的中文本地化内容）\n")
    outf.write("START TRANSACTION;\n")

    for row in cur:
        keyvals = dict(zip(pk_cols, row[:len(pk_cols)]))
        texts = row[len(pk_cols):]
        where = " AND ".join(f"`{k}`={int(v)}" for k, v in keyvals.items())
        numeric_ok = all(str(v).isdigit() for v in keyvals.values())
        if not numeric_ok or int(list(keyvals.values())[0]) not in clean_ids:
            skipped_custom += 1
            continue
        # 只保留至少一列含中文的行
        pairs = []
        for col, val in zip(cols, texts):
            if cjk(val):
                pairs.append(f"`{col}`={esc(conn, val)}")
        if not pairs:
            skipped_no_zh += 1
            continue
        outf.write(f"UPDATE `{table}` SET {', '.join(pairs)} WHERE {where};\n")
        kept += 1

    outf.write("COMMIT;\n")
    outf.close()
    cur.close()
    stats[table] = {"kept": kept, "skipped_custom": skipped_custom,
                    "skipped_no_zh": skipped_no_zh}
    print(f"  {table:24s} 保留 {kept:>7} | 过滤自定义 {skipped_custom:>7} | 无中文 {skipped_no_zh:>7}")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--host", required=True)
    ap.add_argument("--port", type=int, default=3306)
    ap.add_argument("--user", required=True)
    ap.add_argument("--password", required=True)
    ap.add_argument("--src", required=True, help="锁定上游源码根（用于干净 ID 集）")
    ap.add_argument("--out", required=True, help="locale/zhCN 目录")
    args = ap.parse_args()

    conn = pymysql.connect(
        host=args.host, port=args.port, user=args.user, password=args.password,
        charset="utf8mb4", connect_timeout=15, read_timeout=600,
        autocommit=True,  # 只读 SELECT
    )

    out_world = os.path.join(args.out, "world")
    os.makedirs(out_world, exist_ok=True)
    stats = {}

    print("== 加载干净 ID 集（来自锁定上游 base SQL） ==")
    clean = {}
    for _, table, _, _ in TABLES:
        clean[table] = load_clean_ids(args.src, table)
        print(f"  {table:24s} {len(clean[table]):>7} 个上游 ID")

    print("== 只读提取 zhCN（基础列） ==")
    started = time.time()
    for schema_name, table, pk, cols in TABLES:
        extract_table(conn, clean[table], schema_name, table, pk, cols, out_world, stats)

    # manifest
    total_kept = sum(s["kept"] for s in stats.values())
    total_custom = sum(s["skipped_custom"] for s in stats.values())
    manifest = {
        "name": "GSWXY Locale zhCN",
        "version": "1.0.0",
        "language": "zhCN",
        "generated_at": time.strftime("%Y-%m-%dT%H:%M:%S+08:00"),
        "source": "GSWXY 耳语魔兽（只读提取）",
        "filter": "仅保留锁定上游 base SQL 中存在的主键；自定义 ID 一律排除",
        "strategy": "写基础列（与客户端语言无关），不写 _locale 表；DBC 保持 enUS",
        "tables": stats,
        "totals": {"kept": total_kept, "filtered_custom": total_custom},
        "upstream": "见 versions/upstream.json",
    }
    with open(os.path.join(args.out, "manifest.json"), "w", encoding="utf8") as f:
        json.dump(manifest, f, ensure_ascii=False, indent=2)

    print(f"== 完成: 保留 {total_kept} 行，过滤自定义 {total_custom} 行，"
          f"耗时 {time.time() - started:.0f}s ==")
    conn.close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
