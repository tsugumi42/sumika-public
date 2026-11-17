# 合并转发摘要卡画法规格

ctl-spec；版本、T/L、DIP、RTL和净室边界沿用 [settings-section](settings-section.md#坐标来源与边界)。整卡是“我定的”，登记 [CTL-FORWARD](../exceptions.md#ctl-forward)。Telegram普通Forward头没有“标题+三行摘要+共N条”的封装；参考WebPage的引用底板、单/双行标题、总行预算和方向/选中配色，而非把QQ节点浏览器搬进组件。来源 `T/ui/chat/chat.style:107-108,151-153,530-535`、`T/history/view/media/history_view_web_page.cpp:801-890,1004-1044,1244-1293`。

## 数据边界（我定的）

宿主提供稳定卡ID、标题、按稳定节点ID排列的准备好摘要、格式化CountLabel、Ready/Loading/Failed状态、错误、可用性与OnOpen/OnRetry。总条数不等于摘要条数；“共N条”由宿主本地化，N=0也可以是合法空集合。不解析远端协议、拉节点、检测循环、维护转发栈或打开原消息；这些都归宿主。卡内不得包含固定QQ术语。

## 数值、形状与配色

| 项 | 组合 / 来源 | theme |
|---|---|---|
| 最大宽 | msgMaxWidth430，仍受父内容实际宽约束 | `T/ui/chat/chat.style:14`；message.maxWidth |
| 内padding | historyPagePreview=(10,5,7,7) | 同文件 `:107-108`；preview.padding* |
| 引用轮廓 | historyQuoteStyle.radius5、outline3；背景.12、条.9 | `T/chat_helpers/chat_helpers.style:1285-1287`；`T/ui/chat/chat_style.h:56-57`；quote.* |
| 标题 / 正文 | webPageTitleFont13 semibold / webPageDescriptionFont13 regular | `T/ui/chat/chat.style:530-531`；contentTitle / body |
| 区块间距 | mediaInBubbleSkip5 | 同文件 `:156`；preview.mediaGap |
| 标题/摘要行预算 | 标题最多2、摘要最多3 | 标题2来自 `T/history/view/media/history_view_web_page.cpp:807,869-890`；摘要3和分开预算为我定的 |

局部W≤430，内容宽C=W−10−7。根为引用样式底板，不再额外套白卡和阴影；消息壳由宿主chat.Bubble拥有。条画在起始边，宽3、与底板圆角按已有Quote轮廓规则相接；填色取对应入/出站replyBar色×.12，条同色×.9，消息selected则用该方向selected角色。复用 [content](../content.md) 的引用背景，不复制上游可收藏颜色/背景emoji；本卡不自动按来源人名生成新颜色。

## 内容排版（我定的）

标题原点(10,5)，最多两自然行，最后一行尾省略。标题后有摘要才留5；按给定顺序把摘要文本排进最多三条可见文字行，软换行计行，硬换行计行；总预算是三行而不是每个节点各三行。每项自身可带宿主提供的发送人前缀，组件不拼领域名。最后可见行有未显示内容时省略；不能先按rune截断再塑形。

CountLabel在摘要（或无摘要时标题）自然底后留5，13 regular、对应入/出站日期色，一行尾省略；完整名称交辅助技术。无摘要不留空三行，标题为空不造一行；主内容总高为5+非空块实际高+块间5+7。数字变化重测；CountLabel与消息底部Info是两份不同内容，后者的时间/回应避让继续交 [message-bottom](message-bottom.md)，不得把“共N条”塞进Info.Label冒充时间。

这是WebPage画法的衍生组合：官方普通网页总预算通常5行且标题最多2，摘要用剩余预算，本卡固定独立3行摘要另加count，因此不能声称行预算完全等同WebPage。没有宿主缩略图时不画图片空槽；带媒体摘要需另由宿主使用现有content.Media，不扩展本页为相册。

## 状态与动作（我定的）

| 状态 | 展示 / 交互 |
|---|---|
| Ready | 完整标题/摘要/count；有OnOpen时整卡一个按钮目标，无回调则只读 |
| Loading | 保留已知标题/count/旧摘要；状态块显示宿主加载文案，位置在count后5；不把旧摘要清成“空转发”；禁用打开 |
| Failed | 保留已知内容，count后5显示错误，色semantic.danger；仅宿主允许重试时提供LinkButton“重试”，与错误间5；重试是独立按钮 |
| Disabled | 不激活打开/重试，数据仍可读；父禁用按mygo0.5衰减 |

hover不改变引用底，按下使用现有Quote/ripple颜色与650/200ms；只在Ready可打开时启用整卡ripple。错误重试不得冒泡再触发OnOpen；Enter/Space与鼠标同样只交ID，不自行开启浮层。保留状态ID、焦点句柄和宿主详情窗口；节点迟到不改变当前卡身份。无默认入场、加载spinner、自动重试或提示音。

## 后续验收输入

0/1/3/超3摘要、单项多行占预算、长标题两行、Unicode/RTL省略、计数0/多位/本地化；Loading保留旧数据、Failed可/不可重试、Ready只读/可打开；消息方向/selected、明暗/三档缩放。量padding10/5/7/7、引用5/3轮廓、三行总预算和5间隔；时间不被count替代。未运行本页UI。

## theme 要补的

| 建议键 | 数值 / 来源 |
|---|---|
| forwardSummary.maxSummaryLines | 3（我定的总行预算；以WebPage5总行、标题2为参照，`T/history/view/media/history_view_web_page.cpp:805-807`） |

其余复用message.maxWidth、preview.padding*/titleLines/mediaGap、quote.radius/outlineWidth/backgroundOpacity/outlineOpacity和入出站/selected角色；正文与标题字阶已有。不重复新增同值几何。错误/加载文案与节点数不是theme，截断行数属于展示配置而非业务节点上限。
