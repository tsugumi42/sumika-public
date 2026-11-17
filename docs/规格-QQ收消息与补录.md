# 规格：QQ 收消息、去重、归属、补录接缝与发送确认

> 状态：草案，2026-11-11。从 Go 现状（worktree 基于 main，下面所有"文件:行号"都是这一份）反推写成；Go 和以后的 Bun 后台（ADR-345 第 4 批）都以本文为准，影子跑比出差异时用本文判谁对（第八节）。本文待主人确认前不算定稿，第九节"存疑"里的每一条都没替主人定。
> 2026-11-16 整理：影子跑判据随 ADR-359（Bun 冻结）失效；I-27 和补录接缝仍被现行规格引用，那几节当现行规则读。
>
> 参考了：
> - [规格-QQ渠道](规格-QQ渠道.md)（认人、陌生人、状态标签；有冲突的地方列在第七节）；
> - [规格-补录覆盖](规格-补录覆盖.md)：自动补录一轮**怎么扫、怎么判覆盖、缺口怎么变**已经有正式规格和 32 个共用场景，本文**不重写**，只写它和入库一站的接缝（第四节），其余指过去；
> - [规格-QQ收图下载和断线补录](规格-QQ收图下载和断线补录.md) 第五节（参数表、检查位置与连续水位）；
> - [规格-QQ发送上线](规格-QQ发送上线.md) 3.1、3.2（回包怎么判）；
> - 决策记录 ADR-007（一个号绑一个角色）、ADR-116（不接话名单只限制触发）、ADR-275、278（补录缺口的底线与简化）、ADR-345（换 Bun）；
> - [词汇表](词汇表.md)：场合只用"本地私聊、本地群聊、QQ 私聊、QQ 群"四种；
> - 共用场景 `testdata/shared/` 下的 `qq-intake/`、`backfill/`、`qq-send/`、`qq-frames/` 及各自 README。
>
> 范围：协议端一帧进来，到入库、到"哪些角色看到了"为止；补录一轮提交消息时和入库一站的交互；一段话发出去，到回执、回声认领为止。**不含**：闸门/拦截链/要不要回（`gate.go`、`pipeline.go`）、媒体下载（`media_fetch`）、链接展开、守卫的内容判断、手动补录和对齐任务。
>
> 本地私聊、本地群聊不经 QQ 适配器、不走本文的键和回声规则（渠道名是 `console`）；本文说"私聊""群"时只指 QQ 私聊、QQ 群。
>
> 记号：每条规则编号（I=入库，N=通知，B=补录接缝，S=发送，T=时间与数字），末尾写"依据"（Go 文件:行号、共用场景）和"覆盖"（共用场景名、Go 测试名；都没有的写"未覆盖"）。共用场景在 `testdata/shared/` 下，省略前缀。

## 一、名词

| 名词 | 含义 |
|---|---|
| 号 | QQ 号，一律字符串。 |
| 登录号 / 她的号 | 配置里 `qq.accounts` 的号，每个号绑一个角色（ADR-007），一个角色可以绑几个号。收到一条消息的"哪个号"叫 `Account`。 |
| 会话串 | 群：`qq:group:<群号>`；私聊：`qq:private:<登录号>:<对方号>`（`types/conversation.go:34`）。同一个群被两个号看到是同一个会话；同一个朋友私聊两个号是两个会话。 |
| 平台消息号 | 协议端给的 `message_id`，十进制整数原文，可能为负，可能超过 2^53（第六节）。 |
| 事件 | 库里 `events` 的一行，一条消息或一条通知。 |
| 看到了 | 表 `observations`：哪个角色看到了哪个事件。去重时只补这个。 |
| 映射 | 表 `channel_msgs(channel, conversation, platform_id) → event_seq`：去重的唯一键。 |
| 水位 / 缺口 / 连续证明 | 见 [规格-补录覆盖](规格-补录覆盖.md) 第一节，本文不再解释。 |

## 二、一条消息从协议端到入库

流水线：协议端帧 → 适配器翻译（`translate`）→ `Intake.Submit` 的检查 → 按会话排队 → 准入 → 回声认领 → 入库事务 → `OnStored`。下面分步写规则。

### 2.1 翻译：帧变成统一消息

- **I-1 哪些帧是消息。** `post_type` 是 `message` 或 `message_sent` 就是消息；没有 `post_type` 但有 `message_type` 和非 null 的 `message` 也当消息。无效 JSON、不是对象、认不出的 `post_type` 整帧丢弃（不交出任何东西）。依据：`native/internal/channel/snowluma/snowluma.go:378-388, 482-495`。覆盖：`qq-frames/hostile-and-malformed.json`、`qq-frames/messages-basic.json`。
- **I-2 发言人号。** 取 `user_id`，没有取 `sender.user_id`；过一遍"零值归一"：空白、`0`、`-0`、`0.0`、`00`、`+0` 一类都算没有，其余原样保留（不去空格、不去前导零，`0200` 不是 `200`）。外层是 0 时**不**回退去读 `sender`。依据：`snowluma.go:128-135, 494-499`。覆盖：`qq-frames/hostile-and-malformed.json`、`qq-frames/messages-basic.json`。
- **I-3 是不是"她自己发的"。** `post_type == message_sent`，或发言人号等于收到它的登录号，一律算 `kind = self`，发言人记成登录号。其余是 `kind = message`。依据：`snowluma.go:501-505`。覆盖：`qq-frames/messages-self.json`。
- **I-4 会话怎么落。** 群还是私聊看 `message_type`（`group`/`private`），没有它才看 `group_id` 非空非 0；临时会话的私聊帧即使带着来源群号也落私聊。私聊会话的"对方"：别人发的取发言人；她自己发的按 `target_id`、`peer_id`、`user_id` 的顺序找第一个"非零且不是她自己"的号，找不到则对方为空，之后被入库一站拒收（I-8）。依据：`snowluma.go:390-418`。覆盖：`qq-frames/messages-self.json`、`qq-frames/hostile-and-malformed.json`。
- **I-5 平台消息号原样取。** `message_id` 用 JSON 原文：字符串取字符串，数字取数字原文（不经 float，2^53 加 1 这个数往返不变，`-5` 保留负号）；缺、null 是空串。依据：`snowluma.go:113-123, 510`。覆盖：`qq-frames/messages-basic.json`（含 2^53 加 1、`-5`、`0`、空）。
- **I-6 消息体降级。** `message` 不是段数组时，退成一段文字：先取 `raw_message`，没有再取字符串形式的 `message`，都没有则空段列表（不丢整条）。依据：`snowluma.go:435-450`。覆盖：`qq-frames/segments.json`。

### 2.2 交给入库一站之前的检查

- **I-7 时间补齐。** 帧里的 `time`（秒，整数、小数、数字字符串都认）必须是正数且小于 1e11（再大的像毫秒，按缺失）；缺失时适配器在交出之前用**收到这一刻**补上。依据：`snowluma.go:415-432`；`snowluma/connect.go:546-554`。覆盖：`qq-frames/messages-basic.json`（`time_unix` 为 0 表示没给）；补齐本身见 Go 测试 `TestZeroTimeUsesNow`（`intake_test.go:839`）。
- **I-8 收下之前的五项检查，任一不过就整条不收（返回错误，不入库，不进队列）。** 依据：`channel/intake.go:244-263`；覆盖：Go 测试 `TestRejectedBeforeQueue`（`intake_test.go:378`）。
  1. `Kind` 只能是 `message` 或 `self`；
  2. 发言人号非空；
  3. 收到它的号必须在名册里（不在配置里的号不接入，不悄悄接新号）；
  4. 会话合法：群不带登录号、私聊必须带，且有平台会话号；
  5. 私聊会话串里的登录号必须等于交来的 `Account`（否则会存进别的号的私聊）。
- **I-9 段拷贝与截断。** 排队前把段拷一份；每段的 `Raw` 预览按字（rune）截到 `RawMax`，不切开一个字。依据：`intake.go:221, 267-276`。覆盖：Go 测试 `TestSegsCopiedAtSubmit`（`intake_test.go:1071`）。
- **I-10 排队。** 队列键是（渠道、形式、登录号、会话号）四个字段拼起来，不拆会话串；同一个键的消息、通知、补录**一个接一个**处理，不同键互不等待。适配器的读循环只负责排进去就返回，不等入库。依据：`intake.go:203-209, 279-281`。覆盖：Go 测试 `TestSameConversationInOrder`、`TestConversationsDoNotWaitForEachOther`（`intake_test.go:445, 475`）。
  - 注意：队列键里的 `Self` 对群是空的，所以**两个号看同一个群的两份消息在同一条队列里**；两个号的私聊各一条队列。

### 2.3 准入

- **I-11 准入由平台插件定，入库一站只执行。** 返回三种结果：`Drop`（完全不收：不存、不记看到了）、`NoObserve`（存下来，但不记看到了）、附加标签。依据：`intake.go:45-51, 295-303`。覆盖：Go 测试 `TestAdmission`（`intake_test.go:701`）。
- **I-12 QQ 的准入规矩。** 依据：`app/channel.go:536-575`；覆盖：入库一站执行准入见 Go 测试 `TestAdmission`；QQ 的名单规矩只有私聊白名单与身份的间接测试（`app/guard_identity_test.go:243, 321`）和未知渠道丢弃（`app/admit_channel_test.go:16`），群白名单 `Drop` 与陌生人 `NoObserve` 这两条**未覆盖**。
  - QQ 群：群号不在 `qq.groups` 里 → `Drop`；在里面 → 全收。
  - QQ 私聊：发言人是"别人"（既不是主人也不是角色的号）且不在 `qq.privateAllow` 里 → 陌生人：`NoObserve` + 标签 `stranger`（存原文，不记看到了，因此不进记忆、也不被以后补着总结）；其余全收。主人、角色号发的私聊永远不是陌生人。
  - 通知的准入同消息（I-30 的后半句）。
- **I-13 不接话名单（机器人名单）只限制触发，不影响存。** `Roster.NoReply`（平台:原始号 → 原因）在名单里的号，其消息照存、照记看到了，只是 `CanTrigger` 为假，闸门不放行。依据：`channel/roster.go:40, 115-139`；ADR-116。覆盖：`channel/bot_test.go`（Go）；共用场景**未覆盖**（因为只影响触发，不影响入库结果）。

### 2.4 去重：唯一键与重复到达

- **I-14 唯一键。** （渠道，会话串，平台消息号）三个一起。跨账号：群的会话串不含登录号，所以两个号看到同一个群的同一条是**同一条**；私聊的会话串含登录号，同一个朋友私聊两个号即使消息号相同也是两条。依据：`store/inbound.go:12-16, 67-84`；`types/conversation.go:34-39`。覆盖：`qq-intake/duplicate-by-channel-conversation-platform-id.json`、`qq-intake/same-id-in-other-conversation-is-not-duplicate.json`、`qq-intake/two-accounts-same-group-one-event-two-observers.json`、`qq-intake/same-peer-private-to-two-accounts.json`。
- **I-15 没有号就不去重。** 平台消息号是空串，或去掉前导 `0` 后是空（`0`、`00`），不能做键：每条单独入库、不写映射表，并留一行日志（只写会话和号的原值，不写消息原文）。依据：`store/inbound.go:12-16`；`intake.go:389-393`。覆盖：`qq-intake/platform-id-edge-cases.json`；日志见 Go 测试 `TestNoPlatformIDIsLogged`（`intake_test.go:1311`）。
- **I-16 重复到达只补"看到了"。** 键已在库里：不新增事件、不改已有事件的任何字段（包括内容、时间、`actor`、`origin`、标签）；只给本次的观察者补一条"看到了"（已有的不重复记）。结果标 `Duplicate`，并带回已有那条事件的 `seq/id/actor/origin`。依据：`store/inbound.go:67-84`；`intake.go:397-400`。覆盖：`qq-intake/duplicate-by-channel-conversation-platform-id.json`、`qq-intake/two-accounts-same-group-one-event-two-observers.json`；Go 测试 `TestDuplicateKeepsStoredOrigin`（`intake_test.go:1108`）。
- **I-17 先到的算数。** 同一条消息，实时先到还是补录先到，库里留下的内容、时间、标签（有没有 `backfill`）都是**先到的那一份**的。依据：同 I-16；`qq-intake/backfill-after-live-is-duplicate.json`（两个顺序都测了）。覆盖：该场景。
- **I-18 一个事务。** 事件、映射、"看到了"在一个事务里，要么都在要么都不在；引用的连接（I-22）也在同一个事务里。依据：`store/inbound.go:57-113`。覆盖：Go 测试 `TestAppendInboundIsOneTransaction`、`TestAppendInboundDuplicateFailureKeepsOriginal`（`store/inbound_test.go:216, 243`）；共用场景**未覆盖**（场景只比最终状态）。
- **I-19 并发重复。** 同一条被并发交来多次，最终只有一个事件。依据：同一条队列串行（I-10）加事务里先查再插。覆盖：Go 测试 `TestConcurrentDuplicateDelivery`（`intake_test.go:961`）。

### 2.5 谁说的：ActorKind、ActorID、Origin

- **I-20 认人按号，不按名字，顺序固定：主人 > 角色的号 > 别人。** 依据：`channel/roster.go:104-112`。覆盖：`qq-intake/actors-and-origins.json`；Go 测试 `TestActorsAndOrigins`（`intake_test.go:267`）。

  | 条件 | ActorKind | ActorID |
  |---|---|---|
  | 发言人号 = 该渠道配置的主人号（先判，主人号同时配成账号也算主人） | `owner` | `owner` |
  | 发言人号是名册里某个账号 | `character` | 绑的角色 id |
  | 其余 | `human` | `渠道:号`，例如 `qq:200` |

  群名片改成"主人"的群友仍是 `human`（名字不参与身份）。主人和角色的 `ActorID` 是固定名字，不是号。
- **I-21 Origin 的取值与判定。** 依据：`intake.go:412-430`；`roster.go:15-21`。覆盖：`qq-intake/actors-and-origins.json`、`qq-intake/self-message-origin-by-takeover-time.json`；Go 测试 `TestSelfMessageOriginByTakeover`（`intake_test.go:752`）。

  | 发言人 | 来源 | Origin |
  |---|---|---|
  | 主人 | 任意 | `owner` |
  | 别人 | 任意 | `peer` |
  | 角色的号（没有被回声认领的） | 实时到的 | `human`（真人拿着她的号手发的） |
  | 角色的号 | 补录来的，消息时间 ≥ 接管时刻 | `human` |
  | 角色的号 | 补录来的，消息时间 < 接管时刻，或这个角色还没有接管时刻 | `external`（以前别的程序用这个号说的） |

  接管时刻 = 这个角色在这个渠道里**看到的第一条非补录事件**的时间（`Store.FirstLiveAt`），缓存在内存里并随每次新的"看到了"精确更新，不整个作废重查。它是启发式，没有验证（存疑 Q4）。依据：`intake.go:450-492`；`store/inbound.go:115-127`；覆盖：`qq-intake/self-message-origin-by-takeover-time.json`（含"补录来的群友消息不会把接管时刻提前"），Go 测试 `TestTakeoverCacheMatchesStoreAfterEveryDelivery`（`intake_test.go:1271`）。
  - `Origin` 的另外几种值（`automation:*`、`character`）是出站写的，入站不会出现。
  - 重复到达时，通知里带的 `Actor`/`Origin` 是库里已有那条的，不按这一份重算（同一事件只能有一个口径）。依据：`intake.go:397-400`；覆盖：`TestDuplicateKeepsStoredOrigin`。
- **I-22 引用连接。** 取第一个引用段的目标消息号；库里有（同渠道、同会话）就把新事件的 `reply_to` 连上它，没有就留空，不挡入库，也不事后补。依据：`intake.go:353-359`；`store/inbound.go:86-97`。覆盖：Go 测试 `TestReplyLinking`（`intake_test.go:817`）；共用场景**未覆盖**。
- **I-23 标签。** 标签依次追加：补录来的 `backfill`；`kind = self` 或发言人号等于收到它的号 `fromSelf`；准入给的（`stranger` 等）。存进事件的 `segments_json.tags`（不是单独的列）。"是不是补录来的"全库只有一个口径：`tags` 里有 `backfill`；`segments_json` 不是合法 JSON 的行当作不是补录。依据：`intake.go:344-351`；`store/inbound.go:48-51`。覆盖：`qq-intake/backfill-after-live-is-duplicate.json`（间接）；标签的具体内容未被共用场景逐字比，**未覆盖**。
- **I-24 另一个角色的号发的话。** 角色 B 的号发的消息被角色 A 的号看到，发言人是 `character:B`，不是 `human`。入库前回声认领（S 节）对"任何账号交来的角色号的消息"都问一次，不只问收到它的那个号。依据：`intake.go:305-311`；`channel/outlet_echo.go:36-40`。覆盖：`qq-send/echo-key-includes-account.json`；入库一侧的完整链路见 Go 测试 `TestEchoClaimedBeforeIntakeStoresIt`（`outlet_echo_test.go:272`），共用场景**未覆盖**。
- **I-25 内容文本。** `events.text` 是各段按规则拼出来的纯文字（`PlainText`）；同时存段、发言当时的名字、标签，和排好的"给模型看的一行"（`segments.line`，名片以后改了也不变）。排一行出意外时这一行为空，事件照存。依据：`intake.go:360-366, 434-448`。覆盖：Go 测试 `TestStoredLineRebuildsWithNameAndRole`（`intake_line_test.go:63`）、`TestPlainText`（`intake_test.go:849`）；共用场景只比 `text`，**不比** `line`。

### 2.6 看到了、通知后面的步骤

- **I-26 谁看到了。** 收到它的号绑的那个角色记一条"看到了"；准入给了 `NoObserve` 就不记。重复到达时再补一条（已记的不重复）。两个号同在一个群时，事件一份，两个角色各一条。依据：`intake.go:368-371`；`store/inbound.go:75-79, 108`。覆盖：`qq-intake/two-accounts-same-group-one-event-two-observers.json`。
- **I-27 `OnStored` 什么时候通知后面的步骤（闸门、拦截链）。** 只在"这个角色这一次看到了新东西"时：新存的事件通知；重复到达的，只有该角色**第一次**看到这条事件才通知；同一个号把同一条再交一遍（重连后重推、补录与实时重叠）**不**通知。陌生人、自检这类不记看到了的，只有新存的那一份通知。补录来的不进拦截链、不触发回复（由后面的步骤看 `backfill` 标签）。依据：`intake.go:323-326, 397-401`。覆盖：Go 测试 `TestRedeliveryOnlyNotifiesWhatIsNewToTheCharacter`（`intake_test.go:1180`）；共用场景**未覆盖**（场景不比通知）。
- **I-28 出意外不能卡队列。** 准入、入库后的步骤 panic 都接住，凭据（Ticket）一定有结果，队列照走；入库失败只记日志（只写会话和号），这一条等以后补录再取。依据：`intake.go:284-293, 314-318`。覆盖：Go 测试 `TestOnStoredPanicIsContained`、`TestStoreFailureDoesNotStickQueue`（`intake_test.go:674, 647`）。
- **I-29 关闭。** 关闭后不再收（返回关闭错误，什么也不写）；正在入库的做完；排着没开始的丢掉，它们的 Ticket 得到关闭错误，靠补录取回。依据：`intake.go:505-513`。覆盖：Go 测试 `TestCloseDropsQueuedAndRefusesLate`（`intake_test.go:578`）。

### 2.7 形状 × 入口

高风险部分（谁说的、谁冒充谁）按"形状 × 入口"列一遍。入口四个：实时帧、补录的历史页、回声认领、通知。格里写"处理了（怎么处理）/ 不可能 / 测了（哪个）/ 未覆盖"。

| 形状 | 实时帧 | 补录的历史页 | 回声认领 | 通知 |
|---|---|---|---|---|
| 发言人号缺、空、`0`、`-0` | 处理了：归一成空，入库检查拒收（I-2、I-8.2）；测了 `qq-frames/hostile-and-malformed.json` | 处理了：无合法发送者丢弃该条（B-8） | 不可能：没有号的回声不认（S-14） | 处理了：做事的人空则按会话对方认（N-4）；准入一侧测了 |
| 发言人号 = 她的登录号 | 处理了：算 `self`（I-3）；测了 `qq-frames/messages-self.json` | 处理了：同实时，自己的消息照收（`backfill/private-self-messages-kept.json`） | 就是回声本身：能认就认（S-14） | 处理了：角色号做的通知不存（N-3） |
| 发言人号是另一个角色的号 | 处理了：`character`、不是 `human`（I-20）；测了 `qq-intake/actors-and-origins.json` | 处理了：同实时，Origin 走补录分支（I-21） | 处理了：对任何账号交来的都问（I-24） | 处理了：同样不存（N-3） |
| 群友名片/昵称写成"主人" | 处理了：只认号（I-20） | 同实时 | 不可能：回声只比文字 | 同实时 |
| 登录号不在名册 | 处理了：整条拒收（I-8.3） | 处理了：补录目标就是本号 | 不可能 | 处理了：整条拒收 |
| 会话与登录号对不上（私聊串里的号 ≠ 交来的号） | 处理了：拒收（I-8.5） | 处理了：翻译后核对完整会话，不一致丢弃（B-8） | 不可能：键里含发送号和会话 | 未覆盖 |
| 群号不在白名单 | 处理了：`Drop`（I-12）；未覆盖 | 不可能：补录目标不含白名单外的群 | 处理了：准入先于回声认领 | 处理了：通知准入同消息 |
| 陌生人私聊 | 处理了：存、不记看到了、标 `stranger`（I-12）；未覆盖 | 同实时 | 不可能 | 处理了：照存不观察 |
| 平台消息号空、`0`、`00` | 处理了：不去重、单条入库（I-15）；测了 `qq-intake/platform-id-edge-cases.json` | 处理了：丢弃（历史要非零 int64） | 处理了：不认领 | 不可能：通知用自己的键 |
| 平台消息号 `-0`、`0.0`、`1e3`、小数 | 未覆盖：原样当键（存疑 Q3） | 处理了：丢弃 | 未覆盖 | 不可能 |
| 平台消息号 > 2^53 | 处理了：字符串往返（T-3）；测了 | 处理了：≤ int64 最大值才收，更大丢弃（存疑 Q2）；未覆盖 | 处理了：字符串比 | 不可能 |
| 平台消息号为负 | 处理了：保留负号；测了 `qq-frames/messages-basic.json` | 处理了：合法（`decimalID` 只拒 0） | 处理了：回执里的负号原样登记映射 | 不可能 |
| `time` 缺、0、负、毫秒量级、非数字 | 处理了：按缺失用收到时刻（I-7）；测了 | 处理了：丢弃该条，不以当前时间代填（B-8） | 不可能 | 同实时 |
| `time` 带小数秒 | 处理了：保留到毫秒（T-1）；未覆盖 | 处理了：整数秒才收 | 不可能 | 键用秒级时间 |
| `message` 不是段数组（字符串、null、对象） | 处理了：降成一段文字或空（I-6）；测了 `qq-frames/segments.json` | 共用段翻译，同实时 | 回声只在有段时比文字 | 不可能 |
| 同一条重复到达（同号重推、两个号、补录与实时重叠） | 处理了：只补看到了（I-16、I-17）；测了 | 同左 | 处理了：已认识的号不认领（S-14） | 处理了：通知键相同则只存一份（N-2）；未覆盖 |
| 实时与补录同时到同一会话 | 处理了：同一条队列串行（I-10、B-1） | 同左；测了 `backfill/group-live-arrives-during-backfill.json` | 不可能 | 处理了：同一条队列 |

## 三、通知（戳一戳、撤回、表情回应、被踢）

通知不是发言，规则和消息不同，影子跑时单独比（第八节），所以这里单列。

- **N-1 通知存成一行事件，`kind = notice`，不是 `message`。** 不进给模型看的历史。依据：`channel/intake_notice.go:3-15, 154-190`。覆盖：`qq-frames/notices.json`（只覆盖翻译）；入库一侧 Go 测试 `TestPipelineNoticeAdmission`、`TestPipelineNoticeValidation`（`channel/pipeline_test.go:564, 602`），共用场景**未覆盖**。
- **N-2 通知的去重键 ≠ 消息的键。** 通知没有平台消息号，键是 `notice:<种类>:<哈希前 12 字节的十六进制>`；哈希输入是（种类、做事的人、对象、被指向的消息号、表情、**秒级**时间、原始数据），以 `\x00` 分隔后取 sha256。放在同一张 `channel_msgs` 里，前缀保证撞不上数字串的消息号。代价是同一个人同一秒内戳同一个人两下只存一行。依据：`intake_notice.go:209-215`。覆盖：**未覆盖**（共用场景没有入库后的通知；我没有找到专门检查键格式和同秒合并的 Go 测试）。
- **N-3 角色自己的号做的通知不存**（她经出口戳出去的，计划表里已经有）；被踢没有会话也不存。依据：`intake_notice.go:108-130`。覆盖：Go 测试 `TestPipelineDoesNotStoreNoticesFromCharacterAccounts`（`pipeline_test.go:548`）；被踢不存见 `TestKickedNoticeWithSameNumberInTwoChannels`（`pipeline_test.go:590`）。
- **N-4 做事的人没说是谁**（协议端没给或给 0）：`ActorKind = human`、`ActorID = "<渠道>:"`（冒号后空）、`Origin = peer`；准入时按会话对方认。依据：`intake_notice.go:167-170`；`app/channel.go:550-566`。覆盖：准入一侧 Go 测试 `TestAdmitNoticeUnknownActorUsesPeer`（`app/admit_notice_test.go:13`）；入库写 `ActorID` 的那一行**未覆盖**。
- **N-5 撤回、表情回应指向的那条**：库里有就连上 `reply_to`，没有空着；这个"连"发生在通知入库时，不事后补。依据：`intake_notice.go:11-12, 128-140`。覆盖：**未覆盖**（我没有找到检查 `reply_to` 连接的 Go 测试）。

## 四、补录与入库一站的接缝

自动补录一轮怎么扫、怎么判覆盖、缺口和水位怎么变，**全部照** [规格-补录覆盖](规格-补录覆盖.md) 第二至六节（ADR-275、278 的决定条目也在那里落地，本文不复述）。下面只写补录**提交消息**时和入库一站的交互。

- **B-1 补录用同一个入库。** 补录来的消息走 `Intake.Submit(ctx, in, {Backfill: true})`，和实时共用准入、回声认领、去重、观察、同一条会话队列；因此同一会话里实时的和补录的一个接一个，水位才能按顺序推进。依据：`intake.go:97-100, 212-241`；`app/backfill.go:577`。覆盖：Go 测试 `TestBackfillAndLiveShareOneLane`（`intake_test.go:538`）；`backfill/group-live-arrives-during-backfill.json`。
- **B-2 补录标记由补录一方打。** 适配器不知道自己交的是不是补录。`Backfill` 影响三件事：事件标签加 `backfill`（I-23）；Origin 判定走"补录来的"分支（I-21）；回声认领对"早于现在 5 分钟"的消息不问（B-4）。依据：`intake.go:305-307, 345-347, 419-428`。覆盖：`qq-intake/self-message-origin-by-takeover-time.json`；回声那条见 B-4。
- **B-3 补录绑定连接寿命。** 提交时若 `ctx` 已取消，这条在队列里轮到时直接作废（凭据得到 ctx 错误），不写库。依据：`intake.go:224-231`。覆盖：`backfill/private-cancel-keeps-watermark.json`（取消不推进水位）；"排队期间断线的旧消息不在新连接里迟到写库"这一条**未覆盖**。
- **B-4 补录来的角色号消息，早于"现在 − 5 分钟"的不去认领回声**（老历史不能冒认当前发送的回声）；晚于的仍按出口自己的回声窗口处理。依据：`intake.go:305-311`。覆盖：**未覆盖**（`qq-send` 和 `qq-intake` 都没有这个边界；回声窗口本身见 S-14）。
- **B-5 补录的每条都等入库结果。** 补录按时间从早到晚逐条 `Submit` 再 `Wait`；结果 `Dropped`（准入说不收）整轮按失败处理；`Duplicate` 只计数。所以补录目标会话必须是允许的会话（群在白名单里、私聊限本号），否则整轮失败。依据：`app/backfill.go:575-596`。覆盖：Go 测试 `backfill_*_test.go`；`backfill/` 场景里 `stored[]` 隐含此行为。
- **B-6 补录不推水位的情形。** 逐条已入库的消息保留；但**取消、出错的一轮不结算，水位不动**，已登记缺口的则照常推水位。实时消息入库**不**动水位。依据：[规格-补录覆盖](规格-补录覆盖.md) 第二节第 6 条、第六节。覆盖：`backfill/private-cancel-keeps-watermark.json`、`backfill/group-call-failed-third.json`。
- **B-7 何时触发（接缝以外，只列要点）。** 连上并就绪后稳定 30 秒才开始第一轮；重连后 10 分钟内没有待扫描缺口则延后；之后每分钟在线推进检查位置；到期缺口（距上次尝试 ≥ 10 分钟）触发再扫。依据：`app/backfill.go:17-24, 131-199`。覆盖：**共用场景未覆盖**（定时器，规格-补录覆盖第七节已列，Go 测试 `TestBackfillDebounceWatermark`）。
- **B-8 历史页的翻译比实时严。** 历史消息要求：平台消息号是非零的 int64 十进制整数；`time` 是整数秒且小于 1e11；会话和发言人必须与请求的会话/号核对；不合格的**丢弃该条**（页返回"部分翻译"，覆盖证明失效）。实时则不要求这些（I-5、T-1）。依据：`channel/snowluma/query.go:341-405`；`call.go:626-635`。覆盖：Go 测试 `snowluma/history_test.go` 等；共用场景**未覆盖**。这是实时与补录的已知差异，见存疑 Q2。

## 五、发送：计划、分段、确认、未知、回声

本节只写 QQ 私聊、QQ 群的文字类发送；戳一戳、回应、转发的差异在各条里标明。回包怎么判的细节在 [规格-QQ发送上线](规格-QQ发送上线.md) 3.2，这里写结论。

### 5.1 计划与分段

- **S-1 先落库后发送。** 一次"她说话"产生一个计划（plan）：计划行、事件行、各段（part）行在一个事务里写好，再排进该会话的出站队列；崩溃后留下的是"发送中"，不是"没记录"。依据：`channel/outlet.go:490-640`；`store/outbound.go:111`。覆盖：Go 测试 `TestSendHeldWritesFirstAndSendsOnlyAfterStart`（`outlet_test.go:1068`）；`qq-send/` 场景的 `plans[]` 比计划状态。
- **S-2 定哪个号发。** 私聊：用会话串里的登录号，且该号必须绑给说话的角色（角色不能用别人的号发私聊）；群：这个角色在该渠道**绑的第一个号**。依据：`outlet.go:285-310`。覆盖：Go 测试 `TestAccountRouting`（`outlet_test.go:771`）；共用场景**未覆盖**。
- **S-3 档位。** 说话（`speak`）真发；试运行（`dry-run`）写库、事件 Origin 记 `automation:dry-run`、不发；其余（含不认识的值、钩子 panic）按只记录，出口不收（返回错误）。每次收到要发的、每一段发之前（含停顿之后）都问一次，切档后没发的段中止。依据：`outlet.go:42-47, 264-275, 560-570`；`outlet_run.go:80-94, 106-115`。覆盖：Go 测试 `TestDryRunWritesButNeverSends`、`TestRecordOnlyModeRefuses`、`TestSwitchModeDuringPause`（`outlet_test.go:838, 890, 915`）。
- **S-4 发送检查与守卫。** 说话档在落库前和每次真正调用 `Send` 之前各过一次发送检查（通道门、风控间隔）；没过：这一批不进队列、不写库（落库前），或把没发的段中止（调用前）。守卫（记忆可见性）对 `character` 来源和引用/转发都生效，只有登记过的插件来源可以豁免。依据：`outlet.go:569-580`；`outlet_run.go:168-176`。覆盖：Go 测试 `TestSendHeldCheckFailureIsRecognizable`（`outlet_test.go:1165`）；共用场景**未覆盖**。
- **S-5 分段。** 一次回复先由回合裁决按标点拆成几条文字段（`split.go`，参数不许凭感觉改，Bun 与 Go 的拆法有逐条对拍 `testdata/asent.json`）；库里的 `events.text` 是各段文字用**空行**（`\n\n`）连起来，界面分气泡按 `segments_json` 里的 `parts`。依据：`outlet.go:370, 373-395, 619-631`；`split.go:1-8`。覆盖：Go 测试 `TestEventTextOfParts`（`outlet_test.go:1048`）；拆条对拍不属于共用场景（`asent.json`）。
- **S-6 段间停顿。** 第二段起的**文字段**，发之前停 `min(2500ms, 字数 × 40ms)`，从上一段进入终态算起；第一段和非文字段不停。字数按 rune。停顿值也存进库（`pause_ms`，取整毫秒）。依据：`outlet.go:115-122, 406-411`；`outlet_run.go:84-94`。覆盖：Go 测试 `TestPauseCountsFromPreviousTerminalState`（`outlet_test.go:549`）；共用场景用假钟，**未覆盖**停顿数值。
- **S-7 同一会话的出站是一条队列。** 段与段之间不插别的计划的段；不同会话互不等待。依据：`outlet.go:161, 679`（`lanes` 按渠道+会话排队）。覆盖：Go 测试 `TestSameConversationSerialDifferentConversationsParallel`（`outlet_test.go:574`）。

### 5.2 回执：确认、明确失败、结果未知

一段的状态：`pending → sending → confirmed / retrying / unknown / failed / aborted`；计划状态：`sending / done / unknown / failed`。每一步转移都是库里的条件更新，谁先到谁算数，后到的转不动就去看现在是什么状态。依据：`outlet_run.go:1-8, 274-288`。覆盖：`qq-send/` 场景逐字比段状态。

- **S-8 一次 Send 的结果只有三种。** 确认、明确失败、结果未知。适配器判法（以回包 JSON 为准）依据 `snowluma/call.go:499-562`；覆盖：Go 测试 `snowluma/send_test.go`、`verify_test.go`，共用场景**未覆盖**这一层（`qq-send/README` 的"没覆盖"一节已写明）。

  | 回包情形 | 结果 |
  |---|---|
  | `status` 为 `failed`，且措辞表示"没拿到送达回执 / 超时 / no response" | **未知**（QQ 往往已收，不重试、不换形式补发） |
  | `status` 为 `failed`，其他措辞 | **明确失败**；措辞有"审核、风控、体积、不支持"一类 → 标"协议端拒绝"（不重试） |
  | `retcode` 不是 JSON 整数 `0`（缺、null、`"0"`、布尔、非 0） | **未知** |
  | `retcode` 为 `0`，文字/转发的 `data.message_id` 非零整数（整数或十进制字符串，负数合法）或戳一戳/回应 | **确认**（戳一戳、回应的平台消息号留空，不登记映射，即使带了号也不用） |
  | `retcode` 为 `0`，文字/转发没有可用的消息号 | **未知** |
  | 没写出去（没连上、取消、号不对） | 没连上/没写出去/取消 → **明确失败**；登录号核验不过、通道不健康 → **未知** |
  | 写出去之后连接断了、等回包取消 | **未知** |
- **S-9 确认。** 段转 `confirmed`，`via = reply`；回执带的平台消息号在同一个事务里登记进映射，指向这次回复的事件（只在号可用时写）；这是发送侧给"回声认领"留的防重复依据（S-12）。依据：`outlet_run.go:200-207, 279-282`；`store/outbound.go:250-295`。覆盖：`qq-send/` 场景 `final.mapped`。
- **S-10 结果未知：不重发。** 段转 `unknown`，计划记未知，后面还没发的段全部 `aborted`。协议端可能已经发出去了，重发就是发两遍。依据：`outlet_run.go:208-215, 95-99`。覆盖：`qq-send/echo-claims-unknown-part-within-window.json`；Go 测试 `TestRejectionAndUnknownAreNeverRetried`（`outlet_test.go:1178`）。
- **S-11 明确失败：最多发 3 次，隔 1 秒；协议端拒绝、适配器说不支持的不重试直接失败。** 用完重试或属于不重试情形：段转 `failed`，后面的段 `aborted`，通知通道门这一段没发出去。重试等待期里回声来了：认领成确认，重试作废，后面的段接着发。依据：`outlet.go:71`；`outlet_run.go:218-243`。覆盖：`qq-send/echo-during-retry-wait-cancels-retry.json`；Go 测试 `TestRetryOnExplicitFailure`、`TestFailedPartAbortsTheRest`（`outlet_test.go:443, 498`）。
- **S-12 一次调用最多等多久。** 适配器自己等 10 秒（从调用开始算，含等核验和写入；到点能取消卡住的写）；出口另有 30 秒兜底，到点按结果未知办。适配器必须比出口短。依据：`snowluma/call.go:23, 98-106`；`outlet.go:125`。覆盖：Go 测试 `TestSendTimeoutIsUnknown`（`outlet_test.go:614`）。
- **S-13 启动与关闭。** 启动时对每个平台库扫一遍：发送中的段转"结果未知"（不重发），待发和等重试的转"已中止"（不续发几分钟前的话）。关闭时先不收新的，等在发的那段回执最多 5 秒，之后封库，封库以后迟到的回执、回声、计时器什么也不写。依据：`outlet.go:211-232, 767-830`。覆盖：Go 测试 `outlet_close_test.go`；共用场景**未覆盖**。

### 5.3 回声认领

协议端会把我们自己发出去的消息推回来；不认领的话入库一站会把它存成"她的号上别人发的"（Origin = human），历史里多一条重复。

- **S-14 认领的条件。** 依据：`outlet_echo.go:1-107`；`store/outbound.go:326-410`。覆盖：`qq-send/` 全部 9 个场景（见下）。
  - 回声要有可用的平台消息号，且这个号在（渠道、会话）里**还没有映射**（已认识的号一律不认领：同一个回声再到、同群双号各交一份、回包已确认段的回声，都交给入库去重，只补看到了）；
  - 候选段：同一个发送号、同一个会话、同一种类（文字或转发）、状态是发送中/等重试/结果未知，且在这一段第一次调 Send 起 **5 分钟内（含边界；多 1 毫秒不认）**；已确认、已失败、已中止的段不认；
  - 文字段比文字：去首尾空白、换行统一成 `\n`，文字为空不认；转发段不比节点正文，但含图片/视频的不认；带图片、表情、视频的文字回声一律不认；
  - 每个回声只认一段、每段只认一次，候选里**发得最早的先认**（对得上多段时先按优先级挑：md5 强对上、有人在等、没人等过、等过已放弃，见规格-链接发送改版 A1）；键里有发送号，同群两个号各发一段同样的字各认各的；
  - 认领成功：段转 `confirmed`，`via = echo`，回声的号登记进映射，重试作废，迟到的回包不再改它。
- **S-15 认领发生在入库一站里，位置在准入之后、入库之前。** 认领出错只记日志，这条按普通消息存。依据：`intake.go:305-311`。覆盖：Go 测试 `TestEchoClaimedBeforeIntakeStoresIt`、`TestWithoutEchoClaimTheEchoIsStoredAsHuman`（`outlet_echo_test.go:272, 340`）；共用场景**未覆盖**这一串联（`qq-send/README` 已注明）。
- **S-16 残余风险（已知，不当结论）。** 主人拿着号手动发同样的字，或转发在途时手动转发，只靠段类型、文字和窗口区分不开；协议端没有别的可比的键。依据：`outlet_echo.go:18-19`。覆盖：不可测。

## 六、时间、长整数、时区

- **T-1 平台时间是整秒；实时帧里的小数秒会被保留。** 实时 `time` 的小数部分四舍五入到纳秒保留，入库取毫秒，所以实时消息的 `created_at` 可能不是 1000 的整数倍；补录来的历史页只收整数秒。依据：`snowluma.go:420-433`；`query.go:372-377`；`intake.go:334-336`。覆盖：`qq-frames/` 比 `time_unix`（秒）；毫秒小数部分**未覆盖**。
- **T-2 入库时间的取法。** 有平台时间且大于 0 用平台时间；否则用收到这一刻（`Now`）。库里存的是 Unix 毫秒（UTC 纪元，不带时区）。同一秒内的多条靠平台消息号定先后，到达先后是库里的 `seq`，两者不同。依据：`intake.go:333-336`；`types/event.go:31`。覆盖：`qq-intake/` 的 `time_s` 比时间；"用收到的时间"见 Go 测试 `TestZeroTimeUsesNow`。
- **T-3 平台消息号永远按字符串存、按字符串比，不转数字。** 超过 2^53 的号往返不变，相邻两个号不混；可以为负。依据：`store/inbound.go:21`；`snowluma.go:113-123`。覆盖：`qq-intake/platform-id-edge-cases.json`（2^53 与 2^53 加 1 是两条）、`qq-frames/messages-basic.json`。
- **T-4 群历史的协议序号 `message_seq`** 是正 int64，只在补录里用来判覆盖，实时消息不带，群历史之外（私聊）永远不用。依据：`query.go:393-402`。覆盖：`backfill/group-seq-*.json`。
- **T-5 时区。** 入库的所有时间戳是 UTC 纪元毫秒，不受时区影响。只有 `segments.line` 里排给模型看的那个 `[时间]` 前缀带时区：用配置的 `Location`，没配用**本机时区**（`time.Local`）。因此影子跑时 Go 和 Bun 所在机器的时区必须相同，否则 `line` 必然不同（第八节不比 `line` 的原因之一）。依据：`channel/line.go:50-59`。覆盖：Go 测试 `TestStoredLineByScene`（`intake_line_test.go:85`）。
- **T-6 补录起点重叠 60 秒、重连防抖 30 秒、重试间隔 10 分钟等毫秒级参数** 全部在 [规格-补录覆盖](规格-补录覆盖.md) 和 [规格-QQ收图下载和断线补录](规格-QQ收图下载和断线补录.md) 5.3，本文不重复。

## 七、和已有规格的冲突 / 不一致

没有改任何旧规格，只列出来，主人定了再改。

1. **会话串写法。** [规格-QQ渠道](规格-QQ渠道.md) 第五节写私聊会话是 `qq:private:<号>`；Go 实际是 `qq:private:<登录号>:<对方号>`（`types/conversation.go:38`），[规格-补录覆盖] 和 [规格-QQ发送上线] 用的也是后者。以 Go 为准，建议改旧规格。
2. **`outage`、`unverified-head` 缺口原因。** [规格-补录覆盖](规格-补录覆盖.md) 第一节把 `outage` 写成"断线登记的空档"，但 Go 非测试代码里没有任何地方写入这两个原因，只有 `ctlserver/backfill.go:72` 一个显示用的 `switch` 认识它们；[规格-QQ收图下载和断线补录](规格-QQ收图下载和断线补录.md) 5.1 的缺口表说它们是"旧值保留可读"。**补录覆盖的名词表比代码多出一条**，Bun 实现不应生成这两个原因（存疑 Q1）。
3. **影子跑时长。** [方案-后台换Bun加pi](notes/方案-后台换Bun加pi-2026-11-10.md) 写"一周无差异再接发送"，用户 2026-11-11 改定为两天（第八节采用两天）。
4. **私聊的 `Latest`。** [规格-补录覆盖](规格-补录覆盖.md) 第四节说 `Latest` 看首页；适配器对**私聊页恒置 `Latest = true`、群页恒为假**，`Continuous` 恒假（`query.go:357`）。结论（私聊现实里拿不到覆盖证据）不变，但写成"私聊 Latest 恒真"更准确。
5. **补录对记忆的影响。** [规格-QQ渠道](规格-QQ渠道.md) 状态标签表已被 ADR-092、101 改过（补录不进记忆整理，学说法吃补录）；本文只保证 `backfill` 标签正确存在（I-23），过滤归消费者，不在入库一站做。

## 八、影子跑怎么比

前提：[方案](notes/方案-后台换Bun加pi-2026-11-10.md) 定 QQ 这一批先**只读影子**，Bun 与 Go 同时从同一个 SnowLuma 收（不发）；Bun 的补录只做历史**查询**，写自己的影子库，不写 Go 的库。比的是两边各自的库，不是内存里的东西。发送侧怎么比见 8.4，需要主人定（存疑 Q7）。

### 8.1 比哪些

按"表 → 对齐键 → 比的列"，列名以 Go 的库为准：

| 表 | 对齐键 | 比的列 | 不比 |
|---|---|---|---|
| `events`（`kind = message`） | （会话串，平台消息号），经 `channel_msgs`；无号的见 8.2 | 会话、`actor_kind`、`actor_id`、`origin`、`kind`、`text`、`created_at`、`segments_json.tags`（集合比，不比顺序）、`segments_json.segs` 的段内容、`from`（号、昵称、群名片） | `seq`、`id`、`segments_json.line`（依赖名字缓存和本机时区，单列软比，见 8.3） |
| `events`（`kind = notice`） | 通知键（`notice:` 开头，N-2） | 种类、做事的人、对象、被指向的消息号、`text`、秒级时间、`origin` | 同上 |
| `channel_msgs` | （渠道，会话，平台消息号） | 键是否存在、指向的事件按对齐键换算后是否同一条 | `event_seq` 原值 |
| `observations` | （事件对齐键，角色） | 集合是否相等 | 记录先后 |
| `events.reply_to` | 事件对齐键 | 指向的事件按对齐键换算后是否同一条（空也算一个值） | 原值 |
| `channel_gaps` | （会话，`from_ms`，`to_ms`） | `state`、`reason`、`acknowledged` 是否非零 | `id`、`tries`、时间戳 |
| `qq_sync`、`qq_backfill_continuity` | 账号 / （账号，会话） | `through` 的毫秒值，允许差不超过一个补录周期内的时钟差（见 8.3） | 更新时间 |
| 日志里"没有平台消息号"一行 | 会话 | 出现次数（I-15） | 其余日志 |

### 8.2 对齐的特殊情形

- 没有平台消息号的消息（I-15）：按（会话，`created_at`，`actor_id`，`text` 的哈希）作多重集对齐；同一秒内同人同文的两条算两条，按到达先后一一对上。
- `created_at` 来自"收到的时刻"的行（帧里没有时间，I-7）：两边各自的收到时刻不同，**这类行不比 `created_at`**，只比其余列；用适配器日志里"缺时间"的帧计数核对两边条数相同。
- 同一条消息两边都有，但一边是补录先到、一边是实时先到：先到的算数（I-17），所以 `created_at` 和 `tags`（有没有 `backfill`）可能不同。这是**预期差异**，不计入差异，但要在报告里单列"到达顺序不同的条数"并抽查几条确认符合 I-17。

### 8.3 什么算差异

- **差异（必须解决）**：8.1 里"比的列"任一不同；一边有事件一边没有（排除 8.2 第二条）；一边的重复到达多存了事件（违反 I-16）；一边的 `observations` 比另一边多或少；缺口的 `state/reason` 不同（Bun 生成了 `outage`、`unverified-head` 之类 Go 不生成的原因也算）。
- **不算差异**：`seq`/`id`/计划 id 的原值；日志措辞；`line` 的时间前缀只要时区一致就应相同，**不一致**则只当提示（名字缓存导致的名字差异也只当提示），不阻塞通过；`qq_sync`/连续证明的 `through` 在一个轮次的时钟误差内（建议 ≤ 2 分钟）；影子库里 Bun 比 Go 晚收到、晚写入的最后几条（以两边都停止写入后的快照比较）。
- **谁对按本文判**：每个差异记录对应的规则编号（例如 I-21）。本文对、Go 错 → 修 Go（并补测试，必要时加共用场景）；本文对、Bun 错 → 修 Bun；两边一致但都与本文不符 → 本文有误，改本文并请主人确认；本文没写 → 记入第九节"存疑"，不由任何一边的实现当结论。
- 比对要有工具，不靠肉眼：把 8.1 的列导出成两份规范化 JSON（按对齐键排序、剔除不比的列、毫秒值按整数、号一律字符串）再 `diff`，输出差异时对号和昵称打码，不把消息原文写进报告。

### 8.4 发送侧（待主人定）

影子期间只能有一边真发，Bun 若只记计划不调 `Send`，则发送侧能比的是：同一个触发下计划的**内容**（段数、各段文字、目标会话、选的号）和**回声认领结果**（Bun 对 Go 实际发出去的那几条回声，是否认出"这是我们发的"并和 Go 一样不另存一条 `origin = human`），不是回执。回执分类（S-8）靠 `snowluma/send_test.go` 一类的离线用例对拍，不进影子库比较。

### 8.5 通过标准

全部满足才算过（用户 2026-11-11 定：两天）：

1. 连续跑满 48 小时，两边都在线收消息，期间 8.1 全部表快照比对没有"必须解决"的差异。
2. 期间**至少发生一次断线重连**（可以人为断一次 SnowLuma 的 WebSocket），重连后两边都完成一次自动补录一轮，且这一轮里：至少一个会话**补进了新消息**，至少一个会话**全是重复**（验证 I-16）；`channel_gaps` 的变化两边一致。
3. 期间至少出现一次**实时与补录交错**（补录进行中有实时消息到同一个会话）并核对 I-17。
4. 两个号同时在线的群里至少有一条被两边都记成"两个角色各一次看到了"（I-26）；如果这两天恰好没有，用共用场景补跑，不算通过依据。
5. 第九节存疑里凡标"影子跑前必须定"的，已有主人的回答并写入本文。
6. 报告写在 `data/reports/`，列：比对时间范围、各表条数、差异清单（规则号 + 判定 + 处理）、到达顺序不同的条数。

## 九、存疑（写不清或 Go 行为像 bug）

- **Q1 `outage`、`unverified-head` 两个缺口原因没人写。** Go 代码里只有显示用 `switch` 认识它们（`ctlserver/backfill.go:72`），[规格-补录覆盖] 却把 `outage` 写成"断线登记的空档"。是规格多写了，还是"登记断线空档"这个功能没做？影响：Bun 要不要实现它们。**影子跑前必须定。**
- **Q2 实时和补录对平台消息号 / 时间的接受范围不同。** 实时：消息号原样接受任意字符串，`time` 缺失用收到时刻、允许小数秒、允许超出 int64 的号（共用场景钉着 > 2^53 往返）；补录：消息号必须是非零 int64 十进制整数，时间必须是整数秒，否则整条丢弃（`query.go:341-405`）。同一条消息实时能存、补录会丢，后果是补录对号超出 int64 的消息不能补。QQ 的消息号现实中是否会超过 int64？是否要统一？**影子跑前必须定**（影子跑会比出这个差异）。
- **Q3 `HasPlatformID` 只挡"全是 0"，不挡 `-0`、`0.0`、`0e0`。** `store/inbound.go:14-16` 用"去掉前导 0 后非空"判断，所以 `-0` 会被当成可用的键；而 `snowluma.go:128-135` 的零值归一只用在号码上、不用在消息号上，历史页的 `decimalID` 又会拒绝 0。当前没有场景覆盖这几种写法，是否要统一成"数值为 0 就不能做键"？
- **Q4 `originOf` 的接管时刻是启发式，没有验证。** "第一条非补录事件的时间"在如下情形会偏：重启后库里的第一条实时事件是补录之前的旧数据；一个新绑上的号先靠补录进库。代码注释自己写了"照搬，不作为结论"（`intake.go:411`）。要不要换成明确记录的"接管时刻"（例如首次连上并就绪的时刻）？**影响 `origin` 列，影子跑会比到。**
- **Q5 补录绕过回声认领的 5 分钟界限靠 `now()`。** 补录来的角色号消息只要时间晚于"现在 − 5 分钟"就会去问回声认领；如果补录发生在重连后很晚，刚发出但未确认的计划段可能已被清理（S-13 启动时转成未知/中止），此时回声不会被认领，会存成 `origin = human`。这个边界没有场景（B-4）。是否接受？
- **Q6 通知里 `Actor` 为空时的 `ActorID` 是 `"<渠道>:"`（冒号后空）**（`intake_notice.go:167`），不是合法的号，也不是规格里 `渠道:号` 的形状。是有意的占位还是该改成别的？
- **Q7 影子期间谁发。** 方案写"先只读影子"；若 Bun 只记计划不发，8.4 就只能比计划内容和回声认领，回执分类、重试、未知都比不到。要不要在影子期间让 Bun 在一个测试群里真发（经主人点头，用 Go 同样的守卫），以覆盖发送侧？这需要主人定，不能由我定。
- **Q8 `events.text` 和 `segments.line` 的名字。** 昵称/群名片随时变，`line` 里存的是发言当时的名字；Bun 没有同一份名字缓存时 `line` 必然不同。是否接受影子跑不比 `line`、只比段内容？（8.1 暂按"不比"写。）
- **Q9 `TagFromSelf` 的判定。** 标签 `fromSelf` 在 `kind = self` 或发言人号等于收到它的号时加（`intake.go:348`）；角色 B 的号发的、被 A 的号看到的消息不加此标签（发言人号 ≠ A 的号），尽管 `actor_kind` 是 `character`。后续消费者按哪个口径过滤，规格没写。
- **Q10 补录里 `Dropped` 整轮失败。** 补录提交遇到准入说不收（`Dropped`），整轮失败不结算（`app/backfill.go:587`）。如果一次中途改配置把某个群移出白名单，下一轮会因目标集合变化而不再扫它，所以现实中少见；但这是否应该"跳过这条继续"而非"整轮失败"？


### 9.1 主会话定的（2026-11-11）

- Q1：`outage`、`unverified-head` 标“没实现”，Bun 也不做；以后真要登记断线空档另写规格。影子跑不比这两种原因。
- Q2：不统一。补录要拿消息号当 int64 游标翻页、回执也用同一判断，真实 QQ 消息号是整数、时间是整数秒，两边在真实数据上都满足。Bun 照抄 Go：实时宽、补录严。
- Q3：照 Go 现状（只挡全 0），真实数据碰不到；不改。
- Q4：影子跑期间两边用同一套启发式（Bun 照抄），影子跑通过后再改成明确记录接管时刻（另开待办）。
- Q5、Q6、Q9、Q10：照 Go 现状，Bun 照抄；影子跑比出差异再按规格回头定。
- Q8：影子跑不比 `segments.line`，只比段内容（8.1 照此）。
- Q7：主人 2026-11-11 同意：影子跑期间让 Bun 在**内部测试默认群**（群号只在本机配置和记忆里，不进 git）真发少量消息，覆盖发送回执三分类、重试、结果未知和回声认领；一次只发几条，发前在交接里写明发什么，其余群照旧只记计划不发。
## 十、覆盖统计

规则共 64 条：I-1～I-29（29）、N-1～N-5（5）、B-1～B-8（8）、S-1～S-16（16）、T-1～T-6（6）。按有没有测试分：

| 类别 | 条数 | 规则 |
|---|---|---|
| 有共用场景（Go 和以后的 Bun 都能对着跑） | 26 | I-1～I-7、I-14～I-17、I-20、I-21、I-24、I-26；B-1、B-2、B-6；S-1、S-9、S-10、S-11、S-14；T-2、T-3、T-4 |
| 没有共用场景，只有 Go 测试（Bun 重写时要对着 Go 测试单独验，规格-补录覆盖第七节同理） | 28 | I-8～I-11、I-13、I-18、I-19、I-22、I-25、I-27～I-29；N-1、N-3；B-5、B-7、B-8；S-2～S-8、S-12、S-13、S-15；T-5 |
| **完全未覆盖**（没有找到任何测试） | 4 | I-23（标签内容）、N-2（通知键）、N-5（通知连 `reply_to`）、B-4（补录的回声 5 分钟界限） |
| **部分未覆盖**（规则的一部分没有测试） | 4 | I-12（群白名单 `Drop`、陌生人 `NoObserve` 两条）、N-4（入库时写 `ActorID`）、B-3（排队期间断线的旧补录不写库）、T-1（实时小数秒进毫秒） |
| 不可测 | 1 | S-16（主人手发同样的字，协议端无区别） |
| 指向别处 | 1 | T-6 |

"未覆盖"共 **8 条规则**（4 完全 + 4 部分），另 S-16 不可测。这 8 条建议在第 4 批动手之前先补共用场景，否则影子跑里出现的差异无处对。
