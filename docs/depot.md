# Component 的 Depot

Component 的 `language`、`tools` 和可选 `depot` 均复用 common。
RepoKit 的 `ComponentBinding` 另保存 root、manifests 和 `toolEvidence`；发现证据不进入 Tool。
`Tool` 表示项目声明的开发工具，不代表该工具已经安装或可执行。

一个 Component 在当前 checkout 对应零个或一个逻辑 Depot，Depot 包含多个 Bin。
Bin 的 `path` 是规范化的 POSIX 仓库相对路径，`kind` 为 dependencies、output 或 cache。
不同 Component 可以引用同一物理 Bin；目录尚未创建、已经回收，不改变逻辑声明。
依赖安装的一次操作可以准备多个 Component 共享的 Bin。

## 静态发现

Go、Python、TypeScript 使用同一组 conformance 用例，inspect、diff、view 解释各自捕获版本的材料。
发现阶段不扫描忽略目录、不安装依赖、不执行构建工具。

当前原生规则：

- Node package.json 对应安装根下的 node_modules。
- uv 项目的 pyproject.toml 与安装根 uv.lock 对应 .venv。普通 Python manifest 不猜测虚拟环境。
- npm/Yarn package.json workspaces、pnpm-workspace.yaml packages、tool.uv.workspace members/exclude
  决定安装根。仅有父级 lockfile 不构成 workspace 成员关系。
- 成员模式支持相对目录、分段 `*` 和跨层级 `**`，支持排除；未支持的模式、畸形配置不猜测 Depot。
  Yarn PnP 不声明 node_modules Bin。
- 同一目录包含 Node 和 uv manifest 时，两类 Bin 放在同一个 Depot，不受主要 language 限制。

未知位置保持未声明。Go 全局 module cache、conda 全局环境、动态构建配置中的输出位置不自动发现。
common 支持 output/cache Bin，但 RepoKit 不凭 dist、vendor 等目录名推定可删除资源；当前只自动
发现上述 dependency Bin。显式 worktree outputs 保留原有项目授权语义。

## 显式观察与准备

```sh
repocli depot inspect --repo . --json
repocli depot prepare --repo . --component apps/web --timeout 10m --json
```

省略 `--component` 处理整个 checkout；重复指定可选择多个 Component。选择成员仍使用其 workspace
安装根，多个 Component 共用的物理 Bin 聚合为一项任务，每轮最多执行一次安装。报告保留选中 Components，以及每个 Bin 的
全部引用 Components、安装根、manager、command、inputs、fingerprint 和运行态结果。
未声明 Depot 不证明没有依赖；当前不准备 output/cache，也不安装语言运行时或升级依赖。

Go 公共入口为 `depot.Inspect` / `depot.Prepare`。Python 原生依赖 API 仍独立执行；
各语言调用方也可直接消费上述版本化 CLI 结果，不作为原生 SDK 的隐式回退。

| status | 含义及 prepare 行为 |
|---|---|
| ready | 回执匹配当前输入；`reused`，不安装。 |
| present | 本地目录存在但没有回执；`preserved`，报告未验证告警。 |
| missing | 目录不存在；有 recipe 时安装，否则 `unsupported`。 |
| stale | 回执与输入不匹配或无效；有 recipe 时重新安装。 |
| unsupported | 位置或准备方式不支持；报告原因，不安装。 |
| unknown | 已开始安装后失败或中断，目录内容未确认；不能作为 ready 证据。 |

支持 npm ci、pnpm/Bun frozen install、Yarn classic frozen / modern immutable 和 uv sync --locked。
uv workspace 一次同步全部成员；不生成或更新锁文件。输入指纹绑定绝对安装根、lockfile、workspace 成员
manifest 与本地安装配置。验证复用共享相同的安装输入路径规则；共享 Bin 不扩大为安装根下全部源码。uv 清除继承的环境路径覆盖。symlink、嵌套 checkout 与含 tracked 文件的 Bin
不会作为安装目标。已有无回执目录不会被接管，`present` 不表示经过验证，也可能是先前失败留下的目录。

准备与 worktree cleanup、Python 准备共用 repository 级跨进程 lease；锁等待受调用方 context 约束，
没有额外 30 秒安装预算。锁内重新观察，计划变动即停止。安装串行，每个进程最多 10 分钟且受整轮更短预算
约束；stdout 上限 4 MiB、stderr 上限 64 KiB，取消或超限终止进程组。真正安装失败后剩余 Bin 标为 `not_run`。

旧成功回执在执行前移除；安装成功、输出为本地目录且输入复核一致后，原子写入包含 manager、fingerprint
和 generation 的回执。失败不回滚安装产生的副作用，也不发布 ready。若失败留下无回执目录，后续报告
`present`，需使用者核对或移除该安装目录后重新准备，工具不自动把它认作成功。

JSON 使用统一 envelope，`data.bins` 保留 action、executed、exitCode、stdout/stderr 和结构化 error。
`data.complete` 表示是否处理完请求范围，不等于所有 Bin ready；失败/不支持通过非零退出与 error 表达。
`data.phases` 和 `cost.phases` 提供 observe、lock_wait、install、verify、receipt 秒数，带 Bin 路径。
外层 cost.wallSeconds 是整轮墙钟时间，不应再加到阶段耗时中；verify 包含其内部重新观察。

## 回收

回执和执行状态属于 RepoKit，common 不保存命令、回执、占用或就绪状态。state 只协调生命周期、lease
与未完成删除，安装 recipe、指纹、执行过程及报告留在 depot。

worktree retention 负责环境回收，见 [worktrees](worktrees.md)。Depot 声明本身不授权删除：
仍需回执或项目 outputs 策略、Git ignore/tracked 检查、checkout 保护及删除恢复 journal。
共享目录按物理位置处理；tracked 文件、符号链接、嵌套 checkout 不因为存在 Bin 声明而变得可删除。

## 验证输入与复用

Go/CLI validate 自动为输入范围内的 dependency Bin 记录 `depotWitnesses`，调用方无需维护第二份缓存。
证据绑定物理目录身份与准备回执内容；对应安装根的源码子树保守加入输入范围，防止子组件显式声明
遗漏 workspace 根锁文件和其它成员的 manifest。

缺失、无回执、回执无效或不支持的物理位置仍可执行真实检查，但不能复用历史成功。
开始执行前和整轮结束后复核这些证据；回收、替换目录或修改回执会使旧计划失效。
回执不是对所有依赖文件逐一校验的完整性证明，任意手工修改目录内部内容不在这一保证内。
outputs/cache 不作为 dependency witness，不因构建写入忽略的 dist 而使本轮自我失效。

未发现的外部依赖或动态资源仍应由调用方通过显式 input witnesses 或 DisableReuse 声明。
验证不隐式安装依赖，真实检查失败保留原始命令和输出。
