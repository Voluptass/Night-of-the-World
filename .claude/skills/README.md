# Go / Discord Bot 开发 Skills

本目录提供三个项目级 skill，只在本仓库中使用。它们是给编程助手的工作指南，不是 Go 运行时依赖、Discord 插件或自动执行的脚本。

| Skill | 用途 |
| --- | --- |
| [golang-patterns](golang-patterns/SKILL.md) | Go 结构、错误处理、并发、资源管理及验证 |
| [golang-testing](golang-testing/SKILL.md) | 表驱动测试、fake、离线测试、竞态检测、覆盖率与 fuzz |
| [discord-go](discord-go/SKILL.md) | Discord 交互响应、权限、限流、凭据保护及持久化约定 |

## 使用

在本仓库目录中打开 Claude Code。三个 skill 都允许手动调用，也保留按任务相关性自动加载的默认行为；自动加载不保证每次都发生，需要明确应用时可手动指定：

```text
/golang-patterns 检查当前 Go 实现的错误处理和并发安全
/golang-testing 为积分和冷却逻辑补充离线测试
/discord-go 实现一个仅在测试 guild 注册的 /ping 命令
```

也可以直接说“按项目的 Discord Go skill 实现这个功能”。Skills 按需加载，并非每次对话把全文放入上下文。

如果当前会话没有识别新 skill，在项目目录重新打开会话，再检查 `/` 命令菜单。SDK 宿主还需启用项目设置来源和 Skill 工具；本次不修改宿主或全局配置。发现机制参考 [官方 Skills 文档](https://code.claude.com/docs/en/skills)。

### 在 Pi 中使用

同一份 skill 也可以给 Pi 使用，无需复制。仓库里的 [`.pi/settings.json`](../../.pi/settings.json) 把 `skills` 指向 `../.claude/skills`，两个工具共用这个目录，改一处即可，避免版本分叉。

```text
/skill:discord-go 实现一个 /ping 命令
/skill:golang-testing 为命令处理逻辑补充离线测试
```

`.pi` 是项目级资源，Pi 首次打开会要求确认项目信任；拒绝信任时只跳过这些项目 skill，不影响用户级 skill。Pi 忽略 `user-invocable`、`argument-hint` 等 Claude 专有字段，只读取 `name`、`description` 和 `disable-model-invocation`，所以同一文件可直接共用。

说明：`.claude/skills` 目录里的 `README.md` 没有 frontmatter，会被 Pi 当作普通文件跳过，不产生告警。

`discord-go` 为新项目默认选择 `github.com/disgoorg/disgo`，已有 SDK 则沿用。这里没有固定 disgo 版本或 Go 工具链版本：创建应用时核对兼容性并在 `go.mod` 中记录。

## 安装范围与权限

- 仅添加这三个 skill、两个上游许可证和本说明；未安装整个上游插件包。
- 没有可执行脚本、hooks、`allowed-tools` 授权或全局设置修改。
- Skill 内的命令是工作指南，不会因安装文件而自动执行，也不会绕过宿主权限设置。
- 未创建 Bot 源码、`go.mod` 或数据库，未安装 Go/SDK/linter，未连接 Discord 或注册命令。
- 后续访问真实 Discord、注册/覆盖命令或发送消息，需要明确说明目标与操作并取得对应授权。

## 上游来源与许可证

`golang-patterns` 与 `golang-testing` 基于 [affaan-m/everything-claude-code](https://github.com/affaan-m/everything-claude-code) 的对应 skill，采用中文精简适配，**不是未经修改的上游镜像**。

审阅的上游版本：`ef648e01899ba3e8dc6371642deaaf64b4477775`。

- [golang-patterns 原文](https://github.com/affaan-m/everything-claude-code/blob/ef648e01899ba3e8dc6371642deaaf64b4477775/skills/golang-patterns/SKILL.md)
- [golang-testing 原文](https://github.com/affaan-m/everything-claude-code/blob/ef648e01899ba3e8dc6371642deaaf64b4477775/skills/golang-testing/SKILL.md)
- [上游 MIT 许可证](https://github.com/affaan-m/everything-claude-code/blob/ef648e01899ba3e8dc6371642deaaf64b4477775/LICENSE)，已分别保留在 [golang-patterns/LICENSE](golang-patterns/LICENSE) 和 [golang-testing/LICENSE](golang-testing/LICENSE)。

主要适配：

- 将长篇示例改为按需应用的规则，保留错误处理、并发、接口、测试、基准和 fuzz 等核心内容。
- 不照搬不完整示例；明确池化缓冲区的所有权、channel 结束协议，以及 SQLite 内存库的连接隔离。
- 去掉固定旧版 Go / linter / CI 模板和武断的覆盖率门槛，改为按项目版本和业务价值选择。
- 修正通过 `-tags=!generate` 排除生成代码的误导性建议，要求主动 fuzz 选择单包单目标。
- 加入离线优先、不得依赖真实 Token、如实报告失败或未执行检查等约束。

`discord-go` 是本项目自定义内容，平台行为依据官方文档；不是 Discord 或 disgo 官方提供的 skill。两个上游 MIT 许可证不代表已为整个 Bot 项目选择开源许可证。

## 后续更新

这些文件不会自动同步上游。更新时选择并记录新的上游提交，先审阅差异及许可证，再合并适合本项目的规则；保留本地适配，不直接覆盖整个 `.claude` 目录。
