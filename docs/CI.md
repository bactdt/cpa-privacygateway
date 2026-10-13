# CI 与框架判定 · cpa

> 本文档依据《存量项目 Harness 工程化规则》附录 C 的字段要求，记录框架判定、CI 现状、门禁清单与落地说明。
> 关联文档：`exec-plans/completed/harness-migration.md`（改造计划）、`exec-plans/tech-debt-tracker.md`（问题清单）。
> 最后更新：2026-10-09（阶段 3 门禁审计与落地）。

## 1. 框架判定

**判定结果：其他（非 Tauri）。判定日期：2026-10-09。**

### 判定依据（规则 §3.2 机械判定表逐项核验）

| # | 判定证据 | 命中 | 核验命令 / 结果 |
|---|---|---|---|
| 1 | 存在 `src-tauri/tauri.conf.json` | 否 | `find cpa -name 'tauri.conf.json'` → 无输出 |
| 2 | `src-tauri/Cargo.toml` 依赖含 `tauri` | 否 | `find cpa -name 'Cargo.toml'` → 无输出 |
| 3 | 根 `package.json` 依赖含 `@tauri-apps/*` | 否 | `find cpa -name 'package.json'` → 无输出 |

补充事实：`cpa/` 为**纯 Go 模块**（`go.mod` + Go 源码），构建形态为 C 共享库（`-buildmode=c-shared`），无前端、无 Rust、无桌面打包链路。

### 结论

- 三条 Tauri 证据**全部不命中** → 判定为「其他」类型。
- 按规则 §3.4，**跳过 §3.3 的 Tauri 三问**，**不生成任何打包/发布流水线**（§3.3 交互确认记录见 §6，标注"不适用"）。
- **基础门禁照建**：lint / test / build 三件套按 §5 阶段 3 落地（见 §4、§7）。

## 2. 现有 CI 现状

| 文件 | 触发条件 | Runner | 构建对象 | 是否覆盖 `cpa/` | 是否自动发布 |
|---|---|---|---|---|---|
| `.github/workflows/build.yml` | `push`（`main`/`master`/`v*`）、`pull_request`、`workflow_dispatch` | macOS（iOS IPA） | `native/`（`go-version-file: native/go.mod`） | 否 | 是（tag） |
| `.github/workflows/dart-format-patch.yml` | 仅 `workflow_dispatch` | ubuntu | Dart 格式化补丁 | 否 | 否 |
| `../.github/workflows/cpa.yml` | `push`/`pull_request` 命中 `cpa/**`，或手动触发 | ubuntu | `cpa/` Go C 共享库与门禁 | **是** | 否 |

**审计结论**：原有两个 workflow 不覆盖 `cpa/`，现已新增 `../.github/workflows/cpa.yml` 覆盖插件门禁；原有 workflow 保留不动。该 workflow 位于父仓库 `/project/web/nomal/.github/workflows/`，不是插件目录内的文件。

## 3. 门禁清单（阶段 3 落地）

每项均可在本地一条命令复现（`make <target>`），CI 作业见 §4。

| 检查名 | 触发时机 | 失败含义 | 修复指引 | 本地复现 |
|---|---|---|---|---|
| `gofmt` 格式 | 提交前 / CI | 存在未格式化文件 | `make fmt` | `make fmt-check` |
| `go vet` | 提交前 / CI | 静态检查发现可疑代码 | 按告警逐条修正 | `make vet` |
| 构建共享库 | 提交前 / CI | 无法产出 `build/libprivacygateway.so` | 修复编译错误 | `make build` |
| 自定义 linter（T1/T2/T6） | 提交前 / CI | 文件超 500 行 / 非结构化日志 / 重复实现 | 见 linter 输出的修复指令 | `make lint` |
| 架构结构测试（T7） | 提交前 / CI | 分层依赖越界 / 领域层导入宿主 SDK / 导入环 | 见测试输出的修复指令 | `go test ./internal/...` |
| 数据竞态检测 | 提交前 / CI | `-race` 检出并发访问冲突 | 修复数据竞态 | `make race` |
| 单元测试 + 覆盖率下限（T10） | 提交前 / CI | 测试失败，或覆盖率低于下限 | 补测试 / 修实现 | `make coverage` |
| 文档校验（T9） | 提交前 / CI | 死链 / 索引缺失 / 必填节缺失 / 过期 | 见脚本输出的修复指令 | `make docs-check` |
| 依赖漏洞扫描（独立目标） | 手动 / 定期 | 调用路径存在已知漏洞 | 升级 Go 工具链或依赖 | `make vulncheck` |

**总入口**：`make check`（串联上述全部；`vulncheck` 因需要网络，不进入 `check` 串联，单独执行）。

### 3.1 门禁实测记录（规则 §5 阶段 3 要求"必须实测"）

| 实测项 | 注入的违规 | 期望 | 实测结果 |
|---|---|---|---|
| T1 文件大小 | 临时文件 522 行 | 失败 | ✅ 失败，报 `[T1-file-size]` + 修复指令 |
| T2 结构化日志 | 注入 `fmt.Println` | 失败 | ✅ 失败，报 `[T2-structured-log]` + 修复指令 |
| T7 依赖方向（未登记包） | 新增 `edgepkg` 导入 `redact` | 失败 | ✅ 失败，报"未在分层白名单登记" |
| T7 领域层纯净 | `redact` 导入宿主 SDK | 失败 | ✅ 失败，报"领域层违规" |
| T9 死链 | 在 `docs/index.md` 加死链 | 失败 | ✅ 失败，报"死链" + 修复指令 |
| T9 索引缺失 | 新增未被索引的文档 | 失败 | ✅ 失败，报"索引缺失" |

> 全部实测通过：门禁不仅能"配置上存在"，且**故意违规时确实失败**。

## 4. CI 落地说明

- **workflow 文件**：`.github/workflows/cpa.yml`（新建，父仓库内，与现有 `build.yml` 并存）。
- **触发条件**：`push` / `pull_request` 命中 `cpa/**` 或该 workflow 本身；支持 `workflow_dispatch`。
- **作业**：单 job `gates`，`runs-on: ubuntu-latest`，`working-directory: cpa`，依次执行 §3 全部检查。
- **Go 版本**：`go-version-file: cpa/go.mod`（当前 `go 1.27.2`；工具链升级修复 GO-2026-6613 等 4 项标准库漏洞）。
- **与目标平台的对应**：不适用（非 Tauri，无打包矩阵）。
- **与发布方式的对应**：不适用（无 Release 流水线）。
- **与现有配置的关系**：**新建**，不修改 `build.yml` / `dart-format-patch.yml`。

> 决策依据 D5：基础门禁落地在父仓库 `.github/workflows/`；D7：不引入 `golangci-lint`/`staticcheck` 等第三方二进制，使用仓库内 `go run ./scripts/*` 脚本。

## 5. 本地开发与发布

```sh
export PATH=/home/gh/.local/toolchains/go/bin:$PATH   # 本机 Go 不在 PATH
cd cpa
make check        # 本地门禁总入口
make build        # 产出 build/libprivacygateway.{so,dylib,dll}
make clean        # 清理产物
```

- **发布链路**：`cpa` 无独立发布流程。作为 CPA 插件，产物是 C 共享库，随 CPA 宿主部署。
- **源码同步**：`cpa` 已加入父仓库 `scripts/sync_source.py` 的 `SOURCE_DIRECTORIES`（决策 D2），源码进入发布镜像。
- **故障排查**：ABI 契约见 `design-docs/abi-contract.md`；失败策略见 `design-docs/fail-closed.md`。

## 6. 交互确认记录

非 Tauri 项目，规则 §3.3 的三个 CI 问题**不适用**，未逐条询问。

| 问题 | 结论 | 时间 |
|---|---|---|
| 是否生成 GitHub CI 配置 | 不适用（非 Tauri，跳过打包发布流水线）；基础门禁照建 | 2026-10-09 |
| 目标平台（多选） | 不适用 | 2026-10-09 |
| Release 发布方式 | 不适用 | 2026-10-09 |

## 7. 门禁豁免

| 豁免项 | 原因 | 到期时间 | 状态 |
|---|---|---|---|
| 覆盖率下限（整体 ≥70%、redact ≥80%） | 已有测试和覆盖率门禁；当前报告为整体 81.49%、redact 87.98%，本轮文档同步未重跑 | 用户安排集中复验 | 开发快照 |
| 依赖漏洞扫描（`govulncheck`） | 已解除：`make vulncheck` 独立目标（2026-10-10，`go run golang.org/x/vuln/cmd/govulncheck@latest`，需网络；当前 0 漏洞） | — | 已解除 |
| 端到端设备验证 | 本机与 CI 均无 CPA 宿主、无真实上游（决策 D6 接受） | 长期 | 生效中 |

> 规则：豁免必须显式、带原因、带到期时间，且可被审计。上表即为豁免台账。

## 8. 验收边界（重要）

本机**无 CPA 宿主、无真实上游**，端到端链路不可在本地或 CI 执行。可机械验证的上限为：

1. `go build -buildmode=c-shared` 产出共享库；
2. `go vet` / `gofmt` 无告警；
3. `go test ./...` 单元测试通过；
4. ABI 契约以「Go 侧直接调用 `handleABIMethod`」做集成测试（绕过 C 边界）。

「端到端设备验证」标注为**本机不可执行**，阶段 3/5 不得声称已验证（决策 D6）。
