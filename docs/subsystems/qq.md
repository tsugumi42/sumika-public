# QQ 渠道：收发、认人、自检、本机目录

开发规则见 [开发指导/渠道与平台](../开发指导/渠道与平台.md)。

## 职责与边界

以下 TypeScript 路径记录已退役的 Bun 实现（ADR-203），连接边界和历史设计见 [规格-QQ渠道](../规格-QQ渠道.md)。Go 当前入口是 `native/internal/channel` 与 `native/internal/app`；记忆按 ADR-242 在四种场合均可召回、逐条标来源，下文旧实现的 `fullMemory` / `qq.memoryAll` 不适用于 Go；QQ 主人问照答，群友套问仍走发送前守卫，见[记忆召回规格](../规格-记忆召回不分场合.md)。

| 插件 | 服务名 | 源码 | 负责 |
|---|---|---|---|
| qq | `ctx.qq` | qq.ts（私有存档） | 认人、去重、下载媒体、入库、通知、断线补录、补下载、往上翻、发私聊和说话档的群、收发自检、名片簿、合并转发。依赖 `store`、`onebot` |
| qq-local | `ctx.qqLocal` | qq-local.ts（私有存档） | 本机 QQ、协议端、QQ 数据目录在哪（自动找、手动选、写回 `sumika.yml`）；把小黄脸的图复制一份到 `data/cache/qq/faces/` |
| qq-health | `ctx.qqHealth` | qq-health.ts（私有存档） | 掉线、恢复、被踢、号对不上、发送失败的提醒；记进 `qq_alerts`。依赖 `qq`、`store`、`onebot` |
| qq-console | 没有服务，只注册接口 | qq-console.ts（私有存档） | 主窗口 QQ 那一半要用的全部 `/api/qq/*`，加按哈希取媒体 `/api/blobs/:hash`；会话列表、聊天记录、未读、头像和卡片图片的代取 |

辅助文件（不碰网络、不存库的部分）：

- qq/segments.ts（私有存档）：OneBot 消息段 → 内部的段（`normalize`）、存进 `text` 列的纯文字（`plainText`）、小程序和分享卡片（`card`）、转发的条数和预览（`forwardSummary`）。另有 `formatLine`（给模型看的一行"[时间] 名字(身份)：内容"）；转义 `escapeUserText` 在 util/escape.ts（私有存档），这里转出去；名字（行头、转发行头、被 @、被引用的）用 `escapeName`，另把半角括号换全角、冒号换空格，伪造不出"主人(主人)："的行头。没给地址的图标成 `skipped: 'failed'`，那一行写"（没存下来）"，不和能看的图的占位"[图片]"混（识图按先后填描述时不错位）。
- qq/selftest.ts（私有存档）：自检的 11 项、收到的消息算哪几项（`recvItems`）、测试用的 64 × 64 PNG（`testPng`）。
- qq/faces.ts（私有存档）：小黄脸的复制和命名（`syncFaces`、`addUserFaces`、`applyCatalogNames`）。

边界：

- 群里说不说由群的档位管（规格-群聊闸门 10.1、ADR-090、ADR-106）：闸门放行后路由让她看一轮（这一批后面，配置 `characters.<角色>.groupReminder` 开着的角色再追加一条提醒，一直留在历史里，只群聊给，见 [agent-and-console](agent-and-console.md)"路由"，ADR-121）；只有档位是"说话"、这一轮开口条件（群聊场合注入测试最近 3 次都全过、且都是现在这套设置：提示词和卡、模型、群聊思考档位、群聊末尾的提醒开没开（ADR-121）、防线代码的哈希）过了、过了保险丝（ADR-099，util/fuse.ts（私有存档）：一小时上限 round(4 + 16 × 频率) 条，主动的到上限这一轮不看、被叫到的到 2 倍才不看，按最近 60 分钟算、试运行想说的也算；一条最多 200 字；拦下闸门记 fuse）的才走 `message/outbound`；一条没发出去，同一轮后面的都标"没发出去"；`send` 超时、等回包时断了的不重发（标"不知道发出去没有"），发了一半、不知道发没发的标 `partial`，保险丝照样算；超时那一段的回声过一会儿才到的，认成它（见 `send`）；`sendOut` 发群消息前再核一次档位是"说话"、群在白名单里，不然返回理由不发。2026-10-18 只开了主人的群。
- 发出去的只有纯文字（`send` 只组 `text` 段），拆开的每一条末尾、每行行尾单独的"。"去掉（ADR-110 第 1 条、ADR-114 第 2 条，见下面"发消息"）。主窗口不能往 QQ 里发东西，QQ 页是只读的。
- QQ 渠道里没有本地执行能力：`forAudience` 对非主窗口的听众只给记忆工具（见[工具与审批](tools.md)）。
- 不启动、不配置协议端和 QQ；找目录只读进程、端口、注册表、文件。

## 挂载顺序

main.ts（私有存档） 里 onebot → qq → router → qq-local → qq-health → qq-console（前面是 settings、maintenance，完整顺序见[运行时](runtime.md)）。

- qq 排在 onebot 后面：构造时先用 `ctx.onebot.list()` 把已有的号状态接过来，再听 `onebot/state`、`onebot/event`。
- qq 排在 router 前面：`turn/should-respond` 里 qq 的判断先于路由的。
- **通道不通、连着发不出去时先不生成**（2026-10-22，审计 F）：`qq.canSend(账号)`——协议端没连上不行；一个号连着 3 次整条没发出去，暂停 60 秒，之后放一轮过去试（发成清零、又失败再暂停）。`turn/should-respond` 里 QQ 这边在真要往外发的回合（私聊，或"说话"档的群）先问它，不通就给理由、不调模型，免得白生成一堆发不出去的话、或者让她自己编"试通道"的话；这句话已经存下，通道好了以后她的下一句话再回。qq-health 启动时补查一遍各号现在的状态，启动前就连不上的也会在断久以后提醒。
- qq-local 的 `autoDetect` 只在配了号、且没设 `SUMIKA_DATA_DIR`（不是测试 / 预览）时开；qq-health 的日志目录传的是函数 `() => qqLocal.logDir()`，设置里改了目录不用重启。
- 没配 `qq.accounts` 就什么都不连，但四个插件照样挂上（qq-console 的接口还在）。

## 表

都在 `events.db`（和 `events`、`observations`、`blobs` 同一个库），由各插件启动时 `CREATE TABLE IF NOT EXISTS` 建。

| 表 | 谁建 | 内容 |
|---|---|---|
| `qq_msgs` | qq | `(conversation, message_id)` → `event_seq`，主键。去重、引用和撤回找原消息、发出去的消息号对回回复都靠它。她发的一条回复拆成几条发，每一条的消息号都指向同一个 `event_seq` |
| `qq_people` | qq | 名片簿：`uid` 主键、`nickname`、`cards_json`（群号 → 群名片）、`aliases_json`（曾用名）、`last_seen` |
| `qq_stickers` | qq | 见过的表情包的内容哈希。没被协议端标成表情包的图，哈希在这里就当表情包 |
| `qq_groups` | qq | 群号 → 群名。每次连上问一次协议端（`get_group_info`） |
| `qq_sync` | qq | 每个号"连续收到哪儿为止"：`account` → `through`（毫秒）。补录的起点之一，见下面"补录" |
| `qq_forwards` | qq | 合并转发的完整内容：转发 id → 整棵树 JSON |
| `qq_selftest_runs` | qq | 收发自检每次一行：`account`、`peer`、`app`、起止时间、`log_json`、`sent_json`；`ended_at` 为空的是正在进行的 |
| `qq_alerts` | qq-health | 提醒记录：`at`、`kind`（down / up / kicked / mismatch / send-failed）、`account`、`text`、`detail` |
| `qq_read` | qq-console | 会话 → 主人在主窗口里看到哪个时间点。未读数从这里算，和 QQ 客户端的已读无关 |
| `qq_avatars` | qq-console | 头像、卡片图片的代取缓存：`key` → 内容哈希、取回时间 |

另外 qq 还建了索引 `ix_events_conv_time (conversation, created_at, seq)`：QQ 会话按消息自己的时间排（补录、往上翻拉回来的旧消息 `seq` 比新消息大）。会话列表靠它按会话取最后一条；store 建的 `ix_obs_seq (event_seq, character_id)` 给"哪些角色看到过"按消息对上。

QQ 的消息就是 `events` 里 `channel = 'qq'` 的行：会话 id `qq:group:<群号>`（如 `qq:group:30001`）或 `qq:private:<她的号>:<对方号>`（如 `qq:private:10001:…`，带上她的号，主人同时私聊两个号是两个会话，`convOf` 生成）。`segments_json` 是 `{ segs, line, forwardImgs?, seen?, backfill?, selftest?, unsent?, partial?, aborted?, original? }`（`original` 只在她的回复上：发送时去了句尾句号，这里留去句号之前的那句（剥过句尾乱码、清过"】【"），见"发消息"；`line` 是收消息时排好、转义过的给模型看的一行（`lineOf`，身份按号认：主人 / 你的号 / 角色 / 群友 / 好友）；带合并转发的，后面接着展开的几行（`forwardBody`，以半角"[转发"开头，`forwardImgs` 是展开里写进占位的图，ADR-093、规格-合并转发第四节，细节见下面"合并转发"一节），agent 重建对话原样用，没有的以前的消息由 `util/event.ts` 的 `saidBy` 把原文转义了兜底；规格-群聊闸门 10.2。`seen` 是她看这一批时识图把"[图片]""[表情包]"换成带描述的那一行（`vision.see`，规格-识图第五节；识图看出了用法的，描述后面接"；用法：…"（`imageTag`，2026-10-19 起，以前存下的 `seen` 不改）；没看清的等看完再存，每轮最多等 `SEEN_WAIT.ms`（10 分钟）、最多 6 轮，等够了还没结果就不存；后台重启时没存下来的，启动后 `sweepSeen` 补存一次：最近 7 天、图都有结果的，她当时没看过的行（补录、补看的历史）也一起补上），`saidBy` 有它就用它，重建对话、整理记忆读到的和她当时看到的一样），通知是 `{ notice, sub, raw, likes?, add? }`。图片、视频走 `store.putBlob`（`blobs` / `blob_refs`，属主 `qq-msg`），段里只记哈希、尺寸、类型。

## 事件

| 事件 | 时机 |
|---|---|
| `qq/state(account)` | 某个号的状态变了（来自 `onebot/state`）。qq-health 和主窗口听 |
| `qq/message(event, info)` | 一条消息入库后。`info` 有 account、character、group、peer、stranger、backfill、fromSelf、mentioned、selftest。主窗口听它（广播成 `qq-event`）；回复与否走 `message/inbound` 的 `tags`，不看这个；识图听的是下一行的 `message/stored` |
| `message/stored(event, info)` | 平台无关的那一份（ADR-141），紧跟 `qq/message` 发；`info` 只有 backfill、stranger、fromSelf、selftest。识图（vision）听它；转发里的图由识图 `ctx.get(event.channel).forwardImages(id)` 来取（`PlatformLookup`，`api/channel.ts`） |
| `qq/event(event)` | 会话里多了或改了一条：她的回复（发送中）、"没发出去"标记、通知、补下载完成。主窗口跟着刷新 |
| `qq/send-failed` | 一条回复重试后仍没发出去。qq-health 记一笔；一个号连着失败的**第一条弹系统通知**，之后连着失败的只记进 `qq_alerts`（"连续第 N 条"）、不再弹 |
| `qq/send-ok` | 连着发不出去以后，一整条发成了。qq-health 听它解除并弹一次"发消息恢复了"（之前没发出去的不会自动补发） |
| `qq/kicked` | 收到 `bot_offline` 或 `kicked_offline` 通知。qq-health 单独提醒 |
| `qq/selftest(state)` | 自检开始、每记一项、结束 |

它自己挂的回合事件链：`turn/should-respond`（第 2 站）、`message/outbound`（第 6 站，只处理 `channel === 'qq'`）、`message/unsent`。

## 收消息：从协议端到交给路由

`onebot/event` 进来后在 qq 的**同一个队列**里一件一件处理（`enqueue`，p-queue `concurrency: 1`，所有号共用，下载图片是异步的，不能乱序，两个号推同一条群消息时后到的一定看得到先到的）。后台关掉（`live = false`）后排着的事不再处理（`queue.clear()`）；`idle()` 是 `queue.onIdle()`。`post_type` 为 `message` / `message_sent` 的走 `onMessage`，`notice` 的走 `onNotice`，其余忽略。

`onMessage` 的顺序：

1. 群消息不在 `qq.groups` 里：直接丢，完全不收。
2. 算出会话、对方、`role`（`qq.owner` → 主人，`qq.accounts` 里的号 → 对应角色，其余 → 群友 / 朋友 `qq:<号>`）。`fromSelf` = 发送者是她的号，或 `message_sent`；私聊的对方在 `fromSelf` 时取 `target_id`。
3. 算 `stranger`（私聊、不是她自己、是普通人、不在 `qq.privateAllow`）和 `selftest`（自检中，这个号和主人的私聊）。`observers` = 陌生人或自检时为空，否则是这个号的角色。
4. 去重：`qq_msgs` 里已有这条（另一个号推过、补录和实时重叠、自己发的回声）：只给 `observers` 补一条"看到了"，然后结束。
5. 更新名片簿；`normalize` 成段。她的号发出、内容对得上 Sumika 刚发的一段：正在发的（`inflight`，协议端的回声可能比回包先到），或者超时没确定、5 分钟内的（`maybeSent`）：不另存，映射到那条回复（`claimEcho`）。
6. 算 `origin`（见下一节）。群消息里被 @ 的人名片簿还没有名字：先问一次协议端，存的文字里 @ 的是群名片，不是号。
7. 下载图片、视频（`fetchMedia`）；有合并转发的，取完整内容并在段上记条数和预览。
8. 找引用的是哪条（`reply_to`）；`mentioned` = @ 了她的号、@ 全体，或引用了她自己的话。
9. `appendEvent`：`text` 是 `plainText`（@ 写成名片簿里的名字），`created_at` 用消息自己的发送时间；写 `qq_msgs`。
10. 自检中：把收到的各项记进自检结果。
11. 发 `qq/message` 和 `message/stored`，再 `ctx.parallel('message/inbound', …)`（不在队列里等生成，出错只记一笔）：`channel: 'qq'`、`scene`（群 / 私聊）、`fromName`、`mentioned`、`tags`（`backfill` / `selftest` / `stranger`）、`fullMemory`（私聊、不是她自己、号在 `qq.memoryAll` 里）、`platformPrompt: 'platform-qq'`、`asSent: dropTrailingPeriods`（她的话发到 QQ 的样子，见"发消息"）。

路由（见[回合循环与主窗口](agent-and-console.md)）只接私聊里 `origin` 为 `owner` / `peer` 的话；`tags` 里有 `backfill` / `selftest` / `stranger` 的在第 2 站被拦下，照存不回。`mentioned` 现在路由没用上，是给以后的群聊闸门留的。

## 谁说的（origin）和消息状态

`origin` 由写入的一方在代码里标（类型见 api/event.ts（私有存档））。QQ 这边：

| origin | 谁 |
|---|---|
| `owner` | 主人 |
| `peer` | 群友、私聊的朋友（包括陌生人） |
| `character` | 她自己的回复（回合发的，由 agent 写） |
| `human` | 她的号发出、又不在 Sumika 发送记录里的，实时收到的：真人登录她的号手动发的 |
| `external` | 同上但是补录来的、且早于"接管时刻"：以前别的程序（MaiBot）用这个号说的 |
| `automation:qq-selftest` | 自检时她发的测试消息 |
| `automation:qq` | 补录失败时 qq 自己写的"这里可能漏了消息"提示 |

"接管时刻"（`takeoverAt`）= 这个角色看到的第一条不是补录来的 QQ 事件的时间；还没有实时收过消息时，补录到的一律记 `external`。`human` / `external` 在给模型看时会加旁注"从你的号发出去的，但不是你说的"（agent.ts（私有存档） `toMessage`）。别人说的话给模型看的都是转义过的一行（`segments.line`，路由把它作为 `said` 交给 agent；带图的先等识图最多 8 秒，用补好描述的那一行），原文 `text` 只拿来检索。提示词注入测试题：`bun scripts/check-injection.ts`（真实模型、临时目录、什么都不发，结果存 `data/dev/injection/`；有一道发真的带字的图，经过识图）。启动时 `migrateOrigin` 会把旧库里的 `notMine`、自检标记迁成 `origin`（只改还是初值的行，重跑不变）。

消息状态一览（规格里那张表的现状）：

| 状态 | 存在哪 | 现在的效果 |
|---|---|---|
| 补录 | `segments.backfill`；`tags` 含 `backfill` | 不回；其余照常 |
| 自检 | `segments.selftest`；不记"看到了" | 不回、不进记忆 |
| 陌生人 | 不记"看到了"；`tags` 含 `stranger` | 不回；存原文，主窗口看得到 |
| 没发出去 | `segments.unsent`（理由） | 主窗口显示"没发出去"；在她的历史里还在 |
| 说到一半被打断 | `segments.aborted`（agent 写） | 路由不发，并标成"没发出去" |
| 撤回 | 一条 `kind: 'notice'`，`reply_to` 指向原消息 | 原消息本地还留着，主窗口标"已撤回" |

按这些状态过滤"回溯学习"还没做（现在没有这个功能），做的时候第一件事是照规格的表写过滤并加测试。

## 认人和名片簿

- 身份认号，不认名字：`role()` 只看号和配置。名字只是显示线索。
- `qq_people` 由 `remember` 写：消息带的 `sender.nickname`、群名片（`card`）按群记；昵称或某个群的名片变了，旧的记进 `aliases_json`；`last_seen` 取最大值。`ensureMember` 在名片簿里没有这个人在这个群的名字（只被 @ 过、通知里出现过）时问协议端一次（`get_group_member_info`），同一人同一群每次运行只问一次，问不到就显示号。
- 显示名 `nameOf(uid, gid)`：这个群的群名片 > 昵称 > 号。存进 `events.text` 的 @ 是当时的名字，之后改名不回头改。
- 连上时 `fetchGroupNames` 对每个白名单群取群名，并把她自己和主人在这个群的名片取好。

## 媒体和表情

下载与存储（`fetchMedia`）：

- 图片上限 20 MB、视频上限 50 MB，超过不下，段上记 `skipped: 'too-big'`；下载失败 `failed`；QQ 服务器上已经删了（404 且正文含 expired）`expired`，换链接也拿不到，不重试。每次下载 20 秒超时。
- 下载后看内容认格式（util/sniff.ts（私有存档），不看扩展名），记 `mime`、是否动图（GIF 多帧、APNG、WebP 动画位）、宽高（先占位用）；图片后面藏了压缩包（图种）的记 `hidden`，不解压。按内容哈希存，临时链接不留。
- 视频收到就下（下载地址带临时密钥会过期）；语音（`record`）、文件（`file`）只记元数据，不下载。
- 两个兜底：商城表情 SnowLuma 一律给 `.gif`，静态的只有 `.png`，下不到换 `.png` 再试；合并转发里套着的群图 SnowLuma 给的是私聊图地址，服务器回"已过期"，文件名是 32 位十六进制（图片的 MD5）时，按群图的老地址再取一次。
- 没下载下来的，每次连上做一遍 `repairMedia`：向协议端 `get_msg` 要新链接再下一次，同一条每次运行只试一次，按同类段的先后对上。

**表情包怎么判断**（`normalize`，照 MaiBot 的统一 QQ 连接器）：图片段的 `sub_type` 有值且不是 0、4、9，或者 `summary` 有字且不是"[图片]"，就是表情包（`sticker`）。商城表情单独认（`market`）：地址含 `gxh.vip.qq.com/club/item/parcel`，或文件名是"两位-32 位哈希"开头；它同时算表情包。协议端没标的图，下载后哈希在 `qq_stickers` 里就补标 `sticker`；标了的哈希写进 `qq_stickers`。blob 的 `kind` 记 `sticker` 或 `image`。商城表情后面 SnowLuma 跟的同名文字（"[好耶]"）不重复存。

**其余表情段**：

- `face`：小黄脸，记编号；段里有 `resultId` 或 `sub_type` 为 3 的是超级表情（`super`，骰子、猜拳的结果也在这）。
- `mface`：NapCat 一类协议端推的商城表情段（`emoji_id`、包号、名字）。SnowLuma 把商城表情推成图片，所以那边看到的是带 `market` 的 `image`。给模型看的那一行里，mface 写成 `[商城表情：名字]`，商城图写成 `[表情包：名字]`（识图按占位找图，两个同名就会撞车，2026-10-19 审查 #14）。
- 存进 `text` 列的占位：`[图片]`、`[表情包]`、`[商城表情：名字]`、`[表情]`（小黄脸）、`[名字]`（mface）、`[语音]`、`[视频]`、`[文件：名]`、`[合并转发 N 条]`（树上记着截断的写成 `[合并转发 N 条（还有 M 条没取下来）]`）、`[卡片：…]`。模型看到的私聊历史就是这个 `text`。

**小黄脸的图**在 qq-local 里：见"本机目录和小黄脸"。主窗口按 `GET /api/qq/face/:id` 取图，名字表走 `GET /api/qq/faces`。

## 合并转发（下面是 Bun 历史；Go 现状见本节末）

收到带 `forward` 段的消息就取一次完整内容（`forward`，调 `get_forward_msg`）：里面的图片、视频照常下载；套着的转发再往里取，最多 10 层、一棵树最多 200 条（预算用完不再往里取、不存空树，截断条数记在 `truncated`；Bun 按 ADR-093 不取陌生人的，Go 按 ADR-244 自动取陌生人的树和图）。整棵树存进 `qq_forwards`，过期后仍能看。Bun 的里层重取走 `forwardViaOuter`：先重取外层补协议端内存缓存，再取里层；同一外层 10 分钟一次。消息摘要按同一份树计算条数、缺条数和前三行预览。

**给她看的展开**（`forwardBody`，qq/segments.ts（私有存档）；规格-合并转发第四节，ADR-093）：收到消息、排她那一行（`lineOf`）时，把存着的树展开成几行接在那一行后面，一起存进 `segments_json.line`，以后重建对话原样用（前缀缓存不断）。

- 样子：先一行"[转发] 共 N 条，M 个人，时间范围"（有没取下来的写"N 条（还有 K 条没取下来）"），然后每条"[转发 1] 名字(身份)：内容"；套着的转发往里展开，编号"[转发 3.1]"；同一个人连着说的几句并成一行"[转发 5～7]"，句子之间用" / "隔开；图先写"[图片]"，她看这一批时识图补成"[图片：描述]"（[规格-识图](../规格-识图.md)第五节）。取不到的写"[转发] 内容没取到"，套着的那层取不到写"[合并转发（没取到）]"。
- 身份按号认（`lineOf` 里的 `who`）：主人的号"(主人)"，她自己的号"(你的号)"，别的角色的号"(角色)"，其余"(别人)"。一棵树里只有一个号的（转发里的号常常是同一个占位号）谁都写"(别人)"，人数按名字数。
- 上限（`FORWARD_VIEW`，一条消息里所有转发合起来算）：30 行（含第一行和最后的"后面还有"）、1500 字（文字、@、卡片标题、文件名都算，一条最多 300 字）、16 张图（多的写"[还有 N 张图]"）、展开 3 层（根算第 1 层，更深的只写"[合并转发 N 条]"；取树本身可到 10 层）。超了最后一行写"[转发] 后面还有 N 条没写（其中 M 张图）"。
- 防伪造：展开的行都以半角"[转发"开头，群友（包括转发里的人）打的方括号都转成全角、换行变成"换行 + 两个空格"，伪造不出这样的行。
- 记忆总结不读展开：`memory.ts` 的 `transcriptOf` 用 `withoutForwardBody` 去掉这些行，只留"[合并转发 N 条]"（转发是别人转来的话，不当成这个群里有人说了这些）。`text` 列不改，学说法读的是它，本来就没有展开。
- 图：展开里写进占位的图（`forwardImgs`）收到时排识图的普通队列，树里其余的走慢队列；`forwardImages(id)` 按存着的树深度优先找图（最多 10 层），补看清单也用它。

### Go 侧合并转发（2026-11-02）

可选适配器能力 types.ForwardReader，SnowLuma 的 get_forward_msg 复用 query（一个在途、隔 2 秒、45 秒超时、先核验连接）。forwardfetch 单工人异步取树，OnStored 只入队；QQ 已准入的新消息统一取，包括陌生人、自检、补录和自己的号，不看 Observed。qq.forward.fetch 默认 true；工人启动不补扫，首次连接和每次重连成功（StatusConnected）后补扫近 7 天，最多 50 个缺树资源。10 层、总计 200 条、顶层 5 分钟，预算用完不存空层；里层 payload_empty 按 Via 先取外层再重试，同一外层 10 分钟一次、最多 4 层。

里层单独重取先扣授权根内目标子树之外已存的节点和图片（包含已下载图），起始深度计入 Via；节点和图片额度不会随点击重置。

每层存 qq_forwards；同 id 较少或同样条数的回包不覆盖已有树，保留本地 hash。每页用一条参数化 IN 查询取前 3 个节点、条数和名字，desktopread 按 (id,fetched_at) 缓存最多 2000 个摘要。api.ForwardReader.ReadForward 纯读；FetchForward 走独立 api.ForwardActions，先验证 Source → Via → ID 的每一环。MediaRef.Forward 同样从可见事件逐层授权，hash 自己不能读任意图。

forward_media_fetch 是 QQ 平台迁移 0004 的新增表：主键 (forward_id,node,idx)，另带 conversation 供提交通知；0005 增加 width、height、animated（默认 0）。取层后按根预算最多登记 60 张，超出记 forward_limit；树与任务同事务提交，提交后才唤醒下载，重启补扫可恢复。相同 URL 指纹保留终局状态、次数、下次时刻、错误和元数据，新任务或 URL 改变才初始化并清掉旧尺寸；failed 的额外一次只由补扫触发。图片、表情和卡片封面复用 mediafetch 的下载器、白名单、重试及媒体库；成功后调用与正文相同的 imageMeta，元数据和 hash、状态同事务写入。回填核对种类、URL 指纹和 hash 为空，提交后刷新界面。启动元数据补扫先扫正文，再用 (forward_id,node,idx) 游标扫转发 done 且缺尺寸的行，每页 64 张、每张间隔 50 ms；缺文件或坏头继续前进，一页一次提交、按会话合并通知。ForwardMediaRows 批量读这一层，desktopread 复用正文 displaySegments 按 hash 投影；读树不打开媒体文件。archive 不造工人，没表或没有尺寸列的旧库仍可纯读。关机先停 intake、forwardfetch，再收 mediafetch、关库。

video 段形式的转发视频仍不自动下载；文件扩展后，`PutForward` 与树同事务登记 file 任务，`OfferForward` 按 ADR-261 调度文件，视频文件下好后共用播放准备。转发 video/file 都可经桌面动作手动下载，`forward_media_fetch` 已有封面和时长。`PrepareVideo` 逐层核对 Forward，只接受终点 video 自身 hash 或下载表核对过的 file 视频 hash；图片、卡片及封面不能冒充视频。接口详见下方「媒体文件下载」。

桌面气泡是标题、前三行预览、条数的聊天记录卡片；查看层复用正文、图组、看大图、表情、引用预览、链接和分享卡片。后台把节点内容拆为每行最多 8 块、图片组每 20 张拆块的虚拟行（我定的），ui.List 只构建可见范围。栈最多 10 层，环不压栈，每层保留 ListState；取消同步结束该层 busy、保留 dirty，请求代次拒绝旧结果，返回父层补积压刷新。退场 200 ms 内标题和节点保留。角色上下文和记忆的转发仍只写 [合并转发]，不给角色展开等主人定。本次只在编造帧、临时库和 Tester 上验收，不连接真实 QQ。

转发窗口的纯图片沿用正文的无外壳布局，图片自己带边框；引用单独套壳，图文保持正文气泡。竖图和横图第一帧按 DTO 宽高占位，位图到达不改框；验收数值见[转发规格 3.7](../规格-链接和合并转发.md#37-转发里的图)。
转发动图与正文共用 `imageStore` 的解码、像素预算、帧推进和减少动效规则，但按当前层 ListState 判断可见范围，离屏暂停；查看窗口独立持有缓存，退场 200 ms 后取消在途读取并释放，正文缓存不受影响。点面板外、Esc、Alt+← 和返回按钮都只退一层，最外层才关闭；关闭按钮关闭整个查看窗口。面板接住按下，点正文不返回；出栈清掉旧层指针。面板入场只淡入，不缩放重采样整块内容；量测与验收见[规格 3.2](../规格-链接和合并转发.md#32-查看窗口)和 `native/forward_lifecycle_test.go`，只用编造转发和 GIF。

QQ 引用的 `QuoteView.Segments` 来自可见原消息，页外引用里的 @ 与正文共用一次批量查名。界面共用 `atText`：群名片/昵称优先，未知号用 `config.MaskID`，all 写全体成员，非法 target 写某人；QQ 会话最后一句也用展示段，旧无段引用和转发节点 raw 引用中的 `@数字` 打码。复制沿显示块取字。没有新增聊天系统通知；已有运行提醒沿用 `maskDigits`。本地私聊、本地群聊的引用文字不变。


## 通知

只有这些通知会存成事件（`kind: 'notice'`），其余一律不记：

- 群（只限白名单群）：表情回应（`group_msg_emoji_like`，`reply_to` 指向被回应的消息，段里记 `likes` 和 `add`）、撤回、进群、退群 / 被踢、禁言和解除（全员禁言协议端发 `user_id` 0，算操作人发的）、改群名片、戳一戳。
- 私聊：撤回（原消息找到才存）、戳一戳。自检期间（这个号正在自检）主人撤回、戳她只记进自检结果；她自己撤回、她戳主人的回声不算；自检期间的戳一戳不存成事件，撤回的通知照存但标 `selftest`、不记"看到了"。
- `bot_offline`、`kicked_offline` 不存，发 `qq/kicked`。

通知都记成这个号的角色"看到了"（含陌生人私聊里的撤回和戳一戳）；`origin` 按操作人算（她的号触发的通知是真人在 QQ 里操作的，记 `human`）。

## 发消息

路由整轮生成完后逐条走 `message/outbound`：记忆插件的守卫排最前（回复里原样出现了对方不该看到的记忆就拦下），qq 排最后，只处理自己的渠道。

- `sendOut`：私聊照发；群会话要档位是"说话"（问 `ctx.gate`）、群在白名单里，用她（`characterId`）配的那个号发，不然返回理由；先发 `qq/event` 让主窗口显示"发送中"，再 `send`。
- 句尾句号（`dropTrailingPeriods` / `dropTrailingPeriod`，ADR-110 第 1 条、ADR-114 第 2 条，补 ADR-047 第 9 条）：按 `splitMessage` 拆开，每一条末尾单独的一个"。"去掉（前面的空白、换行一起去："好\n。"→"好"），一条里用单换行隔开的几行，每行行尾单独的"。"也各去各的（"好的。\n明天见。"→"好的\n明天见"，CRLF 原样留着），不加随机；前面是数字的照样去（"我考了100。"→"我考了100"：全角"。"不是小数点）；整条（那一行）只有一个"。"、"。。""。。。"、末尾在引号或括号里的（"。」""。）"）、跨行的引号括号里那几行（"他说「好。\n我知道了。」"两行都不动；引号括号按整段配，空行拆开的两条也算，后面没配上的左括号不算，颜文字的":("、"（小声"后面几行照常去）、句中的不动；有去掉的就把几条用空行连起来，没有就原样；去到不再变为止（"好。\n。"→"好"），去过的再去一遍不变。同一个函数用在两处：① 收消息时放进 `message/inbound` 的 `asSent`，路由交给 agent，agent 存她的回复之前改在消息对象上（和"】【"一样，和剥句尾乱码、清"】【"一起循环到不再变，见 [agent-and-console](agent-and-console.md)），所以库里的（真发的、试运行"她会说"的）、她实例历史里的、主窗口看到的是同一句，第一次去句号之前的那句（剥过乱码、清过"】【"）记进 `segments.original`；② `sendOut` 发之前再过一遍兜底（没经过 agent 的），还去掉了什么就把库里那条（`events.text`）改过来、记 `original`（已经有了就不盖，`json_insert`），再发"发送中"；回声认领（`inflight` / `maybeSent` 记的是去过的文字）对得上。"[不说] 为什么"不改。主窗口自己的会话（ADR-051、053）没有 `asSent`，不去。
- 拆条（[规格-发送前拆条](../规格-发送前拆条.md)）：`asSentQQ` = 先去句尾句号，空行分开的每一条分别交给 `qq/split.ts` 的 `splitRound` 按标点拆（整段最多 3 条、每条至少 4 词、该拆的按 0.7 的概率拆、随机数由这句话定；≥30 字的一条话、或模型已分条而这一条 ≥20 字的不掷骰子，ADR-129），一直拆到不再变，用空行接起来；条里有单个换行（特意排的多行）的不动。收消息时给 agent 的 `asSent` 和 `sendOut` 兜底都用它（替代原来直接用 `dropTrailingPeriods`），再过一遍不变。客服腔计数（`auditOutbound`）逐条匹配说法前把空行隔开的几条接回去。
- `send`：整段话按空行拆成几条（`splitMessage`，单段超过 1500 字再按换行、句号拆）；条与条之间停 `字数 × 40 ms`，最多 2.5 秒；每条最多试 3 次（即重试 2 次），间隔 1 秒。动作按协议端画像：SnowLuma 用 `send_msg`（`message_type: 'private'` 或 `'group'`），别的用 `send_private_msg` / `send_group_msg`。发出去的消息号写进 `qq_msgs`（推回来的回声按它去重）。超时、不知道发没发的那一段记进 `maybeSent`（会话、文字、第几段、一共几段、过期时间）：5 分钟（`UNSURE_ECHO_MS`）内她号上对得上的消息认成它的回声，映射进 `qq_msgs`、不另存一条"不是她说的"；那段是整条回复的最后一段的，去掉"没发出去"和"发了一半"（`markSent`，发 `qq/event` 让主窗口刷新）。过期了就不认，照常存。后台关掉时清空。回声在回包之前（超时之前）就到了的，那一段记作发出去了，接着发后面的，不标"没发出去"。回的是失败，但回声在重试间隔里到了的，也记作发出去了，不再重发（sleep 之后 `if (flight.echo) break`，2026-10-19 审查 #20）。
- 客服腔和"作为 AI"只记数、不拦（ADR-110 第 2 条，util/outbound-audit.ts（私有存档））：2026-10-23 起不在 QQ 插件里记，由路由（`router.ts` 的 `audit`）在 `message/outbound` 整条发出去以后统一记，按库里那一条现在的字（也就是去过句尾句号的）算；试运行"她会说"的记成 dry；主窗口的本地聊天不记。
- 失败：`markUnsent` 在事件上记 `segments.unsent`，发 `qq/send-failed`；一条失败后面的不发。被守卫拦下、说到一半被打断的，路由发 `message/unsent`，qq 同样标"没发出去"。
- 后台关掉的瞬间还在发的：剩下的不发，也不碰已关的库。

## 主人 QQ 私聊审批（Go，默认关）

配置 `qq.remoteApproval.enabled`（默认 `false`）、`qq.remoteApproval.awayAfterSeconds`（默认 60，0～600），设置页“平台账号”能改，保存后立即生效。规格见[规格-主人QQ私聊审批](../规格-主人QQ私聊审批.md)。

实现分三层：`internal/approvalrelay`（解析回复、发卡、票、墓碑、选单，身份和配置绑定都由宿主注入）→ `internal/app/remoteapproval.go`（现读配置的绑定、走自动化出口 `remote-approval` 发卡、等计划终态才算送达、被截走的回复行改标来源）→ `channel.Pipeline` 的 `Control` 钩子（`OnStored` 最前面，返回 true 就不进观察、拦截链、闸门和模型）。卡片和结局的来源是 `automation:remote-approval`，在 `history.NotSaidSQL` 里，所以模型历史、自动行、压缩、记忆证据都看不到，界面照常显示。出口档位对这个来源只认绑定里的主人私聊，且仍过 `qqSendMode` 的发送启用条件；不过守卫（同 `linkOrigin` 的写法，只发主人）。

## `qq.reply` 开关

配置 `qq.reply`（默认 `false`，ADR-037）。qq 在第 2 站对 `channel === 'qq'` 且 `config.reply === false` 返回"QQ 私聊自动回复关着"，所以 `agent.respond` 一开头就不接，不调模型，也不会走到收尾那一站，**记忆整理不会被 QQ 私聊触发**。消息照收照存、照记"看到了"。同一处还拦下 SnowLuma 自己的状态指令（`#sl` 开头，`isProtocolCommand`），协议端自己会回。

该键不在设置页的白名单里，要改 `sumika.yml` 再重启。代码里没有这个键（测试里）等于开。

### Go 侧：插件、叫名与不接话名单（2026-11-03，feat/bot-wiring）

[app/channel.go](../../native/internal/app/channel.go) 正式装配消费 `plugins.sessions`、`characters.<角色>.aliases`、`reply.deny`；配置来源和设置展示见 [运行时](runtime.md)。群里的复读、戳回去默认关；QQ 私聊戳回去仍须 `qq.reply` 开、对方是主人、发送启用条件齐全，允许按完整私聊 ID 单独关闭。

[Roster.CanTrigger](../../native/internal/channel/roster.go) 按平台身份查三类名单，流水线在入库和观察之后阻止这些号触发插件、链接观察者、闸门及私聊回复。闸门重读批次不把名单消息标成 Addressed，但保留它们的原文上下文；复读历史不让它们凑人数，戳回去同样读取此规则。叫名在拦截链之前统一计算，只比当前 text 段，短英文数字按词边界，复读让路。本地私聊不走此回复入口，本地群聊没有新接收入口，QQ 名单不跨到本地身份。具体契约、边界表和验证入口见 [渠道中间件第七节](../规格-渠道中间件.md#七拦截链处理掉就停)。


### Go 侧：收图下载（2026-11-02，[规格-QQ收图下载和断线补录](../规格-QQ收图下载和断线补录.md) 第四节，分支 qq-media-backfill 上做完、未合 main）

- 排队：接收模式下，消息入库以后入库一站的回调（`app/media.go` 的 `withMedia`）把它的 image / emoji 段交给 `mediafetch.Fetcher.Offer`，只排队不碰库不联网，不挡入库和回复。只管 QQ、新存的（重复到达的不重复交）、有 URL 且没 Hash 的图片段；陌生人私聊、自检、补录和她自己的号发的图照下，不看角色是否观察（2026-11-02 用户，ADR-243：“QQ什么样就什么样，那边会下那这边也会下，不下她没法理解图”）。准入和观察写入照旧，非白名单群整条不存；本地私聊、本地群聊不涉及。
- 下载（`native/internal/mediafetch`，2 个工人）：只认 `multimedia.nt.qq.com.cn`、`gchat.qpic.cn`、`p.qpic.cn`、`gxh.vip.qq.com`、`qq.ugcimg.cn`、`tianquan.gtimg.cn` 和 `*.qpic.cn`、`*.nt.qq.com.cn`、`*.ftn.qq.com`（QQ 文件中转，2026-11-06 补）；域名解析到不是外网的地址（回环、链路本地、私网、CGNAT、保留段、NAT64 等，`netsafe.PublicIP`，和链接取数同一份）的不连（代理的假地址段放行（读 `linkReader.fakeIPRanges`，默认含 IPv4 198.18.0.0/15 和 IPv6 2001:2::/48、fdfe:dcba:9876::/48，ADR-392），代理的假地址）；跳转每一跳重新核对，最多 3 跳；单张 20 MB、一次请求 20 秒、建连 5 秒；看文件头只收位图（PNG、JPEG、GIF、WebP、BMP、AVIF、HEIC）。
- 存储：`mediastore.Put` 写进 `<数据>/go/media/qq/<hash 前两位>/<hash>`（临时文件在 `.tmp/`、边写边算 sha256、拒联接）；下载器只读头取得宽高和动图标记，`store.FinishSegHash` 在一个事务里往 `$.segs[idx].hash` 填 hash（别的字节不动），并写 `media_fetch` 的状态及 width/height/animated（chat/0007）。段里的 `url`、`file` 原样留着。
- hash 回填事务失败：文件保留，hash 与 done 状态一起回滚；仍满足候选条件的图由下次连接补扫重新下载，同内容只保留一个媒体文件。没有只补数据库的定时任务。
- 失败：网络错误、超时、5xx、429 共试 3 次（立即、30 秒后、5 分钟后）；400（QQ 图片服务器的地址过期就回 400）/ 403 / 404 / 410 先向 SnowLuma `get_msg`（适配器的 `Message`）换新地址，按文件名对段，对不上只有张数一样才按先后，其余不猜，协议端答了但换不出记 `expired`；没问成（没连上、超时、断线）不记，留给下一次补扫；商城表情 `.gif` 404 换 `.png`。状态在 `media_fetch`：`pending`（等重试）、`done`、`expired`、`too_big`、`not_image`、`blocked`、`failed`；日志和 `last_error` 只写事件号、状态、短原因和主机名，没有查询串。
- 补扫：Start 只起工人；首次连接和每次重连成功（StatusConnected）后扫近 7 天“有地址、没 hash、没有终局状态”的图片段，不查 observations，陌生人私聊、自检期间的图同样补下载；`failed` 的在事件 24 小时内再试一次；队列满了（500）丢掉的也靠它捡回来。旧 done 图片的元数据补扫仍在启动后独立运行，不查询协议端。
- 元数据补扫：`mediafetch.backfillMeta` 启动后独立扫 done 且缺宽高的行，每页 64 张，每张间隔 50 毫秒；`mediastore.OpenMedia` 不读全文校验 hash，缺文件跳过、不刷日志。补齐后通知已打开会话重读。`store.enrichDisplayMedia` 在展示查询事务里一次 IN 取整页，desktopread 按段下标及 hash 投影，legacy 优先；消息读取不碰文件，也不占 queryGate 补尺寸。GIF/WebP 的块扫描器由后台和界面共用。
- 开关：`qq.media.download`（默认 true）。关着不造下载器，archive 模式也不造。
- 没做：图片占位文字读取下载状态、voice 段下载、Go 侧识图。视频和 file 段下载见下一节，断线补录见后面「断线补录」一节。

### Go 侧：媒体文件下载（视频和文件，ADR-255、261）

- 实时和断线补录新入库的 video 段与图片共用 Offer/工人，按 ADR-255 固定 ≤100 MiB 自动下载；超限记 too_big、等手动，未知大小按实际流计数，超限丢半文件。谁发都下载，准入照旧。适配器保存合法 file/url/size/file_id；get_msg 刷新视频地址，图片族与视频分别配对。
- 配置：移除旧 `qq.media.videoMaxMB`，真实配置不改；继承 `qq.media.download` 开关，图片仍为 20 MiB。ffmpeg/ffprobe 路径默认空，先配置再 PATH，缺工具仍下载、仍能系统播放。
- 手动入口沿用并扩展独立可选 `api.VideoDownloadActions`：正文 video/file，以及可见事件逐层可达的转发节点 video/file。界面使用展示段 `Download` 凭据（Source、Index，转发另带 Forward 全路径及 Node）；三个动作均由 desktopread 核对，不收 URL 或裸文件 ID。最多 2 个手动任务，视频和文件共用限额，同段复用；字节进度只存内存、订阅合并；取消等临时文件清理，退订不取消，App 退出等工人。
- 下载后 ffprobe 取宽高/时长、ffmpeg 抽第一帧 JPEG，长边 640；程序单线程、低优先级、串行、单次 20 秒，不经 shell，输出封顶。chat/0008 加 cover_hash、duration_ms、size，和回填视频 hash 同事务；探测或抽帧失败保留原视频。
- 展示只批量查库，不打开文件。封面异步读取用现有图片缓存；无封面显示 16:9 深色块。点视频后台核对事件凭据、复制到 `.tmp/video-open/`，按真实容器加扩展名，系统播放器打开；副本不用硬链接，防播放器修改原件。启动清超过一天的播放副本，包括 archive。
- 文件按两档处理：扩展名和头部一致的文档、文本、视频、音频、图片、动图 ≤100 MiB 自动下；超限、压缩包、程序、未知、无扩展名、不一致均等手动。转发文件同规则；本次仍不自动下 video 段形式的转发视频，但能手动请求。文件保持 kind=file，只存媒体库，不自动打开或执行。`FileKind` 表示已核对种类，图 / 视频的 `Media` 复用 ReadMedia / PrepareVideo；图文件读取上限 100 MiB，普通图仍 20 MiB。
- 适配器新增 `FileURL`：正文群用 get_group_file_url，私聊用 get_private_file_url，file_hash 作为不透明令牌原样保存，只供取址，不展示、不记日志。缺址及地址过期走文件接口；转发节点明确给出的 group_id 保存为 file_group，用原群取址，缺原群时不猜接收群，有 file_hash 仅尝试私聊接口，缺凭据 / 被拒照实失败。SnowLuma get_file 只查图片 / 语音缓存，本次不调用。所有直链仍受原 HTTP 白名单、DNS / 内网、端口和重定向检查约束；实际 QQ 文件可能使用额外域名或 IP，未验证的仍 blocked。
- `media_fetch` / `forward_media_fetch` 新增 awaiting_manual 状态。chat/0010 保存 format；qq/0006 给转发结果加 format、size、cover_hash、duration_ms。文件回填绑定原 URL、文件名、ID、取址令牌，防迟到结果写错文件；转发的 hash 和元数据同事务提交。溢出补扫恢复本轮新 video/file seq 范围，不批量下旧正文；转发文件复用已有持久化队列。
- API、输入矩阵和真机待验接口见[收图规格第十三节](../规格-QQ收图下载和断线补录.md#十三视频和文件下载后台adr-255261)。剩下 MewTypeUI 第 4 步下载界面、窗口内视频播放、旧正文视频 / 文件补扫及真机直链核对。

### Go 侧：小程序和分享卡片（2026-11-02）

适配器解析完整 CardJSON，保存 card_title（120 rune）、card_description（300）、card_source（40）、card_url（仅 http(s)，拒凭据、控制字符和反斜杠）。清理外来文字；Raw 仍为 200 字预览。旧中立段尚未提取字段时，desktopread 从完整 CardJSON 走同一个 types.FillCardDisplay。URL / Hash 在 card 段代表封面，复用 mediafetch 的队列、补扫、限额、白名单和回填，不把封面当跳转链接。桌面照 chat.LinkPreview 的间距画标题、两行简介、来源和本地封面，整张点击打开默认浏览器；认不出标题仍显示 [卡片]。输入表和改坏记录见[规格 1.4](../规格-链接和合并转发.md#14-卡片版式2026-11-02-本次任务)。

桌面文字链接用 linkreader.FindURLs，最多 20 条、单条 2048 字节、单段扫描 64 KiB；正文保留原字，点击只开 http(s) 原链接。每条消息的主题右键菜单可复制链接或干净链接，多条一行一条，按 Clean 去重。追踪删除入口只有 tracking.go，第三轮平台表由它调用 tracking_more.go，桌面与取数共用；网易云 #/ 路由也清洗，未知站点的 s/t/ts/spm 保留。短链不展开。小黄脸与 ui.Link 混排走同一个 RichText 段落。

无 Segments 的消息也在 msgFromView 转换时缓存正文块和链接表；绘制只读缓存。演示流式文字在更新时重建块和链接，沿同一绘制契约。



### Go 侧：断线与手动补录（2026-11-04）

- 入口：receive 模式下 app/backfill.go 订阅 QQ 状态；等 Ready 最多 60 秒，就绪即由同一协调器补扫媒体 / 转发，历史再稳定 30 秒。自动账号轮次不重复补扫，手动账号轮次可请求一次；重复 connected 合并，没有 open 缺口时前后历史轮次至少隔 10 分钟。断线取消并等待旧轮退出，关闭在 intake 和数据库之前。
- 能力：HistoryPage 区分 Messages、Latest 和 Continuous；当前协议端不提供 Continuous。ADR-278 增加下述群 message_seq 证明，不能用本地末条、锚点重合或越过起点代替。真实字段及证据边界见协议端参考页（私有存档）。
- ADR-278 群序号证明：适配器从历史 `message_seq` 读取 `Inbound.MessageSeq`，只收正 int64。跨页按消息 ID 去重，序号唯一且无空号，已提交消息的首尾时间包住缺口才可关闭；缓存旧尾不能扩成当前时刻。私聊不使用此序号。字段缺失或无效不阻止消息入库。
- 证据失效：`conversation` 在所有分页退出后统一计算覆盖；任何页查询错误、缺请求锚点或跨页同消息时间 / 序号冲突，都会累计否决本轮证明。后页无效 / 重复序号即使直接 stalled，也不能沿用首页覆盖；有效消息仍照存。取消或入库失败返回错误，自动与手动入口均不结算缺口。
- 分页：QQ 群无锚点首页 100 条，以最旧新消息的 message_id + reverse_order 继续，最多 5 页；平台号去重，下一页必须含请求锚点且同号时间不变。同秒可换新号继续；重复页、只有锚点或只添较新消息记 stalled，断页记 broken-page。QQ 私聊仅无锚点首页 100 条，不按 message_seq 排序。起点之后的有效消息先按时间稳定排序，再 Submit(Backfill:true) 等入库。
- 范围：主人私聊优先、上轮 budget 缺口优先于其他到期缺口、再取最近活动的其他私聊最多 30 个、qq.groups 白名单群按活动排。陌生人私聊补存但不观察；白名单外群不查不存；本地私聊、本地群聊不涉及。补录带 backfill，不进拦截链、闸门或内核；图仍下载。旧于 5 分钟回声窗口的自己消息不认领当前回复。
- 同步点：qq_sync.through 只保存账号检查位置；qq_backfill_continuity 按账号 × 会话保存连续证明。SettleQQBackfill 统一结算整轮缺口、连续边界和检查位置；open / blocked / abandoned 均封住连续水位。手动单段和人工确认不推进这两类位置。群缺口账号归属尚未做；ADR-278 本次不做持久续扫。
- 旧 filled 升级：QQ 0007 首次引入连续证明时，将旧 filled 降为 blocked / unverified-history，自动 tries、last_try_ms 清零，手动冷却保留；同事务把所属账号检查位置退到最早旧 filled 起点，群因没有账号列而回退全部已有账号，原本更早的位置不动。迁移只执行一次，新版 filled 重开保留；不修改原始消息。此 0007 尚未发布，按任务授权直接修订，0001～0006 冻结。
- 限额：Ready 后先开启共享预算，再补扫媒体；历史和 get_msg 共用串行通道，结束后至少隔 2 秒，查询超时 45 秒；每轮共 120 次（含防抖期间图片换地址）。预算耗尽只算暂时问不到，图片不因此记 expired。
- 缺口（ADR-275 / 278）：群可用无空号且两端已包住的 message_seq 证明，目标消息提交后才可 filled。只有 open、以及迁移恢复的 last_try_ms=0 blocked 会自动扫；扫过未证实即 blocked，定时器不再重查。预算未扫到保持 open。新窗口跳过旧受阻段，单独登记新尾段，不与已扫描 blocked 合并；手动入口仍可用。RemainingOpen 统计 open / blocked 的未完成数，不表示会自动重试；abandoned 保留且不复活。
- 手动单段：受阻 / 旧 abandoned 均可请求；缺页锚点只撤销覆盖证明，有更旧新消息仍在原五页预算内继续。目标区间的有效消息排序、去重后按 Backfill:true 保存，不因无完整性证明而丢弃；私聊仍只支持一页。
- 人工确认：现有缺口服务实现 api.GapAcknowledgements.SetGapAcknowledged；只接受当前准入范围内的 blocked 原区间。QQ 0008 增加派生列 acknowledged_ms；非零代表已确认、停止提醒，false 可撤销。GapView 保留记录和确认时间，不算 filled、不推进任何位置、不挡手动补录；archive 旧列缺失投影 0。界面按钮以后接。
- 配置：qq.backfill.enabled、qq.backfill.groups，默认均 true；不提供 maxAge/since，不读发送交接记录。关闭只停补录，实时准入和回复权限照旧。
- 任务发布：WatchBackfill / WatchAlignment 只观察 `@alignment`，中间进度最多每 200 ms 合并发布；媒体工人独立通过 `@alignment-media` 唤醒统计，消息逐条提交不会绕过发布节奏。父账号轮次结束后仍保留最近单段终局供原订阅读取，持久表仍只有 active / last 两槽。
- 中断投影：取消或重启后，未完成的媒体排队 / 运行项转为 unchecked，保留已下载、过期等已证实结果；archive 只投影、不修写遗留 active。缓存转发统计复用既有十层 / 200 节点预算，不为统计拉取树。
- 媒体关联：正文 file 按近七天补扫；本轮新入库旧 file / video 直接排入现有下载器。转发 video / file 走同一任务设施。file awaiting_manual 和 video / file too_big 计等手动，expired 只消费下载器已有证据；预算占位不下载，缺树仍计未检查。

| 表 / 接口 | 位置 | 作用 |
|---|---|---|
| channel_gaps | store/migrations/chat/0009、0012；qq/0007、0008 | 派生的缺口和提醒状态；QQ 0007 恢复旧 filled，0008 增加独立 acknowledged_ms，可撤销，不影响水位 |
| qq_sync / qq_backfill_continuity | qq/0003、qq/0007_backfill_continuity.sql；store/backfill.go、backfill_settle.go | 前者仅为账号检查位置，升级时按旧 filled 回退；后者为账号 × 会话的连续证明区间，三类未完成缺口都封住边界，整轮原子结算 |
| QQActivities | store/backfill.go | 按平台时间取本账号私聊和群活动顺序 |
| ChannelGaps / RecordChannelGap / UpdateChannelGap | store/backfill.go | 仅合并待扫描区间；已扫描 blocked 保留原 ID / 范围且停止自动重查；UpdateChannelGap 只改 open、blocked 行，终局行和不存在的 id 返回 ErrChannelGapSettled（目前只有测试调用） |
| HistoryPage | channel/snowluma/query.go | 查询历史页、校验边界并翻译成统一消息；能力接口不改变 Adapter |
| ReadGaps / WatchGaps / ReadAlignmentGaps | api/backfill.go、desktopread/backfill.go | 基于实际已提交行按闭区间相交读取；后者供账号状态页 / ctl 分页，游标绑定读取范围 |
| RequestGapBackfill / RequestAlignment / WatchBackfill | app/backfill_tasks.go、api.BackfillActions | 同一协调器、共享查询预算；单段重读原区间，16 个待处理上限、持久 10 分钟手动冷却 |
| SetGapAcknowledged | api.GapAcknowledgements、app/backfill_ack.go、store/backfill_ack.go | 核对原 GapRef 后只改提醒确认时间，现有缺口接口可读可撤销，界面以后接 |
| WatchAlignment / AlignmentGapCounts / AlignmentMediaTargets | app/desktop_backfill.go、app/backfill_media.go、store/backfill_alignment.go、backfill_media.go | 消息阶段和近七天＋本轮新增媒体分开；不支持的新会话发现单列，不把未检查记零失败 |
| channel_alignment_latest | store/migrations/chat/0012_backfill_tasks.sql | 每账号最多 active / last 两槽；启动把遗留 active 标 canceled / interrupted，不逐页写快照 |
| ref_blocks | store/migrations/chat/0017（单库 events/0006 同表） | 渠道回合的参考信息块原字节，按（角色、会话、档、排在哪条事件前、序号）存；重建时同档、同卡、记忆核对过的才插回原位；交回时同事务清窗口起点之前的，记忆删改时清引用它的（规格-提示词前缀稳定第七节、7.9） |

2026-11-04 gap-backend 增加后台查询 / 手动任务 / 对齐快照与 ctl，实施证据见[补录缺口界面规格](../规格-补录缺口界面与自动对齐.md)第九节。已合入 main 的 ADR-261 file 两档设施并接入媒体对齐；第 2～4 步 MewTypeUI 服务消息与入口仍未接。

一次性修补：`scripts/fix-gap-rows-20261103.ts <qq.db> [--coverage <JSON>] [--yes]`，默认只读；只删除 2026-11-03 16:00 后本次新建的 unverified-head 重复行，原 outage/page-cap 可作保留行。完整覆盖证明或已有 filled 区间才可标 filled，事件 min/max 不作为分页证明；不改事件、水位和 abandoned。副本演练及证明缺失限制见规格 12.6。

## Bun 历史参考：掉线、补录、补下载、往上翻

连接、重连、心跳看门狗在适配器（私有存档）里。qq 听 `onebot/state`，**每次变成 online** 就排一个任务：`fetchGroupNames` → `backfill` → `repairMedia`。

- **补录**（`backfill`）：每次连上（包括刚启动）对每个白名单群（`get_group_msg_history`，一页 100 条）、这个号 `qq_msgs` 里有过记录的每个私聊（`get_friend_msg_history`，一页 50 条）补。起点取**"这个号连续收到哪儿为止"（`qq_sync`）和"这个会话本地最后一条"里早的那个**，都没有才用发现断线的时刻（`downSince`），再没有就不补。`qq_sync` 在这次连上后的补录没出错时记成补录开始的时刻，之后在线每分钟推到现在（`markSynced`）；后台没开、断线、补录出错时停着，下次从同一个地方再补。只看最后一条不够：2026-10-17 旧代码（当时从 `downSince` 补）漏了 23:5x～04:02，04:08 又实时收到一条，最后一条跳到洞后面，之后每次都从那往后补，洞再也补不上；补录出错后又实时收到消息也一样。`historySince` 一页一页往前翻（锚点写法同往上翻），直到翻过起点前 60 秒、或协议端没有更早的了，最多 `BACKFILL_PAGES` = 10 页；翻满了还没到起点，就在会话里写一条 `notice: 'gap'`"断线太久，只补了最近 N 条"。按时间从早到晚走 `onMessage(…, backfill = true)`，靠 `qq_msgs` 去重。补录的消息标 `backfill`，不触发回复。接口报错（协议端没有这个扩展）时在会话里写一条 `notice: 'gap'`（`origin = automation:qq`）"这里可能漏了消息"。
- **手动补一段**（`backfillSince`，`POST /api/qq/backfill`）：从给的时刻起向协议端要这个会话的历史（同样最多 10 页），按补录方式存、靠 `qq_msgs` 去重，重跑没关系。用第一个在线、看得到这个会话的号。2026-10-17 用它补了三个群 23:00 以后漏的 125、24、2 条。
- **往上翻**（`fetchOlder`，`POST /api/qq/history`）：本地翻到头后向协议端要这个会话更早的 100 条，从本地最早那条往前，按补录方式存。翻页锚点各家叫法不同：SnowLuma 是 `message_id` + `reverse_order`，NapCat 是 `message_seq`；传错名字协议端不报错，只是每次给最新的一批。返回新存了几条，0 就是协议端也没有更早的了。

## 掉线提醒（qq-health）

- 掉线（`offline`）：先等 `downAfterMs`（默认 30 秒），到点时这个号还没回来才提醒一次（弹 Windows 系统通知 + 记 `qq_alerts`，附协议端最近日志里和掉线有关的几行）；抖一下马上连回来的不吵。
- 恢复：只有之前提醒过掉线、被踢或号对不上的才发"恢复了"，附断了多久。
- 被踢（`qq/kicked`）：马上提醒，不等 30 秒。
- 号对不上（`mismatch`）：提醒一次，Sumika 不去改协议端。
- 发送失败（`qq/send-failed`）：只记进 `qq_alerts`，不弹系统通知。
- Go 版 SnowLuma 适配器（`native/internal/channel/snowluma/health.go`）：连接就绪以后看每一拍心跳里的 `status.good` 和 `online`，变假 Raise 一条"SnowLuma 连着但收不到 QQ 消息"（键 `qq:<号>:recv`），变回真 Clear；同一状态不重复提醒，心跳没有 status 或 status 里没有这两个字段的不当成坏，也不当成恢复。只提醒，不停发、不重启协议端，不另起轮询。以后看情况再加：bridge 那样每 60 秒调一次 `get_status` 兜底、心跳整段静默的告警（拆解笔记 7.2、11.10）。
- 协议端日志（`readProtocolLog`）：日志目录里最近改过的 `.log` / `.txt`，末尾 256 KB 中和掉线有关的最后 8 行，每行截到 300 字，先打码（logfile.ts（私有存档） 的 `redact`，再把令牌原值换成 `***`）。只读，不写不删。
- 系统通知用 util/notify.ts（私有存档）（Windows 自带的 PowerShell 调通知接口）。

## 收发自检

升级协议端或 QQ 以后逐项确认收发是否正常。只和主人（`qq.owner`）的私聊做，不往群里发。

- `startSelftest(account)`：要配了主人、这个号在线；已有一次进行中就返回那次。开始后这个号和主人的私聊：照存、标 `selftest`、不记"看到了"、她不回；她发的测试消息也一样（`origin = automation:qq-selftest`）。状态存在 `qq_selftest_runs`，重启后台后接着进行；`stopSelftest` 结束，什么都没测就结束的那次不留在历史里。
- 项目（`SELFTEST_ITEMS`）：能发能收的 7 项 —— 文字、图片、小黄脸、引用、合并转发、撤回、戳一戳；只测收的 4 项 —— 表情包、超级表情、商城表情、@（@ 只在群里有，要主人在白名单群里 @ 她）。
- 发（`selftestSend`）：合并转发依次试 `send_private_forward_msg`、`send_forward_msg`，戳一戳试 `friend_poke`、`send_poke`、`send_friend_poke`（各家叫法不同）；撤回是发一条、停 1.5 秒、`delete_msg`。每项的结果（成功与否、用了哪个动作、消息号）记进日志。
- 收：主人发来的每一条按 `recvItems` 算通过哪几项（图片没下载下来的不算通过）；撤回和戳一戳从通知里认。
- `selftestRuns` 取最近几次，用来对比升级前后。

## 本机目录和小黄脸（qq-local）

> 2026-11-14 核对：下面是旧 Bun 版 qq-local 的行为。Go 版没有任何代码读 `qqDir`、`qqDataDir`、`protocolDir`、`protocolLogDir`，设置注册表把它们标“尚未生效”、只读。

- 三个目录：QQ 安装目录（`qqDir`，认的标准是有 `QQ.exe` 和 `versions`）、协议端目录（`protocolDir`，找正在监听的进程（指定了口就先看这几个口）再往上找 SnowLuma 的目录；连接地址从它来，见下面的 `protocolDirNow`）、QQ 的数据目录（`qqDataDir`，Tencent Files，里面有以号命名且带 `nt_qq` 的文件夹）。找的实现在 util/locate.ts（私有存档）。
- 启动时（`autoDetect` 开着）：配置里的目录还在就用，不在了就重新找，找到写回 `sumika.yml`（`setConfigValue`）。设置 · 平台账号里可以重新识别（`detect`）或弹系统的文件夹选择窗口（`pick`，窗口在运行后台的这台电脑上），选的目录不对会拒绝并说明原因。
- 协议端日志目录：手写的 `protocolLogDir` 优先，否则是协议端目录下的 `logs`。
- 小黄脸的图（`syncFaces`）：从 QQ 安装目录最新版本的 `resources/app/resource/default-emojis` 复制按编号命名的 PNG 和名字表，写 `data/cache/qq/faces/index.json`，版本没变不重复复制。三步：安装目录 → 数据目录里登录后下载的新表情（`addUserFaces`，只补安装目录没有的编号，名字没有就叫"表情<编号>"）→ 协议端的表情目录 `data/sys-face-catalog.json` 补中文名（`applyCatalogNames`）。分类 `classic` / `super` / `emoji`，emoji 那组没有图，记字符。

## 接口

全部在 qq-console.ts（私有存档）。`conversation` 参数必须是 `qq:group:<数字>` 或 `qq:private:<数字>:<数字>`，否则 400"会话 id 不对"。

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/qq/people?ids=qq:<号>,…` | 一组说话人的名字（名片簿里的昵称，没有就是号）和身份，最多 200 个（记忆页显示"关于谁"、原话是谁说的） |
| GET | `/api/qq/members?group=&q=&limit=` | 群友列表弹窗（规格-群友列表（私有存档））：只算群友（不含主人、她们的号），一条 `GROUP BY` 数每人说过几条、最近一次；不给 `group` 是所有群。没说过话但名片簿里有群名片的排最后。`q` 搜号、昵称、任何群名片、曾用名（包含、不分大小写） |
| GET | `/api/qq/person?uid=&group=` | 一个群友的资料：昵称、各群名片（`group` 那个群排第一）、曾用名、每个会话说过几条 |
| GET | `/api/qq/person/messages?uid=&conversation=&before=&limit=` | 他说过的话，按消息自己的时间倒序翻页（游标是上一页最后一条的 seq，补录的旧消息不乱序），带会话标题 |
| GET | `/api/qq/accounts` | 各号状态（id、角色、`state`、协议端名字、原因、`downSince`）加角色名 |
| GET | `/api/qq/conversations` | QQ 会话列表，最多 200 个，按最后一条的时间；每项带标题、最后一条、谁说的、哪些角色看到过、未读数（最多数到 100，界面写"99+"）。每来一条 QQ 消息就重算，三条查询都按索引走（`CONV_LAST_SQL` 等，测试用 EXPLAIN 守着），"哪些角色看到过"每个会话只查上次之后的新消息 |
| POST | `/api/qq/read?conversation=` | 主人看到这个会话最新一条了（只往后推） |
| GET | `/api/qq/messages?conversation=&limit=&before=` | 一页聊天记录（`limit` 默认 200、最多 1000；`before` 是当前最早那条的 `seq`），按消息自己的时间排；带这一页引用、回应指向的更早消息（`refs`）、出现的人（`people`，名字已认好）、每张图的识图状态（`media`：哈希 → 看好了的那句话 / 正在看 / 失败原因 / 还没看过，规格-识图）；第一页（没有 `before`）还带这个会话被哪些角色看到过（`characters`，和会话列表同一个查法：群里两个以上角色时右边写名字，不等会话列表） |
| GET | `/api/qq/media?hash=` | 一张图的识图详情：状态、看出来的全文（看得见的、图上的字、猜的、作用）、现在的注解和改过的历史、用的模型。每次看都记进运行 · 模型调用（`stage` = `vision`，见 [runtime.md](runtime.md) 的 llm 一行）|
| POST | `/api/qq/media/redo?hash=` | 再看一遍：不管看没看过、失败了多久都重看，马上返回"正在看"，看完从 WebSocket 推 `qq-media` |
| PUT | `/api/qq/media/note` | 写 / 改 / 删注解（`{ hash, note }`，空的是删，最多 2000 字）：旧的留着，按新注解重看；和现在一样的不重复存 |
| PUT | `/api/qq/media/check` | 标"看对了、不用写注解"或撤销（`{ hash, ok }`，ADR-091）：只是整理记号，不重看；和现在一样的不重复记。标"看对了"要这张图看好了（有描述），没看过、正在看、看失败了的 400；撤销不拦 |
| GET | `/api/qq/media/stickers?days=&limit=&filter=` | 最常发的表情包：最近 `days`（默认 30）天按发的次数排，带识图状态、注解、标没标看对了（`checked`）；`filter` = `todo`（还没核对的：没写注解、也没标看对了）/ `checked`（看对了的）/ `failed`（看失败的） |
| GET | `/api/qq/media/backfill` | 补看（规格-识图第十二节）：`counts` 是还有几张没看过（`images`、其中表情包 `stickers`）、看失败的（`failed`、其中表情包 `failedStickers`），正在看的时候是 null（不扫聊天记录）；正在看 / 上次补看的进度（`job`，自己停下的带 `reason`）；识图开没开（`enabled`）、用的模型、同时看几张 |
| POST | `/api/qq/media/backfill` | 开始补看（`{ stickers?, failed? }`）；已经在看了、识图关着就 409 |
| POST | `/api/qq/media/backfill/stop` | 停下补看：正在看的看完就停 |
| POST | `/api/qq/media/import-notes` | 把 `data/dev/vision/labels.json`（`try-vision.ts --import` 生成）里的备注导进注解：先备份聊天记录库，备份没开或没成就 409 不导；和现在一样的不重复写，写着"不知道是啥"的不导；返回 `{ imported, unchanged, skipped }` |
| GET | `/api/qq/refs?conversation=&ids=` | 按事件 id 取同一会话里的几条（最多 50）和里面的人（说话的、被 @ 的，按这个群的名片），实时推来的通知指向没加载的旧消息、或者带着还不认识的人时补；前端合人名时后要回来的盖掉先读进来的（后台按现在的名片簿算，2026-10-19）；只有要这一趟时才换，已经认识的人改名后再说话不会来要（熟人只有被 @、被引用、往上翻到时名字才跟着换） |
| POST | `/api/qq/history?conversation=` | 往上翻：向协议端要更早的，返回 `{ added }`；失败 502 |
| POST | `/api/qq/backfill?conversation=&since=` | 手动从 `since`（毫秒）起补一段，返回 `{ fetched, added, capped }`；没有在线、看得到这个会话的号 502 |
| GET | `/api/qq/forward?id=` | 合并转发的完整树，带这一层每张图的识图状态（`media`，ADR-093）；取不到 404，说明协议端给的原因 |
| GET | `/api/qq/gate?conversation=qq:group:<号>` | 这个群的闸门（[规格-群聊闸门](../规格-群聊闸门.md) 10.1、10.2）：档位、现在生效的频率（`frequency`）和不算临时那层的（`base`）、安静到几点（`quietUntil`）、最近 300 次判断、能不能开口（`speak`，看群聊场合的注入测试）、这小时的数。只有群；闸门没开 404 |
| PUT | `/api/qq/gate` | 改这个群的 `mode`（null 跟默认）、`frequency`（0～1，null 跟默认）、`quietMinutes`（0 是取消安静）。切 `speak` 要群聊场合的注入测试**最近 3 次都全过、而且都是现在这套设置**（提示词和角色卡、她用的模型、群聊思考档位、群聊末尾的提醒开没开、防线代码 segments.ts / injection.ts 的哈希；ADR-106、121），不满足就 409，说明里带原因（哪样变了、最近只跑过几次）。`speakCheck` 由 main.ts 组好（带现在的模型、群聊思考档位和提醒开关）传给 Router 和 qq-console（ADR-090、ADR-106） |
| GET | `/api/blobs/:hash?w=` | 按内容哈希取媒体。只认 64 位十六进制；位图和音视频按入库时认出的类型给，其余（含 SVG、HTML）一律当下载文件；支持 Range（视频拖进度条）；`w` 要缩略图（ADR-043）。不在 `/api/qq/` 下，但由 qq-console 注册 |
| GET | `/api/qq/selftest` | 自检项、正在进行的那次、最近 5 次 |
| POST | `/api/qq/selftest/start?account=` | 开始自检（不给 `account` 用第一个号） |
| POST | `/api/qq/selftest/stop` | 结束自检 |
| POST | `/api/qq/selftest/send?item=` | 发一项测试消息给主人 |
| GET | `/api/qq/alerts` | 最近 50 条提醒（`qq_alerts`）。主窗口前端现在没有读它，通知中心还没做 |
| GET | `/api/qq/local` | 三个目录现在是什么、是否有效，小黄脸的版本和张数 |
| POST | `/api/qq/local/detect?kind=` | 重新自动识别，`kind` 为 `qq` / `protocol` / `data`；找不到 404 |
| POST | `/api/qq/local/pick?kind=` | 弹文件夹选择窗口手动选 |
| GET | `/api/qq/faces` | 小黄脸名字表（编号 → 名字、分类、emoji 字符） |
| GET | `/api/qq/face/:id` | 一张小黄脸的图（本机复制的那份） |
| GET | `/api/qq/avatar/:kind/:id` | 头像（`kind` 为 `user` / `group`）；后台代取、按哈希存、3 天刷新，页面不直接连腾讯 |
| GET | `/api/qq/remote-image?u=` | 卡片封面、图标的代取：只认 `ugcimg.cn`、`gtimg.cn`、`qpic.cn`、`qlogo.cn` 的 https 地址，取一次用一年 |

合计 22 条 HTTP 路由。WebSocket 上由 console.ts 推给网页的 QQ 消息：`qq-state`（号状态）、`qq-event`（`qq/message` 和 `qq/event`）、`qq-selftest`（自检状态），见[回合循环与主窗口](agent-and-console.md)。

## 配置

下表保留 Bun 的 `qq` 配置历史，定义在 config.ts（私有存档）。Go 的键和默认值以 `native/internal/config/config.go` 为准，示例见 `sumika.example.yml`；`app.Open` 在 receive 模式要求选中账号显式填写 `accounts[].url`，空白就报错，不自动查找协议端地址，archive 模式不要求账号 URL。

| 键 | 默认 | 谁读 | 作用 |
|---|---|---|---|
| `owner` | 无 | qq、qq-local | 主人的号。认主人、自检的对象、找数据目录时优先含这个号的 |
| `accounts[].id` / `character` | `[]` | onebot、qq | 她的号，绑哪个角色 |
| `accounts[].url` | 无（自动找） | onebot | 协议端的正向 WebSocket 地址。不写就每次连之前按 SnowLuma 的 `config/onebot_<号>.json` 找（ADR-100）；写了照用，只比一下口 |
| `accounts[].token` | 无 | onebot | 访问令牌。一般不写，存在加密的密钥里（`qq.<号>.token`，ADR-071）；设置 · 平台账号里"从 SnowLuma 读取令牌"读一次存进去（ADR-100）。也能直接写或写 `${环境变量}` |
| `groups` | `[]` | qq | 群白名单，白名单外的群完全不收 |
| `privateAllow` | `[]` | qq | 除主人外还有谁私聊会被当成"朋友"（不是陌生人） |
| `reply` | `false` | qq | 私聊自动回复，见上 |
| `qqDir` / `qqDataDir` / `protocolDir` | 无 | qq-local | 旧 Bun 版自己找、找到写回；Go 版没有任何代码读它们，设置注册表标“尚未生效”、只读（2026-11-14 核对） |
| `protocolLogDir` | 无 | qq-local、qq-health | 旧 Bun 版的协议端日志目录；Go 版同样没人读，标“尚未生效” |

`QqConfig` 里的 `pace`（发送节奏）、`download`（换掉下载）只给测试注入，不是配置项。

## 约束和已知的不足

- 群里只有档位是"说话"的群真发（ADR-090，先只开主人的群），其余只收；发出去的只有纯文字（没有图片、表情、@、引用）。发表情包的条目见 [待办](../待办.md)。
- **协议端那边的问题**（[待办 第一节](../待办.md)）：SnowLuma 收到发送回包后直接丢弃，所以她的号在 QQ 客户端手动发的文字实时收不到，只能靠补录补到（[SnowLuma#528](https://github.com/SnowLuma/SnowLuma/issues/528)）；一些套着的合并转发 `get_forward_msg` 回"payload is empty"（[#529](https://github.com/SnowLuma/SnowLuma/issues/529)，2.0.1 修了，但要同一个进程先取过外层），主窗口点开时先重取一次外层再试，还不行就照实显示协议端给的原因（见上面"合并转发"；2026-10-19 起，真实的那条三层转发里 3 个里层都取到了）；电脑 QQ 看私聊不是立刻刷新（[规格 十二点五](../规格-QQ渠道.md)）。
- 补录最多翻 10 页（群 1000 条、私聊 500 条），更久的标"可能漏了"、靠往上翻再要；只补这个号已经有记录的私聊。24 小时挂机还没跑过。
- `aliases_json`、`last_seen` 除了写入时合并，没有别的地方读它们：曾用名现在不显示、不查询、不给模型；别名带来源和时间见待办。
- 通知只认上面列的几种，别的（群文件、管理员变动等）直接丢；陌生人私聊里的撤回、戳一戳通知会记"看到了"，他的消息本身不记。
- qq_ 开头的表和 QQ 的事件没有清理（maintain.ts（私有存档） 不碰它们），群消息 90 天归档没做。
- `qq/message` 的类型注释（qq.ts（私有存档））说"要不要回由听的人看 info 决定"，现在实际是看 `message/inbound` 的 `tags`。
- 设置页不能改 `reply`、`privateAllow`，只能改 `sumika.yml`（待办里有）；记忆全量白名单已随 ADR-242 删除。
- **超时认领的已知误吞**：超时那一次拿不到发出去的消息号，所以认领（`maybeSent`）只能按会话和文字对，窗口 5 分钟。主人在她手机上发了一句和刚超时那段一模一样的话，会被认成回声：不另存一条"不是她说的"，映射到那条回复；那段恰是整条回复的最后一段的，还会把"没发出去"去掉。回声本身分不出是哪一次发的，现在没有处理这个误吞。

**规格-QQ渠道 写了、代码里还没有的**：图种"点了才另存"（没有另存隐藏文件的接口）；语音和文件的下载；`poke`、`dice`、`rps`、`location`、`contact` 单独的段（现在都归 `other`，只存类型和截短的数据）；通知中心记一条（只有 `qq_alerts`）；输入联想；会动的小黄脸；没装 QQ 的机器上下载表情；回溯学习；反向 WebSocket。规格里 `mface` 写成"超级表情"，代码里 `mface` 是商城表情，超级表情是 `face` 加 `super`；群友名片簿规格有时写 `people` 表，代码里 QQ 的是 `qq_people`（`people` 是记忆库里角色对人的印象）。

## 验证

```bash
bun test test/qq.test.ts test/qq-chat.test.ts test/qq-e2e.test.ts test/qq-period.test.ts test/outbound-audit.test.ts test/qq-segments.test.ts test/qq-health.test.ts test/qq-local.test.ts test/qq-console.test.ts
```

都对着假协议端和临时数据目录，不碰真实的 `data/`（通过 test/helpers/qq-setup.ts（私有存档） 同时挂适配器和 qq）。

| 文件 | 管什么 |
|---|---|
| qq.test.ts（私有存档） | 连接与核对号、白名单、认人、去重、图片按内容存、陌生人、断线补录、心跳、视频、贴表情、补下载、合并转发、商城表情、往上翻、戳一戳、来源 |
| qq-period.test.ts（私有存档） | 句尾句号：正样本 54 条、负样本 40 条（含每行行尾、跨行引号、没配上的括号）；拆开再连起来对得上；引号括号按整段配；去两遍等于去一遍（两组 6 个字、长度 1～6 穷举，各 55,986 条） |
| outbound-audit.test.ts（私有存档） | 客服腔这类的记数规则（ADR-114 第 3 条，只认典型套话）：正样本 58 条（大模型露馅时的典型原话，每类至少 5 条）全记对、负样本 99 条（群里常见的说法，含复核写的和真实语料里旧规则误记的）0 条、"宁可漏"的 24 条（像 AI 但定成不记，写了为什么）0 条；句首、后面接我、反话、宾语、随时、答题、引号、问号八道限定各有"去掉限定就会记"的样本（18、3、13、4、1、4、3、2 条）；病态长输入各 < 50 毫秒；"AI"不分大小写；接口的 `days`；统计只算最近 7 天（边界）、按角色、真发和试运行分开；旧表补 `dry` 列 |
| qq-chat.test.ts（私有存档） | 私聊收发：按空行拆条、句尾句号去掉（库里、主窗口、回声认领一致；收消息带上 `asSent`）、客服腔只记数（发出去才记，没发的不记）、不回的几种、守卫拦截、`memoryAll`、重试与"没发出去"（超时后回声才到的认成那条、过了时限的不认；回包之前就到的算发出去了）、`origin`、被打断、`qq.reply` 关、自检、重启后接着自检、关机时发一半、SnowLuma 的发送动作 |
| qq-e2e.test.ts（私有存档） | 一条龙：真的 agent（假模型）+ 路由 + 闸门 + qq + 假协议端在同一个进程里，18 种形状（8 种句号、10 种句尾乱码，其中 3 种的乱码、"】【"夹在句号前面，3 种是紧挨汉字的外文碎片：群里删、私聊原样留着，两种被"】【"挡在后面）从模型回包走到假协议端：私聊、群说话档里库里、她下一轮历史里、发出去的是同一句，去句号之前的那句在 `original`（只有真去掉了才有；剥过乱码、清过"】【"）；清过的再让她原样说一遍，一个字不变、没有 `original`；群试运行存的"她会说"再去一遍不变、不发，轮完从她历史里拿掉；"[不说]"原样存、不发 |
| qq-segments.test.ts（私有存档） | 段转换、表情包判断、卡片、纯文字、给模型的一行（转义） |
| qq-health.test.ts（私有存档） | 掉线阈值、恢复、被踢、协议端日志打码 |
| qq-local.test.ts（私有存档） | 小黄脸复制与命名、目录重找与写回、按端口找协议端 |
| qq-console.test.ts（私有存档） | 会话列表按消息时间取最后一条、未读、`refs` 和补取接口 |

界面（QQ 页）见 web/src/Outside.tsx（私有存档）（会话列表、聊天窗口）和 web/src/qq.ts（私有存档）（前端的 QQ 数据仓库，Zustand：`useQqStore`）。真机验收（主人在 QQ 里私聊她、收发自检逐项通过）见[规格-QQ渠道 十](../规格-QQ渠道.md)。

### Go 侧：链接展开出口（2026-11-04，feat/link-expand，离线检查已执行）

出站普通图文统一为 `types.PartMessage` 与有序 `BodySegment`；转发节点同样使用 Body，不能夹带 reply / forward。媒体资源由宿主创建，出口只把非敏感描述写入历史，不序列化字节、文件路径或签名URL。历史迁移把旧文字、图片、表情和节点正文转成新形状；旧媒体描述没有可发送资源，启动仍中止旧待发计划，不恢复发送。

桌面历史里已释放的出站媒体只显示元信息，不生成收件下载凭据；其尺寸、字节数和动图状态仍保留。QQ 收件视频与文件继续提供原下载入口，包括 main 新增的文件分类与嵌套转发下载；两种来源不能因同用 video 段而混淆。

平台聊天库的出站形状迁移为 `chat/0011_outbound_body.sql`，排在 main 的 `0010_file_meta.sql` 后；旧单库入口仍用 `events/0004_outbound_body.sql`。合并时只给尚未合 main 的迁移顺延编号，不改已合入的文件元数据迁移。

`Outlet.BindAutomation("link-reader")` 返回绑定 origin 的出口，普通 `Outbox.Send` 不能冒用已注册origin；`Submit` 返回已开始的 Held。Mode 查询包含角色、origin和会话，只有链接插件使用自己的名单，所有发送仍读发送锁、暂停、健康、核号及原守卫。`Held.Done` 在逻辑终态且资源释放后关闭；Write 迟到时继续持有资源，媒体只凭有效action回包确认，文字或不同图片回声不能串认。确认号保留，迟到回包不覆盖unknown终态。

SnowLuma分别声明私聊/群的图片、视频、GIF及转发内媒体能力；媒体能力默认声明能发（ADR-310，不靠真机开关）；某种媒体在某场合回执明确失败、且它的文字版确认了，宿主记下这个组合并退成文字（`internal/app/media_block.go`，重启后清空）。假端测试单独声明。适配器只接受出口拥有的内存/文件资源，拒绝任意HTTP图片地址、路径和伪造节点身份。节点发送者固定为登录账号；合并转发前不再另发引用导语、转发段本身不带回复段（[规格-链接发送改版](../规格-链接发送改版.md) B4、A5，ADR-360），转发节点里的引用（`ForwardNode.ReplyTo`）本适配器在本地拒掉，链接插件去掉引用重发；不靠变换action名称试错。完整形状表与M1～M9真机清单见[发送端规格](../规格-链接自动展开.md)。
