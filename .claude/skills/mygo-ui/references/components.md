# 组件分层与复用

使用时机：选现成控件、写应用 helper、扩展一个组件族。出处代号/版本见 sources.md。

## 先选行为，再选外观

| 需求 | 首选 | 边界/出处 |
| --- | --- | --- |
| 增长中的会话、聊天、任务 | ui.List + 独立 ListState | 有限父高度、业务 key；M docs/ui/list.md:5–18、54–82；[源码] |
| 有上限的设置/详情 | ui.Scroll | 构建所有内容，不能冒充虚拟化；M docs/ui/scroll.md:5–28；[文档] |
| 单行/多行输入 | TextInput / TextArea.Lines | 已有选择、撤销、IME；M docs/ui/text-input.md:5–19、119–145；[源码] |
| 普通/主操作 | Button / PrimaryButton | 产品视觉确实不同再用 ButtonBase；M ui/widgets.go:132–190；[源码] |
| 布尔/单选/页签 | Switch、Checkbox、Radio、Tabs、Segmented | 用对应 Base 定制；不把 Box+Clicked 当完整控件；M docs/ui/custom-widgets.md:5–33；[文档] |
| 标签/说明/错误关联 | Field/Form | 点击与无障碍关联，不能只画旁边的 Text；M docs/ui/form.md:5–56；[文档] |
| 更多/右键、浮层 | Menu/PopoverMenu、Modal/Popover 或 Base | 保留焦点、退层与禁用；M docs/ui/overlays.md:34–86；[源码] |

分层：mygo widgets/Base → 项目 styled helpers → 领域行/卡 → 页面。只有领域层知道「审批」「工具」「消息」，只有业务层决定发送、授权和持久化。L 的 controls/sections/cards 是结构案例；不复制其 GPL 代码。出处：L desktop/controls.go:23–140、sections.go:129–187、cards.go:117–209；[源码→推断]。

## helper 的最小契约

首参当前 *ui.Context；必需数据直传；并列可选行为/外观用具名 options；内容用同步 func()；返回本轮 root Element 供调用者布局。多动作确实需要时返回具名结果。勿为几个 helper 建额外组件框架。依据：K settings_view.go:443–457；L desktop/controls.go:90–98、223–304、sections.go:129–187；[源码→推断]。

下面是 M docs/ui/custom-widgets.md:5–33、ui/theme.go:59–96 的 MIT API 组合；target 是外框命中尺寸，不是图标墨迹尺寸。

```go
func Action(c *ui.Context, label string, content func()) ui.Element {
    t := c.Theme()
    b := ui.ButtonBase(c).Size(t.Space(8), t.Space(8)).
        Center().Radius(t.Radius).Label(label).Tooltip(label)
    bg := t.Surface
    if b.Pressed() { bg = t.SurfacePressed } else if b.Hovered() { bg = t.SurfaceHover }
    return b.Background(bg).Children(content)
}
```

调用用 Action(c.Key("attach"), "添加附件", func(){ ...实际图标构建... }) 的结构；图标内容由宿主传入。生命周期、身份和线程只遵守维护 skill；不要保存 helper 的 root/parts。出处：M docs/ui/views.md:8–20、150–166；[源码]。

同一组件族的 variants 覆盖普通/主操作/危险、小/常规密度、图标/文字、disabled/loading；修改共享 variant，勿每页再调 padding/alpha/字号。K 的设置行用可缩文本列和不缩控件列；可复用其关系，不照抄 package 级 groupRows。出处：K settings_view.go:438–457；[源码→推断]。

## 交付一个完整族

先列消费者与状态：会话列表、聊天正文、转发/查看、右栏是否共用同一 helper；空、忙、失败、无回调、长中文、窄窗都要在同一场景验证。图标点击不能又触发父行；业务变化后不是只改主页面。依据：L desktop/sections.go:172–173；S docs/决策记录.md:3361–3363、LESSONS.md:316–319；[源码/项目约定]。

第三方库的「Conversation」可能只是普通 Scroll；目录里有组件不代表具备空集合、ID、分页、虚拟化和审批回写。O 明确不提供虚拟化与前插锚定；应用应借语义组合并保留 M 的 List。出处：O source/components/chat/chat.go:413–416；M docs/ui/list.md:5–18；[源码]。
