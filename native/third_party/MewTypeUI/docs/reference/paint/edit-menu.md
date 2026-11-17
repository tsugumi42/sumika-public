# 标准编辑菜单桥（mw-gaps2）

## 任务卡

用户目标：对当前消息正文复制选区或全选，复制保留宿主 CopyData 的表情文字还原，右键菜单关闭后继续操作同一选区。作用域为宿主持久 ui.Handle 绑定的 Selectable 容器；命令即时作用于该正文，宿主持有文本和 CopyData。入口为正文右键/菜单键/Shift+F10，无设置或持久化。

## 命令与作用域

参考 mygo v0.7.4 ui/menu.go:30–42,176–195 的标准编辑菜单对象：在正文容器上用 ContextMenu 的 ui.Menu 加入业务条目及 EditItems。该对象绑定菜单实际打开的正文段落；复制和全选走引擎原有路径。ui/textselection.go:342–370：全选覆盖同一 Selectable 范围，复制调用范围上的 CopyData；ui/ref.go:134–142：只在当前构建 Resolve 持久 Handle，不保存 Element。

提供 EditContextMenu(c, scope ui.Handle, build func(*ui.Menu)) ui.Element。宿主在正文绑定 Handle、设置 Selectable/CopyData 后调用；build 构造同一菜单中的宿主项，末尾由库加入标准 EditItems。命令由 ui.Menu 执行，不通过 ID 回调重新实现复制，不创建第二套并排菜单。

mygo v0.7.4 没有公开以 Handle 直接 dispatch 编辑命令的方法。按原缺口清单给出的替代方案，采用标准 ui.Menu 对象生成并执行编辑项。菜单只处理实际正文，不给其他焦点控件发送合成按键。隐藏目标当帧不注册菜单。

## 选区、焦点和画法

参考 Telegram 消息菜单的选择范围与复制方式：T/history/history_inner_widget.cpp:3586–3600 的选区复制菜单项，:4015–4029 的选区到 CopySelection；本桥不新增正文绘制。具体命令语义以固定 mygo 的真实标准实现为准。

使用 mygo 的原生标准菜单，而非 overlay 自绘 flyout：尺寸、字体和开闭由系统菜单负责，无额外绘制参数。正常选择/取消不调用 scope.Focus，因为这会把焦点从正文段落移到容器（mygo ui/input.go:1312–1328）；引擎保留原段落焦点与选区（ui/textselection.go:521–531、ui/menu.go:547–557）。关闭后立即可继续 Ctrl+C；系统取消与新输入即时打断，减少动效遵循系统原生菜单。

这是我定的兼容映射：固定依赖缺少安全的公开直接命令桥，使用其已有标准菜单对象，系统菜单外观不声称与 Telegram 自绘菜单逐像素相同。没有修改依赖缓存、fork mygo、unsafe/linkname 或宿主剪贴板替代实现。
