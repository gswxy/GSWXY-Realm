# 更新日志

本项目遵循语义化版本（semver）。stable 通道只包含纯 `x.y.z` 版本；`-nightly.N` 后缀为测试版，绝不会作为稳定版推送。

## 1.1.0（未发布）

艾泽旅伴产品化收口版本。

### 品牌

- 中文品牌定名 **艾泽旅伴**，产品展示名「艾泽旅伴 · GSWXY Realm」；应用中心、桌面入口、安装向导、管理界面全部统一
- 全新应用图标：古老魔法传送门（双金环 + 铆钉石 + 拱心石）+ 旅伴萤火；矢量源与渲染脚本入库（`resources/icon/`）
- `com.gswxy.realm`、数据目录、升级路径保持不变，旧版 FPK 可原位升级

### 修复（P0）

- **安装向导参数此前从未生效**：管理密码、服务器名称、初始机器人数量写入的 `bootstrap.json` 没有任何代码读取。现在 Manager 启动时一次性校验、应用并安全删除（绝不覆盖已设置的密码）
- **Playerbot 统计此前查的是三张不存在的表**（错误被吞掉后显示 0）：统一改为按随机机器人账号前缀（RNDBOT*）联表统计，名字池读取正确的 `characters.playerbots_names`，SQL 错误如实上报
- **备份此前缺失整个 acore_playerbots 库**：现在完整备份四份数据（账号/角色/机器人/世界关键表）+ 逐成员 SHA-256 清单；恢复前校验完整性与版本兼容、自动快照当前状态、静默游戏服务器、单任务互斥、拒绝符号链接与超大成员、state.json 按机器合并（保留本机数据库端口）
- **配置接口字段缺失**：`/api/config/list` 此前不返回分组（Section）与说明（Comment），前端分组下拉一直是坏的；同时修复节标题解析器（此前从未解析成功，全部落 General），现按真实上游横幅样式解析（worldserver 591 项 64 组 / playerbots 891 项 38 组实测）
- **Playerbot 模板混入 22 个上游不存在的配置键**（EnableGuild / WorldChat / RandomBotLoginAtStartup 等，写了不生效）：五个模板全部重写为锁定上游（mod-playerbots@037c0141）的真实键；`recommended.json` 清除 30 个死键；冒烟测试新增键名防回归
- authserver 端口键名修正：上游为 `RealmServerPort`（此前文档/推荐值用了不存在的 `LoginServerPort`）
- 配置写入与 `RegenerateAll()`、Realm 地址落库等错误不再被吞掉；配置新增服务端类型校验（bool/int/float）；`0/1` 默认值不再被误判为布尔导致拒绝合法数值
- 重启接口补齐三个进程目标（此前前端三个重启按钮、后端只认 worldserver/all），数据库重启自动按依赖顺序停起
- 登出即轮换会话签名密钥（Bearer 令牌随之全部失效）；`GSRM_DEV_NOAUTH` 在 fnOS 生产环境拒绝启动
- BAT 启动器下载改 Bearer Blob 方式，修复 fnOS 桌面 iframe 内 401

### 新功能

- WebUI 全面翻新：统一 Toast/弹窗组件（移除全部 prompt/alert/confirm）；账号页正式表单（创建/改密/GM 等级 0–3/封禁/解封/角色列表）；配置中心 常用/高级/原始 三模式；概览页端口就绪度（区分进程存活与服务就绪）、最近事件、连接指引；日志页错误/警告过滤 + 自动刷新 + 诊断包导出；备份页任务进度 + 每日自动备份（30 份 / 24 GB 上限）+ 恢复影响确认；版本页 GitHub 在线更新检查（stable/nightly 通道，只读）
- 数据页支持 NAS 本地导入：把 Data.zip 放进 `downloads/manual/` 即可扫描校验导入，免浏览器上传 1.1 GB
- 概览页公网 IP 检测改为后台异步（首屏不再等待网络超时）
- `gswxy-manager version` 输出真实构建版本

### 构建与流程

- 版本唯一来源收敛到 `fnos/manifest`：nightly 构建 = manifest + `-nightly.<run_number>` 并同步改写 manifest；发布工作流新增版本一致性守卫（stable 必须纯 x.y.z）；FPK 文件名 / manifest / build-info.json 三方校验
- CI 新增 `go test -race`；Release 文案修正 MariaDB → MySQL 8 等历史错误
- 新增单元测试：安装引导消费、状态机断点续跑、备份归档防穿越/防篡改/合并语义、语义化版本比较（nightly < stable）、配置三层解析与真实 dist 解析、更新检查通道过滤

### 文档

- README 全面重写（品牌/截图/要求/安装/玩法/配置/备份/端口/构建/致谢）
- 新增 `CHANGELOG.md`、`docs/升级指南.md`、`docs/常见问题.md`、`docs/备份恢复.md`；部署指南随新界面更新

## 1.0.2

- 浅色主题、手机窄屏布局、Realm 公网/局域网双地址识别与 BAT 启动器
- 会话修复：Bearer Token 鉴权（fnOS iframe 兼容）、空值 cookie 修复

## 1.0.1

- 模块配置注入数据库凭据；模块配置按 AC 期望生成；冒烟测试校验 data/sql 布局

## 1.0.0

- 首个公开版本：AzerothCore + Playerbots 一体化发行版，内置 MySQL 8 运行时与中文数据，真机登录协议验证通过
