# 流式还原裸载荷修复计划

> 日期：2026-10-11。状态：**全部完成，用户真实宿主验收通过（2026-10-11）**。规格：`.kiro/specs/stream-restore-bare-chunks/`（bugfix.md / design.md / tasks.md）。

## 1. 问题

映射非空时，宿主 CPA v8.0.22 逐事件把**裸载荷**（OpenAI/Gemini 裸 JSON、Codex 单行 `event:`/`data:`）交给 `response.intercept_stream_chunk`，插件按 `\n` 分行的 `RestoreSSEChunk` 把无换行块整体缓冲并返回 `DropChunk=true`。客户端只收到宿主自写的 `data: [DONE]`；经 Cloudflare 表现为 524。本机真实宿主 + mock 上游已复现（2026-10-11）。此外 Claude 等整帧路径中，跨事件切分的占位符以残片输出。

## 2. 决策

| 编号 | 决策 |
|---|---|
| SR-1 | 一进一出、永不 `DropChunk`；未变化返回空 Body（宿主沿用原块），变化时只做定点字节替换 |
| SR-2 | 按载荷形态分派：裸 JSON / SSE 文本（逐块按行，不跨块缓冲行）/ opaque 原样 |
| SR-3 | 在解码后的**文本字段**上还原与暂扣；按文本流键（choice / content block / output 内容 / candidate）携带 ≤33 字节残留前缀 |
| SR-4 | 范围：可见文本、推理文本、拒答文本、工具调用参数（与非流式 `RestoreJSON` 一致）；其他字符串只还原完整占位符 |
| SR-5 | 结束信号投递残留：OpenAI/Gemini 注入结束事件字段；Claude/Responses 在结束帧（或 Codex 的 `event:` 行）前合成增量帧 |
| SR-6 | 暂扣仅在"协议 × 形态"可投递时启用，否则只还原完整占位符 |
| SR-7 | 删除 `RestoreSSEChunk` 与 `lineBuf`（宿主从不跨块切行）；`RestoreChunk`/`Flush`/`Reset`/`Pending` 字节级契约保留 |
| SR-8 | 流中断时残留在 `Flush` 清空并计入 `Leftover`（SDK 无终块通道，TD-CPA-032 残留） |
| SR-9 | 领域逻辑全在 `redact`（新文件按协议拆分），`plugin.go` 只接线，`abi.go` 不改 |

## 3. 与 request-trace-log 的协调

本修复拥有 `redact/stream.go` 与 `InterceptStreamChunk`，并定义 `StreamStats`（Chunks / Events / Restored / Unknown / Carried / MaxPending / Flushed / Synthesized / Passthrough / Leftover）与 `Stats()`。request-trace-log 的 `stream.summary` 直接读取该结构；其 `Chunks` 含义改为 `RestorePayload` 调用次数；`FlushWithResult` 由该规格在本修复之后基于 `Leftover` 实现。

## 4. 已知限制（已登记技术债）

缺陷本身登记为 TD-CPA-045（fixed）；TD-CPA-032 补充中断残留计入 `Leftover`。

| 限制 | 技术债 |
|---|---|
| 工具参数中原值含引号时按纯文本替换、不做 JSON 转义（与非流式一致） | TD-CPA-046 |
| Responses 合成增量帧复用模板的 `sequence_number` | TD-CPA-047 |
| Gemini `alt` 非 SSE 模式（JSON 数组片段）只做完整占位符还原 | TD-CPA-048 |
| OpenAI 结束块目标字段存在但非字符串（如 `"content":null`）时残留无法注入，计入 `Leftover` | TD-CPA-049 |
| `editStrings` 失败（嵌套 >64）时事件原样透传，已执行的暂扣不回滚 | TD-CPA-050 |

任务 2 另发现旧 `appendSSELine` 对未改动的 `data:` 行丢失 `data: ` 前缀，随 `RestoreSSEChunk` 删除一并消除（记入 TD-CPA-045 证据）。

## 5. 进度

| 项 | 状态 |
|---|---|
| bugfix.md / design.md / tasks.md | 已完成（2026-10-11） |
| 任务 1 探索测试（`TestStreamHost*`，未修复代码上失败） | 已完成（2026-10-11） |
| 任务 2 保留性测试（`TestStreamPreserve*`） | 已完成（2026-10-11） |
| 任务 3.1–3.15 修复与文档同步 | 已完成（2026-10-11） |
| 任务 3.16 真实宿主 E2E（`/tmp/cpahost`，端口 18317） | 已完成（2026-10-11，本机自建宿主，非用户生产环境）：CPA v8.0.22 源码自建（cgo）+ mock 上游（7 字符 `delta.content` 分片回显）；`make build` 后替换 `.so`（sha256 `c657d5e4…f789` 一致），`kill` 旧 PID 后重启，管理接口 `privacygateway` `registered/effective_enabled=true`。① 流式含邮箱/手机：HTTP 200，10 个 data 块 JSON 全部合法，`delta.content` 拼接 = `邮箱 user@example.com 手机 13812345678 结束`，含 `"finish_reason":"stop"` 块与 `data: [DONE]`；上游日志仅见 `[[PG_306f02ef535568d2_1]]`/`_2`，原文命中 0。② 流式无敏感：内容原样，stop/[DONE] 齐全。③ 非流式含敏感：响应 `content` 已还原，上游仅见 `[[PG_ebea1127eb00c1d9_1]]`/`_2`。全部通过 |
| 任务 4 检查点 | 已完成（2026-10-11）：`make check` 全绿（覆盖率整体 88.48% / redact 92.11%），`make smoke` 通过，非测试 .go 文件均 ≤500 行（`abi.go` 499）；根目录 `privacygateway-v0.1.0.so` 已从 zip 刷新（sha256 `c657d5e4…f789` 一致，zip `e0cc706c…a3c6`，`checksums.txt` 校验通过） |
| 用户真实宿主验收 | 用户真实 CPA 宿主验收通过（2026-10-11，用户执行）：逐字符脱敏测试 5 项敏感值均以占位符到达上游、对照组原样；流式与非流式请求、还原测试均通过（不再 524） |
