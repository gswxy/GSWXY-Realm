#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
只读审计耳语魔兽生产库：确认 zhCN 数据实际分布（表、行数、示例）。
绝不读取 acore_auth / 玩家数据；只看 world 内容表与机器人名字池。
"""
import sys
import pymysql

conf = dict(
    host=sys.argv[1] if len(sys.argv) > 1 else "wow.gswxy.com",
    port=int(sys.argv[2]) if len(sys.argv) > 2 else 9116,
    user="root",
    password=sys.argv[3],
    charset="utf8mb4",
    connect_timeout=10,
    read_timeout=60,
    cursorclass=pymysql.cursors.Cursor,
)

db = pymysql.connect(**conf)
cur = db.cursor()

def q(sql, args=None):
    cur.execute(sql, args)
    return cur.fetchall()

# 1. 数据库清单（只显示，不进入 auth/characters）
print("== databases ==")
for (name,) in q("SHOW DATABASES"):
    print(" ", name)

for schema in ("acore_world", "acore_playerbots", "acore_characters"):
    print(f"== {schema}: 相关表 ==")
    rows = q(
        """SELECT table_name, table_rows FROM information_schema.tables
           WHERE table_schema = %s ORDER BY table_name""", (schema,))
    for name, n in rows:
        if schema == "acore_characters" and not name.startswith("playerbots"):
            continue  # 只关心机器人名字池，不触碰玩家角色数据
        print(f"  {name:50s} ~{n}")

# 2. 关键内容表的中文覆盖率
def cjk(s):
    if not s:
        return False
    return any('\u4e00' <= c <= '\u9fff' for c in str(s))

print("== 中文覆盖（world 基础列） ==")
for table, cols in [
    ("creature_template", "entry, name, subname"),
    ("item_template", "entry, name, description"),
    ("quest_template", "ID, LogTitle, QuestDescription"),
    ("gameobject_template", "entry, name"),
    ("npc_text", "ID, text"),
    ("page_text", "ID, Text"),
    ("gossip_menu_option", "id, OptionText"),
    ("broadcast_text", "ID, male_text"),
    ("points_of_interest", "ID, Name"),
    ("acore_string", "entry, content_default"),
]:
    try:
        total, zh = 0, 0
        sample = ""
        cur.execute(f"SELECT {cols} FROM acore_world.{table}")
        for row in cur.fetchall():
            total += 1
            if any(cjk(x) for x in row[1:]):
                zh += 1
                if not sample:
                    sample = str(row[1])[:30]
        print(f"  {table:28s} {zh}/{total}  例: {sample}")
    except Exception as e:
        print(f"  {table:28s} ERROR: {e}")

print("== playerbots 中文池 ==")
for schema, table, col in [
    ("acore_characters", "playerbots_names", "name"),
    ("acore_characters", "playerbots_guild_names", "name"),
    ("acore_characters", "playerbots_arena_team_names", "name"),
    ("acore_playerbots", "playerbots_speech", "text"),
]:
    try:
        cur.execute(f"SELECT COUNT(*), SUM({col} REGEXP '[一-龥]') FROM {schema}.{table}")
        total, zh = cur.fetchone()
        print(f"  {schema}.{table:32s} {zh}/{total}")
    except Exception as e:
        print(f"  {schema}.{table:32s} ERROR: {e}")

# ai_playerbot_texts zhCN 列覆盖
try:
    cur.execute("""SELECT COUNT(*), SUM(text_loc4 REGEXP '[一-龥]')
                   FROM acore_playerbots.ai_playerbot_texts""")
    total, zh = cur.fetchone()
    print(f"  ai_playerbot_texts.text_loc4            {zh}/{total}")
except Exception as e:
    print("  ai_playerbot_texts ERROR:", e)

db.close()
