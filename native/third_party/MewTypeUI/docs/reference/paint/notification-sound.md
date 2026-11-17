# 提示音接口与播放行为规格

ctl-spec；版本和T/L路径沿用 [settings-section](settings-section.md#坐标来源与边界)。本页无绘制像素；规定组件向宿主交付声音意图的边界。接口形状与通用交互cue为“我定的”，登记 [CTL-SOUND](../exceptions.md#ctl-sound)。参考官方通知调度先判静音/资格、独立控制播放音量、缓存已准备资源的做法；不复制其读取文件、内嵌音频或音频引擎。来源 `T/window/notifications_manager.cpp:163-177,332-391,727-785,995-1045`、`T/media/audio/media_audio_track.cpp:145-163`。

## 官方何时响

| 条件 / 行为 | 官方事实与来源 |
|---|---|
| 通知是否参与 | thread无当前通知、item.skipNotification则跳过；退出应用/未启用其他账号通知也跳过 | `T/window/notifications_manager.cpp:314-327,356-360` |
| 静音资格 | thread设置未知先等待；thread未静音或特殊通知来源未静音才可能提示，sound.none排除声音 | 同文件 `:163-177,372-390` |
| 静默消息 | silent标志、sound.none、非Message类型不进入这一路声音队列；从计划消息发出的outgoing可展示但强制silent | 同文件 `:338-352,377-390,459-462` |
| 成组播放 | 同thread队列把本次到期后500ms内的后续alert一并移除；一次showNext最终只对选定alertThread播放一次 | 同文件 `:61-64,727-751,767-785` |
| 全局开关 | soundNotify必须开启；默认true | 同文件 `:767`；`T/core/core_settings.h:1090` |
| 原生通知 | 可能由OS通知系统负责声音；volumeSupported检查desktopNotify/ManagerType::Native及平台支持，避免重复本地播放 | `T/window/notifications_manager.cpp:1062-1067,1570-1573,1644-1653` |
| 显式试听 | playSound入口直接播指定资源与volumeOverride，不经过常规通知资格 | 同文件 `:1080-1084` |

这些是通知事件规则，不是“每个按钮点击都响”。没有本任务所查官方控件paint路径要求选择/开图/关图/翻页强制响；LinkButton、RoundButton、Tabs等不得继承CrychicUI的自动Tap/Select。宿主要保留这些交互cue时必须显式启用，属于本库扩展。

## 音量与静音

官方通知音量优先读取thread级非0值，其次通知类别值，再用全局notificationsVolume；Thread和类别0表示未覆写。传给playOnce的正override直接成为OpenAL gain，否则回退全局百分比/100。**它不是“主音量×会话音量”相乘。**来源 `T/window/notifications_manager.cpp:770-781`、`T/media/audio/media_audio_track.cpp:155-160`。全局默认100；官方声音音量滑块100档、1…100，0不是滑块静音档。来源 `T/core/core_settings.h:1221`、`T/data/notify/data_peer_notify_volume.cpp:31-35,45-50,106-114`。关闭声音用soundNotify/none/mute，不靠override=0。

本库采用明确的宿主输入：Enabled、Muted、Silent、Eligible、OSOwnsSound、可选VolumePercent（未设置与显式0分开）。有效输出gain∈[0,1]；显式0为静音、完全不发播放请求，不沿用Telegram“0是继承”的隐式歧义。宿主先按上段解析继承，再传最终音量。系统DND、活跃聊天是否已读、免打扰时段、账号策略在宿主资格判定中处理，不由组件查操作系统/数据库；OSOwnsSound=true时不再本地响。此映射明确登记CTL-SOUND，不声称Telegram有同名结构体。

## 只给接口的合同（我定的）

音频资源必须由宿主准备并拥有生命周期：采用宿主定义的不可变PreparedSound句柄，指向已加载/解码数据；库只持短生命周期借用，不按每次hover拷贝字节，不读取路径、不解码、不建立设备、不带默认资源或静音替代音频。若宿主想用原始声音数据，先自行准备句柄，再通过callback接收。

建议接口职责是 `播放请求{EventID, ScopeID, CueID, Resource, Gain}` 与宿主提供的 `OnPlay(request)`；此为文字合同，尚无公开Go API。EventID在一次用户动作/通知生命周期内稳定，ScopeID用于宿主通知合并；CueID为宿主定义，不内置Select/Open/Close/Swipe枚举。组件构建和paint不得自动OnPlay；只在已处理动作/显式事件入口交一次请求，rebuild/重挂/动画帧不会重播。

通知合并可由宿主沿官方500ms窗口处理；库不在UI子节点开启定时器、队列或无限去重缓存。多窗口事件ID和窗口存亡由宿主协调，同一次OS通知只选择OS或本地一个声音所有者。声音开始后组件卸载不擅自停播，宿主控制播放终止/混音；需要回报失败由宿主播放层提供显式结果，不吞错成成功。不要顺便引入OpenAL/FFmpeg依赖。

试听是独立明确动作，允许越过会话静音/通知资格，但仍尊重全局显式禁音与音量0；这是本库选择，与官方直接playSound区别登记CTL-SOUND。未知资源时宿主显示不可试听，不让组件自行找默认Telegram素材。没有事件则没有声音请求。

## 后续验收输入

人工合成或允许分发的宿主声音：消息正常/静默/静音/未知资格、thread/类别/全局继承和显式0、连续同scope500ms合并、多窗口同EventID、OSOwnsSound、全局禁音、试听、播放失败；分别确认宿主回调次数和最终gain。真实音频另量设备输出/OS音量/时延，回调次数不证明用户实际听到声音。本任务只规格，不播放真实声音、不调用真实消息后台。

## theme 要补的

无需新增theme颜色、尺寸、字阶或motion。音量、静音、通知500ms合并窗口是宿主行为偏好/调度参数，不是视觉主题；不要把audio gain当opacity token，或将100默认音量写入theme生成器。声音设置界面复用已有settings.Toggle、MediaSlider和本组Dropdown；展示值1…100来自上述源码，业务是否允许0由宿主声明。
