# 结构与状态所有权

使用时机：新窗口、页面切换、分栏、业务状态与控件状态归属。出处代号和版本见 sources.md。

## 从完整场景拆结构

先写页面的输入、可见输出、动作和状态转换，再定壳、区域和组件。参考必须表达「谁随谁变、谁在谁里面、何时隐藏、隐藏后保留什么」；新界面先在独立假后端的交互原型里跑通。依据：S docs/决策记录.md:3359–3364；[项目约定]。

窗口根负责安装主题、组合稳定的导航/正文/右栏与浮层；页面负责展示投影和动作；组件负责一族视觉及输入反馈。L 的窗口先 applyTheme、view、sheetsView，controls/sections 再被各页面消费；这是组织方式，不要求复制其目录或全局 store。出处：L desktop/window.go:46–52、controls.go:23–140、sections.go:49–117；GPL-3.0，仅结构；[源码→推断]。

| 状态 | 所有者 | 失效/重置时机 | 依据 |
| --- | --- | --- | --- |
| 会话、消息、审批版本、请求结果 | 业务 model/store | 收到领域更新 | M docs/ui/views.md:18–20；L desktop/model/store.go:135–245；[源码] |
| 当前页面 ID、侧栏开关、抽屉栈 | window model | 导航/窗口关闭 | L desktop/mainwindow.go:27–72、overlay.go:35–98；[源码] |
| 草稿、ListState、选中 ID、媒体阅读位置 | 每窗口 × 会话 ID 的状态 | 明确切换/淘汰策略 | M ui/list.go:35–65；L desktop/chat.go:34–61；[源码→推断] |
| 微交互/动画进度 | 控件内部或 ui.Local | 元素身份结束 | M docs/ui/views.md:186–189；[源码] |

生命周期、身份和线程规则统一读 [mygo-maintenance](../../mygo-maintenance/SKILL.md)，不在本页重列；它的输入时序按 pitfalls.md 更正。依据：M docs/ui/views.md:8–20、71–148；[源码]。

L 切聊天会新建 chatState（desktop/chat.go:56–61），并没有自动保留每个聊天的草稿。Sumika 要保留时明确用 chat ID 索引状态；不要复制该重置行为后宣称已满足需求。GPL-3.0，仅行为；[源码→推断]。

## 稳定壳与页面

下段为 M docs/ui/layout.md:16–42、docs/ui/views.md:150–166 的 MIT API 独立组合。left/body 是调用者提供的同步构建闭包；返回值只供本次构建继续配置。

```go
func Frame(c *ui.Context, left, body func()) ui.Element {
    return ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
        ui.Column(c.Key("navigation")).Width(260).Shrink(0).Children(left)
        ui.Column(c.Key("content")).Grow(1).MinWidth(0).MinHeight(0).
            Children(body)
    })
}
```

260 只是配方尺寸，应进入产品布局参数。条件侧栏出现/消失时正文身份保持稳定；同一页面的编辑状态不应因兄弟位置变化而串位。[源码→推断]；M docs/ui/views.md:150–166。

有历史导航用初始化一次的 ui.Router，长期导航壳放 Router.View 外；少量平行面板可以命名枚举 switch。不要把页面身份绑到显示顺序或中文标题；Tab 的位置索引在组件边界转成业务 ID。依据：M docs/ui/views.md:53–58、docs/ui/navigation.md:1–48；S native/ui.go:17–19、39–59、97–106 是数字路由反例；[源码→推断]。

## 帧内上下文显式传递

行 helper 的输入明确包含记录、邻居/分组、所属列表可见范围与操作。不要先改 app.currentRow，再期待图片、头像或嵌套转发从 app 猜出当前行。S native/row.go:12–18、73–82 是已有隐式上下文反例；目前只判断维护风险，没有把它写成已复现串播。[源码→推断]。

验收：切页/收放侧栏前后输入属于同一记录；会话 A/B 草稿独立；关闭最上层浮层后返回原页面与焦点。方法见 testing.md。依据：M docs/ui/testing.md:20–47、docs/ui/overlays.md:34–45；[源码→项目验收]。
