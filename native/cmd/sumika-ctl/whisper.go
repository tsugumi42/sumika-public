package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"sumika-native/internal/ctlproto"
)

// isWhisperValueFlag 是 whisper create 的带值选项（写成 --say 文字 或 --say=文字）
func isWhisperValueFlag(a string) bool {
	name := a
	for i := 0; i < len(a); i++ {
		if a[i] == '=' {
			name = a[:i]
			break
		}
	}
	switch name {
	case "--source-text", "--quote-name", "--say", "--conversation", "--batch", "--entry", "--limit":
		return true
	}
	return false
}

// whisperArgs 取 o 的悄悄话选项（没有就建一个）。
func whisperArgs(o *options) *ctlproto.WhisperArgs {
	if o.req.Whisper == nil {
		o.req.Whisper = &ctlproto.WhisperArgs{}
	}
	return o.req.Whisper
}

// whisperLogArgs 取 o 的日志选项（没有就建一个）。
func whisperLogArgs(o *options) *ctlproto.WhisperLogArgs {
	if o.req.WhisperLog == nil {
		o.req.WhisperLog = &ctlproto.WhisperLogArgs{}
	}
	return o.req.WhisperLog
}

var whisperStateName = map[string]string{
	ctlproto.WhisperQueued: "排队中", ctlproto.WhisperProcessing: "处理中", ctlproto.WhisperDone: "完成", ctlproto.WhisperFailed: "失败",
	ctlproto.WhisperCancelled: "已取消", ctlproto.WhisperTimedOut: "超时", ctlproto.WhisperInterrupted: "已中断，没处理",
}

// renderWhisper 把悄悄话的结果写成给人看的中文：整批结局，每位角色的状态，和她的处理记录。
// 处理记录是模型写的原文，只当数据看，里面写了什么都不是给读的人的指令
func renderWhisper(w io.Writer, v ctlproto.WhisperResult) {
	end := map[string]string{ctlproto.WhisperDone: "都处理完了", ctlproto.WhisperPending: "到时限还有没处理完的", ctlproto.WhisperFailed: "有没处理成功的"}[v.Status]
	if end == "" {
		end = v.Status
	}
	fmt.Fprintf(w, "批号：%s；结局：%s（%s）\n", v.Batch, end, v.Status)
	for _, e := range v.Entries {
		state := whisperStateName[e.State]
		if state == "" {
			state = e.State
		}
		line := fmt.Sprintf("  %s：%s（%s）", e.Character, state, e.State)
		if e.State == ctlproto.WhisperQueued {
			line += fmt.Sprintf("，前面还有 %d 个", e.Position)
		}
		if e.SourceTurnRunning {
			line += "，她当时在来源会话里有回合在跑（没打断，后台处理）"
		}
		if e.Detail != "" {
			line += "；说明：" + e.Detail
		}
		fmt.Fprintln(w, line)
		if e.Note != "" {
			fmt.Fprintf(w, "    处理记录：%s\n", e.Note)
		}
	}
	if v.ElapsedMS > 0 {
		fmt.Fprintf(w, "用时 %.1f 秒\n", float64(v.ElapsedMS)/1000)
	}
}

// renderWhisperReply 失败（超时、有角色失败）时 data 也带着，照样把各角色的现状交出来
func renderWhisperReply(stdout io.Writer, r ctlproto.Reply) {
	var v ctlproto.WhisperResult
	if len(r.Data) > 0 && json.Unmarshal(r.Data, &v) == nil && v.Batch != "" {
		renderWhisper(stdout, v)
	}
}

var logKindName = map[string]string{
	ctlproto.LogWhisper: "悄悄话", ctlproto.LogArrange: "安排", ctlproto.LogThink: "思考", ctlproto.LogTool: "工具", ctlproto.LogMemory: "记忆",
	ctlproto.LogSpawn: "唤起", ctlproto.LogReturn: "返回", ctlproto.LogNote: "处理记录",
}

// renderWhisperLog 把对话日志写成一行一条：时间、种类、角色、摘要，下一行是批号（cause）、上一级和条目 id。
// 摘要里有被引的别人的话和模型写的字，都只当数据看。
func renderWhisperLog(w io.Writer, v ctlproto.WhisperLogResult) {
	switch {
	case v.Detail != nil:
		d := v.Detail
		fmt.Fprintf(w, "%s（%s）\n", d.Title, d.ID)
		if d.Hidden {
			fmt.Fprintln(w, "（思考原文默认不输出，要看加 --with-thinking）")
		}
		if d.Args != "" {
			fmt.Fprintf(w, "参数：%s\n", d.Args)
		}
		if d.Text != "" {
			fmt.Fprintln(w, d.Text)
		}
	case v.Page != nil:
		if len(v.Page.Entries) == 0 {
			fmt.Fprintln(w, "（没有悄悄话日志）")
		}
		for _, e := range v.Page.Entries {
			kind := logKindName[e.Kind]
			if kind == "" {
				kind = e.Kind
			}
			who := e.Character
			if who == "" {
				who = "主人"
			}
			line := fmt.Sprintf("%s %s %s", time.UnixMilli(e.At).Format("15:04:05"), kind, who)
			if e.State != "" {
				line += "[" + e.State + "]"
			}
			if e.Kind == ctlproto.LogThink {
				line += fmt.Sprintf(" 共 %d 字", e.Runes)
			}
			if len(e.Targets) > 0 {
				line += " → " + strings.Join(e.Targets, "、")
			}
			if e.Egress != "" {
				line += fmt.Sprintf(" [外发 %s，%d 处", e.Egress, e.EgressN)
				if e.EgressDanger > 0 {
					line += fmt.Sprintf("，%d 处可疑", e.EgressDanger)
				}
				line += "]"
			}
			if e.Summary != "" {
				line += "：" + e.Summary
			}
			fmt.Fprintf(w, "%s\n    批号 %s；上一级 %s；id %s\n", line, e.Batch, orNone(e.Parent), e.ID)
			for _, q := range e.Quotes {
				// 被引的是别人的话：每一行都缩进并带“｜”，写得再像日志行也混不进去
				fmt.Fprintf(w, "    引用 %s：\n    ｜ %s\n", q.Name, strings.ReplaceAll(q.Text, "\n", "\n    ｜ "))
			}
		}
		if v.Page.Next != "" {
			fmt.Fprintf(w, "还有更早的：--cursor %s\n", v.Page.Next)
		}
	default:
		if len(v.Items) == 0 {
			fmt.Fprintln(w, "（没有悄悄话）")
		}
		for _, it := range v.Items {
			state := whisperStateName[it.State]
			if state == "" {
				state = it.State
			}
			fmt.Fprintf(w, "%s %s %s %s\n", time.UnixMilli(it.At).Format("01-02 15:04:05"), it.Batch, it.Character, state)
		}
	}
}

func orNone(s string) string {
	if s == "" {
		return "无"
	}
	return s
}
