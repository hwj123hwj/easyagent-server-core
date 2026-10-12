# 首版核心与示例验证记录

日期：2026-10-12。环境：macOS arm64，Go 1.26.1。此记录只覆盖公开核心与虚构适配器，未访问生产模型、数据库、对象存储或任务。

## 已执行

- go test -race ./...：通过。覆盖严格参数、授权拒绝、审批缺失/绑定/过期/撤权/目标变更、写操作不确定/部分完成、预算、取消、流中断、证据引用和并发上下文。
- go vet ./...：通过。
- 源码复制测试：将四个 core 包复制到全新临时宿主 module，调整 import，关闭 module 网络下载，实际构建并运行模型/工具循环。
- HTTP provider：使用本地 httptest，验证固定 endpoint/header、逐字节 UTF-8/SSE 和工具参数拼装、DONE/finish 标记、超大/非法流、私有推理不输出、错误正文不泄露、重定向拒绝与取消；无自动重试。
- 九个离线场景：合同测试与实际 go run 均通过，见下表。
- 模块依赖检查：go list -m all 仅包含本项目，无第三方 Go module。

| 场景 | 状态 | 模型次数 | 工具次数 | 实际虚构副作用 |
| --- | --- | --- | --- | --- |
| query | completed | 2 | 1 | 0 |
| history | completed | 1 | 0 | 0 |
| diagnostics | completed | 3 | 2 | 0 |
| pending | pending_approval | 2 | 1 | 0 |
| approved | completed | 4 | 3 | 1 |
| denied | interrupted | 2 | 1 | 0 |
| unknown | interrupted | 2 | 2 | 1 |
| partial | interrupted | 2 | 2 | 1 |
| duplicate | completed | 4（最后一次） | 3（最后一次） | 1（两次合计） |

unknown 故意在副作用发生后模拟断线，核心报告不确定且停止；副作用计数为 1 不是“操作成功”的断言。denied/unknown/partial 的 CLI 将预期错误作为演示处理，状态仍为 interrupted。

配置隔离回归曾发现 schema map 的浅拷贝问题，已改为独立快照并由并发/快照测试覆盖。生产工具代码中未引入通用命令或动态加载入口；测试中的 os/exec 只用于在临时目录执行 Go 构建，不进入 core。

## 尚未验证或交付

- 真实 Router 的认证、模型字段和兼容协议；现在只验证 Chat Completions SSE 本地合同。
- 宿主登录、会话库、切换/刷新 UI、HTTP/SSE 路由和保留策略。
- 真实人机审批、批准持久化/消费、业务操作持久去重及审计。
- 后台队列、检查点、PG/Redis、多 worker 领取或崩溃接管。
- OS 沙箱、任意 Go 回调的强制终止、完整 JSON Schema、金额硬上限。

GitHub Actions 配置了最低 Go 版本与 stable 的检查矩阵，远程执行结果应以对应提交的 checks 为准。本地验证不等同于该矩阵或生产验收。
