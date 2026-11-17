# 消息底部信息与表情回应画法规格

本页是 a2-spec 的净室画法规格，只记录官方 Telegram Desktop 的几何、状态和可观察行为，不包含 C++ 摘录、译写代码或素材。后续实现只读本页；本次不修改 Go、主题生成器或画廊。64Gram 截图只用于提出问题，不作为数值或额外功能的来源。

## 基线、来源与坐标

核对版本：tdesktop `250fc73c2f17a6c65327cb66af06375b7872c0ba`、lib_ui `1ff7be77d578aaf8ad17d2fa6e51bae1ee567c1b`，与 [参考目录登记](../README.md) 相同。以下短文件名按本表解析，每处引用均为该固定版本的文件与行号。

| 短文件名 / 前缀 | 完整位置 |
|---|---|
| `history_view_message.cpp` | `tdesktop/Telegram/SourceFiles/history/view/history_view_message.cpp` |
| `history_view_element.cpp` | `tdesktop/Telegram/SourceFiles/history/view/history_view_element.cpp` |
| `history_view_bottom_info.cpp` | `tdesktop/Telegram/SourceFiles/history/view/history_view_bottom_info.cpp` |
| `history_view_reactions.cpp` | `tdesktop/Telegram/SourceFiles/history/view/reactions/history_view_reactions.cpp` |
| `history_view_group_call_bar.cpp` | `tdesktop/Telegram/SourceFiles/history/view/history_view_group_call_bar.cpp` |
| `history_view_photo.cpp` / `.h` | `tdesktop/Telegram/SourceFiles/history/view/media/history_view_photo.cpp` / `.h` |
| `history_view_media_grouped.cpp` / `.h` | `tdesktop/Telegram/SourceFiles/history/view/media/history_view_media_grouped.cpp` / `.h` |
| `history_view_gif.cpp` | `tdesktop/Telegram/SourceFiles/history/view/media/history_view_gif.cpp` |
| `history_item.cpp` | `tdesktop/Telegram/SourceFiles/history/history_item.cpp` |
| `chat.style`、`chat_style.style`、`chat_style.cpp` | `tdesktop/Telegram/SourceFiles/ui/chat/` 下同名文件 |
| `dialogs.style` | `tdesktop/Telegram/SourceFiles/dialogs/dialogs.style` |
| `L/` | `lib_ui/ui/` |
| `R/` | `tdesktop/Telegram/Resources/` |

参考了官方 tdesktop 的三条设计：消息壳决定底部信息由正文还是媒体拥有；正文/回应通过一个不可见的尾部矩形为时间让位；先测回应自然宽度，再参与气泡宽度协商。它们分别解决图片双重时间、回应与时间碰撞、短正文配长回应的问题。依据：`history_view_message.cpp:1536-1559,5413-5467,7227-7246,7287-7324`。只借行为和数值，不沿用其类型和函数组织。

所有几何为 100% 的逻辑像素，即 DIP；内边距按“左、上、右、下”书写。矩形用 `(x,y,w,h)`，右边界和下边界分别为 `x+w`、`y+h`，避免把 Qt 的包含式 `right()/bottom()` 多算一像素。下文先描述普通 LTR 界面，RTL 规则单列。字体 `F` 是实际行高、`A` 是实际 ascent，不能用 13 DIP 字号替代；回应数字另用 `Fs/As` 表示 semibold 度量。依据：`history_view_bottom_info.cpp:275-276,298-303`；`history_view_reactions.cpp:609-616`。

## 样式数值登记

本表补齐绘制规格所需的声明及用途；已有 token 的完整明暗值见 [颜色表](../theme-colors.md)、[尺寸表](../theme-metrics.md)、[字阶表](../theme-behavior.md)。标为“未登记”的量是后续实现所需的主题缺口，本次不新增 token。

| Telegram 常量 | 值 | 来源 | 本库用途 / 现有映射 |
|---|---:|---|---|
| `msgMaxWidth`、`msgMinWidth` | 430、160 | `chat.style:14,22` | `message.maxWidth/minWidth`；普通宽度边界，不是每个气泡强制最小 160 |
| `msgPadding` | 11、8、11、8 | `chat.style:25` | `message.padding*`；气泡内容区 |
| `msgDateFont` | 13 regular | `chat.style:21` | `messageTime`；所有时间/浏览/回复文字 |
| `msgDateSpace`、`msgDateDelta` | 12、(2,5) | `chat.style:99-100` | `message.timestampSpace/DeltaX/DeltaY`；尾部占位和绘制修正 |
| `msgDateImgDelta`、`msgDateImgPadding` | 4、(8,2) | `chat.style:102-103` | `media.badgeInset/PaddingX/PaddingY`；图片上信息底板 |
| `mediaInBubbleSkip` | 5 | `chat.style:156` | 已有 `preview.mediaGap`，回应与上方内容也使用此来源 |
| `maxSignatureSize` | 144 | `chat.style:174` | 签名与日期组合的上限；未登记专用 token |
| `historySendStateSpace`、`historySendStatePosition` | 24、(-17,-19) | `chat.style:238-239` | 收据槽、相对日期行底的图标原点；未登记 |
| `historyShortcutStateSpace` | 18 | `chat.style:240` | 快捷消息特殊占位；未登记 |
| `historyViewsSpace`、`historyViewsWidth` | 8、20 | `chat.style:242-243` | 浏览/回复数字右侧间距、左图标槽；未登记 |
| `historyPinWidth`、`historyViewsTop`、`historyPinTop` | 24、-15、-18 | `chat.style:244-246` | 置顶槽与浏览/置顶图标纵向偏移；未登记 |
| `historySilentWidth`、`historySilentTop` | 12、-13 | `chat.style:247-248` | 静默定时消息标记；未登记 |
| `historyEphemeralStateWidth/Top` | 16、-14 | `chat.style:341-342` | 无气泡且媒体未自画的限时标记；未登记 |
| `reactionInlinePadding` | 5、2、7、2 | `chat.style:673` | `reaction.padding*` |
| `reactionInlineSize/Image/Skip` | 18 / 32 / 3 | `chat.style:674-676` | `reaction.iconSize/imageSize/labelGap` |
| `reactionInlineEmptySkip` | 2 | `chat.style:683` | 零计数的纯图标胶囊缩减；未登记 |
| `reactionInlineBetween` | 4 | `chat.style:687` | `reaction.gap`；横向与行间均使用 |
| `reactionInlineInBubbleLeft` | -3 | `chat.style:688` | 气泡内回应左移；未登记 |
| `reactionInlineUserpicsPadding` | 1、1、1、1 | `chat.style:689` | 头像替代计数时的专用留白；未登记 |
| `reactionInlineUserpics.size/shift/stroke` | 20 / 7 / 1 | `chat.style:690-695` | `reaction.userpicSize/userpicOverlap/userpicStroke` |
| `semiboldFont`、`fsize` | 13 semibold | `L/basic.style:53-55` | 普通回应计数用 `contentTitle`；`reaction` 的 12 regular 实为标签字阶 |
| `reactionInfoSize/Skip/Between`、`effectInfoImage` | 15 / 3 / 3、12 | `chat.style:697-699,723` | 消息特效小标记，非回应胶囊；未登记 |
| `bubbleRadiusSmall` → `roundRadiusLarge` | 6 | `chat.style:326`；`L/basic.style:107` | 无壳媒体旁 Background 信息底板圆角 |

### 颜色角色

以下十六进制值为基础浅色 palette，alpha 在末尾；暗色按同名角色取现有主题表，不复用浅色常量。服务底色在 Telegram 可随墙纸调整，`msgServiceBg` 的基础值不能冒充任意墙纸下最终采样值。消息方向与“我已回应”是两个独立状态。角色选择依据：`history_view_reactions.cpp:499-548`；`chat_style.cpp:762-806`。

| 区域 / 状态 | 填色 | 文字 / 单色前景 | 来源 |
|---|---|---|---|
| 气泡内，入站，未选中回应 | `msgFileInBg` `#40a7e3` × 0.12 | `msgInServiceFg` `#168acd` | `L/colors.palette:355,388`；`history_view_reactions.cpp:38,499-509,536-548` |
| 气泡内，出站，未选中回应 | `msgFileOutBg` `#5fbe67` × 0.18 | `msgOutServiceFg` `#45a32d` | `L/colors.palette:357,391`；同文件 `:39,499-509,536-548` |
| 气泡内，入站，我已回应 | `msgFileInBg` 实色 | `historyFileInIconFg` `#ffffff` | `L/colors.palette:388,411`；`history_view_reactions.cpp:492-509,536-548` |
| 气泡内，出站，我已回应 | `msgFileOutBg` 实色 | `historyFileOutIconFg` `#effdde` | `L/colors.palette:391,415`；同文件 `:492-509,536-548` |
| 消息整条选中，入站 | 底色换 `msgFileInBgSelected` `#51a3d3`，未回应仍 ×0.12 | 未回应 `msgInServiceFgSelected` `#168acd`；已回应 `historyFileInIconFgSelected` `#c2dcf2` | `L/colors.palette:356,390,412`；同文件 `:499-548` |
| 消息整条选中，出站 | 底色换 `msgFileOutBgSelected` `#50ac9b`，未回应仍 ×0.18 | 未回应 `msgOutServiceFgSelected` `#469992`；已回应 `historyFileOutIconFgSelected` `#b7dbdb` | `L/colors.palette:358,392,416`；同文件 `:499-548` |
| 气泡外，未回应 | `msgServiceBg` 基础 `#517c417f`，不再乘0.12/0.18 | `msgServiceFg` `#ffffff` | `L/colors.palette:367-368`；同文件 `:510-518,536-539` |
| 气泡外，我已回应 | `msgServiceFg` | `msgServiceBg` RGB，alpha 至少192/255 | 同文件 `:43-45,514-518,536-539` |
| 普通入站时间 / 消息选中 | 无独立底板 | `msgInDateFg` `#a0acb6` / Selected `#6a9cc5` | `L/colors.palette:363-364`；`history_view_message.cpp:5432-5435` |
| 普通出站时间 / 消息选中 | 无独立底板 | `msgOutDateFg` `#6db566` / Selected `#56b2a6` | `L/colors.palette:365-366`；同文件 `:5432-5435` |
| 图上时间 / 消息选中 | `msgDateImgBg` `#00000054` / Selected `#1c4a7187` | `msgDateImgFg` → `msgServiceFg` | `L/colors.palette:379-382`；`history_view_message.cpp:5437-5455` |
| 普通勾 / 消息选中；图上勾 | — | `historyOutIconFg` `#57b84c` / Selected `#45a3aa`；`historyIconFgInverted` → 白 | `L/colors.palette:272-274`；`chat_style.style:88-91,214-215` |
| 出站发送中 / 非出站发送中 / 图上发送中 | — | `historySendingOutIconFg` `#98d292` / In `#a0adb5` / Inverted `#ffffffc8` | `L/colors.palette:275-277`；`dialogs.style:448-451` |

位图表情保留其原色，不整体染成文字颜色；自定义单色表情可以使用计算后的前景。按下涟漪使用前景 RGB、alpha=25/255，裁切在胶囊内；常态绘制没有额外悬停底色。依据：`history_view_reactions.cpp:549-581,768-815,845-878`。涟漪声明为 650ms 扩散、200ms 消退，来源 `L/widgets/widgets.style:705-706`，现有 `motion.rippleShow/Hide` 已登记。

## 回应胶囊

### 形状与内容矩形

普通回应胶囊高 `Hr=2+18+2=22`，填充圆角矩形，半径 `Hr/2=11`，无轮廓描边；启用高质量抗锯齿。计数字体不反过来改变这个高度。来源：`history_view_reactions.cpp:328-372,494-497,701-711`；`chat.style:673-675`。

以胶囊左上角 `(x,y)` 为原点：表情布局槽为 `(x+5,y+2,18,18)`；普通表情的 32×32 绘制画布居中跨出槽，左上为 `(x-2,y-5)`。32 是带透明边的资源画布，不能据此把可见 emoji 放大到32 DIP，也不能把整个胶囊裁切为只剩22高。计数起点 `x+5+18+3=x+26`；文字行顶为 `y+floor((22−Fs)/2)`，基线为该值加 `As`。来源：`history_view_reactions.cpp:463-465,491,569-581,603-616`。

| 内容 | 胶囊宽度（DIP） | 绘制与条件 | 来源 |
|---|---|---|---|
| 普通非零计数 | `5+18+3+C+7 = 33+C` | `C` 是缩写后计数的 semibold 实测宽度；1 也显示 | `history_view_reactions.cpp:272-285,341-344,364-367` |
| 零计数 | `5+18+7−2 = 28` | 计数文字为空，非字面量“0”；付费占位项会出现此状态 | 同文件 `:279-285,364-365,911-935` |
| n 个头像替代计数 | `5+18+1+[20+(n−1)×13]+1 = 45+(n−1)×13` | 头像起点 `(x+24,y+1)`；步进 `20−7=13`，总高仍22 | 同文件 `:345-367,597-601`；`chat.style:689-695` |

计数使用 `Lang::FormatCountToShort` 的显示字符串，而非始终十进制整数；本页不指定宿主语言下的具体缩写字面量。排序：付费项优先；普通项按“总数减去自己的一次”降序，同数按官方回应列表顺序。来源：`history_view_reactions.cpp:182-206,272-285`。实现边界：宿主交付已格式化的文字及稳定顺序，组件按真实字符串测量，不在绘制时加载数据。

头像来源条件：私聊可确定回应者时使用头像；非私聊只有所有回应项的 recent 列表完整、各项数量吻合、总数≤3时才全部改用头像，否则用计数。头像遮叠按从后向前合成，每个圆周以透明 Source 模式擦出1 DIP线，不是气泡色实体描边。来源：`history_view_reactions.cpp:937-976,832-842`；`history_view_group_call_bar.cpp:26-64`。资源由宿主准备，本库不复制 Telegram 头像或表情。

### 分行与对齐

气泡内的回应列表原点位于气泡内容区左沿，但每行首个胶囊从 `L=−3` 开始，所以普通气泡中首胶囊左沿距外壳 `11−3=8`。气泡外 `L=0`。相邻胶囊外框相隔4；一行放不下下一个完整胶囊时整项移到下一行，y 增加 `22+4=26`。首项即使大于可用宽也不会先产生一个空行，不拆数字或胶囊。来源：`history_view_reactions.cpp:234-246,375-418`；`chat.style:687-688`。

令 `W` 为传入列表的排版宽度。普通非居中列表中，第i项的右边界超过 `L+W` 且它不是该行首项，才换行；等于边界仍保留。宽度协商时，列表自然宽度为 `L + Σ胶囊宽 + 4×(项数−1) + S`，其中 `S` 是底部信息占位宽；气泡按该宽度加左右11扩宽，通常上限430，而不是永远按正文自然宽度裁回应。来源：`history_view_reactions.cpp:328-418`；`history_view_message.cpp:1536-1559,6572-6591`。

气泡内出站消息的回应仍从左排，时间在右；不因自己发送而翻转胶囊顺序。气泡外的右侧布局才镜像每个胶囊的x位置。服务消息的居中列表另受 `chatGiveawayWidth=292`（`chat.style:874`）约束，不适用普通气泡的左对齐公式。来源：`history_view_message.cpp:6794-6795,6922-6929`；`history_view_reactions.cpp:234-246,385-444`。

## 底部信息

### 内容顺序与宽度

普通底部信息只有一条日期文字行，行高 `F=msgDateFont.height`。以 LTR 视觉从左到右排列：特效标记（可选）、非出站发送中时钟（可选）、回复图标与数、浏览图标与数、置顶、限时、静默、签名与日期文字、出站收据。不存在的项不占宽。来源：`history_view_bottom_info.cpp:265-391,588-621`。

日期文字自身通常为“签名, 已编辑 时间”；无签名不加逗号。编辑、更新、近似时间、重复日程的前缀由状态优先级选一个，不拼成多个前缀。签名只在“签名+分隔+日期”超过144时省略签名，不能截断时间来硬塞；导入消息使用“日期 已导入”形式。正常时间按 locale 的 ShortFormat，保存/转发日期或媒体编辑器可为完整日期。来源：`history_view_bottom_info.cpp:485-524,661-699,740-764`；`chat.style:174`。

设 `D` 为最终签名/日期文字宽，`V`、`Q` 为实际显示的浏览/回复数字宽；普通无特效信息宽度为：

`B = D + (出站或发送中 ? 24 : 0) + (有浏览 ? 20+V+8 : 0) + (有回复 ? 20+Q+8 : 0) + (置顶 ? 24 : 0) + (限时 ? 16 : 0) + (静默 ? 12 : 0)`。

这是尺寸求和，不是建议的代码组织。浏览和回复文字均为13 regular；各数字后的8是到右侧下一项的间距，20是其左侧图标槽，不能互换为普通 Row.Gap。浏览存在时至少格式化为1；发送中隐藏浏览及回复。回复数为0、回复列表上下文或快捷消息上下文不显示回复项。来源：`history_view_bottom_info.cpp:339-379,554-577,588-621`。

出站收据的选择依赖显示的未读状态：发送中为时钟；已发送但仍未读为单勾；已读为双勾。非出站没有单/双勾，发送中时钟出现在信息块左端。该版本把发送失败也作为 Sending 标志输入这一路，不在底部信息中画红色叉号。来源：`history_view_bottom_info.cpp:277-294,369-379,715-717`。

### 基线与图标槽

设信息块左上为 `(ix,iy)`、右边界 `ir=ix+B`。日期/浏览/回复文字行顶均为 `iy`，基线为 `iy+A`；图标偏移的参考是日期行底 `iy+F`，不是基线。以下画布大小从固定版本的100% PNG元数据读取，只登记尺寸、不复制图形；样式中的附加偏移也要计入。来源：`history_view_bottom_info.cpp:275-378`；`L/style/style_core_icon.cpp:205-216`。

| 项 | 槽宽 | 素材画布 | 素材绘制左上（LTR） | 样式/资源来源 |
|---|---:|---:|---|---|
| 出站单勾 | 24 | 13×11 | `(ir−17+2, iy+F−19+4)`，即 `(ir−15,iy+F−15)` | `chat.style:238-239`；`chat_style.style:214`；`R/icons/history_sent.png` |
| 出站双勾 | 24 | 18×11 | 同单勾；画布右侧可越过槽参考点3 DIP | `chat_style.style:215`；`R/icons/history_received.png`；同上位置常量 |
| 出站发送中时钟 | 24 | 11×11 | `(ir−17+5, iy+F−19+5)` | `dialogs.style:448-449`；`R/icons/dialogs/dialogs_sending.png` |
| 非出站发送中时钟 | 24 | 11×11 | 槽左+3，`iy+F−15` | `dialogs.style:450-451`；`chat.style:245`；同一时钟资源 |
| 浏览 / 回复 | 20 | 各16×11 | 各自图标槽左，`iy+F−15`；数字从槽左+20开始 | `chat.style:242-245`；`chat_style.style:92-101`；`R/icons/history_views.png`、`R/icons/history_replies.png` |
| 置顶 | 24 | 18×18 | 槽左，`iy+F−18` | `chat.style:244,246`；`chat_style.style:102-106`；`R/icons/history_pin.png` |
| 静默 | 12 | 8×8 | 槽左，`iy+F−13` | `chat.style:247-248`；`chat_style.style:107-111`；`R/icons/dialogs/dialogs_mute.png` |
| 限时 | 16 | 14×14 | 槽左，`iy+F−14` | `chat.style:341-342`；`chat_style.style:119-122`；`R/icons/chat/ephemeral_badge.png` |

图标必须使用独立允许分发的图形代替，保持表中的槽位/矩形；不能按时间字号给所有图标一个13×13框。素材轮廓差异沿用 [exceptions.md](../exceptions.md) 的图标替代约定，不声称逐像素一致。

### 普通气泡与图上底板

普通气泡，令供信息绘制的内壳（已扣评论等底部附加条）右边界为 `r`、下边界为 `b`：信息右边界为 `r−(11−2)=r−9`，下边界为 `b−(8−5)=b−3`，左上为 `(r−9−B,b−3−F)`。无单独时间背景。来源：`history_view_message.cpp:2021-2024,2191-2204,5432-5451`。

图上信息，令实际图片矩形右下为 `(r,b)`：信息右边界为 `r−4−8=r−12`，下边界为 `b−4−2=b−6`；底板为 `(r−4−(B+16), b−4−(F+4), B+16, F+4)`，也就是外底板距图右/下各4、文字留白左右8/上下2。底板实色含alpha，文字不随底板alpha衰减；无额外描边或阴影。圆角使用整数 `floor((normalFont.height+4)/2)`，依据普通字体行高，不凭字号填一个固定半径。信息多行时底板高度随信息块增高，但角半径沿用该值。来源：`history_view_message.cpp:5437-5460`；`chat_style.cpp:797-802`。

Background 类型用于无壳媒体旁的信息：从调用方给出的右下扣(8,2)，底板包住文字留白，填 `msgServiceBg`、文字 `msgServiceFg`，使用小气泡圆角6，不额外扣图片内缩4。它与图上 Image 类型不同。来源：`history_view_message.cpp:5442-5459`；`chat_style.cpp:803-806`；`history_view_gif.cpp:1081-1091`。

## 同行、换行与正文末行

### 统一占位

测量普通信息自然宽度 `Bmax` 后，尾部保留矩形为：`S = msgDateSpace+Bmax−msgDateDelta.x = Bmax+10`，`K = F−msgDateDelta.y = F−5`。信息隐藏时两者均为0。来源：`history_view_element.cpp:1610-1621`；`history_view_message.cpp:5502-5507`。下面两个“同行”判定共享这块占位，但占位只交给实际处于底部的内容。

气泡当前实际宽度记为 `Wb`。普通文本/回应测量宽度为 `W=max(Wb,160)−22`；富文章使用实际 `Wb−22`。气泡在测量后可收缩到真实内容宽，因此160是普通文字测量的下限，并非画出来的所有气泡至少160。底部信息自身的可用宽度使用 `max(Wb−22,1)−2×2`，最大不超过自然宽度。宽度先受消息边距、媒体尺寸和气泡上限影响，不能拿整个聊天栏宽或430直接做同行判定。来源：`history_view_message.cpp:5611-5622,6546-6591,6704-6751,6768-6772`。

### 没有回应时：正文末行让时间

当正文是最后的可见内容时，在正文末尾加入 `S×K` 的不可见矩形，参与真实文字排版。末行实际剩余宽度≥S时，信息可留在该行右下；小于S时占位进入下一行。需要对字体塑形后的宽度（包括字间留白、bearing、引用缩进）判定，不能按字符数、整段总宽或固定“时间长约40”估计。独占末行的高度为K，不是无条件再加完整正文行高。来源：`history_view_message.cpp:7287-7315`；`L/text/text.cpp:948-987,1300-1315,1368-1391`。

正文后还有回应时，正文的时间占位被移除；时间由回应列表承接。正常顺序下正文后是媒体，或者有事实核查/日志内容时，也不能继续在正文末尾留时间占位。媒体在上、说明文字在下时可把文字作为尾部内容。来源：`history_view_message.cpp:7287-7315`。

RTL 边界：静态文本以“末段方向是否不同于界面方向”判定是否在占位前另起行；末尾是引用块也强制另起行。不能笼统写成“所有 RTL 正文必定另起一行”。流式文字显现的分支另外要求末行非RTL才把时间留在当前显现行。来源：`L/text/text.cpp:717-734,963-972`；`history_view_message.cpp:7174-7181`。

### 有回应时：最后一行回应让时间

气泡内回应下方由普通信息布局负责时间时，回应列表获得 `S×K` 占位；图片自行画时间的路径移除这块占位。先使用完整W给胶囊分行，再检查最后一个胶囊，不能一开始把每一行都缩成 `W−S`。来源：`history_view_message.cpp:5522-5523,7227-7246,7305-7312`；`history_view_reactions.cpp:375-418`。

令最后一行最后胶囊右边界在列表局部坐标中为 `e`。其后剩余宽度为 `W−e`，时间留在这一行的精确条件是 `e+S≤W`；大于W才在整个列表末尾增加高度K。这里比较的是W本身，胶囊分行比较的是 `L+W`；L=−3要保留，不能把两个条件合并成同一个右边界。时间不会被移到第一行，也不会为它把最后胶囊倒挪到前一行。来源：`history_view_reactions.cpp:382-418`。

若共有n行胶囊，则胶囊区域高 `R=22n+4(n−1)`。列表总高为 `R` 或 `R+K`，由上述条件决定。普通无附加条时，回应列表底部距气泡底8；时间块下边界距气泡底3。因此时间行顶相对列表原点为 `列表总高+5−F`，基线再加A。同行时最后胶囊底比时间行底高5 DIP；增加K时，时间行顶恰好是胶囊区域底R，不额外再插4 DIP行间距。来源：`history_view_message.cpp:2024-2045,5432-5451`；`history_view_reactions.cpp:415-418`；`chat.style:25,100`。

普通没有 view button / 媒体附加状态行时，回应区与上方内容间隔5；不是正文一结束就紧贴回应。媒体另有 additionalInfoString 时，测量改用日期行高F隔开其状态，绘制还结合该媒体矩形和5 DIP skip；不得继续把所有媒体简化成普通“正文+5”模板。来源：`history_view_message.cpp:1545-1552,2026-2034,6854-6863`。

### 特效与其他底部条的边界

消息特效小图标与 emoji 回应胶囊是不同区域：特效自然宽为 `15+3=18`，图像12居中放入15槽；信息自然宽包含它。宽度不足时特效另占日期行高F，靠信息块右侧，日期、浏览数和勾本身不拆成多行；消息高度另加 `信息实际高−F`。来源：`history_view_bottom_info.cpp:112-160,381-390,423-427,463-474,588-621`；`history_view_message.cpp:6906-6907`。普通同行公式的常用情形为信息单行；带特效多行时仍用完整Bmax作S，另叠加这段高度，不能把特效当回应列表一项。

评论按钮、view button、行内键盘在消息壳另有高度与位置；时间以扣除对应底条后的inner矩形定位，回应也要按同一内壳计算。view button 会改变回应顶部留白，不能让固定footer贯穿所有这些状态。来源：`history_view_message.cpp:1931-1955,2021-2068,2191-2204,6910-6936`。本规格不定义这些按钮自身的画法。

## 图片与状态矩阵

图片是否占气泡底部决定信息归属。`Photo.customInfoLayout=true`；照片处于底部时用 Image 信息，回应置于气泡外；图片上方只有标题/正文并不会把它变成普通文字时间布局。反之，媒体上方、说明文字在下，媒体已不处于底部，则普通信息归消息壳，回应嵌入气泡。决定顺序依赖可见正文、媒体位置及媒体自身布局能力，不能只判断“消息含图片”。来源：`history_view_photo.h:89-96`；`history_view_message.cpp:6296-6373,7231-7246`。

| 状态 | 回应与时间位置、变化 | 依据 |
|---|---|---|
| 别人发的普通正文，无回应 | 时间在末行右下，放不下才换行；无收据 | `history_view_bottom_info.cpp:277-294`；`history_view_message.cpp:7287-7315` |
| 自己发的普通正文，无回应 | 同上，时间右侧多24 DIP收据槽；从单勾变双勾不改变槽宽 | `history_view_bottom_info.cpp:277-294,588-596` |
| 普通正文，有一行回应 | 正文不再保留时间空槽；回应左、时间右；e+S≤W就共享这一行 | `history_view_message.cpp:7300-7315`；`history_view_reactions.cpp:415-418` |
| 回应折成多行 | 时间只跟随最后一行；最后一行不够时再增K高时间区；前面各行仍用完整W | `history_view_reactions.cpp:375-418` |
| 带“已编辑” | 文字前缀增宽D/B/S，重新测量并可能触发时间换行；字体/基线不变 | `history_view_bottom_info.cpp:485-524`；`history_view_message.cpp:5556-5559,7327-7353` |
| 开启主日期显示编辑时间 | 使用“编辑于时间/日期”的本地化日期文本，而非总加一个edited前缀；保存转发日期另有优先级 | `history_view_bottom_info.cpp:72-83,485-508,694-699` |
| 没有可见正文，有媒体与回应 | 不生成空正文行；由媒体决定普通信息还是图上时间；图片底部路径的回应距媒体/壳底5 DIP另起区 | `history_view_message.cpp:1531-1559,6816-6864,6922-6929`；`history_view_photo.h:91-93` |
| 只有回应，且无正文/媒体/其他有效内容 | 不是普通有效文字气泡：HistoryItem.isEmpty 不把回应计为内容，drawBubble 不为它造空文字壳；不得从此输入虚构一个Telegram普通气泡样式 | `history_item.cpp:4938-4944`；`history_view_message.cpp:5890-5902` |
| 无下方说明文字的照片；或正文在照片上面 | 时间在图右下半透明底板；回应在外部区，不占图上时间宽；出站外部回应按右侧布局镜像位置 | `history_view_photo.cpp:582-594`；`history_view_message.cpp:1931-1944,6296-6373,6922-6929` |
| 照片在上、说明文字在下 | 照片不再自画底部时间；说明文字无回应时避让时间，有回应时由回应最后一行避让 | `history_view_message.cpp:6303-6306,6359-6373,7231-7246,7287-7315`；`history_view_photo.cpp:583` |
| 无说明文字的网格相册 | 单个图上信息块位于整个相册右下，不每张各画一个；回应在外部区 | `history_view_media_grouped.cpp:491-502`；`history_view_media_grouped.h:87-90` |
| 有下方说明文字的网格相册 | 与下方正文的普通信息布局一致；实际说明文字项由组内容决定 | `history_view_media_grouped.cpp:106-126,930-947`；`history_view_message.cpp:7231-7246` |
| 消息整条选中 / 我已回应 | 仅按颜色表切换对应角色；计数/头像变化会重测宽，颜色切换不改变几何 | `history_view_reactions.cpp:209-231,492-548` |
| 隐藏底部信息的欢迎模板、媒体编辑器假消息 | S=K=0，不保留时间槽；媒体编辑器也不构建回应列表 | `history_view_element.cpp:1593-1596,1610-1621,2979-2985` |

照片上的时间不是全时常驻：发送中、失败、鼠标在该消息上、聊天预览、或该消息是非“收藏夹”聊天最后一条且出站时显示；其他普通静态照片可不显示。网格相册还包括视频处理中状态。文字气泡时间不采用这条鼠标显隐规则。来源：`history_view_photo.cpp:1094-1102`；`history_view_media_grouped.cpp:950-958`；`history_view_element.cpp:1541-1548`。

Saved Messages 的 tag 另有标签形状，不能套普通11半径胶囊：左半径6、右半径3、箭头5，标签12 regular，文字位置(26,2)，标签专用skip6；有标题时还加普通skip3。付费回应仍可用普通胶囊几何，但气泡内底色为 `creditsBg3=#f0b400`，外部已选为 `creditsBg2=#ffd951`，未选文字为 `creditsFg=#ba7000`。来源：`chat.style:677-686`；`history_view_reactions.cpp:48-76,261-269,337-361,499-548,648-739`；`L/colors.palette:686-688`。这些是官方特殊状态，不引入64Gram消息ID或扩展菜单。

## MewTypeUI 现状对照

本节是源码审计，描述未改动的实现；不把已有测试通过当作符合本页的证据。

| 现状不符项 | 当前绘制 / 组合位置 | 与本规格的具体差异 |
|---|---|---|
| 1. footer 强制把时间另起一行 | `chat/bubble.go:46-48,78-92`；实际聊天组合 `gallery/layout.go:215-229` | 只要Footer存在就清空正文Info，footer之后再建空Body；没有e+S≤W的同排协商。正文、回应、时间成三段 |
| 2. 回应行不知道信息占位 | `content/reactions.go:22-33,104-106` | 普通Wrap只排胶囊，没有S/K或最后一行剩余宽度合同，不能独立修正上项；外壳和回应必须共享测量结果 |
| 3. 自定义正文不参与尾部避让 | `chat/bubble.go:74-84`；`chat/body.go:19-39`；`gallery/layout.go:223-229` | Body只接收纯字符串，Markdown/媒体插入content后，空Body的时间占位不会参与该正文末行；无回应的自定义正文也另生时间行 |
| 4. 收据宽度、图标尺寸和竖向位置 | `chat/body.go:25-30,39-56` | 图标统一取时间字号、只加该宽，Row排在文本后；Telegram槽宽24、不同画布和基于F的偏移。导致B/S和换行边界错误；Failed红叉也不同于官方此路径 |
| 5. views/edited/signature 缺少分项几何 | `chat/bubble.go:27-32`；`chat/body.go:24-25,40-41` | 单Label可承载编辑/签名字符串，但不能表达浏览/回复图标20槽、右8间距、固定顺序、144签名限宽或发送中隐藏计数；宿主塞字符图标不能等同本页 |
| 6. 回应左移与顶部5 DIP缺失 | `content/reactions.go:33,43`；`chat/bubble.go:86-91` | 气泡内容左沿直接开始，没有−3；Footer无来源中的上方5 DIP间隔 |
| 7. 胶囊高度随字体/头像撑大 | `content/reactions.go:44-50` | 计数使用max(18,字体高)，头像用20+上下2而变24；Telegram两者均22，头像用专用1留白。字体实际高于18时也存在偏差 |
| 8. 头像宽、位置与轮廓 | `content/reactions.go:43,84-95` | 仍用普通gap3/右7及普通上2，代替专用左右1/上1；用气泡色实体Border，官方是透明擦边1。宽度因此影响折行 |
| 9. 计数与顺序 | `content/reactions.go:35-42,94-95` | strconv.Itoa总画数字含0，没有零计数28宽分支或缩写文字入口。组件直接使用输入顺序，宿主目前没有官方排序资料；计数ContentTitle=13/600已正确 |
| 10. 图片信息缺完整绘制入口 | `content/media.go:36-48,120-150,175-184,186-240`；`chat/bubble.go:49,79-80` | mediaBadge只画左上GIF/时长，不接时间/浏览/收据；HideInfo只是跳过壳时间。没有右下Image时间底板、其显隐和媒体底部归属协商 |
| 11. 气泡外回应缺角色与右对齐模式 | `content/content.go:13-14`；`content/reactions.go:51-75` | 只有Outgoing/Selected状态，所有回应按In/Out气泡色×0.12/0.18处理；无服务底色/前景反转及右侧外部回应位置翻转 |
| 12. RTL末段判断过于简化 | `chat/body.go:31-33,92-103` | 末段RTL一律加换行，未比较末段方向与界面方向，也不能识别末尾引用块；与静态官方规则不同 |

已相符的部分：普通图标槽18、图片画布32、计数13 semibold、图文间距3、项间/行间4、普通入出站与我已回应/消息选中颜色角色，以及普通纯字符串Body的不可见占位思路。位置见 `content/reactions.go:31-46,51-83,94-100`、`chat/body.go:37-39`；这些局部一致不能抵消以上组合差异。

后续实现需要把本页未登记的尺寸加入现有theme生成源，而不是在chat/content写字面量；现有访问器范围见 `theme/metrics_gen.go:350-374,485-502`。尤其缺少inBubbleLeft、userpicsPadding、emptySkip、收据/浏览/签名槽。此任务只登记需求，未更改主题JSON契约。

## 验收输入与本次核对边界

以下是后续绘制实现的验收条件，不是本次已经通过的UI测试。只使用编造消息和宿主准备的资源；同版本官方截图对照按仓库≤1 DIP要求进行。

| 场景 | 必须观察的结果 | 来源规则 |
|---|---|---|
| 正文末行剩余S、S−1 | 前者同排，后者占位换行；换行只增加K | `L/text/text.cpp:1300-1315,1368-1391` |
| 最后一行回应e+S分别为W、W+1 | 前者时间在该行右下，后者列表增K；前几行胶囊位置不因时间预留而改变 | `history_view_reactions.cpp:402-418` |
| 同一消息切入站/出站、single/read、edited、浏览数位数变化 | 入站无勾；出站槽24；单/双勾等宽；D/V变化确实触发重新测量 | `history_view_bottom_info.cpp:277-379,485-577,588-621` |
| 一、二、三行回应，含零计数、1/2/3头像 | 22高、26行步进、头像宽公式成立；时间在最后一行或其下，不倒回第一行 | `history_view_reactions.cpp:328-418,597-616` |
| 照片无下方文字 / 照片下有说明 / 文字在照片上 | 区分图上时间与普通信息；外部回应不与图上时间争宽；hover前后按状态显隐 | `history_view_photo.cpp:582-594,1094-1102`；`history_view_message.cpp:6296-6373,7231-7246` |
| 明暗、消息选中、已回应、100/125/150%缩放 | 几何源值不随颜色改变；字体按平台实际度量；不裁掉32表情画布的透明边 | `history_view_reactions.cpp:469-581`；`chat_style.cpp:797-806` |

本次证据范围：逐段读取上述源码和本库对应绘制；核对两个外部仓库HEAD；读取8个PNG的尺寸元数据；对文档引用、样式值、同行边界公式做静态核对。未运行Telegram或改动画廊，未作截图叠图、UI/性能测试、故意改坏检查或真模型视觉验收；文档任务不以这些尚未实施的结果冒充绘制通过。“我定的”仅包括本页符号命名、表格组织和后续验收输入；没有自定新的视觉数值。

## a2-impl 实现登记

实现只读取本规格、已有 Go 和 mygo v0.7.4 的 MIT 公共布局/塑形接口，没有打开 tdesktop/lib_ui 源码。另研究 CrychicUI 的 chat/reactions.go 与 markdown_message.go：借鉴交互根使用 ButtonBase、内容保持宿主准备、显式 Markdown 状态的接口思路；未借入 Go 实现。实际数值和布局决策来自本页。

现状不符清单 1–3：Footer 改接 S×K；不再清空 Info 后创建空 Body。BubbleOptions.Body 将尾部占位传给 Markdown，正文后有回应时移交 Footer。4–5：独立24收据槽、20浏览/回复槽及8间距，固定图标画布/底边偏移；Failed 使用时钟并隐藏计数；Prefix、Signature、Views、Replies 分项测量，签名上限144且保留日期。6：回应首项左移3，上方间隔5。7–9：胶囊固定22高；头像使用1留白、20尺寸/13步进/透明擦边；零计数宽28、CountLabel 按宿主短计数字符串测量，顺序仍由宿主交付。10–11：Photo/Album 的 BottomInfo 插槽、图上背景/前景与显隐状态；MediaBottom 将回应放在壳外，外部服务色和出站镜像。12：末段方向与界面方向比较；Markdown 末尾引用不嵌入时间。

新增25个主题 token 登记在 metrics.tsv，经 `go run ./internal/cmd/themegen -metrics-only` 生成；此模式只读本库登记表，不接触 GPL 来源。快照格式为 v6。

## a2-impl 验收实测

mygo v0.7.4，Windows系统字体，100%DIP：F=17.291016，K=12.291016。入站时间B=30.849609、S=40.849609；出站增加24槽，S=64.849609。单个「+ 1」胶囊38.230469宽；入站同行临界W=76.080078，出站W=100.080078，各少1 DIP都只增加K。1/2/3行回应高22/48/74，溢出时间后34.291016/60.291016/86.291016。纯文本与Markdown边界70.791504→69.791504时，高17.291016→29.582054，增加K的误差约0.000023。

完整气泡入站/出站实测一致：正文+单回应60.291016；带edited仍60.291016；200×120照片+外部回应163；照片下说明+回应180.291016；无回应的短正文33.291016；只有回应的无效消息壳0。实际日期下沿比胶囊低5，默认字体的日期/数字基线差7.708984；不能把同行误写成基线相同。

31条新增断言语句逐一反转均红，包含12个消息状态高度路径；4个实现故障（恢复整行正文时间、丢失回应占位、头像撑高、图片失去4内缩）均被回归捕获。全库CGO_ENABLED=0测试、Go vet、mygo v0.7.4 vet通过。chat/bubbles和layout/chat明暗原生启动、滚动截图、选中和提交清稿/新消息可见位置验证完成；96 DPI，18张PNG含6张100/125/150%软件渲染，不冒充这些比例的原生显示器验收。

性能依据共享《标准：性能与效率（2026-11-03）》的Google RAIL应用绘制P95≤10ms。明暗×三比例×三遍×200次软件重建/渲染，共3600次，P95范围1.506–4.508ms，最大5.513ms；稳态约1441B/次。无等待吞吐下CPU接近单核满载，不能当空闲CPU。原生气泡页每主题预热20次后3×200次、固定25Hz切换选择：控件树往返P95为3.852–4.715ms，最大52.293ms；单核进程CPU7.62–9.18%。1188个活动帧P50/P95/P99为1.7/2.3/2.7ms，最大6.1ms，>100ms为0；分配P95为123.8KiB。另保留4个启动首帧213.3–215.5ms，主要为present。主进程工作集/私有提交采样最大90,644,480/137,539,584B。均非物理输入到屏幕或DXGI错失率，未量GPU显存、十分钟空闲或长期驻留。

原生自动化版/无inspector发行版分别14,377,472/13,820,416B。测量、故障覆盖、构建与截图均在任务指定scratchpad/a2-impl，不进入库；文件清单和完整日志随该目录交付。模型截图复核的DeepSeek与agnes均未给最终正文，agnes耗尽4096推理token；另一次几何请求60秒超时。这些失败不算验收通过。后续agnes真实几何请求于4512ms返回34.291016（finish=stop，376输入/219输出token），与一DIP不足时的实测高度一致；这仅交叉核对算式，不代替视觉验收。


## 待补

- 原始用户截图未随任务提供，本次不能量与官方同版本截图的区域差异；保留几何边界实测和画廊截图，不能将其称为官方叠图通过。
- reaction-size 已补全 [资源尺寸合同](reaction-size.md)：默认无专用透明边位图按16绘制，完整中心画布显式32、自定义帧20；Unicode系统文字按18槽拟合。旧的“任意宿主位图保持32画布”合同已移除。官方centerIcon的实际墨迹范围仍依赖素材，不能从样式推断。
- 我定的：mygo 的对象独占行会保留默认字体 descent。给不绘字的段落种子及尾部 Span 使用0.0001 DIP，所有可见文字显式保留主题字阶，避免独占尾行被正文行高撑大。量得增加高度与K差约0.000023 DIP；此值是数值排版适配，不是可见字号。
- 我定的：Info.Prefix、Label 和计数短字符串由宿主按本页优先级完成本地化；组件不推断协议上下文/日期或对回应排序。暂无官方语言格式字面量的进一步规格。
- 我定的：规格只明确正文、末尾引用和媒体的尾部避让，没有定义 CommonMark 列表/表格/代码围栏的对应关系。沿用末尾引用的已有规则：非直接文本块后另放K高占位；不猜测 Telegram 未定义的 CommonMark 映射。普通段落和行内样式仍参与真实塑形避让。

