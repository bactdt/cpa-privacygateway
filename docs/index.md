# 文档索引

> 本文件索引 `docs/` 下的全部文档，并标注校验状态。
> 机械校验：`go run ./scripts/check-docs`（校验链接可解析 + 必填节 + 新鲜度）。

## 设计文档

| 文档 | 主题 | 校验状态 |
|---|---|---|
| [design-docs/index.md](design-docs/index.md) | 设计文档索引 | 已校验 2026-10-09 |
| [design-docs/core-beliefs.md](design-docs/core-beliefs.md) | 第一性原则、红线、反模式 | 已校验 2026-10-09 |
| [design-docs/placeholder-format.md](design-docs/placeholder-format.md) | 占位符格式与映射生命周期 | 已校验 2026-10-09 |
| [design-docs/abi-contract.md](design-docs/abi-contract.md) | C ABI 与 RPC 契约 | 已校验 2026-10-09 |
| [design-docs/fail-closed.md](design-docs/fail-closed.md) | 失败策略语义 | 已校验 2026-10-09 |
| [design-docs/redaction-algorithm.md](design-docs/redaction-algorithm.md) | 脱敏算法契约 | 开发快照 2026-10-09 |

## 执行计划

| 文档 | 主题 | 校验状态 |
|---|---|---|
| [exec-plans/completed/harness-migration.md](exec-plans/completed/harness-migration.md) | 当前改造计划 | 已校验 2026-10-09 |
| [exec-plans/completed/plugin-development-alignment.md](exec-plans/completed/plugin-development-alignment.md) | 插件开发文档对齐计划 | 已校验 2026-10-12 |
| [exec-plans/completed/concurrency-review-fixes.md](exec-plans/completed/concurrency-review-fixes.md) | 并发审查修正计划与验证记录 | 已校验 2026-10-13 |
| [exec-plans/active/privacy-contract-hardening.md](exec-plans/active/privacy-contract-hardening.md) | 隐私闭环与算法契约完善计划 | 开发快照 2026-10-09 |
| [exec-plans/active/plugin-packaging.md](exec-plans/active/plugin-packaging.md) | CPA 插件发布包计划 | 开发快照 2026-10-10 |
| [exec-plans/active/request-trace-log.md](exec-plans/active/request-trace-log.md) | 请求脱敏追踪日志计划 | 开发快照 2026-10-11 |
| [exec-plans/active/stream-restore-fix.md](exec-plans/active/stream-restore-fix.md) | 流式还原裸载荷丢块修复计划 | 开发快照 2026-10-11 |
| [exec-plans/active/tool-output-placeholder-leak.md](exec-plans/active/tool-output-placeholder-leak.md) | 工具输出误脱敏与流式占位符泄漏修复计划 | 开发快照 2026-10-24 |
| [exec-plans/tech-debt-tracker.md](exec-plans/tech-debt-tracker.md) | 技术债台账 | 已校验 2026-10-09 |

## 质量与流程

| 文档 | 主题 | 校验状态 |
|---|---|---|
| [QUALITY_SCORE.md](QUALITY_SCORE.md) | 质量评分与差距 | 已校验 2026-10-09 |
| [CI.md](CI.md) | 框架判定与 CI 决策 | 已校验 2026-10-09 |
| [HARNESS-RULES.md](HARNESS-RULES.md) | 改造施工图（竣工后归档） | 已校验 2026-10-09 |

## 参考资料

| 文档 | 主题 | 校验状态 |
|---|---|---|
| [references/sdk.md](references/sdk.md) | 宿主 SDK 速查 | 已校验 2026-10-09 |

## 顶层文档（仓库根）

| 文档 | 主题 |
|---|---|
| [../AGENTS.md](../AGENTS.md) | 入口地图 |
| [../ARCHITECTURE.md](../ARCHITECTURE.md) | 领域与包分层地图 |
| [../config.example.yaml](../config.example.yaml) | 示例配置 |
