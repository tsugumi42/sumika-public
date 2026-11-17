# 会话列表、搜索栏与导航栏画法规格

基线：tdesktop `250fc73c2f17a6c65327cb66af06375b7872c0ba`、lib_ui `1ff7be77d578aaf8ad17d2fa6e51bae1ee567c1b`。T=tdesktop/Telegram/SourceFiles，L=lib_ui/ui，R=tdesktop/Telegram/Resources/icons。下列引用都是这两个版本。只记录形状、数学关系、状态及测量结果，不摘录或译写 GPL 代码；实现者只读本页、既有规格及本库/mygo API。

## 任务卡与边界

- 用户目标：扫读会话身份、最后消息和未读状态，搜索会话，用文件夹切换列表；外观依据官方桌面版。
- 信息架构：左侧 rail（固定主菜单入口＋可滚动文件夹）→ 会话栏（搜索顶栏＋会话行）。主菜单入口不属于文件夹列表、不随列表滚动；这里不定义账户抽屉内容。依据 T/window/window_filters_menu.cpp:120–153。
- 归属：宿主提供稳定 ID、已格式化时间/计数、名字/预览富文本、头像资源、当前项、静音/草稿/发送状态、文件夹顺序及搜索范围；组件只测量、绘制和报告操作。没有新增配置编辑面。静音图标开关是显示策略，不由“静音”一个字段隐式开启（T/dialogs/ui/dialogs_layout.cpp:56–65,935–979）。
- 适用范围：普通/窄行、标签/论坛的布局变体，搜索框及其外层顶栏，rail 的三种模式、主菜单和角标。故事环、TTL/订阅/群通话动画的既有取证见 [dialogs 对照](../dialogs.md#剩余状态的源码取证2026-11-03)；不重新定义那些独立图形。图标与字体的已知替代限制沿用 [例外](../exceptions.md#dialogs)。
- 验收以默认主题、100%文字缩放的 DIP 几何为基准，允许误差≤1 DIP；125%屏幕缩放另记物理像素。颜色、文字放大、窄窗、按下和快速切换分别验收；不能把字体墨迹当布局框。

参考了官方按“头像/两条文字带/右端独立预留”的组织（T/dialogs/ui/dialogs_layout.cpp:552–568,825–864,1194–1246），用于避免把时间和徽标塞进一个通用 flex 列；参考 L/widgets/side_bar_button.cpp:153–219 的多行标签增高和角标切洞。另读 CrychicUI `chat/conversation_list.go:33–60`：仅借鉴身份、预览、状态分层的可读结构；不采用其圆角行、Accent 淡底、按下标身份、字阶比例或入场动画，未复制其实现。

## 坐标和字体度量

矩形统一 `(x,y,w,h)`，右/下边界是 `x+w/y+h`。以下为 LTR；镜像时在所属容器宽 W 内将矩形变为 `(W−x−w,y,w,h)`，字体塑形方向独立。图标样式偏移属于画布留白，不能在镜像后再加一次。依据 L/style/style_core_icon.cpp:205–216。

| 符号 | 含义 / 来源 |
|---|---|
| W、H | 行宽、行高，行局部原点在左上；T/dialogs/ui/dialogs_layout.cpp:1249–1253 |
| Fn/An/Dn | normalFont 的整数行高/ascent/descent；Dn=Fn−An |
| Fs/As | semiboldFont 行高/ascent；不是字号13 |
| Fb/Ab | dialogsUnreadFont 行高/ascent；不是字号12 |
| N、D、C | 已塑形名字、时间、未读字符串的自然宽度；不能由字符数乘字号 |
| q(a/b) | C++ 整数商向零截断；正数时等于 floor；半像素的绘制公式另明确保留浮点 |

Telegram 默认族是 Open Sans，而不是 Windows 系统 UI 字体；自选/系统族会先按 Open Sans 的小写字形高度调整字号并保持参考 ascent，再用真实全字符紧框调整 height。声明13并不保证最终物理字号13。度量最终取整为 height、ascent，descent 为二者差。来源 L/style/style_core_font.cpp:166–183,216–301,304–335,424–442。normal/semibold 为13，时间13 regular，徽标12 bold，rail 标签11 semibold、角标12 semibold；来源 L/basic.style:53–55，T/dialogs/dialogs.style:78–88，T/window/window.style:235–240。

mygo v0.7.4 的 MeasureText 只返回w/h（`ui/richtext.go:243–255`），但公开 `ui.Font.Metrics()` 可取Ascent/Descent/LineGap（`ui/glyphs.go:10–56`），`ui.Shape`＋`Painter.Glyphs` 可按明确基线绘制并缓存塑形结果（同文件:77–95）。实现必须使用与Span相同的族、字号、字重、语言构造Font，先取度量再按上表目标基线绘制；行顶绘制入口的补偿是 `目标基线−原行顶−实际Ascent`，不是固定“向下1”。普通名字/预览保留10/34行顶，时间/徽标按目标公式协调各字阶基线；不能用字号乘比例猜ascent。系统字体与Qt/Open Sans的advance、CJK回退、墨迹和取整仍不相同，已有exceptions.md的系统字体差异继续适用，但其“MeasureText未公开ascent”不表示整个mygo没有度量API，实施时应更正这条旧限制。表中Qt整数度量与mygo浮点度量分别代入报告差值，不为默认族差异缩小全部文字。

普通文本默认行高为字体 height，未指定固定 lineHeight；T/dialogs/dialogs.style:86–87，L/basic.style:84–88，L/text/text.cpp:2264–2269。Unicode emoji 是18图形＋左右各1的20宽行内槽，竖向按字体行高居中，不随13字号缩成13图形；来源 L/basic.style:59–60、L/text/text_block.cpp:81–84、L/text/text_renderer.cpp:932–944。资源由宿主提供允许分发的图片；系统彩色字形替代见既有素材/字体例外，不能承诺 emoji 墨迹与原版一致。

## 行框与两条文字带

| 项目 | 几何与绘制 | 来源 / 现有主题用途 |
|---|---|---|
| 整行 | `(0,0,W,62)`，实色矩形、无边框、无行间gap、无独立圆角卡片 | T/dialogs/dialogs.style:91–103；T/dialogs/ui/dialogs_layout.cpp:468–492 → conversation.rowHeight/background |
| 头像 | `(10,8,46,46)`，普通身份圆形裁切；头像资源不参与文字宽度协商 | T/dialogs/dialogs.style:95–103；T/dialogs/ui/dialogs_video_userpic.cpp:135–172 → conversation.avatarSize/padding* |
| 标题初始带 | `(68,10,W−78,Fs)`；预览初始带 `(68,34,W−78,Fn)` | T/dialogs/ui/dialogs_layout.cpp:552–568,1194–1212 → titleLeft/Top、previewLeft/Top |
| 名字 | 13 semibold，单行末端省略；行顶10、基线 `10+As` | T/dialogs/ui/dialogs_layout.cpp:866,955–964；L/basic.style:55 |
| 时间 | 框右沿 `W−10`，宽 D；基线 `10+Fs−Dn`，故行顶 `10+Fs−Dn−An` | T/dialogs/ui/dialogs_layout.cpp:87–106；T/dialogs/dialogs.style:88–89 |
| 时间让位 | 标题右端先减 `D+5`；无时间绘制分支则不减；不是预览也减D | T/dialogs/ui/dialogs_layout.cpp:96,665–670,807–809 |
| 预览 | 在右端徽标预留后的一条文字带内塑形、省略；服务发送者/活动前景和普通摘要前景分开 | T/dialogs/ui/dialogs_layout.cpp:1194–1246；T/dialogs/ui/dialogs_message_view.cpp:420–437,532–545 |
| 窄行 | 只画头像与必要角标，隐藏两条文字带；默认宽 `10+46+10=66` | T/dialogs/ui/dialogs_layout.cpp:304–319,556–560；T/window/window_session_controller.cpp:2754；既有 CompactWidth |
| 标签行 | H=72，预览top30，标签top52，其他默认值继承；标签从x68逐项按实际宽＋4前进 | T/dialogs/dialogs.style:114–118,158–160；T/dialogs/ui/dialogs_layout.cpp:1009–1015 |
| 论坛行 | H=80，预览区top32、先21高话题带，摘要top53；带标签H96、tag top77 | T/dialogs/dialogs.style:119–128；T/dialogs/ui/dialogs_layout.cpp:1243–1246；T/dialogs/ui/dialogs_message_view.cpp:448–451 |

名字左端类型图标占位按“含样式偏移的图标宽＋3”计算，不按可见墨迹宽；预览仍从68开始。群组/机器人样式宽分别17，频道15，论坛20，故名字左沿分别88、86、91；图形画布位置见素材表。依据 T/dialogs/ui/dialogs_layout.cpp:600–605,1086–1120；L/style/style_core_icon.cpp:185–202,205–216。置顶/未读不改变标题左沿。

头像无图/加载中/失败共用宿主首字回退：D=46时字阶 `q(D×13/33)=18` semibold，文字行框在46正方形内居中，背景从上到下取该peer两个userpic palette色，无描边，白色historyPeerUserpicFg。来源 T/ui/empty_userpic.cpp:291–344；T/ui/chat/chat.style:332。现有本库dialogs头像20/600是已登记例外，不应误写成官方18；宿主预制位图可保持内容差异。普通圆形、forum按 `q(D×.3)` 半径的圆角正方形裁切；图像缓存先按身份形状处理，再放入同一46框。T/data/data_peer.cpp:438–461；T/ui/userpic_view.cpp:34–36,155–191。故事环使图像在原头像框内围绕中心缩小，上述默认46不是故事环内实际图片边长；该独立变体沿用既有dialogs.md来源。

标题右端依次扣时间、20发送状态槽、名字后徽记和可选静音。普通无徽记时，令 `E=W−10−(有时间?D+5:0)−(有收据?20:0)`，名字允许宽 `E−标题左沿−(绘静音?12:0)`。发送状态画布原点是 `(E,10)` 再加样式偏移；不是紧贴时间字符串的右边。已发送未读为单勾、已读双勾；发送中和失败均走时钟，草稿保存中也走时钟，关闭话题的锁优先。T/dialogs/ui/dialogs_layout.cpp:825–864。认证/premium/scam 的尺寸协商属于名字后徽记，不能把徽记挤到时间区；调用关系 T/dialogs/ui/dialogs_layout.cpp:867–954，具体徽记布局 T/ui/unread_badge.cpp:238–404。

认证或premium静态徽记均占18宽：画布原点 `(标题左沿+min(N,当前允许宽−18),10)`，名字允许宽再减18；它们不是20宽的botVerified专用前置标记。scam/fake/direct文字徽记先按9 semibold测短标签，宽=文字宽+4，高=该字体行高，放在 `x=标题左沿+min(N+4,当前允许宽−徽记宽)`、`y=10+q((Fs−徽记高)/2)`；返回预留宽=徽记宽+4。优先文字徽记，其后按verified/emojiStatus/premium条件决定静态图形；默认该调用未要求同时画两个。来源 T/ui/unread_badge.cpp:238–340,386–394；T/dialogs/dialogs.style:491–494。状态emoji槽18，实际自定义emoji边20、skip=−1，绘制原点相对槽(2,−1)、返回宽22，不能用静态18预留替代。来源 T/ui/unread_badge.cpp:343–383；L/text/text_custom_emoji.cpp:45–46。

徽记选择的可执行顺序：scam/fake（且允许scam角色）或monoforum的direct标签优先；否则令E为premiumBadgesShown且提供premium样式，状态emoji资格为 `E且状态ID非零且(peer premium或channel)且!hideEmojiStatus`。本调用prioritizeVerification/bothVerifyAndStatus均false，所以有资格状态emoji优先，其次verified认证，再次E且premium的静态星，无条件满足则不画。来源 T/ui/unread_badge.cpp:241–289、T/ui/unread_badge.h:65–66。文字徽记为无填充的1 DIP描边圆角框、半径2、抗锯齿；描边中心线就是名义矩形，墨迹各边外扩.5。DPR=1时文字左上为框内(2,0)、基线为框y+A9；DPR>1时在扣左右2后的内矩形中心对齐文字，不将1 DIP描边额外计入标签返回宽。来源 T/ui/unread_badge.cpp:185–215；T/dialogs/dialogs.style:491–494；L/basic.style:62。

静音图标只在显示开关开启、存在thread且已静音时出现（T/dialogs/ui/dialogs_layout.cpp:935–948）；名字后有徽记宽B时，它的原点为 `(标题左沿+min(N,名字允许宽)+B+4, 10+q((Fs−8)/2))`，并先预留8+4。默认显示策略不得从本规格推定为开启。T/dialogs/ui/dialogs_layout.cpp:965–979。

正文预览不是两行自动折行：普通行只有标题＋一行预览。长消息在该行省略，不能撑高62；论坛的21高话题带是明确附加结构。预览图片按16×16、top偏移1、步进18、图片组后再加3，发送者前缀有图且无左侧小图标时额外让5。图不够18宽就停止；最终文本允许宽须大于省略号宽才画。T/dialogs/dialogs.style:496–500；T/dialogs/ui/dialogs_message_view.cpp:460–545。话题标题从左依次排，普通间隔8、可跳转的首项后14；T/dialogs/ui/dialogs_topics_view.cpp:193–239。这些扩展不得改变普通行的两个基准文字带。

### 右下未读与置顶

普通计数胶囊高19，宽 `U=max(19,C+10)`；右沿 `W−10`，top `Y=34+An−Ab−q((19−Fb)/2)`。胶囊两端为半径9.5的圆弧，无描边、抗锯齿；文本x为胶囊左＋q((U−C)/2)，基线 `Y+q((19−Fb)/2)+Ab=34+An`。因此默认未读数字和预览基线相同，不能把胶囊机械居中到62行里。T/dialogs/ui/dialogs_layout.cpp:322–343；T/ui/unread_badge_paint.cpp:97–134,144–188。

预览为胶囊让 `U+5`；无未读且无其他角标时才画置顶20×20画布，原点 `(W−30,34)`，预览让25。未读/mention/reaction/poll 均会使普通置顶隐藏，右端动作按钮另有优先分支。窄行胶囊右沿56、top35；最多两个角标，不能把窄行角标放到行右边66。T/dialogs/ui/dialogs_layout.cpp:160–285,304–319,1166–1168。数字缩写/截尾在宿主预处理，测量用最终字符串，不把“999+”宽写死。

组合角标从右向左依次排列：先未读；再mention/reaction共用一槽且mention优先；最后poll。宽行后三类用18×18画布、top同数字胶囊Y，各扣18+5；窄行在19×19无数字胶囊内居中放13×13画布（相对胶囊(3,3)），每项扣19+5，累计两槽后跳过后续项。普通窄行数字仍扣U+5。未读标记直径未配置时仍走普通19空胶囊；样式有直径d>0才画居于19框中的d圆。来源 T/dialogs/ui/dialogs_layout.cpp:160–285。宽行非静音mention/reaction/poll前景分别dialogsMentionIconFg/ReactionIconFg/PollIconFg，active取dialogsNameFgActive；静音取UnreadBgMuted/Over/Active。窄行mention胶囊取普通未读六态底；reaction取DraftFg/Over/Active（静音仍取未读静音底）；poll取PollIconFg（静音同前）；三者图形均UnreadFg/Over/Active。来源 T/dialogs/dialogs.style:591–609；T/dialogs/ui/dialogs_layout.style:67–105；T/ui/unread_badge_paint.cpp:25–47,97–113。它们由SVG生成18/13画布，不存在同名独立@1x PNG，测量与替代见素材补表。

预览优先级：满足条件的活动覆盖摘要或草稿；非论坛且没有最后消息或没有未读时才选云草稿；有未读则消息优先。推广自定义文案可覆盖预览，推广标签占时间位。T/dialogs/ui/dialogs_layout.cpp:570–587,642–768,1144–1159,1213–1227。数据缺失不生成假消息；空预览可留空，行身份仍存在（同文件:769–806）。

## 搜索框与顶栏

独立搜索框只拥有自身35高矩形；宿主外层54高搜索栏负责留白及菜单入口，两者不能混为一个35高整栏。顶栏54来自 T/info/info.style:919；放置见 T/dialogs/dialogs_widget.cpp:4650–4716。

令会话栏宽P，展开且没有子列表过渡：

| 条件 | 搜索框 `(x,y,w,h)` | 其他矩形 | 来源 |
|---|---|---|---|
| rail已显示 | `(11,9,P−22,35)` | rail主菜单在另一栏；会话栏菜单隐藏 | T/dialogs/dialogs_widget.cpp:1729–1734,4665–4689 |
| rail未显示 | `(54,9,P−65,35)` | toggle自身 `(7,7,40,40)`，32画布 `(11,11,32,32)`；下层同动作扩大命中区 `(0,0,54,54)`，故完整菜单命中54×54 | T/dialogs/dialogs_widget.cpp:1718–1723,4665–4703；T/dialogs/dialogs.style:287–298 |
| 搜索取消/清空按钮 | 搜索框局部 `(w−35,0,35,35)` | 显示条件：query非空，或（不在inChat范围且searchActive/ suggestionsLocked至少一个成立）；不能仅以query判断 | T/dialogs/dialogs_widget.cpp:2151–2152,3974–3983,4711–4712；T/dialogs/dialogs.style:347–363 |

54高减35高是19，Qt整数定位取9而不是9.5。窄栏过渡：`r=interpolate(P,66,childProgress)`，`s=260−66=194`，`n=(r<s ? (s−r)/(s−66) : 0)`；先按 `max(r,s)` 算搜索框宽，x再由初始x向66插值；菜单x由7向13插值，窄搜索按钮从栏左外进入中心。宽度小于66时调用方直接跳过此几何更新。来源 T/dialogs/dialogs_widget.cpp:4650–4709、T/window/window.style:34；插值取整沿用Qt整数几何，不把正在收起的搜索框强夹成一个可见小输入框。

searchActive为普通模式的字段焦点，读屏模式为宿主已进入搜索的engaged状态。全局空查询但搜索active时仍显示取消按钮；在聊天范围内空查询时隐藏这个按钮，退出范围另有入口。取消时清engaged、停止当前请求；有查询先清查询，只有强制完整取消或没有待清查询时才清范围/来源/标签等。若强制完整取消或清后已不在聊天范围内，且字段有焦点或建议被锁定，则将焦点交回列表并解除建议锁定；范围内只清查询可以继续编辑。建议页consume查询/仅tabs的提前返回、单栏跳回目标会话由宿主导航策略处理。来源 T/dialogs/dialogs_widget.cpp:5139–5200,5207–5230。现有独立Search的“清空保焦点”仅是字段原语合同；完整搜索栏必须提供上述取消状态和宿主焦点回交，不能将原语合同当官方全局取消行为。

| 内部项 | 画法 | 来源 |
|---|---|---|
| 外形 | 35高、名义外圆角18；描边中心矩形四边内缩0.75，中心线半径17.25，Qt按可用高度限制圆角 | T/dialogs/dialogs.style:314–339；L/widgets/fields/input_field.cpp:2878–2897 |
| 描边 | **圆角分支始终用 border/denominator=3/2=1.5**；聚焦只插值颜色和底色。borderActive=2不在这条分支改变线宽，不能按旧对照表画成聚焦1 DIP | 同上；L/widgets/fields/input_field.cpp:2900–2925是另一种平底分支 |
| 文本名义区 | 左12、上8、右30、下5；35高时名义区高22。字段documentMargin=0；不要叠Qt默认document留白 | T/dialogs/dialogs.style:317；L/widgets/fields/input_field.cpp:1965–1966,4167–4173 |
| 占位 | 比正文左端再右5，右端再收2；空且仅聚焦不浮起、不缩小；输入后沿x正向最多50平移并淡出 | T/dialogs/dialogs.style:322–325；L/widgets/fields/input_field.cpp:2967–2998,4213–4226 |
| 占位基线 | `8+自选字体margin+占位专用skip+clamp(4Fn/5−max(leading,0),0,Fn)`，不是一律8+An；default字体margin为0，非单行模式还可能有placeholderCustomFontSkip | L/widgets/fields/input_field.cpp:1149–1163,1904–1924,4176–4184 |
| 颜色/动画 | filterInputInactiveBg→ActiveBg，borderFg→windowBgRipple，placeholderFg→placeholderFgActive；聚焦和占位各150ms linear | T/dialogs/dialogs.style:315–329；L/widgets/widgets.style:1044；L/widgets/fields/input_field.cpp:3299,4222–4226 |
| 清空图形 | 35画布、中心(17.5,17.5)，skip12、stroke1.5、minScale .3；终态11×11的平头X，填充轮廓而不是圆头线；不是PNG | T/dialogs/dialogs.style:347–363；L/effects/cross_animation.cpp:123–147,174–180 |
| 范围搜索附加键 | 日历32×35、来源29×35，图形画布偏移(1,6)；右侧按日历再来源逐个扣宽，来源可见时额外增加字段right margin29 | T/dialogs/dialogs.style:300–312；T/dialogs/dialogs_widget.cpp:4626–4646,4711–4716 |

清空显隐150ms linear，a同时控制alpha。这里35/2的整数值为17：令d=.3+.7a，边内缩s=12d+17(1−d)，未旋转边长 `e=35−2s=4+7a`；中心仍17.5，绕中心旋转θ=`90(1−a)`度。终态两条平头对角带的正交厚度1.5，其边缘在未旋转方框内，交叠处合并填一次，高质量抗锯齿。来源 L/widgets/buttons.cpp:741–774,796–804；L/effects/cross_animation.cpp:123–147,160–180。

loading周期1000，u为周期进度、每段局部进度r=`8u−floor(8u)`。把X视为中心连接四个斜向半臂的并集，每个半臂是由两条平行边界限定的带：靠方框角的一对点称外端角，靠中心交叠边界的一对点称内根角；顺序沿X轮廓对应，插值均线性。每段从原始X的未变形几何计算，已删除的臂不重新出现。来源 L/effects/cross_animation.cpp:43–96,123–147。

| u区间 | 半臂轮廓变化，其他剩余端点固定 |
|---|---|
| [0,1/8) | 右下臂两外端角分别向右、下内根角移动r；末端右下臂消失 |
| [1/8,2/8) | 左下臂两外端角向下、左内根角移动r；右下已无 |
| [2/8,3/8) | 左上臂两外端角向左、上内根角移动r；两个下臂已无 |
| [3/8,4/8) | 仅剩右上臂；它的下、左内根角向右上臂对应外端角移动r，收至右上外端边 |
| [4/8,5/8) | 右上臂内根角从外端角恢复至原位置r |
| [5/8,6/8) | 右下臂外端角从对应内根角恢复至原位置r |
| [6/8,7/8) | 左下臂外端角从对应内根角恢复至原位置r |
| [7/8,1) | 左上臂外端角从对应内根角恢复至原位置r，回完整X |

圆弧中心同17.5，半径 `sqrt(2)×e/2−.75`，线宽1.5、圆帽。以Qt角度约定（0向右、正角逆时针），参考起角α=`45−θ`；有符号扫角σ在u<.5时为−720u，否则720(1−u)。若σ<0，以α+σ为实际起角、−σ为正扫角；否则以α为起角、σ为扫角。Qt以1/16度对σ及θ相关角量就近取整。停止loading继续到当前周期结束；减动效时圆环静态显示。来源 L/effects/cross_animation.cpp:150–158,182–203；L/widgets/buttons.cpp:775–833。IME/光标保留mygo原生输入，Qt内部margin的差异沿用 [SET-INPUT-PLATFORM](../exceptions.md#set-input-platform)。

## 导航栏与主菜单

| 项目 | 几何 / 行为 | 来源 |
|---|---|---|
| 栏 | 宽72，高由宿主可用区决定，整栏sideBarBg；主菜单固定 `(0,0,72,54)`，文件夹滚动区 `(0,54,72,H−54)` | T/window/window.style:229,256–259；T/window/window_filters_menu.cpp:143–153 |
| 普通文件夹 | 宽72、最小高62，图标36画布 `(18,6,36,36)`；标签区 `(6,40,60,文本高)`，水平居中、最多3行 | T/window/window.style:230–245；L/widgets/side_bar_button.cpp:153–197,267–276 |
| 增高 | `62+max(min(实际60宽文本高,3Frail)−Frail,0)`；每多一行加实际字体行高，不是每项一律62 | L/widgets/side_bar_button.cpp:153–162 |
| 纯文字 | 最小44、标签top15；角标偏移改(15,1)，仍按上式增高 | T/window/window.style:247–251 |
| 纯图标 | 高54、36画布 `(18,9,36,36)`；不保留标签空间 | T/window/window.style:252–255；L/widgets/side_bar_button.cpp:153–156,267–276 |
| 主菜单 | 无标签，32画布 `(20,11,32,32)`；可见三线图形只占 `(26,21,20,12)`；不能把它画满32或36 | T/window/window.style:256–267；L/widgets/side_bar_button.cpp:267–276；R/dialogs/dialogs_menu.png 实测 |
| 角标 | 文本宽C，`U=max(17,C+8)`，高17；`x=min(39,72−U−10)`，y7；纯文字改desired x51、y1；文字框居中 | T/window/window.style:238–244,250；L/widgets/side_bar_button.cpp:81–93,200–219；L/widgets/widgets.style:821–825 |
| 角标切洞 | 在图标本身擦出胶囊＋2 DIP外扩，使用透明Source合成；再画胶囊；不是给整个图标盖一块实色矩形 | L/widgets/side_bar_button.cpp:279–315；T/window/window.style:243 |
| 锁定 | 前景、图标、角标透明度.6，背景正常，仍可点以报告锁定意图，但不能成为当前项 | L/widgets/side_bar_button.cpp:68–77,174–176,223–249 |
| 锁图形 | 9×10，弧笔宽3/2=1.5、SquareCap/圆连接，弧内缩2、高3；底块(0,5,9,5)、圆角2；纯图标中心锚在画布右下往内5处 | L/widgets/widgets.style:1682–1688；L/widgets/side_bar_button.cpp:223–249,339–397 |

锁在有文字模式中占首行的开头：先为锁预留宽 `ceil(9/字母l的advance)×字母l的advance` 的不可见文字槽，参与60宽排版；令包括该槽的首行宽L、字体行高F、空格宽S，先在 `((72−L)/2,textTop+(F−10)/2,9,10)` 用不衰减的当前背景擦除该槽，再将锁x左移S/2、以.6绘制。普通textTop40、纯文字15；这是首行inline锁，不是整列左边加一个20图标。来源 L/widgets/side_bar_button.cpp:106–125,223–241。纯图标先在36画布内挖透明圆洞：圆心(31,31)、半径 `10/2+2=7`，再叠锁；画布原点(18,9)，锁左上 `18+36−5−q(9/2)=45`、`9+36−5−q(10/2)=35`，故锁框(45,35,9,10)。来源同文件:242–249,316–325；不是badge胶囊切洞。

主菜单其他账号有未读时叠点，点位在32画布内(22,7,6,6)；全静音换sideBarBadgeBgMuted，其余sideBarBadgeBg，不改变54高。T/window/window_filters_menu.cpp:233–243；T/window/window.style:261–267。文件夹标签/图标普通、active分别用sideBarTextFg/IconFg及其Active；鼠标hover本身没有另一套整行底色，按下有ripple。L/widgets/side_bar_button.cpp:165–197；L/widgets/widgets.style:1529–1541。

## 素材实测与替代合同

下面数据来自上述固定版本 @1x PNG 的逐像素测量，**这些是黑底白色灰度掩模，alpha全不透明，不能按alpha测包围盒**。L/style/style_core.cpp:90–100,124–136 以掩模首字节作覆盖率。B表示灰度≥1的外包围盒，B128表示灰度≥128的主体框，均为 `(x,y,w,h)`；不复制或分发PNG。样式偏移后才得到行内位置。替代图标不直接将Lucide的24 viewBox拉到目标画布：先量自身笔画外框（包括半线宽），将可见图形缩放到B宽高并平移到B原点，保留外画布与布局宽度；圆头/圆连接沿用Lucide。原版PNG无法给出可靠矢量线头或统一stroke，不能凭空登记“原版2 DIP”。可见轮廓差已在 exceptions.md Dialogs 登记。

| R/路径（.png） | 画布 | B / B128 | 样式偏移；替代与来源 |
|---|---|---|---|
| dialogs/dialogs_chat | 16×10 | (1,1,14,9) / 同 | (1,4)；Lucide Users可见14×9；T/dialogs/dialogs.style:388–392 |
| dialogs/dialogs_channel | 12×11 | (0,0,11,11) / (1,0,10,11) | (3,4)；Megaphone 11×11；同:393–397 |
| dialogs/dialogs_bot | 16×11 | (1,0,14,11) / 同 | (1,3)；Bot 14×11；T/dialogs/ui/dialogs_layout.style:40–44 |
| dialogs/dialogs_forum | 19×10 | (0,0,19,10) / 同 | (1,4)；MessagesSquare 19×10；同:45–49 |
| dialogs/dialogs_sent | 12×10 | (0,1,12,9) / (1,2,11,8) | (10,4)；Check 12×9；T/dialogs/dialogs.style:405–409 |
| dialogs/dialogs_received | 17×10 | (0,1,17,9) / (1,2,16,8) | (5,4)；CheckCheck 17×9；同:410–414 |
| dialogs/dialogs_sending | 11×11 | (0,0,11,11) / 同 | (8,4)；Clock 11×11；T/dialogs/ui/dialogs_layout.style:50–54 |
| dialogs/dialogs_pinned | 20×20 | (4,4,13,12) / (5,4,11,11) | (0,0)；Pin可见13×12，不能画满20；T/dialogs/dialogs.style:415–419 |
| dialogs/dialogs_mute | 8×8 | (0,0,8,8) / (0,1,7,7) | (0,0)；BellOff可见8×8；T/dialogs/ui/dialogs_layout.style:107–112 |
| dialogs/dialogs_menu、dialogs_menu_unread | 32×32 | (6,10,20,12) / (7,10,18,12) | 居中画布；Menu可见20×12；T/window/window.style:256–267 |
| dialogs/dialogs_menu_unread_dot | 32×32 | (22,7,6,6) / 同 | 独立抗锯齿圆点；同上 |
| dialogs/dialogs_calendar | 23×22 | (4,4,15,14) / 同 | (1,6)；CalendarDays可见15×14；T/dialogs/dialogs.style:300–307 |
| dialogs/dialogs_search_from | 23×22 | (4,4,14,16) / (4,4,14,15) | (1,6)；UserSearch可见14×16；同:308–312 |
| dialogs/dialogs_cancel_search | 18×18 | (3,3,12,12) / (4,4,10,10) | (11,11)；ArrowLeft可见12×12，此为退出范围按钮，不是字段清空；同:340–345 |
| dialogs/dialogs_verified_star / verified_check | 各18×16 | (4,2,14,14) / (4,2,13,13)；(7,6,8,6) / (8,7,6,5) | 复合认证底/勾；BadgeCheck可见14×14放在(4,2)，不能统一20画布；T/dialogs/dialogs.style:430–443 |
| dialogs/dialogs_premium | 18×16 | (5,3,12,12) / (5,4,12,10) | Star可见12×12；T/dialogs/dialogs.style:442–446 |

素材补表：R/dialogs/dialogs_chatlist_{mention,reaction,poll}.svg:2–5 是72×72矢量源，样式编译生成18×18或13×13掩模，源码树没有这六个尺寸的独立@1x PNG。下列B/B128为Windows WPF独立栅格化的测量值，用于替代图形校准，**不是Qt生成PNG的实测**；矢量坐标乘18/72或13/72，不改SVG、不将其作为本库素材。替代使用Lucide AtSign、实心Heart、ChartNoAxesColumn，按表列可见框安放；只保证外框，不复制内部轮廓。来源 T/dialogs/dialogs.style:591–609、T/dialogs/ui/dialogs_layout.style:67–105；已有图标替代例外适用。

| 语义 / 生成画布 | B | B128 |
|---|---|---|
| mention18 | (2,2,14,14) | 同 |
| mention13 | (1,1,11,11) | (2,2,10,10) |
| reaction18 | (2,3,14,13) | (2,3,14,12) |
| reaction13 | (1,2,11,10) | (2,2,10,9) |
| poll18 | (2,2,14,14) | (2,3,14,12) |
| poll13 | (1,2,11,9) | (2,2,9,9) |

rail的每个图标画布均36×36，表内B决定替代图形尺寸与位置，不改变 `(18,6)` 的画布原点。来源 T/ui/filter_icons.style:10–87；其中同一个PNG的普通/active仅染色不同。所有替代名称是既有Lucide 1.58.0（ISC/Feather MIT）或宿主提供的同许可资源用途，尚未新增Go图形；本库原创实心文件夹可作为Custom替代，但必须调整到22×17的B，不能保留现有过大的24×21。

| folders/folders_*.png 后缀 | B | B128 | 替代语义 |
|---|---|---|---|
| all | (4,7,28,23) | (5,7,27,22) | MessagesSquare |
| custom | (7,9,22,17) | 同 | 原创实心Folder |
| edit | (7,9,22,19) | (7,10,22,17) | FolderPen |
| cat | (3,6,31,23) | (3,7,30,21) | Cat |
| book | (5,7,26,20) | (6,8,24,19) | BookOpen |
| money | (6,6,24,24) | 同 | CircleDollarSign |
| game | (6,9,24,18) | 同 | Gamepad2 |
| light | (10,6,16,25) | (10,6,16,24) | Lightbulb |
| like | (7,6,22,23) | 同 | ThumbsUp |
| note | (11,6,15,23) | 同 | Music2 |
| palette | (6,7,23,22) | 同 | Palette |
| travel | (6,6,25,24) | (7,6,23,24) | Palmtree |
| sport | (6,6,24,24) | 同 | CircleDot |
| favorite | (5,4,26,25) | (6,5,25,24) | Star |
| study | (4,6,28,23) | (4,6,28,22) | GraduationCap |
| airplane | (5,8,24,20) | 同 | Plane |
| private | (6,6,24,24) | 同 | CircleUserRound |
| group | (4,9,28,19) | (5,9,27,18) | Users |
| unread | (7,7,23,21) | (7,8,22,20) | MessageCircleMore |
| bots | (5,7,26,21) | 同 | Bot |
| crown | (4,7,28,21) | (5,7,26,21) | Crown |
| flower | (6,5,24,26) | (7,5,22,26) | Flower2 |
| home | (5,6,26,23) | 同 | House |
| love | (7,9,22,19) | 同 | Heart |
| mask | (2,11,32,14) | (3,11,30,14) | VenetianMask |
| party | (8,8,20,21) | 同 | PartyPopper |
| trade | (5,7,24,21) | (6,7,23,21) | ChartNoAxesCombined |
| work | (6,7,24,22) | 同 | BriefcaseBusiness |
| unmuted | (8,6,20,25) | 同 | Bell |
| channels | (7,7,22,23) | 同 | Megaphone |
| setup | (9,6,18,21) | (9,7,18,20) | SlidersHorizontal |
| poo | (5,5,26,24) | 同 | 宿主独立允许分发的同语义图形 |

图标替代的笔画视觉重量须在100/125%分别以B128及横截面报告，不能把PNG的细灰边推成一个统一线宽；只有位置和图形外框≤1 DIP不等于内部轮廓一致。本页不抄原PNG轮廓来生成SVG。特殊头像、媒体缩略图、emoji内容由宿主提供，不能拿一张参考头像的墨迹宽作为通用46头像尺寸。

## 状态与动效

| 状态 | 变化及优先级 | 来源 |
|---|---|---|
| 普通 / hover / 当前会话 | 普通currentBg与dialogsName/Text/DateFg；hover取Over；当前会话取Active且优先于hover；整行不额外描边 | T/dialogs/ui/dialogs_layout.cpp:468–492,955–964；L/colors.palette:183–235 |
| hover和当前切换 | 上述分支直接取色，没有一条120ms整行颜色插值；不能沿用CrychicUI Snappy | 同上 |
| 按下 | 整行矩形裁切ripple；Active时换dialogsRippleBgActive；650ms easeOutQuint扩散、松开200ms linear淡出 | T/dialogs/dialogs_row.cpp:337–379；T/dialogs/ui/dialogs_layout.cpp:487–492；L/widgets/widgets.style:705–706；L/effects/ripple_animation.cpp:86,151 |
| 未读静音 | 按Muted/Over/Active六组角色取色，胶囊位置不变；不是整体降透明度 | T/ui/unread_badge_paint.cpp:25–31,97–113 |
| rail当前/按下 | Active背景/前景/图标直接切色；按下sideBarBgRipple；hover不加其他底色 | L/widgets/side_bar_button.cpp:51–65,165–219；L/widgets/widgets.style:1529–1541 |
| 暗色 | 所有同名palette角色由night覆盖/继承，几何不变；不把rail浅色本来就深的底再次反转 | L/colors.palette:183–235,645–658；R以外的Telegram/Resources/night.tdesktop-theme，既有theme-colors.md登记 |
| 搜索焦点/占位 | 150ms linear；空框聚焦不隐藏占位，非空隐藏；取消按钮的空query显隐和焦点回交按上节状态合同 | L/widgets/fields/input_field.cpp:3299,4213–4226；T/dialogs/dialogs_widget.cpp:3974–3983,5139–5200 |
| 在线点 | 10圆、2描边、底部偏移2；150ms linear，窄行计数优先；独立于整行颜色直接切换 | T/dialogs/dialogs.style:185–188；T/dialogs/dialogs_row.cpp:286–305,462–489,675–696 |
| 文件夹切换 | 240ms easeOutCirc；内容两块裁剪共享边界，旧内容位移100×进度、新内容宽×剩余进度；动画不是rail行颜色动画 | T/dialogs/dialogs_widget.cpp:3063–3073；T/window/window_slide_animation.cpp:115–151,196–211；L/basic.style:96–98 |

mygo ReduceMotion直接终态、禁用语义、字体放大增高均沿用已登记 exceptions.md；它们是本库映射，不把不可用态统一当Telegram .6。清空、改选、倒转和取消不得留下拦截层。上述时间轴变化不触发头像解码或重新解析全文。

曲线归一化进度t∈[0,1]：linear=t，easeOutQuint=`1−(1−t)^5`，easeOutCirc=`sqrt(1−(t−1)^2)`。快速反向焦点/占位/清空时从当前值续向新目标，重新计满声明时长；不跳到预设0/1。来源 L/effects/animation_value.cpp:75–84,118–127、L/effects/animations.h:409–440,471–479。ripple半径从按下原点0增至覆盖遮罩四角的最大距离（就近取整），650ms Quint；松开后的alpha用200ms linear独立衰减，扩散可以同时继续，新波开始先停止旧波。来源 L/effects/ripple_animation.cpp:59–86,102–155,189–193。

## 可脚本校验的几何表

输入必须包含实际塑形宽/行高，不能仅传“名字有六个汉字”。固定编造文本可用“林知夏”“周明远与星港设计协作小组”“今晚把讨论记录整理好，明早一起确认”；各平台先量N/D/C，再代入同一公式。下面**算例度量输入由我定的**，不冒充运行Qt实测：W=320，Fn=Fs=18，An=As=14，Dn=4，Fb=16，Ab=12，D=35。所有名字无认证/状态emoji，普通LTR，无媒体和活动。算法来源为上节所列源码。

| 输入 | 应有矩形 / 数值 | 判据来源 |
|---|---|---|
| 私聊、N=39，无收据/徽标 | row(0,0,320,62)，avatar(10,8,46,46)，name(68,10,202,18)，date(275,10,35,18)，preview(68,34,242,18) | dialogs_layout.cpp:552–568,87–106,1194–1212 |
| 群组、长名N=300，已读，C=14未读 | 类型画布(69,14,16,10)，墨迹(70,15,14,9)；名字允许(88,10,162,18)并省略；双勾画布(255,14,17,10)；date同上 | dialogs_layout.cpp:600–605,825–864；素材表 |
| 上行未读 | U=24，Y=35；badge(286,35,24,19)，计数基线48；preview(68,34,213,18)，预览基线48 | dialogs_layout.cpp:322–343；unread_badge_paint.cpp:157–188 |
| 私聊N=39、静音开关开，无收据/未读 | 名字允许宽190，mute画布(111,15,8,8)；date不变；若开关关则不绘、不预留12 | dialogs_layout.cpp:935–979 |
| 置顶、无任何角标 | pin画布(290,34,20,20)，墨迹(294,38,13,12)；preview宽217；未读出现则pin消失 | dialogs_layout.cpp:197–244,1166–1168 |
| 窄行W66、C=7未读 | avatar同上，badge(37,35,19,19)；不画名字、预览、时间、置顶 | dialogs_layout.cpp:304–319,556–560 |
| rail高600，标签一行Frail15 | main(0,0,72,54)，main画布(20,11,32,32)，main墨迹(26,21,20,12)；第一folder(0,54,72,62)，folder画布(18,60,36,36)，标签顶94 | window_filters_menu.cpp:143–153；side_bar_button.cpp:153–197 |
| rail标签三行，Frail15，C=14角标 | 行高92；角标U22、x39、y7，文本居中；Custom画布内可见(7,9,22,17)，第一项全局墨迹(25,69,22,17) | side_bar_button.cpp:153–219；素材表 |
| P320、有rail | search(11,9,298,35)，clear(274,9,35,35)，正文名义区(23,17,256,22) | dialogs_widget.cpp:4665–4712；dialogs.style:317 |
| P320、无rail | search(54,9,255,35)，toggle(7,7,40,40)、完整菜单命中(0,0,54,54)，menu墨迹(17,21,20,12)，clear同上；点(2,2)也应打开菜单 | T/dialogs/dialogs_widget.cpp:1718–1723,4696–4703；菜单素材表 |
| 标签/论坛 | 普通标签H72、预览y30；论坛H80、话题y32/h21、摘要y53；标签论坛H96、tag y77 | dialogs.style:114–128；dialogs_message_view.cpp:448–451 |
| 未读C14＋mention＋poll | W320时badge(286,35,24,19)、mention画布(263,35,18,18)、poll(240,35,18,18)，preview宽167；窄W66时只保留数字与mention、poll省略 | T/dialogs/ui/dialogs_layout.cpp:160–285 |
| 空query搜索状态 | 全局active或suggestionsLocked显示取消，范围内空query隐藏；全局取消回列表焦点，范围内清非空query保留编辑 | T/dialogs/dialogs_widget.cpp:3974–3983,5139–5200 |
| clear a=.5 | s13.75、边7.5、θ45°、alpha.5；u=.25时有符号扫角−180°、u=.75时+180°；不可按无符号弧长画同一半圆 | L/effects/cross_animation.cpp:123–158,182–203 |
| 锁定rail，F15，S4，首行L40 | 普通模式擦除(16,42.5,9,10)、锁(14,42.5,9,10)；纯文字y17.5；纯图标锁(45,35,9,10)、图标局部圆洞(31,31,r7) | L/widgets/side_bar_button.cpp:223–249,316–325 |

等号边界：badge `C+10=19` 不应加宽；rail `C+8=17` 不应增高；预览可用宽等于省略号宽时不绘正文；名字与时间各自保留独立区域。每个案例再测Active/hover/muted/night不改变矩形、100/125/150%像素转换不重复乘缩放。字体/图形墨迹另列Δx/Δy/Δw/Δh，不用名字替换后的宽度评分。

## theme 接手登记

已有尺寸、颜色和字阶以 theme-metrics.md/theme-colors.md/theme-behavior.md 为唯一生成登记；本页没有改JSON。以下是实现时应补或纠正的用途，不能在组件内写字面量：

| 用途 | 常量 / 值 / 来源 | 处理 |
|---|---|---|
| rail主菜单高/画布 | windowFiltersMainMenu.minHeight=54、dialogs_menu32画布；T/window/window.style:256–259及素材表 | 新增专用用途；不能语义上借iconsOnlyHeight表示主菜单 |
| 搜索外层 | topBarHeight54、dialogsMenuToggle40、filterPadding7、filterSkip4；T/info/info.style:919；T/dialogs/dialogs.style:92–93,287–298 | 现有header.height/search.outerPadding/outerSkip可按共同来源复用；主菜单命中宽须显式登记 |
| 搜索描边 | 圆角运行分支border/2=1.5；L/widgets/fields/input_field.cpp:2883–2887 | search.borderActive=1仅是未消费声明，不再作为圆角绘制参数；同步生成表描述与调用测试 |
| 字段占位 / clear | placeholderMargins5/2、shift−50；clear stroke1.5/minScale.3/period1000；T/dialogs/dialogs.style:322–325,351–362 | 现有search.clearSize/Inset之外补专用token；动效可复用同来源150ms linear |
| 图标可见框 | 本页全部B及B128、对应PNG路径 | 画布保持原有iconSize，替代图形在独立允许分发的资产内校准，不新增按页面的随意光学偏移 |
| 名字徽记 / rail锁 | verified18×16、状态emoji20/22预留；锁9×10/stroke1.5；上节来源 | 补专用用途；旧20通用iconSize不能代表这些几何 |

## 实现接手清单与验收边界

1. 先补rail固定54主菜单行和搜索外层54顶栏；当前 `dialogs/rail.go:68–78` 只有文件夹列表，`dialogs/search.go:17–21` 只有字段，宿主组合须消费本页矩形，不能在Search内部再塞顶栏padding。
2. 按素材B重新校准原创文件夹及置顶/认证/Menu等可见图形，保留画布/槽位；`dialogs/icons.go:17–18` 原创文件夹现为24×21可见框，应按Custom22×17校准。新增图形沿用现有许可证登记，不复制PNG。
3. 修正搜索聚焦描边仍1.5的调用事实；现有 `dialogs/search.go:24–28` 切成BorderActive，旧 dialogs.md 的“聚焦1”是声明推断，实施时同步更正文档/测试，不能继续据它验收。
4. 用公开Font.Metrics/基线绘制核对时间与徽标，修正旧“mygo无ascent”推断；核对可选静音、名字让位和窄行角标。系统族与Qt的真实墨迹差单列，不写固定猜测偏移。已有标签/论坛/活动路径必须保持。
5. 后续画廊用3–6条真实中文全名/长标题/长预览，空列表保持真实空态；每个画廊页明暗打开并稳定，若缺 TestEveryEntry 则先补全量页测试。默认/窄栏、搜索输入清空与焦点、当前项/锁定、右键与键盘、长徽标、反向切换逐项真操作。改前/改后输出矩形CSV和轮廓叠图＋左右拼接，禁止反色闪烁。

本次为文档交付，未修改Go/主题/画廊，未运行UI或宣称现有实现满足规格。已核对两个外部HEAD，运行灰度掩模测量，并按给定度量演算几何表；运行结果、独立审查与范围检查见组件表。适用准则：C01/C02尺寸有据、C07基线、C08画布、C12变体、B01层级及比较可复现已落实为规格条目；实际UI几何、IME、动画、官方截图和性能均待实现验收。本页“我定的”仅为符号、验收输入和独立允许分发的替代图形选择；平台差异均链接既有exceptions登记，未扩大文件修改范围。
