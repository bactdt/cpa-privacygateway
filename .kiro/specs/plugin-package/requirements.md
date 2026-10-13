# Requirements Document

## Introduction

本文档由 `design.md` 派生，约束隐私脱敏网关插件（`privacygateway`）面向 CPA v8.0.22 的 Linux amd64 发布包：zip 包布局与命名、版本与 ID 一致性、可复现与校验、当前文件夹输出与裸 `.so` 交付、安装与启用说明、独立检测面板、CPA 管理界面内嵌插件页面、交付物冒烟测试、真实宿主验收与已知限制。

## Glossary

- **Packager**：打包程序 `scripts/package/main.go`，由 `make package` 调用。
- **Package**：Packager 产出的 `privacygateway_<ver>_linux_amd64.zip`。
- **Plugin_Library**：插件 C 共享库；包内路径为 `plugins/linux/amd64/privacygateway-v<ver>.so`。
- **Standalone_Library**：复制到当前文件夹的裸库 `./privacygateway-v<ver>.so`。
- **Plugin_ID**：常量 `privacygateway`，宿主由库文件名推导。
- **Plugin_Version**：`plugin.go` 中 `const pluginVersion` 的值，唯一版本源。
- **Snippet**：包内 `privacygateway/config.snippet.yaml`，需合并进 CPA `config.yaml`。
- **Readme**：包内 `privacygateway/README.md`，由 `packaging/README.md` 模板生成。
- **Checksums_File**：包内 `privacygateway/checksums.txt`，`sha256sum` 格式。
- **Panel**：单文件检测页面 `packaging/panel.html`，包内路径为 `privacygateway/panel.html`。
- **Plugin**：加载到 CPA 宿主中的 Plugin_Library 的运行时实例（入口层 main 包）。
- **Management_Handler**：Plugin 中处理 `management.register` 与 `management.handle` 的入口层组件（`management.go`）。
- **Panel_Route**：资源路由 `{Path:"panel", Menu:"隐私脱敏检测", Description}`，宿主 URL 为 `/v0/resource/plugins/privacygateway/panel`。
- **Host**：CLIProxyAPI（CPA）v8.0.22 宿主进程。
- **Smoke_Tester**：cgo 宿主模拟器 `scripts/smoke`，由 `make smoke` 调用。
- **Host_Version_Rule**：宿主版本格式 `^[0-9][0-9A-Za-z.+-]*$`。
- **Makefile**：仓库根目录构建脚本，提供 `package`、`smoke`、`clean` 等目标。
- **Acceptance_Record**：`docs/exec-plans/active/plugin-packaging.md` 中的真实宿主验收记录。
- **Shutdown_State**：Plugin 收到 `shutdown` 调用后的状态（`abiState.shuttingDown` 为真）。

## Requirements

### Requirement 1: zip 包布局与命名

**User Story:** 作为 CPA 运维者，我想在 CPA 工作目录直接解压插件包，以便宿主自动发现插件且不覆盖我已有的文件。

#### Acceptance Criteria

1. THE Packager SHALL 只在 Package 中写入位于 `plugins/linux/amd64/` 或 `privacygateway/` 下的条目。
2. THE Packager SHALL 将 Plugin_Library 以文件名 `privacygateway-v<Plugin_Version>.so` 写入 Package 的 `plugins/linux/amd64/` 目录。
3. THE Packager SHALL 为 Plugin_Library 条目设置权限 0755，为其余条目设置权限 0644。
4. THE Packager SHALL 以 `/` 分隔、不含 `..` 的相对路径和常规文件类型写入每个 Package 条目。
5. THE Packager SHALL 将 Package 命名为 `privacygateway_<Plugin_Version>_linux_amd64.zip`。
6. THE Packager SHALL 在 Package 中包含 `privacygateway/README.md`、`privacygateway/config.snippet.yaml`、`privacygateway/panel.html` 和 `privacygateway/checksums.txt` 四个文件。

### Requirement 2: 版本单一来源与 ID 一致性

**User Story:** 作为插件维护者，我想让版本号和插件 ID 只有一个来源，以便宿主推导出的 ID、元数据名称和配置键始终一致。

#### Acceptance Criteria

1. THE Packager SHALL 通过 `go/parser` 从 `plugin.go` 的 `pluginVersion` 常量读取 Plugin_Version。
2. IF `pluginMetadata().Name` 不等于 Plugin_ID，THEN THE Packager SHALL 以非 0 退出码终止并在 stderr 输出不一致原因。
3. THE Packager SHALL 使 Plugin_Library 文件名经宿主 `pluginFileFromPath` 规则推导出的 ID 等于 Plugin_ID、推导出的版本等于 Plugin_Version。
4. IF Plugin_Version 不匹配 Host_Version_Rule，THEN THE Packager SHALL 以非 0 退出码终止并在 stderr 输出版本格式原因。
5. IF 目标平台参数不是 `linux/amd64`，THEN THE Packager SHALL 以非 0 退出码终止并在 stderr 提示仅支持 `linux/amd64`。

### Requirement 3: 可复现性与校验和

**User Story:** 作为 CPA 运维者，我想校验插件包的完整性，以便确认收到的文件未被篡改或损坏。

#### Acceptance Criteria

1. WHEN Packager 对相同输入文件执行两次，THE Packager SHALL 产出 SHA-256 相同的 Package。
2. THE Packager SHALL 在 Checksums_File 中以 `sha256sum` 格式记录 Plugin_Library、Readme、Snippet 与 Panel 的 SHA-256。
3. WHEN 运维者在 Package 解压目录执行 `sha256sum -c privacygateway/checksums.txt`，THE Checksums_File SHALL 使全部条目校验通过。

### Requirement 4: 输出位置、输入校验与裸库交付

**User Story:** 作为插件维护者，我想在当前文件夹直接拿到 zip 和裸 `.so`，以便手动分发或直接放入宿主插件目录。

#### Acceptance Criteria

1. WHEN `make package` 成功完成，THE Packager SHALL 将 Package 写入当前文件夹 `./privacygateway_<Plugin_Version>_linux_amd64.zip`。
2. THE Standalone_Library SHALL 以文件名 `privacygateway-v<Plugin_Version>.so` 存放在当前文件夹，且内容与 Package 内 Plugin_Library 相同。
3. WHEN Packager 成功写入 Package，THE Packager SHALL 在 stdout 输出 Package 路径、字节数与 SHA-256。
4. IF 输入共享库不存在、不是 ELF x86-64 或缺少 `cliproxy_plugin_init` 导出符号，THEN THE Packager SHALL 以非 0 退出码终止并在 stderr 输出原因。
5. WHEN 执行 `make clean`，THE Makefile SHALL 只清理 `build/` 与 `coverage.out`，并保留当前文件夹中的 Package 与 Standalone_Library。

### Requirement 5: 安装与启用说明

**User Story:** 作为 CPA 运维者，我想按包内说明完成安装、启用、验证、升级和卸载，以便不阅读宿主源码也能让插件生效。

#### Acceptance Criteria

1. THE Packager SHALL 由 `config.example.yaml` 机械缩进派生 Snippet 中 `plugins.configs.privacygateway` 下的插件参数。
2. THE Snippet SHALL 包含 `config.example.yaml` 中的每一个顶层插件参数键。
3. THE Snippet SHALL 将 `plugins.enabled` 与 `plugins.configs.privacygateway.enabled` 设为 `true`，并在文件头注释说明须合并进 `config.yaml` 而非整份替换。
4. THE Readme SHALL 包含前置条件、安装、验证、升级、卸载与已知限制六个章节。
5. THE Packager SHALL 将 Readme 模板中的 `{{VERSION}}` 与 `{{SO_PATH}}` 替换为 Plugin_Version 与 Plugin_Library 包内路径。
6. THE Readme SHALL 在验证章节按 `plugins.enabled` → 配置键 → 文件名 → 工作目录 → glibc 的顺序给出未加载时的排查步骤。
7. THE Readme SHALL 在升级章节提示删除旧版 `privacygateway-v*.so` 或用 `store.version` 固定版本。

### Requirement 6: 独立检测面板

**User Story:** 作为 CPA 运维者，我想用一个单文件页面检查插件状态并测试脱敏与还原，以便快速判断插件是否真正生效。

#### Acceptance Criteria

1. THE Panel SHALL 以单个 HTML 文件交付，且不引用任何外部脚本、样式或图片资源。
2. WHEN 用户执行状态检查，THE Panel SHALL 调用 `GET /v0/management/plugins`，并在该调用失败时回退调用 `/v8` 前缀的同一接口。
3. WHEN 状态检查返回的插件列表包含 Plugin_ID，THE Panel SHALL 显示该插件的加载状态与启用状态。
4. WHEN 用户执行一键启用，THE Panel SHALL 调用 `PATCH /v0/management/plugins/privacygateway/enabled`，并在成功后重新执行状态检查。
5. WHEN 用户执行脱敏测试，THE Panel SHALL 发送含合成敏感值的逐字符回显提示词，并显示每个合成敏感值以明文还是占位符形式到达上游。
6. WHEN 用户执行还原测试，THE Panel SHALL 显示客户端收到的响应中占位符是否被还原为原值。
7. WHILE Panel 经 `http:` 或 `https:` 协议加载，THE Panel SHALL 以 `window.location.origin` 作为默认 CPA 地址。
8. WHILE Panel 经 `file:` 协议打开，THE Panel SHALL 以 `http://127.0.0.1:8317` 作为默认 CPA 地址。
9. THE Panel SHALL 只向用户配置的 CPA 地址发起数据请求，并使用用户在页面中输入的管理密钥或 API Key。

### Requirement 7: 管理能力声明与资源路由注册

**User Story:** 作为 CPA 运维者，我想在 CPA 管理界面看到插件的菜单项，以便不再出现"未找到插件页面"的提示。

#### Acceptance Criteria

1. WHEN Host 调用 `plugin.register`，THE Plugin SHALL 在响应 `capabilities` 中返回 `management_api: true`。
2. WHEN Host 调用 `management.register`，THE Management_Handler SHALL 返回 JSON 对象 `{"routes":[],"resources":[Panel_Route]}`。
3. THE Management_Handler SHALL 以无 JSON tag 的字段名 `Path`、`Menu`、`Description` 编码 Panel_Route，并省略 `Handler` 字段。
4. THE Management_Handler SHALL 将 Panel_Route 的 `Path` 设为 `panel`、`Menu` 设为 `隐私脱敏检测`，且 `Description` 为非空字符串。
5. WHEN Host 调用 `management.register` 或 `management.handle`，THE Plugin SHALL 在 `beginPluginCall` 与 fail-closed 路径之前分派该调用，使分派结果与插件配置和实例注册状态无关。
6. IF Plugin 处于 Shutdown_State 时收到 `management.register` 或 `management.handle`，THEN THE Management_Handler SHALL 返回错误信封。

### Requirement 8: 内嵌插件页面服务

**User Story:** 作为 CPA 运维者，我想在 CPA 管理界面内直接打开检测页面，以便无需单独保存或打开 HTML 文件。

#### Acceptance Criteria

1. THE Plugin SHALL 通过 `//go:embed packaging/panel.html` 嵌入 Panel，且嵌入字节与 `packaging/panel.html` 完全相同。
2. WHEN `management.handle` 收到 `Method` 为 GET 且 `Path` 归一化后为 `panel` 的请求，THE Management_Handler SHALL 返回 `StatusCode` 200 与嵌入的 Panel 字节。
3. WHEN Management_Handler 返回 Panel，THE Management_Handler SHALL 设置响应头 `Content-Type: text/html; charset=utf-8`、`X-Content-Type-Options: nosniff`、`Cache-Control: no-store` 与 `Content-Security-Policy: default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src *`。
4. WHEN `management.handle` 收到 `Method` 不是 GET 的请求，THE Management_Handler SHALL 返回 `StatusCode` 405 并设置响应头 `Allow: GET`。
5. WHEN `management.handle` 收到 `Method` 为 GET 且 `Path` 归一化后不是 `panel` 的请求，THE Management_Handler SHALL 返回 `StatusCode` 404。
6. THE Management_Handler SHALL 在归一化 `Path` 时去除首尾 `/`，并将 `/v0/resource/plugins/privacygateway/panel` 视为 `panel`。
7. IF `management.handle` 的请求体无法解码为 `ManagementRequest`，THEN THE Management_Handler SHALL 返回 `StatusCode` 400。
8. IF Management_Handler 在处理 `management.handle` 时发生 panic，THEN THE Management_Handler SHALL 恢复该 panic 并返回 `StatusCode` 500，使 Host 不熔断 Plugin。
9. THE Management_Handler SHALL 以 Base64 编码 `ManagementResponse.Body`，并以 `map[string][]string` 形状编码 `ManagementResponse.Headers`。

### Requirement 9: 交付物冒烟测试

**User Story:** 作为插件维护者，我想在发布前用模拟宿主加载真正的交付物，以便在交给用户前发现加载或注册问题。

#### Acceptance Criteria

1. WHEN 执行 `make smoke`，THE Smoke_Tester SHALL 以 `dlopen(RTLD_NOW|RTLD_LOCAL)` 加载 Package 解压到临时目录后的 Plugin_Library。
2. WHEN Smoke_Tester 加载 Plugin_Library，THE Smoke_Tester SHALL 断言 `cliproxy_plugin_init` 存在、init 返回 0、`abi_version` 等于 1，且 `call`、`free_buffer`、`shutdown` 均非空。
3. WHEN Smoke_Tester 调用 `plugin.register`，THE Smoke_Tester SHALL 断言 `ok` 为 true、`schema_version` 在 1 到 6 之间、`Name/Version/Author/GitHubRepository` 均非空、`management_api` 为 true。
4. WHEN Smoke_Tester 以含 `user@example.com` 的正文调用 `request.intercept_before`，THE Smoke_Tester SHALL 断言返回正文包含 `[[PG_` 且不包含 `user@example.com`。
5. WHEN Smoke_Tester 调用 `management.handle` 请求 `panel`，THE Smoke_Tester SHALL 断言 `StatusCode` 为 200 且正文以 `<!doctype html` 开头（不区分大小写）。
6. IF 任一冒烟步骤断言失败，THEN THE Smoke_Tester SHALL 以非 0 退出码终止，输出步骤名与信封错误码，且不回显请求正文。
7. WHEN 全部冒烟步骤完成，THE Smoke_Tester SHALL 依次调用 `request.complete`、`shutdown` 与 `dlclose`，并删除临时解压目录。

### Requirement 10: 真实宿主验收

**User Story:** 作为插件维护者，我想在真实 CPA 上记录验收结果，以便只有在证据齐全时才对外宣称插件可用。

#### Acceptance Criteria

1. WHEN Plugin_Library 已替换且 Host 已重启，THE Host SHALL 在 `GET /v0/management/plugins` 中为 Plugin_ID 返回 `menus: [{path:"/v0/resource/plugins/privacygateway/panel", menu:"隐私脱敏检测"}]`。
2. WHEN 运维者在 CPA 管理界面打开 Plugin_ID 的插件页面，THE Host SHALL 显示 Panel，而非"未找到插件页面"提示。
3. WHILE 真实宿主验收未通过，THE Readme 与 `docs/QUALITY_SCORE.md` SHALL 标注"真实宿主待验"，而非"已在 CPA 上验证"。
4. WHEN 一次真实宿主验收执行完毕，THE Acceptance_Record SHALL 包含该次验收的加载日志结论、`menus` 字段、内嵌页面打开情况与脱敏测试结果。
5. WHEN 管理界面显示"未找到插件页面"，THE Readme SHALL 说明该提示仅表示 Plugin 未声明菜单，须以 Panel 状态检查判定 Plugin 是否加载与启用。

### Requirement 11: 已知限制声明

**User Story:** 作为 CPA 运维者，我想在安装前知道插件的运行限制，以便判断我的环境是否适用。

#### Acceptance Criteria

1. THE Readme SHALL 声明运行环境须满足 glibc ≥ 2.34。
2. THE Readme SHALL 声明 Host 须为 cgo 构建，非 cgo 构建的 Host 不加载任何插件。
3. THE Readme SHALL 声明 Package 仅支持 `linux/amd64`。
4. THE Readme SHALL 声明流尾残缺占位符 flush 仍未闭合（TD-CPA-032 残留）。
5. THE Readme SHALL 声明 Host 启用 Home 模式时资源路由不可用，此时须改用包内 `privacygateway/panel.html` 独立打开。
