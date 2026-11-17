package main

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"sumika-native/internal/ctlproto"
)

// kernelFaultLine 是一条故障的一行写法；原因、会话是后台交来的文字，打印前由 cleanWriter 再清一遍控制字符
func kernelFaultLine(f ctlproto.KernelFault) string {
	who := "整个内核（所有会话都停止受理）"
	if f.Scope != "kernel" {
		who = "会话 " + f.Conversation + "（角色 " + f.Character + "）"
	}
	at := ""
	if f.At > 0 {
		at = time.UnixMilli(f.At).Format("01-02 15:04:05")
	}
	s := fmt.Sprintf("%s｜%s｜%s｜已重试 %d 次", who, at, f.Reason, f.Tries)
	if f.Detail != "" {
		s += "\n    最近的原始错误：" + f.Detail
	}
	return s
}

// renderKernel 打印 kernel faults、kernel retry 的成功回复。--json 不走这里
func renderKernel(w, stderr io.Writer, req ctlproto.Request, r ctlproto.Reply) {
	if len(req.Args) == 0 {
		return
	}
	switch req.Args[0] {
	case ctlproto.KernelFaultsCmd:
		var v ctlproto.KernelFaults
		if !decode(stderr, r, &v) {
			return
		}
		if v.StrayEnds > 0 {
			defer fmt.Fprintf(w, "另有 %d 条工具或后台任务的退出记录没写进库（不影响受理）；kernel retry 会一起重写\n", v.StrayEnds)
		}
		if len(v.Faults) == 0 {
			fmt.Fprintln(w, "没有故障：内核正常受理。")
			return
		}
		fmt.Fprintf(w, "%d 处故障：\n", len(v.Faults))
		for _, f := range v.Faults {
			fmt.Fprintln(w, "  "+kernelFaultLine(f))
		}
		fmt.Fprintln(w, "解除：sumika-ctl kernel retry [<角色> <会话>]，或重启 Sumika（启动时会核对）")
	case ctlproto.KernelRetryCmd:
		var v ctlproto.KernelRetry
		if !decode(stderr, r, &v) {
			return
		}
		if len(v.Cleared) == 0 {
			fmt.Fprintln(w, "没有要重试的故障。")
			return
		}
		fmt.Fprintf(w, "已恢复 %d 处，重新受理：\n", len(v.Cleared))
		for _, f := range v.Cleared {
			fmt.Fprintln(w, "  "+kernelFaultLine(f))
		}
	}
}

// renderKernelRemaining 在 kernel retry 失败时把还没恢复的那些写到 stderr（失败回复的 Data 里带着）
func renderKernelRemaining(stderr io.Writer, r ctlproto.Reply) {
	var v ctlproto.KernelRetry
	if json.Unmarshal(r.Data, &v) != nil {
		return
	}
	if v.StrayEnds > 0 {
		fmt.Fprintf(stderr, "还有 %d 条工具或后台任务的退出记录没写进库（不影响受理）\n", v.StrayEnds)
	}
	if len(v.Remaining) == 0 {
		return
	}
	fmt.Fprintf(stderr, "还没恢复的 %d 处：\n", len(v.Remaining))
	for _, f := range v.Remaining {
		fmt.Fprintln(stderr, "  "+kernelFaultLine(f))
	}
}
