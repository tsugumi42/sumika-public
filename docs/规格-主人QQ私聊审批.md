# 规格：主人 QQ 私聊审批

> 状态：2026-11-09 已合 main（ADR-343）。**范围 1（本地审批转发到主人 QQ 私聊、回复结算）已做并验收；范围 2（主人在 QQ 私聊里发起执行和写文件）没做**，见下面“实现状态”。配置默认关。
> 依据：ADR-277、ADR-281、ADR-305、ADR-330 第 3 条。主人原话：“私聊还好吧，群聊先不放开”。
> 这是[规格-外平台执行和远程审批](规格-外平台执行和远程审批.md)里“私聊”那一小块的实现；那份规格的群、改投别的平台、按平台 yolo、沙箱、任务目录、子代理范围都不在这次范围里，第八节列出和它的差异。

## 实现状态

| 项 | 状态 | 落点 |
|---|---|---|
| 配置键 `qq.remoteApproval.enabled`（默认 false）、`awayAfterSeconds`（默认 60） | 已做 | `internal/config`、`settingsreg`、`sumika.example.yml` |
| `Approvals.DecideVia`（只收 once、deny，来源进结局和步骤记录） | 已做 | `internal/approval/decide.go`、`internal/turn/approvalsteps.go` |
| 发卡、认回复、一次性、5 分钟作废、多单追问、迟到回复、本机先答 | 已做 | `internal/approvalrelay` |
| 流水线 `Control` 钩子、来源 `automation:remote-approval` 不进模型历史和记忆 | 已做 | `channel/pipeline.go`、`history`、`store/memsource` |
| 装配：现读配置的绑定、自动化出口、出口档位、被截走的回复行改标 | 已做 | `internal/app/remoteapproval.go` |
| 设置页“平台账号”的开关和一句风险说明（`awayAfterSeconds` 在页上只读显示，要改去配置文件或设置接口）（“号被盗就能批”常驻在开关下面；写失败时值不变、写明没改成） | 已做 | `native/settings_remote_approval.go`、`internal/desktopread`；`ui.NewTester` 测，截图在 `data/dev/qq-approval/` |
| 主人 QQ 私聊里发起这一轮的执行和写文件（第二节第 5～9 条、下面表里标“待做”的） | **没做** | 要动 `audience.Audience`、`turn.toolsFor` / `prepareCall`、`policy.Decide`、`approval.Gate.Check`、提示词、检查点；这几处正是 feat/eco-approval 在改，它已合 main，现在可以接 |
| 批准后执行前再比一次目标（ADR-343 第 5 条） | 已做（回答来自 QQ 时） | `approval.Gate.changedSince`：标题、内容、预览、写入目标真实路径任何一项和建卡时不同就不执行；本机点的批准不复查（看的是当前卡） |
| 独立复核补的洞（结果未知留墓碑、写库失败说真话、发卡途中回复、平台时间和重连窗口、参数截短、带号无单截走、端到端走真实闸门） | 已做 | 见第四节新增的行 |

## 决定（已写成 ADR-343，下面是写 ADR 时的底稿）

**决定**：

1. 主人 QQ 私聊能收到本地审批的完整内容，回复“同意”“拒绝”就结算。后台结算，模型没有批准工具。一次性，5 分钟作废。本地和 QQ 谁先答算谁。（ADR-330 第 3 条，主人定）
2. 主人在 QQ 私聊里发的消息直接引起的这一轮，可以申请执行命令和写文件，每次都问，不继承本地 yolo、对话授权和始终授权。别人的消息、角色接角色、定时任务、补录都不行。结果由她在私聊里回。（ADR-277 第 1 条的私聊部分）
3. 群里一律不发起、不批。（ADR-330 第 3 条，主人定）
4. **我定的**：配置 `qq.remoteApproval.enabled` 默认关，关着时和现在完全一样；保存后立即生效，不用重启接收，方便账号出事时马上关。
5. **我定的**：本地单什么时候算“主人离开”：审批等了 `awayAfterSeconds`（默认 60 秒）还没人处理，且主窗口不在前台或系统空闲超过这个时间。设成 0 就是一出单就发。**实现里只接了“主窗口不在前台”这一个信号（`Options.WindowForeground`，没有给就当不在），没有系统空闲**：窗口在前台但人走了就不发，偏保守；要加系统空闲时在 `Away` 回调里补。
6. **我定的**：“同意”的认法。只有一单且没有歧义时裸“同意”就算（ADR-305）。这几种要带单号或引用那条审批消息：几单同时在等；最近 10 分钟内有单过期、取消、被本机处理，或者刚重启（防迟到回复转到新单）；消息发到还不到 3 秒（来不及看）。
7. **我定的**：批准后执行前再比一次目标（卡片标题、内容、预览、写入目标的真实路径），变了就不执行，让她重新申请。只对回答来自 QQ 的批准做（主人在 QQ 里看的是建卡时的旧内容；本机点的看的是当前卡）。
8. **我定的**：QQ 发起的写文件和命令不拍检查点，结果里写明不能回退。检查点的恢复入口只认本地身份，接过来要另做界面。

**依据**：审查（`data/dev/harness-audit/00-总表.md` B21）说审批只能在桌面点、人一走长任务就卡住；主人要的是引导加简单兜底。

**影响清单**：AGENTS「安全边界」第 2 条；`docs/架构.md`“执行与外发”；`docs/subsystems/qq.md`、`tools.md`、`runtime.md`；`sumika.example.yml`；`native/internal/settingsreg`；`docs/待办.md` 第 11、75 行。

## 先参考了什么

本次读的本机克隆：Hermes `785c2e5`（2026-11-04）、Codex `8703d32`（2026-11-09）、opencode `12a661e`（2026-11-08）、OpenClaw（`_audit`）、oh-my-pi `21f1188`（2026-11-09）、Pinky `5bc3070`（调研报告 `data/dev/pinky-review/报告.md`）。Claude 官方 Telegram 插件用 gh 取 `external_plugins/telegram/server.ts`。Claude Code 核心未公开，只读到插件和 CHANGELOG。

| 来源 | 读到的 | 采用 | 不采用 |
|---|---|---|---|
| Claude 官方 Telegram 插件 `server.ts` | `PERMISSION_REPLY_RE = /^\s*(y\|yes\|n\|no)\s+([a-km-z]{5})\s*$/i`，入站先 gate 再截走明确 verdict，不交主模型；按 sender 认 | 回复在模型之前截走；精确匹配整句；短单号避开易混字符 | 每次都要单号（ADR-305 定了一单时裸“同意”就算） |
| Hermes `tools/approval_gateway_wait.py` | 每单一个等待对象；超时、中断、通知失败都不执行；取消时记原因，不把撤回说成用户拒绝；“once 只盖一次副作用，跟随者要重新问” | 通知失败和超时都按没批；取消和拒绝说法分开；一次批准只盖一次 | `/approve` 默认批最老的、`/approve all`（ADR-277 明确不猜） |
| OpenClaw `exec-approval-reply.ts`、`exec-approval-consumption.ts` | 交互动作带 approvalId 和 decision；消费核对 consumer、runtimeEpoch、未消费 | 按编号绑定单子；决定在同一临界区核对状态和期限 | 批准后宽限窗口；原会话默认能收完整命令 |
| Codex `protocol.rs` `ReviewDecision` | 默认值是 Denied；Denied 和 Abort 分开；ApprovedForSession 单独一档 | 拿不准一律不批；外平台不给“这个对话”“始终”两档 | 执行策略修正（amendment）和审批代理 |
| opencode `permission.ts` | Reply 只有 once / always / reject；`Unattended` 规则：没人在场时不问就拒；`pending` 按 id；请求失败时 `Deferred.fail` | 没有可用入口时直接按不通过，不挂着 | 无人值守规则集（这次不做规则系统） |
| oh-my-pi `/forget-approvals` | 记住的授权能一键清掉 | 关开关立即作废所有在等的远程单 | 文件和目录级授权（这次不做） |
| Pinky `approval/approval.go`、`gateway/pipeline.go` | 审批绑定调用和参数摘要、一次性、批准后重读现场变了就 `stale`、迟到回复由墓碑回答 | 第七条的批准后复查；墓碑回答迟到回复；状态说法分开（过期、取消、变了） | `approval_pending` 重连协议：我们的等待在同一次工具调用里同步进行，模型换不了参数 |

本地底子：`approval.Approvals` 的认领、持久化、订阅；`channel.Outlet` 的计划和回执；`clientinfo.go` 对主人 QQ 身份和新鲜配置的判断（规格-客户端自知第四节）。

## 一、目标和范围

例子 1：主人不在电脑前，她在本地跑一个任务要写文件。等了 60 秒没人点，完整内容发到主人 QQ 私聊。主人回“同意”，本地任务接着跑，本地卡片立刻消失；主人回家在本机先点了，QQ 里收到一句“本机已批准这一单”。

例子 2：主人在 QQ 私聊说“帮我看下下载目录有什么”，这一轮她可以列目录；要写文件或跑命令时，完整内容发到同一个私聊，主人回“同意”才执行，她把结论说给主人。

做：上面两个例子；配置和设置页开关；假 SnowLuma 逐帧验收。

不做：群（不发起、不批、不提示）；yolo 继承；沙箱和任务目录；别的平台和改投；自然语言判断“同意”；子代理和生态工具在 QQ 的放开；手机端。

### 四种场合

| 场合 | 发起资格 | 能不能在这里批 | 卡片发不发 QQ |
|---|---|---|---|
| 本地私聊 | 和现在一样 | 本机照旧 | 主人离开时发主人 QQ 私聊，那边也能批 |
| 本地群聊 | 和现在一样（Go 还没有本地群执行） | 本机 | 不发 |
| QQ 私聊（主人） | 开关开时可以申请执行和写文件，每次问 | 只能在这个私聊 | 就是这里 |
| QQ 私聊（别人） | 没有 | 不能 | 不发 |
| QQ 群（主人或群友） | 没有 | 不能：群里的“同意”就是一句普通的话 | 不发、不提示 |

## 二、技术路线

> 下面第 1～4 条已实现；**第 5～9 条（发起资格、工具门、`Gate.Check` 的 QQ 分支、工作目录、提示词）属范围 2，没有实现**。卡片、结局、被截走的回复行的来源 `automation:remote-approval` 在 `history.NotSaidSQL` 里（模型历史、自动行、压缩看不到），但没进 `store/display.go` 的隐藏列表，所以界面照常显示。

```
Approvals（唯一状态机）
  │ Subscribe：请求、结局
  ▼
approvalrelay.Relay（新包，只管投影和认单）
  │ Deliver：AutomationOutlet → Outlet → 假 / 真 SnowLuma
  │ Control：Pipeline.OnStored 最前面截走主人回复
  ▼
Approvals.DecideVia(id, once|deny, via)
```

1. **不另造状态机**。结算只走 `Approvals`，先答先算由它的认领保证；`Relay` 只有“票”：等主人离开、发送中、开着、关了。关了的票留墓碑回答迟到回复。
2. **发卡**：`channel.Outlet.BindAutomation("remote-approval")`，来源 `automation:remote-approval`，走唯一出口和发送门；发送门不开（试运行、暂停、断线）就算发不到。等计划终态，状态是 `done` 才算送达，部分发送、结果未知、失败都不开票。
3. **截回复**：`PipelineConfig.Control` 在 `OnStored` 最前面，在观察、拦截链、闸门、私聊打断之前。返回 true 这一条到此为止，不进模型。身份判断见第四节。
4. **历史**：`automation:remote-approval` 进 `history.NotSaidSQL`，模型历史、补给下一批的自动行、压缩都看不到卡片和结局；被截走的回复行来源改成同一个；界面照常显示。记忆证据只认 owner、peer，也不会把这些当证据。
5. **发起资格**：宿主在 `Responder.issueLocked` 里问 `ExecGrant`，把 `Audience.RemoteExec` 签成真；字段 `json:"-"`，不进持久载荷，重启不复活。`validateInput` 再核形状。
6. **工具门三处都认同一个判断**：`turn.toolsFor` 给子集（`policy.RemoteExecTool`：read、ls、grep、find、write、edit、powershell、process_output、process_stop），`prepareCall` 执行前和批准后各问一次 `Config.RemoteExecValid`（现读配置），`policy.Decide` 第一关放行同一子集。不把 QQ 听众填成 console。
7. **`Gate.Check` 的 QQ 分支**：`mode` 固定 manual，不查授权，不给“这个对话”“始终”，期限固定 5 分钟；不改全局 `Gate.Mode`。
8. **工作目录**：`freezeWorkspace` 对 `RemoteExec` 给和本地一样的默认目录；其他 QQ 回合仍是零值。
9. **提示词**：`RemoteExec` 的回合用 `scene-private-exec` 换掉 `scene-private`（后者写着不能动电脑）。

### 什么时候开票

| 请求来源 | 条件 | 什么时候发 |
|---|---|---|
| 本地私聊 | 卡片本来就在主窗口显示的那些（`console:<角色>` 和话题，不含自检） | `awayAfterSeconds` 后主人不在；之后每 15 秒再看一次，直到有结果 |
| QQ 私聊 | `Scope` 是主人、会话是当前接收账号和主人号 | 立刻 |

发不出去时：本地单照常留在本机；QQ 单按不通过，告诉她发不到主人私聊。

## 三、参数

| 参数 | 值 | 说明 |
|---|---|---|
| `qq.remoteApproval.enabled` | false | 配置键，立即生效 |
| `qq.remoteApproval.awayAfterSeconds` | 60（0～600） | 配置键；0 表示一出单就发 |
| 窗口期限 | 300 秒（`approval.RemoteTTL`） | 固定常量。QQ 单：创建起算，本地单也等于创建起 300 秒。本地单：送达确认起 300 秒，且不超过卡片自己的期限；过了 QQ 这边作废，本机还能批 |
| 歧义窗口 | 10 分钟 | 单子非主人回复结束后、刚启动后，这段时间裸“同意”要带单号 |
| 最短阅读时间 | 3 秒 | 送达后这么短内的裸回复要带单号 |
| 开着的票上限 | 16 | QQ 单超了按不通过；本地单不发 |
| 墓碑 | 256 张 | 回答迟到回复 |
| 回复长度 | 1024 字节 | 超了不算决定 |
| 单号 | 4 位数字，`crypto/rand` | 和开着的票、墓碑都不重复 |
| 单段长度 | 1500 字符，最多 8 段 | 放不下算发不全，不能在 QQ 批 |
| QQ 执行回合的字数保险丝 | 2000 字 | 普通 QQ 回合仍是 200 |

## 四、形状 × 入口

范围 1 的入口只有三个：A 入库后的认人和解析（`Control`）、B 选单和结算（`Relay` + `Approvals`）、D 发卡（出口和发送门）。范围 2 的入口（发起资格、工具门、执行前复查）还没有，相关格子写“待做”。四种场合：本地私聊、本地群聊、QQ 私聊、QQ 群——本地群聊的卡不发 QQ（`remoteEligible` 只认本地私聊），QQ 群没有任何入口。

“测了”后面是测试名：`approvalrelay` 包的是单元层（假出口、真 `Approvals`），`TestQQApproval*` 是 `internal/app` 里假 SnowLuma 逐帧的端到端。每道门单独关掉后哪些测试会红，见 `data/dev/qq-approval/改坏记录.md`。

| 形状 | 结果 | 测了 |
|---|---|---|
| 主人正常回“同意 编号”“拒绝 编号” | 结算，QQ 回确认，回复行改标来源 | `TestLocalApprovedFromQQ`、`TestDeniedFromQQ`；`TestQQApprovalLocalApprovedFromQQ`、`TestQQApprovalDeniedFromQQ` |
| 昵称写“主人”、名片写主人号 | 不结算：身份按号，由名册认 | `TestControlIdentityShapes`（昵称写主人但不是主人号、别人发的）；`TestQQApprovalHostileShapes` |
| 零宽字符、双向字符、BOM、变体选择符 | 不结算，只提示一次；卡里把它们显示成 `\uXXXX` | `TestParseReply`、`TestInvisibleOnlyHints`、`TestRenderCardEscapesInvisible`、`TestQQApprovalHostileShapes` |
| 转发、引用里写同意，夹图、@、表情 | 不是决定，照普通聊天走 | `TestParseReply`、`TestControlIdentityShapes`、`TestQQApprovalHostileShapes` |
| 引用别的消息回“同意” | 提示引用的不是审批，不结算 | `TestQuoteOfOtherMessage` |
| 引用审批卡回“同意” | 结算被引用的那一单 | `TestQuoteOfCardSelectsThatTicket` |
| 单号和引用对不上 | 提示，都不结算 | `TestIDQuoteMismatch` |
| 别人（陌生号、群友）回同意 | 不结算，同意只是一句话 | `TestControlIdentityShapes`、`TestQQApprovalHostileShapes` |
| 自己的号（发言人是账号、`message_sent` 回声） | 不结算 | `TestControlIdentityShapes`（自己的号回声、自己的号当发言人）、`TestQQApprovalHostileShapes` |
| 主人号等于接收账号 / 等于某个账号 | 绑定不成立：不发卡、不认回复 | `TestQQApprovalOwnerEqualsAccount`、`TestQQApprovalBindingChanges`、`TestQQApprovalNoCardWhenBindingBroken` |
| 消息号缺、为 0 | 不认 | `TestControlIdentityShapes` |
| 单号写法：三位、五位、全角数字、超长回复、非法 UTF-8 | 不是决定 | `TestParseReply` |
| 配置写成字符串 true、数字、负数、超范围 | 解析失败，不当成默认的成功 | `TestRemoteApprovalConfigShapes`（config 包） |
| 迟到回复（过期后；带旧号或裸“同意”） | 不结算，不转到新单 | `TestLateReplyDoesNotMoveToNewTicket`、`TestQQApprovalLateReplyIgnored` |
| 正好 300 秒、299 秒 | 299 秒可以，300 秒算过期 | `TestDeadlineBoundary` |
| 同时多单 | 裸“同意”只追问，带号才结算那一单；错号不处理 | `TestParallelTicketsAskFirst`、`TestWrongIDDoesNothing`、`TestQQApprovalParallelAsksFirst` |
| 本机和 QQ 同时答 | 恰一方赢，输的被告知；认领在 `Approvals` 同一把锁里 | `TestLocalAndQQRace`（40 轮）、`TestDecideViaRacesLocal`（200 轮）、`TestLocalFirstUpdatesQQ`、`TestQQApprovalLocalFirstUpdatesQQ` |
| 同一条消息重推、补录 | 不再结算，也不再回话 | `TestSameMessageTwiceDecidesOnce`、`TestControlIdentityShapes`（重复、补录） |
| 发不出去（明确一段都没发出去）/ 试运行 / 发送门没开 | 不开票，本机的卡照常等，只试一次，不留墓碑；之后的裸“同意”是普通聊天 | `TestDeliveryFailureStaysLocal`、`TestNothingSentLeavesNoTombstone`、`TestQQApprovalSendFailed`、`TestQQApprovalDryRunNotSent` |
| 结果未知 / 部分发送 / 90 秒超时前已发出几段（`Delivery.Attempted`） | 不开票，但编号收成墓碑（“没确认送达”）并记一次结局：10 分钟内裸“同意”必须带号，回这个编号得到“没有这一单”；不转到后来的单 | `TestUnknownDeliveryLeavesTombstone` |
| 带 4 位编号的同意/拒绝，此刻没有票也没有墓碑 | 截走并回“没有编号 X 的待批操作，没有处理”，不进模型（编号只来自卡片，卡片不在模型历史里，放进去角色只能盲回） | `TestNumberedReplyWithoutTicketIsIntercepted` |
| 发卡途中（第一段已到手机、后面的段还在发）回编号 | 截走并回“这张卡还在发，发完请再回一次”，不结算、不进模型 | `TestReplyWhileSendingIsHeld` |
| 回复已接受但结局写库失败（按拒绝处理）；回合取消与批准同时发生 | QQ 的确认话由结局回调按真实结局说：写库失败说“没能生效、操作没有执行”，不出现“已批准”；`persistAndNotify` 改写结局时保留来源 | `TestPersistFailureNotReportedAsApproved`、`TestPersistFailureKeepsVia`、`TestApprovedConfirmationComesFromResolution`（取消与批准同时发生的窄缝见第六节） |
| 平台时间比这张卡开始发送还早的裸回复（断线期间说的、重连后才推来） | 不结算，提示带号；宿主给不出平台时间时只靠到达时刻 | `TestStalePlatformTimeBareReplyNeedsID` |
| 协议端重连后 10 分钟内的裸回复 | 要带号（`NoteReconnect`，接在连接状态“已连上”） | `TestReconnectOpensAmbiguityWindow` |
| 没声明的工具参数超过 6000 字被截短（`Request.SubjectTruncated`） | 不开票，告诉主人回本机处理；本机的卡照常 | `TestRenderCardRefusesTruncatedSubject`、`TestUndeclaredToolSubjectTruncatedFlag`、`TestQQApprovalGateEndToEndTruncatedArgsStayLocal` |
| 端到端（真实 `Gate.Check` → 审批 → 假 SnowLuma）批准 / 拒绝 / 过期 | 批准：放行恰一次，重复同意不再执行；拒绝、过期：拦下，模型收到“主人拒绝了”“审批过期了”，迟到的同意不执行（执行计数按“没拦下才执行”记，工具本体不在 `Gate` 里） | `TestQQApprovalGateEndToEndApprovedRunsOnce`、`TestQQApprovalGateEndToEndDeniedNotExecuted`、`TestQQApprovalGateEndToEndExpiredNotExecuted` |
| 发卡期间本机先答 | 不开票，补一句结局 | `TestLocalDecisionWhileSending` |
| 重启 | 票只在内存里，不跨重启（`Approvals` 关机时取消全部）；刚启动 10 分钟内裸“同意”要带号 | `TestStartupWindowNeedsID` |
| 刚送达 3 秒内的裸回复 | 要带号 | `TestTooFreshNeedsID` |
| 账号绑定变、主人号变 | 旧票作废，新主人号看不到旧票 | `TestOwnerChangedCannotUseOldTicket`、`TestQQApprovalBindingChanges` |
| 开关关（从头关、中途关） | 一帧不发、不截、不改标；中途关立即作废，再打开不复活 | `TestQQApprovalOffBehavesAsBefore`、`TestDisableClosesTickets`、`TestBindingClosedWhileWaiting`、`TestQQApprovalTurnedOffWhileWaiting` |
| 主人在场（不离开） | 不发 | `TestDefaultNothingSentWhenNotAway`、`TestAwayZeroSendsAtOnce` |
| 卡来自 QQ、群、自检话题、别的角色 | 不发 | `TestNotEligibleNotSent`、`TestQQApprovalOnlyLocalPrivateCards` |
| 预览被截短、内容放不下 8 段 | 不开票，告诉主人回本机处理 | `TestUnsendableCards`、`TestRenderCardSegments` |
| 开着的票超过 16 | 多的不发 | `TestOpenLimit` |
| 群里任何形状 | 不发、不批，“同意”是普通话 | `TestControlIdentityShapes`（群聊）、`TestQQApprovalHostileShapes`（群里同意） |
| 模型说“同意”、工具结果写同意 | 不可能：模型输出不进 `Control`，也没有批准工具；`DecideVia` 只在宿主代码里被调，且只收 once、deny | `TestDecideViaRejectsOtherChoices`（结构保证，不是测出来的） |
| 默认关时和现在完全一样 | 不发帧、不截消息、不改标来源、`policy` / `approval` 对拍不变 | `TestQQApprovalOffBehavesAsBefore`；`policy`、`approval`、`turn` 原有测试未改 |
| 写文件的目标在等待期间被改（批准后复查） | 回答来自 QQ 且内容、预览、真实路径变了：不执行，模型收到“和主人批准时看到的不一样，没有执行”；没变照常放行；本机点的不复查 | `TestQQApprovalRechecksFileBeforeRunning`、`TestQQApprovalUnchangedStillRuns` |
| 范围 2：QQ 私聊发起执行（别人发起、角色接角色、定时、补录唤醒、群、`RemoteExec` 不进持久载荷、工具清单没有批准工具、`Gate.Check` 的 QQ 分支） | 待做 | — |

## 五、禁止的偷懒写法

- 把 QQ 听众填成 console 过门。`ApproveRead` 那种写法不抄。
- 子串匹配“同意”；剥掉零宽字符再比；从引用、转发、图片里拼出“同意”。
- 取队首、取最新、让模型选单。过期后把同意挪到下一单。
- 用定时器代替期限比较。结算前必须比一次当前时间。
- 消息发送失败、结果未知也开票；发不到还说“已发你”。
- 改全局 `Gate.Mode`、`ApprovalMinutes` 来实现 QQ 分支。
- 给 QQ 单 `conversation`、`always` 两档。
- 把 `RemoteExec` 写进持久载荷、或从 `PrivateOK` 推出来。
- 卡片进模型历史，再靠提示词说别念出来。
- 开关关着时走任何新分支（发帧、截消息、多一道复查）。

## 六、已知的坑

- `Approvals.Decide` 对不认识的 choice 按批准算，所以新入口 `DecideVia` 只收 once、deny，别的报错。
- 回声可能比回包先到，`Held.Done` 只保证计划终态；引用选单靠 `channel_msgs` 查到事件 seq，和 `Held.EventSeq` 比。
- 结果未知的段 5 分钟内可能被回声认领成确认。这里不追认：票已按没送达关了，少批不多批。
- 主人在 QQ 里发普通消息会按原路打断她正在跑的回合，所以这一单会被取消，和本地发新消息一样。截走的只有“同意”“拒绝”这类，以及“好的”“可以”这种短答（提示要明确回，单子继续等）。
- `artifact.ValidScope` 要求 `AuthorityID`，QQ 身份没有，所以 QQ 执行回合走非 Work 路径：工具结果留在实例里，重启后不恢复。这和现在的 QQ 回合一致。
- 检查点的 `Authorize` 只认本地来源，QQ 写入要绕开它并写明不能回退，同时保留“不写数据目录”的拒绝。
- 主人的回复行被改标以后，界面仍显示，模型和记忆不当主人说过的话。
- 无法由后台证明的情况：A 批完、B 已发出超过 3 秒后，主人新发一条裸“同意”其实想说 A。平台信息和“想批 B”完全一样，这个窄缝按 ADR-305 接受。
- 回合取消和 QQ 批准同时发生时，`Ask` 返回 cancelled，但已发布的结局是 approved（`DecideVia` 先认领、取消后到）：QQ 会说“已批准”而工具没有执行。窄缝，没有修，要修得改 `Approvals` 的发布顺序，另开一件（待办）。
- 回复被接受后结局回调迟迟不来：巡检在 30 秒后告诉主人“不知道有没有生效，请到本机确认”。（`TestDecidingWithoutResolutionTimesOut`）
- 配置现读：`Binding` 每次读文件。放在发卡、结算、发起、执行前这几个点，不在每条聊天上读。

## 七、验收标准

1. 默认关：配置不写时，主人私聊发“同意”照普通消息走；假协议端没有审批来源的帧；工具清单没有新增；`policy`、`approval` 对拍不变。（`TestQQApprovalOffBehavesAsBefore`；工具清单没有新增靠结构：本功能没有改 `turn`、`policy`、`audience`）
2. 本地单：离开时发到私聊；帧里有完整命令和编号；回“同意”后没被拦下（放行恰一次）；本地列表立刻少一张。（`TestQQApprovalLocalApprovedFromQQ`、`TestQQApprovalGateEndToEndApprovedRunsOnce`）
3. 本地先答：QQ 收到结局帧，之后的“同意”不再结算。（`TestQQApprovalLocalFirstUpdatesQQ`）
4. （待做，属范围 2）QQ 发起：主人私聊这一轮能列目录、能写文件；写文件前有卡；回“同意”后临时目录里真的有文件；她的回复发回私聊。（测试名做的时候再写，现在没有）
5. 拒绝、过期：工具零执行；她收到不执行的说明。（`TestQQApprovalGateEndToEndDeniedNotExecuted`、`TestQQApprovalGateEndToEndExpiredNotExecuted`）
6. 第四节每个“测了”的格子都有测试；每道门单独改坏后对应测试变红，记录在 `仓库根/data/dev/qq-approval/改坏记录.md`。
7. 设置页有开关和一句风险说明，`ui.NewTester` 验证切换后写回；截图存 `仓库根/data/dev/qq-approval/`。

## 八、和规格-外平台执行和远程审批的差异

| 那份规格 | 这次 | 原因 |
|---|---|---|
| 决定证据带 32 字节 nonce，内存存原文、库存摘要 | 不做 nonce，只有 `Approvals` 的宿主 ID 和短单号 | nonce 不广播、不要求主人抄，只在进程内关联，等价于宿主 ID；真正的保护是身份认证 |
| ActionDigest 存审批事件 | 不存摘要；批准和执行在同一次工具调用里，`pc.Args` 冻结；第七条复查目标 | 模型换不了参数；要防的是等待期间世界变了 |
| 新增审批追加事件表 | 步骤的 `approval_json` 加 `via` | 只有一个入口，不值得加表；要多入口时再说 |
| 歧义标记永不自动清 | 10 分钟窗口，或者主人明确决定后清 | 否则隔几天的下一单也要带单号；窗口内的保护一样 |
| 零单回“没有待确认操作” | 最近没有单时裸“同意”放给普通聊天（常用词）；带 4 位编号的同意/拒绝一律截走并回一句；最近有单裸回复也回一句 | 不劫走常用词；编号只来自卡片，放进模型只会被盲回 |
| 群、改投、yolo、任务目录、子代理范围 | 不做 | ADR-330 和这次任务范围 |
