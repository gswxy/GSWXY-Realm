#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
config-schema/generator/generate.py

解析 AzerothCore / mod-playerbots 的 .conf.dist 真实文件，生成
config-schema.json（设置项 + 分组 + 注释 + 默认值 + 类型）。

输出结构与 config-schema/translations/zhCN.json 叠加，UI 缺失翻译时
显示 upstream key + 原始说明 + 「待翻译」标记，绝不漏项。

用法:
    python3 generate.py --src <AC源码根> --out <输出json>
例如:
    python3 generate.py --src build/src \
        --out config-schema/generated/config-schema.json
"""
import argparse
import json
import os
import re
import sys

# conf.dist 实际位置（相对 AC 源码根，按 2026-10 上游真实布局）
CONF_FILES = [
    ("worldserver.conf", "src/server/apps/worldserver/worldserver.conf.dist"),
    ("authserver.conf", "src/server/apps/authserver/authserver.conf.dist"),
    ("playerbots.conf", "modules/mod-playerbots/conf/playerbots.conf.dist"),
]

RE_SECTION = re.compile(r"^#\s{0,2}([A-Z][A-Z0-9 ()&/\-]{2,60}[A-Z0-9)])\s*$")
RE_BLOCK_KEY = re.compile(r"^#\s{4}([A-Za-z][A-Za-z0-9._]*)\s*$")
RE_SETTING = re.compile(r"^([A-Za-z][A-Za-z0-9._]*)\s*=\s*(.*?)\s*$")
RE_DEFAULT = re.compile(r"^#?\s*Default:\s*(.+)$")
RE_DESC = re.compile(r"^#?\s*Description:\s*(.+)$")


RE_BOOLISH_KEY = re.compile(
    r"(?i)(enable|disable|allow|activat|use|show|hide|support|log|kick|ban|announce|cleanup|preload|squelch|anticheat|cheat)")


def infer_type(value: str, key: str = "") -> str:
    v = value.strip().strip('"')
    if v in ("0", "1", "true", "false", "yes", "no", "True", "False"):
        if v in ("true", "false", "yes", "no", "True", "False"):
            return "bool"
        # 0/1：仅当 key 具备布尔语义词时视为 bool，否则 int（如 RealmID）
        return "bool" if RE_BOOLISH_KEY.search(key or "") else "int"
    if re.fullmatch(r"-?\d+", v):
        return "int"
    if re.fullmatch(r"-?\d+\.\d+", v):
        return "float"
    return "string"


def parse_dist(path: str) -> dict:
    """解析一个 .conf.dist → {"sections": [...], "entries": [...]}"""
    sections, entries = [], []
    seen_keys = set()
    section = "General"

    block_keys = []      # 当前注释块声明的 key（多 key 共用一个说明）
    desc_lines = []      # Description 行
    default_value = None
    last_desc = ""       # 连续设置行（如 LoginDatabaseInfo/WorldDatabaseInfo）
    last_default = None  # 共享上一个说明块

    with open(path, encoding="utf-8", errors="replace") as f:
        for raw in f:
            line = raw.rstrip("\n")

            # 分节：# DATABASE & CONNECTIONS（全大写）
            m = RE_SECTION.match(line)
            if m:
                sec = m.group(1).strip()
                if len(sec) > 3 and not sec.startswith(("Default", "Description", "Example", "Important", "Note")):
                    section = sec
                    if sec not in sections:
                        sections.append(sec)
                block_keys, desc_lines, default_value = [], [], None
                continue

            # 注释块内的 key 声明：#    RealmID
            m = RE_BLOCK_KEY.match(line)
            if m:
                block_keys.append(m.group(1))
                continue

            m = RE_DESC.match(line)
            if m:
                desc_lines.append(m.group(1).strip())
                continue

            m = RE_DEFAULT.match(line)
            if m and (block_keys or desc_lines):
                # Default 行可能带 "- (说明)" 尾注
                default_value = re.sub(r"\s+-\s*\(.*$", "", m.group(1)).strip()
                continue

            # 实际设置行
            m = RE_SETTING.match(line)
            if m and not line.lstrip().startswith("#"):
                key = m.group(1)
                if key in seen_keys:
                    block_keys, desc_lines, default_value = [], [], None
                    continue
                seen_keys.add(key)
                use_desc = " ".join(desc_lines).strip()
                use_default = default_value
                if not block_keys and not use_desc:
                    # 无注释块的连续设置行：继承上一个说明
                    use_desc = last_desc
                    use_default = last_default
                last_desc = use_desc
                last_default = use_default
                default = use_default if use_default is not None else m.group(2).strip()
                entries.append({
                    "key": key,
                    "default": default.strip().strip('"'),
                    "type": infer_type(default, key),
                    "section": section,
                    "description": use_desc,
                    "source": os.path.basename(path),
                })
                block_keys, desc_lines, default_value = [], [], None
                continue

            # 普通文字/空行：重置块（仅在非注释行时）
            if line and not line.startswith("#"):
                block_keys, desc_lines, default_value = [], [], None

    return {"sections": sections, "entries": entries}


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--src", required=True, help="AC 源码根目录")
    ap.add_argument("--out", required=True, help="输出 config-schema.json（完整审计用）")
    ap.add_argument("--out-zh", help="输出 config-schema.zh.json（UI 中文层，扁平 key→{name,desc}）")
    ap.add_argument("--translations", help="zhCN.json 翻译文件")
    args = ap.parse_args()

    schema = {}
    total = 0
    for conf, rel in CONF_FILES:
        path = os.path.join(args.src, rel)
        if not os.path.exists(path):
            print(f"WARN: conf 不存在，跳过: {path}", file=sys.stderr)
            continue
        parsed = parse_dist(path)
        schema[conf] = parsed
        total += len(parsed["entries"])
        print(f"{conf}: {len(parsed['entries'])} 项, {len(parsed['sections'])} 组")

    os.makedirs(os.path.dirname(args.out), exist_ok=True)
    with open(args.out, "w", encoding="utf-8") as f:
        json.dump(schema, f, ensure_ascii=False, indent=1)
    print(f"共 {total} 个配置项 → {args.out}")

    # UI 中文层：key → {name, desc, pending}
    if args.out_zh:
        trans = {}
        if args.translations and os.path.exists(args.translations):
            trans = json.load(open(args.translations, encoding="utf-8"))
        zh = {}
        translated = 0
        for conf in schema.values():
            for e in conf["entries"]:
                t = trans.get(e["key"])
                if t and isinstance(t, dict) and t.get("name"):
                    zh[e["key"]] = {"name": t["name"], "desc": t.get("desc", "")}
                    translated += 1
                else:
                    zh[e["key"]] = {"pending": True}
        with open(args.out_zh, "w", encoding="utf-8") as f:
            json.dump(zh, f, ensure_ascii=False, indent=1)
        print(f"中文层: {translated} 已翻译 / {len(zh) - translated} 待翻译 → {args.out_zh}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
