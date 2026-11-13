# Python 仓库工具包

## 项目定位与边界

`compforge-repokit`（导入名 `repokit`）提供原生同步 tree / inspect / owner，通过 Git CLI 读取版本控制事实，
原生 API 不包装 repocli CLI；显式 `repokit.cli` 提供已安装 CLI 的有界操作与查询客户端。身份复用 harness-common；布局与工具证据归本包，消费方拥有执行策略。

## 代码地图与核心模块

```text
repokit/
├── __init__.py / model.py  # 公共 API、common 派生类型和 owner 查询
├── validation/            # engine：原生规划/执行；makefile：共享 grammar；report：Go wire report 解码
├── dependencies.py        # 只读依赖观察与显式锁定安装；workspace 安装根与回执
├── _inspect.py            # 输入版本、整次调用预算与一致性观察
├── access.py / cli.py     # checkout 访问组装 / 显式 CLI 协议客户端
├── config.py              # 用户配置与 host 级 Forge 连接组装
├── state/                 # 仓库状态业务、checkout 生命周期、metadata、身份与存储
├── git/                  # operations、state、index、stash、remote 与内部 catalog/staging
├── _process.py           # 有界子进程
├── _layout.py             # 纯布局投影、manifest / Makefile 识别和包工具证据
└── _language.py / languages.json # 固定依赖生成的文件名识别目录
```

## 关键约定

- 公共入口使用 Python 命名与 frozen dataclass；未知身份不伪造，完整性保留诊断。
- Git 子进程与元数据读取共享 deadline；超时、取消或超出输出容量时终止并回收子进程。
  按目录和对象批次读取，不为每个源文件启动 Git。
- 配置只作静态数据；inspect 不读取源码内容；snapshot 捕获内容但不执行目标项目。操作 API 不保存消费方状态。
- 生成目录由根 `make generate-languages` 更新，不能独立修改；三种语言共用 conformance。
- 使用 `uv sync --locked` 准备依赖；改动后运行 make format/lint/test/build。uv.lock 由 uv 生成。
  Python 包版本由 pyproject.toml 管理，同时遵守根 VERSION 约定。

## References

- [共享识别契约](../../docs/repository.md)
- [调用方式](README.md)

仓库操作契约见 [Git](../../docs/git.md) 与 [PR](../../docs/pr.md)。分析与写操作分别显式调用，不能让分析触发写入。

公共结构化错误 `RepoKitError` 的字段、cause 与 JSON/text 表达见 [SDK 错误契约](../../docs/errors.md)。
