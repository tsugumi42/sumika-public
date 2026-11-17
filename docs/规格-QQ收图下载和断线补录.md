# 规格：QQ 收图下载和断线补录

> 状态：收图下载、断线补录及 fix/gap-fill 已合 main；2026-11-03 fix/gap-fill2 修复 SnowLuma 首页核对、独立区间填平、重复缺口和旧缺口拖住新窗口，已合 main并换上运行，真库上三轮补录没再产生假缺口；旧代码留下的 181 条假缺口用 scripts/fix-gap-fake-abandoned-20261103.ts 清掉。第 7 步界面缺口标记及媒体占位文字原写"不做"，已被 ADR-258 改为要做（等聊天区换成 MewTypeUI 后接），M14 真机收图仍待验收。
> 后续界面与手动入口见[规格-补录缺口界面与自动对齐](规格-补录缺口界面与自动对齐.md)：后台查询、单段补录、连接对齐汇总和 ctl 已合 main；MewTypeUI 缺口界面尚未接。下文第 7 步“不在本次范围”仅说明原后台实施批次；文件按 ADR-261 两档执行。
> 2026-11-06 ADR-278 简单版已合 main（2026-11-06，分支 `fix/backfill-adr275`）：群 message_seq 连续且包住缺口两端可关闭；无证明扫一轮后 blocked、不再自动重查；手动取到的消息照存；缺口 API 可确认停止提醒并撤销。检查位置与连续水位分离、旧 filled 降级照留。不做持久续扫游标、撤回对号或多轮追洞。
> 图片宽高及动图标记：chat/0007_media_meta 已在 main；下载事务写入、后台补扫、展示批量查库及合成图片验收见第四节。
> 视频第一步及 ADR-255 下载后台已合 main（feat/qq-video-dl）；feat/qq-file-dl 按 ADR-261 将同一链路扩展到文件段，2026-11-04 已合 main。8 包测试、同范围 vet、15 个改坏场景及独立冒烟量测已完成，见 13.3；真实协议端接口仍待验。下文“不做文件 / 视频”仅指原收图范围；正文补扫只恢复本轮新收到的视频和文件，不批量下载旧记录。
>
> 参考了：[规格-渠道中间件](规格-渠道中间件.md) 第六节（进来的路径）、第十五节第 8 步、第十六节 B"空会话补录"和"补录和实时交错"、第十八节；[规格-QQ渠道](规格-QQ渠道.md) 第五节（媒体）、第六节（补录）；[参考页 qq.md](subsystems/qq.md)"媒体和表情""掉线、补录、补下载、往上翻"（Bun 时代的做法，Bun 代码在 sumika-bun 仓库、本机不留，所以照这份文档，不照代码）；[规格-界面接Go后台](规格-界面接Go后台.md) 5.2～5.4；规格-从Bun导入历史（私有存档） 的 `qq_sync` 一行；[清单-常见边界情况](notes/清单-常见边界情况-2026-10-26.md)（用到的类：数据晚到、失败不是全有全无、并发乱序重复、数量上限、外部协议实际行为、缓存有效期、关闭后旧任务还在回来）。
>
> 借了：Bun 的"起点取水位和本会话最后一条里早的那个"（2026-10-17 漏 23:5x 的教训）、下载上限 20 MB / 超时 20 秒 / `expired` 不重试 / 用 `get_msg` 换新链接 / 商城表情 `.gif` 下不到换 `.png`、补录不触发回复。没借：Bun 的"每次连上最多翻 10 页"（Go 版按请求总数封顶，见 5.3）、`notice: gap` 往会话里塞一行通知（Go 版用结构化的缺口表）。
>
> SnowLuma 的核查对象是本机装的 2.3.3（`<SnowLuma 目录>\index.mjs`，只读了程序代码，没碰 config、data、logs，没启动它）。
>
> 本次修复参考：NapCatQQ `packages/napcat-onebot/action/go-cqhttp/GetGroupMsgHistory.ts:39-47`（无锚点取最新视图，有锚点才翻历史），借“首页最新性”和“旧页进度”分开证明；Koishi OneBot `src/bot/base.ts:57-86`（包含 before 锚点、统一转换历史消息），借包含锚点的衔接契约；MaiBot-Napcat-Adapter `apis/message.py:287-297` 只转发历史动作，不提供覆盖证明，因此不把请求成功当补齐。均只借结构，不复制源码。沿用清单的并发乱序、部分失败、缓存来源、重启和重试边界。

## 零、先说人话

1. **收到图片，现在只存了一个会过期的链接。** QQ 的图片链接大约一小时就失效，失效以后这张图就永远看不到了。要做的是：消息先照常存、照常回，另有一个后台小工人拿着链接把图下载到本地的媒体库，算出指纹（sha256），再把指纹填回那条消息的图片段里；下载不下来就老老实实记"取不到"，不编一张假的。已经收到、还只有链接的那一批，启动时也补一遍。
2. **Go 关着或掉线的时候，QQ 里来的话现在永远缺着。** 要做的是：重新连上以后，对每个聊过的私聊（和白名单群），向 SnowLuma 要"这段时间的历史消息"，按平台消息号去重后补进库，打上"补录"标记；补回来的不触发回复、不让群闸门开口。一次拉太多会被 QQ 当可疑动作，所以有页数、间隔、总次数的上限；拉不全的地方记一条"这里缺了一段"，不假装补齐。
3. 两件事共用一层"向 SnowLuma 问"的小接口（拉历史、按消息号取一条），先做这一层。

## 一、范围和不做的

做：QQ 渠道（私聊、白名单群）的图片段和表情段（`image`、`emoji`）下载；私聊和白名单群的断线补录；只在接收模式（`-mode receive`）下启用。

不做：

- 语音、视频、文件的下载：现在的段里没存它们的地址（`snowluma.go:195` 只给 image 段存 `URL`、`File`），要做得先加字段，另开一条。
- Go 侧识图（待办同一条的②）、出站图片视频（③）。
- 本地私聊、本地群聊：没有协议端，没有这两件事。
- 多个 QQ 小号（现在 `-account` 只连一个）；表里留了 `account`，但没做并发多账号。
- 往上翻更早的历史（Bun 的 `fetchOlder`）：不是断线补录，要的时候另做。
- 图种（图片后面藏压缩包）的识别：Bun 做了，Go 没有消费者，不做。
- 补录回来的消息进记忆总结：不进（ADR-092），带 `backfill` 标签交给各消费者自己过滤。

## 二、现状（带出处，2026-11-02 在 main 上读的）

**收图：**

- 适配器把 image 段翻成 `SegImage`，`sticker()` 判断是表情包就翻成 `SegEmoji`，两种都只存 `URL`（`mediaURL` 只留 http(s)、不带控制字符、不超 2048 字节）和 `File`（`mediaFile` 只留单个文件名）（`channel/snowluma/snowluma.go:195`、`:243`）。`Seg.Hash` 一直是空的（`channel/types/seg.go:36`）。SnowLuma 的 `mface`（商城表情）也被它翻成 image 段，地址是 `gxh.vip.qq.com/.../raw300.gif`（`index.mjs:21658`）。
- 媒体按 `<数据目录>/go/media/<平台>/<hash 前两位>/<hash>` 存放；`mediastore.OpenMedia` 直接读平台目录，诊断核对 sha256，`mediastore.Put` 写入。ADR-308 起读写不检查导入状态（`mediastore/media_read.go`、`media_write.go`）。
- `blobs`、`blob_refs` 两张表在每个平台库的基线里（`store/migrations/chat/0001_baseline.sql:8-9`），起草时运行期没有代码读写它们，历史导入工具曾写入；该工具已依 ADR-308 退役。
- 桌面显示一个图片段，靠 `Seg.Hash` 找文件；没有 hash 的显示“尚无本地媒体”（规格-界面接Go后台 5.2）。宽高和动图标记由下载器存进 `media_fetch`，读取消息只查库（同规格 5.3）。
- `media-check` 能数出 hash 为空的段（`mediastore/media_check.go` 的 `EmptyHash`），但它分不出"本来就没地址"和"有地址没下"，所以补下载要自己按"有地址、hash 为空"挑。
- 入库一站把消息存成 `events` 的一行，段在 `segments_json` 的 `$.segs` 里（`channel/intake.go:store`）；入库后调 `Config.OnStored`。流水线的观察者（`Pipeline.Observers`）**不给**补录、陌生人、自检、她自己说的（`channel/pipeline.go:127`），所以下载不能挂在观察者上，也不检查 `Stored.Observed`：陌生人的图照下（2026-11-02 用户：“QQ什么样就什么样，那边会下那这边也会下，不下她没法理解图”，ADR-243）。
- 起草时运行期还没有事后改 `events.segments_json` 的代码；当时的导入核对实现见已退役历史导入工具（删除提交 的父版本）。现由 `store.SetSegHash` 回填。

**补录：**

- 适配器里没有任何拉历史的调用；`types.Adapter` 和 `Caps` 里没有历史能力；`call()` 是包内的，现在只有发送和登录号核验在用（`channel/snowluma/call.go`、`verify.go`）。`callTimeout` 是 10 秒。
- 入库一站已经为补录留了口：`Intake.Submit(ctx, in, Options{Backfill: true})` 加 `Ticket.Wait`（`intake.go:212`）。补录的事件打 `backfill` 标签，`originOf` 按"实时接管时刻"分 external 和 human，不进拦截链和闸门（`pipeline.go:127`），"她看到了"通知照发、带标签。去重键是（渠道、会话、平台消息号）的 `channel_msgs`，号为空或 0 不去重（`store/inbound.go:57`）。
- `qq_sync(account, through)`：只在 `store/migrations/qq/0003_import_history.sql` 里建了表，注释明说"不在这里加运行期补录逻辑"；导入时 Bun 的值原样带过来（规格-从Bun导入历史一行）。运行期没有任何代码读写它。
- 规格-渠道中间件第十五节第 8 步写了 `HistoryPage`（`Caps` 加一项）和"结构化的未补区间"，没做。
- 空档的实际范围（切换记录）：Bun 最后一次写库 2026-10-31 17:29，Go 2026-11-02 15:1x 起收，中间约 46 小时谁也没存（SnowLuma 一直开着，它自己的库里有这段）。

## 三、SnowLuma 接口核查（2.3.3，`index.mjs` 行号）

本节是 2.3.3 的读码核查；后续 B14 真机结果见 3.x，私聊带锚点不能继续往前翻，实施按实测结果。

| 动作 | 实现了吗 | 参数 | 返回 | 分页和限制 | 出处 |
|---|---|---|---|---|---|
| `get_group_msg_history` | 实现了（只读） | `group_id`；`message_id`（默认 0，整数、可为负）；`count`（默认 20，**封顶 200**）；`reverse_order`（默认 true，**只在 message_id 非 0 时有效**，true 返回锚点及更旧，false 返回锚点及更新） | `{messages: [...]}`，每条是消息事件，**删掉了 `post_type`、`self_id`，加了 `real_id`**；旧记录里 `real_id` 可能是 0 | 锚点是 `message_id`，**没有"从时刻 T 起"的参数**。无锚点：锚在 SnowLuma **本地库里这个群见过的最新一条**的序号，再向服务器取；**本地一条都没有就直接退回本地库、不问服务器**（可能返回空）。有锚点但本地库不认这个 id（或序号不是服务器确认过的）：返回 `[]`。每次动作内部按 30 个序号一窗向服务器取，最多 12 次往返，窗与窗之间进程级排队、**至少间隔 300 毫秒**（`HistoryRequestGate`）。拉回的消息会存进 SnowLuma 自己的库，**不会推给 WS 客户端**，所以不会和实时帧重复到达 | `index.mjs:11588`（动作）、`:37466-37536`（`getGroupMsgHistory`、`getGroupHistory`）、`:38243`（count 规范化）、`:38250`（清洗）、`:307681-307731`（常量和翻页） |
| `get_friend_msg_history` | 实现了（只读） | `user_id`；其余同上 | 同上；私聊自己发的消息没有 `post_type`，`user_id` 是自己的号，`target_id` 是对方（`target_id` 为 0 时 SnowLuma 补成这个对方） | 无锚点：走服务器的漫游接口（时间游标），每页 100，最多 12 页，翻不满且没到头就**抛错**，不返回不完整的缓存。有锚点：同群，锚点必须是 SnowLuma 库里序号确认过的消息，否则退回本地库。解析不出好友的内部标识也抛错。**已撤回的私聊消息会被省略** | `:11619`、`:37482`、`:37538-37573`（`getFriendHistory`）、`:308350-308392`（漫游翻页） |
| `get_msg` | 实现了（只读） | `message_id`（整数，负值合法） | 一条消息事件（同样删了 `post_type`、`self_id`，加 `real_id`）；**逐段刷新图片、语音、视频的地址**，某段刷新失败就保留原地址继续返回 | 消息必须在 SnowLuma 本地库里，否则 `message not found`（retcode 100） | `:7872`、`:39294`（`refreshMessageMedia`） |
| `get_image` | 实现了（只读） | `file` 或 `file_id`（原样取自图片段，须在 SnowLuma 的媒体缓存里） | `{file, url, file_size（字符串）, file_name}`；`url` 可能为空；不在缓存返回 `image not found in cache` | 无分页。要 Go 带着段里的 `File` 去问，而 Go 的段里 `File` 是被 `mediaFile` 过滤过的名字，未必和 SnowLuma 缓存的键一致，所以**不作为主路径**，只当 `get_msg` 之后的备用（没验证） | `:11984`、`:39377` |
| `get_file` | 实现了，**只认图片和语音缓存** | `file_id` 或 `file` | 同 `get_image`；找不到时提示群文件要用 `get_group_file_url` | 同上 | `:13984` |
| `get_record` | 实现了 | `file`、`out_format`（可选，服务端转码并附 base64） | 语音信息 | 不在本规格范围 | `:12006` |
| `get_recent_contact` | **占位，恒返回空数组** | `count` | `[]` | 所以**发现不了"Go 库里没有记录的私聊"**：Go 关着期间第一次来找她的新人，补不回 | `:13219` |
| `get_login_info`、`get_status` | 实现了，适配器已在用 | — | 登录号、收发健康 | 登录核验的一部分 | `verify.go` |

图片地址的时效：`multimedia.nt.qq.com.cn` 一类地址要带 `rkey`，SnowLuma 生成消息事件时按当时的 rkey 给地址签好；rkey 的有效期用服务器给的 `ttlSeconds`，**没给就按 3600 秒**，刷新提前量 60 秒（`:39673-39735`）。`gchat.qpic.cn` 的老地址不带 rkey（`urlNeedsRKey`，`:39852`）。**没查到**服务器给的 `ttlSeconds` 的真实值，所以"链接大约一小时失效"是代码缺省值，不是实测。`urlNeedsRKey` 对任何含 `/download` 的地址都会追加 rkey（`:39855`），下载器因此必须自己限制能访问的主机（见 4.4）。

登录时自动补历史（`historySync.enabled`，代码缺省 false，`:5074`、`:340804-340822`）：它只往 SnowLuma 自己的库里存（每个会话最多 20 条，间隔 2 秒，`:307598`），**不推事件给 Go**；Go 不依赖它，也不去改它（不碰 SnowLuma 的 config）。

**没查到：** 协议端本地消息库的保留期（retention 命中的是合并转发文件）；服务器侧单个群、单个好友能往回翻多远。已撤回的群消息不返回，由 B14 实测确认。

**结论：** 两个历史动作和 get_msg 已实现。按 B14，群可以 message_id 锚点向前翻，私聊只取无锚点首页；没有按时间取的参数。协议端从没见过的群可能返回空，发现不了无记录的新私聊。

### 3.x B14 真机实测（2026-11-02 23:5x，主人在场点头："现在做"）

用 `native/cmd/snowluma-record -act` 对本机 SnowLuma 发了 4 个只读查询（一个白名单群两页、主人私聊两页，每页 20 条，查询之间隔 2 秒以上），只记结构和编号，原始帧看完即删，不进 git。结论：

1. **历史和实时的消息号是同一个**：群第一页 20 条里 19 条按 `message_id` 在 `channel_msgs` 里找到；私聊第一页 19 条全部找到。补录按平台消息号去重成立。
2. **群翻页接得上**：以第一页最旧一条为锚点、`reverse_order: true` 取上一页，回来 20 条，锚点本身也在里面，`message_seq` 和第一页首尾相连。
3. **群历史不返回已撤回的**：第二页里缺了一个 `message_seq`，库里这段时间正好有一条撤回通知。断开期间被撤回的不会被补回来。
4. **私聊带锚点只回锚点本身**：第一页（不带锚点）19 条正常；拿最旧一条当锚点取上一页只回 1 条（锚点），更早的取不到（第三节说的"锚点要服务器确认过，否则退回本地库"）。私聊补录只能靠不带锚点的第一页，一页 100 条够补短的空档；更长的空档记缺口。
5. **私聊的 `message_seq` 是两套**：自己发的和对方发的各一套，不能按序号排先后、判断连续，只能按时间。
6. **当晚的漏消息确认是换 exe 断开造成的**：群第一页唯一缺的一条，正好落在 23:51:41～23:51:47 退出重启的那几秒（当晚换了 7 次 exe，每次断 2～19 秒）。

2026-11-05 第二轮核查结论作为历史记录保留于[序号调查](规格-补录缺口界面与自动对齐.md#第二轮序号调查与关闭路径p2未实现新行为)。2026-11-06 按 ADR-278 接入群序号证明：只读本机协议端 2.4.0 `convertGroupMessage`（index.mjs:23102–23126），真实字段名 message_seq，来自 event.msgSeq；message_id / real_id 仍是消息标识。缺 / 空 / 0 / 负 / 小数 / 溢出 / 错类型的序号不作证明，消息仍保存。私聊 message_seq 是发送方局部值，不作连续证明。testdata 只有编造对拍样本，没有真实历史录帧；本轮未连真机。

## 四、收图下载

### 4.1 技术路线

> 陌生人下载规则已被 ADR-243 改为：陌生人的图照下（2026-11-02 用户：“QQ什么样就什么样，那边会下那这边也会下，不下她没法理解图”）。自检期间的图同样下载；准入、观察写入照旧，非白名单群仍然整条不存。

```
适配器翻译 → Intake.Submit → 入库（segs 里 image/emoji 有 URL、hash 为空）
                                  │ Config.OnStored（会话队列里同步调，只许排任务）
                                  ▼
                    mediafetch.Fetcher.Enqueue（不阻塞：队列满就丢，留给补扫）
                                  │ 要等重试或记终局状态时才写 media_fetch（开始下载时不写，见第十二节）
                                  ▼
         工人 ×2：校验地址 → 下载（流式，边写临时文件边算 sha256）→ 认文件头
                  → 落盘到 <数据>/go/media/qq/<aa>/<hash> → 读头取宽高和动图 → 事务里回填 hash、media_fetch 记 done 和元数据
```

- **新包 `native/internal/mediafetch`**（常驻，不是一次性的包，AGENTS 的"按寿命拆包"）：`Fetcher`（队列、工人、状态）、下载器（地址校验、限额、认格式）、刷新接口 `Refresher`（由装配处接适配器的 `Message`，没有适配器就不刷新）。**不放进历史导入工具，也不放进 `mediastore`**（mediastore 只放文件读写，不联网）。
- **写文件的入口放在 `mediastore`**：`Put(data, platform, r io.Reader, maxBytes) (hash string, size int64, err error)`。ADR-308 起直接使用平台媒体目录，不检查导入状态；临时文件写在同一目录卷下的 `<媒体根>/<平台>/.tmp/`；流式算 sha256，超过 `maxBytes` 就中止并删临时文件；写完 fsync、关闭，再 `rename` 到 `<aa>/<hash>`；目标已存在就删临时文件、校验大小一致即算成功（内容寻址，同 hash 同内容）；逐级拒绝符号链接和联接（照 `OpenMedia` 的检查）；`rename` 在 Windows 上可能被杀毒软件短暂占用，重试 3 次、间隔 50 毫秒。
- **回填 hash 在 `store`**：新方法 `SetSegHash(seq, idx, kind, url, hash)`，一条 SQL 用 `json_set` 只改 `$.segs[idx].hash`，条件是该下标的段 kind 在 image/emoji、`url` 和入队时记的一致、现有 hash 为空，三条同时成立才写；成功后走 `commitChanged(conversation)` 让界面刷新。**不读出来再整体重新编码**：`types.Seg` 以后加字段，旧代码重新编码会把不认识的字段丢掉。
- **失败不覆盖原段**：段里的 `url`、`file` 原样留着（待办要求）。"取不到"这类状态记在 `media_fetch` 里，不记在段里。
- **不阻塞入库和回复：** `OnStored` 回调里只做一次非阻塞的 `select` 入队；工人是另外的 goroutine，不占会话队列。下载慢、卡死、磁盘满，都不拖慢消息入库、不拖慢回合。
- **哪些段会下载：** QQ 消息成功入库（有 seq）、不是重复到达（`Duplicate`），`Kind` 是 `image` 或 `emoji`、`URL` 非空、`Hash` 为空就排队。陌生人私聊、自检、补录和她自己的号发的（别的设备上发的）图照下，不要求 `Stored.Observed` 为真；同一段由主键保证幂等。准入不改：非白名单群照旧整条不存，不会走到下载。
- **换新链接：** 下载遇到 400、403（rkey 过期；QQ 图片服务器 `multimedia.nt.qq.com.cn` 过期时回的是 400，正文 `{"retcode":-5503007,"retmsg":"download url has expired"}`，2026-11-02 真机补扫见到）或 404/410（`expired`）时，先试一次 `get_msg`：用这条消息的平台消息号（查 `channel_msgs`）向 SnowLuma 要新地址，按 `File` 名对上段（找不到同名的，才在张数相等时按先后对；对不上就放弃刷新，不猜）。刷新后的地址再下一次，仍失败就是终局。每条消息每次运行只刷新一次。刷新走适配器的查询层，和补录共用同一个限速（见 5.3）。**问协议端没问成不等于换不出新地址**（没连上、超时、连接断了、核验没通过、被取消）：这时不记 `expired`、不写 `media_fetch` 行，段原样留着，由下一次补扫再问；只有协议端答了、但不行（拒绝、回来的消息对不上、没有平台消息号、没有对得上的图）才记 `expired`。首次补扫等到连接成功；下载或刷新途中仍可能断线，不能因此把积压图误记成终局。没问成的结果在每次补扫请求时清掉，不被"同一条消息 10 分钟内共用一次查询"的缓存挡住。
- **商城表情兜底：** 地址主机是 `gxh.vip.qq.com`、路径以 `raw300.gif` 结尾，下不到（404）时换 `raw300.png` 再试一次（Bun 的做法，qq.md "两个兜底"）。
- **连接后补扫（补“已经收到、只存了地址的那批”）：** `Fetcher.Start` 只起工人，首次连接和每次重连成功（`StatusConnected`）后各扫一遍近 7 天的事件，挑出“image/emoji、有 URL、hash 为空、`media_fetch` 里没有终局状态”的段入队，不要求 `observations` 里有角色看到了，陌生人和自检期间的图同样补下。SQL 用 `json_each(events.segments_json, '$.segs')`。导入来的旧段没有 URL，不会被挑中，它们的空 hash 是另一回事（源里就没有文件，`media-check` 单独报）。另外，`failed` 的行在连接后的那一遍里最多再试一次，事件超过 24 小时的不再试。Bun 的 `repairMedia`（参考页 qq.md “下载与存储”）只是“每次连上对没下成的再下一次，同一条每次运行只试一次”；“`failed` 再试一次”和“24 小时”是 Go 这边我定的，见 4.2。
- **已下载图片补元数据（feat/media-meta，2026-11-02）：** `Fetcher.Start` 另起一个后台任务，用 `(event_seq, idx)` 游标扫所有 `state=done` 且 width 或 height 为 0 的行，不受近 7 天窗口限制。每页最多 64 张、每张之前等 50 毫秒（我定的）；`mediastore.OpenMedia` 保留路径和句柄检查，不读全文算 hash。文件缺失直接跳过、不刷日志，认不出尺寸保留 0；每次启动只顺序扫一遍，失败行不会卡住后面的图。补成后提交并通知会话刷新，关闭时取消并等待退出；不占 desktopread 的 queryGate。
- **收尾：** `Fetcher.Close`：取消在途下载、删临时文件、等工人退出，之后不写库不写文件（LESSONS："插件关掉以后还在路上的活不能再碰库"）。装配处的关机顺序放在 `app.go` 的"2 关入口"一步里，在 `intake.Close` 之后、关库之前。启动时清掉 `.tmp/` 里超过 1 小时的残留。

### 4.2 具体参数

| 项 | 值 | 谁定的 | 理由 |
|---|---|---|---|
| 单张上限 | 20 MB | 照 Bun | qq.md "下载与存储"；超过记 `too_big`，不下载（先看 `Content-Length`，没有就流式数到上限中止） |
| 单次请求总超时 | 20 秒；建连 5 秒 | 照 Bun（20 秒）；建连 5 秒我定的 | CDN 在国内，慢过 20 秒基本是链接坏了 |
| 同时下载 | 2 | 我定的 | 一张图通常不到 1 MB；补录一次可能涌进几百张，2 路够慢慢消化又不抢带宽、不让磁盘突刺；放大十倍时量一次再调 |
| 重试 | 共 3 次：立即、30 秒后、5 分钟后；只对网络错误、超时、5xx、429 重试 | 我定的 | 链接约一小时有效（第三节的缺省值），5 分钟内的重试都在有效期里；更晚的由重连后的补扫接手 |
| 终局状态 | `done`、`expired`（404/410，或正文含 expired）、`too_big`、`not_image`（认不出是位图）、`blocked`（地址主机不在白名单）、`failed`（重试用完） | 我定的 | 各自界面上的话不同，见 4.3 |
| 内存队列长度 | 500 | 我定的 | 满了丢掉新的，`media_fetch` 的 pending 行和补扫会接住；不无限堆 |
| 补扫窗口 | 近 7 天 | 我定的 | 链接早已失效的，只剩 `get_msg` 一条路，SnowLuma 的库也未必还留着；7 天后不再试 |
| 能访问的主机 | 完全匹配 `multimedia.nt.qq.com.cn`、`gchat.qpic.cn`、`p.qpic.cn`、`gxh.vip.qq.com`、`qq.ugcimg.cn`、`tianquan.gtimg.cn`；后缀匹配 `.qpic.cn`、`.nt.qq.com.cn`、`.ftn.qq.com`（QQ 文件中转，2026-11-06 主人报视频被拦后补） | 我定的 | 来自 `index.mjs` 里生成地址的几处（`:120801-120812`、`:21658`、`:332105`）；真实地址里还有没有别的域名没验证（不能读 `data/`），真机第一天看日志里 `blocked` 的主机名（只记主机名）再补；2026-11-03 真机日志拦下 `qq.ugcimg.cn`、`tianquan.gtimg.cn`，主人同意按完整域名加上 |
| 能接受的文件头 | PNG、JPEG、GIF、WebP、BMP、AVIF、HEIC（看文件头，不看扩展名） | 我定的 | 规格-QQ渠道第五节"看内容不看扩展名"；SVG、HTML、压缩包一律不收 |
| 陌生人的图 | 照下载，实时入队和补扫都不看是否有角色观察 | 用户 2026-11-02，ADR-243 | “QQ什么样就什么样，那边会下那这边也会下，不下她没法理解图”；准入和 observations 写入不改 |
| 开关 | `qq.media.download`，默认 `true` | 我定的，**待主人点头**（十.8） | 出了问题能一键关掉；只关下载，不影响入库。要同步改 `native/internal/config`、`sumika.example.yml`、参考页 |
| `failed` 的补扫再试 | 重连后的补扫对 `failed` 的行再试一次（试过这一次的不再碰），事件超过 24 小时的不再试 | 我定的 | Bun 的 `repairMedia` 每次连上都对没下成的再下，没有次数和年龄的限制；Go 这边收紧：连败三次的多半是链接坏了，一次补扫足够，不无限重试 |
| 同一条消息换地址的结果共用 | 10 分钟、最多缓存 512 条；出错的结果每次补扫请求时清掉 | 我定的 | 一条消息的几张图只问协议端一次（5.3 的限速）；缓存的是换来的新地址，链接约一小时有效，10 分钟内共用不会拿到过期的 |
| 启动清临时文件 | `.tmp/` 里超过 1 小时的 | 我定的 | 正在写的临时文件不会这么老，进程被杀留下的才会 |
| 补扫翻页 | 每页 200 条事件，按 seq 往后翻 | 我定的 | 12 万行库上挑候选 0.5 秒（M13），一页不占库太久 |
| 视频、语音、文件 | 不下载 | 我定的 | 段里没有地址（第一节） |
| 宽高、动图 | 下载落盘后只读文件头，和 hash 回填同一事务写进 `media_fetch`；mime 不存 | 我定的 | `image.DecodeConfig` 取尺寸；JPEG 长边 ≤2048 且 EXIF 方向 5～8 对调宽高，与界面一致；GIF 扫块数帧，超过一帧才是动图；WebP 共用 `mediastore.WebPFrameCount` 的 ANIM 判定，不解码像素。认不出尺寸为 0，界面保留 4:3 占位 |

### 4.3 `media_fetch` 表（每个平台库一份，`chat/0006_media_fetch.sql` 建表，`chat/0007_media_meta.sql` 只加列）

| 列 | 说明 |
|---|---|
| `event_seq` | 事件的 seq（本库内唯一） |
| `idx` | 段在 `$.segs` 里的下标 |
| `kind` | `image` / `emoji`（入队时记，回填时核对） |
| `url_sig` | 入队时地址的 sha256 前 16 位（不存地址本身，rkey 在查询串里；回填 hash 的条件用它和事件里现在的地址算出来的比） |
| `state` | `pending` / `done` / `expired` / `too_big` / `not_image` / `blocked` / `failed` |
| `tries`、`next_at`、`updated_at` | 重试次数、下次最早时刻（毫秒）、最后改动 |
| `hash` | `done` 时的 sha256 |
| `last_error` | 短原因（"超时""403""非位图"等），**不含地址和查询串** |
| `width`、`height`、`animated` | INTEGER NOT NULL DEFAULT 0；展示尺寸和动图标记，下载和后台补扫写入；我定的：直接加三列，不另建相同主键的小表 |

主键 `(event_seq, idx)`，写入是整行覆盖（`PutMediaFetch`）。界面显示：段的 hash 为空时，界面读这张表给占位文字（`pending` 下载中；`expired` 图片已过期，QQ 那边取不到了；`failed` 下载失败；`too_big` 图片太大没下载；`blocked`、`not_image` 没有下载）。读这张表的界面改动是第 7 步，**不在 `OnStored` 的路径上**。

展示元数据由 `store.enrichDisplayMedia` 在原有展示查询事务里对本页 event_seq 做一次 IN 查询，按 `(event_seq, idx)` 填给 desktopread；hash 对得上才用于对应图片段，legacy 自带字段优先。翻页、单事件和订阅区间重查共用此路径，读取消息不打开媒体文件。

转发图片、表情、卡片封面也调用同一个 `mediafetch.imageMeta`，写入 QQ 的 `forward_media_fetch`（qq/0005）；旧行启动补扫和投影见[链接和合并转发 3.7](规格-链接和合并转发.md#37-转发里的图)。

临时库验收（2026-11-02，合成数据）：200 条消息含 100 张图，元数据额外查询每轮 1000 次，三轮平均每页增加 0.436992、0.406403、0.414321 毫秒。370 张已下载 PNG 补扫用时 20.0689081 秒，18.436 张/秒，每页 64 张、间隔 50 毫秒，日志为空。未读取真实媒体库。

回归验收：根包、mediafetch、store、desktopread、mediastore 的 `go test -short -count=1` 与同范围 `go vet` 均退出 0。五个新增测试均先改坏对应路径确认失败：下载丢元数据、禁止启动补扫、展示丢库元数据、UI 丢宽高、UI 丢动图标记；恢复后通过。另把消息投影打开文件加回去，计数器报“读消息打开媒体 5 次，要 0”，恢复后为 0。UI Tester 量到图文框 30×80、纯图扣边框后比例 0.375，位图到达前后框不变、左右像素为图片色；动图取 tier=0 原文件并解出两帧。动态 WebP 冒烟读出 30×80、animated=true。

### 4.4 敌对输入 × 入口

地址由协议端给，但协议端会把发送者能影响的字段原样带出来；`Seg.URL` 不能当可信。

| 形状 | 下载器 | `get_msg` 刷新 | 补扫 |
|---|---|---|---|
| 主机不在白名单（含 `127.0.0.1`、SnowLuma 自己的 WebUI 端口、内网地址） | 拒，记 `blocked`，不发请求 | 刷新回来的新地址同样过白名单 | 同 |
| 白名单主机重定向到别的主机 / 内网 | 每一跳重新校验，最多 3 跳，出了白名单就中止 | — | — |
| 域名解析到不是外网的地址（回环、链路本地、RFC1918 私网、CGNAT 100.64.0.0/10、保留和文档网段、组播、IPv6 的 NAT64 等） | 拒（拨号层检查解析后的地址）。判断用 `internal/netsafe` 的 `PublicIP`，和链接取数（`linkreader`）同一份白名单，不另写黑名单；**只在它上面放行 198.18.0.0/15**：主人机器上的代理可能用它做假地址（fake-ip），挡了会让所有下载失败【我定的】 | — | — |
| `file:`、`ftp:`、`data:`、`base64://` | 适配器的 `mediaURL` 已经不存；下载器再核一遍 scheme，只认 http、https | — | 同 |
| 地址超长、带控制字符 | 同上 | — | — |
| 响应声称很小、实际很大 / 无限流 | 流式计数，到上限中止，删临时文件 | — | — |
| 响应是 HTML 错误页、"expired" 文字、SVG | 看文件头，认不出位图就不落盘：404 且正文含 expired 记 `expired`，别的记 `not_image` | — | — |
| `File` 名带路径（`../`、盘符） | 不参与路径：文件名永远是 hash，不用平台给的名字 | 只拿来对段，对不上就放弃 | — |
| 同一内容不同 `File` / 不同地址 | 内容寻址，写一个文件，两个段都回填 | — | — |
| 下载来的内容的 hash 对不上回填条件里的预期（段已被别的路径填过、事件被改） | 条件不成立就不写，文件留着（它是合法的内容） | — | — |
| 日志、`last_error`、提醒里出现地址 | 只写主机名和原因；查询串（含 rkey）不进任何日志、库、提醒 | 同 | 同 |

QQ 私聊、QQ 群两种场合都走同一条路；本地私聊、本地群聊不涉及。

## 五、断线补录

> **按 ADR-239 和 B14 实测实现**：空档默认都补，没有接管时刻或 72 小时下限，起点 through 减 60 秒；白名单群默认一起补，不记 pre-takeover。群可向前翻最多 5 页，私聊只取无锚点的 100 条首页；更早取不到的记缺口。历史、实时按平台消息号去重。第 7 步界面缺口标记不在本次范围。

### 5.1 技术路线

- **适配器查询层（ADR-275 / 278）：** HistoryPage 区分 Messages、Latest 和 Continuous；当前协议端不给 Continuous。群历史另读取正 int64 的 message_seq 到 Inbound.MessageSeq，跨页去重后无空号且首尾时间包住目标区间才作为证明。缺有效序号只失去证明，不丢消息。缓存旧尾不延伸为当前时刻；私聊序号不参与。
- **翻译入口（坑 1）：** SnowLuma 的历史帧**没有 `self_id`、没有 `post_type`**（`index.mjs:38250`），现在的 `translate()` 拿 `self_id` 当 `account`，`sink.Inbound` 又拿它和配置的号比，原样喂进去会整条被拒。查询层先把配置的号写进 `self_id`、写上 `post_type`（发言人是自己的号写 `message_sent`，否则 `message`），再走现有的 `translate`，这样翻译规则（私聊对方取 `target_id`、`0` 号的处理、段翻译）只有一份。返回前核对：翻出来的会话必须就是请求的会话（群号对上，私聊对方对上），不一致整条丢弃并记一行。
- **补录协调器（app/backfill.go）：** receive 且 enabled 时装配。connected 等 Ready（最多 60 秒）并稳定 30 秒；重复 connected 合并。10 分钟内无待扫描缺口时推迟重连轮次，不提前改检查位置。在线每分钟只挑 open 与迁移恢复的 last_try_ms=0 blocked；已扫描 blocked 不再定时复查。断线取消并等旧轮退出，连续边界仍由覆盖事务结算。
- **一轮怎么走：**
  1. 新窗口 S = 账号检查位置减 60 秒；待扫描 open 和迁移恢复的 blocked 可拉回起点，群最多五页、私聊一页。已扫描 blocked / abandoned 不拉回起点；新窗口与旧洞重叠时从其终点之后继续，只登记新尾段，连续水位仍被旧洞钉住。首次无检查位置仍从已有 QQ 历史最早处补。
  2. 会话清单：主人的私聊优先；到期缺口优先于最近活动，防止旧会话饿死【我定的】；再取本账号最近活动的其他私聊（最多 30 个），其余记 budget 等到期；最后白名单群按活动排，尚无记录的白名单群也查询。发现不了无记录的新私聊（get_recent_contact 是占位）。
  3. 群先无锚点取 100 条，再取上一页最旧新消息的 message_id + reverse_order，最多 5 页。每页必须包含请求锚点；同号的时间不能改变；新消息必须让锚点向旧方向移动（同秒可换号）。重复页、只有锚点、只添较新消息记 stalled；断页或同号改时间记 broken-page，不能拿孤立旧消息的时间证明中段已覆盖。私聊仅无锚点首页，没覆盖 S 记 page-cap。会话所有可用消息按时间稳定排序，提交 S 之后（含 S）的消息，逐条 Submit(Backfill:true) 并等 Ticket；私聊绝不按 message_seq 排序。
  4. 拿到多少存多少。群序号去重后须唯一、无空号（max−min=条数−1）、跨页同号的时间和序号一致，首尾时间包住目标且消息提交后才填平。私聊、有空号或缓存旧尾不能证明的扫一次后 blocked，不再定时重查；budget 未扫到保持 open。手动缺页锚点只撤销证明，有更旧新消息仍继续翻页并保存；只补原登记区间、不触发回复。
     后页错误、缺锚点或同消息时间 / 序号冲突会累计撤销证明；无效 / 重复序号即使触发 stalled 也不能沿用首页结论。所有分页退出共用循环后的最终覆盖计算，非 nil 执行错误由自动 / 手动调用者拦在结算前；回归为 TestReviewLateInvalidSequence。
  5. 消息逐条入库后，整轮旧缺口更新、新缺口登记、各账号 × 会话的连续边界和账号检查位置，在 Store.SettleQQBackfill 的一个事务里提交。检查位置可到本轮开始；连续边界只接受覆盖证明，并被最早 open / blocked / abandoned 起点封住。在线每分钟只更新检查位置，取消或结算失败不部分提交边界。
- **向前翻没选：** `reverse_order: false` 从本地最后一条向新的翻，能保证从起点连续，但要求那条老消息的号 SnowLuma 认得；导入来的老消息的号和 SnowLuma 库里的是否一致没验证。向后翻是 Bun 在真实群里用过的做法。
- **怎么知道"缺了一段"（缺口表 `channel_gaps`，新）：**

| 列 | 说明 |
|---|---|
| `id`、`channel`、`conversation` | |
| `from_ms`、`to_ms` | 未证实的毫秒闭区间；没有连续性保证时保留整个目标，不以页内消息时间缩小它。旧缺口仍单独保留，不把它与新窗口之间的跨日区间重记 |
| reason | page-cap、no-anchor、stalled、broken-page、unverified-history（扫描到起点仍无完整证明）、call-failed、budget、unsupported；旧 unverified-head / outage 保留可读，不生成 pre-takeover |
| `state` | open（等待）、blocked（受阻）、filled（已证明完整）、abandoned（停止自动重试） |
| tries、last_try_ms、created_ms、updated_ms | 只记录等待 / 已扫描事实，不再累计到三次 abandoned。open 和迁移恢复且 last_try_ms=0 的 blocked 扫一次；预算未轮到仍 open，十分钟后可继续。已扫描 blocked 不与新尾段合并、不复活；filled / abandoned 保留 |
| acknowledged_ms | QQ 0008 增加；0 未确认，非零为首次人工确认时间。缺口仍 blocked 且可查询 / 手动补录；撤销清零，不改两类位置 |

  channel_gaps 沿用 chat/0009、0012，QQ 0008 只增加提醒确认派生列。QQ 0007_backfill_continuity 不从 qq_sync 旧值造连续证明，并将旧 filled 恢复 blocked、回退检查位置，自动再扫一次。

### 5.2 检查位置和连续水位分开（ADR-275）

2026-11-03 把 qq_sync 当作处理水位的旧说法已由 ADR-275 改为两个独立量：

- `qq_sync(account, through)` 的旧列仅表示检查位置，API 改名 QQCheckedThrough / SetQQCheckedThrough；所有调用者同步迁移，不留旧方法别名。它供新窗口调度使用，在线首轮完成且 Ready 时每分钟记录 now −60 秒；从不作为覆盖证明。
- `qq_backfill_continuity(account, conversation, from_ms, through_ms)` 保存本账号、本会话的连续证明区间。首次 `through_ms=from_ms−1` 是空证明，不声称更早历史完整；旧 qq_sync 不导成证明。只有新的可信区间接到既有连续边界才延伸，并封顶在最早未完成缺口起点之前；发现更早缺口时保守收回到该空区间。
- QQ 0007 升级会撤销没有新连续证明的旧 filled，恢复为 blocked / unverified-history 并清零自动重试；所属账号检查位置退到旧区间最早起点，原来更早的保留。群缺口没有账号列，回退全部已有账号但不改变白名单。该迁移尚未发布，直接修订 0007，已发布的 0001～0006 不改；失败整份迁移回滚，成功重开不会再降级新版 filled。细则与升级回归见[补录缺口规格“旧 filled 升级”](规格-补录缺口界面与自动对齐.md#旧-filled-升级go-backfill-fix2)。
- open / blocked / abandoned 都阻止连续水位越洞；abandoned 只表示停止自动重试。检查位置可以继续推进，旧洞继续独立可见。群缺口账号归属仍是 P2，当前共享群缺口会保守约束各账号的连续证明，不猜旧行账号。
- SettleQQBackfill 整轮一个事务结算缺口、连续边界和检查位置；消息本身已由 Intake 单独提交，结算失败仍留住消息，三类结算状态全部回滚。单段手动只结算原缺口，不推进账号检查位置或会话连续边界；下一轮自动收到完整证明后再结算边界。


### 5.3 具体参数

| 项 | 值 | 谁定的 | 理由 |
|---|---|---|---|
| 配置键 | qq.backfill.enabled（默认 true）、qq.backfill.groups（默认 true） | 默认按 ADR-239 | config、sumika.example.yml、参考页同步；不保留 since/maxAge |
| 起点下限 | 首次无年龄下限；重试受已有群五页、私聊一页约束 | 首次按 ADR-239；重试约束我定的 | 旧 open 不扩成跨日新缺口，abandoned 不参与回溯 |
| 群默认 | 白名单群和私聊一起补 | 用户，ADR-239 | 仅记录的群也需要连续历史 |
| 每页条数 | 100 | 我定的 | SnowLuma 单次封顶 200，100 留一半余量，一次动作在它那边约 4 个窗口、1～3 秒 |
| 每个会话最多页数 | 群 5 页（含重复锚点至多 500 条）；私聊仅 1 页 100 条 | 原限额 + B14 实测 | 私聊带锚点只回锚点本身，更早记缺口 |
| 每轮普通私聊数量 | 主人加最近活动的其他 30 个；到期缺口优先，群另算 | 我定的 | 未轮到的记 budget，由缺口重试保障能轮到 |
| 两次查询之间的最小间隔 | 2 秒 | 我定的 | 照 SnowLuma 自己登录时补历史用的间隔（`index.mjs:307598`），它自己设的就是给 QQ 的"不可疑"间隔；会话之间也是这个 |
| 每轮查询总数 | 120（含 `get_msg` 刷新） | 我定的 | 120 次 × 2 秒 ≈ 4 分钟；用完剩下的会话记 `budget` 缺口，10 分钟后下一轮接着补 |
| 并发 | 同一时刻 1 个查询 | 我定的 | SnowLuma 那头本来就是进程级串行（`HistoryRequestGate`），多发只会排队和超时 |
| 重连防抖 | Ready 后稳定 30 秒；无 open 缺口且距前轮不足 10 分钟时延后，不提前推水位 | 我定的 | 重连抖动不放大查询，也不能跳过这次空档 |
| 单次查询超时 | 45 秒 | 我定的 | 12 次往返 × 300 毫秒 + 服务器耗时，留余量 |
| 起点重叠 | 60 秒 | 照 Bun | |
| 检查位置记录 | 每 60 秒、写 `now - 60 秒` | 间隔照 Bun，余量我定的 | 连续水位只由 5.2 的覆盖证明结算，不随定时器推进 |
| 缺口自动扫描 | 未扫描区间一轮；无证明即 blocked 停查；budget 十分钟后继续 | ADR-278 | 不做续扫游标、撤回对号、多轮追洞；手动补录独立可用 |

### 5.4 形状 × 入口

历史帧边界（2026-11-03 实施前核对）：查询层统一补 `self_id/post_type` 后走现有 translate；协调器只接已翻译的 Inbound。B14 的群分页、私聊单页和双序号结论优先于第三节的读码推断。

| 形状 / 场合 | HistoryPage 边界 | Message / 实时入口 | 协调器、Submit / channel_msgs |
|---|---|---|---|
| 缺 self_id / post_type | 处理了：补配置号；按发送者号决定 message_sent | Message 共用 historyInbound；实时仍按原帧 | 处理了：原有账号核验 |
| self_id 空、0、null、错误类型或其他号 | 处理了：仅字段缺失可补，字段存在必须匹配配置号 | Message 同边界；实时原规则 | 不进补录 |
| user_id 缺、空、0、自己的号、错误类型；sender 冒充名字 | 处理了：沿用 translate 按号认；0 不回退 sender；无合法发送者丢弃；名字不参与身份 | Message / 实时沿用 translate | 原有准入；自己的号旧消息不认领当前回声 |
| 私聊 target_id 是自己、缺失；群号或私聊对方对不上 | 处理了：翻译后核对完整 Conversation，不一致丢弃、只记原因 | Message 核对会话，实时原规则 | 不放宽白名单 |
| message_id 缺、空、0、null、布尔、对象、小数、溢出 | 处理了：丢弃；负整数合法 | Message 已核请求号；实时无号仍原规则 | 页内、跨页按平台号去重；库再去重，只补观察 |
| time 缺、空、0、负、null、对象、小数或毫秒值 | 处理了：只收正整数秒（小于毫秒量级），不以当前时间代填 | Message / 实时原规则 | 按时间稳定排序、判断起点，不读 message_seq |
| real_id 为 0；私聊两套 message_seq；伪造 post_type | 处理了：不用 real_id / seq；覆盖 post_type | Message 共用补字段路径 | 按时间和平台消息号核对首页，不用私聊序号 |
| messages 缺、null、对象、错误类型；非零 retcode、超时 | 处理了：返回错误，不冒充空页 | 查询与 get_msg 共用串行、2 秒间隔和轮次预算 | 已取页照存，未覆盖段 call-failed；断线取消则水位不动 |
| 消息段缺失、字符串、单对象、错误类型 | 处理了：复用 messageSegs 现有降级 | 同 translate | 不另写段解析 |
| 页内重复、翻页锚点重复、与实时重复 | 处理了：允许返回重复帧 | 实时照旧 | 内存和 channel_msgs 去重；不重复写事件 |
| QQ 群 / QQ 私聊首页空、只有锚点、只回最近页 | 处理了：返回消息与来源字段，Continuous=false | 不涉及 Message | 空页 no-anchor、停滞 stalled；均保留未证实区间 |
| QQ 私聊只一页，最旧时间晚于起点 | 处理了：不发带锚点的私聊查询 | 不涉及 Message | page-cap，保存可用消息但不能声称完整 |
| 下一页缺锚点、同号时间变化；同秒跨页 | 处理了：协议页返回已核验消息 | 不涉及 Message | broken-page 不扩展已证明覆盖；同秒新号仍允许扫描前进 |
| 首页 Latest / 本地末条 / 右锚点 / 越过起点 | 处理了：Latest 与 Continuous 分开 | 不再记录本地末条作证 | 自动和手动同一证据门槛；未知首页中段不清、零新增不报完整 |
| QQ 群非白名单、QQ 私聊陌生人 | 处理了：只能查所请求会话 | 原有 admit | 群名单外不查询、不入库；陌生人存原文不观察，图照下载 |
| 本地私聊、本地群聊 | 不可能：补录只装在 receive 模式的 QQ 连接 | 原有实时路径不变 | 不可能：不运行补录 |

清理脚本边界（只改 QQ channel_gaps，不碰实时数据）：

| 形状 | 协调器 / Store | 一次性脚本 |
|---|---|---|
| 有可信来源及 Continuous 的页完整包住缺口 / 只提供时间端点 | 前者消息提交后才可 filled；后者保持 blocked | 只接受完整连续证明，不以事件 min/max 代替 |
| 相邻、重叠、桥接多行 open；已有 tries | 事务合并，次数取最大；filled/abandoned 不并 | 只删除当天 16:00 后新建的 unverified-head 重复行，保留行次数取最大 |
| 原 outage 后改成 unverified-head；旧 page-cap；abandoned；其他渠道 | 原状态保留，按区间处理 | 原 outage（已重试）、日期外行、page-cap、abandoned、其他渠道不能删除；新重复行可合并进旧 open |
| 无 --yes、未知参数、非法 JSON、缺字段/null/字符串/反向区间 | 不涉及 | 无 --yes 只读；非法输入开库前报错；事务失败不部分写 |
| 覆盖证明缺失 | 不涉及 | 可合并重复行；仅用现有 filled 区间证明填平，不推断旧分页结果 |
| 本地私聊、本地群聊、非准入 QQ 群 | 原准入不变，非 QQ 不运行补录 | 非 QQ 不改；证明按完整会话隔离 |

脚本 `scripts/fix-gap-rows-.ts <qq.db> [--coverage <JSON>] [--yes]`；证明格式 `[{conversation,from_ms,to_ms}]`，相邻已证明区间可合并。只改缺口，不改事件和水位。临时寿命：升级并完成备份修补后，下次清理 scripts 时删除，待办保留删除条件。

实施选择【我定的】：首次没有 qq_sync 时用本账号已存事件的最早时间建立起点（没有事件才取本轮开始），防止首次启动跳过旧空档；每轮预算由适配器查询通道统一扣减，含同期 get_msg。新迁移用 chat/0009（视频先占了 0008），qq_sync 已有表，不另加 QQ 迁移。断线取消并等旧轮结束：排队补录执行前检查连接 ctx；Ticket.Wait 的取消只结束等待、不会撤销已开始的入库，所以停止返回前还要等这张 Ticket 收尾，保证返回后没有补录迟到写库。


## 六、禁止的偷懒写法

- 在 `OnStored` 回调里下载，或者等下载完才让 `Ticket` 完成。
- 把下载挂在 `Pipeline.Observers` 上（它收不到补录和陌生人，补录来的图会漏）。
- 下载失败就把 `Seg.URL` 清掉、或者往段里写"expired"：段保持原样，状态记在 `media_fetch`。
- 拿平台给的 `File` 名当本地文件名、拼路径。
- 白名单用"包含 qq.com"这类子串判断；只看 URL 的 scheme 不看主机；跳转不再校验。
- 为了让下载通过，把私网地址整段放开（只放 198.18.0.0/15）。
- 整段读进内存再算哈希；下载不限大小。
- 补录把空页、新私聊、零新增、本地末条或锚点重合当成完整覆盖。
- 补录把重复的、没有号的帧按单条入库。
- 补录的消息走拦截链、走闸门、触发回复、计"这小时热闹"。
- 为了补满而无限翻页，或不限速地连续发查询；把 `callTimeout` 调大而不是给历史单独设超时。
- 用补录替代"实时消息去重"：实时帧的去重照旧靠 `channel_msgs`。
- 日志里写图片地址的查询串、`message_id` 全串之外的任何消息内容。
- 在测试里读写真实的 `data/`、连真实的 SnowLuma、用真实的号码和群名（样例一律编造，号码用 10001 这样的短号）。
- 补录跑在 archive 模式或没有适配器的时候。

## 七、已知的坑

1. **历史帧没有 `self_id`、`post_type`**（5.1）。
2. **群历史无锚点时可能锚在协议端缓存末条**：本地 / 实时末条也不能证明页内完整。当前 SnowLuma 没有 Continuous，扫描结束仍保留 blocked；普通历史上翻不复用缺口覆盖状态。
3. **群用 message_id 往回翻；私聊只能无锚点首页**：B14 已验证，私聊双向序号不能判断连续；超过首页覆盖范围就记缺口。
4. **锚点本身会在下一页重复出现**；“只有锚点”只能证明没继续往旧翻，不能证明已覆盖起点。页缺锚点也不能把两边时间直接连起来；同秒多条仍可换号继续翻。
5. **一次历史动作在 SnowLuma 里可能有 12 次服务器往返**，10 秒的 `callTimeout` 会误判超时；超时以后动作在对面还在跑，下一次查询会排在它后面（进程级串行）。
6. **图片链接的有效期是缺省 3600 秒，没实测**。补录拉回来的帧里的地址是 SnowLuma 当场新签的（`convertEvent` 里解析），所以补录的图要马上下，不能排到明天。
7. **地址里有 rkey**：不进日志、不进 `last_error`、不进提醒；`events.segments_json` 里已经存着原地址（2026-10-31 的决定），这是现状，不在这里改。
8. **断线期间撤回的消息**：私聊接口省略撤回；B14 群实测也不返回已撤回消息，不增加补录撤回处理。
9. **`Seg.Hash` 事后才有**：复读按 hash 比图，空 hash 不相等；同一条图在回填前后，复读的判断不同。复读只处理实时的、群里的、此刻刚到的消息，影响很小，但要写进测试。
10. **`mediastore.Put` 的目录**：ADR-308 起直接使用平台媒体目录，不检查导入状态。
11. **Windows**：`rename` 可能被安全软件短暂占用；临时文件和目标要在同一个卷；路径含空格。
12. **代理的假地址**：下载器的"拒内网"检查不能把 198.18.0.0/15 一起拒了，否则开了代理的机器上所有下载都失败（4.4）。
13. **补扫的 SQL 要量**：`json_each` 扫近 7 天的 `events`，现在库里约一万多条，放大十倍到十几万行时要量耗时（验收 M13），超过 1 秒就改成按 seq 起点扫。
14. **磁盘**：下载不限总量，只限单张；日志里每小时记一次新增文件数和字节数，让人看得出涨得多快。真要封顶是另一件事。
15. **`origin` 的判定读"实时接管时刻"**：导入来的旧事件算不算"实时"，会决定补录来的、她的号发的消息记成 `human` 还是 `external`（5.4），动手前用临时库验。
16. **图片段的 `File`、`URL` 可能被 `mediaURL`、`mediaFile` 过滤成空**：没有 URL 的段下载不了，也不会被挑进队列，不是 bug。
17. **窗口和时间**：`through`、`S`、缺口的 `from_ms/to_ms` 都是毫秒 epoch（UTC），不拼时区；日志里给人看的才转本地时间。

## 八、验收

测试只用临时数据目录和假协议端、假 HTTP 服务器、假时钟，样例全部编造。`M` 开头是收图下载，`B` 开头是补录。每一条都要有对应的测试或一行日志（LESSONS）。

**收图下载**

- M1 入库不等下载：假服务器对这张图卡住 30 秒，`Ticket` 在 1 秒内完成，同一会话的下一条消息照常入库。
- M2 成功路径：文件落在 `<媒体根>/qq/<aa>/<hash>`，`hash == sha256(内容)`，`OpenMedia` 能读出；`events.segments_json` 里该段的 `hash` 回填，同一行的其他字段（`url`、`file`、别的段、`tags`、`line`）逐字节不变；`media_fetch` 为 `done`。
- M3 两条消息同一张图：一个文件、两个段都回填。
- M4 不是位图（HTML 错误页、SVG、压缩包）：不落盘，记 `not_image`；404 加正文含 expired 记 `expired`。
- M5 超限：`Content-Length` 超过上限不发正文；没有 `Content-Length` 的无限流在上限处中止，临时文件被删。
- M6 重试：500 / 超时重试 3 次（假时钟看间隔 0、30 秒、5 分钟），用完记 `failed`；403 先走 `get_msg` 刷新，新地址下载成功则 `done`；刷新后仍失败记 `expired`；刷新按 `File` 名对段，对不上不猜。
- M7 敌对地址：白名单外的主机、重定向到 `127.0.0.1`、解析到私网的名字、`file:`、`ftp:`——全部 `blocked`，且假服务器收到 0 个请求；198.18.x.x 放行。
- M8 下载范围：陌生人私聊、自检期间、她自己的号发的图照下；`Duplicate` 的不重复入队；`qq.media.download: false` 一张不下；archive 模式不构造下载器；非白名单群照旧整条不存。
- M9 关闭：`Close` 时在途下载被取消、临时文件删掉、关闭后不写库不写文件（用卡住的下载在关库前后各试一次）。
- M10 崩溃恢复：入队后进程"死"在回填之前（只有 `pending` 行或连行也没有），重启后补扫能补上；补扫只看近 7 天；导入来的没有 URL 的段不被挑中。
- M11 回填条件：段在下载期间被别的路径填过、事件的地址变了，`SetSegHash` 不写；`json_set` 不动别的字段（对一条带了未知字段的 `segments_json` 回填，比较前后逐字节）。
- M12 日志卫生：用编造的 rkey 串，扫日志、`last_error`、提醒、`media_fetch`，一个字都不能出现。
- M13 放大十倍：一次涌入 500 张图，量总耗时、内存峰值、入库 `Ticket` 的 p99 延迟、goroutine 数、磁盘占用；补扫在 12 万行 `events` 上的耗时。数字抄进提交说明。
- M14 真机（要主人在场点头）：收一张真图，看文件落盘、桌面能看到；日志里没有 `blocked` 的真实主机，有就补白名单。

**补录**

- B1 去重：补录的一条与实时已有的同一平台消息号，不新增事件，只补"看到了"；没有号的帧补录丢弃。
- B2 标签和不开口：补录的事件 `tags` 含 `backfill`；200 条含 @她的补录，拦截链、闸门、内核的调用数都是 0，热闹计数不变（对应规格-渠道中间件第十六节 B）。
- B3 起点和停止：S = through 减 60 秒；连续群页翻到 S 或更早停止，最多 5 页；未到 S 的重复页、只有锚点、只添较新消息记 stalled；断页或同号改时间记 broken-page。同秒跨页可前进。私聊只取无锚点的 100 条首页，没覆盖 S 记 page-cap。首页最新性未知时缺口记到本轮开始，不能填平旧缺口。
- B3b 无下限：导入水位早于当前时间 46 小时，假钟再过 30 天仍从同一水位减 60 秒补；不读交接记录、不要求 since、不生成 pre-takeover。首次无水位从已有历史起点补。
- B4 历史帧翻译：没有 `self_id`、`post_type` 的帧能翻成 `types.Inbound`；自己发的按 `user_id` 认，私聊对方取 `target_id`；帧自带的号和配置不一致、会话对不上请求、没有 `time` 的，被丢弃。
- B5 空页：QQ 群与 QQ 私聊、已有和新会话都记 no-anchor / blocked；空集不提供完整性证据。
- B6 中途出错：第三页超时，前两页照存，记 call-failed；混合坏页保留有效消息并返回错误，不能冒充完整页。
- B7 断线中止：连接掉了，一轮立刻停、不再发请求、不再写库、`through` 不动；重连后重新开始，已存的靠去重不重复。
- B8 限速：假时钟下，相邻两次查询间隔不小于 2 秒；任何时刻在途的查询不超过 1 个；一轮查询总数不超过 120，剩下的会话记 `budget`，下一轮接着补。
- B9 防抖：10 分钟内连续三次 connected 只起一轮；就绪不满 30 秒又掉线的不起。
- B10 连续水位：只由可信覆盖推进，open / blocked / abandoned 不得越过；在线定时器和实时单条只影响检查位置。检查位置、连续边界与缺口事务一致，结算失败和中止保留旧值。
- B11 缺口（ADR-278）：私聊、群空号、缓存证明不了的扫一轮后 blocked，不再定时重查；预算未扫到保持 open。群无空号序号须跨过目标两端才 filled，旧洞关掉后未知新尾段仍可见。已有受阻段手动补录真实入库、去重、按平台时间排并标补录；确认 / 撤销提醒不推进任何位置。
- B12 回声：补录的、她的号发的、早于回声窗口的消息不调 `ClaimEcho`；一段正在发送的同文字回复不被补录偷走认领（先交补录的旧消息，再交回声，看认领还在）。
- B13 补录的图也下载：补录来的消息（带 `backfill`）里的图进下载队列，陌生人的图照下（2026-11-02 用户决定，ADR-243）。
- B14 真机：2026-11-02 已做，见 3.x。群两页接上且锚点重复，私聊带锚点只回锚点本身，私聊 seq 两套，实时和历史 message_id 相同，已撤回群消息不返回。本次未连接真实协议端，不重复实测。
- B15 放大十倍：补录一万条（分多个会话、多轮）的耗时、内存、入库延迟；十个角色、上万条消息、很长的历史各试一遍，数字抄进提交说明。
- B16 回归：现有的契约测试、`channel_msgs` 去重、`Intake` 的补录测试全部原样通过；archive 模式不起补录。

## 九、分步

每一步单独提交、能整个撤掉。顺序：1 → 2、3 可以并行 → 4（收图）→ 5（补录）→ 6、7。

| 步 | 做什么 | 验收 | 撤回 |
|---|---|---|---|
| 1 适配器查询层 | Message 已有；本次补齐 Caps.History、HistoryReader、HistoryPage，复用翻译、串行、2 秒间隔及 45 秒超时，补录轮次统一预算 | B4、B8；形状见 5.4 | 撤掉历史可选能力 |
| 2 媒体库写入 | `mediastore.Put`（临时文件、sha256、上限、rename、拒联接；ADR-308 起不检查导入状态） | M4、M5 的文件一半；并发写同一个 hash；Windows 路径含空格；历史改坏：不核大小上限、不过当时的屏障各红一次；该屏障后被 ADR-308 移除 | 删掉函数，没人调 |
| 3 表和 store 方法 | media_fetch 已有；本次加 chat/0009_channel_gaps、qq_sync 运行期读写、会话活动和缺口读写；qq 最高仍为 0003 | B1、B5、B10、B11，原迁移回归 | 只加表，不改原历史 |
| 4 收图下载 | `mediafetch` 包、`OnStored` 分流、启动补扫、`get_msg` 刷新、配置 `qq.media.download`、关机顺序；`config`、`sumika.example.yml`、参考页同提交 | M1～M13；改坏检查：去掉白名单、去掉流式上限、去掉 `url_sig` 条件各红一次；M14 等主人 | 配置关掉即停；撤掉提交 |
| 5 断线补录 | 本次完成：连接协调器、qq_sync 水位、channel_gaps、旧补录回声例外、enabled/groups 配置；不读接管时刻，无年龄下限 | B1～B13、B15、B16；B14 已实测，分页按 3.x | 配置关闭即停；撤掉改动 |
| 6 命令行补一段（**默认不做**，见主人要定的第 4 条） | `sumika-ctl backfill` | — | — |
| 7 界面最小改动 | 图片段的占位文字读 `media_fetch`（下载中 / 已过期 / 失败 / 太大）；会话里"这里缺了一段"的标记读 `channel_gaps`。照项目技能 `.claude/skills/mygo-maintenance/SKILL.md` 和 LESSONS"先读组件库的现成写法"，不自己拼；过一遍[设计自查](设计自查.md)；占位的高度先按图片的比例占住（LESSONS 虚拟列表那条），回填后用 `ui.NewTester` 量锚点不跳 | 量出来的数：回填前后列表锚点偏移为 0；十个会话、上万条的帧耗时 | 撤掉提交，界面回到"尚无本地媒体" |

## 十、要主人定的

> 第 1、2 条由 ADR-239 定了；第 3、5 条由 3.x 的 B14 实测回答。本次只在临时数据和假协议端验收，未启动或连接真实程序。

1. **空档默认都补**（用户，ADR-239）：包含 Bun 停写到 Go 接管之间约 46 小时，没有接管时刻和 72 小时下限。
2. **白名单群默认一起补**（用户，ADR-239）：groups 默认 true，不改变准入或群里回复的权限。
3. **B14 已完成**（2026-11-02）：群能向前翻；私聊只取无锚点首页；历史和实时平台消息号一致。结论见 3.x。
4. **sumika-ctl backfill 默认不做**：手动补一段和界面缺口标记另在第 6、7 步，本次不增加命令，也不提供 since 配置。
5. **撤回消息不特别处理**：B14 群历史不返回已撤回的，私聊接口同样省略。
6. **已定：陌生人（不在 `privateAllow` 的私聊）发的图照下载**（2026-11-02 用户：“QQ什么样就什么样，那边会下那这边也会下，不下她没法理解图”，ADR-243）。实时回调不看 `Observed`，补扫不查 `observations`；准入和观察写入不改。
7. **要不要存图片宽高、写 `blobs`/`blob_refs` 两张表。** 我先不存不写：运行期没有任何代码读它们，宽高由界面 worker 读文件头补（规格-界面接Go后台 5.3），内容寻址的文件本身就是"blob"。要的话在第 4 步加，回填脚本遍历媒体文件就行，不用重新下载。

8. **收图下载默认开启，新版本第一次启动会改写近 7 天已有事件的 `segments_json`，并用这台电脑的网络向 QQ 的图片服务器发请求。**【我定的，待主人点头；这条本该先问，因为属于"改真实数据"】补扫会把近 7 天里"有图片地址、没有本地文件"的事件（接管后收到的，加上导进来的里带地址的）逐张下载，只往这些事件的段里加一个 `hash` 字段（按字节拼接，别的字节不动，M11 测过）；迁移 0006 只加 `media_fetch` 表。我的建议：第一次启动新版本之前，关掉 sumika.exe，拷一份 `data/go/chat/qq.db`（连同同目录的 `-wal`、`-shm` 文件）；想更保守，合并时先把 `qq.media.download` 的默认值设成 `false`，等你看过第一批再开。你不开口的话，合并以前由主会话先问你。

## 十一、影响清单（同一个提交里改了的）

- 本规格（新）；[docs/README.md](README.md) 的索引加一行。
- [待办](待办.md)：①"切到 Go 以后 QQ 媒体先缺的三样"一条末尾和"断线补录"一条末尾各加"见规格-QQ收图下载和断线补录"；第 6、7 步不另挂条目，规格里写着。
- [规格-渠道中间件](规格-渠道中间件.md)：第十六节 B 与本规格 5.2 保持一致，水位不越过未覆盖区间。
- [清单-QQ小号切换到Go](notes/清单-QQ小号切换到Go-2026-10-30.md)「切完以后暂时没有的」表第一行加链接。
- 以后各步实现时才改：`native/internal/config`、`sumika.example.yml`、[参考页](subsystems/qq.md)（Go 侧的补录和下载）、规格-界面接Go后台 5.2 那段"新增实时媒体的下载……由渠道媒体流水线负责"。

## 十二、收图下载和断线补录做到哪了

**收图下载已做：** mediastore.Put、chat/0006、chat/0007_media_meta、适配器 Message、mediafetch 与装配。**本次补录补齐：** HistoryPage / Caps.History、chat/0009_channel_gaps、水位和连接协调器、旧补录回声保护；实现与验收记录见本节末尾。

**和上文写法不同的两处（动手时改的）：**

1. 开始下载时不写 `pending` 行。每张图只提交一次：下载好了，`FinishSegHash` 在同一个事务里回填 hash 并写 `done` 行；要等重试才写 `pending`（带 `next_at`），记终局状态时才写终局行。进程死在中途，补扫挑的是"有地址、没 hash、没有终局状态"的段，没有行的也挑得到（M10 测了）。起因：先写 `pending` 再写 `done` 每张图三次提交，500 张图的入库总耗时比关着下载慢了一倍（0.75 秒对 1.64 秒），改成一次提交后是 0.93 秒对 1.15 秒。
2. `SetSegHash` 不用 SQL 的 `json_set`，按字节拼接：不依赖 SQLite 怎么重新编码，别的段、别的键的字节一个不动（M11 的测试用了中文、`&<>`、emoji、U+2028、控制字符转义、未知字段、小数、空白）。

**收图下载验收对照（M 开头；本次补录 B 对照见本节末尾）：**

| 条 | 测试 | 结果 |
|---|---|---|
| M1 入库不等下载 | `app.TestMediaDownloadDoesNotBlockIntake`：一张图的下载卡着，同一会话的下一条照常入库，`intake.Idle` 5 秒内返回 | 过 |
| M2 成功路径 | `mediafetch.TestDownloadSuccess`（文件在 `<aa>/<hash>`、`OpenMedia` 读得出、该段 hash 回填、去掉 hash 以后和原文逐字节一样、`done` 行）；`mediastore.TestPutStoresContentAddressed` | 过 |
| M3 两条消息同一张图 | `TestSameImageInTwoMessages` | 过 |
| M4 不是位图 | `TestNotBitmapIsNotStored`（HTML、SVG、压缩包、空内容、假 BMP、正文说过期、404、410、403、400（QQ 的过期正文，记 `expired`）、401，共 11 种，都不落盘、段里地址原样）；`TestSniff` | 过 |
| M5 超限 | `TestTooBig`（声明超限不读正文、无限流在上限处断开、刚好等于上限收、多一个字节不收）；`mediastore.TestPutCapsSize` | 过 |
| M6 重试和换新地址 | `TestRetrySchedule`（假钟看间隔 0、30 秒、5 分钟，用完 `failed`，之后不再试）、`TestTimeoutThenSuccess`、`TestRefreshOnForbidden`（按文件名对段，同一条消息只问一次）、`TestRefreshDoesNotGuess`（6 种对不上都不猜）、`TestRefreshOnlyOncePerRun`、`TestRefreshErrorKinds`（协议端拒绝记 `expired`；没连上、超时、取消不记任何状态）、`TestMediaSweepWaitsForConnection`（工人启动不下旧图，连接后补扫换新地址）、`TestRefreshUnavailableWhileResweepPassed`（问的期间补扫来过，放弃后再请求一遍）；`snowluma.TestMessageNotConnectedIsNoReply`；`app.TestMediaRefreshBeforeConnectedThenAfter`（整条链：适配器没连上不补扫，连上后换到新地址）；`app.TestMediaRefreshThroughAdapter`（经真实适配器向假协议端发 `get_msg`） | 过 |
| M7 敌对地址 | `TestHostileURLs`（22 种：白名单外、回环、内网、解析到私网 / 链路本地 / IPv6 回环 / IPv6 私网 / CGNAT / NAT64 / 保留段、解析里混了回环、file / ftp / data、带用户名、非默认端口、后缀冒充、下划线主机名、跳转到回环 / 别的协议、跳转太多；第一跳被拦的拨号 0 次、请求 0 个）、`TestHostRule`（白名单和 IP 表）；198.18.x.x 和大写主机名放行 | 过 |
| M8 下载范围 | `app.TestMediaDownloadSkips`（陌生人私聊的图下载并回填、消息照存；主人和陌生人的同一条重复到达各只下一次；`qq.media.download: false` 一张不下且不造下载器；archive 不造下载器）、`mediafetch.TestOfferFilters` | 过 |
| M9 关闭 | `TestCloseCancelsAndDoesNotWrite`（在途的被取消、临时文件删掉；下载完到写库之间关闭，不写库）、`app.TestMediaShutdownWhileDownloading`（下载卡着时关机，Run 在时限内返回） | 过 |
| M10 崩溃恢复 | `TestSweepRecovery`（没有行的、pending 的补上；终局状态的、已有 hash 的、没有地址的导入段、8 天前的、`failed` 且事件太老的不碰；`failed` 事件在 24 小时内再试一次；再补扫一遍一个请求都不多）、`TestFailedRetriedOnlyOnce`、`TestOverflowIsRecoveredBySweep`（队列满了丢掉的，队列清空后补扫自己捡回来）、`app.TestMediaBackfillOnStartup`（先关着下载收一条，再开着重启，连接后补扫补上） | 过 |
| M11 回填条件 | `store.TestSpliceSegHashKeepsOtherBytes`、`TestSpliceSegHashShapes`（15 种形状）、`TestSetSegHashInDB`、`TestFinishSegHashWritesRowInSameTx`；`mediafetch.TestBackfillConditionsNotMet`（下载期间地址变了、被别的路径填过：不回填、文件留着、状态 `done`） | 过 |
| M12 日志卫生 | `TestNoSecretsInLogsOrRows`：编的 rkey 不在日志、`last_error`、`media_fetch` 任何一列；错误文字全是固定短原因 | 过 |
| M13 放大十倍 | `TestScale500Images`（原验收，本次未重跑）：500 张约 60 KB 的图，全部交给队列 1 毫秒，`Offer` 的 p99 不到 1 微秒、最长 0.53 毫秒；全部下完 2.3～3.3 秒；堆峰值约 64 MB（含库和假服务器）；goroutine 峰值 19；落盘 500 个文件 28.6 MB；回填 500/500。`TestScaleSweepOn120kEvents`：12 万行 events（近 6 天、一半带已下好的图、200 张没下），2026-11-02 按 ADR-243 改前、改后各跑三遍；保留 observations 条件时 829 ms、1.183 s、707 ms（第二遍超过 1 秒，整次命令退出码 1）；去掉后 476 ms、648 ms、503 ms，三遍都挑出 200 条（1 页）、退出码 0。夹具不变，改后未超过 1 秒；此前 0.49～0.55 秒是原验收的两遍结果。`app.TestScaleIngestWithDownloads`（原验收，本次未重跑）：500 条带图消息一口气交给入库一站，开着下载入库总耗时 1.15 秒，关着 0.93 秒；开着下载时 500 张下完共 2.2 秒 | 改后查询三遍过；改前一次超 1 秒 |
| M14 真机 | 要主人在场：收一张真图，看文件落盘、桌面能看到；日志里有没有 `blocked` 的真实主机，有就补白名单 | **没做** |

**形状 × 入口（AGENTS「动手前先读」第 7 条；格里写测了哪个）：** 入口是 `Offer`（入库回调）、下载器、`get_msg` 刷新、补扫。

| 形状 | Offer | 下载器 | 刷新 | 补扫 |
|---|---|---|---|---|
| 缺（没地址、没 hash 字段） | 不入队（`TestOfferFilters`） | — | 没有平台号不刷新（`TestRefreshDoesNotGuess`） | 不挑（`TestSweepRecovery` 的导入段） |
| 空（空地址、空内容） | 同上 | 空内容 `not_image`（`TestNotBitmapIsNotStored`） | 回来没有图记 `expired`；没问成（没连上、超时）不记，留给下次补扫（`TestRefreshErrorKinds`） | — |
| 已有 hash | 不入队 | — | — | 不挑；下载期间被填的不覆盖（`TestBackfillConditionsNotMet`） |
| 类型不对（文字段带地址、语音带地址） | 不入队（`TestOfferFilters`） | — | 只取 image / emoji（`TestRefreshOnForbidden`） | SQL 只挑 image / emoji（`TestMediaCandidates`） |
| 超大、无限流 | — | `too_big`（`TestTooBig`） | — | — |
| 自己的号发的 / 陌生人 / 自检 / 重复到达 | 她的号别的设备发的、陌生人、自检都入队，重复不入队（`app.TestMediaDownloadSkips` 验证陌生人和重复） | 同一下载器，地址和限额照旧 | 同一刷新入口 | 不按 observations 筛选，没角色看到的也挑（`store.TestMediaCandidates`、`TestSweepRecovery`、`app.TestMediaBackfillOnStartup` 验证陌生人） |
| 敌对地址（白名单外、回环、内网、跳转、file / ftp / data、带用户名、怪端口） | 不判断，交给下载器 | `blocked`（`TestHostileURLs`） | 刷新回来的新地址走同一个下载器 | 同下载器 |
| 日志里出现地址 | — | 固定短原因（`TestNoSecretsInLogsOrRows`） | 同 | 同 |

QQ 私聊、QQ 群走同一条路；本地私聊、本地群聊不涉及（`qq.media.download` 只装在 QQ 渠道上，`withMedia` 只认 `Conv.Channel == "qq"`）。

**历史改坏检查（当时最终代码；红的是哪个测试）：** ADR-308 后已移除导入屏障，这些结果不代表当前代码的验证。 补扫放回“必须有角色看到了”的 `observations` 条件 → `store.TestMediaCandidates`、`mediafetch.TestSweepRecovery`、`app.TestMediaBackfillOnStartup` 红（分别漏挑陌生人事件、陌生人图片请求 0 次、启动补扫请求 0 次）；去掉主机白名单 → `TestHostileURLs` 红 3 个子项；`blockedIP` 恒为假 → `TestHostileURLs` 红 5 个子项加 `TestHostRule`；跳转不再校验 → `TestHostileURLs/跳转到别的协议` 红（跳到回环的那项仍由拨号层的 IP 检查挡住，两层各自都测了）；认文件头恒为真 → `TestNotBitmapIsNotStored` 红 9 个子项；把写入上限放宽 4096 字节 → `TestTooBig` 红（多一个字节那项）；去掉声明长度检查 → `TestTooBig` 红；回填时不核地址指纹 → `store.TestSpliceSegHashShapes`、`TestSetSegHashInDB`、`mediafetch.TestBackfillConditionsNotMet/地址变了` 红；`Put` 去掉大小上限、换掉导入屏障 → `mediastore` 各红 1～2 个；`withMedia` 放回 `s.Observed` 条件 → `app.TestMediaDownloadSkips/陌生人和重复` 红（陌生人图片请求 0 次）；适配器 `Message` 去掉间隔、放开串行、去掉会话核对 → `snowluma` 各红 1 个。2026-11-02 复核后加的：换新地址的“没问成”判断恒为假 → `TestRefreshErrorKinds` 三项、`TestRefreshUnavailable*` 红；补扫请求时不清出错的缓存 → `TestRefreshUnavailableAtStartupThenResweep` 红；问的期间补扫来过时放弃后不再请求 → `TestRefreshUnavailableWhileResweepPassed` 红；装配处不把 `ErrNoReply` 映射成 `ErrRefreshUnavailable` → `app.TestMediaRefreshBeforeConnectedThenAfter` 红；没发出去的查询也占查询间隔 → `snowluma.TestMessageNotConnectedIsNoReply` 红；`Message` 的错误不带 `ErrNoReply` → `snowluma` 两个测试红；旧的拨号黑名单放回去 → `TestHostileURLs` 的 CGNAT、NAT64、保留段三项和 `TestHostRule` 红。除这次按 ADR-243 重做的观察条件两项外，其余沿用原验收，原输出在 `data/dev/test-runs/qq-media-backfill/`。

**还没做的：**

1. 第 7 步：界面读 `media_fetch` 给占位文字（下载中 / 已过期 / 失败 / 太大）。现在没有 hash 的段仍显示"尚无本地媒体"。
2. M14 真机收图仍等主人在场；B14 历史查询已完成（见 3.x）。
3. blobs / blob_refs 运行期写入不做；宽高与动图已经随 chat/0007_media_meta 做完。
4. 视频、语音、文件下载；Go 侧识图；出站图片。
5. 白名单主机来自 SnowLuma 代码里的几处，真机第一天看日志里 `blocked` 的主机名再补。
6. 收图下载默认开启待主人点头（十.8）：合并前先问，并在第一次启动前备份 `data/go/chat/qq.db`。

### 12.1 断线补录实现与验收对照（2026-11-03，feat/qq-backfill）

本次实现 HistoryReader / Caps.History、HistoryPage、chat/0009_channel_gaps、store/backfill.go、app/backfill.go、连接与关闭装配、旧历史回声保护；配置只加 enabled / groups，默认 true。没有新增 QQ 迁移，没有改真实数据或启动真实程序，没有提交、推送或合并。

| 验收 | 对应测试 / 证据 |
|---|---|
| B1 去重、补观察 | TestBackfillRestartDedup；HistoryPageBoundary 丢弃无效平台号；原 AppendInbound 去重回归 |
| B2 标签、不开口、热闹不变 | TestBackfillNoReplyAndMedia：200 条含 @她，tags、gate_decisions、gate_state、outbound_parts 和模型调用计数；原 Pipeline 拦截链补录测试 |
| B3 群翻页与停止、私聊单页 | TestBackfillGroupPagination、TestBackfillPrivateOnePage、TestBackfillPartialAndEmpty、TestBackfillCoverage |
| B3b 无接管 / 年龄下限 | TestBackfillNoTakeoverOrAgeFloor：无水位从最早事件起；导入水位早 46 小时，墙钟再过 30 天仍补 |
| B4 历史帧翻译、敌对字段 | TestHistoryPageBoundary、TestHistoryPageProtocol；形状 × 入口表在 5.4 |
| B5 群空页 | TestBackfillPartialAndEmpty 的 empty-known / empty-new |
| B6 第三页出错保留前两页 | TestBackfillPartialAndEmpty/failed |
| B7 断线取消、排队不迟到写库 | TestBackfillDisconnect、TestBackfillQueuedCancellation |
| B8 共用限速、串行、预算、公平轮转 | TestHistorySharedQueryBudget、TestBackfillRoundBudget；上轮 budget 缺口下一轮优先，不因预算不足消耗“取不到”的重试次数【我定的】 |
| B9 防抖 | TestBackfillDebounceWatermark |
| B10 水位 | TestBackfillDebounceWatermark、TestBackfillDisconnect、TestBackfillRestartDedup、TestBackfillGapSurvivesOnlineWatermark |
| B11 缺口重试、填平、放弃 | TestBackfillGapRetries、TestBackfillInteriorGapCovered、TestBackfillRepeatedRoundsMerge、TestChannelGapMergesOpenIntervals、TestBackfillAbandonedDoesNotRewind、TestBackfillOldOpenPageBound；区间独立结算，合并保留次数，旧缺口不扩大新窗口 |
| B12 回声 | TestBackfillOldSelfCannotClaimCurrentEcho：旧同文字补录不认领 unknown 回复，随后真实回声仍能认领 |
| B13 补录、陌生人图下载 | TestBackfillNoReplyAndMedia：完整入库回调与图片工人；陌生人无 observations |
| B14 真机结论 | 既有 3.x 实测；本次没有连接真实协议端 |
| B15 十倍量 | TestBackfillScale10000：十个群、三轮、重复锚点按 B14；一万事件，第一个角色看一万条，其他九个角色各看千条重叠消息，共 19000 条观察；历史起点 30 天前 |
| B16 契约、原去重、Intake、模式 | 受影响包与 channel/contract 原测试；TestBackfillModesAndRefreshBudget 验 receive 关闭和 archive 不装补录 |

实现选择【我定的】：首次没有水位从已有历史最早处开始；抖动推迟的一轮补完前不推水位；上轮 budget 缺口优先于其他到期缺口，预算没轮到不消耗取数失败次数，但仍更新等待时刻；起点外旧消息仅用于判断覆盖，不新增事件。手动补录及界面缺口标记不在本次范围。


### 12.2 本次改坏检查记录

- 去掉旧补录的回声窗口保护 → TestBackfillOldSelfCannotClaimCurrentEcho 红，旧消息 8701 把当前 unknown 回复认领成 done；恢复。
- 关闭排队补录的 ctx 取消检查 → TestBackfillQueuedCancellation 红，取消结果为 nil；恢复。
- 把历史补入的 self_id 改为空 → TestHistoryPageBoundary 红（缺 self、正确 self、伪造 post、段降级等正常帧被拒），TestHistoryPageProtocol 红（私聊首页没有可用消息）；恢复。
- 查询信号量由 1 改为 2 → TestHistorySharedQueryBudget 红：“历史在途时又发了 get_msg”；恢复。

本组只运行 channel 与 channel/snowluma 的指定测试，低优先级，命令退出码 1；日志 check-20261103-010657-440.log 位于本次任务的 sol-backfill 临时目录。

- mutant-a：页上限改 1、去掉 60 秒重叠、重试次数每次加 2。TestBackfillRestartDedup、TestBackfillGroupPagination、TestBackfillPrivateOnePage、TestBackfillPartialAndEmpty、TestBackfillRoundBudget、TestBackfillGapRetries、TestBackfillScale10000、TestBackfillNoTakeoverOrAgeFloor 全部在行为断言处红；例如只查 26 次而非 120、一万条只补到 2080、重试首次变为 2。退出码 1，日志 mutant-a-20261103-014700.log。
- mutant-b：不理会 enabled=false、抖动推迟期间也推水位、Submit 改 Backfill:false。TestBackfillModesAndRefreshBudget、TestBackfillDebounceWatermark、TestBackfillNoReplyAndMedia 全部红；补录变成 tags=0、gate=200、state=1、parts=4、llm=4。退出码 1，日志 mutant-b-20261103-014711.log。
- mutant-c：取消后不等已开始的入库收尾。TestBackfillDisconnect/入库已开始 红：“停止返回时已开始的入库还没收尾，会迟到写库”。退出码 1，日志 mutant-c-20261103-014718.log。

后三组只用临时 Go overlay，工作树一直保留正常实现。合计 17 个新增顶层测试均有改坏后断言失败的记录；随后正常实现的八包短测通过。日志保留在本次任务的 sol-backfill 临时目录，临时驱动和 overlay 验收后删除。

### 12.3 验收运行记录

2026-11-03，所有 Go 检查先取得约定的 heavy-job.lock，再通过 run-low.ps1 低优先级执行；数据只用临时目录和编造帧。

- 七包回归：`go test -short -count=1 -timeout=600s -v ./internal/channel ./internal/channel/snowluma ./internal/channel/types ./internal/config ./internal/store ./internal/channel/contract ./internal/archcheck` 全部通过。包耗时依次为 79.810s、23.449s、0.175s、3.469s、31.260s、2.151s、63.574s；完整输出为 sol-backfill 临时目录的 check-20261103-013231-382.log（另存 UTF-8 副本 .log.utf8）。
- app 回归：`go test -short -count=1 -timeout=600s -v ./internal/app` 退出码 0，126.893s；日志 app-final-20261103-014727.log。B1～B13、B15、B16 对应的新增测试全部通过；原短测中按条件跳过的真实数据、慢测仍保持原条件。
- 同八包 `go vet`：app、channel、channel/snowluma、channel/types、config、store、channel/contract、archcheck，退出码 0，无诊断。
- 隔离重启冒烟：临时库＋假 WebSocket 协议端＋假模型 HTTP 服务，完整关闭再打开应用接收装配；`SMOKE_OK restarted=2 events=5 recovered_private=1 group_pages=2 overlap_rows=1 backfill_tags=3 gaps=0 history_queries=3 sends=0 model_calls=0`。只在历史中出现的私聊消息被补回，群两页接上，重叠消息只一条。日志 restart-smoke-20261103-014946.log；没有启动 sumika.exe 或连接真实协议端。
- B15：10000 条新事件耗时 11.6876111s，均值 1.168761ms；十个角色共 19000 条观察的总耗时 21.9971582s，堆采样最大值 3604888 bytes，Intake P99=2.8089ms。数据分十个群、三轮，历史起点 30 天前。
- 文档检查：`bun scripts/check-docs.ts --staged`，0 个错、2 个既有 ADR 日期提醒。D9 基线只补登记 B15 实测内存字节数 3604888 的哈希，该数不是 QQ 号。本次未运行 `go test ./...`，未提交、推送或合并。


### 12.4 连接补扫与回填锁冲突调查（2026-11-03，sol-busy）

工人启动不等于协议端已连接。`forwardfetch.Start` 和 `mediafetch.Start` 只起工人，首次连接与每次重连成功以后由 `app.sink.Status(StatusConnected)` 请求补扫。元数据补扫、临时文件清理仍在启动后运行，因为它们不查询协议端。旧实现会让 `TestForwardSweepWaitsForConnection` 在连接前取到 first，让 `TestMediaSweepWaitsForConnection` 在连接前下载、刷新各一次；移除启动补扫后，两条及应用重启、地址刷新路径通过。

回填路径：[读码] `mediafetch.succeed` → `Store.FinishSegHash` → `setSegHash`，用 QQ 平台 Store.Events 的单连接（`SetMaxOpenConns(1)`），modernc 默认 deferred 事务。顺序为 BEGIN → SELECT conversation/segments_json → Go 按字节拼 hash → 带旧正文条件的 UPDATE events → INSERT/UPSERT media_fetch → COMMIT → 通知会话。配置为 WAL、busy_timeout=5000、foreign_keys=1、journal_size_limit=4 MiB。未改 timeout、连接数、事务策略或重试次数。

临时库复现：[实测] modernc `mode=ro` 长读事务、连续短查询，及独立 Bun 进程的 `readonly:true` 长读事务、连续一万次短查询、反复一千次打开/查询/关闭，共五组；每组两个工人并发调用 200 次 FinishSegHash，均成功，**复现不出真实运行的 SQLITE_BUSY**。前四组回填段耗时依次 2.460、2.591、2.400、1.957 秒；最后一组 210.306 毫秒、Go 写进程 CPU 62.500 毫秒、结束时 Go 堆约 27.88 MiB、累计新分配约 1.60 MiB（不含 Bun 进程资源；这些是实验记录，不作性能对比）。该组 QQ 库约 416 KiB、shm 32 KiB、活动 WAL 约 3.94 MiB；文件均在临时目录，退出清理。

回填失败后：[实测] 临时触发器让事件 UPDATE 失败，hash 仍空、media_fetch 没有 done 行、已下载文件保留；移除触发器后补扫，HTTP 总请求变成两次，hash 补成、状态 done，最终媒体文件一个、35 字节。恢复会重新下载；相同内容在落盘时去重，并不是跳过 HTTP。补扫仍受近七天、状态和重试次数条件限制；没有立即只补数据库的重试任务。

参考与取舍：

- [Koishi OneBot adapter 的 ws.ts](https://github.com/koishijs/koishi-plugin-adapter-onebot/blob/master/src/ws.ts) 的 accept：先绑定可用连接的 request，再 initialize；借它把网络工作放在连接生命周期内的做法，复用本项目已有 StatusConnected 入口。
- [Crush 的 connect_modernc.go](https://github.com/charmbracelet/crush/blob/main/internal/db/connect_modernc.go) 为写事务设置 `_txlock=immediate`，原因是避免 deferred 升级死锁。本次仅作为候选参考；没有复现，不把它直接套到全部 Store 事务。
- [modernc 驱动文档](https://pkg.go.dev/modernc.org/sqlite#hdr-Connection_String)：`_txlock` 默认 deferred；本机 v1.61.1 的 tx.go/newTx 也确认未设置 beginMode 时执行 begin。
- [SQLite Isolation](https://www.sqlite.org/isolation.html)：WAL 读者不会挡普通写入；BUSY_SNAPSHOT 需要读快照之后有其他写者提交，再把旧读事务升成写事务，不能仅凭外部只读统计重合认定。
- [SQLite WAL §2.2](https://www.sqlite.org/wal.html#concurrency)：长读事务可挡 checkpoint 进度、令 WAL 增长，不等于直接挡写者。
- [SQLite busy_handler](https://www.sqlite.org/c3ref/busy_handler.html)：可能死锁时 busy handler 可以不执行，因此当秒失败并不证明 timeout 未配置。真实错误记录只有 SQLITE_BUSY(5)，不能据此确认 BUSY_SNAPSHOT(517)。

调查脚本与原始输出留在本次 sol-busy scratchpad，未加入仓库；没有读取或修改真实聊天库。


最终四包短测：`go test -short -count=1 -timeout=600s ./internal/forwardfetch ./internal/mediafetch ./internal/app ./internal/store`，退出码 0；包耗时依次 1.551s、14.466s、143.744s、19.522s。通过约定 heavy-job.lock 串行准入、run-low.ps1 低优先级运行；未运行全模块测试。


同四包 `go vet ./internal/forwardfetch ./internal/mediafetch ./internal/app ./internal/store` 退出码 0、无诊断（验收驱动输出 `VET_OK packages=4`）。

### 12.5 缓存首页与覆盖判断修复（2026-11-03，sol-gap）

本节是上一轮历史记录；其中“未填缺口钉住水位”的行为已由 sol-gap2 的 5.1、5.2、12.6 取代。

本次只读核对获准的日志与数据库副本；修复和验证只用临时目录、编造历史和假协议端，未连接真实 QQ。最新来源证明、连续分页和未填缺口的两条水位入口见 5.1、5.2、5.4。重拨理由改用当前错误的脱敏结果，不再报告空串。

- 最终短测：`go test -short -count=1 -json ./internal/app ./internal/channel/snowluma ./internal/channel/types ./internal/store` 退出 0。顶层通过依次 153、83、4、96，按原条件跳过 4、1、0、1；合计 336 通过、6 跳过、0 失败。相同四包 `go vet` 退出 0、无诊断。全部先拿共享重活锁，经 run-low.ps1 执行，没有运行整套测试。
- 回归：TestBackfillCoverage 九种分页场景，含锚点不动、最近页循环、只添较新消息、中间断页、同号改时间、缓存首页越过起点 / 翻满五页，以及正常连续页、同秒跨页；TestBackfillGapPinsOnlineWatermark 覆盖 open 与 abandoned。每种失败后再到实时消息，水位仍保持。TestWSReconnectReason 验证本次重拨错误与令牌脱敏；HistoryPage 协议测试覆盖群首页来源及混合坏页保留有效消息。
- 旧代码检查：恢复原 conversation 算法（只适配新的返回结构），上述七个失败分页场景全部红，正常连续页和同秒跨页通过。恢复后拆掉新增的断页、停滞、首页最新性判断，同样七个场景红。
- 其他改坏：缺口门槛返回 false → open / abandoned 两项红；重拨理由改回空串 → TestWSReconnectReason 红；群首页谎报 Latest=true → 四个协议场景红；忽略混合坏页错误 → 坏页和混合页两项红。全部恢复后完成上述最终短测与 vet。
- 隔离冒烟：App.Run 连假 WebSocket，首个心跳后完成登录与健康核验；真实适配器查询并翻译历史，Intake 存一条补录，再存一条实时消息；未证实尾段留下 unverified-head 缺口，through 保持，出站计划和模型调用均为 0。测得单轮 3.1683 ms，进程 CPU 采样 31.25 ms（含并发后台及系统计时粒度），堆采样 36,438,352 bytes，临时数据目录 14 个文件、1,671,499 bytes。只是一页假端的运行证据，不代表真实 QQ 恢复速度；临时冒烟代码和 overlay 验收后移除。
- 最终 B15：一万条新事件 12.1585841 s，均值 1.215858 ms；十角色累计 19000 条观察总计 21.740191 s，堆采样最大 3,262,136 bytes，Intake P99 2.0971 ms。未改变五页、120 次预算、2 秒间隔和重试次数。


### 12.6 首页核对、区间结算与缺口合并（2026-11-03，sol-gap2）
本节为历史记录；首页本地核对、未知首页中段可填平的行为已被 ADR-275 和第五节取代。

实现与规则见 5.1、5.2、5.4；参考 NapCat、Koishi 的具体入口见规格开头。没有新增迁移、配置或兼容开关，没有连接真实协议端、运行真实程序或改真实聊天库。

- 最终短测：`go test -short -count=1 -json ./internal/app ./internal/store` 退出 0；app 顶层 160 通过、4 跳过，耗时 136.930s；store 97 通过、1 跳过，11.291s；失败均为 0。同两包 `go vet` 退出 0、无诊断。均先取共享 heavy-job.lock，经 run-low.ps1 低优先级执行，没有跑全模块测试。`bun run typecheck` 实际执行 `tsc --noEmit -p tsconfig.json`，退出 0。
- 旧实现证明：恢复 HEAD 的 run、round、conversation、RecordChannelGap（保留新夹具所需的接线和返回结构），首页核对、独立区间填平、连续多轮合并、abandoned 不拖回溯、旧 open 五页限制回归均红。
- Go 改坏：去掉首页核对、谎报最新、不清连接证据、要求整轮成功才 filled、忽略区间终点、信任断页、不合并、合并清次数、不接相邻区间、合并 abandoned、abandoned 拉回起点、旧 open 扩大新窗口、突破五页、钉住在线水位、忽略提交起点，十五种分别红。在线水位断言改为比较首轮完成后的值；未入库旧页不能填平。未知首页重建 abandoned 的两个场景另先在修复前变红，再由最终短测确认通过；新建区间扣除 abandoned，仅保留之后的新尾段。
- 脚本七种改坏均红：dry-run 写库、去日期边界、合并其他渠道、清零次数、合并 abandoned、忽略覆盖证明、不接相邻证明。合成库 14→12 行，两条有证明的 open 变 filled；dry-run 文件哈希不变，重跑删除和填平均为 0；非法证明开库前拒绝。
- App.Run 冒烟：临时目录、假 WebSocket、真实 SnowLuma 适配器和 Intake；首次用实时末条核对群首页，协调器重连后用本地末条核对。两轮、两次历史查询、两条事件、缺口一条且 filled；发送和模型调用均为 0。首轮墙钟 4.5861ms；GetProcessTimes 差值为 0（短于计时粒度，不表示零 CPU 开销）；Go 堆 23,691,320 bytes、工作集 35,954,688 bytes、私有提交 86,917,120 bytes。临时应用目录 14 个文件、1,712,699 bytes；这些是本次假端进程样本，不是完整桌面和模型的资源验收。
- B15：一万条新事件 12.1771169s、均值 1.217711ms；十角色累计 19000 条观察总计 22.4017429s，堆采样峰值 3,309,464 bytes，Intake P99 2.5099ms。本临时负载低于[性能标准笔记](notes/标准-性能与效率-2026-11-03.md)的入站 P99 暂定 200ms；不外推线上速度。
- 只读原始库副本，再 VACUUM INTO 新演练副本。原来 36 行（open 14、abandoned 22），脚本合并删除 7 条本次新建重复行后 29 行（open 7、abandoned 22），重跑 0 修改；演练数据库 55,607,296 bytes。执行段 6.467ms、CPU 差值 0、堆 1,103,872 bytes；不包含复制数据库和进程启动时间。
- **离线填平限制：** 本次副本没有历史页覆盖元数据；获准日志只有请求和锚点，没有逐页返回边界。已有事件的时间范围不能证明分页连续，所以副本本次 filled=0，没有把旧 outage 冒充补齐。脚本支持 `--coverage` 输入完整连续区间，或用已有 filled 区间作证；没有证明的旧行留给新版实际拉页后结算。

完整输出位于本次 `scratchpad/sol-gap2/` 的 final-tests.log、final-vet.log、runtime-smoke.log、old-main.log、各改坏日志、abandoned-tail-before.log 和 script-checks.log；验收代码及库副本均在该临时目录，不在暂存区。真实库由主会话备份后操作。

### 12.7 ADR-275 保守覆盖结算（2026-11-05）

自动和手动共用显式 Continuous 门槛；当前 SnowLuma 无保证，未知区间保持 blocked。四条旧代码反例、改坏检查、202 条场景、六包短测和实际 App.Run 假端冒烟见[补录缺口规格第十节](规格-补录缺口界面与自动对齐.md#十adr-275-修复记录fixbackfill-adr275)。普通消息上翻仍独立于缺口状态；持久续扫、群缺口账号归属、界面和独立通知损失是未做的 P2。



## 十三、视频和文件下载后台（ADR-255、261）

范围：QQ 私聊、白名单 QQ 群实时和断线补录新入库的视频、文件，以及私聊 / 群合并转发及其嵌套里的文件。文件只分两档：文档、文本、视频、音频、图片、动图在扩展名和实头一致时 ≤100 MiB 自动下（MB=1024² 字节）；超限、压缩包、程序、无扩展名、未知或不匹配等待手动。未知大小按实际流限制，超限清半文件。手动任务最多同时两个，视频和文件共享限额、进度、取消；原件只存媒体库，不自动打开或执行。界面交 MewTypeUI 第 4 步。本地私聊、本地群聊、旧正文视频 / 文件批量补扫及 video 段形式的转发视频自动下载不在本次范围；转发视频可手动下。

参考了：本机 Telegram Desktop `data_auto_download.cpp` 的 ForceAllowed（大小包含等号）；`data_download_manager.cpp` 的 addLoadingExternal/updateLoadingExternal/cancelLoadingExternal（任务归属消息、ready/total 字节、取消归任务）。只借结构，不翻译代码。沿用媒体调研（私有存档）的保存与理解分离、[常见边界](notes/清单-常见边界情况-2026-10-26.md)的取消晚到、重复请求、部分失败和数量边界。没有增加下载依赖：现有 net/http、mediastore.Put 已提供流式写入、内容去重和失败清理。

文件参考：本机 NapCatQQ `action/file/GetGroupFileUrl.ts:31-38`、`GetPrivateFileUrl.ts:30-43`，借“文件 ID 到直链的查询”和不混用群 / 私聊凭据；`GetFile.ts:33-120` 会触发协议端下载到本地，因此不调用它。SnowLuma `<SnowLuma 目录>/index.mjs:10420-10447,10892-10920,14285-14306,21993-22010,39776-39788,135309-135334` 只读核对：file 段有 file/name、file_id/id、size/file_size、url、file_hash；get_file 只查图片 / 语音缓存；私聊 getPrivateUrl 实际使用 selfUid/fileId/fileHash，传入 userId 不参与构造。只借结构和契约，不翻译实现。沿用已有渠道层 AstrBot 调研（私有存档）的按场合选文件接口、媒体笔记的保存与理解分离。没有新依赖；格式识别借标准库 net/http、archive/zip，传输复用已有下载器。

取址：优先使用已存 file.url；缺址或 HTTP 400/403/404/410 时调用对应文件接口一次。正文群文件用 get_group_file_url(group_id,file_id)，busid 沿 SnowLuma 默认 102；正文私聊用 get_private_file_url(user_id,file_id,file_hash)。SnowLuma 转发节点明确给出的 group_id（`index.mjs:38720-38746`）保存为 file 段的 file_group，用这个原群取址；缺字段时不能拿接收转发的群冒作原群，有 file_hash 仅尝试不指定 user_id 的私聊取址，缺凭据或协议拒绝就 failed。get_file、磁盘路径、base64 均不作为下载后备。返回直链仍受原主机白名单、默认端口、DNS / 内网及逐跳检查限制；本次不根据动态返回的主机扩大白名单。实际 QQ 文件可能使用其他域名或 IP，未获准的仍 blocked，真机核对后由主人决定是否补明确的规则。

识别最多读 64 KiB，且不超自动大小上限加一；不会先把未知文件写到媒体库再分类。识别清单由 `mediastore.FileCandidate / FileKind` 给出，含纯文本常见后缀、PDF / RTF / Office / ODF、常见位图、MP4 / MOV / WebM / MKV / AVI 及常见音频。Office ZIP 要核对对应文档条目，CFB 要对应流名；头部不足以证明时按未知处理，仍能手动下。自动不匹配记 awaiting_manual；手动允许任意原始字节，仍不解压、不执行。

持久化：复用 media_fetch / forward_media_fetch，chat/0010 加 format；qq/0006 给转发结果加 format、size、cover_hash、duration_ms。format 是已核对文件的 image / video / audio / document / text，未知为空。下载回填同时绑定原 URL、文件名、file_id、file_hash 和 file_group，缺址也不能把迟到结果填到另一份文件；正文和转发均核对。文件段保持 kind=file。视频文件复用探测 / 封面与 PrepareVideo；图和动图复用 ReadMedia，文件图读取上限 100 MiB，普通图片仍为 20 MiB。消息投影只查库，不读文件头。

手动下载的文件即使扩展名不匹配，也按实头识别图片 / 视频 / 音频能力；例如 MP4 叫 .txt 时默认不下，显式下好后仍可 PrepareVideo。媒体类型来自内容，程序及未知字节不取得可播放凭据。

实施前的形状 × 入口表（沿用 4.4 的白名单，不增加域名）：

文件扩展实施前矩阵（qq-file，ADR-261；对应入口还包括 `OfferForward`、桌面三项动作、`PrepareVideo` / `ReadMedia` 与补扫；验收结果在 13.3）：

| 形状 | QQ 私聊正文 | QQ 群正文 | 私聊 / 群的转发与嵌套 | 桌面请求 / 订阅 / 取消 | HTTP / 落盘 / 展示 |
|---|---|---|---|---|---|
| 文档、文本、视频、音频、图片、动图 | 处理：≤100 MiB 自动 | 处理：同左 | 处理：同一策略，任务键含树和节点 | 处理：可见事件和逐层路径核对 | 处理：扩展名和实际头一致才自动存 |
| 压缩包、程序、未知、无扩展名、扩展名伪装 | 处理：等待手动 | 处理：同左 | 处理：同左 | 处理：手动允许下载；不自动打开 | 处理：不匹配不落盘；原件仅内容寻址 |
| 缺 / 空 / 类型错误的 URL、ID、hash、大小 | 处理：边界清理；缺址查询文件接口 | 处理：群文件接口 | 处理：不猜原群，无法取址照实失败 | 处理：不接受外来 URL、hash 当下载凭据 | 处理：禁止本地路径、base64 和任意端口 |
| 正好上限、超一字节、未知大小、刷新后超大 | 处理：大小声明和实际流双检查 | 处理：同左 | 处理：同左 | 处理：超大仍能手动 | 处理：自动超限删除半文件 |
| 400 / 403 / 404 / 410、刷新失败 | 处理：文件接口刷新一次 | 处理：同左 | 处理：已有地址可下；缺来源不猜 | 处理：短原因，显式可重试 | 处理：新地址仍检查每跳和解析 IP |
| 重复、两路手动上限、取消、退出、迟到完成 | 处理：复用现有任务 | 处理：同左 | 处理：树 / 节点 / 段定位，不撞正文 | 处理：取消等清理，退订不取消 | 处理：不提交迟到 hash；失败不冒成功 |
| 伪造平台、事件、会话、路径、节点、下标 | 不可能：自动来自入库 | 不可能：同左 | 处理：自动来自已取树 | 处理：三个入口均拒绝 | 处理：视频实头核对；图片用现有认头 |
| 本地私聊 / 本地群聊 | 不可能：非 QQ 入库不接 | 不可能 | 不可能 | 处理：拒绝 | 不可能：不发请求 |


| 形状 | 实时 / 补录自动 | 手动请求 / 订阅 / 取消 | HTTP / 刷新 |
|---|---|---|---|
| 正好上限 / 大一字节 / 未知大小 | ≤ 上限下载；超限等手动；未知流式数；get_msg 新给的合法大小也先检查 | 手动不再套自动大小限额 | 同时检查响应长度和实际流；超限删临时文件 |
| 无 URL、已下载、非视频 / 文件、负下标 | 文件缺址交文件接口；普通视频无址不排；已有 hash 不排 | 核对可见事件、路径、节点及段下标、种类；不接受调用方 URL | 无可用地址照实失败 |
| 伪造会话、平台、事件、转发路径 | 原准入不变 | 拒绝不可达路径、错误节点、非媒体段 | 不发请求 |
| 内网、非白名单、凭据、非默认端口、非法 scheme、跳转或 DNS 到内网 | 原下载边界 | 共用原下载器 | 每跳及解析 IP 都校验；刷新地址也不例外 |
| 400 / 403 / 404 / 410 | 原 get_msg 刷新一次 | 同规则；查询未完成照实失败可重试 | 不把原始 URL、rkey 带入错误或进度 |
| 重复请求 / 自动任务还在下 / 第三个手动任务 | 每段幂等 | 复用同段任务；超过两路返回忙 | 不重复连接 |
| 取消 / 退出 / 迟到完成 | 退出取消并等清理 | 取消等任务收尾；关订阅不取消下载 | 中断响应流、删半文件；终局后不再发布下载中 |
| QQ 私聊 / QQ 群 | 同一路，含陌生人、自检、补录 | 同一可见事件检查 | 同一白名单 |
| 本地私聊 / 本地群聊 | 不可能 | 拒绝 QQ 下载动作 | 不请求 |

验收：假 HTTP、临时数据目录覆盖实际入库与下载路径；上限前/等于/超一字节、声明超限零请求、无长度超限清理、手动进度、并发去重与两路门槛、取消/退出、重取后仍拦恶意地址。量接收回调与请求反馈耗时、下载吞吐、进程 CPU/内存、去重文件数和字节、取消残留；不以假服务吞吐推定真实 QQ 带宽。

### 13.1 界面调用契约

`api.VideoDownloadActions` 扩展为视频和文件的共用接口，桌面 Reader 可以类型断言；Reader 不加方法、不另建下载链路。界面直接使用 `DisplaySegment.Download` 发出的 `VideoDownloadRef`：正文 `{Source: message.Ref, Index: 原始段下标}`；转发再带 `Forward: 从根到当前树的逐层 ID, Node: 当前树节点下标`。UI 拆块 / 聚合前保留此凭据。引用媒体使用引用原事件 Source。三个动作都核对可见事件和完整路径，不接受裸树 ID、hash、URL、协议文件 ID 作为授权。archive 或 `qq.media.download=false` 返回 `api.ErrVideoDownloadUnavailable`。

函数签名：

```go
RequestVideoDownload(context.Context, api.VideoDownloadRef) (api.VideoDownloadProgress, error)
WatchVideoDownload(context.Context, api.VideoDownloadRef) (api.Subscription[api.VideoDownloadProgress], error)
CancelVideoDownload(context.Context, api.VideoDownloadRef) error
```

先 Watch，消费 `Updates()` 给出的 `api.Snapshot[api.VideoDownloadProgress]`，再 Request。初始快照先交付当前值，后续版本只在该订阅内递增；通知可以合并，不要求界面为每个网络块重绘。订阅 Close 或上下文取消只停止观察，下载归 App 管；请求上下文只约束接收请求，不因界面切走而终止下载。Cancel 返回成功时该任务已经收尾、临时文件已清理；上下文先到期则返回上下文错误，取消仍已发出。App 退出在关库前取消全部任务并等待。

进度结构：`VideoDownloadProgress{State VideoDownloadState, Received int64, Total int64, Hash string, Error string}`。Received 是本次尝试实际读取的 HTTP 正文字节，重取 / 重试从零开始；Total=0 表示未知。正文读完可能还在探测 / 生成封面，downloaded 才表示 hash 和元数据已提交。终局从正文或转发下载表读取，不持久化字节通知；完成时 Received/Total 都为实际文件大小。Error 是短原因，不含 URL、令牌、本地路径。展示段的 FileKind 指已验证格式；Media 为图 / 视频凭据，Cover 为视频封面，Download 为下载动作凭据。下载完成后重读该消息或转发取得 Media，再调用 PrepareVideo / ReadMedia；文档、音频和未知文件本次只提供保存及下载状态，音频播放界面不在本任务。

| 状态值 | 界面含义 |
|---|---|
| not_downloaded | 没下；重启后仅有 pending 行也不冒充正在下载 |
| auto_downloading | 自动排队、下载或等重试中 |
| awaiting_manual | 超过自动限额，或属于第二档 / 头部不匹配；等待显式请求 |
| manual_downloading | 手动任务已接收，正在下载或生成元数据 |
| downloaded | 原件及元数据已提交；按 FileKind 和展示段 Media 选择原有视频 / 图片路径 |
| failed | 失败；Error 给短原因，可以显式重试 |
| canceled | 用户或宿主取消，可以重新请求，从零开始 |

同步错误：`ErrVideoDownloadBusy`（已有两路手动任务；同段复用不占第三路）、`ErrVideoDownloadUnavailable`（未装下载器 / 已关闭）、`ErrVideoDownloadSource`（没有可排的媒体源）、`desktopread.ErrMediaRef` / `ErrForwardRef` 或 store 的参数 / 范围错误，以及查询 / 上下文错误。异步缺址、HTTP、写盘、容器、刷新失败经 failed 报告；blocked / not_video / expired 等细分状态留在下载表。取消不存在的活跃任务幂等成功，仍先核对来源。

我定的：保留并扩展 VideoDownloadActions 的名称和方法，不引入并行的文件下载 API。手动不限文件大小或总传输时限，仍限制建连 5 秒、响应头 20 秒，正文可取消。自动沿用 20 秒、三次重试；手动一次请求只试一次，过期地址允许一次对应协议查询。正文补扫恢复本次 Offer 见过的最小 video/file seq 起的媒体，不受原图片 7 天窗口限制；旧正文不批量下。转发文件复用已有持久化队列和补扫。文件解码仅为验证格式及图片 / 视频元数据，不自动交给外部应用。

### 13.2 临时数据验收（qq-video）

独立 Go 程序实际调用 desktopread 的三项动作，注入编造的 DNS 结果，将通过 IP 检查的拨号引到假 HTTP 服务，仍走真实 HTTP、SQLite 和媒体写入；所有数据在本会话 scratchpad/qq-video 的临时数据根。未连接真实 QQ、SnowLuma 或运行中的 Sumika；未做界面。

- 真实字节边界：104,857,599、104,857,600 字节自动完成，104,857,601 字节自动转 awaiting_manual，手动完成的 Received/Total 都是 104,857,601，流式 sha256 对上。
- 内容去重：四条已完成视频引用只留 3 个媒体实体，合计 314,572,800 字节；重复内容新增文件 0。取消和退出后 `.tmp` 文件 0。
- 对照[性能标准](notes/标准-性能与效率-2026-11-03.md)的 100 ms 快反馈线：200 次已完成请求 P50 521 µs、P95 1008 µs、最大 1688 µs，没有新增 HTTP；手动接收请求 1.066 ms，取消清理 5.629 ms、退出清理 4.983 ms。Offer 三次为 0 µs，受计时分辨率限制，不解释成没有耗时。
- 下载路径资源量测：禁用工具搜索，不起 ffprobe/ffmpeg 子进程；样本是可认头的合成容器，量传输、hash 和落盘，不证明可播放。墙钟 2646.057 ms，进程 CPU 1796.875 ms，单核口径 67.908%；5 ms 采样峰值工作集 27,213,824 字节、私有提交 83,857,408 字节，结束 Go 堆 27,760,616 字节。范围包含同进程假 HTTP 服务，不含 UI、真实网络和模型，不据此宣称完整 Sumika 常驻资源。
- 两个自动边界样本的假服务有效吞吐为 105.255、177.607 MiB/s；超限后再手动的合并阶段 141.787 MiB/s，包含先丢弃再下载，不当纯下载速率。前一次保留工具搜索的 smoke 同样通过，合成容器探测失败按规格保留原件。
- 五个新增测试均抓到对应改坏：HTTP 大小比较改为 >=，两路门槛改为一路，拆主机白名单，拆桌面视频种类检查，默认退回 50 MiB。恢复后的专项退出 0；未下载、等待手动、手动进度、完成、取消和失败都有真实任务观察。旧 50 MiB 会阻止声明 60 MiB 的实时/补录视频，本次测试抓住。
- 最终 6 包 `go test -short -count=1 -p=1` 与同范围 `go vet -p=1` 均退出 0：api、mediafetch、desktopread、app、config、store。get_msg 新给的超限大小用例加入后，mediafetch 整包补测通过（30.491s）；原有图片和转发测试同包通过。真实 ffmpeg 生成并下载 1291 字节短片，探测得到 160×90、1200 ms，JPEG 封面 160×90，像素断言通过。

完整输出保留在本会话 scratchpad/qq-video：`mutants.txt`、`restored.txt`、`smoke.txt`、`final-smoke.txt`、`final-tests-utf8.txt`、`refreshed-size-tests-utf8.txt`、`final-vet.txt`。临时 smoke 源码、overlay、exe 和两个数据根已清理；无新 ADR、无提交、无推送。

### 13.3 文件扩展验收状态（qq-file）

文件后台在 feat/qq-file-dl 分块提交，2026-11-04 已合 main。2026-11-04 取得共享锁后，全部本地重活均经 `run-low.ps1` 低优先级串行运行；等待上限为 3 小时，每 30 秒尝试，不覆盖或删除其他任务的锁。

`go test -short -count=1 -p=1` 与 `go vet -p=1` 范围均为 `./internal/api ./internal/channel/types ./internal/channel/snowluma ./internal/mediastore ./internal/mediafetch ./internal/store ./internal/desktopread ./internal/app`，最终退出均为 0。测试 8/8 包通过，依次为 0.044、0.032、14.422、0.327、22.430、12.058、23.182、159.960 秒。首轮退出 1：原群解析未拒绝字符串 bad；另一夹具声明 1025 字节却返回空正文。已增加原群正整数校验并修正夹具正文，未放宽断言。

15/15 个隔离 Go overlay 改坏触发断言失败，每个测试退出 1，检查脚本整体退出 0：扩展名与头部匹配、第二档零自动请求、文件取址、转发结果提交、嵌套可达路径、私聊 hash、文件身份绑定、入库文件调度、流式限额、树事务登记、旧归档查询、段 hash 解析、手动媒体识别、原群保留、原群正整数校验。首次脚本因未使用变量编译失败，退出 1；修正绕过写法后才计入上述 15 项。overlay 不改仓库源码。

独立 Go 冒烟实际调用桌面动作、SQLite、假 HTTP 和媒体写入，退出 0：104,857,600 字节自动完成；声明 104,857,601 字节自动请求 0，手动完成；无长度流超限转手动后完成，哈希逐一核对。程序自动请求 0；伪装图片只读头后等手动。嵌套 GIF 原件 91 字节、Animated=true；ffmpeg 生成的视频 1736 字节，伪装为 txt 后手动下载，PrepareVideo 返回逐字节一致原件，元数据 160×90、1000 ms。未启动 Sumika 或连接真实协议端。

200 次已完成请求不新增 HTTP，P50=0 ms（计时分辨率限制）、P95=1.0242 ms、最大 1.3548 ms，低于性能标准的 100 ms 反馈线。Offer 五次均测得 0 µs，不解释为没有开销。取消等待清理 2.622 ms，退出 2.5669 ms。含下载、哈希和验证读取的有效吞吐：100 MiB 自动 265.681 MiB/s；声明超限手动 251.892 MiB/s；无长度先超限再手动合并阶段 185.973 MiB/s，不据此推定 QQ 带宽。

量测墙钟 1675.3316 ms，当前进程 CPU 1640.625 ms，单核口径 97.928374%；1 ms 采样峰值工作集 31,301,632 字节、私有提交 89,604,096 字节，结束 Go 堆 24,013,936 字节。范围含同进程假 HTTP，不含解码子进程、UI 或完整应用。媒体实体 6 个、209,717,358 字节；另有本次 PrepareVideo 播放副本 1 个，下载临时残留 0；重复内容未增加媒体实体，合成数据根退出时删除。

完整输出在本会话 scratchpad/qq-file：`test-round2.txt`（首轮失败）、`test-fixed-round2.txt`、`vet-round2.txt`、`mutants-round2.txt`（脚本编译失败）、`mutants-fixed-round2.txt`、`mutation-results.json`、`smoke-round2.txt`。文件扩展结果只采用本次输出，不引用 13.2 旧视频数字。

真机需核对（本任务禁止连接真实协议端，均未做）：①群文件 get_group_file_url 的 file_id、默认 busid 102 和实际直链；②私聊 get_private_file_url 的 file_hash，缺失时的真实返回；③get_forward_msg 的外层 / 嵌套文件 url、file_hash、节点 group_id，跨群转发及协议端重启后能否取到；④过期文件直链经文件接口刷新后的结果；⑤直链和跳转实际主机 / IP / 端口。新域名或 IP 不自动加入白名单，保留 blocked，由真机证据支持后再决定明确规则。




