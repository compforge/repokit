# Forge Releases

Go `forge.Client.Releases()` 提供 `release.Service`，通过已有 Forge transport 共享连接、认证、
响应容量限制和限流冷却。CLI 复用 RepoKit home 配置，不读取调用方凭据，不触发 PR/worktree maintenance。
Release 操作不创建本地 tag，不要求本地存在目标 commit；目标由所选远端 Repository 解析。

```sh
repocli release latest --repo . --json
repocli release show toolkit/go/v1.2.3 --repo . --json
repocli release create toolkit/go/v1.2.3 --target <remote-commit-or-ref> --notes-file notes.md --repo . --json
```

`--target-repository` 可指定同 Forge 的一个目标仓库，省略则使用 checkout 的 origin 身份。
`--target` 必填，不猜默认分支；`--title` 默认 tag；`--notes-file -` 读取 stdin，正文原样保留。
CLI 要求显式 notes 文件，允许空文件；notes 的产品意义与非空要求归项目发布策略。
库输入是 `CreateRequest{Tag, Target, Name, Notes}`，不解释 SemVer、递增、模块顺序或生成 notes。
包发布、部署、CI 等待和 release assets 上传属于独立工作流；本次 create 发布普通 Release，
不提供 draft/prerelease/upcoming 创建选项。

## 查询

`Show` / `Latest` 返回 `QueryResult{repository, release, selection, phases}`。
成功的 `release: null` 表示在可访问仓库中未找到；资源 404 后还须确认仓库可访问。
认证、访问失败、响应结构错误和 tag 无法解析均为错误，不能冒充“尚未发布”。
Release 保留 tag、name、完整 notes、URL、publishedAt、draft/prerelease/upcoming 和 targetCommit。
`targetCommit` 来自实际 tag 的 commit，不使用 GitHub `target_commitish` 冒充 SHA。
GitHub annotated tag 最多剥离 16 层，非 commit 对象、缺失对象或不完整响应均不接受。

`selection` 明确平台选择语义：

- `tag`：指定 tag 的 Release。
- `github_latest_published`：GitHub latest API 的已发布 Release，排除 draft/prerelease。
- `gitlab_released_at`：GitLab latest permalink，依据 released_at。

这些结果均不是“最大 SemVer”；选择项目版本线、处理 upcoming 和比较版本由调用方决定。
平台依据见 [GitHub Releases](https://docs.github.com/en/rest/releases/releases)、
[GitLab Releases](https://docs.gitlab.com/api/releases/#get-the-latest-release)。

## 创建与恢复

`PublishResult` 包含 repository、tag、requestedTarget、固定后的 targetCommit、observedTagCommit（本次阶段观察到的 tag commit）、最终观察到的 release、
confirmed、writeAttempted 和 phases。writeAttempted 表示进入写请求，不证明服务端收到或执行了写入。

1. prepare：在目标 Forge 仓库解析显式 target，固定完整 commit ID。
2. preflight：查询 Release 和 tag。已有 Release 的 commit、name、notes 与普通发布状态完全一致时
   返回 confirmed=true、writeAttempted=false；其他情况报告 conflict。仅有 tag 时也必须指向目标 commit。
3. write：最多一次 POST，以固定 commit 创建 Release；不更新、覆盖或删除已有 tag/Release。
4. verify：重新查询同一仓库、tag 的 Release 和实际 tag commit，只有内容和状态一致才 confirmed=true。

写入成功、超时或返回错误后均尝试核验。实际已满足时可以恢复为成功，不声称本次一定创建了它。
确定的 4xx 拒绝且成功观察到 Release 不存在时返回确定失败；未确认的写入、核验失败、取消或
预算耗尽返回 `forge.release_outcome_unknown` / `error.uncertain=true`，不自动重试。
观察到不同 Release/tag 时返回 conflict 并保留结果；写入后的 conflict 可能伴随已产生的远端对象。
Forge 不提供 tag 检查与 Release 创建之间的跨资源 CAS，因此并发变动只能在最终核验时发现，
RepoKit 不自动补偿或回滚他人的远端资源。

调用方遇到 unknown，执行同仓库的 `release show <tag>`，比较实际 commit、name、notes 和发布状态，
再决定后续动作。只有明确的缺失与已解决的失败原因才支持重新尝试；无输出不能当作未写入。

每个 HTTP 请求沿用共享 transport 的 10 秒上限；调用方 context 的更短预算优先。
write 为 verify 保留剩余总预算的 20%（最多 10 秒），取消后不创建后台核验。
phases 记录实际执行阶段的秒数，CLI 映射到 `cost.phases` 的 `release.*`；阶段不执行就不伪造耗时。
