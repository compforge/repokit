# RepoKit

[English](README.md)

RepoKit 是项目与工具包名称，`repocli` 是命令行应用。
Go 使用 `github.com/compforge/repokit/toolkit/go`（包名 `repokit`），Node.js 使用
`@compforge/repokit`，Python 安装 `compforge-repokit` 并导入 `repokit`。

面向 Repository 理解与操作的工具包，提供 Go、TypeScript、Python API 和 CLI，供开发者、脚本与 coding agent 使用。
职责覆盖仓库布局与 Component、代码与变更、Git 状态与操作，以及构建工具、包管理工具和 manifest。
各语言已支持能力不同，包括布局识别、内容身份、工具证据、变更分析、代码图和 Git/Forge 操作。
CLI 暴露其中的能力并提供本地代码图浏览；业务流程与决策由调用方负责。

测试推荐采用 best-effort 方式：有目标的推测关系参与推荐，解释路径保留置信度；未知目标不记录。
空测试列表不证明没有测试受影响。

`repocli` 应用提供仓库查询与显式验证入口：

| 命令 | 用途 |
|---|---|
| `tree` | 查询目录、文件和已知角色，不依赖组件识别 |
| `inspect` | 查询组件目录、语言和包工具证据 |
| `snapshot` | 判断仓库内容身份及捕获完整性 |
| `diff` | 捕获改动，并将相关 Fragment 组装为 Unit |
| `impact` | 分析可能受影响的文件与测试 |
| `format` / `lint` / `test` / `validate` | 规划并执行 Component 的 Makefile 目标 |
| `view` | 交互浏览代码图和对应源码 |

## CLI 安装

需要 Go 1.26+ 和 Git，从源码安装：

```sh
git clone https://github.com/compforge/repokit.git
cd repokit
make install
```

标签 [Release](https://github.com/compforge/repokit/releases) 提供 macOS/Linux 压缩包及校验和，
用 `repocli version` 或 `repocli --version` 查看已安装版本，`repocli version --json` 输出 JSON。
源码构建与发布版都使用构建时注入的根目录 `VERSION`，运行时不依赖仓库文件。

`make install` 使用 `go install`，安装到 `GOBIN`；未设置时安装到 `$(go env GOPATH)/bin`。
确保该目录在 `PATH` 中。仅构建 CLI 可用 `make -C apps/cli build`，产物位于 `bin/repocli`。根 `make build` 构建全部组件，需要先安装 TS 包依赖。

## 浏览代码图

```sh
repocli view
repocli view --repo /path/to/repo --addr 127.0.0.1:5484
```

打开命令打印的本地 URL，可查看 Document 概览、搜索符号、筛选关系类型与 confidence，
并查看对应快照的源码和诊断。修改代码后点击 **Refresh snapshot** 重新捕获。
Cytoscape.js 和页面资源内嵌在 Go 二进制中，运行时无需 Node.js、CDN 或数据库。
范围与限制见 [代码图浏览](docs/view.md)。

## CLI 更新

```sh
repocli upgrade --check          # 查询最新稳定版
repocli upgrade --check --json   # 输出供自动化读取的结果
repocli upgrade                  # 校验并安装
```

自升级支持已发布的 macOS/Linux 架构，只更新 CLI 可执行文件。下载后按 Release 的
SHA-256 校验文件验证，再原子替换；下载或校验失败保留原安装。安装目录需要可写，
由包管理器安装的副本应使用对应包管理器更新。`version` 保持离线，分析命令不检查或安装更新；
各语言 library 通过包管理器更新。详见 [更新契约](docs/upgrade.md)。

## Go API

导入 `github.com/compforge/repokit/toolkit/go`，在调用方进程内分析仓库：

```go
report, err := repokit.Inspect(ctx, repokit.InputRequest{Repository: repoPath})
if err != nil {
    return err
}
if !report.Complete {
    return fmt.Errorf("仓库结构观察不完整：%v", report.Diagnostics)
}
component := report.Owner("server/main.go") // 无所属组件时返回 nil
```

`Inspect` 提供仓库组织，`Snapshot` 提供内容身份，`Diff` 提供变更，`FormUnits` 组装 Unit，`AnalyzeImpact` 提供影响证据，`Graph` 提供节点、
关系和对应版本的源码。调用方通过 context 设置超时与取消，并决定如何处理不完整结果。
库可以调用 Git，但不会启动 repocli 进程、执行目标项目命令或写入 CLI 日志与历史。
边界见 [工具包职责与边界](AGENTS.md)。

Go 工具包位于 `toolkit/go`，CLI 是 `apps/cli` 下的独立 module；仓库通过 go.work 联合开发。
构建与发布约定见 [发布契约](docs/release.md)。

## 使用

`diff` 输出捕获的路径、patch 与前后源码；增加 `--units` 可查看 Unit 组装。
`impact` 输出改动声明、可能受影响的文件及组件上下文。声明发现遵循 CodeGraph
的语言能力；依赖感知的测试选择支持 Go、Python、JavaScript 和 TypeScript。

```sh
repocli diff --repo /path/to/repo --base main --json
repocli impact --repo /path/to/repo --base main --test-dir tests --json
```

省略 `--json` 输出可读文本。`impact` 的多个测试目录可重复传入 `--test-dir`；测试与源码同目录时，
使用 `--test-dir .`。不传测试目录则以仓库内受支持的源码作为候选入口，仍受构图预算约束。`--base` 默认为 `HEAD`，比较该提交与当前工作区。

影响分析自动选择符号、文件或包起点；每个文件报告起点、路径置信度、依赖距离和证据。
局部提取缺口保留为 observation，不使全部候选降级。影响范围是静态估计，结果由调用方自行消费，`diff` 不执行项目命令。
patch 输入、输出字段和分析限制见 [diff 使用说明（英文）](docs/diff-usage.md)。

`inspect` 查询仓库结构，不读取源码内容或计算全仓摘要：

```sh
repocli inspect --json
repocli inspect --staged --json
repocli inspect --head HEAD --json
```

输出 Repository 身份、组件根目录、语言和包工具证据。Component 是 lint、test、打包等工程操作的粒度，
由调用方决定并执行这些操作。识别优先依据 `go.mod`、`package.json` 等 manifest，Makefile 次之；
仅有 Makefile 时 language 未知。两者都没有时，仓库的 Component 列表为空。
无需添加、也不会生成 RepoKit 专属配置文件。识别规则与限制见 [仓库结构识别](docs/repository.md)。

`snapshot` 读取仓库内容摘要，无需运行变更或测试影响分析：

```sh
repocli snapshot --json
repocli snapshot --staged --json
repocli snapshot --head HEAD --json
```

snapshot schema 2 只报告内容身份与捕获完整性；原先读取组件字段的调用方改用 `inspect`。
摘要规则与 `diff` 一致；比较前须检查 `complete`。
详见 [snapshot 使用说明（英文）](docs/snapshot.md)。

分析命令和无效调用默认记录工作目录、完整参数、版本、耗时和退出码，日志位于
`~/.repocli/logs/YYYY-MM-DD.log`，保留今天及之前 29 天。
每次记录有独立 `run_id`；参数保留为 JSON 数组，便于准确重试。
成功的 help/version 调用不初始化日志。命令报告与错误仍输出到原位置，内容不复制到日志。
diff 分析另追加到同目录的 `diff-YYYY-MM-DD.jsonl`，保留版本、比较输入、查询参数和阶段耗时。
按耗时找出最慢 case，以原参数重试，并核对快照摘要后比较；工作区、index 和 patch 重试须保留原始输入。
详见 [日志说明（英文）](docs/logging.md)。

## 本地统计

`repocli stats` 汇总保留期内的跨仓验证执行与复用、命令及分析耗时、目录文件容量。
支持 `--json`、`--top 10` 和 `--state-dir`，不执行检查或清理记录。
统计口径和不完整报告的退出码见 [本地统计](docs/stats.md)。

## TypeScript API

`toolkit/typescript/` 提供 Node.js 22+ 原生工具包 `@compforge/repokit`，当前支持仓库识别与文件归属：

```typescript
import { inspect, owner } from "@compforge/repokit";

const report = await inspect({ repository: repoPath, timeoutMs: 5_000 });
if (!report.complete) throw new Error(JSON.stringify(report.diagnostics));
const component = owner(report, "server/main.go");
```

实现直接调用 Git，复用 quality-harness common 身份，无需 repocli 二进制或 Go 运行环境。
安装、输入选择和边界见 [TypeScript 指南](toolkit/typescript/README.md)。snapshot、diff 和 graph 使用 Go API。

## Python API

`toolkit/python` 提供原生同步 `inspect` / `owner`，复用 common 身份和共享识别契约。
支持 Python 3.11+、macOS/Linux；安装与调用见 [Python 工具包](toolkit/python/README.md)。

仓库可以没有 Component，根目录也可以就是一个 Component。`tree` 返回基础目录和文件视图，
`inspect` 返回组件及 Manifest 证据；gitlink 只保留引用，不递归捕获子仓内容。原生库还提供工作区
snapshot 与 Git 查询，Python 提供 Git/Forge 操作。CLI 按需求暴露能力，详见
[仓库模型](docs/repository.md)与[Git 操作](docs/git.md)与[PR 契约](docs/pr.md)。

### 查看变更单元

`repocli diff --units --max-units 8` 展示 diff 如何拆为 Fragment，再按关系形成 Unit；
加 `--json` 输出结构化结果。Go 工具包提供同一能力，详见 [Fragment 与 Unit](docs/units.md)。
