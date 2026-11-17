# 交互

使用时机：焦点、快捷键、IME、命中框、菜单、浮层退层。出处代号与版本见 sources.md。

## 焦点与命令作用域

生命周期、持久身份和线程规则读维护 skill；交互只在所属窗口/区域注册，输入框的编辑键先处理，不用根级 Enter/方向键覆盖所有页面。依据：M docs/ui/overlays.md:34–45、docs/ui/text-input.md:60–85；[源码→项目选择]。

MIT API 组合，model 字段是 query string、search ui.Handle；必须在适用的区域作用域里声明命令。

```go
ui.TextInput(c.Key("search"), &a.query).Bind(&a.search).Label("搜索")
a.search.OnShortcut(c, ui.Cmd, ui.KeyF, func() { a.search.Focus(c) })
```

出处：M docs/ui/list.md:222–242、ui/ref.go:170–177；[源码]。这个示例是目标存在且启用时的 scoped 命令；如果要从其他区域跳搜索，由窗口壳声明 c.OnShortcut，不能期望焦点控件内部命令全窗口生效。ui.Cmd 在 Windows/Linux 为 Ctrl，macOS 为 Command。依据：M docs/ui/input.md:149–199；[文档]。

## 聊天输入

M docs/ui/text-input.md:74–85 的 MIT API 模式；send 接受文本且不再读取旧 draft，发送成功/失败后的产品行为由宿主定义。发送不能只因为某次重建自动触发。

```go
ui.TextArea(c.Key("composer"), &a.draft).Lines(1, 8).
    Label("消息").OnTextKey(func(k ui.TextKey) bool {
        if k.Key == ui.KeyEnter && k.Mods == 0 {
            a.send(k.Text())
            k.SetText("")
            return true
        }
        return false
    })
```

Shift+Enter 留给编辑器换行。组合期间 Enter/Esc 属于 IME，不提交、不触发快捷键、不关 dialog；自定义 HandleInput 时显式检查 Composing。TextKey 只在回调期间有效。出处：M docs/ui/text-input.md:60–85、139–145；[源码]。

## 命中框、墨迹和名称

按钮外框决定命中尺寸；小图标的 Size 只决定画布。用 Button/Base 保留 Tab、Enter/Space、disabled；图标按钮 Label 提供语义名，Tooltip 是补充。L 28×28 外框内16图标只是桌面密度参考；本项目配方用32 DIP，涉及触控再按产品目标调整。出处：M docs/ui/custom-widgets.md:5–33；L desktop/controls.go:23–76，GPL，仅取值；[源码→项目选择]。

背景整行可点与右侧动作分开；点击附件/菜单不再触发行打开。右键操作绑定被点行 ID，不用当前选中 ID 代替。Selected-only List 的右击不改选择，多选 Selection 默认会选择被右击行，MenuKeepsSelection 可保留选择。出处：L desktop/sections.go:172–173；M docs/ui/list.md:124–138；[源码]。

## 浮层只退一层

优先 Modal/Popover/DialogBase；自定义抽屉用 Overlay + Modal + OverlayShortcut，顶层拥有退出动作。Popover 外点会透传到底下按钮，Select popup 会消费该按下；不能给所有浮层套同一份外点逻辑。M docs/ui/overlays.md:34–50、77–105；[源码]。

| 当前层 | 输入 | 预期 | 来源/可信度 |
| --- | --- | --- | --- |
| 抽屉 | Esc/抽屉外点 | 只关抽屉，恢复触发点焦点 | M docs/ui/overlays.md:42–50、88–105；[源码] |
| 转发面板 + 图片查看 | Esc/图片空白 | 只关图片，转发面板保留 | S LESSONS.md:316–319；[项目经验] |
| 对话框 + 输入法候选 | Enter/Esc | 候选处理，对话框保持 | M docs/ui/text-input.md:139–145；[源码] |
| 下层页面 + 上层模态 | 页面快捷键 | 不执行下层动作 | M docs/ui/overlays.md:36–45；[源码] |

外点判定使用按下那一刻的层栈，不在上层关闭后再用新状态判断同一次事件；详情见 recipes.md 抽屉。S LESSONS.md:316–319；[项目经验→推断]。

原生右键菜单在 Windows 可能进入系统循环，automation 不跨平台保证选择；用 Tester.RightClick/ChooseMenuItem 走菜单路径，真窗口自动化用 PopoverMenu 或应用菜单 API。M docs/plugins/automation.md:214–229；[源码]。

验收：Tab/Shift+Tab、Enter/Space、中文组合、按住/拖出/松开、外点、双层浮层、背后快捷键；断言真实动作 ID 与次数，不能只看文字还在。M docs/ui/testing.md:28–47；[源码→项目验收]。
