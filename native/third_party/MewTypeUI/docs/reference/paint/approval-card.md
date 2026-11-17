# 通用审批卡画法规格

ctl-spec；版本、T/L、DIP、RTL、字体度量与净室边界见 [settings-section](settings-section.md#坐标来源与边界)。本页整卡为“我定的”，登记 [CTL-APPROVAL](../exceptions.md#ctl-approval)。Telegram没有“AI工具审批卡”这一业务封装；参考官方Box的标题/正文分区和行内bot键盘的并列动作、忙状态表达，不把审批改成默认确认对话框。依据 `L/layers/layers.style:73-87,119`、`T/ui/chat/chat.style:281-286`、`T/history/history_item_components.cpp:1096-1164,1306-1356`、`T/history/view/history_view_element.cpp:219-304,332-409`。

## 数据与边界（我定的）

宿主提供稳定审批ID、标题、摘要、详情内容、expanded、状态、当前提交动作ID、error、两个动作标签和可用性；通用动作ID表示允许一次/拒绝，业务执行、权限、是否允许重试、审批是否仍有效全部归宿主。默认示例文字“允许一次 / 拒绝”必须可本地化覆盖，不出现QQ/Sumika专用字段。不读取工具参数文件，不自行执行工具；细节可由末尾content槽构建，容器仍返回根Element。

## 外壳、标题与详情（我定的）

| 区域 | 组合数值 / 现有来源 |
|---|---|
| 外壳 | 宽=min(父可用宽,dialog.wideWidth364)，圆角dialog.radius8、底boxBg；无描边/阴影；`L/layers/layers.style:38,118` |
| 水平padding | 24/24，boxRowPadding；`L/layers/layers.style:87` |
| 正文上下padding | 14/8，boxPadding；`L/layers/layers.style:119` |
| 标题 | 16 semibold、boxTitleFg；`L/layers/layers.style:73-79` |
| 摘要/详情/错误 | 14 regular、普通boxTextFg/错误boxTextFgError；`L/basic.style:56-57`、`L/colors.palette:136,138` |
| 块间距 | defaultVerticalListSkip6；`L/basic.style:130` |

局部宽W、正文内宽C=W−48。标题从(24,14)自然换行；摘要存在才在标题自然底后留6，并排其完整文字；“详情”切换入口使用 [link-button](link-button.md)，从上一块底后6开始。详情采用 [collapse](collapse.md)，展开后在入口底后6绘制，按实际宽C测高；折叠不保留6的空洞。无详情不造空入口。

错误/进行中说明放在详情之后、动作区之前，相邻非空块间6；自然换行，不盖住按钮。整卡高=14+各可见正文块自然高+实际间隔+动作间隔2+动作区高+8；动画仅详情块150ms linear改变整卡高，不缩放/淡入整个卡。标题/摘要自身不自动截断安全信息；极长详情由宿主外层滚动，不暗设三行上限。

## 两个动作（我定的）

借用bot内联按钮的height36、padding10、gap2、textTop8，`T/ui/chat/chat.style:281-286`。动作区宽C，两列各 `(C−2)/2`，左“允许一次”、右“拒绝”（RTL镜像），label使用serviceTextStyle=13 semibold，与官方bot文字同级，来源 `T/history/view/history_view_element.cpp:187-191`、`T/ui/chat/chat.style:18-20`。每块圆角取message.radiusSmall6；两列不与卡外壳8混用。

默认允许按钮填botKbInlinePrimaryBg、拒绝填botKbInlineDangerBg、文字windowFgActive；hover叠msgBotKbOverBgAdd，按下ripple=msgBotKbRippleBg、650 Quint/200 linear，均裁各自圆角。颜色来自 `L/colors.palette:87-88,435-437`，填充选择参考 `T/history/view/history_view_element.cpp:246-289,293-304`。不使用“绿色允许=自动推荐”的额外图标。

文字两边各留10、单行居中；若任一标签自然宽+20大于列宽，则两按钮改上下排列、各全宽，高度仍36、间隔2；若单列仍不足，按钮按实际字行高+上下8增高、标签自然换行。窄宽和增高是本库可访问性扩展，不是官方bot键盘分配算法。按实际内容重新测，不将两个按钮都截成同一句省略文案。

## 状态机与交互（我定的）

| 状态 | 可见与行为 |
|---|---|
| Awaiting | 两动作可用；详情开关独立；Enter只激活当前明确聚焦的按钮，整卡没有默认允许动作 |
| Submitting | 两动作均禁用；保留原标签与整行几何，在状态块显示宿主“正在处理…”；详情仍可展开查看；不按重构建重复提交 |
| Failed | 显示宿主错误；宿主声明可重试才恢复两个动作；不自动倒计时或重新发送 |
| Resolved / Expired | 保留标题/详情和宿主最终说明，两动作禁用；只有宿主移除卡片才退出，不因点击已回调就提前消失 |

官方内联忙状态用轮廓glare（减少动效时发送时钟），见 `T/history/view/history_view_element.cpp:332-409`；本卡选择显式状态文字避免复制动画素材，不宣称已复现该glare。状态不清展开值；两个动作在同一个审批ID上最多有一项在途，宿主给Submitting是事实来源。Tab顺序为详情入口、允许、拒绝；失效的聚焦动作移回卡入口。卡片本身无整面点击、无Enter默认确认、无退出时默认允许。外部禁用和mygo0.5衰减沿用SET-DISABLED。

## 后续验收输入

折叠/展开、无/长详情、长标题/标签导致两列转单列；Awaiting→Submitting→Failed→重试/Resolved/Expired；连点与键盘只报告一次意图；审批ID切换不串状态；取消/迟到结果归宿主验证。明暗/RTL/三档缩放，量364上限、24/14/8留白、36按钮和2间隔。未实施审批业务或UI。

## theme 要补的

| 建议键 | 数值 / 来源 |
|---|---|
| approval.actionHeight / actionGap / actionPadding / actionTextTop | 36/2/10/8；`T/ui/chat/chat.style:282-285` |

宽364、圆角8、正文24/14/8、gap6、文字字阶、message.radiusSmall6和所有palette颜色已有。允许/拒绝tone映射与响应式排法属于CTL-APPROVAL，不把业务状态进theme。官方bot glare未采用，无相关动画token；详情用新增motion.collapse。
