# 聊天首字头像与单条发送人颜色

基线：tdesktop `250fc73`、lib_ui `1ff7be7`，版本见 [README](../README.md)。T=tdesktop/Telegram/SourceFiles，L=lib_ui/ui。本页只记录画法、公式、状态与来源，不摘录或翻译 GPL 实现。实现阶段仅依本页及本库/mygo API，不再打开 Telegram 源码。

## 任务卡

- 用户目标：头像无图、加载中或加载失败时仍能从首字辨认发送人；本地群聊/QQ群中逐消息表达发送人名字颜色。
- 信息架构：聊天区 → 消息行 → 发送者头像、组首名字、正文；画廊增加 `chat/identity`，集中展示四种头像资源状态、颜色覆盖、分组与视口跟随。
- 配置归属和作用域：宿主准备每条 `Message` 的首字、可访问全名、已解码位图及可选颜色；本帧立即生效，不增加全局设置、不读文件或发网络请求。资源状态由宿主归并为“可用位图 / 没有可用位图”，库不建立第二套加载状态机。
- 验收：33 DIP头像、14 DIP左沿、40 DIP步进；明暗、125%与大字；有图→无图→恢复、主题和颜色更新；组首名字只在已有规则要求时出现；首字沿用现有组尾与视口算法。几何误差≤1 DIP；系统字体墨迹差单列，不能把mygo字体与Qt声明为相同。

## 参考与净室边界

参考 T/ui/empty_userpic.cpp:291–344 的圆形、竖向两色渐变和中心文字，T/history/history_inner_widget.cpp:1064–1119 的组尾/视口夹取，T/history/view/history_view_message.cpp:2503–2525,2583–2588 的名字颜色与单行字阶。借鉴的是绘制职责、数学关系和状态条件，不借用类型名、代码结构或素材。

另读 CrychicUI（MIT）display/avatar.go:5–8、display/helpers.go:71–90：头像在组件内统一选择位图或首字，宿主只交数据。采用这一职责边界；尺寸、颜色、自动name hash和入场动画不采用，未复制其实现。首字文本由宿主准备，不在绘制函数里解析姓名或猜渠道身份。

## 圆与首字

|项目|画法 / 数值|来源与本库用途|
|---|---|---|
|头像直径 D|33 DIP；半径D/2；无描边，圆边抗锯齿|T/ui/chat/chat.style:23；T/ui/empty_userpic.cpp:304–314,336–344 → `message.avatarSize`|
|默认首字背景|从圆框顶到圆框底竖向线性渐变；peer4两端 #5caffa / #408acf|T/ui/empty_userpic.cpp:306–310；L/colors.palette:303,323 → 已有 `avatar.peer4UserpicBackground/2`|
|首字前景|historyPeerUserpicFg，浅/暗均由主题映射|T/ui/empty_userpic.cpp:326–332；L/colors.palette:316 → 已有 `avatar.userpicForeground`|
|首字字阶|基准D=33时13 DIP semibold；来源公式为整数部分 `D×13/33`，系统TextScale再乘一次|T/ui/empty_userpic.cpp:300–302；T/ui/chat/chat.style:332 → 新 `typography.messageAvatar`=13/600，按 `floor(D×messageAvatar.Size/33)` 计算；33取已登记基准直径，不额外硬编码绘制尺寸|
|首字位置|对实际塑形后的文字行框(w,h)，左上为 `(cx−w/2, cy−h/2)`；不按字形墨迹质心修正|T/ui/empty_userpic.cpp:329–332 的完整正方形中心对齐；mygo 映射见下文|
|已有位图|在同一33×33圆内cover裁切；替换首字而不叠在首字之上|T/data/data_peer.cpp:449–461；现有Row位图API沿用|

`messageAvatar` 是独立可编辑字阶，不能复用现有20 DIP `avatar`（后者用于dialogs且是已登记例外），也不能使修改名字字号同时改变头像字阶。与通用控件字阶和图形指标共同纳入JSON v9：614 colors、806 metrics、34 typography、74 motion；旧v1–v8（含两路不同的v7）均拒绝，不加兼容别名。

## 资源与颜色优先级

1. `Message.Avatar != nil`：圆底上绘宿主位图，不绘首字，使用同一圆裁切；保留 `AvatarLabel` 的可访问名字。透明位图像素仍露出已有圆底。
2. `Avatar == nil` 且 `AvatarInitials != ""`：库内绘制首字，缺图/加载中/失败完全共用此路径。无资源时选择EmptyUserpic的依据为 T/data/data_peer.cpp:450–460,485–486。
3. `AvatarColor` 可选，零值代表不覆盖；非零时两个渐变端点都取该色，得到宿主指定纯色。默认取上述主题端点。颜色覆盖是本库宿主接口，见 [例外](../exceptions.md#聊天身份扩展)。没有位图也没有首字时保留现有空圆的主题底色；此输入不表示资源加载失败。
4. `AvatarLabel` 是完整可访问名，与短首字字符串分离。非空时mygo映射为头像预留列上的 `RoleImage`+`Label`，空时保持装饰列，不增加无名image节点；不把首字文本另加为可访问子节点。预留列的语义矩形是本行头像列，不宣称等于绘制中动态夹取的33 DIP圆框。名字标签不做首字推断或自动翻译。

## 组尾与视口

沿用现有 `RowOptions.ReserveAvatar` 和 `AvatarGroupTop`。只在入站且预留头像列时绘制。令当前行矩形R、绘制可见区V、组顶G（没有宿主值时为行内容顶）：

- 有后继且行底严格小于V底时，不绘头像；组尾或跨越视口底的当前可见行才绘。T/history/history_inner_widget.cpp:1084–1089。
- `B0 = min(R.bottom − message.marginBottom, V.bottom − message.historyBottom − message.marginBottom)`，`B = max(B0,G+D)`。圆框 `(R.x+message.avatarLeft, B−D,D,D)`。T/history/history_inner_widget.cpp:1093–1106；T/ui/chat/chat.style:52–53,329（marginBottom=2、avatarLeft=14）。`historyBottom=8` 已有来源登记在theme-metrics。
- 预留列宽由现有 `avatarSkip + marginLeft − avatarLeft` 计算，头像与气泡分别定位；首字不参加气泡测量，不撑高消息。T/ui/chat/chat.style:23–24,52,329；气泡左沿行为见已有 [chat.md](../chat.md#分组与边界)。
- 缓存图层改变Painter原点时，宿主窗口坐标G经绑定句柄转换到绘制坐标。沿用现有Row实现，不在宿主重绘头像。

## 发送人名字

`Message.SenderNameColor ui.Color`：零值使用已有 `messageColors` 的方向/selected主题角色；非零只覆盖本条消息的名字前景。`Bubble`与`Row`都消费同一个Message字段，不用全局改主题。覆盖不改变字号、布局、正文/时间/引用颜色，也不改变 `SenderName==""` 或 `Group.Previous` 时不显示名字的现有条件。

依据：T/history/view/history_view_message.cpp:2507–2525,2583–2588 按当前消息取nameFg并绘名字；T/ui/chat/chat_style.h:806–816 在出站取该方向service前景、入站按发送者取色；T/ui/chat/chat_style.cpp:1507–1514 对selected及发送者调色取当前值。官方颜色索引/collectible计算不属于本接口；宿主显式颜色在selected时仍为该颜色，需要选中变色时由宿主传相应主题角色。未给覆盖时完全沿用原来的主题默认，见 [例外](../exceptions.md#聊天身份扩展)。

## mygo绘制映射与验收项

mygo v0.7.4 `Painter.RichText/MeasureText`（ui/richtext.go:235–247）可携带字体、字重、颜色；按完整文字行框居中并使用同一Span测量和绘制。只在首字、字体、字号、字重或TextScale变化时重算行框，颜色变化不破坏几何缓存。圆裁切与FillGradient沿用公开Painter API；无自行生成像素位图、无每帧资源解码。

适用检查：C01/C02尺寸有据；C07几何与字体行框；B12颜色只表达发送者；A03/C12逐消息覆盖及变体一致；状态完整（无图/加载/失败/恢复），比较可复现（明暗、125%）。按Tester量布局与像素、检查可访问名字；通过Go overlay分别破坏每条新断言并确认红灯；另做消费者可见实现故障（去首字、忽略宿主色、丢名字色），最后用原生画廊实际切换和滚动。性能遵循共享RAIL应用绘制P95≤10ms，报告构建/绘制、CPU/内存和显式产物大小；不把automation idle或软件渲染当物理呈现延迟。

画廊的四种资源状态、初始800×920 DIP客户区和分组滚动区 `4×header.height` 是我定的合成演示条件，不是新的组件尺寸或Telegram常量；窄窗使用380 DIP。组件自身仍只读已登记头像和消息token。

## 验收实测（2026-11-04）

共享重任务锁、Idle优先级：CGO_ENABLED=0全库10个测试包通过；Go vet、mygo v0.7.4 vet均退出0；CGO_ENABLED=1的chat/theme/gallery三个测试包退出0。首字覆盖明暗×100/125/150%六组，缓存/非缓存视口两组；名字色覆盖明暗×方向×选中×Bubble/Row十六组，并逐条更改相邻消息颜色检查像素隔离；独立字阶参考覆盖13/600、19/700与TextScale=1/1.5。完整主题快照含32字阶并按v7往返。

首字位置以常规Text的独立flex居中布局作参考，保持边界误差≤1 DIP，不把不对称字形的墨迹中心当作文字行框中心。独立诊断的H行框为9.55957×17.291016 DIP；100/125/150%下，两种画法相对圆框的纯白墨迹边界差均为0物理像素。100%换底色时两种画法的纯白集合均由6×9变为5×1像素，说明精确白色集合不能独立充当字体几何判据；没有为通过测试移动实际字形。

正文带时间占位时，RichText可访问文本含U+FFFC，按纯正文查Find会得到空矩形。颜色隔离改为比较名字区域之外的完整帧像素，同时验证当前名字确实改变、其他消息保持不变；不靠空采样或抗锯齿色的众数判定正文/时间颜色。诊断图仅含编造数据并留在本地scratchpad。

软件烟测直接调用已注册的chat/identity页面，执行资源按钮、选中/名字颜色开关，输出明暗各四种状态与380 DIP窄窗，共10张125%图像。完整App的原生窗口事件由原生启动器提供；不能把没有Window的App.View交给Tester。

软件性能条件：500×800 DIP、8条带首字与名字色的消息、20帧预热、每组3×200帧，共3600帧；每次翻转选中态，测量Frame+Image的完整软件绘制/读回。各遍P95如下，单位ms：

|主题|100%|125%|150%|
|---|---|---|---|
|浅色|3.0036 / 3.0048 / 3.0032|4.0075 / 6.5748 / 6.7351|10.5041 / 8.8836 / 6.8499|
|暗色|3.6294 / 3.5110 / 4.2504|6.5783 / 7.0566 / 5.1926|6.6003 / 6.5582 / 6.2097|

最高样本26.2375ms，150%浅色首遍P95=10.5041ms，原始超10ms样本保留。分配约1,613,094–1,613,170 / 2,514,340–2,514,518 / 3,612,171–3,612,319 bytes/frame，90.715–91.700 allocations/frame；其中Image完整RGBA读回分别为1,600,000 / 2,500,000 / 3,600,000 bytes。该数字覆盖整个Tester场景与读回，组件自身不生成位图；应用帧的10ms预算按原生build/layout/paint日志另外判断。

19条新增断言语句通过Go overlay逐条反转，19/19分别退出1且命中各自断言；三个实现故障（去首字、忽略宿主头像色、忽略单条名字色）分别退出1。恢复后CGO=0的chat/theme/gallery再次退出0；红检运行器本身退出0。没有修改已提交源文件来制造故障，临时overlay源在每项检查后删除。

原生e2e完整App在DPI120完成11个阶段：缺图、加载、失败、就绪、选中、还原默认名字色、暗色、三遍资源状态循环、滚动分组；阶段均退出0，生成9张不同阶段PrintWindow图像。读取控件树确认头像列的image角色及完整名字，滚动后检查可见分组的头像语义；已核对原生首字、位图替换、暗色/选中和滚动图像。

原生资源切换按预定25Hz、每遍200次、共600次；样本从预定到达时刻计入排队，终点为automation idle，单位ms：

|遍|P50|P95|最大|
|---|---:|---:|---:|
|1|1.5113|69.0283|239.0073|
|2|1.6868|157.3633|269.3940|
|3|1.6444|16.7979|140.2537|

该代理计时第二遍P95超过100ms，全部保留；物理输入到实际呈现没有测量，不能宣称端到端响应达标。原生635帧总耗时P95=1.1ms、最大772.9ms；除首帧634帧最大13.2ms，无>100ms帧。build+layout+paint的P50/P95/最大为0.5/0.6/11.6ms，应用工作P95满足10ms预算。首帧build/layout/paint/present=2.6/4.2/4.8/761.3ms，总772.9ms；日志只能定位到present等待，尚不能归因为GPU、驱动或合成器。

原生进程wall=34090.0354ms、CPU=6562.5ms，平均单核口径19.2505%；末次工作集99786752 bytes、私有提交132603904 bytes，均非峰值。范围为画廊进程，不含驱动/PowerShell测试进程；没有另起模型或业务辅助进程。e2e画廊二进制20825600 bytes，软件烟测二进制20028928 bytes；8张独立诊断、10张软件页面、9张原生图共27个PNG，合计1579243 bytes，均为编造数据且仅在指定本地scratchpad。组件运行不写文件。

适用验收：C01/C02尺寸来源、C07独立行框/几何、A03/C12逐消息隔离与变体、B12发送人颜色、四种资源状态、125%与窄窗均通过上述测试及图像核对。系统字体与Qt逐像素相同、物理输入呈现、DXGI错帧、GPU显存、十分钟空闲及长期资源稳定性均未验证；原生启动长帧、代理交互超100ms和软件读回超10ms样本保留，不宣称性能全项通过。

