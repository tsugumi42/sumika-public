# 可折叠区域画法规格

ctl-spec；基线、T/L、DIP、RTL及净室边界沿用 [settings-section](settings-section.md#坐标来源与边界)。参考SlideWrap以“活内容最终宽度测高、只改变外部占高和裁切”的结构，避免把展开误做文字缩放/淡入。来源 `L/wrap/slide_wrap.cpp:116-169`。外围标题/按钮继续用已有settings行，不重画。

## 样式与形状

SlideWrap没有自己的底色、圆角、边框、箭头或阴影；只包PaddingWrap和内容。默认minimalHeight=0、up=false、toggled=true；可显式padding，不能再自动加22或8。来源 `L/wrap/slide_wrap.h:68-74`、`L/wrap/slide_wrap.cpp:17-42`。动画时长slideWrapDuration=150ms，调用明确linear，而现有motion.slide=150 ease-out不是正确替代。来源 `L/basic.style:100`、`L/wrap/slide_wrap.cpp:55-68`。

## 几何与动画

对当前可用宽W，以最终宽构建内容并测自然总高H；最小保留高m，进度a=0…1。无Qt外部widget margin的mygo组合占高 `h=round(m+(H-m)*a)`；裁切框 `(0,0,W,h)`，默认内容原点不动，只从上向下显现。父布局跟随h重排，不能在全高H上仅淡出内容而留下空洞。来源 `L/wrap/slide_wrap.cpp:116-149,158-169`。

up=true时内容原点为 `(paddingLeft,paddingTop−(内容完整高−当前高))`，因此从底部露出/向上收；不是对根本身做y负位移。LTR/RTL只影响水平padding镜像，展开方向不随语言翻转。来源同文件 `:118-136`。

上游有Qt child外部margins时：动画中使用heightNoMargins、上下外margin从getMargins暂时返回0，静止完全展开恢复完整height及外margin；这是Qt布局协议，不得把本库padding当外margin删掉再加回一次。来源同文件 `:124-155`。本库“单一padding计入H、无Qt外margin双计”映射登记 [CTL-COLLAPSE](../exceptions.md#ctl-collapse)。

展开线性0→1、收起1→0，各150ms；中途反向从当前a接到目标，重计150ms，不跳端点。相同shown不重启；初始化toggleOn后finishAnimating，首帧直接终态。duration0/instant直接终态；完成回调只在该次过渡完成时交一次。来源 `L/wrap/slide_wrap.cpp:55-109`、`L/effects/animations.h:471-479`。宽度/子内容高变化时重新测完整H并用当前a求h，而非重启进度；静止展开跟随内容尺寸、静止折叠保留m，来源 `L/wrap/slide_wrap.cpp:158-169`。

## 显隐、交互和状态保留

收起动画结束后才隐藏内容；m>0时保留最小预览但不把“未展开”的全部子内容暴露给辅助技术。官方shouldBeHidden依赖!toggled且!animating，来源 `L/wrap/slide_wrap.cpp:141-147`。展开前要能测量子内容，不能先销毁再凭缓存高度猜测。

我定的通用合同见CTL-COLLAPSE：宿主保存expanded、草稿、选择/滚动；组件只持ui.Handle和动画。开始收起时若焦点在内部，移给显式展开入口，取消对隐藏目标的待聚焦请求；不可见部分不响应点击、Tab或读屏，结束后保持身份。标题按钮带expanded和controls语义；展开是独立动作，不顺便改变设置布尔值。disabled可禁止标题切换但不清内部数据。初始折叠、恢复、快速切换和ReduceMotion均不触发外部I/O。

箭头若宿主需要使用允许分发chevron，默认不另播90度旋转；没有来自SlideWrap的箭头数值来源。请求附加旋转须另立规格，不在本页自定。

## 后续验收输入

初始开/关、m0/非0、padding有/无、up正反、0/75/150ms、75ms反向、重复同值、动画中子内容长高/缩宽；内部焦点收起/恢复、点裁外/Tab/读屏、宿主状态保留；明暗/RTL/三档缩放。检查文字字号不变、150ms后无持续请求帧。未运行本页UI。

## theme 要补的

| 建议键 | 值 / 来源 |
|---|---|
| motion.collapse | 150ms linear；`L/basic.style:100`、`L/wrap/slide_wrap.cpp:63-68` |

不修改现有motion.slide的含义，以免改变其他消费者。m、padding、方向由宿主/所包内容显式提供；没有新的颜色、圆角、图标大小或统一展开高度token。
