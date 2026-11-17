# 下拉与弹出选择画法规格

ctl-spec；版本、T/L、DIP、RTL与净室边界沿用 [settings-section](settings-section.md#坐标来源与边界)。可搜索长列表已经由 [choose-box](choose-box.md) 定义，本页不重写。短选择参考官方SettingsButton当前值入口、PopupMenu和Menu::Action；不是浏览器原生select。来源 `T/settings/settings_common.cpp:485-532`、`L/widgets/menu/menu_action.cpp:51-64,98-150,173-195`、`L/widgets/popup_menu.cpp:535-541,617-659`。

## 入口

设置行使用 [settings-button](settings-button.md) 的38/40高、当前值右23、可选箭头扩展，长值先截断。独立入口使用 [round-button](round-button.md) Light变体、当前标签单行；选择箭头采用 [icon-button](icon-button.md) 的允许分发图形并占显式槽。前者已有规格/实现，后者和“选中ID→菜单项目”组合是 [CTL-SELECT](../exceptions.md#ctl-select) 的我定的契约。

入口显示的是宿主选中ID对应标签；label、ID独立，重复label合法。未选中时显示宿主占位，列表为空时不可打开菜单；不能伪选第一项。宿主正在加载/失败时显示其真实文案，不造空结果。disabled阻止打开和交付选择。

## Popup几何与画法

| 项 | 常量 / 值 | 来源 / 已有theme |
|---|---|---|
| 普通项 | defaultMenu.itemPadding=(17,8,17,7)，13 regular | `L/widgets/widgets.style:969-971`；menu.padding* / typography.menu |
| 项高 | 8+实际字体行高+7 | `L/widgets/menu/menu_action.cpp:62-64` |
| 菜单宽 | 自然宽夹156…300 | `L/widgets/widgets.style:980-981`；`L/widgets/menu/menu_action.cpp:173-195` |
| 上下留白 / 圆角 | 8/8；8 | `L/widgets/widgets.style:990-995`；menu.scrollPadding/radius |
| 分隔线 | 上5、线1、下5 | 同文件 `:952-955`；menu.separatorPadding/Width |
| 普通/高亮/禁用 | windowBg/windowBgOver；windowFg/windowFgOver/menuFgDisabled | 同文件 `:960-967` |

自然宽为文字自然宽+左右17，有快捷键/子菜单时再加6和其真实宽；绘制文字起点(17,rowY+8)，可用宽按上述额外槽扣除后尾省略。普通高亮是整行矩形，不另加圆角药丸；背景在下，ripple其次，图标和文字在上。来源 `L/widgets/menu/menu_action.cpp:98-150,173-195`。图标菜单改用54左槽和上下8，直接引用 [overlay](../overlay.md)。

当前值标记不能冒充PopupMenu自身必有的Radio。官方可勾选菜单Toggle另画ToggleView（右侧padding内居中），`L/widgets/menu/menu_toggle.cpp:30-41,58-66`；本库单选选择菜单采用现有overlay的radio/checked入口，使用 [radio-check](radio-check.md) 的22槽，而非把Toggle称作Radio，登记CTL-SELECT。选择标记的预留槽在每个项目一致，文字不因换选中跳位。

独立原生flyout、工作区翻转、滚动裁切、子菜单、200ms展开/150ms淡出、阴影平台差异、焦点回收直接沿用 [overlay](../overlay.md)。固定最终文字布局后裁切显现，不能缩放文字。下拉默认锚在入口下方起始沿，超出工作区翻向上或夹取；这是CTL-SELECT组合定位规则，不承诺菜单总与入口等宽。

## 交互与提交

打开时选中值与鼠标/键盘高亮分开；Esc/外点/失焦关闭不提交，激活可用叶项报告稳定ID并关闭，宿主接受后才改变当前值。方向/Home/End/首字母/Enter沿用现有overlay导航；禁用项和分隔线不进入可激活序列。官方Action触发前先hideMenu，来源 `L/widgets/popup_menu.cpp:535-541`；不把这一顺序当宿主保存成功。原生菜单关闭后入口仍可见时恢复焦点；主窗口销毁不延迟外部操作。

长列表需要搜索时，宿主显式选ChooseBox模式，沿用其固定搜索和键盘规则，不在打开后按长度阈值偷偷变形。独立下拉不提供多选chips或逗号串编辑。

## 后续验收输入

无值、当前值被过滤/删除、重复标签不同ID、空/长列表、禁用项/分隔/子菜单；打开、Escape/外点、Enter、点击只交一次ID；工作区四角、明暗/RTL/三档缩放。验证选值与高亮不混同，菜单不被宿主内容区裁掉。

## theme 要补的

无新增默认数值；入口复用button.*（待补见round-button），radio槽和menu各项已存在。arrow画布复用header.iconSize24，文字与arrow间隔复用menu.rightSkip6；该用途组合为我定的。搜索模式复用choose.*，不另造第二套搜索/菜单时长。
