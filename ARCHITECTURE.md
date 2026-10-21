# ARCHITECTURE.md

> 领域与包分层的顶层地图。依赖方向规则由 `internal/archtest/arch_test.go` 机械强制。

## 1. 系统定位

`cpa` 是 **CLIProxyAPI（CPA）的原生插件**，不是独立进程：

```text
CPA 宿主进程
   │  dlopen(libprivacygateway.so)
   │  cliproxy_plugin_init(host_api, plugin_api)   ← ABI 握手
   ▼
本插件（同进程内）
   │  RPC: plugin.register / plugin.reconfigure
    │       request.intercept_before / request.intercept_after
    │       response.intercept_after / response.intercept_stream_chunk
    │       request.complete
    │       management.register / management.handle
   ▼
redact 领域层：检测 → 占位符映射 → 响应/流还原
```

宿主经 `cliproxy_plugin_init` 拿到插件回调表，之后按方法名通过 `PrivacyGatewayPluginCall` 分发。插件可回调宿主（`host_log` 等）经 `privacygateway_call_host`。

## 2. 分层模型

存量项目适配规则 §4.1，本项目只有两层（规模小、职责清晰）：

```text
入口层 (main 包)      →   领域层 (redact 包)
abi.go / plugin.go          detect.go  (检测)
management.go (资源/数据路由) mapping.go (占位符映射)
management_trace.go          walk.go    (JSON 遍历替换)
state.go / config.go         jsonedit.go (JSON 字符串定点编辑)
metadata.go / abi_envelope.go finding.go  (Finding/MaskPreview，观测旁路数据形状)
trace.go / trace_hooks.go    stream.go / stream_payload.go / stream_text.go (流式还原、形态分派、文本流暂扣)
main.go                      stream_openai.go / stream_claude.go / stream_gemini.go / stream_responses.go (协议规划器)
shared_library_*.go
```

**依赖方向规则（强制）**：

| 规则 | 含义 | 机械检查 |
|---|---|---|
| R-1 | `main` 可导入 `redact` | `internal/archtest` `TestDependencyDirection` |
| R-2 | `redact` 禁止导入 `main` | 同上（白名单为空集） |
| R-3 | `redact` 禁止导入宿主 SDK `github.com/router-for-me/CLIProxyAPI/...` | `TestDomainLayerHasNoHostSDK` |
| R-4 | 包之间禁止导入环 | `TestNoImportCycles` |
| R-5 | 新增包必须在白名单显式登记 | `TestDependencyDirection` |

> 为什么领域层必须纯净：`redact` 的正确性即安全属性（漏报=隐私泄露，误还原=请求破坏）。保持它无宿主依赖，才能用纯单元测试穷举边界。

## 3. 运行时数据流

### 3.1 请求脱敏（`InterceptRequestBeforeAuth`）

```text
req.Body（Base64 解码后的原始 JSON 字节）
  → 准入检查：RequestID / 并发上限 / 关键词 / 体积 / SourceFormat
  → NewMapping(capacity, maxBytes)          分配请求级占位符表（随机 nonce）
  → RedactJSON(format, body, mapping, cfg)  遍历受字段表允许的 JSON 字符串
       → mapping.Redact(text)               检测 span → allowlist 过滤 → 替换为 [[PG_<nonce>_<seq>]]
  → 存 mapping 到 stateTable[requestID]
  → 返回脱敏后的 JSON body 给宿主

当前不处理 Query、Header、表单、二进制或媒体正文；映射容量错误仍有被 visitor 忽略的风险（TD-CPA-031）。
```

### 3.2 响应还原（`InterceptResponse` / `InterceptStreamChunk`）

```text
非流式：RestoreJSON(body, mapping)   → 占位符 → 原文
流式：  StreamRestorer.RestorePayload(format, chunk)
          → 一进一出、永不丢块（不设 DropChunk）；形态分派 bare JSON / SSE 行 / opaque
          → 协议文本字段上还原，跨事件占位符按文本流暂扣（≤33 字节），结束信号处注入字段或合成增量帧
          → 详见 docs/design-docs/redaction-algorithm.md §6
```

### 3.3 生命周期

```text
plugin.register / plugin.reconfigure  → 解析 YAML → newPlugin → 启动 janitor（reconfigure 替换全局实例并重置旧状态）
request.complete                      → stateTable.release(requestID)  清理映射
janitor（每分钟）                      → 清理 TTL 过期条目
流结束                                  → 残留在协议结束信号处投递；结束信号前中断时 request.complete 的 Flush 计入 StreamStats.Leftover 后丢弃（SDK 无终块通道，TD-CPA-032 残留）
```

### 3.4 观测旁路（RT-1 追踪，只读不干预）

```text
redact 检测命中  → Finding{Kind, Path, Count, Preview}   Preview = MaskPreview(原值)，原值不出 redact
                     → WalkOptions.OnFinding 收集器（nil 即零成本旁路）
trace_hooks.go     → Trace_Buffer 环形缓冲（trace_max_requests × trace_max_events，仅内存）
   四拦截器挂钩      → 阶段事件（request.before_auth/after_auth/response.restored/stream.* 等）
   HandleRequestComplete → duration_ms / outcome 落账 + 冲刷
GET/DELETE /v0/management/privacygateway/traces → 页面轮询（不落宿主日志，RT-3）
```

旁路原则：观测失败（panic/超限/丢弃）不得改变拦截器返回值与脱敏结果；`trace_enabled=false` 时缓冲清空且入口单次开关读取即返回（架构不变量 R-1/R-2 不受影响：`redact` 仍不反向依赖 `main`，收集器经回调注入）。

## 4. 关键不变量

| 编号 | 不变量 | 位置 |
|---|---|---|
| I-1 | 同一请求内，同一敏感值复用同一占位符 | `mapping.go` `rawToPh` |
| I-2 | 占位符格式 `[[PG_<16hex>_<seq>]]`，nonce 每请求随机 | `mapping.go` `placeholderRe` |
| I-3 | 解析失败 / 超限 / 格式不支持 / 映射容量耗尽 / 空 RequestID 时按 `fail_closed` 决定拒绝还是放行（默认拒绝）；重复 RequestID（before/after-auth）用既有映射重新脱敏，after-auth 无状态时完整脱敏并写回（TD-CPA-031/034/037 已实现，待集中验证） | `plugin.go` |
| I-4 | 状态表条目受 TTL 约束：空闲超 TTL 可被驱逐，admitted 在途条目保留至 2×TTL 后强制回收；满表全在途拒绝新准入；重配/重新注册移交在途状态 | `state.go` |
| I-5 | Mapping 与 StreamRestorer 内部互斥；数组文本上下文只作用于直接字符串元素，不穿过对象子树 | `redact/mapping.go`、`redact/stream.go`、`redact/walk.go` |
| I-6 | ABI 层单次调用受 `maxCGoBytesLen` 约束 | `abi.go` |
| I-7 | management.* 分派先于 beginPluginCall 与 fail-closed 路径；可达性由注册态双开关联动（`panel.enabled && trace_enabled`，TD-11），关闭即不声明 management_api 且 404；shutdown 后拒绝；handler 内 panic 必须被 recover，避免宿主熔断插件 | `management.go`、`management_trace.go`、`abi.go` |
| I-8 | 追踪事件只含掩码预览（`MaskPreview`），原值不出 `redact`；缓冲仅内存、有界，观测路径的任何失败不改变拦截器返回值 | `redact/finding.go`、`redact/mapping.go`、`trace.go`、`trace_hooks.go` |

## 5. 目标态与差距

当前架构已满足分层要求，无越层依赖。待改进项见 `docs/exec-plans/tech-debt-tracker.md`：

- `abi.go` 已按 `abi_export` / `abi_dispatch` / `abi_register` 拆分（TD-CPA-019，fixed；当前 404 行，仍需守 <=500）。
- `walkValue`（68 行）、`Config.Detect`（54 行）按已有处置保留；`InterceptRequestBeforeAuth` 已拆分（TD-CPA-010/011）。
- 映射超限错误传播、空/重复 RequestID、热重载在途映射已处理（TD-CPA-031/033/034）；流式终块投递仍无宿主通道（TD-CPA-032 残留）。
- 流式块被裸载荷误判丢弃已修复（TD-CPA-045，单元/属性/fuzz 验证，真实宿主端到端待验）；流式还原已知限制见 TD-CPA-046–050。
- 手写插入排序两处重复、占位符格式两处定义已收敛（TD-CPA-014/015，fixed）。
