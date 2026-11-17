# 主题数值例外

以下均为「我定的」，不声称与 Telegram 运行绘制完全一致。

- reaction.* 已按 C++ 运行行为改为 msgFileIn/OutBg 的用途别名；普通药丸分别乘 0.12/0.18，自己回应用实色，selected 取对应 selected 角色。细节见 content.md。悬停不改变 Telegram 未声明的填色；点击使用前景 alpha 25/255 的涟漪。
- quote.in/out.background 默认别名改为 replyBar 色，绘制时乘 0.12；条同色乘 0.9。name、summary 可独立覆盖；消息选中时取该方向 selected 前景。旧 2×36 replyBar token 是历史声明，当前 Quote 不使用。
- semantic.warning 复用 statisticsChartLineGolden，semantic.success 复用 boxTextFgGood，semantic.danger 复用 attentionButtonFg；这是 mygo 通用语义角色的映射选择。night 中 success 因原角色用途而呈蓝色。
- control.spacing 采用 historyGroupSkip 的 4 DIP 作为 mygo 通用布局单位。聊天组件必须读取各自尺寸，不从这个单位推导全部布局。
- 字体的 normal / semibold / bold 转为数值字重 400 / 600 / 700；字体族空串使用系统字体。Telegram 的字体选择随平台变化，本库不嵌入字体。
- control/slide 等通用角色仍沿用原有 mygo ease-out 选择，尚未全部核实调用点；scrollbar 为 linear。page 已按 window_slide_animation.h:43-44、cpp:196-211 改为240ms easeOutCirc。dialogs、chat、content、overlay 与 compose 已核对的角色见 theme-behavior.md。布局 listCollapse 使用120ms easeOutCirc；thirdPanel 是主槽缓存页面切换的240ms easeOutCirc。第三栏并排首次开合按 mainwidget.cpp:2084-2091,2114-2118 直接重排。
- content 的按压涟漪读取 lib_ui/ui/effects/ripple_animation.cpp:86/155，使用 ease-out-quint 扩张和线性淡出；系统 ReduceMotion 下立即结束。
- 浮层的菜单 Circ 展开/线性退出、涟漪 Quint、对话框与 toast、查看器控件均已核对 C++ 调用点，见 theme-behavior.md。
- 所有表面默认 alpha=1、backdropBlur=0、glass=none、clarity=0、window.kind=none。背景保留 palette 原有 alpha；表面 alpha 是额外乘数。材质是本库扩展，不是 palette 内容。
- window.kind 仅开放 mygo v0.7.4 实际支持的 none / mica / acrylic。宿主用 Kind().Vibrancy() 设置窗口；组件用 c.Vibrancy() 决定是否采用 OpaqueBackground()。内容 backdropBlur 使用 glass.Blur，glass regular/clear 与 clarity 使用 glass.Glass。库不设置窗口，不把系统材质当作可调半径的内容模糊。
- night.tdesktop-theme 已取到。其同名项覆盖当前 palette，缺项继承浅色声明并在合并后的表内解析别名。随主题附带的 background.png 不复制、不使用；不实现壁纸色相调整。旧 night 包中已从当前 palette 移除的键不公开。对照表列出每个当前角色的来源或继承状态。
- Scrollbar.Width=4 为 defaultScrollArea.width(10) 扣除两侧 deltax(3)。mygo ScrollbarWidth 只有拇指宽度；主题同时保留总槽宽 10。显式覆盖 0 在 MewTypeUI 中保留，但 mygo 的 Spacing / ScrollbarWidth 按其接口将 0 当默认值；需要零宽滚动条的组件直接读取完整主题。
- Theme 的字号不随 Apply 改动；Apply 仅给 ui.Theme.FontSize 乘一次系统 TextScale。组件自行将未缩放字阶乘一次 TextScale，避免颜色变化影响测量缓存。
- 展开列表按附件采用 540 DIP 硬上限；上游 window_session_controller.cpp:2825 的该夹取已注释，本库差异明确保留在 layout.md。默认 5/14 取 Windows 来源；宿主可覆盖比例。
- 第三栏扩窗由 `WindowHost` 执行宿主窗口操作，库实现上游请求次序、比例保存和实际扩宽量回收。宿主没有提供窗口能力、拒绝请求或窗口固定时，资料退化在主槽显示。主槽已实现缓存图像滑动和遮罩；我定的：不复制 slide_shadow 素材，边缘改用 defaultBoxShadow 的5 DIP/0.25参数生成渐变，不能声称阴影逐像素相同；顶栏任意位图蒙版路径尚未实现。
- 分隔线键盘步长借用已登记的会话行水平留白 10 DIP；Home/End、焦点/悬停强调色是本库可访问性选择，不声称 Telegram 有相同键盘操作。画廊外壳复用已有 token，不为画廊新增任意尺寸；没有使用图标或第三方素材。
- 我定的：圆底、图标和旋转弧共用浮点局部圆心；局部对称取整可能令外框比名义尺寸多 1 物理像素（本次比例下 ≤1 DIP），避免独立吸附造成半像素偏心。非对称图标保留原画布，徽章保留字体行框居中。三档实测、容差及未验收边界见 [centering.md](centering.md)。

## Dialogs

- 我定的：使用 Lucide 1.58.0 及本库原创实心文件夹替代图形。布局仍使用类型图标各自画布、静音8、置顶/认证20和竖栏36；实际图形与笔画按 [icon-glyph](paint/icon-glyph.md) 和 [逐项盘点](icon-glyph.md) 校准，不再把布局画布当图形边界。★、@、♥、▥仍是系统字形；替代轮廓不声称逐像素相同，许可证见根目录THIRD_PARTY_NOTICES.md。
- 我定的：无头像时默认 initials 使用 peer4 的两个 palette 色组成竖向渐变；字号采用20/600，来自现有20 DIP图标槽的用途映射，宿主应传预制 Bitmap 或 AvatarIcon 以控制默认头像。本库不复制头像字体、图片、专用素材。
- 我定的：系统文字放大时保留标题位置，增大行高与预览偏移以免文字被裁切；这是 mygo 的可访问性适配，非已验证的 Telegram 字体排版。
- 打字、录音波纹、上传箭头和选择贴纸眼睛的周期、相位与缓动已对照 send_action_animations.cpp，详见 dialogs.md 和 theme-behavior.md；mygo 的 MeasureText 未公开字体 ascent，活动图形竖向中心取预览行高中心，属于我定的排版映射。
- 窄窗主布局和收栏宽度过渡属于宿主／layout 组件。dialogs 只接收 Compact 状态并提供66 DIP宽度计算，不替宿主决定单双栏切换。
- 菜单条目、聊天置顶顺序与文件夹顺序由宿主提供。OnReorder 交付意图，组件不更改宿主数据。mygo 的原生右键菜单和滚动惯性不保证与 Qt 平台实现一致。
- 故事环、群通话频谱、订阅／TTL／community角标、推广／验证码专用行及搜索范围已实现。故事图片、头像解码、验证码和推广业务数据由宿主准备；订阅星、验证码与回复/编辑图形使用Lucide，community下拉箭头及TTL表盘用基本几何绘制。TTL局部模糊复用实际头像内容并在头像/主题变化时失效；mygo滤波核不声称与Qt逐像素相同。
- 我定的：FolderPages 使用活的宿主内容和两个互斥裁剪区域实现上游视差，不复制Qt页面快照或slide_shadow素材；不绘制该素材的边界阴影。宿主按ID保存列表和滚动状态；动画期间不接收内容点击。快速改选从上一目标重新过渡，始终保持裁剪互斥。

聊天路的「我定的」与附件决定：

- 头像已按上游改为组末/底部可见行跟随，名字仍在组首。虚拟历史的组顶坐标由宿主提供；缺省只以当前行顶为约束，不能推导未挂载消息的几何。
- 图标使用 Lucide 1.58.0。顶栏保留24 DIP布局画布，搜索实际16.5、通话16、更多4×16；时间旁保留各自收据画布并校准内部图形，见 [icon-glyph](paint/icon-glyph.md)。Telegram图形像素和轮廓不复制，来源与ISC/MIT许可见根目录THIRD_PARTY_NOTICES.md。
- 尾巴为独立二次曲线，填色6×10、下阴影2 DIP，已对固定版本3倍PNG掩模量覆盖边界差：最大0.847059 DIP。参考版本没有此尾巴的SVG或绘制路径；未复制PNG，仍不声称像素颜色逐点相同。
- 消息最大宽、边距、字体、四角、时间避让和服务/导航几何均来自主题。系统字体与 Telegram 的 Qt 字体选择可能有不同度量，按自然行高排版，不用猜测固定行高。
- 时间等底部信息仍由宿主格式化。文字发送已保留真实输入框图层并按上游280/160/110ms轨迹混合到消息；我定的：mygo没有Qt文档的逐行切片/逐字墨迹混合接口，本库对整块快照做尺寸插值和交叉淡化，复杂多行重排不等同上游 MorphLayout；捕获沿用该帧的光标绘制，尚未单独去除光标。旋转编辑器图层身份会重建组件私有临时状态，草稿/回复持久状态及焦点句柄由宿主保留。
- 本分支画廊的完整聊天组合背景使用已登记的窗口底色；宿主负责墙纸，库不分发 Telegram 墙纸或建立网络资源加载。
- a2-impl：我定的，不绘字的尾部占位和段落字体种子为0.0001 DIP，以绕开 mygo 对象独占行仍保留默认字体 descent 的行为；可见文字全部使用主题字阶。边界测试量得额外误差约0.000023 DIP。字体和抗锯齿仍由 mygo 平台决定，不能声称与 Qt 字形一致。
- a2-impl：回应顺序、缩写计数、签名/日期/编辑优先级由宿主准备；组件提供独立几何入口。没有用户截图，未作官方叠图；规格末尾「待补」列明资源及验证缺口。只含回应的输入不生成空消息壳，画廊在壳外单独展示列表。
- a2-impl：我定的，CommonMark末尾列表/表格/代码围栏没有本规格对应项，按末尾引用的已有K高独立占位处理；普通段落保留真实塑形同行。此选择与资源/截图缺口同列在画法规格末尾，未伪造新的Telegram数值。

## content 绘制和 API 的选择

- 我定的：使用 Lucide 1.58.0 的 file/play/pause/download/x/copy；原始路径来自24×24坐标系，但显示时按 [icon-glyph](paint/icon-glyph.md) 在原布局画布内匹配真实图形和独立线宽。没有复制Telegram图标，THIRD_PARTY_NOTICES.md保留ISC和Feather MIT许可。
- 我定的：Markdown 输入为 CommonMark 加 `>^` 折叠引用、`||…||` 剧透；Telegram 的服务端 entity 模型由宿主转换。正文不加气泡，不生成网络图片，不打开 URL；所有标签及跳转、复制动作由宿主提供。
- 剧透已按上游文字/图片两套描述符生成周期粒子图集。保留的差异：我定的固定PCG种子使程序化素材可复现；圆角精灵用4×4覆盖采样，Qt抗锯齿可能不同；逐rune隐藏占位的kerning仍可能不同。ReduceMotion静止，隐藏原文不进入复制和辅助技术树。图片底板仍为主题遮盖色，未复制Telegram资源或模糊预览图。
- 普通带气泡图片通过InBubble/Caption在组件内完成宽度测量和adjustHeightForLessCrop规则；宿主只传正文数据和可用宽，无须计算增高。照片使用3/4可见比例及一像素容差；GIF/视频使用4/3增高上限，原尺寸和裁切计算不依赖消息壳布局。
- 静态封面和传输目标来自宿主，下载/解码/播放/资源生命周期仍归宿主。传输圆环已实现350ms非线性追赶和3000ms旋转；媒体就绪后控件按现有API消失，未新增独立完成淡出状态。
- mw-gaps2：相册逐格失败文字与重试是宿主扩展，文字采用已有MessageTime与底部4 DIP inset；公开 OnTileSize 的完整格子DIP尺寸通知借鉴上游按geometry选择缓存，不拥有资源请求。Media.StatusText/Progress迁入单一模型，不保留Options同名别名。控件进入/退出仍即时显隐，加载过程沿用350 ms追赶和3000 ms旋转。见paint/album-state.md。
- 我定的：当前 content 画廊使用编造的色带和几何贴纸，不含 Telegram 素材。mygo 的原生窗口 capture 在内存重绘最后一帧（ui/window.go:662-676），本次实测在滚动后的组合页与桌面GPU画面有纵向位置差，不能作GPU逐像素证据；另以Windows CopyFromScreen保留明暗真实桌面截图，确认发送正文、回应和时间标记完整位于编辑器上方。与同版本Telegram原生截图的并排验收仍未完成。
- reaction-size：我定的宿主资源分类为默认无专用透明画布的静态位图、显式完整center frame、自定义帧；分别按32/2、32、round(18×1.12)绘制，来源与合同见 [reaction-size](paint/reaction-size.md)。无图时系统Unicode完整行框等比缩入18槽；不分发Telegram emoji sprite，不能声称墨迹逐像素相同。自定义帧的右侧圆角掩模和单色着色由宿主准备，库保持预制像素。
- 菜单、每层子菜单、tooltip 已迁至独立 mygo flyout，viewer 已迁至覆盖宿主显示器的独立全屏窗口；宿主内容区裁剪差异已移除。Box/Toast 保留内容区行为。查看器入口直接显示/关闭，不把控件200/600 ms显隐当窗口开关动画。
- 按附件要求，查看器信息放顶部；Telegram 普通图片路径的信息实际位于底部（media_view_overlay_widget.cpp:1884-1913,9163）。我定的：顶部沿用 mediaviewTextLeft/TextTop/HeaderTop 的 14/26/47 DIP，关闭与缩放操作使用 mediaviewIconSize 的 46×54 DIP 槽。
- 我定的：内置Lucide图形按 [icon-glyph](paint/icon-glyph.md) 校准，菜单布局仍左15、上5、文字左54；宿主SVG按明确限框处理。轮廓允许不同，不复制其素材。查看器直接ui.Icon调用是并行gaps2文件边界，见icon-glyph盘点末节。
- 我定的：普通 tooltip 的重新计时移动阈值采用 defaultTooltip.skip 的10 DIP，Telegram 读取 QApplication::startDragDistance()（tooltip.cpp:72），mygo 未提供该阈值。按本次附件要求增加淡入，复用 Motion.Fade 的200 ms linear（basic.style:101），而固定版本普通 Tooltip::popup 直接show，无该入场动画。定位由 mygo 的工作区翻转/滑入算法完成，最边缘处不保证与 Qt 的10 DIP skip完全相同。
- mw-gaps2：TooltipFocus键盘立即触发沿用mygo v0.7.4 ui/tooltip.go:36–43,145–167，是明确需求的可访问性扩展。正常离开补200 ms淡出和同owner反转，仍复用既有Motion.Fade，不把它称为官方普通Tooltip::popup的行为；Esc不移动焦点，Tab不向旧anchor回收。见paint/tooltip.md。
- 我定的：查看器选中的 ID 被宿主删除时关闭；页边不循环。媒体解码、下载、失败重试均由宿主实现，组件只展示准备好的 loading/failed/ready 状态与操作意图。
- mw-gaps2：用户明确要求图片双击适应/原始大小与打开后500 ms点击保护；固定官方版本只在视频双击时切全屏（media_view_overlay_widget.cpp:8158–8182），图片释放关闭路径:8658–8684无500 ms常量。按需求增加，500 ms是固定输入阈值而非可编辑动效，适用空白及关闭按钮；Esc、主动关闭和生命周期关闭不延迟。见paint/viewer.md。
- mw-gaps2：EditContextMenu 按原缺口建议采用标准 ui.Menu 对象路径，以持久 Handle 解析正文并追加 EditItems；固定mygo v0.7.4的引擎负责执行 Copy/Select All 和 CopyData。该菜单是系统原生标准菜单，外观/动效遵循平台，不等同现有 overlay 自绘菜单；正文不能再同时挂另一套菜单。直接按 Handle dispatch 仍需mygo公开API，本库不通过私有结构或合成按键绕过。见paint/edit-menu.md。
- mygo 在 macOS 上绘制连续圆角，在 Windows/Linux 上绘制圆弧（ui/paint.go:53-56）；本次视觉验收针对 Windows。系统字体与 Qt 的字体度量可能不同；项高采用实际字体行高加样式内边距，不把13 DIP字号当行高。
- 我定的：菜单 flyout 使用 mygo 的原生系统阴影以避免窗边裁切；Windows 11 的 DWM 阴影、外框细线和外窗圆角由系统决定，mygo 不提供与 defaultBoxShadow 的5 DIP/1 DIP/0.25逐项对应的控制。内容仍读取主题8 DIP圆角与原版揭示参数；不声称系统阴影和外窗边角满足逐像素对照。macOS/Linux 的窗口行为本次未作原生验收。
- 本次原生窗口验收机器仅连接一块1920×1080、96 DPI显示器，工作区1920×1032。Windows查看器按物理Monitor矩形定位，避免已核实的混合DPI坐标投影错误；多显示器、负坐标屏和异DPI屏之间的实机切换未验收，不把单屏结果扩称为多屏已通过。
- 性能验收边界：百次原生菜单开关只量到首帧后HWND可见；没有ETW/DWM实际呈现、掉帧率、GPU显存或72小时回收验证。三轮帧日志各保留一个211～225 ms的gallery启动首帧（主要在present阶段），不能声称零长帧。百次后工作集/私有提交/句柄分别增加15302656 bytes/16187392 bytes/49，增长来源未分类，不声称长期无增长。具体数据见overlay.md。

## 输入区（compose）

- 我定的：普通布局画布24、语音26保持；回复/编辑35槽内图形分别22×21和17×17，附件/保存/取消按各自PNG的实际图形范围校准，见 [icon-glyph](paint/icon-glyph.md)。图形仍来自Lucide，订阅星为填充star，不分发原版素材。语音为Lottie语义替代，保留既有26画布，不把不存在的静态PNG线宽冒称量测结果。
- DockedPicker默认向编辑区右上方停靠，Escape、外部按下及离开300ms关闭；宿主可覆盖Place、KeepOpen或使用独立Picker。资源帧、远端搜索与窗口生命周期仍由宿主提供；组件不加载Telegram表情、不生成替代资源。
- 新增 composeButton 的120ms线性时间轴、picker 的200ms时间轴和进出圆形轨迹已有 C++ 调用点依据；rippleShow 改为源码的 easeOutQuint。上述条目不适用旧的“缓动一律未对照”说明。面板按最终布局裁切内容，按 defaultPanelAnimation 的尺寸时段和渐变遮罩显现；不缩放文字。startOpacity 在该版本的 PanelAnimation 中只被赋值，computeState 实际从0按 opacityDuration 计算透明度，本库采用调用行为而非未使用的声明。
- mygo v0.7.4 的 Disabled 固定衰减0.5（ui/paint.go:263-267），本库保留原语的焦点、输入和无障碍禁用语义，不再额外叠乘透明度。默认衰减与 send_button.cpp:27 的 kForbiddenOpacity=0.5 相同；底层没有关闭此衰减的 API，禁用透明度不独立提供主题 token。
- 我定的：画廊示例使用Twemoji v17.0.3的四张未修改SVG（CC-BY-4.0），组件支持宿主图片和系统Unicode字形。分类栏保留宿主分类，额外“全部”时钟按钮用于回到顶部；未提供图标的自定义分类按自然文字宽加两侧iconSkip排版。普通分类图标槽30、内区域28、两端留白3，均有源码依据；不分发Telegram表情或分类素材。

## 设置页画法规格（set-spec）

本节保留规格阶段的差异登记；set-impl已按六份规格实现settings包，API与验收记录见 [settings.md](../settings.md)。这些例外不代表已经通过Telegram逐像素叠图。对应 [设置分段](paint/settings-section.md)、[行按钮](paint/settings-button.md)、[开关](paint/settings-toggle.md)、[单选多选](paint/radio-check.md)、[选择框](paint/choose-box.md)、[数字输入](paint/numeric-input.md)。事实来源沿用固定版本，T=tdesktop/Telegram/SourceFiles，L=lib_ui/ui，I=lib_ui/icons。

### SET-MUTED-VALUE

任务要求右侧值为灰色；官方defaultSettingsRightLabel实际用windowActiveTextFg（浅#168acd），不是灰色（L/widgets/widgets.style:1505-1508）。我定的：模型页显式使用windowSubTextFg（浅#999999，暗#708499）的muted变体，右对齐、宽度和截断顺序沿用官方。不改动官方默认值的登记。

### SET-VALUE-OVERFLOW

CreateRightLabel按“容器宽−左右padding−标题自然宽−23”计算右值允许宽，未定义本库通用窄窗合同（T/settings/settings_common.cpp:504-511）。我定的：允许宽≤0时隐藏右值，标题按自身可用宽省略；防止负宽进入本库排版。这是确定的排版规则，不把右值固定半行宽。

### SET-ROW-ARROW

基础SettingsButton没有右箭头，官方Business/Premium是在特定促销行外挂图标（L/widgets/buttons.cpp:977-1031；T/settings/sections/settings_business.cpp:261-274）。我定的：普通设置行可选24×24 Lucide chevron，距右22、垂直居中；与右值间隔6，色menuIconFg/Over，RTL翻向。24复用现有header.iconSize，22来自settingsButton.padding.right，6来自defaultMenu.itemRightSkip（L/widgets/widgets.style:970）。整行仍只有一个命中目标；箭头、右值组合及空槽处理见settings-button。不声称官方普通设置行有同款。

### SET-GROUP-HEADERS

LanguageBox已有recent/official两组加8高divider、组上下10，没有组标题（T/boxes/language_box.cpp:1224-1304）；ChooseLanguageBox也没有带标题分组封装（T/ui/boxes/choose_language_box.cpp:282-345）。Telegram其他界面有小标题和搜索组条，不能写成全局无分组。我定的：模型选择器按稳定组ID组织，命名组头复用SubsectionTitle的14 semibold与22/7/10/9边距，跟随列表滚动、不吸顶、不参与键盘选择；过滤后空组连标题/留白隐藏，多组共用checked ID和导航序列。视觉数值均取L/layers/layers.style:148-155，组合为本库扩展。

### SET-NUMBER-ROW

Telegram确实有NumberInput：代理端口55宽、55高、limit65535（T/boxes/connection_box.cpp:1666-1696）。自动下载/缓存上限用离散MediaSlider，不是自由键入数值（T/boxes/auto_download_box.cpp:136-175；T/settings/sections/settings_local_storage.cpp:1955-1993）。这些调用未提供“任意上下文整数直接编辑的通用设置行”。我定的：模型页用设置行显示当前值，点击标准Box内全宽55高下划线字段，外边距22，底部保存/取消；单位、范围、错误信息及提交由宿主提供。允许0表示自动时保留字面量0，明确区别于官方NumberInput编辑时清0、超限恢复原串的合同（L/widgets/fields/number_input.cpp:29-51）。不加入自定加减按钮、滑块档位或step。

### SET-DIVIDER-SHADOW

官方分隔带阴影用I/box_divider_top.png、box_divider_bottom.png两条1×5资源，整带高8，依次绘顶/底，范围会重叠（L/widgets/widgets.style:675-681；L/widgets/box_content_divider.cpp:29-56）。素材限制下不复制位图，也不把它当5 DIP实黑线。我定的替代：保留5 DIP边缘范围，用control.shadowForeground（shadowFg，浅#00000018/暗#04080e56）在顶边从原alpha线性降至0、底边反向，先顶后底source-over。范围来自资源元数据，色来自palette；梯度替代本身不是原资源还原，不能声称逐像素一致。

### SET-BOX-SHADOW

官方Box实际使用boxRoundShadow→roundShadowRadius8px的八片掩模、四边extend10（L/layers/layers.style:71；L/widgets/widgets.style:918-928），并非defaultBoxShadow。我定的素材替代：保留10 DIP阴影外扩的规格登记，实际复用overlay.Box现有menu.shadowBlur=5、shadowY=1、shadowOpacity=.25程序化阴影，颜色windowShadowFg；mygo的blur扩展不等同八片资源的固定10 DIP覆盖。来源L/widgets/widgets.style:913-916；只是借另一官方表面的参数，不能宣称Box阴影像素等价。新增FullBody固定正文不另建外壳。

### SET-ICONS

不复制Telegram设置图标、锁、搜索与勾位图。Lucide勾保留官方(4,7)的14×10布局槽，内部图形12×10；搜索保留16×16槽，内部图形14×15；普通行图标槽仍24，箭头图形7×12。独立描边与校准公式见 [icon-glyph](paint/icon-glyph.md)。原版勾为静态资源，a>0完整绘制，没有可借的C++线头参数（L/widgets/checkbox.cpp:255-257；widgets.style:839）；不把替代轮廓称为像素相同。许可见THIRD_PARTY_NOTICES，未借CrychicUI组件实现。

### SET-DISABLED

Checkbox壳明确以0.5画文字和check并停止动画（L/widgets/checkbox.cpp:695-713,879-889）；SettingsButton没有统一0.5，只关闭paintOver和交互，灰字需显式disabled样式（L/widgets/buttons.cpp:980,1042-1055；T/settings/settings.style:78-88）。实现保留mygo原语Disabled的固定0.5，设置行因此存在透明度差异；画廊开关页已说明，且不再乘第二次0.5。禁用不触发点击或新涟漪。ReduceMotion立即终态是本库可访问性选择。

### SET-INPUT-PLATFORM

NumberInput的文字、光标、选择、IME最终交QLineEdit绘制，源码用contentsMargins−(2,1,2,1)补偿Qt内部余量（L/widgets/fields/masked_input_field.cpp:97-106,142-144,223-224）。mygo保留自身文本输入与IME，不搬该Qt补偿。我的映射：以textMargins名义内容矩形、实际字体行高/ascent定位行顶与基线；系统字体/Qt与mygo的字形度量和光标像素差须原生验收，尚不能承诺≤1 DIP。后续截图比较须把这个区域单列，不能以固定字段高代替文字对齐证据。

### SET-CHOOSER-COMPOSITION

LanguageBox的选择会请求切语言、更新checked，框保持打开，由OK关闭；另一ChooseLanguageBox单选点击即关闭（T/boxes/language_box.cpp:1466-1469,1513-1526；T/ui/boxes/choose_language_box.cpp:329-333）。我定的模型组合：没有翻译设置、chips、语言分享/移除菜单；保留搜索/双标题/左Radio，激活只交ID，宿主接受后关闭。默认保留LanguageBox右菜单空槽，去掉它必须显式选择宽文本变体。模型disabled项由宿主给能力，禁止激活并使用0.5原语衰减，不冒用语言“removed”状态。极窄父区不足320时允许外壳夹宽；搜索与正文实际字号放大时按真实行高增高，是本库适配而非官方固定尺寸事实。组头和未支持0的数字策略分别见上述条目。

规格阶段只交文档的限制不再代表本实现的测试状态。实现验收、改坏检查与性能口径见 [settings.md](../settings.md)；Telegram原生截图叠图仍须使用宿主提供的参考图，不能用本库软件渲染或静态来源核查替代。

### SET-INPUT-MAPPING

我定的mygo映射：数字输入的原生编辑行按实际字体行高放在textTop=28；55 DIP字段全框都能获取焦点，整框不增加加减按钮。选择框搜索的底部留白补偿mygo原生编辑器的垂直居中，使首行名义顶点保持6+7=13 DIP；不把补偿值作为新的Telegram样式常量。原生选择、IME及Qt字体度量差仍属于SET-INPUT-PLATFORM，未取得同版本客户端截图前不声称逐像素一致。

### SET-HOST-EXAMPLE

我定的包名、API命名、五个服务商和40个模型、三个角色及上下文范围均为宿主示例。选择器只交ID，宿主接受后关闭；精确数字对话框复用FullBody并在字段两侧各留22 DIP。思考档位示例点击依次切换快速/均衡/深入，不把它冒称Telegram模型业务。未增加配置存储、模型网关或媒体加载路径。

## 本地叠图验收工具（fidelity）

Telegram没有该开发工具的对应组件或样式常量。半透明、编码sRGB绝对差、页面局部DIP偏移、缩放与参考物理比例均为本库的验收操作，不是Telegram组件外观；默认1.25来自本轮用户给定的125%缩放。范围、步长与测量阈值明确登记在 [工具参数](paint/fidelity-coverage.md#工具参数我定的)，不能把它们写回主题当官方常量。图层只合成实际组件成像与本地参考，不代替组件自己的画法规格。

原生flyout是独立窗口，主窗口图层效果不能作用到它。菜单对照先捕获本库真实原生菜单，再用画廊的-menu-render载入页面作静态成像对照；原生交互页仍可直接打开实际菜单。这是验收表示限制，不是另画一套菜单，也不声称静态图能验证原生窗口交互。

## 聊天身份扩展

画法见 [chat-identity](paint/chat-identity.md)。我定的：宿主直接提供AvatarInitials、AvatarLabel、AvatarColor和SenderNameColor，库不推导姓名或用户调色索引；零ui.Color表示不覆盖，非零覆盖在选中时仍保留，宿主可按选中态传对应主题色。AvatarColor把两端同时设为同一色；未给时首字使用官方peer4渐变。已有未提供首字的空圆继续使用原底色。

AvatarLabel映射到Row头像预留列的RoleImage语义节点，语义边界为该行预留列；圆本身仍由DrawOver按组尾/视口底定位，不把语义列边界冒称动态圆框。此列没有额外点击动作；组中每行可提供发送者可访问名字。系统字体字形与Qt不保证相同，文字采用实际塑形行框居中。

新增messageAvatar字阶13/600，按头像主题直径与默认33 DIP基准的比例取整后乘TextScale；基准从主题默认token读取，组件不另写尺寸常量。独立字阶默认来源是empty_userpic.cpp:300–302和chat.style:332；原dialogs的avatar字阶20/600例外不变。当前完整快照为JSON v9，v1–v8拒绝。

## 通用控件与宿主扩展画法规格（ctl-spec）

本节均为“我定的”；ctl-spec登记的映射已实现，当前theme契约为JSON v9。官方来源与逐项数值见 [controls.md](controls.md) 及其15份画法规格，API与实现验收见 [实现说明](../controls.md)。版本仍为tdesktop 250fc73、lib_ui 1ff7be7；T/L路径沿用settings-section。原版图形/声音/渐隐掩模不分发；采用已有平台禁用/字体差异不等于官方叠图通过。

### CTL-COMPOSITION

我定的：[RoundButton](paint/round-button.md)和[LinkButton](paint/link-button.md)用mygo按钮原语承接焦点、Enter/Space和可访问名称，ReduceMotion直接终态。RoundButton的Light透明底变体可将底色alpha置0、保留官方Light hover/ripple；这是Ghost用途组合，不把CrychicUI的Outline/Soft命名视作官方视觉合同。通用Outline若需要独立描边，使用宿主显式stroke（默认取control.borderWidth1）和官方RoundButton的radius4/Light前景；该描边组合为本库扩展。字体、宽度和布局仍按round-button，不能留旧CrychicUI样式。数字后缀的150ms Circ滚动、等宽槽与最新延迟目标按该页来源规定；初始静态/ReduceMotion直接终态是本库映射。

### CTL-ICON

我定的：[IconButton](paint/icon-button.md)保留backButton40×40/ripple40和24布局画布，内置返回图形16×16、关闭12×12；名称必填，专用Info偏移仍按page-title。图形限框见 [icon-glyph](paint/icon-glyph.md)，不复制资源轮廓。浮点共圆心、mygo禁用0.5与centering/SET-DISABLED一致；未声明的duration不增加动画。

### CTL-INPUT

我定的：[InputField](paint/input-field.md)保留mygo文本编辑/IME/选择/光标，不搬QTextEdit的customFontMargins和非单行placeholderCustomFontSkip（L/widgets/fields/input_field.cpp:1904-1924,4167-4185）；名义textTop28、占位rise20/scale.9仍按来源。单行粘贴CR/LF转空格，多行Enter默认换行、提交必须显式指定；readOnly允许选择复制，disabled禁止编辑提交。宿主负责业务验证。mygo可见滚动条若无法关闭，应列原生差异，不把它称原版滚动条；文字/ascent和IME对齐须独立截图验收，尚未保证≤1 DIP。

### CTL-SELECT

我定的：[Dropdown](paint/dropdown.md)以已有设置行或Light按钮加原生overlay.PopupMenu组合，选择按稳定ID而非label/index；短菜单单选用overlay现有radio/checked语义，不冒充官方Menu::Toggle。保留所有项同宽22选择标记槽，设置行箭头按SET-ROW-ARROW，独立箭头画布24、间隔6。无值显示宿主占位，空集合不弹出；开在入口下方起始沿，工作区约束优先；激活报告ID、宿主接受后更新值。搜索长列表显式选ChooseBox，不按项目数自动切样式。

### CTL-SEGMENTS

我定的：[SettingsSlider](paint/settings-slider.md)在Σ标签自然宽超过可用宽时改为自然宽+两侧barSkip3的横向可滚动段，不采用上游注释承认的文字互压路径（T/ui/widgets/discrete_sliders.cpp:340-371）；段间3仍保留。可用宽等于总自然宽时仍沿源码等宽/分配规则，避免边界定义冲突。按ID保持selected；空项集合占高0、单项可显示；禁用项跳过、焦点与选中分离。键盘方向/Home/End移动焦点，Enter/Space提交。禁用/ReduceMotion沿本库原语。150ms选择线性，不复用数值滑块hover角色。

### CTL-TABS

我定的：[Tabs](paint/tabs.md)提供明确的下划线与Info岛状两种变体，键盘手动激活、disabled、空集合、ID保留和RTL镜像作为通用扩展；上游岛状源码直接用物理x的拖动/滚动路径，本库镜像逻辑起始方向，不声称已验证其RTL等价。下划线窄宽遵循CTL-SEGMENTS；岛状本来就是自然宽滚动。岛状outline的浅/暗RGBA参数必须主题化（原版在T/info/profile/tabs/info_profile_tabs_strip.cpp:34-42），不隐藏成组件常量。官方两端使用fade_horizontal掩模；我定的替代为宽16（复用tabPadding）的线性透明→岛底色渐变，距端剩余量/(16×.5)决定强度，右侧反向；不复制掩模，不声称该宽度/渐变曲线等价于原资源。阴影使用来源blur8/offsetY2/opacity.18的程序化mygo绘制，核函数与Qt差异单列。拖动阈值使用mygo平台手势阈值，若无法取得则复用choose.clearInset12作为显式替代，来源L/widgets/widgets.style:1119，不称Qt默认阈值。

### CTL-CATEGORIES

我定的：[Side categories](paint/side-categories.md)将官方设置行组成常驻侧栏；固定版本Main实际为分类入口列表+页面栈，没有同款常驻VerticalTabs（T/settings/sections/settings_main.cpp:364-449；T/info/settings/info_settings_widget.cpp:66-74）。行几何复用settings-button，selected配色为windowActiveTextFg+windowBgOver，不把settingsButtonActive这个名字等同选中。宽度由父布局给、默认最小260借columnMinimalWidthLeft；窄布局复用layout导航，宿主保存选中/草稿/滚动，不复制业务路由。

### CTL-TITLE

我定的：[Page title](paint/page-title.md)只让显式返回槽可点击，标题用heading语义；内嵌标题+说明+动作组合用官方14/600、24/17与6/9留白，但其换行/动作换行不是Info固定54高。系统大字号时扩高保留内容，而非裁20高文字；截图需单列字体差。模式显隐可选官方FadeWrap：150ms线性opacity，scale参数.7，kWideScale5快照使实际可见缩放为.76+.24×opacity（T/info/info.style:264-265；L/effects/fade_animation.cpp:15,44-71,143-164），居中不改变布局；初始状态直接终态，途中反向从当前opacity接续。不能简单把.7当可见起始尺寸。

### CTL-BADGE

我定的：[Badge](paint/badge.md)以官方10 semibold、padding4/1/4/1、radius4的NewBadge扩展成通用StatusBadge；Neutral/Pending用windowSubTextFg+windowBgOver，Active用windowFgActive+windowBgActive，Success/Warning/Error用现有semantic前景+windowBgOver，始终有状态文字。独立MaxWidth时省略、role=status；不内置连接探测或spinner。数字未读仍引用19高的已有dialogs合同，不能给全部标签套19或H/2。

### CTL-COLLAPSE

我定的：[Collapse](paint/collapse.md)将Qt外margin协议映射为mygo单一padding（计入完整H），只改变外部高度/裁切；150ms明确linear。宿主保存展开值与内部持久状态；收起开始时移出内部焦点，裁外/隐藏内容不可点击、Tab或读屏；保持身份，结束才隐藏。SlideWrap本身没有箭头，本页不新增猜测的90度旋转。

### CTL-APPROVAL

我定的：[审批卡](paint/approval-card.md)是Box正文分区加bot并列按钮的衍生组合，Telegram没有同款AI审批业务。宽≤364、radius8、padding24/14/24/8；动作36高、gap2、padding10、textTop8、圆角6，允许/拒绝用inlinePrimary/inlineDanger底。窄宽改上下排列，长标签必要时增高，状态文字替代官方轮廓glare。Awaiting/Submitting/Failed/Resolved/Expired由宿主提供；无默认允许、无自动重试/执行、不因点击回调即解决；详情用Collapse，文案与ID通用可本地化。

### CTL-FORWARD

我定的：[合并转发摘要](paint/forward-summary.md)借WebPage引用底板，标题最多2行、摘要独立最多3行、另有count；官方网页总5行/标题2的预算并不等于这份组合。使用historyPagePreview10/5/7/7、quote5圆角/3条/.12底/.9条；不采用可收藏颜色/背景emoji。Loading/Failed保留旧内容，Retry独立且仅宿主允许时出现；节点获取、循环/深度限制、浏览栈与打开窗口由宿主维护。组件不包含QQ协议。

### CTL-STOP

我定的：[停止生成](paint/stop-generation.md)复用官方SendButton::Stop的14×14/radius3（T/ui/controls/send_button.cpp:336-352；T/chat_helpers/chat_helpers.style:1538-1539），不是凭录音图标猜方块。AI生成状态Generating/Stopping/StopFailed/Stopped与runID是宿主扩展；保持44×46槽、草稿和焦点，不与Cancel混用，不因编辑区Disabled禁掉可用Stop。120ms线性切换按5倍扩边快照推导可见旧scale1−.8a/新scale.2+.8a；中断时接当前快照。ReduceMotion和禁用衰减沿用本库映射，无自动提示音。

### CTL-SOUND

我定的：[提示音](paint/notification-sound.md)只定义宿主准备资源句柄和OnPlay意图；无内置素材、文件/网络读取、解码、音频引擎或设备。静音资格/继承音量/同scope500ms合并由宿主负责；通知由OS负责声音时不重复本地播放。VolumePercent未设置与显式0分开，0确实静音；官方override0意味着回退全局（T/media/audio/media_audio_track.cpp:155-160），两者不能混同。按钮/选择/开关图/翻页无默认cue；宿主显式配置才报告一次，构建/paint不得重复。试听可越过会话通知静音，但保留本库全局显式禁音/音量0，不声称与官方直接playSound完全相同。未运行音频/设备验收。

### CTL-INPUT-OWNERSHIP

我定的 mygo 接入：原生编辑器组词期间的非文本键由组词过程独占，不继续提交或处理导航；单行粘贴仍经 mygo 原生 CR/LF 归一化，保留撤销与选区。关闭中的 Box 只保留视觉淡出，立即退出模态输入与焦点范围，让 Dropdown 恢复入口焦点；不让不可交互的淡出层继续截获键盘。

Dropdown 在宿主禁用入口或移除全部选项时关闭已有选择层，避免仍能从旧层提交；关闭不推断选择成功、不替宿主改 selected ID。

### CTL-COLLAPSE-ACCESSIBILITY

mygo v0.7.4 的 `IgnoreInput` 明确保留辅助技术可见性，辅助技术还可直接 press 子按钮（`ui/element.go:1046-1076`、`ui/access.go:326-352,549-577`）；公开 API 没有“仍绘制但从辅助树移除整个子树”的 inert 设置。`Collapse` 在完全收起且 MinimumHeight=0 后使用 Hidden，可隐藏整个辅助子树；正在裁切或保留最小预览时，IgnoreInput 仅保证鼠标/键盘不进入，不能保证读屏隐藏/辅助操作隔离。此项是规格 `paint/collapse.md#显隐交互和状态保留` 尚未满足的平台限制，不按通过计；不借用错误的图片/按钮角色伪装成隐藏。宿主含可执行操作的详情应使用默认零预览，并在自身状态中限制操作资格。

## 聊天区补缺状态反馈（mw-gaps2）

我定的：官方照片没有宿主逐格加载/失败文案胶囊。为避免窄格裁切、文字覆盖取消控制和亮图上的白字失读，含状态文字时把44 DIP控制与单行胶囊组合居中，间隔/外留白4、内边距8/2均沿用media.badge*；底板复用L/colors.palette:28的不透明imageBg，前景沿用:379的msgDateImgFg，选中仍用:353的整格蒙层。超长文案省略，完整文字放在格子的辅助描述中。普通msgDateImgBg(:380)仅有0x54 alpha，白图上约2.30:1，不能用于这项必要反馈。见[状态文字规格](paint/album-state.md#状态文字底板)；没有把这项扩展称为官方画法。

## ICON-GLYPH 素材适配

我定的：使用[图形规格](paint/icon-glyph.md#图形限框)的单一比例，先取较小轴比例，再将未修改源图（Lucide笔画2）的几何墨迹比例限制在±1%，给用户≤2%的总误差预留另一半栅格预算。输出线宽仍默认1.75、小标记1.25并保留原短边一半夹取；不能更换原图描边作为验收基线。通常另一轴留白；固定线宽与原图比例冲突时选择最近的允许比例，极端自定义目标可能超出目标框，画布与命中区不变。实际与Telegram最大轴误差逐项报告，不要求两轴都≤1 DIP。零图形/零画布不画，几何缓存每窗口每源最多8种尺寸。浮点中心Painter在长轴预留一设备像素，短轴按同一比例缩减；裁切框仍等比，不再两轴各减一像素。比例验收覆盖该真实采样路径。

我定的：宿主任意SVG没有公开路径边界或描边修改接口。导航以Lucide house的20/24宽、21/24高估计26×23目标框，整个画布等比缩放后直接按最终大小栅格化，不按house边界裁切其他宿主形状；不能保证任意素材的墨迹都在同一参考框内。线宽随宿主源图整体等比缩放，不能声称都等于1.75。宿主菜单/行图标沿用单一画布比例；32 DIP Toast/Lottie与准备好的位图不作为Lucide缩放处理。

我定的：代码块复制图标仍按现有13 DIP代码头用途，将15×17图形按13/24缩放；不是普通24 DIP菜单图标。停止方块、TTL、活动动画、progress弧及原有几何Check壳按独立规格保留。

并行文件约束：overlay/viewer.go直接ui.Icon路径未在本路抢改；Viewer关闭/翻页须在gaps2合并时对其专用资源范围接入校准，不能直接套普通菜单箭头。其他并行保留文件列在 [盘点](icon-glyph.md) 末节；媒体从共用CircleIcon获得校准。
