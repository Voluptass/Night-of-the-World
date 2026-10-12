# Go / Discord Bot 开发 Skills

本目录的三个项目级 skill 是给编程助手的工作指南，不是运行时依赖，也不会自动执行。

| Skill | 用途 |
| --- | --- |
| [golang-patterns](golang-patterns/SKILL.md) | Go 结构、错误处理、并发、资源管理及验证 |
| [golang-testing](golang-testing/SKILL.md) | 表驱动测试、fake、离线测试、竞态检测、覆盖率与 fuzz |
| [discord-go](discord-go/SKILL.md) | Discord 交互响应、权限、限流、凭据保护及持久化约定 |

## 使用

三个 skill 支持手动调用，也会按任务相关性自动加载。

```text
/golang-patterns 检查当前 Go 实现的错误处理和并发安全
/golang-testing 为命令处理逻辑补充离线测试
/discord-go 实现一个仅在测试 guild 注册的 /ping 命令
```

### 在 Pi 中使用

同一份 skill 也给 Pi 用，无需复制：[`.pi/settings.json`](../../.pi/settings.json) 把 `skills` 指向 `../.claude/skills`，两个工具共用同一目录，命令形式为 `/skill:<name>`。

## 许可证

`golang-patterns` 与 `golang-testing` 改编自 [affaan-m/everything-claude-code](https://github.com/affaan-m/everything-claude-code)，MIT 许可证保留在各自目录下的 LICENSE 文件。
