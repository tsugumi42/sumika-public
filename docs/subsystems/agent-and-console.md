# 回合循环与主窗口

开发规则见 [开发指导/回合内核](../开发指导/回合内核.md)、[开发指导/界面与交互](../开发指导/界面与交互.md)。

> 2026-11-08，codex/memory-quality 第二轮：ToolCall.InputSeqs 由内核按每次模型请求冻结当前输入与已送达插话；prepareCall 给每个工具独立副本。来源绑定只用于核验，不扩大工具权限；细节见 [记忆质量规格](../规格-记忆质量.md)。

## Go 检查点动作接口（feat/checkpoint-lsp，未合 main）

[`api.CheckpointActions`](../../native/internal/api/checkpoint.go) 的 List/Preview/Restore 由 `app.DesktopReader` 提供，复用 LocalChat 的本地私聊 key 校验及桌面入口生命周期。恢复先验证 ID/mode/version，再通过现有审批卡请求 once/deny；批准后重新检查会话屏障、当前工具授权和文件内容。拒绝、取消、错误和部分恢复均写同一工具步骤、Invocation 和 ResultStore，返回逆检查点及已完成路径。archive、本地群聊、QQ 私聊和 QQ 群无此权限。本轮只交后台接口，专用列表及变化面板未实现；[规格](../规格-检查点与回退.md)定义后续界面契约。

悄悄话（[`internal/whisper`](../../native/internal/whisper/)、[`internal/mention`](../../native/internal/mention/)，装配在 `internal/app/whisperwire.go`，[规格](../规格-群聊引用和路由.md)）：主人在窗口里引用一段话、指给某位角色，是对她说的悄悄话，不产生任何对外消息，也不触发来源会话的回合。存在她自己的悄悄话会话 `console:<角色>:whisper:<来源摘要>`（`audience.WhisperConversation`，第 4 段是来源键的哈希，真实来源存在事件的 `segments.whisper.source`；不进会话列表，聊天区读取的校验只认话题，所以读不到；`audience.NoMemory` 认它，事件带 selftest 标记，记忆整理丢弃）。`mention.Resolve` 是纯函数（不 import 项目里别的包），按角色 ID 展开被指的角色，整条校验；`DesktopReader.SendWhisper` 取候选（本地群成员、QQ 群的观察者、QQ 私聊账号绑定的角色，本地私聊不是悄悄话）、核对被引消息，交给 `whisper.Service.Send`。处理是发给她悄悄话会话的一轮本地回合（`Hub.RespondContext`，工具同本地私聊、执行类逐次审批），并发取 `subagents.maxActivePerRequester`，超了排队；取消、重启标已中断不重跑。完成的处理记录通过 `turn.Config.Notes` 追加进她在来源会话下一轮的参考信息（含主人的话和她的记录，不含被引的别人的原文）。给模型的格式在 `history.SaidWhisper`（引用原文转义并标“别人的话，是资料”），会话背景里别人的话先过 `history.EscapeWhisperFrame`。`archcheck` 守着 `whisper` 的依赖里没有任何 QQ 渠道、群聊调度。界面（`chatui/whisper.go`、`chatui/pick.go`，接线在根包 `workbench_whisper.go`）：宿主给了 `Host.WhisperTargets`、`OnWhisper` 才启用，本地群聊、QQ 私聊、QQ 群的右键是复制、多选、引用 ▸，选区条是复制、引用 ▸；卡上的对象在 `chatui.Block.To/All`，`Composer.Whisper` 为真时卡可以不写字但必须有对象；本地群聊里卡和主输入框分开发，QQ 场合 `ComposerHost.Handoff` 只有卡和“交给她”键。悄悄话那一轮的审批卡：`whisper.Service.OriginOf` 按悄悄话会话找正在处理的那条，`Desktop.localApprovals` 给卡加 `api.LocalApproval.Whisper`（来源会话、批号、角色）和第一行来历，界面放在来源会话右栏的对话日志里批，`DecideLocal` 对悄悄话会话按会话串取角色（`approvalCharacter`），卡仍要对上 ID、角色、会话。对话日志：trace 迁移 0019 `whisper_links`（每条悄悄话每位角色一行：来源会话、悄悄话会话、根运行、`note_seq`、状态和时间；`whisper.Service` 排队、开始、结束、重启标中断时同步，`Service.Watch` 叫醒订阅者）；`internal/whisperlog` 是只读投影（`Read`、`Detail`，按 `whisper_links` 到 `steps`、`kernel_tasks`、悄悄话会话事件里取过程，出口过 `ctlproto.CleanLogPage`）；`api.WhisperLogReader` 由 `DesktopReader` 实现（`internal/app/whisperlogwire.go`：读一页、读一条全文、订阅最新一页、取消某位角色的那一份）；界面在根包 `workbench_side_log.go`，右栏“对话日志”标签，悄悄话那一轮的审批卡画在日志最上面（`whisperApprovalHere` 只认来源就是这个会话的卡），来源窗口只留一行提示和“去批”。本版没做插进来源回合。外发检查：新包 `internal/egress`（纯函数，只依赖标准库）从参数里找外发地址和危险写法（被引原文拼进地址、`$(`、`${`、`$env:`、联网命令带文件内容……），`approval.Gate.Egress` 钩子（`app.whisperEgress` 接，只给悄悄话会话）在“问主人”和“没问就放行”两条路径上写 `Request.Egress`；卡上（`api.LocalApproval.Egress`，`chatui.ApprovalInfo.Egress`）写“会往外发 N 处地址 · 编号 E-XXXXXX”，危险的排前面整行标红；编号和几处、几处危险写进 `approval_json`（`turn.egressJSON`，不含地址），`whisperlog` 读出来显示在日志的工具条目上（`ctlproto.LogEntry.Egress`）。只标不拦。sumika-ctl：`whisper create --selftest|status|cancel|log|list`。

向主人提问（`ask_owner`，[`internal/askowner`](../../native/internal/askowner/)，装配在 `internal/app/askwire.go`，[规格](../规格-向主人提问.md)）：只在主人本地私聊（含话题、自检）的前台回合可见，本地群聊、线、QQ、子任务看不见，执行时按宿主绑定的身份再核。问题问出口就是她的一条消息，点选项是主人的一条；打字回答走 `SubmitLocal` 的插话并结束等待；回答只是文字，包不引审批包，答完要动手照常弹审批。目标、定期任务、自检里不等、不挂卡片，问题带选项留在聊天里（目标和定期任务另提醒主人）。重启不恢复等待。桌面接口 `api.LocalQuestions`（`WatchLocalQuestions`、`AnswerLocal`），界面是 `chatui.QuestionCard`（新界面输入区上方，旧界面同卡，打字回答只在新界面）。

`sumika-ctl checkpoint list/preview/restore/status/cancel` 经可选控制端口调用相同桌面动作；管道认证不批准文件恢复。进度订阅提供阶段及累计完成数，取消等待恢复 I/O 真实退出；逆 ID 可显式 recovery 撤回或 resume 续做，仍须重新预览和单次审批。客户端只有协议投影，不链接后台；详细命令见 [Go 命令行工具](go-tools.md#sumika-ctlagent-怎么操作开着的程序)。



## Go 桌面外观

桌面入口的 [根视图](../../native/ui.go) 每帧最先应用 MewTypeUI 缓存主题。正式与演示设置共用 [外观编辑页](../../native/appearance.go)；深浅随系统或手动选择，颜色、圆角、字号与表面材质立即生效。宿主 [themefile.go](../../native/themefile.go) 在后台使用原生文件对话框读写 JSON、按修订顺序保存，并在退出前保存最后一份。

[desksettings.Appearance](../../native/internal/desksettings/appearance.go) 把亮/夜两份 MewTypeUI definition 和模式存入桌面设置，不修改后台 `sumika.yml`；读取失败返回默认并提示。默认、导入导出、材质降级与验收见 桌面主题规格（私有存档）、[native 使用说明](../../native/README.md)。下文的 HTTP 设置接口属于已退役的 Bun 版。

聊天外壳用 MewTypeUI `layout.Window/Pane`、`dialogs.Rail` 和 `chat.Header`。几何和窄窗切页由库管理；Sumika 保留本地/QQ/记忆/运行/设置路由、历史加载与第三栏动作。`native/panes.go` 仅管偏好、防抖及内容接线，`titlebar.go` 提供统一标题内容；旧 Split、覆盖式右侧栏和 52 DIP 标题行已移除。`desksettings.Panes` 在读取边界单向转换旧设置，保存比例和第三栏偏好；详见聊天界面规格（私有存档）。

聊天外围文字读取 MewTypeUI 字阶与语义色；右栏标签换为 `control.Tabs` 的稳定 ID，原页内容和归属不变。`titlebar.go` 独立嵌入当前外壳使用的十枚 Lucide SVG（许可随 assets/nav 保留），启动时解析一次。链接复制、图片打开/关闭调用 [feedback.go](../../native/feedback.go)，由 `sound.Dispatch` 接准备好的 mygo 音频资源；保持原音高、时长及 50% 增益，资源首次播放生成一次，测试进程不打开音频设备。Viewer 翻页仍属待迁路径。

会话栏由 [convlist.go](../../native/convlist.go) 的 MewTypeUI `dialogs.Model/List/Search` 管：宽行与头像窄栏共用虚拟列表，完整 ConversationKey 作为 ID。本地空私聊保持入口；搜索词分别属于本地/QQ。概览更新、建群成功和头像完成替换快照，普通构建复用模型，只请求可见行头像。头像缓存寿命跟随数据源，换 Reader 时一并重置模型；消费者回归与未通过的性能指标见聊天界面规格。

单栏列表的控件选择不等于当前聊天：返回列表时不显示聊天高亮，所以再次单击当前会话仍能进入；聊天身份保留，放宽窗口后恢复当前会话高亮。

消息由 [row.go](../../native/row.go)、[bubble.go](../../native/bubble.go) 的 MewTypeUI `chat.Row/Bubble` 承载；引用、Markdown 和回应使用 `content.Quote/Markdown/Reactions`。分组按 actor 与时间，私聊不加名字行，群聊主人名字用强调色；头像缺图/加载/失败共用库内首字，已连接的虚拟行按视口夹取组尾头像。时间逐条放入库的信息区，正文复制排除时间占位并还原小黄脸。Reveal 按主题时长展开完整行高，实时水位线与状态跟消息身份存活；Markdown 展开状态在行重挂、版本更新后保留。转发节点复用相同内容入口，引用保持只读预览。

消息段直接使用 `api.DisplaySegment.Kind` 与展示字段；[data.go](../../native/data.go) 的 `seg` 只内嵌 DTO、段位置和下载键，不再把 `voice/emoji/superface` 翻译成另一套 UI 种类。宿主转换为不含媒体凭据的 `segview.Segment`，正文、引用预览和表情复制共用原始种类。演示旧 JSON 只在 `demo_reader.go` 的 `demoSeg` 解码后转换，不能进入正式渲染分支。

行内 `Run/FaceRef/LinkRef`、段与块、链接拆分、文本清理、表情回退和完整排版由 [segview](../../native/internal/segview/blocks.go) 共用（新界面主线 S2）。后台计算唯一的 `msg.blocks`，绘制只读；媒体、下载和转发凭据留在根包，按块内 Position 回取同版本的原段。新视图接线见 [新界面主线规格](../规格-新界面主线.md)。61 条旧实现 golden 在类型切换后仍逐字节一致，未重录基线。

普通正文、行内小黄脸和链接由 [body.go](../../native/body.go) 接入 MewTypeUI `chat.RetainedBody`：同一份 `TextLayout` 用于字形、对象框、链接和选区，不在取页或绘制阶段另排正文。窗口内按数据源代次、会话、转发层、消息、壳和段隔离状态；每次构建结束释放虚拟列表不再构建的段，包括切页、切会话和关闭转发。`BodySelection` 的 `CopyData` 保留原段 ID 和 rune 范围，还原小黄脸并排除时间尾槽；同段的双向文字碎片直接连接，不同段空一行。正文语义文字和链接并列，链接每个折行框有独立 Tooltip 身份。Markdown 保留 `content.MarkdownState` 的组件路径。

链接卡由 [card.go](../../native/card.go) 组合 MewTypeUI `content.LinkPreview/Photo`，封面到达前后保持几何；原链接仍由宿主校验后打开。[picture.go](../../native/picture.go) 使用 `Photo/GIF/Sticker/Album`，相册每十项分组，各项 StatusText 与单图/GIF/贴纸共用 Media 模型，网络错误逐项重试、坏格式单独说明。库已提供逐格下载/取消与尺寸通知，完整宿主接入另排。动贴纸请求最多 256 像素以限制上传量，逻辑尺寸仍由主题决定；没有修改 images.go 的读取、解码或缓存层。

[video.go](../../native/video.go)、[file.go](../../native/file.go) 使用 `content.Video/File`，视频的进度及文案来自 `Media.Progress/StatusText`，`VideoOptions.ShowPlay=true` 保留未知时长的播放标记；通过 [media_download.go](../../native/media_download.go) 共用原 `DisplaySegment.Download` 的 `WatchVideoDownload/RequestVideoDownload/CancelVideoDownload`。未知总量不显示假百分比；完成后重读消息或当前转发层取得 FileKind/Media，图片和视频文件进入对应组件。来源、转发路径、节点和段位置共同隔离状态，切会话/换源/关闭转发取消订阅并丢弃迟到结果；订阅结束不取消后台下载。

合并转发由 [forward.go](../../native/forward.go) 的 `content.ForwardSummary` 展示标题、三行摘要、条数/缺失数、读取和失败状态，并由 `overlay.Box.FullBody` 承载节点虚拟列表；返回栈、读取取消和迟到回调归宿主，退出期间保留内容/媒体缓存，逐帧安排最终清场。根视图用 `overlay.Native` 绑定真实窗口；配置来源、正文链接和贴纸说明由 `overlay.Tooltip` 显示原生提示。库已提供 Viewer 固有尺寸/双击/保护和标准编辑菜单桥；宿主 Viewer 仍待窗口级迁移，现行正文菜单通过 `BodySelection.EditItems` 完成复制/全选，具体整合边界与验收见聊天界面规格。

本地输入由 [localchat.go](../../native/localchat.go) 组合 MewTypeUI `compose.Field/ActionButton/StopGeneration`，发送与停止复用同一动作槽；附件、表情选择尚未接入，明确禁用。草稿/错误/busy 归会话，生成期间字段只读、停止在途禁用。私聊审批改用 `control.ApprovalCard`，详情归请求 ID，明确允许一次/拒绝，失败显示原错误并允许再次提交。思考和工具过程未有桌面读契约，不能把正文草稿当思考；范围和库缺口见聊天界面规格第 5 步。

本地私聊和群聊停止确认后立即显示“已停止”，迟到正文不重新打开草稿；已派发工具仍可在宽限内完成，过程行继续接收真实结果。界面保留会话 busy，私聊订阅直到 `Drained`，群聊和旧输入等待发送 worker 实际退出后才允许下一轮。停止在途或宽限期间不能重复发送。

## Go 本地链接资料（2026-11-03）

本地私聊（日常、已有所属话题）由 `Hub.SendLocal` 入库；已送达的本地插话也由 Hub 入库。`turn.Config.OnLocalStored` 在提交后、拼上下文前同步通知装配处，只提链排队；本地群由 `localgroup.Service.OnStored` 在成员观察记录提交后通知同一入口。`app.observeLocalLinks` 交给 `linkplugin.ObserveLocal`，保留真实的 `console:<角色>[:topic:<编号>]` / `console:group:<编号>` 键，不转成 QQ 的私聊格式。

四种场合共用 linkplugin 的有界工人队列、缓存和回合钩子：上下文最多等现有 2 秒，超时结果在下一次输入追加，不额外唤醒回话。摘要与 `read_link_detail` 共用 `store.LinkNotes` 的当前会话＋角色 observations 查询，本地群观察记录只来自真实成员。context 开关关闭时既不排队取数，也不挂摘要和详情工具；archive 没有写入口。QQ 准入、档位和展开规则沿用原流水线。

本地事件没有 QQ 的 `segs/from` 信封，链接读投影同时取事件原文和 actor_kind，用原文计算未处理链接数量、用可信 owner 身份补发言人；不改原消息。规格与四场合边界表见[链接解析](../规格-链接解析.md#11-本地接线的形状--入口local-links2026-11-03)，本地功能缺口表见[本地群聊第十节](../规格-本地群聊.md#十本地两个场合的规格盘点local-links2026-11-03)。

## Go 本地群聊（2026-11-03）

`native/internal/localgroup` 管真实本地群，不经过 QQ 闸门。`api.LocalGroupWriter` 是 Reader 的可选写入口，desktopread 只在可写后台装配后转交；archive 返回不支持。界面的 `native/localgroup.go` 用 CrychicUI Modal、TextField 和 Check 建群，空群按真实创建时间出现在群聊组；`native/localchat.go` 与本地私聊共用 MewTypeUI 输入区和按会话保存的输入/运行状态，群发送不填 Mentions，后台解析正文 @名字。

| 入口 / 数据 | 职责 |
|---|---|
| `CreateLocalGroup(ctx, LocalGroupCreate)` | 群名和 2–10 个可读角色的 ID，事务写组和有序成员，提交后发布概览变化 |
| `SendLocalGroup(ctx, LocalGroupSend)` | 主人输入先落库，@ 优先/持久化轮转，串行调用已有 `Hub.RespondContext`，整条主人消息最多两位角色 |
| `StopLocalGroup(ctx, ConversationKey)` | 取消这一群的整轮并等收尾，包括排队但未进入 active 的内核回合 |
| `local_groups` | `id`、`name`、`created_at`、`next_position` |
| `local_group_members` | `group_id`、`character_id`、`position`、`auto_count`、`last_auto_at`；只在 local 平台库 |

会话固定为 `console:group:<id>`，展示读取按成员表核对资格。角色回复写进同一个会话，每位成员都记 observations 与新事件通知；历史里其他角色的文字带说话人 ID，自己的回复保留 assistant 身份。群听众是 console × group、PrivateOK=true、没有外人，场合段为 `scene-home-group`。沿用 `memory.write` 的 `local-group` 开关；同角色记忆在本地私聊、本地群聊、QQ 私聊、QQ 群都能想起，主人显式共用池以外仍隔离角色（ADR-242、ADR-162，见[记忆召回规格](../规格-记忆召回不分场合.md)）。

群发送等整轮结束才解锁输入；期间显示“正在回复”且可停止。输入和最终回复由既有消息订阅展示，失败照实显示并保留已入库内容。切走再切回保留每群草稿、busy 和错误；数据源更换取消旧请求并拒绝迟到投递。群不绘制审批卡；工具清单、插件执行前检查及审批转换共用私聊执行边界。archive 提示启动后台，Reader 无 `LocalGroupWriter` 时不显示群输入框；QQ 私聊、QQ 群只看记录。

压缩快照按 `character_id + conversation` 读取；`history.For`、首次构建、实例重建和提前压缩的版本检查都走同一入口。没有本角色的成功快照就读原文，不借用同群其他角色的摘要；本地私聊、本地群聊、QQ 私聊、QQ 群均如此。

没有无人触发的自动模式。自动续接最多一层，每位一次、5 秒冷却；主人无 @ 重置计数，有 @ 只重置被点到的角色。同群同时一个主人轮次，重复提交报忙。关闭先拒收、取消并等待全部调用，再关闭读服务和数据库。参数、外部参考与验收见[本地群聊规格](../规格-本地群聊.md)。

下面各节保留 Bun 历史实现，已随 ADR-203 退役；Go 本地群以本节和源码为准。

## 职责与边界

- agent.ts（私有存档）（`ctx.agent`，`AgentHub`）：每个"角色 × 会话"一个 pi `Agent` 实例；跑一轮、把回复写回事件库、记一条模型调用追踪，回合事件经 sink 交给调用方。不存对方的话（平台先存好）。`interrupt(角色, 会话)`：她正在说就停下（主窗口的会话一并取消待审批；QQ 来了新消息用它）。`steer(角色, 会话, 原话)`：主窗口插话，她不停、这一步做完下一步开头看到，送到时才存进记录；不在跑返回 null。`stop(角色, 会话)`：停下她，取消待审批、丢掉没送到的插话，下一轮参考信息里提醒一次（带她说出口的半句）。见[规格-排队插话停止](../规格-排队插话停止.md)。
- router.ts（私有存档）（`ctx.router`）：收到的消息（`message/inbound`）交给哪个角色回、用什么可见范围和场合提示词；回合事件转发成 `turn/event`；整轮生成完逐条走 `message/outbound`。见下面"路由"。
- console.ts（私有存档）：主窗口后端（REST + WebSocket + 打包后的静态页面），也是一个平台：主人的话由 `consoleSend` 存好后发 `message/inbound`，回合事件从 `turn/event` 认领 `console` 渠道的推给网页。
- topics.ts（私有存档）（`ctx.topics`）：本地分话题（规格-本地话题与压缩第七节、ADR-073）。每个角色一条闲聊 `console:<角色>` 加若干话题 `console:<角色>:topic:<编号>`，各是一个会话、各有自己的实例和上下文；话题表 `local_topics` 在 events.db。分叉（`fork`，ADR-074 第 2 条）：把一个会话现在"给她看的那份"（`AgentHub.contextEvents`：快照 + 之后的原文）复制成新话题——原文一条条复制、保留原来的时间、不记 observations（记忆整理和找原话不会算两遍），有快照就用 `copyEpoch` 抄一份（trigger `fork`）；两边各留一行通知（`fork` / `forked`），新话题起标题只看分出来以后的（`local_topics.forked_seq`）。话题第一轮她回完以后用后台模型起 12 个字以内的标题（主人改过的不再改），归档时整理一次这个话题的记忆；`owns(角色, 会话)` 管主窗口的哪些接口、哪条 WebSocket 消息认这个会话。
- settings.ts（私有存档）：设置页能改的那几项配置（`GET / PUT /api/settings`），写回 `sumika.yml`。见下面"设置页的配置"。
- maintenance.ts（私有存档）：每天的后台维护，只有两个接口挂在这一页的表里（实现见[运行时](runtime.md)）。
- web/（私有存档）：主窗口前端（React + Vite，Markdown 用 Streamdown）。

## 运行路径（主窗口私聊一回合）

1. 前端经 WebSocket 发 `{type:'send', characterId, conversation?, text}`（`conversation` 是闲聊或她的话题，不给就是闲聊；不是她的会话就回一个 `bad_conversation` 错误；在归档了的话题里说话会把它拿回来）。话题用这个角色的 `topic` 模型（配置 `characters.<角色>`，ADR-084），没写就用 `model`。
2. `consoleSend`（主窗口作为平台的"收"）：`agent.interrupt` 停下她正在说的（被打断的半句在历史里排在新消息前面）→ 写主人事件（来源 `owner`）→ `message/inbound`（`local: true`，带工作目录）→ 路由选场合提示词、写好"对面是谁"，调 `AgentHub.respond`：检查角色和模型 → 取或建实例（固定前缀：`core` → 角色卡的身份 / 行为 / 表达三段 → 第四段「私下」（外貌、对主人的亲近；卡里有这一块才有，主窗口和私聊拼，群聊不拼，见下面"角色卡的四块"）→ 场合 `scene-*` → 平台 `platform-*`，规格-语言 3.3）→ `buildContext` 跑回合事件链第 3 站 `turn/context`（记忆挂在这一站，见 [memory.md](memory.md)；agent 自己只加报时和开头那行，只追加）→ `agent.prompt([参考信息?, 这句话, 提醒?])`（`RespondOptions.trailing`，只有群聊的一轮会给，主窗口没有，见下面"路由"）→ 收尾跑第 7 站 `turn/end`。开头先跑第 2 站 `turn/should-respond`，有插件给出理由就不回；工具在建实例时由第 4 站 `turn/tools` 决定。事件链的规则见 规格-插件.md（私有存档） 第五节。
3. pi 事件转成回合事件：`start` / `delta` / `thinking` / `tool` / `event`（回复落盘）/ `end`（统计）/ `error`，经路由的 `turn/event` 回到主窗口，带上会话广播给所有连接（网页按会话分开存草稿、记录、统计）；话题列表变了推 `{type:'topics', characterId}`，网页重新读。回复再走一遍 `message/outbound`（主窗口没有挂发送，守卫照常检查）。
4. 每个助手消息结束时写一条 `model_runs`：模型、Magpie 实际落到的渠道和模型（`route_provider` / `route_model`，从响应头取）、首字、总耗时、token（未命中 `input_tokens`、命中 `cache_read_tokens`、写入 `cache_write_tokens`；输出 `output_tokens` 含思考，其中思考多少记在 `reasoning_tokens`，取 pi 的 `usage.reasoning`，后台调用也记）、属于哪一轮（`turn_id`，和 `steps.turn_id` 一样；后台调用、压缩是空的）、查记忆耗时、前缀复用、提示词段落（id、来源、字数）。`GET /api/usage/routes?days=` 按渠道汇总。
5. 一轮里的每次思考和工具调用记进 `trace.db` 的 `steps` 表（`store.stepStart` / `stepEnd` / `stepApproval`，审批也写在这里，可能比工具开始先到）；回复落盘时 `attachSteps` 把这一轮还没挂上的步骤都挂到这条回复的 `event_seq` 上。一轮可以有好几步，只有出了文字的那一步落一条回复。刷新页面后卡片靠 `GET /api/characters/:id/steps` 挂回去。

## 路由（router.ts）

不调模型、不懂具体平台，只做分发：

- **交不交给角色**（`routes`）：私聊（`scene === 'private'`），并且这条事件的 `origin` 是 `owner` 或 `peer`；她的号发出的、插件自动发的不算。群消息不在这里接，由闸门放行（`gate/enter`）以后走 `groupTurn`。
- **群里一轮**（`groupTurn`，规格-群聊闸门 10.1～10.3）：闸门带来的这一批（她上次看完以后别人说的，最多最近 50 条）逐条排成一行给她；更早的没逐条给的（`GateEnter.earlier`）在最前面加一行"（前面还有 N 条没逐条给你看，下面是最近的 M 条）"（N 比一万多时说"一万条以上"）（ADR-109 第 4 条，这句只在 `router.ts` 的 `earlierLine` 写，不进 `scene-group.md`：改提示词说话档要重跑注入测试）；这个角色在配置里开了 `characters.<角色>.groupReminder`（默认关）的话，这一批后面再追加一条单独的 user 消息（`prompts/reminder-group.md`，ADR-121；路由读文件、交给 `respond` 的 `trailing`，agent 排在 `[参考信息?, 这一批]` 后面）：一直留在实例历史里（每轮请求是上一轮的延长，缓存不断；不在轮后拿掉、不临时插入），试运行 / 不说时 `forgetHeld` 停在最后一条 user（就是这条提醒）上，所以提醒留着；不存进事件库，实例重建后历史不带回以前各轮的提醒；私聊（`reply`）和主窗口不给；`skipSeqs` 只是这 50 条，实例重建时更早的那些照常按 `recentMessages` 进历史；`audience` 是 `scene: 'group'`、说话的这几个人、`privateOk: false`；场合 `scene-group` + 平台那段；她可以只回"[不说] 为什么"。试运行（默认）时她的话存成 `automation:dry-run`、不发（存之前照平台的 `asSent` 改成开口以后的样子），并过一遍客服腔记数、记成 `dry`（ADR-110 第 2 条，`util/outbound-audit.ts`；真发的也由路由在整条发出去以后记，`router.ts` 的 `audit`，2026-10-23 起）；"说话"档（ADR-090）过了开口条件和保险丝才走 `message/outbound` 发到群里；"只记录"的群（ADR-095）闸门不放行，没有这一轮。结束后告诉闸门说没说（`turnDone`，试运行说的也算次数）和结果（`outcome`）；回合被哪一站拦了记"没进回合"。
- **要不要接**：挂在第 2 站 `turn/should-respond`，消息带 `backfill`（补录）/ `selftest`（自检）/ `stranger`（陌生人）标签的不回（`NO_REPLY_TAGS`）。QQ 那边另有 `qq.reply` 开关，见 [qq.md](qq.md)。
- **谁在听**（`audience`，决定能看哪些记忆）：主人是 `personIds: ['owner']`、`privateOk: true`；其他人是 `personIds: [他的 actor_id]`、`privateOk: false`；平台标了 `fullMemory` 的私聊带上，能看全部记忆（ADR-041）。
- **对面是谁**（`relation`）：主人一句，朋友一句（"只说可以让别人知道的事"），放进只追加的尾巴，不进固定前缀。
- **场合提示词**四选一：本地私聊 `scene-home`、本地群 `scene-home-group`、平台私聊 `scene-private`、平台群 `scene-group`；场合后面是平台给的附加段（`m.extraPrompts`：主窗口给跑命令的说明 `shell-pwsh` / `shell-powershell5`，ADR-125），再后面是平台那段（`m.platformPrompt`，QQ 是 `platform-qq`）。每次建实例时才读文件。
- **改了提示词下一轮就生效**（2026-10-22）：`acquire` 每次先重新拼固定前缀（通用规则 → 角色卡几块 → 场合 → 风格 → 平台），和实例里存的比，角色卡版本或这段文本变了就重建实例（历史照样从库里接着带）；没变就是同一个实例。以前只比角色卡版本，网页里改了 `prompts/`、选了别的场合或风格预设，要等实例被回收才生效，和界面上"下一轮就用新版"的说法对不上。
- **同一个会话的回合排队**（2026-10-22）：`respond` 先 `interrupt`（只有正在输出的那一轮停得了），再接在这个会话上一轮的尾巴后面跑（`turnTail`），同一时刻一个会话只有一轮在跑。两句话在同一刻到达时，旧代码两轮同时往下走，pi 报 "Agent is already processing a prompt"，并且后进来的那一轮把 `live.sink` 改成自己的，先跑的那一轮的回复事件送进了别人的接收端，回复存了但路由没收到、没发出去。
- **发出去**：`respond` 结束后逐条走 `message/outbound`；回复带 `aborted`（说到一半被新消息打断）的不发，发 `message/unsent`；一条没发出去，后面的也不发。出错自己接住记日志。`idle()` 等正在回的都做完（测试收尾用）。

## 状态

- 实例上限 `agent.maxLive`（默认 8），闲置 `agent.idleMinutes`（默认 45，ADR-109 第 3 条跟缓存寿命对齐；2026-10-19 以前 30）分钟释放（每分钟查一次，`sweep`）；本地私聊另有空闲压缩（ADR-388）：最后一轮结束后闲满 `agent.idleCompact.after`（默认 50 分钟）且实例至少 `agent.idleCompact.minTokens`（默认 200000）就在后台压一次（`trigger = idle`），`agent.idleCompact.enabled` 默认开；主人 QQ 私聊、群聊不做，失败 6 小时内不再试，和提前压共用任务登记和失败记录；模型流式调用另有"内容空闲"时限：`agent.firstContentSeconds`（发出后等第一段内容）、`agent.contentIdleSeconds`（两段内容之间），默认都 180 秒、0 是不管，只有正文、思考（含结构化思考）、工具参数算进展，Magpie 每 15 秒的 `: keepalive` 心跳不算，超了 `llm.IdleError`（Kind 为 `first-content` / `content-gap`）按一时的错误重试，质量统计归 `timeout`；做法照 omp 的 `streamFirstEventTimeoutMs` / `streamIdleTimeoutMs`（`llm.Client` 的注释）；释放或重建后从事件库最近 `agent.recentMessages`（默认 30）条聊天消息恢复上下文（`store.messageWindow`：从最新往前数够这么多条 `kind = 'message'` 的；通知、她没说出口的试运行和"[不说]"、这一轮单独追加的这一批不算数，也不进历史。2026-10-19 以前数的是最近 30 个事件，这几样占掉名额，群里重建时真正进历史的中位 17 条，改后 52 次重建都是 30 条，估算多约 660 token）。
- 上下文上限 `agent.context`（util/context.ts（私有存档））：超过软门槛时，这一次请求里本轮之前的长工具结果换成短占位（`transformContext`，不改保存的历史）；超过硬门槛时（话题另外不超过 `topicCompactAt`，默认 200K，ADR-074），本地的话题和闲聊在回复前现场压缩（见下一条；闲聊可以用 `autoCompactChat` 关掉），QQ 的会话、关了的、压缩没成的下一轮开始前只带最近 `keepTokens`（默认 20K）的话重建（紧急纪元），统计里 `context.reset` 报一次。都只做一次、不循环；工具调用和结果始终成对（`toolPairProblems`）。估算时不算消息列表里的系统提示那条（pi 把它也放在消息最前面，它已经在固定前缀里了；2026-10-17 以前算了两遍）。
- 压缩（规格-本地话题与压缩第八节，util/compact.ts（私有存档）、`AgentHub.compact`）：从最新往前留约 `keepTokens` 的原文，切在主人说话的那一条前面；先发 `context/before-compact`（记忆插件在这里整理到最新），再用一个临时 Agent（同一份系统提示、工具、历史、会话编号）追加 `prompts/compact-topic.md` 或 `compact-chat.md` 请它写摘要，前缀和正常对话一样、走缓存。压完自检（不能太短、没有"我是 AI"套话、提到的路径原文里有、比压之前小、降到门槛八成以下），记进 events.db 的 `context_epochs`（成了和没用上的都记），另记一条 `model_runs`（`stage = compact`）。之后重建实例 = 固定前缀 + 最新一份成了的快照（历史最前面一条，开头是 `SUMMARY_HEADER`）+ `from_seq` 起的原文；没有快照的会话照旧读最近 `recentMessages` 条。新建实例后的第一次请求在 `model_runs.rebuild` 记 `compact` / `reset` / `fresh`（预期的缓存重建；fresh 是后台重启、换人设、闲置后重建、新话题这类，2026-10-17 模型调用页用真实数据查误报时加的），每次请求记 `epoch_id`。原文一条不删。一轮结束后上下文到了硬门槛的八成，就在后台提前压好（trigger `early`），她说下一句时直接用；压好的时候她正在回复的话，这一轮照旧说完，下一轮开始前换上（`respond` 开头比实例新的快照就重建）。模型报"上下文超了"（pi-ai 的 `isContextOverflow`）时先不报错，压一次（`overflow`）再重试这一轮，重试的那次再超就照常报错，不循环（本地私聊例外：开着工作记录时一轮最多恢复 6 次，第二次起折叠本轮更早的工具步骤，没进展就停）。本地私聊开着工作记录时，重建实例会把旧 root 的工具调用和结果按预算接回聊天历史（`turn/workhistory.go`），有工具活动的会话压缩用 `compact-topic`、摘要末尾由宿主追加累计 `<work-record>`，其余场合照旧（[规格-工具历史恢复和压缩](../规格-工具历史恢复和压缩.md)）。同一个会话同时只压一次，到硬门槛时后台那次还没完就等它。`AgentHub.idle()` 等后台的压缩做完。本地闲聊另外在记忆整理完一批以后（`memory/summarized`）压一次：快照之后的原文超过 `keepTokens` + 4000 才压，切点不晚于记忆整理到的那条（压掉的都已经进了记忆，ADR-062 第 2 条），触发写 `memory`。
- 参考信息块按档存库（规格-提示词前缀稳定第七节、7.9，ADR-368，`turn/refblock.go`）：渠道回合（QQ 群、QQ 私聊）拼出的参考信息块（报时、对面是谁、记忆召回……）在回合交回时（`Hub.commit`）写进平台库的 `ref_blocks`，她这一轮没说话（不会有 Commit）的回合在回合结束时写；本地回合不存。只存声明了 `Contribution.Storable` 的贡献拼的块（目前只有记忆召回，带 `Refs`：记忆编号和 `memory.HitDigest` 指纹），不认识的插件、用户扩展、带悄悄话记录（`Config.Notes`）的块整块不存。重建（`buildWith`，冷路径和 `rebuildIn` 共用）按 `Config.RefScope`（装配处是 `memory.ScopeKey`）取同档的块，卡哈希对不上、记忆核对（`Registry.OnRefCheck`，记忆插件实现，`memory.VerifyRefs`：已删、被隔离、过期、范围或文字变了都对不上）不过、没有核对者的整块不插，读事件库失败这一轮报错；插回到第一条 seq 不小于锚点的消息前面。交回同事务清窗口起点（`history.WindowFloor`，留一个跳点的余量）之前的块；记忆写入后条目表换新时（`memory.Options.OnChanged`）删掉引用了变化条目的块（`Hub.PurgeRefBlocks`）。`Config.RefScope` 为 nil 就不存不插（cmd 下的评测、压测、对拍程序都是这样）。
- 上下文明细（`contextBreakdown`）：每次请求按实际发出去的内容拆成通用规则、人设、场合和平台、工具定义（每个工具）、历史消息（你说的 / 她说的 / 工具结果 / 之前几轮的参考信息）、本轮追加，加上窗口和留给回复的，放在 `context.breakdown` 里，存进 `model_runs.context_json`；右侧栏"本轮注入"顶上画成用量条（Inspector.tsx 的 `ContextMeter`）。都是估算，服务商报了真实输入就在下面写差多少。
- 思考档位按场合选（`agent.thinking`）：主窗口会话是 `local`，会话 id 里带 `:group:` 的是 `group`，其余 `private`；模型不支持思考时不开。设置页改了立刻生效。
- 句尾多吐的"】【"（util/clean-text.ts（私有存档） 的 `dropStrayBrackets`，2026-10-18）：Codex 线路的模型（gpt-6-luna）偶尔在句号前多吐"】【"（"标清楚】【。"）。每条助手消息结束时 agent 直接改在消息对象上，所以存进事件库的回复和留在实例历史里的都是清过的，不会被她自己学走、发到群里；压缩摘要（`AgentHub.compact`）和后台调用（`ctx.llm.background`：记忆整理、识图等，返回里带删了几处的 `brackets`）也过一遍。只删前面没有没合上的"【"、后面紧跟标点 / 换行 / 结尾的"】【"；成对的（【公告】、【1】【2】）和落单的括号不动，只数出来。agent 和 `callBackground`（util/background.ts（私有存档））会为这个写一行日志（只记次数和上下文长度，不记正文）；识图直接调 `background`，清了但不记日志。依据见 `docs/archive/notes/调研-GPT输出的】【乱码.md`。
- 句尾多吐的乱码（util/tail-junk.ts（私有存档） 的 `dropTailJunk`，2026-10-19）：Luna 有时在整段话的最后多吐私用区 / 未分配码位、零宽字符、外文字母碎片、落单的右引号、句尾顿号。`message_end` 里的先后：① 剥句尾乱码 → ② 清"】【"，清掉了东西就再剥一次（"好的<乱码>】【"）→ ③ 平台的 `asSent`（下一条；"……。<乱码>"的句号要等乱码剥掉才去得掉）；三步一轮，循环到这句话不再变为止，最多 16 轮（真实 822 条最多 2 轮；"。乱码"交替 n 对要 n+2 轮）、到了上限还在变就停并记一行 warn（去句号会连前面的空白一起去，露出前两步该清的："好的<乱码>。""收到】【 。"）；都在拆"[不说]"之前，理由跟着一起清。只看最后一个文字块（剥空了或只剩空白，接着看前一块）。存进事件库的、发出去的、留在实例历史里的都是清过的。删：私用区（除 U+F8FF）、第 4～13 平面的码位、零宽字符（旗帜的标签字符除外）、落单的右引号、正好一个的句尾顿号；紧挨着汉字的外文碎片只在 QQ 群（`live.channel === 'qq'` 并且 `sceneOf` 按会话 id 认出是群）删，QQ 私聊和主窗口只记数（会误删解释外语词的话和装饰字母；私聊里她的话几乎都是对用户说的，和主窗口一样乱码只有用户看得到）。像乱码、但会撞上正常写法的（垃圾词、英文串、"巜"、别的平面的未分配码位、符号串、和汉字隔着空格的外文词、落单的 `**`、连着两个以上的顿号）只记数不删，照原样发出去。删了或记了数时记一行日志（类别、码位、原来的结尾），这一步的 `model_runs` 连回复一起存 7 天（`response_json.tailJunk` 带清之前的全文）。整段清完是空的或只剩空白（没有工具调用的那一步）：当这一轮不说，不存、不发，轮完从实例历史里拿掉（前面某一步已经说过话的，那句也跟着从历史里拿掉，和"[不说]"一样）。压缩摘要和后台调用没接。规则、分界和量的数见 [句尾乱码规则-2026-10-19](../notes/句尾乱码规则-2026-10-19.md)（`scripts/try-tail-junk.ts` 离线量）。
- 平台发出去的样子（`RespondOptions.asSent`，平台在 `InboundMessage.asSent` 里给、路由原样转交；QQ 是 `dropTrailingPeriods`，去句尾句号，ADR-110 第 1 条）：剥过句尾乱码、清完"】【"以后，同样在消息对象上把她这条的文字换成发出去的样子（多段文字并成第一段，思考、工具调用不动），存进事件库的（真发的、试运行"她会说"的）和留在实例历史里的是同一句，重建对话前后一样；变了的话第一次改之前的那句（剥过乱码、清过"】【"的；乱码夹在句号前面的，这句里乱码还在）记进 `segments.original`。"[不说] 为什么"不改；主窗口没有 `asSent`。
- 人设改动（`persona/changed`）会丢掉该角色空闲中的实例，下一句话按新人设重建：pi 的 `systemPrompt` 是只读的。
- 角色卡的四块（ADR-123；persona.ts（私有存档） 的 `splitPersona` 读、util/prompts.ts（私有存档） 的 `personaSections` 拼）：`character.md` 认四个二级标题 `## 身份` / `## 行为` / `## 表达` / `## 私下`（`PersonaBlocks.inPrivate`），别的标题留在所在的块里；没有 `## 私下` 的老卡（三块、整篇一块）照常用，这一块是空的，系统提示里也不出现这一段（不留 0 字的空段）。「私下」写外貌、对主人的亲近这类只在私聊和主窗口用得上的内容，拼成段 `persona-private`（正文以"私下："开头），排在表达后面、场合前面。是不是群聊由 `AgentHub` 的 `isGroupTurn(conversation, audience)` 定：闸门给的 `audience.scene` 是 `group`，或会话 id 带 `:group:` 就算群（任一个说是群就算，收紧；主窗口里的群 `scene-home-group` 也是 group，也不拼），建实例时算一次；压缩时没有活着的实例、要现拼系统提示，只能按会话 id 认。同一个会话里场合不变，所以同一个实例的固定前缀不变（缓存不断）。`prompts/core.md` 开头那句"下面身份、行为、表达三节……"故意没改：群聊那份提示词要和实验量过的（confirm4）一字不差，私下这一段自己的标题就够说明它是什么。
- 角色状态 `idle` / `thinking` / `waiting`（有工具在等审批）经 `agent/status` 事件推到主窗口。同一个角色在几个会话里同时回时，都停了才算空闲。类型里还有 `error`，目前没有地方发出。

## 接口

**只给本机用**（2026-10-18，`util/local-only.ts`）：所有请求和 `/api/ws` 连接都要 Host 是本机地址（或配置里的监听地址），浏览器带来的 Origin 也得是本机；不然 HTTP 回 403、WebSocket 不接受，日志记一条"拒绝了一个不是从本机来的……"。防的是你浏览器里别的网页跨站连上来切审批模式、替你批准，以及 DNS 重绑定。

**监听地址只能是本机**（ADR-106，`config.ts` 的 `isLocalHost`、`main.ts` 启动检查）：`console.host` 不是本机地址（`127.0.0.0/8`、`::1`、`localhost`）时后台直接拒绝启动，日志和标准错误里写清为什么、怎么改（改回 `127.0.0.1`）。现在没有访问令牌，监听到局域网 / 公网上同网段的人就能连上来切审批、替她说话；要远程访问等令牌和桌面壳一起做。这道检查在写任何文件（挪令牌进密钥）之前。写 `"[::1]"` 和 `::1` 都行：判断本机和交给 listen 用同一个剥掉中括号的结果（`listenHost`，Node 的 listen 不认带中括号的写法，以前会被当成"端口被占"退出）。

HTTP 路由共 58 条，分别由 console.ts（私有存档）（37）、topics.ts（私有存档）（4）、settings.ts（私有存档）（2）、maintenance.ts（私有存档）（2）、backup.ts（私有存档）（9）、lifecycle.ts（私有存档）（1）、secrets.ts（私有存档）（3）注册；另有 1 个 WebSocket 和 1 条静态页兜底。QQ 那 22 条见 [qq.md](qq.md)。

角色与回合：

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/state` | 角色列表、模型列表（所有模型来源合起来，ADR-142）、连不上的来源、待审批、审批模式、工作目录、嵌入状态 |
| GET | `/api/characters/:id/messages?limit=&before=&conversation=` | 主窗口会话的事件（`conversation` 是闲聊或她自己的话题，不给就是闲聊，别的会话 404；下面 steps、runs、summarize 一样）；`limit` 默认 200、最多 1000；`before` 是这一条 `seq` 之前的（往上翻） |
| GET | `/api/characters/:id/steps?from=&to=&conversation=` | 这一段事件里的思考和工具调用（`steps` 表）；`from` 是读回来的第一条消息的 `seq`，不给 `to` 时连还没挂上回复的（被打断、出错的那一轮）一起给 |
| GET | `/api/characters/:id/epochs?conversation=` | 这个会话压过的每一次（成了的、没用上的），按时间正序；聊天区在 `fromSeq` 那一条前面画分割线 |
| GET | `/api/prompts` | 提示词清单（出厂的每一份：分类、标题、有没有用户版、字数、历史版本数）和用户版所在目录（ADR-133） |
| GET / PUT / DELETE | `/api/prompts/:name` | 一份提示词：读（现在用的、出厂的、变量、历史版本）/ 保存用户版（旧的先存进历史；和出厂的一样等于没改）/ 恢复出厂。名字不在出厂清单里 404 |
| GET / POST / DELETE | `/api/prompts/:name/versions/:id[/restore]` | 历史版本：看内容 / 换成现在用的（现在的先存进历史）/ 删除；版本号不对 400 |
| GET | `/api/presets` | 风格 / 场合预设清单（内置在前，带字数、被几处选着）、平台列表、现在的选法 `selection`（规格-提示词预设） |
| POST | `/api/presets/:kind` | 新建自定义预设（也是"复制为新预设"）：`{name, description?, text?}`；kind 是 `style` 或 `scene` |
| GET / PUT / DELETE | `/api/presets/:kind/:id` | 一个预设：读（正文、历史、谁选着）/ 改名字说明和正文（旧正文先存历史；内置风格只能改正文，存成用户版；内置场合 400）/ 删除（被选着的 400，删了进 `config/prompts/presets/trash/`） |
| POST | `/api/presets/:kind/:id/revert` | 内置风格换回出厂的（用户版先存进历史再去掉；ADR-136），别的 400 |
| GET / POST / DELETE | `/api/presets/:kind/:id/versions/:vid[/restore]` | 自定义预设的历史版本，同提示词的历史版本 |
| PUT | `/api/presets/selection` | 整份换选法：`{defaults: {"平台:private\|group": {style?, scene?}}, conversations: {会话: {style?, scene?}}}`；选的预设不存在 400 |
| GET / PUT | `/api/characters/:id/card` | 角色卡正文 `character.md`：读（带历史版本列表）/ 改（旧版由角色卡插件存进 `card/history/`） |
| GET / POST / DELETE | `/api/characters/:id/card/versions/:vid[/restore]` | 角色卡历史版本：看 / 换成现在用的 / 删除 |
| POST | `/api/characters/:id/compact?conversation=` | 主人手动压一次（`/压缩`）；她正在回复、前面不多不用压返回 409；压了但自检没过返回 200 带着那一次的记录 |
| GET | `/api/runs?days=&flag=&stage=&before=&limit=` | 运行「模型调用」：最近一天 / 一周的每次调用（只读轻的列），按 util/runs.ts（私有存档） 的规则标出缓存突然掉了、首字变慢、渠道连续失败、输入快满了、出错，带各类计数、按岗位（回复之外：压缩、学说法、识图……）的次数 / 出错 / 花费（`stages`，`stage=` 只看一个岗位，识图是 `vision`）、趋势（前 4 个模型的首字 / 速度 / 命中）、单条花费，新的在前分页（规格-本地话题与压缩第九节）。一段时间最多读 2 万次，超了留最近的，`truncated: true`，计数也只算读到的 |
| GET | `/api/characters/:id/runs?limit=&stage=&conversation=` | 最近的模型调用（注入面板用，带 `context_json` 上下文明细）；`limit` 默认 20、最多 200，`stage` 过滤用途 |
| PUT | `/api/characters/:id/model` | 换模型 `{model, which}`，写进配置 `characters.<角色>`（ADR-084，不动角色卡、不重建对话）：`which: topic` 换话题用的（`topic`，`model: null` 删掉、跟着闲聊，ADR-073）；角色不存在 404，模型不在 Magpie 里 400，闲聊用的不能清空 400 |
| POST | `/api/characters/:id/new-topic` | 开一个新话题（2026-10-17 起；以前是在闲聊里插"从这里开始新话题"的分隔），返回这个话题那一行（topics.ts） |
| GET | `/api/characters/:id/topics` | 她的闲聊和全部话题（归档的也在）：标题、谁起的、置顶 / 归档时间、最后一句、主人说了几句（topics.ts） |
| POST | `/api/characters/:id/fork` | 分叉 `{conversation}`：把这个会话（闲聊或她自己的话题）现在给她看的那份复制成新话题，返回新话题那一行（topics.ts） |
| POST | `/api/topics/:id` | 改名 `title`、置顶 `pinned`、归档 / 拿回来 `archived`；归档时顺带整理一次这个话题的记忆（topics.ts） |
| POST | `/api/characters/:id/summarize` | 不等安静 10 分钟，马上整理一次记忆（测试、按钮用；跳过总结失败后的退避），返回统计 |
| GET | `/api/characters/:id/memory` | 事实（最近 200 条）和片段（最近 50 条）的原样（以前给记忆页的，现在没有页面在用） |
| POST | `/api/models/refresh` | 重新拉各个模型来源的列表 |

记忆管理（规格-记忆管理，ADR-076；只在主窗口，`:char` 是角色 id，`:ref` 是 `f:<id>` / `e:<id>`；改的接口在她正在整理这个角色的记忆时返回 409）：

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/memory/:char/items?kind=&status=&person=&conversation=&q=&cursor=&limit=` | 事实和片段合在一起按时间倒序；不给 `status` 时不含已撤销、被取代、等确认的；`q` 按字搜（各种状态、两个字的也搜得到）；每页默认 50、最多 200，游标是上一页最后一条的 `created_at|ref` |
| GET | `/api/memory/:char/counts` | 各种类有效的条数、片段、等确认、过期的暂时事、已撤销；记忆来自哪些会话；是不是正在整理 |
| GET | `/api/memory/:char/items/:ref` | 一条的全部字段、变化过程（新的在前）、改动记录、原话（最多 10 条、每条 200 字）和第一句原话前后各两句 |
| PATCH | `/api/memory/:char/items/:ref` | 改字 `text`（新建一条、旧的被取代）、改谁能听到 `scope`、改状态 `status`（active / historical / disputed）；片段不能改，400 |
| POST | `/api/memory/:char/items/:ref/confirm` | 等确认的记住（可带 `text` 改了再记）；已经不是等确认的 409 |
| POST | `/api/memory/:char/items/:ref/revoke` | 撤销 `{reason}`：标 revoked、拿出索引，能恢复 |
| POST | `/api/memory/:char/items/:ref/restore` | 撤销的恢复，重新进索引 |
| DELETE | `/api/memory/:char/items/:ref?hard=1` | 真删：只能删已撤销的（不然 400），先备份这个角色的记忆库；删行、索引、以前的改动记录，留一行 purged |
| POST | `/api/memory/:char/forget` | 忘掉某个人 `{person}` 或某段对话 `{conversation}`：`dryRun` 只数；`allCharacters` 每个角色各做一遍；正式做 = 逐条撤销，一次最多 500 条（`more`） |
| GET | `/api/memory/:char/try?q=&conversation=` | 试一下她会想起什么：前 20 条，每条带全文名次、向量名次和相似度、能不能看到、最后给不给；不给 `conversation` 是主人在本地私聊 |

运行、用量、数据：

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/health` | 运行时长、进程号、内存（RSS / 堆 / 堆外 `externalMB` / `arrayBuffersMB`）、活跃实例数、待审批数、嵌入子进程状态（挂机测试用）；CPU 两次请求之间的差分，第一次是 null：`cpuPercent`（本进程，占整机）、`systemCpuPercent`（整机）、`systemMemMB`（整机内存，util/procstats.ts） |
| GET | `/api/health/data` | 数据健康度：最近一次维护的各库大小和清了什么、近 30 天大小历史（maintenance.ts） |
| POST | `/api/maintenance/run` | 手动跑一次维护；正在跑返回 409（maintenance.ts） |
| GET | `/api/backups` | 备份列表：备份位置、每份的时间、原因（每天 / 手动 / 改动前）、状态和问题、大小、带了什么、里面有哪些库（backup.ts） |
| POST | `/api/backups` | 现在备份一份：`{material?, media?}`，媒体是 none / stickers / all，做完才返回（backup.ts） |
| POST | `/api/backups/:name/restore` | 恢复：`{kind: whole}` 整份或 `{kind: db, path}` 一个库；先做"恢复前"备份，做不成、备份坏了、改动前那种只带一个库却要整份、已经有恢复在等，都返回 409；成了返回后马上自己重启（backup.ts，ADR-070） |
| POST | `/api/export/summary` | 导出前的隐私清单 `{choice}`：QQ 号、群聊、私聊、本地聊天各多少，草稿几份（成人向几份）、回放样本、媒体、密钥；另给导出位置、设没设主密码（backup.ts，ADR-072） |
| POST | `/api/export` | 导出 `{choice: {drafts, adult, replay, media, secrets}}`：做一份备份打成 zip，返回文件、大小；没设主密码却要带密钥等返回 409（backup.ts） |
| POST | `/api/export/pick` | 弹 Windows 的选文件夹对话框换导出位置（只记在这次运行里；backup.ts） |
| POST | `/api/export/open` | 在资源管理器里选中刚导出的那个包（只认这次运行里导出的；backup.ts） |
| GET | `/api/restore` | 上次恢复的结果（`restore-last.json`）、在等重启的恢复、数据目录旁边换下来的几份（backup.ts） |
| POST | `/api/restore/swap-back` | 切回换下来的某一份 `{name}`：现在的同样改名留着，然后自己重启（backup.ts） |
| GET | `/api/secrets` | 密钥：`state`（empty / open / locked）、设没设主密码、存了哪些名字；从来不给值（secrets.ts） |
| POST | `/api/secrets/password` | 设或改主密码 `{password}`（至少 8 个字，要先解开）；返回新的恢复码，只给这一次（secrets.ts） |
| POST | `/api/secrets/unlock` | 锁着时用主密码或恢复码解开 `{secret}`，用这台电脑重新包好，然后自己重启；不对返回 409（secrets.ts） |
| POST | `/api/restart` | 后台自己重启（设置里"重启后生效"的提醒上点"现在重启"；lifecycle.ts） |
| GET | `/api/maintenance?limit=` | 最近几次维护的记录（`limit` 默认 10、最多 50） |
| GET | `/api/outbound-audit?days=` | 她发到平台上的话里客服腔、"作为 AI"这类各命中几次（ADR-110 第 2 条，只记数、不拦；`outbound_hits`，见 [QQ 渠道](qq.md)"发消息"）：`{ days, characters: { 角色 id: [{ kind, sent, dry, last }] } }`，按角色分，有记录的角色四类都给（没命中的是 0），`sent` 真发出去的、`dry` 试运行"她会说"的；`last` 是最近一次（不分真发、试运行）的 `at`、`hit`、`dry`、`text`（那条消息的整条，删掉了是 null）、`conversation`（会话 id，悬停卡片照模型调用页写成"QQ 群 · 群名""QQ 私聊 · 名字"，QQ 会话列表里没有名字的群写群号），没命中是 null。`days` 不给是 7，给了要是 1～90 的整数，0、负数、小数、超过 90、不是数回 400（`auditDays`）。运行 · 性能的"说话像不像 AI"读它 |
| GET | `/api/usage?days=` | 用量汇总：按天、角色、模型、用途、会话、速度；`days` 默认 30（1 到 365）。有价格缓存时带花费，第一次没有缓存会去 models.dev 取一次 |
| POST | `/api/usage/prices/refresh` | 手动刷新价格缓存；取不到 502 |
| GET | `/api/usage/routes?days=` | 按 Magpie 实际渠道汇总缓存命中；`days` 默认 7（1 到 90） |
| POST | `/api/perf` | 前端性能记录（设置里临时打开）：每 5 秒送一批（最多 100 条）写进 `data/logs/perf-日期.jsonl`，一天最多 20 MB |

设置：

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/settings` | 现在的值（每次从 `sumika.yml` 重读）、改过但还没生效要重启的项（`restart`）、整个模型目录的上下文长度和思考档位、Magpie 地址、数据目录、工作目录 |
| PUT | `/api/settings` | `{key, value}` 改一项；值不对 400。见下面"设置页的配置" |

表达学习（实现和细节见表达学习（私有存档），这里只列路由）：

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/expression/expressions?status=&limit=` | 统计和列表 |
| GET | `/api/expression/expressions/:id/evidence` | 证据原话 |
| POST | `/api/expression/expressions/:id/:decision` | `approve` / `reject` / `undo`，别的 404；带 `from` 而状态已变 409 |
| POST | `/api/expression/expressions/:id/block` | 某个角色不让用 / 放行 |
| GET | `/api/expression/review` | 候选审到哪了：多久审一次、上次、下次大概、正在审吗、还有几条候选（ADR-097） |
| POST | `/api/expression/review` | 现在审一批：马上返回、后台审；正在审时 409 |
| POST | `/api/expression/import-seeds` | 导入 `data/dev/seeds/` 下的种子表；先备份 `learning.db`，备份没做成返回 409、不导 |
| POST | `/api/expression/rejudge` | 按现在的审核标准重判可用的说法，马上返回、后台一批批补；先备份 `learning.db`，备份没做成返回 409、不重判 |
| GET | `/api/expression/try?character=&conversation=&text=` | 试一句话会挑出哪些（只读） |
| GET | `/api/expression/ai-speakers` | 哪些号是 AI |
| POST | `/api/expression/ai-speakers` | 标上 / 取消标 AI 号；配置里写的取消不了，409 |

WebSocket：

| | 路径 | 说明 |
|---|---|---|
| WS | `/api/ws` | 连上先收一条 `state`。**上行**（网页发给后台）：`send {characterId, conversation?, text, mode?}`（最长 20000 字；`mode: steer` 是她在忙时插话，她不在跑就当普通的一句，规格-排队插话停止）、`stop {characterId, conversation?}`（停下她）、`decide {id, choice, reason?}`（审批：`once` / `conversation` / `always` / `deny`，拒绝可带一句原因，规格-审批分级；旧的 `approve: true/false` 照样认）、`mode {mode}`（切审批模式）；格式不对回 `error`（`bad_message`）。**下行**见下 |
| GET | 不以 `/api/` 开头的所有路径 | 打包后的主窗口页面（`web/dist`），找不到文件回 `index.html`；没打包时给一句提示 |

WebSocket 下行的消息类型（`type`）：

- `state`：角色、模型、待审批、审批模式、嵌入状态等（同 `/api/state`）；人设、嵌入、记忆、审批模式变了就重推。
- `status`：某个角色的状态（`idle` / `thinking` / `waiting`）。
- `start` / `delta` / `thinking` / `tool` / `event` / `end` / `error`：回合事件（第 3 步），都带 `characterId`。`error` 也可能是网页自己发的 `send` 处理失败（`internal`）。
- `steer`：主人插的话：`accepted` 收下了（等她做完这一步）、`delivered` 送到了（记录里已经有这条）、`dropped` 没送到（停下了就丢；带 `requeue` 的是回合结束还没送到，网页当普通的一句再发）。`approval-resolved`：审批有结果；`approval-gone`：这条审批已经不在了（只回给发 `decide` 的那个连接）。
- `qq-state` / `qq-event` / `qq-selftest`：QQ 号状态、会话里多了或改了一条、收发自检状态。见 [qq.md](qq.md)。

## 设置页的配置（settings.ts）

只开放一张白名单，令牌、QQ 号这类一律不读不写：

| 键 | 说明 |
|---|---|
| `defaults.backgroundModel` | 后台的活用的模型，必须在模型来源的列表里；没有默认值，没选读出来是 `null`（ADR-098） |
| `vision.model` | 识图用的模型，必须在模型来源的列表里；没有默认值，没选读出来是 `null`，图都不看（ADR-098） |
| `expression.reviewHours` | 每几小时把学说法的候选交给 AI 审一批，0 = 不自动审（ADR-097） |
| `defaults.backgroundThinking` | 后台调用的思考档位 |
| `agent.thinking.local` / `private` / `group` | 本地 / 平台私聊 / 群聊的思考档位 |
| `agent.context.window` | 上下文封顶，`null` 或不写 = 跟着模型报的长度（去掉这一项） |
| `agent.context.topicCompactAt` | 话题到这么大就压缩（默认 200000，ADR-074） |
| `agent.context.autoCompactChat` | 本地闲聊自动压缩（默认开；关掉的话满了只带最近的话） |
| `tools.workdir` | 默认工作目录，空 = 用户目录（去掉这一项） |
| `tools.approvalMinutes` | 审批超时 |
| `tools.autoApproveDirs` | 自动模式下可以直接放行写文件的目录 |
| `trace.debug` | 调试记录，每次模型调用存完整请求和回复 |

- 写回 `sumika.yml` 用 `setConfigValue`，保留文件里的注释和其他项。

Go 侧已读、设置页还没开放的模型键（ADR-390，规格-模型配置 M1；只读和校验，请求仍走合成出来的 magpie）：

| 键 | 说明 |
|---|---|
| `providers.<id>` | 模型服务商：`kind`（只认 `openai-compatible`）、`name`、`baseUrl`、`keyRef`（`${secret:名字}` 或 `${env:名字}`，明文拒收）、`compat`（占位，还没接到请求上）。ID 用小写字母、数字、`-`、`_`，不含 `/ # :` |
| `defaultProvider` | 裸模型引用归哪个服务商；不写 = `magpie` |

- 读配置时没有 `providers.magpie` 就在内存里按 `magpie.baseUrl` 补一条，不写回文件；`providers.magpie` 存在时 `magpie.baseUrl` 被忽略，加载时日志提示一次。
- **改完马上生效（`LIVE`）的 10 项**：`defaults.backgroundModel`（改到记忆、学说法、话题三个插件上）、`defaults.backgroundThinking`、`vision.model`、`expression.reviewHours`、`agent.thinking.local` / `private` / `group`、`agent.context.window`、`agent.context.topicCompactAt`、`agent.context.autoCompactChat`——这些插件每次调用时才读配置，设置页直接改到运行中的插件上，下一句话就用新值。
- **其余 4 项重启后台才生效**：返回里的 `restart` 列出改过的项，页面照实说。这个列表只在内存里，后台重启就清了。
- 审批模式（`tools.approvalMode`）不在这里，走 WebSocket 的 `mode`，见[工具与审批](tools.md)；角色用的模型走 `PUT /api/characters/:id/model`。

## 前端要点

- 界面全部用组件库 `web/src/ui/`（规范见 [设计语言.md](../设计语言.md)）。外壳是信息架构的四栏：App.tsx（私有存档）（图标竖栏、会话列表、右侧栏）、Thread.tsx（私有存档）（本地聊天窗口）、Inspector.tsx（私有存档）（右侧栏的"本轮注入""资料"）；外壳的样式在 `web/src/ui/shell.css`，`lint:ui` 也管它。竖栏现在是 本地 / QQ / 搜索（命令面板，Commands.tsx（私有存档））/ 记忆 / 运行 / 设置：QQ 页是 Outside.tsx（私有存档）（数据在 qq.ts（私有存档），见 [qq.md](qq.md)），记忆和运行两页在 DataCenter.tsx（私有存档）（学到的说法在 Expressions.tsx（私有存档），用量在 Usage.tsx（私有存档）），设置是 Settings.tsx（私有存档）（外观、平台账号等）加 SettingsServer.tsx（私有存档）（模型、工具与审批、数据，走 `/api/settings`）。
- 正在生成的回复是"草稿"，回复落盘（`event`）时转成正式消息，思考过程和工具调用跟着转过去。上一个工具没做完下一个就开始的算同一批（`batch`），合成一张并行卡片。
- 主人的一句话和她这一轮的回复合成一个 `Turn`（轮次刻度按它量）；最后一条回复下面显示这一轮的统计（输入 = input + cacheRead，速度只算出字阶段）。
- 读屏（2026-10-19，业界对照总报告第 12 条）：本地的聊天记录 `role="log"` 配 `aria-live="off"`，里面不放状态区；要念的只有四句（她开始回复了、说完了、出错了：原文、她想做什么在等你批准），由 `ChatFrame` 里常驻的隐藏状态区念（`announce` 属性），什么时候念哪句在 thread/announce.ts（私有存档）；你自己按停止的那一轮不念"说完了"，后台跟着推的"操作被中止"也不念"出错了"。QQ 的聊天记录照旧是 polite 的 log（`ChatFrame` 的 `live`：消息整条到，不会逐字念）。键盘：聊天滚动区的 `scroll-padding` 让开顶栏和输入框；停止键收起后焦点回输入框（焦点已经挪到别处、鼠标点过别处、正在选字时不拉）；轮次刻度整列一个 Tab 位、方向键走。`scripts/check-a11y.ts` 分段查这些（段名和每段查什么写在文件开头）。
- 后台还不支持插话和停止：她回复时再发 = 打断她带着新消息重新回复（ADR-020），输入框的提示也这么写（`Thread.tsx` 给输入框传 `interrupt`、没传停止按钮）；Alt+Enter 排队是前端自己存着，她回到空闲再发。
- 刷新页面或往上翻时，消息从 `/messages` 读，思考和工具调用从 `/steps` 读回来按 `event_seq` 挂回对应的回复（store.ts（私有存档） 的 `loadHistory` / `loadOlder`）；最近一轮的统计从 `/runs` 读回来挂在最后一条回复上。
- 前端状态（ADR-120）：主窗口的数据在 store.ts（私有存档） 的 Zustand 仓库 `useSumikaStore`（角色、模型、审批、每个会话的记录和草稿、连接状态），怎么变由里面的纯函数 `reducer` 管；动作（`send`、`loadHistory`、`decide`……）是同一个文件导出的函数，App 里 `useEffect(connectSumika, [])` 连 WebSocket。后台推来的消息先攒在 `enqueue` 里、下一个任务一起更新一次（出字另外攒 33 毫秒）：Zustand 靠 `useSyncExternalStore`，更新是同步的，不攒的话一轮结束要多画好几遍（`test/web-store-batch.test.ts`）。QQ 的数据在 qq.ts（私有存档） 的 `useQqStore`。组件订阅只取自己用的那块，选择器会新建对象 / 数组 / Set 的包 `useShallow`。组件自己的局部状态照旧用 React state。
- 主窗口的栏（ADR-120）：竖栏右边的会话列表 | 聊天 | 右侧栏在 Shell.tsx（私有存档），用 react-resizable-panels 的 `Group` / `Panel` / `Separator`，外壳 `.su-app` 只剩"竖栏 | 右边一块"两列（shell.css（私有存档））。每栏的默认、最小、最大宽度是 `tokens.css` 的 `--w-list` / `--w-side` 和 `--w-*-min` / `--w-*-max`，聊天窗口至少 `--w-chat-min`；拖出来的宽度按像素存在 `view-state.ts` 的 `panelWidths`（浏览器里的 `sumika.panels`），双击分隔线清掉回默认。窗口 ≤ `--bp-compact` 时列表固定 `--w-list-icons`、没有分隔线；≤ `--bp-narrow` 时右侧栏是浮层（App.tsx 的 `Sheet floating`），不在这几栏里。检查：`scripts/check-panels.ts`（check:ui 的 panels）。
- 当前角色的颜色写进外壳的 `--char`，外壳带 `data-char`，角色色阶梯跟着重算。styles.css（私有存档） 只剩给 Streamdown 用的 Tailwind 工具类和颜色映射（不引 preflight，不定义 `--accent` 这类会和 token 重名的变量）。

## 验证

开发：`bun run dev`（后端，7720）+ `bun run dev:web`（前端，7721，代理 `/api`）。生产：`bun run build:web` 后 `bun run start`，直接打开 7720。
