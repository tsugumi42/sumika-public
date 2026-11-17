# 停止生成按钮画法规格

ctl-spec；版本、T/L、DIP、RTL、净室边界见 [settings-section](settings-section.md#坐标来源与边界)。Telegram本版有真实SendButton::Stop分支，可以直接参考几何；“AI停止整轮生成、等待停止结果”是宿主语义扩展，登记 [CTL-STOP](../exceptions.md#ctl-stop)。不把现有compose.Cancel的叉形说成Stop。来源 `T/ui/controls/send_button.cpp:91-100,208-225,336-352`。

## 样式与图形

| 项 | 常量 / 数值 / 角色 | 来源 / theme |
|---|---|---|
| 外框 | historySendSize44×46，inner继承historyAttach | `T/chat_helpers/chat_helpers.style:1431,1523-1528`；compose.sendWidth/Height |
| 停止图形 | historySend.stopSize14×14、stopRadius3 | 同文件 `:1538-1539`；待补compose.stopSize/Radius |
| 前景 / hover或down | historySendIconFg / historySendIconFgOver | `T/ui/controls/send_button.cpp:351`；已有compose.sendIconForeground/Hover |
| ripple | 直径40，origin(2,3)，windowBgOver | `T/chat_helpers/chat_helpers.style:1340-1342,1525`；compose.rippleSize40 |
| 编辑区定位 | 距右2，底部对齐发送按钮槽 | 同文件 `:1329`；[compose](../compose.md#数值和主题) |

局部W44×H46，停止实心框 `(15,16,14,14)`，圆角3，高质量抗锯齿、无描边。圆形40仅是按压ripple遮罩，不是常驻蓝圆；按钮透明融入输入底。先ripple后实心方块；真实方块无需外部图标或GPL素材。来源 `T/ui/controls/send_button.cpp:336-352`。RTL镜像槽到输入区起始侧，方形与圆形共用浮点局部中心；不要独立吸附造成125%偏心，沿用 [centering](../centering.md) 的本库映射。

## Send与Stop互换

同一个稳定动作根、同一44×46槽，只换动作内容和可访问名称，不在行内追加第二个停止键；编辑字段宽度不随纯Send/Stop互换改变。切换以universalDuration120ms linear，保留from/to内容快照；同值不重启。来源 `T/ui/controls/send_button.cpp:40-60,134-147`。

旧内容居中以alpha `1-a`、可见scale `1-4a/5`收缩，新内容以alpha a、可见scale `(1+4a)/5`显现。两张快照先以kWideScale5扩边，画布目标宽从5w→w及w→5w，实际图形只占画布的1/5，因此不是0→1的可见缩放；a=.5时两者均为原大小的.6，不能把kWideScale当无影响的存储细节。目标矩形按源码整数插值，取整误差单列。来源 `T/ui/controls/send_button.cpp:24,166-205`。切换时width可插值以支持官方其他状态；纯Send/Stop两端同宽不移动槽。中途再次切换捕获当前可见内容再向新目标120ms过渡，不跳回先前端点。ReduceMotion/Instant直接新终态。完成后释放快照，不保留持续动画请求。

## 生成状态（我定的）

| 宿主状态 | 动作槽 | 可激活行为 |
|---|---|---|
| Idle | Send或宿主当前录音/保存入口 | 沿用现有compose合同 |
| Generating | Stop图形、名称“停止生成” | Click/Enter/Space交付OnStop(runID)，保留草稿 |
| Stopping | 保持Stop图形，名称/描述“正在停止”，禁用 | 不重复报告；不因一个UI回调就当已停止 |
| StopFailed且仍在生成 | 宿主恢复Generating并提供错误说明 | 允许再次明确停止；错误在编辑区外状态行显示 |
| Stopped/Completed | 宿主切回Idle入口 | 若已有草稿，恢复发送；不清草稿/不自动发送 |

runID由宿主持有；切会话、切轮次、迟到结果都由宿主关联，组件不取消goroutine或网络。停止只表达请求，不等同删除消息/拒绝审批/取消上传。停止入口与编辑器整体Disabled分开，生成时即使禁止再次发送也必须保留可用Stop；宿主可以显式禁止编辑草稿，但不能把整条Disabled顺便禁掉停止。OnStop不自动触发Sound，保留平台禁用0.5且不二次乘alpha。

## 后续验收输入

发送→生成→请求停止→确认停止、失败后仍生成、自然完成先于停止结果、重复点击/自动重复键、切会话/新runID；空/非空/多行草稿、整体输入只读但Stop可用；普通/hover/down/disabled、0/60/120ms、快速反向、明暗/RTL/三档缩放。量44×46、14×14、3角与圆40中心，验证文字编辑宽和草稿不变。未实施真实生成或UI。

## theme 要补的

| 建议键 | 数值 / 来源 |
|---|---|
| compose.stopSize / stopRadius | 14 / 3；`T/chat_helpers/chat_helpers.style:1538-1539` |

其余复用compose.sendWidth/Height、sendRight、rippleSize、swapScale5、motion.composeButton120 linear与rippleShow/Hide；ripple原点从外框与圆直径居中推导。语义状态和停止结果不进theme。无Telegram音频或图标素材。
