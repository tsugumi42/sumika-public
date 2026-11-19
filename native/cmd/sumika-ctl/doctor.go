package main

// doctor 的给人看的输出（规格-环境自检 设计第 4 条）：一行一项“状态 名字：详情”，不是正常的下一行写下一步，
// 子项缩进列在项下面，最后一行汇总。--json 不走这里，原样输出快照。

import (
	"encoding/json"
	"fmt"
	"io"

	"sumika-native/internal/ctlproto"
)

// envStateName 是清单状态的固定说法：和界面、规格里的词一致，不把 unknown 说成正常。
func envStateName(s string) string {
	switch s {
	case "ok":
		return "正常"
	case "warn":
		return "注意"
	case "bad":
		return "有问题"
	case "na":
		return "不适用"
	case "unknown":
		return "不知道"
	}
	return s
}

func renderDoctor(w io.Writer, d ctlproto.DoctorSnapshot) {
	var bad, warn int
	for _, it := range d.Items {
		fmt.Fprintf(w, "%s %s：%s\n", envStateName(it.State), it.Name, it.Detail)
		if it.State != "ok" && it.Next != "" {
			fmt.Fprintf(w, "  下一步：%s\n", it.Next)
		}
		for _, sub := range it.Sub {
			fmt.Fprintf(w, "  %s %s：%s\n", envStateName(sub.State), sub.Name, sub.Detail)
			if sub.State != "ok" && sub.Next != "" {
				fmt.Fprintf(w, "    下一步：%s\n", sub.Next)
			}
		}
		switch it.State {
		case "bad":
			bad++
		case "warn":
			warn++
		}
	}
	fmt.Fprintf(w, "%d 项有问题、%d 项要注意\n", bad, warn)
}

// doctorExitCode 是 doctor 的退出码：回复成功时看快照里有没有“有问题”，有就 1；只有“注意”仍是 0。
func doctorExitCode(r ctlproto.Reply) int {
	var d ctlproto.DoctorSnapshot
	if err := json.Unmarshal(r.Data, &d); err != nil {
		return ctlproto.ExitFailed
	}
	if ctlproto.DoctorHasBad(d) {
		return ctlproto.ExitFailed
	}
	return ctlproto.ExitOK
}
