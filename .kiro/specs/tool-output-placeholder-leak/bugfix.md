# Bugfix Requirements Document

## Introduction

用户通过 CPA + 本插件使用 AI agent 时，客户端回传给模型的工具输出（Claude 的 `tool_result`、OpenAI 的 `role: tool` 消息等）里出现了两类问题：

1. **过度脱敏**：tar 文件名、Bash 工具返回的 `session_id` 这类普通标识符被替换成 `[[PG_<nonce>_<seq>]]` 占位符。它们不是密码，也不是个人信息。
2. **占位符回流到工具调用**：模型只看到占位符，于是在下一次工具调用（如 `ProcessWait`）的参数里原样写出占位符。参数里的占位符没有被还原，客户端执行工具时拿到的是 `[[PG_...]]`，工具调用失败。用户只能改用"输出写文件 + 轮询读文件"的方式绕开。

用通俗的话说：插件的"高熵检测器"会把"足够长、带数字、字符足够杂乱"的字符串当作密钥。它的判断只看字符本身，看不出这是文件名还是 API Key。实测（按当前默认阈值：长度 ≥ 24、至少一个数字、香农熵 ≥ 3.5）以下样例全部被判为敏感值：

| 样例 | 被检出的片段 | 长度 | 熵 |
|---|---|---|---|
| `a3f1c2d4-5b6e-4f70-8a9b-0c1d2e3f4a5b`（UUID 形式的 session_id） | 整串 | 36 | 3.97 |
| `bash_session_7f3k9q2m1x8z` | 整串 | 25 | 4.24 |
| `workspace-snapshot-20261011-153045.tar.gz` | `workspace-snapshot-20261011-153045` | 34 | 4.14 |
| `node_modules_backup_20261011_1530.tar` | `node_modules_backup_20261011_1530` | 33 | 4.11 |
| `proc_8f3a2c9e1b7d4f60a5e2c3b1`（ManagedProcess 返回的进程 id，形态为推断） | 整串 | 29 | 4.21 |

**ManagedProcess 进程 id 的情况**：ManagedProcess 是客户端（agent）一侧的工具，本仓库里没有它的代码，所以它返回的 id 长什么样无法从仓库确认，上表那一行是按常见写法推断的样例。按插件当前规则推算：

- 如果 id 是"前缀 + 随机串"（如 `proc_…`、`mp_…`）、UUID，并且总长 ≥ 24，就会被高熵检测命中。例如 UUID 形式 36 位、熵约 4.0；`mp_` 加 26 位 ULID 风格串共 29 位、熵约 4.9，都会命中。
- 如果 id 是短串（< 24 位，例如 `proc_7f3k9q2m`），高熵检测不会命中。其他检测器也基本碰不到它：邮箱、私钥、各类固定前缀密钥的格式都对不上。只有一种例外：纯数字 id 恰好是 11 位且以 `13`–`19` 开头（会被当成手机号），或者是 13–19 位且通过 Luhn 校验（会被当成银行卡号）。
- JSON 里名叫 `id` 的字段本身是"结构字段"，插件会跳过不扫描。但工具输出是一段文本，放在 `content` / `text` 这类字段里。id 一旦写进这段文本（例如 `Started process proc_8f3a…`），插件就会把它当普通文字扫描，所以照样会被脱敏。

后果：模型拿到的进程 id 是占位符。之后模型用它回查或等待进程时（例如调用 `ProcessWait`），工具参数里写回的还是占位符。如果这个占位符没被还原（见下面列出的还原失败情况），客户端就找不到这个进程，调用失败。用户只好不用 ManagedProcess，改用 Bash 直接运行部署脚本（脚本自己已经转入后台，连接建立后就返回），绕开"拿 id 回查进程"这一步。

被替换之后，能否把模型写回的占位符还原，取决于响应走哪条还原路径。目前已知以下情况会还原失败，占位符原样到达客户端：

- 流式响应中，工具调用参数是分成多个小片段（增量）发出的，一个 33 字节的占位符很容易被切到两个片段里。只有部分"协议 × 块形态"组合（openai/gemini 的裸 JSON 块、claude/openai-response 的 SSE 块）会把被切开的占位符暂存起来，拼完整后再还原。`codex`、`antigravity`、`interactions` 格式，以及 openai 的 SSE 块、openai-response 的裸 JSON 块，都没有这种暂存，被切开的占位符只能以残片输出。
- 映射只在单个请求内有效，请求结束就清除，下一个请求会生成新的随机 nonce。如果上一轮的占位符没被还原、留在了客户端的对话历史里，下一轮请求再把它带上来时，插件既不会再次脱敏它（它已经长得像占位符），也无法还原它（旧映射已清除）。这时它就成了永久无法还原的字符串。

影响：agent 的多步工具调用链（启动进程 → 等待进程、打包 → 传输文件等）断掉。用户只能绕开工具的正常用法，例如不用 ManagedProcess，改用 Bash 直接运行已后台化的部署脚本。

排查中还发现了两个反方向的问题（该脱敏的没脱敏）：

- **短密码漏检**：`.env` 里常见的 `DB_PASSWORD=Summer2024abc` 这种值，长度不到 24 位、熵也不高，又不带 `sk-` 之类的固定前缀，所以高熵检测和固定凭据规则都不会命中，原值直接发给上游。
- **OpenAI Responses 工具输出整段不扫描**：Responses 格式的请求里，工具输出放在 `input` 数组中 `type` 为 `function_call_output` 的条目的 `output` 字段里，例如 `{"type":"function_call_output","call_id":"call_1","output":"..."}`。插件只扫描键名在"文本键名单"里的字段（`text`、`content`、`input` 等）。`output` 不在名单里，也不在"结构字段名单"里，所以这个字段既不扫描也不改写，里面就算有 `sk-` 密钥也会原样发给上游。

### 修复方向（已与用户确认）

以下决策已与用户确认，本文件即为决策记录：

- **A. 只在可验证的安全场景下跳过高熵检测**：UUID、"主体 + 常见扩展名"的文件名、满足可信条件的业务 id（见 2.3）跳过通用高熵检测。URL/路径片段不豁免（很多 webhook URL 把密钥放在路径里）。"单词 + 分隔符 + 随机后缀"这种形态不再有通用豁免，因为 Stripe 的 `sk_live_…`、GitLab 的 `glpat-…`、Hugging Face 的 `hf_…`、npm 的 `npm_…` 等真实令牌在字符串层面与业务 id 无法区分。检测优先级固定为：固定凭据规则 / `custom_rules` / PII → 敏感上下文（B）→ 安全 ID 条件豁免 → 通用高熵检测；豁免只能跳过通用高熵检测，不能覆盖前面任何一类检测。
- **B. 新增"敏感上下文"检测**：按文本的样子判断，不按文本从哪来判断。识别的写法有 `KEY=value`、`export KEY=value`、JSON 的 `"key": "value"`、YAML/ini 的 `key: value` 和 `key = value`。当左边的键名（不区分大小写）含有 `SECRET`、`TOKEN`、`PASSWORD`/`PASSWD`/`PWD`、`API_KEY`/`APIKEY`、`PRIVATE`、`CREDENTIAL`、`ACCESS_KEY` 等敏感词时，右边的值视为敏感：不看熵，长度 ≥ 8 就脱敏。纯数字和布尔值（`true`/`false`/`yes`/`no`）跳过，避免把 `TOKEN_TTL=3600` 这种配置项误判。这条规则对 A 里跳过高熵检测的形态同样生效，例如 `HEROKU_API_KEY=<uuid>` 必须脱敏。已知误伤风险：shell 环境变量 `PWD`/`OLDPWD` 表示当前目录（如 `PWD=/home/gh/project`），按"键名含 PWD"会被误判，设计阶段需决定 `PWD` 是只匹配完整词还是排除这类变量。
- **C. 不做跨消息关联（范围外）**：插件只看当前这段文本本身的内容，不会因为上一条消息里执行过 `cat .env` 就把下一条工具输出整体当作敏感。这样做的原因是跨消息推断来源需要理解对话语义，规则难以写准，容易同时产生漏检和误伤。
- **D. 补上 Responses 工具输出的扫描**：`function_call_output` 条目的 `output` 字段要和其他协议的工具输出（Claude 的 `tool_result`、OpenAI Chat 的 `role: tool` 消息内容）接受同样的检测。注意：插件目前只按"键名"决定扫不扫，如果直接把 `output` 加进文本键名单，所有叫 `output` 的字符串字段都会被扫描，例如用户自定义的 `metadata.output`，或者其他条目类型里的 `output`。所以设计阶段需要把扫描范围限定在 `type` 为 `function_call_output` 的条目上（是否包含 `custom_tool_call_output` 等同类条目也在设计阶段确定）。同一条目里 `type` 字段可能写在 `output` 字段之后，限定方式需要考虑这种键顺序。
- **E. 还原问题留在本次范围内**：流式跨片段占位符暂存覆盖不全（1.4/2.4）和过期占位符记录到 trace（1.5/2.5）都在本次修复内。
- **F. 不按工具来源限定豁免（范围外）**：插件拿到工具输出时只看到 `tool_use_id` / `tool_call_id`，不知道工具名；要知道工具名需回查同一请求里更早的工具调用，属于 C 排除的跨消息关联。因此 `session_id` 不内置为安全字段，由用户通过 `safe_id_keys` 显式配置。

## Bug Analysis

### Current Behavior (Defect)

1.1 WHEN 请求正文的可检测文本字段（如工具输出所在的 `content`/`text`）里有 UUID 形式的会话标识（例如 `a3f1c2d4-5b6e-4f70-8a9b-0c1d2e3f4a5b`），且没有其他凭据特征 THEN 系统把它当作高熵密钥，替换成 `[[PG_...]]` 占位符后发给上游模型

1.2 WHEN 请求正文的可检测文本字段里有带扩展名的文件名（例如 `workspace-snapshot-20261011-153045.tar.gz`），其主体部分长度 ≥ 24、含数字、熵 ≥ 3.5 THEN 系统把文件名主体替换成占位符，只留下 `.tar.gz` 这类扩展名，上游模型看不到真实文件名

1.3 WHEN 请求正文的可检测文本字段里有长度 ≥ 24、含数字、熵 ≥ 3.5 的进程或会话标识（例如 Bash 工具返回的会话标识、ManagedProcess 返回的 `proc_<随机串>` 形式进程 id） THEN 系统把它当作高熵密钥替换成占位符，模型之后无法用真实 id 回查或等待该进程。这类"单词 + 分隔符 + 随机后缀"的形态与 `sk_live_…`、`glpat-…` 等真实令牌在字符串层面无法区分，所以修复不能以形态本身作为豁免依据

1.4 WHEN 某个流式响应的协议格式和块形态组合不在暂存支持范围内（`codex`、`antigravity`、`interactions` 的任意块形态；`openai` 的 SSE 块；`openai-response` 的裸 JSON 块），且模型在工具调用参数里写出的占位符被切到相邻两个增量片段里 THEN 系统输出残缺的占位符片段，客户端拼出的工具参数里是 `[[PG_...]]` 字面量，不是原值

1.5 WHEN 上一轮请求的占位符没被还原、进入了客户端对话历史，并在下一轮请求里被带回（nonce 属于已清除的旧映射） THEN 系统既不脱敏也不还原它，把它原样发给上游；之后模型在响应里复述它时也原样返回客户端，并且不留下任何可观测的记录，用户无从得知占位符来自过期映射

1.6 WHEN OpenAI Responses 格式请求的 `input` 数组中有 `type` 为 `function_call_output` 的条目，且其 `output` 字符串里含有凭据或 PII（例如 `sk-` 密钥、邮箱） THEN 系统完全不扫描该字段，把原值发给上游模型

1.7 WHEN 可检测文本字段里有 `DB_PASSWORD=Summer2024abc`、`export API_TOKEN=abc12345xyz` 这类"敏感键名 + 值"的写法，且值的长度不足 24 位或熵低于阈值，也没有命中固定凭据规则 THEN 系统不脱敏该值，把原值发给上游模型

### Expected Behavior (Correct)

2.1 WHEN 请求正文的可检测文本字段里有 UUID 形式的会话标识，没有命中固定凭据规则、PII 规则或 `custom_rules`，也不处于 2.7 所述的敏感上下文 THEN 系统 SHALL 跳过对它的高熵检测，保持原值发往上游

2.2 WHEN 请求正文的可检测文本字段里有带常见文件扩展名（例如 `.tar`、`.tar.gz`、`.tgz`、`.zip`、`.log`、`.txt`、`.json`）的文件名，文件名主体没有命中固定凭据规则、PII 规则或 `custom_rules`，也不处于 2.7 所述的敏感上下文 THEN 系统 SHALL 跳过对文件名主体的高熵检测，保持原值发往上游；没有扩展名的 URL/路径片段（例如 webhook URL 路径末尾的 token） SHALL 照常做高熵检测

2.3 WHEN 请求正文的可检测文本字段里有满足以下任一可信条件的业务 id，且没有命中固定凭据规则、PII 规则或 `custom_rules`，也不处于 2.7 所述的敏感上下文 THEN 系统 SHALL 跳过对它的高熵检测，保持原值发往上游：(a) 它是内置安全键名 `process_id`、`pid`、`trace_id`、`request_id`（不区分大小写，兼容驼峰写法如 `processId`）以 `key=value`、`key: value` 或 `"key": "value"` 写法给出的值；(b) 它是用户配置项 `safe_id_keys`（默认空）中键名的值；(c) 它以用户配置项 `safe_id_prefixes`（默认空）中的某个前缀开头。不满足任一条件的 id SHALL 照常做高熵检测。因此 1.3 中出现在纯文本里、或使用非内置键名（如 `session_id`）的 id，只有在用户配置 `safe_id_keys` 或 `safe_id_prefixes` 后才保持原值

2.4 WHEN 流式响应采用任一支持的协议格式与块形态，且模型在工具调用参数或文本增量里写出的本请求占位符被切到相邻的增量片段里 THEN 系统 SHALL 把被切开的占位符暂存起来，拼完整后还原成原值再输出，客户端拼出的工具参数中 SHALL 不含本请求 nonce 的占位符；对确实无法暂存的组合，系统 SHALL 在追踪记录（trace）中记录该限制被触发的次数

2.5 WHEN 请求正文中有符合占位符格式、但 nonce 不属于当前请求映射的过期占位符 THEN 系统 SHALL 在追踪记录中记录"过期占位符"的数量（只记数量和掩码预览，不记原值），让用户能看出问题来自上一轮没被还原的占位符

2.6 WHEN OpenAI Responses 格式请求的 `input` 数组中有 `type` 为 `function_call_output` 的条目 THEN 系统 SHALL 用与其他工具输出相同的检测规则扫描其 `output` 字符串（固定凭据规则、PII 规则、`custom_rules`、高熵检测及 2.1–2.3 的跳过规则、2.7 的敏感上下文规则），命中的值替换成占位符后再发往上游；扫描范围 SHALL 限定在该类条目上，其他位置名为 `output` 的字段不因此被扫描

2.7 WHEN 可检测文本字段（包括 2.6 所述的 Responses 工具输出）里有 `KEY=value`、`export KEY=value`、`"key": "value"`、`key: value` 或 `key = value` 写法，左边键名（不区分大小写）含 `SECRET`、`TOKEN`、`PASSWORD`、`PASSWD`、`PWD`、`API_KEY`、`APIKEY`、`PRIVATE`、`CREDENTIAL`、`ACCESS_KEY` 等敏感词，右边的值长度 ≥ 8 且不是纯数字、不是布尔值（`true`/`false`/`yes`/`no`，不区分大小写），也不是占位符 THEN 系统 SHALL 不看熵，把该值替换成占位符；该规则 SHALL 对 2.1–2.3 中跳过高熵检测的形态同样生效（例如 `HEROKU_API_KEY=a3f1c2d4-5b6e-4f70-8a9b-0c1d2e3f4a5b` 中的 UUID SHALL 被脱敏）；判断 SHALL 只依据当前字段文本本身，不依据其他消息的内容（例如上一条消息里的 `cat .env` 命令）

2.8 WHEN 响应方向的流式还原遇到无法修复的被切开占位符（规划器不认识的事件类型、缺少合成模板、请求结束时仍有残留片段） THEN 系统 SHALL 原样输出该事件、不合成可能破坏 SSE 结构的错误事件，并在追踪记录中按"不支持的事件类型""缺少合成模板""结束时残留"三类分别计数，同时记录 `SourceFormat` 和至多 5 个掩码样例；该放行只涉及占位符还原，SHALL 不影响请求方向的完整扫描与脱敏

2.9 WHEN 用户配置的 `safe_id_prefixes` 条目长度不足 3、不以 `_` 或 `-` 结尾，或与已知密钥前缀（`sk`、`rk`、`pk`、`gh`、`github_pat`、`glpat`、`hf`、`npm`、`xox`、`AKIA`、`ASIA`、`AIza`、`eyJ`）在不区分大小写时互为前缀 THEN 系统 SHALL 在加载配置时报错拒绝该配置

### Unchanged Behavior (Regression Prevention)

3.1 WHEN 可检测文本字段（包括工具输出）里有命中固定凭据规则的值（`sk-` 密钥、GitHub token、AWS key ID、Google API key、Slack token、JWT、私钥块），包括这些值恰好长成 2.1–2.3 所述形态的情况 THEN 系统 SHALL CONTINUE TO 把它替换成占位符后再发给上游

3.2 WHEN 可检测文本字段里有邮箱、大陆手机号、通过校验的身份证号、通过 Luhn 校验的银行卡号，包括这些值出现在 2.1–2.3 所述形态内部的情况 THEN 系统 SHALL CONTINUE TO 把它替换成占位符

3.3 WHEN 可检测文本字段里有不满足 2.1–2.3 豁免条件的高熵随机串（长度 ≥ 24、含数字、熵 ≥ 3.5），包括纯随机 token、没有扩展名的 URL/路径片段、未配置为安全的"单词 + 分隔符 + 随机后缀"形态（例如 `sk_live_…`、`glpat-…`、`hf_…`、`npm_…`） THEN 系统 SHALL CONTINUE TO 用高熵检测把它替换成占位符

3.4 WHEN 用户配置了 `allowlist`、`custom_rules`、`min_entropy`、`min_entropy_len`，或关闭了 `entropy` 检测 THEN 系统 SHALL CONTINUE TO 按这些配置判断是否脱敏：`custom_rules` 命中的值 SHALL CONTINUE TO 被替换（包括 2.1–2.3 所述形态）；`allowlist` 中的值 SHALL CONTINUE TO 不被替换；`min_entropy`、`min_entropy_len` SHALL CONTINUE TO 决定非豁免形态的高熵阈值；关闭 `entropy` 时 SHALL CONTINUE TO 不做高熵检测

3.5 WHEN 流式响应属于当前已支持暂存的组合（openai/gemini 的裸 JSON 块、claude/openai-response 的 SSE 块） THEN 系统 SHALL CONTINUE TO 一进一出、不丢块地还原占位符，并在协议结束信号处投递暂存的残留

3.6 WHEN 非流式响应中包含本请求映射里的占位符（包括工具调用参数里的占位符） THEN 系统 SHALL CONTINUE TO 把它还原成原值；未知 nonce 或未知序号的占位符 SHALL CONTINUE TO 保持原样，不猜测还原

3.7 WHEN 同一请求内同一敏感值出现多次 THEN 系统 SHALL CONTINUE TO 复用同一个占位符；不同请求 SHALL CONTINUE TO 使用各自随机的 nonce，不共享映射，映射 SHALL CONTINUE TO 在请求完成时释放

3.8 WHEN 结构字段（`id`、`name`、`tool_call_id`、`tool_use_id` 等）或 `redact_tool_arguments=false` 时的工具参数子树中出现字符串 THEN 系统 SHALL CONTINUE TO 不检测、不改写这些字符串

3.9 WHEN 发生解析失败、映射容量耗尽、`max_findings` 超限等错误 THEN 系统 SHALL CONTINUE TO 按 `fail_closed` 配置整体拒绝或原样放行，不产生半脱敏正文

3.10 WHEN 写入追踪记录 THEN 系统 SHALL CONTINUE TO 只记录掩码预览和计数，原值不离开 `redact` 领域层，观测失败不改变拦截器的返回结果

3.11 WHEN 文本里有 `KEY=value` 等写法但左边键名不含 2.7 所列敏感词（例如 `BUILD_ID=a3f1c2d4-5b6e-4f70-8a9b-0c1d2e3f4a5b`、`LOG_FILE=app.log`） THEN 系统 SHALL CONTINUE TO 只按固定凭据规则、PII 规则、`custom_rules` 和高熵检测（含 2.1–2.3 的跳过规则）判断，不因键值写法本身脱敏

3.12 WHEN 敏感键名右边的值是纯数字（例如 `TOKEN_TTL=3600`）、布尔值（例如 `ENABLE_TOKEN=true`）或长度不足 8 THEN 系统 SHALL CONTINUE TO 不因敏感上下文脱敏该值，只按其他检测规则判断

3.13 WHEN OpenAI Responses 格式请求中出现 `function_call_output` 条目的 `output` 以外的字段（例如 `type`、`call_id`、`id`、用户自定义的 `metadata` 等），或其他类型条目中名为 `output` 的字段 THEN 系统 SHALL CONTINUE TO 按现有键名规则处理，不因 2.6 被新增扫描或改写

3.14 WHEN 同一段工具输出前后出现多条消息（例如上一条消息执行了 `cat .env`） THEN 系统 SHALL CONTINUE TO 只依据每个字段自身的文本内容判断是否脱敏，不做跨消息关联
