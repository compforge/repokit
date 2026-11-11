# Go 错误契约

`toolkit/go/errs` 拥有公共 `Error` 类型及 JSON/text 表达。SDK 通过标准 `error` 返回它，
CLI 直接将它放入结果的 `error` 字段，文本模式直接调用 `Text()`；不建立另一套错误 DTO。
包本身不读取参数、不选择 stdout/stderr、不输出日志、不决定进程退出码或自动重试。

## 字段与表达

| 字段 | 语义 |
|---|---|
| `code` / `message` | 稳定机器标识与可读说明；机器不解析 message 分支。 |
| `op` / `subject` | 失败的操作及目标；没有目标时省略 subject。 |
| `uncertain` | 已启动的写入可能生效，不能把错误解释为“没有副作用”。 |
| `httpStatus` / `retryAt` | 可用的上游 HTTP 状态及限流解除时间；retryAt 使用 RFC3339 时间。 |
| `hint` | 有事实依据的恢复提示，不等于授权自动重试。 |
| `Cause` | 仅进程内保留，`Unwrap()` 支持 `errors.Is/As`；不单独序列化或递归展开。 |

`Error()` 返回说明文字，供普通 Go 错误链组合；`Text()` 展示 code、操作、目标、说明、
不确定性及可用上游信息和提示。JSON 通过标准 `encoding/json` 序列化同一对象，遵循字段标签；
不包含独立的 cause 字段、stack、退出码或 CLI envelope；message 仍可包含错误链中明确提供的说明文字。

```go
var problem *errs.Error
if errors.As(err, &problem) {
    encoded, encodeErr := json.Marshal(problem)
    // 调用方处理 encodeErr 并决定 encoded 的输出位置。
    text := problem.Text()
    // 调用方决定 text 的输出位置。
}
```

## 产生、包装与消费

各业务组件负责确定领域错误码和上下文；Forge 在处理响应时确定 HTTP 错误、限流及写入不确定性。
Repository 分析、validation 和 state 的操作边界为未分类错误补充所属操作与目标。
`errs.Wrap(err, code, op, subject)` 对 nil 返回 nil；已结构化的错误保留原 code 和已知上下文，
只补充空字段，并保留外层包装文字和完整 cause 链，不修改共享错误对象。
标准取消、deadline、文件权限/缺失及网络错误依据 `errors.Is/As` 分类，其余使用显式传入的 code。

错误的 op/subject 描述已知失败点，外层调用不能覆盖更具体的上下文。例如 Forge 读写请求失败时，
保留 HTTP 方法及 API 路径；上层 create 操作不能将它替换成无目标的通用 execution。
调用方需要具体 code 时，应由拥有该失败语义的组件直接构造 `Error`，而非依赖文字解析。

CLI 使用 `uncertain` 呈现 `outcome=unknown`，将参数错误映射为退出码 2，其余执行错误为 1；
这些是 [CLI 输出契约](cli-output.md) 的规则，不进入共享 Error。错误输出失败也由 CLI 提供
`output.write_failed` 等自身错误码，并使用共享 Text 表达。

测试失败、部分扫描、维护跳过等属于领域结果，继续由 CheckResult、报告和 diagnostics 表达，
不把所有非成功状态转换为异常。调用方同时检查返回结果与 error，保留已完成动作的证据。
