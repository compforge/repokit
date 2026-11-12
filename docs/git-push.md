# 普通分支发布

`git.Push(ctx, repo, PushOptions)` 与 `repocli git push` 发布显式分支的一个固定 commit，并核验实际
push destination。它们不决定保护分支、PR 生命周期、外来提交或验证门禁；这些策略归调用方。
历史改写继续使用 [rebase](git-rebase.md) 的精确 lease，不为普通 push 增加隐式 force。

## 输入与确认

```sh
repocli git push --branch topic --commit <full-sha> --remote origin --set-upstream --repo . --json
repocli git push --branch topic --commit <same-full-sha> --remote origin --check --repo . --json
```

`--commit` 必须是调用方冻结的完整 SHA-1/SHA-256；发布前验证它仍是显式本地分支的 tip。
`--target` 可选择不同的远端分支，默认与源分支同名。只发送固定 SHA 的一个 refspec，不附带 tags、
不递归推送 submodule。dirty 工作区不阻止发布已有 commit，也不会被暂存或提交；Git 原生 push hooks 仍执行。

配置的 remote 必须只有一个 push URL，预检、push 和核验使用同一解析结果；fetch URL 可以不同。
URL 保持私有，不进入报告。恢复观察时调用方需保持 remote 配置不变；修改配置后的查询属于新目标。

`confirmed=true` 的唯一依据是远端目标 ref 与 `targetCommit` 一致。
`remoteState` 为 matching、different、missing 或 unknown；`remoteCommit` 是成功观察到的远端 SHA。
`pushAttempted` 与 `pushExitCode` 是执行证据，不能代替确认。Git 返回错误但核验相同时，报告成功和
`git.push_reconciled` warning；预检已相同时直接确认，不重复推送。

`--check` 只观察，不 push、不配置 upstream；本地分支后来前进或删除，不影响对原 commit 的远端核验。
成功查询到 missing/different 时命令仍可成功，但 `confirmed=false`，调用方不能当作发布成功。

## 失败与恢复

只有 Git porcelain 明确拒绝且远端核验成功时才报告已知拒绝；连接中断、取消、核验失败或未匹配的
响应均保留 unknown。每次调用最多执行一次 push，无自动重试。
响应丢失时，用原 `--commit` 和相同 remote/target 执行 `--check`；不要将新的 HEAD 当成旧请求重试。
此操作不保存第二套 branch 生命周期状态，冻结意图由显式参数及报告携带。

与 worktree/rebase 共用 common Git directory 的 lease。调用方 context 约束完整操作，push 为后续
核验预留预算，不延长 deadline。锁只协调 RepoKit 操作，不能阻止外部 Git 同时修改源分支或远端。
即使源分支在最后核对后被外部进程移动，发送的仍是原固定 SHA。

## Upstream 与成本

`--set-upstream` 在确认远端后写入源分支的 remote/merge 配置，有界 fetch 目标分支并核对 upstream SHA，单独返回 `upstreamConfirmed`。
失败保留 `confirmed=true` 并附 warning，不能因此重做已确认的 push。只读 check 不允许此选项。
tracking refs 按 remote 的 fetch mapping 更新；fetch URL 与 push URL 不同或配置没有对应 mapping 时，
可能已发布但 upstream 无法确认，报告保留 warning。

JSON/text 均报告 prepare、lock_wait、remote_preflight、push、remote_verify、configure_upstream、refresh_tracking 中实际
执行的阶段耗时；统一 envelope 的 `cost.phases` 使用 `git.push.` 前缀。
