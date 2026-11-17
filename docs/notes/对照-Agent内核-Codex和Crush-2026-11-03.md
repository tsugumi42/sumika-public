# 对照：Agent 内核 · Codex和Crush（2026-11-03）

> 用户 2026-11-03："把我们 sumika 的 Agent 核心和 pi、opencode v2 之类的做深度的架构解析对比，看看我们思路上缺少什么更现代优秀的设计。" 只读源码调研，Sumika 基点 1181083e；标了 [推断 / INFERENCE] 的是工程判断，没有实测。三份的合并结论和改造顺序见同日的交叉分析。

先说人话：**Sumika 的方向没有落后：自己写小而明确的 Go 回合循环，把角色、记忆和渠道行为放在循环之外，这条路线适合陪伴聊天。最值得向 Codex 和 Crush 学的，是把“这次运行到底用了什么状态、发生了什么、重启后还能认出什么”定义成贯穿各层的契约。** Codex 在请求快照、执行历史重建、权限与上下文来源上更完整；Crush 在服务装配、消息持久化和模型适配上更接近 Sumika，改造方式更容易借用。收益最大的改动是运行事件与快照、独立的执行上下文、明确的生命周期归属，以及统一的模型调用记录。

本次只读源码、规格和官方网络资料；未修改文件、未访问 `sumika/data`、未编译、未运行程序或测试。下文实现事实属于 **[读码]**；“差在思路”和改造建议属于 **[推断 / INFERENCE]**，没有性能、费用或效果实测。

先区分两处现状：

- `docs/subsystems/runtime.md`、`agent-and-console.md` 的主体仍描述旧 Bun/Cordis/pi 实现；Go 现状以 `native/internal` 为准，不能把旧文档里的话题分叉、事件推送直接算作 Go 已实现。
- [ADR-242](仓库根/docs/决策记录.md:2878) 已决定：同一角色在本地私聊、本地群聊、QQ 私聊、QQ 群都能召回全部记忆，并标来源。当前读取的 [Recall](仓库根/native/internal/memory/recall.go:390) 仍调用旧的 `AllowedScopes`。这是“已定方向”和“所读实现”的差别；下文不把旧的场合过滤当成应保留的架构约束。工具执行授权、工作区资料读取权限仍须单独处理。

## 1. 回合循环：三家都是循环，区别在状态由谁持有

**Codex 怎么做。** 主体仍是 `run_turn` 循环：准备请求→模型采样→处理输出和工具→检查是否有工具续接、待输入、压缩或停止钩子→继续或结束。状态分属 `Session`、`TurnContext`、`StepContext` 和活动任务，不靠一个全局状态枚举表达全部行为。`SessionTask` 负责运行和中止，`StepContext` 保存一次请求的确定视图。出处：turn.rs:161（私有存档）、turn.rs:474（私有存档）、tasks/mod.rs:173（私有存档）、step_context.rs:22（私有存档）。

工具输出项进入处理路径后，运行时保留对应的在途任务；进入下一次请求前收齐结果。结束条件还包括待输入和钩子决定，因此“一次模型不调工具”不必然等于整个运行结束。turn.rs:3040（私有存档）、turn.rs:3450（私有存档）。

**Crush 怎么做。** `coordinator` 管模型、工具和服务装配，`sessionAgent.Run` 管会话排队与落库，真正的模型—工具循环交给 Fantasy。`PrepareStep` 在请求边界刷新工具、接纳排队输入；`StopWhen` 判断上下文压力和重复调用。带 `RunID` 的排队请求保留自己的结束事件，没有 `RunID` 的普通跟进输入可以合入当前回合。出处：agent.go:854（私有存档）、agent.go:866（私有存档）、agent.go:1108（私有存档）、[Fantasy Stream](https://github.com/charmbracelet/fantasy/blob/v0.45.1/agent.go#L914)。

**Sumika 怎么做。** `Hub` 按“角色 × 会话”排回合；`Live` 持有上下文、插话、取消句柄和当轮状态。`runTurn` 每步发一次模型请求，执行工具，再接纳插话；没有工具也没有插话便结束。`Send` 的新输入会打断当前输出，渠道侧另行决定插话还是停止后重开。出处：[hub.go:87](仓库根/native/internal/turn/hub.go:87)、[hub.go:261](仓库根/native/internal/turn/hub.go:261)、[hub.go:1376](仓库根/native/internal/turn/hub.go:1376)。

**差在思路／值不值得借。** 不需要为了“现代”把循环改成工作流图或大状态机。值得借的是**每个边界都有明确所有者**：会话拥有历史，运行拥有取消与结局，请求拥有有效配置和工具目录，工具调用拥有执行结局。Sumika 已有这些对象的部分形态，但跨层契约还分散在字段、锁和调用顺序中。先补明确的运行身份、请求快照与退出等待，比重写循环收益大。

## 2. 事件与流式：统一身份，比统一总线更重要

**Codex 怎么做。** 内核通过类型化 `EventMsg` 发事件；`send_event` 附上当前回合身份，事件先经过持久化策略再交给消费者。App Server 按线程维护监听器和当前回合投影；向已运行线程发送恢复响应与订阅增量，放在同一监听器顺序中处理。出处：session/mod.rs:2515（私有存档）、session/mod.rs:2736（私有存档）、thread_state.rs:63（私有存档）。

这里有两个限制：

- `Event.id` 在该路径中承载回合／提交身份，不能当作整个事件流的递增游标。
- 流式正文、思考增量、审批请求等属于瞬态事件，不全都持久化。恢复依靠已保存的消息、完成项和状态投影，不是逐 token 重放。rollout/policy.rs:133（私有存档）。

**Crush 怎么做。** 消息、会话、审批等服务各自发布类型化事件，再由 Backend 汇集、Server 转成协议载荷。消息增量在 33 毫秒窗口内合并，终态和结构变化同步刷库。普通发布允许丢失；`PublishMustDeliver` 有界等待，超时后仍可能丢失，源码明确要求消费者能够重新取状态。出处：message.go:15（私有存档）、broker.go:1（私有存档）、backend/events.go:18（私有存档）、server/events.go:24（私有存档）。

**Sumika 怎么做。** 目前有三套不同用途的通知：

1. `Config.OnDelta(text)`、`OnTool(id,name)` 是全局回调，本身不带角色、会话、回合身份。
2. 插件的 `Seen`、`Settled` 带身份，按订阅者独立排队，但队列不限长。
3. 审批已有版本化状态、快照和原子订阅重放；桌面聊天展示也已有版本化快照和水位增量。

出处：[turn.go:57](仓库根/native/internal/turn/turn.go:57)、[plugin.go:177](仓库根/native/internal/turn/plugin.go:177)、[approval/subscribe.go:46](仓库根/native/internal/approval/subscribe.go:46)、[desktopread/service.go:337](仓库根/native/internal/desktopread/service.go:337)。

**差在思路／值不值得借。** **Sumika 已经有可复用的恢复订阅设计，缺的是把运行状态接入同一种契约。** 不必新建全局消息总线。运行事件统一带角色、会话、运行、步骤、工具调用身份；消费者先取得运行快照，再接增量；结束状态能从存储重建。

正文仍是纯增量时不能直接丢包。要合并，必须保留累计文本或可核对的偏移和版本。`Seen` 则适合逐步转成“某会话已更新到哪个水位”，让消费者按游标补读，避免靠无限队列保证不丢。

## 3. 会话与持久化：公开聊天记录和模型执行历史需要分开

**Codex 怎么做。** 每个线程有追加式 rollout，保存模型消息、工具调用与结果、请求上下文、压缩检查点和必要状态。恢复时重建当前模型窗口，并一起恢复有效设置、窗口身份和上下文基线；回滚要跳过被撤销的回合段。分叉存在复制历史、引用历史前缀等持久化路径，不应笼统说成“每条消息都是一棵树”。出处：rollout/policy.rs:22（私有存档）、rollout_reconstruction.rs:11（私有存档）、rollout_reconstruction.rs:134（私有存档）、session.rs:1085（私有存档）。

持久化通过 `LiveThread` 收口，内核不直接依赖“本地 JSONL 文件”这一具体存法。live_thread.rs:32（私有存档）。

**Crush 怎么做。** SQLite 中保存有序消息及内容块，消息包括工具调用、结果、思考签名、附件和结束原因。`Session.ParentSessionID` 支持子会话关系，`SummaryMessageID` 指定当前上下文的摘要起点。进程中断留下的未配对工具调用，会补成明确的中断结果；发给模型时把结果重新排到相应调用后面。出处：session.go:52（私有存档）、message.go:40（私有存档）、settle.go:141（私有存档）、agent.go:1583（私有存档）。

**Sumika 怎么做。** 活实例里的 `Msg` 能保存工具调用、结果和思考；冷重建经过 `history.For`，主要得到 `user/assistant` 文本和摘要。执行步骤、模型调用另存 trace，但它们目前不是完整的模型上下文恢复来源。启动时把遗留回合记为中断、运行中工具记为结果未知、待批工具记为取消，不自动重跑。出处：[loop.go:31](仓库根/native/internal/turn/loop.go:31)、[history.go:122](仓库根/native/internal/history/history.go:122)、[turn.go:393](仓库根/native/internal/turn/turn.go:393)、[store/turns.go:55](仓库根/native/internal/store/turns.go:55)。

**差在思路／值不值得借。** 现在“公开说过的话”和“代理执行过的事”在热实例里相连，冷重建时却退回聊天视图。日常闲聊可以接受，资料整理和本地工作容易失去证据、步骤和结果引用。

值得借的是**独立的执行上下文序列**：聊天 Events 继续记公开对话；trace 保存恢复所需的模型消息、工具调用与结果引用、压缩切点和配置版本。恢复重新组装上下文，不能把恢复理解成再次执行工具。

Codex 也不是任意位置的透明续跑：其暂停交接明确丢弃进程内待输入和交互等待状态，另有适用环境限制。turn_suspension.rs:82（私有存档）、daemon_recovery.rs:22（私有存档）。Sumika 当前“不重跑未知副作用”的原则应保留。

## 4. 上下文管理：把可重建材料、摘要和请求投影分清

**Codex 怎么做。** `ContextManager` 保存模型历史及宿主保留信息，请求使用历史投影；不可用图片可以在投影中替换，不改变保留历史。工具输出带原始截断预算，恢复后沿用原口径。异步压缩候选绑定历史版本、模型、压缩版本和认证所有者，接纳时再次核对。出处：history.rs:100（私有存档）、session/mod.rs:3792（私有存档）、async_compaction.rs:24（私有存档）。

其图片预处理统一覆盖用户消息和工具结果，记录处理后的尺寸等元信息；这比“在每个工具里各自塞图片”更一致。image_preparation.rs:127（私有存档）。

**Crush 怎么做。** 历史内容先转换成 Fantasy 消息，再做工具调用配对、模型媒体能力处理和缓存标记。压缩生成摘要消息，成功后更新 `SummaryMessageID`，后续只读取摘要及其后的消息。待办会进入摘要请求。缓存控制按提供方设置。出处：agent.go:1374（私有存档）、agent.go:1525（私有存档）、agent.go:1572（私有存档）、agent.go:1720（私有存档）。

**Sumika 怎么做。** 固定前缀与追加参考信息分开，报时和关系说明尽量只追加；软门槛只精简当前请求，不改活历史。压缩保存新摘要与原文切点，原文保留；提前压缩使用复制出来的材料。当前压缩材料主动去掉参考信息、工具调用、结果和思考，并固定使用聊天摘要。主循环 `Msg` 和序列化以文本为主，渠道消息也先排成文本行，图片在这条路径中是占位。出处：[hub.go:1230](仓库根/native/internal/turn/hub.go:1230)、[slim.go:10](仓库根/native/internal/turn/slim.go:10)、[compact.go:37](仓库根/native/internal/turn/compact.go:37)、[compact.go:180](仓库根/native/internal/turn/compact.go:180)、[channel/line.go:1](仓库根/native/internal/channel/line.go:1)。

**差在思路／值不值得借。** Sumika 已有缓存友好的请求投影和摘要纪元。下一步应把**不同材料的生命周期**表达清楚：

- 闲聊摘要：人物、约定、话题和未完成交流。
- 工作摘要：目标、约束、已完成步骤、证据引用、待处理问题。
- 记忆、项目说明等可重建资料：压缩后重新取得当前版本，并重新记接纳账本。
- 长工具结果：保留可读取的结果引用，摘要只保留结论和引用。

不能简单把全部工具结果塞进共享聊天摘要；ADR-242 放开的是角色记忆召回，不自动授予 QQ 读取本地工作资料的能力。

Codex 的 WebSocket 增量请求还会核对请求属性和完整前缀是否一致，失败就退回完整请求。client.rs:1616（私有存档）。这种优化依赖接口协议，Sumika 当前更值得先保住材料分层和稳定前缀。

## 5. 工具系统：声明、执行、授权、沙箱是不同边界

**Codex 怎么做。** 工具目录与执行运行时分开，`StepContext.tool_router` 保存本次向模型公布的确定目录。普通工具失败可以转为模型可见结果，致命执行错误另行传播。并行工具取共享执行锁，独占工具取写锁，使独占调用形成执行屏障。审批、沙箱选择和沙箱拒绝后的重试由专门的 orchestrator 管理，扩大执行权限需要重新评审。出处：registry.rs:60（私有存档）、parallel.rs:118（私有存档）、parallel.rs:281（私有存档）、orchestrator.rs:1（私有存档）。

**Crush 怎么做。** 工具定义交给 Fantasy；其流式执行路径先读完流、确定结束原因，再校验和调度工具，避免执行被截断的参数。并行路径有容量 5，结果按调用顺序重排；不要据此推断它的串行工具具有 Codex 那种读写锁屏障。出处：[Fantasy 工具调度](https://github.com/charmbracelet/fantasy/blob/v0.45.1/agent.go#L1664)。

权限服务按调用 ID 接受一次性或会话授权，但审批请求存在全局 `requestMu`。Hook 可以重写输入、阻止工具或预批准调用。出处：permission.go:106（私有存档）、permission.go:189（私有存档）、hooked_tool.go:55（私有存档）。

**Sumika 怎么做。** 工具声明保留从 Bun 导出的 JSON Schema；统一入口处理参数 JSON、工具存在性、截断结束原因、审批和执行时限，字段语义仍由具体工具处理。插件工具绑定当轮注册对象，执行前复核身份和听众，同名重注册不能接替旧调用。混合批次里有串行工具便整批串行；纯并行批次为各调用启动协程。出处：[tools.go:15](仓库根/native/internal/tools/tools.go:15)、[loop.go:534](仓库根/native/internal/turn/loop.go:534)、[plugin.go:335](仓库根/native/internal/turn/plugin.go:335)。

审批和路径风险检查不能等同于系统沙箱。当前 PowerShell 路径直接启动进程，工作目录与环境来自宿主，取消时终止进程树。[shell.go:41](仓库根/native/internal/tools/shell.go:41)、[shell.go:148](仓库根/native/internal/tools/shell.go:148)。

**差在思路／值不值得借。**

- **值得借 Codex 的执行屏障，借 Fantasy 的容量上限。** 两者分别解决副作用顺序和资源数量，不能互相替代。
- 保留“给模型看的声明”和“执行前必须核验的授权”两道边界。模型看不到工具不构成授权保护。
- 长结果的模型预算、持久化预算和全文保留分别定义。Codex 自己也截断部分持久化命令/MCP 输出；Crush 的 shell 大输出会另存全文并附读取路径。Codex 持久化策略（私有存档）、Crush truncate.go:46（私有存档）。
- 本地执行面扩大时再接操作系统沙箱；QQ 场合继续通过能力授权限制，不能靠沙箱替代渠道权限。

## 6. 扩展模型：编译内插件、技能、外部工具不应共用一个信任等级

**Codex 怎么做。** 内核扩展可贡献模型请求拦截、工具策略和生命周期行为；技能负责提供上下文；MCP 提供外部工具和资源。执行器 Hook 来源经过当前插件选择和 MCP 策略筛选，并绑定到请求快照。启用工具搜索时，MCP 工具可以延迟暴露，而非把所有 schema 都塞进每个请求。出处：model_request.rs:11（私有存档）、hook_runtime/executor.rs:14（私有存档）、mcp_tool_exposure.rs:158（私有存档）。

**Crush 怎么做。** MCP 管理连接、目录刷新、重连与配置协调；配置变动时比较当前连接配置，撤销旧代次再重启。连接与目录是进程全局状态，会话工具清单另做过滤。技能目录标记 system/user/project 来源；Hook 是用户配置的命令，按可信本地命令执行，并且子代理内部不触发这套 Hook。出处：mcp/lifecycle.go:23（私有存档）、mcp/tools.go:30（私有存档）、skills/catalog.go:11（私有存档）、hooks/runner.go:157（私有存档）、hooked_tool.go:27（私有存档）。

**Sumika 怎么做。** `turn.Plugin` 是编进程序的 Go 代码，通过小型注册接口提供上下文、工具、观察通知和压缩前处理。每个注册可撤销；工具权限类别由中央 `policy.Specs` 决定，插件不能自行声明更宽权限。项目说明插件额外核对工作区信任、根目录身份和接纳代次。出处：[plugin.go:3](仓库根/native/internal/turn/plugin.go:3)、[plugin.go:273](仓库根/native/internal/turn/plugin.go:273)、[projectcontext/plugin.go:121](仓库根/native/internal/projectcontext/plugin.go:121)。

**差在思路／值不值得借。** 当前编译内插件设计适合 Sumika，不需要插件框架或运行时 Go 插件加载。以后接 MCP，应复用现有工具门，同时保存“来源服务＋目录版本＋连接代次”的绑定；撤销旧服务后，旧结果不能被新连接冒名接纳。

技能仍是上下文来源，不能因为技能要求执行某操作就获得权限。安全检查也不能放进可能被跳过、超时后放行或对子代理不运行的可选 Hook。Crush 的这些取舍适合本地用户定制，不能直接承担 QQ 的能力边界。

## 7. 子代理与多代理：该借任务归属，不必借完整代理社会

**Codex 怎么做。** 一个根代理树共用运行时，区分整树 `SessionId` 与各线程 `ThreadId`；控制器管理身份、容量、发送、恢复和结果路由。孩子配置来自调用步骤的有效设置，角色覆盖后重新应用运行时权限。创建中断时，`PendingSpawn` 负责清理尚未交付的孩子和持久化归属。出处：control.rs:95（私有存档）、child_config.rs:106（私有存档）、child_config.rs:181（私有存档）、spawn_guard.rs:1（私有存档）。

整树预算由控制器累计；孩子终态先捕获，再路由给父线程。完成通知源码明确是尽力投递，入队成功也不等于父代理已处理。budget.rs:1（私有存档）、completion.rs:1（私有存档）。

**Crush 怎么做。** `agent` 工具创建独立子会话，输入主要是明确的任务 Prompt；父消息 ID 和工具调用 ID 派生子会话身份。孩子复用同一执行器，结束后返回正文，费用尽力累加到父会话。这个路径是等待工具完成的委派，复杂度低于 Codex 的代理树和邮箱体系。出处：agent_tool.go:26（私有存档）、coordinator.go:1648（私有存档）。

**Sumika 怎么做。** 当前内核入口围绕角色与聊天会话；插件 `ToolCall` 已有回合、听众、工作区和上下文代次，但没有直接交付工具调用 ID。已有子代理规格明确提出独立内部上下文、可信委派身份、结果邮箱、一个 Hub 承载父子，以及不自动恢复被杀任务；该规格仍标草案，不能算实现。出处：[plugin.go:110](仓库根/native/internal/turn/plugin.go:110)、[规格-子代理.md:24](仓库根/docs/规格-子代理.md:24)、[规格-子代理.md:104](仓库根/docs/规格-子代理.md:104)。

**差在思路／值不值得借。** 已有规格选的方向合适，补充本次研究不需要重新开路线。优先借：

- 父工具调用身份贯穿受理、去重、预算和结果回收。
- 创建、运行、取消、收尾都有人持有任务归属，创建失败不能遗留孩子。
- 子代理使用独立执行上下文，过程不进入普通聊天和记忆整理。
- 结果回到原始角色与会话，再经过渠道发送流程。

陪伴聊天需要的是“去查资料，查完回来告诉她”。自由代理互发消息、全历史分叉、常驻多层代理树暂时没有足够收益依据。不同角色之间的记忆关系也不能照搬成代理树共享上下文。

## 8. 模型提供方：统一能力和调用记录，比支持多少协议更关键

**Codex 怎么做。** `ModelClient` 持有会话级提供方、认证和传输状态，`ModelClientSession` 管回合级连接与续接。模型设置显式传入请求；认证所有者变化会清除连接、增量历史和路由状态。重试与 WebSocket→HTTPS 退路集中处理。它主要围绕 Responses 协议和提供方能力工作，不能当作任意模型协议的通用适配器。出处：client.rs:1（私有存档）、responses_retry.rs:58（私有存档）。

**Crush 怎么做。** Fantasy 统一语言模型接口，Coordinator 选择 OpenAI、Anthropic、Google、Bedrock 等协议适配器；Catwalk 提供模型能力与价格。模型集合整体更新，重试可刷新凭证，但用户切换模型等到下一轮。用量区分实报与估算，估算不累计费用；提供方实报费用可以覆盖本地价格计算。出处：coordinator.go:989（私有存档）、coordinator.go:1298（私有存档）、shared_models.go:23（私有存档）、agent.go:1964（私有存档）。

**Sumika 怎么做。** `llm.Client` 是 OpenAI 兼容接口的薄包装，不持有历史；SDK 自动重试关闭，由回合层决定是否以及何时重试。已有缓存键、思考档位、流空闲检测、`Retry-After`、结构化思考和用量解析。正式装配默认建立 Magpie 来源。回复请求记录较完整，记忆后台适配器目前只返回正文。出处：[llm.go:1](仓库根/native/internal/llm/llm.go:1)、[llm.go:81](仓库根/native/internal/llm/llm.go:81)、[llm/retry.go:54](仓库根/native/internal/llm/retry.go:54)、[app.go:268](仓库根/native/internal/app/app.go:268)、[guarddeps.go:145](仓库根/native/internal/app/guarddeps.go:145)。

**差在思路／值不值得借。** 对当前依赖兼容网关的产品，直接引入多套原生协议并不必然更好。先把每次实际模型调用统一成同一口径：

- 有效模型、来源、岗位、思考设置与能力。
- 请求尝试和所属运行的关系。
- 成功、失败、取消及是否收到用量。
- 实报用量、估算用量、金额估算分别标识。
- 重试和后台压缩同样记入消耗；弃用结果不等于没有花费。

限流也要区分提供方 `429` 退避、进程并发容量与整轮预算。当前源码能证明各自部分机制，不能据此宣称三家都有完整的主动限流或公平调度。

## 9. 取消、并发、错误与测试：明确“停止等待”和“工作已停止”

**Codex 怎么做。** 任务、工具调用、背景压缩分别持有取消和退出追踪。工具运行时可以声明取消后必须完成收尾，调度层据此选择等待或中止。异步压缩关停先关闭追踪、取消网络工作，再等待用量收尾。出处：registry.rs:69（私有存档）、parallel.rs:361（私有存档）、async_compaction.rs:213（私有存档）。

**Crush 怎么做。** 对异步入口额外追踪“已受理但尚未进入执行”的请求，并用接纳序号判定一次取消覆盖哪些排队输入。活动取消句柄以对象身份比较删除，防止旧回合收尾清掉新回合。最终写入使用脱离运行取消、但另有期限的上下文。出处：agent.go:198（私有存档）、agent.go:348（私有存档）、agent.go:1129（私有存档）。

**Sumika 怎么做。** 每轮独立取消，插话与收尾在锁内交接；工具超时可以先返回，未响应取消的函数可能继续运行。插件通知超时后继续投递下一条，旧回调也可能仍在运行。记忆插件自己补了准入关闭、取消、计时器停用和 `WaitGroup` 排空；Hub 的 `Idle` 只等提前压缩，装配另用 `trackedKernel` 等回合。出处：[loop.go:719](仓库根/native/internal/turn/loop.go:719)、[plugin.go:641](仓库根/native/internal/turn/plugin.go:641)、[memplugin/plugin.go:298](仓库根/native/internal/memplugin/plugin.go:298)、[hub.go:205](仓库根/native/internal/turn/hub.go:205)、[app.go:474](仓库根/native/internal/app/app.go:474)。

**差在思路／值不值得借。** Sumika 在叶子组件里已经做出较好的生命周期管理，缺少内核级的统一归属。应让 Hub 明确覆盖准入、排队、活动回合、背景工作和收尾；超时返回必须说明是否仍有执行在途。通知回调应只排队或标脏，耗时任务由订阅者自己的生命周期管理。

测试方面，三家都有可控模型基础：

- Codex 用 HTTP/WebSocket 假服务并捕获实际请求：responses.rs:40（私有存档）。
- Crush 用临时 SQLite、可替换模型和 VCR：common_test.go:32（私有存档）。
- Sumika 已有真 Hub＋真 store＋假网关的跨包夹具，以及真实 SSE 录像回放：[turntest.go:1](仓库根/native/internal/turn/turntest/turntest.go:1)、[replay_test.go:3](仓库根/native/internal/llm/replay_test.go:3)。

这里不缺新测试框架。以后改契约时，优先验证取消交接、快照订阅、冷重建和副作用未知状态，不继续扩大逐字对拍范围。本次只读了这些测试实现，未执行。

## 10. 特别值得借的设计：信息来源不能被摘要或复制改变身份

**Codex 怎么做。** 宿主保留信息独立于模型窗口；原始用户消息、继承材料、检查点副本和压缩输出有不同来源。恢复时不会因为某段短文本来自检查点，就把它认成完整原始授权；继承给工作代理的父历史也不会自动变成工作代理的新授权。出处：history.rs:100（私有存档）、history_user_authorization.rs:1（私有存档）、history_user_authorization.rs:151（私有存档）。

**Crush 怎么做。** 技能目录有来源标签，持久消息也区分文本、工具、附件等内容块；但部分渠道信息仍从 Prompt 中的 `<channel>` 元素解析回路由元数据。这个实现更轻，类型边界没有全部贯穿。skills/catalog.go:11（私有存档）、channelreply.go:41（私有存档）。

**Sumika 怎么做。** 已有值得保留的机制：`Msg.Kind` 区分公开发言、参考信息、工具结果和摘要；`Controlled` 只能由宿主赋予；`ContextID` 标识上下文代次；贡献先作为候选，真正进入上下文后才执行 `OnAccepted`。记忆插件据此避免把超时、取消或未接纳的记忆提前记成“已经给过”。出处：[loop.go:44](仓库根/native/internal/turn/loop.go:44)、[plugin.go:49](仓库根/native/internal/turn/plugin.go:49)、[plugin.go:545](仓库根/native/internal/turn/plugin.go:545)、[memplugin/plugin.go:405](仓库根/native/internal/memplugin/plugin.go:405)。

**差在思路／值不值得借。** Sumika 的接纳点设计已经接近 Codex 的做法；下一步是让来源信息跨过纯字符串拼接、压缩和恢复：

- 这条材料来自谁、哪个会话、哪个事件或工具调用。
- 是原话、模型摘要、外部网页、长期记忆还是受信任项目说明。
- 使用了哪个版本，是否已被更正或撤销。
- 被复制、摘要以后仍保持原来源身份。

这尤其适合陪伴产品。长期聊天最容易混淆的是“她猜的”“别人说的”“主人确认的”和“她自己总结的”。按 ADR-242 给记忆标来源，应沿已有 `Contribution`、接纳账本和记忆证据结构推进；来源标记帮助判断，不等于消除了模型误判。

## Sumika 该借的几条：按收益排序

以下是架构建议，不改现有排期。

### 1. 把运行事件接到已有的版本化快照契约

**现在的痛点：** 活动回合分散在全局回调、插件通知、数据库和界面状态里；多会话并行、重连和取消时，没有一份统一的运行视图。

**借什么：** Codex 的线程／回合／条目身份与有序监听器；复用 Sumika 审批和桌面展示已有的快照订阅方式。

**大概怎么改：** 排队受理时分配运行身份；事件携带角色、会话、步骤、调用 ID；提供活动运行快照和结束状态。增量用于显示，持久结局用于恢复。先替换 `OnDelta`、`OnTool` 的无身份契约，不新建总线框架。

**涉及包：** `turn`、`api`、`desktopread`、`app`；审批沿用已有模型。

**风险：** 快照与订阅衔接不原子会漏结束；流式合并处理不当会重复或丢字。必须区分“模型生成完成”和“渠道发送完成”。

### 2. 给本地工作和未来孩子准备独立的执行上下文

**现在的痛点：** 活实例保留的工具往返，冷重建后主要变成聊天文本；执行证据与公开聊天之间缺少稳定恢复关系。

**借什么：** Codex 的模型历史重建、Crush 的结构化持久消息。

**大概怎么改：** 在 trace 中保存必要的模型消息序列、工具调用与结果引用、压缩检查点和有效配置版本；聊天 Events 继续由既有本地／渠道路径写。先覆盖实际需要恢复的本地工作路径，复用同一循环。孩子以后使用同一执行上下文机制。

**涉及包：** `turn`、`history`、`store`、`tools`；`channel` 保留现有 Commit 边界。

**风险：** 把恢复做成重执行会重复副作用；把工作结果写进聊天视图会改变可见范围。思考正文是否保存沿现有策略，不以恢复为由默认全存。

### 3. 统一有效请求快照与实际调用记录

**现在的痛点：** 模型、思考档位、工具目录、后台调用与使用记录由不同入口处理；难以解释一次请求究竟用了什么，也容易让后台调用脱离记账。

**借什么：** Codex 的 `StepContext`、Crush 的模型集合整体更新和明确的用量来源。

**大概怎么改：** 每次请求前确定有效模型、档位、工具绑定、工作区和上下文版本；工具执行沿用该请求公布的绑定，同时检查撤销。实际模型请求统一返回和记录用量是否已知、路由、耗时及错误。回复、压缩、整理、守卫使用同一记录入口。

**涉及包：** `turn`、`llm`、`app`、`store`；`memory.Generator` 的返回契约。

**风险：** 冻结全部状态会让撤销失效；边执行边读所有最新配置又会改变已公布契约。应明确“本次请求固定什么、执行前重查什么”，无需引入复杂配置层。

### 4. 把准入、取消和排空归还给工作所有者

**现在的痛点：** 装配负责补等 Hub 回合，插件各自管理排空；无限通知队列和无总容量的工具批次，把资源约束藏在调用方。

**借什么：** Codex 的任务追踪和创建失败清理；Crush 的受理—排队—执行交接；沿用 Sumika 记忆插件的关闭顺序。

**大概怎么改：** Hub 提供停止准入、取消和排空；涵盖排队回合、活动回合、提前压缩和必要写库。工具执行加容量，独占工具保留屏障。观察通知尽量按会话水位合并，由消费者补读。

**涉及包：** `turn`、`app`、`channel`；消费者侧主要是 `memplugin`。

**风险：** 关闭后仍登记工作、先关库后收尾、取消只停止等待而未停止执行。控制生命周期与限制容量可以逐步补，不需要统一全部后台任务的调度框架。

### 5. 让上下文来源和接纳账本贯穿压缩、恢复与记忆

**现在的痛点：** 贡献在入口有来源和接纳代次，拼成文本后来源信息容易变弱；长期记忆、摘要和原始发言可能被模型混为一谈。

**借什么：** Codex 对原始材料、继承材料和摘要副本的区分；保留 Sumika 已有 `OnAccepted` 机制。

**大概怎么改：** 在现有贡献和结果引用上保留来源、版本、原始事件引用及生成方式；压缩只改变表达，不改变来源身份。按 ADR-242 渲染每条记忆的来源，重建后再核对当前版本。

**涉及包：** `turn`、`memplugin`、`memory`、`projectcontext`、`linkplugin`、`store`。

**风险：** 把来源标签当成授权依据，或让摘要伪装成主人确认。不要新建通用“信任引擎”；先使用现有证据和版本结构。

## 以后再说：哪些设计不宜照搬

| 设计 | 适用时机与 Sumika 的取舍 |
|---|---|
| 通用历史树、任意回滚与全历史分叉 | 本地写作、研究工作需要时再做。QQ 是已经发生的交流，回滚模型历史不会撤回平台消息、文件修改或他人的认知。 |
| Codex 完整代理树、邮箱、常驻代理和层级恢复 | 先按现有子代理规格完成有界、单层、独立任务。陪伴聊天暂不需要代理之间持续自由交流。 |
| 任意外部 Hook、插件市场 | 当前编译内插件够用。引入第三方可执行代码时才需要额外隔离、资源和安装生命周期；不能让 Hook 承担唯一权限边界。 |
| 原生多协议模型适配与 WebSocket 增量续接 | 当前兼容网关确有无法解决的能力或延迟问题时再做。先统一能力、快照和调用记录；不能因别家有多套适配器便搬进来。 |
| 操作系统沙箱 | 扩大本地执行面前值得专门做；需要 Windows 实际能力验证。它保护本地执行环境，不能替代 QQ 委派授权。 |
| 主循环统一多模态内容块 | 主回合实际接入图片、音频或文件内容时采用。媒体保存、背景识别与主模型看图是不同路径，不能把“已下载附件”算成“主模型理解了附件”。 |
| 用统一事件日志替换所有存储 | 当前没有必要。聊天、执行、记忆、发送计划各有业务含义；统一身份和恢复契约即可，不必把全部状态改成一种存法。 |

尤其应保留 Sumika 已有的渠道边界：**模型生成→渠道裁决和改写→写聊天与发送计划→`Hub.Commit`→开始发送**。[settle.go:1](仓库根/native/internal/channel/settle.go:1)、[settle.go:214](仓库根/native/internal/channel/settle.go:214)。Crush 的渠道自动回复主要围绕 MCP 回复工具和已完成工具集合；Sumika 对群聊试运行、拦截、拆条和发送历史的处理更符合自己的产品需求，不应为了统一 Agent 接口而压回模型循环。

## 源码、文档与版本

以下是读取时记录的本机仓库 HEAD；分析以实际读到的工作树文件为准，未核验工作树与 HEAD 完全一致。

| 项目 | 本机目录 | HEAD |
|---|---|---|
| Sumika | `仓库根` | `0fafdef501501e069f10c5d8f54fcc8ecb94f75e` |
| Codex CLI | `_external/codex` | `7ac6081623928adead75caa2882e307444edad82` |
| Crush | `_external/crush` | `136d4a6914395bd44ca153917ea2eb2f36cdd7c6` |
| Fantasy，Crush 的循环依赖 | 上游 `charmbracelet/fantasy` | `v0.45.1`，版本由 Crush `go.mod` 核对；源码通过网络读取 |

主要深读范围如下；正文链接给出具体文件与行号。

| 项目 | 源码范围 |
|---|---|
| Sumika 回合与工具 | `native/internal/turn/{turn,hub,loop,plugin,compact,slim,retry}.go`；`tools/{tools,exec,shell}.go`；`approval/subscribe.go`；`history/history.go`；`store/turns.go` |
| Sumika 渠道、装配和模型 | `channel/{respond,settle,line}.go`；`app/{app,guarddeps}.go`；`llm/{llm,retry,replay_test}.go`；`desktopread/service.go` |
| Sumika 记忆与上下文 | `memplugin/{plugin,schedule}.go`；`memory/{scope,recall,ports}.go`；`projectcontext/plugin.go`；`turn/turntest/turntest.go` |
| Codex 执行与状态 | `codex-rs/core/src/session/{session,mod,turn,step_context,step_activation,rollout_reconstruction,async_compaction,daemon_recovery,turn_suspension}.rs`；`tasks/mod.rs` |
| Codex 工具、扩展与提供方 | `core/src/tools/{registry,parallel,orchestrator}.rs`；`client.rs`、`responses_retry.rs`、`model_request.rs`、`image_preparation.rs`、`mcp_tool_exposure.rs`、`skills.rs`、`hook_runtime/executor.rs` |
| Codex 历史、多代理和恢复 | `context_manager/{history,history_user_authorization}.rs`；`agent/{control,child_config}.rs`；`agent/control/{spawn,spawn_guard,budget,completion}.rs`；`rollout/src/policy.rs`；`thread-store/src/live_thread.rs`；`app-server/src/thread_state.rs`；`core/tests/common/responses.rs`、`core/src/test_support.rs` |
| Crush 执行与模型 | `internal/agent/{agent,coordinator,agent_tool,hooked_tool,settle,shared_models,channelreply,common_test}.go` |
| Crush 存储、事件与扩展 | `message/message.go`、`session/session.go`、`pubsub/broker.go`、`permission/permission.go`、`backend/events.go`、`server/events.go`、`hooks/runner.go`、`skills/catalog.go`、`agent/tools/mcp/{tools,lifecycle}.go`、`shell/truncate.go` |
| Sumika 文档 | `规格-Go后台.md`、`subsystems/runtime.md`、`subsystems/agent-and-console.md`、`规格-子代理.md`、`决策记录.md` 中 ADR-242；既有缺口盘点用于去重 |

网络交叉核对使用 [Codex App Server 官方文档](https://developers.openai.com/codex/app-server)、[其 Markdown 正文](https://learn.chatgpt.com/docs/app-server.md)、[Fantasy v0.45.1 源码](https://github.com/charmbracelet/fantasy/blob/v0.45.1/agent.go)及[版本页面](https://pkg.go.dev/charm.land/fantasy@v0.45.1)。在线文档可能晚于本机提交，具体实现判断以本机源码为主。

许可核对：Codex 为 Apache-2.0（私有存档）；当前 Crush 为 FSL-1.1-MIT（私有存档），并非 GPL/AGPL。本报告只比较设计与契约，没有复制或移植代码。
