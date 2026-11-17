package main

import (
	"fmt"
	"io"

	"sumika-native/internal/ctlproto"
)

// renderDB 打印 db status。库名、路径、原因都是读出来的，只当数据看。--json 不走这里。
func renderDB(w, stderr io.Writer, r ctlproto.Reply) {
	var v ctlproto.DBStatus
	if !decode(stderr, r, &v) {
		return
	}
	if len(v.Databases) == 0 {
		fmt.Fprintln(w, "（没有记下任何库）")
		return
	}
	for _, d := range v.Databases {
		state := "可用"
		if d.Status != "ok" {
			state = "不可用"
			if d.Reason != "" {
				state += "（" + d.Reason + "）"
			}
		}
		fmt.Fprintf(w, "%s｜%s｜%s\n", ctlproto.Clean(d.Name), ctlproto.Clean(d.Path), state)
		if d.Detail != "" {
			fmt.Fprintf(w, "  %s\n", ctlproto.Clean(d.Detail))
		}
		for _, t := range d.Tracks {
			fmt.Fprintf(w, "  %s：库 %d / min_reader %d / 程序 %d\n", ctlproto.Clean(t.Track), t.Version, t.MinReader, t.Latest)
		}
		if d.Snapshot != "" {
			fmt.Fprintf(w, "  快照：%s\n", ctlproto.Clean(d.Snapshot))
		} else {
			fmt.Fprintln(w, "  快照：这次打开没拍")
		}
	}
}
