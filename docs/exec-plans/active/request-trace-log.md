# 请求脱敏追踪日志计划

> 日期：2026-10-11。状态：**需求阶段完成**（`.kiro/specs/request-trace-log/design.md` 与 `requirements.md` 已完成；tasks.md 未生成；未改源码）。

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
| TD-5 | 默认 `trace_enabled: true`、`trace_max_requests: 200`、`trace_max_events: 64`（最坏约 6 MiB） |
| TD-6 | 流式只保留一条原地更新的 `stream.summary` 事件，字段为流式计数接口的值 |
| TD-7 | 管理密钥默认只放内存，可选 `sessionStorage`；401/403 立即停止轮询并清除内存与 `sessionStorage` 中的密钥（宿主 5 次失败封禁 IP 30 分钟） |
| TD-8 | 记录宿主 `TraceID`，用于对照 gin 日志 |
| TD-9 | `pluginMetadata` 从 `abi.go`（499 行）迁到 `metadata.go` |
| TD-10 | 流式统计来源为 `stream-restore-bare-chunks` 的计数接口；不改 `redact/stream.go` 与 `InterceptStreamChunk` 的还原逻辑 |

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
| design.md | 已完成（2026-10-11；同日按流式协调修订，属性已回填需求编号） |
| requirements.md | 已完成（2026-10-11，22 条需求） |
| tasks.md | 未开始 |
| 实现与测试 | 未开始 |
| 真实宿主验收 | 未开始 |
