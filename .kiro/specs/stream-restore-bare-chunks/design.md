# stream-restore-bare-chunks Bugfix Design

## Overview

宿主 CPA v8.0.22 把每个流事件的**载荷本身**交给 `response.intercept_stream_chunk`，而插件的 `StreamRestorer.RestoreSSEChunk` 假设输入是以 `\n` 结尾的 SSE 行。裸载荷没有换行，于是被整块塞进 `lineBuf` 并返回 `DropChunk=true`，宿主 `continue` 跳过写出。映射非空时客户端只收到宿主自写的 `data: [DONE]`（经 Cloudflare 则 524）。

更深一层：真实模型会把一个占位符切到多个 delta 事件里。字节级尾部缓冲（`restoreChunkLocked` / `placeholderTail`）若跨事件拼接，会把上一个事件 JSON 里的 `[[PG_ab` 拼进下一个事件的 JSON，产出损坏 JSON。

修复策略：
1. **按载荷形态分派，从不丢块**：裸 JSON、单行 SSE 字段、完整 SSE 帧三种形态逐块独立处理，一进一出，`DropChunk` 恒为 false。
2. **在事件内的文本字段上还原**：解析每个 JSON 事件，按协议定位"模型生成文本"字段，以**文本**（而非 JSON 字节）为单位做占位符还原与残留前缀暂扣；暂扣内容按"文本流"（同一 choice / content block / output 内容 / candidate）拼到下一事件的同一字段前面。
3. **结束信号处投递残留**：收到协议的结束信号时，把残留前缀写进该事件（裸 JSON 协议：注入字段；SSE 文本协议：在同一输出块内、结束事件之前合成一个增量帧）。
4. **无变化返回原字节**：只有目标字段值实际变化才做定点字节替换（复用 `walk.go` 的偏移解码 + 替换），不重新序列化整个事件。

领域逻辑全部落在 `redact`（纯 Go + `encoding/json`），`plugin.go` 只做接线。

## Glossary

- **Bug_Condition (C)**：映射非空，且（C1）某个流块是不含 `\n` 的裸载荷，或（C2）某个本请求占位符被切到同一文本流的相邻事件。
- **Property (P)**：每个输入块恰好产出一个输出块且不丢弃；所有 JSON 合法、结构不变；每个文本流的输出文本拼接等于原文本完全还原后的结果。
- **Preservation**：映射为空时逐字节不变；非流式还原、请求脱敏、`RestoreChunk`/`Flush`/`Reset`/`Pending` 字节级契约、注释行透传、并发安全不变。
- **F / F'**：修复前 / 修复后的 `plugin.InterceptStreamChunk`（及其调用的 `StreamRestorer`）。
- **形态（shape）**：`bareJSON`（去前导空白后以 `{`/`[` 开头且 `json.Valid`）、`sseText`（以 `data:`/`event:`/`id:`/`retry:`/`:` 开头）、`opaque`（其他，如 `[DONE]`）。
- **文本流键（stream key）**：协议内标识一段连续生成文本的字符串，如 `c0/content`、`b1`、`o0/c0/text`、`g0/text`。
- **残留前缀（carry）**：文本流当前末尾、可能是 `[[PG_<nonce>_<digits>]]` 开头的部分，最长 `maxPlaceholderLen`=33 字节。
- **事件计划（eventPlan）**：协议规划器从一个事件 JSON 得出的目标字段路径、文本流键、结束键。
- **`InterceptStreamChunk`**：`plugin.go:265-284`，流块拦截入口。
- **`StreamRestorer`**：`redact/stream.go:8-14`，请求级流还原状态（已带互斥锁，TD-CPA-039）。

## Bug Details

### Bug Condition

宿主源码（`$H=/home/gh/go/pkg/mod/github.com/router-for-me/!c!l!i!proxy!a!p!i/v8@v8.0.22`）确认 `Body` 就是执行器产出的 `chunk.Payload`：

- `$H/sdk/api/handlers/handlers_stream.go:438-447`（非插件执行器的主路径 `transformStreamPayload`，含 bootstrap 首块 L515 与后续 L700）与 `:212-221`（插件执行器路径 `streamWithPluginExecutor`）都以 `Body: payload`、`SourceFormat: responseProtocol` 构造请求；`:459-468`/`:233-241` 中 `DropChunk` 为真则直接返回/`continue`，不写出。
- `:113-124`、`:413-424` 是 header-init 调用（`ChunkIndex = StreamChunkHeaderInitIndex`，无 Body），与还原无关，保持忽略。
- `responseProtocol` 由 `model_execution.go:249-254` 决定（exit 协议优先，否则入口协议）；入口协议即处理器 `HandlerType()`：`openai`（`openai_handlers.go:50`）、`openai-response`（`openai_responses_handlers.go:532`）、`claude`（`code_handlers.go:56`）、`gemini`（`gemini_handlers.go:38`）。

各协议 `Body` 形态：

| SourceFormat | Body 形态 | 依据 |
|---|---|---|
| `openai`（chat completions，含 `/v1/completions` 内部转换） | 裸 JSON `{"id":..,"choices":[{"index":0,"delta":{"content":"hel"}}]}`，无 `data:`、无 `\n` | 翻译器 `$H/internal/translator/openai/openai/chat-completions/openai_openai_response.go:29-38` 剥掉 `data:` 返回裸 JSON；处理器自写 `data: %s\n\n`（`openai_handlers.go:541,661,721`）与 `data: [DONE]\n\n`（`:530,646,748`）；`[DONE]` 不进入拦截器（翻译器 L32-36 返回空） |
| `gemini` | 裸 JSON（`candidates[].content.parts[].text`） | `gemini_handlers.go:249-251,323-325`：`alt==""` 时处理器自写 `data: ` 前缀与 `\n\n`；`alt` 非空时原样写（非 SSE 的 JSON 数组片段，按 opaque/非完整 JSON 透传） |
| `claude` | 完整 SSE 帧 `event: X\ndata: {...}\n\n` | 原生透传 `$H/internal/runtime/executor/claude_executor_stream.go:371-410` 逐行累积到空行再整帧发出；翻译路径 `translator/openai/claude/openai_claude_response.go:227-302` 用 `AppendSSEEventBytes(..., 2)`（`translator/common/bytes.go:102-112`）；处理器原样写（`code_handlers.go:295,312`） |
| `openai-response`（经翻译器） | 完整 SSE 帧 `event: X\ndata: {...}\n\n` | `translator/openai/openai/responses/openai_openai-responses_response.go:74-75` → `SSEEventData`（`common/bytes.go:80-88`） |
| `openai-response`（Codex 原生） | **单行**、无 `\n`：`event: response.output_text.delta` 与 `data: {...}` 分别是独立的块（空行块被 `handlers_stream.go:206,512,697` 的空载荷检查跳过） | `executor/codex_executor_stream.go:377-449` 扫描器逐行翻译，`codex/openai/responses/codex_openai-responses_response.go:15-31` 原样返回该行 |

`openai-response` 在拦截器**之后**还经过 `responseSSEValidator`（`handlers_stream.go:429-432,471-480`；实现 `:733-853`）：按 `\n\n` 切帧，要求每帧 `data:` 合并载荷为合法 JSON 或 `[DONE]`，否则 502。处理器端 `responsesSSEFramer`（`openai_responses_handlers.go:78-115,459-506`）再按行补换行、按帧写出。因此插件输出必须保持每个 `data:` 行是合法 JSON，合成帧必须以 `\n\n` 结束。

当前插件路径：`plugin.go:276` 调用 `RestoreSSEChunk`；`redact/stream.go:105-109` 找不到 `\n` 就把整块并入 `lineBuf` 并跳出；`:118-121` 输出为空返回 `drop=true`；`plugin.go:277-278` 返回 `DropChunk:true`；`plugin.go:290-293` 请求结束时 `Flush()` 输出被丢弃（TD-CPA-032 残留）。

**Formal Specification:**
```
FUNCTION isBugCondition(X)
  INPUT: X = (mapping, format, chunks[]) —— 一个请求的映射、SourceFormat 与按序到达的流块
  OUTPUT: boolean

  // C1：裸载荷被缓冲丢弃
  C1 ← EXISTS c IN X.chunks : len(c.Body) > 0 AND NOT contains(c.Body, '\n')
  // C2：本请求占位符被切到同一文本流的相邻事件
  C2 ← EXISTS token p IN X.mapping.Placeholders(), EXISTS stream key k :
          p 是 concat(text_k(events(X.chunks))) 的子串
          AND NOT (存在单个事件 e 使 p 是 text_k(e) 的子串)

  RETURN X.mapping.Size() > 0 AND (C1 OR C2)
END FUNCTION
```

其中 `text_k(e)` 指事件 `e` 在文本流 `k` 上的文本字段值（定义见"文本字段与结束信号"）。

### 文本字段与结束信号（范围决策）

| 协议 | 目标文本字段（路径 → 文本流键） | 结束信号 | 残留投递方式 |
|---|---|---|---|
| `openai` | `choices[i].delta.content` → `c<index>/content`；`.delta.reasoning_content` → `c<index>/reasoning_content`；`.delta.reasoning` → `c<index>/reasoning`；`.delta.refusal` → `c<index>/refusal`；`.delta.tool_calls[j].function.arguments` → `c<index>/tool<tool.index>` | `choices[i].finish_reason` 非空 → 该 choice 全部键 | 结束事件内已有该字段：直接前置；否则向 `choices[i].delta` 注入字段（工具参数注入 `tool_calls` 元素） |
| `claude` | `content_block_delta`：`delta.text`（text_delta）、`delta.thinking`（thinking_delta）、`delta.partial_json`（input_json_delta）→ `b<index>` | `content_block_stop{index}` → `b<index>`；`message_delta` / `message_stop` → 全部 | 在结束帧之前合成 `event: content_block_delta\ndata: <模板>\n\n`（模板 = 该键最近一次 delta 事件，仅替换文本字段） |
| `gemini` | `candidates[i].content.parts[j].text` → `g<index>/text`（`thought:true` 的 part → `g<index>/thought`） | `candidates[i].finishReason` 非空 → 该 candidate 全部键 | 结束事件内有对应 part：直接前置；否则在 `parts` 数组开头注入 `{"text":..}`（缺 `parts`/`content` 时逐级注入） |
| `openai-response` | `response.output_text.delta` / `response.refusal.delta` / `response.reasoning_text.delta` / `response.reasoning_summary_text.delta` / `response.function_call_arguments.delta` / `response.custom_tool_call_input.delta` 的 `delta` → `o<output_index>/c<content_index 或 summary_index>/<kind>`（函数参数 `o<output_index>/args`） | 对应 `*.done`（同 kind 全部键）；`response.content_part.done` / `response.output_item.done` / `response.completed` / `response.incomplete` / `response.failed` → 全部。既识别 `data:` JSON 的 `type`，也识别 `event:` 行名（Codex 单行模式下 `event:` 行先于 `data:` 行单独到达） | 在结束事件（或其 `event:` 行）之前合成增量帧 `event: <模板 type>\ndata: <模板>\n\n` |
| 其他/空 | 无目标字段 | — | 仅还原完整占位符，不暂扣 |

决策说明：
- **工具调用参数纳入范围**：请求侧会脱敏工具参数（`toolArg`），模型在流式工具调用里回写占位符时，客户端执行工具必须拿到原值；非流式 `RestoreJSON` 本就还原全部字符串，流式应一致。参数文本是 JSON 片段，但占位符只含 `[ ] P G _ 0-9 a-f`，在参数文本中按纯文本替换与非流式 `Mapping.Restore` 行为一致（原值含引号时两侧同样不转义，见 Hypothesized Root Cause 之外的已知限制，登记技术债）。
- **非目标字符串一律做"完整占位符还原"**：如 Responses 的 `*.done.text`、`response.completed.response.output[...]`、Claude `message_start.message.content`、Gemini `functionCall.args`（整块下发）。这些字段携带完整文本，按 `Mapping.Restore` 只替换完整占位符、不暂扣，与非流式一致。
- **暂扣仅在能投递时启用**：`openai`/`gemini` 仅在 `bareJSON` 形态、`claude`/`openai-response` 仅在 `sseText` 形态启用暂扣；协议与形态不匹配时退化为"只还原完整占位符"，保证不出现无法投递的残留。
- **最后兜底**：流在结束信号前中断时，残留在 `HandleRequestComplete` 的 `Flush` 中清空并计入 `Leftover` 统计；SDK 无终块输出通道（TD-CPA-032 残留，保持登记）。

### Examples

- OpenAI chat，映射 `{[[PG_a1b2c3d4e5f60718_1]] → user@example.com}`，上游 7 字符切片：块 `{"choices":[{"index":0,"delta":{"content":"邮箱 [[PG_a"}}]}`。当前：`DropChunk=true`，客户端无输出。期望：输出 `...{"content":"邮箱 "}...`，暂扣 `[[PG_a`；下一块 `1b2c3d4` 输出 `""`（整段仍是前缀，继续暂扣）；直到 `..._1]] 手机` 的块输出 `user@example.com 手机`。
- OpenAI 结束块 `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` 而该 choice 暂扣 `[`（模型原文以 `[` 结尾）：期望输出 `{"choices":[{"index":0,"delta":{"content":"["},"finish_reason":"stop"}]}`。
- Claude：`b0` 暂扣 `[[PG_a1`，随后块 `event: content_block_stop\ndata: {"type":"content_block_stop","index":0}\n\n`：期望输出 `event: content_block_delta\ndata: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"[[PG_a1"}}\n\nevent: content_block_stop\ndata: {...}\n\n`。
- Codex 单行：块 `event: response.output_text.done`（独立块）且 `o0/c0/text` 有残留：期望输出 `event: response.output_text.delta\ndata: {<模板，delta=残留>}\n\nevent: response.output_text.done`。
- 其他 nonce：`[[PG_ffffffffffffffff_1]]` 原样输出；本 nonce 但不在映射中的 `_999` 原样输出。
- 边界：块 `[DONE]`、`: keep-alive\n\n`、无法解析的 `data: {bad` 原样透传；映射为空时任何块都不改动。

## Expected Behavior

### Preservation Requirements

**Unchanged Behaviors:**
- 映射为空时 `InterceptStreamChunk` 返回空修改，不解析载荷（`mapping.Size()==0` 短路）。
- 插件未启用、header-init 调用、空 `Body` 返回空修改。
- 不含占位符且所属文本流无残留的事件返回原字节（`changed=false` → 空 `Body`，宿主沿用原块）。
- SSE 文本中注释行、`event:`/`id:`/`retry:` 行原样透传；`data:` 行保留前缀与分隔空格。
- 其他 nonce、未知序号、非占位符文本原样输出。
- `InterceptResponse`（非流式）、请求脱敏两个入口不变。
- `StreamRestorer.RestoreChunk` / `Flush` / `Reset` / `Pending` 的字节级行为及其现有测试（`TestRestoreStreamCrossChunk`、`TestRestoreStreamUnknownTokenPreserved`、`TestRestoreStreamEscapedToken`、`TestRestoreStreamErrorHandling`、`TestRestoreStreamMultipleCallsStateful`、`TestRestoreStreamSSEFormat`、`TestRestoreStreamFlush`、`TestRestoreStreamMaxLengthPlaceholderBuffered`）不变；`Reset`/`Flush` 额外清空文本流残留，`Pending` 额外计入残留字节（新增状态，旧路径下恒为 0）。
- `-race` 下并发安全（沿用 `StreamRestorer.mu`）。

**行为变更（显式标注，需同步改测试）：**
- 删除 `RestoreSSEChunk` 与 `lineBuf`：宿主从不把一行切到两个块里（Claude 整帧、Codex 整行），跨块行缓冲只会制造 C1 缺陷。依赖它的 `TestRestoreStreamSSEIncompleteLineHeldAcrossChunks`（断言 drop）改写为"不完整行也立即输出"；`TestRestoreStreamSSEPlaceholderSplitAcrossEventsNotMerged`（断言跨事件**不**还原）改写为"同一文本流跨事件还原、不同文本流不合并"；`TestRestoreStreamSSERestoresDataLinesOnly` 改为调用新 API，断言不变。`plugin_test.go` 的 `TestPluginInterceptStreamChunk`（断言 chunk1 drop）改写为宿主形态。

**Scope:**
所有不满足 isBugCondition 的输入不受影响，包括：
- 映射为空的请求的全部流块；
- 非流式响应与请求阶段；
- 映射非空但每个占位符都完整落在单个事件文本内、且流块均为含 `\n` 的完整 SSE 帧（Claude / 翻译后的 Responses）的输出：与修复前 **JSON 语义相等**（解码后字段值相同）；未被还原的字段与非 `data:` 行逐字节相同。被还原的那个字符串字面量会以 `encodeTextLiteral` 重新编码（修复前是在转义字节上定点替换），其中原有的 `\uXXXX` 等等价转义写法可能变化，属可接受差异。

## Hypothesized Root Cause

1. **输入契约误判（已在源码确认）**：`RestoreSSEChunk` 按"宿主转发原始 SSE 字节流"设计，而宿主逐事件投递载荷且自行加 `data:` 前缀（`openai_handlers.go:541`）。无 `\n` 的块走 `stream.go:105-109` 缓冲分支，`:118-121` 返回 drop。
   - Codex 单行块同样无 `\n`，且 `lineBuf` 直接拼接相邻行（`stream.go:95-101`），即使后来输出也是损坏行。
2. **丢弃语义不可逆**：`DropChunk=true` 让宿主跳过写出（`handlers_stream.go:465-467`），被缓冲的字节只能靠后续块"捎带"输出；裸载荷永远不带 `\n`，所以永远不输出。
3. **终止输出无通道**：`HandleRequestComplete` 的 `Flush()` 结果被丢弃（`plugin.go:292`），SDK 也没有终块标志，缓冲内容永久丢失（TD-CPA-032 残留）。
4. **字节级跨事件暂扣不适用于 JSON 事件**：`restoreChunkLocked` 在原始字节上切尾，若用于逐事件 JSON 会把半个占位符从一个 JSON 移到另一个 JSON 外部，破坏两个事件的合法性；当前实现只好在事件边界释放残片（`stream.go:111-113`），导致 C2。正确做法是在解码后的文本字段上暂扣，并按文本流键携带。

## Correctness Properties

Property 1: Bug Condition - 宿主形态流块不丢弃且跨事件占位符完整还原

_For any_ 请求输入 X（映射非空，SourceFormat ∈ {openai, gemini, claude, openai-response}，流块为该协议的宿主形态：随机把含若干本请求占位符与普通文本的原文按任意长度切成 N 个事件，并以结束信号收尾），当 isBugCondition(X) 为真时，修复后的 `InterceptStreamChunk` SHALL 对每个输入块返回 `DropChunk=false`，每个有效输出块中的每个 JSON 载荷 `json.Valid`，事件个数与顺序不变（合成增量帧只出现在结束事件之前），且每个文本流的输出文本按序拼接等于 `Mapping.Restore(原文)`。

**Validates: Requirements 2.1, 2.2, 2.3, 2.4, 2.5, 2.6, 2.7**

Property 2: Preservation - 非缺陷输入的输出与修复前一致

_For any_ 请求输入 X，当 isBugCondition(X) 为假时（映射为空；或占位符均完整落在单个事件内且流块为含 `\n` 的完整 SSE 帧；或载荷为 `[DONE]`/注释/非 JSON；或插件未启用 / header-init / 空 Body），修复后函数的有效输出（`Body` 非空取 `Body`，否则取输入）SHALL 与修复前函数相同（映射为空、opaque 载荷、开关/header-init/空 Body 逐字节相同；单事件内完整占位符的 SSE 帧为 JSON 语义相等且非 `data:` 行逐字节相同）；`RestoreChunk`/`Flush`/`Reset`/`Pending`、`InterceptResponse` 与请求脱敏的结果 SHALL 不变。

**Validates: Requirements 3.1, 3.2, 3.3, 3.4, 3.5, 3.6, 3.7, 3.8, 3.9, 3.10**

**Expected behavior predicate（供 Property 1 断言）：**
```
FUNCTION expectedBehavior(X, outs)
  INPUT: X 同上；outs[i] = InterceptStreamChunk'(X.chunks[i])
  OUTPUT: boolean

  eff[i] ← IF len(outs[i].Body) > 0 THEN outs[i].Body ELSE X.chunks[i].Body
  RETURN len(outs) = len(X.chunks)
     AND FOR ALL i: outs[i].DropChunk = false
     AND FOR ALL i, FOR ALL JSON 载荷 j IN eff[i]: json.Valid(j)
     AND FOR ALL i: 去掉合成增量帧后，eff[i] 与 X.chunks[i] 除目标文本字段值外结构相同
     AND FOR ALL 文本流 k: concat_i(text_k(eff[i])) = Mapping.Restore(concat_i(text_k(X.chunks[i])))
END FUNCTION
```

## Fix Implementation

### Changes Required

假设上述根因成立：

**File**: `redact/mapping.go`

**Function**: 新增 `(*Mapping).RestoreCount(text string) (out string, restored, unknown int)`

**Specific Changes**:
1. **计数还原**：与 `Restore` 相同的正则替换，同时统计命中映射的个数与"本 nonce 但不在映射中"的个数（供统计）。`Restore` 保持不变（约 +20 行，文件仍 < 500）。

**File**: `redact/jsonedit.go`（新）

**Specific Changes**:
2. **路径编辑原语**（复用 `walk.go` 的 `offsetDecoder` / `replacement` / `applyReplacements`）：
   - `editStrings(body []byte, visit func(path []string, value string) (string, bool)) ([]byte, bool, error)`：遍历所有字符串值（深度上限 64），返回定点替换后的字节；无替换时返回原切片与 `false`。
   - `locateContainer(body []byte, path []string) (open int, empty bool, ok bool)`：返回某对象/数组 `{`/`[` 后一字节的偏移以及容器是否为空，用于注入。
   - `insertAt(body []byte, offset int, fragment []byte, needComma bool) []byte`。
3. **字符串编码**：新增 `encodeTextLiteral(value string) []byte`，使用 `json.Encoder` + `SetEscapeHTML(false)`，去掉末尾换行；避免把模型文本中的 `<>&` 改写为 `\u003c` 等。

**File**: `redact/stream_text.go`（新）

**Specific Changes**:
4. **文本流残留引擎**（由 `StreamRestorer` 持有，受同一互斥锁保护）：
   - 状态：`carry map[string]string`、`templates map[string]streamTemplate`（SSE 协议用：最近一次 delta 事件原字节 + 文本字段路径 + `event:` 名）、`stats StreamStats`。
   - `restoreText(key, value string, final bool) string`：`combined = carry[key] + value`；`restored = RestoreCount(combined)`；`final` 时清空残留并返回 `restored`；否则 `tail = placeholderTail([]byte(restored))`（沿用 `stream.go` 的 nonce 前缀判断），`carry[key] = restored[len-tail:]`，返回 `restored[:len-tail]`。`tail` 起点必为 ASCII `[`，切分不破坏 UTF-8。
   - `takeCarry(keys)`：取出并清空指定键（或全部）的残留，用于结束信号投递；空残留跳过。
   - `StreamStats` 结构（为 request-trace-log 预留，见下）。

**File**: `redact/stream_openai.go`、`redact/stream_claude.go`、`redact/stream_gemini.go`、`redact/stream_responses.go`（新，各一个协议）

**Specific Changes**:
5. **协议规划器**：每个文件提供 `planXxx(event []byte) (eventPlan, bool)`，用最小结构体 `json.Unmarshal` 读出索引、类型、结束标志，产出：
   ```go
   type textTarget struct { path string; key string } // path 以 "\x00" 连接 transformJSON 的路径段
   type eventPlan struct {
       targets []textTarget
       endKeys []string // 本事件结束的文本流键；endPrefix 匹配同前缀全部键
       endAll  bool
   }
   ```
   以及投递函数：`openai`/`gemini` 提供 `injectXxx(event []byte, key, carry string) ([]byte, bool)`（定位容器后插入字段/元素）；`claude`/`openai-response` 提供 `synthXxx(tpl streamTemplate, carry string) []byte`（用模板替换文本字段并加 `event:` 行和 `\n\n`）。`openai-response` 另提供 `responsesEndByEventName(name string) (kinds []string, all bool)`。

**File**: `redact/stream_payload.go`（新）

**Specific Changes**:
6. **入口 API**：
   ```go
   // RestorePayload 处理宿主投递的一个流块，从不丢弃。changed=false 表示输出与输入相同，
   // 调用方应返回空 Body 让宿主沿用原块。
   func (s *StreamRestorer) RestorePayload(format string, chunk []byte) (out []byte, changed bool)
   ```
   - 映射为空 → `(chunk,false)`；计数 `Chunks++`。
   - 形态分派：`bareJSON` → `restoreEvent`；`sseText` → 按 `\n` 切行（保留 `\r\n` 原样），`data:` 行载荷若为合法 JSON 则 `restoreEvent` 并只替换载荷字节；`event:` 行记录当前帧事件名并（`openai-response`）检查名称结束信号；空行重置帧起点；合成帧插入当前帧起点（帧的第一行之前）；`opaque` → 原样，`Passthrough++`。
   - `restoreEvent(format, shape, event)`：规划 → `editStrings`（目标路径走 `restoreText`，`final` = 该键在本事件结束；其他字符串走 `RestoreCount` 完整还原）→ 对 `endKeys`/`endAll` 中仍有残留且本事件无目标字段的键做投递（注入或返回合成帧）→ 对有暂扣的 SSE 目标更新模板。
   - 协议与形态不匹配暂扣能力时（见范围决策）`targets` 置空，仅完整还原。
   - 解析失败 → 原样透传，`Passthrough++`（与现有 fail-open 流式语义一致）。

**File**: `redact/stream.go`

**Specific Changes**:
7. **收敛旧路径**：删除 `RestoreSSEChunk`、`appendSSELine`、`sseDataSeparator`、`sseDataPayload`（逻辑移入 `stream_payload.go` 的行处理）与 `lineBuf`；`StreamRestorer` 增加文本流状态字段；`Reset`/`Flush` 同时清空残留（`Flush` 把残留字节计入 `stats.Leftover`）；`Pending` 计入残留字节；新增 `Stats() StreamStats`。`RestoreChunk`/`restoreChunkLocked`/`placeholderTail`/`isPlaceholderPrefix` 不变。

**File**: `plugin.go`

**Function**: `InterceptStreamChunk`、`HandleRequestComplete`

**Specific Changes**:
8. **接线**：`body, changed := restorer.RestorePayload(req.SourceFormat, req.Body)`；`changed` 为假返回空响应，否则返回 `Body: body`；任何情况下都不设置 `DropChunk`。`HandleRequestComplete` 保持调用 `Flush()`（输出仍无通道，残留已计入统计），不新增日志（request-trace-log 接入后再暴露）。`abi.go`（499 行）不改动。

**File**: `docs/design-docs/redaction-algorithm.md`、`docs/exec-plans/tech-debt-tracker.md`、`docs/QUALITY_SCORE.md`、`ARCHITECTURE.md`（如列有 redact 文件清单）

**Specific Changes**:
9. **文档同步**：更新流式还原现状；新增技术债条目"流式块被裸载荷误判丢弃"（修复后 fixed）；TD-CPA-032 补充"宿主逐事件投递、无行分帧需求，终块投递仍无通道"；登记工具参数原值含引号时不转义的限制（与非流式一致）、Responses 合成帧复用模板 `sequence_number` 的限制、Gemini `alt` 非 SSE 模式只做完整还原。

**与 request-trace-log 规格的协调**：本修复拥有 `redact/stream.go` 与 `InterceptStreamChunk`。`StreamStats` 在此定义：
```go
type StreamStats struct {
    Chunks      int // RestorePayload 调用次数
    Events      int // 成功解析的 JSON 事件数
    Restored    int // 还原的占位符个数
    Unknown     int // 本 nonce 但不在映射中的占位符个数
    Carried     int // 发生暂扣的次数
    MaxPending  int // 单个文本流最大暂扣字节数
    Flushed     int // 结束信号投递残留的次数
    Synthesized int // 注入字段或合成增量帧的次数
    Passthrough int // 非 JSON / 解析失败原样透传的载荷数
    Leftover    int // Flush 时未能投递而丢弃的残留字节数
}
```
request-trace-log 的 `Stats()` 字段集合可直接映射（其设计中 `Chunks` 的含义从"RestoreSSEChunk 调用次数"改为"RestorePayload 调用次数"）；其 `FlushWithResult` 可在本修复后以 `Leftover`/残留是否为未闭合前缀实现，本修复不实现它。

## Testing Strategy

### Validation Approach

两阶段：先在未修复代码上用宿主形态的流块暴露缺陷（预期失败，确认根因），再验证修复后缺陷消失、非缺陷输入行为不变。测试只用标准库（`testing`、`math/rand` 固定种子的生成器、`testing/quick`、Go fuzz），不引入新依赖。保留测试通过插件公共入口 `InterceptStreamChunk` 与 `RestoreChunk` 编写，避免依赖将被删除的 `RestoreSSEChunk`。

### Exploratory Bug Condition Checking

**Goal**: 在实施修复前暴露反例，确认或推翻根因分析；若推翻需重新假设。

**Test Plan**: 新建 `plugin_stream_host_test.go`，构造带映射的请求（先 `InterceptRequestBeforeAuth` 脱敏 `邮箱 user@example.com 手机 13812345678 结束`），取得占位符化文本后按宿主形态生成流块逐个调用 `InterceptStreamChunk`，按 `expectedBehavior` 断言。

**Test Cases**:
1. **OpenAI 7 字符切片**：mock 同款事件 + `delta:{}` 结束块（未修复：每块 `DropChunk=true`，会失败）
2. **随机切片性质**：固定种子 200 次随机切分长度 1..12（未修复：失败）
3. **Codex 单行**：`event:` 行与 `data:` 行分别成块（未修复：`DropChunk=true`，会失败）
4. **Claude 整帧跨事件占位符**：占位符切到两个 `content_block_delta`（未修复：输出残片，会失败）

**Expected Counterexamples**:
- 第一个裸块即返回 `DropChunk=true` 且 `Body` 为空；拼接文本为空。
- Claude 帧输出含 `[[PG_` 残片，拼接文本不含 `user@example.com`。
- 可能原因：行分帧假设（C1）、事件边界释放残片（C2）。

### Fix Checking

**Goal**: 验证对所有满足缺陷条件的输入，修复后函数产生期望行为。

**Pseudocode:**
```
FOR ALL X WHERE isBugCondition(X) DO
  outs ← [InterceptStreamChunk'(c) FOR c IN X.chunks]
  ASSERT expectedBehavior(X, outs)
END FOR
```

### Preservation Checking

**Goal**: 验证对所有不满足缺陷条件的输入，修复后函数与原函数结果相同。

**Pseudocode:**
```
FOR ALL X WHERE NOT isBugCondition(X) DO
  ASSERT effective(InterceptStreamChunk(X)) = effective(InterceptStreamChunk'(X))
END FOR
```

**Testing Approach**: 保留性检查推荐用基于性质的测试：自动生成大量输入、覆盖手写用例遗漏的边界、对"所有非缺陷输入"给出更强保证。

**Test Plan**: 先在未修复代码上观察非缺陷输入的输出并固化为期望，再写性质测试。

**Test Cases**:
1. **空映射逐字节不变**：随机裸 JSON / SSE 帧 / `[DONE]`，映射为空时响应为空修改（未修复与修复后相同）
2. **完整帧完整占位符**：随机含完整占位符的 `data: {...}\n\n` 帧（单事件内完整）+ `: keep-alive` 注释，有效输出与未修复相同（已还原、注释原样）
3. **开关与 header-init**：插件禁用、`ChunkIndex == StreamChunkHeaderInitIndex`、空 Body 均为空修改
4. **非流式与字节级 API**：`InterceptResponse` 输出与 `RestoreChunk`/`Flush` 系列现有测试不变

### Unit Tests

- `redact/jsonedit_test.go`：路径替换保持其他字节不变、无替换返回原切片、`locateContainer` 空/非空容器、HTML 字符不转义、深度上限。
- `redact/stream_text_test.go`：完整占位符还原、跨 2..N 次暂扣、最长 33 字节、`final` 清空、其他 nonce 不暂扣、UTF-8 文本切分安全、统计字段。
- `redact/stream_openai_test.go` / `_claude_test.go` / `_gemini_test.go` / `_responses_test.go`：各协议宿主形态 fixture（含多 choice/多 block、工具参数、结束信号注入/合成、非目标字段完整还原）。
- `redact/stream_payload_test.go`：三种形态分派、`\r\n` 保留、注释透传、解析失败透传、合成帧位于帧起点、Codex 单行 `event:` 名触发投递。

### Property-Based Tests

- 随机原文（混合中英文、占位符、`[`、`[[PG_` 假前缀、其他 nonce 占位符）× 随机切分 × 每个协议：拼接还原文本等于 `Mapping.Restore(原文)`；每个输出 JSON 合法；块数不变、`DropChunk` 恒 false。
- 无占位符随机事件：`changed=false` 且输出为原切片。
- 扩展 `redact/fuzz_test.go` 新增 `FuzzRestorePayload`：任意字节输入不 panic、输出中原本合法的 JSON 载荷仍合法。

### Integration Tests

- 插件级：`plugin_stream_host_test.go` 覆盖四协议宿主形态端到端（脱敏 → 流块拦截 → 拼接）。
- `make check`（fmt-check + vet + build + lint + test(-race) + coverage + docs）全绿。
- 真实宿主 E2E（本机，宿主进程由用户/会话单独启动）：
  1. `make build && cp build/libprivacygateway.so /tmp/cpahost/run/plugins/linux/amd64/privacygateway-v0.1.0.so`
  2. 重启宿主：`pid=$(pgrep -x cpa)`，`kill "$pid"`（勿用 `pkill -f`，会匹配调用者 shell 命令行）；在 `/tmp/cpahost/run` 下重新运行 `/tmp/cpahost/cpa -config /tmp/cpahost/run/config.yaml -local-model`（后台、输出到 `/tmp/cpahost/cpa.out`）；mock `/tmp/cpahost/mock.py`（127.0.0.1:18999）保持运行。
  3. `curl -sN http://127.0.0.1:18317/v1/chat/completions -H 'Authorization: Bearer test-api-key' -H 'Content-Type: application/json' -d '{"model":"mock-model","stream":true,"messages":[{"role":"user","content":"邮箱 user@example.com 手机 13812345678 结束"}]}'`
  4. 期望：首字节立即到达；所有 `delta.content` 拼接为 `邮箱 user@example.com 手机 13812345678 结束`；包含 `"finish_reason":"stop"` 块与 `data: [DONE]`；`/tmp/cpahost/upstream.log` 最新一行只含占位符。
  5. 对照：同请求去掉敏感信息、`stream:false` 各一次，结果与修复前相同。
