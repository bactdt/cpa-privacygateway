# Bugfix Requirements Document

## Introduction

流式响应还原在真实 CPA v8.0.22 宿主上完全失效。宿主调用 `response.intercept_stream_chunk` 时传入的是**每个事件的载荷本身**（OpenAI chat / Gemini 为裸 JSON，Codex 原生 Responses 为不带换行的单行 `event:` / `data:`），而插件按"以 `\n` 结尾的 SSE 行"处理：没有换行的块被整体缓冲并返回 `DropChunk=true`，宿主跳过写出。结果是只要请求命中脱敏（映射非空），客户端只能收到宿主自写的 `data: [DONE]`，所有内容块和 `finish_reason` 块全部丢失；部署在 Cloudflare 后面时源站 100 秒内无字节输出，表现为 HTTP 524。

此外，即使在宿主以完整 SSE 帧投递的路径（Claude、经翻译器的 Responses），真实模型会把一个占位符切成多个 delta 事件，当前实现在事件边界原样释放残片，客户端看到 `[[PG_…` 残片而不是原值。

2026-10-11 已在本机用宿主源码构建的真实 CPA + mock 上游（按 7 字符切片回放最后一条用户消息）复现：带敏感信息的请求 `邮箱 user@example.com 手机 13812345678 结束` 上游收到占位符正确，客户端 HTTP 200 但正文只有 `data: [DONE]`；同一请求不含敏感信息时流式正常（映射为空走短路）。非流式还原在用户宿主上已验证正常。

术语：
- **映射非空**：本请求的 `Mapping.Size() > 0`。
- **裸载荷**：宿主传入的 `Body` 是单个事件的 JSON（或单行 SSE 字段），不含 `\n` 行终止。
- **文本流**：同一请求中、同一协议位置（同一 choice / content block / output 内容 / candidate）上由模型逐段生成的文本字段序列。
- **结束信号**：协议中标志某文本流结束的事件（如 `finish_reason` 非空、`content_block_stop`、`finishReason`、`response.output_text.done`）。
- **残留前缀（carry）**：文本流末尾可能是本请求占位符开头的那段文本，暂扣到下一事件。

## Bug Analysis

### Current Behavior (Defect)

1.1 WHEN 映射非空 AND 宿主传入的流块 `Body` 是不含 `\n` 的裸载荷（OpenAI chat 裸 JSON、Gemini 裸 JSON、Codex 原生逐行 `data: {...}` 或 `event: ...`） THEN the system 把整块追加到行缓冲并返回 `DropChunk=true`，宿主跳过写出该块

1.2 WHEN 映射非空 AND 整个流的块均为裸载荷 THEN the system 让客户端只收到宿主自写的 `data: [DONE]`，全部内容块与 `finish_reason` 块丢失；流开始后长时间无字节输出，经 Cloudflare 时表现为 HTTP 524

1.3 WHEN 映射非空 AND 相邻两个裸载荷行（如 Codex 的 `event: X` 与 `data: {...}`）先后被缓冲 THEN the system 把它们无分隔地拼接成一条损坏的行

1.4 WHEN 请求结束（`request.complete`）时行缓冲或尾部缓冲仍有内容 THEN the system 调用 `Flush()` 后丢弃其输出，缓冲中的内容永久丢失

1.5 WHEN 映射非空 AND 一个本请求的占位符被模型切分到同一文本流的相邻事件中（如 Claude 的连续 `content_block_delta`） THEN the system 在事件边界原样释放残片，客户端看到 `[[PG_…` 残片与剩余片段而不是原始值

### Expected Behavior (Correct)

2.1 WHEN 映射非空 AND 宿主传入任意形态的流块（裸 JSON、单行 SSE 字段、完整 SSE 帧） THEN the system SHALL 对每个输入块恰好产出一个输出块且从不返回 `DropChunk=true`；输出中每个 JSON 载荷合法，事件的结构、字段顺序与先后次序保持不变，只有目标文本字段的值可能改变

2.2 WHEN 映射非空 AND 整个流的块均为裸载荷 THEN the system SHALL 让客户端从第一个内容块起即时收到字节，最终收到全部内容块与 `finish_reason` 块，拼接后的文本等于原始文本完全还原的结果

2.3 WHEN 映射非空 AND 相邻流块为互相独立的单行 SSE 字段 THEN the system SHALL 逐块独立处理，不跨块拼接行，输出行与输入行一一对应

2.4 WHEN 映射非空 AND 一个文本字段内包含完整的本请求占位符 THEN the system SHALL 将其还原为原始值，并按 JSON 规则正确转义写回该字段

2.5 WHEN 映射非空 AND 一个本请求占位符被切分到同一文本流的相邻事件中 THEN the system SHALL 还原该占位符：只暂扣文本末尾可能构成本请求占位符前缀 `[[PG_<本请求 nonce>_` 的最短必要后缀（不超过 33 字节文本），其余立即输出，暂扣部分拼到该文本流下一事件的文本前面

2.6 WHEN 映射非空 AND 某文本流收到结束信号时仍有残留前缀 THEN the system SHALL 在该结束事件（或紧邻其前、同一输出块内的合成增量事件）中输出残留前缀；残留前缀不构成真实占位符时按原文输出

2.7 WHEN 请求结束时仍有未输出的残留前缀（流在结束信号前中断） THEN the system SHALL 清空全部缓冲并计入统计（未投递字节），不泄漏到其他请求

### Unchanged Behavior (Regression Prevention)

3.1 WHEN 本请求映射为空（未命中脱敏） THEN the system SHALL CONTINUE TO 原样放行所有流块（不返回 Body、不解析、逐字节不变）

3.2 WHEN 插件未启用 OR 流块为宿主 header-init 调用（`ChunkIndex == StreamChunkHeaderInitIndex`） OR `Body` 为空 THEN the system SHALL CONTINUE TO 返回空修改

3.3 WHEN 文本中出现其他 nonce 的占位符、本 nonce 但不在映射中的序号、或任何非占位符文本 THEN the system SHALL CONTINUE TO 原样输出这些文本

3.4 WHEN 载荷不是 JSON（如 `[DONE]`、`: keep-alive` 注释行、`event:` 行、无法解析的数据） THEN the system SHALL CONTINUE TO 原样透传

3.5 WHEN 映射非空 AND 一个事件不含任何占位符、且其所属文本流没有残留前缀 THEN the system SHALL CONTINUE TO 返回原始字节（不重新序列化）

3.6 WHEN 处理完整 SSE 帧中的 `data:` 行 THEN the system SHALL CONTINUE TO 保留 `data:` 前缀与分隔空格，注释行与其他字段行原样透传

3.7 WHEN 处理非流式响应（`InterceptResponse`） THEN the system SHALL CONTINUE TO 按现有 `RestoreJSON` 还原所有字符串字段

3.8 WHEN 处理请求（`InterceptRequestBeforeAuth` / `InterceptRequestAfterAuth`） THEN the system SHALL CONTINUE TO 按现有规则脱敏，上游只看到占位符

3.9 WHEN 调用 `StreamRestorer` 的 `RestoreChunk` / `Flush` / `Reset` / `Pending` THEN the system SHALL CONTINUE TO 保持现有字节级行为与测试结果（TD-CPA-016 / TD-CPA-030 契约）

3.10 WHEN 同一请求的流块回调并发到达 THEN the system SHALL CONTINUE TO 在 `-race` 下无数据竞态
