# Requirements Document

## Introduction

本文档由 `design.md` 派生，约束隐私脱敏网关插件（`privacygateway`）在 CPA 管理界面中提供的"请求脱敏追踪日志"：敏感值掩码预览、路径白名单、观测接口对脱敏输出无影响、永不记录的内容、有界内存缓冲、按阶段记录的事件、流式统计（依赖 `stream-restore-bare-chunks`）、宿主鉴权的数据路由与数据隔离、内嵌日志页面、配置项、`TraceID` 关联、独立排障面板保留、冒烟测试、文档同步与真实宿主验收。

已确认决策 RT-1~RT-4 与设计决策 TD-1~TD-10 见 `design.md` 概述。非目标：持久化到磁盘、写宿主日志、导出日志、跨进程聚合、修改脱敏算法、修改流式还原算法、修改宿主或管理中心前端、展示请求/响应正文片段。

## Glossary

- **Host**：CLIProxyAPI（CPA）v8.0.22 宿主进程。
- **Plugin**：加载到 Host 中的插件运行时实例（入口层 main 包）。
- **Interceptors**：Plugin 的四个拦截器：请求拦截（before_auth / after_auth）、响应拦截、流块拦截，以及请求完成回调 `HandleRequestComplete`。
- **Raw_Value**：检测器在请求中检出的敏感原值（凭据、高熵串、邮箱、手机号、身份证号、银行卡号）。
- **Kind**：检出类别，取值 `credential`、`entropy`、`email`、`phone`、`id_card`、`bank_card`。
- **Placeholder**：形如 `[[PG_<16 位十六进制 nonce>_<序号>]]` 的占位符，正则 `\[\[PG_[0-9a-f]{16}_[0-9]{1,9}\]\]`。
- **Mask_Previewer**：`redact` 包纯函数 `MaskPreview(kind, raw)`，生成掩码预览。
- **Preview**：Mask_Previewer 的输出字符串。
- **Visible_Rune_Cap**：design.md 组件 1.1 表中每个 Kind 的"可见原值 rune 上限"：email 为 1 + min(域名 rune 数, 31)；phone 为 7；id_card 为 8；bank_card 为 8；私钥为 0；其他 credential 为前缀长度 + 5 且 body 部分不超过 body 长度的 1/3；entropy 为 4；未知 Kind 为 0。
- **Path_Renderer**：`redact` 包纯函数 `RenderPath(path)`，把 JSON 遍历路径渲染为白名单形式。
- **Path_Whitelist**：`textKeys ∪ structuralKeys ∪ toolArgKeys(所有格式) ∪ {messages, contents, parts, tools, tool_calls, function, functionCall, input, system}`。
- **Finding**：一次检出的可观测描述，字段为 Kind、Placeholder、Preview、Path、Reused。
- **Finding_Sink**：`WalkOptions.OnFinding` 回调，类型 `FindingSink`。
- **Redactor**：`redact` 包中的 `RedactJSON` 与 `Mapping.RedactObserved`。
- **Restorer**：`redact` 包中的 `Mapping.RestoreCounted` 与 `RestoreJSONStats`。
- **Trace_Buffer**：入口层有界环形缓冲 `trace.go` 中的 `traceBuffer` 全局实例。
- **Trace_Record**：Trace_Buffer 中以 RequestID 为键的单请求记录 `traceRecord`。
- **Trace_Event**：Trace_Record 中的一条阶段事件 `traceEvent`。
- **Trace_Recorder**：入口层阶段记录辅助 `trace_hooks.go`，由 Interceptors 与 `abi.go` 调用。
- **Management_Handler**：处理 `management.register` 与 `management.handle` 的入口层组件（`management.go`、`management_trace.go`）。
- **Trace_Data_Route**：管理路由 `GET` / `DELETE /v0/management/privacygateway/traces`，经 Host 管理鉴权。
- **Trace_Page_Route**：资源路由 `{Path:"trace", Menu:"隐私脱敏日志"}`，Host URL 为 `/v0/resource/plugins/privacygateway/trace`，不经鉴权。
- **Trace_Page**：内嵌单文件页面 `packaging/trace.html`（Go 变量 `traceHTML`）。
- **Panel**：独立排障页面 `packaging/panel.html`，包内路径 `privacygateway/panel.html`。
- **Config_Parser**：`config.go` 中的 `parseConfig`。
- **Stream_Fix_Spec**：bugfix 规格 `.kiro/specs/stream-restore-bare-chunks/`，拥有 `redact/stream.go` 与 `plugin.go` 的 `InterceptStreamChunk`。
- **Stream_Counter_API**：Stream_Fix_Spec 修复后提供的流式计数接口，提供块数、还原数、未知数、跨事件暂存字节（当前值与峰值）、结束时冲刷（是否已冲刷、冲刷字节数）。
- **Packager**：打包程序 `scripts/package/main.go`。
- **Readme**：包内 `privacygateway/README.md`，由 `packaging/README.md` 生成。
- **Smoke_Tester**：cgo 宿主模拟器 `scripts/smoke`，由 `make smoke` 调用。
- **Plan_Doc**：执行计划 `docs/exec-plans/active/request-trace-log.md`。

## Requirements

### Requirement 1: 按类别生成掩码预览

**User Story:** 作为 CPA 运维者，我想在日志中看到每个被脱敏值的掩码预览，以便识别是哪一项敏感信息被替换，同时看不到完整原值。

#### Acceptance Criteria

1. WHEN Mask_Previewer 收到 Kind 为 `email` 且最后一个 `@` 之前的本地部分不少于 2 个 rune 的值，THE Mask_Previewer SHALL 输出本地部分首 rune、`***`、`@` 与域名的拼接（例如 `user@example.com` → `u***@example.com`）。
2. WHEN Mask_Previewer 收到 Kind 为 `email` 且本地部分恰为 1 个 rune 的值，THE Mask_Previewer SHALL 输出 `*@` 与域名的拼接（例如 `a@b.cc` → `*@b.cc`）。
3. WHEN `email` 值的域名超过 32 个 rune，THE Mask_Previewer SHALL 将域名输出为前 31 个 rune 加 `…`。
4. WHEN Mask_Previewer 收到 Kind 为 `phone` 且含有不少于 11 位数字的值，THE Mask_Previewer SHALL 取最后 11 位数字 d，输出 `d[0:3]`、`****`、`d[7:11]` 的拼接，且输出中不含 `+86` 或 `0086` 前缀（例如 `+86 13812345678` → `138****5678`）。
5. WHEN Mask_Previewer 收到 Kind 为 `id_card` 且去除空白后长度为 15 或 18 的值，THE Mask_Previewer SHALL 输出前 4 个字符、(长度 − 8) 个 `*`、后 4 个字符的拼接（例如 `110101199003071239` → `1101**********1239`）。
6. WHEN Mask_Previewer 收到 Kind 为 `bank_card` 且含有不少于 12 位数字的值，THE Mask_Previewer SHALL 输出前 4 位数字、`********`、后 4 位数字的拼接（例如 `6217 0000 1234 7893` → `6217********7893`）。
7. WHEN Mask_Previewer 收到 Kind 为 `credential` 且包含 `PRIVATE KEY` 的值，THE Mask_Previewer SHALL 输出 `[private_key]`。
8. WHEN Mask_Previewer 收到 Kind 为 `credential`、不含 `PRIVATE KEY`、且去除已知公开前缀（`sk-`、`ghp_`、`gho_`、`ghu_`、`ghs_`、`ghr_`、`github_pat_`、`AKIA`、`ASIA`、`AIza`、`xox?-`、`eyJ`）后的 body 不少于 16 个 rune 的值，THE Mask_Previewer SHALL 输出前缀、body 首 1 个 rune、`****`、body 末 4 个 rune 的拼接（例如 `sk-test1234567890ABCD` → `sk-t****ABCD`）。
9. WHEN Mask_Previewer 收到 Kind 为 `credential`、不含 `PRIVATE KEY` 且 body 少于 16 个 rune 的值，THE Mask_Previewer SHALL 在存在已知前缀时输出前缀加 `****`，在无已知前缀时输出 `[credential]`。
10. WHEN Mask_Previewer 收到 Kind 为 `entropy` 且不少于 16 个 rune 的值，THE Mask_Previewer SHALL 输出首 2 个 rune、`****`、末 2 个 rune 的拼接。

### Requirement 2: 掩码通用安全约束

**User Story:** 作为安全负责人，我想让掩码预览在任何输入下都不泄露完整原值，以便日志即使被管理员看到也不构成原值泄露。

#### Acceptance Criteria

1. THE Mask_Previewer SHALL 以 rune 为单位处理输入，并把无效 UTF-8 字节、控制字符与换行在计算与输出中都替换为 `?`。
2. THE Mask_Previewer SHALL 输出不超过 48 个 rune 的合法 UTF-8 字符串，超出部分截断并以 `…` 结尾。
3. THE Mask_Previewer SHALL 使输出中来自 Raw_Value 的可见 rune 数不超过该 Kind 的 Visible_Rune_Cap。
4. IF 输入不满足该 Kind 的掩码规则（长度不足或格式不符），THEN THE Mask_Previewer SHALL 输出仅含类别标签的 `[<kind>]`（例如 `[phone]`、`[id_card]`、`[bank_card]`、`[entropy]`）。
5. IF Kind 不属于 Kind 枚举，THEN THE Mask_Previewer SHALL 在 Kind 匹配 `[a-z_]{1,16}` 时输出 `[<kind>]`，在不匹配时输出 `[unknown]`。
6. THE Mask_Previewer SHALL 产生既不等于 Raw_Value、也不以 Raw_Value 为子串的输出。
7. THE Mask_Previewer SHALL 在除 `id_card` 以外的所有 Kind 中使用固定个数的 `*` 作为掩码段，使输出长度不反映原值长度。

### Requirement 3: JSON 路径白名单渲染

**User Story:** 作为安全负责人，我想让日志中的检出位置只显示协议层面的已知键，以便工具参数中由用户定义的键名不进入日志。

#### Acceptance Criteria

1. THE Path_Renderer SHALL 将数组下标渲染为 `[n]`。
2. THE Path_Renderer SHALL 原样输出属于 Path_Whitelist 的键，并将其他所有键输出为 `*`。
3. WHEN 路径超过 8 段，THE Path_Renderer SHALL 只保留最后 8 段，并以 `…` 作为输出开头。
4. THE Path_Renderer SHALL 输出不超过 96 字节、且只由 Path_Whitelist 键、`*`、`[n]`、`.` 与前导 `…` 组成的字符串。

### Requirement 4: 检出观测接口不改变脱敏与还原输出

**User Story:** 作为插件维护者，我想让追踪功能只旁路观测脱敏结果，以便开启日志不会改变发往上游与返回客户端的任何字节。

#### Acceptance Criteria

1. WHEN Redactor 在 Finding_Sink 非 nil 时写入一个 Placeholder，THE Redactor SHALL 以包含 Kind、Placeholder、Preview（Mask_Previewer 输出）、Path（Path_Renderer 输出）与 Reused 的 Finding 调用 Finding_Sink 恰好一次。
2. THE Redactor SHALL 构造只含 Kind、Placeholder、Preview、Path、Reused 五个字段的 Finding，Raw_Value 只存在于 Redactor 内部局部变量中。
3. WHEN 写入的 Placeholder 在本次调用前已存在于映射中，THE Redactor SHALL 将该 Finding 的 Reused 置为真；否则置为假。
4. WHERE Finding_Sink 为 nil，THE Redactor SHALL 返回与现有 `RedactJSON` 逐字节相同的输出、计数与错误，且不调用 Mask_Previewer 与 Path_Renderer。
5. IF Finding_Sink 在调用中 panic，THEN THE Redactor SHALL 在 `redact` 包内恢复该 panic，并返回与 Finding_Sink 为 nil 时相同的字节、计数与错误。
6. WHEN Restorer 执行 `RestoreCounted`，THE Restorer SHALL 返回与 `Restore` 相同的字符串，并令 Restored 等于已知 Placeholder 出现次数、Unknown 等于符合 Placeholder 格式但不在映射中的出现次数、UnknownSamples 至多 5 个。
7. THE Restorer SHALL 使 `RestoreJSON` 的输出与引入 `RestoreJSONStats` 之前逐字节相同。
8. THE `redact` 包 SHALL 只依赖 Go 标准库与本包类型，`internal/archtest` 结构测试保持通过。

### Requirement 5: 永不记录的内容与输出通道

**User Story:** 作为安全负责人，我想明确日志中绝不出现的内容，以便追踪数据本身不成为新的泄露面。

#### Acceptance Criteria

1. THE Trace_Recorder SHALL 只从 `redact` 包接收 Finding 与计数，Raw_Value 不进入 main 包的任何结构。
2. THE Trace_Recorder SHALL 在 Trace_Event 中省略请求与响应正文、请求头（含 `Authorization`）、`Metadata`、命中的拒绝关键词、allowlist、解析错误原文、panic 文本、`RequestCompletion.Error` 原文、流块内容与跨事件暂存文本。
3. THE Trace_Data_Route 的 GET 响应正文 SHALL 不包含任何 Raw_Value 子串，也不包含其 JSON 转义形式。
4. THE Trace_Recorder SHALL 只用代码内常量填写 Trace_Event 的 `stage`、`level`、`hook`、`reason`、`action` 与 `outcome` 字段。
5. WHEN Trace_Recorder 记录 `model`、`source_format` 或 `to_format`，THE Trace_Recorder SHALL 将值截断为至多 64 字节并把控制字符替换为 `?`。
6. THE Trace_Recorder SHALL 只保存匹配 Placeholder 正则的 `placeholder` 与 `unknown_samples` 值，并只保存来自 Mask_Previewer 的 `preview` 值。
7. THE Plugin SHALL 只在 Trace_Buffer 内存中保存追踪数据，追踪数据不写入磁盘与 Host 日志；现有 `logHost` 错误日志保持不变。

### Requirement 6: 有界环形缓冲

**User Story:** 作为 CPA 运维者，我想让追踪缓冲有固定上限，以便长时间运行时插件内存不会无界增长。

#### Acceptance Criteria

1. THE Trace_Buffer SHALL 在任意时刻保存不超过 `trace_max_requests` 个 Trace_Record，且每个 Trace_Record 的 Trace_Event 数不超过 `trace_max_events`。
2. WHEN 新 RequestID 的事件到达且 Trace_Record 数已等于 `trace_max_requests`，THE Trace_Buffer SHALL 淘汰首次出现最早的 Trace_Record，并将 `evicted` 加 1。
3. WHEN 已被淘汰的 RequestID 再次产生事件，THE Trace_Buffer SHALL 为其新建 Trace_Record 并将 `partial` 置为真。
4. WHILE 某 Trace_Record 的事件数已达 `trace_max_events − 1`，THE Trace_Buffer SHALL 丢弃除 `request.complete` 以外的新事件，并将该记录的 `dropped_events` 加 1。
5. WHEN `request.complete` 事件到达未被淘汰的 Trace_Record，THE Trace_Buffer SHALL 将其追加为该记录的最后一个事件。
6. WHILE 某 Trace_Record 的 Finding 明细已达 64 条，THE Trace_Buffer SHALL 只累加 `findings_truncated` 计数而不再保存新明细。
7. THE Trace_Buffer SHALL 为每个 Trace_Event 分配全局严格递增的 `seq`，并使同一 Trace_Record 内事件的 `at` 单调不减。
8. WHEN 事件的 RequestID 为空，THE Trace_Buffer SHALL 使用合成键 `anon-<seq>` 建立 Trace_Record，并将 `anonymous` 置为真。
9. IF 记录事件过程中发生 panic，THEN THE Trace_Buffer SHALL 恢复并丢弃该事件，Interceptors 的返回值保持不变，且不写 Host 日志。
10. THE Trace_Buffer SHALL 支持 record、configure、snapshot 与 clear 的并发调用，在 `-race` 检测下无数据竞争，且并发期间第 1 条约束始终成立。

### Requirement 7: 缓冲生命周期与追踪开关

**User Story:** 作为 CPA 运维者，我想通过配置开关和调整追踪缓冲，以便在需要时关闭追踪或改变容量而不重启 CPA。

#### Acceptance Criteria

1. WHEN `plugin.register` 成功，THE Plugin SHALL 按配置中的 `trace_enabled`、`trace_max_requests`、`trace_max_events` 初始化 Trace_Buffer。
2. WHEN `plugin.reconfigure` 成功，THE Plugin SHALL 保留 Trace_Buffer 中已有的 Trace_Record 并应用新的上限。
3. WHEN 重配后的 `trace_max_requests` 小于当前 Trace_Record 数，THE Trace_Buffer SHALL 按首次出现顺序淘汰最旧的记录，直至记录数等于新上限。
4. WHEN `trace_enabled` 变为 false，THE Trace_Buffer SHALL 清空全部 Trace_Record 并停止记录，且 Redactor 收到的 Finding_Sink 为 nil。
5. WHILE `trace_enabled` 为 false，THE Trace_Recorder SHALL 在每个记录入口只做一次开关读取即返回。
6. WHEN Plugin 收到 `shutdown` 调用，THE Plugin SHALL 清空 Trace_Buffer 并关闭记录。
7. THE Interceptors SHALL 在 `trace_enabled` 为 true 与为 false 时对相同输入返回相同的结果。

### Requirement 8: 按阶段记录请求事件

**User Story:** 作为 CPA 运维者，我想按请求看到从接收到还原的每一步，以便定位请求在哪一步被拦截、失败或放行。

#### Acceptance Criteria

1. WHEN before_auth 或 after_auth 请求拦截入口被调用，THE Trace_Recorder SHALL 记录 `request.received` 事件，字段为 `hook`、`attempt`（after_auth 的第 n 次凭据尝试）、`source_format`、`to_format`、`model`、`requested_model`、`stream`、`body_bytes` 与 `trace_id`。
2. WHEN 请求因并发上限（429）、缺少 RequestID（400）、格式不支持（415）或 RequestID 复用但格式不一致（400）被拒绝，THE Trace_Recorder SHALL 记录 `request.rejected` 事件，字段为 `reason` 与 `status`。
3. WHEN 请求因关键词命中（403）或正文超限且 fail_closed（413）被拦截，THE Trace_Recorder SHALL 记录 `guard.blocked` 事件，字段为 `reason`（`reject_keyword` 或 `body_too_large`）、`status`、`body_bytes` 与 `limit`，命中的关键词不进入该事件。
4. WHEN 请求因正文超限且 fail_open、格式不支持且 fail_open、空正文或格式为空而原样放行，THE Trace_Recorder SHALL 记录 `guard.passthrough` 事件，字段为 `reason` 与 `action=passthrough`。
5. WHEN `RedactJSON` 成功返回（含 0 个检出），THE Trace_Recorder SHALL 记录 `redact.done` 事件，字段为 `findings`、`unique`、`reused`、`counts`（键属于 Kind 枚举）、`details`（Finding 列表）、`mapping_size`、`elapsed_us` 与 `stateless`。
6. WHEN 脱敏因 JSON 格式错误、容量不足、检出数超限或映射分配失败而失败，THE Trace_Recorder SHALL 记录 `redact.failed` 事件，字段为 `reason`（`malformed_json`、`capacity`、`max_findings` 或 `mapping_unavailable`）、`action`（`terminate` 或 `passthrough`）与 `status`。
7. WHEN 响应拦截被调用，THE Trace_Recorder SHALL 记录 `response.restored` 事件，字段为 `status_code`、`body_bytes`、`restored`、`unknown`、`unknown_samples` 与 `skipped`（`no_mapping`、`empty_body` 或 `parse_failed`）。
8. WHEN 请求链路中发生 panic 恢复、插件不可用或载荷解码失败，THE Trace_Recorder SHALL 记录 `plugin.error` 事件，字段为 `reason`（`panic`、`unavailable` 或 `decode_failed`）、`method` 与 `action`，并只从载荷中解码 RequestID 与 TraceID。
9. WHEN `HandleRequestComplete` 被调用，THE Trace_Recorder SHALL 记录 `request.complete` 事件，字段为 `outcome`（`succeeded`、`failed`、`rejected` 或 `canceled`）、`status_code`、`duration_ms` 与 `has_error`。
10. IF 请求完成回调缺少 `StartedAt` 或 `CompletedAt`，THEN THE Trace_Recorder SHALL 以该 Trace_Record 首个事件的时间为起点计算 `duration_ms`。
11. WHEN after_auth 在已有映射时重扫请求，THE Trace_Recorder SHALL 在 `redact.done` 中按 Finding 的 Reused 字段区分新增项与复用项。

### Requirement 9: 流式统计与 stream-restore-bare-chunks 依赖

**User Story:** 作为插件维护者，我想让流式日志只消费流式修复提供的计数，以便两个规格不同时修改流式还原代码。

#### Acceptance Criteria

1. THE Plugin SHALL 从 Stream_Counter_API 获取流式计数，`redact/stream.go` 与 `InterceptStreamChunk` 的还原逻辑保持由 Stream_Fix_Spec 所有，本特性对流式还原算法零修改。
2. WHEN 一个流块处理完成，THE Trace_Recorder SHALL 将该请求的 `stream.summary` 事件原地更新为 Stream_Counter_API 当前值，字段为 `chunks`、`restored`、`unknown`、`carry_bytes` 与 `max_carry`。
3. THE Trace_Recorder SHALL 为每个 Trace_Record 保存至多一条 `stream.summary` 事件。
4. WHEN 请求完成时的结束冲刷执行完毕，THE Trace_Recorder SHALL 记录 `stream.flush` 事件，字段为 `flushed`、`flushed_bytes` 与冲刷前的 `carry_bytes`。
5. THE Plugin SHALL 在记录与不记录流统计时，对相同的流块序列产生逐字节相同的每块输出与冲刷输出。
6. WHILE Stream_Counter_API 尚未由 Stream_Fix_Spec 提供，THE Trace_Recorder SHALL 记录除 `stream.summary` 与 `stream.flush` 以外的全部阶段事件。

### Requirement 10: 追踪数据管理路由注册

**User Story:** 作为 CPA 运维者，我想让追踪数据只经宿主管理鉴权访问，以便未登录的访问者拿不到敏感元数据。

#### Acceptance Criteria

1. WHEN Host 调用 `management.register`，THE Management_Handler SHALL 返回两条管理路由：`GET /v0/management/privacygateway/traces` 与 `DELETE /v0/management/privacygateway/traces`。
2. THE Management_Handler SHALL 以仅含 `Method`、`Path`、`Description` 三个字段的形式编码每条管理路由，使 `Menu` 与 `Handler` 不出现在注册响应中。
3. WHEN Host 调用 `management.register`，THE Management_Handler SHALL 返回唯一一条资源路由 `{Path:"trace", Menu:"隐私脱敏日志"}`，替换原 `panel` 资源路由。
4. THE Trace_Data_Route SHALL 使用 `/v0/management/privacygateway/traces` 路径，位于 Host 管理鉴权前缀 `/v0/management/` 下且不在 Host 的 `/v0/management/plugins/` 参数路由树下。

### Requirement 11: 追踪数据查询与响应结构

**User Story:** 作为 CPA 运维者，我想按条件增量查询追踪数据，以便页面低开销地刷新最近的请求。

#### Acceptance Criteria

1. WHEN Trace_Data_Route 收到 GET 请求，THE Management_Handler SHALL 返回 200 与 JSON 对象，字段为 `version`、`plugin`（`version`、`enabled`、`fail_closed`、`trace_enabled`）、`limits`（`max_requests`、`max_events`、`max_finding_details`）、`buffered`、`evicted`、`next_since` 与 `requests`。
2. THE Management_Handler SHALL 在 `requests` 中按 `first_seen` 倒序返回不超过 `limit` 条 Trace_Record，每条含 `request_id`、`trace_id`、`anonymous`、`partial`、`first_seen`、`updated_seq`、`outcome`、`status`、`terminated`、`has_error`、`finding_total`、`counts`、`dropped_events`、`findings_truncated` 与 `events`。
3. THE Management_Handler SHALL 将 `limit` 默认取 50，有效范围为 1 至 `trace_max_requests`。
4. WHEN GET 请求携带 `since`，THE Management_Handler SHALL 只返回 `updated_seq` 大于 `since` 的 Trace_Record。
5. WHEN GET 请求携带 `filter=findings`，THE Management_Handler SHALL 只返回 `finding_total` 大于 0 的 Trace_Record。
6. WHEN GET 请求携带 `filter=errors`，THE Management_Handler SHALL 只返回 `terminated` 为真、`has_error` 为真或 `outcome` 属于 `failed`、`rejected`、`canceled` 的 Trace_Record。
7. IF `limit`、`since` 或 `filter` 的值无法解析或超出范围，THEN THE Management_Handler SHALL 对该参数采用默认值并返回 200 与合法 JSON。
8. THE Management_Handler SHALL 使 `next_since` 不小于本次返回的任意 `updated_seq`，且在无新事件时以 `since = next_since` 再次查询返回空的 `requests`。
9. THE Management_Handler SHALL 为 Trace_Data_Route 响应设置 `Content-Type: application/json; charset=utf-8`、`Cache-Control: no-store` 与 `X-Content-Type-Options: nosniff`。
10. WHEN Trace_Data_Route 收到 DELETE 请求，THE Management_Handler SHALL 清空 Trace_Buffer 并返回 200 与 `{"cleared":n}`，n 为清空前的记录数。
11. IF Trace_Data_Route 收到 GET 与 DELETE 以外的方法，THEN THE Management_Handler SHALL 返回 405 与 `Allow: GET, DELETE`。
12. WHILE `trace_enabled` 为 false，THE Management_Handler SHALL 在 GET 响应中返回 `trace_enabled: false` 与空的 `requests`。

### Requirement 12: 管理分发与数据隔离

**User Story:** 作为安全负责人，我想确保不鉴权的资源通道永远拿不到追踪数据，以便页面路由暴露在外也不泄露元数据。

#### Acceptance Criteria

1. THE Management_Handler SHALL 只在 `ManagementRequest` 的完整路径等于 `/v0/management/privacygateway/traces` 时返回追踪数据。
2. WHEN 请求路径归一化为 `trace`（含 `/v0/resource/plugins/privacygateway/trace` 形式）且方法为 GET，THE Management_Handler SHALL 返回 200 与 Trace_Page 内容。
3. IF 请求路径归一化为 `trace` 且方法不是 GET，THEN THE Management_Handler SHALL 返回 405 与 `Allow: GET`。
4. IF 请求路径不匹配 Trace_Data_Route 与 Trace_Page_Route（含旧 `panel` 路径），THEN THE Management_Handler SHALL 返回 404。
5. IF `management.handle` 载荷解码失败，THEN THE Management_Handler SHALL 返回 400 与固定短文本。
6. IF 管理处理内部发生异常，THEN THE Management_Handler SHALL 恢复并返回 500。
7. THE Management_Handler SHALL 对任意方法与任意路径的 `ManagementRequest` 返回 HTTP 响应而不 panic。
8. WHEN 请求路径以 `/v0/resource/` 开头或为相对路径，THE Management_Handler SHALL 返回 Trace_Page 内容或固定短错误文本之一。

### Requirement 13: 日志页面资源与安全响应头

**User Story:** 作为 CPA 运维者，我想在管理中心通过"隐私脱敏日志"菜单打开日志页，以便无需额外部署即可查看追踪。

#### Acceptance Criteria

1. THE Plugin SHALL 以 `go:embed` 嵌入 `packaging/trace.html`，使 `traceHTML` 与该文件逐字节相同。
2. THE Trace_Page SHALL 只包含内联脚本与内联样式，不引用 `http://` 或 `https://` 外部资源。
3. THE Management_Handler SHALL 为 Trace_Page 响应设置 `Content-Type: text/html; charset=utf-8`、`X-Content-Type-Options: nosniff`、`Cache-Control: no-store` 与 `Referrer-Policy: no-referrer`。
4. THE Management_Handler SHALL 为 Trace_Page 响应设置 `Content-Security-Policy: default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'none'`。
5. THE Trace_Page SHALL 只向与页面同源的 Trace_Data_Route 发送数据请求。

### Requirement 14: 页面连接与轮询

**User Story:** 作为 CPA 运维者，我想让日志页在任何网络状况下都给出明确反馈，以便页面不会再出现无提示的"一直在请求中"。

#### Acceptance Criteria

1. WHEN Trace_Page 发起任一 `fetch`，THE Trace_Page SHALL 使用 `AbortController` 在 8 秒后中止未完成的请求。
2. IF 请求超时、网络错误或返回非 2xx 状态，THEN THE Trace_Page SHALL 在状态区显示具体原因（例如"请求超时（8s）"或"HTTP 502：插件管理处理失败"）。
3. IF Trace_Data_Route 返回 404，THEN THE Trace_Page SHALL 提示"插件版本过旧或未注册管理路由，请确认已替换 .so 并重启 CPA"。
4. IF Trace_Data_Route 返回 502，THEN THE Trace_Page SHALL 提示"插件管理处理失败（宿主返回 502），请查看 CPA 日志"。
5. WHILE 一次轮询请求在途，THE Trace_Page SHALL 推迟下一次轮询，直至该请求结束。
6. IF Trace_Data_Route 返回 401 或 403，THEN THE Trace_Page SHALL 停止自动刷新、清除内存与 `sessionStorage` 中的管理密钥，并提示"密钥错误，已停止自动刷新以免触发宿主封禁"。
7. WHILE `document.hidden` 为真，THE Trace_Page SHALL 暂停自动刷新。
8. WHEN 页面由隐藏变为可见，THE Trace_Page SHALL 立即刷新一次。
9. WHERE 自动刷新开启，THE Trace_Page SHALL 每 3 秒发起一次增量查询，首次使用 `since=0`，之后使用上次响应的 `next_since`。
10. WHEN 增量响应到达，THE Trace_Page SHALL 按 `request_id` 合并更新本地列表，并保留不超过 `limits.max_requests` 条记录。
11. THE Trace_Page SHALL 以 `Authorization: Bearer <key>` 请求头携带管理密钥。

### Requirement 15: 页面渲染与交互

**User Story:** 作为 CPA 运维者，我想以时间线方式阅读每个请求的处理步骤，以便快速判断脱敏、还原是否正常。

#### Acceptance Criteria

1. THE Trace_Page SHALL 只通过 `textContent` 与 `createElement` 渲染来自 Trace_Data_Route 的数据，且页面源码不含 `innerHTML`。
2. THE Trace_Page SHALL 在汇总区显示插件版本、`enabled`、`fail_closed`、缓冲占用（`buffered`/`max_requests`）与 `evicted`。
3. THE Trace_Page SHALL 为每个 Trace_Record 显示首次时间、RequestID、`trace_id`、格式、是否流式、`outcome`、`status`、耗时与各 Kind 检出数，并按相对首事件的毫秒偏移列出全部 Trace_Event。
4. WHEN Trace_Record 含 after_auth 事件，THE Trace_Page SHALL 按 `attempt` 分组显示"第 n 次凭据尝试"，并汇总新增项数与复用项数。
5. WHILE 未输入管理密钥，THE Trace_Page SHALL 显示"请输入 CPA 管理密钥"。
6. WHILE 已连接且 `requests` 为空，THE Trace_Page SHALL 显示"暂无请求。请通过 CPA 发送一次模型请求，再回到此页"。
7. WHILE GET 响应中 `trace_enabled` 为 false，THE Trace_Page SHALL 显示"追踪已关闭：在配置中设置 `trace_enabled: true`"。
8. WHILE GET 响应中插件 `enabled` 为 false，THE Trace_Page SHALL 显示"插件总开关关闭，请求不会被脱敏，也不会被记录"。
9. WHEN 用户点击"清空日志"，THE Trace_Page SHALL 先请求二次确认，确认后发送 DELETE，成功后清空本地列表并将 `since` 重置为 0。
10. THE Trace_Page SHALL 用颜色区分终止或失败（红）、fail_open 放行（橙）、正常（绿）与信息（灰），并同时显示图标与文字。
11. THE Trace_Page SHALL 为每个输入控件提供 `<label>`，以 `<details>`/`<summary>` 呈现请求条目，为状态区设置 `role="status"` 与 `aria-live="polite"`，为错误区设置 `role="alert"`，并保留可见的焦点样式。

### Requirement 16: 管理密钥存储

**User Story:** 作为安全负责人，我想限制管理密钥在浏览器中的存储位置，以便关闭标签页后密钥不残留。

#### Acceptance Criteria

1. THE Trace_Page SHALL 默认只在 JavaScript 内存变量中保存管理密钥。
2. WHERE 用户勾选"本标签页记住"，THE Trace_Page SHALL 将管理密钥写入 `sessionStorage["privacygateway.managementKey"]`。
3. WHEN 用户取消勾选"本标签页记住"，THE Trace_Page SHALL 立即删除 `sessionStorage["privacygateway.managementKey"]`。
4. THE Trace_Page SHALL 将管理密钥的存储限定在内存变量与 `sessionStorage` 两处，页面源码不含 `localStorage`。

### Requirement 17: 追踪配置项

**User Story:** 作为 CPA 运维者，我想通过配置文件控制追踪的开关与容量，以便在内存与可见性之间取舍。

#### Acceptance Criteria

1. THE Config_Parser SHALL 支持布尔配置 `trace_enabled`，缺省时取 true。
2. THE Config_Parser SHALL 支持整数配置 `trace_max_requests`，缺省时取 200，有效范围为 1 至 2000。
3. THE Config_Parser SHALL 支持整数配置 `trace_max_events`，缺省时取 64，有效范围为 8 至 256。
4. IF `trace_max_requests` 或 `trace_max_events` 超出有效范围，THEN THE Config_Parser SHALL 返回错误，使初次注册失败、重配保留旧配置。
5. THE Plugin SHALL 在插件元数据的 `ConfigFields` 中声明 `trace_enabled`、`trace_max_requests` 与 `trace_max_events`，并使 `config.example.yaml`、包内 `config.snippet.yaml` 与 `ConfigFields` 的键集合一致。
6. THE Readme SHALL 说明三个 `trace_*` 配置项的默认值、范围与内存代价（默认最坏约 6 MiB，上限值约 160 MiB）。

### Requirement 18: TraceID 关联显示

**User Story:** 作为 CPA 运维者，我想在日志中看到宿主的 TraceID，以便与 CPA gin 访问日志中的方括号 ID 对照。

#### Acceptance Criteria

1. WHEN `request.received` 或 `request.complete` 的宿主载荷含 TraceID，THE Trace_Recorder SHALL 将其写入该 Trace_Record 的 `trace_id`。
2. IF TraceID 不匹配 `^[0-9A-Za-z-]{1,64}$`，THEN THE Trace_Recorder SHALL 丢弃该 TraceID。
3. WHEN 后续事件（响应拦截、流块拦截）的载荷不含 TraceID，THE Trace_Buffer SHALL 保留该 Trace_Record 已有的 `trace_id`。
4. THE Trace_Page SHALL 在每个 Trace_Record 的摘要行以 `trace [<trace_id>]` 形式显示 TraceID。

### Requirement 19: 独立排障面板保留

**User Story:** 作为 CPA 运维者，我想在资源路由不可用时仍有独立排障页面，以便在宿主 Home 模式下也能检查脱敏效果。

#### Acceptance Criteria

1. THE Packager SHALL 继续将 Panel 写入 zip 包的 `privacygateway/panel.html`。
2. THE Readme SHALL 将菜单名称写为"隐私脱敏日志"，并说明 Panel 是以 `file://` 打开的独立排障工具，适用于宿主 Home 模式等资源路由不可用的场景。

### Requirement 20: 交付物冒烟测试

**User Story:** 作为插件维护者，我想在发布前用宿主模拟器验证新路由，以便确认解压后的 `.so` 能注册并返回追踪数据。

#### Acceptance Criteria

1. WHEN Smoke_Tester 调用 `management.register`，THE Smoke_Tester SHALL 断言返回 2 条管理路由与资源路由 `trace`。
2. WHEN Smoke_Tester 以 GET 调用 Trace_Page_Route，THE Smoke_Tester SHALL 断言返回 HTML 内容。
3. WHEN Smoke_Tester 完成一次含邮箱的脱敏请求后以 GET 调用 Trace_Data_Route，THE Smoke_Tester SHALL 断言返回的 JSON 含 `redact.done` 事件且不含该邮箱原值。

### Requirement 21: 文档同步

**User Story:** 作为插件维护者，我想让仓库文档与新行为一致，以便后续协作者不被过期说明误导。

#### Acceptance Criteria

1. WHEN 本特性实现完成，THE 仓库文档 SHALL 在 `docs/design-docs/abi-contract.md` 中记录管理路由线格式（`Method`/`Path`/`Description`，不设 Menu）、按完整路径分发规则与 `traces` 响应结构。
2. WHEN 本特性实现完成，THE 仓库文档 SHALL 在 `ARCHITECTURE.md` 与 `AGENTS.md` 的目录地图中登记 `trace.go`、`trace_hooks.go`、`management_trace.go`、`metadata.go` 与 `redact/finding.go`，并补充"观测旁路"数据流。
3. WHEN 本特性实现完成，THE 仓库文档 SHALL 在 `docs/design-docs/core-beliefs.md` 红线一节补充"追踪日志只含掩码预览"。
4. THE Plan_Doc SHALL 记录 RT-1~RT-4、TD-1~TD-10、规格进度，以及对 Stream_Fix_Spec 的依赖。
5. WHEN 本特性实现完成，THE 仓库文档 SHALL 在 `docs/QUALITY_SCORE.md` 中新增"可观测性"一行，并在 `.kiro/specs/plugin-package/design.md` 组件 6 注明内嵌页面已被本规格替换。
6. WHEN 文档变更完成，THE 仓库文档 SHALL 通过 `go run ./scripts/check-docs` 校验。

### Requirement 22: 真实宿主验收（用户执行）

**User Story:** 作为 CPA 运维者，我想在自己的 CPA 上确认日志页可用，以便确认本机模拟与真实环境一致。

#### Acceptance Criteria

1. WHEN 用户替换 `.so` 并重启 Host，THE Host 管理中心 SHALL 显示"隐私脱敏日志"菜单。
2. WHEN 用户在 Trace_Page 输入管理密钥并发送一次对话请求，THE Trace_Page SHALL 显示该请求从 `request.received` 到 `request.complete` 的完整时间线。
3. WHEN 用户完成验收，THE Plan_Doc SHALL 记录 Trace_Page 显示的 `trace_id` 与 gin 访问日志方括号 ID 是否一致，以及 after_auth `attempt` 计数与宿主重试次数是否一致。
4. WHEN Stream_Fix_Spec 已合入且用户发送一次含敏感值的流式请求，THE Trace_Page SHALL 显示 `chunks` 大于 0 的 `stream.summary` 事件，Plan_Doc 记录该次请求的客户端是否收到完整流内容。
