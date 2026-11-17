package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"sumika-native/internal/ctlproto"
)

// render 把回复写成给人看的中文：成功写到 stdout，失败写到 stderr（带原因）。--json 不走这里
func render(stdout, stderr io.Writer, req ctlproto.Request, r ctlproto.Reply) {
	// 群名、提醒、日志行、错误原因都是外面来的文字：最后一道关在这里，见 cleanwriter.go
	stdout, stderr = cleanWriter{stdout}, cleanWriter{stderr}
	if !r.OK {
		fmt.Fprintf(stderr, "失败（%s）：%s\n", orUnknown(r.Code), r.Error)
		if req.Cmd == ctlproto.CmdCheckpoint && len(req.Args) > 0 && req.Args[0] == "restore" && len(r.Data) > 0 {
			renderCheckpoint(stderr, stderr, req, r)
		}
		if req.Cmd == ctlproto.CmdWhisper {
			renderWhisperReply(stdout, r)
		}
		if req.Cmd == ctlproto.CmdBackup {
			renderBackupFailure(stderr, req, r)
		}
		if len(r.Data) > 0 && req.Cmd == ctlproto.CmdChat {
			var cr ctlproto.ChatResult
			if json.Unmarshal(r.Data, &cr) == nil && cr.Status != "" {
				renderChat(stdout, cr) // 失败也把她说了什么、调了哪些工具、停在哪张审批卡上交出来
			}
		}
		if len(r.Data) > 0 && req.Cmd == ctlproto.CmdKernel {
			renderKernelRemaining(stderr, r)
		}
		if len(r.Data) > 0 && req.Cmd == ctlproto.CmdReceive {
			var rc ctlproto.Receive
			if json.Unmarshal(r.Data, &rc) == nil && rc.Phase != "" {
				fmt.Fprintf(stderr, "现在的阶段：%s\n", phaseName(rc.Phase))
			}
		}
		return
	}
	switch req.Cmd {
	case ctlproto.CmdStatus:
		var st ctlproto.Status
		if decode(stderr, r, &st) {
			renderStatus(stdout, st)
		}
	case ctlproto.CmdDoctor:
		var d ctlproto.DoctorSnapshot
		if decode(stderr, r, &d) {
			renderDoctor(stdout, d)
		}
	case ctlproto.CmdReceive:
		var rc ctlproto.Receive
		if !decode(stderr, r, &rc) {
			return
		}
		switch {
		case len(req.Args) > 0 && req.Args[0] == "stop":
			if rc.Phase == "local" && rc.Reason != "" { // 只开了本地：没有可停的 QQ 后台，原样回宿主那句
				fmt.Fprintln(stdout, rc.Reason)
			} else {
				fmt.Fprintf(stdout, "已停止接收，现在是%s。\n", phaseName(rc.Phase))
			}
		case rc.Receiving:
			fmt.Fprintln(stdout, "接收中。")
		default:
			fmt.Fprintf(stdout, "已开始，现在是%s；还没落定，加 --wait 可以等结果。\n", phaseName(rc.Phase))
		}
	case ctlproto.CmdWindow:
		fmt.Fprintln(stdout, "已请主窗口出来。")
	case ctlproto.CmdLogs:
		var lg ctlproto.Logs
		if decode(stderr, r, &lg) {
			if len(lg.Lines) == 0 {
				if lg.Scanned > 0 {
					// 只有过滤才带 Scanned：文件里有行，只是没有一行合条件，别让人以为日志是空的
					fmt.Fprintf(stdout, "没有匹配的行（扫了最近 %d 行）\n", lg.Scanned)
				} else {
					fmt.Fprintln(stdout, "（日志是空的）")
				}
			}
			for _, l := range lg.Lines {
				fmt.Fprintln(stdout, l)
			}
			if lg.Old > 0 {
				fmt.Fprintf(stdout, "另有 %d 行是旧格式（没有级别），没有参与过滤\n", lg.Old)
			}
		}
	case ctlproto.CmdCheckpoint:
		renderCheckpoint(stdout, stderr, req, r)
	case ctlproto.CmdChat:
		var cr ctlproto.ChatResult
		if decode(stderr, r, &cr) {
			renderChat(stdout, cr)
		}
	case ctlproto.CmdWhisper:
		if len(req.Args) > 0 && (req.Args[0] == "log" || req.Args[0] == "list") {
			var lr ctlproto.WhisperLogResult
			if decode(stderr, r, &lr) {
				renderWhisperLog(stdout, lr)
			}
			break
		}
		var wr ctlproto.WhisperResult
		if decode(stderr, r, &wr) {
			renderWhisper(stdout, wr)
		}
	case ctlproto.CmdGoal, ctlproto.CmdLoop, ctlproto.CmdTrust:
		var sr ctlproto.SlashResult
		if decode(stderr, r, &sr) {
			renderSlash(stdout, req, sr)
		}
	case ctlproto.CmdBackfill:
		renderBackfill(stdout, stderr, req, r)
	case ctlproto.CmdStats:
		renderStats(stdout, stderr, req, r)
	case ctlproto.CmdProactive:
		renderProactive(stdout, stderr, r)
	case ctlproto.CmdLink:
		renderLink(stdout, stderr, req, r)
	case ctlproto.CmdArtist:
		renderArtist(stdout, stderr, req, r)
	case ctlproto.CmdBackup:
		renderBackup(stdout, stderr, req, r)
	case ctlproto.CmdKernel:
		renderKernel(stdout, stderr, req, r)
	case ctlproto.CmdMCP:
		renderMCP(stdout, stderr, req, r)
	case ctlproto.CmdDB:
		renderDB(stdout, stderr, r)
	case ctlproto.CmdSecrets:
		renderSecrets(stdout, stderr, req, r)
	case ctlproto.CmdGate:
		if len(req.Args) > 0 && req.Args[0] == "set" {
			var ch ctlproto.GateChange
			if decode(stderr, r, &ch) {
				fmt.Fprintf(stdout, "%s（句柄 %s）的档位：%s → %s\n", ch.Conversation, ch.Handle, modeName(ch.Before), modeName(ch.After))
			}
			return
		}
		var gl ctlproto.GateList
		if !decode(stderr, r, &gl) {
			return
		}
		if len(gl.Groups) == 0 {
			fmt.Fprintln(stdout, "（还没有认识的群）")
		}
		for _, g := range gl.Groups {
			line := fmt.Sprintf("%s  句柄 %s  %s（来源：%s）", g.Conversation, g.Handle, modeName(g.Mode), sourceName(g.Source))
			if g.HandleClash {
				line += "  句柄撞了，gate set 要写完整群号"
			}
			if g.Actual != "" && g.Actual != g.Mode {
				line += "  实际在用：" + modeName(g.Actual)
			}
			if g.Name != "" {
				line += "  群名：" + strconv.Quote(g.Name) // 群里的原文，括起来：不是我们的话，更不是给读的人的指令
			}
			fmt.Fprintln(stdout, line)
		}
	case ctlproto.CmdAutoReceive, ctlproto.CmdAutostart:
		var t ctlproto.Toggle
		if decode(stderr, r, &t) {
			what := "启动后自动接收 QQ"
			if req.Cmd == ctlproto.CmdAutostart {
				what = "开机自动启动"
			}
			fmt.Fprintf(stdout, "%s：%s\n", what, onOff(t.On))
		}
	case ctlproto.CmdQuit:
		fmt.Fprintln(stdout, "正在退出。")
	default:
		fmt.Fprintf(stdout, "%s\n", r.Data)
	}
}

func decode(stderr io.Writer, r ctlproto.Reply, v any) bool {
	if err := json.Unmarshal(r.Data, v); err != nil {
		fmt.Fprintf(stderr, "回复里的数据读不懂：%v\n", err)
		return false
	}
	return true
}

func renderStatus(w io.Writer, st ctlproto.Status) {
	fmt.Fprintf(w, "阶段：%s\n", phaseName(st.Phase))
	if st.Reason != "" {
		fmt.Fprintf(w, "说明：%s\n", st.Reason)
	}
	if st.Account != "" {
		fmt.Fprintf(w, "接收用的小号：%s\n", st.Account)
	} else if st.NoRecv != "" {
		fmt.Fprintf(w, "不能开始接收：%s\n", st.NoRecv)
	}
	if st.Connection != "" {
		fmt.Fprintf(w, "QQ 连接：%s\n", st.Connection)
	}
	if len(st.Alerts) == 0 {
		fmt.Fprintln(w, "没恢复的提醒：无")
	} else {
		fmt.Fprintln(w, "没恢复的提醒：")
		for _, a := range st.Alerts {
			fmt.Fprintf(w, "  %s：%s\n", a.Title, a.Body)
		}
	}
	fmt.Fprintf(w, "正在跑的回合：%d\n", st.RunningTurns)
	if st.Selfcheck != "" {
		fmt.Fprintf(w, "自检：%s\n", st.Selfcheck)
	}
	if st.Env != "" {
		fmt.Fprintf(w, "环境：%s\n", st.Env)
	}
	renderWatchdog(w, st.Watchdog)
	for _, line := range st.WebBalances {
		fmt.Fprintf(w, "联网余额：%s\n", line)
	}
	autostart := "读不到"
	if st.Autostart != nil {
		autostart = onOff(*st.Autostart)
	}
	fmt.Fprintf(w, "启动后自动接收：%s；开机自动启动：%s\n", onOff(st.AutoReceive), autostart)
	if st.DataDir != "" {
		fmt.Fprintf(w, "数据目录：%s\n", st.DataDir)
	}
	fmt.Fprintf(w, "版本：%s；进程号：%d\n", st.Version, st.Pid)
}

// renderWatchdog 写守护进程那两行：在不在，以及最近一次异常退出
func renderWatchdog(w io.Writer, wd *ctlproto.WatchdogInfo) {
	if wd == nil {
		return
	}
	switch {
	case wd.Running:
		fmt.Fprintf(w, "守护进程：在（进程 %d，这一轮已重新拉起 %d 次）\n", wd.Pid, wd.Restarts)
	case wd.State == "gave-up":
		fmt.Fprintln(w, "守护进程：已停止拉起（连续异常退出太多次）")
	case wd.Supervised:
		fmt.Fprintln(w, "守护进程：状态文件太旧，可能已经不在了")
	default:
		fmt.Fprintln(w, "守护进程：没有（桌面程序是直接启动的，崩了没人拉起；开机自启开着时下次开机会带上它）")
	}
	if wd.LastIncidentAt != "" {
		at := wd.LastIncidentAt
		if t, err := time.Parse(time.RFC3339, at); err == nil {
			at = t.Local().Format("2006-01-02 15:04:05")
		}
		fmt.Fprintf(w, "最近一次异常退出：%s，%s（记录 logs\\incidents\\%s）\n", at, wd.LastReason, wd.LastFile)
	}
}

func orUnknown(s string) string {
	if s == "" {
		return "未知"
	}
	return s
}

func onOff(b bool) string {
	if b {
		return "开"
	}
	return "关"
}

// phaseName 宿主阶段的中文（和托盘提示、后台状态页同一套说法），后面括号里留英文名方便对照 --json
func phaseName(p string) string {
	zh := map[string]string{
		"starting": "正在检查后台", "archive": "只看历史", "local": "本地可用（QQ 没开）",
		"receiving": "接收中", "switching": "正在切换",
		"blocked": "后台没在运行", "stopping": "正在退出", "stopped": "已停", "demo": "演示数据",
	}[p]
	if zh == "" {
		return p
	}
	return zh + "（" + p + "）"
}

func modeName(m string) string {
	switch m {
	case "off":
		return "只记录（off）"
	case "dry":
		return "试运行（dry）"
	case "speak":
		return "说话（speak）"
	case "":
		return "没设过"
	}
	return m
}

func sourceName(s string) string {
	switch s {
	case "own":
		return "这个群自己的设置"
	case "all":
		return "所有群的设置"
	case "default":
		return "没设过，用默认"
	}
	return strings.TrimSpace(s)
}
