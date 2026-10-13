# Implementation Plan: CPA 插件发布包（Linux amd64）

## Overview

打包器（`scripts/package/main.go`）、包内 README 模板、独立面板 `packaging/panel.html`、`make package` 目标与裸 `./privacygateway-v0.1.0.so` 已完成，本计划不再重复规划。剩余工作按以下顺序推进：

1. 插件内嵌管理页面（新文件 `management.go`，`abi.go` 仅最小改动，`abi.go` 当前 496 行，上限 500）。
2. 面板默认 CPA 地址改为 `window.location.origin`（`file:` 回退 `http://127.0.0.1:8317`）。
3. 打包器最小重构并补单元测试。
4. `make smoke`（cgo 宿主模拟器）。
5. 文档同步。
6. 重新打包并全量验证。
7. 真实宿主验收（用户手工执行，代理不得标记为已验证）。

硬性约束（AGENTS.md）：单文件 ≤ 500 行；main 包禁止 `fmt.Print*` / `log.Print*`，统一用 `logHost`；分层 main → redact 单向；运行 Go 前先 `export PATH=/home/gh/.local/toolchains/go/bin:$PATH`；门禁为 `make check`。

## Tasks

- [x] 1. 插件内嵌管理页面
  - [x] 1.1 新建 `management.go`：嵌入面板与资源路由注册
    - 添加 `//go:embed packaging/panel.html` 生成 `panelHTML`
    - 定义 `abiResourceRoute{Path, Menu, Description}`（无 JSON tag、无 `Handler`）与 `abiManagementRegistrationResponse{Routes []struct{} \`json:"routes"\`; Resources []abiResourceRoute \`json:"resources"\`}`
    - Panel_Route：`Path="panel"`、`Menu="隐私脱敏检测"`、`Description` 非空
    - 实现 `handleABIManagement` 分派 `pluginabi.MethodManagementRegister` 与 `pluginabi.MethodManagementHandle`；`abiState.shuttingDown` 为真时返回错误信封
    - _Requirements: 7.2, 7.3, 7.4, 7.6, 8.1_

  - [x] 1.2 在 `management.go` 实现 `management.handle`
    - 解码 `pluginapi.ManagementRequest`（`Body` 为 Base64 `[]byte`）与 `host_callback_id`
    - 路径归一化：去除首尾 `/`；`/v0/resource/plugins/privacygateway/panel` 视为 `panel`
    - GET + `panel` → 200 + `panelHTML`，头部 `Content-Type: text/html; charset=utf-8`、`X-Content-Type-Options: nosniff`、`Cache-Control: no-store`、`Content-Security-Policy: default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src *`
    - 非 GET → 405 + `Allow: GET`；其他路径 → 404；无法解码 → 400；`recover` panic → 500
    - 以 `pluginapi.ManagementResponse{StatusCode, Headers map[string][]string, Body []byte}` 包入 abi OK 信封
    - _Requirements: 8.2, 8.3, 8.4, 8.5, 8.6, 8.7, 8.8, 8.9_

  - [x] 1.3 最小修改 `abi.go` 接入管理分派与能力声明
    - `abiCapabilities` 增加 `ManagementAPI bool \`json:"management_api"\``，`registrationEnvelope` 中置 `true`
    - 在 `handleABIMethod` 第一个 switch（与 register 并列）加一个 case 分派到 `handleABIManagement`，位于 `beginPluginCall` 与 fail-closed 之前
    - 若 `abi.go` 将超过 500 行，把一小段无关代码移到别的文件
    - 验证 `wc -l abi.go` ≤ 500 且 `go build ./...` 通过
    - _Requirements: 7.1, 7.5_

  - [x] 1.4 编写 `management_test.go` 单元测试
    - register 响应 JSON 字段名：`routes`/`resources`，路由字段 `Path`/`Menu`/`Description`，不含 `Handler`
    - GET `panel` → 200 + HTML 正文 + 4 个响应头；完整 URL 路径与带首尾 `/` 的路径同样命中
    - 未知路径 404；非 GET 405 且 `Allow: GET`；无法解码 400；shutdown 后两个方法均返回错误信封
    - `plugin.register` 信封含 `management_api=true`
    - `panelHTML` 字节与磁盘 `packaging/panel.html` 相等
    - 插件未注册、无配置时 `management.register/handle` 仍可用
    - _Requirements: 7.1, 7.2, 7.3, 7.4, 7.5, 7.6, 8.1, 8.2, 8.3, 8.4, 8.5, 8.6, 8.7_

  - [ ]* 1.5 编写属性测试：资源请求分派
    - **Property 7: 资源请求分派**
    - 用 `testing/quick` 或 fuzz 生成任意方法与路径，断言不 panic，且 GET+`panel` → 200/`panelHTML`、非 GET → 405、其他路径 → 404；每个属性至少 100 次迭代
    - 标签：Feature: plugin-package, Property 7
    - **Validates: Requirements 8.2, 8.4, 8.5, 8.6**

  - [ ]* 1.6 编写属性测试：管理响应编码往返
    - **Property 9: 管理响应编码往返**
    - 对任意 `StatusCode`、`map[string][]string` 头与字节正文，编码后经 `pluginapi.ManagementResponse` 解码，三项与原值相等；至少 100 次迭代
    - 标签：Feature: plugin-package, Property 9
    - **Validates: Requirements 8.9**

- [x] 2. Checkpoint - 管理页面
  - Ensure all tests pass, ask the user if questions arise.

- [x] 3. 面板默认 CPA 地址
  - [x] 3.1 修改 `packaging/panel.html` 默认地址
    - `location.protocol` 为 `http:`/`https:` 时默认 `window.location.origin`，否则 `http://127.0.0.1:8317`
    - 保持单文件、无外部资源；其余行为不变
    - _Requirements: 6.1, 6.7, 6.8_

  - [x] 3.2 编写面板静态检查测试 `panel_test.go`
    - 断言 `panelHTML` 含默认地址选择逻辑（`window.location.origin` 与 `http://127.0.0.1:8317` 两个字面量）
    - 断言不含外部 `src=`/`href=` 的 `http(s)://` 资源引用
    - 与磁盘 `packaging/panel.html` 字节一致（沿用 1.4 的比较，但此处覆盖修改后的内容）
    - _Requirements: 6.1, 6.7, 6.8, 8.1_

- [x] 4. Checkpoint - 面板与内嵌页面
  - Ensure all tests pass, ask the user if questions arise.

- [x] 5. 打包器可测试性与单元测试
  - [x] 5.1 最小重构 `scripts/package/main.go`
    - 把版本读取/校验、宿主文件名→ID/版本推导复刻、snippet 生成、zip 构建、checksums 生成拆为可直接调用的函数；行为保持不变
    - 保持单文件 ≤ 500 行，必要时拆到同包新文件
    - 验证 `make package` 输出与重构前一致
    - _Requirements: 2.1, 2.3, 2.4, 3.2, 5.1_

  - [x] 5.2 编写版本与 ID 单元测试 `scripts/package/version_test.go`
    - 版本正则 `^[0-9][0-9A-Za-z.+-]*$` 的合法与非法样例（空串、`v1`、含空格、`..`）
    - `readVersion` 读取 `plugin.go` 的 `pluginVersion`
    - 宿主 `pluginFileFromPath` 推导复刻：`privacygateway-v<ver>.so` → ID `privacygateway`、版本 `<ver>`；`checkMetadataName` 不一致时报错
    - 非 `linux/amd64` 参数被拒绝
    - _Requirements: 1.2, 2.1, 2.2, 2.3, 2.4, 2.5_

  - [ ]* 5.3 编写属性测试：ID 一致性与版本格式
    - **Property 1: ID 一致性**
    - **Property 2: 版本格式校验**
    - 对任意合法版本推导 ID/版本一致；对任意字符串，接受当且仅当匹配正则；至少 100 次迭代
    - **Validates: Requirements 1.2, 2.3, 2.4**

  - [x] 5.4 编写 zip 与校验和单元测试 `scripts/package/zip_test.go`
    - 条目路径为 `/` 分隔相对路径、不含 `..`、常规文件、仅位于 `plugins/linux/amd64/` 或 `privacygateway/`
    - `.so` 权限 0755，其余 0644；包含 README、snippet、panel、checksums 四个文件；zip 命名正确
    - `checksums.txt` 覆盖除自身外全部条目且 sha256 与条目内容一致
    - 相同输入构建两次字节相同
    - 输入库缺失、非 ELF x86-64、缺 `cliproxy_plugin_init` 时失败并给出原因
    - _Requirements: 1.1, 1.3, 1.4, 1.5, 1.6, 3.1, 3.2, 3.3, 4.4_

  - [ ]* 5.5 编写属性测试：可复现、条目安全、校验和完整性
    - **Property 3: 可复现性**
    - **Property 4: zip 条目安全与布局**
    - **Property 5: 校验和完整性**
    - 用随机文件内容构造输入，断言三个性质；至少 100 次迭代
    - **Validates: Requirements 1.1, 1.3, 1.4, 3.1, 3.2, 3.3**

  - [x] 5.6 编写 snippet 测试 `scripts/package/snippet_test.go`
    - snippet 中 `plugins.configs.privacygateway` 下键集合是 `config.example.yaml` 顶层键的超集
    - `plugins.enabled` 与 `plugins.configs.privacygateway.enabled` 均为 `true`，文件头含"合并而非整份替换"注释
    - README 占位符 `{{VERSION}}`、`{{SO_PATH}}` 全部被替换，且含六个章节标题
    - _Requirements: 5.1, 5.2, 5.3, 5.4, 5.5_

  - [ ]* 5.7 编写属性测试：snippet 键集合一致
    - **Property 6: snippet 键集合一致**
    - 对随机生成的顶层键集合，派生 snippet 后均出现在 `plugins.configs.privacygateway` 下；至少 100 次迭代
    - **Validates: Requirements 5.1, 5.2**

- [x] 6. Checkpoint - 打包器测试
  - Ensure all tests pass, ask the user if questions arise.

- [x] 7. `make smoke` 交付物冒烟
  - [x] 7.1 新建 `scripts/smoke/main.go`（cgo 宿主模拟器）
    - 从 `make package` 产出的 zip 解压 Plugin_Library 到临时目录，`dlopen(RTLD_NOW|RTLD_LOCAL)` 加载
    - 断言：`cliproxy_plugin_init` 存在、init 返回 0、`abi_version == 1`、`call`/`free_buffer`/`shutdown` 非空
    - `plugin.register`：`ok=true`、`schema_version` 在 1..6、`Name/Version/Author/GitHubRepository` 非空、`management_api=true`
    - `request.intercept_before` 带 `user@example.com`：返回正文含 `[[PG_` 且不含原值
    - `management.register` 含 `panel`；`management.handle` GET `panel` → 200 且正文以 `<!doctype html`（不区分大小写）开头
    - 依次 `request.complete`、`shutdown`、`dlclose`，并删除临时目录；失败时非 0 退出，仅输出步骤名与信封错误码，不回显请求正文
    - 参照 `scripts/lint` 对 `scripts/` 的处理，沿用现有脚本的 `fmt.Fprintln(os.Stderr, ...)` 风格，确保 `go run ./scripts/lint` 通过
    - _Requirements: 9.1, 9.2, 9.3, 9.4, 9.5, 9.6, 9.7_

  - [x] 7.2 Makefile 增加 `smoke` 目标
    - `smoke` 依赖 `package`，运行 `go run ./scripts/smoke`（含 cgo）
    - `make clean` 行为不变，仍只清理 `build/` 与 `coverage.out`
    - _Requirements: 4.5, 9.1_

  - [x] 7.3 为冒烟程序的纯逻辑部分补单元测试并校验架构测试
    - 对信封解析、`<!doctype html` 前缀判断等不依赖 cgo 的函数写单元测试（拆到无 cgo 的文件）
    - 运行 `go test ./internal/archtest`；若新包需登记则更新其白名单
    - 运行 `go run ./scripts/lint`，确认 T1/T2/T6 通过
    - _Requirements: 9.5, 9.6_

- [x] 8. Checkpoint - 冒烟
  - Ensure all tests pass, ask the user if questions arise.

- [x] 9. 文档同步
  - [x] 9.1 更新 `docs/design-docs/abi-contract.md`
    - 增加"宿主发现与命名规则"一节（目录、文件名→ID/版本、版本正则）
    - 增加 `management.register` / `management.handle` 线格式一节（字段名、Base64 正文、头部形状、状态码规则、shutdown 拒绝）
    - _Requirements: 7.2, 7.3, 8.2, 8.9_

  - [x] 9.2 更新 `AGENTS.md` 与 `ARCHITECTURE.md`
    - `AGENTS.md`：常用命令加入 `make smoke`，目录地图加入 `management.go` 与 `scripts/smoke/`
    - `ARCHITECTURE.md`：入口层文件清单加入 `management.go`
    - _Requirements: 7.5, 9.1_

  - [x] 9.3 更新 `packaging/README.md` 模板
    - 保留 `{{VERSION}}`、`{{SO_PATH}}` 占位符
    - 说明管理界面"未找到插件页面"仅表示未声明菜单，须以面板状态检查判定加载与启用
    - 说明 Home 模式下资源路由不可用，改用包内 `privacygateway/panel.html` 独立打开
    - 保留 glibc ≥ 2.34、cgo 构建、仅 linux/amd64、TD-CPA-032 残留声明，以及"真实宿主待验"标注
    - _Requirements: 10.3, 10.5, 11.1, 11.2, 11.3, 11.4, 11.5_

  - [x] 9.4 更新 `docs/QUALITY_SCORE.md` 与 `docs/exec-plans/active/plugin-packaging.md`
    - `QUALITY_SCORE.md` 端到端行：更新为含 smoke 与管理页面，保持"真实宿主待验"
    - `plugin-packaging.md`：更新进度，并新增"真实宿主验收（用户执行）"清单：替换 `.so`、重启 CPA、检查加载日志、`GET /v0/management/plugins` 的 `menus` 字段、打开内嵌页面、运行脱敏测试，并预留 Acceptance_Record 记录位
    - 若新增 `docs/` 下文档须登记到 `docs/index.md`
    - 运行 `go run ./scripts/check-docs` 确认通过
    - _Requirements: 10.1, 10.2, 10.3, 10.4_

- [x] 10. Checkpoint - 文档
  - Ensure all tests pass, ask the user if questions arise.

- [x] 11. 最终重建与全量验证
  - [x] 11.1 重新打包并复制裸库
    - `make package` 重新产出 zip，并把 `./privacygateway-v<ver>.so` 覆盖为包内同一份（面板与管理页面改动后字节已变化）
    - 确认裸库与包内 Plugin_Library 的 sha256 相同
    - _Requirements: 4.1, 4.2, 4.3_

  - [x] 11.2 验证可复现与校验和
    - 连续两次 `make package`，zip 的 sha256 相同
    - 解压到临时目录执行 `sha256sum -c privacygateway/checksums.txt`，全部通过，之后清理临时目录
    - _Requirements: 3.1, 3.3_

  - [x] 11.3 运行 `make check` 与 `make smoke`
    - `make check`（fmt-check、vet、build、lint、test、race、coverage、docs-check）全部通过
    - `make smoke` 通过
    - 记录无法验证的内容：真实宿主行为
    - _Requirements: 9.1, 9.2, 9.3, 9.4, 9.5, 9.7_

- [x] 12. Checkpoint - 自动化验证完成
  - Ensure all tests pass, ask the user if questions arise.

- [ ] 13. 真实宿主验收（用户执行，非自动化）
  - 该任务为人工检查点，代理不得执行，也不得标记为已验证；仅用户在真实 CPA v8.0.22 上完成后，由用户自行勾选。
  - 步骤记录于 `docs/exec-plans/active/plugin-packaging.md`：替换 `.so` 并重启 CPA；确认加载日志；`GET /v0/management/plugins` 中 `menus` 含 `/v0/resource/plugins/privacygateway/panel` 与 `隐私脱敏检测`；在管理界面打开插件页面显示面板而非"未找到插件页面"；用面板做脱敏与还原测试；把结论写入 Acceptance_Record。
  - 通过前，README 与 `docs/QUALITY_SCORE.md` 持续标注"真实宿主待验"。
  - _Requirements: 10.1, 10.2, 10.3, 10.4_

## Notes

- 带 `*` 的子任务为可选属性测试，可跳过以加快 MVP；核心单元测试不可选。
- 每个子任务都引用具体需求条款，便于追溯。
- 检查点用于增量验证；Property 8（面板默认地址）为浏览器端逻辑，由 3.2 的静态检查加真实宿主验收覆盖，不做属性测试。
- 已完成项（仅作背景）：`scripts/package/main.go`、`packaging/README.md`、`packaging/panel.html`（独立面板）、Makefile `package` 目标、裸库 `./privacygateway-v0.1.0.so`。

## Task Dependency Graph

```json
{
  "waves": [
    { "id": 0, "tasks": ["1.1", "5.1"] },
    { "id": 1, "tasks": ["1.2", "5.2", "5.4", "5.6"] },
    { "id": 2, "tasks": ["1.3", "3.1", "5.3", "5.5", "5.7"] },
    { "id": 3, "tasks": ["1.4", "3.2", "7.1"] },
    { "id": 4, "tasks": ["1.5", "7.2", "7.3"] },
    { "id": 5, "tasks": ["1.6", "9.1", "9.2", "9.3"] },
    { "id": 6, "tasks": ["9.4"] },
    { "id": 7, "tasks": ["11.1"] },
    { "id": 8, "tasks": ["11.2"] },
    { "id": 9, "tasks": ["11.3"] }
  ]
}
```
