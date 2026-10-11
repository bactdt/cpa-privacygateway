# privacygateway {{VERSION}}（linux/amd64）

CLIProxyAPI（CPA）隐私脱敏网关插件：请求发往上游前把凭据与 PII 替换为占位符，响应与 SSE 流中再还原。

## 前置条件

- CPA v8.0.22 或兼容版本（插件 ABI 1，schema_version <= 6）。
- CPA 必须以 cgo 构建，否则宿主不会加载任何动态库插件。
- linux/amd64，glibc >= 2.34。

## 安装

1. 在 **CPA 的工作目录**（运行 CPA 的目录，默认插件目录 `plugins` 相对于它）解压：

   ```sh
   unzip privacygateway_{{VERSION}}_linux_amd64.zip
   sha256sum -c privacygateway/checksums.txt
   ```

   解压只会写入两处：`{{SO_PATH}}` 与 `privacygateway/`。

2. 把 `privacygateway/config.snippet.yaml` **合并**进 CPA 的 `config.yaml`。宿主要求
   `plugins.enabled: true` 与 `plugins.configs.privacygateway.enabled: true` 同时成立，否则不会加载。
   配置键必须是 `privacygateway`，由库文件名决定，不要改文件名。

3. 重启 CPA。

## 验证

最简单的方式：用浏览器打开 `privacygateway/panel.html`（本地单文件，无外部依赖）。填入 CPA 地址、管理密钥、API Key 和模型后：

- 「检查状态」逐项列出插件是否被发现、注册、启用，并给出修复建议；配置未启用时可一键启用。
- 「脱敏测试」判定上游收到的是占位符还是原文；「还原测试」检查回复是否还原。

面板只把请求发往你填写的 CPA 地址，密钥只保存在页面内存中。

手动方式：

- 查看 CPA 启动日志，确认出现 privacygateway 已加载的记录。
- 发送一条含合成邮箱（如 `user@example.com`）的请求，上游收到的正文应为 `[[PG_<16hex>_<序号>]]` 形式的占位符，客户端收到的响应应已还原。

没有加载时按顺序排查：`plugins.enabled` → 配置键是否为 `privacygateway` 且 `enabled: true` → 库文件名与目录 → 是否在 CPA 工作目录解压（或设置了 `plugins.dir`）→ CPA 是否为 cgo 构建 → glibc 版本。

### 管理界面提示「未找到插件页面」

该提示仅表示本插件没有声明菜单页面（旧版本），或宿主启用了 Home 模式；它不能证明插件没有加载。判断插件是否加载与启用，请看面板的「检查状态」。

新版插件会在 CPA 管理界面提供「隐私脱敏检测」页面，地址为 `/v0/resource/plugins/privacygateway/panel`。该页面是静态页，不需要管理鉴权即可打开；页面内的所有数据请求仍需在页面里输入管理密钥/API Key。

## 升级与卸载

- 升级：删除旧的 `privacygateway-v*.so` 后解压新包。同 ID 多个文件并存时宿主取最高版本；也可在配置中用 `store.version` 固定版本。
- 卸载：删除库文件，并将 `plugins.configs.privacygateway.enabled` 设为 `false`。

## 主要配置

全部可省略，默认 `fail_closed: true`（无法解析、超限、格式不支持时拒绝请求而非放行）。完整键见 `config.snippet.yaml` 内注释。

## 已知限制

- 流结束时尚未完成的占位符尾部没有投递渠道（TD-CPA-032 残留）。
- 宿主启用 Home 模式时插件资源路由不可用，此时用包内 `privacygateway/panel.html` 直接用浏览器打开（它会连接你在页面里填写的 CPA 地址）。
- 尚未在真实 CPA 宿主上完成端到端验收，仅有 Go 单元测试与交付物加载冒烟验证。
- 仅提供 linux/amd64；其他平台未构建、未验证。
