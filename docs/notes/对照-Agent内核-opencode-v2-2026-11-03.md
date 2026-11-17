# 对照：Agent 内核 · opencode-v2（2026-11-03）

> 用户 2026-11-03："把我们 sumika 的 Agent 核心和 pi、opencode v2 之类的做深度的架构解析对比，看看我们思路上缺少什么更现代优秀的设计。" 只读源码调研，Sumika 基点 1181083e；标了 [推断 / INFERENCE] 的是工程判断，没有实测。三份的合并结论和改造顺序见同日的交叉分析。

需要区分一处设计与实现：ADR-242 已决定同角色四种场合都能召回全部记忆，但本次读到的 `AllowedScopes` 和 `Recall` 仍有旧过滤。报告会以 ADR-242 为目标，把旧实现标明，避免建议重新强化已被取代的隔离。另一处也会写清：v2 默认保存事务投影，但完整事件日志保留默认关闭，不能把两者都称为可完整回放的事件溯源。

先说人话：**Sumika 已有完整、而且针对陪伴聊天做过认真取舍的 Agent 内核。最值得向 opencode v2 学的，是把“输入收到了什么、模型看到了什么、工具执行了什么、最终对外说了什么”变成边界清楚、身份一致、可以恢复的状态。** Sumika 目前主要靠热实例和多处回调维持这些关系；v2 把更多关系落成了持久记录和明确协议。建议优先借运行身份、输入接纳与执行分离、指令版本、工具结果契约和生命周期管理。QQ 的裁决、改写、发送屏障，以及角色长期记忆，应保留自己的设计。

## 阅读口径与版本

本次只读 Sumika 源码和文档，未访问 `sumika/data`，未编译、未运行测试、未调用模型、未提交。按附件要求更新了外部仓库引用，另建 `opencode-v2` checkout；v1 源码临时归档放在指定 scratchpad 下。

| 对象 | 本次核对的提交 | 用途 |
|---|---|---|
| Sumika 工作区 HEAD | `1181083e62ca8e3dfa77594bad4fae9091fae408` | Go 实现分析，结论以本次实际读取的工作区源码为准 |
| 原 `opencode-beta` | `b5da081112574bd6a558960e05096cac4ac59e43`，2026-10-25 | 记录旧 checkout 的身份 |
| opencode 最新 `dev` | `2e8a861e11d6fe51c03d6aa4ac93a7d8a41b4aab`，2026-11-02 | 本文 v2 的主要源码依据，位于 `_external/opencode-v2` |
| opencode 最新已列出的 v2 发布标签 | `v2.2.5` → `9bbb8ce18c80f099e0e448775f59d965ead40a21`，2026-10-30 | 核实发布版本；本文没有把 dev 当成该发布标签 |
| opencode v1 对照 | `v1.19.5` → `810c0eba1a7348036f8bdf61e6632d0c36c6876c`，2026-10-28 | 对比 v1 的循环、消息更新、执行协调与分叉 |

原 checkout 的默认 fetch 失败，原因是远端 `beta` 分支已删除；随后显式 fetch `dev`、`v2.2.5`、`v1.19.5` 成功。远端还有 `2.0` 分支，但主分析选择更新的默认 `dev`。

两点口径需要先固定：

- `docs/subsystems/runtime.md`、`agent-and-console.md` 的主体仍描述 Bun/Cordis/pi，不能直接当作 Go 后台现状；本文以 `native/internal` 实现为准。
- [ADR-242](仓库根/docs/决策记录.md:2878) 已决定同角色四种场合都能召回全部记忆。当前读到的 `memory/scope.go:60`、`memory/recall.go:394` 仍有旧范围过滤，这是设计与实现尚未对齐的现状，不能据此建议重新强化旧隔离。

下文源码行为属于读码结论；“差异判断”“建议”“风险与收益”均为 **[INFERENCE] 工程判断**，本次没有性能、费用或真实聊天效果测量。

opencode 本次 checkout 的根 LICENSE（私有存档） 是 MIT；本文仍只讨论设计借鉴，没有复制代码。

## 先核实：v2 相比 v1 到底改变了什么

**不能把 Effect、逐步重读历史、流式消息、工具循环、子会话、分叉都算成 v2 新发明。** 本次对照的较新 v1 已经有这些基础，甚至已经通过桥接层使用部分 v2 Core。

| 关注点 | v1 源码截面 | v2 源码截面 | 变化的实质 |
|---|---|---|---|
| 循环组织 | `SessionPrompt.runLoop` 循环读取压缩后的历史，再交给 processor；见 [v1 prompt.ts:1082](https://github.com/anomalyco/opencode/blob/810c0eba1a7348036f8bdf61e6632d0c36c6876c/packages/opencode/src/session/prompt.ts#L1082) | 执行协调、inbox 提升、逻辑 Step、物理 Attempt 分开；见 v2 runner/llm.ts:64（私有存档） | 更明确地区分“接纳输入”和“执行输入”，以及一次逻辑步骤里的多次实际请求 |
| 事件语义 | `MessageUpdated`、`PartUpdated` 等更新事件已经存在；见 [v1 session.ts:629](https://github.com/anomalyco/opencode/blob/810c0eba1a7348036f8bdf61e6632d0c36c6876c/packages/opencode/src/session/session.ts#L629) | 明确发布 `InboxEnqueued`、`Tool.Called`、`Tool.Success`、`Execution.*` 等领域事实 | 从“对象更新了”进一步转向“发生了什么”，更适合恢复与多消费者 |
| 执行所有权 | v1 按 Instance 保存会话 runner，空闲时移除；`run-state.ts:44` | v2 进程级按 Session ID 协调，Location 服务在执行时选择；`execution.ts:84` | 会话身份、进程内执行所有权、运行位置不再绑成一件事 |
| 插件 | v1 返回工具和 hooks 对象 | v2 在插件生命周期内注册 hooks、transforms、工具和订阅 | 注册撤销与后台任务寿命归同一 Scope；[官方迁移说明](https://opencode.ai/v2/docs/build/plugins/migrate-v1)明确要求移植实现 |
| 上下文更新 | 每步重新拼系统信息与历史 | 指令内容按哈希保存，变更按时间追加，压缩推进指令 epoch | 将缓存稳定性、运行中更新和历史解释性放进同一契约 |

这是一组版本截面比较，没有追溯每项设计首次进入仓库的提交。v2 的设计理由，优先采信其事件流决策记录、Session 契约和官方迁移文档；源码与文字不一致时，以源码为准。

## 1. 回合循环的形状：循环仍在，状态边界更明确

**它怎么做。** v2 仍有 `while` 循环，并不是换成一个通用工作流引擎。它把循环分成三层：

1. `SessionRunCoordinator` 管一个 Session 的忙碌周期：同会话只拥有一个执行，重复 wake 合并；不同会话并行。
2. runner 在安全步骤边界提升 inbox 输入、处理压缩或移动控制项、重新装配上下文。
3. `SessionStep.attempt` 管一次实际模型请求、流、工具和最终收尾。

完整本地工具调用一旦到达，就先完成 `Tool.Called` 的持久发布，再启动工具 fiber；因此工具可以在模型流尚未完全结束时开始。Step 结束前等待工具结局，随后从持久投影重载历史继续。出处：`run-coordinator.ts:30`、`runner/llm.ts:83`、runner/step.ts:106（私有存档）。

它区分重试同一个逻辑步骤、保留半截回复后生成新助手消息续写、超窗后重建，以及正常下一步工具循环。停止、失败、完成都是明确结局。

**Sumika 怎么做。** [Hub.runTurn](仓库根/native/internal/turn/hub.go:1242) 持续追加 `Live.msgs`，每步构造请求，等完整模型结果，再执行这一批工具，接纳插话；没有工具、没有插话就结束。`Respond` 按“角色 × 会话”排队，实例再按 Scope、渠道、听众条件区分。停止依赖 context，`InterruptIn` 等当前回合退出。

这里并非完全依赖隐式状态：已有 `Outcome`、`Settled`、`turns`、工具步骤状态和压缩状态。但排队状态、热历史、执行所有权、后台压缩、渠道待 Commit 的片段，集中在 Hub/Live 中维护。

**差在思路哪里。** v2 把“一个运行中的会话”拆成了持久工作与临时执行者；Sumika 的热实例承担更多语义。后者实现直接，但每增加一种恢复、后台任务或入口，都更容易碰到 Hub 的多个状态集合。

**值不值得学。** 值得借“输入、步骤、请求尝试、执行周期”的区分；暂时没有理由引入状态机框架。保留现有循环，在当前结构里明确各状态的拥有者和转换边界即可。v2 的提前启动工具不宜直接照搬：Sumika 当前整批串行规则和审批顺序承担了副作用约束。

## 2. 事件与流式：可靠状态和在线增量要有不同契约

**它怎么做。** v2 的 Bus 支持带版本、聚合 ID、序号的 durable 事件，投影与相关操作性状态在同一事务内提交，再通知消费者。`log(after, follow)` 先订阅唤醒，再读取历史水位，发出 `log.synced`，然后继续读取新增记录，避免“读完历史、尚未订阅”之间漏事件。见 bus.ts:395（私有存档）、`bus.ts:830`。

但必须区分：

- 文本、思考等 Delta 是在线事件，不能靠 durable log 逐字重放；块结束事件保存完整内容。
- **durable 投影默认存在，完整事件 payload 日志保留默认关闭。** `bus.ts:214` 的 `persist` 默认是 `false`；开启后才保留可供历史日志读取的事件行。
- Core 的在线 PubSub 仍有无界队列。HTTP Server 另设每连接独立的有限队列，编码一次、分发同一字符串；慢连接溢出就终止该连接，健康连接继续。见 event-feed.ts:33（私有存档）。

事件流决策记录明确解释了为什么选独立队列：避免最慢消费者拖住发布，也避免共享队列静默丢事件。出处：`specs/v2/event-stream-architecture.md:50`。

**Sumika 怎么做。** 正文、工具开始、插话、压缩分别通过配置回调；`OnDelta(text)`、`OnTool(id,name)` 本身没有运行归属。插件通知走 `OnSettled`、`OnSeen`，每订阅者独立无界队列。见 `turn/turn.go:68`、`turn/plugin.go:604`。

审批已经有更完整的局部契约：版本化状态、Snapshot、订阅时重放，以及同锁衔接后续变化。见 [approval/subscribe.go:48](仓库根/native/internal/approval/subscribe.go:48)。

**差在思路哪里。** Sumika 不是缺一个“事件总线库”，而是各类事件分别定义了身份、重连和交付保证。以后桌面、运行记录、后台任务同时消费时，消费者必须自己拼接。

**值不值得学。** 先统一运行事件信封：角色、会话、回合、步骤、实际请求尝试、工具调用 ID。再分别定义“可用快照恢复的状态”和“仅在线增量”。可以从现有审批订阅的模式扩展，不必先做全仓事件溯源。队列容量也应基于消费者行为选择，不能照抄 v2 的 4096。

## 3. 会话与状态持久化：v2 是有序会话加派生关系，不是任意消息树

**它怎么做。** v2 用 `session_message.seq` 保存有序消息，助手内容拆到 `session_part`；待处理输入另存在 `session_inbox`。Session 的父子关系用于子代理，fork 来源另有 `fork_session_id` 和边界。见 session/sql.ts:29（私有存档）。

fork 会复制边界前已收尾的历史，重写子会话中的消息 ID 和摘要引用；正在执行的助手、shell、压缩不复制。它不是所有消息共用一个任意分支 DAG。见 `session/projector.ts:111`。

执行开始时写 claim；成功、失败、用户停止释放 claim，关机中断保留。下次启动据 claim 恢复，限制恢复次数，并先把遗留工具调用记成中断。源码明确写明：恢复是至少一次语义，不能保证模型请求、外部副作用只发生一次。见 `session/execution.ts:69`、execution/restart.ts:42（私有存档）。

**Sumika 怎么做。** 聊天事件按平台分库，trace 共享；上下文快照保存摘要和续接位置。热实例保留工具往返、思考和参考信息；冷重建主要恢复摘要与聊天消息。见 `turn/turn.go:352`、`turn/loop.go:31`。

崩溃后已有事务化收尾：未结束回合记 interrupted，执行中工具记结果未知，等待审批记取消；不自动重跑。见 [store/turns.go:55](仓库根/native/internal/store/turns.go:55)。

**差在思路哪里。** v2 把工具过程也纳入可继续执行的会话材料。Sumika 的聊天事实和运行记录分离是合理的，但运行记录尚未形成与热实例等价的恢复材料。

**值不值得学。** 先为本地干活和未来子任务定义可恢复的执行记录：接纳了哪些输入、完成了哪些步骤、哪些结果可引用、哪些副作用未知。普通聊天历史继续保存真正说出口的内容。QQ 群不应因为进程重启就自动续发旧话；v2 面向长编码任务的自动恢复策略，不适合直接成为陪伴聊天默认值。

## 4. 上下文管理：最值得借的是指令版本与“预先算好、何时采用”分离

**它怎么做。** v2 将指令来源读取、内容版本和模型请求装配分开。指令值按内容哈希保存；初始值构成 epoch 基线，之后更新以增量和冻结的更新文本进入时间线。压缩完成后，当前值成为新基线。见 instruction-state.ts:21（私有存档）。

压缩保存滚动结构化摘要和一段近期原文；摘要明确记录目标、要求、决定、工作状态、阻塞和下一步。原始历史保留。超窗时有受限恢复路径；输入本身太大时还会缩短摘要材料。见 `session/compaction.ts:166`、`:453`、`:691`。

后台预计算有一个更细的边界：**摘要先算好留着，达到真正压缩条件才采用**，期间继续保留原前缀；采用前检查模型、指令和历史锚点是否仍匹配。该能力默认关闭。见 compaction.ts:330（私有存档），以及[官方压缩说明](https://opencode.ai/v2/docs/compaction)。

附件在接纳时解析、规范化并保存；请求装配再按模型能力和图片预算处理，不能简单把附件 URI 当成始终可用的输入。出处：`session/prompt.ts:81`、`session/model-request.ts:42`。

**Sumika 怎么做。** 固定前缀每轮重拼，文本、模型、工具定义或快照变化时重建实例；参考信息只追加，插话时再补。已有稳定缓存键、请求副本精简、实际用量辅助估算、摘要加原文、提前压缩和超窗恢复。见 `turn/hub.go:944`、`turn/loop.go:306`、`turn/slim.go:13`。

压缩材料主动去掉参考信息、工具过程和思考，当前调用 `compact-chat`；提前压缩成功即写快照，下一轮 acquire 采用。见 [turn/compact.go:35](仓库根/native/internal/turn/compact.go:35)、`turn/hub.go:707`。

Agent 的 `Msg` 主要是文本、思考、调用和工具结果；媒体下载、存储、描述是独立体系，尚没有 v2 那样统一的模型内容块表示。这不等于 Sumika 没有收图能力。

**差在思路哪里。** 两家都重视缓存，但 v2 更明确地区分了来源版本、已接纳内容、压缩基线和预计算候选。Sumika 已有 `ContextID` 和贡献接纳回调，基础并不落后；主要差别是这些版本和边界还没有贯穿恢复材料。

**值不值得学。** 优先记录提示词版本及生效边界，并借预计算候选的采用校验。不要把所有人设、听众和权限变化都改成尾部文字：需要切换隔离边界的变化仍应重建。陪伴摘要也不宜照搬工作清单模板；应保留关系变化、未完话题、约定和更正，干活摘要再保留任务状态。

## 5. 工具系统：统一调用契约值得学，权限放在哪里要保留判断

**它怎么做。** v2 工具统一声明输入、可选机器输出、模型内容和 UI metadata。请求捕获实际工具值，执行使用这份快照；参数在运行边界解码，声明了输出时再验证输出。见 tool/runtime.ts:29（私有存档）、`tool.ts:253`。

它区分三个结果用途：

- `output`：给 Code Mode 继续计算的机器值。
- `content`：给模型、进入持久记录的内容。
- `metadata`：给界面的紧凑状态。

完整调用持久化后才执行；普通工具错误可以回给模型。用户明确拒绝、取消、程序缺陷有独立控制路径。无反馈的人工拒绝会中断执行；附修正意见的拒绝允许模型看到反馈继续。见 `permission.ts:252`、`session/model-request.ts:401`。

长文本结果统一保存全文并返回受限预览。当前读到的 `tool-output.ts:71` 实现是按前部行数和字节保留；不能只按规格中“头尾截取”的文字描述它。

权限由可信工具提出 action/resource 请求，Permission 服务判定。Code Mode 限制直接文件访问、imports、计时器等，但本地工具最终仍能调用真实文件系统和进程；不能把 Code Mode 的受限解释器等同于整个工具系统的 OS 沙箱。出处：`codemode/tool.ts:75`、`environment/local.ts:14`。

**Sumika 怎么做。** 工具声明保留 JSON Schema 文本；`parseArgs` 处理 JSON，`Prepare` 整理参数，具体工具自行检查。当前不是所有调用都经过统一的声明 schema 解码。见 `tools/tools.go:15`、`tools/exec.go:50`。

审批集中在内核执行前，插件工具还必须登记 `policy.Specs`；工具清单和执行都核对当轮听众。插件调用绑定原注册，停用或同名替换不能接管旧调用。见 [turn/plugin.go:264](仓库根/native/internal/turn/plugin.go:264)。

一批中有串行工具就整批串行，否则并行执行，结果按模型调用顺序接回。工具已有总超时和取消处理。结果是 `Text / IsError / Details`，另有宿主控制的接纳标记。

**差在思路哪里。** Sumika 的权限集中执行，约束更容易统一检查；v2 的输入输出及结果表示更统一。两者各有值得保留的部分，不能因为 v2 工具接口更丰富，就改成由任意插件自觉执行审批。

**值不值得学。** 先形成统一调用身份、统一参数解码、模型结果与界面详情的明确边界，以及统一全文结果引用。保留 `policy.Specs` 和宿主审批。动态 MCP 接入时，需要为未知工具建立明确策略，不能靠名称登记的有限内置表直接承接，也不能相信对方自报的只读属性。

## 6. 扩展模型：Sumika 已经借到了主要思想

**它怎么做。** v2 插件激活拥有独立 Scope，注册、订阅、后台 fibers 和 finalizers 一起释放；变换按注册顺序重新作用于目录状态，hooks 在操作边界处理可变事件。失败重载可恢复旧版本，普通 hook 失败会影响当前操作。见 plugin.ts:64（私有存档）、`plugin/hooks.ts:88`、`plugin/supervisor.ts:143`。

MCP 连接也有独立生命周期；迟到通知先核对是否仍来自当前连接，过期连接不能更新新状态。工具目录变化经过重新注册进入统一工具系统。见 `mcp/connection.ts:97`、`tool/mcp.ts:38`。

Scope 是资源和逻辑生命周期边界；不能据此认为任意 npm 插件获得了进程安全隔离。

**Sumika 怎么做。** 编译进程序的 `Plugin{ID, Setup}`、撤销函数、停用取消、原注册绑定、超时和迟到结果丢弃都已存在。`Contribution.OnAccepted` 只在真正进入上下文后提交账本。见 `turn/plugin.go:36`、`:528`。

记忆插件还有完整的“拒绝新工作 → 取消 → 停计时器 → 等在途任务 → 允许关库”顺序。见 [memplugin/plugin.go:273](仓库根/native/internal/memplugin/plugin.go:273)。

**差在思路哪里。** 插件生命周期方面差距有限。v2 的统一程度更高；Sumika 对召回超时、单插件故障的隔离反而更符合聊天延迟需求。

**值不值得学。** 继续复用记忆插件的生命周期模式，让宿主后台任务也归明确拥有者。当前没有必要做动态 Go 插件、JavaScript 插件宿主或全领域 transform 系统。`规格-Go后台.md:358` 已明确这些不是当前目标，这个取舍仍成立。

## 7. 子代理与多代理：关键是独立任务身份和结果交付

**它怎么做。** `subagent` 创建带 `parentID` 的独立 Session，默认新上下文；这与复制历史的 fork 是两种操作。可以指定代理、模型，前台等结果，后台通过 Job 执行并在完成后向父会话投递通知。续发时检查目标确实是当前父会话的孩子。见 subagent.ts:130（私有存档）。

创建前检查代理模式、深度和委派权限。父会话能创建孩子，不代表孩子拥有父会话全部能力；源码按选定代理和会话规则构造权限，不能自行推导出“必定是父子权限交集”。

后台完成通知和重启恢复独立于父调用栈。见 `session/subagent-job.ts`、`subagent-completion.ts`、`execution/restart.ts:167`。

**Sumika 怎么做。** 本次检查的 `turn`、`tools` 和 `app/kernel.go` 未找到子代理派生实现。现有 `ToolCall` 有角色、会话、回合和 ContextID，但没有实际工具调用 ID、助手步骤身份、父任务身份。已有缺口盘点也已列出子代理规格，本文不再重复功能需求。

**差在思路哪里。** 需要新增的是“可独立存活、独立收尾的任务实体”，不能只在工具里起 goroutine，再等一个字符串。

**值不值得学。** 实施既有子代理规格时，借独立上下文、父子关系、稳定完成通知身份和取消规则。对 Sumika 应明确区分角色与工作执行者：子代理结果先回父任务，再由角色决定怎么表达；过程不能自动混进普通聊天和长期记忆。父回合停止后后台任务是否继续，也必须有单独规则，不能只靠继承 context 偶然决定。

## 8. 模型提供方抽象：借分层，不必在 Sumika 重做网关

**它怎么做。** v2 的 AI 层把协议、部署端点、鉴权、流帧和传输拆开：同一种 Chat 协议可以被多家兼容服务复用；runner 消费统一请求、事件和错误。见 ai/route/protocol.ts:4（私有存档）、`ai/route/client.ts:48`。

有状态 WebSocket 还单独维护会话连接、续传 checkpoint、连接亲和和错误交付状态。它区分请求未发送、可能发送、提供方已接收等情况，为重试提供依据。见 `session/model-transport.ts:32`。

费用优先使用端点实报，其次用价格目录计算；零费用也能是有效实报。但 `SessionUsage.tokens` 对缺失用量仍归零，不能把它当作“未知用量口径已经完美”的实现。见 `session/usage.ts:10`。

限流失败有分类、Retry-After 和退避策略；本次核对的是这些路径，没有据此认定它提供跨会话的全局额度协调。

**Sumika 怎么做。** `llm.Client` 是 OpenAI 兼容流的薄包装，关闭 SDK 隐式重试，提供 usage、实际路由、思考字段、结构化思考和流空闲检测；循环在 turn 包。见 [llm/llm.go:24](仓库根/native/internal/llm/llm.go:24)。

这是围绕网关的合理边界。当前正式装配默认 Magpie，后台记忆调用共用客户端，但只把正文返回上层。见 `app/app.go:268`、`app/guarddeps.go:113`。

**差在思路哪里。** v2 适合自己承接多种原生协议；Sumika 将协议复杂度交给网关。当前更值得补的是调用语义的一致性，而不是提供方数量。

**值不值得学。** 统一一次模型调用的用途、有效配置、实际请求尝试、结果、用量是否收到和错误类别，让回复、压缩、整理、守卫共用调用边界。等确有直连原生协议需求，再在 `llm` 下增加适配器。不要把账号路由、提供方刷新、原生 WebSocket 的整套职责搬进 Sumika。

## 9. 取消、并发、错误与可测试性：生命周期契约比语言框架更重要

**它怎么做。** v2 使用 scoped fibers 管工作寿命，在短的最终状态发布阶段屏蔽取消，保证调用和 Step 得到结局。`interrupt` 表示接受停止，`awaitIdle` 表示清理完成，两者语义分开。见 `session/run-coordinator.ts:14`、`runner/step.ts:142`。

工具可预期失败、人工拒绝、取消、缺陷有不同处理。并发工具按各自来源保序，消费者按 ID/ordinal 合并，不要求所有来源具有一个含业务意义的全局完成顺序。

测试有可控的 `TestLLM`：响应脚本、卡点、请求观察、半截失败、挂起流；HTTP/WebSocket recorder 另负责真实协议回放和未消费交互检查。见 ai/testing.ts:24（私有存档）、`http-recorder/src/replay/state.ts:36`。

**Sumika 怎么做。** 已有 context、每会话队列、工具总超时、写文件提交边界处理、插件结果接纳校验。部分不理取消的工作会在调用方不再等待后继续退出，因此仍需拥有者负责关库前排空。App 目前用自身追踪与关闭顺序补齐 Hub 的生命周期。见 `turn/loop.go:717`、`app/app.go:472`。

可测试性已有扎实基础：真 Hub、临时库、可控 HTTP 假网关、插话屏障与模型录像。录像会检查请求不匹配、超额请求和未用完交互。见 [turntest.go:45](仓库根/native/internal/turn/turntest/turntest.go:45)、`llm/llmtest/cassette.go:1`。

**差在思路哪里。** Sumika 的主要差距是“谁等哪些工作退出”还分散在装配和各组件；不是缺 fake model 或回放体系。插件中把 panic、普通错误都转成工具失败，也会降低内部故障与正常失败的区分度。

**值不值得学。** 明确停止接受与停止完成的语义，让执行拥有者负责排空；内部保留取消、超时、审批拒绝、工具失败和程序缺陷的区别。继续使用已有测试基础设施，只给新契约补相应交错验证。没有必要为接近 v2 而改用另一个并发框架。

## 10. 另外值得注意的设计：三个容易被混为一谈的“历史”

### 原始事实、模型上下文、对外交付应分开

v2 的领域事件与投影帮助它区分“发生的事”和“当前视图”。Sumika 在 QQ 上已有更贴近产品的边界：模型输出先裁决、改写、守卫、写发送计划，再同步 Commit 回内核，最后开始发送。见 [channel/settle.go:1](仓库根/native/internal/channel/settle.go:1)、`turn/hub.go:777`。

这是应保留的架构。**模型生成完，不等于群友看见了；发送计划写入，也不等于平台确认送达。** 后续统一运行记录时，应把这些状态关联起来，不能将它们合并成一个 assistant message 状态。

### 指令、工作状态、长期记忆应各有来源

v2 摘要明确要求不要抄录仓库规则、环境设置，因为当前指令会另行提供；摘要负责无法方便重新取得的任务状态。见 `compaction.ts:199`。

Sumika 已通过 `memplugin` 让内核与 memory 解耦，memory 通过事件、嵌入、Generator 端口接外部；不应把这套长期记忆简化成会话摘要。见 `memplugin/plugin.go:1`、`memory/ports.go:10`。

可借的是来源和版本：聊天摘要、任务摘要、长期记忆、当前规则分别维护，再在明确边界装配。

### Code Mode 的可取点是机器结果与模型叙述分开

v2 可以把许多工具藏进受限脚本运行时，让模型用程序组合结构化结果，减少中间数据反复进入上下文；见 `tool.ts:267`、`codemode/tool.ts:89`。

Sumika 以聊天、记忆和少量辅助工具为主，目前没有证据需要这套执行面。以后出现大量资料查询、筛选、聚合任务时，可以先借结构化结果契约，再决定是否需要脚本运行时。

## Sumika 该借的几条：按收益排序

以下是架构建议，不是新增排期，也不是对已有功能缺口再列一遍。

### 架构上值得改

| 顺序 | 现在的痛点 | 借什么、怎么转换成 Sumika 做法 | 主要包 | 风险与验收重点 |
|---|---|---|---|---|
| **1** | 回调、模型请求、工具、审批、渠道交付使用不同层次的身份，消费者自行关联 | 建立贯穿一次执行的运行身份；统一事件信封，明确回合、步骤、实际请求尝试与工具调用的关系。复用现有表和订阅方式 | `turn`、`llm`、`approval`、`store`、`channel`、`app` | 不能把模型结束当发送完成；两个会话交错、重试、插话、审批都必须归属正确 |
| **2** | 输入已存库、已入队、已被模型看到、已处理的状态分散在内存集合和渠道状态里 | 借 inbox 的接纳/交付分离。先明确可信输入 ID、接纳状态和安全交付边界；让统一执行拥有者处理停止准入、取消和排空 | `turn`、`store`、`channel`、`app` | 不直接统一所有平台入口语义；保留聊天批次、渠道屏障，关闭时不得迟到写库 |
| **3** | 本地任务热运行时知道工具过程，冷重建后主要剩聊天 | 为工作执行保存能继续使用的材料：已接纳输入、步骤、工具终态、结果引用、未知副作用。聊天历史继续只承载产品定义的聊天事实 | `turn`、`store`、`tools`、`history` | 记录不能变成自动重做授权；重启后未知结果必须保持未知，子任务过程不能污染角色记忆 |
| **4** | 提示词变化通常触发重建，提前摘要成功与采用没有独立候选状态 | 借内容版本、生效边界和预计算候选校验；先记录每次装配使用的版本，再考虑对适合追加的动态指令采用增量 | `prompts`、`turn`、`projectcontext`、`store` | 权限、听众、角色身份变化不能靠尾部文字替代隔离；过期摘要不得覆盖新语境；缓存收益需另量 |
| **5** | 回复、压缩、记忆整理和守卫使用同一底层客户端，但调用记录及结果口径不同 | 借一次模型调用的统一边界：用途、有效设置、实际尝试、部分结果、usage 是否收到、路由、失败分类。保留网关职责 | `llm`、`turn`、`app`，适配 `memory.Generator` | 防止 SDK 与调用方重复重试、重复记账；已经执行工具的步骤不能因换模型整轮重跑 |
| **6** | 未来子代理若直接挂在工具闭包里，任务寿命、角色身份和消息寿命容易混在一起 | 按既有子代理需求建立独立任务记录、父子关系、上下文和结果交付；补足调用身份，复用执行生命周期 | `turn`、`store`、`tools`、`app`、`channel` | 明确孩子权限、结果回到哪里、父回合取消后的行为；不要默认继承全部历史和授权 |

其中第 1、2 项应先确定契约；第 3 项聚焦本地工作和子任务，不需要先把普通闲聊改成完整执行日志。第 4 项也应先做可解释的版本记录，再凭测量决定是否改变缓存与压缩策略。

### 以后再说

| 设计 | 暂缓理由 |
|---|---|
| 全量事件溯源、所有表都能从日志重建 | 连 v2 默认也没有保留完整事件 payload 日志；Sumika 现有表和事务已覆盖许多事实，没有证据值得全库改造 |
| QQ 自动恢复旧回合并继续发言 | 编码任务需要持续完成；群聊依赖当前时机，旧回复恢复后可能已经失去语境 |
| 完整动态插件、npm 安装、热重载与全领域 transforms | 当前主要扩展是自有 Go 代码；既有撤销、超时、停用与接纳机制足以支持它们 |
| Code Mode | 适合大量结构化工具组合；当前陪伴场景没有已测量的需求支撑 |
| 原生 WebSocket 续传、提供方加密压缩窗口 | 与具体协议、模型绑定，跨模型可移植性更差；先让网关承担协议复杂度 |
| 任意消息树、工作区移动、分布式执行与 fencing | v2 的这些需求来自编码、远程运行和多宿主场景；Sumika 当前单进程、多会话不自动需要它们 |
| 用工作摘要取代长期记忆 | 两者服务不同寿命和事实来源；Sumika 的陪伴记忆是自己的核心能力 |

## 主要读过的源码与依据

以下均读取了相关实现段；没有声称逐行通读全仓。

| 范围 | 本次主要源码 |
|---|---|
| Sumika 回合与上下文 | `native/internal/turn/{turn.go,hub.go,loop.go,plugin.go,compact.go,slim.go}` |
| Sumika 工具与审批 | `tools/{tools.go,exec.go}`；`approval/{approval.go,subscribe.go}` |
| Sumika 状态与恢复 | `store/turns.go`；Hub 的 acquire、Commit、settle、排队与重建路径 |
| Sumika 渠道与装配 | `channel/{respond.go,settle.go}`；`app/{app.go,guarddeps.go}` |
| Sumika 记忆边界 | `memplugin/plugin.go`；`memory/{ports.go,recall.go,scope.go}` |
| Sumika 测试设施 | `turn/turntest/turntest.go`；`llm/replay_test.go`、`llmtest/cassette.go` |
| Sumika 文档 | 附件指定的四份文档，以及 ADR-242 |
| opencode v2 执行 | `session/{execution.ts,run-coordinator.ts,inbox.ts,sql.ts,store.ts}`；`execution/restart.ts`；`runner/{index.ts,llm.ts,step.ts,retry.ts,publish-llm-event.ts}` |
| opencode v2 上下文与历史 | `session/{prompt.ts,instruction-state.ts,compaction.ts,model-request.ts,model-transport.ts,usage.ts,projector.ts,revert.ts}` |
| opencode v2 事件、工具与扩展 | `bus.ts`；`schema/src/session-event.ts`；`server/src/event-feed.ts`；`tool.ts`、`tool/runtime.ts`、`tool-output.ts`、`permission.ts`；`plugin.ts`、`plugin/{hooks.ts,supervisor.ts}` |
| opencode v2 MCP、子代理与执行环境 | `mcp/connection.ts`、`tool/mcp.ts`、`tool/plugin/subagent.ts`、`codemode/tool.ts`、`environment/local.ts` |
| opencode v2 AI 与回放 | `ai/src/{llm.ts,testing.ts}`、`ai/src/route/{protocol.ts,client.ts}`；`http-recorder/src/replay/state.ts` |
| opencode v1 对照 | `v1.19.5` 的 `packages/opencode/src/session/{prompt.ts,processor.ts,run-state.ts,session.ts}` |
| opencode 设计依据 | `specs/v2/{session.md,tools.md,event-stream-architecture.md}`；官方[插件迁移](https://opencode.ai/v2/docs/build/plugins/migrate-v1)、[Effect 生命周期](https://opencode.ai/v2/docs/build/plugins/effect)、[压缩说明](https://opencode.ai/v2/docs/compaction) |

本次未验证运行效果、吞吐、内存、缓存命中或摘要质量；文中没有把对方仓库已有测试、基准结果当作本次实测。
