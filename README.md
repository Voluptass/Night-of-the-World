# Night of the World — Discord 清理 Bot

使用 Go + [disgo](https://github.com/disgoorg/disgo) 开发的单服务器管理 Bot。第一项功能：**预览并确认清理一个帖子/公开线程中指定账号的回复**。

> 删除不可恢复。请先在隔离的测试服务器验证。运行 Bot 不会自动注册命令，预览不会删除消息；实际删除必须由任务发起者点击确认。

## 能做什么

```text
/purge-user account:用户ID或@用户 post:帖子链接或线程ID
/purge-status task:任务ID
/search-posts forum:论坛频道 keyword:标题关键词 [tags:标签ID]
```

### 清理指定账号的发言（`/purge-user`）

- 按用户 ID 匹配，不受昵称变化影响，也可处理已离开服务器的账号。
- 分页读取历史，不限于最近 100 条消息；扫描完整结束后才允许确认。
- 显示目标账号、服务器、帖子、数量和处理方式。仅发起者能确认、取消或查询任务。
- 保护首帖、系统消息、其他账号及其他帖子；只删除预览时保存的消息 ID，之后的新发言不受影响。
- 近期消息每批 2–100 条删除，超过两周或临近边界的消息逐条删除。老帖清理可能较慢。
- 检查操作者与 Bot 在**目标帖子父频道**的权限；确认和处理过程中重新检查。重复确认不会重复启动任务。
- 所有交互回复为 ephemeral（仅自己可见），禁用自动提及。日志不包含消息正文或凭据。

### 搜索论坛帖子（`/search-posts`）

- 在指定论坛或媒体频道中，按**帖子标题**关键词和 Discord 标签筛选帖子。
- 关键词可写多个，用空格分隔；**全部包含**才命中（AND），不区分大小写。
- 标签用自动补全选择，支持多个，用逗号分隔；同样要求**全部包含**。输入不存在的标签会被拒绝，不会静默放宽条件。
- 结果为 ephemeral 回复，每页 10 条，通过“上一页/下一页”按钮翻页；只显示标题与跳转链接，不读取帖子正文。
- 结果保留 **15 分钟**，Bot 重启后失效。仅发起者能翻页。
- 检索要求操作者与 Bot 都拥有该论坛的**查看频道**和**读取历史记录**权限。
- 枚举顺序：全服活跃线程中属于本论坛的部分，加上按归档时间倒序翻页的归档公开线程。

## 第一版的边界

- 支持配置服务器内的论坛帖子及公开线程，包括已归档或锁定的帖子；支持论坛/媒体帖子、文字/公告频道的公开线程。归档与锁定状态保持不变。
- 不支持整频道清理、私聊、私密线程或普通消息的“引用回复树”。不能把论坛频道 ID 当作帖子 ID。
- 不自动取消归档、解锁、加入服务器、调整权限或删除整个帖子。
- 每个帖子最多一个活动任务，全局最多两个活动任务（包括等待确认）。
- 单次预览最多扫描 **100,000 条**消息、保存 **50,000 条**候选，扫描最长 **5 分钟**。任一限制或分页错误都会使预览失败，**不会用不完整结果开始删除**。
- 预览完成后 **5 分钟**内确认。单次删除任务最长 **2 小时**；失败、超时或取消后保留部分处理结果，不伪报全部完成。
- 任务表仅在内存中，最多 100 条记录，结束后保留 30 分钟。Bot 重启会丢失任务与确认按钮的有效状态，不会自动续删；重新预览即可核对剩余消息。
- Discord 交互 Token 有效期只有 15 分钟，自动进度更新最多持续约 14 分钟；更长任务仍在后台处理，用 `/purge-status` 查询。
- 停止任务只能取消后续工作，已发送的请求可能仍会完成，已删除内容不能恢复。

## Discord 配置

1. 在 [Discord Developer Portal](https://discord.com/developers/applications) 创建应用与 Bot，记录 Application ID。
2. 将 Bot 安装到准备管理的服务器。OAuth scopes 选择 `bot`、`applications.commands`。
3. 在目标帖子父频道给 Bot **View Channel、Read Message History、Manage Messages** 权限；操作者同样需要这三项权限。
4. 处理**同时已归档且锁定**的帖子时，操作者和 Bot 还必须具备目标帖的 **Manage Threads（管理线程）** 权限。只有归档或只有锁定时，不额外要求此权限。Administrator / 服务器所有者按 Discord 的权限规则处理，但操作者的权限不会自动赋予 Bot。不需要 Message Content intent 或 Guild Members intent；程序仅请求普通的 Guilds intent。
5. 在 Discord“用户设置 → 高级”启用开发者模式，可以复制用户、服务器和线程 ID。也可以复制帖子内消息链接：

   ```text
   https://discord.com/channels/服务器ID/线程ID
   https://discord.com/channels/服务器ID/线程ID/消息ID
   ```

   两种链接都定位到线程，不会只删除链接中的那一条消息。确认前请核对预览范围。

命令菜单默认要求 Manage Messages；运行时仍核对目标帖权限，不会因命令能显示就跳过授权。

归档帖不能直接使用 Slash Command，请在同服务器的其他可用频道执行 `/purge-user`，把目标帖链接或 ID 填入 `post`；查询进度同样可在其他频道使用 `/purge-status`。Bot 只读取状态并删除获准的消息，不会为了清理而自动解锁或取消归档。线程状态和权限会在扫描、确认和后续处理时重新检查，权限不足时停止。规则依据：[Discord 官方线程文档](https://docs.discord.com/developers/topics/threads)。

## 本地运行

需要 Go **1.24 或更高版本**；建议安装仍受支持的稳定版本。依赖版本由 `go.mod` / `go.sum` 固定。

### 1. 设置环境变量

参考 [.env.example](.env.example)：

| 变量 | 说明 |
| --- | --- |
| `DISCORD_TOKEN` | Bot Token，使用环境变量或部署平台密钥服务 |
| `DISCORD_APPLICATION_ID` | 应用 ID，必须与 Token 所属应用匹配 |
| `DISCORD_GUILD_ID` | 唯一允许操作的服务器 ID；先使用测试服务器 |

程序**不会自动加载 `.env` 文件**。不要把真实 Token 写进源码、命令历史或聊天。Bash 中可以交互式设置：

```bash
export DISCORD_APPLICATION_ID='替换为应用ID'
export DISCORD_GUILD_ID='替换为测试服务器ID'
read -rsp 'Bot Token: ' DISCORD_TOKEN
printf '\n'
export DISCORD_TOKEN
```

### 2. 显式注册命令

下面的命令会创建/更新配置服务器中的 `purge-user` 和 `purge-status`，因此执行前确认 `DISCORD_GUILD_ID` 正确。它不会全局发布，也不会批量覆盖删除其它命令。

```bash
go run ./cmd/bot register-commands
```

### 3. 启动

```bash
go run ./cmd/bot
```

或者构建后运行：

```bash
mkdir -p bin
go build -o bin/bot ./cmd/bot
./bin/bot run
```

`./bin/bot --help` 不需要 Token，也不连接 Discord。运行进程收到 SIGINT/SIGTERM 后，会停止后台任务并关闭连接。SDK 的原始诊断日志被禁用，避免其中包含带交互 Token 的 URL；应用输出脱敏的生命周期、错误和任务统计。

## 操作流程

1. 在服务器中运行 `/purge-user`，传入目标账号和帖子。
2. 等待扫描完成。检查账号 ID、帖子链接、可清理数量、保留数量，以及“不可恢复”警告。
3. 点击“确认删除（不可恢复）”或“取消”。两周前的消息较多时，请耐心等待。
4. 通过 `/purge-status` 查看结果或取得新的按钮。无权限、过期、重复点击或其他账号的确认会被拒绝。

### 搜索帖子流程

1. 运行 `/search-posts`，选择论坛频道，填写标题关键词，可选填标签。
2. 等待枚举完成（帖子多的论坛需要数秒）。结果按发帖时间倒序，标题可点击跳转。
3. 用按钮翻页。标签输入框带自动补全，只列出当前论坛的标签。

搜索是只读操作，不会修改或删除任何内容。它只匹配**标题**，不搜索帖子正文。

统计中的“删除请求成功处理”不是精确的新增删除数量：批量接口对已不存在的 ID 也可能返回成功。单条请求明确返回 Unknown Message 时计入“已不存在”。超时或网络失败时可能存在不确定结果，它们仍包含在“剩余/未确认”中；重新预览核对，不要依赖一次请求的计数作审计事实。

## 离线开发与验证

默认测试使用 fake、可控时钟及 `httptest`，不会连接 Discord、发送消息或需要真实 Token。

```bash
go test ./...
go test -count=10 ./...
go vet ./...
go build ./...
go mod verify
```

在支持 cgo 且安装了 C 编译器的环境中运行竞态检测：

```bash
CGO_ENABLED=1 go test -race ./...
```

可选的短时输入 fuzz（单包、单目标）：

```bash
go test ./internal/discordbot -run '^$' -fuzz '^FuzzParseTargets$' -fuzztime=10s -parallel=2
```

重点测试覆盖：完整分页和保护范围、两周边界及回退、父频道权限覆盖、重复/越权/过期确认、取消、部分失败、REST 路径与审计理由、交互先 defer 后工作，以及长任务查询。

真实端到端测试必须另行选择隔离测试服务器并确认操作范围：准备两位账号在同帖/其他帖的测试消息，分别检查预览、取消、确认与无权限场景。不要用实际历史消息试验删除；旧消息路径可以通过离线时钟测试。

## 代码结构

- `cmd/bot/`：启动、显式命令注册、退出。
- `internal/config/`：环境配置校验。
- `internal/purge/`：分页、删除策略、确认与内存任务状态，不依赖 Discord SDK。
- `internal/postsearch/`：帖子枚举、标题/标签过滤、分页与内存结果缓存，不依赖 Discord SDK。
- `internal/discordbot/`：disgo 适配、实时权限计算、指令、按钮与自动补全。
- [.claude/skills/README.md](.claude/skills/README.md)：项目开发辅助 skills。
