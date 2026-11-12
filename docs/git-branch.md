# Branch 创建与改动携带

`toolkit/go/git/branch` 拥有显式分支创建、基线刷新、改动保存与恢复；CLI 映射这些能力。
调用方决定分支名、目标基线、是否携带改动以及 session/任务策略。此操作不拥有 PR 或验证门禁，
不将恢复记录塞入可回收的 repository 状态。

## 入口与结果

```sh
repocli git branch create feature --base main --remote origin --carry-changes --repo . --json
repocli git branch status feature --repo . --json
```

Go 入口是 `branch.Create(ctx, repo, Options)` 与 `branch.Status(ctx, repo, name)`。
`--base` 必须显式提供：没有 `--remote` 时解析本地 ref；提供 configured remote 时先刷新该远端分支。
只有新建分支刷新基线，随后固定 commit；当前已在同名分支则复用，不 reset、不重新 stash。
其它已有本地分支拒绝创建，调用方应显式选择已有 checkout。

结果分别给出 `branchConfirmed`、`changesRestored` 和完整操作的 `confirmed`。
`action` 区分 created、reused、observed；未确认动作时可能为空。
失败后也可能已切换到新分支，消费方必须先处理已确认的身份，再决定开发流程是否继续。
`stashOid` 是本次保留的精确备份；`recoveryPath` 指向 checkout 私有恢复记录。
JSON 与 text 均报告 lock_wait、inspect、resolve_base、save_changes、create_branch、restore_changes 中
实际执行的阶段耗时；失败阶段同样计时。

## 数据保护与恢复边界

- 默认拒绝 dirty checkout。显式 carry 使用 `stash push --include-untracked` 保存 staged、unstaged 与
  untracked，通过唯一 marker 定位 stash，再以准确 OID 执行 `stash apply --index`。
  不 pop/drop，成功或失败均保留备份；ignored 文件不进入 stash，checkout 不允许覆盖 ignored 文件。
- 存在 Git 原生操作或未合并 index 时拒绝；stash 后仍 dirty（如嵌套仓库）时停止切分支并报告剩余现场。
- 同一 common Git directory 使用与 worktree/rebase/Depot 相同的 lease；总预算来自调用方 context。
  lease 仅协调 RepoKit 操作，不阻止外部 Git 或编辑器修改文件，操作期间调用方需保持 checkout 独占。
- 已知 checkout 失败，仅在原分支和 HEAD 未变、工作区干净时恢复 stash 到原 checkout。
  stash 保存失败、checkout 结果未知或 apply 失败均保留记录；不猜测成功、不重复 apply。
- 记录以 branch 名的 hash 分隔，保存在 checkout Git directory 的 `repokit-branches/`。
  每次写操作前原子发布阶段与固定身份。linked worktree 的记录互相隔离，后续其它分支操作不覆盖原备份信息。
  文件权限 0600、目录 0700，记录不含凭据或 devloop session。

`status` 只读观察当前分支、HEAD、冲突与已有记录；保存 stash 时响应丢失，可通过记录 marker 重新发现 OID。
恢复状态不是实时工作区内容快照；历史 `changesRestored` 不证明用户后来没有修改文件。
未知 apply 的结果无法仅靠 clean/dirty 判断，因此 status 不把它自动升级为成功。

未完成记录阻止同名 create 重放。发生冲突时，先检查当前 index、工作区和准确 stash 内容，手动解决；
只有确认改动已恢复或明确放弃该次操作之后，才将 `recoveryPath` 指向的记录归档移出该位置。
保留 stash 直到所有内容已核对。没有自动 abort/continue，也不提供通过重试 create 隐式恢复的路径。
已确认 Git 动作后的记录写入失败作为 warning；调用方不应因此重做已经确认的 Git 动作。

`repocli git branch show <name> --json` 返回当前 `observation` 与可选的 `creation` 来源记录。
`status` 继续查询创建事务与恢复证据；`show` 不恢复或重放操作。来源记录的归属与生命周期见 [state](state.md)。
