# 开发说明

写给要改代码的人：环境怎么配、怎么跑、怎么验证。

## 环境要求

**Go 1.24 或更高版本**，建议使用仍受支持的稳定版。依赖版本由 `go.mod` / `go.sum` 锁定，不要在安装步骤里留 `@latest`。

## 环境变量

参考 [.env.example](../.env.example)：

| 变量 | 说明 |
| --- | --- |
| `DISCORD_TOKEN` | Bot Token。通过环境变量或部署平台密钥服务传入 |
| `DISCORD_APPLICATION_ID` | 应用 ID，必须与 Token 所属应用一致 |
| `DISCORD_GUILD_ID` | 唯一允许操作的服务器 ID，先用测试服务器 |

程序**不读取 `.env` 文件**。真实 Token 不要写进源码、命令历史或聊天。Bash 里可交互式设置：

```bash
export DISCORD_APPLICATION_ID='替换为应用ID'
export DISCORD_GUILD_ID='替换为测试服务器ID'
read -rsp 'Bot Token: ' DISCORD_TOKEN
printf '\n'
export DISCORD_TOKEN
```

Token 会在启动时做形态校验：必须是三段式（`xxx.yyy.zzz`）。误填成 OAuth2 客户端密钥会直接报错，不会带着错误凭据往下走。

## 运行

**注册命令**——仅命令有增删改时才需要。它会创建或更新配置服务器里的 `purge-user`、`purge-status`、`search-posts`，执行前先确认 `DISCORD_GUILD_ID`。不会全局发布，也不会批量覆盖或删除其他命令。

```bash
go run ./cmd/bot register-commands
```

**启动**

```bash
go run ./cmd/bot
```

或先构建再运行：

```bash
mkdir -p bin
go build -o bin/bot ./cmd/bot
./bin/bot run
```

**行为说明**

- `./bin/bot --help` 不需要 Token，也不连接 Discord。
- 进程收到 SIGINT / SIGTERM 后停止后台任务、关闭连接。
- SDK 的原始诊断日志被禁用（其中可能含带交互 Token 的 URL）。应用自身输出的生命周期、错误与任务统计均已脱敏。

## 测试与验证

默认测试使用 fake、可控时钟与 `httptest`，不连接 Discord、不发送消息、不需要真实 Token。

```bash
go test ./...
go test -count=10 ./...
go vet ./...
go build ./...
go mod verify
```

**竞态检测**需要 cgo 与 C 编译器：

```bash
CGO_ENABLED=1 go test -race ./...
```

**输入 fuzz**（单包、单目标）：

```bash
go test ./internal/discordbot -run '^$' -fuzz '^FuzzParseTargets$' -fuzztime=10s -parallel=2
```

**已覆盖的重点**：完整分页与保护范围、两周边界及回退、父频道权限覆盖顺序、重复/越权/过期确认、取消、部分失败、REST 路径与审计理由、交互先 defer 后工作、长任务查询。

**真实端到端测试**必须另找隔离的测试服务器，并先确认操作范围：准备两个账号，分别在同一帖与其他帖留测试消息，走一遍预览、取消、确认、无权限。不要拿实际历史消息试删除——旧消息那条路径可以用离线时钟测。

## 代码结构

```text
Night-of-the-World/
├── cmd/bot/                # 启动、显式命令注册、退出
├── internal/
│   ├── config/             # 环境变量校验
│   ├── purge/              # 发言清理：分页、删除策略、确认与内存任务状态
│   ├── postsearch/         # 帖子检索：枚举、过滤、分页与内存结果缓存
│   └── discordbot/         # disgo 适配、实时权限计算、指令、按钮与自动补全
├── docs/                   # 使用说明与开发说明
└── .claude/skills/         # 开发辅助 skills
```

**测试边界**：`purge` 与 `postsearch` 不 import disgo，用 fake 即可完整测试；`discordbot` 的测试通过 `httptest` 拦截 REST 请求，验证路径、请求体与权限计算。

**可用 skills**：见 [.claude/skills/README.md](../.claude/skills/README.md)。同一份 skill 也接给了 Pi，配置在 [.pi/settings.json](../.pi/settings.json)。
