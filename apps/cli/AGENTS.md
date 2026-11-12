# RepoKit 应用

## 项目定位与边界

CLI 是 Go 工具包的消费方，负责命令行交互和本地 viewer；通过公共 API 获取分析结果。
共享契约见 [项目约定](../../AGENTS.md)，发布入口见 [发布契约](../../docs/release.md)。

## 代码地图与核心模块

```text
cmd/repocli/       # 启动、信号、版本与退出
internal/
├── cli/           # 根命令、结果封装、执行日志与 JSONL 历史
│   ├── git/       # 显式 Git 操作与 tag 发布
│   ├── release/   # Forge Release 查询与创建
│   ├── validation/ # 验证执行、历史查询与 text 展示；根 cli 组装统一结果
│   ├── worktree/  # checkout 创建/复用、观察与清理命令
│   ├── pr/        # PR 生命周期和 discussion 命令及展示
│   ├── command/   # 共享选项与结果记录契约
│   └── forgeaccess/ # PR / Release / cleanup 共用的 Forge 访问装配
├── stats/         # CLI 日志与存储统计；验证历史通过工具包公共 API 读取
├── upgrade/       # 显式 Release 查询、checksum 验证与原子安装
└── viewer/        # HTTP 服务、静态页面及 GraphSnapshot 展示
```

## 关键约定

- 仓库识别和代码分析归 toolkit；应用测试通过公共 API 验证结果，不导入工具包 internal。
- 新增或调整命令输出时，遵循 [CLI 输出契约](../../docs/cli-output.md)，检查结果、诊断、输出通道与退出状态的一致性。
- viewer 归当前 CLI 应用；库调用不产生 CLI 日志；CLI 为验证工具包显式提供默认历史目录。
- 在仓库内用 go.work 联合构建；go.mod 声明可发布的工具包版本，不提交本机路径 replace。
- `make lint/test/build/install` 是本应用入口；构建与安装从根 VERSION 注入版本，go install @version 使用模块构建信息。

- [CLI 更新](../../docs/upgrade.md)：显式查询与安装、版本和输出契约。
