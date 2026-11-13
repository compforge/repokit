# Makefile 验证

RepoKit 按 Component 规划和执行项目声明的 lint/test。前提是项目遵守下面的 Makefile 契约；
工具包不根据语言猜测试命令，也不通过 `make -n` 探测能力，因为 Make 在读取文件时就可能执行函数。
Go 复用已有 gotreesitter 的 Make grammar 静态读取规则和变量引用，不新增解析依赖；Python 保持
该约定子集的原生识别，双方共用契约语料。语法解析不等于求值，条件规则和动态目标不推断为可用入口。
分析与规划保持只读，执行是调用方明确发起的操作。下述组合执行、复用及报告 schema 2 为 Go/CLI 契约；Python 原生验证实现本轮保留，不迁移或扩展。

## Checkout 依赖准备（Python）

`inspect_dependencies(component_path)` 只读返回 `DependencyEnvironment`；`prepare_dependencies(environment)`
显式执行依赖安装。创建 worktree、inspect、snapshot 均不隐式准备环境；调用时机和验证门禁属于消费方。
原生 Python workflow 可使用该 API；Go/CLI 的 Depot 入口与结果契约见 [Depot](depot.md)。

观察区分 `ready`（准备回执与当前输入一致）、`present`（本地环境存在但未经回执验证）、
`missing`、`stale` 和 `unsupported`。消费方可允许用户自管的 present 环境进入实际验证，但不能称其
锁文件一致性已经验证。显式 prepare 会使用支持的锁定安装方式，并在成功后写环境目录内的回执。
无对应依赖声明的 Component 返回空列表；不会尝试安装语言运行时。

安装支持 npm ci、pnpm/Bun frozen install、Yarn classic frozen / modern immutable 和 uv sync --locked。
不生成或更新锁文件。环境状态与安装能力分开表达：目录不存在时为 `missing`；没有支持的锁定安装入口时
`command` 为空，显式 prepare 返回 `unsupported`。目录软链接或非目录占位为环境 `unsupported`；
观察错误直接报告，不能当作目录缺失。消费方依据这些事实决定是否直接运行项目检查。
Yarn PnP 暂不支持。Node package.json workspaces、pnpm-workspace.yaml 与 uv workspace 成员解析到本 checkout
内的安装根；嵌套 Component 不重复安装整套 workspace。成员排除生效，暂不支持 brace/extglob 成员表达式。

回执绑定绝对安装路径、锁文件、workspace 成员 manifest 与安装配置；成员新增或修改会使回执失效。
整个 node_modules/.venv 目录的外部链接不会被当成本地就绪环境，uv 不继承其它 checkout 的环境路径覆盖。
多个线程按安装根串行、锁内复查；默认安装预算 600 秒，受调用方 operation scope 的更短预算与取消控制。
输出有容量限制，中断会终止安装进程组，返回 uncertain，不宣称副作用已回滚。准备与 cleanup 共用 repository 级跨进程 lease。
安装非零退出、超时、未生成本地依赖或安装期间输入变化均不写 ready 回执，返回原因与可用的进程证据。

## Depot 与自动依赖证据

Go/CLI 从 common `Component.depot` 的 dependency Bin 自动收集 `depotWitnesses`。
目录缺失或回执不可用时只禁止复用，不阻止真实检查；安装根的源码子树保守纳入输入范围，
共享 workspace 锁文件不会被成员的显式 inputs 遗漏。规划、执行前后复核物理目录与回执，
outputs/cache 不参与依赖 witness。详细边界见 [Depot](depot.md)。

## 项目契约

Component 可在其根目录提供普通文件 `Makefile`（不接受符号链接），直接声明目标。
Go/CLI 的 lint/test 缺少本地目标时回退到根 Component，具体选择规则见下文：

- format：统一表示代码格式化，按 `format` → `fmt` → `fix` 的优先级选择第一个直接声明的 Makefile 目标，只执行一个。
  `fmt` / `fix` 是历史目标名，报告中的操作仍为 `format`，命令保留实际目标名。具体修改行为由项目 recipe 决定，
  包括历史 `fix` 中可能包含的自动修复。选中目标执行失败时不尝试其他别名；独立报告准备结果，不产生检查通过证据。
- lint：优先 `lint-ci`，其次 `lint`。
- test：优先 `test`，其次 `test-ci`、`test-local`。
- 目标返回 0 表示检查通过，非 0 表示检查失败；目标不修改参与验证的源码。
- 可选的 `LINT_FILES` / `TEST_FILES` 为 Component 相对路径的空格分隔列表。
  非空时检查这些文件，空值必须执行该目标的完整检查。
- 只有当前 Makefile 显式引用 `$(LINT_FILES)` / `${LINT_FILES}` 或相应 TEST_FILES 变量，
  才视为声明文件过滤支持。主 Makefile 需直接声明公开目标；不展开 include、变量目标或动态生成的规则。

例如 Python Component 的测试入口：

```make
.PHONY: test
# 空值时 pytest 发现全量测试；非空时按给出的测试文件运行。
test:
	uv run pytest $(TEST_FILES)
```

项目负责实现这些语义。检查通过与否由命令退出码决定；框架输出只提供可选的耗时观测，
不能从一个返回 0 的空 recipe 证明实际检查发生。
依赖安装应在验证前完成；Python 调用方可使用已有的 `inspect_dependencies` / `prepare_dependencies`。

### 本仓 Go 测试选择与耗时

`toolkit/go` 和 `apps/cli` 的 Makefile 接入 `TEST_FILES`：Go 文件按所属 package 去重后执行，
保留同包 helper、fixture 和其它测试文件；不把 `go test file_test.go` 当成文件过滤。
CLI 的 Node 测试按所选 `*_test.cjs` 文件执行；仅选择 Go 文件时不重复跑 Node，反之亦然。
空列表执行完整 suite；无法识别的文件类型保守执行完整 suite。`scope=focused` 描述文件选择请求，
具体执行粒度由 Makefile 决定，本仓 Go 粒度是 package，不承诺只跑该文件中的 test。

```sh
repocli test --repo . --component toolkit/go --file toolkit/go/state/clean_test.go --json
```

两个入口都使用 `go test -json`。Go 执行器从捕获的 stdout 提取有终态和 `Elapsed` 的事件，存入
`data.components[].checks[].testTimings[]`，原始 stdout/stderr 仍完整保留在捕获预算内。
不识别的输出、格式损坏或没有 elapsed 的事件不产生耗时条目，也不改变检查的判定和 scope。
中断时仅保留已经观测到的终态；没有条目不表示测试没有运行。

| 字段 | 含义 |
|---|---|
| `package` | Go package import path |
| `test` | Test/Example/Benchmark 名称；保留 subtest 的斜线层级；省略表示 package |
| `status` | `passed` / `failed` / `skipped` |
| `durationSeconds` | runner 报告的 elapsed，单位秒 |
| `cached` | Go 自身缓存重放；与整个 Check 的 `reused` 分开 |

文本输出每个检查最多展示 3 个 package、10 个 test 的慢项，优先展示本次真实运行的事件；JSON
保留所有提取到的明细。package、父 test/subtest 与并行 case 的耗时相互重叠，不能相加作为总成本，
也不增加 `cost.validation.executionSeconds`。Go cached 事件标记为历史结果；RepoKit 复用整个检查时，
保留该 Run 的原始明细并用 `reused` 标明，当前 execution 仍为 0。构建、排队、fixture 准备等未被
runner 分解的部分仍包含在 check 的总耗时内，不以相减方式伪造这些阶段的耗时。

检查完成日志包含 `duration_seconds` 与提取的 `test_timings` 条数；明细在命令完成后随报告输出。
事件格式以 [Go test2json 契约](https://pkg.go.dev/cmd/test2json) 为准。

`validate --check format,lint,test` 显式先运行 format，再重新分析和规划 lint/test。缺少可选 format 不阻止后续检查；
format 失败或中断时不启动检查。独立 `format` 缺少目标时返回 unavailable。验证不隐式安装依赖。

## 实现归属

Go 根包 `validation.go` 提供公共类型与入口，`internal/validation` 拥有规划、格式化、执行、输入清单、
执行历史和复用，直接消费 analysis / project / git，不反向依赖公共根包。CLI 负责参数与报告适配；
项目 Makefile 定义检查语义，调用方决定准备时机与门禁。验证执行不反向扩展静态分析范围。

## 模型与流程

`ValidationPlan` 按实际执行的 Component 分组，每组包含若干 `CheckSpec`。每项 `requestedComponents`
保存产生该请求的 Component 根路径，排序去重；`component.root` 是执行目录。每项固定检查种类、命令、文件、
范围、选择原因和执行环境摘要；整个计划绑定工作区内容快照。Go 计划可输出 JSON 供审阅，
但序列化输出不包含环境值，也不能反序列化后直接执行。计划只在创建它的进程中执行。

`CheckResult` 记录具体命令的状态、退出码、耗时、stdout、stderr 和诊断。
`ValidationReport` 汇总各 Component 的结果、整轮完整性及内容一致性：

- `inputsConsistent`：lint/test 观察的仓内内容与依赖凭据在规划、开始、结束时一致。
- `complete`：检查均得到终态，包括 failed、unavailable；取消或结果不确定时为 false。
- `passed`：输入一致，所有选中的 lint/test 通过或 skipped；unavailable 不冒充通过。
- 单个检查 passed 不等于整轮输入有效。只有 inputsConsistent 为 true 才能复用该检查。
  另一项 unavailable 或 failed 不会抹掉这个已完成检查的成功事实。
- `preparation` 单独记录 format；format-only 报告没有检查结果，inputsConsistent 为 false，不授予验证证据。
- 没有选中任何检查时报告空结果；它不证明整个仓库已经验证。

Go 提供 `PlanValidation`、`ExecuteValidation`、`Validate`、`Format`、`Lint`、`Test`。
Python 原生 API 为 `plan_validation`、`execute_validation`、`validate`、`lint`、`test`，
不包装 repocli CLI。CLI 组合 Go 公共 API；format/lint/test 各自独立，validate 默认选择 lint/test，可用 checks 加入 format。
包含 format 的计划是修改前的预览；ExecuteValidation 拒绝执行 format，组合执行必须经 Validate 重新规划。
TypeScript 当前不提供验证执行 API。

各语言能力可以按需求增减，共有能力的行为必须一致；“尚未实现”不能混同为同名能力的另一套语义。
当前配置与验证能力如下，CLI 是 Go 工具包的应用入口：

| 能力 | Go SDK | Python SDK | TypeScript SDK |
|---|---|---|---|
| home 解析与 config.json 读取 | 支持 | 支持 | 支持 |
| lint/test 规划与原生执行 | 支持 | 支持 | 未提供 |
| format、自动 impact、并发预算、输入声明 | 支持 | 未提供对应选项 | 未提供 |
| 验证历史写入与复用 | 支持 | 未提供原生 API | 未提供 |
| 保留 Run 列表与单次证据查询 | 支持 | 未提供原生 API | 支持 |

Go 原生验证的空 HistoryDir 关闭持久化；CLI 显式传入 RepoKit home 下的默认历史根。
Python 当前只执行检查，不写历史；这不表示在独立目录维护另一套历史，也不授权调用方自建复用缓存。
今后新增历史能力必须读写 validation 拥有的同一协议，不能按 SDK 语言另设存储。
Go 的保留 Run 查询在 HistoryDir 为空时、TypeScript 查询在 historyDir 未指定时，均使用 RepoKit home 下的 `repositories/`。
TypeScript 提供 `listValidationRuns({repository, home?, checkout?, component?, check?, status?, limit?, offset?})`
和 `readValidationRun(repository, runID, {home?, historyDir?, signal?, timeoutMs?})`；缺省查询预算为 5 秒。
查询读取同一 schemaVersion=2 JSON WAL、input manifest 与日志，保留未完成 Run、复用次数和证据缺口。
锁等待最多 5 秒；Linux/macOS 使用与 Go 相同的 flock，Node 通过 fs-ext 原生绑定调用，安装需要其原生构建工具链。
只读回放逻辑处理 `.START`/`.END` 截断恢复，不改名、删除 segment 或续期访问；损坏不能变成空历史或部分成功。
列表与明细均不判定当前代码已通过；input snapshot 只可用于源码比较，环境、工具链和外部输入仍由 validation 判定复用。
Go 与 Python 使用同源 tree-sitter-make grammar，解析最多 2 MiB、最多 2 秒；
无效语法和未完成解析报告 unavailable，不触发根入口回退。解析不执行 Make 或展开 includes。
共有执行语义包括 Component 选择、根入口回退与去重、文件范围、冻结环境和结果判定：
`complete` 表示选中检查的终态已知（包括 unavailable），`inputsConsistent` / `inputs_consistent`
独立表示输入未变；输入变化使 passed 为 false，但不抹掉已经完成的单项检查。
`conformance/validation/cases.json` 与 `execution.json` 由 Go/Python 分别执行，配置和 home 语料由三语言共享。


## 选择检查范围

未提供 impact 或非空 `files` 时，对选中的 Component 做全量检查；未指定 Component 时选择全部已发现 Component。
显式空 Component 列表表示不选任何 Component，未知 Component 是参数错误。

非空 `files` 按当前布局的最具体归属选择 Component，并只向各组件传递属于它的候选文件。
同时指定 `components` 时，它是允许的来源边界；边界内没有候选文件的组件不执行，越界文件在执行前报错。
父组件不隐含包含嵌套组件。显式空边界与非空文件列表冲突；无法确定归属或路径不规范也在执行前报错。
文件缺失但当前布局仍能确定归属时，只在该组件内回退全量，并在 reason 中说明缺失。

`--component` 限定请求来源，不禁止为缺失入口回退到 root；即使未显式选择 root，也可在那里执行回退检查。
Go/Python 对每个所选或受影响 Component 的 lint/test 分别选择入口：本地存在对应 target 就使用本地；
否则直接回退到根 Component（`.`），不逐级寻找父 Component。根也没有对应 target 时记为 unavailable。
不为不存在的根 Component 伪造执行对象。无关的 skipped 请求不触发回退；Makefile 无法安全读取或解析时
保留错误，不把它解释为没有目标；已声明目标执行失败时不再回退。format 只使用自身入口，不参与回退。

规划器按实际执行 Component/check 去重；合并定向文件并转换成执行目录的相对路径，全量请求优先于定向请求。
原始 ImpactReport 不改写，requestedComponents 记录来源，报告 components 按执行位置组织。
没有文件过滤契约、安全路径无法表达或归属变化时仍使用全量检查。Makefile recipe 是不透明的；
同时声明了父子入口就分别运行，不通过解析 recipe 推断覆盖关系。只为转发到 root 而声明的 target 可由项目移除，
让缺失入口自然回退。Go 与 Python 原生规划共同遵循该选择与回退契约。

调用方可提供当前工作区的 `ImpactReport`。规划器核对 checkout、input 和 snapshot，拒绝把旧报告、
index 或 commit 报告套在当前工作区。默认在 affectedFileCounts 中出现 source/test/config/manifest/lockfile
时选择该 Component；调用方可以提供自己的 tags。lint 使用该 Component 的 affectedFiles，test 使用 testFiles。
分析缺口不单独触发检查，原始 ImpactReport 继续保留 before/unowned 等分析证据。

删除、重命名前侧的路径仍按真实归属检查：候选已经不存在或当前属于别的 Component 时，当前检查
回退全量并记录原因。没有返回候选文件、项目不支持文件过滤、路径无法安全传给 Make 时，也回退全量。
不为已不存在的 Component 或 unowned 路径伪造执行目标。

手工 `files` 与 impact 是两种互斥的选择来源。文件必须是规范的仓库相对路径；进入 Make 前转换为
Component 相对路径并去重排序。包含空格、shell/Make 元字符或穿过符号链接的路径不进入文件参数。
这类候选只在其所属组件内导致全量回退，不会被静默丢掉或扩大到无关组件。

## 检查输入与结果复用

`repocli validate` 负责触发执行和跨次复用。开发验证与提交工作流每次都调用它；调用方不维护第二套
复用缓存，也不传入历史报告。`lint` / `test` 使用同一验证入口；format 不提供可复用检查证据。

Check 表达本次任务；Run 表达真实执行；Reuse 表达本次请求采用某个成功 Run。每轮捕获一份完整输入清单，
每个文件只计算一次内容摘要，各 Check 记录自己的输入路径集合。没有完整输入声明时采用整仓输入。
不再生成每个 Check 的总 fingerprint；原 Previous API、fingerprint 字段和 `--reuse` 参数移除，旧报告不能
提供复用证据。完整 snapshot 仍用于规划和执行前后的一致性核对。

复用只忽略 CodeGraph 分类为 VersionTag 的 Document，包括显式声明和 root 回退输入，不提供例外配置。
新增、删除和内容变化均按两轮路径集合比较；执行范围仍由 affected 规则决定，未命中历史不会扩大 scope。
版本文件在规划后或执行期间变化仍使本轮输入一致性失效。

`--inputs` 接受按 Component/check 组织的 JSON，例如：

```json
{"apps/api":{"test":{"components":["packages/core"],"paths":["fixtures"],"witnesses":[".venv/.repokit-dependencies.json"]}}}
```

components/paths 声明完整传递输入，自身 Component 自动纳入，嵌套 Component 按最具体归属划分。
paths 是仓库相对的字面文件或目录，不展开 glob；非法声明报错。缺少声明或选中符号链接时使用整仓身份，
原因在 inputReason 中显示。回退到 root 的检查保守使用整仓输入，不套用 root 的局部输入声明；
相关 Component 的声明仍被校验，依赖回执取并集，任一 disableReuse 均生效。witnesses 是显式给出的依赖回执文件，
报告只保存路径和摘要；规划和执行边界均核对。
没有可信依赖凭据的调用方可设置 disableReuse，真实检查仍执行。声明不自动证明依赖闭包，外部服务结果不在摘要中。

验证器先匹配同一 checkout、Component/check、命令、scope、文件集合、输入声明、逐文件内容、
实际执行环境和依赖凭据，再取最近完成的相同 Run；先后以 finished 事件的写入顺序为准，复用事件不改变顺序。
该 Run 成功且输入一致才可复用，否则重新执行，不跳过同一输入下的新失败去找旧成功。
不相同的 Run 不影响彼此的复用资格，例如 A 成功、B 失败之后，检查 A 仍可复用，检查 B 必须执行。
未完成的 Run 不提供复用证据；没有相同记录或证据不可用时重新执行。
初期不推断 full 对 focused 的覆盖关系。`--force` 明确要求真实执行。

复用结果保留原 `runID`、`checkedAt`、输出和 `durationSeconds`；`requestedAt` 与 `lookupSeconds` 描述本次请求。
`inputID` 关联本轮输入清单，`reuseReason` 解释复用或执行原因。报告的完整 snapshot 始终属于本轮。
没有精确匹配时，`reuseReason` 报告同一 checkout、Component/check 最近完成候选的首个差异：
命令/scope/文件集合、输入声明、依赖凭据、执行环境或输入内容。它不是全部差异清单；
较新候选不匹配时仍继续查找更早的精确匹配，不改变最近匹配 Run 的成功/失败判定。

### 执行历史

逐次执行、复用次数、筛选分页与单次输出/输入证据通过
[`validate history list/show`](#验证历史查询) 查询；汇总统计入口保持独立。

代码按职责拆分：`internal/validation/history/` 拥有事件记录、WAL 存储、重放统计、保留期清理和文件锁；
父包的 `reuse.go` 解释检查结果及输入清单，判断本轮是否可复用，并衔接执行。依赖方向仅为 validation → history。
history 原样保存检查载荷，仅解析统计和事件一致性所需字段，不依赖父包的 CheckResult、输入类型或规划策略。

CLI 默认使用 `~/.repokit/repositories/<name>/`，`--history-dir` 可指定父目录。
仓库名称由 Forge 与完整仓库路径组成，例如 `github--compforge--repokit`；路径分隔符使用 `--`，
原名称中的分隔符及路径特殊字符转义。无远端身份时使用本地仓库名与共享 Git 元数据路径摘要。
同仓多个 clone/worktree 共用一个历史库和锁；checkout 路径记录在事件和输入中，不创建 checkout 子目录。
统计汇总 repository 内的执行与复用，候选 Run 仍要求与本次 checkout 相同；不会跨 checkout 复用。
工具包通过 `ValidationRequest.HistoryDir` 显式启用历史；空目录配置不持久化、不复用。
存储目录应在被检查仓库之外，或由项目明确忽略。规划不需要用户目录；无法解析默认目录时，
普通执行报告 warning 并关闭持久化，仍执行检查；JSON 模式放在 diagnostics，文本模式向 stderr 告警。显式 `--history` 查询则报错并提示指定 `--history-dir`。

- `history/`：tidwall/wal 的 JSONL 分段，默认目标 20 MiB，连续序号；业务 JSON 保存在 data 字符串中。
- `inputs/<id>.json`：完整输入清单，包含路径、类型和摘要，不保存源码正文。
- `logs/YYYY-MM-DD.stdout.jsonl`、`YYYY-MM-DD.stderr.jsonl`：按 Run 开始时间的 UTC 日期聚合实际检查输出，每个 Run 向各文件追加一条 `{runID, output}` 记录。跨午夜执行仍使用开始日期，多行输出以 JSON 字符串保存。追加和读取共用 repository 文件锁；不完整尾记录明确报错，不自动修复。旧的 `logs/<run-id>/stdout`、`stderr` 继续可读，不自动搬迁。

started 在检查进程成功启动后追加；finished 在整轮输入复核后追加；reused 引用原 Run，所有事件记录其 checkout。
进程启动后、started 持久化前崩溃可能留下计数缺口；本地历史不宣称跨进程启动与磁盘写入的原子事务。
缺少 finished 的 Run 不提供复用证据。内存 Run 索引及统计由事件重放重建，追加校验与读取共用同一投影。

macOS/Linux 通过 OS 文件锁保护 Open → 读写 → Close，锁等待最多 5 秒且受 context 约束。
检查执行期间不持锁；并发请求可以分别执行，不实现执行合并。追加前在同一写锁内重放当前历史并
校验整批事件，避免旧会话状态写入无效引用。若 Run 已被其他会话清理，跳过其迟到的 finished/reused 事件并
告警，不影响本次检查结果或同批其他记录；这些事件不进入保留期统计，也不会恢复已过期的 Run。wal 保持默认同步写入。
历史损坏、缺失输入/输出或写入失败明确进入 diagnostics，不自动修复，也不以不完整历史授予复用；
真实检查可以继续，结果通过与历史持久化分别报告。

默认按 30 天窗口滚动截断事件的过期前缀，不区分成功、失败或未完成，也不因近期复用延长旧 Run 的保留期。
截断后缺少 started 的 finished/reused 事件在重放时忽略，不计入统计、不提供复用证据；最差按未执行过重新检查。
随后清理过期且未被保留 Run 引用的 inputs/logs。每日输出文件仅在该日期没有保留 Run、且文件修改时间已过期时整体删除；当天过期 Run 的输出可随同日保留 Run 延后清理。旧布局沿用按 Run 清理。工具包可用 HistoryRetentionDays 调整天数。
`validate --history` 输出保留期统计，不执行检查；报告 history 也提供执行次数、通过/失败/未完成次数、
实际执行耗时、复用次数、比较耗时及最近实际检查时间。复用不重复累计原执行耗时；统计不是永久累计值。

`repocli stats` 通过公共 `ReadValidationHistories` API 汇总所有现存 repository 历史，复用相同的锁和事件投影，
不要求 checkout 仍存在，也不执行保留期清理。读取失败的仓库保留 diagnostics，不将部分重放当成完整统计。
它同时提供命令日志与存储统计，详见 [本地统计](stats.md)。

## 验证历史查询

Validation 拥有检查执行、输入证据、历史和复用。查询从同一 WAL 重放实际 Run，
不运行 Make、不检查当前文件是否匹配、不创建第二套缓存；state 只提供仓库身份与目录约定。

### 历史查询 CLI

```sh
repocli validate history list --repo . --check test --status failed --limit 20 --json
repocli validate history list --repo . --component toolkit/go --checkout /path/to/checkout --offset 20 --json
repocli validate history show <runID> --repo . --json
```

`--repo` 通过现存 checkout 确定 Repository，默认列出该 Repository 所有 checkout 的保留记录。
`--checkout` 是精确路径筛选，允许查询已经移除的 worktree；不会在该路径运行 Git 或检查。
`--component` 使用仓库相对 root，根组件为 `.`；`--check` 为 lint/test；`--status` 为
passed/failed/cancelled/uncertain/unfinished。空筛选表示不限。
`--history-dir` 指定历史根目录，默认 `<RepoKit home>/repositories`；身份与存储位置规则见
[验证执行历史](#执行历史)。`validate --history` 和 `stats` 继续提供汇总统计。

列表按 Run 开始时间降序、相同时间按 runID 排序；每个实际执行只占一行。
复用不会新增或重排 Run，`reuseCount` 和 `lastReusedAt` 表示保留事件中的复用记录。
`limit` 默认为 20，范围 1–100；`offset` 默认为 0。`total` 是筛选后的数量，`hasMore`
表示还有记录。每次查询是独立观察，并发执行或保留期清理可能改变后续页面。
列表不加载输出和输入 manifest，Run 摘要不包含当前代码的通过判定。

详情返回 Run、原始 CheckResult、输入 manifest 和证据缺口：

- Run 含 checkout、Component、check、scope、开始/完成事件时间、实际执行耗时和输入一致性。
  `finishedAt` 是整轮输入复核后写入完成事件的时间；实际检查耗时使用 `durationSeconds`，
  不用两个事件时间相减推算。
  没有 finished 事件的记录标为 `unfinished`，`inputsConsistent` 为 null；不能据此断言进程仍在运行。
- CheckResult 保留命令、文件范围、输入声明、退出状态、测试 timing、stdout/stderr 和诊断。
  `testTimings` 是 runner 报告的历史耗时，父子 case 和并行测试时间可能重叠。
- 输入 manifest 提供 snapshot 与文件类型/摘要，不包含源码正文或环境变量值；
  CheckResult 的 `inputFiles` 指明该 check 使用的输入范围。
- `complete` 只说明该 Run 的证据是否齐备。历史检查失败仍可是完整详情，
  历史检查通过也不意味着当前代码通过，不构成 commit/push 门禁。

JSON 保留完整捕获内容。text 展示状态、历史耗时、慢 case、输入摘要和有界输出片段；
截断时提示使用 `--json`。两个命令支持 `--output <file>` 导出统一结果。

### 历史查询结果与成本

遵循 [CLI 输出契约](cli-output.md)：读取成功使用 `outcome=succeeded` / exit 0，
即使查询到的历史检查是 failed 或 unfinished。列表分页由 `hasMore` 表达，不是查询失败。

找不到 runID 返回 `validation.run_not_found`，不推断它从未存在或已过期。
日志/输入缺失或不可解析时保留已读 Run，`complete=false`，返回 `outcome=partial` / exit 1，
diagnostics 指向缺失证据；空输出字符串不能替代其可用性判断。
WAL 损坏、读取失败或取消返回失败，不以空集合或部分重放掩盖错误。

`cost.wallSeconds` 和 `cost.phases.history.query` 度量本次查询；Run 的 `durationSeconds`
与 CheckResult 的 timing 是历史执行成本，不计入本次查询的 validation execution cost。

### 原生历史查询 API 与存储边界

| 能力 | Go | TypeScript | Python |
|---|---|---|---|
| 保留 Run 筛选、分页 | `ListValidationRuns` | `listValidationRuns` | 未提供 |
| 本页输入证据摘要 | `ValidationRunQuery.IncludeInputs` | `includeInputs` | 未提供 |
| 单次详情与输出 | `ReadValidationRun` | `readValidationRun` | 未提供 |

能力集合按实际需求提供；表中共同能力遵守相同身份、默认值、错误、存储和副作用契约，
由 `conformance/history/` 的共享语料验证。Python 未提供的能力不以 CLI 子进程隐式补齐。

Go `ListValidationRuns(ctx, ValidationRunQuery)` 和 TS `listValidationRuns(query)` 返回
筛选、分页后的摘要。Go 的 `HistoryDir` 为空时使用 `REPOKIT_HOME` 或默认 `~/.repokit`
下的 `repositories`；TS 使用相同默认目录，并可用显式 `home` 覆盖。显式 `HistoryDir` /
`historyDir` 都直接指定 repositories 根。查询不需要 Forge 请求，也不初始化缺失的 home。

默认列表只读取执行事件。显式 `IncludeInputs` / `includeInputs` 为返回页的每条 Run 附上
`inputs: {snapshot?, complete, diagnostics}`，在一次 WAL 回放和同一把仓库锁内读取该页
manifest，不读取 stdout/stderr，也不读取其他页的 manifest。这里的 `complete` 只说明输入
证据可用，不能证明输出日志完整。缺失或损坏的 manifest 保留 Run，并通过 inputs diagnostics
报告；分页、排序、total 和执行状态不受影响。摘要不返回完整文件表，也不判定验证可复用。

Go `ReadValidationRun(ctx, historyDir, checkout, runID)` 与 TS
`readValidationRun(repository, runID, options)` 才读取完整 manifest 和输出；详情的
`complete/diagnostics` 表达所有执行证据的可用性。SDK 返回领域对象，不返回 CLI envelope。

两种查询均不执行检查、不清理、不续期访问、不产生 CLI 执行日志。
已有历史使用同一 advisory lock 协调读取、写入与保留期清理；读取仍需可写的锁文件。
Go/TS 支持 Linux/macOS，TS 使用 `fs-ext` 原生模块。读取逻辑恢复 WAL 截断状态，但不重命名、
删除或修复磁盘段。损坏的历史使查询失败，不能作为成功的空列表或部分历史返回。

### 查询错误与取消

Go 返回 `errs.Error`，TS 抛出继承公共 `RepoKitError` 的 `ValidationHistoryError`。调用方使用 `code`、`op`、
`subject` 判断，保留原始 cause 供诊断，不解析 message。list 的 op 是
`validation.history.list`、subject 是传入的 repository；详情的 op 是
`validation.history.show`、subject 是请求的 runID。

| code | 含义 |
|---|---|
| `validation.invalid_query` | 非法分页、check 或 status |
| `validation.invalid_run_id` | 非法 Run ID |
| `validation.run_not_found` | 成功读取的保留历史中没有目标 Run |
| `validation.history_failed` | 历史损坏或其他未分类的查询失败 |
| `execution.canceled` | 调用方取消 |
| `execution.deadline_exceeded` | 调用预算或锁等待预算耗尽 |
| `filesystem.permission_denied` / `filesystem.not_found` | 建立查询时的文件访问失败 |

Go context 与 TS AbortSignal/timeoutMs 在 Git 定位、锁等待、回放和证据读取阶段生效；
取消或超时不能降级为缺失证据或成功的空集合。两端都沿用调用方总预算，不隐式限制整次查询时长；
TS 可用 `timeoutMs` 显式设置总预算，或直接传入 AbortSignal。两端锁等待上限均为 5 秒。
非法参数在访问仓库前拒绝。manifest/输出缺失属于详情诊断，不是上述查询终止错误。

## 执行与预算

执行器只消费冻结计划，不重新查询目标、分析 affected 或推导 scope。开始和结束各捕获一次工作区，
扫描次数不随 Component/check 数量增加。规划之后内容已变时不启动检查；执行途中变化则整轮失效。

环境在规划时复制，执行时显式传给子进程；显式环境需提供用于查找 make 的 PATH。报告只保留其摘要，绝不输出环境值。
每条命令用 `make -f Makefile` 固定读取规划时的文件，避免 GNUmakefile/makefile 抢占默认入口。
Make 控制变量 MAKEFILES/MAKEFLAGS/MFLAGS/MAKEOVERRIDES/GNUMAKEFLAGS/MAKELEVEL 与两类文件变量不从父环境继承；
每条命令显式设置 LINT_FILES 和 TEST_FILES，防止继承的 dry-run 或文件选择改变计划语义。
环境摘要描述实际传入的环境，不要求 Python/Node 宿主进程的环境相等。
Go/CLI 默认继承环境时额外移除 `CODEX_TOOL_CALL_ID`，避免每次工具调用的追踪身份使复用失效；
该变量同时从实际检查进程中移除，不是仅从摘要中忽略。其它环境变量（包括构建配置、凭据和 sandbox 设置）
继续参与摘要。Go 调用方显式提供 `ValidationRequest.Environment` 时保留该变量，并以其实际值参与复用判定。
已有历史不改写；旧环境摘要不匹配时正常执行一次，后续请求才能复用新的成功记录。

Go/CLI 的 concurrency 默认 2，范围 1–64；format 顺序完成后，lint/test 共用一个有界队列。Go 调用方通过 context 传入整轮 deadline；Python 保留顺序执行，使用 timeout 和已有 operation
取消作用域。CLI 沿用全局 --timeout。macOS/Linux 上每次 Make 都运行在独立会话中，取消、超时、
输出超限或退出时清理其进程组。启动后中断标为 uncertain，未启动时取消标为 cancelled；不自动重试。
stdout 上限 16 MiB，stderr 上限 64 KiB。输出超过预算会终止检查并报告 uncertain。

## CLI

```sh
repocli format --component toolkit/go
repocli validate --component toolkit/go --check format,lint,test --timeout 10m
repocli lint --component toolkit/python --file toolkit/python/repokit/model.py
repocli test --component toolkit/python --file toolkit/python/tests/test_inspect.py
repocli validate --base HEAD --plan
repocli validate --base origin/main --json --timeout 10m
```

`--plan` 输出 JSON envelope，计划位于 `data`，不执行 Makefile。`--base` 显式请求先分析 working-tree impact；
不传该参数时，不隐式分析 diff。`--file` 可重复，不能与 --base 混用。
`--check` 只用于 validate；步骤顺序不影响 format 先完成这一约束。`--changed-file` 在 --base 下限定初始影响分析种子；format 实际改变的路径会加入后续分析，避免漏掉生成内容。
`--output` 原子写入完整命令结果，与 JSON stdout 相同；目录必须存在且应处于 Git 忽略的本地状态中。`--history` 查询历史统计；`--force` 禁用本次复用。
JSON 使用[统一输出契约](cli-output.md)，报告或历史统计位于 `data`，目标 stdout/stderr 作为单项结果字段保存。
`cost` 提供本次总耗时、validation/observation 阶段耗时，以及各 Component/check 的执行和查询耗时。
reuse 只计算本次查询成本，不重复计算历史执行时长；并发检查的累计耗时与总墙钟耗时分别解释。
文本模式展示判定、耗时和失败日志摘要；JSON stdout 与文件导出均保留捕获日志。
退出码为 0（完整通过）、1（检查失败或验证不完整）、2（CLI 参数错误）。

Go/Python 共用 `conformance/validation/cases.json`，并分别通过真实 Make 进程验证内容变动、
环境冻结、失败、超时和子进程清理。CLI 测试通过公共 API 验证入口组合与退出码。

## format 命名迁移

自 0.46.0 起，原 Go `Fix` API、CLI `fix` / `--check fix` 和 Makefile `fix` target
统一改为 `Format` / `format`；调用方与项目 Makefile 需同步更新，不自动调用旧 `fix` target。
format 使用既有 `LINT_FILES` 文件选择契约；完成后重新规划 lint/test，二者可并行执行。
Python 原生验证 API 仍只提供 lint/test。


## Python 消费 Go 验证报告

`decode_validation_report(payload, checkout=..., checks=("lint", "test"))` 解码并核对 Go CLI 的
ValidationReport wire schema；传入 CLI stdout 或导出文件中结果对象的 `data`。
协议版本、checkout、Component 路径、结果字段与 requestedComponents
覆盖均由工具包验证；允许真实失败结果与 root fallback 分组，拒绝重复、缺失 check、空 Component 分组
以及与结果矛盾的汇总状态。无选中任务时允许空 components；执行前输入变化导致的空结果保留为未完成报告，
format 提前终止的报告不冒充 lint/test 证据。
覆盖校验基于报告声明的 requestedComponents，不自行重新推断调用方的任务选择。
这只是协议入口，不启动 CLI，也不替代现有验证引擎。调用方负责进程入口、报告存储与门禁策略。

`dependency_witnesses(component)` 返回由依赖输入与准备回执组成的路径/摘要映射；无需安装环境时为
空映射，环境未就绪或观察期间变化时为 `None`，读取失败抛出异常。回执文件名、输入集合与摘要算法
均由 RepoKit 管理，消费方不得根据安装目录自行拼装回执路径。调用方决定何时准备环境；Go 验证执行
接收这些 witness 路径，并在执行边界冻结、核对其摘要，调用方的状态投影不能代替证据。

Python 验证实现集中在 `repokit/validation/`：`engine.py` 负责原生规划与执行，`report.py` 负责 Go wire report 解码，
包入口汇总公共类型与函数。`repokit` 包根和 `repokit.validation` 的既有执行 API 保持不变。

## Repository 本地信息

信息目录、read-through、freshness 与回收边界见 [Repository 状态](state.md)。
