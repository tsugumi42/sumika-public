# Inline 回应图像尺寸与资源合同

基线沿用 [版本登记](../README.md)：tdesktop 250fc73、lib_ui 1ff7be7。T=tdesktop/Telegram/SourceFiles，L=lib_ui/ui。仅记录结构、数值、公式和状态；实现阶段只看本页与本库/mygo，不再读上游 C++，不复制 Telegram 资源。

## 任务卡

用户目标：消息下方的回应图形与数字保持 Telegram 的紧凑比例。范围为 content.Reactions 的资源尺寸、槽内定位及原生画廊；宿主仍拥有资源、计数、顺序和选择状态。入口为聊天消息底部及 content/reaction-size 画廊，无新增配置编辑面或持久设置。验收 Windows DPI120、浅/暗、普通/自己/选中/禁用、零/多位/头像/换行；尺寸误差≤1 DIP。参考 Sumika 用户截图用于重现过大图形，不把它当官方正确目标。

## 来源与参考

参考官方回应的“布局槽、资源画布、实际图形分开”及普通资源与自定义资源分支，避免把大画布当可见图形大小。参考 CrychicUI chat/reactions.go:31-37 的 ButtonBase 和容器组合，但不采用它的下标身份、内部改计数或自定弹跳。没有借入实现。

| 量 | 值 / 公式 | 来源 | 本库用途 |
|---|---|---|---|
| 槽位与胶囊 | 槽18，padding左5/上2/右7/下2，H=22，R=11 | T/ui/chat/chat.style:673-676；T/history/view/reactions/history_view_reactions.cpp:328-372,491-497,701-711 | 现有 reaction.*，胶囊和命中区不被图形撑大 |
| 普通回应画布 | 32，槽内偏移(18−32)/2=−7；相对胶囊左上(−2,−5) | 同cpp:463-465,569-581；chat.style:675 | reaction.imageSize，仅适用于已经准备好透明边的中心画布 |
| 缺 centerIcon 的资源 | selectAnimation 帧缩至画布一半16，再在32透明画布中居中；最终相对胶囊(6,3,16,16) | T/data/data_message_reactions.cpp:733-744,820-848 | 普通无额外画布的宿主位图默认按 reaction.imageSize/2 显示；不在组件内重新分配透明画布 |
| 有 centerIcon 的资源 | 帧缩至32，内部透明边由该资源自身决定；可见包围盒不能仅凭样式计算 | 同cpp:733-744,820-848 | 显式 ReactionCenterFrame，宿主准备完整画布；不得自动裁透明边 |
| 自定义回应 | create默认 Normal；Normal取Ui::Emoji::GetSizeNormal；adjusted=round(18×1.12)=20；槽内偏移(18−20)/2=−1，相对胶囊(4,1,20,20) | T/history/view/reactions/history_view_reactions.cpp:249-254,845-878；T/data/stickers/data_custom_emoji.h:40-54；cpp:82-91,1017-1020；L/text/text_custom_emoji.cpp:17-20,45-46 | 显式 ReactionCustomEmoji；1.12是资源尺寸换算算法系数，基准取reaction.iconSize；宿主提供已经准备的圆角/单色着色帧 |
| 静态 Unicode emoji | Normal尺寸18×DPR；Large=24×DPR（100%）；按指定尺寸选择对应缓存实例，非旧式size index常量 | L/emoji_config.cpp:487-498,615-624,785-807；L/basic.style:59 | 无图的系统文字是素材例外；放入18槽并按实际字体行框等比缩放，不受消息正文/系统文字放大撑大 |
| 数字 | 普通semiboldFont=13/600，tag的12 regular不是普通回应计数字体；x=5+18+3=26；top=floor((22−Fs)/2)，baseline=top+As | T/history/view/reactions/history_view_reactions.cpp:469,603-616；L/basic.style:53-55；chat.style:684 | 现有ContentTitle字阶正确，按实际字体行高/ascent定位；不改字阶 |
| 普通胶囊宽 | 33+C；零计数28；头像45+(n−1)×13 | T/history/view/reactions/history_view_reactions.cpp:328-372；chat.style:683,689-695 | 保持已有宽度协商与末行时间占位 |

## 绘制合同

Image 表示不含 Telegram 专用透明画布的普通宿主位图，默认按16 DIP图框 contain；ImageKind=ReactionCenterFrame 表示完整32 DIP画布；ImageKind=ReactionCustomEmoji 表示20 DIP自定义帧。三种框的中心都与(14,11)的18槽中心重合。绘制不裁胶囊外的资源画布，胶囊点击区始终22高。资源内部的透明边、非正方形比例必须保留，不扫描像素猜类型，不按位图原始分辨率猜用途。位图保留颜色；自定义单色前景和与Telegram相同的右侧圆角掩模由宿主准备，组件不生成GPL素材。

无Image时使用Emoji的系统文字作为替代图形。按18 DIP显式字体塑形完整行框，以min(1,18/行宽,18/行高)等比缩放并居中，度量在文字、字体或资源尺寸改变时更新并缓存。这是我定的跨平台替代，不声称字体墨迹与Telegram18像素sprite相同。图形忽略TextScale，与Telegram按资源几何绘制一致；计数仍遵守原有TextScale合同。不能用c.MeasureText的富文本段落种子行高拟合：mygo的主题字号随TextScale增大，即使span显式18，其测量行高也会被主题种子撑大，导致图形反向缩小；使用显式ui.Font的ui.ShapeText规避该影响。

所有尺寸由既有theme reaction.iconSize/imageSize/padding* 推导；本次无新增独立主题值，不改变完整快照格式。16是32/2，20是round(18×1.12)，在此登记来源及算法；不借头像20或Picker32凑数。

## 比较与验收口径

同一资源、同一计数对改前/改后量胶囊宽高、位图图框及非透明包围盒、数字基线；普通位图目标16，自定义目标20，中心画布目标32但可见包围盒依宿主素材。用程序化不透明色块验证16/20/32，避免把资源透明边误当布局。

用户图229×87物理像素，125%；它是Sumika缺陷参考。轮廓叠图只把该图回应区域边缘描成彩色线并对齐本库回应胶囊原点，保留实际1.25比例，不把错误图形缩成正确目标。左右图明确标为缺陷参考/修复后。没有官方原生截图，tdesktop列是源码推导并使用本机字体度量，不冒充Qt运行实测；官方centerIcon的真实墨迹包围盒无法从样式确定。

## reaction-size 验收记录

普通不透明正方形资源、Count=1；本机字体计数 advance C=5.23047，行高 Fs=17.291016、ascent As=14.02832。坐标相对胶囊左上；图框中心是槽中心(14,11)，不是含数字胶囊的整体中心(19.115235,11)。公式：countX=5+18+3=26；W=countX+C+7；baseline=floor((22−Fs)/2)+As。Qt字体未运行，下表 tdesktop 列仅将本机字体度量代入源码公式，不称为官方运行截图实测。

| 项目（DIP） | 改前实测 | 修复后实测 | tdesktop源码公式/目标 | 修复后偏差 |
|---|---|---|---|---|
| 普通胶囊宽×高 | 38.23047×22 | 38.23047×22 | (33+C)×22 = 38.23047×22 | 0×0 |
| 普通位图墨迹宽×高 | 32×32 | 16×16 | 32/2 = 16×16 | 0×0 |
| 图框左上 | (−2,−5) | (6,3) | (5+(18−16)/2,2+(18−16)/2)=(6,3) | 0；125%中心栅格误差≤0.4 |
| 数字起点x / 基线y | 26 / 16.02832 | 26 / 16.02832 | 26 / floor((22−Fs)/2)+As | 0 / 0 |
| 自定义不透明帧墨迹 | 无资源分类 | 20×20 | round(18×1.12)=20 | 0×0 |
| 显式完整中心画布 | 无资源分类 | 32×32 | reactionInlineImage=32 | 0×0 |
| 完整中心画布，中央半幅墨迹 | 无资源分类 | 16×16 | 宿主中央半幅32/2 | 0×0 |

静态16/256像素源在100%/125%/150%下均得16 DIP墨迹；原路径overlay的六个子例均以32而非16失败，修复后通过。另以抛弃式运行程序检查静态、自定义、完整中心、透明半幅中心、透明半幅静态、非正方形静态6种资源×浅/暗×三种缩放，共36例；墨迹尺寸全部符合16/20/32/16/8/8×16，中心相对(14,11)误差≤0.4 DIP，低于任务≤1 DIP标准。程序、逐项日志和测量JSON保留在任务scratchpad，不进入组件。

轮廓叠图与左右图使用用户缺陷参考原始229×87像素，保留1.25比例。画廊使用既有Twemoji挥手素材，用户图是点赞；两者不能作字形逐像素差分，尺寸结论来自同资源改前/改后与上表源码公式。官方centerIcon真实墨迹仍由宿主素材决定。

原生验收通过：实际窗口 GetDpiForWindow=120，1018×760物理像素；浅/暗分别由Windows CopyFromScreen捕获可见桌面帧，同时保留PrintWindow副本。两个主题都以系统鼠标输入点击首个回应，胶囊背景由普通变为自己选中，并保留clicked截图；窗口关闭退出0。桌面原为96 DPI，拍摄时临时调为120，finally恢复原缩放并读回验证成功。轮廓图包含用户回应ROI的299个品红边缘像素，按胶囊原点对齐，不缩放原图；左右图明确写出Sumika缺陷参考和本库修复。

界面任务卡核验：尺寸与定位通过（上表、36例资源检查）；状态通过（普通、自己、出站、选中、禁用、气泡外、零计数、多位计数、头像，原生浅/暗）；点击通过（真实原生输入改变自己状态，宿主持有）；配置归属不适用（没有新设置）；官方同版本像素墨迹一致性未验证（没有官方截图/中心动画素材，使用源码公式验收几何）。动画机制未改，沿用既有涟漪和底部布局交互检查。

产物目录为任务scratchpad/reaction-size：native-light.png、native-dark.png、对应clicked与printwindow副本、contour-overlay.png、side-by-side.png；资源用例逐项记录kinds.log/kinds.json，几何和性能记录before-measurements.json/after-measurements.json。CGO=0全库测试、go vet、mygo v0.7.4 vet首次均退出0；最终源码版本另留final检查记录。录制主题生成的614颜色、732指标无内容变更；启用core.autocrlf的工作区首轮原位生成统一换行后，第二轮三项SHA256完全一致，结果在generation-hashes.json。

性能量测口径：CGO=0 ui.NewTester软件帧；浅/暗×100%/125%/150%，每组预热20次、3批各200次自己状态切换帧，记录P50/P95/最大值和runtime分配差额。按任务《标准-性能与效率-2026-11-03》:246的应用帧P95≤10ms暂定门槛判定，60Hz刷新周期另为16.67ms；来源为Google RAIL（https://web.dev/articles/rail）。不把测试器耗时当物理输入到显示延迟；Windows计时量化会产生0ms样本。原生另记录启动、截图、一次真实点击到退出前的CPU累计、working set和private bytes。未量DXGI错帧、GPU显存及物理输入呈现延迟，不据此声称这些指标通过。

最终源码检查：CGO_ENABLED=0 go test -count=1 ./...、go vet ./...、mygo v0.7.4 vet、原生release构建均退出0；新Unicode像素回归在旧路径的三个缩放档位均失败（exit1），显式字体版本全通过。三个Unicode图形×三种DPR×两档TextScale（1/2）的18例均保持相同图形墨迹并满足18槽上限，Unicode资源例外仍按本页登记。

最终软件帧量测（六批/每个缩放）：100% P95最大0.5211ms、125% 1.1364ms、150% 1.0093ms，均小于10ms应用帧P95暂定门槛；单帧最大分别6.321、15.2348、10.3319ms。125%改前P95最大1.0054ms，改后1.1364ms；单组重复间存在调度差异，不据此声称提速。每帧分配均值改前/后：100%约448/448 B，125%约10249/10244 B，150%约10240/10245 B；新增类型字段与Own共享结构体留边，普通位图路径未增加结构体尺寸。Unicode额外度量只在文字/字体/资源尺寸改变时塑形，逐帧复用缓存。

原生轻场景的启动、截图与一次点击合计：浅色2.510s/CPU656.25ms，working set74,366,976B/private119,160,832B；暗色2.716s/CPU562.5ms，working set74,510,336B/private118,632,448B。这是短采样，未冒充十分钟空闲/长期稳定性或启动P95。release原生可执行文件14,563,328B；两个新增Go文件共5,780B；任务目录文件大小逐项保存在artifact-manifest.json。截图和验证脚本均留scratchpad，组件本身不读写这些文件。
