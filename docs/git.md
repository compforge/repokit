# Git 查询与操作

调用方组合仓库动作形成开发工作流，RepoKit 提供可复用的 Git 事实和显式操作。
任务归属、验证门禁、操作顺序、分支与工作区保留策略由调用方决定。

## 使用方式

Python 的 `repokit.git` 和 `repokit.git.state` 提供 Git 适配；checkout 生命周期由 `repokit.state` 拥有。TypeScript 提供原生 Git 查询，Go 的 state 包拥有显式 worktree 操作。
各语言直接调用 Git，不启动 repocli CLI，也不要求分析器参与写操作；工作区内容捕获见 [Snapshot](snapshot.md)。

```python
from repokit import git, snapshot

observed = snapshot("/path/to/checkout")
if not observed.complete:
    raise RuntimeError(observed.diagnostics)

result = git.stage("/path/to/checkout", ["src/example.py"])
if not result.ok:
    raise RuntimeError(result.err)
# 调用方核对 index 范围与验证结果后，再决定是否 commit / push。
```

`stage` 使用 literal pathspec，要求非空的显式路径；它不清空调用方已有的 index。
Python 的 `stage(..., validate=callback)` 先在隔离 index 中暂存，再把候选 `IndexView`
交给调用方验证。该视图同时提供相对 HEAD 的 changes、完整 entries，以及绑定这些条目 blob OID 的
配置查询；涉及多个文件的判断使用同一候选内容，不混用工作区文件。
验证抛错或 add 失败时，真实 index 保持原样，包括部分暂存；通过后才原子替换。
此模式允许空路径列表，用于验证已有暂存。整个过程持有 Git 原生 index lock，已有锁时拒绝操作。
回调只读检查，不执行 index 写操作；敏感文件、提交范围等策略由回调的调用方拥有。
gitlink 与其他路径一样作为 entry 返回，保留 mode 与 OID；是否要求注册由调用方决定。
保护范围仅限 index，不回滚 Git 对象写入或 clean filter 的外部副作用。
`commit` 提交当前 index。`push` 默认普通推送，可显式给出期望远端 SHA 的 lease。
`rebase` 解析明确的目标版本，冲突保留在工作区。Python 的 `continue_rebase` 继续已暂存的冲突解决，
可显式设置本次调用的 `core.editor`（Git 的 editor 环境变量仍优先）；`abort_rebase` 请求 Git 恢复
rebase 前的状态。两者保留 GitResult，包括失败与 uncertain，不自动重试或管理消费方事务。
是否继续、终止及何时推送，由调用方决定。
Worktree 的路径和分支由调用方给出，删除默认拒绝脏内容和锁定状态。Go `RemoveWorktree` 的显式
`Force` 使用单次 `--force`，允许回收 dirty/submodule checkout，但仍拒绝显式锁定的 checkout。
Python `git.prune_worktrees` 显式请求 Git 清理失效注册，沿用 Git 的过期及锁定保护规则；
返回 GitResult，不删除 checkout 目录或分支，清理时机与失败处理由调用方决定。

Python `git.add_exclude(repo, pattern)` 向 Git `info/exclude` 幂等追加一条精确规则，返回是否新增。
元数据路径由 Git 解析，适用于 linked worktree 和独立 Git 目录；保留原文件字节及规则空白，
读写失败向上传递。选择规则与 best-effort 策略属于调用方，库不内置任何应用目录。

`git.create_branch` 从明确基线创建并切换分支，不隐式保存工作区。Python `stash.save` 返回本次保存的
对象 ID；`stash.restore` 按该 ID 恢复，并保留 index 与工作区的区别。恢复使用 apply，成功或冲突后
均保留 stash 备份，调用方可展示其 ID 供后续清理；库不按共享 stash 栈的位置删除条目。
是否携带改动、何时切分支及何时更新调用方状态，由工作流决定。

GitResult 保留退出码和输出；超时的 uncertain 表示结果可能已生效，不能盲目重试。
查询失败不能当作 clean：workspace status 包含 complete，不能观察完整时由调用方决定如何处理。
Git 传输默认有超时。Forge 客户端的读取与写入契约见 [PR](pr.md)。

## Git 事实与 checkout 拓扑

Python `git.state.local_default_branch(repo, remote="origin")` 只读取本地缓存的远端 HEAD，
保留含斜线的分支名；缺失返回 None，Git 失败抛错。该查询不访问网络、不猜默认分支，
刷新缓存和缺失时的 fallback 由调用方决定。

Python 的 `committed_paths` / `range_paths` 与 TypeScript 的 `committedPaths` / `rangePaths`
返回原始仓相对路径，保留 Unicode、空格、换行及 rename 两端。单 commit 按第一父提交比较，
根 commit 与空树比较；分支范围按 merge base 比较。查询失败抛错，不能解释成没有改动。
`parse_remote_url` / `parseRemoteUrl` 统一解析 HTTPS、SSH URL 和 scp 格式；结果只有 host/path，
不保留凭据，也不选择平台、API 地址或认证方式。身份解析接受带 host/path 的非 file URL，
包括 `git+ssh`、`git+https`、`ssh+git` 等 scheme；是否允许或能够执行该传输由 Git 操作决定。

`checkout_info` / `checkoutInfo` 返回当前 checkout、独立 Git 元数据、共享元数据和可确认的主 checkout。
`list_worktrees` / `listWorktrees` 保留 Git 注册记录，使用 NUL 分隔以完整保留路径；失败抛错。
注册记录的第一项可能是 bare 或独立 Git 目录，不能直接当作主 checkout。
独立 Git 目录没有反向记录原 checkout 时，`main_root` / `mainRoot` 为未知；调用方决定共享状态
放在可确认的主 checkout 还是 common directory。库不创建 `.devloop`、Session 或清理计划。

Go 消费方从 `github.com/compforge/repokit/toolkit/go/state` 导入并使用 `ListWorktrees`、`Checkout`、`AddWorktree`、`RemoveWorktree`。
查询接受 context，Git 读取每次最多 5 秒，写操作最多 30 秒；调用方更短的时限优先。
Go `GitResult.Err` 表示操作错误，`Uncertain` 表示启动后取消导致结果不确定；
Python 使用 `ok` / `uncertain`。新分支与 detached 模式互斥，remove 默认保护脏内容与锁定 checkout。
CCR 可以据此组织评审 checkout，devloop 可以组织开发 checkout；命名、占用、保留期限和清理授权
均属于消费方。TypeScript 当前提供查询，写操作按实际消费需求扩展。

Python `git.index` 提供逐文件 status、工作区删除路径、带前后 mode 的 index changes，以及工作区
`.gitmodules` 中的注册路径。路径按 NUL 分隔读取，保留空白、换行和 rename 两端；查询失败抛错，
不能当作没有改动。是否暂存、是否允许 gitlink 及如何处理敏感文件由消费方决定。

`list_checkouts` / `listCheckouts` / Go `ListCheckouts` 将 Git 注册记录解释为 checkout 清单。独立元数据目录无法反查主工作区时，
对应项保留未知 path；从主工作区自身调用时可使用当前 checkout 定位。消费方不能把未知位置当成
checkout 已回收，也不能把元数据目录作为工作区操作。

本地分支与远端分支 tip 查询的空集合表示成功读取且无匹配项；Git 执行失败抛错。Checkout 查询也
区分未知主工作区位置与查询失败，兼容的 metadata/linked 查询复用同一拓扑模型。bare 注册项不构成
checkout。消费方决定是否保留旧观察、展示未知或停止操作。三种语言共用 `conformance/git/checkouts.json` 验证
checkout 语义；原始 worktree 注册记录用于底层诊断，工作区消费者使用 checkout 清单。

单值查询也保留缺失与失败的区别：current branch 的空值表示 detached HEAD；HEAD 的空值表示 unborn；
revision 的空字符串表示 quiet verification 确认无法解析该 revision。ahead/behind 的空值表示缺少
比较端点或 upstream，不表示 0/0。执行失败与格式错误抛错。祖先判断仅在 Git 返回 0/1 时返回
true/false，缺失对象和执行失败抛错；即使两个输入字符串相等也必须验证。PR 选择、监控缓存及
写入前如何处理缺口属于消费方。Python/TypeScript 共用 scalar query 契约语料，不为语言对齐扩展无消费者的 API。

`git_path` / `gitPath` 由 Git 解析元数据的绝对路径，保留空格和换行，允许目标尚不存在；失败抛错。
Git 决定 `index`、`rebase-merge` 等 checkout-local 路径与 `info/exclude` 等共享路径的归属，
消费方无需猜测 `.git` 的布局。Python `rebase_in_progress` 观察当前 checkout 的两种 rebase backend；
状态目录不存在表示未进行，Git 查询或文件读取失败不能当成未进行。路径契约由 Python/TypeScript
共享语料验证。事务文件命名、内容、index 活跃度解释及 rebase 的 lease/验证/发布策略归消费方。

`find_checkout` / `findCheckout` / Go `FindCheckout` 查找物理路径祖先中的 `.git` 条目并由 Git 验证。
没有 checkout 返回 None/undefined/空字符串；路径无效、权限、损坏元数据或 Git 执行失败抛错。
路径保留尾部空格和换行。该查询不把 Git 元数据目录当源码 checkout，也不通过环境覆盖来选择其它仓库。

Python `git.state.fetch` 和 `ls_remote_tips` 接受显式 remote 名或 URL，默认 origin；选择远端、
本地 ref 命名以及 push / fetch 的协作策略属于调用方。

## 调用预算与不确定结果

Python `operation(timeout=..., cancel=...)` 为一组 Git/Forge 调用提供共享 deadline；嵌套 scope 不能延长
外层预算。Git 的单次默认上限仍生效；取消、超时或输出超限会终止并回收子进程。stdout 上限 16 MiB，
stderr 上限 64 KiB。启动前取消是明确未执行；启动后中断保留 GitResult.uncertain，不推断回滚。

```python
from repokit import git, operation

with operation(timeout=20):
    result = git.create_branch(repo, "feature", "origin/main")
```


## 工作流消费的语义查询

Python `git.state.commits(base, head)` 返回 OID、subject 和 body，保留多行说明；`merge_base`
区分无共同祖先与查询失败。`validate_branch_name` 只接受字面分支名；`git.fetch_branches`
按明确的分支清单刷新 tracking refs，即使配置的 fetch refspec 较窄也能获取所需端点。
`git.index.staged_patch` 按字面路径读取零上下文 patch；`working_tree_differences` 比较候选 index
与工作区原始字节、mode、symlink 和 gitlink，返回差异路径，不执行 clean filter 或修改 index。
调用方负责验证身份、并发写入约束和提交许可。

Python `checkout_at` / TypeScript `checkoutAt` 只观察指定目录，不向祖先搜索；无 `.git` 条目为
正常缺失，损坏元数据为错误。拓扑缓存最多一秒，元数据标记变化提前失效；缓存不含 branch/HEAD
或 PR 状态。主工作区不明时仍保留 common directory，应用决定自己的状态存放位置。
TypeScript `localDefaultBranch`、`refExists`、`isIgnored` 和 `hasUnstagedChanges` 保留缺失与
失败的区别。`addExclude` 与 Python `add_exclude` 一样显式追加精确规则，保留已有字节，
不内置任何应用目录；默认分支 fallback、忽略目录选择和 best-effort 策略由调用方决定。

候选 gitlink 与工作区比较时，只有缺失路径或目录可以表示未初始化的子模块；普通文件或
symlink 替换必须作为类型差异返回，不能借用 live index OID 得出相同结论。


## 实现布局

Python Git 能力集中在 `repokit/git/`：包入口导出显式操作，`operations.py` 执行动作，
`git/state.py` 观察 checkout/ref，`index.py` 观察 index，`stash.py` 和 `remote.py` 处理各自语义。
`_catalog.py` 与 `_staging.py` 是内部读取和 index 事务实现。公开子模块使用
`repokit.git.state`、`repokit.git.index`、`repokit.git.stash`、`repokit.git.remote`；调用方直接迁移导入。
TypeScript 的对应实现集中在 `src/git/`，`index.ts` 汇总公共导出，包根 API 保持不变，
`catalog.ts` 仍为内部实现。仓库观察和 Forge 保持各自边界，不放进 Git 目录。

## Repository 本地信息

信息目录、read-through、freshness 与回收边界见 [Repository 状态](state.md)。

显式 checkout Git 操作过滤继承的 `GIT_DIR`、`GIT_WORK_TREE`、`GIT_INDEX_FILE`、object store 与
命令级配置覆盖，避免 hook/agent 上下文把操作指向其他 checkout。认证、传输、作者配置保留；
显式临时 index 在过滤后应用。通用 subprocess 和无 checkout 的 global Git 操作不套用此规则。
验证/依赖准备的默认继承环境同样清理 Git checkout override；显式 validation environment 保留调用方契约。
此改动不复制 `node_modules`、`.venv` 等依赖目录，不引入 reflink 或跨 worktree 的依赖继承。

继承的 `GIT_CONFIG_COUNT/KEY_*/VALUE_*` 与 `GIT_CONFIG_PARAMETERS` 按配置项过滤：
保留 HTTP、credential、URL rewrite、SSH/代理及用户身份配置，重新编码后传给 Git；
`core.worktree`、include 等可能改变仓库定位或引入额外配置的覆盖不继承。显式每次调用的覆盖仍最后应用。

## Tag publication

Go `git.PublishTags(ctx, repo, git.PublishTagsOptions{Remote, Target, Tags})` 与 CLI 使用同一 service：

```sh
repocli git tag publish toolkit/go/v1.2.3 apps/cli/v1.2.3 --target <commit-or-local-ref> --repo . --json
```

`--target` 必填，解析为 commit 后固定用于整个操作；`--remote` 默认 `origin`，必须是已配置
且只有一个 push URL 的 remote。预检、推送、核验都使用该 push destination，支持 fetch/push URL
不同的仓库；多个 push URL 无法提供跨目的地原子性，写入前拒绝。认证复用 Git 的 SSH/credential
配置与现有执行环境，不经过 Forge HTTP 客户端。调用不会创建本地 tag、切换分支或触发 worktree maintenance。

名称遵守 Git ref 语法；版本格式、递增规则、模块发布顺序和 Release/包/部署动作属于调用方。
重复名称按输入顺序去重。远端已有 lightweight 或 annotated tag 按 peeled commit 比较：同 commit
视为已满足，不改写 tag 对象；不同 commit 在推送前拒绝整个批次。缺少的 tag 以 lightweight tag
通过一次 `git push --atomic --no-follow-tags` 发布，不 force、不附带其它 tag；远端不支持 atomic
时失败，不降级。预检后的并发冲突由 Git 拒绝，不覆写已发布 tag。

结果包含 `remote`、`targetCommit`、`confirmed`、`pushAttempted`、可用的 `pushExitCode` 和 `tags`。
每个 tag 保留 name/ref、观测到的 peeled objectID（已有 tag 可能指向非 commit），以及 `present` / `missing` / `conflict` / `unknown`。
`present` 说明远端当前满足目标，不声称一定由本次操作创建。推送成功与否均尝试读取远端核验；
全部满足目标才成功，因此推送超时但远端已完成仍可返回成功。推送后观察失败、预算耗尽或写入结果
尚不确定时返回 `git.tag_publish_unknown` / `error.uncertain=true`，不能把预检事实当作最终状态。
Git porcelain 明确报告拒绝且核验完成时返回确定失败；仅有非零退出码或未收到确认的
remote failure 仍保留不确定性。不会自动重推；调用方先检查同一 push destination 的目标 refs，再决定恢复。

本地查询每次最多 5 秒，远端观察最多 30 秒，push 最多 120 秒；调用方 context 的更短预算优先。
push 为最终核验预留剩余预算的 20%（最多 30 秒），取消后不创建脱离调用方的后台任务。
`phases` 记录 prepare、remote_preflight、push、remote_verify 的实际耗时；CLI 同时映射到
`cost.phases`，未执行的阶段不补造。错误保留 code/op/subject 和 cause，不将可能含凭据的远端 URL
或原始 transport stderr 写入公共输出。

## Recoverable rebase

Go `git/rebase` 与 `repocli git rebase start/status/continue/abort/finish` 拥有显式 rebase
的完整恢复能力。Python 原生 `git.rebase` 等仍是单次 Git 操作；调用方需要 CLI 生命周期时直接调用
repocli，不再维护第二套恢复记录。事务与精确 lease 的契约见 [Recoverable rebase](git-rebase.md)。
