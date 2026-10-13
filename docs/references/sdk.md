# 宿主 SDK 速查

> 外部依赖的可检索参考资料（规则 §2.1 `docs/references/`）。
> 来源：`github.com/router-for-me/CLIProxyAPI/v8@v8.0.22` 的 `sdk/` 目录。

## 版本常量

| 常量 | 值 | 包 |
|---|---|---|
| `SchemaVersion` | 6 | `sdk/pluginabi` |
| `ABIVersion` | 1 | `sdk/pluginabi` |
| `StreamChunkHeaderInitIndex` | -1 | `sdk/pluginapi` |

## RPC 方法名常量（`sdk/pluginabi`）

```text
MethodPluginRegister              = "plugin.register"
MethodPluginReconfigure           = "plugin.reconfigure"
MethodRequestInterceptBefore      = "request.intercept_before"
MethodRequestInterceptAfter       = "request.intercept_after"
MethodRequestComplete             = "request.complete"
MethodResponseInterceptAfter      = "response.intercept_after"
MethodResponseInterceptStreamChunk= "response.intercept_stream_chunk"
MethodHostLog                     = "host.log"
```

## 能力键（`abiCapabilities`）

```text
request_interceptor          request_lifecycle_plugin
response_interceptor         response_stream_interceptor
```

## 请求/响应格式常量（`sdk/translator`）

`openai`、`openai-response`、`claude`、`gemini`、`codex`，另有 `antigravity`、`interactions`。

## 导出符号约定

插件必须导出 `cliproxy_plugin_init`；本插件另导出 `PrivacyGatewayPluginCall` / `PrivacyGatewayPluginFree` / `PrivacyGatewayPluginShutdown`。

## 更新方式

升级 SDK 版本时，同步更新本文件与 `docs/design-docs/abi-contract.md`，并重跑 `make check`。
