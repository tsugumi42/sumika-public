# 调研：Telegram 话题 / threaded mode 和 Matrix（Element）的 Space、Room、Thread（2026-11-11）

> 状态：草稿，等主会话审过再挪进 `docs/notes/`。只读调研，没改代码，没碰真实 `data/`。
> 写法照[拆解笔记写法](拆解笔记-写法.md)：一到四节只写它们本身，对照 Sumika 只放第五节。
> 起因：ADR-352 定了会话结构（会话 = 场合 + 参与者 + 主题；工作 / 工单两层；侧栏 = 角色入口 + 待你处理 / 进行中 + 最近会话），主人说以后大部分时间在群聊，群聊 token 效率和工作安排很重要。

## 0. 看了什么、没看什么

| 项 | 版本 / 怎么拿到 | 可信度标记 |
|---|---|---|
| Telegram Bot API | 页面 `core.telegram.org/bots/api` 和 `/bots/api-changelog`，2026-11-11 用 curl 取回存本地（WebFetch 被域名检查拦了，改用 curl）；当前最新是 Bot API 10.4（2026-11-09） | `[文档]`，字段名和更新日志原文 |
| Telegram MTProto 文档 | `core.telegram.org/api/forum`、`/api/threads`、`/api/notify` 同样取回 | `[文档]` |
| Telegram Desktop | 本机 `_external\tdesktop` @`250fc73`（2026-10-31），读了 `data_forum_topic.cpp` 等几处 | `[读码]`，只读了话题的未读 / 静音 / 打开流程，**没读话题列表的绘制和 tabs 界面** |
| Matrix 规范 | `spec.matrix.org/latest/client-server-api/`（含 v1.18、v1.19 标记），取回后按行读；下文行号指我存的纯文本副本，所以只写章节名 | `[文档]` |
| Element Web | `element-hq/element-web` @`21d2a55`（develop），浅克隆；Windows 路径过长导致 checkout 失败（只影响测试截图），源码用 `git show HEAD:<路径>` 读 | `[读码]`，读了 SpaceStore、SpaceNotificationState、RoomNotificationState、SpacePanel、SpaceTreeLevel、ThreadPanel、ThreadsActivityCentre；**没读新房间列表（RoomListStoreV3）和线程视图** |
| Telegram `schema` 页 | 取不到（网络失败） | 因此没能直接核对 `inputNotifyForumTopic`，每话题静音的证据来自 tdesktop 代码，不来自 API 文档 |

## 一、Telegram

### 1.1 群组论坛话题（Forum Topics）

- **开关**：论坛是超级群的一种模式，`channels.createChannel` 带 `forum` 标志，或 `channels.toggleForum(enabled=true)` 把已有超级群转成论坛；只有拥有者权限的管理员能开关；基础群要先升级成超级群。[文档 `api/forum` “Group forums”]
- **谁能建**：只有带 `can_manage_topics` 管理员权限的人能建、改、删话题（Bot API：`promoteChatMember` 的 `can_manage_topics`，`ChatPermissions.can_manage_topics`“允许成员建话题”）。[文档]
- **话题 = 一条消息线程**：话题 id 等于创建它的服务消息 `messageActionTopicCreate` 的消息 id；每个论坛都有删不掉的“General”话题，id = 1；发消息到话题只是在 `reply_to_msg_id` 里填话题 id；话题里的消息不会再生成子线程（线程不嵌套）。[文档 “Interacting within topics”]
- **字段**：`forumTopic` 带 `title`、`icon_color`、`icon_emoji_id`、`closed`、`pinned`、`hidden`（仅 General 可隐藏）、`my`（我建的）、`title_missing`、`top_message`、`read_inbox_max_id`、`read_outbox_max_id`、`unread_count`、`unread_mentions_count`、`unread_reactions_count`、`unread_poll_votes_count`。**未读数是按话题单独算的，读标记（`read_inbox_max_id`）也是按话题的。** [文档 `api/forum` “Forum topics”]
- **置顶**：每个论坛最多 `topics_pinned_limit` 个置顶话题，可重排（`messages.updatePinnedForumTopic`、`reorderPinnedForumTopics`）。[文档]
- **列表样式**：超级群论坛有两种话题选择界面，`channel.forum_tabs` 为真是标签栏，否则是列表，群主用 `toggleForum(tabs=...)` 切换；另有每个账号自己的“View as messages”（`channels.toggleViewForumAsMessages`）：把所有话题的消息当普通群显示，设置跟着账号同步。[文档 “Tabbed or list-based forum UI”“View as messages”]
- **删除**：`messages.deleteTopicHistory` 删话题和它所有消息；没有专门的“话题被删”更新，客户端从“话题根消息被删”推断整条话题已删，再用 `getForumTopicsByID` 拿到 `forumTopicDeleted` 确认。[文档 “Managing topics”]
- **机器人怎么知道在哪个话题**：Bot API 的 `Message.message_thread_id` + `is_topic_message`，发消息时带 `message_thread_id`。[文档 Bot API 9.3 更新日志、`Message` 类型]
- **通知 / 未读**：Bot API 里没有任何未读、静音字段（查了 `bots/api` 全文，没有）；这些只在 MTProto 客户端侧存在。tdesktop 里话题有自己的静音状态：`ForumTopic` 构造时 `Thread::setMuted(owner().notifySettings().isMuted(this))`，`ForumTopic::setMuted` 单独改并触发 `UpdateFlag::Notifications`（`Telegram/SourceFiles/data/data_forum_topic.cpp:236`、`:892-903`）；列表角标由 `chatListUnreadState()` 取 `_replies->displayedUnreadCount()`（同文件 `:907-910`）。`[读码]`
- **“正在输入”也按话题**：`sendChatAction` 支持 `message_thread_id`（Bot API 9.x 更新日志），tdesktop 里每个话题有自己的 `SendActionPainter`（`data_forum_topic.cpp:238` 附近、`:903-905`）。`[读码]`

### 1.2 机器人私聊里的 threaded mode（话题）

时间线（[文档 更新日志]）：

| 日期 | 版本 | 内容 |
|---|---|---|
| 2025-12-31 | Bot API 9.3 | “Topics in private chats”：`User.has_topics_enabled`（只在 `getMe` 返回）；`Message.message_thread_id`、`is_topic_message` 在私聊话题模式下可用；`sendMessage` 等所有发送方法、`sendChatAction`、`editForumTopic`、`deleteForumTopic`、`unpinAllForumTopicMessages` 支持 `message_thread_id`；`ForumTopic` / `ForumTopicCreated` 加 `is_name_implicit`；同时新增 `sendMessageDraft`（流式预览） |
| 2026-02-09 | Bot API 9.4 | 机器人可用 `createForumTopic` 在私聊里建话题；@BotFather 小程序里新增设置，可禁止用户自己建 / 删话题；`User.allows_users_to_create_topics`（只在 `getMe`） |
| 2026-11-09 | Bot API 10.4 | 私聊话题链接：`https://t.me/<bot_username>/<message_thread_id>`，只对该私聊里的那个用户打开 |

要点：

- **怎么开**：在 @BotFather 里给机器人打开 Threaded mode；MTProto 里是 `user.bot_forum_view` 标志。文档明说“这个模式对 AI 聊天机器人特别有用”，并注明**该功能对 Telegram Stars 购买有额外费用（服务条款 6.2.6）**。[文档 `api/forum` “Bot forums”]
- **谁能建**：机器人永远能建 / 改 / 删；用户能不能由 `user.bot_forum_can_manage_topics`（BotFather 的 “Disallow users to create new threads” 选项）决定，不能时调用会报 `BOT_FORUM_CREATE_FORBIDDEN`。[文档]
- **General 话题的 UI 规则**（对客户端的要求）：如果用户不能建话题，发“无话题消息”会落进 General；图形客户端应阻止这一点，把 General 的话题界面换成“按消息查看”界面，并在底部固定一个“Type any message to create a new thread”气泡，告诉用户“在这里发消息会新建话题并把消息发进去”。用户能建话题时则允许直接发到 General。[文档]
- **话题名**：`title_missing` / `is_name_implicit` 表示“名字不是创建者明确给的，机器人大概要改名”。[文档 `ForumTopicCreated`]
- **删除**：私聊话题被删时，通过普通消息删除更新（`updateDeleteMessages`）带出，根消息被删就算整个话题删了。[文档]
- **流式输出**：`sendMessageDraft` 是 30 秒的临时预览，定稿必须再 `sendMessage` 落库；可选 `can_stop` 给用户一个停止按钮，用户点了机器人收到 `stopped_message_generation` 更新。[文档 `sendMessageDraft`]
- **打开话题的行为**：tdesktop 打开机器人话题时先 `requestTopic` 取话题，已知未读数才 `showRepliesForMessage`，否则退回 `showPeerHistory`（`Telegram/SourceFiles/window/window_session_controller_bot_forum.cpp:11-39`）。`[读码]`

### 1.3 Communities（顺带）

Bot API 10.3（2026-08-24）“初步支持 Communities —— 若干超级群、频道和机器人围绕一个共同主题 / 受众关联起来”：只新增了 `Community` 类、`ChatFullInfo.community`、`CommunityChatAdded` / `CommunityChatRemoved` 服务消息。没有层级、没有未读聚合的字段。[文档] 这是 Telegram 版的“容器”，目前很薄。

## 二、Matrix 规范

### 2.1 房间类型和 Space

- 房间类型写在 `m.room.create` 的 `type` 键；规范只定义了一种：`m.space`。[文档 “Room Types”（Rooms 一节）]
- **Space 本身就是房间**：名字、话题、头像都用普通房间状态事件；成员、邀请、可见性、加入规则都复用房间机制。规范**不建议在 space 里发普通消息**，要求建 space 时把 `m.room.power_levels` 的 `events_default` 设得很高（默认结构下 100）来禁止，客户端“可以进一步忽略 space 的通知计数”。[文档 “Spaces”]
- **层级的两种声明方式**：
  - 在 space 里放 `m.space.child` 状态事件（`state_key` = 子房间 id）。`content.via` 必须有（没有 `via` 就等于移除）；`order`（可选，ASCII 0x20–0x7E，≤50 字符，字典序）；`suggested`（可选，默认 false，“渲染提示：对 space 成员可能感兴趣，客户端可以更积极地显示”）。排序规则：有 `order` 的先按 `order`，其次按事件时间戳，再其次按房间 id。不需要子房间的管理员同意。
  - 在子房间里放 `m.space.parent`（`state_key` = 父 space id），`via` 必填，`canonical` 可选（主父级；多个时取房间 id 字典序最小）。**防冒充规则**：`m.space.parent` 除非满足下面之一就必须忽略——父 space 里能找到对应的 `m.space.child`；或者发这个事件的人在父 space 里有足够权限发 `m.space.child`。
  - 两种都有 = 双向；只有 child = 父方“策展一份清单”，子方不知情；只有 parent = 子方“悄悄”归属、父方不公开。[文档 “Managing rooms/spaces included in a space”“m.space.child relationship”“m.space.parent relationships”“Ordering of children within a space”]
- **嵌套与环**：space 可以嵌套 space，规范禁止成环但要求客户端遇到环不能死循环，也要提防过深的树。
- **发现**：`GET /_matrix/client/v1/rooms/{roomId}/hierarchy`（v1.2），深度优先遍历，参数 `from` / `limit` / `max_depth` / `suggested_only`，只看 `m.space.child`。[文档 “Discovering rooms within spaces”]

### 2.2 Thread

- v1.4 加入。线程用 `rel_type: "m.thread"` 指向**线程根**（主时间线里的某条事件），线程内所有事件都指向根而不是上一条；**不能从已有关系的事件（回复、编辑、反应等）开线程，所以线程不能嵌套**；服务器应对这种请求返回 400。[文档 “Threading”]
- 给不认识线程的老客户端留了回退：线程消息同时带 `m.in_reply_to` 和 `is_falling_back: true`，退化成一条回复链。[文档 “Fallback for unthreaded clients”]
- **服务器聚合**：线程根的 `unsigned.m.relations["m.thread"]` 里有 `latest_event`、`count`、`current_user_participated`（我发了根或线程里任意一条为真）。[文档 “Server-side aggregation”]
- **列出线程**：`GET /_matrix/client/v1/rooms/{roomId}/threads`，`include=all|participated`（只看我参与过的）。[文档 “Querying threads in a room”]
- **未读和通知按线程算**：已读回执可带 `thread_id`（线程根 id 或固定字符串 `main`）；不带 `thread_id` 的“无线程回执”作用于整个房间；线程根和与线程根只有非线程关系的事件算在主时间线（`main`）；判断事件是否在线程里，沿关系最多追 3 跳。`/sync` 的 `RoomEventFilter.unread_thread_notifications=true` 时，房间的 `unread_notifications` 只含主时间线，每个线程的数在 `unread_thread_notifications`（以线程根 id 为键，没通知的线程可省略）。[文档 “Receipts”“Receiving notifications”“Threading” 里的 `unread_thread_notifications`]

## 三、Element Web（element-hq/element-web @`21d2a55`）

- **空间栏**（`apps/web/src/components/views/spaces/SpacePanel.tsx`、`apps/web/res/css/structures/_SpacePanel.pcss`）：左侧一条窄栏，折叠时宽 68px（`_SpacePanel.pcss:33-35`）；顶部是“元空间”按钮：`MetaSpace.Home`、`Orphans`、`VideoRooms`（`SpaceStore.ts:68` 顺序），再往下是用户加入的顶层 space；有 `isPanelCollapsed` 状态（`SpacePanel.tsx:130`）；支持拖拽排序（用了 react-beautiful-dnd，`:24`）。设置 `Spaces.allRoomsInHome` 决定“Home”是只显示不属于任何 space 的房间，还是显示所有房间（`SpaceStore.ts:136`、`:193`、`:467`）。`Orphans` 是“不属于任何 space 的房间”，Home 开启时它是 Home 的子集（`SpaceStore.ts:639-646`）。
- **子空间**：`SpaceTreeLevel.tsx` 递归显示子 space，折叠状态存在 `SpaceTreeLevelLayoutStore`；根层级默认折叠（`SpaceTreeLevel.tsx:217-220`，注释“default to collapsed for root items”）。
- **记住每个 space 上次看的房间**：切 space 时回到上次选中的房间，存 `localStorage` 键 `mx_space_context_<space>`（`SpaceStore.ts:72`、`:297-300`）。
- **角标聚合**：`SpaceNotificationState` 把 space 下所有房间的通知数加起来、取最高级别，**跳过 `isLowPriorityActivity` 的房间**（`SpaceNotificationState.ts:74`；`RoomNotificationState.ts:109`，注释写明原因：用户特意降级的房间不能点亮整个应用的角标，见 element-web issue 16836）；更新用 100ms 节流（`SpaceNotificationState.ts:66-85`）。
- **点空间角标 = 跳到第一个有通知的房间**，优先有提及的，再退到任意未读的（`SpaceStore.ts:188-215`，`setActiveRoomInSpace`）。
- **线程面板**（`apps/web/src/components/structures/ThreadPanel.tsx`）：右栏卡片，顶部筛选“全部线程 / 我的线程”（`ThreadFilterType { My, All }`，`:42-44`）和“全部标为已读”按钮（`:113-136`）。
- **线程活动中心 TAC**（`.../spaces/threads-activity-centre/ThreadsActivityCentre.tsx`）：空间栏底部的弹层，列出有未读线程的房间，分两栏 `"my_threads" | "other_threads"`（`:39`、`:52-58`，数据来自 `participatingThreads` / `otherThreads`），每个线程条目带通知级别（提及 / 通知 / 活动）和数量（`:176-250`）。

## 四、两边的差别（任务第 3 点）

| 问题 | Telegram | Matrix / Element |
|---|---|---|
| 一个会话里分多个话题 | 话题是**第一等对象**：有自己的标题、图标、置顶、关闭、未读、静音、正在输入；论坛 = 群 + 一串话题，话题列表代替了单一的消息流（可退回“按消息查看”）；机器人私聊同样 | 线程是**主时间线上的附属分支**：房间仍是一条主时间线，线程挂在某条消息下，在右栏或内联折叠显示；线程**不能嵌套**；房间才是列表里的第一等对象 |
| 一组会话归到一个容器 | 群论坛是“一个群装多个话题”，不是“多个聊天装进一个容器”；Communities（10.3）刚起步，只有关联关系和两个服务消息 | Space 是**专门的层级结构**：房间之间的有向边，可嵌套、可单向声明、可建议、可排序；space 本身是房间但不放消息；一个房间可以同时在多个 space 里 |
| 未读 / 通知 | 每个话题一套（`unread_count`、`read_inbox_max_id`、提及数、反应数、静音）；Bot API 里机器人**看不到**这些 | 按“房间主线 + 每个线程”分桶（`main` 伪线程）；space 的角标是下属房间的聚合，Element 在客户端聚合并排除低优先级；线程另有“我参与的 / 其他”的区分 |
| “进行中”状态 | 只有 `sendChatAction`（正在输入 / 上传等），按话题；没有任务进度概念 | 只有 typing / presence，没有；Beluga 那种“谁在干活”要自己做 |
| 谁能分类 | 话题由有 `manage_topics` 权限的人建；机器人私聊里机器人必能建，用户能不能建由机器人设置 | 把房间挂进 space 不需要房间管理员同意（只在 space 里写 `m.space.child`）；子方声明父方必须被验证 |

## 五、对 Sumika（只在这一节对照）

### 5.1 已被我们的设计覆盖

| 它们的做法 | 我们已有的对应 |
|---|---|
| Telegram 的 General 话题（id=1，删不掉，机器人私聊里是“默认入口”） | ADR-352 第 2 条 / 讨论稿 §3.2：每个角色一个常驻“日常”，不进列表，住在角色入口里 |
| 机器人私聊话题“打开时是空白，发第一句才新建” | 讨论稿 §4 和 ADR-086：话题点开始进空白，发第一句才建 |
| `title_missing` / `is_name_implicit`：名字不是用户明确给的，由机器人改 | `local_topics.title_by`（讨论稿 §1、§5.2 `title_by: ctl | loop | user | character`） |
| “按消息查看”与话题列表可切 | 讨论稿 §3.1 的 B 退路（把角色入口藏起来就是平铺）；§7 Q1、Q3 |
| 机器人建话题、用户建话题可开关 | 讨论稿 §4：“她提议要不要开话题”仍把正文预填进输入框；用户和她都能建 |
| Matrix 线程不能嵌套；Telegram 话题里不再有子线程 | ADR-352 第 3 条：分工最多两层（工作 → 工单），工单不再派工单 |
| `m.space.child` 单向挂接：父方列清单，子方不必知情 | 讨论稿 §9.1“会话和工作可以互相引用，不强制互相包含”；工作区加入要主人点头（§9.4） |
| space 不放消息；Element 让低优先级房间不点亮聚合角标 | 讨论稿 §3.2“内部会话怎么藏”、Q5：自检、子任务默认不进列表（但我们是“藏”，Element 是“留着但不冒泡”，见 5.2 的第 3 条） |
| 点角色头像回到上次看的会话；Element 记住每个 space 上次的房间 | 讨论稿 §3.3：“再点已选中的 = 回到上次在她名下看的会话” |
| `unread_count` 按话题分 | 讨论稿 §5.2 `ConversationView.unread`（已有），话题 / 群每行一个 |

### 5.2 值得借的

**1. 先用已有测量验证“群里分话题能省多少”，再决定要不要做**（对群聊 token 效率影响：可能大，但目前是 `[推断]`，没有数）

- 它们的做法：Telegram 论坛 = 一个群拆成多个话题，每个话题自己的消息线；Matrix 线程是人类阅读用的分支，但服务器给每个线程单独的计数。
- 用到我们这：群聊是“参与者多于 1 的会话”（ADR-352），“同一批人聊不同的事”在我们这里自然是**同成员的多个群会话**，上下文从“整个群长历史”变成“当前这个话题的历史”。群聊每轮上下文随历史长度和角色数一起涨（ADR-352 理由），话题化是把历史长度上限变小的一条路，和 ADR-352 第 5 条的候选（共享前缀、分段摘要、只让被点到的角色跑）互补。
- 好处：不改模型调用逻辑，每个话题的历史自然短；压缩快照仍按 `character_id + conversation` 存（规格-本地群聊 第三节第 5 条），话题就是新的 conversation，**不需要改快照键**。
- 代价：（a）本地群聊里要主人自己选话题 / 开新话题，QQ 群里是平台上的人在说话，不会替我们选话题，要自动分只能靠分类，分类本身要花 token；（b）话题之间的信息要靠记忆（ADR-242 同角色跨场合共用）而不是上下文共享；（c）切话题时旧话题里的“约定”会丢出上下文。
- 对群聊 token 效率的影响：本地群聊理论上每轮输入从“全群历史”降到“当前话题历史”；QQ 群基本没有收益（没法让人选话题）。**没有数据**，因此第一步只做测量：ADR-352 第 5 条的固定剧本测量里，顺手把真实本地群聊日志按“静默超过 N 小时”或“人工标记”假想切段，算“只带最后一段”的每轮输入 token 对比。

**2. 单向挂接的校验规则：谁说了算**（对 token 无影响，对安全有影响）

- 它们的做法：`m.space.parent` 子方自己声明属于某个 space 时，必须父方有对应的 `m.space.child`，或者声明者在父方有发 `m.space.child` 的权限，否则忽略；规范特意写了“防止房间谎称自己属于某 space”。
- 用到我们这：工作（Work）与会话的“互相引用”（§9.1）里，**以工作侧的登记为准**；会话侧写“来源工作 X”只是显示用，不据此授予任何权限、工作区或执行权（对应讨论稿 §9.1“工单记住来源和权限快照，转交不扩权”）。
- 好处：堵住“有人 / 某个会话声称自己属于某工作从而借到执行权”这一类冒充；成本几乎为零，写进工作的规格即可。
- 代价：多写一条规则和一行测试（形状 × 入口表里加“会话自称属于工作”这一格）。

**3. 聚合角标的判据：把“冒泡”和“列出”分开**（对 token 无影响，对界面清晰有影响）

- 它们的做法：Element 的 `SpaceNotificationState` 汇总时跳过低优先级房间（`SpaceNotificationState.ts:74`），房间仍在列表里；线程中心把“我参与的”和“其他的”分开两栏（`ThreadsActivityCentre.tsx:39`）；Matrix 的 `current_user_participated` 是现成判据。
- 用到我们这：侧栏的“待你处理”只收主人**参与过 / 被点名 / 等主人批准**的事；自检、子任务、定期任务的普通活动可以存在、可以在“进行中”露出，但**不点亮角色头像和侧栏角标**。这比“默认全隐藏”更细：隐藏是显示筛选，不冒泡是通知策略，二者分开写。
- 好处：定期任务一多，角色头像不会总亮着；主人没参与的群里别的角色互相说话也不会拉主人去看。
- 代价：要在会话行上定一个“是否冒泡”的来源字段（内部 / 定期 / 主人参与），后台判定；讨论稿 §5.2 的 `internal`、`title_by` 已经是它的材料。

**4. 本地群“从这几条开新话题”（同成员新会话 + 带引用），以及同成员群的归组**（对 token：配合第 1 条才有收益）

- 它们的做法：Telegram 在论坛里新建话题；Matrix 从一条消息起线程，线程根留在主时间线；Space 把相关房间归成一组。
- 用到我们这：讨论稿 §2 已经规定“不原地变群，用同标题新建群并带引用”，这里是把它扩成群里的常用动作：群里聊偏了，“从这条起分出一个新话题”，旧话题留一条指向它的引用。至于“同一批成员的多个群会话在侧栏折成一组”，是 Space 式归组的最小版，只用成员集合分组，不引入新的容器对象。
- 好处：主人在本地群里不用自己手动建群选成员；侧栏不被同一批人的多个话题刷屏。
- 代价：要新增会话间的引用关系（`forked_from` 的群版本）；群目前没有 pinned / archived 列（讨论稿 §5.1 已计划补）。

**5. 将来接 Telegram 平台时，会话键要带 `message_thread_id`；流式预览有现成接口**（以后）

- 事实：机器人私聊 / 群论坛里每个话题是独立的 `message_thread_id`；`sendMessageDraft` 是 30 秒临时预览，定稿要再 `sendMessage`；有 `can_stop` 按钮；开 threaded mode 对 Stars 购买有额外费用（服务条款 6.2.6，我只查到这句，没读条款原文）。
- 用到我们这：[方案-Telegram账号取数](方案-Telegram账号取数-2026-11-10.md) 的那条线里，若以后让角色在 Telegram 里发言，会话键要设计成 `平台 + chat_id + thread_id`，一个话题 = 一个 Sumika 会话，和平台“渠道”概念（词汇表）一致。当前别的渠道一律只记录，不用现在动。

### 5.3 不该借的

| 它们的做法 | 不借的理由 |
|---|---|
| Telegram 机器人私聊：机器人是聊天容器、话题在里面 | 和 ADR-352 第 1 条“角色是参与者，不是容器”正相反；它的“bot = 联系人”在我们这里对应侧栏顶部的角色入口，话题行里不再放“谁”（讨论稿 §3.2）。只借 General 的“日常 + 发第一句才建话题”这两个机制，已有 |
| Matrix Space 禁止在 space 里发消息、把容器做成房间 | 我们的工作要有“工作对话”（§9.4 主人可以直接在里面插话），所以工作不是 Space；只借“单向挂接”（5.2 第 2 条）和“建议显示”的提示思想 |
| Matrix 线程作为主时间线上的附属分支，用来给模型减上下文 | 线程是给人看的渲染 / 通知分区，**服务器端历史仍是同一间房**；模型上下文我们得自己拼，线程本身不省 token。借它的“线程不嵌套、未读按线程分桶”即可，不借存储结构 |
| `via` 服务器列表、`order` 字符串字典序、联邦发现、`/hierarchy` 分页 | 单机本地应用没有联邦；排序用置顶 + 最近活动（讨论稿 §3.3），不需要字符串排序键 |
| Telegram 话题的图标颜色 / 自定义 emoji、`closed` 状态、置顶数量上限 | 我们头像已经够标识“谁”，界面少写字（主人 11-06）；`closed` 对应已有的归档；置顶上限等真有人置顶过多再说 |
| Telegram 每话题静音、Matrix 每房间通知规则整套 | 先不做；如果以后要“群里让某角色安静”，那更像是 ADR-352 第 5 条里“只让被点到的角色跑模型”，属于路由而不是通知，别借成通知设置 |
| Communities | 只有关联关系和两个服务消息，没有可借的行为 |

### 5.4 建议（现在做 / 以后做 / 不做，最多 6 条）

| # | 建议 | 说明 |
|---|---|---|
| 1 | **现在做**：群聊测量里顺手加“按静默 / 手动分段假想切段”的对比账 | 不改功能，只在 ADR-352 第 5 条的测量里多记一栏；结果决定第 4 条值不值得做 |
| 2 | **现在做**：工作规格里写“挂接以工作侧登记为准，会话自称不授权”，并加进形状 × 入口表 | 一两行规格，防冒充；工单两层已有 |
| 3 | **现在做**：侧栏规格里把“冒泡”和“列出”分开：待你处理 = 主人参与 / 点名 / 等批准；内部、定期的普通活动不点亮头像和角标 | 只写规格，界面等群聊效率之后；材料是 `internal`、`title_by` |
| 4 | **以后做**：本地群里“从这几条开新话题”（同成员新会话 + 引用）和同成员群在侧栏折成一组 | 先看第 1 条的数；没数据就不做 |
| 5 | **以后做**：接 Telegram 平台时会话键带 `message_thread_id`，参考 `sendMessageDraft` 做流式预览 | 现在渠道只记录，不动 |
| 6 | **不做**：Space 式的工作容器、机器人式的角色容器、线程式的“主线内嵌分支”存储、`via` / `order` / 话题图标颜色 | 理由见 5.3 |

## 六、没有把握的地方

1. **“群里分话题能省多少 token”只是推断**：没有拿真实群日志算过；QQ 群无法让人选话题，这个办法大概只适用本地群聊。需要先做建议 1 的测量。
2. **两家的实际界面只读了一部分**：Telegram 话题列表和标签栏的绘制（tdesktop 没读）、Element 的新房间列表（RoomListStoreV3）和线程视图没读；Telegram 每话题静音只在 tdesktop 代码里看到，没能核对 API schema 页；本机 tdesktop 是 2026-10-31 的提交，可能比 Bot API 10.4（11-09）旧一点。

## 七、请主会话顺手改的一处旧稿

方案-会话结构（私有存档） §0 表里那一行写“Telegram 论坛话题属于‘群’而不是‘联系人’，没联网核实 [推断]”：现在查实 Telegram 的机器人私聊也有话题（Bot API 9.3/9.4），“bot = 联系人 + 里面的话题”是它的另一种做法，该行可改成指向本笔记。
