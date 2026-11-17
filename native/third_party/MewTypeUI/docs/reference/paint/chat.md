# 消息气泡、引用、转发头与系统消息画法规格

## 任务卡与边界

目标：让只读本页的实现者还原消息的可见轮廓、文字层级和相邻关系，不能只把430、16、11等数值填进一个通用盒子。范围为普通文字气泡、分组、发送人名字、回复预览、正文引用、转发头、日期分隔与文字服务消息；媒体内容、底部时间/收据/回应和头像绘法分别沿用 [消息底部](message-bottom.md)、[聊天身份](chat-identity.md)。引用媒体的缩略图位置在本页，媒体下载/解码不属于组件。

基线沿用参考README：tdesktop `250fc73c2f17a6c65327cb66af06375b7872c0ba`、lib_ui `1ff7be77d578aaf8ad17d2fa6e51bae1ee567c1b`。T/为tdesktop的Telegram/SourceFiles，L/为lib_ui/ui，R/为tdesktop的Telegram/Resources/icons。每个来源均是这两个版本，不以64Gram为准。本页只写行为、数学和几何，不移植GPL代码或素材。

信息顺序为：外部日期/未读/主题分隔 → 消息壳 → 发送人名字（或独立via行）→ 转发头 → 回复预览 → 正文 → 底部信息/回应。名字、预览和正文在同一内矩形协商宽度；不是每层各自加11左右留白。普通组合顺序来源 T/history/view/history_view_message.cpp:896–940,2830–2968,6867–6907；额外主题、临时消息及摘要头存在时由宿主提供相应槽位，不将它们误合并成普通转发头。宿主拥有消息ID、发送者/频道、分组边界、方向、本地化文案、资源和展开状态；组件只消费准备好的状态。

参考官方的三条结构：按实测正文和头部共同决定气泡宽度，按发送侧只改变连接角，服务消息按每行宽度产生凹凸连接。分别解决短消息太宽、分组像四角一起变小、系统消息像整块矩形的问题。来源 T/history/view/history_view_message.cpp:1594–1650,6546–6659；T/history/view/history_view_service_message.cpp:327–431。另读CrychicUI的 `chat/bubble.go:15–25`、`chat/message_list.go:30–51,67–69`，借鉴宿主消息/分组与壳的分层；官方900秒边界、名字在壳内和同行时间代替其5分钟分组、外部名字/时间和通用Arrive动效，未复制实现。

## 坐标、字体与数值登记

矩形为 `(x,y,w,h)`，右下边界为x+w/y+h；正数整数除法记q并向零截断。气泡B的局部原点与整行原点不同，尾巴与阴影允许画在B外。DIP对应100%逻辑像素，平台缩放只在最终输出应用一次。LTR公式在所属容器宽W内按 `x'=W−x−w` 镜像，正文双向塑形与出站布局独立。来源 T/ui/chat/message_bubble.cpp:188–204,277–295；L/style/style_core_icon.cpp:205–216。

| 项目 | 数值/公式 | 来源及本库用途 |
|---|---|---|
| 普通最大宽 | 430；内部正文通常最多408 | T/ui/chat/chat.style:14,25；message.maxWidth/padding* |
| msgMinWidth | 160；不是短文字气泡的强制最小宽 | T/ui/chat/chat.style:22；T/history/view/history_view_message.cpp:6667–6671,6733–6743 |
| 外边距左/上/右/下 | 16/6/56/2；相连上边距0 | T/ui/chat/chat.style:52–54；message.margin*/attachedTop |
| 内边距左/上/右/下 | 11/8/11/8 | T/ui/chat/chat.style:25；message.padding* |
| 小/大圆角 | 6/16；小角来自roundRadiusLarge | T/ui/chat/chat.style:326–327；L/basic.style:107；message.radiusSmall/Large |
| 下阴影 | 2；无模糊、无额外四周描边 | T/ui/chat/chat.style:54；T/ui/cached_round_corners.cpp:30–51；message.shadowHeight |
| 头像/步进 | 33/40，图形左沿14 | T/ui/chat/chat.style:23–24,329；具体绘制沿用chat-identity |
| 正文/名字/服务文字 | 13 regular /13 semibold /13 semibold | T/ui/chat/chat.style:15–19；L/basic.style:53–55 |
| 回复外上下/内边距 | 2/2；11/2/6/2 | T/ui/chat/chat.style:63–66,73；quote.reply* |
| 回复缩略图/外边距 | 32；7/4/4/4 | T/ui/chat/chat.style:65–66；quote.preview* |
| 引用公共形状 | radius5、outline3、outlineShift2 | T/chat_helpers/chat_helpers.style:1282–1287；quote.radius/outline* |
| 正文引用留白/段外间隙 | 10/2/20/2；verticalSkip4 | T/chat_helpers/chat_helpers.style:1289–1297；正文引用，不是回复预览 |
| 服务/日期内边距 | 12/3/12/4 | T/ui/chat/chat.style:95；service.padding* |
| 服务/日期外边距 | 10/10/10/2 | T/ui/chat/chat.style:96；service.margin* |

Fn/An为normalFont行高/ascent，Fs/As为semiboldFont，Ft/At为时间字体；它们是实际度量，不等于字号13。默认body行距取字体height，名字/转发头每行Fs。普通文本的行基线为行顶+ascent；显式lineHeight大于字体height时上部增加q((lineHeight−fontHeight)/2)。上/下标、自定义emoji可分别扩大行ascent/descent，不把整个多行正文硬夹成n×Fn。来源 L/text/text.cpp:1721–1781；L/text/text_renderer.cpp:398–405,932–944。

字体族、Qt的Open Sans参考高度归一化、mygo公开 `Font.Metrics()` / `ui.Shape` / `Painter.Glyphs` 和基线补偿按 [dialogs字体度量](dialogs.md#坐标和字体度量)。本页实现以同一个Font完成测量、塑形和绘制；目标行顶给定时，基线绘制取目标行顶+目标ascent，不能按字号比例猜ascent。系统字体advance、CJK回退和抗锯齿差沿用 [现有例外](../exceptions.md)，实际差须另量，合成验收度量不冒称本机Qt实测。

Unicode emoji默认18图形、左右各1的20宽槽，图形y相对行顶为q((Fn−18)/2)并叠加该行的基线调整；自定义emoji按宿主资源和公开垂直度量，不一律缩成13。来源 L/basic.style:59–60；L/text/text_block.cpp:81–84；L/text/text_renderer.cpp:932–944。资源、头像和emoji由宿主提供允许分发的图像，本库不取Telegram素材。

## 宽度、正文与纵向堆叠

### 普通气泡的宽度协商

设行宽W，模式为Normal/Narrow/Wide，R在Narrow为16，其余为56。普通可用宽 `A=W−16−R`；说明/评论根的居中视图用 `A=W−32`。左起点先取右布局的R或左布局的16，有头像再加40。右布局等于out背景且非Wide；Wide仍可用出站底色但不自动右贴边。来源 T/history/view/history_view_message.cpp:6546–6565；T/history/view/history_view_element.cpp:2866–2868。

仅当有头像且存在右侧动作时，可用宽还减 `动作宽+(40−historyFastShareSize)`；不能对所有头像行无条件再扣40。动作宽来自宿主可见操作，来源 T/history/view/history_view_message.cpp:6564–6568,6005–6025；`historyFastShareSize=31`，来自 T/ui/chat/chat.style:495 和 T/chat_helpers/chat_helpers.style:1855。没有此操作的普通文字行保持A；宿主不能把Narrow与头像的可见性条件任意组合，头像的模式资格见 T/history/view/history_view_message.cpp:3848–3905 及chat-identity。

自然宽不是 `max(160,正文宽+22)`。先塑形正文及底部不可见占位，记无宽限制的完整正文宽T；名字、via、转发头、回复、回应等宽度各加一份22，取最大作为自然壳宽M，保留非正文要求的最大值J。来源 T/history/view/history_view_element.cpp:2926–2932；T/history/view/history_view_message.cpp:1520–1544,1594–1650,1679–1681。默认普通文本上限L=430，含monospace或媒体时L取430、其声明需求宽的最大值；显式unlimited-message-width且无可见媒体才取消这个上限。来源 T/history/view/history_view_message.cpp:6712–6720。

初始宽 `b0=min(A,M,L)`，在内宽 `b0−22` 进行真实换行，记实际占用宽U（包含末尾占位）；普通静态纯文字最终 `b=min(b0,max(U+22,J))`。正文宽 `b−22`，再次布局以最终宽为准。不能用完整可用宽铺满每个短气泡，也不能只按正文压缩掉更宽的引用或名字。来源 T/history/view/history_view_message.cpp:6733–6751,6582–6591；T/history/view/history_view_element.cpp:1884–1918。160是部分媒体/过小输入的控制值，普通142宽气泡合法。

非Wide时，出站x在初始起点再加A−b；入站保持左起点；居中说明视图增加q((A−b)/2)。Wide的居中根则在510宽内容带内居中，增量max(q((510−b)/2),0)。来源 T/history/view/history_view_message.cpp:6594–6604。普通长消息在W600时最大430，入站x16，出站x154；有头像入站x56。不要把Wide误画成整个窗口正中。

媒体字幕若允许窄泡且原始正文不超过6行，可选满足≤6行的最窄宽，不能把这个二分协商套给所有普通文本；原始超过6行则保持完整宽。来源 T/history/view/history_view_message.cpp:5634–5692。Markdown article、媒体尺寸和右侧操作的自身画法不在本页新增，壳仅消费它们的已测宽高。

### 内部矩形与高度

普通B=(x,y,b,h)，内矩形左上 `(x+11,y+8)`，宽b−22。常规无媒体时 `h=16+H名字/独立via+H转发+H回复+H正文+H尾部增量`，没有这些头部就加0；名字行不额外加Gap。正文按实体塑形后换行，自然行高/基线前述；允许空行，不能靠字符数估算。来源 T/history/view/history_view_message.cpp:6816–6843,6867–6907。

正文末尾的S×K避让、签名/时间、回应转移最后一行、媒体底部信息归属完全按message-bottom；H尾部增量包括bottomInfoHeight−Ft，不重复给每条消息加一行时间。空Info没有占位；纯回应不造空壳。来源 T/history/view/history_view_message.cpp:6906–6907；[消息底部规格](message-bottom.md)。头部和正文共享同一内容左沿，回复外上下2已经包含在H回复中，不能再加通用5或8。

整行总高为上边距+h+下边距；默认6+h+2，相连为0+h+2。可见填色气泡之间独立组间8、组内2；2 DIP阴影恰落在下方2外留白里，不将阴影高度再计一次。日期、未读条、主题栏和前置服务消息各自在marginTop累加实际高，隐藏消息不保留普通6/2。来源 T/history/view/history_view_message.cpp:1750–1790,6607–6612。

## 分组圆角、尾巴与阴影

### 分组边界

普通历史的相连条件：时间差绝对值严格小于900秒、同发送者、相同topic根、相同临时消息类别/锚定状态且可连接；当前消息前不能有日期、未读、前置服务或forum线程栏。服务/空/推广/隐藏作者的post/guest bot不连接，前条有可见inline键盘也阻断。临时出站还须同接收者；匿名群作者须同rank。Saved/Replies/Verify/imported场景改比较displayFrom、是否forward以及隐藏身份，不仅比较显示名字。来源 T/history/view/history_view_element.cpp:104–105,449–470,2383–2466。Welcome上下文允许忽略时间差；宿主按这些语义提供连接标记，本库不读数据库。

身份分组与气泡连接分开：连着一条无壳媒体时可以仍是同一发送者组，但本侧相应bubble连接标记为false；名字显隐看身份连接，圆角看bubble连接。来源 T/history/view/history_view_element.cpp:2785–2825。只提供一对Group布尔值的现有API需要在实现时表达这一区别，不能为兼容旧API把两者混淆。

### 四角与填色

角顺序为TL/TR/BR/BL。设S=6、L=16，T为尾巴角（本体角半径0并接独立尾巴），P/N分别表示气泡接前/接后：

| 消息位置 | 入站/左布局 | 出站/右布局 | 来源 |
|---|---|---|---|
| 独立P0N0 | L,L,L,T | L,L,T,L | T/history/view/history_view_message.cpp:6615–6649 |
| 首条P0N1 | L,L,L,S | L,L,S,L | 同上 |
| 中间P1N1 | S,L,L,S | L,S,S,L | 同上 |
| 末条P1N0 | S,L,L,T | L,S,T,L | 同上 |

非发送侧两个角始终L。无尾巴时T换L；N为true、媒体自行抑尾、底部是visual-media article、inline键盘、about/评论根均不画尾。inline键盘还将两个底角都改S。来源 T/history/view/history_view_message.cpp:6629–6659。Wide出站按左布局选择角，不能单由底色决定尾巴朝向。可选small-radius设置使L也等于6，重启后生效；来源 T/ui/chat/chat_style_radius.cpp:20–43。

本体是无描边的填色圆弧角多边形，四角分别按表半径裁切，顶/底各补齐剩余的矩形，不因一角6而压扁另一角16；不对每个子片重复叠加半透明底色。抗锯齿在弧线边缘进行。来源 T/ui/chat/message_bubble.cpp:94–187；T/ui/cached_round_corners.cpp:30–51。mygo Windows/Linux圆弧与macOS连续角差异沿用exceptions的平台登记。

### 尾巴及下阴影

官方固定版本尾巴只有灰度PNG，画布6×10，B=(0,0,6,10)、B128=(0,2,6,8)，没有可引用的矢量路径。入站画布 `(x−6,y+h−10,6,10)`，出站 `(x+b,y+h−10,6,10)` 并水平镜像；尾巴不计入b或左右11留白。来源 T/ui/chat/chat_style.style:216–219；T/ui/chat/message_bubble.cpp:188–204,277–295。

本库沿用已登记的原创二次曲线替代：局部从接缝顶部到外侧底部，参数t∈[0,1]的外边界为 `外向距离=6t²，向下距离=10t`，再沿底边回接缝闭合；实际绘制使用最终6×10 DIP路径，不能先栅格化1×1后放大。此式描述现有MIT实现的几何选择，非Telegram路径。既有exceptions记录3倍参考掩模覆盖边界最大差0.847059 DIP；该旧测量不等于本次重新测量，也不代表颜色逐像素相同。来源及边界见 [现有尾巴登记](../chat.md) 和exceptions。

阴影是将同形状的下边缘向下延2 DIP，底圆角下方也延伸，主体遮住上部分；没有blur、侧面整圈阴影或独立border。直段从 `x+底左角半径−左尾宽` 到 `x+b−底右角半径+右尾宽`，y=y+h，高2；尾角半径按0计。无shadowed分片不画，只有到整个气泡底部才恢复底角/阴影。来源 T/ui/chat/message_bubble.cpp:36–54,194–204；T/ui/cached_round_corners.cpp:30–51。尾巴和阴影颜色跟随消息方向及整条选中，避免接缝异色。

## 发送人名字与转发头

名字行在普通群聊入站/频道作者且未身份连接前条时显示；普通本人出站不因有SenderName字段就显示。历史特殊方向与Saved转发由宿主解析；官方完整资格见 T/history/view/history_view_message.cpp:5796–5848,5855–5887。名字13 semibold，行框高Fs，x为内容左沿、y为当前内容顶；只占一行，过长省略，之后正文顶增加Fs。来源 T/history/view/history_view_message.cpp:2589–2618,6867–6873。名字色和头像沿用chat-identity；premium状态槽和via/rank按下述规则测量，不套dialogs会话行的22宽状态槽。

设内容左u/顶v/宽w、semibold空格宽S、rank占宽B。名字行先扣有rank时的11+B；状态资格为premium发送者，或带非空emoji状态且不是当前会话peer的channel。状态槽18（premium的18×16画布），只有剩余宽>18时才预留并绘制状态；via仅在没有转发头且有可见via文字时扣其宽+S。名字可用宽取剩余非负值，实画宽n=min(自然名字宽,可用宽)，状态画布在(u+n,v)，颜色取名字RGB但alpha固定115/255。自定义状态图形20，skip=q((18−20)/2)=−1，原点为(u+n−2skip,v+skip)，因此相对状态槽(2,−1)，返回槽仍18；播放上限2轮，暂停服从宿主。via左沿为u+n+S，有状态对象时再加18+S（即使窄到未画出状态也仍按对象存在加宽），基线v+As。来源 T/history/view/history_view_message.cpp:2507–2581,2608–2627,5541–5549；L/text/text_custom_emoji.cpp:45–46；T/history/view/history_view_message.cpp:86。

rank由宿主给Creator/Admin/User和标签/boost富文本，默认出站及Welcome不显示。标签regular13，管理员/群主的padding5/−1/5/0产生药丸高Fn−1、宽max(标签自然宽+10,Fn−1)，y=v+q((Fs−药丸高)/2)，标签在药丸水平居中、顶y−1；背景rank色alpha.15、半径高/2，普通User只有文字，无常态药丸。boost非空时占宽为2+其自然宽；整体B为标签/药丸宽加此占宽，整体右贴内容右沿，boost文字顶v、右贴u+w。按下rank的药丸遮罩用rank色alpha.1，650/200ms同引用ripple。来源 T/history/view/history_view_message.cpp:513–527,1088–1223,2643–2731；T/ui/chat/chat.style:26–27,958–965；L/colors.palette:692–694。单boost只画16×16图标，多boost画另一16×16图标及十进制计数；图标在emoji原点下移2，advance16，均计入boost自然测宽。来源同文件:1192–1207；L/text/text_utilities.cpp:78–81；L/text/custom_emoji_instance.cpp:972–975。

转发头是同一段本地化富文本，不固定拆成“转发自”一行和作者一行。短时1×Fs，超出可用宽时2×Fs；最多两行，若正常单词换行仍超过两行，允许在任意字符边界省略。普通宽b−22，PSA时另减23；转发头正文位于该段之后，没有通用Gap。来源 T/history/view/history_view_message.cpp:2830–2855,2897–2905,2951,6887–6893；T/ui/chat/chat.style:334–337。普通用msgServiceFg/fwdTextPalette，PSA用boxTextFgGood及对应palette；source/via链接范围分别交宿主，不把整行当一个无区分链接。

官方文案可含原作者头像，18图形、(0,1,3,0)留白形成21宽对象，按emoji行顶定位，右3不再加一次；对象原点为塑形结果，不能假定总在行最左。只有实际文案包含头像实体才占位。来源 T/history/history_item_components.cpp:300–383,386–411；T/ui/chat/chat.style:146；T/data/stickers/data_custom_emoji.cpp:621–646；L/text/custom_emoji_instance.cpp:914–947。宿主位图/首字替代沿用chat-identity，不内嵌参考头像。

PSA问号图标20画布，x为转发段右沿−(−5)−20，即右沿−15，y同段顶；图形可向段右伸5，但不能与正文宽计算共用一个“额外20”。显隐时中心缩放及alpha同一进度，200ms linear；打开tooltip时隐藏，关闭恢复，MediaEditor不画。来源 T/history/view/history_view_message.cpp:2925–2946,4557–4569；T/ui/chat/chat.style:334–337；L/basic.style:101。

## 回复预览与正文引用

### 回复预览框

回复预览是泡内独立可点击引用，位置在名字/转发头之后。令分配矩形 `(u,v,w,H)`，外顶/底各2；可见底板 `(u,v+2,w,H−4)`，圆角5，左条3与底板轮廓共同裁切。普通回复没有右上引号；只有可显示的非空manualQuote使用mini_quote。来源 T/history/view/history_view_reply.cpp:418–421,797–830,848–883；T/chat_helpers/chat_helpers.style:1282–1297。旧msgReplyBarPos/Size的2×36声明不能替代当前3宽条与动态高度。

普通内留白左/上/右/下=11/2/6/2。无缩略图、单行名字+单行摘要时，底板高 `2+Fs+Fn+2`，分配高 `H=2+(2+Fs+Fn+2)+2`。名字行顶v+4，摘要行顶v+4+Fs；二者基线加各自As/An。来源 T/history/view/history_view_reply.cpp:702–732,746–799,929–932,1024–1053。

有缩略图时预留 `p=3+7+32+4−11=35`。图像在 `(u+7,v+2+4,32,32)`，cover裁切和RoundSmall；名字x=u+11+p，摘要首行同此x，之后行回到u+11。名字可用宽 `w−11−6−p−nameIconsSkip`；p在整体宽w≤11+p时清零并隐藏缩略图。图片右边缘u+39，名字起点u+46，实际间隙7，不能用一个预览图列让整段多行摘要永远缩进35。来源 T/history/view/history_view_reply.cpp:749–775,914–925,978–993,1010–1053。

多行回复：在宽w小于自然最大宽且multiline时，名字可1或2行；先在扣p和图标的名字宽测高度，再决定是否2行。名字两行时摘要首行取消p，名字一行时摘要只首行扣p。收起最多5行摘要，第5行省略；这不是正文引用的3行。摘要高至少Fn，总高为外4+内4+名字行高+实际摘要高。宽≥自然最大宽时使用最优排版与1行名字；`maxWidth`同样包含缩略图、名字/摘要和左右11/6。来源 T/history/view/history_view_reply.cpp:702–732,746–815；收起常量 T/history/view/history_view_reply.cpp:46。展开只有确实被截断且尚未展开时生效，先展开再导航；正常单行不会被“有Multiline字段”强制留5行。来源同文件:493–498,758–788。

loading/deleted/story不可用文案由宿主给出；没有显示内容时按msgDateFont在x=u+11、文本顶y=v+4+q(Ft/2)处画单行状态，不伪造作者与摘要；目标基线为该顶点+At。来源 T/history/view/history_view_reply.cpp:712–724,1064–1076。选中、方向与来源作者颜色决定底板/条/文字，见下节；没有统一减透明度的禁用态。

### 正文引用块

正文实体引用与回复预览分别布局。先用带引用样式的无宽限塑形取得该引用自身自然最大宽Q（包括引用自己的留白、图标与最小轮廓宽），实际框宽 `w引用=min(w段落,Q)`，文字可用宽为w引用−10−20；测量、换行、底板及右侧引号都用同一w引用，不能因相邻普通正文较长而把短引用铺满。Q的自然测量按各行advance/bearing取得：行宽从引用最小宽M起算，再累加该行塑形单词宽和相邻bearing/padding，取所有中间宽及行末宽的最大值后向上取整。M取左右文字留白、header及图标需求的宽，与两倍最大角/图标包围尺寸的较大者；普通无header、16×12引号及(4,4)位置时M=max(10+20,2×max(5,3,20,16))=40。来源 L/text/text.cpp:613–639,663–689,713–719,1663–1690；实际宽限制及对齐来源同文件:1214–1223、L/text/text_renderer.cpp:631–652。水平居中时整体移q((w段落−w引用)/2)；未居中时，仅左对齐且引用段RTL、或右对齐且引用段LTR的组合移动w段落−w引用，其余移0，文字与背景共同移动。

普通blockquote文字左10、右20；上下内2、段外各4，所以正文排版的外部总留白为top6/bottom6，底板在段顶部+4至段底部−4之间，文字顶为段顶+6。n行普通文字总流高 `6+n×Fn+6`，底板高 `2+n×Fn+2`；首尾非引用段落不再重复加一份通用Gap。来源 T/chat_helpers/chat_helpers.style:1282–1297；L/text/text.cpp:1653–1660；L/text/text_renderer.cpp:408–448。

半径5、左条3，mini_quote16×12画布在右上内偏移(4,4)，即底板右沿−20、顶+4；收起实体最多3行，底部箭头12×8在右下内偏移(6,4)，展开时竖直镜像，只有可折叠实体才显示。展开/收起重新测量段落，普通引用不默认截断成3行。来源 L/text/text_renderer.h:17；L/text/text.cpp:1815–1818；L/text/text.cpp:325–335,471–487。正文中的inline链接/剧透/代码属于实体排版，不将右侧引号算作文字rune；底部S×K仍由最后可见内容承担。

左条不是完整圆角矩形的border。纯色条宽3，顶/底按radius5外轮廓裁切，其余底色区域从x+3开始，避免透明叠加变深。若宿主提供多色作者，条使用2或3色斜带：两色周期12、三色18（3×4或3×6），outlineShift2固定其纵相位；换行分片不能重新从相位0开始。来源 L/text/text.cpp:247–324,385–470。多色/collectible及背景emoji的调色资源由宿主提供；既有通用Quote色是单色映射，不能标成已实现全部上游配色。

代码围栏的形状同5/3/4，左右留白继承普通historyQuoteStyle的10/4；顶部header20，copy16×16画布在右上偏移(4,2)，文字顶相对流顶=4+20+2=26。暗色代码背景可覆盖为黑色alpha192/255，不把这个覆盖套给普通引用。来源 T/chat_helpers/chat_helpers.style:1299–1305；L/text/text.cpp:1653–1660；T/ui/chat/chat_style.cpp:785–793。代码的语法高亮和横向滚动不在本页重新定义。

## 日期分隔与服务消息

### 日期胶囊

日期使用13 semibold，设自然文字宽D、实际Fs/As。普通有效宽V=W，Wide为min(W,542)，542=430+2×40+2×16；在V带内居中，不在额外大窗口宽内居中。胶囊宽D+24、高Fs+7，x=10+q((V−20−D−24)/2)，y=日期块起点+10。文字左x+12，基线y+3+As。外部总高 `10+3+Fs+4+2=Fs+19`，半径q((Fs+7)/2)。来源 T/history/view/history_view_service_message.cpp:149–187,214–216；T/history/view/history_view_element.cpp:827–846；T/ui/chat/chat_style.cpp:94–100。

第一个可见非空/非推广消息和本地日期改变时显示；前条隐藏日期时也可重新显示；当前hideDisplayDate强制隐藏。按实际本地日比较，不用UTC字符串、时间差24小时或每条强插。日期栏还阻断当前与前条连接。来源 T/history/view/history_view_element.cpp:2676–2727,2396–2399。日期没有尾巴、阴影、额外描边，也不占发送侧头像列。

### 多行服务消息轮廓

服务文字同13 semibold、居中，每行独立宽度；W有效带和Wide542同上，两边外10、内12，文字可用宽 `max(V−44,1)`。单行自然宽足够时胶囊仅按文字+24，不铺满整条；总高为10+7+实际文字高+2。图文服务的媒体/回应另测量并加相应间隔，不改变这里文字轮廓公式。来源 T/history/view/history_view_service_message.cpp:470–481,500–559,562–579,700–737。

令F=Fs，外圆半径 `R=q((F+7)/2)`，凹圆半径 `r=F−R`，内收 `s=q(2r/3)`，能分出独立台阶的最小宽差 `d=2(R+r−s)`。来源 T/ui/chat/chat_style.cpp:94–115；T/history/view/history_view_service_message.cpp:392–404。F18时R12/r6/s4/d28，不能统一以6作为服务胶囊半径。

先取得居中塑形后的各行自然宽wi，非负化；相邻宽差绝对值严格小于d时，将较窄者扩大为较宽者，并反复向相邻处传播直到稳定。差正好d不合并。只是改变背景轮廓宽，文字保持自身居中塑形；不用空格填满。来源 T/history/view/history_view_service_message.cpp:392–431。

每行外宽bi=wi+24，相对有效带x=10+q((V−20−bi)/2)。背景从y10开始分段，各行顶/底端类型按下表决定；凸指半径R向内裁去外角，凹指半径r向外连接相邻更宽段，平指直边无圆角。

| 关系 | 本行底端 | 下一行顶端 | 来源 |
|---|---|---|---|
| 下一行更宽 | 凹 | 凸 | T/history/view/history_view_service_message.cpp:339–352,382–388 |
| 下一行更窄 | 凸 | 凹 | 同上 |
| 两行同宽 | 平 | 平 | 同上 |
| 首/末行 | 首顶凸 | 末底凸 | 同文件:338,350–351 |

每段分配高 `hi=F+3×[顶凸]+4×[底凸]−4×[顶凹]−3×[底凹]`，段起点yi=10+前面hi之和；这是轮廓切片，不是文字行高。任一端凹时将本段核心左右各内收s；若上一段已内收且bi未变，本段继续内收。来源 T/history/view/history_view_service_message.cpp:354–380,107–146。凸端占核心内部R高，凹端小弧在核心左外r/右外r处，底凹位于yi+hi−r，顶凹位于yi；平段填满核心直边。顶凹的左右角方向交换，连接不能画成尖折线。来源同文件:67–146。

按上述核心与圆弧构造一次轮廓或互不覆盖的填片，半透明底色只合成一次；不能把每行独立圆角矩形重叠，交叠会变深、内凹消失。文字第i行顶为块顶+13+iF，基线再加As（无特殊实体度量时）。来源同文件:700–737；背景高总和=实际文本高+7。选中不将整个服务胶囊换为msgServiceBgSelected：底板仍用msgServiceBg，文字选择通过serviceTextPalette的selectOverlay填选区。来源同文件:312–324,648,705–737；T/ui/chat/chat_style.style:10–20。

## 图标画布、可见框与替代

逐像素量固定版本@1x PNG，B为灰度≥1、B128为≥128，矩形为 `(x,y,w,h)`。这些黑底白图是覆盖率掩模，不能用全不透明alpha取图形框；染色读取首字节，来源 L/style/style_core.cpp:90–100,124–136。下表不分发素材，替代图形采用既有Lucide 1.58.0（ISC/Feather MIT）及原创尾巴。每张替代先量笔画外框，再缩放/平移至B；不把16/20画布直接填满，不把Lucide24当最终布局宽。图形内部及圆头差沿用exceptions登记。

| R/路径 | 画布 | B / B128 | 替代/放置及来源 |
|---|---|---|---|
| bubble_tail.png | 6×10 | (0,0,6,10)/(0,2,6,8) | 原创二次曲线，本页尾巴；T/ui/chat/chat_style.style:216–219 |
| chat/mini_quote.png | 16×12 | (3,2,10,8)/(4,3,8,6) | Quote可见10×8；正文框右20/顶4的画布；T/chat_helpers/chat_helpers.style:1292–1293 |
| chat/mini_copy.png | 16×16 | (3,2,10,12)/(4,3,9,11) | Copy可见10×12；右20/顶2画布；同:1303–1304 |
| intro_country_dropdown.png | 12×8 | (0,1,12,7)/(1,1,10,6) | ChevronDown可见12×7，向上镜像；同:1294–1297 |
| message_psa_tooltip.png | 20×20 | (1,1,18,18)/同 | CircleHelp可见18×18；T/ui/chat/chat_style.style:116–118,220 |
| chat/reply_type_user.png | 12×12 | (1,2,10,9)/(1,2,9,9) | UserRound可见10×9；上4/右4对象留白；T/ui/chat/chat.style:74–77 |
| chat/reply_type_group.png | 16×12 | (1,2,14,9)/同 | Users可见14×9；上4/右4；同:78–81 |
| chat/reply_type_channel.png | 12×12 | (0,0,11,11)/(1,0,10,11) | Megaphone可见11×11；上5/右4；同:82–85 |
| mini_forward.png | 16×16 | (2,3,12,10)/同 | Forward可见12×10；上2/右2；同:86–89 |
| stories/boost_mini.png | 16×16 | (4,1,9,14)/(4,2,8,12) | Zap可见9×14；emoji下移2；T/ui/chat/chat.style:958–961 |
| stories/boosts_mini.png | 16×16 | (3,3,10,10)/(4,3,8,10) | Zap可见10×10；emoji下移2，后接计数；同:962–965 |

reply_type_*类型标记只用于跨会话回复名字的已塑形对象，不能给每个名字无条件加图标。mini_forward则在originalNameAdded成立时插入回复名字富文本，顺序为当前显示名字、空格、forward对象、原作者名字，之后可追加via用户名；advance=0+16+2=18，图形在emoji原点下移2。此18及两个名字一起参与名字自然宽、两行判断和省略，摘要不承担该对象。来源 T/history/view/history_view_reply.cpp:653–697,1024–1030；T/ui/chat/chat.style:86–89；L/text/text_utilities.cpp:78–81。发送收据、时间旁pin/浏览/回复图标见message-bottom表，名字premium图形的18×16画布/B见dialogs表、状态定位按本页名字规则。宿主位图头像/emoji按实际图像可见框单列，不把字体字形截图当PNG掩模实测。

## 状态、颜色与动画

| 区域/状态 | 规则 | 来源 |
|---|---|---|
| 入站正常/整条选中 | msgInBg(windowBg)/msgInBgSelected(#c2dcf2)；阴影#748ea229/#548dbb29 | L/colors.palette:349–360；T/ui/chat/chat_style.cpp:250–264 |
| 出站正常/整条选中 | msgOutBg(#effdde)/msgOutBgSelected(#b7dbdb)；阴影#3ac3461d/#37a78d22 | L/colors.palette:351–362 |
| 正文、链接、名字 | 正文historyTextIn/OutFg及Selected，链接独立角色；名字按宿主peer色，不把所有头部染成正文黑 | L/colors.palette:260–267,292–315；chat-identity |
| 回复/正文引用 | 默认方向replyBar色生成背景alpha.12、条alpha.9；图标保留来源name色原alpha，只有代码pre缓存另用alpha.6；选中改取对应selected角色，来源颜色/多色优先见正文 | T/ui/chat/chat_style.cpp:37–48,119–138,774–793；T/ui/chat/chat_style.h:56–60；L/colors.palette:370–374 |
| 服务/日期 | 正常msgServiceBg基础#517c417f、文字白；服务选择为文字选区，底板不整体重染 | L/colors.palette:367–369；T/history/view/history_view_service_message.cpp:705–737 |
| hover | 普通壳不增加通用hover底或缩放；文字链接按自身palette/链接范围显示 | T/history/view/history_view_message.cpp:1793–1975,2830–2855 |
| 按下回复 | radius5内裁切ripple，650ms easeOutQuint扩散，松开200ms linear消退；颜色为该引用cache.bg | T/history/view/history_view_reply.cpp:878–909,1080–1102；L/widgets/widgets.style:705–706 |
| 按下名字/转发链接 | 按实际文字链接轮廓裁切，名字padding3/1/3/1、radius4，转发同名字路径；前景alpha.1，不全泡闪底 | T/history/view/history_view_message.cpp:2856–2919；T/ui/chat/chat.style:147–150 |
| 暗色 | 同名night角色覆盖/继承；几何与行数规则不变。服务底色可受宿主墙纸调整 | T/ui/chat/chat_style.cpp:762–793；既有theme-colors和exceptions墙纸登记 |

easeOutQuint归一化为1−(1−t)^5，linear=t；按下从实际指针原点扩张至最远角，释放alpha独立消退，不把650ms当完整按压寿命。来源 L/effects/animation_value.cpp:75–84；L/effects/ripple_animation.cpp:59–86,102–155。选中/hover分支没有额外整泡120ms颜色插值；方向、圆角和高度变化也不自动播放CrychicUI入场。新消息显式空间展开150ms，当前聊天easeOutCirc、另一列表easeOutQuint的已有Reveal合同沿用docs/reference/chat.md，不因虚拟列表重新挂载而重播。来源 T/history/history_widget.cpp:8899–8958；T/history/view/history_view_list_widget.cpp:3091–3093；T/ui/chat/chat.style:58。

日期浮层的1000ms停留/200ms淡出、跨日3000ms停留和视口推走属于既有FloatingDate合同，普通静态日期不套该计时。来源 T/ui/chat/chat.style:312–313；T/history/history_inner_widget.cpp:142–143,1154–1162,4709–4767。ReduceMotion和系统字号扩展按已有exceptions；不增添没有来源的spring或disabled淡底。

## 可脚本校验的几何表

以下验收输入为**我定的合成度量**：Fn=Fs=Ft=18，An=As=At=14，所有标注的文字宽为已塑形结果；不把中文字符数当测宽。无媒体、无主题/前置栏、无回应，Info默认隐藏，以便只量本页壳。真实字体另代入同一公式。每个矩形相对整行，必要时额外注明子框原点。

| 输入 | 应有矩形/输出 | 公式依据 |
|---|---|---|
| W600，Normal入站，正文120宽单行，无名字 | B(16,6,142,34)，正文(27,14,120,18)，基线28，行高42 | 本页宽度/堆叠；T/history/view/history_view_message.cpp:6733–6751 |
| 同上出站 | B(442,6,142,34)，右沿584，尾画布(584,30,6,10)，阴影底42 | 同文件:6546–6607；T/ui/chat/message_bubble.cpp:188–204 |
| W600，长正文撑满430 | 入站B.x16，出站B.x154；内容宽408 | 同文件:6712–6720 |
| W360，Narrow无头像，长正文 | 可用壳宽328，内宽306 | 同文件:6699–6706 |
| 加组首名字80宽，无其他头部 | B(16,6,142,52)，名字框(27,14,120,18)/基线28，正文顶32/基线46，行高60 | 同文件:6867–6873 |
| 组内无名字单行 | B.y0/H34，行高36；前条B底至本条B顶2；跨组8 | 同文件:1750–1790 |
| P1N1，左布局 | TL6/TR16/BR16/BL6，无尾；右布局镜像 | 同文件:6615–6659 |
| B宽202，回复预览+单行正文，有缩略图，名字/摘要都一行 | B(16,6,202,78)，内x27/w180；回复分配(27,14,180,44)，底板(27,16,180,40)，预览(34,20,32,32)，名字x73/top18/基线32；摘要首行x73/top36/基线50；正文top58/基线72，行高86 | T/history/view/history_view_reply.cpp:746–799,914–1053 |
| 同回复多行摘要，名字一行 | 摘要第2行x38，首行x73；名字变2行时摘要第1行即x38；宽w≤46时缩略图隐藏 | 同文件:749–794,914–925 |
| 正文引用3行、段流顶0 | 流高66，底板y4/H58，文字y6/24/42、基线20/38/56；展开后按实际行数增高，普通引用不截断 | L/text/text.cpp:1653–1660,1815–1818 |
| 三行短引用与长正文，w段落300/Q140，引用左沿0/流顶0 | 底板(0,4,140,58)，文本内宽110；引号画布(120,8,16,12)，不能铺成300宽；居中引用时全体x加80 | L/text/text.cpp:1214–1223；L/text/text_renderer.cpp:631–652 |
| 回复名字含当前名字40/空格4/forward18/原名50，摘要自然宽≤112，无预览/其他图标 | 自然名字宽112；回复自然宽129；摘要不多留18 | T/history/view/history_view_reply.cpp:680–711,726–728 |
| 名字内容u27/v14/w180，名字40、S4，有状态、无via/rank | 名字允许162；状态画布(67,14,18,16)、custom20图形原点(69,13)，alpha115/255 | T/history/view/history_view_message.cpp:2533–2581 |
| 同内容有Creator标签自然宽24，无状态/boost | 药丸宽34/高17，原点(173,14)、半径8.5；文字x178/top13；名字先扣45 | T/history/view/history_view_message.cpp:513–527,2511–2516,2643–2694 |
| 日期W400/D80 | 胶囊(148,10,104,25)，文字x160/基线27；总高37，radius12 | T/history/view/history_view_service_message.cpp:149–187 |
| 日期W900/Wide/D80 | 有效带542，胶囊x219，禁止居中到整窗x398 | 同文件:159–165,214–216 |
| 服务W400，1行文字100宽 | 胶囊(138,10,124,25)，文字x150/top13/基线27，总高37 | 同文件:500–539,700–737 |
| 服务W400，3行宽100/90/160 | 平滑后100/100/160；轮廓段外框(138,10,124,21)/(138,31,124,15)/(108,46,184,25)；中段内收后核心(142,31,116,15)，底凹y40；轮廓底71、行总高73 | 同文件:327–431 |
| 服务宽100/128与100/127 | 差28不合并；差27合并为127/127；文字原自然宽不变 | 同文件:402–428 |
| attach时间差899/900秒 | 其余资格相同时899连接、900不连接；同名不同ID不连，跨日期不连 | T/history/view/history_view_element.cpp:2383–2466,2676–2727 |

颜色/hover/Selected/night切换不改变上述名义矩形；125/150%只转物理像素不重复乘。尾巴之外的轮廓逐角量半径、尾巴测覆盖边界、文字测基线、服务段测凹角位置。实现前后分别导出 `(case,part,x,y,w,h,baseline)`，列Δx/Δy/Δw/Δh并与≤1 DIP目标比较；不能用换短名字或减少正文行数掩盖误差。给人的比较采用轮廓叠图及左右拼接/滑动分割，禁止反色闪烁。

## 实现接手与验收边界

1. `chat/bubble.go` 的壳宽协商要同时考虑文字、名字、引用、转发和尾部占位；`Row` 应表达Normal/Narrow/Wide与身份/气泡两种连接，避免把颜色方向当布局方向。保留消息ID身份，不增加组件I/O。
2. `content/quote.go` 的现有整列缩略图布局与官方“仅摘要首行让图”不同；需按本页外2/内2、缩略图(7,4)、5行折叠与实际名字1/2行重做测量，状态行不保留假作者。转发头保留本地化单段、两行上限和可选内联头像。
3. 校准mini_quote/copy/chevron/PSA/type等替代图标至B，而不是填满画布；阴影保持无模糊的下沿2。现有尾巴为原创且已有≤1 DIP覆盖边界登记，可沿用，不为本轮文档任务重写Go。
4. `chat/service.go` 的Selected当前换整块底色，实施时改为官方文字选区；日期与服务消息分开单行胶囊/多行凹凸轮廓，Wide有效带542须独立传入。核对 `chat/service_contour.go` 的内收传播与差正好d边界。
5. theme生成表登记缺少的专用用途：historyReplyTop/Bottom2、historyReplyPadding11/2/6/2、PreviewMargin7/4/4/4、正文quote的verticalSkip4和三/五行不同限制、fwd头像padding0/1/3/0、PSA23与(−5,0)。已有消息padding/radius/阴影、quote3/5和service留白复用；不得为了对齐写未登记字面量。声明来源全在上表及对应条目。
6. 实现验收先补缺失的全画廊TestEveryEntry，每页明暗打开并稳定；在layout/chat组合页用3–6条中文全名、长正文/长名字、组首中末/跨日、回复/转发/状态行、服务3行与空列表，真实点按引用展开/导航、选择文字、hover和快速切换。新几何断言逐个改坏确认失败，重计算按共享锁和低优先级；另提供官方对照图、各区改前/改后误差与实际运行性能。

本次只修改规格与README组件表，未改Go/主题/画廊；@1x表由灰度测量得到，几何表由算式烟测验证。没有Qt原生截图、mygo UI运行或性能测量，不将文档检查标为实现通过。适用界面准则C01/C02/C07/C08/C12/B01落实为尺寸、基线、素材、变体与结构合同；真实UI与IME、动效、系统字体墨迹仍待实现验收。本页“我定的”仅为符号、合成验收输入和既有MIT替代图形的表达，平台/素材/墙纸差异沿用exceptions；未新增未登记的视觉例外。
