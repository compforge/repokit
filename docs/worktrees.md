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
repocli worktree ensure topic --branch topic --base main --remote origin --repo . --json
repocli depot prepare --repo .worktrees/topic --timeout 10m --json
```

`worktree ensure <name>` 对应 Go `worktree.Ensure`：必须显式提供 `--repo`、branch 和创建基线 Base。
name 是单个目录名，与 Git branch 独立；拒绝空名称、`.`、`..`、绝对路径和路径分隔符。
RepoKit 解析 repository 的主 checkout，统一创建在 `<primary>/.worktrees/<name>`；从软链接、子目录或
linked worktree 调用仍定位到同一个主 checkout。无法定位主 checkout 时报告错误，调用方需通过
`--repo` 指定主 checkout。位置参数不接受路径，也没有 `--path` 覆盖。
`--remote` 指定已配置的 remote，此时 Base 是远端分支名；省略 remote 时 Base 是本地 revision。
底层 `state.EnsureWorktree` 接收解析后的路径，负责创建、复用和登记。

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

结果返回 name、checkout、branch、headSha、action、confirmed、writeAttempted 和新分支的 baseCommit。
`writeAttempted` 表示尝试执行 worktree add；fetch 的本地 tracking ref 更新单独计入 base 阶段。
创建后的核验失败为 unknown，不能据此盲目重试；失败不会删除已经创建的目录或分支。
confirmed 后的创建时间记录或状态发布失败放在 diagnostics，不撤销已确认成功。
状态发布在释放 Git lease 后执行，避免与 cleanup 的锁顺序冲突。

`cost.phases` 报告 inspect、lock_wait、inventory、branch、base、create、verify、record、observe
中实际发生的阶段。`ensure` 不隐式安装依赖；调用方决定是否继续运行 `depot prepare` 和验证。
通用生命周期判断归 state，checkout 拓扑与 worktree 操作归 internal/git/worktree，通用 Git 命令执行归 internal/git/command，refs、push、互斥与恢复记录机制归 internal/git；任务与 session 归属留在调用方。

## Policy and ordering

Configuration lives in `~/.repokit/repositories/<repo-key>/persistent/config.json`, shared by every checkout of one repository:

```json
{"worktree":{"max_count":10,"max_prepared":3,"outputs":[]}}
```

`0 <= max_prepared <= max_count`. Primary checkouts are excluded from both counts. Each run:

1. Removes eligible checkouts of verified **merged** PRs, even below the count limits.
2. Removes the oldest remaining linked checkouts until `max_count` is reached.
3. Recounts surviving prepared checkouts and releases their oldest environments until
   `max_prepared` is reached. This is an independent count, not a fixed number of removals.

The newest retained set provides a buffer for concurrent development sessions, which are
expected to work in recently created checkouts. Cleanup does not discover Codex or other
Harness sessions. Merged checkouts are finished work and are reclaimed independently of this
buffer; callers must start a new worktree for subsequent work. Dirty contents do not extend
retention beyond the count policy. Use an explicit Git worktree lock to pin a checkout.

Creation time is recorded by the Python and Go worktree creation APIs in the checkout's Git
administration directory (`repokit-created.json`, schema 1, `created_at` in Unix nanoseconds).
Missing creation times sort **before all recorded times**; equal/unknown times sort by path.
No commit timestamp or filesystem mtime is substituted. CLI overrides apply only to that run.

Only merged is terminal for cleanup. Closed unmerged PRs receive ordinary count retention.
Count retention is independent of PR state: it can remove an old open/no-PR checkout,
while preserving its branch. A failed merged verification skips that checkout for the round.

## Protection and environment ownership

The invoking checkout, primary checkout, detached checkouts, explicit Git worktree locks,
in-progress Git operations and ancestors of registered nested checkouts are protected.
Eligible checkouts are removed whole with one `git worktree remove --force`, including dirty,
untracked and ignored files, dependency directories and initialized submodule clones. Cleanup
prioritizes disk reclamation; it does not save or archive uncommitted contents. Local branch refs
and their commits remain. Explicit worktree locks remain protected because removal never uses
double force. Environment ownership and inventory errors do not block whole-checkout removal;
these checks apply only when releasing environments from surviving checkouts.
PR-triggered removal verifies a unique merged PR for the source repository and branch. Local
commits after the merge do not block reclamation. Open or ambiguous PR associations do not
qualify for merged cleanup. Checkout identity is rechecked before removal.

Environment release can leave dirty sources intact. It uses the existing Component/workspace
dependency inventory, accepts only ignored checkout-local directories with RepoKit dependency
receipts, and additionally accepts exact relative directories declared in `outputs`. Such output
declarations assert that their contents are regenerable; they must not contain tracked files,
symlinked installation roots or nested Git repositories. Unmanaged dependency environments count
toward the prepared limit but are retained. Shared package-manager caches are never removed.
Preparation and cleanup share a repository process lease; preparation waits within its deadline
and Python cleanup skips a busy repository. Go cleanup waits until the caller cancels. Explicit Git worktree locks
are the interoperable way to pin an otherwise clean checkout during use by another process.
Arbitrary external editors/installers do not participate in RepoKit's lease.

Skipped/failed actions are reported individually. Protected resources can leave counts above
the limits; limits do not bypass protection checks. Git operations and identity are checked again
at the mutation boundary. Partial environment deletion is reported and can be retried.

## Entrypoints

The native Python cleanup operation is `repokit.state.clean_worktrees`. PR writes refresh only
the affected observations and never trigger filesystem maintenance. Choose cleanup explicitly
with `repocli clean` or `repocli worktree clean`; both share the same reclamation service.

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

## Checkout with dependency preparation

`repocli worktree ensure <name> --repo <repository> --branch <branch> --base <ref> --prepare --json` composes
checkout confirmation and Depot preparation in `toolkit/go/worktree.Ensure`. State owns checkout
lifecycle; Depot owns Bin discovery, receipts and installation. Without `--prepare`, ensure only
confirms the checkout. Standalone `depot prepare` remains useful for existing checkouts and changed
lockfiles; `depot inspect` is a query, never a mandatory preflight before preparation.

The flat checkout report remains available together with `preparation` and `ready`. If creation
succeeds but preparation fails or remains unverified, outcome is `partial`, `confirmed=true`, and
`ready=false`. Do not recreate or remove that checkout: inspect each Bin and resume preparation.
`ready` requires complete observations and ready Bins; a receipt-free existing directory is only
`present`. Costs include checkout phases and individual Depot phases. Validation is independent.

## Cleanup time budget

Both cleanup commands interpret `--timeout` (default `2m`) as a cumulative processing budget
for each worktree. Inspection, PR verification, size estimation and removal or environment
release share that worktree's budget; processing other checkouts does not consume it.
A timed-out worktree is reported individually and does not stop the next one. Repository cache
reclamation has no local time budget. Caller cancellation stops the entire sweep and preserves
confirmed actions. Dry-run uses the same budget as execution.
