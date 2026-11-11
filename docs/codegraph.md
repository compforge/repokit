# CodeGraph

本文描述 diff 消费的仓库关系投影。view 直接展示共享 CodeGraph 的完整事实，见 [代码图浏览](view.md)。

CodeGraph 构建并查询带证据的局部代码关系图。RepoKit 负责仓库定位、内容捕获、材料准入与仓库上下文；
共享 CodeGraph 负责语法、声明与语言绑定事实，RepoKit 不根据名称或结构边重新推导语言规则。
每张图属于一个内容版本；图、关系位置和显示源码使用同一份捕获内容。前后版本分别构图和查询，
不能把两侧的边拼成一条证据路径。关系聚合、距离排序、测试推荐及 Web 布局由消费者决定。

RepoKit 通过共享 [`compforge/codegraph`](https://github.com/compforge/codegraph) 获取跨语言声明类别、
源码范围、细分语义依赖。公开值模型来自 `codegraph/pkg`，影响查询保留原始 relation kind 与证据，仅投影仓库查询所需的节点身份。仓库级模块解析通过 ResolutionContext 提供给构图；import 的探索与影响关系、配置继承、
Go package 关系和测试候选仍由 RepoKit 负责。

diff 的共享 Builder 显式采用 AnalysisAdmission 的实体粒度，保留核心声明及 dependency bridges，
按需发布节点；仓库捕获层已确定的材料范围保持不变，包括显式测试候选。view 继续使用完整图。
省略的局部变量仍参与语言绑定，修改行由保留的所属声明覆盖；无法定位时使用文件级保守种子。
MaxNodes / MaxRelations 约束共享图的发布规模，内部语义工作由 CodeGraph 的独立分析额度保护。

## 概念

共享 CodeGraph 组合 GoGraph 的属性图与 Cypher、gotreesitter 的语法与事实提取、pond 的异步调度。
**Document** 是共享图的输入材料及节点，**Directory** 表达已接纳材料的祖先路径，通过 `in_directory`
连接直接子项。NodeKind / RelationKind 表达实体与关系；source、documentation、resource、tooling、
generated、dependency 等物料分类使用 Tags。Language 保存 grammar，Gitlink 保存子模块 commit，
清单元数据和格式语义独立于路径标签。

具体声明通过 `declares` 关联 Document，词法嵌套通过 `encloses` 表达。源码发生点与 `BindsTo` 是导航事实，
与声明依赖分开消费，不把 Reads/Writes/UsesType/Captures/UsesResource 压成泛化引用。
RepoKit 使用共享 `codegraph/pkg` 的开放 RelationKind，仓库专有关系扩展同一个类型。

**File** 是 RepoKit 对某一版本中仓库相对路径的称呼，file/symbol 投影服务于查询，不替代共享图的具体节点类别。
**Symbol** 表示文件中的具名声明，包含所属作用域和该版本的源码范围。
同名方法通过 owner 区分，例如 `A.work` 与 `B.work`。行号用于定位，不充当跨版本身份。
显式 import 指向的名字可以先作为 binding 节点存在；这证明引用了该名字，不证明已经找到其声明。
Module 节点表达语言层面的聚合，例如 Python 模块的整体引用、Go 的 package。

**Relation** 是有方向的语法关系，记录关系种类和证据位置。依赖方向为引用方到被引用方。
关系种类与确信度独立。`confidence` 为空表示有确定关系（JSON 省略该字段），`strong` 表示强推断，
`weak` 表示弱推断。共享 CodeGraph 的调用边保留原生 `exact` / `scoped` / `name_only` / `heuristic`、relation ID、完整 Evidence 数组和发生位置。
Evidence 记录各自的 basis、confidence 和可选支撑位置；仓库解析规则继续使用关系上的 basis。这里的确定性针对受支持的静态关系，不表示运行时一定执行；强弱档位不是概率。
解析器以 `basis` 记录解析依据。Python 的可见但未证明优先级的路径提供强推断；
仅从文件目录匹配模块名提供弱推断，即使只有一个匹配也不升级为确定关系。

关系按用途区分，查询必须显式选择：

| 关系 | 含义 | 可用于影响传播 |
| --- | --- | --- |
| `imports`、`reexports` | 明确解析的本地 import、导出别名 | 是 |
| `calls` | 共享 CodeGraph 支持的静态调用 | 是 |
| `reads`、`writes`、`uses_type`、`captures` | 值读取、写入、类型使用、闭包捕获 | 是 |
| `uses_resource` | 静态资源依赖 | 是；只有此类关系证明资源影响已建模 |
| `aliases`、`extends`、`implements` | 别名、继承、接口实现 | 是 |
| `binds_to` | 源码发生点到目标的绑定 | 否；Unit 使用它定位触及改动行的依赖证据 |
| `package_member` | Go 编译单元的隐式依赖 | 是，按 package 粒度 |
| `config_extends` | 显式配置继承 | 是 |
| `contains` | 文件或声明拥有子声明 | 反向投影已到达的声明至所属文件，不访问兄弟声明 |
| `config_scope` | 目录暗示的配置适用范围 | 仅定位分析缺口 |

**Graph** 持有单个版本的节点和关系。**Path** 是查询得到的关系证据链。
**Diagnostic** 记录实际提取、配置解析和探索预算的缺口。未知依赖目标不生成边；共享图报告的未解析调用保留为局部诊断。
**QueryResult** 给出候选节点到查询起点的置信度优先关系路径，路径保留关系的原生 confidence、仓库推断 basis 和源码 Evidence。

## 流程

```text
可读文件目录 + 关系种类 + 扩展上限
  → 创建空图和解析所需的目录元数据
  → 调用方加入待探索文件
  → 将预算内文件提交共享 Extractor，按队列顺序消费 Facts，解析仓库内目标
  → 按需展开确定目标、有限歧义候选和 Go package 成员，继续提交新发现的文件
  → 将 Facts 与有路径或配置依据的 ResolutionContext 交给共享 Builder，统一构图
  → 从共享图取得声明和静态调用，组合仓库上下文关系
  → 返回局部 Graph、实际解析文件及 Diagnostic
  → 沿有目标的关系反向查询，返回候选节点的稳定置信度优先 Path
```

Go 的 `_test.go` 分组属于语言编译语义，与调用方是否将它作为候选无关。

## 关键设计

### 提取事实与绑定上下文

Facts 保存完整单文件材料，探索过程中不构图。before / after 共享有界 Extractor，
各自的 Builder 依据该版本的材料与上下文重新绑定，目标删除或配置变化不会复用旧关系。
类型化访问直接读取 Graph，diff 无需创建 Cypher 索引。

Go 模块根和有路径、配置依据的 Python / JS / TS 模块目标进入 ResolutionContext；
Python 祖先初始化文件仍是加载依赖，只有模块目标参与符号绑定。
全目录同名匹配的弱候选服务于影响探索，语言绑定继续依据自身规则及局部诊断。

### 局部构图由业务范围驱动

调用方加入的文件沿 import 展开中间入口、re-export 和有限候选目标。文件目录为解析提供候选，
未进入 workset 的源码无需建立 AST。完整目录用于解析引用和配置，独立的源码准入集合限制实际解析；
以裁剪快照代替建图边界会丢失解析依据。Changeset、Testset 和 Workset 的范围由 [impact](diff.md#comparison-and-analysis-scope) 定义，
未选中组件的生产源码不因 import 或测试归属而自动准入。

构图和查询有不同的生命周期。`NewBuilder` 接受单个版本的文件目录和构建选项，创建空图；
`Add` 接受一批入口，批量提交已知文件的提取任务，按入口和依赖队列顺序有界展开，探索结束后一次构图。随后 `Result` 提供图、声明、解析范围及缺口，
其 `Query` 只查询这些关系，不读取源码。`Build` 是已知探索文件列表时的批量入口，使用同一个 Builder。
构图入口没有 test dir 或 diff 参数；查询中的起点和候选是普通节点身份。

diff 将 changeset 和 testset 一起作为入口；未指定测试目录时使用全部受支持源码作为候选入口，展开为有界 workset；变更声明与关系查询复用该版本的同一共享图。
这里的“增量”是同一次分析里逐步扩展局部图，前后版本使用独立 Builder。
一次 diff 内用有界内存缓存共享路径和内容都未变的单文件事实，分析结束后释放；
每个版本仍按各自的文件目录和配置重新绑定关系，文件与构建预算也分别执行。
`Result` 重新组合仓库关系和当前共享图事实，避免增量解析后的旧诊断或关系残留到新结果。

包与模块解析需要文件名及 manifest/config 元数据索引；这些与源码 AST 解析分开。
diff 的 workset builder 为各自内容版本准备 manifest 图，服务依赖解析；组件识别不依赖构图。Go 模块路径来自
Document declares Module，JS 包名来自 Document 的 manifest 属性；适配层不另行推断这两类身份。
清单分类通过 manifest tag 表达；模块解析依据 ManifestMetadata 与声明关系，不受标签规则开关影响。
共享图中的 Directory 与 in_directory 保留路径结构，影响分析的 file/symbol 投影不将其视为声明或调用。
RepoKit 仍使用原生配置读取器校验 Go 构建配置并解释 replace、package exports、依赖与 packageManager，
CodeGraph 没有提供的仓库行为不能通过一个 manifest 分类推断。manifest 的解析预算与源码 workset
分开，不占用源码文件数限额。view 由实际选入的 Document 构图，省略的 go.mod 不暗中提供模块声明。
扩展深度和文件数受请求约束，达到边界时保留已经证明的引用，并输出 Diagnostic。
workset 内所有源码都提供声明；查询种类决定是否消费本地调用关系及相应缺口，而非另设详细文件范围。

### 通用源码事实与消费者上下文分层

共享 CodeGraph 负责语言识别、声明具体类别、源码范围和其支持的调用关系；RepoKit 不复刻其声明类别
或范围算法。RepoKit 的适配层保留既有报告与节点身份，并继续提取 import、导出、Python 静态路径上下文和
配置事实，因为这些信息服务于仓库级解析与测试选择策略。impact 无需识别 AST；原始位置和诊断使用共享类型。

共享 CodeGraph 的语法、声明和未解析调用诊断保留 subject、location 和 outline 计数，
由 impact 输出为局部 observation。import、导出与动态加载仍由 RepoKit 的仓库级 resolver 处理，
共享图库在不同仓库解析上下文中的 import 缺口不能重复当成最终缺口。

声明覆盖由共享 CodeGraph 的 `Capabilities(language)` 决定，只有具备声明类别的 grammar 才作为源码语言。
Go、Python、JavaScript、TypeScript 和 TSX 同时具备 RepoKit 的依赖上下文；Java、Rust、C/C++、Ruby 等
outline 语言可以报告变更声明，但引用解析保持不完整。outline 没有报告遗漏不等于覆盖全部声明。
箭头函数通过变量声明取得身份。
多个名称共享 Go 声明范围时保留 outline 歧义诊断，不任取一个名称。Python 赋值和 JS/TS 解构等未覆盖
声明不能提供符号级起点；未被符号范围覆盖的变更由 diff 使用文件起点，Go 的影响传播始终保持 package 粒度。


Python 采用有界静态求值：跟踪受支持的变量、导入别名、文件相对路径和字面量序列，按语句顺序处理路径变更。
无条件插入到已知搜索前缀的目录可以证明查找优先级；append、未知分支和延迟执行的函数体不能证明优先级。
分支合流只保留一致的值和路径前缀，参数与局部赋值会遮蔽外部绑定，未知路径写操作使既有前缀失效。
一次 import 使用它所在位置的上下文，后续路径操作不会追溯影响它。搜索根目录自身及其祖先不会被误当成模块的包初始化依赖。

该模型不执行 Python，不模拟 import hooks、模块缓存、跨模块副作用或任意函数调用。未知目标的依赖操作不生成关系，
求值预算耗尽保留诊断；普通数据与无关控制流本身不构成依赖缺口。AST 规模、求值步数和字面量展开都有上限。

### 关系证据与声明归属分开

`A.work` 的变更不能仅沿 `contains` 到达 `A.other`。反向查询只遍历指定的关系，
不会因为两个符号属于同一个文件就创造调用关系。依赖路径可以包含中间 File、Symbol 和 Module。

对测试的直接 named import，diff 采用“导入了变更符号即相关”的粒度，不要求测试实际调用它。
本文件调用支持 Python/JavaScript/TypeScript 中指向唯一模块级函数声明的裸标识符调用；
参数、赋值、局部声明等引起的绑定冲突有明确候选时生成推测调用边；无目标时省略该关系。
方法动态分派、嵌套函数目标、箭头函数目标、赋值别名、任意值引用及完整类型推导不在此支持范围内。
箭头变量仍可作为变更声明和 import 目标。

Module/namespace/default import 保持模块粒度，Go 保持 package 粒度。
跨文件路径到达一个 importing File 后，会继续按文件粒度传播。
这些是明确的静态近似，不声称完整运行时调用图。

### Best-effort 可达性

确定边和推测边都参与推荐，关系的 confidence 与 basis 保留在解释路径上。
歧义 import 与条件导出把已捕获的候选目标记录为推测边；完全未知的动态目标不记录。
这允许推荐存在误报和漏报，空结果不是独立性证明。

查询按 exact、strong、weak 三档分别寻找最短依赖路径，原生 scoped 归入 strong，name_only 与 heuristic 归入 weak；先选最强可达档位，
再比较依赖距离和关系步数。三次有界搜索保留在后续弱边处可能更优的短弱前缀，避免单标签贪心丢失解释。
contains、package_member 和 reexports 的距离为 0，其余参与影响传播的关系为 1。
声明起点不会立即扩大到它自己的整个文件；已到达的其他声明可沿 contains 投影至文件，
不反向进入同文件的兄弟声明。遍历阶段只保存通向起点的前驱边，
输出时为请求的候选恢复路径，避免为所有中间节点重复复制完整路径。
环路终止、关系过滤、版本隔离和取消检查是查询契约；文件归属不会凭空生成依赖边。

配置解析器通过独立资源目录读取同版本中显式引用的子模块 JSON。它遵循内核的输入边界；
资源不可读与文件缺失分别报告，配置变化仍影响其父仓消费者。

图是一次分析的内存结果；本包不提供持久化索引、图数据库、全仓调用图或后台服务。
CLI 的 diff 结果和参数见 [diff-usage.md](diff-usage.md)，业务流程见 [diff.md](diff.md)。

## Source admission

The captured file catalog supplies reference and configuration context; it is not an
instruction to parse every source. Internal builders accept an optional `SourceFiles`
admission set. A nil set preserves unrestricted catalog expansion for other consumers;
an explicit set bounds parser submission and dependency expansion, including Go package
members. Known excluded references remain in the overlay with `source_scope` diagnostics.
Impact owns Component/Testset selection and interprets these planned boundaries as
observations; the generic builder has no Component or test-discovery policy.

## 从调用方的只读视图构图（Go）

`GraphFromSource(ctx, source, GraphOptions)` 消费一个已有的 `GraphSource`，不重新捕获仓库。
视图通过 `SnapshotID`、`ReadDirectory` 和 `ReadSource` 提供身份、直接子项和内容；调用方保证固定视图的身份与字节一致。`OpenGraphSource` 可用于
独立分析：commit/index 输入固定捕获，未指定版本时支持工作目录及没有 `.git` 的源码目录；
目录读取是调用期间的观察，不承诺原子快照。已捕获 review 必须直接复用它自己的视图。

调用方先确定 Repository、Component 或 diff 所需的材料范围，通过 `CodeGraph.Scope` 指定路径范围，`Select` 选择候选文件格式。
RepoKit 在调用 `ReadDirectory` 之前检查目录范围、标签与祖先忽略规则；工作目录只读取允许进入目录的
直接子项。捕获快照通过 `NewSourceTree` 建立内存索引，保留同一版本的路径和字节。
目录命中 `Admission.ExcludedTags` 或祖先 Git ignore 规则时剪去整个子树；允许进入后，才读取本目录
`.gitignore`，再判断其 Document 与子目录。嵌套与否定规则由隔离临时仓库中的 Git 解释，
已排除或范围外子树的规则无需读取。仍会影响候选材料的规则不可读或超限时明确失败。
忽略规则元数据分别受 `MaxDocumentBytes` 单文件和 `MaxSourceBytes` 累积字节上限约束。

准入后的材料按 metadata、`Prioritize`、路径排序，再应用图的文件数、单文件字节和总字节上限。
调用方需包含建图所需的清单。`IsSourceMaterial` 只识别源码或清单格式；材料排除由显式准入策略拥有，
工作目录与 commit/index 视图共享这套规则，普通隐藏目录不被输入适配器提前删除。
`go.mod` 等清单作为 Document 交给 CodeGraph，模块名和关系不由 RepoKit 或调用方重新解析。
`GraphResult` 保留 Graph、BuildReport、实际接纳的 Sources 和单独的 InputDiagnostics。
它不是对调用方快照重新计算的内容摘要；图身份由 `Graph.Snapshot()` 读取。

`RetryOnBudget` 默认关闭，严格消费者保留结构化预算错误。允许局部覆盖的调用方可显式开启，
预算失败后在同一 context 期限内按有序输入前缀缩小图；被省略的材料和实际发布字节保持一致。
读取失败、材料预算和图缩减均有诊断，不能把不完整图解释为不存在依赖。

`SourceRoles(path, graph)` 提供 source、manifest、lock、test、entrypoint 和 handler 等职责提示。
Go 运行入口只消费 CodeGraph 的声明事实，不能用 `main.go` 文件名证明入口；Python/Node 命名与框架
角色是仓库约定提示，不等同于已证明的框架绑定。测试命名标签由 CodeGraph 的 `TestTag` 提供，测试发现与 deadcode 排除共享它；RepoKit 不再维护后缀列表。
测试支持文件（如 conftest.py）可以具有 test 角色，
并不自动成为 impact 的可运行测试候选。调用方决定如何评审、是否执行或如何构造上下文。

接入按目录枚举的视图时，`ReadDirectory(ctx, path)` 返回 `SourceDirectory`，其中的 Directories
和 Documents 都是仓库相对的直接子项，不递归枚举。已有捕获视图可将其 `SourceFiles` 目录交给
`NewSourceTree` 并复用其 `ReadDirectory`，继续用原视图提供身份和读取字节。

`IsSourceMaterial` 是格式识别，消费方升级时必须显式迁移材料排除策略。例如 review 可在
`Admission.ExcludedTags` 中选择 dependency、test_fixture、generated、tooling、cache 与 build_output；
所选标签由调用方决定，不能再依赖输入适配器或格式 helper 隐式过滤路径。

## 资源引用

RepoKit 显式启用 CodeGraph 的 `semantics/nodejs.ResourceReferences()`，识别
`new URL("./template.html", import.meta.url)` 这类静态文件引用，包括受支持的导入别名。
diff 和完整仓库 view 均启用该适配；`GraphFromSource` 仍使用调用方显式传入的 CodeGraph 配置。
引用不要求经过 `readFileSync` 等读取函数；API 语义由 CodeGraph 的独立适配包提供，
RepoKit 不重复识别函数名，也不从普通字符串猜测依赖。

diff 的变更集或显式构图入口中的资源随工作集作为 Document 加入，和源码共同受 MaxFiles 约束。
引用关系保留原始 ID、位置和 Evidence；局部初始化变量的依赖桥保留在影响查询中。
影响分析分别查询变更前后的图，因此删除或重命名仍可通过旧版本的资源找到相关测试。

本次接入不扫描整个资源目录，也不递归补齐所有 URL 目标。目标未进入工作集时保留
`resource_not_loaded`；动态模板、变量路径等保留 `unresolved_resource`。有明确引用的资源
变更可以提供已知依赖路径；没有引用证据的资源仍报告未建模影响。资源引用本身不证明读取、
打包或运行时执行，动态 `Bun.build` 等构建输入仍需要进一步的静态事实支持。

RepoKit 直接依赖包含语义 lowering 的 CodeGraph 版本。共享图的依赖经过同一个投影入口，保留
`RelationKind`、ID、位置、confidence 与 Evidence；不会重新合并为泛化的 `references`。
影响策略明确选择传播种类，资源分类只认 `uses_resource`。源码发生点通过 `NodeKind.IsOccurrence()`
识别，`binds_to` 用于导航，不进入声明依赖路径。仓库特有的 package/config 等关系扩展同一个开放 enum；
仓库输入、候选范围和查询策略由 RepoKit 拥有，语言绑定与依赖语义由 CodeGraph 拥有。
