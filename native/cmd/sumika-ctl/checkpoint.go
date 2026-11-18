package main

import (
	"fmt"
	"io"

	"sumika-native/internal/ctlproto"
)

func renderCheckpoint(stdout, stderr io.Writer, req ctlproto.Request, r ctlproto.Reply) {
	if len(req.Args) == 0 {
		return
	}
	switch req.Args[0] {
	case "list":
		var v ctlproto.CheckpointList
		if !decode(stderr, r, &v) {
			return
		}
		if len(v.Items) == 0 {
			fmt.Fprintln(stdout, "这个会话没有检查点。")
			return
		}
		for _, c := range v.Items {
			child := ""
			if c.Child {
				child = " · 子任务"
			}
			if c.State == "skipped" {
				fmt.Fprintf(stdout, "%s  未拍快照 · %s%s · %s，不能回退\n", c.ID, c.Tool, child, c.Note)
				continue
			}
			fmt.Fprintf(stdout, "%s  %s · %s%s · %d 文件 / %d 字节\n", c.ID, c.State, c.Tool, child, c.Files, c.Bytes)
		}
	case "preview":
		var v ctlproto.CheckpointPlan
		if !decode(stderr, r, &v) {
			return
		}
		fmt.Fprintf(stdout, "检查点 %s（%s）\n工作目录：%s\n版本：%s\n", v.ID, v.Mode, v.Workspace, v.Version)
		for _, f := range v.Files {
			fmt.Fprintf(stdout, "%s  当前 %t/%d 字节 → 目标 %t/%d 字节\n", f.Path, f.CurrentExists, f.CurrentBytes, f.TargetExists, f.TargetBytes)
		}
		fmt.Fprintf(stdout, "恢复需要在主窗口同会话单次批准：checkpoint restore %s %s %s %s\n", req.Args[1], v.ID, v.Version, v.Mode)
	case "restore":
		var v ctlproto.CheckpointRestore
		if !decode(stderr, r, &v) {
			return
		}
		if !r.OK && v.InverseID == "" {
			return
		}
		fmt.Fprintf(stdout, "恢复状态：%s\n目标：%s\n逆检查点：%s\n", v.State, v.TargetID, v.InverseID)
		for _, path := range v.Completed {
			fmt.Fprintf(stdout, "已恢复：%s\n", path)
		}
		for _, path := range v.Remaining {
			fmt.Fprintf(stdout, "未完成：%s\n", path)
		}
	case "status", "cancel":
		var v ctlproto.CheckpointProgress
		if !decode(stderr, r, &v) {
			return
		}
		fmt.Fprintf(stdout, "恢复状态：%s\n已恢复 %d / 共 %d\n目标：%s\n逆检查点：%s\n", v.State, v.Done, v.Total, v.TargetID, v.InverseID)
		if v.Error != "" {
			fmt.Fprintf(stdout, "操作错误：%s\n", v.Error)
		}
		if v.InverseID != "" && !v.OutcomeSaved && (v.State == "interrupted" || v.State == "failed") {
			fmt.Fprintln(stdout, "恢复结局尚未确认保存，请重新预览 recovery 或 resume。")
		}
		for _, path := range v.Completed {
			fmt.Fprintf(stdout, "已恢复：%s\n", path)
		}
		for _, path := range v.Remaining {
			fmt.Fprintf(stdout, "未完成：%s\n", path)
		}
	}
}
