# Makefile 验证

RepoKit 按 Component 规划和执行项目声明的 lint/test。前提是项目遵守下面的 Makefile 契约；
工具包不根据语言猜测试命令，也不通过 `make -n` 探测能力，因为 Make 在读取文件时就可能执行函数。
Go 复用已有 gotreesitter 的 Make grammar 静态读取规则和变量引用，不新增解析依赖；Python 保持
该约定子集的原生识别，双方共用契约语料。语法解析不等于求值，条件规则和动态目标不推断为可用入口。
分析与规划保持只读，执行是调用方明确发起的操作。下述组合执行、复用及报告 schema 2 为 Go/CLI 契约；Python 原生验证实现本轮保留，不迁移或扩展。

## Checkout 依赖准备（Python）

`inspect_dependencies(component_path)` 只读返回 `DependencyEnvironment`；`prepare_dependencies(environment)`
显式执行依赖安装。创建 worktree、inspect、snapshot 均不隐式准备环境；调用时机和验证门禁属于消费方。
依赖准备由 Python API 提供，供原生 Python workflow 使用。

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
输出有容量限制，中断会终止安装进程组，返回 uncertain，不宣称副作用已回滚。跨进程锁不在本 API 保证范围内。
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

项目负责实现这些语义。RepoKit 不解析测试框架输出，也不能从一个返回 0 的空 recipe 证明实际检查发生。
依赖安装应在验证前完成；Python 调用方可使用已有的 `inspect_dependencies` / `prepare_dependencies`。
`validate --check format,lint,test` 显式先运行 format，再重新分析和规划 lint/test。缺少可选 format 不阻止后续检查；
format 失败或中断时不启动检查。独立 `format` 缺少目标时返回 unavailable。验证不隐式安装依赖。

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

## 选择检查范围

未提供 impact 时，对选中的 Component 做全量检查；未指定 Component 时选择全部已发现 Component。
显式空 Component 列表表示不选任何 Component，未知 Component 是参数错误。

`--component` 限定请求来源，不禁止为缺失入口回退到 root；即使未显式选择 root，也可在那里执行回退检查。
Go/CLI 对每个所选或受影响 Component 的 lint/test 分别选择入口：本地存在对应 target 就使用本地；
否则直接回退到根 Component（`.`），不逐级寻找父 Component。根也没有对应 target 时记为 unavailable。
不为不存在的根 Component 伪造执行对象。无关的 skipped 请求不触发回退；Makefile 无法安全读取或解析时
保留错误，不把它解释为没有目标；已声明目标执行失败时不再回退。format 只使用自身入口，不参与回退。

规划器按实际执行 Component/check 去重；合并定向文件并转换成执行目录的相对路径，全量请求优先于定向请求。
原始 ImpactReport 不改写，requestedComponents 记录来源，报告 components 按执行位置组织。
没有文件过滤契约、安全路径无法表达或归属变化时仍使用全量检查。Makefile recipe 是不透明的；
同时声明了父子入口就分别运行，不通过解析 recipe 推断覆盖关系。只为转发到 root 而声明的 target 可由项目移除，
让缺失入口自然回退。Python 原生规划与报告本轮不扩展此规则。

调用方可提供当前工作区的 `ImpactReport`。规划器核对 checkout、input 和 snapshot，拒绝把旧报告、
index 或 commit 报告套在当前工作区。默认在 affectedFileCounts 中出现 source/test/config/manifest/lockfile
时选择该 Component；调用方可以提供自己的 tags。lint 使用该 Component 的 affectedFiles，test 使用 testFiles。
分析缺口不单独触发检查，原始 ImpactReport 继续保留 before/unowned 等分析证据。

删除、重命名前侧的路径仍按真实归属检查：候选已经不存在或当前属于别的 Component 时，当前检查
回退全量并记录原因。没有返回候选文件、项目不支持文件过滤、路径无法安全传给 Make 时，也回退全量。
不为已不存在的 Component 或 unowned 路径伪造执行目标。

手工 `files` 与 impact 是两种互斥的选择来源。文件必须是仓库相对路径；进入 Make 前转换为
Component 相对路径并去重排序。包含空格、shell/Make 元字符或穿过符号链接的路径不进入文件参数。
这类候选会导致全量回退，不会被静默丢掉。

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

### 执行历史

代码按职责拆分：`internal/validation/history/` 拥有事件记录、WAL 存储、重放统计、保留期清理和文件锁；
父包的 `reuse.go` 解释检查结果及输入清单，判断本轮是否可复用，并衔接执行。依赖方向仅为 validation → history。
history 原样保存检查载荷，仅解析统计和事件一致性所需字段，不依赖父包的 CheckResult、输入类型或规划策略。

CLI 默认使用 `~/.repocli/repositories/<name>/`，`--history-dir` 可指定父目录。
仓库名称由 Forge 与完整仓库路径组成，例如 `github--compforge--repokit`；路径分隔符使用 `--`，
原名称中的分隔符及路径特殊字符转义。无远端身份时使用本地仓库名与共享 Git 元数据路径摘要。
同仓多个 clone/worktree 共用一个历史库和锁；checkout 路径记录在事件和输入中，不创建 checkout 子目录。
统计汇总 repository 内的执行与复用，候选 Run 仍要求与本次 checkout 相同；不会跨 checkout 复用。
工具包通过 `ValidationRequest.HistoryDir` 显式启用历史；空目录配置不持久化、不复用。
存储目录应在被检查仓库之外，或由项目明确忽略。规划不需要用户目录；无法解析默认目录时，
普通执行向 stderr 告警并关闭持久化，仍执行检查。显式 `--history` 查询则报错并提示指定 `--history-dir`。

- `history/`：tidwall/wal 的 JSONL 分段，默认目标 20 MiB，连续序号；业务 JSON 保存在 data 字符串中。
- `inputs/<id>.json`：完整输入清单，包含路径、类型和摘要，不保存源码正文。
- `logs/<run-id>/stdout`、`stderr`：实际检查输出，权限限制在本地用户。

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
随后清理过期且未被保留 Run 引用的 inputs/logs。工具包可用 HistoryRetentionDays 调整天数。
`validate --history` 输出保留期统计，不执行检查；报告 history 也提供执行次数、通过/失败/未完成次数、
实际执行耗时、复用次数、比较耗时及最近实际检查时间。复用不重复累计原执行耗时；统计不是永久累计值。

`repocli stats` 通过公共 `ReadValidationHistories` API 汇总所有现存 repository 历史，复用相同的锁和事件投影，
不要求 checkout 仍存在，也不执行保留期清理。读取失败的仓库保留 diagnostics，不将部分重放当成完整统计。
它同时提供命令日志与存储统计，详见 [本地统计](stats.md)。

## 执行与预算

执行器只消费冻结计划，不重新查询目标、分析 affected 或推导 scope。开始和结束各捕获一次工作区，
扫描次数不随 Component/check 数量增加。规划之后内容已变时不启动检查；执行途中变化则整轮失效。

环境在规划时复制，执行时显式传给子进程；显式环境需提供用于查找 make 的 PATH。报告只保留其摘要，绝不输出环境值。
每条命令用 `make -f Makefile` 固定读取规划时的文件，避免 GNUmakefile/makefile 抢占默认入口。
Make 控制变量 MAKEFILES/MAKEFLAGS/MFLAGS/MAKEOVERRIDES/GNUMAKEFLAGS/MAKELEVEL 与两类文件变量不从父环境继承；
每条命令显式设置 LINT_FILES 和 TEST_FILES，防止继承的 dry-run 或文件选择改变计划语义。
环境摘要描述实际传入的环境，不要求 Python/Node 宿主进程的环境相等。

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

`--plan` 只输出 JSON 计划，不执行 Makefile。`--base` 显式请求先分析 working-tree impact；
不传该参数时，不隐式分析 diff。`--file` 可重复，不能与 --base 混用。
`--check` 只用于 validate；步骤顺序不影响 format 先完成这一约束。`--changed-file` 在 --base 下限定初始影响分析种子；format 实际改变的路径会加入后续分析，避免漏掉生成内容。
`--output` 原子写入报告；目录必须存在且应处于 Git 忽略的本地状态中。`--history` 查询历史统计；`--force` 禁用本次复用。
JSON 报告与目标 stdout/stderr 分离，目标输出作为单项结果字段保存。
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
ValidationReport wire schema。协议版本、checkout、Component 路径、结果字段与 requestedComponents
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
