# 异步与性能

使用时机：加载、搜索、流式回复、图片、万条历史和卡帧。代号/固定版本见 sources.md。

生命周期、身份和线程规矩统一读维护 skill；本页只写界面工作量与结果语义。Window.Update 是异步投递，返回不代表回调执行完，Invalidate 也不提供模型写入同步。M content.go:150–176；[源码]。

## 每条请求有结果归属

按请求真实范围保存 loading/error/cancel/generation；切会话/查询/窗口时取消旧任务，并在 UI 发布结果时再比较当前源身份。取消不等于请求必然停，回主线程也不等于属于当前页面。L Async 自己只切线程；bootstrap 用 generation、sheet 用 closed 是各自边界。出处：L desktop/model/store.go:279–287、400–430、sheet_review.go:202–223；GPL，仅结构；[源码→推断]。

MIT API 组合，a.generation/a.closed 由 UI 线程拥有，request 和 load 是宿主的任务状态与函数；这段用于说明结果接收门，不替代现有任务系统。

```go
request := a.generation
win := a.win
go func() {
    result, err := load()
    win.Update(func() {
        if a.closed || request != a.generation { return }
        a.result, a.err = result, err
        a.loading = false
    })
}()
```

出处：M content.go:162–176、维护 skill State/work 段；[源码→组合改写]。官方 markdown-chat 示例里的 stopped 在 Update 回调写后立即由 worker 读，不符合该异步契约；不要照搬其 worker。只借 List/Stream 组合；这里是源码契约推断，未声称复现其用户后果。M examples/markdown-chat/main.go:111–133、content.go:171–176；[源码→推断]。

## 将工作移出帧

| 工作 | 发生时机 | 依据 |
| --- | --- | --- |
| 排序、过滤、消息行投影 | 数据 revision/query 改变时 | S native/convlist.go:93–143 为应保留的现状；[源码] |
| 文件读取、图片解码 | 资源到达/请求处理阶段，后台限并发 | M docs/ui/image.md:19–24；S native/images.go:35–41 为应保留的预算；[源码/文档] |
| SVG/文字布局 | 复用资源；只在源、宽度、字阶、版本改变时重建 | L desktop/icons.go:18–43、model/markdown.go:404–422 说明缓存边界；GPL，仅机制；[源码→推断] |
| 动画 | 框架 Animation/Transition 或纯绘制时间轴 | M docs/ui/motion.md:67–96；[源码] |
| 流式文本 | UI 发布新快照后更新；只渲染可见项 | M docs/ui/list.md:5–9；[源码→推断] |

List 只减少 row 构建；view 前面的全表格式化、图像读取、长文本重新解析仍会卡。缓存 key 至少反映内容版本、宽度、字体/主题相关参数；有测量再加缓存，勿先堆多个 manager。L 的全文 Markdown 缓存对每个流式中间值都可能新增 entry，这是风险，不是本次测出泄漏。L desktop/model/markdown.go:404–422；[源码→推断]。

媒体预占几何；静态解码结果复用，动态同尺寸 Bitmap.Update 复用纹理。S native/images.go:137–160 为应保留的现状；不把 GIF/APNG 的播放时轴误改成 UI spring。[源码→推断]。

## 先定位慢在何处

启动进程前设 MYGO_FRAME_STATS；开关在包初始化读取。1/true/on 是超过8ms，数字4是超过4ms，all是每帧。日志分 build/pass、layout、paint、present、other，并有 text layouts、进程 allocations/GC；进程统计包含其他 goroutine，不能全算 UI。出处：M ui/framestats.go:13–41、docs/ui/rendering.md:23–39；[源码]。

PowerShell 示例（构建物路径由当前任务指定；重活按项目锁和 run-low.ps1）：

```powershell
$env:MYGO_FRAME_STATS = 'all'
& .\app.exe
```

上段为 shell 用法，变量契约出处同上；[源码→运行配方]。同一场景保持数据、视口、scale、主题和渲染路径一致，比较改前改后 build/layout/paint/present，报告采样数与慢帧；CPU Tester 不当 D3D11 原生呈现证据。M docs/ui/rendering.md:3–21、23–39；[源码→项目验收]。

空闲不主动要帧；一般 UI 不设全局 ticker。焦点光标、spinner、流式过程可继续请求，但过程结束/离屏应停止。框架按 input/Invalidate/After/animation 画帧，Draw 只能绘制；测高回填放构建阶段。M docs/ui/rendering.md:41–43、docs/ui/views.md:182–184；[源码→项目选择]。
