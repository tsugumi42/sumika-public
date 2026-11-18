package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"sumika-native/internal/ctlproto"
)

var backupReasonName = map[string]string{"daily": "每天", "manual": "手动", "pre-change": "改动前"}
var backupStatusName = map[string]string{"ok": "校验通过", "bad": "没通过校验（不算数）", "partial": "没做完"}
var backupClassName = map[string]string{"self": "她本人", "material": "素材", "media": "媒体"}
var backupMediaName = map[string]string{"none": "不带媒体", "stickers": "表情包和头像", "all": "全部媒体"}

func human(n int64) string {
	const k = 1024.0
	f := float64(n)
	switch {
	case f >= k*k*k:
		return fmt.Sprintf("%.2f GB", f/(k*k*k))
	case f >= k*k:
		return fmt.Sprintf("%.1f MB", f/(k*k))
	case f >= k:
		return fmt.Sprintf("%.0f KB", f/k)
	}
	return fmt.Sprintf("%d B", n)
}

var backupRowsName = map[string]string{"match": "行数和清单一致", "mismatch": "行数和清单对不上", "unrecorded": "清单没记行数，只核对了完整性"}

func durationText(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%d 毫秒", ms)
	}
	return fmt.Sprintf("%.1f 秒", float64(ms)/1000)
}

func renderDBChecks(w io.Writer, dbs []ctlproto.BackupDBCheck) {
	for _, d := range dbs {
		fmt.Fprintf(w, "  库 %s：完整性 %s｜%s（%d 张表）\n", ctlproto.Clean(d.Path), ctlproto.Clean(d.Integrity), nameOr(backupRowsName, d.Rows), d.Tables)
		for _, m := range d.Mismatch {
			fmt.Fprintln(w, "    "+ctlproto.Clean(m))
		}
	}
}

func nameOr(m map[string]string, k string) string {
	if v, ok := m[k]; ok {
		return v
	}
	return strconvQuote(ctlproto.Clean(k)) // 不认识的原样写出（加引号，不当成我们的话）
}

// renderBackup 打印 backup now、backup list。名字、清单里的文字是读出来的，打印前再清一遍控制字符，只当数据看。--json 不走这里
func renderBackup(w, stderr io.Writer, req ctlproto.Request, r ctlproto.Reply) {
	if len(req.Args) == 0 {
		return
	}
	switch req.Args[0] {
	case ctlproto.BackupNowCmd:
		var v ctlproto.BackupNow
		if !decode(stderr, r, &v) {
			return
		}
		if !v.Settled {
			fmt.Fprintln(w, "已经开始做了，还没做完（它在桌面程序里接着做，不会因为这条命令退出而中断）；用 sumika-ctl backup list 看结果")
			return
		}
		fmt.Fprintf(w, "%s：%s｜%d 个文件，其中 %d 个和上一份共用｜%s\n", ctlproto.Clean(v.Name), nameOr(backupStatusName, v.Status), v.Files, v.Linked, human(v.Bytes))
		for _, p := range v.Problems {
			fmt.Fprintln(w, "  问题："+ctlproto.Clean(p))
		}
	case ctlproto.BackupRestoreCmd:
		var v ctlproto.BackupRestored
		if !decode(stderr, r, &v) {
			return
		}
		renderRestored(w, v)
	case ctlproto.BackupVerifyCmd:
		var v ctlproto.BackupVerified
		if !decode(stderr, r, &v) {
			return
		}
		renderVerified(w, v)
	case ctlproto.BackupListCmd:
		var v ctlproto.BackupList
		if !decode(stderr, r, &v) {
			return
		}
		o := v.Overview
		if o.DirError != "" {
			fmt.Fprintln(w, "备份目录有问题："+ctlproto.Clean(o.DirError))
			return
		}
		fmt.Fprintf(w, "备份目录：%s\n", ctlproto.Clean(o.Dir))
		line := fmt.Sprintf("共 %d 份：每天 %d、手动 %d、改动前 %d", o.Total, o.Daily, o.Manual, o.PreChange)
		if o.Bad > 0 || o.Partial > 0 {
			line += fmt.Sprintf("（其中没通过校验 %d、没做完 %d）", o.Bad, o.Partial)
		}
		fmt.Fprintln(w, line)
		if o.Foreign > 0 {
			fmt.Fprintf(w, "另有 %d 份是别的数据目录写进来的（不算在上面，也不参与这里的轮换）\n", o.Foreign)
		}
		used := "实际占用 " + human(o.UsedBytes) + "（硬链接只算一次"
		if o.UsedEstimated {
			used += "；没有算好的占用，这是按各份清单估算的，下一次备份结束会重算"
		} else if o.UsedAt > 0 {
			used += "；" + time.UnixMilli(o.UsedAt).Format("01-02 15:04") + " 那次备份结束时算的"
		}
		used += "）"
		if o.AlertGB > 0 {
			used += fmt.Sprintf("｜提醒线 backup.alertGB = %g GB", o.AlertGB)
			if float64(o.UsedBytes) > o.AlertGB*(1<<30) {
				used += "（已超过）"
			}
		}
		fmt.Fprintln(w, used)
		if o.FreeKnown {
			fmt.Fprintf(w, "所在盘剩余 %s\n", human(o.FreeBytes))
		} else {
			fmt.Fprintln(w, "所在盘剩余空间：取不到")
		}
		on := "关"
		if o.DailyOn {
			on = "开"
		}
		fmt.Fprintf(w, "每天自动备份：%s｜媒体档：%s\n", on, nameOr(backupMediaName, o.Media))
		fmt.Fprintln(w, "轮换规则："+ctlproto.Clean(o.Rules))
		fmt.Fprintln(w, drillLine(o.Drill))
		if len(v.Rows) == 0 {
			fmt.Fprintln(w, "（还没有备份）")
			return
		}
		fmt.Fprintln(w, "逐份（新的在前）：")
		for _, row := range v.Rows {
			classes := make([]string, len(row.Classes))
			for i, c := range row.Classes {
				classes[i] = nameOr(backupClassName, c)
			}
			inc := strings.Join(classes, "、")
			if inc == "" {
				inc = "（空）"
			}
			fmt.Fprintf(w, "%s  %s  %s  %s｜%s｜%s｜%d 个库｜%s\n", time.UnixMilli(row.At).Format("2006-01-02 15:04"), ctlproto.Clean(row.Name),
				nameOr(backupReasonName, row.Reason), human(row.Bytes), inc, nameOr(backupMediaName, row.Media), row.DBs, nameOr(backupStatusName, row.Status))
			if row.WriteWaitSamples > 0 || row.WriteWaitFailed > 0 {
				line := fmt.Sprintf("    备份期间入库的写锁等待：探了 %d 次，最长等 %d 毫秒", row.WriteWaitSamples, row.WriteWaitMaxMs)
				if row.WriteWaitFailed > 0 {
					line += fmt.Sprintf("，另有 %d 次没探到", row.WriteWaitFailed)
				}
				if row.WriteWaitMaxMs > 1000 {
					line += "（超过 1 秒，入库在备份期间被拖慢了）"
				}
				fmt.Fprintln(w, line)
			}
			if row.SkippedMedia > 0 {
				fmt.Fprintf(w, "    有 %d 个媒体没带上（链接或读不了，清单的 notes 里有）\n", row.SkippedMedia)
			}
			for _, p := range row.Problems {
				fmt.Fprintln(w, "    问题："+ctlproto.Clean(p))
			}
		}
	}
}

// drillLine 是 backup list 总览里的“每周抽验”一行
func drillLine(d ctlproto.BackupDrill) string {
	switch {
	case !d.On && !d.Have:
		return "每周自动抽验：关（跟着每天自动备份；sumika-ctl backup verify 可手动抽验）｜还没有计入的抽验" + otherDrillLine(d)
	case !d.Have:
		return "每周自动抽验：开｜还没有计入的抽验（第一次在下一个没有角色在回复的整点）" + otherDrillLine(d)
	}
	on := "开"
	if !d.On {
		on = "关（跟着每天自动备份；sumika-ctl backup verify 可手动抽验）"
	}
	kind := "每周自动"
	if d.Kind == "manual" {
		kind = "手动"
	}
	res := "通过"
	if !d.OK {
		res = "没通过"
		if len(d.Problems) > 0 {
			res += "：" + ctlproto.Clean(d.Problems[0])
		}
	}
	line := fmt.Sprintf("每周自动抽验：%s｜上次抽验 %s（%s）%s｜%s｜用时 %s", on, time.UnixMilli(d.At).Format("01-02 15:04"), kind, ctlproto.Clean(d.Backup), res, durationText(d.DurationMs))
	if d.HavePass && !d.OK {
		line += "｜上次通过 " + time.UnixMilli(d.PassAt).Format("01-02 15:04")
	} else if !d.HavePass {
		line += "｜还没有通过过的抽验"
	}
	if d.Running {
		line += "｜正在抽验"
	}
	return line + otherDrillLine(d)
}

// otherDrillLine：最近一次不计入“上次通过”的手动抽验（别的数据目录的、不是最新一份好备份的）另起一行，不顶替上面那行
func otherDrillLine(d ctlproto.BackupDrill) string {
	if !d.HaveOther {
		return ""
	}
	res := "通过"
	if !d.OtherOK {
		res = "没通过"
		if d.OtherProblem != "" {
			res += "：" + ctlproto.Clean(d.OtherProblem)
		}
	}
	return fmt.Sprintf("\n另有一次手动抽验不计入“上次通过”（%s）：%s %s｜%s", ctlproto.Clean(d.OtherWhy), time.UnixMilli(d.OtherAt).Format("01-02 15:04"), ctlproto.Clean(d.OtherBackup), res)
}

func renderRestored(w io.Writer, v ctlproto.BackupRestored) {
	if !v.Settled {
		fmt.Fprintf(w, "已经开始恢复，还没做完（它在桌面程序里接着做，不会因为这条命令退出而中断）。目标：%s\n看那里的 .restore\\report.json：核对通过后目录里没有 .restore\\incomplete.json，有它说明还没做完或没通过\n", ctlproto.Clean(v.Target))
		return
	}
	state := "核对通过"
	if !v.OK {
		state = "核对没通过"
	}
	fmt.Fprintf(w, "%s：%s → %s\n", state, ctlproto.Clean(v.Name), ctlproto.Clean(v.Target))
	fmt.Fprintf(w, "%d 个文件，%s，用时 %s\n", v.Files, human(v.Bytes), durationText(v.DurationMs))
	renderDBChecks(w, v.DBs)
	for _, n := range v.Notes {
		fmt.Fprintln(w, "说明："+ctlproto.Clean(n))
	}
	for _, p := range v.Problems {
		fmt.Fprintln(w, "问题："+ctlproto.Clean(p))
	}
	if v.OK {
		fmt.Fprintf(w, "这条命令没有改配置、没有切换、没有碰现在的数据目录。要用它：先退出 Sumika，把 --data（或 SUMIKA_DATA_DIR）指到 %s 再启动；原来的数据目录原样留着，不满意再指回去。\n", ctlproto.Clean(v.Target))
		fmt.Fprintln(w, "密钥提醒：没设主密码的备份，换了 Windows 用户或换了电脑以后 secrets.bin 解不开，令牌和 key 要重新填。")
		fmt.Fprintln(w, "副本提醒：这个目录里有明文配置和全部聊天库，不在审批门的保护范围里（本地私聊里的模型读得到）；不用了就删掉。")
		return
	}
	fmt.Fprintf(w, "%s 里留着未完成标记（.restore\\incomplete.json），Sumika 不会把它当数据目录打开；确认不要它了就整个删掉，换一份备份或排查原因后重新恢复。\n", ctlproto.Clean(v.Target))
}

func renderVerified(w io.Writer, v ctlproto.BackupVerified) {
	if !v.Settled {
		fmt.Fprintln(w, "已经开始抽验，还没做完（它在桌面程序里接着做，不会因为这条命令退出而中断）；用 sumika-ctl backup list 看“上次抽验”")
		return
	}
	state := "抽验通过"
	if !v.OK {
		state = "抽验没通过"
	}
	fmt.Fprintf(w, "%s：%s｜%d 个文件，%s｜用时 %s\n", state, ctlproto.Clean(v.Name), v.Files, human(v.Bytes), durationText(v.DurationMs))
	if !v.Counts {
		fmt.Fprintf(w, "这次不计入“上次通过”（%s）：核对照做、结果照记，但不推迟每周抽验、也不让自检转绿\n", ctlproto.Clean(v.NotCountedWhy))
	}
	renderDBChecks(w, v.DBs)
	for _, p := range v.Problems {
		fmt.Fprintln(w, "问题："+ctlproto.Clean(p))
	}
}

// renderBackupFailure：restore、verify 核对没通过时，失败的回复里带着当时的核对结果，照样打印出来
func renderBackupFailure(w io.Writer, req ctlproto.Request, r ctlproto.Reply) {
	if len(req.Args) == 0 || len(r.Data) == 0 {
		return
	}
	switch req.Args[0] {
	case ctlproto.BackupRestoreCmd:
		var v ctlproto.BackupRestored
		if json.Unmarshal(r.Data, &v) == nil && v.Settled {
			renderRestored(w, v)
		}
	case ctlproto.BackupVerifyCmd:
		var v ctlproto.BackupVerified
		if json.Unmarshal(r.Data, &v) == nil && v.Settled {
			renderVerified(w, v)
		}
	}
}
