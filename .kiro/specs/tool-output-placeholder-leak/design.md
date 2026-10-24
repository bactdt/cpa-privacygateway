# tool-output-placeholder-leak Bugfix Design

## Overview

工具输出里的普通标识符（UUID 形式的会话标识、带扩展名的文件名、进程 id）被高熵检测当成密钥替换成占位符；模型随后在工具参数里写回占位符，部分流式协议又没能把被切开的占位符拼回还原，客户端执行工具时拿到 `[[PG_...]]` 字面量。排查中还发现两处漏检：`.env` 里的短密码，以及 Responses 格式 `function_call_output.output` 整段不扫描。

修复分请求方向和响应方向两部分。

**请求方向（脱敏）**

1. **检测顺序固定**：固定凭据规则 / `custom_rules` / PII → 敏感上下文 → 安全 ID 条件豁免 → 通用高熵检测。豁免只用来过滤高熵候选，前面几类检测各自独立产出检出结果，所以豁免在结构上不可能覆盖它们。
2. **新增敏感上下文检测**：`KEY=value` 等写法中，键名含敏感词时值长度 ≥ 8 就脱敏，不看熵。
3. **严格的豁免条件**：只豁免 UUID、"主体 + 常见扩展名"的文件名、内置安全键名或用户显式配置的 id。形态（"单词 + 分隔符 + 随机后缀"）本身不是豁免依据。
4. **补扫 Responses 工具输出**：只扫 `type` 为 `function_call_output` / `custom_tool_call_output` 的条目里的 `output`，兼容 `type` 写在 `output` 之后的键顺序。
5. **记录过期占位符**：请求正文里 nonce 不属于本请求的占位符计入 trace。

**响应方向（还原）**

6. **补齐暂存**：openai SSE 复用注入投递；新增 interactions 轻量规划器；codex 复用 Responses 规划器；antigravity 复用 Gemini 规划器（多一层 `response`）。openai-response 裸 JSON 不新增暂存。
7. **无法修复时放行并分类记录**：原事件原样输出，trace 按"不支持的事件类型 / 缺少合成模板 / 结束时残留"三类计数，附 `SourceFormat` 和掩码样例。

领域逻辑全部落在 `redact`（纯 Go + 标准库），`plugin.go` / `config.go` / `trace_hooks.go` 只做配置与接线。

## Glossary

- **Bug_Condition (C)**：见下文 C1–C7 的并集。
- **Property (P)**：普通标识符在可信条件下原样发往上游；凭据、敏感上下文值、Responses 工具输出里的敏感值被脱敏；流式响应中被切开的本请求占位符被拼回还原，无法拼回时有分类记录。
- **Preservation**：固定凭据 / PII / `custom_rules` / `allowlist` / 阈值配置行为、已支持协议的流式还原、非流式还原、映射生命周期、结构字段跳过、`fail_closed`、trace 不含原值，均不变。
- **高熵候选**：`tokenPattern`（`\b[A-Za-z0-9_\-]{16,}\b`）匹配、长度 ≥ `MinEntropyLen`、含数字、熵 ≥ `MinEntropy` 的片段（`redact/detect.go` `appendEntropy`）。
- **豁免区间（exempt range）**：本字段文本中被判定为安全标识符的字节区间；完全落在豁免区间内的高熵候选被丢弃。
- **键值写法扫描器（kv scanner）**：从文本中找出 `KEY=value`、`export KEY=value`、`"key": "value"`、`key: value`、`key = value` 的键与值区间，供敏感上下文检测和安全键名豁免共用。
- **敏感键名**：按 `_`、`-`、`.` 和驼峰边界切词并转大写后，含 `SECRET`、`TOKEN`、`PASSWORD`、`PASSWD`、`PWD`、`PRIVATE`、`CREDENTIAL`、`APIKEY` 中任一词，或含相邻词对 `API KEY`、`ACCESS KEY`；整个键名为 `PWD` 或 `OLDPWD` 时除外。
- **过期占位符（stale）**：匹配 `placeholderRe`、但 nonce 不等于当前映射 nonce 的占位符。
- **残片（dangling prefix）**：文本末尾可能是本请求占位符开头、但未闭合的部分（`holdbackLen` > 0）。

## Bug Details

### Bug Condition

```
FUNCTION isBugCondition(X)
  INPUT: X = (cfg, format, requestBody, mapping, responseChunks[])
  OUTPUT: boolean

  // C1–C3：普通标识符被高熵误报（请求方向）
  C1 ← 可检测文本中存在 UUID 形态的高熵候选，且不在敏感上下文、未命中其他规则
  C2 ← 存在"主体 + 常见扩展名"中主体是高熵候选的文件名，同上
  C3 ← 存在内置安全键名 / safe_id_keys 的值或以 safe_id_prefixes 开头的高熵候选，同上
  // C4：敏感上下文中的低熵或短值未脱敏
  C4 ← 存在敏感键名 + 值（长度 ≥ 8、非纯数字、非布尔、非占位符），且值未被其他规则命中
  // C5：Responses 工具输出未扫描
  C5 ← format ∈ {openai-response, codex} 且 input[] 中存在 type ∈ {function_call_output,
        custom_tool_call_output} 的条目，其 output 字符串含任一检测器可命中的值
  // C6：过期占位符无记录
  C6 ← requestBody 中存在 nonce ≠ mapping.nonce 的占位符
  // C7：流式跨片段占位符未拼回（响应方向）
  C7 ← mapping 非空，且某本请求占位符被切到同一文本流相邻事件，且 (format, shape) ∈
        {(openai, SSE), (interactions, *), (codex, SSE), (antigravity, bare)}
        —— 这些组合修复后可拼回；其他无法暂存的组合（如 openai-response 裸 JSON）
        属于 2.8 的"记录并放行"

  RETURN C1 ∨ C2 ∨ C3 ∨ C4 ∨ C5 ∨ C6 ∨ C7
END FUNCTION
```

### 宿主协议核实（CPA v8.0.22）

`$H=/home/gh/go/pkg/mod/github.com/router-for-me/!c!l!i!proxy!a!p!i/v8@v8.0.22`

| SourceFormat | 何时出现 | 流块形态 | 依据 |
|---|---|---|---|
| `openai` / `openai-response` / `claude` / `gemini` | 普通入口，`HandlerType()` 同时作为 entry 与 exit 协议 | 见 stream-restore-bare-chunks 设计表 | `handlers_stream.go:281`、`handlers_execution.go:44` |
| `interactions` | `POST /v1beta/interactions`，entry = exit = `interactions` | 完整 SSE 帧 `event: step.delta\ndata: {...}\n\n` | `sdk/api/handlers/gemini/interactions_handlers.go:73-90,134-200`；翻译器用 `SSEEventData`（`internal/translator/common/bytes.go`） |
| `codex` / `antigravity` | 仅插件执行器通过 `ExitProtocol` 指定时（`model_execution.go:249-254`） | codex 与 Responses 同构（SSE）；antigravity 为 Gemini 结构外包 `response` | `internal/translator/antigravity/gemini/antigravity_gemini_response.go:34,86` |

interactions 事件结构（`internal/translator/antigravity/interactions/interactions_antigravity_response.go:181-309`）：

- `step.delta`：`delta.type` 为 `text` 时文本在 `delta.text`；为 `thought_summary` 时在 `delta.content.text`；为 `arguments_delta` 时工具参数在 `delta.arguments`（字符串）。
- `step.stop`：带 `index`，结束该 step 的全部文本流。
- `interaction.completed`、`done`（`[DONE]` 载荷）：结束全部文本流。

### Examples

| 输入 | 修复前 | 修复后 |
|---|---|---|
| 工具输出含 UUID 形态会话标识 | 被替换为占位符 | 原样 |
| `backup-<时间戳与随机串>.tar.gz` | 主体被替换 | 原样 |
| `https://hooks.slack.com/services/T0/B0/<24 位随机>` | 末段被替换 | 不变（仍替换，路径片段不豁免） |
| `process_id: proc_<26 位随机>` | 被替换 | 原样（内置安全键名） |
| `Started process proc_<26 位随机>`，未配置 | 被替换 | 不变（仍替换） |
| 同上，`safe_id_prefixes: [proc_]` | 被替换 | 原样 |
| `STRIPE_KEY=sk_live_<随机>` | 被替换 | 不变（仍替换，固定规则 `sk-` 不命中下划线时由敏感上下文与高熵命中） |
| `glpat-<20 位随机>` 出现在纯文本 | 被替换 | 不变（仍替换，无通用形态豁免） |
| `HEROKU_API_KEY=<uuid>` | 被替换 | 不变（仍替换，敏感上下文优先于 UUID 豁免） |
| `DB_PASSWORD=Summer2024abc` | 原样（漏检） | 被替换 |
| `TOKEN_TTL=3600`、`ENABLE_TOKEN=true` | 原样 | 原样 |
| `PWD=/home/gh/project` | 原样 | 原样 |
| Responses `{"output":"key sk-...","type":"function_call_output"}` | 原样（漏检） | `sk-` 值被替换 |
| openai SSE 中占位符切成 `[[PG_ab` + `cd..._3]]` | 客户端拿到占位符字面量 | 拼回并还原 |

## Expected Behavior

### Preservation Requirements

**Unchanged Behaviors（对应 bugfix.md 3.1–3.14）：**
- 固定凭据规则、PII、`custom_rules` 命中的值仍被替换，包括恰好长成 UUID / 文件名 / 安全 id 形态的情况（3.1、3.2、3.4）。
- 不满足豁免条件的高熵串仍被替换，包括无扩展名的路径片段和 `sk_live_…`、`glpat-…`、`hf_…`、`npm_…`（3.3）。
- `allowlist`、`min_entropy`、`min_entropy_len`、关闭 `entropy` 的语义不变（3.4）。
- 已支持暂存的组合（openai/gemini 裸 JSON、claude/openai-response SSE）一进一出、不丢块（3.5）。
- 非流式还原、未知 nonce / 序号不猜测还原（3.6）；映射复用与生命周期（3.7）；结构字段与 `redact_tool_arguments=false` 跳过（3.8）；`fail_closed`（3.9）；trace 只记掩码与计数（3.10）。
- 非敏感键名的键值写法、纯数字 / 布尔 / 短值不因上下文脱敏（3.11、3.12）。
- 其他位置名为 `output` 的字段不新增扫描（3.13）；不做跨消息关联（3.14）。

**Scope:** 不满足 isBugCondition 的输入，脱敏结果与还原输出应与修复前逐字节相同。唯一的已知例外：新增的敏感上下文检测本身就是 2.7 要求的新行为，满足 C4 的值会从"原样"变为"被替换"。

## Hypothesized Root Cause

1. **高熵检测只看字符统计**：`appendEntropy`（`redact/detect.go:184-207`）对 `tokenPattern` 候选只判长度、含数字、熵，没有任何上下文或形态信息（C1–C3）。
2. **没有键值语义**：检测器把字段文本当作无结构字符串，`DB_PASSWORD=Summer2024abc` 的值长度 13 < 24，固定规则也不命中（C4）。
3. **扫描范围按键名决定**：`RedactJSON` 只扫 `textKeys` 中的键（`redact/walk.go:30-42,101-107`），`output` 不在其中（C5）。
4. **只统计本 nonce 的未知占位符**：`RestoreCount` / `RestoreCounted` 对其他 nonce 的占位符不计数（`redact/mapping.go:209-228`、`redact/restore_stats.go:15-37`），请求方向则完全不看占位符（C6）。
5. **暂存白名单不全**：`carryEnabled`（`redact/stream_payload.go:49-57`）只放行 4 个组合，`planEvent`（`:315-327`）不认识 interactions / codex / antigravity，未规划事件只做完整占位符还原（C7）。
6. **投递失败只计字节**：`countDelivery` 与 `Flush` 都把失败计入 `Leftover`（字节数），无法区分原因，也没有格式和样例。

## Correctness Properties

Property 1: Bug Condition - 普通标识符在可信条件下原样发往上游

_For any_ 字段文本 T，若 T 中某高熵候选完全落在豁免区间内（UUID；主体 + 常见扩展名的文件名主体；内置安全键名、`safe_id_keys` 键名的值；以 `safe_id_prefixes` 开头的 token），且该候选不与任何固定凭据 / PII / `custom_rules` / 敏感上下文检出区间重叠，修复后的 `Config.Detect` SHALL 不为它产出 `KindEntropy` 检出。

**Validates: Requirements 2.1, 2.2, 2.3**

Property 2: Bug Condition - 敏感上下文值被脱敏

_For any_ 字段文本 T 中由键值写法扫描器识别出的 (key, value)，若 key 是敏感键名、value 去引号后长度 ≥ 8 rune、不是纯数字、不是布尔值、不是占位符，修复后的 `Config.Detect` SHALL 产出覆盖 value 区间的 `KindSecretContext` 检出，且该检出不受任何豁免影响。

**Validates: Requirements 2.7**

Property 3: Bug Condition - Responses 工具输出被扫描

_For any_ format ∈ {openai-response, codex} 的请求体，`input[i]` 为对象且其 `type` ∈ {`function_call_output`, `custom_tool_call_output`}、`output` 为字符串时（不论 `type` 与 `output` 的键顺序），修复后的 `RedactJSON` 对该 `output` 的替换结果 SHALL 等于 `Mapping.Redact(output)`；其他位置名为 `output` 的字段 SHALL 不被改写。

**Validates: Requirements 2.6, 3.13**

Property 4: Bug Condition - 过期占位符可观测

_For any_ 请求体，修复后 `RedactJSON` 在 `OnStale` 非 nil 时 SHALL 对每个被扫描字段中 nonce ≠ 当前 nonce 的占位符回调一次；回调参数只含掩码样例与路径，不含原值；`OnStale` 为 nil 时输出与修复前逐字节相同。

**Validates: Requirements 2.5, 3.10**

Property 5: Bug Condition - 新增组合的跨事件占位符拼回

_For any_ (format, shape) ∈ {(openai, SSE), (interactions, SSE), (codex, SSE), (antigravity, bare)}，把含若干本请求占位符的原文按任意长度切成 N 个该协议事件并以结束信号收尾，修复后每个输入块恰好产出一个输出块、`DropChunk=false`、每个 `data:` 载荷 `json.Valid`，且每个文本流的输出拼接等于 `Mapping.Restore(原文)`。

**Validates: Requirements 2.4**

Property 6: Bug Condition - 无法修复时放行并分类记录

_For any_ 无法拼回的残片（未规划的组合或事件、无合成模板、请求结束时残留），修复后的输出事件 SHALL 与"只做完整占位符还原"的结果相同（不合成错误帧），且 `StreamStats` 的 `UnsupportedEvent` / `MissingTemplate` / `LeftoverAtEnd` 中恰好对应的一项增加，样例至多 5 个且只含掩码后的残片。

**Validates: Requirements 2.8**

Property 7: Preservation - 非缺陷输入行为不变

_For any_ 不满足 isBugCondition 的输入，修复后的 `RedactJSON`、`RestoreJSON(Counted)`、`RestorePayload`、`Flush`、`Reset`、`Pending` 输出 SHALL 与修复前逐字节相同；固定凭据 / PII / `custom_rules` 检出集合 SHALL 不变；配置 `entropy: false` 时 SHALL 不产出 `KindEntropy`。

**Validates: Requirements 3.1–3.14**

Property 8: Preservation - 配置校验拒绝危险前缀

_For any_ `safe_id_prefixes` 条目 p，若 `len(p) < 3`、或 p 不以 `_`/`-` 结尾、或 p 与已知密钥前缀表中任一项在不区分大小写时互为前缀，`loadConfig` SHALL 返回错误；`safe_id_keys` 中含敏感键名的条目 SHALL 同样被拒绝。

**Validates: Requirements 2.9**

## Fix Implementation

### Changes Required

**1. 新文件 `redact/detect_context.go`：键值扫描与敏感键名**

- `kvPattern`（RE2，无回溯）：
  ```
  (?:^|[\s{,;(\[])(?:export\s+)?["']?([A-Za-z_][A-Za-z0-9_.\-]*)["']?[ \t]*[:=][ \t]*("(?:[^"\\\n]|\\.)*"|'[^'\n]*'|[^\s,;}\]'"]+)
  ```
  第 1 组为键，第 2 组为值（含引号时值区间取引号内）。只匹配单行，不跨换行。
- `type kvMatch struct{ key string; valStart, valEnd int }`；`scanKV(text string) []kvMatch`。同一文本只扫描一次，结果同时供 2、3 使用。
- `keyWords(key string) []string`：按 `_`、`-`、`.` 和驼峰边界（小写后接大写）切词，转大写。
- `isSensitiveKey(key string) bool`：整个键名（大写）为 `PWD` 或 `OLDPWD` 时返回 false；否则任一词 ∈ {`SECRET`,`TOKEN`,`PASSWORD`,`PASSWD`,`PWD`,`PRIVATE`,`CREDENTIAL`,`APIKEY`}，或相邻词对 ∈ {`API KEY`,`ACCESS KEY`} 时返回 true。按"词"比对，因此 `tokenizer`、`input_tokens`、`max_tokens` 不会命中。
- `sensitiveValueOK(value string) bool`：rune 长度 ≥ 8；不是纯数字；不是 `true/false/yes/no`（不区分大小写）；不匹配 `placeholderRe`。
- `isSafeIDKey(key string, extra []string) bool`：键名不区分大小写、去掉 `_`/`-` 后等于 `processid`、`pid`、`traceid`、`requestid`，或等于 `extra` 中某项（同样归一化）；且 `!isSensitiveKey(key)`。

**2. `redact/detect.go`：新检测器与豁免过滤**

- 新增 `KindSecretContext Kind = "secret_context"`（满足 `validKindName` `^[a-z_]{1,16}$`，`MaskPreview` 走默认分支输出 `[secret_context]`）。
- `Config` 新增 `SecretContext bool`（默认 true）、`SafeIDKeys []string`、`SafeIDPrefixes []string`。
- `Detect` 中在 PII 之后、高熵之前：
  ```
  kvs := scanKV(text)                      // 仅在 SecretContext 或需要豁免时计算
  if c.SecretContext:
      for kv in kvs: if isSensitiveKey(kv.key) && sensitiveValueOK(value):
          append span{kv.valStart, kv.valEnd, KindSecretContext}
  if c.Entropy:
      exempt := exemptRanges(text, kvs, c)
      out = appendEntropy(text, base, c, exempt, out)
  ```
- `exemptRanges` 返回区间列表：
  - `uuidPattern` = `\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`。
  - 文件名主体：对每个 `tokenPattern` 候选，若紧随其后是 `.` + 常见扩展名（不区分大小写：`tar.gz`、`tar.bz2`、`tar.xz`、`tar`、`tgz`、`zip`、`gz`、`7z`、`log`、`txt`、`json`、`yaml`、`yml`、`csv`、`sql`、`db`、`sqlite`、`bak`、`md`、`html`、`png`、`jpg`、`pdf`），且扩展名后是文本结尾或非 `[A-Za-z0-9_-]` 字符，则候选区间豁免。扩展名表是包级常量，不开放配置。
  - 安全键名：`kvs` 中 `isSafeIDKey(kv.key, c.SafeIDKeys)` 的值区间。
  - 安全前缀：`tokenPattern` 候选以 `c.SafeIDPrefixes` 中某项开头（区分大小写）。
- `appendEntropy` 新增 `exempt` 参数：候选完全落在某个豁免区间内时跳过。不改变阈值逻辑。
- 优先级如何保证：固定规则、`custom_rules`、PII、敏感上下文检出都在豁免之前独立 append，`exempt` 只作用于高熵候选；`resolveSpans` 对重叠区间保留起点更早、更长的检出，所以 `HEROKU_API_KEY=<uuid>` 由 `KindSecretContext` 覆盖。无需修改 `resolveSpans`。

**3. `config.go`：新配置与校验**

- `detectorConfig` 新增 `SecretContext *bool \`yaml:"secret_context"\``（默认开，与其他检测器开关一致）。
- `config` 新增 `SafeIDKeys []string \`yaml:"safe_id_keys"\``、`SafeIDPrefixes []string \`yaml:"safe_id_prefixes"\``，默认空，去空白后传入 `redact.Config`。
- 校验（`loadConfig` 现有校验段之后）：
  - 前缀：`len < 3`、不以 `_` / `-` 结尾，或与 `knownSecretPrefixes` 在不区分大小写时互为前缀 → 报错。`knownSecretPrefixes` = `sk`、`rk`、`pk`、`gh`、`github_pat`、`glpat`、`hf`、`npm`、`xox`、`akia`、`asia`、`aiza`、`eyj`，放在 `redact` 包导出，供校验与测试共用。
  - 键名：`redact.IsSensitiveKey(key)` 为真 → 报错（例如误填 `api_token`）。
- 错误信息只含条目序号和原因，不回显条目内容之外的配置。

**4. `redact/walk.go`：Responses 工具输出与过期占位符**

- `WalkOptions` 新增 `OnStale func(path []string, sample string)`。
- `RedactJSON` 中 `scanToolOutput := format == formatResponses || format == "codex"`，通过 `transformJSON` 新参数传入。
- `walkValue` 对象分支：当 `scanToolOutput` 为真时，遇到键 `output` 且值为字符串，不立即调用 `visit`，而是暂存（字符串值、字面量偏移、路径）；同时记录本对象 `type` 的字符串值。对象结束时：
  - `type` ∈ {`function_call_output`, `custom_tool_call_output`} → 以 `forceText=true`（复用现有 `arrayText` 参数语义）调用 `visit`。
  - 否则以原参数调用 `visit`，与修复前行为一致。
  - 替换列表在 `applyReplacements` 中按偏移排序，延后调用不影响结果。
- `RestoreJSON` / `RestoreJSONCounted` 不传 `scanToolOutput`，路径完全不变。
- 过期占位符：`RedactJSON` 的 visitor 在决定扫描某字段后（toolArg 或文本键或 forceText），若 `OnStale != nil`，用 `placeholderRe.FindAllString` 找出 nonce ≠ `m.Nonce()` 的占位符逐个回调。样例掩码为 `[[PG_<nonce 前 4 位>…_<seq>]]`。`OnStale == nil` 时不做任何额外工作。

**5. 流式还原：`redact/stream_payload.go` 及新规划器**

- 新增常量 `formatInteractions = "interactions"`、`formatCodex = "codex"`、`formatAntigravity = "antigravity"`。
- `carryEnabled` 改为：

  | format | 暂存的形态 | 投递方式 |
  |---|---|---|
  | openai | bare、SSE | 注入结束事件（`injectOpenAI`） |
  | gemini、antigravity | bare | 注入结束事件（`injectGemini`，antigravity 带 `response` 前缀路径） |
  | claude | SSE | 合成帧 |
  | openai-response、codex | SSE | 合成帧（含单行 `event:` 投递） |
  | interactions | SSE | 合成帧 |
  | 其他 | 无 | 记录并放行 |

- `planEvent`：`codex` → `planResponses`；`antigravity` → 若事件顶层有 `response` 对象，则在其内部做 `planGemini`，并给 targets 的路径加 `response` 前缀；否则直接 `planGemini`；`interactions` → `planInteractions`。
- `deliverEnded` 的分派按"投递方式"而不是按 format 名，避免新增协议时漏改：openai 注入、gemini/antigravity 注入、其余合成帧。`deliverByEventName` 对 codex 同样生效（`restoreSSE` 中 `format == formatResponses` 的判断改为 `isResponsesLike(format)`）。
- 新文件 `redact/stream_interactions.go`：
  - `planInteractions(event)`：`event_type == "step.delta"` 时，按 `delta.type` 选目标路径：`text` → `delta.text`，`thought_summary` → `delta.content.text`，`arguments_delta` → `delta.arguments`（仅当为 JSON 字符串时）；键为 `s<index>/text|thought|args`。`step.stop` → `endPrefixes "s<index>/"`；`interaction.completed`、`event_type == "done"` → `endAll`。
  - 合成帧：把 `synthClaude` / `synthResponses` 的公共部分抽成 `synthSSE(tpl, carry, defaultEvent string)`，interactions 默认事件名 `step.delta`。合成帧插在结束事件所在帧之前（现有 `insertFrames` 机制）。
- `restoreSSE` 中遇到 `data: [DONE]` 这类非 JSON 行时，若当前 format 的结束语义是"全部结束"（interactions 的 `done`），在该帧前投递全部残留；复用 `deliverByEventName` 的思路，新增 `deliverAllBefore`。

**6. 无法修复时的分类记录：`redact/stream_text.go`、`stream_payload.go`**

- `StreamStats` 新增：
  ```go
  UnsupportedEvent int      // 无暂存能力的组合或未规划事件中出现残片
  MissingTemplate  int      // 结束信号到达但无模板/无法注入，残留丢失
  LeftoverAtEnd    int      // Flush 时仍有残留的文本流数
  DanglingSamples  []string // 至多 5 个掩码残片
  ```
  `Leftover`（字节数）保留，语义不变。`Stats()` 返回时复制 `DanglingSamples` 切片。
- 计数位置：
  - `restoreEvent` 中 `carry == false` 的分支：对每个字符串值在完整还原后调用 `holdbackLen(next, nonce)`，> 0 时 `UnsupportedEvent++` 并记样例。
  - `countDelivery(ok=false)`：`MissingTemplate++` 并记样例。
  - `Flush`：每个仍有残留的键 `LeftoverAtEnd++` 并记样例。
- 样例掩码：残片只含 `[[PG_`、nonce 和数字，不含原值；仍按 `[[PG_<nonce 前 4 位>…` 截断，并保证不超过 16 rune。
- 无法修复时事件输出就是"只做完整占位符还原"的结果，不注入、不合成，保证 SSE 结构不被破坏（Property 6）。

**7. 接线：`plugin.go`、`trace_hooks.go`**

- `redactObservedJSON`：追踪开启时设置 `opts.OnStale`，聚合到 `findingAgg` 新字段 `stale int`、`staleSamples []string`（至多 5 个）；`redactDone` 输出 `stale`、`stale_samples`。
- `InterceptStreamChunk` / `HandleRequestComplete`：`streamSummary` 和 `streamFlush` 增加 `source_format`、`unsupported_event`、`missing_template`、`leftover_at_end`、`dangling_samples` 字段。`source_format` 取 `req.SourceFormat`；`HandleRequestComplete` 没有 SourceFormat，从请求状态表（`state.useFormat` 记录的格式）读取。
- trace 写入失败不影响返回值（现有 `traceLogger` 语义）。

**8. 文档**

- `docs/design-docs/redaction-algorithm.md`：补充检测顺序、豁免条件、敏感上下文规则和 Responses `output` 扫描范围。
- `config.example.yaml`（若存在）或 README 配置段：补 `detectors.secret_context`、`safe_id_keys`、`safe_id_prefixes` 及前缀校验规则说明。

### 不做的事

- 不按工具来源限定豁免，不做跨消息关联（决策 C、F）。
- 不内置 `session_id`、`proc_` 等猜测出来的安全键名或前缀。
- 不为 openai-response 裸 JSON 新增暂存（宿主正常路径不产生该组合，`*.done` / `response.completed` 中的完整文本已做完整还原）。
- 不新增固定凭据规则（`sk_live_`、`glpat-` 等靠"无通用豁免 + 高熵 + 敏感上下文"覆盖）。

## Testing Strategy

### Validation Approach

两阶段：先在未修复代码上写探索测试，确认 C1–C7 能复现、根因判断正确；再实现修复，用同一批测试验证缺陷消失，并用保持性测试确认其余行为不变。

### Exploratory Bug Condition Checking

**Goal**：在修复前复现缺陷，确认或推翻根因假设。

**Test Cases**（预期在未修复代码上失败）：
1. UUID 会话标识被 `KindEntropy` 命中（C1）。
2. `<随机主体>.tar.gz` 主体被命中（C2）。
3. `process_id: proc_<随机>` 被命中（C3）。
4. `DB_PASSWORD=Summer2024abc` 未被命中（C4）。
5. Responses 请求 `{"output":"... sk-...","type":"function_call_output"}` 中 `sk-` 未被替换，键顺序反过来同样未被替换（C5）。
6. 请求体含其他 nonce 占位符时没有任何可观测输出（C6）。
7. openai SSE、interactions SSE、codex SSE、antigravity bare 中切开的占位符原样输出残片（C7）。

**Expected Counterexamples**：1–3 出现 `KindEntropy` 检出；4、5 检出为空；6 无回调；7 客户端拼接文本含 `[[PG_` 字面量。

### Fix Checking

```
FOR ALL X WHERE isBugCondition(X) DO
  result := pipeline_fixed(X)
  ASSERT Property 1–6 对应断言成立
END FOR
```

### Preservation Checking

```
FOR ALL X WHERE NOT isBugCondition(X) DO
  ASSERT pipeline_original(X) = pipeline_fixed(X)
END FOR
```

保持性测试优先用属性测试：随机生成文本（混入固定凭据、PII、普通高熵串、无扩展名路径片段、`sk_live_` / `glpat-` / `hf_` / `npm_` 形态），断言固定规则与 PII 的检出集合在修复前后相同、不满足豁免条件的高熵候选仍被检出。修复前的行为通过在测试中保留一份"旧检测函数"的调用结果（以 `Entropy` 豁免全部关闭、`SecretContext=false` 的配置模拟）作为对照。

### Unit Tests

- `detect_context_test.go`：`keyWords` 切词；`isSensitiveKey` 覆盖 `DB_PASSWORD`、`apiKey`、`ACCESS_KEY_ID`、`client-secret`、`TOKEN_TTL`（敏感）、`tokenizer`、`input_tokens`、`PWD`、`OLDPWD`（非敏感）、`DB_PWD`（敏感）；`sensitiveValueOK` 边界（7/8 rune、纯数字、布尔、占位符）；`scanKV` 的五种写法、引号内转义、单行限制。
- `detect_test.go`：Examples 表逐行；`safe_id_prefixes` 区分大小写；豁免区间与 `custom_rules` / `allowlist` 的交互；`entropy: false` 不产出高熵检出。
- `walk_test.go`：`function_call_output` / `custom_tool_call_output` 两种键顺序；`metadata.output`、`message` 条目里的 `output` 不被改写；非 Responses 格式不扫描 `output`；`RestoreJSON` 输出不变；`OnStale` 计数与掩码，nil 时逐字节相同。
- `stream_interactions_test.go`：text / thought_summary / arguments_delta 切片拼回；`step.stop`、`interaction.completed`、`done` 结束投递；合成帧以 `\n\n` 结尾。
- `stream_payload_test.go`：openai SSE、codex SSE（含单行 `event:` 模式）、antigravity bare 拼回；openai-response bare、未知 format 计 `UnsupportedEvent` 且输出等于完整还原结果；删除模板后结束计 `MissingTemplate`；`Flush` 计 `LeftoverAtEnd`；样例至多 5 个且不含 nonce 第 5 位之后的字符。
- `config_test.go`：前缀与键名校验的通过 / 拒绝用例；默认值。
- `trace_hooks` 相关测试：`redact.done` 带 `stale`；`stream.summary` / `stream.flush` 带新字段与 `source_format`。

### Property-Based Tests

- 扩展 `stream_property_test.go`：把 Property 5 的生成器扩到四个新组合，随机切分长度 1–40 字节。
- 新增检测属性测试：Property 1、2、7（固定规则 / PII 检出集合不变）。
- 扩展 `fuzz_test.go`：`FuzzDetect` 覆盖 `scanKV` 与豁免（不 panic、检出区间合法、不越界）；`FuzzRedactJSON` 覆盖 `scanToolOutput` 路径（输出仍是合法 JSON）。

### Integration Tests

- 插件级：Responses 请求 → `InterceptRequest` 脱敏 `function_call_output.output` → 模拟上游流在工具参数中切开占位符 → `InterceptStreamChunk` 拼回 → `HandleRequestComplete`；断言客户端拼接得到原值、trace 中计数正确且不含原值。
- 插件级：interactions 流同上。
- 真实宿主验收（手动，记录到 `docs/exec-plans`）：在 CPA 上复现原始场景（Bash `session_id` 配置为 `safe_id_keys`、ManagedProcess 前缀配置为 `safe_id_prefixes`），确认 ProcessWait 能拿到真实 id。
