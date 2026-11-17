# 文字链接按钮画法规格

ctl-spec；基线与坐标沿用 [settings-section](settings-section.md#坐标来源与边界)。`T/`、`L/`含义同该页。参考 LinkButton 以文字自然宽定命中框、hover换下划线字体的做法；它不经过RoundButton/ripple路径。来源 `L/widgets/buttons.cpp:63-119`。

## 几何与画法

| 项 | 常量 / 值 | 来源 |
|---|---|---|
| 字体 | defaultLinkButton.font=linkFont→normalFont，13 regular | `L/widgets/widgets.style:696-700`；`L/basic.style:53-54,104` |
| hover字体 | linkFontOver，13 regular + underline | `L/basic.style:105` |
| 常态 / hover前景 | windowActiveTextFg，两态同角色 | `L/widgets/widgets.style:697-698` |
| padding | 样式未指定；默认四边0 | `L/widgets/widgets.style:696-701`；`L/widgets/buttons.cpp:104-109` |

设padding为L/U/R/D、实际行高F/ascent A、文字宽T：自然宽 `L+T+R`，高 `U+F+D`，文字起点 `(L,U+A)`（基线坐标）。父宽不足自然宽时，按 `W-L-R` 单行尾省略；不折行。透明背景，无圆角、描边、阴影或涟漪。来源 `L/widgets/buttons.cpp:75-109`。

hover即时换带下划线字体；down不引入另一种底色或动画；退出恢复原字体。前景override优先于两种默认色。文字改变时更新自然宽和可访问名称。来源 `L/widgets/buttons.cpp:78-118`。禁止用Text加点击回调替代按钮原语而丢失键盘和禁用语义。

## 状态与宿主边界

覆盖空/短/截断文字、普通/hover/down/disabled、键盘焦点；禁用不响应激活，mygo固定0.5衰减差异沿用 [SET-DISABLED](../exceptions.md#set-disabled)。RTL镜像内容矩形；文字塑形按实际语言方向，勿按字符反转。键盘Enter/Space、焦点可见性采用mygo按钮原语，是 [CTL-COMPOSITION](../exceptions.md#ctl-composition) 的可访问性映射。激活仅交付意图；URL、安全策略、打开浏览器或网络请求由宿主执行，不在paint/构建时产生副作用。

## 后续验收输入

hover前后行框不位移；下划线出现/消失即时；窄宽省略不越界；按下拖出无激活；禁用与Enter/Space；明暗/RTL/三档缩放。未运行本页UI场景。

## theme 要补的

无需新增数值：typography.body=13/400、colors.window.activeTextForeground已有；padding0为本样式明确配置，underline为状态字形装饰，非新尺寸。没有新增motion。若将用途字阶单独暴露，只增加同源别名，不能把RoundButton的textTop8套进来。
