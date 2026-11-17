# 消息内容对照

版本沿用 README.md：tdesktop 250fc73、lib_ui 1ff7be7。T = Telegram/SourceFiles，L = lib_ui/ui。完整数值、palette 别名及默认明暗值分别由 theme-metrics.md、theme-colors.md 生成。

| 内容 | Telegram 声明 / 行为来源 | MewTypeUI 使用处 |
|---|---|---|
| 回复背景与竖条 | T/ui/chat/chat_style.h:56-57：背景 0.12、竖条 0.9；chat_style.cpp:774-781 选入/出站及选中态的 replyBar 色 | content.Quote；quote.backgroundOpacity、outlineOpacity；quote.in/out.background、bar |
| 回复圆角和布局 | T/chat_helpers/chat_helpers.style:1283-1287：圆角 5、竖条 3；T/ui/chat/chat.style:65-73：预览 32，padding 11/2/6/2 | QuoteOptions、QuoteState；quote.replyPadding*、previewSize、radius、outlineWidth |
| 回复摘要 | T/history/view/history_view_reply.cpp:46,746-794：普通单行、多行收起最多 5 行，先展开后跳转 | QuoteData.Multiline、QuoteState.Expanded |
| 转发头 | T/ui/chat/chat.style:151-153：13 semibold，链接不加下划线；颜色按 MessageStyle 进出及选中 | content.Forward，contentTitle 字阶、message.in/out.serviceForeground |
| 回应胶囊 | T/ui/chat/chat.style:673-695；净室规格 paint/message-bottom.md：固定22高/11半径、emoji槽18/画布32、计数起点26；零计数28宽；头像45+(n−1)×13宽、1留白和透明擦边；气泡内左移3；完整宽度折行，尾行独立S×K | content.Reactions；reaction.*；CountLabel与稳定顺序由宿主准备 |
| 回应普通/自己/选中/气泡外 | T/history/view/reactions/history_view_reactions.cpp:38-45,499-548；净室规格颜色角色表：气泡内入站12%/出站18%，自己实色；气泡外服务底色/前景反转，出站镜像 | reaction.in/out.*、inOpacity、outOpacity、outsideOwnAlpha；ReactionOptions.InBubble/Outside |
| 回应按下 | 同文件:549-566：前景 alpha 25/255；L/widgets/widgets.style:705-706：650/200 ms；L/effects/ripple_animation.cpp 的 quint 扩散、linear 消退 | content.ripple，reaction.rippleOpacity、motion.rippleShow/Hide；悬停保留药丸填色并显示可点击指针 |
| 链接卡 | T/ui/chat/chat.style:107-108,530-535：padding 10/5/7/7，thumbnail gap 8；T/history/view/media/history_view_web_page.cpp:801-890：总 5 行，标题最多 2 行，右图随文字高度逐行收缩 | content.LinkPreview；preview.*，Width 由宿主传当前内容宽；无图/右侧缩略图/下方大图 |
| 正文字阶 | T/ui/chat/chat.style:15,114-145：13，分别取进出方向的 text/link/mono/spoiler 与 selected 角色；L/basic.style:54-56：normal/semibold/bold | content.Markdown；message、contentTitle、contentStrong、contentCode 字阶；系统 TextScale 只乘一次 |
| 正文选区底色 | L/colors.palette:350,352：msgInBgSelected、msgOutBgSelected 同时供消息和文字选区使用 | Markdown.SelectionColor，按入站/出站方向取 selected 背景 |
| 正文引用、代码块 | T/chat_helpers/chat_helpers.style:1282-1305：引用 padding 10/2/20/2，pre header 20，左右滚动；L/text/text_renderer.h:17：折叠引用 3 行 | Markdown 的 >^ 折叠引用，代码语言头、复制、水平滚动；quote.textCollapsedLines、codeHeaderHeight |
| 暗色代码底色 | T/ui/chat/chat_style.cpp:785-787：暗色 pre 取黑色 alpha=192；chat_style.h:58：header 0.3 | quote.codeDarkBackground、codeDarkOpacity、codeHeaderOpacity |
| 剧透粒子 | L/effects/spoiler_mess.cpp:32-71,124-193,391-480：128 DIP图块、60帧、33ms；文字9000粒子、200/200/200ms、位移4～8 DIP；图片3000粒子、300/0/300ms、位移10～20 DIP；五种1.5～2 DIP圆角精灵 | content/spoiler.go独立生成白色透明图集，绘制时按方向/选中角色着色；随机出生相位、速度方向、线性淡入/淡出及边界环绕。文本在段落所有者DrawObjects中收集同一行连续占位并合并绘制，避免每字符一个GPU裁切批次，也避免mygo内联子节点跳过该回调。ReduceMotion静止 |
| 图片、贴纸与带正文裁切 | T/ui/chat/chat.style:168-173,330；photo.cpp:343-432；media_streaming_utility.cpp:389-410；gif.cpp:538-548 | Photo/GIF/Video的InBubble/Caption由组件自行测正文宽度，至少200 DIP且受可用宽约束。照片按3/4可见比例减一像素容差决定是否增高到原比例；GIF/视频最多增高4/3。贴纸仍按224边界适配 |
| 贴纸选中 | T/history/view/media/history_view_sticker.cpp:315-345：msgStickerOverlay 按图像 alpha 混色 | selectedSticker 采样宿主 Bitmap 并保留 alpha；透明边缘不生成矩形背景 |
| 媒体角标和控制 | T/ui/chat/chat.style:102-103,384：角标 inset 4，padding 8/2，控制底板 44；video_status.cpp:36-53：13 regular、胶囊；gif.cpp:2711 条件显隐 | GIF 悬停或传输时显示角标；Video 显示时长及播放按钮；media.badge*、actionSize |
| 传输圆环 | L/basic.style:124-125：radialDuration=350ms、radialPeriod=3000ms；L/arc_angles.h:15-16；L/effects/radial_animation.cpp:32-69,79-98 | Photo/GIF/Video/File/Voice共用非线性追赶：from+(target-from)×elapsed/(350+elapsed)，新目标从上次已显示值接续；旋转仍3000ms，最小一度。ReduceMotion/Instant立即取目标；几何使用缓存Circle/Trim路径与共享浮点圆心，避免折线接头加粗及圆底/图标分别吸附造成偏心，见centering.md |
| 相册 | T/ui/chat/chat.style:508-510：上限 430、最小 100、间距 4；T/ui/grouped_layout.cpp:115-178：比例分类与 2/3/4 张拓扑；:438-562：5–10 张裁切比例和分行评分 | content.Album；album_layout.go 通过拓扑约束和分行搜索算矩形，按宿主宽缩放；只有外角继承 bubble 圆角 |
| 文件 | T/ui/chat/chat.style:378-426：普通 padding 12/8/10/8、图标 44、间隔 11、名字 y=12、大小 y=34；thumbnail padding 6/6/10/6、72/间隔 14、名字 y=11、大小 y=31、链接 y=55；悬停 200 ms | content.File；file.*、motion.fileHover；准备好的 Bitmap thumbnail，独立 OnLink |
| 语音 | T/ui/chat/chat.style:429-432：条宽 2、间隔 1、最小/最大高 3/17；document.cpp:233-305：按显示宽度归并样本，按播放位置分双色 | content.Voice；voice.*、message.waveform*；无样本画最小高度占位，宿主提供播放状态及 seek 回调 |

相册的比例阈值、裁切区间、分行评分系数是无单位算法约束，直接记录在 album_layout.go 注释指向的来源中；不把它们解释为间距或字号。空相册和超过 Telegram 单组 10 张的集合由宿主分组，Album 的输入契约为 1–10 张，每张 ID 唯一、尺寸为正。

mw-gaps2：相册逐项状态、进度、下载/取消/重试和只读尺寸通知的画法见 [paint/album-state.md](paint/album-state.md)。Media.StatusText/Progress 是单图与相册共同数据源；AlbumOptions 的四类动作与 OnTileSize 均按 ID 交付。圆底44、弧宽3复用 media.actionSize/progressWidth，额外用途来源 chat.style:511–512。宿主状态胶囊使用 imageBg / msgDateImgFg（lib_ui/ui/colors.palette:28,379）、单行省略和完整辅助描述；组合居中与不透明底板为登记的宿主扩展，覆盖亮图、选中和窄格可读性。

mw-gaps2：VideoOptions.ShowPlay只控制就绪封面的播放标记，空文案不再决定标记或视频裁切规则；点击意图仍交付宿主回调。下载/加载/失败控件优先；规格与来源见 [paint/video.md](paint/video.md)。

绘制/API 层的选择与平台差异见 exceptions.md。组件不拥有媒体获取、解码、播放、消息跳转或链接打开策略。

隐藏文字占位从整段已排版文本的UTF-16光标位置差取宽；该排版保留空白，因此末尾空格不会变成宽度为零的普通OBJ字形。真正零宽的组合/格式字符使用不可见占位。测量只在原文、字体或样式改变时更新；逐rune替换仍可能改变连字与双向重排，差异保留在exceptions.md。

集成验收：13 个 Tester 测试、全库 `go test -count=1 ./...` 与 mygo v0.7.4 vet 通过。83/83 条实际断言路径逐一反转变红；回复留白、回应间距、单图边界、相册间距、seek 回调、代码原文复制和可见选区七项实现变异均被捕获。13 页软件交互 smoke 与原生明暗共 26 张截图完成。剧透占位按字体 ascent/descent 保持基线，揭开后不改变引用重新收起的消息高度；字体与斜体变化使测量缓存失效。原生驱动的导航 key 与页面 key 已分开，截图不再因同名目标歧义中断。
