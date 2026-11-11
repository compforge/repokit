# RepoKit

[中文](README.zh-CN.md)

**Repository understanding and operations, available as libraries and a CLI.**

RepoKit is the project and library name; `repocli` is its command-line application.
Go imports `github.com/compforge/repokit/toolkit/go` as `repokit`, Node.js uses
`@compforge/repokit`, and Python installs `compforge-repokit` and imports `repokit`.

A toolkit for understanding and operating on repositories, with Go, TypeScript and Python APIs and a CLI for
developers, scripts, and coding agents. Its scope covers repository layout and components, code and changes,
Git state and operations, and build/package tools and manifests. Available capabilities vary by language:
the libraries currently provide layout discovery, content identity, tool evidence, change analysis, code graphs,
and Git/Forge operations. The CLI exposes selected capabilities and a local graph browser; callers own workflow decisions.

The `repocli` application exposes repository queries and explicit validation:

| Command | Use it to |
|---|---|
| `tree` | List directories, files and known roles without requiring components |
| `inspect` | Discover components, languages and package-tool evidence |
| `snapshot` | Identify repository contents and capture completeness |
| `diff` | Capture changes and group related fragments into units |
| `impact` | Explain possible file and test impact |
| `format` / `lint` / `test` / `validate` | Plan and execute Component Makefile targets |
| `view` | Browse the code graph and its captured source |

## CLI installation

Requires Go 1.26+ and Git. Install from source:

```sh
git clone https://github.com/compforge/repokit.git
cd repokit
make install
```

Tagged [releases](https://github.com/compforge/repokit/releases) provide macOS/Linux archives
and checksums. Use `repocli version` or `repocli --version` to identify the installed
binary; `repocli version --json` produces structured output. Both source and release
builds inject the version from the repository’s `VERSION` file.

`make install` uses `go install`: the binary goes to `GOBIN`, or `$(go env GOPATH)/bin`
when `GOBIN` is unset. Ensure that directory is on `PATH`. Use `make -C apps/cli build` to produce `bin/repocli`. Root `make build` builds all
components and requires Node.js, Python/uv and installed toolkit dependencies.

## CLI updates

```sh
repocli upgrade --check          # discover the latest stable release
repocli upgrade --check --json   # structured result for automation
repocli upgrade                  # verify and install it
```

Self-upgrade supports the published macOS/Linux architectures and changes only the
CLI executable. It checks the archive against the release's SHA-256 checksums before
an atomic replacement; a failed download or verification leaves the installed binary
unchanged. The install directory must be writable. For package-manager-owned
installations, use that package manager's upgrade command instead.

`version` stays offline; analysis never checks for updates or upgrades itself.
Library dependencies are updated through their language package managers.
See [update behavior and output](docs/upgrade.md).

## Go API

Import `github.com/compforge/repokit/toolkit/go` to run repository analysis in your process:

```go
report, err := repokit.Inspect(ctx, repokit.InputRequest{Repository: repoPath})
if err != nil {
    return err
}
if !report.Complete {
    // Decide how to handle report.Diagnostics before using partial organization.
    return fmt.Errorf("repository inspection incomplete: %v", report.Diagnostics)
}
component := report.Owner("server/main.go") // nil when no component owns the path
```

`Tree` returns repository entries; `Inspect` returns organization; `Snapshot` returns content identity; `Diff` reports
changes and possible impact; `Graph` returns nodes, relations and their captured source.
Pass a context with the required deadline. Calls may invoke Git, but do not launch the
repocli binary or write CLI logs/history. Analysis does not run project commands;
explicit Makefile validation does. Results retain completeness
and diagnostics so the caller can choose its policy. See [toolkit responsibilities and boundaries](AGENTS.md).

The Go toolkit lives in `toolkit/go`; the CLI is a separate module in `apps/cli`.
Use the repository workspace for joint development. See [build and release details](docs/release.md).

## TypeScript API

`@compforge/repokit` provides native repository inspection for Node.js 22+:

```typescript
import { inspect, owner } from "@compforge/repokit";

const report = await inspect({ repository: repoPath, timeoutMs: 5_000 });
if (!report.complete) throw new Error(JSON.stringify(report.diagnostics));
const component = owner(report, "server/main.go");
```

It calls Git directly and reuses quality-harness common identities. It requires no repocli
binary or Go runtime. See [TypeScript setup and scope](toolkit/typescript/README.md); native working-tree snapshots, Git queries and read-only GitHub/GitLab clients are also available. Diff and graph
analysis are provided by the Go API.

## Python API

The native Python toolkit provides tree/organization queries, working-tree snapshots and Git/Forge operations for Python 3.11+ on macOS/Linux:

```python
from repokit import inspect, owner

report = inspect("/path/to/repo", timeout=5)
if not report.complete:
    raise RuntimeError(report.diagnostics)
component = owner(report, "server/main.py")
```

Install from `toolkit/python`; see [Python setup and scope](toolkit/python/README.md).
It requires Git on PATH, shares common identities and uses the same inspection contract.

## Deadcode candidates

`repocli deadcode --json` builds a graph of all captured text documents and reports
declaration nodes with no incoming usage edges. The Go API is `AnalyzeDeadcode`.
Results retain snapshot identity and graph diagnostics; public APIs and entrypoints
may appear. See [rule and limitations](docs/deadcode.md).

## Usage

`diff` reports captured paths, patches and before/after source. Add `--units` to form related Units.
`impact` reports changed declarations, potentially affected files and component context.
Declaration discovery follows CodeGraph language capabilities;
dependency-aware test selection supports Go, Python, JavaScript, and TypeScript.

```sh
repocli diff --repo /path/to/repo --base main --json
repocli impact --repo /path/to/repo --base main --test-dir tests --json
```

Omit `--json` for readable text. For `impact`, repeat `--test-dir` for multiple directories;
use `--test-dir .` for tests alongside source files. Production source scope is limited to Components owning diff paths. Tests are discovered independently: by default in those Components and repository-level test locations, or under explicit test directories. Referenced test helpers may join the workset without selecting additional production Components. Empty test candidates are valid.
`--base` defaults to `HEAD` and compares that commit with the working tree.

Test impact is best effort: known-target inferred edges participate in recommendations,
with confidence and basis retained on explanation edges. Granularity is automatic; each
affected file reports its seed, path confidence and dependency distance. Local extraction
gaps remain observations; unknown targets do not create edges. An empty
test list does not prove that no tests are affected. Callers decide how to use the result. Analysis
does not run project commands. See [diff usage](docs/diff-usage.md) for patch input,
output fields, and analysis limits.

`view` opens a local web server for the working tree's code graph:

```sh
repocli view
repocli view --repo /path/to/repo --addr 127.0.0.1:5484
```

Open the printed URL to explore documents, search symbols, filter relationships by
kind and confidence, and inspect captured source and diagnostics. Use **Refresh snapshot**
after edits. Cytoscape.js and all page assets are embedded; no Node.js, CDN, or database
is needed at runtime. See [graph viewer](docs/view.md) for scope and limits.

`inspect` describes repository organization without reading source contents or hashing the repository:

```sh
repocli inspect --json
repocli inspect --staged --json
repocli inspect --head HEAD --json
```

It reports repository identity, component roots, languages and package-tool evidence from the selected
input. Components provide the granularity for lint, test and packaging operations; callers choose and
run those operations. Discovery prefers project manifests such as `go.mod` and `package.json`, then
Makefiles with unknown language. Without either, the repository has no recognized components.
No RepoKit-specific configuration file is required or generated.
See [repository inspection](docs/repository.md) for discovery rules and limits.

`snapshot` identifies repository contents without running change or impact analysis:

```sh
repocli snapshot --json
repocli snapshot --staged --json
repocli snapshot --head HEAD --json
```

Its digest uses the same rules as `diff`; check `complete` before comparing it.
Snapshot schema 2 reports content identity only. Consumers of its former component fields should use `inspect`.
See [snapshot usage](docs/snapshot.md) for scope and limitations.

Analysis commands and invalid invocations automatically log replay arguments, version, elapsed time and exit status
to `~/.repokit/logs/YYYY-MM-DD.log`, keeping today and the previous 29 days.
Each recorded run has a `run_id`; output destinations stay unchanged.
Successful help/version commands do not initialize or write log files.
Diff analyses also append comparison inputs and stage timings to `diff-YYYY-MM-DD.jsonl`
in the same directory. See [execution logs](docs/logging.md).

## Local statistics

`repocli stats` summarizes retained validation execution/reuse across repositories,
command and analysis timings, and local storage. Use `--json` for structured output,
`--top 10` for slow runs, or `--home` for an alternate state root.
It does not execute checks or prune records. See [local statistics](docs/stats.md)
for counting rules and partial-report semantics.

## Makefile validation

Projects can expose Component checks through documented Makefile targets:

```sh
repocli validate --component toolkit/go --check format,lint,test --timeout 10m
repocli test --component toolkit/python --file toolkit/python/tests/test_inspect.py
repocli validate --base HEAD --plan
```

`format`, `lint` and `test` run independently; `validate --check format,lint,test` formats first,
then replans and runs bounded checks through the Go toolkit. Planning is read-only;
execution uses the frozen commands, scopes and environment, then verifies that inputs
stayed unchanged. Missing lint/test targets fall back to the root Component; requests
for the same execution Component/check are merged. If root also lacks the target, it
is unavailable. Unsupported file filtering falls back to a full Component check. See the [Makefile contract and validation API](docs/validation.md).

## Repository toolkit

工具包提供仓库组织、内容身份与 Git 查询。Python 还提供 worktree、提交、推送、rebase 和
GitHub/GitLab 的 PR/MR 操作；调用方直接导入库，组织自己的开发流程。
分析接口保持只读，操作接口接收明确参数，不隐式执行验证或管理任务状态。

详见 [Git 操作](docs/git.md)与[PR 契约](docs/pr.md)。

### Inspect change units

`repocli diff --units --max-units 8` shows how a diff becomes source fragments and related units.
Use `--json` for structured output. The Go library exposes the same analysis; see [Fragment and Unit](docs/units.md).

## Worktree cleanup

`repocli worktree clean --dry-run` previews repository retention; omit `--dry-run` to apply it.
Configure `worktree.max_count` and `worktree.max_prepared` in the shared Git metadata's
`repokit.json`. Unknown creation times count as oldest. The Go command requires only Git. See [policy, protection and PR integration](docs/worktrees.md).


## Pull requests

`repocli pr create/show/list/update/close/merge` uses native Go GitHub/GitLab clients.
Run `repocli validate` before committing/pushing code and submitting its PR; validation is explicit.
Create requires `--title`, `--base` and optionally `--body-file`; it reuses an unambiguous open PR.
Merge requires `--expected-sha`. Successful writes perform best-effort repository maintenance.
See [PR operations](docs/pr.md#go-cli-and-toolkit).
