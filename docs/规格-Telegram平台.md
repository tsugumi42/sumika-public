# 规格：Telegram 平台（Bot API 长轮询）

> 状态：候选，未排期（2026-11-16 整理）。
> 2026-11-06 状态：文档草案，已合 main（2026-11-06，分支 docs/spec-telegram）；未实现、未创建 Bot、未连接 Telegram、未进行收发或性能验收。接替 2026-11-03 的 Telegram 渠道草稿；工程取舍标为“我定的”，开放范围见第十二节，排期只看[待办](待办.md)。本任务不改代码、ADR、真实配置或数据。
>
> 参考与借用：
> - 缺口盘点-平台中间件（私有存档）：借持久确认、编辑、主题、账号共享限速和文件凭据清单；更新为 ADR-274 的只追加记录和 ADR-277 的远程审批。
> - [渠道中间件](规格-渠道中间件.md)、[多平台装配](规格-多平台装配.md)、[聊天记录按平台分库](规格-聊天记录按平台分库.md)、[架构总纲](架构.md)：复用统一事件、观察、单 Hub、唯一出口和平台库，不把 QQ 当通用平台。
> - 主工作区收件箱 `docs/inbox/2026-11-05_平台层-Hermes Agent.md`、`2026-11-05_平台层-AstrBot.md`：作为源码导航；本分支未收录这两份材料。下文另列本次重新读取的固定源码，不把报告本身当实现证据。
> - [常见边界情况清单](notes/清单-常见边界情况-2026-10-26.md)：对照文本、生命周期、多账号、权限、预算、部分失败、乱序、外部协议和秘密泄漏几类；[性能与效率标准](notes/标准-性能与效率-2026-11-03.md)：借计时口径、资源归因、文件增长和预先冻结预算的方法。

## 一、目标、依据与源码取舍

一个角色用一个 Telegram Bot 收发私聊、普通群、超级群及 topics，原始聊天进独立的 `telegram.db`。接收、记录、角色观察、回复、执行授权分别判定；平台发来的编辑和回应变化也有可回放的记录。QQ、本地和 Telegram 的账号、游标与权限不串用。

本规格设计完整接入；本次交付仅文档。普通用户登录、MTProto/TDLib、webhook 服务、代建/托管 Bot、频道运营、Business、Guest、Inline mode、临时消息、Rich Messages、付费广播不在实现范围。官方现在已有 Rich Messages；本次选择普通消息与 caption，不把这项选择说成 Telegram 永远不能图文混排。普通 Bot 没有任意会话的历史分页，也没有任意 message ID 的读消息接口。

### 1.1 本次读码证据

浅克隆均在 `_external/sumika-telegram-*`，只读、未安装或运行上游。以下日期是提交/发布元数据，不是可用性或稳定性实测。外部实现只借结构，独立实现 Sumika 契约；AstrBot 不逐段翻译。

| 来源及快照 | 已读的实现入口 | 借用与理由 | 不照搬的边界 |
|---|---|---|---|
| [Hermes Agent，bd312c90](https://github.com/NousResearch/hermes-agent/tree/bd312c903ea42763a535a5ac034ef132fad1a5d2)，2026-11-05，MIT | `plugins/platforms/telegram/adapter.py` 的 `_start_polling_once`、`_cold_boot_drop_pending`、`send`、`_send_exec_approval_prompt`、`_claim_callback_state`、`_handle_exec_approval_callback`；同目录 `update_admission.py`、`telegram_entities.py`、`telegram_topic_routing.py` | 接收 Bot 与会话/角色分开；UTF-16；审批指向具体 request；重连就绪以实际 getUpdates 往返为准；逐 chat FIFO | receipt 文件是异步刷新且有保留窗口，不能当 Sumika 的事务确认；冷启动/409 有丢积压路径，不采用；不按授权白名单自动认执行审批者，不借“始终允许”按钮 |
| [AstrBot，86c24e84](https://github.com/AstrBotDevs/AstrBot/tree/86c24e846c0de78830f6a0bc402049edb0f0c671)，2026-11-05，AGPL-3.0-or-later（已核 `pyproject.toml:6` 与 LICENSE） | `astrbot/core/platform/sources/telegram/tg_adapter.py:543` 的 `convert_message`、`:462` 的 callback；`tg_event.py:327` 的 `send_with_client`、`:117` 的文本发送 | 有序组件按平台发送；topic 独立会话；caption 与 text 的实体分开；非文本引用仍保留来源 | `self_id` 取 username、`from_user` 没区分 sender_chat，不能借来认主人；转换只读 `update.message`，不能据此宣称支持编辑；组件逐条发图不满足本规格 caption 拼装；SDK 自动 offset 不等落库 |

固定源码：[Hermes adapter](https://github.com/NousResearch/hermes-agent/blob/bd312c903ea42763a535a5ac034ef132fad1a5d2/plugins/platforms/telegram/adapter.py)、[Hermes admission](https://github.com/NousResearch/hermes-agent/blob/bd312c903ea42763a535a5ac034ef132fad1a5d2/plugins/platforms/telegram/update_admission.py)、[AstrBot adapter](https://github.com/AstrBotDevs/AstrBot/blob/86c24e846c0de78830f6a0bc402049edb0f0c671/astrbot/core/platform/sources/telegram/tg_adapter.py)、[AstrBot send](https://github.com/AstrBotDevs/AstrBot/blob/86c24e846c0de78830f6a0bc402049edb0f0c671/astrbot/core/platform/sources/telegram/tg_event.py)。

### 1.2 Go 库比较与选择

2026-11-06 用 GitHub API 核对归档状态、最新 release、pushedAt，并读克隆中的实现和 LICENSE。三个仓库均未归档；“最近提交”取克隆 HEAD 的提交时间，不用 pushedAt 代替。

| 候选 | 维护证据与许可证 | 轮询 / webhook | 文件、媒体、topic | 对本任务的代价 |
|---|---|---|---|---|
| [mymmrac/telego](https://github.com/mymmrac/telego/tree/46e5618cd0b346917c043e81fb4e79f90b1c3f6a) | HEAD 2026-11-02；release v1.12.1，2026-08-29；MIT | 公开 `GetUpdates(ctx, params)`；也有自动轮询和 webhook | `methods.go`、`types.go` 有文件、媒体组、`MessageThreadID`、caption 位置、reaction；multipart 由 `io.Pipe` 流式构造 | 自带 fasthttp/JSON 等依赖较多；须显式选 net/http、禁自动重试和 SDK 日志 |
| [go-telegram/bot](https://github.com/go-telegram/bot/tree/5cc7db71a70a6528c7c171dfe3673122b5388c04) | HEAD 2026-11-02；release v1.27.0，2026-09-11；MIT | 有长轮询、webhook；当前轮询 `getUpdates` 与 `rawRequest` 均私有 | 有照片、文件、媒体组、forum 方法及 thread 参数 | 无第三方依赖，但轮询先推进内存 ID 再交队列；当前 `raw_request.go` 把上传整份缓冲，不能直接满足确认点和上传内存要求 |
| [tucnak/telebot](https://github.com/tucnak/telebot/tree/ee8708cd2a42cd51ed457bcf6fdaf871e73b17b7) | HEAD 2026-06-16 UTC；GitHub 最新 release v3.3.6，2024-06-10；源码 LICENSE 是 MIT，GitHub 分类为 Other | `LongPoller`、webhook；支持自定义 Poller | `media.go`、`file.go`、`topic.go` 支持媒体/文件/forum，`SendOptions` 带 thread | 已读 `LongPoller` 在交队列前改 `LastUpdateID`；主要 API 是框架处理器风格；最新 release 与所读 HEAD 不能混作同一能力版本 |

**我定的：长轮询优先。** 本地桌面程序没有稳定公网 HTTPS 入口，出站 HTTPS 可直接复用本机代理，睡眠/重启也无需维护域名、证书和入站端口。webhook 仍要处理持久确认、重复投递和停机期间的积压，不能解决桌面离线；云端服务确有公网接收需求时再单独设计，不在本次保留两种传输开关。

**我定的：选 Telego，使用公开的单次 `GetUpdates`，由 Sumika 控制循环和持久游标。** 不用其 `UpdatesViaLongPolling`、handler 框架、`RetryCaller`；不另写 Bot API 客户端。配置 `WithHTTPClient` 使用宿主提供的 net/http Transport，`WithDiscardLogger` 后由边界报告脱敏错误；不依赖默认 fasthttp 的取消行为。

已读依据：Telego `methods.go:10–76`、`long_polling.go:109–153`、`bot_options.go`、`bot.go:166–255`、`telegoapi/api.go`、`caller.go:82–154`、`request_constructor.go:30–70`；Go Bot `get_updates.go:83–107`、`raw_request.go:28–60`、`methods.go`；Telebot `poller.go:68–87`、`topic.go`、`options.go`。Telego 当前 HEAD 的 Go 要求为 1.26.7，仓库为 1.27.3；这是静态版本比较，未编译。

实施先固定本次审阅的 Telego 提交为精确 module 版本，再做假 HTTP 冒烟；不把所读 HEAD 的能力冒充 v1.12.1 的能力，不依赖浮动 main。本任务不加依赖、不新增 ADR。选库建议需要按项目依赖流程落地，但不需要主人替程序选择每个 HTTP 参数。

### 1.3 官方协议事实

以方法/类型页面为准：

- [getUpdates](https://core.telegram.org/bots/api/methods/getupdates)：只有下一次请求的 offset **严格大于** update_id 才确认；与 webhook 互斥，limit 为 1～100。旧 FAQ 的小于等于表述不作为边界依据。
- [Getting updates](https://core.telegram.org/bots/api#getting-updates)、[Update](https://core.telegram.org/bots/api/types/update)：服务端更新最多保留 24 小时；至少一周没有新更新后 ID 可以随机重起。ID 跳号不证明漏消息。
- [Message](https://core.telegram.org/bots/api/types/message)、[实体格式](https://core.telegram.org/bots/api/formatting)：消息 ID 在 chat 内唯一；sender_chat 可伴随兼容假 from；实体位置按 UTF-16；普通消息长度与 caption 上限不同。
- [Privacy Mode](https://core.telegram.org/bots/features#privacy-mode)、[Bot 互聊](https://core.telegram.org/bots/features#bot-to-bot-communication)：隐私模式影响可见更新；当前有条件式 Bot 互聊，不能用“永远收不到 Bot”当防循环措施。

## 二、装配边界与缺口

| 层 | 本次实现目标 | 已核对现状 / 要补的契约 |
|---|---|---|
| 平台适配器 `internal/channel/telegram` | Telego 调用、协议翻译、执行出口交来的动作、报告连接事实 | 不写 SQL，不认主人、不调模型、不判闸门；一个平台 Adapter 管多个 Bot，逐 Bot poller |
| 渠道中间件 / store | 准入、记录、观察、持久确认、版本投影、队列、发送计划与回执 | [event.go](../native/internal/channel/types/event.go) 没有编辑和完整回应集合；[intake.go](../native/internal/channel/intake.go) 的 Sink 仅入队，不能用返回 nil 作为确认 |
| Telegram 策略 | 可信身份、私群准入、命令、主题、发送模式、审批输入转换 | 复用既有策略分发；Telegram-only 不要求 QQ 账号/协议端；`ExtraChannels` 只是测试入口 |
| Agent / 功能插件 | 复用 Hub、观察来源、记忆、工具审批、平台无关有序段 | 不出现 token、offset、Telego 类型；链接插件不自己判断平台或发请求 |
| 桌面 / 查询 | Telegram 列表、topic、引用、编辑、回应、媒体和缺口状态 | 登记 [DefaultRegistry](../native/internal/store/platform.go)，增加明确的展示范围分支；未知平台继续拒绝 |

`Roster`、Intake、Gate、Outlet、Hub 仍各一份；按平台和 Bot 拆状态，不复制一套 Agent。平台独立启停遵守 ADR-262，401、身份错和消费者冲突只停该 Bot 的收发，其他平台继续运行。共享数据库等宿主故障照现有全局错误路径。每 Bot 运行独占权覆盖接收与发送，避免两个进程抢同一 Bot。

[secrets.Store](../native/internal/secrets/secrets.go) 现在只能 Open/Get，不能宣称已有安全录入 UI；[Conversation](../native/internal/channel/types/conversation.go) 没有 Thread；[Receipt/Caps](../native/internal/channel/types/adapter.go) 没有 retry_after、UTF-16 单位和 caption 计划。这些均为实施前置改动，不能在适配器里偷偷拆条、直发或重试来掩盖缺口。

## 三、动手前的形状 × 入口表

“处理了”表示规格已有规则，**不表示测试通过**。实施逐格换成“测了＋证据”；不可能只表示该入口不消费该字段。线上、恢复、直接命令调用、出口、桌面/上下文和回放/评测读同一套边界，不各写一套较宽的判断。

| 形状 / 敌对输入 | 入站翻译、准入、记录 | 恢复、版本、审批结算 | 出站、下载 | 桌面、上下文、回放/评测 |
|---|---|---|---|---|
| ID 缺/null/空/0/小数/错类型/超 int64；合法负 chat ID | 处理了：边界校验，合法 ID 转十进制串；坏路由不交回合 | 处理了：不猜主人/会话，不推进未持久位置 | 处理了：负群号保留，消息 0 不引用/不冒充确认 | 处理了：往返保值，错误明确 |
| 名字“主人”、换行/零宽、QQ 同号、引用/转发作者是主人 | 处理了：平台＋当前真实 user ID；行头转义 | 处理了：恢复不重新按名字授权；审批不读引用作者认人 | 处理了：出口重核平台/Bot/会话 | 处理了：来源只作证据，不当授权 |
| sender_chat/actor_chat、匿名管理员、假 from、自己 Bot、无发送者 | 处理了：chat 主体优先，未知不触发 | 处理了：不匹配主人，不消费审批 | 处理了：不能请求执行或替人批准 | 处理了：明确标身份种类 |
| 重投、两个 Bot、同 chat/msg ID 的不同私聊、普通群多接收 | 处理了：更新幂等与消息映射分层 | 处理了：不重复回合/审批；观察分别记 | 处理了：只用当前 Bot 的原始 ID/file_id | 处理了：不能按同文合并 |
| batch 中间失败、提交前/后崩溃、一周重起 ID | 处理了：持久成功前缀和流代次 | 处理了：已收事件恢复投影，不重放外部副作用 | 不可能：出站结果不推进更新 offset | 处理了：查实际 offset、事件、触发数 |
| 两 topic、General、通知无 thread、群升级 | 处理了：chat 与 thread 分开 | 处理了：按消息映射找 topic，孤立状态不猜 | 处理了：thread 随请求，配额按 chat；不自动授权新群 | 处理了：查询/停止/审批不串主题 |
| 原文晚于编辑、同秒编辑、换图后旧下载完成 | 处理了：独立版本事件 | 处理了：旧版不盖新版，冲突可见 | 处理了：下载关联具体版本 | 处理了：同一投影用于展示/下轮上下文 |
| emoji 在实体前、caption 实体、越界/半代理对、嵌套标记 | 处理了：UTF-16，坏实体保正文不造 @/命令 | 处理了：审批只认本条正文和可信路由 | 处理了：按可见内容拆分，再转义 | 处理了：Unicode 往返、不改原文 |
| photo 多尺寸、animation+document、未知类型、假文件大小/路径 | 处理了：按语义翻一次，元数据先记 | 处理了：下载/版本结果另追加 | 处理了：流式硬限、固定主机、文件名不当路径 | 处理了：下载不到不冒充已看过 |
| 429/403/5xx/超时、已发一半、message_id=0 | 不可能：不作新消息入站 | 处理了：结果事件追加；未知不补发 | 处理了：账号共享等待，confirmed/failed/unknown 分开 | 处理了：显示部分成功/可能发送 |
| 非主人“同意”、伪审批消息、转发/编辑同意、多单/过期/重放/并发批准 | 处理了：原始输入照记，控制输入不交模型当批准 | 处理了：查后台单号/消息映射、期限、一次性状态，原子结算 | 处理了：投递未知不形成可批准入口 | 处理了：拒绝原因、审批来源可查 |
| 关闭后返回、密钥锁住、代理故障、token 在异常 URL | 处理了：取消并等退出，不降直连 | 处理了：迟到回调不碰已关资源 | 处理了：错误脱敏，缺钥只停该 Bot | 处理了：无 token、无迟到写盘 |

| 行为 / 场合 | 本地私聊 | 本地群聊 | QQ 私聊 | QQ 群 | Telegram 私聊 | Telegram 群 / topic |
|---|---|---|---|---|---|---|
| 认主人 | 处理了：可信本地入口 | 处理了：实际发起者 | 处理了：QQ ID | 处理了：QQ ID | 处理了：登记 user ID | 处理了：当前 user ID，管理员不等于主人 |
| 执行授权 | 处理了：原权限审批 | 处理了：ADR-269 每次批 | 处理了：ADR-277 后台落地前仍禁；落地后只限主人直接引起 | 处理了：同左，群友不能触发 | 处理了：同一 ADR-277 后台门槛 | 处理了：同左，混批/角色续接不升级 |
| 审批输入 | 处理了：原本地入口 | 处理了：原本地入口 | 处理了：ADR-277 单单直回，多单指明 | 处理了：主人引用指定单 | 处理了：单单直回，多单指明 | 处理了：主人引用指定单 |
| 记忆 / 原话 | 处理了：ADR-242 标来源；原话按观察 | 处理了：同左 | 处理了：同左 | 处理了：同左 | 处理了：同左；用途选择见十二节 | 处理了：同左；群准入不替代用途选择 |

## 四、轮询与持久确认

### 4.1 启停和参数

我定的工程初值：`getUpdates(limit=100, timeout=25s)`；轮询请求总时限 40s，普通 API 15s，建连 5s；文件传输 60s 并允许取消。网络重连按 1、2、4、8、16、32、60s 退避，上限 60s，成功往返清零；取消不重连。参数不是实测结果。

启用后取密钥、取得 Bot 独占权，显式 `getMe` 核配置 Bot ID 和 `is_bot`；身份不从 token 前缀或 SDK 的懒加载 username 推断。读取 `getWebhookInfo`：非空就停在“已有 webhook”状态，不自动删；迁交旧消费者另由主人决定，禁止 drop_pending_updates 和负 offset。409 停在冲突状态，不抢轮询。空数组是健康往返，不能用没人说话判掉线。

每次明确订阅 `message`、`edited_message`、`my_chat_member`、`message_reaction`、`message_reaction_count`。若将来批准按钮，再加 `callback_query`；当前不开放按钮审批。`allowed_updates=[]` 默认不含 reaction；不指定会沿用旧订阅。遗留类型照“已忽略”持久处理，不误路由到普通群。

### 4.2 确认点与恢复

确认点是 **telegram.db 已经保存可恢复的输入事实或明确拒收/忽略结果**。HTTP 返回、放进内存队列、Sink 返回 nil、模型开始处理都不算。复用 Ticket 等待形式，新增覆盖消息、通知、编辑、审批输入的中立持久接收契约；SQL 归中间件/store。

一条事务追加接收事实与其来源 `(bot_id, stream_epoch, update_id)`，同时更新派生的 `next_offset=update_id+1`。按服务端 batch 顺序逐条提交，下一次 getUpdates 只携带持久成功前缀之后的 offset；中间失败不能跳到 batch 最大 ID。准入不允许的只留必要路由/拒收原因，不暗存其正文。准入后、守卫前记录正文，守卫拒绝另追加标记（ADR-270）。事务里不等网络、模型、下载。

应用事实、消息映射、观察和“已应用”投影在同一平台事务收敛；后置回调只在提交后执行。接收和投影可以分开提交，事件账本是恢复依据。崩溃前未提交的由 Telegram 重投；已提交未确认的重投查原接收事实，不重复事件/回合/审批。持久接收之后的外部副作用不承诺 exactly-once：恢复输入只补记录和上下文，标 backfill，不重发回复、不补执行命令。审批执行中断也不能靠恢复输入再次执行。

首次无游标取最早未确认更新；首次积压与重连排空记 backfill，不突然回复旧消息。排空阶段允许 timeout=0，空批后恢复长轮询。未处理队列有界：每 Bot 最多一批在途，不在内存无限预取；持久处理跟不上就暂停下一批，不跳过。

一周无新 Update 时，持久记一次流重定位，再省略 offset 找最早未确认更新；同一空闲期不反复增代，业务消息映射跨代仍幂等。update ID 不连续不能造“漏 N 条”。离线或权限不可见区间单独追加记录；超过 24 小时明确“可能缺消息，Bot API 无法补回”，恢复/空页不能关成“完整”。

Telego 对未知嵌套 union 可能整批解码报错（如 reaction 类型，`types.go:4990–5017`）。此时保持游标、可取消退避并报告解码受阻，修依赖/协议边界后再取；不能用 offset+100 猜着跳，也不能降成未经校验的可执行消息。已能读取合法 update ID 的普通未知类型可存 ignored；读不到 ID 的响应不能确认。

## 五、身份、会话与只追加记录器

### 5.1 身份与主题

主人只认本机登记的 `Owners["telegram"]` 正整数 user ID；接收账号认 `getMe.id`，发送者认当前 `message.from.id`。`telegram:<user_id>` 不与 `qq:<同号>` 自动关联；昵称/username/签名/转发作者/共享联系人/引用主人都不能赋权。平台 ID 定型为 int64 后转规范十进制串；配置、库和公共接口使用字符串，不经 float64，群 chat ID 允许负数。

`sender_chat` 优先于兼容 from，表示 `telegram:chat/<chat_id>`；回应的 `actor_chat` 同理。匿名管理员不是主人；无可靠主体记录未知、不触发回复/执行，不拿会话对方补成主人。`is_bot` 保留；收到其他 Bot 消息只记录，触发默认关闭，不靠服务器不可见性防循环。

统一 Conversation 增加中立 `Thread`；ID 保持真实 chat_id，私聊 Self 是 Bot ID，群不带 Self。编造示例：`telegram:private:90001:70001`、`telegram:group:-10070001:thread:17`。普通群/超级群都映为 group，实际 chat_type 另记。队列、Hub、停止、历史、审批源会话使用完整 Thread；群准入和发送配额按实际 chat。

超级群论坛的 General：无 thread 与显式 1 归同一默认主题，保留原始 wire thread，向 General 发普通消息省略 message_thread_id；其他 topic 必须携带正 thread ID。保留并识别 forum_topic_created/edited/closed/reopened 等服务事实；主题创建根消息不当成普通用户引用。私聊若平台送来合法 topic 同样保留，BotFather 未开启时不主动创建。未知主题不按“最近说话的主题”猜；topic 关闭/无权限时失败，不改发 General。

reaction 更新没有 thread，按 `(chat, message)` 映射找主题；未见过目标时保留孤立通知待关联，不归到默认主题。群升级追加 old→new 关系，旧证据、消息号、会话不改写，新 chat 未准入不自动开收发，也不把旧 message ID 发到新 chat。

消息映射：私聊 `(bot_id, chat_id, message_id)`；超级群 `(chat_id, message_id)` 共享原始消息，各 Bot 追加观察与原始地址；普通群消息号跨账号不能假定相同，沿旧草稿的保守工程方案，同一普通群只登记一个接收 Bot，检测到第二路明确拒绝配置，不靠文本合并。超级群共享依据还参考[底层消息号序列](https://core.telegram.org/api/updates#message-id-sequences)，Bot API 真机多 Bot 对照仍列验收。主题不构成新的消息号命名空间。内部映射键不直接当 Telegram message_id 发出。

### 5.2 分库、事件与派生数据

ADR-199：`telegram → telegram.db` 正式登记，经 `paths.ChatDB("telegram")` 取路径，不硬编码当前/未来数据根；只读 Required=false，旧安装没有该库仍可看 local/qq。记忆属于角色，复用 ADR-242 的全局召回与来源标签，不加 telegram.memoryAll，不把跨平台查原话放宽成扫全库。

ADR-274：以下名字是拟定数据职责，不是已存在的表结构；沿用当前 store 迁移机制，旧迁移冻结，增量迁移升级前备份，失败整体回滚。

| 数据 | 唯一出处与修改规则 |
|---|---|
| 接收事实、原始消息/通知、编辑版本、回应变化 | 只追加；存收到的原文、实体、主体、原始路由、平台时间/接收时间和来源更新键；去除连接秘密，不拿截短预览代替原文 |
| 出站计划/候选内容、尝试、回执、取消/未知、审批创建/投递/结算/执行结果 | 每次状态变化追加事件，关联原事件或计划；发送成功不 UPDATE 原始发言冒充它当时已送达 |
| 游标、inbox 待处理/已应用、消息映射、最新编辑、回应集合/匿名计数、媒体下载状态、限速截止 | 明确归为派生运行状态，可更新和按账本重建；不得成为正文、审批决定或送达证据的唯一副本 |
| observations、来源/引用关联、媒体内容索引 | 观察是独立事实；迟到引用、下载 hash 作为追加关联事实，再重建索引；不原地改旧消息 JSON |

编辑展示可覆盖投影，原始事件不变；删除/撤回只变投影并保留原文事实。状态投影重建不能复发网络动作。备份按平台枚举，seq 引用必须带 conversation；同为 seq=1 的 QQ/Telegram 不是同一事件。接收正文不进普通运行日志；事件库有意保存的聊天与日志分开。

## 六、入站事件与媒体映射

| Telegram 输入 | 统一事件 / 段 | 消费规则 |
|---|---|---|
| private / group / supergroup 的 `message` | InboundMessage；Account=接收 Bot，Conv=第五节，From=可信主体 | 新消息只追加；准入、记录、观察、拦截、闸门依既有顺序 |
| text/entities、caption/caption_entities | SegText；已核实的用户提及为 SegAt；格式实体及 text_link 目标保留为内容元数据 | 段是内容依据，plain 为派生；不反解析正文当身份；caption 不套 text 的 offsets |
| `text_mention`、`mention`、`bot_command` | ID 提及保留稳定 ID；@username 精确匹配当前 getMe/已知映射，否则仍是文本；命令仅认开头合法实体 | UTF-16 越界/半代理对的实体不产生可执行命令或 @；保留正文并记解析问题 |
| reply_to_message | SegReply＋原始地址和只读快照；实际 quote 为摘录元数据 | 快照不额外制造角色观察；缺目标明确未存，不能虚构任意消息查询 |
| external_reply、quote、forward_origin | 来源/摘录关联，保留是否手工 quote；不当 QQ 合并转发 | 不自动跨会话取正文、不替换当前发送者；引用不是批准证据 |
| edited_message | 新增中立 MessageEdited 事实，指向原消息，携完整新版本 | 不走“重复新消息只补观察”；不计闸门新发言、不自动重答；自动停/重答策略见十二节 |
| 普通聊天删除 | 无通用入站 Update，能力为不支持 | 不从 400、空页、下载失败推断被删；本 Bot 主动删除若将来有授权，只记本次动作回执；Business 删除类型不混入普通聊天 |
| photo | 一份 SegImage＋PhotoSize 候选、file_id/unique_id/尺寸 | 多尺寸是一张图；选择可下载且合预算的版本，原始候选保留 |
| sticker | SegEmoji＋静态/animated/video 类型、emoji、set 等元数据 | 静态可解码才显示图片；TGS/WebM 保文件/占位，不伪造已解码静态图 |
| video / video_note / animation | SegVideo；保留子类型、时长/尺寸、缩略图、file_id | animation 同时含 document 是兼容字段，只翻译一次；不自动转码 |
| voice / audio | voice→SegVoice；audio→SegFile、子类型 audio，保时长/MIME/标题 | 不把文件到达当成转写成功；STT 不属于适配器 |
| document | SegFile＋原名/MIME/字节数/file_id | 图片按文件发也保留 document 类型，不按后缀伪装照片 |
| media_group_id | 每条 message 先独立入库，再做相册关联 | 按 Bot 消息作用域＋chat＋group ID；不等合组才确认、不丢每个 message ID |
| message_reaction | NoticeReaction 扩为 actor 的 old/new 集合事实 | new 集合替换该 actor 当前集合，含清空/换表情；不是只增加 |
| message_reaction_count | 独立聚合回应快照，actor 为空 | 替换聚合总数，不猜匿名用户、不与逐人集合重复相加；可能延迟几分钟 |
| my_chat_member、群迁移/主题/成员服务消息 | 对应中立 Notice；连接能力变化另报 Status | 服务事实照记，不算普通发言；权限减少只收紧 |
| 未支持/未来类型 | 可路由者留 Other 类型与 200 rune 安全预览，原始已准入事实另存 | 不把未知内容当主人指令；无可信路由则只留拒收结果 |

编辑排序使用平台 edit_date，单 Bot 同秒按接收 update 顺序；不同 Bot update_id 不可比较。更旧版本不能盖更晚版本；同秒跨 Bot 正文不同留冲突证据，不按最大 update_id 决胜。先见编辑再见原文保留两件事实，投影仍以编辑为准；本次建议仅影响展示和下一轮上下文，不自动回滚已形成的记忆。

媒体入库后走宿主下载能力：`getFile(file_id)`→仅内存构造官方下载 URL→流式写内容存储→追加媒体关联。`file_id` 属于接收 Bot；`file_unique_id` 不能下载，也不是 SHA-256。同图跨 Bot 重发使用各自的 file_id 或本地内容重传。

官方[下载上限](https://core.telegram.org/bots/api/methods/getfile)为 20 MB，地址至少有效一小时；使用保守十进制 20 MB 字节预算，同时检查声明大小与实际流硬限，超限保元数据和“云端 Bot API 不可下载”，不自动改用本地 Bot API server。文件名、MIME 不一定随 getFile 保留，应从原消息记下。到期重新 getFile，不假造 MessageReader。

照片/可解码静态贴纸按获批用途在入库后排队；视频、语音、普通文件默认按需下载，下载策略待十二节确认。并发初值 2；文件名仅展示，不当本地路径；临时文件与最终内容存储均由数据根管理，下载失败/取消删除未完成文件。编辑换媒体后旧下载只关联旧版本，不覆盖新投影。文件 URL/token 不进入 Seg.URL、events、trace、缓存键或模型。

## 七、出站拼装、格式与频率

### 7.1 唯一出口与平台能力

角色回复、链接自动化、命令回复、审批卡及状态通知全部走现有唯一出口：来源绑定→记录候选/检查→Telegram renderer 定型→记录实际发送计划→配额与最新权限复核→适配器动作→追加回执。不可变内容及计划在 HTTP 前持久化；渲染改变可见内容的部分必须重新检查，不能以分段/转图跳过守卫。适配器一动作一次请求，不内部暗拆/暗重试。

普通文字、photo、video、animation、同会话引用、普通群 reaction 属于目标能力；本次不做发送语音/任意文件/贴纸和媒体组发送，收到这些媒体仍完整记录。统一 Caps 按实际消费者扩 caption、UTF-16 和已支持媒体，QuotePrivate/Group=true、History/Echo/Poke/ForwardPrivate/ForwardGroup=false；ReactGroup 仅已授权基础 emoji/清空，不宣称任意 custom/paid reaction。未实现动作返回 ErrUnsupported。Telegram `forwardMessage` 是原生转发，不等于 QQ PartForward.Nodes，不伪造合并转发能力。

### 7.2 长度、格式和转义

[sendMessage](https://core.telegram.org/bots/api/methods/sendmessage)正文 1～4096 字符，[sendPhoto](https://core.telegram.org/bots/api/methods/sendphoto) caption 0～1024 字符，均指解析实体后的文本。**我定的保守计数是 UTF-16 单元**，与已读 Hermes 相同；不把 Go 字节数/rune 数说成官方所有字段的定义。单位进入 Caps 并由出口真正消费，QQ 继续自己的单位；不统一砍成 1000 字。

选择普通消息的 **HTML renderer**，不把模型或网页原始 HTML 直通 parse_mode。基于公共富文本/Markdown 的结构只生成允许的 b/i/s/code/pre/a/blockquote；其余节点保内容、退成普通文字结构。文本 `< > &` 和属性引号转义，href 经链接规则校验；不执行 HTML，不把 `<a>` 文本伪装成真实目标。不让 Telegram 解析原始 CommonMark；不使用“全局正则替换”修 MarkdownV2。若以后采用 MarkdownV2，必须遵循官方[不同上下文的转义规则](https://core.telegram.org/bots/api/formatting)，不能复用 HTML escape。

先在可见结构的段落、换行、词/字素边界定型，再逐块生成闭合 HTML，重算局部实体位置；不在 `&amp;`、标签、UTF-8 或代理对中间切。长 pre/code 分块各自闭合并保留全部代码，不用省略号删内容。异常单字素超过预算时按 Unicode 标量边界切并标记边界受限，不丢原文。格式/网络报错照三态结算，不捕获全部错误后悄悄纯文本重发。

### 7.3 ADR-272 的有序段接收点

输入是功能插件交来的有序 text/title/link/image/video 段，renderer 返回有顺序和来源段范围的动作计划；保持信息顺序与图片位置。短标题/作者/链接等相邻文字可合并，正文按原段落切。**我定的拼法**：

1. 遇到一张照片（或支持 caption 的 video/animation），可把紧邻其前、合计不超过 1024 单元的完整文字段放 caption，照片用 show_caption_above_media=true；若无前文，可吸收紧邻其后的完整文字段作下方 caption。两边都有文字只取前侧，后文留给后续动作，不挪过图片。
2. 紧邻文字长于 caption：先按段落拆，能附图的最后一组完整前文作为上方 caption，前面的按 sendMessage≤4096 单元发；单段过长允许按 7.2 切分，所有字都保留。文字先拆出前缀，再发“余文＋图”，顺序不反转。
3. 连续图片按单图动作顺序发；本次不启用相册出站。官方 sendMediaGroup 支持 2～10 项，但返回多个 Message，合组的计数/部分回执和内插正文需额外契约；本路线无需它也能满足单消息附图，不留绕过出口的临时相册路径。
4. 引用只附第一个适用动作，`reply_parameters` 指向本 Bot 在同 chat/对应 thread 的已知原始消息，allow_sending_without_reply=false。后续动作凭计划顺序关联；引用失效不改发 General、不偷偷去掉引用重发。

| 输入（均为完整段） | 实际动作顺序 |
|---|---|
| 标题＋短正文＋图 | 一个 sendPhoto，上方 caption 含标题和正文 |
| 图 A＋短说明＋图 B | sendPhoto(A，下方 caption=说明)，sendPhoto(B) |
| 段 A＋图 A＋段 B＋图 B＋段 C | photo(A，上方=A)，photo(B，上方=B)，message(C) |
| 超 1024 的前文＋图，前文不超 4096 | 前文按上述保序规则拆，最后一块随图；拼回可见文本必须完全一致 |
| 连续长正文＋图＋正文 | 图前的 message/图 caption→图→图后的 message，不能提取全部图片到末尾 |

photo 上传≤10 MB，宽高之和≤10000，长短边比≤20；[sendVideo](https://core.telegram.org/bots/api/methods/sendvideo) / [sendAnimation](https://core.telegram.org/bots/api/methods/sendanimation) 使用各方法的 50 MB 上传预算，超限明确失败，不无声压缩、换内容或转文件。媒体通过已授权内容存储引用取文件流，先经统一下载服务取远端资源；不把任意 URL 交给 Telegram 代抓绕过代理/地址检查。[sendMediaGroup](https://core.telegram.org/bots/api/methods/sendmediagroup)仅作为能力比较来源。

### 7.4 配额、回执与部分失败

官方 [FAQ](https://core.telegram.org/bots/faq#my-bot-is-hitting-limits-how-do-i-avoid-this)建议单 chat 不超过每秒一条、群每分钟 20 条、广播约每秒 30 条；是限制/建议，不是“不出 429”的保证。我的工程初值：每 Bot 出站动作滚动 1 秒≤25；同 chat 消息间隔≥1s；群各 topic 合计滚动 60 秒≤20 且间隔≥3.1s。命令/审批/状态消息都占实际额度，不按功能另开配额；各 chat 公平等待，一个群限流不锁住别的 chat。

| 观察到的结果 | 回执与下一步 |
|---|---|
| 合法 ok=true，返回真实 chat、可用正 message_id | confirmed；记每个动作 ID；reaction 成功 True 无新消息号，不能编造 PlatformID |
| message_id=0 / 延后安排 / 成功载荷不足 | unknown/待确认，不冒充已送达；不再发一次 |
| 明确 ok=false 400/403/401 | failed；错误参数、无权限、拉黑不重试；401 停该 Bot |
| 明确 429＋retry_after | failed 且有结构化 not-before；至少作用于该 Bot 共享调度，重启仍遵守 |
| 明确请求未交出就失败 | 可重试 failed；同计划有限尝试 |
| 可能交出后的超时/断线、5xx、畸形或截断响应 | unknown，不盲目重发；Bot API 无通用发送幂等键/回查保证 |

统一 Receipt 扩结构化 retry_after/可重试原因，迁完适配器、fake、出口和契约；只有出口安排重试，总尝试初值 3，429 不早于服务端期限；等待可取消并重核许可。已确认的前段不重发；某段 failed/unknown 暂停其后段并显示部分结果，避免正文缺口后继续乱序发送。用户显式处理未知结果时另建动作，不把重试当作确认。禁止 allow_paid_broadcast=true。

## 八、主人、privacy mode 与远程审批

### 8.1 收得到什么与允许做什么

privacy mode 默认开启：明确给本 Bot 的命令、满足条件的通用命令、via-bot 内容、回复给本 Bot 的消息；服务消息、私聊另按平台规则可达。普通正文 @Bot 不能保证送达。关闭隐私模式需要重新入群才能生效；管理员通常能收全量，但申请管理员与关闭隐私都是主人选择，不自动执行。getMe 的 can_read_all_group_messages 与实际群权限一起展示；部分可见的群统计标“仅可见消息”，不冒充完整群热度。

reaction 需群管理员＋显式 allowed_updates；不是关 privacy 就一定能收。Bot 自己设的 reaction 不会收到对应用户更新，要用本次动作的确认事实投影。没有管理员权限时显示“不支持接收回应”，不为了验收提权。其他 Bot 可见条件以最新 Features 为准，本次不启 Bot 互聊触发。

命令经记录/认人后处理：/start 只介绍、深链参数不认主人；/help 返回当前可用命令；/settings 只给主人本机配置入口，不远程改权限；/stop 仅主人停本角色×完整会话，不能停其他 topic/平台。菜单 scope 不是授权，直接调用同样检查。/cmd@其他Bot 不执行，不让模型把未知命令转成 shell。

### 8.2 ADR-277 的已定规则

ADR-277 第 11 条是生效前提：共享审批后台没有实现前，Telegram 也不给执行/写文件工具。落地后，只接受可信主人消息**直接引起**的回合申请；混批含非主人触发、群友诱导后角色接话、引用主人、匿名管理员均不满足。执行参数、目标和来源由后台绑定，模型不能提交“批准”工具。

完整单据发登记的主人私聊，群里仅发“已发你确认”和不泄密的短单号。群这条提示是后台登记的审批消息，可由主人引用它回“同意”；不在群里透露命令/路径。私聊只有一单且关联无歧义时，主人直接回“同意”即批；多单必须明确引用/选定具体单，不能猜最新一单。Telegram 私聊不通可投到另一平台已登记主人私聊，仍是同一单、同一 5 分钟期限，不因换平台重置。

计划/参数摘要、发起者、源平台/Bot/chat/thread、owner ID、动作摘要哈希、可批准入口、已投递的实际消息 ID、创建/截止时间由后台固定。投递未 confirmed（含 unknown）不建立可批准入口，转不了其他已登记入口则不通过；不能把完整卡发群里充当退路。审批超过普通消息长度时，分块完整投递并建立一条最终确认锚点，所有块 confirmed 才允许批；不得截去危险参数。

只认新的直接用户消息：TrimSpace 后精确“同意”批准一次，“拒绝”拒绝；转发的同意、编辑成同意、引用里的同意不结算。未匹配继续等；危险动作不用自然语言模型辅助。非危险动作如以后使用辅助，也只能返回同意/拒绝/看不清，不能改变人、单号或范围。

群必须引用后台保存的原审批提示/卡消息 ID，且当前发送者是主人、接收 Bot/chat/thread 匹配；不能凭“看着像审批卡”的文字、按钮标签、引用作者核验。私聊的无引用同意在出现过期/替换卡导致关联歧义时要求引用新卡，旧迟到同意不得落到另一单。重启恢复的积压同意只记账不执行，旧会话审批取消；主动重发申请产生新单。

| 后台状态 | 输入 / 条件 | 转换与副作用 |
|---|---|---|
| created / delivering | 投递成功且入口可用 | 追加 delivery，进入 pending；失败/未知且无可用入口→delivery_failed，不执行 |
| pending | 非主人、错单/错路由、伪造、歧义 | 记拒绝匹配原因，保持 pending，不扩大范围 |
| pending | 合法同意且 now < expires_at、权限与动作摘要未变 | 原子追加 approved 并占用一次性执行权；并发/跨平台只有一份胜出 |
| pending | 合法拒绝 / now ≥ expires_at / 取消 / 宿主重启 | 追加 denied / expired / cancelled，停止等待 |
| approved | 交执行前权限失效、动作摘要变化、不能隔离 | 取消/拒绝执行；不能用旧批准跑新参数 |
| approved / executing | 重启、重复同意、重复 callback | 不再授权新执行；执行中断标结果未知，绝不重放命令 |
| 任意终态 | 迟到/重放同意 | 返回已处理/已过期，不转到别的单 |

高危可远程批，但入口必须是该次执行碰不到的通道；若正在控制批准所用的桌面/账号或无法证明隔离，不能从那里批准。任务目录和派出代理限工具沿 ADR-277；做不到有限权限拒绝，不退裸跑。外平台默认不继承 yolo，只有本机设置显式按平台选择才采用；本规格不设开关为 true。结果群里只回成败，命令输出/路径/文件/截图发主人私聊或本机，ADR-277 第 7 条仍是主人待定建议。

### 8.3 回复与内联按钮的比较

| 方式 | 防伪条件 | 判断 |
|---|---|---|
| 群引用回复“同意” | 当前 from.id=owner；引用命中后台实际发出的 bot/chat/message；单未过期且一次性；thread 一致 | 可确定单据，符合 ADR-277，作为正式入口 |
| 私聊无引用“同意” | 当前 owner、批准目的会话、唯一无歧义 pending；多单/旧卡歧义要求指明 | 符合已定习惯；弱点是无单号，不能用“最近那单”消歧 |
| Inline Keyboard callback | callback.from.id=owner；随机不透明 token 查后台单；匹配 Bot/chat/message/thread、期限、动作摘要；CAS 结算；不是信任 data 文本 | 有显式单据关联，减少无引用歧义；同样不能防主人账号被盗。它是新增批准手势，待主人定后才开 |

按钮若获批：callback_data 只放 128 位随机 token＋动作标记，总计≤64 字节；不放命令、路径或可自报的 owner；只给允许一次/拒绝，不借 Hermes 的 session/always。`CallbackQuery.data` 是不可信输入，官方明确提示它未必仍对应当前按钮；缺 message、只有 inline_message_id、原卡不可访问或映射不符均不批准。后台尽快 answerCallbackQuery，只表示交互已响应，批准成功必须以持久结算结果为准；UI 移除按钮不代替一次性门槛。[CallbackQuery](https://core.telegram.org/bots/api/types/callbackquery)。

## 九、密钥、代理与配置归属

令牌只放加密密钥库，配置仅存 `tokenKey`（拟名 `telegram.<BotID>.token`），不存明文或环境变量兜底。宿主在本机解锁后注入 Adapter，聊天、模型、trace、原始 Update 录帧、HTTP dump、URL 错误链、缓存文件名都不得包含 token。当前 secrets 只读：实施先接安全本机写入/解锁入口或使用已存在条目，不能要求退役 Bun 录入，不能要求把 token 发聊天。

Telego 默认日志即使不在 debug 也可能报告含 URL 的网络异常；必须关闭 SDK 日志，在边界将 URL、代理凭据和 token 从错误链/通知中去掉，同时保留 method、状态码、超时/解码/权限等可诊断类别。下载 URL 的 `/file/bot<token>/` 同样脱敏。SDK 的 Token/FileDownloadURL 不暴露给功能插件。轮换令牌：取消旧请求并等退出→从库取新值→getMe 核同 Bot→续原游标；不能同时开两代 poller。

ADR-273 原文管链接取数；**我定的扩用**是 Telegram API、getFile 下载和链接媒体预下载都消费同一宿主网络策略：配了代理就用代理；fake-IP `198.18.0.0/15` 或默认路由 TUN 时用配置代理；没有可用代理则报告阻塞，不悄悄直连。无代理且非 fake-IP 才可直连；不把 fake-IP 本身当内网目标拒掉。

API 与带 token 的文件请求仅发 `https://api.telegram.org`，TLS 正常验证，不允许第三方 base URL；下载 file_path 只接受官方相对路径，禁止 scheme、主机、..、反斜线/控制字符和凭据跳转；带 token 请求不跟随重定向到别的主机。恶意消息给的 URL 不能改变 API 根。链接下载按 ADR-273 检查字面私网地址/主机名及跳转，代理负责解析后不转内网；不能通过把 URL 发给 Telegram 代下载绕开检查。代理端口不写死在平台包里。

| 配置 / 状态 | 归属与未配置行为 |
|---|---|
| Telegram enabled、Bot ID→角色、tokenKey、主人 user ID | 本机平台设置；未启用不联网，缺钥/错 ID 停该 Bot；一个 Bot 只绑一角色 |
| 私聊准入、群/主题范围、记录/试运行/说话、媒体自动下载 | Telegram 自己的策略；待十二节确认，不继承 QQ 的通配范围 |
| privacy、管理员身份、Bot 互聊 | Telegram 端事实/主人选择，程序只报告，不自动修改 BotFather/提权 |
| yolo、批准入口与跨平台主人映射 | 共享权限/审批设置；默认不等同本地；设置更改取消旧授权条件 |
| proxy | 宿主网络/下载服务，平台仅消费；不得 import 链接插件私有配置 |
| offset、退避、配额截止、健康 | 逐 Bot 运行状态；chat 限额含所有 topics；不把 token 作为持久键 |

配置键在实施时按 `native/internal/config → sumika.example.yml → 子系统参考页 → 本规格` 同步；本次没有新增可用配置，所以不提前在示例/参考页宣称支持。桌面复用既有平台设置结构，不在本任务设计新页面。

## 十、禁止的偷懒写法与已知的坑

禁止：

- 直接用 SDK 自动轮询提前确认；入内存即推进 offset；启动丢积压、冲突清队列、自动删 webhook；只保新消息却漏通知/编辑/审批确认路径。
- 适配器写库/认主人/判闸门；功能插件/命令/审批通知直接调用 SDK；每 Bot 建第二套 Hub；用 ExtraChannels 冒充正式接入。
- 更新旧消息正文或覆写原始回执；把投影表当审批/执行的唯一证据；重建索引顺带重放发信或 shell。
- 按名字/username/引用作者认主人；匿名 sender_chat 的兼容 from 提权；跨平台裸数字匹配；普通群同文去重；缺字段退成主人。
- thread 冒充 chat；topic 各领 20 条/分钟；引用失效退发 General；群升级自动授权新 ID；unknown 平台退回 QQ。
- 全文截断、把图集中到末尾、Markdown/HTML 原串直通、转义后按字节切、caption 超限静默删字、无条件格式失败重发。
- 大文件整份读入内存、file_unique_id 当下载凭据、file_id 跨 Bot 用、下载阻塞确认、假文件名写任意路径、把 token URL 写入段/日志。
- 以关闭 privacy 替代 Sumika 准入；为了收到 reaction 自动提权；把菜单权限或按钮 token 当主人身份；批准多个 pending 中的“最新一单”。
- 5 分钟续期、迟到同意转批另一单、编辑/转发同意生效、审批卡未知送达仍执行、超时/5xx 自动补发、内置 RetryCaller 偷重试、付费广播。

已知的坑（均对应第三节与下一节验收）：

1. 24 小时保留更新不等于可查 24 小时历史；隐私模式没投递的无法补；一周后 ID 随机重起不能靠最大值永久前进。
2. 编辑可能只是平台不可见字段变了；原文/编辑/媒体下载乱序不能覆盖新版本；同秒跨 Bot ID 不可比。
3. 普通聊天无通用删除通知，Bot 自己的 reaction 无更新，匿名计数延迟；缺更新不是“撤回/回应不存在”的证明。
4. 私聊要先有平台允许的互动才可发；知道主人 ID 不保证审批私聊可达；多 Bot 私聊的 chat_id 相同仍是不同对话。
5. HTML 长度按解析后内容，emoji/实体位置按 UTF-16；合法用户 Markdown 不是 Telegram MarkdownV2。General 的缺省 thread 与 1 不能分裂队列。
6. 云端下载 20 MB 与上传限制不同；file_id 不能跨 Bot，过期取址与消息删除不是一回事；贴纸不总是静态图。
7. Telegram HTTP 没有可依赖的通用发送幂等键，回包丢了只能诚实显示 unknown；不能为“自动恢复”牺牲不重复外发。
8. Telego 当前上传为流式，但选默认 transport/重试装饰器会改变取消和内存语义；union 解码错误可能卡整个 batch，必须有受阻状态。
9. 远程审批来自可信账号仍可能因账号被盗而被利用；本规格遵循 ADR-277 已定风险取舍，不能声称按钮能证明本人在场。

## 十一、验收标准与实施步骤

### 11.1 行为证据

下表是后续实现的完成标准，**本次均未运行**。自动验证只用临时数据根、编造号码/文件、假 HTTP 和可控时钟；可靠性不用模型评分。永久测试只保消费者可见的边界/状态转换，临时冒烟证明正式装配经过真实网络调用路径。

| 编号 | 必须观察到的结果 | 验证入口 / 证据 |
|---|---|---|
| T1 | Telegram-only 正式装配可运行；启停只作用本平台；getMe mismatch/401/webhook/409 不影响 local/QQ | 实际启动 app 对假 HTTP；核状态与请求，不连真实 QQ |
| T2 | 消息/编辑/通知/审批输入提交前后崩溃均不丢已收事实、不重复副作用；batch 中失败不确认后缀 | 提交屏障＋子进程重启；记录 offset、账本、观察、实际 HTTP/执行次数 |
| T3 | 首次积压/重连 backfill 不开口；空批健康；跳号不造丢失条数；七天重定位只一次；超24h区间仍不完整 | 假时钟＋轮询参数及本地状态；未知 union 不猜 offset |
| T4 | 冒充名、sender_chat、假 from、跨平台同号、转发/引用主人不提权；四种既有场合与 Telegram 两种全覆盖 | 从协议转换到工具分配/直接命令/审批后台，核真实 actor、触发来源 |
| T5 | 两 topic、General 缺省/1、私聊 topic 不串；两 Bot 私聊分开；超级群同消息一事实各观察；普通群多接收显式拒绝；升级不改旧证据 | 临时平台库＋队列/停止/真实 wire chat-thread；真实多 Bot 编号另由 T15 核 |
| T6 | text/caption UTF-16、隐藏链接、媒体引用、quote/external_reply、编辑先到/同秒/冲突、reaction 增删/匿名总数正确 | 原始事件不变，投影、桌面、下一轮上下文一致；不造新发言/自动重答 |
| T7 | 照片多尺寸一图、animation 兼容不重复、贴纸/语音/文件元数据完整；相册不丢 ID；下载硬限/取消/换图正确 | 实际文件流、hash/字节数/版本关联、临时文件清单；20 MB 边界和伪 Content-Length |
| T8 | 令牌只在允许的网络请求中出现；日志、异常、库、trace、文件名/导出无编造 token；代理失效不直连 | 假 token＋代理/重定向/错误响应注入，查所有可观察输出；密钥锁住/轮换含取消旧请求 |
| T9 | 4096/1024 前一单位、恰好、超一单位及 emoji、长代码块不丢字；HTML闭合/转义；上方/下方 caption 顺序等于7.3表 | 从有序段→出口→假 HTTP，逐动作核可见文本、媒体位置、计划持久化与实际请求，不只测 helper |
| T10 | 跨计划/Bot/chat 的配额正确；topic 共群配额；429 重启遵守 not-before；unknown不重发；部分成功不重发前段/不越过缺段 | 可控时钟＋真实 HTTP 记录时间/次数，覆盖取消/切档/关闭，检查回执事件只追加 |
| T11 | 群仅主人引用真卡可批；私聊单单/多单、转投/过期/旧卡/重启积压、并发一次性、摘要/权限变化均符合8.2状态表 | 穷举状态×事件表；真实审批后台＋假执行器核执行次数，禁止用模型文字冒充成功 |
| T12 | 高危被控桌面不能批自己；外平台未启yolo不继承；结果群里无命令/路径/文件/截图；/stop不越主题 | 假执行目标与登记入口资源，查群/主人两路出口载荷及邻居状态 |
| T13 | 三平台分库、同seq不串；角色仅读观察过的原话；原始消息编辑/撤回/下载/回执后仍字节不变；删除投影后重建一致且0外发/执行 | 临时库、正式查询、重建冒烟；非空结果不是合格证据，核具体事件/版本/观察 |
| T14 | 取消 poll/下载/限速/审批等待后无迟到写库/发信/重连；有界关闭 | 屏障卡真实请求→关闭→释放；核退出、文件与计时 |
| T15 | 专用 Bot 的私聊、批准测试群/topic、privacy 差异、引用编辑、各媒体、回应权限、重启与多 Bot 消息号符合协议 | 主人指定 Bot/范围后逐项手试，结果脱敏；当前未授权、未做 |
| T16 | 达到下表资源/延迟预算，原因能解释，非删数据/关持久化过线 | 固定临时数据＋进程/文件计量，报告所有分位数和最终状态 |

保护类改坏验收：移除 sender_chat 隔离→T4 必红；把 offset 提前到入内存→T2 必红；审批去 owner/消息映射/期限/CAS 中相应门槛→T11 必红；去共享群额度→T10 必红。改坏拆被测逻辑，不改测试输入造错；恢复后相关包通过，记录具体失败。纯文档本次不编写测试、不做改坏。

### 11.2 性能与文件预算（我定的实施门槛，未实测）

沿[性能标准](notes/标准-性能与效率-2026-11-03.md)区分外部指南与工程预算：100ms 反馈来自响应指南；下列 CPU/内存/存储数字是项目暂定预算，不冒充行业标准。实施测前冻结，失败定位网络、SQLite、锁或分配，不按结果放宽。

| 场景 | 负载 / 计时口径 | 门槛与报告 |
|---|---|---|
| 入站持久接收 | 10 Bot/10角色、多chat与topic；每会话万条历史后持续10条/s，正文约1 KiB、媒体仅元数据 | HTTP结果可用→持久凭据 p95≤100ms、p99≤250ms；不含网络长轮询等待/模型/下载；核收件数与去重数 |
| 停止 / 关闭 | 卡住轮询、上传、下载、429、审批分别试 | 本地停止反馈≤100ms；平台正常取消并释放资源≤5s；记录超时请求的三态，不以杀进程过线 |
| 空闲 | 10 Bot 长轮询，10分钟，无消息/模型/媒体 | 增量单核CPU均值≤1%；净堆增量≤32 MiB；报工作集、Private Bytes、Go heap、goroutine和请求数；不能用任务管理器0%当测量 |
| 媒体流 | 2并发、近20 MB文件接收与近50 MB媒体上传，编辑/取消交错 | 除内容存储外的传输临时堆增量≤16 MiB，不随文件长度线性增长；产物hash/字节数、峰值与取消残留均核对 |
| 文件增长 | 单独新临时根装载10万条约1 KiB消息，另记媒体；重启/重复投递再跑 | 去重重投不新增正文事实/媒体实体；归一后DB+索引≤原始有效载荷4倍＋10 MiB预算，WAL峰值另报；无临时残片，媒体实体大小与唯一内容字节总数一致 |
| 桌面 | 10角色、万条历史、长消息/topic/编辑/媒体混排，沿既有界面规格 | 用ui.NewTester/帧统计检查，交互反馈≤100ms、60Hz帧预算16.67ms；报元素数、p95/max帧、>100ms长帧数、位置跳变；未跑不能写“不卡” |

### 11.3 分步落地

| 步 | 交付 | 验收 / 前置 |
|---|---|---|
| 1 | 持久接收/只追加事件、Thread、编辑/reaction、caption计划、结构化回执；迁完调用者与fake | T2/T4/T5/T6/T9/T10/T13；不顺手重写QQ策略，通用契约测试更新 |
| 2 | 固定Telego版本，Telegram Adapter、显式轮询、翻译、媒体取址、代理/密钥边界 | T1～T10/T14，实际起假HTTP做冒烟；新依赖走项目既有流程 |
| 3 | 正式平台装配、准入/命令/启停/唯一出口、库登记与查询显示 | T1/T4/T5/T12/T13；Telegram-only与local/QQ并行，不以测试注入代替生产 |
| 4 | 接共享ADR-277后台、审批单投递/匹配/结算、结果分流 | T11/T12/T14；共享后台未落实时保持外平台无执行，不能用暂时直批交差 |
| 5 | 桌面状态、媒体/主题、不可恢复区间、规模与资源验收 | T6/T7/T13/T16；按既有界面规则测，不启动退役网页 |
| 6 | 主人落实十二节的真实账号/范围后，专用Bot与测试私群验收 | T15；记录缺项，不宣称完整历史同步或提前开放真实群 |

每步独立可撤回，接口/迁移/依赖、参考页、示例配置、规格状态和待办同步；分支合并时按项目流程记录真实状态。实现阶段只跑相关包、低优先级并按重活锁执行；合并前由主会话跑 go-verify。本次只跑文档检查，不启动程序/模型、不拿重活锁、不写实现。

## 十二、待主人定

不重复询问 ADR-277 已定的执行条件、五分钟、群引用/私聊单单同意和按平台yolo；不在本文把建议写成主人拍板。以下不妨碍先用假数据实施，但真实接入前需明确：

1. **Bot与角色/主人登记**：用专用新Bot还是移交现有Bot、绑哪个角色、主人Telegram user ID；机器人身份/名字头像是否合适，是否已有消费者/webhook。令牌在本机密钥库录入，不发聊天。Bot API不能满足普通账号历史需求时另立TDLib方案。
2. **接收与说话范围**：先主人私聊还是测试群；其他私聊/群/topic收谁、回谁，陌生人是否记正文/自动下图；记录/试运行/说话分别明确。建议先一个Bot、主人私聊，Telegram默认不开收发，不把QQ通配扩大过来。
3. **群可见范围**：保privacy靠命令/回复，还是关privacy重入群旁听；要不要为reaction授管理员；不自动开Bot互聊。
4. **编辑策略**：建议只更新展示和下一轮上下文，不自动重答/撤回已发回复/撤派生记忆；若要停当前回合再答，需区分生成中、部分发送和全部发送。
5. **审批按钮**：已定文字回复照常；是否增加本文8.3的允许一次/拒绝按钮。建议先文字闭环；按钮不能跳过同一后台门槛。
6. **审批入口与执行结果**：登记哪些跨平台主人私聊用于不可达时转投；高危执行可用哪些独立入口。ADR-277第7条的“群里只回成败、细节给主人”仍按建议，等主人最终定。
7. **数据用途**：沿旧草稿保留的问题——Telegram原文送模型、长期整理、跨场合使用如何告知/取得同意，撤回/删除要求与只追加保留策略如何协调、静态加密如何落实。参考[平台条款4.2～4.4](https://telegram.org/tos/bot-developers#4-privacy)、[内容许可](https://telegram.org/tos/content-licensing)；本任务未审真实加密状态，不改ADR-242/274，也不以入群或群白名单代替选择。

**我定的**：Telego＋显式长轮询、net/http流式、持久确认/账本与投影分开、Thread/消息键、普通群单接收、HTML保序caption拼装、账号/chat配额与三态、密钥库引用及宿主代理复用、工程初值、验收和步骤。均为本规格工程建议；本次文档不使这些运行行为或权限生效。
