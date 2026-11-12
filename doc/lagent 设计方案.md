# lagent 设计方案

Oct 19, 2026 · @Someone

## 1 背景与目标

lagent 是公司内部的 Agent 平台：把自然语言任务转成一串工具调用并执行，产出可直接使用的交付物。本方案以 `doc/design-framework.html` 为基础，从零给出可落地的工程设计：一个 Go 单仓、一个二进制、五层代码结构、按角色启动的四种进程。

### 1.1 定位

- 接入面：网页会话、飞书群 @提及、内部系统 HTTP、定时触发。
- 与对话助手的本质区别：它执行有副作用的操作，所以有风险的调用必须经人工审批，这条约束贯穿全部设计。
- 平台层只提供机制；业务 Agent（投放、创意、画像、分析、客服坐席）是数据库里的配置，新增一个业务 Agent 不改平台代码。

### 1.2 设计原则

| 编号 | 原则 | 落到工程上的含义 |
| --- | --- | --- |
| P1 | 计算无状态 | 进程内不缓存会话；每个轮次从库重建上下文，结束即释放 |
| P2 | 协调外置 | 租约、信令、队列、事件缓冲放 Redis；数据库只做记录读写 |
| P3 | 人工介入可恢复 | 挂起 = 写待办 + 写检查点 + 释放租约 + 结束；恢复是一次新的轮次任务，可落在任意实例 |
| P4 | 依赖可替换 | 所有外部依赖走 `infra` 接口，每个接口都有内存或本地实现 |
| P5 | 接口取能力交集 | 仓储只用游标分页、应用层生成 ID；对象存储只暴露对象语义 |
| P6 | 单一执行单元 | 子任务、流程步骤、定时任务都归结为一次标准执行轮次（新增，原文隐含） |
| P7 | 分层可机器检查 | 层间依赖规则写进 lint 配置，CI 拦截违规（新增） |

### 1.3 范围

```
```

| 一期在范围内 | 一期不做 |
| --- | --- |
| 会话式任务执行、事件流、审批与提问 | SSO、租户自助开通、租户间物理隔离 |
| 飞书接入与群内审批闭环 | 飞书之外的连接器（统一走 MCP） |
| 定时任务、声明式流程模板、断点续跑 | 图形化流程编辑器 |
| 工具注册、MCP 接入、检索源、Skill、记忆 | 向量库与自建 RAG 管道 |
| 对外 HTTP 接口 + Go SDK、逻辑多租户（§11） | 对公网开放的客服机器人 |

## 2 总体架构

一份代码、五层单向依赖、四种启动角色。执行轮次是中心：四个入口都汇聚到它，子任务与流程步骤也都是它的一次调用。

&#91;embedded content: 代码分层 · 5 层 + core + 进程外依赖\]

规则层与能力层平级，彼此不依赖；规则层连基础设施接口也不碰，数据由编排层取好后作为参数传入。虚线表示只依赖 `infra` 的接口包，具体实现只有 `app/` 组装根能看到。

### 2.1 部署形态

同一个镜像，按 `--roles` 启动不同角色。小规模时一个进程开全部角色，负载上来后拆开，代码不动。

| 角色 | 职责 | 加载的层 | 扩容 |
| --- | --- | --- | --- |
| api | REST、SSE、鉴权、提交即返回 202 | transport + orch/session + infra | 水平 |
| engine | 消费 `turn.run` / `turn.resume`，跑 Agent 循环 | orch/turn + rules + capability + infra | 水平 |
| worker | 流程推进、定时到期、归档重试、清理 | orch/workflow + scheduler + rules + infra | 水平 |
| feishu | 长连接、事件落库入队 | transport/feishu + orch/session + infra | 受飞书单应用连接数上限约束 |

**关键变化：API 不再直接调用引擎，而是经队列投递。** 网页、飞书、定时、审批恢复、流程步骤全部变成同一种队列任务，引擎只有一个入口。引擎崩溃时任务由 Asynq 重新投递，另一实例从检查点接管。

### 2.2 有状态依赖

| 依赖 | 承载 | 备注 |
| --- | --- | --- |
| 主库 | 会话、消息、轮次、待办、流程、配置、审计 | 默认 MongoDB（需副本集），支持 PostgreSQL |
| Redis | 事件流缓冲、执行租约、控制信令、队列 | 只做协调，丢失后不丢事实 |
| 对象存储 | 交付物、附件、超长工具结果 | S3 兼容 |
| 密钥存储 | 模型密钥、MCP 令牌、飞书凭证 | 只下发给 engine 与 feishu 角色 |

### 2.3 关键设计决策

1. **挂起即结束**：需人工介入时写待办、写检查点、释放租约、轮次状态置 `suspended` 后返回。待办处理后投递 `turn.resume`。原文时序图中「挂起时保留并续约租约、等审批信令」与 P3 矛盾：无人值守审批可能等一夜，实例不应一直持有它。
2. **准入与互斥分开**：会话是否可接新任务由主库 CAS（`idle → queued`）判定；执行互斥由 Redis 租约 + fencing token 保证。前者给用户精确的 409，后者防双写。
3. **子任务也挂起**：父轮次分发子任务后挂起，不在内存里同步等，全部子轮次终态后恢复。审批、提问、子任务共用一套挂起恢复。
4. **事件缓冲不是事实来源**：Redis Stream 只缓冲 24 小时；超出后客户端拉快照重置。
5. **两项待决策直接定**：Agent 循环自建、编排内核自研，理由见 §7。

## 3 目录设计

一个 Go module、一个二进制 `lagent`，`internal/` 下的一级目录就是五层。层名即包名，审代码时看 import 路径就能判断是否越层。

```text
lagent/
├── cmd/
│   └── lagent/main.go            # 唯一入口：lagent serve --roles=api,engine,worker,feishu
├── internal/
│   ├── core/                     # 共享内核：实体类型、ID、错误码、时钟接口（不含逻辑）
│   │   ├── id/  errs/  clock/
│   │   └── types/                # Session, Message, Turn, Event, Pending, Role ...
│   ├── transport/                # 接入层（Go 中 interface 是关键字，故叫 transport）
│   │   ├── httpapi/              # REST 路由、校验、鉴权、错误映射、幂等键
│   │   ├── sse/                  # 事件推送、Last-Event-ID 续传
│   │   └── feishu/               # 长连接维持、事件转内部请求（立即返回）
│   ├── orch/                     # 编排层
│   │   ├── session/              # 会话创建、消息提交、待办处理的用例入口（API 调它）
│   │   ├── turn/                 # 执行轮次：Run / Resume / Cancel
│   │   ├── workflow/             # 执行图调度、续跑、提升为模板
│   │   ├── subtask/              # 子任务分发、并发上限
│   │   ├── scheduler/            # 定时到期判定与投递
│   │   └── jobs/                 # 队列任务类型与 handler 注册
│   ├── rules/                    # 规则层：纯函数，只依赖 core
│   │   ├── permission/  approval/  compaction/
│   │   ├── assembly/             # 角色 → 系统提示词 + 工具名清单
│   │   ├── memory/  skill/  budget/
│   │   ├── sessionfsm/           # 会话与轮次状态机
│   │   ├── flowdef/              # 流程定义校验、fan-out 展开、步骤指纹
│   │   ├── schedule/             # cron + 时区 + DST + 重叠策略
│   │   └── artifact/             # 归档时机与命名约束
│   ├── capability/               # 能力层：模型看得见的东西
│   │   ├── tool/                 # Tool 接口、Registry、元数据
│   │   ├── builtin/              # 文件、检索、记忆、load_skill、ask_user、submit_result
│   │   ├── mcp/                  # MCP 客户端、工具发现与包装
│   │   ├── retrieval/            # 检索源 = MCP 工具 + 固定参数
│   │   └── connector/feishu/     # 飞书发消息、回话题
│   ├── infra/                    # 基础设施层：接口在包根，实现在子目录
│   │   ├── repo/                 # 接口 + mongo/ postgres/ memory/
│   │   ├── objectstore/          # 接口 + s3/ local/
│   │   ├── secret/               # 接口 + vault/ local/
│   │   ├── eventbus/  lease/  signal/  queue/   # 接口 + redis/ memory/
│   │   ├── llm/                  # 模型网关：接口 + anthropic/ openai/ fake/
│   │   ├── sandbox/              # 工作区：临时目录 + 低权限账号 + rlimit
│   │   └── telemetry/            # OpenTelemetry、slog、metrics
│   └── app/                      # 组装根：按角色注入实现、启停生命周期
│       ├── config.go  wire_api.go  wire_engine.go  wire_worker.go  wire_feishu.go
├── pkg/client/                   # 给内部系统用的 Go SDK（唯一对外可 import 的包）
├── api/
│   ├── openapi.yaml              # REST 契约，生成 server stub 与 pkg/client
│   └── events.schema.json        # 事件契约
├── seeds/                        # 内置层资产：角色、模板、Skill、风险基础表（随版本发布，只读）
├── migrations/postgres/          # PG 建表脚本；Mongo 索引在 repo/mongo 启动时确保
├── configs/lagent.example.yaml
├── deploy/
│   ├── compose/                  # 本地：Mongo + Redis + MinIO
│   └── k8s/                      # 四个 Deployment 共用同一镜像，只是 --roles 不同
├── test/
│   ├── integration/              # testcontainers 起真实 Mongo/Redis/MinIO
│   └── e2e/                      # fake LLM 脚本化回放的端到端用例
├── web/                          # 前端（独立构建，不在本文范围）
├── doc/
├── .golangci.yml                 # depguard 规则落实 R-1…R-5
├── Makefile
└── go.mod
```

### 3.1 几处关键取舍

- **新增 `core/`**：原文把实体模型放在 `rules/session`，但基础设施层的仓储也要用这些类型，会造成 infra → rules 的上向依赖。实体类型下沉到 `core/types`，只含字段不含逻辑，所有层都可以依赖。
- **新增 `orch/session`**：原文接入层直接调 turn。增加一层「用例入口」后，HTTP、飞书、定时三个入口共用同一套建会话与提交逻辑，入口差异只剩协议转换。
- **流程定义的纯逻辑下沉 `rules/flowdef`**：校验、fan-out 展开、指纹计算都是纯函数，且是最容易出错的部分，放在规则层可脱离环境单测；`orch/workflow` 只负责调度。
- **模型网关 `infra/llm` 自带 `fake/`**：按脚本回放模型输出，e2e 与轮次单测不依赖真实模型。
- **`seeds/` 与数据库分层**：内置层资产随二进制发布、只读；组织层、用户层资产在库里版本化。读取时按「用户 › 组织 › 内置」合并。

### 3.2 依赖规则（depguard 机械检查）

| 规则 | 内容 | 违反后果 |
| --- | --- | --- |
| R-1 | `rules/**` 只能 import `core/**` 与标准库 | 权限判定、压缩边界无法脱离环境单测 |
| R-2 | `transport/**` 不得 import `infra/repo`、`capability/**` | 鉴权与审计被绕过 |
| R-3 | 业务包只能 import `infra/<x>` 接口包，实现子包只允许 `app/` import | 存储引擎不可插拔，本地开发失去轻量替代 |
| R-4 | `capability/**` 不得 import `orch/**` | 工具反向驱动轮次，递归失控 |
| R-5 | `core/**` 不得 import 任何 `internal/` 其他包 | 共享内核带入依赖，分层失效（新增） |

子任务分发工具 `dispatch_subtask` 由 `orch/subtask` 实现并在组装时注册进 Registry，而不是放在 `capability/builtin`。这样 R-4 不被打破，子轮次的工具集只需不含它即可禁止递归。

## 4 模块设计

每个模块只给出职责、对外接口与关键约束。接口是实现阶段的契约，内部实现可以自由替换。

### 4.1 接入层 transport

| 模块 | 职责 | 约束 |
| --- | --- | --- |
| httpapi | REST 路由、请求校验、共享密钥鉴权、`Idempotency-Key`、错误映射 | 只调 `orch/session` 等用例入口；不含业务判断 |
| sse | 按会话订阅事件流，支持 `Last-Event-ID` 续传；缓冲已过期时返回 `reset` 事件让客户端拉快照 | 只读 `eventbus` |
| feishu | 维持长连接，事件落库并入队后立即返回；卡片按钮回调转为待办处理 | 回调内不等轮次；按 `event_id` 去重 |

### 4.2 编排层 orch

**session（用例入口）**：创建会话、提交消息、处理待办、取消。提交消息时以条件更新把会话由 `idle` 置为 `queued` 并写入 `active_turn_id`，失败即返回 409；成功后投递 `turn.run` 任务并返回 202。

**turn（执行轮次）**：系统唯一的执行单元。

```go
type Runner interface {
    // Run 从会话历史重建上下文并跑到终态或挂起
    Run(ctx context.Context, req RunRequest) (Outcome, error)
    // Resume 在待办已处理后继续，可落在任意实例
    Resume(ctx context.Context, turnID id.Turn, pendingID id.Pending) (Outcome, error)
}

type Outcome struct {
    Status  TurnStatus // completed | suspended | failed | cancelled | lost_lease
    Result  *StepResult // 流程步骤与子任务的结构化上报
}
```

内部拆成 `loop.go`（主循环）、`context.go`（重建 + 压缩）、`toolexec.go`（只读并发、副作用串行）、`checkpoint.go`、`emit.go`（事件产出）。**自建循环，不引入 Eino**，理由见 §7。取消不单独成为方法：由循环在迭代间检查持久标记、并监听信令取消 `ctx`。

**workflow（执行图）**：对外只暴露 `Start / Continue / Rerun / Cancel / Get` 五个方法，将来换 Temporal 不影响调用方。调度是事件驱动的：步骤终态时投递 `workflow.advance`，由 Worker 计算可派发的后继并以条件更新 `pending → dispatched` 领取。

**subtask**：实现 `dispatch_subtask` 工具，单次并行数上限（默认 4）。父轮次不在内存里同步等待子任务，而是挂起，全部子轮次终态后以 `Resume` 继续——与审批共用同一套挂起恢复机制。

**scheduler**：每 15 秒以条件更新领取到期任务（`next_run_at <= now` 且 `version` 匹配），投递即返回；多副本同时跑也只有一个能领到。

### 4.3 规则层 rules（全部纯函数）

| 模块 | 函数形状 |
| --- | --- |
| permission | `Decide(risk RiskLevel, mode PermissionMode, grants []StandingGrant, call ToolCall) Decision` |
| approval | `Transition(item Pending, action Resolution) (Pending, Verdict)`，Verdict 含 `already_resolved` |
| assembly | `Assemble(role Role, mem []MemoryEntry, skills []SkillHeader, b Budget) (Prompt, []ToolName, Truncations)` |
| memory / skill / budget | 分层合并、去重、按优先级截断；三者共享同一份注入预算 |
| compaction | `Boundary(msgs []Message, limit Tokens, reserve Tokens) (cut int, ok bool)`，只切在轮次边界 |
| sessionfsm | 会话、轮次、步骤、待办的状态迁移表与合法性校验 |
| flowdef | `Validate(def) error`、`Freeze(params, clock, rand) Frozen`、`Expand(def, frozen) Graph`、`StepKey(...) string` |
| schedule | `Next(cron, tz, after time.Time) time.Time`，DST 重复时点只取一次、缺失时点顺延 |
| artifact | 归档路径命名（`runs/{run}/{step}/{name}`）与保留期计算 |

判据不变：测试需要起数据库或网络的，就不属于这一层。时间与随机通过参数传入，不在函数内部读取。

### 4.4 能力层 capability

```go
type Tool interface {
    Spec() Spec          // 名称、描述、JSON Schema
    Meta() Meta          // 分类、风险等级、SideEffect、ReadOnly、NeedsReview、Source
    Call(ctx context.Context, in json.RawMessage, env CallEnv) (Result, error)
}

type Registry interface {
    Resolve(names []ToolName) ([]Tool, []ToolName /*missing*/)
    List() []Meta
}
```

- `CallEnv` 携带工作区路径、会话与轮次标识、凭证读取句柄，工具拿不到编排层对象。
- 三类来源（内置、连接器、MCP）注册后形态相同；MCP 工具默认 `external` 风险。
- 工具返回内容统一包裹 `<tool_data source="...">` 边界标记，超长结果自动落对象存储并只回引用。
- 内置工具一期清单：`read_file`、`write_file`、`list_files`、`run_command`、`search`（走检索源）、`remember`、`load_skill`、`ask_user`、`submit_result`。

### 4.5 基础设施层 infra

| 接口 | 关键方法 | 线上实现 | 本地实现 |
| --- | --- | --- | --- |
| repo | 按实体的 Get / Put / CAS / List(cursor) | MongoDB（默认）/ PostgreSQL | memory |
| objectstore | Put / Get / List / Delete / SignedURL | S3 兼容 | 本地目录 |
| secret | Get / Put / Rotate | 受管加密存储 | 本地加密文件 |
| eventbus | Append(session, ev) seq / Read(session, afterSeq) / Subscribe | Redis Stream | memory |
| lease | Acquire / Renew / Release（带 fencing token） | Redis | memory |
| signal | Publish / Subscribe(session) | Redis Pub/Sub | memory |
| queue | Enqueue(job, opts) / Handle(type, fn) | Asynq | 同步执行 |
| llm | Stream(ctx, Request) iter / Complete | Anthropic、OpenAI 兼容 | fake 脚本回放 |
| sandbox | Create(turn) Workspace / Exec / Cleanup | 临时目录 + 低权限账号 + rlimit | 同左（不降权） |

`repo.CAS` 是全局唯一的状态迁移写法：`CAS(id, expectStatus, mutate)`，对应「状态迁移一律条件更新」的共性规则，并在接口层面不提供「先读后写」的更新方法。`lease` 增加 fencing token：每次抢占递增，写消息时带上，仓储拒绝旧 token 的写入，给「续期失败即放弃」加一道存储侧保险。

## 5 核心流程

所有流程都是执行轮次加上不同的触发与收尾。轮次只有两个出口：完成，或挂起后结束；没有「在内存里等」的状态。

&#91;embedded content: 执行轮次生命周期 · 一个循环、两个出口\]

审批、提问、子任务全部走右侧这条路：写待办、释放租约、结束；事项处理后投递 `turn.resume`，由任一引擎实例从检查点继续。

### 5.1 其他流程概览

| 流程 | 触发 | 要点 |
| --- | --- | --- |
| 断连续传 | SSE 重连 | 按 `Last-Event-ID` 读增量；超出缓冲发 `stream.reset`，客户端拉快照 |
| 取消 | `POST /cancel` | 写会话取消标记（正确性）+ 发 Pub/Sub 信令（加速）；语义是停止推进，不是回滚 |
| 子任务 | 模型调用 `dispatch_subtask` | 创建子轮次并投递，父轮次以 `subtasks` 待办挂起；子工具集不含分发工具 |
| 模板启动 | 手动 / 定时 / 接口 | 前置校验 → 收参 → 冻结 → 展开 → 定图落库；校验失败不产生记录 |
| 步骤推进 | `workflow.advance` | 步骤终态触发；算可派发后继，CAS `pending → dispatched` 后以步骤角色投递 `turn.run` |
| 续跑 / 重新运行 | 使用者 | 都新建运行；续跑沿用冻结量、指纹命中即复用；含副作用步骤永不复用 |
| 失败与超时 | 四类失败源 | 退避重试不改指纹；策略为中止 / 跳过后继 / 继续；模型 < 步骤 < 流程超时 |
| 定时触发 | scheduler | 条件更新领取；重叠默认跳过并记录；停机错过不补跑；DST 只跑一次 |
| 交付物归档 | 工具产出文件 | 产出即归档，归档成功才算步骤成功；先归档后清工作区 |
| 上下文压缩 | 轮次开始 | 预留余量、边界按轮次对齐、摘要落库缓存；只改出站内容 |
| 飞书接入 | 群内 @ | 回调落库入队即返回；话题绑定会话；审批卡片回话题 |
| Skill 加载 | 模型调用 `load_skill` | 正文只在本轮生效、不写回历史；加载记审计 |

各流程的完整时序与异常分支沿用 `doc/design-framework.html` §9，只有上表标出的挂起、推进与续传三处按本方案修正。

### 5.2 共性规则

- 状态迁移一律 `repo.CAS`，接口里没有「先读后写」的更新方法。
- 「已被其他方处理」是正常态，返回已处理及结果，不返回冲突错误。
- 时间与随机在运行启动时冻结；规则层函数通过参数拿到 `clock`。
- 含外部副作用的步骤不复用、不重试、重新审批。
- 事件是唯一对外契约，序号是唯一顺序依据。

## 6 数据模型与接口

主库共 20 个集合（含 §8 新增的 tool\_calls 账本），分三类：运行记录（只追加或条件更新）、版本化配置（只新增版本、只停用不删除）、审计（只追加）。主键一律为应用层生成的 ULID，天然有序，可直接做游标。

### 6.1 核心实体

| 类别 | 集合 | 关键字段 | 写入方式 |
| --- | --- | --- | --- |
| 运行 | sessions | role\_id + role\_version、permission\_mode、memory\_spaces、channels、status、active\_turn\_id、cancel\_requested | CAS |
| 运行 | messages | session\_id、turn\_id、seq、role、content blocks、lease\_token | 追加 |
| 运行 | turns | session\_id、trigger、status、checkpoint、parent\_turn\_id、step\_run\_id、usage | CAS |
| 运行 | pendings | turn\_id、kind（approval / question / subtasks）、risk、脱敏参数、触发来源、status、resolution、resolved\_via | CAS |
| 运行 | compactions | session\_id、from\_seq、to\_seq、summary | 追加（缓存键 = 消息范围） |
| 运行 | artifacts | owner（turn / step）、object\_key、name、size、sha256 | 追加 |
| 编排 | workflow\_runs | def\_id + def\_version、frozen、status、origin\_run\_id、mode（start / continue / rerun） | CAS |
| 编排 | step\_runs | run\_id、step\_id、fanout\_item、step\_key、role\_id + version、status、attempts、result\_ref、reused\_from | CAS |
| 编排 | schedules | cron、tz、overlap\_policy、template\_ref、standing\_grants、next\_run\_at、version | CAS |
| 编排 | schedule\_runs | schedule\_id、planned\_at、outcome（ran / skipped / missed）、reason、session\_id | 追加 |
| 配置 | roles | id、version、layer、prompt、toolsets、skills、memory\_spaces、default\_mode、model | 新版本 |
| 配置 | workflow\_defs | id、version、layer、params、steps、enabled | 新版本 |
| 配置 | skills | id、version、name、description、body、attachments | 新版本 |
| 配置 | memory\_entries | space、key、content、priority | upsert |
| 配置 | mcp\_servers / retrieval\_sources | transport、secret\_ref、status / tool + fixed\_args | upsert |
| 配置 | risk\_overrides | tool、scope、level（只允许调严）；另有 feishu\_threads 记录话题 → 会话绑定与定向回复授权 | upsert |
| 审计 | audit\_logs | actor、action、target、turn\_id、payload\_digest | 追加 |

### 6.2 状态机

- **会话**：`idle → queued → running → (waiting | idle)`；`waiting → queued`（待办处理后）。
- **轮次**：`queued → running → completed | suspended | failed | cancelled`；`suspended → running`（Resume）。
- **步骤**：`pending → dispatched → running → succeeded | failed | skipped | cancelled | reused`。`dispatched` 与 `running` 不可合并，领取靠这一步 CAS。
- **待办**：`open → resolved | expired_notice`，不自动拒绝。

### 6.3 事件契约

事件是引擎对外唯一契约。每条事件形如 `{seq, session_id, turn_id, type, ts, data}`，`seq` 按会话单调递增（Redis `INCR` 生成，与 Stream ID 解耦）。类型一期固定为：

`turn.started` · `message.delta` · `message.completed` · `tool.requested` · `tool.started` · `tool.completed` · `pending.created` · `pending.resolved` · `artifact.created` · `turn.suspended` · `turn.completed` · `turn.failed` · `turn.cancelled` · `run.*` / `step.*`（流程）

Redis 事件流保留 24 小时，只是缓冲；事实来源是主库的 messages、turns、pendings。续传时若请求的序号已超出缓冲，服务端发 `stream.reset`，客户端拉 `GET /sessions/{id}` 快照后从当前序号继续。这修补了原文「断连超过 TTL」的空白。

### 6.4 REST 接口（v1）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| POST | /v1/sessions | 创建会话；内部系统必须显式传 `permission_mode` |
| GET | /v1/sessions/{id} | 会话快照：状态、最近消息、未处理待办、当前序号 |
| POST | /v1/sessions/{id}/messages | 提交任务，支持 `Idempotency-Key`；返回 202 + turn\_id |
| GET | /v1/sessions/{id}/events | SSE，支持 `Last-Event-ID` |
| POST | /v1/sessions/{id}/cancel | 幂等；语义为停止推进 |
| GET | /v1/turns/{id} | 轮次状态；`waiting_human` 与 `running` 区分 |
| GET / POST | /v1/pendings、/v1/pendings/{id}/resolve | 待办中心与处理；重复处理返回 200 + `already_resolved` |
| POST | /v1/workflows/{id}/runs | 启动模板运行；前置校验失败返回 422 且不产生记录 |
| POST | /v1/runs/{id}/continue、/rerun、/cancel | 续跑与重新运行是两个动作 |
| POST | /v1/runs/{id}/promote | 运行时图提升为模板草稿 |
| CRUD | /v1/roles、/skills、/workflow-defs、/schedules、/mcp-servers、/retrieval-sources、/memory | 配置管理；版本化资源只有新增版本与停用 |
| GET | /v1/artifacts/{id}/url | 签名 URL |

### 6.5 业务扩展点

业务 Agent 只通过五类配置接入：角色、工具集（引用已注册工具或 MCP 服务）、流程模板、记忆空间、Skill。角色只能收窄不能放宽：不能声明未注册工具、不能调低风险等级。新增平台能力（如多模态）才需要写代码，写在 `capability/builtin` 或 `infra/llm`。

## 7 技术选型、配置与可观测性

原文的两项待决策在此直接给结论：**Agent 循环自建，编排内核自研执行图**，两者都收窄对外接口，留好将来替换的余地。

### 7.1 选型

| 位置 | 选型 | 理由 |
| --- | --- | --- |
| 语言 | Go 1.23+ | IO 密集、大量等待；单二进制分发四种角色 |
| HTTP | 标准库 `net/http` + chi | 四个进程、单租户内网，Kratos / go-zero 收益有限 |
| 契约 | OpenAPI 3 + oapi-codegen | 服务端 stub 与 `pkg/client` 同源生成 |
| 模型 | anthropic-sdk-go、openai-go，封装在 `infra/llm` | 外层统一为流式迭代器；多模态留接口位 |
| MCP | 官方 Go SDK（modelcontextprotocol/go-sdk） | 无需自写协议；不满足时只在 `capability/mcp` 内补齐 |
| 飞书 | oapi-sdk-go v3（含长连接客户端） | 无需公网入口 |
| 队列 | Asynq | Redis 底座，支持唯一任务、重试、定时投递 |
| 主库 | mongo-driver v2（默认）/ pgx v5 | 仓储接口只取能力交集 |
| Redis | go-redis v9 | 事件流、租约、信令、队列四用 |
| 对象存储 | aws-sdk-go-v2 S3 | 本地用 MinIO 或目录实现 |
| 配置 | koanf：YAML + 环境变量覆盖 | 密钥只存引用，不写配置文件 |
| 日志 / 追踪 | `log/slog` + OpenTelemetry | 追踪里不记录提示词原文与密钥 |
| 测试 | 标准库 testing + testcontainers-go | CI 必须用真实依赖 |
| 分层检查 | golangci-lint depguard | R-1…R-5 机械拦截 |

### 7.2 为什么自建 Agent 循环

不可让步的需求是「挂起后可在其他实例恢复」。自建时这天然成立：挂起就是写检查点后返回，状态只有主库一份。引入 Eino 则多一份框架 State 要与会话对齐，且跨进程 interrupt / resume 尚需实测。循环本身约 1500 行，最费力的流式解析由模型 SDK 承担。若日后要换，`turn.Runner` 接口不变。

### 7.3 为什么暂不引入 Temporal

Temporal 的适用前提（3–10 条业务线、99.5% SLA、5–8 人团队）当前都不成立，且会与「一个步骤 = 一次轮次」的统一性冲突。自研执行图只对外暴露五个方法；触发重评的条件写死在路线图里：接入业务线 ≥ 3 或提出明确 SLA。

### 7.4 配置

```yaml
roles: [api, engine, worker, feishu]   # 也可用 --roles 覆盖
store:   { driver: mongo, uri: ${LAGENT_DB_URI} }
redis:   { addr: ${LAGENT_REDIS_ADDR} }
object:  { driver: s3, bucket: lagent-artifacts }
secret:  { driver: vault, ref_prefix: lagent/ }
llm:
  default: anthropic/claude-opus-5-5
  providers: { anthropic: { key_ref: llm/anthropic } }
limits:
  turn_max_iterations: 50
  subtask_parallel: 4
  fanout_parallel: 8
  timeouts: { model_call: 5m, step: 30m, flow: 6h }   # 必须严格递增，启动时校验
  lease_ttl: 30s
  event_ttl: 24h
```

启动时校验三层超时不等式与「租约 TTL > 3 × 续期间隔」，不满足即拒绝启动。上述数值是初始值，待压测确定。

### 7.5 可观测性

- **追踪**：一个轮次一条 trace，模型调用与工具调用各一个 span；workflow run 作为父 span 串起各步骤。
- **指标**：轮次时长与结局分布、token 用量按角色归集、队列积压、租约丢失次数、审批批准率与决策时长（盲批信号）、飞书在线连接数。
- **告警**：飞书连接全部掉线、归档失败、租约丢失率超阈值、待办积压超 24 小时。

### 7.6 测试策略

| 层级 | 对象 | 手段 |
| --- | --- | --- |
| 单测 | `rules/**` | 表驱动，无任何依赖；权限、压缩边界、指纹、DST 要求全分支覆盖 |
| 组件 | `orch/turn`、`orch/workflow` | memory 实现 + fake LLM 脚本 |
| 集成 | `infra/*` 实现 | testcontainers 起真实 Mongo / PG / Redis / MinIO，同一套契约测试跑所有实现 |
| 故障 | 跨实例场景 | 两个引擎进程；注入 Redis 断连、进程 kill、Pub/Sub 丢消息，验证取消与租约语义 |
| 端到端 | HTTP → 事件 | `pkg/client` 驱动，覆盖审批、断连续传、续跑 |

## 8 执行可靠性

核心问题是：引擎在任意一行代码崩溃后，重新投递的轮次必须既不丢工作、也不重复副作用。原稿只说「写检查点」，没有定义它是什么、何时写，这是最大的缺口。

### 8.1 检查点 = 已落库的消息 + 工具调用账本

不另设快照。消息本身只追加，重建上下文就是恢复。唯一需要额外记录的是「工具已发出但结果未落库」这段窗口，由新增集合 `tool_calls` 承担：

| 字段 | 说明 |
| --- | --- |
| id | 取模型返回的 tool\_use id，天然唯一 |
| turn\_id、session\_id、tool、input\_digest | 定位与审计 |
| side\_effect | 取自工具元数据 |
| status | `decided → started → succeeded / failed`，全部 CAS |
| idempotency\_key | `{turn_id}:{tool_use_id}`，透传给支持幂等的下游（MCP 工具参数、飞书 uuid） |
| result\_ref | 结果消息 id 或对象存储引用 |

### 8.2 写入顺序

一次工具调用必须按这个顺序，每一步先落库再发事件：

1. 模型回复（含 tool\_use）作为 assistant 消息落库 → 发 `message.completed`。
2. 权限判定后写 `tool_calls`，状态 `decided`；需人工则转挂起。
3. CAS `decided → started` → 发 `tool.started` → 执行。
4. 结果作为 tool\_result 消息落库，同一事务内 CAS `started → succeeded` → 发 `tool.completed`。

流式增量 `message.delta` 不落库，只走事件流；崩溃时丢弃未完成的模型回复，恢复后重新请求模型。这一步没有副作用，只多花一次调用。

### 8.3 崩溃恢复语义

| 恢复时看到的状态 | 只读 / 本地写 | 外部副作用 |
| --- | --- | --- |
| `decided` | 直接执行 | 直接执行（未发出） |
| `started`，无结果 | 重新执行 | **不重执行**；下游支持幂等键则带同一 key 重试，否则回填 `outcome_unknown` 结果并创建提问待办，由人确认是否已发生 |
| `succeeded` | 跳过 | 跳过 |

`outcome_unknown` 会如实告诉模型「该操作可能已执行」。宁可问一次人，也不把消息发两遍。

### 8.4 长轮次与队列超时

- Asynq 任务超时设为单次轮次上限（默认 30 分钟），重试次数 3。重试靠上述账本保证安全。
- 轮次内部另有迭代上限（50 次）与 token 预算（§10），先到先停。触顶时不是失败，而是以 `budget_exhausted` 挂起一个提问待办：「已用完预算，是否继续？」。
- 画像类小时级任务（G3）不应卡在一个轮次里等：工具提交外部作业后立即返回作业号，轮次以 `external_job` 待办挂起，作业完成回调或轮询到终态后 `turn.resume`。这是挂起机制的第四种用法，不必把租约和事件流 TTL 拉长到小时级。

### 8.5 运行中追加消息

原稿与上一版都是轮次运行时直接 409。对网页使用者体验很差，改为：运行中提交的消息写入 `inbox`（会话上的待注入消息列表），引擎在下一次迭代前一并取出并追加为 user 消息。只有内部系统调用仍保持 409，便于调用方处理。

## 9 安全与治理补充

原稿的安全模型只管「工具调用要不要审批」，没管工具内部能做什么、Agent 能写哪些持久状态、配置改错了怎么办。四处补上：

### 9.1 工作区出网控制

`run_command` 在进程级软隔离里运行，若可任意出网，那么「外部地址白名单」这条注入防护就被一句 `curl` 绕过。

- 工作区进程以专用低权限账号运行，主机防火墙按 UID 默认拒绝出网。
- 确需联网的命令（如安装依赖）走配置里的出口代理，代理按域名白名单放行并记录。
- `run_command` 的风险等级固定为「命令执行」，默认需审批；内部系统全自动模式下也不自动放行，除非角色未声明该工具。
- 密钥不以环境变量注入工作区；工具在引擎进程内用 `CallEnv` 取凭证，不下放给子进程。

### 9.2 记忆写入治理

`remember` 是唯一能让模型影响「下一次会话」的工具，也是注入攻击最想拿到的持久化入口：一次被注入，每次会话都带着。

- 记忆空间分两类：只读空间（管理方维护，如 `ad-ops:bidding-rules`）与可写空间（会话或用户级）。`remember` 只能写后者。
- 写入一条记忆按「本地写入」风险处理，写入内容记审计，并标注来源轮次。
- 记忆注入时同样包在数据边界标记内，不作为指令。

### 9.3 配置发布与回滚

角色、模板、Skill 实际上就是业务 Agent 的「代码」，却没有代码的发布流程。增加三样东西：

1. **草稿 / 已发布**两态：编辑只产生草稿，发布才生成新版本；新会话只绑定已发布版本。
2. **试运行**：草稿可在隔离会话里试跑，强制「逐项询问」权限模式。
3. **回滚 = 把旧版本重新发布为新版本**，不改历史。配置变更全部进审计，含前后 diff。

另外建议把组织层资产同时导出到一个 Git 仓库（`lagent export` / `lagent apply`），让业务方可以走 PR 审阅。数据库仍是运行时的唯一来源。

### 9.4 鉴权升级路径

一期共享密钥保留，但代码上一开始就把「调用方」做成一等概念：

- `httpapi` 中间件产出 `Principal{kind, id}`，一期只有 `shared_key` 与 `feishu_user` 两种；审计、待办、会话都记 `principal`。
- 内部系统一期即按调用方发放各自的密钥（仍是静态密钥，成本很低），并按密钥配置最高允许的权限模式。这样原 R12 的叠加风险直接消失：全自动只给显式开通的调用方。
- 二期接 SSO 时只新增一种 `Principal`，其余代码不动。

## 10 成本与容量

Agent 平台的主要成本是模型调用，且单个失控的循环就能烧掉大量 token。原稿只提到「成本归集」，没有任何止损与优化机制。

### 10.1 三级预算

| 级别 | 默认值（初始，待实测） | 触顶行为 |
| --- | --- | --- |
| 单轮次 | 50 次迭代或角色配置的 token 上限 | 挂起提问待办，由人决定是否继续（§8.4） |
| 单流程运行 | 模板声明，缺省为各步之和 | 未开始步骤不再派发，按部分成功 / 失败判定 |
| 角色 / 调用方每日 | 管理方配置 | 新轮次在准入时拒绝，返回明确原因 |

用量在每次模型调用后累加到 `turns.usage`，并以 Redis 计数器做日级汇总；判定逻辑是 `rules/budget` 里的纯函数。

### 10.2 模型限流与降级

- `infra/llm` 内置按提供方的全局并发上限（Redis 信号量），避免多个引擎副本一起触发 429。
- 429 / 5xx 按 `retry-after` 退避重试；角色可配置备用模型，连续失败后切换并在事件中标注。
- 模型拒答等停止原因统一映射为轮次失败原因，不当作正常回复。

### 10.3 缓存友好的上下文构造

无状态设计意味着每次迭代都重发完整上下文。如果前缀稳定，模型侧的提示缓存能让重发部分只按缓存价计费；如果不稳定，就全价重算。约束如下：

1. 装配输出必须确定：工具按名称排序，Skill 与记忆按固定键排序，系统提示词里不写当前时间等易变量。
2. 拼接顺序固定为：工具清单 → 角色提示词 → Skill 描述 → 记忆 → 压缩摘要 → 历史消息。越少变的越靠前。
3. 历史只追加不改写；`load_skill` 正文、当前时间等本轮信息放在末尾。
4. 压缩按大步长触发（一次压到预算的一半以下），而不是每轮小压一点；每次压缩都会让其后的缓存失效。
5. `infra/llm` 记录缓存命中 token 数作为指标；命中率异常下降即说明有人破坏了前缀稳定性。

### 10.4 容量估算方法

当前没有业务量数据，下面给的是算法和需要的输入，不是结论：

| 资源 | 估算公式 | 需要的输入 |
| --- | --- | --- |
| 引擎并发 | 同时运行轮次数 ≈ 峰值提交率 × 平均轮次时长（不含挂起时间） | 峰值每分钟任务数、P50 / P95 轮次时长 |
| 模型并发 | ≤ 引擎并发；受提供方限额约束 | 各提供方的并发 / TPM 限额 |
| Redis 内存 | 活跃会话数 × 单会话 24 小时事件量 × 平均事件大小 | 单轮次事件数（delta 占大头，可合并后再写） |
| 主库写入 | 每次迭代约 3–5 次写（消息、账本、轮次状态） | 峰值迭代率 |
| 对象存储 | 交付物数 × 平均大小 × 保留期 | 保留期（待定） |

建议在 M1 结束时用 fake LLM 做一轮压测，把上表各项的实测系数填回来。另外 `message.delta` 建议按 50–100 ms 合并后再写入事件流，事件数可以降一个量级。

## 11 多租户

一期实现**逻辑多租户**：共用部署与数据库，每条记录带 `tenant_id`，由仓储层强制过滤。不做物理隔离与自助开通。放在一期的原因是：租户字段要贯穿所有表、键和接口，一开始就加几乎没有额外成本，上线后再加则要迁移全部历史数据。这部分工作归入 M0。

### 11.1 租户模型

- **租户 = 公司内的业务线或部门**（如投放、创意、客服）。记录为 `tenants` 集合：id、名称、状态、配额、默认角色、交付物保留期。
- **每个请求恰好属于一个租户**，由 `Principal` 携带：内部系统的密钥绑定租户；飞书群在 `feishu_chats` 中绑定租户，未绑定的群拒绝并提示管理员；网页一期按登录密钥归属租户。
- **租户内两种角色**：成员（使用 Agent、处理自己的待办）与租户管理员（维护本租户的角色、模板、Skill、MCP、预算）。租户本身由平台管理员用 `lagent tenant create` 创建。
- **配置分层变为「内置 › 租户 › 用户」**：原来的「组织层」就是租户层。内置资产的 `tenant_id` 固定为 `_system`，对所有租户只读可见。

### 11.2 各层隔离点

| 位置 | 做法 |
| --- | --- |
| 主库 | 全部集合带 `tenant_id`，所有索引以它打头；仓储方法从 `ctx` 取租户，缺失即返回错误，不接受显式参数 |
| Redis | 键前缀 `t:{tenant}:`，含租约、事件流、信令频道、预算计数器 |
| 对象存储 | 前缀 `tenants/{tenant}/`；签发 URL 前校验交付物与请求同属一个租户 |
| 密钥 | 路径 `lagent/{tenant}/...`；各租户的 MCP 令牌互不可见 |
| 工具注册表 | 内置工具全局共享；MCP 服务与检索源按租户注册，接口改为 `Resolve(tenant, names)` |
| 风险等级 | 平台基础表 → 租户覆盖 → 用户覆盖，每一级只能调严 |
| 预算与限流 | 租户级日预算 + 并发轮次上限；引擎按租户公平领取，一个租户的批量任务不会占满引擎与模型配额 |
| 事件流 / 待办中心 | 订阅与查询时校验会话租户；待办中心只列本租户、且与自己相关的事项 |
| 审计与指标 | 全部带 `tenant` 维度；租户管理员只看本租户 |
| 工作区 | 仍为进程级隔离，目录按 `{tenant}/{turn}`；敏感租户可配专属队列 + 专属引擎副本，得到计算隔离，只改部署不改代码 |

### 11.3 防越权的工程手段

逻辑隔离的风险是某处忘了加过滤。不靠记忆，靠结构：

1. **租户入口只有一个**：`transport` 中间件把 `Principal` 放入 `ctx`；队列任务载荷必带 `tenant_id`，handler 入口恢复到 `ctx`。
2. **仓储层自动加条件**：Mongo / PG 实现在封装层统一拼 `tenant_id`，业务代码无法绕开。跨租户查询只存在于 `infra/repo/admin`，depguard 限定只有平台运维用例可引用（新增 R-6）。
3. **契约测试强制覆盖**：每个仓储方法都跑一遍「租户 A 写、租户 B 读不到」；e2e 始终以两个租户运行。
4. **ID 不可猜不算防护**：ULID 只是主键，每次访问仍校验租户。

### 11.4 一期不做

| 不做 | 将来如何升级 |
| --- | --- |
| 租户自助开通与管理界面 | 在现有 CLI 能力上加接口与页面 |
| SSO 与租户内细粒度权限 | 新增一种 `Principal`，角色从密钥属性改为目录组 |
| 租户间物理隔离、数据驻留 | 仓储实现按租户路由到独立库，接口不变 |
| 对外部客户的 SaaS 多租户 | 威胁模型不同（租户互不信任），工作区需要容器级隔离，属于另一个项目 |
| 计费出账 | 已有按租户的用量统计，对接财务系统即可 |

需要同步的地方：`core/types` 增加 `TenantID` 与 `Principal`；§6.1 的全部集合隐含 `tenant_id` 字段，并新增 `tenants`、`feishu_chats`、`api_keys` 三个集合。

## 12 管理后台

一期提供 Web 管理后台，覆盖配置、调试、运维三类工作。它和外部调用方用同一套 REST 接口，不直连数据库，管理操作同样走鉴权与审计。

&#91;embedded content: 管理后台页面结构 · 5 组 23 页\]

调试组是本节重点：业务 Agent 只是配置，配置的好坏只能靠跑起来看，所以平台必须让「改 → 试 → 看清为什么 → 发布」这个循环足够快。

### 12.1 技术方案

- **前端**：React + TypeScript + Vite，组件库 Ant Design。`web/` 下分 `apps/console`（管理后台）、`apps/chat`（使用者会话界面）与 `packages/api-client`（由 `api/openapi.yaml` 生成）。
- **托管**：构建产物用 `go:embed` 打进二进制，由 api 角色在 `/admin` 与 `/` 提供，部署不增加进程；本地开发用 Vite 代理到后端。
- **登录**：一期用密钥登录，`api_keys` 带 `role`（member / tenant\_admin / platform\_admin），换取 HttpOnly 会话 Cookie；二期接 SSO 只替换登录页。
- **后端**：管理接口在 `/v1/admin/*`，路由在 `transport/httpapi/admin`，用例在新增的 `orch/admin`。

### 12.2 配置功能

| 页面 | 能做什么 | 关键约束 |
| --- | --- | --- |
| 角色 | 编辑提示词，选工具集、Skill、记忆空间、默认权限模式、模型；草稿 → 试运行 → 发布 → 回滚；版本间 diff | 工具选择器只列已注册工具；风险等级只读展示 |
| Skill | Markdown 编辑正文、上传附件、预览注入后的名称与描述 | 名称与描述占用注入预算，超长在保存时提示 |
| 记忆空间 | 增删条目、调优先级、查看哪些条目由 `remember` 写入及其来源轮次 | 只读空间只有管理员可改 |
| 流程模板 | JSON 编辑器 + 只读 DAG 预览 | 保存时跑 `flowdef.Validate`，错误定位到字段；不做拖拽式编辑器 |
| 定时任务 | cron + 时区、重叠策略、常驻授权、暂停 / 恢复 | 实时显示未来 5 次触发时间（`schedule.Next`），DST 日标出 |
| MCP 服务 | 配传输与凭证、连接测试、同步工具列表 | 凭证只写不读，页面不回显；放宽风险需二次确认 |
| 检索源 | 选工具、填固定参数、试查询 | 显示被哪些模板依赖，删除前提示 |
| 风险等级覆盖 | 按工具设置租户级风险 | 只能调严 |
| 飞书群绑定 | 群 → 租户、默认角色 | 未绑定的群在列表里标出，等待认领 |

### 12.3 调试功能

| 工具 | 用途 | 实现要点 |
| --- | --- | --- |
| Playground | 用任意角色版本（含草稿）开会话，实时看事件流 | 会话标记 `debug=true`：强制「逐项询问」、不计入业务统计、单独预算 |
| 装配预览 | 不跑模型，直接看某个角色在某租户、某用户下的最终系统提示词、工具清单、截断记录与 token 数 | 直接调用 `rules/assembly` 纯函数；是分层设计的直接收益 |
| 轮次调试器 | 按时间线看一个轮次：每次模型请求的完整输入输出（含压缩后的出站内容）、工具调用账本、权限判定的输入与结论、待办、token 与耗时、trace 链接 | 需要记录模型请求原文：新增 `llm_calls` 集合，默认保留 14 天、按租户可配，写入前脱敏密钥 |
| 分叉重跑 | 在调试器里从某次迭代复制出一个调试会话，换角色版本或提示词后继续跑，并排对比两次结果 | 历史只追加，复制到该点即可；副作用工具在分叉会话里强制逐项询问 |
| 模板试运行 | 草稿模板 dry-run：显示冻结量、展开后的图、每步指纹、可复用的步骤；可选真实调试运行 | dry-run 只调 `rules/flowdef`，不产生运行记录 |
| MCP 工具测试 | 手填参数调用某个工具，看原始返回与耗时 | 有副作用的工具需二次确认并记审计 |
| 流程运行查看 | DAG 按状态着色，点步骤跳到对应轮次调试器；续跑 / 重新运行两个按钮分开 | 失败原因以可读文字展示，不只给堆栈 |

### 12.4 权限

| 角色 | 可见范围 |
| --- | --- |
| 成员 | 工作台；自己的会话与待办；可用已发布角色的 Playground |
| 租户管理员 | 上述全部 + 本租户配置、调试、审计、用量 |
| 平台管理员 | 跨租户运维、内置资产、系统状态；默认看不到租户会话内容，查看须填写理由并记审计 |

### 12.5 后端与排期

- 新增：`orch/admin`、`transport/httpapi/admin`、`transport/static`（go:embed）；集合 `llm_calls`；会话增加 `debug` 标记。
- 系统状态页的数据：队列积压（Asynq Inspector）、引擎副本心跳、飞书在线连接数、MCP 服务健康。
- 排期跟随后端：轮次调试器与 Playground 随 M1（它们也是 M1 自己的排障工具）；配置页、装配预览、MCP 测试随 M2；模板试运行、流程运行查看与运维页随 M3。

* [ ]

  ## 13 与原设计的差异与实施路线

本方案保留原文的全部原则与安全模型，改动集中在工程落地与两处与 P3 不自洽的地方。

### 13.1 差异清单

| 主题 | 原设计 | 本方案 | 理由 |
| --- | --- | --- | --- |
| 审批挂起 | 挂起时保留并续约租约，等审批信令 | 挂起即结束，释放租约，处理后投递 `turn.resume` | 与 P3 一致；无人值守审批可能等一夜 |
| 子任务等待 | 父轮次同步等待 | 父轮次挂起，子轮次全终态后恢复 | 同上，且复用同一套机制 |
| API → 引擎 | 直接派发 | 一律经队列 | 引擎只有一个入口，崩溃自动重投 |
| 会话互斥 | API 抢 Redis 租约 | 主库 CAS 准入 + 引擎抢租约 + fencing token | 准入给精确 409；存储侧拒绝过期写入 |
| 事件续传 | 未定义超出 TTL 的情况 | `stream.reset` + 快照重置 | 补上长断连的缺口 |
| 实体类型位置 | `rules/session` | `core/types` | 避免 infra 上向依赖 rules |
| 入口用例 | 接入层各自调 turn | `orch/session` 统一用例入口 | 三个入口共用建会话与提交逻辑 |
| 流程纯逻辑 | 全在 `workflow` | 校验、展开、指纹下沉 `rules/flowdef` | 最易错的部分可脱离环境单测 |
| 分层约束 | review 人工检查 | depguard 机械拦截，新增 R-5 | 人工检查会漏 |
| Agent 循环 | 待决（Eino / 自建） | 自建 | 跨实例恢复天然成立，状态只有一份 |
| 编排内核 | 待决（自研 / Temporal） | 自研，接口收窄为 5 个方法 | Temporal 前提不成立；重评条件写进路线图 |
| 配置校验 | 文字约定 | 启动时校验超时不等式与租约参数 | 把「须满足」变成「不满足就起不来」 |

第二轮完善又补了原稿完全没有涉及的五处：

- **工具调用账本**（§8）：崩溃后不重复副作用，结局不明时问人而不是重发。
- **外部作业挂起与运行中追加消息**（§8）：小时级任务不占轮次；网页不再因「运行中」被 409。
- **出网控制与记忆写入治理**（§9）：堵住绕过白名单与持久化注入两条攻击路径。
- **配置草稿 / 发布 / 回滚与按调用方发密钥**（§9）：后者直接消解原 R12。
- **三级预算、模型限流、缓存友好上下文**（§10）：对无状态重发上下文的成本影响最大。

### 13.2 实施路线

&#91;embedded content: 实施路线 · 5 个阶段、4 道门槛\]

M1 是风险最集中的阶段：租约、挂起恢复、取消的缺陷会在所有编排场景中放大，故障测试不过不进入 M2。各阶段工期需按人力另定。

### 13.3 已定事项

| 事项 | 结论 | 理由 |
| --- | --- | --- |
| 内部系统鉴权 | 一期按调用方发密钥，密钥绑定租户与最高权限模式 | 消解原 R12，且是多租户的前提 |
| 客服 Agent 形态 | 坐席助手；对外机器人另立系统 | 对外机器人要推翻内网与审批两项假设 |
| 定时任务绑定版本 | 模板标识 + 最新已发布版本，版本变更在自动化列表显式提示 | 既不静默改变行为，也不让改动失效；配合 §9.3 的发布流程，发布本身就是显式动作 |
| 「部分成功」判定 | 至少一个出度为零的步骤成功产出交付物或结构化结果 | 可机械判定，写在 `rules/flowdef` |
| 完成回调 | 一期实现：HMAC 签名的 webhook，经队列投递、指数退避重试 24 小时 | 约 1 人天；轮询仍是兑底，建议间隔 ≥ 10 秒 |
| 签名 URL 有效期 | 15 分钟，每次访问重新签发 | 链接被转发后很快失效 |
| 交付物保留期 | 默认 180 天，按租户可配；被模板运行引用的与运行记录同寿命 | 续跑复用的是引用，对象被删则复用失效 |
