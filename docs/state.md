# Repository 信息与资源生命周期

`~/.repokit/repositories/` 是 RepoKit 的本地信息集散地。目的是让查询、Forge 观察、Git 操作和验证
共用 repository 身份与已有事实，并由 RepoKit 负责按需获取、更新与回收。目录可被 `ls/tree` 浏览
是附带收益；目录不声明远端全集、实时同步或当前操作的授权依据。

## 代码组织与职责

`state` 是 Repository 状态的业务组件，负责身份与关联、观测复用、checkout 生命周期、
状态发布及恢复。Git 适配层负责读取 Git 事实和执行命令，不依赖 state，也不回调 state 发布记录。
创建、移除与清理 checkout 由 state 协调 Git 操作和状态更新；上层 CLI/Forge 消费 state 的业务入口。
持久化记录是 state 的实现材料，不能通过直接改 JSON 代替 Git 操作。
访问记录、worktree 保留与 repository 状态回收分别维护；聚合清理只组合既有 worktree service。
state 不承担 Forge 连接、Git 命令执行、validation 工作流或 CLI 展示，也不作为所有持久化数据的总入口。

| 实现 | 目录与入口 |
|---|---|
| Go | `toolkit/go/state`：`LoadBranch`、`Observe`、`AddWorktree`、`RemoveWorktree`、`CleanWorktrees`；Git checkout 适配在 `internal/gitworktree` |
| Python | `toolkit/python/repokit/state`：`RepositoryState`、`default_branch`、`add_worktree`、`remove_worktree`、`clean_worktrees`；Git 适配在 `repokit/git` |
| TypeScript | `toolkit/typescript/src/state`：`RepositoryState`；Git 适配在 `src/git` |

各语言的 state 目录同时拥有 home 解析、身份编码、记录存储和 lease 协议。Forge 的 PR 查询与
观察流程复用这套存储能力，validation 继续拥有自己的执行历史格式与保留规则。
源码模块的组织不改变下面的磁盘布局和跨语言记录协议。

结构信息复用、动态观测的新鲜度、操作前实时核验分别属于 state 的读取与操作契约。
当前 branch 读取已有 read-through；checkout 结构查询仍读取 Git，尚未实现跨调用的结构信息复用。

## 目录与身份

```text
~/.repokit/
  repositories/<repo-key>/
    repository.json                     # 身份；本地登记不随缓存过期
    activity.json                       # checkout 登记位置与访问事实；不因维护续期
    persistent/
      config.json                      # repository 级配置，不随缓存过期
      clones/<clone-key>/branches/<branch-key>.json  # 已确认的分支创建来源
    remotes/<scope-key>.json             # 请求范围与远端 URL 隔离的远端分支观测
    metadata.json                       # 已观察的 repository metadata（例如 default branch）
    branches/<branch-key>/
      observation.json                  # 按 clone 保留 head、observedAt、checkout keys
      worktrees/<checkout-key> -> ../../../checkouts/<checkout-key>
      associations/<scope-key>.json     # source/targets/connection 范围内的 PR 发现
    checkouts/<checkout-key>/
      observation.json                  # path、commonDir、head、branch、primary、observedAt
      path -> /absolute/source/path
    prs/<number>/observations/<connection-key>.json  # 该连接的快照、刷新与发现状态
    history/                            # 验证 WAL，独立保留策略
    inputs/
    logs/
  scans/                                # 可能跨仓的 Forge discovery checkpoint
  forge-http/                           # endpoint/account 范围的传输协调
```

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

## 资源与回收

repository 与实际被使用的 checkout 的访问事实保存在 `activity.json`，与观测时间分开。
`repocli clean` 复用 worktree 既有保留策略，并默认按 30 天闲置条件回收 repository 缓存；本地身份和 checkout 登记保留，确保后续清理仍可发现资源。访问来源、并发边界、
缺失时间策略和预览契约见 [闲置清理](cleanup.md)。

Go `state.Observe`、Python/TS `RepositoryState.observe` 记录本地事实；Git 创建/移除已成功但状态发布失败时，保留成功的 Git 结果并附 metadata diagnostic，避免误重试。`reconcile` 另外回收
当前 clone 中已不再注册、且源路径确实不存在的 checkout 记录及其 branch 链接。它只删除明确拥有的
状态文件和 symlink，不遍历或删除 symlink 指向的源码。未知文件、未知 checkout 路径或读取失败不会
变成删除授权。仍存在的非注册源码目录保留其旧记录，供后续核对。

删除 branch 的投影只移除当前 clone 的信息；其他 clone、PR 关联和执行历史独立保留。
没有 clone 信息且没有其他资产的 branch 空投影可回收。执行历史按自己的 retention 清理，不能因为
checkout 消失连带删除历史。仓库根保留信息不会触发全量 Forge 扫描或后台实时同步。

Python/Go worktree 创建、移除和清理接受显式 state directory，并在成功操作后观察/对账。
CLI 的 `worktree clean` 和 PR context 成功写入后的 maintenance 使用同一入口。Python/TS PR context
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

Go、Python、TypeScript 与 CLI 共用 RepoKit home：显式 `home` / `Home` / `--home` →
`REPOKIT_HOME` → `~/.repokit`。目录解析由工具包统一提供，解析本身不创建文件。
省略 home 表示使用默认目录，不表示禁用持久化；实时只读、刷新、写入和回收由各 API 契约决定。
CLI 日志、PR、topology、统计与验证默认历史根都从同一 home 派生。
`validate --history-dir` 仍独立覆盖历史根路径，不隐式改变其他信息的位置。

## 持久化边界

记录通过临时文件、fsync 和 rename 发布；权限为目录 0700、记录 0600。topology 按 repository
持有共享 lease，PR/关联按记录持有 lease；Go/Python/TypeScript 使用兼容的 `.lock` 目录协议。
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

`persistent/config.json` 保存 repository 级 worktree 策略，多个 clone 共用。Go
`SaveWorktreePolicy` / `LoadWorktreePolicy` 和 Python `load_policy` 使用同一路径，缺失时使用默认策略。
分支创建来源按 common Git directory 隔离，记录 branch、baseRef、baseCommit、remote 与 createdAt。
`state.CreateBranch` 和 worktree ensure 在确认新分支后发布记录；记录失败保留已确认的 Git 效果并返回诊断。
Go `LoadBranchCreation`、Python `branch_creation` 与 CLI `git branch show` 读取来源；未经 RepoKit
记录的分支来源为未知，不从当前 ancestry 猜测。分支在外部删除重建后，旧记录仅是最后一次已确认创建的历史，
不能证明当前分支仍源于该基线，也不能覆盖调用方的任务基线或 PR target。

过期缓存清理保留 `persistent/`、仓库身份和 checkout 登记。Git branch/commit/rebase 的恢复记录继续
由 Git 操作组件保存在 common Git directory；它们不随观测缓存清理。Validation 历史仍由 validation
拥有格式与保留机制，不能因为物理上同处 RepoKit home 就纳入 state 的业务模型。

Python `checkout_view` 组装当前 checkout、分支、默认分支、拓扑与创建来源；`remote_branches` 按
请求的 refs、fetch URL 与时效复用远端结果。远端查询失败返回错误，不能当成空集合；这类读模型不构成
跨系统一致快照，任何删除、发布或覆盖操作仍必须实时核验。
