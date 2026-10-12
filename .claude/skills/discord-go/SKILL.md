---
name: discord-go
description: 使用 Go 开发 Discord Bot / DC 机器人。Use for disgo or discordgo bots, slash commands, buttons, select menus, modals, intents, permissions, interaction responses, rate limits, persistence, and bot tests.
argument-hint: "[要实现、排查或审查的 Bot 功能]"
user-invocable: true
---

# Discord Bot（Go）开发约定

根据当前请求或 `$ARGUMENTS` 工作；这些是开发约定，不会自行创建应用或连接 Discord。Go 通用实现和测试分别参考项目的 `golang-patterns`、`golang-testing` skills。

## 默认方案与版本依据

- 新项目默认使用 `github.com/disgoorg/disgo`，通过 Gateway 接收事件，优先 Slash Commands；不为普通 Bot 引入对外 HTTP 服务。
- 如果项目已经使用 `github.com/bwmarrin/discordgo` 或其他方案，沿用现有依赖，不未经请求迁移 SDK，也不混用不同 SDK 的类型与方法。
- 以 `go.mod` 中的版本为准。新建项目时选择与 Go 工具链兼容的稳定版本并固定依赖，不把 `@latest` 留作每次运行的安装步骤。
- SDK 方法签名、事件类型和组件结构应核对该版本的官方文档、`go doc` 或源码；可用时先用 Context7 查资料，但不要把检索片段当作编译依据。
- 遵循现有目录结构。空项目可从 `cmd/bot` 和少量 `internal` 包开始，按功能增长拆分；不要一次生成空的复杂分层。
- 当前请求仅涉及 skill、文档或设计时，不初始化 Go 模块、不安装依赖、不申请 Token，也不启动 Bot。

## 指令、组件与响应

- Discord 要求收到交互后 **3 秒内发送首次响应**。会调用外部 API、数据库或排队的操作，应在耗时工作前及时 defer；不要把 defer 本身放进可能阻塞的任务队列。
- 每次交互只允许一次首次确认。首次响应或 defer 成功后，使用编辑原始响应或 follow-up；避免多个 goroutine 竞相首次响应。
- 交互 Token 的有效期为 **15 分钟**，不是无限续期。长任务应在有效期内更新状态；需要更久的任务采用持久化作业或后续查询，不继续复用过期 Token。
- 私密结果、权限拒绝及敏感错误优先 ephemeral；defer 时就决定原始回复的可见性，不能假设后续可以把公开原始回复变为私密。
- Slash Command、按钮、选择菜单和 modal 的响应类型并不完全相同。特别是要打开 modal 时，不能先 defer 再尝试把 modal 作为首次响应；核对该交互类型允许的流程。
- 组件 `custom_id` 使用清晰、可版本化的格式，但其中的用户 ID、资源 ID 和操作码都不是授权凭证。校验发起者、资源所属 guild、有效期和当前权限。
- 处理重复点击、过期组件、资源已删除及 DM 场景。guild-only 功能遇到私聊时明确拒绝，不假设成员或 guild 字段总存在。
- 对用户提供的文本配置 `allowed_mentions`，默认禁止不需要的群体、角色或用户提及；按 Discord 限制检查消息、embed 和组件长度。

## 权限、凭据与输入

- 仅使用正式 Bot 身份，不使用用户 Token 或 self-bot。
- `DISCORD_TOKEN` 从环境变量或部署密钥服务获取；缺失时清晰报错。不要在对话中索要完整 Token，也不要写入源码、测试夹具或文档。
- 不读取、打印或上传密钥文件来“验证配置”；日志过滤 Bot Token、交互 Token、带 Token 的 webhook URL 和请求 Authorization 头。
- 创建应用配置时使用占位符 `.env.example`，并忽略真实 `.env`、本地密钥和数据库文件。`.env` 文件不会被 Go 自动加载，若使用需明确加载方式。
- OAuth scopes、Gateway intents、Bot 权限和调用者权限是不同概念，分别最小化。Slash Command 不需要仅为解析命令而启用 Message Content intent。
- 不默认请求 Administrator，也不无条件开启成员、在线状态或消息内容等特权 intents；真正需要时说明用途和 Developer Portal 配置。
- 不只依赖命令菜单的默认权限：执行时验证调用者、Bot、guild/channel 范围及操作所需的角色层级。管理操作必要时让用户确认具体目标。
- SQL 参数化；不要把输入拼接进 shell。若功能需要下载用户 URL，再明确协议、地址范围、大小、超时和重定向限制。
- 若任务明确改为 HTTP interactions：对原始请求体验证 Discord 签名和时间戳头，处理 PING，拒绝无效签名；不能因为存在某个请求头就信任请求。

## 限流、连接与生命周期

- 复用 SDK 的 REST 限流器和 Gateway 心跳、恢复与重连机制；不另写无限重试循环或每次事件重建客户端。
- 尊重 429 和 `Retry-After`；额外重试必须有上限、截止时间和取消能力，避免与 SDK 内置重试叠加。
- 超时后不确定是否成功的发消息、扣积分等操作，不盲目重试；根据操作设计去重、结果查询或补偿。
- 不在 Ready 或重连事件中无条件重新注册全部命令。开发注册优先限定测试 guild；命令同步使用显式、可审查的步骤。
- 批量覆盖注册可能删除现有命令；全局发布或删除命令前核对目标和差异，并取得该操作的明确授权。创建 Bot 代码不等于获得操作线上服务器的授权。
- 耗时任务使用有界并发和可取消 context；区分应用关闭、首次响应截止时间及业务任务超时。
- 收到退出信号后停止接收新任务，取消或有界等待后台工作，关闭 Gateway 和持久化连接；不要依赖进程被强杀来清理。

## 业务状态与持久化

- handler 只做输入校验、授权、调用业务逻辑和生成回复；通过小接口注入存储、Discord 响应能力和时钟，支持离线测试。
- 仅在功能需要时添加存储。小型单实例项目可用 SQLite；已有数据库时沿用，不为了 Bot 默认引入 Redis、队列或微服务。
- guild 数据按 guild ID 隔离；唯一键、查询条件及组件操作均包含正确的租户范围。
- 积分、库存、抽奖或奖励发放使用事务/原子操作；需要去重时将交互 ID 等幂等键与业务状态在同一事务中持久化。
- 业务状态提交与 Discord 消息发送不是同一事务，分别处理失败；不能因为回复失败就重复奖励。
- 使用 UTC 存储时间，并注入时钟测试冷却；进程内缓存不作为要求重启后保留的数据的唯一来源。

## 验证与交付

先按涉及的功能补离线用例：

- 正常响应、参数无效、私聊中调用 guild-only 功能。
- 调用者或 Bot 权限不足、角色层级不允许、跨 guild 访问。
- 耗时操作先 defer，再完成或失败；首次确认不重复，响应错误得到处理。
- 重复事件/点击、过期组件、并发积分或冷却更新。
- 数据库失败、超时、取消、限流与退出清理。

已有 Go 应用时格式化改动，执行适用的 `go vet ./...`、`go test ./...`、`go test -race ./...` 和 `go build ./...`。无工具链、无源码或环境不支持时据实说明。

真实 Discord 验证需另行明确授权及测试 guild；先说明将注册哪些命令、发送什么内容或修改哪些资源。不要发送测试消息来替代离线测试，也不要把未运行的线上验证写成已通过。

交付说明列出配置变量名称、所需 scopes/intents/权限、命令注册方式，以及已执行与未执行的检查；所有凭据只展示占位符。

## 文档依据

- [disgo 源码及示例](https://github.com/disgoorg/disgo)
- [disgo 包文档](https://pkg.go.dev/github.com/disgoorg/disgo)（选择与项目匹配的版本）
- [Discord interactions](https://docs.discord.com/developers/interactions/receiving-and-responding)
- [Discord Gateway intents](https://docs.discord.com/developers/events/gateway#gateway-intents)
- [Discord rate limits](https://docs.discord.com/developers/topics/rate-limits)

平台规则可能更新；实现时重新核对官方资料，不把这份 skill 当作完整的 API 参考。
