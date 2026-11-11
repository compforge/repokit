# RepoKit

## 项目定位与边界

RepoKit 是 Repository 理解与操作工具包：结合仓库布局、代码、Git 版本和工具链解释仓库，
并提供可组合的操作。公共 API 提供结果与诊断，CLI 将这些能力映射为命令；调用方拥有业务流程与决策。
能力覆盖布局与组件识别、代码关系、变更与影响分析、仓库状态、Git/Forge 操作、构建及包工具约定。
这是职责范围；各语言的实际支持以公共 API 与专题文档为准。

### 核心概念与职责

| 概念或组件 | 身份、关系与职责 |
|---|---|
| Repository / Checkout | Repository 是 Forge 与仓库路径组成的稳定身份；Checkout 是本次读取或操作的本地工作区。同仓可以有多个 clone/worktree，目录位置不进入公共仓库身份；无法确定远端身份时保持未知。 |
| Component / Product | Component 属于 Repository，是 lint、test、打包等工程操作的粒度；优先依据 manifest、其次 Makefile 识别。文件归最深组件，workspace 声明另行决定安装根。Product 与 Component 可以多对多，RepoKit 不从目录推断产品关系。 |
| Snapshot / Comparison | Snapshot 标识捕获内容并报告缺口；Comparison 配对前后版本、文件变化及源码。布局、图、位置和显示源码必须属于相同版本，前后版本的证据路径不能混用。 |
| Fragment / Unit | Fragment 表达同一对象上的实际编辑，Unit 按关系强度聚合相关 Fragment；容量和数量约束由工具包负责，评审与验证决策归调用方。 |
| CodeGraph | 共享 CodeGraph 拥有语言声明、语法与绑定事实；RepoKit 拥有仓库上下文解析、材料准入和影响策略。Changeset、Testset、Workset 属于 RepoKit，不进入通用图模型。 |
| Configuration | 用户级访问配置，按 Forge 实例解析 provider、API host 与凭据；与可回收的 repository 状态分开，契约见配置专题。 |
| State | 核心业务组件，拥有仓库身份与关联、观测复用、checkout 生命周期、状态发布与恢复。Git 是下层事实与操作适配，由 state 调用，不反向更新 state。持久化目录中的本地键见 state 专题。 |
| PullRequest / Forge | PR 由目标 Repository 与仓内编号确定；源仓、分支、revision 和状态不参与身份。Forge 适配平台读写与查询，观察层维护显式启用的刷新和持久化；调用方拥有关注范围、时效要求、进程生命周期与业务动作。 |
| Validation | 项目 Makefile 定义检查语义，工具包拥有规划、执行、输入证据、历史与复用；调用方决定依赖准备时机、检查要求和门禁，不维护第二套检查缓存。 |

Forge、Repository、Component、Product 复用 quality-harness common 的中立身份；RepoKit 持有本次布局、
文件归属与工具证据，不把 checkout 路径或调用方策略加入共享身份。语言、工具和目录是元数据，
组件归属不证明依赖隔离，也不授权跳过验证。

Changeset 是变更在对应版本中存在的 Document，变更 Symbol 是查询起点；Testset 是独立发现的测试候选，
只有其成员能成为测试推荐。Workset 是所选源码、测试及按需引用的辅助文件实际入图的集合；
完整目录保留用于解析，未选中源码不因 import 自动解析，空测试集合也不证明没有受影响测试。

### 主流程

| 入口 | 必要工作与输出边界 |
|---|---|
| tree / inspect | tree 查询同版本路径、类型与已知角色；inspect 读取必要配置，解释 Repository、Component、语言与工具证据。不依赖构图，也不执行项目代码。 |
| snapshot / diff | snapshot 捕获内容身份与完整性；diff 独立捕获前后内容、文件状态与 patch，按需派生同版本布局或形成 Unit。 |
| impact | 从前后布局确定变更归属，独立发现 Testset；按版本建立有界 Workset 和 CodeGraph，再查询影响、投影测试候选并保留证据与缺口。 |
| deadcode | 完整快照图提供引用证据；全仓或文件范围筛选候选，diff 前后节点作为入口追踪用途变化，区分本次变化和相关历史候选。分析规则与公开边界由消费者声明，结果不授权删除。 |
| view | 捕获内容和布局，构建共享代码图；图、结构上下文和源码作为一个版本展示与刷新，不经过 impact 的候选裁剪。 |
| state / Git / Forge 操作 | 调用方提供目标与参数；state 协调 checkout 生命周期及状态更新，Git/Forge 适配执行实际动作。操作前实时核验，结果不确定时先核对事实再决定重试。 |
| format / lint / test / validate | 按 Component 生成计划；显式 format 后重新规划检查；执行返回单项结果、整轮完整性及输入一致性。成功 Run 按输入证据复用。 |
| clean | 遍历已登记仓库，统一调用 worktree 清理 service 沿用现有保留策略，并按访问时间回收过期 repository 状态；维护不续期访问。 |
| stats / upgrade | stats 只读汇总保留历史、日志与容量并报告读取缺口；upgrade 由 CLI 显式查询和安装 Release，不访问目标仓库。 |

各入口按需组合输入选择、内容捕获、结构识别和代码分析，不强制经过包含全部分析的 prepare。
`DiffReport.Inspect` 复用已经捕获的内容，`GraphFromSource` 消费调用方已有的只读视图。
新增命令复用这些能力，不按命令名称维护重复的 diff 事实，也不将调用方工作流塞入通用图或快照。
help/version 不访问目标仓库，成功调用不初始化文件日志。

## 代码地图与核心模块

```text
repokit/
├── toolkit/
│   ├── go/                   # 独立 Go module；公共 API 与分析实现
│   │   ├── repokit.go        # Tree / Inspect / Snapshot / Diff / AnalyzeImpact / Graph
│   │   ├── units.go          # Fragment / Unit 公共 API
│   │   ├── validation.go     # 验证公共入口；实现归 internal/validation
│   │   ├── state/            # 仓库状态业务、checkout 生命周期与共享存储
│   │   ├── config/           # 用户配置与 host 级 Forge 访问解析
│   │   ├── forge/            # 平台 PR 查询、写入与观察
│   │   └── internal/
│   │       ├── analysis/     # 捕获、布局、构图与分析编排
│   │       │   └── deadcode/ # deadcode 编排、候选规则与前后图比较
│   │       ├── git/          # Git 材料与内容读取
│   │       ├── gitworktree/  # checkout 查询与 Git 命令执行适配
│   │       ├── project/      # 结构识别、组件归属与工具证据
│   │       ├── diff/         # patch 与变更行
│   │       ├── units/        # Fragment / Unit 形成与聚合
│   │       ├── impact/       # 变更起点、测试候选与影响结果
│   │       ├── codegraph/    # 共享 CodeGraph 适配、仓库解析与查询
│   │       └── validation/   # Makefile 规划、执行、输入身份、历史与复用
│   ├── typescript/           # 原生 Node 工具包；src/state、git、forge 各有职责
│   └── python/               # 原生 Python 工具包；state、git、forge、validation
├── apps/cli/                 # 独立 Go module；工具包消费方
│   ├── cmd/repocli/          # 进程生命周期、信号与退出码
│   └── internal/            # CLI 参数/展示/日志、统计、升级与本地 viewer
├── conformance/              # 多语言共享布局、Git、状态与验证契约语料
├── docs/                     # 各能力的模型、机制与完整契约
├── go.work                   # 本仓 Go 模块联合开发
├── VERSION                   # 仓库与 CLI 发布版本
└── Makefile                  # leaf 验证约定与显式全仓检查、构建
```

Go 工具包和应用分别拥有 go.mod 与 internal，应用只能消费工具包公共 API。工具包不依赖 Cobra、
HTTP 展示、CLI 日志或调用方门禁；viewer 只展示分析结果与捕获源码。库负责验证输入约束，不能依赖 CLI 预校验。
各语言的原生 API 直接完成解析与操作，不依赖 repocli 或 Go。Python 的显式 `repokit.cli`
入口独立消费已安装 repocli 的版本化协议，不作为原生 API 的隐式回退。
文件名识别目录由固定版本 Go 依赖生成，共享语料校验语言间的身份、版本、归属与诊断契约。

## 关键约定

1. **分析只读，操作显式。** 分析与规划不修改 index、工作区、配置或依赖，不执行目标仓库代码、构建脚本或测试，也不自动下载依赖、初始化子模块或写远端。静态模型不能解释的行为报告为缺口。
2. **事实、推断和完整性分开。** 已知结果与未知部分同时保留，失败不转成空集合。inspect 的布局观察、snapshot 的内容捕获和 impact 的分析完整性独立；`complete: true` 不证明运行时依赖或测试覆盖完整，命令成功也不证明目标项目正确。
3. **内容版本与范围明确。** 同一分析复用同版本材料；删除、重命名及配置变化使用各自版本。源码读取、解析和扩展受时间与容量约束，缓存只复用适用上下文中的事实，截断结果不能标为完整。测试推荐不反向扩大生产源码 Component 集合。
4. **生命周期由业务组件协调。** state 拥有状态复用、更新和恢复；目录存在或缓存命中不授权删除、合并。Git 默认保护脏工作区，强制行为和 lease push 必须显式指定；副作用成功后的记录失败不能变成可盲目重试的动作失败。
5. **调用方拥有工作流。** 任务归属、目标选择、操作顺序、验证门禁、合并授权和清理时机属于调用方；Repository 访问配置由 RepoKit config 统一解析，也可显式传入连接参数。RepoKit 不读取调用方配置或保存其 session。取消与预算使用 Go context、TS AbortSignal/timeoutMs、Python Event/timeout；库不隐式创建 CLI 日志或 timeline。
6. **契约随 owner 维护。** 全局核心模型、职责与约束直接维护在本文件；能力细节归对应 `docs/<topic>.md`，实现约定归子项目 `AGENTS.md`。公共 API 和版本化 JSON 各自承担契约责任，内部算法及语法树保持私有；新增语言按实际需求接入，不预建空 SDK。
7. **公共仓保持中立。** 不包含内部地址、个人机器路径、凭据或公司专属逻辑。项目、工具包与导入名使用 RepoKit / `repokit`；CLI 二进制和命令名为 `repocli`，用户状态目录为 `~/.repokit`。
8. **代码改动同步版本。** 同一 PR 修改代码时 bump 根 `VERSION`，默认 patch，新增功能或不兼容变更按 SemVer 选择 minor/major；仅文档改动无需升级。源码构建从 VERSION 注入 CLI 版本，发布 tag 和独立包版本见发布契约。

9. **按目标模型演进。** 设计与实现不受历史数据和旧调用方兼容性约束；直接收敛到明确的职责与契约，不为旧结构保留兼容读取、双格式或迁移支线。

## 开发验证

在实际改动的 leaf Component 执行 `make format`、`make lint`、`make test`、`make build`，通过
`repocli validate` 统一规划与执行检查。验证目录为 `toolkit/go`、`toolkit/typescript`、`toolkit/python`、
`apps/cli`；根 `make format/lint/test` 仅提示、不递归，显式全仓检查使用 `make check`。
根 `make build` 汇总构建，`make install` 只安装 CLI。TS 先安装包依赖，Python 使用 `uv sync --locked`。
关键行为用隔离临时仓库和多语言语料验证，不能运行被分析项目的测试来替代分析器自身的回归。

## References

- [README.md](README.md)：判断能力边界、开始使用或接入。
- [仓库结构识别](docs/repository.md)：tree / inspect、Component、语言与工具证据。
- [仓库状态](docs/state.md)：state 业务边界、身份、持久化、复用与对账。
- [Git](docs/git.md) / [worktree retention](docs/worktrees.md)：查询、操作、checkout 拓扑及回收保护。
- [用户配置](docs/configuration.md)：RepoKit home 配置、host 级凭据与 SDK 接入。
- [PR](docs/pr.md)：Forge 身份、读写、持续扫描和观察生命周期。
- [Snapshot](docs/snapshot.md)：内容捕获、摘要与完整性。
- [CodeGraph](docs/codegraph.md)：图模型、材料准入、语言解析与关系证据。
- [Deadcode](docs/deadcode.md)：全仓、文件范围与 diff 依赖入口，候选变化、可达性及风险。
- [Impact](docs/diff.md) / [使用方式](docs/diff-usage.md)：影响范围、测试候选、缺口与 CLI 参数。
- [Fragment 与 Unit](docs/units.md)：变更形成、关系优先级、聚合及预算。
- [代码图浏览](docs/view.md)：viewer 的版本、展示与刷新契约。
- [Depot](docs/depot.md) / [Makefile 验证](docs/validation.md)：依赖准备、检查规划、执行与结果复用。
- [Go 错误契约](docs/errors.md)：SDK 结构化错误、cause 链及共享 JSON/text 表达。
- [CLI 输出契约](docs/cli-output.md)：统一输出结构、结果与诊断、证据、退出状态及兼容性。
- [闲置清理](docs/cleanup.md)：访问记录、30 天策略、磁盘回收与跨语言 lease。
- [日志](docs/logging.md) / [统计](docs/stats.md)：执行记录、timeline、重放与只读统计。
- [发布](docs/release.md) / [CLI 更新](docs/upgrade.md)：模块与包版本、构建、发布和安装。
