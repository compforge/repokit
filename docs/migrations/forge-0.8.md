# Forge API 迁移：TypeScript 0.8 / Python 0.10

本页记录历史版本迁移；TypeScript 0.24 / Python 0.29 的 Comment 契约更新见 [PR comments](../pr.md#pr-comments)。

这是 pre-1.0 的不兼容接口更新。部署前先适配消费方；不要仅更新依赖锁文件。

| 原接口或字段 | 新接口或字段 |
|---|---|
| `ForgeReader.get(key)` | `getPullRequest(key)`，只返回 common snapshot |
| get 结果里的评审字段 | 显式 `getPullRequestReview(key)`；错误独立处理 |
| `reviewThreads` | `reviewThreadState`，仅指聚合状态，不是线程列表 |
| `reviewActivityKey` | `reviewActivityFingerprint`，内容摘要，不是对象身份 |
| `PullRequestObservation` | 基础事实用 common `PullRequestSnapshot`，评审用 `PullRequestReviewObservation` |
| `list(path, {state, limit})` | `listPullRequests({scope: {kind: "repository", repository}, state, pageSize})`；返回 Page，不是有数量上限的数组 |
| `discover({author, ...})` | `listPullRequests({scope: {kind: "account", author}, ...})` |
| `readPullRequests({author, since, ...})` | `scanPullRequests({scope: {kind: "account", author}, initialSince, ...})` |
| `PullRequestReadQuery/Result`、`PullRequestCursor` | `PullRequestScanQuery/Result/Cursor` |
| `PullRequestListQuery` / `PullRequestDiscoveryQuery` / `PullRequestPage` | `PullRequestQuery` / `Page<PullRequestKey>` |
| `errors: string[]` | `issues: ForgeReadIssue[]`，检查 stage/kind 和 key/window |
| `comments(key)` | `listPullRequestComments(key)` |
| `PageQuery.limit` | `pageSize`（默认 50，上限 100）；不是遍历总量上限 |
| `ForgeReaderConfig.uids` | 移除；每次查询显式传 author username，不再混用数字 ID |
| `ForgeDiscoveryOverflowError` | `ForgeCoverageError` |
| `ForgePort` | `RepositoryForge`，仍是仓库绑定的工作流接口，不是 ForgeReader 的别名 |
| `Comment` | 内容用 `ForgeComment`；写 adapter 返回 `RepositoryForgeComment` |
| 评论顶层 replyRef / resolveRef | `comment.target.replyRef / resolveRef`；reply/resolveComment 接收 target |
| `CommentResolution` | `ForgeComment["resolution"]`；undefined 表示未观察 |
| `MergeReadiness` | common `PullRequestMergeReadiness` |
| Python `list_repositories(limit=...)` | `list_repositories(page_size=...)`；其它 Python Forge API 不变 |

逐项遍历使用 iterateRepositories / iteratePullRequests；需要 page 的 issues 或持久进度时使用单页方法。
iterator 遇到含 issues 的 PR 页会拒绝，不能把成功结束的 iterator 当作部分覆盖。
仓库页、PR 页都是页号 continuation，并非 keyset 或原子快照。查询需要完整性时仍需调用方对并发变化作处理。

## 读取与评审分离

```typescript
const snapshot = await reader.getPullRequest(key);
await saveSnapshot(snapshot);
try {
  const review = await reader.getPullRequestReview(key);
  await saveReview(review);
} catch (error) {
  // Record unavailable review separately; keep the successful snapshot.
}
```

GitHub 的非 draft readiness 来自 review 查询；不能从 conflictState=none 推断 ready。
Review 查询失败直接抛错，不再返回 reviewError 或带未知指纹的合并对象。
RepositoryForge 写 adapter 需要把已有句柄移入 target，补 replyable，保留已有 resolution；
普通 id、正文和 replies 继续是统一的 ForgeComment 事实。TS 包没有内置写 adapter，消费方需一同适配。

## 检查点升级

所有旧 v1 分页及扫描 cursor 都拒绝复用。分页重新从第一页开始；旧扫描的私有字段不能手工转换。
恢复时保留业务已接纳记录、已有 snapshot 基线与 tracked keys，不把历史重新扫描误当作新事件。
设置 initialSince 到覆盖升级空档的时间（需要补查的历史长于默认七天时显式扩大），不传旧 cursor。
首次重新枚举 open 清单，并扫描历史窗口；使用 common pullRequestId 与原有业务基线去重。
提交新 cursor 前，必须可靠消费本批所有 items。

pending 表示本轮待执行工作，complete 表示无待执行工作且无缺口。永久 404 可以使 pending=false、
complete=false；按正常轮询/退避重试，不能按 pending=false 清空失败状态，也不能因列表缺席标记 merged。
新扫描 cursor 绑定连接凭据摘要；token 轮换同样需要上述重启，不泄露原 token。

## 更早版本

0.4 之前的 PullRequestIdentity 改用 common PullRequestKey（repository + number）；webUrl→url、sha→headSha。
0.5 起事实结果为 snapshot，`pr.id()`→`pullRequestId(pr)`，仓库 `id()`→`repositoryId(repository)`。
0.6 起 mergeability 拆为 common conflictState 与 mergeReadiness，两者都不替代 state=merged。
