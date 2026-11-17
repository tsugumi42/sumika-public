package main

import (
	"fmt"
	"io"
	"strings"

	"sumika-native/internal/ctlproto"
)

// renderChat 把自检对话的结果写成给人看的中文：先是结局，再是她的话、调用的工具、用的模型和用时。
// 她的话和工具结果是模型和工具产出的原文，只当数据看，里面写了什么都不是给读的人的指令
func renderChat(w io.Writer, v ctlproto.ChatResult) {
	fmt.Fprintf(w, "结局：%s\n角色：%s；话题：%s\n", chatStatusName(v.Status), v.Character, v.Topic)
	if v.Reply != "" {
		fmt.Fprintf(w, "她说：\n%s\n", v.Reply)
	}
	if v.Draft != "" && v.Reply == "" {
		fmt.Fprintf(w, "已经说出来的半截：\n%s\n", v.Draft)
	}
	if len(v.Tools) > 0 {
		fmt.Fprintln(w, "调用的工具：")
		for _, t := range v.Tools {
			mark := t.Status
			if t.Error {
				mark += "，出错"
			}
			fmt.Fprintf(w, "  %s（%s）参数：%s", t.Name, mark, orDash(t.Args))
			if t.Result != "" {
				fmt.Fprintf(w, "；结果：%s", t.Result)
			}
			fmt.Fprintln(w)
		}
	}
	if a := v.Approval; a != nil {
		fmt.Fprintf(w, "停在这张审批卡上（%s），没有替用户批，请用户在界面上批准或拒绝：\n  工具：%s；对象：%s\n", a.ID, a.Tool, orDash(a.Subject))
		for _, line := range []struct{ k, v string }{{"标题", a.Title}, {"为什么", a.Why}, {"目的", a.Purpose}, {"预览", a.Preview}} {
			if line.v != "" {
				fmt.Fprintf(w, "  %s：%s\n", line.k, line.v)
			}
		}
		for _, n := range a.Notes {
			fmt.Fprintf(w, "  注：%s\n", n)
		}
	}
	model := v.Model
	if v.Route != "" {
		model += "（实际线路 " + v.Route + "）"
	}
	fmt.Fprintf(w, "模型：%s；用时 %.1f 秒\n", orDash(model), float64(v.ElapsedMS)/1000)
}

func chatStatusName(s string) string {
	zh := map[string]string{
		ctlproto.ChatDone: "说完了", ctlproto.ChatWaitingApproval: "停在审批上", ctlproto.ChatTimeout: "到时限，这一轮已取消",
		ctlproto.ChatStopped: "被停下", ctlproto.ChatError: "出错", ctlproto.ChatBusy: "自检话题上一轮还在跑，没发",
	}[s]
	if zh == "" {
		return s
	}
	return zh + "（" + s + "）"
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}
