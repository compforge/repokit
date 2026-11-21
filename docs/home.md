# RepoKit Home

Home 是 remote 与 local 的桥梁：将 Forge / Git remote 上的 Repository、PR、Comment 与本机
不同位置的 clone、branch、worktree 关联起来。RepoKit / repocli 结合远端事实、Home 中的配置与
辅助信息操作本地仓库，源码继续留在调用方选择的位置。

Home 保存访问配置、仓库登记、身份关联、操作来源、观测和执行记录。它不声明远端全集、
实时同步或跨系统事务；目录存在也不构成操作授权。

其中的观测与派生结构是有时效、可回收的缓存和辅助信息，代码不能预期这些记录永久存在，
也不能将其当作稳定的业务事实。PR、Comment 及平台状态以 Forge 远端为权威；Git 状态以对应
Git 仓库为权威。缓存仅在明确的访问范围与新鲜度契约内复用，缺失或过期由 RepoKit 按需重建。
本地配置、创建来源和执行历史仍遵循各自的来源与保留契约，不与 Forge 观测缓存混为一类。

## 代码组织与职责

`state` 是 Repository 状态的业务组件，负责身份与关联、观测复用、checkout 生命周期、
状态发布及恢复。Git 适配层负责读取 Git 事实和执行命令，不依赖 state，也不回调 state 发布记录。
创建、移除与清理 checkout 由 state 协调 Git 操作和状态更新；上层 CLI/Forge 消费 state 的业务入口。
持久化记录是 state 的实现材料，不能通过直接改 JSON 代替 Git 操作。
访问记录、worktree 保留与各领域缓存分别维护；聚合清理协调所属领域入口。
state 不承担 Forge 连接、Git 命令执行、validation 工作流或 CLI 展示，也不作为所有持久化数据的总入口。

| 实现 | 目录与入口 |
|---|---|
| Go | `toolkit/go/state`：`LoadBranch`、`Observe`、`AddWorktree`、`RemoveWorktree`、`CleanWorktrees`；Git checkout 适配在 `internal/git/worktree` |
| Python | `toolkit/python/repokit/state`：`RepositoryState`、`default_branch`、`add_worktree`、`remove_worktree`、`clean_worktrees`；Git 适配在 `repokit/git` |
| TypeScript | `toolkit/typescript/src/state`：`RepositoryState`；Git 适配在 `src/git` |

各语言的 state 目录同时拥有 home 解析、身份编码、记录存储和 lease 协议。Forge 的 PR 查询与
观察流程复用这套存储能力，validation 继续拥有自己的执行历史格式与保留规则。
源码模块的组织不改变下面的磁盘布局和跨语言记录协议。

结构信息复用、动态观测的新鲜度、操作前实时核验分别属于 state 的读取与操作契约。
当前 branch 读取已有 read-through；checkout 结构查询仍读取 Git，尚未实现跨调用的结构信息复用。

## 文件组织思路

目录表达对象的身份与强归属，文件表达该对象的事实和策略；关联关系通过完整身份引用表达。
文件布局同时服务对象定位、独立清理和失败后对账；清理契约见[资源生命周期](#资源生命周期)。

### 归属、关联与清理边界

Repository 拥有 PR，PR 拥有 Comment；Clone 拥有本地 Branch 和 Checkout。Worktree 是 linked
Checkout，使用同一身份和记录。PR 分别引用 source / target branch，Checkout 引用检出的 branch，
Comment 通过 reply_to_id 引用另一条评论。这些引用不改变对象归属，也不直接授予级联删除资格。

本地分支按 Clone + ref 区分；远端分支按 Repository + ref 区分。共享对象只保留一份记录，
使用者保存引用。反向关联优先按需计算，确有查询需要时再建立可重建索引，明确事实来源。
源码和依赖环境继续留在本地 checkout；Home 保存位置和领域记录，不按磁盘树复制源码树。

### 文件命名与信息表达

某层级的信息直接放在对象目录下，不机械增加 observations/ 或单设 mapping.json 重复身份。
文件按含义命名，例如 branch.json、checkout.json、creation.json、access.json；目录已确定访问对象，
PR/Comment 的连接观测文件可继续以 connection-key 命名，隔离不同访问范围。

文件名、存在性和内容都是信息载体：明确的布尔标记可用空文件，独立简单值可用短文本，需要整体
核验和更新的字段使用 JSON。需要一起变化的字段原子发布；标记文件同样要定义创建、删除和并发语义。
路径标识复用安全编码；同一事实有一个明确来源，派生索引可重建。访问时间显式保存，不用 mtime 代替。

目录分组应对应真实归属或领域职责。validation/ 将同一领域的历史、输入和输出放在一起；
不再通过 persistent/ 按保留期限拆散对象。配置、创建来源、当前观测可以共处一个对象目录，
各自保留期限由领域 cleanup 判断；只有整个对象已无保留内容时，才回收对象目录。

### 登记、访问与锁

checkout.json 拥有位置登记，状态字段标明观测时间；淘汰动态观测时保留仍有效的登记字段。
不在 repository 的集中访问记录中重复保存所有 checkout 的位置。clone.json 保存 common Git directory
等发现信息，失效登记经过实时核验后回收。登记是资源定位线索，不替代操作前的身份验证。

访问记录按实际决策粒度就近保存：PR、Branch、Checkout 各有 access.json；评论按 PR 的评论集合
淘汰时，使用 comments/access.json，无需为每条 Comment 增加文件。Repository 的 access.json
只保留自身聚合访问事实，不复制子对象清单。明确访问可以续期所属对象，后台刷新和维护不续期。

锁位于被保护的回收目录之外，仓库级清理还要与子对象发布协调。移动记录路径时统一调整三种语言
的锁键和顺序，继续复用现有空 .lock 目录 lease；文件表达原则不改变锁协议。读取与清理均不跟随
源码 symlink 扩大操作边界，记录发布失败仍须保留已确认的 Git/Forge 操作结果。

### 查找本地仓库

`repocli repo find <query>` 从当前 Home 的登记中查找本机主 checkout。仓库 URL 按 Forge 和仓库路径精确匹配，支持 HTTPS、SSH 和 SCP 写法。关键词按仓库名、owner、仓库路径及本地目录做大小写无关的匹配，完整仓库名优先于部分匹配。查询在本地核验 Git 拓扑和 origin。

同一仓库的独立 clone 各返回一行。输出包含仓库身份、本地绝对路径及去除凭据的 origin。已删除或已更换仓库身份的登记会跳过。用 `--json` 获取 `data.matches`，成功但没有匹配时返回空数组。登记无法读取时报告诊断。

### 对象布局

```text
~/.repokit/
  config.json
  locks/
    activity/<repo-key>.lock/            # 短期访问 lease
    resources/<repo-key>.lock/           # 仓库记录发布与回收 lease
  logs/
  scans/
  forge-http/
  repositories/<repo-key>/
    repository.json
    config.json                         # repository 级策略
    access.json
    metadata.json
    remotes/<scope-key>.json             # 按查询范围隔离的远端分支观测
    clones/<clone-key>/
      clone.json
      access.json
      branches/<branch-key>/
        branch.json
        creation.json
        access.json
        associations/<scope-key>.json   # 可重建的 PR 关联查询记录
      checkouts/<checkout-key>/
        checkout.json
        access.json
    prs/<number>/
      access.json
      <connection-key>.json
      comments/
        access.json
        <comment-key>/<connection-key>.json
    validation/
      lock                              # 领域自身的 OS advisory lock
      history/
      inputs/
      logs/
```

本地对象键沿用 common Git directory、checkout 路径及 ref 的明确身份编码；移动路径后需重新核验
登记，不把本地键提升为公共 Repository 身份。PR 关联保留完整源/目标仓库身份和访问范围。
远端查询缓存、跨仓扫描 checkpoint、传输协调记录继续由所属模块管理。

## 目录与身份

Home 统一解析优先级为显式 `home` / `Home` / `--home` → `REPOKIT_HOME` → `~/.repokit`。
解析本身不创建文件；省略 Home 使用默认目录，不表示禁用持久化。空字符串视为省略，纯空白路径拒绝；
路径做绝对化和词法规范化，不解析 symlink。CLI、配置、Forge、topology 和验证默认历史均从此根派生。
`--history-dir` 只覆盖验证历史根，不改变其它信息的位置。

工作源码继续位于调用方选择的位置，不搬进状态目录。branch 可以没有 checkout；detached checkout
只有 checkout 记录。PR 属于目标 repository，branch 关联记录可以引用另一个目标仓。
多个 clone 的同名 branch 分别保存 head；任何一次观察只更新当前 common Git directory 对应的 clone。

三种语言共享 `conformance/repository-state.json`：Forge + 完整 namespace/repository 形成 repo key，
小写 ASCII 常见名字可读；大写、分隔符与其他 UTF-8 字节编码为 `%XX`。branch slash 不成为目录层级，
大小写不同的 branch 在大小写不敏感文件系统上也不碰撞。超长名字保留前缀并附内容摘要。
无可解析 origin 时使用 common Git directory 的本地身份。checkout key 由绝对路径摘要生成，
原始身份和路径始终保存在记录内；凭据不进入 repository 身份或路径。

## 读取与更新边界

| 信息 | 复用与刷新 |
|---|---|
| 内容版本、执行记录 | 由版本/输入身份判断；不因时间流逝伪造一次新执行，保留期限另行管理 |
| merged 生命周期 | 已确认的 merged 不回退；Python/TS 日常轮询停止，显式刷新仍可更新标题等元数据 |
| PR 标题、开放/关闭状态、关联 | 检查 connection scope 和 freshness；按需查询 Forge 后写回，closed 可 reopen |
| 默认分支 | Python `default_branch` 按 `max_age` 复用；失败保留旧值但返回 `fresh=False` 与 diagnostic |
| branch head、checkout 拓扑 | 按 clone 与明确时效读取；`max_age=0` 请求 Git 当前事实 |
| 删除、合并等操作前置条件 | 在操作边界重新读取 Git/Forge；目录存在或缓存命中不能授权操作 |

查询者不自己实现“读文件、判断是否过期、再调 Forge”的第二套逻辑。Go `state.LoadBranch`、
Python `RepositoryState.branch`、TS `RepositoryState.branch` 负责 read-through，并返回来源、观测时间与 checkout 位置完整性 `complete`。
无法定位的注册 checkout 不会伪装成完整的空集合；缺失或版本不一致的 checkout 投影由读取流程按分支从 Git 重建。
单分支读取只发布该分支和相关 checkout，不依赖其他分支的状态文件。恢复失败仍返回错误，不伪造不存在。
缺失或过期记录由 RepoKit 刷新。读取失败与不存在分开；JSON 损坏、权限错误、Git 查询失败不会转成空集合。
观测不提供 Git 全局事务快照：一次规划共用一份 inventory，实际写操作继续重新验证。
同名 tag 不改变 branch 身份，分支名从完整 `refs/heads/` 读取。

Go `forge.Client.Load(ctx, repo, number, maxAge)` 在解析后的 home 下持久化 PR 信息；
`maxAge=0` 强制远端读取，`Get` 始终实时，供写操作与 cleanup 验证。CLI `pr show` 默认最多复用一分钟，
`--max-age 0` 强制刷新。三种语言对等价的 provider、Forge、endpoint 和 credential 使用相同连接摘要，契约由共享语料验证。
不同连接分别保存快照、scopeIds、重试和刷新进度；一个连接的读取不能覆盖另一个连接的观察任务。
相同连接通过共享 record lease 串行发布，并保留 discovery membership。生命周期快照缺少完整 PR 字段时，
Go 会补查 Forge，不把缺失字段当空值返回。Go 成功写入与缓存刷新共用记录锁，随后发布返回事实；
发布失败尝试失效旧缓存并记录 diagnostic，不能把已经成功的远端写入变成可重试失败。

Python `PullRequestObserver.get_pull_request(key, refresh=True)` 和 TS observer 的 `refresh: true`
允许刷新 merged 后的可变元数据。刷新失败不推进成功观测时间，也不抹掉既有终态；需要当前元数据的
调用方检查错误/freshness，不能把“确认过 merged”当作“所有字段刚刚刷新”。

## 本地操作与恢复

访问与回收统一见下文[清理与回收](#清理与回收)。Comment ID 与平台回复结构从 Forge 数据重建，
不能依赖仅存在本地的映射；缓存删除后显式读取历史 PR 会重新查询 Forge。merged 不表示正文与
resolution 永远不变，也不要求永久保留评论缓存。

Go `state.Observe`、Python/TS `RepositoryState.observe` 记录本地事实；Git 创建/移除已成功但状态发布失败时，保留成功的 Git 结果并附 metadata diagnostic，避免误重试。`reconcile` 另外回收
当前 clone 中已不再注册、且源路径确实不存在的 checkout 记录及其 branch 投影。它只删除明确拥有的
状态文件，不遍历或删除 symlink 指向的源码。未知文件、未知 checkout 路径或读取失败不会
变成删除授权。仍存在的非注册源码目录保留其旧记录，供后续核对。

删除 branch 的投影只移除当前 clone 的信息；其他 clone、PR 关联和执行历史独立保留。
没有 clone 信息且没有其他资产的 branch 空投影可回收。执行历史按自己的 retention 清理，不能因为
checkout 消失连带删除历史。仓库根保留信息不会触发全量 Forge 扫描或后台实时同步。

Python/Go worktree 创建、移除和清理接受显式 state directory，并在成功操作后观察/对账。
CLI 的 `worktree ensure` 在确认创建/复用及可选依赖准备后，同步调用 worktree cleanup，
只维护所属 clone 的数量预算；`worktree clean` 和全局 `clean` 保留显式清理语义。PR 操作不触发文件系统清理。Python/TS PR context
查询 branch PR 时使用 Git 当前 inventory，不把 topology 发布作为读取前置条件。
CLI 的 `validate`、`lint`、`test`、`format` 在完成检查、输入核验和历史保存后返回，不刷新 branch/checkout 拓扑。
CLI 统一记录所选 checkout 的访问；访问时间不代表拓扑的新鲜度。拓扑由按需分支查询、worktree 生命周期操作
或显式 `worktree observe` 更新，首次验证也不要求预先建立拓扑投影。
原生纯分析 API 不隐式写入此目录；CLI 的仓库命令（含 plan）只额外记录所选 checkout 的访问，不改项目内容。

```sh
repocli worktree observe --repo /path/to/repository
repocli worktree observe --repo /path/to/repository --reconcile --home /tmp/repokit-state
repocli pr show 123 --repo /path/to/repository --max-age 0
```

## 持久化边界

记录通过临时文件、fsync 和 rename 发布；权限为目录 0700、记录 0600。topology、PR、Comment
与关联记录共用 repository 级 resources lease，避免父资源清理和子资源发布并发。访问与回收
按 activity → resources 的顺序获取 lease；远端查询不占访问 lease。
Go/Python/TypeScript 使用兼容的 `.lock` 空目录协议：获取等待最多 30 秒、每 10 秒续期、30 秒失效。获取期限不限制已获锁操作的总时长。
失去 lease 或取消后不得发布记录；同一 Home 的进程使用相同锁参数。Validation WAL 使用自己的 OS 文件锁，
锁文件位于保留的 validation 目录内，不随 Run 淘汰而删除；执行语义与保留规则见 [validation](validation.md#执行历史)。
锁的持有周期覆盖一次临界操作，正常结束、失败或取消均释放；资源保留周期独立。清理资源时仍持锁，
完成后释放。全局 clean 另外回收失效的空 lease 目录，不删除活跃 lease 或未知锁内容。
失败可能留下部分最新投影，下次分支读取或显式观察修复；因此它不是动作日志，也不能代替操作前重新验证。

只读写当前目录结构，不扫描、迁移或回退旧的 repo key、PR 文件和关联记录。
更改 home 是显式切换存储根，不自动搬运数据；需要保留现有目录时由用户在工具进程停止后移动目录。

## 验证场景

- 共享身份编码与环境语料由三个语言消费。
- 两个 clone 的同名 branch 有不同 head，观察一个不能覆盖另一个。
- detached、分支无 checkout、失败 inventory、保留源码与历史、回收失效链接。
- Python 与 TS 互读拓扑，并竞争同一个 lease；Go 验证取消、原子写和 lease 互斥。
- PR 连接身份、跨连接观察任务共存、写后读取、完整字段补查、merged 终态防回退。
- 部分发布后读取自动修复；无关分支损坏不阻断单分支读取；真实取消和 lease 替换禁止发布。
- 继承另一个 checkout 的 Git 环境时，查询与 Make 验证仍操作显式目标。

## 显式确保 checkout

`EnsureWorktree` 将已存在 checkout 的身份核验、已有分支恢复和新分支创建组织为同一生命周期入口。
它复用 Git 适配和 creation record；命名、基线选择与依赖准备时机仍由调用方决定。
三类结果、取消/失败和阶段成本见 [worktree 创建与复用](worktrees.md#创建与复用)。

## 持久配置、来源记录与操作恢复

`config.json` 保存 repository 级 worktree 策略，多个 clone 共用。Go
`SaveWorktreePolicy` / `LoadWorktreePolicy` 和 Python `load_policy` 使用同一路径，缺失字段继承 Home 默认配置。
分支创建来源按 common Git directory 隔离，记录 branch、baseRef、baseCommit、remote 与 createdAt。
`state.CreateBranch` 和 worktree ensure 在确认新分支后发布记录；记录失败保留已确认的 Git 效果并返回诊断。
Go `LoadBranchCreation`、Python `branch_creation` 与 CLI `git branch show` 读取来源；未经 RepoKit
记录的分支来源为未知，不从当前 ancestry 猜测。分支在外部删除重建后，旧记录仅是最后一次已确认创建的历史，
不能证明当前分支仍源于该基线，也不能覆盖调用方的任务基线或 PR target。

过期缓存清理保留配置、`creation.json`、仓库身份和有效 checkout 登记。Git branch/commit/rebase 的恢复记录继续
由 Git 操作组件保存在 common Git directory；它们不随观测缓存清理。Validation 历史仍由 validation
拥有格式与保留机制，不能因为物理上同处 RepoKit home 就纳入 state 的业务模型。

Python `checkout_view` 组装当前 checkout、分支、默认分支、拓扑与创建来源；`remote_branches` 按
请求的 refs、fetch URL 与时效复用远端结果。远端查询失败返回错误，不能当成空集合；这类读模型不构成
跨系统一致快照，任何删除、发布或覆盖操作仍必须实时核验。

## Comment observations

`comment-key` 是公共 Comment ID 的安全文件名编码，复用 `Part` / `name_part` / `namePart`。
完整对象身份为 `(PRKey, CommentID)`；路径已表示归属，无需 `mapping.json` 或 `observations/` 中间层。
`connection-key` 与 PR 观察使用同一访问配置摘要，隔离 provider、Forge、endpoint 和 credential，
文件中不保存凭据。同一评论可有不同访问范围的观测，不能互相覆盖。

每条记录的 schema 为 `{version: 1, connection, observedAt, comment}`。`comment` 使用公共 camelCase
字段，`pr` 保存完整 `{repository, number}`，Python 的仓库绑定 API 在落盘时补全身份。
完整列表成功后替换该连接的已观察评论集合；单条读取/写入保存确认后的结果，删除成功移除该连接的
对应记录。每条观测独立标记时间，不声称整组 resolution 同时刷新。

Go/Python 的 CRUD 与 TS 的读取始终查询 Forge，不以 Home 中的旧正文、resolution 或身份记录
授权写入。读取失败不能退回缓存伪装成功；缓存写入失败记录 diagnostic，但不把已确认的远端写入
变成可重试失败。线程句柄在 adapter 当次读取中重建，不额外持久化可恢复的第二套映射。
Comment ID、结构性回复边与平台能力见 [PR comments](pr.md#pr-comments)。

## 配置

RepoKit 的用户配置是 `<home>/config.json`。配置只支持 JSON，由用户维护；加载不创建目录或文件。
文件必须是仅 owner 可访问的普通文件（`chmod 600`）。文件缺失等价于空配置；损坏、未知字段、
字段名大小写不匹配、字段类型错误或权限过宽会报错，不转成空配置，也不在错误里输出文件内容。

```json
{
  "forges": {
    "github.com": {
      "provider": "github",
      "token": "<token>"
    },
    "git.example.com": {
      "provider": "gitlab",
      "token": "<token>"
    }
  }
}
```

### 身份、配置与状态

Repository 元信息已经包含 Forge 身份与仓库路径；配置以具体 Forge host 为键，同一个实例的仓库
共享访问配置。`github` / `gitlab` 公共身份分别解析到 `github.com` / `gitlab.com`；host 大小写
规范化，重复的规范化键拒绝加载。provider 是 `github` 或 `gitlab` 平台类型，不是实例身份。
自建 host 必须声明 provider，不根据名字猜测平台。
host 与 apiHost 使用同一语法：ASCII DNS label（国际化域名使用 punycode）、标准 IPv4 或带方括号的 IPv6；
可选端口为 1–65535 的十进制整数，不允许空端口、空 label、地址缩写或 IPv6 zone。
大小写规范化不改写显式端口或 IPv6 文本；三种语言不依赖各自 URL parser 的隐式规范化。

| 内容 | Owner |
|---|---|
| Repository 所属 Forge、仓库路径、checkout 与远端事实 | Repository / state |
| host → provider、API host、凭据来源 | 独立 config 模块 |
| HTTP 请求、连接隔离、PR 读写与观察 | forge |
| 提交、PR 目标、review 与合并流程 | 调用方 |

`config.json` 不属于 repository 缓存，`clean` 不删除或修改它。运行时访问记录、PR 观察和验证历史
由各领域组件拥有，state 提供共享身份、访问与存储协调。工具包不读取 `.devloop/config.json`、仓内应用配置或 `gh/glab` 登录文件，也不自动
搬迁凭据；使用方可一次性把自己的访问配置放到 RepoKit home。显式构造 Forge client/connection
仍可直接传参，不隐式加载文件。

### Forge 条目

| 字段 | 含义 |
|---|---|
| `provider` | `github` 或 `gitlab`；两个公共 host 可省略，其余必须设置 |
| `apiHost` | 可选的 API **host**，可含端口；不含 scheme、路径、用户信息。省略时从实例 host 推导 |
| `token` | 当前 host 的凭据；不进入 repository 身份或状态记录 |

API 路径由 provider 适配：公共 GitHub 使用 `https://api.github.com`，Enterprise 使用
`https://<apiHost>/api/v3`，GitLab 使用 `https://<apiHost>/api/v4`。API host 只改变传输地址，
Repository 仍属于原来的 Forge。CLI `--api-host` 保持完整 API URL 的运行参数语义；
`--provider` 若给出，必须与解析出的配置一致。

**Forge 访问配置只来自 config.json**，不读取 `GH_TOKEN`、`GITHUB_TOKEN`、`GITLAB_TOKEN`，
也没有环境变量名间接引用或逐字段合并覆盖。配置缺失即报缺失，避免终端、CI 和 agent 因隐藏的
环境变量获得不同账号。SDK 显式构造连接时仍可传入参数，配置加载和直接传参是明确分开的入口。
`REPOKIT_HOME` 只选择用户目录，不提供访问配置。

每次操作加载一个配置快照并跨仓复用，更改文件在下次操作生效。日志只记录配置位置、host 和
provider，不记录 token；配置对象的普通展示不包含凭据。

### SDK 与 CLI

Go 通过公共 config 包显式加载并解析，Forge client 继续只接收显式参数：

```go
settings, err := config.Load(home)
// 检查 err。
access, err := settings.Forge(repository.Forge.Name)
// 检查 err。
client, err := forge.NewClient(access)
```

Python：

```python
from repokit.config import load_config

settings = load_config()
connection = settings.forge(repository.forge.name)
client = connection.client(repository.path)
```

TypeScript：

```typescript
import { loadConfig } from "@compforge/repokit";

const settings = await loadConfig();
const connection = settings.forge(repository.forge.name);
const reader = connection.reader();
```

CLI 的 `pr`、`clean`、`worktree clean` 共用同一配置解析与连接组装入口。缺少访问配置时，
错误包含对应 host 与配置路径；PR 不可用不会被解释为无 PR。清理的部分失败语义见
[闲置清理](#清理与回收)。三种语言共用 `conformance/config/cases.json` 验证解析契约。

接入 devloop 时，使用这些入口替代其 `forge_entry`、`forge_token` 与凭据优先级逻辑；
PR 目标选择、review 配置和工作流策略仍归 devloop。RepoKit 不保留调用方配置的兼容读取分支。

#### 从 Checkout 访问 Forge

Python `forge_client(checkout, home=..., timeout=..., target_repository=...)` 和
`pull_request_context(checkout, home=..., timeout=..., targets=...)`，以及 TypeScript
`pullRequestContext(checkout, { home, timeoutMs, targets })` 统一完成 origin → config →
原生 client/context 的组装。无 Forge origin 返回空；缺少凭据或配置无效保持错误。
每次操作加载一次配置，不读取调用方配置，不推断 fork 的目标仓库。调用方显式选择 targets，
并负责 context/observer 的关闭。Python `pull_request_identity` 只解析身份，不读取凭据。

程序选择已有 CLI 查询时，显式导入 Python `repokit.cli` 的 `list_pull_requests` 或
`pull_request_description`。客户端校验 schema、command、outcome 与数据形状，保留错误，
共享调用方 operation 预算并限制输出容量；不持有凭据、缓存、重试或工作流目标选择。
原生 SDK 不调用这个入口；CLI 不可用也不会静默切换到原生 API。

### Repository 配置

`~/.repokit/repositories/<repo-key>/config.json` 保存所有 clone 共用的 repository 策略。
例如 `{"worktree":{"max_count":10,"max_prepared":3,"outputs":["dist"]}}`。缺失时使用默认策略，
无效配置返回错误；配置、创建来源和登记不随过期观测清理。Git branch/commit/rebase 恢复记录仍归
Git 操作组件，存放在 common Git directory。完整布局见上文[目录与身份](#目录与身份)。

## 清理与回收

`repocli clean` 的主要目标是 Home 及其登记指向的 worktree 目录，按各领域生命周期回收资源。repository 状态默认闲置阈值为 **30 天**；
worktree 沿用现有保留策略。命令扫描 RepoKit home 中已登记的仓库；不扫描磁盘寻找源码仓库，也不删除 primary checkout 或 branch refs。

```sh
repocli clean --dry-run --json
repocli clean --inactive-days 30 --json
repocli clean --repo /path/to/repository --dry-run --json
repocli clean --home /path/to/repokit-home --inactive-days 90 --json
```

不传 `--repo` 时覆盖所选 home 的已登记仓库；显式传入则只处理该 repository 身份，包含其记录的多个
clone。home 沿用显式参数、`REPOKIT_HOME`、`~/.repokit` 的优先级。`--inactive-days` 范围为 1–36500；
`--timeout` 默认 2 分钟，是**单个 worktree 的累计处理预算**，包括该 worktree 的检查、PR 核验、目录大小统计、
删除或依赖释放。处理其他 worktree 的时间不计入其预算。某个 worktree 超时会报告失败并继续处理其他 worktree，
不会让该仓所有 worktree 共用一个期限。dry-run 使用同样的预算。repo 级缓存回收不设时间预算；
用户取消或调用方 context 到期仍会终止整轮，并保留已完成结果。
Go `CleanOptions.WorktreeTimeout` 与 `WorktreeCleanOptions.Timeout` 为 0 时仅受调用方 context 限制。

### 同一套 worktree 清理能力

两个 CLI 入口统一调用 service 层的 `state.CleanWorktrees`，由它加载仓库策略并完成 merged 核验、
创建时间排序、数量保留、删除保护和依赖环境回收。`clean` 额外负责遍历已登记的 repository/clone，
并在 worktree 维护后检查已 merged PR 的评论缓存、独立判断是否回收过期 repository 状态；worktree 保留或维护失败不阻止缓存过期，
错误仍进入清理报告。不通过子进程再次运行 CLI。

| 入口 | 范围 | 保留规则 |
|---|---|---|
| `clean` | 所选 home 已登记的仓库；可用 `--repo` 限定 | 各 clone 的既有 worktree 策略；repository 状态另按访问时间过期 |
| `worktree clean` | 指定 clone | 同一 worktree 策略；支持本次运行的数量覆盖 |

worktree 数量沿用 `~/.repokit/repositories/<repo-key>/config.json` 的 `worktree.max_count` 和 `worktree.max_prepared`，
默认 **10 个 linked checkout、3 个 prepared checkout**。先删除实时核验已 merged 的候选，再按创建时间
回收超额 checkout 和依赖环境。未知创建时间排最老；30 天访问阈值不替代数量策略，也不导致限额内的
普通 worktree 被全部删除。最新 worktree 的保留窗口为并行开发 session 留出缓冲；repocli 不探测 Harness session，
已 merged 的 worktree 不再承接后续开发。完整规则见 [worktree retention](worktrees.md)。

两个入口共用 [RepoKit 用户配置](#配置)、Forge 连接与实时 PR 查询能力；未配置凭据时报告 PR 状态不可用，仍可执行数量回收。
PR 核验失败的 checkout 本轮保留。`--provider`、`--api-host`、`--target-repository` 使用同一配置语义。

primary、当前工作目录所属 checkout、detached、Git lock、进行中的 Git 操作、嵌套 checkout 继续保护。
一旦满足 merged 或数量回收条件，整体删除 checkout，包括未提交改动、未跟踪和 ignored 文件；不归档本地内容。
本地 branch refs 和提交保留，PR 合并后的新增提交不阻止回收。依赖环境检查仅用于保留 checkout 时的局部释放，不阻止整体删除。
包含当前 RepoKit home 的 checkout 不可回收。操作前重新读取 Git 身份与保护条件；通过检查后用单次 `git worktree remove --force` 整体回收目录与
注册信息，包含其中初始化的 submodule 副本。清理不逐个管理 gitlink，也不为这些副本建立归档；
不使用可绕过显式 worktree 锁的双重 `--force`。

### PR 评论与访问事实

Home 中已有评论集合可以通过统一 Forge 配置实时查询 merged 状态，不依赖本地 checkout。
确认 merged 后回收整个评论集合；查询错误进入部分失败报告，不据此推断 merged。
数量预算与 repository 闲置回收独立生效，缓存清理不删除远端评论。

明确访问只更新所选对象及其所属集合、PR、clone、repository 的 access.json；不续期兄弟对象。
Comment 的访问同时更新 comments/access.json 和所属 PR。访问时间与 observedAt 分开，后台刷新、
clean、dry-run、stats 和历史查询不续期。缺失访问时间按冷数据排序，损坏记录仍是错误。
外部编辑器、直接 Git 和终端 cd 不提供 RepoKit 访问事实；实际删除仍核验 Git 保护条件。

登记保留位置，不因缓存闲置而丢失。后续 clean 继续定位 clone 并核验 Git；路径消失才回收登记。
配置、创建来源与 Validation 的独立保留规则不受 repository 缓存预算影响。维护不删除真实 clone、
branch refs、远端对象、共享包缓存或其它 Home。显式 history-dir 指向的外部目录不在全局 clean 范围内。

### 输出与空间

结果遵循 [CLI envelope](cli-output.md)：`data.actions` 列出 repository、动作、路径、状态、原因与可用的 `lastAccessAt`，
`data.errors` 保留失败；text 模式同样把各条错误写到 stderr。部分失败返回非零和 `outcome: partial`，
成功项目不会被抹掉。`skipped` 保留动作本身不产生错误；跳过资源显示的 0 字节表示未计入回收估算，
不表示该目录为空。
`remove_repository_cache` 回收指定缓存文件；`remove_pr`、`remove_comment` 等动作标明领域。
配置、登记、创建来源和未知内容保留，不以删除 repository 整目录来实现缓存预算。
`data.counts` 报告对应 repository / clone 的前后数量、max_count 与 over_budget。
`planned` 表示预览候选，`completed` 表示已完成；跳过和失败的资源不计入回收估算。

`estimatedBytes` 是不跟随 symlink 的普通文件逻辑大小之和；dry-run 汇总预计回收动作，执行结果汇总
已完成动作。它不是文件系统实际释放块数：硬链接、稀疏文件、压缩与 snapshot 都会影响物理空间。
失败时不声称释放全部容量。`cost.wallSeconds` 记录命令耗时，动作日志包含路径、原因与估算字节数。

## 资源生命周期

### 概念与关系

| 概念 | 身份与关系 |
|---|---|
| Repository | Forge + repository path；拥有 PR，关联多个本地 Clone |
| Clone | common Git directory；拥有本地 Branch、对象库与 Checkout |
| Checkout / Worktree | Clone 下的工作目录，可检出 Branch 或 detached；Worktree 为 linked Checkout，不重复登记、计数 |
| Branch | 本地为 Clone + ref，远端为 Repository + ref；与 Checkout、PR 分别关联 |
| PR | 目标 Repository + number；分别引用一个 source branch 和 target branch，一个 Branch 可参与多个 PR |
| Comment | PR + CommentID；reply_to_id 引用同一 PR 内另一 Comment；resolution 保留平台事实 |
| Component / Bin | 组件引用实际依赖环境或产物；多个组件可以共用同一 Bin，按实际使用范围核验 |
| Validation Run | 一次执行身份，引用 repository、checkout、输入和输出证据 |

Branch 名称可以复用，旧 PR merged 不能证明当前 branch 已无用途。关联判断保留仓库、ref、revision
与观测来源；本地 branch 结合 remote/upstream 等事实关联远端。缓存缺失不表示远端对象消失。

### 独立 cleanup 与组合调用

每类可管理资源在所属领域提供 cleanup，接受明确的 scope、policy、dry-run 和调用方取消信号。
各语言采用自身 API 形式，共享身份、结果语义、持久化与并发契约。无需给所有资源预建通用 CRUD 基类。

| Cleanup owner | 自己完成的契约 |
|---|---|
| Repository | 仓库缓存预算、闲置回收及附属领域组合，保留有价值的配置、登记和历史 |
| PR | PR 缓存淘汰，完整回收所属评论集合，失效本仓分支的关联查询缓存 |
| Comment | 评论预算、集合完整性及缓存回收 |
| Checkout / Worktree | 删除资格、实际目录操作、位置登记和链接对账 |
| Branch | 分支缓存清理；不删除实际 Git ref |
| Clone | 核验本地位置与拥有对象，清理失效登记和辅助记录；缓存预算不自动授权删除实际 clone |
| Bin | 核验剩余使用者，释放环境或产物并更新回执 |
| Validation | Run 保留规则，成组回收证据并保留有效的共享引用 |

Go/Python/TypeScript 的 state 均提供 Repository、PR、Comment、Branch、Clone 缓存独立清理。
Go 的 state.CleanWorktrees、depot.Clean 与 repokit.CleanValidationHistory 分别管理真实 worktree、
Bin 与历史；repokit.Clean 组合 state 和 validation，repocli clean 消费此入口。PR 清理在同一 lease
内完整删除子评论集合，后续 Comment 清理跳过已删除父对象。全局入口不通过子进程调用 CLI。

结果包括已完成、跳过、失败及原因，前后数量、剩余超额、空间估算、受影响关联对象和覆盖缺口。
未知操作结果先核对事实；重试不重复已确认的副作用，部分完成后的对账失败可继续修复。

### 数量预算与访问事实

配置放在 Home 的 config.json 与 repository 的 config.json，后者覆盖前者。三语言共享解析语料。
省略继承默认值，0 表示尽量不保留可回收项，负数、null、布尔值和非整数拒绝。Repository 总预算
由 Home 配置决定；对象清理使用其 repository 覆盖策略。默认值不是容量测量结论。

```json
{"cleanup":{"repository":{"max_count":100},"pr":{"max_count":100},"comment":{"max_count":5000},"branch":{"max_count":200},"validation":{"max_count":1000}},"worktree":{"max_count":10,"max_prepared":3}}
```

| 预算 | 统计范围 | 默认 max_count |
|---|---|---:|
| Repository 缓存 | 每个 Home 有缓存内容的 repository，登记独立保留 | 100 |
| PR 缓存 | 每个 Repository 不同 PR 身份 | 100 |
| Comment 缓存 | 每个 Repository 全部根评论和回复 | 5,000 |
| Branch 缓存 | 每个 Repository，按 clone + ref 区分本地身份 | 200 |
| Linked Checkout | 每个 Clone | 10 |
| Prepared Checkout | 每个 Clone | 3 |
| Validation Run | 每个 Repository，与保留期限共同生效 | 1,000 |

max_count 是软上限，正常创建和读取不因达限而失败。先固定最新保留窗口，再处理窗口外候选；
旧候选受到保护时允许超额，不挤掉窗口内较新对象。clean 按领域资格和排序尽量收敛；正在使用、
锁定、共享依赖或无法确认状态等条件可以造成超额，报告具体原因。真实资源的使用与保护需要可核验
证据，访问时间或数量窗口不等同于识别所有外部 session。保留现有 worktree 内容处置与保护契约。

同一对象的多个 connection 观测不重复计入逻辑数量；实际文件容量另计，可后续增加 max_bytes。
Comment 按整个 PR 评论集合淘汰，计数仍按每条 Comment；最新单个集合过大时可超过正数预算。
读取仍返回完整远端结果，不能把预算裁剪后的缓存作为完整列表。Bin 按物理资源去重；Prepared
Checkout 统计带环境的 checkout，不把 Bin 数量与 checkout 数量混用。

observed_at 控制事实新鲜度，last_access_at 参与淘汰排序，保留期限由领域决定。访问记录采用上文
对象级布局，维护、后台轮询和 stats 不续期；访问子对象可续期所属集合、PR、repository，不续期兄弟对象。
缺少访问时间可以使可重建缓存排在较早候选位置，损坏、读取失败和真实资源删除资格仍须分别处理。

### 关联清理与全局调度

归属关系由 owner 组合清理；引用关系只触发对方重新判断；共享资源核验最后一个有效使用者。
最后一个 checkout 消失后仍核验 clone 是否存在，创建来源和执行历史可独立保留。

全局 clean 的协调流程由工具包拥有，repocli 负责参数和展示：

1. 发现 Home 登记，回收已失效的空锁记录，按 repository 访问时间固定超额候选。
2. 各 clone 通过现有 worktree owner 核验并回收 checkout / Bin；错误保留在报告中。
3. 在访问 lease 之外查询已有评论集合的 PR merged 状态。
4. Validation owner 独立按 TTL 与 Run 数量维护历史。
5. 按 activity → resources 获取 lease，重读访问事实；顺序清理 PR、Comment、Branch 缓存和失效登记，汇总数量与部分失败。

归属清理不重复计算子目录空间；PR 淘汰使本仓派生的 branch association 缓存失效。
仍有 scopeIds 或 nextRefreshAt 的 PR 记录受到保护，预算不能使跟踪任务消失。跨仓扫描 checkpoint
由扫描 owner 管理。没有通用引用递归删除；剩余本地位置和真实 Git 资源按各自资格重新核验。

dry-run 使用相同候选与保护规则，模拟父子删除关系，不修改资源或登记；临时 lease 和 CLI 日志仍按常规管理。
一个资源失败仍可继续独立资源。保留单 worktree 累计时间预算，repository 缓存回收无隐式本地期限；
调用方取消终止整轮并保留已完成结果。Validation 按 WAL 前缀淘汰；未完成 Run 可保留额外前缀，
但不挤掉最新 Run。TTL 独立生效；新近无引用证据暂留至 TTL，避免删除并发执行尚未发布的输入或输出。

### 布局切换

本版本直接使用上述布局，不双读、不自动迁移旧格式。升级前如需保留旧配置和创建来源，应在工具进程
停止后将 repository 的 persistent/config.json 移到 config.json，将 persistent/clones/<clone>/branches/<branch>.json
移到 clones/<clone>/branches/<branch>/creation.json。旧 history、inputs、logs 可一同移入 validation/ 保留历史。
可重建观测无需搬运，位置登记由后续明确访问或 observe 重新建立；旧布局不会作为当前事实读取。
