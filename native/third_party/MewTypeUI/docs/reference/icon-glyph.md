# 替代图标盘点（icon-glyph）

基线、净室公式与状态见 [画法规格](paint/icon-glyph.md)。R=tdesktop/Telegram/Resources/icons；L=lib_ui/icons。PNG 表内 bbox 取非零亮度；表中目标以这个边界为基准，低覆盖像素与半覆盖边界最多相差1 DIP时取其间值。原始非零/半覆盖记录保存在任务 scratchpad 的 assets.json / assets-half.json，不含进仓库的上游素材。

用户目标：导航、输入和顶栏的图形大小与固定版本 Telegram 相同。范围是所有现有替代图标及其普通/选中/hover/down/disabled 状态；设置结构与宿主状态所有权不变。主题是唯一尺寸来源。

## 图形与布局盘点

“旧”表示 SVG 被画入的矩形，不表示可见图形已经填满该矩形；Lucide24的图形一般占14–22，详见对应源码路径。尺寸为 DIP，bbox右/下不包含。

| 位置与调用入口 | 对照 PNG | 画布 | bbox | 旧绘制矩形 | Telegram目标框（非拉伸尺寸） |
|---|---|---|---|---|---|
| dialogs.Rail 默认文件夹；scope folder | R/folders/folders_custom.png |36×36|(7,9)-(29,26)|36×36；原创图形24×21|22×17|
| dialogs.Rail 宿主 Lucide | R/folders/folders_home.png |36×36|(5,6)-(31,29)|36×36；通常33×33|26×23；宿主边界例外|
| rail 所有消息参照 | R/folders/folders_all.png |36×36|(4,7)-(32,30)|宿主提供|28×22～23|
| rail 频道参照 | R/folders/folders_channels.png |36×36|(7,7)-(29,30)|宿主提供|22×23|
| rail 未读参照 | R/folders/folders_unread.png |36×36|(7,7)-(30,28)|宿主提供|22～23×20～21|
| rail 机器人参照 | R/folders/folders_bots.png |36×36|(5,7)-(31,28)|宿主提供|26×21|
| rail 书参照 | R/folders/folders_book.png |36×36|(5,7)-(31,27)|宿主提供|24～26×19～20|
| rail 猫参照 | R/folders/folders_cat.png |36×36|(3,6)-(34,29)|宿主提供|30～31×21～23|
| dialogs.Row 群类型 | R/dialogs/dialogs_chat.png |16×10|(1,1)-(15,10)|16×10|14×9|
| dialogs.Row 频道类型 | R/dialogs/dialogs_channel.png |12×11|(0,0)-(11,11)|12×11|11×11|
| dialogs.Row 机器人类型 | R/dialogs/dialogs_bot.png |16×11|(1,0)-(15,11)|16×11|14×11|
| dialogs.Row/rail 锁 | R/dialogs/mini_tag_lock.png |16×16|(4,3)-(12,13)|20×20|8×10；小锁语义映射|
| dialogs.Row 静音；chat Silent | R/dialogs/dialogs_mute.png |8×8|(0,0)-(8,8)|8×8|8×8|
| dialogs.Row 置顶/compact | R/dialogs/dialogs_pinned.png |20×20|(4,4)-(17,16)|20×20|13×12|
| dialogs.Row 认证 | R/dialogs/dialogs_verified_star.png |18×16|(4,2)-(18,16)|20×20|14×14|
| dialogs.avatarContent 归档 | R/archive_userpic.png |24×24|(2,1)-(22,22)|头像内槽，通常26×26|20×21|
| dialogs.cornerBadge 订阅星形 | dialogsSubscriptionBadgeSize；程序星形 |16×16；外轮廓扩1.5|完整星形范围|16×16；原可见约14×14|16×16；外19×19|
| dialogs.avatarContent 宿主/验证码头像 | 宿主资源；无静态等价PNG |头像内槽|随资源|头像内槽|保留头像用途；不套14×14小认证指标|
| dialogs.Row Sending | R/dialogs/dialogs_sending.png |11×11|(0,0)-(11,11)|11×11|11×11|
| dialogs.Row Sent | R/dialogs/dialogs_sent.png |12×10|(0,1)-(12,10)|12×10|12×9|
| dialogs.Row Read | R/dialogs/dialogs_received.png |17×10|(0,1)-(17,10)|17×10|17×9|
| dialogs.Search/Scope clear | R/dialogs/dialogs_cancel_search.png |18×18|(3,3)-(15,15)|11×11|10～12×10～12|
| chat.Header 搜索 | R/top_bar_search.png |32×32|(8,8)-(25,25)|24×24；图形20×20|16.5×16.5|
| chat.Header 通话 | R/top_bar_call.png |32×32|(9,8)-(25,24)|24×24；图形22×22|16×16|
| chat.Header 更多 | L/title_menu_dots.png |8×20|(2,2)-(6,18)|24×24；图形4×18|4×16|
| chat.Header 返回 | R/title_back.png |16×16|(2,0)-(12,16)|24×24；图形16×16|10×16|
| chat.Header 关闭 | R/info/info_close.png |32×32|(9,9)-(23,23)|24×24；图形14×14|12～14×12～14；用途见例外|
| chat.BottomInfo 单/双勾 | R/history_sent.png / history_received.png |13×11 /18×11|(0,1)-(13,11)/(0,1)-(18,11)|各素材画布|13×10/18×10|
| chat.BottomInfo 浏览/回复 | R/history_views.png / history_replies.png |16×11|(0,0)-(16,11)/(2,0)-(13,10)|16×11|16×11/11×10|
| chat.BottomInfo 置顶 | R/history_pin.png |18×18|(4,2)-(16,15)|18×18|12×13|
| chat.BottomInfo 发送中/限时 | R/dialogs/dialogs_sending.png；chat/ephemeral_badge.png |11×11；14×14|分别沿完整小槽|11×11；14×14|不缩小槽；独立描边|
| chat.LatestButton | R/history_down_arrow.png |52×52|(16,23)-(36,33)|24×24；图形14×8|20×10|
| compose.ActionButton 附件 | R/chat/input_attach.png |40×40|(11,9)-(30,31)|24×24|19×22|
| compose.ActionButton 表情 | R/chat/input_smile_face.png + 程序圆圈 |40×40|(15,17)-(25,26)仅脸部|24×24；外圆22|外圆22；不能把脸部10×9当整图|
| compose.ActionButton 发送 | R/chat/input_send.png |24×24|(1,1)-(23,23)|24×24|22×22|
| compose.ActionButton 语音 | recordSize/Lottie |26×26|无静态等价PNG|26×26|保留；Lottie例外|
| compose.ActionButton 保存 | R/chat/input_save.png |40×40|(10,12)-(30,27)|24×24|20×15|
| compose.ActionButton/Reference 取消 | L/box_button_close.png |16×16|(2,2)-(14,14)|24×24；图形14×14|12×12|
| compose.ReferenceBar 回复 | R/chat/input_reply_settings.png |32×32|(5,5)-(27,26)|35×35；图形约26×19|22×21|
| compose.ReferenceBar 编辑 | R/chat/input_edit.png |32×32|(8,8)-(25,25)|35×35；图形约32×32|17×17|
| compose.Attachments 文件/编辑/移除 | R/menu/file.png；chat/input_edit.png；L/box_button_close.png |24/32/16画布|同对应行|24×24|17×16；17×17；12×12|
| compose.Picker clear/最近分类 | L/box_button_close.png；R/emoji/emoji_recent.png |16/24画布|(2,2)-(14,14)/(3,3)-(21,21)|24×24 /28×28|12×12 /18×18|
| content.Media/File/Voice 播放 | R/history_file_play.png |32×32|(11,7)-(25,25)|24×24；图形约18×20|14×18|
| content.File/Voice 暂停 | R/history_file_pause.png |32×32|(9,8)-(23,24)|24×24；图形16×20|14×16|
| content.Media/File/Voice 下载 | R/history_file_download.png |32×32|(7,7)-(25,26)|24×24；图形20×20|18×19|
| content.Media/File/Voice 取消 | R/history_file_cancel.png |32×32|(9,9)-(23,23)|24×24；图形14×14|14×14；线宽独立|
| content.File 文件 | R/history_file_document.png |32×32|(7,7)-(25,25)|24×24；图形18×22|18×18；文件轮廓替代|
| content.Markdown 复制 | R/menu/copy.png |24×24|(4,3)-(19,20)|13×13|目标15×17按13/24比例；紧凑代码头例外|
| overlay.Menu 勾/子菜单 | L/default_checkbox_check.png；R/menu/submenu_arrow.png |14×10；24×24|(1,0)-(13,10)/(9,6)-(16,18)|24×24|12×10；7×12|
| overlay.Menu 宿主图标 | R/menu/copy.png等 |24×24|按宿主资源|24×24|宿主画布限框；不能保证任意图形轮廓|
| overlay.Box 关闭 | L/box_button_close.png |16×16|(2,2)-(14,14)|24×24|12×12|
| overlay.Viewer 左右/关闭/缩放 | R/mediaview/next.png；title_viewer_button_close.png |24×24；44×32|(9,4)-(18,20)/(17,11)-(27,21)|24×24|9×16；10×10；缩放属宿主扩展；viewer.go为并行路保留|
| overlay.Toast 宿主图标 | defaultToastLottieSize |32×32|无任意SVG对应物|32×32|保留宿主资源；非Lucide默认|
| settings.Button 宿主图标/箭头 | R/settings/*；R/menu/submenu_arrow.png |24×24|箭头(9,6)-(16,18)|24×24|宿主限框；7×12|
| settings.ChooseBox 搜索 | L/box_search.png |16×16|(1,1)-(15,16)|16×16；图形约13×13|14×15|
| settings.CheckMark | L/default_checkbox_check.png |14×10|(1,0)-(13,10)|14×10；图形约8×5|12×10|
| control.IconButton 返回/关闭；PageTitle | L/box_button_back.png / box_button_close.png |16×16|(0,0)-(16,16)/(2,2)-(14,14)|24×24|16×16 /12×12|
| control.RoundButton/Dropdown 宿主SVG | R/menu/* |24×24|按宿主资源|24×24|宿主资源限框|
| control.Dropdown 默认箭头 | R/menu/submenu_arrow.png旋向 |24×24|(9,6)-(16,18)|24×24|12×7旋向|

## 已按独立规格绘制的几何

没有把 *IconSize 全部视为错误：reaction.iconSize=18 是布局槽，ImageSize=32 是宿主表情画布；file.iconSize=44 是圆底；media.actionSize=44 是命中/圆底；viewer.actionWidth/Height=46/54 是按钮槽；toast.iconSize=32 是Lottie画布。Stop方块14、TTL表盘、community箭头、typing/upload/recording/speaking动画、progress圆弧及settings清空X已经由各自 paint 规格直接给几何，本轮不统一缩小。

## 素材和并行边界

宿主SVG只有公开Size，mygo没有公开路径边界/重写笔画接口。本库内置Lucide按注册的中心线边界校准；导航按20/24宽、21/24高的house占比推算限框，普通宿主装饰将源画布限制为布局画布的20/24。任意宿主轮廓和笔画不能保证对应同一PNG。没有复制 Telegram 图形来规避这个边界。

并行 gaps2 的 overlay/viewer.go、overlay/tooltip.go、content/album*.go、content/media.go（Video路径）、compose/editor.go 保留；媒体通过共用 CircleIcon 校准。overlay/viewer.go:410的viewerButton仍直接ui.Icon，未接入本路的图形校准；其左右箭头应按mediaview/next.png的9×16、关闭按title_viewer_button_close.png的10×10独立接入，不能套菜单箭头7×12。交回合并路处理，未按已校准计。

## icon-glyph 前批次验收（保留历史）

CGO_ENABLED=0全库12个测试包、Go vet及固定mygo v0.7.4 vet退出0。57个实际墨迹尺寸子场景覆盖19类×100/125/150%，两轴误差均≤1 DIP；原有0.05物理像素居中断言保留并通过。故障覆盖将Fit与HostGlyph两轴减半，57/57子场景变红；原实现57/57通过，故障文件只在scratchpad，未改仓库源文件。

独立Go程序实际渲染51组组件与官方PNG参考叠图，改后最大误差1 DIP。原生画廊采用相同公开API验收页比较基线组件与改后组件，明暗各六页、共24个新进程，实际GetDpiForWindow均为120、外框1018×760物理像素，正常退出码均0。截图批次在独占重任务锁内选择125%，finally恢复批次开始时的显示档位；dpi-before.json与dpi-restored.json均为125%。另以用户telegram-rail.png实际载入画廊半透明叠图，native-reference.png显示参考图和对齐参数，DPI 120、正常退出0；该裁切截图只供视觉对照，尺寸验收仍用登记版本的官方PNG。

| 原生图形 | 改前DIP | 改后DIP | PNG参考DIP | 最大轴误差：前→后 |
|---|---|---|---|---|
| 导航宿主house |31.2×32.8|25.6×23.2|26×23|9.8→0.4|
| 顶栏搜索 |20.8×20.8|16×16.8|17×17|3.8→1.0|
| 输入附件 |22.4×23.2|18.4×21.6|19×22|3.4→0.6|

截图、裁切与绿/洋红叠图位于任务scratchpad/icon-glyph：before/after-icons-glyphs-light.png、before/after-chat-header-light.png、before/after-compose-actions-light.png；三张并排图为native-rail-before-after.png、native-header-before-after.png、native-compose-before-after.png。JSON原始量测为proof/smoke-before.json、proof/smoke-after.json和native-comparison.json。绿为本库、洋红为参考，仅按图形边界验收，轮廓不是相同素材。

完整生成器两次输出的四个文件逐字节一致。本次改动的metrics_gen.go与theme-metrics.md也与仓库逐字节一致；未改动的配色Go/Markdown文件仅有Windows工作副本CRLF差异，归一换行后相同，未因此重写无关配色文件。记录见generation-comparison.json。

真实模型调用通过本地网关完成；最终opendesign/deepseek-v4.1-flash回复完整。复核其条件项：缓存仅存几何且每变体SVG独立不可变，零尺寸提前返回；宿主任意SVG的边界限制保留。模型意见不代替尺寸、居中与原生运行证据。

活动帧性能：相同验收页、明暗×100/125/150%共六档，每档先热身30帧再强制重建200帧，前后各1200帧。改前build P95为0.583–1.006 ms，改后0.533–1.000 ms；包含软件截图读回的total P95从2.000–4.423 ms变为1.625–3.156 ms，最大帧24.136→17.348 ms，均无超过100 ms的样本。每帧分配含完整RGBA读回：约1.94–4.34 MB、196次，前后相近；不能当成原生渲染器的分配量或呈现周期。原始记录在proof/active-before.json和proof/active-after.json，命令退出均0。

原生新进程12次改后采样：工作集最大126930944字节，私有提交最大161697792字节；启动至截图944.1–1656.7 ms（含脚本等待），进程CPU累计343.75–2250 ms。最大值出现在dialogs/rail明色样本并保留；12次改前对应工作集/私有提交最大91758592/136466432字节、CPU累计343.75–453.125 ms。此采样不是空闲CPU、输入到呈现延迟或DXGI错帧率测量。

相同验收页的默认优化Go构建：gallery-before.exe为20819456字节，gallery-after.exe为20866560字节，差47104字节；包含调试符号及构建路径差异，不能归为纯图形代码体积。组件不读写文件；新增源码和规格均在仓库，截图、量测、模型请求/回复、对照基线与构建产物均在任务scratchpad。无运行期图形缓存文件。


## icon-aspect 等比验收（2026-11-04）

选择“较小轴初值＋原图墨迹比例约束”，公式见[图形规格](paint/icon-glyph.md#图形限框)。固定输出线宽与原图比例冲突时不再拉伸，允许另一轴留白或超出目标；实际与Telegram的差按下表报告。布局画布、命中区、806个指标的字段与默认值均不变，重新运行themegen，快照仍为JSON v9。

下表列56份库内源图在共用Fit→Element路径及4份宿主图在HostGlyph路径的实际渲染量测；56份库内图还逐一经过实际居中Icon/Painter路径，共116条路径。原图使用未修改的源笔画2；原创实心文件夹保留原填充。以32倍渲染、半覆盖边界量本库墨迹，避免小图的整数像素量化；官方@1x PNG按非零亮度量边界。Δ为“本库−Telegram”，单位DIP；两条路径各自的比例、宽高、Δ和最大轴误差在任务scratchpad/icon-aspect的numbers.json、numbers-table.txt。Element最大比例差1.286%，Painter最大1.5625%，116/116≤2%；故意仅压缩纵轴的两处故障使60/60 Element和56/56 Painter比例断言失败。

`—`表示没有同用途静态PNG：订阅为程序星形，表情外圆为程序几何，语音为Lottie替代，Viewer缩放为扩展。mic与zoom行仅验其源素材的共用路径比例，原有直接Icon调用本身已等比，未把这些调用冒称新校准的Telegram图形。宿主house/message-circle/brain/settings统一对照folders_home的26×23目标，只保证等比，不保证未知素材都内含同一墨迹框。Dropdown的PNG边界已旋转90°后比较。

| 素材 / 共用绘制用途 | 原始墨迹比例 | 现比例 | 实测宽×高 DIP | Telegram Δ宽 / Δ高 DIP | 最大轴误差 DIP |
|---|---:|---:|---:|---:|---:|
| chat/backIcon | 1.000 | 1.000 | 10.000×10.000 | 0.000 / -6.000 | 6.000 |
| chat/downIcon | 1.750 | 1.767 | 13.250×7.500 | -6.750 / -2.500 | 6.750 |
| chat/sentIcon | 1.385 | 1.396 | 13.000×9.313 | 0.000 / -0.688 | 0.688 |
| chat/readIcon | 1.630 | 1.642 | 16.313×9.938 | -1.688 / -0.063 | 1.688 |
| chat/pendingIcon | 1.000 | 1.000 | 11.000×11.000 | 0.000 / 0.000 | 0.000 |
| chat/viewsInfoIcon | 1.375 | 1.381 | 15.188×11.000 | -0.813 / 0.000 | 0.813 |
| chat/replyInfoIcon | 1.000 | 1.000 | 10.000×10.000 | -1.000 / 0.000 | 1.000 |
| chat/pinInfoIcon | 0.727 | 0.731 | 9.500×13.000 | -2.500 / 0.000 | 2.500 |
| chat/silentInfoIcon | 1.192 | 1.182 | 8.938×7.563 | 0.938 / -0.438 | 0.938 |
| chat/searchIcon | 1.000 | 1.000 | 16.500×16.500 | -0.500 / -0.500 | 0.500 |
| chat/menuIcon | 0.222 | 0.221 | 3.531×16.000 | -0.469 / 0.000 | 0.469 |
| chat/cancelIcon | 1.000 | 1.000 | 12.000×12.000 | 0.000 / 0.000 | 0.000 |
| chat/callIcon | 1.000 | 1.000 | 16.000×16.000 | 0.000 / 0.000 | 0.000 |
| dialogs/muteIcon | 1.000 | 1.000 | 8.000×8.000 | 0.000 / 0.000 | 0.000 |
| dialogs/pinIcon | 0.727 | 0.737 | 11.188×15.188 | -1.813 / 3.188 | 3.188 |
| dialogs/clockIcon | 1.000 | 1.000 | 11.000×11.000 | 0.000 / 0.000 | 0.000 |
| dialogs/sentIcon | 1.385 | 1.391 | 12.000×8.625 | 0.000 / -0.375 | 0.375 |
| dialogs/readIcon | 1.630 | 1.642 | 16.313×9.938 | -0.688 / 0.938 | 0.938 |
| dialogs/groupIcon | 1.100 | 1.097 | 9.875×9.000 | -4.125 / 0.000 | 4.125 |
| dialogs/channelIcon | 0.952 | 0.955 | 10.500×11.000 | -0.500 / 0.000 | 0.500 |
| dialogs/botIcon | 1.222 | 1.222 | 13.438×11.000 | -0.563 / 0.000 | 0.563 |
| dialogs/archiveIcon | 1.100 | 1.100 | 20.000×18.188 | 0.000 / -2.813 | 2.813 |
| dialogs/verifiedIcon | 0.818 | 0.826 | 11.563×14.000 | -2.438 / 0.000 | 2.438 |
| dialogs/clearIcon | 1.000 | 1.000 | 12.000×12.000 | 0.000 / 0.000 | 0.000 |
| dialogs/lockIcon | 0.909 | 0.916 | 8.813×9.625 | 0.813 / -0.375 | 0.813 |
| dialogs/subscriptionFill | 1.045 | 1.049 | 16.000×15.250 | — | — |
| compose/clipIcon | 0.939 | 0.937 | 18.688×19.938 | -0.313 / -2.063 | 2.063 |
| compose/sendIcon | 1.000 | 1.000 | 22.000×22.000 | 0.000 / 0.000 | 0.000 |
| compose/smileIcon | 1.000 | 1.000 | 22.000×22.000 | — | — |
| compose/micIcon | 0.727 | 0.724 | 18.813×26.000 | — | — |
| compose/closeIcon | 1.000 | 1.000 | 12.000×12.000 | 0.000 / 0.000 | 0.000 |
| compose/checkIcon | 1.385 | 1.396 | 19.813×14.188 | -0.188 / -0.813 | 0.813 |
| compose/fileIcon | 0.818 | 0.819 | 14.750×18.000 | -3.250 / 0.000 | 3.250 |
| compose/replyIcon | 1.385 | 1.396 | 19.813×14.188 | -2.188 / -6.813 | 6.813 |
| compose/editIcon | 1.000 | 1.000 | 17.000×17.000 | 0.000 / 0.000 | 0.000 |
| compose/pickerAllIcon | 1.000 | 1.000 | 18.000×18.000 | 0.000 / 0.000 | 0.000 |
| content/playIcon | 0.900 | 0.901 | 14.000×15.531 | 0.000 / -2.469 | 2.469 |
| content/pauseIcon | 0.800 | 0.801 | 12.813×16.000 | -1.188 / 0.000 | 1.188 |
| content/fileIcon | 0.818 | 0.819 | 14.750×18.000 | -3.250 / 0.000 | 3.250 |
| content/downloadIcon | 1.000 | 1.000 | 18.000×18.000 | 0.000 / -1.000 | 1.000 |
| content/cancelIcon | 1.000 | 1.000 | 14.000×14.000 | 0.000 / 0.000 | 0.000 |
| content/copyIcon | 1.000 | 1.000 | 15.000×15.000 | 0.000 / -2.000 | 2.000 |
| overlay/closeIcon | 1.000 | 1.000 | 12.000×12.000 | 0.000 / 0.000 | 0.000 |
| overlay/leftIcon | 0.571 | 0.573 | 6.875×12.000 | -0.125 / 0.000 | 0.125 |
| overlay/rightIcon | 0.571 | 0.573 | 6.875×12.000 | -0.125 / 0.000 | 0.125 |
| overlay/checkIcon | 1.385 | 1.368 | 13.000×9.500 | 1.000 / -0.500 | 1.000 |
| overlay/zoomInIcon | 1.000 | 1.000 | 24.000×24.000 | — | — |
| overlay/zoomOutIcon | 1.000 | 1.000 | 24.000×24.000 | — | — |
| settings/chevronRight | 0.571 | 0.573 | 6.875×12.000 | -0.125 / 0.000 | 0.125 |
| settings/chevronLeft | 0.571 | 0.573 | 6.875×12.000 | -0.125 / 0.000 | 0.125 |
| settings/checkIcon | 1.385 | 1.391 | 12.000×8.625 | 0.000 / -1.375 | 1.375 |
| settings/searchIcon | 1.000 | 1.000 | 14.000×14.000 | 0.000 / -1.000 | 1.000 |
| control/backIcon | 1.000 | 1.000 | 16.000×16.000 | 0.000 / 0.000 | 0.000 |
| control/closeIcon | 1.000 | 1.000 | 12.000×12.000 | 0.000 / 0.000 | 0.000 |
| control/downIcon | 1.750 | 1.745 | 12.000×6.875 | 0.000 / -0.125 | 0.125 |
| dialogs/folderIcon | 1.143 | 1.143 | 19.438×17.000 | -2.563 / 0.000 | 2.563 |
| host/house | 0.953 | 0.953 | 21.938×23.031 | -4.063 / 0.031 | 4.063 |
| host/message-circle | 1.000 | 1.000 | 24.125×24.125 | -1.875 / 1.125 | 1.875 |
| host/brain | 1.001 | 1.000 | 24.125×24.125 | -1.875 / 1.125 | 1.875 |
| host/settings | 0.907 | 0.906 | 21.813×24.063 | -4.188 / 1.063 | 4.188 |

### 原生DPI120对照

`dialogs/rail`、`chat/header`、`compose/actions`及`icons/glyphs`均打开原生新进程，亮暗稳定后捕获本进程窗口并正常关闭。三处局部对比同时保存左右拼接和官方PNG洋红轮廓叠图；原始页图保留。以下为半覆盖边界；120 DPI的一物理像素为0.8 DIP，保留实际量化结果，未把它替换成高分辨率值。

| 区域 / 主题 | 官方宽×高 DIP | 改前宽×高 DIP | 改后宽×高 DIP | 最大轴差 改前→改后 DIP |
|---|---:|---:|---:|---:|
| 导航 / 亮 | 26×23 | 25.6×23.2 | 22.4×22.4 | 0.4→3.6 |
| 导航 / 暗 | 26×23 | 25.6×22.4 | 22.4×22.4 | 0.6→3.6 |
| 顶栏 / 亮、暗 | 10×16 | 9.6×16 | 10.4×10.4 | 0.4→5.6 |
| 输入 / 亮、暗 | 19×22 | 16.8×20.8 | 16.8×18.4 | 2.2→3.6 |

文件均在任务scratchpad/icon-aspect：`native-{rail,header,compose}-{light,dark}-before-after.png`；`native-{before,after}-{rail,header,compose}-{light,dark}-outline.png`；量测含半覆盖和非零覆盖两种边界，见`native-comparison.json`。PNG只作私有对照，不进入MIT仓库。此次不把两个轴与官方PNG同时贴合当作比例验收条件。

### 检查、资源与边界

CGO_ENABLED=0全库测试、Go vet、mygo v0.7.4 vet、CGO_ENABLED=1原生构建和原生捕获均退出0。画廊每页亮暗调用并稳定后出图。新增布局画布、目标内含、贴近目标边界三项行为故障分别被新断言拒绝；全部比例故障及第三轮细描边反例修复前退出1、修复后通过。旧的按实现公式重算两轴尺寸及抗锯齿尾迹的快照已删除；0.05物理px居中线未放宽。Sol xhigh三轮审查的第三轮采样问题修复后定向复核为`VERDICT: PASS`。

themegen完整生成两次均退出0：colors/metrics的Go和对照文档四份产物两次逐字节相同，与仓库逐行规范化换行后相同；字段、默认值和JSON v9合同未变，既有拒绝v1–v8测试通过。

软件Tester在100/125/150%各量亮暗200个活动帧。当前总耗时P95为1.734–3.085ms，最大5.431ms；125%亮/暗P95由2.512/2.430ms变为2.506/2.511ms。每帧约196次分配，125%约3.018MB，含图像回读缓冲；不能当作窗口输入到物理呈现的延迟。原始分组、分配和堆记录见`perf/active-{before,after}.json`，本次未测DXGI错帧率或GPU内存。组件仍不读写文件，临时驱动、模型审查记录、截图和构建产物全部留scratchpad。


