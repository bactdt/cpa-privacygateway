# 失败策略（fail_closed）

> 更新：2026-10-09。现状依据 `plugin.go`、`redact/walk.go`。默认策略不等于所有错误均已被正确传递；未闭合分支见下表。

## 1. 原则

`fail_closed=true` 为默认值：已传播的请求解析、格式或初始化错误应拒绝请求。`false` 允许携带原文放行。当前实现仍存在旁路和错误吞掉的分支，不具备全面 fail-closed 保证。

## 2. 当前各分支行为

| 场景 | true | false |
|---|---|---|
| 新 RequestID 并发超限 | 始终 429 | 始终 429 |
| 命中拒绝关键词 | 始终 403 | 始终 403 |
| Body 超过 max_body_bytes | 413，释放状态 | 原样放行，释放状态 |
| 非空 SourceFormat 不支持 | 415，释放状态 | 原样放行 |
| SourceFormat 为空 | 原样放行 | 原样放行 |
| RequestID 为空 | 400，不扫描，释放状态；fail-open 时新建临时映射无状态脱敏（不写状态表）并告警 | 同左 |
| RequestID 已存在（before/after-auth） | 关键词/大小 guard 先行（复用条目命中不释放首请求状态），再按既有映射重新脱敏新正文（不重复计数）；格式不一致拒绝 400；无状态条目走完整脱敏并写回；失败不释放已有条目（TD-CPA-034，ownsEntry 区分准入/复用） | 同左 |
| enabled: false | 四拦截器直通，不产生状态条目（TD-CPA-017，2026-10-10 生效） | 同左 |
| 支持格式，Body 为空 | 原样返回 | 同左 |
| nonce/映射初始化失败 | 500，释放状态 | 原样放行，释放状态 |
| JSON 无效/深度超限 | 400，释放状态 | 原样放行 |
| 映射条数/字节容量耗尽 | 500 mapping_capacity_exceeded，整体不放行残留正文 | 原样放行 |
| 单字段检出超限（max_findings） | 500 max_findings_exceeded，整体不放行残留正文（与容量超限同构，TD-017 终版语义） | 原样放行 |
| 无敏感节点 | 原样返回，保留准入状态至 complete/TTL | 同左 |
| 非流响应无映射/JSON 无效/无命中 | 保持原响应 | 同左 |
| 流恢复尾部未结束 | 保留 pending 或 DropChunk | 同左 |
| ABI 层插件不可用（未注册/shutdown/panic） | 500 server_error 终止信封（TD-CPA-044，2026-10-10 生效；panic 在 `handleABIMethod` recover，不再依赖宿主熔断） | 空 Body 直通（宿主保留原请求体）；实例不可用时 fail_closed 取值沿用最近一次注册配置，从未注册过默认 true |
| ABI 层请求解码失败 | 400 invalid_request_error 终止信封（TD-CPA-044） | 空 Body 直通 |

空格式和重复 ID 必须结合宿主约束验收（TD-CPA-034，已实现上述行为，待集中验证）；映射超限错误已原子向上传递（TD-CPA-031）。响应恢复发生在上游调用之后，不能通过请求 fail_closed 开关补救原文已经发送。

## 3. 业务拒绝与 RPC 错误

| 拒绝场景 | HTTP status | ResponseBody.error.type |
|---|---|---|
| 并发超限 | 429 | rate_limit_exceeded |
| 关键词命中 | 403 | permission_error |
| 体积超限 | 413 | invalid_request_error |
| 非空格式不支持 | 415 | invalid_request_error |
| JSON 解析失败 | 400 | invalid_request_error |
| 映射初始化不可用 | 500 | server_error |
| 映射容量耗尽 | 500 | mapping_capacity_exceeded |
| max_findings 超限 | 500 | max_findings_exceeded |
| ABI 层插件不可用/panic | 500 | server_error |
| ABI 层请求解码失败 | 400 | invalid_request_error |

业务拒绝与 ABI 层不可用/解码失败（TD-CPA-044）均返回 RPC `ok=true` 且 `result.Terminate=true`——宿主 v8.0.22 对 RPC error 信封放行（abi-contract §8 事实 5），fail-closed 必须落在信封结果内；生命周期方法（register/reconfigure）故障仍可能为 RPC `ok=false`。详见 [abi-contract.md](abi-contract.md)。

## 4. 后续验收要求

映射失败后不得返回部分脱敏请求；空 ID、重试/重复 ID、格式为空和新正文不应静默绕过检测。max_findings 超限在所有正文路径（before/after-auth、复用 ID、空 ID 无状态）均按本表执行，redact 层错误经 `ErrFindings` 原子透传，不产生部分脱敏正文（TD-CPA-043，2026-10-10 闭合；此前"仍存在旁路"的表述经核实现路径后删除）。目标行为变更按 [隐私闭环执行计划](../exec-plans/active/privacy-contract-hardening.md) 实施，并在用户安排集中验证时覆盖 true/false 两组分支。现有测试通过记录不能证明上述未测语义已经安全。
