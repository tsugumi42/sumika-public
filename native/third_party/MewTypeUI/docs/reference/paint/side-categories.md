# 侧边分类导航画法规格

ctl-spec；版本、DIP、T/L、RTL及净室边界沿用 [settings-section](settings-section.md#坐标来源与边界)。参考官方设置入口的图标+标题整行与显式页面栈；API参考CrychicUI `navigation/vertical_tabs.go:10-41` 的独立选择和内容分离，但采用稳定ID、不借其下标身份、2DIP活动条或Snappy。官方本版设置分类在Main的内容列表，不是常驻“左分类/右详情”VerticalTabs：`T/settings/sections/settings_main.cpp:364-449,727-777`；进入/返回由 `T/info/settings/info_settings_widget.cpp:66-74` 处理。常驻侧栏组合标 [CTL-CATEGORIES](../exceptions.md#ctl-categories)。

## 复用的行外观

普通设置分类的画法、hover/down/ripple、disabled、图标和文字槽完全引用 [settings-button](settings-button.md)，段头/分隔引用 [settings-section](settings-section.md)，不建立第二套行高。带图标40高、图标left20、标题left60/top10、右22；无图标38高、标题left22/top10、右22。背景铺整栏，不是有外间隔的圆角卡片。来源 `T/settings/settings.style:13-27`、`L/widgets/buttons.cpp:977-1055`。

## 常驻分类组合（我定的）

宿主给分类ID、标签、可选图标、disabled、selectedID和OnSelect；容器宽由父布局给，推荐默认最小260取layout.listMinWidth（`T/window/window.style:34`），不复制CrychicUI的192定宽。内容连续排，无分类时占空容器但不自造“设置”业务标签；标题与说明由宿主提供。图标/标签重复不影响ID。

已选项使用下表显式扩展配色，不画另加的左竖线。官方infoMainButton只是lightButtonFg/Over的semibold动作行，并无selected类别语义（`T/info/info.style:621-625`）；不能把settingsButtonActive这个样式名当成持久选中合同。hover仅视觉反馈，点击/Enter/Space报告ID，宿主接受后才换selectedID。可选尾箭头沿用SET-ROW-ARROW；已选状态不使箭头成为独立按钮。

| 选中角色 | 对应 / 来源 |
|---|---|
| 前景 | 本组合复用windowActiveTextFg（已有window.activeTextForeground）；`L/colors.palette:20` |
| 背景 | 本组合复用windowBgOver（已有window.backgroundHover）；`L/colors.palette:11` |
| selected+hover/down | 保持选中底，叠整行ripple；不发明第二个selected-hover数值 |

上表是组合选择，不能声称infoMainButton已经替本库定义“选中类别”。行本身没有持久selected语义；因此新选中行为、配色用途、260最小宽一并登记CTL-CATEGORIES。所有几何仍来自现有设置行和窗口数值。

## 滚动、状态、焦点

列表超高在自身视口竖滚，分隔与组标题随内容滚动；分组不是额外可选项。按ID保留选中、各详情页滚动和编辑状态；宿主可惰性加载页面，组件不因被构建就查询。切页不销毁状态，也不自动保存业务数据。窄布局改为分类页→详情页，复用layout页面切换与返回合同，断点由容器/宿主决定，不新增一套固定窗口阈值。

键盘Up/Down/Home/End在可用分类间移动焦点，Enter/Space激活；focus和selected分开，disabled项跳过。分类被删除时宿主显式选择目标，不能按原下标误选另一类。当前焦点项收起/消失时转到分类容器或当前可见入口，持久存ui.Handle；ReduceMotion直接终态。均为CTL-CATEGORIES通用映射；默认不为分类切换、hover或选择播放提示音。

## 后续验收输入

有/无图标、长标题、重复名字不同ID、分组、禁用当前项、删项/重排、过长滚动、窄布局返回、焦点与selected不同；普通/hover/press/selected；明暗/RTL/三档缩放。量40/38行高、20/60/22槽，类别变化不丢右页草稿。尚未实施本页UI。

## theme 要补的

无需新增默认尺寸或动效：settings.button*、layout.listMinWidth、motion.page和ripple已有。选中颜色直接采用现有window.activeTextForeground/backgroundHover；用途映射在CTL-CATEGORIES登记，不新增猜测的侧栏间距/活动条。类别数组、断点、页面状态不进theme。
