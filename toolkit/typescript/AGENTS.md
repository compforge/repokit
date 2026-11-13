# TypeScript 仓库工具包

## 项目定位与边界

`@compforge/repokit` 直接完成 tree / inspect 与文件归属查询，只调用 Git，不启动 repocli CLI。
Repository、PullRequest、Component、Product 复用 harness-common；业务决策和进程生命周期属于调用方；PR 观察层拥有显式启用的状态存储与刷新。
共享契约见 [仓库结构识别](../../docs/repository.md)。

## 代码地图

- `src/index.ts`、`model.ts`：公共入口、结果与 owner 查询。
- `src/forge.ts`、`forge/`：common PR 契约、只读 GitHub/GitLab 客户端；query/page 管范围与单页，scan 管有界扫描与覆盖，评审读取独立于基础事实。
- `src/forge/observation/`：PR 缓存、持久化、刷新与终态收敛；契约见 [PR 观察](../../docs/pr.md#持续观察)。
- `src/inspect.ts`：输入版本、统一时限及完整性判定。
- `src/config.ts`：用户配置与 host 级 Forge 连接组装；契约见 [用户配置](../../docs/configuration.md)。
- `src/validation/`：原生验证历史查询；与 Go 共享 Repository 身份、WAL 回放和 advisory lock，不执行检查；列表可按页读取输入证据摘要，详情才读取输出，契约见 [validation](../../docs/validation.md#原生历史查询-api-与存储边界)。
- `src/state/`：RepositoryState、home、身份编码和共享记录存储；Git 适配不依赖 state。
- `src/git/`：operations、checkout/ref 状态、remote 解析与内部 catalog 读取；`index.ts` 汇总公共导出。
- `src/layout.ts`：manifest / Makefile 组件发现、语言和工具证据。
- `src/language.ts`、`languages.json`：生成的文件名识别目录，不加载 AST。
- `tests/` 与 `../../conformance/inspect/`：真实 Git 回归、Go/TS 共享语料。

## 关键约定

保持 Node ESM 可用；在本目录运行 `make lint`、`make test`、`make build`。
不手改生成目录；在仓库根运行 `make generate-languages`，并运行 Go 与 TS 两侧检查。
包版本由 package.json 独立维护；代码变化同时遵守仓库根 VERSION 约定。

仓库操作契约见 [Git](../../docs/git.md) 与 [PR](../../docs/pr.md)。分析与写操作分别显式调用，不能让分析触发写入。

公共结构化错误 `RepoKitError` 的字段、cause 与 JSON/text 表达见 [SDK 错误契约](../../docs/errors.md)。
