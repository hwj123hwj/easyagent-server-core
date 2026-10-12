# 嵌入接入与运行示例

当前为开发预览：最小核心、可选 HTTP provider 与离线虚构示例已实现。没有数据库、Redis、后台恢复或生产业务适配器；不要把示例的内存去重与模拟批准当作生产实现。

## 1. 本地运行

需要 Go 1.24 或更高版本，无第三方 Go module 依赖。命令在仓库根目录执行：

```sh
go test -race ./...
go vet ./...
go run ./examples/embedded
go run ./examples/embedded -scenario diagnostics
go run ./examples/embedded -scenario pending
go run ./examples/embedded -scenario approved
go run ./examples/embedded -scenario duplicate
```

默认使用离线 scripted provider，不访问网络、不读取模型密钥、不产生真实操作。输出是公开事件 JSON 和执行状态。九个场景均有自动验证：

| scenario | 预期结果 |
| --- | --- |
| query | 业务助手查虚构七天成本、引用证据并分段回答 |
| history | 宿主传入上一轮公开消息，继续解释统计口径 |
| diagnostics | 运维助手查运行记录和产物，给证据与建议，零修改 |
| pending | 写操作需批准但没有 gate，返回 pending_approval，零副作用 |
| approved | 模拟宿主批准后登记虚构重试，再读取记录确认，一次副作用 |
| denied | 模拟批准拒绝，interrupted，零副作用 |
| unknown | 模拟操作生效后连接断开，保留 unknown，停止，不重试 |
| partial | 模拟部分完成，保留 partial，停止，不自行补偿 |
| duplicate | 同一虚构意图显式提交两次，宿主业务去重只产生一次副作用 |

denied/unknown/partial 是预期演示案例，CLI 正常退出但状态仍为 interrupted；真实接入必须查看 Outcome.State、Observations 和错误，不能只依据进程退出码判断业务成功。duplicate 的内存状态只在本次进程内有效。

## 2. 实际入口

源码为准：[model](../core/model/model.go)、[tool](../core/tool/tool.go)、[runner 类型](../core/runner/types.go)、[runner 执行链](../core/runner/runner.go)、[公开事件](../core/event/event.go)。完整接入实例见 [embedded](../examples/embedded/main.go)。

```go
// Provider adapts the existing host Router client; no Eino is required.
provider := model.ProviderFunc(func(ctx context.Context, req model.Request, emit func(string) error) (model.Response, error) {
    return hostModel.Generate(ctx, req, emit)
})

r, err := runner.New(runner.Definition{
    ID: "data-assistant", Version: "1", Model: "host-configured-model",
    System: "只使用当前账户可见数据，说明统计口径。",
    Tools: []tool.Tool{hostQueryTool},
}, runner.Dependencies{
    Provider: provider,
    Authorizer: hostAuthorizer,
})
// Check err before using r.
out, err := r.Run(ctx, runner.Request{
    Principal: tool.Principal{TenantID: trustedTenant, SubjectID: loggedInSubject},
    RunID: hostRunID, Mode: runner.Chat,
    History: authorizedHistory, Question: question,
}, hostEventHandler)
// Save/publish a successful round only if err == nil && out.State == runner.Completed.
```

示意代码的 hostModel 等变量由宿主提供，不是隐藏 SDK 或可独立编译的完整程序。单租户宿主使用可信配置的固定 TenantID 即可，无须另建企业 IAM；SubjectID 来自真实登录上下文。历史、问题和会话资格在宿主校验，Principal、Definition、OperationID 不接受模型或未核对的客户端字段。

两个助手使用不同 Definition 与工具集合。ChatInstructions/AgentInstructions 可由宿主设置，默认分别聚焦当前问答和多步调查。两种模式仅改变提示策略与预算，不改变业务授权。新建 Runner 时复制 schema；每次 Run 拥有独立历史。共享 Runner/连接时，宿主回调与 Provider 必须并发安全。

## 3. 参数、预算与结果

工具使用 Spec.Input 的受限 Schema：object、array、string、integer、number、boolean，必填、字符串枚举/长度和数组数量限制。每层 object 都拒绝额外属性；拒绝重复 JSON 键、尾随数据、无效 UTF-8 和不合法类型。数字长度/指数和嵌套深度有解析限制；不是完整 JSON Schema 引擎。日期、数值业务范围、目标归属和状态必须由宿主继续核验。

核心初版全部串行、无自动重试。默认 Chat 为 90 秒/8 次模型/12 次工具，Agent 为 3 分钟/16 次模型/24 次工具；二者写调用预算默认均为 0。预算还限制单次模型输入字节、累计模型/工具结果字节、每次最大输出 token 和重复读取次数。宿主显式提供 Budget 时必须完整且有限，不支持无限/负数。

写能力须同时注册具名 write 工具、提供 OperationID 回调并设置 MaxWrites；这些不代替 Authorizer。**每个 write 工具都必须提供 Prepare，无论是否需要人工审批**：它是资源、目标版本和影响范围的唯一来源，核心据此绑定操作并在鉴权后复核目标是否变化；缺少 Prepare 的 write 在注册阶段即被拒绝。免审批只表示不弹人工确认，不表示跳过资源核对。调用前保守预留工具声明的最大结果字节；因此剩余额度不足时即使实际返回很小，也会停止。HTTP provider 另限制请求与 SSE 原始字节，不据此承诺精确 token 或金额账单。

Outcome 为 completed、pending_approval 或 interrupted，包含已知 Observations。出错后保留已发生操作的记录；unknown/partial 不会交给模型继续操作。读工具错误转换为受限 unavailable 摘要；原始错误不进入模型或公开流。

证据引用格式为 [[evidence:id]]，核心只接受本次工具提供或 Request.EvidenceIDs 中宿主明确授权的 ID。其他文本引用或自然语言事实不保证正确；需要时用 ValidateFinal 施加业务规则。证据正文与读取权限在宿主维护。

## 4. 审批与业务去重

RequiresApproval 工具须提供只读 Prepare 回调，返回可展示的具体意图、资源与必要版本。核心构造 fingerprint，绑定账户、助手 ID/版本、稳定 OperationID、工具/版本、规范化参数及意图。

ApprovalGate 是可信宿主接口：验证真实审批人、批准记录、有效期和消费状态后，才返回 Approved、匹配 fingerprint 和 ExpiresAt。不能直接把浏览器提交的 fingerprint 或 approved=true 作为批准。核心再次鉴权、调用 Prepare 核对目标，再执行工具；业务接口还需按目标版本执行条件修改，避免检查与修改之间的并发变化。

无 gate 或 Pending 返回 approval.required 和 pending_approval，没有副作用。核心不保存挂起任务。继续时宿主加载原意图和有效批准，以相同 OperationID 显式发起执行；不要把完整未完成历史轮直接重放。批准中的工具、参数、范围或目标变化须重新确认。

OperationID 来自宿主可信意图，应持久绑定账户、工具和载荷；同一意图续跑保持 ID，不同意图用新 ID。真实业务接口以此去重、核对载荷冲突并查询结果；RunID、进程内锁和一次性批准都不能保证副作用只生效一次。示例 gate 是模拟批准，并未实现生产审批存储。

工具错误、超时、取消或不合法写结果可能意味着已生效：核心记录 unknown 并停止。已知 partial 也停止。宿主核对业务记录或交给人工，不自动再次执行；取消不回滚。后续回答/保存失败时同样核对 Observations 与业务记录，不能重做修改来“补回答”。

## 5. Provider 与流式约定

Provider.Generate 同步返回完整 Response；文本 emit 是增量，须按有效 UTF-8 边界分段，与最终 Text 一致。不得发私有推理或工具参数，不得在返回后调用 emit；收到 callback 错误或 context 取消应立即停止。工具 Progress 和事件 handler 也遵守同步约定，不能返回后继续发事件。

事件带 RunID/Turn/CallID，Turn 区分模型生成代次。delta 是临时预览；done 仅表示核心执行完成，持久成功由宿主保存后确认。UI 自行处理会话切换、迟到事件和预览替换。handler 错误会停止后续调用；工具客户端必须配合取消，核心无法强制杀死不合作的 Go 回调。

需要持久化时，handler 可立即转发 delta/进度，但应暂存 done；等 Run 返回 completed、保存完整公开轮成功后再向客户端发送最终成功事件。不要直接转发 done 后才检查保存结果。

可选 [HTTP provider](../providers/openai/openai.go) 使用配置的完整 Endpoint，不自动拼 /v1。通过固定 Headers 支持宿主 Router 约定；拒绝重定向，不自动重试。要求 Chat Completions SSE 的合法 finish_reason 和 [DONE]，length、断流、非法工具片段均失败；不支持 Responses、非 SSE JSON 或多候选输出。工具名的点转换为下划线，冲突在请求前拒绝。

HTTP 默认要求 HTTPS，允许 IP 回环测试地址；可信宿主可显式 AllowHTTP。外部请求须由用户/运维提供真实授权配置后自行开启：

```sh
# EA_MODEL_ENDPOINT / EA_MODEL_KEY / EA_MODEL 由宿主安全注入，不写入仓库。
go run ./examples/embedded -http -scenario query
```

离线示例只有显式 -http 才使用 HTTP provider。项目验证只访问本地 httptest 服务，尚未核验任何生产 Router 的具体字段合同。

## 6. 源码引入

复制 core/model、core/tool、core/runner、core/event，调整内部 import，保留 LICENSE/NOTICE，记录来源 SHA、文件映射和本地修改。providers/openai 可选；可以完全使用宿主现有模型客户端。不需要完整 EasyAgent SDK、Eino、go.sum 或外部服务。

[源码复制验收](../integration/source_copy_test.go) 在临时宿主 module 中改 import，关闭 module 网络下载，构建并执行真实模型/工具循环，同时检查核心直接依赖边界。测试专用的 Go 构建进程不会进入运行核心。

会话库、PG/Redis、后台调度、审批存储、业务审计和多 worker 接管均未内置。按宿主启用的能力落实 [验收计划](ACCEPTANCE.md)，生产集成前还需真实权限、模型与业务来源回归。
