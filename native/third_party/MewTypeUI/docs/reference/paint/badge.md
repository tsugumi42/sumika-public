# 状态标签与 Badge 画法规格

ctl-spec；基线、T/L、DIP、RTL、字体度量及净室边界见 [settings-section](settings-section.md#坐标来源与边界)。参考官方NewBadge“自然文字宽+padding、非交互标签”和UnreadBadge“最小圆宽、字体行框居中”两种结构，避免把状态字串硬套未读数字槽。来源 `T/ui/new_badges.cpp:52-72,101-125`、`T/ui/unread_badge_paint.cpp:144-188`。

## 紧凑文字标签

| 项 | 常量 / 数值 | 来源 |
|---|---|---|
| 字体 / 前景 | settingsPremiumNewBadge=10 semibold / windowFgActive | `T/settings/settings.style:154-158` |
| padding | (4,1,4,1) | 同文件 `:161` |
| 默认底 / 圆角 | windowBgActive；半径取padding.left=4 | `T/ui/new_badges.cpp:64-70` |
| 标题后偏移 | (4,1)，仅AddAfterLabel调用 | `T/settings/settings.style:160`；`T/ui/new_badges.cpp:94-98` |

设实际字体行高F、ascent A、自然宽T：根宽T+8、高F+2；文字行顶1、基线1+A。先填4圆角矩形，再文字，无描边/阴影，高质量抗锯齿；不是半径H/2的胶囊。来源 `T/ui/new_badges.cpp:55-70`。默认标签不接鼠标/焦点，不能因放入可点击行而变成第二个按钮。

官方在按钮文字后附badge时，文字后间隔为该按钮字体的space宽，基线对齐，剩余宽不足整个badge则整块隐藏，而非截半个字。来源 `T/ui/new_badges.cpp:101-125`。独立通用标签不能因太长凭空消失：给定MaxWidth时单行尾省略、保持padding，完整可访问名称不截断；这是 [CTL-BADGE](../exceptions.md#ctl-badge) 的扩展。

## 状态语义（我定的）

Telegram的NewBadge不是连接状态组件。通用StatusBadge输入为宿主准备的label和语义tone，允许任意业务域；不内置QQ、网关、模型或连接探测。用上述10/600和4/1留白/4圆角。

| Tone | 前景 | 背景 |
|---|---|---|
| Neutral / pending | windowSubTextFg | windowBgOver |
| Active / informational | windowFgActive | windowBgActive |
| Success | semantic.success | windowBgOver |
| Warning | semantic.warning | windowBgOver |
| Error | semantic.danger | windowBgOver |

这些组合明确是CTL-BADGE映射；浅/暗均按现有语义角色，尤其semantic.success在night是蓝色，不能偷偷强改成绿色。状态一定有文字，不只靠色。pending无自动spinner，离线不自动等同error；宿主决定tone与label。不新增无来源的彩色半透明底。

## 数字Badge

未读计数直接引用 [dialogs](../dialogs.md) 的19高、左右5、12bold及普通/hover/active/muted角色，不重写现有会话行API。宽 `max(文字宽+10,19)`；文字行框在19内居中，基线再加ascent，底为胶囊，半径19/2对应上下圆弧。计数0是否隐藏/点标由宿主显式状态决定，不把状态标签内容按数字解析。来源 `T/ui/unread_badge_paint.cpp:144-188`、`T/dialogs/dialogs.style:78-80`。图形/数字并存仍由宿主交付预格式化短计数。

## 状态与动效

badge本身无hover/press、无ripple、无默认入场；宿主更改状态/文案即时绘制并重测所需宽。放入禁用父按钮时按父禁用衰减，不额外乘0.5。加载结果不在组件内推进。RTL只镜像附着位置；字串按本地方向塑形。大文字缩放按自然高F+2，不固定成12高。独立状态控件role=status，频繁更新不要每帧重复播报，见CTL-BADGE。

## 后续验收输入

短/长/空标签，各tone和重复标签；附着基线与空间不足；计数1/多位/短计数/muted/active；动态换字、父禁用、明暗/RTL/三档缩放。文字标签圆角4、F+2高与数字19高须分别量，不能全部验成胶囊。未运行本页UI。

## theme 要补的

| 建议键 | 值 / 来源 |
|---|---|
| badge.textPaddingLeft/Top/Right/Bottom / textRadius | 4/1/4/1 / 4；`T/settings/settings.style:161`；`T/ui/new_badges.cpp:69-70` |
| badge.afterLabelX/Y | 4/1；`T/settings/settings.style:160` |
| typography.statusBadge | 10/600；同文件 `:156` |

数字badge的conversation.badgeHeight19/badgePadding5和typography.badge12/700已有；semantic/window颜色已有，tone为上述用途组合，无需新增RGBA或motion。
