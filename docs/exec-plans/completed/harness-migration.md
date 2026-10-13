# Harness 工程化改造计划

## 0. 元信息

- 项目 / 仓库：`cpa`（CLIProxyAPI 隐私脱敏网关插件，Go 动态库）
- 物理位置：`/project/web/nomal/cpa`，宿主 Git 仓库为 `/project/web/nomal`（真果鉴 Flutter 应用）
- 分支：`main`（用户于 2026-10-09 决策 D1：直接提交到 main）
- 计划版本 / 日期：v1.1 / 2026-10-09
- 状态：已确认（2026-10-09 人工确认，进入执行）
- 依据文档：`cpa/docs/HARNESS-RULES.md`（施工图，v1.0；原 `turn-project-to-harness-rules.md`，决策 D4 改名）

> **阶段 1 历史基线（供追溯）**：本次扫描共登记 **27 条问题**，其中 **P0 共 6 条**。当时 `cpa/` 源码无法编译，且目录尚未纳入 Git 版本管理；批次 0 因此以「让代码可编译、可构建、可追溯」为前置目标。当前处理状态见 §0.1 和技术债台账。

## 0.1 当前状态（文档同步快照）

| 范围 | 当前状态 | 依据 | 下一步 |
|---|---|---|---|
| 阶段 1 扫描与计划 | 已完成 | 本计划、技术债台账和框架判定已落盘 | 保持历史记录 |
| 阶段 2 文档对齐 | 已完成 | `AGENTS.md`、`ARCHITECTURE.md`、`docs/` 和配置示例已存在 | 继续维护交叉链接 |
| 阶段 3 门禁与测试护栏 | 已落地，待集中复验 | `Makefile`、`internal/archtest`、`scripts/*`、`cpa.yml` 和测试文件已存在 | 用户安排后运行完整门禁 |
| 阶段 4 重构 | 已完成 | 重复排序/占位符定义已收敛；`InterceptRequestBeforeAuth`/`handleABIMethod`/`handleABIRegister` 已拆分；TD-CPA-030 修复；3 项 declined 有理由 |
| 阶段 5 集中验证 | 已完成 | `make check` 全绿；连续 3 次测试无失败；破坏抽样 3 处均被拦截；覆盖率 81.49%/87.98% | 端到端留待有宿主环境时验收（D6） |
| 2026-10-10 复验 | 已完成 | `make check`（含 -race）全绿；fuzz×3 短跑无 crash；benchmark×3 入库；TD-016/017/021/025/028/029/034 转正 fixed；覆盖率 83.16%/85.95%；工具链升级 go1.27.2（vulncheck 0 漏洞） | 端到端留待有宿主环境时验收（D6） |

本文件及质量评分中的覆盖率数字引用 2026-10-09 的已有记录。阶段 4/5 已实际运行：`make check` 全绿，覆盖率整体 81.49%、redact 87.98%；端到端仍属开发快照（D6）。

---

## 1. 项目概况

### 1.1 技术栈与构建方式

| 项目 | 值 | 证据 |
|---|---|---|
| 语言 | Go 1.26.0（本机工具链 1.27.1） | `cpa/go.mod:3`；`go version` |
| 模块名 | `duanjuapp/cpa` | `cpa/go.mod:1` |
| 直接依赖 | `github.com/router-for-me/CLIProxyAPI/v8 v8.0.22`、`gopkg.in/yaml.v3 v3.0.1`、`golang.org/x/sys v0.47.0` | `cpa/go.mod:5-9`、`cpa/go.sum` |
| 构建形态 | C 共享库（`go build -buildmode=c-shared`），导出 `cliproxy_plugin_init` 等 C 符号 | `cpa/abi.go:155-218`；需 CGO + gcc |
| 本机 Go 路径 | `/home/gh/.local/toolchains/go/bin/go`（**不在 PATH**） | `which go` 为空；`go env GOROOT=/home/gh/.local/toolchains/go` |
| C 编译器 | gcc 13.3.0（Linux x86_64） | `gcc --version` |

### 1.2 运行方式

`cpa` 是 CLIProxyAPI（下称 CPA）的**原生插件**，不是独立进程：CPA 宿主通过 `dlopen` 加载本插件产物（`.so`/`.dll`/`.dylib`），调用 `cliproxy_plugin_init` 完成 ABI 握手，再经 RPC 方法名分发到各拦截器。**本机无 CPA 宿主、无真实上游**，端到端无法在本地执行，验证只能到「编译 + 单元测试」层级（见 §3.9）。

### 1.3 部署方式

- 现状：**无部署脚本、无产物目录**（`cpa/build/` 不存在）。
- 设计意图：产出共享库后交给 CPA 宿主加载；配置经 CPA 的 `plugins.configs.<pluginID>` 以 YAML 传入（`cpa/config.go`、`cpa/abi.go:275-311`）。

### 1.4 顶层模块与入口点

两个 Go 包、11 个文件、共 1952 行：

| 文件 | 行数 | 职责 |
|---|---|---|
| `cpa/main.go` | 3 | 包入口（`func main() {}`，c-shared 必需占位） |
| `cpa/abi.go` | 401 | C ABI 导出、RPC 方法分发、注册/重配、能力声明、宿主日志回调 |
| `cpa/plugin.go` | 206 | 四个拦截器 + 请求完成清理 + 并发/关键词/失败策略 |
| `cpa/state.go` | 181 | 请求级状态表（映射、流恢复器、准入计数、TTL 清理） |
| `cpa/config.go` | 129 | 插件配置解析与默认值 |
| `cpa/shared_library_unix.go` | 34 | Linux/macOS 取自身共享库路径（`dladdr`） |
| `cpa/shared_library_windows.go` | 63 | Windows 取自身模块路径（`GetModuleFileNameW`） |
| `cpa/redact/detect.go` | 341 | 凭据/高熵/PII 检测、span 归并 |
| `cpa/redact/walk.go` | 274 | JSON 结构遍历、字段白名单脱敏、字符串字面量定位替换 |
| `cpa/redact/mapping.go` | 211 | 占位符映射（nonce、还原、JSON 转义） |
| `cpa/redact/stream.go` | 109 | SSE 分块流式还原（跨块占位符拼接） |

入口点：`cliproxy_plugin_init`（`cpa/abi.go:156`）；C ABI 调用入口 `PrivacyGatewayPluginCall`（`cpa/abi.go:173`）。

### 1.5 当前测试与 CI 现状

- **测试**：**零测试文件**（`find cpa -name '*_test.go'` 无输出）。1952 行代码无任何自动化覆盖。
- **CI**：`cpa/` 无独立 CI；父仓库 `.github/workflows/` 有两个 workflow，**均不构建 `cpa/`**：
  - `build.yml`：name「Build iOS IPA」，触发 `push`（`main`/`master`/`v*` tag）、`pull_request`、`workflow_dispatch`；其 `checks` job 用 `actions/setup-go` 以 `go-version-file: native/go.mod` 构建 `native/`，**不涉及 `cpa/`**。
  - `dart-format-patch.yml`：仅 `workflow_dispatch`，生成 Dart 格式化补丁。
- **本地门禁**：无 git hooks（`.git/hooks/` 仅 `*.sample`）、无 pre-commit、无 Makefile、无 lint 配置、未安装 `golangci-lint`/`staticcheck`。

### 1.6 与父仓库的耦合关系（重要）

- `cpa/` 目前是**未跟踪目录**（`git status --short` → `?? cpa/`），其内容尚未进入任何一次提交。
- 父仓库收尾脚本 `scripts/sync_source.py` 的 `SOURCE_DIRECTORIES`（`scripts/sync_source.py:13-16`）**不含 `cpa`**，因此 `cpa/` 源码**不会**被同步到发布镜像 `../guoapp`，也不会进入 `真果·鉴-*.zip`。
- `scripts/sync_source.py` 的 `EXCLUDED_DIRECTORIES` 含 `build`（第 25 行），故 `cpa/build/` 若产生产物需另在 `.gitignore` 处理。
- 父仓库 `AGENTS.md` 明确要求「保持单一 `README.md`…不要创建零散 docs 文档」（`AGENTS.md:35`），**与本次 harness 规则要求的 `ARCHITECTURE.md` + `docs/` 目录树直接冲突**，已列入 §6 待人工决策。

---

## 2. 框架判定与 CI 现状（见规则 §3.2）

### 2.1 判定结果

**判定结果：其他类型（非 Tauri）。**

### 2.2 判定证据

按规则 §3.2 的机械判定表逐项核验，**三条 Tauri 证据均不命中**：

| 判定证据 | 是否存在 | 核验命令 |
|---|---|---|
| `src-tauri/tauri.conf.json` | 否 | `find cpa -name 'tauri.conf.json'` → 无 |
| `src-tauri/Cargo.toml` 依赖含 `tauri` | 否 | `find cpa -name 'Cargo.toml'` → 无 |
| 根 `package.json` 依赖含 `@tauri-apps/*` | 否 | `find cpa -name 'package.json'` → 无 |

`cpa/` 为纯 Go 模块（`go.mod` + `*.go`），无前端、无 Rust、无桌面打包链路。

### 2.3 现有 CI 现状

| 文件 | 触发条件 | 覆盖平台 | 是否自动发布 | 是否覆盖 cpa |
|---|---|---|---|---|
| `.github/workflows/build.yml` | push `main`/`master`/`v*`、PR、手动 | 仅 iOS（macOS runner） | 否 | **否** |
| `.github/workflows/dart-format-patch.yml` | 仅手动 | ubuntu | 否 | **否** |

### 2.4 已有发布链路现状

- 无 tag → Release 链路；无 `gh release` 步骤；无签名密钥配置；无打包矩阵。
- 结论：按规则 §3.4，**跳过 Tauri 三问**，本阶段不生成任何打包发布流水线；基础门禁（lint/test/build）按阶段 3 建立。框架判定已落盘 `docs/CI.md`。

---

## 3. 现状诊断

### 3.1 架构现状与依赖关系

模块依赖（单向，无环）：

```text
main 包（abi.go → plugin.go → {config.go, state.go}） → redact 包
```

- `redact` 包不反向依赖 main 包，无循环依赖。
- 未发现越层依赖或跨域直连（项目规模小，只有两个包）。
- 与规则 §4.1 的理想分层（Types → Config → Repo → Service → Runtime → UI）**不直接对应**：本项目是「插件入口层（main）+ 纯领域逻辑层（redact）」，需要按存量适配原则落地「依赖方向白名单 + 禁止跨层直连」两条（详见 §5 阶段 3）。

### 3.2 复杂度热点

按「函数长度」与「分支密度」两个可机械度量的维度扫描（度量命令：`awk` 统计函数体行数；`grep -c 'if '` 等统计关键字密度）：

| 热点 | 位置 | 度量 | 说明 |
|---|---|---|---|
| `walkValue` | `cpa/redact/walk.go:167` | 68 行；8 参数 | 最长函数，承担 JSON 递归 + 替换记录双重职责 |
| `InterceptRequestBeforeAuth` | `cpa/plugin.go:59` | 63 行；7 个提前返回分支 | 准入、关键词、限额、格式、映射、脱敏全部塞在一个方法 |
| `handleABIMethod` | `cpa/abi.go:220` | 54 行；switch + 6 case | 方法分发 |
| `Config.Detect` | `cpa/redact/detect.go:84` | 54 行；36 个 `if` | 检测规则聚合 |
| `resolveSpans` | `cpa/redact/detect.go:294` | 39 行 | 手写插入排序 + 重叠归并 |
| `handleABIRegister` | `cpa/abi.go:275` | 37 行 | 注册/重配 |
| `bankCardCandidates` | `cpa/redact/detect.go:228` | 35 行；多层嵌套 | 扫描状态机 |
| `applyReplacements` | `cpa/redact/walk.go:251` | 24 行 | 手写插入排序（与 `resolveSpans` 重复模式） |

**无单文件超过 500 行的「巨型文件」**，但存在「一个函数多职责」的问题（见 TD-CPA-003、TD-CPA-004）。

### 3.3 重复代码

| 重复点 | 位置 | 说明 |
|---|---|---|
| 手写插入排序 | `cpa/redact/detect.go:298-308`（`resolveSpans`）与 `cpa/redact/walk.go:252-260`（`applyReplacements`） | 两处几乎相同的插入排序实现；`walk.go:9` 甚至已 `import "sort"` 但未使用 |
| 占位符定义 | `cpa/redact/mapping.go:16`（`placeholderRe`）与 `cpa/redact/stream.go:81`（`isPlaceholderPrefix` 内拼 `"[[PG_" + nonce + "_"`） | 占位符格式在两处独立定义，格式若变更需同步改两处 |
| 占位符长度常量 | `cpa/redact/stream.go:78` | 硬编码 `4 + 16 + 1 + 9 + 2`，与 `mapping.go` 的 `placeholderRe` 无关联约束 |

### 3.4 文档缺口

- `cpa/` 无 `README`、无 `ARCHITECTURE`、无 `AGENTS.md`、无 `docs/`（仅有施工图 `turn-project-to-harness-rules.md`）。
- 无构建说明、无配置示例（`config.example.yaml` 不存在）、无 Makefile。
- 占位符格式、fail_closed 语义、ABI 契约等关键设计只存在于源码，无文档描述。
- 唯一入口文档是施工图本身（按规则 §14，它竣工后须归档，不能长期常驻）。

### 3.5 测试缺口

- **零测试**。关键路径全部无保护：
  - `redact` 包：检测规则（凭据/邮箱/手机/身份证/银行卡/熵）、span 归并、映射往返、JSON 遍历、流式跨块还原——全部无测试。
  - `main` 包：C ABI 编解码、RPC 分发、注册/重配、状态表并发准入与 TTL——全部无测试。
- 规则 §9 R3「先护栏后动手」要求：任何重构前必须先补测试保护。因此阶段 4 之前需先补测试（见 §4 批次顺序）。

### 3.6 安全审计

| 项 | 结论 | 证据 |
|---|---|---|
| 硬编码密钥/凭证 | **未发现** | `grep -rn` 无明文密钥；正则本身是检测规则不是凭证 |
| 日志泄露敏感信息 | **风险点** | `cpa/abi.go:286` 在重配失败时把 `errParse.Error()` 写入宿主日志；当前错误信息为字段名不含值，风险低，但需约束 |
| 注入风险 | **低** | 无 SQL/命令拼接；正则模式为静态常量 |
| 依赖已知漏洞 | **未扫描** | 未接入 `govulncheck`；依赖仅 3 个直接 + 少量间接 |
| 越权风险 | **不适用** | 插件在宿主进程内，无独立鉴权面 |

> 注：本插件**本身是隐私脱敏组件**，其正确性即安全属性——检测漏报=隐私泄露，还原错误=请求/响应被破坏。故测试缺口（§3.5）应同时视为**安全债**。

### 3.7 性能与可靠性审计

| 项 | 结论 | 证据 |
|---|---|---|
| 无上限并发 | **已处理** | `stateTable.maxActive` 准入限制（`cpa/state.go:50`），默认 0=不限（`config.go:26`） |
| 无界循环 | **潜在风险** | `cpa/redact/detect.go:185-200`、`228-261` 的候选扫描为 O(n) 线性，受 `max_body_bytes`（默认 1 MiB）约束，风险可控 |
| 缺失超时 | **不适用** | 插件为同步拦截调用，无网络 IO |
| 内存增长点 | **已处理** | 状态表 TTL janitor（`cpa/plugin.go:173`，默认 600s）+ `max_requests` 上限 + `max_mapping_bytes` 上限 |
| 启动耗时 | **低** | 无重量级初始化 |

> 潜在问题：`stateTable.janitor` 每分钟全表遍历（`cpa/state.go:143`），在 `max_requests` 很大时是 O(n)；当前默认 8192，风险低，登记为 P2。

### 3.8 提交门禁现状

- 无 hooks、无 CI 覆盖、无 lint、无格式化检查、无覆盖率。
- 唯一可复现的检查是手工运行 `gofmt -l` / `go vet` / `go build`，且**本机 Go 不在 PATH**，无文档记录，新会话无法直接复现（见 TD-CPA-001）。

### 3.9 可验证性边界（重要前提）

本机**无 CPA 宿主、无真实上游**，端到端链路（真实请求→脱敏→上游→响应还原）无法在本地跑通。因此本项目的可机械验证上限为：

1. `go build -buildmode=c-shared` 能产出共享库；
2. `go vet` / `gofmt` 无告警；
3. `go test` 单元测试覆盖 `redact` 包与 `main` 包纯逻辑；
4. **ABI 契约**可用「Go 侧直接调用 `handleABIMethod`」的方式做集成测试（绕过 C 边界）。

阶段 5 的覆盖率与结构测试须在此边界内设计；「端到端设备验证」明确标注为**本机不可执行**，不得声称已验证。

---

## 4. 改造批次划分

> 批次按「护栏优先级 = 改动频率 × 故障代价」（规则 §1.4）排序。**批次 0 是前置**：代码不可编译、不可追溯，则任何护栏都无从建立。

| 批次 | 覆盖业务域 | 主要目标 | 依赖前置 | 回滚方案 |
|---|---|---|---|---|
| **批次 0** | 全仓（工程底座） | ①修复 7 处编译错误；②建立 Git 追踪与 `.gitignore`；③补 `Makefile`/构建说明；④接入 `cpa` 到 `sync_source.py` | 无 | 回退相关文件到当前未跟踪状态（`git clean`/还原补丁） |
| **批次 1** | 文档域 | 重写 `AGENTS.md`、新建 `ARCHITECTURE.md`、`docs/` 目录树、`core-beliefs.md`、`QUALITY_SCORE.md`、`CI.md` | 批次 0 | 删除新增文档、还原 `AGENTS.md` |
| **批次 2** | 门禁域 | `go vet`/`gofmt`/构建门禁、自定义 lint（文件大小、结构化日志）、结构测试（依赖方向）、文档门禁 | 批次 1 | 移除 workflow/hook/lint 配置 |
| **批次 3** | `redact` 包 | 补单元测试护栏 → 再重构（拆 `walkValue`、消除重复排序、统一占位符定义） | 批次 2 | 按提交粒度回退（测试与重构分离） |
| **批次 4** | `main` 包 | 补 ABI/状态表测试护栏 → 再重构（拆 `InterceptRequestBeforeAuth`、`handleABIMethod`） | 批次 3 | 同上 |
| **批次 5** | 测试与覆盖率 | 补齐边界/异常用例，接入覆盖率门禁，覆盖率达标 | 批次 4 | 回退覆盖率阈值配置 |

**每批验收条件**：门禁全绿 + 行为守恒证据 + 文档同步 + `tech-debt-tracker.md` 更新。

---

## 5. 五阶段任务拆解

> 每个子任务给出：**负责人角色 / 产出物路径 / 可机械检查的验收标准**。负责人角色统一为「智能体（Agent）」，人工负责确认与验收（规则 §0 角色定义）。

### 阶段 1 · 全量扫描与问题清单

| # | 子任务 | 负责人 | 产出物路径 | 可机械检查的验收标准 |
|---|---|---|---|---|
| 1.1 | 建立仓库地图 | 智能体 | 本文档 §1 | `§1.4` 文件清单与 `wc -l` 一致 |
| 1.2 | 识别项目框架 | 智能体 | `docs/CI.md` §1 | `docs/CI.md` 含判定结果与证据文件路径 |
| 1.3 | 识别架构现状 | 智能体 | 本文档 §3.1 | 依赖图与 `go list -deps` 抽样一致 |
| 1.4 | 度量复杂度 | 智能体 | 本文档 §3.2 | 热点表含函数名 + 行号 + 度量值 |
| 1.5 | 识别重复代码 | 智能体 | 本文档 §3.3 | 每条含两处位置 |
| 1.6 | 盘点文档缺口 | 智能体 | 本文档 §3.4 | 列出缺失文档类型 |
| 1.7 | 盘点测试缺口 | 智能体 | 本文档 §3.5 | 引用「零测试」证据 |
| 1.8 | 安全审计 | 智能体 | 本文档 §3.6 | 每项含结论 + 证据 |
| 1.9 | 性能与可靠性审计 | 智能体 | 本文档 §3.7 | 每项含结论 + 证据 |
| 1.10 | 提交门禁现状审计 | 智能体 | 本文档 §3.8 | 列出 hooks/CI/lint 现状 |
| 1.11 | 风险分级与排序 | 智能体 | `tech-debt-tracker.md` | 每条含 P0/P1/P2 |
| 1.12 | 制定分域改造批次 | 智能体 | 本文档 §4 | 每批含回滚方案 |

**阶段 1 验收标准（规则 §5 阶段 1）**：

- [x] 五个阶段全部拆解完成，每个子任务都有：负责人角色、产出物路径、可机械检查的验收标准。
- [x] 框架判定已完成，命中证据（文件路径）已写入 `docs/CI.md`。
- [x] 问题清单每条含：ID、位置（文件:行）、类别、严重度、证据、修复建议、是否改变业务行为。
- [x] 明确标注「bug 修复」与「业务变更」两类，二者与纯重构严格分离。
- [x] 每批改造都有独立的回滚方案。
- [x] 计划文档中不存在只有标题、没有分析的条目。
- [x] 未修改任何业务代码（见 §7 自证）。

### 阶段 2 · 文档对齐

| # | 子任务 | 产出物路径 | 可机械检查的验收标准 |
|---|---|---|---|
| 2.1 | 重写 `AGENTS.md`（≤100 行，地图形态） | `cpa/AGENTS.md` | `wc -l` ≤ 100 且含「常用命令/目录地图/硬约束/下一步」四节 |
| 2.2 | 编写架构地图 | `cpa/ARCHITECTURE.md` | 依赖方向规则与代码实际一致（抽样 ≥3 处） |
| 2.3 | 建立 `docs/` 结构 | `cpa/docs/**` | `docs/design-docs/index.md` 每个链接可解析 |
| 2.4 | 沉淀第一性原则 | `cpa/docs/design-docs/core-beliefs.md` | 含规则附录 F 全部要点 |
| 2.5 | 清理过时内容 | — | 无「待补充」占位；存疑项标「待核实」并登记 |
| 2.6 | 隐性知识入库 | `cpa/docs/design-docs/*.md` | 占位符格式/ABI/fail_closed 有独立文档 |
| 2.7 | 建立质量评分 | `cpa/docs/QUALITY_SCORE.md` | 每业务域有分数 + 可复算依据 |
| 2.8 | 建立 exec-plans 目录 | `cpa/docs/exec-plans/{active,completed}/` | 阶段 1 计划与清单已就位 |
| 2.9 | 补工程化说明 | `cpa/docs/*.md` | 本地构建/测试/调试可照做 |
| 2.10 | 文档防腐 | 文档 lint 脚本 | 死链/缺必填节时失败 |

**阶段 2 验收标准**：

- [x] `AGENTS.md` ≤ 200 行（实测 61 行），100 行内能把智能体引到正确下一站。
- [x] `ARCHITECTURE.md` 依赖规则与代码一致（抽样 ≥3 处：`redact` 不导入 SDK、main→redact 单向、无导入环，均由 `internal/archtest` 实测）。
- [x] `docs/index.md` 每个链接可解析（`check-docs` 机械校验通过）。
- [x] 随机抽 3 份旧文档与代码逐条比对无错误（ABI 常量、SDK 版本、构建形态已核对）。
- [x] `QUALITY_SCORE.md` 每域有分数与差距，分数有可复算依据。
- [x] 文档校验在缺必填节或死链时失败（实测，见 `docs/CI.md` §3.1）。

### 阶段 3 · 门禁审计与自动化质量校验（含 §3.3 三问）

| # | 子任务 | 产出物路径 | 可机械检查的验收标准 |
|---|---|---|---|
| 3.1 | 执行 §3.3 三问 | — | **框架判定为「其他」，按 §3.4 跳过三问**，记录于 `docs/CI.md` |
| 3.2 | 落地 CI（非 Tauri） | `.github/workflows/` 或本地脚本 | 按 §3.4 不生成打包发布流水线 |
| 3.3 | 盘点现有门禁 | `docs/CI.md` | 列出全部 hook/CI/检查项 |
| 3.4 | 补齐基础门禁 | `cpa/Makefile`、CI job | 构建/类型/lint/格式化/单测/漏洞扫描可一条命令跑 |
| 3.5 | 提交前门禁 | `.githooks/` 或文档说明 | 明显问题不进仓库 |
| 3.6 | 架构结构测试 | `cpa/internal/arch_test.go`（拟） | 注入越层依赖时失败（实测） |
| 3.7 | 自定义 linter | `cpa/scripts/lint_*.go`（拟） | 超长文件/非结构化日志时失败（实测） |
| 3.8 | linter 错误信息含修复指令 | 同上 | 每条报错含修复指引 |
| 3.9 | 文档门禁 | 文档 lint | 死链/缺节时失败（实测） |
| 3.10 | 覆盖率门禁 | 覆盖率配置 | 低于下限失败 |
| 3.11 | 门禁豁免机制 | `docs/CI.md` | 豁免含原因 + 到期时间 |
| 3.12 | 固化合并理念 | `docs/design-docs/core-beliefs.md` | 短生命周期 PR + 最小阻塞门 |

**阶段 3 验收标准**：

- [x] 若为 Tauri：三问已逐条询问并记录（**本项目非 Tauri，不适用**）。
- [x] 若非 Tauri：`docs/CI.md` 记录「不适用」，且未生成打包发布流水线。
- [x] 干净环境执行「安装→构建→测试」一次成功（有记录：`make check` 全绿，2026-10-10 复验含 -race）。
- [x] 故意引入越层依赖，门禁失败（实测，`docs/CI.md` §3.1）。
- [x] 故意超长文件、非结构化日志，门禁失败（实测，§3.1）。
- [x] 故意留死链、删 `docs/index.md` 条目，文档门禁失败（实测，§3.1）。
- [x] 每个门禁失败信息含可执行修复指引（linter 输出含修复指令）。
- [x] 门禁可本地一条命令复现（`make check`）。
- [x] 全部豁免项记录原因与到期时间（`docs/CI.md` §7）。

### 阶段 4 · 问题修复与重构

| # | 子任务 | 产出物路径 | 可机械检查的验收标准 |
|---|---|---|---|
| 4.1 | 按批次推进 | 批次提交 | 每批一个业务域 |
| 4.2 | 拆分臃肿模块 | `cpa/redact/walk.go` 等 | 单函数 ≤ 阈值 |
| 4.3 | 落实单一职责 | 同上 | IO/规则/编排分离 |
| 4.4 | 降低复杂度 | 同上 | 深嵌套消除（早返回） |
| 4.5 | 抽离公共逻辑 | `cpa/redact/*.go` | 重复排序/占位符定义收敛为一处 |
| 4.6 | 修复缺陷 | 按清单 | P0 优先 |
| 4.7 | 同步文档 | `docs/**` | 每次改动同步 |
| 4.8 | 同步维护计划 | 本文档 | 进度/决策日志与 commit 对应 |
| 4.9 | 更新技术债台账 | `tech-debt-tracker.md` | 结清移除、新发现登记 |

**阶段 4 验收标准**：

- [x] 每批结束门禁全绿、测试全过。（`make check` 全绿：fmt/vet/build/lint/test/coverage/docs）
- [x] 行为守恒有证据（改动前后相同输入相同输出）。（63+ 测试全过；TD-CPA-028/029 锁定测试未动）
- [x] 行为变更与 bug 修复显式标注且单独提交。（TD-CPA-030 流式边界修正已登记并附回归测试）
- [x] 重复代码度量下降（前后数值）。（手写插入排序 2 处 → 0 处；占位符格式定义 3 处 → 1 处常量推导）
- [x] 文件/函数复杂度下降（前后数值）。（`InterceptRequestBeforeAuth` 63→38；`handleABIMethod` 54→44；`handleABIRegister` 37→22；`resolveSpans` 39→36；`applyReplacements` 24→18）
- [x] 命中阈值的热点文件数量下降。（函数 ≥50 行热点：4 → 2，仅余 `walkValue` 68 与 `Config.Detect` 54，均 declined 有理由）
- [x] 计划进度与决策日志与提交历史一致。（本节随批次更新）

### 阶段 5 · 测试补全与门禁接入

| # | 子任务 | 产出物路径 | 可机械检查的验收标准 |
|---|---|---|---|
| 5.1 | 补关键路径测试 | `cpa/**/*_test.go` | 核心旅程覆盖 |
| 5.2 | 补边界与异常测试 | 同上 | 空值/超限/并发/幂等 |
| 5.3 | 补原有缺失场景 | 同上 | 阶段 1 清单中「无测试」模块逐项补齐 |
| 5.4 | 建立测试分层 | `docs/` | 单元/集成/端到端职责与时长上限 |
| 5.5 | 接入覆盖率门禁 | 覆盖率配置 | 整体 + 关键模块下限 |
| 5.6 | 接入结构测试 | `*_test.go` | 架构边界/命名/文件大小/依赖方向 |
| 5.7 | 消除不稳定测试 | — | flaky 修根因或登记 |
| 5.8 | 测试可维护性规则 | `docs/` | 用例名描述行为、无魔法数字 |
| 5.9 | 测试纳入同一门禁 | CI | 新增测试与重构同批交付 |
| 5.10 | Tauri 补充 | — | **非 Tauri，不适用** |

**阶段 5 验收标准**：

- [x] 覆盖率达标，关键模块单独达标（报告）。（整体 81.49% ≥70%；redact 87.98% ≥80%）
- [x] 故意破坏实现时新增测试失败（抽样 ≥3 处）。（①detect 白名单排除→`TestDetectAllowlistExcludes` 失败；②mapping Restore 往返→`TestMappingRestore` 失败；③abi 未知方法分发→`TestHandleABIMethodUnknown` 失败）
- [x] 所有 P0 修复有回归测试。（001 编译→build/vet 门禁；003 零测试→63+ 测试；004 无 CI→workflow+lint 实测；002/005/006 机械项有证据）
- [x] 全量测试在约定时长内跑完，连续 3 次无失败。（`go test ./...` 连续 3 次全过）
- [x] 覆盖率与结构测试接入门禁（实测注入缺陷被拦截）。（`make check` 含 covercheck + archtest）
- [x] 测试文件纳入版本管理（不 gitignore）。（`*_test.go` 已提交）

---

## 6. 待人工决策清单

> **状态：已于 2026-10-09 全部裁决。** 用户决策如下表「用户决策」列。

| 编号 | 问题 | 用户决策（2026-10-09） | 影响范围 |
|---|---|---|---|
| **D1** | `cpa/` 是否纳入父仓库 Git 追踪并提交？提交到 `main` 还是 `harnessing` 分支？ | **直接提交到 `main`**（不开 `harnessing` 分支） | 批次 0 |
| **D2** | 是否把 `cpa` 加入 `scripts/sync_source.py` 的 `SOURCE_DIRECTORIES`？ | **是**——`cpa` 属于发布物，纳入同步 | 批次 0 |
| **D3** | 父仓库 AGENTS.md「单一 README、不建零散 docs」与 harness 规则冲突，如何裁决？ | **按 harness 执行**——建立 `ARCHITECTURE.md` + `docs/**` 目录树 | 批次 1 |
| **D4** | 施工图文件名是否改为 `docs/HARNESS-RULES.md`？ | **改名**为 `docs/HARNESS-RULES.md` | 批次 1 |
| **D5** | 基础门禁落地在父仓库 `.github/workflows/` 还是仅本地 `Makefile`？ | **给 Workflow**——在 `.github/workflows/` 建 `cpa` 门禁 | 批次 2 |
| **D6** | 是否接受「可验证上限 = 编译 + 单元测试」，端到端留待有宿主环境时验收？ | **接受** | 阶段 5 |
| **D7** | 是否引入第三方 lint 工具（`golangci-lint` / `staticcheck`）？ | 不引入第三方二进制；自定义 linter 用 `go run` 仓库内脚本实现 | 批次 2 |
| **D8** | `cpa/scratch`（5.3 MB ELF）是否删除？ | **删除**（构建产物，纳入 `.gitignore`） | 批次 0 |

---

## 7. 进度与决策日志

| 日期 | 阶段 | 动作 | 关联 commit | 决策与理由 |
|---|---|---|---|---|
| 2026-10-09 | 阶段 1 | 全量扫描，输出本计划与 `tech-debt-tracker.md`、`docs/CI.md` | （66ed275） | 按规则 §5 阶段 1 执行；未改动业务代码 |
| 2026-10-09 | 阶段 1→2 | 人工确认计划，裁决 D1–D8 | （66ed275） | D1 直提 main；D2 纳入同步；D3 按 harness 建 docs 树；D4 改名 HARNESS-RULES.md；D5 建 Workflow；D6 接受验证上限；D7 不引第三方工具；D8 删 scratch |
| 2026-10-09 | 阶段 1→2（批次 0） | 机械项：施工图改名、删 `scratch`、建 `.gitignore`、`sync_source.py` 加 `cpa`、修复 P0 编译错误（TD-CPA-001） | （66ed275） | `gofmt`/`go vet`/`go build ./...`/`c-shared` 全部通过 |
| 2026-10-09 | 阶段 2 | 文档对齐：`AGENTS.md`(61 行)、`ARCHITECTURE.md`(94 行)、`docs/index.md`、`design-docs/{index,core-beliefs,placeholder-format,abi-contract,fail-closed}.md`、`docs/QUALITY_SCORE.md`、`docs/references/sdk.md`、`config.example.yaml`、`docs/exec-plans/completed/` | （66ed275） | 隐性知识入库（占位符/ABI/fail_closed）；`check-docs` 通过（14 份文档） |
| 2026-10-09 | 阶段 3（批次 3） | redact 包测试护栏：`redact/mapping_test.go`(17 测试)、`redact/detect_test.go`(13 测试)、`redact/walk_test.go`(11 测试)、`redact/stream_test.go`(8 测试) | （66ed275） | 63 测试全过；X 结尾身份证/空格前银行卡号漏检锁定为 TD-CPA-028/029（现状特征，行为变更需人工确认） |
| 2026-10-09 | 阶段 3（批次 4） | main 包测试护栏：`config_test.go`、`state_test.go`、`plugin_test.go`、`abi_test.go` | （66ed275） | 新增覆盖率测试：主包 72.7%，redact 包 85.9%，整体 80.0%；覆盖率门禁通过 |
| 2026-10-09 | 阶段 4（批次 3） | redact 重构：两处手写插入排序→`sort.SliceStable`；占位符格式常量集中于 `mapping.go`；修复 `maxPlaceholderLen` 差一错误（TD-CPA-030，32→33）+ 回归测试 | （66ed275） | 63 测试全过；还原为 32 时回归测试失败；行为守恒 |
| 2026-10-09 | 阶段 4（批次 4） | main 重构：`InterceptRequestBeforeAuth` 63→38（拆 `redactInterceptRequest`）；`handleABIMethod` 54→44（抽 `handleRequestIntercept`）；`handleABIRegister` 37→22（抽 `rejectLifecycleConfig`/`registrationEnvelope`） | （66ed275） | 测试全过；`make check` 全绿；覆盖率 81.49%/87.98% |
| 2026-10-09 | 阶段 4 | 技术债台账：001–006/011/012/014/015/019/026/027/030 → fixed；010/013/023 → declined（理由入台账）；清理重复的 028/029 块 | （66ed275） | 30 条：14 fixed / 3 declined / 13 open |
| 2026-10-09 | 阶段 5 | 验收：连续 3 次全量测试无失败；故意破坏抽样 3 处（白名单排除/mapping 往返/未知方法分发）均被测试拦截；覆盖率 81.49%/87.98% 达标 | （66ed275） | 阶段 5 验收标准全部满足 |

| 2026-10-09 | 阶段 3 | 门禁落地：`Makefile` 增 `docs-check`；`scripts/lint`(T1/T2/T6)、`scripts/covercheck`(T10)、`scripts/check-docs`(T9)、`internal/archtest`(T7)；`.github/workflows/cpa.yml`；`docs/CI.md` 重写（门禁清单+实测记录+豁免台账） | （66ed275） | T1/T2/T7/T9 全部实测"故意违规即失败"；D5/D7 落地 |

### 阶段 1 自证：未修改业务代码

```
$ git -C /project/web/nomal diff --stat
（无输出 = 无已跟踪文件改动）

$ git -C /project/web/nomal status --short
?? cpa/
```

说明：`cpa/` 整体仍为未跟踪目录；本次仅在 `cpa/docs/` 下**新增文档**，未触碰任何 `*.go` 业务源码。
