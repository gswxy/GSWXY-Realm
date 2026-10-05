#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
生成 GSWXY 中文机器人名字池（playerbots/names.sql）。

方法承自 GSWXY 耳语魔兽 tools/gen_pbots_zh.py 的方案：
名字 = 种族气质前缀(1~2字) + 官译风格音译中段/收尾，女名收尾偏
娜/莉/丝/薇/露/莎，男名收尾偏 斯/尔/德/洛/恩/托。
生成过程是纯程序化的（固定随机种子，可复现），不复制任何服务器数据。

输出:
  locale/zhCN/playerbots/names.sql        playerbots_names       (name_id, name, gender)
  locale/zhCN/playerbots/guilds.sql       playerbots_guild_names (name_id, name)
  locale/zhCN/playerbots/arena_teams.sql  playerbots_arena_team_names (name_id, name, type)

全部为 DELETE + INSERT（与上游模块 SQL 同一模式），幂等可重复导入。
"""
import argparse
import json
import os
import random
import time

random.seed(20261005)

RACE_HEADS = {
    "人类": ["洛", "米奈", "普罗", "乌瑞", "斯托", "雷马", "杜克", "格雷", "莫格", "莱恩",
             "图拉", "提里", "瓦里", "安度", "麦迪", "艾格", "塞拉", "瑞文", "霍格", "达尔"],
    "矮人": ["巴尔", "杜林", "格瑞", "索林", "托林", "弗力", "莫林", "达因", "洛肯", "库德",
             "布拉", "弗斯", "穆拉", "斯坦", "罗德", "铁兰", "山铎", "熔恩", "岩德", "铜须"],
    "暗夜": ["泰兰", "玛法", "伊利", "塞纳", "奥蕾", "艾露", "希洛", "黛莉", "鲁纳", "艾索",
             "兰达", "温蕾", "露娜", "瑟兰", "卡多", "奥恩", "弥拉", "希尔", "洛玛", "梵妮"],
    "侏儒": ["吉兹", "图克", "斯佩", "米洛", "比克", "兹尔", "温克", "里克", "波普", "蒂克",
             "诺姆", "泽塔", "库克", "菲兹", "邦克", "韦兹", "希姆", "达克", "莫兹", "齿轮"],
    "兽人": ["格罗", "萨尔", "杜隆", "卡加", "纳兹", "莫格", "奥格", "血斧", "战锤", "碎手",
             "雷克", "加尔", "祖尔", "克洛", "布洛", "加罗", "什尔", "怒风", "裂牙", "黑掌"],
    "亡灵": ["莫尔", "德里", "塞德", "克罗", "瓦格", "幽暗", "夜访", "腐影", "骨语", "葬火",
             "奥尔", "纳斯", "阿纳斯", "伯瓦", "希尔", "凋零", "噬魂", "暗语", "墓歌", "霜尸"],
    "牛头": ["血蹄", "雷角", "风鬃", "石栏", "烈日", "狼奔", "鹰风", "巨角", "荒野", "草原",
             "塔恩", "沃恩", "高岭", "霜蹄", "奔雷", "灰牛", "长须", "山峦", "旷野", "夏柯"],
    "巨魔": ["祖尔", "金", "森金", "沃金", "卡金", "赞达", "巴金", "努波", "瓦克", "加金",
             "塔卡", "里格", "金加", "基克", "扎卡兹", "古拉", "玛卡", "提克", "断骨", "猎首"],
    "血精灵": ["阿斯", "凯尔", "莉安", "塞恩", "维兰", "晨曦", "日光", "银月", "星辉", "远行者",
               "艾尔", "洛瑟", "塔莉", "费尔", "莱登", "晨锋", "辉翼", "曜焰", "血羽", "法瑟"],
    "德莱尼": ["阿卡玛", "努奥", "维伦", "玛尔", "泰穆", "先知", "圣光", "克乌", "艾克", "欧萨",
               "哈达", "约伦", "纳鲁", "塔拉", "泽拉", "库尔", "奥马尔", "艾欧", "晶莹", "流光"],
}

# 官译风格音译中段
MID = ["拉", "德", "尔", "斯", "玛", "里", "奥", "洛", "丹", "吉", "安", "希", "瓦", "图", "克",
       "罗", "萨", "格", "兰", "多", "维", "赛", "诺", "布", "弗", "托", "缪", "涅", "琴", "泽",
       "塔", "莱", "苏", "米", "贝", "恩", "海", "加", "科", "杜", "伊", "雅", "乌", "帕", "雷"]
MID2 = ["拉格", "德斯", "塔尔", "米隆", "萨里", "奥丁", "洛翰", "丹尼", "吉尔", "希尔",
        "瓦里", "图克", "克洛", "罗兰", "萨格", "兰蒂", "多维", "赛恩", "诺斯", "布莱",
        "弗林", "托尔", "缪斯", "涅罗", "琴恩", "泽尔", "莱恩", "苏雷", "米尔", "贝恩"]
FEMALE_TAIL = ["娜", "莉", "丝", "薇", "露", "莎", "黛", "珊", "娅", "蕾", "雅", "拉", "雅", "娃"]
MALE_TAIL = ["斯", "尔", "德", "洛", "恩", "托", "姆", "克", "顿", "森", "伦", "姆", "杜", "伦"]

GUILD_A = ["铁血", "晨曦", "暮光", "星辰", "远征", "永恒", "烈焰", "寒霜", "风暴", "圣光",
           "暗影", "雷霆", "苍穹", "破晓", "流沙", "灰烬", "黎明", "新月", "苍狼", "飞鹰"]
GUILD_B = ["公会", "远征军", "骑士团", "兄弟会", "议会", "军团", "守望者", "游侠", "旅团", "盟约",
           "守望", "之刃", "之环", "之誓", "之魂", "战团", "佣兵团", "学院", "教团", "哨卫"]

ARENA_A = GUILD_A + ["银月", "奥格", "暴风", "铁炉", "达纳", "幽暗", "埃索", "雷文", "斯坦", "奎尔"]
ARENA_B = ["战队", "猎手", "决斗者", "角斗士", "挑战者", "竞技者", "斗士", "征服者", "追猎者", "先驱",
           "之影", "之锋", "之牙", "之爪", "之翼", "之魂", "之光", "之怒", "之盾", "之刃"]

MAX_NAME_LEN = 12  # 数据库 varchar(24)，中文名保守限长


def gen_names(count: int):
    """生成 count 个唯一中文名（gender: 0=男 1=女）。"""
    names = set()
    pairs = []
    races = list(RACE_HEADS)
    while len(pairs) < count:
        race = random.choice(races)
        head = random.choice(RACE_HEADS[race])
        gender = random.randint(0, 1)
        tail = random.choice(FEMALE_TAIL if gender else MALE_TAIL)
        r = random.random()
        if r < 0.35:
            mid = ""
        elif r < 0.8:
            mid = random.choice(MID)
        else:
            mid = random.choice(MID2)
        name = (head + mid + tail)[:MAX_NAME_LEN]
        # 服务端禁三连字：检查无连续 3 个相同字符
        if any(name[i] == name[i + 1] == name[i + 2] for i in range(len(name) - 2)):
            continue
        if name in names:
            continue
        names.add(name)
        pairs.append((name, gender))
    return pairs


def write_names(out_path: str, count: int):
    pairs = gen_names(count)
    with open(out_path, "w", encoding="utf8", newline="\n") as f:
        f.write("-- GSWXY Locale zhCN 1.0.0 —— 机器人中文名池（程序化生成，非复制）\n")
        f.write("DELETE FROM `playerbots_names`;\n")
        f.write("INSERT INTO `playerbots_names` VALUES\n")
        rows = [f"({i},'{n}',{g})" for i, (n, g) in enumerate(pairs)]
        # 每行 500 个元组，控制单条 INSERT 体积
        for i in range(0, len(rows), 500):
            chunk = rows[i:i + 500]
            f.write(",".join(chunk) + ",\n" if i + 500 < len(rows) else ",".join(chunk) + ";\n")
    return len(pairs)


def write_guilds(out_path: str, count: int):
    seen, rows = set(), []
    guard = 0
    while len(rows) < count and guard < count * 200:
        guard += 1
        a, b = random.choice(GUILD_A), random.choice(GUILD_B)
        name = a + b
        if name in seen:
            continue
        seen.add(name)
        rows.append(name)
    with open(out_path, "w", encoding="utf8", newline="\n") as f:
        f.write("-- GSWXY Locale zhCN 1.0.0 —— 机器人中文公会名池\n")
        f.write("DELETE FROM `playerbots_guild_names`;\n")
        f.write("INSERT INTO `playerbots_guild_names` (`name_id`, `name`) VALUES\n")
        vals = [f"({i},'{n}')" for i, n in enumerate(rows)]
        f.write(",".join(vals) + ";\n")
    return len(rows)


def write_arena(out_path: str, count: int):
    seen, rows = set(), []
    guard = 0
    while len(rows) < count and guard < count * 200:
        guard += 1
        a, b = random.choice(ARENA_A), random.choice(ARENA_B)
        name = a + b
        if name in seen:
            continue
        seen.add(name)
        rows.append(name)
    with open(out_path, "w", encoding="utf8", newline="\n") as f:
        f.write("-- GSWXY Locale zhCN 1.0.0 —— 机器人中文竞技场队名池\n")
        f.write("DELETE FROM `playerbots_arena_team_names`;\n")
        f.write("INSERT INTO `playerbots_arena_team_names` (`name_id`, `name`, `type`) VALUES\n")
        vals = [f"({i},'{n}',{i % 3 + 1})" for i, n in enumerate(rows)]
        f.write(",".join(vals) + ";\n")
    return len(rows)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", required=True, help="locale/zhCN 目录")
    ap.add_argument("--names", type=int, default=100000)
    ap.add_argument("--guilds", type=int, default=400)
    ap.add_argument("--arena", type=int, default=300)
    args = ap.parse_args()

    out = os.path.join(args.out, "playerbots")
    os.makedirs(out, exist_ok=True)
    n1 = write_names(os.path.join(out, "names.sql"), args.names)
    n2 = write_guilds(os.path.join(out, "guilds.sql"), args.guilds)
    n3 = write_arena(os.path.join(out, "arena_teams.sql"), args.arena)
    print(f"names={n1} guilds={n2} arena={n3}")

    # 更新 manifest（若存在则合并）
    mpath = os.path.join(args.out, "manifest.json")
    manifest = {}
    if os.path.exists(mpath):
        manifest = json.load(open(mpath, encoding="utf8"))
    manifest["playerbots"] = {
        "names": n1, "guilds": n2, "arena_teams": n3,
        "method": "程序化生成（承自 gen_pbots_zh.py 方案，固定种子可复现）",
    }
    json.dump(manifest, open(mpath, "w", encoding="utf8"), ensure_ascii=False, indent=2)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
