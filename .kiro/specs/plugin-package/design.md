# 设计文档：CPA 插件发布包（Linux amd64）

## Overview

目标：产出一个 zip 插件包。用户在 CPA 工作目录解压、把配置片段合并进 `config.yaml` 并重启 CPA 后，宿主自动发现、加载并启用隐私脱敏网关；同时在 CPA 管理界面中提供一个内嵌的插件页面，用于检测插件状态与脱敏效果。

已确认决策：

| 编号 | 决策 | 取值 | 日期 |
|---|---|---|---|
| PK-1 | 目标平台 | 仅 `linux/amd64` | 2026-10-10 |
| PK-2 | 包结构 | 带 `plugins/<goos>/<goarch>/` 目录树，在 CPA 工作目录解压 | 2026-10-10 |
| PK-3 | 规格入口 | Design-first | 2026-10-10 |
| PK-4 | zip 输出位置 | 插件根目录 / 当前文件夹（`./privacygateway_<ver>_linux_amd64.zip`），不使用 `dist/` | 2026-10-10 |
| PK-5 | 裸 `.so` 交付 | 额外复制 `./privacygateway-v<ver>.so` 到当前文件夹，文件名不得改动 | 2026-10-10 |
| PK-6 | 检测面板 | 包内附单文件 `panel.html`（仅 zip 分发、`file://` 排障）；管理界面内嵌页已由 RT-1 的 `trace.html`（菜单「隐私脱敏日志」）取代，`panel.html` 不再嵌入二进制 | 2026-10-11（2026-10-21 RT-1 调整） |

非目标：插件商店（store）格式、GitHub Release 自动发布、arm64/macOS/Windows、修改宿主、改动脱敏业务逻辑、插件管理 API 写操作（插件只提供只读静态页面）。

### 当前进度（2026-10-11）

| 项 | 状态 | 说明 |
|---|---|---|
| `make package` | 已实现 | `scripts/package/main.go`；两次构建 sha256 相同；解压后 `sha256sum -c` 通过 |
| 裸 `.so` | 已交付 | `./privacygateway-v0.1.0.so` |
| `packaging/panel.html` | 已实现 | 独立单文件面板，见组件 3.2 |
| `make smoke`（cgo） | 未实现 | 曾用一次性 Python ctypes 脚本验证：init `abi_version=1`、register `schema_version=6`、元数据字段非空、邮箱被替换为占位符 |
| 插件内嵌页面 | 已实现（RT-1 重定向） | 由 `request-trace-log` 规格以 `trace.html` 替代原面板方案，见该规格 design.md；本文件组件 6/数据模型保留原面板设计仅作历史参照 |
| 真实宿主验收 | 未通过 | 用户用逐字符回显提示词测试，5 个敏感值全部以明文到达上游，插件当时未生效，根因未定位 |

### 宿主加载规则（CPA v8.0.22 源码事实）

以下 `$H` = `github.com/router-for-me/CLIProxyAPI/v8@v8.0.22`。

| 事实 | 依据 | 对设计的约束 |
|---|---|---|
| 插件 ID 由**文件名**推导，不读 manifest | `$H/internal/pluginhost/platform.go` `pluginFileFromPath` | 文件名必须为 `privacygateway[-v<ver>].so`；`libprivacygateway.so` 会得到 ID `libprivacygateway`，与配置键 `privacygateway` 不匹配 |
| `-v<ver>` 后缀被解析为版本；版本不得以 `v` 开头，须匹配 `^[0-9][0-9A-Za-z.+-]*$` | 同上 `validPluginVersion` | 采用 `privacygateway-v0.1.0.so` |
| 扫描目录依次为 `<dir>/<GOOS>/<GOARCH>`、`<dir>`；只扫平铺常规文件，跳过符号链接与子目录 | 同上 `candidateDirs`、`selectPluginFilesWithCandidates` | 库文件放 `plugins/linux/amd64/`，必须是常规文件 |
| 同 ID 多个文件时取最高版本；带版本优先于不带版本 | 同上 `pluginFilePreferred` | 升级时旧版本可共存，安装说明须提示清理 |
| `plugins.dir` 默认 `plugins`，相对 CPA 进程工作目录；支持 `~` | `$H/internal/config/plugin_path.go` | 说明文档要求在 CPA 工作目录解压，或设置 `plugins.dir` |
| 需 `plugins.enabled: true` 且 `plugins.configs.<id>.enabled: true` 才加载 | `$H/internal/pluginhost/config.go` | 必须附配置片段，插件无法自行启用 |
| `plugins.configs.<id>` 整个子树作为 `config_yaml` 传给插件 | 同上 `runtimeConfigYAML` | 片段中插件参数与 `enabled/priority` 平级 |
| 加载检查：`dlopen(RTLD_NOW)`、`cliproxy_plugin_init` 符号、init 返回 0、`abi_version == 1`、`call/free_buffer` 非空 | `$H/internal/pluginhost/loader_unix.go` | 冒烟测试按同序验证 |
| register 检查：`schema_version <= 6`；`Name/Version/Author/GitHubRepository` 非空；至少一个能力 | `$H/internal/pluginhost/rpc_client.go`、`host.go` `validPlugin` | 冒烟测试断言 register 信封 |
| 宿主需 cgo 构建，否则不加载任何插件 | `loader_unix.go` build tag | README 列为前置条件 |
| 管理 API 跨域：`corsMiddleware` 设置 `Access-Control-Allow-Origin: *` | 宿主管理路由 | 独立面板以 `file://` 打开也能调用管理 API |

当前产物事实（本机 `make build`）：动态依赖仅 `libc.so.6`；最高 glibc 符号版本 `GLIBC_2.34`；导出 `cliproxy_plugin_init` 等 4 个符号；`pluginVersion = "0.1.0"`（`plugin.go:17`）；`abi.go` 当前 496 行（上限 500）。

## Architecture

```text
plugin.go: pluginVersion ──(单一版本源)──┐
                                        ▼
make package ─► make build (c-shared, 不变)
             ├► scripts/package (Go 程序，入口层工具，不进 redact)
             │     1. 读 pluginVersion，校验版本格式；校验 pluginMetadata().Name == pluginID
             │     2. 校验 build/libprivacygateway.so 为 ELF x86-64 且导出 cliproxy_plugin_init
             │     3. 复制库 → zip 内 plugins/linux/amd64/privacygateway-v<ver>.so (0755)
             │     4. 生成 config.snippet.yaml（由 config.example.yaml 派生）
             │     5. 复制 packaging/README.md（替换 {{VERSION}} / {{SO_PATH}}）
             │     6. 复制 packaging/panel.html
             │     7. 生成 checksums.txt（sha256sum 格式，覆盖以上 4 个文件）
             │     8. 打 zip（固定时间戳、排序条目，可复现）
             └► ./privacygateway_<ver>_linux_amd64.zip（当前文件夹，PK-4）

裸 .so ─► ./privacygateway-v<ver>.so（PK-5，文件名与 zip 内一致）

make smoke（未实现）─► scripts/smoke (cgo 宿主模拟器)
              dlopen 解压后的 .so → init → register → 一次请求脱敏 → management → shutdown

CPA 宿主 ─► plugin.register（capabilities.management_api=true）
         ─► management.register ─► routes=[]，resources=[{Path:"panel", Menu:"隐私脱敏检测"}]
         ─► GET /v0/resource/plugins/privacygateway/panel ─► management.handle ─► 内嵌 panel.html
```

`build/` 产物命名保持不变（`libprivacygateway.so` 已被现有 CI 和测试引用），仅在打包阶段重命名，变更面最小。

## Components and Interfaces

### 1. 版本单一来源

- `plugin.go` 的 `const pluginVersion` 是唯一版本源。
- 打包脚本用 `go/parser` 读取该常量，不用正则。版本不满足宿主 `validPluginVersion` 时退出码非 0。
- Makefile 不重复定义版本号。

### 2. `scripts/package/main.go`（已实现）

| 参数 | 默认 | 说明 |
|---|---|---|
| `-lib` | `build/libprivacygateway.so` | 输入共享库 |
| `-out` | `.` | 输出目录（当前文件夹，PK-4） |
| `-goos/-goarch` | `linux`/`amd64` | 只接受 PK-1 的组合，其他组合报错 |

职责与约束：
- 输入库不存在、不是 ELF x86-64，或缺少 `cliproxy_plugin_init` 导出（用 `debug/elf` 检查）时失败。
- 从 `abi.go` 校验 `pluginMetadata().Name` 等于 `pluginID`，不一致时失败。
- zip 条目使用 `/` 分隔、相对路径、无 `..`、常规文件；`.so` 权限 0755，其余 0644。
- 条目按名称排序，修改时间固定，保证同输入字节级可复现。
- 成功时 stdout 输出 zip 路径、字节数与 sha256；失败时 stderr 输出原因、非 0 退出。

### 3. 包内容

```text
privacygateway_0.1.0_linux_amd64.zip
├── plugins/linux/amd64/privacygateway-v0.1.0.so
├── privacygateway/README.md
├── privacygateway/config.snippet.yaml
├── privacygateway/panel.html
└── privacygateway/checksums.txt
```

说明文件放在 `privacygateway/` 子目录，避免在 CPA 根目录散落 `README.md`，也不会覆盖用户已有文件。`plugins/` 下只放库文件，宿主不会扫到其他文件。

#### 3.1 `config.snippet.yaml`

需合并进 CPA 的 `config.yaml`，而不是整份替换：

```yaml
plugins:
  enabled: true
  # dir: plugins          # 默认值；相对 CPA 工作目录
  configs:
    privacygateway:
      enabled: true
      priority: 0
      fail_closed: true
      # 其余参数见 README，全部可省略
```

派生规则：插件参数部分由 `config.example.yaml` 机械缩进生成，保证键集合与示例一致；文件头注释说明合并方式。

`checksums.txt`：`sha256sum` 格式，覆盖 `.so`、README、snippet、panel.html，可用 `sha256sum -c` 校验。

`README.md`（`packaging/README.md` 模板）：
1. 前置条件：CPA v8.0.22 及兼容 ABI 1 / schema ≤6 的版本、cgo 构建的宿主、glibc ≥ 2.34、linux/amd64。
2. 安装：在 CPA 工作目录执行 `unzip`，校验 checksums，合并 snippet，重启。
3. 验证：从宿主日志或管理 API 确认插件已加载；用面板或发送含合成邮箱的请求，确认上游收到的是占位符。
4. 升级：删除旧版 `privacygateway-v*.so`，或在配置中用 `store.version` 固定版本。
5. 卸载：删除库文件，并将 `plugins.configs.privacygateway.enabled` 设为 false。
6. 已知限制：流尾残缺占位符 flush 仍未闭合（TD-CPA-032 残留）；尚未在真实宿主上完成端到端验收（直到验收记录补齐）。

#### 3.2 `panel.html`（独立检测面板，已实现）

单文件 HTML，无外部资源依赖。用户输入 CPA 地址、管理密钥、API Key 后：

| 功能 | 调用 | 判定 |
|---|---|---|
| 状态检查 | `GET /v0/management/plugins`，失败时回退 `/v8/...` | 列表中存在 `privacygateway` 且已加载、已启用 |
| 一键启用 | `PATCH /v0/management/plugins/privacygateway/enabled` | 返回成功后重新执行状态检查 |
| 脱敏测试 | 发送含合成敏感值的逐字符回显提示词 | 模型回显的是占位符而不是原值 |
| 还原测试 | 让模型原样复述占位符 | 客户端收到的响应中占位符被还原为原值 |

默认 CPA 地址：
- 经 `http(s)` 加载（即由 CPA 资源路由提供，组件 6）时，默认值取 `window.location.origin`。
- 经 `file://` 打开时，默认值保持 `http://127.0.0.1:8317`。

跨域：宿主 `corsMiddleware` 设置 `Access-Control-Allow-Origin: *`，`file://` 打开时也能调用管理 API。

### 4. `scripts/smoke`（C 加载冒烟测试，未实现）

- 用 cgo 编写的宿主模拟器，`dlopen(RTLD_NOW|RTLD_LOCAL)` 加载 **zip 解压后**的 `.so`（不用 `build/` 下的文件，确保验证的是交付物）。
- 步骤与断言：
  1. `dlsym("cliproxy_plugin_init")` 非空；init 返回 0；`abi_version == 1`；`call/free_buffer/shutdown` 非空。
  2. 调用 `plugin.register`，载荷为 `config_yaml`（Base64）与 `plugin_dir`；断言 `ok=true`、`schema_version` 在 1..6、元数据 4 个必填字段非空、至少一个能力为 true、`management_api=true`。
  3. 调用 `request.intercept_before`，正文含合成邮箱 `user@example.com`；断言返回正文含 `[[PG_`、不含原邮箱。
  4. 调用 `management.register`，断言资源路由含 `panel`；调用 `management.handle` GET `panel`，断言状态 200 且正文以 `<!doctype html` 开头（不区分大小写）。
  5. 宿主日志回调 `host.log` 由模拟器实现，记录调用但不回显内容。
  6. `request.complete` → `shutdown` → `dlclose`，无崩溃。
- 结构体布局复用 `abi.go` 中的 C 定义（复制到 smoke 的 cgo 前导），并在注释中标注来源，防止漂移。
- 宿主 ID 推导结果由一个纯 Go 单元测试覆盖：在测试里复刻 `pluginFileFromPath` 规则，断言包内文件名得到 ID `privacygateway`、版本 `0.1.0`。
- 临时替代：一次性 Python ctypes 脚本已验证步骤 1~3（不入库，不算门禁）。

### 5. Makefile 目标

| 目标 | 状态 | 行为 |
|---|---|---|
| `package` | 已实现 | `build` → `go run ./scripts/package -lib build/libprivacygateway.so -out .` |
| `smoke` | 未实现 | `package` → 解压到临时目录 → `go run ./scripts/smoke -lib <解压路径>` → 清理临时目录 |
| `clean` | 不变 | 清理 `build/` 与 `coverage.out`；不删除当前文件夹下的 zip 与裸 `.so`（属于交付物） |

`make check` 暂不串联 `package/smoke`，避免拖慢日常门禁；发布前手动执行。`.gitignore` 已忽略 `*.so` 与 `privacygateway_*.zip`。

### 6. CPA 管理界面内嵌插件页面

#### 6.1 问题与根因

用户在 CPA 管理界面看到"未找到插件页面 / 该插件页面未在当前后端插件列表中声明"。

根因：插件没有声明任何管理路由或资源路由，宿主插件列表中该插件的 `menus` 为空，界面找不到可打开的页面。这条提示**本身不能证明插件加载失败**，插件条目很可能已在列表中。插件是否生效需单独用状态检查判断。

#### 6.2 宿主事实（CPA v8.0.22）

| 事实 | 依据 |
|---|---|
| register 响应 `capabilities` 含 `management_api` 布尔字段 | `$H/internal/pluginhost/rpc_schema.go:47` |
| `management_api=true` 时，宿主调用 RPC `management.register`（`pluginabi.MethodManagementRegister`），请求体为 `pluginapi.ManagementRegistrationRequest{Plugin, BasePath="/v0/management", ResourceBasePath="/v0/resource/plugins/<id>"}` | `$H/internal/pluginhost/rpc_client.go:619-634` |
| 插件返回 `{"routes":[...],"resources":[...]}`（`rpcManagementRegistrationResponse`） | `rpc_schema.go:131-134` |
| `ResourceRoute` 字段无 JSON tag：`Path`、`Menu`、`Description`；`Handler` 由宿主设置，插件不得编码 | 同上 |
| 资源路由只支持 GET，挂在 `/v0/resource/plugins/<pluginID>/<path>`；`path` 不得含 `:`、`*`、`..`、空白 | `management.go` `normalizeResourceRoute`、`ServeResourceHTTP:291-331` |
| 资源请求**不经过**管理鉴权；`cfg.Home.Enabled` 时资源路由被禁用 | 同上 |
| 管理界面菜单来自 `Menu` 非空的资源路由，经 `GET /v0/management/plugins` 的 `menus` 字段暴露 | `$H/internal/pluginhost/snapshot.go:123-146` |
| 资源请求经 RPC `management.handle` 转发，请求体为 `pluginapi.ManagementRequest{Method, Path, Headers, Query, Body}`（无 JSON tag，`Body` 为 `[]byte` 即 Base64）外加 `host_callback_id` | `rpc_client.go` |
| 插件返回 `pluginapi.ManagementResponse{StatusCode, Headers map[string][]string, Body []byte(Base64)}` | 同上 |
| `schema_version=6` ≥ `SchemaVersionRawManagementResponse(6)`，响应正文不做 HTML 转义 | `rpc_schema.go` |
| handler panic 时宿主 `callResourceHandler` recover 后调用 `fusePlugin` 熔断整个插件 | `management.go` |

#### 6.3 设计

新增文件 `management.go`（main 包，入口层）。`abi.go` 已 496 行，接近 500 行上限，只在其中做最小改动。

```go
//go:embed packaging/panel.html
var panelHTML []byte

const (
    panelResourcePath = "panel"
    panelMenu         = "隐私脱敏检测"
    panelDescription  = "检测隐私脱敏网关的加载状态与脱敏/还原效果"
)

// 宿主 ResourceRoute 无 JSON tag，字段名按 Go 默认编码；不含 Handler。
type abiResourceRoute struct {
    Path        string
    Menu        string
    Description string
}

type abiManagementRegistrationResponse struct {
    Routes    []struct{}         `json:"routes"`    // 恒为空数组
    Resources []abiResourceRoute `json:"resources"`
}

// handleABIManagement 处理 management.register / management.handle。
func handleABIManagement(ctx context.Context, method string, request []byte) ([]byte, error)
```

`abi.go` 改动：
- `abiCapabilities` 增加 `ManagementAPI bool \`json:"management_api"\``，注册时置为 `true`。
- `handleABIMethod` 第一个 `switch` 增加 `case pluginabi.MethodManagementRegister, pluginabi.MethodManagementHandle: return handleABIManagement(ctx, method, request)`，位于 `beginPluginCall` 与 fail-closed 路径之前。

分发规则：
- 页面是静态内容，与插件配置、实例状态无关，因此不走 `beginPluginCall`，不受 fail-closed 影响，未注册或重配间隙也能提供页面。
- `abiState.shuttingDown` 为真时拒绝，返回错误信封。
- `management.register` 返回 `routes=[]`、`resources=[{Path:"panel", Menu:"隐私脱敏检测", Description:...}]`。
- `management.handle`：

| 条件 | StatusCode | 正文 |
|---|---|---|
| `Method == GET` 且 `Path` 归一化后为 `panel` | 200 | `panelHTML` |
| `Method != GET` | 405（`Allow: GET`） | 简短纯文本 |
| 其他路径 | 404 | 简短纯文本 |
| 请求体无法解码 | 400 | 简短纯文本 |

`Path` 归一化：去掉首尾 `/`，并兼容宿主传入完整前缀 `/v0/resource/plugins/privacygateway/panel` 的形式。

200 响应头：

| 头 | 值 |
|---|---|
| `Content-Type` | `text/html; charset=utf-8` |
| `X-Content-Type-Options` | `nosniff` |
| `Cache-Control` | `no-store` |
| `Content-Security-Policy` | `default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src *` |

不 panic 约束：handler 只做解码、比较与字节返回；用 `defer recover()` 兜底，恢复后返回 500 响应而不是让宿主熔断。

#### 6.4 panel.html 改动

默认 CPA 地址按组件 3.2 规则取值：`location.protocol` 为 `http:` 或 `https:` 时用 `window.location.origin`，否则保持 `http://127.0.0.1:8317`。嵌入的字节与 `packaging/panel.html` 完全一致，不另维护副本。

#### 6.5 安全

- 页面为静态内容，不含任何密钥、配置或运行时数据；资源路由不经管理鉴权是宿主设计，暴露面仅为这份静态 HTML。
- 页面发起的所有数据调用仍需用户手动输入管理密钥或 API Key，且只发往用户配置的 CPA 地址。
- CSP 禁止加载外部脚本、样式、图片与框架；`connect-src *` 是为了支持用户填写非同源 CPA 地址。
- `cfg.Home.Enabled` 时宿主禁用资源路由，页面不可用，此时用户可改用包内 `panel.html` 独立打开。

#### 6.6 验收（真实宿主，尚未执行）

替换 `.so` 并重启 CPA 后：
1. `GET /v0/management/plugins` 中 `privacygateway` 的 `menus` 为 `[{path:"/v0/resource/plugins/privacygateway/panel", menu:"隐私脱敏检测"}]`。
2. 管理界面打开该插件页面，不再显示"未找到插件页面"。
3. 在内嵌页面执行状态检查，用于诊断此前明文泄露测试的根因。

## Data Models

| 名称 | 形状 | 来源 |
|---|---|---|
| 版本 | 字符串，例 `0.1.0` | `plugin.go` `pluginVersion` |
| 插件 ID | 常量 `privacygateway` | 与 `pluginMetadata().Name`、配置键一致；打包脚本断言三者相等 |
| 包名 | `privacygateway_<ver>_linux_amd64.zip` | 与宿主 store 资产命名 `<id>_<ver>_<goos>_<goarch>.zip` 同构 |
| 库路径 | `plugins/linux/amd64/privacygateway-v<ver>.so` | PK-2 |
| 裸库 | `./privacygateway-v<ver>.so` | PK-5 |
| 资源路由 | ~~`{Path:"panel", Menu:"隐私脱敏检测"}`~~ → `{Path:"trace", Menu:"隐私脱敏日志"}`（RT-1，2026-10-21） | 组件 6 已由 `request-trace-log` 规格取代 |
| 页面 URL | ~~`/v0/resource/plugins/privacygateway/panel`~~ → `/v0/resource/plugins/privacygateway/trace`；数据只走 `/v0/management/privacygateway/traces` | 宿主 `ResourceBasePath` + `Path` |

## Correctness Properties

*属性是在系统所有合法执行中都应成立的特征，是对系统应做什么的形式化陈述，连接人类可读的规格与机器可验证的正确性保证。*

### Property 1: ID 一致性

对于任意合法版本号 v，包内库文件名 `privacygateway-v<v>.so` 经宿主 `pluginFileFromPath` 规则推导出的 ID 等于 `pluginMetadata().Name`，也等于 snippet 中的配置键，推导出的版本等于 v。

**Validates: Requirements 1.2, 2.3**

### Property 2: 版本格式校验

对于任意字符串 s，打包脚本接受 s 作为版本当且仅当 s 匹配 `^[0-9][0-9A-Za-z.+-]*$`。

**Validates: Requirements 2.4**

### Property 3: 可复现性

对于任意相同的输入文件集合，两次构建 zip 得到的字节序列（及 SHA-256）相同。

**Validates: Requirements 3.1**

### Property 4: zip 条目安全与布局

对于任意打包产物，所有条目均为 `/` 分隔的相对路径、不含 `..`、均为常规文件，且只位于 `plugins/linux/amd64/` 或 `privacygateway/` 下；`.so` 权限为 0755，其余为 0644。

**Validates: Requirements 1.1, 1.3, 1.4**

### Property 5: 校验和完整性

对于任意打包产物，`checksums.txt` 中每一行的 sha256 都等于对应 zip 条目内容的 sha256，且覆盖除自身以外的全部条目。

**Validates: Requirements 3.2, 3.3**

### Property 6: snippet 键集合一致

对于 `config.example.yaml` 中的任意顶层插件参数键，该键都出现在 `config.snippet.yaml` 的 `plugins.configs.privacygateway` 下。

**Validates: Requirements 5.1, 5.2**

### Property 7: 资源请求分派

对于任意 `ManagementRequest`（任意方法、路径），`management.handle` 不 panic，并且：方法为 GET 且路径归一化为 `panel` 时返回 200 与 `panelHTML`；方法非 GET 时返回 405；其他路径返回 404。

**Validates: Requirements 8.2, 8.4, 8.5, 8.6**

### Property 8: 面板默认地址

对于任意页面来源 URL，协议为 `http:`/`https:` 时默认 CPA 地址等于 `window.location.origin`，否则等于 `http://127.0.0.1:8317`。

**Validates: Requirements 6.7, 6.8**

### Property 9: 管理响应编码往返

对于任意 `StatusCode`、任意 `map[string][]string` 响应头与任意字节正文，Management_Handler 编码出的 `ManagementResponse` 经宿主 `pluginapi.ManagementResponse` 类型解码后，`StatusCode`、`Headers`、`Body` 与原值相等。

**Validates: Requirements 8.9**

## Error Handling

| 场景 | 行为 |
|---|---|
| 版本格式非法 / ID 不一致 / 缺导出符号 / 非 x86-64 ELF / 输入库缺失 | 打包失败，非 0 退出，stderr 输出原因 |
| 非 linux/amd64 参数 | 打包失败，并提示 PK-1 |
| smoke 任一步断言失败 | 非 0 退出，输出步骤名与信封错误码，不回显请求正文 |
| 用户未启用配置 | 宿主不加载；README 的"验证"一节给出排查顺序（`plugins.enabled` → 配置键 → 文件名 → 工作目录 → glibc） |
| `management.*` 在 shutdown 后调用 | 返回错误信封，不访问已释放状态 |
| `management.handle` 请求无法解码 | 返回 400 响应，不 panic |
| `management.handle` 内部异常 | `recover` 后返回 500 响应，避免宿主熔断插件 |
| 宿主启用 Home 模式 | 资源路由不可用；README 提示改用包内 `panel.html` |
| 管理界面显示"未找到插件页面" | 不据此判定插件加载失败；先用面板状态检查确认 |

## Testing Strategy

| 层 | 内容 | 自动化 |
|---|---|---|
| 单元（打包） | 版本读取与校验、ID 推导复刻、snippet 键集合、zip 条目路径安全、校验和 | `make test` |
| 单元（管理页面） | `management.register` 响应 JSON 字段名为 `routes`/`resources`、资源路由字段为 `Path`/`Menu`/`Description` 且无 `Handler`；`management.handle` GET `panel` → 200 + HTML + 4 个响应头；未知路径 404；非 GET 405；shutdown 后拒绝；register 信封含 `management_api=true`；`panelHTML` 与 `packaging/panel.html` 字节相等 | `make test` |
| 属性 | Property 1~7、9 用 Go 标准库 `testing/quick` 或 `testing.F` 模糊测试，每个属性至少 100 次迭代 | `make test` |
| 交付物 | `make smoke`：dlopen → init → register → 脱敏 → management → shutdown | 手动 / 发布前（未实现） |
| 可复现 | 连续两次 `make package` 比较 SHA-256 | 已手动验证；后续并入 smoke |
| 真实宿主 | 用户在 CPA v8.0.22 上按 README 安装，记录加载日志、`menus` 字段、内嵌页面打开情况与一次脱敏请求 | 用户执行，结果写入执行计划 |

真实宿主验收通过之前，README 与 `QUALITY_SCORE.md` 都不得宣称"已在 CPA 上验证"。

## 未决事项

| 项 | 状态 | 下一步 |
|---|---|---|
| `make smoke`（cgo） | 未实现 | 按组件 4 实现，替代一次性 Python 脚本 |
| 真实宿主端到端验收 | 未通过 | 内嵌页面上线后按 6.6 执行 |
| 明文泄露根因 | 未定位 | 用面板状态检查确认插件是否加载、是否启用、`plugins.enabled` 是否为 true |

## 文档同步

- `docs/exec-plans/active/plugin-packaging.md` 记录 PK-1~PK-6 决策与验收进度，并在 `docs/index.md` 登记。
- `docs/design-docs/abi-contract.md` 补两节："宿主发现与命名规则"（宿主加载规则表）和"management.register / management.handle 线格式"（6.2 事实与 6.3 响应形状）。
- `AGENTS.md` 常用命令补 `make smoke`（实现后），目录地图补 `management.go`、`scripts/smoke`。
- `ARCHITECTURE.md` 入口层文件清单补 `management.go`。
- `QUALITY_SCORE.md` 端到端一行在 smoke 实现前保持"真实宿主待验"。
