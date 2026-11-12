# CLI 输出契约

RepoCLI 的 JSON 输出遵循本文的 `schemaVersion: "1.0"` 契约。
这是 CLI 展示协议；SDK 领域报告保持自己的结构，CLI 将其放在 `data` 中。

输出应让人、脚本和 AI 判断：动作是否完成、结果覆盖什么范围、哪里存在问题，以及如何获取证据。
领域组件拥有结果与诊断事实；CLI 拥有输出格式、呈现和退出码映射；调用方拥有门禁与恢复决策。
执行日志的记录和保留见 [日志](logging.md)，不替代命令结果。

## 输出通道与格式

- 普通命令的 JSON 模式向 stdout 输出一个最终结果对象，成功、失败和部分结果使用同一入口；
  进度与运行日志走 stderr。可处理的参数错误和执行错误也输出结构化结果。
- 子进程 stdout/stderr 由 CLI 收集为证据，不能直接混入 JSON 结果流。stderr 出现内容本身不代表失败。
- 文本模式先展示结论、问题和关键统计，再展示有界的明细；文本与 JSON 从同一结果生成，不能各自判断成功。
- 长任务确需实时机器消费时，另行提供显式 JSONL 模式，用事件类型、关联标识和终态区分过程与结果。
  单次 JSON 与 JSONL 不随终端探测、并发程度或执行时长隐式切换。
- help、`--version` 信息开关及 shell 补全按各自文本格式输出。进程被强制终止、管道断开等情况可能没有完整结果；
  消费方必须同时检查退出状态和结果是否可解析，不能将缺失结果解释为成功或无副作用。

## 结果、诊断与证据

公共结构只承载跨命令共有的信息，领域数据保持各自模型。所有结果包含以下字段：

| 字段 | 含义 |
|---|---|
| `schemaVersion` | 输出格式版本，当前为 `"1.0"`。 |
| `command` | 完整命令路径，例如 `repocli validate`、`repocli pr create`。 |
| `outcome` | `succeeded`、`failed`、`partial`、`unknown`；描述命令声明的操作结果。 |
| `summary` | 可读结论；不用于机器分支判断。 |
| `diagnostics` | 诊断数组；没有诊断为 `[]`。 |
| `data` | 领域报告或集合；尚无可用结果为 `null`，已知空集合为 `[]`。 |
| `artifacts` | 成功导出的证据数组；没有导出为 `[]`。 |
| `cost` | 本次调用的耗时；具体语义见下节。 |

终止命令的错误另有 `error` 对象；检查失败、批量部分失败等已由领域报告解释的结果，
通过 `diagnostics` 与 `data` 表达，不再附加一个重复的通用 error。

`error` 直接使用 toolkit 的 [`errs.Error`](errors.md)，包含稳定 `code`、可读 `message`，
按需提供 `op`、`subject`、`hint`、`uncertain`、上游 `httpStatus` 和 `retryAt`。
Go 内部保留原始 cause，支持 `errors.Is/As`，不向 JSON 暴露 stack 或原始 cause；文本直接使用 Error.Text()。
错误分类依据类型和状态码，不能解析 message 推断是否超时、是否可重试。常见 code 包括：
`usage.invalid_argument`、`execution.failed`、`execution.canceled`、`execution.deadline_exceeded`、
`forge.authentication_required`、`forge.access_denied`、`forge.not_found`、`forge.rate_limited`、
`forge.http_error`、`forge.outcome_unknown`、`filesystem.not_found`、`filesystem.permission_denied`、
`network.failed`；特定操作可提供自己的稳定 code。操作和目标与 code 独立表达。

每项 diagnostic 包含 `code`、`severity`（warning/error）、`phase`、`message`，按需提供
`subject`、`path`、`line`、`version`、`evidence` 和 `hint`。`evidence` 是当前最终结果内的
JSON Pointer，例如 `/data/components/0/checks/1`。源码位置沿用领域报告的仓库路径与版本语义；
artifact 的 `path` 是绝对路径，`command_result` 由显式 `--output` 产生，保留与删除由调用方负责。
validation 的执行、计划和历史查询均可导出；文件与 JSON stdout 使用相同的最终结果结构与字节内容。
导出失败时不声明 artifact，stdout 保留领域结果并报告导出错误。

设计时必须覆盖以下信息：

| 信息 | 契约 |
|---|---|
| 格式身份 | JSON 格式版本独立于 CLI 产品版本；结果种类应可辨认。 |
| 操作结果 | 区分成功、失败、部分完成和结果未知；已确认完成的动作及其对象身份必须保留。 |
| 领域判定 | validation 的 `passed`、`complete`、`inputsConsistent` 等保留明确含义，不能被一个通用成功标记替代。 |
| 诊断 | 提供稳定机器标识、严重程度、所属阶段、对象或位置、说明和可用证据；恢复提示有事实依据。 |
| 范围与来源 | 指明实际覆盖范围；复用观测或验证结果时，保留适用身份、来源及必要的时间信息。 |
| 证据入口 | 文件位置、日志或 artifact 引用应可解析，并说明相关版本、路径基准及必要的保留边界。 |

机器标识用于分支判断，说明和恢复提示允许改写；上游错误码与 CLI 自身的错误标识分开表达。
普通跳过、缓存未命中和合法空集合按领域语义呈现，不自动升级成 warning 或 error。
集合为空表示已知无结果，读取失败或未检查必须另行表达；缺失、null 和空集合不能混用。

批量操作保留成功和失败的逐项结果及汇总。分页、预算耗尽或输出截断时，必须说明不完整的范围，
并在可用时提供继续读取的游标或完整 artifact；展示截断与分析本身不完整分别表达。
完整测试日志可存为 artifact，默认摘要优先暴露失败 Component、检查范围、执行或复用情况和耗时。
证据不包含凭据，不能为了缩短输出丢掉影响结果判断的诊断。

## 耗时与 validation 成本

Depot 的 `cost.phases` 提供 observe、lock_wait、install、verify、receipt，逐 Bin 结果与完整语义见 [Depot](depot.md)。

所有 JSON 结果的 `cost.wallSeconds` 都以秒计量，从 CLI 执行入口开始，到准备序列化最终结果时结束；
包含参数处理和命令执行，不包含最终结果编码、文件/终端写出及日志关闭。失败也保留已经测得的耗时。
它不是 CPU 时间、费用或整台机器的资源消耗。

validation 还提供 `cost.phases[]` 和 `cost.validation`：

| 字段或阶段 | 测量边界 |
|---|---|
| `phases[].name=planning` | 仅 `--plan` 的规划调用；不执行检查。 |
| `phases[].name=validation` | 普通执行的整个工具包验证调用，包含内部规划、format 后重规划、检查、复核及历史读写。 |
| `phases[].name=observation` | 验证后的仓库状态观察。 |
| `phases[].durationSeconds` | 该阶段的实测墙钟耗时；未进入的阶段不出现。 |
| `validation.executionSeconds` | 本轮检查执行调用的耗时之和，包含启动准备及失败尝试。 |
| `validation.lookupSeconds` | 本轮各检查复用查询耗时之和。 |
| `validation.steps[]` | 每个 preparation/check 的 `component`、`check`、`status`、`scope`、`reused`、`executionSeconds`、`lookupSeconds` 和报告 `evidence`。 |

复用检查的 `executionSeconds` 为 0，仍记录本次 lookup；历史原执行耗时保留在
`data` 中对应 CheckResult 的 `durationSeconds`，不能再次计入本轮成本。未执行的检查也保留状态，
不能把 0 秒解释为已成功执行。`--history` 查询中的累计统计归 `data`，不计入本次执行成本。

检查可能并发，因此累计 execution/lookup 与 wall 不能相减推算“CLI 开销”，也不能将父阶段与子检查
相加当成总耗时。目前没有把 validation 内部规划、锁等待、扫描等进一步拆开；缺少测量不报告为 0。

文本验证结果先展示判定和成本，再列出每个 Component/check 的 scope、执行/复用及耗时。
有 Go JSON 事件时，检查的 `testTimings[]` 额外提供 package/test/subtest 耗时，文本展示有界的慢项。
它们是可重叠的 runner 观测，不能累加到 command/check 成本；Go `cached` 与整轮检查 `reused`
分别标记历史来源。字段和提取边界见 [测试耗时](validation.md#本仓-go-测试选择与耗时)。
只展示失败检查日志的尾部，每个 stdout/stderr 最多 20 行、2000 字符；展示截断有明确标记。
JSON stdout 与 `--output` 导出结果的 `data` 均保留已捕获的检查输出；
执行器自身的输出预算与超限语义见 [验证契约](validation.md#执行与预算)。

## 退出状态与恢复

退出码提供粗粒度控制信号，稳定错误标识和领域结果提供详细原因。0 表示命令声明的成功条件成立；
执行失败、要求未满足、批量部分失败及结果未知使用非零退出码，并保留已获得的结果。
退出码为 0（命令成功）、1（执行失败、领域条件不成立或结果未知）、2（CLI 参数错误）。
分析命令成功产出带缺口的报告时仍可 `outcome=succeeded`、退出 0；覆盖范围由 `data.complete`、
`data.scope` 与 diagnostics 表达。它不等于全仓分析完整，也不授权省略验证。
validation 要求整轮完整通过且输入一致；stats 要求统计完整；worktree clean 要求没有维护错误。

| 场景 | 应呈现的结果 |
|---|---|
| PR 创建成功，后续维护失败 | 保留 PR 身份和创建成功事实，维护问题单独报告；不能建议重做已成功的创建。 |
| 多 Component 验证部分失败 | 返回每项检查及整轮判定，保留已通过检查，退出非零。 |
| 远端写入超时，无法确认是否生效 | 结果未知，提供可核验的目标身份；先查询事实，再决定是否重试。 |
| 查询完成且没有匹配对象 | 返回合法空结果；是否满足命令条件由该命令契约定义。 |

恢复提示不能仅凭 timeout 或 network 分类声明可安全重试；需要考虑是否已产生副作用、操作是否幂等，
以及是否存在可核验的操作身份。记录或展示失败不得抹去已确认的业务动作结果。

## 稳定性与验收

JSON 结果只有一种结构；`version --json` 的版本位于 `data.version`。
validation 的 `--plan`、`--history` 默认使用 JSON，参数错误和前置执行错误也遵循同一结构。
`view` 不提供最终 JSON 模式，传入 `--json` 返回结构化参数错误。
`--output` 保存最终结果，不提供另一套原生报告导出格式。SDK 返回领域对象，领域对象不依赖 CLI envelope。

兼容新增允许消费方忽略未知字段，不兼容修改升级 schema 主版本；
未知枚举值不能按成功解释。稳定机器标识、字段语义、路径基准、时间和耗时单位都属于契约。
JSON 模式不受颜色、终端宽度或本地化影响，特殊字符与文件名须无损编码。

共享输出入口负责序列化、通道和退出码映射。契约测试至少覆盖成功、参数错误、执行失败、部分失败、
合法空结果、结果未知、截断以及输出写入失败；检查字段、通道和副作用事实，不只匹配提示文案。
字段选择和实时事件流按实际消费需求增加，不作为统一输出的前置条件。

## 设计参考

- [Lark CLI 错误契约](https://github.com/larksuite/cli/blob/main/errs/ERROR_CONTRACT.md)：稳定错误分类、说明文字和批量部分失败的边界。
- [OpenCLI 输出实现](https://github.com/jackwener/OpenCLI/blob/main/src/command-output.ts)：统一结果通道及结果未知的表达。
  [renderOutput](https://github.com/jackwener/OpenCLI/blob/main/src/output.ts) 的 elapsed 用于表格页脚，JSON 只输出结果 envelope；不能据此推断它有统一的 JSON 阶段成本。
- [npm timing](https://github.com/npm/cli/blob/latest/docs/lib/content/using-npm/logging.md)：命名 timer 和独立 timing 文件，时间单位为毫秒。
- [Terraform 机器可读 UI](https://developer.hashicorp.com/terraform/internals/machine-readable-ui)：操作事件携带 elapsed_seconds，过程与终态分开。
- [ripgrep 汇总实现](https://github.com/BurntSushi/ripgrep/blob/master/crates/core/main.rs)：JSON summary 区分累计搜索统计与 elapsed_total。
- [Terraform JSON 格式](https://developer.hashicorp.com/terraform/internals/json-format)：格式版本与兼容规则。
- [Cargo 外部工具协议](https://doc.rust-lang.org/cargo/reference/external-tools.html)：过程事件、终态与子进程输出边界。

### Discussion 操作

`pr discussion list/show/create/reply/resolve` 使用同一 envelope。`data` 保留 PR 身份、
thread/comment 的独立 ID、完整正文及平台能力；具体字段和命令见 [PR discussions](pr.md#pr-discussions)。
读取失败不输出已知空数组；写入响应不能确认结果时 outcome 为 unknown，不能从退出码推断未生效。

Release 查询与写入的 `data`、确认状态及 `release.*` 阶段成本见 [Forge Releases](forge-releases.md)。

## Worktree ensure

`worktree ensure` 的成功条件是 `data.confirmed=true`，action 为 created、resumed 或 reused。
状态发布失败是 warning；创建后无法核验为 unknown。阶段耗时和完整恢复边界见
[worktree 创建与复用](worktrees.md#创建与复用)。

### Rebase

`git rebase` 返回 checkout、action、phase、transaction、headSha、conflicts、confirmed、
writeAttempted、pushAttempted、diagnostics 与 phases。`paused` 是已确认的暂停，不等于完成；
`finish` 的成功条件是远端分支匹配固定提交。中断后未确认的写操作返回 unknown/uncertain，
记录发布失败保留已确认 Git 结果并附 warning。阶段耗时映射到 `cost.phases` 的 `git.rebase.*`。
详细恢复和 status 时效契约见 [rebase](git-rebase.md)。

`git branch create/status` 分别暴露 `branchConfirmed`、`changesRestored`、`confirmed`、
精确 `stashOid` 和 `recoveryPath`。创建分支成功但恢复失败时，不能只凭非零退出码推断没有写入；
消费规则见 [Branch 创建](git-branch.md)。`cost.phases` 的阶段名以 `git.branch.` 开头。
