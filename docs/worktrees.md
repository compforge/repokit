# Worktree retention

RepoKit owns local worktree maintenance. A checkout carries a branch; a branch may carry a PR.
Remote PRs and local branches do not require a checkout. Removing a checkout retains branch refs,
PR observations and shared validation history.

The global `repocli clean` and `repocli worktree clean` call the same `state.CleanWorktrees`
service entry with the policy below. Global cleanup additionally traverses recorded repositories
and expires repository state after 30 days; that threshold does not change worktree retention.
See [repository cleanup](cleanup.md).

## 创建与复用

```sh
repocli worktree ensure .worktrees/topic --branch topic --base main --remote origin --repo . --json
repocli depot prepare --repo .worktrees/topic --timeout 10m --json
```

`worktree ensure` 对应 Go `state.EnsureWorktree`：显式指定路径、branch 和创建基线 Base。
相对路径以 `--repo` 对应 checkout 根为基准。`--remote` 指定已配置的 remote 名称，此时 Base 是该 remote
上的分支名；省略 remote 时 Base 是本地 revision。库不选择默认分支、不规定命名或工作目录布局。

| action | 行为 |
|---|---|
| created | 分支不存在，解析并固定基线 commit 后创建分支和 checkout；远端基线必须先成功 fetch。 |
| resumed | 分支存在但未 checkout，在指定的新路径恢复它；不刷新或重置到 Base。 |
| reused | 路径已注册且实际 checkout 属于同一 clone、同一分支，保持其 HEAD、脏文件与创建时间。 |

存在但未注册的目录、不同仓库/分支、detached checkout、leaf symlink、其它路径已 checkout 的分支以及
失效注册均报告冲突或核验错误。不会接管目录、prune 注册、移动分支、reset/rebase 或清理其它 checkout。
创建阶段使用同一 repository lease 与 Depot/cleanup 互斥，锁内重新列举注册并验证身份。
预算来自调用方 context / CLI `--timeout`；Git 只读查询最多 5 秒，fetch/add 单步最多 2 分钟。
锁等待没有额外 30 秒截断。跨进程非协作 Git 操作不受 lease 管理，执行后的核验仍需保留。

结果返回 checkout、branch、headSha、action、confirmed、writeAttempted 和新分支的 baseCommit。
`writeAttempted` 表示尝试执行 worktree add；fetch 的本地 tracking ref 更新单独计入 base 阶段。
创建后的核验失败为 unknown，不能据此盲目重试；失败不会删除已经创建的目录或分支。
confirmed 后的创建时间记录或状态发布失败放在 diagnostics，不撤销已确认成功。
状态发布在释放 Git lease 后执行，避免与 cleanup 的锁顺序冲突。

`cost.phases` 报告 inspect、lock_wait、inventory、branch、base、create、verify、record、observe
中实际发生的阶段。`ensure` 不隐式安装依赖；调用方决定是否继续运行 `depot prepare` 和验证。
通用生命周期判断归 state，Git 查询/写入适配归 internal/gitworktree；任务与 session 归属留在调用方。

## Policy and ordering

Configuration lives in `<git-common-dir>/repokit.json`, shared by every checkout of one repository:

```json
{"worktree":{"max_count":10,"max_prepared":3,"outputs":[]}}
```

`0 <= max_prepared <= max_count`. Primary checkouts are excluded from both counts. Each run:

1. Removes eligible checkouts of verified **merged** PRs, even below the count limits.
2. Removes the oldest remaining linked checkouts until `max_count` is reached.
3. Recounts surviving prepared checkouts and releases their oldest environments until
   `max_prepared` is reached. This is an independent count, not a fixed number of removals.

Creation time is recorded by the Python and Go worktree creation APIs in the checkout's Git
administration directory (`repokit-created.json`, schema 1, `created_at` in Unix nanoseconds).
Missing creation times sort **before all recorded times**; equal/unknown times sort by path.
No commit timestamp or filesystem mtime is substituted. CLI overrides apply only to that run.

Only merged is terminal for cleanup. Closed unmerged PRs receive ordinary count retention.
Count retention is independent of PR state: it can remove an old clean open/no-PR checkout,
while preserving its branch. A failed merged verification skips that checkout for the round.

## Protection and environment ownership

The invoking checkout, primary checkout, detached checkouts, explicit Git worktree locks,
in-progress Git operations and ancestors of registered nested checkouts are protected.
Dirty/untracked files and unmanaged ignored files prevent checkout removal. After these checks,
Go cleanup uses one `git worktree remove --force` to reclaim the entire checkout and its Git
registration, including initialized submodule clones. It does not archive or separately manage
gitlinks; explicit worktree locks remain protected because removal never uses double force.
Status checks use `--ignore-submodules=none` so repository configuration cannot hide unsaved
submodule changes. PR-triggered removal reuses the existing observer's association verification
and requires the local HEAD to equal the PR source SHA, including squash merges. New commits
on a reused branch are therefore not erased merely because an earlier PR merged.

Environment release can leave dirty sources intact. It uses the existing Component/workspace
dependency inventory, accepts only ignored checkout-local directories with RepoKit dependency
receipts, and additionally accepts exact relative directories declared in `outputs`. Such output
declarations assert that their contents are regenerable; they must not contain tracked files,
symlinked installation roots or nested Git repositories. Unmanaged dependency environments count
toward the prepared limit but are retained. Shared package-manager caches are never removed.
Preparation and cleanup share a repository process lease; preparation waits within its deadline
and Python cleanup skips a busy repository. Go cleanup waits within its command deadline. Explicit Git worktree locks
are the interoperable way to pin an otherwise clean checkout during use by another process.
Arbitrary external editors/installers do not participate in RepoKit's lease.

Skipped/failed actions are reported individually. Protected resources can leave counts above
the limits; limits do not bypass protection checks. Git operations and identity are checked again
at the mutation boundary. Partial environment deletion is reported and can be retried.

## Entrypoints

The native Python operation is `repokit.state.clean_worktrees`. `PullRequestContext.refresh()`
owns observation plus maintenance; its `ensure_pull_request`, `update`, `merge` and `close` methods
invoke that lifecycle after success. Maintenance errors are logged and available in
`last_lifecycle`, without changing the successful remote operation's result. There is no
`after_success` callback. Raw Forge clients, `get_branch`, `local_branches` and other ordinary
queries do not invoke filesystem maintenance.

The Go implementation lives in `toolkit/go/state`; the CLI directly calls this package.
Python provides a toolkit only: the `repokit-worktree` console entry point has been removed.

```sh
repocli worktree clean --repo /path/to/repository --dry-run --json
repocli worktree clean --repo /path/to/repository --max-count 10 --max-prepared 3
```

PR operations use the native `toolkit/go/forge` client. Both PR operations and cleanup query that
same capability, verifying the source repository, branch and exact head. Cleanup always queries current platform facts. Go PR display can use the explicit
[repository observation store](state.md); cached display metadata never authorizes cleanup.
Credentials come from `GH_TOKEN`/`GITHUB_TOKEN` or `GITLAB_TOKEN`; `--provider`, `--api-host`
and repeatable `--target-repository owner/repo` supply explicit connection/target scope.
No fork parent or another application's credential configuration is inferred. Without credentials,
count retention still works and PR observation is reported unavailable. `--dry-run` projects both
stages without deleting resources. The [CLI result envelope](cli-output.md) puts actions,
errors and projected/actual remaining counts in `data`. Errors return nonzero.

Both implementations keep `repokit-releases.json` in the checkout's Git administration directory.
Each pending relative directory is bound to its device/inode identity before deletion. This
ownership evidence survives removal of the dependency receipt and allows a later clean to resume;
a replacement directory is retained with an error. Python preparation refuses a pending release.
Go cancels Git process groups and waits for the Git process before releasing the repository lease;
in-process environment removal checks cancellation between entries and preserves the journal.

Deletion journals are stored outside disposable environments. A normal cleanup retires records
whose directory is already absent, completing an interrupted deletion before later preparation.
Dry-run only observes these records. A different directory identity at the same path remains
protected; recovery never authorizes deleting a replacement directory.
