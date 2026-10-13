# CPA 插件发布包计划

> 日期：2026-10-10（2026-10-11 更新状态）。状态：`make package` 已实现；输出位置按用户要求为当前文件夹（design.md 中的 `dist/` 作废）；`make smoke` 已实现；独立面板（`packaging/panel.html`）与插件内嵌管理页面代码保留但默认关闭；真实 CPA 宿主验收待用户执行。设计详见 `.kiro/specs/plugin-package/design.md`。

## 1. 已确认决策

| 编号 | 决策 | 取值 |
|---|---|---|
| PK-1 | 目标平台 | 仅 linux/amd64 |
| PK-2 | 包结构 | zip 根目录带 `privacygateway-v<ver>.so`（供商店安装器读取）；手动安装先临时解压，再安装到插件发现目录；说明文件放 `privacygateway/` |
| PK-3 | 规格流程 | Design-first |

## 2. 关键事实（CPA v8.0.22 源码）

- 仓库根目录的 `plugin.yaml` 是待接入外部 registry 的发布元数据草案；CPA 宿主不读取它，最终字段须按目标 registry 的 schema 调整。

- 插件 ID 由库文件名推导，不读 manifest；现有产物 `libprivacygateway.so` 推导出的 ID 与配置键 `privacygateway` 不一致，必须在打包时重命名。
- 宿主需 `plugins.enabled: true` 与 `plugins.configs.privacygateway.enabled: true` 才加载；包内附配置片段，用户需手动合并。
- 扫描 `plugins/<GOOS>/<GOARCH>/` 与 `plugins/`，相对 CPA 工作目录。

## 3. 验收

| 项 | 状态 |
|---|---|
| `make package` 产出可复现 zip | 已完成（2026-10-10）：zip 输出到插件根目录（当前文件夹），连续两次 sha256 相同，`sha256sum -c` 通过 |
| 交付物加载冒烟（解压后 dlopen/init/register/脱敏/管理页面/shutdown） | 已由 `make smoke` 覆盖（2026-10-11）：Go cgo 冒烟对 zip 解压后的 .so 通过 |
| 内嵌管理页面单元测试（management.register/handle） | 已完成 |
| 真实 CPA v8.0.22 按 README 安装并完成一次脱敏请求（用户执行） | 未开始 |

## 4. 检测面板（2026-10-10 用户要求）

用户实测逐字符提示词，结果显示原文到达上游，插件未生效；随后要求提供面板。保留**独立单文件 HTML**（`packaging/panel.html`，打进 zip 的 `privacygateway/panel.html`）和插件内嵌管理处理代码，内嵌页面由配置开关控制、默认关闭（2026-10-13 更新：`plugins.configs.privacygateway.panel.enabled`，默认 `false`；为 `true` 时向宿主声明 `management_api` 并暴露 panel 资源，见 `docs/design-docs/abi-contract.md` §10）。2026-10-13 面板 UI 重构：单文件内拆为 Tab（连接/插件状态/脱敏测试）与 `Panel.{conn,status,test}` 模块命名空间，状态检查行抽取为纯函数 `statusChecks`，行为守恒。

- 状态检查：`GET /v0/management/plugins`（404 时回退 `/v8`），逐项判定 plugins_enabled / 发现 / configured / enabled / registered / effective_enabled，并给出修复建议。
- 一键启用：`PATCH /v0/management/plugins/privacygateway/enabled`，宿主写回 config.yaml 后异步重载；全局 `plugins.enabled` 不代改。
- 脱敏测试：逐字符复述提示词，判定每项是占位符还是原文；还原测试检查回复中是否残留占位符。
- 验证范围：JS 语法检查通过；判定逻辑用用户实测输出（5 项都判为未脱敏，对照组正常）和构造的已脱敏输出（5 项都判为已脱敏）各验证一次。没有在真实 CPA 或浏览器中运行过。

## 5. 真实宿主验收（用户执行）

以下清单由用户在真实 CPA 环境逐项执行并勾选：

- [ ] 替换 .so：从 zip 根目录取 `privacygateway-v<ver>.so`，安装到 `plugins/linux/amd64/`（文件名不得改），替换后重启 CPA。
- [ ] 确认启动日志无 `pluginhost: failed to load plugin privacygateway`。
- [ ] `GET /v0/management/plugins` 中 privacygateway 的 `registered` 与 `effective_enabled` 均为 `true`。
- [ ] 面板默认关闭：`capabilities.management_api` 为 false、`resources` 为空、访问插件 panel 返回 404；将 `panel.enabled` 改为 `true` 重启后，`management_api` 为 true、resources 含 panel、GET panel 返回 200。
- [ ] 用真实请求做脱敏测试，确认敏感项以占位符到达上游。
- [ ] 用真实请求做还原测试（含流式），确认回复无占位符残留、语义完整。

Acceptance_Record（验收后由用户填写）：

| 日期 | 宿主版本 | 加载日志结论 | menus 字段 | 页面是否打开 | 脱敏测试结果 | 备注 |
|---|---|---|---|---|---|---|
|  |  |  |  |  |  |  |

> 注意：此前一次实测中 5 个敏感值全部以明文到达上游，根因尚未定位。请以面板「检查状态」的判定结果（plugins_enabled / 发现 / configured / enabled / registered / effective_enabled 各项）为线索进行排查。
