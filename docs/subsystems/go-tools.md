# Go 命令行工具

开发规则见 [开发指导/开发流程测试便携](../开发指导/开发流程测试便携.md)、[开发指导/技术栈](../开发指导/技术栈.md)。

以下命令在 `native/` 运行，先替换尖括号占位。参数旁为默认值，“空”指空字符串。
共用内核身份和输入生命周期的分支状态见[运行时](runtime.md#go-内核身份与输入生命周期分支2026-11-04)。桌面宿主已提供 `api.LocalInputs`（SubmitLocal/StopLocalRun/WatchLocalRun），现有诊断入口的 Send/Respond 同样经持久受理；没有新增 CLI 子命令。临时真实宿主＋假模型验收及跨块整合清单见内核补强 5.3（私有存档）。


独立审阅没有新 CLI，也不让模型派审阅工具。可信本地宿主调用 `App.RunWork`，按 `ConversationKey` 核对本地私聊所有权后传入显式目标与验收，复用 `Hub.RespondWork`；`agent.advisor`、`agent.verify` 默认关闭。配置开启后接同一 Lifetime、Results/Work 和预算化 ModelCaller，模型用途路由与角色模型分开；普通 SubmitLocal/SendLocal 不触发核验。契约及实际隔离验收见内核补强 §9（私有存档）。

## 日常用

### sumika-go

**receive 会读写数据、调模型并产生费用。** 无界面诊断入口默认 receive；`-mode archive` 只读已有 Go 历史。

```sh
go run ./cmd/sumika-go -root .. -account "<账号>" -data "<数据目录>"
```

参数：`-root` 空，不给就从当前目录往上找有 `sumika.example.yml` 的目录当资源根（1f41eeda；桌面独立按 ADR-248 找根，见[运行时](runtime.md#go-桌面根目录与数据位置adr-248)）；`-mode receive`，可选 archive；`-account` 空，receive 必填，须在 `qq.accounts` 中；`-data` 空，省略时照配置选择数据目录；`-first-takeover`（配 `-mode receive`、`-account`、显式 `-data` 和 `-confirm-bun-watchdog-stopped`）首次接管 QQ 发送权，核对并写交接后退出，不启动后台、不发送；`-mode media-check`（只要显式 `-data`）停后台后只读核对已导入的媒体，只往终端打每个平台的计数（不打路径和 hash），不读配置、不建日志和锁；有内容不符或读取出错时退出码非 0，缺文件只计数。
receive 读配置、角色卡、记忆和密钥，写 Go 聊天库、运行记录及日志。archive 不建 Hub、不取令牌、不连网关，不创建或迁移业务库；缺库和配置读取失败均报错。两种模式持有同一 `go/backend.lock`，关库后放锁；`Close` 支持 Open 成功而尚未 Run 的收尾。命令日志写 `logs/go-<日期>.log` 并打印到终端（`internal/filelog`；桌面程序同一个包写 `logs/desk-<日期>.log`、不打终端），Ctrl+C 收尾退出。`app.Options` 的零模式为 archive，CLI 显式传 receive 维持诊断入口行为。
桌面宿主的日志和 `Reason` / `NoRecv` 状态在写文件、发布界面快照前共用 [`app.RedactLog`](../../native/internal/app/log.go)：遮盖已知 QQ 令牌（配置原文、环境变量展开值、密钥文件）、会话号、长数字和 Windows 用户目录前缀（`C:\Users\<用户名>` 换成 `%USERPROFILE%`）；令牌快照不可读取时隐藏自由文本详情，`filelog` 不读配置或密钥。
启动先拿 `backend.lock`后，仅 TCP 连接探测 `127.0.0.1:7720` 和配置的旧 `console.port`（重复端点只查一次），每端点 500 ms。只有明确拒绝连接放行；连接成功报 `legacy_port_occupied`，超时或其它错误报 `legacy_port_unknown`。两者都在开业务库、读令牌、连 QQ/模型之前退出并放锁；不发 HTTP、不枚举进程、不轮询。
receive 的主动提醒都进 `native/internal/alert` 的登记处（按键去重，Raise 发生、Clear 恢复），桌面的状态从这里读：`qq:<号>:conn`（连接断满 30 秒，适配器）、`qq:<号>:send`（发送暂停、有消息发不出去，`app/send.go`）、`qq:<号>:lock`（开了 `qq.goSend` 却没接到发送权，`app/channel.go`）、`guard:embed`（发送前检查的嵌入还不可用，`app/guarddeps.go`）、`guard:literal-pending`（发送前检查只比对原文，`app/channel.go`）、`guard:<角色>:route`（这个角色的守卫这一次走了退路：嵌入没用上就只比原文、照常发，确认模型没用上就按相似度拦；路线变了才 Raise、恢复时 Clear，`app/guardroute.go`，ADR-186 第 4 条）。`:conn` 和 `:send` 的发生、恢复另写进 QQ 库的 `qq_alerts`（试运行统计要排除掉线时段），其余只记日志。

来源：[main.go](../../native/cmd/sumika-go/main.go)；规格：Go 后台装配（私有存档）。

生产模型请求经 [`modelcall.Client`](../../native/internal/modelcall/client.go)统一预留、登记派发、结算和限制 transport 容量；主回复、压缩、回合内守卫、后台整理各带可信用途 Invocation。根预算在持久受理时按真实 accepted_at 建立，排队或重试不延长期限；已有 Root 不再另开余额。渠道 settle 把原回合身份传进守卫，不能按 cache key 或最近聊天猜根。缺 usage 保留未知预留，实报超额记真实账并禁止后续派发；Reasoning 已含在 Output，不重复计量。配置入口是 `config/kernel_budget.go`。

用量审计将未知思考 token 留为 NULL，供应商明确报告的 0 保留为 0；取消或缺 usage 不伪造已知零。直接调用守卫的宿主也必须先建立并携带所属根，不能借最近会话推定。

生命周期在预留之后拒绝模型准入时，客户端保留原上下文退回未派发预留，再返回准入错误；失败返回的空上下文不参与收尾。Seal 后实际派发为零，预留与 transport 槽全部释放，不能因关闭竞争触发 panic。

晚到插话重排为独立运行时，新根沿该条 inbox 原始 `accepted_at` 计时。本地私聊、本地群聊直接排队，QQ 私聊、QQ 群待上一轮 Commit 后认领，四种入口均不能通过重排延长截止时刻。

手动聊天压缩在排队前建立独立 compact 根预算；硬压缩和提前压缩沿所属回合根计账。没有合法工作投影的聊天只压聊天材料，不查询不存在的工作头；此类 Live 按精确 Scope 复用并更新当前输入身份，工作投影仍要求同一 Root。

提前压缩的后台工作使用独立 RunID，仍沿原 Root、Scope 和预算截止计账；前台 WaitRun 不等待这个后台 Run，宿主 Drain 继续等它真正退出。`RespondContext` 将调用方 context 一并传给持久队列，取消覆盖排队及模型请求；显式桌面持久提交仍按自身入口契约运行。

插件参考信息、压缩前钩子及 Seen/Settled 通知在准入时登记活动，取消或等待超时后仍由真实回调退出结束活动。`TestPluginActivitiesDrainRealExit` 用无网络 Hub、临时 trace 与 `testing/synctest` 保留 10ms 钩子、5ms 未退出排空及 1 秒最终排空门槛，验证超时后的迟到写入仍在关库之前完成；同包通知、压缩、插话和审批测试以事件同步观察实际完成。

桌面只读契约已在 `native/internal/api` 定义：`ConversationKey`、`EventRef`、`MessageView`、`List`、`Result` 和 `Reader`；订阅先登记再读初始快照，`Close` 返回时停止查询。包只依赖标准库，不暴露 SQL、app 或 UI 句柄；查询与订阅实现属于[界面接 Go 第 2 步](../规格-界面接Go后台.md#72-步骤表)。

桌面可选写接口 [`api.LocalChat`](../../native/internal/api/localchat.go) 由 [`app.DesktopReader`](../../native/internal/app/desktop_chat.go) 实现：`SendLocal` 等真实内核 Outcome，主人输入和回复只进本地库；`StopLocal` 同时取消尚未开始的发送及活动回合；`WatchLocalApprovals` 先登记唤醒再取真实审批快照；`DecideLocal` 按角色、会话、请求 ID 回答允许一次或拒绝，迟到、跨会话回答报失效。只给本地私聊日常及已有所属话题，QQ 和本地群聊拒绝；archive 返回 `ErrLocalChatUnsupported`。正文流使用该发送请求的完整步骤草稿（回合号、步骤、正文），不走存储 dirty 通知。桌面关闭先封写入口、取消并等发送与审批订阅，再关查询和库。正式界面接线见 [`native/localchat.go`](../../native/localchat.go) 和[规格 4.4.1](../规格-界面接Go后台.md#441-本地私聊写入口2026-11-03)。

内核工作材料由 [`workcontext`](../../native/internal/workcontext/state.go)和 [`turn.WorkPort`](../../native/internal/turn/ports.go)管理。完整可信工作 Scope 才恢复材料并提供 result_read；普通聊天缺少来源授权时继续聊天，不推导或补授工作权限。统一 finalizeTool 保存原文、步骤和工作记录，调度器不再保留第二个结果写入入口。受控结果只在实际进入实例后 Accept；停止、来源失效、写库失败和超时迟到均 Discard，恰一次结算。工作快照按 Scope/Root 隔离，不进入跨档摘要、公开 Events 或记忆输入；思考、受控正文和回调不恢复。同一个会话换 Root 时实例照旧复用（`turn.Hub.switchWorkRoot` 只换属于 Root 的状态，消息原样，缓存前缀不断）；`result_read` 按 Scope 校验，复用后仍读不到别的场合、角色、发言人的结果；回到已有工作记录的旧 Root 才整体重建。旧全文缺失标 legacy_unavailable，不重放工具。

`agent.work` 的配置和硬限见内核补强 §6.1（私有存档），读取边界由 `config.Work` 统一校验；部分配置保留其余默认值，显式零、小数、错误类型和 null 拒绝。事件恢复按 `history.WalkEvents` 固定高水位 keyset 分页，超过 2000 条不再跳过中段。


展示查询的底层入口为 `store.ReadDisplay(ctx, DisplayScope, cursor, limit)`：limit 必须为 1～1000；按时间和 seq 倒序取 limit+1，再正序返回。同一读事务取得平台 seq 水位。QQ 仅返回授权角色 observations 的并集，本地日常按角色、话题按 local_topics 所有权校验；引用摘要和撤回标记分别批量读取，不返回未观察或跨会话目标。`Set.ReadDisplayConversations` 包含无消息的角色日常入口和已拥有话题，目录支持继续分页。发送状态取已提交的计划表，QQ 名字批量取本机名片和昵称，不刷新远程资料。

`Store.SubscribeChanges` 在成功提交后登记会话 dirty，容量 1 的唤醒不带正文；`Take` 在读库前交换集合，读取期间的新提交留给下一批。覆盖通用事件、重复入站补观察、出站计划与终态修正、回声、启动清扫和 QQ 提醒。失败事务不登记，不在通知锁里查库；`Close` 可重复调用。

`internal/desktopread.Service` 实现 `WatchOverview`、`ReadMessages`、`WatchConversation`、`WatchRuntime`，依赖 api 契约、store 和提醒登记处，不 import app 或窗口库。构造时注入角色显示 DTO、平台库组及 `Runtime` 宿主状态源；本步没有界面消费者。宿主以 `Runtime.Publish` 提交已经确认的阶段与连接事实，不从 WS 连通推断 QQ 在线，账号统一脱敏。overview 和 runtime 订阅各自登记宿主 dirty，并使用 `alert.Hub.SubscribeWithOpen` 接收已有提醒和恢复；未接状态源时明确 unsupported。单 worker 先发布初始快照，再按 dirty 重查，预算 5 秒；取消等待查询和提醒订阅退出。新增按 seq 分批 200 条追到固定上界；补观察、撤回和发送状态另重查已加载时间区间，关闭最后一个同范围订阅时释放跟踪身份。`Snapshot.Version` 只表示订阅内投递顺序，`MessageView.Version` 是同一服务串行读的行版本，同一快照的 Page 与 Changed 使用相同的新行状态；分页消费者按事件身份合并并拒绝较旧版本，会话切换取消旧请求。窗口锚点和驻留缓存属于第 4 步。第 3 步（2026-10-31）加了 `ReadMedia`、`RIndex`、`RPNG`：`WithData` 接数据根；媒体凭据要求事件在该会话可见、它或它同会话的可见引用带这个 hash，再按凭据的平台调 `mediastore.OpenMedia`，读原文件（上限 20 MiB，多读 1 字节认超限），tier 只校验、缩放在界面；小黄脸只读 `<data>/cache/qq/faces/index.json` 和 `<编号>.png`，每一级拒绝链接。段投影解析完整 `types.Seg`，导入旧段经 `bunseg.LegacySegment` 对位后补尺寸、动图、表情和缺失 hash，对不上的在 `DisplaySegment.Issue` 说明、不给媒体凭据。`desktopread.Reader` 把展示与设置合成 `api.Reader`。第 4 步（2026-10-31）：`desktopread.NewResults` 允许单个角色资料读失败（ID 仍参与可见范围，概览里报失败和原因）；`(*Service).Close` 拒绝新调用、取消在途查询和订阅并等它们退出（`ErrClosed`），宿主关库前先调它。`internal/app` 的 `(*App).Desktop` 每个 App 只建一份：角色显示资料用 `persona.Display` 只读解析卡（不写卡、不进 history），设置每次显式重读配置；`Desktop.Publish` 由桌面宿主在后台线程提交阶段，App 关了以后不再发布；`Desktop.Close` 等界面消费者退出。Open / Run / Close 原有逻辑没动。

QQ 未读复用相同观察过滤器，使用已有 `qq_read.read_at`；缺行时在服务实例里保留首次最新可见时间，重建订阅不清零。只数严格晚于基线、`kind=message && actor_kind!=character` 的可见事件，最多 100（界面显示 99+）；本地 `HasUnread=false`。读路径不建已读行、不写业务表。角色卡枚举及真实宿主、媒体的消费者接线仍由后续装配提供，本步不启动后台。

### visible-events

```sh
go run ./cmd/visible-events -data "<已有 Go 数据目录>" -conversation "<完整会话身份>" -characters "<授权角色 ID，逗号分隔>"
```

可选 `-character` 进一步按一个已授权角色筛选。持有数据锁，只读打开已有库，不迁移、不建业务库、不输出正文；每页 200 条，逐页输出 `platform/eventID/seq/created_at/recalled`，每列以制表符分隔。角色清单由本机操作方明确给出，不从显示名推断。该入口供主会话在另行安排的真实库冒烟中核对可见身份；本步只用临时库运行。[源码](../../native/cmd/visible-events/main.go)。

链接的离线核心在 [internal/linkreader](../../native/internal/linkreader/doc.go)：`Recognize` / `Clean` 规范化 X、B 站、GitHub、YouTube、抖音、网易云、pixiv、BOOTH、Bluesky、Misskey（misskey.io）、Mastodon（mastodon.social）、Steam、爱发电、Spotify、TikTok、小黑盒、小红书及已支持短链（小黑盒帖子和小红书只识别、回取不到，见规格-链接解析 4.15、4.16）；`Extract` 按干净链接去重，`FromCard` 从 QQ 跳转字段走同一识别。追踪删除入口为 tracking.go，第三轮平台项在 tracking_more.go，未知参数原编码/重复/空值保留。保留表（时间点 t、start、time_continue、start_progress，分 P 和播放列表 p、index、list、lid）优先于任何追踪表，规范化改写不得丢它们；全平台表见规格-链接解析 6.1。网易云 #/ 路由和旧 pixiv 定位键转换后仍须幂等。卡片上限 1 MiB、64 层，拒绝凭据；短链不在纯函数层展开。完整载荷来自 `Seg.CardJSON`，不把 `Raw` 的 200 字预览当 JSON。参考及字段边界见[规格第四节](../规格-链接解析.md#四联网取内容及字段契约)。

匿名取数的统一数据契约为 `linkreader.Result`：`Field[T]` 分开保存状态、值、原因与截断标记；作者数字 ID 保持字符串，统计零值与未返回分开。`Block` 保留文字、媒体和引用顺序，`Media` 区分声明字节数与尺寸；JSON 可直接保存与回读；缓存见下文 `Service`。

X 的 `Author.Avatar` 取 avatar_url；`Links` 保留 t.co original、展开后的 URL 与 display，`LinkCard` 保存标题、描述、站点和图；媒体 facet 不重复当外链。`Media.Variants` 保存全部视频格式、码率、地址和可得尺寸，主 URL 选最高码率 MP4，未声明大小保留 not_returned。`Expansion` 是 ADR-253 的 short/long（300 个 Unicode 码点、2 图、1 视频为边界；GIF 算视频；引用和文章一律 long），内容不全又不能确定 long 时保留缺项。引用只留一层，被引结果的 `MoreQuotes=true` 表示还有更深引用。

Article 的 `Text` 仍是主帖引导文字，`Title` 为文章标题，`Article` 为正文文字；`Blocks` 依序保留引导、article_title、article_cover、正文文字/图片/quote 卡片，与 B 站图文共用块结构。乱序 entityMap 按 key 关联，正文 MEDIA 按 mediaId 查表；SourceType 保留标题/列表等类型，块 Links 保留外链，Markdown 代码/表格与分隔线各占原位置。内联 tweetId 补取同一个匿名 FX v2，共享原有时限和预算，重复 ID 本次复用；内联里再引用只标 MoreQuotes，不继续追。字段或正文关联缺失有原因，Article.truncated 标未完整保留，预览不冒充全文。

接进渠道和内核的是 [internal/linkplugin](../../native/internal/linkplugin/plugin.go)：宿主在入库后、角色档位过滤前只交一次 `OnStored`，上下文与自动展开独立准入、共享 Service 和消息关联。上下文只处理角色可见消息；回合开头附本会话未给过的摘要，每条40字，`read_link_detail` 仍按会话、角色核权。自动展开使用冻结设置编排，经当前名单、源消息、发送硬前置及文字守卫重新校验，走注册的 `automation:link-reader` 出口。两名后台 worker 等出口终态及媒体释放才继续；撤回、撤权、关闭取消未发部分。默认展开关闭且白名单为空。分支已跑15包测试/vet、9项改坏和隔离量测，结果见[发送端规格](../规格-链接自动展开.md#十实施分步和可撤回提交)；不能据此认定真实QQ已上线。

本地主人消息由 `ObserveLocal` 接入，保留本地会话键、逐角色观察记录和原文数量预算；本地私聊、本地群聊均不创建展开任务。QQ 使用一次 `OnStored`，不再重复注册旧观察入口。

匿名联网仍在同一包，依赖方向是 `Fetcher → Recognize / Result`，不依赖渠道或 app。`NewFetcher(FetchOptions) (*Fetcher, error)` 支持传输注入、代理、超时、体积与 User-Agent；`Fetch(ctx, Link)` 分派已支持平台（TikTok 的视频、图文、用户页见规格第四节 4.14）；`Resolve(ctx, Link)` 逐跳处理 b23/bili2233/t.co/v.douyin/jx.douyin/163cn，终点只接受已实现内容；youtu.be 直接取路径 ID。`Requests()` 包含 DoH 和失败发起。默认 4 MiB、单请求 10 秒、整体 30 秒、User-Agent=Mozilla/5.0。B 站、抖音、网易云直连；X、GitHub、YouTube、pixiv 走既有 proxy（含环境代理），公共 DoH 经 TLS 校验，CONNECT 绑定已校验 IP。精确接口/图片/短链主机才有 fake-ip 例外，mapped 地址先 Unmap，混合 DNS 逐项检查，完整名单及形状表见[规格第五节](../规格-链接解析.md#五短链与网络安全)。元数据取数只检查媒体候选和头像的所有 A/AAAA；展开消费者通过同一安全客户端的 `Fetcher.Stream` 下载，所有轨道共用字节预算。FX v2 有限回退到 VX；B 站只有视频 view 风控走 WBI，opus/detail 不签名、不补 Cookie。`NewService` 复用 24 小时缓存、同键在途合并、跨重启、消息关联与详情快照；新平台不另造缓存或发送路径。媒体准备与释放见[发送端规格](../规格-链接自动展开.md)，字段映射和错误见[链接解析规格第四节](../规格-链接解析.md#四联网取内容及字段契约)。

B 站的 Media 同时保留封面 image 与所选 P 的 video 元数据：封面尺寸未返回，不借视频尺寸填充；video 保存该 P 的宽高、时长，播放 URL 标 unsupported/playback_not_requested。本入口不请求播放接口，不把封面当视频直链。

B 站风控（2026-11-13，[规格 4.2.2](../规格-链接解析.md)）：HTTP 412、JSON -352/-412 让整个 bilibili 平台冷却（15 分钟起、连撞翻倍到 2 小时，`rate.go` services 表的 `riskBase/riskMax`），-509/-799 冷却 5 分钟，冷却中的请求直接报 `rate_limited`；B 站请求统一桌面 Chrome UA 和 `Accept-Language`、api.* 带 `Origin`，并带 `finger/spi` 取的匿名访客标识（缓存 6 小时）；不做 WBI 回退、指纹伪造和换 IP。会员购普通商品为 `bilibili/mall`：[mall.go](../../native/internal/linkreader/mall.go) 先校验 detail 路由与唯一的正整数 itemsId（最多128位），兼容旧 index 的明确 page 路由和 `&amp;`。与视频相同地接收文字、小程序 jumpUrl/qqdocurl、安全 b23 终点；不接市集、魔力赏、票务。只向固定 merchant/info 发匿名 GET。`Result.Mall` 保存元字符串价格区间、独立定金、保守状态、店铺、UTC预售/开售时间和预计到货补款月份；Blocks 为文字后首图，图片补 https: 后沿既有媒体校验。0时间不显示为1970，未知销售枚举显示状态未知，接口原文和店主号不保存。规范链接仅有 itemsId/page，B站清洗表新增 share_mid/channel/from/msource，短链请求、派生缓存和卡片均使用清洗结果。展开复用 B站 card/cover-text 和既有内容政策；没有新增购买或发送出口。输入形状表与验收记录见[规格4.2.1](../规格-链接解析.md#421-b-站会员购普通商品link-mall)。

发送端的 [internal/linkexpand](../../native/internal/linkexpand/templated.go) 把 Result 编成有序消息计划：每个站点的排版由 [internal/linktemplate](../../native/internal/linktemplate/template.go) 的通配符模板决定（`$字段`、`[可选段]`、`$分段`、`$合并转发`；站点表和内置默认在 `sites.go`，设置在 `linkReader.expand.templates` / `sites`），转发上限、节点拆分仍由代码保证；2026-11-08 起不再按敏感程度拦截或替换，展开与否只看会话名单。`Preparer` 先对累计语义文字调用宿主守卫，再取媒体与卡片；媒体槽位失败保留正文和原因。B站视频按开关及能力请求播放信息，MP4保持独立消息段；合并转发前不再另发引用导语，转发段不带回复段（[规格-链接发送改版](../规格-链接发送改版.md) B4、A5，ADR-360）。媒体能力和明确失败的组合记录见 [qq.md](qq.md)（ADR-310）。

展开端消费 main 的 `Ugoira`、`Novel` 与 `Media.Fallback`，不保留旧 `PixivInfo/Regular` 两套契约；regular 候选仍受展开图片 20 MB 上限。生产 Fetcher 实现 `Metadata/Complete`：pixiv 元信息阶段不请求 pages/ugoira_meta、不映射小说正文，获许可后才完成内容；元信息不冒充成功缓存。B 站动态统计读 `like/comment/forward`，抖音描述读 `Text`。GitHub issue/PR/release/gist、网易云 album/playlist 格式未定，仍能取数和读详情，不自动展开。

[internal/linkcard](../../native/internal/linkcard/card.go) 使用 MewTypeUI 浅色主题与 mygo `ui.Render`，单独 worker 离屏生成720物理像素宽、最高960像素的内存 PNG，超过2MB试JPEG90，仍超限返回错误。输入是已检查文字和已解码封面，绘制没有I/O；正常路径不写卡片文件。媒体客户端复用 Fetcher 的安全传输、平台限速及逐跳主机校验，原始签名地址仅用于内存下载；默认视频60秒/100MB，图片20MB，ugoira限制300帧并按累计时间生成GIF。`MediaManager` 临时产物仅在自身实例目录，交出口持有至实际调用退出后释放；独占后台启动只清理超过1小时的其他旧实例。


`VideoPart.Title` 与 `VideoPart.Duration` 同样使用 `Field`；分 P 字段缺失保留原因。X 的整个 media 缺失/null、all:null 标 not_returned；本轮实测的 media:{} 和显式 all:[] 都表示无附件，记 available 空列表。

视频的 `Author.Avatar` 取 owner.face；上传日期在 `Published`、七项统计在 `Stats`、封面在 image.URL、所选 P 时长在 `SelectedPart.Duration` 和 video.Duration，分 P 数即 `len(Parts.Value)`。动态图文的 `DynamicType` 保存 item.type（0 普通、1 专栏型），`PublishedText` 保留 pub_time 原文，标题可缺。`Blocks` 保留原段序及 paragraph_type：1 word/rich 文字、2 多图和宽高、5 列表条目文字；未知类型保留 unsupported 块与编号，Text.truncated=true。`/opus/`、`/dynamic/`、t.bilibili.com 以及 www/m/mobile 变体共用 opus/detail；不抓旧接口摘要冒充全文。

第三轮字段：GitHubInfo 保存状态/语言/更新时间，GitHub REST 的匿名额度剩余为 0 时按 reset 冷却；仓库头图为官方 Open Graph 候选，REST 未给自定义头图。MusicInfo 保存逐个歌手及专辑；歌曲时长放 audio.Duration，URL 标 playback_not_requested。YouTube watch 初始 JSON 给标题、频道、日期、时长、播放和缩略图，缺点赞为 not_returned；简介和 GitHub 正文预览最多 800 rune。pixiv 保留标签、收藏/浏览、图数、x_restrict/possibly_sensitive，多页图原顺序及固定 Media.Referer；缺/未知限制不判普通，页图失败标未取全。抖音 detail/slidesinfo 的拒绝或空作品为 not_returned，不写空成功缓存。各接口状态与真实样本限制见规格 4.3～4.7。

pixiv 补全按 ADR-254：作品 ai_type/ai_generated、age_rating（all_ages/R18/R18-G）；Media.URL 为每页 original，Fallback 保存 regular、100 MiB 退档条件和注明文本，实际超限判断在下载轮。Ugoira 保存 originalSrc ZIP、逐帧文件名/毫秒延迟和 Referer，局部失败保留 not_returned；不下载/转码。小说 novel/show.php?id= 读 Ajax novel：Novel 保存简介与原始字数，Text 保留最多 800 rune 的正文开头，Media 按全文插图标记排原图；未知标记不判普通作品，匿名拒绝保留 not_returned 和 reason。完整契约与形状表见规格 4.7。

### link-probe

```sh
go run ./cmd/link-probe -proxy "<HTTP代理>" "<公开链接>"
```

只读、匿名；每次 HTTP 发起至少间隔 2.1 秒，输出统一结果或固定种类错误、包含 DoH 的请求发起数和 `elapsed_ms`（从 Fetch 开始到结束，包含节流和地址检查）。不读配置、角色卡或聊天库，不接 QQ、不下载媒体；`-proxy` 用于 X、GitHub、YouTube、pixiv，省略时读 HTTPS_PROXY/HTTP_PROXY，B 站、抖音、网易云始终直连。诊断隐藏原帖、媒体、全部视频候选、regular 候选、ugoira ZIP、作者头像、外链预览和段落链接 URL 的查询值；候选原始签名只留在进程内结果中，字段状态与所选 P 的 cid 保留。成功退出 0，取数失败退出 1，参数不合法退出 2；标签、内联帖或媒体候选局部失败保留主体并写字段原因。

第三轮已测五平台 13 条公开链接；pixiv 补全轮指定 11 条匿名实测均退出 0，共 51 次请求。四件 R18 图稿的页图、动图 ZIP/帧为 not_returned/not_found，独立请求核对为 HTTP 404；小说字数、800 rune 预览及 8 张插图原图已核对，R18 诊断证据仅留元数据。该轮三包回归、11 项改坏、vet、构建及量测见规格 4.7。成功与限速/受限分别记录，不以夹具通过宣称平台匿名接口始终可用。


### gate-report

**只读真实数据。** 统计试运行的闸门放行、开口比例和 QQ 掉线。

```sh
go run ./cmd/gate-report -data "<数据目录>" -days 7
```

参数：`-data` 空，必填；`-days 7`，含今天；`-since` 空，格式 `年-月-日T时:分`，与 days 取更晚的起点；`-account` 空，默认统计所有账号的掉线；`-json false`。
只读 `go/trace.db`、`go/chat/qq.db` 和 `logs/desk-日期.log`、`logs/go-日期.log`，不迁移、不调模型；文本或 JSON 打到终端，不写报告文件。将 `qq_alerts` 的 down/up 与日志的断开、重连、被踢、账号不符、连接事件按时间合并，再按账号配对；重复 down 保留起点，最后未恢复的算到截止。这样跨重启缺 up、闪断期间没有再次 Raised 的 down 都不会丢。日志账号掩码须在全部提醒账号中唯一，缺失或碰撞时只用提醒表。desk / go 各自只在连续日志日期间继承账号与连接覆盖；中间缺一天就清空，直到再读到账号行才认后续连接。缺日志仍按提醒表统计；读取失败照实报错。这里统计协议连接，不从收图、闸门判断推断在线，也不替代独立的登录和接收健康提醒。

日志只到秒、提醒表精确到毫秒。每个账号从首条可信连接事实开始，连续日志日期内按日志重建状态；覆盖在桌面重启、新账号行或关机诊断处结束，每天最多到次日零点（截断到报告截止）。覆盖区间内忽略提醒，避免跨秒迟到的 down/up 覆盖较新的连接事实；区间外提醒仍保留毫秒精度。

来源：[main.go](../../native/cmd/gate-report/main.go)、[connections.go](../../native/cmd/gate-report/connections.go)；规格：Go 后台装配（私有存档）。

### snowluma-record

**录真实帧、写文件，须单独授权。** 排查适配问题时，连接 OneBot 正向 WS 录成 JSONL。

```sh
go run ./cmd/snowluma-record -url "<WS地址>" -token-env SUMIKA_RECORD_TOKEN -out "<输出目录>/frames.jsonl" -for 30m
```

参数：`-url` 空，必填；`-token`、`-token-env` 空，前者为空才从指定环境变量取令牌；`-for 30m`，不大于零则等 Ctrl+C；`-out` 空；`-act` 空，给一个动作 JSON 文件（action、params）时发这一个动作，不给只收不发；`-account` 空，`-act` 时必填，协议端登录的号对不上就不发；`-send` 关，`-act` 时不给只核验、打印要发什么，给了才真发；`-wait 15s`，发出去以后再录多久。
只收业务帧、不调模型，文件存在则追加。默认写当前目录的 `data/dev/snowluma-frames/<时间>.jsonl`；在 `native/` 跑即 `native/data/…`，建议给 out。
来源：[main.go](../../native/cmd/snowluma-record/main.go)；规格：[渠道中间件第十九节 A10](../规格-渠道中间件.md)。

### sumika-ctl：Agent 怎么操作开着的程序

**对象是正在运行的桌面程序（`sumika.exe`），不是后台诊断入口。** 主人和 Agent（Claude Code 里的铃音）在命令行里对开着的程序做托盘能做的事，并拿到结果。桌面程序没开着时它不会把程序拉起来，只报"Sumika 没在运行"。

```sh
go build -o sumika-ctl.exe ./cmd/sumika-ctl
sumika-ctl status
```

| 命令 | 做什么 | 对应托盘 |
|---|---|---|
| `status` | 阶段、说明、接收用的小号（打码）、QQ 连接、没恢复的提醒、两个开关、数据目录、版本、进程号、守护进程那一栏（在不在、这一轮重新拉起几次、最近一次异常退出，[规格-看门狗](../规格-看门狗.md)） | 后台状态页 |
| `receive start [--wait[=60s]]` | 开始接收 QQ。不带 `--wait`：最多等 0.2 秒，很快就有结果的（已在接收、没有小号、数据锁被占）当场回，慢的回"已开始"和当时的阶段；带 `--wait`：等阶段落定（接收中 / 回到只看历史 / 受阻），最多等给的时间（默认 60 秒，上限 5 分钟），超时退出码 1 并带当时的阶段 | 开始接收 QQ |
| `receive stop` | 停止接收，回到只看历史；本来就没在接收也算成功 | 停止接收 QQ |
| `window show` | 把主窗口叫出来 | 打开主窗口 |
| `logs [-n 50]` | 桌面日志（`logs/desk-<日期>.log`）的最后几行，1～500，当天不够接着读前一天；日志写入时已打码，原样给 | — |
| `gate list` | 各群的档位：`mode`（off 只记录 / dry 试运行 / speak 说话）、`source`（own 这个群自己的 / all 所有群的 / default 没设过）、`name`（群名表里的名字，最多 20 个字）、`actual`（接收中才有：真正在用的档位，发送条件不齐时 speak 会被压成 dry）；`conversation` 是打码的（`qq:group:***01`），`handle` 是这个群的句柄（8 个小写十六进制字符，同一台机器上稳定），`handleClash` 为真是和别的群句柄撞了 | — |
| `gate set <qq:group:群号\|句柄> off\|dry` | 把一个群调成只记录或试运行，写运行记录库并记一行日志（`ctl`，群号打码）。群可以写完整群号（用户自己知道号），也可以写 `gate list` 给的句柄；句柄撞了回 `invalid`，要写完整群号 | — |
| `backfill status` | 同一 AlignmentView 的本轮任务、最近对齐、消息 / 媒体分项和 open / abandoned / excluded 原始缺口数；只读，不启动接收 | 待 MewTypeUI 接入 |
| `backfill gaps [--cursor <游标>] [-n 100]` | 当前 QQ 账号登记缺口，1～200 条；号码打码，以带原区间的八位 HMAC 句柄指认，游标不含原始会话号 | — |
| `backfill retry <缺口句柄>` | 重新解析当前登记行并复用协调器补单段；不接受裸 ID、号码、URL、路径或 force | 待缺口行接入 |
| `backfill run` | 仅在已接收且 Ready 时排入同一账号对齐任务；当场回 accepted / JobID，不等媒体、不宣称填平 | 待 MewTypeUI 接入 |
| `checkpoint list <本地会话>` | 列出本地主窗口私聊或所属话题的检查点，最多 100 条，含 child 归属标记 | 专用面板待做 |
| `checkpoint preview <会话> <ID> [step\|turn\|recovery\|resume]` | 精确 Scope 内的变化摘要和内容版本，不接受文件路径 | — |
| `checkpoint restore <会话> <ID> <version> [模式]` | 同会话主窗口 once/deny 审批后复核版本再恢复；返回逆 ID、已完成/未完成路径 | 复用现有审批卡 |
| `checkpoint status <会话> <目标或逆ID>` | 独立查询准备/恢复/收尾阶段及已恢复 N / 共 M；终态含两类路径 | 桌面可订阅同源进度 |
| `checkpoint cancel <会话> <目标或逆ID>` | 取消指定恢复，等全部真实 I/O 退出后返回 partial/ready 和逆 ID | 不新增界面设计 |
| `chat [--character <id>] [--new] [--timeout 120s] "<文字>"` | 自检对话：以主人身份往角色的自检话题（本地私聊的一个话题 `selftest-<角色>`，标题"自检"）发一句话，等这一轮结束；返回她的最后一句、这一轮调用的工具（取自运行记录）、模型、用时；停在审批上回 `waiting_approval` 和卡片，不替用户批；`--new` 开新话题清空上下文。话题里的事件都带 `selftest` 标记：记忆不收、没有 `remember`、不取链接，不往 QQ 外发；参数里没有会话 id。细节见规格第八节 | — |
| `goal [show\|set <目标>\|edit <目标>\|pause\|resume\|clear] [--character <id>]` | 目标模式，和输入框里的 `/goal` 同一套逻辑（服务端拼成斜杠命令全文，后台走同一张命令表）；会话固定是该角色的自检话题，设目标会把目标原文发给她并自动续轮；不写子命令是 `show`；目标原文以 `-` 开头先写 `--`；`data` 带回复、`started`、目标现状。细节见规格第十节 | — |
| `loop [list\|add [间隔] <任务>\|edit <编号> [间隔] [新任务文字]\|pause <编号>\|resume <编号>\|remove <编号>] [--character <id>]` | 定期任务，同上；间隔写 `30m`、`2h`、`1d`，`add` 不写间隔由她自己定节奏；`add`、`edit` 还认 `--every 30m`、`--daily 09:00`、`--weekly 1,3,5 --at 09:00`、`--self`、`--new-chat`、`--same-chat`、`--notify content|errors|none`（和设置里“定期任务”页同一批选项）；`edit`、`pause`、`resume`、`remove` 必须写编号（不像输入框省略就是全部）；`data.loops` 是这个会话的全部任务 | — |
| `stats usage [--days N\|--year Y\|--all] [--by task\|model\|day\|week] [--full]` | 运行页的用量（只读）：按任务类别（前台回合、记忆整理、守卫、识图、上下文压缩、子代理、顾问和核验、/goal、/loop、未知）、模型、本地时区的天或周汇总次数、总输入（含缓存读写，旁边是缓存命中率）、输出（已含思考）、平均耗时（只算成功的）、失败数和没拿到用量的次数；默认最近 7 天（含今天）、按类别，`--days / --year / --all` 互斥，`--full` 在类别下列到模型；Go 侧还没接的类别（识图、子代理、/goal、/loop）没有记录时写"未接入"，不写 0。`--json` 里类别用 `foreground` 等 ASCII 键，`source` 说这次读的是汇总表还是原始记录。不收路径、SQL、清理动作；`stats resources\|storage\|quality` 在后面几步接入，现在回 `invalid`。细节见[运行页规格](../规格-运行页.md) | — |
| `whisper create --selftest --character <id[,id]> --source-text <文字> [--quote-name 名字] --say <文字> [--timeout 120s]`、`whisper status <批号>`、`whisper cancel <批号>` | 自检悄悄话：模拟主人引用一段话指给这些角色，等她们处理完，回各自的处理记录。来源是命令行给的文字，挂在第一个角色的自检话题上；请求里没有任何会话 id 字段，没有 `--selftest` 一律 `invalid`，碰不到真实的 QQ 会话和本地群。`status` 只读，只认这次运行里发的批号。规格：[群聊引用和路由](../规格-群聊引用和路由.md) 第十一节（`group`、`send` 还没做） | — |
| `whisper log --conversation <自检会话> [--batch <批号>] [--limit N] [--cursor <游标>] [--with-thinking]`、`whisper log --conversation <自检会话> --entry <条目id>`、`whisper list [--selftest]` | 读右侧栏“对话日志”的同一份投影（`internal/whisperlog`，条目：悄悄话、安排、思考、工具、记忆、唤起、返回、处理记录，每条带批号 `cause` 和上一级）；服务端只认自检会话（`audience.IsSelftest`），传 `qq:`、本地群、悄悄话会话串一律 `invalid`；思考原文默认不输出（`--with-thinking` 才给）；条目文字出去前过控制字符；`list` 只给批号、角色、状态、时间，不带正文。规格：[群聊引用和路由](../规格-群聊引用和路由.md) 15.6 | — |
| `artist list [--status pending\|confirmed\|declined\|all] [--platform x] [-n 100]`、`artist import <文件>`、`artist import --undo <批次>`、`artist confirm <ID>...`、`artist remove <ID>...` | 画师表（X 的画师，按账号数字 ID，[规格-画师原图](../规格-画师原图.md)）：`list` 只读，默认列待确认的（展开推特链接时发现的像画师的账号，不生效）；`import` 的文件是确认页导出的 `artists-confirmed.json`（绝对路径、不超过 8 MiB，由程序自己读），重复导入不产生新批次，有变化给批次号，`--undo` 只动还带着这个批次的行；`confirm`、`remove` 一次最多 200 个、整批要么都做要么都不做；会改表的三个不算只读，演示模式拒绝；名字、账号、线索名是外来文字，输出前清控制字符 | 链接展开的画师发原图 |
| `proactive log [-n 50]` | 主动联系的导演日志（只读，1～500 行）：各角色下次什么时候醒、系数、连冷次数，以及最近每次醒、被拦、没醒到各一行（不开口也有行），每行带当时的快照；备注是库里来的文字，加引号显示。规格：[主动联系](../规格-主动联系.md) 第七节 | — |
| `backup now [--media none\|stickers\|all] [--wait[=60s]]`、`backup list`、`backup restore <备份名> --to <新目录> [--wait[=60s]]`、`backup verify [<备份名>] [--wait[=60s]]` | 备份（[规格-数据目录与备份第十节](../规格-数据目录与备份.md)，R-386）：`now` 做一份手动备份（默认等 60 秒，到点没做完回“已开始”，不会被自动删），`list` 只读：总览（共几份、按原因各几份、实际占用硬链接只算一次、轮换规则和 `backup.alertGB` 提醒线、备份目录和所在盘剩余空间）加逐份列表；服务在后台 App 里（`internal/app/backup.go`），和每天的调度共用一把锁；`restore` 把一份校验通过的备份恢复成不存在或为空的新目录并逐文件、逐库核对（不切换、不碰现在的数据目录，10.10），`verify` 抽验（恢复到备份目录旁的 `Sumika-抽验`、核对、删掉、记下结果，`list` 总览显示“上次抽验”，自检 S12 读它），这两条和 `now` 一样任何档位都要主人点 | 无（命令行专用；工具清单里没有，测试守着） |
| `kernel faults`、`kernel retry [<角色> <会话>]` | 回合内核的故障（质检 X-07、后续路线 P0-4，见[运行时参考](runtime.md)的“内核故障”）：`faults` 只读，列出没解除的故障（哪个会话或整个内核、什么时候、什么原因、已重试几次）；`retry` 重写每处没写成的写入并读回确认，成功才恢复受理，不写角色和会话 = 全部；没恢复就失败（退出码 1），回复里带着还没恢复的那些。入口只在本机管道，不在模型能调的工具里；自检 S10 `kernel.faulted` 对应这里 | 无（命令行专用；界面里的“重试”按钮还没做） |
| `mcp status`、`mcp release <服务> --reason "原因" --accept-unknown-risk`、`mcp reconnect <服务>` | MCP 服务的隔离和重连（[规格-MCP客户端](../规格-MCP客户端.md) 2.4，质检 X-08）：`status` 只读，列各服务状态和被隔离的旧调用（原因、时间）；`release` 主人带原因接受“旧调用结果未知”的风险后解除（原因必填、必须带开关，旧行改 `risk_accepted` 并记人、时间、原因）；`reconnect` 让自动重连用完次数的本机服务再连一次，不能解除隔离。不是模型工具；只看历史、演示模式拒绝 | — |
| `auto-receive on\|off` | 启动后自动接收 QQ，默认关；存在 `<数据目录>/go/desktop-settings.json`，不改 sumika.yml；和托盘的勾选项是同一个开关 | 启动后自动接收 QQ |
| `autostart on\|off` | 开机自动启动（Run 项写成 `sumika.exe -watch`，由守护进程拉起桌面程序） | 开机自动启动 |
| `quit` | 先回"正在退出"，再走唯一退出流程 | 退出 Sumika |

**输出**：不加参数是给人看的中文，成功写 stdout、失败写 stderr；加 `--json` 一律在 stdout 打**一个** JSON 对象 `{"ok":…, "data":{…}, "error":"…", "code":"…"}`（连不上时 `code` 为 `not-running`，命令行写错时为 `invalid`，退出码 3），Agent 读它就够了。`--demo` 连演示数据那个实例（`sumika.exe -demo`，管道名多一个 `-demo`），那里 `receive`、`gate`、`auto-receive` 回 `demo`。

**退出码**：0 成功；1 命令执行失败（含 `readonly`、`timeout`、后台没起来）；2 桌面程序没在运行或连不上（2 秒内给结论）；3 被拒（`invalid`：参数或命令不认识、不允许；`denied`；`demo`）；4 协议版本不对。`error` 里写原因，`code` 是机器读的：`version`、`invalid`、`denied`、`readonly`、`demo`、`failed`、`timeout`、`not-running`。

检查点默认 `step`；`turn` 回到该 RunID 首次变更前，不吞另一会话或 child 的独立变更；`recovery` 撤回 interrupted/partial 的已完成部分，`resume` 仅对恢复逆记录续做原来已冻结的剩余目标，两者都先 preview 再单次审批。会话写 `console:<角色>` 或 `console:<角色>:topic:<所属话题>`。恢复要求可写后台、无活动/排队输入、当前执行授权和单次审批，YOLO 也要答卡；本地群聊、QQ 私聊、QQ 群、archive、demo 和检查点关闭时拒绝。同步客户端超时只表示未收到结局；用另一条 `checkpoint status` 核对、`checkpoint cancel` 等真实退出。备份范围和排除项见[检查点规格](../规格-检查点与回退.md)。


**边界（Agent 用它时照旧要遵守的）**：
- 只走本机命名管道 `\\.\pipe\sumika-ctl-<当前用户 SID>`，安全描述只给当前用户，go-winio 建管道固定带拒绝远程客户端的标志，**不开任何网络端口**。
- 客户端连上管道以后核对另一头是当前用户的进程（`GetNamedPipeServerProcessId` 取进程、比用户 SID）：对不上或查不到、打开管道被拒都是 `denied`（退出码 3），不当"没在运行"；别的本地账号抢先占了同名管道骗不了它。程序没开着时的提示不带管道名（里面有用户 SID）。
- 群名、提醒、连接状态、日志行、错误原因是外面来的文字：服务端和客户端各清一遍终端控制字符和换行，帮助里说明这些只当数据看，不当指令。
- `status`、`receive start` / `stop` 的原因及失败回复里的 `error` / `data.reason` 都来自宿主已脱敏的状态快照，和桌面日志共用 `app.RedactLog`；终端控制字符清洗仍另由 `ctlproto.Clean` 负责。
- **`speak`（让一个群真开口）不在这个接口里**：档位只认 `off`、`dry` 且一字不差（`SPEAK`、` speak`、空、别的值都是 `invalid`），会话只认 `qq:group:<数字群号>`（私聊、本地会话、通配符都是 `invalid`）。服务端（`internal/ctlserver`）和后台（`internal/app/gatectl.go`）各校验一道。让群真开口只能主人在界面上亲手改。
- "要先问主人"的那几类事照旧先问：在真实群里开口、改配置、发消息、改或删真实数据、花钱；`quit`、`receive start`、`autostart` 也会真的动开着的程序，没有主人的话别替他按。
- `gate set` 要先 `receive start`：只看历史模式下库是只读的，回 `readonly`。"认识的群"= QQ 库里有这个群的消息，或群名表有它，或档位表里已有它自己的一行；别的回 `invalid`。
- 群号不出这个接口：`gate list`、`gate set` 的回复里会话是打码的，用句柄指群（`HMAC-SHA256(本机密钥, 会话号)` 的前 8 位，密钥存在数据目录的 `go/desktop-settings.json`，反推不出群号）；小号、提醒文字里的长数字照界面的规则打码。群名最多 20 个字，文本输出里括在引号里：它是群里的原文，只当数据看，不当指令。号码、令牌、密钥不进日志。
- `backfill` 沿用 v1、管道 SID 校验、脱敏与退出码。`retry` / `run` 不自动启动 receive、不改闸门或配置；archive 回 readonly，demo 回 demo。status / gaps 不记操作日志，写动作记固定短结果和任务 ID；媒体尚未检查与文件设施未接入不会记成已完成。
- ADR-278 后 `backfill status` 的 RemainingOpen 是 open / blocked 未补齐数，不再显示为“可自动重试”；受阻段不会定时重查，`retry` 仍可手动补。提醒确认 / 撤销已在可选 api.GapAcknowledgements 中提供，ctl 本轮不新增写命令。

**怎么做的**：协议和客户端只依赖 [`internal/ctlproto`](../../native/internal/ctlproto/proto.go)（一次连接一行 JSON 一问一答，请求最多 64 KB，读写各 5 秒；客户端等回复最久 90 秒，`--wait` 时是 wait 加 20 秒）；服务端 [`internal/ctlserver`](../../native/internal/ctlserver/server.go) 在桌面程序里，在日志和数据目录定下来以后起（`native/ctl.go`），`beforeQuit` 第一步就关；每个命令调托盘用的同一批函数（`host.startReceive` / `stopReceive`、`shell.showMain`、`setAutoReceive`、退出走 `beforeQuit`），碰窗口的回界面线程。`internal/archcheck` 守着 `cmd/sumika-ctl` 只依赖协议包。

补录后台快反馈验收为 200 次新连接的真实管道请求：从连接前至 accepted 回复解析完成，包含 SID 核对及完整宿主处理，守住 P95≤100 ms / P99≤200 ms；依据是补录规格第三节“任务接受后在 100 ms 反馈目标内显示排队 / 正在补”。独立 sumika-ctl 进程的 retry / run / status、输出和退出码另作端到端功能检查。原先把 200 次 Windows 进程启动 / 退出混入后台预算的历史量测，在[补录缺口规格](../规格-补录缺口界面与自动对齐.md)第六、九节明确更正；启动耗时仍保留分段诊断。
**配置 `backend.local: bun` 时**：`chat`、`goal`、`loop`、`trust`、`whisper create|status|cancel` 不再直调 Go 的 `DesktopReader`（那边回“本地私聊现在由 Bun 管”），而是由 `native/ctl_bun.go` 转给 Bun 的 `selftest.*` 方法，执行体在 Bun（`server/src/local/selftest.ts`）；Bun 不在回“本地后台没连上”，不退回 Go。`whisper log|list`、`checkpoint *` 在 Bun 模式下回明确的“还不能”。见规格-本地私聊切到Bun（私有存档）第十一节。`go` 模式一行没变。

来源：[cmd/sumika-ctl](../../native/cmd/sumika-ctl/main.go)；规格：[本机控制接口](../规格-本机控制接口.md)。

### sumika-models

本机嵌入（gte-multilingual-base）和重排（bge-reranker-v2-m3）服务：纯 Go 分词器加 purego 加载的 `onnxruntime.dll`，HTTP 形状照已删的 Python 版（ADR-389；差异见规格第三节）。`embed.backend: go-models` 时 Go 后台的 `embed.Local` 起它（命令行和退出码的处理见 `memory.md`；默认仍是 `legacy`，没人改配置就不会起），也可以手起，给评测用；不读写 `data/`，不花网关费用，日志写 stderr、不含输入文字、令牌、绝对路径。

```powershell
$env:SUMIKA_EMBED_TOKEN = '<自设令牌>'
go run ./cmd/sumika-models --models-root '<模型根目录>' --ort-library '<onnxruntime.dll 的绝对路径>' --models gte-multilingual-base --lazy-models bge-reranker-v2-m3 --max-line-tokens 2048 --port 11440
```

环境变量：`SUMIKA_EMBED_TOKEN` 必填，没有就拒绝启动，每个请求（含 `/health`）都要 `Authorization: Bearer <令牌>`；`SUMIKA_EMBED_PORT`（`--port` 没给时用，再没有取 11440）；`LOCAL_MODELS_ROOT`（`--models-root` 没给时用，都没有就报错，没有默认路径）。
参数：`--host 127.0.0.1`（只允许 127.0.0.1、localhost、::1）；`--port`；`--models-root`；`--ort-library` 默认本程序所在目录里的 `onnxruntime.dll`，必须在 `ort/known.go` 的已知好清单里；`--models` 点名的模型启动时文件和哈希必须对，缺省是登记的全部（去掉 `--lazy-models` 里的）；`--lazy-models` 也登记也列出但启动时只看文件在不在，缺了只它自己 `ready=false`、被请求时回 500，文件补上不用重启（生产里重排放这里）；`--max-loaded 2`（小于 1 按 1）；`--idle-unload 600` 秒（0 不自动卸，W1 传 0）；`--threads 2`；`--max-bytes 4 GiB`；`--wait 30`；`--max-request-seconds 600`（0 不设，到点回 504，客户端走了回 499，都只停在行边界）；`--max-line-tokens`（嵌入单行 token 上限，只能收紧，缺省是登记表的 8192，估计占 11 GiB 超过缺省 `--max-bytes`，所以真跑要传 2048 或同时放宽）；`--run-watchdog-seconds 60`（0 关；单次前向超过就退出码 4）；`--exit-on-stdin-eof`（读 stdin，EOF 就退出，父进程崩了不留孤儿；默认关，手起时 stdin 可能是 NUL）；`--verify`（不起服务、不要令牌、不开 dll，算全部登记模型文件的哈希对登记表，退出码 0 / 1）。没有 `--config`（登记表编在程序里，给了退出码 2）。
接口：`GET /health`（`models`、`loaded`、`resident_bytes` 是估计、`rss_bytes` 是进程真实常驻内存、`run_elapsed_ms`、`last_ok_unix`、`engine`、`ort_version`；只读池的一份快照，前向和加载进行中也答）、`GET /v1/models`（每个多一个 `ready`）、`POST /v1/embeddings`、`POST /v1/rerank`、`POST /v1/models/{id}/unload`。请求体上限 32 MiB（413），类型不对 422，同时连接 32 个、请求头 10 秒。
退出码：0 正常（信号或 stdin 关闭后先停 HTTP、再关池、最后关运行时）；1 `--verify` 对不上或运行中没法恢复的错；2 配置、文件、dll 的确定性错误（缺令牌、非回环地址、根目录没配、登记表或点名模型的文件对不上、dll 不合格、参数不对），Local 见到停止重试；3 端口被占（Windows 的 10048 和 10013 都算），Local 换口重试；4 看门狗。出错时 stderr 第一行是 `[sumika-models] 错误：…`。
第一次请求才加载模型；启动时的 `CHECKED`、`READY`（含监听地址）、请求的 `EMBED` / `RERANK`、`LOADED` / `UNLOADED` / `WARN`、收尾的 `STOP` / `EXIT` 都是一行一条。dll 放在 exe 旁边最省事；用官方发布包里的那份（`scripts/fetch-ort.ts` 取回，哈希登记在 `ort/known.go`，规格 2.3），Python wheel 里的不认。
来源：[main.go](../../native/cmd/sumika-models/main.go)、[server 包](../../native/internal/localmodels/server/)；规格：Go 本地模型（私有存档）、本地模型服务（私有存档）。

## 界面检查

### ui-tour

`go run ./cmd/ui-tour -app <e2e exe> -socket-dir <当前用户独占短目录> -out <新输出目录>`。三个参数必填，无默认数据目录；只启动该 exe 的 `-demo` 实例，以每次不同 socket 隔离单实例和控制管道。关闭时只停止持有的子进程，绝不调用日用 `sumika-ctl`；不读真实数据、不联网、不发送消息。Windows 也使用 Unix domain socket，长 scratchpad 路径用临时 `subst` 映射；完整构建、手动连接和启动命令见 [native/README.md](../../native/README.md#真窗口自动巡视)。
按钮按元素树的 DIP 命中框通过官方 `input/click` 激活，文字编辑与滚入视图走 `act`。虚拟列表用现有“搜索会话”定位，随后核对聊天标题和已加载页；不会在找不到行时盲点坐标。
深浅主题由仅 e2e 的 `SUMIKA_AUTOMATION_THEME=light|night` 在启动时选定，演示外观只留在内存；非法值返回 2。这样即使窄窗的主题按钮已越界，仍能收集该布局的两种主题，缺陷保留在截图和规则报告。



巡视覆盖深浅主题、宽窄客户区下的会话列表、本地私聊/群聊、QQ 私聊/群聊长文图片撤回、三个右侧页、十个演示设置页及页尾、搜索空状态、记忆和运行页。每状态先 `idle`，再保存 PNG、JSON；然后打开 F12 的 Issues，保存检查器树并关闭。JSON 中的 `frames` 与 `inspectorFrames` 分开，PNG 大小由真实 capture 返回。`index.html` 一页排图，`report.json` 汇总计数、最慢帧、进程 CPU 和峰值工作集，`.log` 留官方帧原文。失败退出 1 并保留已采集结果；缺参数退出 2。规则发现问题留在报告，不把现有产品缺陷当工具运行失败。
`idle` 到 10 秒仍活跃的状态会记录 `idleErrors` 并截下当时画面，继续收集余下状态；全部采集后仍退出 1，index 会显示失败。不会把没静止的窗口算作通过。每帧原文只放 `.log`，JSON 按帧号保存数值，最慢帧附状态名供回查。


六类树规则与可判范围见[规格](../规格-原生界面自动巡视.md)：AX 没有完整布局、字形和省略号字段，裁字与 AX 重复 Key 标候选；真实布局重复 Key 以 Issues 为准。同族目前只对同父 tab/radio 高度自动比较；不能据此宣称整套界面通过设计自查。

### mygo vet

`go tool mygo vet .` 使用模块锁定的 `github.com/egoist/mygo/cmd/mygo` 工具，先执行 `go vet ./...`，再检查 native UI 构建值寿命与 goroutine 捕获。根包非测试 Go 文件或 go.mod/go.sum 改动时，`scripts/go-verify.ts` 与 `scripts/pre-push.ts` 都调用同一个 `verifyMygo`，失败不继续记录验证成功。只改内部包、测试或文档不触发；`go-verify --full` 也运行。

## 评测和压测

### e2e-run

**会调用真实模型并产生费用；只建全新临时数据，OneBot 和图片服务均为自己起的随机回环端口。** 命令名和角色“岚禾”是我定的。用真实 `app.Open` / `App.Run` 的 receive 装配，号码 100/200/300/400 全部编造，不连接真实 QQ 协议端，不操作桌面程序。

```powershell
go run ./cmd/e2e-run -scenario smoke -out "<临时目录>/e2e" -models-root <模型根目录> -models-exe 仓库根/native/sumika-models.exe -ort-library 仓库根/native/onnxruntime.dll
```

`-scenario full` 默认完整剧本；`smoke` 是两轮“记下钥匙位置 → 问回”；也可给 JSON 文件路径，文件形状见 [完整剧本](../../native/cmd/e2e-run/scenarios/full.json)。完整剧本含主人私聊、图、引用、撤回、白名单群聊天和 @、群友追问主人私事、断线和离线历史消息。群聊隐私只记录原文和生产守卫结局，不判对错。离线消息只放在假端历史，不实时重推；报告显示有没有发起补录和实际入库。

参数：`-root` 空，向上找资源根；`-model auto/deepseek-v4.1-flash`；`-gateway http://127.0.0.1:3425/v1`（含 /v1）；`-turn-timeout 3m`；`-out` 是运行目录的父目录，必填，每次新建时间戳目录；`-delete false`，开启只删本次 `data/`，报告保留。

**嵌入的必要调整（我定的）**：当前发送门要求标定的 gte 守卫 full，关闭嵌入或 legacy 都无法完成主人私聊回复。因此必须给已有的 `-models-root`（也可用 `LOCAL_MODELS_ROOT`）、`-models-exe`，可选 `-ort-library`（默认同 exe 目录）；只加载真实小型 `gte-multilingual-base`，不起本地生成式大模型、不下载资源、不伪造 full。remember 开，后台整理关以控制费用（我定的）。空记忆库由检查工具建表后走现有迁移、备份和启用契约；从环境继承的 `SUMIKA_CONFIG` 会在运行期间指到新配置。

输出在运行目录：`report.md`、`results.json`、`scenario.json`、`backend.log`，旁边的 `data/` 留有配置、编的卡、聊天/记忆和下载的 PNG。报告记录每轮首/末发送耗时、nearest-rank P50/P95（无回复不计零）、回复原文、所有模型 HTTP 调用和 usage（含工具循环、记忆和守卫）、最终记忆及召回证据、收图状态、日志警告/错误摘录。Windows 复用 soak 的 OS 内存计数结构取工作集峰值，GetProcessTimes 取 CPU 时间差；只统计 e2e-run 自身（包括 app 和假服务器），不包含网关和独立嵌入子进程。失败/超时保存部分报告并返回非零，未取得 usage 单列为未知。

验收只跑 `./cmd/e2e-run`，不跑整套；手动测试和真实运行都应先遵守当前 `heavy-job.lock` 协调约定，再交 `scripts/run-low.ps1` 低优先级运行。完整真实剧本留给主人执行。

来源：[cmd/e2e-run](../../native/cmd/e2e-run/main.go)；规格：后台真跑检查（私有存档）。


### soak

**默认往主工作区 data/ 写报告。** 用编的对话和假网关跑真实内核，查长期资源开销。

```sh
go run ./cmd/soak -duration 72h -interval 1m -rate 0.5 -out "<输出目录>/soak"
```

参数：`-duration 72h`；`-interval 1m`；`-rate 0.5` 句/秒；`-out` 空，默认主工作区 `data/dev/soak/<时间>/`。
不读真实配置或聊天库，不调真实模型。写 `samples.csv`、`summary.txt`；输出目录下的临时内核正常收尾删除。少于 72 小时只作观察。三小时以上的运行跳过第一小时预热，拿第二小时和最后一小时比（ADR-219）；判定改了以后看旧的运行用 `go run ./cmd/soak -report <输出目录> -duration <那次实际时长>`，只打印，不改原来的 `summary.txt`。
来源：[main.go](../../native/cmd/soak/main.go)；规格：[Go 后台 B6](../规格-Go后台.md)。

### stress

**默认往 data/ 写报告和夹具。** 假网关并发跑本地会话，查串会话、卡住和开销。

```sh
go run ./cmd/stress -sessions 10,50,100 -characters 4 -turns 10 -out "<输出目录>/stress"
```

参数：`-sessions "10,50,100"`；`-characters 4`；`-turns 10`；`-parallel-tools 3`；`-steer-ratio`、`-stop-ratio`、`-compact-ratio` 各 `0.05`；`-diagnose-handles false`，开启后每档同进程跑三次。
不读真实聊天库、不调真实模型；写夹具、`summary.md`、`results.json` 和分档证据；句柄诊断写 `handles.md/json`。`-out` 默认是写死的本机项目 `data/dev/stress/<时间>/`，换机器须覆盖。
来源：[main.go](../../native/cmd/stress/main.go)；规格：[Go 后台 B7](../规格-Go后台.md)。

### bench-t2

**正式跑会花模型费用，默认报告写主工作区 data/。** 表二执行器：读题、起壳、判分、核对网关账本；dry 可先看估算。

```sh
go run ./cmd/bench-t2 -work "<仓库外工作目录>" -out "<输出目录>/bench-t2" -shells go -dry
```

常用参数：`-work` 空，必填且须在 git 仓库外；`-shells "go,omp"`（仅支持 go 和 omp）；`-repeat 1`；`-only` 空（题号正则）；`-dry false`；`-turn 600` 秒；`-model "auto/deepseek-v4.1-flash"`；`-thinking high`；`-character suzu`；`-gateway http://127.0.0.1:3425`（不含 /v1）；`-approve false`（批准限定在工作目录内的审批）。
`-root` 空，向上找项目；`-tasks` 空，取 `<项目根>/scripts/bench-t2/tasks`；`-out` 空，取主工作区 `data/dev/bench-t2/<时间>/`。dry 只建工作目录、拷题、查判分，不调模型；正式跑写 `meta.json`、`runs/*.json`、`summary.md`，会执行模型工具。
来源：[main.go](../../native/cmd/bench-t2/main.go)；规格：表二执行器（私有存档）。

### bench-shell

**会花模型费用、写题目指定目录。** 表二 Go 壳，由 bench-t2 调用，也可单独重跑题目。

```sh
go run ./cmd/bench-shell -task "<题目.json>" -out "<结果.json>"
```

参数：`-task`、`-out` 空，运行须给文件路径；`-root` 空，向上找项目根。
读 Task JSON（与题库格式不同）：在 `dataDir` 写卡和库，在 `workdir` 执行工具，向 `gateway` 调模型，结果写 out；先核对三个字段，避免指向真实目录。
来源：[main.go](../../native/cmd/bench-shell/main.go)；规格：表二执行器（私有存档）。

### memory-eval

**读真实记忆和聊天库，往数据目录写结果。** 用考题量召回、可见范围和耗时，对比全文、嵌入和重排。

```sh
go run ./cmd/memory-eval -data "<数据目录>" -exam "<考题.json>" -novec
```

参数：`-data` 空，必填；`-exam` 空，默认选 `dev/memory-eval/` 下按文件名排序最后的 `*-v0-*.json`；`-novec false`；`-group 300` 条群消息；`-tag go-v2`。
`-embed-url`、`-embed-model` 空，成对指定嵌入服务；`-embed-timeout 10m`；`-rerank-url`、`-rerank-model` 空，成对指定重排服务；`-rerank-k 40`；`-topk 0`：明确回忆题给 12 就走线上 `memory_search` 带 TopK 的那条 Hits 分支（重排 A/B 要走同一条分支才比得准），0 沿用评测原来的筛选分支。重排只在明确回忆题上用（线上也只在 `memory_search` 上用）。
`-dump-embed-inputs <文件>`：不连任何模型，用假嵌入器和假重排把这一趟会送去嵌入和重排的字符串（条目、考题查询、群消息查询、守卫的句子、最多 100 次重排的查询和候选）原样写成 [embedinputs](../../native/internal/embedinputs/embedinputs.go) 格式的 JSON，给需要"生产实际送进去的字符串"的排查和比对用；不能和 `-embed-url`、`-rerank-url`、`-novec` 同时给，不写结果 JSON，终端只打条数。文件是真实原文，只许写在临时目录或被 git 忽略的路径（写之前问 git，否则拒绝），用完自己删。
`-models-exe`、`-ort-library`、`-models-root` 空：不带 `-embed-url`、按配置起本机嵌入时用，盖过 `embed.modelsExe`、`ortLibrary`、`modelsRoot`（取绝对路径）；评测没有资源根，所以 `sumika-models.exe` 要么写在 `embed.modelsExe`，要么给 `-models-exe`。
库及 WAL/SHM 拷到系统临时目录，索引建在副本，正常退出删除；结果写 `<数据目录>/dev/memory-eval/runs/<时间>-<tag>[-novec].json`。默认按 `config/sumika.yml` 的 embed 一节起本地嵌入子进程（用 config 包解析，和后台装配同一处：起 `sumika-models.exe`；`modelsRoot` 空和相对路径按数据目录解析，`modelsExe` 空时评测没有资源根可以找默认的，要写配置或 `-models-exe`）；手起的 `sumika-models` 用 `-embed-url` 连；novec 关闭嵌入，重排默认关闭。指定服务后会发送查询和候选，费用取决于服务。
ADR-242 / ADR-162：存在 `shared/owner-memory.db` 时也拷进临时目录，角色与共用池参加同一次召回。主人问后群友再问分开报告“再次召回”和“字面披露检查缺口”，再次想起不算泄漏；后者应为零。未读真实样本时不把这项诊断当正式语义验收。

来源：[main.go](../../native/cmd/memory-eval/main.go)；规格：[记忆 v2](../规格-记忆-v2.md)、本地模型服务（私有存档）。

### guard-eval

**只读合成数据目录，往里写结果；方法 A / B / C 会调付费的确认模型（费用要先报给主人）。** 量发送前守卫的拦住率、误拦率和延迟（规格-记忆-v2 13.2.8，ADR-202）：A 只比原文，B 每段问模型，C 先比原文、再比嵌入相似度、过门槛才问模型。数据集是 `-data` 目录里的 `dataset.json`（不进 git，合成的）。

```sh
go run ./cmd/guard-eval -data "<合成数据目录>" -method C -round 1 -limit 6
go run ./cmd/guard-eval -data "<合成数据目录>" -report            # 汇总三遍
go run ./cmd/guard-eval -data "<目录>" -method S -round 1 -engine python
go run ./cmd/guard-eval -data "<目录>" -method S -round 1 -engine go -models-exe "<sumika-models.exe>" -models-root "<模型根>" -ort-library "<onnxruntime.dll>"
go run ./cmd/guard-eval -compare-scores "<目录>/round1-S-python.jsonl" "<目录>/round1-S-go.jsonl"
```

参数：`-data` 空，必填；`-method` 空（A / B / C / S）；`-round 1`、`-first-round 1`、`-start 0`、`-limit 6`；`-threshold 0.30`（丙的实测门槛，扫描固定 0.30～0.90）；`-model auto/deepseek-v4.1-flash`、`-base http://127.0.0.1:3425/v1`；`-python`、`-server` 起 Python 引擎的 `server.py`（默认写死 MaiBot 的 venv，GM9 去掉）。
**`-method S`（规格-Go本地模型 GM7 验收 5，不花钱）**：只算嵌入相似度，不调确认模型，也不记 1500 次预算的账（本机嵌入不花钱，`calls.jsonl` 不写）；每个样本每一段（前面全部段加上本段）对每条禁看条目的最大相似度写进 `round<N>-S-<引擎>.jsonl`；数据集只查结构（小数据集也行，A / B / C 的 25～30 条记忆、正负各 30、12 类各 5 的要求不变）。`-engine go`（默认，2026-11-18 起）或 `python`（Python 服务已从仓库删掉，要用先从历史取回，ADR-389）：`go` 用 `embed.Local` 起 `sumika-models`，命令行和后台完全一样（`-models-exe`、`-models-root` 必填，`-ort-library` 空取 exe 旁边的）。A / B 不用嵌入，不分引擎；C 用嵌入，`-engine go` 的结果文件名带 `-go`（`round<N>-C-go.jsonl`），和 `python` 引擎的三遍放在同一个 `-data` 里互不覆盖，`-report -engine go` 报告 Go 一侧的丙（规格-Go本地模型 GM7 验收 6：同一份数据、同一轮次两种引擎各三遍，逐样本决定相同；调确认模型要花钱，先报估算再跑）。
`-compare-scores a.jsonl b.jsonl`：比两份 S 的结果，不跑嵌入、不读数据集。判：两边样本、段数、字面命中一一对上；全部段最大相似度的最大差 D ≤ 1e-5；门槛 0.30～0.90 各档上两边“拦 / 不拦”决定（字面先命中，或相似度够了，比较用线上的 `memory.GuardReaches`，float32 比）不同的翻转，到门槛的距离都不超过 D；线上门槛 0.70 附近（±0.01）不足 50 段写“这一边没量够”。不通过退出码 1。
来源：[main.go](../../native/cmd/guard-eval/main.go)、[scores.go](../../native/cmd/guard-eval/scores.go)；规格：[记忆 v2](../规格-记忆-v2.md) 13.2.8、Go 本地模型（私有存档） GM7。

### memory-shadow

**读真实聊天库和记忆库的拷贝，拷贝时要拿数据锁，会占电脑一阵。** 影子整理：把 Go 的聊天库组和一个角色的记忆库拷进 `<数据目录>/dev/memory-shadow/<时间>/copy/`，在副本上把后台整理跑一遍，出报告给主人抽检；原库只在拷贝那一段被读。估算和真跑由主会话跑，不交给实现子代理。

```sh
go run ./cmd/memory-shadow -data "<数据目录>" -char "<角色 id>"
go run ./cmd/memory-shadow -score "<报告目录>/marks.csv"
```

参数：`-data`、`-char` 空，必填；`-work` 空，默认 `<数据目录>/dev/memory-shadow/<时间>`，必须在 `<数据目录>/dev/` 下、是空目录、被 git 忽略（根本不在仓库里也行）；`-places` 空，默认只有 `owner-private`（就是 W5 的 `memory.write.places` 的五个取值和默认，原样交给 `memory.Open` 的 `WritePlaces`，所以 W5 的场合门是第一道，`classify.go` 的筛选是第二道、只会更窄），可加 `local-private`、`local-group`、`platform-private`、`platform-group`；`-cursors real`（副本里的真实游标），或 `seed` 配 `-last <天数>` / `-since 2006-01-02`（报告里写明不是真实游标）；`-max-batches 50`（每个会话）；`-gen` 空 = 估算（用固定的空回复把整理循环在副本上真跑一遍，只数批数和字数，一次模型都不调），`fake` = 假生成器，`real` 必须同时给 `-spend <次数>` 且现在关着（`gen.go` 的 `realGenReady`：场合门已接上，缺真实生成器、W5 合进 main 和主人同意的 `-spend` 数）；`-prompt` 换整理提示词；`-keep` 不删副本；`-seed 1`；`-name`、`-root`；`-score <marks.csv>` 只读主人标好的清单算精确率，不拷库。
拷贝：先拿 `go/backend.lock`（桌面程序或 Go 后台在跑就拒绝），只拷 `go/` 下的 Go 库（记忆库经 `paths.CheckMemoryPath` 检查：必须是 `go/characters/<id>/memory.db`，Bun 的原库一律拒绝，联接、符号链接、多个硬链接不认；聊天库和 `trace.db` 按真实位置比），不拷 `memory-v2.db`；拷前拷后各记一次指纹，对不上就重拷，副本 `integrity_check` 通过才往下；数据锁持到这些都核对完才放（前后指纹一致、记忆库和 `-wal` 与源逐字节相同、副本 `integrity_check` 通过；指纹对不上或 `integrity_check` 不过就重拷、锁一直拿着，逐字节对不上直接报错），之后工具只证明“拷贝那一刻是一致的”。
副本里记忆库开着整理（`SummarizeEnabled`）和自动提升（`AutoConfirm` 强制打开，正式配置里它默认关），整理前先做一遍启动补提升（`ConfirmSweep`，不调模型），导入的旧候选攒够证据会不会被一起提升，报告 C 节先让主人看到；账里“看了”只数范围在 `-places` 放行场合里的，范围不放行的另数“没碰”（总览、C 节、`summary.json` 的 `sweep.skipped`、运行日志都有）。
输出写 `<work>/out/`：`report.md`（有聊天原话，只由主人自己打开）、`summary.json`（只有计数、编号、范围，会话和子代理读这个）、`marks.csv`（待标清单）。C 节列这次提升的条目（新写的和导入的旧候选），D 节列受保护的统计和卡在受保护旧事实上的候选，F 节自检含“受保护的有效事实跑完没被动过”。副本默认跑完删，崩溃遗留的下次启动清（问锁，不猜 PID）；`-keep` 留下的运行结束时记一个 `.keep`，下次启动不清、日志记一行跳过。

W6b 编造数据模式：`memory-shadow -fixture issue17 -work <尚不存在的临时目录>` 只估算，不接受 `-data` / `-char`；`-gen fake` 跑确定性夹具；`-gen real -spend N -model agnes-ai/agnes-2.5-flash -embed-url <独立嵌入服务/v1> -embed-model gte-multilingual-base` 通过 Magpie 跑真模型，并经生产 `embed.Connect` 调真嵌入（令牌沿用该客户端的环境变量约定；real 缺嵌入参数即拒绝；这个模式独立于真实库影子整理的 `realGenReady`）。先 `-sample 2` 小样本，完整夹具可 `-rounds 3`，超过 300 次先交回；本轮任务总预算 250 次。独立的本地嵌入进程启动及运行均需重活锁，跑完关闭该进程。本模式自己创建正式形状的平台聊天库和两个角色库，走 `memwrite.Open`、`Hub.NotifySeen`、`memplugin` 排程，关库重启后由实际 `Hub.RespondContext` 生成最终回答；不接 SnowLuma，不读用户配置。假嵌入仅量机制，不代表语义检索质量；报告单列嵌入调用 / 服务失败，服务失败使语义成绩无效，正常关库取消的后台请求不计服务失败。

W6b 产物是该临时目录中的 `results.json`（编造事件、来源、修订、原话结果、召回 / 四场合矩阵、模型实际请求与最终回答、token 和资源）、`marks.csv`（事实待标，仍用 `-score`）、`answers.csv`（回答待人工判题）和 `files.json`（文件字节清单，不含清单自身）。字符串匹配只作诊断，不冒充正确率；ADR-242 同角色跨场合应召回，角色隔离、原话 observations、写入准入、最终输出分别计失败。提交前故障用临时 SQLite 触发器中止同一事务的 job 记账；提交后故障先破坏派生全文表，使主库已提交、内存刷新失败，再重启重建 FTS / 向量并重放通知。正常整理等待已有排程的两分钟退避重试，网关和回答不另加重试；QQ 群 `PrivateOK=false`，主人问起由 `OwnerAsked` 单独表达。所有主库和报告保留在显式临时目录供复查，不扫描或清理旧运行；进程资源量测仅支持 Windows。
ADR-162 的夹具使用正式 `OpenOwnerPool`；角色任务/游标仍从角色库读取，共用事实、证据与修订从共用库导出，合并后的行带 storage=owner-pool。故障恢复破坏并重建共用库自身的派生索引；不把导出视图反写角色库，不由夹具补来源。


来源：[main.go](../../native/cmd/memory-shadow/main.go)；规格：[记忆写入上线 W6](../规格-记忆写入上线.md)。

## 开发自检

### sumika-dev：开发工具程序

仓库里的脚本和钩子逐个改写成它的子命令（ADR-389，[规格](../规格-脚本迁Go.md)）；现在有 `version`、`help`、`notice`、`check docs`、`check private`、`check conflicts`、`check gofmt`、`check commit-msg`、`check ui-skill`，后面每迁一个加一个子命令，同提交换掉钩子里的旧调用。

- `check docs [--staged] [--update]`：文档一致性检查（D1、D2、D4、D8、D10～D14），原 `scripts/check-docs.ts` 和 `check-spec-merged.ts`；逻辑在 `internal/devtools/docs/`。`--staged` 读暂存区里的版本（一次 `git cat-file --batch`），并继承钩子给的 `GIT_INDEX_FILE`、`GIT_DIR`（部分提交时临时索引才是这次要提交的内容）；不带 `--staged` 和查分支是否已合进 main 时清掉全部 `GIT_*`。`--update` 写 `scripts/docs-baseline.json`（旧名计数，键顺序和旧版逐字节一致）。有“错”退出码 1，只有“提醒”退出码 0；读不到文件这类没法继续的也是 1。git 子进程统一走 `internal/devtools/gitrun`，程序名由 `cmd/sumika-dev` 注入（archcheck 不许 `internal/` 里写字面量程序名）。
- 钩子里的调用：`lefthook.yml` 的“文档检查”一行 sh，`native/bin/sumika-dev.exe` 不存在时先 `go build` 一次再调用；新旧对拍记录见主工作区 `data/dev/scripts-go/check-docs-对拍.txt`。
- `check private --staged | --range A..B | --allow 编号… | --tree 目录 [--allow-missing]`：隐私扫描，原 `scripts/scan-private.ts`；逻辑在 `internal/devtools/private/`。查新增的行、文件名、提交说明、作者和提交者里有没有本机认识的号码、名字、私人字符串、像密钥的串；号码分“拦”和“提醒”，输出只有打码的样子和位置；`--allow` 只登记哈希，认识的号码和私人字符串只对那个文件放行。名单只在内存里，从主工作区（按 git 的 common dir 找，worktree 里没有 `data/`）读这几类来源：Go 聊天库、MaiBot 库（环境变量 `SUMIKA_SCAN_MAIBOT` 指定，测试用）、群名表、`sumika.yml` 里 QQ 相关的号、私人字符串名单；缺聊天库或认识的号码为 0 时退出码 2、不扫（`--allow-missing` 才跳过）。有要拦的退出码 1，只有提醒 0。
  - 入口：`lefthook.yml` 的“隐私扫描”（`--staged`，写法同文档检查）、`scripts/pre-push.ts`（`--range`）；两处都经 `scripts/lib/sumika-dev.ts` 或同样的一行 sh 保证程序已编好。
  - 读来源用 `modernc.org/sqlite`（只读）和 `goccy/go-yaml`（走 AST 取标量原文，按 YAML 1.2 核心模式自己判断，和旧版 Bun.YAML 一致；重复键、复杂键新版有意报错退出 2）。`SUMIKA_SCAN_DATA` 只给测试用，把名单所在的主工作区指到编造的临时目录。
  - Go 正则没有前后查找：数字段的边界、`sk-` 词头改成显式判断，三个以 `$` 结尾的上下文判断手写；`/i` 只认 ASCII，小写转换照 JS 补了 İ 和词尾 Σ。新旧对拍记录见主工作区 `data/dev/scripts-go/scan-private-对拍.txt`。
- `check conflicts`：暂存区新增的行里有没有没解决的合并冲突标记，原 `scripts/check-conflict-markers.ts`；逻辑在 `internal/devtools/conflicts/`。只读 `git diff --cached -U0` 加进来的行，`<<<<<<<`、`>>>>>>>` 单独出现就红，`=======`、`|||||||` 只在同一文件里还有前两种时才跟着报；继承钩子的 `GIT_INDEX_FILE`；退出码 1 有标记或读不到暂存区。新旧对拍记录见主工作区 `data/dev/scripts-go/check-conflict-markers-对拍.txt`。文本小函数（JS 的 trim 空白集、按 WHATWG 解码 UTF-8）在 `internal/devtools/jstext/`，后面几个小关卡共用。
- `check gofmt 文件…`：暂存的 .go 文件有没有 gofmt，原 `scripts/check-gofmt.ts`；逻辑在 `internal/devtools/gofmt/`。查的是暂存区里的内容（`git show :<文件>` 交给 `gofmt -l` 的标准输入），不查工作区：worktree 签出是 CRLF，工作区查会把没问题的文件全报出来。在程序所在那棵树的根上跑（旧脚本靠钩子在根上启动，从子目录跑会读错索引；新版不看当前目录），继承钩子的 `GIT_DIR`、`GIT_INDEX_FILE`。有文件没 gofmt、读不到暂存区、gofmt 自己报错都是退出码 1，读不到和报错立刻退出、不再列前面没 gofmt 的文件（和旧版一样）。`gofmt` 程序名由包里的 `gofmt.Program` 变量给（archcheck 不许 `exec.Command` 写字面量）。对拍记录 `data/dev/scripts-go/check-gofmt-对拍.txt`。
- `check commit-msg <说明文件>`：提交说明的标题检查，原 `scripts/check-commit-msg.ts`；逻辑在 `internal/devtools/commitmsg/`。“类型(模块): ”后面超过 40 个字（按码点数）、带分号、整行带 ADR 编号就拦；`Merge `、`Revert `、`fixup! `、`squash! `、`合并 ` 开头不查，正文不查。参数是 git 给的路径：存在就读；不存在时（反斜杠被 sh 吃掉的 D:… 路径）按结尾认 `MERGE_MSG`、`COMMIT_EDITMSG`、`.omp-commit-msg-…`，去 `git rev-parse --git-path` / `--git-dir` 报的位置找；找不到退出码 2、不放行。和别的 check 不同，它按当前工作目录（钩子启动的目录）找文件和仓库，不按程序所在的树。退出码 1 标题不合规，2 没参数或拿不到说明文件。对拍记录 `data/dev/scripts-go/check-commit-msg-对拍.txt`。
- `check ui-skill`：界面技能入口页 `.claude/skills/mygo-ui/SKILL.md` 和代码对不对得上，原 `scripts/check-ui-skill.ts`；逻辑在 `internal/devtools/uiskill/`。查四件事：入口页超过 150 行；反引号里的 `internal/…` 路径在 `native/` 下没有对应目录；反引号里的 `包名.名字` 在 `native/internal/<包名>/` 的非 `_test.go` 里找不到 func（含方法）或 type（包名不是本仓库的，如 `ui`，只记“没核对”）；写的 `mygo vX.Y.Z` 和 `native/go.mod` 的版本不一致或一处没写。读的是工作区文件，不是暂存区（入口页很少和别的改动同时半改）。有错退出码 1，没错打“核对了 N 个包、N 个名字，跳过 N 个”。原 JS 的 `\s`、`\b` 在 Go 里按 JS 的空白集写（`uiskill.jsSpace`）。钩子的 glob 是入口页、`go.mod`、`native/internal/winshell/` 和这个检查自己的源码。对拍记录 `data/dev/scripts-go/check-ui-skill-对拍.txt`。

- `notice [--reports 目录] [--no-ci]`：主会话的报告提醒，原 `scripts/report-notice.ts`；逻辑在 `internal/devtools/notice/`。Claude Code 的 UserPromptSubmit 钩子（`.claude/settings.json` 里的一行 sh：先 `cd "${CLAUDE_PROJECT_DIR:-<git 顶层>}"`，会话从 `native/` 之类子目录启动也找得到程序；程序不在才兜底编，编的时候带上和自我重编一样的摘要 `-ldflags`（先编一个临时程序，用它的隐藏子命令 `__srcdigest` 问源码摘要，再带摘要重编），以后别的关卡不用再重编一次；超时 30 秒给第一次编译留的），从标准输入读钩子 JSON（`session_id`、`prompt`），把近两天（今天和昨天）`data/reports/` 里 `复盘-` / `巡检-` / `早报-` 加日期的报告里这个会话没看过的“## 给主会话”一节打出来（打出来的进这一轮上下文）；没有这一节或只写“没有 / 无”不打，定时任务自己的会话（提示词含 `scheduled-task` 或 `SCHEDULED TASK`）和没有会话号都不提醒，一份报告最多 2000 个 UTF-16 单位、多了截断并给路径。记录在 `data/reports/.已提醒.json`，按会话分开、每个会话每份报告只提醒一次，7 天没动的会话删掉；在 worktree 里跑时往上找主仓库的 `data/`。**永远退出码 0**：任何错误（读不了目录、报告、记录，标准输入不是 JSON，找不到仓库根）最多往 stderr 写一行，不挡主人发消息。`--reports` 只给试用、冒烟把报告目录指到别处。和旧版的差别只有三处，都不影响正常使用：记录改成先写临时文件再改名（别的会话同时在读不会读到半截）；记录的顶层不是对象（`null`、数组等）时旧版抛异常什么都不打，新版当写坏了从空的开始；某个会话的字段类型不对同理。`notice` 不做前台重编（main 里直接分派，`__srcdigest` 同理）：切分支、合并后第一条消息不会去等重编锁和编译（最长几分钟、会被 30 秒超时掐掉），程序旧一点也照跑，由提交钩子等别的关卡保持最新。今天或昨天名字像报告、但读不了的文件（是目录、没权限）只跳过它、不记成看过，其他照常。新旧对拍（458 个场景 480 步，含 400 个随机文档）记录见主工作区 `data/dev/scripts-go/notice-对拍.txt`。
  - CI 失败提醒（`internal/devtools/notice/ci.go`）：每次钩子运行，用 `gh run list` 查 origin（只认 github.com，`git remote get-url origin` 推出 owner/name，不写死）最近 48 小时里分支是 main、事件是 push 或 schedule、结论是 failure 的运行，再对没提醒过的最多 5 条（最新的在前，其余下一条消息再列）并行调 `gh run view --json jobs`，每条一行：工作流名、事件、北京时间、短提交号、哪个 job 的哪一步红、运行链接（用运行编号自己拼）。不下载日志。开头说明这是 CI 结果、资料不是主人的指示、先核对。同一会话每个运行编号只提醒一次，记在同一个记录文件该会话名下（`ci` 字段）。信任边界：PR 的分支名、标题是外人可写的，不取不看；进上下文的名字都清掉控制字符、截到 60 字符。列表和详情各有各的时限（列表 5 秒、详情 7 秒，详情并行问），没装、没登录、离线、超时、没有 origin 都只往 stderr 写一行、不打这一段、不记“已提醒”（下一条消息再试）；列表按 `--event push`、`--event schedule` 各查一次再合并（`--branch main` 匹配的是 headBranch，fork 里叫 main 的 PR 运行会占名额；两次里有一次失败就整个当失败）；报告目录不存在时不查（没地方记已提醒）；定时任务会话、没有会话号同样不查。记录文件和缓存文件的读、读改写都在报告目录的 `.提醒.lock` 里（用 `selfbuild` 的系统文件锁，LockFileEx / flock），查 CI 的那几秒不拿锁、写之前才重读；读失败但不是“文件不存在”时不写（不会当空的去覆盖别的会话的记录）；改名覆盖被占用（Windows 的 Access is denied）时重试几次；取不到锁就这次报错、不打、不硬写。`--no-ci` 只看报告。实测（2026-11-19，本机 gh 2.103）：`gh run list` 约 1.7～2.9 秒、`gh run view` 约 2.4～2.7 秒，不缓存时每条消息多约 2 秒、有新失败时 5～6 秒。
  - 缓存（`internal/devtools/notice/cicache.go`）：查到的失败运行（含每条的 job 详情，最新 10 条）按仓库存在报告目录的 `.ci缓存.json`（带查询时间 `at`），各会话共用，5 分钟内的钩子直接用、不调 gh，过期才真查；按会话去重不变，仍在 `.已提醒.json` 记运行编号，用缓存时照样再核一遍 main / push、schedule / failure / 48 小时。写法同记录文件（先写临时文件再改名），读坏了、格式不对、时间解析不了或在将来都当没有缓存。gh 失败（超时、没登录等）不写成空列表的成功缓存，只记 `failAt`，1 分钟内不再重试（也不再往 stderr 说），过了再试。代价：缓存期内新出的失败最多晚 5 分钟看到。job 详情没取到的运行叫“待补”：这一条先不打、不记成已提醒，缓存只给 30 秒，下一次真查只补这几条的详情（已有的沿用），连续失败 3 次才写“job 详情没取到”放行（没开缓存时失败一次就放行）。缓存按仓库存，同样在 `.提醒.lock` 里读写。实测（2026-11-19，真 gh，临时报告目录）：冷缓存 4 轮 5.1～5.8 秒、4 条运行的详情都齐；5 分钟内第二、三次各 0.2 秒；同会话再来 0.3 秒、不再打。`cicache.go` 里起的 goroutine 都带 recover，panic 当作这次查询失败（不写成功缓存、记失败时间）。

- 编：`cd native && go build -ldflags "-X sumika-native/internal/devtools/selfbuild.BuiltDigest=$(...)" -o bin/sumika-dev.exe ./cmd/sumika-dev`。第一次不用管摘要：直接 `go build -o bin/sumika-dev.exe ./cmd/sumika-dev`，运行时发现摘要对不上会自己重编成带摘要的版本。`native/bin/` 被 git 忽略，每个 worktree 各一份。
- 自我重编：启动时算 `cmd/sumika-dev/`、`internal/devtools/` 和它许可 import 的 `internal/jsjson/`、`internal/netsafe/`（固定列表，`selfbuild.SourceDirs`；`go list -deps` 现算要 1 秒，钩子付不起，archcheck 保证传递依赖不超出）下所有 `.go`（按相对路径排序，CRLF 先归一成 LF）加 `go.mod`、`go.sum`、编译用的那套 Go 的版本（读 `GOROOT/VERSION`）、`GOWORK` 和 `GOTOOLCHAIN` 的值的 SHA-256；这些目录里不许用 go:embed（archcheck 查），和编进去的值比；对不上就编到同目录的 `.new`，成功后旧的改名成 `.old`、新的换上（Windows 上运行中的 exe 能改名不能覆盖，`.old` 下次启动时清掉），再带原参数重新执行并原样传回退出码。编不过打印中文原因、退出码非零，不放行。
- 重编锁：`native/bin/sumika-dev.exe.lock` 常驻、从不删除，用操作系统的文件锁（Windows `LockFileEx`，别的系统 `flock`，非阻塞每 50 毫秒试一次，上限 = 编译超时加余量，`selfbuild/lock*.go`）；锁的生命期就是打开它的文件句柄，持锁进程被杀、崩溃，系统自动放锁，不判断“老锁”“死锁”。锁里写的 pid 只在超时提示里说“谁占着”，不参与判断。以前“创建锁文件加 pid 加认领清老锁”的做法有认领到活锁再放回的空窗，复核实测两个进程同时持锁（160 次里红 16 次、再改一版仍 7 次），已整套换掉。测试用测试二进制自己当子进程，4 个进程各取锁 40 次做进入 / 退出计数，被杀的持锁进程马上放锁，超时返回中文原因。
- 找模块根：从程序所在位置往上找 `go.mod` 或 `native/go.mod`；环境变量 `SUMIKA_DEV_ROOT` 可指定（`native/` 或仓库根）。找不到就不检查，照现有版本跑。`SUMIKA_DEV_NO_REBUILD=1` 跳过检查，给测试用。重编用的 go 不按名字去 PATH 里找（archcheck 的外部程序规则），取编出本程序的那套 `GOROOT` 里的 `bin/go`；Go 升级搬家后找不到时，设 `SUMIKA_DEV_GO` 为 go 的绝对路径，或手动 `go build` 一次。
- 边界：`internal/devtools/...` 和 `cmd/sumika-dev` 不许 import 产品包，只许标准库、`golang.org/x/...`、`internal/jsjson`、`internal/netsafe` 和白名单里登记过的第三方库，查的是传递依赖（含测试文件），标准库按 `go list` 的 Standard 字段认（archcheck 的 `TestDevtoolsImportAllowlist`）。第三方库现在有两组：`modernc.org/sqlite`（连同它的传递依赖 `modernc.org/libc`、`mathutil`、`memory`，`dustin/go-humanize`、`mattn/go-isatty`、`ncruces/go-strftime`、`remyoudompheng/bigfft`）和 `goccy/go-yaml`，都是隐私扫描读名单用的，产品里本来就在用；要加新的先去 `devtools_test.go` 登记并写理由。第三方库的版本变化由 `go.mod`、`go.sum` 覆盖（摘要含这两个文件），不用进摘要目录。

来源：[main.go](../../native/cmd/sumika-dev/main.go)、[selfbuild.go](../../native/internal/devtools/selfbuild/selfbuild.go)。


### sumika-proto

全占位的 mygo 交互演练入口：`go run ./cmd/sumika-proto`。窗口、本地发送、流式回复、场景脚本和外观状态全部在这个 main 包内；只依赖标准库与 mygo，不导入生产后台，不发现数据目录，不调用模型或 QQ。启动及打包命令见 [native/README](../../native/README.md#独立交互原型)，范围见交互原型规格（私有存档）。

`engine.go` 持有会话、消息、草稿及按时间／序号排序的内存事件队列，`script.go` 严格解码JSON并核对显式目标；`view.go` 绑定会话草稿，持有选中会话、ListState和布局状态，`scenes.go` 向同一个引擎发操作。发送先进入sending，确认后启动过滤目标会话的脚本；明确失败在原ID重试，停止保留部分正文，重置递增runId并清事件。假读搜折为资料栏一行；第一步不含命令／写入或审批。

场景入口在宽窗导航底部、窄窗当前层标题栏各保留唯一图标，以窗口内浮层打开；原底部工具条已移除。透明无框外壳28 DIP，导航四边12／内角16，布局常量统一在 `tokens.go`，逐层骨架见规格十一节。消息分组只依赖相邻Actor，跨Turn不断组；调用mygo四参数Radius绘制靠头像侧的相贴角6，其余角按单／多行高度推导。失败由左下24 DIP命中区角标重试同ID，语义名“重试发送”，悬停给一句提示。自己的消息无身份头，他人群消息、会话、顶栏和成员共用32 DIP首字头像及角色实底；未读数有胶囊底，导航／会话／聊天用三档中性底。标题、消息列与输入框左线及顶部文字基线有几何断言。活动零值为空态，空白／pending发送仍禁用。几何检查读取Tester实际树并记录每个父子关系和相邻间距，不再从surface登记读取豁免字符串；3条布局例外集中在测试常量名单，均待批。

键盘Enter可能先于编辑器绑定发布文本；原型先用TextKey同步草稿，将普通发送的会话ID和文字快照留到下一次构建受理，再排180ms假确认。这样低频绘制时不会在确认清空后又写回旧草稿。回复中的排队发送仍当场通过TextKey清空，失败仍保留原草稿；按钮发送沿用绑定完成后的OnClick。

普通构建不加载 automation；`e2e.go` 仅在 `-tags e2e` 且非浏览器时导入插件。`go tool mygo` 使用 native/go.mod 固定的 v0.7.4 CLI，`vet ./cmd/sumika-proto` 的普通 vet 和生命周期分析均限制在原型目录。


### core-check

**读取真实配置和 Bun 库；-call 会花模型费用。** 查旧库表结构和计数，可试调一次模型。

```sh
go run ./cmd/core-check
```

参数：`-root` 空，向上找项目；`-call false`；`-model` 空，按角色 ID 排序取首个已配模型，再取 `defaults.backgroundModel`。
按配置选数据目录，只读 `shared/events.db`、`shared/trace.db`，不写库或配置；计数、哈希及可选调用结果打印到终端。
来源：[main.go](../../native/cmd/core-check/main.go)；规格：[Go 后台四点五](../规格-Go后台.md)。

### turn-check

**读真实配置、花模型费用。** 跑本地私聊再原样重发请求，查缓存键和前缀命中。

```sh
go run ./cmd/turn-check -model "<模型ID>"
```

参数：`-model` 空，按角色 ID 排序取首个已配模型；`-root` 空，默认读当前项目配置，提示词仍用这份代码的。
不读真实聊天库；系统临时目录里建卡、历史、Go 库和工作目录，跑完删除。经配置中的 Magpie 调模型，两次结果和缓存用量打印到终端。
来源：[main.go](../../native/cmd/turn-check/main.go)；规格：[Go 后台四点六](../规格-Go后台.md)。

### harness-check

**花模型费用，审批一律放行。** 用真实装配（`app.Open`）在你给的临时数据目录里跑一轮本地私聊，逐项试项目说明、技能、子代理、网页、MCP、读文件、跑命令、客户端自知通不通。

```sh
go run ./cmd/harness-check -data "<临时数据目录>" -char "<角色ID>" -msg "<发给角色的话>"
```

参数：`-data` 必填，目录名叫 `data` 且路径里没有 `temp` 或 `scratchpad` 的一律拒绝；`-root` 空，向上找项目；`-char tester`；`-account 10001`（配置里占位的 QQ 小号，不会真连）；`-timeout 5m`；`-answer N`（她用 `ask_owner` 提问时替主人点第 N 个选项，从 1 数，0 不点；只给试验用）；环境变量 `HC_PREVIEW` 控制每步结果显示多少字，默认 300。
数据目录要自备 `config/sumika.yml`（把 `tools.workdir` 指到临时目录）、`characters/<id>/card/` 和 `sumika-data.json`；要试 MCP 就放一份 `config/mcp.json`。每次从空库开始就先删掉 `go/`。
跑完把回复、这一轮的工具步骤（名字、参数、结果开头）打到终端；步骤从 trace 库只读取。
来源：[main.go](../../native/cmd/harness-check/main.go)。

## 素材

### logotool

**写文件，不碰数据和配置。** Sumika 的 logo 素材工具：Edge 把 SVG 渲染成指定边长的 PNG、和原图逐像素对拍、PNG 打成只含图标的 `.syso`。

```sh
go run ./cmd/logotool render -svg ../docs/assets/logo/icon.svg -size 256 -out assets/icon/icon-256.png
go run ./cmd/logotool syso -arch amd64 -out rsrc_windows_amd64.syso assets/icon/icon-16.png assets/icon/icon-256.png
```

子命令：`render -svg -size -out [-edge <msedge.exe>]`（无头 Edge，最多重试三次，工作目录在输出文件旁边、按 PID 收 Edge）；`measure -ref -cand -out <目录>`（写 `metrics.json`、差异图）；`syso -out [-arch amd64|arm64|386] <png>...`（PNG 要正方形、边长 1～256，不带版本信息和清单）。
重新导出整套的步骤和对拍数字在 [docs/assets/logo/README.md](../assets/logo/README.md)；`go test ./cmd/logotool` 会查提交的 `.syso` 是不是正好由 `assets/icon` 里的 PNG 打出来的。
来源：[main.go](../../native/cmd/logotool/main.go)；决策：ADR-230。
