# 脱敏算法契约

> 更新：2026-10-24；§3.2 豁免与敏感上下文、§3.1 Responses 工具输出补扫、§6 暂存组合与统计按 `redact/detect*.go`、`redact/walk.go`、`redact/stream*.go`、`plugin.go` 源码与测试对照更新。现状与目标分别描述，新增风险见技术债台账（TD-CPA-052）。

## 1. 定位与参考边界

本插件在 CPA 宿主进程内，把模型请求正文中的凭据和 PII 替换为请求级占位符，再在响应中还原。算法依赖本机内存，不调用远程识别服务，不使用跨请求 HMAC 令牌或持久化映射。

CosyRedactGateway 是用户指定的算法参考项目，地址为 https://github.com/bactdt/CosyRedactGateway 。本轮未获得该项目的可核验源码版本，以下行为来自本仓库源码，不能据此宣称与参考项目等价、已完成算法迁移或协议兼容。取得参考版本后，只对照检测、映射与流式还原，不引入其部署架构。

## 2. 已实现范围

| 输入 | 当前行为 | 限制 |
|---|---|---|
| 模型 JSON 正文 | 遍历选定字符串并脱敏 | `SourceFormat` 必须在支持表内 |
| 工具参数 | 开关启用后按协议字段进入参数子树 | 依据键名，未核验完整工具调用路径 |
| JSON 响应 | 扫描全部字符串值并还原 | 不还原对象键或数字值 |
| 流式 Body 块 | 按载荷形态在协议文本字段上还原，跨事件占位符按文本流暂扣（§6） | 一进一出、不跨块缓冲行；已知限制见 TD-CPA-046–050 |
| Header、URL Query、表单 | 不检测、不改写 | 不应宣称受保护 |
| 图片、二进制、多模态结构字段 | 不检测、不改写 | 不下载或查看图片 |

支持的格式名称为 `openai`、`openai-response`、`claude`、`gemini`、`codex`、`antigravity`、`interactions`。格式在准入阶段用于判断支持范围，在遍历阶段主要用于选择工具参数键；没有为各协议建立完整路径 schema。

## 3. 请求处理顺序

```text
RequestID 检查 → 状态准入 → 关键词拒绝 → 正文体积检查
  → SourceFormat 检查 → 随机映射初始化 → JSON token 遍历
  → 选定字符串检测 → span 筛选 → 构造占位符
  → 重新编码被改写的字符串字面量 → 保存请求映射
```

空 RequestID 返回空修改对象；已有 RequestID 刷新活动时间并原样返回当前正文。两者不进行重复检测，是当前旁路边界，不能作为 fail-closed 保证（TD-CPA-034）。Auth 后若已有映射则旁路；否则再次进入准入流程，但已有状态会提前返回，不能假设后置钩子会补扫新正文。

### 3.1 JSON 字段选择

普通字符串只在最后一级键属于以下集合时检测：`text`、`content`、`prompt`、`instructions`、`system`、`description`、`query`、`input_text`、`output_text`、`title`、`input`。

`role`、`model`、`id`、`name`、`url`、`data`、`signature`、`thinking`、`reasoning`、`encrypted_content`、`image_url` 等结构字段保持原样。完整集合以 `walk.go` 为准。该集合按字符串值的末级键判断，不等于跳过整个父对象：未知父节点内的 `text` 仍可能被检测。

数组遍历把数字索引放到路径末级，所以 `input: ["..."]`、`content: ["..."]` 中的裸字符串当前不会命中文本键；对象数组内的 `text` 可以命中（TD-CPA-036）。

`redact_tool_arguments=false` 是默认值。启用后：Claude 的 `arguments`、`args`、`input`，Gemini 的 `args`，其他格式的 `arguments` 会标记其子树内所有字符串为工具参数。数字、布尔值和对象键不处理；包含内层 JSON 的参数字符串作为整体文本检测，不单独解析内层 schema。

JSON 使用 `json.Decoder.Token` 遍历，默认深度上限 64；多个顶级 JSON 值、无效 JSON、深度超限返回解析错误。只替换选定字面量，保留未修改区段的原始空白、数字写法和顺序。返回的 count 是改写字符串节点数，不是敏感实体总数。

Responses/Codex 补扫：当格式为 `openai-response` 或 `codex` 时，仅对 `input[i]` 直接元素（路径为 `input.<数字>`）中 `type` 为 `function_call_output`/`custom_tool_call_output` 的 `output` 字符串强制检测，`type` 可位于 `output` 之前或之后。`metadata.output`、`type:"message"`/`function_call` 等非工具输出字段不进入补扫，避免把普通响应元数据误脱敏（TD-CPA-052）。

### 3.2 检测器

检测顺序为固定凭据、`custom_rules`、邮箱、手机号、身份证、银行卡、敏感上下文、高熵，再追加 allowlist 允许区间。实体边界使用 UTF-8 字节偏移的半开区间 `[start,end)`。

| 类型 | 算法 | 已知限制 |
|---|---|---|
| 凭据 | 固定正则：`sk-`、GitHub token、AWS key ID、Google API key、Slack token、JWT、私钥块 | 不按 `password` 等字段自动检测；不能保证任意密码被识别 |
| 邮箱 | ASCII 正则 | 不覆盖全部国际化邮箱格式 |
| 手机号 | 大陆 11 位号码，可带 `+86`/`0086`；前后紧邻字母或数字的候选不检出（2026-10-10 边界修复，排除时间戳/订单号误报） | 不覆盖任意国际号码 |
| 身份证 | 17 位数字 + 数字或 `X/x` 校验位 + 加权校验（TD-CPA-028，2026-10-10 修复） | 不验证完整行政区和日期 |
| 银行卡 | 13–19 位候选（前导空格/连字符不阻断，TD-CPA-029，2026-10-10 修复）、最多 5 个分隔符、去分隔符后 Luhn | 纯 Luhn 误报概率极低但存在 |
| 敏感上下文 | 单行 `KEY=value`、`export KEY=value`、`"key": "value"`、`key: value`/`key = value`；键名按 `_`/`-`/`.`/驼峰切词后含 `SECRET`/`TOKEN`/`PASSWORD`/`PASSWD`/`PWD`/`PRIVATE`/`CREDENTIAL`/`APIKEY`，或相邻词为 `API KEY`/`ACCESS KEY`；值需 ≥8 rune、非纯数字、非 `true/false/yes/no`、非完整占位符；整个键名为 `PWD`/`OLDPWD` 时不视为敏感 | 仅单行；值含空格必须加引号；以 `PWD` 为完整键名的真实密码可能漏检（TD-CPA-052） |
| 高熵 | token 正则 + 至少一个数字 + 长度/香农熵阈值；先过滤豁免区间 | 默认长度 24、熵 3.5；存在误报和漏报 |

高熵豁免只过滤候选，不改变其他检测器：UUID 整串；token 主体后紧跟 `.tar.gz`、`.zip`、`.log`、`.sql` 等常见扩展名（大小写不敏感，扩展名后为非 token 字节或文本结尾）；内置安全键名 `process_id`/`pid`/`trace_id`/`request_id` 的值；配置 `safe_id_keys` 命中的键值；配置 `safe_id_prefixes` 命中的 token。形态本身不豁免，例如 UUID 只有完整匹配才放过，片段仍按高熵处理。纯文本 id（如 `proc_...`）需显式配置前缀，不按工具来源自动限定（TD-CPA-052）。

香农熵为 `H = -Σ p(c) log₂ p(c)`，按候选字符计数。`max_findings`（默认 2000）限制单个字段的最大检出数：fail_closed 下整体拒绝（500 max_findings_exceeded），fail_open 下原样放行，均不产生部分脱敏正文；allowlist 条目不计入上限（TD-CPA-043，2026-10-10 闭合，取代 TD-017 初版的"超出部分保留原文"语义）。

### 3.3 span 冲突与 allowlist

`resolveSpans` 稳定排序：起点升序、相同起点终点降序。非允许 span 如与已保留项重叠则丢弃，不把重叠项合并成更大的区间，也没有额外实体类型优先级；相邻区间可以同时保留。

allowlist 使用区分大小写的字面子串搜索，不是字段白名单，也不是要求整段文本相等。任何与允许区间重叠的检测候选都会被排除。因此允许较短子串可能使更长敏感值保持原文，配置时需考虑覆盖范围。

已符合占位符正则的文本不再次脱敏，即使属于其他 nonce。允许项和占位符重叠过滤是当前实际行为，不能承诺所有被正则识别的内容都会被替换。

## 4. 映射与替换

格式见 [placeholder-format.md](placeholder-format.md)。每请求生成 8 字节随机 nonce，编码为 16 位小写十六进制；序号从 1 开始。相同原文复用同一占位符，不同请求不共享映射。

三张表分别保存 `rawToPh`、`phToRaw`、`phToEsc`。映射仅存于 Go 内存，未加密，删除状态只释放引用，不保证内存立即擦除。

`Mapping.Redact` 按起点升序，用 builder 追加未修改片段和占位符；JSON 字面量替换也按起点升序构造新字节缓冲，依据原始偏移而非在原字符串上修改，所以不会因替换长度改变后续偏移。

默认最多 1024 个不同原值；`max_mapping_bytes` 累计的是原值字节数，不含三张表、转义字符串、占位符与 Go 对象开销，不能理解为实际内存硬上限。单个原值还受 1 MiB 内部上限限制。

当前 visitor 把 `Mapping.Redact` 错误当成“节点不修改”，没有向插件返回错误。容量耗尽可能使后续敏感节点原样发往上游，即使开启 fail-closed（TD-CPA-031）。目标修复必须向上传递错误，整份请求原子拒绝，禁止返回半份脱敏正文。

## 5. 普通响应还原

使用同一个 RequestID 查询映射；未命中映射或空正文时返回空修改对象，由宿主保持原响应。有效 JSON 内全部字符串值调用 `Mapping.Restore`，只还原表中精确匹配的占位符；未知 nonce/序号保持原样。

还原值通过 `json.Marshal` 编成字符串字面量。响应 JSON 解析失败、没有命中时保持原响应，不重新请求上游，不猜测占位符。

nonce 提供跨请求隔离而非权限控制。上游能够看到本请求占位符并可能重复输出它，插件会还原为原文；插件不能据此阻止恶意模型向下游复述同请求隐私。

## 6. 流式还原：现状与目标

现状（2026-10-11 更新，TD-CPA-045）：宿主逐事件投递流块，块形态可能是裸 JSON（OpenAI/Gemini）、单行 SSE 字段（Codex 的 `event:`/`data:` 分开投递）或完整 SSE 帧（Claude 等）。旧 `RestoreSSEChunk` 按 `\n` 分行，把无换行块整块缓冲并 `DropChunk=true`，客户端只收到 `data: [DONE]`；已删除，连同 `lineBuf`。

`plugin.go` `InterceptStreamChunk` 调用 `StreamRestorer.RestorePayload(SourceFormat, chunk)`，不再设置 `DropChunk`：

| 步骤 | 行为 | 源码 |
|---|---|---|
| 一进一出 | 每个输入块恰好产出一个输出块；未变化返回空 Body 由宿主沿用原块；映射为空时直接放行 | `stream_payload.go` |
| 形态分派 | 去前导空白后：`{`/`[` 开头且合法 JSON → bare；SSE 字段名或 `:` 开头 → sse（逐行处理，保留原行尾，不跨块缓冲行）；其余（如 `[DONE]`）→ opaque 原样 | `payloadShapeOf` |
| 定点编辑 | 只替换 JSON 字符串字面量，其余字节不变；嵌套上限 64 | `jsonedit.go` `editStrings` |
| 文本流还原 | 协议规划器标出文本字段并给出跨事件稳定的文本流键；该字段与上次残留拼接后还原，末尾可能是本 nonce 占位符开头的部分（≤33 字节）暂扣；其他字符串只还原完整占位符（`Mapping.RestoreCount`） | `stream_text.go`、`stream_openai.go`/`stream_claude.go`/`stream_gemini.go`/`stream_responses.go` |
| 结束信号投递 | OpenAI（`finish_reason`）/Gemini（`finishReason`）向结束事件注入字段；Claude（`content_block_stop`/`message_delta`/`message_stop`）与 Responses（`*.done`、`output_item.done`、`completed` 等）以最近一次 delta 为模板在结束帧前合成增量帧；Codex 单行模式由 `event:` 行名触发 | `deliverEnded`、`deliverByEventName` |
| 暂扣启用条件 | openai = bare+SSE；gemini/antigravity = bare；claude/openai-response/codex/interactions = SSE；其他组合只还原完整占位符 | `carryEnabled` |
| 统计 | `StreamStats`（Chunks/Events/Restored/Unknown/Carried/MaxPending/Flushed/Synthesized/Passthrough/Leftover/UnsupportedEvent/MissingTemplate/LeftoverAtEnd/DanglingSamples）经 `StreamRestorer.Stats()` 读取，`Reset` 不清零 | `stream_text.go`、`stream.go` |
| 可观测性 | `stream.summary`/`stream.flush` 带 `source_format`、`unsupported_event`、`missing_template`、`leftover_at_end`、`dangling_samples`；`redact.done` 带 `stale`/`stale_samples`（其他 nonce 占位符，样例掩码） | `trace_hooks.go`、`plugin.go` |

文本流范围：OpenAI `delta.content`/`reasoning_content`/`reasoning`/`refusal` 与 `tool_calls[].function.arguments`；Claude `delta.text`/`thinking`/`partial_json`；Gemini/Antigravity `parts[].text`（区分 thought）；Responses/Codex `output_text`/`refusal`/`reasoning_text`/`reasoning_summary_text`/函数参数/custom 工具输入；Interactions `step.delta` 的 `delta.text`、`delta.content.text`、字符串 `delta.arguments`，`step.stop` 只结束同 `index` 的流，`interaction.completed`/`event: done`/`data: [DONE]` 结束全部。`RestoreChunk`/`Flush`/`Reset`/`Pending` 字节级契约保留。

验证（单元/属性/fuzz，非真实宿主）：`TestStreamHost*`、`TestStreamPreserve*`、`TestRestorePayload*`、`TestStreamProperty*`（原 4 协议 × 250 次 + 新增 4 组合 × 200 次、切片 1–40 字节）、`TestInteractions*`、`FuzzRestorePayload`、`FuzzDetect`、`FuzzRedactJSON`。新增缺陷探索测试为 `TestBugCond*`，保留性测试为 `TestPreserve*`（TD-CPA-052）。

仍开放（TD-CPA-032 残留）：SDK 没有"最后一块"标志；流在结束信号前中断时，各文本流残留（≤33 字节）在 `HandleRequestComplete` 的 `Flush()` 计入 `StreamStats.Leftover` 后丢弃，终块投递仍无通道。

已知限制（TD-CPA-046–050、052）：工具参数原值含引号时按纯文本替换不转义（与非流式一致）；Responses 合成增量帧复用模板 `sequence_number`；Gemini `alt` 非 SSE 模式只做完整占位符还原；OpenAI 结束块目标字段存在但非字符串（如 `"content":null`）时残留无法注入、计入 `Leftover`；`editStrings` 失败（嵌套 >64）时事件原样透传且已执行的暂扣不回滚；`openai-response` 裸 JSON 不暂存（残片计入 `UnsupportedEvent`）；纯文本 id 需配置安全前缀，且扫描不按工具来源限定；完整键名 `PWD` 可能漏掉真实密码。

流式转义还原（TD-CPA-040，2026-10-10 修复）：`RestoreEscapedBytes` 不再整载荷反转义。整载荷是单个 JSON 字符串字面量时，反转义内容、按明文占位符还原后重新 JSON 编码；否则只定点还原 `\u005b/\u005d` 括号转义并按 escaped 形式替换占位符，结构转义保持不变。

并发（TD-CPA-039，2026-10-10 修复）：`Mapping` 与 `StreamRestorer` 内部各自持互斥锁，同 RequestID 钩子并发不再产生数据竞态。

目标：在真实宿主上验收上述行为（端到端未完成）；未知占位符不触发还原；完成通知只能清理状态，不能假设其返回值能补发流数据。

## 7. 生命周期与资源

状态表持有映射、流恢复器和 lastSeen，条目字段访问由 `requestState.mu` 保护。完成通知删除条目并刷新恢复器 pending。janitor 每分钟清理空闲超过 TTL 的条目；admitted 在途条目保留至 2×TTL（hard deadline）后强制清理，兼顾恢复能力与状态表不被耗尽（TD-CPA-038，2026-10-10）。

`max_requests` 满时只驱逐 lastSeen 超过 TTL 的条目；满表全在途时新准入被拒绝（fail-closed）。重配更新上限与 TTL 但不清空状态表；重新注册（register/reconfigure 成功）会把旧实例状态表整体移交新实例，在途映射跨热重载保留。残留风险：超过 2×TTL 的超长请求响应不可还原；满表全活跃期间新请求被拒。

after-auth（TD-CPA-037，2026-10-10 修复）：`InterceptRequestAfterAuth` 无状态条目时新建映射完整脱敏并写回状态表；有状态条目时用既有映射重新脱敏新正文。after-auth 阶段新增的敏感值不再原文外发。

关闭语义（TD-CPA-041，2026-10-10 修复）：`shuttingDown` 后 register/reconfigure 被拒绝，插件不会在关闭后被生命周期调用复活；标记只由 `cliproxy_plugin_init` 重置。

数组检测上下文（TD-CPA-042，2026-10-10 修复）：textKeys 数组的 arrayText 上下文只作用于数组的直接字符串元素，不穿过对象子树，避免结构对象未知字段被误脱敏。

关闭标记先阻止普通新调用并解除全局引用，再等待已登记的调用结束。它不显式 `Clear` 所有映射，不阻止生命周期方法重新注册，也不是卸载/回调安全已验收的证明。

## 8. 安全目标与验收

目标：选定字段中的敏感值完成原子脱敏后才进入上游；未知占位符不猜测还原；日志和错误不包含原始正文或配置值；同请求恢复顺序可确定；容量与生命周期限制不破坏保护链路。

检测规则有覆盖边界，图片/音频、结构字段、Header 和 URL 不受当前算法保护；宿主与其日志也不属于插件可控制的隐私边界。

| 验收组 | 必须覆盖 |
|---|---|
| 检测 | 各规则正例/反例、X 校验位、银行卡前导字符、allowlist 重叠 |
| 遍历 | 各格式路径、裸字符串数组、工具参数嵌套、结构字段、深度与无效 JSON |
| 映射 | 同值复用、跨请求未知值、随机失败、条数/字节超限原子拒绝 |
| 流式 | 宿主载荷形态（裸 JSON / 单行 SSE / 整帧）、跨事件占位符、转义与代理对、结束信号投递、中断残留和 `[DONE]` |
| 生命周期 | 取消、重复 ID、重试正文变化、TTL、满表、热重载和同 ID 并发 |
| 安全边界 | 合成隐私值不进入日志、ABI 错误和可导出的源码快照 |

本轮仅做源码与文档对照。纯逻辑、Go 分发、C ABI 握手和实际 CPA 宿主分别验收，不能以 Go 单元测试通过替代共享库加载与端到端验收。
