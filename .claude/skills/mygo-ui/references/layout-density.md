# 布局与密度

使用时机：包装层、间距、长中文、内容宽度、缩窗和 DPI。出处/版本见 sources.md。

## 先闭合约束

Row 默认横排、垂直居中；Column/Box 纵排、横向 stretch。主栏 Grow(1).MinWidth(0)，内部滚动区 Grow(1).MinHeight(0)；固定侧栏和操作 Shrink(0)，可截断文字列 Shrink(1).MinWidth(0)。List/Scroll 必须在有限父高度内。出处：M docs/ui/layout.md:16–42、docs/ui/list.md:17–18；K settings_view.go:447–456；[源码]。

每个包装层写清职责：身份/流式布局/滚动/裁剪/背景/浮层。只传尺寸、不增加上述职责的层可合并；不要给每段文字都套卡片，也不要全页套多个 Scroll。层数不设武断上限，审查数的是职责重复和嵌套滚动。依据：M docs/ui/layout.md:14–29、125–139；L desktop/inspector.go:154–204；[源码→推断]。

## 可直接用的行关系

MIT，按 K settings_view.go:447–456 的弹性关系独立组合；t 为 c.Theme()。

```go
ui.Row(c).Gap(t.Space(2)).Children(func() {
    ui.Text(c, title).Grow(1).Shrink(1).MinWidth(0).SingleLine()
    ui.Text(c, stamp).Shrink(0).TextColor(t.TextMuted)
    ui.Button(c, "打开").Shrink(0)
})
```

SingleLine 负责省略，不能替代有限宽度；正文按需要换行，不将所有内容压成一行。M docs/ui/layout.md:33–42、docs/ui/text.md:3–4、33–38；[文档→推断]。

## Lorca 实际取值与关系

下面仅记录 L bf1ab709 的 GPL-3.0 做法/参数，没有代码摘录；它们是参考尺寸，不是 Sumika 新默认值。

| 表面 | 取值 | 关系/出处与可信度 |
| --- | --- | --- |
| 三栏 | sidebar 232–340；inspector 268–320；正文目标最小 460；header 52 DIP | 先缩 inspector，再缩 sidebar；仍不够隐藏 inspector；desktop/mainwindow.go:18–25、534–562；[源码] |
| 气泡/组 | 最大宽 580；横向 22；头像 26+间隙10；组首14、连接行4 | 用户侧另留72，消息按发送者分组；desktop/chat.go:21–31、326–346；[源码] |
| 气泡内部 | Padding(10,14)、Radius(14) | 与父可用宽相容，不能只把一组数字复制到无界容器；desktop/chat.go:350–385；[源码] |
| composer | 外侧 Padding(8,20,14,20)，1–8行、行高19，圆角22 | 浮动层真实测高再回填；desktop/composer.go:230–255、335–336；[源码] |
| 侧栏行 | 高54、头像槽38、圆角8 | 标题/预览可缩，时间/未读保持；desktop/sidebar.go:437–561；[源码] |
| 小按钮 | 图标按钮28、图标16；push 常规高24、小22、大32 | 命中框与墨迹不同；desktop/controls.go:23–76、101–111；[源码] |
| 正文/字阶 | message13.5、body13、small12、caption10.5 | 是桌面密度观察，不作为大字/触屏可访问性保证；desktop/theme.go:166–175；[源码→推断] |

项目间距用统一刻度（theme.md），内容宽度由父约束和可读行长确定；不要同时依赖 v0.7.4 的 MaxWidthPercent 与 MaxWidth 求交。该版本两者写入同一个约束字段，后设覆盖；配方用算出的单一 MaxWidth。依据：M ui/element.go:851–858，详见 pitfalls.md；[源码→项目选择]。

## 四种几何分开

布局框、图标画布、实际墨迹、命中框分别记录。图标等比缩放；文字对齐看 baseline/实际行高；不要用图像透明边距当墨迹尺寸。边框参与盒模型，Absolute 从父边框内侧定位；需要中心对齐先用 flex。依据：M docs/ui/layout.md:61–97；S LESSONS.md:308–314、docs/决策记录.md:3361；[源码/项目经验→推断]。

嵌套卡片统一 inset，内圆角从外圆角与 inset 推导；左右基准线共用。改完用 bounds 测，不凭截图相似。依据：S LESSONS.md:308–314；[项目经验]。

布局单位 DIP；SetScale(1.25) 改物理像素，SetZoom 改内容缩放，TextScale 改文字偏好。2048×1030 DIP 在1.25下，数学高度1287.5像素需要由 renderer 取整，记录实际值，不硬写1288保证。出处：M ui/headless.go:371–388、412–418、456–468；[源码→推断]。

验收最小/窄/目标/宽窗、浅深色、大字、长中文与长路径；检查按钮未被挤走、滚动仅一个所有者、裁切符合产品要求。开检查器会缩窄 c.Size()，量布局时注明是否开启。M docs/ui/inspector.md:22–23；[源码→项目验收]。
