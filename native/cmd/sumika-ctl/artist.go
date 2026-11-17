package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"sumika-native/internal/ctlproto"
)

// renderArtist 打印 artist 的结果。名字、账号、线索名是外面来的原文，打印前再清一遍控制字符，只当数据看。
func renderArtist(w, stderr io.Writer, req ctlproto.Request, r ctlproto.Reply) {
	if len(req.Args) == 0 {
		return
	}
	switch req.Args[0] {
	case ctlproto.ArtistListCmd:
		var v ctlproto.ArtistListResult
		if !decode(stderr, r, &v) {
			return
		}
		fmt.Fprintf(w, "平台 %s｜生效 %d、待确认 %d、不是 %d｜列出 %s\n", ctlproto.Clean(v.Platform),
			v.Counts["confirmed"], v.Counts["pending"], v.Counts["declined"], artistStatusName(v.Status))
		if len(v.Rows) == 0 {
			fmt.Fprintln(w, "（没有）")
		}
		for _, row := range v.Rows {
			line := fmt.Sprintf("%s  @%s  %s  %s（%s）", row.ID, ctlproto.Clean(row.Handle), ctlproto.Clean(row.Name), artistStatusName(row.Status), artistSourceName(row.Source))
			if row.Score > 0 {
				line += fmt.Sprintf("  分数 %g", row.Score)
			}
			if len(row.Clues) > 0 {
				clues := make([]string, len(row.Clues))
				for i, c := range row.Clues {
					clues[i] = ctlproto.Clean(c)
				}
				line += "  线索：" + strings.Join(clues, "、")
			}
			if row.At > 0 {
				line += "  " + time.UnixMilli(row.At).Format("01-02 15:04")
			}
			fmt.Fprintln(w, line)
		}
	case ctlproto.ArtistImportCmd, ctlproto.ArtistConfirmCmd, ctlproto.ArtistRemoveCmd:
		var v ctlproto.ArtistChange
		if !decode(stderr, r, &v) {
			return
		}
		switch {
		case req.Args[0] == ctlproto.ArtistConfirmCmd:
			fmt.Fprintf(w, "确认 %d 个，本来就生效 %d 个\n", v.Confirmed, v.Already)
		case req.Args[0] == ctlproto.ArtistRemoveCmd:
			fmt.Fprintf(w, "移除 %d 个，本来就是“不是”%d 个\n", v.Removed, v.Already)
		case req.Artist != nil && req.Artist.Undo != "":
			fmt.Fprintf(w, "已撤回：恢复 %d 行，删除 %d 行\n", v.Restored, v.Deleted)
		case v.Batch == "":
			fmt.Fprintf(w, "没有变化（本来就生效 %d 个，本来就是“不是”%d 个，跳过 %d 个，格式不对 %d 行）\n", v.Already, v.AlreadyDeclined, v.Skipped, v.Malformed)
		default:
			fmt.Fprintf(w, "导入完成，批次 %s：新增 %d、待确认改为生效 %d、本来就生效 %d、标为“不是”%d、本来就是“不是”%d、跳过 %d、格式不对 %d 行\n撤回用：sumika-ctl artist import --undo %s\n",
				v.Batch, v.Added, v.Upgraded, v.Already, v.Declined, v.AlreadyDeclined, v.Skipped, v.Malformed, v.Batch)
		}
	}
}

func artistStatusName(s string) string {
	switch s {
	case "confirmed":
		return "生效"
	case "pending":
		return "待确认"
	case "declined":
		return "不是"
	case "all":
		return "全部"
	}
	return ctlproto.Clean(s)
}

func artistSourceName(s string) string {
	switch s {
	case "import":
		return "导入"
	case "auto":
		return "按需发现"
	case "manual":
		return "手动"
	}
	return ctlproto.Clean(s)
}
