# CPA Privacy Gateway

CLIProxyAPI（CPA）的隐私脱敏动态库插件。插件在请求发送到上游前识别凭据和个人信息，将命中的内容替换为请求级占位符；响应和 SSE 流返回客户端前再恢复原文。

## 功能

- 凭据、邮箱、手机号、身份证、银行卡和高熵字符串检测
- 自定义 RE2 正则脱敏规则
- OpenAI、OpenAI Responses、Claude、Gemini、Codex 等格式
- 非流式响应和 SSE 流式响应还原
- 请求级并发、映射数量、映射字节数和 TTL 限制
- `fail_closed` 失败关闭策略
- C ABI 动态库，可由 CPA 直接加载

占位符只在 CPA 与上游之间使用，格式类似：

```text
[[PG_0123456789abcdef_1]]
```

## 安装

从 [Releases](https://github.com/bactdt/cpa-privacygateway/releases) 下载最新的 Linux amd64 zip。也可以在 CPA 插件商店中添加 registry：

```text
https://raw.githubusercontent.com/bactdt/cpa-privacygateway/main/plugin.json
```

商店安装器会自动把根目录动态库写入 CPA 的插件目录。手动安装时，先在临时目录解压，再把动态库放入 CPA 插件目录：

```sh
cd /path/to/CPA
mkdir -p /tmp/privacygateway-0.1.3
unzip privacygateway_0.1.3_linux_amd64.zip -d /tmp/privacygateway-0.1.3
(cd /tmp/privacygateway-0.1.3 && sha256sum -c privacygateway/checksums.txt)
install -D -m 0755 /tmp/privacygateway-0.1.3/privacygateway-v0.1.3.so \
  plugins/linux/amd64/privacygateway-v0.1.3.so
```

CPA 插件商店安装器会将动态库写入：

```text
plugins/linux/amd64/privacygateway-v0.1.3.so
```

## 配置

在 CPA 的 `config.yaml` 中同时启用全局插件和本插件：

```yaml
plugins:
  enabled: true
  configs:
    privacygateway:
      enabled: true
      priority: 100
      fail_closed: true
      max_concurrency: 0
      max_requests: 8192
      request_ttl_seconds: 600
```

完整配置参考 [config.example.yaml](config.example.yaml)。修改后重启 CPA，或使用 CPA 支持的配置重载功能。

## 自定义规则

`custom_rules` 使用 Go RE2 正则表达式。命中的完整文本会进入同一请求映射，并在响应中自动还原：

```yaml
plugins:
  configs:
    privacygateway:
      enabled: true
      custom_rules:
        - 'CUSTOM-[A-Z0-9]{8}'
        - '订单号[:：]?[A-Z]{2}[0-9]{8}'
```

例如，`CUSTOM-AB12CD34` 会被替换为占位符。非法正则会使插件配置加载失败，不会静默忽略。

`allowlist` 可排除不应脱敏的字面值：

```yaml
plugins:
  configs:
    privacygateway:
      allowlist:
        - 'example.com'
```

## 验证插件是否生效

1. 查看 CPA 启动日志，确认 `privacygateway-v<version>.so` 加载和注册成功。
2. 确认配置键是 `privacygateway`，不要写成仓库名 `cpa-privacygateway`。
3. 用合成邮箱、手机号或自定义规则值发送请求。
4. 查看上游收到的请求，应该看到 `[[PG_...]]`，而不是原文。
5. 客户端收到的普通响应和流式响应应该恢复原文。

如果上游日志已经出现原文，说明请求没有经过插件，优先检查插件目录、CPA 的 `plugins.enabled`、配置键和 CPA 是否为 cgo 构建。

## 安全边界

- 映射只保存在请求状态中，并受 TTL 和容量限制。
- 占位符使用每请求随机 nonce，不能跨请求复用。
- 插件不会把映射上传到外部服务。
- 自定义规则是脱敏匹配规则，不是密码学加密。
- 当前面板页面默认关闭，代码仍保留以便后续按需重新启用。

## 构建

本地构建需要 Go、cgo 和 Linux amd64 工具链：

```sh
make build
make package
```

发布版本由 GitHub Actions 在推送 `v*` 标签时构建并上传 Release。发布包只提供 Linux amd64 动态库。

## 许可

本项目使用 [MIT License](LICENSE)。
