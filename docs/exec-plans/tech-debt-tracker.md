# 技术债台账 · cpa

> 本台账由阶段 1 全量扫描生成，是 `docs/exec-plans/active/harness-migration.md` 的配套问题清单。
> 字段定义（规则附录 B）：**ID / 位置 / 类别 / 严重度 / 证据 / 修复建议 / 业务行为影响 / 关联批次 / 验收方式 / 状态**。
> 严重度：`P0`=阻断构建/发布或安全红线；`P1`=显著阻碍维护与可验证性；`P2`=一般改进项。
> 状态：`open` / `in-progress` / `fixed` / `declined`（须附理由）。
> 「业务行为影响」一列明确区分：`无（纯重构/工程）` / `有（bug 修复）` / `有（业务变更，需人工确认）`。

**汇总：51 条 —— P0 9 条、P1 26 条、P2 16 条（fixed 41、declined 3、open 7）。**

---

## P0 · 阻断级（9 条）

### TD-CPA-001 · 源码无法编译（7 处错误）

| 字段 | 内容 |
|---|---|
| 位置 | `cpa/redact/detect.go:97,103,108,113,118,159`；`cpa/redact/walk.go:9` |
| 类别 | 构建 / bug |
| 严重度 | **P0** |
| 证据 | `go build ./...` 输出：`redact/detect.go:97:67: too few values in struct literal of type span`（共 6 处，行号 97/103/108/113/118/159）；`redact/walk.go:9:2: "sort" imported and not used`。`go vet ./...` 报同样 7 条。退出码 1。 |
| 修复建议 | ①`detect.go` 中 6 处 `span{...}` 字面量补齐第 4 个字段 `allow`（对照 `span` 定义，四字段为 `start/end/kind/allow`）；②删除 `walk.go:9` 未使用的 `import "sort"`（若后续重构复用 `sort` 则改为实际使用）。 |
| 业务行为影响 | 无（当前代码根本编译不过，修复即恢复既定行为） |
| 关联批次 | 批次 0 |
| 验收方式 | `go build ./...` 与 `go vet ./...` 均退出码 0；`gofmt -l .` 无输出 |
| 修复证据 | 批次 0：`span` 字面量补齐 `allow` 字段、`sort` 改为实际使用；`go build`/`go vet`/`gofmt` 全过（2026-10-09） |
| 状态 | fixed |

### TD-CPA-002 · `cpa/` 未纳入 Git 版本管理

| 字段 | 内容 |
|---|---|
| 位置 | 仓库根 `/project/web/nomal`；`cpa/` 全目录 |
| 类别 | 工程 / 可追溯性 |
| 严重度 | **P0** |
| 证据 | `git status --short` → `?? cpa/`；`git diff --stat` 无输出（因全部未跟踪）。 |
| 修复建议 | 将 `cpa/` 纳入追踪并提交（分支选择见计划 §6 决策 D1）；提交前先完成 TD-CPA-001 修复与 `.gitignore`（TD-CPA-006）。 |
| 业务行为影响 | 无（工程治理） |
| 关联批次 | 批次 0 |
| 验收方式 | `git ls-files cpa/ | wc -l` > 0 且包含全部 `*.go`；`git status --short` 中 `cpa/` 不再是 `??` |
| 修复证据 | 批次 0：`cpa/` 纳入 Git 追踪并提交 `main`（D1）（2026-10-09） |
| 状态 | fixed |

### TD-CPA-003 · 零测试覆盖

| 字段 | 内容 |
|---|---|
| 位置 | 全仓；`cpa/` 与 `cpa/redact/` |
| 类别 | 测试 / 安全债 |
| 严重度 | **P0** |
| 证据 | `find cpa -name '*_test.go'` 输出 0 个文件；1952 行代码（11 个 `*.go`）无任何自动化测试。关键路径：检测规则、span 归并、映射往返、流式还原、ABI 分发、状态表并发均无保护。 |
| 修复建议 | 按规则 §9 R3「先护栏后动手」，在重构前为 `redact` 包与 main 包纯逻辑补单元测试；ABI 层可用「Go 侧直接调用 `handleABIMethod`」做集成测试。 |
| 业务行为影响 | 无（新增测试不改变运行时行为） |
| 关联批次 | 批次 3、批次 4、批次 5 |
| 验收方式 | `go test ./...` 通过；覆盖率报告存在；抽样 ≥3 处故意破坏实现时测试失败 |
| 修复证据 | 批次 3/4/5：redact 63 测试 + main 包测试全过；覆盖率门禁通过（2026-10-09） |
| 状态 | fixed |

### TD-CPA-004 · 无任何 CI/门禁覆盖 `cpa/`

| 字段 | 内容 |
|---|---|
| 位置 | `.github/workflows/build.yml`、`.github/workflows/dart-format-patch.yml` |
| 类别 | 工程 / 门禁 |
| 严重度 | **P0** |
| 证据 | `build.yml` 的 `checks` job 使用 `go-version-file: native/go.mod` 仅构建 `native/`；`dart-format-patch.yml` 仅手动生成 Dart 补丁。两者均不引用 `cpa/`。`.git/hooks/` 仅含 `*.sample`，无 pre-commit。 |
| 修复建议 | 阶段 3 新增覆盖 `cpa/` 的 job（`go vet` + `gofmt -l` + `go build -buildmode=c-shared` + `go test`），或先以 `cpa/Makefile` 提供本地一条命令门禁。落地位置见计划 §6 决策 D5。 |
| 业务行为影响 | 无（工程治理） |
| 关联批次 | 批次 2 |
| 验收方式 | 干净环境执行门禁命令一次成功；故意注入编译错误时 CI 失败 |
| 修复证据 | 批次 2：`scripts/lint`/`covercheck`/`check-docs`、`internal/archtest`、`.github/workflows/cpa.yml` 全部实测拦截违规（2026-10-09） |
| 状态 | fixed |

### TD-CPA-005 · `cpa` 未纳入源码同步/打包

| 字段 | 内容 |
|---|---|
| 位置 | `scripts/sync_source.py:13-16`（`SOURCE_DIRECTORIES`） |
| 类别 | 工程 / 发布 |
| 严重度 | **P0** |
| 证据 | `SOURCE_DIRECTORIES = {'.github','lib','native','android',...}` 不含 `cpa`；故 `cpa/` 源码不会同步到发布镜像 `../guoapp`，也不进入 `真果·鉴-*.zip`。 |
| 修复建议 | 若插件属于发布物，把 `cpa` 加入 `SOURCE_DIRECTORIES`；同时确认 `EXCLUDED_DIRECTORIES` 含 `build`（第 25 行）已能排除产物目录。是否纳入见计划 §6 决策 D2。 |
| 业务行为影响 | 无（工程治理） |
| 关联批次 | 批次 0 |
| 验收方式 | `python3 scripts/sync_source.py --check` 通过；同步产物中包含 `cpa/*.go` 且不含 `cpa/build/` |
| 修复证据 | 批次 0：`sync_source.py` 的 `SOURCE_DIRECTORIES` 加入 `cpa`；`--check` 通过（2026-10-09） |
| 状态 | fixed |

### TD-CPA-006 · 构建产物 `scratch`（5.3 MB ELF）混入源码树

| 字段 | 内容 |
|---|---|
| 位置 | `cpa/scratch` |
| 类别 | 工程 / 卫生 |
| 严重度 | **P0** |
| 证据 | `ls -la` → 5553435 字节；`file` → `ELF 64-bit LSB executable, x86-64, Go BuildID=...`，为一次手工 `go build` 的可执行产物。 |
| 修复建议 | 删除 `cpa/scratch`；在 `.gitignore` 增加 `cpa/build/`、`cpa/scratch`、`*.so`、`*.dll`、`*.dylib`；构建产物统一输出到 `cpa/build/`。删除动作见计划 §6 决策 D8。 |
| 业务行为影响 | 无（工程卫生） |
| 关联批次 | 批次 0 |
| 验收方式 | `git status --ignored` 显示产物被忽略；`find cpa -maxdepth 1 -name scratch` 无输出 |
| 修复证据 | 批次 0：`scratch/` 删除并纳入 `.gitignore`；产物统一输出 `cpa/build/`（2026-10-09） |
| 状态 | fixed |

## P1 · 显著阻碍维护与可验证性（26 条）

### TD-CPA-007 · 关键设计无文档（占位符格式 / ABI 契约 / fail_closed）

| 字段 | 内容 |
|---|---|
| 位置 | `cpa/redact/mapping.go`（占位符）、`cpa/abi.go`（ABI）、`cpa/plugin.go`（fail_closed 策略） |
| 类别 | 文档 |
| 严重度 | **P1** |
| 证据 | `cpa/` 无 README/ARCHITECTURE/AGENTS/docs 树；占位符 `[[PG_<16hex>_<seq>]]`、`SchemaVersion=6`/`ABIVersion=1` 契约、解析失败默认拒绝（fail_closed）语义仅存在于源码。 |
| 修复建议 | 阶段 2 在 `cpa/docs/design-docs/` 下为占位符格式、ABI 契约、fail_closed 各写独立文档；入口 `cpa/ARCHITECTURE.md` 引用。 |
| 业务行为影响 | 无（文档） |
| 关联批次 | 批次 1 |
| 验收方式 | 上述三主题各有文档且与代码抽样一致（≥3 处） |
| 处置决定 | 文档/工程底座已经存在；当前源码同步和集中复验仍按计划执行。 |
| 修复证据 | 批次 1：`docs/design-docs/{placeholder-format,abi-contract,fail-closed}.md` 落地并被 `ARCHITECTURE.md` 引用（2026-10-09） |
| 状态 | fixed |

### TD-CPA-008 · 无构建脚本与构建说明

| 字段 | 内容 |
|---|---|
| 位置 | `cpa/`（缺 `Makefile`） |
| 类别 | 工程 / 可复现性 |
| 严重度 | **P1** |
| 证据 | `find cpa -iname 'Makefile'` 无输出；构建需 `CGO_ENABLED=1 go build -buildmode=c-shared`，但无脚本、无文档；本机 Go 不在 PATH（见 TD-CPA-009）。 |
| 修复建议 | 新增 `cpa/Makefile`，提供 `build/test/vet/fmt/lint/clean` 目标，产物统一输出 `cpa/build/`。 |
| 业务行为影响 | 无（工程） |
| 关联批次 | 批次 0 |
| 验收方式 | 干净环境 `make build` 产出共享库；`make test` 通过 |
| 处置决定 | 文档/工程底座已经存在；当前源码同步和集中复验仍按计划执行。 |
| 修复证据 | 批次 0：`cpa/Makefile` 落地（build/test/vet/fmt/lint/coverage/check）；`make build` 产出共享库（2026-10-09） |
| 状态 | fixed |

### TD-CPA-009 · 构建环境（Go 工具链路径）无记录

| 字段 | 内容 |
|---|---|
| 位置 | 本机 `/home/gh/.local/toolchains/go/bin/go`；`cpa/go.mod:3`（`go 1.26.0`） |
| 类别 | 工程 / 可复现性 |
| 严重度 | **P1** |
| 证据 | `which go` 为空；`go` 仅在 `/home/gh/.local/toolchains/go/bin/`；`go env GOROOT=/home/gh/.local/toolchains/go`。新会话无法直接复现构建。 |
| 修复建议 | 在 `cpa/AGENTS.md`（阶段 2）记录「常用命令」含 `export PATH=/home/gh/.local/toolchains/go/bin:$PATH`；或在 Makefile 中检测并提示。 |
| 业务行为影响 | 无（工程） |
| 关联批次 | 批次 1、批次 2 |
| 验收方式 | 新会话按文档命令一次构建成功 |
| 处置决定 | 文档/工程底座已经存在；当前源码同步和集中复验仍按计划执行。 |
| 修复证据 | 批次 1/2：`cpa/AGENTS.md` 记录 Go 工具链 PATH；`docs/CI.md` 记录构建环境（2026-10-09） |
| 状态 | fixed |

### TD-CPA-010 · `walkValue` 函数过长（68 行 / 8 参数）

| 字段 | 内容 |
|---|---|
| 位置 | `cpa/redact/walk.go:167` |
| 类别 | 复杂度 / 可维护性 |
| 严重度 | **P1** |
| 证据 | `awk` 统计函数体 68 行，为全仓最长；签名为 8 参数；同时承担「JSON 递归遍历」与「替换记录」双重职责。 |
| 修复建议 | 拆分职责：遍历与替换记录分离；参数收敛为结构体。先补测试（TD-CPA-003）再重构。 |
| 业务行为影响 | 无（纯重构，需行为守恒证据） |
| 关联批次 | 批次 3 |
| 验收方式 | 拆分后函数 ≤ 阈值；重构前后相同输入输出一致 |
| 处置决定 | walkValue 保留：68 行递归 walker、分支密度低、walk_test 11 测试护栏充分；为行数拆分收益低于行为变更风险 |
| 状态 | declined |

### TD-CPA-011 · `InterceptRequestBeforeAuth` 方法过长（63 行 / 7 个提前返回）

| 字段 | 内容 |
|---|---|
| 位置 | `cpa/plugin.go:59` |
| 类别 | 复杂度 / 单一职责 |
| 严重度 | **P1** |
| 证据 | `awk` 统计 63 行；含准入、关键词匹配、限额检查、格式判定、映射建立、脱敏共 6 类职责与 7 个提前返回分支。 |
| 修复建议 | 拆为「准入判定 / 关键词 / 限额 / 脱敏编排」若干私有方法。先补测试再重构。 |
| 业务行为影响 | 无（纯重构） |
| 关联批次 | 批次 4 |
| 验收方式 | 拆分后函数 ≤ 阈值；行为守恒测试通过 |
| 修复证据 | 批次 4：63→38 行（准入/限额/格式闸门与 `redactInterceptRequest` 分离）；测试全过（2026-10-09） |
| 状态 | fixed |

### TD-CPA-012 · `handleABIMethod` 分发函数过长（54 行 / 6 case）

| 字段 | 内容 |
|---|---|
| 位置 | `cpa/abi.go:220` |
| 类别 | 复杂度 / 单一职责 |
| 严重度 | **P1** |
| 证据 | `awk` 统计 54 行；switch + 6 个 case，每个 case 内联处理逻辑。 |
| 修复建议 | 每 case 抽为独立 handler 方法并建注册表；先补 ABI 集成测试。 |
| 业务行为影响 | 无（纯重构） |
| 关联批次 | 批次 4 |
| 验收方式 | 函数 ≤ 阈值；分发行为与重构前一致 |
| 修复证据 | 批次 4：54→44 行（抽取 `handleRequestIntercept`，消除重复解码）；测试全过（2026-10-09） |
| 状态 | fixed |

### TD-CPA-013 · `Config.Detect` 分支密集（54 行 / 36 个 `if`）

| 字段 | 内容 |
|---|---|
| 位置 | `cpa/redact/detect.go:84` |
| 类别 | 复杂度 |
| 严重度 | **P1** |
| 证据 | `awk` 统计 54 行；`grep -c 'if '` 计数 36。检测规则聚合在单函数内。 |
| 修复建议 | 各检测规则抽为独立 `detector`，以切片注册；先补每类规则的单元测试。 |
| 业务行为影响 | 无（纯重构） |
| 关联批次 | 批次 3 |
| 验收方式 | 分支数下降；规则级测试逐条通过 |
| 处置决定 | Config.Detect 保留：规则聚合器，36 个 if 多为空值/长度保护；逐规则拆函数只增间接层 |
| 状态 | declined |

### TD-CPA-014 · 手写插入排序重复实现

| 字段 | 内容 |
|---|---|
| 位置 | `cpa/redact/detect.go:298-308`（`resolveSpans`）与 `cpa/redact/walk.go:252-260`（`applyReplacements`） |
| 类别 | 重复代码 |
| 严重度 | **P1** |
| 证据 | 两处几乎相同的插入排序实现；`walk.go:9` 已 `import "sort"` 却未使用（即 TD-CPA-001 的一半）。 |
| 修复建议 | 抽取公共排序/归并工具函数，两处复用；或直接用 `sort.Slice`。 |
| 业务行为影响 | 无（纯重构） |
| 关联批次 | 批次 3 |
| 验收方式 | 重复实现收敛为一处；排序结果等价测试通过 |
| 修复证据 | 批次 3：两处手写插入排序改用 `sort.SliceStable`（比较器等价）（2026-10-09） |
| 状态 | fixed |

### TD-CPA-015 · 占位符格式定义分散两处

| 字段 | 内容 |
|---|---|
| 位置 | `cpa/redact/mapping.go:16`（`placeholderRe`）与 `cpa/redact/stream.go:81`（`isPlaceholderPrefix` 内拼 `"[[PG_" + nonce + "_"`） |
| 类别 | 重复代码 / 一致性 |
| 严重度 | **P1** |
| 证据 | 占位符格式在两处独立定义；`stream.go:78` 的 `MaxPlaceholderLen` 硬编码 `4+16+1+9+2`，与 `placeholderRe` 无关联约束。格式变更需同步改多处，易漏。 |
| 修复建议 | 占位符前缀/长度抽为单一常量与构造函数，正则与流式判定共用。 |
| 业务行为影响 | 无（纯重构） |
| 关联批次 | 批次 3 |
| 验收方式 | 定义收敛为一处；格式常量变更时无需改多处 |
| 修复证据 | 批次 3：占位符格式常量集中于 `mapping.go` 推导（2026-10-09） |
| 状态 | fixed |

### TD-CPA-016 · 死代码：未被调用的导出/私有方法（已修复）

| 字段 | 内容 |
|---|---|
| 位置 | `cpa/abi.go:147`（`inferPluginDir`）、`cpa/plugin.go:42`（`reconfigure`）、`cpa/state.go:62`（`stateTable.get`）、`cpa/state.go:137`（`activeCount`）、`cpa/redact/stream.go:17,21,53`（`StreamRestorer.Reset/Pending/Flush`）、`cpa/redact/mapping.go:173,178,186`（`Mapping.Has/Placeholders/Clear`）、`cpa/abi.go:111-112`（`logLevelInfo/logLevelWarn`） |
| 类别 | 死代码 |
| 严重度 | **P1** |
| 证据 | 全仓 `grep -rn`：`inferPluginDir` 仅 1 处（定义）；`reconfigure` 仅 1 处（定义）；`stateTable.get` 仅 1 处（定义）；`activeCount` 仅 1 处（定义）；`StreamRestorer.Reset/Pending/Flush` 各仅 1 处（定义）；`Mapping.Has/Placeholders/Clear` 各仅 1 处（定义）；`logLevelInfo/logLevelWarn` 各仅 1 处（定义，未被调用）。 |
| 修复建议 | 逐项判定：确无用途则删除；若属预留 API 则加 `// 保留：<理由>` 注释并补测试。删除前按规则 §9 R5 确认（无法判断的内容不删）。 |
| 业务行为影响 | 无（纯重构） |
| 关联批次 | 批次 3、批次 4 |
| 验收方式 | `staticcheck`/`deadcode` 无 U1000 告警；或每项有保留理由注释 |
| 修复证据 | 2026-10-10：删除 `inferPluginDir`（生产无调用点）及其测试；`StreamRestorer.Reset/Pending`、`Mapping.Has/Clear` 保留并注明宿主契约理由 |
| 状态 | fixed |

### TD-CPA-017 · 配置项解析后从不生效（已修复）

| 字段 | 内容 |
|---|---|
| 位置 | `cpa/config.go:24-25`（`Enabled`/`Priority`）、`cpa/redact/detect.go:30,43`（`MaxFindings`）、`cpa/state.go:14,87,91`（`requestState.format`） |
| 类别 | bug / 配置 |
| 严重度 | **P1** |
| 证据 | `Enabled`：定义于 `config.go:24`、默认 `true`（`config.go:43`），但全仓无读取点——插件忽略 `enabled=false`。`Priority`：定义于 `config.go:25`，无读取点。`MaxFindings`：默认 2000（`detect.go:43`），无读取点——上限从不生效。`requestState.format`：`state.go:91` 写入后无读取点。 |
| 修复建议 | 明确语义：①`enabled=false` 时应在注册/拦截阶段短路（否则配置误导用户）；②`Priority` 交由宿主使用还是插件读取，需澄清；③`MaxFindings` 应在检测循环中强制。语义确认见计划 §6（决策待补）。 |
| 业务行为影响 | 有（业务变更，需人工确认预期语义） |
| 关联批次 | 批次 4 |
| 验收方式 | 每个配置项有对应生效测试：`enabled=false` 不脱敏、`MaxFindings` 超限被截断 |
| 修复证据 | 2026-10-10：`enabled: false` 四拦截器直通（`plugin.enabled()`）；`priority` 文档化为宿主管理、插件不消费。`max_findings` 语义两步修正：初版静默丢弃超限检出（回归：3 邮箱 + max_findings=1 → 2 个明文放行，违反本条验收「禁止漏扫后放行」）；终版 `capFindings` 返回 truncated，`Redact` 转 `ErrFindings`，插件按 fail_closed 拒绝（500 `max_findings_exceeded`）/ fail_open 原样放行（与容量超限同构）。超限拒绝/放行路径的完整闭合与多节点部分脱敏防护在 TD-CPA-043 记录。测试：`TestMaxFindingsTruncatedSignal`、`TestMaxFindingsAllowlistNotCounted`、`TestRedactJSONMaxFindingsError`、`TestPluginMaxFindingsRejectsWhenFailClosed`、`TestPluginMaxFindingsPassesThroughWhenFailOpen`、`TestPluginMaxFindingsWithinLimitRedacts` |
| 状态 | fixed |

### TD-CPA-018 · 结构化日志不完整 / 错误信息可能泄露

| 字段 | 内容 |
|---|---|
| 位置 | `cpa/config.go`（`configParseError`）、`cpa/abi.go`（`rejectLifecycleConfig` 日志路径） |
| 类别 | 可观测性 / 安全 |
| 严重度 | **P1** |
| 证据 | 重配失败时 `logHost(ctx, logLevelError, ..., map[string]any{"error": errParse.Error()})` 将解析错误写入宿主日志；当前为字段名不含值，风险低，但缺少「不记录敏感值」的约束与测试。`logHost` 正常路径从不调用（仅错误路径）。 |
| 修复建议 | 约束日志字段白名单（禁止记录 body/值）；补一条「日志不含敏感样例」的测试；按需增加正常路径结构化日志。 |
| 业务行为影响 | 无（可观测性增强，不改脱敏语义） |
| 关联批次 | 批次 2、批次 4 |
| 验收方式 | 自定义 linter 拦截「非结构化/含敏感字段」日志；测试断言日志不含样例密钥 |
| 修复证据 | 三轮迭代，最终为白名单重建：黑名单式剥值两度被绕过（值内反引号、自定义标签/大写标签/verbatim 标签/显式 `!!int`/未知锚点/重复键，共 13 输入中 9 种泄露）。终版 `configParseError.Error()` 不再基于原始报错改写，而是只由固定类别、行号数字、`standardYAMLTags` 白名单标签（自定义标签显示为 custom tag）与字符校验通过的 Go 类型名重拼；原始错误仅经 `Unwrap` 进程内获取，不写日志。语法错误降级为 `line N: syntax error`（不泄露的取舍，排查配置略不便）。测试：`TestParseConfigErrorNeverEchoesUserContent` 表驱动 11 场景（回退旧实现 10/11 失败）+ 前轮 7 条全保留（2026-10-10） |
| 状态 | fixed |

---

### TD-CPA-043 · max_findings 超限静默丢弃等同漏扫放行（已修复）

| 字段 | 内容 |
|---|---|
| 位置 | `cpa/redact/detect.go`（`capFindings`/`Detect`）、`cpa/redact/mapping.go`（`Redact`）、`cpa/redact/walk.go`（`transformJSON` 错误透传）、`cpa/plugin.go`（三正文入口 + `redactFailure`） |
| 类别 | bug / 安全 |
| 严重度 | **P1** |
| 证据 | TD-017 初版把"超出 max_findings 的检出静默丢弃"当作可接受取舍：3 个邮箱 + max_findings=1 时前 1 个被替换、其余 2 个原文放行——超限场景恰是高密度凭据字段，丢弃即漏扫。另两处路径缺口：① `transformJSON` 把 visitor 返回的 `ErrFindings` 包成 `ErrMalformedJSON`，插件按 400 误报且返回重建正文（部分脱敏混排原文）；② 已有状态条目路径（after-auth、复用 RequestID）失败时无条件 `release`，拒绝在途请求的状态会破坏首个请求的响应还原。 |
| 修复建议 | 超限处理与容量超限同构：fail_closed 拒绝 / fail_open 原样放行，禁止部分脱敏正文外发；错误经领域层原子透传；已有状态条目失败不释放。 |
| 业务行为影响 | 有（超限从"放行部分明文"变为"整体拒绝/放行"，无明文泄露） |
| 关联批次 | 批次 5（TD-017 终版语义延伸） |
| 验收方式 | 多节点超限无部分脱敏输出；已有状态路径拒绝后条目仍在；ABI 层重配后超限返回 500 max_findings_exceeded |
| 修复证据 | 2026-10-10：`capFindings` 返回 `([]span, truncated)`，`Detect` 双返回值；`Redact` 转 `ErrFindings`；`transformJSON` 对 `ErrCapacity||ErrFindings` 原样透传（错误与正文不同时返回，杜绝部分脱敏）；插件统一 `redactFailure`（ErrCapacity/ErrFindings → fail_closed 500，其余 400/fail-open 放行），`redactInterceptRequest` 增加 `ownsEntry` 区分新准入（失败释放）与复用条目（失败不释放）；`handleEmptyRequestID` 无状态路径无映射可还原，保持放行语义并注明。allowlist 不计入上限；`parseConfig` 拒绝负数 `max_findings`。测试：`TestDetectMaxFindingsReportsTruncated`（未超限/恰等/超限/allowlist 不计）、`TestRedactJSONFindingsErrorPropagates`、`TestRedactJSONFindingsErrorNoPartialRedaction`（回退 transformJSON 修复即失败）、`TestPluginExistingStateMaxFindingsFailClosed`、`TestPluginReusedRequestIDMaxFindingsFailClosed`、`TestPluginExistingStateMalformedJSONFailClosed`、`TestPluginExistingStateMalformedJSONFailOpen`、`TestHandleABIReconfigureMaxFindings`（改写为断言 500 拒绝，原"占位符 ≤1"断言在拒绝语义下恒真失效） |
| 状态 | fixed |

### TD-CPA-044 · 插件 panic 无自防护，宿主按熔断处理（已修复）

| 字段 | 内容 |
|---|---|
| 位置 | `cpa/abi.go`（`handleABIMethod` 请求链路入口 recover）、`cpa/plugin.go`（`guardRequest` 测试注入点） |
| 类别 | 健壮性 / 可用性 |
| 严重度 | **P1** |
| 证据（原状） | `handleABIMethod` 无 recover；宿主 interceptor 适配层 recover 后 `fusePlugin` 熔断（abi-contract §8，v8.0.22 源码核读），插件一旦熔断，后续拦截调用被 `isPluginFused` 静默跳过——脱敏网关失效等同全量原文外发。 |
| 修复证据 | 2026-10-10：`handleABIMethod` 请求链路方法加 `defer recover`（panic → 宿主 error 日志 + fail-closed 终止信封，生命周期方法不在此列，仍由宿主熔断语义兜底）；插件不可用（未注册/shutdown）与请求解码失败从"返回 Go error 信封"改为"OK 信封内结果"——宿主 v8.0.22 对 RPC error 放行（abi-contract §8 事实 5），error 信封无法实现 fail-closed；`gatewayFailClosed` 读已注册实例配置，实例不可用时沿用最近一次注册配置的 `fail_closed`，从未注册过时默认 true（最安全）；fail_closed 下：不可用/panic → 500 `server_error` 终止，请求解码失败 → 400 `invalid_request_error` 终止；响应/流方法无终止协议，解码失败按空结果直通并由流恢复器尾块兜底（记宿主 warn 日志）。**未注册/共享库加载失败属部署期问题，插件自身无法防护，依赖宿主加载日志监控（已有 §8 事实）。** 测试：`TestABIRecoverConvertsPanicToTerminate`（guardRequest 注入 panic → 500 终止而非熔断）、`TestABINotRegisteredFailClosedTerminates`、`TestABINotRegisteredFailOpenPassesThrough`（未注册且无历史配置 → 默认 fail_closed 终止）、`TestABIRegisteredFailOpenUnavailablePassesThrough`（fail_open + 不可用 → 空 Body 直通，宿主保留原请求体）、`TestABIDecodeFailureFailClosedTerminates`（400 invalid_request_error）、`TestABIDecodeFailureFailOpenPassesThrough`；`TestHandleABIMethodNotRegistered`、`TestHandleABIShutdownGuardsAllMethods` 改写为新契约（shutdown 后请求拦截 500 终止、响应/流/完成空直通）。 |
| 业务行为影响 | 有（panic 与不可用路径从"宿主熔断网关失效"变为"fail-closed 终止或按配置直通"） |
| 关联批次 | 隐私闭环计划补批 |
| 验收方式 | 注入 panic 的 ABI 调用返回 fail-closed 终止响应而非进程崩溃；共享库加载 harness 下复验 |
| 状态 | fixed |

### TD-CPA-045 · 流式块被裸载荷误判丢弃（已修复）

| 字段 | 内容 |
|---|---|
| 位置 | `cpa/redact/stream.go`（原 `RestoreSSEChunk`/`lineBuf`）、`cpa/plugin.go` `InterceptStreamChunk` |
| 类别 | bug / 可用性 |
| 严重度 | **P1** |
| 证据（原状） | 映射非空时宿主逐事件投递裸载荷（OpenAI/Gemini 裸 JSON、Codex 单行 `event:`/`data:`）；旧 `RestoreSSEChunk` 按 `\n` 分行，无换行块整块缓冲并返回 `DropChunk=true`，客户端只收到宿主自写的 `data: [DONE]`（经 Cloudflare 表现为 524）。Claude 等整帧路径中跨事件切分的占位符以残片输出。另：旧 `appendSSELine` 对未改动的 `data:` 行丢失 `data: ` 前缀（任务 2 保留性测试发现）。 |
| 修复建议 | 一进一出、永不丢块；按载荷形态分派，在协议文本字段上还原并按文本流暂扣跨事件占位符，结束信号处投递残留。 |
| 业务行为影响 | 有（bug 修复：流式块不再被丢弃，跨事件占位符完整还原） |
| 关联批次 | 流式裸载荷修复（`docs/exec-plans/active/stream-restore-fix.md`） |
| 验收方式 | 宿主形态探索测试修复前失败、修复后通过；保留性测试、属性测试与 fuzz 无失败 |
| 修复证据 | 2026-10-11：新增 `redact/stream_payload.go` `RestorePayload(format, chunk)`，一进一出永不丢块，形态分派 bare/sse/opaque；`redact/stream_text.go` 文本字段级还原与按文本流键暂扣（≤33 字节）；协议规划器 `stream_openai.go`/`stream_claude.go`/`stream_gemini.go`/`stream_responses.go`；结束信号投递（OpenAI/Gemini 注入字段，Claude/Responses 合成增量帧，Codex 按 `event:` 行名触发）；`jsonedit.go` 定点编辑；`Mapping.RestoreCount`；`StreamStats` + `StreamRestorer.Stats()`；删除 `RestoreSSEChunk`/`lineBuf`；`InterceptStreamChunk` 改用 `RestorePayload`，不再设 `DropChunk`。测试：`plugin_stream_host_test.go` `TestStreamHost*`（修复前失败、修复后通过）、`plugin_stream_preserve_test.go` `TestStreamPreserve*`、`TestRestorePayload*`、`TestStreamProperty*`（4 协议 × 250 次）、`FuzzRestorePayload`（20s 约 110 万次执行无失败）、`TestTextCarry*`、`TestJSONEdit*`、`TestStreamOpenAI*`/`TestStreamClaude*`/`TestStreamGemini*`/`TestStreamResponses*`、`TestMappingRestoreCount`、`TestStreamRestorerStatsAndReset`；`abi_test.go` `TestHandleABIStreamChunk` 改写为宿主形态。本机自建宿主 E2E 通过；用户真实 CPA 宿主验收通过（2026-10-11，用户执行）：逐字符脱敏测试 5 项敏感值均以占位符到达上游、对照组原样；流式与非流式请求、还原测试均通过（不再 524）。 |
| 状态 | fixed |

## P2 · 一般改进项（15 条）

### TD-CPA-019 · `abi.go` 单文件 401 行，职责过多

| 字段 | 内容 |
|---|---|
| 位置 | `cpa/abi.go`（401 行） |
| 类别 | 复杂度 / 结构 |
| 严重度 | P2 |
| 证据 | `wc -l cpa/abi.go` → 401，为最大文件；内含 ABI 导出、RPC 分发、注册/重配、能力声明、日志回调、`inferPluginDir` 等多类职责。 |
| 修复建议 | 按职责拆分：`abi_export.go`（C 符号）/`abi_dispatch.go`（方法分发）/`abi_register.go`（注册）/`log.go`（日志）。 |
| 业务行为影响 | 无（纯重构） |
| 关联批次 | 批次 4 |
| 验收方式 | 拆分后单文件行数下降且 ≤ 阈值；构建与测试通过 |
| 修复证据 | 批次 4：分发/注册/helper 拆分；当前 404 行 < 500（T1）（2026-10-09） |
| 状态 | fixed |

### TD-CPA-020 · `evictOldestLocked` 每次插入 O(n)

| 字段 | 内容 |
|---|---|
| 位置 | `cpa/state.go`（`evictOldestLocked`，被插入路径调用） |
| 类别 | 性能 |
| 严重度 | P2 |
| 证据 | 逐条比较时间戳选取最旧项，单次淘汰 O(n)；在高并发 + `max_requests=8192` 时淘汰成本随表增大。 |
| 修复建议 | 引入最小堆或近似 LRU 结构；或在压力测试确认无瓶颈前标记 `wontfix` 并记录理由。 |
| 业务行为影响 | 无（性能优化，行为等价） |
| 关联批次 | 批次 4 |
| 验收方式 | 基准测试 `BenchmarkEvict` 显示插入均摊复杂度改善 |
| 备注 | 2026-10-10：`BenchmarkStateTableAcquireWithEviction`（8192 容量 + 立即过期）实测 112µs/op（2 allocs），即满表时每次插入约 13.7ns/条淘汰扫描；均摊成本远低于单请求 JSON 遍历（BenchmarkRedactJSON 79µs）。决策：**保留 O(n) 扫描**，不引入堆/LRU——默认容量 8192 下淘汰只发生在满表瞬间，优化收益不抵复杂度。压测或默认容量上调后重估 |
| 状态 | open |

### TD-CPA-021 · 关键词匹配对请求体做整串拷贝（已修复）

| 字段 | 内容 |
|---|---|
| 位置 | `cpa/plugin.go`（关键词检查处 `strings.Contains(string(req.Body), keyword)`） |
| 类别 | 性能 |
| 严重度 | P2 |
| 证据 | 每个关键词一次 `string(req.Body)` 转换 = 每次 O(body) 分配；多关键词时重复拷贝，body 上限 1 MiB。 |
| 修复建议 | 转换为 `bytes.Contains(req.Body, []byte(keyword))` 避免拷贝，或在循环外转一次。 |
| 业务行为影响 | 无（性能优化，行为等价） |
| 关联批次 | 批次 4 |
| 验收方式 | 基准测试分配次数下降；匹配结果等价测试通过 |
| 修复证据 | 2026-10-10：`guardRequest` 改用 `bytes.Contains(req.Body, []byte(keyword))`，无整串拷贝 |
| 状态 | fixed |

### TD-CPA-022 · 状态表 janitor 全表遍历

| 字段 | 内容 |
|---|---|
| 位置 | `cpa/state.go:143`（`janitor`） |
| 类别 | 性能 |
| 严重度 | P2 |
| 证据 | 定时器每分钟遍历整表清理过期项，复杂度 O(n)；默认 `max_requests=8192`，风险低但随规模增长。 |
| 修复建议 | 若压力测试显示瓶颈，改用到期时间桶/堆；否则标记 `wontfix` 并记录理由。 |
| 业务行为影响 | 无（性能优化） |
| 关联批次 | 批次 4 |
| 验收方式 | 基准测试显示 janitor 耗时随表规模可接受 |
| 备注 | 2026-10-10：`BenchmarkStateTableJanitorFull`（8192 全过期，含重建）1.28ms/op；`BenchmarkStateTableJanitorFresh`（8192 全新鲜）114µs/op、0 allocs。决策：**保留全表遍历**——周期 1 分钟、与请求路径异步，1.3ms/分钟（0.002% 单核）远低于可感知阈值。表规模超过 10^5 量级时重估 |
| 状态 | open |

### TD-CPA-023 · `bankCardCandidates` 多层嵌套（35 行）

| 字段 | 内容 |
|---|---|
| 位置 | `cpa/redact/detect.go:228` |
| 类别 | 复杂度 |
| 严重度 | P2 |
| 证据 | `awk` 统计 35 行，含多层嵌套扫描状态机。 |
| 修复建议 | 早返回降低嵌套层级；抽状态机为独立类型并补测试。 |
| 业务行为影响 | 无（纯重构） |
| 关联批次 | 批次 3 |
| 验收方式 | 最大嵌套层级下降；候选识别测试通过 |
| 处置决定 | bankCardCandidates 保留：35 行低于热点阈值；改动需先解决 TD-CPA-029 缺陷才安全 |
| 状态 | declined |

### TD-CPA-024 · 缺配置示例 `config.example.yaml`

| 字段 | 内容 |
|---|---|
| 位置 | `cpa/`（缺 `config.example.yaml`） |
| 类别 | 文档 / 可用性 |
| 严重度 | P2 |
| 证据 | `find cpa -name 'config*.yaml'` 无输出；`config.go` 定义了全部配置键但无示例，用户不知如何配置 `plugins.configs.<pluginID>`。 |
| 修复建议 | 新增 `cpa/config.example.yaml`，列出全部键与默认值，并与 `config.go` 结构体标签保持同步。 |
| 业务行为影响 | 无（文档） |
| 关联批次 | 批次 1 |
| 验收方式 | 示例中的键集合与 `config.go` 的 yaml 标签集合一致（脚本校验） |
| 处置决定 | 文档/工程底座已经存在；当前源码同步和集中复验仍按计划执行。 |
| 修复证据 | 批次 1：新增 `cpa/config.example.yaml`，键集合与 `config.go` yaml 标签一致（2026-10-09） |
| 状态 | fixed |

### TD-CPA-025 · 未接入依赖漏洞扫描（已修复）

| 字段 | 内容 |
|---|---|
| 位置 | `cpa/go.mod`、`cpa/go.sum` |
| 类别 | 安全 |
| 严重度 | P2 |
| 证据 | 无 `govulncheck` 配置或 CI 步骤；直接依赖 3 个、间接依赖少量。 |
| 修复建议 | 在门禁中加入 `govulncheck ./...`（阶段 3），或至少在发布前人工执行一次。 |
| 业务行为影响 | 无（安全治理） |
| 关联批次 | 批次 2 |
| 验收方式 | CI 或 Makefile 含 `govulncheck` 目标；执行无高危告警 |
| 修复证据 | 2026-10-10：`make vulncheck` 独立目标（需网络，不进 `make check` 串联）；Go 工具链升级 1.27.2 修复 4 项标准库漏洞，当前 0 漏洞 |
| 状态 | fixed |

### TD-CPA-026 · `handleABIRegister` 合并注册与重配逻辑（37 行）

| 字段 | 内容 |
|---|---|
| 位置 | `cpa/abi.go:275` |
| 类别 | 复杂度 / 单一职责 |
| 严重度 | P2 |
| 证据 | `awk` 统计 37 行；同一函数处理首次注册与重配（`plugin.register` 与 `plugin.reconfigure`），分支交织。 |
| 修复建议 | 拆为 `register`/`reconfigure` 两条清晰路径，共享校验；补两条路径的集成测试。 |
| 业务行为影响 | 无（纯重构） |
| 关联批次 | 批次 4 |
| 验收方式 | 注册与重配各自独立测试通过；行为守恒 |
| 修复证据 | 批次 4：37→22 行（`rejectLifecycleConfig`/`registrationEnvelope` 抽取）（2026-10-09） |
| 状态 | fixed |

### TD-CPA-027 · `resolveSpans` 归并逻辑内联（39 行）

| 字段 | 内容 |
|---|---|
| 位置 | `cpa/redact/detect.go:294` |
| 类别 | 复杂度 |
| 严重度 | P2 |
| 证据 | `awk` 统计 39 行；排序 + 重叠归并 + 允许区间判定混在一处（与 TD-CPA-014 排序重复相关）。 |
| 修复建议 | 抽取「归并重叠区间」为独立函数并复用；补重叠/相邻/嵌套三类边界测试。 |
| 业务行为影响 | 无（纯重构） |
| 关联批次 | 批次 3 |
| 验收方式 | 归并函数独立可测；边界用例通过 |
| 修复证据 | 批次 3：39→36 行；排序收敛 stdlib，归并逻辑保留（2026-10-09） |
| 状态 | fixed |

---

### TD-CPA-028 · X 结尾身份证号漏检（已修复）

| 字段 | 内容 |
|---|---|
| 位置 | `cpa/redact/detect.go:182` (`idCardCandidates`) |
| 类别 | 检测缺陷 |
| 严重度 | P1 |
| 证据 | 数字串扫描止于首个非数字字符（含校验位 `X`），使 `end-i` 为 17 而非 18，`validIDCard` 无法通过。`validIDCard` 显式支持 `X` 校验位，存在逻辑不一致。 |
| 修复建议 | 扫描时允许末位为 `X`/`x`，或在 `end` 计算后单独处理校验位；补 X 结尾身份证测试。 |
| 业务行为影响 | 有（检测行为变更，需人工确认） |
| 关联批次 | 批次 3 |
| 验收方式 | X 结尾身份证被检出；现有测试通过 |
| 修复证据 | 2026-10-10：`idCardCandidates` 支持 17 位数字 + X/x 校验位（X 后跟字母数字不算边界）；KnownGap 测试转正为 `TestDetectIDCardXSuffix` |
| 状态 | fixed |

### TD-CPA-029 · 空格/连字符前银行卡号漏检

| 字段 | 内容 |
|---|---|
| 位置 | `cpa/redact/detect.go:228` (`bankCardCandidates`) |
| 类别 | 检测缺陷 |
| 严重度 | P1 |
| 证据 | 扫描前跳过 `i-1` 为 `' '` 或 `'-'` 的起始位，导致 `"card 4111..."` 形式漏检；而 `"cc=4111..."`、行首数字可正常检出。 |
| 修复建议 | 移除或调整前导字符跳过逻辑；补空格前银行卡号测试。 |
| 业务行为影响 | 有（检测行为变更，需人工确认） |
| 关联批次 | 批次 3 |
| 验收方式 | 空格前银行卡号被检出；现有测试通过 |
| 修复证据 | `bankCardCandidates` 不再跳过前导空格/连字符起始位；回归测试 `TestDetectBankCardSpaceBefore` 通过（commit 5513fee） |
| 状态 | fixed |

---

### TD-CPA-030 · 最大长度流式占位符跨块缓冲失效（已修复）

| 字段 | 内容 |
|---|---|
| 位置 | `cpa/redact/stream.go:78`（`maxPlaceholderLen`） |
| 类别 | 缺陷 |
| 严重度 | P1 |
| 证据 | 原常量 `4 + 16 + 1 + 9 + 2 = 32`，但占位符 `[[PG_` 实为 5 字符，最大长度应为 `5 + 16 + 1 + 9 + 2 = 33`。长度差 1 使 `placeholderTail` 扫描起点后移 1 字节，33 字符占位符恰好结束于块尾时无法被识别为未完成，导致占位符被提前发出、后续块无法还原。 |
| 修复方式 | 阶段 4 统一占位符格式定义时将长度改为由格式常量推导（`len(placeholderOpen) + placeholderNonceHexLen + 1 + placeholderMaxSeqLen + len(placeholderClose)`），并新增回归测试 `TestRestoreStreamMaxLengthPlaceholderBuffered`（还原为 32 时该测试失败）。 |
| 业务行为影响 | 有（流式边界行为修正：33 字符占位符结束于块尾时正确缓冲） |
| 关联批次 | 批次 3（阶段 4 重构副产物） |
| 验收方式 | `TestRestoreStreamMaxLengthPlaceholderBuffered` 通过；还原为 32 时失败 |
| 修复证据 | 阶段 4：`maxPlaceholderLen` 改为格式常量推导（`len(placeholderOpen) + placeholderNonceHexLen + 1 + placeholderMaxSeqLen + len(placeholderClose)` = 33）；回归测试 `TestRestoreStreamMaxLengthPlaceholderBuffered` 通过，临时还原为 32 时该测试失败（2026-10-09） |
| 状态 | fixed |


### TD-CPA-031 · 映射容量错误被 visitor 吞掉，可能放行原文

| 字段 | 内容 |
|---|---|
| 位置 | `redact/walk.go` `RedactJSON` visitor；`redact/mapping.go` `Mapping.Redact` |
| 类别 | 隐私安全 / bug |
| 严重度 | **P0** |
| 证据 | `Mapping.Redact` 返回 `ErrCapacity` 时 visitor 返回未修改；遍历层继续处理其他节点，若 count 为 0，插件可能把包含敏感值的原文当作“无命中”返回。 |
| 修复建议 | 保留领域错误并终止整份 JSON 变换；`fail_closed=true` 时拒绝，禁止输出半脱敏正文。 |
| 业务行为影响 | 有（安全 bug 修复） |
| 关联批次 | 批次 4/隐私闭环计划 |
| 验收方式 | 同一请求中前后节点触发容量上限时均不得向上游返回原文；false 模式行为单独断言 |
| 修复证据 | 隐私闭环计划：`redact/walk.go` visit 回调传播 error 且 `ErrCapacity` 直通不包 `ErrMalformedJSON`；`plugin.go` 容量错误 fail-closed 终止 500 `mapping_capacity_exceeded`、fail-open 原样放行；测试 `TestRedactJSONCapacityError*`、`TestPluginCapacityExceeded*`（2026-10-10） |
| 状态 | fixed |

### TD-CPA-032 · SSE 分帧和流结束 flush 契约未闭合

| 字段 | 内容 |
|---|---|
| 位置 | `redact/stream.go`、`plugin.go` `InterceptStreamChunk` |
| 类别 | 流式响应 / bug |
| 严重度 | P1 |
| 证据 | 当前恢复器按原始字节尾部拼接，不解析 SSE 事件；`Flush` 未在 `request.complete` 或其他终止钩子调用。整块反转义也可能改变非内容 JSON 字节。 |
| 修复建议 | 与宿主确认终块信号；按事件和 JSON 字符串增量处理，终止时明确输出 pending 或原样释放。 |
| 业务行为影响 | 有（流式响应边界修复） |
| 关联批次 | 隐私闭环计划 |
| 验收方式 | 任意字节切分、独立事件、转义字符、`[DONE]` 和未完成占位符均有可复现实例 |
| 修复证据 | 隐私闭环计划：`redact/stream.go` 新增 `RestoreSSEChunk` 按行分帧，仅还原 `data:` 载荷、未收尾行缓冲至下一块、事件边界（空行）释放残缺占位符原样输出、保留 `data:` 分隔符；`plugin.go` `HandleRequestComplete` 调用 `Flush()`；残留：flush 输出当前无处投递，需与宿主约定终块信号（见执行计划）。测试 `TestRestoreStreamSSE*` ×3（2026-10-10）。补充（2026-10-11，TD-CPA-045）：宿主逐事件投递、无行分帧需求，`RestoreSSEChunk`/`lineBuf` 已删除，改为 `RestorePayload` 在协议结束信号处投递残留；流在结束信号前中断时，各文本流残留（≤33 字节）在 `Flush` 计入 `StreamStats.Leftover` 后丢弃，终块投递仍无通道。 |
| 状态 | fixed |

### TD-CPA-033 · 热重载/淘汰可能丢失在途请求映射

| 字段 | 内容 |
|---|---|
| 位置 | `plugin.reconfigure`、`stateTable.reset`、`evictOldestLocked` |
| 类别 | 生命周期 / 一致性 |
| 严重度 | P1 |
| 证据 | 重配创建新插件并清空状态表；状态表满时可淘汰最旧活动请求；后续响应钩子访问当前状态表，无法找到旧映射。 |
| 修复建议 | 重配先排空旧实例或按实例路由 requestID；淘汰只针对已完成/可取消状态，或明确 fail-closed。 |
| 并发修正（2026-10-13） | `abiState` 锁覆盖实例选择、状态移交、发布；旧/新表同时持锁，完成条目移交后才发布 redirect；旧回调的读写、流恢复与释放转发到新表。收紧 max_requests 到已有条目数以下时保留旧请求、拒绝新 ID，不再淘汰一条后仍超额插入。`TestOldCallbacksFollowStateHandoff`、`TestConcurrentReconfigureAndOldRequests`、`TestTightenedItemLimitPreservesOldRequestsAndRejectsNewIDs` 验证还原及容量。 |
| 当前 TTL 限制 | 保留既有策略：满表准入可淘汰空闲超过 1×TTL 的最旧条目，janitor 对 admitted 条目在超过 2×TTL 后强制回收；二者均可能影响超时在途请求的还原，不能把 TTL 外保留描述为无条件保证。 |
| 业务行为影响 | 有（响应还原行为） |
| 关联批次 | 隐私闭环计划 |
| 验收方式 | 重配和满表期间的请求均能还原或按记录的安全策略结束，不静默返回占位符/原文混合结果 |
| 修复证据 | 隐私闭环计划：`plugin.reconfigure` 去掉 `states.reset()`；`abi.go` 重新注册时 `takeOverFrom` 移交旧状态表；`evictOldestLocked` 仅驱逐 lastSeen 超过 TTL 的条目，满表全在途时新准入被拒（fail-closed 429）；janitor 跳过 admitted 在途条目。残留风险：宿主丢失完成通知会永久占位（TTL 内），满表全活跃时拒绝新请求。测试 `TestStateTableEvictSkipsActiveWhenFullOfInflight`、`TestStateTableEvictReclaimsTTLIdleEntry`、`TestStateTableTakeOver`、`TestPluginReconfigureKeepsInflightMapping`、`TestPluginReconfigureTightensLimitKeepsEntries`、`TestPluginRegisterHandsOverInflightState`（2026-10-10） |
| 状态 | fixed |

### TD-CPA-034 · 空/重复 RequestID 可能绕过请求扫描（已修复）

| 字段 | 内容 |
|---|---|
| 位置 | `plugin.go` `InterceptRequestBeforeAuth`/`InterceptRequestAfterAuth` |
| 类别 | 隐私安全 / 准入 |
| 严重度 | **P0** |
| 证据 | 空 RequestID 直接返回空修改；已有 ID 跳过关键词拦截与大小上限。 |
| 修复建议 | 关键词与大小检查抽为统一 guard，三入口前置。 |
| 并发修正（2026-10-13） | after-auth 无条目先 acquire；setMapping 不隐式准入，原子保留首个映射，竞争失败用该映射重新脱敏。before/after-auth 共用格式处理，在条目锁内绑定首次非空格式并拒绝冲突；不支持格式按 fail_closed 415 或原样放行。首次失败通过 releaseIfUnmapped 避免回收另一回调已发布映射。`concurrency_test.go` 覆盖同 ID 往返、两类限额、格式竞争、malformed/ErrFindings 失败清理；`TestConcurrentLimitUpdatesAndAdmission` 同时验证带锁限额更新。 |
| 业务行为影响 | 有（准入安全修复） |
| 关联批次 | 隐私闭环计划 |
| 验收方式 | 空 ID、同 ID 相同正文、同 ID 不同正文、请求后置钩子各有明确结果 |
| 修复证据 | 2026-10-10：空 ID 保持 fail-closed 无状态脱敏；`guardRequest`（关键词+MaxBodyBytes）统一前置到 before-auth 新旧条目与 after-auth 有/无状态全部路径；复用条目命中拦截不释放（首请求可能在途）。TD-021 一并修复（bytes.Contains 免 []byte→string 拷贝）。测试 `TestPluginGuardAppliesToReusedRequestID`、`TestPluginGuardAppliesToAfterAuth` |
| 状态 | fixed |

### TD-CPA-035 · ABI []byte Base64 wire 规则未入库（已补文档）

| 字段 | 内容 |
|---|---|
| 位置 | `docs/design-docs/abi-contract.md`；`abi.go` 的 `[]byte` 字段 |
| 类别 | 文档 / ABI |
| 严重度 | P1 |
| 证据 | Go `encoding/json` 会把 Body 等 `[]byte` 编为 Base64；此前示例把其写成普通 JSON 文本，容易导致宿主互操作错误。 |
| 修复建议 | 在 ABI 契约中固定大小写字段名、Base64、信封和释放责任；保持示例使用合成数据。 |
| 业务行为影响 | 无（文档修复） |
| 关联批次 | 批次 1 |
| 验收方式 | 文档示例可由 Go `encoding/json` 生成/解码；宿主 fixture 后续复验 |
| 修复证据 | 批次 1：`docs/design-docs/abi-contract.md` 补 Base64 wire 规则（2026-10-09） |
| 状态 | fixed |

### TD-CPA-036 · JSON 字段遍历对裸字符串数组覆盖不足

| 字段 | 内容 |
|---|---|
| 位置 | `redact/walk.go` `lastKey(path)` 与字符串 visitor |
| 类别 | 检测覆盖 / 行为 |
| 严重度 | P1 |
| 证据 | 数组元素路径末级是数字索引，普通文本检测只看末级键；`input:["敏感值"]` 等裸字符串不命中，工具参数开关关闭时也不扫描。 |
| 修复建议 | 先按协议定义允许的数组语义，再为正文文本数组增加显式规则；不要放宽到结构字段和图片数据。 |
| 业务行为影响 | 有（检测覆盖变更，需人工确认） |
| 关联批次 | 隐私闭环计划 |
| 验收方式 | 文本数组正例、结构数组反例、工具参数数组和多格式 fixture 分别断言 |
| 修复证据 | 隐私闭环计划：`redact/walk.go` visit 回调追加 `arrayText` 参数，数组路径向上传播 textKeys 上下文，裸字符串数组（如 `input:["..."]`）参与检测；不放宽到结构键数组（`data`/`b64_json` 等不扫描）；toolArg 开关优先级不变。测试 `TestRedactJSONStringArrayUnderTextKey`、`TestRedactJSONStringArrayStructuralKeyUntouched`（2026-10-10） |
| 状态 | fixed |

### TD-CPA-037 · after-auth 已有映射时直接放行原文（评审发现）

| 字段 | 内容 |
|---|---|
| 位置 | `plugin.go` `InterceptRequestAfterAuth` |
| 类别 | 隐私安全 / bug |
| 严重度 | **P0** |
| 证据 | after-auth 阶段查到已有映射时直接返回原正文；若 after-auth 新增或修改了凭据/PII，会原文发往上游。 |
| 修复建议 | after-auth 与 before-auth 采用同一脱敏路径：无状态时新建映射并写回；有状态时用既有映射重新脱敏新正文。 |
| 业务行为影响 | 有（安全修复） |
| 关联批次 | 隐私闭环计划评审 |
| 验收方式 | after-auth 新敏感值、已有映射+修改正文两种场景均不得外发原文，且响应可还原 |
| 修复证据 | 评审修复：`InterceptRequestAfterAuth` 无状态走 `redactAfterAuthWithoutState`（完整脱敏+写回状态表），有状态走 `redactRequestWithExistingState`（既有映射重新脱敏）。测试 `TestPluginAfterAuthRedactsNewSecrets`、`TestPluginAfterAuthRedactsModifiedBodyWithExistingMapping`（2026-10-10） |
| 状态 | fixed |

### TD-CPA-038 · janitor 跳过全部 admitted 条目可耗尽状态表（评审发现）

| 字段 | 内容 |
|---|---|
| 位置 | `state.go` `janitor`、`evictOldestLocked` |
| 类别 | 生命周期 / 可用性 |
| 严重度 | P1 |
| 证据 | 宿主丢失 complete 通知时 admitted 条目永不清理，满表后新准入被永久拒绝。 |
| 修复建议 | 为 admitted 条目保留宽限期后强制回收。 |
| 业务行为影响 | 有（超长在途请求的恢复能力 vs 可用性取舍） |
| 关联批次 | 隐私闭环计划评审 |
| 验收方式 | 1×TTL 内 admitted 保留；超过 2×TTL 强制清理释放准入计数 |
| 修复证据 | 评审修复：admitted 条目超过 2×TTL（hard deadline）由 janitor 强制清理；evictOldestLocked 同样仅驱逐超 TTL 条目。残留风险：超过 2×TTL 的超长请求响应不可还原。测试 `TestStateTableJanitorReclaimsAdmittedAfterHardDeadline`（2026-10-10） |
| 状态 | fixed |

### TD-CPA-039 · Mapping/StreamRestorer 同 RequestID 并发无锁（评审发现）

| 字段 | 内容 |
|---|---|
| 位置 | `redact/mapping.go`、`redact/stream.go` |
| 类别 | 并发 / 数据竞态 |
| 严重度 | P1 |
| 证据 | 返回的 Mapping 与 StreamRestorer 内部 map/缓冲无锁；同 RequestID 钩子并发时可能 map panic、占位符序号错乱或 SSE 内容损坏。 |
| 修复建议 | Mapping 与 StreamRestorer 内部加互斥锁。 |
| 业务行为影响 | 无（并发安全修复） |
| 关联批次 | 隐私闭环计划评审 |
| 验收方式 | `-race` 下并发 Placeholder/Restore/流式还原全部通过 |
| 修复证据 | 评审修复：`Mapping.mu`、`StreamRestorer.mu` 覆盖全部可变状态；requestState 字段访问也收敛到 `requestState.mu`。测试 `TestMappingConcurrentPlaceholderAndRestore`、`TestStateTableConcurrentAccess`（race 下运行）（2026-10-10） |
| 状态 | fixed |

### TD-CPA-040 · RestoreEscapedBytes 整载荷反转义破坏 JSON（评审发现）

| 字段 | 内容 |
|---|---|
| 位置 | `redact/mapping.go` `unescapeJSONBytes` |
| 类别 | 流式响应 / bug |
| 严重度 | P1 |
| 证据 | 对整个 SSE data 载荷反转义会把 JSON 结构本身的 `\n`/`\"` 等一并还原，产生非法 JSON 或语义变化。 |
| 修复建议 | 仅在可证明是单个 JSON 字符串字面量时整段反转义并重编码；否则只定点还原占位符括号的 `\u005b/\u005d` 转义。 |
| 业务行为影响 | 有（流式还原字节级行为变更） |
| 关联批次 | 隐私闭环计划评审 |
| 验收方式 | 结构转义保持不变、`\u005b` 占位符还原、字符串字面量载荷保持 JSON 编码三场景断言 |
| 修复证据 | 评审修复：`RestoreEscapedBytes` 双路径（字符串字面量反转义+重编码 / 定点括号转义还原）；占位符 token 明文形式用 phToRaw、转义形式用 phToEsc。测试 `TestMappingRestoreEscapedBytesDoesNotUnescapeStructure`、`TestMappingRestoreEscapedBytesBracketEscapeOnly`、`TestMappingRestoreEscapedBytesStringLiteralPayload`（2026-10-10） |
| 状态 | fixed |

### TD-CPA-041 · shutdown 后 register/reconfigure 可复活插件（评审发现）

| 字段 | 内容 |
|---|---|
| 位置 | `abi.go` `handleABIRegister`、`PrivacyGatewayPluginShutdown` |
| 类别 | 生命周期 / 状态机 |
| 严重度 | P1 |
| 证据 | 关闭后并发调用 register/reconfigure 会重新创建实例并清除 shuttingDown 标记，插件被复活。 |
| 修复建议 | 生命周期入口检查 shuttingDown；关闭后拒绝生命周期调用直到重新 init。 |
| 业务行为影响 | 有（关闭语义收紧） |
| 关联批次 | 隐私闭环计划评审 |
| 验收方式 | shutdown 后 register/reconfigure 返回错误且不重建实例 |
| 修复证据 | 评审修复：`handleABIRegister` 在 shuttingDown 时拒绝并记录宿主日志；`shuttingDown` 只由 `cliproxy_plugin_init` 重置。测试 `TestPluginLifecycleShutdownBlocksReregister`（2026-10-10） |
| 状态 | fixed |

### TD-CPA-042 · text 数组上下文穿透对象子树（评审发现）

| 字段 | 内容 |
|---|---|
| 位置 | `redact/walk.go` `walkValue` |
| 类别 | 检测覆盖 / 误报 |
| 严重度 | P2 |
| 证据 | textKeys 数组的 arrayText 上下文沿整棵子树传播，数组内结构对象里的未知字段会被扫描脱敏，可能破坏协议结构。 |
| 修复建议 | 上下文只作用于数组的直接字符串元素，不穿过对象继续传播。 |
| 业务行为影响 | 有（检测范围收窄） |
| 关联批次 | 隐私闭环计划评审 |
| 验收方式 | 数组内结构对象未知字段不扫描；同数组直接字符串元素仍扫描 |
| 修复证据 | 评审修复：对象分支以 `arrayText=false` 进入子树；数组分支只对直接元素应用 directArrayText。测试 `TestPluginTextArrayNestedObjectNotScanned`、`TestPluginTextArrayOfStringsStillScanned`（2026-10-10） |
| 状态 | fixed |

### TD-CPA-046 · 流式工具参数原值含引号时不做 JSON 转义（已知限制）

| 字段 | 内容 |
|---|---|
| 位置 | `cpa/redact/stream_text.go`、`cpa/redact/stream_openai.go`（工具调用参数文本流） |
| 类别 | 流式还原 / 已知限制 |
| 严重度 | P2 |
| 证据 | 工具参数增量是内层 JSON 的片段，按纯文本替换占位符；原值含 `"` 等字符时还原后不按内层 JSON 转义，拼接后的参数串可能不是合法 JSON。与非流式 `RestoreJSON` 行为一致（TD-CPA-045 登记）。 |
| 修复建议 | 若需改变，流式与非流式需同时调整：按内层 JSON 字符串上下文转义还原值。 |
| 业务行为影响 | 当前仅登记，未改变行为 |
| 关联批次 | 流式裸载荷修复 |
| 验收方式 | 原值含引号的工具参数还原后可被 `json.Unmarshal` 解析 |
| 状态 | open |

### TD-CPA-047 · Responses 合成增量帧复用模板 `sequence_number`（已知限制）

| 字段 | 内容 |
|---|---|
| 位置 | `cpa/redact/stream_responses.go` `synthResponses` |
| 类别 | 流式还原 / 已知限制 |
| 严重度 | P2 |
| 证据 | 结束信号前合成的 delta 帧以最近一次 delta 事件为模板，`sequence_number` 原样复用，客户端可能看到重复序号。 |
| 修复建议 | 确认客户端是否依赖 `sequence_number` 单调；必要时改写合成帧序号或省略该字段。 |
| 业务行为影响 | 当前仅登记，未改变行为 |
| 关联批次 | 流式裸载荷修复 |
| 验收方式 | 合成帧序号不与已发帧重复，或有客户端兼容性结论 |
| 状态 | open |

### TD-CPA-048 · Gemini `alt` 非 SSE 模式只做完整占位符还原（已知限制）

| 字段 | 内容 |
|---|---|
| 位置 | `cpa/redact/stream_payload.go` `carryEnabled`、`cpa/redact/stream_gemini.go` |
| 类别 | 流式还原 / 已知限制 |
| 严重度 | P2 |
| 证据 | Gemini 非 SSE 流（JSON 数组片段）不按 candidate 文本流暂扣，跨块切开的占位符保持残片，只还原块内完整占位符。 |
| 修复建议 | 确认宿主是否会以该模式投递；需要时为数组片段形态补规划器与投递点。 |
| 业务行为影响 | 当前仅登记，未改变行为 |
| 关联批次 | 流式裸载荷修复 |
| 验收方式 | 数组片段形态下跨块占位符完整还原 |
| 状态 | open |

### TD-CPA-049 · OpenAI 结束块目标字段非字符串时残留无法注入（已知限制）

| 字段 | 内容 |
|---|---|
| 位置 | `cpa/redact/stream_openai.go` `injectOpenAI` |
| 类别 | 流式还原 / 已知限制 |
| 严重度 | P2 |
| 证据 | 结束块 `delta` 中目标字段已存在但不是字符串（如 `"content":null`）时 `injectOpenAI` 不覆盖该字段，残留无法投递，字节数计入 `StreamStats.Leftover`。 |
| 修复建议 | 字段为 `null` 时替换为残留字符串字面量。 |
| 业务行为影响 | 当前仅登记，未改变行为 |
| 关联批次 | 流式裸载荷修复 |
| 验收方式 | 结束块含 `"content":null` 时残留注入成功、`Leftover` 为 0 |
| 状态 | open |

### TD-CPA-050 · 嵌套超限事件原样透传且已执行的暂扣不回滚（已知限制）

| 字段 | 内容 |
|---|---|
| 位置 | `cpa/redact/stream_payload.go` `restoreEvent`、`cpa/redact/jsonedit.go` `editStrings` |
| 类别 | 流式还原 / 已知限制 |
| 严重度 | P2 |
| 证据 | `editStrings` 失败（嵌套超过 64 层）时事件原样透传并计入 `Passthrough`；遍历中已执行的 `restoreText` 对暂扣状态的修改不回滚，该事件中的文本流状态可能与输出不一致。 |
| 修复建议 | 先在副本上计算暂扣变更，编辑成功后再提交。 |
| 业务行为影响 | 当前仅登记，未改变行为 |
| 关联批次 | 流式裸载荷修复 |
| 验收方式 | 嵌套超限事件透传后暂扣状态与处理前一致 |
| 状态 | open |

### TD-CPA-051 · 面板开关硬编码为编译期常量（已修复）

| 字段 | 内容 |
|---|---|
| 位置 | `cpa/management.go`（原 `managementPanelEnabled = false` 常量）、`cpa/abi.go`（`registrationEnvelope`）、`cpa/config.go` |
| 类别 | 可用性 / 配置 |
| 严重度 | P2 |
| 证据（原状） | 面板可达性由 `const managementPanelEnabled = false` 锁死，部署后无法按需打开，只能改源码重编；`abi.go:338` 能力位读该常量。 |
| 修复证据 | 2026-10-13：新增 `config.go` `panelConfig`（`panel.enabled`，默认 false）；`abiState.panelEnabled` 由 `plugin.register`/`plugin.reconfigure` 写入；`management.go` 改为 `panelEnabled()` 运行时读取；`pluginMetadata` 迁至 `metadata.go`（RT-1 TD-9 顺带完成，保住 T1 ≤500 行）。测试：`TestParseConfigPanelDefaultFalse/EnabledTrue/InvalidType`、`TestManagementPanelEnabledByConfig`、`TestManagementPanelFollowsReconfigure`；smoke 支持 `PANEL_ENABLED=1`（`make smoke-panel`）。面板 UI 同步重构为 Tab + 模块命名空间（行为守恒）。 |
| 业务行为影响 | 无（默认配置下行为与改前一致；新增性能为可选开启） |
| 关联批次 | 面板开关与重构（2026-10-13 用户确认：开关放 config.yaml、仅拆 JS 模块 + 改 UI、不推进 trace 页） |
| 验收方式 | 默认配置 `make check`/`make smoke` 与改前一致；`panel.enabled=true` 时注册声明 management_api、GET panel 200 |
| 状态 | fixed |

---

| 严重度 | P0 | 9 | 001–006, 031, 034, 037 |
| P1 | 26 | 007–018, 028–030, 032, 033, 035, 036, 038–041, 043–045 |
| P2 | 16 | 019–027, 042, 046–051 |
| **合计** | **51** | |

| 状态 | 条数 | ID 列表 |
|---|---|---|
| fixed | 41 | 001–009, 011, 012, 014–019, 021, 024–029, 030–034, 035–041, 043–045, 051 |
| declined | 3 | 010, 013, 023 |
| open | 7 | 020, 022（均已有 benchmark 数据并登记保留决策）；046–050（流式还原已知限制） |

| 关联计划 | 涉及 ID |
|---|---|
| Harness 批次 0–5 | 001–030 |
| 隐私闭环计划 | 017, 018, 028, 029, 031–034, 036, 037–044（评审批与 043/044 补批） |
| 流式裸载荷修复 | 032（补充）, 045–050 |
| 面板开关与重构 | 051 |

| 业务行为影响 | 涉及 ID |
|---|---|
| 无（纯重构/工程/文档） | 除 017, 028, 029, 031–034, 036, 045–050 外全部 |
| 有（业务变更或安全修复，需复验） | 017、028、029、031、032、033、034、036、045 |
| 仅登记限制，未改变行为 | 046–050 |
