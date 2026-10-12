# Night of the World

用 Go 写的 Discord 单服务器管理机器人。两项能力：**清理某个账号在帖子中的发言**，以及**检索论坛帖子**。

## 技术栈

| 层级 | 选型 |
| --- | --- |
| 语言 | Go 1.24 |
| Discord SDK | [disgo](https://github.com/disgoorg/disgo) v0.19.6（Gateway + REST） |
| 关键依赖 | `omit` v1.0.0、`snowflake/v2` v2.0.3 |
| 存储 | 无数据库。任务表与检索结果存放在进程内存 |
| 部署 | 单个二进制，无外部服务依赖 |
| 权限模型 | 每次操作都用实时 REST 数据重算，不依赖 SDK 缓存 |

## 项目架构

```text
Night-of-the-World/
├── cmd/bot/                # 启动、显式命令注册、优雅退出
├── internal/
│   ├── config/             # 环境变量校验
│   ├── purge/              # 发言清理：分页扫描、删除策略、任务状态
│   ├── postsearch/         # 帖子检索：线程枚举、条件过滤、结果缓存
│   └── discordbot/         # disgo 适配：REST、权限计算、交互响应
├── docs/                   # 使用说明与开发说明
└── .claude/skills/         # 开发辅助 skills
```

**分层原则**：`purge` 和 `postsearch` 是纯逻辑，各自定义所需的 `API` 接口，**不 import disgo**——因此可以脱离 Discord，用 fake 和可控时钟完整测试。`discordbot` 是唯一接触 SDK 的适配层，`cmd/bot` 只负责接线。

**单向依赖**：`cmd/bot` → `discordbot` → {`purge`, `postsearch`, `config`}。

## 核心功能

### 清理账号发言（`/purge-user`）

在一个帖子或公开线程里找出指定账号的全部回复，扫描完成后进入预览，由发起者确认再删。

- **匹配**：按用户 ID，不受昵称变更影响，已退服账号也能处理
- **扫描**：分页读取完整历史，未扫完不提供确认入口
- **保护**：首帖、系统消息、他人发言、其他帖子一概不动
- **删除**：两周内批量，超过两周逐条

### 检索论坛帖子（`/search-posts`）

在论坛或媒体频道中按标题关键词与 Discord 标签筛选帖子。只读操作。

- **关键词**：多词 AND，不区分大小写，只匹配标题
- **标签**：AND，带自动补全，只列当前论坛的标签
- **结果**：分页展示，保留 15 分钟

## 实现亮点

**权限实时计算，不走 SDK 缓存。** Bot 没有开启任何缓存标志，SDK 缓存里不会有成员数据。因此每次鉴权都重新拉取 guild、roles、member，按 Discord 的规则现算：

```text
基础权限（@everyone 角色 + 成员角色，Administrator 短路）
  → @everyone 的频道覆盖
  → 成员各角色的覆盖（allow 取并集 / deny 取并集）
  → 成员个人覆盖
  → 通信禁言：降级为 查看频道 | 读取历史
```

**归档与锁定分级授权。** 不是"归档就拒绝"：只归档、或只锁定，按常规三项权限处理；只有**同时归档且锁定**才额外要求 Manage Threads（管理线程）。操作者与 Bot 各自都要满足——操作者持有的 Administrator 不会自动传递给 Bot。

**快照式删除。** 预览时把命中的消息 ID 存成快照，确认后只删快照内的 ID。不边扫边删，预览之后新增的发言也不在范围内。

**两周边界的动态判断与降级。** Discord 只允许批量删除 14 天内的消息，混入一条超期的整个请求就是 400。留 1 分钟余量避免卡边界；批量请求返回"超期"时，自动把这一批降级为逐条删除。

```go
BulkAge    = 14 * 24 * time.Hour
BulkMargin = time.Minute
```

**内存任务表 + 归属校验。** 没有数据库。任务表、检索结果都在内存，带 TTL 与数量上限，且按发起者校验——只有本人能确认、取消、翻页。重启后任务不恢复，不会假装还在跑。

**检索的枚举策略。** Discord 没有帖子搜索接口，只能枚举后本地过滤：

```text
活跃线程      GET /guilds/{id}/threads/active              一次取回，按 parent_id 过滤
归档公开线程  GET /channels/{id}/threads/archived/public   按 archive_timestamp 游标分页
```

去重后按标题 AND、标签 AND 过滤，按发帖时间倒序。渲染时逐条检查字符预算（上限 1800），避免超过 Discord 的 2000 字符限制导致整条消息发送失败。

**输入解析有 fuzz 覆盖。** `ParseAccount` / `ParsePost` 直接吃用户输入（ID 与链接），由 `FuzzParseTargets` 覆盖。

## 快速开始

需要 Go 1.24 或更高版本，以及一个已装入服务器的 Discord 应用。

```bash
export DISCORD_APPLICATION_ID='应用ID'
export DISCORD_GUILD_ID='测试服务器ID'
read -rsp 'Bot Token: ' DISCORD_TOKEN && echo

go run ./cmd/bot register-commands   # 仅命令有增改时执行
go run ./cmd/bot
```

程序不读取 `.env`，上面是手动导出环境变量的方式。完整步骤见[开发说明](docs/development.md)。

## 文档

| 文档 | 读者 | 内容 |
| --- | --- | --- |
| [使用说明](docs/usage.md) | 服务器管理员 | 命令用法、权限要求、边界与限制 |
| [开发说明](docs/development.md) | 开发者 | 环境变量、本地运行、测试验证、代码结构 |

## 注意事项

- **删除不可恢复**。请先在隔离的测试服务器上验证。
- 启动 Bot 不注册命令，预览不删除消息。真正的删除必须由发起者亲自确认。
- 不要把真实 Token 写进源码、命令历史或聊天。

## 许可证

本仓库未选择开源许可证。`.claude/skills/` 下的两个上游 skill 保留各自的 MIT 许可证，与本项目无关。
