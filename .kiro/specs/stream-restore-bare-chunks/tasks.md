# Implementation Plan

> 所有命令在 `/project/web/cpa` 下执行，先 `export PATH=/home/gh/.local/toolchains/go/bin:$PATH`。
> 约束：单文件 ≤ 500 行；`abi.go` 不得增长；`main` 包禁止 `fmt.Print*`/`log.Print*`；`redact` 不得导入宿主 SDK；只用标准库测试工具。每个任务只改列出的文件。

- [x] 1. Write bug condition exploration test
  - **Property 1: Bug Condition** - 宿主形态流块不丢弃且跨事件占位符完整还原
  - **CRITICAL**: This test MUST FAIL on unfixed code - failure confirms the bug exists
  - **DO NOT attempt to fix the test or the code when it fails**
  - **NOTE**: This test encodes the expected behavior - it will validate the fix when it passes after implementation
  - **GOAL**: Surface counterexamples that demonstrate the bug exists
  - **Scoped PBT Approach**: 缺陷是确定性的，先固定具体反例（mock 同款 7 字符切片），再用固定种子 `math/rand` 生成 200 组随机切分
  - 新建 `plugin_stream_host_test.go`（只改此文件）：
    - 辅助函数 `redactedText(t, p, reqID, text)`：调用 `InterceptRequestBeforeAuth`（`SourceFormat:"openai"`，正文 `{"messages":[{"role":"user","content":<text>}]}`）并从返回 Body 解出占位符化文本
    - 辅助函数 `openAIChunks(text, sizes)` 生成 `{"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":<piece>},"finish_reason":null}]}`，末尾追加 `{"id":"x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`（无 `data:`、无 `\n`，即宿主形态）
    - 辅助函数 `effective(resp, in)`：`len(resp.Body)>0` 取 `resp.Body` 否则取 `in`
    - `TestStreamHostOpenAIBareChunks`：原文 `邮箱 user@example.com 手机 13812345678 结束`，7 字符切片；断言每块 `DropChunk==false`、每个有效输出 `json.Valid`、块数不变、`delta.content` 拼接 == 原文、最后一块含 `"finish_reason":"stop"`
    - `TestStreamHostOpenAIRandomSplitProperty`：`rand.New(rand.NewSource(1))`，200 次，切片长度 1..12，原文随机混入两个敏感值与字符 `[`；同样断言（isBugCondition：映射非空且块无 `\n`）
    - `TestStreamHostCodexSingleLines`：`SourceFormat:"openai-response"`，块序列为独立的 `event: response.output_text.delta` / `data: {"type":"response.output_text.delta","item_id":"m1","output_index":0,"content_index":0,"delta":<piece>}` 交替，最后 `event: response.output_text.done` 与 `data: {"type":"response.output_text.done",...,"text":<全文占位符化>}`；断言无 drop、`data:` 行 JSON 合法、delta 拼接 == 原文
    - `TestStreamHostClaudeSplitAcrossEvents`：`SourceFormat:"claude"`，块为完整帧 `event: content_block_delta\ndata: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":<piece>}}\n\n`，占位符被切到两帧，最后 `content_block_stop` 帧；断言 text 拼接 == 原文
  - 运行：`go test ./ -run 'TestStreamHost' -count=1`
  - **EXPECTED OUTCOME**: Test FAILS（证明缺陷存在）
  - 在任务备注中记录反例（预期：OpenAI/Codex 首块即 `DropChunk=true` 且 Body 为空；Claude 输出含 `[[PG_` 残片）
  - Mark task complete when test is written, run, and failure is documented
  - _Requirements: 1.1, 1.2, 1.3, 1.5, 2.1, 2.2, 2.3, 2.5_

- [x] 2. Write preservation property tests (BEFORE implementing fix)
  - **Property 2: Preservation** - 非缺陷输入的输出与修复前一致
  - **IMPORTANT**: Follow observation-first methodology
  - 新建 `plugin_stream_preserve_test.go`（只改此文件；只经 `InterceptStreamChunk` / `InterceptResponse` 公共入口，不调用 `RestoreSSEChunk`）：
    - 先在未修复代码上运行并观察，再把观察到的输出固化为断言
    - `TestStreamPreserveEmptyMapping`：请求不含敏感信息（映射为空），固定种子随机生成裸 JSON、`data: {...}\n\n` 帧、`[DONE]`、`: keep-alive\n\n` 共 200 个块；断言响应 `Body` 为空且 `DropChunk==false`
    - `TestStreamPreserveSwitches`：插件禁用、`ChunkIndex: pluginapi.StreamChunkHeaderInitIndex`、空 Body 三种情况响应为零值
    - `TestStreamPreserveCompleteFrame`：映射非空，块为 `": keep-alive\ndata: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"key: <完整占位符>\"}}]}\n\n"`；断言有效输出含原值、含 `: keep-alive\n` 行、`data:` 载荷 `json.Valid`、解码后 content == `key: <原值>`
    - `TestStreamPreserveDoneFrame`：映射非空，块 `data: [DONE]\n\n` 的有效输出与输入逐字节相同
    - `TestStreamPreserveNonStreamResponse`：`InterceptResponse` 对含占位符的非流式 JSON 输出与 `redact.RestoreJSON` 一致
  - 运行：`go test ./ -run 'TestStreamPreserve' -count=1` 与 `go test ./redact -run 'TestRestoreStream(CrossChunk|UnknownTokenPreserved|EscapedToken|ErrorHandling|MultipleCallsStateful|SSEFormat|Flush|MaxLengthPlaceholderBuffered)$' -count=1`
  - **EXPECTED OUTCOME**: Tests PASS（确认要保留的基线行为）
  - Mark task complete when tests are written, run, and passing on unfixed code
  - _Requirements: 3.1, 3.2, 3.3, 3.4, 3.6, 3.7, 3.9_

- [x] 3. Fix for 流式还原丢弃宿主裸载荷块并无法还原跨事件占位符

  - [x] 3.1 新增 `Mapping.RestoreCount`
    - 只改 `redact/mapping.go`、`redact/mapping_test.go`
    - `RestoreCount(text string) (string, int, int)`：同 `Restore` 的正则替换，返回已还原个数与"本 nonce 但不在映射中"个数；持 `m.mu`；`Restore` 不变
    - 测试 `TestMappingRestoreCount`：命中、未知序号、其他 nonce、空映射
    - 验收：`go test ./redact -run TestMappingRestoreCount -count=1`；`wc -l redact/mapping.go` ≤ 500
    - _Bug_Condition: isBugCondition(X) from design（统计支撑）_
    - _Expected_Behavior: expectedBehavior 中 Mapping.Restore 一致性_
    - _Preservation: Restore/RestoreEscaped/RestoreEscapedBytes 不变_
    - _Requirements: 2.4, 3.3_

  - [x] 3.2 新增 JSON 定点编辑原语 `redact/jsonedit.go`
    - 只改 `redact/jsonedit.go`、`redact/jsonedit_test.go`
    - 实现 design 第 2、3 条：`editStrings`、`locateContainer`、`insertAt`、`encodeTextLiteral`（`SetEscapeHTML(false)`）；复用 `walk.go` 的 `offsetDecoder`、`replacement`、`applyReplacements`，不修改 `walk.go`
    - 路径段以 `"\x00"` 连接成字符串供匹配（数组下标为十进制）
    - 测试：未改动时返回同一切片（`&out[0]==&in[0]` 或 `bytes.Equal` 且 changed=false）、只替换目标字面量、`<>&` 不转义、空对象/空数组/非空容器的插入偏移与逗号、深度 > 64 返回错误、非法 JSON 返回错误
    - 验收：`go test ./redact -run TestJSONEdit -count=1`
    - _Requirements: 2.1, 2.4, 3.5_

  - [x] 3.3 新增文本流残留引擎 `redact/stream_text.go`
    - 只改 `redact/stream_text.go`、`redact/stream_text_test.go`
    - 定义 `StreamStats`（字段见 design "与 request-trace-log 规格的协调"）、`streamTemplate`、`textCarry`（`carry map[string]string`、`templates`、`stats`）
    - 实现 `restoreText(key, value, final)`、`takeCarry(keys []string, prefix string, all bool) map[string]string`、`pendingBytes()`、`reset()`；holdback 复用 `stream.go` 的 `placeholderTail` 逻辑（抽成以 nonce 为参数的包级函数时只改 `stream_text.go`，`stream.go` 原方法保留）
    - 不自带锁：调用方（`StreamRestorer`）持锁
    - 测试：完整占位符、占位符切成 2..5 段、33 字节最长占位符、`final=true` 清空、其他 nonce 不暂扣、`[` 结尾暂扣 1 字节、中文文本切分 UTF-8 合法、`MaxPending`/`Carried`/`Restored` 计数
    - 验收：`go test ./redact -run TestTextCarry -count=1`
    - _Bug_Condition: C2（占位符跨事件）_
    - _Expected_Behavior: 暂扣 ≤ 33 字节、拼接文本 = Restore(原文)_
    - _Requirements: 2.4, 2.5, 2.6, 3.3_

  - [x] 3.4 OpenAI chat 规划器与注入 `redact/stream_openai.go`
    - 只改 `redact/stream_openai.go`、`redact/stream_openai_test.go`
    - 定义共享类型 `textTarget`、`eventPlan`（若 3.5–3.7 也需要，放在本文件并在后续任务复用）
    - `planOpenAI(event)`：目标 `choices[i].delta.{content,reasoning_content,reasoning,refusal}` 与 `choices[i].delta.tool_calls[j].function.arguments`；键用 `choices[i].index` 与 `tool_calls[j].index`（不是数组位置）；`finish_reason` 非空 → 该 choice 结束
    - `injectOpenAI(event, key, carry)`：向 `choices[i].delta` 注入字段；工具参数在已有 `tool_calls` 数组开头插入 `{"index":k,"function":{"arguments":carry}}` 或注入整个 `tool_calls`；缺 `delta` 时注入 `delta` 对象
    - 测试：多 choice、工具参数按 index 分键、`delta:{}` 结束块注入、结束块已含 content 时不注入、`choices:[]` usage 块无目标
    - 验收：`go test ./redact -run TestStreamOpenAI -count=1`
    - _Requirements: 2.1, 2.5, 2.6_

  - [x] 3.5 Claude 规划器与合成帧 `redact/stream_claude.go`
    - 只改 `redact/stream_claude.go`、`redact/stream_claude_test.go`
    - `planClaude(event)`：`content_block_delta` 的 `delta.text`/`delta.thinking`/`delta.partial_json` → `b<index>`；`content_block_stop` → `b<index>` 结束；`message_delta`/`message_stop` → 全部结束
    - `synthClaude(tpl, carry)`：`event: content_block_delta\ndata: <模板替换文本>\n\n`
    - 测试：三种 delta 类型、stop 合成帧 JSON 合法且 index 正确、message_stop 全部投递
    - 验收：`go test ./redact -run TestStreamClaude -count=1`
    - _Requirements: 2.1, 2.5, 2.6_

  - [x] 3.6 Gemini 规划器与注入 `redact/stream_gemini.go`
    - 只改 `redact/stream_gemini.go`、`redact/stream_gemini_test.go`
    - `planGemini(event)`：`candidates[i].content.parts[j].text` → `g<index>/text`（`thought:true` → `g<index>/thought`；无 `index` 字段用数组位置）；`finishReason` 非空 → 该 candidate 结束；`functionCall.args` 不作为目标（走完整还原）
    - `injectGemini(event, key, carry)`：在 `parts` 开头插入 `{"text":carry}`（thought 加 `"thought":true`）；缺 `parts` 注入 `"parts":[...]`；缺 `content` 注入 `"content":{"role":"model","parts":[...]}`
    - 验收：`go test ./redact -run TestStreamGemini -count=1`
    - _Requirements: 2.1, 2.5, 2.6_

  - [x] 3.7 Responses 规划器与合成帧 `redact/stream_responses.go`
    - 只改 `redact/stream_responses.go`、`redact/stream_responses_test.go`
    - `planResponses(event)`：design 表中六类 `*.delta` 的 `delta` 字段 → 键 `o<output_index>/c<content_index|summary_index>/<kind>`（函数参数 `o<output_index>/args`）；对应 `*.done` 结束同 kind；`response.content_part.done`/`output_item.done`/`completed`/`incomplete`/`failed` 全部结束
    - `responsesEndByEventName(name)`：按 `event:` 行名给出结束 kind / 全部
    - `synthResponses(tpl, carry)`：`event: <模板 type>\ndata: <模板替换 delta>\n\n`
    - 测试：output_text 与 function_call_arguments 分键、done 触发、事件名触发、`response.completed` 中完整文本走完整还原
    - 验收：`go test ./redact -run TestStreamResponses -count=1`
    - _Requirements: 2.1, 2.3, 2.5, 2.6_

  - [x] 3.8 `StreamRestorer` 增加文本流状态与统计 `redact/stream.go`
    - 只改 `redact/stream.go`、`redact/stream_test.go`
    - `StreamRestorer` 增加 `text textCarry` 字段（`NewStreamRestorer` 初始化）；`Reset` 同时清空残留；`Flush` 同时清空残留并把残留字节计入 `stats.Leftover`；`Pending` 返回 `len(pending)+text.pendingBytes()`；新增 `Stats() StreamStats`（持锁返回副本）
    - 本任务**不删除** `RestoreSSEChunk`（`plugin.go` 仍在使用，删除放到 3.11）
    - 新增测试 `TestStreamRestorerStatsAndReset`；现有 `TestRestoreStream*` 不得修改
    - 验收：`go test ./redact -count=1 -race`；`wc -l redact/stream.go` ≤ 500
    - _Preservation: 3.9, 3.10_
    - _Requirements: 2.7, 3.9, 3.10_

  - [x] 3.9 载荷形态分派与入口 `RestorePayload` `redact/stream_payload.go`
    - 只改 `redact/stream_payload.go`、`redact/stream_payload_test.go`
    - 实现 design 第 6 条：形态判定、SSE 行处理（保留 `\r\n`、`data:` 前缀与分隔空格、注释/其他字段原样、合成帧插入帧起点、`openai-response` 事件名触发）、`restoreEvent`（规划 → `editStrings` → 投递 → 更新模板）、协议/形态不匹配时不暂扣、解析失败透传、`changed` 判定
    - 以 `func (s *StreamRestorer) RestorePayload` 实现，持 `s.mu`，使用 3.1–3.8 的函数与字段
    - 测试：三形态分派、Codex 单行 `event:` 名触发合成帧、Claude 帧合成帧位置、`[DONE]`/注释透传、映射为空返回原切片
    - 验收：`go test ./redact -run TestRestorePayload -count=1`
    - _Bug_Condition: C1 ∪ C2_
    - _Expected_Behavior: expectedBehavior(X, outs) from design_
    - _Preservation: 3.1, 3.4, 3.5, 3.6_
    - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.5, 2.6, 3.1, 3.4, 3.5, 3.6_

  - [x] 3.10 插件接线 `plugin.go`
    - 只改 `plugin.go`、`plugin_test.go`
    - `InterceptStreamChunk` 改为 `body, changed := restorer.RestorePayload(req.SourceFormat, req.Body)`；`!changed` 返回空响应，否则 `Body: body`；删除 `DropChunk` 分支
    - `HandleRequestComplete` 保持 `Flush()`；不新增日志、不改 `abi.go`
    - 改写 `TestPluginInterceptStreamChunk` 为宿主形态（裸 JSON 两块跨占位符，断言均不 drop、拼接还原）
    - 验收：`go test ./ -count=1 -race`
    - _Bug_Condition: isBugCondition(X) from design_
    - _Expected_Behavior: expectedBehavior(X, outs) from design_
    - _Preservation: Preservation Requirements from design_
    - _Requirements: 1.1, 1.2, 2.1, 2.2, 3.1, 3.2_

  - [x] 3.11 删除旧行分帧路径并改写依赖旧语义的测试 `redact/stream.go`
    - 只改 `redact/stream.go`、`redact/stream_test.go`
    - 删除 `RestoreSSEChunk`、`appendSSELine`、`sseDataSeparator`、`sseDataPayload`、`lineBuf`、`sseDataPrefix`/`sseCommentMark`（若 3.9 已在 `stream_payload.go` 内自带等价实现）；先 `grep -rn RestoreSSEChunk --include=*.go .` 确认无引用
    - 改写：`TestRestoreStreamSSERestoresDataLinesOnly` → 调 `RestorePayload("openai", …)`，断言不变；`TestRestoreStreamSSEIncompleteLineHeldAcrossChunks` → 不完整行立即输出、不 drop；`TestRestoreStreamSSEPlaceholderSplitAcrossEventsNotMerged` → 同一文本流跨事件还原、不同 choice 不合并
    - 其余 `TestRestoreStream*` 不得修改
    - 验收：`go build ./... && go test ./... -count=1 -race`；`wc -l redact/*.go` 每个 ≤ 500
    - _Preservation: 3.9_
    - _Requirements: 1.3, 1.4, 2.3, 2.5_

  - [x] 3.12 性质测试与 fuzz 扩展
    - 只改 `redact/stream_property_test.go`（新）、`redact/fuzz_test.go`
    - 四协议随机原文 × 随机切分（固定种子，每协议 ≥ 200 次）：拼接文本 = `Mapping.Restore(原文)`、每个 JSON 合法、块数不变；无占位符事件 `changed=false`
    - `FuzzRestorePayload`：任意输入不 panic，原本合法的 `data:`/裸 JSON 载荷输出仍合法
    - 验收：`go test ./redact -run TestStreamProperty -count=1` 与 `go test ./redact -run '^$' -fuzz FuzzRestorePayload -fuzztime 20s`
    - _Requirements: 2.1, 2.4, 2.5, 3.5_

  - [x] 3.13 Verify bug condition exploration test now passes
    - **Property 1: Expected Behavior** - 宿主形态流块不丢弃且跨事件占位符完整还原
    - **IMPORTANT**: Re-run the SAME test from task 1 - do NOT write a new test
    - The test from task 1 encodes the expected behavior
    - When this test passes, it confirms the expected behavior is satisfied
    - 运行：`go test ./ -run 'TestStreamHost' -count=1`
    - **EXPECTED OUTCOME**: Test PASSES (confirms bug is fixed)
    - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.5, 2.6_

  - [x] 3.14 Verify preservation tests still pass
    - **Property 2: Preservation** - 非缺陷输入的输出与修复前一致
    - **IMPORTANT**: Re-run the SAME tests from task 2 - do NOT write new tests
    - 运行：`go test ./ -run 'TestStreamPreserve' -count=1` 与任务 2 中的 `./redact` 命令
    - **EXPECTED OUTCOME**: Tests PASS (confirms no regressions)
    - Confirm all tests still pass after fix (no regressions)
    - _Requirements: 3.1, 3.2, 3.3, 3.4, 3.6, 3.7, 3.9_

  - [x] 3.15 文档同步
    - 只改 `docs/design-docs/redaction-algorithm.md`、`docs/exec-plans/tech-debt-tracker.md`、`docs/QUALITY_SCORE.md`、`docs/exec-plans/active/stream-restore-fix.md`（以及 `ARCHITECTURE.md` 若列有 redact 文件清单）
    - 内容见 design 第 9 条；新技术债条目记录本缺陷、修复证据与测试名；TD-CPA-032 追加说明；登记三项已知限制
    - 验收：`go run ./scripts/check-docs`
    - _Requirements: 2.7_

  - [x] 3.16 真实宿主端到端验收
    - 不改源码；按 design "Integration Tests" 的 E2E 步骤执行（替换 `.so` → `pgrep -x cpa` 取 PID 后 `kill` → 在 `/tmp/cpahost/run` 下后台重启 `/tmp/cpahost/cpa -config /tmp/cpahost/run/config.yaml -local-model` → curl 流式请求）
    - 禁止 `pkill -f`（会匹配调用者 shell）
    - 期望：`delta.content` 拼接为 `邮箱 user@example.com 手机 13812345678 结束`，包含 `"finish_reason":"stop"` 块与 `data: [DONE]`；`/tmp/cpahost/upstream.log` 最新行只含占位符；无敏感信息流式与非流式对照结果不变
    - 把命令输出摘要写入 `docs/exec-plans/active/stream-restore-fix.md` 的进度表
    - _Requirements: 2.2_

- [x] 4. Checkpoint - Ensure all tests pass
  - 运行 `make check`（fmt-check + vet + build + lint + test + coverage + docs），全部通过
  - 确认 `abi.go` 仍为 499 行、所有文件 ≤ 500 行
  - Ensure all tests pass, ask the user if questions arise.
