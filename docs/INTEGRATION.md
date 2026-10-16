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

## 7. 宿主控制命令

`core/command.Parse` 在消息入口识别 `/new`、`/reset`、`/stop`、`/cancel`、`/status`、`/help`。它返回语义类型，不执行操作或调用模型；未知/带不支持参数的 slash 也应由宿主返回帮助，不能默默送给模型。嵌入、代码引用和 URL 中的 `/new` 仍是普通文本。此层无第三方依赖或传输/数据库假设。

宿主先校验真实身份和当前权限，再分派控制命令；保持输入去重和最终回复的独立去重。控制命令不应排在正在处理的普通问题之后。`new/reset` 必须持久切换会话代次，隔离旧历史并阻止旧执行的迟到结果写入新会话；`stop/cancel` 仅停止当前账户/通道会话的 AI 运行，不停止被诊断的业务任务。状态/帮助不应依赖模型在线。多 Pod、事务、租约 fencing、取消事件和来源权限仍归宿主，解析到命令不等于已执行成功。

消息通道的临时“处理中”反馈应覆盖正常完成、失败、取消和断线恢复的收尾；错误的反馈不能阻塞回答。删除自己已知的临时反馈与重复发送正文是不同操作：前者可核对目标后幂等收尾，后者仍须保留不确定状态、禁止盲重发。核心不绑定飞书等 IM SDK。设计参考本地 EasyCodeClient 的控制优先通道及 gateway 的 finally 收尾行为；未复制其编码执行工具或 TypeScript 实现。

## 8. 确认后执行固定操作

`Runner.Continue(ctx, runner.Continuation{Principal, RunID, Proposal}, handle)` 不调用模型，也不重放历史。宿主必须从自己的持久化台账加载原 `Proposal`，验证确认消息或回调的真实身份、通道、会话代次和有效期，然后才调用此入口；不能接收客户端任意 Proposal、布尔 confirmed 或模型自称批准。

核心核对原操作者/租户/RunID、当前工具版本与闭合参数 schema、原操作指纹、当前资源版本及稳定 OperationID，并复用 Run 的逐次授权和 ApprovalGate。只开放明确 RequiresApproval 的 write 工具；权限撤销、参数/目标变化、超期批准和未知结果均停止，不重问模型生成另一项操作。pending 返回 PendingApproval，未知副作用仍返回 ErrUncertain。最终结果来自宿主 Execute 的正式状态。

宿主仍负责在一个事务里消费批准、复用原业务接口执行、保存结果与审计，并保证重复确认不会重复执行。外部系统写入未知时保留 unknown，不能因网络重试重新 Run/Continue。该入口不内置 IM、存储、业务角色或通用执行工具。


## 流式预算与私有 provider 连续状态

可选 OpenAI-compatible provider 将 SSE 线协议预算（Config.MaxResponseBytes）与调用方解码后的回答/工具参数预算（Request.MaxResponseBytes）分开约束。密集小帧、usage 与私有连续字段不会挤占公开回答预算；公开增量在 emit 前逐段限额，完整回答及工具参数还需通过总输出预算。原始网络响应仍有独立上限，不能通过增加私有字段绕过内存限制。HTTP 错误仍只公开状态码，不传递响应正文；不增加第三方依赖。

部分兼容服务要求下一轮工具请求回传 `reasoning_content`。model.Message/Response 的 ReasoningContent 是不透明、只在同一运行内使用的传输状态；provider 接收 reasoning_content/reasoning 后只回传给模型，不触发文本增量、进度或公开 JSON。Runner 的输入预算显式计入该字段，provider 还会限制实际请求体。宿主不得用 fmt 输出整个内存对象，也不要另行持久化该字段。普通跨会话历史使用 JSON 序列化的公开消息，不能从保存的历史恢复该私有状态。

此变更不提供业务话术路由、会话数据库或自动执行。pending approval 仍由宿主保存原提案并发送用户确认；Continue 只执行固定的、重新鉴权和重新准备的原工具调用。业务权限、配置差异、飞书/其他渠道回调、事务和审计继续由宿主工具负责。

## 身份与工具权限接入

核心已经通过 `tool.Principal`、`Definition.Tools` 和 `Dependencies.Authorizer` 分开表达可信操作者、可见工具和实际执行授权，不需要新增平行角色表或把具体应用的管理员命名写进核心。

接到消息后，宿主先验证传输来源，再用真实登录会话或消息发送者标识查询本人。之后从宿主现有权限服务解析当前能力与数据范围。消息正文、历史文字、模型参数中的工号/角色，以及被 @ 的人都是业务输入；被 @ 的目标不能替换实际操作者。未知、停用或无法核实的发送者不能因为模型声称“已识别”而获得访问。

推荐执行顺序：

1. 宿主得到 `Principal{TenantID, SubjectID}`，在服务端绑定当前请求；这些字段不从模型或客户端指定的角色推断。
2. 根据当前能力构建 `Definition.Tools`。不具备写能力的用户不展示写工具，写预算为零；工具选择和预算由可信宿主设置。
3. 每次调用由 `Authorizer.CheckTool` 重新检查当前操作者、具体工具、规范化参数及资源所属租户/本人/组织范围。工具可见不代表已经授权，隐藏工具也不能代替后端鉴权。
4. `Prepare` 查询当前状态并形成明确的资源版本与影响；需要确认时把原提案展示给真实用户，不由模型自行批准。
5. 确认回调核对本人、租户、通道、会话代次、原预览与有效期，再使用原 `Proposal` 调用 `Continue`。核心仍会重新鉴权和准备；确认期间撤权不能被旧批准覆盖。
6. 业务读取/修改接口在其自身事务或条件查询中再次限权，过滤结果。保存最终结果、展示历史或恢复后台任务时也应复核当前范围，不能复用旧缓存授权。

| 宿主授权示例 | 可展示工具 | 数据边界 | 写操作 |
| --- | --- | --- | --- |
| 仅本人 | 用量、本人报告等具名查询 | 当前本人及租户 | 不注册写工具，写预算0 |
| 组织范围 | 对应组织的查询 | 当前已授权组织及租户 | 另有对应管理能力才提供 |
| 某项配置管理能力 | 该能力的具名查询与修改工具 | 宿主该项配置和目标资源 | 逐次鉴权；需确认的仍走审批 |

这些只是虚构集成模式，不是核心内置角色，也不表示每个宿主已开放这些业务功能。即使共用模型和核心实现，也不能共用不同用户的授权快照、可变历史、审批或工具闭包中的可变请求状态。必要时按请求/用户重新构建 Runner，或复用不可变定义并用当前请求身份执行鉴权；不要把一位管理员的工具配置当作所有账户的默认权限。

可执行的虚构宿主合同见 [identity_test.go](../core/runner/identity_test.go)：

```sh
go test -race ./core/runner -run TestHostIdentitySelectsToolsAndReauthorizesTheirScope
```

回归核对本人读取、同事越权、组织范围、跨组织/跨租户、隐藏工具被模型强行调用、正文/提及伪装管理员、请求身份替换、管理员待批准、有效批准和批准后撤权。此测试不接入真实登录或飞书，不代替宿主身份映射、业务接口、会话、历史与通知通道的生产验收。


## 按需工具披露

`Definition.Discovery` 是可选、与 provider 无关的渐进式 schema 披露；未设置时兼容原有静态工具集合。它只选择 `Definition.Tools` 中宿主已经注册并授权展示的工具，不下载代码、运行插件或接收任意工具定义。

```go
Discovery: &runner.Discovery{
    Groups: []runner.ToolGroup{
        {ID: "business.usage", Description: "业务：人员用量与费用", Tools: []string{"person.usage"}},
        {ID: "system.members", Description: "管理：查询和修改成员权限", Tools: []string{"member.read", "member.update"}},
    },
    MaxActiveTools: 4,
},
```

初始只展示未分组的常驻工具及核心生成的 `discover_tools`，其简短目录来自组 ID/描述；分组工具的描述和参数 schema 不会提前传给模型。模型调用 `discover_tools({"groups":["business.usage"]})`，下一次模型请求才出现对应的具名工具。组 ID 可以用前缀表达业务/管理等分类；核心不内置应用分类、角色或自然语言路由。配置不得含未知工具、重复分组 ID、重复工具归属或冲突的 `discover_tools` 名称。

每次发现替换此前选中的组，保持同一运行的历史和已产生的工具结果；可以同时选择多个组，但总数不能超过 MaxActiveTools（包含发现工具和未分组工具）。超限或重复组返回 invalid，保留旧选择；空数组卸载所有分组。未披露工具不能执行，即使模型猜出名称，或把发现与未披露调用放进同一响应也拒绝整批。发现不产生业务读取或写入，且照常消耗调用、时间、结果和重复读取预算，并通过 Authorizer。不会为了选择工具自动重试或另开 Agent。

选择状态是每次 Run 的私有状态；新的用户消息/运行应重新发现，不能从未核验的历史或旧会话恢复权限。历史工具结果可保留用于上下文，但它们不会激活工具或替代新鲜读取。宿主必须先按已认证身份过滤注册表、组及目录描述，不能把未授权工具名/敏感资源写进公共目录。每次实际调用仍走原 Authorizer 和业务范围检查，发现后撤权同样拒绝。不要因为发现工具不读业务就免除当前会话资格校验。

发现写工具只让模型能提出操作；写预算、Prepare、ApprovalGate 和正式业务事务不变。用户确认后仍加载宿主保存的原 Proposal，用 Continue 执行固定操作；无需恢复发现状态或再次调用模型，且必须重新鉴权。公开事件与 Observations 包含发现步骤，可与业务工具调用一起审计。

推荐先用少量能力组和有界活跃工具数，测量实际 schema 字节、调用正确率、越权拒绝、跨领域续聊、时延和预算耗尽。发现本身增加模型轮次，不承诺所有小工具库都更快。跨域任务可能分阶段切换组，不应无限累积工具。宿主的历史管理需保持完整 tool-call/result 对。附带的兼容 HTTP provider 会序列化已卸载工具的历史调用、拒绝历史别名冲突，并且只接受当前披露集合的新工具调用；历史名称不会重新取得执行资格。其他 provider 应落实相同兼容边界，不能通过恢复旧授权规避。

可执行示例 `go run ./examples/discovery` 使用虚构业务用量与队列事实，验证初始单一发现工具、下一轮加载、跨组切换、历史保留及有界执行。`core/runner/discovery_test.go` 额外覆盖隐藏/同批猜测、超限、重复、卸载、并发隔离、配置快照、撤权及固定写操作继续执行；均不接入真实消息或生产来源。
