---
name: golang-testing
description: Go 测试设计与验证。Use when writing or fixing Go tests, table-driven tests, subtests, fakes, race checks, benchmarks, fuzzing, and coverage; useful for Discord bot business logic and handlers.
user-invocable: true
metadata:
  origin: affaan-m/everything-claude-code
  upstream-revision: ef648e01899ba3e8dc6371642deaaf64b4477775
  adaptation: project-local-condensed
---

# Go 测试规范

这是上游 `golang-testing` 的精简适配版，不是逐字镜像。来源与改动见 [技能说明](../README.md)，上游许可证见 [LICENSE](LICENSE)。

根据当前请求或 `$ARGUMENTS` 选择需要的测试，不为覆盖率数字增加没有行为价值的断言。

## 工作方式

1. 阅读被修改代码、已有测试、`go.mod` 和项目的测试入口。
2. 明确输入、预期输出、错误分支及副作用。修复 bug 时优先增加能复现问题的回归测试。
3. 适用时采用红—绿—重构：确认失败确实来自目标行为，做最小实现，再验证相关测试。
4. 先跑受影响包，再按改动范围运行全量测试、竞态检测或其他检查；保留真实失败输出。

## 用例组织

- 用表驱动测试覆盖正常、边界和失败情况；`t.Run` 名称描述场景，而不是仅编号。
- 设置失败或结果无法继续检查时用 `t.Fatal` / `t.Fatalf`；可独立检查的多个差异使用 `t.Error` / `t.Errorf`。
- 测试可观察行为，避免锁定实现细节；确有必要时允许测试包内函数，不强制为了测试导出 API。
- 用 `errors.Is` / `errors.As` 断言错误类别，按业务需要断言错误文本。
- 每个测试独立创建数据，不依赖其他测试或子测试先运行。CRUD 测试不要假设另一个用例创建了某个固定 ID。
- 仅对真正独立的用例使用 `t.Parallel()`；避免并行修改进程环境、全局状态或共享数据库。
- 根据项目的 Go 语言版本处理循环变量捕获，不无条件复制旧版本补丁。

## 隔离外部依赖

- 默认单元测试不访问 Discord、不发送消息、不注册命令，也不需要真实 Token。
- 通过小接口和简单 fake 隔离 Discord 调用、数据库边界、时钟与随机源；不 mock 每一个内部函数。
- 用 fake 记录调用次数、参数和响应顺序，并模拟权限不足、超时、限流、取消及存储失败。
- HTTP 客户端或处理器测试使用 `httptest`；对请求方法、路径、必要字段、响应状态及错误路径作断言。
- 真实服务集成测试需要显式启用和隔离的测试配置；缺少条件时明确跳过。不要把生产资源当作测试夹具。

## 清理、数据库与时间

- 帮助函数调用 `t.Helper()`；资源通过 `t.Cleanup()` 清理，文件使用 `t.TempDir()`。
- 数据库测试使用临时库、临时文件或事务隔离；每个用例独立初始化需要的数据和迁移。
- SQLite 的 `:memory:` 数据库按连接隔离。若通过 `database/sql` 测试，选择每测试独立的临时文件，或在理解串行限制后将独立测试池限制为单连接；不要假设默认连接池共享同一个内存库。
- 不用固定 `time.Sleep()` 猜测 goroutine 已完成；用 channel、等待机制、可注入时钟和有界截止时间。
- 为积分、冷却和过期逻辑注入时钟；测试边界时刻及并发更新，不真的等待冷却时间结束。
- 使用 `go test -race` 检查共享状态；它不能替代并发业务正确性的断言。

## 快照、覆盖率与性能

- 黄金文件存放在 `testdata/`；更新必须是明确操作，检查差异，不能仅为消除失败覆盖旧期望。
- 优先覆盖权限、数据一致性、重复事件和错误恢复等关键行为；覆盖率是定位遗漏的信号，不擅自设置 80%/100% 等硬门槛。
- 不用 `-tags=!generate` 试图自动排除生成代码。排除规则应遵循项目已有的包选择或覆盖率后处理，并解释排除范围。
- 基准测试的数据准备、状态重置和计时范围要明确，避免反复对已排序数据计时或让目标操作被编译器消除；报告硬件、工具链和命令。
- Fuzz 测试针对不可信输入的解析器和业务不变量，添加有效与无效种子；主动 fuzz 时指定一个包和一个目标，不对整个 `./...` 同时 fuzz。

## 常用检查

仅在已有模块和相应源码时运行；优先使用项目现成的测试脚本。

```sh
go test ./...
go test -race ./...
go test -coverprofile=coverage.out ./...
go tool cover -func=coverage.out
```

定向检查示例（必须将包路径和测试名替换为实际存在的目标）：

```sh
go test ./internal/example -run '^TestExample$' -count=1
go test ./internal/example -run '^$' -bench '^BenchmarkExample$' -benchmem
go test ./internal/example -run '^$' -fuzz '^FuzzExample$' -fuzztime=30s
```

- `go test -short` 只有在测试显式检查 `testing.Short()` 时才会跳过相应工作。
- CI 使用项目声明且受支持的 Go 版本；不要复制上游示例的固定旧版本或凭空创建发布流程。
- 输出中区分已通过、失败、未运行和条件性跳过；没有 Go 源码时说明不适用，不创建假模块凑通过结果。
