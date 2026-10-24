# Implementation Plan

> 所有命令在 `/project/web/cpa` 下执行，先 `export PATH=/home/gh/.local/toolchains/go/bin:$PATH`。
> 约束：单文件 ≤ 500 行（`detect.go` 已 419 行、`plugin.go` 已 448 行，新增逻辑放新文件）；`abi.go` 不得增长；`main` 包禁止 `fmt.Print*`/`log.Print*`；`redact` 不得导入宿主 SDK；只用标准库测试工具。每个任务只改列出的文件。
> 测试数据约定：高熵样例在测试代码里用固定种子 `math/rand` 或字符串拼接生成，不在文档和测试源码里写可直接使用的真实令牌。

- [x] 1. Write bug condition exploration tests
  - **Property 1–5: Bug Condition**（design C1–C7）
  - **CRITICAL**: 这些测试必须在未修复代码上失败，失败即证明缺陷存在
  - **DO NOT attempt to fix the test or the code when it fails**
  - 新建 `redact/bugcond_test.go`（只改此文件）：
    - `TestBugCondUUIDNotEntropy`：工具输出文本含 UUID 形态会话标识，断言无 `KindEntropy` 检出（C1）
    - `TestBugCondFilenameBody`：`<28 位随机主体>.tar.gz`、`.tar`、`.log`，断言主体无检出（C2）
    - `TestBugCondSafeIDKey`：`process_id: proc_<26 位随机>`、`"requestId": "<随机>"`，断言值无检出（C3）
    - `TestBugCondSecretContext`：`DB_PASSWORD=Summer2024abc`、`export API_TOKEN=abc12345xyz`、`"client_secret": "<12 位低熵>"`，断言值被检出（C4）
    - `TestBugCondResponsesToolOutput`：`RedactJSON("openai-response", …)`，`input` 中 `{"type":"function_call_output","call_id":"c1","output":"key sk-<20 位>"}` 与 `type` 在 `output` 之后两种顺序，断言 `sk-` 值被替换（C5）
    - `TestBugCondStalePlaceholder`：请求体含另一 nonce 的占位符，`WalkOptions.OnStale` 被回调 1 次（C6；未修复时该字段不存在 → 以编译失败或断言失败记录）
  - 新建 `redact/bugcond_stream_test.go`（只改此文件）：
    - 对 (openai, SSE)、(interactions, SSE)、(codex, SSE 帧与单行两种)、(antigravity, bare) 四组，把含两个本请求占位符的原文按固定 7 字节切片构造事件并以结束信号收尾，经 `RestorePayload` 处理；断言每块一进一出、`data:` 载荷 `json.Valid`、文本流拼接 == `Mapping.Restore(原文)`（C7）
  - 运行：`go test ./redact -run 'TestBugCond' -count=1`
  - **EXPECTED OUTCOME**: 全部 FAIL
  - 把每个用例的反例（检出 Kind、实际拼接文本）记入 `tasks.meta.json` 的 `failingExample`
  - _Requirements: 1.1–1.7, 2.1–2.7_

- [x] 2. Write preservation property tests (BEFORE implementing fix)
  - **Property 7: Preservation** - 非缺陷输入行为不变
  - **IMPORTANT**: 先在未修复代码上运行并观察，再把观察到的输出固化为断言
  - 新建 `redact/preserve_test.go`（只改此文件）：
    - `TestPreserveCredentialAndPII`：固定种子生成 300 段文本，随机混入 `credentialRules` 各类样例、邮箱、手机号、合法身份证号、Luhn 银行卡号；记录未修复代码的检出集合（不含 `KindEntropy`）为基线，断言一致
    - `TestPreserveEntropyStillHits`：纯随机 token、无扩展名 URL 路径末段、`sk_live_` / `glpat-` / `hf_` / `npm_` 前缀 + 随机后缀，断言仍有检出（3.3）
    - `TestPreserveConfigSemantics`：`allowlist` 命中不替换、`custom_rules` 命中替换、`entropy:false` 无 `KindEntropy`、调高 `min_entropy_len` 后短串不命中（3.4）
    - `TestPreserveNonSensitiveKV`：`BUILD_ID=<随机>` 仍按高熵判断、`LOG_FILE=app.log` 原样、`TOKEN_TTL=3600` / `ENABLE_TOKEN=true` / 短值原样（3.11、3.12）
    - `TestPreserveOtherOutputFields`：Responses 请求中 `metadata.output`、`type:"message"` 条目里的 `output` 不被改写；`openai` / `claude` 格式下 `output` 键不扫描（3.13）
    - `TestPreserveRedactJSONBytes`：`OnFinding`、`OnStale` 均为 nil 时，200 个随机请求体的 `RedactJSON` 输出与基线逐字节相同
  - 运行：`go test ./redact -run 'TestPreserve' -count=1` 以及现有 `go test ./redact ./ -run 'TestStreamPreserve|TestStream|TestRestore' -count=1`
  - **EXPECTED OUTCOME**: 全部 PASS
  - _Requirements: 3.1–3.14_

- [ ] 3. Fix for 工具输出标识符误脱敏与占位符回流

  - [x] 3.1 键值扫描与敏感键名
    - 只改新文件 `redact/detect_context.go`、`redact/detect_context_test.go`
    - 实现 design 第 1 条：`kvPattern`、`scanKV`、`keyWords`、`isSensitiveKey`（导出包装 `IsSensitiveKey`）、`sensitiveValueOK`、`isSafeIDKey`、导出 `KnownSecretPrefixes`
    - 验收：
      - `keyWords("dbPassword")` == `[DB PASSWORD]`，`keyWords("ACCESS_KEY_ID")` == `[ACCESS KEY ID]`
      - 敏感：`DB_PASSWORD`、`apiKey`、`ACCESS_KEY_ID`、`client-secret`、`DB_PWD`、`TOKEN_TTL`；非敏感：`tokenizer`、`input_tokens`、`max_tokens`、`PWD`、`OLDPWD`、`LOG_FILE`
      - `sensitiveValueOK`：7 rune 否、8 rune 是、`12345678` 否、`TRUE` 否、完整占位符否
      - `scanKV` 覆盖五种写法；值含 `\"` 转义时区间正确；不跨行；返回区间均满足 `0 ≤ start ≤ end ≤ len(text)`
      - `go test ./redact -run 'TestDetectContext' -count=1` 通过；文件 ≤ 500 行
    - _Requirements: 2.7, 2.3_

  - [x] 3.2 豁免区间与检测顺序
    - 只改 `redact/detect.go`、新文件 `redact/detect_exempt.go`、`redact/detect_test.go`
    - 实现 design 第 2 条：`KindSecretContext`、`Config.SecretContext/SafeIDKeys/SafeIDPrefixes`、`DefaultConfig` 中 `SecretContext:true`、`exemptRanges`、`appendEntropy` 增加 `exempt` 参数
    - 验收：
      - design Examples 表中请求方向的 13 行（除最后一行流式）逐行通过
      - `HEROKU_API_KEY=<uuid>` 产出 `KindSecretContext`，不产出 `KindEntropy`
      - `custom_rules` 命中 UUID 时仍产出 `KindCustom`；`allowlist` 包含该 UUID 时不替换
      - `safe_id_prefixes:["proc_"]` 时 `Started process proc_<随机>` 无检出，`PROC_<随机>` 仍检出（区分大小写）
      - `SecretContext:false` 时不产出 `KindSecretContext`
      - `MaskPreview(KindSecretContext, …)` == `[secret_context]`
      - `detect.go` ≤ 500 行；`go test ./redact -run 'TestDetect' -count=1` 通过
    - _Requirements: 2.1, 2.2, 2.3, 2.7, 3.1–3.4_

  - [x] 3.3 配置项与校验
    - 只改 `config.go`、`config_test.go`、`config.example.yaml`
    - 实现 design 第 3 条：`detectors.secret_context`、`safe_id_keys`、`safe_id_prefixes`，传入 `redactConfig()`
    - 验收：
      - 默认配置下 `SecretContext==true`、两个列表为空
      - 拒绝：`"pr"`（过短）、`"proc"`（无分隔符结尾）、`"sk_"`、`"SK-"`、`"gh"`、`"glpat-"`、`"hf_"`、`"npm_"`、`"s"`（与 `sk` 互为前缀）；通过：`"proc_"`、`"bash-session-"`
      - `safe_id_keys` 拒绝 `api_token`、`client_secret`；通过 `session_id`
      - 错误信息含条目序号与原因，不含其他配置值
      - `config.example.yaml` 增加三个配置的注释说明
      - `go test ./ -run 'TestConfig' -count=1` 通过；`config.go` ≤ 500 行
    - _Requirements: 2.3, 2.9_

  - [x] 3.4 Responses 工具输出扫描与过期占位符
    - 只改 `redact/walk.go`、`redact/walk_test.go`
    - 实现 design 第 4 条：`scanToolOutput` 延后访问 `output`、`WalkOptions.OnStale`
    - 验收：
      - `openai-response` 与 `codex` 格式下，`function_call_output`、`custom_tool_call_output` 两种类型、`type` 在前和在后两种顺序，`output` 都被扫描
      - `type` 缺失、为 `message`、为 `function_call` 时 `output` 不被扫描
      - `RestoreJSON` / `RestoreJSONCounted` 对同一输入输出与修复前逐字节相同
      - `OnStale` 对其他 nonce 占位符每个回调一次，样例形如 `[[PG_<4 位>…_<seq>]]`；本 nonce 占位符不回调；`OnStale==nil` 时输出不变
      - `go test ./redact -run 'TestWalk|TestRedactJSON' -count=1` 通过；`walk.go` ≤ 500 行
    - _Requirements: 2.5, 2.6, 3.8, 3.13_

  - [x] 3.5 流式统计字段扩展
    - 只改 `redact/stream_text.go`、`redact/stream.go`、`redact/stream_text_test.go`
    - 实现 design 第 6 条中 `StreamStats` 新字段、`Flush` 的 `LeftoverAtEnd` 计数、样例掩码函数 `maskDangling`（至多 16 rune、至多 5 个）
    - 验收：
      - `Stats()` 返回的 `DanglingSamples` 修改后不影响内部状态
      - `Flush` 后 `LeftoverAtEnd` 等于仍有残留的文本流数，`Leftover` 字节数语义不变
      - 样例不含 nonce 第 5 位之后的字符
      - `go test ./redact -run 'TestStreamText|TestRestoreStream' -count=1` 通过
    - _Requirements: 2.8_

  - [x] 3.6 openai SSE、codex、antigravity 接入暂存
    - 只改 `redact/stream_payload.go`、`redact/stream_gemini.go`、`redact/stream_claude.go`、`redact/stream_responses.go`、`redact/stream_payload_test.go`
    - 实现 design 第 5 条：新增格式常量、`carryEnabled` 新表、`planEvent` 分派、antigravity 的 `response` 前缀路径、按投递方式分派 `deliverEnded`、`isResponsesLike`、`synthSSE` 公共函数；第 6 条的 `UnsupportedEvent` / `MissingTemplate` 计数
    - 验收：
      - openai SSE（`data: {...}\n\n` 帧）切开的占位符在 `finish_reason` 帧注入后拼回
      - codex 完整帧与单行模式均拼回，`event:` 行触发投递
      - antigravity 裸 JSON（外包 `response`）拼回，注入后 `json.Valid`
      - openai-response 裸 JSON、未知 format 中出现残片时 `UnsupportedEvent` +1，输出等于只做完整还原的结果
      - 删除模板后到达结束信号时 `MissingTemplate` +1，不产生合成帧
      - claude、openai-response SSE、openai/gemini 裸 JSON 的现有测试全部不改仍通过
      - 每个文件 ≤ 500 行；`go test ./redact -count=1` 通过
    - _Requirements: 2.4, 2.8, 3.5_

  - [x] 3.7 interactions 规划器
    - 只改新文件 `redact/stream_interactions.go`、`redact/stream_interactions_test.go`，以及 `redact/stream_payload.go` 中 `planEvent` / `carryEnabled` 的 interactions 分支和 `[DONE]` 行前投递 `deliverAllBefore`
    - 验收：
      - `step.delta` 的 `text`、`thought_summary`（`delta.content.text`）、`arguments_delta`（`delta.arguments` 为字符串时）三种增量切开后拼回
      - `step.stop` 只结束同 `index` 的文本流；`interaction.completed` 与 `event: done` / `data: [DONE]` 结束全部
      - 合成帧 `event: step.delta\ndata: {...}\n\n`，插在结束帧之前，`data:` 载荷 `json.Valid`
      - `delta.type` 为 `function_result`、`thought_signature` 时不作为目标，只做完整还原
      - `go test ./redact -run 'TestInteractions' -count=1` 通过
    - _Requirements: 2.4_

  - [x] 3.8 trace 接线
    - 只改 `plugin.go`、`trace_hooks.go`、`state.go`（仅新增只读 getter `format(requestID)`）、新文件 `plugin_trace_leak_test.go`
    - 实现 design 第 7 条
    - 验收：
      - `redact.done` 含 `stale`、`stale_samples`（至多 5 个）
      - `stream.summary`、`stream.flush` 含 `source_format`、`unsupported_event`、`missing_template`、`leftover_at_end`、`dangling_samples`
      - trace 关闭时不调用 `OnStale`、不计算样例
      - 测试遍历 trace 事件的全部字符串字段，断言不含任何原始敏感值
      - `plugin.go` ≤ 500 行（超出时把新接线移到 `trace_hooks.go`）；`abi.go` 行数不变
      - `go test ./ -count=1` 通过
    - _Requirements: 2.5, 2.8, 3.10_

  - [x] 3.9 Verify bug condition exploration tests now pass
    - **IMPORTANT**: 重跑任务 1 的同一批测试，不新写测试
    - 运行：`go test ./redact -run 'TestBugCond' -count=1`
    - **EXPECTED OUTCOME**: 全部 PASS
    - _Requirements: 2.1–2.7_

  - [x] 3.10 Verify preservation tests still pass
    - **IMPORTANT**: 重跑任务 2 的同一批测试，不新写测试
    - 运行：任务 2 中的全部命令
    - **EXPECTED OUTCOME**: 全部 PASS
    - _Requirements: 3.1–3.14_

  - [x] 3.11 属性测试与 fuzz 扩展
    - 只改 `redact/stream_property_test.go`、`redact/fuzz_test.go`、新文件 `redact/detect_property_test.go`
    - 验收：
      - Property 5：四个新组合，固定种子各 200 组，切片长度 1–40 字节
      - Property 1、2：固定种子各 300 组随机文本
      - `FuzzDetect` 覆盖 `scanKV` 与豁免；`FuzzRedactJSON` 覆盖 `scanToolOutput`；各跑 `-fuzztime=30s` 无失败
      - `go test ./redact -count=1` 通过
    - _Requirements: 2.1–2.4, 2.6, 2.7_

  - [x] 3.12 文档同步
    - 只改 `docs/design-docs/redaction-algorithm.md`、`docs/design-docs/abi-contract.md`（配置表）、`docs/exec-plans/tech-debt-tracker.md`、`docs/QUALITY_SCORE.md`，新建 `docs/exec-plans/active/tool-output-placeholder-leak.md`
    - 内容：检测顺序、豁免条件、敏感上下文规则、Responses `output` 扫描范围、新 trace 字段、三个新配置；新技术债条目 `TD-CPA-052` 记录本缺陷与测试名；登记已知限制（纯文本 id 需配置、不按工具来源限定、openai-response 裸 JSON 不暂存、`PWD` 规则可能漏掉以 `PWD` 为完整键名的真实密码）
    - 验收：`go run ./scripts/check-docs` 通过
    - _Requirements: 全部_

  - [x] 3.13 真实宿主端到端验收
    - 不改源码；在 `/tmp/cpahost` 测试宿主上替换 `.so`，用 `pgrep -x cpa` 取 PID 后 `kill` 再后台重启（禁止 `pkill -f`）
    - 场景与期望：
      - Claude 格式请求，`tool_result` 含 UUID 会话标识与 `.tar.gz` 文件名 → `/tmp/cpahost/upstream.log` 中二者为原值
      - 同一请求含 `DB_PASSWORD=<值>` → 上游只见占位符
      - Responses 格式 `function_call_output.output` 含 `sk-` 值 → 上游只见占位符
      - 配置 `safe_id_prefixes:["proc_"]` 后 `Started process proc_<随机>` → 上游见原值；去掉配置后 → 占位符
      - openai 流式响应在工具参数中切开占位符 → 客户端拼接得到原值
      - trace 页面可见 `stale`、`source_format`、三类 unsupported 计数
    - 把命令输出摘要写入 `docs/exec-plans/active/tool-output-placeholder-leak.md` 的进度表
    - _Requirements: 2.1–2.8_

- [x] 4. Checkpoint - Ensure all tests pass
  - 运行 `make check`（fmt-check + vet + build + lint + test + race + coverage + docs-check），全部通过
  - 确认 `abi.go` 行数不变、所有文件 ≤ 500 行、覆盖率不低于现有门槛
  - 确认工作区只有本 spec 列出的文件改动，无临时文件
  - Ensure all tests pass, ask the user if questions arise.
