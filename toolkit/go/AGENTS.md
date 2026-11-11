# Go 仓库工具包

## 项目定位与边界

`github.com/compforge/repokit/toolkit/go` 是可独立导入的 Repository 解析与分析工具包。
分析入口在 `repokit.go`，仓库状态与 checkout 生命周期在 `state/`，共享模型和职责见 [项目约定](../../AGENTS.md)。

## 代码地图与核心模块

```text
repokit.go          # 分析 API 与结果类型
units.go            # diff → Fragment → Unit 的公共 API
errs/               # 结构化错误、cause 链及 JSON/text 表达
state/              # 仓库状态业务：观测、checkout 生命周期、保留策略与共享存储
git/                # 显式 Git 操作：不可变 tag 发布与远端核验，不承担 workflow
config/             # 用户配置与 host 级 Forge 访问解析；不承载 repository 状态
forge/              # 原生 PR 查询、写入与成功后维护
├── release/       # Release 查询、确认发布和结果核验，不承担版本策略
├── discussion/    # 讨论模型、查询和写入，不承担 maintenance
├── model/         # PR 身份，供生命周期与 discussion 共享
└── internal/transport/ # 共享 HTTP、认证、响应限制与限流冷却
validation.go       # 验证公共类型别名与入口；实现归 internal/validation
internal/
├── validation/    # Makefile 规划、格式化、检查执行、输入身份与结果复用
│   └── history/   # 执行记录、WAL、统计、清理与文件锁；不依赖 validation 父包
├── analysis/      # 输入选择、识别、捕获、构图与变更分析编排
│   └── deadcode/  # deadcode 编排、候选规则、可达性、前后比较与筛选
├── gitworktree/   # Git checkout 查询与命令执行适配，不依赖 state
├── git/           # 只读 Git 材料与容量边界
├── project/       # common 身份、目录布局、语言、工具证据及归属
├── diff/          # patch、变更行与 postimage
├── units/         # 源码片段拆分、分类和 Unit 组装
├── impact/        # 影响策略、测试候选和缺口
└── codegraph/     # 共享代码事实及仓库解析上下文
```

## 关键约定

- 应用只能消费公共 API；不能为了 CLI 或测试放宽 internal 可见性。工具包不依赖 apps、Cobra 或 HTTP 展示。验证历史由 validation 通过显式 HistoryDir 保存；不隐式写入用户目录。
- 终止操作的错误使用公共 `errs.Error`，在所属组件提供 code、操作与目标；应用直接复用其 JSON/text 表达，退出码和输出通道归应用。领域报告中的失败与缺口仍留在报告中，见 [错误契约](../../docs/errors.md)。
- context 负责取消与时限；结果保持完整性和诊断，消费策略归调用方。Repository / Component / Product 复用 common。
- `make lint/test/build` 验证本模块。`../../conformance` 是仓库共享语料，测试需在完整仓库中运行。
- 文件名目录由 `make generate-languages` 更新，不能手改生成产物；TS/Python 运行时不依赖 Go。
