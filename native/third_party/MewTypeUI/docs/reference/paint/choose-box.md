# 可搜索分组选择框画法规格

净室与坐标约定见 [settings-section](settings-section.md#坐标来源与边界)。`T/`=tdesktop/Telegram/SourceFiles/，`L/`=lib_ui/ui/，`I/`=lib_ui/icons/。固定版本以官方LanguageBox为主，另核对ChooseLanguageBox，不能混用两者的选择提交方式。参考三条做法：搜索固定在滚动视口上方；当前选中ID与键盘/鼠标高亮分开；过滤只隐藏行，单选值不随下标变化。来源 `T/boxes/language_box.cpp:680-725,785-820,1466-1538`。

## 外壳样式数值登记

| 常量 | 值 / 用途 | 来源 |
|---|---|---|
| boxWidth / boxWideWidth | 320 / 364 DIP；LanguageBox用320 | `L/layers/layers.style:117-118`；`T/boxes/language_box.cpp:1478,1496` |
| boxRadius / boxDuration | 8 DIP / 200ms | `L/layers/layers.style:37-38` |
| boxTitleFont / boxTitle.maxHeight | 16 semibold / 24 DIP | 同文件 `:73-79` |
| boxTitlePosition / boxTitleHeight | (24,13) / 48 DIP | 同文件 `:81-82` |
| boxTopMargin | 8 DIP，无标题时默认内容顶留白 | 同文件 `:89`；`L/layers/box_layer_widget.cpp:469-474` |
| boxTitleAdditionalSkip/font | 9 DIP / 13 regular | `L/layers/layers.style:83-84` |
| defaultBox.buttonPadding/buttonHeight | (6,10,10,10) / 34 DIP；按钮区总高54 | 同文件 `:124-128`；`L/layers/box_layer_widget.cpp:110-117` |
| defaultBoxButton | 14 semibold；自然文字宽+30；高34；文字top7 | `L/layers/layers.style:40-48` |
| buttonRadius | 4 DIP，不是control.radiusSmall=3 | `L/widgets/widgets.style:716`；`L/widgets/buttons.cpp:606-609` |
| defaultBox.margin | (0,10,0,10) DIP | `L/layers/layers.style:128` |
| boxMaxListHeight | 492 DIP，内容区申请上限，不含48标题和54按钮区 | 同文件 `:120`；`L/layers/box_layer_widget.cpp:465-466` |
| boxBg/titleFg/layerBg | 浅#ffffff/#404040/#0000007f；暗#17212b/#ebebeb/#0000007f | `L/colors.palette:129,135,139`；[颜色表](../theme-colors.md) |
| boxRoundShadow | roundShadowRadius8px，四边extend=10 DIP，资源着windowShadowFg | `L/layers/layers.style:71`；`L/widgets/widgets.style:918-928` |

## 外壳矩形与绘制层次

父内容区 `Pw×Ph`，申请宽W=320（显式宽版364），内容申请高C。带标题时标题区48，无标题时内容顶8（显式noContentMargin才为0）；按钮区54。壳全高 `Hfull=titleArea+C+54`，实际高 `H=min(Hfull,Ph-10-10)`；首次或父容器改变时居中 `(floor((Pw-W)/2),floor((Ph-H)/2))`，最小top skip可进一步约束。来源 `L/layers/box_layer_widget.cpp:178-208,455-483`。内容尺寸改变通常保持原top，只有将越过父底10或强制居中才重新定位，不能每次输入都重居中，见 `:420-450`。

LanguageBox记录曾申请过的最大“搜索区+列表内容”高度，C=`min(历史最大值,492)`；过滤缩短列表不让整个框来回跳高。父窗口缩小时仍受可用高限制。来源 `T/boxes/language_box.cpp:1489-1501`。

最下层是原窗口内容；其上全父区覆盖layerBg；再画壳外阴影；最后画boxBg填充8圆角壳、标题/固定搜索、裁切后的列表与底部按钮。圆角用高质量圆弧，无壳描边。来源 `L/layers/layer_widget.cpp:258-331`、`L/layers/box_layer_widget.cpp:132-152`。

官方Box阴影是八片资源，不是defaultBoxShadow的blur=5/offset=(0,1)/alpha=.25。四边扩展10，角片随壳位置布局；不分发素材，所以程序化替代和误差边界单列 [SET-BOX-SHADOW](../exceptions.md#set-box-shadow)。不得把现有overlay.Box用的菜单阴影当成官方Box阴影已经复现。

标题标签行框左上 `(24,13)`，可用宽 `W-48-顶部按钮总宽`，自然宽超出才省略，maxHeight24。附加标题位于主标题右侧再加9，基线以主标题ascent对齐。可选右上close按钮占48×48、右上角(0距右,0距顶)，不是LanguageBox默认必有；它的ripple40×40、相对按钮(4,4)。来源 `L/layers/box_layer_widget.cpp:169-175,307-325`、`L/layers/layers.style:91-103`。

底部按钮在y=`H-10-34`；第一个传入按钮最右，距右10；后续向左，间隔6；左按钮距左10。同一区域不会滚动，无默认分隔带。自然按钮宽=14 semibold实测文字宽+30，高34、文字top7；hover为lightButtonBgOver，文字lightButtonFgOver，按下用lightButtonBgRipple涟漪。来源 `L/layers/box_layer_widget.cpp:294-305`、`L/layers/layers.style:40-48,124-128`、`L/widgets/widgets.style:753-763`。

## 进出场动画

总200ms；框和随框缓存的阴影透明度线性0→1/1→0，框位置、尺寸和字号固定，没有默认缩放或下落。遮罩独立使用easeOutCirc：单位进度t时打开覆盖系数 `sqrt(1-(t-1)^2)`，关闭为 `1-sqrt(1-(t-1)^2)`，再乘layerBg自身127/255 alpha。来源 `L/layers/layer_widget.cpp:124-155,201-221,258-265,329-331`；时长 `L/layers/layers.style:37`。进出完成才切换活内容/释放缓存，见 `L/layers/layer_widget.cpp:159-167`。ReduceMotion立即终态是本库可访问性映射。

## 搜索输入区

LanguageBox使用不含chips的MultiSelect，不是聊天列表的35高圆角胶囊。这里以“模型选择框没有LanguageBox顶部翻译设置”作为本库组合选择；其余原版搜索布局照下表。来源 `T/boxes/language_box.cpp:1471-1486`。

| 样式项 | 值 | 来源 |
|---|---|---|
| defaultMultiSelect.padding/bg | (8,6,8,6)，boxSearchBg | `L/widgets/widgets.style:1127-1129` |
| fieldIconSkip / fieldMinWidth / fieldCancelSkip | 36 / 42 / 40 DIP | 同文件 `:1143-1149` |
| 搜索field | minHeight32，textMargins=(2,7,2,0)，13 regular，无边框，无圆角外壳 | 同文件 `:1088-1104` |
| placeholder | placeholderFg/Active，scale=0，不浮起；有文字时淡出 | 同文件 `:1092-1097`；`L/widgets/fields/input_field.cpp:2967-2989` |
| 搜索图形 | boxFieldSearchIcon资源16×16，偏移(10,9) | `L/widgets/widgets.style:1107`；`I/box_search.png`元数据 |
| 清空按钮 | 44×44，crossPosition=(4,4)，cross.size36/skip12/stroke1.5/minScale.3；显隐150ms | `L/widgets/widgets.style:1109-1125` |
| 搜索占位动效 | field.duration继承150ms，linear | 同文件 `:1044,1088`；`L/widgets/fields/input_field.cpp:2967-2989`、`L/effects/animations.h:67` |

空chips且宽足够时搜索区高 `6+32+6=44`，field矩形 `(44,6,W-52,32)`，有query清空按钮目标显示时field宽再减40；文字行框原点 `(46,13)`。图标在 `(18,15,16,16)`。清空按钮框 `(W-44,0,44,44)`。来源 `T/ui/widgets/multi_select.cpp:770-776,829-840,843-859,1013-1044`。这一布局的最小几何前提是内容内宽≥42+40=82，否则原源码不会承诺单行搜索；超窄外壳扩展见例外。

清空按钮只在trim后query非空时显现，150ms线性a控制opacity；X绕中心旋转90×(1−a)度。其36画布内X包围边长 `12×(.3+.7a)`，两条对角笔画正交厚1.5、平头，高质量抗锯齿，中心位于按钮(22,22)。来源 `L/widgets/buttons.cpp:741-774,796-804`、`L/effects/cross_animation.cpp:123-133,160-180`。点击清空并恢复field焦点，来源 `T/ui/widgets/multi_select.cpp:699-702,706-716`。清空不提交值。

## 过滤与空结果

关键词由主/副标题合并后做trim、小写、去重音并分词；查询同样归一化。分词边界包括空白、@、连字符、+、括号、中括号、大括号、尖括号、逗号、点、冒号、!、下划线、分号、单双引号、NUL。每个查询词都须是某个关键词的前缀；不是任意substring匹配，也不是必须整句前缀。来源 `T/boxes/language_box.cpp:332-341,680-725`、`L/text/text_entity.cpp:1221-1223,1499-1517`。过滤本地同步完成，无源码规定的debounce、网络spinner或模糊搜索。

每次query变化清空鼠标/键盘高亮、pressed与弹出菜单，保持checked ID；隐藏项涟漪释放。过滤后的行按原顺序连续重排，不按新下标认身份。两组都空时显示空结果区；单组空则整组连其上下10隐藏，两组之间的divider也隐藏。来源 `T/boxes/language_box.cpp:680-725,1224-1289`。

空区高 `9×membersAbout.style.font.height`，注意是14字体的实际font height，不是9×22固定行距；提示用membersAbout（14 regular，lineHeight22，minWidth240，membersAboutLimitFg浅#919191/暗#677a8b），标签在区内水平、垂直居中，无按钮和图标。来源 `T/boxes/language_box.cpp:1257-1279`、`T/boxes/boxes.style:128-132`、`L/layers/layers.style:51-52`、`L/colors.palette:151`。提示文本由宿主本地化。

## 列表行

| 样式项 | 值 | 来源 |
|---|---|---|
| passportRowPadding | (22,8,25,8) DIP；该列表的绘制公式实际两次使用left22，不能把right25机械代入 | `T/passport/passport.style:108`；`T/boxes/language_box.cpp:959-981` |
| passportRowSkip / passportRowIconSkip | 2 / 10 DIP | `T/passport/passport.style:109-110` |
| title / description | 13 semibold / 13 regular | `T/boxes/language_box.cpp:332-339` |
| Radio | langsRadio，槽22，背景boxBg | `T/boxes/language_box.cpp:328-331`；`T/boxes/boxes.style:175-177` |
| topBarSearch.width | 40 DIP，原版右侧语言菜单槽 | `T/info/info.style:928-931`；`T/boxes/language_box.cpp:456-463` |

对宽W和某行rowY：单行估计高 `H0=8+Fs+2+Fn+8`，Fs/Fn为13 semibold/regular实际行高；菜单槽top0=`floor((H0-40)/2)`，右侧保留 `R=boxScroll.width-boxScroll.deltax+top0+40=49+top0`。文字可用宽 `A=W-22-22-22-R-10=W-76-R`；主/副标题分别测高Hs/Hn，行高 `H=8+Hs+2+Hn+8`。文字左66，主标题顶rowY+8，副标题顶rowY+8+Hs+2；Radio槽 `(22,rowY+floor((H-22)/2),22,22)`。来源 `T/boxes/language_box.cpp:456-463,933-966,973-1020`。主/副标题按各自自然排版增高，没有普通设置按钮的单行20槽；不强写一个经验行高。

原版即便某行没有菜单也按同一A预留右侧槽。模型选择器不提供语言分享/移除菜单，保留该空槽可保持原版文字换行；去掉右槽以增加可读宽度属于显式变体，登记 [SET-CHOOSER-COMPOSITION](../exceptions.md#set-chooser-composition)。

常态boxBg；鼠标hover/键盘高亮/pressed行整宽windowBgOver，无选中蓝底；主标题windowFg，副标题普通windowSubTextFg、高亮windowSubTextFgOver。已选值只由左侧Radio内点表达，不能用高亮替代checked。整行鼠标涟漪用defaultRippleAnimation（650 Quint/200 linear），不叠Radio圆涟漪。来源 `T/boxes/language_box.cpp:487-500,984-1020`，Radio画法见 [radio-check](radio-check.md)。原版removed语言行另用stickersRowDisabledOpacity，不是一般模型disabled项；模型禁用项的可访问性适配登记在例外，不借用“removed”语义。

## 滚动区

内容区原点在标题区下方；搜索固定在其顶部44，滚动视口 `(0,44,W,内容区实高-44)`，不把标题或底部按钮放进列表滚动。BoxContent的上下边缘为1 DIP shadowFg横条，上边沿紧贴搜索底，下边沿紧贴按钮区顶；顶部因innerTopSkip=44而默认常显，底部在未滚到底或有innerBottomSkip时显示。来源 `L/layers/box_content.cpp:340-355,430-443`；颜色由 `L/widgets/shadow.cpp:129-140` 的PlainShadow决定。视口裁切所有列表内容，Radio和涟漪都不能跨到标题或按钮。

边缘阴影随滚动显隐使用FadeShadow：200ms线性透明度，固定矩形、不缩放；内容resize时可instant更新。时长来自fadeWrapDuration，复用现有motion.fade。来源 `L/wrap/fade_wrap.cpp:15-21,44-77,127-133`、`L/effects/fade_animation.cpp:143-164`、`L/basic.style:101`；resize调用 `L/layers/box_content.cpp:440-442`。

boxScroll继承defaultSolidScroll：总槽14、左右内缩5、可见thumb宽4，顶/底各6，圆角2，最小thumb高20，不滚动时隐藏；hiding=0，不按普通ScrollArea的1000ms自动消失。来源 `L/layers/layers.style:85`、`L/widgets/widgets.style:807-836`。设视口高V、内容高D、滚动偏移s，可用轨道高Q=V-12；thumb高 `max(20,floor(Q×V/D))`，thumb顶 `6+floor((Q-thumb高)×s/(D-V))`。若D≤V或Q<20则隐藏。来源 `L/widgets/scroll_area.cpp:107-120,147-185`。hover色150ms线性混合；颜色用scrollbar.thumbBackground/Hover、trackBackground/Hover，见 `:249-270,294-319`。不套聊天背景专用scrollbar颜色。

## 键盘、焦点、选择提交

搜索先取得焦点；Up/Down/PageUp/PageDown交给列表。跨两组的导航把可见可选行看作一条序列，组头与divider不占index；正向无高亮时先到第一项，末端不循环；从第一项Up会退为无高亮。Page步长=`max(floor(整个BoxContent高度/H0),1)`，原版并非精确可见行数。导航请求整行滚入可见区，已经完全可见则不移动。来源 `T/boxes/language_box.cpp:1315-1372,1676-1700`、`L/widgets/scroll_area.cpp:23-57`。

搜索Enter：有高亮则激活该项；无高亮先选择第一结果再激活；零结果不提交。列表直接获得焦点时另支持Home/End、Space/Enter（忽略自动重复）；Tab进入Rows优先落在checked项。来源 `T/boxes/language_box.cpp:350-394,1404-1413,1503-1505`。移动高亮不切checked，提交后由宿主确认新ID再更新Radio；原LanguageBox调用switchToLanguage，更新current ID，框保持打开，底部OK关闭，见 `:1466-1469,1513-1526`。

ChooseLanguageBox的另一种单选模式点击后回调并立即关闭，底部Cancel；它没有左侧Radio这个同款行，不能把那种提交方式说成LanguageBox的默认。来源 `T/ui/boxes/choose_language_box.cpp:329-333,380`。模型选择器采用“激活报告ID、宿主接受后关闭”的我定的组合，详见例外；选择事件本身不调用模型网络接口。

Escape：field将事件ignore并发cancelled，MultiSelect回调清空query；被忽略的事件继续到LanguageBox时关闭，因此官方调用链不能描述为“第一次只清query，第二次才关闭”。来源 `L/widgets/fields/input_field.cpp:4537-4539`、`T/ui/widgets/multi_select.cpp:695-698,930-933`、`T/boxes/language_box.cpp:1509-1511,1676-1680`。实现按一次Escape关闭的用户可见结果处理，清query不另外触发选择提交；外点是否关闭由Box宿主开关控制，来源 `L/layers/box_layer_widget.cpp:245-250`。

## 分组与组标题

官方LanguageBox已有recent/official两组：每组上下各10留白，组间8 DIP BoxContentDivider，无标题；只剩一组时无divider，空组整体隐藏，变化是instant，不能套150ms滑动。来源 `T/boxes/language_box.cpp:1224-1255,1283-1304`。

未在该版本的LanguageBox或ChooseLanguageBox发现现成“可搜索单选列表+命名组头”的封装；其他界面有SubsectionTitle和搜索结果组条，不能声称Telegram全局无分组。模型按供应商/用途分组的**我定的**画法：组非空才画普通SubsectionTitle（14 semibold、左22/上7/右10/下9），其下按上述Rows绘制；首组保留顶部10，其余组之前插8高divider，各组底10。组头随列表滚动，不吸顶，不可选、不画Radio、不响应点击；过滤不匹配组头，仅按项匹配，空组连标题/留白隐藏；所有组共享一个checked ID和一条可见项键盘序列。复用来源 `L/layers/layers.style:148-155`；完整选择登记 [SET-GROUP-HEADERS](../exceptions.md#set-group-headers)。没有新增自定视觉数值。

## 现有overlay.Box接入差异

`overlay/box.go:94-96`给整个正文额外加boxPadding=(24,14,24,8)、固定22行高，并只提供一个内部scroll。这不能直接实现LanguageBox的全宽行与固定搜索：实现路必须让外壳可提供不额外padding的固定区/滚动区，并让Rows用13字阶自然行高；或者在组件内拥有这两区。这里只列实现前提，不修改代码，不建立第二种Box外观。现有菜单阴影与按钮3圆角差异也不能当作本规格已验收。

## 后续验收输入

短/长列表、长主/副标题、单/多组及空组；query大小写/重音/标点、多词前缀/非前缀、清空、全空；已选项被过滤后恢复；Up/Down跨组/首尾、Enter无高亮/零结果、Tab与Escape；点击按下拖出/释放；明暗、RTL、100/125/150%。量44搜索、48标题、54按钮区、320宽、492申请高与父高夹取；输入过滤不缩框、列表滚动时搜索不动。文档任务未运行这些UI场景或截图叠图。

## theme 要补的

已有：dialog.width/wideWidth/radius/titleLeft/titleTop/titleHeight/titleGap/buttonHeight/buttonGap/buttonPadding/buttonTop/buttonRight/buttonBottom/margin/maxBodyHeight；dialogTitle=16/600、dialogBody=14/400、dialogButton=14/600、body/contentTitle=13/400和13/600；dialog/dialogScrim=200 linear/Circ；全部palette与ripple、scrollbar动效已有。以下为尚缺的数值，不应拿同值但别处用途的token硬凑。

| 建议键 | Telegram常量、值、来源 |
|---|---|
| dialog.titleMaxHeight / topMargin / buttonTextTop / buttonRadius | boxTitle.maxHeight=24、boxTopMargin=8、defaultBoxButton.textTop=7；`L/layers/layers.style:76,89,47`；buttonRadius=4，`L/widgets/widgets.style:716` |
| dialog.shadowExtend | roundShadowRadius8px.extend各10；`L/widgets/widgets.style:927`，实际资源阴影用途 |
| choose.searchPaddingLeft/Top/Right/Bottom | defaultMultiSelect.padding=8/6/8/6；`L/widgets/widgets.style:1129` |
| choose.searchFieldMinHeight / searchFieldMinWidth / searchIconSkip / searchCancelSkip | field.heightMin32、fieldMinWidth42、fieldIconSkip36、fieldCancelSkip40；同文件 `:1102,1144-1149` |
| choose.searchTextLeft/Top/Right/Bottom | field.textMargins=2/7/2/0；同文件 `:1090` |
| choose.searchPlaceholderLeft/Right | placeholderMargins左右2；同文件 `:1095` |
| choose.searchIconLeft/Top/Size | boxFieldSearchIcon偏移10/9，16画布；同文件 `:1107`，`I/box_search.png`元数据 |
| choose.clearWidth/Height / clearCanvas / clearInset / clearStroke / clearMinScale / clearPositionX/Y | 44/44、36、12、1.5、.3、4/4；同文件 `:1110-1121` |
| motion.chooseSearch / chooseClear | field.duration=150ms / cancel.duration=150ms，linear；同文件 `:1044,1123`；`L/widgets/buttons.cpp:748-752`、`L/effects/animations.h:67` |
| choose.groupPaddingTop/Bottom | defaultBox.margin.top=10/10；`T/boxes/language_box.cpp:1233-1243` |
| choose.rowPaddingLeft/Top/Right/Bottom | passportRowPadding=22/8/25/8；`T/passport/passport.style:108`，本页保留实际调用公式 |
| choose.rowTextGap / rowIconGap | passportRowSkip=2 / passportRowIconSkip=10；同文件 `:109-110` |
| choose.emptyFontHeights / emptyMinWidth | 9倍实际font高 / 240；`T/boxes/language_box.cpp:1262`、`T/boxes/boxes.style:129` |
| choose.scrollTrackWidth / scrollInsetX / scrollInsetTop/Bottom / scrollHideDelay | defaultSolidScroll=14/5/6/6/0；`L/widgets/widgets.style:830-836` |

Radio补项见radio-check；divider和命名组头补项见settings-section。scrollbar.width=4、radius=2、minHeight=20已有；40菜单参考槽复用header.actionWidth。阴影程序化替代可复用menu.shadowBlur/shadowY/shadowOpacity，但这三个值不属于官方Box阴影。零border/scale和空chips是本组件明确样式配置，不新增常量token。
