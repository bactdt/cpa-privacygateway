# privacygateway {{VERSION}}（linux/amd64）

CLIProxyAPI（CPA）隐私脱敏网关插件：请求发往上游前把凭据与 PII 替换为占位符，响应与 SSE 流中再还原。

## 前置条件

- CPA v8.0.22 或兼容版本（插件 ABI 1，schema_version <= 6）。
- CPA 必须以 cgo 构建，否则宿主不会加载任何动态库插件。
- linux/amd64，glibc >= 2.34。

## 安装

1. 在临时目录解压包，并把根目录动态库安装到 CPA 的插件发现目录：

   ```sh
   cd /path/to/CPA
   mkdir -p /tmp/privacygateway-{{VERSION}}
   unzip privacygateway_{{VERSION}}_linux_amd64.zip -d /tmp/privacygateway-{{VERSION}}
   (cd /tmp/privacygateway-{{VERSION}} && sha256sum -c privacygateway/checksums.txt)
   install -D -m 0755 \
     /tmp/privacygateway-{{VERSION}}/{{SO_PATH}} \
     plugins/linux/amd64/{{SO_PATH}}
   ```

   插件商店安装器会自动完成最后的目录移动；手动安装时不要把 `.so` 留在 CPA 工作目录。

2. 把临时解压目录中的 `privacygateway/config.snippet.yaml` **合并**进 CPA 的 `config.yaml`。宿主要求
   `plugins.enabled: true` 与 `plugins.configs.privacygateway.enabled: true` 同时成立，否则不会加载。
   配置键必须是 `privacygateway`，由库文件名决定，不要改文件名。

3. 重启 CPA。

## 验证

手动方式：

- 查看 CPA 启动日志，确认出现 privacygateway 已加载的记录。
- 发送一条含合成邮箱（如 `user@example.com`）的请求，上游收到的正文应为 `[[PG_<16hex>_<序号>]]` 形式的占位符，客户端收到的响应应已还原。

没有加载时按顺序排查：`plugins.enabled` → 配置键是否为 `privacygateway` 且 `enabled: true` → 库文件名 → `.so` 是否已安装到 CPA 配置的插件发现目录（默认 `plugins/linux/amd64/`，相对于 CPA 工作目录）→ CPA 是否为 cgo 构建 → glibc 版本。

### 管理页面（追踪日志）

默认不注册管理页面，`/v0/resource/plugins/privacygateway/trace` 与
`/v0/management/privacygateway/traces` 均返回 404。需要排障时在配置中设
`panel.enabled: true`（并保持 `trace_enabled: true`）后重启 CPA：

- `GET /v0/resource/plugins/privacygateway/trace`：内嵌追踪页面（菜单「隐私脱敏日志」，嵌入动态库，GET-only）。
- `GET /v0/management/privacygateway/traces?since=&limit=`：内存中的请求级脱敏事件快照
  （JSON，仅掩码信息，不含原值）；`limit` 范围 1..`trace_max_requests`（越界回退 50）；
  `DELETE` 同路径清空缓冲，返回 `{"cleared":n}`。
- 页面可见性 = `panel.enabled` 且 `trace_enabled`；数据缓冲仅内存、重启即清空。

追踪相关配置（全部可省略）：`trace_enabled`（默认 `true`）、`trace_recording`（默认 `false`，注册后是否立即记录）、
`trace_max_requests`（默认 `200`，范围 1..2000）、`trace_max_events`（默认 `64`，范围 8..256，单请求事件上限，Finding 明细上限 64）。

页面默认处于「未在记录」状态：点「开始记录」才开始采集（会清空旧记录），点「停止记录」后已有记录仍可阅读；
「暂停刷新」只停页面轮询，不影响记录开关。
最坏情形内存约 6 MiB（200 请求 × 64 事件的仅内存环形缓冲）；`trace_enabled: false` 清空缓冲并隐藏管理页面。

包内 `panel.html` 保留为 file:// 本地排障用的旧版检测面板，不随插件注册。

## 升级与卸载

- 升级：按安装步骤将新动态库安装到插件目录，再清理旧版本并重启 CPA。同 ID 多个文件并存时宿主取最高版本；也可在配置中用 `store.version` 固定版本。
- 卸载：删除库文件，并将 `plugins.configs.privacygateway.enabled` 设为 `false`。

## 主要配置

全部可省略，默认 `fail_closed: true`（无法解析、超限、格式不支持时拒绝请求而非放行）。完整键见 `config.snippet.yaml` 内注释。

## 已知限制

- 流结束时尚未完成的占位符尾部没有投递渠道（TD-CPA-032 残留）。
- 插件管理页面当前默认关闭；请通过 CPA 的 `/v0/management/plugins` 查看发现、注册和启用状态。
- 尚未在真实 CPA 宿主上完成端到端验收，仅有 Go 单元测试与交付物加载冒烟验证。
- 仅提供 linux/amd64；其他平台未构建、未验证。
