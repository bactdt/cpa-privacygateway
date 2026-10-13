# 插件开发文档对齐计划

> 2026-10-12。目标：对照 CPA 开发文档（https://help.router-for.me/plugin/development.html）和锁定的 v8.0.22 宿主实现，修正当前发布说明、元数据和验证门禁；保持脱敏与还原行为不变。

1. [x] 修正注册元数据：仓库地址、`custom_rules` 配置字段；面板关闭时不声明 `management_api`，保留面板实现。
2. [x] 同步 ZIP 根目录动态库的安装说明、打包器注释、`plugin.yaml` 草案和包内 README。
3. [x] 更新管理接口、打包结构、交付物冒烟测试，检查关闭面板与商店安装包（`management_test.go`、`scripts/package/zip_test.go`、`scripts/smoke/{check,check_test,main}.go` 已对齐根目录布局与面板关闭行为）。
4. [x] 运行相关测试及可用门禁（`make check` 通过：fmt/vet/build/lint/test/coverage/docs-check），更新质量记录并归档本计划。

发布说明：当前 `plugin.json` 的 schema v2/direct 是 CPA v8.0.22 支持的格式；官网页面示例仍描述 schema v1/GitHub Release 路径，本轮不改变已验收成功的安装方式。

对齐依据（官方文档关键条款）：

- ZIP 根目录必须直接包含动态库，禁止放入子目录 → 打包器与冒烟提取器均按根目录校验。
- "Declare only capabilities the plugin actually implements" → 面板关闭时不声明 `management_api`。
- 手动安装路径 `plugins/linux/amd64/` 属于宿主发现目录规则，与 zip 内布局是两回事，README 已区分。
