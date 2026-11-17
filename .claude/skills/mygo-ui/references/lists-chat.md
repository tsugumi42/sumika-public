# 列表与聊天

使用时机：会话列表、万条历史、流式消息、前插、浮动 composer。版本与代号见 sources.md；可运行组合在 recipes.md。

## 五个职责分开

| 职责 | v0.7.4 写法 | 证据 |
| --- | --- | --- |
| 限制构建数 | 有界 List；只有 MaxHeight 不够 | M docs/ui/list.md:5–18；[源码] |
| 保持业务身份 | ListState.Key 或 fluent ItemKey，取消息/会话 ID | M ui/list.go:42–47、ui/list_builder.go:20–53；[源码] |
| 选中记录 | 业务 SelectedID，组件的 Selected 下标只作适配 | M ui/list.go:66–92；[源码→推断] |
| 阅读位置 | 独立 ListState，默认按可见项锚定；聊天不用 KeepOffset | M ui/list.go:48–65；[源码] |
| 跟随最新 | FollowEnd=true；用户上翻暂停，回尾恢复；按钮 ScrollToEnd | M docs/ui/list.md:68–73、176–199；[源码] |

配置在构建前；fluent List 的 ItemKey/Selection/尺寸等先于 Rows。不要先传 row 回调建完再补身份。依据：M ui/list_builder.go:20–38、47–88；[源码]。

## 最小虚拟 transcript

M docs/ui/list.md:187–199 的 MIT API 组合；Message 为宿主类型，消息需要 ID，row 是真实渲染函数。state 由会话持有。

```go
func Transcript(c *ui.Context, state *ui.ListState, messages []Message, row func(int)) ui.Element {
    state.Key = func(i int) any { return messages[i].ID }
    state.FollowEnd = true
    return ui.List(c, state, len(messages), row).
        Grow(1).MinHeight(0).Justify(ui.End)
}
```

是否平滑尾随是产品选择，FollowAnimation=nil 即刻贴底；不要因为别的产品用了 &ui.Smooth 就覆盖用户偏好。S native/main.go:431–442 为应保留的现状；M ui/list.go:59–65 为契约；[源码]。

## 前插与变高

旧页前插保持 ID；不要自己累加「新增高度」。分组/日期行有独立身份，前插不能改变已有第一行的名字、头像、上边距而不验几何。图片/视频先按宽高占位；资源晚到只替换内容。依据：M docs/ui/list.md:54–73；S LESSONS.md:372–375、native/row.go:15–28（需保留的分组契约）；[源码/项目经验→推断]。

Key 不是无限距离搜索的保证：M ui/list.go:31–33 的 listSearch=1000；大批重排/跨万条前插要实测，必要时由领域 ID 明确找新位置再 ScrollTo，而不是声称任意变化都自动无跳。[源码→推断]。

Visible 靠近头部只作为取旧页信号；HasMore、loading、会话 ID 与请求代次决定是否启动/接收。成功合并去重后再展示，失败保留错误和可重试动作；不要在每次 view 重建启动新请求。L 的旧页请求有首条 ID 检查，这是做法参考，GPL 不抄实现。出处：L desktop/model/store.go:2634–2667；M docs/ui/views.md:3–6；[源码→推断]。

业务到达和元素挂载分开：历史页、过滤恢复、离屏回收都不是新消息；入场按 ID 与事件发生时间决定。S native/arrive.go:16–97 是要保留的现状；不改成 len 增长就重播全部行。[源码→推断]。

## 浮动 composer 高度回填

浮动输入区离开文档流，transcript 必须留实际高度的底部 padding，滚动条也避让。L 的 composerHeight+14 用于内容，ScrollbarInsets 避开输入区；测得 Bounds 与上一帧高度不同就请求下一帧。O 同样用真实高度留尾部空间。出处：L desktop/chat.go:175–212；O source/components/chat/floating.go:68–81、119–130；L 为 GPL，仅机制；[源码]。

本项目实现采用构建后读取已提交 Handle.Bounds(c)，更新持久 height 并 c.AnimationFrame；草稿、区域宽度或窗口尺寸变化时也请求下一帧，因为本轮测得的高度仍可能等于旧值。Draw 只画；不要搬 O 在 Draw 回调里写布局状态的实现，也不要估算「字符数×行高」。recipes.md 的 Chat 是独立 v0.7.4 组合。依据：M ui/ref.go:205–218、ui/context.go:460–464；[源码→项目选择，本次探针验证此边界]。

必须覆盖附件/引用出现、1→8 行、自动折行、发送清空、删除附件和窗口收窄；不再输入、不手动 Frame 也应收回空白。L composer_test.go:32–76 专门防清空后留白不恢复；[源码→项目验收]。

## 验收数字

同一锚点 ID 前插前后 Y 位移；上翻后追加不拉回；回尾后追加重新跟随；万条中末帧构建行数；composer 高度变化前后最后消息底边和输入区顶边。用长短中文、代码与图片占位混排，固定高探针只能证明其自身路径。依据：M docs/ui/testing.md:28–79、L desktop/chat_test.go:15–79；[源码→项目验收]。全篇选择需求不能靠虚拟行的 Selectable 自动完成，离屏段落尚未构建，需独立文档选择模型。M docs/ui/text.md:40–70；[文档→推断]。
