# EasyAgent Server Core

面向企业应用的可嵌入 Go Agent 核心：受限查询工具、流式回复、账户会话扩展点和可恢复任务合同。

**状态：设计提案，尚无可运行发行版。** 仓库：[hwj123hwj/easyagent-server-core](https://github.com/hwj123hwj/easyagent-server-core)。所有示例为虚构数据；文档中的接口和目录均为待实现设计。

项目从 [EasyAgent](https://github.com/hwj123hwj/easyagent) 提取必要协议并改造，作为宿主应用中的代码运行。服务端专用指面向企业后端的权限、并发和执行语义，不要求独立部署 Agent 服务。

## 集成方式

```text
公开通用核心源码
        ↓ 固定版本引入
宿主应用：登录/权限 + 模型配置 + 会话/任务存储 + 业务工具
        ↓ 实例化
数据助手 / 运维排查助手 / 其他领域助手
```

首选源码引入：宿主复制约定的纯核心目录，调整 import，保留 LICENSE/NOTICE，记录来源 tag/SHA 与本地改动。没有 daemon、独立 Agent API、强制 RPC 或新增 Pod 的安装要求。标准 Go module 导入可作为其他宿主的选择，本项目不依赖 EasyAgent 完整 SDK。

## 核心能力与宿主职责

| 通用核心 | 宿主应用 |
| --- | --- |
| 消息、模型接口、受限 Router/兼容 HTTP provider | 模型 endpoint/key 配置与凭据保管 |
| Chat/Agent 有界循环、预算、取消、循环检测 | 登录身份、助手资格、数据范围和逐工具授权 |
| 工具 schema、结构化证据与公开事件 | 实际业务查询、对象存储/任务诊断适配器 |
| 会话/任务/事件存储接口与恢复合同 | PG/Redis 实现、既有 HTTP 路由、SSE 和 worker 调度 |
| AgentDefinition：提示模板、工具集合和策略 | 创建业务助手与运维助手，维护私有规则与 UI |

Chat 完成当前问题，Agent 主动关联多个只读查询形成结果。两者都受硬预算约束，切换模式不增加权限。

核心不提供 Goal 无限循环、Workflow、shell、SSH、kubectl、文件编辑、原始 SQL 执行、任意 URL 请求或动态工具加载。业务工具是受审查的 Go 回调，声明只读不等于凭据/系统权限也只读；宿主必须提供范围受限的实现。

嵌入运行与宿主共享进程权限。核心能限制模型可调用的入口，不能在宿主进程被完全控制时提供 OS 隔离；不以拆服务为本项目集成前提。

## 文档

1. [架构与接口 RFC](docs/RFC-001-SERVER-RUNTIME.md)：嵌入分层、宿主接口、执行与恢复合同。
2. [威胁模型与权限边界](docs/THREAT-MODEL.md)：提示注入、权限校验和同进程边界。
3. [验收与开源边界](docs/ACCEPTANCE.md)：虚构示例、回归和源码引入要求。
4. [评审 001：受控操作能力的缺口](docs/REVIEW-001-OPERATION-CAPABILITY.md)：为何保留「无通用执行」，但需为具名受控业务操作留出协议位置。

本仓库采用 [MIT License](LICENSE)，来源说明见 [NOTICE](NOTICE.md)。实现引入 EasyAgent 源码时保留来源 LICENSE、版权和文件映射。当前参考提交为 `d90804fbe5cf488e6ace810cff925f7cc8b8c6d9`，升级按文件审查。
