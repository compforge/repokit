# RepoKit for TypeScript

Inspect a Git repository directly in a Node.js process. Discover component boundaries,
languages and tools, Depot declarations and tool evidence without loading source contents or running project code.
Node.js 22+ and Git are required; the repocli executable and Go toolchain are not needed at runtime.

```typescript
import { inspect, owner } from "@compforge/repokit";

const report = await inspect({ repository: repoPath, signal, timeoutMs: 5_000 });
if (!report.complete) throw new Error(JSON.stringify(report.diagnostics));
const component = owner(report, "server/main.go"); // undefined when unowned
```

Use `staged: true` for the index or `head: "HEAD"` for a resolved commit. They are mutually
exclusive; the default is the working tree. One timeout (default five seconds) covers the
whole call. Invalid configuration, read failures, limits and cancellation reject the promise;
observed concurrent changes return an incomplete report with diagnostics.

Repository, Component and Product types come from `@compforge/harness-common`.
`owner` accepts repository-relative paths, including deleted files. The result serializes to
Go inspect's schema 1. Its completeness describes organization metadata, not buildability or
content identity. See [the shared contract](../../docs/repository.md).

This package also provides Git queries, content snapshots and read-only GitHub/GitLab Forge access.
The Go API separately provides diff and graph analysis. RepoKit owns its persistent repository state
and validation history; callers own logging destinations, validation policy and handling partial results.

## Development

Run `npm ci`, `make lint` and `make test` in this directory. Commit `package-lock.json`
with dependency changes so managed worktrees can prepare the locked installation, including
the TypeScript compiler, before validation. `make build` produces ESM
and type declarations in `dist/`. Runtime Git calls and metadata reads are bounded; tests use
isolated local repositories and need no remote service. `../../conformance/inspect` supplies the
same organization and version-selection cases to Go and TypeScript.

The dependency `@compforge/harness-common@^0.5.8` must be published before a registry install
or release. While reviewing both repositories together, build and `npm pack` the common package,
then install that tarball with `npm install --no-save --package-lock=false <tarball>`. Do not
commit local paths as dependencies.

The filename catalog is generated from the Go module's pinned CodeGraph/gotreesitter metadata.
After upgrading those dependencies, run `make generate-languages` at the repository root;
Go tests detect stale metadata and TS tests check the upstream filename probes.

## Repository toolkit

工具包提供仓库组织、内容身份与 Git 查询。Python 还提供 worktree、提交、推送、rebase 和
GitHub/GitLab 的 PR/MR 操作；调用方直接导入库，组织自己的开发流程。
分析接口保持只读，操作接口接收明确参数，不隐式执行验证或管理任务状态。

详见 [Git 操作](../../docs/git.md)与[PR 契约](../../docs/pr.md)。

## Repository entries

`tree` lists directories, files, Git entry kinds and known roles independently of
Component discovery, for working-tree, index and commit inputs. Project Manifest
files retain their ecosystem; Component bindings retain `manifests` as path evidence.
Components provide the granularity for lint, test and packaging operations. Discovery prefers
manifests, then Makefiles with unknown language. Without either, the repository has no
recognized Component; no root Component or tool-specific configuration file is created.
See [repository contract](../../docs/repository.md) and [snapshot v2](../../docs/snapshot.md).

## Forge reads

```typescript
import { createForgeReader, iterateRepositories, iteratePullRequests } from "@compforge/repokit";

const reader = createForgeReader({ forge: "github", host: "github.com", provider: "github", token });
for await (const repository of iterateRepositories(reader, { pageSize: 50 })) {
  for await (const key of iteratePullRequests(reader, {
    scope: { kind: "repository", repository, author: "octocat" }, state: "merged",
  })) {
    const snapshot = await reader.getPullRequest(key);
    // Consume common PullRequestSnapshot facts; no review request is made.
  }
}
```

Use account scope `{kind: "account", author: "octocat"}` for cross-repository discovery.
`listRepositories` and `listPullRequests` expose single pages with `items` and optional `nextCursor`.
PR pages may also contain `issues`; successful rows do not imply complete coverage. Iterators reject such pages.
Continuations currently wrap provider page numbers, not stable keyset positions. They do not provide atomic snapshots.
Iterators load lazily, stop without prefetch, and own neither durable progress nor a cross-poll cache.

`getPullRequestReview(key)` explicitly reads review state, activity fingerprint and available merge readiness.
`listPullRequestComments(key)` returns conversation comments and nested replies. Errors propagate independently of
previously read snapshots. Conflict-free, ready and merged are separate facts.

For bounded polling:

```typescript
const batch = await reader.scanPullRequests({
  scope: { kind: "account", author: "octocat" }, cursor, tracked,
});
await durablyConsume(batch.items);
await saveCursor(batch.cursor);
// pending: more work in this pass; complete: no unfinished work or retained coverage gaps.
// Inspect issues even when pending is false, then apply the application's polling/backoff policy.
```

The scan checkpoint retains failed keys and discovery gaps without blocking healthy objects.
The consumer owns scheduling, snapshot baselines, durable acceptance and deduplication.
See [read and scan contracts](../../docs/pr.md#typescript-forge-读取) and
[0.8 migration](../../docs/migrations/forge-0.8.md) before upgrading existing consumers.

## Persistent PR observation

`PullRequestObserver.getPullRequest(key)` returns lifecycle status and its observation time.
The observer wraps an existing `ForgeReader` with durable state,
bounded background polling and merged-terminal convergence; hosts call `close()` on shutdown. Supply `home` and a stable access-configuration
`connection` revision; never pass the credential itself as that revision. See
[PR observation](../../docs/pr.md#持续观察) for directory layout, freshness, cancellation and recovery.

## Validation history

`listValidationRuns({repository, checkout, limit, includeInputs: true, signal})` reads retained
executions and the selected page's input evidence summaries in one locked query. It does not
read output logs. `readValidationRun(repository, runID, {signal})` explicitly reads full evidence.
The default history location is `REPOKIT_HOME` or `~/.repokit`, shared with Go and the CLI.
An absent store stays absent. Pass a signal or `timeoutMs` for a total query budget;
lock acquisition is capped at five seconds. Native `fs-ext` locking requires Linux/macOS
and native build tooling at installation.

History failures expose `ValidationHistoryError.code`, `op`, `subject` and the original `cause`.
Missing input/output evidence is reported as incomplete evidence, while corrupt history,
cancellation and timeout reject the query. Historical success and an unchanged source snapshot
do not prove current validation or reuse eligibility. Supported capabilities and error codes
are defined in [the shared validation contract](../../docs/validation.md#原生历史查询-api-与存储边界)
and tested by `conformance/history/` in both Go and TypeScript.
