# GSWXY Realm

> **fnOS 原生 AzerothCore Playerbots 一体化服务器发行版。**
> 安装即可开服：不需要 Docker、不需要 SSH、不需要编译、不需要安装 MySQL、不需要提取地图。

[![CI](https://github.com/gswxy/GSWXY-Realm/actions/workflows/ci.yml/badge.svg)](https://github.com/gswxy/GSWXY-Realm/actions/workflows/ci.yml)
[![Build](https://github.com/gswxy/GSWXY-Realm/actions/workflows/build.yml/badge.svg)](https://github.com/gswxy/GSWXY-Realm/actions/workflows/build.yml)

## 这是什么

GSWXY Realm 是一个 fnOS 应用（`.fpk` 安装包），安装完成后你会得到：

- 一台开箱即用的《魔兽世界》3.3.5a 服务端（AzerothCore + mod-playerbots）
- **机器人玩家（Playerbot）**：升级、副本、战场、公会全程陪你玩
- 全中文世界（NPC/物品/任务/广播文本/机器人名字，均来自 GSWXY zhCN 数据包）
- 中文 Web 管理界面：概览 / 服务器 / Playerbot / 账号 / 配置 / 数据 / 日志 / 备份 / 版本
- 内置数据库运行环境（MySQL Community 8.0，仅监听本机，零配置）

`Powered by AzerothCore + mod-playerbots`

## 安装（普通用户）

1. 在 [Releases](https://github.com/gswxy/GSWXY-Realm/releases) 下载 `GSWXY-Realm-*-x86_64.fpk`
2. fnOS 应用中心 → 手动安装 → 选择该文件
3. 按安装向导设置：服务器名称、管理界面密码、初始机器人数量
4. 打开 GSWXY Realm → 点击「开始初始化」
   - 数据库初始化与导入（自动）
   - 中文数据导入（自动，内置）
   - AC 客户端数据下载（约 1.1 GB，自动下载并校验；GitHub 直连不佳时会自动切换公共代理，也可手动导入）
5. 初始化完成后点击「启动全部」
6. 客户端 realmlist 指向你的 NAS：`set realmlist <NAS的IP>`，用 WebUI「账号」页创建的账号登录

整个过程不需要 SSH、Docker、MySQL、git、SQL、Linux 命令。

## 端口

| 端口 | 用途 |
| --- | --- |
| 18700 | Web 管理界面（fnOS 桌面图标打开） |
| 3724 | 登录服务器（客户端 realmlist） |
| 8085 | 世界服务器（客户端连接） |

局域网游玩需在 fnOS 防火墙/路由器放行 3724 与 8085（TCP）。数据库只监听 `127.0.0.1`，不对外。

## 数据目录（升级/卸载均保留）

```
var/                        （fnOS @appdata）
├── mysql/                  数据库数据目录
├── client-data/            dbc / maps / vmaps / mmaps / Cameras
├── config/                 用户配置（三层模型中的用户层）
├── backups/                备份档案（tar.gz + manifest.json）
├── logs/                   Manager / 审计 / 进程日志
└── state/                  初始化状态、版本信息、数据库凭据(0600)
```

卸载默认**保留全部数据**；只有你在卸载向导中明确选择「同时删除所有服务器数据」才会清除。

## Playerbot 快速上手

「Playerbot」页提供：

- 在线统计（真人 / 机器人 / 联盟 / 部落 / 等级与职业分布）
- 一键配置模板：**自然世界 / 单人陪玩 / 副本优先 / PvP 活跃 / 低性能**（应用前显示逐项 diff）
- 全部模板都映射到真实的 `AiPlayerbot.*` 配置，不发明不存在的功能

机器人说中文的条件与上游一致：账号 locale 为 zhCN（本发行版创建的账号已自动设置）。

## 配置中心

三级配置模型，配置项给足：

1. **上游默认**：来自锁定的 `worldserver.conf.dist` / `authserver.conf.dist` / `playerbots.conf.dist`
2. **GSWXY 推荐**：内置推荐值（如机器人数量、SOAP 仅本机等安全基线）
3. **用户覆盖**：你在 WebUI 改的值，升级不丢失；上游改默认值时会明确提示

- 图形界面覆盖全部 **1516 个**配置项（worldserver 591 / authserver 36 / playerbots 889），
  带中文说明与分组；未翻译的项显示上游原名 + 原始说明 + （待翻译）标记
- **原始编辑器**：直接编辑用户层 `.conf` 文本，支持搜索 / diff / 恢复默认

## 升级与版本

- 应用升级（新 FPK）替换运行时；数据库、配置、备份、客户端数据全部保留
- 客户端数据只在版本变化时重新下载（`resources.json` 版本比对）
- Core / Playerbots 只随 GSWXY Realm 发布更新（构建于 GitHub Actions，版本见 `versions/upstream.json`）
- 「版本」页显示 Core / 模块的精确 commit、数据版本、Locale 版本、构建 Run ID

## 开发与构建

```bash
# 完整构建（在 Linux/CI 上）
bash scripts/fetch-upstream.sh build/src      # 按 versions/upstream.json 锁定 SHA 拉取
bash scripts/build-core.sh                    # CMake + Ninja 编译 Core + Playerbots
bash scripts/build-manager.sh                 # Go 构建 Manager
python3 config-schema/generator/generate.py --src build/src \
  --out config-schema/generated/config-schema.json \
  --out-zh config-schema/generated/config-schema.zh.json \
  --translations config-schema/translations/zhCN.json
bash scripts/collect-runtime.sh               # 组装 fnOS payload（含 MariaDB 运行时）
bash scripts/gen-build-info.sh                # 生成 build-info.json
bash scripts/smoke-test.sh                    # 冒烟测试
bash scripts/build-fpk.sh                     # fnpack 打包 .fpk
bash scripts/verify-package.sh                # 包结构验证
```

自动化工作流：

| Workflow | 触发 | 作用 |
| --- | --- | --- |
| `ci.yml` | push / PR | Manager 单测、WebUI 检查、schema 生成测试、locale 校验、shellcheck、FPK 静态验证 |
| `build.yml` | 手动 / 关键路径变更 | 全量编译 + FPK + Artifact |
| `release.yml` | 手动（版本号 + stable/nightly） | 编译 + 发布 Release（fpk / sha256 / build-info / 许可证） |
| `upstream-check.yml` | 每日 | 上游 SHA 变化 → 开 Issue（不自动进 Stable） |

上游版本锁定在 [`versions/upstream.json`](versions/upstream.json)，任何正式 Release 都可据此精确复现。

## 许可证

- GSWXY Realm 自有代码：AGPL-3.0（见 [LICENSE](LICENSE)）
- AzerothCore / mod-playerbots：AGPL-3.0
- MySQL Community Server（内置运行时）：GPL-2.0 with FOSS exception
- 第三方组件清单：见发布资产 `THIRD-PARTY-LICENSES.txt`

本仓库不包含、不分发任何暴雪客户端原始资产；客户端数据（DBC/Maps/...）由用户端从
[AzerothCore 官方 Client Data](https://github.com/wowgaming/client-data) 发布页下载。
