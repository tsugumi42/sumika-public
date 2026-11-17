package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"sumika-native/internal/ctlproto"
)

// renderResources 把 stats resources 的回复写成给人看的中文。--json 不走这里。
// 取不到的数写“—”，显存取不到整列写“不可用”和原因，没接入的队列写“未接入”，都不写 0
func renderResources(w, stderr io.Writer, r ctlproto.Reply) {
	var rep ctlproto.ResourcesReport
	if !decode(stderr, r, &rep) {
		return
	}
	memName := "内存（专用工作集）"
	if rep.MemoryKind == ctlproto.MemWorkingSet {
		memName = "工作集（系统没给专用工作集）"
	}
	cpuNote := "CPU 没有前一次采样，写“—”"
	if rep.IntervalMS > 0 {
		cpuNote = "CPU 按 " + durText(rep.IntervalMS) + " 的间隔算"
	}
	fmt.Fprintf(w, "资源（%s 采样，%d 个逻辑核；%s；%s）\n",
		time.UnixMilli(rep.SampledAtMS).Local().Format("15:04:05"), rep.Cores, cpuNote, memName)
	switch {
	case rep.GPU.Pending:
		fmt.Fprintln(w, "显存和 GPU：性能计数器还在准备")
	case !rep.GPU.Available:
		fmt.Fprintf(w, "显存和 GPU：不可用（%s）\n", rep.GPU.Reason)
	}
	fmt.Fprintln(w, "进程：")
	for _, p := range rep.Processes {
		name := p.Name
		if p.Label != "" {
			name += " · " + p.Label
		}
		line := fmt.Sprintf("%s%s  %s  CPU %s  内存 %s  提交 %s", strings.Repeat("  ", p.Depth+1), name, categoryText(p.Category), cpuText(p.CPU), bytesPtr(p.Memory), bytesPtr(p.Commit))
		if rep.GPU.Available {
			line += fmt.Sprintf("  GPU %s  显存 %s", cpuText(p.GPU), bytesPtr(p.GPUMemory))
		}
		fmt.Fprintf(w, "%s  PID %d\n", line, p.PID)
	}
	rt := rep.Runtime
	fmt.Fprintln(w, "主程序内部：")
	fmt.Fprintf(w, "  运行时总量 %s，堆对象 %s，栈 %s，GC 后存活的堆 %s，内存上限 %s\n", bytesText(rt.Total), bytesText(rt.HeapObj), bytesText(rt.Stacks), bytesText(rt.HeapLive), limitText(rt.MemLimit))
	gc := "—"
	if rt.GCCPU != nil {
		gc = fmt.Sprintf("%.2f%%", *rt.GCCPU*100)
	}
	pause := "—"
	if rt.PauseP99Ns != nil {
		pause = fmt.Sprintf("%.1f 毫秒", float64(*rt.PauseP99Ns)/1e6)
	}
	fmt.Fprintf(w, "  协程 %s，GC %s 次，GC 占 CPU %s，暂停 p99 %s，其他 %s（主程序占用减运行时总量）\n", num(rt.Goroutines), num(rt.GCCycles), gc, pause, bytesPtr(rt.Other))
	fmt.Fprintln(w, "后台队列：")
	for _, q := range rep.Queues {
		switch {
		case !q.Wired:
			fmt.Fprintf(w, "  %s  未接入\n", q.Label)
		case q.Err != "":
			fmt.Fprintf(w, "  %s  %s\n", q.Label, q.Err)
		default:
			wait := "—"
			if q.OldestWaitMs != nil {
				wait = durText(*q.OldestWaitMs)
			}
			fmt.Fprintf(w, "  %s  积压 %s  在跑 %s  容量 %s  最久等待 %s  %s\n", q.Label, countPtr(q.Backlog), countPtr(q.Running), countPtr(q.Capacity), wait, stateText(q.State))
		}
	}
}

func categoryText(c string) string {
	switch c {
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
	return c
}

func stateText(s string) string {
	switch s {
	case ctlproto.QueueIdle:
		return "空闲"
	case ctlproto.QueueBusy:
		return "忙"
	case ctlproto.QueueJammed:
		return "堵"
	}
	return "—"
}

func cpuText(v *float64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", *v)
}

func bytesPtr(v *int64) string {
	if v == nil {
		return "—"
	}
	return bytesText(*v)
}

func countPtr(v *int64) string {
	if v == nil {
		return "—"
	}
	return num(*v)
}

func limitText(v *int64) string {
	if v == nil {
		return "没设"
	}
	return bytesText(*v)
}
