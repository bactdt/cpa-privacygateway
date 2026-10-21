# 请求脱敏追踪日志计划

> 日期：2026-10-11。状态：**实现完成，本机真实宿主验收通过**（2026-10-21；`.kiro/specs/request-trace-log/` 规格 + 源码 + 测试 + 交付物冒烟全部完成；同日用真实 CPA 宿主 dlopen 加载完成 Requirement 22 等价验收；tasks.md 未生成（直接按阶段提交推进））。

## 1. 已确认决策（用户，2026-10-11）

| 编号 | 决策 | 取值 |
|---|---|---|
| RT-1 | 页面形态 | 替换 CPA 管理界面内嵌的"隐私脱敏检测"面板，改为日志页，按请求展示从接收到还原的每一步 |
| RT-2 | 敏感值展示 | 只显示掩码预览（如 `u***@example.com`、`138****5678`），不显示完整原值与完整正文 |
| RT-3 | 输出通道 | 只在插件内存缓冲中保存，经管理页面查看；不写 CPA 宿主日志，现有 `logHost` 错误日志不变 |
| RT-4 | 规格流程 | Design-first |
| RT-5 | 流式协调 | 流式还原由 `stream-restore-bare-chunks` 修复并替换 API；本规格的流式统计只消费其计数接口，不修改流式还原算法（用户批准设计时确认） |

## 2. 设计决策（用户已批准设计）

| 编号 | 决策 |
|---|---|
| TD-1 | 掩码由 `redact` 包纯函数 `MaskPreview` 完成，原值不出领域层 |
| TD-2 | 数据接口 `GET/DELETE /v0/management/privacygateway/traces`（宿主鉴权，不设 Menu）；避开宿主 `/v0/management/plugins/:id/...` 参数路由树 |
| TD-3 | 页面资源路由 `trace`，菜单"隐私脱敏日志"；移除内嵌 `panel` 路由 |
| TD-4 | `panel.html` 继续随 zip 分发，仅作 `file://` 独立排障 |
| TD-5 | 默认 `trace_enabled: true`、`trace_recording: false`、`trace_max_requests: 200`、`trace_max_events: 64`（最坏约 6 MiB） |
| TD-6 | 流式只保留一条原地更新的 `stream.summary` 事件，字段为流式计数接口的值 |
| TD-7 | 管理密钥默认只放内存，可选 `sessionStorage`；401/403 立即停止轮询并清除内存与 `sessionStorage` 中的密钥（宿主 5 次失败封禁 IP 30 分钟） |
| TD-8 | 记录宿主 `TraceID`，用于对照 gin 日志 |
| TD-9 | `pluginMetadata` 从 `abi.go`（499 行）迁到 `metadata.go` |
| TD-10 | 流式统计来源为 `stream-restore-bare-chunks` 的计数接口；不改 `redact/stream.go` 与 `InterceptStreamChunk` 的还原逻辑 |
| TD-12 | 记录开关默认关闭：注册后不采集，点页面「开始记录」（`POST /traces/recording`）或配置 `trace_recording: true` 才记录；开启清空旧记录，停止保留已有记录（2026-10-21 用户要求：日常不要在面板出现日志） |
| TD-11 | 管理可见性联动：`management_api` 能力、资源/数据路由可达性 = `panel.enabled` **且** `trace_enabled`；任一关闭即不声明能力并 404（2026-10-21 实施阶段确认，已回写 requirements/design，替代原"trace_enabled=false 时页面可达并显示关闭提示"的描述） |

## 3. 依赖

| 依赖 | 内容 | 影响 |
|---|---|---|
| `.kiro/specs/stream-restore-bare-chunks/`（bugfix，并行编写） | 修复"存在映射时流块全部被丢弃"（宿主每块传裸 JSON 载荷，占位符跨 delta 事件需文本层暂存）；拥有 `redact/stream.go` 与 `plugin.go` 的 `InterceptStreamChunk`；提供计数接口（块数、还原数、未知数、跨事件暂存字节、结束时冲刷） | 本规格的 `stream.summary` / `stream.flush` 与 Property 8 在其合入后实现；其余阶段不受影响 |

## 4. 未决事项

- iframe 能否复用管理中心已保存的管理密钥（需读管理中心源码确认）。
- after_auth 多次凭据尝试的计数需在真实宿主上核对。
- 旧面板"一直在请求中"/流式 524：已定位为上述流式缺陷（内容被丢弃 → 客户端无字节 → Cloudflare 524），与请求侧脱敏无关；修复后在真实宿主上复核。

## 5. 进度

| 项 | 状态 |
|---|---|
| design.md | 已完成（2026-10-11；同日按流式协调修订，属性已回填需求编号；2026-10-21 回写 TD-11 联动偏差） |
| requirements.md | 已完成（2026-10-11，22 条需求；2026-10-21 按 TD-11 回写 R11.12/R15.7 可见性条款） |
| tasks.md | 未生成（以 `docs/exec-plans/active/request-trace-log.md` 分阶段提交代替） |
| 实现与测试 | 已完成（2026-10-21）：阶段 1 掩码/观测/计数（`redact/finding.go`、`restore_stats.go`）、阶段 2 缓冲与阶段 hook（`trace.go`、`trace_hooks.go` + 拦接线）、阶段 3 生命周期与配置（`config.go`、`abi.go`）、阶段 4 管理路由与页面（`management_trace.go`）、阶段 5 打包/冒烟/配置样例、阶段 6 文档同步 |
| 交付物冒烟 | 已完成（`make smoke` 与 `make smoke-panel`：路由注册、资源通道取数据 404、完整管理路径 GET 含 `redact.done` 且不含原值、DELETE `cleared`） |
| 真实宿主验收 | **通过（2026-10-21，本机真实 CPA 宿主）**：dlopen 加载 v0.1.4 → trace 页 200（CSP/referrer/nosniff 齐）→ 旧 panel 资源 404 → 数据路由无钥/错钥 401、正确钥 200 → GET 含 before/after_auth + redact.done + stream.summary + response.restored + request.complete，4 个样本原值全部掩码 → DELETE `{"cleared":2}` → 资源通道取数据 404 → limits 200/64/64；线格式修复（`Method`/`Path` 默认键）经真实宿主解码验证 |
