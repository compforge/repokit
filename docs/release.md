# 构建与发布

仓库按可复用工具包和应用划分发布单元。Go、TypeScript、Python toolkit 和 CLI 各自拥有依赖清单；
项目与公共库使用 RepoKit / `repokit`，CLI 二进制、安装命令和压缩包前缀保持 `repocli`。
根 Makefile 提供显式全仓检查与构建，不承担包依赖。go.work 只连接本仓源码，不进入外部调用方的依赖解析。

## 开发与验证

实际 `make format/lint/test` 由各 leaf Component 的 Makefile 执行，根同名入口只提示、不递归，
避免自动验证同时选择根与子组件时重复执行。需要全仓检查时显式运行根 `make check`；release CI 使用这一入口。
在 `toolkit/typescript` 安装 npm 依赖、在 `toolkit/python` 执行 `uv sync --locked` 后，
根 `make check` 和 `make build` 覆盖全部 Component。单个组件可用 `make -C <component> lint test`。`make install` 只构建并安装 CLI；
`make -C apps/cli build` 生成根 `bin/repocli`。共享语料保存在根 conformance，各实现从同一份语料验证契约。

Go 工具包可用 `GOWORK=off go build ./...` 单独构建；应用通过 go.mod 固定工具包版本。
发布前的联合开发由 go.work 将该版本指向本地源码，不在应用 go.mod 中保留目录 replace。
CLI 版本通过构建参数注入；`go install ...@version` 则读取 Go 模块构建信息，不依赖运行时文件。

## 版本与发布顺序

- 根 `VERSION` 定义仓库和 CLI 版本，仓库 Release/tag 为 `v<VERSION>`。
- Go 工具包 module 为 `github.com/compforge/repokit/toolkit/go`，对应 tag `toolkit/go/v<VERSION>`。
- CLI module 为 `github.com/compforge/repokit/apps/cli`，对应 tag `apps/cli/v<VERSION>`。
  go.mod 声明的工具包版本与本次仓库版本保持一致，go.work 同步绑定该版本到本地源码。
- `@compforge/repokit` npm 包独立使用 `toolkit/typescript/package.json` 的版本。
  它依赖的 harness-common 版本必须先在 npm 可用；npm 发布遵循独立的包发布流程。

发布时先通过受控 Git 发布流程为同一提交创建 Go 工具包及 CLI 子模块 tag，再创建仓库 tag。
仓库 tag 触发 release workflow：检查版本与既有子模块 tag 的目标、验证全部 Component、构建各平台 CLI，
并在关闭 go.work 后独立安装 CLI，最后上传附件。工作流不创建或移动 Go 子模块 tag，也不自动发布 npm 包。
缺少子模块 tag、目标提交不一致或依赖不可用时停止发布。

子模块 tag 可用后，外部用户可通过以下入口安装或引用：

```sh
go get github.com/compforge/repokit/toolkit/go@v0.49.0
go install github.com/compforge/repokit/apps/cli/cmd/repocli@v0.49.0
```

发布时独立验证 CLI 的模块依赖与版本输出，避免仅有 go.work 的本地替换掩盖缺失的发布依赖。

## Python 包

`toolkit/python` 的发行名为 `compforge-repokit`，导入名为 `repokit`；版本独立由 pyproject.toml 管理。
使用 `uv sync --locked` 和 `make format lint test build` 验证并构建 wheel/sdist，包含运行时语言目录和类型标记。
Python 包复用已发布的 harness-common，不要求本地 quality-harness checkout。发布通过独立的 Python
包发布流程执行，Git release workflow 只验证，不自动发布 PyPI。安装后的 wheel 应在仓库外验证 inspect/owner。

Tag 发布可使用 `repocli git tag publish <tags...> --target <verified-commit> --repo . --json`；
每次调用只发布显式 tags，模块 tag 顺序仍按本文的发布契约执行。检查 `outcome=succeeded` 与
`data.confirmed=true` 后再验收 CI 和 Release assets；tag 确认不等于 Release 或包发布完成。
幂等、冲突和不确定结果恢复见 [Git tag publication](git.md#tag-publication)。
