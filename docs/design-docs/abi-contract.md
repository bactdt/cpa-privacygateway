# ABI 契约

> 更新：2026-10-13。依据 `abi.go`、`management.go` 与锁定 SDK 的 `pluginapi/types.go`；§9/§10 依据核读的宿主 v8.0.22 源码；示例使用合成值。本轮验证 Go 并发回归及交付物 C 加载冒烟，不代表新版本真实部署验收。

## 1. 宿主基线

| 项 | 值 | 来源 |
|---|---|---|
| SDK | `github.com/router-for-me/CLIProxyAPI/v8 v8.0.22` | `go.mod` |
| SchemaVersion | 6 | `sdk/pluginabi` |
| ABIVersion | 1 | `sdk/pluginabi` |
| header-init 索引 | -1 | `sdk/pluginapi.StreamChunkHeaderInitIndex` |

SchemaVersion >=3 的流正文块不携带 OriginalRequest/RequestBody；>=5 不携带 HistoryChunks。插件当前不依赖这三个字段还原。

## 2. C 导出符号、内存与状态码

| 符号 | 行为 |
|---|---|
| `cliproxy_plugin_init(host_api, plugin_api)` | 保存宿主指针、填充插件回调表；空参数返回 1，成功返回 0 |
| `PrivacyGatewayPluginCall(method, req, len, resp)` | 复制 C 请求到 Go，按方法分发，写回 JSON 信封 |
| `PrivacyGatewayPluginFree(ptr, len)` | `C.free` 插件返回的缓冲；nil 可用，长度不参与释放 |
| `PrivacyGatewayPluginShutdown()` | 设置关闭标记、停止 janitor、解除全局引用并等待在途普通调用 |

`cliproxy_buffer` 是 `{void* ptr; size_t len;}`。宿主拥有传入 method 和请求内存，需保证本次调用期间有效；返回内存由 `C.CBytes` 分配，宿主消费后必须调用插件的 free_buffer 一次。插件调用宿主返回的内存只能经宿主 free_buffer 释放。

普通 RPC 即使业务失败也返回 C 状态 0，调用方必须读取信封 `ok/error`，不能仅检查 C 返回值。Go 侧普通错误在 C 入口转换为 `plugin_error`。nil method 为 `invalid_method`，长度超过 `1<<31-1` 为 `request_too_large`，未知方法为 `unknown_method`；返回缓冲为 nil 或分配失败尚无完整故障契约。

初始化当前只检查指针非空，没有主动核验 host.abi_version 或所有回调是否可用；不应称其为完整 ABI 兼容验证。宿主指针被保存，需活到关闭和回调结束；并发卸载的安全性需要 C 侧复验。

## 3. RPC 与能力

| 方法 | 处理入口 | 输入结构 |
|---|---|---|
| `plugin.register` | `handleABIRegister` | `abiLifecycleRequest` |
| `plugin.reconfigure` | `handleABIRegister` | 同上 |
| `request.intercept_before` | `InterceptRequestBeforeAuth` | `RequestInterceptRequest` |
| `request.intercept_after` | `InterceptRequestAfterAuth` | 同上 |
| `request.complete` | `HandleRequestComplete` | `RequestCompletion` |
| `response.intercept_after` | `InterceptResponse` | `ResponseInterceptRequest` |
| `response.intercept_stream_chunk` | `InterceptStreamChunk` | `StreamChunkInterceptRequest` |
| `host.log`（插件→宿主） | `logHost` | `abiHostLogRequest` |

能力 `request_interceptor`、`request_lifecycle_plugin`、`response_interceptor`、`response_stream_interceptor` 均为 true。能力声明表示宿主可调用该钩子，不表示全部协议边界已验收。

## 4. Wire JSON：大小写与 Base64

信封是 `{"ok":true,"result":...}` 或 `{"ok":false,"error":{"code":"...","message":"..."}}`。

生命周期字段为小写 `config_yaml`、`plugin_dir`；请求/响应内嵌 SDK 结构多数没有 JSON 标签，实际编码使用 `RequestID`、`SourceFormat`、`Body` 等 Go 字段名。`request_id`、`format` 不能作为等价替代。`host_callback_id` 是插件包装器的小写扩展字段。

所有 `[]byte` 字段经 Go `encoding/json` 编码为 **Base64 字符串**，不是未编码的 JSON 正文字符串，也不是整型数组。此规则包括 Body、ResponseBody、OriginalRequest、RequestBody、config_yaml；nil 编为 null。以下为最小有效请求，不展示所有零值字段。

注册：

```json
{"config_yaml":""}
```

请求前/请求后（Body 解码为 `{"content":"hello"}`）：

```json
{"RequestID":"req-demo","SourceFormat":"openai","Body":"eyJjb250ZW50IjoiaGVsbG8ifQ==","host_callback_id":"callback-demo"}
```

普通响应：

```json
{"RequestID":"req-demo","Body":"eyJjb250ZW50IjoiaGVsbG8ifQ==","StatusCode":200}
```

流初始化/正文/完成：

```json
{"RequestID":"req-demo","ChunkIndex":-1,"Body":null}
```

```json
{"RequestID":"req-demo","ChunkIndex":0,"Body":"ZGF0YToge31cblxu"}
```

```json
{"RequestID":"req-demo","Outcome":"succeeded"}
```

上面流正文 Base64 解码为带字面反斜杠的演示字节 `data: {}\n\n`；实际 SSE 应由宿主传入真实换行字节，不照抄字面反斜杠。

成功注册 result 含 `schema_version`、`metadata`、`capabilities`。普通拦截 result 为对应 SDK 响应结构；完成返回 `{"ok":true,"result":{}}`。未知方法示例：

```json
{"ok":false,"error":{"code":"unknown_method","message":"unknown method: demo.unknown"}}
```

## 5. 修改与业务拒绝

Body 非空才替换原正文；空修改对象表示保留宿主原内容。Headers 合并/替换同名头，ClearHeaders 负责删除头；插件当前不脱敏请求头。

策略拒绝是 RPC 成功，result 内设置 `Terminate=true`、StatusCode、ResponseHeaders、Base64 ResponseBody，不是信封 `ok=false`。例如 429 的解码后 ResponseBody 为：

```json
{"error":{"type":"rate_limit_exceeded","message":"privacy gateway concurrency limit reached"}}
```

流块 `DropChunk=true` 表示宿主不交付本块，Body 非空替换本块；ChunkIndex 从 0 开始，-1 为 header-init。SDK 没有终块布尔字段，request.complete 只通知清理，不提供补发 Body 的渠道。

## 6. 完整配置清单

CPA 通过 `plugins.configs.<pluginID>` 传入 YAML 子配置。完整示例见 [../../config.example.yaml](../../config.example.yaml)，不能把整个宿主配置传给 parseConfig。

| 字段 | 默认值 | 当前行为/校验 |
|---|---|---|
| enabled | true | `enabled: false` 四拦截器直通、不产生状态条目（2026-10-10 生效） |
| priority | 0 | 已解析，插件内部不读取；宿主排序另行确认 |
| max_concurrency | 0 | 0 不限制；必须 >=0，超过上限总是 429 |
| reject_keyword / reject_keywords | 空 / 空数组 | 去首尾空白后合并；正文大小写敏感子串匹配 |
| fail_closed | true | 已传播的请求体/解析错误按此开关处理 |
| request_ttl_seconds | 600 | 必须 >0；空闲 TTL，janitor 每分钟运行 |
| max_requests | 8192 | 必须 >0；收紧限额后保留已有条目并拒绝超额新 ID；满表时仅可淘汰空闲超过 1×TTL 的最旧条目（可能仍在途），否则 429 |
| max_body_bytes | 1048576 | 必须 >0；检查 Body 解码后字节数 |
| max_mappings | 1024 | 必须 >0；不同原值条数 |
| max_mapping_bytes | 1048576 | 必须 >0；只累计原值字节数 |
| redact_tool_arguments | false | 各格式工具参数字符串/子树，详见算法文档 |
| allowlist | 空数组 | 字面子串允许区间；有交集的敏感候选不替换 |
| custom_rules | 空数组 | Go RE2 正则，命中值进入请求映射；非法正则拒绝配置 |
| detectors.credentials/entropy/email/phone/id_card/bank_card | true | 独立开关；未指定沿用默认 |
| detectors.min_entropy | 3.5 | YAML 0 使用领域默认，负数拒绝 |
| detectors.min_entropy_len | 24 | YAML <=0 使用领域默认 |
| max_findings | 2000 | 单字段检出上限；负数拒绝；超限 fail_closed 500 max_findings_exceeded / fail_open 原样放行；allowlist 不计（TD-CPA-043） |

宿主 metadata.ConfigFields 展示七项（max_concurrency、reject_keyword、fail_closed、max_body_bytes、redact_tool_arguments、allowlist、custom_rules），不是可解析 YAML 字段全集。max_findings 默认 2000、可配置：单字段检出超限时 fail_closed 拒绝（500 `max_findings_exceeded`）、fail_open 原样放行，与容量超限同构；allowlist 条目不计入上限。

## 7. 热重载、日志与待决语义

注册和成功重配都创建新实例；`abiState` 锁覆盖旧实例选择、状态移交与新实例发布，避免并发重配取到同一个旧表。`takeOverFrom` 同时持旧/新表锁，接收完条目后才发布 redirect；旧回调的准入、读写、流恢复及释放转发到新表，旧 janitor 停止。重配解析失败且有旧实例时保持旧配置，记宿主 error 日志，并仍返回注册成功；首次注册错误则返回错误。日志回调失败被忽略，不影响拦截。

before/after-auth 共用格式处理：在条目锁内绑定首次非空 SourceFormat，冲突返回 400；非空且不支持的格式按 fail_closed 返回 415 或原样放行。after-auth 新 ID 先 acquire，超过并发或条目上限返回 429。`setMapping` 只操作已准入条目，在表锁和条目锁内原子选择首次映射；竞争失败的回调用已存映射重新生成正文。首次回调失败仅回收仍无映射的条目，不能删除另一回调已经发布的映射。

日志允许类型、数量、耗时和固定错误分类，禁止原值、请求正文、配置正文和映射。配置解析错误经 `configParseError` 白名单重建（固定类别 + 行号 + 标准标签 + 校验后的 Go 类型名），自定义标签显示为 custom tag，原始报错不落日志（TD-CPA-018，2026-10-10 关闭）。

| 待决事项 | 当前事实 | 目标决策 |
|---|---|---|
| enabled | `enabled: false` 四拦截器直通（TD-CPA-017，2026-10-10 已生效） | 已生效；`priority` 仍为宿主管理，插件不消费 |
| MaxFindings | 超限 fail_closed 拒绝 / fail_open 放行（TD-CPA-043，2026-10-10 已实现，全部正文路径覆盖） | 已解决；剩余为真实宿主联调确认 |
| 热重载 | 原子移交、发布及旧回调转发已覆盖并发重配回归 | Go 层已验证；真实宿主重载仍需部署验收；空闲 >1×TTL 满表淘汰和 >2×TTL janitor 强制回收的限制保留 |
| 同 ID 并发/复用 | 首次映射与格式选择持锁，失败不删除已发布映射；领域对象内部持锁 | Go 层并发往返与失败清理回归已验证，不依赖宿主串行调用 |
| 终块刷新 | 无调用渠道 | 在宿主可输出钩子内约定 flush |
| ABI 初始化/卸载 | 最小指针检查 | 核验版本、回调生命周期和 panic/错误隔离（TD-CPA-044 已关闭：请求链路 recover + fail-closed 终止信封，见 §8 事实 5 插件侧防护） |

详见 [redaction-algorithm.md](redaction-algorithm.md)、[../exec-plans/tech-debt-tracker.md](../exec-plans/tech-debt-tracker.md) 和并发审查修正计划。

## 8. 宿主 v8.0.22 行为事实（2026-10-10 核读源码）

以下事实逐条核读自宿主模块缓存源码 `github.com/router-for-me/CLIProxyAPI/v8@v8.0.22`（下称 $H），仅供联调与设计对齐；宿主升级后需复核。

| # | 宿主行为 | 文件来源 |
|---|---|---|
| 1 | after-auth 拦截返回 `Terminate=true` 时，执行器侧包装为 `RequestTerminatedError`（携带状态码/响应头/响应体），执行循环对 `isRequestTerminatedError` 直接返回、**不进入凭据重试**；请求侧再转 `DirectResponse` 错误消息直达客户端 | `$H/sdk/cliproxy/auth/conductor_execution.go:331-367`（`applyRequestAfterAuthInterceptor`）、`:146/:206`（terminated 即 return，不重试）、`$H/sdk/cliproxy/executor/types.go:144-153`、`$H/sdk/api/handlers/handlers_execution.go:352-361` |
| 2 | after-auth 拦截在**每次凭据尝试**时调用（`executeMixedOnce` 凭据循环内、`executor.Execute` 之前）；正文是 `SourceFormat` 协议格式（`opts.SourceFormat`），尚未转为上游 `ToFormat` | `$H/sdk/cliproxy/auth/conductor_execution.go:588-594`（循环内调用点）、`:337`（`SourceFormat: opts.SourceFormat`）、`$H/sdk/cliproxy/executor/types.go:101-103`（"after credential selection and before executor translation"） |
| 3 | `RequestID` 由 `uuid.NewString()` 生成于生命周期跟踪器创建时，同请求 before/after/complete 共用 | `$H/sdk/api/handlers/handlers_interceptors.go:120` |
| 4 | 宿主把插件 `Terminate` 视为 `DirectResponse` 错误，`completeError` 中 `DirectResponse=true` 映射为完成事件 `RequestCompletionRejected`——即**拒绝后宿主仍会发 RequestComplete**（outcome=rejected），插件的状态清理依赖这一点 | `$H/sdk/api/handlers/handlers_interceptors.go:173-176`（`completeError`）、`$H/sdk/api/handlers/handlers_execution.go:101-104`（拦截终止即 `completeError`）、`$H/sdk/api/handlers/handlers_interceptors.go:196-201`（`requestTerminationError`） |
| 5 | 插件返回 Go error（RPC err）时宿主**放行**：`callRequestInterceptor` 记 warn 日志后返回零值响应（不终止），链路继续——这是 fail-open 宿主语义，插件 fail_closed 依赖靠 `Terminate` 响应而非 error；插件 panic 时宿主 `recover` 后 `fusePlugin` 熔断（该插件后续调用被 `isPluginFused` 跳过），进程不崩溃 | `$H/internal/pluginhost/adapters_interceptors.go:18-31`（err→零值+warn）、`:6-8`（fused 跳过）、`:12-16`（recover→fusePlugin）、`$H/internal/pluginhost/adapters_usage_translation.go:96-105`（`fusePlugin`）、`$H/internal/pluginhost/client_guard.go:44-63`（guarded client recover 后 re-panic，由上层 interceptor recover 兜底） |
| 6 | 插件侧防护（TD-CPA-044，2026-10-10 已实现）：请求链路方法（intercept before/after、complete、response/stream）在 `handleABIMethod` 入口 `defer recover`——panic 转宿主 error 日志 + fail-closed 终止信封（500 `server_error`），不再依赖宿主熔断兜底；插件不可用（未注册/shutdown）与请求解码失败同样返回 OK 信封内结果而非 Go error（事实 5 表明 error 信封实现不了 fail-closed）：fail_closed → 500 `server_error` / 400 `invalid_request_error` 终止；fail_open → 空 `Body` 直通（宿主对 `len(resp.Body)==0` 保留原请求体，见 `adapters_interceptors.go:144-146`）；fail_closed 取值：已注册实例读配置，实例不可用时沿用最近一次注册配置，从未注册过默认 true。响应/流方法无终止协议，解码失败空结果直通并由流恢复器尾块兜底 | 插件源码 `cpa/abi.go`（`handleABIMethod` recover、`gatewayFailClosed`、`unavailableEnvelope`、`decodeFailureEnvelope`）、`cpa/abi_test.go`（`TestABIRecoverConvertsPanicToTerminate` 等 6 条） |

## 9. 宿主发现与命名规则

以下规则逐条核读自 $H（2026-10-11），决定插件包内 `.so` 的文件名、目录与启用开关；与 [.kiro/specs/plugin-package/design.md](../../.kiro/specs/plugin-package/design.md) 的「宿主加载规则」表同源。

| # | 宿主行为 | 依据 |
|---|---|---|
| 1 | 扫描目录依次为 `<dir>/<GOOS>/<GOARCH>`、`<dir>`（`runtime.GOOS`/`GOARCH` 由宿主编译期决定）；目录不存在则跳过 | `$H/internal/pluginhost/platform.go:308-313`（`candidateDirs`）、`:131-134`（`os.ReadDir` 不存在即 continue） |
| 2 | 只扫平铺**常规文件**且后缀匹配平台扩展（linux 为 `.so`）；符号链接与子目录被 `entry.Type().IsRegular()` 过滤，文件按名称排序后逐个解析 | `$H/internal/pluginhost/platform.go:140-148` |
| 3 | 插件 ID/版本由**文件名**推导，不读 manifest：去扩展名后，在**最后一个 `-v`** 处切分（`strings.LastIndex`），两侧分别匹配 ID 与版本正则；切分失败或 ID 非法则整个文件被跳过。ID 正则 `^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`；版本正则 `^[0-9][0-9A-Za-z.+-]*$` 且**不得以 `v` 开头**（`validPluginVersion` 额外检查） | `$H/internal/pluginhost/platform.go:16-19`（正则）、`:43-45`（`validPluginVersion`）、`:62-96`（`pluginFileFromPath`）、`:84-91`（`LastIndex(name, "-v")` 切分） |
| 4 | 同 ID 多个文件取**版本更高**者；带版本优先于不带版本（候选版本为空直接落选）；版本不可逐段数值比较时退化为字典序。宿主还会清理未选中的旧文件（`cleanupUnselectedPluginFiles`） | `$H/internal/pluginhost/platform.go:204-216`（`pluginFilePreferred`）、`:192-202`、`:252-289` |
| 5 | 目录默认 `plugins`（相对 CPA 进程工作目录），支持 `~` 前缀展开；`plugins.dir` 可覆盖 | `$H/internal/config/plugin_path.go:14-33`（`ResolvePluginsDir`）、`$H/internal/pluginhost/config.go:31`（默认值） |
| 6 | **双开关**：需 `plugins.enabled: true` 且 `plugins.configs.<id>.enabled: true` 才加载该插件；`plugins.configs.<id>.enabled` 为 YAML 指针，nil 归一化为 false（未写 enabled 的配置块默认禁用）；`plugins.enabled=false` 时清空全部管理/资源路由 | `$H/internal/pluginhost/host.go:226`（`!rc.Enabled` 清空）、`:262`（`!item.Enabled` 跳过）、`$H/internal/config/config_types.go:28-42`、`:44-59`（`Enabled *bool`） |
| 7 | `plugins.configs.<id>` 整个子树（含宿主强制的 `enabled`/`priority` 标量）作为 `config_yaml` 传给插件；空配置块传默认的 `enabled: false\npriority: 0\n` | `$H/internal/pluginhost/config.go:81-88`（`runtimeConfigYAML`）、`:13`（默认 YAML） |
| 8 | cgo 构建前提：`loader_unix.go` 带 `//go:build cgo` 标签，非 cgo 宿主不编译任何 dlopen 逻辑 | `$H/internal/pluginhost/loader_unix.go:1` |
| 9 | 加载检查顺序：`dlopen(RTLD_NOW\|RTLD_LOCAL)` → `dlsym("cliproxy_plugin_init")` → init 返回 0 → `api.abi_version == pluginHostABIVersion`（= `pluginabi.ABIVersion`，即 1）→ `api.call`/`api.free_buffer` 非空，任一失败即拒绝加载 | `$H/internal/pluginhost/loader_unix.go:118-168`、`$H/internal/pluginhost/abi.go:9` |
| 10 | register 检查：`schema_version <= pluginabi.SchemaVersion`（=6，缺失视为 1）；元数据 `Name/Version/Author/GitHubRepository` 去空白后非空；至少一个能力指针非空（`validPlugin`） | `$H/internal/pluginhost/rpc_client.go:71-77`（schema 检查）、`$H/internal/pluginhost/host.go:1185-1219`（`validPlugin`） |

对本插件的约束：交付文件名必须为 `privacygateway-v<ver>.so`（裸 `.so` 与 zip 内一致），放置于 `plugins/linux/amd64/`；`build/libprivacygateway.so` 直接复制会得到 ID `libprivacygateway`，与配置键不匹配；升级时旧版本文件需手动清理（或用 `plugins.configs.<id>.store.version` 固定版本）。

## 10. management.register / management.handle 线格式

如果插件在注册信封中声明 `capabilities.management_api`（小写下划线，`rpcSchemaCapabilities` 无 omitempty），宿主会在重建路由表时逐插件调用 `management.register`；两个方法常量见 `$H/sdk/pluginabi/types.go:87-88`。本插件实现在 `management.go`，分发于 `abi.go` 的 fail-closed 路径之前（页面为静态内容，与实例状态无关）。

**当前状态：管理可见性由双开关联动控制**（TD-11，2026-10-21 回写）：`panel.enabled` 且 `trace_enabled` 同时为 true 才可达（运行时取值保存在 `abi.go` 的 `abiState.panelEnabled`/`traceEnabled`，由最近一次成功的 `plugin.register`/`plugin.reconfigure` 写入，shutdown 与重新 init 经 `resetManagementLocked` 清除）。遵循官方开发文档"只声明实际实现的能力"的建议，任一开关为 false（含从未注册过）时注册信封**不声明** `capabilities.management_api`；`management.register` 返回空 `routes` 与 `resources`，`management.handle` 对一切请求返回 404。双开关同时开启时注册信封声明 `management_api`，`management.register` 返回两条数据管理路由与单个资源 `{Path:"trace", Menu:"隐私脱敏日志", ...}`（原 `panel` 资源路由已移除），`management.handle` 按 10.3 服务。`packaging/trace.html` 经 `go:embed` 嵌入；`packaging/panel.html` 不再嵌入，仅随 zip 分发作 `file://` 独立排障（不受开关影响）。

### 10.1 management.register

宿主请求为 `pluginapi.ManagementRegistrationRequest`（字段无 JSON tag，按 Go 字段名编码）：

| 字段 | 值 | 依据 |
|---|---|---|
| `Plugin` | 插件元数据 | `$H/internal/pluginhost/management.go:113-117` |
| `BasePath` | `/v0/management` | 同上 `:18`（`managementBasePath`） |
| `ResourceBasePath` | `/v0/resource/plugins/<id>` | 同上 `:19,116`（`resourcePluginBasePath + "/" + record.id`） |

插件响应 `{"routes":[...],"resources":[...]}`（小写 tag `routes`/`resources`，见 `$H/internal/pluginhost/rpc_schema.go:131-134`）。**路由条目**解码为 `pluginapi.ManagementRoute`，该结构**无 JSON tag**（`$H/sdk/pluginapi/types.go:1429-1440`），故插件必须按 Go 默认键名编码 `Method`、`Path`、`Description`；`Menu` 与 `Handler` 字段不得出现（`Menu` 非空的 GET 路由会被宿主当旧式资源菜单，`Handler` 由宿主解码后补设，`rpc_client.go:619-641`）。资源条目字段名大写、**无 JSON tag**：`Path`、`Menu`、`Description`（`$H/sdk/pluginapi/types.go:1442-1454`）。`Menu` 非空的资源路由经 `$H/internal/pluginhost/snapshot.go:123-146`（`registeredPluginMenus`）暴露到 `GET /v0/management/plugins` 的 `menus` 字段，即管理界面菜单来源。

双开关开启时本插件返回 `routes` 两条：

| Method | Path | Description |
|---|---|---|
| `GET` | `/v0/management/privacygateway/traces` | 读取内存中的请求脱敏追踪快照 |
| `DELETE` | `/v0/management/privacygateway/traces` | 清空内存中的追踪缓冲 |

`resources` 恒为单条 `{Path:"trace", Menu:"隐私脱敏日志", Description:"查看每个请求的脱敏/还原事件流（仅内存，不含原值）"}`；任一开关关闭时两者皆空（见 `management_trace.go` 的 `managementTraceRoutes`）。宿主按 `METHOD + 精确路径` 注册路由，相对路径会补到 `/v0/management` 下（`normalizeManagementRoute`，`$H/internal/pluginhost/management.go:120-154`），本插件直接给出完整路径避免歧义。

### 10.2 资源路由规则（宿主侧）

| 规则 | 依据 |
|---|---|
| 资源路由仅 GET：`ServeResourceHTTP` 对非 GET 直接返回 false（落到 404） | `$H/internal/pluginhost/management.go:295-297` |
| 完整 URL 前缀为 `/v0/resource/plugins/<id>/`；插件可传相对路径（如 `panel`）或完整前缀路径，宿主归一化；路径含 `:`、`*`、`..`、空白即整条无效 | 同上 `:189-225`（`normalizeResourceRoute`） |
| 资源请求**不经管理鉴权**（`ResourceRoute.Handler` 注释明示），入口挂在 `engine.NoRoute` 的 `/v0/resource/plugins/` 前缀分支 | `$H/sdk/pluginapi/types.go:1452-1454`、`$H/internal/api/server.go:252`、`$H/internal/api/server_management.go:260-265` |
| `cfg.Home.Enabled` 时资源路由禁用（404） | `$H/internal/api/server_management.go:297-300`（`pluginResourceNoRoute`） |
| 宿主把请求包装为 `pluginapi.ManagementRequest{Method, Path, Headers, Query, Body}` 经 RPC 转发，另加小写扩展字段 `host_callback_id`（`rpcManagementRequest` 内嵌 + tag）；`Body` 为 `[]byte`，经 JSON 即 Base64 | `$H/internal/pluginhost/rpc_client.go:643-651`、`$H/internal/pluginhost/rpc_schema.go:125-129`、`$H/sdk/pluginapi/types.go:1459-1471` |
| `schema_version=6` ≥ `SchemaVersionRawManagementResponse(6)`，响应正文不做 HTML 实体转义（低于 6 才转义） | `$H/sdk/pluginabi/types.go:30-32`、`$H/internal/pluginhost/management.go:356-358`、`:270-272` |

### 10.3 management.handle（插件侧响应规则）

请求即 10.2 的 `ManagementRequest` + `host_callback_id`；响应为 `pluginapi.ManagementResponse{StatusCode, Headers http.Header, Body []byte}`（`StatusCode` 为 0 时宿主按 200 处理，`Headers` 为 map[string][]string 逐值 Add，`Body` 经 Base64 编码；字段无 JSON tag，按 Go 字段名编码，见 `$H/sdk/pluginapi/types.go:1473-1483`）。

本插件的分发规则（实现在 `management.go`/`management_trace.go`；任一开关为 false 时解码与方法检查均不生效，一切请求直接 404）：

| 条件 | StatusCode | 正文/头 |
|---|---|---|
| **可见性关闭（`panel.enabled=false` 或 `trace_enabled=false`，TD-11）** | 404 | 纯文本 `privacy gateway management resource not found` |
| `Method == GET` 且 `Path` 剪掉 query 后**完整等于** `/v0/management/privacygateway/traces`（即 10.1 注册的完整路径） | 200 | 追踪快照 JSON（见下）+ `Content-Type: application/json; charset=utf-8`、`X-Content-Type-Options: nosniff`、`Cache-Control: no-store` |
| `Method == DELETE` 且同一完整路径 | 200 | `{"cleared":n}`（n 为清除的 Trace_Record 条数）+ 同上三个响应头 |
| `Method == GET` 且 `Path` 归一化后为资源 `trace`（相对 `trace` 或完整 `/v0/resource/plugins/privacygateway/trace`） | 200 | `traceHTML` + `Content-Type: text/html; charset=utf-8`、`X-Content-Type-Options: nosniff`、`Cache-Control: no-store`、`Content-Security-Policy: default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'none'`、`Referrer-Policy: no-referrer` |
| 数据路径上的 `Method` 非 GET/DELETE（仅双开关开启后可达） | 405 | 纯文本 + `Allow: GET, DELETE` |
| 页面路径上的 `Method != GET`（仅双开关开启后可达） | 405 | 纯文本 + `Allow: GET` |
| 其他路径（仅双开关开启后可达） | 404 | 纯文本 |
| 请求体无法解码（仅双开关开启后可达） | 400 | 纯文本 |
| 插件 `shuttingDown` | 错误信封（RPC error） | 不访问已释放状态 |

**`GET /v0/management/privacygateway/traces` 响应结构**（`traceJSONResponse`，R11.1–R11.9）：

```json
{
  "version": "0.1.6",
  "plugin": {"version": "0.1.6", "enabled": true, "fail_closed": true, "trace_enabled": true},
  "limits": {"max_requests": 200, "max_events": 64, "max_finding_details": 64},
  "buffered": 3, "evicted": 0, "next_since": 7,
  "requests": [{ "...Trace_Record：id/started_at/finished_at/request_id/trace_id/format/stream/outcome/status/duration_ms/partial + events 与 findings 明细...": "..." }]
}
```

- 查询参数 `limit`（1..`trace_max_requests`，越界回退 50）与 `since`（整数事件序号）由 `parseTraceQuery` 解析；`since=0` 返回全部快照，`since=next_since` 为增量轮询。
- `plugin.trace_enabled` 取 `abiState`（可达时恒为 true，TD-11）；`plugin.version`/`limits.*` 为配置化字段。
- 资源通道 `/v0/resource/plugins/privacygateway/trace` 仅服务 HTML 页面，**永远** 404 JSON 数据（数据只走完整管理路径，R12.1）。
- 分发以"完整路径剪 query 后精确匹配"为主、资源前缀折叠为辅：`normalizeManagementPath` 只剪 query，不做 `plugins/<id>` 参数折叠，避免 `/v0/management/privacygateway/traces` 之外的路径被误配对。

与 fail-closed 的关系：management 分发位于 `handleABIMethod` 的 fail-closed 路径**之外**（请求链路熔断不影响管理能力），但管理可达性独立地由注册态双开关（TD-11）决定：从未注册或关闭时 404。这与 §6 事实 6 的请求链路 fail-closed 语义无关。

panic 约束：宿主对 `callResourceHandler`/`callManagementHandler` 均 `defer recover` 后 `fusePlugin` 熔断**整个插件**（`$H/internal/pluginhost/management.go:338-344`、`:364-371`），因此插件 handler 必须自行 recover，把内部异常转为 500 `ManagementResponse`（信封 `ok=true`）而不是让 panic 逃逸（`management.go` 的 defer recover）。
