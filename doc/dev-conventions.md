# lagent 开发约定（实现者必读）

设计文档（权威）：https://claude.ai/code/artifact/98c757e7-247f-4fec-95a6-a60402f7fd30 。原始草案：`doc/design-framework.html`。

## 分层（import 只能向下）

| 层 | 目录 | 规则 |
| --- | --- | --- |
| core | `internal/core/{id,errs,clock,tenant,types}` | 只有类型与工具，不依赖其他内部包 |
| transport | `internal/transport/*` | 协议转换；只调用 orch，不 import `infra/repo`、`capability` |
| orch | `internal/orch/*` | 用例与调度；可用 rules、capability、infra 接口 |
| rules | `internal/rules/*` | 纯函数：只 import core 与标准库（`robfig/cron` 例外）；测试不需要数据库或网络 |
| capability | `internal/capability/*` | 模型看得见的工具；不得 import orch |
| infra | `internal/infra/<x>` 接口，`internal/infra/<x>/<impl>` 实现 | 实现子包只允许被 `internal/app` import |
| app | `internal/app` | 组装根；按配置选实现、按角色启动 |

跨租户读取只能经 `internal/infra/repo/scope`（仅 orch/admin、调度器、工作流清扫器可用）。

## 必须遵守

- **租户只来自 context**：`tenant.From(ctx)`；仓储自动按租户过滤，接口不接受 tenant 参数。队列 handler 的 ctx 已是该 job 租户的 system principal。
- **状态迁移只用 `repo.Update`**（读-改-条件写，版本冲突自动重试；fn 内校验迁移是否合法）。禁止「先查再写」。
- 「已被其他方处理」返回 `errs.CodeAlreadyResolved`，是正常结果，不是失败。
- 错误一律用 `internal/core/errs`，transport 按 code 映射 HTTP 状态。
- 时间用注入的 `clock.Clock`；rules 里时间通过参数传入。
- 每个 infra 实现都要有内存或本地替代；仓储实现必须通过 `internal/infra/repo/repotest` 契约测试。
- 新增 HTTP 路由：在自己的包里写 handler，用 `httpapi.Server.Mount(func(r chi.Router){...})` 挂到已鉴权的 `/v1` 下（在 `internal/app/ext_*.go` 里挂）。复用 `httpapi.WriteJSON / WriteError / ReadJSON / Page / RequireRole`。
- 新模块接入：在 `internal/app/` 新建 `ext_<name>.go` 或 `driver_<name>.go`，用 `init()` 调 `RegisterExtension` 或填 `storeDrivers / redisDriver / objectDrivers / llmDrivers`。**不要改 `app.go`、`drivers.go`**，需要改动时在汇报里说明。
- turn 生命周期观察用 `turn.Hooks`（`App.AddHook`），后台循环用 `App.OnStart`，队列 handler 用 `App.Handle`（类型常量在 `internal/infra/queue`）。
- 不要修改他人负责的目录。确需给 `internal/core/types` 加字段，只追加字段，不改已有字段，并在汇报里列出。
- 注释与周围代码风格一致：英文注释，说明「为什么」，引用设计文档章节号。

## 构建与测试

- Go 1.26（go.mod 已切换 toolchain）。依赖已下载；需要新依赖时 `go get <mod>@<ver>`，**不要运行 `go mod tidy`**（多人并行会冲突，最后统一整理）。
- 只构建/测试自己的包：`go build ./internal/<你的目录>/... && go test -race ./internal/<你的目录>/...`；`go vet` 同理。全量构建可能因别人进行中的代码暂时失败，不要去修别人的代码。
- 本机没有 Docker、MongoDB、Redis、PostgreSQL。Redis 用 `github.com/alicebob/miniredis/v2` 测试；真实数据库的集成测试用环境变量开关（如 `LAGENT_TEST_MONGO_URI`），未设置时 `t.Skip`。
- 本机有 HTTP 代理环境变量；本地 curl 测试前 `export NO_PROXY='*' no_proxy='*'`。
- 内存版全链路测试夹具：`internal/orch/orchtest`（`orchtest.New(t)` 得到 store、bus、queue、fake 模型、engine、session service）。fake 模型脚本见 `internal/infra/llm/fake`。
