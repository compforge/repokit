# 用户配置与 Forge 访问

RepoKit 的用户配置是 `<home>/config.json`。home 沿用显式 `--home` / SDK home、
`REPOKIT_HOME`、`~/.repokit` 的优先级；只支持 JSON。配置由用户维护，加载不创建目录或文件。
文件必须是仅 owner 可访问的普通文件（`chmod 600`）。文件缺失等价于空配置；损坏、未知字段、
字段类型错误或权限过宽会报错，不转成空配置，也不在错误里输出文件内容。

```json
{
  "forges": {
    "github.com": {
      "provider": "github",
      "token": "<token>"
    },
    "git.example.com": {
      "provider": "gitlab",
      "token": "<token>"
    }
  }
}
```

## 身份、配置与状态

Repository 元信息已经包含 Forge 身份与仓库路径；配置以具体 Forge host 为键，同一个实例的仓库
共享访问配置。`github` / `gitlab` 公共身份分别解析到 `github.com` / `gitlab.com`；host 大小写
规范化，重复的规范化键拒绝加载。provider 是 `github` 或 `gitlab` 平台类型，不是实例身份。
自建 host 必须声明 provider，不根据名字猜测平台。

| 内容 | Owner |
|---|---|
| Repository 所属 Forge、仓库路径、checkout 与远端事实 | Repository / state |
| host → provider、API host、凭据来源 | 独立 config 模块 |
| HTTP 请求、连接隔离、PR 读写与观察 | forge |
| 提交、PR 目标、review 与合并流程 | 调用方 |

`config.json` 不属于 repository 缓存，`clean` 不删除或修改它。运行时访问记录、PR 观察和验证历史
由各领域组件拥有，state 提供共享身份、访问与存储协调。工具包不读取 `.devloop/config.json`、仓内应用配置或 `gh/glab` 登录文件，也不自动
搬迁凭据；使用方可一次性把自己的访问配置放到 RepoKit home。显式构造 Forge client/connection
仍可直接传参，不隐式加载文件。

## Forge 条目

| 字段 | 含义 |
|---|---|
| `provider` | `github` 或 `gitlab`；两个公共 host 可省略，其余必须设置 |
| `apiHost` | 可选的 API **host**，可含端口；不含 scheme、路径、用户信息。省略时从实例 host 推导 |
| `token` | 当前 host 的凭据；不进入 repository 身份或状态记录 |

API 路径由 provider 适配：公共 GitHub 使用 `https://api.github.com`，Enterprise 使用
`https://<apiHost>/api/v3`，GitLab 使用 `https://<apiHost>/api/v4`。API host 只改变传输地址，
Repository 仍属于原来的 Forge。CLI `--api-host` 保持完整 API URL 的运行参数语义；
`--provider` 若给出，必须与解析出的配置一致。

**Forge 访问配置只来自 config.json**，不读取 `GH_TOKEN`、`GITHUB_TOKEN`、`GITLAB_TOKEN`，
也没有环境变量名间接引用或逐字段合并覆盖。配置缺失即报缺失，避免终端、CI 和 agent 因隐藏的
环境变量获得不同账号。SDK 显式构造连接时仍可传入参数，配置加载和直接传参是明确分开的入口。
`REPOKIT_HOME` 只选择用户目录，不提供访问配置。

每次操作加载一个配置快照并跨仓复用，更改文件在下次操作生效。日志只记录配置位置、host 和
provider，不记录 token；配置对象的普通展示不包含凭据。

## SDK 与 CLI

Go 通过公共 config 包显式加载并解析，Forge client 继续只接收显式参数：

```go
settings, err := config.Load(home)
// 检查 err。
access, err := settings.Forge(repository.Forge.Name)
// 检查 err。
client, err := forge.NewClient(access)
```

Python：

```python
from repokit.config import load_config

settings = load_config()
connection = settings.forge(repository.forge.name)
client = connection.client(repository.path)
```

TypeScript：

```typescript
import { loadConfig } from "@compforge/repokit";

const settings = await loadConfig();
const connection = settings.forge(repository.forge.name);
const reader = connection.reader();
```

CLI 的 `pr`、`clean`、`worktree clean` 共用同一配置解析与连接组装入口。缺少访问配置时，
错误包含对应 host 与配置路径；PR 不可用不会被解释为无 PR。清理的部分失败语义见
[闲置清理](cleanup.md)。三种语言共用 `conformance/config/cases.json` 验证解析契约。

接入 devloop 时，使用这些入口替代其 `forge_entry`、`forge_token` 与凭据优先级逻辑；
PR 目标选择、review 配置和工作流策略仍归 devloop。RepoKit 不保留调用方配置的兼容读取分支。

### 从 Checkout 访问 Forge

Python `forge_client(checkout, home=..., timeout=..., target_repository=...)` 和
`pull_request_context(checkout, home=..., timeout=..., targets=...)`，以及 TypeScript
`pullRequestContext(checkout, { home, timeoutMs, targets })` 统一完成 origin → config →
原生 client/context 的组装。无 Forge origin 返回空；缺少凭据或配置无效保持错误。
每次操作加载一次配置，不读取调用方配置，不推断 fork 的目标仓库。调用方显式选择 targets，
并负责 context/observer 的关闭。Python `pull_request_identity` 只解析身份，不读取凭据。

程序选择已有 CLI 查询时，显式导入 Python `repokit.cli` 的 `list_pull_requests` 或
`pull_request_description`。客户端校验 schema、command、outcome 与数据形状，保留错误，
共享调用方 operation 预算并限制输出容量；不持有凭据、缓存、重试或工作流目标选择。
原生 SDK 不调用这个入口；CLI 不可用也不会静默切换到原生 API。

## Repository 配置

`~/.repokit/repositories/<repo-key>/persistent/config.json` 保存所有 clone 共用的 repository 策略。
例如 `{"worktree":{"max_count":10,"max_prepared":3,"outputs":["dist"]}}`。缺失时使用默认策略，
无效配置返回错误；配置、创建来源和登记不随过期观测清理。Git branch/commit/rebase 恢复记录仍归
Git 操作组件，存放在 common Git directory。完整布局见 [state](state.md)。
