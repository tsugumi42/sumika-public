# 设置组件与宿主接入

`settings` 按六份 [净室画法规格](reference/paint/settings-section.md) 实现。实现阶段只读本仓库的规格、来源登记和 mygo/CrychicUI；没有读取 tdesktop/lib_ui 源码。所有绘制注释指向 `reference/paint/` 条目，素材与平台边界见 [例外](reference/exceptions.md#设置页画法规格set-spec)。

## 组件

| API | 布局、状态与交互 |
|---|---|
| `SectionTitle`, `Skip` | 独立标题留白；自然文字高度；6 DIP 空段可累加 |
| `Divider`, `DividerContent` | 全宽8 DIP分隔带；灰底说明自然换行；顶/底阴影独立关闭；富文本及链接由末尾内容回调构建 |
| `Button` | 有/无图标；主标题、蓝色/显式灰色右值、可选箭头；右值先截断；普通/Light/Attention；悬停、按下、禁用、键盘及650/200 ms涟漪 |
| `ToggleRow` | 34×20逻辑框、28×14轨道、16 DIP圆钮路径及2 DIP居中描边；开/关/悬停/禁用/宿主锁图；120 ms线性反向接续；首帧直接终态；switch无障碍语义 |
| `Radio`, `Check` | 42 DIP设置行；22 DIP槽；单选外环/内点、多选圆角框/完整勾；整行点击、键盘激活、禁用、Check错误色、多行标签；120 ms过渡和局部圆形涟漪 |
| `RadioMark`, `CheckMark` | 非交互标记，用于宿主自定义行；选中来自参数，不保存业务值 |
| `ChooseBox` | 共用 `overlay.Box` 外壳；固定搜索、可滚动命名分组、双标题单选行、空结果；ID回调、Up/Down/Page、Rows Home/End/Space、Enter、Tab、Esc及可配置遮罩关闭 |
| `NumberInput` | 55 DIP下划线字段；原生编辑/光标/选择；150 ms占位、焦点线和错误色；正整数纠正、空中间态、超限恢复整段及光标；显式允许0；Enter/Esc交宿主 |
| `LimitValue`, `LimitSlider` | 30 DIP只读值行与31 DIP离散滑块块；3 DIP轨道、15 DIP圆钮；拖动/键盘/Home/End/Page/禁用；宿主提供档数、值表和格式 |

默认几何是100%逻辑像素，等于DIP。系统TextScale只乘字阶一次；文字放大可撑高行和搜索区。RTL镜像行槽、开关方向、单选位置及尾部箭头。禁用设置行保留mygo的0.5衰减，区别于Telegram原版无统一衰减的设置按钮，见SET-DISABLED。

## 宿主状态

```go
var chooser settings.ChooseState
var modelID string
var number settings.NumberState

settings.Button(c, "模型", settings.ButtonOptions{
    Value: modelID, Arrow: true, OnActivate: chooser.Show,
})
settings.ChooseBox(c, &chooser, preparedItems, modelID, "选择模型", settings.ChooseOptions{
    SearchLabel: "搜索模型", SearchPlaceholder: "搜索模型或能力",
    ClearLabel: "清空搜索", EmptyLabel: "没有匹配的模型", CloseLabel: "取消",
    OnSelect: func(id string) {
        modelID = id
        chooser.Open = false // 宿主接受后关闭
    },
})
```

`ChoiceItem` 提供 `ID/Title/Subtitle/GroupID/GroupTitle/Disabled`。ID须在整个目录唯一、非空；组ID稳定，同组标题一致。组按首次出现排序，组内保持输入顺序，空组完全隐藏。无组头可留空 `GroupTitle`。过滤只匹配主/副标题，不匹配组标题；小写、去重音、标点分词后，每个查询词必须是某个关键词的前缀。`WideText` 显式取消原版右菜单空槽，`Wide` 使用364 DIP外壳，默认宽320。

`ChooseState.Show` 清空查询、高亮和滚动位置。显示期间继续每帧构建组件，关闭后也继续调用以完成200 ms退场。checked ID始终来自宿主；移动高亮不选值，`OnSelect` 不自动关框。搜索、空结果、关闭、清空等文字全部由宿主本地化。状态每个窗口独立，组件不读文件、访问网络或数据库。

数值示例先让宿主把已格式化整数写入 `NumberState.Text`。默认纠正行为匹配正的signed-int数字输入：去掉非Unicode数字；不可解析或0清空；超过正 `Limit` 恢复前值及光标；不夹成上限。`AllowZero` 是明确的模型扩展，保留字面量0。范围、单位、错误状态、保存和持久化由宿主负责。通用滑块仅接收 `[0,count-1]` 的有效index与至少两个档位；下载MiB档位不进入theme或通用控件。

## 参考与取舍

实现前参考CrychicUI `form/settings_form.go` 的宿主绑定、`choice/search_dropdown.go` 的搜索焦点/提交组织、`input/number_field.go` 的数值回调边界；只借API和行为组织，没有复制实现，也没有采用其substring过滤、音效、经验间距或加减按钮。mygo v0.7.4 的基础控件提供输入、焦点、可访问性和Tester。选择框沿用本仓库 `overlay.Box`，新增 `FullBody/BodyHeight` 固定正文合同，避免另建一种对话框外观。图形使用已登记的Lucide 1.58.0，见根目录THIRD_PARTY_NOTICES。

我定的：包名 `settings`；`Button/ToggleRow/Radio/Check/ChooseBox/NumberInput/LimitSlider` 命名；显式宿主状态；模型页业务标签、三个角色、五个编造服务商及40个模型；普通行箭头、命名组头、灰值、允许0及精确数值弹框沿用规格阶段已登记的扩展。没有添加模型调用、配置存储或新素材加载路径。

## 画廊

`gallery/settings.go` 独立注册九页：`settings/sections`、`buttons`、`toggles`、`radio`、`check`、`choose`、`numbers`、`slider`、`models`。每页提供相关状态；悬停/按住/释放由真实输入产生。`settings/choose` 使用200个编造项目；`settings/models` 组合三个角色模型、思考档位、后台模型、识图模型和三个上下文数值。点击模型行弹搜索分组框，点击数值行弹输入/保存/取消框。

启动示例：`go run ./cmd/gallery -page settings/models -focus -locale zh-Hans`；加 `-dark` 检查夜间颜色。`-reference` 可读取宿主截图供对照，库组件本身不读图。

数值登记：`internal/cmd/themegen/metrics.tsv` 的设置扩展130项与图形校准指标生成到 `reference/theme-metrics.md`；字阶与动效在 `reference/theme-behavior.md`。当前主题完整快照为JSON v9，共614个颜色、806个数值、34个字阶和74个动效；设置组件的颜色仍复用已登记palette。`go run ./internal/cmd/themegen -metrics-only` 可仅用仓库登记表重建数值访问器，不读取GPL源码。

## 验证口径

`settings` 的测试通过 `ui.NewTester` 实际绘制和输入，覆盖几何/颜色、长值截断、行点击与键盘、开关反向、选择框前缀与重音过滤/空结果/跨组/200项滚动/宿主ID、数字纠正/上限/0/光标/焦点中断、离散滑块及120/150/200/650 ms边界。明暗、RTL和100/125/150%像素比均有测试；字阶放大不重复乘TextScale。

92条新增断言逐一反转均变红（settings 88条、gallery 4条）；另用Go overlay注入五种独立实现故障：右值向左偏4 DIP、开关误用ease-out、前缀误改substring、删除数字上限、滑块步长改2，均被相应行为测试捕获。改坏文件与日志只在任务scratchpad，不进入组件包。全库CGO_ENABLED=0测试、Go vet和锁定mygo v0.7.4的生命周期检查均已运行通过。

原生回归发现模型示例的数字保存若忽略OnSubmit的文字参数，会在原生事件早于下一次绑定重建时读取旧值；示例已直接消费编辑器提交文字，保存0并回填已实际通过。测试使用编造的数据，不访问真实聊天、配置或模型目录。

画廊宿主的最大示例宽度为540 DIP，复用layout.listMaxWidth；这是我定的演示宽度，不作为设置组件固定宽度或Telegram设置页来源。

## 200项实测（2026-11-03）

原生Windows画廊，96 DPI、800×768 DIP客户区；九页×明暗共18个新进程完成启动和可见截图。开关、数字编辑、滑块、模型ID选择/回填、0保存、过滤空结果与Esc均实际执行。打开各预热5次、记录200次；过滤固定25 Hz，200项与单项结果交替，每套主题3×200次。计时到无障碍树提交对应值和结果，不是输入到屏幕光子的端到端延迟。

| 观测 | 亮色 | 暗色 |
|---|---|---|
| 打开，200次，P50/P95/P99/max ms | 2.494 / 5.585 / 50.786 / 54.058 | 2.644 / 3.857 / 4.851 / 11.028 |
| 过滤第一遍，P50/P95/P99/max ms | 5.091 / 15.412 / 18.121 / 18.796 | 6.942 / 17.295 / 18.530 / 18.623 |
| 过滤第二遍，P50/P95/P99/max ms | 6.387 / 17.897 / 33.307 / 106.563 | 7.102 / 17.202 / 18.499 / 18.797 |
| 过滤第三遍，P50/P95/P99/max ms | 6.783 / 21.236 / 35.894 / 214.676 | 6.872 / 16.901 / 18.365 / 18.716 |
| 过滤活动帧数；P95/P99/max ms | 2206；2.1 / 3.6 / 202.4 | 2432；1.3 / 1.7 / 11.3 |
| 过滤期间平均CPU（单核百分比） | 13.73% | 10.79% |
| 过滤前→后工作集MiB | 73.18 → 85.48 | 72.88 → 73.97 |
| 过滤前→后PrivateUsage MiB | 117.38 → 122.53 | 114.94 → 115.66 |

亮色活动区保留一帧202.4 ms（build200.3、layout0.5、present0.6，1624次分配/61.4 KB，无GC）；不能凭分位数合格声称“无明显卡顿”。冷启动另有223.3/258.8 ms首帧，不混入活动帧；亮色首帧present占206.1 ms。标准采用Nielsen/RAIL及项目《标准-性能与效率-2026-11-03》：反馈P95≤100/P99≤200 ms、活动帧P95≤10 ms、使用CPU≤单核25%、主程序PrivateUsage≤256 MiB；其中分位数和资源线为项目暂定预算。实际呈现错失率、120 Hz、输入到画面呈现、DXGI/GPU内存和长期稳定性未测，不能把树提交或Go堆替代它们。

软件Tester另测三遍各200次过滤：P95为14.052/5.166/5.608 ms，max175.209/6.976/8.300 ms；平均每次约83.8 KB、1688次分配，包含构建/布局/软件绘制，不是纯过滤算法成本。追加3000次CPU/执行跟踪诊断，最慢17.988 ms，无>100 ms；CPU采样中prepare含MeasureText占4.17%，软件光栅呈现占52.08%。该诊断不能抹掉前一轮长帧。

真实模型`auto/deepseek-v4.1-flash`经127.0.0.1:3425复核四张编造数据截图，12.194秒完成；未见主设置页文字重叠/右值撞箭头/数字不可读。模型指出弹框视口底部部分行被裁切；这符合本规格“滚动区”要求的视口裁切，保留连续滚动，键盘导航会将目标整行滚入。

截图与测量JSON/日志保存在任务scratchpad `set-impl`，组件不产生这些文件。原生exe为14,656,000字节；新增settings及其画廊Go源13文件共86,365字节（含测试，不含生成theme表）。没有用户提供的官方Telegram同界面截图，因此尚未做≤1 DIP截图叠图验收。

原生再用仅在scratchpad生效的Go build overlay注入CPU/执行跟踪，900次过滤P95=22.812、P99=84.128、max=1232.981 ms（诊断含采样开销，不与基线混算）。跟踪定位主线程在mygo v0.7.4的Windows UIA `uiaTree.update.func2`调用阻塞309.610 ms，在D3D11 `Renderer.present`调用阻塞207.521 ms；对应帧分别为319.6 ms（other317.6）和232.8 ms（present211.4）。初始化device.New=203.871 ms、首个present=688.052 ms另列。另有166.108 ms主线程Running区间无法用Go调度跟踪区分OS抢占和实际计算；初轮build200.3 ms没有同轮trace，仍不能直接归因。未关闭无障碍、屏蔽长帧或改性能门槛；因此“零应用长停顿”保留未通过/未归因状态。Go依赖及本机mygo源码未修改，诊断钩子不进入仓库。
