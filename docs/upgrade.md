# CLI 更新

## 边界

更新是 CLI 安装维护操作，属于 `apps/cli/internal/upgrade`，不进入 Repository 工具包。
`version` 查询本机编译版本；`upgrade --check` 查询 GitHub 最新稳定 Release；`upgrade` 显式安装。
分析命令不联网检查更新，Go、Python 和 TypeScript library 的版本由各自消费者的包管理器管理。

## 查询与安装

查询使用 `compforge/repokit` 官方仓库的 `releases/latest`，比较语义版本，不安装预发布版本，也不降级。
源码开发版没有可比较版本时返回错误；带版本的源码构建与 Release 二进制使用相同规则。
新版必须包含当前 OS/架构的精确附件和 `checksums.txt`；缺少附件、限流和网络错误均作为
查询失败报告，不误报“已是最新版”。可用 `GH_TOKEN` 或 `GITHUB_TOKEN`（前者优先）
提高 GitHub API 配额；凭据仅发送到官方 API，不发送到下载域名。
HTTP 请求有 30 秒上限，整次命令受 `--timeout` 控制。

安装先下载并验证压缩包的 SHA-256，再提取唯一的普通 `repocli` 文件。
在已安装二进制所在目录写临时文件，保留执行权限、刷盘后原子 rename；rename 是唯一提交点，
之前的任何失败都不改变旧二进制。符号链接解析到实际二进制后替换，链接本身保留。
目标目录不可写时返回错误，不自动提权。包管理器拥有的安装应交回原包管理器更新。
checksum 用于验证 Release 附件的一致性，信任来源仍是官方 GitHub Release 与 HTTPS。

## 自动化输出

`repocli upgrade --check --json` 和 `repocli upgrade --json` 都输出[统一结果 envelope](cli-output.md)，其中 `data` 为：

```json
{"currentVersion":"0.17.0","latestVersion":"0.18.0","updateAvailable":true,"updated":false}
```

`updateAvailable` 比较执行前版本与最新稳定版；`updated` 仅在实际替换成功后为 true。
查询成功即退出 0，包括发现新版；调用者读取字段决定下一步。执行失败退出 1，参数错误退出 2。
失败时 stdout 返回带 `error` 的统一结果，执行日志走 stderr。`--check` 不下载附件或写安装目录。
