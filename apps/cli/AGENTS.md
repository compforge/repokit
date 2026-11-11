# RepoKit 应用

## 项目定位与边界

CLI 是 Go 工具包的消费方，负责命令行交互和本地 viewer；通过公共 API 获取分析结果。
共享契约见 [项目约定](../../AGENTS.md)，发布入口见 [发布契约](../../docs/release.md)。

## 代码地图与核心模块

```text
cmd/repocli/       # 启动、信号、版本与退出
internal/
├── cli/           # Cobra 命令、结果展示、执行日志与 JSONL 历史
├── stats/         # CLI 日志与存储统计；验证历史通过工具包公共 API 读取
├── upgrade/       # 显式 Release 查询、checksum 验证与原子安装
└── viewer/        # HTTP 服务、静态页面及 GraphSnapshot 展示
```

## 关键约定

- 仓库识别和代码分析归 toolkit；应用测试通过公共 API 验证结果，不导入工具包 internal。
- viewer 归当前 CLI 应用；库调用不产生 CLI 日志；CLI 为验证工具包显式提供默认历史目录。
- 在仓库内用 go.work 联合构建；go.mod 声明可发布的工具包版本，不提交本机路径 replace。
- `make lint/test/build/install` 是本应用入口；构建与安装从根 VERSION 注入版本，go install @version 使用模块构建信息。

- [CLI 更新](../../docs/upgrade.md)：显式查询与安装、版本和输出契约。
