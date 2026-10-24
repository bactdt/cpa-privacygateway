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
mkdir -p /tmp/privacygateway-0.1.8
unzip privacygateway_0.1.8_linux_amd64.zip -d /tmp/privacygateway-0.1.8
(cd /tmp/privacygateway-0.1.8 && sha256sum -c privacygateway/checksums.txt)
install -D -m 0755 /tmp/privacygateway-0.1.8/privacygateway-v0.1.8.so \
  plugins/linux/amd64/privacygateway-v0.1.8.so
```

CPA 插件商店安装器会将动态库写入：

```text
plugins/linux/amd64/privacygateway-v0.1.8.so
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
- 管理界面追踪页面默认不可见（需 `panel.enabled: true` 且 `trace_enabled: true` 才注册，菜单「隐私脱敏日志」）。
- 追踪默认**不记录**：页面打开后点「开始记录」才采集（开启时清空旧记录，停止后保留已有记录可继续阅读）；也可配置 `trace_recording: true` 开机即记录。追踪只保留掩码预览、仅内存有界，不写磁盘与宿主日志。
- 包内 `panel.html` 仅为随 zip 分发的 `file://` 本地排障页，不再嵌入动态库。

## 构建

本地构建需要 Go、cgo 和 Linux amd64 工具链：

```sh
make build
make package
```

发布版本由 GitHub Actions 在推送 `v*` 标签时构建并上传 Release。发布包只提供 Linux amd64 动态库。

发布流程（`release.yml` 会在 tag push 时**重新构建并覆盖** Release 资产，本地 `make package` 的 zip/校验值不可直接用于 `plugin.json`）：

1. bump `plugin.go` 的 `pluginVersion`（及 `plugin.yaml`/README 中的版本引用），提交后打 `v*` 标签并推送。
2. 等待 `Release plugin` workflow 完成（CI 重新打包 zip 并上传，附带 `checksums.txt`）。
3. 从 **CI 发布后的资产** 取 `sha256` 与 `size`，回写 `plugin.json` 的 `install.artifacts[0]` 并推送。
4. 验证：按 `plugin.json` 的 `url` 下载资产，`sha256sum` 与 `plugin.json` 一致（CPA 插件商店安装时会校验，不一致报 502 `plugin_install_failed`）。

## 许可

本项目使用 [MIT License](LICENSE)。
