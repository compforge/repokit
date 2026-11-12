# 验证历史查询

Validation 拥有检查执行、输入证据、历史和复用。查询从同一 WAL 重放实际 Run，
不运行 Make、不检查当前文件是否匹配、不创建第二套缓存；state 只提供仓库身份与目录约定。

## CLI

```sh
repocli validate history list --repo . --check test --status failed --limit 20 --json
repocli validate history list --repo . --component toolkit/go --checkout /path/to/checkout --offset 20 --json
repocli validate history show <runID> --repo . --json
```

`--repo` 通过现存 checkout 确定 Repository，默认列出该 Repository 所有 checkout 的保留记录。
`--checkout` 是精确路径筛选，允许查询已经移除的 worktree；不会在该路径运行 Git 或检查。
`--component` 使用仓库相对 root，根组件为 `.`；`--check` 为 lint/test；`--status` 为
passed/failed/cancelled/uncertain/unfinished。空筛选表示不限。
`--history-dir` 指定历史根目录，默认 `<RepoKit home>/repositories`；身份与存储位置规则见
[验证执行历史](validation.md#执行历史)。`validate --history` 和 `stats` 继续提供汇总统计。

列表按 Run 开始时间降序、相同时间按 runID 排序；每个实际执行只占一行。
复用不会新增或重排 Run，`reuseCount` 和 `lastReusedAt` 表示保留事件中的复用记录。
`limit` 默认为 20，范围 1–100；`offset` 默认为 0。`total` 是筛选后的数量，`hasMore`
表示还有记录。每次查询是独立观察，并发执行或保留期清理可能改变后续页面。
列表不加载输出和输入 manifest，Run 摘要不包含当前代码的通过判定。

详情返回 Run、原始 CheckResult、输入 manifest 和证据缺口：

- Run 含 checkout、Component、check、scope、开始/完成事件时间、实际执行耗时和输入一致性。
  `finishedAt` 是整轮输入复核后写入完成事件的时间；实际检查耗时使用 `durationSeconds`，
  不用两个事件时间相减推算。
  没有 finished 事件的记录标为 `unfinished`，`inputsConsistent` 为 null；不能据此断言进程仍在运行。
- CheckResult 保留命令、文件范围、输入声明、退出状态、测试 timing、stdout/stderr 和诊断。
  `testTimings` 是 runner 报告的历史耗时，父子 case 和并行测试时间可能重叠。
- 输入 manifest 提供 snapshot 与文件类型/摘要，不包含源码正文或环境变量值；
  CheckResult 的 `inputFiles` 指明该 check 使用的输入范围。
- `complete` 只说明该 Run 的证据是否齐备。历史检查失败仍可是完整详情，
  历史检查通过也不意味着当前代码通过，不构成 commit/push 门禁。

JSON 保留完整捕获内容。text 展示状态、历史耗时、慢 case、输入摘要和有界输出片段；
截断时提示使用 `--json`。两个命令支持 `--output <file>` 导出统一结果。

## 结果与成本

遵循 [CLI 输出契约](cli-output.md)：读取成功使用 `outcome=succeeded` / exit 0，
即使查询到的历史检查是 failed 或 unfinished。列表分页由 `hasMore` 表达，不是查询失败。

找不到 runID 返回 `validation.run_not_found`，不推断它从未存在或已过期。
日志/输入缺失或不可解析时保留已读 Run，`complete=false`，返回 `outcome=partial` / exit 1，
diagnostics 指向缺失证据；空输出字符串不能替代其可用性判断。
WAL 损坏、读取失败或取消返回失败，不以空集合或部分重放掩盖错误。

`cost.wallSeconds` 和 `cost.phases.history.query` 度量本次查询；Run 的 `durationSeconds`
与 CheckResult 的 timing 是历史执行成本，不计入本次查询的 validation execution cost。

## Go API 与存储边界

`ListValidationRuns(ctx, ValidationRunQuery)` 返回筛选、分页后的摘要；
`ReadValidationRun(ctx, historyDir, checkout, runID)` 返回单次详情。
调用方显式提供 history root；SDK 不隐式读取 CLI 配置，不返回 CLI envelope。
详情中的证据缺口通过 `Complete/Diagnostics` 返回；无法建立查询结果时返回结构化 error。

查询沿用 history 的仓库锁、WAL 重放和日志读取；单次详情在同一把锁下读取 Run、manifest 和输出，
避免与保留期清理交错。它不执行清理、不更新访问时间，不产生 CLI 执行日志或初始化缺失的历史库。
已有历史仍需要可写的锁文件；这是锁协调，不是检查或证据写入。
本轮只提供 Go API 与 CLI，TS/Python 原生读取及 devloop Board 接入另行实现。
