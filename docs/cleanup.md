# 闲置资源清理

`repocli clean` 的目标是回收长期未访问的本地资源、节约磁盘空间。repository 状态默认闲置阈值为 **30 天**；
worktree 沿用现有保留策略。命令扫描 RepoKit home 中已登记的仓库；不扫描磁盘寻找源码仓库，也不删除 primary checkout 或 branch refs。

```sh
repocli clean --dry-run --json
repocli clean --inactive-days 30 --json
repocli clean --repo /path/to/repository --dry-run --json
repocli clean --home /path/to/repokit-home --inactive-days 90 --json
```

不传 `--repo` 时覆盖所选 home 的已登记仓库；显式传入则只处理该 repository 身份，包含其记录的多个
clone。home 沿用显式参数、`REPOKIT_HOME`、`~/.repokit` 的优先级。`--inactive-days` 范围为 1–36500；
命令预算通过 `--timeout` 调整，取消或部分失败保留已经完成的动作。

## 同一套 worktree 清理能力

两个 CLI 入口统一调用 service 层的 `state.CleanWorktrees`，由它加载仓库策略并完成 merged 核验、
创建时间排序、数量保留、删除保护和依赖环境回收。`clean` 额外负责遍历已登记的 repository/clone，
并在 worktree 维护后判断是否回收过期 repository 状态；不通过子进程再次运行 CLI。

| 入口 | 范围 | 保留规则 |
|---|---|---|
| `clean` | 所选 home 已登记的仓库；可用 `--repo` 限定 | 各 clone 的既有 worktree 策略；repository 状态另按访问时间过期 |
| `worktree clean` | 指定 clone | 同一 worktree 策略；支持本次运行的数量覆盖 |

worktree 数量沿用 `<git-common-dir>/repokit.json` 的 `worktree.max_count` 和 `worktree.max_prepared`，
默认 **10 个 linked checkout、3 个 prepared checkout**。先删除实时核验已 merged 的候选，再按创建时间
回收超额 checkout 和依赖环境。未知创建时间排最老；30 天访问阈值不替代数量策略，也不导致限额内的
普通 worktree 被全部删除。完整规则见 [worktree retention](worktrees.md)。

两个入口共用 Forge 连接与实时 PR 查询能力；未配置凭据时报告 PR 状态不可用，仍可执行数量回收。
PR 核验失败的 checkout 本轮保留。`--provider`、`--api-host`、`--target-repository` 使用同一配置语义。

primary、当前工作目录所属 checkout、detached、Git lock、进行中的 Git 操作、嵌套 checkout 继续保护。
脏文件、未跟踪文件和未托管的 ignored 文件阻止删除 checkout；但其明确托管的闲置依赖环境仍可释放，
源码保留。包含当前 RepoKit home 的 checkout 不可回收。操作前重新读取 Git 身份与保护条件，绝不传 `--force`。

## 访问事实

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

## 状态回收与并发

访问更新与闲置回收共用 `home/locks/activity/<repository-key>.lock` 的跨语言 lease；锁位于待回收
目录之外。清理在锁内重新读访问记录。Git 资源操作另外使用现有 repository lease；checkout 创建先释放
Git lease 再发布访问/状态，避免锁顺序倒置。维护成功后的观察发布不续期。

repository 状态仅在超过阈值、没有需保留的 linked checkout、没有状态 lease 或近期状态写入时删除。
清理前发现的近期写入用于保护仍有写入者的数据，不冒充用户访问。某个 checkout 删除失败或仍受保护时，
其 repository 状态保留以便后续核验和重试。回收状态包含该 repository 下的 PR 观察与验证历史，后续按需重建；
日志、共享包缓存、其它 home，以及 `--history-dir` 指向的外部目录不在范围内。

目录删除限定在所选 home 的 repositories 根内；不跟随其中的源码 symlink。数据回收成功不代表远端 PR
消失，也不删除源码仓库。`--dry-run` 不删除资源、不更新访问或 topology；临时 lease 和 CLI 日志仍按常规管理。

## 输出与空间

结果遵循 [CLI envelope](cli-output.md)：`data.actions` 列出 repository、动作、路径、状态、原因与可用的 `lastAccessAt`，
`data.errors` 保留失败；部分失败返回非零和 `outcome: partial`，成功项目不会被抹掉。
`planned` 表示预览候选，`completed` 表示已完成；跳过和失败的资源不计入回收估算。

`estimatedBytes` 是不跟随 symlink 的普通文件逻辑大小之和；dry-run 汇总预计回收动作，执行结果汇总
已完成动作。它不是文件系统实际释放块数：硬链接、稀疏文件、压缩与 snapshot 都会影响物理空间。
失败时不声称释放全部容量。`cost.wallSeconds` 记录命令耗时，动作日志包含路径、原因与估算字节数。
