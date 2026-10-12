# 评审 002：首版实现的合同收口

状态：已处理，代码与文档已按本节修正。日期：2026-10-12。
针对版本：`4c28baf`（PR #1 `feat/minimal-core`，最小核心与离线示例首版实现）。

本轮为对首版实现的评审，结论已直接落到代码与文档，不保留待议项。原文判断与本轮处理并列如下。

## 验证基线

| 检查 | 结果 |
| --- | --- |
| `go vet ./...` | 通过 |
| `go test -race ./...` | 通过（runner / tool / openai / examples / integration） |
| `gofmt -l core providers examples integration` | 无输出 |
| 九个离线场景 `go run ./examples/embedded -scenario ...` | 状态与 [验证记录](VALIDATION.md) 一致 |
| 第三方 Go module 依赖 | 无 |

## 本轮修正

| 评审发现 | 处理 |
| --- | --- |
| 免审批写操作绕过资源与目标核对 | 收紧：`Effect=write` 一律要求只读 `Prepare`，注册阶段即拒绝缺失者；审批只是其上再加人工确认 |
| 取消路径丢失结构化错误码 | 修正：context 取消/超时及 provider 返回的 context 错误统一为 `cancelled_or_timed_out`，保留 `Failure.Code` 与 context cause |
| 审批有效期在执行前复核后才检查 | 修正：审批有效期的执行前复核明确保留在 `RequiresApproval` 分支，且复核后再次校验未过期 |

### 免审批写操作的合同缺口（收紧依据）

首版允许 `Effect=write` 且 `RequiresApproval=false`，而 `Prepare` 仅在需要审批时调用。由于 `Prepare` 是资源、目标版本与影响范围的**唯一**来源，该类写操作实际跳过了：

- `Intent.Resources` / `Version` 的资源级核对；
- `fingerprint` 绑定与重算比对；
- `RFC §5` 要求的「审批前构造具体操作提案」中可审计的影响面。

这使 `THREAT-MODEL.md` 「每次调用校验……参数、**资源**和业务权限」的承诺对免审批写不成立。处理选择收紧而非放宽：写操作的影响声明是执行链的组成部分，不是审批的附属品。免人工审批只表示不需要弹确认，不表示可以跳过资源与目标核对。

具体改动：

- `core/runner/runner.go`：`Effect=write` 且 `t.Prepare == nil` 时在 `newRunner` 返回 `ErrConfiguration`；
- 执行链中 `Prepare` 对**所有**写操作调用，`requiresApproval` 仅控制是否进入审批门；
- 审批后复核与无审批路径共用同一 `fingerprint` 比对，目标变化一律 `proposal_changed`；
- `core/tool/tool.go`：`Prepare` 文档改为「每个写操作必填」；
- `docs/INTEGRATION.md`、`docs/RFC-001-SERVER-RUNTIME.md`：同步说明免审批不等于跳过影响核对；
- 测试：新增「无 `Prepare` 的 write 注册被拒」回归；`TestModePolicyAndExplicitLowRiskWrite` 改为断言 `Prepare` 仍被调用两次（提案与执行前复核）。

### 取消语义

`runner.go` 原在循环头与模型返回后直接 `return out, err`，使 `ctx.Err()` 以裸错误返回。顶部 `defer` 虽能把公开事件码归一到 `cancelled_or_timed_out`，但返回值不是 `*Failure`，宿主 `errors.As` 取不到 `Code`；provider 主动返回 `context.DeadlineExceeded` 时更会被归类为 `model_failed`。现统一为：

```go
if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
    return fail("cancelled_or_timed_out", errors.Join(ctx.Err(), err))
}
```

新增 `TestCancellationSurfacesStructuredCode` 覆盖 deadline、主动取消与 provider 返回 context 错误三种情形，同时断言错误码事件与 context cause 均保留。

## 仍然存在的边界（非缺陷，属设计取舍）

- **模型流两遍缓冲**：`preview` 在内存中累积整轮增量文本，再与最终文本做逐字节相等断言。这保证了「预览与最终答案一致」，代价是该轮文本常驻内存；在 `MaxOutputBytes` 硬上限下不构成 DoS。
- **`OperationID` 语义靠宿主**：同意图续跑复用 ID、新意图换 ID 由宿主保证，核心只能校验非空、长度与单次运行内不重复。
- **不存在恢复原语**：`pending_approval` 的续跑由宿主「加载原意图与有效批准、以相同 `OperationID` 显式发起」；核心不保存挂起任务，也不提供把未完成循环装回执行的机制。这与 [RFC §8](RFC-001-SERVER-RUNTIME.md) 的分工一致，但宿主需要自己实现该续跑入口。
- **无 OS 沙箱、无强制终止**：不合作的 Go 回调只能靠 context 配合，核心无法强制杀死。

## 未验证

真实 Router 字段合同、宿主登录/会话库、真实人机审批与批准消费、业务持久去重与审计、后台队列/多 worker 接管、OS 沙箱。详见 [验证记录](VALIDATION.md) 的「尚未验证或交付」。
