# 工具输出占位符泄漏修复计划

> 日期：2026-10-24。状态：**全部完成（代码、测试、文档、本机真实宿主 E2E、`make check`）**。规格：[`tool-output-placeholder-leak`](../../../.kiro/specs/tool-output-placeholder-leak/)（bugfix.md / design.md / tasks.md）。

## 1. 问题

1. Responses 工具输出中的普通标识符（UUID 会话 id、`.tar.gz` 文件名主体、`process_id` 值）满足高熵规则，被替换为占位符，破坏工具结果语义。
2. `DB_PASSWORD=...` 等敏感键值不在固定凭据正则覆盖内，未脱敏。
3. 跨块占位符在 openai SSE、Codex 完整帧、Antigravity 裸 JSON、Interactions 中未暂存或未在结束帧投递，残片泄漏到客户端。
4. 旧 nonce 占位符没有 trace 计数，难以定位跨请求污染。

## 2. 决策

| 编号 | 决策 |
|---|---|
| TL-1 | 检测顺序加入敏感上下文与高熵豁免；豁免只过滤高熵候选，不影响固定凭据/PII/敏感上下文 |
| TL-2 | 高熵豁免：UUID 整串、常见扩展名文件名主体、内置安全 id 键值、`safe_id_keys`/`safe_id_prefixes` |
| TL-3 | 敏感键名按 `_`/`-`/`.`/驼峰切词，值需 ≥8 rune、非纯数字/布尔/完整占位符；`PWD`/`OLDPWD` 整键名除外 |
| TL-4 | Responses/Codex 仅补扫 `input[i]` 直接元素中 `function_call_output`/`custom_tool_call_output` 的 `output`，`type` 顺序无关 |
| TL-5 | 流式暂存组合：openai=bare+SSE；gemini/antigravity=bare；claude/openai-response/codex/interactions=SSE |
| TL-6 | 结束信号前投递残留：注入结束事件字段或合成 SSE 增量帧；Interactions 的 `step.stop` 只结束同 `index` 流，全局结束投递全部残留 |
| TL-7 | `StreamStats` 增加 `UnsupportedEvent`/`MissingTemplate`/`LeftoverAtEnd`/`DanglingSamples`；trace 输出 `source_format`、`stale`/`stale_samples` |
| TL-8 | 领域逻辑留在 `redact`；`plugin.go` 只接线，`abi.go` 不改；每个文件 ≤500 行 |

## 3. 配置

| 字段 | 默认 | 语义 |
|---|---|---|
| `detectors.secret_context` | true | 敏感键名上下文检测 |
| `safe_id_keys` | 空 | 额外安全 id 键名（忽略 `_`/`-` 与大小写）；敏感键名永不豁免 |
| `safe_id_prefixes` | 空 | 额外安全 id 前缀；至少 3 字符、以 `_`/`-` 结尾、不得与已知密钥前缀互为前缀 |

## 4. 已知限制（TD-CPA-052 登记）

- 纯文本 id 需显式配置 `safe_id_prefixes`，不按工具来源自动限定。
- `openai-response` 裸 JSON 不暂存，残片计入 `UnsupportedEvent`。
- 完整键名 `PWD` 可能漏掉真实密码。
- `Flush` 仍无终块投递通道（TD-CPA-032 残留）。

## 5. 进度

| 项 | 状态 |
|---|---|
| 任务 1 缺陷探索测试（`TestBugCond*`，修复前失败） | 已完成（2026-10-24） |
| 任务 2 保留性测试（`TestPreserve*`） | 已完成（2026-10-24） |
| 任务 3.1–3.7 检测/遍历/流式修复 | 已完成（2026-10-24） |
| 任务 3.8 trace 接线（`source_format`、`stale`、残留统计） | 已完成（2026-10-24） |
| 任务 3.9–3.11 探索/保留/属性/fuzz 验证 | 已完成（2026-10-24）：`FuzzDetect`、`FuzzRedactJSON` 各 30s 无失败 |
| 任务 3.12 文档同步 | 已完成（2026-10-24） |
| 任务 3.13 真实宿主 E2E（`/tmp/cpahost`） | 已完成（2026-10-24，本机自建宿主，非用户生产环境）：`/tmp/cpahost` CPA v8.0.22 + mock 上游 18999，替换 `plugins/linux/amd64/privacygateway-v0.1.7.so`（sha256 `5bff8b43…6700`，与 `build/libprivacygateway.so` 一致），`kill` 旧 PID 后重启，日志确认 plugin loaded/registered。① Claude `tool_result` 含 UUID 与 `.tar.gz` 文件名 → 上游原值，`DB_PASSWORD=...` → `[[PG_9dd689ba172aec64_1]]`；② Responses `function_call_output.output` 的 `sk-...` → `[[PG_f41b256161a9588e_1]]`；③ `safe_id_prefixes:["proc_"]` 时 `proc_...` 上游原值，移除配置重启后 → `[[PG_7e21764ddce6ebbb_1]]`；④ openai 流式 7 字符分片拼接 = `contact user@example.com now`；⑤ trace API/页面 200，`redact.done.stale=1`、`stale_samples=["[[PG_0000…_1]]"]`，`stream.summary`/`stream.flush` 均含 `source_format=openai`、`unsupported_event`、`missing_template`、`leftover_at_end`、`dangling_samples` |
| 任务 4 `make check` 检查点 | 已完成（2026-10-24）：fmt-check/vet/build/lint/test/race/coverage/docs-check 全绿；整体 89.55%、redact 92.21%；`abi.go` 440 行未变，全部文件 ≤500 行 |
