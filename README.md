# EasyAgent Server Core

面向企业应用的可嵌入 Go Agent 核心：有界模型循环、具名受控工具、逐次授权和流式事件。

**状态：首版开发预览，核心与离线示例已可运行。** 仓库：[hwj123hwj/easyagent-server-core](https://github.com/hwj123hwj/easyagent-server-core)。所有示例为虚构数据；尚未接入生产 Router、业务来源或持久后台任务。

项目从 [EasyAgent](https://github.com/hwj123hwj/easyagent) 提取必要协议并改造，在宿主应用进程中运行。服务端专用指工具与权限的执行边界，不要求独立部署 Agent 服务。

## 运行

需要 Go 1.24+，无第三方 Go module 依赖，默认示例不访问网络：

```sh
go test -race ./...
go vet ./...
go run ./examples/embedded -scenario query
go run ./examples/embedded -scenario diagnostics
go run ./examples/embedded -scenario pending
go run ./examples/embedded -scenario approved
go run ./examples/embedded -scenario duplicate
```

示例输出分段回答、查询进度和执行状态；pending 不执行，duplicate 显式提交同一虚构意图两次但只产生一次副作用。另有 history、denied、unknown、partial 场景。模拟审批与内存去重只用于演示；真实权限、批准记录和业务去重由宿主落实。

源码：[模型协议](core/model/model.go)、[工具合同](core/tool/tool.go)、[执行器](core/runner/types.go)、[事件](core/event/event.go)、[可选 HTTP provider](providers/openai/openai.go)。接入入口是 runner.New + Runner.Run；详见 [接入说明](docs/INTEGRATION.md)。

## 最小核心

1. 消息、模型协议和固定配置的 HTTP/流式调用。
2. 带输入约束的工具注册与执行。
3. 有轮数、超时、调用次数和取消限制的 Chat/Agent 循环。
4. 每次工具调用的权限检查，以及可选的人工审批接口。
5. 回复增量、查询进度、完成、待批准和错误事件。

模型提出工具调用；代码决定是否允许并执行。核心不提供 shell、SSH、kubectl、任意脚本或代码执行、文件编辑、原始 SQL、任意 URL 请求、动态工具加载、Goal 或 Workflow。

工具可以是读取，也可以是宿主明确开放的单一业务操作。例如 `job.runs(job_id)` 查询记录，`job.retry(job_id)` 请求重跑一个任务。后者必须有资源范围、真实业务权限和操作结果合同；不能用一个接收命令字符串的回调包装成“受控工具”。

**权限校验与人工审批分开：每次工具调用都鉴权，授权范围内的普通读取直接执行，需要确认的操作先审批。** 缺少授权检查不能执行工具；需要审批却没有可用审批通道时，停止并返回待批准，不默认放行。写操作的幂等与结果确认由业务接口保证，不能由一个布尔声明代替。

## 集成与职责

```text
公开通用核心源码（固定版本）
        ↓ 引入
宿主：登录/权限 + 模型配置 + 历史消息 + 业务工具 + 事件/UI
        ↓ 按定义实例化
业务助手 / 运维排查助手 / 其他领域助手
```

| 通用核心 | 宿主应用 |
| --- | --- |
| 模型协议、可选兼容 HTTP provider | Router 的固定调用方式、endpoint/key 配置 |
| 工具协议、注册、参数校验、执行循环 | 业务工具实现、数据范围和每次调用的授权 |
| 可选审批接口与待批准事件 | 展示操作、批准/拒绝、审批记录和业务去重 |
| Definition、预算、取消和流式事件 | 登录、历史会话、HTTP/SSE、UI、审计与结果保存 |
| 单次执行入口 | 按需使用 PG/Redis、既有 worker 和多 Pod 协调 |

核心接受宿主传入的可信身份与历史消息，不内置会话数据库、任务队列、outbox、租约或 Workflow 引擎。Chat 完成当前问题，Agent 允许更主动的多步工具调用；两者共用执行器，模式不授予额外权限，也不必进入后台任务。

首选源码引入：复制约定的纯核心目录，调整 import，保留 LICENSE/NOTICE，记录来源 tag/SHA 与本地改动。没有 daemon、强制 RPC、新增 Pod 或 PG/Redis 安装要求。标准 Go module 导入也可选择；不依赖 EasyAgent 完整 SDK 或 Eino。宿主已有 Router HTTP 客户端可以直接适配 Provider。

首个宿主先实现业务查询和运维只读诊断。公共协议允许受控操作，不代表宿主已经注册或授权生产重试、发布等能力。嵌入核心与宿主共享进程权限，工具约束不提供 OS 沙箱。

## 文档

1. [架构与接口 RFC](docs/RFC-001-SERVER-RUNTIME.md)：最小核心、执行链、受控操作与宿主边界。
2. [威胁模型与权限边界](docs/THREAT-MODEL.md)：授权、审批、操作结果和同进程边界。
3. [验收与开源边界](docs/ACCEPTANCE.md)：核心验收、宿主按需验收与虚构示例。
4. [评审 001 与采纳说明](docs/REVIEW-001-OPERATION-CAPABILITY.md)：保留原始意见，并记录本轮采纳与修正。当前方案以 RFC 为准。
5. [嵌入接入与运行示例](docs/INTEGRATION.md)：实际 API、预算、审批、源码复制及未实现范围。
6. [首版验证记录](docs/VALIDATION.md)：本地检查、示例预期与验证边界。

本仓库采用 [MIT License](LICENSE)，来源说明见 [NOTICE](NOTICE.md)。实现引入 EasyAgent 源码时保留来源 LICENSE、版权和文件映射。当前参考提交为 `d90804fbe5cf488e6ace810cff925f7cc8b8c6d9`，升级按文件审查。
