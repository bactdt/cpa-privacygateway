# AGENTS.md

> 本文件是**内容目录**，不是百科全书。需要细节时，按下方指引跳转。

## 这是什么项目

`cpa` 是 **CLIProxyAPI（CPA）的隐私脱敏网关插件**：以 Go C 共享库形态被 CPA 宿主 `dlopen` 加载，在请求链路中把凭据/PII 脱敏为占位符，再在响应与 SSE 流中还原。目标是让上游模型服务看不到用户的敏感数据，同时不破坏对话语义。

## 如何开始

- 架构总览与分层规则：见 `ARCHITECTURE.md`
- 设计理念与操作原则：见 `docs/design-docs/core-beliefs.md`
- 设计文档索引：见 `docs/design-docs/index.md`
- 当前执行计划：见 `docs/exec-plans/active/`
- 已知技术债：见 `docs/exec-plans/tech-debt-tracker.md`
- CI 与框架判定：见 `docs/CI.md`
- 改造施工图（竣工后归档）：见 `docs/HARNESS-RULES.md`

## 常用命令

```sh
export PATH=/home/gh/.local/toolchains/go/bin:$PATH   # 本机 Go 不在 PATH
make build        # 产出 C 共享库 build/libprivacygateway.{so,dylib,dll}
make test         # 单元测试
make vet          # go vet
make lint         # 自定义品味不变量（文件大小/结构化日志/重复实现）
make coverage     # 覆盖率报告 + 下限校验
make package      # 构建并在当前目录产出 privacygateway_<ver>_linux_amd64.zip（CPA 解压即用）
make smoke        # 对解压后的交付物做 dlopen/init/register/脱敏/管理页面冒烟（需先有 make package 产出的 zip）
make check        # 本地门禁总入口（fmt-check + vet + build + lint + test + coverage）
```

## 目录地图

| 路径 | 职责 |
|---|---|
| `abi.go` | C ABI 导出、RPC 方法分发、注册/重配、能力声明、宿主日志回调 |
| `management.go` | 管理注册与分发骨架：内嵌 trace.html，capabilities/routes 可见性联动（TD-11） |
| `management_trace.go` | `/v0/management/privacygateway/traces` 数据路由（GET/DELETE）、查询解析与 trace 页面路由 |
| `plugin.go` | 四个拦截器 + 请求完成清理 + 并发/关键词/失败策略 |
| `state.go` | 请求级状态表（映射、流恢复器、准入计数、TTL 清理） |
| `config.go` | 插件配置解析与默认值（含 trace_* 开关/上限） |
| `metadata.go` | 插件元数据与 ConfigFields（自 abi.go 迁出） |
| `trace.go` / `trace_hooks.go` | 内存环形追踪缓冲与四拦截器阶段挂钩（仅内存、掩码预览） |
| `main.go` | c-shared 必需的 `main` 占位 |
| `shared_library_*.go` | 按平台取自身共享库路径 |
| `redact/` | **纯领域层**：检测、映射、JSON 遍历、流式还原、Finding/MaskPreview（观测旁路数据形状） |
| `internal/archtest/` | 结构测试：强制分层依赖方向 |
| `scripts/lint/` | 自定义 linter（T1/T2/T6） |
| `scripts/covercheck/` | 覆盖率下限校验 |
| `scripts/package/`、`packaging/` | 插件 zip 打包器、包内 README 模板、`trace.html`（内嵌管理页）、`panel.html`（仅随 zip 分发的 file:// 排障页） |
| `scripts/smoke/` | cgo 宿主模拟器：加载解压后的 .so 做交付物冒烟 |
| `docs/` | 设计文档、执行计划、技术债、CI 决策 |

## 硬性约束（不可违反）

- **分层依赖单向**：`main`（入口层）→ `redact`（领域层）；`redact` 禁止反向依赖 `main`，禁止导入宿主 SDK。
- **边界处解析数据形状**：进入 `redact` 的输入必须已是明确类型，禁止 YOLO 式探字段。
- **结构化日志**：禁止 `fmt.Print*` / `log.Print*`，统一用 `logHost(ctx, level, message, fields)`。
- **单文件 ≤ 500 行**。
- 违反以上约束会被 `make lint` / `make test` 拦截。

## 工作方式

- 变更前先读相关 `docs/`，变更后同步更新文档与 `docs/QUALITY_SCORE.md`。
- 提交粒度单一职责；commit 用 Conventional Commits 前缀。
- 计划写在 `docs/exec-plans/active/`，完成后移入 `completed/`。
- 任何交互式决策确认后必须写入仓库文档。
- 完成开发后执行 `python3 ../scripts/finish_task.py --message "..."` 收尾同步。
