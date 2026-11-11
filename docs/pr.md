# PullRequest

PullRequest（PR，GitLab 中为 MR）由目标 Repository 与仓内编号确定，身份复用 harness-common；
fork 源仓、分支、revision 与生命周期状态不参与身份。状态查询、平台读写与持续观察属于 RepoKit，
通知、业务基线、提交门禁和合并授权属于调用方。

## 生命周期与能力边界

创建 PR 后可观察到 open，随后可变为 closed 或 merged；closed 可以重新打开，merged 视为生命周期终态。
生命周期、代码冲突和平台合并条件是独立事实；读取失败、权限变化或列表缺席不能推导终态。
标题、评论与评审仍可变化，不因 merged 而冻结。

TypeScript `PullRequestObserver.getPullRequest(key)` 提供生命周期状态查询，内部负责本地状态与更新。
`ForgeReader` 提供基本事实、评论、评审与扫描的直接读取；Python `repokit.forge` 提供 GitHub/GitLab 读写。
原始平台读取、持续观察及业务消费分别拥有自己的完成边界，保存观察不代表调用方已消费它。

## Python Forge 读写与请求协调

Forge 的合并状态表示平台观察；哪些状态需要提醒、等待或阻断开发流程，由消费方决定。

Forge 构造参数显式指定 host、repository 与 token。创建/更新使用中立的标题、正文和分支参数；
合并要求 expected_sha，防止把检查之后新增的提交一并合入。客户端不替调用方决定是否允许合并。
GitHub/GitLab 的评论线程、引用与 resolution 由 adapter 解释，业务 finding 和 verdict 不进入库模型。

Forge 客户端复用有超时的 HTTP 传输，不自动重试写操作。Python Forge 多页查询只在所有页面
均符合列表协议时返回，格式错误或读取失败不返回部分清单。

### Forge 观察与请求协调

`open_pull_requests()` 完整分页读取当前仓库发起、且目标也在当前仓库的开放 PR/MR，按创建时间倒序。
与默认 `prs_for_branch()` 一样排除其它源仓的同名分支。分页失败抛错，不返回部分清单；一个 PR
不再出现在开放列表中，并不证明它已经合并。调用方通过 `get()` 确认变化，并决定历史补查频率。

GitHubForge / GitLabForge 可显式接收 `home`。同一目录、host 和凭据的请求跨线程/进程串行，
锁等待受 operation deadline 约束。状态只保存凭据摘要；目录和缓存文件限制为当前用户访问。
不同凭据分别隔离；同一账号的不同 token 可能共享平台额度，当前不会推断它们属于同一主体。
目录由调用方选择，库不依赖应用配置或应用状态目录。未提供目录时不持久化缓存或共享冷却。

GET 条件缓存按完整 URL、参数及请求头区分，每次读取仍向服务端验证 ETag。304 复用对应响应体，
所有分页仍逐页验证，因此旧页评论编辑可见。POST/PUT/PATCH 不使用响应缓存；GraphQL 查询保留
既有协议。`request_counts()` 返回本客户端实际请求数和 304 数，日志记录资源和冷却时间。

`ForgeRateLimited` 区别于鉴权失败，携带 resource、retry_at 和 secondary。HTTP 403/429 及
GraphQL 200 的 RATE_LIMITED 错误均可分类；主配额按 resource 分开冷却，二级限制阻止同一凭据的
全部请求。Retry-After / reset 用于决定恢复时间，连续二级限制指数退避；库不会睡到恢复时间或自动
重试写操作。调用方保留上次成功观察和它的时间，另行报告刷新失败，不把缓存变成新的权威证据。

### 调用预算与不确定结果

Python `operation(timeout=..., cancel=...)` 为 Git/Forge 调用提供共享 deadline，嵌套 scope 不能延长外层预算；
通用调用方式见 [Git 调用预算](git.md#调用预算与不确定结果)。

Forge 写请求已发出后发生传输中断、5xx 或无法解码响应，抛出 ForgeOutcomeUnknown（ForgeError 子类），
调用方必须先核对远端状态。明确的 4xx 保留对应错误，库不自动重试。HTTP 单响应最多 16 MiB；
分页共用整个列表查询的预算，最多 10,000 项，失败不返回截断清单。调用方可用 operation 将多次
adapter 调用放在同一预算内。HTTP 取消在请求/读取边界检查，正在阻塞的 socket 受本次请求时限约束，
不是可立即打断的异步客户端。scope 不提供事务或补偿。

## Fork 与跨仓 PR

Python `Forge.repository()` 返回 common Repository，包括可空的 `forked_from` 直接父仓路径；
它限定在同一个 Forge 实例，查询错误直接传播。GitHub 使用 parent，GitLab 使用
forked_from_project；父仓与 fork 网络根仓不是同一概念。

`for_repository(path)` 选择 PR 编号、读取和更新所属的仓库。
`create(source_repository, source_branch, target_repository, target_branch, ...)` 显式接收四元组；
两个 repository 均为 common Repository，必须属于当前 Forge 实例，跨 Forge 在请求前拒绝。
客户端原有仓库作用域不覆盖显式传入的源仓或目标仓；Repository.id() 提供统一身份引用。
`prs_for_branch(branch, source_repository=...)` 在当前目标仓查询并匹配源仓，省略源仓时取当前仓。
工具包不自动选择父仓，调用方负责目标分支选择。
GitHub 分支查询在 owner:branch 服务端过滤后仍核对完整 repo；GitLab 核对 source_project_id。
列表分页完成后才返回，不能把其他 fork 的同名分支当作结果。

GitHub 创建请求发送到目标仓，指定 head owner 与 head_repo；GitLab 请求发送到源项目，明确
指定 target_project_id。平台数值 ID 留在 adapter 中，公共仓库身份仍是 Forge 与 path。
PullRequest 的 source_repository / target_repository 保存可确认的路径，缺失保留为空。

## TypeScript Forge 读取

`createForgeReader` 显式接收 `{forge, host, provider, token}`。forge 是 common 身份名，host 是网络地址；
配置不含作者筛选，作者属于每次查询。库不读取应用环境变量、不选择账号、不持久化应用状态。

| 操作 | 结果与语义 |
|---|---|
| `listPullRequests(query)` | 单页 `Page<PullRequestKey>`；显式 account 或 repository scope |
| `iteratePullRequests(reader, query)` | 惰性逐项遍历，沿用单页查询；有行级 issues 时拒绝，不宣称清单完整 |
| `getPullRequest(key)` | common `PullRequestSnapshot`，仅基本事实，不请求评审 API |
| `getPullRequestReview(key)` | 独立 `PullRequestReviewObservation`；评审不可用则抛错 |
| `listPullRequestComments(key)` | 普通评论及嵌套评审回复 |
| `scanPullRequests(query)` | 有界持续扫描，返回 snapshots、检查点及覆盖状态 |

```typescript
const query = {
  scope: { kind: "account" as const, author: "octocat" },
  state: "all" as const,
  updatedAfter: "2026-10-01T00:00:00Z",
  updatedBefore: "2026-10-26T00:00:00Z",
  pageSize: 50,
};
const page = await forge.listPullRequests(query);
for (const key of page.items) {
  const snapshot = await forge.getPullRequest(key);
  // Persist common facts before committing the page continuation.
}
// Inspect page.issues; a partial page does not establish complete coverage.
```

account scope 按显式平台 username 跨可见仓库查询，不从 token 推断作者。
repository scope 为 `{kind: "repository", repository: RepositoryKey, author?: string}`，可查询仓内所有作者。
state 支持 open / closed / merged / all（默认 all），closed 不含 merged。
查询可带 updatedAfter / updatedBefore；游标不能跨 scope、作者、状态、时间范围或页大小复用。

GitHub 使用 Search API，支持 repository、author、状态和时间窗口；单个搜索窗口超过 1000 条或
incomplete_results 时抛 ForgeCoverageError，不能把截断结果当完整清单。GitLab 使用仓库或实例的
merge_requests API，作者通过 author_username 过滤。仓库查询直接使用给定 RepositoryKey；账号查询从
web_url 确认目标仓，支持 `/-/merge_requests/:iid` 和 `/merge_requests/:iid`，校验 origin、路径和编号。
单行异常保留其余有效 keys，并返回结构化 issues；全页传输或 envelope 错误直接抛出。
按页读取仍可能因并发变动漏项或重复，顺序 iterator 不会改变这个属性。

基本事实与源/目标分支来自 common，身份为目标 Repository + number；使用 pullRequestId / repositoryId
计算身份，结果可 JSON 或 structuredClone 往返。GitHub 保留已知 fork 源仓；GitLab 源项目与目标项目
相同时保留源仓，fork 源项目通过共享客户端解析路径；已删除的源仓保持未知。getPullRequest 不附评审时间或评审错误。

无冲突、平台 ready 与已 merged 是三类独立事实。GitHub 基本 REST 读取给出 conflictState 和已知 draft，
其它 mergeReadiness 来自显式的 getPullRequestReview；GitLab 基本 MR 的 detailed_merge_status 可提供
readiness。评审结果包括 key、reviewThreadState、reviewActivityFingerprint、observedAt，以及可用的 readiness。
指纹覆盖评审线程解决状态和完整评论活动，编辑、删除也会改变它；失败不能生成可信指纹。
评论排除 GitLab system notes。评审/comment/discussion 最多 20 页，超限抛错。

HTTP 使用 Node 原生 fetch 及其连接复用，可注入 fetch；单请求（含正文）默认超时 10 秒，响应上限 4 MiB，
由 timeoutMs / maxResponseBytes 调整。评论查询最多并行两个请求。401/403（排除限流）为 ForgeAuthError，
404 为 ForgeNotFound，传输错误为 ForgeError。ForgeRateLimitError 携带 retryAfterMs，同实例传输在冷却期
拒绝新请求，不自动重试。基础事实不会因“读取时顺带查询评审”而丢失；两类显式请求仍共享平台凭据和冷却。

### 持续扫描与覆盖

`scanPullRequests({scope, cursor?, initialSince?, tracked?})` 使用上面的通用查询和基本事实读取。
initialSince 仅决定首次历史回看（默认七天），首次另枚举可见 open 对象。tracked 是消费方持有的 keys，
独立复查用于确认其 merged / closed；清单缺席永远不构成终态证据。

扫描 cursor 是版本化检查点，包含窗口、缓冲页位置、失败对象重试队列与覆盖缺口；它与单页 cursor 不可互换。
枚举进度 enumeratedThrough 与无缺口的 scannedThrough 分开维护，都是私有字段。
当前 cursor 为版本 3，不迁移旧 cursor；tracked 每次以请求中的当前集合为准，刷新进度独立于发现轮次。
调用方只保存完整 cursor，**可靠消费全部 items 后再提交**；同一检查点重放可重复返回事实。
账号、仓库、连接或凭据改变后要开始新扫描，恢复方式见 [0.8 迁移](migrations/forge-0.8.md)。

返回语义：

- `items`：本批成功读取的 common snapshots，可独立消费。
- `pending`：当前扫描轮次还有未执行工作，适合继续请求下一批；false 时可回到正常轮询间隔。
- `complete`：当前没有待执行工作，也没有保留的覆盖缺口。`pending=false, complete=false` 表示本轮已处理完，但仍存在错误待后续重试。
- `issues`：结构化 stage、kind、message，以及已知 key、scope、window、retryAfterMs。保留未恢复错误，不把失败当空清单。

失败详情读取会推进当前页位置，并将 key 放入持久重试队列；后续对象可继续返回。每批轮转重试最多 2 个，
最多发现 4 个窗口、读取 5 个新对象及 5 个 tracked 对象。重试、发现、tracked 各有独立 20 秒总期限，
单请求期限仍生效。队列最多 500 个失败对象，满时暂停新详情的接纳并报告覆盖问题，直到重试腾出容量；
已有失败仍重试，不静默丢弃对象。tracked 刷新不受该容量限制，失败按当前 tracked 集合独立保留，
移除的 tracked 对象不再刷新。低层 scanner 只执行一批，调用方根据 issues 记录日志；
[PullRequestObserver](#持续观察) 可进一步拥有持久化、退避和显式轮询循环。

为减少可变页号的遗漏，扫描每个时间窗口只取第一页：无法一次覆盖时按更新时间二分，边界重叠。
GitHub 搜索上限触发窗口细分。最小一秒窗口仍满 50 条或有后续页时记录 coverage gap；
不在该窗口内靠翻页宣称完整。失败窗口保留、其它窗口继续；同一状态的缺口合并为保守的时间区间，
下轮可能重读其中成功区域，以避免无限积累失败窗口。正常增量保留一分钟重叠。

这是快照扫描，不是事件日志；无法保证捕获轮询间的中间变化、已删除/失权对象或超出重叠范围的迟到搜索索引。
RepoKit 管请求、扫描覆盖，以及显式启用的 PR 观察、持久化和轮询。消费方拥有关注范围、业务基线对比、
业务去重和 Input 接纳。观察状态的保存不代表业务消费已完成。

### 仓库范围的工作流契约

`RepositoryForge` 是绑定单仓库的读写接口契约，参数中的 number 均属于绑定的仓库；TS 目前不提供它的原生写实现。
ForgeReader 是独立的跨仓只读客户端，不能互相替代。
`ForgeComment` 表达内容事实；`RepositoryForgeComment` 在此基础上附可选 `target: ForgeCommentTarget`。
replyRef / resolveRef 是写 adapter 提供的不透明句柄，缺失表示没有该能力；调用方不从普通 comment.id 推导它们。
评论 resolution 可为 unsupported / unresolved / resolved，缺失表示未观察。

升级映射与旧检查点处理见 [TypeScript 0.8 / Python 0.10 迁移](migrations/forge-0.8.md)。

### 外部 ForgeReader

提供方可在独立包实现 `ForgeReader`，provider 是开放字符串；内置 `createForgeReader` 仍只接受 GitHub/GitLab 配置。
外部 reader 用 `createPullRequestScanner(reader, { connection })` 实现 `scanPullRequests`，复用覆盖、游标和重试逻辑。
`connection` 必须绑定凭证和配置修订；它只进入不透明游标，不放明文凭证。提供方负责请求超时和响应上限，
可用 `withinDeadline` 给整个扫描批次加总时限。列表必须精确执行 scope、state 和时间边界，剩余页必须返回 nextCursor，不能以截断结果声明完整。

`ForgeHttpClient` 是内置和外部 Forge 的共享有界 JSON 传输，提供请求超时、响应大小限制、
调用间隔、429 冷却、聚合 deadline、隔离的 withSignal 取消和 close。提供方适配 HTTP SDK 的 request 回调，
认证和业务错误包留在提供方；业务包返回限流时调用 rateLimit 更新同一个冷却预算。
ForgeReader 可实现 withSignal/close；消费方在调用边界传递取消，不把 AbortSignal 放进 JSON 业务查询。

## 持续观察

TypeScript `PullRequestObserver` 对外提供 `getPullRequest(key)`，返回 PR 身份、生命周期状态和观察时间。
业务调用方不指定缓存来源、刷新步骤或 tracked 集合。RepoKit 负责本地复用、远端读取、持久化、
周期更新、失败恢复和 merged 终态收敛。ForgeReader 保留原始平台读取能力，评论与评审继续独立查询。

### 使用与生命周期

```typescript
import { PullRequestObserver, createForgeReader } from "@compforge/repokit";
import { homedir } from "node:os";
import { join } from "node:path";

const reader = createForgeReader(config);
const prs = new PullRequestObserver(reader, {
  home: join(homedir(), ".repokit"),
  connection: "github-personal-access-v1",
});
try {
  const status = await prs.getPullRequest({
    repository: { forge: { name: "github.com" }, path: "org/repo" }, number: 107,
  });
  console.log(status.state, status.observedAt);
} finally {
  await prs.close();
  reader.close?.();
}
```

TypeScript observer 默认启动维护循环；短进程设置 `background: false` 后调用 `maintain()`。
Python observer 默认由宿主调用 `maintain()`；常驻宿主可调用 `start()`，退出时调用 `close()`。一次 get 自动把 PR 纳入后续维护；
重启后恢复同一访问身份的本地记录。工具包要求显式 home；命令行消费方可使用上面的默认目录。
Python 和 TypeScript 提供原生持续观察，Go CLI 不提供 Forge adapter。
可配置 scopes 自动发现仓库或账号的 PR；initialSince 只控制首次历史回看，首次另枚举开放对象。
查询接受 AbortSignal；close 取消内部循环及协作式远端请求，不关闭调用方提供的共享 reader。

成功查询只返回满足新鲜度策略的生命周期事实。不能取得当前非终态状态时抛出
`PullRequestStatusUnavailable`，不会把旧观察当成最新结果。最近成功 snapshot 仍保存在本地。
`health` 给宿主提供后台轮询的最近完成时间、结构化 issues 和持久化错误，便于记录日志与告警。
周期刷新失败不使未处理的 Promise rejection 逃逸；下一轮继续恢复。持久化失败必须报告，不能当作日志写入失败忽略。

### 状态与目录

```text
<home>/
├── repositories/
│   └── <repository-key>/
│       ├── repository.json
│       └── prs/<number>/
│           ├── observation.json
│           └── observation.json.lock/  # 仅持有刷新锁期间存在
└── scans/<scope-key>.json
```

repository-key 为 Forge 名称与完整目标仓库路径的摘要，repository.json 保留原始身份；
PR 身份为目标 Repository + number，与 checkout/worktree 无关。每个 PR 一个目录；空目录不表示观察成功。
observation.json 保存版本、访问配置摘要、最近成功 snapshot/observedAt、最近尝试时间和错误、
下次刷新时间、连续失败次数与发现范围关联。merged 事实保留在同一记录，不维护另一份终态标记文件。
scans 仅在启用范围发现后创建，保存绑定访问身份与查询范围的 cursor；跨仓库账号查询不归某个 Repository。

connection 是调用方提供的访问身份/配置修订号，不是凭据。更换凭据或权限后必须更换该值；
落盘只保存摘要。访问身份不匹配时不复用观察，一个 PR 的记录按最后访问身份替换；本版本不保留多账号缓存副本。
目录 0700、状态文件 0600；状态不参与 logs 的定期清理。存储根用于可信的本地私有状态。

### 刷新与失败恢复

默认 open 每 60 秒更新，closed 每 600 秒更新，因其可能重新打开；查询新鲜度默认 60 秒。
merged 是生命周期终态：可靠保存后停止常规状态刷新；其 observedAt 不会伪造更新。
标题、评论、评审等可变字段不受终态规则保证，使用 ForgeReader 的对应读取能力。
读取失败、404、权限错误和列表缺席均不推导终态，也不抹掉之前的成功事实。

失败按对象退避，默认 30 秒起、指数增长至一小时；尊重远端提供的更长 retryAfterMs。
后台每轮最多处理 5 个到期对象，按到期时间轮转；刷新失败持久化在该 PR 的记录中。
发现使用既有 scanner 的窗口、覆盖与有界补读能力，失败补读最多 500 个；满载只暂停发现详情的接纳，
并保留缓冲页位置，不阻断已知 PR 的刷新。连接级限流继续由共享 HTTP 客户端协调。
低层 scanner 使用 cursor v3，旧格式直接拒绝；低层 tracked 每次以当前传入集合为准，移除即退出刷新。

单请求默认 20 秒超时；外部 reader 应自行提供有界 I/O，并可通过 withSignal 协作取消。
等待锁最多 30 秒，锁每 10 秒续期、30 秒失效；统一使用 proper-lockfile 的目录锁与崩溃恢复机制，
新增该依赖是为跨进程协调，避免自建文件锁协议。相同状态目录的所有进程必须使用一致锁参数。
进程内或跨进程的查询均在锁内重新检查状态；先取得锁的刷新保存后，后续查询可直接复用。

状态通过临时文件、fsync、原子 rename 和目录 fsync 保存。失去锁或取消后不提交新状态。
先逐个保存 PR 观察，再保存扫描检查点；中断允许重复读取，不允许跳过未保存事实。
持久化完成只证明观察已保存，不表示任何调用方已完成业务消费。通知、业务基线和门禁仍由调用方拥有。

### 验证

`toolkit/typescript/tests/forge-observation.test.mjs` 使用隔离目录和真实子进程验证缓存重启、
merged 终态、closed 重开、失败退避、未知状态、并发合并、保存失败与检查点顺序、
饱和补读队列下的健康刷新和恢复。cooperative reader 用例验证调用方取消、observer 关闭与请求超时
均把取消信号传入底层详情读取，后台列表扫描在关闭时终止且不保存检查点。
既有 ForgeReader 的读取、限流与扫描契约用例继续执行。


## 本地 Branch 与 Checkout 关联

PR 身份是目标 Repository + number。关联查询显式接收本地 checkout、源 Repository 和目标
Repositories；相同分支名在不同 fork 中不能互相替代。一个 branch 可以对应多个历史或当前 PR，
也可以没有 checkout。worktree 是 branch 的一种 checkout；detached HEAD 不产生 branch 关联。

Python `PullRequestContext(observer, checkout, source=..., targets=(...))` 与 TypeScript
`new PullRequestContext(reader, {checkout, source, targets, home, connection})` 提供：

| Python / TypeScript | 查询 |
|---|---|
| `get_branch` / `getBranch` | 本地分支的候选 PR、选中结果、歧义和查询错误 |
| `get_checkout` / `getCheckout` | 当前 checkout 的分支关联；detached 返回空 |
| `local_branches` / `localBranches` | 全部分支的逐项视图，未完成发现明确返回 pending |
| `for_pull_request` / `forPullRequest` | PR 对应的本地 branch 与可选 checkouts |
| `verify` | 副作用前重新核验关联及 branch/HEAD/checkout 身份 |

存在唯一 open 候选时选中该 PR；否则以 Git ancestry 筛选已完成候选。多个匹配保留歧义，
读取失败保留可用候选但不返回可用于决策的 selected。目标仓库选择、明确任务绑定以及清理策略
由业务调用方决定。`verify` 之后，实际修改本地资源的执行器仍必须核验即时 Git 身份。

PR 生命周期终态与分支关联具有不同的更新周期：merged 事实停止普通刷新，但分支可能被复用，
因此关联发现继续按自己的到期时间更新。冷历史发现默认每轮最多 20 个到期关联，并受整轮预算
约束；已知 PR 的 `maintain` 独立刷新，不等待全部历史发现完成。

关联记录位于 `<home>/associations/<scope-digest>.json`，身份包含 connection、源仓、branch
和目标仓列表。Git topology 只在查询时读取，不作为本地资源删除的持久化授权。
Python 与 TypeScript 共用观察和关联记录及 `.lock` 目录 lease：30 秒过期、10 秒续期、原子替换、
文件和目录 fsync。connection 是调用方提供的访问配置 revision，磁盘只存摘要，不存 token。

```python
from pathlib import Path
from harness_common import Forge, Repository
from repokit.forge import GitHubForge, PullRequestKey, PullRequestObserver

repository = Repository(Forge("github"), "org/repo")
reader = GitHubForge("github.com", "org", "repo", token)
prs = PullRequestObserver(reader, forge="github", home=Path.home() / ".repokit",
                          connection="personal-access-v1")
try:
    status = prs.get_pull_request(PullRequestKey(repository, 107))
    issues = prs.maintain()  # bounded one-shot; a long-lived host can instead call start()
finally:
    prs.close()
```


## 创建与复用

Python `repokit.forge.ensure_pull_request` 接受配置好的 Forge 客户端、源分支、标题/正文，
以及可选的目标 Repository ID 和目标分支。RepoKit 读取源仓身份、解析目标仓，并在目标仓内
按完整源仓身份查找开放 PR；调用方无需拼装平台请求或执行查询后创建逻辑。
未指定目标仓时使用当前仓，不自动选择 fork parent；显式目标必须属于同一 Forge。
未指定目标分支时复用已有 PR 的 base，仅在创建时读取目标仓默认分支。
多个匹配的开放 PR 是歧义错误，不能任意选取；指定目标分支可缩小匹配范围。

```python
from repokit.forge import ensure_pull_request

result = ensure_pull_request(forge, source_branch="feature", title="Improve repository queries",
                             target_repository="github/example/project")
print(result.pull_request.web_url, result.created)
```

返回值同时包含 PR、是否新建、源仓和目标仓。复用不修改已有标题或正文；是否追加本次提交说明
属于调用方策略，可使用目标仓客户端的 `description` / `update`。读取失败停止创建，写入结果
不确定时原样抛出 `ForgeOutcomeUnknown`，不自动重发。查询后创建不提供跨调用方的 exactly-once
保证；不确定结果恢复时先重新观察远端。创建不会隐式 commit、push 或 merge。


## 连接与观察入口

Python `ForgeConnection` / TypeScript `ForgeConnection` 接收显式 `host`、provider、token 与可选 API host，
统一构造 provider client、Repository 身份和 PR context。使用 RepoKit 用户配置时，由独立
[config 模块](configuration.md) 加载并组装连接；调用方也可直接传入凭据，目标仓始终显式选择。
host 与 API host 按小写规范化，大小写不改变身份或缓存；逻辑 Forge 身份来自 host，
API host 只决定传输地址。Python client 切换 repository 后仍保留逻辑身份与传输配置。
工具包不读取应用配置，也不推断 fork parent。缺少凭据是不可用，不能解释成没有 PR。

连接对象管理观察缓存标识与默认 `~/.repokit` 状态根目录；可显式指定隔离目录。provider、Forge 身份、
API endpoint 或 credential 改变时，连接标识改变，旧缓存不能作为新访问范围的观察。凭据不写入缓存，
也不进入连接对象的日志表示。Python HTTP 协调状态归该状态根目录的 `forge-http/`。

```python
from repokit.forge import ForgeConnection

connection = ForgeConnection(host="github.com", token=token)
client = connection.client("owner/repository")
context = connection.pull_request_context(checkout, source="owner/repository")
# 显式跨仓查询：targets=("upstream/repository",)
```

TypeScript 使用 `connection.reader()` 与
`connection.pullRequestContext(checkout, { source: "owner/repository", targets: ["upstream/repository"] })`。
两个语言通过共享 conformance 语料验证连接标识，调用方无需自行计算哈希或拼装 observer。

## Repository lifecycle operations

`PullRequestContext.refresh()` observes local associations and maintains local worktree resources.
Its `ensure_pull_request`, `update`, `merge` and `close` operations perform maintenance after remote
success; failures are reported separately in `last_lifecycle` and logs. Existing query methods and
raw Forge clients do not clean files. See [retention policy and safety](worktrees.md).


## Go CLI and toolkit

`toolkit/go/forge` provides native GitHub/GitLab `Client.Get/List/Create/Update/Close/Merge`.
`Context` binds a checkout, source identity and explicit target repositories; `Ensure` reuses a
unique open PR or creates it. Successful context writes invoke worktree maintenance directly;
maintenance errors are returned separately and never turn a successful remote write into failure.
Raw client queries/writes do not touch local worktrees. No callback registry or validation gate is
part of the PR client. The CLI exposes these operations through `repocli pr`:

```sh
repocli pr create --repo . --base main --title 'Describe the change' --body-file /tmp/pr-body
repocli pr show 123 --json
repocli pr list --state open --json
repocli pr update 123 --body-file /tmp/pr-body
repocli pr close 123
repocli pr merge 123 --expected-sha <reviewed-source-sha> --method squash
```

Go uses a reusable HTTP client with a ten-second request deadline, shared operation context,
16 MiB response limit, at most 100 pages, per-client request serialization and rate-limit cooldown.
It does not retry writes; interrupted, malformed or server-failed write responses are uncertain.
Go `Client.Load` owns optional read-through PR observations under `Config.Home`; `Get` remains
a live read. `repocli pr show --max-age 0` requests a fresh observation. Connection scopes remain
isolated across credentials and providers; cleanup uses current source-verified PR facts.
See [storage, freshness and migration](state.md).

Breaking migration: Go worktree exports moved from the module root to its `worktree` package.
Python remains an independently usable toolkit; its former console entry point is removed.
