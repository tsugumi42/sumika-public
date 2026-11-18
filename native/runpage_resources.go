package main

// 运行页的资源标签（规格-运行页 第四、八节）：现在谁在吃 CPU、内存、显存。
// 数据和 sumika-ctl stats resources 同一份（api.StatsReader.Resources）。排法：采样条（间隔、刷新、更新时间）；进程表
// （树状，子进程缩进在父进程下）；主程序内部（Go 运行时）；后台队列。
// 采样只在这个标签在前台时发生：离开标签、切走页面、窗口隐藏都会取消订阅，之后没有定时器，也没有常驻协程（runpage.go 的 runSync）。
// 取不到的数一律写"—"，显存取不到时整列写"不可用"和原因，不写 0；没接入的队列写"未接入"。

import (
	"fmt"
	"strings"

	"github.com/ELY-Open-Source/CrychicUI/display"
	"github.com/egoist/mygo/ui"

	"sumika-native/internal/ctlproto"
	"sumika-native/internal/loadui"
)

// resourceForgetter 是读服务的可选能力：离开资源标签时丢掉采样的前值、放掉性能计数器句柄。
// 不放进 api.StatsReader：命令行那边没有"离开"这回事
type resourceForgetter interface{ ForgetResources() }

// resourceState 是资源标签两张表的展开状态
type resourceState struct {
	proc, queue ui.OutlineState[string]
	seen        map[int]bool // 已经见过的进程：新出现的有子进程的默认展开，用户收起的不再被刷新弄开
}

// categoryLabel 是进程类别的中文名
func categoryLabel(cat string) string {
	switch cat {
	case ctlproto.ProcMain:
		return "主程序"
	case ctlproto.ProcModels:
		return "本地模型"
	case ctlproto.ProcAnalyzer:
		return "命令分析"
	case ctlproto.ProcMCP:
		return "MCP 服务"
	case ctlproto.ProcShell:
		return "运行中的命令"
	case ctlproto.ProcMedia:
		return "媒体工具"
	case ctlproto.ProcUnregistered:
		return "未登记的子进程"
	}
	return cat
}

// queueStateLabel 是队列状态的中文名；没有判断依据时是空串，界面写"—"
func queueStateLabel(s string) string {
	switch s {
	case ctlproto.QueueIdle:
		return "空闲"
	case ctlproto.QueueBusy:
		return "忙"
	case ctlproto.QueueJammed:
		return "堵"
	}
	return ""
}

// fmtCPU：CPU 百分比。nil 是没有前值或没读到，写横线，不写 0
func fmtCPU(v *float64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", *v)
}

// fmtBytesPtr：字节数。nil 写横线
func fmtBytesPtr(v *int64) string {
	if v == nil {
		return "—"
	}
	return fmtBytes(*v)
}

// fmtCountPtr：次数、个数。nil 写横线
func fmtCountPtr(v *int64) string {
	if v == nil {
		return "—"
	}
	return fmtCount(*v)
}

// fmtNs：纳秒写成微秒或毫秒
func fmtNs(ns int64) string {
	if ns < 1_000_000 {
		return fmt.Sprintf("%d 微秒", ns/1000)
	}
	return fmt.Sprintf("%.1f 毫秒", float64(ns)/1e6)
}

func procKey(pid int) string { return fmt.Sprint(pid) }

// runFetchResources 采一次（界面线程发起，后台线程采，结果回界面线程）。和别的标签一样：有旧数据时不显示加载，读失败留旧的
func (a *app) runFetchResources() {
	p := &a.run
	if p.src == nil {
		return
	}
	p.res.req++
	req, gen, src, ctx := p.res.req, p.gen, p.src, a.runLife()
	p.res.loading = true
	p.res.load.Begin()
	go func() {
		rep, err := src.Resources(ctx)
		a.post(func() {
			if p.gen != gen || p.res.req != req {
				return
			}
			p.res.loading = false
			if err != nil {
				p.res.err = err
				p.res.load.Fail()
				return
			}
			p.res.data, p.res.has, p.res.err, p.res.at = rep, true, nil, p.clock()
			p.res.ver++
			p.res.load.Succeed()
		})
	}()
}

func (a *app) resourcesView(c *ui.Context) {
	t := c.Theme()
	p := &a.run
	b := &p.res
	v := b.load.View(c.Now())
	loadui.Wake(c, v)
	switch {
	case p.src == nil:
		a.runUnavailable(c, "后台还没起来", "资源要等后台打开以后才能看。状态标签里写着原因。")
		return
	case !b.has && b.unavailable():
		a.runUnavailable(c, "资源暂时看不了", b.err.Error())
		return
	case !b.has && b.err != nil:
		if a.errorState(c, "没取到资源", b.err.Error(), "重试") {
			a.runFetchResources()
		}
		return
	case !b.has || v.Skeleton:
		a.runSkeleton(c, v.Skeleton, true, 4)
		return
	}
	rep := b.data
	ui.Scroll(c).Grow(1).Children(func() {
		col := ui.Column(c).Padding(t.Space(4), 0).Gap(t.Space(4)).MaxWidth(1280)
		narrow := false
		if w := col.LayoutBounds().W; w > 0 && w < runWide {
			narrow = true // 内容区窄：主程序内部的键值叠成一列
		}
		col.Children(func() {
			if b.stale() {
				a.staleNote(c, b.err.Error(), b.at)
			}
			a.resourcesToolbar(c)
			a.resourcesProcesses(c, rep)
			a.resourcesRuntime(c, rep.Runtime, narrow)
			a.resourcesQueues(c, rep.Queues)
			ui.Text(c, "各模块共用一个堆，所以不按模块拆；“其他”是主程序占用减去 Go 运行时的部分（SQLite、图片、界面缓冲、系统库）。").TextColor(t.TextMuted).Wrap()
		})
	})
}

// resourcesToolbar：采样间隔（1 / 2 / 5 秒、暂停）、刷新、更新时间。通用的刷新和间隔选择不写提示
func (a *app) resourcesToolbar(c *ui.Context) {
	t := c.Theme()
	p := &a.run
	ui.Row(c).Wrap().Gap(t.Space(3)).GapY(t.Space(2)).AlignItems(ui.Center).Children(func() {
		ui.Row(c.Key("res-every-row")).MaxWidth(280).Children(func() {
			pick := p.resIdx()
			old := pick
			a.segmentedChoice(c, "res-every", "采样间隔", runResLabels, &pick)
			if pick != old {
				p.resPick = pick + 1
				a.runStopKeep(true) // 间隔变了：换一份订阅，定时器按新间隔起；采样器前值要留着
				a.runSync()
			}
		})
		if a.ghostButton(c, "刷新") {
			a.runFetchResources()
		}
		switch {
		case p.res.load.View(c.Now()).Refreshing:
			mutedText(c, "刷新中…")
		case p.resIdx() == len(runResIntervals)-1 && !p.res.at.IsZero():
			mutedText(c, "已暂停，最后一次是 "+p.res.at.Format("15:04:05"))
		case !p.res.at.IsZero():
			mutedText(c, "更新于 "+p.res.at.Format("15:04:05"))
		}
	})
}

// resourcesProcesses 是进程表：主程序在最上面，子进程缩进在它下面
func (a *app) resourcesProcesses(c *ui.Context, rep ctlproto.ResourcesReport) {
	st := &a.run.resTbl
	if st.seen == nil {
		st.seen = map[int]bool{}
	}
	rows := map[string]ctlproto.ProcessRow{}
	kids := map[string][]string{}
	var roots []string
	has := map[int]bool{}
	for _, r := range rep.Processes {
		has[r.Parent] = true
	}
	for _, r := range rep.Processes {
		k := procKey(r.PID)
		rows[k] = r
		if r.Depth == 0 {
			roots = append(roots, k)
		} else {
			pk := procKey(r.Parent)
			kids[pk] = append(kids[pk], k)
		}
		if has[r.PID] && !st.seen[r.PID] { // 第一次见到有子进程的：默认展开
			st.proc.Open.Add(k)
		}
		st.seen[r.PID] = true
	}
	memTitle := "内存"
	if rep.MemoryKind == ctlproto.MemWorkingSet {
		memTitle = "工作集" // 系统没给专用工作集：列名跟着换，不静默换口径
	}
	cols := []ui.TableColumn{
		{Title: "名称", ID: "name", MinWidth: 200},
		{Title: "类别", ID: "cat", Width: 130},
		{Title: "CPU", ID: "cpu", Width: 80, Align: ui.End},
		{Title: memTitle, ID: "mem", Width: 100, Align: ui.End},
		{Title: "提交", ID: "commit", Width: 100, Align: ui.End},
		{Title: "GPU", ID: "gpu", Width: 80, Align: ui.End},
		{Title: "显存", ID: "gpumem", Width: 100, Align: ui.End},
		{Title: "PID", ID: "pid", Width: 70, Align: ui.End},
	}
	gpuOK := rep.GPU.Available
	gpuWait := rep.GPU.Pending // 性能计数器还在准备：两列写"—"，不写"不可用"
	reason := rep.GPU.Reason
	if reason == "" {
		reason = "没有采样"
	}
	qualityTable(c, "res-procs", "进程", &st.proc, cols, roots, func(k string) []string { return kids[k] }, func(k string, col int) {
		r := rows[k]
		switch col {
		case 0:
			name := r.Name
			if r.Label != "" {
				name += " · " + r.Label
			}
			ui.Text(c, name).SingleLine().Tooltip(name)
		case 1:
			ui.Text(c, categoryLabel(r.Category)).SingleLine()
		case 2:
			numText(c, fmtCPU(r.CPU))
		case 3:
			numText(c, fmtBytesPtr(r.Memory))
		case 4:
			numText(c, fmtBytesPtr(r.Commit))
		case 5:
			if gpuWait {
				mutedText(c, "—")
			} else if !gpuOK {
				mutedText(c, "不可用").Tooltip(reason)
			} else if r.GPU == nil {
				mutedText(c, "—")
			} else {
				numText(c, fmtCPU(r.GPU))
			}
		case 6:
			if gpuWait {
				mutedText(c, "—")
			} else if !gpuOK {
				mutedText(c, "不可用").Tooltip(reason)
			} else {
				numText(c, fmtBytesPtr(r.GPUMemory))
			}
		case 7:
			numText(c, fmt.Sprint(r.PID))
		}
	})
	if !gpuOK && !gpuWait {
		ui.Text(c, "显存和 GPU 不可用："+reason).TextColor(c.Theme().TextMuted).Wrap()
	}
}

// resourcesRuntime 是主程序内部：两列键值
func (a *app) resourcesRuntime(c *ui.Context, rs ctlproto.RuntimeStats, narrow bool) {
	t := c.Theme()
	limit := "没设"
	if rs.MemLimit != nil {
		limit = fmtBytes(*rs.MemLimit)
	}
	gcCPU, pause, other := "—", "—", "—"
	if rs.GCCPU != nil {
		gcCPU = fmt.Sprintf("%.2f%%", *rs.GCCPU*100)
	}
	if rs.PauseP99Ns != nil {
		pause = fmtNs(*rs.PauseP99Ns)
	}
	if rs.Other != nil {
		other = fmtBytes(*rs.Other)
	}
	items := []display.KeyValuePair{
		{Key: "运行时总量", Value: fmtBytes(rs.Total)},
		{Key: "堆对象", Value: fmtBytes(rs.HeapObj)},
		{Key: "栈", Value: fmtBytes(rs.Stacks)},
		{Key: "GC 后存活的堆", Value: fmtBytes(rs.HeapLive)},
		{Key: "内存上限", Value: limit},
		{Key: "协程数", Value: fmtCount(rs.Goroutines)},
		{Key: "GC 次数", Value: fmtCount(rs.GCCycles)},
		{Key: "GC 占 CPU", Value: gcCPU},
		{Key: "GC 暂停 p99", Value: pause},
		{Key: "其他", Value: other},
	}
	ui.Column(c.Key("res-runtime")).FillWidth().Gap(t.Space(2)).Children(func() {
		ui.Text(c, "主程序内部").Bold()
		if narrow {
			a.keyValues(c, "res-runtime-kv", items)
			return
		}
		// 宽窗：两列键值（规格第八节的线框），左边是内存，右边是协程和 GC
		half := (len(items) + 1) / 2
		ui.Row(c).FillWidth().Gap(t.Space(4)).AlignItems(ui.Start).Children(func() {
			ui.Column(c.Key("res-runtime-left")).Grow(1).MinWidth(0).Children(func() { a.keyValues(c, "res-runtime-kv-l", items[:half]) })
			ui.Column(c.Key("res-runtime-right")).Grow(1).MinWidth(0).Children(func() { a.keyValues(c, "res-runtime-kv-r", items[half:]) })
		})
	})
}

// resourcesQueues 是后台队列：没接入的行写"未接入"，读失败的行写原因，没有的计数写"—"，都不写 0
func (a *app) resourcesQueues(c *ui.Context, queues []ctlproto.QueueRow) {
	st := &a.run.resTbl
	rows := map[string]ctlproto.QueueRow{}
	var roots []string
	for _, q := range queues {
		rows[q.Key] = q
		roots = append(roots, q.Key)
	}
	cols := []ui.TableColumn{
		{Title: "队列", ID: "name", MinWidth: 160},
		{Title: "积压", ID: "backlog", Width: 90, Align: ui.End},
		{Title: "在跑", ID: "running", Width: 80, Align: ui.End},
		{Title: "容量", ID: "cap", Width: 80, Align: ui.End},
		{Title: "最久等待", ID: "wait", Width: 100, Align: ui.End},
		{Title: "状态", ID: "state", Width: 80},
	}
	qualityTable(c, "res-queues", "后台队列", &st.queue, cols, roots, noKids, func(k string, col int) {
		t := c.Theme()
		q := rows[k]
		switch {
		case !q.Wired:
			switch col {
			case 0:
				ui.Text(c, q.Label).SingleLine().TextColor(t.TextMuted)
			case 1:
				mutedText(c, "未接入")
			}
			return
		case q.Err != "":
			switch col {
			case 0:
				ui.Text(c, q.Label).SingleLine()
			case 1:
				ui.Text(c, strings.TrimSpace(q.Err)).SingleLine().TextColor(t.Danger).Tooltip(q.Err)
			}
			return
		}
		switch col {
		case 0:
			e := ui.Text(c, q.Label).SingleLine()
			if q.Note != "" {
				e.Tooltip(q.Note)
			}
		case 1:
			numText(c, fmtCountPtr(q.Backlog))
		case 2:
			numText(c, fmtCountPtr(q.Running))
		case 3:
			numText(c, fmtCountPtr(q.Capacity))
		case 4:
			if q.OldestWaitMs == nil {
				numText(c, "—")
			} else {
				numText(c, fmtMS(*q.OldestWaitMs))
			}
		case 5:
			label := queueStateLabel(q.State)
			switch {
			case label == "":
				mutedText(c, "—")
			case q.State == ctlproto.QueueJammed:
				ui.Text(c, label).SingleLine().TextColor(t.Danger)
			case q.State == ctlproto.QueueIdle:
				mutedText(c, label)
			default:
				ui.Text(c, label).SingleLine()
			}
		}
	})
}
