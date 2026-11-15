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
访问记录、worktree 保留与 repository 状态回收分别维护；聚合清理只组合既有 worktree service。
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

## 目录与身份

目录按对象身份与归属组织；某一层级的信息直接放在该对象目录下。目录已经定位对象时，
不为统一分类机械增加 `observations/` 等中间层，也不再单设文件重复记录同一身份映射。
新增或调整布局时按这一原则收敛，文件名明确表达记录的含义或访问范围。

文件名、存在性和内容都是信息载体：单一标记可用空文件表达，简单值可用短文本，
需要整体读写的结构化信息使用 JSON；不强制把所有信息包装成 JSON 对象。路径中的业务标识
复用统一的安全编码规则。同一事实只保留一种权威表达，需要一起变化的字段放在同一记录中原子更新。
锁也是这种表达方式的例子；本项目现有的 `.lock` 空目录 lease 协议见下文“持久化边界”，
不因文件表达原则改成另一套锁协议。

Home 统一解析优先级为显式 `home` / `Home` / `--home` → `REPOKIT_HOME` → `~/.repokit`。
解析本身不创建文件；省略 Home 使用默认目录，不表示禁用持久化。空字符串视为省略，纯空白路径拒绝；
路径做绝对化和词法规范化，不解析 symlink。CLI、配置、Forge、topology 和验证默认历史均从此根派生。
`--history-dir` 只覆盖验证历史根，不改变其它信息的位置。

落盘布局如下：

```text
~/.repokit/
  config.json                           # 用户维护的 Forge 访问配置
  logs/
    YYYY-MM-DD.log                      # CLI 执行日志
    diff-YYYY-MM-DD.jsonl                # diff timeline；日志独立保留策略
  locks/activity/<repo-key>.lock/        # repository 外的访问/回收 lease
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
    prs/<number>/
      <connection-key>.json              # 该连接的 PR 快照、刷新与发现状态
      comments.lock/                     # 仅发布评论记录或清理期间存在
      comments/<comment-key>/
        <connection-key>.json            # 单条评论的最近观测
    history/                            # 验证 WAL，独立保留策略
    inputs/
    logs/
  scans/<scope-key>.json                 # 可能跨仓的 Forge discovery checkpoint
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

## 本地操作与恢复

访问与回收统一见下文[清理与回收](#清理与回收)。Comment ID 与平台回复结构从 Forge 数据重建，
不能依赖仅存在本地的映射；缓存删除后显式读取历史 PR 会重新查询 Forge。merged 不表示正文与
resolution 永远不变，也不要求永久保留评论缓存。

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

## 持久化边界

记录通过临时文件、fsync 和 rename 发布；权限为目录 0700、记录 0600。topology 按 repository
持有共享 lease，PR/关联按记录持有 lease，Comment 以 PR 下 `comments` 为发布/清理锁范围。
Go/Python/TypeScript 使用兼容的 `.lock` 空目录协议：等待最多 30 秒、每 10 秒续期、30 秒失效。
失去 lease 或取消后不得发布记录；同一 Home 的进程使用相同锁参数。Validation WAL 使用自己的 OS 文件锁，
其执行语义与保留规则见 [validation](validation.md#执行历史)。
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

`~/.repokit/repositories/<repo-key>/persistent/config.json` 保存所有 clone 共用的 repository 策略。
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

worktree 数量沿用 `~/.repokit/repositories/<repo-key>/persistent/config.json` 的 `worktree.max_count` 和 `worktree.max_prepared`，
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

### 已 merged PR 的评论缓存

从 Home 已有 `prs/<number>/comments/` 出发，通过统一配置直接查询 Forge，不要求本地 checkout
仍存在，也不扫描远端 PR 全集。只有实时确认 merged 才回收 comments；404、权限或网络错误保留
缓存并进入部分失败报告，open/closed 保留。CLI 使用 Forge client 的默认 10 秒单请求超时；
不将 worktree 的 `--timeout` 当作整仓扫描预算，调用方取消仍终止维护。

发布与删除共用 PR 下 `comments.lock/`，持有中的 writer 以 `state_locked` 保留。删除不跟随
symlink，不删除远端评论或 PR 观测。dry-run 同样核验，报告 `remove_pr_comments` 的 `planned` 动作；
实际删除成功标为 `completed`。后续历史读取重新构建缓存与回复结构。已访问过期的仓库直接进入整仓缓存回收，不再逐 PR 核验或重复估算空间。
访问过期回收独立于 PR 终态；失败不意味着 merged，也不延长缓存保留期。

### 访问事实

每个 `repositories/<key>/activity.json` 保存 schema 1 的 repository `lastAccessAt`，以及按 checkout
路径摘要索引的 `checkouts`：`path`、`commonDir` 和 `lastAccessAt`。时间单位为 Unix 秒。
访问记录不等于 topology/PR 的 `observedAt`，也不使用操作系统 atime、Git commit 时间或源码 mtime 推断访问。

- 面向仓库的 CLI 命令记录所选 checkout；原生 Go `RecordAccess`、Python/TS 的 RepositoryState
  branch/observe、checkout 创建，以及显式 PR 缓存查询记录对应使用。纯原生分析 API 仍只读，调用方
  若以其它方式使用 checkout，应显式记录访问或使用 Git worktree lock 固定它。
- 读取一个 checkout 不续期同仓其他 checkout。扫描库存与维护不是访问；Go 清理上下文、Python
  `observe(record_use=False)`、TS `observe({recordUse:false})` 不更新访问时间。
- clean/dry-run、stats、help、version、upgrade 不更新访问。验证历史的统计读取不续期；正常验证执行
  与复用查询记录使用。失败尝试也可能说明用户正在使用该 checkout。
- 缺少 repository 访问记录或访问时间时按很老处理，进入状态过期候选，动作原因标为 `access_unknown`。
  位置从现有 checkout observation 获取，再用 Git 实时核验；不拿 observation 时间冒充访问时间。
  未来访问时间仍保留；损坏记录、读取失败或身份不匹配属于错误，不当作时间未知。

外部编辑器、终端 `cd`、直接 Git 与任意第三方 SDK 不提供 RepoKit 访问事实。worktree 保留由现有
数量与 merged 策略决定；正在使用的干净 worktree 可通过 `git worktree lock` 固定。

### 状态回收与并发

访问更新与闲置回收共用 `home/locks/activity/<repository-key>.lock` 的跨语言 lease；锁位于待回收
目录之外。清理先在短 lease 内读取登记位置，释放后执行 Git/PR 维护，最后重新取得 lease 并读取
最新访问记录再决定状态删除。远端查询不占 activity lease，也不继承其 30 秒发布期限；查询期间发生的
前台访问仍能及时续期。删除前再次检查状态写入锁。Git 资源操作另外使用现有 repository lease；checkout
创建先释放 Git lease 再发布访问/状态，避免锁顺序倒置。维护成功后的观察发布不续期。

repository 状态仅按访问过期与状态写入保护决定删除，不依赖 linked checkout 数量、PR 查询结果或
worktree 删除结果。状态写入 lease 以 `state_locked` 保留；缓存文件的 mtime 不续期访问，也不阻止
过期回收。登记与缓存有不同生命周期：

- 有本地 checkout 登记时，保留 `repository.json` 身份和 `activity.json` 中的位置、common Git directory
  及原访问时间，只删除其余可重建状态，包括 topology、PR 观察与验证历史。仅存在于 topology 的位置
  先并入登记，再删除缓存；未知访问时间仍为 0，不因维护续期。
- 后续全局 clean 继续从登记定位 clone，并实时核验 Git/PR。上次因锁或 PR 查询失败而保留的
  worktree，在条件解除后仍可回收，无需先访问仓库。登记是发现线索，不是删除授权；失效路径跳过，
  身份变化与读取错误仍报告。
- 没有本地 checkout 登记的过期 repository 状态可整目录删除。轻量本地登记不按缓存年龄过期；
  被保留的 primary checkout、worktree 和 branch refs 不受缓存回收影响。

日志、共享包缓存、其它 home，以及 `--history-dir` 指向的外部目录不在范围内。

目录删除限定在所选 home 的 repositories 根内；不跟随其中的源码 symlink。数据回收成功不代表远端 PR
消失，也不删除源码仓库。`--dry-run` 不删除资源、不更新访问或 topology；临时 lease 和 CLI 日志仍按常规管理。

### 输出与空间

结果遵循 [CLI envelope](cli-output.md)：`data.actions` 列出 repository、动作、路径、状态、原因与可用的 `lastAccessAt`，
`data.errors` 保留失败；text 模式同样把各条错误写到 stderr。部分失败返回非零和 `outcome: partial`，
成功项目不会被抹掉。`skipped` 保留动作本身不产生错误；跳过资源显示的 0 字节表示未计入回收估算，
不表示该目录为空。
`remove_repository_cache` 表示回收缓存并保留登记，`remove_repository_state` 表示无本地登记时整目录回收；
只剩登记时以 `registration_only` 跳过，保留文件不计入回收字节数。
`planned` 表示预览候选，`completed` 表示已完成；跳过和失败的资源不计入回收估算。

`estimatedBytes` 是不跟随 symlink 的普通文件逻辑大小之和；dry-run 汇总预计回收动作，执行结果汇总
已完成动作。它不是文件系统实际释放块数：硬链接、稀疏文件、压缩与 snapshot 都会影响物理空间。
失败时不声称释放全部容量。`cost.wallSeconds` 记录命令耗时，动作日志包含路径、原因与估算字节数。
