# 仓库结构识别

Go API `repokit.Inspect`、TypeScript/Python API `inspect` 与命令 `repocli inspect` 报告选定版本的 Repository、Component、语言、tools 和 Depot 声明。身份类型复用
quality-harness 各语言的 common；布局发现、文件归属和包工具识别由 RepoKit 负责。

## 基础内容视图

`repocli tree` / Go `Tree` / Python、TypeScript `tree` 返回目录、文件及已知文件角色。
它不依赖 Component 识别，没有 Component 时仍可查询内容。与 inspect 一样，支持
工作区、index 和 commit，工作区包括未忽略的未跟踪文件；不递归进入独立仓库。

```sh
repocli tree --json
repocli tree --staged --json
repocli tree --head HEAD --json
```

结果包含 checkout、input、head、directories、files、complete 和 diagnostics；CLI JSON schema 为 1。
Directory 以 path 表达，根为 `.`；仅返回 Git 条目隐含的目录，不扫描忽略目录或空目录。
File 包含 path、kind、mode，以及可确定时的 oid。kind 为 regular、symlink 或 gitlink；role 可为
manifest、build_script 或 lockfile，未知角色省略。Manifest 另保留 path、ecosystem；文件角色与
Git kind 不混用，名为 go.mod 的符号链接不会被当作 Manifest。

工作区普通文件不计算 OID；gitlink 在已初始化时读取 HEAD，否则保留 index 引用。
index/commit 的 OID 来自对应版本。gitlink 本身不作为 Directory，子仓内容不进入父仓。
可变输入复读条目，不一致返回 tree_changed。这里的完整性只覆盖路径、类型与引用，内容身份使用 snapshot。
查询限制为 10,000 项，所有 Git 调用共用调用方时限；异常、超限、未合并 index 不返回虚假的空结果。

## inspect 与共享识别规则

```sh
repocli inspect --json
repocli inspect --staged --json
repocli inspect --head HEAD --json
```

默认枚举 Git 跟踪及未忽略的工作区文件；`--staged` 使用 index，`--head` 先解析为确定 commit。
inspect 只读取识别所需的元数据内容（包括各目录的 `package.json`、`pyproject.toml`、`pnpm-workspace.yaml` 和 `.yarnrc.yml`），
其它普通文件仅提供路径和标记文件存在性。它不读取源码内容、计算全仓摘要、解析 AST
或捕获子模块内容，也不执行项目代码、Makefile 或安装依赖。仓库无需添加 RepoKit 专属配置文件。

Go 的 `project.Load` 与 TypeScript/Python 的内部 layout 实现遵守相同的组件、语言和包工具规则，
由 `conformance/inspect` 的共享用例验证。inspect 提供轻量文件目录和必要配置；diff/view
使用各自已经捕获的同版本材料。diff 的前后版本及 patch postimage 分别识别，不能借用工作区配置。
这些规则无需构建代码图；CodeGraph 在 diff/view 的代码事实分析阶段提供 manifest 声明、符号与关系。

JSON schema 1 包含 `repository`、`components`、`checkout`、`input`、可选 `head`、`complete` 和
`diagnostics`。没有 `snapshot` 摘要；它不能作为全仓验证结果复用的内容指纹。
`complete` 只描述路径和必要配置的观察是否完成，不证明项目可构建或自动发现的组件边界符合所有业务意图。
工作区和 index 会复读路径及必要配置；发现变化时返回 `inspection_changed` 且 `complete: false`。
这不能提供文件系统原子性，也不追踪无关源码内容的并发编辑。

文件目录最多 10,000 项，必要配置每份最多 2 MiB、合计最多 128 MiB。读取错误、未合并的 index、
超限返回退出码 1，不伪造根组件。必要配置的符号链接不跟随；不读取嵌套仓库、
gitlink 内容。参数错误退出 2；正常报告（含观察到的并发变化）退出 0。
畸形 package.json 按既有最佳努力策略保留目录标记，但不提供 packageManager 证据。

`snapshot` schema 2 专注内容身份，不再携带仓库结构；查询旧组件字段的调用方改用 inspect。
view API 在顶层提供 `repository` 和 `components`，其 `snapshot` 仅描述内容身份；diff 保留组件影响结果。

## Repository、Component 与语言

Repository 身份来自 origin，无法确定时为 null，checkout 路径单独报告。
Component 是 lint、test、打包等工程操作的粒度；RepoKit 识别边界与工具证据，调用方决定并显式执行操作。

- 优先依据 manifest 识别 Component：`pyproject.toml`、`go.mod`、`package.json`、`setup.py`、
  `Cargo.toml` 的存在提供组件根候选，不要求先解析项目声明，也不执行文件。
- 同目录没有 manifest 时，以 `Makefile` 作为次级依据识别 Component，language 保持未知。
  requirements、tsconfig 和源码单独出现不创建 Component，也不为 Makefile-only Component 推断语言。
- manifest 和 Makefile 都没有时，就是未识别到 Component 的 Repository，`components` 为空，不补造根组件。
- Component 可在任意目录层级嵌套；每个符合条件的 manifest 或 Makefile 目录都保留自己的边界。
  依赖、生成物、隐藏目录和 testdata 不参与发现；Git 捕获边界先于组件发现生效。
- 文件归属于最长匹配的组件根目录。仅有子组件的仓库中，根目录共享文件可以没有归属。
- 同目录的多个 manifest 按 Python、Go、Node、Rust 的优先级确定语言；Node 根据包中的 TypeScript
  线索或 tsconfig 区分 TS/JS。未知语言省略；common 的 Ecosystem 从语言派生，不单独持久化。

代码图中的 manifest 分类、项目名、版本号和模块声明来自 CodeGraph；组件命名、目录排除和嵌套规则由
RepoKit 决定。比如 `testdata/corpus/go.mod` 可以保留为 manifest 图事实，却不会自动成为 Component。
只含工具配置或解析不完整的 manifest 仍可作为目录边界候选；这不声称该目录可独立构建。

Component 的身份复用 common 类型，布局和工具证据由 RepoKit 持有。`manifests` 保留自身根目录内
已观察到的 Manifest 路径；Makefile-only 组件没有 Manifest 证据。Product 关系不从目录推断，识别结果的
`products` 为空。语言、目录和包工具不参与 Component 身份，也不意味着组件间没有依赖。

Component 发现与 workspace 成员声明分别解释：未列入 workspace 的子包仍可形成 Component；
依赖安装根由包管理器的 workspace 声明确定。父子组件的验证入口与汇总执行策略由调用方决定。

## Component 数量与归属

| 形态 | 组件根 | 目录与文件归属 |
|---|---|---|
| 无 Component | 空 | 保留内容，owner 为空 |
| 单个子目录 Component | app | app 内归属该组件，外部可无归属 |
| 单个根 Component | . | 根内普通目录也属于该组件 |
| 多个同级 Component | api、web | 分别归属，docs 等共享目录可无归属 |
| 多层 Component 共存 | .、sdks/typescript、sdks/typescript/common | 文件归最深组件，父级保留其余文件 |
| 仅有 Makefile 的 Component | Makefile 所在目录 | 按根目录归属，language 未知 |

这些形态由三语言共享的 `conformance/inspect/layouts.json` 验证；组件数量不改变 tree 的内容查询能力。

## Depot

`Component.depot` 复用 common 的 `Depot`，保存按仓库相对路径声明的 `bins`。
声明描述可恢复的逻辑位置，目录尚未创建或已经回收时仍保留。Node 的 `node_modules` 和 uv 的
`.venv` 根据 workspace 声明解析到安装根；成员 Component 可以共享同一路径。
识别、准备、回收和验证复用的边界见 [Depot](depot.md)。

## 工具证据

`tools` 使用 common 的 `Tool`，每项包含 name 和可选 version。RepoKit 的 `toolEvidence`
（Python 为 `tool_evidence`）按工具名保存发现证据路径。工具声明与发现证据分开，
均描述当前捕获材料中的工具线索；多条线索保留为多个候选，不指定执行工具。
version 仅保留清单声明，不查询本机安装版本。

| 依据 | 工具 |
|---|---|
| go.mod | go |
| uv.lock / poetry.lock / Pipfile.lock | uv / poetry / pipenv |
| package.json 的 packageManager | npm / pnpm / yarn / bun，并保留声明版本 |
| package-lock.json 或 npm-shrinkwrap.json | npm |
| pnpm-lock.yaml / yarn.lock / bun.lock 或 bun.lockb | pnpm / yarn / bun |

只读取组件根目录的材料，不把父目录锁文件自动继承给子组件。workspace 成员关系、锁文件依赖图和
运行命令不在此检测范围。仅有 requirements 或 pyproject 不足以判定 pip 或其它具体管理工具。
未知工具保持缺省；畸形 package.json 不提供声明证据，但其独立锁文件仍可提供线索。

## TypeScript 调用契约

`inspect({ repository, staged, head, signal, timeoutMs })` 在 Node 进程内完成识别，返回 schema 1 对象。
`owner(report, path)` 返回最具体的 ComponentBinding，未归属时返回 undefined。结果与 common 身份字段
均按只读使用。Go 由 context 提供截止时间，TS 默认整个调用五秒，可通过 timeoutMs 调整并用 signal 取消。
读取失败或参数错误会抛出异常；部分结果仍由 complete 与 diagnostics 表达，退出码属于 CLI 适配层。

TS 与 Go 使用相同的路径、元数据容量及子仓库边界。TS 的单次 Git 输出另有 16 MiB 上限，超限拒绝；
读取 blob 前先批量检查不可变对象的大小，再按总预算读取。文件名识别目录来自固定依赖的生成产物，
Go 测试检查目录漂移，TS/Python 测试核对全部生成的路径探针。运行时无需 repocli 二进制或 Go 工具链。

## Python 调用契约

`inspect(repository, *, head=None, staged=False, timeout=5.0, cancel=None)` 提供同步调用，
返回 frozen dataclass `InspectReport`；字段使用 snake_case，集合使用 tuple。
`owner(report, path)` 为同步纯查询，接受仓库相对路径，未归属返回 None。取消使用 threading.Event；
整次调用共用秒级 timeout；Git 子进程在超时、取消或输出超限时终止并回收，文件系统读取与
Python 投影在操作间检查预算。超时、取消、无效输入和 I/O 错误抛出异常；并发变化仍以 complete/diagnostics 表达。
运行平台为 macOS/Linux，依赖 PATH 中的 Git，容量、版本选择和子仓库边界与 TS 一致。Git 负责
index、tree、ignore 和 revision 语义；Python 负责布局投影。目录和必要 blob 按批次读取，先核对
对象大小，再获取内容；不按源文件启动进程。容量约束作用于工具包接收的数据，不是 Git 进程内存上限。
Python 复用已发布 harness-common 的身份类型；ComponentBinding 承载当前布局元数据。
共享语料比较三种实现的语义投影，不要求 Python 内存对象复制 CLI JSON 字段命名。

## 从已捕获 Diff 读取布局（Go）

`DiffReport.Inspect(before)` 复用比较中保留的源码快照，返回对应版本的 `InspectReport`，包括
`Snapshot`、Component、manifest、包工具证据和完整性诊断。它不重新读取 Git、工作区或 origin；
origin 身份在 Diff 捕获时观察。`before=true` 读取旧侧，删除或迁移的组件必须按相应侧解释。
`Select` 只缩小 Change 集合，不缩小布局；从 JSON 反序列化的 DiffReport 不含捕获材料，查询返回错误。

此接口与独立 `Inspect` 共用工程 Component 发现规则：未被排除的嵌套 manifest 形成独立 Component，
同目录多语言 manifest 作为证据保留，Component 的主要语言不限制所含文件的语言。
源码体积导致的缺口与捕获时并发变化继续报告，调用方不得用当前 checkout 补齐历史材料。

## Forge 仓库分页

TypeScript `ForgeReader.listRepositories({ pageSize?, cursor? })` 和 Python
`Forge.list_repositories(page_size=..., cursor=...)` 返回 `Page<RepositorySnapshot>` / `Page[Repository]`。
默认每页 50 项，允许 1..100 项。身份来自 common，分页契约属于 RepoKit。
范围是当前凭据拥有或参与的仓库，不能据此推断账号曾贡献过的全部目标仓库。
GitHub 使用 `/user/repos`，GitLab 使用 `/projects?membership=true`；不扫描本地 checkout。

每次调用一次 HTTP 请求，返回 items 和可选 nextCursor / next_cursor。当前 adapter 使用平台页号分页；
不透明 continuation 封装页号及查询条件，**不是 last_id/keyset 游标，也不提供原子快照保证**。
TS 游标绑定 provider、forge、endpoint、凭据摘要、查询及 pageSize；Python 绑定 provider、host、操作及 page_size，
由调用方保证凭据相同。省略页大小时沿用游标值。满页可能保守产生下一页，最终页可以为空。
不跟随游标或分页头中的任意 URL，避免向其它地址发送凭据。凭据轮换后 TS 需要重新分页。

```typescript
const first = await forge.listRepositories({ pageSize: 50 });
if (first.nextCursor) {
  const next = await forge.listRepositories({ cursor: first.nextCursor });
}
```

`iterateRepositories(reader, query?)` 使用 `@compforge/ts-stdx/iter` 的 loadingIterator。
构造不请求，当前页消费完才加载下一页，break 不预取，错误直接抛出。每个 iterator 独立、单次消费，
不自动重试、不缓存跨轮次清单、不保存业务检查点。清单快照应在遍历成功后整体替换；已 yield 的数据不会撤回。
需要持久化分页进度时使用单页 API，先持久消费该页再保存 continuation。

成员关系、仓库新增或重命名可能导致翻页重复或遗漏。调用方按 common 身份去重，不能仅因清单缺席判定删除。
无法读取或不兼容的端点报错，不自动回退成本地仓库集合或平台专有 API，也不编造 total。


## 默认分支观察

Python `repokit.repository.default_branch(repo, forge=...)` 统一处理默认分支查询、持久缓存和
远端刷新。默认缓存一天；缓存以 common Git directory 与 origin 配置隔离，linked checkout 共享
观察，origin 变更不会复用旧仓事实。记录位于工具包状态目录的 `repositories/*/metadata.json`。
刷新优先使用 Forge，失败或无客户端时尝试远端 HEAD；失败返回显式 stale 观察及诊断，
不推进观察时间，不把 main/master 猜测记为事实。调用方只选择缺失时的开发基线，不维护 TTL
或传输 fallback。显式 `home` 可隔离应用/测试；`max_age=0` 请求刷新。

## Repository 本地信息

信息目录、read-through、freshness 与回收边界见 [Repository 状态](state.md)。
