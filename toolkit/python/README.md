# RepoKit for Python

Native organization inspection for Git repositories: discover Components, language and
tools, Depot declarations and tool evidence, then locate the owner of a repository-relative path.
Python 3.11+ on macOS/Linux and Git on PATH are required. Organization analysis runs
in Python; Git provides version-control facts through batched commands.

Install from the repository checkout:

```sh
python -m pip install ./toolkit/python
```

```python
from repokit import inspect, owner

report = inspect("/path/to/repo", timeout=5)
if not report.complete:
    raise RuntimeError(report.diagnostics)
component = owner(report, "server/main.py")
if component is not None:
    print(component.name, component.root, component.language)
```

`inspect(..., staged=True)` selects the index; `inspect(..., head="HEAD")` selects a
resolved commit. The default observes the working tree, including non-ignored untracked
paths. `timeout` is a whole-call budget in seconds; an optional `threading.Event` passed
as `cancel` requests cancellation. Git processes are terminated and reaped on timeout,
cancellation or output overflow. Filesystem reads and Python analysis check the budget
between operations. Timeout raises `TimeoutError`, cancellation raises `InterruptedError`,
invalid selections/configuration or limits raise `ValueError`, and filesystem/Git failures
propagate `OSError` / `subprocess.CalledProcessError`.

Reports and their nested collections are immutable dataclasses and tuples. Shared
Repository, Component, Product and Forge identities come from `harness-common`;
ComponentBinding adds checkout layout and tool evidence. `owner` accepts a repository-relative
path and returns the deepest binding or None, including for deleted paths.
Python properties use snake_case; these objects are not a CLI JSON protocol adapter.

Inspection reads filenames and necessary metadata, not source contents or ASTs. It never
runs project code or installs dependencies. `complete` concerns observed organization;
concurrent metadata changes return an incomplete report with diagnostics. Consumers own
acceptance, command selection and validation policy. Snapshot, diff and graph analysis
remain available through the Go toolkit / CLI.

Development: `uv sync --locked`, then `make format`, `make lint`, `make test`, `make build`.
Tests consume the shared `conformance/inspect` cases and generated filename probes.
See [the inspection contract](../../docs/repository.md).

## Repository toolkit

工具包提供仓库组织、内容身份与 Git 查询。Python 还提供 worktree、提交、推送、rebase 和
GitHub/GitLab 的 PR/MR 操作；调用方直接导入库，组织自己的开发流程。
分析接口保持只读，操作接口接收明确参数，不隐式执行验证或管理任务状态。

使用已安装的 RepoCLI 时，`repokit.cli.create_commit` 消费已规划的 branch、parent、tree 和 index
指纹，确认提交和 index 安装完成。`repokit.cli.push_commit` 推送指定 SHA 并核对远端分支。
操作结果无法确认时，`RepoKitError.hint` 提供针对原提交或目标分支的只读核对命令。

详见 [Git 操作](../../docs/git.md)与[PR 契约](../../docs/pr.md)。

## Repository entries

`tree` lists directories, files, Git entry kinds and known roles independently of
Component discovery, for working-tree, index and commit inputs. Project Manifest
files retain their ecosystem; Component bindings retain `manifests` as path evidence.
Components provide the granularity for lint, test and packaging operations. Discovery prefers
manifests, then Makefiles with unknown language. Without either, the repository has no
recognized Component; no root Component or tool-specific configuration file is created.
See [repository contract](../../docs/repository.md) and [snapshot v2](../../docs/snapshot.md).

### Component content identities

```python
from repokit import component_scopes, inspect, snapshot

layout = inspect(repository)
contents = snapshot(repository, scopes=component_scopes(layout))
for component in contents.scopes:
    print(component.name, component.digest, component.complete)
```

Verify layout/content consistency in the calling operation. These scopes follow file
ownership, not transitive build dependencies; see [Snapshot](../../docs/snapshot.md).

## Makefile validation

```python
from repokit import plan_validation, execute_validation

plan = plan_validation(repo, components=("backend",), checks=("lint", "test"))
report = execute_validation(plan, timeout=600)
# Use report.complete and report.passed together, not an isolated command exit code.
```

Planning never executes Make. Targets and optional LINT_FILES/TEST_FILES filtering follow
[the shared Makefile contract](../../docs/validation.md). Prepare dependencies and run any
formatter before planning. Execution freezes the environment, bounds subprocesses, and
checks repository contents once before and once after the round. Reports contain execution
facts; callers own persistence, reuse and workflow gates.

PR lifecycle queries are native Python: `repokit.forge.PullRequestObserver` owns durable observations,
terminal-state convergence, retries and optional periodic maintenance. `PullRequestContext` relates PRs
to live local branches and checkouts without choosing workflow or cleanup policy. See [PR contracts](../../docs/pr.md).


PR 创建和复用使用 `repokit.forge.ensure_pull_request`，无需调用方解析目标仓或默认分支。
Git 提交与推送使用 `repokit.git.commit` / `push`；提交范围、验证门禁和动作顺序仍由调用方负责。
完整契约见 [PR](../../docs/pr.md#创建与复用) 与 [Git](../../docs/git.md)。
