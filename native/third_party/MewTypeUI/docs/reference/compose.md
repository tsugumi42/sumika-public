# 输入区 API、状态与来源

基线：tdesktop `250fc73`、lib_ui `1ff7be7`，版本见 README.md。源码只有样式和行为取证；Go 组件独立实现，媒体由宿主提供，图标来自 Lucide。

## API

所有函数第一个参数为 `*ui.Context`，Options 最后，返回根 `ui.Element`。同级多实例在调用前 `c.Key(id)`；附件、分类、网格项内部按 ID 起 key。草稿、PickerState 和资源属于宿主，每个窗口使用独立状态。

```go
compose.Editor(c, &draft, compose.EditorOptions{
    OnSubmit: func(value string) {
        // 请求已被宿主接受后再清草稿；组件不推断发送成功。
        draft = ""
    },
    OnAttach: choosePreparedAttachments,
    OnEmoji: func() { pickerOpen = !pickerOpen },
    OnVoice: startHostRecorder,
})

// 无需组合 Editor 时可直接使用 Field、ActionButton 和 ReferenceBar。
compose.Picker(c, &pickerState, catalog, compose.PickerOptions{
    Open: &pickerOpen,
    OnSelect: chooseHostMedia,
})
compose.Attachments(c, preparedAttachments, compose.AttachmentOptions{
    OnRemove: removeByID,
    OnMove: moveBeforeID,
})
```

以上回调名是宿主示例。组件不选择文件、不下载/解码图片、不录音、不上传、不维护消息发送结果。`ui.ImageSource` 可以是已准备好的位图或 SVG；动图的当前帧由宿主更新。ID 在同一分类内唯一；分类 ID 在同一标签内唯一，不能用显示名代替 ID。

`FieldOptions.AvailableHeight` 是扣除聊天顶栏和其他常驻条后的高度；0 使用 ExpandedMaxHeight。`ReserveHeight` 扣除额外回复/附件区域。Editor 自动扣除 Reference 的实际高度。系统 TextScale 仅乘一次，布局用实际换行测量，超过上限滚动；放大文字时，回复条、标签、搜索、分类和分组标题至少容纳完整字行。

`PickerState` 保存每个标签的搜索词、分类和滚动位置。分类按钮滚到对应标题；搜索匹配Label/Keywords，OnSearch交付宿主更新远端结果。`Category.HideHeader`表达无标题分组，Icon为宿主准备的分类图形。`PickerItem.Emoji`是选择后插入的Unicode序列，Image可提供准备好的Twemoji等资源；Emoji标签没有Image时使用系统文字绘制。`PickerPage.State`为PageReady/PageLoading/PageFailed/PageRestricted，内置提示随c.Locale()切换；非空Status覆盖该提示，用于宿主错误详情。`PickerOptions.AvailableWidth`限制可用宽度；0按窗口和主题宽度取值。`Open`非nil时每帧继续调用组件，关闭后完成退场。`DockedPicker(c, editor, &state, data, PickerDockOptions{Picker: PickerOptions{Open: &open}})`默认右上停靠并处理Escape、外部点击、离开延迟关闭；Place可覆盖布局，KeepOpen可关闭离开策略。

## 组件与状态

| 函数 | 状态与交互 | Telegram 依据 |
|---|---|---|
| Field | 空、占位、聚焦、多行换行、36 最小高、224 最高、剩余高度约束、超限滚动、禁用、只读选择复制、IME、粘贴交宿主、Enter/Shift+Enter、Ctrl/Cmd+Enter | chat_helpers.style:1307-1327；history_widget.cpp:9234-9253 |
| Editor | 空稿语音按钮、有文字/附件发送、编辑保存、输入权限、回复/编辑条、附件与表情动作；提交不清草稿；返回根元素可直接作为DockedPicker锚点 | history_widget.cpp:7640-7667；send_button.cpp:208-225 |
| ActionButton | 附件、表情、发送、语音、保存、取消；普通/悬停/按下/禁用/激活；键盘按钮行为；120ms 切换；650ms 涟漪与200ms消退 | chat_helpers.style:1333-1343,1409-1435,1523-1540；send_button.cpp:24,27,139-143；widgets.style:705-706 |
| ReferenceBar | 回复/编辑图标、长名字/摘要省略、缩略图、加载、目标缺失、打开目标、独立关闭按钮、禁用 | chat_helpers.style:1144-1164；history_widget.cpp:11546-11651 |
| Picker | 表情/贴纸/GIF 标签；最近/收藏/分类/包由数据表达；图片网格、GIF 比例、禁用项、搜索、空结果、加载、失败、权限限制、保留各标签状态、横向分类栏、窄容器、显隐、Escape、200ms 标签切换 | chat_helpers.style:506-611,795-833,879,932-944；tabbed_panel.cpp:71-73,369-394；tabbed_selector.cpp:148-149,1409-1411 |
| Attachments | 确认框预览：原比例单图、最多10项相册、文件/缩略图、准备中/失败、打开/编辑/删除、稳定ID拖动重排、禁用；Ungrouped关闭相册，AsFile切换文件展示，滚动由父容器提供 | send_files_box.cpp:438-470,1576-1643；ui/chat/attach/attach_*_preview.cpp |

## 数值和主题

完整尺寸在生成的 theme-metrics.md，颜色在 theme-colors.md；新增输入区/面板 token 在 `internal/cmd/themegen/metrics.tsv` 维护。字阶和动画在 theme-behavior.md。所有组件读取 theme.T(c)，没有组件私有的颜色、字号、尺寸或时长常量。

输入区总最小高为 `36 + 9×2 = 54`；按钮在底部对齐，44×46，左右各留2。字段左侧起点为 `2 + 44 = 46`。72 只是 historyComposeField 的继承声明；HistoryWidget 在 computeMaxFieldHeight 中实际改为 `min(224, available)`，并保留49高的历史区，不能把72当正式输入上限。

面板宽345、高278～640，常规目标高为可用高度的0.75，容器不足时受其可用高度限制；圆角8。表情目标槽37×35（desiredSize=37，两个verticalSizeSub=1），按实际宽度计算列数并分配余量，图片32；贴纸槽最小64，按列数等分，图片边界比槽少两倍roundRadiusSmall，即默认6。贴纸的11是组边距，不是图片内缩。GIF行高96、间隔3、最窄48，外边距取gifsPadding。标签43，底部分类36、图标槽30、分类内区域28、栏两端各3，搜索33；搜索胶囊圆角为实高的一半（tabbed_search.cpp:598-599）。显现参数取defaultPanelAnimation的0.5/0.3起始宽高和0.6/0.9/0.3时段比例，采用圆形缓动；标签移动采用源码的进/出圆形轨迹与配套透明度。

面板展开使用固定的最终内容布局，在右下角按目标尺寸裁切，并按源码的渐变遮罩显现，文字不跟随边框缩放。`Open` 的关闭动画结束后才隐藏布局；输入在显隐过程中交回宿主。`startOpacity` 声明在固定版本中没有参与 computeState 的透明度计算，因此本库从0渐入，详见 exceptions.md。

## 画廊与验证

`gallery/compose.go` 注册Field、Editor、Action buttons、Reply and edit bars、Emoji/stickers/GIFs、Attachment previews、IME preedit and caret七页。Editor使用默认DockedPicker；附件页展示相册与普通文件，IME页提供中文提交和长稿滚动入口。素材与数据均为画廊编造。

`compose/compose_test.go` 使用 ui.NewTester 检查实际布局、绘制与交互。验证和改坏检查结果在 CHANGELOG.md 对应批次记录；真实系统输入法的候选窗和录音设备属于宿主原生验收边界。

集成验收：13 个 Tester 测试和全库 `go test -count=1 ./...` 通过，81/81 条实际断言路径逐一反转均变红；Go vet 与 mygo v0.7.4 vet 通过。六页明暗、宽窄布局共 24 张软件截图完成。原生窗口通过控件树定位输入框，输入完整的 `Native compose proof` 后按 Enter，确认提交回显、宿主清稿与暗色主题提交，保存三张截图。像素扫描每次只取一张位图，消除逐像素复制整图的测试开销；动画仍按原时刻、原曲线、1 DIP 门槛检查，窄窗从未被视口裁切的第二个格子量位移。

## details-b 来源补充

发送确认预览依据 boxes/send_files_box.cpp:438-470,1576-1643：连续图片/视频组成最多10项的相册，普通文件切分成单项，块之间10 DIP。单图依据 ui/chat/attach/attach_abstract_single_media_preview.cpp:138-170：宽不超过308，保留小图实际尺寸但宽至少20，高不超过min(1.5×宽,245)，控制最小高26+5×2=36。图像使用contain，不把每份附件塞进308×64的横卡。普通文件依据 attach_abstract_single_file_preview.cpp:158-220,240-297：44图标或64缩略图、独立名字/状态位置、横排编辑/删除动作。动作胶囊依据 attach_controls.cpp:15-79 和 chat_helpers.style:692-710，媒体48×26、top/right=5；文件按钮32×32、top2/right1。

媒体动作的 roundedBg/roundedFg 在 lib_ui/ui/basic.style:138-139 分别别名 radialBg/radialFg，本库直接读取 control.radialBackground/Foreground。

回复/编辑栏依据 history_widget.cpp:11546-11651 与 chat_helpers.style:1144-1163：左7/top7绘制操作图标；名字起点53，缩略图32加10间隔；49高独立取消区域。本库使用 Lucide reply/pencil 替代受限制的素材，去掉旧竖条例外。

Picker默认停靠依据tabbed_panel.cpp:151-171,194-220,255-303：锚点位于编辑区右上沿，面板向上展开、右对齐，窗口边缘限制。默认布局使用绝对边缘定位，避免mygo AttachTo强加4 DIP popup边距；宿主Place可选择其他布局。离开延迟、Escape与外部按下关闭，离开计时可由KeepOpen禁用。

IME：mygo v0.7.4 ui/textarea.go:412-415 绘制预编辑下划线，:315-328 滚动跟随预编辑光标；internal/windows/surface.go:547-593 更新原生文本状态，将DIP光标转设备像素并调用 ImmSetCompositionWindow / ImmSetCandidateWindow(CFS_EXCLUDE)；:900-936 处理WM_IME_COMPOSITION的预编辑与提交。组件保留 TextAreaBase，避免覆盖其IME坐标或重复绘制下划线。真实系统候选窗由主会话用中文输入法验收。

## details-b 验收记录

普通与`CGO_ENABLED=0 go test -count=1 ./...`、Go vet、`go run github.com/egoist/mygo/cmd/mygo@v0.7.4 vet .`通过。64条新增/迁移断言逐条反转均失败；五个实现故障（订阅前景消失、TTL图形缓存失效遗漏、相册上限改为9、Picker错位4 DIP、回复图标归零）均被对应回归拦住。TTL像素回归只采圆形内的实心区域，避免把下方头像透过透明角的变化当成缓存更新。

最终CGO_ENABLED=0原生画廊：六页×明暗×380/800 DIP，共24个场景；实测中文提交与宿主清稿、回复目标、相册编辑/打开、默认Picker显隐与Escape关闭。预编辑和滚动光标用Tester覆盖，真实中文候选窗仍按任务分工交主会话验收。共58张PNG含前后状态与性能场景，素材均为编造数据。

按共享性能标准的固定25 Hz脚本，预热20次，三轮各200次替换草稿：控件树更新合并P50/P95/P99为1.114/1.640/2.249ms，最大11.969ms；这不是物理输入到屏幕呈现。活动615帧P50/P95为0.6/1.1ms，最大11ms，>100ms为0，每活动帧分配P95为23.6KiB；单核进程CPU三轮2.697%/5.208%/3.278%。采样工作集/私有内存最大77,389,824/120,381,440字节。24个混合页面启动到控件树就绪中位389.616ms、最大559.632ms，不冒充固定场景冷启动P95。保留含启动的639条帧记录，启动首帧最大211.6ms，与活动区间分开报告。

证据保存在任务指定的scratchpad/details-b：all-tests*.log、mygo-vet.log、mutation-*.json、production-mutations.json、native-*.json、measurement-summary.json、source-inventory.json及PNG。尚未测物理输入呈现、DXGI错失周期、GPU显存、120Hz专项、十分钟空闲和72小时稳定性；没有据此宣称完整产品通过。

真模型截图复核：DeepSeek两次仅耗尽输出长度，按任务约定切换agnes-ai/agnes-2.5-flash，HTTP200、5.705秒取得最终答复。其关于省略号、浮动媒体动作及滚动裁切的意见属于明确行为；编辑栏/独立列计数的重叠意见经截图复核未观察到。答复与复核说明在model-response.json、model-review.txt；模型意见不替代Tester或逐像素验收。

## details-b2：表情、分类栏与语言

先参考 CrychicUI `overlay/emoji_picker.go:12-18,71-84` 的字符与名称分离、宿主选择行为；Sumika ADR-150 的社区 Twemoji v17.0.3 固定版本与CC-BY-4.0图形许可。组件接收准备好的图片或Unicode Emoji；画廊嵌入该版本的少量真实SVG，不联网、不读文件。Emoji与可访问名称分开，选择后插入Unicode序列。

`chat_helpers/chat_helpers.style:806-809` 分类栏36高、单格30、边缘skip3、选中区域28；`stickers_list_footer.cpp:319-321,1148-1152` 的skip用于栏两端，不是每格间距；选中背景在28×28区域内。分类用图标、名称仅作可访问标签；自定义无图分类用有水平留白的文字按钮。

`tabbed_selector.cpp:148-187` 标签滑动200ms（chat_helpers.style:521），线性时间轴，进入easeOutCirc/离开easeInCirc，同时进入alpha=easeInCirc、离开alpha=1−easeOutCirc。两页交叠是上游行为，缺少alpha会造成不透明内容叠压。本库保留这套进出位置与透明度。

文字通过mygo i18n.NewLibraryCatalog命名空间MewTypeUI，按c.Locale()取值；英文/简体中文随宿主语言切换，Options明确字符串仍可覆盖。宿主分类名称和远端错误详情由宿主翻译。


details-b2验收：全库`CGO_ENABLED=0 go test -count=1 ./...`、Go vet、锁定v0.7.4的mygo vet通过；24条新增断言逐条反转变红，六个实现故障全部被对应回归捕获（空心文件夹、线性曲线、失去裁剪、Unicode退回空图片、去掉滑动alpha、分类槽减半）。Tester同时检查Unicode实际字形、中英状态切换/自定义提示优先级、分类槽宽和间距；不只检查非空图像。

原生PrintWindow截图涵盖明暗、中英、380/800 DIP及真实emoji/分类/标签/搜索空结果；另有原版对照、完整聊天文件夹筛选和100/125/200% Tester图。计量时保留28张PNG，任务目录127个文件共28,393,307字节，其中原生画廊19,977,216字节、捕获驱动5,649,408字节；四张Twemoji原始SVG总计4,410字节。二进制、故障覆盖文件、日志、截图均位于指定scratchpad/details-b2，没有进入仓库。

固定25Hz搜索剧本，中文浅色/暗色、英文窄窗及一次浅色复测，各3轮×200次，共2400次：控件树提交延迟各组P95为2.680/3.174/2.909/2.707ms；活动帧P95为1.6/1.7/1.7/1.6ms，合计2400帧、最大5.8ms、>100ms为0，每活动帧分配P95为54.5～66.0KiB。原生进程采样工作集/私有提交最大119,418,880/158,138,368字节。十个混合场景启动至可见控件树426.660～743.353ms，样本不足以宣称冷/热启动P95。

单核进程CPU：首轮中文浅色30.90%、暗色6.35%、英文窄窗6.29%；补测浅色三轮7.71/7.51/8.28%。保留首次异常，不把补测替代原值，也不据此宣称完整产品通过纯文本流式25%预算。应用帧与控件树延迟不等于物理输入到呈现，未量DXGI错失周期、GPU显存或长期空闲；原生环境为1920×1080/100%，125%由Tester证明，指定屏幕的原生验收仍缺环境。

真模型：auto/deepseek-v4.1-flash，HTTP200、9.826秒返回截图复核，确认真实emoji、分类无重叠和中文提示。模型的文件夹模糊意见与直接边缘像素不符；英文typing/Sending/Sent/Read为画廊宿主编造内容，不是picker默认字串。原始答复及逐项核实保存在model-outcomes.json、model-review.txt。

