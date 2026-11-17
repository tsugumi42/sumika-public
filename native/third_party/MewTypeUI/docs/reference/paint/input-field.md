# 通用单行与自增高输入框画法规格

ctl-spec；基线、T/L、DIP、RTL和净室边界见 [settings-section](settings-section.md#坐标来源与边界)。外观复用 [numeric-input](numeric-input.md#字段矩形与绘制)，本页只补普通InputField的文本/测高行为，不复制数字过滤合同。参考官方“编辑器管塑形与光标、外围管浮动占位和底线、按内容高度夹取”的分工，来源 `L/widgets/fields/input_field.cpp:1881-1936,2835-3004,6299-6308`。

## 形状、数值和文字框

| 项 | Telegram 常量 / 数值 | 来源 / 复用 |
|---|---|---|
| 根高 / 宽 | defaultInputField.heightMin55、heightMax148、widthMin64 | `L/widgets/widgets.style:1058-1060`；input.*已登记 |
| 内容边距 / 正文 | (0,28,0,4)；boxTextStyle=14 regular | 同文件 `:1033,1055`；`L/basic.style:56-57` |
| 占位 | 14 semibold；scale .9、向上20；margin0 | `L/widgets/widgets.style:1036-1043` |
| 线 / 动效 | 普通1、active2、radius0、150ms linear | 同文件 `:1044-1053`；`L/widgets/fields/input_field.cpp:2900-2927,4213-4226` |

背景windowBg，正文windowFg，普通底线inputBorderFg、聚焦activeLineFg、错误activeLineFgError；占位windowSubTextFg→windowActiveTextFg→attentionButtonFg，浅/暗展开值见 [numeric-input](numeric-input.md#数字文本框真实设置调用)。不画四边轮廓、不加圆底或ripple。

局部W×H，编辑视口 `(0,28,W,H-32)`，底线 `(0,H-1,W,1)`。active线的起点、展开和错误颜色插值完全引用numeric-input；H增高时两条线跟随底边，不固定在y54。占位常态名义顶28，聚焦/有正文/有IME预编辑时上浮到8、比例.9；清空仍聚焦不回落。来源 `L/widgets/fields/input_field.cpp:2900-2964,4167-4185,4213-4226,6299-6308`。Qt对非单行的placeholderCustomFontSkip依平台ascent/leading计算，不能把它猜成某固定DIP；mygo映射和精度限制见 [CTL-INPUT](../exceptions.md#ctl-input)。

## 单行

Mode::SingleLine关闭软换行，自动高度计算直接退出；默认高55，长串随光标水平滚动，不省略编辑内容。普通字段不执行NumberInput的清0、去符号或端口上限规则。硬换行按单行编辑原语处理，不能保留隐藏第二行。官方替换文本里的CR/LF转空格，来源 `L/widgets/fields/input_field.cpp:1934-1936,2835-2841,5230-5236`；mygo粘贴路径需单独验收，见CTL-INPUT。

Enter交付提交意图，不自行保存；Escape报告取消并允许宿主作用域处理。Tab/Shift+Tab按焦点序列，不插tab；IME候选确认不能当成提交。来源 `L/widgets/fields/input_field.cpp:4501-4550`；IME执行沿用mygo原语，不翻译Qt事件代码。

## 多行自增高

设以当前内容宽真实塑形得到文档高度D（含硬换行和空末段），额外宿主边距为U/Dx：目标高 `clamp(ceil(D)+28+4+U+Dx,55,148)`。不可按字符数或“最多六行”估高。无额外边距时名义内容上限116；到148后内部滚动跟随光标，外框不继续增高。正文删除或宽度增大导致行数减少时立即缩小；测高不是150ms高度动画。来源 `L/widgets/fields/input_field.cpp:2835-2863,3552-3553,6299-6308`。

官方关闭横纵滚动条的可见绘制，仍保留编辑滚动；不在148高字段外加常驻Scrollbar。来源 `L/widgets/fields/input_field.cpp:1959-1967,6293`。本库若采用mygo自带滚动条，须记入CTL-INPUT并与官方分别验收。

多行Enter默认插入换行，提交键策略为宿主显式选项；不带入compose.Editor的发送动作、附件、emoji或36/224高合同。官方是否提交由SubmitSettings决定，来源 `L/widgets/fields/input_field.cpp:4469-4472,4487-4511,4535-4539`；通用表单的默认选择登记CTL-INPUT。

## 状态和所有权

空/非空、focus/blur、错误显示/清除、readOnly、disabled分别保留。readOnly仍能选择复制；disabled不编辑/不提交，采用mygo禁用语义。验证错误由宿主给，组件不因输入一字自动推断业务错误已解除。无障碍标签与占位分开，绑定宿主持久字符串；局部状态仅保留焦点句柄、选择/滚动和动画，不保存Element。隐藏再显示或调整字号不清草稿。以上通用表单映射见CTL-INPUT；输入绘制不能读文件/网络或启动防抖查询。

## 后续验收输入

空/长单行、粘贴换行、硬换行/软换行、空末段、55/148上下界、resize回缩；聚焦/失焦/错误0/75/150ms、预编辑占位、中文候选确认、Tab/Escape/Enter、只读复制/禁用；明暗、RTL、三档缩放。字体度量与光标单列原生截图≤1 DIP，不以字段高相同代替字形对齐。

## theme 要补的

无需新增已登记的input.heightMin/Max/widthMin、text*、placeholderScale/Rise、border/Active、typography.dialogBody/inputPlaceholder和motion.inputField；全部沿用numeric-input对应来源，已在theme。radius0和默认无额外边距是配置。宿主可覆MinHeight/MaxHeight，但不得把聊天224当通用默认。多行测高、只读和提交策略不是theme数值。
