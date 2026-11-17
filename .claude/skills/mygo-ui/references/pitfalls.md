# 常见坑与反模式

使用时机：开始实现前与交付审查。出处代号、版本与可信度见 sources.md。

## 更正官方 maintenance 的输入时序

项目副本与 v0.7.4 模板都写「绑定在 construction/configuration 后更新，Changed/Submitted 到下一 build pass」。当前固定版本的 ui/doc.go、views.md 与 value_input.go 则明确：公开控件构造器返回前应用输入；紧接着读取绑定值和 Changed 就能取得本次编辑。容器包含子项，等容器构建返回再处理输入。当前配置通常支配用户在下一显示帧上的输入，不能用新 Disabled 值追溯撤销上一帧点击。出处：M docs/ui/views.md:71–75、99–148；ui/value_input.go:5–14、42–52、71–89；[源码]。

下面是 M docs/ui/views.md:115–119 的 MIT 直接模式；viewed 为 map[string]bool，fileID 来自业务模型。

```go
local := viewed[fileID]
if ui.Checkbox(c.Key(fileID), &local, "已读").Changed() {
    viewed[fileID] = local
}
```

不要因此删除写回；副本仍必须明确提交。OnClick/OnChange/OnSubmit 可把修改推到构建结束，适合删改正在遍历的集合；不是等「下一次用户输入」。同一输入已消费后重建不会重复执行。M docs/ui/views.md:74–97、138–148；[源码]。

## 三份材料迁移到 v0.7.4 的修正

| 材料里的写法/推论 | 本 skill 的处理 | 最终依据/可信度 |
| --- | --- | --- |
| A 以未发布 main 文档和示例为主，Kero 又用 v0.7.5 伪版本 | 所有可调用代码改为 M；不使用 main 新方法/字段 | M ui/theme.go:7–57、ui/transition.go:20–68；[源码] |
| A 列 Transition 的 Width/Height、ExitDuration/ExitEase、AfterExits/ChildExit、Motion.Blur | M 只有 Position/Size/Colors、Enter/Exit/Match 和 Motion 的基本字段 | M ui/transition.go:20–68；[源码] |
| A 称 ElementTransition.Duration<0 立即到位 | M 未设 Animation 时，Duration<=0 回退200ms；AnimateTo/AnimateColor 负时长 panic。即时变化直接给值，或按需要用 c.DisableAnimations | M ui/transition.go:190–220、ui/motion.go:302–305、466–469、ui/context.go:466–473；[源码] |
| A/main 的 FocusTakesText、OnWindowShortcut、TabStop、LabelledBy | 不调用；使用已存在的 Handle/区域快捷键、Field、Label | M ui/ref.go:170–177、docs/ui/input.md:149–199、docs/ui/overlays.md:34–45；[源码检索] |
| A/main 的 Tester.Access/AccessByRole、命名 Click 自动证明无遮挡 | M 无前两者；Click 只匹配标签与中心在窗口内，必须验动作/状态 | M ui/headless.go:459–516；[源码] |
| A/main 的 Theme 扩展、NonMotion、Local io.Closer 自动关闭 | 不依赖；按当前字段和显式资源所有权处理 | M ui/theme.go:7–57、ui/motion.go:16–27；[源码检索] |
| A/main 的 automation CLI 短 refs/settle diff | 本版使用 plugin 的 NDJSON unix socket 协议，无新增 CLI 前提 | M docs/plugins/automation.md:46–90；[源码] |
| B 的小会话列表/普通 Scroll 配方 | 只借行结构和状态语义，增长集合改为 List + ID | M ui/list.go:35–97；O source/components/chat/chat.go:413–416；[源码] |
| B/O 的 Draw 回调写高度后重绘 | 借高度回填机制，改在构建时读 Handle 已提交 Bounds 并补帧 | O source/components/chat/floating.go:119–130；M ui/ref.go:203–217；[源码→项目选择] |
| Lorca 的 MaxWidth(580)+MaxWidthPercent(100) | M 两方法写同一 maxW；不用它们求交，先求单一上限 | M ui/element.go:851、858；L desktop/chat.go:351、382；[源码] |
| TextArea.Lines(1,8) 用于紧凑输入 | M 的 styled TextArea 仍有 t.Space(20) 最小高度，需显式 MinHeight(0)；Lines 只改编辑器行数 | M ui/textinput.go:15–22、216–224；[源码/渲染核对] |
| Lorca 的旧 Element/焦点/动画 helper 与测试 Sleep | 仅借结构取值；用 M 的值 API、Handle、Advance | M docs/ui/testing.md:52–79；L desktop/render_test.go:153–176；[源码] |
| Lorca 切聊天就新建 chatState、持续绿点/shimmer | 不当成全会话草稿缓存或 ReduceMotion 已完整覆盖 | L desktop/chat.go:56–61、avatar.go:101、136、chat.go:1037；[源码→推断] |

Themed 已在 M，不误删成 main 专属。M ui/api_gen.go:2378–2386；[源码]。

## Sumika 自身的反例与要保留的现状

| 情况 | 风险与改法 | 出处/可信度 |
| --- | --- | --- |
| 数字路由 | a.rail==4 等与数组顺序耦合；边界转命名 page ID，不加新路由框架 | S native/ui.go:17–19、39–59、97–106；[源码→推断] |
| app.rowAt 隐式上下文 | 图片/转发依赖先写当前行；显式传消息/范围/分组上下文 | S native/row.go:12–18、73–82；[源码→推断] |
| 查看器另一套视觉公式 | 独立按钮 size、alpha、scrim；保留缩放平移手势，统一视觉角色 | S native/viewer.go:281–301；[源码→推断] |
| 下标当身份 | 插入/过滤后对象变了；key 和业务选择都用 ID，下标仅当前适配 | M ui/list.go:42–47、66–71；[源码→推断] |
| 旧页/回收重播新消息动画 | 保留 arrival 的 live/ID 水位线，别用 len 判断新消息 | S native/arrive.go:16–97；[源码，保留现状] |
| 为统一外观删性能边界 | 保留可见范围加载、revision 快照、媒体字节/像素/并发预算 | S native/convlist.go:93–143、images.go:35–41、82–90；[源码，保留现状] |

这些是维护判断；没有在本任务启动真实 Sumika，不声称上述风险都已造成故障。把「自写查看器」「播放动图」「有守卫的 view 调度」本身判错，会替换掉真实需求。[推断]，依据见上表。

## 交付前快速拦截

不保存构建值、不在帧内 I/O、不用无 key 的条件兄弟、不每帧重建输入/Router、不把 MaxHeight 当 List 视口、不把 Window.Update 当等待、不以 Tester.Click(nil error) 当成功、不清空内容后再播退场。各自依据：维护 skill；M docs/ui/views.md:53–58、150–184、docs/ui/list.md:17–18、content.go:171–176、ui/headless.go:490–516、docs/ui/transitions.md:113–123；S LESSONS.md:394–396；[源码/项目经验→验收]。
