# GSWXY Locale zhCN —— 数据说明

## 定位

`locale/zhCN/` 是 GSWXY Realm 的中文本地化数据包，随 FPK 分发，首次初始化时由
Manager 按事务导入数据库。与上游数据的关系：

- **写基础列，不写 `_locale` 表**：中文直接写入 `creature_template.name` 等
  基础列，客户端无论 enUS/zhCN 都显示中文；Playerbots 的 AI 逻辑依赖英文
  DBC 键值，因此 DBC 保持 enUS 不动（这是上游生态的成熟做法）。
- **不破坏上游**：全部为 `UPDATE`（world 表）与 `DELETE+INSERT`（机器人名字池，
  与上游模块 SQL 同一模式），导入幂等，可重复执行；world 文件包在事务里，
  失败即回滚，不会留下半导入状态。
- **不含任何私有/运营数据**：账号、角色、IP、日志、封禁记录一律不涉及。

## 来源与过滤

数据由 `extractor/extract_world_zh.py` 从「耳语魔兽」运行库**只读**提取：

1. 以 `versions/upstream.json` 锁定的上游源码 `data/sql/base/db_world/*.sql`
   解析出每个表的「干净 ID 集」；
2. 只保留既属于干净 ID 集、又含中文的行 → 通用游戏内容的中文本地化；
3. 生产库中的自定义 NPC / 自定义物品 / 自定义任务 / 运营数据（主键不在上游集合中）全部排除。

## 目录

```
locale/zhCN/
├── manifest.json          版本、来源、统计
├── world/                 基础列中文（事务化 UPDATE）
│   ├── creature_template.sql
│   ├── item_template.sql
│   ├── quest_template.sql
│   ├── broadcast_text.sql
│   ├── acore_string.sql
│   └── ...
├── playerbots/            机器人名字池（程序化生成，非复制）
│   ├── names.sql          100,000 个中文名（含性别）
│   ├── guilds.sql         400 个公会名
│   └── arena_teams.sql    300 个竞技场队名
└── checksums/             每个数据文件的 SHA-256
```

## 机器人名字的生成

`extractor/gen_bot_names.py` 承自 GSWXY 耳语魔兽的 `gen_pbots_zh.py` 方案：
名字 = 种族气质前缀 + 官译风格音译 + 性别化收尾；固定随机种子，可完全复现，
规避服务端三连字校验。纯程序生成，不存在任何数据复制问题。

## 复现提取（维护者）

```bash
python3 locale/extractor/audit_source.py <host> <port> <user> '<password>'   # 只读审计
python3 locale/extractor/extract_world_zh.py --host ... --src <上游源码> --out locale/zhCN
python3 locale/extractor/gen_bot_names.py --out locale/zhCN
python3 locale/extractor/validate_locale.py locale/zhCN
```

连接串只经命令行传入，绝不写入仓库或任何文件。
